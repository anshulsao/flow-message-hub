# flow-hub-live

A live web view of flow's message bus — participants (agent sessions +
the human `self`), directed messages (who sent what to whom, with
status + wait time), watch subscriptions (mutual + self-watch), and
pending nudges. Reads `~/.flow/flow.db` **read-only**; nothing is ever
written back.

```
flow.db ──ro──▶ Go (net/http + modernc.org/sqlite)
                   ├─ GET /api/hub  → live JSON snapshot
                   └─ GET /         → embedded React page
                        │ polls /api/hub every 2.5s
                        ▼
                   radial network graph + message log (auto-updates)
```

## Run

```
go run .                       # serves http://127.0.0.1:8787
go run . --addr :9000          # different port
go run . --db /path/to/flow.db # different database
```

Env overrides: `FLOW_DB` (full path) or `FLOW_ROOT` (dir holding
`flow.db`). Build a binary with `go build -o flowhub .`.

## Layout

- `hub.go`   — pure raw-rows → served-model transform (unit-tested)
- `hub_test.go` — mutual/self-watch, routing-to-human, nudge, broadcast
- `db.go`    — read-only SQLite reader (bus_messages/watches/nudges/tasks)
- `main.go`  — HTTP server, embeds `index.html` (`go:embed`, so rebuild after editing it)
- `index.html` — app-shell frontend: **D3-force** graph (center) + live
  transmission feed (right panel). Signal packets animate along the wires
  sender→target; node glow on send/receive; dark/light control-room theme.
  D3 v7 loaded from cdnjs; no build step. **Rebuild the Go binary to pick up
  edits** (`go build -o flowhub .`) since the HTML is embedded.

## Notes

- `self` = the human hub; a message with a null `to_task_slug` routes to it.
- Broadcasts fan out to watchers and aren't drawn as directed edges.
- A referenced slug that isn't an in-flight task (a project, assignee, or
  closed task) shows as a muted `target` node.
