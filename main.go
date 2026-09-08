package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "embed"
)

//go:embed index.html
var indexHTML []byte

func defaultDBPath() string {
	if p := os.Getenv("FLOW_DB"); p != "" {
		return p
	}
	if root := os.Getenv("FLOW_ROOT"); root != "" {
		return filepath.Join(root, "flow.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".flow", "flow.db")
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address")
	dbPath := flag.String("db", defaultDBPath(), "path to flow.db")
	flag.Parse()

	st, err := openStore(*dbPath)
	if err != nil {
		log.Fatalf("open flow.db (%s): %v", *dbPath, err)
	}
	log.Printf("reading bus from %s (read-only)", *dbPath)

	fx := newFlowExec()
	mux := http.NewServeMux()

	// Live DM inbox derived from the bus (human-directed threads).
	mux.HandleFunc("/api/inbox", func(w http.ResponseWriter, r *http.Request) {
		msgs, err := st.messages()
		if err != nil {
			httpErr(w, err)
			return
		}
		tasks, err := st.tasks()
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, buildInbox(msgs, tasks))
	})

	// Agents you can start a conversation with = live (in-progress) sessions.
	mux.HandleFunc("/api/agents", func(w http.ResponseWriter, r *http.Request) {
		tasks, err := st.tasks()
		if err != nil {
			httpErr(w, err)
			return
		}
		type agent struct {
			Slug string `json:"slug"`
			Name string `json:"name"`
		}
		var out []agent
		for slug, m := range tasks {
			if m.Status == "in-progress" {
				out = append(out, agent{Slug: slug, Name: m.Name})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		writeJSON(w, out)
	})

	// POST /api/threads/{task}/seen   -> ack the given pending ids (pop-on-seen)
	// POST /api/threads/{task}/reply  -> route an answer back to the agent
	mux.HandleFunc("/api/threads/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/threads/")
		slash := strings.LastIndex(rest, "/")
		if slash < 0 {
			http.NotFound(w, r)
			return
		}
		task, action := rest[:slash], rest[slash+1:]
		switch action {
		case "seen":
			var body struct {
				IDs []string `json:"ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			var acked []string
			for _, id := range body.IDs {
				if err := fx.Ack(id); err != nil {
					httpErr(w, err)
					return
				}
				acked = append(acked, id)
			}
			writeJSON(w, map[string]any{"ok": true, "acked": acked})
		case "reply":
			var body struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				httpErr(w, err)
				return
			}
			if err := fx.Reply(task, body.Text); err != nil {
				httpErr(w, err)
				return
			}
			writeJSON(w, map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/api/hub", func(w http.ResponseWriter, r *http.Request) {
		msgs, err := st.messages()
		if err != nil {
			httpErr(w, err)
			return
		}
		watches, err := st.watches()
		if err != nil {
			httpErr(w, err)
			return
		}
		nudges, err := st.nudges()
		if err != nil {
			httpErr(w, err)
			return
		}
		tasks, err := st.tasks()
		if err != nil {
			httpErr(w, err)
			return
		}
		hub := buildHub(msgs, watches, nudges, tasks, time.Now().Format(time.RFC3339))
		enrichLiveness(hub.Nodes, tasks) // replace stale status with real liveness
		writeJSON(w, hub)
	})

	// Tasks the UI can launch headlessly: backlog (start) + crashed (restart).
	mux.HandleFunc("/api/launchable", func(w http.ResponseWriter, r *http.Request) {
		tasks, err := st.tasks()
		if err != nil {
			httpErr(w, err)
			return
		}
		states := taskRunStates(tasks)
		type launchable struct {
			Slug  string `json:"slug"`
			Name  string `json:"name"`
			State string `json:"state"` // "backlog" | "crashed"
			Verb  string `json:"verb"`  // "start" | "restart"
		}
		var out []launchable
		for slug, m := range tasks {
			if v := launchVerb(states[slug]); v != "" {
				out = append(out, launchable{Slug: slug, Name: m.Name, State: states[slug], Verb: v})
			}
		}
		// Crashed (needs attention) first, then backlog; each group by name.
		sort.Slice(out, func(i, j int) bool {
			if out[i].State != out[j].State {
				return out[i].State == "crashed"
			}
			return out[i].Name < out[j].Name
		})
		writeJSON(w, out)
	})

	// POST /api/tasks/{slug}/run-auto -> launch `flow do --auto <slug>`.
	// The server re-derives the task's real state and only launches a task
	// that is genuinely backlog or crashed, passing --fresh for a restart.
	mux.HandleFunc("/api/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
		slash := strings.LastIndex(rest, "/")
		if slash < 0 || rest[slash+1:] != "run-auto" {
			http.NotFound(w, r)
			return
		}
		slug := rest[:slash]
		tasks, err := st.tasks()
		if err != nil {
			httpErr(w, err)
			return
		}
		if _, ok := tasks[slug]; !ok {
			http.Error(w, "no such task", http.StatusNotFound)
			return
		}
		state := taskRunStates(tasks)[slug] // real state incl. process liveness
		verb := launchVerb(state)
		if verb == "" {
			http.Error(w, "task is not launchable (state="+state+")", http.StatusConflict)
			return
		}
		if err := fx.RunAuto(slug, verb == "restart"); err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "slug": slug, "verb": verb})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})

	log.Printf("flow message hub -> http://%s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
