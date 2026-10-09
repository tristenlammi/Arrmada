package automation

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/store"
)

// attachTestCoord is a store-backed coordinator with movies and a live bus.
func attachTestCoord(t *testing.T) (*Coordinator, *movies.Service, *eventbus.Bus, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := eventbus.New(log)
	mv := movies.NewService(st.DB(), nil, nil, t.TempDir(), "", bus, log)
	c := &Coordinator{db: st.DB(), log: log, movies: mv, bus: bus}
	return c, mv, bus, st
}

func writeVideo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The grab row carrying the download's hash decides the movie — even when the import's
// title would match nothing — and attaching marks the movie downloaded, flips the grab to
// imported and announces movie.downloaded.
func TestAttachMovieImportByGrabHash(t *testing.T) {
	ctx := context.Background()
	c, mv, bus, _ := attachTestCoord(t)
	mustExec(t, c, `INSERT INTO movies (id, tmdb_id, title, year, monitored) VALUES (7, 438631, 'Dune', 2021, 1)`)
	mustExec(t, c, `INSERT INTO grabs (movie_id, version_id, title, indexer, quality_profile, media_type, info_hash)
		VALUES (7, 0, 'Dune.2021.2160p.WEB-DL.DV-GRP', 'idx', '', 'movie', 'ABCDEF01')`)
	events, cancel := bus.Subscribe("movie.downloaded")
	defer cancel()

	target := filepath.Join(t.TempDir(), "Dune (2021)", "Dune (2021).mkv")
	writeVideo(t, target)
	out, err := c.AttachMovieImport(ctx, library.ImportRecord{
		Hash: "abcdef01", TargetPath: target, Title: "Something Else Entirely",
		ReleaseName: "Dune.2021.2160p.WEB-DL.DV-GRP", Year: 2021,
	})
	if err != nil || out != library.Attached {
		t.Fatalf("attach = %v, %v; want Attached", out, err)
	}
	m, err := mv.Get(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasFile || m.MovieFilePath != target {
		t.Fatalf("movie not attached: has_file=%v path=%q", m.HasFile, m.MovieFilePath)
	}
	var status string
	if err := c.db.QueryRow(`SELECT status FROM grabs WHERE movie_id = 7`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "imported" {
		t.Fatalf("grab status = %q, want imported", status)
	}
	select {
	case ev := <-events:
		data, _ := ev.Data.(map[string]any)
		if id, _ := data["id"].(int64); id != 7 {
			t.Fatalf("movie.downloaded = %+v", ev.Data)
		}
	default:
		t.Fatal("no movie.downloaded published")
	}
}

// Nothing in the library fits: Unmatched, with a reason worth reading.
func TestAttachMovieImportUnmatched(t *testing.T) {
	c, _, _, _ := attachTestCoord(t)
	target := filepath.Join(t.TempDir(), "x.mkv")
	writeVideo(t, target)
	out, err := c.AttachMovieImport(context.Background(), library.ImportRecord{
		Hash: "none", TargetPath: target, Title: "Not In Library", Year: 1999,
	})
	if out != library.Unmatched || err == nil {
		t.Fatalf("attach = %v, %v; want Unmatched with a reason", out, err)
	}
}

// A lower-resolution import over a better file is refused, not retried.
func TestAttachMovieImportRefusedByQualityGate(t *testing.T) {
	ctx := context.Background()
	c, _, _, _ := attachTestCoord(t)
	existing := filepath.Join(t.TempDir(), "Dune (2021)", "Dune.2021.2160p.WEB-DL.mkv")
	writeVideo(t, existing)
	mustExec(t, c, `INSERT INTO movies (id, tmdb_id, title, year, monitored, has_file, movie_file_path, source_release)
		VALUES (7, 438631, 'Dune', 2021, 1, 1, ?, 'Dune.2021.2160p.WEB-DL')`, existing)
	incoming := filepath.Join(t.TempDir(), "Dune.2021.720p.WEB-DL.mkv")
	writeVideo(t, incoming)
	out, err := c.AttachMovieImport(ctx, library.ImportRecord{
		Hash: "low", TargetPath: incoming, Title: "Dune", Year: 2021, ReleaseName: "Dune.2021.720p.WEB-DL",
	})
	if out != library.Refused || err == nil {
		t.Fatalf("attach = %v, %v; want Refused with the reason", out, err)
	}
	if _, statErr := os.Stat(existing); statErr != nil {
		t.Fatalf("the better file was touched: %v", statErr)
	}
}

// Deleting a movie file forgets its import synchronously, so even a bus whose only
// subscriber never drains (every file.removed dropped) can't let the still-seeding
// torrent be imported back.
func TestDeleteFileForgetsImportWithSaturatedBus(t *testing.T) {
	ctx := context.Background()
	c, mv, bus, st := attachTestCoord(t)
	lib := t.TempDir()
	imports := library.NewManager(st.DB(), lib, bus, c.log)
	mv.SetOnFileRemoved(func(ctx context.Context, path string) {
		if err := imports.MarkRemovedByTarget(ctx, path); err != nil {
			t.Errorf("forget import: %v", err)
		}
	})

	// A subscriber that never reads, its buffer already full.
	stuck, cancel := bus.Subscribe("file.removed")
	defer cancel()
	for len(stuck) < cap(stuck) {
		bus.Publish("file.removed", map[string]any{"path": "/elsewhere"})
	}

	target := filepath.Join(lib, "Dune (2021)", "Dune (2021).mkv")
	writeVideo(t, target)
	mustExec(t, c, `INSERT INTO movies (id, tmdb_id, title, year, monitored, has_file, movie_file_path)
		VALUES (7, 438631, 'Dune', 2021, 1, 1, ?)`, target)
	mustExec(t, c, `INSERT INTO imports (download_hash, source_path, target_path, title, size_bytes)
		VALUES ('seed1', '/downloads/Dune.mkv', ?, 'Dune', 1000)`, target)

	if err := mv.DeleteFile(ctx, 7); err != nil {
		t.Fatal(err)
	}
	var removed int
	if err := c.db.QueryRow(`SELECT removed FROM imports WHERE download_hash = 'seed1'`).Scan(&removed); err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatal("the import wasn't forgotten — its torrent would be imported straight back")
	}
}
