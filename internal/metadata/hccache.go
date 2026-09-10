package metadata

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Hardcover's free plan allows 5,000 requests a day and 60 a minute. A home library
// never needs that many unless something asks the same question over and over — a
// Discover page re-fetching "trending" on every open, an author page reloaded, the
// library scan hitting the same book. So every read is cached for a while, and a
// daily budget stops the module short of the cap so the last few hundred requests of
// the day are there for what the user is doing right now; past it, the list queries
// fall back to Open Library rather than fail.
const (
	hcDailyBudget  = 4500 // leave headroom under the 5,000 cap
	hcCacheEntries = 4000 // total entries before the cache is cleared wholesale

	// Hardcover's per-minute limiter is a token bucket: 60 a minute with a burst of
	// 10. Ours is the same shape, a touch under it, so a page that fires eight
	// requests at once goes straight through and a long job trickles at one a second.
	hcBurst      = 10.0
	hcPerSecond  = 55.0 / 60.0
	hcRetryAfter = 20 * time.Second // when a 429 arrives without a Retry-After

	hcTTLSearch  = time.Hour
	hcTTLBook    = 24 * time.Hour
	hcTTLAuthor  = 24 * time.Hour
	hcTTLList    = 6 * time.Hour // trending, subjects, an author's works, a series
	hcTTLSimilar = 24 * time.Hour
)

// ErrHardcoverBudget is returned when the day's request budget is spent; callers with
// another source fall back to it, the rest report it.
var ErrHardcoverBudget = errors.New("hardcover: today's request budget is used up — Open Library is answering until tomorrow")

// hcBudget counts requests per UTC day and paces them with a token bucket.
type hcBudget struct {
	mu     sync.Mutex
	day    string
	used   int
	tokens float64
	refill time.Time           // when tokens was last brought up to date
	now    func() time.Time    // swappable in tests
	sleep  func(time.Duration) // swappable in tests
}

func newHCBudget() *hcBudget {
	return &hcBudget{tokens: hcBurst, refill: time.Now(), now: time.Now, sleep: time.Sleep}
}

// take reserves one request, waiting for a token when the burst is spent; false when
// the day's budget is gone.
func (b *hcBudget) take() bool {
	b.mu.Lock()
	now := b.now()
	today := now.UTC().Format("2006-01-02")
	if b.day != today {
		b.day, b.used = today, 0
	}
	if b.used >= hcDailyBudget {
		b.mu.Unlock()
		return false
	}
	b.used++
	b.tokens += now.Sub(b.refill).Seconds() * hcPerSecond
	if b.tokens > hcBurst {
		b.tokens = hcBurst
	}
	b.refill = now
	var wait time.Duration
	if b.tokens >= 1 {
		b.tokens--
	} else {
		// Owe a token: the wait is how long the refill takes to cover it. Taking it
		// now (going negative) keeps concurrent callers queued behind each other.
		wait = time.Duration((1 - b.tokens) / hcPerSecond * float64(time.Second))
		b.tokens--
	}
	b.mu.Unlock()
	if wait > 0 {
		b.sleep(wait)
	}
	return true
}

// usage reports today's count and the budget.
func (b *hcBudget) usage() (used, budget int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.day != b.now().UTC().Format("2006-01-02") {
		return 0, hcDailyBudget
	}
	return b.used, hcDailyBudget
}

// hcCache is a small TTL cache keyed by query kind + argument, in front of the disk
// cache (when one is set) so an answer survives a restart and a stale one is served
// while it refreshes.
type hcCache struct {
	mu      sync.Mutex
	entries map[string]hcCacheEntry
	disk    *DiskCache
}

type hcCacheEntry struct {
	expires time.Time
	value   any
}

func newHCCache() *hcCache { return &hcCache{entries: map[string]hcCacheEntry{}} }

func (c *hcCache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return e.value, true
}

func (c *hcCache) put(key string, v any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= hcCacheEntries {
		c.entries = map[string]hcCacheEntry{}
	}
	c.entries[key] = hcCacheEntry{expires: time.Now().Add(ttl), value: v}
}

// cached runs fetch unless a fresh answer for key is held in memory or on disk (a
// stale disk answer is returned while fetch runs behind it). Errors are not cached.
func cached[T any](ctx context.Context, c *hcCache, key string, ttl time.Duration, fetch func(ctx context.Context) (T, error)) (T, error) {
	if v, ok := c.get(key); ok {
		if t, ok := v.(T); ok {
			return t, nil
		}
	}
	t, err := swr(ctx, c.disk, "hc:"+key, ttl, fetch)
	if err != nil {
		return t, err
	}
	c.put(key, t, ttl)
	return t, nil
}
