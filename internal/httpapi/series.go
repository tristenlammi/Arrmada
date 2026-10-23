package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/libfolders"
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
	// The library page shows posters, counts and status, never a show's overview, cast or
	// alternate titles — and those were four fifths of the list (1.25 MB for 400 shows).
	// The detail endpoint still carries them.
	for i := range list {
		list[i].Overview, list[i].Extra, list[i].Aliases = "", nil, nil
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
		a.writeError(w, http.StatusBadRequest, "metadata isn't configured — add a TMDB key in Settings → Connections")
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
		// Monitor is a monitoring preset ("all", "future", …); "" uses the configured
		// default (Settings → series_monitor_default).
		Monitor           string `json:"monitor"`
		MonitorNewSeasons *bool  `json:"monitor_new_seasons"`
		// FolderID is the TV folder chosen in the add dialog (0 = the default); absent
		// lets the folder rules pick.
		FolderID *int64 `json:"folder_id"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.TMDBID == 0 {
		a.writeError(w, http.StatusBadRequest, "tmdb_id is required")
		return
	}
	if req.Monitor != "" && !series.ValidPreset(req.Monitor) {
		a.writeError(w, http.StatusBadRequest, "unknown monitoring preset "+strconv.Quote(req.Monitor))
		return
	}
	monitored := true
	if req.Monitored != nil {
		monitored = *req.Monitored
	}
	// "Search on add" mirrors movies: off means "just add it, don't go get it," so the
	// show is added paused (the sweep won't chase it until it's resumed). The episode
	// flags still follow the preset, ready for then. It applies to this add only: the
	// setting is just the dialog's starting point.
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
	s, err := a.deps.Series.AddWith(r.Context(), req.TMDBID, req.QualityProfile, series.AddOptions{
		Monitored: monitored, Preset: req.Monitor, MonitorNewSeasons: req.MonitorNewSeasons, FolderID: req.FolderID,
	})
	if errors.Is(err, series.ErrExists) {
		a.writeError(w, http.StatusConflict, "that series is already in your library")
		return
	}
	if errors.Is(err, series.ErrUnknownFolder) {
		a.writeError(w, http.StatusBadRequest, "that TV folder doesn't exist — reload and pick again")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if s.Monitored && searchOnAdd {
		_, _, _ = a.submit(r, triggered(automation.TriggerAdd, a.seriesSearchJob(s.ID)))
	}
	a.writeJSON(w, http.StatusCreated, s)
}

// seriesSearchList is the optional body of POST /series/{id}/search: episodes picked on
// the series page (SER-16). No body, or no episodes, is the whole-show search.
type seriesSearchList struct {
	Episodes []struct {
		Season  int `json:"season"`
		Episode int `json:"episode"`
	} `json:"episodes"`
}

// episodeListScope turns a picked list into one scoped search: every episode in one season
// (the page picks per season), each named once, at most automation.MaxScopeEpisodes. nil
// with no error means there was no list.
func episodeListScope(body seriesSearchList) (*automation.SeriesScope, error) {
	if len(body.Episodes) == 0 {
		return nil, nil
	}
	if len(body.Episodes) > automation.MaxScopeEpisodes {
		return nil, fmt.Errorf("pick at most %d episodes at a time", automation.MaxScopeEpisodes)
	}
	season := body.Episodes[0].Season
	seen := map[int]bool{}
	var eps []int
	for _, e := range body.Episodes {
		if e.Season != season {
			return nil, errors.New("pick episodes from one season at a time")
		}
		if !seen[e.Episode] {
			seen[e.Episode] = true
			eps = append(eps, e.Episode)
		}
	}
	sort.Ints(eps)
	sc := &automation.SeriesScope{Season: season, Episodes: eps, Trigger: "bulk"}
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	return sc, nil
}

// handleSearchSeries triggers a search+grab for one series (manual "search now"). With a
// body naming episodes it searches just those, as one scoped search (GrabForScope) whose
// outcome lands in the show's history and on the bus like any other click.
func (a *api) handleSearchSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	// Read the body whatever Content-Length says: a chunked list must never be mistaken for
	// "no body" and turn into a whole-show search.
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		var body seriesSearchList
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			a.writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		sc, err := episodeListScope(body)
		if err != nil {
			a.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if sc != nil {
			a.searchEpisodeList(w, r, id, *sc)
			return
		}
	}
	// The button's search holds back seasons the client is still downloading
	// (SearchSeriesManual), as the sweep does.
	started := time.Now().UnixMilli()
	jobID, existing, ok := a.submitOr503(w, r, a.seriesManualSearchJob(id))
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "searching", "started_at_ms": started})
}

// searchEpisodeList runs one scoped search for the picked episodes as a job. The target
// names the exact list, so a second pick in the same season isn't mistaken for this one
// already running, while a double click on the same pick is.
func (a *api) searchEpisodeList(w http.ResponseWriter, r *http.Request, id int64, sc automation.SeriesScope) {
	h := fnv.New32a()
	for _, e := range sc.Episodes {
		_, _ = fmt.Fprintf(h, "%d,", e)
	}
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{
		Kind: "series.grab-scope", Target: fmt.Sprintf("series:%d:s%d:list-%08x", id, sc.Season, h.Sum32()),
		Class: jobs.ClassIndexerSearch, Timeout: 5 * time.Minute,
		Fn: interactive(errFn(func(ctx context.Context) error {
			_, err := a.deps.Automation.GrabForScope(ctx, id, sc)
			return err
		}))})
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "searching", "episodes": len(sc.Episodes)})
}

// maxBulkMonitor bounds one bulk monitor request; the page sends at most a season.
const maxBulkMonitor = 2000

// handleSetEpisodesMonitored monitors or unmonitors several of a show's episodes at once
// (the series page's select mode). Ids from another show refuse the whole change.
func (a *api) handleSetEpisodesMonitored(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		EpisodeIDs []int64 `json:"episode_ids"`
		Monitored  bool    `json:"monitored"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if len(req.EpisodeIDs) == 0 {
		a.writeError(w, http.StatusBadRequest, "no episodes picked")
		return
	}
	if len(req.EpisodeIDs) > maxBulkMonitor {
		a.writeError(w, http.StatusBadRequest, fmt.Sprintf("pick at most %d episodes at a time", maxBulkMonitor))
		return
	}
	n, err := a.deps.Series.SetEpisodesMonitored(r.Context(), id, req.EpisodeIDs, req.Monitored)
	if errors.Is(err, series.ErrForeignEpisodes) {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update episodes")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"updated": n, "monitored": req.Monitored})
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
	// Each episode file's audio languages, from the library index (never probed here).
	a.attachSeriesAudio(r.Context(), &s)
	// What the History, Duplicates and Blocklist panels key their reloads on: it moves
	// whenever something is recorded against the show (an import, a grab, a search).
	s.LastEventID = a.deps.Series.LastEventID(r.Context(), id)
	s.Extra = apiSeriesExtra(s.Extra) // display_title and display_overview carry what pages need
	a.writeJSON(w, http.StatusOK, s)
}

// seriesEpisodeDownload is one episode's in-flight download, for the light poll.
type seriesEpisodeDownload struct {
	Season   int     `json:"season"`
	Episode  int     `json:"episode"`
	State    string  `json:"state"`
	Progress float64 `json:"progress"`
	Hash     string  `json:"hash,omitempty"`
}

// handleSeriesDownloads is what the series page polls while something downloads: only the
// episodes with a download in flight, instead of the whole show (seasons, stats, every
// episode) every three seconds. The page reloads the full detail when an import or search
// for the show is announced.
func (a *api) handleSeriesDownloads(w http.ResponseWriter, r *http.Request) {
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
	out := []seriesEpisodeDownload{}
	for _, sn := range s.Seasons {
		for _, e := range sn.Episodes {
			if e.Download != nil {
				out = append(out, seriesEpisodeDownload{Season: e.SeasonNumber, Episode: e.EpisodeNumber,
					State: e.Download.State, Progress: e.Download.Progress, Hash: e.Download.Hash})
			}
		}
	}
	a.writeJSON(w, http.StatusOK, out)
}

// attachEpisodeDownloads tags each not-yet-downloaded episode with any in-flight download
// from the live queue (a season pack — no episode markers — covers every episode in its
// season).
//
// The queue is read once per request through download.Service's public Queue and each item
// parsed once: this used to parse every queue item again for every file-less episode.
// When the shared queue snapshot (ACQ-22) lands, the Queue call is what switches to it.
func (a *api) attachEpisodeDownloads(ctx context.Context, s *series.Series) {
	if a.deps.Downloads == nil || len(s.Seasons) == 0 {
		return
	}
	queue, err := a.deps.Downloads.Queue(ctx)
	if err != nil || len(queue) == 0 {
		return
	}
	idx := indexEpisodeDownloadsFit(queue, *s, parser.Parse, a.deps.Series.SearchFit(ctx))
	for si := range s.Seasons {
		for ei := range s.Seasons[si].Episodes {
			e := &s.Seasons[si].Episodes[ei]
			if e.HasFile {
				continue
			}
			if d := idx.lookup(e.SeasonNumber, e.EpisodeNumber); d != nil {
				e.Download = d
			}
		}
	}
}

// episodeDownloads indexes a show's in-flight downloads by what they cover.
type episodeDownloads struct {
	whole   *series.EpisodeDownload // a complete-series pack
	seasons map[int]*seasonDownloads
}

type seasonDownloads struct {
	pack *series.EpisodeDownload         // a pack of the whole season (or several)
	eps  map[int]*series.EpisodeDownload // single- or multi-episode releases
}

// indexEpisodeDownloads makes one pass over the queue: incomplete TV downloads whose name
// is this show (series.FitRelease — the same identity rule as the sweeps, so an alias or
// romaji-named download counts and another show's never does), each parsed once. parse is
// injectable so a test can count the parses.
func indexEpisodeDownloads(queue []download.Item, s series.Series, parse func(string) parser.Release) episodeDownloads {
	return indexEpisodeDownloadsFit(queue, s, parse, nil)
}

// indexEpisodeDownloadsFit is indexEpisodeDownloads by a search's rule (series.SearchFit):
// a download a search took by the show's translated title shows on its episodes too.
func indexEpisodeDownloadsFit(queue []download.Item, s series.Series, parse func(string) parser.Release, fit *series.SearchFit) episodeDownloads {
	idx := episodeDownloads{seasons: map[int]*seasonDownloads{}}
	season := func(n int) *seasonDownloads {
		sd := idx.seasons[n]
		if sd == nil {
			sd = &seasonDownloads{eps: map[int]*series.EpisodeDownload{}}
			idx.seasons[n] = sd
		}
		return sd
	}
	for _, it := range queue {
		if it.Progress >= 1 || it.Category != download.CategoryTV {
			continue // finished (import handles it, or it's seeding), or not a TV grab
		}
		p := parse(it.Name)
		if !fit.Fit(p, s).OK {
			continue
		}
		d := &series.EpisodeDownload{State: it.State, Progress: it.Progress, Hash: it.Hash}
		if p.Complete {
			if idx.whole == nil {
				idx.whole = d
			}
			continue
		}
		covered := append([]int{}, p.Seasons...)
		if p.Season > 0 || p.SeasonExplicit {
			covered = append(covered, p.Season)
		}
		for _, sn := range covered {
			sd := season(sn)
			if len(p.Episodes) > 0 && sn == p.Season {
				for _, ep := range p.Episodes {
					if sd.eps[ep] == nil {
						sd.eps[ep] = d
					}
				}
			} else if len(p.Episodes) == 0 && sd.pack == nil {
				sd.pack = d
			}
		}
	}
	return idx
}

// lookup is the download covering one episode: its own release first, then a pack of its
// season, then a complete-series pack.
func (x episodeDownloads) lookup(season, episode int) *series.EpisodeDownload {
	if sd := x.seasons[season]; sd != nil {
		if d := sd.eps[episode]; d != nil {
			return d
		}
		if sd.pack != nil {
			return sd.pack
		}
	}
	return x.whole
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
	// All optional: the "monitor new seasons" checkbox mustn't turn searching off by
	// leaving monitored out. A preset applies first and turns searching on unless
	// monitored says otherwise (series.SetMonitoring).
	var req struct {
		Monitored         *bool  `json:"monitored"`
		Preset            string `json:"preset"`
		MonitorNewSeasons *bool  `json:"monitor_new_seasons"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	before, err := a.deps.Series.Get(ctx, id)
	if errors.Is(err, series.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "series not found")
		return
	}
	if err != nil {
		// Without the show as it was, "resumed" can't be told: change nothing.
		a.writeError(w, http.StatusInternalServerError, "could not load series")
		return
	}
	err = a.deps.Series.SetMonitoring(ctx, id, series.MonitorChange{
		Monitored: req.Monitored, Preset: req.Preset, NewSeasons: req.MonitorNewSeasons,
	})
	if errors.Is(err, series.ErrUnknownPreset) {
		a.writeError(w, http.StatusBadRequest, "unknown monitoring preset "+strconv.Quote(req.Preset))
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update monitoring")
		return
	}
	s, err := a.deps.Series.Get(ctx, id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load series")
		return
	}
	// resumed: this change turned the show's searching on, so the page can say so.
	a.writeJSON(w, http.StatusOK, map[string]any{
		"monitored": s.Monitored, "monitor_new_seasons": s.MonitorNewSeasons, "monitor_preset": s.MonitorPreset,
		"resumed": s.Monitored && !before.Monitored,
	})
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

type bulkDeleteSeasonsRequest struct {
	Seasons     []int  `json:"seasons"`
	DeleteFiles bool   `json:"delete_files"`
	Confirm     string `json:"confirm,omitempty"`
}

type bulkDeleteSeasonsReply struct {
	DeletedSeasons []int    `json:"deleted_seasons"`
	DeletedFiles   int      `json:"deleted_files"`
	Bytes          int64    `json:"bytes"`
	Errors         []string `json:"errors,omitempty"`
}

// handleDeleteSeasonsBulk deletes multiple seasons of a series at once.
// If delete_files is true, episode files and sidecars are moved to the recycle bin (or hard-deleted).
func (a *api) handleDeleteSeasonsBulk(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req bulkDeleteSeasonsRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if len(req.Seasons) == 0 {
		a.writeError(w, http.StatusBadRequest, "no seasons specified")
		return
	}
	ctx := r.Context()
	// A bulk delete of many seasons can still be huge, so the same "type the title"
	// guard as a whole-series delete applies to the bytes actually selected.
	if req.DeleteFiles {
		sr, err := a.deps.Series.Get(ctx, id)
		if err != nil {
			a.writeError(w, http.StatusNotFound, "series not found")
			return
		}
		plan, err := a.deps.Series.SeasonsDeletePlan(ctx, id, req.Seasons)
		if err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not list the seasons' files")
			return
		}
		if plan.Bytes > bigSeriesDeleteBytes && req.Confirm != sr.Title {
			a.writeError(w, http.StatusBadRequest, "this deletes over 100 GB — type the series title to confirm")
			return
		}
	}
	res, err := a.deps.Series.DeleteSeasons(context.WithoutCancel(ctx), id, req.Seasons, req.DeleteFiles)
	if errors.Is(err, series.ErrFilesNotRemoved) {
		a.writeJSON(w, http.StatusConflict, map[string]any{
			"status": "error", "message": err.Error(), "deleted_seasons": res.DeletedSeasons, "deleted_files": res.DeletedFiles, "bytes": res.Bytes, "errors": res.Errors,
		})
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not delete seasons")
		return
	}
	a.writeJSON(w, http.StatusOK, bulkDeleteSeasonsReply{
		DeletedSeasons: res.DeletedSeasons,
		DeletedFiles:   res.DeletedFiles,
		Bytes:          res.Bytes,
		Errors:         res.Errors,
	})
}

// bigSeriesDeleteBytes is where deleting a show's files needs its title typed (100 GiB).
// A var only so tests can lower it.
var bigSeriesDeleteBytes int64 = 100 << 30

// handleSeriesDeletePreview answers what deleting the show (or the seasons named in
// ?seasons=1,2, for a bulk season delete) would move, and where it would go.
func (a *api) handleSeriesDeletePreview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var seasons []int
	if q := r.URL.Query().Get("seasons"); q != "" {
		for _, part := range strings.Split(q, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				a.writeError(w, http.StatusBadRequest, "seasons must be a comma-separated list of numbers")
				return
			}
			seasons = append(seasons, n)
		}
	}
	plan, err := a.deps.Series.SeasonsDeletePlan(r.Context(), id, seasons)
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

// handleScanSeriesLibrary catalogs series already present in the library folders (every
// TV folder, when there are several). An optional body {root} scans just that TV folder.
func (a *api) handleScanSeriesLibrary(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Root string `json:"root"`
	}
	if r.ContentLength > 0 && !a.decodeJSON(w, r, &in) {
		return
	}
	scope, ok := a.scanScope(w, r, libfolders.MediaSeries, in.Root)
	if !ok {
		return
	}
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "series.scan", Target: scope.target(), Class: jobs.ClassLibraryScan, Timeout: libraryScanTimeout,
		Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
			if err := scope.recheck(libfolders.MediaSeries); err != nil {
				return nil, err
			}
			// Progress rides the job (job.updated on the live socket), as the movie scan's does.
			res, err := a.deps.Series.ScanLibrary(ctx, scope.Root, func(done, total int) {
				if total > 0 {
					p.Set(float64(done)/float64(total), fmt.Sprintf("Scanning %s / %s show folders", thousands(done), thousands(total)))
				}
			})
			if err != nil {
				return nil, err
			}
			a.deps.Log.Info("series library scan complete", "imported", res.Imported, "skipped", res.Skipped, "unmatched", len(res.Unmatched))
			a.deps.Bus.Publish("library.scanned", map[string]any{"media": "series", "imported": res.Imported, "unmatched": len(res.Unmatched)})
			p.SetMessage(scanMessage("series", res.Imported, len(res.Unmatched)) +
				unreadFolders(len(res.FailedFolders), func(i int) string { return res.FailedFolders[i].Root }))
			return scanSummary{Imported: res.Imported, Skipped: res.Skipped, Unmatched: len(res.Unmatched)}, nil
		}})
	if !ok {
		return
	}
	if existing {
		// One scan at a time, as for movies: the page follows the one that's running (job_id).
		a.alreadyRunning(w, jobID, scope.running("a TV library scan is already running"))
		return
	}
	a.accepted(w, jobID, false, map[string]any{"status": "scanning"})
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
		// Root is the TV folder the scan found it in ("" = the default). The service
		// accepts only one of the library's folders.
		Root string `json:"root"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if !safeFolder(req.Folder) || req.TMDBID == 0 {
		a.writeError(w, http.StatusBadRequest, "folder and tmdb_id are required")
		return
	}
	root := strings.TrimSpace(req.Root)
	if root == "" {
		root = a.libTV(r)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := a.deps.Series.ImportFolderAs(ctx, root, req.Folder, req.TMDBID); err != nil {
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
	ctx, cancel := context.WithTimeout(indexer.WithInteractive(r.Context()), 90*time.Second)
	defer cancel()
	list, err := a.deps.Automation.RankSeriesReleases(ctx, id, season, episode)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// The search's own scope rides on the token: it becomes the grab's scope, which
	// decides which episodes skip the import gate.
	a.tokenize(r, &list, automation.ReleaseRef{MediaKind: automation.ReleaseKindSeries, MediaID: id, Season: season, Episode: episode})
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
//
//	POST /api/v1/series/{id}/grab  {token}
//
// The token comes from this show's interactive search and carries that search's scope —
// the whole show, a season or an episode — which limits the episodes that skip the import
// gate. Taking it from the token rather than the body means the browser can't widen it.
func (a *api) handleGrabSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	ref, ok := a.resolveRelease(w, r, req.Token, automation.ReleaseKindSeries, id)
	if !ok {
		return
	}
	scope := automation.ScopeFor(ref.Season, ref.Episode)
	if err := a.deps.Automation.GrabForSeries(r.Context(), id, ref.Indexer, ref.DownloadURL, ref.Title, scope); err != nil {
		a.writeGrabError(w, err, ref)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "grabbed", "title": ref.Title})
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
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{
		Kind: "series.grab-scope", Target: fmt.Sprintf("series:%d:s%de%d", id, req.Season, req.Episode),
		Class: jobs.ClassIndexerSearch, Timeout: 5 * time.Minute,
		Fn: interactive(errFn(func(ctx context.Context) error {
			_, err := a.deps.Automation.GrabForScope(ctx, id, sc)
			return err
		}))})
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "searching"})
}

// handleRefreshSeries re-pulls metadata and rescans the disk for a series.
func (a *api) handleRefreshSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	// Even the owner's Refresh never renumbers: a numbering change becomes a proposal the
	// page shows (GET .../numbering), and only its Apply moves files.
	if _, _, err := a.deps.Series.Refresh(ctx, id, series.RefreshOptions{}); err != nil {
		if errors.Is(err, series.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "series not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not refresh series")
		return
	}
	if err := a.deps.Automation.RescanSeries(ctx, id); err != nil {
		// A rescan that refused (the TV share looked unmounted) says why in words the
		// person can act on; nothing on the episodes changed either way.
		var refused *automation.RescanRefusedError
		if errors.As(err, &refused) {
			a.writeError(w, http.StatusConflict, refused.Reason)
			return
		}
		a.deps.Log.Warn("series: rescan failed", "series_id", id, "err", err)
		a.writeError(w, http.StatusInternalServerError, "could not rescan the disk")
		return
	}
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
	ids := make([]int64, 0, len(all))
	for _, s := range all {
		ids = append(ids, s.ID)
	}
	// One sweep at a time (the job runner's single-flight). Two overlapping runs would
	// double every metadata pull and race each other's episode writes for no benefit.
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "series.refresh-all", Target: "all", Class: "series.refresh-all", Timeout: 2 * time.Hour,
		Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
			refreshed, failed, unscanned, unclear := a.refreshSeriesSweep(ctx, ids, p)
			msg := fmt.Sprintf("Refreshed %d of %d series", refreshed, len(ids))
			if unscanned > 0 {
				msg += fmt.Sprintf(" — %d not rescanned because their folders didn't look mounted (nothing on them changed)", unscanned)
			}
			if unclear > 0 {
				msg += fmt.Sprintf(" — %d not rescanned because it isn't clear which folder they live in (see each show's page; nothing on them changed)", unclear)
			}
			p.SetMessage(msg)
			return map[string]int{"refreshed": refreshed, "failed": failed, "not_rescanned": unscanned + unclear, "folder_unclear": unclear}, nil
		}})
	if !ok {
		return
	}
	if existing {
		a.alreadyRunning(w, jobID, "a refresh of all series is already running")
		return
	}
	a.accepted(w, jobID, false, map[string]any{"queued": len(ids)})
}

// refreshSeriesSweep walks every series, refreshing metadata and rescanning the disk.
// Detached from the request: ctx is the job's, the run context with a two-hour budget.
// unscanned counts shows whose rescan refused because their folders looked unmounted;
// unclear, those whose folder can't be told (series.ErrNoShowFolder).
func (a *api) refreshSeriesSweep(ctx context.Context, ids []int64, p *jobs.Progress) (refreshed, failed, unscanned, unclear int) {
	a.deps.Log.Info("series: refreshing all", "count", len(ids))
	for i, id := range ids {
		p.Set(float64(i)/float64(len(ids)), fmt.Sprintf("Refreshing %d of %d", i+1, len(ids)))
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
		if err := a.deps.Automation.RescanSeries(each, id); err != nil {
			// The metadata refresh still counts; the rescan changed nothing for this show
			// (RescanSeries logs a refusal itself).
			switch {
			case errors.Is(err, series.ErrNoShowFolder):
				unclear++
			case errors.As(err, new(*automation.RescanRefusedError)):
				unscanned++
			default:
				a.deps.Log.Warn("series: rescan failed", "series_id", id, "err", err)
			}
		}
		cancelEach()
		refreshed++
		if ctx.Err() != nil {
			break
		}
	}
	a.deps.Log.Info("series: refreshed all", "refreshed", refreshed, "failed", failed, "not_rescanned", unscanned, "folder_unclear", unclear)
	return refreshed, failed, unscanned, unclear
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
	ctx, cancel := importListContext(r)
	defer cancel()
	cands, truncated := a.deps.Automation.SeriesImportCandidates(ctx, dir)
	if cands == nil {
		cands = []automation.SeriesImportCandidate{}
	}
	a.writeImportList(w, r, ctx, "series", dir, cands, truncated)
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
		jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "series.import-folder", Target: jobTarget("series", id), Class: jobs.ClassImport, Timeout: 2 * time.Hour,
			Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
				if err := a.deps.Automation.ManualImportSeries(ctx, id, src); err != nil {
					return nil, err
				}
				p.SetMessage("Imported the folder")
				return nil, nil
			}})
		if !ok {
			return
		}
		a.accepted(w, jobID, existing, map[string]any{"status": "importing", "background": true})
		return
	}

	if err := a.deps.Automation.ManualImportSeries(r.Context(), id, src); err != nil {
		if errors.Is(err, series.ErrNoShowFolder) {
			a.writeError(w, http.StatusConflict, err.Error())
			return
		}
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
	if errors.Is(err, series.ErrNoShowFolder) {
		a.writeError(w, http.StatusConflict, err.Error())
		return
	}
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
	if errors.Is(err, series.ErrNoShowFolder) {
		a.writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not rename")
		return
	}
	a.writeJSON(w, http.StatusOK, res)
}

// handleSeriesMisplaced previews putting the show's misplaced episode files back into its
// folder; handleSeriesMisplacedApply does exactly the previewed items it's sent.
func (a *api) handleSeriesMisplaced(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	plan, err := a.deps.Automation.MisplacedPreview(r.Context(), id)
	if errors.Is(err, series.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "series not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not check the show's files")
		return
	}
	a.writeJSON(w, http.StatusOK, plan)
}

func (a *api) handleSeriesMisplacedApply(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Items []automation.MisplacedItem `json:"items"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	res, err := a.deps.Automation.MoveMisplacedBack(r.Context(), id, req.Items)
	if errors.Is(err, series.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "series not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not put the files back")
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
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{
		Kind: "series.regrab-episode", Target: fmt.Sprintf("series:%d:s%de%d", id, season, episode),
		Class: jobs.ClassIndexerSearch, Timeout: 5 * time.Minute,
		Fn: interactive(errFn(func(ctx context.Context) error { return a.deps.Automation.RegrabEpisode(ctx, id, season, episode) }))})
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "searching"})
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
		if errors.Is(err, series.ErrNoShowFolder) {
			a.writeError(w, http.StatusConflict, err.Error())
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
