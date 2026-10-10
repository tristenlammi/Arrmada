package notify

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/safego"
)

// The delivery queue. Dispatch writes one notification_deliveries row per subscribed,
// enabled connection — a quick insert, so the bus loop never waits on a slow endpoint —
// and RunWorker sends them: up to three at once, one at a time per connection, each
// failure retried after 1, 5 and 30 minutes before the row is marked failed. The rows
// are also each connection's delivery log on the Alerts page.
//
// Why not internal/outbox: its dispatcher runs one handler at a time across every
// consumer, so a 20-second apprise timeout would hold up import side effects (the
// requester's "ready", Convert's index); its schedule (30 s doubling for 20 tries) is
// wrong for a phone ping; and a per-connection log needs a row per connection with its
// own status, cascading away with the connection. The pattern is the outbox's — a
// durable row, a nudge channel, crash recovery at start — on a table shaped for alerts.
//
// Delivery is at least once: a crash between a send and its row being marked sent
// sends it again after the restart. The dedupe key (DispatchOnce) makes the queueing
// exactly-once per connection; the message itself can, rarely, arrive twice.

// Delivery statuses.
const (
	StatusQueued  = "queued"
	StatusSending = "sending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
)

const (
	maxAttempts      = 4
	queueBatch       = 20
	queueConcurrency = 3
	sendTimeout      = time.Minute
	defaultQueuePoll = 2 * time.Second
	// HistoryKeep is how long finished deliveries (and so their dedupe keys) are kept.
	HistoryKeep = 30 * 24 * time.Hour
)

// retryAfter is the wait after the 1st, 2nd and 3rd failed attempt; the 4th fails the row.
var retryAfter = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

// Dispatch queues a message for one event to every enabled connection subscribed to it,
// and says how many rows that was. It's the single way alerts go out: Run, Emit and
// later producers all come through here.
func (s *Service) Dispatch(ctx context.Context, key string, m Message) (int, error) {
	return s.DispatchOnce(ctx, key, "", m)
}

// DispatchOnce is Dispatch with a dedupe key: a connection that already has a row with
// this key (queued, sent or failed, within HistoryKeep) isn't queued it again, so a
// producer can call it on every pass — after a restart, from two code paths — and each
// connection hears once. Use a key that names the occurrence, e.g.
// "request.created:42" or "health.problem:qbittorrent:<first_seen>". "" means no dedupe.
func (s *Service) DispatchOnce(ctx context.Context, key, dedupe string, m Message) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO notification_deliveries (connection_id, event_key, title, body, link, dedupe_key, created_at, next_attempt_at)
		 SELECT n.id, ?, ?, ?, ?, ?, ?, 0 FROM notifications n
		 JOIN notification_subscriptions sub ON sub.connection_id = n.id AND sub.event_key = ?
		 WHERE n.enabled != 0`,
		key, m.Title, m.Body, m.Link, dedupe, s.now().Unix(), key)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.Wake()
	}
	return int(n), nil
}

// Wake nudges the worker to look for due rows now. It never blocks.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// worker is RunWorker's bookkeeping: which connections have a send in flight.
type worker struct {
	mu       sync.Mutex
	inflight map[int64]bool
	running  int
	wg       sync.WaitGroup
}

func newWorker() *worker { return &worker{inflight: map[int64]bool{}} }

// RunWorker sends queued deliveries until ctx ends, then waits for sends in flight.
// Start it once (main runs it in the run group).
func (s *Service) RunWorker(ctx context.Context) {
	// A row left "sending" by a crash or a kill was never confirmed: send it again.
	if _, err := s.db.ExecContext(ctx, `UPDATE notification_deliveries SET status = ? WHERE status = ?`, StatusQueued, StatusSending); err != nil && ctx.Err() == nil {
		s.log.Warn("notify: couldn't requeue interrupted deliveries", "err", err)
	}
	w := newWorker()
	defer w.wg.Wait()
	t := time.NewTicker(s.poll)
	defer t.Stop()
	for {
		if err := s.startDue(ctx, w); err != nil && ctx.Err() == nil {
			s.log.Warn("notify: couldn't read the delivery queue", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

type delivery struct {
	id, connID int64
	event      string
	msg        Message
	attempts   int
}

// startDue claims due rows and starts their sends, within the concurrency limit and
// never two for the same connection at once (a slow or dead endpoint holds up only its
// own alerts). Rows it can't start now stay queued for the next pass.
func (s *Service) startDue(ctx context.Context, w *worker) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, connection_id, event_key, title, body, link, attempts FROM notification_deliveries
		 WHERE status = ? AND next_attempt_at <= ? ORDER BY next_attempt_at, id LIMIT ?`,
		StatusQueued, s.now().Unix(), queueBatch)
	if err != nil {
		return err
	}
	var due []delivery
	for rows.Next() {
		var d delivery
		if err := rows.Scan(&d.id, &d.connID, &d.event, &d.msg.Title, &d.msg.Body, &d.msg.Link, &d.attempts); err != nil {
			rows.Close()
			return err
		}
		due = append(due, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range due {
		w.mu.Lock()
		if w.running >= queueConcurrency || w.inflight[d.connID] {
			w.mu.Unlock()
			continue
		}
		res, err := s.db.ExecContext(ctx, `UPDATE notification_deliveries SET status = ? WHERE id = ? AND status = ?`, StatusSending, d.id, StatusQueued)
		if err != nil {
			w.mu.Unlock()
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			w.mu.Unlock()
			continue
		}
		w.inflight[d.connID] = true
		w.running++
		w.wg.Add(1)
		w.mu.Unlock()
		safego.Go(s.log, "notify: send", func() {
			defer func() {
				w.mu.Lock()
				delete(w.inflight, d.connID)
				w.running--
				w.mu.Unlock()
				w.wg.Done()
				s.Wake() // a slot is free; the next row for this connection may be due
			}()
			s.sendOne(ctx, d)
		})
	}
	return nil
}

// sendOne delivers one row and records how it went.
func (s *Service) sendOne(ctx context.Context, d delivery) {
	// The outcome is written even when shutdown cut the send short.
	bg := context.WithoutCancel(ctx)
	c, err := s.Get(ctx, d.connID)
	if errors.Is(err, ErrNotFound) {
		return // deleted meanwhile; its rows went with it
	}
	if err == nil && !c.Enabled {
		s.finishDelivery(bg, d, errConnectionDisabled, true)
		return
	}
	if err == nil {
		sctx, cancel := context.WithTimeout(ctx, sendTimeout)
		err = s.deliver(sctx, c, d.msg)
		cancel()
	}
	if err != nil && ctx.Err() != nil {
		// Shutting down: the send was cut short, not refused. Leave it for next start.
		_, _ = s.db.ExecContext(bg, `UPDATE notification_deliveries SET status = ? WHERE id = ?`, StatusQueued, d.id)
		return
	}
	if err != nil {
		s.log.Warn("notify: delivery failed", "connection", c.Name, "event", d.event, "attempt", d.attempts+1, "err", err)
	}
	s.finishDelivery(bg, d, err, false)
}

var errConnectionDisabled = errors.New("connection disabled")

// finishDelivery marks a row sent, schedules its retry, or fails it for good.
func (s *Service) finishDelivery(ctx context.Context, d delivery, sendErr error, final bool) {
	now := s.now()
	attempts := d.attempts + 1
	var err error
	switch {
	case sendErr == nil:
		_, err = s.db.ExecContext(ctx,
			`UPDATE notification_deliveries SET status = ?, attempts = ?, last_error = '', sent_at = ? WHERE id = ?`,
			StatusSent, attempts, now.Unix(), d.id)
	case final || attempts >= maxAttempts:
		if sendErr == errConnectionDisabled {
			attempts = d.attempts
		}
		_, err = s.db.ExecContext(ctx,
			`UPDATE notification_deliveries SET status = ?, attempts = ?, last_error = ? WHERE id = ?`,
			StatusFailed, attempts, clip(sendErr.Error()), d.id)
	default:
		_, err = s.db.ExecContext(ctx,
			`UPDATE notification_deliveries SET status = ?, attempts = ?, last_error = ?, next_attempt_at = ? WHERE id = ?`,
			StatusQueued, attempts, clip(sendErr.Error()), now.Add(retryAfter[attempts-1]).Unix(), d.id)
	}
	if err != nil {
		s.log.Warn("notify: couldn't record a delivery outcome", "id", d.id, "err", err)
	}
}

func clip(s string) string {
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

// recordTest logs a Test press on a saved connection in its delivery history, so the
// card's status dot reflects it.
func (s *Service) recordTest(ctx context.Context, c Connection, m Message, sendErr error) {
	status, errText, sentAt := StatusSent, "", s.now().Unix()
	if sendErr != nil {
		status, errText, sentAt = StatusFailed, clip(sendErr.Error()), 0
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO notification_deliveries (connection_id, event_key, title, body, link, status, attempts, last_error, created_at, sent_at)
		 VALUES (?, 'test', ?, ?, ?, ?, 1, ?, ?, ?)`,
		c.ID, m.Title, m.Body, m.Link, status, errText, s.now().Unix(), sentAt); err != nil {
		s.log.Warn("notify: couldn't record a test delivery", "connection", c.Name, "err", err)
	}
}

// Delivery is one row of a connection's delivery log.
type Delivery struct {
	ID            int64  `json:"id"`
	EventKey      string `json:"event_key"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	Status        string `json:"status"`
	Attempts      int    `json:"attempts"`
	LastError     string `json:"last_error"`
	CreatedAt     int64  `json:"created_at"`
	NextAttemptAt int64  `json:"next_attempt_at"`
	SentAt        int64  `json:"sent_at"`
}

// Deliveries lists a connection's most recent deliveries, newest first.
func (s *Service) Deliveries(ctx context.Context, connID int64, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, event_key, title, body, status, attempts, last_error, created_at, next_attempt_at, sent_at
		 FROM notification_deliveries WHERE connection_id = ? ORDER BY id DESC LIMIT ?`, connID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.EventKey, &d.Title, &d.Body, &d.Status, &d.Attempts, &d.LastError, &d.CreatedAt, &d.NextAttemptAt, &d.SentAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeliveryState is a connection's latest outcome, for its status dot: Status is "" (never
// sent), "sent", "failed", or "retrying" (the last try failed and another is due).
type DeliveryState struct {
	Status     string `json:"last_status"`
	Error      string `json:"last_error,omitempty"`
	LastSentAt int64  `json:"last_sent_at"`
}

// DeliveryStates gives every connection's latest outcome in one query.
func (s *Service) DeliveryStates(ctx context.Context) (map[int64]DeliveryState, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT d.connection_id, d.status, d.attempts, d.last_error,
		        (SELECT COALESCE(MAX(sent_at), 0) FROM notification_deliveries x WHERE x.connection_id = d.connection_id)
		 FROM notification_deliveries d
		 JOIN (SELECT connection_id, MAX(id) AS id FROM notification_deliveries
		       WHERE status IN (?, ?) OR (status = ? AND attempts > 0)
		       GROUP BY connection_id) latest ON latest.id = d.id`,
		StatusSent, StatusFailed, StatusQueued)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]DeliveryState{}
	for rows.Next() {
		var (
			id       int64
			st       DeliveryState
			attempts int
		)
		if err := rows.Scan(&id, &st.Status, &attempts, &st.Error, &st.LastSentAt); err != nil {
			return nil, err
		}
		if st.Status == StatusQueued {
			st.Status = "retrying"
		}
		if st.Status == StatusSent {
			st.Error = ""
		}
		out[id] = st
	}
	return out, rows.Err()
}

// PruneDeliveries deletes finished deliveries older than olderThan and says how many.
func (s *Service) PruneDeliveries(ctx context.Context, olderThan time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM notification_deliveries WHERE status IN (?, ?) AND created_at < ?`,
		StatusSent, StatusFailed, s.now().Add(-olderThan).Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// failQueued fails a connection's waiting rows — it was switched off.
func failQueued(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, connID int64) error {
	_, err := q.ExecContext(ctx,
		`UPDATE notification_deliveries SET status = ?, last_error = ? WHERE connection_id = ? AND status = ?`,
		StatusFailed, errConnectionDisabled.Error(), connID, StatusQueued)
	return err
}
