package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const noteBodyMax = 8000

// validSlug guards filesystem lookups against path traversal.
func validSlug(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	return !strings.ContainsAny(s, `/\`) && !strings.Contains(s, "..")
}

func titleOf(content string) string {
	for _, ln := range strings.Split(content, "\n") {
		ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "#"))
		if ln != "" {
			return ln
		}
	}
	return ""
}

// readTaskFiles loads <root>/tasks/<slug>/brief.md and updates/*.md (oldest
// first). Missing files are not an error; a bad slug returns nothing.
func readTaskFiles(root, slug string) (string, []note) {
	if !validSlug(slug) {
		return "", nil
	}
	dir := filepath.Join(root, "tasks", slug)
	brief, _ := os.ReadFile(filepath.Join(dir, "brief.md"))
	entries, _ := os.ReadDir(filepath.Join(dir, "updates"))
	notes := []note{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "updates", e.Name()))
		if err != nil {
			continue
		}
		body := string(raw)
		if len(body) > noteBodyMax {
			body = body[:noteBodyMax] + "\n…"
		}
		notes = append(notes, note{Time: info.ModTime(), Title: titleOf(body), Body: body, File: e.Name()})
	}
	sort.SliceStable(notes, func(i, j int) bool { return notes[i].Time.Before(notes[j].Time) })
	return string(brief), notes
}

// latestNotes returns each task's newest update note (for card "latest" lines).
func latestNotes(root string, slugs []string) map[string]note {
	out := map[string]note{}
	for _, s := range slugs {
		if _, ns := readTaskFiles(root, s); len(ns) > 0 {
			n := ns[len(ns)-1]
			n.Body = ""
			out[s] = n
		}
	}
	return out
}
