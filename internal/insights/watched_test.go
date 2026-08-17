package insights

import (
	"context"
	"testing"
)

// The Users tab reported 166,699 hours across 4,857 plays — 34 hours per play, against a
// real average nearer half an hour. Watch time was derived from wall clock, which for an
// imported Tautulli row spans every minute a client sat idle without sending a stop.
func TestWatchedSecsPrefersTheReportedFigure(t *testing.T) {
	const hour = 3600
	cases := []struct {
		name string
		row  HistoryRow
		want int64
	}{
		{
			// The shape behind the bug: a 34-minute episode on a client left open for a
			// day and a half. Wall clock says 34h; Tautulli recorded what was watched.
			name: "abandoned session uses the reported watch time, not the wall clock",
			row:  HistoryRow{StartedAt: 0, StoppedAt: 34 * hour, WatchedMS: 34 * 60 * 1000},
			want: 34 * 60,
		},
		{
			// A live-tracked session has no reported figure — the poller saw the start and
			// the stop itself, so wall time minus paused is the honest answer.
			name: "live session falls back to wall clock minus paused",
			row:  HistoryRow{StartedAt: 0, StoppedAt: 2 * hour, PausedMS: 30 * 60 * 1000},
			want: 90 * 60,
		},
		{
			// Nobody can watch for longer than the session existed.
			name: "a reported figure longer than the session is capped",
			row:  HistoryRow{StartedAt: 0, StoppedAt: 600, WatchedMS: 9999 * 1000},
			want: 600,
		},
		{
			// Corrupt / in-progress rows must not drag a total negative.
			name: "stopped before started is zero, not negative",
			row:  HistoryRow{StartedAt: 500, StoppedAt: 100},
			want: 0,
		},
		{
			name: "paused longer than the wall time is zero, not negative",
			row:  HistoryRow{StartedAt: 0, StoppedAt: 600, PausedMS: 9999 * 1000},
			want: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := watchedSecs(c.row); got != c.want {
				t.Errorf("watchedSecs = %d, want %d", got, c.want)
			}
		})
	}
}

// The History list computes watch time in Go and the Users/Graphs totals compute it in
// SQL. If the two ever disagree, a row shows one number and the total it feeds another.
func TestWatchedExprMatchesWatchedSecs(t *testing.T) {
	db := newDataTestService(t).repo.db
	ctx := context.Background()
	rows := []HistoryRow{
		{StartedAt: 0, StoppedAt: 34 * 3600, WatchedMS: 34 * 60 * 1000},
		{StartedAt: 0, StoppedAt: 2 * 3600, PausedMS: 30 * 60 * 1000},
		{StartedAt: 0, StoppedAt: 600, WatchedMS: 9999 * 1000},
		{StartedAt: 500, StoppedAt: 100},
		{StartedAt: 0, StoppedAt: 600, PausedMS: 9999 * 1000},
	}
	for i, r := range rows {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO stream_sessions (session_key,user_id,started_at,stopped_at,paused_ms,watched_ms)
			 VALUES (?,'u',?,?,?,?)`,
			i, r.StartedAt, r.StoppedAt, r.PausedMS, r.WatchedMS); err != nil {
			t.Fatal(err)
		}
	}
	var sqlTotal int64
	if err := db.QueryRowContext(ctx, `SELECT `+watchedSum+` FROM stream_sessions`).Scan(&sqlTotal); err != nil {
		t.Fatal(err)
	}
	var goTotal int64
	for _, r := range rows {
		goTotal += watchedSecs(r)
	}
	if sqlTotal != goTotal {
		t.Errorf("SQL total %ds != Go total %ds — the History list and the Users totals disagree",
			sqlTotal, goTotal)
	}
}
