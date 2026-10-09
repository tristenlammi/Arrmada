package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/safego"
)

// DiskCache keeps catalogue answers in SQLite so they survive a restart, and serves a
// stale answer while a fresh one is fetched in the background. The pages that matter
// (Discover's rows, a book's details) are then instant on every open, including the
// first one after a deploy, and an upstream that's slow or paced — Hardcover allows
// one request a second — never sits between the user and the page.
type DiskCache struct {
	db *sql.DB

	mu       sync.Mutex
	inflight map[string]bool // keys being refreshed in the background
	lastGC   time.Time
}

// diskStaleMax is how long past its TTL an entry is still worth serving while a
// refresh runs; older than this and the caller waits for a fresh answer.
const diskStaleMax = 7 * 24 * time.Hour

func NewDiskCache(db *sql.DB) *DiskCache {
	return &DiskCache{db: db, inflight: map[string]bool{}}
}

// get returns the stored value, whether its TTL has passed, and whether it exists at
// all (entries older than diskStaleMax count as absent).
func (c *DiskCache) get(key string) (value []byte, expired, ok bool) {
	if c == nil || c.db == nil {
		return nil, false, false
	}
	var stored, expires int64
	err := c.db.QueryRow(`SELECT value, stored_at, expires_at FROM metadata_cache WHERE key = ?`, key).Scan(&value, &stored, &expires)
	if err != nil {
		return nil, false, false
	}
	now := time.Now().Unix()
	if now-stored > int64(diskStaleMax.Seconds()) {
		return nil, false, false
	}
	return value, now >= expires, true
}

func (c *DiskCache) put(key string, value []byte, ttl time.Duration) {
	if c == nil || c.db == nil {
		return
	}
	now := time.Now()
	_, _ = c.db.Exec(`INSERT INTO metadata_cache (key, value, stored_at, expires_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, stored_at = excluded.stored_at, expires_at = excluded.expires_at`,
		key, value, now.Unix(), now.Add(ttl).Unix())
	c.mu.Lock()
	gc := now.Sub(c.lastGC) > time.Hour
	if gc {
		c.lastGC = now
	}
	c.mu.Unlock()
	if gc {
		_, _ = c.db.Exec(`DELETE FROM metadata_cache WHERE stored_at < ?`, now.Add(-diskStaleMax).Unix())
	}
}

// claim marks a key as refreshing; false if a refresh is already running.
func (c *DiskCache) claim(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight[key] {
		return false
	}
	c.inflight[key] = true
	return true
}

func (c *DiskCache) release(key string) {
	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()
}

// swr is stale-while-revalidate over the disk cache: a fresh entry is returned; a
// stale one is returned at once while fetch runs in the background (detached from
// the request's context, so it completes after the response); nothing at all means
// fetch now. Errors are never stored. A nil cache just calls fetch.
func swr[T any](ctx context.Context, c *DiskCache, key string, ttl time.Duration, fetch func(ctx context.Context) (T, error)) (T, error) {
	return swrTTL(ctx, c, key, func(T) time.Duration { return ttl }, fetch)
}

// swrTTL is swr where the answer decides how long it is kept (OMDb keeps "no such title"
// for a shorter time than real scores).
func swrTTL[T any](ctx context.Context, c *DiskCache, key string, ttl func(T) time.Duration, fetch func(ctx context.Context) (T, error)) (T, error) {
	if c == nil {
		return fetch(ctx)
	}
	if raw, expired, ok := c.get(key); ok {
		var v T
		if json.Unmarshal(raw, &v) == nil {
			if expired && c.claim(key) {
				safego.Go(nil, "metadata cache refresh", func() {
					defer c.release(key)
					bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
					defer cancel()
					if nv, err := fetch(bg); err == nil {
						if b, err := json.Marshal(nv); err == nil {
							c.put(key, b, ttl(nv))
						}
					}
				})
			}
			return v, nil
		}
	}
	v, err := fetch(ctx)
	if err != nil {
		return v, err
	}
	if b, err := json.Marshal(v); err == nil {
		c.put(key, b, ttl(v))
	}
	return v, nil
}
