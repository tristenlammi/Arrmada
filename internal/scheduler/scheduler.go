// Package scheduler runs Arrmada's recurring background jobs (session cleanup
// now; RSS sync, library refresh, backups later). M0 provides fixed-interval
// scheduling; cron expressions can layer on without changing callers.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// TaskFunc is a unit of scheduled work. Returning an error is logged, not fatal.
type TaskFunc func(ctx context.Context) error

type task struct {
	name       string
	every      time.Duration
	runAtStart bool
	fn         TaskFunc
}

// Scheduler owns a set of recurring tasks and their goroutines.
type Scheduler struct {
	log     *slog.Logger
	mu      sync.Mutex
	tasks   []task
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
	t := task{name: name, every: every, runAtStart: runAtStart, fn: fn}
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
	tasks := append([]task(nil), s.tasks...)
	s.mu.Unlock()

	for _, t := range tasks {
		s.wg.Add(1)
		go s.run(ctx, t)
	}
	s.log.Info("scheduler started", "tasks", len(tasks))
}

// Wait blocks until every task goroutine has exited (after ctx cancellation).
func (s *Scheduler) Wait() { s.wg.Wait() }

func (s *Scheduler) run(ctx context.Context, t task) {
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

func (s *Scheduler) exec(ctx context.Context, t task) {
	start := time.Now()
	if err := t.fn(ctx); err != nil {
		s.log.Error("scheduled task failed", "task", t.name, "err", err, "dur_ms", time.Since(start).Milliseconds())
		return
	}
	s.log.Debug("scheduled task ran", "task", t.name, "dur_ms", time.Since(start).Milliseconds())
}
