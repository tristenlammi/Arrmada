package httpapi

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/automation"
)

// The grab body's season/episode decide which episodes skip the import gate. Season 0 is
// Specials and must survive the trip — a zero-means-absent reading turned it into the
// whole show, the widest scope there is.
func TestGrabScopeOfRequest(t *testing.T) {
	n := func(v int) *int { return &v }
	cases := []struct {
		name            string
		season, episode *int
		want            automation.GrabScope
		ok              bool
	}{
		{"no scope is the whole show", nil, nil, automation.WholeShow, true},
		{"a season", n(3), nil, automation.GrabScope{Season: 3}, true},
		{"season with episode 0 is the season", n(3), n(0), automation.GrabScope{Season: 3}, true},
		{"an episode", n(3), n(4), automation.GrabScope{Season: 3, Episode: 4}, true},
		{"specials stay specials", n(0), nil, automation.GrabScope{Season: 0}, true},
		{"a special", n(0), n(5), automation.GrabScope{Season: 0, Episode: 5}, true},
		{"episode without a season is refused", nil, n(4), automation.GrabScope{}, false},
		{"negative season is refused", n(-1), nil, automation.GrabScope{}, false},
		{"negative episode is refused", n(2), n(-3), automation.GrabScope{}, false},
	}
	for _, tc := range cases {
		got, ok := grabScopeOf(tc.season, tc.episode)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("%s: got %+v, %v; want %+v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}
