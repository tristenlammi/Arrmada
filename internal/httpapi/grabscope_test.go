package httpapi

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/automation"
)

// A series grab's scope is the search its token came from, and it decides which episodes
// skip the import gate. Season 0 is Specials and must survive the trip — a
// zero-means-absent reading turned it into the whole show, the widest scope there is.
func TestGrabScopeFromSearch(t *testing.T) {
	cases := []struct {
		name            string
		season, episode string
		want            automation.GrabScope
	}{
		{"no scope is the whole show", "", "", automation.WholeShow},
		{"a season", "3", "", automation.GrabScope{Season: 3}},
		{"season with episode 0 is the season", "3", "0", automation.GrabScope{Season: 3}},
		{"an episode", "3", "4", automation.GrabScope{Season: 3, Episode: 4}},
		{"specials stay specials", "0", "", automation.GrabScope{Season: 0}},
		{"a special", "0", "5", automation.GrabScope{Season: 0, Episode: 5}},
		{"an episode without a season is the whole show", "", "4", automation.WholeShow},
		{"a negative season is the whole show", "-1", "", automation.WholeShow},
		{"a negative episode is the season", "2", "-3", automation.GrabScope{Season: 2}},
	}
	for _, tc := range cases {
		season, episode := releasesScope(tc.season, tc.episode)
		if got := automation.ScopeFor(season, episode); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
