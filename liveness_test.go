package main

import "testing"

// runState is the single source of truth the board trusts instead of the
// stale tasks.status column. It folds three signals — nominal status, whether
// a process is actually alive, and whether it was an --auto run — into one of:
// live | idle | crashed | done | backlog.
func TestRunState(t *testing.T) {
	cases := []struct {
		name        string
		m           taskMeta
		liveSession bool
		want        string
	}{
		{
			name: "auto run whose session is attached in ps is live",
			m:    taskMeta{Status: "in-progress", AutoRunStatus: "running", SessionID: "abc"},
			liveSession: true, want: "live",
		},
		{
			name: "interactive task whose session is attached is live",
			m:    taskMeta{Status: "in-progress", SessionID: "abc"},
			liveSession: true, want: "live",
		},
		{
			name: "auto run in-progress but session gone from ps is crashed, not idle",
			m:    taskMeta{Status: "in-progress", AutoRunStatus: "running"},
			want: "crashed",
		},
		{
			name: "auto run flow already marked dead is crashed",
			m:    taskMeta{Status: "in-progress", AutoRunStatus: "dead"},
			want: "crashed",
		},
		{
			name: "interactive in-progress, tab closed, never an auto run = idle",
			m:    taskMeta{Status: "in-progress", SessionID: "abc"},
			want: "idle",
		},
		{
			name: "done stays done even with a stale running label",
			m:    taskMeta{Status: "done", AutoRunStatus: "running"},
			want: "done",
		},
		{
			name: "backlog is backlog",
			m:    taskMeta{Status: "backlog"},
			want: "backlog",
		},
	}
	for _, c := range cases {
		if got := runState(c.m, c.liveSession); got != c.want {
			t.Errorf("%s: runState = %q, want %q", c.name, got, c.want)
		}
	}
}
