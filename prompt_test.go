package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSanitizePrompt(t *testing.T) {
	cases := map[string]string{
		"hello":                  "hello",
		"  spaced  ":             "spaced",
		"a\r\nb":                 "a\nb",
		"esc\x1b[31mred\x03":     "esc[31mred",
		"tab\there":              "tab here",
		"keep\nnewlines\n\nok":   "keep\nnewlines\n\nok",
		"paste-end \x1b[201~ hi": "paste-end [201~ hi",
	}
	for in, want := range cases {
		got, err := sanitizePrompt(in)
		if err != nil || got != want {
			t.Errorf("sanitize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := sanitizePrompt(" \x1b\x03 "); err == nil {
		t.Error("empty after cleaning should error")
	}
	if _, err := sanitizePrompt(strings.Repeat("x", promptMax+1)); err == nil {
		t.Error("over-long prompt should error")
	}
}

func TestITermScript_SingleAndMultiLine(t *testing.T) {
	one := itermScript("/dev/ttys003", `say "hi" \ ok`)
	if !strings.Contains(one, `tty of s is "/dev/ttys003"`) {
		t.Errorf("tty match missing:\n%s", one)
	}
	if !strings.Contains(one, `ESC & "[200~" & "say \"hi\" \\ ok" & ESC & "[201~"`) {
		t.Errorf("body not escaped inside bracketed paste:\n%s", one)
	}
	if !strings.Contains(one, `newline no`) || !strings.Contains(one, `write text ""`) {
		t.Errorf("must paste without newline then press Enter:\n%s", one)
	}
	multi := itermScript("/dev/ttys003", "line A\nline B")
	if !strings.Contains(multi, `"line A" & linefeed & "line B"`) {
		t.Errorf("newlines should be joined with linefeed:\n%s", multi)
	}
	if strings.Contains(multi, "line A\nline B") {
		t.Errorf("raw newline must not appear inside the AppleScript string")
	}
}

func TestParseAgents(t *testing.T) {
	raw := `[{"kind":"interactive","pid":36459,"status":"idle","sessionId":"s1"},
	          {"kind":"background","pid":null,"status":null,"sessionId":"s2","id":"s2"}]`
	m, err := parseAgents([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if a := m["s1"]; a.Kind != "interactive" || a.PID != 36459 || a.Status != "idle" {
		t.Errorf("s1: %+v", a)
	}
	if a := m["s2"]; a.Kind != "background" || a.PID != 0 {
		t.Errorf("s2: %+v", a)
	}
}

type fakeSender struct {
	tty, text string
	err       error
}

func (f *fakeSender) Send(tty, text string) error { f.tty, f.text = tty, text; return f.err }

func promptDeps(agents map[string]agentInfo, tty string, sender *fakeSender) promptDeps_ {
	return promptDeps_{
		agents: func() (map[string]agentInfo, error) { return agents, nil },
		tty: func(pid int) (string, error) {
			if tty == "" {
				return "", errors.New("no tty")
			}
			return tty, nil
		},
		sender: sender,
	}
}

func TestResolvePromptTarget(t *testing.T) {
	live := map[string]agentInfo{"sid": {Kind: "interactive", PID: 42, Status: "busy"}}
	cases := []struct {
		name   string
		task   chanTask
		agents map[string]agentInfo
		tty    string
		ok     bool
		reason string
	}{
		{"live interactive", chanTask{SessionID: "sid", Status: "in-progress"}, live, "/dev/ttys003", true, ""},
		{"no session", chanTask{Status: "backlog"}, live, "/dev/ttys003", false, "no session"},
		{"done", chanTask{SessionID: "sid", Status: "done"}, live, "/dev/ttys003", false, "task is done"},
		{"not running", chanTask{SessionID: "other", Status: "in-progress"}, live, "/dev/ttys003", false, "session isn't open"},
		{"background", chanTask{SessionID: "sid", Status: "in-progress"}, map[string]agentInfo{"sid": {Kind: "background", PID: 42}}, "/dev/ttys003", false, "not an interactive"},
		{"no tty", chanTask{SessionID: "sid", Status: "in-progress"}, live, "", false, "terminal"},
		{"headless auto run", chanTask{SessionID: "sid", Status: "in-progress", AutoRunStatus: "running"}, live, "", false, "auto run"},
	}
	for _, c := range cases {
		tg := resolvePromptTarget(promptDeps(c.agents, c.tty, &fakeSender{}), c.task)
		if tg.OK != c.ok || (c.reason != "" && !strings.Contains(tg.Reason, c.reason)) {
			t.Errorf("%s: %+v", c.name, tg)
		}
	}
	tg := resolvePromptTarget(promptDeps(live, "/dev/ttys003", &fakeSender{}), chanTask{SessionID: "sid", Status: "in-progress"})
	if tg.TTY != "/dev/ttys003" || tg.Status != "busy" {
		t.Errorf("target details: %+v", tg)
	}
}

func TestPromptRoute(t *testing.T) {
	st, err := openStore(seedDB(t)) // task "a" is in-progress with session sid-a
	if err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{}
	deps := promptDeps(map[string]agentInfo{"sid-a": {Kind: "interactive", PID: 7, Status: "idle"}}, "/dev/ttys009", sender)
	mux := http.NewServeMux()
	registerPromptRoute(mux, st, deps)
	srv := httptest.NewServer(guardWrites(mux))
	defer srv.Close()

	var info promptTarget
	if code := getJSON(t, srv.URL+"/api/prompt/a", &info); code != 200 || !info.OK || info.Status != "idle" {
		t.Fatalf("GET: %d %+v", code, info)
	}
	post := func(body, origin string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/prompt/a", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post(`{"text":"status\u001b"}`, srv.URL); c != 200 || sender.tty != "/dev/ttys009" || sender.text != "status" {
		t.Fatalf("send: code=%d sender=%+v", c, sender)
	}
	if c := post(`{"text":"x"}`, "https://evil.example"); c != http.StatusForbidden {
		t.Errorf("cross-site POST must be refused, got %d", c)
	}
	if c := post(`{"text":"   "}`, ""); c != 400 {
		t.Errorf("empty text: %d", c)
	}
	sender.err = errTabNotFound
	if c := post(`{"text":"hi"}`, ""); c != 409 {
		t.Errorf("tab not found should be 409, got %d", c)
	}
}

func TestGuardWrites_AllowsSameOriginAndGETs(t *testing.T) {
	h := guardWrites(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	cases := []struct {
		method, origin, host string
		want                 int
	}{
		{"GET", "https://evil.example", "127.0.0.1:8787", 204},
		{"POST", "", "127.0.0.1:8787", 204},
		{"POST", "http://127.0.0.1:8787", "127.0.0.1:8787", 204},
		{"POST", "http://localhost:8787", "127.0.0.1:8787", 403},
		{"POST", "https://evil.example", "127.0.0.1:8787", 403},
		{"POST", "null", "127.0.0.1:8787", 403},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://"+c.host+"/api/x", nil)
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s origin=%q: %d want %d", c.method, c.origin, rec.Code, c.want)
		}
	}
}
