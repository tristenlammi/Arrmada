package httpapi

import (
	"errors"
	"fmt"
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
	page, err := a.deps.Automation.ListAllBlocks(r.Context(), automation.BlockFilter{
		Type: typ, Q: q.Get("q"), Limit: limit, Offset: offset,
	})
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the blocklist")
		return
	}
	a.writeJSON(w, http.StatusOK, page)
}

// bulkUnblockRequest says which entries to unblock: either ids (the ones ticked), or
// all: true with the list's filter (type, q) and through_id, the newest id the list showed
// (its newest_id) — every entry matching the filter, on every page, up to that one.
type bulkUnblockRequest struct {
	IDs       []int64 `json:"ids"`
	All       bool    `json:"all"`
	Type      string  `json:"type"`
	Q         string  `json:"q"`
	ThroughID int64   `json:"through_id"`
}

// handleBulkUnblock unblocks many blocklist entries at once: the releases become grabbable
// again on the next search, exactly as unblocking each one would.
func (a *api) handleBulkUnblock(w http.ResponseWriter, r *http.Request) {
	var req bulkUnblockRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	var (
		n   int
		err error
	)
	switch {
	case req.All:
		if len(req.IDs) > 0 {
			a.writeError(w, http.StatusBadRequest, "send either ids or all, not both")
			return
		}
		if !automation.ValidBlockType(req.Type) {
			a.writeError(w, http.StatusBadRequest, "type must be movie, series, book, music or global")
			return
		}
		if req.ThroughID <= 0 {
			a.writeError(w, http.StatusBadRequest, "through_id is required with all")
			return
		}
		n, err = a.deps.Automation.RemoveMatchingBlocks(r.Context(), automation.BlockFilter{Type: req.Type, Q: req.Q}, req.ThroughID)
		if err == nil {
			a.deps.Log.Info("blocklist: unblocked everything matching", "type", req.Type, "q", req.Q, "removed", n)
		}
	default:
		if len(req.IDs) == 0 || len(req.IDs) > automation.MaxBulkUnblockIDs {
			a.writeError(w, http.StatusBadRequest, fmt.Sprintf("pick between 1 and %d entries", automation.MaxBulkUnblockIDs))
			return
		}
		n, err = a.deps.Automation.RemoveBlockEntries(r.Context(), req.IDs)
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not remove the blocklist entries")
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
