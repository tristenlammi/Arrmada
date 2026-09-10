package books

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// The re-match must land on the catalogue's entry for the same book across the ways
// catalogues render it, and must not land on a different book of the same title.
func TestMatchUpgrade(t *testing.T) {
	book := Book{Title: "Dust", Author: "Hugh Howey"}
	results := []metadata.BookResult{
		{Key: "hc:1", Title: "Dust", Author: "Elizabeth Bear"},
		{Key: "hc:2", Title: "Dust (Silo, #3)", Author: "Howey, Hugh"},
		{Key: "hc:3", Title: "Wool", Author: "Hugh Howey"},
	}
	if m := matchUpgrade(book, results); m == nil || m.Key != "hc:2" {
		t.Errorf("matched %+v, want hc:2 (same title, author shares 'howey')", m)
	}
	// Exact key match wins over an overlap.
	exact := append([]metadata.BookResult{{Key: "hc:9", Title: "Dust", Author: "Hugh Howey"}}, results...)
	if m := matchUpgrade(book, exact); m == nil || m.Key != "hc:9" {
		t.Errorf("matched %+v, want the exact hc:9", m)
	}
	// Same title, different author, no overlap: not a match.
	if m := matchUpgrade(book, results[:1]); m != nil {
		t.Errorf("matched a different author's Dust: %+v", m)
	}
	// No author on the library side: the single same-title result is taken.
	if m := matchUpgrade(Book{Title: "Dust"}, results[1:2]); m == nil || m.Key != "hc:2" {
		t.Errorf("authorless book didn't take the sole same-title result: %+v", m)
	}
	if m := matchUpgrade(Book{Title: "Dust"}, results[:2]); m == nil || m.Key != "hc:1" {
		t.Errorf("authorless book with two same-title results should take the first: %+v", m)
	}
}

func TestAuthorsOverlap(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"Hugh Howey", "Howey, Hugh", true},
		{"H. Howey", "Hugh Howey", true},
		{"J.K. Rowling", "Rowling, J. K.", true},
		{"Hugh Howey", "Elizabeth Bear", false},
		{"Jo Nesbø", "Jo Smith", false}, // "jo" is too short to count
	} {
		if got := authorsOverlap(c.a, c.b); got != c.want {
			t.Errorf("authorsOverlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// The re-match must read titles the way catalogues write them, and take an
// authorless catalogue entry when that is all the catalogue has under the title.
func TestMatchUpgradeReadsSubtitlesAndAuthorlessEntries(t *testing.T) {
	book := Book{Title: "The Final Empire", Author: "Brandon Sanderson"}
	results := []metadata.BookResult{
		{Key: "hc:k", Title: "The Final Empire", Author: "Wm. H. Kötke"},
		{Key: "hc:m", Title: "Mistborn: The Final Empire", Author: "Brandon Sanderson"},
	}
	if m := matchUpgrade(book, results); m == nil || m.Key != "hc:m" {
		t.Errorf("matched %+v, want the Mistborn entry (subtitle is the title)", m)
	}
	sky := Book{Title: "Skyward Flight", Author: "Brandon Sanderson"}
	if m := matchUpgrade(sky, []metadata.BookResult{{Key: "hc:s", Title: "Skyward Flight"}}); m == nil || m.Key != "hc:s" {
		t.Errorf("an authorless same-title entry should be taken: %+v", m)
	}
	if m := matchUpgrade(sky, []metadata.BookResult{{Key: "hc:x", Title: "Skyward Flight", Author: "Someone Else"}}); m != nil {
		t.Errorf("a different author's book of the same title must not match: %+v", m)
	}
	amp := Book{Title: "The Songbird and the Heart of Stone", Author: "Carissa Broadbent"}
	if m := matchUpgrade(amp, []metadata.BookResult{{Key: "hc:a", Title: "The Songbird & the Heart of Stone", Author: "Carissa Broadbent"}}); m == nil {
		t.Error("& and 'and' are the same title")
	}
}

// A record that is a guide to a book names the book and its author in its title.
func TestDerivedWork(t *testing.T) {
	for _, c := range []struct {
		title, author, work, by string
		ok                      bool
	}{
		{"LinguiSystems novel guide for Harry Potter and the goblet of fire by J.K. Rowling", "Laura Sauser", "Harry Potter and the goblet of fire", "J.K. Rowling", true},
		{"Harry Potter and the prisoner of Azkaban by J.K. Rowling", "Linda Ward Beech", "Harry Potter and the prisoner of Azkaban", "J.K. Rowling", true},
		{"Dune by Frank Herbert", "Frank Herbert", "", "", false},
		{"Death by Chocolate", "Sally Berneathy", "", "", false},
		{"The Final Empire", "Brandon Sanderson", "", "", false},
	} {
		work, by, ok := derivedWork(c.title, c.author)
		if ok != c.ok || work != c.work || by != c.by {
			t.Errorf("derivedWork(%q, %q) = (%q, %q, %v), want (%q, %q, %v)", c.title, c.author, work, by, ok, c.work, c.by, c.ok)
		}
	}
}
