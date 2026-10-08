package convert

import (
	"context"
	"database/sql"
	"time"
)

// Skip kinds. These group the Problems list, so they're coarse on purpose — the user cares
// "why won't these convert", not which line of code returned.
const (
	SkipHDRUnsupported = "hdr_unsupported" // this file's HDR can't be carried through a conversion
	SkipHardlinked     = "hardlinked"      // still seeding / hardlinked, so it isn't ours to replace
	SkipNotSmaller     = "not_smaller"     // the encode didn't save enough to be worth keeping
	SkipQualityGate    = "quality_gate"    // the encode couldn't meet the quality threshold
	SkipCancelled      = "cancelled"       // you cancelled it; left alone for a while
	SkipAlreadyTarget  = "already_target"  // nothing to do; not worth recording

	// Conditions that clear on their own (someone frees space, a file reappears). They back
	// off instead of being retried on the very next pick — that used to hot-loop the runner.
	SkipNoScratch   = "no_scratch"   // the transcode folder hasn't room for this file
	SkipLibraryFull = "library_full" // the library disk hasn't room for the converted file
	SkipSourceGone  = "source_gone"  // the library file is missing
	SkipTransient   = "transient"    // any other failure that should clear on its own

	// The original wouldn't fit in the recycle bin under its cap, so retiring it would make
	// the bin purge it within the hour. Waits for the owner to raise the cap (or for the
	// bin to empty); checked daily.
	SkipBinFull = "bin_full"
)

// backoffSkip reports whether a skip kind waits longer each time it repeats.
func backoffSkip(kind string) bool {
	switch kind {
	case SkipNoScratch, SkipLibraryFull, SkipSourceGone, SkipTransient:
		return true
	}
	return false
}

// retryDelay is how long a temporary skip waits before the file is picked again. A seeding
// file is checked twice a day; a file you cancelled stays out of the way for a month (or
// until you press "Try again"). A full disk is looked at again after an hour, then six,
// then daily — soon enough to notice space was freed, rarely enough not to flood the log.
// attempts counts this kind in a row, starting at 1.
func retryDelay(kind string, attempts int) time.Duration {
	switch kind {
	case SkipHardlinked:
		return 12 * time.Hour
	case SkipCancelled:
		return 30 * 24 * time.Hour
	case SkipBinFull:
		return 24 * time.Hour
	}
	if backoffSkip(kind) {
		switch {
		case attempts <= 1:
			return time.Hour
		case attempts == 2:
			return 6 * time.Hour
		}
		return 24 * time.Hour
	}
	return 0
}

// permanentSkip reports whether a skip reason will still hold next time, unchanged.
//
// A Dolby Vision file cannot become AV1-convertible by waiting, and a file that didn't
// shrink won't shrink on a retry. A seeding file, by contrast, stops seeding eventually.
// Only permanent skips are excluded from the reclaimable-space figure — otherwise the
// Overview keeps promising space that will never arrive.
func permanentSkip(kind string) bool {
	switch kind {
	case SkipHDRUnsupported, SkipNotSmaller, SkipQualityGate:
		return true
	}
	return false
}

// skipStore persists why files were skipped, so the reasons survive a restart and can be
// shown to the user instead of scrolling out of an in-memory job list.
type skipStore struct{ db *sql.DB }

// Skipped is one skipped file, for the Problems list.
type Skipped struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
	Permanent bool   `json:"permanent"`
	UpdatedAt string `json:"updated_at"`
	// When a temporary skip is next tried (unix seconds; 0 = no wait), and how many times
	// in a row it has hit this same kind.
	RetryAfter int64 `json:"retry_after"`
	Attempts   int   `json:"attempts"`

	// Resolved for display.
	MediaKind string `json:"media_kind"`
	MovieID   int64  `json:"movie_id,omitempty"`
	SeriesID  int64  `json:"series_id,omitempty"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	Title     string `json:"title"`
}

// record stores why an item was skipped and returns how many times in a row it has now hit
// this kind (1 for a new or different kind), which sets how long a backing-off skip waits.
// Only one job runs per item at a time, so the read and the write can't interleave.
func (st *skipStore) record(ctx context.Context, key, kind, reason string) int {
	if key == "" || kind == "" || kind == SkipAlreadyTarget {
		return 0 // "already the target codec" isn't a problem, it's success
	}
	perm := 0
	if permanentSkip(kind) {
		perm = 1
	}
	attempts := 1
	var prevKind string
	var prevAttempts int
	if st.db.QueryRowContext(ctx, `SELECT kind, attempts FROM convert_skips WHERE item_key = ?`, key).
		Scan(&prevKind, &prevAttempts) == nil && prevKind == kind {
		attempts = prevAttempts + 1
	}
	var retry int64
	if d := retryDelay(kind, attempts); d > 0 {
		retry = time.Now().Add(d).Unix()
	}
	_, _ = st.db.ExecContext(ctx,
		`INSERT INTO convert_skips (item_key, kind, reason, permanent, retry_after, attempts, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(item_key) DO UPDATE SET
		   kind = excluded.kind, reason = excluded.reason, permanent = excluded.permanent,
		   retry_after = excluded.retry_after, attempts = excluded.attempts, updated_at = datetime('now')`,
		key, kind, reason, perm, retry, attempts)
	return attempts
}

// clear forgets an item's skip — called when it converts successfully, or when the user
// asks for it to be retried.
func (st *skipStore) clear(ctx context.Context, key string) {
	_, _ = st.db.ExecContext(ctx, `DELETE FROM convert_skips WHERE item_key = ?`, key)
}

func (st *skipStore) clearAll(ctx context.Context) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM convert_skips`)
	return err
}

// permanentKeys returns the items whose skip won't resolve on its own, so their space can
// be left out of the reclaimable total.
func (st *skipStore) permanentKeys(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	rows, err := st.db.QueryContext(ctx, `SELECT item_key FROM convert_skips WHERE permanent = 1`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			out[k] = true
		}
	}
	return out
}

// waitingKeys returns the items the runner must not pick right now: permanent skips, and
// temporary ones whose retry time hasn't come.
func (st *skipStore) waitingKeys(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	rows, err := st.db.QueryContext(ctx,
		`SELECT item_key FROM convert_skips WHERE permanent = 1 OR retry_after > ?`, time.Now().Unix())
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			out[k] = true
		}
	}
	return out
}

func (st *skipStore) list(ctx context.Context) ([]Skipped, error) {
	rows, err := st.db.QueryContext(ctx,
		`SELECT item_key, kind, reason, permanent, updated_at, retry_after, attempts FROM convert_skips
		 ORDER BY permanent DESC, kind, updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Skipped{}
	for rows.Next() {
		var s Skipped
		var perm int
		if err := rows.Scan(&s.Key, &s.Kind, &s.Reason, &perm, &s.UpdatedAt, &s.RetryAfter, &s.Attempts); err != nil {
			return nil, err
		}
		s.Permanent = perm == 1
		var b Blocked
		b.Key = s.Key
		parseItemKey(&b)
		s.MediaKind, s.MovieID, s.SeriesID, s.Season, s.Episode = b.Kind, b.MovieID, b.SeriesID, b.Season, b.Episode
		out = append(out, s)
	}
	return out, rows.Err()
}
