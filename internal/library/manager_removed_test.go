package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A file the user deleted on purpose must not be imported straight back from the
// torrent that's still seeding — a deleted CAMRIP kept returning. A file that merely
// vanished (not deleted in the app) is still re-imported, and clearing the flag (what
// a fresh grab does) lets the release import again.
func TestDeliberatelyDeletedImportStaysDeleted(t *testing.T) {
	ctx := context.Background()
	lib := t.TempDir()
	m := newTestManager(t, lib, nil)
	src := t.TempDir()
	video := filepath.Join(src, "Resident.Evil.2026.1080p.CAMRIP.mkv")
	writeFile(t, video, 1000)
	cand := []Candidate{{Hash: "camrip1", Name: "Resident Evil 2026 1080p CAMRIP", ContentPath: video}}

	if n := m.Process(ctx, cand); n != 1 {
		t.Fatalf("first import: got %d, want 1", n)
	}
	target, done, err := m.repo.targetFor(ctx, "camrip1")
	if err != nil || !done || target == "" {
		t.Fatalf("no import record: %q %v %v", target, done, err)
	}

	// Deleted in the app: the file goes, and the movie service's file.removed event
	// flags the record.
	_ = os.Remove(target)
	if err := m.repo.markRemovedByTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if n := m.Process(ctx, cand); n != 0 {
		t.Fatalf("deleted on purpose, still seeding: re-imported %d, want 0", n)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("the deleted file came back")
	}

	// A fresh grab of the same release clears the flag (coordinator.clearRemovedImport).
	if _, err := m.repo.db.ExecContext(ctx, `DELETE FROM imports WHERE download_hash = ? AND removed = 1`, "camrip1"); err != nil {
		t.Fatal(err)
	}
	if n := m.Process(ctx, cand); n != 1 {
		t.Fatalf("after a new grab: imported %d, want 1", n)
	}

	// Vanished without an in-app delete: self-heal still re-imports.
	_ = os.Remove(target)
	if n := m.Process(ctx, cand); n != 1 {
		t.Fatalf("file vanished on its own: re-imported %d, want 1", n)
	}
}
