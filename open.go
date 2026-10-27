package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// openSession resumes a task's closed session in a new terminal tab via
// `flow do <slug>` (flow picks the terminal and runs `claude --resume`).
// Refuses when the session is already running: two processes on one session
// would interleave its transcript.
func openSession(r flowRunner, t chanTask, run string) (int, string) {
	switch {
	case t.SessionID == "":
		return http.StatusConflict, "no session yet; start it with: flow do " + t.Slug
	case t.Status == "done":
		return http.StatusConflict, "task is done"
	case run == "live":
		return http.StatusConflict, "session is already running"
	}
	if out, err := r.run("do", t.Slug); err != nil {
		return http.StatusInternalServerError, err.Error()
	} else {
		return http.StatusOK, out
	}
}

func registerOpenRoute(mux *http.ServeMux, st *store, r flowRunner) {
	mux.HandleFunc("/api/open/", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		slug := strings.TrimPrefix(req.URL.Path, "/api/open/")
		tasks, err := st.chanTasks()
		if err != nil {
			httpErr(w, err)
			return
		}
		for _, t := range tasks {
			if t.Slug != slug {
				continue
			}
			code, msg := openSession(r, t, chanRunStates([]chanTask{t})[slug])
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": code == http.StatusOK, "message": msg})
			return
		}
		http.NotFound(w, req)
	})
}
