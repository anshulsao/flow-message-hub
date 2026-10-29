package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type fakeLauncher struct {
	cwd, cmd string
	calls    int
	err      error
}

func (f *fakeLauncher) Launch(cwd, cmd string) error {
	f.cwd, f.cmd = cwd, cmd
	f.calls++
	return f.err
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":       "'plain'",
		"it's":        `'it'\''s'`,
		"a b $HOME":   "'a b $HOME'",
		"/Users/x/my": "'/Users/x/my'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestDraftCommandAndPrompt(t *testing.T) {
	cmd := draftCommand("/Users/me/repo's", "11111111-2222-4333-8444-555555555555", draftPrompt("fampay"))
	if !strings.HasPrefix(cmd, `cd '/Users/me/repo'\''s' && claude --session-id 11111111-2222-4333-8444-555555555555 '`) {
		t.Errorf("command: %s", cmd)
	}
	if strings.Contains(cmd, "--dangerously") {
		t.Errorf("interactive sessions keep normal permissions: %s", cmd)
	}
	p := draftPrompt("fampay")
	for _, want := range []string{"flow skill", "--project fampay", "flow do --here"} {
		if !strings.Contains(p, want) {
			t.Errorf("project prompt missing %q: %s", want, p)
		}
	}
	if f := draftPrompt(""); strings.Contains(f, "--project") || !strings.Contains(f, "which project") {
		t.Errorf("floating prompt: %s", f)
	}
	if strings.Contains(p, "—") {
		t.Errorf("no em dashes in prompts")
	}
}

func TestNewUUIDv4(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	a, b := newUUID(), newUUID()
	if !re.MatchString(a) || a == b {
		t.Errorf("uuids: %s %s", a, b)
	}
}

func TestDraftStorePersists(t *testing.T) {
	p := filepath.Join(t.TempDir(), "drafts.json")
	ds := newDraftStore(p)
	ds.add(draft{SID: "s1", Project: "fampay", Created: time.Now()})
	if got := newDraftStore(p).list(); len(got) != 1 || got[0].SID != "s1" || got[0].Project != "fampay" {
		t.Fatalf("reload: %+v", got)
	}
	ds.remove("s1")
	if got := newDraftStore(p).list(); len(got) != 0 {
		t.Fatalf("after remove: %+v", got)
	}
	var nilStore *draftStore
	if nilStore.list() != nil {
		t.Error("nil store lists nothing")
	}
}

func TestDraftState(t *testing.T) {
	now := time.Now()
	tasks := []chanTask{{Slug: "bound-task", SessionID: "s-bound"}}
	fresh := draft{SID: "s-new", Created: now.Add(-30 * time.Second)}
	old := draft{SID: "s-old", Created: now.Add(-10 * time.Minute)}
	bound := draft{SID: "s-bound", Created: now.Add(-time.Minute)}
	if st, _ := draftState(fresh, tasks, false, now); st != "open" {
		t.Errorf("fresh not-yet-live draft stays open (claude still starting): %s", st)
	}
	if st, _ := draftState(old, tasks, true, now); st != "open" {
		t.Errorf("live draft stays open: %s", st)
	}
	if st, _ := draftState(old, tasks, false, now); st != "abandoned" {
		t.Errorf("old dead draft is abandoned: %s", st)
	}
	if st, slug := draftState(bound, tasks, false, now); st != "bound" || slug != "bound-task" {
		t.Errorf("bound: %s %s", st, slug)
	}
}

// newDraftServer wires the channel + prompt + new routes over the seed DB
// (project fampay; task "a" has session sid-a).
func newDraftServer(t *testing.T, extra ...string) (*httptest.Server, *store, *fakeLauncher, string) {
	t.Helper()
	st, err := openStore(seedDBWith(t, extra...))
	if err != nil {
		t.Fatal(err)
	}
	st.drafts = newDraftStore(filepath.Join(t.TempDir(), "drafts.json"))
	projects := t.TempDir()
	l := &fakeLauncher{}
	mux := http.NewServeMux()
	registerChannelRoutes(mux, st, t.TempDir(), projects)
	registerPromptRoute(mux, st, promptDeps(nil, "", &fakeSender{}))
	registerNewRoute(mux, st, l)
	srv := httptest.NewServer(guardWrites(mux))
	t.Cleanup(srv.Close)
	return srv, st, l, projects
}

func postJSON(t *testing.T, url, body, origin string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = decodeJSON(resp.Body, &out)
	return resp.StatusCode, out
}

func TestNewRoute_LaunchesAndShowsDraft(t *testing.T) {
	srv, st, l, projects := newDraftServer(t)

	code, out := postJSON(t, srv.URL+"/api/new", `{"channel":"fampay"}`, srv.URL)
	if code != 200 {
		t.Fatalf("POST /api/new: %d %v", code, out)
	}
	sid, _ := out["sid"].(string)
	slug, _ := out["slug"].(string)
	if l.calls != 1 || !strings.Contains(l.cmd, "--session-id "+sid) || !strings.Contains(l.cmd, "--project fampay") {
		t.Fatalf("launcher: %+v", l)
	}
	home, _ := os.UserHomeDir()
	if l.cwd != home {
		t.Errorf("no project work_dir in seed: cwd should fall back to home, got %q", l.cwd)
	}
	if slug != draftPrefix+sid || len(st.drafts.list()) != 1 {
		t.Fatalf("draft not recorded: %v %+v", out, st.drafts.list())
	}

	// the channel lists it as a live-session card
	var ch struct {
		Cards []Card `json:"cards"`
	}
	getJSON(t, srv.URL+"/api/channel/fampay", &ch)
	var card *Card
	for i := range ch.Cards {
		if ch.Cards[i].Slug == slug {
			card = &ch.Cards[i]
		}
	}
	if card == nil || !card.Draft || card.Section != "active" || card.Status != "in-progress" {
		t.Fatalf("draft card: %+v in %+v", card, ch.Cards)
	}

	// thread + session work under the draft slug; the transcript is read by sid
	os.MkdirAll(filepath.Join(projects, "-Users-me"), 0o755)
	os.WriteFile(filepath.Join(projects, "-Users-me", sid+".jsonl"),
		[]byte(asstLine(t, "2026-10-29T07:00:00Z", text("What are we working on?"))), 0o644)
	var th Thread
	if c := getJSON(t, srv.URL+"/api/thread/"+slug, &th); c != 200 || !th.Draft || th.Status != "in-progress" {
		t.Fatalf("draft thread: %d %+v", c, th)
	}
	var sess struct {
		Items []sessItem `json:"items"`
		Found bool       `json:"found"`
	}
	getJSON(t, srv.URL+"/api/session/"+slug, &sess)
	if !sess.Found || len(sess.Items) != 1 || sess.Items[0].Body != "What are we working on?" {
		t.Fatalf("draft session: %+v", sess)
	}

	// the composer can target it (session not open in this test, so not ok, but resolvable)
	var pt promptTarget
	if c := getJSON(t, srv.URL+"/api/prompt/"+slug, &pt); c != 200 || !strings.Contains(pt.Reason, "isn't open") {
		t.Fatalf("draft prompt target: %d %+v", c, pt)
	}

	// other channels don't show it
	var other struct {
		Cards []Card `json:"cards"`
	}
	getJSON(t, srv.URL+"/api/channel/_floating", &other)
	for _, c := range other.Cards {
		if c.Draft {
			t.Errorf("fampay draft leaked into _floating: %+v", c)
		}
	}
}

func TestNewRoute_BoundDraftPointsToTask(t *testing.T) {
	srv, st, _, _ := newDraftServer(t)
	st.drafts.add(draft{SID: "sid-a", Project: "fampay", Created: time.Now()}) // task "a" already carries sid-a
	var th Thread
	if c := getJSON(t, srv.URL+"/api/thread/"+draftPrefix+"sid-a", &th); c != 200 || th.Bound != "a" {
		t.Fatalf("bound draft should name its task: %d %+v", c, th)
	}
	var ch struct {
		Cards []Card `json:"cards"`
	}
	getJSON(t, srv.URL+"/api/channel/fampay", &ch)
	for _, c := range ch.Cards {
		if c.Draft {
			t.Errorf("bound draft must not show as a card: %+v", c)
		}
	}
}

func TestNewRoute_Guards(t *testing.T) {
	srv, _, l, _ := newDraftServer(t)
	if c, _ := postJSON(t, srv.URL+"/api/new", `{"channel":"fampay"}`, "https://evil.example"); c != http.StatusForbidden {
		t.Errorf("cross-site: %d", c)
	}
	if c, _ := postJSON(t, srv.URL+"/api/new", `{"channel":"nope"}`, ""); c != http.StatusNotFound {
		t.Errorf("unknown channel: %d", c)
	}
	if c, _ := postJSON(t, srv.URL+"/api/new", `{"channel":"_floating"}`, ""); c != 200 {
		t.Errorf("floating channel should launch: %d", c)
	}
	if l.calls != 1 {
		t.Errorf("only the floating launch should have run: %d", l.calls)
	}
	l.err = errTabNotFound
	if c, _ := postJSON(t, srv.URL+"/api/new", `{"channel":"fampay"}`, ""); c != http.StatusInternalServerError {
		t.Errorf("launch failure: %d", c)
	}
	if c := getJSON(t, srv.URL+"/api/thread/"+draftPrefix+"unknown", &Thread{}); c != 404 {
		t.Errorf("unknown draft: %d", c)
	}
}

func TestProjectWorkDir(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(seedDBWith(t,
		`ALTER TABLE projects ADD COLUMN work_dir TEXT`,
		`UPDATE projects SET work_dir='`+dir+`' WHERE slug='fampay'`))
	if err != nil {
		t.Fatal(err)
	}
	if got := st.projectWorkDir("fampay"); got != dir {
		t.Errorf("work_dir: %q", got)
	}
	plain, _ := openStore(seedDB(t))
	if got := plain.projectWorkDir("fampay"); got != "" {
		t.Errorf("no column: %q", got)
	}
}
