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
	log        *slog.Logger
}

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

// Create records a new request. When autoApprove is set it's approved (and added)
// immediately. A duplicate request for the same media returns the existing one with
// ErrExists.
func (s *Service) Create(ctx context.Context, in Request, autoApprove bool) (Request, error) {
	switch in.MediaType {
	case "movie", "series":
		if in.TMDBID == 0 {
			return Request{}, fmt.Errorf("tmdb_id is required")
		}
		if existing, ok := s.repo.GetByMedia(ctx, in.MediaType, in.TMDBID); ok {
			return existing, ErrExists
		}
	case "book":
		if in.OLKey == "" {
			return Request{}, fmt.Errorf("ol_key is required")
		}
		if existing, ok := s.repo.GetByBook(ctx, in.OLKey); ok {
			return existing, ErrExists
		}
	default:
		return Request{}, fmt.Errorf("media_type must be movie, series or book")
	}
	in.Status = StatusPending
	created, err := s.repo.Create(ctx, in)
	if err != nil {
		return Request{}, err
	}
	s.log.Info("request created", "media", in.MediaType, "title", in.Title, "by", in.RequestedByName, "auto_approve", autoApprove)
	if autoApprove {
		return s.Approve(ctx, created.ID, in.QualityProfile)
	}
	return created, nil
}

// Approve adds the requested media to the Movies/Series module (monitored) and starts
// a search, then marks the request approved. If the media is already in the library,
// it's still marked approved (no duplicate add).
func (s *Service) Approve(ctx context.Context, id int64, profile string) (Request, error) {
	req, err := s.repo.Get(ctx, id)
	if err != nil {
		return Request{}, err
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
		return Request{}, err
	}
	s.log.Info("request approved", "media", req.MediaType, "title", req.Title, "profile", profile)
	return s.repo.Get(ctx, id)
}

// Decline rejects a request without adding anything.
func (s *Service) Decline(ctx context.Context, id int64) error {
	return s.repo.SetStatus(ctx, id, StatusDeclined, "")
}

// Delete removes a request record.
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
