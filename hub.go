package main

import (
	"sort"
	"strings"
)

// ---- Raw rows read from flow.db (one struct per source table) ----

type rawMessage struct {
	ID           string
	CreatedAt    string
	Kind         string // "message" | "broadcast"
	FromAssignee string
	FromTaskSlug string // "" when null
	ToAssignee   string
	ToTaskSlug   string // "" when null (=> the human inbox)
	Body         string
	Urgent       bool
	Status       string // pending | delivered | acked
	WaitedS      float64
	HasWaited    bool

	// mail-model columns (flow alpha.33+); HasMail=false on older schemas,
	// where "unread" falls back to status=='pending'.
	HasMail    bool
	SeenAt     string
	AnsweredAt string
	Options    []string
	ReplyTo    string
	FYI        bool

	// peer-hub columns (flow peers v0); "" on older schemas.
	Peer          string // outbound: target peer; inbound: source peer
	OriginRef     string // inbound: the other hub's message id
	Delivery      string // outbound: dispatched | sent | failed
	DeliveryError string
}

// peerIn: a row another hub delivered to us (flow's IsPeerInbound, plus a
// from-side fallback for rows missing origin_ref).
func (m rawMessage) peerIn() bool {
	if m.Peer == "" {
		return false
	}
	return m.OriginRef != "" || (m.FromTaskSlug == "" && m.FromAssignee == m.Peer && m.ToAssignee != m.Peer)
}

// peerOut: a row addressed to another hub.
func (m rawMessage) peerOut() bool { return m.Peer != "" && !m.peerIn() }

// peerNodeID is the graph/inbox key for a peer hub. Prefixed so a peer
// named like a local task slug can never collide with it.
func peerNodeID(name string) string { return "peer:" + name }

const connectorPrefix = "connector:"

// isConnectorSender: a connector-origin human notice (from_assignee=connector:<name>).
func isConnectorSender(m rawMessage) bool {
	return m.FromTaskSlug == "" && strings.HasPrefix(m.FromAssignee, connectorPrefix)
}

// msgFrom / msgTo resolve a message's endpoints to node ids, folding peer
// rows onto their remote-hub node.
func msgFrom(m rawMessage) string {
	if m.FromTaskSlug == "" && m.peerIn() {
		return peerNodeID(m.Peer)
	}
	return nodeID(m.FromAssignee, m.FromTaskSlug)
}

func msgTo(m rawMessage) string {
	if m.ToTaskSlug == "" && m.peerOut() {
		return peerNodeID(m.Peer)
	}
	return nodeID(m.ToAssignee, m.ToTaskSlug)
}

// unseen reports whether human mail is still unread.
func (m rawMessage) unseen() bool {
	if m.HasMail {
		return m.SeenAt == ""
	}
	return m.Status == "pending"
}

type rawWatch struct {
	Watcher string // "<assignee>/<task-slug>" or bare assignee
	Watched string // task slug, project, or assignee
}

type rawNudge struct {
	TaskSlug string
	Attempts int
	NudgedAt string
}

// taskMeta enriches a session node with its human-readable name + status.
type taskMeta struct {
	Name            string
	Status          string
	StatusChangedAt string // when it last changed status; for done tasks = when it died
	SessionID       string // harness session id; live if present in a running process
	AutoRunStatus   string // running|dead|completed for --auto tasks ("" = never an auto run)
	AutoRunPID      int    // pid of the background --auto run, 0 if none
	Project         string // project slug, "" for a floating task
}

// runState folds a task's nominal status together with real-process liveness
// into the one label the board trusts. It is pure: the caller supplies whether
// the task's harness session is attached to a live process (liveSession),
// probed by matching its session_id against the running process table (see
// liveness.go). Both interactive and --auto sessions run as
// `claude --session-id <id> …`, so this one signal covers both — and unlike a
// stored pid it can't be spoofed by the OS recycling a dead run's pid onto some
// unrelated process. This exists because tasks.status is set once at launch and
// never corrected on crash, so it silently lies for zombie sessions.
func runState(m taskMeta, liveSession bool) string {
	switch m.Status {
	case "done":
		return "done"
	case "backlog":
		return "backlog"
	}
	// in-progress: trust the OS, not the column.
	if liveSession {
		return "live"
	}
	if m.AutoRunStatus != "" {
		// It was launched as an --auto run and nothing is running now — a
		// failed/finished autonomous run that never reached `flow done`.
		return "crashed"
	}
	// An interactive session whose tab is closed: nominally in-progress but
	// simply detached, resumable with `flow do`. Not a failure.
	return "idle"
}

// ---- Output model served to the frontend ----

type Node struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`   // "human" | "session" | "target"
	Label  string `json:"label"`  // short display label
	Name   string `json:"name"`   // full task name (sessions only)
	Status string `json:"status"` // task status (sessions only)
	// Project is the task's project slug ("" = floating). Drives the graph's
	// project filter. Sessions only.
	Project string `json:"project,omitempty"`
	Nudge   int    `json:"nudge"`             // pending nudge attempts, 0 if none
	DeadAt  string `json:"dead_at,omitempty"` // when a done session finished (drives stale-hiding)
	// RunState is the real liveness the board paints by: live | idle | crashed
	// | done | backlog. Empty for non-session nodes (the human, targets).
	// Unlike Status, it reflects whether a process is actually running.
	RunState string `json:"run_state,omitempty"`
	// Launch is set on nodes the UI may start with `flow do --auto`: a backlog
	// task, or a crashed auto run to relaunch. Empty otherwise.
	Launch string `json:"launch,omitempty"` // "start" | "restart"
}

type Edge struct {
	ID      string  `json:"id"`
	From    string  `json:"from"`
	To      string  `json:"to"`
	Body    string  `json:"body,omitempty"`
	Status  string  `json:"status,omitempty"` // acked | delivered | pending
	Urgent  bool    `json:"urgent,omitempty"`
	WaitedS float64 `json:"waited_s,omitempty"`
	At      string  `json:"at,omitempty"`
	// Peer is set on traffic to/from another hub; Delivery (+error) on outbound.
	Peer          string `json:"peer,omitempty"`
	Delivery      string `json:"delivery,omitempty"`
	DeliveryError string `json:"delivery_error,omitempty"`
}

// Peer is one row of flow's peers table: another hub in the contact book.
type Peer struct {
	Name       string `json:"name"`
	Connector  string `json:"connector"`
	Blocked    bool   `json:"blocked"`
	CreatedAt  string `json:"created_at,omitempty"`
	LastSeenAt string `json:"last_seen_at,omitempty"`
}

type WatchEdge struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Mutual    bool   `json:"mutual"`
	SelfWatch bool   `json:"self_watch"`
}

type Stats struct {
	Participants int `json:"participants"`
	Messages     int `json:"messages"`
	Acked        int `json:"acked"`     // human-directed, answered
	Delivered    int `json:"delivered"` // session→session, handed off (terminal)
	Pending      int `json:"pending"`   // genuinely still waiting
	Watches      int `json:"watches"`
	Nudges       int `json:"nudges"`
}

// Broadcast is one fan-out event: a sender's one-liner that reached a set
// of watchers. flow materializes a broadcast as one row per watcher; we
// regroup those rows back into a single event with all its targets.
type Broadcast struct {
	From    string   `json:"from"`
	Body    string   `json:"body"`
	At      string   `json:"at"`
	Targets []string `json:"targets"`
}

type Hub struct {
	GeneratedAt string      `json:"generated_at"`
	Stats       Stats       `json:"stats"`
	Nodes       []Node      `json:"nodes"`
	Messages    []Edge      `json:"messages"`
	Watches     []WatchEdge `json:"watches"`
	Broadcasts  []Broadcast `json:"broadcasts"`
	Peers       []Peer      `json:"peers"`
}

// humanID is the canonical node id for the local human. Flow's reserved
// assignee is migrating from "self" to "user" (PR #94); we accept both and
// normalize to one node so the human hub doesn't split during the rename.
const humanID = "user"

// canonAssignee folds the human sentinel (self/user) to humanID and leaves
// every other assignee (task slug, project, other user) untouched.
func canonAssignee(a string) string {
	if a == "self" || a == "user" {
		return humanID
	}
	return a
}

// nodeID resolves a message endpoint to a graph node id.
// A null/empty task slug with the human assignee is the human hub.
func nodeID(assignee, taskSlug string) string {
	if taskSlug == "" {
		return canonAssignee(assignee) // self/user -> the human hub
	}
	return taskSlug
}

// watcherTask pulls the task slug out of a "<assignee>/<task>" watcher,
// or folds a bare human assignee to the canonical human id.
func watcherTask(watcher string) string {
	for i := 0; i < len(watcher); i++ {
		if watcher[i] == '/' {
			return watcher[i+1:]
		}
	}
	return canonAssignee(watcher)
}

// buildHub is the pure transformation from raw rows to the served model.
// It has no DB dependency so it can be unit-tested directly.
func buildHub(msgs []rawMessage, watches []rawWatch, nudges []rawNudge, tasks map[string]taskMeta, now string) Hub {
	nudgeBy := map[string]int{}
	for _, n := range nudges {
		nudgeBy[n.TaskSlug] = n.Attempts
	}

	// Collect every referenced node id.
	seen := map[string]bool{}
	order := []string{}
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		order = append(order, id)
	}
	add(humanID) // the human is always present

	for _, m := range msgs {
		add(msgFrom(m))
		add(msgTo(m))
	}
	for _, w := range watches {
		add(watcherTask(w.Watcher))
		add(canonAssignee(w.Watched))
	}
	for _, n := range nudges {
		add(n.TaskSlug)
	}

	nodes := make([]Node, 0, len(order))
	for _, id := range order {
		n := Node{ID: id, Label: id}
		switch {
		case id == humanID:
			n.Kind = "human"
			n.Label = "you"
			n.Name = "you · the human"
		case strings.HasPrefix(id, "peer:"):
			n.Kind = "peer"
			n.Label = strings.TrimPrefix(id, "peer:")
			n.Name = n.Label + " · remote hub"
		case strings.HasPrefix(id, connectorPrefix):
			n.Kind = "connector"
			n.Label = strings.TrimPrefix(id, connectorPrefix)
			n.Name = "connector " + n.Label
		case tasks[id].Name != "":
			n.Kind = "session"
			n.Name = tasks[id].Name
			n.Status = tasks[id].Status
			n.Project = tasks[id].Project
			n.Nudge = nudgeBy[id]
			if tasks[id].Status == "done" {
				n.DeadAt = tasks[id].StatusChangedAt
			}
		default:
			// Referenced but not a known in-flight task (a project,
			// an assignee, or a closed task): show it, but muted.
			n.Kind = "target"
			n.Nudge = nudgeBy[id]
		}
		nodes = append(nodes, n)
	}

	// Message edges (directed). Broadcasts have no single recipient, skip
	// them as edges — they fan out to watchers, represented separately.
	messages := make([]Edge, 0, len(msgs))
	acked, delivered, pending := 0, 0, 0
	for _, m := range msgs {
		if m.Kind != "message" {
			continue
		}
		e := Edge{
			ID:     m.ID,
			From:   msgFrom(m),
			To:     msgTo(m),
			Body:   m.Body,
			Status: m.Status,
			Urgent: m.Urgent,
			At:     m.CreatedAt,
			Peer:   m.Peer,
		}
		if m.peerOut() {
			e.Delivery, e.DeliveryError = m.Delivery, m.DeliveryError
		}
		if m.HasWaited {
			e.WaitedS = m.WaitedS
		}
		switch m.Status {
		case "acked":
			acked++
		case "delivered":
			delivered++
		default: // pending
			pending++
		}
		messages = append(messages, e)
	}

	// Broadcast events. flow fans one broadcast out to one row per watcher;
	// regroup by (sender, instant, body) so it reads as a single event with
	// all its targets rather than N duplicate rows.
	bcastBy := map[string]*Broadcast{}
	bcastOrder := []string{}
	for _, m := range msgs {
		if m.Kind != "broadcast" {
			continue
		}
		from := msgFrom(m)
		key := from + "\x00" + m.CreatedAt + "\x00" + m.Body
		b := bcastBy[key]
		if b == nil {
			b = &Broadcast{From: from, Body: m.Body, At: m.CreatedAt}
			bcastBy[key] = b
			bcastOrder = append(bcastOrder, key)
		}
		b.Targets = append(b.Targets, nodeID(m.ToAssignee, m.ToTaskSlug))
	}
	broadcasts := make([]Broadcast, 0, len(bcastOrder))
	for _, k := range bcastOrder {
		broadcasts = append(broadcasts, *bcastBy[k])
	}

	// Watch edges, with mutual + self detection.
	present := map[string]bool{}
	for _, w := range watches {
		present[watcherTask(w.Watcher)+"\x00"+canonAssignee(w.Watched)] = true
	}
	consumed := map[string]bool{}
	wedges := make([]WatchEdge, 0, len(watches))
	for _, w := range watches {
		from := watcherTask(w.Watcher)
		to := canonAssignee(w.Watched)
		if from == to {
			wedges = append(wedges, WatchEdge{From: from, To: to, SelfWatch: true})
			continue
		}
		key := from + "\x00" + to
		rev := to + "\x00" + from
		if consumed[key] {
			continue
		}
		mutual := present[rev]
		if mutual {
			consumed[rev] = true // collapse the pair into one edge
		}
		wedges = append(wedges, WatchEdge{From: from, To: to, Mutual: mutual})
	}

	stats := Stats{
		Participants: len(nodes),
		Messages:     len(messages),
		Acked:        acked,
		Delivered:    delivered,
		Pending:      pending,
		Watches:      len(watches),
		Nudges:       len(nudges),
	}

	// Stable node ordering: human first, then sessions, then targets, by id.
	sort.SliceStable(nodes, func(i, j int) bool {
		rank := func(n Node) int {
			switch n.Kind {
			case "human":
				return 0
			case "session":
				return 1
			case "peer":
				return 2
			case "connector":
				return 3
			default:
				return 4
			}
		}
		if rank(nodes[i]) != rank(nodes[j]) {
			return rank(nodes[i]) < rank(nodes[j])
		}
		return nodes[i].ID < nodes[j].ID
	})

	return Hub{
		Peers:       []Peer{},
		GeneratedAt: now,
		Stats:       stats,
		Nodes:       nodes,
		Messages:    messages,
		Watches:     wedges,
		Broadcasts:  broadcasts,
	}
}

// addPeerNodes records the peers table on the hub and gives every peer a
// remote-hub node, including peers with no traffic yet.
func addPeerNodes(h *Hub, peers []Peer) {
	if peers == nil {
		peers = []Peer{}
	}
	h.Peers = peers
	for _, p := range peers {
		id := peerNodeID(p.Name)
		var n *Node
		for i := range h.Nodes {
			if h.Nodes[i].ID == id {
				n = &h.Nodes[i]
			}
		}
		if n == nil {
			h.Nodes = append(h.Nodes, Node{ID: id, Kind: "peer", Label: p.Name})
			h.Stats.Participants++
			n = &h.Nodes[len(h.Nodes)-1]
		}
		n.Name = p.Name + " · remote hub via " + p.Connector
		if p.Blocked {
			n.Name += " (blocked)"
		}
	}
}
