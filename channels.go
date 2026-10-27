package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Channels view: projects are channels, tasks are threads. Everything here is
// pure (no DB, no filesystem) so it is driven entirely by tests.

const (
	floatingSlug = "_floating"
	staleAfter   = 7 * 24 * time.Hour
	latestMax    = 160
)

type project struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// chanTask is the slice of a tasks row the Channels view needs.
type chanTask struct {
	Slug, Name, Project, Status, Priority, WaitingOn string
	AutoRunStatus, AutoRunStarted, AutoRunFinished   string
	CreatedAt, UpdatedAt, SessionID                  string
}

// note is one file under ~/.flow/tasks/<slug>/updates/.
type note struct {
	Time  time.Time
	Title string
	Body  string
	File  string
}

type ChannelSummary struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Total        int    `json:"total"`
	Open         int    `json:"open"`
	Live         int    `json:"live"`
	Attention    int    `json:"attention"`
	LastActivity string `json:"last_activity"`
}

type LiveItem struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Channel string `json:"channel"`
	Auto    bool   `json:"auto"`
	Since   string `json:"since"`
}

type ChannelList struct {
	Generated string           `json:"generated"`
	Channels  []ChannelSummary `json:"channels"`
	Live      []LiveItem       `json:"live"`
}

type Card struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	RunState     string `json:"run_state"`
	Priority     string `json:"priority"`
	Section      string `json:"section"`
	Reason       string `json:"reason,omitempty"`
	WaitingOn    string `json:"waiting_on,omitempty"`
	Auto         bool   `json:"auto"`
	AutoStatus   string `json:"auto_status,omitempty"`
	AutoStarted  string `json:"auto_started,omitempty"`
	AutoFinished string `json:"auto_finished,omitempty"`
	Latest       string `json:"latest"`
	Messages     int    `json:"messages"`
	LastActivity string `json:"last_activity"`
}

type Event struct {
	Time   string `json:"time"`
	Kind   string `json:"kind"` // created | auto_start | auto_end | note | message | broadcast
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Body   string `json:"body"`
	Title  string `json:"title,omitempty"`
	Urgent bool   `json:"urgent,omitempty"`
}

type Thread struct {
	Card
	Project string  `json:"project"`
	Brief   string  `json:"brief"`
	Events  []Event `json:"events"`
}

func parseT(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func fmtT(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// classify puts a task into one of the middle-column sections.
func classify(t chanTask, run string, last, now time.Time) (section, reason string) {
	switch t.Status {
	case "done":
		return "done", ""
	case "backlog":
		return "backlog", ""
	}
	switch {
	case run == "live":
		return "active", ""
	case t.WaitingOn != "":
		// an auto that stopped to ask a question isn't a crash
		return "attention", "waiting on " + t.WaitingOn
	case run == "crashed":
		return "attention", "auto died"
	case !last.IsZero() && now.Sub(last) > staleAfter:
		return "attention", fmt.Sprintf("stale %dd", int(now.Sub(last).Hours()/24))
	}
	return "active", ""
}

func channelOf(t chanTask) string {
	if t.Project == "" {
		return floatingSlug
	}
	return t.Project
}

// lastActivityMap: newest of updated_at, auto finish, and any bus message the
// task sent or received.
func lastActivityMap(tasks []chanTask, msgs []rawMessage) map[string]time.Time {
	out := make(map[string]time.Time, len(tasks))
	for _, t := range tasks {
		out[t.Slug] = later(parseT(t.UpdatedAt), parseT(t.AutoRunFinished))
	}
	for _, m := range msgs {
		ts := parseT(m.CreatedAt)
		for _, s := range []string{m.FromTaskSlug, m.ToTaskSlug} {
			if cur, ok := out[s]; ok && s != "" {
				out[s] = later(cur, ts)
			}
		}
	}
	return out
}

func buildChannelList(projects []project, tasks []chanTask, msgs []rawMessage, runs map[string]string, now time.Time) ChannelList {
	names := map[string]string{floatingSlug: "no project"}
	for _, p := range projects {
		names[p.Slug] = p.Name
	}
	last := lastActivityMap(tasks, msgs)
	byChan := map[string]*ChannelSummary{}
	lastByChan := map[string]time.Time{}
	var live []LiveItem

	for _, t := range tasks {
		ch := channelOf(t)
		cs, ok := byChan[ch]
		if !ok {
			name := names[ch]
			if name == "" {
				name = ch // task points at an archived/unknown project
			}
			cs = &ChannelSummary{Slug: ch, Name: name}
			byChan[ch] = cs
		}
		cs.Total++
		if t.Status != "done" {
			cs.Open++
		}
		run := runs[t.Slug]
		if sec, _ := classify(t, run, last[t.Slug], now); sec == "attention" {
			cs.Attention++
		}
		if run == "live" {
			cs.Live++
			since := t.AutoRunStarted
			if since == "" {
				since = t.UpdatedAt
			}
			live = append(live, LiveItem{Slug: t.Slug, Name: t.Name, Channel: ch, Auto: t.AutoRunStatus != "", Since: since})
		}
		lastByChan[ch] = later(lastByChan[ch], last[t.Slug])
	}

	out := ChannelList{Generated: fmtT(now), Live: live}
	for ch, cs := range byChan {
		cs.LastActivity = fmtT(lastByChan[ch])
		out.Channels = append(out.Channels, *cs)
	}
	sort.SliceStable(out.Channels, func(i, j int) bool {
		a, b := out.Channels[i], out.Channels[j]
		if (a.Slug == floatingSlug) != (b.Slug == floatingSlug) {
			return b.Slug == floatingSlug
		}
		return a.LastActivity > b.LastActivity
	})
	sort.SliceStable(out.Live, func(i, j int) bool { return out.Live[i].Since > out.Live[j].Since })
	return out
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "```", " ")
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > latestMax {
		s = string(r[:latestMax-1]) + "…"
	}
	return s
}

// buildCards renders thread cards for one channel's tasks, newest first.
// notes holds the latest update note per task (may be missing).
func buildCards(tasks []chanTask, msgs []rawMessage, notes map[string]note, runs map[string]string, now time.Time) []Card {
	idx := map[string]int{}
	cards := make([]Card, len(tasks))
	lastT := make([]time.Time, len(tasks))
	latestT := make([]time.Time, len(tasks))
	for i, t := range tasks {
		idx[t.Slug] = i
		cards[i] = Card{
			Slug: t.Slug, Name: t.Name, Status: t.Status, RunState: runs[t.Slug],
			Priority: t.Priority, WaitingOn: t.WaitingOn,
			Auto: t.AutoRunStatus != "", AutoStatus: t.AutoRunStatus,
			AutoStarted: t.AutoRunStarted, AutoFinished: t.AutoRunFinished,
		}
		lastT[i] = later(parseT(t.UpdatedAt), parseT(t.AutoRunFinished))
		if n, ok := notes[t.Slug]; ok {
			cards[i].Latest = oneLine(n.Title)
			latestT[i] = n.Time
			lastT[i] = later(lastT[i], n.Time)
		}
	}
	for _, m := range msgs {
		ts := parseT(m.CreatedAt)
		seen := map[int]bool{}
		for _, s := range []string{m.FromTaskSlug, m.ToTaskSlug} {
			i, ok := idx[s]
			if !ok || s == "" || seen[i] {
				continue
			}
			seen[i] = true
			cards[i].Messages++
			lastT[i] = later(lastT[i], ts)
			// bus text is only a fallback: the agent's own words / notes win
			if _, has := notes[s]; !has && !ts.Before(latestT[i]) {
				latestT[i] = ts
				cards[i].Latest = oneLine(m.Body)
			}
		}
	}
	for i, t := range tasks {
		cards[i].Section, cards[i].Reason = classify(t, runs[t.Slug], lastT[i], now)
		cards[i].LastActivity = fmtT(lastT[i])
	}
	sort.SliceStable(cards, func(i, j int) bool { return cards[i].LastActivity > cards[j].LastActivity })
	return cards
}

func isHuman(assignee string) bool { return assignee == "user" || assignee == "self" }

// buildThread merges a task's lifecycle, notes and bus traffic into one
// time-ordered stream.
func buildThread(t chanTask, brief string, notes []note, msgs []rawMessage, run string) Thread {
	var evs []Event
	add := func(ts time.Time, e Event) {
		if ts.IsZero() {
			return
		}
		e.Time = fmtT(ts)
		evs = append(evs, e)
	}
	add(parseT(t.CreatedAt), Event{Kind: "created", Body: "task created"})
	add(parseT(t.AutoRunStarted), Event{Kind: "auto_start", Body: "auto run started"})
	if t.AutoRunFinished != "" {
		add(parseT(t.AutoRunFinished), Event{Kind: "auto_end", Body: t.AutoRunStatus})
	}
	for _, n := range notes {
		add(n.Time, Event{Kind: "note", Title: n.Title, Body: n.Body})
	}
	var lastMsg time.Time
	count := 0
	for _, m := range msgs {
		if m.FromTaskSlug != t.Slug && m.ToTaskSlug != t.Slug {
			continue
		}
		count++
		from := m.FromTaskSlug
		if from == "" && isHuman(m.FromAssignee) {
			from = "you"
		}
		to := m.ToTaskSlug
		if to == "" && isHuman(m.ToAssignee) {
			to = "you"
		} else if to == "" {
			to = m.ToAssignee
		}
		ts := parseT(m.CreatedAt)
		lastMsg = later(lastMsg, ts)
		add(ts, Event{Kind: m.Kind, From: from, To: to, Body: m.Body, Urgent: m.Urgent})
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].Time < evs[j].Time })

	last := later(parseT(t.UpdatedAt), lastMsg)
	last = later(last, parseT(t.AutoRunFinished))
	if len(notes) > 0 {
		last = later(last, notes[len(notes)-1].Time)
	}
	sec, reason := classify(t, run, last, time.Now())
	th := Thread{
		Card: Card{
			Slug: t.Slug, Name: t.Name, Status: t.Status, RunState: run, Priority: t.Priority,
			Section: sec, Reason: reason, WaitingOn: t.WaitingOn,
			Auto: t.AutoRunStatus != "", AutoStatus: t.AutoRunStatus,
			AutoStarted: t.AutoRunStarted, AutoFinished: t.AutoRunFinished,
			Messages: count, LastActivity: fmtT(last),
		},
		Project: t.Project, Brief: brief, Events: evs,
	}
	return th
}
