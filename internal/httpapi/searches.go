package httpapi

import (
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/automation"
)

// handleListSearches lists one title's stored search attempts, newest first:
// GET /api/v1/searches?kind=movie|series|book|music&id=12&since=<unix ms>&limit=20. It
// answers "why isn't it downloading?" — what each search found and why nothing was taken —
// and is what the Search button polls (since = when it was clicked) when no websocket is
// there to say search.finished.
func (a *api) handleListSearches(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind")
	if !automation.ValidAttemptKind(kind) {
		a.writeError(w, http.StatusBadRequest, "kind must be movie, series, book or music")
		return
	}
	id, err := strconv.ParseInt(q.Get("id"), 10, 64)
	if err != nil || id <= 0 {
		a.writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	var since int64
	if s := q.Get("since"); s != "" {
		if since, err = strconv.ParseInt(s, 10, 64); err != nil || since < 0 {
			a.writeError(w, http.StatusBadRequest, "since must be a unix time in milliseconds")
			return
		}
	}
	limit := 20
	if l := q.Get("limit"); l != "" {
		if limit, err = strconv.Atoi(l); err != nil || limit <= 0 {
			a.writeError(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
	}
	if a.deps.Automation == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"attempts": []automation.Attempt{}})
		return
	}
	attempts, err := a.deps.Automation.SearchAttempts(r.Context(), kind, id, since, limit)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read search history")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"attempts": attempts})
}
