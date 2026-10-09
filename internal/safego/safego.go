// Package safego runs background work so that a panic in it is logged and contained
// instead of taking the whole process down.
//
// Without it, one nil dereference in a sweep, a bus loop or a request-started search
// restarts the app mid-encode and mid-stream: Go only lets a recover() on the panicking
// goroutine's own stack catch it, and the HTTP middleware's recover never sees work the
// handler handed to another goroutine.
//
//   - Go runs a fire-and-forget function on its own goroutine.
//   - Call runs a function in place and turns a panic into an error, for code that
//     already has a failure path (a fan-out that reports per-indexer errors, a job
//     worker that marks one job failed and moves on).
//   - Loop runs a long-lived loop and starts it again after a panic, backing off.
//   - Group does the same for a set of named goroutines tied to one context, and at
//     shutdown reports which of them never stopped.
//
// Every recovered panic is logged at Error with its stack, counted, and handed to the
// panic hook (main publishes it on the bus as system.panic).
package safego

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// PanicError is what Call returns when its function panicked.
type PanicError struct {
	Name  string // what was running
	Value any    // the value passed to panic
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("%s: panic: %v", e.Name, e.Value)
}

var (
	panics atomic.Uint64
	hook   atomic.Pointer[func(name string)]
)

// SetPanicHook registers a function called (synchronously, on the panicking goroutine,
// after the stack is logged) for every recovered panic. nil removes it. It must not block.
func SetPanicHook(fn func(name string)) {
	if fn == nil {
		hook.Store(nil)
		return
	}
	hook.Store(&fn)
}

// Panics is how many panics have been recovered since the process started.
func Panics() uint64 { return panics.Load() }

// report logs and counts one recovered panic. Kept separate from the deferred recover
// so the hook can't turn a contained panic back into a crash: a hook that panics itself
// is logged and ignored.
func report(log *slog.Logger, name string, v any) {
	if log == nil {
		log = slog.Default()
	}
	panics.Add(1)
	log.Error("recovered from a panic in background work — it was stopped, the app keeps running",
		"task", name, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
	if fn := hook.Load(); fn != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("panic hook itself panicked", "task", name, "panic", fmt.Sprint(r))
				}
			}()
			(*fn)(name)
		}()
	}
}

// Go runs fn on a new goroutine. A panic is logged with its stack and goes no further.
func Go(log *slog.Logger, name string, fn func()) {
	go func() {
		_ = Call(log, name, func() error { fn(); return nil })
	}()
}

// Call runs fn on the calling goroutine and returns its error. A panic is logged with its
// stack and returned as a *PanicError naming what was running.
func Call(log *slog.Logger, name string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			report(log, name, r)
			err = &PanicError{Name: name, Value: r}
		}
	}()
	return fn()
}

// Backoff bounds for Loop's restarts: a loop that panics every time it starts settles at
// one attempt (and one Error line) a minute instead of spinning.
var (
	loopBackoffMin = time.Second
	loopBackoffMax = time.Minute
)

// Loop runs fn until it returns normally or ctx is done. When fn panics it is started
// again after a pause that doubles from a second up to a minute. fn is expected to watch
// ctx itself; Loop only checks it between restarts.
func Loop(ctx context.Context, log *slog.Logger, name string, fn func(ctx context.Context)) {
	wait := loopBackoffMin
	for {
		err := Call(log, name, func() error { fn(ctx); return nil })
		if err == nil || ctx.Err() != nil {
			return
		}
		if log == nil {
			log = slog.Default()
		}
		log.Warn("restarting background loop after a panic", "task", name, "in", wait.String())
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		wait *= 2
		if wait > loopBackoffMax {
			wait = loopBackoffMax
		}
	}
}

// Group is a set of named goroutines that share one context — in the app, the run
// context cancelled at shutdown. Wait reports which of them are still going.
type Group struct {
	ctx context.Context
	log *slog.Logger

	mu      sync.Mutex
	next    uint64
	running map[uint64]string
	idle    chan struct{} // closed when running drains to zero; nil when nobody waits
}

// NewGroup makes a Group whose goroutines all receive ctx.
func NewGroup(ctx context.Context, log *slog.Logger) *Group {
	return &Group{ctx: ctx, log: log, running: map[uint64]string{}}
}

// Context is the context the group's goroutines receive.
func (g *Group) Context() context.Context { return g.ctx }

// Go runs fn(ctx) once on a new goroutine. A panic ends it, logged; it isn't restarted.
func (g *Group) Go(name string, fn func(ctx context.Context)) {
	id := g.enter(name)
	go func() {
		defer g.leave(id)
		_ = Call(g.log, name, func() error { fn(g.ctx); return nil })
	}()
}

// Loop runs fn(ctx) on a new goroutine under Loop: restarted after a panic, finished when
// it returns normally or the group's context is done.
func (g *Group) Loop(name string, fn func(ctx context.Context)) {
	id := g.enter(name)
	go func() {
		defer g.leave(id)
		Loop(g.ctx, g.log, name, fn)
	}()
}

func (g *Group) enter(name string) uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	g.running[g.next] = name
	return g.next
}

func (g *Group) leave(id uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.running, id)
	if len(g.running) == 0 && g.idle != nil {
		close(g.idle)
		g.idle = nil
	}
}

// Running lists the names of the goroutines still going, sorted, one entry per goroutine.
func (g *Group) Running() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.namesLocked()
}

func (g *Group) namesLocked() []string {
	names := make([]string, 0, len(g.running))
	for _, n := range g.running {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Wait blocks until every goroutine in the group has returned or timeout passes, and
// returns the names of those still running (nil when all finished). Cancel the group's
// context first; Wait doesn't. Goroutines started while Wait runs are waited for too.
func (g *Group) Wait(timeout time.Duration) []string {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		g.mu.Lock()
		if len(g.running) == 0 {
			g.mu.Unlock()
			return nil
		}
		if g.idle == nil {
			g.idle = make(chan struct{})
		}
		idle := g.idle
		g.mu.Unlock()

		select {
		case <-idle:
			// Drained — loop round to confirm nothing started since.
		case <-deadline.C:
			return g.Running()
		}
	}
}
