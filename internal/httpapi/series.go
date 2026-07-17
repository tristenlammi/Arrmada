package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
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
		a.writeError(w, http.StatusBadRequest, "metadata isn't configured — set ARRMADA_TMDB_API_KEY")
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
		go func(id int64, title string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := a.deps.Automation.SearchSeriesNow(ctx, id); err != nil {
				a.deps.Log.Warn("series auto-search on add failed", "series", title, "err", err)
			}
		}(s.ID, s.Title)
	}
	a.writeJSON(w, http.StatusCreated, s)
}

// handleSearchSeries triggers a search+grab for one series (manual "search now").
func (a *api) handleSearchSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	go func(sid int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := a.deps.Automation.SearchSeriesNow(ctx, sid); err != nil {
			a.deps.Log.Warn("series manual search failed", "series_id", sid, "err", err)
		}
	}(id)
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
		if normKey(r.Title) != wantTitle || r.Season != season {
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
	deleteFiles := r.URL.Query().Get("delete_files") == "true"
	if err := a.deps.Series.Delete(r.Context(), id, deleteFiles); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not delete series")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	go func() {
		defer cancel()
		res, err := a.deps.Series.ScanLibrary(ctx, root)
		if err != nil {
			a.deps.Log.Warn("series library scan failed", "err", err)
			return
		}
		a.deps.Log.Info("series library scan complete", "imported", res.Imported, "skipped", res.Skipped, "unmatched", len(res.Unmatched))
		a.deps.Bus.Publish("library.scanned", map[string]any{"media": "series", "imported": res.Imported, "unmatched": len(res.Unmatched)})
	}()
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
	season, _ := strconv.Atoi(r.URL.Query().Get("season"))
	episode, _ := strconv.Atoi(r.URL.Query().Get("episode"))
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	list, err := a.deps.Automation.RankSeriesReleases(ctx, id, season, episode)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, list)
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
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.DownloadURL == "" {
		a.writeError(w, http.StatusBadRequest, "download_url is required")
		return
	}
	if err := a.deps.Automation.GrabForSeries(r.Context(), id, req.Indexer, req.DownloadURL, req.Title); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "grabbed", "title": req.Title})
}

// handleAutoGrabSeries auto-grabs the best eligible release for a season/episode
// scope — the per-episode / per-season quick "grab" action.
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
	go func(sid, season, episode int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := a.deps.Automation.GrabBestForScope(ctx, sid, int(season), int(episode)); err != nil {
			a.deps.Log.Warn("series scope auto-grab failed", "series_id", sid, "err", err)
		}
	}(id, int64(req.Season), int64(req.Episode))
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
	if _, err := a.deps.Series.Refresh(ctx, id); err != nil {
		if errors.Is(err, series.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "series not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not refresh series")
		return
	}
	a.deps.Automation.RescanSeries(ctx, id)
	s, err := a.deps.Series.Get(ctx, id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load series")
		return
	}
	a.writeJSON(w, http.StatusOK, s)
}

// handleSeriesManualImportList lists importable video files under the downloads dir
// (or ?path=). handleSeriesManualImport imports a chosen one into the series.
func (a *api) handleSeriesManualImportList(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.pathID(w, r); !ok {
		return
	}
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = a.deps.Config.DownloadsDir
	}
	cands := a.deps.Automation.SeriesImportCandidates(dir)
	if cands == nil {
		cands = []automation.SeriesImportCandidate{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"candidates": cands})
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
	if err := a.deps.Automation.ManualImportSeries(r.Context(), id, req.Path); err != nil {
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

func (a *api) handleSeriesRename(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	moved, err := a.deps.Automation.SeriesRename(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not rename")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"renamed": moved})
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
	go a.bg(func(ctx context.Context) error { return a.deps.Automation.RegrabEpisode(ctx, id, season, episode) }, "regrab-episode", id)
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
		a.writeError(w, http.StatusInternalServerError, "could not delete episode file")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
