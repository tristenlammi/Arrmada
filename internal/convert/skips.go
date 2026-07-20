package convert

import (
	"context"
	"database/sql"
)

// Skip kinds. These group the Problems list, so they're coarse on purpose — the user cares
// "why won't these convert", not which line of code returned.
const (
	SkipHDRUnsupported = "hdr_unsupported" // the target format can't carry this file's HDR metadata
	SkipHardlinked     = "hardlinked"      // still seeding / hardlinked, so it isn't ours to replace
	SkipNotSmaller     = "not_smaller"     // the encode came out no smaller than the source
	SkipQualityGate    = "quality_gate"    // the encode couldn't meet the quality threshold
	SkipQueueFull      = "queue_full"      // transient: the queue was saturated
	SkipAlreadyTarget  = "already_target"  // nothing to do; not worth recording
)

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

	// Resolved for display.
	MediaKind string `json:"media_kind"`
	MovieID   int64  `json:"movie_id,omitempty"`
	SeriesID  int64  `json:"series_id,omitempty"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	Title     string `json:"title"`
}

func (st *skipStore) record(ctx context.Context, key, kind, reason string) {
	if key == "" || kind == "" || kind == SkipAlreadyTarget {
		return // "already the target codec" isn't a problem, it's success
	}
	perm := 0
	if permanentSkip(kind) {
		perm = 1
	}
	_, _ = st.db.ExecContext(ctx,
		`INSERT INTO convert_skips (item_key, kind, reason, permanent, updated_at)
		 VALUES (?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(item_key) DO UPDATE SET
		   kind = excluded.kind, reason = excluded.reason,
		   permanent = excluded.permanent, updated_at = datetime('now')`,
		key, kind, reason, perm)
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

func (st *skipStore) list(ctx context.Context) ([]Skipped, error) {
	rows, err := st.db.QueryContext(ctx,
		`SELECT item_key, kind, reason, permanent, updated_at FROM convert_skips
		 ORDER BY permanent DESC, kind, updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Skipped{}
	for rows.Next() {
		var s Skipped
		var perm int
		if err := rows.Scan(&s.Key, &s.Kind, &s.Reason, &perm, &s.UpdatedAt); err != nil {
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
