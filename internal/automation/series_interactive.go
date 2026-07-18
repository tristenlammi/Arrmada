package automation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// RankSeriesReleases runs an interactive search for a series, optionally scoped to a
// season (>0) and episode (>0), scoring every relevant release against the series'
// quality profile and returning them ranked best-first — WITHOUT grabbing. This is
// the manual "search indexers" backend, shared by the season- and episode-level UI.
func (c *Coordinator) RankSeriesReleases(ctx context.Context, seriesID int64, season, episode int) (ReleaseList, error) {
	if c.series == nil {
		return ReleaseList{}, nil
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return ReleaseList{}, err
	}
	query := s.Title
	// Anime is released under many numbering conventions (absolute "- 137", per-cour
	// SxxExx, or a split-season S02) — a narrow query would miss most. Search broad by
	// title and let the resolver-backed scope filter pick releases covering the episode.
	if !s.IsAnime() {
		switch {
		case season > 0 && episode > 0:
			query = fmt.Sprintf("%s S%02dE%02d", s.Title, season, episode)
		case season > 0:
			query = fmt.Sprintf("%s S%02d", s.Title, season)
		}
	}
	result, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: query, MediaType: indexer.MediaSeries, Limit: 400})
	if err != nil {
		return ReleaseList{}, err
	}
	c.log.Info("series: interactive search", "series", s.Title, "query", query, "raw", len(result.Releases), "indexer_errors", len(result.Errors))
	for name, e := range result.Errors {
		c.log.Warn("series: indexer error", "indexer", name, "err", e)
	}

	byName := make(map[string]indexer.Release, len(result.Releases))
	cands := make([]quality.Candidate, 0, len(result.Releases))
	var droppedTitle, droppedScope int
	var sampleDropped []string
	for _, rel := range result.Releases {
		if _, dup := byName[rel.Title]; dup {
			continue
		}
		if !seriesTitleMatches(rel.Title, s) {
			droppedTitle++
			if len(sampleDropped) < 8 {
				sampleDropped = append(sampleDropped, rel.Title+" → "+parser.Parse(rel.Title).Title)
			}
			continue // a different show that merely shares a title prefix (e.g. "Below Deck Mediterranean" for "Below Deck")
		}
		if !c.releaseMatchesScope(ctx, s, parser.Parse(rel.Title), season, episode) {
			droppedScope++
			continue // not relevant to the requested season/episode scope
		}
		byName[rel.Title] = rel
		cands = append(cands, quality.NewCandidate(rel.Title, rel.SizeGB(), rel.Seeders))
	}
	c.log.Info("series: search filtered", "series", s.Title, "kept", len(cands), "dropped_wrong_title", droppedTitle, "dropped_out_of_scope", droppedScope)
	if len(sampleDropped) > 0 {
		c.log.Info("series: sample of dropped titles (parsed → title)", "series", s.Title, "samples", strings.Join(sampleDropped, " | "))
	}
	decision := c.quality.Decide(ctx, s.QualityProfile, cands)

	// For a single-episode search we can show a bitrate (size ÷ episode runtime). Season/series
	// packs cover many episodes, so leave bitrate off there rather than mislead.
	epRuntime := 0
	if season > 0 && episode > 0 {
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
		})
	}
	for _, ev := range decision.Eligible {
		appendEval(ev)
	}
	for _, ev := range decision.Rejected {
		appendEval(ev)
	}
	return ReleaseList{Profile: s.QualityProfile, Why: decision.Why, Releases: out}, nil
}

// seriesReleaseMatches reports whether a release is relevant to a season/episode
// scope. season<=0 → any TV release; season set / episode<=0 → covers that season;
// both set → the exact episode, or a pack that covers it.
// releaseMatchesScope is seriesReleaseMatches with anime awareness: an episode-scope
// anime release is resolved through absolute/positional numbering before checking it
// covers the requested (season, episode).
func (c *Coordinator) releaseMatchesScope(ctx context.Context, s series.Series, p parser.Release, season, episode int) bool {
	if !s.IsAnime() {
		return seriesReleaseMatches(p, season, episode)
	}
	if season <= 0 {
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

func seriesReleaseMatches(p parser.Release, season, episode int) bool {
	if season <= 0 {
		return p.IsTV()
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
func (c *Coordinator) GrabForSeries(ctx context.Context, seriesID int64, indexerName, downloadURL, title string) error {
	if err := c.grabTo(ctx, indexerName, downloadURL, title, seriesCategory); err != nil {
		return err
	}
	if s, err := c.series.Get(ctx, seriesID); err == nil {
		c.recordSeriesGrab(ctx, seriesID, title, indexerName, s.QualityProfile)
	}
	c.series.AddEvent(ctx, seriesID, "grabbed", title+" · "+indexerName)
	return nil
}

// GrabBestForScope auto-grabs the best eligible release for a season/episode scope —
// the per-episode / per-season "grab" quick action.
func (c *Coordinator) GrabBestForScope(ctx context.Context, seriesID int64, season, episode int) error {
	list, err := c.RankSeriesReleases(ctx, seriesID, season, episode)
	if err != nil {
		return err
	}
	for _, rel := range list.Releases {
		if rel.Eligible {
			return c.GrabForSeries(ctx, seriesID, rel.Indexer, rel.DownloadURL, rel.Title)
		}
	}
	return fmt.Errorf("no eligible release found for that %s", scopeLabel(season, episode))
}

func scopeLabel(season, episode int) string {
	switch {
	case season > 0 && episode > 0:
		return fmt.Sprintf("S%02dE%02d", season, episode)
	case season > 0:
		return fmt.Sprintf("season %d", season)
	default:
		return "series"
	}
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
	folder := c.series.ExistingFolderName(ctx, seriesID) // "" → importer derives the name

	found := map[[2]int]bool{}
	for _, v := range c.imp.SeriesLibraryVideos(folder, s.Title, s.Year) {
		// Run each file through the full resolver: SxxExx, multi-episode, and — for anime
		// — absolute ("S2 29") and per-cour numbering all map onto the metadata's episodes.
		for _, ref := range c.series.ResolveEpisodes(ctx, seriesID, parser.Parse(filepath.Base(v.Path))) {
			found[[2]int{ref.Season, ref.Episode}] = true
			_ = c.series.MarkEpisodeImported(ctx, seriesID, ref.Season, ref.Episode, v.Path, v.Size)
		}
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

// SeriesImportCandidates lists importable video files under dir (recursively).
func (c *Coordinator) SeriesImportCandidates(dir string) []SeriesImportCandidate {
	vids, _ := library.FindVideos(dir)
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
	return out
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
	folder := c.series.ExistingFolderName(ctx, seriesID)
	ei, ok, err := c.imp.ImportEpisodeInto(folder, s.Title, s.Year, path)
	if err != nil {
		return err
	}
	if !ok {
		// Anime file numbered absolutely (no SxxExx) — resolve + place by absolute number.
		if s.IsAnime() && c.importAbsoluteEpisode(ctx, s, folder, path) > 0 {
			return nil
		}
		return fmt.Errorf("couldn't detect a season/episode from that filename")
	}
	var lastErr error
	for _, ep := range episodesOf(ei) { // double-episode file → mark both
		rs, re := c.series.ResolveEpisode(ctx, seriesID, ei.Season, ep)
		if err := c.series.SupersedeEpisodeFile(ctx, seriesID, rs, re, ei.TargetPath, ei.SizeBytes); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// SeriesRenameItem is one proposed episode-file rename.
type SeriesRenameItem struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// SeriesRenamePreview computes which episode files aren't at their canonical library
// path yet. SeriesRename applies the moves and updates the stored paths.
func (c *Coordinator) SeriesRenamePreview(ctx context.Context, seriesID int64) ([]SeriesRenameItem, error) {
	if c.series == nil || c.imp == nil {
		return nil, nil
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	folder := c.series.ExistingFolderName(ctx, seriesID)
	var items []SeriesRenameItem
	for _, sn := range s.Seasons {
		for _, e := range sn.Episodes {
			if !e.HasFile || e.FilePath == "" {
				continue
			}
			target := c.imp.EpisodeTargetIn(folder, s.Title, s.Year, e.SeasonNumber, e.EpisodeNumber, filepath.Base(e.FilePath), filepath.Ext(e.FilePath))
			if target != "" && target != e.FilePath {
				items = append(items, SeriesRenameItem{From: e.FilePath, To: target})
			}
		}
	}
	return items, nil
}

// SeriesRename renames episode files to the canonical scheme, returning how many moved.
func (c *Coordinator) SeriesRename(ctx context.Context, seriesID int64) (int, error) {
	if c.series == nil || c.imp == nil {
		return 0, fmt.Errorf("series module not ready")
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return 0, err
	}
	folder := c.series.ExistingFolderName(ctx, seriesID)
	moved := 0
	oldDirs := map[string]bool{} // season folders emptied by the moves, to prune after
	for _, sn := range s.Seasons {
		for _, e := range sn.Episodes {
			if !e.HasFile || e.FilePath == "" {
				continue
			}
			target := c.imp.EpisodeTargetIn(folder, s.Title, s.Year, e.SeasonNumber, e.EpisodeNumber, filepath.Base(e.FilePath), filepath.Ext(e.FilePath))
			if target == "" || target == e.FilePath {
				continue
			}
			if err := c.imp.Move(e.FilePath, target); err != nil {
				c.log.Warn("series: rename failed", "from", e.FilePath, "err", err)
				continue
			}
			c.imp.MoveEpisodeSubs(e.FilePath, target) // keep paired subtitles alongside
			if od := filepath.Dir(e.FilePath); od != filepath.Dir(target) {
				oldDirs[od] = true // a season folder that changed name (e.g. "Season 04" → "Season 4")
			}
			_ = c.series.MarkEpisodeImported(ctx, seriesID, e.SeasonNumber, e.EpisodeNumber, target, e.SizeBytes)
			moved++
		}
	}
	for od := range oldDirs {
		c.imp.RemoveDirIfEmpty(od) // drop the now-empty legacy season folder
	}
	if moved > 0 {
		c.series.AddEvent(ctx, seriesID, "renamed", fmt.Sprintf("Renamed %d episode file%s", moved, plural(moved)))
		c.bus.Publish("series.renamed", map[string]any{"id": seriesID, "count": moved})
	}
	return moved, nil
}
