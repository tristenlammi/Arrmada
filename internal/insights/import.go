package insights

import (
	"context"
	"strconv"
	"strings"
	"sync"
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
	DurationMS       int64
	PausedMS         int64
}

// importMu serializes imports process-wide so a double-clicked import returns "already running"
// instead of interleaving duplicate inserts (the sessionExists dedupe check is only reliable when
// one import runs at a time). Callers use TryStartImport/StopImport around a whole import run.
var importMu sync.Mutex

// TryStartImport reports whether an import may begin. It returns false if one is already running;
// on success the caller MUST pair it with StopImport (typically via defer) when the run finishes.
func (s *Service) TryStartImport() bool { return importMu.TryLock() }

// StopImport releases the import guard acquired by a successful TryStartImport.
func (s *Service) StopImport() { importMu.Unlock() }

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
		if stopped == 0 && r.DurationMS > 0 {
			stopped = r.StartedAt + r.DurationMS/1000
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
			StartedAt: r.StartedAt, StoppedAt: stopped, DurationMS: r.DurationMS, PausedMS: r.PausedMS,
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
