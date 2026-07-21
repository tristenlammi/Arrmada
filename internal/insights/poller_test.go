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
