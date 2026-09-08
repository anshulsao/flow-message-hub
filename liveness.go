package main

import (
	"os/exec"
	"strings"
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

// taskRunStates classifies every task's real liveness against one process-table
// snapshot. Liveness is only probed for in-progress tasks (done/backlog can't
// have a live process by definition), so the whole classification costs one ps
// exec per request. We deliberately do NOT consult auto_run_pid: pids are
// recycled by the OS, so a dead run's stale pid can land on an unrelated live
// process and falsely read as alive. session_id-in-ps is the recycle-proof
// signal (a live run is `claude --session-id <id> …`, auto or interactive).
func taskRunStates(tasks map[string]taskMeta) map[string]string {
	probe := newLivenessProbe()
	out := make(map[string]string, len(tasks))
	for slug, m := range tasks {
		live := m.Status == "in-progress" && probe.sessionLive(m.SessionID)
		out[slug] = runState(m, live)
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
