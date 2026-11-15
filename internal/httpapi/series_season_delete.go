package httpapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/series"
)

// pathSeason reads {season} from the path.
func (a *api) pathSeason(w http.ResponseWriter, r *http.Request) (int, bool) {
	season, err := strconv.Atoi(r.PathValue("season"))
	if err != nil || season < 0 {
		a.writeError(w, http.StatusBadRequest, "invalid season")
		return 0, false
	}
	return season, true
}

// handleSeasonDeletePreview says what deleting one season's files would move, where it
// goes, which files stay because another season shares them, and the season's own
// downloads still in progress (#111).
func (a *api) handleSeasonDeletePreview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	season, ok := a.pathSeason(w, r)
	if !ok {
		return
	}
	plan, err := a.deps.Series.SeasonDeletePlan(r.Context(), id, season)
	if err != nil {
		if errors.Is(err, series.ErrNoSuchSeason) || errors.Is(err, series.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not list the season's files")
		return
	}
	shared := make([]string, 0, len(plan.Shared))
	for _, p := range plan.Shared {
		shared = append(shared, filepath.Base(p))
	}
	pending := []automation.PendingSeriesDownload{}
	if a.deps.Automation != nil {
		if p, err := a.deps.Automation.PendingSeriesDownloads(r.Context(), id, season); err == nil {
			pending = p
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"files": len(plan.Paths), "bytes": plan.Bytes, "shared": shared,
		"recycle":           a.recycleMode(r.Context()),
		"pending_downloads": pending,
	})
}

// handleDeleteSeasonFiles moves one season's files to the recycle bin; unmonitor=true
// stops wanting the season, cancel_downloads=true cancels the downloads made for exactly
// that season.
func (a *api) handleDeleteSeasonFiles(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	season, ok := a.pathSeason(w, r)
	if !ok {
		return
	}
	if a.deps.Automation == nil {
		a.writeError(w, http.StatusServiceUnavailable, "automation isn't ready")
		return
	}
	opts := automation.DeleteSeasonOpts{
		Unmonitor:       r.URL.Query().Get("unmonitor") == "true",
		CancelDownloads: r.URL.Query().Get("cancel_downloads") == "true",
	}
	// Detached, as a show's delete: a bin on another disk is a copy that may outlast the
	// request, and stopping half-way would leave files binned that still read as present.
	sum, err := a.deps.Automation.DeleteSeason(context.WithoutCancel(r.Context()), id, season, opts)
	switch {
	case errors.Is(err, series.ErrNoSuchSeason), errors.Is(err, series.ErrNotFound):
		a.writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, series.ErrFilesNotRemoved):
		a.writeJSON(w, http.StatusConflict, map[string]any{
			"status": "error", "message": err.Error(), "moved": sum.Moved, "failed": sum.Failed,
		})
	case err != nil:
		if !a.writeBinRefusal(w, err) {
			a.writeError(w, http.StatusInternalServerError, "could not delete the season's files")
		}
	default:
		a.writeJSON(w, http.StatusOK, sum)
	}
}

// pendingSeriesDownloads is the show's in-flight grabs for its delete dialog; none when
// automation isn't wired or can't say.
func (a *api) pendingSeriesDownloads(r *http.Request, id int64) []automation.PendingSeriesDownload {
	if a.deps.Automation != nil {
		if p, err := a.deps.Automation.PendingSeriesDownloads(r.Context(), id, -1); err == nil {
			return p
		}
	}
	return []automation.PendingSeriesDownload{}
}
