package httpapi

import (
	"context"
	"net"
	"net/http"
	"strings"
)

type extCtxKey int

const externalCtxKey extCtxKey = iota

// classifyExternal reports whether a request came from outside the LAN. A
// Cloudflare Tunnel (or reverse proxy) stamps ExternalHeader on forwarded
// requests; direct LAN hits don't carry it. As a fallback, a public source IP is
// treated as external too (direct port-forward). Unknown/loopback → internal.
func (a *api) classifyExternal(r *http.Request) bool {
	if h := a.deps.Config.ExternalHeader; h != "" && r.Header.Get(h) != "" {
		return true
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false // can't tell — treat as internal rather than lock the LAN out
	}
	return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

// isExternalRequest reads the classification stamped by externalGate.
func isExternalRequest(r *http.Request) bool {
	v, _ := r.Context().Value(externalCtxKey).(bool)
	return v
}

// externalAllowedPrefixes are the only API paths reachable from outside the LAN.
// Everything Discover/request/account/auth-related, so a remote requester can
// browse and request — nothing else.
var externalAllowedPrefixes = []string{
	"/api/health",
	"/api/v1/status",
	"/api/v1/auth/",    // login, logout, setup, me
	"/api/v1/me/",      // the requester's own notifications + push settings
	"/api/v1/discover", // discover feeds
	"/api/v1/books/discover",
	"/api/v1/media/",   // discover detail + image proxy
	"/api/v1/requests", // list own + create (+ approve/decline still role-gated)
}

// externalAllowed reports whether a path is reachable from outside the LAN. The
// SPA index + hashed assets (non-/api) always load; the app then renders the
// Discover-only shell for external visitors, and every other /api call is 403'd.
func externalAllowed(path string) bool {
	if !strings.HasPrefix(path, "/api/") {
		return true
	}
	for _, p := range externalAllowedPrefixes {
		if path == p || strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// externalGate classifies each request (LAN vs external) and, for external
// requests, blocks everything but the Discover-scope API + the SPA shell.
func (a *api) externalGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		external := a.classifyExternal(r)
		r = r.WithContext(context.WithValue(r.Context(), externalCtxKey, external))
		if external && !externalAllowed(a.pathAfterBase(r.URL.Path)) {
			a.writeError(w, http.StatusForbidden, "not available outside your network")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// pathAfterBase strips the configured reverse-proxy base path so allowlist checks
// work regardless of BaseURL.
func (a *api) pathAfterBase(p string) string {
	b := a.deps.Config.BaseURL
	if b != "" && b != "/" && strings.HasPrefix(p, b) {
		p = strings.TrimPrefix(p, b)
		if p == "" {
			p = "/"
		}
	}
	return p
}
