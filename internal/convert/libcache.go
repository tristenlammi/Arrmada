package convert

import (
	"context"
	"time"
)

// The Overview's stats and the TV tab's per-show roll-up are both one pass over the
// whole index — 25,000 rows unmarshalled, and a directory listing for every folder
// (withSidecars) on a spinning array. Fine once; not fine on every page open, which is
// when they ran. Both are now cached against the index's write counter, so they're
// recomputed when the index changes (an import, a convert, a rescan) and otherwise served
// as they are. The TTL catches what the counter can't see: sidecars the Subtitles module
// writes, and skips that resolve on their own.
const libCacheTTL = 10 * time.Minute

type libCacheEntry[T any] struct {
	gen uint64
	key string
	at  time.Time
	v   T
	ok  bool
}

func (e libCacheEntry[T]) fresh(gen uint64, key string) bool {
	return e.ok && e.gen == gen && e.key == key && time.Since(e.at) < libCacheTTL
}

// indexGen is the index's write counter (0 when there is no index).
func (s *Service) indexGen() uint64 {
	if s.index == nil {
		return 0
	}
	return s.index.gen.Load()
}

// libCacheKey folds in the settings the computations depend on, so changing the target
// codec or the track plan invalidates them like an index write would.
func (s *Service) libCacheKey(ctx context.Context) string {
	return s.prefs(ctx).cacheKey()
}

// invalidateLibraryCache forces the next read to recompute — for changes that don't go
// through the index, such as clearing a skip.
func (s *Service) invalidateLibraryCache() {
	if s.index != nil {
		s.index.gen.Add(1)
	}
}

// LibraryStats aggregates the whole index — cached, see above. AsOf says when.
func (s *Service) LibraryStats(ctx context.Context) (*LibraryStats, error) {
	gen, key := s.indexGen(), s.libCacheKey(ctx)
	s.libCacheMu.Lock()
	if c := s.statsCache; c.fresh(gen, key) {
		s.libCacheMu.Unlock()
		return c.v, nil
	}
	s.libCacheMu.Unlock()
	v, err := s.computeLibraryStats(ctx)
	if err != nil {
		return nil, err
	}
	v.AsOf = time.Now().Unix()
	s.libCacheMu.Lock()
	s.statsCache = libCacheEntry[*LibraryStats]{gen: gen, key: key, at: time.Now(), v: v, ok: true}
	s.libCacheMu.Unlock()
	return v, nil
}

// LibraryTVSeries is the per-series roll-up for the TV tab — cached, see above.
func (s *Service) LibraryTVSeries(ctx context.Context) ([]SeriesRollup, error) {
	gen, key := s.indexGen(), s.libCacheKey(ctx)
	s.libCacheMu.Lock()
	if c := s.tvCache; c.fresh(gen, key) {
		s.libCacheMu.Unlock()
		return c.v, nil
	}
	s.libCacheMu.Unlock()
	v, err := s.computeLibraryTVSeries(ctx)
	if err != nil {
		return nil, err
	}
	s.libCacheMu.Lock()
	s.tvCache = libCacheEntry[[]SeriesRollup]{gen: gen, key: key, at: time.Now(), v: v, ok: true}
	s.libCacheMu.Unlock()
	return v, nil
}
