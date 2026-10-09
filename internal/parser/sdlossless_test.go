package parser

import "testing"

// QUAL-08: SD releases that name no resolution read as 480p, but only on explicit SD-era
// signals; an explicit resolution is never overridden.
func TestSDInference(t *testing.T) {
	cases := []struct {
		name     string
		res      Resolution
		inferred bool
	}{
		{"Show.S01E01.HDTV.x264-GRP", Res480p, true},
		{"Old.Show.S02E03.DVDRip.XviD-GRP", Res480p, true},
		{"Movie.1999.XviD-GRP", Res480p, true},
		{"Show.S03E04.DSR.x264-GRP", Res480p, true},
		{"Show.S03E04.SDTV.x264-GRP", Res480p, true},
		{"Show.S03E04.PDTV.x264-GRP", Res480p, true},
		{"Show.S03E04.DVB.x264-GRP", Res480p, true},
		// Explicit resolution untouched.
		{"Show.S01E01.720p.HDTV.x264-GRP", Res720p, false},
		{"Movie.2001.576p.DVDRip.x264-GRP", Res576p, false},
		// No SD signal: still unknown, never guessed.
		{"Show.S01E01.x264-GRP", ResUnknown, false},
		{"Show.S01E01.WEB.h264-GRP", ResUnknown, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Parse(c.name)
			if r.Resolution != c.res || r.ResolutionInferred != c.inferred {
				t.Errorf("Resolution = %q (inferred %v), want %q (inferred %v)", r.Resolution, r.ResolutionInferred, c.res, c.inferred)
			}
			if r.StatedResolution() == Res480p && c.inferred {
				t.Error("StatedResolution should not report an inferred resolution")
			}
		})
	}
}

func TestLosslessAudio(t *testing.T) {
	cases := []struct {
		name     string
		lossless bool
		tag      string // an audio tag that must be present ("" = don't check)
	}{
		{"Movie.1999.1080p.BluRay.REMUX.LPCM.2.0-GRP", true, "LPCM"},
		{"Movie.1999.1080p.BluRay.REMUX.AVC.PCM.2.0-GRP", true, "LPCM"},
		{"Movie.2010.1080p.BluRay.x264.DTS-HD.HRA.7.1-GRP", false, "DTS-HD"},
		{"Movie.2010.1080p.BluRay.x264.DTS-HD.Hi-Res.7.1-GRP", false, "DTS-HD"},
		{"Movie.2010.1080p.BluRay.x264.DTS-HD.MA.5.1-GRP", true, "DTS-HD"},
		{"Movie.2010.1080p.BluRay.x264.DTS-HDMA.5.1-GRP", true, ""},
		{"Movie.2010.2160p.BluRay.REMUX.DTS-X.7.1-GRP", true, "DTS-HD"},
		{"Movie.2010.2160p.BluRay.REMUX.DTS:X.7.1-GRP", true, "DTS-HD"},
		{"Movie.2010.1080p.BluRay.REMUX.TrueHD.7.1.Atmos-GRP", true, "TrueHD"},
		{"Movie.2010.1080p.BluRay.FLAC.2.0.x264-GRP", true, "FLAC"},
		{"Movie.2010.1080p.WEB-DL.DDP5.1.H.264-GRP", false, "DDP"},
		{"Movie.2010.1080p.BluRay.x264.DTS-GRP", false, "DTS"},
		// "pcm" bounded: not inside a word.
		{"Pcmania.2010.1080p.WEB-DL.AAC-GRP", false, "AAC"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Parse(c.name)
			if r.AudioLossless != c.lossless {
				t.Errorf("AudioLossless = %v, want %v (audio %v)", r.AudioLossless, c.lossless, r.Audio)
			}
			if c.tag != "" && !r.HasAudio(c.tag) {
				t.Errorf("audio %v, want it to include %s", r.Audio, c.tag)
			}
		})
	}
}

// A pre-release word in the title doesn't make the release a cam: only the tags after the
// year or season marker count. Without a marker the whole name is read, as before.
func TestPreReleaseOnlyAfterTheTitle(t *testing.T) {
	cases := map[string]Source{
		"Cam.2018.1080p.WEB.x264-GRP":           SourceWebDL,
		"Cam.2018.1080p.NF.WEBRip.x264-GRP":     SourceWebRip,
		"Cam.2018.1080p.x264-GRP":               SourceUnknown,
		"The.TC.Show.S01E01.1080p.WEB-DL-GRP":   SourceWebDL,
		"Movie.2024.HDCAM.x264-GRP":             SourceCAM,
		"Movie.HDCAM.x264-GRP":                  SourceCAM, // no marker: whole-name reading
		"Cam.2018.HDCAM.x264-GRP":               SourceCAM, // a cam of Cam is still a cam
		"Screener.Show.S01E01.720p.HDTV.x264-G": SourceHDTV,
	}
	for name, want := range cases {
		if got := Parse(name).Source; got != want {
			t.Errorf("%s: source %q, want %q", name, got, want)
		}
	}
}
