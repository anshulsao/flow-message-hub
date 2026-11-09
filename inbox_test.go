package main

import "testing"

func threadFor(v InboxView, task string) *InboxThread {
	for i := range v.Threads {
		if v.Threads[i].Task == task {
			return &v.Threads[i]
		}
	}
	return nil
}

// An agent message to the human with no reply yet, still pending, is an
// UNREAD inbox thread and exposes the pending id so "open = seen" can ack it.
func TestBuildInbox_UnreadPending(t *testing.T) {
	msgs := []rawMessage{{
		ID: "in1", Kind: "message", CreatedAt: "2026-09-03T10:00:00+05:30",
		FromAssignee: "self", FromTaskSlug: "deploy-bot",
		ToAssignee: "self", ToTaskSlug: "", // to the human
		Body: "prod go/no-go?", Status: "pending", Urgent: true,
	}}
	tasks := map[string]taskMeta{"deploy-bot": {Name: "Prod deploy bot", Status: "in-progress"}}
	v := buildInbox(msgs, tasks)

	th := threadFor(v, "deploy-bot")
	if th == nil {
		t.Fatal("deploy-bot thread missing")
	}
	if th.State != "unread" {
		t.Fatalf("want unread, got %q", th.State)
	}
	if th.Name != "Prod deploy bot" || th.Repliable != true || th.Urgent != true {
		t.Fatalf("thread meta wrong: %+v", th)
	}
	if len(th.PendingIDs) != 1 || th.PendingIDs[0] != "in1" {
		t.Fatalf("pending ids wrong: %+v", th.PendingIDs)
	}
	if v.Unread != 1 || v.Owe != 0 {
		t.Fatalf("counts wrong: unread=%d owe=%d", v.Unread, v.Owe)
	}
}

// Seen (acked) but no reply after it => owe-reply, no pending ids, not unread.
func TestBuildInbox_SeenOwesReply(t *testing.T) {
	msgs := []rawMessage{{
		ID: "in1", Kind: "message", CreatedAt: "2026-09-03T10:00:00+05:30",
		FromAssignee: "self", FromTaskSlug: "deploy-bot",
		ToAssignee: "self", ToTaskSlug: "",
		Body: "go/no-go?", Status: "acked",
	}}
	v := buildInbox(msgs, map[string]taskMeta{})
	th := threadFor(v, "deploy-bot")
	if th == nil || th.State != "owe-reply" {
		t.Fatalf("want owe-reply, got %+v", th)
	}
	if len(th.PendingIDs) != 0 {
		t.Fatalf("acked msg should not be pending: %+v", th.PendingIDs)
	}
	if v.Unread != 0 || v.Owe != 1 {
		t.Fatalf("counts wrong: unread=%d owe=%d", v.Unread, v.Owe)
	}
}

// A human reply (self -> task, no from_task) AFTER the inbound => answered.
func TestBuildInbox_RepliedIsAnswered(t *testing.T) {
	msgs := []rawMessage{
		{ID: "in1", Kind: "message", CreatedAt: "2026-09-03T10:00:00+05:30",
			FromAssignee: "self", FromTaskSlug: "deploy-bot", ToAssignee: "self", ToTaskSlug: "",
			Body: "go/no-go?", Status: "acked"},
		{ID: "out1", Kind: "message", CreatedAt: "2026-09-03T10:05:00+05:30",
			FromAssignee: "self", FromTaskSlug: "", ToAssignee: "self", ToTaskSlug: "deploy-bot",
			Body: "go", Status: "delivered"},
	}
	v := buildInbox(msgs, map[string]taskMeta{})
	th := threadFor(v, "deploy-bot")
	if th == nil || th.State != "answered" {
		t.Fatalf("want answered, got %+v", th)
	}
	if len(th.Messages) != 2 || th.Messages[0].Dir != "in" || th.Messages[1].Dir != "out" {
		t.Fatalf("thread history wrong: %+v", th.Messages)
	}
	if v.Owe != 0 || v.Unread != 0 {
		t.Fatalf("counts wrong: %+v", v)
	}
}

// A newer pending inbound after a previous reply flips the thread back to unread.
func TestBuildInbox_NewInboundAfterReplyIsUnread(t *testing.T) {
	msgs := []rawMessage{
		{ID: "in1", Kind: "message", CreatedAt: "2026-09-03T10:00:00+05:30",
			FromAssignee: "self", FromTaskSlug: "deploy-bot", ToAssignee: "self", Body: "a", Status: "acked"},
		{ID: "out1", Kind: "message", CreatedAt: "2026-09-03T10:05:00+05:30",
			FromAssignee: "self", ToAssignee: "self", ToTaskSlug: "deploy-bot", Body: "ok", Status: "delivered"},
		{ID: "in2", Kind: "message", CreatedAt: "2026-09-03T10:10:00+05:30",
			FromAssignee: "self", FromTaskSlug: "deploy-bot", ToAssignee: "self", Body: "b", Status: "pending"},
	}
	v := buildInbox(msgs, map[string]taskMeta{})
	th := threadFor(v, "deploy-bot")
	if th == nil || th.State != "unread" {
		t.Fatalf("want unread after new inbound, got %+v", th)
	}
	if len(th.PendingIDs) != 1 || th.PendingIDs[0] != "in2" {
		t.Fatalf("pending ids wrong: %+v", th.PendingIDs)
	}
}

// Message to the human from an unbound human context (no from_task) is the
// non-repliable "general" thread.
func TestBuildInbox_GeneralThreadNotRepliable(t *testing.T) {
	msgs := []rawMessage{{
		ID: "g1", Kind: "message", CreatedAt: "2026-09-03T10:00:00+05:30",
		FromAssignee: "self", FromTaskSlug: "", ToAssignee: "self", ToTaskSlug: "",
		Body: "note to self", Status: "pending",
	}}
	v := buildInbox(msgs, map[string]taskMeta{})
	th := threadFor(v, "~")
	if th == nil || th.Repliable {
		t.Fatalf("general thread should exist and be non-repliable: %+v", th)
	}
}

// An agent whose task is done can't receive a reply (its session won't pop),
// so the thread is not repliable and says why.
func TestBuildInbox_DoneAgentNotRepliable(t *testing.T) {
	msgs := []rawMessage{{
		ID: "in1", Kind: "message", CreatedAt: "2026-09-03T10:00:00+05:30",
		FromAssignee: "self", FromTaskSlug: "smoke", ToAssignee: "self",
		Body: "done?", Status: "acked",
	}}
	tasks := map[string]taskMeta{"smoke": {Name: "Smoke", Status: "done"}}
	v := buildInbox(msgs, tasks)
	th := threadFor(v, "smoke")
	if th == nil || th.Repliable {
		t.Fatalf("done agent must not be repliable: %+v", th)
	}
	if th.LockReason == "" {
		t.Fatalf("want a lock reason explaining why")
	}
	// seen-but-unanswered on a dead agent is "closed", not an owe-reply todo,
	// and must not count toward the owe badge (you can't reply anyway).
	if th.State != "closed" {
		t.Fatalf("dead-agent seen thread should be closed, got %q", th.State)
	}
	if v.Owe != 0 {
		t.Fatalf("closed threads must not count as owe: owe=%d", v.Owe)
	}
}

// Peer-to-peer traffic (session -> session) is NOT part of the human inbox.
func TestBuildInbox_PeerTrafficExcluded(t *testing.T) {
	msgs := []rawMessage{{
		ID: "p1", Kind: "message", CreatedAt: "2026-09-03T10:00:00+05:30",
		FromAssignee: "self", FromTaskSlug: "agent-beta",
		ToAssignee: "self", ToTaskSlug: "demo-apps",
		Body: "peer", Status: "delivered",
	}}
	v := buildInbox(msgs, map[string]taskMeta{})
	if len(v.Threads) != 0 {
		t.Fatalf("peer traffic must not create inbox threads: %+v", v.Threads)
	}
}

// ---- mail-model columns (flow alpha.33+): seen_at / answered_at / options ----

// With the new columns, unread = seen_at IS NULL, regardless of status
// (human mail stays status=pending until answered).
func TestBuildInbox_UnreadIsSeenAtNull(t *testing.T) {
	msgs := []rawMessage{{
		ID: "in1", Kind: "message", CreatedAt: "2026-11-06T10:00:00+05:30",
		FromAssignee: "user", FromTaskSlug: "deploy", ToAssignee: "user",
		Body: "ship?", Status: "pending", HasMail: true, SeenAt: "2026-11-06T10:01:00+05:30",
		Options: []string{"Yes", "No"},
	}}
	v := buildInbox(msgs, map[string]taskMeta{"deploy": {Status: "in-progress"}})
	th := threadFor(v, "deploy")
	if th == nil || th.State != "owe-reply" || len(th.PendingIDs) != 0 || v.Unread != 0 {
		t.Fatalf("seen pending ask should owe a reply, not be unread: %+v", th)
	}
	if th.AskID != "in1" {
		t.Fatalf("ask id = %q", th.AskID)
	}
	m := th.Messages[0]
	if !m.Seen || m.Answered || len(m.Options) != 2 || m.Options[0] != "Yes" {
		t.Fatalf("msg fields: %+v", m)
	}

	msgs[0].SeenAt = ""
	v = buildInbox(msgs, map[string]taskMeta{})
	th = threadFor(v, "deploy")
	if th.State != "unread" || len(th.PendingIDs) != 1 || th.PendingIDs[0] != "in1" {
		t.Fatalf("unseen should be unread: %+v", th)
	}
}

// answered_at set on the newest ask => answered, and there's no open ask.
func TestBuildInbox_AnsweredAt(t *testing.T) {
	msgs := []rawMessage{
		{ID: "a1", Kind: "message", CreatedAt: "2026-11-06T10:00:00+05:30", FromAssignee: "user",
			FromTaskSlug: "deploy", ToAssignee: "user", Body: "q1", Status: "acked", HasMail: true,
			SeenAt: "x", AnsweredAt: "2026-11-06T10:02:00+05:30"},
	}
	v := buildInbox(msgs, map[string]taskMeta{})
	th := threadFor(v, "deploy")
	if th.State != "answered" || th.AskID != "" || !th.Messages[0].Answered {
		t.Fatalf("answered ask: %+v", th)
	}
}

// AskID is the NEWEST unanswered, non-fyi inbound ask.
func TestBuildInbox_AskIDNewestUnanswered(t *testing.T) {
	mk := func(id, at, ans string, fyi bool) rawMessage {
		return rawMessage{ID: id, Kind: "message", CreatedAt: at, FromAssignee: "user", FromTaskSlug: "d",
			ToAssignee: "user", Body: id, Status: "pending", HasMail: true, SeenAt: "x", AnsweredAt: ans, FYI: fyi}
	}
	msgs := []rawMessage{
		mk("old", "2026-11-06T10:00:00+05:30", "", false),
		mk("mid", "2026-11-06T10:01:00+05:30", "", false),
		mk("fyi", "2026-11-06T10:02:00+05:30", "", true),
		mk("done", "2026-11-06T10:03:00+05:30", "y", false),
	}
	th := threadFor(buildInbox(msgs, map[string]taskMeta{}), "d")
	if th.AskID != "mid" {
		t.Fatalf("ask id = %q, want mid", th.AskID)
	}
	if !th.Messages[2].FYI {
		t.Fatal("fyi flag lost")
	}
}

// Old schema (no seen_at column): fall back to status=='pending' for unread.
func TestStore_OldSchemaTolerated(t *testing.T) {
	path := seedDBWith(t, `INSERT INTO bus_messages VALUES ('o1','2026-11-06T10:00:00+05:30','message','self','a','self',NULL,'hi',0,'pending',NULL)`)
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := st.messages()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("msgs=%v err=%v", msgs, err)
	}
	if msgs[0].HasMail {
		t.Fatal("old schema must not claim mail columns")
	}
	th := threadFor(buildInbox(msgs, map[string]taskMeta{}), "a")
	if th == nil || th.State != "unread" {
		t.Fatalf("old schema pending should be unread: %+v", th)
	}
}

func TestStore_ReadsMailColumns(t *testing.T) {
	path := seedDBWith(t,
		`ALTER TABLE bus_messages ADD COLUMN seen_at TEXT`,
		`ALTER TABLE bus_messages ADD COLUMN answered_at TEXT`,
		`ALTER TABLE bus_messages ADD COLUMN options TEXT`,
		`ALTER TABLE bus_messages ADD COLUMN reply_to TEXT`,
		`ALTER TABLE bus_messages ADD COLUMN fyi INTEGER NOT NULL DEFAULT 0`,
		`INSERT INTO bus_messages (id,created_at,kind,from_assignee,from_task_slug,to_assignee,to_task_slug,body,urgent,status,seen_at,answered_at,options,reply_to,fyi)
		 VALUES ('n1','2026-11-06T10:00:00+05:30','message','user','a','user',NULL,'ship?',0,'pending','s1',NULL,'["Yes","No"]',NULL,0),
		        ('n2','2026-11-06T10:05:00+05:30','message','user',NULL,'user','a','Yes',0,'pending',NULL,NULL,NULL,'n1',1)`)
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := st.messages()
	if err != nil || len(msgs) != 2 {
		t.Fatalf("msgs=%v err=%v", msgs, err)
	}
	m := msgs[0]
	if !m.HasMail || m.SeenAt != "s1" || m.AnsweredAt != "" || len(m.Options) != 2 || m.Options[1] != "No" {
		t.Fatalf("n1 = %+v", m)
	}
	if msgs[1].ReplyTo != "n1" || !msgs[1].FYI {
		t.Fatalf("n2 = %+v", msgs[1])
	}
}
