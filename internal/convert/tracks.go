package convert

import (
	"context"
	"errors"
	"sort"
	"strings"
)

// errNoKeepLangs is refusing to run a sweep that would drop nothing — the settings say
// keep everything, so every file is already correct.
var errNoKeepLangs = errors.New("no languages are set to keep — set them in Convert → Audio & subtitle tracks first")

// Track cleanup: dropping the audio and subtitle languages you don't keep, without
// re-encoding anything.
//
// This lives here rather than beside a conversion sweep because the files that need it
// are usually the ones a conversion sweep never looks at. "Candidate" means re-encoding
// would save space, and a file already in the target codec answers no — while still
// carrying the thirty subtitle tracks its WEB-DL shipped with.

// TrackCleanupItem is one movie needing cleanup.
type TrackCleanupItem struct {
	MovieID    int64  `json:"movie_id"`
	Title      string `json:"title"`
	Year       int    `json:"year,omitempty"`
	Path       string `json:"path"`
	SubsNow    int    `json:"subs_now"`
	SubsAfter  int    `json:"subs_after"`
	AudioNow   int    `json:"audio_now"`
	AudioAfter int    `json:"audio_after"`
}

// TrackCleanupSeries is one show's worth, rolled up.
type TrackCleanupSeries struct {
	SeriesID  int64  `json:"series_id"`
	Title     string `json:"title"`
	Year      int    `json:"year,omitempty"`
	Episodes  int    `json:"episodes"`  // episodes that would change
	DropSubs  int    `json:"drop_subs"` // subtitle tracks that would go, across the show
	DropAudio int    `json:"drop_audio"`
}

// TrackCleanup is the whole picture for the Tracks tab.
type TrackCleanup struct {
	KeepSubs  []string             `json:"keep_subs"`
	KeepAudio []string             `json:"keep_audio"`
	Movies    []TrackCleanupItem   `json:"movies"`
	Series    []TrackCleanupSeries `json:"series"`
	// Clean is how many indexed files already carry only the kept languages — the
	// difference between "nothing to do" and "nothing configured".
	Clean int `json:"clean"`
}

// TrackCleanupSummary lists everything that would change if a cleanup ran now.
//
// Reads the library index, which already holds each file's probed MediaInfo, so this is
// one query and no filesystem access.
func (s *Service) TrackCleanupSummary(ctx context.Context) (TrackCleanup, error) {
	plan := s.RemuxPlan(ctx)
	// Every slice here is non-nil on purpose. splitCSV returns a nil slice for an empty
	// setting, and a nil slice marshals to JSON null, not [] — which crashed the page on
	// the very case this screen exists to explain: nothing configured yet.
	out := TrackCleanup{
		KeepSubs:  nonEmpty(plan.Subs.KeepLangs),
		KeepAudio: nonEmpty(plan.Audio.KeepLangs),
		Movies:    []TrackCleanupItem{},
		Series:    []TrackCleanupSeries{},
	}
	if len(out.KeepSubs) == 0 && len(out.KeepAudio) == 0 {
		return out, nil // nothing configured — the UI says so rather than showing an empty list
	}

	movies, err := s.indexedCandidates(ctx, "movie", 0)
	if err != nil {
		return out, err
	}
	for _, c := range movies {
		if !NeedsTrackCleanup(c.Info, plan) {
			out.Clean++
			continue
		}
		out.Movies = append(out.Movies, TrackCleanupItem{
			MovieID: c.MovieID, Title: c.Title, Year: c.Year, Path: c.Path,
			SubsNow: len(c.Info.Subs), SubsAfter: len(keptSubs(c.Info, plan)),
			AudioNow: len(c.Info.Audio), AudioAfter: len(keptAudio(c.Info, plan)),
		})
	}

	eps, err := s.indexedCandidates(ctx, "episode", 0)
	if err != nil {
		return out, err
	}
	byID := map[int64]*TrackCleanupSeries{}
	for _, c := range eps {
		if !NeedsTrackCleanup(c.Info, plan) {
			out.Clean++
			continue
		}
		g := byID[c.SeriesID]
		if g == nil {
			// The indexed title is "Show - S01E02"; keep just the show for the roll-up.
			name := c.Title
			if i := strings.LastIndex(name, " - S"); i > 0 {
				name = name[:i]
			}
			g = &TrackCleanupSeries{SeriesID: c.SeriesID, Title: name, Year: c.Year}
			byID[c.SeriesID] = g
		}
		g.Episodes++
		g.DropSubs += len(c.Info.Subs) - len(keptSubs(c.Info, plan))
		g.DropAudio += len(c.Info.Audio) - len(keptAudio(c.Info, plan))
	}
	for _, g := range byID {
		out.Series = append(out.Series, *g)
	}

	// Most to gain first, matching every other list in this module.
	sort.SliceStable(out.Series, func(i, j int) bool {
		if out.Series[i].Episodes != out.Series[j].Episodes {
			return out.Series[i].Episodes > out.Series[j].Episodes
		}
		return strings.ToLower(out.Series[i].Title) < strings.ToLower(out.Series[j].Title)
	})
	sort.SliceStable(out.Movies, func(i, j int) bool {
		di := out.Movies[i].SubsNow - out.Movies[i].SubsAfter
		dj := out.Movies[j].SubsNow - out.Movies[j].SubsAfter
		if di != dj {
			return di > dj
		}
		return strings.ToLower(out.Movies[i].Title) < strings.ToLower(out.Movies[j].Title)
	})
	return out, nil
}

// QueueTrackCleanupAll queues every file that would change. Returns how many.
func (s *Service) QueueTrackCleanupAll(ctx context.Context) (int, error) {
	plan := s.RemuxPlan(ctx)
	if len(plan.Subs.KeepLangs) == 0 && len(plan.Audio.KeepLangs) == 0 {
		return 0, errNoKeepLangs
	}
	maxFail := s.maxFailures(ctx)
	queued, clean := 0, 0

	movies, err := s.indexedCandidates(ctx, "movie", 0)
	if err != nil {
		return 0, err
	}
	for _, c := range movies {
		if !NeedsTrackCleanup(c.Info, plan) {
			clean++
			continue
		}
		if s.failures.blocklisted(ctx, movieKey(c.MovieID), maxFail) {
			continue
		}
		if _, err := s.queueMovie(ctx, c.MovieID, plan); err != nil {
			s.log.Warn("convert: queue movie track cleanup failed", "movie", c.MovieID, "err", err)
			continue
		}
		queued++
	}

	eps, err := s.indexedCandidates(ctx, "episode", 0)
	if err != nil {
		return queued, err
	}
	for _, c := range eps {
		if !NeedsTrackCleanup(c.Info, plan) {
			clean++
			continue
		}
		if s.failures.blocklisted(ctx, episodeKey(c.SeriesID, c.Season, c.Episode), maxFail) {
			continue
		}
		if _, err := s.enqueueEpisodeIndexed(ctx, c, plan); err != nil {
			s.log.Warn("convert: queue episode track cleanup failed",
				"series", c.SeriesID, "season", c.Season, "episode", c.Episode, "err", err)
			continue
		}
		queued++
	}

	s.log.Info("convert: queued track cleanup across the library",
		"queued", queued, "already_clean", clean,
		"keep_subs", plan.Subs.KeepLangs, "keep_audio", plan.Audio.KeepLangs)
	return queued, nil
}

// nonEmpty guarantees a slice marshals as [] rather than null.
func nonEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
