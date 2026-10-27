package main

import (
	"net/http"
	"strings"
	"time"
)

// registerChannelRoutes wires the read-only Channels API.
func registerChannelRoutes(mux *http.ServeMux, st *store, root string) {
	get := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "read-only", http.StatusMethodNotAllowed)
				return
			}
			h(w, r)
		}
	}
	load := func(w http.ResponseWriter) ([]chanTask, []rawMessage, bool) {
		tasks, err := st.chanTasks()
		if err != nil {
			httpErr(w, err)
			return nil, nil, false
		}
		msgs, err := st.messages()
		if err != nil {
			httpErr(w, err)
			return nil, nil, false
		}
		return tasks, msgs, true
	}

	mux.HandleFunc("/api/channels", get(func(w http.ResponseWriter, r *http.Request) {
		tasks, msgs, ok := load(w)
		if !ok {
			return
		}
		ps, err := st.projects()
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, buildChannelList(ps, tasks, msgs, chanRunStates(tasks), time.Now()))
	}))

	mux.HandleFunc("/api/channel/", get(func(w http.ResponseWriter, r *http.Request) {
		slug := strings.TrimPrefix(r.URL.Path, "/api/channel/")
		tasks, msgs, ok := load(w)
		if !ok {
			return
		}
		var mine []chanTask
		var slugs []string
		for _, t := range tasks {
			if channelOf(t) == slug {
				mine = append(mine, t)
				slugs = append(slugs, t.Slug)
			}
		}
		if len(mine) == 0 {
			http.NotFound(w, r)
			return
		}
		ps, err := st.projects()
		if err != nil {
			httpErr(w, err)
			return
		}
		runs := chanRunStates(mine)
		now := time.Now()
		var summary ChannelSummary
		for _, c := range buildChannelList(ps, mine, msgs, runs, now).Channels {
			if c.Slug == slug {
				summary = c
			}
		}
		writeJSON(w, map[string]any{
			"channel": summary,
			"cards":   buildCards(mine, msgs, latestNotes(root, slugs), runs, now),
		})
	}))

	mux.HandleFunc("/api/thread/", get(func(w http.ResponseWriter, r *http.Request) {
		slug := strings.TrimPrefix(r.URL.Path, "/api/thread/")
		tasks, msgs, ok := load(w)
		if !ok {
			return
		}
		for _, t := range tasks {
			if t.Slug != slug {
				continue
			}
			brief, notes := readTaskFiles(root, slug)
			run := chanRunStates([]chanTask{t})[slug]
			writeJSON(w, buildThread(t, brief, notes, msgs, run))
			return
		}
		http.NotFound(w, r)
	}))
}
