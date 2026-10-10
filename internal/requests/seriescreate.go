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
// never both cover the same season. A new row (or a declined one asked for again) is
// announced like any new ask; following an existing request isn't one.
func (s *Service) createSeries(ctx context.Context, in Request, opts CreateOptions) (Request, bool, error) {
	q, err := s.quotaFor(ctx, in, opts)
	if err != nil {
		return Request{}, false, err
	}
	if q != nil {
		s.quotaMu.Lock() // before seriesMu, always
	}
	s.seriesMu.Lock()
	created, subscribed, inserted, err := s.createSeriesLocked(ctx, in, opts, q)
	s.seriesMu.Unlock()
	if q != nil {
		s.quotaMu.Unlock()
	}
	if err != nil {
		return Request{}, false, err
	}
	switch {
	case inserted:
		s.log.Info("request created", "media", in.MediaType, "title", in.Title, "seasons", series.SeasonsLabel(created.Seasons),
			"by", in.RequestedByName, "auto_approve", opts.AutoApprove)
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
	case !subscribed:
		s.announceCreated(ctx, created, opts) // a declined request re-opened
	case !opts.Silent:
		s.publishUpdated(created, created.Status, s.parties(ctx, created))
	}
	return created, subscribed, nil
}

// createSeriesLocked plans and writes a series request. inserted is true for a new row
// (which an auto-approving requester's request is then approved as).
//
// Asking again for seasons a declined request asked for is a re-request, whether it
// re-opens that row or makes a new one: it needs a note (nothing is written without one)
// and is flagged with the earlier decline's reason.
//
// A limited requester's new row counts its seasons (a whole-show ask, every known regular
// season); following rows that cover what they asked for is free.
func (s *Service) createSeriesLocked(ctx context.Context, in Request, opts CreateOptions, q *quotaCharge) (out Request, subscribed, inserted bool, err error) {
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
	if plan.insert {
		if prev, ok := lastDeclinedOverlapping(existing, plan.seasons); ok {
			if err := needsNote(in, opts, prev); err != nil {
				return Request{}, false, false, err
			}
			if plan.reopen == 0 {
				in.ReRequest, in.DeclineReason = 1, prev.DeclineReason
			}
		}
	}
	units := 0
	if plan.insert {
		asked := plan.seasons
		if plan.reopen > 0 {
			asked = byID[plan.reopen].Seasons
		}
		units = seasonUnits(asked, known)
		// Nothing is written — not even the follows — when the new seasons don't fit.
		if err := s.checkQuota(ctx, q, QuotaSeason, units); err != nil {
			return Request{}, false, false, err
		}
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
	s.chargeQuota(ctx, q, out.ID, QuotaSeason, units)
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

// lastDeclinedOverlapping is the newest declined request among rows that asked for any of
// seasons (nil: the whole show).
func lastDeclinedOverlapping(rows []Request, seasons []int) (Request, bool) {
	var out Request
	found := false
	for _, r := range rows {
		if r.Status == StatusDeclined && overlaps(r.Seasons, seasons) && (!found || r.ID > out.ID) {
			out, found = r, true
		}
	}
	return out, found
}
