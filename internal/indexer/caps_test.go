package indexer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseCapsProwlarr(t *testing.T) {
	c, err := parseCaps(readFixture(t, "caps_prowlarr.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Movie.Available || !reflect.DeepEqual(c.Movie.Params, []string{"q", "imdbid", "tmdbid"}) || !c.TV.Available || c.Book.Available {
		t.Fatalf("modes = %+v", c)
	}
	if want := []int{2000, 2040, 2045, 5000, 5040, 100013}; !reflect.DeepEqual(c.Categories, want) {
		t.Fatalf("categories = %v, want %v", c.Categories, want)
	}
	if c.LimitsMax != 100 || c.LimitsDefault != 100 {
		t.Fatalf("limits = %d/%d", c.LimitsMax, c.LimitsDefault)
	}
	if got := c.Summary(); got != "Movies (imdbid, tmdbid) · TV (season, ep, imdbid, tvdbid) · 6 categories" {
		t.Fatalf("summary = %q", got)
	}
}

// Jackett builds vary in casing: elements and attributes are matched ignoring it.
func TestParseCapsJackettMixedCase(t *testing.T) {
	c, err := parseCaps(readFixture(t, "caps_jackett.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.TV.Available || !reflect.DeepEqual(c.TV.Params, []string{"q", "season", "ep"}) || c.Movie.Available || !c.Audio.Available {
		t.Fatalf("modes = %+v", c)
	}
	if want := []int{3000, 3030, 7000, 7020}; !reflect.DeepEqual(c.Categories, want) || c.LimitsMax != 1000 {
		t.Fatalf("caps = %+v", c)
	}
	if got := c.Summary(); got != "TV (season, ep) · Audio · Books (author, title) · 4 categories" {
		t.Fatalf("summary = %q", got)
	}
}

func TestParseCapsErrorsAndWebPages(t *testing.T) {
	_, err := parseCaps([]byte(`<?xml version="1.0"?><error code="100" description="Incorrect user credentials"/>`))
	var te *TorznabError
	if !errors.As(err, &te) || te.Code != 100 || err.Error() != "Incorrect user credentials" {
		t.Fatalf("error document = %#v", err)
	}
	for name, body := range map[string]string{
		"prowlarr page": "<!DOCTYPE html>\n<html><head><title>Prowlarr</title><script>var x = 1 < 2;</script></head><body><div id=\"root\"></div></body></html>",
		"json":          `{"error":"nope"}`,
		"empty":         "",
		"rss":           `<rss><channel></channel></rss>`,
	} {
		if _, err := parseCaps([]byte(body)); !errors.Is(err, errWebPage) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if !strings.Contains(errWebPage.Error(), "web page, not a Torznab API") {
		t.Fatal(errWebPage)
	}
}

// A search answered with <error> reads as the indexer's own words, not an XML error.
func TestParseFeedPageErrorDocument(t *testing.T) {
	_, _, err := parseFeedPage([]byte(`<error code="910" description="API disabled"/>`))
	var te *TorznabError
	if !errors.As(err, &te) || err.Error() != "API disabled" {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := parseFeedPage([]byte("<html><body>login</body></html>")); !errors.Is(err, errWebPage) {
		t.Fatalf("html = %v", err)
	}
	if rel, _, err := parseFeedPage([]byte(`<rss><channel><item><title>A</title></item></channel></rss>`)); err != nil || len(rel) != 1 {
		t.Fatalf("rss = %v, %v", rel, err)
	}
}

// Testing a Torznab indexer pointed at a web page fails, even though it answered 200; a
// passing Test stores the capabilities for the row.
func TestTorznabTestRejectsWebPageAndStoresCaps(t *testing.T) {
	var page atomic.Bool
	page.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if page.Load() {
			_, _ = w.Write([]byte("<!DOCTYPE html><html><body>Prowlarr</body></html>"))
			return
		}
		_, _ = w.Write(readFixture(t, "caps_prowlarr.xml"))
	}))
	t.Cleanup(srv.Close)
	s, _, ix := healthService(t, srv.URL)
	ctx := context.Background()
	if err := s.Test(ctx, ix[0].ID); !errors.Is(err, errWebPage) {
		t.Fatalf("web page Test = %v", err)
	}
	if got, _ := s.Get(ctx, ix[0].ID); got.CapsJSON != "" {
		t.Fatal("a failed Test stored caps")
	}
	page.Store(false)
	if err := s.Test(ctx, ix[0].ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, ix[0].ID)
	if got.CapsSummary() != "Movies (imdbid, tmdbid) · TV (season, ep, imdbid, tvdbid) · 6 categories" || got.CapsAt.IsZero() {
		t.Fatalf("stored caps = %q at %v", got.CapsSummary(), got.CapsAt)
	}

	// A new address forgets what the old one said.
	got.URL = srv.URL + "/other"
	if err := s.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Get(ctx, ix[0].ID); again.CapsJSON != "" {
		t.Fatal("caps survived an address change")
	}
}

// An indexer's error description — in a 200 or a 401 — reaches the person, with any
// echoed API key removed.
func TestTorznabErrorDescriptionsAreRedacted(t *testing.T) {
	var status atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
		fmt.Fprintf(w, `<error code="100" description="Incorrect user credentials for key %s"/>`, r.URL.Query().Get("apikey"))
	}))
	t.Cleanup(srv.Close)
	s, _, ix := healthService(t, srv.URL)
	for _, code := range []int{http.StatusOK, http.StatusUnauthorized} {
		status.Store(int32(code))
		err := s.Test(context.Background(), ix[0].ID)
		if err == nil || !strings.Contains(err.Error(), "Incorrect user credentials") || strings.Contains(err.Error(), "sekrit-key") {
			t.Fatalf("HTTP %d: Test = %v", code, err)
		}
		_, err = s.Search(WithInteractive(context.Background()), dune)
		if err == nil || !strings.Contains(err.Error(), "Incorrect user credentials") || strings.Contains(err.Error(), "sekrit-key") || strings.Contains(err.Error(), "parse feed") {
			t.Fatalf("HTTP %d: search = %v", code, err)
		}
	}
}

// Testing unsaved settings stores nothing and creates no row.
func TestTestSettingsStoresNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(readFixture(t, "caps_jackett.xml"))
	}))
	t.Cleanup(srv.Close)
	s, _, _ := healthService(t)
	summary, err := s.TestSettings(context.Background(), Indexer{Kind: KindTorznab, Name: "new", URL: srv.URL, APIKey: "typed"})
	if err != nil || !strings.Contains(summary, "Books (author, title)") {
		t.Fatalf("TestSettings = %q, %v", summary, err)
	}
	if list, _ := s.List(context.Background()); len(list) != 0 {
		t.Fatalf("testing settings created rows: %+v", list)
	}
}

// The refresh reads caps that are missing or over a week old, and leaves fresh ones.
func TestRefreshStaleCaps(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(readFixture(t, "caps_prowlarr.xml"))
	}))
	t.Cleanup(srv.Close)
	s, _, ix := healthService(t, srv.URL, srv.URL+"/b")
	ctx := context.Background()
	if err := s.RefreshStaleCaps(ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
	if err := s.RefreshStaleCaps(ctx); err != nil || hits.Load() != 2 {
		t.Fatalf("fresh caps were read again: hits %d, %v", hits.Load(), err)
	}
	if _, err := s.repo.db.ExecContext(ctx, `UPDATE indexers SET caps_at=? WHERE id=?`, time.Now().Add(-8*24*time.Hour), ix[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshStaleCaps(ctx); err != nil || hits.Load() != 3 {
		t.Fatalf("stale caps: hits %d, %v", hits.Load(), err)
	}
	if err := s.RefreshCaps(ctx, []int64{ix[1].ID, 999}); err != nil || hits.Load() != 4 {
		t.Fatalf("RefreshCaps: hits %d, %v", hits.Load(), err)
	}
}
