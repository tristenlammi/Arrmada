package indexer

import (
	"testing"
	"time"
)

// The movie, series and book RSS sweeps each call Recent independently, within seconds
// of each other, so every cycle pulled the identical feed from every indexer three times
// over. A pull inside the TTL must be served from the cache instead of refetching.
func TestRecentCacheServesInsideTTL(t *testing.T) {
	c := &recentCache{
		at: time.Now(), limit: 100,
		res: SearchResult{Releases: []Release{{Title: "Cached Release"}}},
	}
	got, ok := c.fresh(100)
	if !ok {
		t.Fatal("a pull inside the TTL should be served from the cache")
	}
	if len(got.Releases) != 1 || got.Releases[0].Title != "Cached Release" {
		t.Errorf("expected the cached feed, got %+v", got.Releases)
	}
}

// A different limit is a different feed and must not be served from the cache.
func TestRecentCacheKeyedByLimit(t *testing.T) {
	c := &recentCache{at: time.Now(), limit: 100, res: SearchResult{Releases: []Release{{Title: "x"}}}}
	if _, ok := c.fresh(50); ok {
		t.Error("a different limit must miss the cache")
	}
}

// An expired entry must be refetched rather than served stale forever — otherwise a
// short-lived optimization turns into a feed that never updates.
func TestRecentCacheExpires(t *testing.T) {
	c := &recentCache{
		at: time.Now().Add(-2 * recentTTL), limit: 100,
		res: SearchResult{Releases: []Release{{Title: "stale"}}},
	}
	if _, ok := c.fresh(100); ok {
		t.Error("an entry past the TTL must miss the cache")
	}
}

// An empty cache must miss, not serve a zero-valued feed as if it were real.
func TestRecentCacheStartsEmpty(t *testing.T) {
	var c recentCache
	if _, ok := c.fresh(100); ok {
		t.Error("an unpopulated cache must miss")
	}
}

// Callers get their own slice header, so one sweep appending to its result can't
// corrupt what the next sweep reads out of the cache.
func TestRecentHandsOutIndependentSlices(t *testing.T) {
	s := &Service{}
	s.recent = recentCache{
		at: time.Now(), limit: 100,
		res: SearchResult{Releases: append(make([]Release, 0, 4), Release{Title: "one"})},
	}

	got, err := s.Recent(t.Context(), 100)
	if err != nil {
		t.Fatalf("cached pull should not fetch: %v", err)
	}
	// Append into the spare capacity, then overwrite element 0 — both would be visible
	// through the cached slice if the caller shared its backing array.
	got.Releases = append(got.Releases, Release{Title: "injected"})
	got.Releases[0] = Release{Title: "clobbered"}

	if len(s.recent.res.Releases) != 1 || s.recent.res.Releases[0].Title != "one" {
		t.Errorf("caller mutated the cached feed: %+v", s.recent.res.Releases)
	}
}
