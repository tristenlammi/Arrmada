package automation

import (
	"testing"
	"time"
)

// Books don't get the ever-lengthening ladder movies and series use. A title no tracker
// carries today usually isn't carried next week, and a book search is the expensive kind —
// one per wanted edition, across every indexer. Two goes: once on add, once a day later,
// then it's the Search button's job.
func TestBookSearchWait(t *testing.T) {
	cases := []struct {
		misses int
		wait   time.Duration
		giveUp bool
	}{
		{0, 0, false},              // never searched — go now
		{1, 24 * time.Hour, false}, // one retry, a day later
		{2, 0, true},               // done searching automatically
		{9, 0, true},               // and it stays done
	}
	for _, c := range cases {
		wait, giveUp := bookSearchWait(c.misses)
		if wait != c.wait || giveUp != c.giveUp {
			t.Errorf("bookSearchWait(%d) = (%v, %v), want (%v, %v)",
				c.misses, wait, giveUp, c.wait, c.giveUp)
		}
	}
	// The sweep must never quietly keep hammering: past the attempt limit, giveUp is the
	// only answer, whatever the wait says.
	if _, giveUp := bookSearchWait(bookSearchAttempts); !giveUp {
		t.Error("at the attempt limit the sweep must stop searching automatically")
	}
}
