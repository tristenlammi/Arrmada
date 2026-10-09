package automation

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

// brokenBinDir is a recycle-bin path that is a regular file, so nothing can be created
// under it on any OS. Every test here uses temp dirs and synthetic files only.
func brokenBinDir(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("a file, not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeSized(t *testing.T, p string, size int64) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	// Truncate makes a sparse file: big enough to count as a video, no real disk used.
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return p
}

// Deleting a duplicate episode copy through a bin that refuses it leaves the copy in place
// and returns the refusal — it never falls back to a permanent delete.
func TestDeleteSeriesDuplicateRefuses(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	root := t.TempDir()
	svc := series.NewService(st.DB(), nil, root, log)
	c := &Coordinator{db: st.DB(), log: log}
	c.SetSeries(svc, library.NewImporter(root, log))

	res, err := st.DB().ExecContext(ctx, `INSERT INTO series (tmdb_id, title, year, monitored) VALUES (7, 'The Bear', 2022, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO seasons (series_id, season_number, monitored) VALUES (?, 1, 1)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO episodes (series_id, season_number, episode_number, monitored) VALUES (?, 1, 1, 1)`, id); err != nil {
		t.Fatal(err)
	}
	season := filepath.Join(root, "The Bear (2022)", "Season 1")
	keep := writeSized(t, filepath.Join(season, "The Bear - S01E01 - 1080p WEB-DL.mkv"), 80<<20)
	extra := writeSized(t, filepath.Join(season, "The Bear - S01E01 - 720p WEB-DL.mkv"), 60<<20)
	if err := svc.MarkEpisodeImported(ctx, id, 1, 1, keep, 80<<20); err != nil {
		t.Fatal(err)
	}

	c.SetRecycleDir(brokenBinDir(t))
	if err := c.DeleteSeriesDuplicate(ctx, id, extra); !errors.Is(err, library.ErrBinRefused) {
		t.Fatalf("err = %v, want a bin refusal", err)
	}
	if _, err := os.Stat(extra); err != nil {
		t.Fatal("the duplicate was deleted although the bin refused it")
	}

	// A working bin takes it.
	bin := filepath.Join(t.TempDir(), "bin")
	c.SetRecycleDir(bin)
	if err := c.DeleteSeriesDuplicate(ctx, id, extra); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bin, "Season 1", filepath.Base(extra))); err != nil {
		t.Errorf("the duplicate should be in the bin: %v", err)
	}
}

// Deleting a book's files through a bin that refuses them keeps every file and the
// edition's record, for single files and multi-file audiobooks alike.
func TestRemoveBookFileRefuses(t *testing.T) {
	c, svc, ctx := bookTestCoord(t)
	added, _ := svc.AddWorks(ctx, []metadata.BookResult{{Key: "OL1W", Title: "Dune", Author: "Frank Herbert"}}, "", true)
	if len(added) != 1 {
		t.Fatalf("added %d books", len(added))
	}
	id := added[0].ID
	dir := filepath.Join(t.TempDir(), "Frank Herbert", "Dune")
	epub := writeSized(t, filepath.Join(dir, "Dune.epub"), 10)
	audioDir := filepath.Join(dir, "audio")
	parts := []string{
		writeSized(t, filepath.Join(audioDir, "01.mp3"), 10),
		writeSized(t, filepath.Join(audioDir, "02.mp3"), 10),
	}
	if err := svc.MarkImported(ctx, id, books.KindEbook, epub, "epub", 10, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkImported(ctx, id, books.KindAudiobook, audioDir, "mp3", 20, 2); err != nil {
		t.Fatal(err)
	}

	c.SetRecycleDir(brokenBinDir(t))
	for _, kind := range []string{books.KindEbook, books.KindAudiobook} {
		if err := c.DeleteBookEdition(ctx, id, kind); !errors.Is(err, library.ErrBinRefused) {
			t.Errorf("%s: err = %v, want a bin refusal", kind, err)
		}
	}
	for _, p := range append([]string{epub}, parts...) {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was deleted although the bin refused it", filepath.Base(p))
		}
	}
	b, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if b.Ebook == nil || b.Audiobook == nil {
		t.Error("an edition was forgotten although its files are still there")
	}

	// The bin deliberately off still deletes.
	c.SetRecycleDir("")
	if err := c.DeleteBookEdition(ctx, id, books.KindEbook); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(epub); !os.IsNotExist(err) {
		t.Error("with the bin off the ebook should be deleted")
	}
}
