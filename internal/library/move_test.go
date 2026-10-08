package library

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func moveTestImporter(t *testing.T) (*Importer, string) {
	t.Helper()
	root := t.TempDir()
	return NewImporter(root, slog.New(slog.NewTextHandler(io.Discard, nil))), root
}

func writeMoveFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readMoveFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// os.Rename replaces its target on Linux. A rename that lands on an existing episode
// destroyed it; Move must refuse instead and leave both files alone.
func TestMoveRefusesExistingTarget(t *testing.T) {
	im, root := moveTestImporter(t)
	from := filepath.Join(root, "Show", "Season 3", "a.mkv")
	to := filepath.Join(root, "Show", "Season 3", "b.mkv")
	writeMoveFile(t, from, "file A")
	writeMoveFile(t, to, "file B")

	err := im.Move(from, to)
	if !errors.Is(err, ErrTargetExists) {
		t.Fatalf("Move onto an existing file = %v, want ErrTargetExists", err)
	}
	if readMoveFile(t, from) != "file A" || readMoveFile(t, to) != "file B" {
		t.Error("both files must survive a refused move untouched")
	}

	// A free target still moves, creating its folder.
	free := filepath.Join(root, "Show", "Season 4", "a.mkv")
	if err := im.Move(from, free); err != nil {
		t.Fatalf("Move to a free path: %v", err)
	}
	if readMoveFile(t, free) != "file A" {
		t.Error("the moved file should be at its new path")
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Error("the old name should be gone after a move")
	}
}

// Imports hardlink, so the "target" can be another name for the very same file. That's
// not a different file: drop the source name and keep the one inode under the target.
func TestMoveSameInodeRemovesSourceName(t *testing.T) {
	im, root := moveTestImporter(t)
	from := filepath.Join(root, "old.mkv")
	to := filepath.Join(root, "new.mkv")
	writeMoveFile(t, from, "same bytes")
	if err := os.Link(from, to); err != nil {
		t.Skipf("hardlinks unsupported here: %v", err)
	}
	if err := im.Move(from, to); err != nil {
		t.Fatalf("Move onto a hardlink of itself: %v", err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Error("the source name should be removed")
	}
	if readMoveFile(t, to) != "same bytes" {
		t.Error("the file must remain under the target name")
	}
}

func TestMoveSamePathIsNoop(t *testing.T) {
	im, root := moveTestImporter(t)
	p := filepath.Join(root, "a.mkv")
	writeMoveFile(t, p, "keep me")
	if err := im.Move(p, p); err != nil {
		t.Fatalf("Move to itself: %v", err)
	}
	if readMoveFile(t, p) != "keep me" {
		t.Error("a no-op move must leave the file alone")
	}
}

// A case-only rename moves the one file to its new name, whichever kind of disk it's on:
// on a case-insensitive one the two names are a single entry (removing "from" would delete
// the file), and on a case-sensitive one two hardlinks named that way are two entries
// (renaming one onto the other does nothing, leaving the old name behind).
func TestMoveCaseOnlyRename(t *testing.T) {
	im, root := moveTestImporter(t)
	from := filepath.Join(root, "show - s01e01.mkv")
	to := filepath.Join(root, "Show - S01E01.mkv")
	writeMoveFile(t, from, "the episode")
	if _, err := os.Lstat(to); err != nil {
		// Case-sensitive disk: make the second name a hardlink, as an import would.
		if err := os.Link(from, to); err != nil {
			t.Skipf("hardlinks unsupported here: %v", err)
		}
	}
	if err := im.Move(from, to); err != nil {
		t.Fatalf("case-only Move: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(to) {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("folder holds %v, want only %q", names, filepath.Base(to))
	}
	if readMoveFile(t, to) != "the episode" {
		t.Error("the file must survive under its new name")
	}
}
