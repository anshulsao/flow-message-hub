package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// setProject moves a task into target ("" = no project) via the flow CLI.
// work_dir is left alone by flow, so an attached session keeps working.
func setProject(r flowRunner, t chanTask, target string, known []project) (int, string) {
	if t.Project == target {
		if target == "" {
			return http.StatusConflict, "already has no project"
		}
		return http.StatusConflict, "already in " + target
	}
	var args []string
	if target == "" {
		args = []string{"update", "task", t.Slug, "--clear-project"}
	} else {
		found := false
		for _, p := range known {
			found = found || p.Slug == target
		}
		if !found {
			return http.StatusBadRequest, "unknown project " + target
		}
		args = []string{"update", "task", t.Slug, "--project", target}
	}
	if out, err := r.run(args...); err != nil {
		return http.StatusInternalServerError, err.Error()
	} else {
		return http.StatusOK, out
	}
}

func registerProjectRoutes(mux *http.ServeMux, st *store, r flowRunner) {
	mux.HandleFunc("/api/projects", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		ps, err := st.projects()
		if err != nil {
			httpErr(w, err)
			return
		}
		if ps == nil {
			ps = []project{}
		}
		writeJSON(w, ps)
	})
	mux.HandleFunc("/api/task-project/", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		slug := strings.TrimPrefix(req.URL.Path, "/api/task-project/")
		var body struct {
			Project string `json:"project"`
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
		ps, err := st.projects()
		if err != nil {
			httpErr(w, err)
			return
		}
		for _, t := range tasks {
			if t.Slug != slug {
				continue
			}
			code, msg := setProject(r, t, body.Project, ps)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": code == http.StatusOK, "message": msg})
			return
		}
		http.NotFound(w, req)
	})
}
