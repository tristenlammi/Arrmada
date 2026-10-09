package parser

import (
	"strings"
	"testing"
)

// ACQ-21: TitleKey is the one normalizer for every download, queue and library match, so
// it carries the union of the old copies' rules — including series' trailing-bracket drop.
func TestTitleKeySharedCases(t *testing.T) {
	same := [][2]string{
		{"Love & Death", "Love.and.Death"},
		{"Pokémon", "Pokemon"},
		{"Show [2019]", "Show"},
		{"Show (US)", "Show"},
		{"My Hero Academia (Boku no Hero Academia)", "My Hero Academia"},
		{"Attack on Titan [Shingeki no Kyojin]", "Attack on Titan"},
		{"Mission: Impossible – Fallout", "Mission.Impossible.Fallout"},
		{"Marvel's Agents of S.H.I.E.L.D.", "Marvels Agents of SHIELD"},
		// A group at the START is part of the title. The parser hands back "500) Days of
		// Summer" for "(500).Days.of.Summer.2009", so both sides must keep the number.
		{"(500) Days of Summer", "500) Days of Summer"},
		{"(500) Days of Summer", "500 Days of Summer"},
		{"(Un)Well", "Un)Well"},
	}
	for _, p := range same {
		if a, b := TitleKey(p[0]), TitleKey(p[1]); a != b {
			t.Errorf("TitleKey(%q)=%q != TitleKey(%q)=%q", p[0], a, p[1], b)
		}
	}
	differ := [][2]string{
		{"(500) Days of Summer", "Days of Summer"},
		{"Love & Death", "Love, Death & Robots"},
		{"Below Deck", "Below Deck Mediterranean"},
	}
	for _, p := range differ {
		if TitleKey(p[0]) == TitleKey(p[1]) {
			t.Errorf("TitleKey(%q) == TitleKey(%q); different titles must not collide", p[0], p[1])
		}
	}
	// TitleWords keeps brackets: episode-title and alias comparisons read what's inside.
	if got := strings.Join(TitleWords("The Return (Part 1)"), " "); got != "the return part 1" {
		t.Errorf("TitleWords(The Return (Part 1)) = %q", got)
	}
}
