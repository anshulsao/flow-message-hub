package main

import "sort"

// A single message inside a DM thread.
type ThreadMsg struct {
	ID     string `json:"id"`
	Dir    string `json:"dir"` // "in" (agent→you) | "out" (you→agent)
	Body   string `json:"body"`
	Status string `json:"status"`
	Urgent bool   `json:"urgent"`
	At     string `json:"at"`
}

// A DM thread = all traffic between you and one agent/task.
type InboxThread struct {
	Task       string      `json:"task"`       // agent's task slug, or "~" for general
	Name       string      `json:"name"`       // human-readable task name
	Repliable  bool        `json:"repliable"`   // false for general / finished agents
	LockReason string      `json:"lock_reason,omitempty"` // why the composer is locked
	Urgent     bool        `json:"urgent"`      // any unread message is urgent
	State      string      `json:"state"`      // "unread" | "owe-reply" | "answered"
	Unread     int         `json:"unread"`     // count of pending inbound
	PendingIDs []string    `json:"pending_ids"`// inbound ids to ack on "seen"
	LatestAt   string      `json:"latest_at"`
	Messages   []ThreadMsg `json:"messages"`
}

type InboxView struct {
	Threads []InboxThread `json:"threads"`
	Unread  int           `json:"unread"` // threads with an unread inbound
	Owe     int           `json:"owe"`    // threads seen but not yet answered
}

// buildInbox derives the human's DM threads from the raw bus rows. Pure, no DB.
//
// Direction rules (a message's kind must be "message"):
//   inbound  (agent→you): to the human (to_task_slug=="" && to_assignee is self/user)
//                         keyed by from_task_slug ("~" if the sender is unbound)
//   reply    (you→agent): from unbound human (from_task_slug=="" && from_assignee is self/user)
//                         to a task (to_task_slug!=""), keyed by to_task_slug
//
// Peer-to-peer traffic (both ends carry a task slug) is NOT part of the inbox.
func buildInbox(msgs []rawMessage, tasks map[string]taskMeta) InboxView {
	type acc struct {
		th       *InboxThread
		lastIn   string // created_at of newest inbound
		lastInPd bool   // newest inbound is pending
		lastOut  string // created_at of newest reply
		anyUrgent bool
	}
	byTask := map[string]*acc{}
	order := []string{}
	get := func(key string) *acc {
		a := byTask[key]
		if a == nil {
			th := &InboxThread{Task: key, Name: key, Repliable: true}
			switch {
			case key == "~":
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

	for _, m := range msgs {
		if m.Kind != "message" {
			continue
		}
		// accept both the old "self" and new "user" (PR #94) human sentinel
		toHuman := m.ToTaskSlug == "" && canonAssignee(m.ToAssignee) == humanID
		fromHuman := m.FromTaskSlug == "" && canonAssignee(m.FromAssignee) == humanID

		if toHuman {
			// inbound agent→you (or general if the sender is unbound)
			key := m.FromTaskSlug
			if key == "" {
				key = "~"
			}
			a := get(key)
			a.th.Messages = append(a.th.Messages, ThreadMsg{
				ID: m.ID, Dir: "in", Body: m.Body, Status: m.Status, Urgent: m.Urgent, At: m.CreatedAt,
			})
			if m.Status == "pending" {
				a.th.PendingIDs = append(a.th.PendingIDs, m.ID)
			}
			if m.CreatedAt >= a.lastIn {
				a.lastIn = m.CreatedAt
				a.lastInPd = m.Status == "pending"
			}
			if m.Urgent {
				a.anyUrgent = true
			}
			continue
		}
		if fromHuman && m.ToTaskSlug != "" {
			// reply you→agent's task
			a := get(m.ToTaskSlug)
			a.th.Messages = append(a.th.Messages, ThreadMsg{
				ID: m.ID, Dir: "out", Body: m.Body, Status: m.Status, At: m.CreatedAt,
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
		case a.lastOut >= a.lastIn && a.lastOut != "":
			a.th.State = "answered"
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
