package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls [][]string
	err   error
}

func (f *fakeRunner) run(args ...string) (string, error) {
	f.calls = append(f.calls, args)
	return "ok", f.err
}

func TestChangeStatus_MapsToFlowCommands(t *testing.T) {
	cases := []struct {
		name   string
		task   chanTask
		target string
		run    string
		args   string // "" = no call expected
		code   int
	}{
		{"done uses flow done", chanTask{Slug: "a", Status: "in-progress", SessionID: "s"}, "done", "idle", "done a", 200},
		{"reopen with session", chanTask{Slug: "a", Status: "done", SessionID: "s"}, "in-progress", "done", "update task a --status in-progress", 200},
		{"back to backlog", chanTask{Slug: "a", Status: "in-progress", SessionID: "s"}, "backlog", "idle", "update task a --status backlog", 200},
		{"in-progress needs a session", chanTask{Slug: "a", Status: "backlog"}, "in-progress", "backlog", "", 409},
		{"done needs confirm when live", chanTask{Slug: "a", Status: "in-progress", SessionID: "s"}, "done", "live", "", 409},
		{"done without a session explains archive", chanTask{Slug: "a", Status: "backlog"}, "done", "backlog", "", 409},
		{"same status is a no-op conflict", chanTask{Slug: "a", Status: "done"}, "done", "done", "", 409},
		{"archive a backlog task", chanTask{Slug: "a", Status: "backlog"}, "archive", "backlog", "archive a", 200},
		{"archive a live session needs confirm", chanTask{Slug: "a", Status: "in-progress", SessionID: "s"}, "archive", "live", "", 409},
		{"unknown status rejected", chanTask{Slug: "a", Status: "done"}, "archived", "done", "", 400},
	}
	for _, c := range cases {
		fr := &fakeRunner{}
		code, _ := changeStatus(fr, c.task, c.run, c.target, false)
		if code != c.code {
			t.Errorf("%s: code=%d want %d", c.name, code, c.code)
		}
		got := ""
		if len(fr.calls) == 1 {
			got = strings.Join(fr.calls[0], " ")
		}
		if got != c.args {
			t.Errorf("%s: ran %q want %q", c.name, got, c.args)
		}
	}
}

func TestChangeStatus_ConfirmedDoneOnLiveSession(t *testing.T) {
	fr := &fakeRunner{}
	code, _ := changeStatus(fr, chanTask{Slug: "a", Status: "in-progress", SessionID: "s"}, "live", "done", true)
	if code != 200 || len(fr.calls) != 1 {
		t.Fatalf("confirmed done should run: code=%d calls=%v", code, fr.calls)
	}
}

func TestChangeStatus_FlowErrorSurfaces(t *testing.T) {
	fr := &fakeRunner{err: errors.New("boom")}
	code, msg := changeStatus(fr, chanTask{Slug: "a", Status: "in-progress", SessionID: "s"}, "idle", "backlog", false)
	if code != 500 || !strings.Contains(msg, "boom") {
		t.Errorf("code=%d msg=%q", code, msg)
	}
}

func TestStatusRoute(t *testing.T) {
	st, err := openStore(seedDB(t))
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{}
	mux := http.NewServeMux()
	registerStatusRoute(mux, st, fr)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	post := func(path, body string) int {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post("/api/status/a", `{"status":"backlog"}`); c != 200 || strings.Join(fr.calls[0], " ") != "update task a --status backlog" {
		t.Fatalf("code=%d calls=%v", c, fr.calls)
	}
	if c := post("/api/status/nope", `{"status":"done"}`); c != 404 {
		t.Errorf("unknown task: %d", c)
	}
	if c := post("/api/status/a", `not json`); c != 400 {
		t.Errorf("bad body: %d", c)
	}
	resp, _ := http.Get(srv.URL + "/api/status/a")
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Errorf("GET should be 405, got %d", resp.StatusCode)
	}
}
