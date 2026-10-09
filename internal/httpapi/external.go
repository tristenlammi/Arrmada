package httpapi

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/netutil"
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
	if netutil.IsPublic(ip) {
		return true // public source IP → external (direct port-forward)
	}
	// A private/loopback PEER is a reverse proxy in front of us. Rather than the old
	// "no matching header → internal" (which silently exposed the whole LAN-only API
	// to the internet on any proxy that didn't set the configured header), look at the
	// forwarded ORIGINAL client IP: a public one is an internet visitor (external), a
	// private one is a genuine LAN client behind a local proxy (internal, so a LAN +
	// local-TLS-proxy setup isn't locked out). The hop is read right to left, so a
	// visitor who prepends a private address to X-Forwarded-For is still seen by the
	// address our own proxy appended.
	if fwd := netutil.ForwardedClientIP(r); fwd != nil {
		return netutil.IsPublic(fwd)
	}
	return false // no forward info, private peer → treat as LAN
}

// isStaffRequest reports whether the request carries a signed-in admin or manager.
// authenticate runs before the gate, so the session is already resolved here.
func isStaffRequest(r *http.Request) bool {
	u, ok := userFrom(r)
	return ok && u != nil && !u.Disabled && u.Role.AtLeast(auth.RoleManager)
}

// isExternalRequest reads the scope stamped by externalGate: true means this request
// is limited to Discover (outside the LAN and not staff).
func isExternalRequest(r *http.Request) bool {
	v, _ := r.Context().Value(externalCtxKey).(bool)
	return v
}

// externalGate classifies each request (LAN vs external) and stamps the verdict on
// it. It doesn't block anything itself: which routes are reachable from outside is
// the route table's ext() flag, checked by the router (router.go) for every route. It
// used to be a separate prefix list here, and the two drifted — a new route under an
// allowed prefix silently became internet-reachable, and My Books' cover images broke
// away from home because the list didn't name them.
//
// Staff are exempt: an admin or manager who has signed in gets the whole app from
// wherever they are. The Discover-only scope is for the accounts made for it
// (requesters, read-only) and for anyone not signed in. It used to apply to everyone,
// which meant the owner opening the app through their own tunnel hostname — from
// their own sofa — got the requester's view with no menus. Login is rate-limited, so
// what an internet visitor can reach without a staff password is unchanged.
func (a *api) externalGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		external := a.classifyExternal(r) && !isStaffRequest(r)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), externalCtxKey, external)))
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
