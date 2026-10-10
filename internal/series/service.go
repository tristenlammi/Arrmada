package series

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
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
	repo   *Repo
	meta   metadata.SeriesProvider
	root   string        // library root, for delete-with-files and library scan (see libRoot)
	rootFn func() string // the live library root; nil = root
	bin    library.Bin   // where deleted files go (a bin that's off hard-deletes)
	bus    *eventbus.Bus
	log    *slog.Logger
	scene  SceneMapper // TheXEM client (nil → scene mapping falls back to air-date gaps)

	muUnmatched   sync.Mutex
	lastUnmatched []UnmatchedFolder // folders the last scan couldn't identify, for manual pick

	sceneMu    sync.Mutex
	sceneCache map[int64]map[string]int // series id → scene "S-E" → absolute (in-memory)

	seriesLocks sync.Map // series id → *sync.Mutex: one refresh or apply per show at a time

	monitorDefault func(ctx context.Context) string // the series_monitor_default setting

	// libraryChanged is told about a show folder whose files a delete changed, so Plex can
	// rescan it (plexscan). nil = nobody listening.
	libraryChanged func(kind, dir string)
}

// SetLibraryChanged installs who hears that a show folder's files changed through a
// delete here (Plex's scanner). Call it at startup, before anything runs.
func (s *Service) SetLibraryChanged(fn func(kind, dir string)) { s.libraryChanged = fn }

// changed reports a show folder whose files changed.
func (s *Service) changed(dir string) {
	if s.libraryChanged != nil && dir != "" && dir != "." {
		s.libraryChanged("show", dir)
	}
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

// SetRecycleDir points episode-file deletion at the recycle bin (matching movies). ""
// means the bin is switched off and deletes are permanent.
func (s *Service) SetRecycleDir(dir string) { s.bin = library.SingleBin(dir) }

// SetBin routes deleted files to bin (the per-library bins in the app). Call it at
// startup, before anything runs.
func (s *Service) SetBin(b library.Bin) { s.bin = b }

// SetRootFunc makes the TV folder live: the library scan, manual imports and the
// delete tidy-up read it on every use, so a folder changed in Settings → Library applies
// without a restart. Call it at startup, before anything runs.
func (s *Service) SetRootFunc(fn func() string) { s.rootFn = fn }

// libRoot is the TV folder now.
func (s *Service) libRoot() string {
	if s.rootFn != nil {
		return strings.TrimSpace(s.rootFn())
	}
	return s.root
}

// SetBus lets deletes announce file.removed, so the import pipeline forgets a deleted
// file instead of importing the still-seeding torrent straight back.
func (s *Service) SetBus(b *eventbus.Bus) { s.bus = b }

// fileRemoved announces that a library file is gone.
func (s *Service) fileRemoved(path string) {
	if s.bus != nil {
		s.bus.Publish("file.removed", map[string]any{"path": path})
	}
}

// DeleteEpisodeFile moves one episode's file and its subtitles to the recycle bin (or
// deletes them when the bin is off) and flips the episode back to wanted, without touching
// the rest of the show. If the bin can't take the video, nothing changes and the error
// says why — it never falls back to a permanent delete.
func (s *Service) DeleteEpisodeFile(ctx context.Context, seriesID int64, season, episode int) error {
	path, err := s.repo.EpisodeFilePath(ctx, seriesID, season, episode)
	if err != nil {
		return err
	}
	var subFailed []string
	if path != "" {
		var subs []string
		if !library.SharesBase(path) { // a same-name sibling video still pairs with them
			subs = library.Sidecars(path)
		}
		if _, err := library.RemoveToBin(s.bin, path); err != nil {
			return err
		}
		s.fileRemoved(path)
		for _, sub := range subs {
			if _, err := library.RemoveToBin(s.bin, sub); err != nil {
				s.log.Warn("series: subtitle left behind", "path", sub, "err", err)
				subFailed = append(subFailed, filepath.Base(sub))
				continue
			}
			s.fileRemoved(sub)
		}
	}
	if err := s.repo.ClearEpisodeFile(ctx, seriesID, season, episode); err != nil {
		return err
	}
	if path != "" {
		s.changed(ShowFolder(path))
	}
	detail := fmt.Sprintf("S%02dE%02d file deleted", season, episode)
	if len(subFailed) > 0 {
		detail += " (subtitles left in place: " + strings.Join(subFailed, ", ") + ")"
	}
	s.repo.AddEvent(ctx, seriesID, "file.deleted", detail)
	return nil
}

// NewService wires the module. root is the library directory (for scan / delete-files).
func NewService(db *sql.DB, meta metadata.SeriesProvider, root string, log *slog.Logger) *Service {
	return &Service{repo: NewRepo(db), meta: meta, root: root, bin: library.SingleBin(""), log: log}
}

// MetadataAvailable reports whether the metadata provider is configured.
func (s *Service) MetadataAvailable() bool { return s.meta.Available() }

// Lookup searches the metadata provider for series to add.
func (s *Service) Lookup(ctx context.Context, query string) ([]metadata.SeriesResult, error) {
	return s.meta.SearchSeries(ctx, query)
}

// RecentlyImported is the shows with an episode imported in the last `days` days, newest
// first, one row per show.
func (s *Service) RecentlyImported(ctx context.Context, days, limit int) ([]RecentImport, error) {
	return s.repo.RecentlyImported(ctx, days, limit)
}

// List returns the library with roll-up stats.
func (s *Service) List(ctx context.Context) ([]Series, error) {
	all, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	// One query for every alias rather than one per series — the sweep lists the whole
	// library each pass, and almost no series has any.
	if byID := s.repo.AliasTitlesFor(ctx); len(byID) > 0 {
		for i := range all {
			all[i].Aliases = byID[all[i].ID]
		}
	}
	return all, nil
}

// ByTMDBIDs returns the library shows with these TMDB ids, each with its Stats roll-up.
func (s *Service) ByTMDBIDs(ctx context.Context, tmdbIDs []int) ([]Series, error) {
	return s.repo.ByTMDBIDs(ctx, tmdbIDs)
}

// GetSummary returns one series' own record, without its seasons and episodes.
func (s *Service) GetSummary(ctx context.Context, id int64) (Series, error) {
	return s.repo.Get(ctx, id)
}

// Get returns one series with its seasons and episodes.
func (s *Service) Get(ctx context.Context, id int64) (Series, error) {
	sr, err := s.repo.Get(ctx, id)
	if err != nil {
		return Series{}, err
	}
	if seasons, err := s.repo.SeasonsFor(ctx, id); err == nil {
		sr.Seasons = seasons
	}
	sr.Aliases = s.repo.Aliases(ctx, id)
	// The same roll-up the list shows, so the detail page's progress matches its card.
	if st, err := s.repo.StatsFor(ctx, id); err == nil {
		sr.Stats = st
	}
	return sr, nil
}

// AddOptions says how a new show is monitored.
type AddOptions struct {
	// Monitored is the series gate: false adds the show paused (nothing is searched),
	// with episode flags still set by the preset for when it's resumed.
	Monitored bool
	// Preset picks the monitored episodes (see the Preset constants); "" means the
	// configured default (MonitorDefault).
	Preset string
	// MonitorNewSeasons overrides the preset's own choice when set.
	MonitorNewSeasons *bool
	// Seasons, when not nil, replaces the preset: exactly these regular seasons are
	// monitored (with all their episodes), nothing else, never specials. "Monitor new
	// seasons" is then off unless MonitorNewSeasons says otherwise, so a show added for
	// some seasons doesn't start grabbing the next one on its own.
	Seasons map[int]bool
}

// Add adds a show monitored with every regular episode, or — monitored=false, as the
// library scan adds what it finds — with nothing monitored at all.
func (s *Service) Add(ctx context.Context, tmdbID int, qualityProfile string, monitored bool) (Series, error) {
	preset := PresetAll
	if !monitored {
		preset = PresetNone
	}
	return s.AddWith(ctx, tmdbID, qualityProfile, AddOptions{Monitored: monitored, Preset: preset})
}

// AddWith pulls full metadata for a TMDB series id and adds it — series row plus every
// season and episode — then applies the monitoring preset. Specials are never monitored
// by a preset.
func (s *Service) AddWith(ctx context.Context, tmdbID int, qualityProfile string, opts AddOptions) (Series, error) {
	preset := opts.Preset
	if preset == "" {
		preset = s.MonitorDefault(ctx)
	}
	if !ValidPreset(preset) {
		return Series{}, fmt.Errorf("%w: %q", ErrUnknownPreset, preset)
	}
	monitored := opts.Monitored
	d, err := s.meta.GetSeries(ctx, tmdbID)
	if err != nil {
		return Series{}, fmt.Errorf("fetch metadata: %w", err)
	}
	sr := Series{
		TMDBID: d.TMDBID, TVDBID: d.TVDBID, IMDBID: d.IMDBID, Title: d.Title, Year: d.Year, Overview: d.Overview,
		PosterURL: d.PosterURL, Status: d.Status, Network: d.Network,
		Monitored: monitored, MonitorNewSeasons: presetNewSeasons(preset), QualityProfile: qualityProfile, Extra: extraFrom(d),
		SeriesType: detectSeriesType(d),
	}
	created, err := s.repo.Create(ctx, sr)
	if err != nil {
		return Series{}, err
	}
	// Rows go in unmonitored; the preset then decides every flag in one pass.
	seasons := seasonsFromDetails(d, func(int) bool { return false })
	if err := s.repo.InsertSeasons(ctx, created.ID, seasons); err != nil {
		s.log.Warn("series: insert seasons failed", "series", created.Title, "err", err)
	} else if d.NumberingSource != "" {
		// The rows now follow this listing, even when it was a stand-in (its source
		// failed): what's recorded is what the rows ARE numbered by. Leaving a stand-in
		// unrecorded meant a source that kept failing never matched the stored one, so
		// every refresh after it was treated as a stand-in too. A good listing from a
		// better source later is still adopted — see applyNumbering.
		s.setNumberingSource(ctx, created, d.NumberingSource)
		created.NumberingSource = d.NumberingSource
	}
	if created.IsAnime() {
		s.refreshSceneMap(ctx, created.ID, d.TVDBID) // TheXEM scene mapping for split-season anime
	}
	s.syncTMDBAliases(ctx, created.ID, d)
	var monErr error
	if opts.Seasons != nil {
		mns := false
		if opts.MonitorNewSeasons != nil {
			mns = *opts.MonitorNewSeasons
		}
		monErr = s.repo.MonitorOnlySeasons(ctx, created.ID, opts.Seasons, mns)
	} else {
		monErr = s.repo.ApplyMonitorPreset(ctx, created.ID, preset, opts.MonitorNewSeasons)
	}
	if monErr != nil {
		s.log.Warn("series: couldn't apply the monitoring choice", "series", created.Title, "preset", preset, "err", monErr)
	} else if got, err := s.repo.Get(ctx, created.ID); err == nil {
		created = got
	}
	_ = s.repo.MarkRefreshed(ctx, created.ID)
	s.AddEvent(ctx, created.ID, "added", fmt.Sprintf("Added — %d seasons", len(seasons)))
	s.log.Info("series added", "title", created.Title, "year", created.Year, "seasons", len(seasons))
	return created, nil
}

// seasonsFromDetails projects TMDB season/episode metadata into storage rows.
// monitorFor says whether a season (and so its episodes) is monitored; specials (season 0)
// never are. Only rows that don't exist yet take these flags — a refresh never rewrites
// the owner's monitoring of an existing episode.
// Absolute numbers are assigned 1..N across the non-special seasons in order, so an
// anime release numbered absolutely resolves to the right (season, episode).
func seasonsFromDetails(d *metadata.SeriesDetails, monitorFor func(seasonNumber int) bool) []Season {
	seasons := make([]Season, 0, len(d.Seasons))
	abs := 0
	for _, sd := range d.Seasons {
		special := sd.SeasonNumber == 0
		monitored := !special && monitorFor(sd.SeasonNumber)
		sn := Season{
			SeasonNumber: sd.SeasonNumber, Name: sd.Name, Overview: sd.Overview, PosterURL: sd.PosterURL,
			Monitored: monitored,
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
				AbsoluteNumber: absNum, Monitored: monitored,
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

// RefreshOptions says what a refresh may do beyond keeping the listing current.
type RefreshOptions struct {
	// ApplyPlan is the hash of a numbering proposal the owner reviewed and applied. The
	// refresh renumbers the show — moves files onto new (season, episode) rows — only
	// when the fresh listing still produces exactly that plan. Every other refresh, the
	// owner's own Refresh included, at most stores a proposal: a renumber nobody looked
	// at moved and renamed files on its own.
	ApplyPlan string
}

// RefreshResult reports what a refresh did to the episode numbering.
type RefreshResult struct {
	// Rebuilt: the reviewed plan (RefreshOptions.ApplyPlan) was applied and the listing
	// now follows the fresh source. Renumbered: files ended up on a new (season,
	// episode), so the caller should rename them on disk to match.
	Rebuilt    bool
	Renumbered bool
	Remaps     []EpisodeRemap
	// Fallback: the numbering source failed, so the listing was a stand-in and only
	// genuinely new episodes were added.
	Fallback bool
	// ModelChanged: the fresh listing numbers the show differently from what's stored,
	// whether or not the refresh was allowed to act on it.
	ModelChanged bool
	// Proposed: the change can be applied, and is stored as a proposal for review.
	Proposed bool
	// Unplaced counts files the rebuild found no episode for: their rows went, and the
	// files stay on disk where they were.
	Unplaced int
}

// Refresh re-pulls metadata for a series, adding any newly-announced seasons or episodes.
// Existing rows keep their monitor and file state, and each file stays on its (season,
// episode) — only titles, dates and absolute numbers follow the metadata.
//
// When the source has changed the season MODEL — e.g. a TVDB key was added and anime that
// was on TMDB's 20-season, continuously-numbered listing is now TVDB's 22-season, per-season
// listing — keeping each file on its (season, episode) would be wrong. Refresh detects that
// case and stores a proposal listing every file that would move; ApplyNumbering carries the
// files across by absolute number once the owner has reviewed it. See applyNumbering for
// when a change can be applied at all.
//
// Refreshes of one show never interleave, so an Apply can't race a scheduled refresh.
func (s *Service) Refresh(ctx context.Context, id int64, opts RefreshOptions) (Series, RefreshResult, error) {
	unlock := s.lockSeries(id)
	defer unlock()
	return s.refresh(ctx, id, opts)
}

// lockSeries holds a show's refresh lock until the returned func is called.
func (s *Service) lockSeries(id int64) func() {
	m, _ := s.seriesLocks.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Service) refresh(ctx context.Context, id int64, opts RefreshOptions) (Series, RefreshResult, error) {
	var res RefreshResult
	sr, err := s.repo.Get(ctx, id)
	if err != nil {
		return Series{}, res, err
	}
	if d, derr := s.meta.GetSeries(ctx, sr.TMDBID); derr == nil {
		res = s.applyNumbering(ctx, sr, d, seasonsFromDetails(d, s.refreshMonitorFor(ctx, sr)), opts)
		if d.TVDBID > 0 && d.TVDBID != sr.TVDBID {
			_ = s.repo.SetTVDBID(ctx, id, d.TVDBID)
		}
		// The show row doesn't depend on numbering, so it follows the provider whatever
		// applyNumbering decided about the episodes.
		s.refreshShow(ctx, sr, d)
		s.syncTMDBAliases(ctx, id, d)
		if err := s.repo.MarkRefreshed(ctx, id); err != nil {
			s.log.Warn("series: could not record the refresh time", "series", sr.Title, "err", err)
		}
		// Refresh the TheXEM scene map for anime, so split-season releases resolve.
		if sr.IsAnime() || detectSeriesType(d) == SeriesTypeAnime {
			s.refreshSceneMap(ctx, id, d.TVDBID)
		}
	} else {
		s.log.Warn("series: refresh metadata failed", "series", sr.Title, "err", derr)
	}
	got, err := s.Get(ctx, id)
	return got, res, err
}

// refreshMonitorFor is the monitoring a refresh gives rows that don't exist yet. A new
// episode in a season the show already has takes that season's flag, so it follows what
// the owner chose there; a season new to the show follows "monitor new seasons". Neither
// depends on the pause gate, so a paused show has the right flags when it resumes.
func (s *Service) refreshMonitorFor(ctx context.Context, sr Series) func(int) bool {
	flags, err := s.repo.SeasonMonitorFlags(ctx, sr.ID)
	if err != nil {
		s.log.Warn("series: couldn't read season monitoring — new rows follow 'monitor new seasons'", "series", sr.Title, "err", err)
	}
	return func(season int) bool {
		if on, ok := flags[season]; ok {
			return on
		}
		return sr.MonitorNewSeasons
	}
}

// refreshShow brings the show's own row up to date — title, status, poster, overview,
// network, year and the extra blob — which used to be written only once, on Add. The
// stored status gates complete and multi-season packs and decides whether the scheduled
// refresh still visits the show, so a show that ended has to read as ended.
//
// A changed title keeps the old one as a title-only alias, so releases still named the
// old way keep matching. The library folder keeps its name on purpose: imports go into
// the folder the show already has (ExistingFolderName).
func (s *Service) refreshShow(ctx context.Context, sr Series, d *metadata.SeriesDetails) {
	got, err := s.repo.UpdateSeriesMetadata(ctx, sr.ID, SeriesMeta{
		Title: d.Title, Overview: d.Overview, PosterURL: d.PosterURL, Status: d.Status,
		Network: d.Network, Year: d.Year, Extra: extraFrom(d),
	})
	if err != nil {
		s.log.Warn("series: could not update the show's metadata", "series", sr.Title, "err", err)
		return
	}
	if got.Title != sr.Title && parser.TitleKey(got.Title) != parser.TitleKey(sr.Title) {
		// An automatic alias: matched exactly, like the title it was (a prefix match on an
		// old title would swallow spin-offs named after it). It never replaces a row the
		// owner already has for that key — that would reset a season they pinned — or one
		// they removed.
		if _, err := s.repo.AddAutoAlias(ctx, sr.ID, sr.Title, parser.TitleKey(sr.Title)); err != nil {
			s.log.Warn("series: could not keep the old title as an alias", "series", got.Title, "old", sr.Title, "err", err)
		}
		s.AddEvent(ctx, sr.ID, "title", fmt.Sprintf("Title changed: %s → %s", sr.Title, got.Title))
		s.log.Info("series: title changed", "old", sr.Title, "new", got.Title)
	}
	if sr.Status != "" && got.Status != sr.Status {
		s.AddEvent(ctx, sr.ID, "status", fmt.Sprintf("Status: %s → %s", sr.Status, got.Status))
		s.log.Info("series: status changed", "series", got.Title, "old", sr.Status, "new", got.Status)
	}
}

// applyNumbering brings the stored episode listing in line with a fresh one, without ever
// moving a file on a listing that can't be trusted to number the show.
//
// Counted absolute numbers used to drive automatic file moves: one episode added to an
// earlier season shifted every later absolute, which read as a "model change", rebuilt the
// listing by absolute and renamed files onto the wrong episodes — and a TVmaze timeout
// handing over TMDB's numbering did the same, until the next good refresh moved them back.
// The rules now:
//   - A fallback listing (a higher-priority source failed) from the same source the stored
//     rows follow is numbered the same way, so it refreshes titles and dates in place and
//     adds new episodes. One from a different source only adds genuinely new episodes.
//   - Otherwise a file's identity is its (season, episode). For a standard show that's
//     the whole story: files never move, so there's no model change to guard against.
//   - For anime, a model change is a season holding files that the fresh listing no
//     longer has, or — only when the absolutes are real TVDB numbers — an absolute at a
//     different (season, episode). When the fresh listing is TVDB's — the highest-priority
//     source, so moving onto it from anything is an upgrade (the TVDB-key-added case the
//     rebuild exists for) — the change is stored as a proposal for the owner to review; a
//     rebuild (which carries files by absolute number) runs only for the Apply of that
//     exact plan. Anything else keeps the stored numbering and says so in History.
func (s *Service) applyNumbering(ctx context.Context, sr Series, d *metadata.SeriesDetails, seasons []Season, opts RefreshOptions) RefreshResult {
	var res RefreshResult
	id := sr.ID
	fresh := d.NumberingSource
	if fresh == "" {
		fresh = "tmdb" // a provider that doesn't say is the primary
	}
	stored := sr.NumberingSource
	if d.NumberingFallback {
		if fresh == stored {
			// The stand-in is the listing the stored rows already follow — TVDB is
			// failing and TVmaze numbered both, say — so it can't renumber anything.
			// Treating it as a stand-in anyway froze titles and air dates for as long as
			// the other source kept failing.
			s.refreshInPlace(ctx, sr, seasons)
			return res
		}
		res.Fallback = true
		s.log.Warn("series: numbering source failed — metadata only", "series", sr.Title, "listing_from", fresh)
		s.addOnlyNewEpisodes(ctx, sr, seasons)
		return res
	}

	storedSeasons, err := s.repo.SeasonsFor(ctx, id)
	if err != nil {
		s.log.Warn("series: refresh couldn't read the stored listing — adding new episodes only", "series", sr.Title, "err", err)
		s.addOnlyNewEpisodes(ctx, sr, seasons)
		return res
	}
	authoritative := sr.IsAnime() && fresh == "tvdb"
	// A standard show is never rebuilt, so a model change has nothing to protect there:
	// declining one only stopped titles and air dates refreshing for as long as, say, a
	// legacy year-numbered season held files. PruneSeasonsNotIn keeps such seasons anyway.
	res.ModelChanged = sr.IsAnime() && numberingModelChanged(seasons, storedSeasons, authoritative)

	switch {
	case res.ModelChanged && authoritative:
		// The owner can apply this change, but only once they've seen it: plan it, and
		// rebuild only when this is the Apply of exactly that plan.
		plan, perr := s.repo.PlanRebuild(ctx, id, seasons)
		if perr != nil {
			s.log.Warn("series: couldn't plan the renumber — adding new episodes only", "series", sr.Title, "err", perr)
			s.addOnlyNewEpisodes(ctx, sr, seasons)
			return res
		}
		hash := PlanHash(plan)
		if opts.ApplyPlan != "" && opts.ApplyPlan == hash {
			return s.rebuild(ctx, sr, seasons, fresh, res)
		}
		res.Proposed = true
		n := filesInPlan(plan)
		isNew, uerr := s.repo.UpsertNumberingPending(ctx, id, NumberingPending{From: stored, To: fresh, PlanHash: hash, Remaps: plan})
		if uerr != nil {
			s.log.Warn("series: couldn't store the numbering proposal", "series", sr.Title, "err", uerr)
		}
		s.log.Warn("series: numbering model changed — proposed for review", "series", sr.Title,
			"stored_source", stored, "fresh_source", fresh, "files_that_would_move", n, "applying", opts.ApplyPlan != "")
		if isNew {
			// One History line per distinct plan, not one every six hours. Anime takes no
			// new episodes from a listing numbered differently (see addOnlyNewEpisodes),
			// so say that too — an airing show is stuck until this is resolved.
			s.AddEvent(ctx, id, "numbering", fmt.Sprintf(
				"Numbering from %s differs from what's stored — %d file%s would move. New episodes won't be added until it's applied: review it on the series page.",
				sourceLabel(fresh), n, plural(n)))
		}
		s.addOnlyNewEpisodes(ctx, sr, seasons)

	case res.ModelChanged:
		// Nothing the owner can apply: keep the stored numbering and say what would end
		// this. A proposal from an earlier listing no longer stands.
		s.clearPending(ctx, sr)
		detail := fmt.Sprintf("Numbering from %s differs from what's stored (%s) — kept the stored numbering; nothing was moved. New episodes won't be added until %s.",
			sourceLabel(fresh), sourceLabel(stored), untilStoredSourceBack(stored))
		s.log.Warn("series: numbering model changed — not rebuilding", "series", sr.Title,
			"stored_source", stored, "fresh_source", fresh)
		s.addEventOnce(ctx, id, "numbering", detail)
		s.addOnlyNewEpisodes(ctx, sr, seasons)

	default:
		// The listing agrees with what's stored, so nothing is waiting for review.
		s.clearPending(ctx, sr)
		if err := s.repo.InsertSeasons(ctx, id, seasons); err != nil {
			s.log.Warn("series: refresh insert seasons failed", "series", sr.Title, "err", err)
			return res
		}
		// Same model: each file keeps its (season, episode), and absolutes follow the
		// listing. Moving nothing is the point — a shifted count is not a renumber.
		if err := s.repo.ReassignAbsolutes(ctx, id, seasons); err != nil {
			s.log.Warn("series: reassign absolute numbers failed", "series", sr.Title, "err", err)
		}
		s.pruneAndBackfill(ctx, sr, seasons)
		s.setNumberingSource(ctx, sr, fresh)
	}
	return res
}

// untilStoredSourceBack finishes "New episodes won't be added until …" for a declined
// change the owner can't apply, naming what would end it.
func untilStoredSourceBack(stored string) string {
	switch stored {
	case "tvdb":
		return "TVDB numbers the show again — check the TVDB key in Settings"
	case "tvmaze", "tmdb":
		return sourceLabel(stored) + " numbers the show again"
	}
	return "the listing matches the stored numbering again"
}

// refreshInPlace is the refresh for a stand-in listing numbered by the same source the
// stored rows follow: (season, episode) means the same episode on both sides, so titles
// and air dates are updated and new episodes added. Absolutes aren't reassigned and
// nothing is pruned — the stand-in may be missing a season (a TMDB season that failed to
// load comes back empty), and either would act on that gap as if it were real.
func (s *Service) refreshInPlace(ctx context.Context, sr Series, seasons []Season) {
	s.log.Info("series: numbering source failed — refreshed from the listing the show already follows",
		"series", sr.Title, "source", sr.NumberingSource)
	if err := s.repo.RefreshEpisodeMetadata(ctx, sr.ID, seasons); err != nil {
		s.log.Warn("series: refresh episode metadata failed", "series", sr.Title, "err", err)
	}
	if n, err := s.repo.InsertNewEpisodes(ctx, sr.ID, seasons); err != nil {
		s.log.Warn("series: refresh insert new episodes failed", "series", sr.Title, "err", err)
	} else if n > 0 {
		s.log.Info("series: added newly listed episodes", "series", sr.Title, "added", n)
	}
	if err := s.repo.BackfillAbsolute(ctx, sr.ID); err != nil {
		s.log.Warn("series: backfill absolute numbers failed", "series", sr.Title, "err", err)
	}
}

// addOnlyNewEpisodes is the additive path for a listing that mustn't number the show. For
// anime it adds nothing: a stand-in listing for anime is numbered so differently (TMDB's
// one 500-episode season against TVDB's twenty) that its "new" episodes are phantoms, and
// an airing show's new episodes arrive with the next good refresh instead.
func (s *Service) addOnlyNewEpisodes(ctx context.Context, sr Series, seasons []Season) {
	if sr.IsAnime() {
		s.log.Info("series: anime refreshed without a trusted listing — no episodes added until the next good refresh", "series", sr.Title)
		return
	}
	if n, err := s.repo.InsertNewEpisodes(ctx, sr.ID, seasons); err != nil {
		s.log.Warn("series: refresh insert new episodes failed", "series", sr.Title, "err", err)
	} else if n > 0 {
		s.log.Info("series: added newly listed episodes", "series", sr.Title, "added", n)
	}
	if err := s.repo.BackfillAbsolute(ctx, sr.ID); err != nil {
		s.log.Warn("series: backfill absolute numbers failed", "series", sr.Title, "err", err)
	}
}

// pruneAndBackfill drops seasons the metadata no longer lists and fills any absolute
// number still unset. Only run once the stored listing follows the fresh one.
func (s *Service) pruneAndBackfill(ctx context.Context, sr Series, seasons []Season) {
	// Drop seasons the metadata no longer lists. A refresh that can only ADD leaves
	// a show stuck with whatever a previous source invented — Naruto kept seasons
	// 2002-2007 from a year-numbered listing, with no way back short of deleting the
	// show. Anything holding a file is kept regardless.
	keep := make([]int, 0, len(seasons))
	for _, sn := range seasons {
		keep = append(keep, sn.SeasonNumber)
	}
	if n, perr := s.repo.PruneSeasonsNotIn(ctx, sr.ID, keep); perr != nil {
		s.log.Warn("series: prune stale seasons failed", "series", sr.Title, "err", perr)
	} else if n > 0 {
		s.log.Info("series: removed seasons the metadata no longer lists", "series", sr.Title, "removed", n)
	}
	// Fill absolute numbers only where they're still unset — never overwriting the
	// authoritative ones a source like TVDB supplied (see BackfillAbsolute).
	if err := s.repo.BackfillAbsolute(ctx, sr.ID); err != nil {
		s.log.Warn("series: backfill absolute numbers failed", "series", sr.Title, "err", err)
	}
}

func (s *Service) setNumberingSource(ctx context.Context, sr Series, source string) {
	if source == sr.NumberingSource {
		return
	}
	if err := s.repo.SetNumberingSource(ctx, sr.ID, source); err != nil {
		s.log.Warn("series: could not record the numbering source", "series", sr.Title, "err", err)
	}
}

// addEventOnce adds a History event unless the latest event of that kind already says the
// same thing — a declined renumber is re-noticed on every scheduled refresh, and one line
// per change is what's useful, not one every six hours.
func (s *Service) addEventOnce(ctx context.Context, id int64, event, detail string) {
	if evs, err := s.repo.Events(ctx, id, 50); err == nil {
		for _, e := range evs {
			if e.Event == event {
				if e.Detail == detail {
					return
				}
				break
			}
		}
	}
	s.AddEvent(ctx, id, event, detail)
}

func sourceLabel(src string) string {
	switch src {
	case "tvdb":
		return "TVDB"
	case "tvmaze":
		return "TVmaze"
	case "tmdb":
		return "TMDB"
	}
	return "an unrecorded source"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// numberingModelChanged reports whether a fresh listing numbers the show differently from
// what's stored, in a way that keeping each file on its (season, episode) would get wrong:
//   - a season (other than Specials) that holds files is missing from the fresh listing; or
//   - when authoritative (the fresh absolutes are real TVDB numbers the caller trusts to
//     identify episodes — see applyNumbering), a shared absolute episode now sits at a
//     different (season, episode).
//
// A counted absolute shifting — one episode added to an earlier season — is NOT a change:
// counted absolutes are derived from the listing, so they always shift with it.
func numberingModelChanged(desired []Season, stored []Season, authoritative bool) bool {
	if len(stored) == 0 {
		return false // nothing to reconcile against — the additive path is correct
	}
	listed := make(map[int]bool, len(desired))
	for _, sn := range desired {
		listed[sn.SeasonNumber] = true
	}
	storedAt := map[int][2]int{}
	for _, sn := range stored {
		for _, ep := range sn.Episodes {
			// Specials are left out: they carry no absolute, a rebuild leaves them in
			// place, and sources disagree about listing season 0 at all.
			if ep.HasFile && sn.SeasonNumber > 0 && !listed[sn.SeasonNumber] {
				return true
			}
			if ep.AbsoluteNumber > 0 && sn.SeasonNumber > 0 {
				storedAt[ep.AbsoluteNumber] = [2]int{sn.SeasonNumber, ep.EpisodeNumber}
			}
		}
	}
	if !authoritative {
		return false
	}
	for _, sn := range desired {
		for _, ep := range sn.Episodes {
			if ep.AbsoluteNumber <= 0 {
				continue
			}
			if se, ok := storedAt[ep.AbsoluteNumber]; ok && (se[0] != sn.SeasonNumber || se[1] != ep.EpisodeNumber) {
				return true
			}
		}
	}
	return false
}

// filesInPlan counts the files a plan moves — one per file, however many episode rows a
// double-length file serves.
func filesInPlan(plan []EpisodeRemap) int {
	seen := map[string]bool{}
	for _, r := range plan {
		if !r.Unplaced {
			seen[r.FilePath] = true
		}
	}
	return len(seen)
}

// rebuild applies a reviewed renumber: the listing is replaced, files are carried by
// absolute number (the same planner the review showed), and the proposal is closed.
func (s *Service) rebuild(ctx context.Context, sr Series, seasons []Season, fresh string, res RefreshResult) RefreshResult {
	remaps, unplaced, err := s.repo.rebuildEpisodes(ctx, sr.ID, seasons)
	if err != nil {
		// A rebuild that can't complete changes nothing (one transaction); the additive
		// path keeps the show as it was — better stale numbering than half a rebuild.
		s.log.Warn("series: numbering rebuild failed — adding new episodes only", "series", sr.Title, "err", err)
		s.addOnlyNewEpisodes(ctx, sr, seasons)
		return res
	}
	res.Rebuilt = true
	res.Remaps = remaps
	res.Unplaced = unplaced
	res.Renumbered = len(remaps) > 0
	s.log.Info("series: rebuilt episode numbering to match the metadata source",
		"series", sr.Title, "source", fresh, "files_remapped", len(remaps))
	detail := fmt.Sprintf("Episode numbering rebuilt from %s", sourceLabel(fresh))
	if len(remaps) > 0 {
		detail += ": " + remapLines(remaps)
	}
	s.AddEvent(ctx, sr.ID, "renumbered", detail)
	s.pruneAndBackfill(ctx, sr, seasons)
	s.setNumberingSource(ctx, sr, fresh)
	s.clearPending(ctx, sr)
	return res
}

func (s *Service) clearPending(ctx context.Context, sr Series) {
	if err := s.repo.ClearNumberingPending(ctx, sr.ID); err != nil {
		s.log.Warn("series: couldn't clear the numbering proposal", "series", sr.Title, "err", err)
	}
}

// Numbering is a show's numbering source and the renumber waiting for review, if any.
type Numbering struct {
	Source  string            `json:"source"`
	Pending *NumberingPending `json:"pending"`
}

// NumberingFor returns the show's numbering source and its pending proposal (nil when
// there's none, or the owner dismissed it).
func (s *Service) NumberingFor(ctx context.Context, id int64) (Numbering, error) {
	sr, err := s.repo.Get(ctx, id)
	if err != nil {
		return Numbering{}, err
	}
	p, err := s.repo.NumberingPendingFor(ctx, id)
	if err != nil {
		return Numbering{}, err
	}
	return Numbering{Source: sr.NumberingSource, Pending: p}, nil
}

// ApplyNumbering applies the proposal the owner reviewed. It re-fetches the metadata and
// re-plans under the show's refresh lock, and rebuilds only when the fresh plan is the
// one they saw (planHash); otherwise nothing moves and ErrStalePlan says to look again —
// the refresh it ran has stored the current plan for review. The caller renames the
// returned remaps on disk.
func (s *Service) ApplyNumbering(ctx context.Context, id int64, planHash string) (RefreshResult, error) {
	if planHash == "" {
		return RefreshResult{}, ErrStalePlan
	}
	unlock := s.lockSeries(id)
	defer unlock()
	_, res, err := s.refresh(ctx, id, RefreshOptions{ApplyPlan: planHash})
	if err != nil {
		return res, err
	}
	if !res.Rebuilt {
		return res, ErrStalePlan
	}
	return res, nil
}

// DismissNumbering hides the pending proposal and leaves everything as it is. The same
// plan isn't proposed again; a different one is.
func (s *Service) DismissNumbering(ctx context.Context, id int64) error {
	if err := s.repo.DismissNumberingPending(ctx, id); err != nil {
		return err
	}
	s.AddEvent(ctx, id, "numbering", "Numbering change dismissed — the stored numbering stays")
	return nil
}

// SetMonitored pauses or resumes a series (see Repo.SetMonitored: the flag is a gate).
func (s *Service) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	return s.repo.SetMonitored(ctx, id, monitored)
}

// SetMonitorNewSeasons sets whether seasons new to the show are monitored.
func (s *Service) SetMonitorNewSeasons(ctx context.Context, id int64, on bool) error {
	return s.repo.SetMonitorNewSeasons(ctx, id, on)
}

// MonitorChange is one edit to a show's monitoring; nil and "" fields are left alone.
type MonitorChange struct {
	Monitored  *bool  // the pause gate
	Preset     string // applied first, when set
	NewSeasons *bool  // "monitor new seasons"
}

// SetMonitoring applies a monitoring edit. A preset decides every episode flag, so the
// gate then changes alone; without one, turning on a show with nothing monitored still
// monitors every regular episode (see Repo.SetMonitored). An unknown preset changes
// nothing and returns ErrUnknownPreset.
func (s *Service) SetMonitoring(ctx context.Context, id int64, ch MonitorChange) error {
	if ch.Preset != "" && !ValidPreset(ch.Preset) {
		return fmt.Errorf("%w: %q", ErrUnknownPreset, ch.Preset)
	}
	if ch.Preset != "" {
		if err := s.repo.ApplyMonitorPreset(ctx, id, ch.Preset, ch.NewSeasons); err != nil {
			return err
		}
	} else if ch.NewSeasons != nil {
		if err := s.repo.SetMonitorNewSeasons(ctx, id, *ch.NewSeasons); err != nil {
			return err
		}
	}
	if ch.Monitored == nil {
		return nil
	}
	if ch.Preset != "" {
		return s.repo.SetMonitoredFlag(ctx, id, *ch.Monitored)
	}
	return s.repo.SetMonitored(ctx, id, *ch.Monitored)
}

// SetSeasonMonitored toggles a whole season and its episodes.
func (s *Service) SetSeasonMonitored(ctx context.Context, seriesID, seasonNumber int64, monitored bool) error {
	return s.repo.SetSeasonMonitored(ctx, seriesID, seasonNumber, monitored)
}

// SetEpisodeMonitored toggles a single episode.
func (s *Service) SetEpisodeMonitored(ctx context.Context, episodeID int64, monitored bool) error {
	return s.repo.SetEpisodeMonitored(ctx, episodeID, monitored)
}

// SetQualityProfile changes a series' quality profile. A real change ends every episode's
// upgrade hold.
func (s *Service) SetQualityProfile(ctx context.Context, id int64, profile string) error {
	return s.repo.SetQualityProfile(ctx, id, profile)
}

// HoldUpgrades keeps the given episodes' files out of profile-driven upgrades ("keep
// existing files"), returning how many it held.
func (s *Service) HoldUpgrades(ctx context.Context, episodeIDs []int64) (int, error) {
	return s.repo.HoldUpgrades(ctx, episodeIDs)
}

// ResumeUpgrades ends the upgrade hold on a show's episodes — one season's when season >= 0,
// all of them otherwise — returning how many were held. The show must exist.
func (s *Service) ResumeUpgrades(ctx context.Context, id int64, season int) (int, error) {
	if _, err := s.repo.Get(ctx, id); err != nil {
		return 0, err
	}
	n, err := s.repo.ResumeUpgrades(ctx, id, season)
	if err == nil && n > 0 {
		what := "Upgrades resumed"
		if season >= 0 {
			what = fmt.Sprintf("Upgrades resumed for season %d", season)
		}
		s.repo.AddEvent(ctx, id, "upgrades.resumed", what)
	}
	return n, err
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

// ErrFilesNotRemoved means a series delete stopped because a file couldn't go to the
// recycle bin. The show stays in the library; the summary says what moved and what didn't.
var ErrFilesNotRemoved = errors.New("some files couldn't be moved to the recycle bin — the series was kept")

// DeletePlan is what deleting a series with its files would touch, for the dialog to show
// before anything happens.
type DeletePlan struct {
	Files    int      `json:"files"`    // distinct episode video files (a double episode counts once)
	Sidecars int      `json:"sidecars"` // subtitle files that travel with them
	Bytes    int64    `json:"bytes"`    // videos plus subtitles
	Paths    []string `json:"-"`        // the videos, in delete order
}

// DeleteSummary reports what a delete did with the files.
type DeleteSummary struct {
	Moved  []string `json:"moved"`
	Failed []string `json:"failed"`
	Bytes  int64    `json:"bytes"`
}

// DeletePlan lists a series' episode files and their subtitles without changing anything.
func (s *Service) DeletePlan(ctx context.Context, id int64) (DeletePlan, error) {
	seasons, err := s.repo.SeasonsFor(ctx, id)
	if err != nil {
		return DeletePlan{}, err
	}
	var plan DeletePlan
	seen := map[string]bool{}
	for _, sn := range seasons {
		for _, e := range sn.Episodes {
			if !e.HasFile || e.FilePath == "" || seen[e.FilePath] {
				continue
			}
			seen[e.FilePath] = true
			fi, err := os.Stat(e.FilePath)
			if err != nil {
				continue // already gone from disk: nothing to move
			}
			plan.Paths = append(plan.Paths, e.FilePath)
			plan.Files++
			plan.Bytes += fi.Size()
			for _, sub := range library.Sidecars(e.FilePath) {
				plan.Sidecars++
				if si, err := os.Stat(sub); err == nil {
					plan.Bytes += si.Size()
				}
			}
		}
	}
	sort.Strings(plan.Paths)
	return plan, nil
}

// Delete removes a series. With deleteFiles, every episode video and its subtitles go to
// the recycle bin first (or are deleted when the bin is off) and emptied season/series
// folders are pruned. The show's rows are deleted only once every file has moved: on the
// first file the bin refuses, it stops, marks the episodes already moved as missing so
// the library tells the truth, keeps the series, and returns ErrFilesNotRemoved.
func (s *Service) Delete(ctx context.Context, id int64, deleteFiles bool) (DeleteSummary, error) {
	sum := DeleteSummary{Moved: []string{}, Failed: []string{}}
	if !deleteFiles {
		return sum, s.repo.Delete(ctx, id)
	}
	seasons, err := s.repo.SeasonsFor(ctx, id)
	if err != nil {
		return sum, err
	}
	plan, err := s.DeletePlan(ctx, id)
	if err != nil {
		return sum, err
	}
	bin := s.bin
	if bin == nil {
		bin = library.SingleBin("")
	}
	// Pre-flight: a bin that can't even be created fails before a single file moves.
	if len(plan.Paths) > 0 {
		dir, err := bin.For(plan.Paths[0])
		if err == nil {
			err = os.MkdirAll(dir, 0o755)
		}
		if err != nil && !errors.Is(err, library.ErrRecycleDisabled) {
			sum.Failed = append(sum.Failed, filepath.Base(plan.Paths[0]))
			s.repo.AddEvent(ctx, id, "delete.failed", "Couldn't use the recycle bin, so nothing was deleted: "+err.Error())
			return sum, fmt.Errorf("%w: %v", ErrFilesNotRemoved, err)
		}
	}

	dirs := map[string]bool{}
	var movedVideos []string
	var failure error
	for _, video := range plan.Paths {
		subs := library.Sidecars(video)
		size := int64(0)
		if fi, err := os.Stat(video); err == nil {
			size = fi.Size()
		}
		if _, err := library.RemoveToBin(bin, video); err != nil {
			sum.Failed = append(sum.Failed, filepath.Base(video))
			failure = err
			break
		}
		movedVideos = append(movedVideos, video)
		sum.Moved = append(sum.Moved, filepath.Base(video))
		sum.Bytes += size
		s.fileRemoved(video)
		dirs[filepath.Dir(video)] = true               // season folder
		dirs[filepath.Dir(filepath.Dir(video))] = true // series folder
		for _, sub := range subs {
			if _, err := library.RemoveToBin(bin, sub); err != nil {
				sum.Failed = append(sum.Failed, filepath.Base(sub))
				failure = err
				break
			}
			sum.Moved = append(sum.Moved, filepath.Base(sub))
			s.fileRemoved(sub)
		}
		if failure != nil {
			break
		}
	}
	s.pruneEmptyDirs(dirs)
	// Plex drops what went, even when the delete stopped partway.
	shows := map[string]bool{}
	for _, v := range movedVideos {
		shows[ShowFolder(v)] = true
	}
	for d := range shows {
		s.changed(d)
	}

	if failure != nil {
		// The moved videos are in the bin now; their episodes must read as missing.
		moved := map[string]bool{}
		for _, v := range movedVideos {
			moved[v] = true
		}
		for _, sn := range seasons {
			for _, e := range sn.Episodes {
				if moved[e.FilePath] {
					if err := s.repo.ClearEpisodeFile(ctx, id, e.SeasonNumber, e.EpisodeNumber); err != nil {
						s.log.Warn("series: couldn't mark a moved episode missing", "err", err)
					}
				}
			}
		}
		s.repo.AddEvent(ctx, id, "delete.failed", fmt.Sprintf("Stopped deleting after %d file(s): %v", len(sum.Moved), failure))
		return sum, fmt.Errorf("%w: %v", ErrFilesNotRemoved, failure)
	}
	return sum, s.repo.Delete(ctx, id)
}

// pruneEmptyDirs removes folders a delete emptied, deepest first so a season folder goes
// before its series folder. Only empty folders inside the library root are removed —
// never the root itself, even when the last show in it was just deleted.
func (s *Service) pruneEmptyDirs(dirs map[string]bool) {
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		if root := s.libRoot(); root != "" {
			rel, err := filepath.Rel(root, d)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				continue
			}
		}
		ordered = append(ordered, d)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, d := range ordered {
		_ = os.Remove(d) // only succeeds when empty
	}
}

// AddEvent appends a timeline event for a series.
func (s *Service) AddEvent(ctx context.Context, id int64, event, detail string) {
	s.repo.AddEvent(ctx, id, event, detail)
}

// SetConvertedFrom records what the file at path was before Convert shrank it, on every
// episode it serves: its recorded release and sizeBytes (0 = unknown). Convert calls it
// before the repoint and the codec restamp. An episode with a baseline keeps its first.
func (s *Service) SetConvertedFrom(ctx context.Context, seriesID int64, path string, sizeBytes int64) error {
	return s.repo.SetConvertedFromForPath(ctx, seriesID, path, sizeBytes)
}

// RepointEpisodeFile updates every episode served by oldPath to point at newPath. Returns
// how many episode records were moved — more than one for a double-length episode file.
func (s *Service) RepointEpisodeFile(ctx context.Context, seriesID int64, oldPath, newPath string, size int64) (int64, error) {
	// The pre-conversion baseline stays: a repoint is the same content under a new path.
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
	// Usually path = <root>/<Series Folder>/Season NN/<file> and the series folder is
	// two up — but only when the parent actually IS a season folder. A library storing
	// files directly under <root>/<Show>/ used to walk two levels up to the TV ROOT
	// itself, so every later grab imported into <root>/<rootname>/Season N/… — a bogus
	// nested library folder.
	return filepath.Base(ShowFolder(path))
}

// FolderPath is the full path of the show's library folder — the folder above a season
// folder, or the episode's own folder in a flat layout — taken from any episode on disk
// ("" when the show has no files).
func (s *Service) FolderPath(ctx context.Context, seriesID int64) string {
	path, err := s.repo.AnyEpisodeFilePath(ctx, seriesID)
	if err != nil || path == "" {
		return ""
	}
	return ShowFolder(path)
}

// ShowFolder is the show folder an episode file lives in: two levels up when its folder
// is a season folder ("Season 04", "Specials"), one level up otherwise.
func ShowFolder(episodePath string) string {
	parent := filepath.Dir(episodePath)
	if isSeasonFolder(filepath.Base(parent)) {
		return filepath.Dir(parent)
	}
	return parent
}

// reSeasonFolder matches the folder names episode files are stored under inside a
// show's folder ("Season 4", "Season 04", "Specials").
var reSeasonFolder = regexp.MustCompile(`(?i)^(season[ ._-]?\d+|specials)$`)

func isSeasonFolder(name string) bool { return reSeasonFolder.MatchString(name) }

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
		return res, fmt.Errorf("series metadata isn't configured — add a TMDB key in Settings → System → API keys")
	}
	root := rootOverride
	if root == "" {
		root = s.libRoot()
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
		if !e.IsDir() || e.Name() == "" || library.SkipScanDir(e.Name()) {
			continue // series live in per-show folders; skip the recycle bins and other hidden ones
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
		root = s.libRoot()
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
	old, _ := s.repo.EpisodeFilePath(ctx, seriesID, season, episode)
	if old != path {
		// A new file ends the episode's upgrade hold: the hold kept the file it had. (Convert
		// and rename go through RepointEpisodeFile / MarkEpisodeImported and keep it.)
		if err := s.repo.ClearEpisodeHold(ctx, seriesID, season, episode); err != nil {
			s.log.Warn("series: clear upgrade hold failed", "err", err)
		}
	}
	// A new file: what an earlier one was before Convert shrank it says nothing about this
	// one. Convert's own path changes (RepointEpisodeFile, MarkEpisodeImported) keep it.
	if err := s.repo.ClearEpisodeConvertedFrom(ctx, seriesID, season, episode); err != nil {
		s.log.Warn("series: clear pre-conversion baseline failed", "err", err)
	}
	if old != "" && old != path {
		// A double-episode file serves several episode rows. If any sibling still
		// points at the old path, recycling it would yank the file out from under
		// them — the library would claim a file that's sitting in the recycle bin.
		// Leave it on disk for the siblings and only repoint this episode.
		if n, _ := s.repo.EpisodesSharingPath(ctx, seriesID, old, season, episode); n > 0 {
			s.log.Info("series: old file still serves other episodes — keeping it",
				"old", old, "shared_by", n, "new", path)
		} else if _, err := os.Stat(old); err == nil {
			// The old release's subtitles go with it: left behind they'd sit unpaired next to
			// the new file. A same-name container swap keeps them (they pair with the new
			// video too).
			var subs []string
			if !library.SharesBase(old) {
				subs = library.PairedSidecars(old)
			}
			// The new file is already in place, so an old file the bin refuses is kept on
			// disk (and said so) rather than deleted for good — and its subtitles stay with it.
			if _, rerr := library.RemoveToBin(s.bin, old); rerr != nil {
				s.log.Warn("series: the recycle bin refused the superseded file — kept it on disk", "old", old, "err", rerr)
				s.repo.AddEvent(ctx, seriesID, "file.kept",
					fmt.Sprintf("S%02dE%02d old file kept: the recycle bin refused it (%v)", season, episode, rerr))
			} else {
				for _, sub := range subs {
					if _, serr := library.RemoveToBin(s.bin, sub); serr != nil {
						s.log.Warn("series: subtitle left behind", "path", sub, "err", serr)
					}
				}
				s.log.Info("series: superseded old episode file", "old", old, "new", path, "subtitles", len(subs))
			}
		}
	}
	return s.MarkEpisodeImported(ctx, seriesID, season, episode, path, size)
}

// EpisodeExists reports whether the series' metadata actually has this episode.
// The import path uses it to refuse "phantom" placements — a release numbered
// beyond the metadata (TVDB-vs-TMDB count mismatches) used to be hardlinked into
// the library and counted as imported while no episode row tracked it.
func (s *Service) EpisodeExists(ctx context.Context, seriesID int64, season, episode int) bool {
	return s.repo.EpisodeExists(ctx, seriesID, season, episode)
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

// LibraryEpisodeFiles lists every episode with a file across the library, with its show's
// title, profile and monitoring — one query, for whole-library passes.
func (s *Service) LibraryEpisodeFiles(ctx context.Context) ([]LibraryEpisodeFile, error) {
	return s.repo.LibraryEpisodeFiles(ctx)
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

// LastEventID is the id of the series' newest history event (0 when none), so a page can
// tell when there is something new to show without re-reading the history.
func (s *Service) LastEventID(ctx context.Context, seriesID int64) int64 {
	return s.repo.LastEventID(ctx, seriesID)
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

// SearchCursors returns where the last sweep stopped in the season fan-out and the
// absolute-number follow-up, so the next one resumes rather than restarting.
func (s *Service) SearchCursors(ctx context.Context, seriesID int64) (int, int) {
	return s.repo.SearchCursors(ctx, seriesID)
}

// SetSeasonCursor / SetAbsoluteCursor advance those cursors.
func (s *Service) SetSeasonCursor(ctx context.Context, seriesID int64, cursor int) {
	s.repo.SetSeasonCursor(ctx, seriesID, cursor)
}
func (s *Service) SetAbsoluteCursor(ctx context.Context, seriesID int64, cursor int) {
	s.repo.SetAbsoluteCursor(ctx, seriesID, cursor)
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

// MatchByTitle finds a series whose normalized title matches. It knows nothing of years,
// countries or aliases, so it is only for titles that come from metadata (a watchlist);
// a release name goes through MatchRelease.
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
//
// The importer passes the library show's own title and year, so when two shows share a
// title (Doctor Who 1963 and 2005) the year picks the right one. Without a year to tell
// them apart it names no episode rather than borrow the other show's episode title.
func (s *Service) EpisodeTitleByName(ctx context.Context, seriesTitle string, year, season, episode int) string {
	all, err := s.repo.List(ctx)
	if err != nil {
		return ""
	}
	key := normKey(seriesTitle)
	var same []Series
	for _, sr := range all {
		if normKey(sr.Title) == key {
			same = append(same, sr)
		}
	}
	var pick *Series
	switch {
	case len(same) == 1:
		pick = &same[0]
	case len(same) > 1 && year > 0:
		for i := range same {
			if same[i].Year == year {
				pick = &same[i]
				break
			}
		}
	}
	if pick == nil {
		return ""
	}
	return s.repo.EpisodeTitle(ctx, pick.ID, season, episode)
}

// normKey is parser.TitleKey, the one title normalizer shared with automation and the
// Downloads feed: accents fold, "&" and "and" agree, and alternate titles in trailing
// brackets ("My Hero Academia (Boku no Hero Academia)") drop out.
func normKey(str string) string { return parser.TitleKey(str) }

// NormTitle exposes the title-normalization used for matching.
func NormTitle(s string) string { return parser.TitleKey(s) }

// extraFrom projects metadata into the stored extra blob.
func extraFrom(d *metadata.SeriesDetails) *SeriesExtra {
	ex := &SeriesExtra{Genres: d.Genres, BackdropURL: d.BackdropURL, OriginalLanguage: d.OriginalLang, OriginCountry: d.OriginCountry}
	// Keep the original-language title only when it differs from the display title, so
	// anime searches can also query the name releases are actually tagged with.
	if d.OriginalName != "" && !strings.EqualFold(d.OriginalName, d.Title) {
		ex.OriginalTitle = d.OriginalName
	}
	for _, c := range d.Cast {
		ex.Cast = append(ex.Cast, CastMember{Name: c.Name, Character: c.Character, ProfileURL: c.ProfileURL})
	}
	return ex
}
