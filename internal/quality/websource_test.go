package quality

import (
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// QUAL-04: WEB-DL and WEBRip are one tier for the source gates, an unstated source is not
// a cam, and ranking still prefers WEB-DL.
func TestWebSourceTier(t *testing.T) {
	e := NewDefaultEngine()
	webMin := Profile{MinSource: parser.SourceWebDL}
	for _, name := range []string{
		"Show.S01E01.1080p.WEB.h264-GROUP",
		"Show.S01E01.1080p.WEBRip.x264-GRP",
		"Show.S01E01.1080p.WEB-DL.x264-GRP",
		"[SubsPlease] Frieren - 01 (1080p) [ABCD1234].mkv",
		"[Erai-raws] Show - 12 [1080p]",
		"Show.S01E01.1080p.x264-GRP", // states no source
		"Show.S01E01.1080p.BluRay.x264-GRP",
	} {
		if ev := e.Evaluate(webMin, NewCandidate(name, 2, 10)); !ev.Eligible {
			t.Errorf("%s under a WEB minimum: rejected (%s)", name, ev.RejectReason)
		}
	}
	for _, name := range []string{"Show.S01E01.720p.HDTV.x264-GRP", "Movie.2025.CAM.x264-GRP"} {
		ev := e.Evaluate(webMin, NewCandidate(name, 2, 10))
		if ev.Eligible || !strings.HasPrefix(ev.RejectReason, "Not WEB or better") {
			t.Errorf("%s under a WEB minimum: eligible=%v reason %q", name, ev.Eligible, ev.RejectReason)
		}
	}
	// A profile saved with the old "WEBRip+" minimum means the same WEB tier.
	if ev := e.Evaluate(Profile{MinSource: parser.SourceWebRip}, NewCandidate("Show.S01E01.1080p.WEB-DL.x264-GRP", 2, 10)); !ev.Eligible {
		t.Errorf("WEB-DL under a stored WEBRip minimum: %s", ev.RejectReason)
	}

	bdMin := Profile{MinSource: parser.SourceBluray}
	ev := e.Evaluate(bdMin, NewCandidate("Show.S01E01.1080p.x264-GRP", 2, 10))
	if want := "Source isn't stated in the name — this profile needs BluRay or better"; ev.Eligible || ev.RejectReason != want {
		t.Errorf("unstated source under BluRay+: eligible=%v reason %q, want %q", ev.Eligible, ev.RejectReason, want)
	}
	if ev := e.Evaluate(bdMin, NewCandidate("Show.S01E01.1080p.WEBRip.x264-GRP", 2, 10)); ev.Eligible || ev.RejectReason != "Not BluRay or better — this is WEBRip" {
		t.Errorf("WEBRip under BluRay+: eligible=%v reason %q", ev.Eligible, ev.RejectReason)
	}

	webMax := Profile{MaxSource: parser.SourceWebDL}
	if ev := e.Evaluate(webMax, NewCandidate("Movie.2010.1080p.BluRay.x264-GRP", 8, 10)); ev.Eligible || ev.RejectReason != "Above your WEB ceiling — this is BluRay" {
		t.Errorf("BluRay under up-to-WEB: eligible=%v reason %q", ev.Eligible, ev.RejectReason)
	}
	for _, name := range []string{"Show.S01E01.1080p.WEBRip.x264-GRP", "Show.S01E01.1080p.x264-GRP"} {
		if ev := e.Evaluate(webMax, NewCandidate(name, 2, 10)); !ev.Eligible {
			t.Errorf("%s under up-to-WEB: rejected (%s)", name, ev.RejectReason)
		}
	}
}

// The tier only widens the gates: between equal-size releases WEB-DL still wins.
func TestWebDLStillBeatsWebRip(t *testing.T) {
	d := NewDefaultEngine().Decide(Profile{MinSource: parser.SourceWebDL}, []Candidate{
		NewCandidate("Show.S01E01.1080p.WEBRip.x264-GRP", 3, 10),
		NewCandidate("Show.S01E01.1080p.WEB-DL.x264-GRP", 3, 10),
	})
	if d.Winner == nil || d.Winner.Candidate.Release.Source != parser.SourceWebDL {
		t.Fatalf("winner = %+v, want the WEB-DL", d.Winner)
	}
}

// An unstated source is said as such in every reason, never as "unknown".
func TestUnstatedSourceWording(t *testing.T) {
	d := NewDefaultEngine().Decide(Profile{}, []Candidate{
		NewCandidate("Show.S01E01.1080p.WEB-DL.x264-GRP", 3, 10),
		NewCandidate("Show.S01E01.720p.x264-GRP", 3, 10),
	})
	if want := "Chosen over the 720p release — lower resolution"; d.ChosenOver != want {
		t.Errorf("ChosenOver = %q, want %q", d.ChosenOver, want)
	}
}
