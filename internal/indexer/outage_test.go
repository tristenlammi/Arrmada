package indexer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// outageService is a Service over a real store, with one torznab indexer per server URL.
func outageService(t *testing.T, mediaTypes []string, urls ...string) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := NewService(st.DB(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	for i, u := range urls {
		if _, err := s.Create(context.Background(), Indexer{
			Name: fmt.Sprintf("Tracker%d", i+1), Kind: KindTorznab, URL: u,
			MediaTypes: mediaTypes, Priority: 10, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// failing answers every request with a server error and counts the hits.
func failing(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "down for maintenance", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// empty answers every request with a valid, empty feed.
func empty(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<rss><channel></channel></rss>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Every indexer failing is an outage, reported as such — it used to come back as an
// empty result with a nil error, which the sweeps counted as a search miss.
func TestSearchAllFailedIsAnError(t *testing.T) {
	var hits atomic.Int32
	s := outageService(t, nil, failing(t, &hits).URL, failing(t, &hits).URL)

	res, err := s.Search(context.Background(), SearchQuery{Text: "Dune", MediaType: MediaMovie, Limit: 10})
	var af *AllFailedError
	if !errors.As(err, &af) {
		t.Fatalf("want *AllFailedError, got %v", err)
	}
	if len(af.Errors) != 2 || !strings.Contains(err.Error(), "Tracker1") || !strings.Contains(err.Error(), "Tracker2") {
		t.Errorf("error should name both indexers: %q", err)
	}
	if !strings.HasPrefix(err.Error(), "all 2 indexers failed: ") {
		t.Errorf("error text = %q", err)
	}
	if len(res.Errors) != 2 {
		t.Errorf("the result's per-indexer errors should still be filled: %+v", res.Errors)
	}
	if !IsOutage(err) {
		t.Error("IsOutage should recognise an AllFailedError")
	}
}

// One indexer failing while another answers with nothing is a real "nothing found":
// only a search where nobody could answer is exempt from counting as a miss.
func TestSearchPartialFailureIsNotAnError(t *testing.T) {
	var hits atomic.Int32
	s := outageService(t, nil, failing(t, &hits).URL, empty(t).URL)

	res, err := s.Search(context.Background(), SearchQuery{Text: "Dune", MediaType: MediaMovie, Limit: 10})
	if err != nil {
		t.Fatalf("one indexer answering means no error, got %v", err)
	}
	if len(res.Errors) != 1 {
		t.Errorf("the failing indexer should still be listed: %+v", res.Errors)
	}
}

// Nobody serving the media type means nobody was asked — not "nothing exists".
func TestSearchNoIndexerForMediaType(t *testing.T) {
	s := outageService(t, []string{MediaMovie}, empty(t).URL)
	_, err := s.Search(context.Background(), SearchQuery{Text: "Dune", MediaType: MediaBook, Limit: 10})
	if !errors.Is(err, ErrNoIndexers) {
		t.Fatalf("want ErrNoIndexers, got %v", err)
	}
	if !IsOutage(err) {
		t.Error("IsOutage should recognise ErrNoIndexers")
	}
}

// A feed pull where every indexer failed is not cached: the next sweep must ask again
// rather than be served the outage for the cache window.
func TestRecentAllFailedIsNotCached(t *testing.T) {
	var hits atomic.Int32
	s := outageService(t, nil, failing(t, &hits).URL)

	if _, err := s.Recent(context.Background(), 50); !IsOutage(err) {
		t.Fatalf("want an outage error from the feed, got %v", err)
	}
	first := hits.Load()
	if first == 0 {
		t.Fatal("the feed was never requested")
	}
	if _, err := s.Recent(context.Background(), 50); !IsOutage(err) {
		t.Fatalf("second pull: want an outage error, got %v", err)
	}
	if hits.Load() <= first {
		t.Error("the failed pull was served from the cache instead of asking again")
	}
}
