package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBulkStatus(t *testing.T) {
	tasks := []chanTask{
		{Slug: "idle", Status: "in-progress", SessionID: "s1"},
		{Slug: "live", Status: "in-progress", SessionID: "s2"},
		{Slug: "fresh", Status: "backlog"},
		{Slug: "closed", Status: "done", SessionID: "s3"},
	}
	runs := map[string]string{"idle": "idle", "live": "live", "fresh": "backlog", "closed": "done"}
	fr := &fakeRunner{}
	res := bulkStatus(fr, tasks, runs, []string{"idle", "live", "fresh", "closed", "ghost"}, "done", false)

	got := map[string]bulkResult{}
	for _, r := range res {
		got[r.Slug] = r
	}
	if !got["idle"].OK {
		t.Errorf("idle should be done: %+v", got["idle"])
	}
	if got["live"].OK || !got["live"].NeedsConfirm {
		t.Errorf("live needs confirm: %+v", got["live"])
	}
	if got["fresh"].OK || !strings.Contains(got["fresh"].Message, "archive") {
		t.Errorf("never-started can't be done: %+v", got["fresh"])
	}
	if got["closed"].OK || !strings.Contains(got["closed"].Message, "already") {
		t.Errorf("already done: %+v", got["closed"])
	}
	if got["ghost"].OK || got["ghost"].Message != "no such task" {
		t.Errorf("unknown slug: %+v", got["ghost"])
	}
	if len(fr.calls) != 1 || strings.Join(fr.calls[0], " ") != "done idle" {
		t.Errorf("only idle should run flow: %v", fr.calls)
	}
	if len(res) != 5 || res[0].Slug != "idle" || res[4].Slug != "ghost" {
		t.Errorf("results keep request order: %+v", res)
	}

	fr = &fakeRunner{}
	res = bulkStatus(fr, tasks, runs, []string{"live"}, "done", true)
	if !res[0].OK {
		t.Errorf("confirmed live should run: %+v", res[0])
	}
}

func TestBulkStatusRoute(t *testing.T) {
	st, err := openStore(seedDB(t)) // task "a": in-progress with a session
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{}
	mux := http.NewServeMux()
	registerBulkRoute(mux, st, fr)
	srv := httptest.NewServer(guardWrites(mux))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/status-bulk", "application/json",
		strings.NewReader(`{"slugs":["a","nope"],"status":"backlog"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Results []bulkResult `json:"results"`
		OK      int          `json:"ok"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != 200 || out.OK != 1 || len(out.Results) != 2 {
		t.Fatalf("code=%d out=%+v", resp.StatusCode, out)
	}
	for _, bad := range []string{`{"slugs":[],"status":"done"}`, `{"slugs":["a"],"status":"archived"}`, `nope`} {
		resp, _ = http.Post(srv.URL+"/api/status-bulk", "application/json", strings.NewReader(bad))
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("%s: want 400, got %d", bad, resp.StatusCode)
		}
	}
	tooMany := `{"status":"done","slugs":[` + strings.TrimSuffix(strings.Repeat(`"x",`, bulkMax+1), ",") + `]}`
	resp, _ = http.Post(srv.URL+"/api/status-bulk", "application/json", strings.NewReader(tooMany))
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("over the cap: want 400, got %d", resp.StatusCode)
	}
}

func TestBulkArchive(t *testing.T) {
	tasks := []chanTask{
		{Slug: "fresh", Status: "backlog"},
		{Slug: "closed", Status: "done", SessionID: "s"},
		{Slug: "live", Status: "in-progress", SessionID: "s2"},
	}
	runs := map[string]string{"fresh": "backlog", "closed": "done", "live": "live"}
	fr := &fakeRunner{}
	res := bulkStatus(fr, tasks, runs, []string{"fresh", "closed", "live"}, "archive", false)
	if !res[0].OK || !res[1].OK {
		t.Errorf("backlog and done tasks archive: %+v", res)
	}
	if res[2].OK || !res[2].NeedsConfirm {
		t.Errorf("a live session needs confirm before archiving: %+v", res[2])
	}
	want := "archive fresh,archive closed"
	got := []string{}
	for _, c := range fr.calls {
		got = append(got, strings.Join(c, " "))
	}
	if strings.Join(got, ",") != want {
		t.Errorf("calls: %v", got)
	}
}
