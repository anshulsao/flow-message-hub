package main

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type store struct{ db *sql.DB }

func openStore(path string) (*store, error) {
	// Read-only, with a busy timeout so a concurrent flow write never
	// makes us fail. immutable=0 so we still see live commits.
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)&_txlock=deferred", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return &store{db: db}, nil
}

func (s *store) messages() ([]rawMessage, error) {
	rows, err := s.db.Query(`
		SELECT id, created_at, kind, from_assignee,
		       COALESCE(from_task_slug,''), to_assignee,
		       COALESCE(to_task_slug,''), body, urgent, status, waited_s
		FROM bus_messages
		ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rawMessage
	for rows.Next() {
		var m rawMessage
		var urgent int
		var waited sql.NullFloat64
		if err := rows.Scan(&m.ID, &m.CreatedAt, &m.Kind, &m.FromAssignee,
			&m.FromTaskSlug, &m.ToAssignee, &m.ToTaskSlug, &m.Body,
			&urgent, &m.Status, &waited); err != nil {
			return nil, err
		}
		m.Urgent = urgent != 0
		if waited.Valid {
			m.WaitedS = waited.Float64
			m.HasWaited = true
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *store) watches() ([]rawWatch, error) {
	rows, err := s.db.Query(`SELECT watcher, watched FROM bus_watches ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rawWatch
	for rows.Next() {
		var w rawWatch
		if err := rows.Scan(&w.Watcher, &w.Watched); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *store) nudges() ([]rawNudge, error) {
	rows, err := s.db.Query(`SELECT task_slug, attempts FROM bus_nudges`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rawNudge
	for rows.Next() {
		var n rawNudge
		if err := rows.Scan(&n.TaskSlug, &n.Attempts); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// taskMeta for any slug referenced on the bus (name + status enrichment).
func (s *store) tasks() (map[string]taskMeta, error) {
	rows, err := s.db.Query(`
		SELECT slug, name, status, COALESCE(status_changed_at,''),
		       COALESCE(session_id,''), COALESCE(auto_run_status,''),
		       COALESCE(auto_run_pid,0)
		FROM tasks
		WHERE archived_at IS NULL AND deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]taskMeta{}
	for rows.Next() {
		var slug, name, status, changedAt, sessionID, autoStatus string
		var autoPID int
		if err := rows.Scan(&slug, &name, &status, &changedAt,
			&sessionID, &autoStatus, &autoPID); err != nil {
			return nil, err
		}
		out[slug] = taskMeta{
			Name: name, Status: status, StatusChangedAt: changedAt,
			SessionID: sessionID, AutoRunStatus: autoStatus, AutoRunPID: autoPID,
		}
	}
	return out, rows.Err()
}
