package books

import (
	"reflect"
	"testing"
)

func TestFoldVariants(t *testing.T) {
	cases := map[string][]string{
		"Dune":           {"dune"},
		"Ender's Game":   {"enders game", "ender s game"},
		"Ender’s Game":   {"enders game", "ender s game"},
		"L'Étranger":     {"letranger", "l etranger"},
		"Pokémon":        {"pokemon"},
		"Fire & Blood":   {"fire blood"},
		"Fire and Blood": {"fire blood"},
		"!!!":            {""},
	}
	for in, want := range cases {
		if got := foldVariants(in); !reflect.DeepEqual(got, want) {
			t.Errorf("foldVariants(%q) = %q, want %q", in, got, want)
		}
	}
}

// The file matcher folds the same way as the release matcher.
func TestMatchFileNameFolds(t *testing.T) {
	lib := []Book{
		{ID: 1, Title: "Pokémon Adventures, Vol. 1", Author: "Hidenori Kusaka"},
		{ID: 2, Title: "Ender's Game", Author: "Orson Scott Card"},
		{ID: 3, Title: "Pride & Prejudice", Author: "Jane Austen"},
		{ID: 4, Title: "It", Author: "Stephen King"},
		{ID: 5, Title: "Dune", Author: "Frank Herbert"},
		{ID: 6, Title: "Dune Messiah", Author: "Frank Herbert"},
	}
	for name, want := range map[string]int64{
		"Pokemon Adventures Vol 1.epub":     1,
		"Orson.Scott.Card-Enders.Game.epub": 2,
		"Ender s Game - Part 01.mp3":        2,
		"Pride and Prejudice.epub":          3,
		"Frank Herbert - Dune Messiah.epub": 6,
		"Stephen King - It.epub":            4,
	} {
		got, ok := matchFileName(lib, name)
		if !ok || got.ID != want {
			t.Errorf("%q → book %d (ok=%v), want %d", name, got.ID, ok, want)
		}
	}
	for _, name := range []string{"Stephen King - The Institute.epub", "Its Not Summer.epub"} {
		if got, ok := matchFileName(lib, name); ok {
			t.Errorf("%q matched %q, want nothing", name, got.Title)
		}
	}
}

// Accents fold in book identity too, so the two spellings are one book.
func TestIdentityFoldsAccents(t *testing.T) {
	if IdentityOf("Pokémon Adventures, Vol. 1", "Hidenori Kusaka") != IdentityOf("Pokemon Adventures, Vol. 1", "Hidenori Kusaka") {
		t.Error("Pokémon and Pokemon gave two identities")
	}
	if IdentityOf("Love in the Time of Cholera", "Gabriel García Márquez") != IdentityOf("Love in the Time of Cholera", "Gabriel Garcia Marquez") {
		t.Error("García Márquez and Garcia Marquez gave two identities")
	}
	if !SameBook("Ender’s Game", "Orson Scott Card", "Ender's Game", "Orson Scott Card") {
		t.Error("a curly and a straight apostrophe gave two books")
	}
	if SameBook("Dune", "Frank Herbert", "Dune Messiah", "Frank Herbert") {
		t.Error("folding must not merge Dune and Dune Messiah")
	}
}
