package requests

import (
	"context"
	"errors"
	"fmt"

	"github.com/tristenlammi/arrmada/internal/series"
)

// createSeries records a series request, split against what's already covered (see
// planSeasons): a row for the seasons nobody has asked for yet, the caller following the
// rows that cover the rest. It runs under seriesMu, so two people asking at once can
// never both cover the same season.
func (s *Service) createSeries(ctx context.Context, in Request, autoApprove bool) (Request, bool, error) {
	s.seriesMu.Lock()
	created, subscribed, inserted, err := s.createSeriesLocked(ctx, in)
	s.seriesMu.Unlock()
	if err != nil {
		return Request{}, false, err
	}
	if inserted {
		s.log.Info("request created", "media", in.MediaType, "title", in.Title, "seasons", series.SeasonsLabel(created.Seasons),
			"by", in.RequestedByName, "auto_approve", autoApprove)
		if autoApprove {
			created, err = s.Approve(ctx, created.ID, in.QualityProfile) // publishes approved
			return created, false, err
		}
	}
	s.publishUpdated(created, created.Status, s.parties(ctx, created))
	return created, subscribed, nil
}

// createSeriesLocked plans and writes a series request. inserted is true for a new row
// (which an auto-approving requester's request is then approved as).
func (s *Service) createSeriesLocked(ctx context.Context, in Request) (out Request, subscribed, inserted bool, err error) {
	existing, err := s.repo.ListByMedia(ctx, "series", in.TMDBID)
	if err != nil {
		return Request{}, false, false, err
	}
	rows := make([]seasonRow, 0, len(existing))
	byID := map[int64]Request{}
	for _, r := range existing {
		byID[r.ID] = r
		row := seasonRow{id: r.ID, status: r.Status, seasons: r.Seasons, requestedBy: r.RequestedBy}
		if r.Status == StatusApproved {
			row.done = s.delivered(ctx, r)
		}
		rows = append(rows, row)
	}
	var known []int // nil: no catalogue
	if in.KnownSeasons != nil {
		known = append([]int{}, in.KnownSeasons...)
	}
	onDisk := map[int]bool{}
	if s.series != nil {
		if sr, gerr := s.series.GetByTMDB(ctx, in.TMDBID); gerr == nil {
			prog, perr := s.series.SeasonProgress(ctx, sr.ID)
			if perr != nil {
				return Request{}, false, false, perr
			}
			for n, p := range prog {
				if p.OnDisk() {
					onDisk[n] = true
				}
				if known != nil {
					known = append(known, n) // the library's own seasons count as known too
				}
			}
		} else if !errors.Is(gerr, series.ErrNotFound) {
			return Request{}, false, false, gerr
		}
	}

	plan, err := planSeasons(in.Seasons, known, onDisk, rows)
	if err != nil {
		return Request{}, false, false, err
	}

	// Follow the rows that cover what the caller asked for and the new row doesn't.
	for _, id := range plan.follow {
		if r := byID[id]; in.RequestedBy > 0 && in.RequestedBy != r.RequestedBy {
			if err := s.repo.AddSubscriber(ctx, id, in.RequestedBy, in.RequestedByName); err != nil {
				return Request{}, false, false, err
			}
			s.log.Info("request subscribed", "media", r.MediaType, "title", r.Title, "by", in.RequestedByName)
		}
	}
	if !plan.insert {
		out, err = s.repo.Get(ctx, plan.follow[0])
		return out, true, false, err
	}

	if plan.reopen > 0 {
		out, _, err = s.attachToExisting(ctx, byID[plan.reopen], in)
		if err != nil {
			return Request{}, false, false, err
		}
	} else {
		in.Status = StatusPending
		in.Seasons = plan.seasons
		out, err = s.repo.Create(ctx, in)
		if err != nil {
			return Request{}, false, false, fmt.Errorf("create request: %w", err)
		}
		inserted = true
	}
	// Whoever asked for these seasons before and was turned down hears how it goes now.
	for _, uid := range plan.declinedBy {
		if uid == in.RequestedBy || uid == out.RequestedBy {
			continue
		}
		name := ""
		for _, r := range existing {
			if r.RequestedBy == uid {
				name = r.RequestedByName
			}
		}
		if err := s.repo.AddSubscriber(ctx, out.ID, uid, name); err != nil {
			s.log.Warn("request: could not subscribe an earlier requester", "id", out.ID, "user", uid, "err", err)
		}
	}
	return out, false, inserted, nil
}
