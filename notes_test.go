package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadTaskFiles_BriefAndNotesInTimeOrder(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "tasks", "a")
	upd := filepath.Join(dir, "updates")
	if err := os.MkdirAll(upd, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "brief.md"), []byte("# Do the thing\nbody"), 0o644)
	os.WriteFile(filepath.Join(upd, "2026-10-05-second.md"), []byte("\n# Second note\nmore"), 0o644)
	os.WriteFile(filepath.Join(upd, "2026-10-01-first.md"), []byte("First line plain\nrest"), 0o644)
	os.WriteFile(filepath.Join(upd, "ignore.txt"), []byte("x"), 0o644)
	t1 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	os.Chtimes(filepath.Join(upd, "2026-10-01-first.md"), t1, t1)
	os.Chtimes(filepath.Join(upd, "2026-10-05-second.md"), t2, t2)

	brief, notes := readTaskFiles(root, "a")
	if brief != "# Do the thing\nbody" {
		t.Errorf("brief: %q", brief)
	}
	if len(notes) != 2 || notes[0].Title != "First line plain" || notes[1].Title != "Second note" {
		t.Fatalf("notes: %+v", notes)
	}
	if !notes[1].Time.Equal(t2) || notes[1].File != "2026-10-05-second.md" {
		t.Errorf("note meta: %+v", notes[1])
	}

	if _, none := readTaskFiles(root, "missing"); len(none) != 0 {
		t.Errorf("missing task should yield no notes")
	}
	if _, bad := readTaskFiles(root, "../etc"); bad != nil {
		t.Errorf("path traversal must be refused")
	}
}
