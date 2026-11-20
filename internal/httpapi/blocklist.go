package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/automation"
)

// handleListAllBlocks lists blocklist entries of every kind — movies, shows, books,
// albums and the global ones that block a release for every title — newest first.
// ?type= narrows to one kind, ?q= matches the release or the title, ?limit=&offset= page.
func (a *api) handleListAllBlocks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	typ := q.Get("type")
	if !automation.ValidBlockType(typ) {
		a.writeError(w, http.StatusBadRequest, "type must be movie, series, book, music or global")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	rows, total, err := a.deps.Automation.ListAllBlocks(r.Context(), automation.BlockFilter{
		Type: typ, Q: q.Get("q"), Limit: limit, Offset: offset,
	})
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the blocklist")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": rows, "total": total})
}

// bulkUnblockRequest carries the ids to unblock.
type bulkUnblockRequest struct {
	IDs []int64 `json:"ids"`
}

// handleBulkUnblock unblocks many blocklist entries at once.
func (a *api) handleBulkUnblock(w http.ResponseWriter, r *http.Request) {
	var req bulkUnblockRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 {
		a.writeError(w, http.StatusBadRequest, "ids is required")
		return
	}
	if len(req.IDs) > 1000 {
		a.writeError(w, http.StatusBadRequest, "too many ids")
		return
	}
	n, err := a.deps.Automation.RemoveBlockEntries(r.Context(), req.IDs)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not remove blocklist entries")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"removed": n})
}

// handleUnblockAny unblocks one entry of any kind: the release becomes
// grabbable again on the next search.
func (a *api) handleUnblockAny(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	switch err := a.deps.Automation.RemoveBlockEntry(r.Context(), id); {
	case errors.Is(err, automation.ErrBlockNotFound):
		a.writeError(w, http.StatusNotFound, "that entry is no longer on the blocklist")
		return
	case err != nil:
		a.writeError(w, http.StatusInternalServerError, "could not remove the blocklist entry")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
