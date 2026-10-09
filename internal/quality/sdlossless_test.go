package quality

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// QUAL-08: the Lossless format and the target's Lossless fact agree, and both follow the
// parser: LPCM in, DTS-HD High Resolution out.
func TestLosslessFormatAndFactsAgree(t *testing.T) {
	lossless := map[string]CustomFormat{}
	for _, f := range DefaultFormats() {
		lossless[f.Name] = f
	}
	f := lossless["Lossless"]
	for name, want := range map[string]bool{
		"Movie.1999.1080p.BluRay.REMUX.LPCM.2.0-GRP":       true,
		"Movie.2010.1080p.BluRay.x264.DTS-HD.HRA.7.1-GRP":  false,
		"Movie.2010.1080p.BluRay.x264.DTS-HD.MA.5.1-GRP":   true,
		"Movie.2010.2160p.BluRay.REMUX.DTS-X.7.1-GRP":      true,
		"Movie.2010.1080p.BluRay.REMUX.TrueHD.7.1-GRP":     true,
		"Movie.2010.1080p.WEB-DL.DDP5.1.Atmos.H.264-GRP":   false,
		"Movie.2010.1080p.BluRay.x264.FLAC.2.0-GRP":        true,
		"Movie.2010.1080p.BluRay.x264.DTS-HD.5.1-GRP":      false, // neither MA nor HRA stated
		"Movie.2010.1080p.BluRay.x264.DTS-HD.MA.HRA.5.1-G": false, // contradictory: HRA wins
	} {
		r := parser.Parse(name)
		if got := f.Matches(r); got != want {
			t.Errorf("%s: Lossless format = %v, want %v", name, got, want)
		}
		if got := ReleaseFacts(r, 0).Lossless; got != want {
			t.Errorf("%s: ReleaseFacts.Lossless = %v, want %v", name, got, want)
		}
	}
	// A Release built from probed facts rather than parsed still counts by its labels.
	if !losslessAudio(parser.Release{Audio: []string{"TrueHD"}}) {
		t.Error("a TrueHD label alone should read as lossless")
	}
}

// SD releases that name no resolution are eligible under a profile that allows 480p, and
// an explicit 720p stays 720p.
func TestInferredSDIsEligible(t *testing.T) {
	e := NewDefaultEngine()
	p := Profile{AllowedResolutions: []parser.Resolution{parser.Res720p, parser.Res480p}}
	for _, name := range []string{"Show.S01E01.HDTV.x264-GRP", "Old.Show.S02E03.DVDRip.XviD-GRP"} {
		if ev := e.Evaluate(p, NewCandidate(name, 0.4, 10)); !ev.Eligible {
			t.Errorf("%s: %s", name, ev.RejectReason)
		}
	}
	if ev := e.Evaluate(Profile{AllowedResolutions: []parser.Resolution{parser.Res480p}}, NewCandidate("Show.S01E01.720p.HDTV.x264-GRP", 1, 10)); ev.Eligible {
		t.Error("an explicit 720p release passed a 480p-only profile")
	}
}
