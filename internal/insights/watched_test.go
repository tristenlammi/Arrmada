package insights

import (
	"context"
	"testing"
	"time"
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

// History and the Recently watched card compute watch time in Go (toHistoryEntry →
// watchedSecs) and the Users/Graphs totals compute it in SQL (watchedExpr). If the two ever
// disagree, a row shows one number and the total it feeds another.
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

// An imported Tautulli row that spans 34 hours but reports 34 minutes watched must show
// 34 minutes in History — the inline wall-clock formula History used to have showed 34h.
func TestHistoryWatchedUsesWatchedMS(t *testing.T) {
	svc := newDataTestService(t)
	ctx := context.Background()
	db := svc.repo.db
	if _, err := db.ExecContext(ctx,
		`INSERT INTO stream_sessions (session_key,user_id,title,started_at,stopped_at,paused_ms,watched_ms)
		 VALUES ('','u','Imported',1000,1000+34*3600,0,34*60*1000),
		        ('42','u','Live',200000,200000+7200,1800000,0)`); err != nil {
		t.Fatal(err)
	}
	res, err := svc.History(ctx, HistoryFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, r := range res.Rows {
		got[r.Title] = r.WatchedSecs
	}
	if got["Imported"] != 34*60 {
		t.Errorf("imported row watched = %ds, want %ds", got["Imported"], 34*60)
	}
	if got["Live"] != 7200-1800 {
		t.Errorf("live row watched = %ds, want %ds (wall minus paused)", got["Live"], 7200-1800)
	}
}

// Summing the History column for a user must give the Users tab's total for that user.
func TestHistoryTotalsMatchSQL(t *testing.T) {
	svc := newDataTestService(t)
	ctx := context.Background()
	db := svc.repo.db
	rows := []HistoryRow{
		{StartedAt: 0, StoppedAt: 34 * 3600, WatchedMS: 34 * 60 * 1000},
		{StartedAt: 100, StoppedAt: 100 + 2*3600, PausedMS: 30 * 60 * 1000},
		{StartedAt: 200, StoppedAt: 800, WatchedMS: 9999 * 1000},
		{StartedAt: 500, StoppedAt: 100},
		{StartedAt: 300, StoppedAt: 900, PausedMS: 9999 * 1000},
	}
	for i, r := range rows {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO stream_sessions (session_key,user_id,started_at,stopped_at,paused_ms,watched_ms)
			 VALUES (?,'u',?,?,?,?)`, i, r.StartedAt, r.StoppedAt, r.PausedMS, r.WatchedMS); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO plex_users (id,username) VALUES ('u','Una')`); err != nil {
		t.Fatal(err)
	}
	res, err := svc.History(ctx, HistoryFilter{UserID: "u", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var listTotal int64
	for _, r := range res.Rows {
		listTotal += r.WatchedSecs
	}
	var sqlTotal int64
	if err := db.QueryRowContext(ctx, `SELECT `+watchedSum+` FROM stream_sessions WHERE user_id='u'`).Scan(&sqlTotal); err != nil {
		t.Fatal(err)
	}
	users, err := svc.repo.users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].TotalSecs != listTotal || sqlTotal != listTotal {
		t.Errorf("History sums to %ds, SQL %ds, Users tab %+v — they must agree", listTotal, sqlTotal, users)
	}
}

// The Recently watched card used to build its rows without a watch time at all.
func TestStatsRecentHasWatchedSecs(t *testing.T) {
	svc := newDataTestService(t)
	ctx := context.Background()
	now := time.Now().Unix()
	if _, err := svc.repo.db.ExecContext(ctx,
		`INSERT INTO stream_sessions (session_key,user_id,title,media_type,started_at,stopped_at,watched_ms,view_offset_ms,duration_ms)
		 VALUES ('','u','Imported','movie',?,?,?,?,?)`, now-34*3600, now, 34*60*1000, 3000, 2000); err != nil {
		t.Fatal(err)
	}
	st, err := svc.Stats(ctx, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Recent) != 1 || st.Recent[0].WatchedSecs != 34*60 {
		t.Fatalf("recent = %+v, want one row watched 34m", st.Recent)
	}
	if st.Recent[0].ProgressPct != 100 {
		t.Errorf("progress = %d, want clamped to 100", st.Recent[0].ProgressPct)
	}
}
