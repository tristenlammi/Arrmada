package parser

import "testing"

// A file's episode title is an independent check on its episode NUMBER. The importer
// matches on the number alone, so when a release numbers episodes differently from the
// metadata, files are filed and renamed as the wrong episode with nothing to notice.
// Parks and Recreation season 6 did exactly this — the pack put "The Pawnee-Eagleton
// Tip-Off Classic" at 6x03 where TMDB has "Doppelgängers", and everything after it
// landed one slot out.
func TestEpisodeTitleFrom(t *testing.T) {
	cases := []struct{ name, want string }{
		{"Parks and Recreation - 6x03 - The Pawnee-Eagleton Tip-Off Classic.mkv", "The Pawnee-Eagleton Tip-Off Classic"},
		{"Parks and Recreation - 1x01 - Make My Pit a Park.mkv", "Make My Pit a Park"},
		{"Parks and Recreation - 6x01 & 6x02 - London.mkv", "London"},
		{"Teen.Titans.Go.S07E01.The.Mug.1080p.HMAX.WEB-DL.DD2.0.H.264-NTb.mkv", "The Mug"},
		{"Ink.Master.S05E02.Pin.up.Pittfalls.1080p.WEBRip.10bit.EAC3.2.0.x265-iVy.mkv", "Pin up Pittfalls"},

		// No title present — most scene releases. A guess would be worse than silence.
		{"Top.Gear.S22E03.720p.HDTV.x264-ORGANiC.mkv", ""},
		{"Show.S01E05.1080p.WEB-DL-GRP.mkv", ""},
		{"Peppa Pig (2004) S08 1080p WEBRip 10bit EAC3 2 0 x265-iVy", ""},
		{"some random file.mkv", ""},
	}
	for _, c := range cases {
		if got := EpisodeTitleFrom(c.name); got != c.want {
			t.Errorf("EpisodeTitleFrom(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}
