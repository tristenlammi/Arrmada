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

// --- Rules (C2) ---

func (a *api) handleConvertRules(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	rules, err := a.deps.Convert.ListRules(ctx)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load rules")
		return
	}
	if rules == nil {
		rules = []convert.Rule{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (a *api) handleCreateConvertRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string           `json:"name"`
		Auto        bool             `json:"auto"`
		Filters     []convert.Filter `json:"filters"`
		Actions     []convert.Action `json:"actions"`
		Steps       []convert.Step   `json:"steps"`        // branching body (takes precedence over actions)
		WindowStart string           `json:"window_start"` // per-rule schedule window (HH:MM), optional
		WindowEnd   string           `json:"window_end"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		req.Name = "New rule"
	}
	if len(req.Steps) == 0 && len(req.Actions) == 0 { // a rule always at least transcodes
		req.Actions = []convert.Action{{Type: "transcode"}}
	}
	rule, err := a.deps.Convert.CreateRule(r.Context(), convert.Rule{
		Name: req.Name, Enabled: true, Auto: req.Auto, Filters: req.Filters, Actions: req.Actions, Steps: req.Steps,
		WindowStart: req.WindowStart, WindowEnd: req.WindowEnd,
	})
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not create rule")
		return
	}
	a.writeJSON(w, http.StatusCreated, rule)
}

func (a *api) handleSetConvertRuleEnabled(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Convert.SetRuleEnabled(r.Context(), id, req.Enabled); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update rule")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"enabled": req.Enabled})
}

func (a *api) handleDeleteConvertRule(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Convert.DeleteRule(r.Context(), id); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not delete rule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) handleConvertRulePreview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	rule, hits, err := a.deps.Convert.PreviewRule(ctx, id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not preview rule")
		return
	}
	if hits == nil {
		hits = []convert.Candidate{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"rule": rule, "matches": hits})
}

// handleConvertRuleSample encodes a ~30s slice of the first matched movie and returns a
// content-exact size estimate.
func (a *api) handleConvertRuleSample(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res, err := a.deps.Convert.SampleRule(ctx, id)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, res)
}

func (a *api) handleRunConvertRule(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	n, err := a.deps.Convert.RunRule(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not run rule")
		return
	}
	a.writeJSON(w, http.StatusAccepted, map[string]any{"queued": n})
}
