// Package netutil works out who a request really came from.
//
// Arrmada usually sits behind something: a Cloudflare tunnel (cloudflared in its own
// container, so a private Docker address is the TCP peer) or a reverse proxy on the
// same box. Those stamp the visitor's address into Cf-Connecting-Ip, X-Forwarded-For
// or Forwarded. From anyone else those headers are just text an attacker can set, so
// they're believed only when the request reached us from a loopback or private
// address. Login throttling, the LAN/outside verdict and the audiobook server all ask
// here, so they can't disagree about an address.
package netutil

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP is the address to hold a request to: the forwarded visitor when a local
// proxy vouches for one, otherwise the TCP peer.
func ClientIP(r *http.Request) string {
	if ip := ForwardedClientIP(r); ip != nil {
		return ip.String()
	}
	host, _ := peer(r)
	return host
}

// ForwardedClientIP returns the original client a local proxy stamped on r, or nil
// when the peer isn't a local proxy or it stamped nothing usable.
//
// Order: Cf-Connecting-Ip (Cloudflare sets it to the visitor and overwrites whatever
// the visitor sent); then X-Forwarded-For read right to left, taking the first hop
// that isn't a private address — each proxy appends the address it saw, so the
// right-hand end is what our own proxies vouch for and anything further left may be
// forged by the client (the leftmost entry is used only when every hop is private, a
// LAN client behind a local proxy); then the Forwarded header's for= by the same rule.
func ForwardedClientIP(r *http.Request) net.IP {
	if _, p := peer(r); p == nil || !(p.IsLoopback() || p.IsPrivate()) {
		return nil
	}
	if ip := parseHost(r.Header.Get("Cf-Connecting-Ip")); ip != nil {
		return ip
	}
	if ip := pickHop(splitList(r.Header.Values("X-Forwarded-For"))); ip != nil {
		return ip
	}
	return pickHop(forwardedFor(r.Header.Values("Forwarded")))
}

// IsPublic reports whether ip is an internet address: not private, loopback or
// link-local.
func IsPublic(ip net.IP) bool {
	return ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

// peer is the TCP peer's host and parsed address (nil when it isn't an IP).
func peer(r *http.Request) (string, net.IP) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host, net.ParseIP(host)
}

// pickHop walks hops right to left and returns the first public address, or the
// leftmost parseable one when none is public. Unparseable entries ("unknown",
// obfuscated ids) are skipped.
func pickHop(hops []string) net.IP {
	var leftmost net.IP
	for i := len(hops) - 1; i >= 0; i-- {
		ip := parseHost(hops[i])
		if ip == nil {
			continue
		}
		if IsPublic(ip) {
			return ip
		}
		leftmost = ip
	}
	return leftmost
}

// splitList flattens comma-separated header values (a header may also repeat).
func splitList(values []string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// forwardedFor pulls the for= values out of RFC 7239 Forwarded headers, in order.
func forwardedFor(values []string) []string {
	var out []string
	for _, elem := range splitList(values) {
		for _, pair := range strings.Split(elem, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "for") {
				out = append(out, strings.TrimSpace(v))
			}
		}
	}
	return out
}

// parseHost reads an address that may carry quotes, IPv6 brackets or a port:
// 203.0.113.9, "192.168.1.5:443", "[2001:db8::1]:4711", 2001:db8::1.
func parseHost(s string) net.IP {
	s = strings.Trim(strings.TrimSpace(s), `"`)
	if s == "" {
		return nil
	}
	if ip := net.ParseIP(s); ip != nil {
		return ip
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		return net.ParseIP(h)
	}
	return net.ParseIP(strings.Trim(s, "[]"))
}
