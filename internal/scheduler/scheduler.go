// Package scheduler runs Arrmada's recurring background jobs (session cleanup,
// RSS sync, library refresh, the nightly database backup and the like). M0 provides
// fixed-interval scheduling; cron expressions can layer on without changing callers.
//
// Every task keeps a record of how its runs went — last start, duration, result, error,
// failure streak, next run — which the System → Status page reads and which survives a
// restart through a Store. A task can also be started on demand (RunNow) without ever
// running twice at once: a tick that finds the task still busy is skipped, never queued.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tristenlammi/arrmada/internal/safego"
)

// TaskFunc is a unit of scheduled work. Returning an error is logged, not fatal.
type TaskFunc func(ctx context.Context) error

// ErrUnknownTask is RunNow asked for a task nobody registered.
var ErrUnknownTask = errors.New("unknown task")

// ErrBusy is RunNow finding the task already running.
var ErrBusy = errors.New("already running")

// Triggers say what started a run.
const (
	TriggerSchedule = "schedule" // the ticker (or run-at-start)
	TriggerManual   = "manual"   // Run now
)

// Option adjusts a task at registration.
type Option func(*task)

// Label is the plain-language name shown on the Tasks page ("Check indexer feeds for new movies").
func Label(s string) Option { return func(t *task) { t.label = s } }

// Description is the one-line explanation under the label.
func Description(s string) Option { return func(t *task) { t.description = s } }

// Hidden keeps a task off the Tasks page and out of the database: for plumbing like the
// websocket heartbeat, whose runs nobody needs to see and which would only add writes.
func Hidden() Option { return func(t *task) { t.hidden = true } }

type task struct {
	name, label, description string
	every                    time.Duration
	runAtStart               bool
	hidden                   bool
	fn                       TaskFunc

	// running is the one-copy-at-a-time guard. A ticker fire or a Run now that finds it
	// set is skipped; it's an atomic so the check and the claim are one step.
	running atomic.Bool

	mu        sync.Mutex // guards everything below
	st        state
	lastSaved time.Time // when st was last written to the store
	savedOK   bool      // the outcome that was last written, to spot a flip
	saved     bool      // st has been written at least once this process
}

// Scheduler owns a set of recurring tasks and their goroutines.
type Scheduler struct {
	log *slog.Logger

	mu        sync.Mutex
	tasks     []*task
	started   bool
	ctx       context.Context // the Start context, for tasks registered late
	store     Store
	persisted map[string]Persisted // loaded at Start, applied to late registrations too
	onFinish  []func(TaskStatus, bool)
	jobs      JobSubmitter

	wg  sync.WaitGroup
	now func() time.Time // tests may pin the clock; nil = time.Now
}

// JobSubmitter hands a Run now to the job runner, so a manual run leaves a jobs row with
// its outcome and gets the runner's shutdown handling. run is called at most once; when
// the runner already has a job for this task it returns that job's id and existing=true
// without calling run.
type JobSubmitter func(name, trigger string, run func(ctx context.Context, jobID int64, setMessage func(string)) error) (jobID int64, existing bool, err error)

// New creates an empty Scheduler.
func New(log *slog.Logger) *Scheduler { return &Scheduler{log: log} }

func (s *Scheduler) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// SetStore makes task history persistent. Call before Start; the store is read once at
// Start and written after runs (see shouldSave).
func (s *Scheduler) SetStore(st Store) {
	s.mu.Lock()
	s.store = st
	s.mu.Unlock()
}

// SetJobs routes Run now through the job runner. Without it a Run now still runs, just on
// a goroutine of the scheduler's own with no jobs row.
func (s *Scheduler) SetJobs(sub JobSubmitter) {
	s.mu.Lock()
	s.jobs = sub
	s.mu.Unlock()
}

// OnFinish registers a hook called after every run of a visible task. notable is true when
// the run is worth announcing: the task runs a minute or less often, the outcome flipped,
// or someone pressed Run now — so a 30-second import sweep doesn't announce itself twice a
// minute forever. Hooks run on the task's goroutine and must not block.
func (s *Scheduler) OnFinish(fn func(st TaskStatus, notable bool)) {
	s.mu.Lock()
	s.onFinish = append(s.onFinish, fn)
	s.mu.Unlock()
}

// Register adds a task. runAtStart runs it once immediately on Start, then every
// interval after. Registering AFTER Start launches the task immediately — Start
// used to snapshot the list, which silently never ran anything registered later:
// four real jobs (convert-sweep, convert-index, subtitles-auto-grab,
// recycle-enforce) sat dead behind that ordering hazard.
func (s *Scheduler) Register(name string, every time.Duration, runAtStart bool, fn TaskFunc, opts ...Option) {
	t := &task{name: name, every: every, runAtStart: runAtStart, fn: fn}
	for _, o := range opts {
		o(t)
	}
	if t.label == "" {
		t.label = name
	}
	s.mu.Lock()
	s.tasks = append(s.tasks, t)
	started, ctx := s.started, s.ctx
	if p, ok := s.persisted[name]; ok && !t.hidden {
		t.restore(p)
	}
	s.mu.Unlock()
	if started {
		s.wg.Add(1)
		go s.run(ctx, t)
	}
}

// Start loads saved task history, then launches each task in its own goroutine. Tasks
// stop when ctx is cancelled; call Wait afterwards to block until they've drained.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	st := s.store
	s.mu.Unlock()
	var loaded map[string]Persisted
	if st != nil {
		var err error
		if loaded, err = st.Load(ctx); err != nil {
			// History is for display; a task that can't show its last run still has to run.
			s.log.Warn("scheduler: couldn't load saved task history — starting without it", "err", err)
		}
	}

	s.mu.Lock()
	s.started, s.ctx, s.persisted = true, ctx, loaded
	tasks := append([]*task(nil), s.tasks...)
	for _, t := range tasks {
		if p, ok := loaded[t.name]; ok && !t.hidden {
			t.restore(p)
		}
	}
	s.mu.Unlock()

	for _, t := range tasks {
		s.wg.Add(1)
		go s.run(ctx, t)
	}
	s.log.Info("scheduler started", "tasks", len(tasks))
}

// Wait blocks until every task goroutine has exited (after ctx cancellation).
func (s *Scheduler) Wait() { s.wg.Wait() }

// WaitFor is Wait with a limit, for shutdown: Docker kills the container ten seconds
// after asking it to stop, so a task that ignores cancellation mustn't hold the exit
// hostage. It returns the names of tasks still mid-run when time ran out (nil when every
// task goroutine exited).
func (s *Scheduler) WaitFor(timeout time.Duration) []string {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return nil
	case <-t.C:
	}
	var busy []string
	for _, ti := range s.Snapshot() {
		if ti.Running {
			busy = append(busy, ti.Name)
		}
	}
	return busy
}

// Snapshot reports every registered task's schedule and run history, sorted by label.
// Hidden tasks are included (marked Hidden); the API leaves them out.
func (s *Scheduler) Snapshot() []TaskStatus {
	s.mu.Lock()
	tasks := append([]*task(nil), s.tasks...)
	s.mu.Unlock()
	out := make([]TaskStatus, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.status())
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Status is one task's Snapshot entry.
func (s *Scheduler) Status(name string) (TaskStatus, bool) {
	t := s.find(name)
	if t == nil {
		return TaskStatus{}, false
	}
	return t.status(), true
}

func (s *Scheduler) find(name string) *task {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.name == name {
			return t
		}
	}
	return nil
}

// RunNow starts a task immediately, outside its schedule, without disturbing the
// cadence. It returns ErrUnknownTask, or ErrBusy (with the running Run now's job id, if
// there is one) when the task is already running. With a job runner set, the run is a
// job and its id is returned; existing=true means the runner already had one going.
func (s *Scheduler) RunNow(name, trigger string) (jobID int64, existing bool, err error) {
	t := s.find(name)
	if t == nil {
		return 0, false, ErrUnknownTask
	}
	if trigger == "" {
		trigger = TriggerManual
	}
	if t.running.Load() {
		t.mu.Lock()
		id := t.st.JobID
		t.mu.Unlock()
		return id, true, ErrBusy
	}
	s.mu.Lock()
	sub, ctx, started := s.jobs, s.ctx, s.started
	s.mu.Unlock()

	if sub != nil {
		return sub(name, trigger, func(ctx context.Context, jobID int64, setMessage func(string)) error {
			// Claimed inside the job, not before it: a job that never starts (cancelled
			// while queued, or the app shutting down) must not leave the task marked
			// running for good. A tick that got in first wins and this run stands down.
			if !t.running.CompareAndSwap(false, true) {
				setMessage("Already running — this run was skipped")
				return nil
			}
			t.mu.Lock()
			t.st.JobID = jobID
			t.mu.Unlock()
			return s.execClaimed(ctx, t, trigger)
		})
	}

	// No runner (tests, tools): claim now and run on a goroutine of our own.
	if !t.running.CompareAndSwap(false, true) {
		return 0, true, ErrBusy
	}
	if !started || ctx == nil {
		ctx = context.Background()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		_ = s.execClaimed(ctx, t, trigger)
	}()
	return 0, false, nil
}

func (s *Scheduler) run(ctx context.Context, t *task) {
	defer s.wg.Done()

	if t.runAtStart {
		s.exec(ctx, t, TriggerSchedule)
	}

	ticker := time.NewTicker(t.every)
	defer ticker.Stop()
	t.setNext(s.clock().Add(t.every))
	for {
		select {
		case <-ctx.Done():
			t.setNext(time.Time{})
			return
		case <-ticker.C:
			// The next fire is a fixed interval after this one whatever the run takes —
			// that's how a Ticker behaves (a slow run drops the fires it overlaps).
			t.setNext(s.clock().Add(t.every))
			s.exec(ctx, t, TriggerSchedule)
		}
	}
}

// exec runs one scheduled tick of a task, unless the task is already running (a Run now
// still going), in which case the tick is skipped and counted, never queued.
func (s *Scheduler) exec(ctx context.Context, t *task, trigger string) {
	if !t.running.CompareAndSwap(false, true) {
		t.mu.Lock()
		t.st.Skipped++
		t.mu.Unlock()
		s.log.Debug("scheduled task skipped — still running from before", "task", t.name)
		return
	}
	_ = s.execClaimed(ctx, t, trigger)
}

// execClaimed runs a task the caller has already marked running, records the outcome and
// releases it. A panic is caught here (safego logs it with its stack) and recorded as a
// failed run with "panic: …", so the ticker keeps going: before, one nil dereference in
// any sweep took the whole app down with it.
func (s *Scheduler) execClaimed(ctx context.Context, t *task, trigger string) error {
	defer t.running.Store(false)

	s.mu.Lock()
	hasStore := s.store != nil
	s.mu.Unlock()
	start := s.clock()
	t.mu.Lock()
	t.st.LastStart = start
	t.mu.Unlock()

	err := safego.Call(s.log, "scheduled task "+t.name, func() error { return t.fn(ctx) })
	end := s.clock()
	dur := end.Sub(start)

	status, msg := StatusOK, ""
	var pe *safego.PanicError
	switch {
	case errors.As(err, &pe):
		status, msg = StatusPanicked, "panic: "+fmt.Sprint(pe.Value)
	case err != nil:
		status, msg = StatusFailed, err.Error()
	}

	t.mu.Lock()
	prevOK, hadRun := t.st.LastStatus == StatusOK, t.st.LastStatus != ""
	t.st.LastEnd, t.st.LastDuration, t.st.LastStatus, t.st.LastTrigger = end, dur, status, trigger
	t.st.Runs++
	t.st.JobID = 0
	if err != nil {
		t.st.Failures++
		t.st.ConsecutiveFailures++
		t.st.LastError, t.st.LastErrorAt = msg, end
	} else {
		t.st.ConsecutiveFailures = 0
		t.st.LastError = "" // the error stays visible only while it's the latest result
	}
	flipped := hadRun && prevOK != (err == nil)
	save := hasStore && shouldSave(t, end, trigger)
	var p Persisted
	if save {
		p = t.persisted()
		t.lastSaved, t.savedOK, t.saved = end, err == nil, true
	}
	t.mu.Unlock()

	if err != nil {
		s.log.Error("scheduled task failed", "task", t.name, "err", msg, "dur_ms", dur.Milliseconds())
	} else {
		s.log.Debug("scheduled task ran", "task", t.name, "dur_ms", dur.Milliseconds())
	}
	if save {
		s.save(ctx, t.name, p)
	}
	if !t.hidden {
		st := t.status()
		notable := t.every >= time.Minute || flipped || trigger != TriggerSchedule
		s.mu.Lock()
		hooks := append([](func(TaskStatus, bool)){}, s.onFinish...)
		s.mu.Unlock()
		for _, h := range hooks {
			h(st, notable)
		}
	}
	return err
}

// shouldSave decides whether this run's record is written. Hidden tasks never are. Tasks
// that run a minute or less apart (the 30-second import sweeps) would otherwise write
// thousands of rows' worth a day for nothing, so they write when the outcome flips, on a
// Run now, and at most every five minutes otherwise. Called with t.mu held.
func shouldSave(t *task, now time.Time, trigger string) bool {
	if t.hidden {
		return false
	}
	if t.every >= time.Minute || trigger != TriggerSchedule || !t.saved {
		return true
	}
	ok := t.st.LastStatus == StatusOK
	return ok != t.savedOK || now.Sub(t.lastSaved) >= persistEvery
}

// persistEvery is the longest a sub-minute task's saved record may lag behind.
const persistEvery = 5 * time.Minute

func (s *Scheduler) save(ctx context.Context, name string, p Persisted) {
	s.mu.Lock()
	st := s.store
	s.mu.Unlock()
	if st == nil {
		return
	}
	// Written even when the run was cut short by shutdown, so the last run is on record.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := st.Save(wctx, name, p); err != nil {
		s.log.Warn("scheduler: couldn't save task history", "task", name, "err", err)
	}
}
