package parser

import "testing"

// QUAL-04: scene "WEB", fansub and BD names read as the source they are. Synthetic names
// in the shapes indexers actually return.
func TestWebFansubAndBDSources(t *testing.T) {
	cases := []struct {
		name     string
		source   Source
		inferred bool
	}{
		// Scene TV tags an untouched capture plain "WEB".
		{"Show.S01E01.1080p.WEB.h264-GROUP", SourceWebDL, false},
		{"Show.S02E05.2160p.WEB.H265-GRP", SourceWebDL, false},
		{"Show.S01E01.1080p.WEBRip.x264-GRP", SourceWebRip, false},
		// "Web" in a title still loses to the real tag.
		{"Charlottes.Web.2006.DVDRip.XviD-DoNE", SourceDVD, false},
		{"Web.of.Lies.S01E01.HDTV.x264-GRP", SourceHDTV, false},
		// A group called WEB says nothing about the source.
		{"Show.S01E01.1080p.x264-WEB", SourceUnknown, false},
		// Fansubs that name no source are simulcast web captures.
		{"[SubsPlease] Frieren - 01 (1080p) [ABCD1234].mkv", SourceWebDL, true},
		{"[Erai-raws] Show - 12 [1080p]", SourceWebDL, true},
		{"Arigatou.Show.100.[x264.AAC][A3BE77C2].mkv", SourceWebDL, true},
		// Anything the fansub name does state wins over the inference.
		{"[Group] Show - 01 [1080p WEBRip x264]", SourceWebRip, false},
		{"[Group] Show - 01 (WEB 1080p)", SourceWebDL, false},
		{"[Group] Show (BD 1080p HEVC FLAC)", SourceBluray, false},
		{"[Group] Show S01 [BDMux 1080p]", SourceBluray, false},
		{"[Group] Show - 01 [DVD][AF803142]", SourceDVD, false},
		// BD as a whole disc, or as a group name, is not an encode's source.
		{"Movie.2010.BDMV-GRP", SourceUnknown, false},
		{"Movie.BDMV", SourceUnknown, false},
		{"Movie.2010.1080p.x264-BD", SourceUnknown, false},
		// Ordinary scene names are untouched.
		{"Movie.2010.1080p.BluRay.x264-GRP", SourceBluray, false},
		{"Show.S01E01.1080p.x264-GRP", SourceUnknown, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Parse(c.name)
			if r.Source != c.source || r.SourceInferred != c.inferred {
				t.Errorf("Source = %q (inferred %v), want %q (inferred %v)", r.Source, r.SourceInferred, c.source, c.inferred)
			}
		})
	}
}
