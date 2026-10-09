package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/convert"
)

// handleConvertHardware reports the verified encoders, what conversions run on, the space
// reclaimed so far, and the transcode folder.
func (a *api) handleConvertHardware(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	encoders, using := a.deps.Convert.Hardware(ctx)
	if encoders == nil {
		encoders = []convert.Encoder{}
	}
	scratchDir, scratchFree := a.deps.Convert.ScratchInfo(ctx)
	devices, vaapiDevice := a.deps.Convert.Devices(ctx)
	if devices == nil {
		devices = []convert.RenderDevice{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"encoders": encoders, "using": using,
		"reclaimed_bytes":    a.deps.Convert.Reclaimed(ctx),
		"scratch_dir":        scratchDir,
		"scratch_free_bytes": scratchFree,
		"render_devices":     devices,
		"vaapi_device":       vaapiDevice,
	})
}

// handleConvertStatus reports what the runner is doing and what's coming up.
func (a *api) handleConvertStatus(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.deps.Convert.Status(r.Context()))
}

// handleConvertSettings — GET /api/v1/convert/settings
func (a *api) handleConvertSettings(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.deps.Convert.GetSettings(r.Context()))
}

// handleConvertSettingsUpdate — PUT /api/v1/convert/settings (partial)
func (a *api) handleConvertSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	var p convert.SettingsPatch
	if !a.decodeJSON(w, r, &p) {
		return
	}
	st, err := a.deps.Convert.UpdateSettings(r.Context(), p)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, st)
}

// handleConvertLibrary returns each library file's spec and what it needs, from the index.
//
//	?media=tv               → per-series roll-up
//	?media=tv&series=<id>   → that show's episodes
//	(default)               → movies
//	&convertible=1          → only files that need work
func (a *api) handleConvertLibrary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	if q.Get("media") == "tv" && q.Get("series") == "" {
		rollup, err := a.deps.Convert.LibraryTVSeries(ctx)
		if err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not read library index")
			return
		}
		if rollup == nil {
			rollup = []convert.SeriesRollup{}
		}
		a.writeJSON(w, http.StatusOK, map[string]any{"series": rollup})
		return
	}
	mediaType := "movie"
	var seriesID int64
	if q.Get("media") == "tv" {
		mediaType = "episode"
		parsed, err := strconv.ParseInt(q.Get("series"), 10, 64)
		if err != nil || parsed <= 0 {
			a.writeError(w, http.StatusBadRequest, "invalid series id")
			return
		}
		seriesID = parsed
	}
	var (
		list []convert.Candidate
		err  error
	)
	switch {
	case q.Get("convertible") == "1":
		list, err = a.deps.Convert.LibraryConvertible(ctx, mediaType, seriesID)
	case mediaType == "episode":
		list, err = a.deps.Convert.LibraryTV(ctx, seriesID)
	default:
		list, err = a.deps.Convert.Library(ctx)
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read library index")
		return
	}
	if list == nil {
		list = []convert.Candidate{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// handleConvertRequest — POST /api/v1/convert/requests {key}: convert this file now.
func (a *api) handleConvertRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Convert.Request(r.Context(), req.Key); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, convert.ErrAlreadyQueued) {
			status = http.StatusConflict
		}
		a.writeError(w, status, err.Error())
		return
	}
	a.writeJSON(w, http.StatusAccepted, map[string]any{"requested": req.Key})
}

// handleConvertRequestCancel — DELETE /api/v1/convert/requests?key= (or ?all=1)
func (a *api) handleConvertRequestCancel(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" && r.URL.Query().Get("all") != "1" {
		a.writeError(w, http.StatusBadRequest, "pass key= or all=1")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"cancelled": a.deps.Convert.CancelRequest(r.Context(), key)})
}

// handleConvertSeries — POST /api/v1/convert/series/{series}[?season=N]: request every
// episode of a show (or one season) that needs work.
func (a *api) handleConvertSeries(w http.ResponseWriter, r *http.Request) {
	seriesID, ok := a.pathValueID(w, r, "series")
	if !ok {
		return
	}
	season := -1
	if raw := r.URL.Query().Get("season"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			a.writeError(w, http.StatusBadRequest, "invalid season")
			return
		}
		season = n
	}
	n, err := a.deps.Convert.RequestSeries(r.Context(), seriesID, season)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not request conversions")
		return
	}
	a.writeJSON(w, http.StatusAccepted, map[string]any{"requested": n})
}

// handleConvertCancel stops a running job. The original is untouched until a job completes.
func (a *api) handleConvertCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathValueID(w, r, "id")
	if !ok {
		return
	}
	if err := a.deps.Convert.Cancel(id); err != nil {
		a.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"cancelled": id})
}

// handleConvertCompare — POST /api/v1/convert/compare {key}: start a side-by-side test.
func (a *api) handleConvertCompare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Convert.StartCompare(r.Context(), req.Key); err != nil {
		a.writeError(w, http.StatusConflict, err.Error())
		return
	}
	a.writeJSON(w, http.StatusAccepted, a.deps.Convert.CompareStatus())
}

// handleConvertCompareStatus — GET /api/v1/convert/compare
func (a *api) handleConvertCompareStatus(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.deps.Convert.CompareStatus())
}

// handleConvertCompareFile — GET /api/v1/convert/compare/files/{name}: download a kept clip.
func (a *api) handleConvertCompareFile(w http.ResponseWriter, r *http.Request) {
	path, ok := a.deps.Convert.CompareFile(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+r.PathValue("name")+`"`)
	http.ServeFile(w, r, path)
}

// handleConvertBlocklist lists files left alone after repeated failures.
func (a *api) handleConvertBlocklist(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Convert.Blocklist(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the blocklist")
		return
	}
	if list == nil {
		list = []convert.Blocked{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// handleConvertBlocklistClear forgets one item's failures, or all of them with ?all=1.
func (a *api) handleConvertBlocklistClear(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if r.URL.Query().Get("all") != "1" && key == "" {
		a.writeError(w, http.StatusBadRequest, "pass key= or all=1")
		return
	}
	if r.URL.Query().Get("all") == "1" {
		key = ""
	}
	if err := a.deps.Convert.ClearBlocklist(r.Context(), key); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not clear the blocklist")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}

// handleConvertSkips lists files that couldn't be converted, with the reason.
func (a *api) handleConvertSkips(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Convert.Skips(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the skip list")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// handleConvertSkipsClear forgets one skip, or all of them with ?all=1, so they're retried.
func (a *api) handleConvertSkipsClear(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if r.URL.Query().Get("all") != "1" && key == "" {
		a.writeError(w, http.StatusBadRequest, "pass key= or all=1")
		return
	}
	if r.URL.Query().Get("all") == "1" {
		key = ""
	}
	if err := a.deps.Convert.ClearSkip(r.Context(), key); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not clear the skip")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}

// handleConvertStats returns the Overview's library-wide numbers from the index.
func (a *api) handleConvertStats(w http.ResponseWriter, r *http.Request) {
	stats, err := a.deps.Convert.LibraryStats(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read library index")
		return
	}
	a.writeJSON(w, http.StatusOK, stats)
}

// handleConvertLogs returns the recent activity-log lines.
func (a *api) handleConvertLogs(w http.ResponseWriter, r *http.Request) {
	logs := a.deps.Convert.Logs()
	if logs == nil {
		logs = []convert.LogLine{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"lines": logs})
}

// handleConvertHistory — GET /api/v1/convert/history?outcome=&media=&q=&before=&limit=: one
// page of the conversion ledger, newest first. next is the cursor for the following page
// ("" on the last). Manager-only like the rest of Convert: rows carry library file paths.
func (a *api) handleConvertHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := convert.HistoryFilter{Q: q.Get("q"), Before: q.Get("before")}
	switch o := q.Get("outcome"); o {
	case "", convert.OutcomeDone, convert.OutcomeFailed, convert.OutcomeSkipped, convert.OutcomeCancelled, convert.OutcomeInProgress:
		f.Outcome = o
	default:
		a.writeError(w, http.StatusBadRequest, "invalid outcome")
		return
	}
	switch q.Get("media") {
	case "", "all":
	case "movie", "movies":
		f.Media = "movie"
	case "episode", "tv":
		f.Media = "episode"
	default:
		a.writeError(w, http.StatusBadRequest, "invalid media")
		return
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			a.writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		f.Limit = n // capped at 200 by the store
	}
	items, next, err := a.deps.Convert.History(r.Context(), f)
	switch {
	case errors.Is(err, convert.ErrHistoryCursor):
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		a.writeError(w, http.StatusInternalServerError, "could not read the conversion history")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items, "next": next})
}

// handleConvertHistoryEntry — GET /api/v1/convert/history/{id}: one ledger row in full,
// with the probed spec of the original and the result.
func (a *api) handleConvertHistoryEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathValueID(w, r, "id")
	if !ok {
		return
	}
	e, err := a.deps.Convert.HistoryEntry(r.Context(), id)
	switch {
	case errors.Is(err, convert.ErrNoHistory):
		a.writeError(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		a.writeError(w, http.StatusInternalServerError, "could not read the conversion record")
		return
	}
	a.writeJSON(w, http.StatusOK, e)
}

// handleConvertJobs returns recent and running conversions (polled for progress).
func (a *api) handleConvertJobs(w http.ResponseWriter, r *http.Request) {
	jobs := a.deps.Convert.Jobs()
	if jobs == nil {
		jobs = []convert.Job{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

// handleConvertReindex rescans the library so files added or changed since the last sweep
// show up without waiting for the nightly one.
func (a *api) handleConvertReindex(w http.ResponseWriter, r *http.Request) {
	if !a.deps.Convert.RefreshIndex(r.Context()) {
		a.writeJSON(w, http.StatusOK, map[string]any{"started": false, "reason": "a library scan is already running"})
		return
	}
	a.writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

// handleConvertReindexStatus reports whether a rescan is still running.
func (a *api) handleConvertReindexStatus(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, map[string]any{"running": a.deps.Convert.IndexScanning()})
}
