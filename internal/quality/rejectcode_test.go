package quality

import (
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// TestRejectCodeSetForEveryRejectReason walks every way Evaluate turns a release down and
// checks each carries its stable code next to the unchanged sentence.
func TestRejectCodeSetForEveryRejectReason(t *testing.T) {
	e := NewDefaultEngine()
	cases := []struct {
		name   string
		p      Profile
		c      Candidate
		code   string
		reason string // the sentence must still start like this
	}{
		{"resolution", Profile{AllowedResolutions: []parser.Resolution{parser.Res2160p}},
			NewCandidate("Film.2020.1080p.BluRay.x264-GRP", 8, 50), RejectResolution, "Not in profile"},
		{"source floor", Profile{MinSource: parser.SourceBluray},
			NewCandidate("Film.2020.1080p.HDTV.x264-GRP", 4, 50), RejectSourceFloor, "Not BluRay or better"},
		{"source unstated", Profile{MinSource: parser.SourceBluray},
			NewCandidate("Film.2020.1080p.x264-GRP", 4, 50), RejectSourceFloor, "Source isn't stated"},
		{"prerelease", Profile{RejectPreRelease: true},
			NewCandidate("Film.2020.1080p.CAM.x264-GRP", 2, 50), RejectPrerelease, "A cam"},
		{"source ceiling", Profile{MaxSource: parser.SourceWebDL},
			NewCandidate("Film.2020.1080p.BluRay.REMUX.AVC-GRP", 30, 50), RejectSourceCeiling, "Above your"},
		{"bitrate ceiling", Profile{BitrateCapMbps: 10},
			NewCandidate("Film.2020.1080p.BluRay.x264-GRP", 40, 50).WithRuntime(100), RejectBitrateCeiling, "Over your 10 Mbps ceiling"},
		{"seeders", Profile{MinSeeders: 10},
			NewCandidate("Film.2020.1080p.BluRay.x264-GRP", 8, 2), RejectSeeders, "Only 2 seeders"},
		{"rejected term", Profile{Rejected: []string{"x264"}},
			NewCandidate("Film.2020.1080p.BluRay.x264-GRP", 8, 50), RejectTerm, "Contains rejected term"},
		{"missing required", Profile{Required: []string{"Atmos"}},
			NewCandidate("Film.2020.1080p.BluRay.x264-GRP", 8, 50), RejectMissingRequired, "No Atmos"},
		{"min format score", Profile{MinFormatScore: 100},
			NewCandidate("Film.2020.1080p.BluRay.x264-GRP", 8, 50), RejectMinFormatScore, "Below the profile's minimum"},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		ev := e.Evaluate(tc.p, tc.c)
		if ev.Eligible {
			t.Fatalf("%s: eligible, want rejected", tc.name)
		}
		if ev.RejectCode != tc.code {
			t.Errorf("%s: code = %q, want %q (reason %q)", tc.name, ev.RejectCode, tc.code, ev.RejectReason)
		}
		if !strings.HasPrefix(ev.RejectReason, tc.reason) {
			t.Errorf("%s: reason = %q, want it to start %q", tc.name, ev.RejectReason, tc.reason)
		}
		seen[ev.RejectCode] = true
	}
	for _, code := range RejectCodes {
		if !seen[code] {
			t.Errorf("no case covers reject code %q", code)
		}
	}
	// An eligible release carries no code.
	if ev := e.Evaluate(Profile{}, NewCandidate("Film.2020.1080p.BluRay.x264-GRP", 8, 50)); !ev.Eligible || ev.RejectCode != "" {
		t.Fatalf("eligible release: %+v", ev)
	}
}

// Every rejected evaluation a Decide hands back has a code: the search outcome counts by it.
func TestDecideRejectionsAllCarryACode(t *testing.T) {
	p := Profile{AllowedResolutions: []parser.Resolution{parser.Res1080p}, MinSeeders: 5, BitrateCapMbps: 15}
	d := NewDefaultEngine().Decide(p, []Candidate{
		NewCandidate("Film.2020.2160p.WEB-DL.x265-GRP", 15, 50).WithRuntime(120),
		NewCandidate("Film.2020.1080p.WEB-DL.x264-GRP", 4, 1).WithRuntime(120),
		NewCandidate("Film.2020.1080p.BluRay.x264-GRP", 40, 50).WithRuntime(120),
		NewCandidate("Film.2020.1080p.WEB-DL.x264-OK", 6, 50).WithRuntime(120),
	})
	if len(d.Rejected) != 3 || len(d.Eligible) != 1 {
		t.Fatalf("decision = %d rejected, %d eligible", len(d.Rejected), len(d.Eligible))
	}
	for _, ev := range d.Rejected {
		if ev.RejectCode == "" {
			t.Fatalf("rejected without a code: %+v", ev)
		}
	}
}
