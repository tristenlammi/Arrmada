package scheduler

import (
	"context"
	"database/sql"
	"time"
)

// SQLStore keeps task history in the scheduled_tasks table (migration 0121). Times are
// unix milliseconds, 0 for never.
type SQLStore struct{ db *sql.DB }

// NewSQLStore wraps the app database.
func NewSQLStore(db *sql.DB) *SQLStore { return &SQLStore{db: db} }

func msTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func timeMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// Load reads every saved task.
func (s *SQLStore) Load(ctx context.Context) (map[string]Persisted, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, every_seconds, last_started_at, last_finished_at, last_duration_ms,
		       last_status, last_error, last_error_at, runs, failures, consecutive_failures
		FROM scheduled_tasks`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]Persisted{}
	for rows.Next() {
		var (
			name                              string
			every, start, end, dur, errAt     int64
			status, lastErr                   string
			runs, failures, consecutiveFailed int64
		)
		if err := rows.Scan(&name, &every, &start, &end, &dur, &status, &lastErr, &errAt, &runs, &failures, &consecutiveFailed); err != nil {
			return nil, err
		}
		out[name] = Persisted{
			Every:               time.Duration(every) * time.Second,
			LastStart:           msTime(start),
			LastEnd:             msTime(end),
			LastDuration:        time.Duration(dur) * time.Millisecond,
			LastStatus:          status,
			LastError:           lastErr,
			LastErrorAt:         msTime(errAt),
			Runs:                uint64(max(runs, 0)),
			Failures:            uint64(max(failures, 0)),
			ConsecutiveFailures: uint64(max(consecutiveFailed, 0)),
		}
	}
	return out, rows.Err()
}

// Save upserts one task's history.
func (s *SQLStore) Save(ctx context.Context, name string, p Persisted) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO scheduled_tasks (name, every_seconds, last_started_at, last_finished_at, last_duration_ms,
		                             last_status, last_error, last_error_at, runs, failures, consecutive_failures)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			every_seconds = excluded.every_seconds,
			last_started_at = excluded.last_started_at,
			last_finished_at = excluded.last_finished_at,
			last_duration_ms = excluded.last_duration_ms,
			last_status = excluded.last_status,
			last_error = excluded.last_error,
			last_error_at = excluded.last_error_at,
			runs = excluded.runs,
			failures = excluded.failures,
			consecutive_failures = excluded.consecutive_failures`,
		name, int64(p.Every/time.Second), timeMS(p.LastStart), timeMS(p.LastEnd), p.LastDuration.Milliseconds(),
		p.LastStatus, p.LastError, timeMS(p.LastErrorAt), int64(p.Runs), int64(p.Failures), int64(p.ConsecutiveFailures))
	return err
}
