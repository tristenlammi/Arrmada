package parser

import (
	"reflect"
	"testing"
)

// TestAnimeSeasonAbsolute covers the "[Group] Title S2 29" fansub format (No0bSubs et al.)
// where a season number is followed by the ABSOLUTE episode number — which parsed to a
// season pack with no episode ("No S/E detected") before.
func TestAnimeSeasonAbsolute(t *testing.T) {
	cases := []struct {
		name   string
		title  string
		abs    []int
		season int
	}{
		{"[No0bSubs] Frieren - Beyond Journey's End S2 29 (1080p AV1 MULTI Audio)[DA4AB167]", "Frieren - Beyond Journey's End", []int{29}, 0},
		{"[No0bSubs] Frieren - Beyond Journey's End S2 38 (1080p AV1 MULTI Audio)[AC270A95]", "Frieren - Beyond Journey's End", []int{38}, 0},
		{"[SubsPlease] Show - 137 (1080p) [ABCD]", "Show", []int{137}, 0}, // existing dash form still works
		{"[Group] Show S02 (1080p BluRay)", "Show", nil, 2},               // a real season pack, no episode number
	}
	for _, c := range cases {
		r := Parse(c.name)
		if r.Title != c.title || !reflect.DeepEqual(r.AbsoluteEpisodes, c.abs) || r.Season != c.season {
			t.Errorf("%q -> title=%q abs=%v season=%d; want title=%q abs=%v season=%d",
				c.name, r.Title, r.AbsoluteEpisodes, r.Season, c.title, c.abs, c.season)
		}
	}
}
