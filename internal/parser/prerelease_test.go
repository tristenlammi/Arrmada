package parser

import "testing"

// Every pre-release copy parses as CAM, however it's spelled — and ordinary releases that
// merely contain the same letters don't.
func TestPreReleaseSources(t *testing.T) {
	cams := []string{
		"Movie.2025.1080p.HDCAM.x264-GRP",
		"Movie 2025 CAMRip XviD",
		"Movie.2025.CAM.x264-GRP",
		"Movie.2025.HQCAM.x264-GRP",
		"Movie.2025.TELESYNC.x264-GRP",
		"Movie.2025.1080p.HDTS.x264-GRP",
		"Movie.2025.TS.x264-GRP",
		"Movie.2025.TELECINE.x264-GRP",
		"Movie.2025.HDTC.x264-GRP",
		"Movie.2025.DVDSCR.x264-GRP",
		"Movie.2025.SCREENER.x264-GRP",
		"Movie.2025.WORKPRINT.x264-GRP",
		"Movie.2025.R5.x264-GRP",
	}
	for _, name := range cams {
		if got := Parse(name).Source; got != SourceCAM {
			t.Errorf("%s: source %q, want CAM", name, got)
		}
	}
	notCams := map[string]Source{
		"Movie.2025.1080p.BluRay.DTS-HD.MA.5.1.x264-GRP": SourceBluray,
		"Movie.2025.2160p.WEB-DL.DDP5.1.Atmos.HEVC-GRP":  SourceWebDL,
		"Camelot.1967.1080p.BluRay.x264-GRP":             SourceBluray,
		"Scream.2022.1080p.WEB-DL.DD5.1.H.264-GRP":       SourceWebDL,
		"The.Tick.S01E01.1080p.WEBRip.x264-GRP":          SourceWebRip,
	}
	for name, want := range notCams {
		if got := Parse(name).Source; got != want {
			t.Errorf("%s: source %q, want %q", name, got, want)
		}
	}
}
