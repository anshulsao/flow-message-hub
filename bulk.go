package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const bulkMax = 100

type bulkResult struct {
	Slug         string `json:"slug"`
	OK           bool   `json:"ok"`
	Message      string `json:"message,omitempty"`
	NeedsConfirm bool   `json:"needs_confirm,omitempty"`
}

// bulkStatus applies changeStatus to each slug in request order, so every
// task still goes through flow's own rules; one failure never stops the rest.
func bulkStatus(r flowRunner, tasks []chanTask, runs map[string]string, slugs []string, status string, confirm bool) []bulkResult {
	bySlug := make(map[string]chanTask, len(tasks))
	for _, t := range tasks {
		bySlug[t.Slug] = t
	}
	out := make([]bulkResult, 0, len(slugs))
	for _, s := range slugs {
		t, ok := bySlug[s]
		if !ok {
			out = append(out, bulkResult{Slug: s, Message: "no such task"})
			continue
		}
		code, msg := changeStatus(r, t, runs[s], status, confirm)
		res := bulkResult{Slug: s, OK: code == http.StatusOK}
		if !res.OK {
			res.Message = msg
			res.NeedsConfirm = code == http.StatusConflict && strings.Contains(msg, "confirm")
		}
		out = append(out, res)
	}
	return out
}

func registerBulkRoute(mux *http.ServeMux, st *store, r flowRunner) {
	mux.HandleFunc("/api/status-bulk", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Slugs   []string `json:"slugs"`
			Status  string   `json:"status"`
			Confirm bool     `json:"confirm"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		switch {
		case len(body.Slugs) == 0:
			http.Error(w, "no tasks selected", http.StatusBadRequest)
			return
		case len(body.Slugs) > bulkMax:
			http.Error(w, fmt.Sprintf("at most %d tasks at once", bulkMax), http.StatusBadRequest)
			return
		case body.Status != "backlog" && body.Status != "in-progress" && body.Status != "done" && body.Status != "archive":
			http.Error(w, "status must be backlog, in-progress, done or archive", http.StatusBadRequest)
			return
		}
		tasks, err := st.chanTasks()
		if err != nil {
			httpErr(w, err)
			return
		}
		var mine []chanTask
		want := map[string]bool{}
		for _, s := range body.Slugs {
			want[s] = true
		}
		for _, t := range tasks {
			if want[t.Slug] {
				mine = append(mine, t)
			}
		}
		res := bulkStatus(r, mine, chanRunStates(mine), body.Slugs, body.Status, body.Confirm)
		n := 0
		for _, x := range res {
			if x.OK {
				n++
			}
		}
		writeJSON(w, map[string]any{"results": res, "ok": n})
	})
}
