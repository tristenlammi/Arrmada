package parser

import "testing"

// TestFoldAccents is the "Pokémon" case: a library title carrying diacritics has to
// fold to the ASCII spelling scene releases actually use, or it finds/matches nothing.
func TestFoldAccents(t *testing.T) {
	cases := map[string]string{
		"Pokémon Heroes":    "Pokemon Heroes",
		"Pokémon":           "Pokemon",
		"Amélie":            "Amelie",
		"Léon: The Ferrari": "Leon: The Ferrari",
		"Coração":           "Coracao",
		"Jägermeister":      "Jagermeister",
		"Straße":            "Strasse",
		"Æon Flux":          "AEon Flux",
		"Dune 2024":         "Dune 2024", // ASCII passes through untouched
		"":                  "",
		"日本語":               "日本語", // non-Latin untouched
	}
	for in, want := range cases {
		if got := FoldAccents(in); got != want {
			t.Errorf("FoldAccents(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestFoldAccentsCasePreserved checks uppercase accents keep their case.
func TestFoldAccentsCasePreserved(t *testing.T) {
	if got := FoldAccents("PokÉmon"); got != "PokEmon" {
		t.Errorf("FoldAccents(PokÉmon) = %q, want PokEmon", got)
	}
}
