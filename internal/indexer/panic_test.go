package indexer

import (
	"context"
	"strings"
	"testing"
)

// panicky is a searcher whose parser blows up, the way a malformed feed could.
type panicky struct{}

func (panicky) Search(context.Context, Indexer, SearchQuery) ([]Release, error) {
	var r *Release
	_ = r.Title // nil dereference: a real runtime panic
	return nil, nil
}
func (panicky) Test(context.Context, Indexer) error { return nil }

// A panic inside one indexer's search becomes that indexer's error. Before, it ran on a
// fan-out goroutine nothing recovered, so one bad feed restarted the whole app.
func TestSearchPanickingIndexerBecomesItsError(t *testing.T) {
	s := outageService(t, nil, empty(t).URL)
	const kindPanicky Kind = "test-panicky"
	s.registry.searchers[kindPanicky] = panicky{}
	if _, err := s.Create(context.Background(), Indexer{
		Name: "Broken", Kind: kindPanicky, URL: "http://broken.invalid", Priority: 20, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := s.Search(context.Background(), SearchQuery{Text: "Dune", MediaType: MediaMovie, Limit: 10})
	if err != nil {
		t.Fatalf("one healthy indexer answered, so the search should succeed: %v", err)
	}
	if res.Asked != 2 {
		t.Fatalf("asked = %d, want 2", res.Asked)
	}
	if msg := res.Errors["Broken"]; !strings.Contains(msg, "panic") {
		t.Fatalf("the panicking indexer should carry a panic error, got errors %+v", res.Errors)
	}
	if _, ok := res.Errors["Tracker1"]; ok {
		t.Fatalf("the healthy indexer shouldn't have an error: %+v", res.Errors)
	}
}
