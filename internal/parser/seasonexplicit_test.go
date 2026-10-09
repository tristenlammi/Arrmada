package parser

import "testing"

// Season 0 is both Specials and "no season at all". Only a name that spelled the season
// out can be trusted as a special.
func TestSeasonExplicit(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"Show.S00E05.1080p.WEB-DL-GRP", true},
		{"Show 0x05 Special 720p", true},
		{"Show.S02E03.1080p", true},
		{"[Grp] Show - 05 [1080p]", false},
		{"Show.S02.1080p.BluRay-GRP", false},
		{"Show.Complete.Series.1080p", false},
		{"Show S2 29 1080p", false},
	}
	for _, c := range cases {
		if got := Parse(c.name).SeasonExplicit; got != c.want {
			t.Errorf("%s: SeasonExplicit = %v, want %v", c.name, got, c.want)
		}
	}
}
