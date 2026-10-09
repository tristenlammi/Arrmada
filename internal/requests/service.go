package requests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/safego"
	"github.com/tristenlammi/arrmada/internal/series"
)

// Service is the Requests module's application logic. On approval it hands the media
// to the Movies/Series module (monitored, with a profile) and kicks off a search —
// reusing the whole acquisition pipeline rather than duplicating it.
type Service struct {
	repo       *Repo
	movies     *movies.Service
	series     *series.Service
	books      *books.Service
	coord      *automation.Coordinator
	quality    *quality.Service
	bus        *eventbus.Bus
	appriseBin string
	// userApprise guards and sends personal Apprise pushes (usernotify.go).
	userApprise userApprise
	push        PushSender // optional: Web Push fan-out alongside inbox + Apprise
	runner      Runner     // where approval searches run without a job runner; nil = untracked, panic-safe goroutines
	jobs        jobs.Submitter
	log         *slog.Logger
	// searchBook starts a book search (the coordinator's SearchBookNow); a field so
	// tests can see it called without a coordinator.
	searchBook func(ctx context.Context, bookID int64) (automation.SearchOutcome, error)
}

// Runner starts named background work with the app's run context (cancelled at
// shutdown). Satisfied by *safego.Group.
type Runner interface {
	Go(name string, fn func(ctx context.Context))
}

// SetRunner sets where the searches an approval starts run, so shutdown cancels them
// and a panic in one is contained (optional; without it they still can't crash the app).
// Used only when no job runner is set.
func (s *Service) SetRunner(r Runner) { s.runner = r }

// SetJobs makes an approval's search a job (movie.search, series.search or book.search,
// trigger request:<id>): recorded, single-flight with a Search click on the same title,
// and in the indexer-search class with every other search.
func (s *Service) SetJobs(sub jobs.Submitter) { s.jobs = sub }

// background runs fn after the approval has answered, bounded by timeout and logged on
// failure. Detached from the request: an approval returns as soon as the title is added.
// With a job runner it is a job of kind on target; finding the title already being
// searched is fine — that search covers it.
//
// movieID > 0 marks a movie search: it goes through the movie search queue
// (automation.EnqueueMovieSearch) like every other movie search, so the sweeps leave the
// movie alone while it waits.
func (s *Service) background(kind, target, trigger, title, noun string, movieID int64, timeout time.Duration, fn func(ctx context.Context) (automation.SearchOutcome, error)) {
	if s.jobs != nil {
		spec := jobs.Spec{
			Kind: kind, Target: target, Trigger: trigger, Class: jobs.ClassIndexerSearch, Timeout: timeout,
			Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
				out, err := fn(automation.WithSearchTrigger(ctx, automation.TriggerRequest))
				if errors.Is(err, automation.ErrAlreadySearching) {
					out.Reason, err = automation.ReasonAlreadySearching, nil
				}
				if err != nil {
					return out, err
				}
				p.SetMessage(out.Message(noun))
				return out, nil
			},
		}
		var err error
		if movieID > 0 && s.coord != nil {
			_, err = s.coord.EnqueueMovieSearch(context.Background(), s.jobs, movieID, spec)
		} else {
			_, _, err = s.jobs.Submit(context.Background(), spec)
		}
		if err != nil {
			s.log.Warn("request: couldn't start the search", "title", title, "err", err)
		}
		return
	}
	name := kind + " " + target
	run := func(parent context.Context) {
		c, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		if _, err := fn(automation.WithSearchTrigger(c, automation.TriggerRequest)); err != nil && !errors.Is(err, automation.ErrAlreadySearching) {
			s.log.Warn("request: "+name+" failed", "title", title, "err", err)
		}
	}
	if s.runner != nil {
		s.runner.Go("request: "+name, run)
		return
	}
	safego.Go(s.log, "request: "+name, func() { run(context.Background()) })
}

// PushSender delivers a Web Push notification to every device a user registered.
// Satisfied by *push.Service; an interface here keeps requests free of the
// dependency and lets tests stub it.
type PushSender interface {
	SendToUserAsync(userID int64, title, body, url string)
}

// SetPushSender wires the Web Push service (optional; nil-safe when unset).
func (s *Service) SetPushSender(p PushSender) { s.push = p }

// NewService wires the module. bus + appriseBin drive request-ready notifications (both optional).
func NewService(db *sql.DB, mv *movies.Service, sr *series.Service, bk *books.Service, coord *automation.Coordinator, q *quality.Service, bus *eventbus.Bus, appriseBin string, log *slog.Logger) *Service {
	s := &Service{repo: NewRepo(db), movies: mv, series: sr, books: bk, coord: coord, quality: q, bus: bus, appriseBin: appriseBin, log: log}
	if coord != nil {
		s.searchBook = coord.SearchBookNow
	}
	return s
}

// List returns requests (optionally filtered by status and/or requesting user), each
// enriched with whether the media is available in the library yet. requestedBy = 0
// returns everyone's requests (for managers); a user id scopes to that user.
func (s *Service) List(ctx context.Context, status string, requestedBy int64) ([]Request, error) {
	reqs, err := s.repo.List(ctx, status, requestedBy)
	if err != nil {
		return nil, err
	}
	s.enrichAvailability(ctx, reqs)
	return reqs, nil
}

// ErrUnknownProfile is returned when a supplied quality profile doesn't resolve.
var ErrUnknownProfile = errors.New("unknown quality profile")

// Get returns one request by id.
func (s *Service) Get(ctx context.Context, id int64) (Request, error) {
	return s.repo.Get(ctx, id)
}

// Create records a new request. When autoApprove is set it's approved (and added)
// immediately. When the same media has already been requested, the caller is
// attached to the existing request instead: a pending/approved request gains them
// as a subscriber (subscribed = true), and a declined request is re-opened under
// their name with the previous requester kept as a subscriber.
func (s *Service) Create(ctx context.Context, in Request, autoApprove bool) (created Request, subscribed bool, err error) {
	switch in.MediaType {
	case "movie", "series":
		if in.TMDBID == 0 {
			return Request{}, false, fmt.Errorf("tmdb_id is required")
		}
	case "book":
		if in.OLKey == "" {
			return Request{}, false, fmt.Errorf("ol_key is required")
		}
	default:
		return Request{}, false, fmt.Errorf("media_type must be movie, series or book")
	}
	if in.QualityProfile != "" && s.quality != nil && !s.quality.Known(ctx, in.QualityProfile) {
		return Request{}, false, ErrUnknownProfile
	}
	if existing, ok := s.lookupExisting(ctx, in); ok {
		return s.attachAndPublish(ctx, existing, in)
	}
	in.Status = StatusPending
	created, err = s.repo.Create(ctx, in)
	if errors.Is(err, ErrExists) {
		// Lost a create race: someone inserted the same media between our existence
		// check and the INSERT. Re-fetch and attach instead of failing.
		if existing, ok := s.lookupExisting(ctx, in); ok {
			return s.attachAndPublish(ctx, existing, in)
		}
		return Request{}, false, err
	}
	if err != nil {
		return Request{}, false, err
	}
	s.log.Info("request created", "media", in.MediaType, "title", in.Title, "by", in.RequestedByName, "auto_approve", autoApprove)
	if autoApprove {
		created, err = s.Approve(ctx, created.ID, in.QualityProfile) // publishes approved
		if err == nil {
			s.publishAutoApproved(created)
		}
		return created, false, err
	}
	s.publishUpdated(created, StatusPending, s.parties(ctx, created))
	return created, false, nil
}

// attachAndPublish is attachToExisting that tells open pages about it: the request is
// re-opened, or has a new subscriber.
func (s *Service) attachAndPublish(ctx context.Context, existing, in Request) (Request, bool, error) {
	req, subscribed, err := s.attachToExisting(ctx, existing, in)
	if err == nil {
		s.publishUpdated(req, req.Status, s.parties(ctx, req))
	}
	return req, subscribed, err
}

// lookupExisting finds a prior request for the same media, if any.
func (s *Service) lookupExisting(ctx context.Context, in Request) (Request, bool) {
	if in.MediaType == "book" {
		return s.repo.GetByBook(ctx, in.OLKey)
	}
	return s.repo.GetByMedia(ctx, in.MediaType, in.TMDBID)
}

// attachToExisting handles a request for media that's already requested:
//   - pending/approved: the caller becomes a subscriber (idempotent) and shares
//     future notifications; the existing request is returned with subscribed=true.
//   - declined: re-request — the row goes back to pending under the caller, and
//     the previous requester is kept as a subscriber so they still hear the outcome.
func (s *Service) attachToExisting(ctx context.Context, existing, in Request) (Request, bool, error) {
	if existing.Status == StatusDeclined {
		if err := s.repo.Resurrect(ctx, existing.ID, in.RequestedBy, in.RequestedByName, in.QualityProfile); err != nil {
			return Request{}, false, err
		}
		// Keep the previous requester in the loop as a subscriber.
		if existing.RequestedBy > 0 && existing.RequestedBy != in.RequestedBy {
			if err := s.repo.AddSubscriber(ctx, existing.ID, existing.RequestedBy, existing.RequestedByName); err != nil {
				s.log.Warn("request: could not keep previous requester subscribed", "id", existing.ID, "err", err)
			}
		}
		// The new requester may have been a subscriber before; don't list them twice.
		if err := s.repo.RemoveSubscriber(ctx, existing.ID, in.RequestedBy); err != nil {
			s.log.Warn("request: could not clean subscriber row", "id", existing.ID, "err", err)
		}
		s.log.Info("request re-opened", "media", existing.MediaType, "title", existing.Title, "by", in.RequestedByName)
		req, err := s.repo.Get(ctx, existing.ID)
		return req, false, err
	}
	// Pending or approved: subscribe the caller (skip when they already own it).
	if in.RequestedBy > 0 && in.RequestedBy != existing.RequestedBy {
		if err := s.repo.AddSubscriber(ctx, existing.ID, in.RequestedBy, in.RequestedByName); err != nil {
			return Request{}, false, err
		}
		s.log.Info("request subscribed", "media", existing.MediaType, "title", existing.Title, "by", in.RequestedByName)
	}
	return existing, true, nil
}

// Approve adds the requested media to the Movies/Series module (monitored) and starts
// a search, then marks the request approved. If the media is already in the library,
// it's still marked approved (no duplicate add).
func (s *Service) Approve(ctx context.Context, id int64, profile string) (Request, error) {
	req, err := s.repo.Get(ctx, id)
	if err != nil {
		return Request{}, err
	}
	if profile != "" && s.quality != nil && !s.quality.Known(ctx, profile) {
		return Request{}, ErrUnknownProfile
	}
	if profile == "" {
		profile = req.QualityProfile
		// The profile stored on the request may have been deleted since it was made. The
		// approver didn't pick it, so fall to the default rather than adding the title
		// on a ref that no longer exists (an explicit unknown pick is still refused above).
		if profile != "" && s.quality != nil && !s.quality.Known(ctx, profile) {
			profile = ""
		}
	}
	if profile == "" {
		profile = s.quality.DefaultProfile(ctx, req.MediaType)
	}

	trigger := fmt.Sprintf("request:%d", id)
	switch req.MediaType {
	case "movie":
		m, addErr := s.movies.Add(ctx, req.TMDBID, profile, true)
		if addErr != nil && !errors.Is(addErr, movies.ErrExists) {
			return Request{}, addErr
		}
		if addErr == nil {
			mid := m.ID
			s.background("movie.search", fmt.Sprintf("movie:%d", mid), trigger, req.Title, "movie", mid, 3*time.Minute, func(c context.Context) (automation.SearchOutcome, error) {
				return s.coord.SearchMovie(c, mid)
			})
		}
	case "series":
		// The configured monitoring preset (Settings → Library), as an owner's own add
		// starts from.
		sr, addErr := s.series.AddWith(ctx, req.TMDBID, profile, series.AddOptions{Monitored: true})
		if addErr != nil && !errors.Is(addErr, series.ErrExists) {
			return Request{}, addErr
		}
		if addErr == nil {
			sid := sr.ID
			s.background("series.search", fmt.Sprintf("series:%d", sid), trigger, req.Title, "show", 0, 5*time.Minute, func(c context.Context) (automation.SearchOutcome, error) {
				return s.coord.SearchSeriesNow(c, sid)
			})
		}
	case "book":
		b, addErr := s.books.Add(ctx, req.OLKey, profile, true, metadata.BookResult{
			Key: req.OLKey, Title: req.Title, Author: req.Author, Year: req.Year, CoverURL: req.PosterURL,
		})
		if addErr != nil && !errors.Is(addErr, books.ErrExists) {
			return Request{}, addErr
		}
		// Remember which row the request became. On ErrExists that row may sit under
		// another catalogue key than the request, and only this link finds it later.
		if b.ID > 0 {
			if err := s.repo.SetBookID(ctx, id, b.ID); err != nil {
				s.log.Warn("request: could not link the book", "request", id, "err", err)
			}
		}
		// A new row always wants a search; one already there only when it lacks an
		// edition its profile wants (an audiobook request for a book we have as an ebook)
		// and nothing is already downloading for it, which a second grab would duplicate.
		existingWants := addErr != nil && s.lacksWantedEdition(ctx, b) && len(s.activeGrabs(ctx, "book", b.ID)) == 0
		if b.ID > 0 && (addErr == nil || existingWants) && s.searchBook != nil {
			bid := b.ID
			s.background("book.search", fmt.Sprintf("book:%d", bid), trigger, req.Title, "book", 0, 5*time.Minute, func(c context.Context) (automation.SearchOutcome, error) {
				return s.searchBook(c, bid)
			})
		}
	}

	if err := s.repo.SetStatus(ctx, id, StatusApproved, profile); err != nil {
		if errors.Is(err, ErrNotFound) {
			// The request was withdrawn while we were approving it. The library add
			// above stands (the media is monitored either way); just report success.
			s.log.Warn("request deleted mid-approve; library add stands", "media", req.MediaType, "title", req.Title)
			req.Status = StatusApproved
			req.QualityProfile = profile
			return req, nil
		}
		return Request{}, err
	}
	s.log.Info("request approved", "media", req.MediaType, "title", req.Title, "profile", profile)
	s.notifyDecision(ctx, req, true)
	s.publishUpdated(req, StatusApproved, s.parties(ctx, req))
	return s.repo.Get(ctx, id)
}

// BackfillBookIDs links book requests made before requests remembered their library row.
// A request whose catalogue key still names a row links to it; otherwise one with the
// same title and author links only when exactly one row matches, so two copies of a
// book never get a request pinned to the wrong one. It only fills empty links and never
// deletes anything, so it is safe to run on every boot.
func (s *Service) BackfillBookIDs(ctx context.Context) (linked, ambiguous int, err error) {
	reqs, err := s.repo.unlinkedBookRequests(ctx)
	if err != nil || len(reqs) == 0 || s.books == nil {
		return 0, 0, err
	}
	list, err := s.books.List(ctx)
	if err != nil {
		return 0, 0, err
	}
	byKey := map[string]int64{}
	for _, b := range list {
		byKey[b.OLKey] = b.ID
	}
	same := books.NewIdentityIndex(list)
	for _, rq := range reqs {
		id := byKey[rq.OLKey]
		if id == 0 {
			switch matches := same.FindAll(rq.Title, rq.Author); len(matches) {
			case 0:
				continue // not in the library (yet): it still resolves by its key
			case 1:
				id = matches[0].ID
			default:
				ambiguous++
				continue
			}
		}
		if err := s.repo.SetBookID(ctx, rq.ID, id); err != nil {
			return linked, ambiguous, err
		}
		linked++
	}
	if linked > 0 || ambiguous > 0 {
		s.log.Info(fmt.Sprintf("requests: backfilled %d book requests (%d ambiguous)", linked, ambiguous))
	}
	return linked, ambiguous, nil
}

// lacksWantedEdition reports whether a library book is missing an edition its profile
// wants — the same rule the book page uses to show wanted-but-missing editions.
func (s *Service) lacksWantedEdition(ctx context.Context, b books.Book) bool {
	wantEbook, wantAudio := true, false
	if s.quality != nil {
		ref := s.quality.Effective(ctx, b.QualityProfile, quality.MediaBook)
		if sp, err := s.quality.GetStored(ctx, ref); err == nil {
			wantEbook, wantAudio = books.WantedEditions(sp.FormatScores)
		}
	}
	hasEbook := b.Ebook != nil && b.Ebook.Path != ""
	hasAudio := b.Audiobook != nil && b.Audiobook.Path != ""
	return (wantEbook && !hasEbook) || (wantAudio && !hasAudio)
}

// Decline rejects a request without adding anything. The stored quality profile
// is preserved so a later re-request keeps the original choice.
func (s *Service) Decline(ctx context.Context, id int64) error {
	req, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.SetStatus(ctx, id, StatusDeclined, ""); err != nil {
		return err
	}
	s.log.Info("request declined", "media", req.MediaType, "title", req.Title)
	s.notifyDecision(ctx, req, false)
	s.publishUpdated(req, StatusDeclined, s.parties(ctx, req))
	return nil
}

// Delete removes a request record (and its subscribers).
func (s *Service) Delete(ctx context.Context, id int64) error {
	// Who to tell is read first: the subscribers go with the row.
	req, getErr := s.repo.Get(ctx, id)
	var users []int64
	if getErr == nil {
		users = s.parties(ctx, req)
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	if getErr == nil {
		s.publishUpdated(req, eventDeleted, users)
	}
	return nil
}

// enrichAvailability marks each request available if its media is (partly) on disk.
func (s *Service) enrichAvailability(ctx context.Context, reqs []Request) {
	if len(reqs) == 0 {
		return
	}
	type lib struct {
		id              int64
		have            bool
		epHave, epTotal int
		released        bool
		misses          int    // books: searches in a row that found nothing
		nextCheck       string // books: when the ladder looks again (RFC3339)
	}
	movHave := map[int]lib{}
	if ms, err := s.movies.List(ctx); err == nil {
		for _, m := range ms {
			movHave[m.TMDBID] = lib{id: m.ID, have: m.HasFile, released: m.Status == "" || m.Status == "Released"}
		}
	}
	serHave := map[int]lib{}
	if ss, err := s.series.List(ctx); err == nil {
		for _, sr := range ss {
			l := lib{id: sr.ID, released: true}
			if sr.Stats != nil {
				l.have, l.epHave, l.epTotal = sr.Stats.HaveFiles > 0, sr.Stats.HaveFiles, sr.Stats.Episodes
			}
			serHave[sr.TMDBID] = l
		}
	}
	// Books by the row a request is linked to, falling back to the catalogue key for a
	// request not linked yet. The key alone loses the book once it is re-matched.
	bookHave := map[string]lib{}
	bookByID := map[int64]lib{}
	if bs, err := s.books.List(ctx); err == nil {
		for _, b := range bs {
			l := lib{id: b.ID, have: b.HasFile, released: true, misses: b.SearchMisses}
			// Only a monitored book has a next check: the sweep never looks at the others.
			if next := books.NextSearchAt(b.LastSearchAt, b.SearchMisses); b.Monitored && !next.IsZero() {
				l.nextCheck = next.UTC().Format(time.RFC3339)
			}
			bookHave[b.OLKey] = l
			bookByID[b.ID] = l
		}
	}
	for i := range reqs {
		var l lib
		switch reqs[i].MediaType {
		case "movie":
			l = movHave[reqs[i].TMDBID]
		case "series":
			l = serHave[reqs[i].TMDBID]
		case "book":
			l = bookHave[reqs[i].OLKey]
			if linked, ok := bookByID[reqs[i].BookID]; ok {
				l = linked
			}
		}
		reqs[i].Available = l.have
		reqs[i].libID, reqs[i].epHave, reqs[i].epTotal, reqs[i].released = l.id, l.epHave, l.epTotal, l.released
		reqs[i].searchMisses, reqs[i].nextCheckAt = l.misses, l.nextCheck
	}
}
