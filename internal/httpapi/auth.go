package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tristenlammi/arrmada/internal/auth"
)

const sessionCookieName = "arrmada_session"

type ctxKey int

const userCtxKey ctxKey = iota

func withUser(r *http.Request, u *auth.User) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userCtxKey, u))
}

func userFrom(r *http.Request) (*auth.User, bool) {
	u, ok := r.Context().Value(userCtxKey).(*auth.User)
	return u, ok
}

// authenticate resolves the current user from a session cookie or API key and
// stashes it in the request context. It never rejects — enforcement is the job
// of protected/requireRole.
// localAdmin is the synthetic identity every request runs as when auth is
// disabled (local development).
func localAdmin() *auth.User {
	return &auth.User{ID: 0, Username: "local-dev", Role: auth.RoleAdmin, AutoApprove: true}
}

func (a *api) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Auth disabled: treat everyone as a local admin so protected routes work.
		if !a.deps.Config.AuthEnabled {
			next.ServeHTTP(w, withUser(r, localAdmin()))
			return
		}

		var user *auth.User
		if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
			if u, err := a.deps.Auth.ValidateSession(r.Context(), c.Value); err == nil {
				user = u
			}
		}
		if user == nil {
			if key := apiKeyFromRequest(r); key != "" {
				if u, err := a.deps.Auth.ValidateAPIKey(r.Context(), key); err == nil {
					user = u
				}
			}
		}
		if user != nil {
			r = withUser(r, user)
		}
		next.ServeHTTP(w, r)
	})
}

// protected wraps a handler so it returns 401 unless a user is present.
func (a *api) protected(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := userFrom(r); !ok {
			a.writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		h(w, r)
	}
}

// requireRole wraps a handler so it returns 403 unless the user meets min role.
func (a *api) requireRole(min auth.Role, h http.HandlerFunc) http.HandlerFunc {
	return a.protected(func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r)
		if !u.Role.AtLeast(min) {
			a.writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		h(w, r)
	})
}

func apiKeyFromRequest(r *http.Request) string {
	if k := r.Header.Get("X-Api-Key"); k != "" {
		return k
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleSetup creates the first admin account (only allowed when no users exist)
// and logs them in.
func (a *api) handleSetup(w http.ResponseWriter, r *http.Request) {
	// First-run setup is available until an admin exists — not just until the first
	// user exists — so an instance that somehow has only a requester (e.g. auth was
	// toggled) can still bootstrap its admin instead of being locked out.
	n, err := a.deps.Auth.CountAdmins(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not check setup state")
		return
	}
	if n > 0 {
		a.writeError(w, http.StatusConflict, "setup already complete")
		return
	}

	var body credentials
	if !a.decodeJSON(w, r, &body) {
		return
	}

	u, err := a.deps.Auth.CreateUser(r.Context(), body.Username, body.Password, auth.RoleAdmin, true)
	if err != nil {
		a.writeAuthError(w, err)
		return
	}
	a.startSession(w, r, u, http.StatusCreated)
}

// handleLogin authenticates a user and starts a session.
func (a *api) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body credentials
	if !a.decodeJSON(w, r, &body) {
		return
	}
	u, err := a.deps.Auth.Authenticate(r.Context(), body.Username, body.Password)
	if err != nil {
		a.writeAuthError(w, err)
		return
	}
	a.startSession(w, r, u, http.StatusOK)
}

// handleLogout revokes the current session and clears the cookie.
func (a *api) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		_ = a.deps.Auth.DeleteSession(r.Context(), c.Value)
	}
	a.clearSessionCookie(w, r)
	a.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleMe returns the currently authenticated user.
func (a *api) handleMe(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r)
	a.writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (a *api) startSession(w http.ResponseWriter, r *http.Request, u *auth.User, code int) {
	token, expires, err := a.deps.Auth.CreateSession(r.Context(), u.ID)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     a.cookiePath(),
		Expires:  expires,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
	a.writeJSON(w, code, map[string]any{"user": u})
}

func (a *api) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     a.cookiePath(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *api) cookiePath() string {
	if a.deps.Config.BaseURL == "" {
		return "/"
	}
	return a.deps.Config.BaseURL
}

// decodeJSON reads a small JSON body into dst, writing a 400 and returning false
// on failure.
func (a *api) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// writeAuthError maps auth sentinel errors to HTTP status codes.
func (a *api) writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		a.writeError(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, auth.ErrUserExists):
		a.writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, auth.ErrWeakPassword), errors.Is(err, auth.ErrUsernameRequired):
		a.writeError(w, http.StatusBadRequest, err.Error())
	default:
		a.deps.Log.Error("auth error", "err", err)
		a.writeError(w, http.StatusInternalServerError, "authentication failed")
	}
}
