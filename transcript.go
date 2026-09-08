package main

import (
	"regexp"
	"strings"
)

// turn is one rendered unit of a session transcript: a user or assistant
// message, a tool call, or a tool result. It mirrors the sections that
// `flow transcript` prints, so the UI can replay a session as a chat.
type turn struct {
	Role  string `json:"role"`           // user | assistant | tool | result | meta
	Tool  string `json:"tool,omitempty"` // tool name when Role=="tool"
	Body  string `json:"body"`
	Error bool   `json:"error,omitempty"` // result was an error
}

// A section header printed by `flow transcript`, e.g. "─── Tool: Bash ───".
// The space on each side of the label is required, so a run of box-drawing
// characters inside a tool result (no inner spaces) is never a false header.
var transcriptHeader = regexp.MustCompile(`^─── (.+?) ───\s*$`)

// parseTranscript turns the delimited output of `flow transcript` into an
// ordered list of chat turns. Pure — no I/O — so it is unit-tested directly.
func parseTranscript(raw string) []turn {
	lines := strings.Split(raw, "\n")
	var turns []turn
	var cur *turn
	var body []string

	flush := func() {
		if cur == nil {
			return
		}
		cur.Body = strings.Trim(strings.Join(body, "\n"), "\n \t")
		turns = append(turns, *cur)
		cur = nil
		body = nil
	}

	for _, ln := range lines {
		if m := transcriptHeader.FindStringSubmatch(ln); m != nil {
			flush()
			label := m[1]
			t := turn{}
			switch {
			case label == "User":
				t.Role = "user"
			case label == "Assistant":
				t.Role = "assistant"
			case strings.HasPrefix(label, "Tool: "):
				t.Role = "tool"
				t.Tool = strings.TrimPrefix(label, "Tool: ")
			case label == "Result":
				t.Role = "result"
			case label == "Result (error)":
				t.Role = "result"
				t.Error = true
			default:
				t.Role = "meta"
				t.Tool = label
			}
			cur = &t
			continue
		}
		if cur != nil {
			body = append(body, ln)
		}
	}
	flush()
	return turns
}

// Transcript shells out to `flow transcript <slug>` and returns its raw output.
// compact drops tool results + thinking (the CLI's --compact).
func (f *flowExec) Transcript(taskSlug string, compact bool) (string, error) {
	args := []string{"transcript", taskSlug}
	if compact {
		args = append(args, "--compact")
	}
	return f.runBig(args...)
}
