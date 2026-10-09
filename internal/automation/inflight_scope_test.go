package automation

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/series"
)

func tvItem(name string, progress float64, raw string) download.Item {
	left := int64(0)
	if progress < 1 {
		left = 1 << 30
	}
	return download.Item{Name: name, Category: seriesCategory, Progress: progress, RemainingBytes: left, RawState: raw, State: "downloading"}
}

// showWanting is a show missing all of S03 and the newly aired S04E01.
func showWanting() series.Series {
	ep := func(s, e int) series.Episode {
		return series.Episode{SeasonNumber: s, EpisodeNumber: e, AirDate: "2021-01-01", Monitored: true}
	}
	return series.Series{Title: "Show", Monitored: true, Seasons: []series.Season{
		{SeasonNumber: 3, Monitored: true, Episodes: []series.Episode{ep(3, 1), ep(3, 2)}},
		{SeasonNumber: 4, Monitored: true, Episodes: []series.Episode{ep(4, 1)}},
	}}
}

// A stalled S03 pack holds back S03 only: the sweep's wanted set is exactly the newly
// aired S04E01.
func TestInFlightSeasonPackHoldsOnlyItsSeason(t *testing.T) {
	s := showWanting()
	queue := []download.Item{tvItem("Show.S03.1080p.WEB-DL.x264-OLD", 0.1, "stalledDL")}
	seasons, whole, names := seriesInFlightScope(queue, s)
	if whole || len(seasons) != 1 || !seasons[3] || len(names) != 1 {
		t.Fatalf("scope = %v whole=%v names=%v, want just season 3", seasons, whole, names)
	}
	c := &Coordinator{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	only, ok := c.notInFlight(s, seasons, names, "series")
	if !ok || len(only) != 1 || only[0] != (epKey{4, 1}) {
		t.Fatalf("only = %v (ok %v), want exactly [S04E01]", only, ok)
	}

	// With S04E01 on disk, everything missing is in the season still downloading.
	s.Seasons[1].Episodes[0].HasFile = true
	if only, ok := c.notInFlight(s, seasons, names, "series"); ok {
		t.Fatalf("only = %v, want nothing to search", only)
	}
	// And nothing in flight holds nothing back.
	if only, ok := c.notInFlight(s, nil, nil, "series"); !ok || only != nil {
		t.Fatalf("nothing in flight: only = %v ok = %v, want nil, true", only, ok)
	}
}

// Packs that cover more than a season — or whose season can't be read off the name —
// still hold the whole show, so nothing stacks on top of them.
func TestInFlightWideOrUnreadablePacksHoldTheShow(t *testing.T) {
	anime := showWanting()
	anime.SeriesType = series.SeriesTypeAnime
	elementary := showWanting()
	elementary.Title = "Elementary"
	for _, tc := range []struct {
		name string
		s    series.Series
	}{
		{"Elementary S01-07 Complete 1080p WEB x264-Mixed-TL", elementary},
		{"Show.S01-S04.1080p.WEB-DL.x264-GRP", showWanting()},
		{"[SubsPlease] Show - 137 (1080p) [ABCD1234]", anime},
		// An anime cour numbered as a season the listing doesn't have.
		{"Show.S07.1080p.WEB-DL.x264-GRP", anime},
	} {
		_, whole, names := seriesInFlightScope([]download.Item{tvItem(tc.name, 0.3, "downloading")}, tc.s)
		if !whole || len(names) != 1 {
			t.Errorf("%s: whole = %v names = %v, want the whole show held", tc.name, whole, names)
		}
	}
}

// A torrent in an error state never blocks its show; finished torrents and other shows'
// don't either.
func TestInFlightIgnoresErroredFinishedAndOtherShows(t *testing.T) {
	s := showWanting()
	errored := tvItem("Show.S03.1080p.WEB-DL.x264-OLD", 0.4, "error")
	missing := tvItem("Show.S04E01.1080p.WEB-DL.x264-OLD", 0.4, "missingFiles")
	plainErr := tvItem("Show.S04.1080p.WEB-DL.x264-OLD", 0.4, "")
	plainErr.State = "error"
	seeding := tvItem("Show.S03E01.1080p.WEB-DL.x264-GRP", 1, "uploading")
	other := tvItem("Other.Show.S03.1080p.WEB-DL.x264-GRP", 0.2, "downloading")
	seasons, whole, names := seriesInFlightScope([]download.Item{errored, missing, plainErr, seeding, other}, s)
	if whole || len(seasons) != 0 || len(names) != 0 {
		t.Fatalf("scope = %v whole=%v names=%v, want nothing in flight", seasons, whole, names)
	}
	if got := seriesInFlight([]download.Item{errored}, "Show"); got != "" {
		t.Errorf("the show-level check counts an errored torrent: %q", got)
	}
}

// A pack named under one of the show's aliases counts as in flight for it: a title-only
// alias by the release's own season, a season-pinned alias by the season it's pinned to.
func TestInFlightRecognisesAliasNamedPacks(t *testing.T) {
	s := showWanting()
	s.Title = "Attack on Titan"
	s.Aliases = []series.Alias{{Title: "Shingeki no Kyojin"}, {Title: "Attack on Titan The Final Season", TMDBSeason: 4}}

	seasons, whole, _ := seriesInFlightScope([]download.Item{tvItem("Shingeki.no.Kyojin.S02.1080p.BluRay.x264-GRP", 0.5, "downloading")}, s)
	if whole || !seasons[2] || len(seasons) != 1 {
		t.Errorf("title-only alias S02 pack: scope = %v whole = %v, want season 2", seasons, whole)
	}
	seasons, whole, _ = seriesInFlightScope([]download.Item{tvItem("Attack.on.Titan.The.Final.Season.S01.1080p.WEB-DL.x264-GRP", 0.5, "downloading")}, s)
	if whole || !seasons[4] || len(seasons) != 1 {
		t.Errorf("season-pinned alias pack: scope = %v whole = %v, want season 4", seasons, whole)
	}
}

// End to end through the real services: with an S03 pack stalled in the client, the
// missing sweep and RSS both grab the newly aired S04E01 — and neither grabs S03 again.
func TestSweepsGrabOtherSeasonsPastAStalledPack(t *testing.T) {
	for _, sweep := range []string{"missing", "rss"} {
		t.Run(sweep, func(t *testing.T) {
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
			const pack = "Show.S03.1080p.WEB-DL.x264-OLD"
			const s03 = "Show.S03E01.1080p.WEB-DL.x264-AAA"
			const s04 = "Show.S04E01.1080p.WEB-DL.x264-CCC"
			h.qbit.stalledTorrent(hashFor(pack), pack, seriesCategory)
			h.ix.offer(s03, s04)

			if sweep == "missing" {
				h.c.SearchSeriesMissing(h.ctx)
			} else {
				h.c.RSSSyncSeries(h.ctx)
			}
			calls := h.qbit.callLog()
			if strings.Join(calls, "|") != "add "+magnetFor(s04) {
				t.Fatalf("client calls = %q, want only S04E01 added", calls)
			}
		})
	}

	// A multi-season pack in flight still holds the whole show.
	h := newStallHarness(t)
	repo := series.NewRepo(h.c.db)
	sr, err := repo.Create(h.ctx, series.Series{TMDBID: 9, Title: "Show", Year: 2020, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertSeasons(h.ctx, sr.ID, []series.Season{
		{SeasonNumber: 4, Monitored: true, Episodes: []series.Episode{{SeasonNumber: 4, EpisodeNumber: 1, AirDate: "2021-01-01", Monitored: true}}},
	}); err != nil {
		t.Fatal(err)
	}
	const wide = "Show.S01-S03.1080p.BluRay.x264-OLD"
	h.qbit.stalledTorrent(hashFor(wide), wide, seriesCategory)
	h.ix.offer("Show.S04E01.1080p.WEB-DL.x264-CCC")
	h.c.SearchSeriesMissing(h.ctx)
	if n := h.ix.searchCount(); n != 0 {
		t.Errorf("the missing sweep asked the indexer %d times with the whole show in flight, want 0", n)
	}
	h.c.RSSSyncSeries(h.ctx)
	if calls := h.qbit.callLog(); len(calls) != 0 {
		t.Fatalf("a multi-season pack in flight, yet the client got %q", calls)
	}
}
