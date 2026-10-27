package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jl builds one jsonl line.
func jl(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func userLine(t *testing.T, ts, text, kind string) string {
	v := map[string]any{"type": "user", "timestamp": ts, "message": map[string]any{"role": "user", "content": text}}
	if kind != "" {
		v["origin"] = map[string]any{"kind": kind}
	}
	return jl(t, v)
}

func asstLine(t *testing.T, ts string, blocks ...map[string]any) string {
	return jl(t, map[string]any{"type": "assistant", "timestamp": ts, "message": map[string]any{"role": "assistant", "content": blocks}})
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }
func tool(name string) map[string]any {
	return map[string]any{"type": "tool_use", "name": name, "input": map[string]any{}}
}

func sampleSession(t *testing.T) string {
	return userLine(t, "2026-10-27T07:00:00Z", "status", "human") +
		jl(t, map[string]any{"type": "attachment", "timestamp": "2026-10-27T07:00:01Z"}) +
		asstLine(t, "2026-10-27T07:00:02Z", map[string]any{"type": "thinking", "thinking": "hmm"}, tool("Bash")) +
		jl(t, map[string]any{"type": "user", "timestamp": "2026-10-27T07:00:03Z", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "content": "ok"}}}}) +
		asstLine(t, "2026-10-27T07:00:04Z", tool("Bash"), tool("Read")) +
		asstLine(t, "2026-10-27T07:00:05Z", text("All green.")) +
		userLine(t, "2026-10-27T07:00:06Z", "<task-notification>\n<summary>bg done</summary>", "task-notification") +
		userLine(t, "2026-10-27T07:00:07Z", "<system-reminder>noise</system-reminder>", "") +
		jl(t, map[string]any{"type": "assistant", "isSidechain": true, "timestamp": "2026-10-27T07:00:08Z", "message": map[string]any{"content": []any{text("subagent chatter")}}}) +
		userLine(t, "2026-10-27T07:00:09Z", "ship it", "") +
		asstLine(t, "2026-10-27T07:00:10Z", text("Shipped."))
}

func TestParseSession_KeepsConversationCollapsesTools(t *testing.T) {
	items := parseSessionLines(strings.Split(sampleSession(t), "\n"))
	want := []struct{ kind, body string }{
		{"you", "status"},
		{"tools", ""},
		{"agent", "All green."},
		{"you", "ship it"},
		{"agent", "Shipped."},
	}
	if len(items) != len(want) {
		t.Fatalf("items: %+v", items)
	}
	for i, w := range want {
		if items[i].Kind != w.kind || (w.body != "" && items[i].Body != w.body) {
			t.Errorf("item %d: got %s %q, want %s %q", i, items[i].Kind, items[i].Body, w.kind, w.body)
		}
	}
	if items[1].Tools != 3 || items[1].ToolNames != "Bash ×2, Read" {
		t.Errorf("tool batch: %d %q", items[1].Tools, items[1].ToolNames)
	}
	if items[0].Time != "2026-10-27T07:00:00Z" {
		t.Errorf("time: %q", items[0].Time)
	}
}

func TestParseSession_PastedContentIsSummarised(t *testing.T) {
	line := userLine(t, "2026-10-27T07:00:00Z", "look:\n<pasted_content id=\"a1\">\nl1\nl2\nl3\n</pasted_content id=\"a1\">\nthoughts?", "human")
	items := parseSessionLines([]string{line})
	if len(items) != 1 || items[0].Body != "look:\n[pasted 3 lines]\nthoughts?" {
		t.Fatalf("got %+v", items)
	}
}

func writeSession(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadSessionTail_PagesBackwardThenForward(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		ts := "2026-10-27T07:" + pad2(i) + ":00Z"
		b.WriteString(userLine(t, ts, "q"+pad2(i), "human"))
		b.WriteString(asstLine(t, ts, text("a"+pad2(i))))
	}
	p := writeSession(t, b.String())

	last, start, err := readSessionTail(p, -1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) < 10 || last[len(last)-1].Body != "a29" {
		t.Fatalf("tail: %d items, last=%+v", len(last), last[len(last)-1])
	}
	if start <= 0 {
		t.Fatalf("start cursor should point into the file, got %d", start)
	}

	older, start2, err := readSessionTail(p, start, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(older) == 0 || start2 >= start {
		t.Fatalf("older page: %d items, cursor %d -> %d", len(older), start, start2)
	}
	if older[len(older)-1].Time > last[0].Time {
		t.Errorf("older page must end before the newer page starts")
	}
	seen := map[string]bool{}
	for _, it := range append(older, last...) {
		if seen[it.Body] {
			t.Errorf("duplicate across pages: %q", it.Body)
		}
		seen[it.Body] = true
	}

	// forward read from the tail cursor returns the same newest items
	fwd, err := readSessionFrom(p, start)
	if err != nil || len(fwd) != len(last) || fwd[len(fwd)-1].Body != "a29" {
		t.Fatalf("forward: %d vs %d, err=%v", len(fwd), len(last), err)
	}
}

func TestReadSessionTail_BigLinesAcrossChunks(t *testing.T) {
	huge := strings.Repeat("x", 3*tailChunk)
	content := userLine(t, "2026-10-27T07:00:00Z", "first", "human") +
		jl(t, map[string]any{"type": "user", "timestamp": "2026-10-27T07:00:01Z", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "content": huge}}}}) +
		asstLine(t, "2026-10-27T07:00:02Z", text("after big"))
	items, start, err := readSessionTail(writeSession(t, content), -1, 10)
	if err != nil || len(items) != 2 || items[0].Body != "first" || items[1].Body != "after big" || start != 0 {
		t.Fatalf("items=%+v start=%d err=%v", items, start, err)
	}
}

func TestFindSessionFileAndLastAgentText(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "-Users-me-repo")
	os.MkdirAll(proj, 0o755)
	os.WriteFile(filepath.Join(proj, "sid-123.jsonl"), []byte(sampleSession(t)), 0o644)

	p := findSessionFile(dir, "sid-123")
	if p != filepath.Join(proj, "sid-123.jsonl") {
		t.Fatalf("find: %q", p)
	}
	if findSessionFile(dir, "missing") != "" || findSessionFile(dir, "../x") != "" {
		t.Errorf("missing or unsafe ids must return empty")
	}
	c := newSessionCache()
	if got := c.lastAgentText(p); got != "Shipped." {
		t.Errorf("last agent text: %q", got)
	}
}

func pad2(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
