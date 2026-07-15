package httpapi

import (
	"net/http"

	"github.com/tristenlammi/arrmada/internal/automation"
)

// handleListReviews returns downloads held for admin review (content didn't match
// what they were grabbed for).
func (a *api) handleListReviews(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Automation.ListReviews(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list reviews")
		return
	}
	if list == nil {
		list = []automation.Review{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"reviews": list})
}

// handleRejectReview removes the held download (+files), blocklists the release,
// and resolves the review.
func (a *api) handleRejectReview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Automation.RejectReview(r.Context(), id); err != nil {
		a.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "rejected"})
}

// handleDismissReview resolves a review without touching the download.
func (a *api) handleDismissReview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Automation.DismissReview(r.Context(), id); err != nil {
		a.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "dismissed"})
}

// handleImportReview imports a held download into the item it was grabbed for, or
// into a different library item when target_id is given (reassign).
func (a *api) handleImportReview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		TargetID int64 `json:"target_id"`
	}
	if r.ContentLength > 0 && !a.decodeJSON(w, r, &req) { // body optional; 0 = import into the expected item
		return
	}
	if err := a.deps.Automation.ImportReview(r.Context(), id, req.TargetID); err != nil {
		a.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "imported"})
}
