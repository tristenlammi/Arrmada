package httpapi

import (
	"errors"
	"net/http"

	"github.com/tristenlammi/arrmada/internal/auth"
)

// Signed-in devices. A person sees and ends only their own sessions (with the browser
// and a coarse network); an admin can sign someone out everywhere but is never shown
// another person's devices or addresses.

// handleMySessions lists the caller's own signed-in browsers, this one marked.
func (a *api) handleMySessions(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	list, err := a.deps.Auth.ListSessions(r.Context(), u.ID, sessionToken(r))
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list your devices")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

// handleRevokeMySession signs out one of the caller's own sessions by its listed id. An id
// that isn't theirs (another user's, or made up) is simply not found.
func (a *api) handleRevokeMySession(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	ended, err := a.deps.Auth.RevokeSession(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not sign that device out")
		return
	}
	if !ended {
		a.writeError(w, http.StatusNotFound, "that device isn't signed in")
		return
	}
	a.deps.Log.Info("account: signed out one device", "user_id", u.ID)
	w.WriteHeader(http.StatusNoContent)
}

// handleRevokeUserSessions is an admin's "Sign out everywhere" for an account: every
// browser it's signed in on has to sign in again. It answers with a count only, and the
// log records who did it to whom — no tokens, devices or addresses.
func (a *api) handleRevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if _, err := a.deps.Auth.UserByID(r.Context(), id); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) || errors.Is(err, auth.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "user not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not load the user")
		return
	}
	n, err := a.deps.Auth.RevokeUserSessions(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not sign them out")
		return
	}
	var by int64
	if me, ok := userFrom(r); ok && me != nil {
		by = me.ID
	}
	a.deps.Log.Info("users: signed out everywhere by an admin", "user_id", id, "by_user_id", by, "sessions_ended", n)
	a.writeJSON(w, http.StatusOK, map[string]any{"signed_out": n})
}
