package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// pagesDiscovery is a provider with the paged feeds: Browse refuses a bad sort and records
// what it was asked; SearchPage answers one page.
type pagesDiscovery struct {
	metadata.DiscoveryProvider
	asked *metadata.BrowseQuery
}

func (pagesDiscovery) Available() bool { return true }
func (p pagesDiscovery) Browse(_ context.Context, q metadata.BrowseQuery) (metadata.Page, error) {
	*p.asked = q
	if q.Sort == "bogus" {
		return metadata.Page{}, metadata.ErrBadQuery
	}
	return metadata.Page{Items: []metadata.DiscoverItem{{MediaType: "movie", TMDBID: 1, Title: "Harbour Lights"}}, Page: q.Page, TotalPages: 9}, nil
}
func (pagesDiscovery) SearchPage(_ context.Context, q string, page int) (metadata.SearchResults, error) {
	return metadata.SearchResults{Page: metadata.Page{Items: []metadata.DiscoverItem{{MediaType: "series", TMDBID: 2, Title: q}}, Page: page, TotalPages: 4}, People: []metadata.PersonResult{}}, nil
}

func pagesAPI(t *testing.T) (*api, *metadata.BrowseQuery) {
	t.Helper()
	asked := &metadata.BrowseQuery{}
	a := &api{deps: Deps{Discovery: pagesDiscovery{asked: asked}}}
	enrichSnaps.Store(a, &enrichSnapEntry{at: time.Now().Add(time.Hour), snap: emptySnap()})
	t.Cleanup(func() { enrichSnaps.Delete(a) })
	return a, asked
}

// The browse endpoint reads the filters from the address and answers a page with its
// total; a sort the provider refuses is a 400.
func TestDiscoverBrowseHandler(t *testing.T) {
	a, asked := pagesAPI(t)
	rec := httptest.NewRecorder()
	a.handleDiscoverBrowse(rec, httptest.NewRequest("GET", "/api/v1/discover/browse?media=movie&genre=878,x,12&year_from=1990&year_to=1999&rating=7&page=3", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if asked.Media != "movie" || len(asked.Genres) != 2 || asked.Genres[0] != 878 || asked.YearFrom != 1990 || asked.YearTo != 1999 || asked.RatingMin != 7 || asked.Page != 3 {
		t.Errorf("asked %+v", *asked)
	}
	var body struct {
		Items      []discoverCard `json:"items"`
		Page       int            `json:"page"`
		TotalPages int            `json:"total_pages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Page != 3 || body.TotalPages != 9 {
		t.Errorf("body = %+v", body)
	}

	rec = httptest.NewRecorder()
	a.handleDiscoverBrowse(rec, httptest.NewRequest("GET", "/api/v1/discover/browse?sort=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad sort: status %d, want 400", rec.Code)
	}
}

// Search is paged: the page goes through and the total comes back, with people.
func TestDiscoverSearchHandlerPaged(t *testing.T) {
	a, _ := pagesAPI(t)
	rec := httptest.NewRecorder()
	a.handleDiscoverSearch(rec, httptest.NewRequest("GET", "/api/v1/discover/search?q=star&page=2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Items      []discoverCard          `json:"items"`
		People     []metadata.PersonResult `json:"people"`
		Page       int                     `json:"page"`
		TotalPages int                     `json:"total_pages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].Title != "star" || body.Page != 2 || body.TotalPages != 4 || body.People == nil {
		t.Errorf("body = %s", rec.Body)
	}
}
