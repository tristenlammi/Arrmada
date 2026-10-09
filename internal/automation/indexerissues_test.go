package automation

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/indexer"
)

// addDeadIndexer adds a second Torznab indexer that answers every search with an error,
// carrying an API key so the test can check the key never reaches the issues.
func addDeadIndexer(t *testing.T, h *stallHarness, name string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream says no to "+r.URL.String(), http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	if _, err := h.c.indexers.Create(h.ctx, indexer.Indexer{Name: name, Kind: indexer.KindTorznab, URL: srv.URL + "/api?apikey=SECRET", APIKey: "SECRET", Priority: 20, Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

// addUnreachableIndexer adds an indexer whose address refuses connections — a URL that
// carries the key in its query, the shape that leaks if an error echoes the URL.
func addUnreachableIndexer(t *testing.T, h *stallHarness, name string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	if _, err := h.c.indexers.Create(h.ctx, indexer.Indexer{Name: name, Kind: indexer.KindTorznab, URL: "http://" + addr + "/api?apikey=SECRET", APIKey: "SECRET", Priority: 30, Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

func TestRankReleasesNamesTheFailedIndexer(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	h.ix.offer(arrivalRelease)
	addDeadIndexer(t, h, "Dead")

	list, err := h.c.RankReleases(indexer.WithInteractive(h.ctx), mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Releases) != 1 || list.Releases[0].Title != arrivalRelease {
		t.Fatalf("the working indexer's releases are missing: %+v", list.Releases)
	}
	if len(list.IndexerIssues) != 1 || list.IndexerIssues[0].Indexer != "Dead" || list.IndexerIssues[0].Error == "" || list.IndexerIssues[0].Skipped {
		t.Fatalf("issues = %+v", list.IndexerIssues)
	}
	if list.Searched != 2 {
		t.Fatalf("searched = %d, want 2", list.Searched)
	}
}

func TestRankReleasesEveryIndexerFailedIsAnEmptyListNotAnError(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	h.ix.mu.Lock()
	h.ix.down = true
	h.ix.mu.Unlock()
	addDeadIndexer(t, h, "Dead")

	list, err := h.c.RankReleases(indexer.WithInteractive(h.ctx), mid)
	if err != nil {
		t.Fatalf("every indexer failing should come back as issues, got error %v", err)
	}
	if list.Releases == nil || len(list.Releases) != 0 {
		t.Fatalf("releases = %#v, want an empty list", list.Releases)
	}
	if len(list.IndexerIssues) != 2 {
		t.Fatalf("issues = %+v, want both indexers", list.IndexerIssues)
	}
}

func TestIndexerIssuesNeverCarryTheKey(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	addDeadIndexer(t, h, "Dead")
	addUnreachableIndexer(t, h, "Gone")

	list, err := h.c.RankReleases(indexer.WithInteractive(h.ctx), mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.IndexerIssues) != 2 {
		t.Fatalf("issues = %+v", list.IndexerIssues)
	}
	body, _ := json.Marshal(list)
	if strings.Contains(string(body), "SECRET") {
		t.Fatalf("an indexer's key reached the response: %s", body)
	}
}

func TestIssuesFromMergesOneIndexerAcrossSearches(t *testing.T) {
	var n searchNotes
	n.note(indexer.SearchResult{Errors: map[string]string{"TL": "login failed"}, Asked: 3}, nil)
	n.note(indexer.SearchResult{Errors: map[string]string{"TL": "timeout"}, Asked: 2}, nil)
	n.note(indexer.SearchResult{}, &indexer.AllFailedError{
		Errors:  map[string]string{"TL": "login failed", "1337x": "FlareSolverr unreachable http://x/?token=abc"},
		Skipped: map[string]string{"MAM": "paused until 15:00 after 3 failures: HTTP 403"},
	})
	got := n.issues()
	if len(got) != 3 {
		t.Fatalf("issues = %+v, want one per indexer", got)
	}
	byName := map[string]IndexerIssue{}
	for _, is := range got {
		byName[is.Indexer] = is
	}
	if byName["TL"].Error != "login failed" {
		t.Fatalf("TL = %+v, want its first error", byName["TL"])
	}
	if !byName["MAM"].Skipped {
		t.Fatalf("MAM should read as paused: %+v", byName["MAM"])
	}
	if strings.Contains(byName["1337x"].Error, "abc") {
		t.Fatalf("token survived: %q", byName["1337x"].Error)
	}
	if n.searched() != 3 {
		t.Fatalf("searched = %d", n.searched())
	}
	var nilNotes *searchNotes
	nilNotes.note(indexer.SearchResult{}, errors.New("x")) // nil-safe
	if nilNotes.issues() != nil {
		t.Fatal("nil collector has issues")
	}
}
