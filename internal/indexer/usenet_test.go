package indexer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Without a usenet download client a Newznab indexer gets no search or feed requests: its
// results could never be downloaded. A search with nothing else to ask is "no indexer",
// not an empty result.
func TestUsenetIndexerNotQueriedWithoutClient(t *testing.T) {
	var hits atomic.Int32
	nzb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`<rss><channel></channel></rss>`))
	}))
	t.Cleanup(nzb.Close)
	s, _, _ := healthService(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, Indexer{Name: "NZB", Kind: KindNewznab, URL: nzb.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Search(WithInteractive(ctx), dune); !errors.Is(err, ErrNoIndexers) {
		t.Fatalf("search = %v", err)
	}
	_, _ = s.Recent(ctx, 50)
	if hits.Load() != 0 {
		t.Fatalf("the usenet indexer got %d requests", hits.Load())
	}

	// Were a usenet client ever added, it would be asked again.
	s.SetUsenetAvailable(func() bool { return true })
	if _, err := s.Search(ctx, dune); err != nil {
		t.Fatal(err)
	}
	if hits.Load() == 0 {
		t.Fatal("with a usenet client the indexer should be searched")
	}
}
