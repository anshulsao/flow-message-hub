package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// flowExec runs the `flow` CLI for the two mutations the inbox needs. All
// writes to the bus go through flow (never through our read-only db handle),
// so flow stays the single owner of its own state.
type flowExec struct{ bin string }

func newFlowExec() *flowExec {
	bin := os.Getenv("FLOW_BIN")
	if bin == "" {
		bin = "flow"
	}
	return &flowExec{bin: bin}
}

func (f *flowExec) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.bin, args...)
	// FLOW_TERM=bg trips guards on some flow subcommands; strip it for our
	// one-shot message/ack calls.
	env := cmd.Environ()
	out := env[:0]
	for _, e := range env {
		if strings.HasPrefix(e, "FLOW_TERM=") {
			continue
		}
		out = append(out, e)
	}
	cmd.Env = out
	b, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(b))
	if err != nil {
		return s, fmt.Errorf("flow %s: %v: %s", strings.Join(args, " "), err, s)
	}
	return s, nil
}

// Ack marks a specific pending inbound message answered (stops escalation).
// This is the "seen" write in the pop-on-seen model.
func (f *flowExec) Ack(id string) error {
	_, err := f.run("inbox", "ack", id, "--as", "user")
	return err
}

// Reply routes the human's answer back to the agent's own session inbox.
func (f *flowExec) Reply(taskSlug, body string) error {
	if taskSlug == "" || taskSlug == "~" {
		return fmt.Errorf("thread is not repliable")
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("empty reply")
	}
	_, err := f.run("message", "user/"+taskSlug, body)
	return err
}
