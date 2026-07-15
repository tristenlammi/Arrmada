package parser

import "testing"

func TestSeriesKind(t *testing.T) {
	cases := []struct {
		name string
		kind Kind
	}{
		{"Severance S01E03 1080p WEB-DL", KindEpisode},
		{"Severance S01E01-E03 1080p WEB-DL", KindEpisode},
		{"Severance S01 1080p WEB-DL", KindSeasonPack},
		{"Severance S01-S02 1080p WEB-DL", KindMultiSeason},
		{"Severance Seasons 1-3 2160p", KindMultiSeason},
		{"Severance Complete Series 1080p BluRay", KindCompleteShow},
		{"The Matrix 1999 1080p BluRay", KindMovie},
	}
	for _, c := range cases {
		if got := Parse(c.name).Kind(); got != c.kind {
			t.Errorf("%q: kind=%d want %d (parsed %+v)", c.name, got, c.kind, Parse(c.name))
		}
	}
}
