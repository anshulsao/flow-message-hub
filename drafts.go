package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// "+ New" in a channel: launch a fresh, unbound Claude session in a new iTerm
// tab. Claude creates the flow task itself (the flow skill interviews you),
// then binds the session with `flow do --here <slug>`. Until then the session
// shows in the channel as a draft card, read by the session id we picked.

const (
	draftPrefix    = "draft-"
	draftStartWait = 3 * time.Minute // a draft not live after this is abandoned
	draftKeepBound = time.Hour       // forget bound drafts after this
)

type draft struct {
	SID     string    `json:"sid"`
	Project string    `json:"project"` // "" = floating channel
	Created time.Time `json:"created"`
}

// draftStore keeps launched drafts in a small JSON file so they survive a
// hub restart. A nil store is empty and ignores writes.
type draftStore struct {
	mu   sync.Mutex
	path string
	m    map[string]draft
}

func newDraftStore(path string) *draftStore {
	d := &draftStore{path: path, m: map[string]draft{}}
	if b, err := os.ReadFile(path); err == nil {
		var list []draft
		if json.Unmarshal(b, &list) == nil {
			for _, x := range list {
				d.m[x.SID] = x
			}
		}
	}
	return d
}

func (d *draftStore) list() []draft {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]draft, 0, len(d.m))
	for _, x := range d.m {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

func (d *draftStore) get(sid string) (draft, bool) {
	if d == nil {
		return draft{}, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	x, ok := d.m[sid]
	return x, ok
}

func (d *draftStore) add(x draft) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.m[x.SID] = x
	d.saveLocked()
	d.mu.Unlock()
}

func (d *draftStore) remove(sid string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if _, ok := d.m[sid]; ok {
		delete(d.m, sid)
		d.saveLocked()
	}
	d.mu.Unlock()
}

func (d *draftStore) saveLocked() {
	list := make([]draft, 0, len(d.m))
	for _, x := range d.m {
		list = append(list, x)
	}
	if b, err := json.MarshalIndent(list, "", "  "); err == nil {
		_ = os.WriteFile(d.path, b, 0o600)
	}
}

// draftState: "bound" (a task now carries the session; slug returned),
// "abandoned" (never came up, or closed before a task was made), or "open".
func draftState(d draft, tasks []chanTask, live bool, now time.Time) (string, string) {
	for _, t := range tasks {
		if t.SessionID == d.SID {
			return "bound", t.Slug
		}
	}
	if !live && now.Sub(d.Created) > draftStartWait {
		return "abandoned", ""
	}
	return "open", ""
}

func draftSID(slug string) (string, bool) {
	if !strings.HasPrefix(slug, draftPrefix) {
		return "", false
	}
	sid := strings.TrimPrefix(slug, draftPrefix)
	return sid, validSlug(sid)
}

func draftChannel(d draft) string {
	if d.Project == "" {
		return floatingSlug
	}
	return d.Project
}

func draftCard(d draft, latest, lastAt string) Card {
	last := d.Created.Format(time.RFC3339)
	if lastAt != "" {
		last = lastAt
	}
	return Card{
		Slug: draftPrefix + d.SID, Name: "New session (Claude will create the task)",
		Status: "in-progress", RunState: "live", Section: "active", Priority: "medium",
		Latest: latest, LastActivity: last, Created: d.Created.Format(time.RFC3339), Draft: true,
	}
}

// ---------- launching ----------

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func draftPrompt(project string) string {
	where := "Ask me which project it belongs to, if any, then create it with `flow add task`"
	if project != "" {
		where = "Create it with `flow add task \"<name>\" --project " + project + "`"
	}
	return "Start a new flow task with me. Use the flow skill: ask me what this is about. " +
		where + ", and write its brief with me. Then bind this session to it with " +
		"`flow do --here <slug>` and keep working on it here."
}

func draftCommand(cwd, sid, prompt string) string {
	return "cd " + shellQuote(cwd) + " && claude --session-id " + sid + " " + shellQuote(prompt)
}

type sessionLauncher interface{ Launch(cwd, cmd string) error }

type itermLauncher struct{}

// Launch opens a new iTerm tab (or window, if none) and runs cmd in it.
func (itermLauncher) Launch(cwd, cmd string) error {
	script := fmt.Sprintf(`tell application "iTerm2"
  activate
  if (count of windows) = 0 then
    set w to (create window with default profile)
  else
    set w to current window
    tell w to create tab with default profile
  end if
  tell current session of w to write text %s
end tell`, appleQuote(cmd))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "osascript", "-e", script).CombinedOutput(); err != nil {
		return fmt.Errorf("osascript: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func decodeJSON(r io.Reader, v any) error { return json.NewDecoder(r).Decode(v) }

// registerNewRoute: POST /api/new {"channel": "<project slug>|_floating"}.
func registerNewRoute(mux *http.ServeMux, st *store, l sessionLauncher) {
	mux.HandleFunc("/api/new", func(w http.ResponseWriter, r *http.Request) {
		reply := func(code int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(v)
		}
		if r.Method != http.MethodPost {
			reply(http.StatusMethodNotAllowed, map[string]any{"ok": false, "message": "POST only"})
			return
		}
		var body struct {
			Channel string `json:"channel"`
		}
		if err := decodeJSON(r.Body, &body); err != nil {
			reply(400, map[string]any{"ok": false, "message": "bad json"})
			return
		}
		project := ""
		if body.Channel != floatingSlug {
			ps, err := st.projects()
			if err != nil {
				httpErr(w, err)
				return
			}
			for _, p := range ps {
				if p.Slug == body.Channel {
					project = p.Slug
				}
			}
			if project == "" {
				reply(404, map[string]any{"ok": false, "message": "no such channel"})
				return
			}
		}
		cwd, _ := os.UserHomeDir()
		if wd := st.projectWorkDir(project); wd != "" {
			if fi, err := os.Stat(wd); err == nil && fi.IsDir() {
				cwd = wd
			}
		}
		sid := newUUID()
		if err := l.Launch(cwd, draftCommand(cwd, sid, draftPrompt(project))); err != nil {
			reply(500, map[string]any{"ok": false, "message": err.Error()})
			return
		}
		st.drafts.add(draft{SID: sid, Project: project, Created: time.Now()})
		reply(200, map[string]any{"ok": true, "sid": sid, "slug": draftPrefix + sid, "cwd": cwd})
	})
}
