package httpapi

import (
	"context"
	"net/http"
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
	a.writeJSON(w, http.StatusOK, map[string]any{
		"encoders": encoders, "selected": selected,
		"reclaimed_bytes": a.deps.Convert.Reclaimed(r.Context()),
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

// handleConvertLibrary probes downloaded movies and returns their specs + convert candidacy.
func (a *api) handleConvertLibrary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	list, err := a.deps.Convert.Library(ctx)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not scan library")
		return
	}
	if list == nil {
		list = []convert.Candidate{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": list})
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
