package main

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
	rows, err := s.db.Query(`
		SELECT slug, name, COALESCE(project_slug,''), status, COALESCE(priority,''),
		       COALESCE(waiting_on,''), COALESCE(auto_run_status,''),
		       COALESCE(auto_run_started,''), COALESCE(auto_run_finished,''),
		       COALESCE(created_at,''), COALESCE(updated_at,''), COALESCE(session_id,'')
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
			&t.CreatedAt, &t.UpdatedAt, &t.SessionID); err != nil {
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
