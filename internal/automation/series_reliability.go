package automation

import (
	"context"
	"fmt"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// detectStalledSeries fails a stalled series download over to an alternate: past the
// profile's stall timeout with no progress, it blocklists the release, removes the
// torrent, and re-searches (which now skips the blocklisted release). Mirrors the movie
// stall path; called from DetectStalled for media_type='series' grabs.
func (c *Coordinator) detectStalledSeries(ctx context.Context, g grab, queue []download.Item) {
	if c.series == nil || g.StallMinutes <= 0 {
		return
	}
	age := time.Since(parseTime(g.GrabbedAt))
	if age < time.Duration(g.StallMinutes)*time.Minute {
		return
	}
	item, found := findQueued(queue, g.Title)
	stalled := !found ||
		item.State == "error" || item.State == "stalledDL" || item.State == "missingFiles" ||
		(item.Progress < 1.0 && item.DownSpeed == 0)
	if !stalled {
		return
	}
	c.log.Info("series: download stalled, failing over", "series", g.MovieID, "release", g.Title, "age_min", int(age.Minutes()))
	c.addBlockSeries(ctx, g.MovieID, g.Title, g.Indexer, fmt.Sprintf("stalled after %d min", g.StallMinutes))
	if found {
		_ = c.downloads.Remove(ctx, item.Hash, true)
	}
	c.setGrabStatus(ctx, g.ID, "failed")
	_ = c.SearchSeriesNow(ctx, g.MovieID) // re-search; blocklisted release is now skipped
}

// RSSSyncSeries polls indexer RSS feeds for freshly-uploaded releases matching a
// monitored series and grabs anything that fills a wanted episode — the series
// equivalent of RSSSync, catching new episodes of running shows promptly (without
// waiting for the slower missing-sweep). Mirrors the movie RSS path.
func (c *Coordinator) RSSSyncSeries(ctx context.Context) {
	if c.series == nil {
		return
	}
	res, err := c.indexers.Recent(ctx, 100)
	if err != nil {
		c.log.Warn("rss: fetch feeds failed", "err", err)
		return
	}
	if len(res.Releases) == 0 {
		return
	}
	all, err := c.series.List(ctx)
	if err != nil {
		return
	}
	queue, _ := c.downloads.Queue(ctx)
	for _, meta := range all {
		if !meta.Monitored || seriesDownloading(queue, meta.Title) {
			continue
		}
		s, err := c.series.Get(ctx, meta.ID)
		if err != nil {
			continue
		}
		var matched []indexer.Release
		for _, rel := range res.Releases {
			if releaseIsForSeries(rel.Title, s.Title) {
				matched = append(matched, rel)
			}
		}
		if len(matched) == 0 {
			continue
		}
		c.log.Info("rss: series match", "series", s.Title, "candidates", len(matched))
		c.grabSeriesFrom(ctx, s, matched)
	}
}

// UpgradeSeries sweeps every monitored series and grabs a better release for any
// episode that already has a file, when the profile allows upgrades and a clearly
// better episode release exists. Runs on a timer alongside the movie upgrade sweep.
func (c *Coordinator) UpgradeSeries(ctx context.Context) {
	if c.series == nil {
		return
	}
	all, err := c.series.List(ctx)
	if err != nil {
		return
	}
	queue, _ := c.downloads.Queue(ctx)
	for _, meta := range all {
		if !meta.Monitored || seriesDownloading(queue, meta.Title) {
			continue
		}
		if err := c.upgradeSeries(ctx, meta.ID); err != nil {
			c.log.Warn("series: upgrade search failed", "series", meta.Title, "err", err)
		}
	}
}

// upgradeSeries looks for a better release for each monitored episode that already has
// a file. Upgrades are surgical — only individual-episode releases are considered (not
// whole-season packs), so a single better episode doesn't re-download the season.
func (c *Coordinator) upgradeSeries(ctx context.Context, seriesID int64) error {
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return err
	}
	type have struct {
		season, episode int
		release         string // the release it was imported from (NOT the renamed library file)
		sizeGB          float64
		runtimeMin      int // episode length, for the bitrate-based upgrade threshold
	}
	var haveEps []have
	for _, sn := range s.Seasons {
		if sn.SeasonNumber == 0 {
			continue // never upgrade specials
		}
		for _, e := range sn.Episodes {
			if e.Monitored && e.HasFile && e.FilePath != "" {
				// The baseline MUST be the release name, not the library filename. Library
				// files are renamed to a scheme with no group/HDR/audio/codec tags, so they
				// always score near zero — every candidate then looks like an upgrade, and
				// after importing (and renaming back) the same release wins again on the
				// next sweep. That was an unbounded re-download loop. No recorded release
				// (imported before it was tracked) → skip rather than guess.
				if e.SourceRelease == "" {
					continue
				}
				// e.Runtime (episode minutes) drives the bitrate threshold; 0 (unknown)
				// falls back to quality-only upgrades inside UpgradeCandidate.
				haveEps = append(haveEps, have{e.SeasonNumber, e.EpisodeNumber, e.SourceRelease, gbOf(e.SizeBytes), e.Runtime})
			}
		}
	}
	if len(haveEps) == 0 {
		return nil
	}

	res, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: indexerQuery(s.Title), MediaType: indexer.MediaSeries, Limit: 100})
	if err != nil || len(res.Releases) == 0 {
		return err
	}
	blocked := c.blockedSetSeries(ctx, s.ID)
	byName := make(map[string]indexer.Release, len(res.Releases))
	for _, rel := range res.Releases {
		if blocked[normTitle(rel.Title)] {
			continue
		}
		byName[rel.Title] = rel
	}

	grabbed := map[string]bool{}
	for _, ep := range haveEps {
		var cands []quality.Candidate
		for name, rel := range byName {
			if episodeRelease(parser.Parse(name), ep.season, ep.episode) {
				cands = append(cands, quality.NewCandidate(name, rel.SizeGB(), rel.Seeders))
			}
		}
		if len(cands) == 0 {
			continue
		}
		pick, ok := c.quality.UpgradeCandidate(ctx, s.QualityProfile, ep.release, ep.sizeGB, ep.runtimeMin, cands)
		if !ok {
			continue
		}
		winner := byName[pick.Name]
		if grabbed[winner.DownloadURL] {
			continue
		}
		c.log.Info("series: upgrading episode", "series", s.Title, "s", ep.season, "e", ep.episode, "to", winner.Title)
		if err := c.GrabForSeries(ctx, s.ID, winner.Indexer, winner.DownloadURL, winner.Title); err != nil {
			c.log.Warn("series: upgrade grab failed", "series", s.Title, "err", err)
			continue
		}
		grabbed[winner.DownloadURL] = true
	}
	return nil
}

// seriesDownloading reports whether the queue already has a TV torrent for this series
// (so upgrade/RSS sweeps don't stack a second grab on top of an in-flight one).
func seriesDownloading(queue []download.Item, seriesTitle string) bool {
	for _, it := range queue {
		if it.Category == seriesCategory && titleKey(parser.Parse(it.Name).Title) == titleKey(seriesTitle) {
			return true
		}
	}
	return false
}

// releaseIsForSeries reports whether a release title belongs to the given series
// (normalized title match).
func releaseIsForSeries(relTitle, seriesTitle string) bool {
	return titleKey(parser.Parse(relTitle).Title) == titleKey(seriesTitle)
}

// seriesTitleMatches is releaseIsForSeries that also accepts an anime series' romaji
// (original) title, since anime is frequently released under its romaji name.
func seriesTitleMatches(relTitle string, s series.Series) bool {
	if releaseIsForSeries(relTitle, s.Title) {
		return true
	}
	if s.IsAnime() && s.Extra != nil && s.Extra.OriginalTitle != "" {
		return releaseIsForSeries(relTitle, s.Extra.OriginalTitle)
	}
	return false
}

// episodeRelease reports whether a parsed release is a single-episode release for the
// exact (season, episode).
func episodeRelease(p parser.Release, season, episode int) bool {
	if p.Kind() != parser.KindEpisode || p.Season != season {
		return false
	}
	for _, e := range p.Episodes {
		if e == episode {
			return true
		}
	}
	return false
}
