package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
)

// bookQuery must not carry the edition. It used to append "audiobook" for audio searches,
// which reads sensibly against a general tracker and is fatal against a book tracker:
// MyAnonaMouse matches all query words against title/author/narrator/series, and no
// listing's metadata contains that word, so every audiobook search returned zero.
func TestBookQueryCarriesNoEditionWord(t *testing.T) {
	b := books.Book{Title: "The Coven", Author: "Harper L. Woods"}
	if got, want := bookQuery(b), "Harper L. Woods The Coven"; got != want {
		t.Errorf("query = %q, want %q", got, want)
	}
	// Whatever edition is being searched for, the text is the same — the edition rides on
	// SearchQuery.BookEdition so each indexer can apply it its own way.
	for _, kind := range []string{books.KindEbook, books.KindAudiobook} {
		if got := bookQuery(b); got != "Harper L. Woods The Coven" {
			t.Errorf("%s: query = %q, want the edition kept out of the text", kind, got)
		}
	}
	// No author on record: the title alone, with no stray leading space.
	if got, want := bookQuery(books.Book{Title: "The Coven"}), "The Coven"; got != want {
		t.Errorf("author-less query = %q, want %q", got, want)
	}
}
