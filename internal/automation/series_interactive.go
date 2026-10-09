package automation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// RankSeriesReleases runs an interactive search for a series, optionally scoped to a
// season and episode (>0), scoring every relevant release against the series' quality
// profile and returning them ranked best-first — WITHOUT grabbing. This is the manual
// "search indexers" backend, shared by the season- and episode-level UI.
//
// A negative season is the whole show; season 0 is Specials, which lists only releases
// tagged S00.
func (c *Coordinator) RankSeriesReleases(ctx context.Context, seriesID int64, season, episode int) (ReleaseList, error) {
	return c.RankSeriesReleasesWith(ctx, seriesID, season, episode, nil)
}

// RankSeriesReleasesWith ranks under spec instead of the show's own profile when it's set
// (the quality builder's "test on a real title").
func (c *Coordinator) RankSeriesReleasesWith(ctx context.Context, seriesID int64, season, episode int, spec *quality.StoredProfile) (ReleaseList, error) {
	if c.series == nil {
		return ReleaseList{}, nil
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return ReleaseList{}, err
	}
	releases, _, err := c.searchSeriesScope(ctx, s, season, episode)
	if err != nil {
		return ReleaseList{}, err
	}

	byName := make(map[string]indexer.Release, len(releases))
	cands := make([]quality.Candidate, 0, len(releases))
	var droppedTitle, droppedScope int
	var sampleDropped, sampleScope []string
	for _, rel := range bestByTitle(releases) {
		if !seriesTitleMatches(rel.Title, s) {
			droppedTitle++
			if len(sampleDropped) < 8 {
				sampleDropped = append(sampleDropped, rel.Title+" → "+parser.Parse(rel.Title).Title)
			}
			continue // a different show that merely shares a title prefix (e.g. "Below Deck Mediterranean" for "Below Deck")
		}
		if p := parser.Parse(rel.Title); !c.releaseMatchesScope(ctx, s, p, season, episode) {
			droppedScope++
			// What a right-show release DID resolve to is the answer to "there are
			// torrents for this episode, why won't it take them?" — usually that they
			// are other episodes entirely, which no amount of title matching fixes.
			if len(sampleScope) < 8 {
				sampleScope = append(sampleScope, rel.Title+" → "+c.resolvedLabel(ctx, s, p))
			}
			continue // not relevant to the requested season/episode scope
		}
		byName[rel.Title] = rel
		cands = append(cands, newSeriesCandidate(rel))
	}
	c.log.Info("series: search filtered", "series", s.Title, "kept", len(cands), "dropped_wrong_title", droppedTitle, "dropped_out_of_scope", droppedScope)
	if len(sampleDropped) > 0 {
		c.log.Info("series: sample of dropped titles (parsed → title)", "series", s.Title, "samples", strings.Join(sampleDropped, " | "))
	}
	if len(sampleScope) > 0 {
		c.log.Info("series: sample of right-show releases that were other episodes (release → resolved)",
			"series", s.Title, "want_season", season, "want_episode", episode,
			"samples", strings.Join(sampleScope, " | "))
	}
	profile := c.effectiveProfile(ctx, s.QualityProfile, quality.MediaSeries)
	decision := c.decideWith(ctx, profile, spec, cands)

	// For a single-episode search we can show a bitrate (size ÷ episode runtime). Season/series
	// packs cover many episodes, so leave bitrate off there rather than mislead.
	epRuntime := 0
	if season >= 0 && episode > 0 {
		for _, sn := range s.Seasons {
			if sn.SeasonNumber != season {
				continue
			}
			for _, e := range sn.Episodes {
				if e.EpisodeNumber == episode {
					epRuntime = e.Runtime
				}
			}
		}
	}

	winnerName := ""
	if decision.Winner != nil {
		winnerName = decision.Winner.Candidate.Name
	}
	// Flag blocklisted releases like the movie path does, so the UI can warn before the
	// user re-grabs a release that just stalled or imported as junk.
	blocked, err := c.blockedSetSeries(ctx, s.ID)
	if err != nil {
		return ReleaseList{}, err
	}
	out := make([]RankedRelease, 0, len(cands))
	appendEval := func(ev quality.Evaluation) {
		rel := byName[ev.Candidate.Name]
		out = append(out, RankedRelease{
			Title:        ev.Candidate.Name,
			Indexer:      rel.Indexer,
			DownloadURL:  rel.DownloadURL,
			InfoURL:      rel.InfoURL,
			SizeGB:       ev.Candidate.SizeGB,
			Bitrate:      bitrateMbps(ev.Candidate.SizeGB, epRuntime),
			Seeders:      ev.Candidate.Seeders,
			Summary:      summarizeSeries(ev.Candidate.Release),
			Eligible:     ev.Eligible,
			RejectReason: ev.RejectReason,
			Recommended:  ev.Candidate.Name == winnerName,
			Blocklisted:  blocked[normTitle(ev.Candidate.Name)],
			Resolves:     c.resolvesLabel(ctx, s, ev.Candidate.Release),
		})
	}
	for _, ev := range decision.Eligible {
		appendEval(ev)
	}
	for _, ev := range decision.Rejected {
		appendEval(ev)
	}
	return ReleaseList{Profile: profile, Why: decision.Why, Releases: out}, nil
}

// searchSeriesScope runs the indexer queries for a season/episode scope (a negative season
// is the whole show, season 0 is Specials) and returns everything they found, unfiltered,
// plus the per-indexer errors of the main query. Shared by the interactive list and the quick Grab buttons, so
// a button searches exactly where the list the user would see does.
func (c *Coordinator) searchSeriesScope(ctx context.Context, s series.Series, season, episode int) ([]indexer.Release, map[string]string, error) {
	// Clean the title before it reaches an indexer: releases carry no punctuation, so
	// "Teen Titans Go!" must be searched as "Teen Titans Go" or its packs never appear.
	title := indexerQuery(s.Title)
	// Season/episode go in the tvsearch PARAMETERS, not baked into the text. Indexers
	// match those far better than "Title S07" as a string, and a bare title query returns
	// one capped page — which is how a nine-season show showed only 35 results.
	q := indexer.SearchQuery{Text: title, MediaType: indexer.MediaSeries, Limit: 400}
	// Anime is released under many numbering conventions (absolute "- 137", per-cour
	// SxxExx, or a split-season S02) — a narrow query would miss most. Search broad by
	// title and let the resolver-backed scope filter pick releases covering the episode.
	//
	// Specials can't go in the parameters at all: to tvsearch, season 0 is no season.
	if !s.IsAnime() && season > 0 {
		q.Season, q.Episode = season, episode
	}

	result, err := c.indexers.Search(ctx, q)
	if err != nil {
		return nil, result.Errors, err
	}
	// Browsing the whole show: fan out per season as well, for the same reason the
	// automatic search does. One title query can't surface nine seasons' worth of packs.
	if season < 0 && !s.IsAnime() {
		result.Releases = c.addSeasonSearches(ctx, s, title, result.Releases)
	}
	// Specials: name each one, since the broad title query is one capped page of mostly
	// regular episodes and there's no parameter that asks for season 0.
	if season == 0 {
		for _, n := range specialsToQuery(s, episode) {
			sq := fmt.Sprintf("%s S00E%02d", title, n)
			sres, serr := c.indexers.Search(ctx, indexer.SearchQuery{Text: sq, MediaType: indexer.MediaSeries, Limit: 100})
			if serr != nil {
				c.log.Warn("series: specials search failed", "series", s.Title, "query", sq, "err", serr)
				continue
			}
			result.Releases = append(result.Releases, sres.Releases...)
		}
	}
	// Ask for the specific episode by the arc's own numbering. The broad queries above
	// are capped by the indexer — TorrentLeech answers a bare q= with 35 rows, newest
	// first — so an episode from two years ago is never in them however well it matches.
	// Anime can't use tvsearch's season/ep parameters either, since the arc isn't
	// numbered like the season. Naming the episode is the only way to reach it.
	for _, term := range c.series.AliasSearchTerms(ctx, s.ID, season, episode) {
		tres, terr := c.indexers.Search(ctx, indexer.SearchQuery{
			Text: indexerQuery(term), MediaType: indexer.MediaSeries, Limit: 400,
		})
		if terr != nil {
			c.log.Warn("series: targeted alias search failed", "series", s.Title, "query", term, "err", terr)
			continue
		}
		c.log.Info("series: targeted alias search", "series", s.Title, "query", term, "returned", len(tres.Releases))
		result.Releases = append(result.Releases, tres.Releases...)
	}

	// The arc's own name is a different search entirely — the indexer doesn't know the
	// two titles are one show, so the series' title never returns the arc's releases.
	for _, a := range s.Aliases {
		aq := indexerQuery(a.Title)
		if aq == "" || aq == title {
			continue
		}
		ares, aerr := c.indexers.Search(ctx, indexer.SearchQuery{
			Text: aq, MediaType: indexer.MediaSeries, Limit: 400,
		})
		if aerr != nil {
			c.log.Warn("series: alias search failed", "series", s.Title, "alias", a.Title, "err", aerr)
			continue
		}
		c.log.Info("series: alias search", "series", s.Title, "alias", a.Title, "returned", len(ares.Releases))
		result.Releases = append(result.Releases, ares.Releases...)
	}
	// Log the REQUESTED scope, not the query parameters. For anime the query is
	// deliberately left broad (q.Season/q.Episode stay 0 so every numbering convention
	// is returned), so printing those said "season=0 episode=0" for a search of one
	// specific episode — which reads as "it searched everything and found nothing".
	c.log.Info("series: interactive search", "series", s.Title, "query", title,
		"want_season", season, "want_episode", episode,
		"query_season", q.Season, "query_episode", q.Episode,
		"raw", len(result.Releases), "indexer_errors", len(result.Errors))
	for name, e := range result.Errors {
		c.log.Warn("series: indexer error", "indexer", name, "err", e)
	}
	return result.Releases, result.Errors, nil
}

// maxSpecialQueries bounds the per-special searches of one Specials browse, so a show
// with dozens of missing specials can't flood an indexer.
const maxSpecialQueries = 5

// specialsToQuery lists the specials a season-0 search names: the one asked for, or the
// aired specials still missing a file, at most maxSpecialQueries.
func specialsToQuery(s series.Series, episode int) []int {
	if episode > 0 {
		return []int{episode}
	}
	var out []int
	for _, sn := range s.Seasons {
		if sn.SeasonNumber != 0 {
			continue
		}
		for _, e := range sn.Episodes {
			if len(out) < maxSpecialQueries && !e.HasFile && aired(e.AirDate) {
				out = append(out, e.EpisodeNumber)
			}
		}
	}
	return out
}

// isSpecialRelease reports whether a release is a Specials episode: an episode release
// that spelled out season 0 ("S00E05", "0x05"), carrying the wanted episode (any, when
// episode is 0). Nothing else may stand in for a special — a pack "covers" season 0 only
// because it covers the whole run, an absolute-numbered "[Grp] Show - 05" is a regular
// episode, and an alias' numbering has no specials.
func isSpecialRelease(p parser.Release, episode int) bool {
	if p.Kind() != parser.KindEpisode || !p.SeasonExplicit || p.Season != 0 {
		return false
	}
	if episode <= 0 {
		return true
	}
	for _, e := range p.Episodes {
		if e == episode {
			return true
		}
	}
	return false
}

// releaseMatchesScope is seriesReleaseMatches with anime awareness: an episode-scope
// anime release is resolved through absolute/positional numbering before checking it
// covers the requested (season, episode).
func (c *Coordinator) releaseMatchesScope(ctx context.Context, s series.Series, p parser.Release, season, episode int) bool {
	// Specials first: no numbering rule — alias, absolute or scene — maps onto season 0.
	if season == 0 {
		return isSpecialRelease(p, episode)
	}
	// An alias-titled release carries the alias' numbering, so resolve it that way
	// before any of the series' own numbering rules get a look.
	if refs, ok := c.series.AliasEpisodes(ctx, s.ID, p); ok {
		if season < 0 {
			return true
		}
		for _, ref := range refs {
			if ref.Season == season && (episode <= 0 || ref.Episode == episode) {
				return true
			}
		}
		return false
	}
	if !s.IsAnime() {
		return seriesReleaseMatches(p, season, episode)
	}
	if season < 0 {
		return p.IsTV()
	}
	if p.Kind() != parser.KindEpisode {
		if p.CoversSeason(season) {
			return true // real-season pack matching the requested TMDB season
		}
		// A split-season pack ("Frieren S02") for a season TMDB doesn't have.
		if p.Season > 0 && !p.Complete && len(p.Seasons) <= 1 && !c.series.HasSeason(ctx, s.ID, p.Season) {
			for _, ref := range c.series.SceneSeasonEpisodes(ctx, s.ID, p.Season) {
				if ref.Season == season && (episode <= 0 || ref.Episode == episode) {
					return true
				}
			}
		}
		return false
	}
	for _, ref := range c.series.ResolveEpisodes(ctx, s.ID, p) {
		if ref.Season == season && (episode <= 0 || ref.Episode == episode) {
			return true
		}
	}
	return false
}

// seriesReleaseMatches reports whether a release is relevant to a season/episode scope.
// season < 0 (the whole show) → any TV release; season 0 → an S00-tagged special; a season
// with episode <= 0 → covers that season; both set → the exact episode, or a pack that
// covers it.
func seriesReleaseMatches(p parser.Release, season, episode int) bool {
	if season < 0 {
		return p.IsTV()
	}
	if season == 0 {
		return isSpecialRelease(p, episode)
	}
	if !p.CoversSeason(season) {
		return false
	}
	if episode <= 0 {
		return true
	}
	if p.Kind() == parser.KindEpisode {
		for _, e := range p.Episodes {
			if e == episode {
				return true
			}
		}
		return false
	}
	return true // a season/multi-season/complete pack covers the episode
}

// summarizeSeries renders a release's tier + quality in plain language, e.g.
// "Season 2 pack · 1080p · WEB-DL" or "S02E05 · 4K".
func summarizeSeries(r parser.Release) string {
	tier := ""
	switch r.Kind() {
	case parser.KindCompleteShow:
		tier = "Complete series"
	case parser.KindMultiSeason:
		tier = "Seasons " + joinSeasons(r.Seasons)
	case parser.KindSeasonPack:
		tier = fmt.Sprintf("Season %d pack", r.Season)
	case parser.KindEpisode:
		if r.Season > 0 && len(r.Episodes) > 0 {
			tier = fmt.Sprintf("S%02dE%02d", r.Season, r.Episodes[0])
		}
	}
	q := summarize(r) // reuse the movie quality summary (resolution/HDR/source)
	switch {
	case tier == "":
		return q
	case q == "Standard quality":
		return tier
	default:
		return tier + " · " + q
	}
}

func joinSeasons(seasons []int) string {
	parts := make([]string, 0, len(seasons))
	for _, s := range seasons {
		parts = append(parts, strconv.Itoa(s))
	}
	return strings.Join(parts, ", ")
}

// GrabForSeries resolves a release and hands it to the download client in the series
// category, recorded as a series grab (so seed cleanup manages it like an auto grab).
// scope is the modal it was picked from — the whole show, a season or an episode — and
// limits which episodes skip the import gate.
func (c *Coordinator) GrabForSeries(ctx context.Context, seriesID int64, indexerName, downloadURL, title string, scope GrabScope) error {
	return c.grabForSeries(ctx, seriesID, indexerName, downloadURL, title, true, scope)
}

// GrabForSeriesAuto is the same grab made by the automation rather than the user, so the
// import gate still applies: a sweep must not replace a good file with a worse one.
func (c *Coordinator) GrabForSeriesAuto(ctx context.Context, seriesID int64, indexerName, downloadURL, title string) error {
	return c.grabForSeries(ctx, seriesID, indexerName, downloadURL, title, false, WholeShow)
}

func (c *Coordinator) grabForSeries(ctx context.Context, seriesID int64, indexerName, downloadURL, title string, manual bool, scope GrabScope) error {
	hash, err := c.grabTo(ctx, indexerName, downloadURL, title, seriesCategory)
	if err != nil {
		return err
	}
	if s, err := c.series.Get(ctx, seriesID); err == nil {
		c.recordSeriesGrab(ctx, seriesID, title, indexerName, s.QualityProfile, hash)
	}
	// manual: picked out of the interactive search (or Replace), so the import gate must
	// not second-guess it on score — inside scope. Anything else the release carries is
	// gated like an automatic grab. An automatic whole-show grab is the column defaults,
	// so there's nothing to write.
	if manual || scope != WholeShow {
		c.markGrab(ctx, hash, manual, scope)
	}
	c.series.AddEvent(ctx, seriesID, "grabbed", title+" · "+indexerName)
	return nil
}

// RescanSeries reconciles a series' episode records with what's actually on disk —
// the "rescan" half of Refresh & rescan. It walks the show's real library folder
// (not the derived name, so a differently-named folder still works), marks every
// episode file it finds present (updating moved paths), and clears episodes whose
// file is gone — so deleting a stray/duplicate folder is picked up cleanly.
func (c *Coordinator) RescanSeries(ctx context.Context, seriesID int64) {
	if c.series == nil || c.imp == nil {
		return
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return
	}
	// Before anything reads as missing: a file an interrupted rename left under its
	// temporary name would otherwise be cleared below and downloaded again.
	c.restoreRenameTemps(s)
	folder := c.series.ExistingFolderName(ctx, seriesID) // "" → importer derives the name

	// Group by resolved episode first, rather than marking as we walk. Several files on
	// disk can claim the same episode — three copies of every Solo Leveling season 2
	// episode, say, left by re-imports under different names. Marking inside the walk made
	// the LAST file the directory happened to yield the winner, silently overwrote the
	// others' paths, and reported nothing: the extra copies became invisible to Arrmada
	// while still filling the disk and showing up as duplicates in Plex.
	byEpisode := map[[2]int][]library.FoundVideo{}
	for _, v := range c.imp.SeriesLibraryVideos(folder, s.Title, s.Year) {
		// Run each file through the full resolver: SxxExx, multi-episode, and — for anime
		// — absolute ("S2 29") and per-cour numbering all map onto the metadata's episodes.
		for _, ref := range c.series.ResolveEpisodes(ctx, seriesID, parser.Parse(filepath.Base(v.Path))) {
			key := [2]int{ref.Season, ref.Episode}
			byEpisode[key] = append(byEpisode[key], v)
		}
	}

	found := make(map[[2]int]bool, len(byEpisode))
	dupes := 0
	for key, files := range byEpisode {
		found[key] = true
		best := files[0]
		if len(files) > 1 {
			best = bestLibraryVideo(files)
			dupes += len(files) - 1
			others := make([]string, 0, len(files)-1)
			for _, f := range files {
				if f.Path != best.Path {
					others = append(others, filepath.Base(f.Path))
				}
			}
			c.log.Warn("series rescan: several files claim the same episode — keeping the best one; the rest are duplicates taking up space",
				"series", s.Title, "episode", fmt.Sprintf("S%02dE%02d", key[0], key[1]),
				"keeping", filepath.Base(best.Path), "duplicates", strings.Join(others, ", "))
		}
		_ = c.series.MarkEpisodeImported(ctx, seriesID, key[0], key[1], best.Path, best.Size)
	}
	if dupes > 0 {
		c.log.Warn("series rescan: duplicate episode files on disk", "series", s.Title, "extra_files", dupes)
	}

	// Clear any episode still flagged as having a file that wasn't found on disk and
	// whose stored path no longer exists (e.g. a deleted duplicate season folder).
	for _, sn := range s.Seasons {
		for _, e := range sn.Episodes {
			if !e.HasFile || found[[2]int{e.SeasonNumber, e.EpisodeNumber}] {
				continue
			}
			if e.FilePath == "" || !fileExists(e.FilePath) {
				_ = c.series.MarkEpisodeMissing(ctx, seriesID, e.SeasonNumber, e.EpisodeNumber)
			}
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// SeriesImportCandidate is a video file on disk that can be manually imported into a
// series (season/episode parsed from the filename).
type SeriesImportCandidate struct {
	Path      string `json:"path"`
	Filename  string `json:"filename"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	SizeBytes int64  `json:"size_bytes"`
	Quality   string `json:"quality"`
}

// SeriesImportCandidates lists importable video files under dir (recursively), up to
// library.ListMaxResults; truncated says there were more (or the walk was stopped).
// It stops early when ctx ends — the caller checks ctx.Err().
func (c *Coordinator) SeriesImportCandidates(ctx context.Context, dir string) ([]SeriesImportCandidate, bool) {
	vids, truncated, _ := library.FindVideosCtx(ctx, dir, library.ListMaxResults, library.ListMaxVisited)
	out := make([]SeriesImportCandidate, 0, len(vids))
	for _, v := range vids {
		p := parser.Parse(filepath.Base(v.Path))
		ep := 0
		if len(p.Episodes) > 0 {
			ep = p.Episodes[0]
		}
		out = append(out, SeriesImportCandidate{
			Path: v.Path, Filename: filepath.Base(v.Path),
			Season: p.Season, Episode: ep, SizeBytes: v.Size, Quality: string(p.Resolution),
		})
	}
	return out, truncated
}

// ManualImportSeries imports one on-disk file into a series as its parsed episode.
func (c *Coordinator) ManualImportSeries(ctx context.Context, seriesID int64, path string) error {
	if c.series == nil || c.imp == nil {
		return fmt.Errorf("series module not ready")
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return err
	}
	// A directory means "import this whole download", which runs the full pipeline —
	// quality inherited from the release name, the upgrade gate, multi-episode files —
	// rather than the single-file shortcut below.
	//
	// This is also the way to re-run an import Arrmada has already processed. The
	// automatic sweep won't revisit a download it recorded as imported unless the season
	// still has gaps, so when a fix changes what WOULD have imported, pointing manual
	// import at the folder is what applies it.
	if fi, statErr := os.Stat(path); statErr == nil && fi.IsDir() {
		placedRefs, matched, unresolved, importFailed := c.importSeriesInto(ctx, s, path, forceAll)
		placed := len(placedRefs)
		if matched == 0 {
			return fmt.Errorf("none of the video files in that folder could be matched to an episode of %q", s.Title)
		}
		c.log.Info("series: manual folder import", "series", s.Title, "path", path,
			"placed", placed, "matched", matched, "unresolved", unresolved, "failed", importFailed)
		if placed > 0 {
			c.series.AddEvent(ctx, seriesID, "imported", fmt.Sprintf("Imported %d episode%s from %s", placed, plural(placed), filepath.Base(path)))
			c.seriesImported(ctx, seriesID, placedRefs)
		}
		// If the folder is a download the client still holds, record its hash as
		// handled (when nothing failed) — otherwise the 30s sweep re-scans it, places
		// nothing (everything now equal-or-better), and mis-blocklists the release
		// with "fully imported — no more episodes for this season".
		if importFailed == 0 {
			c.recordHashForContentPath(ctx, path)
		}
		return nil
	}

	folder := c.series.ExistingFolderName(ctx, seriesID)
	ei, ok, err := c.imp.ImportEpisodeInto(folder, s.Title, s.Year, path)
	if err != nil {
		return err
	}
	if !ok {
		// Anime file numbered absolutely (no SxxExx) — resolve + place by absolute number.
		if s.IsAnime() && len(c.importAbsoluteEpisode(ctx, s, folder, path)) > 0 {
			return nil
		}
		return fmt.Errorf("couldn't detect a season/episode from that filename")
	}
	var lastErr error
	for _, ep := range episodesOf(ei) { // double-episode file → mark both
		rs, re := c.series.ResolveEpisode(ctx, seriesID, ei.Season, ep)
		if err := c.series.SupersedeEpisodeFile(ctx, seriesID, rs, re, ei.TargetPath, ei.SizeBytes, filepath.Base(ei.SourcePath)); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// recordHashForContentPath finds the completed download whose content path matches,
// records its hash as imported and closes out its grab as 'imported', so seed cleanup
// removes the torrent at its goal. Used after manual imports, which are given a
// filesystem path rather than a torrent hash.
func (c *Coordinator) recordHashForContentPath(ctx context.Context, path string) {
	if c.downloads == nil {
		return
	}
	completed, err := c.downloads.CompletedInCategory(ctx, "")
	if err != nil {
		return
	}
	clean := filepath.Clean(path)
	for _, it := range completed {
		if it.ContentPath != "" && filepath.Clean(it.ContentPath) == clean {
			c.recordImportedHash(ctx, it.Hash, it.Name, it.SizeBytes)
			c.setGrabStatusByHash(ctx, it.Hash, it.Name, "series", grabStatusImported)
			return
		}
	}
}

// bestLibraryVideo picks which of several files claiming one episode to keep as that
// episode's file: highest resolution first, then the bigger file (higher bitrate at the
// same resolution), then the path — so the choice is deterministic rather than "whichever
// the directory walk yielded last".
func bestLibraryVideo(files []library.FoundVideo) library.FoundVideo {
	best := files[0]
	bestRank := parser.ResolutionRank(parser.Parse(filepath.Base(best.Path)).Resolution)
	for _, f := range files[1:] {
		rank := parser.ResolutionRank(parser.Parse(filepath.Base(f.Path)).Resolution)
		switch {
		case rank != bestRank:
			if rank > bestRank {
				best, bestRank = f, rank
			}
		case f.Size != best.Size:
			if f.Size > best.Size {
				best = f
			}
		case f.Path < best.Path:
			best = f
		}
	}
	return best
}

// DuplicateEpisodeFile is one episode that has more than one file on disk: the copy the
// library tracks, and the extra copies that are just taking up space.
type DuplicateEpisodeFile struct {
	Season  int             `json:"season"`
	Episode int             `json:"episode"`
	Keeping DuplicateCopy   `json:"keeping"`
	Extras  []DuplicateCopy `json:"extras"`
}

// DuplicateCopy is one file on disk claiming an episode.
type DuplicateCopy struct {
	Path      string `json:"path"`
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes"`
}

// SeriesDuplicates reports episodes with more than one file on disk.
//
// These accumulate because the library filename embeds derived metadata — the quality tag,
// and for a while the episode title. When that derivation changes (a source re-classified
// WEBRip vs WEB-DL, an episode title added or dropped) a re-import writes to a NEW path,
// and SupersedeEpisodeFile only recycles the ONE file the database was tracking. Every
// earlier copy is orphaned: invisible to Arrmada, still filling the disk, and showing up as
// a duplicate episode in Plex.
//
// Read-only — it reports what it finds and never deletes anything on its own.
func (c *Coordinator) SeriesDuplicates(ctx context.Context, seriesID int64) ([]DuplicateEpisodeFile, error) {
	if c.series == nil || c.imp == nil {
		return nil, nil
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	folder := c.series.ExistingFolderName(ctx, seriesID)

	byEpisode := map[[2]int][]library.FoundVideo{}
	for _, v := range c.imp.SeriesLibraryVideos(folder, s.Title, s.Year) {
		for _, ref := range c.series.ResolveEpisodes(ctx, seriesID, parser.Parse(filepath.Base(v.Path))) {
			key := [2]int{ref.Season, ref.Episode}
			byEpisode[key] = append(byEpisode[key], v)
		}
	}

	out := make([]DuplicateEpisodeFile, 0)
	for key, files := range byEpisode {
		if len(files) < 2 {
			continue
		}
		best := bestLibraryVideo(files)
		d := DuplicateEpisodeFile{Season: key[0], Episode: key[1], Keeping: copyOf(best)}
		for _, f := range files {
			if f.Path != best.Path {
				d.Extras = append(d.Extras, copyOf(f))
			}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Season != out[j].Season {
			return out[i].Season < out[j].Season
		}
		return out[i].Episode < out[j].Episode
	})
	return out, nil
}

func copyOf(v library.FoundVideo) DuplicateCopy {
	return DuplicateCopy{Path: v.Path, Filename: filepath.Base(v.Path), SizeBytes: v.Size}
}

// DeleteSeriesDuplicate recycles one duplicate file. It refuses any path that isn't
// currently reported as an EXTRA copy for this series, so an arbitrary path — or the file
// the library actually tracks — can never be deleted through this route.
func (c *Coordinator) DeleteSeriesDuplicate(ctx context.Context, seriesID int64, path string) error {
	dupes, err := c.SeriesDuplicates(ctx, seriesID)
	if err != nil {
		return err
	}
	for _, d := range dupes {
		for _, e := range d.Extras {
			if e.Path != path {
				continue
			}
			// Into the bin, or for good only when the bin is deliberately off. A bin that
			// refuses the file leaves it in place and says why.
			if _, err := library.RemoveToBin(c.bin(), path); err != nil {
				return err
			}
			c.log.Info("series: deleted a duplicate episode file", "series", seriesID,
				"episode", fmt.Sprintf("S%02dE%02d", d.Season, d.Episode), "path", path)
			c.series.AddEvent(ctx, seriesID, "file.deleted",
				fmt.Sprintf("Deleted duplicate file for S%02dE%02d: %s", d.Season, d.Episode, filepath.Base(path)))
			return nil
		}
	}
	return fmt.Errorf("that file isn't a duplicate of this series — refusing to delete it")
}

// resolvedLabel says which episodes a release actually maps to, for the diagnostic that
// explains an out-of-scope drop. Best-effort and read-only.
func (c *Coordinator) resolvedLabel(ctx context.Context, s series.Series, p parser.Release) string {
	refs, ok := c.series.AliasEpisodes(ctx, s.ID, p)
	via := "alias"
	if !ok {
		refs, via = c.series.ResolveEpisodes(ctx, s.ID, p), "series numbering"
	}
	if len(refs) == 0 {
		return "no episode could be resolved"
	}
	parts := make([]string, 0, 4)
	for i, ref := range refs {
		if i == 4 {
			parts = append(parts, fmt.Sprintf("+%d more", len(refs)-4))
			break
		}
		parts = append(parts, fmt.Sprintf("S%02dE%02d", ref.Season, ref.Episode))
	}
	return strings.Join(parts, ",") + " (via " + via + ")"
}

// resolvesLabel names the library episode(s) a release maps to, for the search list.
// Terser than resolvedLabel: no "via" attribution, and a range rather than a list, since
// this sits in a row rather than a log line.
func (c *Coordinator) resolvesLabel(ctx context.Context, s series.Series, p parser.Release) string {
	refs, ok := c.series.AliasEpisodes(ctx, s.ID, p)
	if !ok {
		refs = c.series.ResolveEpisodes(ctx, s.ID, p)
	}
	if len(refs) == 0 {
		return ""
	}
	first := refs[0]
	if len(refs) == 1 {
		return fmt.Sprintf("S%02dE%02d", first.Season, first.Episode)
	}
	last := refs[len(refs)-1]
	return fmt.Sprintf("S%02dE%02d–E%02d (%d episodes)", first.Season, first.Episode, last.Episode, len(refs))
}
