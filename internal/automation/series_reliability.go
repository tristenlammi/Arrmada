package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// RSSSyncSeries polls indexer RSS feeds for freshly-uploaded releases matching a
// monitored series and grabs anything that fills a wanted episode — the series
// equivalent of RSSSync, catching new episodes of running shows promptly (without
// waiting for the slower missing-sweep). Mirrors the movie RSS path.
func (c *Coordinator) RSSSyncSeries(ctx context.Context) {
	if c.series == nil {
		return
	}
	res, err := c.indexers.Recent(ctx, 100)
	if errors.Is(err, indexer.ErrNoIndexers) {
		return // no indexer has a feed — nothing to sync, and nothing to warn about every cycle
	}
	if err != nil {
		c.log.Warn("rss: fetch feeds failed", "err", err)
		return
	}
	res.Releases = withoutBookUploads(res.Releases) // the feed is shared with the book sweep
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
		s, err := c.series.Get(ctx, meta.ID)
		if err != nil {
			continue
		}
		inSeasons, whole, busy := seriesInFlightScope(queue, s)
		if whole {
			c.log.Info("rss: skipping series — a pack covering the whole show is still downloading", "series", s.Title, "release", busy[0])
			continue
		}
		// A pack still downloading for one season holds back only that season: the rest
		// of the show is fair game, so a new season's episodes aren't stuck behind a slow
		// pack for an old one.
		only, ok := c.notInFlight(s, inSeasons, busy, "rss")
		if !ok {
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
		sctx, notes := newSearchNotes(WithDefaultSearchTrigger(ctx, TriggerRSS))
		notes.consider(matched)
		// Recorded only when it grabs, like the movie feed: the same uploads come round
		// every cycle for hours.
		if n, _ := c.grabSeriesLimited(sctx, s, matched, only); n > 0 {
			out := SearchOutcome{Searched: true}
			c.recordAttempt(sctx, notes, AttemptSeries, s.ID, "", &out, nil)
		}
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
	var outage outageTally
	defer outage.report(c.log, "series upgrade sweep")
	budget := c.newSweepBudget(ctx)
	titlesLeft := 0
	defer func() { c.logBudget("series", budget, titlesLeft) }()
	for i, meta := range all {
		if budget.spent() {
			// Stop before the next indexer search: nothing found now could be grabbed.
			titlesLeft = len(all) - i
			break
		}
		if !meta.Monitored {
			continue
		}
		if busy := seriesInFlight(queue, meta.Title); busy != "" {
			c.log.Info("series: skipping upgrade sweep — a grab is still downloading", "series", meta.Title, "release", busy)
			continue
		}
		err := c.upgradeSeries(ctx, meta.ID, budget)
		if outage.note(err) {
			if outage.stop() {
				break
			}
			continue
		}
		if err != nil {
			c.log.Warn("series: upgrade search failed", "series", meta.Title, "err", err)
		}
	}
}

// upgradeSeries looks for a better release for each monitored episode that already has
// a file. Upgrades are surgical — only individual-episode releases are considered (not
// whole-season packs), so a single better episode doesn't re-download the season. b is
// the sweep's upgrade budget (nil = unlimited); each episode grabbed counts as one.
func (c *Coordinator) upgradeSeries(ctx context.Context, seriesID int64, b *upgradeBudget) (err error) {
	// Recorded under scope "upgrade", so the Wanted view's summaries can leave it out.
	ctx, notes := newSearchNotes(WithDefaultSearchTrigger(ctx, TriggerUpgrade))
	defer func() {
		var out SearchOutcome
		c.recordAttempt(ctx, notes, AttemptSeries, seriesID, ScopeUpgrade, &out, err)
	}()
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
		// cur is the file as the decisions judge it: the release it was imported from
		// (NOT the renamed library file), its size, the episode length for the bitrate
		// threshold, and Convert's probed facts when they still describe it.
		cur quality.CurrentFile
	}
	var haveEps []have
	atCeiling, held := 0, 0
	for _, sn := range s.Seasons {
		if sn.SeasonNumber == 0 {
			continue // never upgrade specials
		}
		for _, e := range sn.Episodes {
			if e.Monitored && e.HasFile && e.FilePath != "" {
				// Kept as it is when the profile changed ("keep existing files").
				if e.UpgradeHold {
					held++
					continue
				}
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
				cur := c.currentEpisodeFile(ctx, e)
				// Out of headroom: the file meets the profile's target, or it's at the best
				// resolution the profile allows and far enough up the bitrate ceiling that
				// the next percentage step lands above it. Nothing the profile would accept
				// can win, so searching only produces work whose one possible outcome is
				// "rejected".
				if c.quality.AtCeiling(ctx, profile, cur) {
					atCeiling++
					continue
				}
				haveEps = append(haveEps, have{e.SeasonNumber, e.EpisodeNumber, cur})
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
		if held > 0 {
			c.log.Info("series: nothing to upgrade — episodes are kept as they are (upgrades paused)",
				"series", s.Title, "held", held)
		}
		return nil
	}
	if atCeiling > 0 {
		c.log.Info("series: skipping episodes already at the profile ceiling",
			"series", s.Title, "at_ceiling", atCeiling, "searching", len(haveEps))
	}

	res, err := c.search(ctx, indexer.SearchQuery{Text: indexerQuery(s.Title), MediaType: indexer.MediaSeries, Limit: 100})
	if err != nil || len(res.Releases) == 0 {
		return err
	}
	notes.consider(res.Releases)
	blocked, err := c.blockedSetSeries(ctx, s.ID)
	if err != nil {
		c.skipUnreadable(s.Title, err)
		return err
	}
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
			notes.mark(rel.Title, DropWrongTitle)
			continue
		}
		if blocked[normTitle(rel.Title)] {
			notes.mark(rel.Title, DropBlocklisted)
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
	pending, err := c.pendingSeriesGrabTitles(ctx, s.ID)
	if err != nil {
		c.skipUnreadable(s.Title, err)
		return err
	}
	rts := newRuntimeIndex(s)
	var picks []episodeUpgrade
	for _, ep := range haveEps {
		cands := c.episodeUpgradeCandidates(ctx, s, rts, byName, ep.season, ep.episode)
		if len(cands) == 0 {
			continue
		}
		if pick, ok := c.quality.UpgradeCandidate(ctx, profile, ep.cur, cands); ok {
			picks = append(picks, episodeUpgrade{season: ep.season, episode: ep.episode, pick: pick})
		}
	}
	// Each episode is one grab against the sweep's budget; the rest wait for the next sweep.
	takeUpgrades(picks, b, func(u episodeUpgrade) bool {
		winner := byName[u.pick.Name]
		if grabbed[winner.DownloadURL] {
			return false
		}
		if pending[normTitle(winner.Title)] {
			return false // this exact upgrade is already in flight
		}
		if !c.diskOKFor(grabbedGB + u.pick.SizeGB) {
			c.log.Warn("series: low disk, skipping upgrade", "series", s.Title, "need_gb", u.pick.SizeGB)
			return false
		}
		c.log.Info("series: upgrading episode", "series", s.Title, "s", u.season, "e", u.episode, "to", winner.Title)
		if err := c.GrabForSeriesAuto(ctx, s.ID, winner.Indexer, winner.DownloadURL, winner.Title); err != nil {
			c.log.Warn("series: upgrade grab failed", "series", s.Title, "err", err)
			return false
		}
		grabbed[winner.DownloadURL] = true
		grabbedGB += u.pick.SizeGB
		return true
	})
	return nil
}

// episodeUpgrade is one episode's chosen upgrade, before it's grabbed.
type episodeUpgrade struct {
	season, episode int
	pick            quality.Candidate
}

// seriesDownloading reports whether the queue already has a TV torrent for this series
// (so upgrade/RSS sweeps don't stack a second grab on top of an in-flight one).
func seriesDownloading(queue []download.Item, seriesTitle string) bool {
	return seriesInFlight(queue, seriesTitle) != ""
}

// SeriesInFlight says which of a show's seasons have a torrent still being fetched (see
// seriesInFlightScope), for the views that explain why a season isn't being searched.
func SeriesInFlight(queue []download.Item, s series.Series) (seasons map[int]bool, whole bool, names []string) {
	return seriesInFlightScope(queue, s)
}

// seriesInFlightScope reports what a show's still-downloading torrents cover: the seasons
// they hold, or whole=true when one covers more than a season can say (a multi-season or
// complete-series pack, an anime absolute-numbered release, or a name with no numbering at
// all). names lists those torrents, for the log line that says why a season was skipped.
//
// The sweeps used to skip the whole show while ANY of its torrents was incomplete, so one
// dead S03 pack kept S04's new episodes from ever being searched. Now only what a torrent
// actually covers is held back. Duplicates inside a season are still stopped by the
// pending-grab guard (pendingSeriesGrabTitles).
//
// Matching goes through seriesTitleMatches, so a pack named under the romaji title or a
// user alias counts. A torrent in an error state doesn't: stall fail-over deals with it,
// and holding the season for a download that will never finish is the bug this replaces.
func seriesInFlightScope(queue []download.Item, s series.Series) (seasons map[int]bool, whole bool, names []string) {
	seasons = map[int]bool{}
	for _, it := range queue {
		if it.Complete() || it.Category != seriesCategory {
			continue // finished (importing or seeding), or not a TV grab
		}
		if it.Phase() == "error" || it.State == "error" {
			continue
		}
		if !seriesTitleMatches(it.Name, s) {
			continue
		}
		names = append(names, it.Name)
		if sn, ok := aliasSeasonOf(it.Name, s); ok {
			// The alias' numbers are read inside one season of the series, whatever
			// season the release itself claims.
			seasons[sn] = true
			continue
		}
		p := parser.Parse(it.Name)
		switch {
		case p.Complete || len(p.Seasons) > 1:
			whole = true
		case p.Season > 0 && s.IsAnime() && len(s.Seasons) > 0 && !hasSeason(s, p.Season):
			// An anime cour numbered as its own season ("Frieren S02") that the series'
			// listing doesn't have: its episodes live in some other season.
			whole = true
		case p.Season > 0:
			seasons[p.Season] = true
		case p.SeasonExplicit:
			seasons[0] = true // an explicit S00 special
		default:
			// Absolute-numbered ("[Group] Show - 137") or unnumbered: which season it
			// lands in takes the series' numbering to work out, so hold the whole show
			// rather than risk stacking a second copy.
			whole = true
		}
	}
	return seasons, whole, names
}

func hasSeason(s series.Series, season int) bool {
	for _, sn := range s.Seasons {
		if sn.SeasonNumber == season {
			return true
		}
	}
	return false
}

// aliasSeasonOf is the series season a release lands in when it matched only through an
// alias pinned to one season (Alias.TMDBSeason > 0); ok is false otherwise.
func aliasSeasonOf(name string, s series.Series) (int, bool) {
	if releaseIsForSeries(name, s.Title) {
		return 0, false
	}
	if s.IsAnime() && s.Extra != nil && s.Extra.OriginalTitle != "" && releaseIsForSeries(name, s.Extra.OriginalTitle) {
		return 0, false
	}
	title := parser.Parse(name).Title
	for _, a := range s.Aliases {
		if a.TMDBSeason > 0 && parser.TitleHasPrefix(title, a.Title) {
			return a.TMDBSeason, true
		}
	}
	return 0, false
}

// notInFlight is the wanted episodes of s outside the seasons still downloading, for a
// sweep to search instead of the whole show. With nothing in flight it returns nil, which
// grabSeriesLimited reads as "everything missing". ok is false when every wanted episode
// is in a season still downloading — nothing to search. sweep names the caller in the
// log line, which is said once per show per sweep.
func (c *Coordinator) notInFlight(s series.Series, inSeasons map[int]bool, busy []string, sweep string) (only []epKey, ok bool) {
	if len(inSeasons) == 0 {
		return nil, true
	}
	wanted, _ := wantedEpisodes(s)
	only = []epKey{}
	for _, k := range wanted {
		if !inSeasons[k.season] {
			only = append(only, k)
		}
	}
	held := sortedSeasons(inSeasons)
	if len(only) == 0 {
		if len(wanted) > 0 {
			c.log.Info(sweep+": skipping series — what's missing is in seasons still downloading",
				"series", s.Title, "seasons", seasonList(held), "release", busy[0])
		}
		return nil, false
	}
	c.log.Info(sweep+": searching "+seasonList(sortedSeasons(seasonsOf(setOf(only))))+" only — "+
		seasonList(held)+" still downloading", "series", s.Title, "release", busy[0])
	return only, true
}

// seasonList renders season numbers for a log line: "S03", "S03, S04".
func seasonList(seasons []int) string {
	parts := make([]string, len(seasons))
	for i, sn := range seasons {
		parts[i] = fmt.Sprintf("S%02d", sn)
	}
	return strings.Join(parts, ", ")
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
//
// It is the show-level form of seriesInFlightScope, kept for the upgrade sweep, which
// still holds the whole show while anything for it downloads (no stacking upgrades).
func seriesInFlight(queue []download.Item, seriesTitle string) string {
	if _, _, names := seriesInFlightScope(queue, series.Series{Title: seriesTitle}); len(names) > 0 {
		return names[0]
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
		// Whole-word prefix, not equality: groups suffix an arc's name with a per-cour
		// subtitle ("... The Calamity") or leave junk the parser didn't strip. The word
		// boundary keeps "Bleach" from matching "Bleachers"; the series' own title is
		// still compared for equality, so "Below Deck" can't swallow "Below Deck
		// Mediterranean" — only titles the user declared get this treatment.
		if parser.TitleHasPrefix(parser.Parse(relTitle).Title, a.Title) {
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
