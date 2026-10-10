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

// ImportOptions tunes one import pass.
type ImportOptions struct {
	// Before, when set (epoch seconds), skips plays that started at or after it — "only
	// import plays from before Arrmada started recording". The overlap check already
	// skips plays recorded live; this is for owners who'd rather not mix sources at all.
	Before int64
	// RunID stamps every play this pass inserts with its import run, so "Remove this
	// import" can find exactly those rows again. 0 = not part of a tracked run.
	RunID int64
}

// ImportCounts is what one import pass did with each row. Every row lands in exactly one
// bucket, so the buckets add up to the rows offered.
type ImportCounts struct {
	Imported    int    `json:"imported"`
	Duplicate   int    `json:"duplicates"`   // already imported (same user, item and start)
	Overlap     int    `json:"overlaps"`     // the same play was recorded live
	Invalid     int    `json:"invalid"`      // no start, or a stop at/before the start
	AfterCutoff int    `json:"after_cutoff"` // started at or after ImportOptions.Before
	Failed      int    `json:"failed"`       // the check or the insert errored
	FirstError  string `json:"first_error,omitempty"`
}

// Add folds another pass's counts into c (the first error seen wins).
func (c *ImportCounts) Add(o ImportCounts) {
	c.Imported += o.Imported
	c.Duplicate += o.Duplicate
	c.Overlap += o.Overlap
	c.Invalid += o.Invalid
	c.AfterCutoff += o.AfterCutoff
	c.Failed += o.Failed
	if c.FirstError == "" {
		c.FirstError = o.FirstError
	}
}

// Processed is how many rows the counts cover.
func (c ImportCounts) Processed() int {
	return c.Imported + c.Duplicate + c.Overlap + c.Invalid + c.AfterCutoff + c.Failed
}

func (c *ImportCounts) fail(err error) {
	c.Failed++
	if c.FirstError == "" {
		c.FirstError = err.Error()
	}
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

// ImportHistory records historical sessions. A row is skipped when it was imported before
// (same user + item + start, so a re-run adds nothing) and when Arrmada already recorded the
// same play live — Tautulli and the poller both watch the same server, so without that check
// every play in the overlap period counted twice. A check that errors skips the row (counted
// as failed): when unsure, never risk a double count.
func (s *Service) ImportHistory(ctx context.Context, rows []ImportedSession, opts ImportOptions) ImportCounts {
	var c ImportCounts
	for _, r := range rows {
		if ctx.Err() != nil {
			break
		}
		if r.StartedAt == 0 {
			c.Invalid++
			continue
		}
		if opts.Before > 0 && r.StartedAt >= opts.Before {
			c.AfterCutoff++
			continue
		}
		uid := strconv.FormatInt(r.UserID, 10)
		if r.UserID == 0 {
			uid = ""
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
			c.Invalid++
			continue
		}
		exists, err := s.repo.sessionExists(ctx, uid, r.RatingKey, r.StartedAt)
		if err != nil {
			c.fail(err)
			continue
		}
		if exists {
			c.Duplicate++
			continue
		}
		live, err := s.repo.overlapsLive(ctx, playWindow{
			UserID: uid, RatingKey: r.RatingKey, Title: r.Title, GrandparentTitle: r.GrandparentTitle,
			ParentIndex: r.ParentIndex, MediaIndex: r.MediaIndex,
			Start: r.StartedAt, End: importedEnd(r.StartedAt, stopped, r.WatchedMS, r.PausedMS),
		})
		if err != nil {
			c.fail(err)
			continue
		}
		if live {
			c.Overlap++
			continue
		}
		if _, err := s.repo.insertSession(ctx, sessionRecord{
			UserID: uid, UserName: r.UserName, RatingKey: r.RatingKey, MediaType: r.MediaType,
			Title: r.Title, GrandparentTitle: r.GrandparentTitle, ParentTitle: r.ParentTitle,
			MediaIndex: r.MediaIndex, ParentIndex: r.ParentIndex, Year: r.Year, Thumb: r.Thumb,
			Player: r.Player, Platform: r.Platform, Product: r.Product, IPAddress: r.IPAddress,
			Decision:  normalizeDecision(r.Decision),
			StartedAt: r.StartedAt, StoppedAt: stopped, DurationMS: r.DurationMS,
			WatchedMS: r.WatchedMS, PausedMS: r.PausedMS, ImportRunID: opts.RunID,
		}); err != nil {
			c.fail(err)
			continue
		}
		// Use the user avatar, never the media poster (r.Thumb), and don't overwrite a good
		// avatar with an empty one — upsertUser keeps the existing thumb when the new one is blank.
		_ = s.repo.upsertUser(ctx, uid, r.UserName, r.UserThumb, stopped)
		c.Imported++
	}
	return c
}

// FirstLiveStart is when Arrmada first recorded a play itself (epoch seconds, 0 = never) —
// the natural cutoff for "only import plays from before Arrmada was watching".
func (s *Service) FirstLiveStart(ctx context.Context) (int64, error) {
	return s.repo.firstLiveStart(ctx)
}

// ImportOverlaps counts imported plays that duplicate a play Arrmada recorded live — the
// double counts imports made before they learned to skip those. firstLive is when live
// recording began, for the explanation next to the count.
func (s *Service) ImportOverlaps(ctx context.Context) (count int, firstLive int64, err error) {
	if count, err = s.repo.countImportOverlaps(ctx); err != nil {
		return 0, 0, err
	}
	firstLive, err = s.repo.firstLiveStart(ctx)
	return count, firstLive, err
}

// RemoveImportOverlaps deletes the imported plays ImportOverlaps counts, with their buffer
// events, in one transaction. Live rows are never touched. expected is the count the owner
// confirmed; if it no longer matches (an import ran since), nothing is deleted and
// ErrOverlapsChanged says to look again. expected < 0 skips that check.
func (s *Service) RemoveImportOverlaps(ctx context.Context, expected int) (int64, error) {
	return s.repo.removeImportOverlaps(ctx, expected)
}
