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
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
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
	log        *slog.Logger
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
	return &Service{repo: NewRepo(db), movies: mv, series: sr, books: bk, coord: coord, quality: q, bus: bus, appriseBin: appriseBin, log: log}
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
		return s.attachToExisting(ctx, existing, in)
	}
	in.Status = StatusPending
	created, err = s.repo.Create(ctx, in)
	if errors.Is(err, ErrExists) {
		// Lost a create race: someone inserted the same media between our existence
		// check and the INSERT. Re-fetch and attach instead of failing.
		if existing, ok := s.lookupExisting(ctx, in); ok {
			return s.attachToExisting(ctx, existing, in)
		}
		return Request{}, false, err
	}
	if err != nil {
		return Request{}, false, err
	}
	s.log.Info("request created", "media", in.MediaType, "title", in.Title, "by", in.RequestedByName, "auto_approve", autoApprove)
	if autoApprove {
		created, err = s.Approve(ctx, created.ID, in.QualityProfile)
		return created, false, err
	}
	return created, false, nil
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
	}
	if profile == "" {
		profile = s.quality.DefaultProfile(ctx, req.MediaType)
	}

	switch req.MediaType {
	case "movie":
		m, addErr := s.movies.Add(ctx, req.TMDBID, profile, true)
		if addErr != nil && !errors.Is(addErr, movies.ErrExists) {
			return Request{}, addErr
		}
		if addErr == nil {
			go func(mid int64) {
				c, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				if err := s.coord.SearchMovie(c, mid); err != nil {
					s.log.Warn("request: movie search failed", "title", req.Title, "err", err)
				}
			}(m.ID)
		}
	case "series":
		sr, addErr := s.series.Add(ctx, req.TMDBID, profile, true)
		if addErr != nil && !errors.Is(addErr, series.ErrExists) {
			return Request{}, addErr
		}
		if addErr == nil {
			go func(sid int64) {
				c, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				if err := s.coord.SearchSeriesNow(c, sid); err != nil {
					s.log.Warn("request: series search failed", "title", req.Title, "err", err)
				}
			}(sr.ID)
		}
	case "book":
		b, addErr := s.books.Add(ctx, req.OLKey, profile, true, metadata.BookResult{
			Key: req.OLKey, Title: req.Title, Author: req.Author, Year: req.Year, CoverURL: req.PosterURL,
		})
		if addErr != nil && !errors.Is(addErr, books.ErrExists) {
			return Request{}, addErr
		}
		if addErr == nil {
			go func(bid int64) {
				c, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				if err := s.coord.SearchBookNow(c, bid); err != nil {
					s.log.Warn("request: book search failed", "title", req.Title, "err", err)
				}
			}(b.ID)
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
	return s.repo.Get(ctx, id)
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
	return nil
}

// Delete removes a request record (and its subscribers).
func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.repo.Delete(ctx, id)
}

// enrichAvailability marks each request available if its media is (partly) on disk.
func (s *Service) enrichAvailability(ctx context.Context, reqs []Request) {
	if len(reqs) == 0 {
		return
	}
	movHave := map[int]bool{}
	if ms, err := s.movies.List(ctx); err == nil {
		for _, m := range ms {
			movHave[m.TMDBID] = m.HasFile
		}
	}
	serHave := map[int]bool{}
	if ss, err := s.series.List(ctx); err == nil {
		for _, sr := range ss {
			serHave[sr.TMDBID] = sr.Stats != nil && sr.Stats.HaveFiles > 0
		}
	}
	bookHave := map[string]bool{}
	if bs, err := s.books.List(ctx); err == nil {
		for _, b := range bs {
			bookHave[b.OLKey] = b.HasFile
		}
	}
	for i := range reqs {
		switch reqs[i].MediaType {
		case "movie":
			reqs[i].Available = movHave[reqs[i].TMDBID]
		case "series":
			reqs[i].Available = serHave[reqs[i].TMDBID]
		case "book":
			reqs[i].Available = bookHave[reqs[i].OLKey]
		}
	}
}
