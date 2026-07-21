package library

import (
	"context"
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
	if err := m.repo.record(ctx, ImportRecord{Hash: "h1", TargetPath: weird, Title: "Movie"}); err != nil {
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
	if err := m.repo.record(ctx, ImportRecord{Hash: "h2", TargetPath: filepath.Join(lib, "gone.mkv"), Title: "Movie"}); err != nil {
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
