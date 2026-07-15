package series

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/parser"
)

// Service is the Series module's application logic.
type Service struct {
	repo    *Repo
	meta    metadata.SeriesProvider
	root    string // library root, for delete-with-files and library scan
	recycle string // recycle-bin dir ("" = hard delete), matching movies
	log     *slog.Logger
}

// SetRecycleDir points episode-file deletion at the recycle bin (matching movies).
func (s *Service) SetRecycleDir(dir string) { s.recycle = dir }

// DeleteEpisodeFile removes one episode's file (to the recycle bin when configured) and flips
// the episode back to wanted, without touching the rest of the show.
func (s *Service) DeleteEpisodeFile(ctx context.Context, seriesID int64, season, episode int) error {
	path, err := s.repo.EpisodeFilePath(ctx, seriesID, season, episode)
	if err != nil {
		return err
	}
	if path != "" {
		if s.recycle != "" {
			if _, rerr := library.RecycleFile(s.recycle, path); rerr != nil {
				s.log.Warn("series: recycle episode file failed, hard-deleting", "path", path, "err", rerr)
				_ = os.Remove(path)
			}
		} else {
			_ = os.Remove(path)
		}
	}
	if err := s.repo.ClearEpisodeFile(ctx, seriesID, season, episode); err != nil {
		return err
	}
	s.repo.AddEvent(ctx, seriesID, "file.deleted", fmt.Sprintf("S%02dE%02d file deleted", season, episode))
	return nil
}

// NewService wires the module. root is the library directory (for scan / delete-files).
func NewService(db *sql.DB, meta metadata.SeriesProvider, root string, log *slog.Logger) *Service {
	return &Service{repo: NewRepo(db), meta: meta, root: root, log: log}
}

// MetadataAvailable reports whether the metadata provider is configured.
func (s *Service) MetadataAvailable() bool { return s.meta.Available() }

// Lookup searches the metadata provider for series to add.
func (s *Service) Lookup(ctx context.Context, query string) ([]metadata.SeriesResult, error) {
	return s.meta.SearchSeries(ctx, query)
}

// List returns the library with roll-up stats.
func (s *Service) List(ctx context.Context) ([]Series, error) { return s.repo.List(ctx) }

// Get returns one series with its seasons and episodes.
func (s *Service) Get(ctx context.Context, id int64) (Series, error) {
	sr, err := s.repo.Get(ctx, id)
	if err != nil {
		return Series{}, err
	}
	if seasons, err := s.repo.SeasonsFor(ctx, id); err == nil {
		sr.Seasons = seasons
	}
	return sr, nil
}

// Add pulls full metadata for a TMDB series id and adds it — series row plus every
// season and episode. Specials (season 0) are added unmonitored by default.
func (s *Service) Add(ctx context.Context, tmdbID int, qualityProfile string, monitored bool) (Series, error) {
	d, err := s.meta.GetSeries(ctx, tmdbID)
	if err != nil {
		return Series{}, fmt.Errorf("fetch metadata: %w", err)
	}
	sr := Series{
		TMDBID: d.TMDBID, IMDBID: d.IMDBID, Title: d.Title, Year: d.Year, Overview: d.Overview,
		PosterURL: d.PosterURL, Status: d.Status, Network: d.Network,
		Monitored: monitored, QualityProfile: qualityProfile, Extra: extraFrom(d),
	}
	created, err := s.repo.Create(ctx, sr)
	if err != nil {
		return Series{}, err
	}
	seasons := seasonsFromDetails(d, monitored)
	if err := s.repo.InsertSeasons(ctx, created.ID, seasons); err != nil {
		s.log.Warn("series: insert seasons failed", "series", created.Title, "err", err)
	}
	s.AddEvent(ctx, created.ID, "added", fmt.Sprintf("Added — %d seasons", len(seasons)))
	s.log.Info("series added", "title", created.Title, "year", created.Year, "seasons", len(seasons))
	return created, nil
}

// seasonsFromDetails projects TMDB season/episode metadata into storage rows.
// Specials (season 0) default unmonitored; everything else follows the series flag.
func seasonsFromDetails(d *metadata.SeriesDetails, monitored bool) []Season {
	seasons := make([]Season, 0, len(d.Seasons))
	for _, sd := range d.Seasons {
		special := sd.SeasonNumber == 0
		sn := Season{
			SeasonNumber: sd.SeasonNumber, Name: sd.Name, Overview: sd.Overview, PosterURL: sd.PosterURL,
			Monitored: monitored && !special,
		}
		for _, ed := range sd.Episodes {
			sn.Episodes = append(sn.Episodes, Episode{
				SeasonNumber: sd.SeasonNumber, EpisodeNumber: ed.EpisodeNumber, Title: ed.Title,
				Overview: ed.Overview, AirDate: ed.AirDate, Runtime: ed.Runtime, StillURL: ed.StillURL,
				Monitored: monitored && !special,
			})
		}
		seasons = append(seasons, sn)
	}
	return seasons
}

// Refresh re-pulls TMDB metadata for a series, adding any newly-announced seasons or
// episodes. Existing rows are left untouched (INSERT OR IGNORE), so monitor/file state
// is preserved; only genuinely new episodes appear.
func (s *Service) Refresh(ctx context.Context, id int64) (Series, error) {
	sr, err := s.repo.Get(ctx, id)
	if err != nil {
		return Series{}, err
	}
	if d, derr := s.meta.GetSeries(ctx, sr.TMDBID); derr == nil {
		if err := s.repo.InsertSeasons(ctx, id, seasonsFromDetails(d, sr.Monitored)); err != nil {
			s.log.Warn("series: refresh insert seasons failed", "series", sr.Title, "err", err)
		}
	} else {
		s.log.Warn("series: refresh metadata failed", "series", sr.Title, "err", derr)
	}
	return s.Get(ctx, id)
}

// SetMonitored toggles a series.
func (s *Service) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	return s.repo.SetMonitored(ctx, id, monitored)
}

// SetSeasonMonitored toggles a whole season and its episodes.
func (s *Service) SetSeasonMonitored(ctx context.Context, seriesID, seasonNumber int64, monitored bool) error {
	return s.repo.SetSeasonMonitored(ctx, seriesID, seasonNumber, monitored)
}

// SetEpisodeMonitored toggles a single episode.
func (s *Service) SetEpisodeMonitored(ctx context.Context, episodeID int64, monitored bool) error {
	return s.repo.SetEpisodeMonitored(ctx, episodeID, monitored)
}

// SetQualityProfile changes a series' quality profile.
func (s *Service) SetQualityProfile(ctx context.Context, id int64, profile string) error {
	return s.repo.SetQualityProfile(ctx, id, profile)
}

// Delete removes a series. When deleteFiles is set, its episode files are removed
// from disk first (and now-empty season/series folders pruned).
func (s *Service) Delete(ctx context.Context, id int64, deleteFiles bool) error {
	if deleteFiles {
		if seasons, err := s.repo.SeasonsFor(ctx, id); err == nil {
			s.removeEpisodeFiles(seasons)
		}
	}
	return s.repo.Delete(ctx, id)
}

// removeEpisodeFiles deletes each episode file on disk and prunes emptied folders
// (deepest first, so a season dir is removed before its series dir).
func (s *Service) removeEpisodeFiles(seasons []Season) {
	dirs := map[string]bool{}
	for _, sn := range seasons {
		for _, e := range sn.Episodes {
			if !e.HasFile || e.FilePath == "" {
				continue
			}
			if err := os.Remove(e.FilePath); err != nil && !os.IsNotExist(err) {
				s.log.Warn("series: delete file failed", "path", e.FilePath, "err", err)
				continue
			}
			dirs[filepath.Dir(e.FilePath)] = true               // season folder
			dirs[filepath.Dir(filepath.Dir(e.FilePath))] = true // series folder
		}
	}
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		ordered = append(ordered, d)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, d := range ordered {
		_ = os.Remove(d) // only succeeds when empty — best effort
	}
}

// AddEvent appends a timeline event for a series.
func (s *Service) AddEvent(ctx context.Context, id int64, event, detail string) {
	s.repo.AddEvent(ctx, id, event, detail)
}

// EpisodeFilePath returns one episode's on-disk file path ("" if none).
func (s *Service) EpisodeFilePath(ctx context.Context, seriesID int64, season, episode int) (string, error) {
	return s.repo.EpisodeFilePath(ctx, seriesID, season, episode)
}

// Events returns a series' activity timeline, newest first.
func (s *Service) Events(ctx context.Context, id int64, limit int) ([]Event, error) {
	return s.repo.Events(ctx, id, limit)
}

// ScanResult summarizes a library scan.
type ScanResult struct {
	Imported  int      `json:"imported"`
	Skipped   int      `json:"skipped"`
	Unmatched []string `json:"unmatched,omitempty"`
}

// ScanLibrary finds series already in the library folder and catalogs them: each
// top-level folder is matched to TMDB, added unmonitored with an unset profile (so the
// existing 1000-episode library isn't auto-upgraded), and its episode files marked
// present. Mirrors the movie library scan.
func (s *Service) ScanLibrary(ctx context.Context, rootOverride string) (ScanResult, error) {
	var res ScanResult
	if !s.meta.Available() {
		return res, fmt.Errorf("series metadata isn't configured — set ARRMADA_TMDB_API_KEY")
	}
	root := rootOverride
	if root == "" {
		root = s.root
	}
	if root == "" {
		return res, fmt.Errorf("no library directory configured")
	}
	existing, err := s.repo.List(ctx)
	if err != nil {
		return res, err
	}
	have := make(map[int]bool, len(existing))
	for _, sr := range existing {
		have[sr.TMDBID] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return res, err
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "" || e.Name()[0] == '.' {
			continue // series live in per-show folders; skip .recycle etc.
		}
		folder := filepath.Join(root, e.Name())
		videos, _ := library.FindVideos(folder)
		if len(videos) == 0 {
			continue // no episode files here
		}
		rel := parser.Parse(e.Name())
		if rel.Title == "" {
			continue
		}
		results, err := s.meta.SearchSeries(ctx, rel.Title)
		if err != nil || len(results) == 0 {
			res.Unmatched = append(res.Unmatched, e.Name())
			continue
		}
		match := bestSeriesMatch(results, rel.Year)
		if have[match.TMDBID] {
			res.Skipped++
			continue
		}
		// Add unmonitored + unset profile — an adopted library must not auto-upgrade.
		added, err := s.Add(ctx, match.TMDBID, "n/a", false)
		if err != nil {
			res.Unmatched = append(res.Unmatched, e.Name())
			continue
		}
		marked := 0
		for _, v := range videos {
			p := parser.Parse(filepath.Base(v.Path))
			if p.Season == 0 || len(p.Episodes) == 0 {
				continue
			}
			if s.repo.SetEpisodeFile(ctx, added.ID, p.Season, p.Episodes[0], v.Path, v.Size) == nil {
				marked++
			}
		}
		have[match.TMDBID] = true
		res.Imported++
		s.AddEvent(ctx, added.ID, "imported", fmt.Sprintf("Found during library scan: %d episode files", marked))
		s.log.Info("series scan: imported", "title", added.Title, "episodes", marked)
	}
	return res, nil
}

// bestSeriesMatch prefers a result whose first-air year matches (±1), else the top hit.
func bestSeriesMatch(results []metadata.SeriesResult, year int) metadata.SeriesResult {
	if year > 0 {
		for _, r := range results {
			if r.Year == year {
				return r
			}
		}
	}
	return results[0]
}

// MarkEpisodeImported records an imported episode file and logs it.
func (s *Service) MarkEpisodeImported(ctx context.Context, seriesID int64, season, episode int, path string, size int64) error {
	if err := s.repo.SetEpisodeFile(ctx, seriesID, season, episode, path, size); err != nil {
		return err
	}
	s.log.Info("series: episode imported", "series_id", seriesID, "s", season, "e", episode)
	return nil
}

// MatchByTitle finds a series whose normalized title matches (for import routing).
func (s *Service) MatchByTitle(ctx context.Context, normalized string) (Series, bool) {
	all, err := s.repo.List(ctx)
	if err != nil {
		return Series{}, false
	}
	for _, sr := range all {
		if normKey(sr.Title) == normalized {
			return sr, true
		}
	}
	return Series{}, false
}

// normKey lowercases and keeps only alphanumerics — for tolerant title matching.
func normKey(str string) string {
	var b []rune
	for _, r := range str {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			if r >= 'A' && r <= 'Z' {
				r += 32
			}
			b = append(b, r)
		}
	}
	return string(b)
}

// NormTitle exposes the title-normalization used for matching.
func NormTitle(s string) string { return normKey(s) }

// extraFrom projects metadata into the stored extra blob.
func extraFrom(d *metadata.SeriesDetails) *SeriesExtra {
	ex := &SeriesExtra{Genres: d.Genres, BackdropURL: d.BackdropURL}
	for _, c := range d.Cast {
		ex.Cast = append(ex.Cast, CastMember{Name: c.Name, Character: c.Character, ProfileURL: c.ProfileURL})
	}
	return ex
}
