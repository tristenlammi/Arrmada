// Package scheduler runs Arrmada's recurring background jobs (session cleanup,
// RSS sync, library refresh, the nightly database backup and the like). M0 provides
// fixed-interval scheduling; cron expressions can layer on without changing callers.
package scheduler

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/safego"
)

// TaskFunc is a unit of scheduled work. Returning an error is logged, not fatal.
type TaskFunc func(ctx context.Context) error

type task struct {
	name       string
	every      time.Duration
	runAtStart bool
	fn         TaskFunc

	// Run history, guarded by Scheduler.mu.
	lastStart    time.Time
	lastDuration time.Duration
	lastErr      string
	runs         uint64
	failures     uint64
	running      bool
}

// TaskInfo is one task's schedule and how its runs have gone since startup.
type TaskInfo struct {
	Name         string        `json:"name"`
	Every        time.Duration `json:"every"`
	LastStart    time.Time     `json:"last_start"`
	LastDuration time.Duration `json:"last_duration"`
	LastErr      string        `json:"last_error"` // empty when the last run succeeded
	Runs         uint64        `json:"runs"`
	Failures     uint64        `json:"failures"` // errors and panics both count
	Running      bool          `json:"running"`
}

// Scheduler owns a set of recurring tasks and their goroutines.
type Scheduler struct {
	log     *slog.Logger
	mu      sync.Mutex
	tasks   []*task
	started bool
	ctx     context.Context // the Start context, for tasks registered late
	wg      sync.WaitGroup
}

// New creates an empty Scheduler.
func New(log *slog.Logger) *Scheduler { return &Scheduler{log: log} }

// Register adds a task. runAtStart runs it once immediately on Start, then every
// interval after. Registering AFTER Start launches the task immediately — Start
// used to snapshot the list, which silently never ran anything registered later:
// four real jobs (convert-sweep, convert-index, subtitles-auto-grab,
// recycle-enforce) sat dead behind that ordering hazard.
func (s *Scheduler) Register(name string, every time.Duration, runAtStart bool, fn TaskFunc) {
	t := &task{name: name, every: every, runAtStart: runAtStart, fn: fn}
	s.mu.Lock()
	s.tasks = append(s.tasks, t)
	started, ctx := s.started, s.ctx
	s.mu.Unlock()
	if started {
		s.wg.Add(1)
		go s.run(ctx, t)
	}
}

// Start launches each task in its own goroutine. Tasks stop when ctx is
// cancelled; call Wait afterwards to block until they've drained.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	s.started, s.ctx = true, ctx
	tasks := append([]*task(nil), s.tasks...)
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
	for _, ti := range s.Tasks() {
		if ti.Running {
			busy = append(busy, ti.Name)
		}
	}
	return busy
}

// Tasks reports every registered task's schedule and run history, sorted by name.
func (s *Scheduler) Tasks() []TaskInfo {
	s.mu.Lock()
	out := make([]TaskInfo, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, TaskInfo{
			Name: t.name, Every: t.every, LastStart: t.lastStart, LastDuration: t.lastDuration,
			LastErr: t.lastErr, Runs: t.runs, Failures: t.failures, Running: t.running,
		})
	}
	s.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Scheduler) run(ctx context.Context, t *task) {
	defer s.wg.Done()

	if t.runAtStart {
		s.exec(ctx, t)
	}

	ticker := time.NewTicker(t.every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.exec(ctx, t)
		}
	}
}

// exec runs one tick of a task. A panic is caught here (safego logs it with its stack)
// and counted as a failed run, so the ticker keeps going: before, one nil dereference in
// any sweep took the whole app down with it.
func (s *Scheduler) exec(ctx context.Context, t *task) {
	start := time.Now()
	s.mu.Lock()
	t.running, t.lastStart = true, start
	s.mu.Unlock()

	err := safego.Call(s.log, "scheduled task "+t.name, func() error { return t.fn(ctx) })
	dur := time.Since(start)

	s.mu.Lock()
	t.running, t.lastDuration = false, dur
	t.runs++
	t.lastErr = ""
	if err != nil {
		t.failures++
		t.lastErr = err.Error()
	}
	s.mu.Unlock()

	if err != nil {
		s.log.Error("scheduled task failed", "task", t.name, "err", err, "dur_ms", dur.Milliseconds())
		return
	}
	s.log.Debug("scheduled task ran", "task", t.name, "dur_ms", dur.Milliseconds())
}
