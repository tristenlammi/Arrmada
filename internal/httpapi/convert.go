package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/tristenlammi/arrmada/internal/convert"
)

// handleConvertHardware reports detected encoders + the one Convert will use, plus the
// cumulative space reclaimed (the Overview headline).
func (a *api) handleConvertHardware(w http.ResponseWriter, r *http.Request) {
	encoders, selected := a.deps.Convert.Hardware()
	if encoders == nil {
		encoders = []convert.Encoder{}
	}
	scratchDir, scratchFree := a.deps.Convert.ScratchInfo(r.Context())
	devices, vaapiDevice := a.deps.Convert.Devices(r.Context())
	if devices == nil {
		devices = []convert.RenderDevice{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"encoders": encoders, "selected": selected,
		"reclaimed_bytes":    a.deps.Convert.Reclaimed(r.Context()),
		"scratch_dir":        scratchDir,
		"scratch_free_bytes": scratchFree,
		"render_devices":     devices,
		"vaapi_device":       vaapiDevice,
	})
}

// handleConvertSweep queues every non-target-codec file now — the manual "Convert all" button.
// It ignores the schedule for QUEUEING (you asked for it, so it gets queued immediately) but
// the workers still hold each job until the encode window opens, so nothing encodes off-hours.
//
// Runs synchronously so the response can report what was actually queued; the UI shows that
// back to the user rather than the movies-only guess it used to display.
func (a *api) handleConvertSweep(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res, err := a.deps.Convert.ConvertAll(ctx)
	if err != nil {
		a.deps.Log.Warn("convert all failed", "err", err)
		a.writeError(w, http.StatusInternalServerError, "could not queue conversions")
		return
	}
	a.deps.Log.Info("convert all queued",
		"queued", res.Queued, "movies", res.Movies, "episodes", res.Episodes, "blocklisted", res.Blocklisted)
	a.writeJSON(w, http.StatusAccepted, res)
}

// handleConvertLibrary returns each library file's spec + convert candidacy, read from the
// persisted index (migration 0058) rather than scanned per request.
//
//	?media=tv               → per-series roll-up (a few dozen rows, not thousands)
//	?media=tv&series=<id>   → that show's episodes
//	(default)               → movies
func (a *api) handleConvertLibrary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.URL.Query().Get("media") == "tv" {
		// A bare TV request gets the roll-up. Returning every episode is what made this
		// tab unusable, so the flat list is only ever served for one series at a time.
		if raw := r.URL.Query().Get("series"); raw == "" {
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
	}

	var (
		list []convert.Candidate
		err  error
	)
	if r.URL.Query().Get("media") == "tv" {
		seriesID, parseErr := strconv.ParseInt(r.URL.Query().Get("series"), 10, 64)
		if parseErr != nil || seriesID <= 0 {
			a.writeError(w, http.StatusBadRequest, "invalid series id")
			return
		}
		list, err = a.deps.Convert.LibraryTV(ctx, seriesID)
	} else {
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

// handleConvertStats returns the Overview tab's library-wide numbers (movies + TV) from the
// index, so the page doesn't have to fetch the whole movie list to render them.
func (a *api) handleConvertStats(w http.ResponseWriter, r *http.Request) {
	stats, err := a.deps.Convert.LibraryStats(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read library index")
		return
	}
	a.writeJSON(w, http.StatusOK, stats)
}

// handleConvertSeries queues every convertible episode of a series, or of one season when
// ?season= is given. The response reports how many were queued so the UI can confirm.
func (a *api) handleConvertSeries(w http.ResponseWriter, r *http.Request) {
	seriesID, ok := a.pathValueID(w, r, "series")
	if !ok {
		return
	}
	season := -1 // -1 = the whole series
	if raw := r.URL.Query().Get("season"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			a.writeError(w, http.StatusBadRequest, "invalid season")
			return
		}
		season = n
	}
	n, err := a.deps.Convert.QueueSeries(r.Context(), seriesID, season)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not queue conversions")
		return
	}
	a.writeJSON(w, http.StatusAccepted, map[string]any{"queued": n})
}

// handleConvertEpisode queues a Save-space conversion for one TV episode.
func (a *api) handleConvertEpisode(w http.ResponseWriter, r *http.Request) {
	seriesID, ok := a.pathValueID(w, r, "series")
	if !ok {
		return
	}
	season, err := strconv.Atoi(r.PathValue("season"))
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid season")
		return
	}
	episode, err := strconv.Atoi(r.PathValue("episode"))
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid episode")
		return
	}
	job, err := a.deps.Convert.QueueEpisode(r.Context(), seriesID, season, episode)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusAccepted, job)
}

// handleConvertLogs returns the recent Convert activity console lines.
func (a *api) handleConvertLogs(w http.ResponseWriter, r *http.Request) {
	logs := a.deps.Convert.Logs()
	if logs == nil {
		logs = []convert.LogLine{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"lines": logs})
}

// handleConvertJobs returns recent + active conversion jobs (polled by the UI for progress).
func (a *api) handleConvertJobs(w http.ResponseWriter, r *http.Request) {
	jobs := a.deps.Convert.Jobs()
	if jobs == nil {
		jobs = []convert.Job{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

// handleConvertMovieSample encodes a ~30s slice of one movie with the current default plan and
// returns a content-exact size estimate — the precise alternative to the heuristic in the table.
func (a *api) handleConvertMovieSample(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res, err := a.deps.Convert.SampleMovie(ctx, id)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, res)
}

// handleConvertMovie queues a Save-space conversion for one movie.
func (a *api) handleConvertMovie(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	job, err := a.deps.Convert.QueueMovie(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusAccepted, job)
}
