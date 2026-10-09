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
func (s *Service) GetApprise(ctx context.Context, userID int64) (string, error) {
	return s.repo.getUserApprise(ctx, userID)
}
func (s *Service) SetApprise(ctx context.Context, userID int64, url string) error {
	return s.repo.setUserApprise(ctx, userID, url)
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

// NotifySeriesReady tells whoever asked for a show that what they asked for is ready. A
// whole-show request waits until no monitored, aired episode is still wanted: a series
// isn't "ready to watch" on its first imported episode. A request for some seasons is
// ready when its own seasons are complete, whatever gaps older seasons have, and one for
// several seasons also hears as each of them completes. Later imports run this again (and
// the ready sweep backstops), so skipping just defers the message.
func (s *Service) NotifySeriesReady(ctx context.Context, seriesID int64) error {
	sr, err := s.series.Get(ctx, seriesID)
	if errors.Is(err, series.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	reqs, err := s.repo.ListByMedia(ctx, "series", sr.TMDBID)
	if err != nil {
		return fmt.Errorf("list requests for series %d: %w", sr.TMDBID, err)
	}
	var (
		errs   []error
		wanted *bool
		prog   map[int]series.SeasonProgress
	)
	for _, req := range reqs {
		if len(req.Seasons) == 0 {
			// Whole-show requests keep the rule (and the reference) they always had, so
			// nobody is told twice.
			if wanted == nil {
				w := s.series.HasWantedEpisodes(ctx, sr.ID)
				wanted = &w
			}
			if !*wanted {
				errs = append(errs, s.notifyReady(ctx, req))
			}
			continue
		}
		if req.Status == StatusDeclined {
			continue
		}
		if prog == nil {
			if prog, err = s.series.SeasonProgress(ctx, sr.ID); err != nil {
				return fmt.Errorf("season progress for series %d: %w", sr.ID, err)
			}
		}
		errs = append(errs, s.notifySeasonsReady(ctx, req, prog))
	}
	return errors.Join(errs...)
}

// notifySeasonsReady sends a season-scoped request's notices: "ready" once all of its
// seasons are complete, and — for a request of two or more seasons — "Season N is ready"
// as each one completes before that. When the whole request completes in one go, only
// the final notice goes out. Each is idempotent (its own inbox reference).
func (s *Service) notifySeasonsReady(ctx context.Context, req Request, prog map[int]series.SeasonProgress) error {
	if _, _, ready := seasonsProgress(prog, req.Seasons); ready {
		return s.notifyReady(ctx, req)
	}
	if len(req.Seasons) < 2 {
		return nil
	}
	var errs []error
	for _, n := range req.Seasons {
		if p, ok := prog[n]; !ok || !seasonReady(p) {
			continue
		}
		body := fmt.Sprintf("Season %d of “%s” is ready to watch.", n, req.Title)
		told, err := s.notifyPartiesCount(ctx, req, "Season ready", body, fmt.Sprintf("%s:s%d", requestRef(req), n), "request-season-ready")
		if told > 0 {
			s.publishUpdated(req, req.Status, s.parties(ctx, req))
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
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
//
// A season-scoped series request is keyed by its own id ("series:456:r12"): a show can
// have several, and each must be told apart. Whole-show requests keep "series:456", so
// nobody who already heard is told again.
func requestRef(req Request) string {
	if req.MediaType == "book" {
		return "book:" + req.OLKey
	}
	if req.MediaType == "series" && len(req.Seasons) > 0 {
		return fmt.Sprintf("series:%d:r%d", req.TMDBID, req.ID)
	}
	return fmt.Sprintf("%s:%d", req.MediaType, req.TMDBID)
}

// requestedWhat names what a request asked for, for its notices: "“Show”", or
// "Season 4 of “Show”" / "S1–2 of “Show”" for a season-scoped series request.
func requestedWhat(req Request) string {
	switch {
	case req.MediaType != "series" || len(req.Seasons) == 0:
		return fmt.Sprintf("“%s”", req.Title)
	case len(req.Seasons) == 1:
		return fmt.Sprintf("Season %d of “%s”", req.Seasons[0], req.Title)
	default:
		return fmt.Sprintf("%s of “%s”", series.SeasonsLabel(req.Seasons), req.Title)
	}
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
	body := fmt.Sprintf("%s is ready to watch.", requestedWhat(req))
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
		body := fmt.Sprintf("Your request for %s was approved — we're on it.", requestedWhat(req))
		if req.notApproved != "" {
			body += " " + req.notApproved
		}
		_ = s.notifyParties(ctx, req, "Request approved", body, requestRef(req)+":approved", "request-approved")
		return
	}
	body := fmt.Sprintf("Your request for %s was declined.", requestedWhat(req))
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
	progBySeries := map[int64]map[int]series.SeasonProgress{}
	for i := range reqs {
		ready := false
		switch reqs[i].MediaType {
		case "movie":
			ready = movHave[reqs[i].TMDBID]
		case "series":
			info, ok := serByTMDB[reqs[i].TMDBID]
			if !ok {
				break
			}
			if len(reqs[i].Seasons) > 0 {
				// Over its own seasons, with per-season notices (the import path's rule).
				prog, seen := progBySeries[info.id]
				if !seen {
					prog, err = s.series.SeasonProgress(ctx, info.id)
					if err != nil {
						s.log.Warn("ready sweep: couldn't read season progress", "series", info.id, "err", err)
						continue
					}
					progBySeries[info.id] = prog
				}
				_ = s.notifySeasonsReady(ctx, reqs[i], prog) // logged inside; the next sweep tries again
				continue
			}
			// Ready = some files on disk AND nothing still wanted (monitored, aired,
			// missing) — the same completeness rule the import-event path applies.
			if info.hasFiles {
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
