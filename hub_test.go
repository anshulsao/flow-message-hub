package main

import "testing"

func findNode(h Hub, id string) *Node {
	for i := range h.Nodes {
		if h.Nodes[i].ID == id {
			return &h.Nodes[i]
		}
	}
	return nil
}

func TestBuildHub_RoutesMessageToHumanHub(t *testing.T) {
	msgs := []rawMessage{{
		ID: "m1", Kind: "message",
		FromAssignee: "self", FromTaskSlug: "snabbit-databricks-uc",
		ToAssignee: "self", ToTaskSlug: "", // null -> the human
		Body: "fix live", Status: "acked", WaitedS: 266.8, HasWaited: true,
	}}
	tasks := map[string]taskMeta{"snabbit-databricks-uc": {Name: "Verify Snabbit", Status: "in-progress"}}
	h := buildHub(msgs, nil, nil, tasks, "now")

	if len(h.Messages) != 1 {
		t.Fatalf("want 1 message edge, got %d", len(h.Messages))
	}
	e := h.Messages[0]
	if e.From != "snabbit-databricks-uc" || e.To != "user" {
		t.Fatalf("bad route: %s -> %s", e.From, e.To)
	}
	if e.WaitedS != 266.8 {
		t.Fatalf("waited not carried: %v", e.WaitedS)
	}
	if hub := findNode(h, "user"); hub == nil || hub.Kind != "human" {
		t.Fatalf("human hub node missing/wrong: %+v", hub)
	}
	sess := findNode(h, "snabbit-databricks-uc")
	if sess == nil || sess.Kind != "session" || sess.Name != "Verify Snabbit" {
		t.Fatalf("session node not enriched: %+v", sess)
	}
	if h.Stats.Acked != 1 || h.Stats.Delivered != 0 || h.Stats.Pending != 0 {
		t.Fatalf("bad stats: %+v", h.Stats)
	}
}

// Both the old ("self") and new ("user", PR #94) human sentinels fold to
// one human hub node, so the graph survives the rename in either direction.
func TestBuildHub_SelfAndUserFoldToOneHuman(t *testing.T) {
	msgs := []rawMessage{
		{ID: "a", Kind: "message", FromAssignee: "self", FromTaskSlug: "t1", ToAssignee: "self", Body: "old"},
		{ID: "b", Kind: "message", FromAssignee: "user", FromTaskSlug: "t2", ToAssignee: "user", Body: "new"},
	}
	h := buildHub(msgs, nil, nil, map[string]taskMeta{}, "now")
	humans := 0
	for _, n := range h.Nodes {
		if n.Kind == "human" {
			humans++
			if n.ID != "user" {
				t.Fatalf("human node id should canonicalize to user, got %q", n.ID)
			}
		}
	}
	if humans != 1 {
		t.Fatalf("want exactly one human node, got %d", humans)
	}
	for _, e := range h.Messages {
		if e.To != "user" {
			t.Fatalf("both self- and user-directed msgs should route to user, got %q", e.To)
		}
	}
}

func TestBuildHub_SessionToSessionDelivered(t *testing.T) {
	msgs := []rawMessage{{
		ID: "m2", Kind: "message",
		FromAssignee: "self", FromTaskSlug: "grantable-uses",
		ToAssignee: "self", ToTaskSlug: "demo-apps",
		Body: "M1 done, go-ahead?", Status: "delivered",
	}}
	h := buildHub(msgs, nil, nil, map[string]taskMeta{}, "now")
	if len(h.Messages) != 1 {
		t.Fatalf("want 1 edge, got %d", len(h.Messages))
	}
	if h.Messages[0].From != "grantable-uses" || h.Messages[0].To != "demo-apps" {
		t.Fatalf("bad route: %+v", h.Messages[0])
	}
	if h.Stats.Delivered != 1 || h.Stats.Acked != 0 || h.Stats.Pending != 0 {
		t.Fatalf("session→session should count as delivered, not pending: %+v", h.Stats)
	}
}

func TestBuildHub_MutualWatchCollapsedOnce(t *testing.T) {
	watches := []rawWatch{
		{Watcher: "self/demo-apps", Watched: "grantable-uses"},
		{Watcher: "self/grantable-uses", Watched: "demo-apps"},
	}
	h := buildHub(nil, watches, nil, map[string]taskMeta{}, "now")
	if len(h.Watches) != 1 {
		t.Fatalf("mutual pair should collapse to 1 edge, got %d: %+v", len(h.Watches), h.Watches)
	}
	if !h.Watches[0].Mutual {
		t.Fatalf("edge not marked mutual: %+v", h.Watches[0])
	}
	if h.Stats.Watches != 2 {
		t.Fatalf("stats should still count 2 raw watch rows, got %d", h.Stats.Watches)
	}
}

func TestBuildHub_SelfWatchDetected(t *testing.T) {
	watches := []rawWatch{{Watcher: "self/pulse-x", Watched: "pulse-x"}}
	h := buildHub(nil, watches, nil, map[string]taskMeta{}, "now")
	if len(h.Watches) != 1 || !h.Watches[0].SelfWatch {
		t.Fatalf("self-watch not detected: %+v", h.Watches)
	}
}

func TestBuildHub_NudgeAttachedToNode(t *testing.T) {
	nudges := []rawNudge{{TaskSlug: "demo-apps", Attempts: 1}}
	tasks := map[string]taskMeta{"demo-apps": {Name: "Demo", Status: "in-progress"}}
	h := buildHub(nil, nil, nudges, tasks, "now")
	n := findNode(h, "demo-apps")
	if n == nil || n.Nudge != 1 {
		t.Fatalf("nudge not attached: %+v", n)
	}
	if h.Stats.Nudges != 1 {
		t.Fatalf("nudge stat wrong: %+v", h.Stats)
	}
}

func TestBuildHub_BroadcastNotAnEdge(t *testing.T) {
	msgs := []rawMessage{{ID: "b1", Kind: "broadcast", FromAssignee: "self", FromTaskSlug: "x", ToAssignee: "self", Body: "fyi"}}
	h := buildHub(msgs, nil, nil, map[string]taskMeta{}, "now")
	if len(h.Messages) != 0 {
		t.Fatalf("broadcast should not be a directed edge: %+v", h.Messages)
	}
}
