package automation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/indexer"
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
	for _, s := range all {
		if !s.Monitored {
			continue
		}
		if seriesDownloading(queue, s.Title) {
			continue // already downloading something for this show — let it finish
		}
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
		n, err := c.searchSeriesOnce(ctx, s.ID)
		switch {
		case err != nil:
			c.log.Warn("series: search failed", "series", s.Title, "err", err)
		case n > 0:
			c.series.ResetSearchMisses(ctx, s.ID)
		default:
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
func (c *Coordinator) SearchSeriesNow(ctx context.Context, seriesID int64) error {
	_, err := c.searchSeriesOnce(ctx, seriesID)
	return err
}

// searchSeriesOnce is SearchSeriesNow that also reports how many releases it grabbed,
// so the missing-sweep can back off a series that keeps coming up empty.
func (c *Coordinator) searchSeriesOnce(ctx context.Context, seriesID int64) (int, error) {
	if c.series == nil {
		return 0, nil
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return 0, err
	}
	res, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: s.Title, MediaType: indexer.MediaSeries, Limit: 100})
	if err != nil || len(res.Releases) == 0 {
		return 0, err
	}
	return c.grabSeriesFrom(ctx, s, res.Releases), nil
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
func (c *Coordinator) grabSeriesFrom(ctx context.Context, s series.Series, releases []indexer.Release) (grabbedN int) {
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
		if !seriesTitleMatches(rel.Title, s) {
			continue // a different show that merely shares a title prefix (e.g. "Below Deck Mediterranean" for "Below Deck")
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
	// grabbedGB tracks what this pass has already committed, so a series with many
	// missing seasons can't queue past the free space by checking each pack against the
	// same (pre-download) free-space reading.
	grabbedGB := 0.0
	grab := func(name string, label string) {
		rel := byName[name]
		if rel.DownloadURL == "" || grabbed[rel.DownloadURL] {
			return
		}
		// Space guard — the movie path has had this; TV (where packs are far bigger) did not.
		if !c.diskOKFor(grabbedGB + rel.SizeGB()) {
			c.log.Warn("series: skipping grab — not enough free space in the downloads dir",
				"series", s.Title, "release", rel.Title, "release_gb", rel.SizeGB(), "already_queued_gb", grabbedGB)
			return
		}
		if err := c.grabTo(ctx, rel.Indexer, rel.DownloadURL, rel.Title, seriesCategory); err != nil {
			c.log.Warn("series: grab failed", "series", s.Title, "release", rel.Title, "err", err)
			return
		}
		grabbed[rel.DownloadURL] = true
		grabbedN++
		grabbedGB += rel.SizeGB()
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
			n := len(c.coveredByFor(ctx, s, eligible[i].Candidate.Release, needed))
			if n > bestCover {
				bestCover, best = n, &eligible[i]
			}
		}
		if best == nil || bestCover == 0 {
			break
		}
		grab(best.Candidate.Name, "pack")
		for _, k := range c.coveredByFor(ctx, s, best.Candidate.Release, needed) {
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
		for _, k := range c.coveredByFor(ctx, s, r, needed) {
			grab(ev.Candidate.Name, "episode")
			delete(needed, k)
		}
	}
	return grabbedN
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
		if matchOK && c.blockedSetSeries(ctx, s.ID)[normTitle(it.Name)] {
			continue
		}

		// Already-imported guard. Normally skip a torrent we've handled — but if the
		// season it covers STILL has aired episodes missing (e.g. a pack that only
		// partly extracted the first time, before the recursive-unpack fix), give it
		// another pass so it can fill the gaps. The per-episode quality gate in
		// importSeriesInto keeps this from ping-ponging once the season is complete.
		if c.hashAlreadyImported(ctx, it.Hash) {
			if !matchOK || !c.series.SeasonHasMissing(ctx, s.ID, parsed.Season) {
				continue
			}
			c.log.Info("series import: re-processing an already-imported pack to fill missing episodes",
				"series", s.Title, "release", it.Name, "season", parsed.Season)
		}

		// If this download was grabbed for a specific series, verify its content is
		// actually that series — otherwise hold it for admin review rather than skip
		// it silently (e.g. a "Below Deck Mediterranean" pack grabbed for "Below Deck").
		if gid, indexer, grabbed := c.grabbedMediaFor(ctx, it.Name, "series"); grabbed {
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
			c.log.Info("series import: no matching library series", "release", it.Name, "parsed_title", parsed.Title)
			continue // not something we grabbed and not a library title — leave alone
		}
		imported, matched := c.importSeriesInto(ctx, s, it.ContentPath)
		if matched > 0 {
			// Every file mapped to a known episode (some newly placed, some already
			// present) — the download is handled, so drop it from the downloads view
			// and stop re-scanning it.
			c.recordImportedHash(ctx, it.Hash, it.Name, it.SizeBytes)
			c.markSeriesGrabImported(ctx, s.ID, it.Name) // flip THIS grab (not siblings) for seed cleanup
			if imported > 0 {
				c.log.Info("series: imported episodes", "series", s.Title, "count", imported, "release", it.Name)
				c.series.AddEvent(ctx, s.ID, "imported", fmt.Sprintf("Imported %d episode%s from %s", imported, plural(imported), it.Name))
				c.bus.Publish("series.imported", map[string]any{"title": s.Title, "id": s.ID, "count": imported})
			} else if c.series.SeasonHasMissing(ctx, s.ID, parsed.Season) {
				// Re-processed but placed nothing new while the season is still incomplete:
				// this release can't finish the job (e.g. Ben 10's scene numbering doesn't
				// map to the metadata). Blocklist it so the auto-searcher stops re-grabbing
				// the identical release every cycle.
				c.addBlockSeries(ctx, s.ID, it.Name, c.grabIndexer(ctx, it.Name, "series"), "downloaded but can't complete the season (unresolved episode numbering)")
				c.log.Warn("series import: release can't complete the season — blocklisted to stop re-grabbing", "series", s.Title, "release", it.Name)
			}
		} else {
			// A completed download that maps to no importable episode is junk, a fake, or
			// an unresolvable release. Blocklist it so it isn't re-grabbed or re-scanned.
			reason := "downloaded but nothing importable was found"
			if hasExecutable(it.ContentPath) {
				reason = "download contained executables and no video (possible fake/malware)"
				c.log.Warn("series import: refusing a download with executables and no video — blocklisted",
					"series", s.Title, "release", it.Name, "content_path", it.ContentPath)
			} else {
				c.log.Warn("series import: nothing importable — blocklisting so it isn't re-grabbed",
					"series", s.Title, "release", it.Name, "content_path", it.ContentPath)
			}
			c.addBlockSeries(ctx, s.ID, it.Name, c.grabIndexer(ctx, it.Name, "series"), reason)
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
