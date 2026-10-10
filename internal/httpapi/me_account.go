package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/auth"
)

// The Me page's Account card, for every role: change your own password and sign your
// other devices out. Everything here acts on the caller's own account only.

// sessionToken is the raw session token this request came in with ("" for an API key).
func sessionToken(r *http.Request) string {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		return c.Value
	}
	return ""
}

// handleMyAccount says what the Account card needs to know: whether the account has a
// password anyone knows (a Plex-only account sets its first one without a current one).
func (a *api) handleMyAccount(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	has, err := a.deps.Auth.HasPassword(r.Context(), u.ID)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load your account")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"password_set": has})
}

// handleChangeMyPassword changes the caller's password: {current, new}. The current
// password is required whenever the account has one; a Plex-only account (no password
// anyone knows) sets its first one on the strength of its signed-in session, which is what
// lets it unlink Plex later. Wrong current passwords are throttled per account. Every
// other session ends; this one stays.
func (a *api) handleChangeMyPassword(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	key := "pw:" + strconv.FormatInt(u.ID, 10)
	if l := a.loginLimiter; l != nil {
		if blocked, retry := l.blocked(key); blocked {
			a.tooManyAttempts(w, retry)
			return
		}
	}
	has, err := a.deps.Auth.HasPassword(r.Context(), u.ID)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not change your password")
		return
	}
	if has && !a.deps.Auth.CheckPassword(r.Context(), u.ID, req.Current) {
		if a.loginLimiter != nil {
			a.loginLimiter.fail(key, "")
		}
		a.writeError(w, http.StatusBadRequest, "Current password is wrong")
		return
	}
	ended, err := a.deps.Auth.ChangePassword(r.Context(), u.ID, req.New, sessionToken(r))
	if errors.Is(err, auth.ErrWeakPassword) {
		a.writeError(w, http.StatusBadRequest, "The new password must be at least 8 characters")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not change your password")
		return
	}
	if a.loginLimiter != nil {
		a.loginLimiter.reset(key, "")
	}
	// Who and how, never the password.
	a.deps.Log.Info("account: password changed by its owner", "user_id", u.ID, "first_password", !has, "other_sessions_ended", ended)
	a.writeJSON(w, http.StatusOK, map[string]any{"password_set": true, "signed_out": ended})
}

// handleRevokeMyOtherSessions signs the caller out of every other browser, keeping this one.
func (a *api) handleRevokeMyOtherSessions(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	n, err := a.deps.Auth.RevokeOtherSessions(r.Context(), u.ID, sessionToken(r))
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not sign out your other devices")
		return
	}
	a.deps.Log.Info("account: signed out of other devices", "user_id", u.ID, "sessions_ended", n)
	a.writeJSON(w, http.StatusOK, map[string]any{"signed_out": n})
}
