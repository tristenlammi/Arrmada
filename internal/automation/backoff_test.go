package automation

import (
	"testing"
	"time"
)

// TestSearchBackoff locks in the escalation: a series that keeps finding nothing must
// stop costing a multi-indexer search every 15-minute sweep, but must never back off
// past the 12h cap (so it still recovers when a release finally appears).
func TestSearchBackoff(t *testing.T) {
	cases := map[int]time.Duration{
		0:  0,                // never missed → search on the next sweep
		1:  30 * time.Minute, // first miss
		2:  1 * time.Hour,
		3:  2 * time.Hour,
		4:  4 * time.Hour,
		5:  8 * time.Hour,
		6:  12 * time.Hour, // capped
		20: 12 * time.Hour, // stays capped, never overflows
	}
	for misses, want := range cases {
		if got := searchBackoff(misses); got != want {
			t.Errorf("searchBackoff(%d) = %v, want %v", misses, got, want)
		}
	}
	if got := searchBackoff(-1); got != 0 {
		t.Errorf("searchBackoff(-1) = %v, want 0", got)
	}
}
