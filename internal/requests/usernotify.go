package requests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/notify"
	"github.com/tristenlammi/arrmada/internal/series"
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
func (s *Service) SetApprise(ctx context.Context, userID int64, url string) error {
	return s.repo.setUserApprise(ctx, userID, url)
}

// --- personal Apprise: kept off internal hosts ---

// userApprise is what personal pushes need beyond the apprise binary: who counts as
// staff (their URLs keep the ordinary rules), a resolver for the host check, and the
// transport. Tests set resolver and send.
type userApprise struct {
	isStaff  func(ctx context.Context, userID int64) bool
	resolver notify.Resolver
	send     func(ctx context.Context, url, title, body string) error
}

// SetStaffLookup says which users are staff, whose personal Apprise URLs keep the
// ordinary rules. Unset, everyone gets the requester rules — the safe side.
func (s *Service) SetStaffLookup(fn func(ctx context.Context, userID int64) bool) {
	s.userApprise.isStaff = fn
}

// checkUserApprise is ValidateUserAppriseURL for the URL's owner.
func (s *Service) checkUserApprise(ctx context.Context, userID int64, url string) error {
	staff := s.userApprise.isStaff != nil && s.userApprise.isStaff(ctx, userID)
	return notify.ValidateUserAppriseURL(ctx, url, staff, s.userApprise.resolver)
}

func (s *Service) sendUserApprise(ctx context.Context, url, body string) error {
	if s.userApprise.send != nil {
		return s.userApprise.send(ctx, url, "Arrmada", body)
	}
	if s.appriseBin == "" {
		return nil // no apprise in this build: the inbox and Web Push still went out
	}
	return notify.Send(ctx, s.appriseBin, "Arrmada", body, url)
}

// AppriseStatus is the owner's view of their personal URL: whether one is saved, a
// harmless hint of it, and why it's being skipped if it no longer passes the check.
// The URL itself never goes back out.
func (s *Service) AppriseStatus(ctx context.Context, userID int64, staff bool) (set bool, hint, blocked string, err error) {
	url, err := s.repo.getUserApprise(ctx, userID)
	if err != nil || url == "" {
		return false, "", "", err
	}
	if verr := notify.ValidateUserAppriseURL(ctx, url, staff, s.userApprise.resolver); verr != nil {
		blocked = verr.Error()
	}
	return true, notify.URLHint(url), blocked, nil
}

// --- the notifier: match imports back to requesters ---

// The import side tells the requester (in-app inbox, personal Apprise, Web Push) through
// these, run as outbox consumers: a row is written when the import is recorded and
// retried until it goes through, so a busy moment or a restart can't lose the message.
// Each is idempotent — the inbox's unique (user, ref) index means a repeat run notifies
// nobody twice — and a library item deleted meanwhile is simply nothing to do.

// NotifyMovieReady tells whoever asked for a movie that it has arrived.
func (s *Service) NotifyMovieReady(ctx context.Context, movieID int64) error {
	m, err := s.movies.Get(ctx, movieID)
	if errors.Is(err, movies.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.notifyRequester(ctx, "movie", m.TMDBID, "")
}

// NotifySeriesReady tells whoever asked for a show that it's ready — but only once no
// monitored, aired episode is still wanted: a series isn't "ready to watch" on its first
// imported episode. Later imports run this again (and the ready sweep backstops), so
// skipping just defers the message.
func (s *Service) NotifySeriesReady(ctx context.Context, seriesID int64) error {
	sr, err := s.series.Get(ctx, seriesID)
	if errors.Is(err, series.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.series.HasWantedEpisodes(ctx, sr.ID) {
		return nil
	}
	return s.notifyRequester(ctx, "series", sr.TMDBID, "")
}

// NotifyBookReady tells everyone behind the requests for a book that it has arrived.
func (s *Service) NotifyBookReady(ctx context.Context, bookID int64) error {
	b, err := s.books.Get(ctx, bookID)
	if errors.Is(err, books.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.notifyBookRequesters(ctx, b.ID, b.OLKey)
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
func (s *Service) notifyRequester(ctx context.Context, mediaType string, tmdbID int, olKey string) error {
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
		return nil
	}
	return s.notifyReady(ctx, req)
}

// notifyBookRequesters alerts everyone behind the requests linked to a just-imported
// book. The link survives the book's catalogue key changing; a request not linked yet
// is still found by the key it was made under.
func (s *Service) notifyBookRequesters(ctx context.Context, bookID int64, olKey string) error {
	linked, err := s.repo.ListByBookID(ctx, bookID)
	if err != nil {
		// Without the list the linked requesters would be skipped for good; fail so a
		// retrying caller (an outbox row) tries again.
		return fmt.Errorf("list requests for book %d: %w", bookID, err)
	}
	if len(linked) == 0 {
		return s.notifyRequester(ctx, "book", 0, olKey)
	}
	var errs []error
	for _, req := range linked {
		errs = append(errs, s.notifyReady(ctx, req))
	}
	return errors.Join(errs...)
}

// notifyReady sends the "your request is ready" notification for one request.
// Idempotent per user (unique inbox ref), so callers may fire it repeatedly.
func (s *Service) notifyReady(ctx context.Context, req Request) error {
	body := fmt.Sprintf("“%s” is ready to watch.", req.Title)
	if req.MediaType == "book" {
		body = fmt.Sprintf("“%s” is ready to read.", req.Title)
	}
	told, err := s.notifyPartiesCount(ctx, req, "Your request is ready", body, requestRef(req), "request-ready")
	// Only when someone heard it for the first time: the ready sweep calls this on a timer,
	// and an open page needs telling once.
	if told > 0 {
		s.publishUpdated(req, eventAvailable, s.parties(ctx, req))
	}
	return err
}

// notifyDecision tells the requester and subscribers a request was approved or declined.
func (s *Service) notifyDecision(ctx context.Context, req Request, approved bool) {
	if approved {
		body := fmt.Sprintf("Your request for “%s” was approved — we're on it.", req.Title)
		_ = s.notifyParties(ctx, req, "Request approved", body, requestRef(req)+":approved", "request-approved")
		return
	}
	body := fmt.Sprintf("Your request for “%s” was declined.", req.Title)
	_ = s.notifyParties(ctx, req, "Request declined", body, requestRef(req)+":declined", "request-declined")
}

// notifyParties fans one notification out to the requester and every subscriber:
// in-app inbox always, personal Apprise push when set. The unique (user_id, ref)
// inbox index de-dupes; the Apprise push only fires when the inbox row was new.
//
// It returns an error when someone may have been missed (the subscriber list or an inbox
// row couldn't be read or written), so a retrying caller runs it again; everyone already
// told is skipped by the inbox index. A failed Apprise push isn't one: the inbox row is
// the notification, and a retry would never re-push it anyway.
func (s *Service) notifyParties(ctx context.Context, req Request, title, body, ref, kind string) error {
	_, err := s.notifyPartiesCount(ctx, req, title, body, ref, kind)
	return err
}

// notifyPartiesCount is notifyParties that also says how many people were told for the
// first time.
func (s *Service) notifyPartiesCount(ctx context.Context, req Request, title, body, ref, kind string) (int, error) {
	told := 0
	seen := map[int64]bool{}
	var userIDs []int64
	var errs []error
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
		errs = append(errs, fmt.Errorf("list subscribers of request %d: %w", req.ID, err))
	}
	now := time.Now().Unix()
	for _, uid := range userIDs {
		inserted, err := s.repo.addUserNotification(ctx, uid, title, body, req.MediaType, ref, now)
		if err != nil {
			s.log.Warn(kind+": could not add inbox notification", "user", uid, "err", err)
			errs = append(errs, fmt.Errorf("inbox notification for request %d: %w", req.ID, err))
			continue
		}
		if !inserted {
			continue // already notified — don't re-push
		}
		told++
		if url, err := s.repo.getUserApprise(ctx, uid); err == nil && url != "" {
			// Checked again at send time: DNS can change, and a URL saved before the
			// check existed gets it too. A refusal skips only this push — the inbox row
			// above (and Web Push below) still tell them.
			if verr := s.checkUserApprise(ctx, uid, url); verr != nil {
				s.log.Warn(kind+": personal Apprise push skipped — the saved link isn't allowed", "user", uid, "reason", verr.Error())
			} else if err := s.sendUserApprise(ctx, url, body); err != nil {
				s.log.Warn(kind+": apprise push failed", "user", uid, "err", err)
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
	return told, errors.Join(errs...)
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
	bookByID := map[int64]bool{}
	if bs, err := s.books.List(ctx); err == nil {
		for _, b := range bs {
			bookHave[b.OLKey] = b.HasFile
			bookByID[b.ID] = b.HasFile
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
			// By the linked row first: the catalogue key may have changed since.
			if has, ok := bookByID[reqs[i].BookID]; ok {
				ready = has
			} else {
				ready = bookHave[reqs[i].OLKey]
			}
		}
		if ready {
			_ = s.notifyReady(ctx, reqs[i]) // logged inside; the next sweep tries again
		}
	}
	return nil
}
