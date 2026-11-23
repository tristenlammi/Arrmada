package requests

import (
	"context"
	"errors"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/series"
)

// Why a bulk delete's "Also stop looking for these titles" left a title as it was.
const (
	// KeptStillRequested: another request for it (one not being deleted) is still waiting
	// or approved, so someone still wants it.
	KeptStillRequested = "still_requested"
	// KeptNotApproved: none of the deleted requests for it was approved, so none of them
	// added it to the library or asked for it to be looked for.
	KeptNotApproved = "not_approved"
	// KeptNotInLibrary: it isn't in Movies or Series (any more).
	KeptNotInLibrary = "not_in_library"
	// KeptAlreadyOff: it was already not being looked for.
	KeptAlreadyOff = "already_off"
	// KeptBook: books are left alone; the Books page's Unmonitor does that.
	KeptBook = "book"
	// KeptError: reading or changing it failed.
	KeptError = "error"
)

// StoppedTitle is what "Also stop looking for these titles" did to one title of a bulk
// delete: Stopped, or left as it was and why (Reason, one of the Kept* values).
type StoppedTitle struct {
	MediaType string `json:"media_type"`
	TMDBID    int    `json:"tmdb_id,omitempty"`
	OLKey     string `json:"ol_key,omitempty"`
	Title     string `json:"title"`
	Stopped   bool   `json:"stopped"`
	Reason    string `json:"reason,omitempty"`
}

// DeleteAndStopLooking deletes requests as a bulk delete does, then stops looking for the
// titles they were for: a movie is unmonitored and a show's searching is turned off, the
// same switches as Movies → Unmonitor and Series → Searching off. Files are never touched:
// a movie or episode already downloaded stays where it is.
//
// A request doesn't record whether its approval added the title or found it already in the
// library, so a title is only changed when the requests just deleted were the only ones
// holding it: one with another request still waiting or approved is left alone, and so is
// one none of whose deleted requests was ever approved (it was never added or monitored by
// a request). Books are left alone. A request that failed to delete changes nothing.
func (s *Service) DeleteAndStopLooking(ctx context.Context, ids []int64) ([]BulkResult, []StoppedTitle) {
	// Read before deleting: the rows are gone afterwards.
	before := make(map[int64]Request, len(ids))
	for _, id := range ids {
		if r, err := s.repo.Get(ctx, id); err == nil {
			before[id] = r
		}
	}
	results := s.Bulk(ctx, BulkDelete, ids, "", "", 0, "")

	type title struct {
		req      Request // the first deleted request for it
		approved bool    // any deleted request for it was approved
	}
	var order []string
	titles := map[string]*title{}
	for _, res := range results {
		r, ok := before[res.ID]
		if !res.OK || !ok {
			continue
		}
		key := r.MediaType + ":" + strconv.Itoa(r.TMDBID)
		if r.MediaType == "book" {
			key = "book:" + r.OLKey
		}
		t := titles[key]
		if t == nil {
			t = &title{req: r}
			titles[key] = t
			order = append(order, key)
		}
		t.approved = t.approved || r.Status == StatusApproved
	}

	out := make([]StoppedTitle, 0, len(order))
	for _, key := range order {
		t := titles[key]
		st := StoppedTitle{MediaType: t.req.MediaType, TMDBID: t.req.TMDBID, OLKey: t.req.OLKey, Title: t.req.Title}
		st.Reason = s.stopLooking(ctx, t.req, t.approved)
		st.Stopped = st.Reason == ""
		out = append(out, st)
	}
	return results, out
}

// stopLooking turns off looking for the title one deleted request was for, or says why it
// was left as it was ("" = stopped).
func (s *Service) stopLooking(ctx context.Context, req Request, approved bool) string {
	switch {
	case req.MediaType != "movie" && req.MediaType != "series":
		return KeptBook
	case !approved:
		return KeptNotApproved
	}
	// The deleted requests are gone, so any request left for it is one that wasn't deleted.
	others, err := s.repo.ListByMedia(ctx, req.MediaType, req.TMDBID)
	if err != nil {
		s.log.Warn("request: couldn't check a title's other requests", "title", req.Title, "err", err)
		return KeptError
	}
	for _, o := range others {
		if o.Status == StatusPending || o.Status == StatusApproved {
			return KeptStillRequested
		}
	}
	if req.MediaType == "movie" {
		m, err := s.movies.GetByTMDB(ctx, req.TMDBID)
		switch {
		case errors.Is(err, movies.ErrNotFound):
			return KeptNotInLibrary
		case err != nil:
			s.log.Warn("request: couldn't read the movie to unmonitor it", "title", req.Title, "err", err)
			return KeptError
		case !m.Monitored:
			return KeptAlreadyOff
		}
		if err := s.movies.SetMonitored(ctx, m.ID, false); err != nil {
			s.log.Warn("request: couldn't unmonitor the movie", "title", req.Title, "err", err)
			return KeptError
		}
		s.movies.AddEvent(ctx, m.ID, "monitored", "Unmonitored when its request was deleted")
		s.log.Info("request deleted: movie unmonitored", "title", req.Title)
		return ""
	}
	sr, err := s.series.GetByTMDB(ctx, req.TMDBID)
	switch {
	case errors.Is(err, series.ErrNotFound):
		return KeptNotInLibrary
	case err != nil:
		s.log.Warn("request: couldn't read the show to turn its searching off", "title", req.Title, "err", err)
		return KeptError
	case !sr.Monitored:
		return KeptAlreadyOff
	}
	if err := s.series.SetMonitored(ctx, sr.ID, false); err != nil {
		s.log.Warn("request: couldn't turn the show's searching off", "title", req.Title, "err", err)
		return KeptError
	}
	s.series.AddEvent(ctx, sr.ID, "monitored", "Searching turned off when its request was deleted")
	s.log.Info("request deleted: show's searching turned off", "title", req.Title)
	return ""
}
