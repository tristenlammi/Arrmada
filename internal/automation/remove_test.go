package automation

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

type removeCall struct {
	hash       string
	deleteData bool
}

// removeCoord is a Coordinator over a scratch DB whose download client is a recorder.
func removeCoord(t *testing.T) (*Coordinator, *[]removeCall) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	calls := &[]removeCall{}
	c := &Coordinator{
		db:     st.DB(),
		log:    log,
		movies: movies.NewService(st.DB(), nil, nil, t.TempDir(), "", nil, log),
		series: series.NewService(st.DB(), nil, t.TempDir(), log),
		removeTorrent: func(_ context.Context, hash string, deleteData bool) error {
			*calls = append(*calls, removeCall{hash, deleteData})
			return nil
		},
	}
	return c, calls
}

const hashA = "0123456789abcdef0123456789abcdef01234567"

func addMovie(t *testing.T, c *Coordinator, title string) int64 {
	t.Helper()
	m, err := movies.NewRepo(c.db).Create(context.Background(), movies.Movie{TMDBID: 1, Title: title, Year: 2020, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func addGrab(t *testing.T, c *Coordinator, mediaType string, id int64, title, hash string) int64 {
	t.Helper()
	// Grabbed two hours ago with a one-minute stall window: well past it, so stall
	// detection would act on it if it still read as pending.
	res, err := c.db.Exec(`INSERT INTO grabs (movie_id, title, stall_minutes, media_type, info_hash, grabbed_at)
		VALUES (?, ?, 1, ?, ?, datetime('now', '-2 hours'))`, id, title, mediaType, hash)
	if err != nil {
		t.Fatal(err)
	}
	gid, _ := res.LastInsertId()
	return gid
}

func grabStatus(t *testing.T, c *Coordinator, id int64) string {
	t.Helper()
	var s string
	if err := c.db.QueryRow(`SELECT status FROM grabs WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// "Remove, keep files" removes only the torrent, closes the grab as 'removed', records it
// on the movie, and stall detection then leaves it alone: nothing blocklisted, nothing
// re-grabbed.
func TestRemoveDownloadKeepFiles(t *testing.T) {
	c, calls := removeCoord(t)
	ctx := context.Background()
	mid := addMovie(t, c, "Arrival")
	gid := addGrab(t, c, "movie", mid, "Arrival.2016.1080p.BluRay.x264-GRP", hashA)

	res, err := c.RemoveDownload(ctx, hashA, "", RemoveKeepFiles, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0] != (removeCall{hashA, false}) {
		t.Fatalf("client calls = %+v, want one Remove(hash, keep files)", *calls)
	}
	if res.Kind != "movie" || res.ID != mid || res.Title != "Arrival" || res.Mode != RemoveKeepFiles {
		t.Errorf("result = %+v", res)
	}
	if s := grabStatus(t, c, gid); s != grabStatusRemoved {
		t.Fatalf("grab status = %q, want removed", s)
	}
	evs, _ := c.movies.Events(ctx, mid, 10)
	if len(evs) == 0 || evs[0].Event != "removed" || !strings.Contains(evs[0].Detail, "files kept") {
		t.Errorf("movie history = %+v, want a 'removed … files kept' entry", evs)
	}

	// The torrent is gone from the client now. Stall detection must not read that as a
	// stall (it never gets as far as the client: nothing is pending).
	c.DetectStalled(ctx)
	var blocked int
	_ = c.db.QueryRow(`SELECT COUNT(*) FROM blocklist`).Scan(&blocked)
	if blocked != 0 {
		t.Fatalf("a removed download was blocklisted (%d rows)", blocked)
	}
	if m, _ := c.movies.Get(ctx, mid); !m.Monitored {
		t.Error("keep files without 'stop wanting' must not unmonitor")
	}
}

// After "remove, keep files" the movie is still monitored and missing, so the next search
// or RSS sweep picks the same top-ranked release. The 'removed' grab must hold it: nothing
// may grab it straight back. (Before the fix the row left the per-title guard the moment
// it stopped being 'grabbed'.)
func TestRemovedReleaseIsNotGrabbedAgain(t *testing.T) {
	c, _ := removeCoord(t)
	ctx := context.Background()
	var logs bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&logs, nil))
	c.quality = quality.NewService(c.db)
	sp, err := c.quality.Create(ctx, quality.StoredProfile{MediaType: quality.MediaMovie, Name: "Any"})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(sp.ID, 10)

	const rel = "Arrival.2016.1080p.BluRay.x264-GRP"
	mid := addMovie(t, c, "Arrival")
	m, _ := c.movies.Get(ctx, mid)
	want := []movies.Version{{ID: 1, Label: "Default", QualityProfile: ref, IsDefault: true}}
	byName := map[string]indexer.Release{rel: {Title: rel, Indexer: "idx", DownloadURL: "http://idx/1"}}
	cands := []quality.Candidate{quality.NewCandidate(rel, 8, 50)}
	if d := c.quality.Decide(ctx, ref, cands); d.Winner == nil {
		t.Fatal("setup: the profile must pick the release, or this test proves nothing")
	}

	// tryGrab runs the shared search/RSS grab step and reports whether it went for the
	// release. There are no indexers wired up, so reaching Grab panics; that's caught and
	// read as "it tried".
	tryGrab := func() (tried bool) {
		logs.Reset()
		defer func() {
			if recover() != nil {
				tried = true
			}
		}()
		c.grabMissing(ctx, m, want, byName, cands)
		return strings.Contains(logs.String(), "automation: grabbing")
	}
	if !tryGrab() {
		t.Fatal("control: with no grab on record the release should be grabbed")
	}

	addGrab(t, c, "movie", mid, rel, hashA)
	if _, err := c.RemoveDownload(ctx, hashA, "", RemoveKeepFiles, false); err != nil {
		t.Fatal(err)
	}
	if tryGrab() {
		t.Fatal("the release the user just removed was grabbed again")
	}

	// The hold doesn't last forever: a month on, the release is fair game again.
	if _, err := c.db.Exec(`UPDATE grabs SET grabbed_at = datetime('now', '-31 days')`); err != nil {
		t.Fatal(err)
	}
	if !tryGrab() {
		t.Fatal("a removal from over a month ago still blocks the release")
	}
}

// The same hold applies to series, books and music.
func TestRemovedGrabHoldsEveryKind(t *testing.T) {
	c, _ := removeCoord(t)
	ctx := context.Background()
	for _, kind := range []string{"series", "book", "music"} {
		gid := addGrab(t, c, kind, 7, "Some.Release-GRP", "")
		c.setGrabStatus(ctx, gid, grabStatusRemoved)
	}
	key := normTitle("Some.Release-GRP")
	if !c.pendingSeriesGrabTitles(ctx, 7)[key] {
		t.Error("series guard ignores a removed grab")
	}
	if !c.pendingBookGrabTitles(ctx, 7)[key] {
		t.Error("book guard ignores a removed grab")
	}
	if !c.pendingMusicGrabTitles(ctx, 7)[key] {
		t.Error("music guard ignores a removed grab")
	}
}

// Delete files passes deleteData through, and a grab with no recorded hash is still
// found by its release name.
func TestRemoveDownloadDeleteFilesByName(t *testing.T) {
	c, calls := removeCoord(t)
	ctx := context.Background()
	mid := addMovie(t, c, "Heat")
	gid := addGrab(t, c, "movie", mid, "Heat.1995.2160p.UHD.BluRay-GRP", "")

	if _, err := c.RemoveDownload(ctx, hashA, "Heat.1995.2160p.UHD.BluRay-GRP.mkv", RemoveDeleteFiles, false); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || !(*calls)[0].deleteData {
		t.Fatalf("client calls = %+v, want Remove with data", *calls)
	}
	if s := grabStatus(t, c, gid); s != grabStatusRemoved {
		t.Fatalf("grab status = %q, want removed", s)
	}
	evs, _ := c.movies.Events(ctx, mid, 10)
	if len(evs) == 0 || !strings.Contains(evs[0].Detail, "files deleted") {
		t.Errorf("movie history = %+v, want 'files deleted'", evs)
	}
}

// An imported grab stays imported: removing a seeding torrent doesn't rewrite history.
func TestRemoveDownloadKeepsImportedStatus(t *testing.T) {
	c, _ := removeCoord(t)
	mid := addMovie(t, c, "Alien")
	gid := addGrab(t, c, "movie", mid, "Alien.1979.1080p-GRP", hashA)
	c.setGrabStatus(context.Background(), gid, grabStatusImported)
	if _, err := c.RemoveDownload(context.Background(), hashA, "", RemoveKeepFiles, false); err != nil {
		t.Fatal(err)
	}
	if s := grabStatus(t, c, gid); s != grabStatusImported {
		t.Fatalf("grab status = %q, want imported", s)
	}
}

// Bad input never reaches the client.
func TestRemoveDownloadRefusesBadInput(t *testing.T) {
	c, calls := removeCoord(t)
	for _, h := range []string{"all", "", "a|b", hashA[:39]} {
		if _, err := c.RemoveDownload(context.Background(), h, "", RemoveKeepFiles, false); err == nil {
			t.Errorf("hash %q accepted", h)
		}
	}
	if _, err := c.RemoveDownload(context.Background(), hashA, "", "nuke", false); err == nil {
		t.Error("unknown mode accepted")
	}
	if len(*calls) != 0 {
		t.Fatalf("client was called: %+v", *calls)
	}
}

// "Stop wanting" unmonitors exactly what the download was for: the movie; one season for
// a season pack; one episode for an episode release. Never the whole show.
func TestRemoveDownloadUnmonitor(t *testing.T) {
	c, _ := removeCoord(t)
	ctx := context.Background()

	mid := addMovie(t, c, "Dune")
	addGrab(t, c, "movie", mid, "Dune.2021.1080p-GRP", hashA)
	res, err := c.RemoveDownload(ctx, hashA, "", RemoveKeepFiles, true)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := c.movies.Get(ctx, mid); m.Monitored {
		t.Error("movie still monitored after 'stop wanting'")
	}
	if res.Unmonitored != "the movie" {
		t.Errorf("unmonitored = %q", res.Unmonitored)
	}

	repo := series.NewRepo(c.db)
	sr, err := repo.Create(ctx, series.Series{TMDBID: 7, Title: "The Bear", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	var seasons []series.Season
	for s := 1; s <= 3; s++ {
		sn := series.Season{SeasonNumber: s, Monitored: true}
		for e := 1; e <= 6; e++ {
			sn.Episodes = append(sn.Episodes, series.Episode{SeasonNumber: s, EpisodeNumber: e, Monitored: true})
		}
		seasons = append(seasons, sn)
	}
	if err := repo.InsertSeasons(ctx, sr.ID, seasons); err != nil {
		t.Fatal(err)
	}
	monitored := func() map[[2]int]bool {
		got, err := c.series.Get(ctx, sr.ID)
		if err != nil {
			t.Fatal(err)
		}
		m := map[[2]int]bool{}
		for _, sn := range got.Seasons {
			for _, e := range sn.Episodes {
				m[[2]int{e.SeasonNumber, e.EpisodeNumber}] = e.Monitored
			}
		}
		return m
	}

	// Season pack: only season 2 goes.
	const hashB = "1123456789abcdef0123456789abcdef01234567"
	addGrab(t, c, "series", sr.ID, "The.Bear.S02.1080p.WEB-DL-GRP", hashB)
	if res, err = c.RemoveDownload(ctx, hashB, "", RemoveKeepFiles, true); err != nil {
		t.Fatal(err)
	}
	for k, on := range monitored() {
		if want := k[0] != 2; on != want {
			t.Errorf("after S02 pack: S%02dE%02d monitored=%v, want %v", k[0], k[1], on, want)
		}
	}
	if res.Unmonitored != "season 2" {
		t.Errorf("unmonitored = %q, want season 2", res.Unmonitored)
	}

	// Single episode: only S03E05 goes.
	const hashC = "2123456789abcdef0123456789abcdef01234567"
	addGrab(t, c, "series", sr.ID, "The.Bear.S03E05.1080p.WEB-DL-GRP", hashC)
	if res, err = c.RemoveDownload(ctx, hashC, "", RemoveKeepFiles, true); err != nil {
		t.Fatal(err)
	}
	for k, on := range monitored() {
		want := k[0] != 2 && k != [2]int{3, 5}
		if on != want {
			t.Errorf("after S03E05: S%02dE%02d monitored=%v, want %v", k[0], k[1], on, want)
		}
	}
	if got, _ := c.series.Get(ctx, sr.ID); !got.Monitored {
		t.Error("the show itself must stay monitored")
	}

	// A complete-series pack unmonitors nothing rather than the whole show.
	const hashD = "3123456789abcdef0123456789abcdef01234567"
	addGrab(t, c, "series", sr.ID, "The.Bear.Complete.Series.1080p-GRP", hashD)
	before := monitored()
	if res, err = c.RemoveDownload(ctx, hashD, "", RemoveKeepFiles, true); err != nil {
		t.Fatal(err)
	}
	after := monitored()
	for k := range before {
		if before[k] != after[k] {
			t.Errorf("complete pack changed S%02dE%02d", k[0], k[1])
		}
	}
	if !strings.HasPrefix(res.Unmonitored, "nothing") {
		t.Errorf("unmonitored = %q, want it to say nothing was", res.Unmonitored)
	}
}
