package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/series"
)

func (a *api) handleListSeries(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Series.List(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list series")
		return
	}
	if list == nil {
		list = []series.Series{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"series":             list,
		"metadata_available": a.deps.Series.MetadataAvailable(),
	})
}

func (a *api) handleLookupSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		a.writeError(w, http.StatusBadRequest, "missing ?q= query")
		return
	}
	if !a.deps.Series.MetadataAvailable() {
		a.writeError(w, http.StatusBadRequest, "metadata isn't configured — add a TMDB key in Settings → API keys")
		return
	}
	results, err := a.deps.Series.Lookup(r.Context(), q)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (a *api) handleAddSeries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TMDBID         int    `json:"tmdb_id"`
		QualityProfile string `json:"quality_profile"`
		Monitored      *bool  `json:"monitored"`
		SearchOnAdd    *bool  `json:"search_on_add"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.TMDBID == 0 {
		a.writeError(w, http.StatusBadRequest, "tmdb_id is required")
		return
	}
	monitored := true
	if req.Monitored != nil {
		monitored = *req.Monitored
	}
	// "Search on add" mirrors movies: off means "just add it, don't go get it," so
	// add it unmonitored (the periodic sweep won't chase it until the user monitors).
	searchOnAdd := a.deps.Settings.GetBool(r.Context(), keySearchOnAdd, true)
	if req.SearchOnAdd != nil {
		searchOnAdd = *req.SearchOnAdd
	}
	if !searchOnAdd {
		monitored = false
	}
	if req.QualityProfile == "" {
		req.QualityProfile = a.deps.Quality.DefaultProfile(r.Context(), "series")
	}
	s, err := a.deps.Series.Add(r.Context(), req.TMDBID, req.QualityProfile, monitored)
	if errors.Is(err, series.ErrExists) {
		a.writeError(w, http.StatusConflict, "that series is already in your library")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if s.Monitored && searchOnAdd {
		sid := s.ID
		a.bg("series auto-search on add", idTarget("series", sid), 5*time.Minute, func(ctx context.Context) error {
			return a.deps.Automation.SearchSeriesNow(ctx, sid)
		})
	}
	a.writeJSON(w, http.StatusCreated, s)
}

// handleSearchSeries triggers a search+grab for one series (manual "search now").
func (a *api) handleSearchSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	a.bg("series manual search", idTarget("series", id), 5*time.Minute, func(ctx context.Context) error {
		return a.deps.Automation.SearchSeriesNow(ctx, id)
	})
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "searching"})
}

func (a *api) handleGetSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	s, err := a.deps.Series.Get(r.Context(), id)
	if errors.Is(err, series.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "series not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load series")
		return
	}
	a.attachEpisodeDownloads(r.Context(), &s)
	a.writeJSON(w, http.StatusOK, s)
}

// attachEpisodeDownloads tags each not-yet-downloaded episode with any in-flight
// download from the live queue, matched by series title + season + episode (a
// season pack — no episode markers — covers every episode in its season).
func (a *api) attachEpisodeDownloads(ctx context.Context, s *series.Series) {
	if a.deps.Downloads == nil || len(s.Seasons) == 0 {
		return
	}
	queue, err := a.deps.Downloads.Queue(ctx)
	if err != nil || len(queue) == 0 {
		return
	}
	want := normKey(s.Title)
	for si := range s.Seasons {
		for ei := range s.Seasons[si].Episodes {
			e := &s.Seasons[si].Episodes[ei]
			if e.HasFile {
				continue
			}
			if d := episodeDownload(queue, want, e.SeasonNumber, e.EpisodeNumber); d != nil {
				e.Download = d
			}
		}
	}
}

// episodeDownload finds an unfinished queue item for the given series episode.
func episodeDownload(queue []download.Item, wantTitle string, season, episode int) *series.EpisodeDownload {
	for i := range queue {
		it := queue[i]
		if it.Progress >= 1 {
			continue // finished — import handles it; not "downloading"
		}
		r := parser.Parse(it.Name)
		// CoversSeason handles multi-season ("S01-07") and complete-series packs, not
		// just a single-season one — otherwise a box set only lit up its first season.
		if normKey(r.Title) != wantTitle || !r.CoversSeason(season) {
			continue
		}
		if len(r.Episodes) > 0 && !containsInt(r.Episodes, episode) {
			continue // a specific-episode release that isn't this one
		}
		return &series.EpisodeDownload{State: it.State, Progress: it.Progress}
	}
	return nil
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func (a *api) handleSetSeriesMonitored(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Monitored bool `json:"monitored"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Series.SetMonitored(r.Context(), id, req.Monitored); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update monitoring")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"monitored": req.Monitored})
}

func (a *api) handleSetSeriesProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		QualityProfile string `json:"quality_profile"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Series.SetQualityProfile(r.Context(), id, req.QualityProfile); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update quality profile")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"quality_profile": req.QualityProfile})
}

func (a *api) handleSetSeriesType(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		SeriesType string `json:"series_type"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Series.SetType(r.Context(), id, req.SeriesType); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"series_type": req.SeriesType})
}

// handleListSceneOverrides returns a series' manual scene-season mappings.
func (a *api) handleListSceneOverrides(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	list := a.deps.Series.SceneOverrides(r.Context(), id)
	if list == nil {
		list = []series.SceneOverride{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"overrides": list})
}

// handleSetSceneOverride pins "scene season N starts at TMDB SxxEyy" for an anime whose
// broadcast cours don't line up with TMDB's numbering.
func (a *api) handleSetSceneOverride(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req series.SceneOverride
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Series.SetSceneOverride(r.Context(), id, req); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, req)
}

// handleDeleteSceneOverride drops one mapping, returning the series to automatic
// resolution (per-cour positional → TheXEM → air-date gap).
func (a *api) handleDeleteSceneOverride(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	season, err := strconv.Atoi(r.PathValue("season"))
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid scene season")
		return
	}
	if err := a.deps.Series.DeleteSceneOverride(r.Context(), id, season); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not remove the mapping")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) handleSetSeasonMonitored(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	season, err := strconv.ParseInt(r.PathValue("season"), 10, 64)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid season")
		return
	}
	var req struct {
		Monitored bool `json:"monitored"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Series.SetSeasonMonitored(r.Context(), id, season, req.Monitored); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update season")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"monitored": req.Monitored})
}

func (a *api) handleSetEpisodeMonitored(w http.ResponseWriter, r *http.Request) {
	eid, ok := a.pathValueID(w, r, "eid")
	if !ok {
		return
	}
	var req struct {
		Monitored bool `json:"monitored"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Series.SetEpisodeMonitored(r.Context(), eid, req.Monitored); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update episode")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"monitored": req.Monitored})
}

func (a *api) handleDeleteSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	deleteFiles := r.URL.Query().Get("delete_files") == "true"
	if deleteFiles {
		sr, err := a.deps.Series.Get(ctx, id)
		if err != nil {
			a.writeError(w, http.StatusNotFound, "series not found")
			return
		}
		plan, err := a.deps.Series.DeletePlan(ctx, id)
		if err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not list the series' files")
			return
		}
		// A very large show needs its title typed — the API asks too, not just the dialog.
		if plan.Bytes > bigSeriesDeleteBytes && r.URL.Query().Get("confirm") != sr.Title {
			a.writeError(w, http.StatusBadRequest, "this deletes over 100 GB — type the series title to confirm")
			return
		}
	}
	// Detached from the request: moving a big show to a bin on another filesystem copies
	// for a long time, and if the browser or a proxy gives up meanwhile, the files would
	// all be in the bin while the series row stayed, still claiming them.
	sum, err := a.deps.Series.Delete(context.WithoutCancel(ctx), id, deleteFiles)
	if errors.Is(err, series.ErrFilesNotRemoved) {
		a.writeJSON(w, http.StatusConflict, map[string]any{
			"status": "error", "message": err.Error(), "moved": sum.Moved, "failed": sum.Failed,
		})
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not delete series")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// bigSeriesDeleteBytes is where deleting a show's files needs its title typed (100 GiB).
// A var only so tests can lower it.
var bigSeriesDeleteBytes int64 = 100 << 30

// handleSeriesDeletePreview says what deleting a series with its files would move and
// where it would go, so the dialog can state it before anything happens.
func (a *api) handleSeriesDeletePreview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	plan, err := a.deps.Series.DeletePlan(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list the series' files")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"files": plan.Files, "sidecars": plan.Sidecars, "bytes": plan.Bytes,
		"confirm_over_bytes": bigSeriesDeleteBytes,
		"recycle":            a.recycleMode(r.Context()),
	})
}

// handleSeriesHistory returns a series' activity timeline.
func (a *api) handleSeriesHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	events, err := a.deps.Series.Events(r.Context(), id, 100)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read history")
		return
	}
	if events == nil {
		events = []series.Event{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// handleScanSeriesLibrary catalogs series already present in the library folder.
func (a *api) handleScanSeriesLibrary(w http.ResponseWriter, r *http.Request) {
	root := a.libTV(r)
	a.bg("series library scan", "series", 10*time.Minute, func(ctx context.Context) error {
		res, err := a.deps.Series.ScanLibrary(ctx, root)
		if err != nil {
			return err
		}
		a.deps.Log.Info("series library scan complete", "imported", res.Imported, "skipped", res.Skipped, "unmatched", len(res.Unmatched))
		a.deps.Bus.Publish("library.scanned", map[string]any{"media": "series", "imported": res.Imported, "unmatched": len(res.Unmatched)})
		return nil
	})
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "scanning"})
}

// handleSeriesUnmatched returns the folders the last scan couldn't identify, each
// with candidate matches to choose from.
func (a *api) handleSeriesUnmatched(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, map[string]any{"unmatched": a.deps.Series.LastUnmatched()})
}

// handleSeriesImportFolder catalogs one library folder as an explicitly chosen
// TMDB series — the manual pick for a folder the scan couldn't identify.
func (a *api) handleSeriesImportFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder string `json:"folder"`
		TMDBID int    `json:"tmdb_id"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if !safeFolder(req.Folder) || req.TMDBID == 0 {
		a.writeError(w, http.StatusBadRequest, "folder and tmdb_id are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := a.deps.Series.ImportFolderAs(ctx, a.libTV(r), req.Folder, req.TMDBID); err != nil {
		a.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "imported"})
}

// handleSeriesReleases runs an interactive search, optionally scoped by ?season= and
// ?episode=, and returns ranked releases without grabbing.
func (a *api) handleSeriesReleases(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	season, episode := releasesScope(r.URL.Query().Get("season"), r.URL.Query().Get("episode"))
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	list, err := a.deps.Automation.RankSeriesReleases(ctx, id, season, episode)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, list)
}

// releasesScope reads the releases search's season/episode params. No season means the
// whole show (-1); season=0 is Specials, so it can't double as "absent" the way a bare
// Atoi would make it.
func releasesScope(seasonParam, episodeParam string) (season, episode int) {
	season = -1
	if seasonParam != "" {
		if n, err := strconv.Atoi(seasonParam); err == nil && n >= 0 {
			season = n
		}
	}
	if season >= 0 && episodeParam != "" {
		if n, err := strconv.Atoi(episodeParam); err == nil && n > 0 {
			episode = n
		}
	}
	return season, episode
}

// handleGrabSeries grabs a chosen release for a series (into the TV category).
func (a *api) handleGrabSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Indexer     string `json:"indexer"`
		DownloadURL string `json:"download_url"`
		Title       string `json:"title"`
		// The modal it was picked from. Absent season = the whole-show search; season 0
		// is Specials, a real season — so these are pointers, not zero-means-unset.
		Season  *int `json:"season"`
		Episode *int `json:"episode"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.DownloadURL == "" {
		a.writeError(w, http.StatusBadRequest, "download_url is required")
		return
	}
	scope, ok := grabScopeOf(req.Season, req.Episode)
	if !ok {
		a.writeError(w, http.StatusBadRequest, "episode needs a season, and neither can be negative")
		return
	}
	if err := a.deps.Automation.GrabForSeries(r.Context(), id, req.Indexer, req.DownloadURL, req.Title, scope); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "grabbed", "title": req.Title})
}

// grabScopeOf turns the optional season/episode of a grab request into its scope. The
// scope decides which episodes skip the import gate, so anything ambiguous is refused
// rather than guessed at — guessing wide would re-open the overwrite it exists to stop.
func grabScopeOf(season, episode *int) (automation.GrabScope, bool) {
	if season == nil {
		if episode != nil {
			return automation.GrabScope{}, false
		}
		return automation.WholeShow, true
	}
	ep := 0
	if episode != nil {
		ep = *episode
	}
	if *season < 0 || ep < 0 {
		return automation.GrabScope{}, false
	}
	return automation.ScopeFor(*season, ep), true
}

// handleAutoGrabSeries is the quick Grab missing (season) / Grab (episode) action. The
// scope is checked here so a bad click gets a 400 rather than a silent background failure;
// the search itself runs in the background and reports its outcome as a 'searched' series
// event and a series.searched bus message.
func (a *api) handleAutoGrabSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Season  int `json:"season"`
		Episode int `json:"episode"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	sc := automation.SeriesScope{Season: req.Season, Episode: req.Episode, Trigger: "grab"}
	if err := sc.Validate(); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.bg("series scope auto-grab", idTarget("series", id), 5*time.Minute, func(ctx context.Context) error {
		_, err := a.deps.Automation.GrabForScope(ctx, id, sc)
		return err
	})
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "searching"})
}

// handleRefreshSeries re-pulls metadata and rescans the disk for a series.
func (a *api) handleRefreshSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	// The owner's own Refresh is the one place a renumber may move files.
	_, rr, err := a.deps.Series.Refresh(ctx, id, series.RefreshOptions{AllowRebuild: true})
	if err != nil {
		if errors.Is(err, series.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "series not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not refresh series")
		return
	}
	// A rebuild moved files to new (season, episode) rows but left them at their old on-disk
	// names; rename brings the library into line before the rescan reads it — through the
	// collision-safe rename, so no file is ever replaced.
	if rr.Renumbered {
		if res, rerr := a.deps.Automation.SeriesRename(ctx, id, nil); rerr != nil {
			a.deps.Log.Warn("series: rename after renumber failed", "series_id", id, "err", rerr)
		} else {
			a.deps.Log.Info("series: renamed files after renumber", "series_id", id, "moved", res.Moved)
			a.deps.Automation.LogRenameSkips(id, res)
		}
	}
	a.deps.Automation.RescanSeries(ctx, id)
	s, err := a.deps.Series.Get(ctx, id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load series")
		return
	}
	a.writeJSON(w, http.StatusOK, s)
}

// handleRefreshAllSeries refreshes metadata and rescans the disk for every series.
//
// The per-series refresh is what reconciles episodes whose file is already on disk but
// whose row still reads as missing — the state that makes the searcher keep grabbing
// releases you already have. Doing that one show at a time is impractical once the
// library is large, which is the whole reason this exists.
//
// Runs in the background: a few hundred series means a few hundred metadata pulls and
// disk walks, far longer than any sensible request timeout. The response says how many
// were queued and the work continues after it returns.
func (a *api) handleRefreshAllSeries(w http.ResponseWriter, r *http.Request) {
	all, err := a.deps.Series.List(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list series")
		return
	}
	// One sweep at a time. Two overlapping runs would double every metadata pull and
	// race each other's episode writes for no benefit.
	if !a.refreshAll.CompareAndSwap(false, true) {
		a.writeError(w, http.StatusConflict, "a refresh of all series is already running")
		return
	}
	ids := make([]int64, 0, len(all))
	for _, s := range all {
		ids = append(ids, s.ID)
	}
	a.bg("refresh all series", "", 2*time.Hour, func(ctx context.Context) error {
		a.refreshSeriesSweep(ctx, ids)
		return nil
	})
	a.writeJSON(w, http.StatusAccepted, map[string]any{"queued": len(ids)})
}

// refreshSeriesSweep walks every series, refreshing metadata and rescanning the disk.
// Detached from the request: ctx is bg's, the run context with a two-hour budget.
func (a *api) refreshSeriesSweep(ctx context.Context, ids []int64) {
	defer a.refreshAll.Store(false)

	a.deps.Log.Info("series: refreshing all", "count", len(ids))
	var refreshed, failed int
	for _, id := range ids {
		// Per-series budget, so one hung metadata call can't stall the whole sweep.
		each, cancelEach := context.WithTimeout(ctx, 60*time.Second)
		// Never a rebuild: refresh-all runs unattended across the whole library, so a
		// numbering change is only noted in History for the owner to apply per show.
		_, _, err := a.deps.Series.Refresh(each, id, series.RefreshOptions{})
		if err != nil {
			failed++
			a.deps.Log.Warn("series: refresh failed", "series_id", id, "err", err)
			cancelEach()
			if ctx.Err() != nil {
				break // the whole sweep timed out or was cancelled
			}
			continue
		}
		a.deps.Automation.RescanSeries(each, id)
		cancelEach()
		refreshed++
		if ctx.Err() != nil {
			break
		}
	}
	a.deps.Log.Info("series: refreshed all", "refreshed", refreshed, "failed", failed)
}

// handleSeriesManualImportList lists importable video files under the downloads dir
// (or ?path=). handleSeriesManualImport imports a chosen one into the series.
func (a *api) handleSeriesManualImportList(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.pathID(w, r); !ok {
		return
	}
	dir, err := a.checkImportPath(r.Context(), r.URL.Query().Get("path"))
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cands := a.deps.Automation.SeriesImportCandidates(dir)
	if cands == nil {
		cands = []automation.SeriesImportCandidate{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"path": dir, "candidates": cands})
}

func (a *api) handleSeriesManualImport(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if !a.decodeJSON(w, r, &req) || req.Path == "" {
		a.writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	src, err := a.checkImportPath(r.Context(), req.Path)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A whole-folder import is long work — a 122-file season pack took 14 minutes — so it
	// runs detached. Holding the request open outlasts any sensible HTTP timeout and
	// leaves the user staring at a spinner, unsure whether navigating away cancels it.
	// Single files stay synchronous: they're quick, and immediate feedback is better.
	if fi, statErr := os.Stat(src); statErr == nil && fi.IsDir() {
		a.bg("series: folder import", idTarget("series", id)+" from "+src, 2*time.Hour, func(ctx context.Context) error {
			return a.deps.Automation.ManualImportSeries(ctx, id, src)
		})
		a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "importing", "background": true})
		return
	}

	if err := a.deps.Automation.ManualImportSeries(r.Context(), id, src); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "imported"})
}

// handleSeriesRenamePreview lists episode files not yet at their canonical path;
// handleSeriesRename applies the moves.
func (a *api) handleSeriesRenamePreview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	items, err := a.deps.Automation.SeriesRenamePreview(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not build rename preview")
		return
	}
	if items == nil {
		items = []automation.SeriesRenameItem{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items, "matches": len(items) == 0})
}

// The body is optional: {items} is the previewed list the user confirmed, and only those
// moves are applied. Without it every pending rename is applied, as before.
func (a *api) handleSeriesRename(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Items []automation.SeriesRenameItem `json:"items"`
	}
	if r.ContentLength != 0 {
		if !a.decodeJSON(w, r, &req) {
			return
		}
		if req.Items == nil {
			req.Items = []automation.SeriesRenameItem{} // "{}" confirms nothing, not everything
		}
	}
	res, err := a.deps.Automation.SeriesRename(r.Context(), id, req.Items)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not rename")
		return
	}
	a.writeJSON(w, http.StatusOK, res)
}

// --- blocklist + per-episode actions (mirrors the movie surface) ---

func (a *api) handleSeriesBlocklist(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	entries, err := a.deps.Automation.BlocklistedSeries(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load blocklist")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"blocklist": entries})
}

func (a *api) handleSeriesBlock(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req blocklistRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Title == "" {
		a.writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if err := a.deps.Automation.BlocklistSeries(r.Context(), id, req.Title, req.Indexer, "manually blocklisted"); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not blocklist")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "blocklisted"})
}

func (a *api) handleSeriesUnblock(w http.ResponseWriter, r *http.Request) {
	bid, ok := a.pathValueID(w, r, "bid")
	if !ok {
		return
	}
	if err := a.deps.Automation.Unblock(r.Context(), bid); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not remove blocklist entry")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// seasonEpisode parses the {season}/{episode} path values.
func (a *api) seasonEpisode(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	season, e1 := strconv.Atoi(r.PathValue("season"))
	episode, e2 := strconv.Atoi(r.PathValue("episode"))
	if e1 != nil || e2 != nil {
		a.writeError(w, http.StatusBadRequest, "invalid season/episode")
		return 0, 0, false
	}
	return season, episode, true
}

// handleRegrabEpisode replaces one episode: blocklist its current release, re-search + grab.
func (a *api) handleRegrabEpisode(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	season, episode, ok := a.seasonEpisode(w, r)
	if !ok {
		return
	}
	if err := (automation.SeriesScope{Season: season, Episode: episode, Replace: true}).Validate(); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.bg("regrab-episode", idTarget("series", id), 5*time.Minute, func(ctx context.Context) error {
		return a.deps.Automation.RegrabEpisode(ctx, id, season, episode)
	})
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "searching"})
}

// handleDeleteEpisodeFile deletes one episode's file (to recycle) and flips it back to wanted.
func (a *api) handleDeleteEpisodeFile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	season, episode, ok := a.seasonEpisode(w, r)
	if !ok {
		return
	}
	if err := a.deps.Series.DeleteEpisodeFile(r.Context(), id, season, episode); err != nil {
		if errors.Is(err, library.ErrBinRefused) {
			a.writeError(w, http.StatusConflict, err.Error())
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not delete episode file")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSeriesDuplicates lists episodes with more than one file on disk. These are copies
// left behind when a re-import derived a different filename (a re-classified source tag, or
// the episode title being added/dropped) — the old file is orphaned and otherwise invisible.
func (a *api) handleSeriesDuplicates(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	dupes, err := a.deps.Automation.SeriesDuplicates(r.Context(), id)
	if err != nil {
		if errors.Is(err, series.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "series not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not scan for duplicates")
		return
	}
	var extras int
	var reclaim int64
	for _, d := range dupes {
		for _, e := range d.Extras {
			extras++
			reclaim += e.SizeBytes
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"duplicates": dupes, "extra_files": extras, "reclaimable_bytes": reclaim,
	})
}

// handleDeleteSeriesDuplicate recycles one duplicate copy. The path must be one the scan
// currently reports as an extra for this series, so the tracked file can't be deleted.
func (a *api) handleDeleteSeriesDuplicate(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		a.writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	if err := a.deps.Automation.DeleteSeriesDuplicate(r.Context(), id, req.Path); err != nil {
		if a.writeBinRefusal(w, err) {
			return
		}
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// handleListAliases returns a series' alternate release titles.
func (a *api) handleListAliases(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	a.writeJSON(w, http.StatusOK, a.deps.Series.Aliases(r.Context(), id))
}

// handleAddAlias records an alternate title the series is released under, optionally
// pinned to a TMDB season so the alias' own numbering is read inside it.
func (a *api) handleAddAlias(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Title      string `json:"title"`
		TMDBSeason int    `json:"tmdb_season"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	al, err := a.deps.Series.AddAlias(r.Context(), id, req.Title, req.TMDBSeason)
	if err != nil {
		// These are all "you typed something that can't work" rather than failures.
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, al)
}

// handleDeleteAlias removes an alternate title.
func (a *api) handleDeleteAlias(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	aliasID, err := strconv.ParseInt(r.PathValue("alias"), 10, 64)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid alias id")
		return
	}
	if err := a.deps.Series.DeleteAlias(r.Context(), id, aliasID); err != nil {
		a.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
