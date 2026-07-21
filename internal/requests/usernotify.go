package requests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tristenlammi/arrmada/internal/notify"
)

// UserNotification is one in-app inbox entry for a requester.
type UserNotification struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	MediaType string `json:"media_type"`
	Ref       string `json:"ref"`
	Read      bool   `json:"read"`
	CreatedAt int64  `json:"created_at"`
}

// --- inbox + per-user Apprise (repo) ---

// addUserNotification inserts one inbox entry. The unique (user_id, ref) index
// makes it idempotent — inserted reports whether this call actually added a row
// (false = already notified), so callers can skip the Apprise push on repeats.
func (r *Repo) addUserNotification(ctx context.Context, userID int64, title, body, mediaType, ref string, at int64) (inserted bool, err error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO user_notifications (user_id, title, body, media_type, ref, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, title, body, mediaType, ref, at)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (r *Repo) listUserNotifications(ctx context.Context, userID int64) ([]UserNotification, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, title, body, media_type, ref, read, created_at FROM user_notifications WHERE user_id = ? ORDER BY created_at DESC LIMIT 100`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserNotification
	for rows.Next() {
		var n UserNotification
		var read int
		if err := rows.Scan(&n.ID, &n.Title, &n.Body, &n.MediaType, &n.Ref, &read, &n.CreatedAt); err != nil {
			return nil, err
		}
		n.Read = read != 0
		out = append(out, n)
	}
	return out, rows.Err()
}

func (r *Repo) markRead(ctx context.Context, id, userID int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE user_notifications SET read = 1 WHERE id = ? AND user_id = ?`, id, userID)
	return err
}
func (r *Repo) markAllRead(ctx context.Context, userID int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE user_notifications SET read = 1 WHERE user_id = ?`, userID)
	return err
}
func (r *Repo) unreadCount(ctx context.Context, userID int64) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_notifications WHERE user_id = ? AND read = 0`, userID).Scan(&n)
	return n, err
}
func (r *Repo) getUserApprise(ctx context.Context, userID int64) (string, error) {
	var url string
	err := r.db.QueryRowContext(ctx, `SELECT apprise_url FROM users WHERE id = ?`, userID).Scan(&url)
	if errors.Is(err, sql.ErrNoRows) { // e.g. the local-dev bypass user has no row
		return "", nil
	}
	return url, err
}
func (r *Repo) setUserApprise(ctx context.Context, userID int64, url string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE users SET apprise_url = ? WHERE id = ?`, url, userID)
	return err
}

// --- service surface (used by the /me endpoints) ---

func (s *Service) Inbox(ctx context.Context, userID int64) ([]UserNotification, error) {
	return s.repo.listUserNotifications(ctx, userID)
}
func (s *Service) UnreadCount(ctx context.Context, userID int64) (int, error) {
	return s.repo.unreadCount(ctx, userID)
}
func (s *Service) MarkRead(ctx context.Context, id, userID int64) error {
	return s.repo.markRead(ctx, id, userID)
}
func (s *Service) MarkAllRead(ctx context.Context, userID int64) error {
	return s.repo.markAllRead(ctx, userID)
}
func (s *Service) GetApprise(ctx context.Context, userID int64) (string, error) {
	return s.repo.getUserApprise(ctx, userID)
}
func (s *Service) SetApprise(ctx context.Context, userID int64, url string) error {
	return s.repo.setUserApprise(ctx, userID, url)
}

// --- the notifier: match imports back to requesters ---

// RunNotifier watches import events and notifies the requester (in-app inbox + optional Apprise
// push) when their request lands. Start once at boot.
func (s *Service) RunNotifier(ctx context.Context) {
	if s.bus == nil {
		return
	}
	movieCh, cancelM := s.bus.Subscribe("movie.downloaded")
	seriesCh, cancelS := s.bus.Subscribe("series.imported")
	bookCh, cancelB := s.bus.Subscribe("book.imported")
	defer cancelM()
	defer cancelS()
	defer cancelB()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-movieCh:
			if id, ok := evID(ev.Data); ok {
				if m, err := s.movies.Get(ctx, id); err == nil {
					s.notifyRequester(ctx, "movie", m.TMDBID, "")
				}
			}
		case ev := <-seriesCh:
			if id, ok := evID(ev.Data); ok {
				if sr, err := s.series.Get(ctx, id); err == nil {
					// A series isn't "ready to watch" on its first imported episode:
					// only notify once no monitored, aired episode is still wanted.
					// Later imports re-fire this event (and the ready-sweep backstops),
					// so skipping here just defers the notification.
					if !s.series.HasWantedEpisodes(ctx, sr.ID) {
						s.notifyRequester(ctx, "series", sr.TMDBID, "")
					}
				}
			}
		case ev := <-bookCh:
			if id, ok := evID(ev.Data); ok {
				if b, err := s.books.Get(ctx, id); err == nil {
					s.notifyRequester(ctx, "book", 0, b.OLKey)
				}
			}
		}
	}
}

// requestRef is the stable de-dupe key for a request's media ("movie:123",
// "series:456", "book:OL123W"). Suffixes distinguish notification kinds so an
// "approved" note doesn't block the later "ready" one.
func requestRef(req Request) string {
	if req.MediaType == "book" {
		return "book:" + req.OLKey
	}
	return fmt.Sprintf("%s:%d", req.MediaType, req.TMDBID)
}

// notifyRequester finds the request behind a just-imported item and alerts its
// requester plus everyone subscribed to it.
func (s *Service) notifyRequester(ctx context.Context, mediaType string, tmdbID int, olKey string) {
	var (
		req Request
		ok  bool
	)
	if mediaType == "book" {
		req, ok = s.repo.GetByBook(ctx, olKey)
	} else {
		req, ok = s.repo.GetByMedia(ctx, mediaType, tmdbID)
	}
	if !ok {
		return
	}
	s.notifyReady(ctx, req)
}

// notifyReady sends the "your request is ready" notification for one request.
// Idempotent per user (unique inbox ref), so callers may fire it repeatedly.
func (s *Service) notifyReady(ctx context.Context, req Request) {
	body := fmt.Sprintf("“%s” is ready to watch.", req.Title)
	if req.MediaType == "book" {
		body = fmt.Sprintf("“%s” is ready to read.", req.Title)
	}
	s.notifyParties(ctx, req, "Your request is ready", body, requestRef(req), "request-ready")
}

// notifyDecision tells the requester and subscribers a request was approved or declined.
func (s *Service) notifyDecision(ctx context.Context, req Request, approved bool) {
	if approved {
		body := fmt.Sprintf("Your request for “%s” was approved — we're on it.", req.Title)
		s.notifyParties(ctx, req, "Request approved", body, requestRef(req)+":approved", "request-approved")
		return
	}
	body := fmt.Sprintf("Your request for “%s” was declined.", req.Title)
	s.notifyParties(ctx, req, "Request declined", body, requestRef(req)+":declined", "request-declined")
}

// notifyParties fans one notification out to the requester and every subscriber:
// in-app inbox always, personal Apprise push when set. The unique (user_id, ref)
// inbox index de-dupes; the Apprise push only fires when the inbox row was new.
func (s *Service) notifyParties(ctx context.Context, req Request, title, body, ref, kind string) {
	seen := map[int64]bool{}
	var userIDs []int64
	if req.RequestedBy > 0 {
		seen[req.RequestedBy] = true
		userIDs = append(userIDs, req.RequestedBy)
	}
	if subs, err := s.repo.Subscribers(ctx, req.ID); err == nil {
		for _, sub := range subs {
			if sub.UserID > 0 && !seen[sub.UserID] {
				seen[sub.UserID] = true
				userIDs = append(userIDs, sub.UserID)
			}
		}
	} else {
		s.log.Warn(kind+": could not list subscribers", "request", req.ID, "err", err)
	}
	now := time.Now().Unix()
	for _, uid := range userIDs {
		inserted, err := s.repo.addUserNotification(ctx, uid, title, body, req.MediaType, ref, now)
		if err != nil {
			s.log.Warn(kind+": could not add inbox notification", "user", uid, "err", err)
			continue
		}
		if !inserted {
			continue // already notified — don't re-push
		}
		if s.appriseBin != "" {
			if url, err := s.repo.getUserApprise(ctx, uid); err == nil && url != "" {
				if err := notify.Send(ctx, s.appriseBin, "Arrmada", body, url); err != nil {
					s.log.Warn(kind+": apprise push failed", "user", uid, "err", err)
				}
			}
		}
		if s.push != nil {
			// Web Push to every device this user enabled it on. Async with its own
			// deadline — the import fan-out must never block on a push service. The
			// inbox insert above already deduped repeats, so this can't double-ping.
			s.push.SendToUserAsync(uid, title, body, "/discover")
		}
		s.log.Info(kind+" notified", "title", req.Title, "user", uid)
	}
}

// SweepReadyRequests is the notification backstop: every approved request whose
// media is now available gets the ready notification. It catches availability
// that arrived without an import event (library scans) and events dropped under
// load. Idempotent by construction — the unique inbox ref means an
// already-notified user is skipped — so it's safe on a short timer.
func (s *Service) SweepReadyRequests(ctx context.Context) error {
	reqs, err := s.repo.List(ctx, StatusApproved, 0)
	if err != nil {
		return err
	}
	if len(reqs) == 0 {
		return nil
	}
	movHave := map[int]bool{}
	if ms, err := s.movies.List(ctx); err == nil {
		for _, m := range ms {
			movHave[m.TMDBID] = m.HasFile
		}
	}
	type serInfo struct {
		id       int64
		hasFiles bool
	}
	serByTMDB := map[int]serInfo{}
	if ss, err := s.series.List(ctx); err == nil {
		for _, sr := range ss {
			serByTMDB[sr.TMDBID] = serInfo{id: sr.ID, hasFiles: sr.Stats != nil && sr.Stats.HaveFiles > 0}
		}
	}
	bookHave := map[string]bool{}
	if bs, err := s.books.List(ctx); err == nil {
		for _, b := range bs {
			bookHave[b.OLKey] = b.HasFile
		}
	}
	for i := range reqs {
		ready := false
		switch reqs[i].MediaType {
		case "movie":
			ready = movHave[reqs[i].TMDBID]
		case "series":
			// Ready = some files on disk AND nothing still wanted (monitored, aired,
			// missing) — the same completeness rule the import-event path applies.
			if info, ok := serByTMDB[reqs[i].TMDBID]; ok && info.hasFiles {
				ready = !s.series.HasWantedEpisodes(ctx, info.id)
			}
		case "book":
			ready = bookHave[reqs[i].OLKey]
		}
		if ready {
			s.notifyReady(ctx, reqs[i])
		}
	}
	return nil
}

// evID pulls the "id" field (int64) out of an event payload.
func evID(data any) (int64, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		return 0, false
	}
	switch v := m["id"].(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	}
	return 0, false
}
