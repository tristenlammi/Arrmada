package books

import "testing"

func TestCatalogueLink(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		key, slug, title, author string
		want                     CatalogueRef
	}{
		{"hardcover with slug", "hc:123", "dune", "Dune", "Frank Herbert",
			CatalogueRef{"Hardcover", "https://hardcover.app/books/dune"}},
		{"hardcover without slug searches title and author", "hc:123", "", "Dune: Part One", "Frank Herbert",
			CatalogueRef{"Hardcover", "https://hardcover.app/search?q=Dune%3A+Part+One+Frank+Herbert"}},
		{"hardcover with nothing to search", "hc:123", "", "", "",
			CatalogueRef{"Hardcover", "https://hardcover.app"}},
		{"hardcover author key is not a book", "hc:a:42", "", "Dune", "Frank Herbert", CatalogueRef{}},
		{"google books", "gb:abc", "", "Dune", "",
			CatalogueRef{"Google Books", "https://books.google.com/books?id=abc"}},
		{"google books without an id", "gb:", "", "Dune", "", CatalogueRef{}},
		{"open library works path", "/works/OL123W", "", "Dune", "",
			CatalogueRef{"Open Library", "https://openlibrary.org/works/OL123W"}},
		{"open library bare id", "OL123W", "", "Dune", "",
			CatalogueRef{"Open Library", "https://openlibrary.org/works/OL123W"}},
		{"open library edition is not a work", "OL123M", "", "Dune", "", CatalogueRef{}},
		{"empty", "", "", "Dune", "", CatalogueRef{}},
		{"unknown", "isbn:9780441013593", "", "Dune", "", CatalogueRef{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CatalogueLink(tc.key, tc.slug, tc.title, tc.author); got != tc.want {
				t.Errorf("CatalogueLink(%q, %q, %q, %q) = %+v, want %+v", tc.key, tc.slug, tc.title, tc.author, got, tc.want)
			}
		})
	}
}
