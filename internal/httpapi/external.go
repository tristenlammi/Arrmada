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
	if !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
		return true // public source IP → external (direct port-forward)
	}
	// A private/loopback PEER is a reverse proxy in front of us. Rather than the old
	// "no matching header → internal" (which silently exposed the whole LAN-only API
	// to the internet on any proxy that didn't set the configured header), look at the
	// forwarded ORIGINAL client IP: a public one is an internet visitor (external), a
	// private one is a genuine LAN client behind a local proxy (internal, so a LAN +
	// local-TLS-proxy setup isn't locked out). A remote client that forges the header
	// to look private only reaches role-gated endpoints anyway — the gate is defense in
	// depth, not the sole control.
	if fwd := forwardedClientIP(r); fwd != nil {
		return !fwd.IsPrivate() && !fwd.IsLoopback() && !fwd.IsLinkLocalUnicast()
	}
	return false // no forward info, private peer → treat as LAN
}

// forwardedClientIP returns the original client IP a reverse proxy stamped, from
// X-Forwarded-For (leftmost hop) or the Forwarded header, or nil if neither is set.
func forwardedClientIP(r *http.Request) net.IP {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		return net.ParseIP(first)
	}
	if f := r.Header.Get("Forwarded"); f != "" {
		for _, part := range strings.Split(f, ";") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(strings.ToLower(part), "for=") {
				v := strings.Trim(part[4:], `"[]`)
				if h, _, err := net.SplitHostPort(v); err == nil {
					v = h
				}
				return net.ParseIP(v)
			}
		}
	}
	return nil
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
