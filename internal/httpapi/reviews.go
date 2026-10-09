package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/jobs"
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
// and resolves the review. {"find_another": true} then searches the title it was grabbed
// for again, as a job — the blocklisted release can't be picked.
func (a *api) handleRejectReview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		FindAnother bool `json:"find_another"`
	}
	if r.ContentLength > 0 && !a.decodeJSON(w, r, &req) { // body optional
		return
	}
	rv, err := a.deps.Automation.GetReview(r.Context(), id)
	if err == nil {
		err = a.deps.Automation.RejectReview(r.Context(), id)
	}
	if err != nil {
		a.writeReviewError(w, err, "could not reject that download")
		return
	}
	if !req.FindAnother || rv.ExpectedID <= 0 {
		a.writeJSON(w, http.StatusOK, map[string]any{"status": "rejected"})
		return
	}
	spec, ok := a.titleSearchJob(rv.MediaType, rv.ExpectedID)
	if !ok {
		a.writeJSON(w, http.StatusOK, map[string]any{"status": "rejected"})
		return
	}
	var jobID int64
	var existing bool
	if rv.MediaType == "movie" {
		// Movie searches wait their turn in the movie search queue.
		var q automation.MovieQueued
		q, err = a.enqueueMovie(r, rv.ExpectedID, spec)
		jobID, existing = q.JobID, q.Existing
	} else {
		jobID, existing, err = a.submit(r, spec)
	}
	if err != nil {
		// The reject itself happened; only the search didn't start.
		a.writeJSON(w, http.StatusOK, map[string]any{"status": "rejected", "search_error": "couldn't start the search just now — search the title by hand"})
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "rejected", "searching": true})
}

// titleSearchJob is the search job for one library item of any kind.
func (a *api) titleSearchJob(kind string, id int64) (jobs.Spec, bool) {
	switch kind {
	case "movie":
		return a.movieSearchJob(id), true
	case "series":
		return a.seriesSearchJob(id), true
	case "book":
		return a.bookSearchJob(id), true
	case "music":
		return jobs.Spec{Kind: "album.search", Target: jobTarget("album", id), Class: jobs.ClassIndexerSearch, Timeout: 5 * time.Minute,
			Fn: outcomeFn("album", func(ctx context.Context) (automation.SearchOutcome, error) {
				return a.deps.Automation.SearchAlbumNow(ctx, id)
			})}, true
	}
	return jobs.Spec{}, false
}

// handleDismissReview resolves a review without touching the download.
func (a *api) handleDismissReview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Automation.DismissReview(r.Context(), id); err != nil {
		a.writeReviewError(w, err, "could not dismiss that review")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "dismissed"})
}

// handleRetryReview clears an "import keeps failing" review so the import sweep tries
// the download again now — once the cause (a folder, a full disk) is fixed.
func (a *api) handleRetryReview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Automation.RetryReviewImport(r.Context(), id); err != nil {
		a.writeReviewError(w, err, "could not retry that import")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "retrying"})
}

// handleReviewFiles lists the files inside a held download, with what each name says
// about its episode numbering. It reads only the review's own download folder.
func (a *api) handleReviewFiles(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	files, truncated, err := a.deps.Automation.ReviewFiles(r.Context(), id)
	if err != nil {
		a.writeReviewError(w, err, "could not list the download's files")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"files": files, "truncated": truncated})
}

// mapInBackground is how many files a hand mapping places before it runs as a job: a big
// pack's hardlinks and supersedes outlast a sensible request.
const mapInBackground = 20

// handleMapReview imports a held series download by mapping its files to episodes by hand:
// {series_id, files:[{rel_path, season, episodes}]}. The mapping is checked before
// anything is touched; a pack of more than 20 files then imports as a job (202).
func (a *api) handleMapReview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		SeriesID int64                    `json:"series_id"`
		Files    []automation.FileMapping `json:"files"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	rv, err := a.deps.Automation.GetReview(r.Context(), id)
	if err != nil {
		a.writeReviewError(w, err, "")
		return
	}
	// The same folder rule as a manual import: only what's inside the downloads or
	// library folders goes into the library.
	if _, err := a.checkImportPath(r.Context(), rv.ContentPath); err != nil {
		a.writeError(w, http.StatusUnprocessableEntity, "This download is outside your downloads and library folders, so it can't be imported from here. "+err.Error())
		return
	}
	plan, err := a.deps.Automation.PlanReviewMap(r.Context(), id, req.SeriesID, req.Files)
	if err != nil {
		a.writeReviewError(w, err, "could not check that mapping")
		return
	}
	if plan.Files() > mapInBackground {
		jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "review.map", Target: jobTarget("review", id), Class: jobs.ClassImport, Timeout: 2 * time.Hour,
			Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
				n, err := a.deps.Automation.ApplyReviewMap(ctx, plan)
				if err != nil {
					return nil, err
				}
				p.SetMessage(countOf(n, "episode") + " imported")
				return map[string]int{"placed": n}, nil
			}})
		if !ok {
			return
		}
		a.accepted(w, jobID, existing, map[string]any{"status": "importing", "background": true})
		return
	}
	n, err := a.deps.Automation.ApplyReviewMap(r.Context(), plan)
	if err != nil {
		a.writeReviewError(w, err, "could not import the mapped files")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "imported", "placed": n})
}

// maxBulkReviews bounds one bulk action: each Reject is a call to the download client.
const maxBulkReviews = 100

// bulkFailure is one review a bulk action couldn't settle, and why.
type bulkFailure struct {
	ID    int64  `json:"id"`
	Error string `json:"error"`
}

// handleBulkReviews dismisses or rejects several reviews at once: {ids, action}. Each is
// settled on its own, so one failure doesn't stop the rest; the answer says which failed.
func (a *api) handleBulkReviews(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []int64 `json:"ids"`
		Action string  `json:"action"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Action != "dismiss" && req.Action != "reject" {
		a.writeError(w, http.StatusBadRequest, "action must be dismiss or reject")
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > maxBulkReviews {
		a.writeError(w, http.StatusBadRequest, "pick between 1 and 100 reviews")
		return
	}
	done, failed := 0, []bulkFailure{}
	for _, id := range req.IDs {
		var err error
		if req.Action == "dismiss" {
			err = a.deps.Automation.DismissReview(r.Context(), id)
		} else {
			err = a.deps.Automation.RejectReview(r.Context(), id)
		}
		if err != nil {
			msg := err.Error()
			if reviewErrorStatus(err) == http.StatusInternalServerError {
				msg = "failed — see the log"
				a.deps.Log.Warn("review: bulk action failed", "action", req.Action, "id", id, "err", err)
			}
			failed = append(failed, bulkFailure{ID: id, Error: msg})
			continue
		}
		done++
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"done": done, "failed": failed})
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
		errors.Is(err, automation.ErrNeedsTarget),
		errors.Is(err, automation.ErrWrongReason),
		errors.Is(err, automation.ErrBadMapping):
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}
