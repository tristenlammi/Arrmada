package automation

import (
	"context"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// A series' in-flight scope comes from its acquisitions' recorded scopes: a pack holds its
// season, an episode holds its season, and anything wider holds the show. Finished,
// errored and held downloads hold nothing. The torrent's name plays no part.
func TestSeriesInFlightFromRecord(t *testing.T) {
	s := showWanting()
	acq := func(scope, phase, status string) Acquisition {
		return Acquisition{MediaType: "series", Title: "Whatever.The.Tracker.Called.It", Scope: scope, Phase: phase, Status: status}
	}
	cases := []struct {
		name    string
		acq     Acquisition
		seasons []int
		whole   bool
	}{
		{"pack", acq("S03", "stalled", grabStatusGrabbed), []int{3}, false},
		{"episode", acq("S04E01", "downloading", grabStatusGrabbed), []int{4}, false},
		{"special", acq("S00E05", "", grabStatusGrabbed), []int{0}, false},
		{"several seasons", acq("S01-S04", "downloading", grabStatusGrabbed), nil, true},
		{"complete", acq("complete", "queued", grabStatusGrabbed), nil, true},
		{"absolute", acq("abs", "downloading", grabStatusGrabbed), nil, true},
		{"finished", acq("S03", phaseComplete, grabStatusGrabbed), nil, false},
		{"seeding", acq("S03", "seeding", grabStatusGrabbed), nil, false},
		{"errored", acq("S03", "error", grabStatusGrabbed), nil, false},
		{"in review", acq("S03", "", grabStatusHeld), nil, false},
	}
	for _, tc := range cases {
		seasons, whole, _ := seriesInFlightScope([]Acquisition{tc.acq}, nil, s)
		if whole != tc.whole || len(seasons) != len(tc.seasons) {
			t.Errorf("%s: seasons %v whole %v, want %v whole %v", tc.name, seasons, whole, tc.seasons, tc.whole)
			continue
		}
		for _, sn := range tc.seasons {
			if !seasons[sn] {
				t.Errorf("%s: season %d not held: %v", tc.name, sn, seasons)
			}
		}
	}
	// A row from before scopes were recorded is read off its release title.
	legacy := Acquisition{MediaType: "series", Title: "Show.S03.1080p.WEB-DL.x264-OLD", Status: grabStatusGrabbed}
	if seasons, whole, _ := seriesInFlightScope([]Acquisition{legacy}, nil, s); whole || !seasons[3] || len(seasons) != 1 {
		t.Errorf("legacy row: %v whole %v, want season 3", seasons, whole)
	}
}

func TestSeriesAcqScope(t *testing.T) {
	s := showWanting()
	for name, want := range map[string]string{
		"Show.S03.1080p.WEB-DL.x264-GRP":           "S03",
		"Show.S03E05.1080p.WEB-DL.x264-GRP":        "S03E05",
		"Show.S00E02.Special.1080p.WEB-DL.x264-GR": "S00E02",
		"Show.S01-S04.1080p.BluRay.x264-GRP":       "S01-S04",
		"Show.Complete.Series.1080p.BluRay-GRP":    "complete",
		"[SubsPlease] Show - 137 (1080p)":          "abs",
	} {
		if got := seriesAcqScope(name, s); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

// A season pack in flight, recorded by hash under a torrent name that doesn't even parse
// as the show, still holds its season: the sweep grabs the newly aired S04E01 and nothing
// for S03. Before, the name didn't match, nothing was held, and S03 was grabbed again.
func TestSeriesPackRecordHoldsItsSeason(t *testing.T) {
	h := newStallHarness(t)
	repo := series.NewRepo(h.c.db)
	sr, err := repo.Create(h.ctx, series.Series{TMDBID: 9, Title: "Show", Year: 2020, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	ep := func(s, e int) series.Episode {
		return series.Episode{SeasonNumber: s, EpisodeNumber: e, AirDate: "2021-01-01", Monitored: true}
	}
	if err := repo.InsertSeasons(h.ctx, sr.ID, []series.Season{
		{SeasonNumber: 3, Monitored: true, Episodes: []series.Episode{ep(3, 1), ep(3, 2)}},
		{SeasonNumber: 4, Monitored: true, Episodes: []series.Episode{ep(4, 1)}},
	}); err != nil {
		t.Fatal(err)
	}
	const listed = "Show.S03.1080p.WEB-DL.DD+5.1.x264-OLD"
	const torrent = "Totally Different Name [Season Three] 1080p"
	if _, err := h.c.db.Exec(`INSERT INTO grabs (movie_id, title, indexer, media_type, info_hash, acq_scope)
		VALUES (?, ?, 'Fake', 'series', ?, 'S03')`, sr.ID, listed, hashFor(torrent)); err != nil {
		t.Fatal(err)
	}
	h.qbit.stalledTorrent(hashFor(torrent), torrent, seriesCategory)
	h.ix.offer("Show.S03E01.1080p.WEB-DL.x264-AAA", "Show.S04E01.1080p.WEB-DL.x264-CCC")

	h.c.SearchSeriesMissing(h.ctx)
	if calls := h.qbit.callLog(); strings.Join(calls, "|") != "add "+magnetFor("Show.S04E01.1080p.WEB-DL.x264-CCC") {
		t.Fatalf("client calls = %q, want only S04E01 added", calls)
	}
}

// A movie version with a download in flight — under a torrent name that parses to some
// other film — isn't searched again by the sweep, nor re-grabbed by RSS; with nothing in
// flight the same movie is.
func TestMovieSweepsSkipVersionInFlight(t *testing.T) {
	setup := func(t *testing.T, inFlight bool) *stallHarness {
		h := newStallHarness(t)
		if _, err := h.c.db.Exec(`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability)
			VALUES (1, 1, 'Alpha', 2001, 1, 'announced')`); err != nil {
			t.Fatal(err)
		}
		if inFlight {
			const torrent = "Some Other Film (1999) 2160p"
			if _, err := h.c.db.Exec(`INSERT INTO grabs (movie_id, version_id, title, indexer, media_type, info_hash)
				VALUES (1, 0, 'Alpha 2001 2160p UHD BluRay DD+ 7.1', 'Fake', 'movie', ?)`, hashFor(torrent)); err != nil {
				t.Fatal(err)
			}
			h.qbit.stalledTorrent(hashFor(torrent), torrent, "")
		}
		h.ix.offer("Alpha.2001.1080p.WEB-DL.x264-GRP")
		return h
	}

	t.Run("search", func(t *testing.T) {
		h := setup(t, true)
		h.c.SearchMissing(h.ctx)
		if n := h.ix.searchCount(); n != 0 {
			t.Errorf("the indexer was searched %d times for a version already downloading", n)
		}
		out, err := h.c.SearchMovie(h.ctx, 1)
		if err != nil || out.Reason != ReasonAlreadyDownloading || out.Searched {
			t.Errorf("Search now: %+v, %v; want already downloading, no search", out, err)
		}
		if adds := h.adds(); adds != 0 {
			t.Errorf("%d grabs added", adds)
		}
	})
	t.Run("rss", func(t *testing.T) {
		h := setup(t, true)
		h.c.RSSSync(h.ctx)
		if adds := h.adds(); adds != 0 {
			t.Errorf("RSS grabbed %d on top of a version already downloading", adds)
		}
	})
	t.Run("control", func(t *testing.T) {
		h := setup(t, false)
		h.c.SearchMissing(h.ctx)
		if h.ix.searchCount() == 0 || h.adds() != 1 {
			t.Errorf("with nothing in flight: %d searches, %d adds; want a search and one grab", h.ix.searchCount(), h.adds())
		}
	})
}

// grabMissing itself refuses a differently named release for a version already
// downloading — and the stall fail-over, which replaces exactly that grab, still can.
func TestGrabMissingRespectsActiveAcquisition(t *testing.T) {
	h := newStallHarness(t)
	if _, err := h.c.db.Exec(`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability)
		VALUES (1, 1, 'Alpha', 2001, 1, 'announced')`); err != nil {
		t.Fatal(err)
	}
	res, err := h.c.db.Exec(`INSERT INTO grabs (movie_id, version_id, title, indexer, media_type, info_hash)
		VALUES (1, 0, 'Alpha.2001.2160p.UHD.BluRay-OLD', 'Fake', 'movie', 'abc')`)
	if err != nil {
		t.Fatal(err)
	}
	gid, _ := res.LastInsertId()
	m, err := h.c.movies.Get(h.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	other := "Alpha.2001.1080p.WEB-DL.x264-NEW"
	want := h.c.missingVersions(h.ctx, 1)
	byName := map[string]indexer.Release{other: {Title: other, Indexer: "Fake", DownloadURL: magnetFor(other)}}
	cands := []quality.Candidate{quality.NewCandidate(other, 4, 25)}

	if n := h.c.grabMissing(h.ctx, m, want, byName, cands); n != 0 || h.adds() != 0 {
		t.Fatalf("grabbed %d (adds %d) for a version already downloading", n, h.adds())
	}
	if got := h.c.grabMissingTitlesExcept(context.Background(), m, want, byName, cands, gid); len(got) != 1 {
		t.Errorf("the fail-over replacing grab %d grabbed %v, want the new release", gid, got)
	}
}
