package series

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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

	muUnmatched   sync.Mutex
	lastUnmatched []UnmatchedFolder // folders the last scan couldn't identify, for manual pick
}

// UnmatchedFolder is a scanned series folder the scan couldn't confidently
// identify, with the search candidates offered for a manual pick.
type UnmatchedFolder struct {
	Folder     string                  `json:"folder"`
	Title      string                  `json:"title"`
	Year       int                     `json:"year"`
	Candidates []metadata.SeriesResult `json:"candidates"`
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
		SeriesType: detectSeriesType(d),
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
// Absolute numbers are assigned 1..N across the non-special seasons in order, so an
// anime release numbered absolutely resolves to the right (season, episode).
func seasonsFromDetails(d *metadata.SeriesDetails, monitored bool) []Season {
	seasons := make([]Season, 0, len(d.Seasons))
	abs := 0
	for _, sd := range d.Seasons {
		special := sd.SeasonNumber == 0
		sn := Season{
			SeasonNumber: sd.SeasonNumber, Name: sd.Name, Overview: sd.Overview, PosterURL: sd.PosterURL,
			Monitored: monitored && !special,
		}
		for _, ed := range sd.Episodes {
			absNum := 0
			if !special {
				abs++
				absNum = abs
			}
			sn.Episodes = append(sn.Episodes, Episode{
				SeasonNumber: sd.SeasonNumber, EpisodeNumber: ed.EpisodeNumber, Title: ed.Title,
				Overview: ed.Overview, AirDate: ed.AirDate, Runtime: ed.Runtime, StillURL: ed.StillURL,
				AbsoluteNumber: absNum, Monitored: monitored && !special,
			})
		}
		seasons = append(seasons, sn)
	}
	return seasons
}

// detectSeriesType flags a show as anime when TMDB says it's Animation AND its
// original language is Japanese — the same heuristic Sonarr-style tools use. It's
// only a default; the user can override per series.
func detectSeriesType(d *metadata.SeriesDetails) string {
	if d.OriginalLang == "ja" {
		for _, g := range d.Genres {
			if strings.EqualFold(g, "Animation") {
				return SeriesTypeAnime
			}
		}
	}
	return SeriesTypeStandard
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
		// Recompute absolute numbers (fills them in for series added before anime
		// support, and covers any newly-announced episodes).
		if err := s.repo.BackfillAbsolute(ctx, id); err != nil {
			s.log.Warn("series: backfill absolute numbers failed", "series", sr.Title, "err", err)
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

// EpisodeRef is a concrete (season, episode) that a file or release maps to.
type EpisodeRef struct {
	Season  int
	Episode int
}

// ResolveEpisodes maps a parsed release to the concrete (season, episode) pairs it
// covers for THIS series. Standard series use SxxExx as-is. Anime also honors:
//   - absolute numbering: "[Group] Show - 137" → the episode whose absolute number is 137
//   - a positional fallback: a per-cour file "S03E01" whose (3,1) doesn't exist in the
//     metadata resolves to the 1st episode of season 3 (e.g. absolute-numbered E137).
func (s *Service) ResolveEpisodes(ctx context.Context, seriesID int64, rel parser.Release) []EpisodeRef {
	sr, err := s.repo.Get(ctx, seriesID)
	if err != nil {
		return nil
	}
	anime := sr.IsAnime()
	if anime && len(rel.AbsoluteEpisodes) > 0 {
		var out []EpisodeRef
		for _, ab := range rel.AbsoluteEpisodes {
			if se, ep, ok := s.repo.EpisodeByAbsolute(ctx, seriesID, ab); ok {
				out = append(out, EpisodeRef{Season: se, Episode: ep})
			}
		}
		return out
	}
	var out []EpisodeRef
	for _, ep := range rel.Episodes {
		out = append(out, s.resolveSE(ctx, seriesID, rel.Season, ep, anime))
	}
	return out
}

// ResolveEpisode resolves a single (season, episode) from a filename to the concrete
// episode it belongs to — used by rescan/import where episodes are already split out.
func (s *Service) ResolveEpisode(ctx context.Context, seriesID int64, season, episode int) (int, int) {
	sr, err := s.repo.Get(ctx, seriesID)
	ref := s.resolveSE(ctx, seriesID, season, episode, err == nil && sr.IsAnime())
	return ref.Season, ref.Episode
}

func (s *Service) resolveSE(ctx context.Context, seriesID int64, season, episode int, anime bool) EpisodeRef {
	if s.repo.EpisodeExists(ctx, seriesID, season, episode) {
		return EpisodeRef{Season: season, Episode: episode}
	}
	if anime {
		if real, ok := s.repo.NthEpisodeOfSeason(ctx, seriesID, season, episode); ok {
			return EpisodeRef{Season: season, Episode: real}
		}
	}
	return EpisodeRef{Season: season, Episode: episode} // standard: mark as-is (no-op if absent)
}

// SetType overrides a series' numbering type ("standard" | "anime"). Ensures absolute
// numbers exist when switching to anime so matching works immediately.
func (s *Service) SetType(ctx context.Context, id int64, seriesType string) error {
	if seriesType != SeriesTypeStandard && seriesType != SeriesTypeAnime {
		return fmt.Errorf("invalid series type %q", seriesType)
	}
	if err := s.repo.SetSeriesType(ctx, id, seriesType); err != nil {
		return err
	}
	if seriesType == SeriesTypeAnime {
		_ = s.repo.BackfillAbsolute(ctx, id)
	}
	return nil
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

// ExistingFolderName returns the name of the series' existing library folder,
// derived from any episode already on disk ("" if the series has no files yet).
// New episodes are routed into this folder so they don't spawn a duplicate under
// a differently-named "<Title> (<Year>)" path.
func (s *Service) ExistingFolderName(ctx context.Context, seriesID int64) string {
	path, err := s.repo.AnyEpisodeFilePath(ctx, seriesID)
	if err != nil || path == "" {
		return ""
	}
	// path = <root>/<Series Folder>/Season NN/<file>; the series folder is two up.
	return filepath.Base(filepath.Dir(filepath.Dir(path)))
}

// Events returns a series' activity timeline, newest first.
func (s *Service) Events(ctx context.Context, id int64, limit int) ([]Event, error) {
	return s.repo.Events(ctx, id, limit)
}

// ScanResult summarizes a library scan.
type ScanResult struct {
	Imported  int               `json:"imported"`
	Skipped   int               `json:"skipped"`
	Unmatched []UnmatchedFolder `json:"unmatched,omitempty"`
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
			res.Unmatched = append(res.Unmatched, UnmatchedFolder{Folder: e.Name(), Title: rel.Title, Year: rel.Year})
			continue
		}
		match, ok := bestSeriesMatch(results, rel.Title, rel.Year)
		if !ok {
			// No confident match — surface the top candidates for a manual pick.
			res.Unmatched = append(res.Unmatched, UnmatchedFolder{Folder: e.Name(), Title: rel.Title, Year: rel.Year, Candidates: topSeries(results, 6)})
			continue
		}
		if have[match.TMDBID] {
			res.Skipped++
			continue
		}
		if err := s.importSeriesFolder(ctx, videos, match.TMDBID); err != nil {
			res.Unmatched = append(res.Unmatched, UnmatchedFolder{Folder: e.Name(), Title: rel.Title, Year: rel.Year})
			continue
		}
		have[match.TMDBID] = true
		res.Imported++
	}
	s.setLastUnmatched(res.Unmatched)
	return res, nil
}

// importSeriesFolder adds a series by TMDB id (unmonitored, unset profile — an
// adopted library must not auto-upgrade) and marks its episode files present.
func (s *Service) importSeriesFolder(ctx context.Context, videos []library.FoundVideo, tmdbID int) error {
	added, err := s.Add(ctx, tmdbID, "n/a", false)
	if err != nil {
		return err
	}
	marked := 0
	for _, v := range videos {
		p := parser.Parse(filepath.Base(v.Path))
		// ResolveEpisodes handles multi-episode files, and — for anime — absolute
		// numbering and the per-cour positional fallback.
		for _, ref := range s.ResolveEpisodes(ctx, added.ID, p) {
			if s.repo.SetEpisodeFile(ctx, added.ID, ref.Season, ref.Episode, v.Path, v.Size) == nil {
				marked++
			}
		}
	}
	s.AddEvent(ctx, added.ID, "imported", fmt.Sprintf("Found during library scan: %d episode files", marked))
	s.log.Info("series scan: imported", "title", added.Title, "episodes", marked)
	return nil
}

// ImportFolderAs catalogs a specific library folder as the chosen TMDB series —
// the manual pick for a folder the scan couldn't confidently identify.
func (s *Service) ImportFolderAs(ctx context.Context, rootOverride, folder string, tmdbID int) error {
	root := rootOverride
	if root == "" {
		root = s.root
	}
	videos, _ := library.FindVideos(filepath.Join(root, folder))
	if len(videos) == 0 {
		return fmt.Errorf("no episode files found in %q", folder)
	}
	if err := s.importSeriesFolder(ctx, videos, tmdbID); err != nil {
		return err
	}
	s.dropUnmatched(folder)
	return nil
}

func topSeries(results []metadata.SeriesResult, n int) []metadata.SeriesResult {
	if len(results) > n {
		return results[:n]
	}
	return results
}

func (s *Service) setLastUnmatched(u []UnmatchedFolder) {
	s.muUnmatched.Lock()
	s.lastUnmatched = u
	s.muUnmatched.Unlock()
}

// LastUnmatched returns the folders the most recent scan couldn't identify.
func (s *Service) LastUnmatched() []UnmatchedFolder {
	s.muUnmatched.Lock()
	defer s.muUnmatched.Unlock()
	return append([]UnmatchedFolder(nil), s.lastUnmatched...)
}

func (s *Service) dropUnmatched(folder string) {
	s.muUnmatched.Lock()
	defer s.muUnmatched.Unlock()
	out := s.lastUnmatched[:0]
	for _, u := range s.lastUnmatched {
		if u.Folder != folder {
			out = append(out, u)
		}
	}
	s.lastUnmatched = out
}

// bestSeriesMatch resolves a scanned folder to a search result, requiring a
// confident match (exact normalized title, optionally confirmed by year). It
// returns ok=false instead of falling back to the most popular hit, which is what
// mis-filed "UNTAMED" (2025) as "The Untamed" (2019).
func bestSeriesMatch(results []metadata.SeriesResult, title string, year int) (metadata.SeriesResult, bool) {
	return metadata.TitleYearMatch(results, title, year,
		func(r metadata.SeriesResult) string { return r.Title },
		func(r metadata.SeriesResult) int { return r.Year })
}

// MarkEpisodeImported records an imported episode file and logs it.
func (s *Service) MarkEpisodeImported(ctx context.Context, seriesID int64, season, episode int, path string, size int64) error {
	if err := s.repo.SetEpisodeFile(ctx, seriesID, season, episode, path, size); err != nil {
		return err
	}
	s.log.Info("series: episode imported", "series_id", seriesID, "s", season, "e", episode)
	return nil
}

// MarkEpisodeMissing flips an episode back to wanted (no file on disk) — used by
// rescan to reconcile episodes whose file was deleted or moved away.
func (s *Service) MarkEpisodeMissing(ctx context.Context, seriesID int64, season, episode int) error {
	return s.repo.ClearEpisodeFile(ctx, seriesID, season, episode)
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

// EpisodeTitle returns the metadata title for one episode ("" when unknown).
func (s *Service) EpisodeTitle(ctx context.Context, seriesID int64, season, episode int) string {
	return s.repo.EpisodeTitle(ctx, seriesID, season, episode)
}

// EpisodeTitleByName resolves a series by (normalized) title and returns one episode's
// title. Used by the importer's naming callback, which only knows the show by name.
func (s *Service) EpisodeTitleByName(ctx context.Context, seriesTitle string, year, season, episode int) string {
	sr, ok := s.MatchByTitle(ctx, normKey(seriesTitle))
	if !ok {
		return ""
	}
	return s.repo.EpisodeTitle(ctx, sr.ID, season, episode)
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
	// Keep the romaji/original title only when it differs from the display title, so
	// anime searches can also query the name releases are actually tagged with.
	if d.OriginalName != "" && !strings.EqualFold(d.OriginalName, d.Title) {
		ex.OriginalTitle = d.OriginalName
	}
	for _, c := range d.Cast {
		ex.Cast = append(ex.Cast, CastMember{Name: c.Name, Character: c.Character, ProfileURL: c.ProfileURL})
	}
	return ex
}
