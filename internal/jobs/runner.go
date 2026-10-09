// Package jobs runs background work that a person or an event starts — a search, a scan,
// an import, a Run now — and keeps a record of it in the jobs table.
//
// Before it, that work left no trace: nobody could see what was running, what a search
// found or why it failed, and two clicks on Search (or a click during the 5-minute sweep)
// ran two searches for the same movie. The runner gives every piece of work:
//
//   - single-flight per (kind, target): submitting work that is already queued or running
//     returns the existing job's id instead of starting a second copy;
//   - class limits, so a burst of searches can't hammer the indexers (indexer-search runs
//     two at a time, library scans and imports one) — the rest wait, visibly queued;
//   - panic safety, a timeout, cancellation, and a clean stop at shutdown;
//   - progress and a result, written to the database and announced as job.updated.
//
// The scheduler's recurring ticks stay out of it (a 30-second import sweep would add
// thousands of rows a day); only a Run now of a task becomes a job.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/safego"
)

// Job states.
const (
	StatusQueued      = "queued"
	StatusRunning     = "running"
	StatusSucceeded   = "succeeded"
	StatusFailed      = "failed"
	StatusCancelled   = "cancelled"
	StatusPanicked    = "panicked"
	StatusInterrupted = "interrupted" // found unfinished at boot: the app stopped mid-job
)

// Terminal reports whether a status is final.
func Terminal(status string) bool {
	switch status {
	case StatusQueued, StatusRunning:
		return false
	}
	return true
}

// TopicUpdated is published on every transition and (throttled) progress change. It is a
// staff topic: the websocket policy keeps anything not declared otherwise from requesters.
const TopicUpdated = "job.updated"

// Classes and their default concurrency.
const (
	ClassIndexerSearch  = "indexer-search"
	ClassLibraryScan    = "library-scan"
	ClassImport         = "import"
	ClassExternalImport = "external-import"
	ClassTask           = "task"
	ClassOther          = "other"
)

var defaultLimits = map[string]int{
	ClassIndexerSearch:  2,
	ClassLibraryScan:    1,
	ClassImport:         1,
	ClassExternalImport: 1,
	ClassTask:           4,
	ClassOther:          2,
}

// Errors.
var (
	ErrNotFound = errors.New("job not found")
	ErrFinished = errors.New("job already finished")
	ErrShutdown = errors.New("shutting down")
)

// Spec describes work to run.
type Spec struct {
	Kind    string // what the work is, e.g. "movie.search"
	Target  string // what it's for, e.g. "movie:12"; "" or "all" for everything
	Trigger string // who or what started it, e.g. "user:3", "request:9", "system"
	Class   string // concurrency class; "" is ClassOther
	// Timeout bounds the work itself: its clock starts when the job leaves the queue and
	// begins running, not at Submit. A search that waited ten minutes behind 300 others
	// still gets its whole budget (it used to time out while it waited).
	Timeout time.Duration
	// Fn does the work. Its result is stored as JSON. Fn must watch ctx: shutdown,
	// Cancel and Timeout all arrive through it.
	Fn func(ctx context.Context, p *Progress) (any, error)
	// Abandon, when set, is called if the job ends without Fn ever running (cancelled or
	// shut down while queued). Work that claims something before submitting (the books
	// sweep's "running" flag) gives the claim back here.
	Abandon func()
}

// Job is one row of the jobs table.
type Job struct {
	ID         int64           `json:"id"`
	Kind       string          `json:"kind"`
	Target     string          `json:"target"`
	Trigger    string          `json:"trigger"`
	Status     string          `json:"status"`
	Progress   float64         `json:"progress"` // 0..1
	Message    string          `json:"message"`
	Error      string          `json:"error"`
	Result     json.RawMessage `json:"result,omitempty"`
	CreatedAt  *time.Time      `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
}

// Filter narrows List. Empty fields match anything; Limit defaults to 50, at most 500.
type Filter struct {
	Kind, Target, Status string
	Limit                int
}

// Publisher is the event bus, narrowed.
type Publisher interface {
	Publish(topic string, data any)
}

type key struct{ kind, target string }

// entry is a job the runner is holding (queued or running).
type entry struct {
	id     int64
	spec   Spec
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	prog   *Progress

	mu         sync.Mutex
	status     string
	userCancel bool
}

// Runner runs jobs. Create with New; stop with Shutdown.
type Runner struct {
	db  *sql.DB
	log *slog.Logger
	bus Publisher

	root       context.Context
	cancelRoot context.CancelFunc

	mu     sync.Mutex
	active map[key]*entry
	byID   map[int64]*entry
	sems   map[string]chan struct{}
	limits map[string]int
	closed bool
	wg     sync.WaitGroup

	now           func() time.Time
	progressEvery time.Duration
}

// New makes a Runner whose jobs run under ctx (the app's run context). Jobs left queued
// or running by a crash or a kill are marked interrupted: they will never finish.
func New(ctx context.Context, db *sql.DB, log *slog.Logger, bus Publisher) (*Runner, error) {
	if log == nil {
		log = slog.Default()
	}
	root, cancel := context.WithCancel(ctx)
	r := &Runner{
		db: db, log: log, bus: bus,
		root: root, cancelRoot: cancel,
		active: map[key]*entry{}, byID: map[int64]*entry{}, sems: map[string]chan struct{}{},
		limits: map[string]int{}, now: time.Now, progressEvery: time.Second,
	}
	for c, n := range defaultLimits {
		r.limits[c] = n
	}
	n, err := markInterrupted(ctx, db, r.now())
	if err != nil {
		cancel()
		return nil, err
	}
	if n > 0 {
		log.Info("jobs: marked work left unfinished by the last run as interrupted", "jobs", n)
	}
	return r, nil
}

// SetLimit changes a class's concurrency. Call before submitting work of that class.
func (r *Runner) SetLimit(class string, n int) {
	if n < 1 {
		n = 1
	}
	r.mu.Lock()
	r.limits[class] = n
	delete(r.sems, class)
	r.mu.Unlock()
}

func (r *Runner) sem(class string) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sems[class]; ok {
		return s
	}
	n, ok := r.limits[class]
	if !ok {
		n = r.limits[ClassOther]
	}
	s := make(chan struct{}, n)
	r.sems[class] = s
	return s
}

// Submit starts work in the background and returns its job id. When a job with the same
// (kind, target) is already queued or running, its id comes back with existing=true and
// nothing new starts.
func (r *Runner) Submit(ctx context.Context, spec Spec) (int64, bool, error) {
	if spec.Kind == "" || spec.Fn == nil {
		return 0, false, errors.New("jobs: a job needs a kind and a function")
	}
	if spec.Class == "" {
		spec.Class = ClassOther
	}
	k := key{spec.Kind, spec.Target}

	// The insert happens under the lock so two submits of the same work can't both miss
	// the map and both insert. It's one small row; the lock is never held across the work.
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return 0, false, ErrShutdown
	}
	if e, ok := r.active[k]; ok {
		r.mu.Unlock()
		return e.id, true, nil
	}
	now := r.now()
	wctx, cancelW := writeCtx(ctx)
	id, err := insertJob(wctx, r.db, spec, now)
	cancelW()
	if err != nil {
		r.mu.Unlock()
		return 0, false, fmt.Errorf("jobs: record %s: %w", spec.Kind, err)
	}
	jctx, cancel := context.WithCancel(r.root)
	e := &entry{id: id, spec: spec, ctx: jctx, cancel: cancel, done: make(chan struct{}), status: StatusQueued}
	e.prog = &Progress{r: r, e: e}
	r.active[k] = e
	r.byID[id] = e
	r.wg.Add(1)
	r.mu.Unlock()

	r.publish(e, StatusQueued, 0, "", "")
	go r.run(e)
	return id, false, nil
}

// Run is Submit that waits for the job to finish (or ctx to end) and returns it. When the
// work was already running it waits for that job instead.
func (r *Runner) Run(ctx context.Context, spec Spec) (Job, error) {
	id, _, err := r.Submit(ctx, spec)
	if err != nil {
		return Job{}, err
	}
	if err := r.Wait(ctx, id); err != nil {
		return Job{}, err
	}
	return r.Get(ctx, id)
}

// Wait blocks until job id is no longer queued or running, or ctx ends.
func (r *Runner) Wait(ctx context.Context, id int64) error {
	r.mu.Lock()
	e, ok := r.byID[id]
	r.mu.Unlock()
	if !ok {
		return nil // already finished (or never ours)
	}
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) run(e *entry) {
	defer r.wg.Done()
	defer e.cancel()

	sem := r.sem(e.spec.Class)
	abandon := func() {
		if e.spec.Abandon != nil {
			_ = safego.Call(r.log, "job abandon "+e.spec.Kind, func() error { e.spec.Abandon(); return nil })
		}
		r.finish(e, e.ctx, nil, e.ctx.Err())
	}
	select {
	case sem <- struct{}{}:
	case <-e.ctx.Done():
		abandon()
		return
	}
	defer func() { <-sem }()
	// A slot that freed up at the moment the job was cancelled (a shutdown stops the
	// running job and wakes this one) is not a reason to start it.
	if e.ctx.Err() != nil {
		abandon()
		return
	}

	// The timeout starts now, with the work, not at Submit.
	workCtx := e.ctx
	if e.spec.Timeout > 0 {
		var cancelT context.CancelFunc
		workCtx, cancelT = context.WithTimeout(e.ctx, e.spec.Timeout)
		defer cancelT()
	}

	started := r.now()
	e.mu.Lock()
	e.status = StatusRunning
	e.mu.Unlock()
	wctx, cancelW := writeCtx(context.Background())
	if err := markRunning(wctx, r.db, e.id, started); err != nil {
		r.log.Warn("jobs: couldn't record a start", "job", e.id, "kind", e.spec.Kind, "err", err)
	}
	cancelW()
	r.publish(e, StatusRunning, 0, "", "")

	var result any
	err := safego.Call(r.log, "job "+e.spec.Kind+" "+e.spec.Target, func() error {
		var err error
		result, err = e.spec.Fn(workCtx, e.prog)
		return err
	})
	r.finish(e, workCtx, result, err)
}

// finish records a job's outcome and lets go of it. ctx is what the work ran under, its
// timeout included.
func (r *Runner) finish(e *entry, ctx context.Context, result any, err error) {
	e.mu.Lock()
	userCancel := e.userCancel
	e.mu.Unlock()

	status, errText := StatusSucceeded, ""
	var pe *safego.PanicError
	switch {
	case err == nil:
	case errors.As(err, &pe):
		status, errText = StatusPanicked, fmt.Sprintf("panic: %v", pe.Value)
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		status, errText = StatusFailed, err.Error()
	case errors.Is(err, context.Canceled) || (ctx.Err() != nil && (userCancel || r.root.Err() != nil)):
		status, errText = StatusCancelled, err.Error()
	default:
		status, errText = StatusFailed, err.Error()
	}

	var resJSON []byte
	if result != nil {
		if b, jerr := json.Marshal(result); jerr == nil {
			resJSON = b
		} else {
			r.log.Warn("jobs: result isn't JSON", "job", e.id, "kind", e.spec.Kind, "err", jerr)
		}
	}
	pct, msg := e.prog.snapshot()
	if status == StatusSucceeded {
		pct = 1
	}

	// The work is over: new work on the same item may start now, rather than joining a
	// job that is only writing its result (a request approved at that moment would
	// otherwise have its search folded into one that already ran). Get and Wait still
	// find this job until its result is written.
	r.mu.Lock()
	if r.active[key{e.spec.Kind, e.spec.Target}] == e {
		delete(r.active, key{e.spec.Kind, e.spec.Target})
	}
	r.mu.Unlock()

	wctx, cancelW := writeCtx(context.Background())
	if werr := markFinished(wctx, r.db, e.id, status, pct, msg, errText, string(resJSON), r.now()); werr != nil {
		r.log.Warn("jobs: couldn't record a result", "job", e.id, "kind", e.spec.Kind, "err", werr)
	}
	cancelW()

	r.mu.Lock()
	e.mu.Lock()
	e.status = status
	e.mu.Unlock()
	delete(r.byID, e.id)
	r.mu.Unlock()
	close(e.done)

	if status != StatusSucceeded && status != StatusCancelled {
		r.log.Warn("job "+status, "kind", e.spec.Kind, "target", e.spec.Target, "err", errText)
	}
	r.publish(e, status, pct, msg, errText)
}

// Cancel stops a queued or running job. ErrFinished for one that already ended,
// ErrNotFound for an id that never existed.
func (r *Runner) Cancel(ctx context.Context, id int64) error {
	r.mu.Lock()
	e, ok := r.byID[id]
	r.mu.Unlock()
	if ok {
		e.mu.Lock()
		e.userCancel = true
		e.mu.Unlock()
		e.cancel()
		return nil
	}
	if _, err := getJob(ctx, r.db, id); err != nil {
		return err
	}
	return ErrFinished
}

// Get returns one job. A job still running carries its latest progress even when the
// database copy is a second behind.
func (r *Runner) Get(ctx context.Context, id int64) (Job, error) {
	j, err := getJob(ctx, r.db, id)
	if err != nil {
		return Job{}, err
	}
	return r.overlay(j), nil
}

// List returns jobs newest first.
func (r *Runner) List(ctx context.Context, f Filter) ([]Job, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	out, err := listJobs(ctx, r.db, f)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i] = r.overlay(out[i])
	}
	return out, nil
}

func (r *Runner) overlay(j Job) Job {
	r.mu.Lock()
	e, ok := r.byID[j.ID]
	r.mu.Unlock()
	if !ok {
		return j
	}
	e.mu.Lock()
	j.Status = e.status
	e.mu.Unlock()
	j.Progress, j.Message = e.prog.snapshot()
	return j
}

// Active lists the jobs queued or running right now, oldest first.
func (r *Runner) Active() []Job {
	r.mu.Lock()
	es := make([]*entry, 0, len(r.byID))
	for _, e := range r.byID {
		es = append(es, e)
	}
	r.mu.Unlock()
	sort.Slice(es, func(i, j int) bool { return es[i].id < es[j].id })
	out := make([]Job, 0, len(es))
	for _, e := range es {
		e.mu.Lock()
		st := e.status
		e.mu.Unlock()
		pct, msg := e.prog.snapshot()
		out = append(out, Job{ID: e.id, Kind: e.spec.Kind, Target: e.spec.Target, Trigger: e.spec.Trigger, Status: st, Progress: pct, Message: msg})
	}
	return out
}

// Shutdown stops taking work, cancels everything running and waits up to timeout for it
// to stop. Whatever is still going then is recorded as cancelled (it is about to be cut
// off by the exit). It returns the kinds of those leftovers.
func (r *Runner) Shutdown(timeout time.Duration) []string {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.cancelRoot()

	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return nil
	case <-t.C:
	}
	r.mu.Lock()
	var left []*entry
	for _, e := range r.byID {
		left = append(left, e)
	}
	r.mu.Unlock()
	var names []string
	for _, e := range left {
		pct, msg := e.prog.snapshot()
		wctx, cancelW := writeCtx(context.Background())
		_ = markFinished(wctx, r.db, e.id, StatusCancelled, pct, msg, "stopped at shutdown", "", r.now())
		cancelW()
		names = append(names, e.spec.Kind+" "+e.spec.Target)
	}
	sort.Strings(names)
	return names
}

// Prune deletes finished jobs older than keepFor and all but the newest keepMax finished
// ones. Unfinished jobs are never touched.
func (r *Runner) Prune(ctx context.Context, keepFor time.Duration, keepMax int) (int64, error) {
	return pruneJobs(ctx, r.db, r.now().Add(-keepFor), keepMax)
}

// PruneDefault is the daily jobs-prune task: 14 days, at most 5000 rows.
func (r *Runner) PruneDefault(ctx context.Context) error {
	_, err := r.Prune(ctx, 14*24*time.Hour, 5000)
	return err
}

func (r *Runner) publish(e *entry, status string, pct float64, msg, errText string) {
	if r.bus == nil {
		return
	}
	r.bus.Publish(TopicUpdated, map[string]any{
		"id": e.id, "kind": e.spec.Kind, "target": e.spec.Target, "status": status,
		"progress": pct, "message": msg, "error": errText,
	})
}

// writeCtx bounds a bookkeeping write. It ignores the caller's cancellation (a job that
// was cancelled still has to be recorded as cancelled) but not forever.
func writeCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

// Progress is how a job reports how far it has got. The zero of a nil *Progress is safe to
// call, so work can be shared between jobs and plain calls.
type Progress struct {
	r *Runner
	e *entry

	mu      sync.Mutex
	pct     float64
	msg     string
	written time.Time
}

// Set records progress (0..1) and a short plain-language message. It reaches the database
// and the bus at most once a second; the final value is always written when the job ends.
func (p *Progress) Set(pct float64, msg string) {
	if p == nil {
		return
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 1 {
		pct = 1
	}
	p.mu.Lock()
	p.pct, p.msg = pct, msg
	now := p.r.now()
	due := now.Sub(p.written) >= p.r.progressEvery
	if due {
		p.written = now
	}
	p.mu.Unlock()
	if !due {
		return
	}
	wctx, cancel := writeCtx(context.Background())
	if err := setProgress(wctx, p.r.db, p.e.id, pct, msg); err != nil {
		p.r.log.Debug("jobs: couldn't record progress", "job", p.e.id, "err", err)
	}
	cancel()
	p.r.publish(p.e, StatusRunning, pct, msg, "")
}

// SetMessage changes the message and keeps the progress.
func (p *Progress) SetMessage(msg string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	pct := p.pct
	p.mu.Unlock()
	p.Set(pct, msg)
}

// JobID is the id of the job this Progress reports for (0 for a nil Progress).
func (p *Progress) JobID() int64 {
	if p == nil || p.e == nil {
		return 0
	}
	return p.e.id
}

func (p *Progress) snapshot() (float64, string) {
	if p == nil {
		return 0, ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pct, p.msg
}
