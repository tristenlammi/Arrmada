// Package connstatus remembers how each integration Arrmada talks to — an indexer, a
// download client, FlareSolverr — has been answering: when it last worked, when and why
// it last failed, how many times in a row, and, for indexers, how long background work
// should leave it alone before trying again.
//
// Before it, a failing indexer only showed up in one search's error map and a log line.
// Nothing could put a red dot on its row, the health panel couldn't name it, and an
// expired login was retried on every search of every sweep — the sort of hammering that
// gets a private-tracker account banned.
//
// State lives in memory behind a mutex. A row is written straight away only when
// something changes that a person would see (working → failing, failing → working, the
// next step of the backoff); everything else (the hourly counters, the average response
// time, a repeat of the same failure) is written by Flush, which the scheduler runs once
// a minute. So a busy sweep costs at most one write per integration per minute.
package connstatus

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Kinds of integration the tracker knows. ref is the integration's id within its kind.
const (
	KindIndexer        = "indexer"
	KindDownloadClient = "download_client"
	KindFlareSolverr   = "flaresolverr"
)

// Phases an integration can be in, as the API and the UI name them.
const (
	PhaseUnknown    = "unknown"     // never asked since it was added or edited
	PhaseOK         = "ok"          // the last attempt worked
	PhaseFailing    = "failing"     // the last attempt failed; background work still asks it
	PhaseBackingOff = "backing_off" // failing, and background work is leaving it alone for now
)

// countsKeep is how long hourly counters are kept; the UI shows the last 24 hours.
const countsKeep = 48 * time.Hour

// writeTimeout bounds a transition write made from inside Record, which has no context
// of its own (it runs at the end of a search).
const writeTimeout = 5 * time.Second

// State is one integration's health.
type State struct {
	Kind                string
	Ref                 string
	LastOKAt            time.Time
	LastErrorAt         time.Time
	LastError           string // redacted
	ConsecutiveFailures int
	FailingSince        time.Time
	BackoffUntil        time.Time
	AvgMS               int64
}

// Phase says what the state means at now.
func (s State) Phase(now time.Time) string {
	switch {
	case now.Before(s.BackoffUntil):
		return PhaseBackingOff
	case s.ConsecutiveFailures > 0:
		return PhaseFailing
	case !s.LastOKAt.IsZero():
		return PhaseOK
	}
	return PhaseUnknown
}

// Outcome is one attempt to use an integration.
type Outcome struct {
	Err        error         // nil = it worked
	Dur        time.Duration // how long it took (successes feed the average)
	RetryAfter time.Duration // the server's Retry-After, if it sent one
	// Backoff lets a failure move the backoff ladder. A person's Test and a download
	// client's queue read leave it false: the error still shows, but nothing is paused.
	Backoff bool
	// Name is how logs name the integration ("TorrentLeech"); it isn't stored.
	Name string
}

// Counts is how much an integration was used in a window.
type Counts struct {
	Queries  int
	Failures int
}

type key struct{ kind, ref string }

type countKey struct {
	key
	hour string // UTC, "2006-01-02T15"
}

type entry struct {
	State
	dirty bool
}

type bucket struct {
	Counts
	dirty bool
}

// Tracker records outcomes and answers "may background work use this integration now?".
// Every method is safe on a nil *Tracker: it records nothing and allows everything, so a
// service built without one behaves as before.
type Tracker struct {
	db  *sql.DB
	log *slog.Logger
	now func() time.Time

	// wmu orders writes: each writer takes its snapshot under it, so the row the database
	// ends up with is always the newest one, whichever write finishes last.
	wmu sync.Mutex

	mu       sync.Mutex
	states   map[key]*entry
	counts   map[countKey]*bucket
	onChange []func(State)
}

// New builds a tracker over the database. Call Load before using it so a restart keeps
// what was known.
func New(db *sql.DB, log *slog.Logger) *Tracker {
	if log == nil {
		log = slog.Default()
	}
	return &Tracker{db: db, log: log, now: time.Now, states: map[key]*entry{}, counts: map[countKey]*bucket{}}
}

// OnChange registers fn to be called after a visible change (working ↔ failing, a new
// backoff step, a reset). It runs on the recording goroutine and must not block.
func (t *Tracker) OnChange(fn func(State)) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.onChange = append(t.onChange, fn)
	t.mu.Unlock()
}

// Load reads the saved states and the last 48 hours of counters into memory.
func (t *Tracker) Load(ctx context.Context) error {
	if t == nil {
		return nil
	}
	rows, err := t.db.QueryContext(ctx, `SELECT kind, ref, last_ok_at, last_error_at, last_error,
		consecutive_failures, failing_since, backoff_until, avg_ms FROM integration_status`)
	if err != nil {
		return err
	}
	loaded := map[key]*entry{}
	for rows.Next() {
		var s State
		var ok, errAt, since, until int64
		if err := rows.Scan(&s.Kind, &s.Ref, &ok, &errAt, &s.LastError, &s.ConsecutiveFailures, &since, &until, &s.AvgMS); err != nil {
			rows.Close()
			return err
		}
		s.LastOKAt, s.LastErrorAt, s.FailingSince, s.BackoffUntil = fromUnix(ok), fromUnix(errAt), fromUnix(since), fromUnix(until)
		loaded[key{s.Kind, s.Ref}] = &entry{State: s}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	crows, err := t.db.QueryContext(ctx, `SELECT kind, ref, hour, queries, failures FROM integration_counts WHERE hour >= ?`,
		hourOf(t.now().Add(-countsKeep)))
	if err != nil {
		return err
	}
	defer crows.Close()
	counts := map[countKey]*bucket{}
	for crows.Next() {
		var k countKey
		var c Counts
		if err := crows.Scan(&k.kind, &k.ref, &k.hour, &c.Queries, &c.Failures); err != nil {
			return err
		}
		counts[k] = &bucket{Counts: c}
	}
	if err := crows.Err(); err != nil {
		return err
	}

	t.mu.Lock()
	t.states, t.counts = loaded, counts
	t.mu.Unlock()
	return nil
}

// Allow reports whether work may use the integration now, with its current state. A
// person's own search (interactive) is always allowed: it is how a fixed indexer gets
// noticed, and one success clears the backoff. Background work is turned away while the
// integration is backing off.
func (t *Tracker) Allow(kind, ref string, interactive bool) (bool, State) {
	if t == nil {
		return true, State{Kind: kind, Ref: ref}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := State{Kind: kind, Ref: ref}
	if e := t.states[key{kind, ref}]; e != nil {
		st = e.State
	}
	if interactive {
		return true, st
	}
	return !t.now().Before(st.BackoffUntil), st
}

// Record notes one attempt's outcome.
func (t *Tracker) Record(kind, ref string, o Outcome) {
	if t == nil {
		return
	}
	now := t.now()
	t.mu.Lock()
	k := key{kind, ref}
	e := t.states[k]
	if e == nil {
		e = &entry{State: State{Kind: kind, Ref: ref}}
		t.states[k] = e
	}
	b := t.bucketLocked(k, now)
	b.Queries++
	b.dirty = true

	prev := e.State
	changed := false
	if o.Err == nil {
		changed = prev.ConsecutiveFailures > 0 || !prev.BackoffUntil.IsZero() || prev.LastOKAt.IsZero()
		e.LastOKAt = now
		e.ConsecutiveFailures = 0
		e.FailingSince = time.Time{}
		e.BackoffUntil = time.Time{}
		if ms := o.Dur.Milliseconds(); ms > 0 {
			if e.AvgMS == 0 {
				e.AvgMS = ms
			} else {
				e.AvgMS = (e.AvgMS*4 + ms) / 5 // a moving average: recent answers count most
			}
		}
	} else {
		b.Failures++
		e.LastErrorAt = now
		e.LastError = Redact(o.Err.Error())
		if e.FailingSince.IsZero() {
			e.FailingSince = now
		}
		if now.Before(e.BackoffUntil) {
			// Already paused: this attempt was a person's search, or one that started
			// before the pause began (three sweeps fire within seconds of each other).
			// Neither says anything new, so the ladder stays put — otherwise one burst
			// of sweeps would jump it straight to hours. A longer Retry-After still wins.
			if o.Backoff && o.RetryAfter > 0 {
				if until := now.Add(pauseFor(0, o.RetryAfter)); until.After(e.BackoffUntil) {
					e.BackoffUntil = until
					changed = true
				}
			}
		} else {
			e.ConsecutiveFailures++
			changed = prev.ConsecutiveFailures == 0
			if o.Backoff {
				if d := pauseFor(e.ConsecutiveFailures, o.RetryAfter); d > 0 {
					e.BackoffUntil = now.Add(d)
					changed = true
				}
			}
		}
	}
	e.dirty = true
	cur := e.State
	hooks := t.onChange
	t.mu.Unlock()

	if !changed {
		return
	}
	t.logTransition(prev, cur, o, now)
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := t.writeNow(ctx, k); err != nil {
		t.log.Warn("connstatus: couldn't save a status change; the next flush will", "kind", kind, "err", err)
	}
	for _, fn := range hooks {
		fn(cur)
	}
}

// logTransition says once when an integration starts failing, backs off, or recovers.
func (t *Tracker) logTransition(prev, cur State, o Outcome, now time.Time) {
	what := strings.ReplaceAll(cur.Kind, "_", " ")
	name := o.Name
	if name == "" {
		name = cur.Ref
	}
	switch {
	case o.Err == nil && prev.ConsecutiveFailures > 0:
		t.log.Info(what+" recovered", "name", name, "after_failures", prev.ConsecutiveFailures)
	case o.Err == nil:
		// First success ever (or since an edit): nothing worth a log line.
	case now.Before(cur.BackoffUntil):
		t.log.Warn(what+" backing off", "name", name, "failures", cur.ConsecutiveFailures,
			"until", cur.BackoffUntil.Local().Format("15:04"), "err", cur.LastError)
	default:
		t.log.Warn(what+" failing", "name", name, "failures", cur.ConsecutiveFailures, "err", cur.LastError)
	}
}

// Reset clears an integration's state — it reads as never used — after its settings were
// changed: the old failures said nothing about the new URL or login. Its counters stay.
func (t *Tracker) Reset(ctx context.Context, kind, ref string) error {
	return t.drop(ctx, kind, ref, false)
}

// Forget removes everything known about an integration that was deleted.
func (t *Tracker) Forget(ctx context.Context, kind, ref string) error {
	return t.drop(ctx, kind, ref, true)
}

func (t *Tracker) drop(ctx context.Context, kind, ref string, counts bool) error {
	if t == nil {
		return nil
	}
	t.wmu.Lock()
	defer t.wmu.Unlock()
	t.mu.Lock()
	k := key{kind, ref}
	_, had := t.states[k]
	delete(t.states, k)
	if counts {
		for ck := range t.counts {
			if ck.key == k {
				delete(t.counts, ck)
			}
		}
	}
	hooks := t.onChange
	t.mu.Unlock()
	err := store.WithTx(ctx, t.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM integration_status WHERE kind = ? AND ref = ?`, kind, ref); err != nil {
			return err
		}
		if counts {
			if _, err := tx.ExecContext(ctx, `DELETE FROM integration_counts WHERE kind = ? AND ref = ?`, kind, ref); err != nil {
				return err
			}
		}
		return nil
	})
	if had {
		for _, fn := range hooks {
			fn(State{Kind: kind, Ref: ref})
		}
	}
	return err
}

// Get returns an integration's state; ok is false when nothing is known about it.
func (t *Tracker) Get(kind, ref string) (State, bool) {
	if t == nil {
		return State{Kind: kind, Ref: ref}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.states[key{kind, ref}]; e != nil {
		return e.State, true
	}
	return State{Kind: kind, Ref: ref}, false
}

// List returns every known state of a kind, ordered by ref.
func (t *Tracker) List(kind string) []State {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	var out []State
	for k, e := range t.states {
		if k.kind == kind {
			out = append(out, e.State)
		}
	}
	t.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}

// Counts24h is how many times an integration was used, and how many of those failed, in
// the last 24 hours (whole hours, so up to 25 of them).
func (t *Tracker) Counts24h(kind, ref string) Counts {
	if t == nil {
		return Counts{}
	}
	from := hourOf(t.now().Add(-24 * time.Hour))
	t.mu.Lock()
	defer t.mu.Unlock()
	var c Counts
	for ck, b := range t.counts {
		if ck.kind == kind && ck.ref == ref && ck.hour >= from {
			c.Queries += b.Queries
			c.Failures += b.Failures
		}
	}
	return c
}

// Flush writes what changed since the last flush (counters, averages, repeat failures)
// and prunes counters older than 48 hours. The scheduler runs it every minute.
func (t *Tracker) Flush(ctx context.Context) error {
	if t == nil {
		return nil
	}
	t.wmu.Lock()
	defer t.wmu.Unlock()
	now := t.now()
	cutoff := hourOf(now.Add(-countsKeep))

	t.mu.Lock()
	var states []State
	for _, e := range t.states {
		if e.dirty {
			states = append(states, e.State)
			e.dirty = false
		}
	}
	type countRow struct {
		countKey
		Counts
	}
	var counts []countRow
	for ck, b := range t.counts {
		if ck.hour < cutoff {
			delete(t.counts, ck)
			continue
		}
		if b.dirty {
			counts = append(counts, countRow{ck, b.Counts})
			b.dirty = false
		}
	}
	t.mu.Unlock()

	err := store.WithTx(ctx, t.db, func(tx *sql.Tx) error {
		for _, s := range states {
			if err := upsertState(ctx, tx, s, now); err != nil {
				return err
			}
		}
		for _, c := range counts {
			if _, err := tx.ExecContext(ctx, `INSERT INTO integration_counts (kind, ref, hour, queries, failures)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (kind, ref, hour) DO UPDATE SET queries = excluded.queries, failures = excluded.failures`,
				c.kind, c.ref, c.hour, c.Queries, c.Failures); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM integration_counts WHERE hour < ?`, cutoff)
		return err
	})
	if err != nil {
		// Put the dirty marks back so the next flush tries again.
		t.mu.Lock()
		for _, s := range states {
			if e := t.states[key{s.Kind, s.Ref}]; e != nil {
				e.dirty = true
			}
		}
		for _, c := range counts {
			if b := t.counts[c.countKey]; b != nil {
				b.dirty = true
			}
		}
		t.mu.Unlock()
		return fmt.Errorf("integration status flush: %w", err)
	}
	return nil
}

// writeNow saves one integration's current state at once (a visible change).
func (t *Tracker) writeNow(ctx context.Context, k key) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	t.mu.Lock()
	e := t.states[k]
	if e == nil {
		t.mu.Unlock()
		return nil // reset or forgotten meanwhile
	}
	s := e.State
	e.dirty = false
	t.mu.Unlock()
	err := store.WithTx(ctx, t.db, func(tx *sql.Tx) error { return upsertState(ctx, tx, s, t.now()) })
	if err != nil {
		t.mu.Lock()
		if e := t.states[k]; e != nil {
			e.dirty = true
		}
		t.mu.Unlock()
	}
	return err
}

func upsertState(ctx context.Context, tx *sql.Tx, s State, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO integration_status
		(kind, ref, last_ok_at, last_error_at, last_error, consecutive_failures, failing_since, backoff_until, avg_ms, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (kind, ref) DO UPDATE SET
			last_ok_at = excluded.last_ok_at, last_error_at = excluded.last_error_at, last_error = excluded.last_error,
			consecutive_failures = excluded.consecutive_failures, failing_since = excluded.failing_since,
			backoff_until = excluded.backoff_until, avg_ms = excluded.avg_ms, updated_at = excluded.updated_at`,
		s.Kind, s.Ref, toUnix(s.LastOKAt), toUnix(s.LastErrorAt), s.LastError, s.ConsecutiveFailures,
		toUnix(s.FailingSince), toUnix(s.BackoffUntil), s.AvgMS, now.Unix())
	return err
}

func (t *Tracker) bucketLocked(k key, now time.Time) *bucket {
	ck := countKey{k, hourOf(now)}
	b := t.counts[ck]
	if b == nil {
		b = &bucket{}
		t.counts[ck] = b
	}
	return b
}

func hourOf(t time.Time) string { return t.UTC().Format("2006-01-02T15") }

func toUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(s int64) time.Time {
	if s <= 0 {
		return time.Time{}
	}
	return time.Unix(s, 0)
}
