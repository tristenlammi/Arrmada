package jobs

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.DB()
}

type busRec struct {
	mu  sync.Mutex
	evs []map[string]any
}

func (b *busRec) Publish(topic string, data any) {
	if topic != TopicUpdated {
		return
	}
	b.mu.Lock()
	b.evs = append(b.evs, data.(map[string]any))
	b.mu.Unlock()
}

func (b *busRec) statuses(id int64) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, e := range b.evs {
		if e["id"] == id {
			out = append(out, e["status"].(string))
		}
	}
	return out
}

func newRunner(t *testing.T) (*Runner, *busRec, *sql.DB) {
	t.Helper()
	db := testDB(t)
	bus := &busRec{}
	r, err := New(context.Background(), db, slog.New(slog.NewTextHandler(io.Discard, nil)), bus)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Shutdown(2 * time.Second) })
	return r, bus, db
}

func waitJob(t *testing.T, r *Runner, id int64) Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Wait(ctx, id); err != nil {
		t.Fatalf("job %d never finished: %v", id, err)
	}
	j, err := r.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestSubmitSingleFlight(t *testing.T) {
	r, bus, _ := newRunner(t)
	release := make(chan struct{})
	calls := 0
	spec := Spec{Kind: "movie.search", Target: "movie:12", Trigger: "user:1", Fn: func(ctx context.Context, p *Progress) (any, error) {
		calls++
		<-release
		p.Set(1, "Grabbed Dune.2021.2160p")
		return map[string]int{"grabbed": 1}, nil
	}}
	id1, ex1, err := r.Submit(context.Background(), spec)
	if err != nil || ex1 {
		t.Fatalf("first submit: %d %v %v", id1, ex1, err)
	}
	id2, ex2, err := r.Submit(context.Background(), spec)
	if err != nil || !ex2 || id2 != id1 {
		t.Fatalf("second submit = %d %v %v, want %d existing", id2, ex2, err, id1)
	}
	// A different target is different work.
	other := spec
	other.Target = "movie:13"
	other.Fn = func(context.Context, *Progress) (any, error) { return nil, nil }
	id3, ex3, _ := r.Submit(context.Background(), other)
	if ex3 || id3 == id1 {
		t.Fatalf("other target shared a job: %d %v", id3, ex3)
	}
	close(release)
	j := waitJob(t, r, id1)
	if j.Status != StatusSucceeded || j.Progress != 1 || j.Message != "Grabbed Dune.2021.2160p" ||
		string(j.Result) != `{"grabbed":1}` || j.Trigger != "user:1" || j.StartedAt == nil || j.FinishedAt == nil || j.CreatedAt == nil {
		t.Fatalf("job = %+v", j)
	}
	if calls != 1 {
		t.Fatalf("ran %d times", calls)
	}
	// Finished work can be submitted again, as a new job.
	waitJob(t, r, id3)
	if id4, ex4, _ := r.Submit(context.Background(), other); ex4 || id4 == id3 {
		t.Fatalf("resubmit after finish = %d %v", id4, ex4)
	}
	if got := bus.statuses(id1); strings.Join(got, ",") != "queued,running,running,succeeded" && strings.Join(got, ",") != "queued,running,succeeded" {
		t.Fatalf("published = %v", got)
	}
}

func TestClassLimitQueues(t *testing.T) {
	r, _, _ := newRunner(t)
	r.SetLimit(ClassLibraryScan, 1)
	release := make(chan struct{})
	started := make(chan string, 2)
	mk := func(target string) Spec {
		return Spec{Kind: "library.scan", Target: target, Class: ClassLibraryScan, Fn: func(ctx context.Context, p *Progress) (any, error) {
			started <- target
			<-release
			return nil, nil
		}}
	}
	id1, _, _ := r.Submit(context.Background(), mk("movies"))
	id2, _, _ := r.Submit(context.Background(), mk("series"))
	// Either may win the slot; the other must wait for it.
	waiting := id2
	if first := <-started; first == "series" {
		waiting = id1
	}
	select {
	case s := <-started:
		t.Fatalf("%s started while the class was full", s)
	case <-time.After(50 * time.Millisecond):
	}
	if j, _ := r.Get(context.Background(), waiting); j.Status != StatusQueued {
		t.Fatalf("waiting job status = %s, want queued", j.Status)
	}
	close(release)
	for _, id := range []int64{id1, id2} {
		if j := waitJob(t, r, id); j.Status != StatusSucceeded {
			t.Fatalf("job = %+v", j)
		}
	}
}

func TestPanicIsRecorded(t *testing.T) {
	r, bus, _ := newRunner(t)
	id, _, _ := r.Submit(context.Background(), Spec{Kind: "boom", Fn: func(context.Context, *Progress) (any, error) {
		var m map[string]int
		m["x"] = 1
		return nil, nil
	}})
	j := waitJob(t, r, id)
	if j.Status != StatusPanicked || !strings.Contains(j.Error, "panic:") {
		t.Fatalf("job = %+v", j)
	}
	if got := bus.statuses(id); got[len(got)-1] != StatusPanicked {
		t.Fatalf("published = %v", got)
	}
}

func TestTimeoutFails(t *testing.T) {
	r, _, _ := newRunner(t)
	id, _, _ := r.Submit(context.Background(), Spec{Kind: "slow", Timeout: 20 * time.Millisecond, Fn: func(ctx context.Context, _ *Progress) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	j := waitJob(t, r, id)
	if j.Status != StatusFailed || !strings.Contains(j.Error, "deadline") {
		t.Fatalf("job = %+v", j)
	}
}

func TestCancelAndShutdown(t *testing.T) {
	r, _, db := newRunner(t)
	running := make(chan struct{})
	id, _, _ := r.Submit(context.Background(), Spec{Kind: "scan", Target: "all", Fn: func(ctx context.Context, _ *Progress) (any, error) {
		close(running)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	<-running
	if err := r.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, r, id); j.Status != StatusCancelled {
		t.Fatalf("cancelled job = %+v", j)
	}
	if err := r.Cancel(context.Background(), id); !errors.Is(err, ErrFinished) {
		t.Fatalf("cancel finished = %v", err)
	}
	if err := r.Cancel(context.Background(), 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancel missing = %v", err)
	}

	// Shutdown cancels what is running and refuses new work.
	running2 := make(chan struct{})
	id2, _, _ := r.Submit(context.Background(), Spec{Kind: "scan", Target: "all", Fn: func(ctx context.Context, _ *Progress) (any, error) {
		close(running2)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	<-running2
	if left := r.Shutdown(2 * time.Second); left != nil {
		t.Fatalf("left = %v", left)
	}
	j, _ := getJob(context.Background(), db, id2)
	if j.Status != StatusCancelled {
		t.Fatalf("job at shutdown = %+v", j)
	}
	if _, _, err := r.Submit(context.Background(), Spec{Kind: "x", Fn: func(context.Context, *Progress) (any, error) { return nil, nil }}); !errors.Is(err, ErrShutdown) {
		t.Fatalf("submit after shutdown = %v", err)
	}
}

func TestShutdownRecordsJobsThatIgnoreCancel(t *testing.T) {
	r, _, db := newRunner(t)
	release := make(chan struct{})
	defer close(release)
	running := make(chan struct{})
	id, _, _ := r.Submit(context.Background(), Spec{Kind: "stubborn", Target: "all", Fn: func(ctx context.Context, _ *Progress) (any, error) {
		close(running)
		<-release // ignores ctx on purpose
		return nil, nil
	}})
	<-running
	if left := r.Shutdown(20 * time.Millisecond); len(left) != 1 || left[0] != "stubborn all" {
		t.Fatalf("left = %v", left)
	}
	if j, _ := getJob(context.Background(), db, id); j.Status != StatusCancelled || j.FinishedAt == nil {
		t.Fatalf("job = %+v", j)
	}
}

func TestInterruptedOnBoot(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	for _, st := range []string{StatusQueued, StatusRunning, StatusSucceeded} {
		if _, err := db.Exec(`INSERT INTO jobs (kind, status, created_at) VALUES ('k', ?, ?)`, st, ms(now)); err != nil {
			t.Fatal(err)
		}
	}
	r, err := New(context.Background(), db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown(time.Second)
	jobs, err := r.List(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, j := range jobs {
		got[j.Status]++
	}
	if got[StatusInterrupted] != 2 || got[StatusSucceeded] != 1 {
		t.Fatalf("statuses = %v", got)
	}
}

func TestProgressThrottled(t *testing.T) {
	r, bus, db := newRunner(t)
	var clock time.Time = time.Now()
	var cmu sync.Mutex
	r.now = func() time.Time { cmu.Lock(); defer cmu.Unlock(); return clock }
	step := func(d time.Duration) { cmu.Lock(); clock = clock.Add(d); cmu.Unlock() }

	proceed := make(chan struct{})
	checked := make(chan struct{})
	id, _, _ := r.Submit(context.Background(), Spec{Kind: "scan", Fn: func(ctx context.Context, p *Progress) (any, error) {
		p.Set(0.1, "10 of 100")
		p.Set(0.2, "20 of 100") // within the second: memory only
		checked <- struct{}{}
		<-proceed
		step(1100 * time.Millisecond)
		p.Set(0.5, "50 of 100") // a second later: written
		checked <- struct{}{}
		<-proceed
		return nil, nil
	}})
	<-checked
	var pct float64
	var msg string
	_ = db.QueryRow(`SELECT progress, message FROM jobs WHERE id = ?`, id).Scan(&pct, &msg)
	if pct != 0.1 || msg != "10 of 100" {
		t.Fatalf("stored = %v %q, want the first write only", pct, msg)
	}
	if j, _ := r.Get(context.Background(), id); j.Progress != 0.2 || j.Message != "20 of 100" {
		t.Fatalf("Get of a running job = %+v, want the latest progress", j)
	}
	proceed <- struct{}{}
	<-checked
	_ = db.QueryRow(`SELECT progress, message FROM jobs WHERE id = ?`, id).Scan(&pct, &msg)
	if pct != 0.5 || msg != "50 of 100" {
		t.Fatalf("stored = %v %q after a second", pct, msg)
	}
	proceed <- struct{}{}
	if j := waitJob(t, r, id); j.Progress != 1 || j.Message != "50 of 100" {
		t.Fatalf("final = %+v", j)
	}
	// queued, running, two progress publishes, succeeded.
	if got := bus.statuses(id); len(got) != 5 {
		t.Fatalf("published = %v", got)
	}
}

func TestPrune(t *testing.T) {
	r, _, db := newRunner(t)
	now := time.Now()
	old := now.Add(-15 * 24 * time.Hour)
	ins := func(status string, finished time.Time) {
		f := int64(0)
		if !finished.IsZero() {
			f = ms(finished)
		}
		if _, err := db.Exec(`INSERT INTO jobs (kind, status, created_at, finished_at) VALUES ('k', ?, ?, ?)`, status, ms(now), f); err != nil {
			t.Fatal(err)
		}
	}
	ins(StatusSucceeded, old)
	ins(StatusFailed, old)
	for i := 0; i < 4; i++ {
		ins(StatusSucceeded, now)
	}
	n, err := r.Prune(context.Background(), 14*24*time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("pruned %d, want 3 (two old, one over the cap)", n)
	}
	var left int
	_ = db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&left)
	if left != 3 {
		t.Fatalf("left %d rows", left)
	}
}

func TestListFilters(t *testing.T) {
	r, _, _ := newRunner(t)
	for _, tgt := range []string{"movie:1", "movie:2"} {
		id, _, _ := r.Submit(context.Background(), Spec{Kind: "movie.search", Target: tgt, Fn: func(context.Context, *Progress) (any, error) { return nil, nil }})
		waitJob(t, r, id)
	}
	got, err := r.List(context.Background(), Filter{Kind: "movie.search", Target: "movie:2"})
	if err != nil || len(got) != 1 || got[0].Target != "movie:2" {
		t.Fatalf("list = %+v %v", got, err)
	}
	all, _ := r.List(context.Background(), Filter{Limit: 1})
	if len(all) != 1 || all[0].Target != "movie:2" {
		t.Fatalf("newest first with limit: %+v", all)
	}
}

// A job cancelled while it waits for its class never runs Fn, and gives back whatever
// was claimed for it through Abandon.
func TestCancelWhileQueuedAbandons(t *testing.T) {
	r, _, _ := newRunner(t)
	r.SetLimit(ClassLibraryScan, 1)
	release := make(chan struct{})
	defer close(release)
	holding := make(chan struct{})
	if _, _, err := r.Submit(context.Background(), Spec{Kind: "scan", Target: "a", Class: ClassLibraryScan, Fn: func(ctx context.Context, _ *Progress) (any, error) {
		close(holding)
		<-release
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	// The first job must hold the class's one slot before the second is submitted: both
	// race for it otherwise, and the second can win and finish before it's cancelled.
	<-holding
	var ran atomic.Bool
	abandoned := make(chan struct{})
	id2, _, _ := r.Submit(context.Background(), Spec{Kind: "scan", Target: "b", Class: ClassLibraryScan,
		Fn:      func(context.Context, *Progress) (any, error) { ran.Store(true); return nil, nil },
		Abandon: func() { close(abandoned) }})
	time.Sleep(20 * time.Millisecond)
	if err := r.Cancel(context.Background(), id2); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, r, id2); j.Status != StatusCancelled || ran.Load() {
		t.Fatalf("queued job = %+v, ran = %v", j, ran.Load())
	}
	select {
	case <-abandoned:
	case <-time.After(time.Second):
		t.Fatal("Abandon wasn't called")
	}
}
