package httpapi

import (
	"net/http"

	"github.com/tristenlammi/arrmada/internal/recyclebin"
)

// handleRecycleStats reports the recycle bin's size + contents and the configured guard rails.
func (a *api) handleRecycleStats(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.deps.Recycle.Stats(r.Context()))
}

// handleRecycleItems lists the individual files in the bin (for the management UI).
func (a *api) handleRecycleItems(w http.ResponseWriter, r *http.Request) {
	items := a.deps.Recycle.List(r.Context())
	if items == nil {
		items = []recyclebin.Item{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleRecycleRestore moves one recycled file back to its original location.
func (a *api) handleRecycleRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Recycle.Restore(r.Context(), req.ID); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "restored"})
}

// handleRecycleDeleteItem permanently deletes one recycled file.
func (a *api) handleRecycleDeleteItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Recycle.DeleteItem(r.Context(), req.ID); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
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
