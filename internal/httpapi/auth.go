package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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
// of the router's per-route scope check (router.go). Authentication is always
// enforced: there is no "local development" bypass, so a LAN-reachable instance
// is never wide open.
func (a *api) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var user *auth.User
		if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
			if u, exp, err := a.deps.Auth.ValidateSessionInfo(r.Context(), c.Value); err == nil {
				user = u
				a.maybeExtendSession(w, r, c.Value, exp)
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

// maybeExtendSession slides a session that's past half its life: the expiry moves to a
// full TTL from now and the cookie is re-sent with it. Sessions in active use never run
// out mid-use, and it costs at most one write per session every couple of weeks.
// Revocation is unchanged: a password change deletes the sessions, and a disabled user
// fails validation before getting here.
func (a *api) maybeExtendSession(w http.ResponseWriter, r *http.Request, token string, exp time.Time) {
	ttl := a.deps.Auth.SessionTTL()
	if exp.IsZero() || time.Until(exp) >= ttl/2 {
		return
	}
	newExp, err := a.deps.Auth.ExtendSession(r.Context(), token)
	if err != nil {
		return // the session still works until its old expiry; try again next request
	}
	a.setSessionCookie(w, r, token, newExp)
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
	// Throttle the unauthenticated setup endpoint too — it's externally reachable
	// on a fresh instance until the first admin exists.
	if !a.loginAllowed(w, r, "setup:"+clientIP(r)) {
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
	// Throttle by IP and by username so neither dimension can be brute-forced.
	if !a.loginAllowed(w, r, "login:"+clientIP(r), "login-user:"+strings.ToLower(strings.TrimSpace(body.Username))) {
		return
	}
	u, err := a.deps.Auth.Authenticate(r.Context(), body.Username, body.Password)
	if err != nil {
		a.writeAuthError(w, err)
		return
	}
	a.startSession(w, r, u, http.StatusOK)
}

// loginAllowed checks every provided rate-limit key; the first that trips writes
// a 429 with a Retry-After and returns false. Wide-open when no limiter is set.
func (a *api) loginAllowed(w http.ResponseWriter, r *http.Request, keys ...string) bool {
	if a.loginLimiter == nil {
		return true
	}
	for _, k := range keys {
		if ok, retry := a.loginLimiter.allow(k); !ok {
			secs := int(retry.Seconds()) + 1
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			a.writeError(w, http.StatusTooManyRequests, "too many attempts — try again in a bit")
			return false
		}
	}
	return true
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
	a.setSessionCookie(w, r, token, expires)
	a.writeJSON(w, code, map[string]any{"user": u})
}

// setSessionCookie writes the session cookie, on sign-in and whenever the session slides.
func (a *api) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     a.cookiePath(),
		Expires:  expires,
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *api) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     a.cookiePath(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// requestIsHTTPS reports whether the ORIGINAL client request used HTTPS. Direct
// TLS sets r.TLS; behind a TLS-terminating reverse proxy (the exposed
// deployment) the proxy speaks plaintext to Go, so r.TLS is nil and we must
// trust the X-Forwarded-Proto / Forwarded header the proxy stamps. Without this
// the session cookie dropped its Secure flag exactly where it matters most.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		return strings.EqualFold(strings.TrimSpace(strings.Split(p, ",")[0]), "https")
	}
	if f := r.Header.Get("Forwarded"); strings.Contains(strings.ToLower(f), "proto=https") {
		return true
	}
	return false
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
	return a.decodeJSONLimit(w, r, dst, 1<<20)
}

// decodeJSONLimit is decodeJSON with a caller-chosen body cap, for the few bodies
// that legitimately carry a file. A body over the cap is reported as such — a
// season-pack .torrent that quietly failed as "invalid request body" was the
// original sin here.
func (a *api) decodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			a.writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("request body is too large (over %d MB)", limit>>20))
			return false
		}
		// Name the field when the body carries one the handler doesn't take, so the
		// next client/server mismatch can be diagnosed from the UI's error line. The
		// encoding/json message has no typed error, so match its fixed prefix.
		if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			a.writeError(w, http.StatusBadRequest, "invalid request body: unknown field "+field)
			return false
		}
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
