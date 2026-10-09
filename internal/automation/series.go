package automation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// executableExts are file types that never belong in a media download; their presence
// (a ".scr" screensaver named like an episode) marks a fake or malicious release.
var executableExts = map[string]bool{
	".scr": true, ".exe": true, ".bat": true, ".cmd": true, ".com": true, ".msi": true,
	".vbs": true, ".js": true, ".ps1": true, ".jar": true, ".apk": true, ".lnk": true,
}

// hasExecutable reports whether the download at path contains an executable file.
func hasExecutable(path string) bool {
	found := false
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if executableExts[strings.ToLower(filepath.Ext(p))] {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// removeIfNoVideo deletes a completed download that holds no video at all — a fake
// (the ".scr" screensaver dressed up as an episode), an empty folder, or an archive set
// that won't unpack. There is nothing to salvage, and left alone it occupies the
// downloads dir forever since it's blocklisted and will never import.
//
// A download that DOES contain video is deliberately kept: we may simply have failed to
// work out which episode it is, and the user can still import it by hand.
func (c *Coordinator) removeIfNoVideo(ctx context.Context, hash, name, contentPath string) {
	if hash == "" || contentPath == "" || c.downloads == nil {
		return
	}
	if vids, err := library.FindVideos(contentPath); err != nil || len(vids) > 0 {
		return // has video (or we can't tell) — leave it for manual import
	}
	// FindVideos floors at ~50MB to skip samples — but before DELETING data, check
	// again without the floor: legitimate small episodes (shorts, mini-episodes)
	// were being destroyed as "no video" when they merely failed the size floor.
	if hasAnyVideoFile(contentPath) {
		c.log.Info("import: download has only small video files — keeping it for manual import", "release", name)
		return
	}
	if err := c.downloads.Remove(ctx, hash, true); err != nil {
		c.log.Warn("import: could not remove junk download", "release", name, "err", err)
		return
	}
	c.log.Info("import: removed a download with no video in it (blocklisted, nothing to import)", "release", name)
}

// hasAnyVideoFile reports whether ANY video-extension file exists under path,
// with no size floor — the last check before deleting a download's data.
func hasAnyVideoFile(path string) bool {
	found := false
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || found || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		for _, e := range videoExts {
			if ext == e {
				found = true
				break
			}
		}
		return nil
	})
	return found
}

// grabIndexer returns the indexer a release was grabbed from ("" if untracked), for
// recording on a blocklist entry.
func (c *Coordinator) grabIndexer(ctx context.Context, name, mediaType string) string {
	if _, ix, ok := c.grabbedMediaFor(ctx, name, mediaType); ok {
		return ix
	}
	return ""
}

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
	// One queue read for the whole sweep: a series with a download already in flight is
	// skipped, so we don't re-grab the same winner every tick while a pack downloads.
	// (RSSSyncSeries and UpgradeSeries already do this; the missing sweep didn't.)
	queue, qerr := c.downloads.Queue(ctx)
	if qerr != nil {
		queue = nil
	}
	var outage outageTally
	defer outage.report(c.log, "series search sweep")
	for _, s := range all {
		if !s.Monitored {
			continue
		}
		// A torrent still downloading for the show no longer skips it here: one for a
		// single season holds back just that season (searchSeriesOnceScoped).
		// Cheap local check before spending an indexer search: a series with nothing
		// grabbable shouldn't cost N queries every sweep, forever.
		if !c.series.HasWantedEpisodes(ctx, s.ID) {
			continue
		}
		// Exponential backoff for a series that keeps finding nothing — an episode no
		// indexer carries used to cost a full multi-indexer search 4×/hour forever.
		lastAt, misses := c.series.SearchState(ctx, s.ID)
		if wait := searchBackoff(misses); wait > 0 {
			if last := parseTime(lastAt); !last.IsZero() && time.Since(last) < wait {
				continue
			}
		}
		out, err := c.searchSeriesOnceScoped(ctx, s.ID, queue)
		n := out.Grabbed
		if errors.Is(err, ErrAlreadySearching) {
			c.log.Debug("series: skipping a show that is already being searched", "series", s.Title)
			continue
		}
		if outage.note(err) {
			if outage.stop() {
				break // the indexers are down: the rest would only fail the same way
			}
			continue
		}
		if err != nil {
			c.log.Warn("series: search failed", "series", s.Title, "err", err)
		}
		reset, miss := sweepOutcome(err, out.Searched, n)
		if reset {
			c.series.ResetSearchMisses(ctx, s.ID)
		}
		if miss {
			c.series.RecordSearchMiss(ctx, s.ID)
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
//
// The outcome says what it found, for the Search button.
func (c *Coordinator) SearchSeriesNow(ctx context.Context, seriesID int64) (SearchOutcome, error) {
	return c.searchSeriesOnce(ctx, seriesID)
}

// searchSeriesOnce is SearchSeriesNow; the missing-sweep reads the outcome's grab count to
// back off a series that keeps coming up empty.
func (c *Coordinator) searchSeriesOnce(ctx context.Context, seriesID int64) (SearchOutcome, error) {
	return c.searchSeriesOnceScoped(ctx, seriesID, nil)
}

// searchSeriesOnceScoped is searchSeriesOnce that leaves out what queue still has
// downloading for the show (seriesInFlightScope). A pack for one season holds back just
// that season: its per-season query isn't sent and its episodes aren't grabbed. A pack
// covering the whole show holds back the whole show, as does one for every season that's
// missing something; then nothing is searched and the outcome says so without counting a
// miss. A nil queue (a manual search, or a queue that couldn't be read) holds nothing back.
//
// Said out loud in the log: a show held back used to be skipped in total silence, so one
// frozen out of the sweep looked identical to one with nothing to find.
func (c *Coordinator) searchSeriesOnceScoped(ctx context.Context, seriesID int64, queue []download.Item) (SearchOutcome, error) {
	if c.series == nil {
		return SearchOutcome{Reason: ReasonNothingWanted}, nil
	}
	release, ok := c.claims.claim(seriesKey(seriesID))
	if !ok {
		return SearchOutcome{Reason: ReasonAlreadySearching}, ErrAlreadySearching
	}
	defer release()
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return SearchOutcome{}, err
	}
	// The grab step refuses to grab with an unreadable blocklist or pending-grab list.
	// Checked up front as well, so that case is an error for the sweep (no backoff miss,
	// and Search now says so) and costs no indexer queries that couldn't be acted on.
	if _, err := c.blockedSetSeries(ctx, s.ID); err != nil {
		c.skipUnreadable(s.Title, err)
		return SearchOutcome{}, err
	}
	if _, err := c.pendingSeriesGrabTitles(ctx, s.ID); err != nil {
		c.skipUnreadable(s.Title, err)
		return SearchOutcome{}, err
	}
	inSeasons, whole, busy := seriesInFlightScope(queue, s)
	if whole {
		c.log.Info("series: skipping sweep — a pack covering the whole show is still downloading", "series", s.Title, "release", busy[0])
		return SearchOutcome{Reason: ReasonNothingWanted}, nil
	}
	only, ok := c.notInFlight(s, inSeasons, busy, "series")
	if !ok {
		return SearchOutcome{Reason: ReasonNothingWanted}, nil
	}
	out := SearchOutcome{Searched: true}
	releases, err := c.searchSeriesReleasesExcept(ctx, s, inSeasons)
	if err != nil {
		return out, err
	}
	// The series search already narrows releases to this show, so what came back is
	// what matched; which of them were usable isn't separated out on this path.
	out.Returned, out.Matching = len(releases), len(releases)
	grabbed, remaining := 0, []epKey(nil)
	if len(releases) > 0 {
		grabbed, remaining = c.grabSeriesLimited(ctx, s, releases, only)
	} else if only != nil {
		remaining = sortedKeys(setOf(only))
	} else if wanted, _ := wantedEpisodes(s); len(wanted) > 0 {
		remaining = sortedKeys(setOf(wanted))
	}
	grabbed += c.searchByAbsolute(ctx, s, remaining)
	out.Grabbed = grabbed
	out.Usable = out.Matching
	out.settle()
	return out, nil
}

// maxAbsoluteQueries caps the follow-up searches per series per sweep. A title search
// returns a fixed page (100), which for a long-running anime is dominated by recent
// uploads — episode 137 simply isn't in it. Querying the absolute number finds it, but
// one query per missing episode would undo the throttle and backoff work, so it's
// bounded; successful grabs reset the backoff and the next sweep continues.
const maxAbsoluteQueries = 3

// searchByAbsolute is the anime follow-up: for episodes the broad title search didn't
// cover, query the absolute episode number the way fansubs actually name releases
// ("Dan Da Dan 13" → "[Group] Dan Da Dan - 13 [1080p]").
//
// Anime-only on purpose: standard series are named SxxExx and are already found by the
// title search, and a bare number would just add noise.
func (c *Coordinator) searchByAbsolute(ctx context.Context, s series.Series, remaining []epKey) int {
	if !s.IsAnime() || len(remaining) == 0 {
		return 0
	}
	// Same starvation as the season fan-out: `remaining` arrives sorted ascending, so three
	// queries a sweep meant the same three oldest gaps were re-asked forever and a later
	// season's episodes were never reached. Resume where the last sweep stopped instead.
	// The cursor packs (season, episode) into one integer; the ordering wraps, so a gap
	// that can't be found doesn't block the ones behind it.
	ordered := rotateKeys(remaining, c.absoluteCursor(ctx, s.ID))
	grabbed, queries := 0, 0
	still := remaining // narrows as earlier queries cover episodes
	last := -1
	for i, k := range ordered {
		if queries >= maxAbsoluteQueries {
			break
		}
		last = i
		if !containsKey(still, k) {
			continue // an earlier absolute query already covered this one
		}
		// The series' own absolute number, and the arc's number for any alias covering
		// this episode. An arc released as its own show is numbered from 1 within that
		// arc, not from 1 across the series, so the series-absolute query can't reach it.
		terms := c.series.AliasSearchTerms(ctx, s.ID, k.season, k.episode)
		if abs := c.series.AbsoluteNumber(ctx, s.ID, k.season, k.episode); abs > 0 {
			terms = append(terms, fmt.Sprintf("%s %d", s.Title, abs))
		}
		if len(terms) == 0 {
			continue // nothing to query by
		}
		queries++
		for _, q := range terms {
			res, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: q, MediaType: indexer.MediaSeries, Limit: 100})
			if err != nil || len(res.Releases) == 0 {
				continue
			}
			n, left := c.grabSeriesLimited(ctx, s, res.Releases, still)
			still = left
			if n > 0 {
				c.log.Info("series: grabbed via targeted episode search", "series", s.Title, "query", q, "count", n)
			}
			grabbed += n
			if !containsKey(still, k) {
				break // this episode is covered — don't spend the other terms on it
			}
		}
	}
	if last >= 0 && len(ordered) > 0 {
		c.series.SetAbsoluteCursor(ctx, s.ID, epCursor(ordered[(last+1)%len(ordered)]))
	}
	return grabbed
}

// epCursor packs an episode key into one sortable integer, so a resume point fits in a
// single column. 10 000 episodes a season is far past anything real.
func epCursor(k epKey) int { return k.season*10000 + k.episode }

func (c *Coordinator) absoluteCursor(ctx context.Context, seriesID int64) int {
	_, abs := c.series.SearchCursors(ctx, seriesID)
	return abs
}

// rotateKeys returns keys reordered to start at the first one at or past cursor, wrapping
// around. Keys are matched by VALUE, not position — which episodes remain changes between
// sweeps, so a stored index would drift.
func rotateKeys(keys []epKey, cursor int) []epKey {
	if len(keys) == 0 || cursor <= 0 {
		return keys
	}
	start := 0
	for i, k := range keys {
		if epCursor(k) >= cursor {
			start = i
			break
		}
	}
	return append(append([]epKey{}, keys[start:]...), keys[:start]...)
}

func containsKey(keys []epKey, k epKey) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

// setOf turns a wanted-episode slice into the map form grabSeriesFrom uses.
func setOf(keys []epKey) map[epKey]bool {
	m := make(map[epKey]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// searchBackoff is how long to wait before sweeping a series again after n consecutive
// sweeps that grabbed nothing: 30m, 1h, 2h, 4h, 8h, capped at 12h. Applies to the
// automatic sweep only — a user-triggered search and RSS sync both ignore it, so a
// newly-aired episode is still picked up promptly.
func searchBackoff(misses int) time.Duration {
	if misses <= 0 {
		return 0
	}
	d := 30 * time.Minute
	for i := 1; i < misses; i++ {
		d *= 2
		if d >= 12*time.Hour {
			return 12 * time.Hour
		}
	}
	return d
}

// grabSeriesFrom applies the season-pack-preference grab logic over a set of releases
// (from an on-demand search, the RSS feed, or a stall re-search) for a series' monitored,
// aired, missing episodes. Blocklisted releases are skipped so a stall re-search doesn't
// re-grab what just failed.
// Returns how many releases it grabbed (named, so the existing bare returns keep
// working) — the sweep uses it to decide whether to back this series off.
func (c *Coordinator) grabSeriesFrom(ctx context.Context, s series.Series, releases []indexer.Release) (int, []epKey) {
	return c.grabSeriesLimited(ctx, s, releases, nil)
}

// grabSeriesLimited is grabSeriesFrom restricted to a specific set of wanted episodes
// (nil = everything the series is missing). The absolute-number follow-up passes the
// episodes the title search left uncovered, so it can't grab a second release for an
// episode the first pass already handled — wantedEpisodes still lists those, because a
// grab isn't reflected until it imports.
func (c *Coordinator) grabSeriesLimited(ctx context.Context, s series.Series, releases []indexer.Release, only []epKey) (grabbedN int, remaining []epKey) {
	grabbedN, remaining, _ = c.grabSeriesScoped(ctx, s, releases, only, nil)
	return grabbedN, remaining
}

// grabSeriesScoped is grabSeriesLimited that also skips the normalized titles in exclude
// and reports the titles it grabbed. A stall fail-over uses both: the stalled release
// isn't blocklisted while it waits for a replacement, so it has to be kept out by hand,
// and the history line names what replaced it.
func (c *Coordinator) grabSeriesScoped(ctx context.Context, s series.Series, releases []indexer.Release, only []epKey, exclude map[string]bool) (grabbedN int, remaining []epKey, took []string) {
	wanted, seriesSeasons := wantedEpisodes(s)
	if only != nil {
		wanted = only
	}
	if len(wanted) == 0 {
		return
	}
	blocked, err := c.blockedSetSeries(ctx, s.ID)
	if err != nil {
		c.skipUnreadable(s.Title, err)
		return 0, wanted, nil
	}
	// Releases already grabbed for this show and not yet imported. seriesDownloading()
	// alone couldn't be trusted to catch these — see pendingSeriesGrabTitles.
	pending, err := c.pendingSeriesGrabTitles(ctx, s.ID)
	if err != nil {
		c.skipUnreadable(s.Title, err)
		return 0, wanted, nil
	}

	// Score all candidates with the series' quality profile; keep the eligible set
	// ranked best-first, each paired with its parsed release + indexer info.
	byName := make(map[string]indexer.Release, len(releases))
	cands := make([]quality.Candidate, 0, len(releases))
	// Every rejection below used to be a silent continue, so a search that found forty
	// releases and took none was indistinguishable from one that found nothing. Count
	// the reasons and keep one example of each — "why wasn't this grabbed?" is the
	// single most common question this code has to answer.
	var nBlocked, nWrongTitle int
	var exampleWrongTitle string
	rts := newRuntimeIndex(s)
	for _, rel := range bestByTitle(grabbable(releases)) {
		if blocked[normTitle(rel.Title)] || exclude[normTitle(rel.Title)] {
			nBlocked++
			continue
		}
		if !seriesTitleMatches(rel.Title, s) {
			// A different show that merely shares a title prefix ("Below Deck
			// Mediterranean" for "Below Deck") — or, for anime, the same show under its
			// romaji name, which only matches once the series is flagged as anime.
			nWrongTitle++
			if exampleWrongTitle == "" {
				exampleWrongTitle = rel.Title
			}
			continue
		}
		byName[rel.Title] = rel
		cands = append(cands, c.newSeriesCandidate(ctx, s, rts, rel))
	}
	// Resolved once: the decision and every grab this pass records run under the same
	// profile, the default when the series' own was deleted.
	profile := c.effectiveProfile(ctx, s.QualityProfile, quality.MediaSeries)
	decision := c.quality.Decide(ctx, profile, cands)
	eligible := decision.Eligible // sorted best (highest quality) first

	// Deferred so it sees the final grabbedN and remaining.
	defer func() {
		if grabbedN > 0 || len(releases) == 0 {
			return
		}
		attrs := []any{
			"series", s.Title, "found", len(releases),
			"rejected_wrong_title", nWrongTitle,
			"rejected_blocklisted", nBlocked,
			"rejected_by_profile", len(cands) - len(eligible),
			"eligible", len(eligible), "still_missing", len(remaining),
		}
		if exampleWrongTitle != "" {
			attrs = append(attrs, "example_title_mismatch", exampleWrongTitle)
		}
		// The ceiling now judges packs by the runtime summed from TMDB. When TMDB's
		// lengths or episode list are off, a pack is wrongly "over the ceiling" — name one,
		// with the runtime it was costed against, so that's diagnosable from the log.
		for _, ev := range decision.Rejected {
			if strings.HasPrefix(ev.RejectReason, "Over your") && ev.Candidate.RuntimeMin > 0 {
				attrs = append(attrs, "example_over_ceiling", fmt.Sprintf("%s (%d min, %.1f Mb/s)",
					ev.Candidate.Name, ev.Candidate.RuntimeMin, bitrateMbps(ev.Candidate.SizeGB, ev.Candidate.RuntimeMin)))
				break
			}
		}
		// Anime released under a romaji name, or numbered absolutely, only matches once
		// the series is flagged as anime — and that flag has to be set by hand on a
		// series added before the anime support landed. It is by far the most common
		// reason a show with plenty of releases grabs none of them.
		if !s.IsAnime() && nWrongTitle > 0 && len(eligible) == 0 {
			attrs = append(attrs, "hint", "every release was rejected on title — if this is anime, turn on the Anime toggle so its romaji title and absolute episode numbers are recognised")
		}
		c.log.Info("series: found releases but grabbed none", attrs...)
	}()

	// Report back what no release here covered, so the caller can follow up with a
	// targeted search (anime: query by absolute episode number).
	g := c.newSeriesGrabber(s, profile, byName, pending, false, WholeShow)
	remaining = planSeriesGrabs(planInput{
		eligible:      eligible,
		wanted:        wanted,
		seriesSeasons: seriesSeasons,
		counts:        seasonEpisodeCounts(s),
		ended:         showEnded(s.Status),
		cover: func(r parser.Release, needed map[epKey]bool) []epKey {
			return c.coveredByFor(ctx, s, r, needed)
		},
		try:   func(name, label string) bool { return g.try(ctx, name, label) },
		log:   c.log,
		title: s.Title,
	})
	return g.n, remaining, g.titles
}

// sortedKeys returns the still-uncovered episodes in a stable order, so follow-up
// searches pick the same episodes each sweep rather than a random map sample.
func sortedKeys(needed map[epKey]bool) []epKey {
	out := make([]epKey, 0, len(needed))
	for k := range needed {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].season != out[j].season {
			return out[i].season < out[j].season
		}
		return out[i].episode < out[j].episode
	})
	return out
}

// seasonEpisodeCounts maps each real season to how many episodes it has, so a pack's bulk
// can be weighed against what's actually wanted from it.
func seasonEpisodeCounts(s series.Series) map[int]int {
	out := map[int]int{}
	for _, sn := range s.Seasons {
		if sn.SeasonNumber > 0 {
			out[sn.SeasonNumber] = len(sn.Episodes)
		}
	}
	return out
}

// indexerQuery turns a library title into something an indexer will actually match.
//
// Scene releases carry no punctuation: "Teen Titans Go!" ships as "Teen.Titans.Go.S07...".
// Searching with the title verbatim therefore narrows or misses results — a whole show's
// season packs can be invisible because of one exclamation mark. Accents are folded for the
// same reason (see the Pokémon case), and separators become spaces so a title that already
// uses dots still matches.
func indexerQuery(title string) string {
	var b strings.Builder
	for _, r := range parser.FoldAccents(title) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		case r == ' ' || r == '.' || r == '_' || r == '-':
			b.WriteByte(' ')
		case r == '&':
			// "&" becomes "and", matching how releases actually spell it and how
			// titleKey normalizes for comparison. Dropping it instead turned
			// "Love & Death" into the query "Love Death", which matches
			// "Love, Death & Robots" so strongly that it filled every result slot and
			// the show's own releases never appeared.
			b.WriteString(" and ")
			// Everything else — ! ? : ; , ' " ( ) [ ] — is dropped: releases don't
			// carry it, and including it only ever narrows the match.
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// sortedSeasons orders seasons so the fan-out is deterministic and the earliest missing
// seasons are queried first when the cap bites.
func sortedSeasons(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// maxSeasonQueries bounds the per-season fan-out so a show with dozens of missing seasons
// can't flood an indexer. Each query is throttled per host anyway.
const maxSeasonQueries = 12

// searchSeriesReleases finds releases for a series, querying each season that's actually
// missing something rather than relying on one broad title search.
//
// A bare title query returns whatever single page the indexer feels like — 35 results, in
// the case that exposed this — so a long-running show's season packs simply never appear
// and those seasons stay empty forever. Torznab's tvsearch takes a season number directly,
// which asks the indexer for that season instead of hoping it turns up in a general match.
func (c *Coordinator) searchSeriesReleases(ctx context.Context, s series.Series) ([]indexer.Release, error) {
	return c.searchSeriesReleasesExcept(ctx, s, nil)
}

// searchSeriesReleasesExcept is searchSeriesReleases without the per-season queries for
// the seasons in skip (ones still downloading), which would only cost indexer calls.
func (c *Coordinator) searchSeriesReleasesExcept(ctx context.Context, s series.Series, skip map[int]bool) ([]indexer.Release, error) {
	title := indexerQuery(s.Title)

	// The broad query first: it catches multi-season and complete-show packs, which no
	// single season number would find.
	res, err := c.indexers.Search(ctx, indexer.SearchQuery{
		Text: title, MediaType: indexer.MediaSeries, Limit: 100,
	})
	if err != nil {
		return nil, err
	}
	all := res.Releases

	// Alias queries. An arc released under its own name ("BLEACH Thousand-Year Blood
	// War") simply isn't in the results for the series' real title — the indexer has no
	// idea the two are the same show — so each alias gets its own broad query. Only
	// runs for a series that has aliases, which is almost none of them.
	for _, a := range s.Aliases {
		aq := indexerQuery(a.Title)
		if aq == "" || aq == title {
			continue
		}
		ares, aerr := c.indexers.Search(ctx, indexer.SearchQuery{
			Text: aq, MediaType: indexer.MediaSeries, Limit: 100,
		})
		if aerr != nil {
			c.log.Warn("series: alias search failed", "series", s.Title, "alias", a.Title, "err", aerr)
			continue
		}
		c.log.Info("series: alias search", "series", s.Title, "alias", a.Title, "returned", len(ares.Releases))
		all = append(all, ares.Releases...)
	}

	wanted, _ := wantedEpisodes(s)
	need := seasonsOf(setOf(wanted))
	for sn := range skip {
		delete(need, sn)
	}
	seasons := sortedSeasons(need)
	if len(seasons) == 0 {
		return all, nil
	}
	return c.searchSeasons(ctx, s, title, c.rotateSeasons(ctx, s, seasons), all), nil
}

// rotateSeasons reorders the incomplete seasons so this sweep resumes where the last one
// stopped, and records where the next should pick up.
//
// Without it the fan-out restarted at the lowest season every time and maxSeasonQueries cut
// it off, so a show with more than that many gaps could never search its later seasons at
// all — Bleach logged "seasons=17 seasons_queried=12" every sweep and season 17 was simply
// never asked about.
//
// The cursor holds a SEASON NUMBER, not an index: which seasons are incomplete changes
// between sweeps as gaps fill, and an index into a shifting slice would skip or repeat
// seasons at random.
func (c *Coordinator) rotateSeasons(ctx context.Context, s series.Series, seasons []int) []int {
	if len(seasons) <= maxSeasonQueries {
		return seasons // they all fit in one sweep — nothing to rotate
	}
	cursor, _ := c.series.SearchCursors(ctx, s.ID)
	start := 0
	for i, sn := range seasons {
		if sn >= cursor {
			start = i
			break
		}
	}
	out := append(append([]int{}, seasons[start:]...), seasons[:start]...)
	// Where the budget runs out. If a sweep is cut short (context cancelled mid-fan-out)
	// this advances past a season it didn't actually query — harmless, because the order
	// wraps, so that season comes around again rather than being lost.
	c.series.SetSeasonCursor(ctx, s.ID, out[maxSeasonQueries])
	return out
}

// searchSeasons runs one tvsearch per season and merges the results into have, deduped by
// download URL. Shared by the automatic and interactive paths so both surface season packs
// the same way.
func (c *Coordinator) searchSeasons(ctx context.Context, s series.Series, title string, seasons []int, have []indexer.Release) []indexer.Release {
	// Deduped by TITLE, not download URL: Prowlarr mints a new URL per request, so the
	// same release fetched by the broad query and by a season query looked like two
	// different results.
	seen := map[string]bool{}
	for _, r := range have {
		seen[r.Title] = true
	}
	queried := 0
	for _, season := range seasons {
		if ctx.Err() != nil || queried >= maxSeasonQueries {
			break
		}
		queried++
		sr, err := c.indexers.Search(ctx, indexer.SearchQuery{
			Text: title, MediaType: indexer.MediaSeries, Season: season, Limit: 100,
		})
		if err != nil {
			continue // one season failing shouldn't sink the rest
		}
		for _, r := range sr.Releases {
			if !seen[r.Title] {
				seen[r.Title] = true
				have = append(have, r)
			}
		}
	}
	c.log.Info("series: searched by season", "series", s.Title,
		"seasons", len(seasons), "seasons_queried", queried, "releases", len(have))
	return have
}

// addSeasonSearches fans out over every season the series HAS — the interactive browse
// case, where the user wants to see what exists rather than only fill gaps.
func (c *Coordinator) addSeasonSearches(ctx context.Context, s series.Series, title string, have []indexer.Release) []indexer.Release {
	_, seriesSeasons := wantedEpisodes(s)
	return c.searchSeasons(ctx, s, title, sortedSeasons(seriesSeasons), have)
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

// coveredByFor is coveredBy with anime awareness: an episode-scope release for an
// anime series is resolved through absolute/positional numbering before matching
// (so "[Group] Show - 137" covers the metadata's season-3 episode). Packs still match
// by season for both types.
func (c *Coordinator) coveredByFor(ctx context.Context, s series.Series, r parser.Release, needed map[epKey]bool) []epKey {
	// A release that matched one of the series' ALIASES is numbered in that alias' own
	// universe, not the series'. "Thousand-Year Blood War S02E02" is the arc's second
	// cour, which is Bleach S17E15 — reading it as the series' own S2E2 would file a
	// 2023 episode against a 2005 one. Checked first, and only ever true for a release
	// whose title matched an alias, so nothing else changes behaviour.
	if refs, ok := c.series.AliasEpisodes(ctx, s.ID, r); ok {
		return keysWanted(refs, needed)
	}
	if !s.IsAnime() {
		return coveredBy(r, needed)
	}
	var refs []series.EpisodeRef
	switch {
	case r.Kind() == parser.KindEpisode:
		refs = c.series.ResolveEpisodes(ctx, s.ID, r) // absolute + per-cour + scene mapping
	case r.Season > 0 && !r.Complete && len(r.Seasons) <= 1 && !c.series.HasSeason(ctx, s.ID, r.Season):
		// A split-season pack ("Frieren S02") for a season TMDB doesn't have — map the
		// whole scene season onto TMDB's continuous numbering.
		refs = c.series.SceneSeasonEpisodes(ctx, s.ID, r.Season)
	default:
		return coveredBy(r, needed) // real-season pack / multi-season / complete show
	}
	return keysWanted(refs, needed)
}

// keysWanted narrows resolved episode refs to the ones actually still wanted.
func keysWanted(refs []series.EpisodeRef, needed map[epKey]bool) []epKey {
	var out []epKey
	for _, ref := range refs {
		k := epKey{ref.Season, ref.Episode}
		if needed[k] {
			out = append(out, k)
		}
	}
	return out
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

// packIsProportionate reports whether taking a pack covering packSeasons is a sensible
// trade for the seasons actually needed.
//
// Coverage alone is not enough. "Do I need something from season 3?" is satisfied by a
// six-season complete pack, so the old check happily downloaded an entire show — hundreds
// of gigabytes — to fill a single missing episode, then discarded almost all of it. A pack
// has to be mostly useful, not merely sufficient.
//
// The rule: at least half the seasons it brings must be seasons we actually want. A show
// missing 5 of 6 seasons still takes the complete pack; a show missing 1 of 6 does not.
func packIsProportionate(neededSeasons, packSeasons map[int]bool) bool {
	if len(packSeasons) <= 1 {
		return true // a single-season pack can't be disproportionate
	}
	useful := 0
	for s := range packSeasons {
		if neededSeasons[s] {
			useful++
		}
	}
	return useful*2 >= len(packSeasons)
}

// packIsWorthIt reports whether a pack brings enough that's actually wanted to justify
// downloading all of it.
//
// Pass 2 used to pick purely by "covers the most needed episodes", which meant a four-season
// pack won for a single missing episode — it covered it, and nothing else did. Even a single
// season pack is a poor trade for one gap: 38 episodes downloaded to obtain one.
//
// So weigh what it brings against what's wanted. A pack must be at least half useful.
func packIsWorthIt(r parser.Release, needed int, seriesSeasons map[int]bool, counts map[int]int) bool {
	brings := 0
	for season := range seriesSeasons {
		if r.CoversSeason(season) {
			brings += counts[season]
		}
	}
	if brings == 0 {
		return true // unknown episode counts — don't block on missing metadata
	}
	return needed*2 >= brings
}

// packSeasonsOf lists the seasons a release covers, bounded by the seasons the series
// actually has (a "S01-S06" tag on a 4-season show shouldn't invent seasons).
func packSeasonsOf(r parser.Release, seriesSeasons map[int]bool) map[int]bool {
	out := map[int]bool{}
	for s := range seriesSeasons {
		if r.CoversSeason(s) {
			out[s] = true
		}
	}
	return out
}

// ---- import (multi-file) -------------------------------------------------

// ImportSeriesDownloads imports finished TV downloads: for each completed torrent
// in the series category, hardlink every episode file into the library and mark
// the episode. A season pack yields many files from one download.
func (c *Coordinator) ImportSeriesDownloads(ctx context.Context) {
	if c.series == nil || c.imp == nil {
		return
	}
	// Look at every completed download (not just the series category), so a TV pack that
	// landed in the wrong category — e.g. added straight to qBittorrent uncategorized —
	// is visible instead of silently ignored.
	completed, err := c.downloads.CompletedInCategory(ctx, "")
	if err != nil {
		return
	}
	for _, it := range completed {
		if it.Category != seriesCategory {
			// Diagnostic: a completed TV download that matches a library series but isn't
			// in the TV category won't import — flag it so it's not a silent no-op.
			if p := parser.Parse(it.Name); p.IsTV() {
				if _, ok := c.series.MatchByTitle(ctx, series.NormTitle(p.Title)); ok {
					c.log.Warn("series import: a completed TV download is in the wrong category — it won't import; re-grab via Arrmada or set its qBittorrent category to "+seriesCategory,
						"release", it.Name, "category", it.Category)
				}
			}
			continue
		}
		if it.ContentPath == "" {
			continue
		}
		if c.hasReview(ctx, it.Hash) {
			continue // already held for review (or resolved) — don't re-flag or import
		}
		parsed := parser.Parse(it.Name)
		s, matchOK := c.series.MatchByTitle(ctx, series.NormTitle(parsed.Title))

		// Given-up guard: if we've already blocklisted this exact release for the series
		// (it downloaded but couldn't import — junk, a fake, or unresolvable numbering),
		// don't re-scan it. The auto-searcher skips blocklisted releases too, so together
		// this breaks the grab→fail→re-grab loop.
		if matchOK {
			blocked, err := c.blockedSetSeries(ctx, s.ID)
			if err != nil {
				// Can't tell whether it was given up on: leave it for the next pass rather
				// than import something we may have blocklisted.
				c.log.Warn("series: blocklist unreadable — not importing this round", "release", it.Name, "err", err)
				continue
			}
			if blocked[normTitle(it.Name)] {
				continue
			}
		}

		// Already-imported guard. Normally skip a torrent we've handled — but if the
		// season it covers STILL has aired episodes missing (e.g. a pack that only
		// partly extracted the first time, before the recursive-unpack fix), give it
		// another pass so it can fill the gaps. The per-episode quality gate in
		// importSeriesInto keeps this from ping-ponging once the season is complete.
		if c.hashAlreadyImported(ctx, it.Hash) {
			// PACKS only. A single-episode release can never fill a season's remaining
			// gaps, so re-processing one just re-imports the episode it already provided,
			// finds nothing new, and blocklists it as a pack that "can't complete the
			// season" — turning every successful single-episode import into a permanent
			// blocklist entry.
			if !matchOK || parsed.Kind() == parser.KindEpisode ||
				!c.series.SeasonHasMissing(ctx, s.ID, parsed.Season) {
				continue
			}
			c.log.Info("series import: re-processing an already-imported pack to fill missing episodes",
				"series", s.Title, "release", it.Name, "season", parsed.Season)
		}

		// If this download was grabbed for a specific series, verify its content is
		// actually that series — otherwise hold it for admin review rather than skip
		// it silently (e.g. a "Below Deck Mediterranean" pack grabbed for "Below Deck").
		if gid, indexer, grabbed := c.grabbedMediaForHash(ctx, it.Hash, it.Name, "series"); grabbed {
			if expected, err := c.series.Get(ctx, gid); err == nil && (!matchOK || s.ID != expected.ID) {
				reason := fmt.Sprintf("Grabbed for %q but the download looks like %q", expected.Title, parsed.Title)
				c.addReview(ctx, Review{
					Hash: it.Hash, Name: it.Name, ContentPath: it.ContentPath, MediaType: "series",
					ExpectedID: expected.ID, ExpectedTitle: expected.Title, ParsedTitle: parsed.Title,
					Reason: reason, SizeBytes: it.SizeBytes, Indexer: indexer,
				})
				continue
			}
		}
		if !matchOK {
			// The import sweep runs every 30 seconds, so an unmatchable download used to
			// log this line forever — thousands of identical entries burying real events,
			// while the searcher separately re-grabbed the same release because its
			// episodes still read as missing. Log it once per download instead, and after
			// enough attempts hand it to review so a human can see it and it stops being
			// invisible.
			n := c.noteUnmatched(it.Hash)
			switch {
			case n == 1:
				c.log.Info("series import: no matching library series",
					"release", it.Name, "parsed_title", parsed.Title)
			case n == unmatchedReviewAfter:
				c.log.Warn("series import: download still matches no series — sending to review",
					"release", it.Name, "parsed_title", parsed.Title, "attempts", n)
				c.addReview(ctx, Review{
					Hash: it.Hash, Name: it.Name, ContentPath: it.ContentPath, MediaType: "series",
					ParsedTitle: parsed.Title, SizeBytes: it.SizeBytes,
					Reason: fmt.Sprintf("Parsed as %q, which matches no series in your library", parsed.Title),
				})
			}
			continue // not something we grabbed and not a library title — leave alone
		}
		// A release the user picked by hand imports regardless of what it scores against
		// the current file — but only for the season or episode it was picked for. See
		// importSeriesInto.
		placed, matched, unresolved, importFailed := c.importSeriesInto(ctx, s, it.ContentPath, c.grabForce(ctx, it.Hash))
		imported := len(placed)
		if importFailed > 0 {
			// Some files resolved to wanted episodes but couldn't be placed (disk full,
			// permissions, a file mid-move). Leave the download unrecorded so the next
			// sweep retries — marking it handled here left episodes permanently
			// unimported after a transient error.
			c.log.Warn("series import: some files failed to place — will retry next sweep",
				"series", s.Title, "release", it.Name, "failed", importFailed, "placed", imported)
		} else if matched > 0 {
			// Every file mapped to a known episode (some newly placed, some already
			// present) — the download is handled, so drop it from the downloads view
			// and stop re-scanning it.
			c.recordImportedHash(ctx, it.Hash, it.Name, it.SizeBytes)
			c.markSeriesGrabImported(ctx, s.ID, it.Hash, it.Name) // flip THIS grab (not siblings) for seed cleanup
			if imported > 0 {
				c.log.Info("series: imported episodes", "series", s.Title, "count", imported, "release", it.Name)
				c.series.AddEvent(ctx, s.ID, "imported", fmt.Sprintf("Imported %d episode%s from %s", imported, plural(imported), it.Name))
				c.seriesImported(ctx, s.ID, placed)
				c.bus.Publish("series.imported", map[string]any{"title": s.Title, "id": s.ID, "count": imported})
			} else if c.series.SeasonHasMissing(ctx, s.ID, parsed.Season) {
				// Re-processed but placed nothing new while the season is still incomplete.
				// Either way we stop re-grabbing it, but WHY matters, and the two cases are
				// not the same thing:
				//
				//   unresolved > 0 — files we couldn't map onto any known episode. This is
				//   the real numbering fault (Ben 10's scene numbering vs the metadata).
				//
				//   unresolved == 0 — every file in the release landed on a known episode.
				//   Nothing is wrong with it; it just doesn't CONTAIN the rest of the season.
				//   Streaming-sourced packs of long-running kids' shows do this constantly:
				//   TMDB lists 52 episodes for the season, the service carried 28. Calling
				//   that "unresolved episode numbering" told users their perfectly good
				//   import was broken and sent them looking for a bug that wasn't there.
				c.addBlockSeries(ctx, s.ID, it.Name, c.grabIndexer(ctx, it.Name, "series"), incompleteSeasonReason(unresolved))
				c.log.Info("series import: release can't complete the season — won't be re-grabbed",
					"series", s.Title, "release", it.Name, "season", parsed.Season, "unresolved_files", unresolved)
			}
		} else if unresolved > 0 {
			// The release IS full of video — we just couldn't work out which episodes.
			// Blocklisting that as junk is a false accusation, and an expensive one: a
			// 122-file "Parks and Recreation S01-S07" pack was discarded because every
			// file used the "1x01" form the parser didn't understand, and the blocklist
			// then blocked recovery even after the parser learned it.
			//
			// Hold it for review instead. The user can see what arrived, import it into
			// the right show, and nothing is thrown away over a naming convention.
			c.addReview(ctx, Review{
				Hash: it.Hash, Name: it.Name, ContentPath: it.ContentPath, MediaType: "series",
				ExpectedID: s.ID, ExpectedTitle: s.Title, ParsedTitle: parsed.Title,
				SizeBytes: it.SizeBytes, Indexer: c.grabIndexer(ctx, it.Name, "series"),
				Reason: fmt.Sprintf("Downloaded, but none of its %d video file%s could be matched to an episode — the episode numbering isn't in a form Arrmada recognises",
					unresolved, plural(unresolved)),
			})
			c.log.Warn("series import: has video but nothing could be placed — held for review",
				"series", s.Title, "release", it.Name, "video_files", unresolved)
		} else {
			// No video at all: junk, a fake, or an archive set that won't unpack. This one
			// can never import, so blocklist it and stop re-scanning.
			reason := "downloaded but contained no video"
			if hasExecutable(it.ContentPath) {
				reason = "download contained executables and no video (possible fake/malware)"
				c.log.Warn("series import: refusing a download with executables and no video — blocklisted",
					"series", s.Title, "release", it.Name, "content_path", it.ContentPath)
				// The release itself is hostile, not merely wrong for this show — block it
				// for the whole library, or the same upload stays grabbable for anything
				// else it happens to match.
				c.addBlockGlobal(ctx, it.Name, c.grabIndexer(ctx, it.Name, "series"), reason)
			} else {
				c.log.Warn("series import: no video in the download — blocklisting so it isn't re-grabbed",
					"series", s.Title, "release", it.Name, "content_path", it.ContentPath)
			}
			c.addBlockSeries(ctx, s.ID, it.Name, c.grabIndexer(ctx, it.Name, "series"), reason)
			c.removeIfNoVideo(ctx, it.Hash, it.Name, it.ContentPath)
			// The download is gone and can never import, so the grab must not stay
			// 'grabbed' — the pending guard would keep answering "already grabbed and
			// still importing" for a day, and the show couldn't take an alternate.
			c.markSeriesGrabFailed(ctx, s.ID, it.Hash, it.Name)
		}
	}
	// Forget unmatched counters for downloads that are gone from the completed list,
	// so the map only ever tracks what's actually in the client.
	active := make(map[string]bool, len(completed))
	activePaths := make(map[string]bool, len(completed))
	for _, it := range completed {
		active[it.Hash] = true
		activePaths[it.ContentPath] = true
	}
	c.pruneUnmatched(active)
	c.pruneMetadataRetries(activePaths)
}

// incompleteSeasonReason explains why a re-processed release added nothing, given how many
// of its files failed to map onto a known episode. Users read this string in the Blocklist,
// so it has to say which of the two situations actually occurred.
func incompleteSeasonReason(unresolved int) string {
	if unresolved > 0 {
		return fmt.Sprintf("downloaded but %d file%s couldn't be matched to an episode (unresolved episode numbering)",
			unresolved, plural(unresolved))
	}
	return "fully imported — this release has no more episodes for this season"
}

func aired(date string) bool {
	// No air date means UNAIRED, and is not searched for.
	//
	// It used to mean "aired (best effort)", on the theory that odd metadata shouldn't
	// block a grab. In practice the opposite is far more common: TMDB pads a season with
	// placeholder episodes that have no date, and the searcher then hunts forever for
	// episodes that don't exist. ARK: The Animated Series listed 14 for a season that
	// aired 6, so a fully-imported show kept re-grabbing its own complete pack — and the
	// "can't complete the season" blocklist never fired to stop it, because
	// SeasonHasMissing already excluded dateless episodes. The three rules disagreed;
	// they now agree.
	//
	// The cost is a genuinely-aired episode whose date TMDB doesn't carry: it won't be
	// searched automatically. The UI already labels those UNAIRED, and a manual search
	// still grabs them.
	if date == "" {
		return false
	}
	return date <= nowDate()
}

// recordSeriesGrab tracks a series grab for seed cleanup (media_type=series).
func (c *Coordinator) recordSeriesGrab(ctx context.Context, seriesID int64, title, indexer, profile, infoHash string) {
	// The stall window comes from the profile the grab ran under, so a deleted profile
	// must mean the default here too — not 0, which switches fail-over off.
	profile = c.effectiveProfile(ctx, profile, quality.MediaSeries)
	seedEnabled, seedRatio, seedHours := c.seedRules(ctx, indexer)
	// stall_minutes was hardcoded to 0, and detectStalledSeries returns immediately when
	// it isn't positive — so the entire series stall fail-over was dead code. A TV
	// download that stalled was never blocklisted, never removed, and its grab row sat at
	// 'grabbed' forever. Movies have always passed the profile's value here.
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO grabs (movie_id, version_id, title, indexer, quality_profile, stall_minutes, seed_enabled, seed_ratio, seed_hours, media_type, info_hash)
		 VALUES (?, 0, ?, ?, ?, ?, ?, ?, ?, 'series', ?)`,
		seriesID, title, indexer, profile, c.quality.StallMinutes(ctx, profile),
		boolToInt(seedEnabled), seedRatio, seedHours, infoHash)
	if err != nil {
		c.log.Warn("series: record grab failed", "err", err)
	}
}
