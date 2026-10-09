package insights

import (
	"context"
	"strconv"
	"strings"
)

// ImportedSession is a historical play session (e.g. from Tautulli) used to backfill Insights so
// its stats/graphs aren't empty on day one. Neutral shape so the importer isn't coupled to a source.
type ImportedSession struct {
	UserID           int64
	UserName         string
	UserThumb        string // user avatar (NOT the media poster)
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
	DurationMS       int64 // the MEDIA's runtime, if the source reports one
	WatchedMS        int64 // actual time watched — Tautulli's `duration`, which is NOT the runtime
	PausedMS         int64
}

// Imports must run one at a time: the sessionExists dedupe check is only reliable then. The
// import is an insights.import-tautulli job, and the job runner's single-flight on it is what
// turns a double-clicked import into "already running".

// normalizeDecision maps a Tautulli transcode_decision to the vocabulary the live recorder uses
// ("direct_play"/"direct_stream"/"transcode", see plex.Session.Decision) so imported and live rows
// group and filter together. Case-insensitive and trimmed; unknown values pass through lower-cased.
func normalizeDecision(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "direct play", "direct_play":
		return "direct_play"
	case "copy", "direct stream", "direct_stream":
		return "direct_stream"
	case "transcode":
		return "transcode"
	default:
		return strings.ToLower(strings.TrimSpace(d))
	}
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
		if stopped == 0 && r.WatchedMS > 0 {
			// No stop timestamp: the best we can say is that the session ran for as long
			// as it was watched. Good enough to keep the row, and watched_ms carries the
			// figure that actually matters either way.
			stopped = r.StartedAt + r.WatchedMS/1000
		}
		// Guard against un-computable durations (in-progress rows with stopped=0/duration=0, or
		// clock-skewed rows): a row with stopped <= started poisons every SUM(watched) aggregate
		// with a huge negative wall time. Skip it rather than record garbage.
		if stopped <= r.StartedAt {
			skipped++
			continue
		}
		if _, err := s.repo.insertSession(ctx, sessionRecord{
			UserID: uid, UserName: r.UserName, RatingKey: r.RatingKey, MediaType: r.MediaType,
			Title: r.Title, GrandparentTitle: r.GrandparentTitle, ParentTitle: r.ParentTitle,
			MediaIndex: r.MediaIndex, ParentIndex: r.ParentIndex, Year: r.Year, Thumb: r.Thumb,
			Player: r.Player, Platform: r.Platform, Product: r.Product, IPAddress: r.IPAddress,
			Decision:  normalizeDecision(r.Decision),
			StartedAt: r.StartedAt, StoppedAt: stopped, DurationMS: r.DurationMS,
			WatchedMS: r.WatchedMS, PausedMS: r.PausedMS,
		}); err != nil {
			continue
		}
		// Use the user avatar, never the media poster (r.Thumb), and don't overwrite a good
		// avatar with an empty one — upsertUser keeps the existing thumb when the new one is blank.
		s.repo.upsertUser(ctx, uid, r.UserName, r.UserThumb, stopped)
		imported++
	}
	return imported, skipped
}
