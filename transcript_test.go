package main

import "testing"

func TestParseTranscript(t *testing.T) {
	raw := "" +
		"─── User ───\n" +
		"do the thing\n\n" +
		"─── Assistant ───\n" +
		"on it\n\n" +
		"─── Tool: Bash ───\n" +
		"$ flow show task\n\n" +
		"─── Result ───\n" +
		"slug: demo\nstatus: in-progress\n\n" +
		"─── Tool: Read ───\n" +
		"/tmp/brief.md\n\n" +
		"─── Result (error) ───\n" +
		"no such file\n"

	turns := parseTranscript(raw)
	if len(turns) != 6 {
		t.Fatalf("want 6 turns, got %d: %+v", len(turns), turns)
	}
	want := []struct {
		role, tool, body string
		err              bool
	}{
		{"user", "", "do the thing", false},
		{"assistant", "", "on it", false},
		{"tool", "Bash", "$ flow show task", false},
		{"result", "", "slug: demo\nstatus: in-progress", false},
		{"tool", "Read", "/tmp/brief.md", false},
		{"result", "", "no such file", true},
	}
	for i, w := range want {
		g := turns[i]
		if g.Role != w.role || g.Tool != w.tool || g.Body != w.body || g.Error != w.err {
			t.Errorf("turn %d = %+v, want role=%q tool=%q body=%q err=%v", i, g, w.role, w.tool, w.body, w.err)
		}
	}
}

// A tool result that happens to contain a box-drawing line in its body must not
// be mistaken for a section header (only a full ─── label ─── line is a header).
func TestParseTranscript_NotTrickedByBodyDashes(t *testing.T) {
	raw := "" +
		"─── Result ───\n" +
		"drew a table:\n" +
		"───────────\n" +
		"a | b\n"
	turns := parseTranscript(raw)
	if len(turns) != 1 {
		t.Fatalf("want 1 turn, got %d", len(turns))
	}
	if turns[0].Body != "drew a table:\n───────────\na | b" {
		t.Fatalf("body mangled: %q", turns[0].Body)
	}
}
