package automation

import (
	"reflect"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/series"
)

// Season 0 used to mean "the whole show", so a Specials search or grab took any TV
// release — a box set included. Now it takes only releases tagged S00, and only the
// special asked for.
func TestSeriesReleaseMatchesSpecials(t *testing.T) {
	cases := []struct {
		name    string
		episode int
		want    bool
	}{
		{"Show.S00E05.1080p.WEB-DL-GRP", 5, true},
		{"Show.S00E05.1080p.WEB-DL-GRP", 0, true},
		{"Show 0x05 720p", 5, true},
		{"Show.S00E06.1080p", 5, false},
		{"Show.S01E05.1080p", 5, false},
		{"Show.Complete.Series.1080p", 5, false},
		{"Show.S01-S03.1080p", 5, false},
		{"Show.S01-S03.1080p", 0, false},
		{"[Grp] Show - 05 [1080p]", 5, false},
		{"[Grp] Show - 05 [1080p]", 0, false},
	}
	for _, c := range cases {
		if got := seriesReleaseMatches(parser.Parse(c.name), 0, c.episode); got != c.want {
			t.Errorf("%s (S00E%02d): %v, want %v", c.name, c.episode, got, c.want)
		}
	}
	// The whole show is now the negative season, and still takes any TV release.
	for _, n := range []string{"Show.Complete.Series.1080p", "Show.S01E05.1080p", "Show.S00E05.1080p"} {
		if !seriesReleaseMatches(parser.Parse(n), -1, 0) {
			t.Errorf("whole show rejected %s", n)
		}
	}
	// Season 1 is unchanged: its pack and episodes, nothing from Specials.
	if !seriesReleaseMatches(parser.Parse("Show.S01.1080p"), 1, 5) || seriesReleaseMatches(parser.Parse("Show.S00E05.1080p"), 1, 5) {
		t.Error("Season 1 matching changed")
	}
}

// A Specials browse names the one special asked for, or the aired ones still missing,
// capped so a show with dozens of specials can't flood an indexer.
func TestSpecialsToQuery(t *testing.T) {
	var eps []series.Episode
	for e := 1; e <= 8; e++ {
		eps = append(eps, series.Episode{SeasonNumber: 0, EpisodeNumber: e, AirDate: "2020-01-01", HasFile: e == 2})
	}
	eps = append(eps, series.Episode{SeasonNumber: 0, EpisodeNumber: 9}) // no air date
	s := series.Series{Seasons: []series.Season{{SeasonNumber: 0, Episodes: eps}, {SeasonNumber: 1, Episodes: []series.Episode{{SeasonNumber: 1, EpisodeNumber: 1, AirDate: "2020-01-01"}}}}}
	if got := specialsToQuery(s, 0); !reflect.DeepEqual(got, []int{1, 3, 4, 5, 6}) {
		t.Errorf("browse = %v, want the first five missing aired specials", got)
	}
	if got := specialsToQuery(s, 9); !reflect.DeepEqual(got, []int{9}) {
		t.Errorf("one special = %v, want [9]", got)
	}
}

// A special's Grab never reaches past its episode: specials are episode-only scopes, and
// the planner refuses packs for them.
func TestPlanSpecialTakesOnlyItsEpisode(t *testing.T) {
	cover := func(r parser.Release, needed map[epKey]bool) []epKey {
		if !isSpecialRelease(r, 0) {
			return nil
		}
		return coveredBy(r, needed)
	}
	var calls []string
	left := planSeriesGrabs(planInput{
		eligible:      evals("Show.Complete.Series.1080p", "[Grp] Show - 05 [1080p]", "Show.S00E04.1080p", "Show.S00E05.720p"),
		wanted:        []epKey{{0, 5}},
		seriesSeasons: map[int]bool{1: true, 2: true},
		counts:        map[int]int{1: 10, 2: 10},
		ended:         true,
		opts:          planOpts{Scoped: true, EpisodesOnly: true},
		cover:         cover,
		try:           func(name, _ string) bool { calls = append(calls, name); return true },
	})
	if !reflect.DeepEqual(calls, []string{"Show.S00E05.720p"}) || len(left) != 0 {
		t.Errorf("grabbed %v (left %v), want only Show.S00E05.720p", calls, left)
	}
}
