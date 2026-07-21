package httpapi

import (
	"net"
	"net/http"
	"testing"
	"time"
)

// The limiter allows up to max attempts per key per window, then denies with a
// positive retry, and each key is independent.
func TestLoginLimiter(t *testing.T) {
	l := newLoginLimiter(3, time.Minute)
	now := int64(0)
	l.nowNano = func() int64 { return now }

	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("ip:a"); !ok {
			t.Fatalf("attempt %d should be allowed", i)
		}
	}
	ok, retry := l.allow("ip:a")
	if ok || retry <= 0 {
		t.Fatalf("4th attempt should be denied with a retry, got ok=%v retry=%v", ok, retry)
	}
	// A different key is unaffected.
	if ok, _ := l.allow("ip:b"); !ok {
		t.Fatal("independent key should be allowed")
	}
	// After the window passes, the key frees up.
	now = time.Minute.Nanoseconds() + 1
	if ok, _ := l.allow("ip:a"); !ok {
		t.Fatal("key should reset after the window")
	}
}

// requestIsHTTPS trusts the proxy's forwarded-proto (the TLS-terminating-proxy case).
func TestRequestIsHTTPS(t *testing.T) {
	mk := func(h map[string]string) *http.Request {
		r, _ := http.NewRequest("GET", "http://x/", nil)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	if requestIsHTTPS(mk(nil)) {
		t.Error("plain HTTP with no proxy header must be http")
	}
	if !requestIsHTTPS(mk(map[string]string{"X-Forwarded-Proto": "https"})) {
		t.Error("X-Forwarded-Proto: https must read as https")
	}
	if !requestIsHTTPS(mk(map[string]string{"X-Forwarded-Proto": "https, http"})) {
		t.Error("first XFP hop https must read as https")
	}
	if requestIsHTTPS(mk(map[string]string{"X-Forwarded-Proto": "http"})) {
		t.Error("X-Forwarded-Proto: http must read as http")
	}
}

// forwardedClientIP recovers the original client the proxy stamped.
func TestForwardedClientIP(t *testing.T) {
	mk := func(h map[string]string) *http.Request {
		r, _ := http.NewRequest("GET", "http://x/", nil)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	if ip := forwardedClientIP(mk(map[string]string{"X-Forwarded-For": "203.0.113.9, 10.0.0.1"})); ip == nil || ip.String() != "203.0.113.9" {
		t.Fatalf("XFF leftmost = %v, want 203.0.113.9", ip)
	}
	if ip := forwardedClientIP(mk(map[string]string{"Forwarded": `for="192.168.1.5:443";proto=https`})); ip == nil || ip.String() != "192.168.1.5" {
		t.Fatalf("Forwarded for = %v, want 192.168.1.5", ip)
	}
	if forwardedClientIP(mk(nil)) != nil {
		t.Error("no forward header → nil")
	}
}

// classifyExternal: a public forwarded client behind a private proxy is external;
// a private forwarded client (LAN behind a local proxy) is internal.
func TestClassifyExternalForwarded(t *testing.T) {
	a := &api{} // no ExternalHeader configured → the forwarded-IP path
	mk := func(remote string, h map[string]string) *http.Request {
		r, _ := http.NewRequest("GET", "http://x/", nil)
		r.RemoteAddr = remote
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	// Internet visitor through a local reverse proxy.
	if !a.classifyExternal(mk("127.0.0.1:9999", map[string]string{"X-Forwarded-For": "203.0.113.9"})) {
		t.Error("public forwarded client behind a proxy must be external")
	}
	// LAN client through a local reverse proxy — must NOT be locked out.
	if a.classifyExternal(mk("127.0.0.1:9999", map[string]string{"X-Forwarded-For": "192.168.1.20"})) {
		t.Error("private forwarded client (LAN) must be internal")
	}
	// Direct LAN peer, no proxy.
	if a.classifyExternal(mk("192.168.1.30:5000", nil)) {
		t.Error("direct private peer must be internal")
	}
	// Direct public peer (port-forward).
	if !a.classifyExternal(mk("203.0.113.50:5000", nil)) {
		t.Error("direct public peer must be external")
	}
}

var _ = net.ParseIP
