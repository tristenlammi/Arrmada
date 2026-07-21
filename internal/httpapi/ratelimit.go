package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// loginLimiter throttles authentication attempts. Cookie auth with bcrypt is the
// only cost otherwise, so an internet-exposed login is an unlimited online
// brute-force / credential-stuffing target. This caps attempts per key over a
// rolling window and returns a retry delay once the cap is hit.
//
// Keyed independently by client IP AND by username, so one attacker can't spray
// many usernames from one IP, and a distributed attack can't hammer one account
// from many IPs — either dimension trips.
type loginLimiter struct {
	mu      sync.Mutex
	hits    map[string][]int64 // key → attempt unix-nanos within the window
	max     int
	window  time.Duration
	lastGC  int64
	nowNano func() int64 // injectable for tests
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{
		hits:    map[string][]int64{},
		max:     max,
		window:  window,
		nowNano: func() int64 { return time.Now().UnixNano() },
	}
}

// allow records an attempt for key and reports whether it's permitted. When
// denied, retryAfter is how long until the oldest in-window attempt expires.
func (l *loginLimiter) allow(key string) (ok bool, retryAfter time.Duration) {
	now := l.nowNano()
	cutoff := now - l.window.Nanoseconds()
	l.mu.Lock()
	defer l.mu.Unlock()

	// Opportunistic GC so idle keys don't accumulate forever.
	if now-l.lastGC > l.window.Nanoseconds() {
		for k, ts := range l.hits {
			if len(ts) == 0 || ts[len(ts)-1] < cutoff {
				delete(l.hits, k)
			}
		}
		l.lastGC = now
	}

	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t >= cutoff {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.hits[key] = kept
		return false, time.Duration(kept[0] + l.window.Nanoseconds() - now)
	}
	l.hits[key] = append(kept, now)
	return true, 0
}

// clientIP is the best-effort source IP for rate-limiting: the proxy-forwarded
// client when present (the deployment is behind a reverse proxy), else the
// direct peer.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First hop is the original client.
		if i := indexByte(xff, ','); i >= 0 {
			return trimSpace(xff[:i])
		}
		return trimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
