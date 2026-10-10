package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Merging a duplicate Plex requester into the account it belongs with (admin only). The
// preview says what would move, as counts — audiobook data only as yes/no, never which
// books — and the merge copies the database first.

// mergeAnswer turns a merge refusal into its HTTP answer; false when err was something else.
func (a *api) mergeAnswer(w http.ResponseWriter, err error) bool {
	var refused *auth.ErrMergeRefused
	switch {
	case errors.As(err, &refused):
		a.writeError(w, http.StatusBadRequest, refused.Reason)
	case errors.Is(err, auth.ErrNotFound):
		a.writeError(w, http.StatusNotFound, "user not found")
	default:
		return false
	}
	return true
}

// handlePlexMergePreview: GET /users/{id}/plex/merge?from=<duplicate id>.
func (a *api) handlePlexMergePreview(w http.ResponseWriter, r *http.Request) {
	target, ok := a.pathID(w, r)
	if !ok {
		return
	}
	from, err := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "from must be a user id")
		return
	}
	p, err := a.deps.Auth.PlexMergePreview(r.Context(), target, from)
	if a.mergeAnswer(w, err) {
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not count what would move")
		return
	}
	a.writeJSON(w, http.StatusOK, p)
}

// handlePlexMerge: POST /users/{id}/plex/merge {from_user_id}. All or nothing, after a
// safety copy of the database; refused when the copy can't be made.
func (a *api) handlePlexMerge(w http.ResponseWriter, r *http.Request) {
	target, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		FromUserID int64 `json:"from_user_id"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	// Checked before the copy, so a refused merge doesn't leave a backup behind.
	if _, err := a.deps.Auth.PlexMergePreview(ctx, target, req.FromUserID); a.mergeAnswer(w, err) {
		return
	} else if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not check the accounts")
		return
	}
	if a.deps.Snapshot == nil {
		a.writeError(w, http.StatusInternalServerError, "couldn't take a safety copy first — nothing was merged")
		return
	}
	path, err := a.deps.Snapshot(ctx, string(store.BackupPreMergeUser))
	if err != nil {
		a.deps.Log.Error("users: safety copy before merge failed", "user_id", target, "from_user_id", req.FromUserID, "err", err)
		a.writeError(w, http.StatusInternalServerError, "couldn't take a safety copy first — nothing was merged ("+err.Error()+")")
		return
	}
	a.deps.Log.Info("users: database copied before merging accounts", "user_id", target, "from_user_id", req.FromUserID, "backup", path)
	if err := a.deps.Auth.MergePlexDuplicate(ctx, target, req.FromUserID); err != nil {
		if a.mergeAnswer(w, err) {
			return
		}
		a.deps.Log.Error("users: merging accounts failed; nothing changed", "user_id", target, "from_user_id", req.FromUserID, "err", err)
		a.writeError(w, http.StatusInternalServerError, "could not merge the accounts — nothing was changed")
		return
	}
	a.deps.Log.Info("users: duplicate Plex account merged", "user_id", target, "from_user_id", req.FromUserID)
	a.writeJSON(w, http.StatusOK, map[string]any{"merged": true})
}
