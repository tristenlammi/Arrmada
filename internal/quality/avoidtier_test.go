package quality

import (
	"strings"
	"testing"
)

// "Avoid Dolby Vision" must mean "never pick a DV release while a non-DV one exists" — a
// strict lower tier, not a score penalty a better source can outweigh. Here the DV release
// is a 4K REMUX (far higher raw quality) and the alternative is a 1080p WEB-DL; the clean
// one must still win.
func TestAvoidedFormatLosesToLowerQualityAlternative(t *testing.T) {
	e := NewDefaultEngine()
	p := Profile{FormatScores: map[string]int{"Dolby Vision": -50, "HDR10": 50, "Atmos": 50}}

	dv := NewCandidate("Obsession 2026 2160p BluRay REMUX DV HDR TrueHD Atmos 7.1-x", 40, 300)
	plain := NewCandidate("Obsession 2026 1080p WEB-DL HDR x265-y", 8, 300)

	d := e.Decide(p, []Candidate{dv, plain})
	if d.Winner == nil || d.Winner.Candidate.Name != plain.Name {
		got := "<nil>"
		if d.Winner != nil {
			got = d.Winner.Candidate.Name
		}
		t.Fatalf("the non-DV release must win despite lower quality, got %q", got)
	}
	if !strings.Contains(d.ChosenOver, "Dolby Vision") {
		t.Errorf("ChosenOver should explain the DV release was skipped, got %q", d.ChosenOver)
	}

	// But a DV release is still grabbable when it's the ONLY option — with a heads-up.
	only := e.Decide(p, []Candidate{dv})
	if only.Winner == nil || only.Winner.Candidate.Name != dv.Name {
		t.Fatalf("with no alternative the DV release should still win")
	}
	found := false
	for _, w := range only.Why {
		if strings.Contains(w, "Only releases with a format you avoid") {
			found = true
		}
	}
	if !found {
		t.Errorf("a DV-only win should flag that only avoided releases were available, got %v", only.Why)
	}
}
