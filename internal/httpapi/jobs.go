package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/jobs"
)

// JobRunner is the job runner as the HTTP layer uses it — an interface so handler tests
// can inject a fake that records what was submitted.
type JobRunner interface {
	Submit(ctx context.Context, spec jobs.Spec) (id int64, existing bool, err error)
	Get(ctx context.Context, id int64) (jobs.Job, error)
	List(ctx context.Context, f jobs.Filter) ([]jobs.Job, error)
	Cancel(ctx context.Context, id int64) error
}

// handleListJobs lists recent background jobs, newest first: ?kind, ?target, ?status,
// ?limit (default 50, at most 500).
func (a *api) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if a.deps.Jobs == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"jobs": []jobs.Job{}})
		return
	}
	q := r.URL.Query()
	f := jobs.Filter{Kind: q.Get("kind"), Target: q.Get("target"), Status: q.Get("status")}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			a.writeError(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		f.Limit = n
	}
	list, err := a.deps.Jobs.List(r.Context(), f)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list jobs")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"jobs": list})
}

// handleGetJob is one job: status, progress, message, result and timings. Buttons follow
// the work they started through here (or job.updated on the websocket).
func (a *api) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if a.deps.Jobs == nil {
		a.writeError(w, http.StatusNotFound, "job not found")
		return
	}
	j, err := a.deps.Jobs.Get(r.Context(), id)
	if errors.Is(err, jobs.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load the job")
		return
	}
	a.writeJSON(w, http.StatusOK, j)
}

// handleCancelJob stops a queued or running job. 409 when it has already finished.
func (a *api) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if a.deps.Jobs == nil {
		a.writeError(w, http.StatusNotFound, "job not found")
		return
	}
	switch err := a.deps.Jobs.Cancel(r.Context(), id); {
	case errors.Is(err, jobs.ErrNotFound):
		a.writeError(w, http.StatusNotFound, "job not found")
	case errors.Is(err, jobs.ErrFinished):
		a.writeError(w, http.StatusConflict, "that job has already finished")
	case err != nil:
		a.writeError(w, http.StatusInternalServerError, "could not cancel the job")
	default:
		a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "cancelling", "job_id": id})
	}
}
