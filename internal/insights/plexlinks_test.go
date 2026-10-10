package insights

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/plex"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// newLinkService is an Insights service over a temp store, connected to url (none if "").
func newLinkService(t *testing.T, url string) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := NewService(st.DB(), settings.NewService(st.DB()), nil, nil, quietLog())
	if url != "" {
		tok := "plex-secret"
		if err := s.SetConfig(context.Background(), url, &tok, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestWatchURLFormat(t *testing.T) {
	got := watchURL("a1b2c3", "4567")
	want := "https://app.plex.tv/desktop/#!/server/a1b2c3/details?key=%2Flibrary%2Fmetadata%2F4567"
	if got != want {
		t.Fatalf("watchURL = %s\nwant %s", got, want)
	}
}

func TestNoPlexNoURL(t *testing.T) {
	s := newLinkService(t, "")
	s.links.idx = &plexIndex{machineID: "m", movieByTMDB: map[int]plex.Item{603: {RatingKey: "1", Type: "movie"}}, builtAt: time.Now()}
	if u := s.WatchURL(context.Background(), "movie", ExternalIDs{TMDB: 603}); u != "" {
		t.Fatalf("WatchURL without Plex set up = %q", u)
	}
}

// A fake Plex server: one new-agent movie library and one legacy-agent TV library.
func fakePlexLibrary(t *testing.T, calls *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "plex-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/identity":
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"abc123","version":"1.40"}}`))
		case r.URL.Path == "/library/sections":
			if calls != nil {
				calls.Add(1)
			}
			_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[
				{"key":"1","title":"Movies","type":"movie"},{"key":"2","title":"TV","type":"show"},{"key":"3","title":"Music","type":"artist"}]}}`))
		case r.URL.Path == "/library/sections/1/all":
			_, _ = w.Write([]byte(`{"MediaContainer":{"totalSize":2,"Metadata":[
				{"ratingKey":"101","type":"movie","title":"The Matrix","year":1999,"guid":"plex://movie/x","Guid":[{"id":"tmdb://603"},{"id":"imdb://tt0133093"}]},
				{"ratingKey":"102","type":"movie","title":"Heat","year":1995,"guid":"com.plexapp.agents.imdb://tt0113277?lang=en"}]}}`))
		case r.URL.Path == "/library/sections/2/all":
			_, _ = w.Write([]byte(`{"MediaContainer":{"totalSize":1,"Metadata":[
				{"ratingKey":"201","type":"show","title":"The Office","year":2005,"guid":"com.plexapp.agents.thetvdb://73244?lang=en"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPlexIndexBuildAndLocate(t *testing.T) {
	srv := fakePlexLibrary(t, nil)
	s := newLinkService(t, srv.URL)
	ctx := context.Background()
	s.links.build = s.buildPlexIndex
	if err := s.links.rebuild(ctx); err != nil {
		t.Fatal(err)
	}

	if u := s.WatchURL(ctx, "movie", ExternalIDs{TMDB: 603}); u != watchURL("abc123", "101") {
		t.Errorf("Matrix by TMDB = %q", u)
	}
	// Legacy agents: a movie found by IMDb, a show by TVDB.
	if it, ok := s.Locate(ctx, "movie", ExternalIDs{TMDB: 949, IMDB: "tt0113277"}); !ok || it.RatingKey != "102" {
		t.Errorf("Heat by IMDb = %+v, %v", it, ok)
	}
	if it, ok := s.Locate(ctx, "series", ExternalIDs{TMDB: 2316, TVDB: 73244}); !ok || it.RatingKey != "201" {
		t.Errorf("The Office by TVDB = %+v, %v", it, ok)
	}
	// Kinds don't cross, and an unknown title has no link.
	if _, ok := s.Locate(ctx, "series", ExternalIDs{TMDB: 603}); ok {
		t.Error("a movie's TMDB id found a show")
	}
	if u := s.WatchURL(ctx, "movie", ExternalIDs{TMDB: 1}); u != "" {
		t.Errorf("unknown title link = %q", u)
	}
	if media, tmdb, ok := s.TMDBForRatingKey("101"); !ok || media != "movie" || tmdb != 603 {
		t.Errorf("TMDBForRatingKey = %s %d %v", media, tmdb, ok)
	}
	if n := len(s.PlexItems("series")); n != 1 {
		t.Errorf("shows = %d", n)
	}
	if u := s.WatchURL(ctx, "movie", ExternalIDs{TMDB: 603}); strings.Contains(u, "plex-secret") || strings.Contains(u, srv.URL) {
		t.Errorf("the link leaks the token or server address: %s", u)
	}
}

// The first lookups on an empty index nudge one build and return at once; asking for a
// refresh after a scan (and many lookups meanwhile) costs one more build, never two at
// once.
func TestIndexStaleOnRefresh(t *testing.T) {
	var lists atomic.Int32
	srv := fakePlexLibrary(t, &lists)
	s := newLinkService(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var building, overlap atomic.Int32
	s.links.build = func(ctx context.Context) (*plexIndex, error) {
		if building.Add(1) > 1 {
			overlap.Add(1)
		}
		defer building.Add(-1)
		time.Sleep(20 * time.Millisecond) // a slow Plex
		return s.buildPlexIndex(ctx)
	}
	var rebuilt atomic.Int32
	s.OnPlexIndexRebuilt(func() { rebuilt.Add(1) })

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			s.Locate(ctx, "movie", ExternalIDs{TMDB: 603})
			if time.Since(start) > 10*time.Millisecond {
				t.Error("a lookup waited for the build")
			}
		}()
	}
	wg.Wait()

	done := make(chan struct{})
	go func() { s.RunPlexIndex(ctx); close(done) }()
	waitFor(t, func() bool { return rebuilt.Load() == 1 })
	if _, ok := s.Locate(ctx, "movie", ExternalIDs{TMDB: 603}); !ok {
		t.Fatal("not found after the first build")
	}

	for i := 0; i < 50; i++ {
		s.PlexIndexStale(0)
		s.Locate(ctx, "movie", ExternalIDs{TMDB: 603})
	}
	waitFor(t, func() bool { return rebuilt.Load() >= 2 })
	time.Sleep(100 * time.Millisecond)
	if n := rebuilt.Load(); n > 3 {
		t.Errorf("%d rebuilds for one burst of refresh asks", n)
	}
	if overlap.Load() != 0 {
		t.Error("two builds ran at once")
	}
	cancel()
	<-done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A steady stream of refresh asks can't put a rebuild off forever.
func TestIndexStaleIsCapped(t *testing.T) {
	var l plexLinks
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	l.idx = &plexIndex{builtAt: now}
	for i := 0; i < 20; i++ {
		l.markStale(90 * time.Second)
		now = now.Add(60 * time.Second)
	}
	if ok, _ := l.due(true); !ok {
		t.Fatalf("no rebuild due %s after the first ask", now.Sub(l.staleFrom))
	}
}
