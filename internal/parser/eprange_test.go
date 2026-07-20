package parser

import (
	"reflect"
	"testing"
)

// A hyphenated episode range covers everything between its endpoints. Recording only the
// endpoints made a file that genuinely holds four episodes mark two of them present and
// leave the middle two "missing" forever — the searcher then hunted for episodes that
// were already on disk, inside a file it had already imported.
func TestMultiEpisodeRangeExpands(t *testing.T) {
	cases := []struct {
		name string
		want []int
	}{
		// The case from the wild: one Peppa Pig file holding four episodes.
		{"Peppa.Pig.S07E62-E65.Cruise.Ship.Holiday.&.Holiday.on.the.Sea.1080p.WEBRip.x265-iVy.mkv",
			[]int{62, 63, 64, 65}},

		// Ranges that were already right because their endpoints are adjacent — these
		// must not regress.
		{"Show.S03E21-E22.1080p.WEB-DL-GRP.mkv", []int{21, 22}},
		{"Show.S03E21-22.1080p.WEB-DL-GRP.mkv", []int{21, 22}},

		// Juxtaposition is a LIST of exactly those episodes, not a range. It happens to
		// look the same when consecutive, which is why the bug hid for so long.
		{"Show.S01E01E02.720p.HDTV.mkv", []int{1, 2}},

		// A three-part range in the middle of a name.
		{"Show.S02E05-E07.1080p.WEB.mkv", []int{5, 6, 7}},

		// Single episodes are untouched.
		{"Show.S01E05.1080p.WEB.mkv", []int{5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Parse(c.name).Episodes; !reflect.DeepEqual(got, c.want) {
				t.Errorf("Episodes = %v, want %v", got, c.want)
			}
		})
	}
}

// An implausibly wide range is far more likely a misparse than a real 40-episode file.
// Expanding it would mark most of a season present from a single file, so past the cap we
// fall back to the endpoints rather than inventing episodes.
func TestAbsurdEpisodeRangeIsNotExpanded(t *testing.T) {
	got := Parse("Show.S01E01-E99.1080p.WEB.mkv").Episodes
	if len(got) > 2 {
		t.Errorf("a %d-episode span should not expand, got %v", 99, got)
	}
}
