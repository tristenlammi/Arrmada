package adultfilter

import "testing"

// Clear erotica labels block; reading-age shelves and ordinary romance don't.
func TestBookIsAdult(t *testing.T) {
	for _, c := range []struct {
		name  string
		title string
		tags  []string
		want  bool
	}{
		{"erotica tag", "A Quiet Summer", []string{"Fiction", "Erotica"}, true},
		{"erotic romance tag", "A Quiet Summer", []string{"Erotic Romance"}, true},
		{"BDSM tag", "A Quiet Summer", []string{"bdsm"}, true},
		{"Google Books category path", "A Quiet Summer", []string{"Fiction / Romance / Erotica"}, true},
		{"Open Library subject", "A Quiet Summer", []string{"Erotic literature"}, true},
		{"pornography subject", "A Quiet Summer", []string{"Pornography"}, true},
		{"adult studio in the title", "Brazzers Presents", nil, true},
		{"romance", "Pride and Prejudice", []string{"Romance", "Classics", "Historical Fiction"}, false},
		{"new adult", "A Quiet Summer", []string{"New Adult", "Romance"}, false},
		{"young adult", "A Quiet Summer", []string{"Young Adult", "Fantasy"}, false},
		{"adult fiction", "A Quiet Summer", []string{"Adult Fiction"}, false},
		{"adult alone", "A Quiet Summer", []string{"Adult"}, false},
		{"eroticism is not erotica", "A Quiet Summer", []string{"Eroticism in art"}, false},
		{"no tags", "Dune", nil, false},
	} {
		if got := BookIsAdult(c.title, c.tags); got != c.want {
			t.Errorf("%s: BookIsAdult(%q, %q) = %v, want %v", c.name, c.title, c.tags, got, c.want)
		}
	}
}

func TestFilterBooks(t *testing.T) {
	type book struct {
		title string
		tags  []string
	}
	in := []book{{"Dune", []string{"Science Fiction"}}, {"Night Games", []string{"Erotica"}}, {"Emma", nil}}
	got := FilterBooks(in, func(b book) (string, []string) { return b.title, b.tags })
	if len(got) != 2 || got[0].title != "Dune" || got[1].title != "Emma" {
		t.Errorf("FilterBooks = %+v", got)
	}
}
