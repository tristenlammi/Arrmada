package listening

import (
	"context"
	"time"
)

// These read only the listening log, which has no book column: the admin's view of
// listening is how much and when, never what.

// minLogSeconds hides sessions that were opened but barely played (a tap on a book).
const minLogSeconds = 30.0

// UserTotals is how much one user has listened over a few windows.
type UserTotals struct {
	UserID     int64   `json:"user_id"`
	Today      float64 `json:"today"`
	Week       float64 `json:"week"`
	Month      float64 `json:"month"`
	AllTime    float64 `json:"all_time"`
	LastListen int64   `json:"last_listen,omitempty"`
}

// Totals returns listening totals per user. dayStart is local midnight today.
func (s *Store) Totals(ctx context.Context, dayStart time.Time) (map[int64]UserTotals, error) {
	today := dayStart.UnixMilli()
	week := dayStart.AddDate(0, 0, -6).UnixMilli()
	month := dayStart.AddDate(0, 0, -29).UnixMilli()
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id,
		   COALESCE(SUM(CASE WHEN started_at >= ? THEN seconds END), 0),
		   COALESCE(SUM(CASE WHEN started_at >= ? THEN seconds END), 0),
		   COALESCE(SUM(CASE WHEN started_at >= ? THEN seconds END), 0),
		   COALESCE(SUM(seconds), 0), MAX(ended_at)
		 FROM listen_log WHERE seconds >= ? GROUP BY user_id`, today, week, month, minLogSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]UserTotals{}
	for rows.Next() {
		var t UserTotals
		if err := rows.Scan(&t.UserID, &t.Today, &t.Week, &t.Month, &t.AllTime, &t.LastListen); err != nil {
			return nil, err
		}
		out[t.UserID] = t
	}
	return out, rows.Err()
}

// LogEntry is one listening session as the admin sees it.
type LogEntry struct {
	UserID    int64   `json:"user_id"`
	Device    string  `json:"device"`
	Client    string  `json:"client"`
	StartedAt int64   `json:"started_at"`
	EndedAt   int64   `json:"ended_at"`
	Seconds   float64 `json:"seconds"`
}

// Log lists listening sessions since a time, newest first (one user when userID > 0).
func (s *Store) Log(ctx context.Context, since time.Time, userID int64, limit int) ([]LogEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 300
	}
	q := `SELECT user_id, device, client, started_at, ended_at, seconds FROM listen_log WHERE started_at >= ? AND seconds >= ?`
	args := []any{since.UnixMilli(), minLogSeconds}
	if userID > 0 {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	q += ` ORDER BY started_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.UserID, &e.Device, &e.Client, &e.StartedAt, &e.EndedAt, &e.Seconds); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
