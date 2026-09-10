package quality

import (
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// TestBitrateMbps pins the GiB-over-minutes → Mbps conversion the upgrade
// threshold and bitrate cap both rely on (10 GiB over 100 min ≈ 14.3 Mbps).
func TestBitrateMbps(t *testing.T) {
	if got := BitrateMbps(10, 100); got < 14.0 || got > 14.7 {
		t.Errorf("BitrateMbps(10, 100) = %.2f, want ≈14.3", got)
	}
	if got := BitrateMbps(10, 0); got != 0 {
		t.Errorf("BitrateMbps with unknown runtime = %.2f, want 0", got)
	}
}

// testProfiles are scoring fixtures for the engine tests. They are plain
// Profiles (the app no longer ships built-in presets), kept here so the engine
// behavior stays pinned to known inputs.
func testProfiles() map[string]Profile {
	return map[string]Profile{
		"4k-hdr": {
			Name:               "4K HDR",
			AllowedResolutions: []parser.Resolution{parser.Res2160p},
			SmallBias:          0.2,
			FormatScores:       map[string]int{"Dolby Vision": 60, "HDR10": 45, "Atmos": 40},
		},
		"4k-sane": {
			Name:               "4K sensible",
			AllowedResolutions: []parser.Resolution{parser.Res2160p},
			BitrateCapMbps:     40,
			SmallBias:          0.4,
			FormatScores:       map[string]int{"Dolby Vision": 55, "HDR10": 45, "Atmos": 40},
		},
		"best-1080p": {
			Name:               "Best 1080p",
			AllowedResolutions: []parser.Resolution{parser.Res1080p, parser.Res720p},
			SmallBias:          0.3,
			FormatScores:       map[string]int{"Atmos": 30},
		},
		"smallest": {
			Name:               "Smallest decent",
			AllowedResolutions: []parser.Resolution{parser.Res1080p},
			SmallBias:          8,
			FormatScores:       map[string]int{"HEVC": 20},
		},
		"remux": {
			Name:               "Remux collector",
			AllowedResolutions: []parser.Resolution{parser.Res2160p, parser.Res1080p},
			MinSource:          parser.SourceRemux,
			FormatScores:       map[string]int{"Dolby Vision": 50, "HDR10": 40, "Atmos": 40},
		},
	}
}

// The seven Dune: Part Two releases from the approved quality-editor mockup. Runtime 166 min so
// the bitrate ceiling has something to divide by (82 GB ≈ 71 Mbps, 24 GB ≈ 21 Mbps).
func duneCandidates() []Candidate {
	cands := []Candidate{
		NewCandidate("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.DV.HDR.TrueHD.Atmos.7.1-FraMeSToR", 82, 44),
		NewCandidate("Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX", 24, 212),
		NewCandidate("Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.HDR.H.265-RUMOUR", 19, 61),
		NewCandidate("Dune.Part.Two.2024.1080p.BluRay.x264.DTS-HD.MA.5.1-SPARKS", 16, 180),
		NewCandidate("Dune.Part.Two.2024.1080p.WEB-DL.DDP5.1.H.264-EVO", 9, 340),
		NewCandidate("Dune.Part.Two.2024.1080p.WEBRip.x265.AAC5.1-YTS", 3.8, 990),
		NewCandidate("Dune.Part.Two.2024.720p.HDTV.x264-GALAXY", 1.5, 22),
	}
	for i := range cands {
		cands[i].RuntimeMin = 166
	}
	return cands
}

func winnerGroup(d Decision) string {
	if d.Winner == nil {
		return ""
	}
	return d.Winner.Candidate.Release.Group
}

func TestPresetDecisions(t *testing.T) {
	engine := NewDefaultEngine()
	presets := testProfiles()
	cands := duneCandidates()

	cases := []struct {
		preset      string
		wantWinner  string // release group of the expected winner
		wantRejects int    // at least this many rejected
	}{
		{"4k-hdr", "FraMeSToR", 4},  // 2160p Remux wins; 1080p/720p rejected
		{"4k-sane", "FLUX", 5},      // Remux over the 40 Mbps cap → 2160p WEB-DL wins
		{"best-1080p", "SPARKS", 3}, // 1080p BluRay wins; 2160p not in profile
		{"smallest", "EVO", 4},      // smallest *decent* — low-quality YTS is penalized
		{"remux", "FraMeSToR", 6},   // only the Remux qualifies
	}

	for _, tc := range cases {
		t.Run(tc.preset, func(t *testing.T) {
			d := engine.Decide(presets[tc.preset], cands)
			if got := winnerGroup(d); got != tc.wantWinner {
				t.Errorf("winner group = %q, want %q", got, tc.wantWinner)
			}
			if len(d.Rejected) < tc.wantRejects {
				t.Errorf("rejected = %d, want >= %d", len(d.Rejected), tc.wantRejects)
			}
			if len(d.Why) == 0 {
				t.Error("expected non-empty why")
			}
		})
	}
}

func TestSizeCapFlipsWinnerLive(t *testing.T) {
	engine := NewDefaultEngine()
	base := testProfiles()["4k-hdr"]
	cands := duneCandidates()

	// No cap: the 82 GB Remux wins.
	if g := winnerGroup(engine.Decide(base, cands)); g != "FraMeSToR" {
		t.Fatalf("uncapped winner = %q, want FraMeSToR", g)
	}

	// Add a 40 Mbps cap: the 82 GB Remux (~71 Mbps) is rejected, the 24 GB WEB-DL takes over.
	capped := base
	capped.BitrateCapMbps = 40
	d := engine.Decide(capped, cands)
	if g := winnerGroup(d); g != "FLUX" {
		t.Fatalf("capped winner = %q, want FLUX", g)
	}
	// The Remux should be rejected specifically for the bitrate ceiling.
	var found bool
	for _, ev := range d.Rejected {
		if ev.Candidate.Release.Group == "FraMeSToR" && strings.Contains(ev.RejectReason, "ceiling") {
			found = true
		}
	}
	if !found {
		t.Error("expected Remux rejected with a size-ceiling reason")
	}
}

func TestChosenOverExplained(t *testing.T) {
	d := NewDefaultEngine().Decide(testProfiles()["4k-hdr"], duneCandidates())
	if d.ChosenOver == "" {
		t.Error("expected a 'chosen over' explanation")
	}
}

// TestPrefersFormatsOverBitrate encodes the upgrade rule: a lower-bitrate release
// that carries the preferred formats beats a bigger one that lacks them (the
// "50 GB with Atmos/HDR10 over 70 GB without" case). Bitrate only breaks ties
// between releases of equal quality/format score.
func TestPrefersFormatsOverBitrate(t *testing.T) {
	profile := Profile{
		Name:               "4K, prefers DV/HDR10/Atmos",
		AllowedResolutions: []parser.Resolution{parser.Res2160p},
		FormatScores:       map[string]int{"Dolby Vision": 55, "HDR10": 45, "Atmos": 40},
	}
	cands := []Candidate{
		NewCandidate("Movie.2024.2160p.WEB-DL.x265-PLAIN", 70, 100),                      // bigger, no preferred formats
		NewCandidate("Movie.2024.2160p.BluRay.REMUX.DV.HDR10.TrueHD.Atmos-GRP", 50, 100), // smaller, all preferred formats
	}
	d := NewDefaultEngine().Decide(profile, cands)
	if d.Winner == nil || d.Winner.Candidate.Release.Group != "GRP" {
		t.Fatalf("expected the format-rich release to win over the bigger plain one, got %+v", winnerGroup(d))
	}
}

// A required format is a gate, not a preference: without it a release is rejected
// however big or well-scored it is, and the reason names the format.
func TestRequiredFormatRejects(t *testing.T) {
	profile := Profile{
		Name:               "4K, Atmos required",
		AllowedResolutions: []parser.Resolution{parser.Res2160p},
		Required:           []string{"Atmos"},
		FormatScores:       map[string]int{"Dolby Vision": 55},
	}
	cands := []Candidate{
		NewCandidate("Movie.2024.2160p.BluRay.REMUX.DV.HDR10.DTS-HD.MA-BIG", 80, 100), // bigger, DV, no Atmos
		NewCandidate("Movie.2024.2160p.WEB-DL.DDP5.1.Atmos.x265-SMALL", 20, 100),      // Atmos
	}
	d := NewDefaultEngine().Decide(profile, cands)
	if d.Winner == nil || d.Winner.Candidate.Release.Group != "SMALL" {
		t.Fatalf("the Atmos release must win, got %+v", winnerGroup(d))
	}
	if len(d.Rejected) != 1 || !strings.Contains(d.Rejected[0].RejectReason, "Atmos") {
		t.Errorf("the non-Atmos release should be rejected naming Atmos: %+v", d.Rejected)
	}
	if !containsStr(d.Winner.Matched, "Atmos") {
		t.Errorf("required format should show as matched: %v", d.Winner.Matched)
	}
	none := NewDefaultEngine().Decide(profile, cands[:1])
	if none.Winner != nil {
		t.Error("with nothing carrying the required format there is no winner")
	}
}

// The ceiling is the number the user typed, against the raw bitrate: a 25 Mbps HEVC
// release is under a 40 Mbps ceiling however efficient the codec.
func TestBitrateCapIsRaw(t *testing.T) {
	profile := Profile{Name: "4K, 40 cap", AllowedResolutions: []parser.Resolution{parser.Res2160p, parser.Res1080p}, BitrateCapMbps: 40}
	e := NewDefaultEngine()
	hevc := NewCandidate("Iron.Lung.2026.2160p.WEB-DL.DDP5.1.HDR.H.265-SCOPE", 22, 100).WithRuntime(120) // ≈26 Mbps raw
	avc := NewCandidate("Iron.Lung.2026.2160p.WEBRip.x264-CREATiVE24", 44, 100).WithRuntime(120)         // ≈52 Mbps raw
	if ev := e.Evaluate(profile, hevc); !ev.Eligible {
		t.Errorf("26 Mbps HEVC rejected under a 40 Mbps ceiling: %s", ev.RejectReason)
	}
	if ev := e.Evaluate(profile, avc); ev.Eligible || !strings.Contains(ev.RejectReason, "ceiling") {
		t.Errorf("52 Mbps should be over a 40 Mbps ceiling: %+v", ev)
	}
	d := e.Decide(profile, []Candidate{hevc, avc, NewCandidate("Iron.Lung.2026.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir", 1.2, 100).WithRuntime(120)})
	if d.Winner == nil || d.Winner.Candidate.Release.Group != "SCOPE" {
		t.Errorf("the 4K HEVC under the ceiling should win, got %s", winnerGroup(d))
	}
}

// Preferring a format is a tie-breaker between comparable encodes: it must not pick a
// 1.4 Mbps HEVC file over a 9 Mbps H.264 one, but a 20 GB HEVC over a 22 GB H.264 is fine.
func TestFormatBonusWaivedOnBitrateCollapse(t *testing.T) {
	profile := Profile{Name: "1080p, prefers HEVC", AllowedResolutions: []parser.Resolution{parser.Res1080p}, FormatScores: map[string]int{"HEVC": 50}}
	e := NewDefaultEngine()
	tiny := NewCandidate("Iron.Lung.2026.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir", 1.2, 100).WithRuntime(120)
	big := NewCandidate("Iron.Lung.2026.1080p.WEB-DL.DDP5.1.H.264-SCOPE", 8.1, 100).WithRuntime(120)
	d := e.Decide(profile, []Candidate{tiny, big})
	if d.Winner == nil || d.Winner.Candidate.Release.Group != "SCOPE" {
		t.Errorf("a collapsed-bitrate HEVC must not win on the format bonus alone: %s", winnerGroup(d))
	}
	for _, ev := range d.Eligible {
		if ev.Candidate.Release.Group == "NeoNoir" && !ev.BonusWaived {
			t.Error("the tiny release's bonus should be marked waived")
		}
	}
	comparable := NewCandidate("Iron.Lung.2026.1080p.WEB-DL.DDP5.1.x265-GRP", 20, 100).WithRuntime(120)
	bigger := NewCandidate("Iron.Lung.2026.1080p.WEB-DL.DDP5.1.H.264-SCOPE", 22, 100).WithRuntime(120)
	if d := e.Decide(profile, []Candidate{comparable, bigger}); d.Winner == nil || d.Winner.Candidate.Release.Group != "GRP" {
		t.Errorf("a comparable HEVC keeps its preference: %s", winnerGroup(d))
	}
	// No runtimes: nothing to compare, the preference stands.
	if d := e.Decide(profile, []Candidate{NewCandidate("Movie.2024.1080p.WEB-DL.x265-HEVCGRP", 1, 100), NewCandidate("Movie.2024.1080p.WEB-DL.x264-AVCGRP", 8, 100)}); d.Winner == nil || d.Winner.Candidate.Release.Group != "HEVCGRP" {
		t.Errorf("without runtimes the bonus is untouched: %s", winnerGroup(d))
	}
}

// The reasons say what decided it: a small-size lean is not "highest bitrate".
func TestWhyReasonsNameTheSizeLean(t *testing.T) {
	lean := Profile{Name: "lean", BitrateCapMbps: 40, SmallBias: 0.4}
	ev := Evaluation{Candidate: NewCandidate("Movie.2024.1080p.WEB-DL.x264-GRP", 5, 100)}
	joined := strings.Join(whyReasons(lean, ev), " | ")
	if !strings.Contains(joined, "Best quality for the size") || strings.Contains(joined, "Highest bitrate") {
		t.Errorf("reasons = %q", joined)
	}
	capped := Profile{Name: "cap", BitrateCapMbps: 40}
	if joined := strings.Join(whyReasons(capped, ev), " | "); !strings.Contains(joined, "Highest bitrate under your 40 Mbps ceiling") {
		t.Errorf("reasons = %q", joined)
	}
}
