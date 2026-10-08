package httpapi

import (
	"errors"
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

// handleReviewTargets lists the library items a review's download could be imported
// into — always items of the review's own kind, filtered by ?q= on title (and author or
// artist for books and albums).
func (a *api) handleReviewTargets(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	list, truncated, err := a.deps.Automation.ReviewTargets(r.Context(), id, r.URL.Query().Get("q"), 0)
	if err != nil {
		a.writeReviewError(w, err, "could not list library items")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"targets": list, "truncated": truncated})
}

// handleImportReview imports a held download into the item it was grabbed for, or
// into a different library item when target_id is given (reassign). target_kind says
// what kind of item target_id is; empty means the review's own kind.
func (a *api) handleImportReview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		TargetID   int64  `json:"target_id"`
		TargetKind string `json:"target_kind"`
	}
	if r.ContentLength > 0 && !a.decodeJSON(w, r, &req) { // body optional; 0 = import into the expected item
		return
	}
	if err := a.deps.Automation.ImportReview(r.Context(), id, req.TargetID, req.TargetKind); err != nil {
		a.writeReviewError(w, err, "")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "imported"})
}

// writeReviewError answers a review action's error with the status that says whose
// problem it is. Only a genuine fault is a 500; everything else is something the user
// can act on, and keeps its message. fallback, when set, replaces the raw error text
// on a 500.
func (a *api) writeReviewError(w http.ResponseWriter, err error, fallback string) {
	status := reviewErrorStatus(err)
	msg := err.Error()
	if status == http.StatusInternalServerError && fallback != "" {
		msg = fallback
	}
	a.writeError(w, status, msg)
}

func reviewErrorStatus(err error) int {
	switch {
	case errors.Is(err, automation.ErrReviewNotFound):
		return http.StatusNotFound
	// A review outliving its download is ordinary, not a server fault: the torrent
	// was removed or the folder cleaned up after the item was held. It used to answer
	// 500 with the raw stat error, which told the user nothing actionable.
	case errors.Is(err, automation.ErrDownloadGone):
		return http.StatusGone
	// The module the review belongs to isn't running; turning it on is the fix.
	case errors.Is(err, automation.ErrModuleOff):
		return http.StatusConflict
	// Nothing placeable in the download, a target of the wrong kind, or no target at all
	// are the user's to act on, not server faults — 422 with the reason intact. A review
	// with no item used to reach a library lookup of id 0 and come back as a 500.
	case errors.Is(err, automation.ErrNothingToImport),
		errors.Is(err, automation.ErrWrongTargetKind),
		errors.Is(err, automation.ErrNeedsTarget):
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}
