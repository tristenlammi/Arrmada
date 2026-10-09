package automation

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/store"
)

// upgradeGrabFixture is a coordinator over a temp store with one movie whose default
// track holds file A (synthetic, in a temp dir).
func upgradeGrabFixture(t *testing.T) (*Coordinator, context.Context, int64, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	c := &Coordinator{db: st.DB(), log: log, movies: movies.NewService(st.DB(), nil, nil, t.TempDir(), "", nil, log)}
	repo := movies.NewRepo(st.DB())
	m, err := repo.Create(ctx, movies.Movie{TMDBID: 10, Title: "Movie", Year: 2010, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(t.TempDir(), "Movie (2010)", "Movie.2010.1080p.mkv")
	if err := repo.SetFile(ctx, m.ID, a); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSourceRelease(ctx, m.ID, "Movie.2010.1080p.WEB-DL.x264-OLD"); err != nil {
		t.Fatal(err)
	}
	return c, ctx, m.ID, a
}

// addMovieGrab records a pending movie grab for the default track.
func addMovieGrab(t *testing.T, c *Coordinator, movieID int64, title, hash, replaces string) int64 {
	t.Helper()
	res, err := c.db.Exec(`INSERT INTO grabs (movie_id, version_id, title, indexer, media_type, info_hash, replaces_path)
		VALUES (?, 0, ?, 'Fake', 'movie', ?, ?)`, movieID, title, hash, replaces)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func loadGrab(t *testing.T, c *Coordinator, id int64) grab {
	t.Helper()
	g, err := scanGrab(c.db.QueryRow(`SELECT `+grabCols+` FROM grabs WHERE id = ?`, id))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// An upgrade grab made while the track holds file A hasn't landed while A is still there;
// it has once the track holds B.
func TestUpgradeGrabNotMarkedImportedEarly(t *testing.T) {
	c, ctx, id, a := upgradeGrabFixture(t)
	gid := addMovieGrab(t, c, id, "Movie.2010.2160p.UHD.BluRay.x265-NEW", "", a)
	if c.movieHasFileFor(ctx, loadGrab(t, c, gid)) {
		t.Fatal("the upgrade counted as landed while the track still holds the file it replaces")
	}
	b := filepath.Join(filepath.Dir(a), "Movie.2010.2160p.mkv")
	if err := movies.NewRepo(c.db).SetFile(ctx, id, b); err != nil {
		t.Fatal(err)
	}
	if !c.movieHasFileFor(ctx, loadGrab(t, c, gid)) {
		t.Fatal("the upgrade's own file landed, but it doesn't count")
	}
}

// An upgrade that lands at the same path counts once the track records its release.
func TestSameNameUpgradeFlipsOnSourceRelease(t *testing.T) {
	c, ctx, id, a := upgradeGrabFixture(t)
	gid := addMovieGrab(t, c, id, "Movie.2010.2160p.UHD.BluRay.x265-NEW", "", a)
	if c.movieHasFileFor(ctx, loadGrab(t, c, gid)) {
		t.Fatal("landed before anything changed")
	}
	if err := movies.NewRepo(c.db).SetSourceRelease(ctx, id, "Movie.2010.2160p.UHD.BluRay.x265-NEW"); err != nil {
		t.Fatal(err)
	}
	if !c.movieHasFileFor(ctx, loadGrab(t, c, gid)) {
		t.Fatal("a same-path upgrade recorded as this release should count as landed")
	}
}

// Rows without replaces_path (grabs for a missing file, rows from before the column) keep
// the old rule: a file on the track means landed.
func TestLegacyGrabRowsUnchanged(t *testing.T) {
	c, ctx, id, _ := upgradeGrabFixture(t)
	gid := addMovieGrab(t, c, id, "Movie.2010.2160p.UHD.BluRay.x265-NEW", "", "")
	if !c.movieHasFileFor(ctx, loadGrab(t, c, gid)) {
		t.Fatal("a legacy grab on a track with a file should count as landed")
	}
	if err := movies.NewRepo(c.db).ClearFile(ctx, id); err != nil {
		t.Fatal(err)
	}
	if c.movieHasFileFor(ctx, loadGrab(t, c, gid)) {
		t.Fatal("no file, not landed")
	}
}

// An import whose torrent name differs from the tracker's title still closes out its own
// grab, by info hash — and leaves a sibling grab alone.
func TestWatchImportsMarksMovieGrabByHash(t *testing.T) {
	c, ctx, id, a := upgradeGrabFixture(t)
	const hash = "0123456789abcdef0123456789abcdef01234567"
	gid := addMovieGrab(t, c, id, "Movie (2010) [2160p UHD BluRay DD+ 7.1]", hash, a)
	sibling := addMovieGrab(t, c, id, "Movie.2010.720p.WEB-DL", "fedcba9876543210fedcba9876543210fedcba98", "")
	newFile := filepath.Join(filepath.Dir(a), "Movie (2010) Bluray-2160p.mkv")
	if err := os.MkdirAll(filepath.Dir(newFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := c.AttachMovieImport(ctx, library.ImportRecord{
		Hash: hash, TargetPath: newFile, ReleaseName: "Movie.2010.2160p.UHD.BluRay.EAC3.7.1.x265-GRP", Title: "Movie", Year: 2010,
	})
	if err != nil || out != library.Attached {
		t.Fatalf("attach: %v %v", out, err)
	}
	if got := loadGrab(t, c, gid).Status; got != grabStatusImported {
		t.Errorf("grab status = %q, want imported (by hash)", got)
	}
	if got := loadGrab(t, c, sibling).Status; got != grabStatusGrabbed {
		t.Errorf("sibling grab status = %q, want still grabbed", got)
	}
}
