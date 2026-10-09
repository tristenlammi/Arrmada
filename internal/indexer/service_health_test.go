package indexer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/connstatus"
	"github.com/tristenlammi/arrmada/internal/store"
)

// healthService is a Service with a status tracker and one torznab indexer per URL,
// returning the created indexers too.
func healthService(t *testing.T, urls ...string) (*Service, *connstatus.Tracker, []Indexer) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewService(st.DB(), log, "")
	tr := connstatus.New(st.DB(), log)
	s.SetStatus(tr)
	var created []Indexer
	for i, u := range urls {
		idx, err := s.Create(context.Background(), Indexer{
			Name: fmt.Sprintf("Tracker%d", i+1), Kind: KindTorznab, URL: u, APIKey: "sekrit-key", Priority: 10, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, idx)
	}
	return s, tr, created
}

func ref(idx Indexer) string { return strconv.FormatInt(idx.ID, 10) }

var dune = SearchQuery{Text: "Dune", MediaType: MediaMovie, Limit: 10}

// A failing indexer is skipped by background searches from its second failure in a row:
// it gets no request and Skipped says why. A person's search still asks it.
func TestFailingIndexerBacksOffForBackgroundSearches(t *testing.T) {
	var hits atomic.Int32
	bad := failing(t, &hits)
	s, tr, ix := healthService(t, bad.URL, empty(t).URL)

	for i := 0; i < 2; i++ {
		if _, err := s.Search(context.Background(), dune); err != nil {
			t.Fatalf("sweep %d: one indexer answering is not an outage: %v", i+1, err)
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("bad indexer hits = %d, want 2", hits.Load())
	}
	st, ok := tr.Get(connstatus.KindIndexer, ref(ix[0]))
	if !ok || st.ConsecutiveFailures != 2 || !time.Now().Before(st.BackoffUntil) {
		t.Fatalf("bad indexer should be backing off: %+v", st)
	}
	if strings.Contains(st.LastError, "sekrit-key") {
		t.Fatalf("stored error carries the API key: %q", st.LastError)
	}

	res, err := s.Search(context.Background(), dune)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("a backing-off indexer was asked by a background search (hits %d)", hits.Load())
	}
	why, skipped := res.Skipped["Tracker1"]
	if !skipped || !strings.Contains(why, "paused until") || !strings.Contains(why, "after 2 failures") {
		t.Fatalf("Skipped = %+v", res.Skipped)
	}
	if _, ok := res.Skipped["Tracker2"]; ok {
		t.Fatal("the healthy indexer was skipped")
	}
	if res.Asked != 1 {
		t.Errorf("Asked = %d, want only the indexer that was asked", res.Asked)
	}

	if _, err := s.Search(WithInteractive(context.Background()), dune); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 3 {
		t.Fatalf("a person's search should still ask the paused indexer (hits %d)", hits.Load())
	}
}

// One success — from a person's search — clears the backoff.
func TestInteractiveSuccessClearsTheBackoff(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `<rss><channel></channel></rss>`)
	}))
	t.Cleanup(srv.Close)
	s, tr, ix := healthService(t, srv.URL)
	for i := 0; i < 2; i++ {
		_, _ = s.Search(context.Background(), dune)
	}
	if ok, _ := tr.Allow(connstatus.KindIndexer, ref(ix[0]), false); ok {
		t.Fatal("expected a backoff after two failures")
	}
	fail.Store(false)
	if _, err := s.Search(WithInteractive(context.Background()), dune); err != nil {
		t.Fatal(err)
	}
	if ok, st := tr.Allow(connstatus.KindIndexer, ref(ix[0]), false); !ok || st.ConsecutiveFailures != 0 {
		t.Fatalf("a success should clear the backoff: %+v", st)
	}
}

// Every indexer paused is an outage that says so — not "nothing found" — and asks nobody.
func TestEveryIndexerPausedIsAnOutage(t *testing.T) {
	var hits atomic.Int32
	s, _, _ := healthService(t, failing(t, &hits).URL)
	for i := 0; i < 2; i++ {
		_, _ = s.Search(context.Background(), dune)
	}
	before := hits.Load()
	res, err := s.Search(context.Background(), dune)
	if !IsOutage(err) || !IsPaused(err) {
		t.Fatalf("want a paused outage, got %v", err)
	}
	if !strings.Contains(err.Error(), "paused") || len(res.Skipped) != 1 {
		t.Fatalf("err = %q, skipped = %+v", err, res.Skipped)
	}
	if hits.Load() != before {
		t.Fatal("a paused indexer was asked")
	}
	// The feed sweep stands down the same way.
	if _, err := s.Recent(context.Background(), 50); !IsPaused(err) {
		t.Fatalf("Recent: want a paused outage, got %v", err)
	}
	if hits.Load() != before {
		t.Fatal("a paused indexer's feed was pulled")
	}
}

// A search the caller abandoned (a closed release modal, shutdown) records no failure.
func TestCancelledCallerRecordsNothing(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); slow.Close() })
	s, tr, ix := healthService(t, slow.URL)

	ctx, cancel := context.WithCancel(WithInteractive(context.Background()))
	time.AfterFunc(50*time.Millisecond, cancel)
	_, _ = s.Search(ctx, dune)
	if st, ok := tr.Get(connstatus.KindIndexer, ref(ix[0])); ok {
		t.Fatalf("a cancelled search recorded a failure: %+v", st)
	}
}

// Test shows on the row: a failure is recorded without pausing anything, a pass clears it.
func TestTestRecordsWithoutBackingOff(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `<caps></caps>`)
	}))
	t.Cleanup(srv.Close)
	s, tr, ix := healthService(t, srv.URL)
	for i := 0; i < 3; i++ {
		if err := s.Test(context.Background(), ix[0].ID); err == nil {
			t.Fatal("test should fail")
		}
	}
	st, _ := tr.Get(connstatus.KindIndexer, ref(ix[0]))
	if st.LastError != "HTTP 401" || !st.BackoffUntil.IsZero() {
		t.Fatalf("a failed Test should show without pausing: %+v", st)
	}
	fail.Store(false)
	if err := s.Test(context.Background(), ix[0].ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := tr.Get(connstatus.KindIndexer, ref(ix[0])); st.Phase(time.Now()) != connstatus.PhaseOK {
		t.Fatalf("a passing Test should turn it OK: %+v", st)
	}
}

// Editing how an indexer is reached clears its status; editing only its scope doesn't.
// Deleting it forgets it.
func TestEditAndDeleteClearStatus(t *testing.T) {
	var hits atomic.Int32
	s, tr, ix := healthService(t, failing(t, &hits).URL)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_, _ = s.Search(ctx, dune)
	}
	idx := ix[0]
	idx.MediaTypes = []string{MediaMovie}
	if err := s.Update(ctx, idx); err != nil {
		t.Fatal(err)
	}
	if _, ok := tr.Get(connstatus.KindIndexer, ref(idx)); !ok {
		t.Fatal("changing only the scope cleared the status")
	}
	idx.URL = empty(t).URL
	if err := s.Update(ctx, idx); err != nil {
		t.Fatal(err)
	}
	if st, ok := tr.Get(connstatus.KindIndexer, ref(idx)); ok {
		t.Fatalf("changing the URL should clear the status: %+v", st)
	}
	_, _ = s.Search(ctx, dune)
	if err := s.Delete(ctx, idx.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := tr.Get(connstatus.KindIndexer, ref(idx)); ok || tr.Counts24h(connstatus.KindIndexer, ref(idx)).Queries != 0 {
		t.Fatal("delete should forget the indexer")
	}
}

// A 429 with Retry-After comes back typed, with the same text as before.
func TestTorznabRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	_, err := NewTorznabSearcher().get(context.Background(), srv.URL+"/api?t=caps")
	var he *HTTPStatusError
	if !errors.As(err, &he) || he.Code != 429 || he.RetryAfter != 600*time.Second {
		t.Fatalf("got %#v", err)
	}
	if err.Error() != "HTTP 429" {
		t.Fatalf("Error() = %q", err.Error())
	}

	// And the tracker honours it: paused for at least ten minutes after one failure.
	s, tr, ix := healthService(t, srv.URL)
	_, _ = s.Search(context.Background(), dune)
	st, _ := tr.Get(connstatus.KindIndexer, ref(ix[0]))
	if time.Until(st.BackoffUntil) < 9*time.Minute {
		t.Fatalf("backoff until %v, want ~10 min", st.BackoffUntil)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"120":                           2 * time.Minute,
		"-5":                            0,
		"soon":                          0,
		"Fri, 09 Oct 2026 14:05:00 GMT": 5 * time.Minute,
		"Fri, 09 Oct 2026 13:00:00 GMT": 0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
