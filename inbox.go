package main

import (
	"sort"
	"strings"
)

// A single message inside a DM thread.
type ThreadMsg struct {
	ID     string `json:"id"`
	Dir    string `json:"dir"` // "in" (agent→you) | "out" (you→agent)
	Body   string `json:"body"`
	Status string `json:"status"`
	Urgent bool   `json:"urgent"`
	At     string `json:"at"`

	Seen     bool     `json:"seen"`               // inbound: human has seen it
	Answered bool     `json:"answered,omitempty"` // inbound ask: answered_at set
	Options  []string `json:"options,omitempty"`  // inbound ask: --options choices
	FYI      bool     `json:"fyi,omitempty"`      // inbound: --fyi, no answer expected
	ReplyTo  string   `json:"reply_to,omitempty"` // outbound: the ask it answers

	From          string `json:"from,omitempty"`           // outbound sent by an agent (task slug) instead of you
	Peer          string `json:"peer,omitempty"`           // peer-hub traffic: the other hub
	Delivery      string `json:"delivery,omitempty"`       // outbound peer: dispatched|sent|failed
	DeliveryError string `json:"delivery_error,omitempty"` // outbound peer: why it failed
}

// A DM thread = all traffic between you and one agent/task.
type InboxThread struct {
	Task       string      `json:"task"`                  // task slug, "~" (general), "peer:<name>" or "connector:<name>"
	Kind       string      `json:"kind"`                  // "task" | "general" | "peer" | "connector"
	Peer       string      `json:"peer,omitempty"`        // peer threads: the remote hub's name
	ReplyID    string      `json:"reply_id,omitempty"`    // peer threads: newest inbound (reply target when no open ask)
	Name       string      `json:"name"`                  // human-readable task name
	Repliable  bool        `json:"repliable"`             // false for general / finished agents
	LockReason string      `json:"lock_reason,omitempty"` // why the composer is locked
	Urgent     bool        `json:"urgent"`                // any unread message is urgent
	State      string      `json:"state"`                 // "unread" | "owe-reply" | "answered" | "closed"
	Unread     int         `json:"unread"`                // count of pending inbound
	PendingIDs []string    `json:"pending_ids"`           // unseen inbound ids to mark seen
	AskID      string      `json:"ask_id,omitempty"`      // newest unanswered inbound ask (reply target)
	LatestAt   string      `json:"latest_at"`
	Messages   []ThreadMsg `json:"messages"`
}

type InboxView struct {
	Threads   []InboxThread `json:"threads"`
	Unread    int           `json:"unread"`    // threads with an unread inbound
	Owe       int           `json:"owe"`       // threads seen but not yet answered
	Connector bool          `json:"connector"` // flowhub connector installed (reply/seen go via flow human)
	Pinged    []string      `json:"pinged"`    // ask ids flow pinged us about (browser notification)
}

// buildInbox derives the human's DM threads from the raw bus rows. Pure, no DB.
//
// Direction rules (a message's kind must be "message"):
//
//	inbound  (agent→you): to the human (to_task_slug=="" && to_assignee is self/user)
//	                      keyed by from_task_slug ("~" if the sender is unbound)
//	reply    (you→agent): from unbound human (from_task_slug=="" && from_assignee is self/user)
//	                      to a task (to_task_slug!=""), keyed by to_task_slug
//
// Peer-to-peer traffic (both ends carry a task slug) is NOT part of the inbox.
func buildInbox(msgs []rawMessage, tasks map[string]taskMeta) InboxView {
	type acc struct {
		th         *InboxThread
		lastIn     string // created_at of newest inbound
		lastInPd   bool   // newest inbound is unseen
		lastInDone bool   // newest inbound is answered (or an fyi)
		askAt      string // created_at of the newest unanswered ask
		lastOut    string // created_at of newest reply
		anyUrgent  bool
	}
	byTask := map[string]*acc{}
	order := []string{}
	get := func(key string) *acc {
		a := byTask[key]
		if a == nil {
			th := &InboxThread{Task: key, Name: key, Repliable: true, Kind: "task"}
			switch {
			case strings.HasPrefix(key, "peer:"):
				th.Kind = "peer"
				th.Peer = strings.TrimPrefix(key, "peer:")
				th.Name = th.Peer + " · remote hub"
			case strings.HasPrefix(key, connectorPrefix):
				th.Kind = "connector"
				th.Name = "connector " + strings.TrimPrefix(key, connectorPrefix)
				th.Repliable = false
				th.LockReason = "connector notice — nothing to reply to"
			case key == "~":
				th.Kind = "general"
				th.Name = "general (no session)"
				th.Repliable = false
				th.LockReason = "general thread — no session to reply to"
			case tasks[key].Status == "done":
				if tasks[key].Name != "" {
					th.Name = tasks[key].Name
				}
				th.Repliable = false
				th.LockReason = "agent's task is done — it won't receive a reply"
			default:
				if tasks[key].Name != "" {
					th.Name = tasks[key].Name
				}
			}
			a = &acc{th: th}
			byTask[key] = a
			order = append(order, key)
		}
		return a
	}

	// ids of peer mail to the human: an agent's reply to one of these
	// (flow message <peer> --reply-to <id>) is shown in that peer's thread.
	peerMail := map[string]bool{}
	for _, m := range msgs {
		if m.Kind == "message" && m.ToTaskSlug == "" && canonAssignee(m.ToAssignee) == humanID && m.peerIn() {
			peerMail[m.ID] = true
		}
	}

	for _, m := range msgs {
		if m.Kind != "message" {
			continue
		}
		// accept both the old "self" and new "user" (PR #94) human sentinel
		toHuman := m.ToTaskSlug == "" && canonAssignee(m.ToAssignee) == humanID
		fromHuman := m.FromTaskSlug == "" && canonAssignee(m.FromAssignee) == humanID

		if toHuman {
			// inbound agent→you, peer hub→you, connector notice, or general
			key := m.FromTaskSlug
			switch {
			case key != "":
			case m.peerIn():
				key = peerNodeID(m.Peer)
			case isConnectorSender(m):
				key = m.FromAssignee
			default:
				key = "~"
			}
			a := get(key)
			unseen := m.unseen()
			a.th.Messages = append(a.th.Messages, ThreadMsg{
				ID: m.ID, Dir: "in", Body: m.Body, Status: m.Status, Urgent: m.Urgent, At: m.CreatedAt,
				Seen: !unseen, Answered: m.AnsweredAt != "", Options: m.Options, FYI: m.FYI,
				Peer: m.Peer,
			})
			if unseen {
				a.th.PendingIDs = append(a.th.PendingIDs, m.ID)
			}
			if m.CreatedAt >= a.lastIn {
				a.lastIn = m.CreatedAt
				if a.th.Kind == "peer" {
					a.th.ReplyID = m.ID
				}
				a.lastInPd = unseen
				a.lastInDone = m.HasMail && (m.AnsweredAt != "" || m.FYI)
			}
			if m.HasMail && m.AnsweredAt == "" && !m.FYI && m.CreatedAt >= a.askAt {
				a.askAt = m.CreatedAt
				a.th.AskID = m.ID
			}
			if m.Urgent {
				a.anyUrgent = true
			}
			continue
		}
		if m.ToTaskSlug == "" && m.peerOut() && (fromHuman || peerMail[m.ReplyTo]) {
			// you (or an agent answering for you) → peer hub, routed out
			// over the peer's connector
			a := get(peerNodeID(m.Peer))
			a.th.Messages = append(a.th.Messages, ThreadMsg{
				ID: m.ID, Dir: "out", Body: m.Body, Status: m.Status, At: m.CreatedAt, ReplyTo: m.ReplyTo,
				From: m.FromTaskSlug, Peer: m.Peer, Delivery: m.Delivery, DeliveryError: m.DeliveryError,
			})
			if m.CreatedAt >= a.lastOut {
				a.lastOut = m.CreatedAt
			}
			continue
		}
		if fromHuman && m.ToTaskSlug != "" {
			// reply you→agent's task
			a := get(m.ToTaskSlug)
			a.th.Messages = append(a.th.Messages, ThreadMsg{
				ID: m.ID, Dir: "out", Body: m.Body, Status: m.Status, At: m.CreatedAt, ReplyTo: m.ReplyTo,
			})
			if m.CreatedAt >= a.lastOut {
				a.lastOut = m.CreatedAt
			}
		}
		// else: peer→peer, ignored for the inbox
	}

	v := InboxView{}
	for _, key := range order {
		a := byTask[key]
		// a thread only exists if it received at least one inbound message
		hasInbound := false
		for _, mm := range a.th.Messages {
			if mm.Dir == "in" {
				hasInbound = true
				break
			}
		}
		if !hasInbound {
			continue
		}
		sort.SliceStable(a.th.Messages, func(i, j int) bool { return a.th.Messages[i].At < a.th.Messages[j].At })
		a.th.Unread = len(a.th.PendingIDs)
		a.th.Urgent = a.anyUrgent && a.lastInPd
		a.th.LatestAt = a.lastIn
		if a.lastOut > a.lastIn {
			a.th.LatestAt = a.lastOut
		}

		switch {
		case a.lastInPd:
			a.th.State = "unread"
			v.Unread++
		case a.lastInDone || (a.lastOut >= a.lastIn && a.lastOut != ""):
			a.th.State = "answered"
		case !a.th.Repliable:
			// seen but you can't reply (finished agent / general thread):
			// it's closed, not an open todo — don't nag with an owe-star.
			a.th.State = "closed"
		default:
			a.th.State = "owe-reply"
			v.Owe++
		}
		v.Threads = append(v.Threads, *a.th)
	}

	// newest activity first
	sort.SliceStable(v.Threads, func(i, j int) bool { return v.Threads[i].LatestAt > v.Threads[j].LatestAt })
	return v
}
