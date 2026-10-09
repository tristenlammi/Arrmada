package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/recyclebin"
)

// handleRecycleStats reports the recycle bin's size + contents and the configured guard rails.
func (a *api) handleRecycleStats(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.deps.Recycle.Stats(r.Context()))
}

// handleRecycleMode is the cheap "where do deleted files go" answer every delete dialog
// asks before it words its warning. Unlike handleRecycleStats it never walks the bin.
func (a *api) handleRecycleMode(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.recycleMode(r.Context()))
}

// recycleMode is the bin's mode, or "off" when no bin manager is wired (deletes are
// then permanent, so saying so is the honest default).
func (a *api) recycleMode(ctx context.Context) recyclebin.Mode {
	if a.deps.Recycle == nil {
		return recyclebin.Mode{Dirs: []string{}}
	}
	return a.deps.Recycle.Mode(ctx)
}

// writeBinRefusal answers a delete the recycle bin refused with 409 and the bin's own
// message, which says nothing was deleted and what went wrong. A movie delete that stopped
// part-way also lists what moved and what didn't. It reports whether it wrote a response.
func (a *api) writeBinRefusal(w http.ResponseWriter, err error) bool {
	var fe *movies.FilesNotRemovedError
	if errors.As(err, &fe) {
		a.writeJSON(w, http.StatusConflict, map[string]any{
			"status": "error", "message": err.Error(), "moved": fe.Moved, "failed": fe.Failed,
		})
		return true
	}
	if errors.Is(err, library.ErrBinRefused) || errors.Is(err, library.ErrReplacementRefused) {
		a.writeError(w, http.StatusConflict, err.Error())
		return true
	}
	return false
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
