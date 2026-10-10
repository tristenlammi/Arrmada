package attention

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/notify"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The Needs-you alerts. After every refresh the Alerter compares the feed with what it
// has already seen and said (the attention_state table) and sends:
//
//   - one alert when a problem appears and has settled — present in two refreshes in a
//     row and for a minute, so something that flickers for a moment says nothing;
//   - one "Resolved" when a health problem it announced has cleared (gone for two
//     refreshes and two minutes). Other kinds clear because someone dealt with them, and
//     hearing so would only be noise;
//   - nothing again for the same problem: not on the next refresh, not after a restart.
//
// Noise guards, all part of the design rather than tuning:
//   - The first start with alerts (a fresh install or the upgrade that adds them) treats
//     everything seen in its first five minutes as already known.
//   - After any start, health problems wait five minutes (a client still booting is not
//     a problem) and nothing counts as cleared yet (sources are still waking up).
//   - A problem that clears and comes back within six hours of its alert isn't
//     announced again.
//   - More than five of one kind at once go as one summary ("6 downloads failed: A, B,
//     C and 3 more"), and no kind sends more than five messages in fifteen minutes: past
//     that, what's new waits and goes together when the window has room.
//
// Pending requests aren't alerted from here: request.created announces each new request
// itself. Stuck searches (one aggregate count) aren't either.
//
// Exactly once: a decision (who is told what) is written to the table first, with the
// dedupe key it will be sent under, and then handed to notify.EmitOnce. A crash between
// the two leaves the decision pending; the next refresh hands it over again and notify's
// dedupe keeps it to one queued message per connection. The dedupe keys name the
// occurrence by its first_seen in this table, so they don't depend on the feed's "since",
// which for most sources only the running process knows.

// Emitter queues a catalog event exactly once per dedupe key (notify.Service).
type Emitter interface {
	EmitOnce(ctx context.Context, key, dedupe string, data map[string]any) (int, error)
}

// Flags is where the one-time "alerts have been seeded" mark is kept (the settings
// service).
type Flags interface {
	GetBool(ctx context.Context, key string, def bool) bool
	SetBool(ctx context.Context, key string, value bool) error
}

// SeededKey is the setting that marks the first start with alerts as done.
const SeededKey = "attention_alerts_seeded"

const (
	settleRuns   = 2
	settleFor    = time.Minute
	bootGrace    = 5 * time.Minute
	resolveRuns  = 2
	resolveAfter = 2 * time.Minute
	flapGuard    = 6 * time.Hour
	batchAbove   = 5
	floodWindow  = 15 * time.Minute
	floodBudget  = 5 // messages per event per floodWindow
	pruneAfter   = 7 * 24 * time.Hour
)

// What the owner last heard about an item.
const (
	toldNone     = ""
	toldProblem  = "problem"
	toldResolved = "resolved"
	toldQuiet    = "quiet" // there before alerts started: never announced, so never resolved either
)

// eventFor is the alert an item raises, "" for none.
func eventFor(it Item) string {
	switch it.Kind {
	case KindReview:
		return notify.EventImportHeld
	case KindImport:
		// The second failed try is worth a badge, not a ping: most recover on the next.
		if it.Level == LevelError {
			return notify.EventImportStuck
		}
	case KindWrongCat:
		return notify.EventImportStuck
	case KindDownload:
		return notify.EventDownloadFailed
	case KindStalled:
		return notify.EventDownloadStalled
	case KindHealth:
		return notify.EventHealthProblem
	}
	return ""
}

// Alerter turns the attention feed into alerts. Its Diff runs at the end of every
// refresh (Service.SetAlerts).
type Alerter struct {
	db    *sql.DB
	emit  Emitter
	flags Flags
	log   *slog.Logger

	// now and bootAt are the clock and when this process started; tests set both.
	now    func() time.Time
	bootAt time.Time

	mu     sync.Mutex
	seeded *bool                  // read from flags on the first diff
	sent   map[string][]time.Time // messages per event within floodWindow (this process)
}

// NewAlerter builds the alerter. Call it at start-up: the boot grace counts from now.
func NewAlerter(db *sql.DB, emit Emitter, flags Flags, log *slog.Logger) *Alerter {
	if log == nil {
		log = slog.Default()
	}
	now := time.Now()
	return &Alerter{db: db, emit: emit, flags: flags, log: log, now: time.Now, bootAt: now, sent: map[string][]time.Time{}}
}

// alertRow is one attention_state row.
type alertRow struct {
	key, kind, event, level, name, title, detail, link string
	firstSeen, lastSeen                                int64
	runs, misses                                       int
	told                                               string
	alertedAt, resolvedAt                              int64
	pendingEvent, pendingDedupe                        string

	dirty bool
}

const stateCols = `key, kind, event, level, name, title, detail, link, first_seen, last_seen, runs, misses,
	told, alerted_at, resolved_at, pending_event, pending_dedupe`

func scanRow(sc interface{ Scan(...any) error }) (*alertRow, error) {
	r := &alertRow{}
	err := sc.Scan(&r.key, &r.kind, &r.event, &r.level, &r.name, &r.title, &r.detail, &r.link, &r.firstSeen, &r.lastSeen,
		&r.runs, &r.misses, &r.told, &r.alertedAt, &r.resolvedAt, &r.pendingEvent, &r.pendingDedupe)
	return r, err
}

// Diff compares one refresh's items with the table and sends what's due.
func (a *Alerter) Diff(ctx context.Context, items []Item) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	ms := now.UnixMilli()

	// Whatever an earlier diff decided but couldn't hand over goes first. A failure here
	// doesn't stop this diff: those rows stay pending and are left alone until it works.
	flushErr := a.flush(ctx)

	rows, err := a.load(ctx)
	if err != nil {
		return err
	}
	seeding := a.seeding(ctx, now)
	grace := now.Sub(a.bootAt) < bootGrace

	present := map[string]bool{}
	for _, it := range items {
		ev := eventFor(it)
		if ev == "" || it.Key == "" || present[it.Key] {
			continue
		}
		present[it.Key] = true
		r := rows[it.Key]
		switch {
		case r == nil:
			r = &alertRow{key: it.Key, firstSeen: ms}
			rows[it.Key] = r
		case r.resolvedAt > 0:
			// Back after clearing: a new occurrence. Whether it's announced is the flap
			// guard's call below.
			r.resolvedAt, r.firstSeen, r.runs = 0, ms, 0
		}
		name := it.Name
		if name == "" {
			name = it.Title
		}
		r.kind, r.event, r.level, r.name, r.title, r.detail, r.link = it.Kind, ev, it.Level, name, it.Title, it.Detail, it.Link
		r.lastSeen, r.misses = ms, 0
		r.runs++
		r.dirty = true
	}

	for _, r := range rows {
		if present[r.key] || r.resolvedAt > 0 {
			continue
		}
		if r.runs != 0 {
			r.runs, r.dirty = 0, true
		}
		if grace || r.pendingDedupe != "" {
			continue // nothing clears while sources are still waking up, or mid-send
		}
		r.misses++
		r.dirty = true
		if r.misses < resolveRuns || ms-r.lastSeen < resolveAfter.Milliseconds() {
			continue
		}
		r.resolvedAt = ms
		switch {
		case r.told == toldProblem && r.kind != KindHealth:
			r.told = toldResolved
		case r.told == toldQuiet:
			r.told = toldNone
		}
		// A health problem the owner was told about keeps told=problem until its
		// "Resolved" goes out below.
	}

	due := map[string][]*alertRow{}
	for _, r := range rows {
		if r.pendingDedupe != "" {
			continue
		}
		if r.resolvedAt > 0 {
			if r.told == toldProblem && r.kind == KindHealth {
				due[notify.EventHealthResolved] = append(due[notify.EventHealthResolved], r)
			}
			continue
		}
		if !present[r.key] || r.told == toldProblem || r.told == toldQuiet {
			continue
		}
		if seeding {
			r.told, r.alertedAt, r.dirty = toldQuiet, ms, true
			continue
		}
		if r.runs < settleRuns || ms-r.firstSeen < settleFor.Milliseconds() || (r.kind == KindHealth && grace) {
			continue
		}
		if r.alertedAt > 0 && ms-r.alertedAt < flapGuard.Milliseconds() {
			continue
		}
		due[r.event] = append(due[r.event], r)
	}
	a.decide(due, now)

	if err := a.save(ctx, rows); err != nil {
		return err
	}
	if err := a.flush(ctx); err != nil {
		return err
	}
	if _, err := a.db.ExecContext(ctx,
		`DELETE FROM attention_state WHERE resolved_at > 0 AND resolved_at < ? AND told != ? AND pending_dedupe = ''`,
		now.Add(-pruneAfter).UnixMilli(), toldProblem); err != nil {
		return err
	}
	return flushErr
}

// decide marks what goes out now, within the flood limits: each row gets the event and
// dedupe key it will be sent under.
func (a *Alerter) decide(due map[string][]*alertRow, now time.Time) {
	ms := now.UnixMilli()
	events := make([]string, 0, len(due))
	for ev := range due {
		events = append(events, ev)
	}
	sort.Strings(events)
	for _, ev := range events {
		rs := due[ev]
		sort.Slice(rs, func(i, j int) bool {
			if rs[i].firstSeen != rs[j].firstSeen {
				return rs[i].firstSeen < rs[j].firstSeen
			}
			return rs[i].key < rs[j].key
		})
		budget := a.budget(ev, now)
		switch {
		case budget == 0:
			// A flood: they stay due and go out together once the window has room.
			a.log.Debug("attention: holding alerts back — too many of one kind in a short time", "event", ev, "count", len(rs))
		case len(rs) > batchAbove || len(rs) > budget:
			mark(rs, ev, fmt.Sprintf("%s:batch:%d:%s", ev, ms, rs[0].key), ms)
			a.spend(ev, now)
		default:
			for _, r := range rs {
				mark([]*alertRow{r}, ev, fmt.Sprintf("%s:%s:%d", ev, r.key, r.firstSeen), ms)
				a.spend(ev, now)
			}
		}
	}
}

func mark(rs []*alertRow, ev, dedupe string, ms int64) {
	for _, r := range rs {
		r.pendingEvent, r.pendingDedupe, r.dirty = ev, dedupe, true
		if ev == notify.EventHealthResolved {
			r.told = toldResolved
		} else {
			r.told, r.alertedAt = toldProblem, ms
		}
	}
}

// budget is how many more messages ev may send in the current window.
func (a *Alerter) budget(ev string, now time.Time) int {
	kept := a.sent[ev][:0]
	for _, t := range a.sent[ev] {
		if now.Sub(t) < floodWindow {
			kept = append(kept, t)
		}
	}
	a.sent[ev] = kept
	return max(floodBudget-len(kept), 0)
}

func (a *Alerter) spend(ev string, now time.Time) { a.sent[ev] = append(a.sent[ev], now) }

// seeding reports whether this is the first start with alerts, still inside its grace:
// everything seen now is taken as already known. Once the grace has passed the mark is
// saved and seeding never happens again.
func (a *Alerter) seeding(ctx context.Context, now time.Time) bool {
	if a.seeded == nil {
		v := a.flags == nil || a.flags.GetBool(ctx, SeededKey, false)
		a.seeded = &v
	}
	if *a.seeded {
		return false
	}
	if now.Sub(a.bootAt) < bootGrace {
		return true
	}
	if err := a.flags.SetBool(ctx, SeededKey, true); err != nil {
		a.log.Warn("attention: couldn't save that alerts are set up", "err", err)
	}
	*a.seeded = true
	return false
}

func (a *Alerter) load(ctx context.Context) (map[string]*alertRow, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT `+stateCols+` FROM attention_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*alertRow{}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out[r.key] = r
	}
	return out, rows.Err()
}

func (a *Alerter) save(ctx context.Context, rows map[string]*alertRow) error {
	return store.WithTx(ctx, a.db, func(tx *sql.Tx) error {
		for _, r := range rows {
			if !r.dirty {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO attention_state (`+stateCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(key) DO UPDATE SET kind = excluded.kind, event = excluded.event, level = excluded.level,
				   name = excluded.name, title = excluded.title, detail = excluded.detail, link = excluded.link,
				   first_seen = excluded.first_seen, last_seen = excluded.last_seen, runs = excluded.runs,
				   misses = excluded.misses, told = excluded.told, alerted_at = excluded.alerted_at,
				   resolved_at = excluded.resolved_at, pending_event = excluded.pending_event,
				   pending_dedupe = excluded.pending_dedupe`,
				r.key, r.kind, r.event, r.level, r.name, r.title, r.detail, r.link, r.firstSeen, r.lastSeen, r.runs, r.misses,
				r.told, r.alertedAt, r.resolvedAt, r.pendingEvent, r.pendingDedupe); err != nil {
				return err
			}
		}
		return nil
	})
}

// flush hands every pending decision to the delivery queue, one message per dedupe key,
// and clears it once queued. A group that fails stays pending for the next diff.
func (a *Alerter) flush(ctx context.Context) error {
	rows, err := a.db.QueryContext(ctx,
		`SELECT `+stateCols+` FROM attention_state WHERE pending_dedupe != '' ORDER BY pending_dedupe, first_seen, key`)
	if err != nil {
		return err
	}
	var groups [][]*alertRow
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			rows.Close()
			return err
		}
		if n := len(groups); n > 0 && groups[n-1][0].pendingDedupe == r.pendingDedupe {
			groups[n-1] = append(groups[n-1], r)
		} else {
			groups = append(groups, []*alertRow{r})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var firstErr error
	for _, g := range groups {
		ev, dedupe := g[0].pendingEvent, g[0].pendingDedupe
		if _, err := a.emit.EmitOnce(ctx, ev, dedupe, alertPayload(g)); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("queue %s alert: %w", ev, err)
			}
			continue
		}
		if _, err := a.db.ExecContext(ctx,
			`UPDATE attention_state SET pending_event = '', pending_dedupe = '' WHERE pending_dedupe = ?`, dedupe); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		a.log.Info("attention: alert queued", "event", ev, "items", len(g))
	}
	return firstErr
}

// alertPayload is the catalog payload for one message (see notify/catalog_needsyou.go).
func alertPayload(g []*alertRow) map[string]any {
	if len(g) == 1 {
		r := g[0]
		return map[string]any{"count": 1, "title": r.title, "detail": r.detail, "link": r.link, "level": r.level}
	}
	names := make([]string, 0, len(g))
	level := LevelWarning
	for _, r := range g {
		names = append(names, r.name)
		if r.level == LevelError {
			level = LevelError
		}
	}
	link := g[0].link
	if f := groupLinks[g[0].kind]; f != nil {
		link = f.Path
	}
	return map[string]any{"count": len(g), "names": names, "link": link, "level": level}
}
