package books

import (
	"context"
	"database/sql"
	"time"
)

// The search ladder: how long the missing-books sweep leaves a book alone after it
// came up empty. Books used to get two tries and then never again, so anything uploaded
// a week after the request only arrived if someone pressed Search. A wanted book is now
// never dropped; it is just asked about less often the longer nothing turns up, which
// keeps the load on private trackers bounded.
const (
	// MonthlyAfter is the miss count past which a book is checked once a month.
	MonthlyAfter = 10
)

// SearchWait is how long to wait after a book's last search, given how many searches
// in a row found nothing: straight away, a day, three days, weekly, then monthly.
func SearchWait(misses int) time.Duration {
	switch {
	case misses <= 0:
		return 0
	case misses == 1:
		return 24 * time.Hour
	case misses == 2:
		return 72 * time.Hour
	case misses <= MonthlyAfter:
		return 7 * 24 * time.Hour
	default:
		return 30 * 24 * time.Hour
	}
}

// NextSearchAt is when the sweep may next search a book last searched at lastAt (as
// stored, UTC) after misses empty searches. The zero time means now: never searched, or
// no misses to wait out.
func NextSearchAt(lastAt string, misses int) time.Time {
	wait := SearchWait(misses)
	last := parseStamp(lastAt)
	if wait == 0 || last.IsZero() {
		return time.Time{}
	}
	return last.Add(wait)
}

// parseStamp reads a stored SQLite timestamp; an empty or unreadable one is the zero time.
func parseStamp(s string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// SearchState is where one book stands on the ladder.
type SearchState struct {
	LastAt string // when the sweep last searched it ("" = never)
	Misses int    // searches in a row that found nothing
}

// SearchStates returns every book's search state in one query, for the sweep.
func (r *Repo) SearchStates(ctx context.Context) (map[int64]SearchState, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, last_search_at, search_misses FROM books`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]SearchState{}
	for rows.Next() {
		var id int64
		var last sql.NullString
		var misses int
		if err := rows.Scan(&id, &last, &misses); err != nil {
			return nil, err
		}
		out[id] = SearchState{LastAt: last.String, Misses: misses}
	}
	return out, rows.Err()
}

// SearchStates is the repo's SearchStates, for the sweep.
func (s *Service) SearchStates(ctx context.Context) (map[int64]SearchState, error) {
	return s.repo.SearchStates(ctx)
}
