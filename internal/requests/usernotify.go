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

func (r *Repo) addUserNotification(ctx context.Context, userID int64, title, body, mediaType, ref string, at int64) error {
	// Unique (user_id, ref) index makes this idempotent — a multi-part import notifies once.
	_, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO user_notifications (user_id, title, body, media_type, ref, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, title, body, mediaType, ref, at)
	return err
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
					s.notifyRequester(ctx, "series", sr.TMDBID, "")
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

// notifyRequester finds the request behind a just-imported item and alerts its requester.
func (s *Service) notifyRequester(ctx context.Context, mediaType string, tmdbID int, olKey string) {
	var (
		req Request
		ok  bool
		ref string
	)
	if mediaType == "book" {
		req, ok = s.repo.GetByBook(ctx, olKey)
		ref = "book:" + olKey
	} else {
		req, ok = s.repo.GetByMedia(ctx, mediaType, tmdbID)
		ref = fmt.Sprintf("%s:%d", mediaType, tmdbID)
	}
	if !ok || req.RequestedBy <= 0 {
		return
	}
	body := fmt.Sprintf("“%s” is ready to watch.", req.Title)
	if mediaType == "book" {
		body = fmt.Sprintf("“%s” is ready to read.", req.Title)
	}
	now := time.Now().Unix()
	if err := s.repo.addUserNotification(ctx, req.RequestedBy, "Your request is ready", body, mediaType, ref, now); err != nil {
		s.log.Warn("request-ready: could not add inbox notification", "err", err)
		return
	}
	// Optional personal Apprise push.
	if s.appriseBin != "" {
		if url, err := s.repo.getUserApprise(ctx, req.RequestedBy); err == nil && url != "" {
			if err := notify.Send(ctx, s.appriseBin, "Arrmada", body, url); err != nil {
				s.log.Warn("request-ready: apprise push failed", "user", req.RequestedBy, "err", err)
			}
		}
	}
	s.log.Info("request-ready notified", "title", req.Title, "user", req.RequestedBy)
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
