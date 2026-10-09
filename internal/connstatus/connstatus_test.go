package connstatus

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// clock is a settable time source for the tracker.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTracker(t *testing.T) (*Tracker, *store.Store, *clock) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c := &clock{t: time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)}
	tr := New(st.DB(), nil)
	tr.now = c.now
	return tr, st, c
}

var errDown = errors.New("connection refused")

func TestBackoffLadder(t *testing.T) {
	want := []time.Duration{0, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour, 6 * time.Hour, 6 * time.Hour}
	for i, w := range want {
		if got := backoffFor(i + 1); got != w {
			t.Errorf("backoffFor(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := pauseFor(2, 10*time.Minute); got != 10*time.Minute {
		t.Errorf("Retry-After longer than the step should win, got %v", got)
	}
	if got := pauseFor(4, 10*time.Minute); got != time.Hour {
		t.Errorf("a step longer than Retry-After should win, got %v", got)
	}
	if got := pauseFor(1, 30*24*time.Hour); got != maxRetryAfter {
		t.Errorf("Retry-After should be capped, got %v", got)
	}
}

func TestRecordClimbsTheLadderAndASuccessResets(t *testing.T) {
	tr, _, c := newTracker(t)
	fail := Outcome{Err: errDown, Backoff: true}

	tr.Record(KindIndexer, "1", fail)
	if ok, st := tr.Allow(KindIndexer, "1", false); !ok || st.ConsecutiveFailures != 1 {
		t.Fatalf("first failure must not pause: ok=%v st=%+v", ok, st)
	}
	steps := []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour, 6 * time.Hour}
	for i, step := range steps {
		tr.Record(KindIndexer, "1", fail)
		st, _ := tr.Get(KindIndexer, "1")
		if got := st.BackoffUntil.Sub(c.now()); got != step {
			t.Fatalf("failure %d: backoff %v, want %v", i+2, got, step)
		}
		if ok, _ := tr.Allow(KindIndexer, "1", false); ok {
			t.Fatalf("failure %d: background work allowed while backing off", i+2)
		}
		if st.Phase(c.now()) != PhaseBackingOff {
			t.Fatalf("phase = %s", st.Phase(c.now()))
		}
		c.add(step) // let the pause run out before the next attempt
	}

	tr.Record(KindIndexer, "1", Outcome{Dur: 120 * time.Millisecond})
	st, _ := tr.Get(KindIndexer, "1")
	if st.ConsecutiveFailures != 0 || !st.BackoffUntil.IsZero() || !st.FailingSince.IsZero() || st.Phase(c.now()) != PhaseOK {
		t.Fatalf("a success should clear the failure state: %+v", st)
	}
	if st.AvgMS != 120 {
		t.Errorf("avg_ms = %d", st.AvgMS)
	}
}

func TestFailuresWhilePausedDontEscalate(t *testing.T) {
	tr, _, c := newTracker(t)
	fail := Outcome{Err: errDown, Backoff: true}
	tr.Record(KindIndexer, "1", fail)
	tr.Record(KindIndexer, "1", fail) // 5m pause
	// Two more sweeps that started before the pause land now.
	tr.Record(KindIndexer, "1", fail)
	tr.Record(KindIndexer, "1", fail)
	st, _ := tr.Get(KindIndexer, "1")
	if st.ConsecutiveFailures != 2 || st.BackoffUntil.Sub(c.now()) != 5*time.Minute {
		t.Fatalf("stale failures moved the ladder: %+v", st)
	}
	if got := tr.Counts24h(KindIndexer, "1"); got.Queries != 4 || got.Failures != 4 {
		t.Errorf("counts = %+v, want every attempt counted", got)
	}
}

func TestRetryAfterOverridesAShorterStep(t *testing.T) {
	tr, _, c := newTracker(t)
	tr.Record(KindIndexer, "1", Outcome{Err: errors.New("HTTP 429"), RetryAfter: 600 * time.Second, Backoff: true})
	st, _ := tr.Get(KindIndexer, "1")
	if got := st.BackoffUntil.Sub(c.now()); got != 10*time.Minute {
		t.Fatalf("a 429 with Retry-After: 600 should pause 10 min even on the first failure, got %v", got)
	}
	c.add(9 * time.Minute)
	if ok, _ := tr.Allow(KindIndexer, "1", false); ok {
		t.Fatal("allowed before Retry-After ran out")
	}
}

func TestInteractiveIsAlwaysAllowed(t *testing.T) {
	tr, _, _ := newTracker(t)
	for i := 0; i < 5; i++ {
		tr.Record(KindIndexer, "1", Outcome{Err: errDown, Backoff: true})
	}
	if ok, _ := tr.Allow(KindIndexer, "1", true); !ok {
		t.Fatal("a person's search must always be allowed")
	}
	if ok, _ := tr.Allow(KindIndexer, "1", false); ok {
		t.Fatal("background work should be turned away")
	}
}

func TestNoBackoffOutcomeShowsTheErrorWithoutPausing(t *testing.T) {
	tr, _, c := newTracker(t)
	for i := 0; i < 3; i++ {
		tr.Record(KindDownloadClient, "2", Outcome{Err: errDown})
	}
	st, _ := tr.Get(KindDownloadClient, "2")
	if !st.BackoffUntil.IsZero() || st.Phase(c.now()) != PhaseFailing || st.LastError != "connection refused" {
		t.Fatalf("state = %+v", st)
	}
}

func TestFlushWritesOnlyDirtyRowsAndLoadRestores(t *testing.T) {
	tr, st, c := newTracker(t)
	ctx := context.Background()
	tr.Record(KindIndexer, "1", Outcome{Dur: 50 * time.Millisecond}) // first success: written at once
	tr.Record(KindIndexer, "1", Outcome{Err: errDown, Backoff: true})
	tr.Record(KindIndexer, "1", Outcome{Err: errDown, Backoff: true}) // backing off: written at once
	if err := tr.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	var updated int64
	if err := st.DB().QueryRow(`SELECT updated_at FROM integration_status WHERE kind = 'indexer' AND ref = '1'`).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	// Nothing changed since: a second flush must not touch the row.
	c.add(time.Minute)
	if err := tr.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	var again int64
	_ = st.DB().QueryRow(`SELECT updated_at FROM integration_status WHERE kind = 'indexer' AND ref = '1'`).Scan(&again)
	if again != updated {
		t.Fatalf("a clean flush rewrote the row (%d → %d)", updated, again)
	}

	// A fresh tracker over the same database knows what the old one did.
	tr2 := New(st.DB(), nil)
	tr2.now = c.now
	if err := tr2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	got, ok := tr2.Get(KindIndexer, "1")
	if !ok || got.ConsecutiveFailures != 2 || got.BackoffUntil.IsZero() || got.LastError != "connection refused" || got.LastOKAt.IsZero() {
		t.Fatalf("state after reload = %+v", got)
	}
	if ok, _ := tr2.Allow(KindIndexer, "1", false); ok {
		t.Fatal("the backoff should survive a restart")
	}
	if c := tr2.Counts24h(KindIndexer, "1"); c.Queries != 3 || c.Failures != 2 {
		t.Fatalf("counts after reload = %+v", c)
	}
}

func TestSteadySuccessesWriteNothingUntilFlush(t *testing.T) {
	tr, st, _ := newTracker(t)
	tr.Record(KindIndexer, "1", Outcome{})
	var n int
	_ = st.DB().QueryRow(`SELECT COUNT(*) FROM integration_counts`).Scan(&n)
	if n != 0 {
		t.Fatalf("counters were written before a flush")
	}
	for i := 0; i < 20; i++ {
		tr.Record(KindIndexer, "1", Outcome{})
	}
	_ = st.DB().QueryRow(`SELECT COUNT(*) FROM integration_counts`).Scan(&n)
	if n != 0 {
		t.Fatalf("steady successes wrote counters")
	}
	if err := tr.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	var q int
	_ = st.DB().QueryRow(`SELECT queries FROM integration_counts`).Scan(&q)
	if q != 21 {
		t.Fatalf("queries = %d", q)
	}
}

func TestResetAndForget(t *testing.T) {
	tr, st, c := newTracker(t)
	ctx := context.Background()
	var changes []State
	tr.OnChange(func(s State) { changes = append(changes, s) })
	tr.Record(KindIndexer, "1", Outcome{Err: errDown, Backoff: true})
	tr.Record(KindIndexer, "1", Outcome{Err: errDown, Backoff: true})
	if err := tr.Reset(ctx, KindIndexer, "1"); err != nil {
		t.Fatal(err)
	}
	if s, ok := tr.Get(KindIndexer, "1"); ok || s.Phase(c.now()) != PhaseUnknown {
		t.Fatalf("reset left state behind: %+v", s)
	}
	if got := tr.Counts24h(KindIndexer, "1"); got.Queries != 2 {
		t.Fatalf("reset should keep the counters, got %+v", got)
	}
	if len(changes) != 3 || changes[2].Phase(c.now()) != PhaseUnknown {
		t.Fatalf("OnChange calls = %+v", changes)
	}

	tr.Record(KindIndexer, "1", Outcome{})
	_ = tr.Flush(ctx)
	if err := tr.Forget(ctx, KindIndexer, "1"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = st.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM integration_status) + (SELECT COUNT(*) FROM integration_counts)`).Scan(&n)
	if n != 0 || tr.Counts24h(KindIndexer, "1").Queries != 0 {
		t.Fatalf("forget left %d rows", n)
	}
}

func TestFlushPrunesOldCounters(t *testing.T) {
	tr, st, c := newTracker(t)
	tr.Record(KindIndexer, "1", Outcome{})
	_ = tr.Flush(context.Background())
	c.add(49 * time.Hour)
	tr.Record(KindIndexer, "1", Outcome{})
	_ = tr.Flush(context.Background())
	var n int
	_ = st.DB().QueryRow(`SELECT COUNT(*) FROM integration_counts`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows = %d, want only the recent hour", n)
	}
}

func TestStoredErrorsAreRedacted(t *testing.T) {
	tr, _, _ := newTracker(t)
	tr.Record(KindIndexer, "1", Outcome{Err: errors.New(`Get "https://prowlarr:9696/1/api?t=search&apikey=abcdef123": dial tcp: refused`)})
	st, _ := tr.Get(KindIndexer, "1")
	if strings.Contains(st.LastError, "abcdef123") || strings.Contains(st.LastError, "apikey") {
		t.Fatalf("secret stored: %q", st.LastError)
	}
}

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"https://x.test/api?t=caps&apikey=SECRET":     "https://x.test/api?…",
		"login form apikey=SECRET&x=1":                "login form apikey=…&x=1",
		"api_key=SECRET passkey=SECRET token=SECRET":  "api_key=… passkey=… token=…",
		"mam_id=SECRET; password=SECRET":              "mam_id=…; password=…",
		"torrentleech: login failed — check password": "torrentleech: login failed — check password",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 500)
	if got := []rune(Redact(long)); len(got) != maxErrorRunes {
		t.Errorf("capped length = %d runes", len(got))
	}
}

func TestNilTrackerIsHarmless(t *testing.T) {
	var tr *Tracker
	tr.Record(KindIndexer, "1", Outcome{Err: errDown, Backoff: true})
	if ok, _ := tr.Allow(KindIndexer, "1", false); !ok {
		t.Fatal("nil tracker must allow")
	}
	if err := tr.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = tr.Reset(context.Background(), KindIndexer, "1")
	_ = tr.List(KindIndexer)
}
