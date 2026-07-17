package httpapi

import "net/http"

// handleRecycleStats reports the recycle bin's size + contents and the configured guard rails.
func (a *api) handleRecycleStats(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.deps.Recycle.Stats(r.Context()))
}

// handleRecycleEmpty deletes everything in the recycle bin and returns the space freed.
func (a *api) handleRecycleEmpty(w http.ResponseWriter, r *http.Request) {
	freed, err := a.deps.Recycle.Empty(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not empty the recycle bin: "+err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"freed_bytes": freed})
}
