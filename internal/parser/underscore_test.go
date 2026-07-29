package parser

import "testing"

// Underscore is a WORD character to Go's regex \b, so "Rafters_S01E01_Pilot" has no word
// boundary before the S and every \b-anchored pattern failed: a 122-file "Packed To The
// Rafters S01-S06" pack parsed as season 0 with no episodes and not one file could be placed.
func TestUnderscoreSeparatedNames(t *testing.T) {
	cases := []struct {
		name    string
		season  int
		episode int
		title   string
		epTitle string
	}{
		{"Packed To The Rafters_S01E01_Pilot 1080p AMZN WEB-DL H265-d3g.mkv", 1, 1, "Packed To The Rafters", "Pilot"},
		{"Packed To The Rafters_S06E12_Packing Up the Rafters 1080p AMZN WEB-DL H265-d3g.mkv", 6, 12, "Packed To The Rafters", "Packing Up the Rafters"},
		{"Show_2x05_Title.mkv", 2, 5, "Show", "Title"},
		// The dot and space forms must keep working exactly as before.
		{"Packed.To.The.Rafters.S01E01.Pilot.1080p.mkv", 1, 1, "Packed To The Rafters", "Pilot"},
		{"Packed To The Rafters S01E01 Pilot 1080p.mkv", 1, 1, "Packed To The Rafters", "Pilot"},
	}
	for _, c := range cases {
		r := Parse(c.name)
		if r.Season != c.season {
			t.Errorf("%q: season = %d, want %d", c.name, r.Season, c.season)
		}
		if len(r.Episodes) != 1 || r.Episodes[0] != c.episode {
			t.Errorf("%q: episodes = %v, want [%d]", c.name, r.Episodes, c.episode)
		}
		if r.Title != c.title {
			t.Errorf("%q: title = %q, want %q", c.name, r.Title, c.title)
		}
		if got := EpisodeTitleFrom(c.name); got != c.epTitle {
			t.Errorf("%q: episode title = %q, want %q", c.name, got, c.epTitle)
		}
	}
}

// A fansub tag holding an underscore must still be read as one group token — the group
// patterns run against the raw name, before underscores are normalized.
func TestUnderscoreKeepsFansubGroupAndAbsolute(t *testing.T) {
	r := Parse("[Some_Group] Show Name - 137 [1080p][x265].mkv")
	if r.Group != "Some_Group" {
		t.Errorf("group = %q, want the underscore preserved inside the fansub tag", r.Group)
	}
	if len(r.AbsoluteEpisodes) != 1 || r.AbsoluteEpisodes[0] != 137 {
		t.Errorf("absolute = %v, want [137]", r.AbsoluteEpisodes)
	}
}
