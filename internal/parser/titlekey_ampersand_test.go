package parser

import "testing"

// "Bride & Prejudice" was grabbed correctly and then held for review for an hour, because
// YTS names its folder "Bride Prejudice (2004)" — an ampersand is awkward in a filename,
// so it's deleted rather than spelled out. Expanding "&" to "and" made those two different
// keys, and the library ended up with the film under a folder missing the ampersand.
func TestTitleKeyAgreesAcrossAmpersandSpellings(t *testing.T) {
	groups := [][]string{
		// Dotted separators included: a release name reaches this after Parse has taken the
		// year and quality off, but the separators are still whatever the group used.
		{"Bride & Prejudice", "Bride and Prejudice", "Bride Prejudice",
			"Bride.&.Prejudice", "Bride.and.Prejudice", "Bride Prejudice"},
		{"Law & Order", "Law and Order", "Law Order"},
		{"Fire & Ice", "Fire.and.Ice", "Fire Ice"},
	}
	for _, g := range groups {
		want := TitleKey(g[0])
		if want == "" {
			t.Fatalf("TitleKey(%q) is empty", g[0])
		}
		for _, variant := range g[1:] {
			if got := TitleKey(variant); got != want {
				t.Errorf("TitleKey(%q) = %q, want %q (same as %q)", variant, got, want, g[0])
			}
		}
	}
}

// Only the standalone conjunction is dropped. A title whose words merely contain those
// letters must keep them, or unrelated films start colliding.
func TestTitleKeyKeepsAndInsideWords(t *testing.T) {
	cases := map[string]string{
		"Andrew":            "andrew",
		"Bandit":            "bandit",
		"The Sandlot":       "thesandlot",
		"Android":           "android",
		"Andor":             "andor",
		"Sand and Sorrow":   "sandsorrow",
		"Pokémon":           "pokemon",
		"Bride & Prejudice": "brideprejudice",
	}
	for in, want := range cases {
		if got := TitleKey(in); got != want {
			t.Errorf("TitleKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// The keys that must stay APART: dropping the conjunction must not blur distinct films.
func TestTitleKeyStillSeparatesDifferentTitles(t *testing.T) {
	pairs := [][2]string{
		{"Bride & Prejudice", "Pride and Prejudice"},
		{"Dune", "Dune Messiah"},
		{"Law & Order", "Law & Order SVU"},
	}
	for _, p := range pairs {
		if TitleKey(p[0]) == TitleKey(p[1]) {
			t.Errorf("TitleKey collapsed %q and %q to the same key %q", p[0], p[1], TitleKey(p[0]))
		}
	}
}
