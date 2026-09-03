package main

import "sort"

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
}

type rawWatch struct {
	Watcher string // "<assignee>/<task-slug>" or bare assignee
	Watched string // task slug, project, or assignee
}

type rawNudge struct {
	TaskSlug  string
	Attempts  int
	NudgedAt  string
}

// taskMeta enriches a session node with its human-readable name + status.
type taskMeta struct {
	Name   string
	Status string
}

// ---- Output model served to the frontend ----

type Node struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`   // "human" | "session" | "target"
	Label  string `json:"label"`  // short display label
	Name   string `json:"name"`   // full task name (sessions only)
	Status string `json:"status"` // task status (sessions only)
	Nudge  int    `json:"nudge"`  // pending nudge attempts, 0 if none
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

type Hub struct {
	GeneratedAt string      `json:"generated_at"`
	Stats       Stats       `json:"stats"`
	Nodes       []Node      `json:"nodes"`
	Messages    []Edge      `json:"messages"`
	Watches     []WatchEdge `json:"watches"`
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
		add(nodeID(m.FromAssignee, m.FromTaskSlug))
		add(nodeID(m.ToAssignee, m.ToTaskSlug))
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
		case tasks[id].Name != "":
			n.Kind = "session"
			n.Name = tasks[id].Name
			n.Status = tasks[id].Status
			n.Nudge = nudgeBy[id]
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
			From:   nodeID(m.FromAssignee, m.FromTaskSlug),
			To:     nodeID(m.ToAssignee, m.ToTaskSlug),
			Body:   m.Body,
			Status: m.Status,
			Urgent: m.Urgent,
			At:     m.CreatedAt,
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
			default:
				return 2
			}
		}
		if rank(nodes[i]) != rank(nodes[j]) {
			return rank(nodes[i]) < rank(nodes[j])
		}
		return nodes[i].ID < nodes[j].ID
	})

	return Hub{
		GeneratedAt: now,
		Stats:       stats,
		Nodes:       nodes,
		Messages:    messages,
		Watches:     wedges,
	}
}
