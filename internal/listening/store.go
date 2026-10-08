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

const (
	// historyKeep is how many earlier places are kept per user and item.
	historyKeep = 50
	// historyGap: a routine position change is written to history at most this often
	// per item; jumps, restores and manual changes always are.
	historyGap = 5 * time.Minute
	// listenSlack is how much listening one live report may claim beyond the wall time
	// since the previous one (report timing jitter).
	listenSlack = 15.0
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
}

// OpenSession starts a play session at the user's saved place (or 0).
func (s *Store) OpenSession(ctx context.Context, userID int64, itemKey, deviceID, device, client string) (Session, Progress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowMs()
	p, _, err := s.progress(ctx, userID, itemKey)
	if err != nil {
		return Session{}, Progress{}, err
	}
	sess := Session{ID: newID(), UserID: userID, ItemKey: itemKey, DeviceID: deviceID, Device: device, Client: client,
		StartedAt: now, LastAt: now, StartPos: p.Position, CurPos: p.Position}
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

// ImportProgress brings in a place from another server (the Audiobookshelf import). It
// only applies when there's no saved place yet or the imported one is newer.
func (s *Store) ImportProgress(ctx context.Context, userID int64, itemKey string, position, duration float64, finished bool, finishedAt, updatedAt int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, found, err := s.progress(ctx, userID, itemKey)
	if err != nil {
		return false, err
	}
	if updatedAt <= 0 {
		updatedAt = s.nowMs()
	}
	if found && cur.UpdatedAt >= updatedAt {
		return false, nil
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
	return true, s.recordHistory(ctx, p, "import")
}

// HideProgress removes an item from "continue listening" without forgetting the place.
func (s *Store) HideProgress(ctx context.Context, userID int64, itemKey string, hidden bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listen_progress SET hidden = ? WHERE user_id = ? AND item_key = ?`, boolInt(hidden), userID, itemKey)
	return err
}

// DeleteProgress forgets a place (the history stays, so it can be put back).
func (s *Store) DeleteProgress(ctx context.Context, userID int64, itemKey string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM listen_progress WHERE user_id = ? AND item_key = ?`, userID, itemKey)
	return err
}

// apply runs Decide against the stored place and writes the result. Caller holds mu.
func (s *Store) apply(ctx context.Context, userID int64, itemKey string, r Report) (Decision, error) {
	r.Position, r.Duration, r.Listened = sanitize(r.Position), sanitize(r.Duration), sanitize(r.Listened)
	cur, found, err := s.progress(ctx, userID, itemKey)
	if err != nil {
		return Decision{}, err
	}
	var d Decision
	if found {
		d = Decide(&cur, r)
	} else {
		d = Decide(nil, r)
	}
	d.Progress.UserID, d.Progress.ItemKey = userID, itemKey
	if !d.Dirty {
		return d, nil
	}
	if err := s.writeProgress(ctx, d.Progress); err != nil {
		return Decision{}, err
	}
	if d.Changed {
		if err := s.recordHistory(ctx, d.Progress, d.Reason); err != nil {
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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO listen_progress (user_id, item_key, position, duration, finished, finished_at, updated_at, device, session_id,
		   pending_position, pending_session, pending_listened, pending_at, hidden)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
		 ON CONFLICT(user_id, item_key) DO UPDATE SET position = excluded.position, duration = excluded.duration,
		   finished = excluded.finished, finished_at = excluded.finished_at, updated_at = excluded.updated_at,
		   device = excluded.device, session_id = excluded.session_id, pending_position = excluded.pending_position,
		   pending_session = excluded.pending_session, pending_listened = excluded.pending_listened,
		   pending_at = excluded.pending_at,
		   hidden = CASE WHEN excluded.position != listen_progress.position THEN 0 ELSE listen_progress.hidden END`,
		p.UserID, p.ItemKey, p.Position, p.Duration, boolInt(p.Finished), p.FinishedAt, p.UpdatedAt, p.Device, p.SessionID,
		pending, p.PendingSession, p.PendingListened, p.PendingAt)
	return err
}

func (s *Store) recordHistory(ctx context.Context, p Progress, reason string) error {
	routine := reason == "forward" || reason == "back"
	if routine {
		var lastAt int64
		_ = s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(at), 0) FROM listen_history WHERE user_id = ? AND item_key = ?`, p.UserID, p.ItemKey).Scan(&lastAt)
		if p.UpdatedAt-lastAt < historyGap.Milliseconds() {
			return nil
		}
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO listen_history (user_id, item_key, position, at, device, reason) VALUES (?, ?, ?, ?, ?, ?)`,
		p.UserID, p.ItemKey, p.Position, s.nowMs(), p.Device, reason); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM listen_history WHERE user_id = ? AND item_key = ? AND id NOT IN (
		   SELECT id FROM listen_history WHERE user_id = ? AND item_key = ? ORDER BY at DESC, id DESC LIMIT ?)`,
		p.UserID, p.ItemKey, p.UserID, p.ItemKey, historyKeep)
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

// Progress returns the user's place in one item.
func (s *Store) Progress(ctx context.Context, userID int64, itemKey string) (Progress, bool, error) {
	return s.progress(ctx, userID, itemKey)
}

func (s *Store) progress(ctx context.Context, userID int64, itemKey string) (Progress, bool, error) {
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

// AllProgress returns every saved place for a user, most recent first.
func (s *Store) AllProgress(ctx context.Context, userID int64) ([]Progress, error) {
	rows, err := s.db.QueryContext(ctx, progressSelect+` WHERE user_id = ? ORDER BY updated_at DESC`, userID)
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
	pending_position, pending_session, pending_listened, pending_at, hidden FROM listen_progress`

func scanProgress(rows *sql.Rows) (Progress, error) {
	var p Progress
	var fin, hid int
	var pending sql.NullFloat64
	if err := rows.Scan(&p.UserID, &p.ItemKey, &p.Position, &p.Duration, &fin, &p.FinishedAt, &p.UpdatedAt, &p.Device,
		&p.SessionID, &pending, &p.PendingSession, &p.PendingListened, &p.PendingAt, &hid); err != nil {
		return Progress{}, err
	}
	p.Finished, p.Hidden = fin != 0, hid != 0
	if pending.Valid {
		v := pending.Float64
		p.PendingPosition = &v
	}
	return p, nil
}

// HistoryEntry is an earlier saved place.
type HistoryEntry struct {
	ID       int64   `json:"id"`
	Position float64 `json:"position"`
	At       int64   `json:"at"`
	Device   string  `json:"device,omitempty"`
	Reason   string  `json:"reason"`
}

// History lists a user's earlier places in one item, newest first.
func (s *Store) History(ctx context.Context, userID int64, itemKey string) ([]HistoryEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, position, at, device, reason FROM listen_history WHERE user_id = ? AND item_key = ? ORDER BY at DESC, id DESC`,
		userID, itemKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var h HistoryEntry
		if err := rows.Scan(&h.ID, &h.Position, &h.At, &h.Device, &h.Reason); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
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

// Restore puts a user's place back to an earlier saved one.
func (s *Store) Restore(ctx context.Context, userID int64, itemKey string, historyID int64) (Decision, error) {
	var pos float64
	err := s.db.QueryRowContext(ctx,
		`SELECT position FROM listen_history WHERE id = ? AND user_id = ? AND item_key = ?`, historyID, userID, itemKey).Scan(&pos)
	if errors.Is(err, sql.ErrNoRows) {
		return Decision{}, errors.New("that earlier place no longer exists")
	}
	if err != nil {
		return Decision{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := false
	d, err := s.apply(ctx, userID, itemKey, Report{Kind: Manual, Position: pos, Finished: &f, At: s.nowMs(), Device: "Arrmada"})
	if err == nil && d.Changed {
		_, _ = s.db.ExecContext(ctx, `UPDATE listen_history SET reason = 'restore' WHERE id = (SELECT MAX(id) FROM listen_history WHERE user_id = ? AND item_key = ?)`, userID, itemKey)
	}
	return d, err
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

// Prune drops play sessions idle longer than SessionRetention. The listening log keeps
// the when-and-how-long; the link to a book goes with the session.
func (s *Store) Prune(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM listen_sessions WHERE last_at < ?`, s.now().Add(-SessionRetention).UnixMilli())
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
