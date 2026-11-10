package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- fixtures: the peer-hubs v0 row shapes ----

// inbound ask from peer hub "rohit" to the human (origin_ref = rohit's id)
func peerAsk(id, at string) rawMessage {
	return rawMessage{
		ID: id, Kind: "message", CreatedAt: at,
		FromAssignee: "rohit", ToAssignee: "user",
		Body: "ship the release?", Options: []string{"Yes", "No"},
		HasMail: true, Peer: "rohit", OriginRef: "r-" + id,
	}
}

// human's answer routed out to rohit (outbound peer row, from the bare human)
func peerReplyOut(id, at, replyTo, delivery, derr string) rawMessage {
	return rawMessage{
		ID: id, Kind: "message", CreatedAt: at,
		FromAssignee: "user", ToAssignee: "rohit", Body: "Yes", ReplyTo: replyTo,
		HasMail: true, Peer: "rohit", Delivery: delivery, DeliveryError: derr,
	}
}

// ---- Inbox ----

func TestBuildInbox_PeerMailGetsItsOwnRepliableThread(t *testing.T) {
	msgs := []rawMessage{peerAsk("p1", "2026-11-10T10:00:00+05:30")}
	v := buildInbox(msgs, map[string]taskMeta{})

	if threadFor(v, "~") != nil {
		t.Fatal("peer mail must not land in ~ general")
	}
	th := threadFor(v, "peer:rohit")
	if th == nil {
		t.Fatalf("peer:rohit thread missing: %+v", v.Threads)
	}
	if th.Kind != "peer" || th.Peer != "rohit" || !th.Repliable {
		t.Fatalf("peer thread meta wrong: %+v", th)
	}
	if !strings.Contains(th.Name, "rohit") || !strings.Contains(th.Name, "remote hub") {
		t.Fatalf("peer thread should be labelled as a remote hub: %q", th.Name)
	}
	if th.AskID != "p1" || th.ReplyID != "p1" || th.State != "unread" {
		t.Fatalf("ask/reply/state wrong: %+v", th)
	}
	if len(th.Messages) != 1 || len(th.Messages[0].Options) != 2 || th.Messages[0].Peer != "rohit" {
		t.Fatalf("options/peer not carried: %+v", th.Messages)
	}
}

func TestBuildInbox_PeerReplyShowsOutboundWithDelivery(t *testing.T) {
	ask := peerAsk("p1", "2026-11-10T10:00:00+05:30")
	ask.SeenAt, ask.AnsweredAt = "2026-11-10T10:01:00+05:30", "2026-11-10T10:01:00+05:30"
	msgs := []rawMessage{ask, peerReplyOut("o1", "2026-11-10T10:01:00+05:30", "p1", "failed", "connector exit 1: no route")}
	v := buildInbox(msgs, map[string]taskMeta{})
	th := threadFor(v, "peer:rohit")
	if th == nil || len(th.Messages) != 2 {
		t.Fatalf("want ask + reply in the peer thread: %+v", th)
	}
	out := th.Messages[1]
	if out.Dir != "out" || out.Delivery != "failed" || out.DeliveryError != "connector exit 1: no route" || out.Peer != "rohit" {
		t.Fatalf("outbound peer reply wrong: %+v", out)
	}
	if th.State != "answered" || th.AskID != "" {
		t.Fatalf("answered peer ask: state=%q ask=%q", th.State, th.AskID)
	}
}

// With only an FYI (nothing to answer) the reply still threads to the newest inbound.
func TestBuildInbox_PeerReplyTargetsNewestInboundWhenNoOpenAsk(t *testing.T) {
	a := peerAsk("p1", "2026-11-10T10:00:00+05:30")
	a.FYI, a.Options = true, nil
	b := peerAsk("p2", "2026-11-10T10:05:00+05:30")
	b.FYI, b.Options = true, nil
	v := buildInbox([]rawMessage{a, b}, nil)
	th := threadFor(v, "peer:rohit")
	if th == nil || th.AskID != "" || th.ReplyID != "p2" {
		t.Fatalf("want reply target p2 with no open ask: %+v", th)
	}
}

func TestBuildInbox_ConnectorNoticeIsItsOwnLockedThread(t *testing.T) {
	msgs := []rawMessage{{
		ID: "c1", Kind: "message", CreatedAt: "2026-11-10T09:00:00+05:30",
		FromAssignee: "connector:flow-p2p", ToAssignee: "user",
		Body: "alice wants to pair", HasMail: true, FYI: true,
	}}
	v := buildInbox(msgs, nil)
	if threadFor(v, "~") != nil {
		t.Fatal("connector notice must not land in ~ general")
	}
	th := threadFor(v, "connector:flow-p2p")
	if th == nil || th.Kind != "connector" || th.Repliable || th.LockReason == "" {
		t.Fatalf("connector thread wrong: %+v", th)
	}
	if !strings.Contains(th.Name, "connector flow-p2p") {
		t.Fatalf("connector thread name: %q", th.Name)
	}
}

// A peer's answer to one of our tasks goes to that task, not the human inbox.
func TestBuildInbox_PeerAnswerToTaskStaysOutOfInbox(t *testing.T) {
	m := peerAsk("p9", "2026-11-10T10:00:00+05:30")
	m.ToTaskSlug, m.ReplyTo, m.Options = "deploy-bot", "o0", nil
	v := buildInbox([]rawMessage{m}, map[string]taskMeta{"deploy-bot": {Name: "D", Status: "in-progress"}})
	if len(v.Threads) != 0 {
		t.Fatalf("peer→task answer leaked into inbox: %+v", v.Threads)
	}
}

// A task's own outbound peer message isn't the human's DM either.
func TestBuildInbox_TaskToPeerStaysOutOfInbox(t *testing.T) {
	m := peerReplyOut("o1", "2026-11-10T10:00:00+05:30", "", "sent", "")
	m.FromTaskSlug = "deploy-bot"
	v := buildInbox([]rawMessage{m}, nil)
	if len(v.Threads) != 0 {
		t.Fatalf("task→peer leaked into inbox: %+v", v.Threads)
	}
}

// ---- reply routing ----

// peerFake is fakeWriter (which records PeerReply as "peer <name> <id> <text>").
type peerFake struct {
	fakeWriter
}

func peerThread() *InboxThread {
	return &InboxThread{Task: "peer:rohit", Kind: "peer", Peer: "rohit", Repliable: true, AskID: "p1", ReplyID: "p1"}
}

// With the flowhub connector, `flow human reply` answers the peer ask: flow
// marks it answered and routes the answer back over the peer's connector.
func TestThreadReply_PeerViaConnectorUsesHumanReply(t *testing.T) {
	f := &peerFake{}
	via, err := threadReply(f, "tok", peerThread(), "peer:rohit", "Yes")
	if err != nil || via != "connector" || len(f.calls) != 1 || f.calls[0] != "hreply tok p1 Yes" {
		t.Fatalf("via=%q err=%v calls=%v", via, err, f.calls)
	}
}

func TestThreadReply_PeerWithoutConnectorUsesFlowMessagePeer(t *testing.T) {
	f := &peerFake{}
	via, err := threadReply(f, "", peerThread(), "peer:rohit", "Yes")
	if err != nil || via != "peer" || len(f.calls) != 1 || f.calls[0] != "peer rohit p1 Yes" {
		t.Fatalf("via=%q err=%v calls=%v", via, err, f.calls)
	}
}

func TestThreadReply_PeerConnectorRejectedFallsBackToPeerMessage(t *testing.T) {
	f := &peerFake{fakeWriter: fakeWriter{replyErr: errNotConnector}}
	via, err := threadReply(f, "bad", peerThread(), "peer:rohit", "Yes")
	if err != nil || via != "peer" || f.calls[len(f.calls)-1] != "peer rohit p1 Yes" {
		t.Fatalf("via=%q err=%v calls=%v", via, err, f.calls)
	}
}

// No open ask (only FYIs): the reply threads to the newest inbound via flow message.
func TestThreadReply_PeerNoOpenAskRepliesToNewestInbound(t *testing.T) {
	f := &peerFake{}
	th := peerThread()
	th.AskID, th.ReplyID = "", "p2"
	via, err := threadReply(f, "tok", th, "peer:rohit", "ok")
	if err != nil || via != "peer" || f.calls[0] != "peer rohit p2 ok" {
		t.Fatalf("via=%q err=%v calls=%v", via, err, f.calls)
	}
}

// A peer/connector key with no matching thread must never fall through to
// `flow message user/peer:rohit`.
func TestThreadReply_UnknownPeerOrConnectorKeyRefused(t *testing.T) {
	for _, key := range []string{"peer:rohit", "connector:flow-p2p"} {
		f := &peerFake{}
		if _, err := threadReply(f, "", nil, key, "hi"); err == nil || len(f.calls) != 0 {
			t.Fatalf("%s: want refusal, err=%v calls=%v", key, err, f.calls)
		}
	}
}

func TestThreadReply_ConnectorThreadRefused(t *testing.T) {
	f := &peerFake{}
	th := &InboxThread{Task: "connector:flow-p2p", Kind: "connector"}
	if _, err := threadReply(f, "tok", th, th.Task, "hi"); err == nil || len(f.calls) != 0 {
		t.Fatalf("connector notices are not repliable: err=%v calls=%v", err, f.calls)
	}
}

// The exact argv the hub hands flow for a peer reply.
func TestFlowExec_PeerReplyArgv(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "flow")
	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"[$a]\" >>" + log + "; done\necho \"SESS:$CLAUDE_CODE_SESSION_ID\" >>" + log + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "leaky")
	f := &flowExec{bin: bin}
	if err := f.PeerReply("rohit", "p1", "Yes — ship it"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	want := "[message]\n[rohit]\n[--reply-to]\n[p1]\n[--]\n[Yes — ship it]\nSESS:\n"
	if string(b) != want {
		t.Fatalf("argv:\n%s\nwant:\n%s", b, want)
	}
	for _, bad := range [][3]string{{"", "p1", "x"}, {"rohit", "", "x"}, {"rohit", "p1", " "}, {"user", "p1", "x"}, {"a/b", "p1", "x"}} {
		if err := f.PeerReply(bad[0], bad[1], bad[2]); err == nil {
			t.Fatalf("PeerReply%v should be refused", bad)
		}
	}
}

// ---- Graph ----

func TestBuildHub_PeerIsRemoteHubNodeWithDeliveryEdges(t *testing.T) {
	out := peerReplyOut("o1", "2026-11-10T10:00:00+05:30", "", "failed", "boom")
	out.FromTaskSlug = "deploy-bot"
	in := peerAsk("p1", "2026-11-10T10:05:00+05:30")
	notice := rawMessage{ID: "c1", Kind: "message", FromAssignee: "connector:flow-p2p", ToAssignee: "user", Body: "pair?"}
	h := buildHub([]rawMessage{out, in, notice}, nil, nil,
		map[string]taskMeta{"deploy-bot": {Name: "D", Status: "in-progress"}}, "now")

	p := findNode(h, "peer:rohit")
	if p == nil || p.Kind != "peer" || p.Label != "rohit" {
		t.Fatalf("peer node wrong: %+v (nodes %+v)", p, h.Nodes)
	}
	if findNode(h, "rohit") != nil {
		t.Fatal("peer must not also appear as a plain assignee node")
	}
	if c := findNode(h, "connector:flow-p2p"); c == nil || c.Kind != "connector" {
		t.Fatalf("connector node wrong: %+v", c)
	}
	var eOut, eIn *Edge
	for i := range h.Messages {
		switch h.Messages[i].ID {
		case "o1":
			eOut = &h.Messages[i]
		case "p1":
			eIn = &h.Messages[i]
		}
	}
	if eOut == nil || eOut.From != "deploy-bot" || eOut.To != "peer:rohit" || eOut.Peer != "rohit" ||
		eOut.Delivery != "failed" || eOut.DeliveryError != "boom" {
		t.Fatalf("outbound peer edge wrong: %+v", eOut)
	}
	if eIn == nil || eIn.From != "peer:rohit" || eIn.To != "user" || eIn.Peer != "rohit" {
		t.Fatalf("inbound peer edge wrong: %+v", eIn)
	}
}

// Peers known only from the peers table still get a node.
func TestBuildHubPeers_AddsQuietPeers(t *testing.T) {
	h := buildHub(nil, nil, nil, nil, "now")
	addPeerNodes(&h, []Peer{{Name: "alice", Connector: "flow-p2p"}})
	if n := findNode(h, "peer:alice"); n == nil || n.Kind != "peer" || !strings.Contains(n.Name, "flow-p2p") {
		t.Fatalf("quiet peer node: %+v", n)
	}
	addPeerNodes(&h, []Peer{{Name: "alice", Connector: "flow-p2p"}})
	c := 0
	for _, n := range h.Nodes {
		if n.ID == "peer:alice" {
			c++
		}
	}
	if c != 1 {
		t.Fatalf("peer node duplicated: %d", c)
	}
}

// ---- Channels ----

func TestBuildThread_PeerMessagesLabelled(t *testing.T) {
	task := chanTask{Slug: "deploy-bot", Name: "D", CreatedAt: "2026-11-10T09:00:00+05:30"}
	out := peerReplyOut("o1", "2026-11-10T10:00:00+05:30", "", "dispatched", "")
	out.FromTaskSlug = "deploy-bot"
	ans := peerAsk("p1", "2026-11-10T10:05:00+05:30")
	ans.ToTaskSlug, ans.ReplyTo = "deploy-bot", "o1"
	th := buildThread(task, "", nil, []rawMessage{out, ans}, "live")

	var eo, ei *Event
	for i := range th.Events {
		if th.Events[i].Kind == "message" && th.Events[i].To == "rohit" {
			eo = &th.Events[i]
		}
		if th.Events[i].Kind == "message" && th.Events[i].From == "rohit" {
			ei = &th.Events[i]
		}
	}
	if eo == nil || eo.Peer != "rohit" || eo.Delivery != "dispatched" {
		t.Fatalf("outbound peer event: %+v (events %+v)", eo, th.Events)
	}
	if ei == nil || ei.Peer != "rohit" || ei.To != "deploy-bot" {
		t.Fatalf("inbound peer answer event: %+v", ei)
	}
}

// ---- store ----

func TestStore_ReadsPeerColumnsAndPeers(t *testing.T) {
	path := seedDBWith(t,
		`ALTER TABLE bus_messages ADD COLUMN peer TEXT`,
		`ALTER TABLE bus_messages ADD COLUMN origin_ref TEXT`,
		`ALTER TABLE bus_messages ADD COLUMN delivery TEXT`,
		`ALTER TABLE bus_messages ADD COLUMN delivery_error TEXT`,
		`INSERT INTO bus_messages (id,created_at,kind,from_assignee,from_task_slug,to_assignee,to_task_slug,body,urgent,status,peer,delivery,delivery_error)
		 VALUES ('o1','2026-11-10T10:00:00+05:30','message','user','a','rohit',NULL,'hi',0,'pending','rohit','failed','boom')`,
		`INSERT INTO bus_messages (id,created_at,kind,from_assignee,from_task_slug,to_assignee,to_task_slug,body,urgent,status,peer,origin_ref)
		 VALUES ('p1','2026-11-10T10:01:00+05:30','message','rohit',NULL,'user',NULL,'yo',0,'pending','rohit','r-9')`,
		`CREATE TABLE peers (name TEXT PRIMARY KEY, connector TEXT, at TEXT, blocked INTEGER, created_at TEXT, last_seen_at TEXT)`,
		`INSERT INTO peers VALUES ('rohit','flow-p2p','rohit@x',0,'2026-11-01T00:00:00Z','2026-11-10T10:01:00+05:30'),
		                          ('eve','flow-p2p','eve@x',1,'2026-11-02T00:00:00Z',NULL)`,
	)
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := st.messages()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]rawMessage{}
	for _, m := range msgs {
		by[m.ID] = m
	}
	if m := by["o1"]; m.Peer != "rohit" || m.Delivery != "failed" || m.DeliveryError != "boom" || m.OriginRef != "" {
		t.Fatalf("outbound peer cols: %+v", m)
	}
	if m := by["p1"]; m.Peer != "rohit" || m.OriginRef != "r-9" {
		t.Fatalf("inbound peer cols: %+v", m)
	}
	peers, err := st.peers()
	if err != nil || len(peers) != 2 {
		t.Fatalf("peers=%+v err=%v", peers, err)
	}
	if peers[0].Name != "eve" || !peers[0].Blocked || peers[1].Name != "rohit" || peers[1].LastSeenAt == "" || peers[1].Connector != "flow-p2p" {
		t.Fatalf("peers wrong: %+v", peers)
	}
}

// An older flow.db (no peer columns, no peers table) still reads cleanly.
func TestStore_OldSchemaHasNoPeers(t *testing.T) {
	st, err := openStore(seedDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.messages(); err != nil {
		t.Fatal(err)
	}
	peers, err := st.peers()
	if err != nil || len(peers) != 0 {
		t.Fatalf("want no peers on old schema, got %+v err=%v", peers, err)
	}
}

// An agent answering the peer's mail to the human (`flow message rohit
// --reply-to <id>` from a task) belongs in the peer thread too, attributed to it.
func TestBuildInbox_TaskReplyToPeerMailJoinsPeerThread(t *testing.T) {
	ask := peerAsk("p1", "2026-11-10T10:00:00+05:30")
	ask.SeenAt = "2026-11-10T10:00:30+05:30"
	r := peerReplyOut("o1", "2026-11-10T10:01:00+05:30", "p1", "sent", "")
	r.FromTaskSlug = "flow-enterprise"
	v := buildInbox([]rawMessage{ask, r}, nil)
	th := threadFor(v, "peer:rohit")
	if th == nil || len(th.Messages) != 2 {
		t.Fatalf("want ask + agent reply: %+v", th)
	}
	if m := th.Messages[1]; m.Dir != "out" || m.From != "flow-enterprise" || m.Delivery != "sent" {
		t.Fatalf("agent reply wrong: %+v", m)
	}
	if th.State != "answered" {
		t.Fatalf("state=%q", th.State)
	}
}
