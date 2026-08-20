package parser

import "testing"

// TitleKey is the one shared title normalizer: accents fold, "&" and "and" are the
// same word, and case/punctuation never matter. Search-side and import-side matching
// both key on it, so these equivalences are what keep grabs attachable on import.
func TestTitleKey(t *testing.T) {
	cases := []struct {
		name string
		a, b string
	}{
		{"accents fold", "Pokémon", "Pokemon"},
		{"ampersand equals and", "Love & Death", "Love and Death"},
		{"case insensitive", "THE ODYSSEY", "the odyssey"},
		{"punctuation insensitive", "Mission: Impossible - Fallout!", "Mission Impossible Fallout"},
		{"dots as separators", "Fast.&.Furious", "Fast and Furious"},
	}
	for _, tc := range cases {
		if ka, kb := TitleKey(tc.a), TitleKey(tc.b); ka != kb {
			t.Errorf("%s: TitleKey(%q)=%q != TitleKey(%q)=%q", tc.name, tc.a, ka, tc.b, kb)
		}
	}

	if got := TitleKey("Pokémon"); got != "pokemon" {
		t.Errorf("TitleKey(Pokémon) = %q, want %q", got, "pokemon")
	}
	// The conjunction is now dropped rather than spelled out, so that a release which
	// DELETED the ampersand keys the same as one that expanded it. See
	// TestTitleKeyAgreesAcrossAmpersandSpellings for why that third spelling matters.
	if got := TitleKey("Love & Death"); got != "lovedeath" {
		t.Errorf("TitleKey(Love & Death) = %q, want %q", got, "lovedeath")
	}
	if TitleKey("Love & Death") != TitleKey("Love Death") {
		t.Error("a release that dropped the ampersand must key the same as the library title")
	}
	if TitleKey("Dune") == TitleKey("Duel") {
		t.Error("different titles must not collide")
	}
}
