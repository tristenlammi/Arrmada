package health

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a settable now() for the registry.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func testRegistry(pub Publisher) (*Registry, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	r := NewRegistry(pub, nil)
	r.now = clk.now
	return r, clk
}

// counting is a check that counts its runs and returns whatever findings are set.
type counting struct {
	runs     atomic.Int32
	mu       sync.Mutex
	findings []Finding
}

func (c *counting) set(f ...Finding) { c.mu.Lock(); c.findings = f; c.mu.Unlock() }
func (c *counting) run(context.Context) []Finding {
	c.runs.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.findings
}

// The driving task ticks every 30s; a 60s check runs on the first tick, skips the next,
// and runs again a minute later.
func TestRegistryRunsDueChecks(t *testing.T) {
	r, clk := testRegistry(nil)
	c := &counting{}
	r.Register(Check{Key: "c", Name: "C", Interval: time.Minute, Run: c.run})
	ctx := context.Background()

	_ = r.RunDue(ctx)
	clk.add(30 * time.Second)
	_ = r.RunDue(ctx)
	if n := c.runs.Load(); n != 1 {
		t.Fatalf("after two ticks 30s apart: %d runs, want 1", n)
	}
	clk.add(30 * time.Second)
	_ = r.RunDue(ctx)
	if n := c.runs.Load(); n != 2 {
		t.Fatalf("a minute after the first run: %d runs, want 2", n)
	}
}

// A check that hangs is reported as timed out, keeps its previous findings (stale), and
// doesn't hold up the others.
func TestRegistryTimeout(t *testing.T) {
	r, _ := testRegistry(nil)
	release := make(chan struct{})
	defer close(release)
	var hang atomic.Bool
	r.Register(Check{Key: "slow", Name: "Plex", Timeout: 50 * time.Millisecond, Run: func(ctx context.Context) []Finding {
		if hang.Load() {
			<-release // ignores ctx on purpose: the worst case
		}
		return []Finding{{Key: "slow.old", Level: LevelWarning, Message: "from before"}}
	}})
	fast := &counting{}
	fast.set(Finding{Key: "fast.bad", Level: LevelError, Message: "fast says no"})
	r.Register(Check{Key: "fast", Name: "Fast", Run: fast.run})

	r.RunAll(context.Background())
	hang.Store(true)
	start := time.Now()
	r.RunAll(context.Background())
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("a hung check held RunAll for %v", took)
	}

	rep := r.Results()
	keys := map[string]Warning{}
	for _, w := range rep.Warnings {
		keys[w.Key] = w
	}
	if _, ok := keys["fast.bad"]; !ok {
		t.Errorf("the fast check's finding is missing: %+v", rep.Warnings)
	}
	if _, ok := keys["slow.old"]; !ok {
		t.Errorf("the slow check lost its previous findings: %+v", rep.Warnings)
	}
	if w, ok := keys["slow.timeout"]; !ok || !strings.Contains(w.Message, "Plex check timed out") {
		t.Errorf("no timed-out warning: %+v", rep.Warnings)
	}
	for _, c := range rep.Checks {
		if c.Key == "slow" && !c.Stale {
			t.Errorf("slow check not marked stale: %+v", c)
		}
	}
	if fast.runs.Load() != 2 {
		t.Errorf("fast check ran %d times, want 2", fast.runs.Load())
	}
	// Still in flight: another RunAll doesn't start a second copy.
	r.RunAll(context.Background())
}

type fakeBus struct {
	mu     sync.Mutex
	events []map[string]any
}

func (b *fakeBus) Publish(topic string, data any) {
	if topic != TopicChanged {
		return
	}
	b.mu.Lock()
	b.events = append(b.events, data.(map[string]any))
	b.mu.Unlock()
}

func (b *fakeBus) count() int { b.mu.Lock(); defer b.mu.Unlock(); return len(b.events) }

// health.changed goes out when the set of problems changes, not on every run, and carries
// counts only — never the message text.
func TestRegistryPublishesOnChangeOnly(t *testing.T) {
	bus := &fakeBus{}
	r, _ := testRegistry(bus)
	c := &counting{}
	r.Register(Check{Key: "c", Name: "C", Run: c.run})
	ctx := context.Background()

	r.RunAll(ctx) // nothing wrong, nothing changed
	if bus.count() != 0 {
		t.Fatalf("published with no problems: %v", bus.events)
	}
	c.set(Finding{Key: "c.bad", Level: LevelError, Message: "/secret/path is gone"})
	r.RunAll(ctx)
	r.RunAll(ctx)
	if bus.count() != 1 {
		t.Fatalf("published %d times for one change", bus.count())
	}
	ev := bus.events[0]
	if ev["status"] != "error" || ev["errors"] != 1 || ev["warnings"] != 0 {
		t.Errorf("payload %v", ev)
	}
	for k, v := range ev {
		if s, ok := v.(string); ok && strings.Contains(s, "/secret") {
			t.Errorf("payload field %s carries the message", k)
		}
	}
	c.set()
	r.RunAll(ctx)
	if bus.count() != 2 || bus.events[1]["status"] != "ok" {
		t.Errorf("clearing wasn't announced: %v", bus.events)
	}
}

// since is when a problem was first seen and survives re-runs; it resets once cleared.
func TestRegistrySince(t *testing.T) {
	r, clk := testRegistry(nil)
	c := &counting{}
	c.set(Finding{Key: "c.bad", Level: LevelWarning, Message: "bad"})
	r.Register(Check{Key: "c", Name: "C", Run: c.run})
	ctx := context.Background()
	r.RunAll(ctx)
	first := r.Warnings()[0].Since
	clk.add(5 * time.Minute)
	r.RunAll(ctx)
	if got := r.Warnings()[0].Since; !got.Equal(first) {
		t.Errorf("since moved from %v to %v", first, got)
	}
	c.set()
	r.RunAll(ctx)
	c.set(Finding{Key: "c.bad", Level: LevelWarning, Message: "bad"})
	r.RunAll(ctx)
	if got := r.Warnings()[0].Since; !got.After(first) {
		t.Errorf("since didn't reset after the problem cleared: %v", got)
	}
}

// Refresh re-runs everything, but not twice inside its gap.
func TestRegistryRefreshRateLimited(t *testing.T) {
	r, clk := testRegistry(nil)
	c := &counting{}
	r.Register(Check{Key: "c", Name: "C", Interval: time.Hour, Run: c.run})
	ctx := context.Background()
	if !r.Refresh(ctx, 10*time.Second) || c.runs.Load() != 1 {
		t.Fatalf("first refresh: runs=%d", c.runs.Load())
	}
	if r.Refresh(ctx, 10*time.Second) || c.runs.Load() != 1 {
		t.Fatalf("second refresh inside the gap ran: runs=%d", c.runs.Load())
	}
	clk.add(11 * time.Second)
	if !r.Refresh(ctx, 10*time.Second) || c.runs.Load() != 2 {
		t.Fatalf("refresh after the gap: runs=%d", c.runs.Load())
	}
}

// Results report errors first, a level per check, pending before the first run, and the
// fix link fields.
func TestRegistryResults(t *testing.T) {
	r, _ := testRegistry(nil)
	w := &counting{}
	w.set(Finding{Key: "w", Level: LevelWarning, Message: "warn"})
	e := &counting{}
	e.set(Finding{Key: "e", Level: LevelError, Message: "err", Fix: FixIndexers})
	r.Register(Check{Key: "warn", Name: "W", Category: CategoryStorage, Run: w.run})
	r.Register(Check{Key: "err", Name: "E", Category: CategoryIndexers, Run: e.run})

	if rep := r.Results(); rep.Status != "ok" || rep.Checks[0].Level != "pending" {
		t.Fatalf("before any run: %+v", rep)
	}
	r.RunAll(context.Background())
	rep := r.Results()
	if rep.Status != LevelError || len(rep.Warnings) != 2 || rep.Warnings[0].Key != "e" {
		t.Fatalf("errors first: %+v", rep.Warnings)
	}
	if got := rep.Warnings[0]; got.Link != "/indexers" || got.LinkKey != "indexers" || got.LinkLabel != "Add an indexer" || got.Check != "err" {
		t.Errorf("fix fields: %+v", got)
	}
	if rep.Checks[0].Key != "warn" || rep.Checks[0].Level != LevelWarning || rep.Checks[1].Level != LevelError {
		t.Errorf("checks by category with levels: %+v", rep.Checks)
	}
}

// RunNow runs one check by key; an unknown key is a no-op.
func TestRegistryRunNow(t *testing.T) {
	r, _ := testRegistry(nil)
	a, b := &counting{}, &counting{}
	r.Register(Check{Key: "a", Run: a.run})
	r.Register(Check{Key: "b", Run: b.run})
	r.RunNow(context.Background(), "a")
	r.RunNow(context.Background(), "nope")
	if a.runs.Load() != 1 || b.runs.Load() != 0 {
		t.Errorf("runs a=%d b=%d", a.runs.Load(), b.runs.Load())
	}
}

// A check that panics is a warning, not a crash.
func TestRegistryCheckPanics(t *testing.T) {
	r, _ := testRegistry(nil)
	r.Register(Check{Key: "boom", Name: "Boom", Run: func(context.Context) []Finding { panic("nil map") }})
	r.RunAll(context.Background())
	ws := r.Warnings()
	if len(ws) != 1 || ws[0].Key != "boom.failed" {
		t.Errorf("panicking check: %+v", ws)
	}
}
