package health

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/safego"
)

// The health registry answers "is everything Arrmada depends on working?" from results
// kept in memory. Each check runs in the background on its own interval and under its
// own timeout, driven by the health-check scheduled task, so the polled health endpoint
// never waits on qBittorrent, Plex or TMDB — before, every Dashboard poll ran every check
// live, and one slow client made the whole panel slow.

// Levels a finding can have. A check with no findings is OK.
const (
	LevelWarning = "warning" // something is degraded
	LevelError   = "error"   // something that matters doesn't work at all
)

// Default interval and timeout for a check that doesn't set its own.
const (
	DefaultInterval = 60 * time.Second
	DefaultTimeout  = 3 * time.Second
)

// Categories, in the order the UI lists them.
const (
	CategoryStorage      = "Storage"
	CategoryDownloads    = "Downloads"
	CategoryIndexers     = "Indexers"
	CategoryIntegrations = "Integrations"
	CategoryTasks        = "Tasks"
)

var categoryOrder = map[string]int{
	CategoryStorage: 0, CategoryDownloads: 1, CategoryIndexers: 2, CategoryIntegrations: 3, CategoryTasks: 4,
}

// Fix is where in the app a problem gets fixed. Key names the web UI's LINKS entry (so a
// page that moves is repointed in one place there); Path is the same address, for
// clients that don't know the key.
type Fix struct {
	Key   string
	Path  string
	Label string
}

// Finding is one problem a check found. Key is stable across runs ("library.tv",
// "downloads.client.3"): it's what "since" and change detection are keyed on.
type Finding struct {
	Key     string
	Level   string
	Message string
	Fix     *Fix
}

// Check is one background health check. Run returns nothing when all is well. It must
// honour ctx: a check still running when its timeout passes is reported as timed out and
// keeps its previous findings until it finishes a later run.
type Check struct {
	Key      string
	Name     string
	Category string
	Interval time.Duration // 0 = DefaultInterval
	Timeout  time.Duration // 0 = DefaultTimeout
	Run      func(ctx context.Context) []Finding
}

// Warning is a finding as the API serves it.
type Warning struct {
	Key       string    `json:"key"`
	Check     string    `json:"check"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	Link      string    `json:"link,omitempty"`
	LinkKey   string    `json:"link_key,omitempty"`
	LinkLabel string    `json:"link_label,omitempty"`
	Since     time.Time `json:"since"` // when this problem was first seen, this time round
}

// CheckResult is one check's latest outcome. Level is "ok", "warning", "error", or
// "pending" before its first run.
type CheckResult struct {
	Key        string    `json:"key"`
	Name       string    `json:"name"`
	Category   string    `json:"category"`
	Level      string    `json:"level"`
	CheckedAt  time.Time `json:"checked_at"`
	DurationMS int64     `json:"duration_ms"`
	Stale      bool      `json:"stale"` // the last run timed out; findings are from before
	Findings   []Warning `json:"findings"`
}

// Report is everything the registry knows.
type Report struct {
	Status   string        `json:"status"` // "ok" | "warning" | "error"
	Warnings []Warning     `json:"warnings"`
	Checks   []CheckResult `json:"checks"`
}

// Publisher is the event bus, as far as the registry needs it.
type Publisher interface {
	Publish(topic string, data any)
}

// TopicChanged is published when the set of problems changes. Its payload is counts only
// ({status, errors, warnings}): every staff client receives it, and the text — paths,
// error strings — stays behind the role-gated endpoint.
const TopicChanged = "health.changed"

type entry struct {
	Check
	ran       bool
	inflight  bool // a run is still going (possibly past its timeout)
	next      time.Time
	checkedAt time.Time
	dur       time.Duration
	findings  []Finding
	stale     bool
}

// Registry holds the checks and their latest results.
type Registry struct {
	log *slog.Logger
	pub Publisher
	now func() time.Time

	mu        sync.Mutex
	entries   []*entry
	byKey     map[string]*entry
	since     map[string]time.Time
	lastSig   string
	refreshAt time.Time
}

// NewRegistry builds an empty registry. pub may be nil (no change events).
func NewRegistry(pub Publisher, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.Default()
	}
	return &Registry{log: log, pub: pub, now: time.Now, byKey: map[string]*entry{}, since: map[string]time.Time{}}
}

// Register adds a check. A second check with the same key replaces the first.
func (r *Registry) Register(c Check) {
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.Category == "" {
		c.Category = CategoryStorage
	}
	e := &entry{Check: c}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.byKey[c.Key]; ok {
		for i, x := range r.entries {
			if x == old {
				r.entries[i] = e
			}
		}
	} else {
		r.entries = append(r.entries, e)
	}
	r.byKey[c.Key] = e
}

// dueSlack lets a check run on the tick just before its time instead of a whole tick late:
// the driving task fires every 30s, so a 60s check started at :00 is due again at :60, and
// a tick landing a hair early shouldn't push it to :90.
const dueSlack = 2 * time.Second

// RunDue runs every check whose interval has passed, in parallel, and returns when each
// has finished or timed out. It's the health-check scheduled task.
func (r *Registry) RunDue(ctx context.Context) error {
	now := r.now()
	r.mu.Lock()
	var due []*entry
	for _, e := range r.entries {
		if !e.inflight && !now.Before(e.next.Add(-dueSlack)) {
			due = append(due, e)
		}
	}
	r.mu.Unlock()
	r.run(ctx, due)
	return nil
}

// RunAll runs every check now, whatever its interval.
func (r *Registry) RunAll(ctx context.Context) {
	r.mu.Lock()
	var all []*entry
	for _, e := range r.entries {
		if !e.inflight {
			all = append(all, e)
		}
	}
	r.mu.Unlock()
	r.run(ctx, all)
}

// Refresh is RunAll at most once per minGap, for a "Check now" button: it reports whether
// it ran. Without the limit a held-down refresh would hammer every integration.
func (r *Registry) Refresh(ctx context.Context, minGap time.Duration) bool {
	r.mu.Lock()
	now := r.now()
	if !r.refreshAt.IsZero() && now.Sub(r.refreshAt) < minGap {
		r.mu.Unlock()
		return false
	}
	r.refreshAt = now
	r.mu.Unlock()
	r.RunAll(withForced(ctx))
	return true
}

type forcedKey struct{}

func withForced(ctx context.Context) context.Context {
	return context.WithValue(ctx, forcedKey{}, true)
}

// Forced reports whether a person asked for this run ("Check now", or a setting the check
// depends on was just saved). Checks that pace their own calls to an outside service —
// TMDB every six hours, Plex every five minutes — ask it now instead of reusing their
// last answer.
func Forced(ctx context.Context) bool {
	v, _ := ctx.Value(forcedKey{}).(bool)
	return v
}

// RunNow runs one check straight away (after a setting it depends on was saved, say). An
// unknown key, or one already running, is a no-op.
func (r *Registry) RunNow(ctx context.Context, key string) {
	r.mu.Lock()
	e, ok := r.byKey[key]
	if !ok || e.inflight {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	r.run(withForced(ctx), []*entry{e})
}

func (r *Registry) run(ctx context.Context, es []*entry) {
	if len(es) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, e := range es {
		r.mu.Lock()
		if e.inflight {
			r.mu.Unlock()
			continue
		}
		e.inflight = true
		r.mu.Unlock()
		wg.Add(1)
		safego.Go(r.log, "health check "+e.Key, func() {
			defer wg.Done()
			r.runOne(ctx, e)
		})
	}
	wg.Wait()
	r.settle()
}

// runOne runs a check under its timeout. A check that overruns is left to finish on its
// own goroutine (it's marked in flight, so it isn't started again meanwhile) and keeps
// the findings it had, marked stale, plus a "timed out" warning.
func (r *Registry) runOne(ctx context.Context, e *entry) {
	cctx, cancel := context.WithTimeout(ctx, e.Timeout)
	start := r.now()
	done := make(chan []Finding, 1)
	safego.Go(r.log, "health check "+e.Key, func() {
		var out []Finding
		err := safego.Call(r.log, "health check "+e.Key, func() error {
			out = e.Run(cctx)
			return nil
		})
		if err != nil {
			out = []Finding{{Key: e.Key + ".failed", Level: LevelWarning, Message: e.Name + " check failed: " + err.Error()}}
		}
		done <- out
	})

	select {
	case out := <-done:
		cancel()
		r.mu.Lock()
		e.findings, e.stale = out, false
		e.ran, e.inflight = true, false
		e.checkedAt, e.dur = start, r.now().Sub(start)
		e.next = start.Add(e.Interval)
		r.mu.Unlock()
	case <-cctx.Done():
		if ctx.Err() != nil {
			// Shutting down, not a slow check: leave its results as they were.
			cancel()
			r.mu.Lock()
			e.inflight = false
			r.mu.Unlock()
			return
		}
		r.mu.Lock()
		e.stale = true
		e.ran = true
		e.checkedAt, e.dur = start, r.now().Sub(start)
		e.next = start.Add(e.Interval)
		r.mu.Unlock()
		r.log.Warn("health check timed out", "check", e.Key, "timeout", e.Timeout)
		// Free the slot once the overrunning run really ends; its late answer is dropped,
		// as the world may have moved on since it started.
		safego.Go(r.log, "health check "+e.Key+" (overrun)", func() {
			<-done
			cancel()
			r.mu.Lock()
			e.inflight = false
			r.mu.Unlock()
		})
	}
}

// findingsLocked is a check's current findings, including its timed-out warning.
func (e *entry) findingsLocked() []Finding {
	if !e.stale {
		return e.findings
	}
	out := append([]Finding(nil), e.findings...)
	return append(out, Finding{Key: e.Key + ".timeout", Level: LevelWarning, Message: e.Name + " check timed out"})
}

// settle updates the since times and announces a changed set of problems.
func (r *Registry) settle() {
	r.mu.Lock()
	now := r.now()
	seen := map[string]bool{}
	var parts []string
	errs, warns := 0, 0
	for _, e := range r.entries {
		for _, f := range e.findingsLocked() {
			seen[f.Key] = true
			if _, ok := r.since[f.Key]; !ok {
				r.since[f.Key] = now
			}
			parts = append(parts, f.Key+"\x00"+f.Level+"\x00"+f.Message)
			if f.Level == LevelError {
				errs++
			} else {
				warns++
			}
		}
	}
	for k := range r.since {
		if !seen[k] {
			delete(r.since, k)
		}
	}
	sort.Strings(parts)
	sig := strings.Join(parts, "\x01")
	changed := sig != r.lastSig
	r.lastSig = sig
	r.mu.Unlock()

	if changed && r.pub != nil {
		r.pub.Publish(TopicChanged, map[string]any{"status": status(errs, warns), "errors": errs, "warnings": warns})
	}
}

func status(errs, warns int) string {
	switch {
	case errs > 0:
		return LevelError
	case warns > 0:
		return LevelWarning
	}
	return "ok"
}

// Results is the latest outcome of every check, without running anything. Warnings are
// errors first, then by category and registration order.
func (r *Registry) Results() Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	rep := Report{Warnings: []Warning{}, Checks: make([]CheckResult, 0, len(r.entries))}
	errs, warns := 0, 0
	for _, e := range r.entries {
		cr := CheckResult{Key: e.Key, Name: e.Name, Category: e.Category, Level: "pending", Findings: []Warning{}}
		if e.ran {
			cr.Level = "ok"
			cr.CheckedAt = e.checkedAt
			cr.DurationMS = e.dur.Milliseconds()
			cr.Stale = e.stale
		}
		for _, f := range e.findingsLocked() {
			w := Warning{Key: f.Key, Check: e.Key, Level: f.Level, Message: f.Message, Since: r.since[f.Key]}
			if f.Fix != nil {
				w.Link, w.LinkKey, w.LinkLabel = f.Fix.Path, f.Fix.Key, f.Fix.Label
			}
			cr.Findings = append(cr.Findings, w)
			if f.Level == LevelError {
				errs++
				cr.Level = LevelError
			} else {
				warns++
				if cr.Level != LevelError {
					cr.Level = LevelWarning
				}
			}
		}
		rep.Checks = append(rep.Checks, cr)
	}
	sort.SliceStable(rep.Checks, func(i, j int) bool {
		return categoryOrder[rep.Checks[i].Category] < categoryOrder[rep.Checks[j].Category]
	})
	for _, c := range rep.Checks {
		rep.Warnings = append(rep.Warnings, c.Findings...)
	}
	sort.SliceStable(rep.Warnings, func(i, j int) bool {
		return rep.Warnings[i].Level == LevelError && rep.Warnings[j].Level != LevelError
	})
	rep.Status = status(errs, warns)
	return rep
}

// Warnings is Results().Warnings.
func (r *Registry) Warnings() []Warning { return r.Results().Warnings }

// String is for logs and test failures.
func (f Finding) String() string { return fmt.Sprintf("%s [%s] %s", f.Key, f.Level, f.Message) }
