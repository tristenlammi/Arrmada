package requests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/push"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/safego"
	"github.com/tristenlammi/arrmada/internal/series"
)

// Service is the Requests module's application logic. On approval it hands the media
// to the Movies/Series module (monitored, with a profile) and kicks off a search —
// reusing the whole acquisition pipeline rather than duplicating it.
type Service struct {
	repo       *Repo
	movies     movieLib
	series     seriesLib
	books      bookLib
	coord      searcher
	quality    *quality.Service
	bus        *eventbus.Bus
	appriseBin string
	// userApprise guards and sends personal Apprise pushes (usernotify.go).
	userApprise userApprise
	push        PushSender // optional: Web Push fan-out alongside inbox + Apprise
	runner      Runner     // where approval searches run without a job runner; nil = untracked, panic-safe goroutines
	jobs        jobs.Submitter
	log         *slog.Logger
	// seriesMu serialises series request creation (and season trims): working out which
	// seasons are already covered and inserting the rest must happen as one step.
	seriesMu sync.Mutex
	// searchBook starts a book search (the coordinator's SearchBookNow); a field so
	// tests can see it called without a coordinator.
	searchBook func(ctx context.Context, bookID int64) (automation.SearchOutcome, error)
	// attentionKick asks the Needs-you feed to refresh after a request changes (pending.go).
	attentionKick atomic.Pointer[func()]
	// staffAlerts tells staff a new request is waiting (staffalert.go).
	staffAlerts StaffAlerts
	// now is the clock decisions are stamped with (time.Now when nil; tests move it).
	now func() time.Time
	// quotaLimits says a requester's limits (quota.go); quotaMu makes checking and
	// recording a limited requester's ask one step. Taken before seriesMu, never after.
	quotaLimits func(ctx context.Context, userID int64) (Limits, error)
	quotaMu     sync.Mutex
	// plex makes 'ready' wait for the owner's Plex to show the title (plexready.go); nil =
	// no Plex, ready on import. plexWaiter runs the check that announces them.
	plex       PlexLocator
	plexWaiter plexWaiter
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
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
	SendToUserAsync(userID int64, m push.Message)
}

// SetPushSender wires the Web Push service (optional; nil-safe when unset).
func (s *Service) SetPushSender(p PushSender) { s.push = p }

// NewService wires the module. bus + appriseBin drive request-ready notifications (both optional).
func NewService(db *sql.DB, mv *movies.Service, sr *series.Service, bk *books.Service, coord *automation.Coordinator, q *quality.Service, bus *eventbus.Bus, appriseBin string, log *slog.Logger) *Service {
	s := &Service{repo: NewRepo(db), quality: q, bus: bus, appriseBin: appriseBin, log: log}
	// Only non-nil pointers go into the interfaces: a nil *movies.Service in one would
	// pass every "is it wired?" check and then panic.
	if mv != nil {
		s.movies = mv
	}
	if sr != nil {
		s.series = sr
	}
	if bk != nil {
		s.books = bk
	}
	if coord != nil {
		s.coord = coord
		s.searchBook = coord.SearchBookNow
	}
	return s
}

// List returns one page of requests (see ListFilter) and how many match in all, each
// enriched with whether its media is in the library yet. Only the page's own media is
// looked up.
func (s *Service) List(ctx context.Context, f ListFilter) ([]Request, int, error) {
	reqs, total, err := s.repo.List(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	s.enrichAvailability(ctx, reqs)
	return reqs, total, nil
}

// Records is List without the library lookups: the bare request rows, for callers that
// only need who asked for what (Discover's badges, the Because rows' seeds).
func (s *Service) Records(ctx context.Context, f ListFilter) ([]Request, error) {
	reqs, _, err := s.repo.List(ctx, f)
	return reqs, err
}

// Counts is how many requests each section holds under f's scope and filters.
func (s *Service) Counts(ctx context.Context, f ListFilter) (Counts, error) {
	return s.repo.Counts(ctx, f)
}

// Strip limits for the Discover strip.
const (
	stripStaffEach = 20 // staff: this many waiting, then this many in progress
	stripMine      = 40 // a requester: their recent requests, at most this many
)

// Strip is the Discover strip's requests. Staff get what's waiting for a decision, oldest
// first, then what's in progress. A requester gets their own and followed requests that
// are waiting or on their way, ready in the last two weeks or declined in the last month,
// most recently changed first.
func (s *Service) Strip(ctx context.Context, viewer int64, staff bool) ([]Request, error) {
	if !staff {
		reqs, _, err := s.List(ctx, ListFilter{Section: sectionStripMine, UserID: viewer, IncludeJoined: true, Limit: stripMine})
		return reqs, err
	}
	waiting, _, err := s.repo.List(ctx, ListFilter{Section: SectionNeedsApproval, Limit: stripStaffEach})
	if err != nil {
		return nil, err
	}
	moving, _, err := s.repo.List(ctx, ListFilter{Section: SectionInProgress, Limit: stripStaffEach})
	if err != nil {
		return nil, err
	}
	reqs := append(waiting, moving...)
	s.enrichAvailability(ctx, reqs)
	return reqs, nil
}

// Detail is one request as viewer sees it: with enrichment, the viewer's relation to it
// and, for staff, who follows it. A requester who neither made nor follows it gets
// ErrNotFound, exactly as for a request that doesn't exist.
func (s *Service) Detail(ctx context.Context, id, viewer int64, staff bool) (Request, error) {
	rel, err := s.repo.RelationOf(ctx, id, viewer)
	if err != nil {
		return Request{}, err
	}
	if !staff && rel == "" {
		return Request{}, ErrNotFound
	}
	req, err := s.repo.Get(ctx, id)
	if err != nil {
		return Request{}, err
	}
	req.Relation = rel
	if staff {
		subs, err := s.repo.Subscribers(ctx, id)
		if err != nil {
			return Request{}, err
		}
		for _, sub := range subs {
			req.Followers = append(req.Followers, Follower{Name: sub.UserName})
		}
	}
	reqs := []Request{req}
	s.enrichAvailability(ctx, reqs)
	return reqs[0], nil
}

// MediaKeysForUser is the movies and shows user uid asked for or follows (not declined
// ones), keyed by MediaKey. The Calendar's 'My requests' filter.
func (s *Service) MediaKeysForUser(ctx context.Context, uid int64) (map[string]bool, error) {
	return s.repo.MediaKeysForUser(ctx, uid)
}

// Unsubscribe stops user uid following request id: no more notifications about it, and
// it leaves their list. ErrNotFound when they didn't follow it.
func (s *Service) Unsubscribe(ctx context.Context, id, uid int64) error {
	req, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	removed, err := s.repo.Unsubscribe(ctx, id, uid)
	if err != nil {
		return err
	}
	if !removed {
		return ErrNotFound
	}
	s.log.Info("request unfollowed", "media", req.MediaType, "title", req.Title, "user", uid)
	// Their open pages drop it; staff pages refresh the follower list.
	s.publishUpdated(req, req.Status, []int64{uid})
	return nil
}

// InLibrary is the library item (movie, series or book id) a listed request became, 0
// when it isn't in the library. Filled by List.
func (r Request) InLibrary() int64 { return r.libID }

// ErrUnknownProfile is returned when a supplied quality profile doesn't resolve.
var ErrUnknownProfile = errors.New("unknown quality profile")

// Get returns one request by id.
func (s *Service) Get(ctx context.Context, id int64) (Request, error) {
	return s.repo.Get(ctx, id)
}

// CreateOptions says how a new request is handled.
type CreateOptions struct {
	// AutoApprove approves (and adds) it straight away: the requester's own auto-approve.
	// They aren't told their own click was approved; anyone following it still is.
	AutoApprove bool
	// Silent tells nobody and publishes nothing: a bulk import of requests people made
	// long ago elsewhere, which mustn't fill their inboxes.
	Silent bool
	// DeferSearch adds an approved title to the library but leaves the search to the
	// scheduled sweeps, so an import of hundreds spreads its searches out instead of
	// queuing them all at once.
	DeferSearch bool
	// QuotaExempt skips the request limits (quota.go): staff. Silent imports are exempt
	// too.
	QuotaExempt bool
}

// Create records a new request. With AutoApprove it's approved (and added) immediately.
// When the same media has already been requested, the caller is attached to the existing
// request instead: a pending/approved request gains them as a subscriber (subscribed =
// true), and a declined request is re-opened under their name with the previous
// requester kept as a subscriber.
//
// Create is the one way a request comes into being — Discover, the Requests page and the
// Overseerr import all call it — and every new ask is announced from announceCreated.
func (s *Service) Create(ctx context.Context, in Request, opts CreateOptions) (created Request, subscribed bool, err error) {
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
		return s.createSeries(ctx, in, opts)
	}
	in.Seasons = nil
	q, err := s.quotaFor(ctx, in, opts)
	if err != nil {
		return Request{}, false, err
	}
	if q != nil {
		// Checking the limit and recording the use are one step for a limited requester.
		s.quotaMu.Lock()
	}
	created, subscribed, inserted, err := s.createOne(ctx, in, opts, q)
	if q != nil {
		s.quotaMu.Unlock()
	}
	if err != nil || !inserted {
		return created, subscribed, err
	}
	s.log.Info("request created", "media", in.MediaType, "title", in.Title, "by", in.RequestedByName, "auto_approve", opts.AutoApprove)
	if opts.AutoApprove {
		approved, err := s.autoApprove(ctx, created, opts)
		if err != nil {
			// The request stands, pending, for staff to approve by hand.
			s.announceCreated(ctx, created, opts)
			return Request{}, false, err
		}
		created = approved
	}
	s.announceCreated(ctx, created, opts)
	return created, false, nil
}

// createOne stores a movie or book request: attached to the request already there for it
// (followed, or a declined one re-opened, announced by attachCharged), or a new pending
// row (inserted, for the caller to auto-approve and announce). A limited requester's ask
// is checked against their quota first and charged once stored.
func (s *Service) createOne(ctx context.Context, in Request, opts CreateOptions, q *quotaCharge) (Request, bool, bool, error) {
	if existing, ok := s.lookupExisting(ctx, in); ok {
		req, subscribed, err := s.attachCharged(ctx, existing, in, opts, q)
		return req, subscribed, false, err
	}
	kind := quotaKind(in.MediaType)
	if err := s.checkQuota(ctx, q, kind, 1); err != nil {
		return Request{}, false, false, err
	}
	in.Status = StatusPending
	created, err := s.repo.Create(ctx, in)
	if errors.Is(err, ErrExists) {
		// Lost a create race: someone inserted the same media between our existence
		// check and the INSERT. Re-fetch and attach instead of failing.
		if existing, ok := s.lookupExisting(ctx, in); ok {
			req, subscribed, err := s.attachCharged(ctx, existing, in, opts, q)
			return req, subscribed, false, err
		}
		return Request{}, false, false, err
	}
	if err != nil {
		return Request{}, false, false, err
	}
	s.chargeQuota(ctx, q, created.ID, kind, 1)
	return created, false, true, nil
}

// attachCharged is attachAndPublish for a movie or book, charging what counts against
// the requester's quota: re-opening a declined request is a new ask; widening an approved
// book request to the other format starts a download nobody approved, so it counts as a
// book too; plain following is free.
func (s *Service) attachCharged(ctx context.Context, existing, in Request, opts CreateOptions, q *quotaCharge) (Request, bool, error) {
	units := 0
	switch {
	case existing.Status == StatusDeclined:
		if err := needsNote(in, opts, existing); err != nil {
			return Request{}, false, err // asked for its note before its quota
		}
		units = 1
	case q != nil && existing.MediaType == "book" && existing.Status == StatusApproved && in.Formats != "":
		if had := s.requestedFormats(ctx, existing); unionFormats(had, in.Formats) != had {
			units = 1
		}
	}
	kind := quotaKind(existing.MediaType)
	if err := s.checkQuota(ctx, q, kind, units); err != nil {
		return Request{}, false, err
	}
	req, subscribed, err := s.attachAndPublish(ctx, existing, in, opts)
	if existing.Status == StatusDeclined && subscribed {
		units = 0 // lost a race to re-open it: they follow the other ask instead
	}
	if err == nil {
		s.chargeQuota(ctx, q, req.ID, kind, units)
	}
	return req, subscribed, err
}

// announceCreated is where every new ask passes once it's stored: a fresh request
// (pending, or approved by the requester's own auto-approve) or a declined title asked for
// again. It tells open pages about a pending one and alerts staff that it's waiting (the
// one place the "New request" alert is raised); an approved one was announced by Approve
// and needs nobody's decision.
func (s *Service) announceCreated(ctx context.Context, req Request, opts CreateOptions) {
	if opts.Silent {
		// Nobody is told, but the staff Needs-you count still moves (an import's pending rows).
		s.kickAttention()
		return
	}
	if req.Status != StatusPending {
		return
	}
	s.publishUpdated(req, StatusPending, s.parties(ctx, req))
	s.alertStaff(ctx, req)
}

// attachAndPublish is attachToExisting that tells open pages about it: the request is
// re-opened (a new ask, so it's announced like one), or has a new subscriber.
func (s *Service) attachAndPublish(ctx context.Context, existing, in Request, opts CreateOptions) (Request, bool, error) {
	if existing.Status == StatusDeclined {
		if err := needsNote(in, opts, existing); err != nil {
			return Request{}, false, err
		}
	}
	req, subscribed, err := s.attachToExisting(ctx, existing, in)
	switch {
	case err != nil:
	case !subscribed:
		s.announceCreated(ctx, req, opts)
	case !opts.Silent:
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
//   - declined: re-request — the row goes back to pending under the caller with their
//     note, flagged as asked again, and the previous requester is kept as a subscriber
//     so they still hear the outcome. Callers check the note first (needsNote).
func (s *Service) attachToExisting(ctx context.Context, existing, in Request) (Request, bool, error) {
	if existing.Status == StatusDeclined {
		if err := s.repo.Resurrect(ctx, existing.ID, in.RequestedBy, in.RequestedByName, in.QualityProfile, in.Note); err != nil {
			if !errors.Is(err, errNotDeclined) {
				return Request{}, false, err
			}
			// Someone else re-opened it a moment ago: follow theirs instead of taking it over.
			fresh, gerr := s.repo.Get(ctx, existing.ID)
			if gerr != nil || fresh.Status == StatusDeclined {
				return Request{}, false, ErrNotFound
			}
			return s.attachToExisting(ctx, fresh, in)
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
	if s.searchBook == nil || s.bookInFlight(ctx, bookID) {
		return
	}
	s.background("book.search", fmt.Sprintf("book:%d", bookID), fmt.Sprintf("request:%d", requestID), title, "book", 0, 5*time.Minute, func(c context.Context) (automation.SearchOutcome, error) {
		return s.searchBook(c, bookID)
	})
}

// autoApprove approves a new request its requester's own auto-approve just made: they
// aren't told about their own click, staff get the auto-approved alert, and an import's
// Silent/DeferSearch carry through.
func (s *Service) autoApprove(ctx context.Context, created Request, opts CreateOptions) (Request, error) {
	profile := created.QualityProfile
	if created.MediaType == "book" {
		// The request already carries the profile for its format choice; passing it as an
		// approver's pick would re-derive the choice from the profile.
		profile = ""
	}
	approved, err := s.Approve(ctx, created.ID, ApproveOptions{
		Profile: profile, DecidedBy: created.RequestedBy, DecidedByName: created.RequestedByName,
		Auto: true, Silent: opts.Silent, DeferSearch: opts.DeferSearch,
	}) // announces it as approved
	if err == nil && !opts.Silent {
		// The staff "Auto-approved request" alert: once, for this new row (an import's
		// old requests don't raise it).
		s.publishAutoApproved(approved)
	}
	return approved, err
}

// ApproveOptions says how a request is approved.
type ApproveOptions struct {
	// Profile is the quality profile to add the title with; "" keeps the one the request
	// was made with, or the default.
	Profile string
	// Seasons trims a series request to these seasons, a subset of what it asked for (any
	// regular seasons for a whole-show request). Empty approves it as asked.
	Seasons []int
	// DecidedBy is who approved it (0: the system). They're never told about their own
	// decision.
	DecidedBy     int64
	DecidedByName string
	// Auto: the requester's own auto-approve did it, so the requester isn't told.
	Auto bool
	// Silent tells nobody and publishes nothing (an import).
	Silent bool
	// DeferSearch leaves the search to the scheduled sweeps.
	DeferSearch bool
}

// DeclineOptions says how a request is declined.
type DeclineOptions struct {
	// Reason is what the requester is told ("Already on Netflix"); "" says only that it
	// was declined. The HTTP layer bounds it (DeclineReasonMax).
	Reason string
	// DecidedBy is who declined it; they're never told about their own decision.
	DecidedBy     int64
	DecidedByName string
}

// DeclineReasonMax is the longest decline reason, in characters.
const DeclineReasonMax = 280

// NeedsNoteError refuses a declined title asked for again without a note: whoever asks
// again has to say why they'd still like it, and sees why it was declined.
type NeedsNoteError struct {
	DeclineReason string
}

func (e *NeedsNoteError) Error() string {
	return "this was declined before — add a note saying why you'd still like it"
}

// needsNote is the NeedsNoteError for asking again for what declined (a declined request)
// turned down, or nil when the ask may go ahead: it carries a note, or it's an import.
func needsNote(in Request, opts CreateOptions, declined Request) error {
	if opts.Silent || strings.TrimSpace(in.Note) != "" {
		return nil
	}
	return &NeedsNoteError{DeclineReason: declined.DeclineReason}
}

// Approve adds the requested media to the Movies/Series module (monitored) and starts
// a search, then marks the request approved. If the media is already in the library,
// it's still marked approved (no duplicate add).
//
// The search is a job in the runner's indexer-search class — movies through the movie
// search queue — so however many requests are approved at once (a bulk approve, an
// import), at most that class's limit of searches runs at a time and the rest wait their
// turn. There is no second queue here.
//
// A series request trimmed to some seasons (o.Seasons) is rewritten to the seasons
// approved, only those are monitored, and its notice says which weren't.
func (s *Service) Approve(ctx context.Context, id int64, o ApproveOptions) (Request, error) {
	req, err := s.repo.Get(ctx, id)
	if err != nil {
		return Request{}, err
	}
	profile := o.Profile
	if profile != "" && s.quality != nil && !s.quality.Known(ctx, profile) {
		return Request{}, ErrUnknownProfile
	}
	explicit := profile != ""
	trimmed := false
	if req.MediaType == "series" && len(o.Seasons) > 0 {
		keep, dropped, err := trimSeasons(req.Seasons, o.Seasons)
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
		if addErr == nil && m.ID > 0 && !o.DeferSearch {
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
		if addErr == nil && sr.ID > 0 && !o.DeferSearch {
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
			// A new row always wants a search (an import leaves it to the sweeps).
			if s.searchBook != nil && !o.DeferSearch {
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
				if !o.DeferSearch {
					s.searchRequestedBook(ctx, id, b.ID, req.Title)
				}
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
		// The seasons not approved are given back to the requester's limit.
		if err := s.repo.trimQuota(ctx, id, len(req.Seasons)); err != nil {
			s.log.Warn("request: couldn't give back trimmed seasons' quota", "request", id, "err", err)
		}
		s.log.Info("request trimmed on approve", "title", req.Title, "seasons", series.SeasonsLabel(req.Seasons), "not_approved", req.notApproved)
	}
	// Who approved it, for staff — nobody for the requester's own auto-approve. The time
	// also keys the decision notice, so a later approval after a re-request is told again.
	// Approving an approved request again keeps the first decision and tells nobody again.
	already := req.Status == StatusApproved
	decision := Decision{By: o.DecidedBy, ByName: o.DecidedByName, At: s.clock().Unix()}
	switch {
	case already:
		decision = Decision{By: req.DecidedBy, ByName: req.DecidedByName, At: req.DecidedAt}
	case o.Auto:
		decision.By, decision.ByName = 0, ""
	}
	req.DecidedBy, req.DecidedByName, req.DecidedAt, req.DeclineReason = decision.By, decision.ByName, decision.At, ""
	if err := s.repo.Decide(ctx, id, StatusApproved, profile, decision); err != nil {
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
	s.log.Info("request approved", "media", req.MediaType, "title", req.Title, "profile", profile, "auto", o.Auto)
	if !o.Silent {
		// Nobody hears about their own decision, and a requester whose own auto-approve did
		// it isn't told about their own click; followers still are.
		skip := map[int64]bool{o.DecidedBy: true}
		if o.Auto {
			skip[req.RequestedBy] = true
		}
		if !already {
			s.notifyDecision(ctx, req, true, skip)
		}
		s.publishUpdated(req, StatusApproved, s.parties(ctx, req))
	} else {
		s.kickAttention() // publishUpdated kicks otherwise
	}
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

// bookInFlight reports whether the acquisition record has a download in flight for a
// book. An unreadable record counts as in flight: a second grab on top of one already
// downloading is the mistake to avoid, and the sweeps search the book anyway.
func (s *Service) bookInFlight(ctx context.Context, bookID int64) bool {
	if s.coord == nil {
		return false
	}
	byItem, err := s.coord.ActiveByItem(ctx, "book")
	if err != nil {
		return true
	}
	return len(byItem[bookID]) > 0
}

// requestedFormatsFor is what a book request being approved on profile asks for: its
// own choice, or for a request from before the choice existed, that profile's editions.
func (s *Service) requestedFormatsFor(ctx context.Context, req Request, profile string) string {
	if req.Formats != "" {
		return req.Formats
	}
	return s.formatsForProfile(ctx, profile)
}

// Decline rejects a request without adding anything, recording who did it and the reason
// the requester is told. The stored quality profile is preserved so a later re-request
// keeps the original choice.
func (s *Service) Decline(ctx context.Context, id int64, o DeclineOptions) error {
	req, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	already := req.Status == StatusDeclined // declining it again tells nobody again
	d := Decision{By: o.DecidedBy, ByName: o.DecidedByName, At: s.clock().Unix(), Reason: strings.TrimSpace(o.Reason)}
	if err := s.repo.Decide(ctx, id, StatusDeclined, "", d); err != nil {
		return err
	}
	req.Status, req.DecidedBy, req.DecidedByName, req.DecidedAt, req.DeclineReason = StatusDeclined, d.By, d.ByName, d.At, d.Reason
	s.log.Info("request declined", "media", req.MediaType, "title", req.Title, "by", d.ByName)
	// A declined ask doesn't count against anyone's limit.
	if err := s.repo.refundQuota(ctx, id); err != nil {
		s.log.Warn("request: couldn't give back the request's quota", "request", id, "err", err)
	}
	if !already {
		s.notifyDecision(ctx, req, false, map[int64]bool{o.DecidedBy: true})
	}
	s.publishUpdated(req, StatusDeclined, s.parties(ctx, req))
	return nil
}

// Bulk actions.
const (
	BulkApprove = "approve"
	BulkDecline = "decline"
	// BulkMax is the most requests one bulk action takes.
	BulkMax = 100
)

// BulkResult is how one request in a bulk action went.
type BulkResult struct {
	ID    int64  `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ErrNotPending is a bulk decision on a request that isn't waiting for one.
var ErrNotPending = errors.New("not waiting for approval")

// Bulk approves or declines several pending requests, one after another, each on its own:
// a failure is reported for that request and the rest still go ahead. Approvals queue
// their searches like any other approval (the job runner's indexer-search class), so a
// bulk approve never fans out more than a couple of searches at a time. by is who
// decided; profile, when set, is used for every approval, and reason for every decline.
func (s *Service) Bulk(ctx context.Context, action string, ids []int64, profile, reason string, by int64, byName string) []BulkResult {
	out := make([]BulkResult, 0, len(ids))
	for _, id := range ids {
		res := BulkResult{ID: id}
		req, err := s.repo.Get(ctx, id)
		switch {
		case err != nil:
		case req.Status != StatusPending:
			err = ErrNotPending
		case action == BulkApprove:
			_, err = s.Approve(ctx, id, ApproveOptions{Profile: profile, DecidedBy: by, DecidedByName: byName})
		default:
			err = s.Decline(ctx, id, DeclineOptions{Reason: reason, DecidedBy: by, DecidedByName: byName})
		}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
		}
		out = append(out, res)
	}
	return out
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
	// Withdrawn (or deleted by staff): whatever it counted is given back.
	if err := s.repo.refundQuota(ctx, id); err != nil {
		s.log.Warn("request: couldn't give back the request's quota", "request", id, "err", err)
	}
	if getErr == nil {
		s.publishUpdated(req, eventDeleted, users)
	}
	return nil
}

// chunkSize keeps IN lists well inside SQLite's limit on bound values.
const chunkSize = 500

// chunks splits a lookup into IN lists of at most chunkSize.
func chunks[T any](xs []T) [][]T {
	var out [][]T
	for lo := 0; lo < len(xs); lo += chunkSize {
		out = append(out, xs[lo:min(lo+chunkSize, len(xs))])
	}
	return out
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
		misses          int    // searches in a row that found nothing
		lastSearch      string // when the sweep last looked, as stored
		nextCheck       string // books: when the ladder looks again (RFC3339)
	}
	// Only these requests' media: a query per media type (per few hundred requests), not
	// the whole library.
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
	movHave := map[int]lib{}
	for _, part := range chunks(movieIDs) {
		if s.movies == nil {
			break
		}
		ms, err := s.movies.ByTMDBIDs(ctx, part)
		if err != nil {
			s.log.Warn("requests: couldn't look up the requested movies", "err", err)
			break
		}
		// The sweep's backoff for just these films; a failure only loses "last checked".
		stamps, _ := s.movies.SearchStatesFor(ctx, movieLibIDs(ms))
		for _, m := range ms {
			st := stamps[m.ID]
			movHave[m.TMDBID] = lib{id: m.ID, have: m.HasFile, released: m.Status == "" || m.Status == "Released",
				misses: st.Misses, lastSearch: st.LastAt}
		}
	}
	serHave := map[int]lib{}
	for _, part := range chunks(seriesIDs) {
		if s.series == nil {
			break
		}
		ss, err := s.series.ByTMDBIDs(ctx, part)
		if err != nil {
			s.log.Warn("requests: couldn't look up the requested shows", "err", err)
			break
		}
		stamps, _ := s.series.SearchStatesFor(ctx, seriesLibIDs(ss))
		for _, sr := range ss {
			st := stamps[sr.ID]
			l := lib{id: sr.ID, released: true, misses: st.Misses, lastSearch: st.LastAt}
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
	keyOwner := map[string]int64{}
	bookByID := map[int64]books.Book{}
	var bs []books.Book
	if s.books != nil && (len(olKeys) > 0 || len(bookIDs) > 0) {
		for _, part := range chunks(olKeys) {
			got, err := s.books.ByKeysOrIDs(ctx, part, nil)
			if err != nil {
				s.log.Warn("requests: couldn't look up the requested books", "err", err)
				break
			}
			bs = append(bs, got...)
			// A request made under a key the book has since left still finds it.
			owners, err := s.books.KeyOwners(ctx, part)
			if err != nil {
				s.log.Warn("requests: couldn't look up the requested books", "err", err)
				break
			}
			for k, id := range owners {
				keyOwner[k] = id
			}
		}
		for _, part := range chunks(bookIDs) {
			got, err := s.books.ByKeysOrIDs(ctx, nil, part)
			if err != nil {
				s.log.Warn("requests: couldn't look up the requested books", "err", err)
				break
			}
			bs = append(bs, got...)
		}
	}
	{
		for _, b := range bs {
			bookByID[b.ID] = b
		}
		for k, id := range keyOwner {
			if b, ok := bookByID[id]; ok {
				bookByKey[k] = b // any key a book has had, not only its current one
			}
		}
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
				l = lib{id: b.ID, have: bookReady(reqs[i].Formats, b), released: true, misses: b.SearchMisses, lastSearch: b.LastSearchAt}
				// Only a monitored book has a next check: the sweep never looks at the others.
				if next := books.NextSearchAt(b.LastSearchAt, b.SearchMisses); b.Monitored && !next.IsZero() {
					l.nextCheck = next.UTC().Format(time.RFC3339)
				}
				reqs[i].partNote = partialNote(reqs[i].Formats, b)
			}
		}
		reqs[i].Available = l.have
		reqs[i].libID, reqs[i].epHave, reqs[i].epTotal, reqs[i].released = l.id, l.epHave, l.epTotal, l.released
		reqs[i].searchMisses, reqs[i].lastSearchAt, reqs[i].nextCheckAt = l.misses, l.lastSearch, l.nextCheck
		if reqs[i].MediaType == "series" && len(reqs[i].Seasons) > 0 && l.id > 0 {
			s.enrichSeasons(ctx, &reqs[i], l.id, progBySeries)
		}
	}
}

// movieLibIDs and seriesLibIDs are the library ids of a lookup's rows.
func movieLibIDs(ms []movies.Movie) []int64 {
	out := make([]int64, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func seriesLibIDs(ss []series.Series) []int64 {
	out := make([]int64, 0, len(ss))
	for _, sr := range ss {
		out = append(out, sr.ID)
	}
	return out
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
