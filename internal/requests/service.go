package requests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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
	push       PushSender // optional: Web Push fan-out alongside inbox + Apprise
	runner     Runner     // where approval searches run without a job runner; nil = untracked, panic-safe goroutines
	jobs       jobs.Submitter
	log        *slog.Logger
	// seriesMu serialises series request creation (and season trims): working out which
	// seasons are already covered and inserting the rest must happen as one step.
	seriesMu sync.Mutex
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
		// A key the library knows — the book's current key or any it had before — links
		// the request to that row from the start, and finds a request made from a card
		// carrying another of its keys.
		if in.BookID == 0 && s.books != nil {
			if id, ok := s.books.BookIDForKey(ctx, in.OLKey); ok {
				in.BookID = id
			}
		}
		// Read, listen or both — and the profile that goes with the choice.
		if err := s.normalizeBookFormats(ctx, &in); err != nil {
			return Request{}, false, err
		}
	default:
		return Request{}, false, fmt.Errorf("media_type must be movie, series or book")
	}
	if in.QualityProfile != "" && s.quality != nil && !s.quality.Known(ctx, in.QualityProfile) {
		return Request{}, false, ErrUnknownProfile
	}
	if in.MediaType == "series" {
		// A show can have several requests, one per ask for more seasons.
		return s.createSeries(ctx, in, autoApprove)
	}
	in.Seasons = nil
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
		profile := in.QualityProfile
		if in.MediaType == "book" {
			// The request already carries the profile for its format choice; passing it
			// as an approver's pick would re-derive the choice from the profile.
			profile = ""
		}
		created, err = s.Approve(ctx, created.ID, profile) // publishes approved
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

// lookupExisting finds a prior request for the same movie or book, if any (series go
// through createSeries).
func (s *Service) lookupExisting(ctx context.Context, in Request) (Request, bool) {
	if in.MediaType == "book" {
		return s.lookupExistingBook(ctx, in)
	}
	return s.repo.GetByMedia(ctx, in.MediaType, in.TMDBID)
}

// lookupExistingBook finds a prior request for the same book. The unique index only
// catches the identical key, and one book has several: the request made under this key;
// else one for the library row this key belongs to (linked to it, or made under any of
// its keys); else, for a book not in the library, a request for the same title and
// author made from another catalogue's card.
func (s *Service) lookupExistingBook(ctx context.Context, in Request) (Request, bool) {
	if req, ok := s.repo.GetByBook(ctx, in.OLKey); ok {
		return req, true
	}
	if in.BookID > 0 && s.books != nil {
		var keys []string
		if ks, err := s.books.KeysFor(ctx, in.BookID); err == nil {
			for _, k := range ks {
				keys = append(keys, k.Key)
			}
		}
		if list, err := s.repo.ListForBook(ctx, in.BookID, keys); err == nil && len(list) > 0 {
			return list[0], true
		}
		return Request{}, false
	}
	if in.Title == "" {
		return Request{}, false
	}
	all, err := s.repo.bookRequests(ctx)
	if err != nil {
		return Request{}, false
	}
	for _, rq := range all {
		if books.SameBook(rq.Title, rq.Author, in.Title, in.Author) {
			return rq, true
		}
	}
	return Request{}, false
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
		// A re-opened book request asks for what its new requester chose.
		if existing.MediaType == "book" && in.Formats != "" {
			if err := s.repo.SetFormats(ctx, existing.ID, in.Formats, in.QualityProfile); err != nil {
				s.log.Warn("request: could not set the formats", "id", existing.ID, "err", err)
			}
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
	if existing.MediaType == "book" && in.Formats != "" {
		if req, ok := s.widenRequest(ctx, existing, in); ok {
			existing = req
		}
	}
	return existing, true, nil
}

// widenRequest folds a second book request's format choice into the existing one:
// someone asking to listen to a book another asked to read makes the request "both".
// An approved request widens its library book too and searches for what is missing.
// It never narrows anything. ok reports the request changed.
func (s *Service) widenRequest(ctx context.Context, existing, in Request) (Request, bool) {
	had := s.requestedFormats(ctx, existing)
	union := unionFormats(had, in.Formats)
	if union == had {
		return existing, false // nothing new asked for; an older request keeps its old ways
	}
	if err := s.repo.SetFormats(ctx, existing.ID, union, s.profileForFormats(ctx, union)); err != nil {
		s.log.Warn("request: could not widen the formats", "id", existing.ID, "err", err)
		return existing, false
	}
	if existing.Status == StatusApproved && s.books != nil {
		bookID := existing.BookID
		if bookID == 0 {
			bookID = in.BookID
		}
		if bookID == 0 {
			bookID, _ = s.books.BookIDForKey(ctx, existing.OLKey)
		}
		if bookID > 0 {
			if existing.BookID == 0 {
				_ = s.repo.SetBookID(ctx, existing.ID, bookID)
			}
			if s.widenBook(ctx, bookID, union) {
				s.searchRequestedBook(ctx, existing.ID, bookID, existing.Title)
			}
		}
	}
	req, err := s.repo.Get(ctx, existing.ID)
	if err != nil {
		return existing, false
	}
	return req, true
}

// searchRequestedBook starts a search for a requested book that lacks an edition the
// request asked for — unless something is already downloading for it, which a second
// grab would only duplicate.
func (s *Service) searchRequestedBook(ctx context.Context, requestID, bookID int64, title string) {
	if s.searchBook == nil || len(s.activeGrabs(ctx, "book", bookID)) > 0 {
		return
	}
	s.background("book.search", fmt.Sprintf("book:%d", bookID), fmt.Sprintf("request:%d", requestID), title, "book", 0, 5*time.Minute, func(c context.Context) (automation.SearchOutcome, error) {
		return s.searchBook(c, bookID)
	})
}

// Approve adds the requested media to the Movies/Series module (monitored) and starts
// a search, then marks the request approved. If the media is already in the library,
// it's still marked approved (no duplicate add).
func (s *Service) Approve(ctx context.Context, id int64, profile string) (Request, error) {
	return s.ApproveWith(ctx, id, ApproveOptions{Profile: profile})
}

// ApproveOptions are the approver's choices.
type ApproveOptions struct {
	// Profile overrides the request's quality profile ("" keeps it).
	Profile string
	// Seasons trims a series request to these seasons, a subset of what it asked for
	// (any regular seasons for a whole-show request). Empty approves it as asked.
	Seasons []int
}

// ApproveWith is Approve with the approver's choices. A trimmed series request is
// rewritten to the seasons approved, only those are monitored, and its notice says
// which weren't.
func (s *Service) ApproveWith(ctx context.Context, id int64, opts ApproveOptions) (Request, error) {
	req, err := s.repo.Get(ctx, id)
	if err != nil {
		return Request{}, err
	}
	profile := opts.Profile
	if profile != "" && s.quality != nil && !s.quality.Known(ctx, profile) {
		return Request{}, ErrUnknownProfile
	}
	explicit := profile != ""
	trimmed := false
	if req.MediaType == "series" && len(opts.Seasons) > 0 {
		keep, dropped, err := trimSeasons(req.Seasons, opts.Seasons)
		if err != nil {
			return Request{}, err
		}
		if !sameSeasons(keep, req.Seasons) {
			req.Seasons, req.notApproved, trimmed = keep, dropped, true
		}
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
		if errors.Is(addErr, movies.ErrExists) {
			// Already in the library. Without a file it may never have been wanted (a
			// scan adds what it finds unmonitored), so approving it is what monitors it;
			// one that has its file is simply approved and the ready path stamps it.
			existing, err := s.movies.GetByTMDB(ctx, req.TMDBID)
			if err != nil {
				return Request{}, fmt.Errorf("find the movie in the library: %w", err)
			}
			m, addErr = existing, nil
			if m.HasFile {
				m.ID = 0
			} else if !m.Monitored {
				if err := s.movies.SetMonitored(ctx, m.ID, true); err != nil {
					return Request{}, err
				}
				s.movies.AddEvent(ctx, m.ID, "monitored", "Monitored by request from "+requesterName(req))
			}
		}
		if addErr == nil && m.ID > 0 {
			mid := m.ID
			s.background("movie.search", fmt.Sprintf("movie:%d", mid), trigger, req.Title, "movie", mid, 3*time.Minute, func(c context.Context) (automation.SearchOutcome, error) {
				return s.coord.SearchMovie(c, mid)
			})
		}
	case "series":
		// A whole-show request starts from the configured monitoring preset (Settings →
		// Library), as an owner's own add does. One for some seasons monitors exactly
		// those, with "monitor new seasons" off so later seasons aren't grabbed unasked.
		addOpts := series.AddOptions{Monitored: true}
		if len(req.Seasons) > 0 {
			only, off := map[int]bool{}, false
			for _, n := range req.Seasons {
				only[n] = true
			}
			addOpts.Seasons, addOpts.MonitorNewSeasons = only, &off
		}
		sr, addErr := s.series.AddWith(ctx, req.TMDBID, profile, addOpts)
		if addErr != nil && !errors.Is(addErr, series.ErrExists) {
			return Request{}, addErr
		}
		if errors.Is(addErr, series.ErrExists) {
			// Already in the library: make it fetch what was asked for, unless it's all
			// on disk already.
			existing, wants, err := s.monitorExistingSeries(ctx, req)
			if err != nil {
				return Request{}, err
			}
			sr, addErr = existing, nil
			if !wants {
				sr.ID = 0
			}
		}
		if addErr == nil && sr.ID > 0 {
			sid := sr.ID
			s.background("series.search", fmt.Sprintf("series:%d", sid), trigger, req.Title, "show", 0, 5*time.Minute, func(c context.Context) (automation.SearchOutcome, error) {
				return s.coord.SearchSeriesNow(c, sid)
			})
		}
	case "book":
		// An approver's explicit profile is their call on the format too.
		if explicit && s.quality != nil {
			if f := s.formatsForProfile(ctx, profile); f != req.Formats {
				if err := s.repo.SetFormats(ctx, id, f, ""); err != nil {
					s.log.Warn("request: could not set the formats", "request", id, "err", err)
				}
				req.Formats = f
			}
		}
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
		switch {
		case b.ID > 0 && addErr == nil:
			// A new row always wants a search.
			if s.searchBook != nil {
				bid := b.ID
				s.background("book.search", fmt.Sprintf("book:%d", bid), trigger, req.Title, "book", 0, 5*time.Minute, func(c context.Context) (automation.SearchOutcome, error) {
					return s.searchBook(c, bid)
				})
			}
		case b.ID > 0:
			// Already in the library: it now wants every edition the request asked for
			// as well as its own (an audiobook request for a book we have as an ebook),
			// and is searched when one of those is missing.
			if s.widenBook(ctx, b.ID, s.requestedFormatsFor(ctx, req, profile)) {
				if !b.Monitored {
					// A book the library holds unmonitored is never looked for again by the
					// sweep; approving a request for it is what wants it.
					if err := s.books.SetMonitored(ctx, b.ID, true); err != nil {
						return Request{}, err
					}
					s.books.AddEvent(ctx, b.ID, "monitored", "Monitored by request from "+requesterName(req))
				}
				s.searchRequestedBook(ctx, id, b.ID, req.Title)
			}
		}
	}

	if trimmed {
		// Under the create lock, so a request being planned against this row sees either
		// the seasons it had or the ones it has now, never half of each.
		s.seriesMu.Lock()
		err := s.repo.SetSeasons(ctx, id, req.Seasons)
		s.seriesMu.Unlock()
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Request{}, err
		}
		s.log.Info("request trimmed on approve", "title", req.Title, "seasons", series.SeasonsLabel(req.Seasons), "not_approved", req.notApproved)
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
	if s.books == nil {
		return 0, 0, nil
	}
	s.logKeyConflicts(ctx)
	reqs, err := s.repo.unlinkedBookRequests(ctx)
	if err != nil || len(reqs) == 0 {
		return 0, 0, err
	}
	list, err := s.books.List(ctx)
	if err != nil {
		return 0, 0, err
	}
	byKey := s.books.KeyIndex(ctx, list) // every key a book has had
	same := books.NewIdentityIndex(list)
	for _, rq := range reqs {
		id := byKey[rq.OLKey].ID
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
		// The key the request was made under is one of the book's keys from now on.
		// One another book already holds stays with that book (ErrExists); the
		// duplicates review sorts those out.
		_ = s.books.AddKey(ctx, rq.OLKey, id, books.KeySourceRequest)
		linked++
	}
	if linked > 0 || ambiguous > 0 {
		s.log.Info(fmt.Sprintf("requests: backfilled %d book requests (%d ambiguous)", linked, ambiguous))
	}
	return linked, ambiguous, nil
}

// logKeyConflicts counts the book requests linked to one library row but made under a
// key another row holds: two rows that may be one book, for the duplicates review. A
// count only — no titles, no requesters.
func (s *Service) logKeyConflicts(ctx context.Context) {
	all, err := s.repo.bookRequests(ctx)
	if err != nil {
		return
	}
	keys, err := s.books.AllKeys(ctx)
	if err != nil {
		return
	}
	n := 0
	for _, rq := range all {
		if owner, ok := keys[rq.OLKey]; ok && rq.BookID > 0 && owner != rq.BookID {
			n++
		}
	}
	if n > 0 {
		s.log.Info(fmt.Sprintf("requests: %d book requests are linked to one book but were made under another book's key", n))
	}
}

// requestedFormatsFor is what a book request being approved on profile asks for: its
// own choice, or for a request from before the choice existed, that profile's editions.
func (s *Service) requestedFormatsFor(ctx context.Context, req Request, profile string) string {
	if req.Formats != "" {
		return req.Formats
	}
	return s.formatsForProfile(ctx, profile)
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
	// Whether it is ready depends on the formats the request asked for, so keep the book.
	bookByKey := map[string]books.Book{}
	bookByID := map[int64]books.Book{}
	if bs, err := s.books.List(ctx); err == nil {
		for _, b := range bs {
			bookByID[b.ID] = b
		}
		bookByKey = s.books.KeyIndex(ctx, bs) // any key a book has had, not only its current one
	}
	progBySeries := map[int64]map[int]series.SeasonProgress{}
	for i := range reqs {
		var l lib
		switch reqs[i].MediaType {
		case "movie":
			l = movHave[reqs[i].TMDBID]
		case "series":
			l = serHave[reqs[i].TMDBID]
		case "book":
			b, ok := bookByID[reqs[i].BookID]
			if !ok {
				b, ok = bookByKey[reqs[i].OLKey]
			}
			if ok {
				l = lib{id: b.ID, have: bookReady(reqs[i].Formats, b), released: true, misses: b.SearchMisses}
				// Only a monitored book has a next check: the sweep never looks at the others.
				if next := books.NextSearchAt(b.LastSearchAt, b.SearchMisses); b.Monitored && !next.IsZero() {
					l.nextCheck = next.UTC().Format(time.RFC3339)
				}
				reqs[i].partNote = partialNote(reqs[i].Formats, b)
			}
		}
		reqs[i].Available = l.have
		reqs[i].libID, reqs[i].epHave, reqs[i].epTotal, reqs[i].released = l.id, l.epHave, l.epTotal, l.released
		reqs[i].searchMisses, reqs[i].nextCheckAt = l.misses, l.nextCheck
		if reqs[i].MediaType == "series" && len(reqs[i].Seasons) > 0 && l.id > 0 {
			s.enrichSeasons(ctx, &reqs[i], l.id, progBySeries)
		}
	}
}

// enrichSeasons fills a season-scoped series request's numbers from its own seasons:
// available once any of them has a file, have/total over their monitored episodes, and
// complete by seasonsProgress (the rule its ready notice uses). progBySeries caches each
// show's counts for the rest of the list.
func (s *Service) enrichSeasons(ctx context.Context, rq *Request, seriesID int64, progBySeries map[int64]map[int]series.SeasonProgress) {
	prog, ok := progBySeries[seriesID]
	if !ok {
		var err error
		if prog, err = s.series.SeasonProgress(ctx, seriesID); err != nil {
			s.log.Warn("requests: couldn't read season progress", "series", seriesID, "err", err)
		}
		progBySeries[seriesID] = prog
	}
	files := 0
	for _, n := range rq.Seasons {
		files += prog[n].Have
	}
	rq.Available = files > 0
	rq.epHave, rq.epTotal, rq.seasonsDone = seasonsProgress(prog, rq.Seasons)
}
