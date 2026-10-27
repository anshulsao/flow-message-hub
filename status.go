package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

type flowRunner interface {
	run(args ...string) (string, error)
}

// changeStatus moves a task to target via the flow CLI, so flow's own
// invariants (e.g. in-progress needs a session) stay authoritative.
// Returns an HTTP status code and a message for the UI.
func changeStatus(r flowRunner, t chanTask, run, target string, confirm bool) (int, string) {
	switch target {
	case "backlog", "in-progress", "done":
	default:
		return http.StatusBadRequest, "status must be backlog, in-progress or done"
	}
	if t.Status == target {
		return http.StatusConflict, "already " + target
	}
	var args []string
	switch target {
	case "done":
		if t.SessionID == "" {
			return http.StatusConflict, "never started, so flow can't close it; archive it instead: flow archive " + t.Slug
		}
		if run == "live" && !confirm {
			return http.StatusConflict, "session is still running; confirm to mark done"
		}
		args = []string{"done", t.Slug}
	case "in-progress":
		if t.SessionID == "" {
			return http.StatusConflict, "no session yet; start it with: flow do " + t.Slug
		}
		args = []string{"update", "task", t.Slug, "--status", "in-progress"}
	case "backlog":
		args = []string{"update", "task", t.Slug, "--status", "backlog"}
	}
	if out, err := r.run(args...); err != nil {
		return http.StatusInternalServerError, err.Error()
	} else {
		return http.StatusOK, out
	}
}

// registerStatusRoute wires POST /api/status/{slug} {"status":..., "confirm":bool}.
func registerStatusRoute(mux *http.ServeMux, st *store, r flowRunner) {
	mux.HandleFunc("/api/status/", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		slug := strings.TrimPrefix(req.URL.Path, "/api/status/")
		var body struct {
			Status  string `json:"status"`
			Confirm bool   `json:"confirm"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		tasks, err := st.chanTasks()
		if err != nil {
			httpErr(w, err)
			return
		}
		for _, t := range tasks {
			if t.Slug != slug {
				continue
			}
			run := chanRunStates([]chanTask{t})[slug]
			code, msg := changeStatus(r, t, run, body.Status, body.Confirm)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": code == http.StatusOK, "message": msg})
			return
		}
		http.NotFound(w, req)
	})
}
