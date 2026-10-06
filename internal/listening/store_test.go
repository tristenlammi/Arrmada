package listening

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

func testStore(t *testing.T) (*Store, *sql.DB, int64, *time.Time) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	res, err := st.DB().Exec(`INSERT INTO users (username, password_hash, role) VALUES ('listener', 'x', 'requester')`)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := res.LastInsertId()
	clock := time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC)
	s := NewStore(st.DB())
	s.now = func() time.Time { return clock }
	return s, st.DB(), uid, &clock
}

// A session survives a "restart" (a fresh Store on the same database) and keeps
// syncing — Audiobookshelf answered "session not found" and dropped every report.
func TestSessionSurvivesRestart(t *testing.T) {
	s, db, uid, clock := testStore(t)
	ctx := context.Background()
	sess, _, err := s.OpenSession(ctx, uid, "b1", "dev1", "Pixel", "Lissen")
	if err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(30 * time.Second)
	if _, err := s.Sync(ctx, uid, sess.ID, 30, 30, 3600, false); err != nil {
		t.Fatal(err)
	}
	restarted := NewStore(db)
	restarted.now = func() time.Time { return *clock }
	*clock = clock.Add(30 * time.Second)
	d, err := restarted.Sync(ctx, uid, sess.ID, 60, 30, 3600, false)
	if err != nil {
		t.Fatalf("sync after restart failed: %v", err)
	}
	if d.Progress.Position != 60 {
		t.Fatalf("position after restart = %v, want 60", d.Progress.Position)
	}
	// A closed session is picked up again too.
	*clock = clock.Add(time.Minute)
	if _, err := restarted.Sync(ctx, uid, sess.ID, 90, 30, 3600, true); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Hour)
	if _, err := restarted.Sync(ctx, uid, sess.ID, 120, 30, 3600, false); err != nil {
		t.Fatalf("sync to a closed session failed: %v", err)
	}
}

// Claimed listening can't exceed the wall time since the last report.
func TestListeningIsCapped(t *testing.T) {
	s, _, uid, clock := testStore(t)
	ctx := context.Background()
	sess, _, _ := s.OpenSession(ctx, uid, "b1", "d", "Pixel", "Lissen")
	*clock = clock.Add(10 * time.Second)
	if _, err := s.Sync(ctx, uid, sess.ID, 10, 99999, 3600, false); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSession(ctx, uid, sess.ID)
	if got.Listened > 10+listenSlack {
		t.Fatalf("listening inflated to %v s", got.Listened)
	}
}

// Uploading the same offline session twice counts it once and doesn't move the place twice.
func TestOfflineUploadIsIdempotent(t *testing.T) {
	s, _, uid, clock := testStore(t)
	ctx := context.Background()
	o := OfflineSession{ID: "off-1", ItemKey: "b1", Device: "Pixel", Client: "Lissen", Position: 1800, Duration: 3600,
		Listened: 1800, StartedAt: clock.Add(-40 * time.Minute).UnixMilli(), UpdatedAt: clock.Add(-5 * time.Minute).UnixMilli()}
	if _, err := s.SyncOffline(ctx, uid, o); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncOffline(ctx, uid, o); err != nil {
		t.Fatal(err)
	}
	totals, _ := s.Totals(ctx, clock.Truncate(24*time.Hour))
	if got := totals[uid].AllTime; got != 1800 {
		t.Fatalf("offline listening counted as %v s, want 1800", got)
	}
	p, _, _ := s.Progress(ctx, uid, "b1")
	if p.Position != 1800 {
		t.Fatalf("offline position = %v", p.Position)
	}
}

// Earlier places are kept and one can be put back.
func TestRestoreEarlierPlace(t *testing.T) {
	s, _, uid, clock := testStore(t)
	ctx := context.Background()
	if _, err := s.SetProgress(ctx, uid, "b1", 2000, 3600, nil, "web"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Hour)
	if _, err := s.SetProgress(ctx, uid, "b1", 100, 3600, nil, "oops"); err != nil {
		t.Fatal(err)
	}
	hist, _ := s.History(ctx, uid, "b1")
	var target int64
	for _, h := range hist {
		if h.Position == 2000 {
			target = h.ID
		}
	}
	if target == 0 {
		t.Fatalf("the 2000 s place is missing from history: %+v", hist)
	}
	if _, err := s.Restore(ctx, uid, "b1", target); err != nil {
		t.Fatal(err)
	}
	p, _, _ := s.Progress(ctx, uid, "b1")
	if p.Position != 2000 {
		t.Fatalf("restored to %v, want 2000", p.Position)
	}
}

// The admin's log carries no book and hides barely-played sessions.
func TestLogHasNoBookAndSkipsTaps(t *testing.T) {
	s, _, uid, clock := testStore(t)
	ctx := context.Background()
	tap, _, _ := s.OpenSession(ctx, uid, "b1", "d", "Pixel", "Lissen")
	*clock = clock.Add(5 * time.Second)
	_, _ = s.Sync(ctx, uid, tap.ID, 5, 5, 3600, true)
	long, _, _ := s.OpenSession(ctx, uid, "b2", "d", "Pixel", "Lissen")
	for i := 0; i < 5; i++ {
		*clock = clock.Add(30 * time.Second)
		_, _ = s.Sync(ctx, uid, long.ID, float64(30*(i+1)), 30, 3600, false)
	}
	log, err := s.Log(ctx, clock.Add(-time.Hour), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 || log[0].Seconds < 140 {
		t.Fatalf("log = %+v, want just the 2½-minute session", log)
	}
}

// A held jump back can be confirmed by the person, which makes it the place at once.
func TestAcceptPendingJump(t *testing.T) {
	s, _, uid, clock := testStore(t)
	ctx := context.Background()
	sess, _, err := s.OpenSession(ctx, uid, "b1", "dev1", "Pixel", "Lissen")
	if err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Minute)
	if _, err := s.Sync(ctx, uid, sess.ID, 3000, 60, 36000, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptPending(ctx, uid, "b1"); err != ErrNoPending {
		t.Fatalf("accept with nothing held = %v, want ErrNoPending", err)
	}
	*clock = clock.Add(15 * time.Second)
	d, err := s.Sync(ctx, uid, sess.ID, 0, 15, 36000, false)
	if err != nil || d.Reason != "held" {
		t.Fatalf("jump to 0 = %+v %v, want held", d, err)
	}
	d, err = s.AcceptPending(ctx, uid, "b1")
	if err != nil || d.Progress.Position != 0 || d.Progress.PendingPosition != nil {
		t.Fatalf("accept = %+v %v, want position 0 and nothing pending", d.Progress, err)
	}
}

// Daily groups listening by the local day each session started.
func TestDailyTotals(t *testing.T) {
	s, db, uid, _ := testStore(t)
	ctx := context.Background()
	loc := time.FixedZone("AEST", 10*3600)
	day1 := time.Date(2026, 9, 1, 23, 30, 0, 0, loc)
	day2 := time.Date(2026, 9, 2, 0, 30, 0, 0, loc)
	for i, r := range []struct {
		at   time.Time
		secs float64
	}{{day1, 600}, {day2, 300}, {day2.Add(time.Hour), 120}, {day2.Add(2 * time.Hour), 10}} {
		if _, err := db.Exec(`INSERT INTO listen_log (session_id, user_id, device, client, started_at, ended_at, seconds) VALUES (?, ?, '', '', ?, ?, ?)`,
			"s"+string(rune('a'+i)), uid, r.at.UnixMilli(), r.at.UnixMilli(), r.secs); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Daily(ctx, time.Date(2026, 9, 1, 0, 0, 0, 0, loc), uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Day != "2026-09-01" || got[0].Seconds != 600 || got[1].Day != "2026-09-02" || got[1].Seconds != 420 {
		t.Fatalf("daily = %+v", got)
	}
}

// Live lists sessions playing now — not closed ones, not offline uploads — and
// SessionItem answers only for the session's own listener.
func TestLiveAndSessionItem(t *testing.T) {
	s, db, uid, clock := testStore(t)
	ctx := context.Background()
	res, _ := db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('other', 'x', 'requester')`)
	other, _ := res.LastInsertId()

	live, _, _ := s.OpenSession(ctx, uid, "b1", "dev1", "Pixel", "Lissen")
	done, _, _ := s.OpenSession(ctx, uid, "b2", "dev1", "Pixel", "Lissen")
	*clock = clock.Add(30 * time.Second)
	_, _ = s.Sync(ctx, uid, live.ID, 300, 30, 3600, false)
	_, _ = s.Sync(ctx, uid, done.ID, 30, 30, 3600, true)
	_, _ = s.SyncOffline(ctx, uid, OfflineSession{ID: "off1", ItemKey: "b3", Position: 50, Duration: 3600, Listened: 50,
		StartedAt: clock.Add(-time.Minute).UnixMilli(), UpdatedAt: clock.UnixMilli()})

	got, err := s.Live(ctx, clock.Add(-10*time.Minute))
	if err != nil || len(got) != 1 || got[0].SessionID != live.ID || got[0].Seconds != 30 {
		t.Fatalf("live = %+v (%v), want only the open session", got, err)
	}
	if key, pos, ok := s.SessionItem(ctx, uid, live.ID); !ok || key != "b1" || pos != 300 {
		t.Fatalf("own session item = %q %v %v", key, pos, ok)
	}
	if _, _, ok := s.SessionItem(ctx, other, live.ID); ok {
		t.Fatal("another user could read which book a session is")
	}
	if got, _ := s.Live(ctx, clock.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("a session quiet since before the window is still live: %+v", got)
	}
}
