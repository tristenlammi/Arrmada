package series

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/xem"
)

// SceneMapper fetches a TheXEM scene→absolute map for a TVDB id (keyed "season-episode").
type SceneMapper interface {
	Fetch(ctx context.Context, tvdbID int) (map[string]int, error)
}

// Service is the Series module's application logic.
type Service struct {
	repo    *Repo
	meta    metadata.SeriesProvider
	root    string // library root, for delete-with-files and library scan
	recycle string // recycle-bin dir ("" = hard delete), matching movies
	log     *slog.Logger
	scene   SceneMapper // TheXEM client (nil → scene mapping falls back to air-date gaps)

	muUnmatched   sync.Mutex
	lastUnmatched []UnmatchedFolder // folders the last scan couldn't identify, for manual pick

	sceneMu    sync.Mutex
	sceneCache map[int64]map[string]int // series id → scene "S-E" → absolute (in-memory)
}

// SetSceneMapper installs the TheXEM client used to reconcile split-season anime.
func (s *Service) SetSceneMapper(m SceneMapper) { s.scene = m }

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
		TMDBID: d.TMDBID, TVDBID: d.TVDBID, IMDBID: d.IMDBID, Title: d.Title, Year: d.Year, Overview: d.Overview,
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
	if created.IsAnime() {
		s.refreshSceneMap(ctx, created.ID, d.TVDBID) // TheXEM scene mapping for split-season anime
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
		if d.TVDBID > 0 && d.TVDBID != sr.TVDBID {
			_ = s.repo.SetTVDBID(ctx, id, d.TVDBID)
		}
		// Refresh the TheXEM scene map for anime, so split-season releases resolve.
		if sr.IsAnime() || detectSeriesType(d) == SeriesTypeAnime {
			s.refreshSceneMap(ctx, id, d.TVDBID)
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
		// Per-cour positional: the season exists but is numbered absolutely.
		if real, ok := s.repo.NthEpisodeOfSeason(ctx, seriesID, season, episode); ok {
			return EpisodeRef{Season: season, Episode: real}
		}
		// TheXEM scene mapping (authoritative): scene (season, episode) → absolute → TMDB.
		if abs, ok := s.sceneAbsolute(ctx, seriesID, season, episode); ok {
			if se, ep, ok := s.repo.EpisodeByAbsolute(ctx, seriesID, abs); ok {
				return EpisodeRef{Season: se, Episode: ep}
			}
		}
		// Air-date-gap fallback: split a TMDB single season at broadcast hiatuses.
		if rs, re, ok := s.resolveSceneSeason(ctx, seriesID, season, episode); ok {
			return EpisodeRef{Season: rs, Episode: re}
		}
	}
	return EpisodeRef{Season: season, Episode: episode} // standard: mark as-is (no-op if absent)
}

// HasSeason reports whether TMDB gives this series the given season number.
func (s *Service) HasSeason(ctx context.Context, seriesID int64, season int) bool {
	return s.repo.SeasonExists(ctx, seriesID, season)
}

// SceneSeasonEpisodes returns every TMDB (season, episode) that a scene season maps to
// — used to match a whole split-season pack ("Dragon Ball Super S02" or "Frieren S02").
// Prefers TheXEM (authoritative); falls back to air-date-gap grouping.
func (s *Service) SceneSeasonEpisodes(ctx context.Context, seriesID int64, sceneSeason int) []EpisodeRef {
	if sceneSeason < 1 {
		return nil
	}
	if m := s.sceneMapFor(ctx, seriesID); len(m) > 0 {
		prefix := fmt.Sprintf("%d-", sceneSeason)
		var out []EpisodeRef
		for k, abs := range m {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			if se, ep, ok := s.repo.EpisodeByAbsolute(ctx, seriesID, abs); ok {
				out = append(out, EpisodeRef{Season: se, Episode: ep})
			}
		}
		if len(out) > 0 {
			sort.Slice(out, func(i, j int) bool {
				if out[i].Season != out[j].Season {
					return out[i].Season < out[j].Season
				}
				return out[i].Episode < out[j].Episode
			})
			return out
		}
	}
	groups := s.sceneGroups(ctx, seriesID, sceneSeason)
	if sceneSeason > len(groups) {
		return nil
	}
	var out []EpisodeRef
	for _, e := range groups[sceneSeason-1] {
		out = append(out, EpisodeRef{Season: e.season, Episode: e.episode})
	}
	return out
}

// refreshSceneMap fetches the TheXEM scene→absolute map for an anime and caches it (DB +
// in-memory). Best-effort: a missing map or a TheXEM outage just leaves the fallback.
func (s *Service) refreshSceneMap(ctx context.Context, seriesID int64, tvdbID int) {
	if s.scene == nil || tvdbID <= 0 {
		return
	}
	m, err := s.scene.Fetch(ctx, tvdbID)
	if err != nil {
		s.log.Warn("series: TheXEM fetch failed", "tvdb", tvdbID, "err", err)
		return
	}
	b, _ := json.Marshal(m)
	_ = s.repo.SetSceneMap(ctx, seriesID, string(b), time.Now().Unix())
	s.sceneMu.Lock()
	if s.sceneCache == nil {
		s.sceneCache = map[int64]map[string]int{}
	}
	s.sceneCache[seriesID] = m
	s.sceneMu.Unlock()
	s.log.Info("series: TheXEM scene map cached", "series_id", seriesID, "tvdb", tvdbID, "entries", len(m))
}

// sceneAbsolute returns the absolute episode number for a scene (season, episode) from
// the cached TheXEM map. ok=false when there's no mapping.
func (s *Service) sceneAbsolute(ctx context.Context, seriesID int64, season, episode int) (int, bool) {
	m := s.sceneMapFor(ctx, seriesID)
	if len(m) == 0 {
		return 0, false
	}
	abs, ok := m[xem.Key(season, episode)]
	return abs, ok
}

// sceneMapFor returns a series' scene→absolute map, loading it from the DB into an
// in-memory cache on first use (nil is cached too, to avoid re-querying).
func (s *Service) sceneMapFor(ctx context.Context, seriesID int64) map[string]int {
	s.sceneMu.Lock()
	defer s.sceneMu.Unlock()
	if s.sceneCache == nil {
		s.sceneCache = map[int64]map[string]int{}
	}
	if m, ok := s.sceneCache[seriesID]; ok {
		return m
	}
	var m map[string]int
	if j := s.repo.SceneMap(ctx, seriesID); j != "" {
		_ = json.Unmarshal([]byte(j), &m)
	}
	s.sceneCache[seriesID] = m
	return m
}

// resolveSceneSeason maps a split-season release's (season, episode) to the continuous
// TMDB numbering by inferring scene seasons from air-date gaps.
func (s *Service) resolveSceneSeason(ctx context.Context, seriesID int64, sceneSeason, sceneEpisode int) (int, int, bool) {
	if sceneSeason < 1 || sceneEpisode < 1 {
		return 0, 0, false
	}
	groups := s.sceneGroups(ctx, seriesID, sceneSeason)
	if sceneSeason > len(groups) {
		return 0, 0, false
	}
	g := groups[sceneSeason-1]
	if sceneEpisode > len(g) {
		return 0, 0, false
	}
	return g[sceneEpisode-1].season, g[sceneEpisode-1].episode, true
}

// minSceneGapDays is the smallest air-date gap treated as a season boundary — big
// enough to ignore weekly cadence and cour breaks-within-a-season, small enough to
// catch a real broadcast-season split.
const minSceneGapDays = 30

// sceneGroups splits a series' continuous episode list into k contiguous groups at the
// k-1 largest air-date gaps — the broadcast/scene seasons that torrents number by.
// Returns nil when there aren't k-1 real season breaks (so a genuinely continuous show
// mislabeled "S2" doesn't get a bogus mapping).
func (s *Service) sceneGroups(ctx context.Context, seriesID int64, k int) [][]epAir {
	eps := s.repo.OrderedEpisodes(ctx, seriesID)
	if len(eps) == 0 || k < 1 {
		return nil
	}
	if k == 1 {
		return [][]epAir{eps}
	}
	type gap struct{ idx, days int }
	gaps := make([]gap, 0, len(eps))
	for i := 0; i+1 < len(eps); i++ {
		gaps = append(gaps, gap{i, daysBetween(eps[i].airDate, eps[i+1].airDate)})
	}
	sort.Slice(gaps, func(a, b int) bool { return gaps[a].days > gaps[b].days })
	cuts := map[int]bool{}
	for i := 0; i < k-1; i++ {
		if i >= len(gaps) || gaps[i].days < minSceneGapDays {
			return nil // not enough real season breaks to form k groups
		}
		cuts[gaps[i].idx] = true
	}
	var groups [][]epAir
	var cur []epAir
	for i, e := range eps {
		cur = append(cur, e)
		if cuts[i] {
			groups = append(groups, cur)
			cur = nil
		}
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	if len(groups) != k {
		return nil
	}
	return groups
}

// daysBetween returns the absolute day count between two "YYYY-MM-DD" dates (0 when
// either is missing/unparseable, so unknown dates never form a season boundary).
func daysBetween(a, b string) int {
	ta, ea := time.Parse("2006-01-02", a)
	tb, eb := time.Parse("2006-01-02", b)
	if ea != nil || eb != nil {
		return 0
	}
	d := int(tb.Sub(ta).Hours() / 24)
	if d < 0 {
		return -d
	}
	return d
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
		if sr, err := s.repo.Get(ctx, id); err == nil {
			s.refreshSceneMap(ctx, id, sr.TVDBID) // pull the TheXEM map now that it's anime
		}
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

// SupersedeEpisodeFile records a newly-imported file and recycles the episode's
// previous file when it lived at a DIFFERENT path — an upgrade (better quality) or a
// naming change. Without this, the new file lands at a new name and the old one is
// left orphaned on disk, so every upgrade/re-grab leaves a duplicate behind.
func (s *Service) SupersedeEpisodeFile(ctx context.Context, seriesID int64, season, episode int, path string, size int64) error {
	if old, _ := s.repo.EpisodeFilePath(ctx, seriesID, season, episode); old != "" && old != path {
		if _, err := os.Stat(old); err == nil {
			if s.recycle != "" {
				if _, rerr := library.RecycleFile(s.recycle, old); rerr != nil {
					_ = os.Remove(old)
				}
			} else {
				_ = os.Remove(old)
			}
			s.log.Info("series: superseded old episode file", "old", old, "new", path)
		}
	}
	return s.MarkEpisodeImported(ctx, seriesID, season, episode, path, size)
}

// WantsFile reports whether importing a candidate release (at resolution res) into this
// episode is worth doing: true when the episode has no file, when its recorded file is
// gone from disk, or when the candidate is a strictly higher resolution than what's on
// disk. The auto-importer uses this to skip an equal-or-lower-quality duplicate — which
// otherwise ping-pongs endlessly between two releases of the same episode.
func (s *Service) WantsFile(ctx context.Context, seriesID int64, season, episode int, res parser.Resolution) bool {
	old, _ := s.repo.EpisodeFilePath(ctx, seriesID, season, episode)
	if old == "" {
		return true
	}
	if _, err := os.Stat(old); err != nil {
		return true // recorded file no longer on disk — re-import it
	}
	cur := parser.Parse(filepath.Base(old)).Resolution
	return parser.ResolutionRank(res) > parser.ResolutionRank(cur)
}

// AcquisitionSummary returns per-monitored-series wanted/upcoming episode counts for
// the downloads feed (Searching + Upcoming tabs).
func (s *Service) AcquisitionSummary(ctx context.Context) []SeriesAcquisition {
	out, err := s.repo.AcquisitionSummary(ctx)
	if err != nil {
		s.log.Warn("series: acquisition summary failed", "err", err)
		return nil
	}
	return out
}

// SeasonHasMissing reports whether the covered season still has an aired, monitored
// episode with no file — used to decide whether an already-imported pack is worth a
// second pass (e.g. a season pack that only partly extracted the first time).
func (s *Service) SeasonHasMissing(ctx context.Context, seriesID int64, season int) bool {
	return s.repo.SeasonHasMissing(ctx, seriesID, season)
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

// normKey folds accents, lowercases, and keeps only alphanumerics — for tolerant title
// matching, so "Pokémon" and "Pokemon" resolve to the same key.
func normKey(str string) string {
	var b []rune
	for _, r := range parser.FoldAccents(str) {
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
