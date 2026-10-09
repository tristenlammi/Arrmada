package scheduler

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestTaskRunsOnInterval(t *testing.T) {
	s := New(quietLogger())
	var runs int32
	s.Register("tick", 10*time.Millisecond, false, func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	time.Sleep(55 * time.Millisecond)
	cancel()
	s.Wait()

	if got := atomic.LoadInt32(&runs); got < 3 {
		t.Fatalf("expected at least 3 runs, got %d", got)
	}
}

func TestRunAtStartFiresImmediately(t *testing.T) {
	s := New(quietLogger())
	var runs int32
	s.Register("boot", time.Hour, true, func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	time.Sleep(20 * time.Millisecond)
	cancel()
	s.Wait()

	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("expected exactly 1 run-at-start, got %d", got)
	}
}

func TestStopsOnContextCancel(t *testing.T) {
	s := New(quietLogger())
	var runs int32
	s.Register("tick", 5*time.Millisecond, false, func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	time.Sleep(25 * time.Millisecond)
	cancel()
	s.Wait()

	before := atomic.LoadInt32(&runs)
	time.Sleep(25 * time.Millisecond)
	if after := atomic.LoadInt32(&runs); after != before {
		t.Fatalf("task kept running after cancel: %d -> %d", before, after)
	}
}

// TestRegisterAfterStartRuns pins the late-registration fix: Start used to
// snapshot the task list, so anything registered afterwards silently never ran —
// which is exactly what happened to four real jobs in main.go (convert-sweep,
// convert-index, subtitles-auto-grab, recycle-enforce).
func TestRegisterAfterStartRuns(t *testing.T) {
	s := New(quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	var runs int32
	s.Register("late", 5*time.Millisecond, true, func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		return nil
	})
	time.Sleep(30 * time.Millisecond)
	if atomic.LoadInt32(&runs) == 0 {
		t.Fatal("a task registered after Start never ran")
	}
	cancel()
	s.Wait()
}

// A task that panics must not take the app down with it: the panic is caught, counted
// as a failure, and the next tick runs as normal.
func TestPanickingTaskRunsAgainNextTick(t *testing.T) {
	s := New(quietLogger())
	var runs int32
	s.Register("crashy", 5*time.Millisecond, true, func(context.Context) error {
		if atomic.AddInt32(&runs, 1) == 1 {
			var m map[string]int
			m["boom"] = 1 // nil map write: a real runtime panic
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&runs) < 3 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	s.Wait()

	if got := atomic.LoadInt32(&runs); got < 3 {
		t.Fatalf("task stopped after panicking: %d runs", got)
	}
	infos := s.Snapshot()
	if len(infos) != 1 {
		t.Fatalf("tasks = %+v", infos)
	}
	ti := infos[0]
	if ti.Failures != 1 || ti.Runs < 3 || ti.LastError != "" || ti.Running || ti.ConsecutiveFailures != 0 {
		t.Fatalf("task info = %+v, want 1 failure, >=3 runs, last run clean", ti)
	}
}

func TestWaitForNamesTasksStillRunning(t *testing.T) {
	s := New(quietLogger())
	release := make(chan struct{})
	started := make(chan struct{})
	s.Register("stuck", time.Hour, true, func(context.Context) error {
		close(started)
		<-release // ignores ctx on purpose
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	<-started
	cancel()
	if left := s.WaitFor(20 * time.Millisecond); len(left) != 1 || left[0] != "stuck" {
		t.Fatalf("WaitFor = %v, want [stuck]", left)
	}
	close(release)
	if left := s.WaitFor(2 * time.Second); left != nil {
		t.Fatalf("WaitFor after release = %v", left)
	}
}
