package insights

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Deciding whether an imported play is the same play Arrmada recorded live.
//
// Tautulli and the live poller watch the same Plex server, so every play from the period
// both were running exists twice — but never with the same start second: the poller's
// start is the first poll that saw the stream, Tautulli's is its own. So the match is on
// time overlap, not on equal timestamps.
//
// Two rows are the same play when, for the same user and the same item (same rating key,
// or — after a Plex database rebuild hands out new keys — the same title, show, season
// and episode), they overlap for MORE THAN HALF of the shorter one. Any overlap at all
// would be too loose: a legitimate back-to-back replay of the same episode touches the
// previous play for a few seconds of clock skew between the two recorders, and that
// replay must survive. A real duplicate covers almost all of the shorter row.

// maxLiveLeadSecs bounds how long before an imported play a matching live row can have
// started. A live recording of the same play starts within seconds of it; the bound only
// lets the index skip a user's older history.
const maxLiveLeadSecs = 24 * 3600

// playWindow is one imported play's identity and the stretch of time it covers.
type playWindow struct {
	UserID           string
	RatingKey        string
	Title            string
	GrandparentTitle string
	ParentIndex      int
	MediaIndex       int
	Start, End       int64 // epoch seconds
}

// importedEnd is where an imported play's window stops for overlap purposes. A source's
// stop time can be far later than the watching (a client left open for a day, or rows
// Tautulli grouped across several sittings), and a window that long would swallow
// unrelated later plays of the same item. Watched plus paused time is the most the play
// can actually have covered.
func importedEnd(start, stop, watchedMS, pausedMS int64) int64 {
	if watchedMS > 0 {
		if end := start + (watchedMS+pausedMS)/1000; end < stop {
			return end
		}
	}
	return stop
}

// importedEndSQL is importedEnd over a stream_sessions row aliased t. Integer division in
// both, so Go and SQL agree to the second.
func importedEndSQL(t string) string {
	return fmt.Sprintf(`(CASE WHEN %[1]s.watched_ms > 0 AND %[1]s.started_at + (%[1]s.watched_ms + %[1]s.paused_ms)/1000 < %[1]s.stopped_at
		THEN %[1]s.started_at + (%[1]s.watched_ms + %[1]s.paused_ms)/1000 ELSE %[1]s.stopped_at END)`, t)
}

// liveOverlapSQL is "a live row is the same play as i": i supplies user_id, started_at,
// rating_key, title, grandparent_title, parent_index and media_index; end is the SQL for
// i's window end. The single-row check and the repair both use it, so the import and the
// repair can never disagree about what a duplicate is.
func liveOverlapSQL(end string) string {
	return `EXISTS (SELECT 1 FROM stream_sessions l
		WHERE l.session_key <> '' AND l.user_id = i.user_id
		  AND l.started_at >= i.started_at - ` + fmt.Sprint(maxLiveLeadSecs) + `
		  AND l.started_at < ` + end + ` AND l.stopped_at > i.started_at
		  AND ((i.rating_key <> '' AND l.rating_key = i.rating_key)
		    OR (i.title <> '' AND l.title = i.title AND l.grandparent_title = i.grandparent_title
		        AND l.parent_index = i.parent_index AND l.media_index = i.media_index))
		  AND 2 * (MIN(l.stopped_at, ` + end + `) - MAX(l.started_at, i.started_at))
		      > MIN(l.stopped_at - l.started_at, ` + end + ` - i.started_at))`
}

// overlapsLive reports whether Arrmada already recorded this play live.
func (r *repo) overlapsLive(ctx context.Context, w playWindow) (bool, error) {
	if w.UserID == "" {
		return false, nil // no user to match on; the live recorder always has one
	}
	var hit bool
	err := r.db.QueryRowContext(ctx, `
		WITH i(user_id, started_at, end_at, rating_key, title, grandparent_title, parent_index, media_index)
		  AS (VALUES (?, ?, ?, ?, ?, ?, ?, ?))
		SELECT `+liveOverlapSQL("i.end_at")+` FROM i`,
		w.UserID, w.Start, w.End, w.RatingKey, w.Title, w.GrandparentTitle, w.ParentIndex, w.MediaIndex).Scan(&hit)
	return hit, err
}

// importOverlapWhere selects imported rows (an empty session_key) that repeat a live play.
var importOverlapWhere = `i.session_key = '' AND i.user_id <> '' AND ` + liveOverlapSQL(importedEndSQL("i"))

func (r *repo) countImportOverlaps(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stream_sessions i WHERE `+importOverlapWhere).Scan(&n)
	return n, err
}

// ErrOverlapsChanged is a repair whose confirmed count no longer matches the data.
var ErrOverlapsChanged = errors.New("the number of double-counted plays changed since you checked — check again")

func (r *repo) removeImportOverlaps(ctx context.Context, expected int) (int64, error) {
	var removed int64
	err := store.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM stream_sessions i WHERE `+importOverlapWhere).Scan(&n); err != nil {
			return err
		}
		if expected >= 0 && n != expected {
			return ErrOverlapsChanged
		}
		if n == 0 {
			return nil
		}
		// The predicate only reads live rows, which this never deletes, so evaluating it
		// again for the second statement selects exactly the same imported rows.
		ids := `SELECT i.id FROM stream_sessions i WHERE ` + importOverlapWhere
		if _, err := tx.ExecContext(ctx, `DELETE FROM buffer_events WHERE session_id IN (`+ids+`)`); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM stream_sessions WHERE session_key = '' AND id IN (`+ids+`)`)
		if err != nil {
			return err
		}
		if removed, err = res.RowsAffected(); err != nil {
			return err
		}
		if removed != int64(n) {
			return fmt.Errorf("repair would remove %d plays but counted %d — nothing was changed", removed, n)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// firstLiveStart is the start of the earliest live-recorded play (0 = none yet).
func (r *repo) firstLiveStart(ctx context.Context) (int64, error) {
	var at sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT MIN(started_at) FROM stream_sessions WHERE session_key <> ''`).Scan(&at)
	return at.Int64, err
}
