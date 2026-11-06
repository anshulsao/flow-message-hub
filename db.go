package main

import (
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"
)

type store struct {
	db     *sql.DB
	drafts *draftStore // hub-launched sessions not yet bound to a task (nil = none)
}

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

// busColumns returns the set of column names in bus_messages, so we can
// read newer columns while tolerating older flow schemas.
func (s *store) busColumns() (map[string]bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(bus_messages)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

func (s *store) messages() ([]rawMessage, error) {
	cols, err := s.busColumns()
	if err != nil {
		return nil, err
	}
	text := func(c string) string {
		if cols[c] {
			return "COALESCE(" + c + ",'')"
		}
		return "''"
	}
	fyi := "0"
	if cols["fyi"] {
		fyi = "COALESCE(fyi,0)"
	}
	hasMail := cols["seen_at"]
	rows, err := s.db.Query(`
		SELECT id, created_at, kind, from_assignee,
		       COALESCE(from_task_slug,''), to_assignee,
		       COALESCE(to_task_slug,''), body, urgent, status, waited_s,
		       ` + text("seen_at") + `, ` + text("answered_at") + `, ` + text("options") + `,
		       ` + text("reply_to") + `, ` + fyi + `
		FROM bus_messages
		ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rawMessage
	for rows.Next() {
		var m rawMessage
		var urgent, fyiN int
		var waited sql.NullFloat64
		var opts string
		if err := rows.Scan(&m.ID, &m.CreatedAt, &m.Kind, &m.FromAssignee,
			&m.FromTaskSlug, &m.ToAssignee, &m.ToTaskSlug, &m.Body,
			&urgent, &m.Status, &waited,
			&m.SeenAt, &m.AnsweredAt, &opts, &m.ReplyTo, &fyiN); err != nil {
			return nil, err
		}
		m.Urgent = urgent != 0
		m.FYI = fyiN != 0
		m.HasMail = hasMail
		if opts != "" {
			_ = json.Unmarshal([]byte(opts), &m.Options) // malformed → no options
		}
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
		       COALESCE(auto_run_pid,0), COALESCE(project_slug,'')
		FROM tasks
		WHERE archived_at IS NULL AND deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]taskMeta{}
	for rows.Next() {
		var slug, name, status, changedAt, sessionID, autoStatus, project string
		var autoPID int
		if err := rows.Scan(&slug, &name, &status, &changedAt,
			&sessionID, &autoStatus, &autoPID, &project); err != nil {
			return nil, err
		}
		out[slug] = taskMeta{
			Name: name, Status: status, StatusChangedAt: changedAt,
			SessionID: sessionID, AutoRunStatus: autoStatus, AutoRunPID: autoPID,
			Project: project,
		}
	}
	return out, rows.Err()
}
