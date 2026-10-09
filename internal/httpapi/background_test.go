package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/safego"
)

func bgTestAPI(ctx context.Context) (*api, *safego.Group) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	g := safego.NewGroup(ctx, log)
	return &api{deps: Deps{Log: log, RunGroup: g}}, g
}

// Work a request hands off runs on the run context, so shutdown reaches it: with the run
// context already cancelled, fn sees a done context straight away.
func TestBgUsesRunContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a, g := bgTestAPI(ctx)

	got := make(chan error, 1)
	a.bg("manual movie search", idTarget("movie", 1), time.Hour, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			got <- ctx.Err()
		case <-time.After(2 * time.Second):
			got <- errors.New("ctx never ended")
		}
		return nil
	})
	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Fatalf("fn ctx err = %v, want canceled", err)
	}
	if left := g.Wait(time.Second); left != nil {
		t.Fatalf("still running: %v", left)
	}
}

// A panic in request-started work is contained and announced; the next piece of work runs
// as normal (before, it took the whole server down).
func TestBgContainsPanics(t *testing.T) {
	a, g := bgTestAPI(context.Background())
	hooked := make(chan string, 1)
	safego.SetPanicHook(func(name string) {
		select {
		case hooked <- name:
		default: // the hook must never block
		}
	})
	t.Cleanup(func() { safego.SetPanicHook(nil) })

	a.bg("manual movie search", idTarget("movie", 7), time.Minute, func(context.Context) error {
		var m map[string]int
		m["x"] = 1
		return nil
	})
	select {
	case name := <-hooked:
		if !strings.Contains(name, "manual movie search") || !strings.Contains(name, "movie 7") {
			t.Fatalf("panic hook name = %q", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("panic hook never called")
	}

	ran := make(chan struct{})
	a.bg("next", "", time.Minute, func(context.Context) error { close(ran); return nil })
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("work after a panic never ran")
	}
	if left := g.Wait(time.Second); left != nil {
		t.Fatalf("still running: %v", left)
	}
}

// The timeout bounds the work even when the run context lives on.
func TestBgAppliesTimeout(t *testing.T) {
	a, _ := bgTestAPI(context.Background())
	got := make(chan error, 1)
	a.bg("slow", "", 10*time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		got <- ctx.Err()
		return ctx.Err()
	})
	select {
	case err := <-got:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout never applied")
	}
}
