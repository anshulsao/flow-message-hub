package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResumeAuto(t *testing.T) {
	stopped := chanTask{Slug: "a", Status: "in-progress", SessionID: "s", AutoRunStatus: "dead"}
	cases := []struct {
		name string
		task chanTask
		run  string
		text string
		code int
		args []string
	}{
		{"with answer", stopped, "crashed", "use suffix names\nOK to touch fam-uat", 200,
			[]string{"do", "--auto", "a", "--with", "use suffix names\nOK to touch fam-uat"}},
		{"no answer", stopped, "crashed", "  ", 200, []string{"do", "--auto", "a"}},
		{"control chars stripped", stopped, "crashed", "go\x1b on", 200, []string{"do", "--auto", "a", "--with", "go on"}},
		{"still running", stopped, "live", "x", 409, nil},
		{"no session", chanTask{Slug: "a", Status: "backlog"}, "backlog", "x", 409, nil},
		{"done", chanTask{Slug: "a", Status: "done", SessionID: "s"}, "done", "x", 409, nil},
	}
	for _, c := range cases {
		fr := &fakeRunner{}
		code, _ := resumeAuto(fr, c.task, c.run, c.text)
		var got []string
		if len(fr.calls) == 1 {
			got = fr.calls[0]
		}
		if code != c.code || strings.Join(got, "|") != strings.Join(c.args, "|") {
			t.Errorf("%s: code=%d args=%q", c.name, code, got)
		}
	}
}

func TestResumeAutoRoute(t *testing.T) {
	st, err := openStore(seedDB(t)) // task "a": in-progress, session sid-a, not running here
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{}
	mux := http.NewServeMux()
	registerResumeRoute(mux, st, fr)
	srv := httptest.NewServer(guardWrites(mux))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/resume-auto/a", "application/json", strings.NewReader(`{"text":"go ahead"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.Join(fr.calls[0], " ") != "do --auto a --with go ahead" {
		t.Fatalf("code=%d calls=%v", resp.StatusCode, fr.calls)
	}
	resp, _ = http.Get(srv.URL + "/api/resume-auto/a")
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Errorf("GET: %d", resp.StatusCode)
	}
}
