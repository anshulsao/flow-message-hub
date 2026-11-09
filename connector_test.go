package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The hub must find the flowhub connector's secret exactly where flow's
// SecretStore puts it: keychain (service flow-connector-flowhub, account
// flow-<12 hex sha256(resolved root)>), else $FLOW_ROOT/secrets/flowhub.
func TestKeychainAccount_MatchesFlow(t *testing.T) {
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root) // macOS: /var -> /private/var
	sum := sha256.Sum256([]byte(filepath.Clean(resolved)))
	want := "flow-" + hex.EncodeToString(sum[:])[:12]
	if got := keychainAccount(root); got != want {
		t.Fatalf("account = %q, want %q", got, want)
	}
	// a symlink to the same root resolves to the same account
	link := filepath.Join(t.TempDir(), "lnk")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if got := keychainAccount(link); got != want {
		t.Fatalf("symlinked root account = %q, want %q", got, want)
	}
}

func TestConnectorSecret_KeychainThenFile(t *testing.T) {
	root := t.TempDir()
	var gotSvc, gotAcct string
	cs := &connectorSecret{root: root, keychainGet: func(svc, acct string) (string, error) {
		gotSvc, gotAcct = svc, acct
		return "kc-secret", nil
	}}
	if s := cs.Get(); s != "kc-secret" {
		t.Fatalf("keychain secret = %q", s)
	}
	if gotSvc != "flow-connector-flowhub" || gotAcct != keychainAccount(root) {
		t.Fatalf("lookup used svc=%q acct=%q", gotSvc, gotAcct)
	}

	// keychain miss → file fallback, trimmed
	cs.keychainGet = func(string, string) (string, error) { return "", errors.New("miss") }
	if s := cs.Get(); s != "" {
		t.Fatalf("no secret anywhere should be empty, got %q", s)
	}
	_ = os.MkdirAll(filepath.Join(root, "secrets"), 0o700)
	_ = os.WriteFile(filepath.Join(root, "secrets", "flowhub"), []byte("file-secret\n"), 0o600)
	if s := cs.Get(); s != "file-secret" {
		t.Fatalf("file secret = %q", s)
	}
}

func askReq(body string, mut func(*http.Request)) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/connector/ask", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Flow-Connector", "1")
	if mut != nil {
		mut(r)
	}
	return r
}

func TestConnectorAsk_RecordsPing(t *testing.T) {
	p := newPingSet(10)
	h := connectorAskHandler(p, liveUI())
	w := httptest.NewRecorder()
	h(w, askReq(`{"id":"abc123","kind":"ask","body":"ship?","options":["Yes","No"]}`, nil))
	if w.Code != 200 {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	if got := p.list(); len(got) != 1 || got[0] != "abc123" {
		t.Fatalf("pinged = %v", got)
	}
}

func TestConnectorAsk_TestKindIsOKButNotRecorded(t *testing.T) {
	p := newPingSet(10)
	w := httptest.NewRecorder()
	connectorAskHandler(p, liveUI())(w, askReq(`{"id":"test","kind":"test"}`, nil))
	if w.Code != 200 || len(p.list()) != 0 {
		t.Fatalf("code=%d pinged=%v", w.Code, p.list())
	}
}

func TestConnectorAsk_Guards(t *testing.T) {
	cases := map[string]func(*http.Request){
		"browser origin": func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:8787") },
		"missing header": func(r *http.Request) { r.Header.Del("X-Flow-Connector") },
		"non-loopback":   func(r *http.Request) { r.RemoteAddr = "10.0.0.5:1234" },
		"no id":          nil,
	}
	for name, mut := range cases {
		p := newPingSet(10)
		body := `{"id":"x1","kind":"ask"}`
		if name == "no id" {
			body = `{"kind":"ask"}`
		}
		w := httptest.NewRecorder()
		connectorAskHandler(p, liveUI())(w, askReq(body, mut))
		if w.Code < 400 {
			t.Errorf("%s: want rejection, got %d", name, w.Code)
		}
		if len(p.list()) != 0 {
			t.Errorf("%s: must not record a ping", name)
		}
	}
	// GET is refused
	p := newPingSet(10)
	r := askReq("", nil)
	r.Method = http.MethodGet
	w := httptest.NewRecorder()
	connectorAskHandler(p, liveUI())(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", w.Code)
	}
}

func TestPingSet_CapsOldest(t *testing.T) {
	p := newPingSet(2)
	p.add("a")
	p.add("b")
	p.add("a") // dup ignored
	p.add("c")
	got := strings.Join(p.list(), ",")
	if got != "b,c" {
		t.Fatalf("pinged = %s", got)
	}
}

// ---- thread writes: connector path vs fallback ----

type fakeWriter struct {
	calls             []string
	seenErr, replyErr error
}

func (f *fakeWriter) HumanSeen(tok, id string) error {
	f.calls = append(f.calls, "seen "+tok+" "+id)
	return f.seenErr
}
func (f *fakeWriter) HumanReply(tok, id, text string) error {
	f.calls = append(f.calls, "hreply "+tok+" "+id+" "+text)
	return f.replyErr
}
func (f *fakeWriter) Reply(task, text string) error {
	f.calls = append(f.calls, "msg "+task+" "+text)
	return nil
}

func TestThreadReply_UsesHumanReplyOnNewestUnansweredAsk(t *testing.T) {
	fw := &fakeWriter{}
	th := &InboxThread{Task: "deploy", AskID: "ask2"}
	via, err := threadReply(fw, "tok", th, "deploy", "Yes — go")
	if err != nil || via != "connector" {
		t.Fatalf("via=%q err=%v", via, err)
	}
	if strings.Join(fw.calls, "|") != "hreply tok ask2 Yes — go" {
		t.Fatalf("calls %v", fw.calls)
	}
}

func TestThreadReply_FallsBackWithoutSecretOrAsk(t *testing.T) {
	fw := &fakeWriter{}
	via, _ := threadReply(fw, "", &InboxThread{AskID: "ask2"}, "deploy", "hi")
	if via != "message" || fw.calls[0] != "msg deploy hi" {
		t.Fatalf("no secret: via=%q calls=%v", via, fw.calls)
	}
	fw = &fakeWriter{}
	via, _ = threadReply(fw, "tok", &InboxThread{}, "deploy", "hi")
	if via != "message" || fw.calls[0] != "msg deploy hi" {
		t.Fatalf("no open ask: via=%q calls=%v", via, fw.calls)
	}
}

func TestThreadReply_FallsBackWhenFlowSaysNotAConnector(t *testing.T) {
	fw := &fakeWriter{replyErr: errNotConnector}
	via, err := threadReply(fw, "stale", &InboxThread{AskID: "a"}, "deploy", "hi")
	if err != nil || via != "message" {
		t.Fatalf("via=%q err=%v", via, err)
	}
	if len(fw.calls) != 2 || fw.calls[1] != "msg deploy hi" {
		t.Fatalf("calls %v", fw.calls)
	}
}

func TestThreadSeen_ConnectorAndNoop(t *testing.T) {
	fw := &fakeWriter{}
	seen, hint, err := threadSeen(fw, "tok", []string{"a", "b"})
	if err != nil || hint != "" || strings.Join(seen, ",") != "a,b" || len(fw.calls) != 2 {
		t.Fatalf("seen=%v hint=%q err=%v calls=%v", seen, hint, err, fw.calls)
	}
	fw = &fakeWriter{}
	seen, hint, err = threadSeen(fw, "", []string{"a"})
	if err != nil || len(seen) != 0 || hint == "" || len(fw.calls) != 0 {
		t.Fatalf("no secret must be a hinted no-op: seen=%v hint=%q calls=%v", seen, hint, fw.calls)
	}
	fw = &fakeWriter{seenErr: errNotConnector}
	seen, hint, err = threadSeen(fw, "stale", []string{"a"})
	if err != nil || len(seen) != 0 || hint == "" {
		t.Fatalf("not-a-connector must be a hinted no-op: seen=%v hint=%q err=%v", seen, hint, err)
	}
}

// ---- flowExec human calls: token via env only, never in args/errors ----

func fakeFlow(t *testing.T, out string, rc int) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	bin = filepath.Join(dir, "flow")
	script := "#!/bin/sh\necho \"ARGS:$*\" >>" + log + "\necho \"TOK:$FLOW_CONNECTOR_TOKEN\" >>" + log +
		"\necho \"ROOT:$FLOW_ROOT\" >>" + log + "\necho '" + out + "'\nexit " + string(rune('0'+rc)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestFlowExec_HumanReplyPassesTokenByEnv(t *testing.T) {
	bin, log := fakeFlow(t, "answered", 0)
	t.Setenv("FLOW_ROOT", "")
	os.Unsetenv("FLOW_ROOT")
	f := &flowExec{bin: bin, root: "/tmp/flowroot"}
	if err := f.HumanReply("s3cr3t", "id1", "Yes — ok"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	got := string(b)
	for _, want := range []string{"ARGS:human reply id1 Yes — ok", "TOK:s3cr3t", "ROOT:/tmp/flowroot"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(strings.SplitN(got, "\n", 2)[0], "s3cr3t") {
		t.Fatal("token leaked into argv")
	}
}

func TestFlowExec_NotAConnectorIsTypedAndTokenFree(t *testing.T) {
	bin, _ := fakeFlow(t, "error: not a connector — flow human reply|seen need FLOW_CONNECTOR_TOKEN", 1)
	f := &flowExec{bin: bin}
	err := f.HumanSeen("s3cr3t", "id1")
	if !errors.Is(err, errNotConnector) {
		t.Fatalf("want errNotConnector, got %v", err)
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Fatal("token in error string")
	}
}

// /api/inbox polls every ~2s; Present() must not exec `security` each time.
// Writes keep using the fresh Get().
func TestConnectorSecret_PresentIsCached(t *testing.T) {
	calls := 0
	now := time.Unix(1000, 0)
	cs := &connectorSecret{root: t.TempDir(), now: func() time.Time { return now },
		keychainGet: func(string, string) (string, error) { calls++; return "s", nil }}
	if !cs.Present() || !cs.Present() || !cs.Present() {
		t.Fatal("want present")
	}
	if calls != 1 {
		t.Fatalf("Present looked up %d times within the TTL, want 1", calls)
	}
	now = now.Add(31 * time.Second)
	cs.Present()
	if calls != 2 {
		t.Fatalf("after TTL want a refresh, calls=%d", calls)
	}
	cs.Get()
	if calls != 3 {
		t.Fatalf("Get must always read fresh, calls=%d", calls)
	}
}

// liveUI is a presence tracker that has just seen a UI poll.
func liveUI() *uiPresence {
	u := newUIPresence(10 * time.Second)
	u.touch()
	return u
}

func TestConnectorAsk_NoOpenTabIs503AndNotRecorded(t *testing.T) {
	p := newPingSet(10)
	u := newUIPresence(10 * time.Second) // no UI poll yet
	w := httptest.NewRecorder()
	connectorAskHandler(p, u)(w, askReq(`{"id":"abc","kind":"ask"}`, nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code %d, want 503", w.Code)
	}
	var body struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.OK || body.Reason != "no open hub tab" {
		t.Fatalf("body = %s (err %v)", w.Body, err)
	}
	if len(p.list()) != 0 {
		t.Fatalf("must not record a ping without a tab: %v", p.list())
	}
}

func TestConnectorAsk_TabWindowExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	u := newUIPresence(10 * time.Second)
	u.now = func() time.Time { return now }
	u.touch()
	p := newPingSet(10)

	now = now.Add(9 * time.Second)
	w := httptest.NewRecorder()
	connectorAskHandler(p, u)(w, askReq(`{"id":"a1","kind":"ask"}`, nil))
	if w.Code != 200 {
		t.Fatalf("within window: code %d", w.Code)
	}

	now = now.Add(2 * time.Second) // 11s since the poll
	w = httptest.NewRecorder()
	connectorAskHandler(p, u)(w, askReq(`{"id":"a2","kind":"ask"}`, nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("after window: code %d", w.Code)
	}
	if got := p.list(); len(got) != 1 || got[0] != "a1" {
		t.Fatalf("pinged = %v", got)
	}
}

func TestConnectorAsk_TestKindOKWithoutTab(t *testing.T) {
	p := newPingSet(10)
	w := httptest.NewRecorder()
	connectorAskHandler(p, newUIPresence(10*time.Second))(w, askReq(`{"id":"test","kind":"test"}`, nil))
	if w.Code != 200 || len(p.list()) != 0 {
		t.Fatalf("code=%d pinged=%v", w.Code, p.list())
	}
}

func TestConnectorAsk_GuardsStillApplyWithoutTab(t *testing.T) {
	p := newPingSet(10)
	w := httptest.NewRecorder()
	connectorAskHandler(p, newUIPresence(10*time.Second))(w, askReq(`{"id":"x","kind":"ask"}`,
		func(r *http.Request) { r.Header.Set("Origin", "http://evil") }))
	if w.Code != http.StatusForbidden {
		t.Fatalf("code %d, want 403", w.Code)
	}
}

func TestUIPresence_TrackMarksLiveOnPoll(t *testing.T) {
	u := newUIPresence(10 * time.Second)
	if u.live() {
		t.Fatal("live before any poll")
	}
	called := false
	h := u.track(func(w http.ResponseWriter, r *http.Request) { called = true })
	h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/inbox", nil))
	if !called || !u.live() {
		t.Fatalf("called=%v live=%v", called, u.live())
	}
}
