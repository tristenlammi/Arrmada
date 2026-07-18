package parser

import "testing"

// TestSeasonWordForm covers spelled-out "Season 3" packs (scene/WEBRip) that don't use
// the "S03" form — previously parsed as season 0, which broke the import guard.
func TestSeasonWordForm(t *testing.T) {
	cases := []struct {
		name   string
		season int
	}{
		{"Ben 10 2016 Season 3 Complete 720p AMZN WEBRip x264 [i_c]", 3},
		{"The Office Season 5 1080p", 5},
		{"Show.S03.1080p", 3},        // the SxxExx form still wins
		{"Some Movie 2016 1080p", 0}, // not TV
	}
	for _, c := range cases {
		if got := Parse(c.name).Season; got != c.season {
			t.Errorf("Parse(%q).Season = %d, want %d", c.name, got, c.season)
		}
	}
}
