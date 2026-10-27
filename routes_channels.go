package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sessionPage = 60

// registerChannelRoutes wires the read-only Channels API.
func registerChannelRoutes(mux *http.ServeMux, st *store, root, projectsDir string) {
	cache := newSessionCache()
	// agentLast: each task's last agent text from its session transcript,
	// merged over the newest update note when it is more recent.
	agentLast := func(tasks []chanTask, notes map[string]note) map[string]note {
		for _, t := range tasks {
			if t.SessionID == "" {
				continue
			}
			p := cache.path(projectsDir, t.SessionID)
			if p == "" {
				continue
			}
			text, at := cache.last(p)
			ts := parseT(at)
			if text == "" || ts.IsZero() {
				continue
			}
			if n, ok := notes[t.Slug]; !ok || ts.After(n.Time) {
				notes[t.Slug] = note{Time: ts, Title: text}
			}
		}
		return notes
	}
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
			"cards":   buildCards(mine, msgs, agentLast(mine, latestNotes(root, slugs)), runs, now),
		})
	}))

	mux.HandleFunc("/api/session/", get(func(w http.ResponseWriter, r *http.Request) {
		slug := strings.TrimPrefix(r.URL.Path, "/api/session/")
		cursor := func(name string) (int64, bool) {
			v := r.URL.Query().Get(name)
			if v == "" {
				return -1, true
			}
			n, err := strconv.ParseInt(v, 10, 64)
			return n, err == nil && n >= 0
		}
		before, ok1 := cursor("before")
		from, ok2 := cursor("from")
		if !ok1 || !ok2 {
			http.Error(w, "bad cursor", http.StatusBadRequest)
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
			p := ""
			if t.SessionID != "" {
				p = cache.path(projectsDir, t.SessionID)
			}
			if p == "" {
				writeJSON(w, map[string]any{"items": []sessItem{}, "start": 0, "found": false, "has_more": false})
				return
			}
			if from >= 0 {
				items, err := readSessionFrom(p, from)
				if err != nil {
					httpErr(w, err)
					return
				}
				writeJSON(w, map[string]any{"items": nonNil(items), "start": from, "found": true, "has_more": from > 0})
				return
			}
			items, start, err := readSessionTail(p, before, sessionPage)
			if err != nil {
				httpErr(w, err)
				return
			}
			writeJSON(w, map[string]any{"items": nonNil(items), "start": start, "found": true, "has_more": start > 0})
			return
		}
		http.NotFound(w, r)
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

func nonNil(items []sessItem) []sessItem {
	if items == nil {
		return []sessItem{}
	}
	return items
}
