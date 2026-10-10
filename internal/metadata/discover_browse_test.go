package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// fakeTMDB answers every request with body and records the queries it was asked.
func fakeTMDB(t *testing.T, body string) (*TMDB, func() []*url.URL) {
	t.Helper()
	var mu sync.Mutex
	var seen []*url.URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL)
		mu.Unlock()
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	tm := NewTMDB("k")
	tm.base = srv.URL
	return tm, func() []*url.URL { mu.Lock(); defer mu.Unlock(); return append([]*url.URL(nil), seen...) }
}

const emptyPage = `{"page":1,"total_pages":1,"results":[]}`

// Nothing the browser sends reaches TMDB raw: a bad sort, list or media is refused, the
// page is clamped, include_adult is always false and a vote floor is always set (higher
// for a rating sort).
func TestBrowseQueryWhitelist(t *testing.T) {
	ctx := context.Background()
	for name, q := range map[string]BrowseQuery{
		"sort":      {Media: "movie", Sort: "vote_count.asc;drop"},
		"list":      {Media: "movie", List: "adult"},
		"media":     {Media: "person"},
		"all":       {Media: "all", List: "popular"},
		"tv-income": {Media: "series", Sort: "revenue.desc"},
	} {
		tm, seen := fakeTMDB(t, emptyPage)
		if _, err := tm.Browse(ctx, q); !errors.Is(err, ErrBadQuery) {
			t.Errorf("%s: err = %v, want ErrBadQuery", name, err)
		}
		if n := len(seen()); n != 0 {
			t.Errorf("%s: %d TMDB calls for a refused query", name, n)
		}
	}

	cases := []struct {
		name  string
		q     BrowseQuery
		path  string
		check map[string]string
	}{
		{"page clamped high", BrowseQuery{Media: "movie", Page: 9999}, "/discover/movie", map[string]string{"page": "500", "sort_by": "popularity.desc", "vote_count.gte": "50"}},
		{"page clamped low", BrowseQuery{Media: "series", Page: -3}, "/discover/tv", map[string]string{"page": "1", "without_genres": noiseGenreCSV}},
		{"rating sort floor", BrowseQuery{Media: "movie", Sort: "vote_average.desc"}, "/discover/movie", map[string]string{"vote_count.gte": "200"}},
		{"filters", BrowseQuery{Media: "movie", Genres: []int{878}, YearFrom: 1999, YearTo: 1990, RatingMin: 7, RuntimeMax: 120, Provider: 8, Language: "EN"}, "/discover/movie",
			map[string]string{"with_genres": "878", "primary_release_date.gte": "1990-01-01", "primary_release_date.lte": "1999-12-31", "vote_average.gte": "7.0", "with_runtime.lte": "120", "with_watch_providers": "8", "watch_region": "US", "with_original_language": "en"}},
		{"series dates", BrowseQuery{Media: "series", Sort: "primary_release_date.desc", YearFrom: 2010}, "/discover/tv", map[string]string{"sort_by": "first_air_date.desc", "first_air_date.gte": "2010-01-01"}},
		{"bad language dropped", BrowseQuery{Media: "movie", Language: "en&x=1"}, "/discover/movie", map[string]string{"with_original_language": ""}},
		{"popular list", BrowseQuery{Media: "series", List: "popular", Page: 3}, "/tv/popular", map[string]string{"page": "3"}},
		{"trending all", BrowseQuery{Media: "all", List: "trending"}, "/trending/all/week", nil},
		{"upcoming", BrowseQuery{Media: "movie", List: "upcoming"}, "/movie/upcoming", nil},
		{"hidden gems", BrowseQuery{Media: "series", List: "hidden_gems"}, "/discover/tv", map[string]string{"vote_count.lte": "3000"}},
	}
	for _, c := range cases {
		tm, seen := fakeTMDB(t, emptyPage)
		if _, err := tm.Browse(ctx, c.q); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		calls := seen()
		if len(calls) != 1 {
			t.Fatalf("%s: %d calls", c.name, len(calls))
		}
		u := calls[0]
		if u.Path != c.path {
			t.Errorf("%s: path %s, want %s", c.name, u.Path, c.path)
		}
		got := u.Query()
		if got.Get("include_adult") != "false" {
			t.Errorf("%s: include_adult = %q", c.name, got.Get("include_adult"))
		}
		if got.Get("vote_count.gte") == "" {
			t.Errorf("%s: no vote floor", c.name)
		}
		for k, want := range c.check {
			if got.Get(k) != want {
				t.Errorf("%s: %s = %q, want %q", c.name, k, got.Get(k), want)
			}
		}
	}
}

// Every browse row goes through toItem: adult rows, adult titles and posterless rows are
// dropped, and the total page count comes back (TMDB's 500 cap kept).
func TestBrowseUsesToItem(t *testing.T) {
	tm, _ := fakeTMDB(t, `{"page":2,"total_pages":812,"results":[
		{"id":1,"title":"Harbour Lights","poster_path":"/a.jpg","release_date":"2020-01-01","vote_count":900},
		{"id":2,"title":"Quiet Evening","poster_path":"/b.jpg","adult":true,"vote_count":900},
		{"id":3,"title":"Brazzers Night Shift","poster_path":"/c.jpg","vote_count":900},
		{"id":4,"title":"No Poster","vote_count":900},
		{"id":1,"title":"Harbour Lights","poster_path":"/a.jpg","vote_count":900}]}`)
	p, err := tm.Browse(context.Background(), BrowseQuery{Media: "movie", Page: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].TMDBID != 1 || p.Items[0].MediaType != "movie" {
		t.Fatalf("items = %+v, want only Harbour Lights", p.Items)
	}
	if p.Page != 2 || p.TotalPages != 500 {
		t.Errorf("page %d of %d, want 2 of 500", p.Page, p.TotalPages)
	}
}

// Search forwards the page and returns the total, with people split out.
func TestSearchPaging(t *testing.T) {
	tm, seen := fakeTMDB(t, `{"page":3,"total_pages":7,"results":[
		{"id":10,"media_type":"movie","title":"Star Harbour","poster_path":"/s.jpg"},
		{"id":12,"media_type":"movie","title":"Star Night","poster_path":"/n.jpg","adult":true},
		{"id":13,"media_type":"movie","title":"Vixen Stars","poster_path":"/v.jpg"},
		{"id":11,"media_type":"tv","name":"Star Tide","poster_path":"/t.jpg"}]}`)
	sr, err := tm.SearchPage(context.Background(), "star", 3)
	if err != nil {
		t.Fatal(err)
	}
	u := seen()[0]
	if u.Path != "/search/multi" || u.Query().Get("page") != "3" || u.Query().Get("query") != "star" || u.Query().Get("include_adult") != "false" {
		t.Errorf("asked %s?%s", u.Path, u.RawQuery)
	}
	if sr.Page.Page != 3 || sr.TotalPages != 7 || len(sr.Items) != 2 || sr.Items[1].MediaType != "series" {
		t.Errorf("results = %+v", sr)
	}
	// The same page again comes from the cache.
	if _, err := tm.SearchPage(context.Background(), "star", 3); err != nil || len(seen()) != 1 {
		t.Errorf("second ask: err %v, %d calls (want 1)", err, len(seen()))
	}
}
