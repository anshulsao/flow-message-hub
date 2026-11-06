package main

import (
	"strings"
	"testing"
)

// The hub is often started from inside a Claude Code / Codex session. flow
// identifies "who is sending" from the harness session env, so if the hub
// passes that env through, every reply from the inbox is attributed to the
// launching session (and a reply to that session's own task is rejected as
// "THIS session's own inbox"). The hub must run flow as the bare human.
func TestHubEnv_StripsHarnessIdentity(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"HOME=/Users/x",
		"FLOW_TERM=bg",
		"CLAUDE_CODE_SESSION_ID=352ff9c3",
		"CLAUDECODE=1",
		"CODEX_THREAD_ID=abc",
		"FLOW_AUTO_RUN=1",
		"FLOW_ROOT=/Users/x/.flow",
	}
	got := strings.Join(hubEnv(in), "\n")
	for _, drop := range []string{"FLOW_TERM=", "CLAUDE_CODE_SESSION_ID=", "CLAUDECODE=", "CODEX_THREAD_ID=", "FLOW_AUTO_RUN="} {
		if strings.Contains(got, drop) {
			t.Errorf("%s should be stripped, got:\n%s", drop, got)
		}
	}
	for _, keep := range []string{"PATH=/usr/bin", "HOME=/Users/x", "FLOW_ROOT=/Users/x/.flow"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%s should be kept, got:\n%s", keep, got)
		}
	}
}
