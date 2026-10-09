package parser

import "testing"

// TitleYear is only a year that names the show: one before the season/episode marker. An
// air year after the marker, a run of years, or a year that IS the title doesn't count.
func TestTitleYearOnlyBeforeMarker(t *testing.T) {
	cases := []struct {
		name string
		want int
	}{
		{"Doctor.Who.2005.S01E01.Rose.1080p.BluRay.x264-GRP", 2005},
		{"Doctor Who (1963) S01E01 576p DVD", 1963},
		{"Battlestar.Galactica.2003.S01.1080p.BluRay-GRP", 2003},
		{"Doctor.Who.2005.Complete.Series.1080p-GRP", 2005},
		{"[Group] Show (2023) - 05 [1080p]", 2023},
		// An air year after the marker says when it aired, not which show.
		{"Show.S01E01.2019.1080p.WEB-DL-GRP", 0},
		{"My Hero Academia (Boku no Hero Academia) S04 2019 1080p WEB-DL", 0},
		{"Show.Season.1.2019.1080p-GRP", 0},
		// A year-titled show: the year is the title.
		{"1923.S01E01.1080p.WEB-DL-GRP", 0},
		{"[Group] 1923 - 05 [1080p]", 0},
		{"1923.2022.S01E01.1080p.WEB-DL-GRP", 2022},
		// A run of years is the span it aired.
		{"The.Office.US.2005-2013.S01-S09.1080p-GRP", 0},
		// No year at all, and movies.
		{"The.Office.US.S02E01.720p-GRP", 0},
		{"Arrival.2016.1080p.BluRay.x264-GRP", 0},
	}
	for _, c := range cases {
		if got := Parse(c.name).TitleYear; got != c.want {
			t.Errorf("TitleYear(%q) = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestSplitCountry(t *testing.T) {
	cases := []struct{ in, base, cc string }{
		{"The Office US", "The Office", "US"},
		{"The.Office.US", "The Office", "US"},
		{"The Office (US)", "The Office", "US"},
		{"Ghosts UK", "Ghosts", "GB"},
		{"Ghosts [GB]", "Ghosts", "GB"},
		{"Hells Kitchen AU", "Hells Kitchen", "AU"},
		{"Utopia au", "Utopia", "AU"},
		// No tag, or the tag would be the whole title.
		{"The Office", "The Office", ""},
		{"Us", "Us", ""},
		{"(US)", "(US)", ""},
		{"Usagi", "Usagi", ""},
		{"Bluey", "Bluey", ""},
	}
	for _, c := range cases {
		base, cc := SplitCountry(c.in)
		if base != c.base || cc != c.cc {
			t.Errorf("SplitCountry(%q) = (%q, %q), want (%q, %q)", c.in, base, cc, c.base, c.cc)
		}
	}
	// What the release parser leaves as the title is what gets split.
	for name, want := range map[string]string{
		"The.Office.US.S02E01.720p.HDTV-GRP":   "US",
		"The.Office.(US).S02E01.720p.HDTV-GRP": "US",
		"Ghosts.UK.S01E01.1080p.WEB-DL-GRP":    "GB",
	} {
		if _, cc := SplitCountry(Parse(name).Title); cc != want {
			t.Errorf("%q: parsed title %q gives country %q, want %q", name, Parse(name).Title, cc, want)
		}
	}
}
