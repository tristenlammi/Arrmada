package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// testBox is an Outbox over a scratch database with a clock the test moves.
func testBox(t *testing.T) (*Outbox, *sql.DB, *time.Time) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clock := time.Unix(1_800_000_000, 0)
	o := New(st.DB(), quietLog())
	o.now = func() time.Time { return clock }
	return o, st.DB(), &clock
}

type rowState struct {
	attempts                int
	nextAt, doneAt, failed  int64
	lastError, payload, key string
}

func readRow(t *testing.T, db *sql.DB, consumer string) rowState {
	t.Helper()
	var r rowState
	err := db.QueryRow(`SELECT attempts, next_at, done_at, failed_at, last_error, payload, dedupe_key
		FROM outbox WHERE consumer = ? ORDER BY id DESC LIMIT 1`, consumer).
		Scan(&r.attempts, &r.nextAt, &r.doneAt, &r.failed, &r.lastError, &r.payload, &r.key)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func countRows(t *testing.T, db *sql.DB, where string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE ` + where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func drain(t *testing.T, o *Outbox) {
	t.Helper()
	if _, err := o.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Every consumer of a topic gets its own row, and each handler sees the payload.
func TestEnqueueWritesOneRowPerConsumer(t *testing.T) {
	o, db, _ := testBox(t)
	ctx := context.Background()
	var got []string
	var mu sync.Mutex
	for _, name := range []string{"convert", "subtitles", "requests.ready"} {
		o.Register(TopicMovieImported, name, func(_ context.Context, p json.RawMessage) error {
			var m MovieImported
			if err := json.Unmarshal(p, &m); err != nil {
				return err
			}
			mu.Lock()
			got = append(got, name+":"+m.Path)
			mu.Unlock()
			return nil
		})
	}
	if err := o.Enqueue(ctx, db, TopicMovieImported, MovieImported{MovieID: 7, Path: "/m/Dune.mkv"}, ""); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "1=1"); n != 3 {
		t.Fatalf("%d rows, want one per consumer (3)", n)
	}
	drain(t, o)
	if len(got) != 3 || got[0] != "convert:/m/Dune.mkv" {
		t.Fatalf("handlers ran %v", got)
	}
	if n := countRows(t, db, "done_at != 0"); n != 3 {
		t.Fatalf("%d rows done, want 3", n)
	}
	// A done row isn't run again.
	drain(t, o)
	if len(got) != 3 {
		t.Fatalf("a finished row ran again: %v", got)
	}
}

// A topic nobody consumes writes nothing.
func TestEnqueueWithoutConsumersWritesNothing(t *testing.T) {
	o, db, _ := testBox(t)
	if err := o.Enqueue(context.Background(), db, "nobody.listens", map[string]int{"id": 1}, ""); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "1=1"); n != 0 {
		t.Fatalf("%d rows for an unconsumed topic", n)
	}
}

// An error is recorded and retried after the backoff — not before.
func TestErrorIsRetriedWithBackoff(t *testing.T) {
	o, db, clock := testBox(t)
	var calls atomic.Int32
	o.Register("t", "c", func(context.Context, json.RawMessage) error {
		if calls.Add(1) == 1 {
			return errors.New("index locked")
		}
		return nil
	})
	if err := o.Enqueue(context.Background(), db, "t", 1, ""); err != nil {
		t.Fatal(err)
	}
	drain(t, o)
	r := readRow(t, db, "c")
	if r.attempts != 1 || r.lastError != "index locked" || r.doneAt != 0 {
		t.Fatalf("after a failure: %+v", r)
	}
	if want := clock.Add(30 * time.Second).Unix(); r.nextAt != want {
		t.Fatalf("next_at = %d, want now+30s (%d)", r.nextAt, want)
	}
	drain(t, o) // not due yet
	if calls.Load() != 1 {
		t.Fatalf("retried before the backoff: %d calls", calls.Load())
	}
	*clock = clock.Add(31 * time.Second)
	drain(t, o)
	if calls.Load() != 2 {
		t.Fatalf("not retried after the backoff: %d calls", calls.Load())
	}
	if r := readRow(t, db, "c"); r.doneAt == 0 || r.lastError != "" {
		t.Fatalf("success not recorded: %+v", r)
	}
}

// A panicking handler is caught, recorded as the row's error, and retried.
func TestPanicIsCapturedAsLastError(t *testing.T) {
	o, db, clock := testBox(t)
	var calls atomic.Int32
	o.Register("t", "c", func(context.Context, json.RawMessage) error {
		if calls.Add(1) == 1 {
			var m map[string]int
			m["boom"] = 1 // nil map write
		}
		return nil
	})
	if err := o.Enqueue(context.Background(), db, "t", 1, ""); err != nil {
		t.Fatal(err)
	}
	drain(t, o)
	r := readRow(t, db, "c")
	if r.attempts != 1 || !strings.Contains(r.lastError, "panic") {
		t.Fatalf("panic not recorded: %+v", r)
	}
	*clock = clock.Add(time.Minute)
	drain(t, o)
	if r := readRow(t, db, "c"); r.doneAt == 0 {
		t.Fatalf("not retried after the panic: %+v", r)
	}
}

// After twenty failures the row is marked failed, stays visible, and stops running.
func TestGivesUpAfterTwentyAttempts(t *testing.T) {
	o, db, clock := testBox(t)
	var calls atomic.Int32
	o.Register("t", "c", func(context.Context, json.RawMessage) error {
		calls.Add(1)
		return errors.New("still broken")
	})
	if err := o.Enqueue(context.Background(), db, "t", 1, ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		drain(t, o)
		*clock = clock.Add(2 * time.Hour)
	}
	if calls.Load() != 20 {
		t.Fatalf("handler ran %d times, want 20", calls.Load())
	}
	r := readRow(t, db, "c")
	if r.failed == 0 || r.attempts != 20 || r.lastError != "still broken" {
		t.Fatalf("not marked failed: %+v", r)
	}
	st, err := o.Stats(context.Background())
	if err != nil || st.Failed != 1 || st.Pending != 0 {
		t.Fatalf("stats = %+v, %v", st, err)
	}

	// Retry brings it back with a clean slate.
	var id int64
	if err := db.QueryRow(`SELECT id FROM outbox`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := o.Retry(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if r := readRow(t, db, "c"); r.failed != 0 || r.attempts != 0 || r.nextAt != 0 {
		t.Fatalf("retry didn't revive the row: %+v", r)
	}
}

// Rows left by a previous run (a crash, a restart) are processed by a new Outbox.
func TestNewOutboxProcessesLeftovers(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	first := New(st.DB(), quietLog())
	first.Register(TopicMovieImported, "convert", func(context.Context, json.RawMessage) error {
		t.Fatal("the first process never got to run it")
		return nil
	})
	if err := first.Enqueue(context.Background(), st.DB(), TopicMovieImported, MovieImported{MovieID: 3}, ""); err != nil {
		t.Fatal(err)
	}
	// "Restart": a fresh Outbox with the handler registered again.
	second := New(st.DB(), quietLog())
	var ran atomic.Int64
	second.Register(TopicMovieImported, "convert", func(_ context.Context, p json.RawMessage) error {
		var m MovieImported
		_ = json.Unmarshal(p, &m)
		ran.Store(m.MovieID)
		return nil
	})
	if _, err := second.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 3 {
		t.Fatal("the leftover row wasn't processed after the restart")
	}
}

// Enqueue inside a transaction that rolls back leaves nothing behind.
func TestEnqueueInRolledBackTxLeavesNoRow(t *testing.T) {
	o, db, _ := testBox(t)
	o.Register("t", "c", func(context.Context, json.RawMessage) error { return nil })
	boom := errors.New("import write failed")
	err := store.WithTx(context.Background(), db, func(tx *sql.Tx) error {
		if err := o.Enqueue(context.Background(), tx, "t", 1, ""); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WithTx = %v", err)
	}
	if n := countRows(t, db, "1=1"); n != 0 {
		t.Fatalf("%d rows survived the rollback", n)
	}
	// And a committed one is there.
	if err := store.WithTx(context.Background(), db, func(tx *sql.Tx) error {
		return o.Enqueue(context.Background(), tx, "t", 1, "")
	}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "1=1"); n != 1 {
		t.Fatalf("%d rows after a commit, want 1", n)
	}
}

// While a row with the same key waits, a second enqueue updates it instead of adding one.
// Once it's done, the key can be queued again. Keys are per topic.
func TestPendingDedupeKeyCollapsesDuplicates(t *testing.T) {
	o, db, _ := testBox(t)
	ctx := context.Background()
	var seen []string
	o.Register("t", "c", func(_ context.Context, p json.RawMessage) error {
		seen = append(seen, string(p))
		return nil
	})
	o.Register("u", "c", func(context.Context, json.RawMessage) error { return nil })
	for _, p := range []string{"first", "second"} {
		if err := o.Enqueue(ctx, db, "t", p, "movie:7"); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.Enqueue(ctx, db, "u", "other topic", "movie:7"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "topic = 't'"); n != 1 {
		t.Fatalf("%d pending rows for one key, want 1", n)
	}
	if n := countRows(t, db, "topic = 'u'"); n != 1 {
		t.Fatal("the same key on another topic was swallowed")
	}
	drain(t, o)
	if len(seen) != 1 || seen[0] != `"second"` {
		t.Fatalf("handler saw %v, want just the newest payload", seen)
	}
	if err := o.Enqueue(ctx, db, "t", "third", "movie:7"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "topic = 't'"); n != 2 {
		t.Fatalf("a finished row blocked a new one: %d rows", n)
	}
}

// An enqueue that lands while the handler is already running the older payload makes the
// row run again — the stale run can't close it.
func TestEnqueueDuringRunRunsAgain(t *testing.T) {
	o, db, _ := testBox(t)
	ctx := context.Background()
	var seen []string
	o.Register("t", "c", func(_ context.Context, p json.RawMessage) error {
		seen = append(seen, string(p))
		if len(seen) == 1 {
			// A newer import of the same movie commits mid-run.
			if err := o.Enqueue(ctx, db, "t", "newer", "movie:7"); err != nil {
				t.Error(err)
			}
		}
		return nil
	})
	if err := o.Enqueue(ctx, db, "t", "older", "movie:7"); err != nil {
		t.Fatal(err)
	}
	drain(t, o)
	if r := readRow(t, db, "c"); r.doneAt != 0 {
		t.Fatal("the stale run marked the row done; the newer payload would be lost")
	}
	drain(t, o)
	if len(seen) != 2 || seen[1] != `"newer"` {
		t.Fatalf("handler saw %v, want older then newer", seen)
	}
	if r := readRow(t, db, "c"); r.doneAt == 0 {
		t.Fatal("row not done after the second run")
	}
}

// A row whose consumer isn't registered says so and keeps being retried on the schedule.
func TestRowWithoutHandler(t *testing.T) {
	o, db, _ := testBox(t)
	if _, err := db.Exec(`INSERT INTO outbox (topic, consumer, payload, created_at) VALUES ('t', 'gone', '{}', 1)`); err != nil {
		t.Fatal(err)
	}
	drain(t, o)
	r := readRow(t, db, "gone")
	if r.lastError != "no handler" || r.attempts != 1 || r.doneAt != 0 {
		t.Fatalf("unhandled row: %+v", r)
	}
}

// Prune drops old finished rows and keeps waiting and failed ones.
func TestPruneKeepsUndoneRows(t *testing.T) {
	o, db, clock := testBox(t)
	now := clock.Unix()
	old := now - int64((8 * 24 * time.Hour).Seconds())
	for _, q := range []string{
		`INSERT INTO outbox (topic, consumer, payload, created_at, done_at) VALUES ('t', 'old-done', '{}', 1, ?)`,
		`INSERT INTO outbox (topic, consumer, payload, created_at, done_at) VALUES ('t', 'new-done', '{}', 1, ?)`,
		`INSERT INTO outbox (topic, consumer, payload, created_at, failed_at) VALUES ('t', 'failed', '{}', 1, ?)`,
	} {
		at := old
		if strings.Contains(q, "new-done") {
			at = now
		}
		if _, err := db.Exec(q, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO outbox (topic, consumer, payload, created_at) VALUES ('t', 'pending', '{}', 1)`); err != nil {
		t.Fatal(err)
	}
	n, err := o.Prune(context.Background(), 7*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1", n, err)
	}
	if c := countRows(t, db, "consumer = 'old-done'"); c != 0 {
		t.Fatal("old done row kept")
	}
	if c := countRows(t, db, "consumer IN ('new-done', 'failed', 'pending')"); c != 3 {
		t.Fatalf("pruned rows it should have kept: %d left of 3", c)
	}
}

// Run picks up an enqueue straight away (the nudge) and stops with its context.
func TestRunDrainsOnNudge(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	o := New(st.DB(), quietLog())
	o.poll = time.Hour // only the nudge can wake it in time
	done := make(chan struct{}, 1)
	o.Register("t", "c", func(context.Context, json.RawMessage) error {
		done <- struct{}{}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { o.Run(ctx); close(stopped) }()
	if err := o.Enqueue(context.Background(), st.DB(), "t", 1, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't pick up the enqueued row")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't stop with its context")
	}
}

func TestBackoff(t *testing.T) {
	for _, c := range []struct {
		failed int
		want   time.Duration
	}{{0, 30 * time.Second}, {1, time.Minute}, {3, 4 * time.Minute}, {7, time.Hour}, {19, time.Hour}} {
		if got := Backoff(c.failed); got != c.want {
			t.Errorf("Backoff(%d) = %v, want %v", c.failed, got, c.want)
		}
	}
}
