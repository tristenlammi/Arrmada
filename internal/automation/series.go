package automation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// plural returns "s" for counts other than 1.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// showEnded reports whether a series has finished airing (TMDB status "Ended" or
// "Canceled"). Anything else — including an unknown/empty status — is treated as
// still running, so whole-show/multi-season packs are only grabbed when we're sure
// the run is complete.
func showEnded(status string) bool {
	s := strings.ToLower(status)
	return strings.Contains(s, "ended") || strings.Contains(s, "cancel")
}

// isPackTier reports whether a release kind counts as a grabbable "pack" for a show
// in the given state. Running shows accept only single-season packs; ended shows also
// accept multi-season and (leftover) complete-show packs.
func isPackTier(k parser.Kind, ended bool) bool {
	if k == parser.KindSeasonPack {
		return true
	}
	return ended && (k == parser.KindMultiSeason || k == parser.KindCompleteShow)
}

func nowDate() string { return time.Now().UTC().Format("2006-01-02") }

// epKey identifies a wanted (season, episode) pair.
type epKey struct{ season, episode int }

// SearchSeriesMissing sweeps every monitored series and grabs what's missing.
func (c *Coordinator) SearchSeriesMissing(ctx context.Context) {
	if c.series == nil {
		return
	}
	all, err := c.series.List(ctx)
	if err != nil {
		return
	}
	for _, s := range all {
		if !s.Monitored {
			continue
		}
		if err := c.SearchSeriesNow(ctx, s.ID); err != nil {
			c.log.Warn("series: search failed", "series", s.Title, "err", err)
		}
	}
}

// SearchSeriesNow finds and grabs releases for a series' monitored, aired, missing
// episodes. Preference depends on whether the show has finished:
//   - ENDED shows: a complete-series pack, then multi-season packs, then single-season
//     packs, then individual episodes (grab the whole run in as few torrents as possible).
//   - STILL-RUNNING shows: single-season packs, then individual episodes — no whole-show
//     or multi-season packs, since the show isn't finished and each new season is best
//     picked up as its own pack.
func (c *Coordinator) SearchSeriesNow(ctx context.Context, seriesID int64) error {
	if c.series == nil {
		return nil
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return err
	}
	res, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: s.Title, MediaType: indexer.MediaSeries, Limit: 100})
	if err != nil || len(res.Releases) == 0 {
		return err
	}
	c.grabSeriesFrom(ctx, s, res.Releases)
	return nil
}

// grabSeriesFrom applies the season-pack-preference grab logic over a set of releases
// (from an on-demand search, the RSS feed, or a stall re-search) for a series' monitored,
// aired, missing episodes. Blocklisted releases are skipped so a stall re-search doesn't
// re-grab what just failed.
func (c *Coordinator) grabSeriesFrom(ctx context.Context, s series.Series, releases []indexer.Release) {
	wanted, seriesSeasons := wantedEpisodes(s)
	if len(wanted) == 0 {
		return
	}
	blocked := c.blockedSetSeries(ctx, s.ID)

	// Score all candidates with the series' quality profile; keep the eligible set
	// ranked best-first, each paired with its parsed release + indexer info.
	byName := make(map[string]indexer.Release, len(releases))
	cands := make([]quality.Candidate, 0, len(releases))
	for _, rel := range releases {
		if blocked[normTitle(rel.Title)] {
			continue
		}
		if _, dup := byName[rel.Title]; dup {
			continue
		}
		byName[rel.Title] = rel
		cands = append(cands, quality.NewCandidate(rel.Title, rel.SizeGB(), rel.Seeders))
	}
	decision := c.quality.Decide(ctx, s.QualityProfile, cands)
	eligible := decision.Eligible // sorted best (highest quality) first

	needed := map[epKey]bool{}
	for _, k := range wanted {
		needed[k] = true
	}
	grabbed := map[string]bool{}
	grab := func(name string, label string) {
		rel := byName[name]
		if rel.DownloadURL == "" || grabbed[rel.DownloadURL] {
			return
		}
		if err := c.grabTo(ctx, rel.Indexer, rel.DownloadURL, rel.Title, seriesCategory); err != nil {
			c.log.Warn("series: grab failed", "series", s.Title, "release", rel.Title, "err", err)
			return
		}
		grabbed[rel.DownloadURL] = true
		c.recordSeriesGrab(ctx, s.ID, rel.Title, rel.Indexer, s.QualityProfile)
		c.series.AddEvent(ctx, s.ID, "grabbed", label+": "+rel.Title+" · "+rel.Indexer)
		c.log.Info("series: grabbing", "series", s.Title, "release", rel.Title, "tier", label)
	}

	// A whole-show / multi-season pack only makes sense once the show has actually
	// finished — for a still-running series we stick to single-season packs so each
	// new season is grabbed cleanly as its own release.
	ended := showEnded(s.Status)

	// Pass 1 — a complete-series pack that covers every needed season (ended shows only).
	neededSeasons := seasonsOf(needed)
	if ended {
		for _, ev := range eligible {
			r := ev.Candidate.Release
			if r.Kind() != parser.KindCompleteShow {
				continue
			}
			if coversAllSeasons(r, neededSeasons, seriesSeasons) {
				grab(ev.Candidate.Name, "complete series")
				return
			}
		}
	}

	// Pass 2 — packs, greedily taking the one that covers the most still-needed
	// episodes. Ended shows may take multi-season (or leftover complete-show) packs so
	// a multi-season pack beats separate season packs; running shows are restricted to
	// single-season packs.
	for {
		var best *quality.Evaluation
		var bestCover int
		for i := range eligible {
			if !isPackTier(eligible[i].Candidate.Release.Kind(), ended) {
				continue
			}
			n := len(coveredBy(eligible[i].Candidate.Release, needed))
			if n > bestCover {
				bestCover, best = n, &eligible[i]
			}
		}
		if best == nil || bestCover == 0 {
			break
		}
		grab(best.Candidate.Name, "pack")
		for _, k := range coveredBy(best.Candidate.Release, needed) {
			delete(needed, k)
		}
	}

	// Pass 3 — individual episodes for whatever's left.
	for _, ev := range eligible {
		if len(needed) == 0 {
			break
		}
		r := ev.Candidate.Release
		if r.Kind() != parser.KindEpisode {
			continue
		}
		for _, k := range coveredBy(r, needed) {
			grab(ev.Candidate.Name, "episode")
			delete(needed, k)
		}
	}
}

// wantedEpisodes returns the monitored, aired, file-less episodes plus the set of
// season numbers the series actually has.
func wantedEpisodes(s series.Series) ([]epKey, map[int]bool) {
	var want []epKey
	seriesSeasons := map[int]bool{}
	for _, sn := range s.Seasons {
		if sn.SeasonNumber > 0 {
			seriesSeasons[sn.SeasonNumber] = true
		}
		if !sn.Monitored {
			continue
		}
		for _, e := range sn.Episodes {
			if e.Monitored && !e.HasFile && aired(e.AirDate) {
				want = append(want, epKey{e.SeasonNumber, e.EpisodeNumber})
			}
		}
	}
	return want, seriesSeasons
}

// coveredBy returns which needed episodes a release satisfies.
func coveredBy(r parser.Release, needed map[epKey]bool) []epKey {
	var out []epKey
	for k := range needed {
		switch r.Kind() {
		case parser.KindEpisode:
			if r.Season == k.season {
				for _, e := range r.Episodes {
					if e == k.episode {
						out = append(out, k)
					}
				}
			}
		default: // packs cover whole seasons
			if r.CoversSeason(k.season) {
				out = append(out, k)
			}
		}
	}
	return out
}

func seasonsOf(needed map[epKey]bool) map[int]bool {
	out := map[int]bool{}
	for k := range needed {
		out[k.season] = true
	}
	return out
}

// coversAllSeasons reports whether a complete-series release covers every needed
// season (and is genuinely a full-show pack for this series).
func coversAllSeasons(r parser.Release, needed, seriesSeasons map[int]bool) bool {
	for s := range needed {
		if !r.CoversSeason(s) {
			return false
		}
	}
	return true
}

// ---- import (multi-file) -------------------------------------------------

// ImportSeriesDownloads imports finished TV downloads: for each completed torrent
// in the series category, hardlink every episode file into the library and mark
// the episode. A season pack yields many files from one download.
func (c *Coordinator) ImportSeriesDownloads(ctx context.Context) {
	if c.series == nil || c.imp == nil {
		return
	}
	completed, err := c.downloads.CompletedInCategory(ctx, seriesCategory)
	if err != nil {
		return
	}
	for _, it := range completed {
		if it.ContentPath == "" {
			continue
		}
		s, ok := c.series.MatchByTitle(ctx, series.NormTitle(parser.Parse(it.Name).Title))
		if !ok {
			continue
		}
		videos, err := library.FindVideos(it.ContentPath)
		if err != nil || len(videos) == 0 {
			continue
		}
		imported := 0
		for _, v := range videos {
			ei, ok, err := c.imp.ImportEpisode(s.Title, s.Year, v.Path)
			if err != nil || !ok {
				continue
			}
			if err := c.series.MarkEpisodeImported(ctx, s.ID, ei.Season, ei.Episode, ei.TargetPath, ei.SizeBytes); err == nil {
				imported++
			}
		}
		if imported > 0 {
			c.log.Info("series: imported episodes", "series", s.Title, "count", imported, "release", it.Name)
			c.markSeriesGrabsImported(ctx, s.ID) // flip its grab to imported for seed cleanup
			c.series.AddEvent(ctx, s.ID, "imported", fmt.Sprintf("Imported %d episode%s from %s", imported, plural(imported), it.Name))
			c.bus.Publish("series.imported", map[string]any{"title": s.Title, "id": s.ID, "count": imported})
		}
	}
}

func aired(date string) bool {
	// Episodes with no air date are treated as aired (best effort) so specials/odd
	// data don't block grabs. YYYY-MM-DD compares lexicographically.
	if date == "" {
		return true
	}
	return date <= nowDate()
}

// recordSeriesGrab tracks a series grab for seed cleanup (media_type=series).
func (c *Coordinator) recordSeriesGrab(ctx context.Context, seriesID int64, title, indexer, profile string) {
	seedEnabled, seedRatio, seedHours := c.seedRules(ctx, indexer)
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO grabs (movie_id, version_id, title, indexer, quality_profile, stall_minutes, seed_enabled, seed_ratio, seed_hours, media_type)
		 VALUES (?, 0, ?, ?, ?, 0, ?, ?, ?, 'series')`,
		seriesID, title, indexer, profile, boolToInt(seedEnabled), seedRatio, seedHours)
	if err != nil {
		c.log.Warn("series: record grab failed", "err", err)
	}
}
