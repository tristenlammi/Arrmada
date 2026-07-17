package parser

import (
	"reflect"
	"testing"
)

// TestSeasonRangeShort covers "S01-07" box-set naming (second bound with no "S"),
// which TorrentLeech uses (e.g. "Elementary S01-07 Complete").
func TestSeasonRangeShort(t *testing.T) {
	cases := []struct {
		name    string
		seasons []int
	}{
		{"Elementary S01-07 Complete 1080p WEB x264-Mixed-TL", []int{1, 2, 3, 4, 5, 6, 7}},
		{"Elementary S01-S07 Complete 1080p WEB x264", []int{1, 2, 3, 4, 5, 6, 7}},
		{"Elementary S05 720p WEBRip x265 HEVC-PSA", []int{5}},
		{"Show S02E07-08 1080p WEB", nil}, // a 2-episode file, NOT a season range
	}
	for _, c := range cases {
		if got := Parse(c.name).Seasons; !reflect.DeepEqual(got, c.seasons) {
			t.Errorf("%q seasons = %v, want %v", c.name, got, c.seasons)
		}
	}
}
