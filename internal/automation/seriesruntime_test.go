package automation

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

const (
	pastDate   = "2001-01-01"
	futureDate = "2999-01-01"
)

// ep builds one episode for a hand-made series.
func ep(season, n, runtime int, airDate string, hasFile bool) series.Episode {
	return series.Episode{SeasonNumber: season, EpisodeNumber: n, Runtime: runtime, AirDate: airDate, HasFile: hasFile}
}

// runtimeShow: season 1 has ten aired 45-minute episodes; season 2 has three aired ones
// (one with no length listed, one already on disk with no air date) and two not yet aired.
func runtimeShow() series.Series {
	var s1, s2 []series.Episode
	for n := 1; n <= 10; n++ {
		s1 = append(s1, ep(1, n, 45, pastDate, false))
	}
	s2 = append(s2,
		ep(2, 1, 50, pastDate, false),
		ep(2, 2, 0, pastDate, false), // TMDB lists no length — the median fills it
		ep(2, 3, 50, "", true),       // no air date, but the file is here, so it exists
		ep(2, 4, 50, futureDate, false),
		ep(2, 5, 50, futureDate, false),
	)
	return series.Series{ID: 1, Title: "Show", Seasons: []series.Season{
		{SeasonNumber: 1, Episodes: s1},
		{SeasonNumber: 2, Episodes: s2},
	}}
}

func TestReleaseRuntime(t *testing.T) {
	s := runtimeShow()
	idx := newRuntimeIndex(s)
	if idx.typical != 45 {
		t.Fatalf("typical = %d, want the median 45", idx.typical)
	}
	c := &Coordinator{}
	ctx := context.Background()
	cases := []struct {
		name string
		want int
	}{
		{"Show.S01E03.1080p.WEB-DL.x264-GRP", 45},
		{"Show.S01E01E02.1080p.WEB-DL.x264-GRP", 90}, // a double episode is both
		{"Show.S02E02.1080p.WEB-DL.x264-GRP", 45},    // no length listed → the median
		{"Show.S01.1080p.BluRay.x264-GRP", 450},      // ten aired episodes
		// Aired or on disk only: S02E04/E05 haven't aired, so the pack can't hold them.
		{"Show.S02.1080p.WEB-DL.x264-GRP", 50 + 45 + 50},
		{"Show.S01-S02.1080p.WEB-DL.x264-GRP", 450 + 145},
		{"Show.Complete.Series.1080p.WEB-DL.x264-GRP", 450 + 145},
		// Unresolvable: a movie-shaped name, and an episode the show doesn't list (the
		// median stands in for its length).
		{"Show.2020.1080p.WEB-DL.x264-GRP", 0},
		{"Show.S01E20.1080p.WEB-DL.x264-GRP", 45},
		// A season the show doesn't have: nothing aired there to sum.
		{"Show.S05.1080p.WEB-DL.x264-GRP", 0},
	}
	for _, tc := range cases {
		if got := c.releaseRuntime(ctx, s, idx, parser.Parse(tc.name)); got != tc.want {
			t.Errorf("%s: runtime %d, want %d", tc.name, got, tc.want)
		}
	}

	// No length known anywhere: the ceiling stays off, exactly as before.
	bare := series.Series{Seasons: []series.Season{{SeasonNumber: 1, Episodes: []series.Episode{ep(1, 1, 0, pastDate, false), ep(1, 2, 0, pastDate, false)}}}}
	bidx := newRuntimeIndex(bare)
	for _, name := range []string{"Show.S01E01.1080p.WEB-DL", "Show.S01.1080p.WEB-DL"} {
		if got := c.releaseRuntime(ctx, bare, bidx, parser.Parse(name)); got != 0 {
			t.Errorf("%s with no lengths known: runtime %d, want 0", name, got)
		}
	}
}

// The acceptance cases: under a 1080p 5–15 Mb/s window, an 8 GB episode is over the
// ceiling, and a ten-episode pack is judged by its summed runtime.
func TestSeriesCandidatesHonourTheCeiling(t *testing.T) {
	s := runtimeShow()
	idx := newRuntimeIndex(s)
	c := &Coordinator{}
	ctx := context.Background()
	p := quality.Profile{Windows: map[string]quality.BitrateWindow{"1080p": {Min: 5, Max: 15}}}
	e := quality.NewDefaultEngine()
	cand := func(name string, gb float64) quality.Candidate {
		return c.newSeriesCandidate(ctx, s, idx, indexer.Release{Title: name, SizeBytes: int64(gb * (1 << 30)), Seeders: 10})
	}

	ev := e.Evaluate(p, cand("Show.S01E01.1080p.BluRay.x265-GRP", 8))
	if ev.Eligible || !strings.HasPrefix(ev.RejectReason, "Over your 15 Mbps ceiling") {
		t.Errorf("8 GB 45-minute episode: eligible=%v reason %q, want over the ceiling", ev.Eligible, ev.RejectReason)
	}
	if ev := e.Evaluate(p, cand("Show.S01.1080p.BluRay.x265-GRP", 40)); !ev.Eligible || ev.Avoided {
		t.Errorf("40 GB ten-episode pack (~12.7 Mb/s): eligible=%v avoided=%v reason %q", ev.Eligible, ev.Avoided, ev.RejectReason)
	}
	if ev := e.Evaluate(p, cand("Show.S01.1080p.BluRay.x265-GRP", 80)); ev.Eligible {
		t.Error("80 GB ten-episode pack should be over the ceiling")
	}

	// The decision follows: a lean in-window WEB-DL beats a bloated BluRay.
	d := e.Decide(p, []quality.Candidate{
		cand("Show.S01E01.1080p.BluRay.x264-GRP", 8),
		cand("Show.S01E01.1080p.WEB-DL.x264-GRP", 2),
	})
	if d.Winner == nil || d.Winner.Candidate.Release.Source != parser.SourceWebDL {
		t.Errorf("winner = %+v, want the in-window WEB-DL", d.Winner)
	}
}

// The upgrade sweep's per-episode choice: candidates carry their runtime, so a release
// inside the window is picked over a larger one above the ceiling.
func TestEpisodeUpgradeStaysUnderTheCeiling(t *testing.T) {
	_, q, ctx := profileStore(t)
	got, err := q.Create(ctx, quality.StoredProfile{
		MediaType: quality.MediaSeries, Name: "TV 1080p", UpgradesEnabled: true,
		AllowedResolutions: []string{"1080p", "720p"},
		Ideal:              &quality.IdealFile{Bitrate: map[string]quality.BitrateWindow{"1080p": {Min: 3, Max: 12}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(got.ID, 10)
	c := &Coordinator{quality: q, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s := runtimeShow()
	rel := func(name string, gb float64) indexer.Release {
		return indexer.Release{Title: name, SizeBytes: int64(gb * (1 << 30)), Seeders: 10}
	}
	byName := map[string]indexer.Release{}
	for _, r := range []indexer.Release{
		rel("Show.S01E01.1080p.BluRay.x264-BIG", 8),    // ~25 Mb/s: over the ceiling
		rel("Show.S01E01.1080p.WEB-DL.x264-GRP", 2),    // ~6.4 Mb/s: inside
		rel("Show.S01E01E02.1080p.BluRay.x264-DBL", 7), // ~11 Mb/s over both episodes: inside
		rel("Show.S01E05.1080p.WEB-DL.x264-OTHER", 2),  // another episode
	} {
		byName[r.Title] = r
	}

	cands := c.episodeUpgradeCandidates(ctx, s, newRuntimeIndex(s), byName, 1, 1)
	if len(cands) != 3 {
		t.Fatalf("got %d candidates, want the three releases holding S01E01", len(cands))
	}
	for _, cd := range cands {
		want := 45
		if strings.Contains(cd.Name, "E01E02") {
			want = 90
		}
		if cd.RuntimeMin != want {
			t.Errorf("%s: runtime %d, want %d", cd.Name, cd.RuntimeMin, want)
		}
	}
	pick, ok := q.UpgradeCandidate(ctx, ref, "Show.S01E01.720p.HDTV.x264-OLD", 0.5, 45, cands)
	if !ok {
		t.Fatal("no upgrade picked")
	}
	if pick.Name == "Show.S01E01.1080p.BluRay.x264-BIG" {
		t.Errorf("picked the over-ceiling release %s", pick.Name)
	}
}

// Anime: absolute numbers and an alias' own numbering resolve to the TMDB episode, and
// that episode's length is what the release is costed against.
func TestReleaseRuntimeResolvesAnimeAndAliases(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	db := st.DB()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO series (id, tmdb_id, title, series_type, monitored) VALUES (1, 30984, 'Bleach', 'anime', 1)`)
	mustExec(`INSERT INTO seasons (series_id, season_number) VALUES (1, 17)`)
	// Two cours with a broadcast break between them; E15 runs long.
	for n := 1; n <= 26; n++ {
		date, rt := "2022-10-11", 24
		if n >= 14 {
			date = "2023-07-08"
		}
		if n == 15 {
			rt = 30
		}
		mustExec(`INSERT INTO episodes (series_id, season_number, episode_number, air_date, runtime, absolute_number, monitored)
			VALUES (1, 17, ?, ?, ?, ?, 1)`, n, date, rt, n)
	}
	svc := series.NewService(db, nil, t.TempDir(), log)
	if _, err := svc.AddAlias(ctx, 1, "BLEACH Thousand-Year Blood War", 17); err != nil {
		t.Fatal(err)
	}
	s, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{series: svc, log: log}
	idx := newRuntimeIndex(s)
	for name, want := range map[string]int{
		"[SubsPlease] Bleach - 15 (1080p) [ABCD1234]":              30, // absolute 15 = S17E15
		"BLEACH Thousand-Year Blood War S02E02 1080p WEB h264-GRP": 30, // cour 2, episode 2 = S17E15
		"[SubsPlease] Bleach - 03 (1080p) [ABCD1234]":              24,
		"BLEACH Thousand-Year Blood War S01 1080p WEB h264-GRP":    13 * 24, // the whole first cour
	} {
		if got := c.releaseRuntime(ctx, s, idx, parser.Parse(name)); got != want {
			t.Errorf("%s: runtime %d, want %d", name, got, want)
		}
	}
}
