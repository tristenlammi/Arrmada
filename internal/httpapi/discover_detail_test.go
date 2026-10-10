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

// detailDiscovery answers MediaDetails with a fixed record; every other feed is unused.
type detailDiscovery struct {
	metadata.DiscoveryProvider
	d *metadata.MediaDetail
}

func (s detailDiscovery) Available() bool { return true }
func (s detailDiscovery) MediaDetails(context.Context, string, int) (*metadata.MediaDetail, error) {
	cp := *s.d
	return &cp, nil
}

// detailAPI is an api whose Discover enrichment snapshot is fixed, so the card's state
// comes from the maps given rather than from a library, request list and queue.
func detailAPI(t *testing.T, d *metadata.MediaDetail, snap *discoverEnrichSnap) *api {
	t.Helper()
	a := &api{deps: Deps{Discovery: detailDiscovery{d: d}}}
	enrichSnaps.Store(a, &enrichSnapEntry{at: time.Now().Add(time.Hour), snap: snap})
	t.Cleanup(func() { enrichSnaps.Delete(a) })
	return a
}

func fetchDetail(a *api, media, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/v1/media/"+media+"/"+id, nil)
	r.SetPathValue("media", media)
	r.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	a.handleMediaDetail(rec, r)
	return rec
}

func emptySnap() *discoverEnrichSnap {
	return &discoverEnrichSnap{
		movIn: map[int]bool{}, movHave: map[int]bool{}, serIn: map[int]bool{}, serHave: map[int]bool{},
		movWanted: map[int]bool{}, serWanted: map[int]bool{}, prog: map[string]float64{}, reqStatus: map[string]string{},
	}
}

// A title opened cold from its link carries the same badge state a row card would.
func TestMediaDetailIncludesCard(t *testing.T) {
	snap := emptySnap()
	snap.serIn[1399], snap.serHave[1399] = true, true
	snap.reqStatus["series:1399"] = "approved"
	snap.prog["series:1399"] = 0.5
	d := &metadata.MediaDetail{MediaType: "series", TMDBID: 1399, Title: "Saltwind", Year: 2011, PosterURL: "https://img/p.jpg",
		Genres: []string{"Drama", "Fantasy", "Adventure", "War"}, Ratings: metadata.Ratings{TMDB: 8.4}}
	rec := fetchDetail(detailAPI(t, d, snap), "series", "1399")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var body struct {
		Title string `json:"title"`
		Adult *bool  `json:"adult"`
		Card  struct {
			MediaType        string   `json:"media_type"`
			TMDBID           int      `json:"tmdb_id"`
			Title            string   `json:"title"`
			PosterURL        string   `json:"poster_url"`
			Genres           []string `json:"genres"`
			VoteAverage      float64  `json:"vote_average"`
			InLibrary        bool     `json:"in_library"`
			HasFile          bool     `json:"has_file"`
			RequestStatus    string   `json:"request_status"`
			DownloadProgress float64  `json:"download_progress"`
		} `json:"card"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	c := body.Card
	if body.Title != "Saltwind" || c.MediaType != "series" || c.TMDBID != 1399 || c.Title != "Saltwind" || c.PosterURL != "https://img/p.jpg" {
		t.Errorf("card identity = %+v (title %q)", c, body.Title)
	}
	if !c.InLibrary || !c.HasFile || c.RequestStatus != "approved" || c.DownloadProgress != 0.5 {
		t.Errorf("card state = %+v", c)
	}
	if len(c.Genres) != 3 || c.VoteAverage != 8.4 {
		t.Errorf("card genres %v, vote %v: want three genres and 8.4", c.Genres, c.VoteAverage)
	}
	if body.Adult != nil {
		t.Errorf("adult is in the response: %v", *body.Adult)
	}
}

// The adult filter is always on: a deep link can't surface a title the rows filter out,
// whether TMDB flags it or its title trips the shared filter.
func TestMediaDetailHidesAdult(t *testing.T) {
	for name, d := range map[string]*metadata.MediaDetail{
		"tmdb flag":    {MediaType: "movie", TMDBID: 7, Title: "Quiet Evening", Adult: true},
		"title filter": {MediaType: "movie", TMDBID: 8, Title: "Brazzers Night Shift"},
	} {
		rec := fetchDetail(detailAPI(t, d, emptySnap()), "movie", "7")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", name, rec.Code)
		}
	}
}
