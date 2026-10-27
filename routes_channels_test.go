package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func channelServer(t *testing.T) *httptest.Server {
	t.Helper()
	dbPath := seedDB(t)
	st, err := openStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(dbPath)
	os.MkdirAll(filepath.Join(root, "tasks", "a", "updates"), 0o755)
	os.WriteFile(filepath.Join(root, "tasks", "a", "brief.md"), []byte("# Brief A"), 0o644)
	os.WriteFile(filepath.Join(root, "tasks", "a", "updates", "n.md"), []byte("# Note one"), 0o644)
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) // older than the session's last reply
	os.Chtimes(filepath.Join(root, "tasks", "a", "updates", "n.md"), old, old)
	projDir := filepath.Join(root, "claude-projects")
	os.MkdirAll(filepath.Join(projDir, "-repo"), 0o755)
	os.WriteFile(filepath.Join(projDir, "-repo", "sid-a.jsonl"), []byte(sampleSession(t)), 0o644)
	mux := http.NewServeMux()
	registerChannelRoutes(mux, st, root, projDir)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func getJSON(t *testing.T, url string, v any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if v != nil && resp.StatusCode == 200 {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func TestRoutes_ChannelListChannelAndThread(t *testing.T) {
	srv := channelServer(t)

	var list ChannelList
	if code := getJSON(t, srv.URL+"/api/channels", &list); code != 200 || len(list.Channels) != 1 || list.Channels[0].Slug != "fampay" {
		t.Fatalf("channels: %d %+v", code, list)
	}

	var ch struct {
		Channel ChannelSummary `json:"channel"`
		Cards   []Card         `json:"cards"`
	}
	if code := getJSON(t, srv.URL+"/api/channel/fampay", &ch); code != 200 || len(ch.Cards) != 1 || ch.Cards[0].Latest != "Shipped." {
		t.Fatalf("channel: %d %+v", code, ch)
	}
	if ch.Channel.Name != "Fampay" {
		t.Errorf("channel summary missing: %+v", ch.Channel)
	}

	var th Thread
	if code := getJSON(t, srv.URL+"/api/thread/a", &th); code != 200 || th.Brief != "# Brief A" || th.Project != "fampay" {
		t.Fatalf("thread: %d %+v", code, th)
	}
	if code := getJSON(t, srv.URL+"/api/thread/nope", nil); code != 404 {
		t.Errorf("unknown thread should 404, got %d", code)
	}
	if code := getJSON(t, srv.URL+"/api/channel/nope", nil); code != 404 {
		t.Errorf("unknown channel should 404, got %d", code)
	}
}

func TestRoutes_ChannelsAreReadOnly(t *testing.T) {
	srv := channelServer(t)
	for _, p := range []string{"/api/channels", "/api/channel/fampay", "/api/thread/a"} {
		resp, err := http.Post(srv.URL+p, "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: want 405, got %d", p, resp.StatusCode)
		}
	}
}

func TestRoutes_SessionPages(t *testing.T) {
	srv := channelServer(t)
	var pg struct {
		Items   []sessItem `json:"items"`
		Start   int64      `json:"start"`
		Found   bool       `json:"found"`
		HasMore bool       `json:"has_more"`
	}
	if code := getJSON(t, srv.URL+"/api/session/a", &pg); code != 200 || !pg.Found || len(pg.Items) != 5 || pg.Items[4].Body != "Shipped." {
		t.Fatalf("session: %d %+v", code, pg)
	}
	if pg.HasMore || pg.Start != 0 {
		t.Errorf("whole small file fits one page: %+v", pg)
	}
	var fwd struct {
		Items []sessItem `json:"items"`
	}
	if code := getJSON(t, srv.URL+"/api/session/a?from=0", &fwd); code != 200 || len(fwd.Items) != 5 {
		t.Errorf("from=0: %d %+v", code, fwd)
	}
	if code := getJSON(t, srv.URL+"/api/session/a?before=abc", nil); code != 400 {
		t.Errorf("bad cursor should 400, got %d", code)
	}
	if code := getJSON(t, srv.URL+"/api/session/nope", nil); code != 404 {
		t.Errorf("unknown task should 404, got %d", code)
	}
}
