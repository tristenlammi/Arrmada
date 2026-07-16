package overseerr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListMapsRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// One page of three requests: approved movie, pending tv, declined movie.
		_, _ = w.Write([]byte(`{
			"pageInfo": {"pages": 1, "page": 1, "results": 3},
			"results": [
				{"status": 2, "type": "movie", "media": {"tmdbId": 603, "mediaType": "movie", "status": 5}, "requestedBy": {"displayName": "Alice"}},
				{"status": 1, "type": "tv", "media": {"tmdbId": 1399, "mediaType": "tv", "status": 2}, "requestedBy": {"plexUsername": "bob"}},
				{"status": 3, "type": "movie", "media": {"tmdbId": 24428, "mediaType": "movie", "status": 1}, "requestedBy": {"username": "carol"}}
			]
		}`))
	}))
	defer srv.Close()

	items, err := New(srv.URL, "secret").List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	if items[0].MediaType != "movie" || items[0].TMDBID != 603 || items[0].Status != "approved" || items[0].Requester != "Alice" {
		t.Errorf("item0 = %+v", items[0])
	}
	if items[1].MediaType != "series" || items[1].TMDBID != 1399 || items[1].Status != "pending" || items[1].Requester != "bob" {
		t.Errorf("item1 = %+v", items[1])
	}
	if items[2].Status != "declined" || items[2].Requester != "carol" {
		t.Errorf("item2 = %+v", items[2])
	}
}

func TestListBadKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "wrong").List(context.Background()); err == nil {
		t.Fatal("expected an error for a bad API key")
	}
}

func TestHelpers(t *testing.T) {
	if got := yearOf("2010-07-16"); got != 2010 {
		t.Errorf("yearOf = %d", got)
	}
	if got := yearOf(""); got != 0 {
		t.Errorf("yearOf empty = %d", got)
	}
	if got := posterURL("/x.jpg"); got != "https://image.tmdb.org/t/p/w500/x.jpg" {
		t.Errorf("posterURL = %q", got)
	}
	if got := posterURL(""); got != "" {
		t.Errorf("posterURL empty = %q", got)
	}
}
