package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/eventbus"
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
	// X-Forwarded-For is read right to left: a visitor who prepends a LAN address is
	// still seen by the address our proxy appended.
	if !a.classifyExternal(mk("127.0.0.1:9999", map[string]string{"X-Forwarded-For": "192.168.1.20, 203.0.113.9"})) {
		t.Error("a forged private hop in front of a public one must stay external")
	}
	// A public peer's forwarded headers are ignored, whatever they claim.
	if !a.classifyExternal(mk("203.0.113.50:5000", map[string]string{"X-Forwarded-For": "192.168.1.20"})) {
		t.Error("a public peer claiming a LAN address must stay external")
	}
	// Behind cloudflared (a private Docker peer) with the configured header: external.
	b := &api{deps: Deps{Config: config.Config{ExternalHeader: "Cf-Connecting-Ip"}}}
	if !b.classifyExternal(mk("172.18.0.4:5000", map[string]string{"Cf-Connecting-Ip": "203.0.113.9", "X-Forwarded-For": "203.0.113.9"})) {
		t.Error("a tunnel visitor must be external")
	}
	if b.classifyExternal(mk("192.168.1.30:5000", nil)) {
		t.Error("a LAN client must stay internal with the header configured")
	}
}

// The failure-counting side of the limiter: only fail() counts, reset clears, and a
// tagged reset clears only that username's failures from an address.
func TestLoginLimiterFailures(t *testing.T) {
	l := newLoginLimiter(3, 15*time.Minute)
	now := int64(0)
	l.nowNano = func() int64 { return now }

	if blocked, _ := l.blocked("ip"); blocked {
		t.Fatal("blocked before any failure")
	}
	l.fail("ip", "alice")
	l.fail("ip", "alice")
	l.fail("ip", "bob")
	blocked, retry := l.blocked("ip")
	if !blocked || retry <= 0 || retry > 15*time.Minute {
		t.Fatalf("3 failures: blocked=%v retry=%v", blocked, retry)
	}
	// Checking doesn't count: still exactly blocked, and alice's success clears only hers.
	l.reset("ip", "alice")
	if blocked, _ := l.blocked("ip"); blocked {
		t.Fatal("alice's success should have cleared her two failures")
	}
	l.fail("ip", "bob")
	l.fail("ip", "bob")
	if blocked, _ := l.blocked("ip"); !blocked {
		t.Fatal("bob's failures should still block the address")
	}
	l.reset("ip", "")
	if blocked, _ := l.blocked("ip"); blocked {
		t.Fatal("a full reset should clear the address")
	}
	// Failures age out of the window.
	for i := 0; i < 3; i++ {
		l.fail("ip2", "x")
	}
	now += (15*time.Minute + time.Second).Nanoseconds()
	if blocked, _ := l.blocked("ip2"); blocked {
		t.Fatal("failures older than the window still block")
	}
}

// The per-username back-off: five free failures, then 30s, 1m, 2m, 4m … capped at 15m,
// counted from the last failure.
func TestLoginLimiterBackoff(t *testing.T) {
	l := newLoginLimiter(10, 15*time.Minute)
	now := int64(0)
	l.nowNano = func() int64 { return now }
	sec := time.Second.Nanoseconds()

	for i := 0; i < 4; i++ {
		l.fail("u", "u")
	}
	if d, _ := l.delayed("u"); d {
		t.Fatal("delayed after only four failures")
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i, w := range want {
		l.fail("u", "u")
		d, retry := l.delayed("u")
		if !d || retry != w {
			t.Fatalf("failure %d: delayed=%v retry=%v, want %v", 5+i, d, retry, w)
		}
		now += w.Nanoseconds() - sec
		if d, _ := l.delayed("u"); !d {
			t.Fatalf("failure %d: free again a second early", 5+i)
		}
		now += sec
		if d, _ := l.delayed("u"); d {
			t.Fatalf("failure %d: still delayed once the pause is over", 5+i)
		}
	}
	// An hour on, the slate is clean.
	now += time.Hour.Nanoseconds()
	l.fail("u", "u")
	if d, _ := l.delayed("u"); d {
		t.Fatal("old failures still count after an hour")
	}
}

// Idle keys are dropped so a flood of addresses can't grow the limiter forever.
func TestLoginLimiterGC(t *testing.T) {
	l := newLoginLimiter(10, 15*time.Minute)
	now := int64(0)
	l.nowNano = func() int64 { return now }
	for i := 0; i < 100; i++ {
		l.fail("login:"+strconv.Itoa(i), "x")
		l.allow("setup:" + strconv.Itoa(i))
	}
	now += (2 * time.Hour).Nanoseconds()
	l.fail("login:fresh", "x")
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.fails) != 1 || len(l.hits) != 0 {
		t.Errorf("after GC: %d failure keys, %d attempt keys; want 1 and 0", len(l.fails), len(l.hits))
	}
}

// signIn posts a login from remote (with optional headers) through the full handler
// chain and returns the status.
func signIn(s *routeServer, remote, user, pass string, headers map[string]string) int {
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	r := httptest.NewRequest("POST", "http://arrmada.local/api/v1/auth/login", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = remote
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	return rec.Code
}

// A stranger guessing the owner's password from the internet can't lock the owner out:
// the owner signs in from the LAN straight away, and from outside once the back-off
// passes (here, at once, because the LAN success reset the username).
func TestLoginThrottleCannotLockOwnerOut(t *testing.T) {
	s := newRouteServer(t, nil)
	if _, err := s.auth.CreateUser(context.Background(), "owner", "right-password", auth.RoleAdmin, true); err != nil {
		t.Fatal(err)
	}
	const attacker, owner = "203.0.113.9:4000", "198.51.100.20:4000"

	for i := 0; i < 5; i++ {
		if code := signIn(s, attacker, "owner", "wrong", nil); code != http.StatusUnauthorized {
			t.Fatalf("guess %d: HTTP %d, want 401", i, code)
		}
	}
	if code := signIn(s, attacker, "owner", "wrong", nil); code != http.StatusTooManyRequests {
		t.Errorf("6th guess at one username from outside: HTTP %d, want 429 (back-off)", code)
	}
	// Five more at other names fill the address's own limit.
	for i := 0; i < 5; i++ {
		_ = signIn(s, attacker, fmt.Sprintf("guess%d", i), "wrong", nil)
	}
	if code := signIn(s, attacker, "someone-else", "wrong", nil); code != http.StatusTooManyRequests {
		t.Errorf("11th failure from one address: HTTP %d, want 429", code)
	}
	// Another internet address: the username is backing off.
	if code := signIn(s, owner, "owner", "right-password", nil); code != http.StatusTooManyRequests {
		t.Errorf("owner from the internet during the back-off: HTTP %d, want 429", code)
	}
	// From home: no username back-off, a fresh address.
	if code := signIn(s, "192.168.1.20:5000", "owner", "right-password", nil); code != http.StatusOK {
		t.Fatalf("owner from the LAN: HTTP %d, want 200", code)
	}
	// That success reset the username, so the owner can sign in from outside too…
	if code := signIn(s, owner, "owner", "right-password", nil); code != http.StatusOK {
		t.Errorf("owner from the internet after a good sign-in: HTTP %d, want 200", code)
	}
	// …but the attacker's address keeps its record.
	if code := signIn(s, attacker, "owner", "right-password", nil); code != http.StatusTooManyRequests {
		t.Errorf("attacker's address after the owner's success: HTTP %d, want 429", code)
	}
}

// Successful sign-ins never use up the budget.
func TestLoginSuccessesDontCount(t *testing.T) {
	s := newRouteServer(t, nil)
	if _, err := s.auth.CreateUser(context.Background(), "owner", "right-password", auth.RoleAdmin, true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if code := signIn(s, "203.0.113.9:4000", "owner", "right-password", nil); code != http.StatusOK {
			t.Fatalf("sign-in %d: HTTP %d", i, code)
		}
	}
	// A wrong password between good ones is forgiven by the next success.
	for i := 0; i < 25; i++ {
		_ = signIn(s, "192.168.1.20:5000", "owner", "wrong", nil)
		if code := signIn(s, "192.168.1.20:5000", "owner", "right-password", nil); code != http.StatusOK {
			t.Fatalf("round %d: HTTP %d", i, code)
		}
	}
}

// A public peer can't dodge the per-address limit by making up forwarded headers, and
// behind cloudflared each visitor is held to their own address.
func TestLoginLimitUsesTheRealAddress(t *testing.T) {
	s := newRouteServer(t, func(d *Deps) { d.Config.ExternalHeader = "Cf-Connecting-Ip" })
	for i := 0; i < 10; i++ {
		fake := map[string]string{"X-Forwarded-For": fmt.Sprintf("10.9.9.%d", i), "Cf-Connecting-Ip": fmt.Sprintf("6.6.6.%d", i)}
		_ = signIn(s, "203.0.113.9:4000", fmt.Sprintf("user%d", i), "wrong", fake)
	}
	if code := signIn(s, "203.0.113.9:4000", "someone", "wrong", map[string]string{"X-Forwarded-For": "10.9.9.99"}); code != http.StatusTooManyRequests {
		t.Errorf("rotating forged headers dodged the limit: HTTP %d", code)
	}

	// Through the tunnel: the peer is cloudflared's private address for everyone.
	tunnel := "172.18.0.4:5000"
	for i := 0; i < 10; i++ {
		_ = signIn(s, tunnel, fmt.Sprintf("user%d", i), "wrong", map[string]string{"Cf-Connecting-Ip": "203.0.113.77"})
	}
	if code := signIn(s, tunnel, "someone", "wrong", map[string]string{"Cf-Connecting-Ip": "203.0.113.77"}); code != http.StatusTooManyRequests {
		t.Errorf("tunnel visitor not limited by their own address: HTTP %d", code)
	}
	if code := signIn(s, tunnel, "someone", "wrong", map[string]string{"Cf-Connecting-Ip": "198.51.100.5"}); code != http.StatusUnauthorized {
		t.Errorf("another tunnel visitor shared the first one's limit: HTTP %d", code)
	}
}

// Twenty wrong passwords for one username within the hour raise a staff-only event,
// without the password in it.
func TestLoginFailuresAlert(t *testing.T) {
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	events, cancel := bus.Subscribe("security.login_failures")
	defer cancel()
	s := newRouteServer(t, func(d *Deps) { d.Bus = bus })

	// From the LAN (no username back-off), spread over addresses to stay under the
	// per-address limit.
	for i := 0; i < 20; i++ {
		remote := fmt.Sprintf("192.168.1.%d:5000", 10+i/5)
		if code := signIn(s, remote, "Owner", "hunter2-wrong", nil); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: HTTP %d", i, code)
		}
	}
	select {
	case ev := <-events:
		data, _ := json.Marshal(ev.Data)
		if !strings.Contains(string(data), `"username":"owner"`) || !strings.Contains(string(data), `"count":20`) ||
			strings.Contains(string(data), "hunter2") {
			t.Errorf("event = %s", data)
		}
	default:
		t.Fatal("no security.login_failures event after 20 failures")
	}
}
