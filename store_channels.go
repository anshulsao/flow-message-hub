package main

import "database/sql"

func (s *store) projects() ([]project, error) {
	rows, err := s.db.Query(`SELECT slug, name FROM projects
		WHERE archived_at IS NULL AND deleted_at IS NULL ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []project
	for rows.Next() {
		var p project
		if err := rows.Scan(&p.Slug, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *store) chanTasks() ([]chanTask, error) {
	// parent_slug arrived in flow v0.1.0-alpha.29; older DBs don't have it.
	parentCol := "''"
	var has int
	if s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('tasks') WHERE name='parent_slug'`).Scan(&has) == nil && has > 0 {
		parentCol = "COALESCE(parent_slug,'')"
	}
	rows, err := s.db.Query(`
		SELECT slug, name, COALESCE(project_slug,''), status, COALESCE(priority,''),
		       COALESCE(waiting_on,''), COALESCE(auto_run_status,''),
		       COALESCE(auto_run_started,''), COALESCE(auto_run_finished,''),
		       COALESCE(created_at,''), COALESCE(updated_at,''), COALESCE(session_id,''),
		       ` + parentCol + `
		FROM tasks
		WHERE archived_at IS NULL AND deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chanTask
	for rows.Next() {
		var t chanTask
		if err := rows.Scan(&t.Slug, &t.Name, &t.Project, &t.Status, &t.Priority,
			&t.WaitingOn, &t.AutoRunStatus, &t.AutoRunStarted, &t.AutoRunFinished,
			&t.CreatedAt, &t.UpdatedAt, &t.SessionID, &t.Parent); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// chanRunStates classifies liveness with the same recycle-proof probe the
// graph uses (session id present in the process table).
func chanRunStates(tasks []chanTask) map[string]string {
	probe := newLivenessProbe()
	out := make(map[string]string, len(tasks))
	for _, t := range tasks {
		m := taskMeta{Status: t.Status, SessionID: t.SessionID, AutoRunStatus: t.AutoRunStatus}
		out[t.Slug] = runState(m, t.Status == "in-progress" && probe.sessionLive(t.SessionID))
	}
	return out
}

// projectWorkDir returns the project's work_dir ("" if unknown, or on an
// older schema without the column).
func (s *store) projectWorkDir(slug string) string {
	if slug == "" {
		return ""
	}
	var n int
	if s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('projects') WHERE name='work_dir'`).Scan(&n) != nil || n == 0 {
		return ""
	}
	var wd sql.NullString
	if s.db.QueryRow(`SELECT work_dir FROM projects WHERE slug = ?`, slug).Scan(&wd) != nil {
		return ""
	}
	return wd.String
}
