package parser

import "testing"

// Scene groups sometimes put pack words BEFORE the season marker, which is where the
// title is cut — so they end up inside the title, the release matches no library series,
// and the download is retried forever while the searcher re-grabs it every 15 minutes.
func TestPackWordsStrippedFromTitle(t *testing.T) {
	cases := []struct{ name, want string }{
		{"The Expanse complete S01-S06 web 10bit ddp2 hevc-d3g", "The Expanse"},
		{"Scorpion S01-S04 complete web 10bit ddp2 hevc-d3g", "Scorpion"},
		{"Band of Brothers Complete Series S01 1080p BluRay", "Band of Brothers"},
		{"The Wire collection S01-S05 1080p", "The Wire"},
		// Pack words inside a real title must survive.
		{"The Complete Sherlock Holmes S01 1080p", "The Complete Sherlock Holmes"},
	}
	for _, c := range cases {
		if got := Parse(c.name).Title; got != c.want {
			t.Errorf("Parse(%q).Title = %q, want %q", c.name, got, c.want)
		}
	}
}
