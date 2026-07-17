package insights

import (
	"context"
	"strconv"
)

// ImportedSession is a historical play session (e.g. from Tautulli) used to backfill Insights so
// its stats/graphs aren't empty on day one. Neutral shape so the importer isn't coupled to a source.
type ImportedSession struct {
	UserID           int64
	UserName         string
	RatingKey        string
	MediaType        string
	Title            string
	GrandparentTitle string
	ParentTitle      string
	MediaIndex       int
	ParentIndex      int
	Year             int
	Thumb            string
	Player           string
	Platform         string
	Product          string
	IPAddress        string
	Decision         string
	StartedAt        int64 // epoch seconds
	StoppedAt        int64
	DurationMS       int64
	PausedMS         int64
}

// ImportHistory records historical sessions, skipping any already present (idempotent on
// user + item + start). Returns how many were imported vs skipped.
func (s *Service) ImportHistory(ctx context.Context, rows []ImportedSession) (imported, skipped int) {
	for _, r := range rows {
		if ctx.Err() != nil {
			break
		}
		if r.StartedAt == 0 {
			continue
		}
		uid := strconv.FormatInt(r.UserID, 10)
		if r.UserID == 0 {
			uid = ""
		}
		if s.repo.sessionExists(ctx, uid, r.RatingKey, r.StartedAt) {
			skipped++
			continue
		}
		stopped := r.StoppedAt
		if stopped == 0 && r.DurationMS > 0 {
			stopped = r.StartedAt + r.DurationMS/1000
		}
		if _, err := s.repo.insertSession(ctx, sessionRecord{
			UserID: uid, UserName: r.UserName, RatingKey: r.RatingKey, MediaType: r.MediaType,
			Title: r.Title, GrandparentTitle: r.GrandparentTitle, ParentTitle: r.ParentTitle,
			MediaIndex: r.MediaIndex, ParentIndex: r.ParentIndex, Year: r.Year, Thumb: r.Thumb,
			Player: r.Player, Platform: r.Platform, Product: r.Product, IPAddress: r.IPAddress, Decision: r.Decision,
			StartedAt: r.StartedAt, StoppedAt: stopped, DurationMS: r.DurationMS, PausedMS: r.PausedMS,
		}); err != nil {
			continue
		}
		s.repo.upsertUser(ctx, uid, r.UserName, r.Thumb, stopped)
		imported++
	}
	return imported, skipped
}
