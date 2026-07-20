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
				// Prefer an absolute number the source supplied authoritatively (TVDB)
				// over the counted one, which drifts whenever the season list is wrong.
				if ed.AbsoluteNumber > 0 {
					absNum = ed.AbsoluteNumber
				}
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

// Refresh re-pulls metadata for a series, adding any newly-announced seasons or episodes.
// In the ordinary case existing rows are left untouched (INSERT OR IGNORE), so monitor/file
// state is preserved and only genuinely new episodes appear.
//
// When the source has changed the season MODEL — e.g. a TVDB key was added and anime that
// was on TMDB's 20-season, continuously-numbered listing is now TVDB's 22-season, per-season
// listing — INSERT OR IGNORE can't help: it never renumbers an episode that already exists.
// So Refresh detects that case and rebuilds the listing, carrying files across by absolute
// number. It returns renumbered=true when files ended up at a new (season, episode), so the
// caller can rename them on disk to match.
func (s *Service) Refresh(ctx context.Context, id int64) (Series, bool, error) {
	sr, err := s.repo.Get(ctx, id)
	if err != nil {
		return Series{}, false, err
	}
	renumbered := false
	if d, derr := s.meta.GetSeries(ctx, sr.TMDBID); derr == nil {
		seasons := seasonsFromDetails(d, sr.Monitored)
		stored, _ := s.repo.StoredNumbering(ctx, id)
		if numberingModelChanged(seasons, stored) {
			remaps, rerr := s.repo.RebuildEpisodes(ctx, id, seasons)
			if rerr != nil {
				// A rebuild that can't complete falls back to the additive path — better a
				// show with stale numbering than one left half-rebuilt.
				s.log.Warn("series: numbering rebuild failed — falling back to insert", "series", sr.Title, "err", rerr)
				if err := s.repo.InsertSeasons(ctx, id, seasons); err != nil {
					s.log.Warn("series: refresh insert seasons failed", "series", sr.Title, "err", err)
				}
			} else {
				renumbered = len(remaps) > 0
				s.log.Info("series: rebuilt episode numbering to match the metadata source",
					"series", sr.Title, "files_remapped", len(remaps))
				if renumbered {
					s.AddEvent(ctx, id, "renumbered", fmt.Sprintf(
						"Episode numbering rebuilt from updated metadata; %d file(s) remapped and renamed", len(remaps)))
				}
			}
		} else if err := s.repo.InsertSeasons(ctx, id, seasons); err != nil {
			s.log.Warn("series: refresh insert seasons failed", "series", sr.Title, "err", err)
		}
		// Drop seasons the metadata no longer lists. A refresh that can only ADD leaves
		// a show stuck with whatever a previous source invented — Naruto kept seasons
		// 2002-2007 from a year-numbered listing, with no way back short of deleting the
		// show. Anything holding a file is kept regardless.
		keep := make([]int, 0, len(seasons))
		for _, sn := range seasons {
			keep = append(keep, sn.SeasonNumber)
		}
		if n, perr := s.repo.PruneSeasonsNotIn(ctx, id, keep); perr != nil {
			s.log.Warn("series: prune stale seasons failed", "series", sr.Title, "err", perr)
		} else if n > 0 {
			s.log.Info("series: removed seasons the metadata no longer lists", "series", sr.Title, "removed", n)
		}
		// Fill absolute numbers only where they're still unset — never overwriting the
		// authoritative ones a source like TVDB supplied (see BackfillAbsolute).
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
	got, err := s.Get(ctx, id)
	return got, renumbered, err
}

// numberingModelChanged reports whether fresh metadata places a shared absolute episode at
// a different (season, episode) than what's stored — i.e. the season model itself changed,
// not merely its episode boundaries. That's the signal that a plain INSERT-OR-IGNORE would
// silently do nothing and a rebuild is needed. Compared on absolute number because it's the
// one identity both models share.
func numberingModelChanged(desired []Season, stored map[int][2]int) bool {
	if len(stored) == 0 {
		return false // nothing to reconcile against — the additive path is correct
	}
	for _, sn := range desired {
		for _, ep := range sn.Episodes {
			if ep.AbsoluteNumber <= 0 {
				continue
			}
			if se, ok := stored[ep.AbsoluteNumber]; ok {
				if se[0] != sn.SeasonNumber || se[1] != ep.EpisodeNumber {
					return true
				}
			}
		}
	}
	return false
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
		if ref, ok := s.resolveSE(ctx, seriesID, rel.Season, ep, anime); ok {
			out = append(out, ref)
		}
	}
	return out
}

// ResolveEpisode resolves a single (season, episode) from a filename to the concrete
// episode it belongs to — used by rescan/import where episodes are already split out.
func (s *Service) ResolveEpisode(ctx context.Context, seriesID int64, season, episode int) (int, int) {
	sr, err := s.repo.Get(ctx, seriesID)
	ref, ok := s.resolveSE(ctx, seriesID, season, episode, err == nil && sr.IsAnime())
	if !ok {
		return season, episode // couldn't remap — leave it as-is for the post-import path
	}
	return ref.Season, ref.Episode
}

func (s *Service) resolveSE(ctx context.Context, seriesID int64, season, episode int, anime bool) (EpisodeRef, bool) {
	if s.repo.EpisodeExists(ctx, seriesID, season, episode) {
		return EpisodeRef{Season: season, Episode: episode}, true
	}
	if anime {
		// A manual scene-season mapping wins over every guess below — it's the user
		// telling us outright where this cour lands. Only consulted when they've set one
		// for this exact scene season, so it can't disturb shows that resolve fine.
		if ref, ok := s.sceneOverrideRef(ctx, seriesID, season, episode); ok {
			return ref, true
		}
		// Per-cour positional: the season exists but is numbered absolutely.
		if real, ok := s.repo.NthEpisodeOfSeason(ctx, seriesID, season, episode); ok {
			return EpisodeRef{Season: season, Episode: real}, true
		}
		// TheXEM scene mapping (authoritative): scene (season, episode) → absolute → TMDB.
		if abs, ok := s.sceneAbsolute(ctx, seriesID, season, episode); ok {
			if se, ep, ok := s.repo.EpisodeByAbsolute(ctx, seriesID, abs); ok {
				return EpisodeRef{Season: se, Episode: ep}, true
			}
		}
		// Absolute number in the SxxExx slot: some groups ship a whole season with the
		// SERIES absolute number in the episode field — EiNSTEiNSiR names S07E01 as
		// "S07E139". Read the number as an absolute, but trust it ONLY when it maps back
		// into the SAME season the file named, so a genuine per-cour "S03E01" (which would
		// otherwise hit absolute 1 → S01E01) is never disturbed.
		if se, ep, ok := s.repo.EpisodeByAbsolute(ctx, seriesID, episode); ok && se == season {
			return EpisodeRef{Season: se, Episode: ep}, true
		}
		// Air-date-gap fallback: split a TMDB single season at broadcast hiatuses.
		if rs, re, ok := s.resolveSceneSeason(ctx, seriesID, season, episode); ok {
			return EpisodeRef{Season: rs, Episode: re}, true
		}
		// Nothing mapped this to a real episode. Do NOT invent a phantom (a file named
		// S07E139 must not become a nonexistent S07E139) — report it unresolved so the
		// caller holds it for review instead of misnaming it.
		return EpisodeRef{}, false
	}
	return EpisodeRef{Season: season, Episode: episode}, true // standard: mark as-is (no-op if absent)
}

// sceneOverrideRef resolves a scene (season, episode) through the user's manual mapping.
// The mapping pins where scene E01 lands; the rest of the cour follows by walking the
// series' absolute order forward, so a cour that crosses a TMDB season boundary still
// resolves correctly. Falls back to a plain within-season offset when absolute numbers
// haven't been backfilled.
func (s *Service) sceneOverrideRef(ctx context.Context, seriesID int64, sceneSeason, sceneEpisode int) (EpisodeRef, bool) {
	if sceneSeason < 1 || sceneEpisode < 1 {
		return EpisodeRef{}, false
	}
	o, ok := s.repo.SceneOverrideFor(ctx, seriesID, sceneSeason)
	if !ok {
		return EpisodeRef{}, false
	}
	if base := s.repo.AbsoluteOf(ctx, seriesID, o.TMDBSeason, o.TMDBEpisode); base > 0 {
		if se, ep, found := s.repo.EpisodeByAbsolute(ctx, seriesID, base+sceneEpisode-1); found {
			return EpisodeRef{Season: se, Episode: ep}, true
		}
	}
	target := o.TMDBEpisode + sceneEpisode - 1
	if s.repo.EpisodeExists(ctx, seriesID, o.TMDBSeason, target) {
		return EpisodeRef{Season: o.TMDBSeason, Episode: target}, true
	}
	return EpisodeRef{}, false
}

// AbsoluteNumber returns an episode's absolute (1..N across the run) number, or 0 when
// it hasn't been computed. Used to build anime searches the way fansubs name releases.
func (s *Service) AbsoluteNumber(ctx context.Context, seriesID int64, season, episode int) int {
	return s.repo.AbsoluteOf(ctx, seriesID, season, episode)
}

// SceneOverrides returns a series' manual scene-season mappings.
func (s *Service) SceneOverrides(ctx context.Context, seriesID int64) []SceneOverride {
	return s.repo.SceneOverrides(ctx, seriesID)
}

// SetSceneOverride records a manual scene-season mapping. Anime-only: standard series
// are matched by SxxExx directly, so an override there would only ever misroute.
func (s *Service) SetSceneOverride(ctx context.Context, seriesID int64, o SceneOverride) error {
	sr, err := s.repo.Get(ctx, seriesID)
	if err != nil {
		return err
	}
	if !sr.IsAnime() {
		return fmt.Errorf("scene-season mapping applies to anime only — set the series type to Anime first")
	}
	if o.SceneSeason < 1 || o.TMDBSeason < 0 || o.TMDBEpisode < 1 {
		return fmt.Errorf("scene season and target episode must be 1 or greater")
	}
	if !s.repo.EpisodeExists(ctx, seriesID, o.TMDBSeason, o.TMDBEpisode) {
		return fmt.Errorf("this series has no S%02dE%02d to map onto", o.TMDBSeason, o.TMDBEpisode)
	}
	if err := s.repo.SetSceneOverride(ctx, seriesID, o); err != nil {
		return err
	}
	s.AddEvent(ctx, seriesID, "scene-map", fmt.Sprintf("Scene season %d mapped to S%02dE%02d", o.SceneSeason, o.TMDBSeason, o.TMDBEpisode))
	return nil
}

// DeleteSceneOverride removes a manual scene-season mapping.
func (s *Service) DeleteSceneOverride(ctx context.Context, seriesID int64, sceneSeason int) error {
	return s.repo.DeleteSceneOverride(ctx, seriesID, sceneSeason)
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
	// A manual mapping wins, so a whole "S02" pack matches the cour the user pinned.
	// The cour runs from its mapped start up to the next mapped scene season (or the
	// end of the series), walked in absolute order.
	if o, ok := s.repo.SceneOverrideFor(ctx, seriesID, sceneSeason); ok {
		if start := s.repo.AbsoluteOf(ctx, seriesID, o.TMDBSeason, o.TMDBEpisode); start > 0 {
			end := 0 // exclusive; 0 = run to the end of the series
			if next, ok2 := s.repo.SceneOverrideFor(ctx, seriesID, sceneSeason+1); ok2 {
				end = s.repo.AbsoluteOf(ctx, seriesID, next.TMDBSeason, next.TMDBEpisode)
			}
			var out []EpisodeRef
			for abs := start; end == 0 || abs < end; abs++ {
				se, ep, found := s.repo.EpisodeByAbsolute(ctx, seriesID, abs)
				if !found {
					break
				}
				out = append(out, EpisodeRef{Season: se, Episode: ep})
			}
			if len(out) > 0 {
				return out
			}
		}
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

// RepointEpisodeFile updates every episode served by oldPath to point at newPath. Returns
// how many episode records were moved — more than one for a double-length episode file.
func (s *Service) RepointEpisodeFile(ctx context.Context, seriesID int64, oldPath, newPath string, size int64) (int64, error) {
	return s.repo.RepointEpisodeFile(ctx, seriesID, oldPath, newPath, size)
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

// FolderSharedWith returns other series storing episodes in the same library folder.
func (s *Service) FolderSharedWith(ctx context.Context, seriesID int64, folder string) []int64 {
	return s.repo.FolderSharedWith(ctx, seriesID, folder)
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
// sourceRelease is the release name the file came from (e.g. the downloaded filename);
// it's recorded so upgrade scoring has a faithful baseline. Pass "" when unknown (a
// rescan of an existing library file) — the upgrade path skips episodes without one.
func (s *Service) SupersedeEpisodeFile(ctx context.Context, seriesID int64, season, episode int, path string, size int64, sourceRelease string) error {
	if sourceRelease != "" {
		if err := s.repo.SetEpisodeSourceRelease(ctx, seriesID, season, episode, sourceRelease); err != nil {
			s.log.Warn("series: record source release failed", "err", err)
		}
	}
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

// SeasonEpisodeTitles returns a season's episode titles, keyed by episode number.
func (s *Service) SeasonEpisodeTitles(ctx context.Context, seriesID int64, season int) map[int]string {
	return s.repo.SeasonEpisodeTitles(ctx, seriesID, season)
}

// SetEpisodeSourceRelease records the release name an episode's file came from. Used to
// repair episodes imported before quality was inherited from the release, whose recorded
// name carries no resolution and so makes every future comparison meaningless.
func (s *Service) SetEpisodeSourceRelease(ctx context.Context, seriesID int64, season, episode int, release string) error {
	return s.repo.SetEpisodeSourceRelease(ctx, seriesID, season, episode, release)
}

// CurrentEpisodeFile returns what an episode currently holds, for upgrade decisions that
// need more than the filename (size, source release, runtime).
func (s *Service) CurrentEpisodeFile(ctx context.Context, seriesID int64, season, episode int) EpisodeFile {
	return s.repo.CurrentEpisodeFile(ctx, seriesID, season, episode)
}

// WantsFile reports whether importing a candidate release (at resolution res) into this
// episode is worth doing: true when the episode has no file, when its recorded file is
// gone from disk, or when the candidate is a strictly higher resolution than what's on
// disk. The auto-importer uses this to skip an equal-or-lower-quality duplicate — which
// otherwise ping-pongs endlessly between two releases of the same episode.
//
// Resolution is the only comparison available from a filename alone. A caller holding the
// quality profile should use automation's wantsEpisodeFile instead, which also applies the
// profile's bitrate-upgrade margin.
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

// SearchState returns the series' last sweep time and consecutive-miss count.
func (s *Service) SearchState(ctx context.Context, seriesID int64) (string, int) {
	return s.repo.SearchState(ctx, seriesID)
}

// RecordSearchMiss notes that a sweep found nothing to grab for this series.
func (s *Service) RecordSearchMiss(ctx context.Context, seriesID int64) {
	s.repo.RecordSearchMiss(ctx, seriesID)
}

// ResetSearchMisses clears the search backoff (a grab succeeded).
func (s *Service) ResetSearchMisses(ctx context.Context, seriesID int64) {
	s.repo.ResetSearchMisses(ctx, seriesID)
}

// HasWantedEpisodes reports whether the automation would actually grab anything for
// this series (monitored + aired + no file) — used to skip a pointless indexer search.
func (s *Service) HasWantedEpisodes(ctx context.Context, seriesID int64) bool {
	return s.repo.HasWantedEpisodes(ctx, seriesID)
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

// TitleMatcher indexes a library snapshot once, keyed by normalized title, so a caller
// resolving many torrents in one pass indexes once instead of reloading the series table
// per torrent. Keyed the same way (and queried with the same NormTitle) as MatchByTitle.
func (s *Service) TitleMatcher(all []Series) func(normalized string) (Series, bool) {
	byKey := make(map[string]Series, len(all))
	for _, sr := range all {
		k := normKey(sr.Title)
		if _, exists := byKey[k]; !exists { // first wins, matching MatchByTitle's scan order
			byKey[k] = sr
		}
	}
	return func(normalized string) (Series, bool) {
		sr, ok := byKey[normalized]
		return sr, ok
	}
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
	// Drop parenthesised alternate titles first, so "My Hero Academia (Boku no Hero
	// Academia)" matches the library's "My Hero Academia" instead of looking like a
	// longer, different show. Applied to both sides, so it never causes a false match.
	str = parser.StripBracketed(str)
	// Same "&" / "and" equivalence as automation's titleKey: a release named
	// "Love.and.Death" must resolve to the library's "Love & Death" and not look like a
	// different show.
	str = strings.ReplaceAll(str, "&", " and ")
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
