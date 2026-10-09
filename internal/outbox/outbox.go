// Package outbox makes the side effects of an import durable.
//
// When a movie, episode or book lands, other modules have work to do: Convert and
// Subtitles reindex it, its requester is told it's ready, the audiobook catalogue drops
// its cache. That used to ride the event bus, which drops an event when a subscriber is
// busy and forgets everything on a restart — a dropped event meant Convert never saw a
// same-path upgrade and the requester waited for the ten-minute backstop sweep.
//
// Now the code that records the import calls Enqueue, inside its own transaction where it
// has one, and a row per registered consumer is written to the outbox table. Run drains
// the table: each row's handler runs (panic-safe, with a timeout) until it succeeds,
// backing off from 30 seconds to an hour between attempts, and after 20 failures the row
// is marked failed and stays visible for a person to look at and Retry.
//
// Delivery is at least once. A handler can run again after it already did its work (a
// crash between the work and marking the row done, a Retry, the same import enqueued
// twice), so every handler must be idempotent: reindex from current state, insert with a
// unique key, and so on.
//
// The event bus stays, but only for the UI and admin alerts — things where a lost
// message costs a stale screen, not lost work.
package outbox

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
	"github.com/tristenlammi/arrmada/internal/store"
)

// Handler does one consumer's work for one row. payload is what Enqueue was given, as
// JSON. Returning an error (or panicking) retries the row later.
type Handler func(ctx context.Context, payload json.RawMessage) error

// Enqueuer is what producers need: something to write outbox rows with. *Outbox is one.
type Enqueuer interface {
	Enqueue(ctx context.Context, q store.Execer, topic string, payload any, dedupeKey string) error
}

const (
	defaultTimeout   = 2 * time.Minute
	defaultPoll      = 2 * time.Second
	defaultBatch     = 20
	maxAttempts      = 20
	backoffMin       = 30 * time.Second
	backoffMax       = time.Hour
	errNoHandlerText = "no handler"
)

// Option tunes one consumer's registration.
type Option func(*consumer)

// WithTimeout bounds how long one run of the handler may take (default two minutes).
func WithTimeout(d time.Duration) Option {
	return func(c *consumer) {
		if d > 0 {
			c.timeout = d
		}
	}
}

type consumer struct {
	name    string
	handler Handler
	timeout time.Duration
}

// Outbox writes rows for registered consumers and runs their handlers.
type Outbox struct {
	db  *sql.DB
	log *slog.Logger

	mu     sync.RWMutex
	topics map[string][]*consumer // topic → consumers, in registration order

	nudge chan struct{}
	now   func() time.Time
	poll  time.Duration
}

// New makes an Outbox over db. Register every consumer before anything can Enqueue.
func New(db *sql.DB, log *slog.Logger) *Outbox {
	if log == nil {
		log = slog.Default()
	}
	return &Outbox{
		db:     db,
		log:    log,
		topics: map[string][]*consumer{},
		nudge:  make(chan struct{}, 1),
		now:    time.Now,
		poll:   defaultPoll,
	}
}

// Register adds a consumer of topic. Enqueue writes a row for every consumer registered
// for the topic at the time, so registration has to happen before producers run (main
// does it before the scheduler starts). Registering the same (topic, consumer) twice
// replaces the handler.
func (o *Outbox) Register(topic, name string, h Handler, opts ...Option) {
	c := &consumer{name: name, handler: h, timeout: defaultTimeout}
	for _, opt := range opts {
		opt(c)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	list := o.topics[topic]
	for i, existing := range list {
		if existing.name == name {
			list[i] = c
			return
		}
	}
	o.topics[topic] = append(list, c)
}

func (o *Outbox) consumers(topic string) []*consumer {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return append([]*consumer(nil), o.topics[topic]...)
}

func (o *Outbox) handler(topic, name string) *consumer {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, c := range o.topics[topic] {
		if c.name == name {
			return c
		}
	}
	return nil
}

// Enqueue writes one row per consumer registered for topic, through q — a *sql.Tx to land
// with the caller's own writes, or the *sql.DB. A topic nobody consumes writes nothing.
//
// dedupeKey ("" for none) collapses repeats: while a row for the same topic, consumer and
// key is still waiting, a new Enqueue replaces its payload and makes it due now instead of
// adding a second row. The key is scoped to the topic, so two topics can use the same one.
//
// The dispatcher is nudged straight away. Inside a transaction the row isn't visible until
// the caller commits; the next poll (two seconds at most) picks it up then.
func (o *Outbox) Enqueue(ctx context.Context, q store.Execer, topic string, payload any, dedupeKey string) error {
	cs := o.consumers(topic)
	if len(cs) == 0 {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outbox: encode %s payload: %w", topic, err)
	}
	key := ""
	if dedupeKey != "" {
		key = topic + "|" + dedupeKey
	}
	now := o.now().Unix()
	for _, c := range cs {
		// The conflict target names the partial index's condition, which is how SQLite
		// picks that index for an upsert. gen moves so a run already in flight with the old
		// payload can't mark the row done (see finish).
		if _, err := q.ExecContext(ctx,
			`INSERT INTO outbox (topic, consumer, payload, dedupe_key, created_at) VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT (consumer, dedupe_key) WHERE dedupe_key != '' AND done_at = 0 AND failed_at = 0
			 DO UPDATE SET payload = excluded.payload, next_at = 0, gen = gen + 1`,
			topic, c.name, string(body), key, now); err != nil {
			return fmt.Errorf("outbox: enqueue %s for %s: %w", topic, c.name, err)
		}
	}
	o.Nudge()
	return nil
}

// Nudge wakes the dispatcher early. It never blocks.
func (o *Outbox) Nudge() {
	select {
	case o.nudge <- struct{}{}:
	default:
	}
}

// Run drains due rows every couple of seconds, or as soon as Enqueue nudges it, until ctx
// ends. Start it once (main runs it in the run group, so a panic restarts it).
func (o *Outbox) Run(ctx context.Context) {
	t := time.NewTicker(o.poll)
	defer t.Stop()
	for {
		// Keep going while full batches come back, so a backlog clears without waiting
		// a poll between every twenty rows.
		for {
			n, err := o.Drain(ctx)
			if err != nil && ctx.Err() == nil {
				o.log.Warn("outbox: couldn't read due rows", "err", err)
			}
			if err != nil || n < defaultBatch || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-o.nudge:
		}
	}
}

type row struct {
	id       int64
	topic    string
	consumer string
	payload  string
	attempts int
	gen      int64
}

// Drain runs the handlers of up to one batch of due rows, oldest first, and reports how
// many rows it looked at. Run calls it; tests call it directly.
func (o *Outbox) Drain(ctx context.Context) (int, error) {
	rows, err := o.due(ctx)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if ctx.Err() != nil {
			return len(rows), ctx.Err()
		}
		o.process(ctx, r)
	}
	return len(rows), nil
}

func (o *Outbox) due(ctx context.Context) ([]row, error) {
	rs, err := o.db.QueryContext(ctx,
		`SELECT id, topic, consumer, payload, attempts, gen FROM outbox
		 WHERE done_at = 0 AND failed_at = 0 AND next_at <= ?
		 ORDER BY next_at, id LIMIT ?`, o.now().Unix(), defaultBatch)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.id, &r.topic, &r.consumer, &r.payload, &r.attempts, &r.gen); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// process runs one row's handler and records the outcome.
func (o *Outbox) process(ctx context.Context, r row) {
	c := o.handler(r.topic, r.consumer)
	if c == nil {
		// Registered once and not now (an older or newer build wrote it): keep the row,
		// retried on the usual schedule, so it runs if the consumer comes back and fails
		// visibly if it doesn't.
		o.finish(ctx, r, errors.New(errNoHandlerText))
		return
	}
	hctx, cancel := context.WithTimeout(ctx, c.timeout)
	err := safego.Call(o.log, "outbox "+r.topic+" → "+r.consumer, func() error {
		return c.handler(hctx, json.RawMessage(r.payload))
	})
	cancel()
	if err != nil && ctx.Err() != nil {
		return // shutting down: the handler was cut short, not refused; try again next start
	}
	o.finish(ctx, r, err)
}

// finish marks a row done, or schedules its retry. Both only apply while the row's gen
// is the one that was run: a newer enqueue in the meantime left it due now with the new
// payload, and it must run again rather than be closed by the stale run.
func (o *Outbox) finish(ctx context.Context, r row, runErr error) {
	now := o.now()
	if runErr == nil {
		if _, err := o.db.ExecContext(ctx,
			`UPDATE outbox SET done_at = ?, last_error = '' WHERE id = ? AND gen = ?`,
			now.Unix(), r.id, r.gen); err != nil {
			o.log.Warn("outbox: couldn't mark a row done — it will run again", "topic", r.topic, "consumer", r.consumer, "err", err)
		}
		return
	}
	attempts := r.attempts + 1
	failedAt := int64(0)
	if attempts >= maxAttempts {
		failedAt = now.Unix()
	}
	next := now.Add(Backoff(r.attempts)).Unix()
	msg := runErr.Error()
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	if _, err := o.db.ExecContext(ctx,
		`UPDATE outbox SET attempts = ?, last_error = ?, next_at = ?, failed_at = ? WHERE id = ? AND gen = ?`,
		attempts, msg, next, failedAt, r.id, r.gen); err != nil {
		o.log.Warn("outbox: couldn't record a failed run", "topic", r.topic, "consumer", r.consumer, "err", err)
		return
	}
	if failedAt != 0 {
		o.log.Error("outbox: gave up after repeated failures — retry it once the cause is fixed",
			"id", r.id, "topic", r.topic, "consumer", r.consumer, "attempts", attempts, "err", msg)
		return
	}
	o.log.Warn("outbox: handler failed — will retry", "id", r.id, "topic", r.topic, "consumer", r.consumer,
		"attempt", attempts, "next_in", Backoff(r.attempts).String(), "err", msg)
}

// Backoff is the wait before the next attempt after `failed` earlier failures: 30 s, then
// doubling, capped at an hour.
func Backoff(failed int) time.Duration {
	if failed < 0 {
		failed = 0
	}
	d := backoffMin
	for i := 0; i < failed; i++ {
		d *= 2
		if d >= backoffMax {
			return backoffMax
		}
	}
	return d
}

// Stats is how much is waiting and how much has given up, for the health page.
type Stats struct {
	Pending int `json:"pending"`
	Failed  int `json:"failed"`
}

// Stats counts waiting and failed rows.
func (o *Outbox) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	err := o.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(CASE WHEN failed_at = 0 THEN 1 ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN failed_at != 0 THEN 1 ELSE 0 END), 0)
		 FROM outbox WHERE done_at = 0`).Scan(&s.Pending, &s.Failed)
	return s, err
}

// Entry is one row as a person would want to see it.
type Entry struct {
	ID        int64  `json:"id"`
	Topic     string `json:"topic"`
	Consumer  string `json:"consumer"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error"`
	CreatedAt int64  `json:"created_at"`
	NextAt    int64  `json:"next_at"`
	FailedAt  int64  `json:"failed_at"`
}

// Unfinished lists rows not yet done — failed ones first — up to limit.
func (o *Outbox) Unfinished(ctx context.Context, limit int) ([]Entry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rs, err := o.db.QueryContext(ctx,
		`SELECT id, topic, consumer, attempts, last_error, created_at, next_at, failed_at FROM outbox
		 WHERE done_at = 0 ORDER BY failed_at DESC, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Entry
	for rs.Next() {
		var e Entry
		if err := rs.Scan(&e.ID, &e.Topic, &e.Consumer, &e.Attempts, &e.LastError, &e.CreatedAt, &e.NextAt, &e.FailedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rs.Err()
}

// ErrNotFound is Retry given an id that isn't an unfinished row.
var ErrNotFound = errors.New("outbox: no such waiting or failed row")

// Retry makes a waiting or failed row due now with a fresh attempt count. When a failed
// row's work is already queued again under the same key, that row will do it, and the
// failed one is closed instead of revived alongside it.
func (o *Outbox) Retry(ctx context.Context, id int64) error {
	res, err := o.db.ExecContext(ctx,
		`UPDATE OR IGNORE outbox SET attempts = 0, next_at = 0, failed_at = 0, last_error = '', gen = gen + 1
		 WHERE id = ? AND done_at = 0`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		o.Nudge()
		return nil
	}
	// Either no such row, or reviving it collided with a pending duplicate.
	res, err = o.db.ExecContext(ctx,
		`UPDATE outbox SET done_at = ?, last_error = 'superseded by a newer queued row' WHERE id = ? AND done_at = 0`,
		o.now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Prune deletes rows that finished more than olderThan ago and reports how many. Failed
// rows are kept: they're what a person needs to see.
func (o *Outbox) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	res, err := o.db.ExecContext(ctx,
		`DELETE FROM outbox WHERE done_at != 0 AND done_at < ?`, o.now().Add(-olderThan).Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Topics lists the registered (topic, consumer) pairs, sorted, for logging at boot.
func (o *Outbox) Topics() []string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	var out []string
	for t, cs := range o.topics {
		for _, c := range cs {
			out = append(out, t+" → "+c.name)
		}
	}
	sort.Strings(out)
	return out
}
