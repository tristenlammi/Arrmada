package automation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/music"
)

// completedTorrent puts a finished download in the fake client, in category, with its
// files at contentPath.
func (h *lifecycleHarness) completedTorrent(name, category, contentPath string) string {
	hash := hashFor(name)
	h.qbit.mu.Lock()
	defer h.qbit.mu.Unlock()
	h.qbit.torrents = append(h.qbit.torrents, map[string]any{
		"hash": hash, "name": name, "state": "uploading", "progress": 1.0, "amount_left": 0,
		"size": 1 << 30, "completed": 1 << 30, "category": category, "content_path": contentPath,
	})
	return hash
}

func reviewFor(t *testing.T, c *Coordinator, hash string) Review {
	t.Helper()
	var id int64
	if err := c.db.QueryRow(`SELECT id FROM import_reviews WHERE hash = ? AND status = 'pending'`, hash).Scan(&id); err != nil {
		t.Fatalf("no pending review for %s: %v", hash, err)
	}
	r, err := c.GetReview(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Every place that holds a download for review says which kind of problem it is.
func TestReviewReasonCodes(t *testing.T) {
	h := newLifecycleHarness(t)
	ctx := h.ctx
	held, cancel := h.c.bus.Subscribe("import.held")
	defer cancel()

	// Movies: a mismatch, one grabbed for a movie since deleted, and a stuck import.
	arrival := h.addMovie(t, 10, "Arrival", 2016)
	mHash := hashFor("Interstellar.2014.1080p")
	addGrab(t, h.c, "movie", arrival, "Interstellar.2014.1080p", mHash)
	h.c.HoldMovieImport(ctx, mHash, "Interstellar.2014.1080p", t.TempDir())
	if r := reviewFor(t, h.c, mHash); r.ReasonCode != ReasonMismatch {
		t.Errorf("movie mismatch: code %q", r.ReasonCode)
	}
	select {
	case ev := <-held:
		p, _ := ev.Data.(map[string]any)
		if p["reason_code"] != ReasonMismatch || p["kind"] != "movie" || p["expected_id"] != arrival || p["id"] == nil {
			t.Errorf("import.held payload = %+v", p)
		}
	case <-time.After(2 * time.Second):
		t.Error("no import.held event")
	}
	gone := hashFor("Gone.Movie.2020.1080p")
	addGrab(t, h.c, "movie", 9999, "Gone.Movie.2020.1080p", gone)
	h.c.HoldMovieImport(ctx, gone, "Gone.Movie.2020.1080p", t.TempDir())
	if r := reviewFor(t, h.c, gone); r.ReasonCode != ReasonUnmatched {
		t.Errorf("deleted movie: code %q", r.ReasonCode)
	}
	stuck := hashFor("Arrival.2016.2160p")
	h.c.HandleMovieImportStuck(ctx, stuck, "Arrival.2016.2160p", t.TempDir(), 5, os.ErrPermission)
	if r := reviewFor(t, h.c, stuck); r.ReasonCode != ReasonImportFailed {
		t.Errorf("stuck import: code %q", r.ReasonCode)
	}

	// Series: a pack whose files carry no numbering, and one grabbed for Show that's
	// another show.
	numbered := h.download(t, "Show.S01.1080p.WEB-DL-GRP", "Show - The Pilot.mkv", "Show - The Return.mkv")
	nHash := h.completedTorrent("Show.S01.1080p.WEB-DL-GRP", seriesCategory, numbered)
	h.seriesGrab(t, "Show.S01.1080p.WEB-DL-GRP", nHash)
	other := h.download(t, "Other.Thing.S01.1080p.WEB-DL-GRP", "Other.Thing.S01E01.1080p.WEB-DL-GRP.mkv")
	oHash := h.completedTorrent("Other.Thing.S01.1080p.WEB-DL-GRP", seriesCategory, other)
	h.seriesGrab(t, "Other.Thing.S01.1080p.WEB-DL-GRP", oHash)
	h.c.ImportSeriesDownloads(ctx)
	if r := reviewFor(t, h.c, nHash); r.ReasonCode != ReasonNumbering {
		t.Errorf("series numbering: code %q (%s)", r.ReasonCode, r.Reason)
	}
	if r := reviewFor(t, h.c, oHash); r.ReasonCode != ReasonMismatch {
		t.Errorf("series mismatch: code %q", r.ReasonCode)
	}

	// Books: a download that matches no book in the library.
	h.c.books = books.NewService(h.c.db, nil, h.c.log)
	bookDL := h.download(t, "Nobody - Unknown Book (2020) [EPUB]")
	bHash := h.completedTorrent("Nobody - Unknown Book (2020) [EPUB]", bookCategory, bookDL)
	for i := 0; i < unmatchedReviewAfter; i++ {
		h.c.ImportBookDownloads(ctx)
	}
	if r := reviewFor(t, h.c, bHash); r.ReasonCode != ReasonUnmatched {
		t.Errorf("book unmatched: code %q", r.ReasonCode)
	}

	// Music: a download for a library album that holds no audio.
	h.c.music = music.NewService(h.c.db, nil, h.c.log)
	mustExec(t, h.c, `INSERT INTO artists (id, mbid, name) VALUES (1, 'a1', 'Radiohead')`)
	mustExec(t, h.c, `INSERT INTO albums (id, artist_id, mbid, title, year) VALUES (1, 1, 'r1', 'Kid A', 2000)`)
	musicDL := h.download(t, "Radiohead - Kid A (2000) [FLAC]")
	aHash := h.completedTorrent("Radiohead - Kid A (2000) [FLAC]", musicCategory, musicDL)
	for i := 0; i < unmatchedReviewAfter; i++ {
		h.c.ImportMusicDownloads(ctx)
	}
	if r := reviewFor(t, h.c, aHash); r.ReasonCode != ReasonNoMedia {
		t.Errorf("music no media: code %q", r.ReasonCode)
	}
}

// The file list shows what's inside the download and what each name says, and never
// anything outside it — a symlink planted in the download is neither listed nor followed.
func TestReviewFilesStaysInsideTheDownload(t *testing.T) {
	c, _, ctx := reviewTestCoord(t)
	dl := filepath.Join(t.TempDir(), "Show.S01.1080p")
	if err := os.MkdirAll(filepath.Join(dl, "Extras"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"Show.S01E02.mkv", "Show.S01E01E02.mkv", "Extras/notes.txt"} {
		if err := os.WriteFile(filepath.Join(dl, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	linked := os.Symlink(outside, filepath.Join(dl, "escape")) == nil
	_ = os.Symlink(filepath.Join(outside, "secret.mkv"), filepath.Join(dl, "secret.mkv"))

	id := seedReview(t, c, "series", 1, dl)
	files, truncated, err := c.ReviewFiles(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(files) != 3 {
		t.Fatalf("files = %+v (truncated %v), want the three real files", files, truncated)
	}
	for _, f := range files {
		if f.RelPath == "escape/secret.mkv" || f.RelPath == "secret.mkv" {
			t.Errorf("listed a symlinked file: %+v", f)
		}
	}
	byPath := map[string]ReviewFile{}
	for _, f := range files {
		byPath[f.RelPath] = f
	}
	if g := byPath["Show.S01E01E02.mkv"].Guess; g.Season != 1 || len(g.Episodes) != 2 || !byPath["Show.S01E01E02.mkv"].Video {
		t.Errorf("double-episode guess = %+v", byPath["Show.S01E01E02.mkv"])
	}
	if f, ok := byPath["Extras/notes.txt"]; !ok || f.Video {
		t.Errorf("nested non-video file = %+v (present %v)", f, ok)
	}
	if !linked {
		t.Log("symlinks unavailable here; the escape case ran on Linux CI")
	}
}

// Retry import clears the importer's back-off for the download and resolves the review so
// the sweep tries again; it's refused for a review that isn't an import failure.
func TestRetryReviewImport(t *testing.T) {
	c, _, ctx := reviewTestCoord(t)
	var retried []string
	c.SetImportRetry(func(hash string) { retried = append(retried, hash) })
	mustExec(t, c, `INSERT INTO import_reviews (hash, name, media_type, expected_id, reason, reason_code) VALUES ('h1', 'X', 'movie', 1, 'Import failed 5 times', 'import_failed')`)
	mustExec(t, c, `INSERT INTO import_reviews (hash, name, media_type, expected_id, reason, reason_code) VALUES ('h2', 'Y', 'movie', 1, 'Grabbed for…', 'mismatch')`)
	mustExec(t, c, `INSERT INTO grabs (movie_id, title, info_hash, status) VALUES (1, 'X', 'h1', 'held')`)

	if err := c.RetryReviewImport(ctx, 2); err == nil {
		t.Error("retrying a mismatch review should be refused")
	}
	if err := c.RetryReviewImport(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if len(retried) != 1 || retried[0] != "h1" {
		t.Errorf("importer back-off cleared for %v, want [h1]", retried)
	}
	var status, resolution, grab string
	_ = c.db.QueryRow(`SELECT status, resolution FROM import_reviews WHERE id = 1`).Scan(&status, &resolution)
	_ = c.db.QueryRow(`SELECT status FROM grabs WHERE info_hash = 'h1'`).Scan(&grab)
	if status != "resolved" || resolution != ResolutionRetried || grab != grabStatusGrabbed {
		t.Errorf("after retry: review %s/%s, grab %s", status, resolution, grab)
	}
	if c.hasReview(ctx, "h1") {
		t.Error("the import sweep would still hold the download")
	}
}
