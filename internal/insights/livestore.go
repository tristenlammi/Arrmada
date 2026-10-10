package insights

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/tristenlammi/arrmada/internal/plex"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Persisting the poller's in-flight streams (insights_live_sessions, migration 0175), so a
// restart neither splits a play in two nor announces it again, and a crash doesn't lose it.

// maxResumeGap is how long a stream can go unseen across a restart and still be the same
// play. An ./update.sh is a minute or two; past this, the old play is closed at its last
// sighting and whatever is playing now is recorded as a new one.
const maxResumeGap = 15 * time.Minute

// execer is what the session writes need, from the database or from a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// bufEventDTO is bufEvent with exported fields, for the buf_events JSON.
type bufEventDTO struct {
	At         int64  `json:"at"` // unix ms
	Offset     int64  `json:"offset"`
	DurationMS int64  `json:"duration_ms"`
	Cause      string `json:"cause"`
	Detail     string `json:"detail"`
}

func (r *repo) saveLive(ctx context.Context, ex execer, ls *liveSession) error {
	evs := make([]bufEventDTO, 0, len(ls.bufEvents))
	for _, e := range ls.bufEvents {
		evs = append(evs, bufEventDTO{At: e.at.UnixMilli(), Offset: e.offset, DurationMS: e.durationMS, Cause: e.cause, Detail: e.detail})
	}
	evJSON, err := json.Marshal(evs)
	if err != nil {
		return err
	}
	snap, err := json.Marshal(ls.sess)
	if err != nil {
		return err
	}
	_, err = ex.ExecContext(ctx, `
		INSERT INTO insights_live_sessions
		 (session_key, rating_key, user_id, started_at, last_seen_at, paused_ms, state, steady, buffering,
		  spell_counted, last_offset_ms, buf_count, buf_events, snapshot)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(session_key) DO UPDATE SET
		  rating_key=excluded.rating_key, user_id=excluded.user_id, started_at=excluded.started_at,
		  last_seen_at=excluded.last_seen_at, paused_ms=excluded.paused_ms, state=excluded.state,
		  steady=excluded.steady, buffering=excluded.buffering, spell_counted=excluded.spell_counted,
		  last_offset_ms=excluded.last_offset_ms, buf_count=excluded.buf_count,
		  buf_events=excluded.buf_events, snapshot=excluded.snapshot`,
		ls.sess.SessionKey, ls.sess.RatingKey, ls.sess.UserID, ls.started.UnixMilli(), ls.lastSeen.UnixMilli(),
		ls.pausedMS, ls.state, b2i(ls.steady), b2i(ls.buffering), b2i(ls.spellCounted), ls.lastOffsetMS,
		ls.bufCount, string(evJSON), string(snap))
	return err
}

func (r *repo) deleteLive(ctx context.Context, ex execer, key string) error {
	_, err := ex.ExecContext(ctx, `DELETE FROM insights_live_sessions WHERE session_key = ?`, key)
	return err
}

func (r *repo) clearLive(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM insights_live_sessions`)
	return err
}

// loadLive reads every saved session. A row whose snapshot can't be read is skipped (and
// reported), never guessed at.
func (r *repo) loadLive(ctx context.Context) (map[string]*liveSession, []string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT session_key, started_at, last_seen_at, paused_ms, state, steady,
		buffering, spell_counted, last_offset_ms, buf_count, buf_events, snapshot FROM insights_live_sessions`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := map[string]*liveSession{}
	var bad []string
	for rows.Next() {
		var (
			key, state, evJSON, snap             string
			started, lastSeen, paused, offset    int64
			steady, buffering, counted, bufCount int
		)
		if err := rows.Scan(&key, &started, &lastSeen, &paused, &state, &steady, &buffering, &counted,
			&offset, &bufCount, &evJSON, &snap); err != nil {
			return nil, nil, err
		}
		ls := &liveSession{
			started: time.UnixMilli(started), lastSeen: time.UnixMilli(lastSeen), state: state, pausedMS: paused,
			steady: steady != 0, buffering: buffering != 0, spellCounted: counted != 0, lastOffsetMS: offset,
			bufCount: bufCount, resumed: true,
		}
		var evs []bufEventDTO
		if json.Unmarshal([]byte(snap), &ls.sess) != nil || json.Unmarshal([]byte(evJSON), &evs) != nil || ls.sess.SessionKey != key {
			bad = append(bad, key)
			continue
		}
		for _, e := range evs {
			ls.bufEvents = append(ls.bufEvents, bufEvent{at: time.UnixMilli(e.At), offset: e.Offset, durationMS: e.DurationMS, cause: e.Cause, detail: e.Detail})
		}
		out[key] = ls
	}
	return out, bad, rows.Err()
}

// restoreLive loads the streams the last run was following, so the first poll continues
// them instead of starting them over.
func (s *Service) restoreLive(ctx context.Context) {
	live, bad, err := s.repo.loadLive(ctx)
	if err != nil {
		s.log.Warn("insights: couldn't load the streams in progress at the last shutdown", "err", err)
		return
	}
	for _, key := range bad {
		s.log.Warn("insights: a saved stream in progress couldn't be read; dropping it", "session", key)
		_ = s.repo.deleteLive(ctx, s.repo.db, key)
	}
	for key, ls := range live {
		s.live[key] = ls
	}
	if len(live) > 0 {
		s.log.Info("insights: picked up streams in progress from before the restart", "streams", len(live))
	}
}

// saveAllLive writes every followed stream in one transaction — once per poll.
func (s *Service) saveAllLive(ctx context.Context) {
	if len(s.live) == 0 {
		return
	}
	err := store.WithTx(ctx, s.repo.db, func(tx *sql.Tx) error {
		for _, ls := range s.live {
			if err := s.repo.saveLive(ctx, tx, ls); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && ctx.Err() == nil {
		s.log.Warn("insights: couldn't save the streams in progress", "err", err)
	}
}

// resume continues a stream the last run was following. It reports false when the stream
// was gone too long to be the same play; the caller then closes it and starts afresh.
// Time Arrmada was down is not counted as watching unless playback actually moved on.
func (ls *liveSession) resume(sess plex.Session, now time.Time) bool {
	ls.resumed = false
	gap := now.Sub(ls.lastSeen)
	if gap > maxResumeGap || gap < 0 {
		return false
	}
	// Credit the gap only as far as the position moved; the rest counts as paused. This
	// replaces observe's own accrual for the gap (it would call all of it paused, or none).
	advance := max(sess.OffsetMS-ls.lastOffsetMS, 0)
	if gapMS := gap.Milliseconds(); advance < gapMS {
		ls.pausedMS += gapMS - advance
	}
	ls.lastSeen = now
	// Nobody saw the playback in between, so a stall now can't be told from a restart's
	// refill: don't count it.
	ls.steady = false
	return true
}
