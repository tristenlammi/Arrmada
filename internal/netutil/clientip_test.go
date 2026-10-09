package netutil

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	for _, c := range []struct {
		name, remote string
		headers      map[string]string
		want         string
	}{
		{"public peer, forged XFF", "198.51.100.7:5000", map[string]string{"X-Forwarded-For": "6.6.6.6"}, "198.51.100.7"},
		{"public peer, forged Cf header", "198.51.100.7:5000", map[string]string{"Cf-Connecting-Ip": "203.0.113.9"}, "198.51.100.7"},
		{"no headers", "198.51.100.7:5000", nil, "198.51.100.7"},
		{"cloudflared container", "172.18.0.4:5000", map[string]string{"Cf-Connecting-Ip": "203.0.113.9", "X-Forwarded-For": "6.6.6.6, 203.0.113.9"}, "203.0.113.9"},
		{"loopback proxy, Cf header", "127.0.0.1:5000", map[string]string{"Cf-Connecting-Ip": "203.0.113.9"}, "203.0.113.9"},
		{"XFF right to left", "10.0.0.2:5000", map[string]string{"X-Forwarded-For": "6.6.6.6, 203.0.113.9"}, "203.0.113.9"},
		{"XFF skips our private proxies", "10.0.0.2:5000", map[string]string{"X-Forwarded-For": "6.6.6.6, 203.0.113.9, 10.0.0.5"}, "203.0.113.9"},
		{"XFF all private → the peer", "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "192.168.1.20, 10.0.0.5"}, "127.0.0.1"},
		{"LAN machine making up a private XFF", "192.168.1.30:5000", map[string]string{"X-Forwarded-For": "10.9.9.9"}, "192.168.1.30"},
		{"LAN machine making up a private Cf header", "192.168.1.30:5000", map[string]string{"Cf-Connecting-Ip": "10.9.9.9"}, "192.168.1.30"},
		{"XFF junk skipped", "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "unknown, 203.0.113.9"}, "203.0.113.9"},
		{"bad Cf header falls through", "127.0.0.1:5000", map[string]string{"Cf-Connecting-Ip": "nope", "X-Forwarded-For": "203.0.113.9"}, "203.0.113.9"},
		{"Forwarded with port and quotes", "127.0.0.1:5000", map[string]string{"Forwarded": `for="198.51.100.5:443";proto=https`}, "198.51.100.5"},
		{"Forwarded IPv6 in brackets", "127.0.0.1:5000", map[string]string{"Forwarded": `for="[2606:4700::1]:4711"`}, "2606:4700::1"},
		{"Forwarded right to left", "127.0.0.1:5000", map[string]string{"Forwarded": `for=6.6.6.6, for=203.0.113.9;proto=https`}, "203.0.113.9"},
		{"private peer, no headers", "192.168.1.30:5000", nil, "192.168.1.30"},
		{"IPv6 loopback peer", "[::1]:5000", map[string]string{"Cf-Connecting-Ip": "203.0.113.9"}, "203.0.113.9"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		if got := ClientIP(r); got != c.want {
			t.Errorf("%s: ClientIP = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestForwardedClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	if ForwardedClientIP(r) != nil {
		t.Error("no forward header → nil")
	}
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if ip := ForwardedClientIP(r); ip == nil || ip.String() != "203.0.113.9" {
		t.Errorf("got %v, want 203.0.113.9", ip)
	}
	// All-private chain: the leftmost, a LAN client behind a local proxy (what the
	// LAN/outside verdict needs).
	r.Header.Set("X-Forwarded-For", "192.168.1.20, 10.0.0.5")
	if ip := ForwardedClientIP(r); ip == nil || ip.String() != "192.168.1.20" {
		t.Errorf("all-private chain: got %v, want 192.168.1.20", ip)
	}
	r.Header.Del("X-Forwarded-For")
	r.Header.Set("Forwarded", `for="192.168.1.5:443";proto=https`)
	if ip := ForwardedClientIP(r); ip == nil || ip.String() != "192.168.1.5" {
		t.Errorf("Forwarded for: got %v, want 192.168.1.5", ip)
	}
	r.Header.Del("Forwarded")
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	r.RemoteAddr = "203.0.113.50:5000"
	if ip := ForwardedClientIP(r); ip != nil {
		t.Errorf("a public peer's headers were believed: %v", ip)
	}
}
