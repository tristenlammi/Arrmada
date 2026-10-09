package automation

import (
	"context"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/series"
)

// What the Wanted view reads from the sweeps: each kind's search ladder and the in-flight
// checks, exported so the view follows the sweeps' own rules instead of copying them.

// SweepSlot is where a wanted title stands on its kind's automatic-search ladder.
type SweepSlot struct {
	Last time.Time // when the sweep last searched it; zero = never
	// Next is when the sweep may search it again. Zero, or a time already past, means on
	// the sweep's next run.
	Next time.Time
	// Slowed: it has come up empty so often that its sweep now checks it only rarely —
	// books monthly, albums weekly. Never dropped for good, but worth saying.
	Slowed bool
}

// SweepSchedule reads a title's stored search state (last_search_at as stored, and the
// misses in a row) through its kind's ladder: movies and series back off 30 min → 12 h
// (searchBackoff), books a day → monthly (books.SearchWait), albums 30 min → weekly
// (musicSearchWait). kind is an Attempt* media type.
func SweepSchedule(kind, lastAt string, misses int) SweepSlot {
	s := SweepSlot{Last: parseTime(lastAt)}
	switch kind {
	case AttemptBook:
		s.Next = books.NextSearchAt(lastAt, misses)
		s.Slowed = misses > books.MonthlyAfter
	case AttemptMusic:
		s.Next = AlbumNextSearchAt(s.Last, misses)
		s.Slowed = misses >= musicWeeklyAfter
	default:
		s.Next = NextSearchAt(s.Last, misses)
	}
	return s
}

// AlbumNextSearchAt is when the music sweep will next search an album that has found
// nothing usable misses times in a row, given when it last searched (zero: as soon as the
// sweep comes round).
func AlbumNextSearchAt(lastSearch time.Time, misses int) time.Time {
	if lastSearch.IsZero() {
		return time.Time{}
	}
	return lastSearch.Add(musicSearchWait(misses))
}

// AlbumReleased reports whether an album is out by today (YYYY-MM-DD): the music sweep's
// own gate, so an album it won't search yet is Upcoming rather than Searching.
func AlbumReleased(al music.Album, today string) bool { return albumReleased(al, today) }

// UntrackedQueue is the unfinished torrents in queue that no grab knows by info hash (see
// untrackedQueue): the only ones still told apart by name. Read it once per page and
// pass it to SeriesInFlightScope for each show.
func (c *Coordinator) UntrackedQueue(ctx context.Context, queue []download.Item) ([]download.Item, error) {
	return c.untrackedQueue(ctx, queue)
}

// SeriesInFlightScope is seriesInFlightScope for a page that already holds the show's
// acquisitions (ActiveByItem) and the untracked torrents: which seasons a download still
// in flight covers, or whole=true when one covers the show, with those releases' names.
// It is the same rule the sweeps hold seasons back by, so the Wanted view can say
// "waiting on S03" exactly when the sweep is waiting on it.
func SeriesInFlightScope(acqs []Acquisition, untracked []download.Item, s series.Series) (seasons map[int]bool, whole bool, names []string) {
	return seriesInFlightScope(acqs, untracked, s)
}

// UntrackedMovieItem is the torrent nobody grabbed through Arrmada that the movie sweep
// takes to be fetching m already (untrackedMovie), "" for none: the sweep leaves m alone
// while it is there, so the Wanted view says it is waiting on it.
func UntrackedMovieItem(untracked []download.Item, m movies.Movie) string {
	return untrackedMovieItem(untracked, m)
}

// AcqHeld reports whether an acquisition is finished but held in Review for a decision.
func AcqHeld(a Acquisition) bool { return a.Status == grabStatusHeld }

// AcqStalled reports whether the client last saw an acquisition's torrent stalled: no
// peer sending data.
func AcqStalled(a Acquisition) bool { return a.Phase == "stalled" }
