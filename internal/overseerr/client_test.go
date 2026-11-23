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
	// Approved and already available in Seerr: finished history, not something to search.
	if items[0].MediaType != "movie" || items[0].TMDBID != 603 || items[0].Status != StatusFinished || items[0].Requester != "Alice" {
		t.Errorf("item0 = %+v", items[0])
	}
	if items[1].MediaType != "series" || items[1].TMDBID != 1399 || items[1].Status != "pending" || items[1].Requester != "bob" {
		t.Errorf("item1 = %+v", items[1])
	}
	if items[2].Status != "declined" || items[2].Requester != "carol" {
		t.Errorf("item2 = %+v", items[2])
	}
}

// TestClassify pins Seerr's numbers (server/constants/media.ts: request 1 pending, 2
// approved, 3 declined, 4 failed, 5 completed; media 1 unknown, 2 pending, 3 processing,
// 4 partly available, 5 available, then deleted at 6 in Overseerr and blocklisted 6 /
// deleted 7 in Jellyseerr and Seerr) to what each becomes here. Issue #130: requests whose
// media had been removed came in approved and set off hundreds of searches.
func TestClassify(t *testing.T) {
	cases := []struct {
		name         string
		req, media   int
		want         string
		mustNotFetch bool
	}{
		{"pending, nothing yet", 1, 1, StatusPending, true},
		{"pending, partly there", 1, 4, StatusPending, true},
		{"pending after the media was deleted (asked again)", 1, 7, StatusPending, true},
		{"approved, waiting", 2, 2, StatusApproved, false},
		{"approved, processing", 2, 3, StatusApproved, false},
		{"approved, show partly there", 2, 4, StatusApproved, false},
		{"approved, available", 2, 5, StatusFinished, true},
		{"approved, deleted (Overseerr)", 2, 6, StatusRemoved, true},
		{"approved, deleted (Jellyseerr/Seerr)", 2, 7, StatusRemoved, true},
		{"declined", 3, 1, StatusDeclined, true},
		{"declined, available", 3, 5, StatusDeclined, true},
		{"failed", 4, 3, StatusPending, true},
		{"completed, available", 5, 5, StatusFinished, true},
		{"completed, partly there", 5, 4, StatusFinished, true},
		{"completed, then deleted (Overseerr)", 5, 6, StatusRemoved, true},
		{"completed, then deleted (Jellyseerr/Seerr)", 5, 7, StatusRemoved, true},
		{"completed, blocklisted (Jellyseerr)", 5, 6, StatusRemoved, true},
		{"unknown request status", 9, 3, StatusPending, true},
	}
	for _, c := range cases {
		got := Classify(c.req, c.media)
		if got != c.want {
			t.Errorf("%s: Classify(%d, %d) = %q, want %q", c.name, c.req, c.media, got, c.want)
		}
		if c.mustNotFetch && got == StatusApproved {
			t.Errorf("%s: must never come in as an approved request that searches", c.name)
		}
	}
}

// A 4K request is judged by the media's 4K status: a 4K copy deleted from the server is
// removed even while the ordinary copy is still there.
func TestListUses4KStatusFor4KRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results": [
			{"status": 2, "is4k": true, "type": "movie", "media": {"tmdbId": 603, "mediaType": "movie", "status": 3, "status4k": 7}, "requestedBy": {"displayName": "Alice"}},
			{"status": 2, "is4k": false, "type": "movie", "media": {"tmdbId": 604, "mediaType": "movie", "status": 3, "status4k": 7}, "requestedBy": {"displayName": "Alice"}}
		]}`))
	}))
	defer srv.Close()
	items, err := New(srv.URL, "k").List(context.Background())
	if err != nil || len(items) != 2 {
		t.Fatalf("List = %v, %v", items, err)
	}
	if items[0].Status != StatusRemoved || items[1].Status != StatusApproved {
		t.Errorf("statuses = %q, %q; want removed, approved", items[0].Status, items[1].Status)
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
