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

// Edition and series notes go; the rest of the title stays.
func TestIdentityStripsNotesOnly(t *testing.T) {
	for in, want := range map[string]string{
		"Dune":                                  "dune",
		"Dune: Deluxe Edition":                  "dune",
		"Dune (Dune Chronicles, #1)":            "dune",
		"Dune (Dune Chronicles, Book 1)":        "dune",
		"Dune: Book One of the Dune Chronicles": "dune",
		"Dune [Illustrated]":                    "dune",
		"The Hobbit":                            "hobbit",
		"An Absolutely Remarkable Thing":        "absolutelyremarkablething",
		"A":                                     "a", // never strip a title down to nothing
		"Thrawn: Alliances":                     "thrawnalliances",
		"Mistborn: The Final Empire: 10th Anniversary Edition": "mistbornthefinalempire",
		"The Way of Kings: The Stormlight Archive, Book 1":     "wayofkings",
	} {
		if got := IdentityOf(in, "x").Full; got != want {
			t.Errorf("IdentityOf(%q).Full = %q, want %q", in, got, want)
		}
	}
	if got := IdentityOf("Mistborn: The Final Empire", "x").Sub; got != "finalempire" {
		t.Errorf("Sub = %q, want finalempire", got)
	}
	if got := IdentityOf("White Sand: Volume 1", "x").Sub; got != "" {
		t.Errorf("a bare volume number is not a subtitle: %q", got)
	}
}

// Same-author books that share a prefix are different books; catalogue renderings of
// one book are the same book.
func TestSameBook(t *testing.T) {
	const author = "Timothy Zahn"
	distinct := [][2]string{
		{"Thrawn", "Thrawn: Alliances"},
		{"Mistborn: The Final Empire", "Mistborn: Secret History"},
		{"Dune: House Atreides", "Dune: House Harkonnen"},
		{"Halo: The Fall of Reach", "Halo: First Strike"},
		{"Heir to the Empire: Star Wars", "Dark Force Rising: Star Wars"},
		{"Series: Book 1", "Other: Book 1"},
		{"Saga: Volume 1", "Saga: Volume 2"},
		{"Dune", "Dune - The Graphic Novel"},
		{"Dune Messiah", "Dune"},
	}
	for _, p := range distinct {
		if SameBook(p[0], author, p[1], author) || SameBook(p[1], author, p[0], author) {
			t.Errorf("%q and %q read as one book", p[0], p[1])
		}
	}
	same := [][2]string{
		{"Dune", "Dune: Deluxe Edition"},
		{"Dune", "Dune (Dune Chronicles, #1)"},
		{"Dune", "Dune: Book One of the Dune Chronicles"},
		{"Mistborn: The Final Empire", "The Final Empire"},
		{"Star Wars: Thrawn: Alliances", "Thrawn: Alliances"},
		{"White Sand Vol. 1", "White Sand #1"},
		{"White Sand Volume 1", "White Sand: Volume 1"},
		{"The Songbird & the Heart of Stone", "The Songbird and the Heart of Stone"},
		{"Heir to the Empire (Star Wars)", "Heir to the Empire"},
	}
	for _, p := range same {
		if !SameBook(p[0], author, p[1], author) || !SameBook(p[1], author, p[0], author) {
			t.Errorf("%q and %q should be one book: %+v vs %+v", p[0], p[1], IdentityOf(p[0], author), IdentityOf(p[1], author))
		}
	}
	// An author on one side only is not a match; on neither side it is.
	if SameBook("Beloved", "", "Beloved", "Toni Morrison") {
		t.Error("a blank author matched a set one")
	}
	if !SameBook("Beloved", "", "Beloved", " ") {
		t.Error("two blank authors should match")
	}
	if SameBook("Beloved", "Toni Morrison", "Beloved", "Bertrice Small") {
		t.Error("different authors matched")
	}
}

// Find reads a title against the library either way round and, among equal matches,
// picks the row with files, then the oldest.
func TestIdentityIndexFind(t *testing.T) {
	idx := NewIdentityIndex([]Book{
		{ID: 1, Title: "Mistborn: The Final Empire", Author: "Brandon Sanderson"},
		{ID: 2, Title: "The Final Empire", Author: "Brandon Sanderson", HasFile: true},
		{ID: 3, Title: "Thrawn", Author: "Timothy Zahn"},
		{ID: 4, Title: "Thrawn", Author: "Timothy Zahn"},
	})
	if b, ok := idx.Find("Mistborn: The Final Empire", "Sanderson, Brandon"); !ok || b.ID != 1 {
		t.Errorf("exact title: got %d %v, want 1", b.ID, ok)
	}
	// "The Final Empire" is row 2's whole title (with files) and row 1's subtitle: the
	// whole-title match comes first.
	if b, ok := idx.Find("The Final Empire", "Brandon Sanderson"); !ok || b.ID != 2 {
		t.Errorf("subtitle read: got %d %v, want 2", b.ID, ok)
	}
	if b, ok := idx.Find("Thrawn", "Timothy Zahn"); !ok || b.ID != 3 {
		t.Errorf("tie: got %d, want the oldest (3)", b.ID)
	}
	if _, ok := idx.Find("Thrawn: Alliances", "Timothy Zahn"); ok {
		t.Error("a prefix sibling matched")
	}
	idx.Add(Book{ID: 5, Title: "Thrawn", Author: "Timothy Zahn", HasFile: true})
	if b, _ := idx.Find("Thrawn", "Timothy Zahn"); b.ID != 5 {
		t.Errorf("got %d, want the row with files (5)", b.ID)
	}
	if n := len(idx.FindAll("Thrawn", "Timothy Zahn")); n != 3 {
		t.Errorf("FindAll = %d rows, want 3", n)
	}
}
