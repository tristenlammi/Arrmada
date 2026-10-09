package httpapi

import (
	"errors"
	"net/http"

	"github.com/tristenlammi/arrmada/internal/scheduler"
)

// handleListTasks is the Tasks table on System → Status: every recurring task with its
// interval, last run, duration, result, error and next run. Plumbing tasks registered
// Hidden (the websocket heartbeat) are left out. The body is a bare JSON array of
// scheduler.TaskStatus — a public shape other pages build on, so fields are only added.
func (a *api) handleListTasks(w http.ResponseWriter, r *http.Request) {
	out := []scheduler.TaskStatus{}
	if a.deps.Scheduler != nil {
		for _, t := range a.deps.Scheduler.Snapshot() {
			if !t.Hidden {
				out = append(out, t)
			}
		}
	}
	a.writeJSON(w, http.StatusOK, out)
}

// handleRunTask is Run now. 202 once the run is under way (with its job id when the job
// runner is wired), 404 for a task that doesn't exist (or is hidden), 409 when it is
// already running — a scheduled tick or an earlier Run now — so it never runs twice.
func (a *api) handleRunTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if a.deps.Scheduler == nil {
		a.writeError(w, http.StatusServiceUnavailable, "the scheduler isn't running")
		return
	}
	if st, ok := a.deps.Scheduler.Status(name); !ok || st.Hidden {
		a.writeError(w, http.StatusNotFound, "no task called "+name)
		return
	}
	id, existing, err := a.deps.Scheduler.RunNow(name, triggerFor(r))
	switch {
	case errors.Is(err, scheduler.ErrUnknownTask):
		a.writeError(w, http.StatusNotFound, "no task called "+name)
	case errors.Is(err, scheduler.ErrBusy):
		body := map[string]any{"status": "error", "message": "already running"}
		if id > 0 {
			body["job_id"] = id
		}
		a.writeJSON(w, http.StatusConflict, body)
	case err != nil:
		a.deps.Log.Warn("run task now failed", "task", name, "err", err)
		a.writeError(w, http.StatusServiceUnavailable, "couldn't start the task")
	case existing:
		a.writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "already running", "job_id": id, "existing": true})
	default:
		a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "started", "job_id": id, "existing": false})
	}
}
