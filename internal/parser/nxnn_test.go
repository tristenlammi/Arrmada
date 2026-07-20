package parser

import (
	"reflect"
	"testing"
)

// "1x01" is the other common episode form, used by pack releasers and by anyone who
// renamed their library that way. Nothing understood it: a 122-file "Parks and
// Recreation S01-S07" pack imported zero episodes and was blocklisted as junk, and four
// Top Gear episodes were lost from a season that otherwise imported fine.
func TestNxNNEpisodes(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		season  int
		episode []int
	}{
		{"Parks and Recreation - 1x01 - Make My Pit a Park.mkv", "Parks and Recreation", 1, []int{1}},
		{"Parks and Recreation - 5x22 - Are You Better Off.mkv", "Parks and Recreation", 5, []int{22}},
		{"top_gear.22x01.720p_hdtv_x264-fov.mkv", "top gear", 22, []int{1}},
		{"Top.Gear.22x08.720p.HDTV.x264-FoV.mkv", "Top Gear", 22, []int{8}},

		// Multi-episode files in this form.
		{"Parks and Recreation - 6x01 & 6x02 - London.mkv", "Parks and Recreation", 6, []int{1, 2}},
		{"Parks and Recreation - 7x12 & 7x13 - One Last Ride.mkv", "Parks and Recreation", 7, []int{12, 13}},

		// An episode TITLE containing "Season N" must not win over the real marker — this
		// one reported season 2 for a file that is plainly 6x19.
		{"Parks and Recreation - 6x19 - Flu Season 2.mkv", "Parks and Recreation", 6, []int{19}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Parse(c.name)
			if r.Season != c.season || !reflect.DeepEqual(r.Episodes, c.episode) {
				t.Errorf("season=%d episodes=%v, want season=%d episodes=%v", r.Season, r.Episodes, c.season, c.episode)
			}
			if r.Title != c.title {
				t.Errorf("Title = %q, want %q", r.Title, c.title)
			}
		})
	}
}

// SxxExx must still take precedence — it's unambiguous, and a stray "16x9" in the same
// name must not override it.
func TestSxxExxStillWinsOverNxNN(t *testing.T) {
	r := Parse("Show.S03E07.1080p.16x9.WEB-DL-GRP.mkv")
	if r.Season != 3 || !reflect.DeepEqual(r.Episodes, []int{7}) {
		t.Errorf("season=%d episodes=%v, want 3 / [7]", r.Season, r.Episodes)
	}
}

// The dangerous failure is a false positive: reading an aspect ratio or a dimension as an
// episode would file a movie under some invented season. The 2-digit episode requirement
// is what prevents it.
func TestNxNNIgnoresAspectRatiosAndDimensions(t *testing.T) {
	for _, name := range []string{
		"Some.Movie.2019.1080p.16x9.BluRay.x264-GRP",
		"Some.Movie.2019.4x4.Offroad.1080p.BluRay-GRP",
		"Some.Movie.2019.1080p.BluRay.DTS.5x1.x264-GRP",
	} {
		if r := Parse(name); r.Season != 0 || len(r.Episodes) > 0 {
			t.Errorf("Parse(%q) read TV markers: season=%d episodes=%v", name, r.Season, r.Episodes)
		}
	}
}

// A hyphenated range in this form spans, matching the SxxExx behaviour.
func TestNxNNRangeExpands(t *testing.T) {
	r := Parse("Show - 2x01-2x04 - Something.mkv")
	if !reflect.DeepEqual(r.Episodes, []int{1, 2, 3, 4}) {
		t.Errorf("Episodes = %v, want [1 2 3 4]", r.Episodes)
	}
}
