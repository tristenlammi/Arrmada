package parser

import "testing"

func TestIsLatin(t *testing.T) {
	for title, want := range map[string]bool{
		"Sousou no Frieren":             true,
		"Pokémon":                       true,
		"Shingeki no Kyojin: The Final": true,
		"1923":                          false, // no letters at all
		"葬送のフリーレン":                      false,
		"進撃の巨人 Attack on Titan":         false, // mixed scripts: not a release title
		"Мастер и Маргарита":            false,
		"":                              false,
	} {
		if got := IsLatin(title); got != want {
			t.Errorf("IsLatin(%q) = %v, want %v", title, got, want)
		}
	}
}
