package convert

import (
	"context"
	"database/sql"
	"fmt"
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

// blocklisted reports whether an item has failed enough times for automation to skip it.
func (f *failureStore) blocklisted(ctx context.Context, key string, max int) bool {
	return f.failureCount(ctx, key) >= max
}
