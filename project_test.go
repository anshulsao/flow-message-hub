package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetProject(t *testing.T) {
	known := []project{{Slug: "fampay", Name: "Fampay"}, {Slug: "flow", Name: "flow"}}
	cases := []struct {
		name, cur, target string
		code              int
		args              string
	}{
		{"assign", "", "fampay", 200, "update task a --project fampay"},
		{"move", "flow", "fampay", 200, "update task a --project fampay"},
		{"clear", "fampay", "", 200, "update task a --clear-project"},
		{"same", "fampay", "fampay", 409, ""},
		{"already floating", "", "", 409, ""},
		{"unknown project", "", "nope", 400, ""},
	}
	for _, c := range cases {
		fr := &fakeRunner{}
		code, _ := setProject(fr, chanTask{Slug: "a", Project: c.cur}, c.target, known)
		got := ""
		if len(fr.calls) == 1 {
			got = strings.Join(fr.calls[0], " ")
		}
		if code != c.code || got != c.args {
			t.Errorf("%s: code=%d ran %q", c.name, code, got)
		}
	}
}

func TestProjectRoutes(t *testing.T) {
	st, err := openStore(seedDB(t)) // projects: fampay (active), old (archived); task "a" in fampay
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{}
	mux := http.NewServeMux()
	registerProjectRoutes(mux, st, fr)
	srv := httptest.NewServer(guardWrites(mux))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/projects")
	if err != nil {
		t.Fatal(err)
	}
	var ps []project
	json.NewDecoder(resp.Body).Decode(&ps)
	resp.Body.Close()
	if len(ps) != 1 || ps[0].Slug != "fampay" || ps[0].Name != "Fampay" {
		t.Fatalf("projects: %+v", ps)
	}

	resp, _ = http.Post(srv.URL+"/api/task-project/a", "application/json", strings.NewReader(`{"project":""}`))
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.Join(fr.calls[0], " ") != "update task a --clear-project" {
		t.Fatalf("clear: %d %v", resp.StatusCode, fr.calls)
	}
	resp, _ = http.Post(srv.URL+"/api/task-project/nope", "application/json", strings.NewReader(`{"project":""}`))
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("unknown task: %d", resp.StatusCode)
	}
}
