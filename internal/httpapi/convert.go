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

// handleConvertSweep converts every non-target-codec file now — the manual "Convert all" button
// (bypasses the schedule gates; the scheduler uses Sweep instead).
func (a *api) handleConvertSweep(w http.ResponseWriter, r *http.Request) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if n, err := a.deps.Convert.ConvertAll(ctx); err != nil {
			a.deps.Log.Warn("convert all failed", "err", err)
		} else {
			a.deps.Log.Info("convert all queued", "count", n)
		}
	}()
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "converting"})
}

// handleConvertLibrary probes the library and returns each file's spec + convert candidacy.
// ?media=tv returns TV episodes; anything else (default) returns movies.
func (a *api) handleConvertLibrary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	var (
		list []convert.Candidate
		err  error
	)
	if r.URL.Query().Get("media") == "tv" {
		list, err = a.deps.Convert.LibraryTV(ctx)
	} else {
		list, err = a.deps.Convert.Library(ctx)
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not scan library")
		return
	}
	if list == nil {
		list = []convert.Candidate{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": list})
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
