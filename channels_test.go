package main

import (
	"testing"
	"time"
)

var chNow = mustTime("2026-10-27T13:30:00+05:30")

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestClassify_Sections(t *testing.T) {
	cases := []struct {
		name     string
		task     chanTask
		run      string
		last     string
		section  string
		reasonOK func(string) bool
	}{
		{"done", chanTask{Status: "done"}, "done", "2026-10-27T10:00:00+05:30", "done", nil},
		{"backlog", chanTask{Status: "backlog"}, "backlog", "2026-10-01T10:00:00+05:30", "backlog", nil},
		{"live auto is active", chanTask{Status: "in-progress", AutoRunStatus: "running"}, "live", "2026-10-27T13:20:00+05:30", "active", nil},
		{"crashed auto needs attention", chanTask{Status: "in-progress", AutoRunStatus: "dead"}, "crashed", "2026-10-15T16:16:00+05:30", "attention",
			func(r string) bool { return r == "auto died" }},
		{"waiting needs attention", chanTask{Status: "in-progress", WaitingOn: "Anuj sign-off"}, "idle", "2026-10-26T10:00:00+05:30", "attention",
			func(r string) bool { return r == "waiting on Anuj sign-off" }},
		{"stale idle needs attention", chanTask{Status: "in-progress"}, "idle", "2026-10-13T13:30:00+05:30", "attention",
			func(r string) bool { return r == "stale 14d" }},
		{"recent idle is active", chanTask{Status: "in-progress"}, "idle", "2026-10-25T13:30:00+05:30", "active", nil},
	}
	for _, c := range cases {
		sec, reason := classify(c.task, c.run, mustTime(c.last), chNow)
		if sec != c.section {
			t.Errorf("%s: section=%q want %q", c.name, sec, c.section)
		}
		if c.reasonOK != nil && !c.reasonOK(reason) {
			t.Errorf("%s: bad reason %q", c.name, reason)
		}
	}
}

func TestBuildChannelList_GroupsByProjectWithFloatingBucket(t *testing.T) {
	projects := []project{{Slug: "fampay", Name: "Fampay"}, {Slug: "empty", Name: "Empty"}}
	tasks := []chanTask{
		{Slug: "fampay-a", Project: "fampay", Status: "in-progress", AutoRunStatus: "running", UpdatedAt: "2026-10-27T13:22:00+05:30"},
		{Slug: "fampay-b", Project: "fampay", Status: "done", UpdatedAt: "2026-10-27T11:00:00+05:30"},
		{Slug: "fampay-c", Project: "fampay", Status: "in-progress", AutoRunStatus: "dead", UpdatedAt: "2026-10-08T12:00:00+05:30"},
		{Slug: "loose", Status: "backlog", UpdatedAt: "2026-10-20T09:00:00+05:30"},
	}
	runs := map[string]string{"fampay-a": "live", "fampay-b": "done", "fampay-c": "crashed", "loose": "backlog"}

	out := buildChannelList(projects, tasks, nil, runs, chNow)

	if len(out.Channels) != 2 {
		t.Fatalf("want fampay + floating (empty project hidden), got %+v", out.Channels)
	}
	f := out.Channels[0]
	if f.Slug != "fampay" || f.Total != 3 || f.Open != 2 || f.Live != 1 || f.Attention != 1 {
		t.Errorf("fampay counts wrong: %+v", f)
	}
	if f.LastActivity != "2026-10-27T13:22:00+05:30" {
		t.Errorf("last activity: %q", f.LastActivity)
	}
	if out.Channels[1].Slug != floatingSlug {
		t.Errorf("floating bucket should sort after active channel, got %q", out.Channels[1].Slug)
	}
	if len(out.Live) != 1 || out.Live[0].Slug != "fampay-a" || !out.Live[0].Auto {
		t.Errorf("live list: %+v", out.Live)
	}
}

func TestBuildChannelList_MessagesBumpLastActivity(t *testing.T) {
	projects := []project{{Slug: "p", Name: "P"}}
	tasks := []chanTask{{Slug: "t", Project: "p", Status: "in-progress", UpdatedAt: "2026-10-01T10:00:00+05:30"}}
	msgs := []rawMessage{{FromTaskSlug: "t", CreatedAt: "2026-10-27T12:00:00+05:30", Body: "hi"}}
	out := buildChannelList(projects, tasks, msgs, map[string]string{"t": "idle"}, chNow)
	if out.Channels[0].LastActivity != "2026-10-27T12:00:00+05:30" {
		t.Errorf("message should bump activity: %q", out.Channels[0].LastActivity)
	}
	if out.Channels[0].Attention != 0 {
		t.Errorf("recent message means not stale: %+v", out.Channels[0])
	}
}

func TestBuildCards_LatestLineAndCounts(t *testing.T) {
	tasks := []chanTask{
		{Slug: "a", Name: "Task A", Status: "done", UpdatedAt: "2026-10-27T13:13:00+05:30"},
		{Slug: "b", Name: "Task B", Status: "in-progress", UpdatedAt: "2026-10-27T13:14:00+05:30", AutoRunStatus: "running"},
	}
	msgs := []rawMessage{
		{FromTaskSlug: "a", ToTaskSlug: "x", Kind: "broadcast", CreatedAt: "2026-10-27T12:48:00+05:30", Body: "phase1"},
		{FromTaskSlug: "a", Kind: "message", CreatedAt: "2026-10-27T13:12:00+05:30", Body: "DONE.\n31/44 groups"},
		{FromTaskSlug: "zz", ToTaskSlug: "a", Kind: "message", CreatedAt: "2026-10-27T12:00:00+05:30", Body: "inbound"},
	}
	notes := map[string]note{"b": {Time: mustTime("2026-10-27T13:20:00+05:30"), Title: "Scanning prod keys"}}
	runs := map[string]string{"a": "done", "b": "live"}

	cards := buildCards(tasks, msgs, notes, runs, chNow)
	if len(cards) != 2 || cards[0].Slug != "b" {
		t.Fatalf("cards should sort newest activity first: %+v", cards)
	}
	if cards[0].Latest != "Scanning prod keys" || cards[0].Section != "active" || !cards[0].Auto {
		t.Errorf("card b: %+v", cards[0])
	}
	a := cards[1]
	if a.Messages != 3 || a.Latest != "DONE. 31/44 groups" {
		t.Errorf("card a: msgs=%d latest=%q", a.Messages, a.Latest)
	}
}

func TestBuildThread_MergesEventsInTimeOrder(t *testing.T) {
	task := chanTask{
		Slug: "a", Name: "Task A", Status: "done",
		CreatedAt:       "2026-10-27T12:30:00+05:30",
		AutoRunStarted:  "2026-10-27T12:38:00+05:30",
		AutoRunFinished: "2026-10-27T13:13:00+05:30",
		AutoRunStatus:   "completed",
	}
	msgs := []rawMessage{
		{FromTaskSlug: "a", FromAssignee: "user", ToAssignee: "user", Kind: "message", CreatedAt: "2026-10-27T13:12:00+05:30", Body: "done"},
		{FromTaskSlug: "a", FromAssignee: "user", ToAssignee: "user", ToTaskSlug: "fampay-modelling", Kind: "broadcast", CreatedAt: "2026-10-27T12:48:00+05:30", Body: "phase1"},
		{FromTaskSlug: "other", Kind: "message", CreatedAt: "2026-10-27T12:50:00+05:30", Body: "not mine"},
	}
	notes := []note{{Time: mustTime("2026-10-27T13:13:30+05:30"), Title: "Consolidation complete", Body: "details", File: "x.md"}}

	th := buildThread(task, "# Brief\nbody", notes, msgs, "done")

	want := []string{"created", "auto_start", "broadcast", "message", "auto_end", "note"}
	if len(th.Events) != len(want) {
		t.Fatalf("events: %+v", th.Events)
	}
	for i, k := range want {
		if th.Events[i].Kind != k {
			t.Errorf("event %d kind=%q want %q", i, th.Events[i].Kind, k)
		}
	}
	if th.Events[3].To != "you" {
		t.Errorf("message to the human should read 'you', got %q", th.Events[3].To)
	}
	if th.Events[4].Body != "completed" {
		t.Errorf("auto_end should carry final status: %q", th.Events[4].Body)
	}
	if th.Brief != "# Brief\nbody" {
		t.Errorf("brief not carried")
	}
}

func TestBuildCards_AgentWordsBeatBusMessages(t *testing.T) {
	tasks := []chanTask{{Slug: "tr", Name: "Tracker", Status: "in-progress", UpdatedAt: "2026-10-27T10:00:00+05:30"}}
	msgs := []rawMessage{{FromTaskSlug: "other", ToTaskSlug: "tr", Kind: "broadcast", CreatedAt: "2026-10-27T13:00:00+05:30", Body: "someone else's news"}}
	agent := map[string]note{"tr": {Time: mustTime("2026-10-27T12:00:00+05:30"), Title: "Sheet reconciled."}}
	cards := buildCards(tasks, msgs, agent, map[string]string{"tr": "idle"}, chNow)
	if cards[0].Latest != "Sheet reconciled." {
		t.Errorf("agent's own words should win over a newer received broadcast, got %q", cards[0].Latest)
	}
	if cards[0].Messages != 1 || cards[0].LastActivity != "2026-10-27T13:00:00+05:30" {
		t.Errorf("message should still count and bump activity: %+v", cards[0])
	}
}

func TestOneLine_StripsCodeFences(t *testing.T) {
	if got := oneLine("```\n🔄 job ALIVE\n```\nAll good"); got != "🔄 job ALIVE All good" {
		t.Errorf("got %q", got)
	}
}
