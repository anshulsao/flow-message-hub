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
type flowExec struct {
	bin  string
	root string // flow home the hub reads; exported as FLOW_ROOT when the env lacks it
}

func newFlowExec() *flowExec {
	bin := os.Getenv("FLOW_BIN")
	if bin == "" {
		bin = "flow"
	}
	return &flowExec{bin: bin}
}

// env is hubEnv plus FLOW_ROOT (when the hub knows its root and the caller
// didn't set one) plus any extra vars.
func (f *flowExec) env(extra ...string) []string {
	env := hubEnv(os.Environ())
	if f.root != "" && os.Getenv("FLOW_ROOT") == "" {
		env = append(env, "FLOW_ROOT="+f.root)
	}
	return append(env, extra...)
}

func (f *flowExec) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.bin, args...)
	cmd.Env = f.env()
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
	cmd.Env = f.env()
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

// runHuman runs `flow human <args>` as the flowhub connector. The secret
// travels only in FLOW_CONNECTOR_TOKEN — never argv, never the error.
func (f *flowExec) runHuman(tok string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.bin, append([]string{"human"}, args...)...)
	cmd.Env = f.env("FLOW_CONNECTOR_TOKEN=" + tok)
	b, err := cmd.CombinedOutput()
	out := strings.ReplaceAll(strings.TrimSpace(string(b)), tok, "<redacted>")
	if err != nil {
		if strings.Contains(out, "not a connector") {
			return errNotConnector
		}
		return fmt.Errorf("flow human %s: %v: %s", args[0], err, out)
	}
	return nil
}

// HumanSeen marks human mail seen (`flow human seen <id>`).
func (f *flowExec) HumanSeen(tok, id string) error { return f.runHuman(tok, "seen", id) }

// HumanReply answers an ask (`flow human reply <id> "<text>"`): flow marks
// it answered and delivers text to the asking task with reply_to=<id>.
func (f *flowExec) HumanReply(tok, id, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("empty reply")
	}
	return f.runHuman(tok, "reply", id, text)
}

// envWithoutFlowTerm returns the current environment minus FLOW_TERM, which
// trips `--auto` (a bg flow session refuses `FLOW_TERM=bg` combined with --auto).
// hubStripEnv lists env vars the hub must not pass to flow:
//   - FLOW_TERM=bg trips guards on some flow subcommands.
//   - The harness session vars make flow think the hub IS the Claude/Codex
//     session that happened to start it, so inbox replies were sent "from"
//     that session's task (and a reply to that task was rejected as its own
//     inbox). The hub always acts as the bare human.
//   - FLOW_AUTO_RUN marks an autonomous run and blocks `flow auto`.
var hubStripEnv = []string{"FLOW_TERM", "CLAUDE_CODE_SESSION_ID", "CLAUDECODE", "CODEX_THREAD_ID", "FLOW_AUTO_RUN"}

// hubEnv returns env without the hubStripEnv vars. It copies, so the
// caller's slice is left intact.
func hubEnv(env []string) []string {
	out := make([]string, 0, len(env))
next:
	for _, e := range env {
		for _, k := range hubStripEnv {
			if strings.HasPrefix(e, k+"=") {
				continue next
			}
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
	cmd.Env = f.env()
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

// PeerReply answers a peer hub's message: `flow message <peer> --reply-to
// <id> -- "<text>"`. flow stores it as an outbound peer row and dispatches
// it over the peer's connector with reply_to_ref = that row's origin_ref.
// "--" keeps a body that starts with "-" from being read as a flag.
func (f *flowExec) PeerReply(peer, id, text string) error {
	if peer == "" || strings.ContainsAny(peer, "/ ") || canonAssignee(peer) == humanID {
		return fmt.Errorf("bad peer %q", peer)
	}
	if id == "" {
		return fmt.Errorf("no message to reply to")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("empty reply")
	}
	_, err := f.run("message", peer, "--reply-to", id, "--", text)
	return err
}
