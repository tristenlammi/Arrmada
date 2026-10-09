package safego

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fastBackoff shrinks Loop's restart pauses for the duration of a test.
func fastBackoff(t *testing.T) {
	t.Helper()
	minB, maxB := loopBackoffMin, loopBackoffMax
	loopBackoffMin, loopBackoffMax = time.Millisecond, 4*time.Millisecond
	t.Cleanup(func() { loopBackoffMin, loopBackoffMax = minB, maxB })
}

func TestCallTurnsPanicIntoError(t *testing.T) {
	before := Panics()
	var hooked atomic.Value
	SetPanicHook(func(name string) { hooked.Store(name) })
	t.Cleanup(func() { SetPanicHook(nil) })

	err := Call(quietLog(), "sweep", func() error { panic("boom") })
	var pe *PanicError
	if !errors.As(err, &pe) {
		t.Fatalf("want *PanicError, got %v", err)
	}
	if pe.Name != "sweep" || pe.Value != "boom" {
		t.Fatalf("panic error = %+v", pe)
	}
	if Panics() != before+1 {
		t.Fatalf("panic counter = %d, want %d", Panics(), before+1)
	}
	if got, _ := hooked.Load().(string); got != "sweep" {
		t.Fatalf("hook got %q", got)
	}
}

func TestCallPassesErrorsThrough(t *testing.T) {
	want := errors.New("plain")
	if err := Call(quietLog(), "x", func() error { return want }); err != want {
		t.Fatalf("got %v", err)
	}
	if err := Call(quietLog(), "x", func() error { return nil }); err != nil {
		t.Fatalf("got %v", err)
	}
}

func TestPanickingHookIsContained(t *testing.T) {
	SetPanicHook(func(string) { panic("hook") })
	t.Cleanup(func() { SetPanicHook(nil) })
	if err := Call(quietLog(), "x", func() error { panic("boom") }); err == nil {
		t.Fatal("want an error")
	}
}

func TestGoRecovers(t *testing.T) {
	done := make(chan struct{})
	Go(quietLog(), "fire", func() {
		defer close(done)
		panic("boom")
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine never ran")
	}
}

func TestLoopRestartsAfterPanicAndStopsOnNormalReturn(t *testing.T) {
	fastBackoff(t)
	var runs atomic.Int32
	Loop(context.Background(), quietLog(), "loop", func(context.Context) {
		if runs.Add(1) < 3 {
			panic("again")
		}
	})
	if runs.Load() != 3 {
		t.Fatalf("runs = %d, want 3 (two panics, then a normal return)", runs.Load())
	}
}

func TestLoopStopsOnCancel(t *testing.T) {
	fastBackoff(t)
	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		Loop(ctx, quietLog(), "loop", func(context.Context) {
			if runs.Add(1) == 5 {
				cancel()
			}
			panic("always")
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop didn't stop after cancel")
	}
	if runs.Load() != 5 {
		t.Fatalf("runs = %d, want 5", runs.Load())
	}
}

func TestGroupWaitNamesGoroutinesThatIgnoreCtx(t *testing.T) {
	fastBackoff(t)
	ctx, cancel := context.WithCancel(context.Background())
	g := NewGroup(ctx, quietLog())

	release := make(chan struct{})
	defer close(release)
	g.Loop("polite", func(ctx context.Context) { <-ctx.Done() })
	g.Go("stubborn", func(context.Context) { <-release })
	panicked := make(chan struct{})
	var once sync.Once
	g.Loop("crashy", func(ctx context.Context) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		once.Do(func() { close(panicked) })
		panic("keeps panicking until cancelled")
	})
	<-panicked

	cancel()
	left := g.Wait(200 * time.Millisecond)
	if !reflect.DeepEqual(left, []string{"stubborn"}) {
		t.Fatalf("still running = %v, want [stubborn]", left)
	}
}

func TestGroupWaitReturnsNilWhenDrained(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	g := NewGroup(ctx, quietLog())
	for i := 0; i < 10; i++ {
		g.Go("worker", func(ctx context.Context) { <-ctx.Done() })
	}
	cancel()
	if left := g.Wait(2 * time.Second); left != nil {
		t.Fatalf("still running = %v", left)
	}
	// Reusable: a later wave is waited for as well.
	g.Go("late", func(context.Context) {})
	if left := g.Wait(2 * time.Second); left != nil {
		t.Fatalf("still running = %v", left)
	}
}

func TestGroupGoGetsGroupContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := NewGroup(ctx, quietLog())
	got := make(chan error, 1)
	g.Go("x", func(ctx context.Context) { got <- ctx.Err() })
	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx err = %v", err)
	}
}
