package indexer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// page renders a torznab response with a page of items and the declared total.
func page(offset, pageSize, total int) string {
	body := fmt.Sprintf(`<rss><channel><newznab:response offset="%d" total="%d"/>`, offset, total)
	for i := offset; i < offset+pageSize && i < total; i++ {
		body += fmt.Sprintf(`<item><title>Release %d</title><link>http://x/%d.torrent</link></item>`, i, i)
	}
	return body + `</channel></rss>`
}

// Indexers cap a response at their own page size regardless of the requested limit —
// TorrentLeech returns 35 whether you ask for 100 or 400. Without following the pagination
// cursor, everything past the first page is invisible, which is how an entire show's season
// packs can be missing while sitting plainly on the tracker.
func TestSearchFollowsPagination(t *testing.T) {
	const pageSize, total = 35, 120
	var requests int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, page(offset, pageSize, total))
	}))
	defer srv.Close()

	s := NewTorznabSearcher()
	got, err := s.Search(context.Background(), Indexer{Name: "test", URL: srv.URL}, SearchQuery{Text: "x", Limit: 400})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != total {
		t.Errorf("got %d releases, want all %d — pagination didn't follow through", len(got), total)
	}
	if requests < 2 {
		t.Errorf("made %d request(s); paging requires more than one", requests)
	}
}

// The caller's limit is still respected — paging shouldn't overshoot it.
func TestSearchStopsAtLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		fmt.Fprint(w, page(offset, 35, 1000))
	}))
	defer srv.Close()

	s := NewTorznabSearcher()
	got, err := s.Search(context.Background(), Indexer{Name: "test", URL: srv.URL}, SearchQuery{Text: "x", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Errorf("got %d releases, want exactly the requested 50", len(got))
	}
}

// A single short page means there's nothing more to fetch — don't keep asking.
func TestSearchStopsWhenExhausted(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		fmt.Fprint(w, page(0, 5, 5))
	}))
	defer srv.Close()

	s := NewTorznabSearcher()
	got, _ := s.Search(context.Background(), Indexer{Name: "test", URL: srv.URL}, SearchQuery{Text: "x", Limit: 400})
	if len(got) != 5 {
		t.Errorf("got %d, want 5", len(got))
	}
	if requests != 1 {
		t.Errorf("made %d requests for a complete single page, want 1", requests)
	}
}
