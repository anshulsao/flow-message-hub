package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// resumeAuto relaunches a stopped auto run headlessly on its existing
// session (`flow do --auto <slug> [--with <answer>]`); flow resumes the
// session so the agent keeps its context and sees the answer.
func resumeAuto(r flowRunner, t chanTask, run, text string) (int, string) {
	switch {
	case t.SessionID == "":
		return http.StatusConflict, "no session yet; start it with: flow do --auto " + t.Slug
	case t.Status == "done":
		return http.StatusConflict, "task is done"
	case run == "live":
		return http.StatusConflict, "session is already running"
	}
	args := []string{"do", "--auto", t.Slug}
	if strings.TrimSpace(text) != "" {
		clean, err := sanitizePrompt(text)
		if err != nil {
			return http.StatusBadRequest, err.Error()
		}
		args = append(args, "--with", clean)
	}
	if out, err := r.run(args...); err != nil {
		return http.StatusInternalServerError, err.Error()
	} else {
		return http.StatusOK, out
	}
}

func registerResumeRoute(mux *http.ServeMux, st *store, r flowRunner) {
	mux.HandleFunc("/api/resume-auto/", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		slug := strings.TrimPrefix(req.URL.Path, "/api/resume-auto/")
		var body struct {
			Text string `json:"text"`
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
			code, msg := resumeAuto(r, t, chanRunStates([]chanTask{t})[slug], body.Text)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": code == http.StatusOK, "message": msg})
			return
		}
		http.NotFound(w, req)
	})
}
