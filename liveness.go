package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// livenessProbe answers "is this actually running right now?" for the tasks
// referenced by the graph. It takes one snapshot of the process table per
// request so every node is classified against the same instant.
type livenessProbe struct {
	psBlob string // combined argv of every running process (for session-id match)
}

func newLivenessProbe() livenessProbe {
	// -axww: every process, full argv untruncated. A resumed/auto harness runs
	// as `claude --session-id <id> ...` or `... --resume <id>`, so the task's
	// session_id appears verbatim in this blob exactly when its session is live.
	out, err := exec.Command("ps", "-axww", "-o", "command=").Output()
	if err != nil {
		return livenessProbe{}
	}
	return livenessProbe{psBlob: string(out)}
}

// sessionLive reports whether an interactive harness session is attached to a
// live process (its tab is open or it was resumed).
func (p livenessProbe) sessionLive(sessionID string) bool {
	return sessionID != "" && strings.Contains(p.psBlob, sessionID)
}

// pidAlive reports whether an --auto background run's pid is still alive.
// Signal 0 performs error checking without delivering a signal.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid) // always succeeds on unix
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// taskRunStates classifies every task's real liveness against one process-table
// snapshot. Liveness is only probed for in-progress tasks (done/backlog can't
// have a live process by definition), keeping the per-request cost to one ps
// exec plus a Signal(0) per running task.
func taskRunStates(tasks map[string]taskMeta) map[string]string {
	probe := newLivenessProbe()
	out := make(map[string]string, len(tasks))
	for slug, m := range tasks {
		live, autoLive := false, false
		if m.Status == "in-progress" {
			live = probe.sessionLive(m.SessionID)
			autoLive = pidAlive(m.AutoRunPID)
		}
		out[slug] = runState(m, live, autoLive)
	}
	return out
}

// launchVerb maps a run state to the UI action that can revive it, or "".
func launchVerb(runState string) string {
	switch runState {
	case "backlog":
		return "start"
	case "crashed":
		return "restart"
	}
	return ""
}

// enrichLiveness stamps every session node with its real RunState and, where
// applicable, a Launch verb.
func enrichLiveness(nodes []Node, tasks map[string]taskMeta) {
	states := taskRunStates(tasks)
	for i := range nodes {
		n := &nodes[i]
		if _, ok := tasks[n.ID]; !ok {
			continue // the human hub and bare targets have no task row
		}
		n.RunState = states[n.ID]
		n.Launch = launchVerb(states[n.ID])
	}
}
