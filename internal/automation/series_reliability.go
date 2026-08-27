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
	window := time.Duration(g.StallMinutes) * time.Minute
	age := time.Since(parseTime(g.GrabbedAt))
	if age < window {
		return
	}
	item, found := findQueued(queue, g)
	if !c.stalledInQueue(g, item, found, window) {
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
		if !meta.Monitored {
			continue
		}
		if busy := seriesInFlight(queue, meta.Title); busy != "" {
			c.log.Info("rss: skipping series — a grab is still downloading", "series", meta.Title, "release", busy)
			continue
		}
		s, err := c.series.Get(ctx, meta.ID)
		if err != nil {
			continue
		}
		var matched []indexer.Release
		for _, rel := range res.Releases {
			// seriesTitleMatches, not releaseIsForSeries: anime is mostly uploaded under
			// its romaji title, and matching English-only made the RSS fast path — the
			// mechanism that catches new episodes promptly — dead for those shows.
			if seriesTitleMatches(rel.Title, s) {
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
	queue, err := c.downloads.Queue(ctx)
	if err != nil {
		// Without the queue, "already downloading" can't be checked — don't risk
		// stacking a second copy of a big release; upgrades can wait for the next sweep.
		c.log.Warn("series: upgrade sweep skipped — can't read the download queue", "err", err)
		return
	}
	for _, meta := range all {
		if !meta.Monitored {
			continue
		}
		if busy := seriesInFlight(queue, meta.Title); busy != "" {
			c.log.Info("series: skipping upgrade sweep — a grab is still downloading", "series", meta.Title, "release", busy)
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
	// Resolved once and used for the gate, the ceiling check and the upgrade decision
	// alike. A gate reading one profile while the decider reads another is how you get a
	// sweep that searches and then rejects everything it finds.
	profile := c.effectiveProfile(ctx, s.QualityProfile, "series")
	// Upgrades off means there is nothing to find, so don't ask an indexer. The movie
	// sweep has always checked this before searching; the series sweep hit the indexer
	// every 6 hours for every monitored show regardless, then threw the results away
	// inside UpgradeCandidate.
	if !c.quality.AllowsUpgrades(ctx, profile) {
		return nil
	}
	type have struct {
		season, episode int
		release         string // the release it was imported from (NOT the renamed library file)
		sizeGB          float64
		runtimeMin      int // episode length, for the bitrate-based upgrade threshold
	}
	var haveEps []have
	atCeiling := 0
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
				// Out of headroom: at the best resolution the profile allows, and far
				// enough up the bitrate ceiling that the next percentage step lands above
				// it. Nothing the profile would accept can win, so searching only produces
				// work whose one possible outcome is "rejected".
				if c.quality.AtCeiling(ctx, profile, e.SourceRelease, gbOf(e.SizeBytes), e.Runtime) {
					atCeiling++
					continue
				}
				// e.Runtime (episode minutes) drives the bitrate threshold; 0 (unknown)
				// falls back to quality-only upgrades inside UpgradeCandidate.
				haveEps = append(haveEps, have{e.SeasonNumber, e.EpisodeNumber, e.SourceRelease, gbOf(e.SizeBytes), e.Runtime})
			}
		}
	}
	if len(haveEps) == 0 {
		// Every episode is either at the ceiling or has no recorded release. Saying so
		// beats a silent no-op, since "why did my upgrade sweep stop" is otherwise
		// indistinguishable from a broken indexer.
		if atCeiling > 0 {
			c.log.Info("series: nothing to upgrade — every episode already meets the profile",
				"series", s.Title, "profile", profile, "episodes", atCeiling)
		}
		return nil
	}
	if atCeiling > 0 {
		c.log.Info("series: skipping episodes already at the profile ceiling",
			"series", s.Title, "at_ceiling", atCeiling, "searching", len(haveEps))
	}

	res, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: indexerQuery(s.Title), MediaType: indexer.MediaSeries, Limit: 100})
	if err != nil || len(res.Releases) == 0 {
		return err
	}
	blocked := c.blockedSetSeries(ctx, s.ID)
	byName := make(map[string]indexer.Release, len(res.Releases))
	droppedTitle := 0
	for _, rel := range bestByTitle(grabbable(res.Releases)) {
		// The candidate must actually BE this show. Nothing here checked, and matching on
		// season/episode numbers alone is not a check: a search for "Goliath" returns
		// "House of David S01E07 David and Goliath - Part 1" — the indexer matched the
		// word in an EPISODE title — and S01E07 lines up with Goliath's own S01E07, so it
		// was grabbed as an upgrade and imported over the real episode. The missing-episode
		// and interactive searches have always gated on this; the upgrade sweep didn't.
		if !seriesTitleMatches(rel.Title, s) {
			droppedTitle++
			continue
		}
		if blocked[normTitle(rel.Title)] {
			continue
		}
		byName[rel.Title] = rel
	}
	if droppedTitle > 0 {
		c.log.Info("series: upgrade search filtered", "series", s.Title,
			"kept", len(byName), "dropped_wrong_title", droppedTitle)
	}

	grabbed := map[string]bool{}
	grabbedGB := 0.0
	pending := c.pendingSeriesGrabTitles(ctx, s.ID)
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
		pick, ok := c.quality.UpgradeCandidate(ctx, profile, ep.release, ep.sizeGB, ep.runtimeMin, cands)
		if !ok {
			continue
		}
		winner := byName[pick.Name]
		if grabbed[winner.DownloadURL] {
			continue
		}
		if pending[normTitle(winner.Title)] {
			continue // this exact upgrade is already in flight
		}
		if !c.diskOKFor(grabbedGB + pick.SizeGB) {
			c.log.Warn("series: low disk, skipping upgrade", "series", s.Title, "need_gb", pick.SizeGB)
			continue
		}
		c.log.Info("series: upgrading episode", "series", s.Title, "s", ep.season, "e", ep.episode, "to", winner.Title)
		if err := c.GrabForSeriesAuto(ctx, s.ID, winner.Indexer, winner.DownloadURL, winner.Title); err != nil {
			c.log.Warn("series: upgrade grab failed", "series", s.Title, "err", err)
			continue
		}
		grabbed[winner.DownloadURL] = true
		grabbedGB += pick.SizeGB
	}
	return nil
}

// seriesDownloading reports whether the queue already has a TV torrent for this series
// (so upgrade/RSS sweeps don't stack a second grab on top of an in-flight one).
func seriesDownloading(queue []download.Item, seriesTitle string) bool {
	return seriesInFlight(queue, seriesTitle) != ""
}

// seriesInFlight returns the name of a torrent still being FETCHED for this series, or ""
// when nothing is. It's what stops a sweep stacking a second copy on top of an in-progress
// grab — and the reason it names the release is that every caller skips the series in
// silence, which made this impossible to diagnose from a log.
//
// Progress < 1 is the whole test, and it matters: the queue is the download client's full
// torrent list, seeding torrents included. Treating those as "downloading" froze a show out
// of the missing sweep, RSS sync AND the upgrade sweep for the entire seeding period — 22
// hours in the case that surfaced this — so a mid-season episode that aired inside that
// window was never picked up at all. Once the bytes are on disk there's nothing left to
// stack: seeding is bookkeeping, not a download.
func seriesInFlight(queue []download.Item, seriesTitle string) string {
	for _, it := range queue {
		if it.Complete() {
			continue // finished — importing or seeding, either way not in flight
		}
		if it.Category == seriesCategory && titleKey(parser.Parse(it.Name).Title) == titleKey(seriesTitle) {
			return it.Name
		}
	}
	return ""
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
	if s.IsAnime() && s.Extra != nil && s.Extra.OriginalTitle != "" && releaseIsForSeries(relTitle, s.Extra.OriginalTitle) {
		return true
	}
	// User-declared alternate titles. Anime arcs are routinely released as if they were
	// a separate show ("BLEACH Thousand-Year Blood War" for Bleach), which no amount of
	// normalizing the real title will ever match. Purely additive: a series with no
	// aliases behaves exactly as it did before.
	for _, a := range s.Aliases {
		if releaseIsForSeries(relTitle, a.Title) {
			return true
		}
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
