package listening

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Re-keying: when two books are merged, every place, bookmark, session and timeline row
// filed under the dropped book's items moves to the kept book's, so nobody loses their
// place. Listening apps cache item ids and may report on the old one long after (an
// offline upload from a plane, a progress update queued on a phone), so each move also
// leaves a redirect behind that the audiobook server follows.
//
// Nothing here logs or returns which users or items moved: RekeyCounts is row counts
// only, and listen_item_redirects has no user column.

// ItemKey is the key a book's standard audiobook ("b12") or one of its audio versions
// ("b12v3") is filed under, here and on the audiobook server.
func ItemKey(bookID, versionID int64) string {
	if versionID > 0 {
		return fmt.Sprintf("b%dv%d", bookID, versionID)
	}
	return fmt.Sprintf("b%d", bookID)
}

// RekeyCounts says how many rows a re-key moved. Counts only, so it can be logged.
type RekeyCounts struct {
	Progress  int `json:"progress"`  // places moved to the new key
	Conflicts int `json:"conflicts"` // places where the user had one on both keys (the newer was kept)
	History   int `json:"history"`   // timeline rows moved
	Sessions  int `json:"sessions"`  // play sessions moved
	Bookmarks int `json:"bookmarks"` // bookmarks moved (one at the same second on the new key wins)
	Redirects int `json:"redirects"` // redirects written or re-pointed
}

// HoldReports stops every report that reads a place and then writes it (sessions,
// offline uploads, progress updates) until release is called. A merge holds them from
// before its transaction starts until after it commits, so a report that read the place
// under the old key can't write it back there once the place has moved.
func (s *Store) HoldReports() (release func()) {
	s.mu.Lock()
	return s.mu.Unlock
}

// RekeyItemsTx moves everything filed under each old key to its new key, inside the
// caller's transaction (a failure anywhere rolls the whole merge back):
//
//   - listen_progress: a place moves; where the user also has one on the new key, the
//     newer one (by updated_at, a live place before a removed one) is kept whole — the
//     pending_* hold fields with it, so a held jump stays held — and the other is kept
//     on the timeline as the place before the merge, so it can still be put back.
//   - listen_history: moves, then each user's timeline on the new key is trimmed as
//     every write trims it.
//   - listen_sessions: move, so an open session keeps syncing.
//   - listen_bookmarks: copied unless the user has one at the same second on the new
//     key, then removed from the old key.
//   - listen_item_redirects: old → new, and any redirect that pointed at old now points
//     at new, so a redirect is always one hop.
//
// The caller should hold HoldReports while the transaction runs.
func (s *Store) RekeyItemsTx(ctx context.Context, tx *store.Transaction, moves map[string]string) (RekeyCounts, error) {
	var c RekeyCounts
	olds := make([]string, 0, len(moves))
	for old, nw := range moves {
		if old == "" || nw == "" || old == nw {
			return c, fmt.Errorf("re-key: bad move %q → %q", old, nw)
		}
		if _, chained := moves[nw]; chained {
			return c, errors.New("re-key: a new key is also being moved away")
		}
		olds = append(olds, old)
	}
	sort.Strings(olds) // the same moves always run in the same order
	now := s.nowMs()
	for _, old := range olds {
		nw := moves[old]
		touched, err := s.rekeyProgress(ctx, tx, old, nw, now, &c)
		if err != nil {
			return c, fmt.Errorf("re-key places: %w", err)
		}
		res, err := tx.ExecContext(ctx, `UPDATE listen_history SET item_key = ? WHERE item_key = ?`, nw, old)
		if err != nil {
			return c, fmt.Errorf("re-key timeline: %w", err)
		}
		n, _ := res.RowsAffected()
		c.History += int(n)
		if n > 0 {
			rows, err := tx.QueryContext(ctx, `SELECT DISTINCT user_id FROM listen_history WHERE item_key = ?`, nw)
			if err != nil {
				return c, err
			}
			for rows.Next() {
				var uid int64
				if err := rows.Scan(&uid); err != nil {
					rows.Close()
					return c, err
				}
				touched[uid] = true
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return c, err
			}
		}
		for uid := range touched {
			for _, place := range []bool{true, false} {
				if err := pruneHistory(ctx, tx, uid, nw, place); err != nil {
					return c, fmt.Errorf("trim timeline: %w", err)
				}
			}
		}
		if res, err = tx.ExecContext(ctx, `UPDATE listen_sessions SET item_key = ? WHERE item_key = ?`, nw, old); err != nil {
			return c, fmt.Errorf("re-key sessions: %w", err)
		}
		n, _ = res.RowsAffected()
		c.Sessions += int(n)
		if res, err = tx.ExecContext(ctx,
			`INSERT INTO listen_bookmarks (user_id, item_key, time, title, created_at)
			 SELECT user_id, ?, time, title, created_at FROM listen_bookmarks WHERE item_key = ? ON CONFLICT DO NOTHING`, nw, old); err != nil {
			return c, fmt.Errorf("re-key bookmarks: %w", err)
		}
		n, _ = res.RowsAffected()
		c.Bookmarks += int(n)
		if _, err := tx.ExecContext(ctx, `DELETE FROM listen_bookmarks WHERE item_key = ?`, old); err != nil {
			return c, fmt.Errorf("re-key bookmarks: %w", err)
		}
		// Redirects: collapse chains onto the new key, and never leave one that would
		// send the new key somewhere else (a loop).
		if res, err = tx.ExecContext(ctx, `UPDATE listen_item_redirects SET new_key = ? WHERE new_key = ?`, nw, old); err != nil {
			return c, fmt.Errorf("re-point redirects: %w", err)
		}
		n, _ = res.RowsAffected()
		c.Redirects += int(n)
		if _, err := tx.ExecContext(ctx, `DELETE FROM listen_item_redirects WHERE old_key = ?`, nw); err != nil {
			return c, fmt.Errorf("re-point redirects: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO listen_item_redirects (old_key, new_key, created_at) VALUES (?, ?, ?)
			 ON CONFLICT(old_key) DO UPDATE SET new_key = excluded.new_key`, old, nw, now); err != nil {
			return c, fmt.Errorf("write redirect: %w", err)
		}
		c.Redirects++
	}
	return c, nil
}

// rekeyProgress moves every user's place on old to nw and returns the users it touched
// (whose timelines on nw need trimming).
func (s *Store) rekeyProgress(ctx context.Context, tx *store.Transaction, old, nw string, now int64, c *RekeyCounts) (map[int64]bool, error) {
	touched := map[int64]bool{}
	rows, err := tx.QueryContext(ctx, progressSelect+` WHERE item_key = ?`, old)
	if err != nil {
		return nil, err
	}
	var moving []Progress
	for rows.Next() {
		p, err := scanProgress(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		moving = append(moving, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, p := range moving {
		touched[p.UserID] = true
		existing, found, err := progressTx(ctx, tx, p.UserID, nw)
		if err != nil {
			return nil, err
		}
		if !found {
			if _, err := tx.ExecContext(ctx, `UPDATE listen_progress SET item_key = ? WHERE user_id = ? AND item_key = ?`,
				nw, p.UserID, old); err != nil {
				return nil, err
			}
			c.Progress++
			continue
		}
		c.Conflicts++
		winner, loser := existing, p
		if placeWins(p, existing) {
			winner, loser = p, existing
		}
		winner.ItemKey = nw
		if err := overwriteProgress(ctx, tx, winner); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM listen_progress WHERE user_id = ? AND item_key = ?`, p.UserID, old); err != nil {
			return nil, err
		}
		// The place that lost is kept on the merged timeline, so "Go back here" can still
		// bring it back.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO listen_history (user_id, item_key, position, at, device, reason, kind, dismissed) VALUES (?, ?, ?, ?, ?, ?, ?, 0)`,
			p.UserID, nw, loser.Position, nonZero(loser.UpdatedAt, now), loser.Device, "merge", KindBefore); err != nil {
			return nil, err
		}
	}
	return touched, nil
}

// placeWins reports whether a beats b when one user has a place on both keys: a live
// place beats one an app removed, then the newer one wins (a tie keeps b, the place
// already on the new key).
func placeWins(a, b Progress) bool {
	if (a.DiscardedAt == 0) != (b.DiscardedAt == 0) {
		return a.DiscardedAt == 0
	}
	return a.UpdatedAt > b.UpdatedAt
}

// progressTx reads one stored place (removed or not) inside a transaction.
func progressTx(ctx context.Context, tx *store.Transaction, userID int64, key string) (Progress, bool, error) {
	rows, err := tx.QueryContext(ctx, progressSelect+` WHERE user_id = ? AND item_key = ?`, userID, key)
	if err != nil {
		return Progress{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Progress{}, false, rows.Err()
	}
	p, err := scanProgress(rows)
	return p, err == nil, err
}

// overwriteProgress writes every column of p over the stored row with its user and key —
// the hold (pending_*), hidden and removed state included, exactly as they were.
func overwriteProgress(ctx context.Context, tx *store.Transaction, p Progress) error {
	var pending any
	if p.PendingPosition != nil {
		pending = *p.PendingPosition
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE listen_progress SET position = ?, duration = ?, finished = ?, finished_at = ?, updated_at = ?, device = ?,
		   session_id = ?, pending_position = ?, pending_session = ?, pending_listened = ?, pending_at = ?, hidden = ?,
		   discarded_at = ?
		 WHERE user_id = ? AND item_key = ?`,
		p.Position, p.Duration, boolInt(p.Finished), p.FinishedAt, p.UpdatedAt, p.Device, p.SessionID,
		pending, p.PendingSession, p.PendingListened, p.PendingAt, boolInt(p.Hidden), p.DiscardedAt,
		p.UserID, p.ItemKey)
	return err
}

func nonZero(v, fallback int64) int64 {
	if v > 0 {
		return v
	}
	return fallback
}

// ResolveItem returns the key an item lives under now: the key it was moved to by a
// merge, or the key itself when it never moved.
func (s *Store) ResolveItem(ctx context.Context, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return key
	}
	var nw string
	if err := s.db.QueryRowContext(ctx, `SELECT new_key FROM listen_item_redirects WHERE old_key = ?`, key).Scan(&nw); err != nil || nw == "" {
		return key
	}
	return nw
}
