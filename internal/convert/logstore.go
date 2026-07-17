package convert

import (
	"context"
	"database/sql"
)

// maxLogLines is how many Convert activity-console lines are retained — in memory and in the DB.
const maxLogLines = 5000

// logStore persists the Convert activity console (convert_logs table) so its history survives an
// app restart / update. The in-memory ring buffer on Service is the fast read path; this is the
// durable mirror, trimmed to the most recent maxLogLines on every append.
type logStore struct{ db *sql.DB }

// append writes one line and trims the table back to the most recent maxLogLines.
func (l *logStore) append(ctx context.Context, ln LogLine) {
	if l == nil || l.db == nil {
		return
	}
	if _, err := l.db.ExecContext(ctx,
		`INSERT INTO convert_logs (at, level, msg) VALUES (?, ?, ?)`, ln.At, ln.Level, ln.Msg); err != nil {
		return
	}
	// Drop anything older than the newest maxLogLines rows (no-op until the table exceeds the cap).
	_, _ = l.db.ExecContext(ctx,
		`DELETE FROM convert_logs WHERE id <= (SELECT MAX(id) FROM convert_logs) - ?`, maxLogLines)
}

// recent returns the last `limit` lines, oldest-first (the order the console renders them).
func (l *logStore) recent(ctx context.Context, limit int) []LogLine {
	if l == nil || l.db == nil {
		return nil
	}
	rows, err := l.db.QueryContext(ctx,
		`SELECT at, level, msg FROM (
		     SELECT id, at, level, msg FROM convert_logs ORDER BY id DESC LIMIT ?
		 ) ORDER BY id ASC`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []LogLine
	for rows.Next() {
		var ln LogLine
		if err := rows.Scan(&ln.At, &ln.Level, &ln.Msg); err != nil {
			return out
		}
		out = append(out, ln)
	}
	return out
}
