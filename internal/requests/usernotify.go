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

// NotifySeriesReady tells whoever asked for a show that what they asked for is ready —
// but only once it's complete (seriesComplete): a series isn't "ready to watch" on its
// first imported episode. A whole-show request asks it of the series' Stats roll-up; a
// request for some seasons asks it of each of its own seasons, whatever gaps older seasons
// have, and one for several seasons also hears as each of them completes. Later imports
// run this again (and the ready sweep backstops), so skipping just defers the message.
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
		errs []error
		prog map[int]series.SeasonProgress
	)
	for _, req := range reqs {
		if req.ReadyAt > 0 {
			continue // already told
		}
		if len(req.Seasons) == 0 {
			// Whole-show requests keep the reference they always had, so nobody is told
			// twice.
			if statsComplete(sr.Stats) {
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
// seasons are complete (which stamps the request's ready_at), and — for a request of two
// or more seasons — "Season N is ready" as each one completes before that. When the whole
// request completes in one go, only the final notice goes out. Each is idempotent (its own
// inbox reference).
func (s *Service) notifySeasonsReady(ctx context.Context, req Request, prog map[int]series.SeasonProgress) error {
	if req.ReadyAt > 0 {
		return nil
	}
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
		told, err := s.notifyPartiesCount(ctx, req, "Season ready", body, fmt.Sprintf("%s:s%d", requestRef(req), n), "request-season-ready", nil)
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
	return s.notifyBookRequesters(ctx, b)
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

// notifyBookRequesters alerts everyone behind the requests for a just-imported book. The
// link survives the book's catalogue key changing; a request not linked yet is still
// found by the key it was made under, current or former.
//
// The import's edition isn't trusted to say what arrived: two imports can share one
// outbox row. Each request is told about every format it asked for that is on disk now,
// and the per-format refs keep that to once each.
func (s *Service) notifyBookRequesters(ctx context.Context, b books.Book) error {
	keys := []string{b.OLKey}
	if ks, err := s.books.KeysFor(ctx, b.ID); err == nil {
		keys = keys[:0]
		for _, k := range ks {
			keys = append(keys, k.Key)
		}
	}
	linked, err := s.repo.ListForBook(ctx, b.ID, keys)
	if err != nil {
		// Without the list the linked requesters would be skipped for good; fail so a
		// retrying caller (an outbox row) tries again.
		return fmt.Errorf("list requests for book %d: %w", b.ID, err)
	}
	var errs []error
	for _, req := range linked {
		errs = append(errs, s.notifyBookReady(ctx, req, b, true))
	}
	return errors.Join(errs...)
}

// notifyBookReady sends a book request's "ready" messages: one per format it asked for
// that is on disk — "ready to read" for the ebook, "ready to listen to" for the
// audiobook. A request from before the format choice gets the one message it always
// did: on any import, or (from the sweep) once the book has a file.
//
// The ebook's message keeps the old single ref, so a request that predates the choice
// and is widened later isn't told about its ebook a second time. The request's ready_at
// is stamped once every format it asked for is here and everyone has been told.
func (s *Service) notifyBookReady(ctx context.Context, req Request, b books.Book, imported bool) error {
	if req.ReadyAt > 0 {
		return nil
	}
	if req.Formats == "" {
		if !imported && !b.HasFile {
			return nil
		}
		return s.notifyReady(ctx, req)
	}
	wantE, wantA := editionsOf(req.Formats)
	told := 0
	var errs []error
	if wantE && hasEbook(b) {
		n, err := s.notifyPartiesCount(ctx, req, "Your request is ready",
			fmt.Sprintf("“%s” is ready to read.", req.Title), requestRef(req), "request-ready", nil)
		told, errs = told+n, append(errs, err)
	}
	if wantA && hasAudiobook(b) {
		n, err := s.notifyPartiesCount(ctx, req, "Your request is ready",
			fmt.Sprintf("The audiobook of “%s” is ready to listen to.", req.Title), requestRef(req)+":"+books.KindAudiobook, "request-ready", nil)
		told, errs = told+n, append(errs, err)
	}
	err := errors.Join(errs...)
	complete := bookReady(req.Formats, b)
	if complete && err == nil {
		// Every format asked for is here and everyone was told about each.
		if merr := s.repo.MarkReady(ctx, req.ID, time.Now().Unix()); merr != nil {
			s.log.Warn("request-ready: could not record that the requester was told", "request", req.ID, "err", merr)
		}
	}
	if told > 0 {
		status := req.Status // half of a "both" request: changed, not yet ready
		if complete {
			status = eventAvailable
		}
		s.publishUpdated(req, status, s.parties(ctx, req))
	}
	return err
}

// notifyReady sends the "your request is ready" notification for one request, then
// stamps its ready_at. A request already stamped has been told and is left alone, so an
// upgrade of a title someone asked for long ago doesn't tell them again; within one
// telling it is idempotent per user (unique inbox ref), so a retry after a partial failure
// skips whoever already heard.
func (s *Service) notifyReady(ctx context.Context, req Request) error {
	if req.ReadyAt > 0 {
		return nil
	}
	body := fmt.Sprintf("%s is ready to watch.", requestedWhat(req))
	if req.MediaType == "book" {
		body = fmt.Sprintf("“%s” is ready to read.", req.Title)
	}
	told, err := s.notifyPartiesCount(ctx, req, "Your request is ready", body, requestRef(req), "request-ready", nil)
	if err == nil {
		// Only once everyone has been told: a failure leaves it unstamped, and the retry
		// (an outbox row, the ready sweep) finishes the job.
		if merr := s.repo.MarkReady(ctx, req.ID, time.Now().Unix()); merr != nil {
			s.log.Warn("request-ready: could not record that the requester was told", "request", req.ID, "err", merr)
		}
	}
	// Only when someone heard it for the first time: the ready sweep calls this on a timer,
	// and an open page needs telling once.
	if told > 0 {
		s.publishUpdated(req, eventAvailable, s.parties(ctx, req))
	}
	return err
}

// notifyDecision tells the requester and subscribers a request was approved or declined,
// except the users in skip.
func (s *Service) notifyDecision(ctx context.Context, req Request, approved bool, skip map[int64]bool) {
	if approved {
		body := fmt.Sprintf("Your request for %s was approved — we're on it.", requestedWhat(req))
		if req.notApproved != "" {
			body += " " + req.notApproved
		}
		_, _ = s.notifyPartiesCount(ctx, req, "Request approved", body, requestRef(req)+":approved", "request-approved", skip)
		return
	}
	body := fmt.Sprintf("Your request for %s was declined.", requestedWhat(req))
	_, _ = s.notifyPartiesCount(ctx, req, "Request declined", body, requestRef(req)+":declined", "request-declined", skip)
}

// notifyPartiesCount fans one notification out to the requester and every subscriber,
// less the users in skip: in-app inbox always, personal Apprise push when set. The unique
// (user_id, ref) inbox index de-dupes; the Apprise push only fires when the inbox row was
// new. It says how many people were told for the first time.
//
// It returns an error when someone may have been missed (the subscriber list or an inbox
// row couldn't be read or written), so a retrying caller runs it again; everyone already
// told is skipped by the inbox index. A failed Apprise push isn't one: the inbox row is
// the notification, and a retry would never re-push it anyway.
func (s *Service) notifyPartiesCount(ctx context.Context, req Request, title, body, ref, kind string, skip map[int64]bool) (int, error) {
	told := 0
	seen := map[int64]bool{}
	for uid, ok := range skip {
		seen[uid] = ok
	}
	var userIDs []int64
	var errs []error
	if req.RequestedBy > 0 && !seen[req.RequestedBy] {
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
		// The request id, not the title: an audiobook's "ready" pairs a user with what they
		// will listen to, and the log is staff-readable.
		s.log.Info(kind+" notified", "request", req.ID, "user", uid)
	}
	return told, errors.Join(errs...)
}

// SweepReadyRequests is the notification backstop: every approved request not yet told
// it's ready whose media now is gets the ready notification. It catches availability that
// arrived without an import event (library scans) and events dropped under load. It reads
// only the requests still waiting (ready_at = 0) and only their media, so a long request
// history costs it nothing; the unique inbox ref keeps a retry from telling anyone twice.
// A season-scoped request also gets its per-season notices, and a book request one per
// format that has arrived, by the same paths an import takes.
func (s *Service) SweepReadyRequests(ctx context.Context) error {
	reqs, err := s.repo.ListAwaitingReady(ctx)
	if err != nil {
		return err
	}
	// In batches, so the lookups' IN lists stay well inside SQLite's limits.
	const batch = 500
	for lo := 0; lo < len(reqs); lo += batch {
		page := reqs[lo:min(lo+batch, len(reqs))]
		lk, err := s.lookupReady(ctx, page)
		if err != nil {
			return err
		}
		for _, rq := range page {
			// Each is logged inside; the next sweep tries again.
			switch {
			case rq.MediaType == "series" && len(rq.Seasons) > 0:
				if prog, ok := lk.seasons(ctx, s, rq); ok {
					_ = s.notifySeasonsReady(ctx, rq, prog)
				}
			case rq.MediaType == "book":
				if b, ok := lk.book(rq); ok {
					_ = s.notifyBookReady(ctx, rq, b, false)
				}
			case lk.ready(ctx, s, rq):
				_ = s.notifyReady(ctx, rq)
			}
		}
	}
	return nil
}

// MarkReadyIfAvailable stamps a request ready, telling nobody, when its media is already
// in the library. An import uses it so people whose old requests are long since on the
// shelf aren't told about them all over again.
func (s *Service) MarkReadyIfAvailable(ctx context.Context, id int64) error {
	req, err := s.repo.Get(ctx, id)
	if err != nil || req.ReadyAt > 0 {
		return err
	}
	lk, err := s.lookupReady(ctx, []Request{req})
	if err != nil || !lk.ready(ctx, s, req) {
		return err
	}
	return s.repo.MarkReady(ctx, req.ID, time.Now().Unix())
}

// readyLookup is the library state a batch of requests is judged ready against, read with
// a query per media type for just those requests' media.
type readyLookup struct {
	movHave   map[int]bool
	series    map[int]series.Series // by TMDB id, each with its Stats roll-up
	prog      map[int64]map[int]series.SeasonProgress
	bookByID  map[int64]books.Book
	bookByKey map[string]books.Book // any key a book has had
}

// lookupReady reads the library state for reqs.
func (s *Service) lookupReady(ctx context.Context, reqs []Request) (*readyLookup, error) {
	lk := &readyLookup{
		movHave: map[int]bool{}, series: map[int]series.Series{}, prog: map[int64]map[int]series.SeasonProgress{},
		bookByID: map[int64]books.Book{}, bookByKey: map[string]books.Book{},
	}
	var movieIDs, seriesIDs []int
	var olKeys []string
	var bookIDs []int64
	for _, rq := range reqs {
		switch rq.MediaType {
		case "movie":
			movieIDs = append(movieIDs, rq.TMDBID)
		case "series":
			seriesIDs = append(seriesIDs, rq.TMDBID)
		case "book":
			olKeys = append(olKeys, rq.OLKey)
			if rq.BookID > 0 {
				bookIDs = append(bookIDs, rq.BookID)
			}
		}
	}
	if len(movieIDs) > 0 && s.movies != nil {
		ms, err := s.movies.ByTMDBIDs(ctx, movieIDs)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			lk.movHave[m.TMDBID] = m.HasFile
		}
	}
	if len(seriesIDs) > 0 && s.series != nil {
		ss, err := s.series.ByTMDBIDs(ctx, seriesIDs)
		if err != nil {
			return nil, err
		}
		for _, sr := range ss {
			lk.series[sr.TMDBID] = sr
		}
	}
	if len(olKeys) > 0 && s.books != nil {
		bs, err := s.books.ByKeysOrIDs(ctx, olKeys, bookIDs)
		if err != nil {
			return nil, err
		}
		for _, b := range bs {
			lk.bookByID[b.ID] = b
		}
		owners, err := s.books.KeyOwners(ctx, olKeys)
		if err != nil {
			return nil, err
		}
		for k, id := range owners {
			if b, ok := lk.bookByID[id]; ok {
				lk.bookByKey[k] = b
			}
		}
	}
	return lk, nil
}

// book is the library book a book request stands for: by the linked row first (the
// catalogue key may have changed since), else by the key it was made under, current or
// former.
func (lk *readyLookup) book(rq Request) (books.Book, bool) {
	if b, ok := lk.bookByID[rq.BookID]; ok {
		return b, true
	}
	b, ok := lk.bookByKey[rq.OLKey]
	return b, ok
}

// seasons is the per-season progress of the show a season-scoped request is for, read
// once per show. ok is false when the show isn't in the library or can't be read.
func (lk *readyLookup) seasons(ctx context.Context, s *Service, rq Request) (map[int]series.SeasonProgress, bool) {
	sr, ok := lk.series[rq.TMDBID]
	if !ok {
		return nil, false
	}
	if prog, seen := lk.prog[sr.ID]; seen {
		return prog, prog != nil
	}
	prog, err := s.series.SeasonProgress(ctx, sr.ID)
	if err != nil {
		s.log.Warn("ready sweep: couldn't read season progress", "series", sr.ID, "err", err)
		prog = nil
	}
	lk.prog[sr.ID] = prog
	return prog, prog != nil
}

// ready reports whether a request's media is complete in the library, by the one rule
// each kind has: a movie with its file; a whole show complete over its Stats and a
// season-scoped request over each of its own seasons (seriesComplete both ways); a book
// with every format the request asked for.
func (lk *readyLookup) ready(ctx context.Context, s *Service, rq Request) bool {
	switch rq.MediaType {
	case "movie":
		return lk.movHave[rq.TMDBID]
	case "series":
		if len(rq.Seasons) > 0 {
			prog, ok := lk.seasons(ctx, s, rq)
			if !ok {
				return false
			}
			_, _, done := seasonsProgress(prog, rq.Seasons)
			return done
		}
		sr, ok := lk.series[rq.TMDBID]
		return ok && statsComplete(sr.Stats)
	case "book":
		b, ok := lk.book(rq)
		return ok && bookReady(rq.Formats, b)
	}
	return false
}
