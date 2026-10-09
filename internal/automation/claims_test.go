package automation

import (
	"errors"
	"sync"
	"testing"
)

func TestClaimIsExclusiveAndReleases(t *testing.T) {
	var c claims
	release, ok := c.claim("movie:1")
	if !ok || !c.claimed("movie:1") {
		t.Fatal("first claim failed")
	}
	if _, ok := c.claim("movie:1"); ok {
		t.Fatal("a second claim on the same title succeeded")
	}
	if _, ok := c.claim("movie:2"); !ok {
		t.Fatal("a different title was blocked")
	}
	release()
	release() // twice is harmless
	if c.claimed("movie:1") {
		t.Fatal("release didn't let go")
	}
}

// A search that panics still lets go of its title (the release is deferred), so the
// title isn't locked out of searching until the next restart.
func TestClaimReleasedOnPanic(t *testing.T) {
	var c claims
	func() {
		defer func() { _ = recover() }()
		release, _ := c.claim("book:7")
		defer release()
		var m map[string]int
		m["x"] = 1
	}()
	if c.claimed("book:7") {
		t.Fatal("a panicking search kept its claim")
	}
}

func TestClaimConcurrentOnlyOneWins(t *testing.T) {
	var c claims
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := c.claim("series:3"); ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d claims won, want 1", wins)
	}
}

// A manual search of a movie the sweep is already searching runs no second indexer
// search; the sweep likewise skips a movie a manual search holds, without counting a miss.
func TestSearchSkipsAClaimedMovie(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	h.ix.offer(arrivalRelease)

	release, ok := h.c.claims.claim(movieKey(mid))
	if !ok {
		t.Fatal("claim failed")
	}
	if _, err := h.c.SearchMovie(h.ctx, mid); !errors.Is(err, ErrAlreadySearching) {
		t.Fatalf("manual search of a claimed movie = %v, want ErrAlreadySearching", err)
	}
	h.c.SearchMissing(h.ctx)
	if n := h.adds(); n != 0 {
		t.Fatalf("grabbed %d times while the movie was claimed", n)
	}
	if _, misses := h.c.movies.SearchState(h.ctx, mid); misses != 0 {
		t.Fatalf("a skipped movie counted %d misses", misses)
	}
	release()
	if _, err := h.c.SearchMovie(h.ctx, mid); err != nil {
		t.Fatal(err)
	}
	if n := h.adds(); n != 1 {
		t.Fatalf("adds after release = %d, want 1", n)
	}
	if h.c.claims.claimed(movieKey(mid)) {
		t.Fatal("the search kept its claim")
	}
}
