package convert

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// failureStore tracks repeated conversion failures per library file so the auto-sweep can
// blocklist one that keeps failing (see convert_max_failures) instead of re-queuing it every
// night. Backed by the convert_failures table, keyed on an opaque item key so movies and TV
// episodes are quarantined the same way (migration 0059).
type failureStore struct{ db *sql.DB }

// movieKey / episodeKey build the identity a failure count is recorded against.
func movieKey(movieID int64) string { return fmt.Sprintf("movie:%d", movieID) }
func episodeKey(seriesID int64, season, episode int) string {
	return fmt.Sprintf("episode:%d:%d:%d", seriesID, season, episode)
}

// jobKey is the failure identity of whatever a job is converting.
func jobKey(j *Job) string {
	if j.Kind == "episode" {
		return episodeKey(j.SeriesID, j.Season, j.Episode)
	}
	return movieKey(j.MovieID)
}

// recordFailure bumps an item's failure count (or starts it at 1), stashing the last error.
func (f *failureStore) recordFailure(ctx context.Context, key, errMsg string) {
	if len(errMsg) > 300 {
		errMsg = errMsg[:300]
	}
	_, _ = f.db.ExecContext(ctx,
		`INSERT INTO convert_failures (item_key, count, last_error, updated_at)
		 VALUES (?, 1, ?, datetime('now'))
		 ON CONFLICT(item_key) DO UPDATE SET count = count + 1, last_error = excluded.last_error, updated_at = datetime('now')`,
		key, errMsg)
}

// clearFailures resets an item's record (called after a successful conversion).
func (f *failureStore) clearFailures(ctx context.Context, key string) {
	_, _ = f.db.ExecContext(ctx, `DELETE FROM convert_failures WHERE item_key = ?`, key)
}

// failureCount returns how many times an item's conversion has failed (0 if never).
func (f *failureStore) failureCount(ctx context.Context, key string) int {
	var n int
	_ = f.db.QueryRowContext(ctx, `SELECT count FROM convert_failures WHERE item_key = ?`, key).Scan(&n)
	return n
}

// Blocked is one quarantined file, for the UI's Problems list.
type Blocked struct {
	Key       string `json:"key"` // "movie:12" | "episode:76:2:5"
	Kind      string `json:"kind"`
	MovieID   int64  `json:"movie_id,omitempty"`
	SeriesID  int64  `json:"series_id,omitempty"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	Title     string `json:"title"`
	Count     int    `json:"count"`
	LastError string `json:"last_error"`
	UpdatedAt string `json:"updated_at"`
}

// list returns every item with recorded failures, worst first.
func (f *failureStore) list(ctx context.Context) ([]Blocked, error) {
	rows, err := f.db.QueryContext(ctx,
		`SELECT item_key, count, last_error, updated_at FROM convert_failures ORDER BY count DESC, updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Blocked{}
	for rows.Next() {
		var b Blocked
		if err := rows.Scan(&b.Key, &b.Count, &b.LastError, &b.UpdatedAt); err != nil {
			return nil, err
		}
		parseItemKey(&b)
		out = append(out, b)
	}
	return out, rows.Err()
}

// parseItemKey unpacks "movie:12" / "episode:76:2:5" back into identifiers.
func parseItemKey(b *Blocked) {
	parts := strings.Split(b.Key, ":")
	switch {
	case len(parts) == 2 && parts[0] == "movie":
		b.Kind = "movie"
		b.MovieID, _ = strconv.ParseInt(parts[1], 10, 64)
	case len(parts) == 4 && parts[0] == "episode":
		b.Kind = "episode"
		b.SeriesID, _ = strconv.ParseInt(parts[1], 10, 64)
		b.Season, _ = strconv.Atoi(parts[2])
		b.Episode, _ = strconv.Atoi(parts[3])
	}
}

// blocklisted reports whether an item has failed enough times for automation to skip it.
func (f *failureStore) blocklisted(ctx context.Context, key string, max int) bool {
	return f.failureCount(ctx, key) >= max
}
