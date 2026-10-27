// Manual end-to-end check against a REAL Claude Code session (opt-in).
// Open a throwaway iTerm window running `claude` in a trusted folder, then:
//
//	E2E_TTY=/dev/ttysNNN go test -run ZZE2E -v .
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestZZE2E(t *testing.T) {
	tty := os.Getenv("E2E_TTY")
	if tty == "" {
		t.Skip()
	}
	time.Sleep(6 * time.Second) // let claude boot
	out, _ := exec.Command("ps", "-axo", "pid=,tty=,command=").Output()
	pid := 0
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) >= 3 && "/dev/"+f[1] == tty && strings.HasSuffix(f[2], "claude") {
			pid, _ = strconv.Atoi(f[0])
		}
	}
	if pid == 0 {
		t.Fatalf("no claude on %s", tty)
	}
	agents, err := (&agentsLister{}).list()
	if err != nil {
		t.Fatal(err)
	}
	sid := ""
	for id, a := range agents {
		if a.PID == pid {
			sid = id
			t.Logf("agents: sid=%s kind=%s status=%s", id, a.Kind, a.Status)
		}
	}
	if sid == "" {
		t.Fatalf("pid %d not in claude agents --json", pid)
	}
	gotTTY, err := ttyOfClaude(pid)
	if err != nil || gotTTY != tty {
		t.Fatalf("tty: %q %v", gotTTY, err)
	}
	msg := "Reply with exactly the word PONG and nothing else.\nThis is line two with \"quotes\" and a back\\slash."
	if err := (itermSender{}).Send(gotTTY, msg); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	path := findSessionFile(filepath.Join(home, ".claude", "projects"), sid)
	for i := 0; i < 40; i++ {
		time.Sleep(time.Second)
		if path == "" {
			path = findSessionFile(filepath.Join(home, ".claude", "projects"), sid)
			continue
		}
		items, _, _ := readSessionTail(path, -1, 10)
		var you, agent string
		for _, it := range items {
			if it.Kind == "you" {
				you = it.Body
			}
			if it.Kind == "agent" {
				agent = it.Body
			}
		}
		if you != "" && agent != "" {
			t.Logf("you=%q", you)
			t.Logf("agent=%q", agent)
			if you != msg {
				t.Errorf("prompt arrived altered:\n got %q\nwant %q", you, msg)
			}
			return
		}
	}
	t.Fatalf("no reply in transcript %s", path)
}
