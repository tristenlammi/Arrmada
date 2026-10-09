package books

import (
	"testing"
	"time"
)

// The ladder slows down but never stops: straight away, a day, three days, weekly to
// ten misses, then monthly however long it goes on.
func TestSearchLadder(t *testing.T) {
	const day = 24 * time.Hour
	last := "2026-10-01 12:00:00"
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		misses int
		wait   time.Duration
	}{
		{0, 0}, {1, day}, {2, 3 * day}, {3, 7 * day}, {10, 7 * day}, {11, 30 * day}, {50, 30 * day},
	} {
		if got := SearchWait(c.misses); got != c.wait {
			t.Errorf("SearchWait(%d) = %v, want %v", c.misses, got, c.wait)
		}
		next := NextSearchAt(last, c.misses)
		if c.wait == 0 {
			if !next.IsZero() {
				t.Errorf("NextSearchAt(%d) = %v, want now (zero)", c.misses, next)
			}
			continue
		}
		if !next.Equal(at.Add(c.wait)) {
			t.Errorf("NextSearchAt(%d) = %v, want %v", c.misses, next, at.Add(c.wait))
		}
	}
	if !NextSearchAt("", 5).IsZero() {
		t.Error("a book never searched is due now")
	}
	if got := NextSearchAt("2026-10-01T12:00:00Z", 1); !got.Equal(at.Add(day)) {
		t.Errorf("RFC 3339 stamp: %v", got)
	}
}
