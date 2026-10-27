package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenSession(t *testing.T) {
	cases := []struct {
		name string
		task chanTask
		run  string
		code int
		args string
	}{
		{"closed interactive", chanTask{Slug: "a", Status: "in-progress", SessionID: "s"}, "idle", 200, "do a"},
		{"crashed auto", chanTask{Slug: "a", Status: "in-progress", SessionID: "s", AutoRunStatus: "dead"}, "crashed", 200, "do a"},
		{"already running", chanTask{Slug: "a", Status: "in-progress", SessionID: "s"}, "live", 409, ""},
		{"no session", chanTask{Slug: "a", Status: "backlog"}, "backlog", 409, ""},
		{"done", chanTask{Slug: "a", Status: "done", SessionID: "s"}, "done", 409, ""},
	}
	for _, c := range cases {
		fr := &fakeRunner{}
		code, _ := openSession(fr, c.task, c.run)
		got := ""
		if len(fr.calls) == 1 {
			got = strings.Join(fr.calls[0], " ")
		}
		if code != c.code || got != c.args {
			t.Errorf("%s: code=%d ran %q", c.name, code, got)
		}
	}
}

func TestOpenRoute(t *testing.T) {
	st, err := openStore(seedDB(t)) // task "a": in-progress, session sid-a (not running here)
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{}
	mux := http.NewServeMux()
	registerOpenRoute(mux, st, fr)
	srv := httptest.NewServer(guardWrites(mux))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/open/a", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || len(fr.calls) != 1 || strings.Join(fr.calls[0], " ") != "do a" {
		t.Fatalf("code=%d calls=%v", resp.StatusCode, fr.calls)
	}
	resp, _ = http.Get(srv.URL + "/api/open/a")
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Errorf("GET should be 405, got %d", resp.StatusCode)
	}
	resp, _ = http.Post(srv.URL+"/api/open/nope", "application/json", nil)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("unknown task: %d", resp.StatusCode)
	}
}
