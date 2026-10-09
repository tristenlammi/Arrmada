package automation

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/series"
)

// scopeShow is a two-season show: Season 1 complete on disk, Season 2 unmonitored as a
// season with a mix of monitored, unmonitored, downloaded and unaired episodes.
func scopeShow() series.Series {
	ep := func(s, e int, monitored, hasFile bool, air string) series.Episode {
		return series.Episode{SeasonNumber: s, EpisodeNumber: e, Monitored: monitored, HasFile: hasFile, AirDate: air}
	}
	return series.Series{Seasons: []series.Season{
		{SeasonNumber: 1, Monitored: true, Episodes: []series.Episode{
			ep(1, 1, true, true, "2020-01-01"), ep(1, 2, true, true, "2020-01-08"),
		}},
		{SeasonNumber: 2, Monitored: false, Episodes: []series.Episode{
			ep(2, 1, true, false, "2021-01-01"),  // wanted
			ep(2, 2, false, false, "2021-01-08"), // not monitored
			ep(2, 3, true, true, "2021-01-15"),   // has a file
			ep(2, 4, true, false, ""),            // no air date → unaired
			ep(2, 5, true, false, "2999-01-01"),  // future
		}},
	}}
}

func TestScopeWantedSkipsEpisodesWithFiles(t *testing.T) {
	s := scopeShow()
	if got := scopeWanted(s, SeriesScope{Season: 1}); len(got) != 0 {
		t.Errorf("a complete season wants %v, want nothing", got)
	}
	if got := scopeWanted(s, SeriesScope{Season: 2, Episode: 3}); len(got) != 0 {
		t.Errorf("an episode with a file wants %v, want nothing", got)
	}
	for _, e := range []int{4, 5} {
		if got := scopeWanted(s, SeriesScope{Season: 2, Episode: e}); len(got) != 0 {
			t.Errorf("unaired E%d wants %v, want nothing", e, got)
		}
	}
}

func TestScopeWantedReplaceIncludesTheEpisode(t *testing.T) {
	got := scopeWanted(scopeShow(), SeriesScope{Season: 1, Episode: 2, Replace: true})
	if !reflect.DeepEqual(got, []epKey{{1, 2}}) {
		t.Errorf("Replace S01E02 wants %v, want just that episode", got)
	}
}

// Naming the season is the say-so for the season's flag, but not for an episode the owner
// switched off. Naming the episode is the say-so for that episode.
func TestScopeWantedSeasonIgnoresSeasonFlag(t *testing.T) {
	s := scopeShow()
	if got := scopeWanted(s, SeriesScope{Season: 2}); !reflect.DeepEqual(got, []epKey{{2, 1}}) {
		t.Errorf("Season 2 wants %v, want only S02E01", got)
	}
	if got := scopeWanted(s, SeriesScope{Season: 2, Episode: 2}); !reflect.DeepEqual(got, []epKey{{2, 2}}) {
		t.Errorf("clicking unmonitored S02E02 wants %v, want it", got)
	}
}

func TestSeriesScopeValidate(t *testing.T) {
	ok := []SeriesScope{{Season: 3}, {Season: 3, Episode: 4}, {Season: 3, Episode: 4, Replace: true},
		{Season: 0, Episode: 5}, {Season: 0, Episode: 5, Replace: true}}
	for _, sc := range ok {
		if err := sc.Validate(); err != nil {
			t.Errorf("%+v: %v", sc, err)
		}
	}
	bad := []SeriesScope{{Season: -1}, {Season: 3, Episode: -1}, {Season: 3, Replace: true}}
	for _, sc := range bad {
		if sc.Validate() == nil {
			t.Errorf("%+v was accepted", sc)
		}
	}
	if err := (SeriesScope{Season: 0, Episode: 0}).Validate(); !errors.Is(err, ErrSpecialsScope) {
		t.Errorf("Specials season grab: %v, want ErrSpecialsScope", err)
	}
}

func TestGrabOutcomeSummary(t *testing.T) {
	// Every indexer failing is not "nothing found".
	failed := GrabOutcome{Searched: true, IndexersFailed: true, IndexerErrors: 3}.Summary()
	if !strings.Contains(failed, "Every indexer failed") || strings.Contains(failed, "found") {
		t.Errorf("all failed = %q", failed)
	}
	// Nothing fit: the reasons, biggest first, with the profile's grouped by first clause.
	var o GrabOutcome
	o.Searched, o.Found, o.WrongShow, o.OutOfScope = true, 40, 2, 20
	o.reject("Over your 20 Mbps ceiling (25.1 Mbps)", "a")
	o.reject("Over your 20 Mbps ceiling (31.0 Mbps)", "b")
	o.reject("Not in profile — 480p", "c")
	got := o.Summary()
	want := "Grabbed nothing · 40 found · 0 fit: 20 other episodes, 2 Over your 20 Mbps ceiling, 2 other shows, 1 Not in profile"
	if got != want {
		t.Errorf("summary =\n %q\nwant\n %q", got, want)
	}
	if o.Example["Over your 20 Mbps ceiling"] != "a" {
		t.Errorf("example = %q, want the first one seen", o.Example["Over your 20 Mbps ceiling"])
	}
	// A grab, with a partial indexer failure and a note.
	g := GrabOutcome{Searched: true, Found: 5, Eligible: 2, Grabbed: []string{"Show.S03E04"}, IndexerErrors: 1, Note: "1 episode still missing"}
	if got := g.Summary(); got != "Grabbed Show.S03E04 · 5 found · 2 fit · 1 indexer failed · 1 episode still missing" {
		t.Errorf("grab summary = %q", got)
	}
	// No search needed: just the note.
	if got := (GrabOutcome{Note: "Nothing missing in Season 3"}).Summary(); got != "Nothing missing in Season 3" {
		t.Errorf("unsearched summary = %q", got)
	}
}
