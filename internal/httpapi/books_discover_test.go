package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/series"
)

// mixedCatalogue answers every list with the same mix: ordinary books, a romance on the
// New Adult shelf, and two that are labelled erotica (one only past the three genres a
// card shows).
type mixedCatalogue struct{ metadata.BookProvider }

func (mixedCatalogue) Available() bool { return true }

func (mixedCatalogue) list() []metadata.BookResult {
	return []metadata.BookResult{
		{Key: "OL1W", Title: "Dune", Author: "Frank Herbert", Genres: []string{"Science Fiction"}},
		{Key: "OL2W", Title: "Summer Hearts", Author: "A. Writer", Genres: []string{"Romance", "New Adult"}, Tags: []string{"Young Adult"}},
		{Key: "OL3W", Title: "Velvet Nights", Author: "B. Writer", Tags: []string{"Fiction", "Erotica"}},
		{Key: "OL4W", Title: "Locked Door", Author: "C. Writer", Genres: []string{"Romance", "Contemporary", "Drama"}, Tags: []string{"Romance", "Contemporary", "Drama", "Erotic Romance"}},
	}
}

func (c mixedCatalogue) SearchBooks(context.Context, string) ([]metadata.BookResult, error) {
	return c.list(), nil
}
func (c mixedCatalogue) TrendingBooks(context.Context) ([]metadata.BookResult, error) {
	return c.list(), nil
}
func (c mixedCatalogue) BooksBySubject(context.Context, string, int) ([]metadata.BookResult, error) {
	return c.list(), nil
}
func (c mixedCatalogue) AuthorWorks(context.Context, string, int) ([]metadata.BookResult, error) {
	return c.list(), nil
}
func (mixedCatalogue) SearchAuthors(context.Context, string) ([]metadata.AuthorResult, error) {
	return nil, nil
}
func (c mixedCatalogue) GetBook(_ context.Context, key string) (*metadata.BookDetails, error) {
	for _, b := range c.list() {
		if b.Key == key {
			return &metadata.BookDetails{BookResult: b, Description: "…"}, nil
		}
	}
	return &metadata.BookDetails{BookResult: metadata.BookResult{Key: key, Title: "Plain"}, Subjects: []string{"Erotic literature"}}, nil
}

// Every Books Discover list drops books labelled erotica, for every role, while ordinary
// and New/Young Adult romance stays; the discover detail of a flagged key is a 404; and
// the staff Add-book search still finds them.
func TestBookDiscoverFiltersAdult(t *testing.T) {
	root := t.TempDir()
	s := newRouteServer(t, func(d *Deps) {
		db := d.Store.DB()
		d.Books = books.NewService(db, mixedCatalogue{}, d.Log)
		mv := movies.NewService(db, nil, nil, root, "", nil, d.Log)
		sr := series.NewService(db, nil, root, d.Log)
		d.Requests = requests.NewService(db, mv, sr, d.Books, nil, nil, nil, "", d.Log)
	})
	_, admin := s.user(t, "owner@example.com", auth.RoleAdmin)
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)

	keys := func(body []byte, field string) string {
		var raw map[string]json.RawMessage
		var got []struct {
			Key  string   `json:"key"`
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("decoding %s: %v", body, err)
		}
		if err := json.Unmarshal(raw[field], &got); err != nil {
			t.Fatalf("decoding %s: %v", raw[field], err)
		}
		var ks []string
		for _, b := range got {
			if b.Tags != nil {
				t.Errorf("%s carries filter-only tags: %v", b.Key, b.Tags)
			}
			ks = append(ks, b.Key)
		}
		return strings.Join(ks, ",")
	}

	for _, path := range []string{
		"/api/v1/books/discover/browse/trending",
		"/api/v1/books/discover/trending",
		"/api/v1/books/discover/search?q=night",
		"/api/v1/books/discover/subjects/romance",
		"/api/v1/books/discover/authors/OL1A/works",
	} {
		for who, c := range map[string]*http.Cookie{"admin": admin, "requester": kid} {
			rec := s.do("GET", path, c)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s as %s: HTTP %d %s", path, who, rec.Code, rec.Body)
			}
			if got := keys(rec.Body.Bytes(), "books"); got != "OL1W,OL2W" {
				t.Errorf("%s as %s: books %s, want OL1W,OL2W", path, who, got)
			}
		}
	}

	for key, want := range map[string]int{"OL1W": 200, "OL2W": 200, "OL3W": 404, "OL4W": 404, "OL9W": 404} {
		if rec := s.do("GET", "/api/v1/books/discover/detail?key="+key, kid); rec.Code != want {
			t.Errorf("detail %s: HTTP %d, want %d", key, rec.Code, want)
		} else if want == 200 && strings.Contains(rec.Body.String(), `"tags"`) {
			t.Errorf("detail %s carries filter-only tags: %s", key, rec.Body)
		}
	}

	// The staff Add-book search is deliberately unfiltered.
	rec := s.do("GET", "/api/v1/books/lookup?q=night", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("lookup: HTTP %d", rec.Code)
	}
	if got := keys(rec.Body.Bytes(), "results"); got != "OL1W,OL2W,OL3W,OL4W" {
		t.Errorf("lookup = %s, want all four", got)
	}
}
