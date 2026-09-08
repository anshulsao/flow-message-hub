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

// runBig is run() with a longer deadline, for commands whose output can be
// large (a full session transcript). Same FLOW_TERM stripping.
func (f *flowExec) runBig(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.bin, args...)
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
	s := string(b)
	if err != nil {
		return strings.TrimSpace(s), fmt.Errorf("flow %s: %v", strings.Join(args, " "), err)
	}
	return s, nil
}

// humanAddr is the reserved human assignee. flow is migrating self→user
// (PR #94); default to "user" but let FLOW_HUMAN override so the writes
// work against a pre-#94 binary (FLOW_HUMAN=self).
func humanAddr() string {
	if h := os.Getenv("FLOW_HUMAN"); h != "" {
		return h
	}
	return "user"
}

// Ack marks a specific pending inbound message answered (stops escalation).
// This is the "seen" write in the pop-on-seen model.
func (f *flowExec) Ack(id string) error {
	_, err := f.run("inbox", "ack", id, "--as", humanAddr())
	return err
}

// envWithoutFlowTerm returns the current environment minus FLOW_TERM, which
// trips `--auto` (a bg flow session refuses `FLOW_TERM=bg` combined with --auto).
func envWithoutFlowTerm() []string {
	env := os.Environ()
	out := env[:0]
	for _, e := range env {
		if strings.HasPrefix(e, "FLOW_TERM=") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// RunAuto launches `flow do --auto <slug>` as a detached background run and
// returns immediately — flow owns the spawned session's lifecycle and writes
// its own auto-runs log, so we don't wait on it. Used to start a backlog task
// or relaunch a crashed one from the UI. --fresh is passed for a restart so a
// dead session is re-bootstrapped rather than resumed into its wedged state.
func (f *flowExec) RunAuto(taskSlug string, fresh bool) error {
	if taskSlug == "" {
		return fmt.Errorf("no task")
	}
	args := []string{"do", "--auto", taskSlug}
	if fresh {
		args = append(args, "--fresh")
	}
	cmd := exec.Command(f.bin, args...)
	cmd.Env = envWithoutFlowTerm()
	// Detach from our stdio; flow's own log captures the run's output.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("flow do --auto %s: %w", taskSlug, err)
	}
	// Reap the launcher process when it exits so it doesn't linger as a zombie;
	// the spawned headless run is re-parented and keeps running independently.
	go func() { _ = cmd.Wait() }()
	return nil
}

// Done marks a task complete via `flow done <slug>`. Used from the UI to clear
// a zombie (crashed/idle) task that will never reach `flow done` on its own, or
// to close out a backlog task by hand.
func (f *flowExec) Done(taskSlug string) error {
	if taskSlug == "" {
		return fmt.Errorf("no task")
	}
	_, err := f.run("done", taskSlug)
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
	_, err := f.run("message", humanAddr()+"/"+taskSlug, body)
	return err
}
