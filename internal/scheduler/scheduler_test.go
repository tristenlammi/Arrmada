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
