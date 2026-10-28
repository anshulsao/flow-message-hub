package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func seedDB(t *testing.T) string { t.Helper(); return seedDBWith(t) }

// seedDBWith runs extra statements after the base seed (e.g. newer columns).
func seedDBWith(t *testing.T, extra ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range append([]string{
		`CREATE TABLE projects (slug TEXT, name TEXT, status TEXT, archived_at TEXT, deleted_at TEXT)`,
		`CREATE TABLE tasks (slug TEXT, name TEXT, project_slug TEXT, status TEXT, priority TEXT,
			waiting_on TEXT, auto_run_status TEXT, auto_run_started TEXT, auto_run_finished TEXT,
			created_at TEXT, updated_at TEXT, session_id TEXT, archived_at TEXT, deleted_at TEXT)`,
		`CREATE TABLE bus_messages (id TEXT, created_at TEXT, kind TEXT, from_assignee TEXT, from_task_slug TEXT,
			to_assignee TEXT, to_task_slug TEXT, body TEXT, urgent INTEGER, status TEXT, waited_s REAL)`,
		`INSERT INTO projects VALUES ('fampay','Fampay','active',NULL,NULL),('old','Old','active','2026-01-01',NULL)`,
		`INSERT INTO tasks VALUES ('a','A','fampay','in-progress','high','Anuj','running','2026-10-27T13:00:00+05:30',NULL,
			'2026-10-27T12:00:00+05:30','2026-10-27T13:00:00+05:30','sid-a',NULL,NULL)`,
		`INSERT INTO tasks VALUES ('gone','G',NULL,'done','low',NULL,NULL,NULL,NULL,'x','x',NULL,'2026-01-01',NULL)`,
	}, extra...) {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return path
}

func TestStore_ChanTasksAndProjects(t *testing.T) {
	st, err := openStore(seedDB(t))
	if err != nil {
		t.Fatal(err)
	}
	ps, err := st.projects()
	if err != nil || len(ps) != 1 || ps[0].Slug != "fampay" || ps[0].Name != "Fampay" {
		t.Fatalf("projects: %+v %v", ps, err)
	}
	ts, err := st.chanTasks()
	if err != nil || len(ts) != 1 {
		t.Fatalf("archived task should be skipped: %+v %v", ts, err)
	}
	a := ts[0]
	if a.Project != "fampay" || a.WaitingOn != "Anuj" || a.AutoRunStatus != "running" || a.AutoRunFinished != "" || a.SessionID != "sid-a" {
		t.Errorf("row mapping: %+v", a)
	}
}
