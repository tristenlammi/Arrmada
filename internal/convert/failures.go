package convert

import (
	"context"
	"database/sql"
)

// failureStore tracks repeated conversion failures per movie so the auto-sweep can blocklist a
// file that keeps failing (see convert_max_failures). Backed by the convert_failures table.
type failureStore struct{ db *sql.DB }

// recordFailure bumps a movie's failure count (or starts it at 1), stashing the last error.
func (f *failureStore) recordFailure(ctx context.Context, movieID int64, errMsg string) {
	if len(errMsg) > 300 {
		errMsg = errMsg[:300]
	}
	_, _ = f.db.ExecContext(ctx,
		`INSERT INTO convert_failures (movie_id, count, last_error, updated_at)
		 VALUES (?, 1, ?, datetime('now'))
		 ON CONFLICT(movie_id) DO UPDATE SET count = count + 1, last_error = excluded.last_error, updated_at = datetime('now')`,
		movieID, errMsg)
}

// clearFailures resets a movie's record (called after a successful conversion).
func (f *failureStore) clearFailures(ctx context.Context, movieID int64) {
	_, _ = f.db.ExecContext(ctx, `DELETE FROM convert_failures WHERE movie_id = ?`, movieID)
}

// failureCount returns how many times a movie's conversion has failed (0 if never).
func (f *failureStore) failureCount(ctx context.Context, movieID int64) int {
	var n int
	_ = f.db.QueryRowContext(ctx, `SELECT count FROM convert_failures WHERE movie_id = ?`, movieID).Scan(&n)
	return n
}
