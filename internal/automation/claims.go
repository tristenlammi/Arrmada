package automation

import (
	"errors"
	"strconv"
	"sync"
)

// ErrAlreadySearching is a search that found the same title already being searched — by
// the sweep, a Search click, or a request approval. Nothing went wrong and nothing more
// needs doing: the search in flight covers it.
var ErrAlreadySearching = errors.New("already being searched")

// claims is the per-title lock that keeps two searches of the same movie, show or book
// from running at once. The job runner already makes two clicks one job, but a click
// and the 5-minute sweep are different work: without this, both queried every indexer
// and could both grab. In-memory on purpose — a search doesn't survive a restart either.
type claims struct {
	mu   sync.Mutex
	held map[string]bool
}

// claim takes key ("movie:12") if nobody holds it. release must be called (deferred, so
// a panic in the search still lets go).
func (c *claims) claim(key string) (release func(), ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held[key] {
		return func() {}, false
	}
	if c.held == nil {
		c.held = map[string]bool{}
	}
	c.held[key] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.held, key)
			c.mu.Unlock()
		})
	}, true
}

// claimed reports whether key is held right now.
func (c *claims) claimed(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held[key]
}

func movieKey(id int64) string  { return "movie:" + strconv.FormatInt(id, 10) }
func seriesKey(id int64) string { return "series:" + strconv.FormatInt(id, 10) }
func bookKey(id int64) string   { return "book:" + strconv.FormatInt(id, 10) }
func albumKey(id int64) string  { return "album:" + strconv.FormatInt(id, 10) }
