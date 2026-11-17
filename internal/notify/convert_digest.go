package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
)

// ConvertAlerts turns Convert's bus events (internal/convert/events.go) into the Convert
// alerts: "gave up on a file" at once, and one summary a day instead of a message per file.
// (Convert being stuck for space is the Needs-you feed's to announce; see
// catalog_convert.go.)
//
// The summary's counts live in a setting, rewritten on every event, so a restart mid-day
// neither loses them nor sends a second summary: the day it went out is stored with them,
// and its dedupe key names the date as well.
//
// The bus can drop an event under load. That costs at most a file missing from a day's
// count or one "gave up" ping; the Problems tab on the Convert page stays the full record.
type ConvertAlerts struct {
	emit  Emitter
	store DigestStore
	log   *slog.Logger
	now   func() time.Time

	mu     sync.Mutex
	state  digestState
	loaded bool
}

// Emitter queues a catalog event exactly once per dedupe key (*Service).
type Emitter interface {
	EmitOnce(ctx context.Context, key, dedupe string, data map[string]any) (int, error)
}

// DigestStore keeps the summary's running counts (the settings service).
type DigestStore interface {
	Get(ctx context.Context, key, def string) string
	Set(ctx context.Context, key, value string) error
}

// DigestKey is the setting the daily summary's counts are kept in.
const DigestKey = "notify_convert_digest"

// digestHour is the local hour from which the day's summary may go.
const digestHour = 9

// maxProblemKeys bounds the problem list kept in the setting. Past it, problems are still
// counted, just no longer told apart.
const maxProblemKeys = 500

// digestState is what has happened since the last summary.
type digestState struct {
	Files      int64 `json:"files"`
	SavedBytes int64 `json:"saved_bytes"`
	// ProblemKeys are the files that failed or stalled, each once ("movie:12").
	ProblemKeys []string `json:"problem_keys,omitempty"`
	// seen is a lazily-built set of ProblemKeys for O(1) duplicate checks. It is
	// rebuilt from ProblemKeys on first use and is not serialized.
	seen map[string]struct{} `json:"-"`
	// ExtraProblems counts problems past maxProblemKeys.
	ExtraProblems int64 `json:"extra_problems,omitempty"`
	// Since is when the first of these happened (unix seconds; 0 = nothing yet).
	Since int64 `json:"since,omitempty"`
	// LastSent is the local date ("2026-10-12") of the last summary sent.
	LastSent string `json:"last_sent,omitempty"`
}

// seenSet returns the set of ProblemKeys, building it from the slice on first use.
// It is not serialized; it is a fast path for duplicate checks only.
func (st *digestState) seenSet() map[string]struct{} {
	if st.seen == nil {
		st.seen = make(map[string]struct{}, len(st.ProblemKeys))
		for _, k := range st.ProblemKeys {
			st.seen[k] = struct{}{}
		}
	}
	return st.seen
}

func (st digestState) problems() int64 { return int64(len(st.ProblemKeys)) + st.ExtraProblems }

// The bus topics Convert publishes (internal/convert/events.go). Named here by value:
// notify doesn't import convert.
const (
	topicConvertDone    = "convert.done"
	topicConvertFailed  = "convert.failed"
	topicConvertBlocked = "convert.blocked"
	topicConvertStalled = "convert.stalled"
)

// NewConvertAlerts wires the Convert alerts to the alert service and where the summary's
// counts are kept.
func NewConvertAlerts(emit Emitter, store DigestStore, log *slog.Logger) *ConvertAlerts {
	return &ConvertAlerts{emit: emit, store: store, log: log, now: time.Now}
}

// Run listens to Convert's events until ctx ends. Start it once at boot.
func (c *ConvertAlerts) Run(ctx context.Context, bus *eventbus.Bus) {
	events, cancel := bus.Subscribe(topicConvertDone, topicConvertFailed, topicConvertBlocked, topicConvertStalled)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			c.Handle(ctx, ev.Topic, payload(ev.Data))
		}
	}
}

// Handle takes one Convert event: counts it toward the summary, and sends "gave up" for a
// blocked file.
func (c *ConvertAlerts) Handle(ctx context.Context, topic string, d map[string]any) {
	if d == nil {
		return
	}
	switch topic {
	case topicConvertDone:
		c.update(ctx, func(st *digestState) {
			st.Files++
			st.SavedBytes += max(num(d, "saved_bytes"), 0)
		})
	case topicConvertFailed:
		// A full disk or a vanished source clears on its own and is announced as a stall
		// (or not at all); only real failures are a problem worth counting.
		if b, _ := d["transient"].(bool); b {
			return
		}
		c.update(ctx, func(st *digestState) { st.addProblem(str(d, "key")) })
	case topicConvertStalled:
		c.update(ctx, func(st *digestState) { st.addProblem(str(d, "key")) })
	case topicConvertBlocked:
		// A problem for the summary too (once per file: a failure of it may already have
		// counted — or not, when its tries were encodes that didn't pay off). The dedupe
		// names the file and the moment it was blocked: "Try again" followed by three more
		// failures is a new occurrence and is announced again; a replayed event is not.
		key := str(d, "key")
		c.update(ctx, func(st *digestState) { st.addProblem(key) })
		dedupe := "convert.blocked:" + key + ":" + strconv.FormatInt(num(d, "at"), 10)
		if _, err := c.emit.EmitOnce(ctx, EventConvertFailed, dedupe, d); err != nil && ctx.Err() == nil {
			c.log.Warn("notify: couldn't queue the Convert gave-up alert", "item", key, "err", err)
		}
	}
}

func (st *digestState) addProblem(key string) {
	if key == "" {
		st.ExtraProblems++
		return
	}
	// seenSet rebuilds from the slice on first use, so state loaded from an older
	// JSON (no map) still dedupes correctly.
	seen := st.seenSet()
	if _, ok := seen[key]; ok {
		return
	}
	if len(st.ProblemKeys) >= maxProblemKeys {
		st.ExtraProblems++
		return
	}
	st.ProblemKeys = append(st.ProblemKeys, key)
	seen[key] = struct{}{}
}

// update changes the counts and saves them at once.
func (c *ConvertAlerts) update(ctx context.Context, fn func(*digestState)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked(ctx)
	fn(&c.state)
	if c.state.Since == 0 {
		c.state.Since = c.now().Unix()
	}
	c.saveLocked(ctx)
}

func (c *ConvertAlerts) loadLocked(ctx context.Context) {
	if c.loaded {
		return
	}
	c.loaded = true
	raw := c.store.Get(ctx, DigestKey, "")
	if raw == "" {
		return
	}
	if err := json.Unmarshal([]byte(raw), &c.state); err != nil {
		c.log.Warn("notify: the Convert summary's saved counts were unreadable — starting again", "err", err)
		c.state = digestState{}
	}
}

func (c *ConvertAlerts) saveLocked(ctx context.Context) {
	b, _ := json.Marshal(c.state)
	if err := c.store.Set(ctx, DigestKey, string(b)); err != nil {
		c.log.Warn("notify: couldn't save the Convert summary's counts", "err", err)
	}
}

// SendDigest sends the day's summary once local time is past digestHour, when there is
// something to say and none has gone today, then starts the counts again. The
// "convert-digest" scheduler task runs it every 15 minutes.
func (c *ConvertAlerts) SendDigest(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked(ctx)
	now := c.now()
	today := now.Format("2006-01-02")
	st := c.state
	if now.Hour() < digestHour || st.LastSent == today || (st.Files == 0 && st.problems() == 0) {
		return nil
	}
	data := map[string]any{"files": st.Files, "saved_bytes": st.SavedBytes, "problems": st.problems(), "since": st.Since}
	// The date in the dedupe key keeps it to one a day even if saving the reset fails.
	if _, err := c.emit.EmitOnce(ctx, EventConvertSummary, "convert.summary:"+today, data); err != nil {
		return fmt.Errorf("queue the Convert summary: %w", err)
	}
	c.state = digestState{LastSent: today}
	c.saveLocked(ctx)
	return nil
}
