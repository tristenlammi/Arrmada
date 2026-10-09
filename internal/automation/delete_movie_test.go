package automation

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// deleteMovieCoord is removeCoord plus an event bus, with one movie that has history, a
// blocklist row and an in-flight grab.
func deleteMovieCoord(t *testing.T) (*Coordinator, *[]removeCall, int64, int64, <-chan eventbus.Event) {
	t.Helper()
	c, calls := removeCoord(t)
	c.bus = eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	events, cancel := c.bus.Subscribe("movie.deleted")
	t.Cleanup(cancel)
	ctx := context.Background()
	mid := addMovie(t, c, "Arrival")
	c.movies.AddEvent(ctx, mid, "grabbed", "Grabbed Arrival")
	if err := c.addBlock(ctx, mid, "Arrival.2016.CAM", "idx", "", "bad"); err != nil {
		t.Fatal(err)
	}
	gid := addGrab(t, c, "movie", mid, "Arrival.2016.1080p.BluRay.x264-GRP", hashA)
	return c, calls, mid, gid, events
}

func countRows(t *testing.T, c *Coordinator, q string, args ...any) int {
	t.Helper()
	var n int
	if err := c.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Deleting a movie with "cancel the download" removes exactly that grab's torrent with its
// partial data, closes the grab as cancelled, and leaves no history or blocklist behind.
func TestDeleteMovieCancelsPendingGrabs(t *testing.T) {
	c, calls, mid, gid, events := deleteMovieCoord(t)
	if err := c.DeleteMovie(context.Background(), mid, DeleteMovieOpts{CancelDownloads: true}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0] != (removeCall{hashA, true}) {
		t.Errorf("remove calls = %+v, want one Remove(hashA, deleteData=true)", *calls)
	}
	if s := grabStatus(t, c, gid); s != grabStatusCancelled {
		t.Errorf("grab status = %q, want cancelled", s)
	}
	if n := countRows(t, c, `SELECT COUNT(*) FROM movie_events WHERE movie_id = ?`, mid); n != 0 {
		t.Errorf("%d movie_events left", n)
	}
	if n := countRows(t, c, `SELECT COUNT(*) FROM blocklist WHERE movie_id = ? AND media_type = 'movie'`, mid); n != 0 {
		t.Errorf("%d blocklist rows left", n)
	}
	if _, err := c.movies.Get(context.Background(), mid); !errors.Is(err, movies.ErrNotFound) {
		t.Errorf("movie should be gone, got %v", err)
	}
	select {
	case ev := <-events:
		if ev.Data.(map[string]any)["id"] != mid {
			t.Errorf("movie.deleted for the wrong id: %+v", ev.Data)
		}
	default:
		t.Error("movie.deleted was not published")
	}
}

// Without "cancel", the torrent is left alone and the grab is marked orphaned — no longer
// pending, so stall detection won't blocklist and re-grab for a movie that's gone.
func TestDeleteMovieWithoutCancelOrphansGrab(t *testing.T) {
	c, calls, mid, gid, _ := deleteMovieCoord(t)
	if err := c.DeleteMovie(context.Background(), mid, DeleteMovieOpts{}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Errorf("no torrent should be removed, got %+v", *calls)
	}
	if s := grabStatus(t, c, gid); s != grabStatusOrphaned {
		t.Errorf("grab status = %q, want orphaned", s)
	}
}

// A grab without a usable hash is never guessed at: it stays orphaned even with cancel.
func TestDeleteMovieNeverRemovesWithoutAValidHash(t *testing.T) {
	c, calls, mid, _, _ := deleteMovieCoord(t)
	noHash := addGrab(t, c, "movie", mid, "Arrival.2016.2160p", "")
	if err := c.DeleteMovie(context.Background(), mid, DeleteMovieOpts{CancelDownloads: true}); err != nil {
		t.Fatal(err)
	}
	for _, call := range *calls {
		if call.hash != hashA {
			t.Errorf("removed a torrent that wasn't the grab's own: %+v", call)
		}
	}
	if s := grabStatus(t, c, noHash); s != grabStatusOrphaned {
		t.Errorf("hashless grab status = %q, want orphaned", s)
	}
}

// When the recycle bin refuses the movie's file, nothing is deleted: the movie stays, no
// torrent is touched, and its grab is pending again.
func TestDeleteMovieFileFailureRestoresGrabs(t *testing.T) {
	c, calls := removeCoord(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	root := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(bin, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.movies = movies.NewService(c.db, nil, nil, root, bin, nil, log)
	ctx := context.Background()
	mid := addMovie(t, c, "Arrival")
	video := filepath.Join(root, "Arrival (2016)", "Arrival (2016).mkv")
	if err := os.MkdirAll(filepath.Dir(video), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.db.Exec(`UPDATE movies SET has_file = 1, movie_file_path = ? WHERE id = ?`, video, mid); err != nil {
		t.Fatal(err)
	}
	gid := addGrab(t, c, "movie", mid, "Arrival.2016.2160p.UHD", hashA)

	err := c.DeleteMovie(ctx, mid, DeleteMovieOpts{DeleteFiles: true, CancelDownloads: true})
	if !errors.Is(err, library.ErrBinRefused) {
		t.Fatalf("err = %v, want a bin refusal", err)
	}
	if len(*calls) != 0 {
		t.Errorf("a torrent was removed although the delete failed: %+v", *calls)
	}
	if s := grabStatus(t, c, gid); s != grabStatusGrabbed {
		t.Errorf("grab status = %q, want grabbed again", s)
	}
	if _, err := c.movies.Get(ctx, mid); err != nil {
		t.Errorf("the movie is gone: %v", err)
	}
	if _, err := os.Stat(video); err != nil {
		t.Error("the file was deleted although the bin refused it")
	}
}

// A download that finishes for a movie deleted meanwhile is held for Review, never
// imported by name into the library.
func TestHoldMovieImportForDeletedMovie(t *testing.T) {
	c, _, mid, _, _ := deleteMovieCoord(t)
	ctx := context.Background()
	if err := c.DeleteMovie(ctx, mid, DeleteMovieOpts{}); err != nil {
		t.Fatal(err)
	}
	reason, hold := c.HoldMovieImport(ctx, hashA, "Arrival.2016.1080p.BluRay.x264-GRP", "/downloads/Arrival")
	if !hold || reason != "Grabbed for a movie you deleted" {
		t.Fatalf("hold = %v, reason = %q", hold, reason)
	}
	revs, err := c.ListReviews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 1 || revs[0].ExpectedID != 0 || revs[0].MediaType != "movie" {
		t.Fatalf("reviews = %+v, want one movie review with no expected id", revs)
	}
	// A second sweep keeps holding it without piling up reviews.
	if _, hold := c.HoldMovieImport(ctx, hashA, "Arrival.2016.1080p.BluRay.x264-GRP", "/downloads/Arrival"); !hold {
		t.Error("the second sweep imported it")
	}
	if revs, _ := c.ListReviews(ctx); len(revs) != 1 {
		t.Errorf("%d reviews after a second sweep, want 1", len(revs))
	}

	// Removing the download from the client settles the review.
	if _, err := c.RemoveDownload(ctx, hashA, "", RemoveKeepFiles, false); err != nil {
		t.Fatal(err)
	}
	if revs, _ := c.ListReviews(ctx); len(revs) != 0 {
		t.Errorf("review still pending after the download was removed: %+v", revs)
	}
}
