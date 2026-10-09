package books

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// CatalogueRef names the catalogue a book's metadata came from and links to the book there.
// Book pages used to assume Open Library for every book, which gave Hardcover and Google
// Books entries a badge pointing at a page that doesn't exist.
type CatalogueRef struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

var olWorkID = regexp.MustCompile(`^OL\d+W$`)

// CatalogueLink works out where a book key lives. slug is Hardcover's page name for the
// book when known; without it the link is a Hardcover search for the title and author,
// which lands on the right book in one click. A key from no known catalogue returns the
// zero value, and the page shows no badge rather than a broken link.
func CatalogueLink(key, slug, title, author string) CatalogueRef {
	key = strings.TrimSpace(key)
	switch {
	case strings.HasPrefix(key, "hc:a:"):
		return CatalogueRef{} // an author key, never a book's
	case metadata.IsHardcoverKey(key):
		if slug = strings.TrimSpace(slug); slug != "" {
			return CatalogueRef{Name: "Hardcover", URL: "https://hardcover.app/books/" + url.PathEscape(slug)}
		}
		q := strings.TrimSpace(strings.TrimSpace(title) + " " + strings.TrimSpace(author))
		if q == "" {
			return CatalogueRef{Name: "Hardcover", URL: "https://hardcover.app"}
		}
		return CatalogueRef{Name: "Hardcover", URL: "https://hardcover.app/search?q=" + url.QueryEscape(q)}
	case strings.HasPrefix(key, "gb:"):
		id := strings.TrimPrefix(key, "gb:")
		if id == "" {
			return CatalogueRef{}
		}
		return CatalogueRef{Name: "Google Books", URL: "https://books.google.com/books?id=" + url.QueryEscape(id)}
	}
	if id := strings.TrimPrefix(key, "/works/"); olWorkID.MatchString(id) {
		return CatalogueRef{Name: "Open Library", URL: "https://openlibrary.org/works/" + id}
	}
	return CatalogueRef{}
}
