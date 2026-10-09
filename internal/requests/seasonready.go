package requests

import (
	"context"

	"github.com/tristenlammi/arrmada/internal/series"
)

// seasonReady is the one rule for "this season of a request is complete", shared by the
// request's tracking and its ready notices: every monitored episode that has aired is on
// disk, and something is. Counting monitored episodes only means an episode the owner gave
// up on (stopped monitoring) doesn't hold the request back forever.
//
// It is REQ-02's whole-show rule (Have > 0 && Have >= Total over monitored episodes)
// restricted to one season; when the two branches meet, both must stay this one rule.
func seasonReady(p series.SeasonProgress) bool {
	return p.MonHave > 0 && p.MonHave >= p.MonTotal
}

// seasonsProgress sums a season-scoped request's own seasons — episodes on disk and
// episodes wanted, over monitored episodes — and says whether it is complete: every one
// of its seasons is ready. A season the library doesn't have isn't.
func seasonsProgress(prog map[int]series.SeasonProgress, seasons []int) (have, total int, ready bool) {
	ready = len(seasons) > 0
	for _, n := range seasons {
		p, ok := prog[n]
		have += p.MonHave
		total += p.MonTotal
		if !ok || !seasonReady(p) {
			ready = false
		}
	}
	return have, total, ready
}

// SeasonRequest is the request standing for one season of a show, as the season list
// shows it: which request, where it is, and whether it's the viewer's own (they asked or
// follow it). RequestedByName is filled for staff only.
type SeasonRequest struct {
	RequestID       int64  `json:"request_id"`
	Status          string `json:"status"`
	Mine            bool   `json:"mine"`
	RequestedByName string `json:"requested_by_name,omitempty"`
}

// SeasonRequests returns, for a show, the active request covering each season it names
// (pending, or approved and not yet delivered — the rows that count as covered when
// someone asks), plus the active whole-show request if there is one. The oldest covering
// request wins. viewer is the signed-in user, for Mine; staff adds the requester's name.
func (s *Service) SeasonRequests(ctx context.Context, tmdbID int, viewer int64, staff bool) (bySeason map[int]SeasonRequest, whole *SeasonRequest, err error) {
	reqs, err := s.repo.ListByMedia(ctx, "series", tmdbID)
	if err != nil {
		return nil, nil, err
	}
	bySeason = map[int]SeasonRequest{}
	for _, r := range reqs {
		row := seasonRow{id: r.ID, status: r.Status, seasons: r.Seasons}
		if r.Status == StatusApproved {
			row.done = s.delivered(ctx, r)
		}
		if !row.active() {
			continue
		}
		sr := SeasonRequest{RequestID: r.ID, Status: r.Status, Mine: viewer > 0 && r.RequestedBy == viewer}
		if !sr.Mine && viewer > 0 {
			if subs, err := s.repo.Subscribers(ctx, r.ID); err == nil {
				for _, sub := range subs {
					if sub.UserID == viewer {
						sr.Mine = true
					}
				}
			}
		}
		if staff {
			sr.RequestedByName = r.RequestedByName
		}
		if len(r.Seasons) == 0 {
			if whole == nil {
				w := sr
				whole = &w
			}
			continue
		}
		for _, n := range r.Seasons {
			if _, taken := bySeason[n]; !taken {
				bySeason[n] = sr
			}
		}
	}
	return bySeason, whole, nil
}

// delivered reports whether a request's "ready" notice has already gone out, so it no
// longer stands for its seasons (a season deleted or aired since must be asked for anew).
// Read from the requester's inbox, which holds one row per reference; a request with no
// requester (an import) counts as delivered once anyone was told.
//
// When phase6/requests' ready_at stamp lands, this should read that instead.
func (s *Service) delivered(ctx context.Context, req Request) bool {
	q := `SELECT 1 FROM user_notifications WHERE ref = ?`
	args := []any{requestRef(req)}
	if req.RequestedBy > 0 {
		q += ` AND user_id = ?`
		args = append(args, req.RequestedBy)
	}
	var one int
	return s.repo.db.QueryRowContext(ctx, q+` LIMIT 1`, args...).Scan(&one) == nil
}
