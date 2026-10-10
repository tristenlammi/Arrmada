package listening

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"sync"
	"time"
)

// ErrSessionNotFound is returned for a session id this server never issued (or pruned).
var ErrSessionNotFound = errors.New("playback session not found")

// ErrNothingToRestore is returned when there's no removed place to put back.
var ErrNothingToRestore = errors.New("there's no removed place to put back")

// ErrNoHistory is returned for a timeline row that doesn't exist (or isn't the caller's).
var ErrNoHistory = errors.New("that earlier place no longer exists")

// Timeline row kinds (listen_history.kind).
const (
	KindApplied   = "applied"   // the place moved
	KindBefore    = "before"    // the place just before a non-routine change
	KindRejected  = "rejected"  // a place an app sent that wasn't used
	KindHeld      = "held"      // a big jump waiting for proof
	KindDiscarded = "discarded" // an app removed the place
	KindRestored  = "restored"  // the person put a place back
)

const (
	// historyKeep is how many places the book moved through (applied and before rows) are
	// kept per user and item; historyKeepOther is the same for everything else on the
	// timeline (rejected, held, discarded, restored), so a chatty app sending stale
	// reports can't push the real places out.
	historyKeep      = 50
	historyKeepOther = 30
	// beforeNear: a "place before a change" row isn't written when the newest place on
	// the timeline is already within this many seconds of it.
	beforeNear = 30.0
	// offerMin and offerMaxAge: a place an app sent that wasn't used is offered back
	// ("use this later spot?") when it's at least this far past the saved place and
	// no older than this.
	offerMin    = 60.0
	offerMaxAge = 14 * 24 * time.Hour
	// RemovedRetention is how long a place an app removed stays restorable.
	RemovedRetention = 90 * 24 * time.Hour
	// historyGap: a routine position change is written to history at most this often
	// per item; jumps, restores and manual changes always are.
	historyGap = 5 * time.Minute
	// listenSlack is how much listening one live report may claim beyond the wall time
	// since the previous one (report timing jitter).
	listenSlack = 15.0
	// reportSkew: an app's own time on a progress report counts only when it is at least
	// this old, so ordinary clock drift can't make a fresh report look "older".
	reportSkew = 2 * time.Minute
	// SessionRetention is how long an idle session is kept before pruning.
	SessionRetention = 30 * 24 * time.Hour
)

// Store persists progress, sessions, history, bookmarks and the listening log.
type Store struct {
	db  *sql.DB
	now func() time.Time
	mu  sync.Mutex // one report at a time: read-decide-write must not interleave
}

// NewStore wraps the database.
func NewStore(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }

func (s *Store) nowMs() int64 { return s.now().UnixMilli() }

// Session is a play session.
type Session struct {
	ID        string  `json:"id"`
	UserID    int64   `json:"-"`
	ItemKey   string  `json:"-"`
	DeviceID  string  `json:"device_id"`
	Device    string  `json:"device"`
	Client    string  `json:"client"`
	Offline   bool    `json:"offline"`
	StartedAt int64   `json:"started_at"`
	LastAt    int64   `json:"last_at"`
	StartPos  float64 `json:"start_pos"`
	CurPos    float64 `json:"cur_pos"`
	Listened  float64 `json:"listened"`
	Closed    bool    `json:"closed"`
	// Restart: the book was finished, so this session starts again from the beginning
	// (not stored; it tells the caller to describe the place as 0:00, unfinished).
	Restart bool `json:"-"`
}

// OpenSession starts a play session at the user's saved place (or 0).
//
// A finished book starts again from 0:00 — opening one from "Listen again" must not play
// the last few seconds and stop. The restart is held like any jump back, tied to this
// session: carrying on listening from the start for a moment saves it (and clears
// Finished), while just opening and closing the book leaves it finished.
func (s *Store) OpenSession(ctx context.Context, userID int64, itemKey, deviceID, device, client string) (Session, Progress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowMs()
	p, found, err := s.progress(ctx, userID, itemKey)
	if err != nil {
		return Session{}, Progress{}, err
	}
	sess := Session{ID: newID(), UserID: userID, ItemKey: itemKey, DeviceID: deviceID, Device: device, Client: client,
		StartedAt: now, LastAt: now, StartPos: p.Position, CurPos: p.Position}
	if found && p.Finished {
		sess.StartPos, sess.CurPos, sess.Restart = 0, 0, true
		zero := 0.0
		p.PendingPosition, p.PendingSession, p.PendingListened, p.PendingAt = &zero, sess.ID, 0, now
		// Only the pending state changes: the saved place, its time and its history stay
		// as they were until the restart proves itself.
		if err := s.writeProgress(ctx, p); err != nil {
			return Session{}, Progress{}, err
		}
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO listen_sessions (id, user_id, item_key, device_id, device, client, offline, started_at, last_at, start_pos, cur_pos)
		 VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		sess.ID, userID, itemKey, deviceID, device, client, now, now, sess.StartPos, sess.CurPos)
	if err != nil {
		return Session{}, Progress{}, err
	}
	if err := s.touchLog(ctx, sess, 0); err != nil {
		return Session{}, Progress{}, err
	}
	return sess, p, nil
}

// GetSession loads a session, checking it belongs to the user.
func (s *Store) GetSession(ctx context.Context, userID int64, id string) (Session, error) {
	var sess Session
	var offline, closed int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, item_key, device_id, device, client, offline, started_at, last_at, start_pos, cur_pos, listened, closed
		 FROM listen_sessions WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&sess.ID, &sess.UserID, &sess.ItemKey, &sess.DeviceID, &sess.Device, &sess.Client, &offline,
			&sess.StartedAt, &sess.LastAt, &sess.StartPos, &sess.CurPos, &sess.Listened, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	sess.Offline, sess.Closed = offline != 0, closed != 0
	return sess, err
}

// Sync applies a live progress report to a session. A closed or long-idle session is
// simply picked up again — it never becomes "not found" while it's kept.
//
// position is nil when the app didn't say where it is (a close with no body, or a sync
// carrying only listening time). That still counts the listening and closes the
// session, but never moves the place: reading "nothing" as 0:00 would reset people.
func (s *Store) Sync(ctx context.Context, userID int64, sessionID string, position *float64, listened, duration float64, closeIt bool) (Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.GetSession(ctx, userID, sessionID)
	if err != nil {
		return Decision{}, err
	}
	now := s.nowMs()
	// Listening can't exceed the wall time since the last report (plus jitter): a buggy
	// or replayed report can't inflate anyone's totals.
	maxListen := float64(now-sess.LastAt)/1000 + listenSlack
	listened = math.Min(math.Max(0, sanitize(listened)), maxListen)

	var d Decision
	if position == nil {
		p, _, err := s.progress(ctx, userID, sess.ItemKey)
		if err != nil {
			return Decision{}, err
		}
		d = Decision{Progress: p, Reason: "no-position"}
	} else {
		d, err = s.apply(ctx, userID, sess.ItemKey, Report{
			Kind: Live, Position: *position, Duration: duration, Listened: listened, At: now,
			SessionID: sess.ID, Device: sess.Device,
		})
		if err != nil {
			return Decision{}, err
		}
		sess.CurPos = clamp(sanitize(*position), duration)
	}
	sess.Listened += listened
	sess.LastAt = now
	closed := 0
	if closeIt {
		closed = 1
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE listen_sessions SET last_at = ?, cur_pos = ?, listened = ?, closed = ? WHERE id = ?`,
		now, sess.CurPos, sess.Listened, closed, sess.ID); err != nil {
		return Decision{}, err
	}
	return d, s.touchLog(ctx, sess, 0)
}

// OfflineSession is a session played without a connection, uploaded later. Uploading
// the same session again updates it rather than counting it twice.
type OfflineSession struct {
	ID        string
	ItemKey   string
	DeviceID  string
	Device    string
	Client    string
	StartTime float64
	Position  float64
	Duration  float64
	Listened  float64 // total seconds listened in the session
	StartedAt int64   // phone clock, ms
	UpdatedAt int64   // phone clock, ms
}

// SyncOffline records an offline session and applies its final position if it is newer
// than the saved place.
func (s *Store) SyncOffline(ctx context.Context, userID int64, o OfflineSession) (Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.ID == "" || o.ItemKey == "" {
		return Decision{}, errors.New("session id and item are required")
	}
	now := s.nowMs()
	if o.UpdatedAt <= 0 || o.UpdatedAt > now+5*60*1000 {
		// A phone clock far in the future would make this session outrank everything.
		o.UpdatedAt = now
	}
	if o.StartedAt <= 0 || o.StartedAt > o.UpdatedAt {
		o.StartedAt = o.UpdatedAt
	}
	span := float64(o.UpdatedAt-o.StartedAt)/1000 + 60
	listened := math.Min(math.Max(0, sanitize(o.Listened)), span)

	var existing int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM listen_sessions WHERE id = ? AND user_id = ?`, o.ID, userID).Scan(&existing)
	if existing == 0 {
		var other int
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM listen_sessions WHERE id = ?`, o.ID).Scan(&other)
		if other > 0 {
			return Decision{}, errors.New("session id belongs to another user")
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO listen_sessions (id, user_id, item_key, device_id, device, client, offline, started_at, last_at, start_pos, cur_pos, listened, closed)
			 VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, 1)`,
			o.ID, userID, o.ItemKey, o.DeviceID, o.Device, o.Client, o.StartedAt, o.UpdatedAt,
			sanitize(o.StartTime), sanitize(o.Position), listened); err != nil {
			return Decision{}, err
		}
	} else if _, err := s.db.ExecContext(ctx,
		`UPDATE listen_sessions SET last_at = MAX(last_at, ?), cur_pos = ?, listened = MAX(listened, ?) WHERE id = ?`,
		o.UpdatedAt, sanitize(o.Position), listened, o.ID); err != nil {
		return Decision{}, err
	}
	d, err := s.apply(ctx, userID, o.ItemKey, Report{
		Kind: Offline, Position: o.Position, Duration: o.Duration, Listened: listened, At: o.UpdatedAt,
		SessionID: o.ID, Device: o.Device,
	})
	if err != nil {
		return Decision{}, err
	}
	sess := Session{ID: o.ID, UserID: userID, Device: o.Device, Client: o.Client, StartedAt: o.StartedAt, LastAt: o.UpdatedAt, Listened: listened}
	return d, s.touchLog(ctx, sess, listened)
}

// SetProgress applies an explicit user action: mark finished or not, set a position.
func (s *Store) SetProgress(ctx context.Context, userID int64, itemKey string, position, duration float64, finished *bool, device string) (Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apply(ctx, userID, itemKey, Report{Kind: Manual, Position: position, Duration: duration, Finished: finished, At: s.nowMs(), Device: device})
}

// ReportPosition applies a position an app set without playing (Audiobookshelf's
// progress PATCH) under the same guards as everything else, rather than letting it
// always win. unfinish is the app marking the book not finished. appAt is when the app
// says the position was set (unix ms; 0 when it didn't say).
func (s *Store) ReportPosition(ctx context.Context, userID int64, itemKey string, position, duration float64, unfinish bool, appAt int64, device string) (Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowMs()
	at := appAt
	// The app's own time is only believed when it's clearly in the past (and in
	// milliseconds — anything before 2001 is seconds or junk): a phone clock a little
	// behind mustn't make a seek it has just made look older than the place it left.
	if at < 1e12 || at > now-reportSkew.Milliseconds() {
		at = now
		// Heard just now, so it's newer than whatever is saved — even a place an offline
		// upload stamped a few minutes ahead by a fast phone clock.
		cur, found, err := s.progress(ctx, userID, itemKey)
		if err != nil {
			return Decision{}, err
		}
		if found && cur.UpdatedAt >= at {
			at = cur.UpdatedAt + 1
		}
	}
	r := Report{Kind: Reported, Position: position, Duration: duration, At: at, Device: device}
	if unfinish {
		f := false
		r.Finished = &f
	}
	return s.apply(ctx, userID, itemKey, r)
}

// ImportProgress brings in a place from another server (the Audiobookshelf import). It
// only applies when there's no saved place yet or the imported one is newer.
func (s *Store) ImportProgress(ctx context.Context, userID int64, itemKey string, position, duration float64, finished bool, finishedAt, updatedAt int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, exists, err := s.progressAny(ctx, userID, itemKey)
	if err != nil {
		return false, err
	}
	if updatedAt <= 0 {
		updatedAt = s.nowMs()
	}
	found := exists && cur.DiscardedAt == 0
	if found && cur.UpdatedAt >= updatedAt || exists && !found && cur.DiscardedAt >= updatedAt {
		// Older than the saved place, or than the moment an app removed it.
		return false, nil
	}
	if found {
		if err := s.recordBefore(ctx, cur); err != nil {
			return false, err
		}
	}
	p := Progress{UserID: userID, ItemKey: itemKey, Position: clamp(sanitize(position), sanitize(duration)),
		Duration: sanitize(duration), Finished: finished, UpdatedAt: updatedAt, Device: "Audiobookshelf import"}
	if finished {
		p.FinishedAt = finishedAt
		if p.FinishedAt <= 0 {
			p.FinishedAt = updatedAt
		}
	}
	if err := s.writeProgress(ctx, p); err != nil {
		return false, err
	}
	return true, s.recordHistory(ctx, p, KindApplied, "import", false)
}

// HideProgress removes an item from "continue listening" without forgetting the place.
func (s *Store) HideProgress(ctx context.Context, userID int64, itemKey string, hidden bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listen_progress SET hidden = ? WHERE user_id = ? AND item_key = ?`, boolInt(hidden), userID, itemKey)
	return err
}

// DeleteProgress is an app removing a place ("discard progress"). It's a soft delete:
// apps stop seeing the place at once, but the person can put it back from Arrmada for
// RemovedRetention. device names who removed it, for the "Recently removed" card.
// Nothing an app sends later brings the removed place back by itself: a new report
// starts a fresh place, and the old one stays on the timeline.
func (s *Store) DeleteProgress(ctx context.Context, userID int64, itemKey, device string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, found, err := s.progress(ctx, userID, itemKey)
	if err != nil || !found {
		return err
	}
	now := s.nowMs()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE listen_progress SET discarded_at = ? WHERE user_id = ? AND item_key = ? AND discarded_at = 0`,
		now, userID, itemKey); err != nil {
		return err
	}
	// Spots offered from before the removal belong to the place that was removed; they
	// mustn't turn up as "a later spot" on a fresh start.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE listen_history SET dismissed = 1 WHERE user_id = ? AND item_key = ? AND kind = ? AND dismissed = 0`,
		userID, itemKey, KindRejected); err != nil {
		return err
	}
	if device == "" {
		device = "app"
	}
	return s.insertHistory(ctx, histRow{userID: userID, key: itemKey, kind: KindDiscarded, pos: cur.Position, at: now,
		device: device, reason: "discard"})
}

// apply runs Decide against the stored place and writes the result. Caller holds mu.
func (s *Store) apply(ctx context.Context, userID int64, itemKey string, r Report) (Decision, error) {
	return s.applyAs(ctx, userID, itemKey, r, "")
}

// applyAs is apply with the timeline row for the change written as a restore when as is
// "restore" (the person putting a place back), rather than an ordinary applied row.
//
// Around the decision it keeps the place's timeline: a report that isn't used is
// recorded (and may be offered back), a hold is recorded when it starts, and before any
// non-routine change the place it replaces is kept, so every move can be undone.
func (s *Store) applyAs(ctx context.Context, userID int64, itemKey string, r Report, as string) (Decision, error) {
	r.Position, r.Duration, r.Listened = sanitize(r.Position), sanitize(r.Duration), sanitize(r.Listened)
	stored, exists, err := s.progressAny(ctx, userID, itemKey)
	if err != nil {
		return Decision{}, err
	}
	found := exists && stored.DiscardedAt == 0
	var d Decision
	afterDiscard := false
	switch {
	case found:
		d = Decide(&stored, r)
	case exists && (r.Kind == Offline || r.Kind == Reported) && r.At <= stored.DiscardedAt:
		// Listening from before an app removed the place (an offline upload that queued
		// up, a stale PATCH): it must not bring the removed place back in a new guise.
		d = Decision{Progress: Progress{UserID: userID, ItemKey: itemKey}, Reason: "older"}
		afterDiscard = true
	default:
		d = Decide(nil, r)
	}
	d.Progress.UserID, d.Progress.ItemKey, d.Progress.DiscardedAt = userID, itemKey, 0
	if !d.Dirty {
		if d.Reason == "older" || d.Reason == "unproven" {
			dur := r.Duration
			if dur <= 0 && found {
				dur = stored.Duration
			}
			if err := s.recordRejected(ctx, userID, itemKey, r, clamp(r.Position, dur), d.Reason, afterDiscard); err != nil {
				return Decision{}, err
			}
		}
		return d, nil
	}
	// Ordinary listening forwards (or a small skip back) is routine; anything else that
	// moves the place — a rewind, a restore, an upload, a finish — keeps the place it
	// replaces on the timeline first.
	flipped := found && d.Progress.Finished != stored.Finished
	routine := (d.Reason == "forward" || d.Reason == "back") && !flipped && as == ""
	if found && d.Changed && !routine {
		if err := s.recordBefore(ctx, stored); err != nil {
			return Decision{}, err
		}
	}
	if err := s.writeProgress(ctx, d.Progress); err != nil {
		return Decision{}, err
	}
	if found && stored.PendingPosition == nil && d.Progress.PendingPosition != nil {
		// A hold starting. (Listen again's restart hold is set when the session opens,
		// not here, so it isn't recorded as a glitch.)
		if err := s.insertHistory(ctx, histRow{userID: userID, key: itemKey, kind: KindHeld, pos: *d.Progress.PendingPosition,
			at: r.At, device: r.Device, reason: d.Reason}); err != nil {
			return Decision{}, err
		}
	}
	if d.Changed {
		kind, reason := KindApplied, d.Reason
		if as == "restore" {
			kind, reason = KindRestored, "restore"
		}
		if err := s.recordHistory(ctx, d.Progress, kind, reason, routine); err != nil {
			return Decision{}, err
		}
	}
	return d, nil
}

func (s *Store) writeProgress(ctx context.Context, p Progress) error {
	var pending any
	if p.PendingPosition != nil {
		pending = *p.PendingPosition
	}
	// Writing a place always makes it live: a removed place only comes back through
	// Undiscard, and a report after a removal starts afresh (the caller decided it
	// against no place at all), so the row it overwrites is the removed one.
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO listen_progress (user_id, item_key, position, duration, finished, finished_at, updated_at, device, session_id,
		   pending_position, pending_session, pending_listened, pending_at, hidden, discarded_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0)
		 ON CONFLICT(user_id, item_key) DO UPDATE SET position = excluded.position, duration = excluded.duration,
		   finished = excluded.finished, finished_at = excluded.finished_at, updated_at = excluded.updated_at,
		   device = excluded.device, session_id = excluded.session_id, pending_position = excluded.pending_position,
		   pending_session = excluded.pending_session, pending_listened = excluded.pending_listened,
		   pending_at = excluded.pending_at,
		   hidden = CASE WHEN excluded.position != listen_progress.position OR listen_progress.discarded_at != 0
		     THEN 0 ELSE listen_progress.hidden END,
		   discarded_at = 0`,
		p.UserID, p.ItemKey, p.Position, p.Duration, boolInt(p.Finished), p.FinishedAt, p.UpdatedAt, p.Device, p.SessionID,
		pending, p.PendingSession, p.PendingListened, p.PendingAt)
	return err
}

// recordHistory writes the timeline row for a change of place. A routine one (ordinary
// listening forwards or a small skip back) is written at most every historyGap.
func (s *Store) recordHistory(ctx context.Context, p Progress, kind, reason string, routine bool) error {
	if routine {
		var lastAt int64
		_ = s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(at), 0) FROM listen_history WHERE user_id = ? AND item_key = ? AND kind IN (?, ?)`,
			p.UserID, p.ItemKey, KindApplied, KindRestored).Scan(&lastAt)
		if p.UpdatedAt-lastAt < historyGap.Milliseconds() {
			return nil
		}
	}
	return s.insertHistory(ctx, histRow{userID: p.UserID, key: p.ItemKey, kind: kind, pos: p.Position, at: s.nowMs(),
		device: p.Device, reason: reason})
}

// recordBefore keeps the place a non-routine change is about to replace, unless the
// newest place on the timeline already says about the same. Routine listening is only
// written every few minutes, so without this the spot just before a jump was often lost.
func (s *Store) recordBefore(ctx context.Context, cur Progress) error {
	var last float64
	err := s.db.QueryRowContext(ctx,
		`SELECT position FROM listen_history WHERE user_id = ? AND item_key = ? AND kind IN (?, ?, ?)
		 ORDER BY id DESC LIMIT 1`, cur.UserID, cur.ItemKey, KindApplied, KindBefore, KindRestored).Scan(&last)
	switch {
	case err == nil && math.Abs(last-cur.Position) <= beforeNear:
		return nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}
	return s.insertHistory(ctx, histRow{userID: cur.UserID, key: cur.ItemKey, kind: KindBefore, pos: cur.Position,
		at: cur.UpdatedAt, device: cur.Device, reason: "before"})
}

// recordRejected keeps a place an app sent that wasn't used. An app re-sending the same
// report doesn't add a second row. quiet rows (from before the place was removed) stay
// on the timeline but are never offered.
func (s *Store) recordRejected(ctx context.Context, userID int64, itemKey string, r Report, pos float64, reason string, quiet bool) error {
	var dup int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM listen_history WHERE user_id = ? AND item_key = ? AND kind = ? AND position = ? AND at = ? AND device = ?`,
		userID, itemKey, KindRejected, pos, r.At, r.Device).Scan(&dup); err != nil {
		return err
	}
	if dup > 0 {
		return nil
	}
	return s.insertHistory(ctx, histRow{userID: userID, key: itemKey, kind: KindRejected, pos: pos, at: r.At,
		device: r.Device, reason: reason, dismissed: quiet})
}

// histRow is one timeline row to write.
type histRow struct {
	userID    int64
	key       string
	kind      string
	pos       float64
	at        int64
	device    string
	reason    string
	dismissed bool
}

// insertHistory adds a timeline row and prunes the item's timeline: the newest
// historyKeep places (applied and before rows) and historyKeepOther of everything else.
func (s *Store) insertHistory(ctx context.Context, h histRow) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO listen_history (user_id, item_key, position, at, device, reason, kind, dismissed) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		h.userID, h.key, h.pos, h.at, h.device, h.reason, h.kind, boolInt(h.dismissed)); err != nil {
		return err
	}
	place := h.kind == KindApplied || h.kind == KindBefore
	keep := historyKeepOther
	if place {
		keep = historyKeep
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM listen_history WHERE user_id = ? AND item_key = ? AND (kind IN (?, ?)) = ? AND id NOT IN (
		   SELECT id FROM listen_history WHERE user_id = ? AND item_key = ? AND (kind IN (?, ?)) = ?
		   ORDER BY at DESC, id DESC LIMIT ?)`,
		h.userID, h.key, KindApplied, KindBefore, boolInt(place), h.userID, h.key, KindApplied, KindBefore, boolInt(place), keep)
	return err
}

// touchLog keeps the session's row in the listening log current. The log has no item
// column — the admin overview built from it can't show what anyone listened to.
// setListened > 0 sets the total (offline uploads); otherwise it mirrors the session.
func (s *Store) touchLog(ctx context.Context, sess Session, setListened float64) error {
	seconds := sess.Listened
	if setListened > 0 {
		seconds = setListened
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO listen_log (session_id, user_id, device, client, started_at, ended_at, seconds) VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET ended_at = MAX(listen_log.ended_at, excluded.ended_at),
		   seconds = MAX(listen_log.seconds, excluded.seconds)`,
		sess.ID, sess.UserID, sess.Device, sess.Client, sess.StartedAt, sess.LastAt, seconds)
	return err
}

// Progress returns the user's place in one item. A place an app removed isn't one.
func (s *Store) Progress(ctx context.Context, userID int64, itemKey string) (Progress, bool, error) {
	return s.progress(ctx, userID, itemKey)
}

// progress is the live place: a removed one reads as no place at all, so apps don't see
// it and a new report starts afresh.
func (s *Store) progress(ctx context.Context, userID int64, itemKey string) (Progress, bool, error) {
	p, found, err := s.progressAny(ctx, userID, itemKey)
	if err != nil || !found || p.DiscardedAt != 0 {
		return Progress{UserID: userID, ItemKey: itemKey}, false, err
	}
	return p, true, nil
}

// progressAny reads the stored row, removed or not.
func (s *Store) progressAny(ctx context.Context, userID int64, itemKey string) (Progress, bool, error) {
	rows, err := s.db.QueryContext(ctx, progressSelect+` WHERE user_id = ? AND item_key = ?`, userID, itemKey)
	if err != nil {
		return Progress{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Progress{UserID: userID, ItemKey: itemKey}, false, rows.Err()
	}
	p, err := scanProgress(rows)
	return p, err == nil, err
}

// AllProgress returns every saved place for a user, most recent first (removed ones
// left out).
func (s *Store) AllProgress(ctx context.Context, userID int64) ([]Progress, error) {
	return s.progressList(ctx, ` WHERE user_id = ? AND discarded_at = 0 ORDER BY updated_at DESC`, userID)
}

func (s *Store) progressList(ctx context.Context, where string, args ...any) ([]Progress, error) {
	rows, err := s.db.QueryContext(ctx, progressSelect+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Progress
	for rows.Next() {
		p, err := scanProgress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const progressSelect = `SELECT user_id, item_key, position, duration, finished, finished_at, updated_at, device, session_id,
	pending_position, pending_session, pending_listened, pending_at, hidden, discarded_at FROM listen_progress`

func scanProgress(rows *sql.Rows) (Progress, error) {
	var p Progress
	var fin, hid int
	var pending sql.NullFloat64
	if err := rows.Scan(&p.UserID, &p.ItemKey, &p.Position, &p.Duration, &fin, &p.FinishedAt, &p.UpdatedAt, &p.Device,
		&p.SessionID, &pending, &p.PendingSession, &p.PendingListened, &p.PendingAt, &hid, &p.DiscardedAt); err != nil {
		return Progress{}, err
	}
	p.Finished, p.Hidden = fin != 0, hid != 0
	if pending.Valid {
		v := pending.Float64
		p.PendingPosition = &v
	}
	return p, nil
}

// Removed is a place an app removed, still restorable.
type Removed struct {
	Progress
	// By is who removed it (the app's name, or "app").
	By string `json:"by,omitempty"`
}

// Discarded lists the places an app removed in the last RemovedRetention, newest
// removal first.
func (s *Store) Discarded(ctx context.Context, userID int64) ([]Removed, error) {
	list, err := s.progressList(ctx, ` WHERE user_id = ? AND discarded_at > ? ORDER BY discarded_at DESC`,
		userID, s.now().Add(-RemovedRetention).UnixMilli())
	if err != nil {
		return nil, err
	}
	out := make([]Removed, 0, len(list))
	for _, p := range list {
		r := Removed{Progress: p}
		_ = s.db.QueryRowContext(ctx,
			`SELECT device FROM listen_history WHERE user_id = ? AND item_key = ? AND kind = ? ORDER BY id DESC LIMIT 1`,
			userID, p.ItemKey, KindDiscarded).Scan(&r.By)
		out = append(out, r)
	}
	return out, nil
}

// Undiscard puts back a place an app removed, exactly as it was.
func (s *Store) Undiscard(ctx context.Context, userID int64, itemKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, exists, err := s.progressAny(ctx, userID, itemKey)
	if err != nil {
		return err
	}
	if !exists || p.DiscardedAt == 0 {
		return ErrNothingToRestore
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE listen_progress SET discarded_at = 0 WHERE user_id = ? AND item_key = ?`,
		userID, itemKey); err != nil {
		return err
	}
	return s.insertHistory(ctx, histRow{userID: userID, key: itemKey, kind: KindRestored, pos: p.Position, at: s.nowMs(),
		device: "Arrmada", reason: "undiscard"})
}

// HistoryEntry is one row of a place's timeline.
type HistoryEntry struct {
	ID       int64   `json:"id"`
	Position float64 `json:"position"`
	At       int64   `json:"at"`
	Device   string  `json:"device,omitempty"`
	Reason   string  `json:"reason"`
	// Kind is applied, before, rejected, held, discarded or restored.
	Kind string `json:"kind"`
	// Dismissed: a rejected place the person said no to (or one from before the place
	// was removed); it's never offered again.
	Dismissed bool `json:"dismissed,omitempty"`
}

const historySelect = `SELECT id, position, at, device, reason, kind, dismissed FROM listen_history`

func scanHistory(rows *sql.Rows) (HistoryEntry, error) {
	var h HistoryEntry
	var dis int
	err := rows.Scan(&h.ID, &h.Position, &h.At, &h.Device, &h.Reason, &h.Kind, &dis)
	h.Dismissed = dis != 0
	return h, err
}

// History lists a user's timeline for one item, newest first.
func (s *Store) History(ctx context.Context, userID int64, itemKey string) ([]HistoryEntry, error) {
	rows, err := s.db.QueryContext(ctx, historySelect+` WHERE user_id = ? AND item_key = ? ORDER BY at DESC, id DESC`,
		userID, itemKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		h, err := scanHistory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Offers finds, per item, a later spot an app sent that wasn't used — the newest one
// not dismissed, at least offerMin past the saved place and no older than offerMaxAge.
// That's the plane scenario: hours of offline listening, rejected because a tablet
// opened the book in between, offered back rather than silently lost.
func (s *Store) Offers(ctx context.Context, userID int64) (map[string]HistoryEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT h.item_key, h.id, h.position, h.at, h.device, h.reason, h.kind, h.dismissed
		 FROM listen_history h JOIN listen_progress p ON p.user_id = h.user_id AND p.item_key = h.item_key
		 WHERE h.user_id = ? AND h.kind = ? AND h.dismissed = 0 AND p.discarded_at = 0 AND p.finished = 0
		   AND h.position > p.position + ? AND h.at > ?
		 ORDER BY h.at DESC, h.id DESC`,
		userID, KindRejected, offerMin, s.now().Add(-offerMaxAge).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]HistoryEntry{}
	for rows.Next() {
		var key string
		var h HistoryEntry
		var dis int
		if err := rows.Scan(&key, &h.ID, &h.Position, &h.At, &h.Device, &h.Reason, &h.Kind, &dis); err != nil {
			return nil, err
		}
		if _, seen := out[key]; !seen {
			out[key] = h
		}
	}
	return out, rows.Err()
}

// Dismiss marks a timeline row as seen and declined, so it's never offered again.
func (s *Store) Dismiss(ctx context.Context, userID int64, itemKey string, historyID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE listen_history SET dismissed = 1 WHERE id = ? AND user_id = ? AND item_key = ?`,
		historyID, userID, itemKey)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoHistory
	}
	return nil
}

// ErrNoPending is returned when there's no held jump to confirm.
var ErrNoPending = errors.New("there's no jump waiting to be confirmed")

// AcceptPending makes a held jump back the saved place now — the person confirming, in
// Arrmada, that going back really was them.
func (s *Store) AcceptPending(ctx context.Context, userID int64, itemKey string) (Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, found, err := s.progress(ctx, userID, itemKey)
	if err != nil {
		return Decision{}, err
	}
	if !found || cur.PendingPosition == nil {
		return Decision{}, ErrNoPending
	}
	f := false
	return s.apply(ctx, userID, itemKey, Report{Kind: Manual, Position: *cur.PendingPosition, Finished: &f, At: s.nowMs(), Device: "Arrmada"})
}

// Restore puts a user's place back to one on its timeline. Using a spot an app sent
// that wasn't used also settles that offer.
func (s *Store) Restore(ctx context.Context, userID int64, itemKey string, historyID int64) (Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pos float64
	var kind string
	err := s.db.QueryRowContext(ctx,
		`SELECT position, kind FROM listen_history WHERE id = ? AND user_id = ? AND item_key = ?`, historyID, userID, itemKey).
		Scan(&pos, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return Decision{}, ErrNoHistory
	}
	if err != nil {
		return Decision{}, err
	}
	f := false
	d, err := s.applyAs(ctx, userID, itemKey, Report{Kind: Manual, Position: pos, Finished: &f, At: s.nowMs(), Device: "Arrmada"}, "restore")
	if err != nil {
		return Decision{}, err
	}
	if kind == KindRejected {
		if _, err := s.db.ExecContext(ctx, `UPDATE listen_history SET dismissed = 1 WHERE id = ?`, historyID); err != nil {
			return Decision{}, err
		}
	}
	return d, nil
}

// Bookmark is a saved spot with a note.
type Bookmark struct {
	ItemKey   string  `json:"item_key"`
	Time      float64 `json:"time"`
	Title     string  `json:"title"`
	CreatedAt int64   `json:"created_at"`
}

// AddBookmark saves (or renames) a bookmark.
func (s *Store) AddBookmark(ctx context.Context, userID int64, itemKey string, t float64, title string) (Bookmark, error) {
	b := Bookmark{ItemKey: itemKey, Time: math.Floor(sanitize(t)), Title: title, CreatedAt: s.nowMs()}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO listen_bookmarks (user_id, item_key, time, title, created_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, item_key, time) DO UPDATE SET title = excluded.title`,
		userID, itemKey, b.Time, title, b.CreatedAt)
	return b, err
}

// DeleteBookmark removes the bookmark at a time.
func (s *Store) DeleteBookmark(ctx context.Context, userID int64, itemKey string, t float64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM listen_bookmarks WHERE user_id = ? AND item_key = ? AND time = ?`,
		userID, itemKey, math.Floor(t))
	return err
}

// Bookmarks lists a user's bookmarks (all items when itemKey is empty).
func (s *Store) Bookmarks(ctx context.Context, userID int64, itemKey string) ([]Bookmark, error) {
	q := `SELECT item_key, time, title, created_at FROM listen_bookmarks WHERE user_id = ?`
	args := []any{userID}
	if itemKey != "" {
		q += ` AND item_key = ?`
		args = append(args, itemKey)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY item_key, time`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bookmark{}
	for rows.Next() {
		var b Bookmark
		if err := rows.Scan(&b.ItemKey, &b.Time, &b.Title, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Prune drops play sessions idle longer than SessionRetention (the listening log keeps
// the when-and-how-long; the link to a book goes with the session) and places removed
// longer ago than RemovedRetention.
func (s *Store) Prune(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM listen_sessions WHERE last_at < ?`, s.now().Add(-SessionRetention).UnixMilli()); err != nil {
		return err
	}
	// A removed place is restorable for RemovedRetention; after that it's gone (its
	// timeline stays, so "Go back here" still works if the book is started again).
	_, err := s.db.ExecContext(ctx, `DELETE FROM listen_progress WHERE discarded_at > 0 AND discarded_at < ?`,
		s.now().Add(-RemovedRetention).UnixMilli())
	return err
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // UUID v4 shape, which Audiobookshelf clients expect
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func sanitize(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
