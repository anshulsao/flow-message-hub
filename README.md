# Flow Message Hub

A live, browser-based view of [`flow`](https://github.com/Facets-cloud/flow)'s
message bus, plus a Slack-like DM inbox for answering the agents that message
you. It shows, in real time: who is on the bus (agent sessions + you), who
messaged whom, what is still awaiting a reply, watch subscriptions, and
broadcast fan-out.

It reads `~/.flow/flow.db` **read-only**. The only writes are the ones you
make from the inbox (a reply or marking a thread seen), and those go through
the `flow` CLI, never the database directly.

```
   ~/.flow/flow.db ──read-only──▶ Go (net/http + modernc.org/sqlite)
                                    ├─ GET /api/hub      live graph snapshot
                                    ├─ GET /api/inbox    your DM threads
                                    ├─ GET /api/agents   in-progress sessions you can message
                                    ├─ POST /api/threads/{task}/seen   ack pending (pop-on-seen)
                                    ├─ POST /api/threads/{task}/reply  flow message <you>/<task>
                                    └─ GET /             embedded single-page app (D3)
                                         │ polls every 2s
                                         ▼
                                    force-directed graph + live feed + inbox
```

## What you get

- **Graph** — a D3 force-directed map. Your node is the amber hub; agent
  sessions orbit it; finished/dead sessions are greyed with static links.
  Blue wires are messages, indigo dashed are watches, indigo dotted are
  broadcasts; a moving packet means it is live; red means urgent.
- **Feed** — every bus signal in reverse chronological order, with delivery
  ticks and status.
- **Inbox** — the agents that messaged you, as DM threads. Opening a thread
  marks it seen (pop-on-seen); you can reply, or start a new thread with any
  in-progress agent. Threads you cannot answer (a finished agent, or the
  no-session general thread) show as closed rather than as an open to-do.

## Prerequisites

- Go 1.24+ (uses the pure-Go `modernc.org/sqlite` driver — no CGO).
- A working [`flow`](https://github.com/Facets-cloud/flow) install with the
  message bus, and a `~/.flow/flow.db` to read.

## Run

```sh
go run .                       # serves http://127.0.0.1:8787
go run . --addr :9000          # different port / interface
go run . --db /path/to/flow.db # a specific database
```

Then open the printed URL. The page is self-contained (D3 is loaded from a
CDN) and updates itself; no build step is needed to run.

### Configuration

| Flag / Env    | Default             | Purpose                                             |
|---------------|---------------------|-----------------------------------------------------|
| `--addr`      | `127.0.0.1:8787`    | Listen address.                                     |
| `--db`        | auto                | Path to `flow.db`.                                  |
| `FLOW_DB`     | —                   | Full path to `flow.db` (overrides auto-detect).     |
| `FLOW_ROOT`   | —                   | Directory containing `flow.db` (e.g. `~/.flow`).    |
| `FLOW_BIN`    | `flow`              | The `flow` binary used for inbox writes.            |
| `FLOW_HUMAN`  | `user`              | Reserved human address. Set `FLOW_HUMAN=self` for a `flow` build from before the `self`→`user` rename (flow PR #94). |

Auto-detect order for the database: `FLOW_DB`, then `FLOW_ROOT/flow.db`, then
`~/.flow/flow.db`.

## Build a binary

```sh
go build -o flowhub .
./flowhub --addr 127.0.0.1:8787
```

## Layout

```
main.go        HTTP server; embeds index.html (go:embed) — rebuild after editing it
hub.go         raw bus rows → graph model (nodes, messages, watches, broadcasts). Unit-tested.
inbox.go       bus rows → DM threads (unread / owe-reply / answered / closed). Unit-tested.
db.go          read-only SQLite reader (bus_messages, bus_watches, bus_nudges, tasks)
flowexec.go    inbox writes via the flow CLI (reply, seen); never touches the db handle
index.html     single-page app: D3 force graph + feed + inbox
*_test.go      go test ./...
```

Note: `index.html` is embedded into the binary via `go:embed`, so after
editing it you must rebuild (`go build`) for the change to be served.

## Test

```sh
go test ./...
```

## Contributing / pushing changes

```sh
# fork or clone, then:
git checkout -b my-change
# ... edit; if you touched index.html:
go build -o flowhub . && go test ./...
git commit -am "describe the change"
git push -u origin my-change
# open a PR against the default branch
```

Keep the database access read-only: all bus mutations go through the `flow`
CLI in `flowexec.go`, never through the SQLite handle in `db.go`.

## Notes

- Reads accept both the `self` and `user` human sentinel, so the graph and
  feed work against any recent `flow`. Inbox **writes** target `FLOW_HUMAN`
  (default `user`); set `FLOW_HUMAN=self` if your `flow` predates the rename.
- Nothing is persisted by this app; it is a live view over `flow.db`. Stop it
  any time — it holds no state of its own.
