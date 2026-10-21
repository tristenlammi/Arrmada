package convert

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// The hold's budget. Held originals take no new space — they're the files that were
// already on the disk — but they don't free any either, and the owner needs a ceiling and
// a view of it. When the next original wouldn't fit under the budget, conversions PAUSE
// (hold_full) until something is released — by its undo window ending, or by you. The hold
// is never emptied to make room.

// SkipHoldFull: the originals hold has no room under its budget for this file's original.
// It waits until the next release (retry_after is the earliest hold_until), and a release
// or a budget change retries it at once.
const SkipHoldFull = "hold_full"

// holdBudget is the hold's ceiling in bytes; 0 = no limit.
func (s *Service) holdBudget(ctx context.Context) int64 {
	gb, err := strconv.ParseFloat(strings.TrimSpace(s.settings.Get(ctx, keyHoldMaxGB, strconv.Itoa(defaultHoldMaxGB))), 64)
	if err != nil || gb < 0 || math.IsNaN(gb) {
		gb = defaultHoldMaxGB
	}
	return int64(gb * (1 << 30))
}

// holdMaxGB is the budget setting as saved, for the settings page.
func (s *Service) holdMaxGB(ctx context.Context) int {
	return int(s.holdBudget(ctx) / (1 << 30))
}

// HoldStats is what the hold holds, for the Overview's Space saved card and Settings.
type HoldStats struct {
	HeldBytes       int64 `json:"held_bytes"`        // originals waiting in the hold
	HeldSavingBytes int64 `json:"held_saving_bytes"` // what releasing them all would free (Σ original − converted)
	HeldCount       int   `json:"held_count"`
	NextReleaseAt   int64 `json:"next_release_at,omitempty"` // the earliest hold_until; 0 = none held
	BudgetBytes     int64 `json:"budget_bytes"`              // 0 = no limit
	HoldDays        int   `json:"hold_days"`
}

// HoldStats sums the hold from the ledger.
func (s *Service) HoldStats(ctx context.Context) HoldStats {
	st := HoldStats{BudgetBytes: s.holdBudget(ctx), HoldDays: s.holdDays(ctx)}
	if s.history == nil || s.history.db == nil {
		return st
	}
	_ = s.history.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(src_size), 0), COALESCE(SUM(`+store.ScalarMax(s.history.db, "src_size - out_size", "0")+`), 0), COUNT(*), COALESCE(MIN(NULLIF(hold_until, 0)), 0)
		   FROM convert_history WHERE hold_state = ?`, HoldHeld).
		Scan(&st.HeldBytes, &st.HeldSavingBytes, &st.HeldCount, &st.NextReleaseAt)
	return st
}

// holdFits reports whether an original of size bytes fits in the hold under its budget, and
// when it doesn't, why (for Problems and the status line) and when to look again.
func (s *Service) holdFits(ctx context.Context, size int64) (ok bool, why string, retryAt int64) {
	st := s.HoldStats(ctx)
	if st.BudgetBytes <= 0 || st.HeldBytes+size <= st.BudgetBytes {
		return true, "", 0
	}
	if size > st.BudgetBytes {
		return false, fmt.Sprintf("its original (%s) is bigger than the whole originals hold (%s) — raise the hold's size in Convert's settings, under Undo",
			humanBytes(size), humanBytes(st.BudgetBytes)), time.Now().Add(24 * time.Hour).Unix()
	}
	full := fmt.Sprintf("the originals hold is full (%s of %s)", humanBytes(st.HeldBytes), humanBytes(st.BudgetBytes))
	if st.NextReleaseAt > 0 {
		return false, fmt.Sprintf("%s — this converts after the next original is released on %s, or raise the hold's size or release some in History",
			full, time.Unix(st.NextReleaseAt, 0).Format("2 Jan")), st.NextReleaseAt
	}
	return false, full + " — raise the hold's size or release some in History", time.Now().Add(24 * time.Hour).Unix()
}

// holdTooFull is the runner's pre-filter: a candidate whose original wouldn't fit under the
// budget is passed over without being started, probed or logged — a full hold would
// otherwise skip (and log) every big file in turn on each pick.
func (s *Service) holdTooFull(ctx context.Context) func(autoCand) bool {
	st := s.HoldStats(ctx)
	if st.BudgetBytes <= 0 || st.HoldDays == 0 {
		return func(autoCand) bool { return false }
	}
	return func(c autoCand) bool { return st.HeldBytes+c.Size > st.BudgetBytes }
}

// holdPausedMessage is the status line while the next file waits on the hold ("" when it
// fits).
func (s *Service) holdPausedMessage(ctx context.Context, size int64) string {
	st := s.HoldStats(ctx)
	if st.BudgetBytes <= 0 || st.HoldDays == 0 || st.HeldBytes+size <= st.BudgetBytes {
		return ""
	}
	msg := fmt.Sprintf("Paused — the originals hold is full (%s of %s)", humanBytes(st.HeldBytes), humanBytes(st.BudgetBytes))
	if st.NextReleaseAt > 0 {
		msg += "; next release " + inDays(st.NextReleaseAt)
	}
	return msg
}

// inDays says when a unix time comes, roughly: "today", "tomorrow", "in 3 days".
func inDays(at int64) string {
	d := int(math.Ceil(time.Until(time.Unix(at, 0)).Hours() / 24))
	switch {
	case d <= 0:
		return "today"
	case d == 1:
		return "tomorrow"
	}
	return fmt.Sprintf("in %d days", d)
}

// finishSkipUntil is finishSkip with the retry time given, not worked out from the kind.
func (s *Service) finishSkipUntil(job *Job, kind, note string, retryAt int64) {
	if s.wasCancelled(job) {
		s.finish(job, StateCancelled, "cancelled")
		return
	}
	attempts := s.skips.recordAt(context.Background(), job.Key, kind, note, retryAt)
	s.record(job).skipKind = kind
	s.finish(job, StateSkipped, note)
	s.skipRecorded(job, kind, note, attempts) // convert.stalled on the first in a row (events.go)
}

// ReleaseHoldsOlderThan releases every held original converted at least days ago (0 = all
// of them) and reports how many went and the space that freed.
func (s *Service) ReleaseHoldsOlderThan(ctx context.Context, days int) (released int, freed int64, err error) {
	if s.history == nil || s.history.db == nil {
		return 0, 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -days).Unix()
	rows, err := s.history.db.QueryContext(ctx,
		`SELECT id FROM convert_history WHERE hold_state = ? AND outcome = ? AND finished_at <= ? ORDER BY finished_at`,
		HoldHeld, OutcomeDone, cutoff)
	if err != nil {
		return 0, 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if ctx.Err() != nil {
			return released, freed, ctx.Err()
		}
		e, err := s.releaseHold(ctx, id, "you released it")
		if err != nil {
			continue // a row mid-swap or already gone: left for its own turn
		}
		if e.HoldState == HoldReleased {
			released++
			if !e.ReclaimDeferred {
				freed += max(e.SrcSize-e.OutSize, 0)
			}
		}
	}
	return released, freed, nil
}
