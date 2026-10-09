package automation

import (
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
)

// The Wanted view's "next automatic try" follows each kind's own sweep ladder.
func TestSweepScheduleFollowsTheLadders(t *testing.T) {
	last := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	stamp := last.Format("2006-01-02 15:04:05")

	// Movies and series: 30 min doubling to a 12 h cap; no misses means due now.
	for misses := 0; misses <= 8; misses++ {
		got := SweepSchedule(AttemptMovie, stamp, misses)
		if want := last.Add(searchBackoff(misses)); !got.Next.Equal(want) || !got.Last.Equal(last) || got.Slowed {
			t.Errorf("movie, %d misses: %+v, want next %v", misses, got, want)
		}
		if misses == 0 && got.Next.After(last) {
			t.Errorf("no misses should be due straight away, got %v", got.Next)
		}
	}
	if got := SweepSchedule(AttemptSeries, stamp, 3); !got.Next.Equal(last.Add(2 * time.Hour)) {
		t.Errorf("3 misses: next %v, want 2 h after the last search", got.Next)
	}
	if got := SweepSchedule(AttemptSeries, stamp, 8); !got.Next.Equal(last.Add(12 * time.Hour)) {
		t.Errorf("8 misses: next %v, want the 12 h cap", got.Next)
	}
	// Never searched: due on the sweep's next run.
	if got := SweepSchedule(AttemptMovie, "", 0); !got.Last.IsZero() || !got.Next.IsZero() {
		t.Errorf("never searched: %+v", got)
	}

	// Books: a day, three days, weekly, then monthly — slowed past MonthlyAfter.
	if got := SweepSchedule(AttemptBook, stamp, 1); !got.Next.Equal(last.Add(24*time.Hour)) || got.Slowed {
		t.Errorf("book, 1 miss: %+v", got)
	}
	if got := SweepSchedule(AttemptBook, stamp, books.MonthlyAfter+1); !got.Next.Equal(last.Add(30*24*time.Hour)) || !got.Slowed {
		t.Errorf("book past monthly: %+v, want a month out and slowed", got)
	}

	// Albums: the series ladder, then weekly from musicWeeklyAfter misses.
	if got := SweepSchedule(AttemptMusic, stamp, 2); !got.Next.Equal(last.Add(time.Hour)) || got.Slowed {
		t.Errorf("album, 2 misses: %+v", got)
	}
	if got := SweepSchedule(AttemptMusic, stamp, musicWeeklyAfter); !got.Next.Equal(last.Add(7*24*time.Hour)) || !got.Slowed {
		t.Errorf("album, weekly: %+v", got)
	}
	if !AlbumNextSearchAt(time.Time{}, 5).IsZero() {
		t.Error("an album never searched should be due now")
	}
}
