package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newer flow DBs carry tasks.parent_slug; "kid" was spun off from "a".
func seedFamilyDB(t *testing.T) string {
	return seedDBWith(t,
		`ALTER TABLE tasks ADD COLUMN parent_slug TEXT`,
		`INSERT INTO tasks (slug,name,project_slug,status,priority,auto_run_status,created_at,updated_at,session_id,parent_slug)
		 VALUES ('kid','Kid task','fampay','in-progress','medium','running','2026-10-27T14:00:00+05:30','2026-10-27T14:05:00+05:30','sid-kid','a')`,
	)
}

func TestStore_ParentSlug(t *testing.T) {
	st, err := openStore(seedFamilyDB(t))
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := st.chanTasks()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, tk := range tasks {
		got[tk.Slug] = tk.Parent
	}
	if got["kid"] != "a" || got["a"] != "" {
		t.Fatalf("parents: %v", got)
	}

	// older flow DB without the column still loads
	old, err := openStore(seedDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if ts, err := old.chanTasks(); err != nil || len(ts) == 0 {
		t.Fatalf("old schema: %v %d", err, len(ts))
	}
}

func TestBuildFamily(t *testing.T) {
	all := []chanTask{
		{Slug: "p", Name: "Parent", Status: "in-progress"},
		{Slug: "c2", Name: "Second", Status: "done", Parent: "p", CreatedAt: "2026-10-27T12:00:00Z"},
		{Slug: "c1", Name: "First", Status: "in-progress", Parent: "p", AutoRunStatus: "running", CreatedAt: "2026-10-27T10:00:00Z"},
		{Slug: "x", Status: "backlog"},
	}
	runs := map[string]string{"c1": "live"}
	now := time.Date(2026, 10, 27, 13, 0, 0, 0, time.UTC)

	parent, kids := buildFamily(all[0], all, runs, now)
	if parent != nil {
		t.Errorf("top-level task has no parent: %+v", parent)
	}
	if len(kids) != 2 || kids[0].Slug != "c1" || kids[1].Slug != "c2" {
		t.Fatalf("children oldest first: %+v", kids)
	}
	if kids[0].RunState != "live" || !kids[0].Auto || kids[0].Created != "2026-10-27T10:00:00Z" || kids[0].Parent != "p" {
		t.Errorf("child card: %+v", kids[0])
	}

	parent, kids = buildFamily(all[2], all, runs, now)
	if parent == nil || parent.Slug != "p" || parent.Name != "Parent" || len(kids) != 0 {
		t.Errorf("child view: parent=%+v kids=%v", parent, kids)
	}
	// parent archived / missing: no link object, but the card keeps the slug
	parent, _ = buildFamily(chanTask{Slug: "o", Parent: "gone"}, all, runs, now)
	if parent != nil {
		t.Errorf("missing parent should be nil: %+v", parent)
	}
}

func TestThreadRoute_Family(t *testing.T) {
	st, err := openStore(seedFamilyDB(t))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerChannelRoutes(mux, st, t.TempDir(), t.TempDir())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var parent struct {
		Children []Card `json:"children"`
	}
	if code := getJSON(t, srv.URL+"/api/thread/a", &parent); code != 200 || len(parent.Children) != 1 || parent.Children[0].Slug != "kid" {
		t.Fatalf("parent thread: %d %+v", code, parent)
	}
	var kid struct {
		Parent     string `json:"parent"`
		ParentTask *Card  `json:"parent_task"`
	}
	if code := getJSON(t, srv.URL+"/api/thread/kid", &kid); code != 200 || kid.Parent != "a" || kid.ParentTask == nil || kid.ParentTask.Name != "A" {
		t.Fatalf("kid thread: %d %+v", code, kid)
	}
	var ch struct {
		Cards []Card `json:"cards"`
	}
	getJSON(t, srv.URL+"/api/channel/fampay", &ch)
	found := false
	for _, c := range ch.Cards {
		if c.Slug == "kid" && c.Parent == "a" {
			found = true
		}
	}
	if !found {
		t.Errorf("kid card should carry parent: %+v", ch.Cards)
	}
}
