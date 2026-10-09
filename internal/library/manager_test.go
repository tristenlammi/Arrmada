package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/store"
)

func newTestManager(t *testing.T, root string, bus *eventbus.Bus) *Manager {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return NewManager(st.DB(), root, bus, quiet())
}

// TestProcessSkipsWhenTargetUnverifiable pins fix #6a: when os.Stat on a recorded
// import target fails with anything OTHER than not-exist (EIO, ENOTDIR, dead
// mount), the candidate is skipped — never re-imported — and the record is kept.
func TestProcessSkipsWhenTargetUnverifiable(t *testing.T) {
	ctx := context.Background()
	lib := t.TempDir()
	m := newTestManager(t, lib, nil)

	// A target path whose parent is a regular FILE → Stat fails with ENOTDIR,
	// which is not os.IsNotExist. That stands in for an errored/dead mount.
	blocker := filepath.Join(lib, "blocker.txt")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	weird := filepath.Join(blocker, "Movie (2024)", "movie.mkv")
	if err := m.repo.record(ctx, ImportRecord{Hash: "h1", TargetPath: weird, Title: "Movie"}, AttachStateAttached); err != nil {
		t.Fatal(err)
	}

	src := t.TempDir()
	video := filepath.Join(src, "Movie.2024.1080p.WEB-DL.mkv")
	writeFile(t, video, 1000)

	n := m.Process(ctx, []Candidate{{Hash: "h1", Name: "Movie.2024.1080p.WEB-DL", ContentPath: video}})
	if n != 0 {
		t.Errorf("Process imported %d, want 0 — an unverifiable target must be skipped", n)
	}
	if _, done, err := m.repo.targetFor(ctx, "h1"); err != nil || !done {
		t.Errorf("record must be kept (done=%v err=%v), not forgotten on an unknown state", done, err)
	}

	// Control: a target that is definitively gone (ENOENT) IS forgotten and re-imported.
	if err := m.repo.record(ctx, ImportRecord{Hash: "h2", TargetPath: filepath.Join(lib, "gone.mkv"), Title: "Movie"}, AttachStateAttached); err != nil {
		t.Fatal(err)
	}
	n = m.Process(ctx, []Candidate{{Hash: "h2", Name: "Movie.2024.1080p.WEB-DL", ContentPath: video}})
	if n != 1 {
		t.Errorf("Process imported %d, want 1 — a missing file re-imports", n)
	}
}

// TestProcessEventCarriesHash pins fix #11: the download.imported event payload
// includes the download hash.
func TestProcessEventCarriesHash(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.New(quiet())
	m := newTestManager(t, t.TempDir(), bus)

	events, cancel := bus.Subscribe("download.imported")
	defer cancel()

	src := t.TempDir()
	video := filepath.Join(src, "Movie.2024.1080p.WEB-DL.mkv")
	writeFile(t, video, 1000)
	n := m.Process(ctx, []Candidate{{Hash: "abc123", Name: "Movie.2024.1080p.WEB-DL", ContentPath: video}})
	if n != 1 {
		t.Fatalf("Process imported %d, want 1", n)
	}
	select {
	case ev := <-events:
		data, ok := ev.Data.(map[string]any)
		if !ok {
			t.Fatalf("event data = %T, want map", ev.Data)
		}
		if h, _ := data["hash"].(string); h != "abc123" {
			t.Errorf("event hash = %q, want abc123", h)
		}
	default:
		t.Fatal("no download.imported event received")
	}
}

// When an imported file vanishes from the library (not deliberately deleted — that is
// MarkRemovedByTarget), the next sweep forgets the stale record and imports the still-
// seeding download again. The fresh record starts pending, so the movie is attached
// again too, through the same retrying path as a first import.
func TestVanishedTargetIsReimportedAndAttachedAgain(t *testing.T) {
	ctx := context.Background()
	fa := &fakeAttach{script: []attachAnswer{
		{outcome: Attached},
		{outcome: AttachRetry, err: errors.New("database is locked")},
		{outcome: Attached},
	}}
	m, db := attachManager(t, t.TempDir(), fa)
	cands := oneCandidate(t, "v1", "Dune.2021.1080p.WEB-DL")
	if n := m.Process(ctx, cands); n != 1 {
		t.Fatalf("first import: %d, want 1", n)
	}
	target, _, err := m.repo.targetFor(ctx, "v1")
	if err != nil || target == "" {
		t.Fatalf("no recorded target: %q %v", target, err)
	}
	if row := readAttach(t, db, "v1"); row.state != AttachStateAttached {
		t.Fatalf("first attach: %+v", row)
	}

	// The library file disappears (a cleanup, a lost disk).
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if n := m.Process(ctx, cands); n != 1 {
		t.Fatalf("re-import: %d, want 1", n)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the file wasn't placed again: %v", err)
	}
	row := readAttach(t, db, "v1")
	if row.state != AttachStatePending || row.attempts != 1 || row.release != "Dune.2021.1080p.WEB-DL" {
		t.Fatalf("after re-import with a failed attach: %+v, want a fresh pending row", row)
	}
	if fa.count() != 2 || fa.calls[1].TargetPath != target {
		t.Fatalf("attach calls = %+v, want a second call for %s", fa.calls, target)
	}

	makeDue(t, db, "v1")
	m.RetryPendingAttach(ctx)
	if row := readAttach(t, db, "v1"); row.state != AttachStateAttached {
		t.Fatalf("after the retry: %+v", row)
	}
	if fa.count() != 3 {
		t.Fatalf("attach calls = %d, want 3", fa.count())
	}
	// Settled: the next sweep finds the file in place and does nothing.
	if n := m.Process(ctx, cands); n != 0 || fa.count() != 3 {
		t.Fatalf("a settled re-import ran again: imported %d, attach calls %d", n, fa.count())
	}
}
