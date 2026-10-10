package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// collectionsDiscovery answers GetCollection from a table; an id it doesn't know fails.
type collectionsDiscovery struct {
	metadata.DiscoveryProvider
	colls map[int]*metadata.Collection
}

func (collectionsDiscovery) Available() bool { return true }
func (d collectionsDiscovery) GetCollection(_ context.Context, id int) (*metadata.Collection, error) {
	if c, ok := d.colls[id]; ok {
		return c, nil
	}
	return nil, metadata.ErrNotFound
}

func member(id int, title, date string) metadata.MovieResult {
	return metadata.MovieResult{TMDBID: id, Title: title, Year: 2000, PosterURL: "p", ReleaseDate: date}
}

func ownedIn(tmdb, coll int) movies.Movie {
	return movies.Movie{TMDBID: tmdb, Extra: &movies.MovieExtra{CollectionID: coll, CollectionName: "c"}}
}

// One row per started collection with at least two released films still to get, the most
// complete first, unreleased members left for Upcoming.
func TestCollectionsRowsGrouped(t *testing.T) {
	colls := map[int]*metadata.Collection{
		1: {ID: 1, Name: "Alien Collection", Members: []metadata.MovieResult{
			member(11, "Alien", "1979-05-25"), member(12, "Aliens", "1986-07-18"), member(13, "Alien 3", "1992-05-22"), member(14, "Alien 9", "2099-01-01"),
		}},
		2: {ID: 2, Name: "Harbour Collection", Members: []metadata.MovieResult{
			member(21, "Harbour", "2001-01-01"), member(22, "Harbour II", "2003-01-01"), member(23, "Harbour III", "2005-01-01"), member(24, "Harbour IV", "2007-01-01"),
		}},
		3: {ID: 3, Name: "Tide Collection", Members: []metadata.MovieResult{member(31, "Tide", "2010-01-01"), member(32, "Tide II", "2012-01-01")}},
	}
	library := []movies.Movie{ownedIn(11, 1), ownedIn(21, 2), ownedIn(22, 2), ownedIn(31, 3), ownedIn(41, 4)}
	rows := buildCollectionRows(context.Background(), collectionsDiscovery{colls: colls}, library)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want Harbour then Alien", rows)
	}
	if rows[0].id != 2 || rows[0].title != "Complete the Harbour Collection" || len(rows[0].items) != 2 {
		t.Errorf("first row = %+v", rows[0])
	}
	if rows[1].id != 1 || rows[1].title != "Complete the Alien Collection" {
		t.Errorf("second row = %+v", rows[1])
	}
	for _, it := range rows[1].items {
		if it.TMDBID == 11 || it.TMDBID == 14 {
			t.Errorf("Alien row holds %d (owned or unreleased)", it.TMDBID)
		}
	}
}

// At most four rows.
func TestCollectionsRowsCapped(t *testing.T) {
	colls := map[int]*metadata.Collection{}
	var library []movies.Movie
	for id := 1; id <= 6; id++ {
		colls[id] = &metadata.Collection{ID: id, Name: "C", Members: []metadata.MovieResult{
			member(id*10, "a", "2000-01-01"), member(id*10+1, "b", "2000-01-01"), member(id*10+2, "c", "2000-01-01"),
		}}
		library = append(library, ownedIn(id*10, id))
	}
	if rows := buildCollectionRows(context.Background(), collectionsDiscovery{colls: colls}, library); len(rows) != collectionsRowsMax {
		t.Errorf("%d rows, want %d", len(rows), collectionsRowsMax)
	}
}

func fetchCollection(a *api, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/v1/discover/collection/"+id, nil)
	r.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	a.handleDiscoverCollection(rec, r)
	return rec
}

// The collection page: every member the provider returns, with badges, and how much of
// what's out is here. One with no members left (the provider drops adult ones) or that
// TMDB doesn't know is a 404.
func TestCollectionEndpointAdultFilter(t *testing.T) {
	snap := emptySnap()
	snap.movIn[11], snap.movHave[11] = true, true
	a := &api{deps: Deps{Discovery: collectionsDiscovery{colls: map[int]*metadata.Collection{
		1: {ID: 1, Name: "Alien Collection", Overview: "In space…", Members: []metadata.MovieResult{
			member(11, "Alien", "1979-05-25"), member(12, "Aliens", "1986-07-18"), member(14, "Alien 9", "2099-01-01"),
		}},
		2: {ID: 2, Name: "Emptied", Members: nil},
	}}}}
	enrichSnaps.Store(a, &enrichSnapEntry{at: time.Now().Add(time.Hour), snap: snap})
	t.Cleanup(func() { enrichSnaps.Delete(a) })

	rec := fetchCollection(a, "1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body collectionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Name != "Alien Collection" || len(body.Items) != 3 || body.Owned != 1 || body.Total != 2 || !body.Items[0].HasFile {
		t.Errorf("body = %s", rec.Body)
	}
	for _, id := range []string{"2", "3"} {
		if rec := fetchCollection(a, id); rec.Code != http.StatusNotFound {
			t.Errorf("collection %s: status %d, want 404", id, rec.Code)
		}
	}
}
