package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Send a prompt to a live, interactive Claude Code session by "typing" it into
// the iTerm tab that hosts it. Sessions are found through the official
// `claude agents --json`; the tab is matched by the session process's tty.

const promptMax = 20000

var errTabNotFound = errors.New("no iTerm tab is showing that session's terminal")

// sanitizePrompt drops control characters (ESC, Ctrl-C, …) so a message can
// never smuggle keystrokes or end the bracketed paste early.
func sanitizePrompt(s string) (string, error) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteRune(' ')
		case unicode.IsControl(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", errors.New("message is empty")
	}
	if len(out) > promptMax {
		return "", fmt.Errorf("message is longer than %d characters", promptMax)
	}
	return out, nil
}

func appleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// itermScript pastes text (bracketed, so newlines don't submit early) into
// the iTerm session on tty, then presses Enter. Prints ok or miss.
func itermScript(tty, text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = appleQuote(l)
	}
	body := strings.Join(lines, " & linefeed & ")
	return fmt.Sprintf(`set ESC to (ASCII character 27)
tell application "iTerm2"
  repeat with w in windows
    repeat with t in tabs of w
      repeat with s in sessions of t
        if tty of s is %s then
          tell s to write text (ESC & "[200~" & %s & ESC & "[201~") newline no
          delay 0.15
          tell s to write text ""
          return "ok"
        end if
      end repeat
    end repeat
  end repeat
  return "miss"
end tell`, appleQuote(tty), body)
}

type itermSender struct{}

func (itermSender) Send(tty, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "osascript", "-e", itermScript(tty, text)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return errTabNotFound
	}
	return nil
}

type agentInfo struct {
	Kind   string `json:"kind"`
	PID    int    `json:"pid"`
	Status string `json:"status"`
}

func parseAgents(raw []byte) (map[string]agentInfo, error) {
	var rows []struct {
		agentInfo
		PID       *int    `json:"pid"`
		Status    *string `json:"status"`
		SessionID string  `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := make(map[string]agentInfo, len(rows))
	for _, r := range rows {
		a := agentInfo{Kind: r.Kind}
		if r.PID != nil {
			a.PID = *r.PID
		}
		if r.Status != nil {
			a.Status = *r.Status
		}
		if r.SessionID != "" {
			out[r.SessionID] = a
		}
	}
	return out, nil
}

// agentsLister runs the official `claude agents --json`, cached briefly so
// UI polling doesn't spawn a process every few seconds.
type agentsLister struct {
	mu   sync.Mutex
	at   time.Time
	last map[string]agentInfo
}

func (l *agentsLister) list() (map[string]agentInfo, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last != nil && time.Since(l.at) < 3*time.Second {
		return l.last, nil
	}
	bin := os.Getenv("CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, bin, "agents", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("claude agents --json: %v", err)
	}
	m, err := parseAgents(raw)
	if err != nil {
		return nil, err
	}
	l.last, l.at = m, time.Now()
	return m, nil
}

// ttyOfClaude returns /dev/ttyNNN for pid, and refuses if pid isn't claude.
func ttyOfClaude(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "tty=,command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", fmt.Errorf("process %d is gone", pid)
	}
	f := strings.Fields(string(out))
	if len(f) < 2 || !strings.Contains(strings.Join(f[1:], " "), "claude") {
		return "", fmt.Errorf("process %d is not claude", pid)
	}
	if f[0] == "??" || f[0] == "?" {
		return "", errors.New("session has no terminal")
	}
	return "/dev/" + strings.TrimPrefix(f[0], "/dev/"), nil
}

type promptSender interface{ Send(tty, text string) error }

type promptDeps_ struct {
	agents func() (map[string]agentInfo, error)
	tty    func(pid int) (string, error)
	sender promptSender
}

type promptTarget struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	Status string `json:"status,omitempty"` // idle | busy | waiting
	TTY    string `json:"-"`
}

func resolvePromptTarget(d promptDeps_, t chanTask) promptTarget {
	switch {
	case t.SessionID == "":
		return promptTarget{Reason: "no session yet"}
	case t.Status == "done":
		return promptTarget{Reason: "task is done"}
	}
	agents, err := d.agents()
	if err != nil {
		return promptTarget{Reason: "can't list Claude sessions: " + err.Error()}
	}
	a, ok := agents[t.SessionID]
	if !ok || a.PID == 0 {
		return promptTarget{Reason: "session isn't open in a terminal"}
	}
	if a.Kind != "interactive" {
		return promptTarget{Reason: "not an interactive session (" + a.Kind + ")"}
	}
	tty, err := d.tty(a.PID)
	if err != nil || tty == "" {
		if t.AutoRunStatus != "" {
			return promptTarget{Reason: "headless auto run: no terminal to type into (message it via the flow bus)"}
		}
		return promptTarget{Reason: "can't find the session's terminal"}
	}
	return promptTarget{OK: true, Status: a.Status, TTY: tty}
}

func registerPromptRoute(mux *http.ServeMux, st *store, d promptDeps_) {
	mux.HandleFunc("/api/prompt/", func(w http.ResponseWriter, r *http.Request) {
		slug := strings.TrimPrefix(r.URL.Path, "/api/prompt/")
		tasks, err := st.chanTasks()
		if err != nil {
			httpErr(w, err)
			return
		}
		var task *chanTask
		for i := range tasks {
			if tasks[i].Slug == slug {
				task = &tasks[i]
			}
		}
		if task == nil {
			http.NotFound(w, r)
			return
		}
		reply := func(code int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(v)
		}
		switch r.Method {
		case http.MethodGet:
			reply(200, resolvePromptTarget(d, *task))
		case http.MethodPost:
			var body struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				reply(400, map[string]any{"ok": false, "message": "bad json"})
				return
			}
			text, err := sanitizePrompt(body.Text)
			if err != nil {
				reply(400, map[string]any{"ok": false, "message": err.Error()})
				return
			}
			tg := resolvePromptTarget(d, *task)
			if !tg.OK {
				reply(409, map[string]any{"ok": false, "message": tg.Reason})
				return
			}
			if err := d.sender.Send(tg.TTY, text); err != nil {
				code := 500
				if errors.Is(err, errTabNotFound) {
					code = 409
				}
				reply(code, map[string]any{"ok": false, "message": err.Error()})
				return
			}
			msg := "sent"
			if tg.Status == "busy" {
				msg = "queued: the session is mid-turn and will pick it up next"
			}
			reply(200, map[string]any{"ok": true, "message": msg})
		default:
			http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		}
	})
}

// guardWrites refuses state-changing requests from other websites: a page on
// any site could otherwise POST to 127.0.0.1 and type into your terminals.
// Browsers always send Origin on cross-site POSTs; same-origin and non-browser
// clients (curl) pass.
func guardWrites(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
				http.Error(w, "cross-origin write refused", http.StatusForbidden)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}
