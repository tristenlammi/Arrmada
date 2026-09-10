package books

import "testing"

// The same novel as three catalogues describe it must collapse to one key; different
// books that merely share words must not.
func TestDedupeKeyToleratesCatalogueDifferences(t *testing.T) {
	same := [][2]string{
		{"Harry Potter and the Philosopher's Stone", "J. K. Rowling"},
		{"Harry Potter and the Philosopher's Stone", "J.K. Rowling"},
		{"Harry Potter and the Philosopher's Stone (Harry Potter, #1)", "Rowling, J.K."},
		{"Harry Potter and the Philosopher's Stone: Illustrated Edition", "J.K. ROWLING"},
		{"The Harry Potter and the Philosopher's Stone", "J. K. Rowling"},
	}
	want := DedupeKey(same[0][0], same[0][1])
	if want == "" {
		t.Fatal("empty key")
	}
	for _, s := range same[1:] {
		if got := DedupeKey(s[0], s[1]); got != want {
			t.Errorf("%q / %q → %q, want %q", s[0], s[1], got, want)
		}
	}
	if DedupeKey("A Game of Thrones", "George R. R. Martin") != DedupeKey("Game of Thrones", "Martin, George R.R.") {
		t.Error("initials and name order should not matter")
	}

	different := [][2]string{
		{"Harry Potter and the Chamber of Secrets", "J.K. Rowling"},
		{"Harry Potter and the Philosopher's Stone", "Jim Kay"},
	}
	for _, d := range different {
		if DedupeKey(d[0], d[1]) == want {
			t.Errorf("%q / %q collided with the first book", d[0], d[1])
		}
	}
	if DedupeKey("", "Someone") != "" {
		t.Error("a book with no title has no key")
	}
	// Two books called "Beloved" by different authors are different books.
	if DedupeKey("Beloved", "Toni Morrison") == DedupeKey("Beloved", "Bertrice Small") {
		t.Error("author must be part of the key")
	}
}

func TestTitleKeyStripsSubtitleAndArticle(t *testing.T) {
	for in, want := range map[string]string{
		"Dune":                           "dune",
		"Dune: Deluxe Edition":           "dune",
		"Dune (Dune Chronicles, Book 1)": "dune",
		"The Hobbit":                     "hobbit",
		"An Absolutely Remarkable Thing": "absolutelyremarkablething",
		"A":                              "a", // never strip a title down to nothing
	} {
		if got := titleKey(in); got != want {
			t.Errorf("titleKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// Catalogues render the same title with "&" or "and" and volume markers every way;
// a subtitle can be the whole of another catalogue's title.
func TestTitleKeysReadCataloguesAlike(t *testing.T) {
	same := [][2]string{
		{"The Songbird & the Heart of Stone", "The Songbird and the Heart of Stone"},
		{"White Sand, Vol. 1", "White Sand 1"},
		{"White Sand Volume 1", "White Sand #1"},
	}
	for _, p := range same {
		if titleKey(p[0]) != titleKey(p[1]) {
			t.Errorf("titleKey(%q)=%q != titleKey(%q)=%q", p[0], titleKey(p[0]), p[1], titleKey(p[1]))
		}
	}
	if !keysOverlap(titleKeys("The Final Empire"), titleKeys("Mistborn: The Final Empire")) {
		t.Errorf("a subtitle should read as the title: %v vs %v", titleKeys("The Final Empire"), titleKeys("Mistborn: The Final Empire"))
	}
	if keysOverlap(titleKeys("Dune Messiah"), titleKeys("Dune")) {
		t.Error("a more specific title must not match its series-mate")
	}
}
