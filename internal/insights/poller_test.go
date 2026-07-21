package insights

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/plex"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// newTestService builds a Service backed by a fresh migrated SQLite DB (bus/geo omitted).
func newTestService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewService(st.DB(), settings.NewService(st.DB()), nil, nil, log)
}

// TestReconcileCreditsOnlyToLastSeen verifies a vanished session is credited up to the last poll
// that actually saw it (lastSeen), not to the wall-clock "now" of the poll that noticed it gone —
// so a Plex outage or a slow poll cannot backfill phantom watch time.
func TestReconcileCreditsOnlyToLastSeen(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)
	sess := plex.Session{SessionKey: "1", RatingKey: "100", UserID: "7", Type: "movie", Title: "Dune"}

	svc.reconcile(ctx, []plex.Session{sess}, t0)                    // start
	svc.reconcile(ctx, []plex.Session{sess}, t0.Add(5*time.Second)) // last time we see it
	// Plex goes away for a long stretch, then the session is gone.
	svc.reconcile(ctx, nil, t0.Add(2*time.Hour))

	rows, _, err := svc.repo.history(ctx, HistoryFilter{Limit: 10})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got, want := rows[0].StoppedAt, t0.Add(5*time.Second).Unix(); got != want {
		t.Errorf("stopped_at = %d, want %d (lastSeen, not the 2h-later poll)", got, want)
	}
	if len(svc.live) != 0 {
		t.Errorf("live sessions = %d, want 0 after vanish", len(svc.live))
	}
}

// TestReconcileSplitsOnRatingKeyChange verifies a reused SessionKey that now carries a different
// item is split into two rows (the binge-merge / session-key-reuse fix), not merged into one.
func TestReconcileSplitsOnRatingKeyChange(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)
	epA := plex.Session{SessionKey: "1", RatingKey: "100", UserID: "7", Type: "episode", Title: "S01E01"}
	epB := plex.Session{SessionKey: "1", RatingKey: "200", UserID: "7", Type: "episode", Title: "S01E02"}

	svc.reconcile(ctx, []plex.Session{epA}, t0)
	svc.reconcile(ctx, []plex.Session{epA}, t0.Add(5*time.Second))  // A alive ≥2s
	svc.reconcile(ctx, []plex.Session{epB}, t0.Add(10*time.Second)) // same key, new item → split
	svc.reconcile(ctx, []plex.Session{epB}, t0.Add(15*time.Second)) // B alive ≥2s
	svc.reconcile(ctx, nil, t0.Add(20*time.Second))                 // B ends

	rows, _, err := svc.repo.history(ctx, HistoryFilter{Limit: 10})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (one per episode)", len(rows))
	}
	// history is ordered by started_at DESC: B first, then A.
	if rows[1].Title != "S01E01" || rows[0].Title != "S01E02" {
		t.Errorf("titles = %q, %q, want S01E02, S01E01", rows[0].Title, rows[1].Title)
	}
	if got, want := rows[1].StoppedAt, t0.Add(5*time.Second).Unix(); got != want {
		t.Errorf("episode A stopped_at = %d, want %d (its own lastSeen)", got, want)
	}
	if got, want := rows[0].StartedAt, t0.Add(10*time.Second).Unix(); got != want {
		t.Errorf("episode B started_at = %d, want %d (when the key switched)", got, want)
	}
}

// TestReconcileSplitsOnUserChange verifies a different user under the same SessionKey (a Plex
// restart reusing a stale key) is not attributed to the previous user.
func TestReconcileSplitsOnUserChange(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)
	a := plex.Session{SessionKey: "1", RatingKey: "100", UserID: "7", Type: "movie", Title: "Dune"}
	b := plex.Session{SessionKey: "1", RatingKey: "100", UserID: "9", Type: "movie", Title: "Dune"}

	svc.reconcile(ctx, []plex.Session{a}, t0)
	svc.reconcile(ctx, []plex.Session{a}, t0.Add(5*time.Second))
	svc.reconcile(ctx, []plex.Session{b}, t0.Add(10*time.Second)) // same key, new user → split
	svc.reconcile(ctx, []plex.Session{b}, t0.Add(15*time.Second))
	svc.reconcile(ctx, nil, t0.Add(20*time.Second))

	rows, _, err := svc.repo.history(ctx, HistoryFilter{Limit: 10})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (one per user)", len(rows))
	}
	if rows[1].UserID != "7" || rows[0].UserID != "9" {
		t.Errorf("user ids = %q, %q, want 9, 7", rows[0].UserID, rows[1].UserID)
	}
}

// TestFlushAllFinalizesLiveSessions verifies the disable/shutdown flush path records in-flight
// sessions immediately, capped at lastSeen, and clears live state so re-enabling starts fresh.
func TestFlushAllFinalizesLiveSessions(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)
	sess := plex.Session{SessionKey: "1", RatingKey: "100", UserID: "7", Type: "movie", Title: "Dune"}

	svc.reconcile(ctx, []plex.Session{sess}, t0)
	svc.reconcile(ctx, []plex.Session{sess}, t0.Add(5*time.Second))
	if len(svc.live) != 1 {
		t.Fatalf("live = %d before flush, want 1", len(svc.live))
	}
	svc.flushAll(ctx) // what Run does when Insights is disabled/unconfigured

	if len(svc.live) != 0 {
		t.Errorf("live = %d after flush, want 0", len(svc.live))
	}
	rows, _, err := svc.repo.history(ctx, HistoryFilter{Limit: 10})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got, want := rows[0].StoppedAt, t0.Add(5*time.Second).Unix(); got != want {
		t.Errorf("stopped_at = %d, want %d (capped at lastSeen, not flush time)", got, want)
	}

	// Re-enabling (a fresh reconcile) starts a brand-new session, not a continuation.
	svc.reconcile(ctx, []plex.Session{sess}, t0.Add(24*time.Hour))
	if ls := svc.live[sess.SessionKey]; ls == nil {
		t.Fatal("expected a fresh live session after re-enable")
	} else if !ls.started.Equal(t0.Add(24 * time.Hour)) {
		t.Errorf("re-enabled session started = %v, want fresh %v", ls.started, t0.Add(24*time.Hour))
	}
}

// TestPollSecondsClamp verifies the interval is clamped to [2,60]s.
func TestPollSecondsClamp(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	for _, tc := range []struct {
		set  string
		want int
	}{{"1", 2}, {"5", 5}, {"300", 60}} {
		if err := svc.settings.Set(ctx, keyPoll, tc.set); err != nil {
			t.Fatal(err)
		}
		if got := svc.pollSeconds(ctx); got != tc.want {
			t.Errorf("pollSeconds(%s) = %d, want %d", tc.set, got, tc.want)
		}
	}
}

// TestObserveBufferSpells verifies buffering is debounced into one event per spell.
func TestObserveBufferSpells(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	ls := &liveSession{started: t0, lastSeen: t0, state: "playing"}
	states := []string{"playing", "buffering", "buffering", "playing", "buffering", "playing"}
	for i, st := range states {
		ls.observe(plex.Session{State: st, OffsetMS: int64(i) * 1000}, t0.Add(time.Duration(i)*time.Second))
	}
	// Two distinct spells (indices 1-2 and 4), not four.
	if ls.bufCount != 2 {
		t.Fatalf("bufCount = %d, want 2", ls.bufCount)
	}
	if len(ls.bufEvents) != 2 {
		t.Fatalf("bufEvents = %d, want 2", len(ls.bufEvents))
	}
}

// TestObservePausedAccrual verifies paused time accrues across intervals spent paused.
func TestObservePausedAccrual(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	ls := &liveSession{started: t0, lastSeen: t0, state: "playing"}
	// playing @0, paused @10, still paused @25 (15s paused accrues), playing @30.
	ls.observe(plex.Session{State: "paused"}, t0.Add(10*time.Second))
	ls.observe(plex.Session{State: "paused"}, t0.Add(25*time.Second))
	ls.observe(plex.Session{State: "playing"}, t0.Add(30*time.Second))
	// From the 10s tick (state became paused) to the 25s tick: prior state paused → +15s.
	// From 25s to 30s: prior state paused → +5s. Total 20s.
	if ls.pausedMS != 20_000 {
		t.Fatalf("pausedMS = %d, want 20000", ls.pausedMS)
	}
}

// TestRecordTranscode verifies the finalized record captures the decision + source→stream detail.
func TestRecordTranscode(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	sess := plex.Session{
		SessionKey: "1", UserID: "42", UserName: "Bob", Type: "movie", Title: "Dune",
		SrcVideoCodec: "hevc", SrcResolution: "4k", SrcAudioCodec: "truehd", SrcContainer: "mkv",
		Transcoding: true, VideoDecision: "transcode", AudioDecision: "transcode",
		StreamVideoCodec: "h264", StreamHeight: 1080, StreamAudioCodec: "aac", TranscodeCont: "mp4",
		PublicIP: "8.8.8.8", Location: "wan", OffsetMS: 5000, DurationMS: 9000000,
	}
	ls := &liveSession{started: t0, lastSeen: t0, state: "playing", sess: sess}
	ls.observe(sess, t0.Add(3*time.Second))
	rec := ls.record(t0.Add(1 * time.Hour))

	if rec.Decision != "transcode" {
		t.Errorf("decision = %q, want transcode", rec.Decision)
	}
	if rec.VideoSrc != "HEVC 4K" || rec.VideoStream != "H264 1080p" {
		t.Errorf("video = %q → %q, want HEVC 4K → H264 1080p", rec.VideoSrc, rec.VideoStream)
	}
	if rec.AudioSrc != "TRUEHD" || rec.AudioStream != "AAC" {
		t.Errorf("audio = %q → %q, want TRUEHD → AAC", rec.AudioSrc, rec.AudioStream)
	}
	if rec.ContainerSrc != "MKV" || rec.ContainerStream != "MP4" {
		t.Errorf("container = %q → %q, want MKV → MP4", rec.ContainerSrc, rec.ContainerStream)
	}
	if rec.IPAddress != "8.8.8.8" || rec.UserID != "42" || rec.StartedAt != t0.Unix() {
		t.Errorf("meta wrong: ip=%q user=%q started=%d", rec.IPAddress, rec.UserID, rec.StartedAt)
	}
}

// TestDirectPlayRecord verifies a direct-play stream records no transcode target.
func TestDirectPlayRecord(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	sess := plex.Session{Title: "Scooby-Doo", Type: "movie", SrcVideoCodec: "h264", SrcResolution: "1080"}
	ls := &liveSession{started: t0, lastSeen: t0, state: "playing", sess: sess}
	rec := ls.record(t0.Add(time.Hour))
	if rec.Decision != "direct_play" {
		t.Errorf("decision = %q, want direct_play", rec.Decision)
	}
	if rec.VideoStream != "" || rec.AudioStream != "" {
		t.Errorf("direct play should have empty stream targets, got video=%q audio=%q", rec.VideoStream, rec.AudioStream)
	}
}

// sessAt builds a session snapshot in a given player state at a playback offset.
func sessAt(state string, offsetMS int64) plex.Session {
	return plex.Session{SessionKey: "b1", RatingKey: "500", UserID: "9", Type: "movie",
		Title: "Heat", State: state, OffsetMS: offsetMS}
}

// bufferState pulls the tracked live session for the buffer tests.
func bufferLS(svc *Service) *liveSession { return svc.live["b1"] }

// Startup fill must not count: a slow client's play-start "buffering" made its owner
// the biggest buffer offender without a single real stall.
func TestBufferStartupFillNotCounted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	t0 := time.Now()

	svc.reconcile(ctx, []plex.Session{sessAt("buffering", 0)}, t0) // first sighting mid startup-fill
	svc.reconcile(ctx, []plex.Session{sessAt("buffering", 0)}, t0.Add(5*time.Second))
	svc.reconcile(ctx, []plex.Session{sessAt("playing", 3_000)}, t0.Add(10*time.Second))
	if ls := bufferLS(svc); ls.bufCount != 0 {
		t.Fatalf("startup fill counted as %d buffer events", ls.bufCount)
	}

	// A genuine stall AFTER steady playback still counts.
	svc.reconcile(ctx, []plex.Session{sessAt("playing", 8_000)}, t0.Add(15*time.Second))
	svc.reconcile(ctx, []plex.Session{sessAt("buffering", 12_900)}, t0.Add(20*time.Second))
	if ls := bufferLS(svc); ls.bufCount != 1 {
		t.Fatalf("real stall not counted, bufCount=%d", ls.bufCount)
	}
}

// A seek refill (offset jumps far beyond wall-clock progress) must not count.
func TestBufferSeekRefillNotCounted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	t0 := time.Now()

	svc.reconcile(ctx, []plex.Session{sessAt("playing", 10_000)}, t0)
	svc.reconcile(ctx, []plex.Session{sessAt("playing", 15_000)}, t0.Add(5*time.Second))
	// Skip intro: offset leaps ~90s while 5s passed → refill, not a stall.
	svc.reconcile(ctx, []plex.Session{sessAt("buffering", 105_000)}, t0.Add(10*time.Second))
	if ls := bufferLS(svc); ls.bufCount != 0 {
		t.Fatalf("seek refill counted as %d buffer events", ls.bufCount)
	}
	// Backwards scrub too.
	svc.reconcile(ctx, []plex.Session{sessAt("playing", 110_000)}, t0.Add(15*time.Second))
	svc.reconcile(ctx, []plex.Session{sessAt("buffering", 20_000)}, t0.Add(20*time.Second))
	if ls := bufferLS(svc); ls.bufCount != 0 {
		t.Fatalf("backward seek counted as %d buffer events", ls.bufCount)
	}
}

// The refill after resuming from pause must not count.
func TestBufferResumeRefillNotCounted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	t0 := time.Now()

	svc.reconcile(ctx, []plex.Session{sessAt("playing", 10_000)}, t0)
	svc.reconcile(ctx, []plex.Session{sessAt("paused", 15_000)}, t0.Add(5*time.Second))
	svc.reconcile(ctx, []plex.Session{sessAt("buffering", 15_000)}, t0.Add(10*time.Second))
	if ls := bufferLS(svc); ls.bufCount != 0 {
		t.Fatalf("pause-resume refill counted as %d buffer events", ls.bufCount)
	}
}

// A counted spell accrues observed stall time across consecutive buffering polls,
// and the duration lands on the recorded event.
func TestBufferSpellAccruesDuration(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	t0 := time.Now()

	svc.reconcile(ctx, []plex.Session{sessAt("playing", 10_000)}, t0)
	svc.reconcile(ctx, []plex.Session{sessAt("playing", 15_000)}, t0.Add(5*time.Second))
	svc.reconcile(ctx, []plex.Session{sessAt("buffering", 19_000)}, t0.Add(10*time.Second)) // stall starts
	svc.reconcile(ctx, []plex.Session{sessAt("buffering", 19_000)}, t0.Add(15*time.Second)) // +5s observed
	svc.reconcile(ctx, []plex.Session{sessAt("buffering", 19_000)}, t0.Add(20*time.Second)) // +5s observed
	svc.reconcile(ctx, []plex.Session{sessAt("playing", 21_000)}, t0.Add(25*time.Second))   // recovers

	ls := bufferLS(svc)
	if ls.bufCount != 1 || len(ls.bufEvents) != 1 {
		t.Fatalf("want one spell, got count=%d events=%d", ls.bufCount, len(ls.bufEvents))
	}
	if got := ls.bufEvents[0].durationMS; got != 10_000 {
		t.Fatalf("spell duration = %dms, want 10000", got)
	}

	// Finalize and confirm the duration reaches the DB.
	svc.reconcile(ctx, nil, t0.Add(30*time.Second))
	var dur int64
	if err := svc.repo.db.QueryRowContext(ctx, `SELECT duration_ms FROM buffer_events LIMIT 1`).Scan(&dur); err != nil {
		t.Fatalf("read buffer event: %v", err)
	}
	if dur != 10_000 {
		t.Fatalf("stored duration = %dms, want 10000", dur)
	}
}

// Offender floor: one sampled blip never makes the leaderboard; a repeated pattern
// or real stall time does — ranked by observed stall time.
func TestReliabilityOffenderFloor(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	now := time.Now().Unix()

	mk := func(user string, bufCount int, durEachMS int64) {
		id, err := svc.repo.insertSession(ctx, sessionRecord{
			UserID: user, UserName: user, RatingKey: "1", MediaType: "movie", Title: "T-" + user,
			Platform: "P-" + user, StartedAt: now - 600, StoppedAt: now - 60, BufferCount: bufCount,
		})
		if err != nil {
			t.Fatalf("insert session: %v", err)
		}
		for i := 0; i < bufCount; i++ {
			if err := svc.repo.insertBufferEvent(ctx, id, now-300, 0, durEachMS, "unknown", ""); err != nil {
				t.Fatalf("insert event: %v", err)
			}
		}
	}
	mk("blip", 1, 0)          // one sampled blip → must NOT appear
	mk("repeat", 3, 0)        // repeated pattern → appears (count floor)
	mk("longstall", 1, 15000) // one long stall → appears (time floor), ranked first

	rel, err := svc.Reliability(ctx, 7)
	if err != nil {
		t.Fatalf("reliability: %v", err)
	}
	names := make([]string, 0, len(rel.ByUser))
	for _, g := range rel.ByUser {
		names = append(names, g.Name)
	}
	if len(names) != 2 || names[0] != "longstall" || names[1] != "repeat" {
		t.Fatalf("offenders = %v, want [longstall repeat] (blip filtered, stall time first)", names)
	}
	if rel.ByUser[0].StallMS != 15000 {
		t.Fatalf("longstall stall_ms = %d, want 15000", rel.ByUser[0].StallMS)
	}
	if rel.Summary.TotalStallMS != 15000 {
		t.Fatalf("summary stall_ms = %d, want 15000", rel.Summary.TotalStallMS)
	}
}
