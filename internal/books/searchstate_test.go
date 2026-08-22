package books

import "testing"

// The missing-books sweep searched every monitored-but-missing book every 30 minutes,
// forever, against every indexer — a title no indexer carries cost exactly as much as one
// about to appear, and book searches run once per wanted edition. Movies and series have
// always backed off; this is the state that lets books do the same.
func TestSearchBackoffState(t *testing.T) {
	repo, ctx := historyRepo(t)
	b, err := repo.Create(ctx, Book{OLKey: "OL1W", Title: "The Coven", Author: "Harper L. Woods"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.Create(ctx, Book{OLKey: "OL2W", Title: "Dune", Author: "Frank Herbert"})
	if err != nil {
		t.Fatal(err)
	}

	// A brand-new book has never been searched, so nothing holds it back.
	if last, misses := repo.SearchState(ctx, b.ID); last != "" || misses != 0 {
		t.Errorf("new book state = (%q, %d), want (\"\", 0) — it must not start backed off", last, misses)
	}

	// Each empty sweep costs the next one more time.
	for want := 1; want <= 3; want++ {
		repo.RecordSearchMiss(ctx, b.ID)
		last, misses := repo.SearchState(ctx, b.ID)
		if misses != want {
			t.Fatalf("after %d misses, counter = %d", want, misses)
		}
		if last == "" {
			t.Error("a miss must stamp the search time, or the wait can never elapse")
		}
	}

	// Backing one book off must not quiet the rest of the library.
	if _, misses := repo.SearchState(ctx, other.ID); misses != 0 {
		t.Errorf("unrelated book picked up %d misses", misses)
	}

	// A successful grab clears it: the book is findable after all, so the next sweep
	// should treat it normally rather than serving out a wait it no longer deserves.
	repo.ResetSearchMisses(ctx, b.ID)
	last, misses := repo.SearchState(ctx, b.ID)
	if misses != 0 {
		t.Errorf("after a grab, misses = %d, want 0", misses)
	}
	if last == "" {
		t.Error("a reset still stamps the time — the sweep just ran")
	}
}
