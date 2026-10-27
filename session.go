package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Reads a Claude Code session transcript (jsonl) as a chat: your prompts,
// the agent's text replies, and tool calls collapsed into counted batches.
// Files can be 100MB+, so reads start from the end and page backwards.

const (
	tailChunk    = 256 * 1024
	sessBodyMax  = 6000
	forwardLimit = 32 * 1024 * 1024
)

type sessItem struct {
	Time      string `json:"time"`
	Kind      string `json:"kind"` // you | agent | tools
	Body      string `json:"body,omitempty"`
	Tools     int    `json:"tools,omitempty"`
	ToolNames string `json:"tool_names,omitempty"`

	names []string
	count map[string]int
}

type sessLine struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Origin      *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type sessBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

var (
	pastedRe   = regexp.MustCompile(`(?s)<pasted_content[^>]*>(.*?)</pasted_content[^>]*>`)
	reminderRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
)

func clip(s string) string {
	if r := []rune(s); len(r) > sessBodyMax {
		return string(r[:sessBodyMax]) + "…"
	}
	return s
}

func cleanPrompt(s string) string {
	s = reminderRe.ReplaceAllString(s, "")
	s = pastedRe.ReplaceAllStringFunc(s, func(m string) string {
		inner := strings.Trim(pastedRe.FindStringSubmatch(m)[1], "\n")
		return fmt.Sprintf("[pasted %d lines]", len(strings.Split(inner, "\n")))
	})
	return strings.TrimSpace(s)
}

// contentBlocks accepts either a plain string or an array of blocks.
func contentBlocks(raw json.RawMessage) []sessBlock {
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return []sessBlock{{Type: "text", Text: s}}
		}
		return nil
	}
	var bs []sessBlock
	_ = json.Unmarshal(raw, &bs)
	return bs
}

func parseSessionLines(lines []string) []sessItem {
	var out []sessItem
	for _, ln := range lines {
		if !strings.Contains(ln, `"type":"user"`) && !strings.Contains(ln, `"type":"assistant"`) {
			continue // cheap reject for attachments, snapshots, meta
		}
		var l sessLine
		if json.Unmarshal([]byte(ln), &l) != nil || l.IsSidechain {
			continue
		}
		switch l.Type {
		case "user":
			if l.IsMeta || (l.Origin != nil && l.Origin.Kind != "human") {
				continue
			}
			var parts []string
			for _, b := range contentBlocks(l.Message.Content) {
				if b.Type == "text" {
					parts = append(parts, b.Text)
				}
			}
			txt := strings.Join(parts, "\n")
			if l.Origin == nil && strings.HasPrefix(strings.TrimSpace(txt), "<") {
				continue // hook / system injection without an origin tag
			}
			if txt = cleanPrompt(txt); txt != "" {
				out = append(out, sessItem{Time: l.Timestamp, Kind: "you", Body: clip(txt)})
			}
		case "assistant":
			for _, b := range contentBlocks(l.Message.Content) {
				switch b.Type {
				case "text":
					if t := strings.TrimSpace(b.Text); t != "" {
						out = append(out, sessItem{Time: l.Timestamp, Kind: "agent", Body: clip(t)})
					}
				case "tool_use":
					if n := len(out); n == 0 || out[n-1].Kind != "tools" {
						out = append(out, sessItem{Time: l.Timestamp, Kind: "tools", count: map[string]int{}})
					}
					it := &out[len(out)-1]
					if it.count[b.Name] == 0 {
						it.names = append(it.names, b.Name)
					}
					it.count[b.Name]++
					it.Tools++
				}
			}
		}
	}
	for i := range out {
		if out[i].Kind == "tools" {
			var parts []string
			for _, n := range out[i].names {
				if c := out[i].count[n]; c > 1 {
					parts = append(parts, fmt.Sprintf("%s ×%d", n, c))
				} else {
					parts = append(parts, n)
				}
			}
			out[i].ToolNames = strings.Join(parts, ", ")
		}
	}
	return out
}

func convCount(items []sessItem) int {
	n := 0
	for _, it := range items {
		if it.Kind != "tools" {
			n++
		}
	}
	return n
}

// readSessionTail returns about n conversation items ending at byte offset
// before (-1 = end of file), plus the offset where the returned page starts
// (pass it back as before to page further up).
func readSessionTail(path string, before int64, n int) ([]sessItem, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	cur := info.Size()
	if before >= 0 && before < cur {
		cur = before
	}
	bufStart := cur
	var buf []byte
	var rev []string // lines, newest first
	found := 0
	for cur > 0 && found < n {
		k := -1
		if len(buf) > 1 {
			k = bytes.LastIndexByte(buf[:len(buf)-1], '\n')
		}
		if k < 0 && bufStart > 0 {
			read := int64(tailChunk)
			if read > bufStart {
				read = bufStart
			}
			chunk := make([]byte, read)
			if _, err := f.ReadAt(chunk, bufStart-read); err != nil && err != io.EOF {
				return nil, 0, err
			}
			buf = append(chunk, buf...)
			bufStart -= read
			continue
		}
		line := buf[k+1:]
		cur = bufStart + int64(k+1)
		buf = buf[:k+1]
		s := string(line)
		rev = append(rev, s)
		found += convCount(parseSessionLines([]string{s}))
	}
	lines := make([]string, len(rev))
	for i, s := range rev {
		lines[len(rev)-1-i] = s
	}
	return parseSessionLines(lines), cur, nil
}

// readSessionFrom parses everything from byte offset from to the end.
func readSessionFrom(path string, from int64) ([]sessItem, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReader(io.LimitReader(f, forwardLimit))
	var lines []string
	for {
		s, err := r.ReadString('\n')
		if s != "" {
			lines = append(lines, s)
		}
		if err != nil {
			break
		}
	}
	return parseSessionLines(lines), nil
}

// findSessionFile locates <projectsDir>/<any>/<sid>.jsonl.
func findSessionFile(projectsDir, sid string) string {
	if !validSlug(sid) {
		return ""
	}
	m, _ := filepath.Glob(filepath.Join(projectsDir, "*", sid+".jsonl"))
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

// sessionCache memoises "agent's last words" per file by size+mtime, so card
// lists can show them without re-reading unchanged transcripts every poll.
type sessionCache struct {
	mu    sync.Mutex
	m     map[string]cachedLast
	paths map[string]string // session id -> transcript path
}

type cachedLast struct {
	size  int64
	mtime time.Time
	text  string
	at    string
}

func newSessionCache() *sessionCache {
	return &sessionCache{m: map[string]cachedLast{}, paths: map[string]string{}}
}

// path resolves a session id to its transcript, remembering hits (misses are
// retried, since a new session's file appears after its first turn).
func (c *sessionCache) path(projectsDir, sid string) string {
	c.mu.Lock()
	p, ok := c.paths[sid]
	c.mu.Unlock()
	if ok {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	p = findSessionFile(projectsDir, sid)
	if p != "" {
		c.mu.Lock()
		c.paths[sid] = p
		c.mu.Unlock()
	}
	return p
}

func (c *sessionCache) last(path string) (string, string) {
	info, err := os.Stat(path)
	if err != nil {
		return "", ""
	}
	c.mu.Lock()
	if v, ok := c.m[path]; ok && v.size == info.Size() && v.mtime.Equal(info.ModTime()) {
		c.mu.Unlock()
		return v.text, v.at
	}
	c.mu.Unlock()
	text, at := "", ""
	items, _, _ := readSessionTail(path, -1, 4)
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == "agent" {
			text, at = items[i].Body, items[i].Time
			break
		}
	}
	c.mu.Lock()
	c.m[path] = cachedLast{size: info.Size(), mtime: info.ModTime(), text: text, at: at}
	c.mu.Unlock()
	return text, at
}

func (c *sessionCache) lastAgentText(path string) string {
	t, _ := c.last(path)
	return t
}
