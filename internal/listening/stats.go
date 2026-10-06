package listening

import (
	"context"
	"sort"
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

// DayTotal is how long one user listened on one local day.
type DayTotal struct {
	UserID  int64   `json:"user_id"`
	Day     string  `json:"day"` // YYYY-MM-DD, local time
	Seconds float64 `json:"seconds"`
}

// Daily returns listening per user per local day from since (local midnight) onwards,
// oldest first. A session is counted on the day it started. userID > 0 limits it to one
// user.
func (s *Store) Daily(ctx context.Context, since time.Time, userID int64) ([]DayTotal, error) {
	q := `SELECT user_id, started_at, seconds FROM listen_log WHERE started_at >= ? AND seconds >= ?`
	args := []any{since.UnixMilli(), minLogSeconds}
	if userID > 0 {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct {
		user int64
		day  string
	}
	sums := map[key]float64{}
	loc := since.Location()
	for rows.Next() {
		var uid, started int64
		var secs float64
		if err := rows.Scan(&uid, &started, &secs); err != nil {
			return nil, err
		}
		sums[key{uid, time.UnixMilli(started).In(loc).Format("2006-01-02")}] += secs
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]DayTotal, 0, len(sums))
	for k, v := range sums {
		out = append(out, DayTotal{UserID: k.user, Day: k.day, Seconds: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day < out[j].Day
		}
		return out[i].UserID < out[j].UserID
	})
	return out, nil
}

// LiveListen is a session someone is listening in right now, as the dashboard shows it to
// a manager: who, on what, since when and how much — never what. SessionID only lets the
// caller recognise the viewer's own sessions (see SessionItem); it isn't for display.
type LiveListen struct {
	SessionID string  `json:"-"`
	UserID    int64   `json:"user_id"`
	Device    string  `json:"device"`
	Client    string  `json:"client"`
	StartedAt int64   `json:"started_at"`
	LastAt    int64   `json:"last_at"`
	Seconds   float64 `json:"seconds"`
}

// Live lists play sessions that reported since a time, most recent first. It reads the
// listening log (no book column); offline uploads are past listening, not live, and are
// left out.
func (s *Store) Live(ctx context.Context, since time.Time) ([]LiveListen, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT l.session_id, l.user_id, l.device, l.client, l.started_at, l.ended_at, l.seconds
		 FROM listen_log l JOIN listen_sessions s ON s.id = l.session_id
		 WHERE l.ended_at >= ? AND s.offline = 0 AND s.closed = 0
		 ORDER BY l.ended_at DESC`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LiveListen{}
	for rows.Next() {
		var e LiveListen
		if err := rows.Scan(&e.SessionID, &e.UserID, &e.Device, &e.Client, &e.StartedAt, &e.LastAt, &e.Seconds); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SessionItem returns the book and place of one of the user's own sessions. It's keyed by
// the user, so it can only ever answer for the person asking.
func (s *Store) SessionItem(ctx context.Context, userID int64, sessionID string) (itemKey string, position float64, ok bool) {
	err := s.db.QueryRowContext(ctx, `SELECT item_key, cur_pos FROM listen_sessions WHERE id = ? AND user_id = ?`,
		sessionID, userID).Scan(&itemKey, &position)
	return itemKey, position, err == nil
}
