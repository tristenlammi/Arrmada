package insights

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/plex"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// restartRig is one database and a bus shared by successive Service instances — each new
// instance is the app after a restart.
type restartRig struct {
	t       *testing.T
	st      *store.Store
	bus     *eventbus.Bus
	started <-chan eventbus.Event
}

func newRestartRig(t *testing.T) *restartRig {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := eventbus.New(nil)
	ch, cancel := bus.Subscribe("plex.stream.started")
	t.Cleanup(cancel)
	return &restartRig{t: t, st: st, bus: bus, started: ch}
}

// boot is a fresh process: a new Service that picks up what the last one saved.
func (r *restartRig) boot() *Service {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewService(r.st.DB(), settings.NewService(r.st.DB()), nil, r.bus, log)
	s.restoreLive(context.Background())
	return s
}

func (r *restartRig) startedEvents() int {
	n := 0
	for {
		select {
		case <-r.started:
			n++
		default:
			return n
		}
	}
}

func (r *restartRig) rows() []HistoryRow {
	r.t.Helper()
	rows, _, err := (&repo{db: r.st.DB()}).history(context.Background(), HistoryFilter{Limit: 50})
	if err != nil {
		r.t.Fatal(err)
	}
	return rows
}

func (r *restartRig) saved() int {
	r.t.Helper()
	var n int
	if err := r.st.DB().QueryRow(`SELECT COUNT(*) FROM insights_live_sessions`).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func at(off time.Duration, offsetMS int64, state string) (time.Time, plex.Session) {
	t0 := time.Unix(1_700_000_000, 0)
	return t0.Add(off), plex.Session{SessionKey: "5", RatingKey: "100", UserID: "7", UserName: "amy", Type: "movie",
		Title: "Dune", State: state, OffsetMS: offsetMS}
}

// An ./update.sh restart mid-film: one play, started before the restart, one "Now playing".
func TestRestartResumesLiveSession(t *testing.T) {
	ctx := context.Background()
	rig := newRestartRig(t)

	s1 := rig.boot()
	for i, st := range []string{"playing", "playing", "buffering", "playing"} {
		now, sess := at(time.Duration(i*5)*time.Second, int64(i*5000), st)
		s1.reconcile(ctx, []plex.Session{sess}, now)
	}
	if rig.saved() != 1 {
		t.Fatalf("saved streams = %d, want 1", rig.saved())
	}
	// Shutdown: Run just returns now; nothing is finalized.

	s2 := rig.boot()
	for _, i := range []int{20, 25} { // back 5 s after the last poll, the position kept moving
		now, sess := at(time.Duration(i)*time.Second, int64(i*1000), "playing")
		s2.reconcile(ctx, []plex.Session{sess}, now)
	}
	end, _ := at(30*time.Second, 0, "")
	s2.reconcile(ctx, nil, end)

	rows := rig.rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want ONE play across the restart", len(rows))
	}
	t0, _ := at(0, 0, "")
	last, _ := at(25*time.Second, 0, "")
	if rows[0].StartedAt != t0.Unix() || rows[0].StoppedAt != last.Unix() || rows[0].PausedMS != 0 {
		t.Errorf("row = %d..%d paused %d, want %d..%d paused 0", rows[0].StartedAt, rows[0].StoppedAt, rows[0].PausedMS, t0.Unix(), last.Unix())
	}
	if rows[0].BufferCount != 1 {
		t.Errorf("buffer count = %d, want the stall from before the restart kept", rows[0].BufferCount)
	}
	var events int
	_ = rig.st.DB().QueryRow(`SELECT COUNT(*) FROM buffer_events WHERE session_id = ?`, rows[0].ID).Scan(&events)
	if events != 1 {
		t.Errorf("buffer events = %d, want 1", events)
	}
	if n := rig.startedEvents(); n != 1 {
		t.Errorf("stream.started published %d times, want 1", n)
	}
	if rig.saved() != 0 {
		t.Errorf("saved streams = %d after the play ended, want 0", rig.saved())
	}
}

// Killed mid-stream (no shutdown at all), back after the stream ended: the play is still
// recorded, up to the last poll that saw it.
func TestCrashRecoveryFinalizesVanished(t *testing.T) {
	ctx := context.Background()
	rig := newRestartRig(t)
	s1 := rig.boot()
	for _, i := range []int{0, 5, 10} {
		now, sess := at(time.Duration(i)*time.Second, int64(i*1000), "playing")
		s1.reconcile(ctx, []plex.Session{sess}, now)
	}

	s2 := rig.boot()
	later, _ := at(3*time.Hour, 0, "")
	s2.reconcile(ctx, nil, later)
	rows := rig.rows()
	last, _ := at(10*time.Second, 0, "")
	if len(rows) != 1 || rows[0].StoppedAt != last.Unix() {
		t.Fatalf("rows = %+v, want one play stopped at its last sighting %d", rows, last.Unix())
	}
	if rig.saved() != 0 {
		t.Errorf("saved streams = %d, want 0", rig.saved())
	}
}

// Back after two hours with the same stream still listed: too long to be the same sitting.
// The old play closes at its last sighting and a new one starts.
func TestResumeGapTooLongStartsNew(t *testing.T) {
	ctx := context.Background()
	rig := newRestartRig(t)
	s1 := rig.boot()
	for _, i := range []int{0, 5} {
		now, sess := at(time.Duration(i)*time.Second, int64(i*1000), "playing")
		s1.reconcile(ctx, []plex.Session{sess}, now)
	}

	s2 := rig.boot()
	now, sess := at(2*time.Hour, 7_200_000, "playing")
	s2.reconcile(ctx, []plex.Session{sess}, now)
	rows := rig.rows()
	last, _ := at(5*time.Second, 0, "")
	if len(rows) != 1 || rows[0].StoppedAt != last.Unix() {
		t.Fatalf("rows = %+v, want the old play closed at %d", rows, last.Unix())
	}
	if ls := s2.live["5"]; ls == nil || !ls.started.Equal(now) {
		t.Fatalf("live = %+v, want a fresh play starting %v", s2.live["5"], now)
	}
	if n := rig.startedEvents(); n != 2 {
		t.Errorf("stream.started published %d times, want 2 (the original and the new play)", n)
	}
}

// Downtime counts as watching only as far as the position moved.
func TestResumeGapCreditedAsPausedWhenOffsetStalled(t *testing.T) {
	for _, tc := range []struct {
		name       string
		offsetBack int64 // position at the first poll after the restart
		wantPaused int64
	}{
		{"position stood still", 5_000, 60_000},
		{"position moved on the whole time", 65_000, 0},
		{"position moved half the time", 35_000, 30_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			rig := newRestartRig(t)
			s1 := rig.boot()
			for _, i := range []int{0, 5} {
				now, sess := at(time.Duration(i)*time.Second, int64(i*1000), "playing")
				s1.reconcile(ctx, []plex.Session{sess}, now)
			}
			s2 := rig.boot()
			now, sess := at(65*time.Second, tc.offsetBack, "playing") // a 60 s gap
			s2.reconcile(ctx, []plex.Session{sess}, now)
			now, sess = at(70*time.Second, tc.offsetBack+5000, "playing")
			s2.reconcile(ctx, []plex.Session{sess}, now)
			end, _ := at(75*time.Second, 0, "")
			s2.reconcile(ctx, nil, end)
			rows := rig.rows()
			if len(rows) != 1 || rows[0].PausedMS != tc.wantPaused {
				t.Fatalf("rows = %+v, want one play with paused %d ms", rows, tc.wantPaused)
			}
		})
	}
}

// Recording a finished play and forgetting its saved copy happen together: when the
// insert fails, the saved copy survives, and the next start records the play — once.
func TestFinalizeIsAtomic(t *testing.T) {
	ctx := context.Background()
	rig := newRestartRig(t)
	s1 := rig.boot()
	for _, i := range []int{0, 5} {
		now, sess := at(time.Duration(i)*time.Second, int64(i*1000), "playing")
		s1.reconcile(ctx, []plex.Session{sess}, now)
	}
	if _, err := rig.st.DB().Exec(`CREATE TRIGGER boom BEFORE INSERT ON stream_sessions BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	end, _ := at(10*time.Second, 0, "")
	s1.reconcile(ctx, nil, end)
	if len(rig.rows()) != 0 || rig.saved() != 1 {
		t.Fatalf("after a failed insert: rows %d, saved %d; want 0 and the saved copy kept", len(rig.rows()), rig.saved())
	}

	if _, err := rig.st.DB().Exec(`DROP TRIGGER boom`); err != nil {
		t.Fatal(err)
	}
	s2 := rig.boot()
	s2.reconcile(ctx, nil, end.Add(time.Minute))
	if len(rig.rows()) != 1 || rig.saved() != 0 {
		t.Fatalf("after the restart: rows %d, saved %d; want the play recorded once", len(rig.rows()), rig.saved())
	}
}

// Turning monitoring off records the play and leaves nothing saved to resume later.
func TestFlushAllEmptiesSavedStreams(t *testing.T) {
	ctx := context.Background()
	rig := newRestartRig(t)
	s1 := rig.boot()
	for _, i := range []int{0, 5} {
		now, sess := at(time.Duration(i)*time.Second, int64(i*1000), "playing")
		s1.reconcile(ctx, []plex.Session{sess}, now)
	}
	s2 := rig.boot() // monitoring was switched off while Arrmada was down
	s2.flushAll(ctx)
	if len(rig.rows()) != 1 || rig.saved() != 0 || len(s2.live) != 0 {
		t.Fatalf("rows %d, saved %d, live %d; want the play recorded and nothing left", len(rig.rows()), rig.saved(), len(s2.live))
	}
}

// Stopping the loop doesn't finalize what's playing: it's saved, for the next start.
func TestRunStopDoesNotFinalize(t *testing.T) {
	ctx := context.Background()
	rig := newRestartRig(t)
	s1 := rig.boot()
	for _, i := range []int{0, 5} {
		now, sess := at(time.Duration(i)*time.Second, int64(i*1000), "playing")
		s1.reconcile(ctx, []plex.Session{sess}, now)
	}
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	s1.Run(stopped)
	if len(rig.rows()) != 0 || rig.saved() != 1 {
		t.Fatalf("after stopping: rows %d, saved %d; want 0 and 1", len(rig.rows()), rig.saved())
	}
}
