package automation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/series"
)

// lifecycleHarness is the stall harness (real download service over a fake qBittorrent)
// with a series that has episodes, an importer, and a recorder for torrent removals.
type lifecycleHarness struct {
	*stallHarness
	seriesID int64
	removed  *[]removeCall
}

func newLifecycleHarness(t *testing.T) *lifecycleHarness {
	t.Helper()
	h := newStallHarness(t)
	root := t.TempDir()
	meta := &importMeta{d: metadata.SeriesDetails{
		SeriesResult:    metadata.SeriesResult{TMDBID: 3, Title: "Show"},
		NumberingSource: "tmdb",
		Seasons: []metadata.SeasonDetails{{SeasonNumber: 1, Episodes: []metadata.EpisodeDetails{
			{EpisodeNumber: 1, AirDate: "2020-01-01"}, {EpisodeNumber: 2, AirDate: "2020-01-08"},
		}}},
	}}
	h.c.series = series.NewService(h.c.db, meta, root, h.c.log)
	h.c.imp = library.NewImporter(root, h.c.log)
	s, err := h.c.series.Add(h.ctx, 3, "", true)
	if err != nil {
		t.Fatal(err)
	}
	removed := &[]removeCall{}
	h.c.removeTorrent = func(_ context.Context, hash string, deleteData bool) error {
		*removed = append(*removed, removeCall{hash, deleteData})
		return nil
	}
	return &lifecycleHarness{stallHarness: h, seriesID: s.ID, removed: removed}
}

// download writes a season pack of sparse 60 MB episode files and returns its folder.
func (h *lifecycleHarness) download(t *testing.T, name string, files ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		fh, err := os.Create(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := fh.Truncate(60 << 20); err != nil {
			t.Fatal(err)
		}
		_ = fh.Close()
	}
	return dir
}

// seededTorrent puts a finished torrent in the fake client that has met any seed goal.
func (h *lifecycleHarness) seededTorrent(hash, name string) {
	defer h.c.downloads.InvalidateSnapshot() // the queue read is cached; show the new torrent
	h.qbit.mu.Lock()
	defer h.qbit.mu.Unlock()
	h.qbit.torrents = append(h.qbit.torrents, map[string]any{
		"hash": hash, "name": name, "state": "uploading", "progress": 1.0, "amount_left": 0,
		"size": 1 << 30, "completed": 1 << 30, "downloaded": 1 << 30, "uploaded": 5 << 30,
		"seeding_time": 400 * 3600, "category": seriesCategory,
	})
}

// seriesGrab records a series grab with a modest seed goal that seededTorrent meets.
func (h *lifecycleHarness) seriesGrab(t *testing.T, title, hash string) int64 {
	t.Helper()
	res, err := h.c.db.Exec(`INSERT INTO grabs (movie_id, title, indexer, stall_minutes, media_type, info_hash, seed_enabled, seed_ratio, seed_hours)
		VALUES (?, ?, 'Fake', 60, 'series', ?, 1, 1.0, 0)`, h.seriesID, title, hash)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (h *lifecycleHarness) hold(t *testing.T, hash, name, contentPath string) int64 {
	t.Helper()
	h.c.addReview(h.ctx, Review{
		Hash: hash, Name: name, ContentPath: contentPath, MediaType: "series",
		ExpectedID: h.seriesID, ExpectedTitle: "Show", Reason: "held for the test",
	})
	var id int64
	if err := h.c.db.QueryRow(`SELECT id FROM import_reviews WHERE hash = ? AND status = 'pending'`, hash).Scan(&id); err != nil {
		t.Fatalf("no pending review: %v", err)
	}
	return id
}

func (h *lifecycleHarness) seriesEvents(t *testing.T) []series.Event {
	t.Helper()
	evs, err := h.c.series.Events(h.ctx, h.seriesID, 50)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func hasEvent(evs []series.Event, event, contains string) bool {
	for _, e := range evs {
		if e.Event == event && strings.Contains(e.Detail, contains) {
			return true
		}
	}
	return false
}

// A series pack imported through Review closes out its grab, so seed cleanup takes the
// torrent (and its data — the library has its own copy) out of the client at its goal and
// says so in the show's history. It used to stay 'grabbed' and seed forever.
func TestSeriesReviewImportClosesOutTheGrab(t *testing.T) {
	h := newLifecycleHarness(t)
	name := "Show.S01.1080p.WEB-DL-GRP"
	hash := hashFor(name)
	gid := h.seriesGrab(t, name, hash)
	dir := h.download(t, name, "Show.S01E01.1080p.WEB-DL-GRP.mkv", "Show.S01E02.1080p.WEB-DL-GRP.mkv")
	rid := h.hold(t, hash, name, dir)
	if s := grabStatus(t, h.c, gid); s != grabStatusHeld {
		t.Fatalf("grab after addReview = %q, want held", s)
	}

	if err := h.c.ImportReview(h.ctx, rid, 0, ""); err != nil {
		t.Fatal(err)
	}
	if s := grabStatus(t, h.c, gid); s != grabStatusImported {
		t.Fatalf("grab after ImportReview = %q, want imported", s)
	}
	var resolution string
	_ = h.c.db.QueryRow(`SELECT resolution FROM import_reviews WHERE id = ?`, rid).Scan(&resolution)
	if resolution != ResolutionImported {
		t.Errorf("review resolution = %q, want imported", resolution)
	}

	h.seededTorrent(hash, name)
	h.c.ManageSeeding(h.ctx)
	if len(*h.removed) != 1 || (*h.removed)[0] != (removeCall{hash, true}) {
		t.Fatalf("removals = %+v, want one Remove(hash, with data)", *h.removed)
	}
	if s := grabStatus(t, h.c, gid); s != grabStatusSeeded {
		t.Errorf("grab after seeding = %q, want seeded", s)
	}
	if !hasEvent(h.seriesEvents(t), "seeded", name) {
		t.Errorf("no 'seeded' event on the show: %+v", h.seriesEvents(t))
	}
}

// A dismissed download leaves the client at its goal, but its files stay: Arrmada never
// imported them and the user said they'd handle them.
func TestDismissedReviewSeedsOutKeepingFiles(t *testing.T) {
	h := newLifecycleHarness(t)
	name := "Show.S01.1080p.WEB-DL-GRP"
	hash := hashFor(name)
	gid := h.seriesGrab(t, name, hash)
	rid := h.hold(t, hash, name, t.TempDir())

	if err := h.c.DismissReview(h.ctx, rid); err != nil {
		t.Fatal(err)
	}
	if s := grabStatus(t, h.c, gid); s != grabStatusDismissed {
		t.Fatalf("grab after DismissReview = %q, want dismissed", s)
	}
	if !hasEvent(h.seriesEvents(t), "dismissed", name) {
		t.Errorf("no 'dismissed' event on the show")
	}
	// A second click (another tab) finds nothing left to act on.
	if err := h.c.DismissReview(h.ctx, rid); err != ErrReviewNotFound {
		t.Errorf("dismissing twice: err = %v, want ErrReviewNotFound", err)
	}

	h.seededTorrent(hash, name)
	h.c.ManageSeeding(h.ctx)
	if len(*h.removed) != 1 || (*h.removed)[0] != (removeCall{hash, false}) {
		t.Fatalf("removals = %+v, want one Remove(hash, keep files)", *h.removed)
	}
	if s := grabStatus(t, h.c, gid); s != grabStatusSeeded {
		t.Errorf("grab after seeding = %q, want seeded", s)
	}
}

// Rejecting removes the download and blocklists it, so its grab is over: 'failed'.
func TestRejectedReviewFailsTheGrab(t *testing.T) {
	h := newLifecycleHarness(t)
	name := "Show.S01.1080p.WEB-DL-GRP"
	hash := hashFor(name)
	gid := h.seriesGrab(t, name, hash)
	rid := h.hold(t, hash, name, t.TempDir())

	if err := h.c.RejectReview(h.ctx, rid); err != nil {
		t.Fatal(err)
	}
	if s := grabStatus(t, h.c, gid); s != grabStatusFailed {
		t.Fatalf("grab after RejectReview = %q, want failed", s)
	}
	if !hasEvent(h.seriesEvents(t), "rejected", name) {
		t.Errorf("no 'rejected' event on the show")
	}
	// Seed cleanup has nothing to do with it.
	h.seededTorrent(hash, name)
	before := len(*h.removed)
	h.c.ManageSeeding(h.ctx)
	if len(*h.removed) != before {
		t.Errorf("seed cleanup removed a rejected download: %+v", *h.removed)
	}
}

// A held release is never grabbed again while its review is pending — not even after the
// one-day window a plain 'grabbed' row gets — and seed cleanup leaves it alone.
func TestHeldGrabBlocksRegrabAndSeedCleanup(t *testing.T) {
	h := newLifecycleHarness(t)
	name := "Show.S01.1080p.WEB-DL-GRP"
	hash := hashFor(name)
	gid := h.seriesGrab(t, name, hash)
	if _, err := h.c.db.Exec(`UPDATE grabs SET grabbed_at = datetime('now', '-25 hours') WHERE id = ?`, gid); err != nil {
		t.Fatal(err)
	}
	h.hold(t, hash, name, t.TempDir())

	pending, err := h.c.pendingSeriesGrabTitles(h.ctx, h.seriesID)
	if err != nil {
		t.Fatal(err)
	}
	if !pending[normTitle(name)] {
		t.Error("a held release dropped out of the pending-grab guard after 25h")
	}
	cleanup, err := h.c.seedCleanupGrabs(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range cleanup {
		if g.ID == gid {
			t.Fatal("a held grab is a seed-cleanup candidate")
		}
	}
	stalled, _ := h.c.pendingGrabs(h.ctx)
	for _, g := range stalled {
		if g.ID == gid {
			t.Fatal("a held grab is watched for stalls")
		}
	}
	h.seededTorrent(hash, name)
	h.c.ManageSeeding(h.ctx)
	if len(*h.removed) != 0 {
		t.Errorf("seed cleanup removed a held download: %+v", *h.removed)
	}
}

// setGrabStatusByHash falls back to the release name only among rows with no hash, and
// never drags a grab that's already closed out back into play.
func TestSetGrabStatusByHashFallbackAndClosedRows(t *testing.T) {
	h := newLifecycleHarness(t)
	ctx := h.ctx
	named := h.seriesGrab(t, "Show.S01E01.1080p.WEB-DL-GRP", "")
	otherHash := h.seriesGrab(t, "Show.S01E01.1080p.WEB-DL-GRP", hashFor("other"))
	done := h.seriesGrab(t, "Show.S01E02.1080p.WEB-DL-GRP", hashFor("done"))
	h.c.setGrabStatus(ctx, done, grabStatusImported)

	moved := h.c.setGrabStatusByHash(ctx, hashFor("unknown"), "Show.S01E01.1080p.WEB-DL-GRP.mkv", "series", grabStatusHeld)
	if len(moved) != 1 || moved[0].ID != named {
		t.Fatalf("moved = %+v, want only the hashless row", moved)
	}
	if s := grabStatus(t, h.c, otherHash); s != grabStatusGrabbed {
		t.Errorf("a row with a different hash was moved by name (%q)", s)
	}
	if got := h.c.setGrabStatusByHash(ctx, hashFor("done"), "", "", grabStatusHeld); len(got) != 0 {
		t.Errorf("an imported grab was moved back: %+v", got)
	}
	if s := grabStatus(t, h.c, done); s != grabStatusImported {
		t.Errorf("imported grab is now %q", s)
	}
}
