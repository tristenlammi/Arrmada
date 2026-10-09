package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// waitFor polls cond until it holds or two seconds pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func statusOf(t *testing.T, s *Scheduler, name string) TaskStatus {
	t.Helper()
	st, ok := s.Status(name)
	if !ok {
		t.Fatalf("task %q not registered", name)
	}
	return st
}

// runOnce presses Run now and waits for that run to be recorded.
func runOnce(t *testing.T, s *Scheduler, name string) TaskStatus {
	t.Helper()
	before := statusOf(t, s, name).Runs
	if _, _, err := s.RunNow(name, ""); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	waitFor(t, "the run to finish", func() bool {
		st := statusOf(t, s, name)
		return st.Runs > before && !st.Running
	})
	return statusOf(t, s, name)
}

func started(t *testing.T, s *Scheduler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	t.Cleanup(func() { cancel(); s.Wait() })
}

func TestSchedulerRecordsFailureAndSuccess(t *testing.T) {
	s := New(quietLogger())
	var calls int32
	s.Register("flaky", time.Hour, false, func(context.Context) error {
		if atomic.AddInt32(&calls, 1) <= 2 {
			return errors.New("indexer unreachable")
		}
		return nil
	}, Label("Flaky thing"))
	started(t, s)

	st := runOnce(t, s, "flaky")
	if st.ConsecutiveFailures != 1 || st.LastError != "indexer unreachable" || st.LastStatus != StatusFailed || st.LastOK || st.LastErrorAt == nil {
		t.Fatalf("after one failure: %+v", st)
	}
	st = runOnce(t, s, "flaky")
	if st.ConsecutiveFailures != 2 || st.Failures != 2 {
		t.Fatalf("after two failures: %+v", st)
	}
	st = runOnce(t, s, "flaky")
	if st.ConsecutiveFailures != 0 || st.LastError != "" || !st.LastOK || st.Failures != 2 || st.Runs != 3 {
		t.Fatalf("after a success: %+v", st)
	}
	if st.Label != "Flaky thing" || st.LastStart == nil || st.LastEnd == nil {
		t.Fatalf("label/times: %+v", st)
	}
}

func TestExecRecoversPanic(t *testing.T) {
	s := New(quietLogger())
	s.Register("crashy", time.Hour, false, func(context.Context) error {
		panic("boom")
	})
	started(t, s)
	st := runOnce(t, s, "crashy")
	if st.LastStatus != StatusPanicked || st.LastError != "panic: boom" || st.Failures != 1 || st.Running {
		t.Fatalf("status after panic = %+v", st)
	}
}

func TestRunNowTriggersImmediately(t *testing.T) {
	s := New(quietLogger())
	ran := make(chan struct{}, 1)
	s.Register("hourly", time.Hour, false, func(context.Context) error {
		ran <- struct{}{}
		return nil
	})
	started(t, s)
	waitFor(t, "the schedule to start", func() bool { return statusOf(t, s, "hourly").NextRun != nil })
	next := statusOf(t, s, "hourly").NextRun
	if _, _, err := s.RunNow("hourly", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("Run now didn't run the task")
	}
	waitFor(t, "the run to be recorded", func() bool { return statusOf(t, s, "hourly").Runs == 1 })
	// A Run now doesn't reset the cadence.
	if after := statusOf(t, s, "hourly").NextRun; next == nil || after == nil || !after.Equal(*next) {
		t.Fatalf("next run moved: %v → %v", next, after)
	}
	if _, _, err := s.RunNow("nope", ""); !errors.Is(err, ErrUnknownTask) {
		t.Fatalf("unknown task: %v", err)
	}
}

func TestRunNowWhileRunningReturnsErrBusy(t *testing.T) {
	s := New(quietLogger())
	release := make(chan struct{})
	var runs int32
	s.Register("slow", time.Hour, false, func(ctx context.Context) error {
		atomic.AddInt32(&runs, 1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	started(t, s)
	if _, _, err := s.RunNow("slow", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run to start", func() bool { return atomic.LoadInt32(&runs) == 1 })
	if _, _, err := s.RunNow("slow", ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("RunNow while running = %v, want ErrBusy", err)
	}
	// A ticker fire during the run (exec is what the ticker calls) is skipped, not queued.
	s.exec(context.Background(), s.find("slow"), TriggerSchedule)
	if st := statusOf(t, s, "slow"); st.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1", st.Skipped)
	}
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("runs while busy = %d, want 1", got)
	}
	close(release)
}

func TestNextRunComputed(t *testing.T) {
	s := New(quietLogger())
	s.Register("hourly", time.Hour, false, func(context.Context) error { return nil })
	before := time.Now()
	started(t, s)
	waitFor(t, "next run to be set", func() bool { return statusOf(t, s, "hourly").NextRun != nil })
	next := *statusOf(t, s, "hourly").NextRun
	if next.Before(before.Add(time.Hour)) || next.After(time.Now().Add(time.Hour)) {
		t.Fatalf("next run %v not an hour from start (%v)", next, before)
	}
	if st := statusOf(t, s, "hourly"); st.IntervalSeconds != 3600 || st.Label != "hourly" {
		t.Fatalf("status = %+v", st)
	}
}

func TestLateRegisteredTaskTracked(t *testing.T) {
	s := New(quietLogger())
	started(t, s)
	s.Register("late", time.Hour, true, func(context.Context) error { return nil })
	waitFor(t, "the late task to run", func() bool { return statusOf(t, s, "late").Runs == 1 })
	if len(s.Snapshot()) != 1 {
		t.Fatalf("snapshot = %+v", s.Snapshot())
	}
}

// memStore is an in-memory Store.
type memStore struct {
	mu    sync.Mutex
	rows  map[string]Persisted
	saves int
}

func (m *memStore) Load(context.Context) (map[string]Persisted, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Persisted{}
	for k, v := range m.rows {
		out[k] = v
	}
	return out, nil
}

func (m *memStore) Save(_ context.Context, name string, p Persisted) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[string]Persisted{}
	}
	m.rows[name] = p
	m.saves++
	return nil
}

func (m *memStore) get(name string) (Persisted, bool, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.rows[name]
	return p, ok, m.saves
}

func TestStoreLoadedOnStart(t *testing.T) {
	last := time.Now().Add(-3 * time.Hour).Truncate(time.Millisecond)
	ms := &memStore{rows: map[string]Persisted{
		"backup": {Every: time.Hour, LastStart: last, LastEnd: last.Add(time.Second), LastDuration: time.Second,
			LastStatus: StatusFailed, LastError: "disk full", LastErrorAt: last.Add(time.Second), Runs: 7, Failures: 2, ConsecutiveFailures: 2},
		"late": {Runs: 4, LastStatus: StatusOK},
	}}
	s := New(quietLogger())
	s.SetStore(ms)
	s.Register("backup", time.Hour, false, func(context.Context) error { return nil })
	started(t, s)
	st := statusOf(t, s, "backup")
	if st.Runs != 7 || st.Failures != 2 || st.ConsecutiveFailures != 2 || st.LastError != "disk full" ||
		st.LastStart == nil || !st.LastStart.Equal(last) || st.LastStatus != StatusFailed {
		t.Fatalf("restored status = %+v", st)
	}
	// Registered after Start, still restored.
	s.Register("late", time.Hour, false, func(context.Context) error { return nil })
	if st := statusOf(t, s, "late"); st.Runs != 4 {
		t.Fatalf("late task not restored: %+v", st)
	}
	// The next run carries on from the saved counts and is saved.
	st = runOnce(t, s, "backup")
	if st.Runs != 8 || st.ConsecutiveFailures != 0 {
		t.Fatalf("after run: %+v", st)
	}
	if p, ok, _ := ms.get("backup"); !ok || p.Runs != 8 || p.LastStatus != StatusOK || p.Every != time.Hour {
		t.Fatalf("saved = %+v", p)
	}
}

func TestHiddenTaskNeverSavedOrListed(t *testing.T) {
	ms := &memStore{}
	s := New(quietLogger())
	s.SetStore(ms)
	s.Register("heartbeat", time.Hour, false, func(context.Context) error { return nil }, Hidden())
	started(t, s)
	runOnce(t, s, "heartbeat")
	if _, ok, _ := ms.get("heartbeat"); ok {
		t.Fatal("hidden task was saved")
	}
	if st := statusOf(t, s, "heartbeat"); !st.Hidden {
		t.Fatalf("hidden flag missing: %+v", st)
	}
}

func TestSubMinuteTaskSavesOnFlipOrEveryFiveMinutes(t *testing.T) {
	tk := &task{every: 30 * time.Second}
	now := time.Now()
	if !shouldSave(tk, now, TriggerSchedule) {
		t.Fatal("first run of a process must be saved")
	}
	tk.saved, tk.savedOK, tk.lastSaved = true, true, now
	tk.st.LastStatus = StatusOK
	if shouldSave(tk, now.Add(time.Minute), TriggerSchedule) {
		t.Fatal("unchanged outcome saved again within five minutes")
	}
	if !shouldSave(tk, now.Add(6*time.Minute), TriggerSchedule) {
		t.Fatal("not saved after five minutes")
	}
	tk.st.LastStatus = StatusFailed
	if !shouldSave(tk, now.Add(time.Minute), TriggerSchedule) {
		t.Fatal("a flip to failed wasn't saved")
	}
	tk.st.LastStatus = StatusOK
	if !shouldSave(tk, now.Add(time.Minute), TriggerManual) {
		t.Fatal("a Run now wasn't saved")
	}
	tk.hidden = true
	if shouldSave(tk, now.Add(time.Hour), TriggerManual) {
		t.Fatal("a hidden task was saved")
	}
	hourly := &task{every: time.Hour, saved: true, savedOK: true, lastSaved: now}
	hourly.st.LastStatus = StatusOK
	if !shouldSave(hourly, now.Add(time.Second), TriggerSchedule) {
		t.Fatal("an hourly task must be saved every run")
	}
}

func TestOnFinishNotable(t *testing.T) {
	s := New(quietLogger())
	type fin struct {
		name    string
		notable bool
	}
	var mu sync.Mutex
	var got []fin
	s.OnFinish(func(st TaskStatus, notable bool) {
		mu.Lock()
		got = append(got, fin{st.Name, notable})
		mu.Unlock()
	})
	var fail atomic.Bool
	s.Register("fast", 30*time.Second, false, func(context.Context) error {
		if fail.Load() {
			return errors.New("x")
		}
		return nil
	})
	s.Register("hb", 30*time.Second, false, func(context.Context) error { return nil }, Hidden())
	started(t, s)
	t1 := s.find("fast")
	ctx := context.Background()
	s.exec(ctx, t1, TriggerSchedule) // first run: nothing to flip from, sub-minute → not notable
	s.exec(ctx, t1, TriggerSchedule) // ok → ok
	fail.Store(true)
	s.exec(ctx, t1, TriggerSchedule) // ok → failed: notable
	runOnce(t, s, "fast")            // manual: notable
	s.exec(ctx, s.find("hb"), TriggerSchedule)
	mu.Lock()
	defer mu.Unlock()
	want := []fin{{"fast", false}, {"fast", false}, {"fast", true}, {"fast", true}}
	if len(got) != len(want) {
		t.Fatalf("finishes = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("finishes = %+v, want %+v", got, want)
		}
	}
}

// fakeJobs runs each submitted run on a goroutine, single-flight by task name.
type fakeJobs struct {
	mu     sync.Mutex
	nextID int64
	active map[string]int64
	msgs   map[int64]string
	done   chan int64
}

func (f *fakeJobs) submit(name, trigger string, run func(ctx context.Context, jobID int64, setMessage func(string)) error) (int64, bool, error) {
	f.mu.Lock()
	if id, ok := f.active[name]; ok {
		f.mu.Unlock()
		return id, true, nil
	}
	f.nextID++
	id := f.nextID
	f.active[name] = id
	f.mu.Unlock()
	go func() {
		_ = run(context.Background(), id, func(m string) {
			f.mu.Lock()
			f.msgs[id] = m
			f.mu.Unlock()
		})
		f.mu.Lock()
		delete(f.active, name)
		f.mu.Unlock()
		f.done <- id
	}()
	return id, false, nil
}

func TestRunNowGoesThroughJobsAndNeverDoubleRuns(t *testing.T) {
	fj := &fakeJobs{active: map[string]int64{}, msgs: map[int64]string{}, done: make(chan int64, 4)}
	s := New(quietLogger())
	s.SetJobs(fj.submit)
	release := make(chan struct{})
	var runs int32
	s.Register("search-missing-movies", time.Hour, false, func(ctx context.Context) error {
		atomic.AddInt32(&runs, 1)
		<-release
		return nil
	})
	started(t, s)
	id, existing, err := s.RunNow("search-missing-movies", "")
	if err != nil || existing || id != 1 {
		t.Fatalf("RunNow = %d %v %v", id, existing, err)
	}
	waitFor(t, "the job to start", func() bool { return statusOf(t, s, "search-missing-movies").JobID == 1 })
	// A second press while it runs names the running job.
	if id2, _, err := s.RunNow("search-missing-movies", ""); !errors.Is(err, ErrBusy) || id2 != 1 {
		t.Fatalf("second RunNow = %d %v", id2, err)
	}
	// A tick that lands meanwhile is skipped.
	s.exec(context.Background(), s.find("search-missing-movies"), TriggerSchedule)
	close(release)
	<-fj.done
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("task ran %d times, want 1", got)
	}
	st := statusOf(t, s, "search-missing-movies")
	if st.Skipped != 1 || st.JobID != 0 || st.Runs != 1 {
		t.Fatalf("status = %+v", st)
	}
}

func TestQueuedRunNowStandsDownForATick(t *testing.T) {
	// A job that only starts once a ticker run has claimed the task must not run it again.
	s := New(quietLogger())
	release := make(chan struct{})
	var runs int32
	s.Register("t", time.Hour, false, func(ctx context.Context) error {
		atomic.AddInt32(&runs, 1)
		<-release
		return nil
	})
	var queued func(ctx context.Context, jobID int64, setMessage func(string)) error
	s.SetJobs(func(name, trigger string, run func(ctx context.Context, jobID int64, setMessage func(string)) error) (int64, bool, error) {
		queued = run
		return 9, false, nil
	})
	started(t, s)
	if _, _, err := s.RunNow("t", ""); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { s.exec(context.Background(), s.find("t"), TriggerSchedule); close(done) }()
	waitFor(t, "the tick to claim the task", func() bool { return statusOf(t, s, "t").Running })
	var msg string
	if err := queued(context.Background(), 9, func(m string) { msg = m }); err != nil || msg == "" {
		t.Fatalf("stand-down = %v %q", err, msg)
	}
	close(release)
	<-done
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("runs = %d, want 1", got)
	}
}

func TestSQLStoreRoundTrip(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ss := NewSQLStore(st.DB())
	ctx := context.Background()
	at := time.Now().Truncate(time.Millisecond)
	p := Persisted{Every: 15 * time.Minute, LastStart: at, LastEnd: at.Add(2 * time.Second), LastDuration: 2 * time.Second,
		LastStatus: StatusPanicked, LastError: "panic: nil map", LastErrorAt: at.Add(2 * time.Second), Runs: 3, Failures: 1, ConsecutiveFailures: 1}
	if err := ss.Save(ctx, "rss-sync", p); err != nil {
		t.Fatal(err)
	}
	p.Runs = 4
	if err := ss.Save(ctx, "rss-sync", p); err != nil { // upsert, not a second row
		t.Fatal(err)
	}
	got, err := ss.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g := got["rss-sync"]
	if len(got) != 1 || g.Runs != 4 || g.LastStatus != StatusPanicked || g.LastError != p.LastError ||
		!g.LastStart.Equal(at) || g.LastDuration != 2*time.Second || g.Every != 15*time.Minute || g.ConsecutiveFailures != 1 {
		t.Fatalf("loaded = %+v", got)
	}

	// End to end: a run is saved and a fresh scheduler (a restart) picks it up.
	s := New(quietLogger())
	s.SetStore(ss)
	s.Register("db-backup", time.Hour, false, func(context.Context) error { return errors.New("no space") })
	started(t, s)
	runOnce(t, s, "db-backup")
	s2 := New(quietLogger())
	s2.SetStore(ss)
	s2.Register("db-backup", time.Hour, false, func(context.Context) error { return nil })
	started(t, s2)
	if st := statusOf(t, s2, "db-backup"); st.Runs != 1 || st.LastError != "no space" || st.ConsecutiveFailures != 1 {
		t.Fatalf("after restart = %+v", st)
	}
}
