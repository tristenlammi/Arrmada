package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/applog"
	"github.com/tristenlammi/arrmada/internal/auth"
)

// keyReq sends a request carrying a personal API key the way scripts do: in X-Api-Key, or
// as Authorization: Bearer when bearer is set. From a LAN address unless ip says otherwise.
type keyReq struct {
	method, path, body, key string
	bearer                  bool
	ip                      string
	cookie                  *http.Cookie
}

func (s *routeServer) withKey(q keyReq) *httptest.ResponseRecorder {
	r := httptest.NewRequest(q.method, "http://arrmada.local"+q.path, strings.NewReader(q.body))
	r.RemoteAddr = "192.168.1.20:5000"
	if q.ip != "" {
		r.RemoteAddr = q.ip + ":5000"
	}
	if q.body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if q.key != "" {
		if q.bearer {
			r.Header.Set("Authorization", "Bearer "+q.key)
		} else {
			r.Header.Set("X-Api-Key", q.key)
		}
	}
	if q.cookie != nil {
		r.AddCookie(q.cookie)
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	return rec
}

type madeKey struct {
	Key    string      `json:"key"`
	APIKey auth.APIKey `json:"api_key"`
}

// makeKey creates a key through the API as the session's owner (password "password123").
func (s *routeServer) makeKey(t *testing.T, c *http.Cookie, name, scope string, days int) madeKey {
	t.Helper()
	body := `{"name":` + strconv.Quote(name) + `,"scope":"` + scope + `","expires_in_days":` + strconv.Itoa(days) + `,"password":"password123"}`
	rec := s.doJSON("POST", "/api/v1/me/api-keys", c, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create key: HTTP %d %s", rec.Code, rec.Body)
	}
	var m madeKey
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// The table: every route marked for keys exists, is one already open to callers from
// outside the network, and none of them is about who you are or how you sign in.
func TestAPIKeyRoutesTable(t *testing.T) {
	rt := testRouter(t, true)
	registered := map[string]routeSpec{}
	for _, s := range rt.specs {
		registered[s.Method+" "+s.Pattern] = s
	}
	for pattern := range apiKeyRoutes {
		spec, ok := registered[pattern]
		if !ok {
			t.Errorf("apiKeyRoutes lists %q, which isn't a route", pattern)
			continue
		}
		if !spec.External {
			t.Errorf("%q is marked for API keys but not ext(): keys reach only the outside-facing surface", pattern)
		}
		if spec.Scope == scopeAdmin {
			t.Errorf("%q is admin-only: API keys never reach admin routes", pattern)
		}
	}
}

// Account security stays a signed-in person's business: no key reaches sign-in, sessions,
// passwords, two-factor, key management, the support bundle or outside services' keys —
// including routes that other work adds later under those names.
func TestAPIKeyRoutesExcludeAccountSecurity(t *testing.T) {
	banned := []string{
		"/auth/login", "/auth/logout", "/auth/setup", "/auth/plex", "/auth/jellyfin",
		"/sessions", "/password", "/2fa", "/totp", "/two-factor", "/mfa", "/otp", "/recovery",
		"/api-keys", "/apikeys", "/access-keys", "/support-bundle", "/logs", "/backups",
		"/me/audio", "/me/push", "/me/apprise", "/me/plex", "/me/jellyfin", "/me/kindle",
		"/me/account", "/me/notify-prefs", "/me/calendar-feed", "/users", "/people", "/ws",
	}
	for _, s := range testRouter(t, true).specs {
		if !s.APIKey {
			continue
		}
		for _, b := range banned {
			if strings.Contains(s.Pattern, b) {
				t.Errorf("%s %s is open to API keys but is account security (%s)", s.Method, s.Pattern, b)
			}
		}
	}
}

// Every route, called with a key of every role and both scopes: a route not marked for
// keys answers 403 whatever the role, a read-only key gets 403 from anything but GET, and
// otherwise the role rules are exactly a session's.
func TestRouteAuthzWithAPIKeys(t *testing.T) {
	rt := testRouter(t, true)
	reached := 0
	for _, spec := range rt.specs {
		method := spec.Method
		if method == "" {
			method = http.MethodGet
		}
		path := samplePath(spec.Pattern)
		if spec.Method == "" {
			path = "/movies"
		}
		for _, who := range walkRoles[1:] {
			for _, scope := range []auth.KeyScope{auth.KeyScopeRead, auth.KeyScopeFull} {
				r := httptest.NewRequest(method, path, nil)
				r = withKeyUse(r, &auth.KeyUse{User: &auth.User{ID: 7, Username: "someone", Role: who.role}, KeyID: 1, Scope: scope})
				rec := httptest.NewRecorder()
				rt.ServeHTTP(rec, r)
				want := sentinelStatus
				switch {
				case !spec.APIKey:
					want = http.StatusForbidden
				case scope == auth.KeyScopeRead && method != http.MethodGet:
					want = http.StatusForbidden
				case spec.Scope != scopePublic && !who.role.AtLeast(spec.Scope.role()):
					want = http.StatusForbidden
				}
				if rec.Code != want {
					t.Errorf("%s as a %s %s key: HTTP %d, want %d", spec, scope, who.name, rec.Code, want)
				}
				if rec.Code == sentinelStatus {
					reached++
				}
			}
		}
	}
	if reached == 0 {
		t.Fatal("no route let a key through at all")
	}
}

// Making, listing, using and revoking a key, end to end.
func TestPersonalAPIKeyLifecycle(t *testing.T) {
	s := newRouteServer(t, nil)
	u, c := s.user(t, "kid@example.com", auth.RoleRequester)

	// The account's password is asked for, and a wrong one refused.
	if rec := s.doJSON("POST", "/api/v1/me/api-keys", c, `{"name":"n8n","scope":"full","expires_in_days":0,"password":"nope"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "password is wrong") {
		t.Fatalf("wrong password: HTTP %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{
		`{"name":"","scope":"full","expires_in_days":0,"password":"password123"}`,
		`{"name":"x","scope":"admin","expires_in_days":0,"password":"password123"}`,
		`{"name":"x","scope":"full","expires_in_days":7,"password":"password123"}`,
	} {
		if rec := s.doJSON("POST", "/api/v1/me/api-keys", c, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", bad, rec.Code)
		}
	}

	m := s.makeKey(t, c, "n8n workflow", "full", 90)
	if !strings.HasPrefix(m.Key, "arrk_") || !auth.LooksLikeAPIKey(m.Key) {
		t.Fatalf("key %q hasn't the arrk_ shape", m.Key)
	}
	if want := "arrk_…" + m.Key[len(m.Key)-4:]; m.APIKey.Hint != want {
		t.Errorf("hint = %q, want %q", m.APIKey.Hint, want)
	}
	if m.APIKey.ExpiresAt == nil || m.APIKey.Role != auth.RoleRequester || m.APIKey.Scope != auth.KeyScopeFull {
		t.Errorf("listing = %+v", m.APIKey)
	}
	// Only the hash is stored.
	var stored string
	if err := s.st.DB().QueryRow(`SELECT key_hash FROM api_keys WHERE id = ?`, m.APIKey.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != sha(m.Key) {
		t.Errorf("stored %q, want the key's SHA-256", stored)
	}
	var plain int
	if err := s.st.DB().QueryRow(`SELECT COUNT(*) FROM api_keys WHERE key_hash = ? OR name = ? OR last4 = ?`, m.Key, m.Key, m.Key).Scan(&plain); err != nil || plain != 0 {
		t.Errorf("the key itself is in the table (%d, %v)", plain, err)
	}

	// Shown once: the list carries neither the key nor its hash.
	rec := s.do("GET", "/api/v1/me/api-keys", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: HTTP %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, m.Key) || strings.Contains(body, stored) || strings.Contains(body, m.Key[5:20]) {
		t.Errorf("the list gives the key away: %s", body)
	}
	if !strings.Contains(body, `"name":"n8n workflow"`) || !strings.Contains(body, `"password_required":true`) {
		t.Errorf("list: %s", body)
	}

	// It works in either header, as its owner.
	for _, bearer := range []bool{false, true} {
		rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: m.Key, bearer: bearer})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"kid@example.com"`) {
			t.Errorf("bearer=%v: HTTP %d %s", bearer, rec.Code, rec.Body)
		}
	}
	// Last used, with the coarse network, shows in the owner's list.
	var listed struct{ Keys []auth.APIKey }
	_ = json.Unmarshal(s.do("GET", "/api/v1/me/api-keys", c).Body.Bytes(), &listed)
	if len(listed.Keys) != 1 || listed.Keys[0].LastUsedAt == nil || listed.Keys[0].LastUsedNetwork != "local" {
		t.Errorf("after use: %+v", listed.Keys)
	}

	// Never in the address.
	for _, p := range []string{"/api/v1/auth/me?apikey=" + m.Key, "/api/v1/auth/me?api_key=" + m.Key, "/api/v1/discover/search?q=x&token=" + m.Key} {
		if rec := s.do("GET", p, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: HTTP %d, want 400", strings.Replace(p, m.Key, "arrk_…", 1), rec.Code)
		}
	}

	// A key can't manage keys, sessions or the password, or reach routes not marked for it.
	for _, call := range []struct{ method, path, body string }{
		{"GET", "/api/v1/me/api-keys", ""},
		{"POST", "/api/v1/me/api-keys", `{"name":"more","scope":"full","expires_in_days":0,"password":"password123"}`},
		{"DELETE", "/api/v1/me/api-keys/" + strconv.FormatInt(m.APIKey.ID, 10), ""},
		{"GET", "/api/v1/me/sessions", ""},
		{"POST", "/api/v1/me/sessions/revoke-others", ""},
		{"POST", "/api/v1/me/password", `{"current":"password123","new":"hijacked123"}`},
		{"GET", "/api/v1/me/account", ""},
		{"POST", "/api/v1/auth/logout", ""},
		{"GET", "/api/v1/calendar", ""},
		{"GET", "/movies", ""},
	} {
		if rec := s.withKey(keyReq{method: call.method, path: call.path, body: call.body, key: m.Key}); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with a key: HTTP %d, want 403", call.method, call.path, rec.Code)
		}
	}
	if n := countKeys(t, s, u.ID); n != 1 {
		t.Errorf("the key made %d keys", n-1)
	}
	if !s.auth.CheckPassword(context.Background(), u.ID, "password123") {
		t.Error("the key changed the password")
	}

	// Revoking takes effect on the very next request.
	if rec := s.do("DELETE", "/api/v1/me/api-keys/"+strconv.FormatInt(m.APIKey.ID, 10), c); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: m.Key}); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked key: HTTP %d, want 401", rec.Code)
	}
	if rec := s.do("DELETE", "/api/v1/me/api-keys/"+strconv.FormatInt(m.APIKey.ID, 10), c); rec.Code != http.StatusNotFound {
		t.Errorf("revoke twice: HTTP %d, want 404", rec.Code)
	}
}

func countKeys(t *testing.T, s *routeServer, userID int64) int {
	t.Helper()
	var n int
	if err := s.st.DB().QueryRow(`SELECT COUNT(*) FROM api_keys WHERE user_id = ?`, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A presented key wins over a browser cookie riding along, so a read-only key can't be
// widened by a session, and a bad key isn't rescued by one.
func TestAPIKeyWinsOverCookie(t *testing.T) {
	s := newRouteServer(t, nil)
	_, c := s.user(t, "kid@example.com", auth.RoleRequester)
	m := s.makeKey(t, c, "read", "read", 0)
	if rec := s.withKey(keyReq{method: "POST", path: "/api/v1/requests", body: `{}`, key: m.Key, cookie: c}); rec.Code != http.StatusForbidden {
		t.Errorf("read-only key with a cookie: HTTP %d, want 403", rec.Code)
	}
	bad := "arrk_" + strings.Repeat("a", 52)
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: bad, cookie: c}); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad key with a cookie: HTTP %d, want 401", rec.Code)
	}
}

// Scope and role: a read-only key only reads; a key is capped by the role it was made
// under (a promotion never widens it) and follows a demotion at once.
func TestAPIKeyScopeAndRoleCap(t *testing.T) {
	s := newRouteServer(t, nil)
	ctx := context.Background()
	kid, kc := s.user(t, "kid@example.com", auth.RoleRequester)
	mgr, mc := s.user(t, "mgr@example.com", auth.RoleManager)

	read := s.makeKey(t, kc, "read only", "read", 0)
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/requests", key: read.Key}); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Errorf("read-only GET: HTTP %d", rec.Code)
	}
	rec := s.withKey(keyReq{method: "POST", path: "/api/v1/requests", body: `{"media_type":"movie","tmdb_id":1}`, key: read.Key})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "read-only") {
		t.Errorf("read-only POST: HTTP %d %s, want 403", rec.Code, rec.Body)
	}

	// A requester's full key still can't do staff things.
	full := s.makeKey(t, kc, "full", "full", 0)
	if rec := s.withKey(keyReq{method: "POST", path: "/api/v1/requests/1/approve", body: `{}`, key: full.Key}); rec.Code != http.StatusForbidden {
		t.Errorf("requester key approving: HTTP %d, want 403", rec.Code)
	}
	// Promoted after the key was made: the key stays a requester's.
	if err := s.auth.UpdateUser(ctx, kid.ID, auth.RoleManager, auth.AutoApproval{}); err != nil {
		t.Fatal(err)
	}
	if rec := s.withKey(keyReq{method: "POST", path: "/api/v1/requests/1/approve", body: `{}`, key: full.Key}); rec.Code != http.StatusForbidden {
		t.Errorf("promotion widened the key: HTTP %d, want 403", rec.Code)
	}
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: full.Key}); !strings.Contains(rec.Body.String(), `"role":"requester"`) {
		t.Errorf("a promoted account's old key acts as %s", rec.Body)
	}

	// A manager's key approves (the route lets it through; there's no request 1)…
	mk := s.makeKey(t, mc, "staff", "full", 0)
	if rec := s.withKey(keyReq{method: "POST", path: "/api/v1/requests/1/approve", body: `{}`, key: mk.Key}); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Errorf("manager key approving: HTTP %d %s", rec.Code, rec.Body)
	}
	// …but still can't use a staff route that isn't open to keys.
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/queue", key: mk.Key}); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "API keys can't") {
		t.Errorf("manager key on the queue: HTTP %d %s, want 403", rec.Code, rec.Body)
	}
	// Demoted: the key follows at once.
	if err := s.auth.UpdateUser(ctx, mgr.ID, auth.RoleRequester, auth.AutoApproval{}); err != nil {
		t.Fatal(err)
	}
	if rec := s.withKey(keyReq{method: "POST", path: "/api/v1/requests/1/approve", body: `{}`, key: mk.Key}); rec.Code != http.StatusForbidden {
		t.Errorf("demoted manager's key approving: HTTP %d, want 403", rec.Code)
	}
}

// Disabled, deleted or expired: the key stops working on the next request.
func TestAPIKeyStopsWithItsAccount(t *testing.T) {
	s := newRouteServer(t, nil)
	ctx := context.Background()
	u, c := s.user(t, "kid@example.com", auth.RoleRequester)
	m := s.makeKey(t, c, "n8n", "full", 30)
	ok := func(want int, why string) {
		t.Helper()
		if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: m.Key}); rec.Code != want {
			t.Errorf("%s: HTTP %d, want %d", why, rec.Code, want)
		}
	}
	ok(http.StatusOK, "fresh")
	if err := s.auth.SetDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	ok(http.StatusUnauthorized, "disabled")
	if err := s.auth.SetDisabled(ctx, u.ID, false); err != nil {
		t.Fatal(err)
	}
	ok(http.StatusOK, "enabled again")
	// Disabling signed the browser out; sign in again.
	tok, _, _ := s.auth.CreateSession(ctx, u.ID)
	c = &http.Cookie{Name: sessionCookieName, Value: tok}
	if _, err := s.st.DB().Exec(`UPDATE api_keys SET expires_at = '2001-01-01 00:00:00' WHERE id = ?`, m.APIKey.ID); err != nil {
		t.Fatal(err)
	}
	ok(http.StatusUnauthorized, "expired")
	var listed struct{ Keys []auth.APIKey }
	_ = json.Unmarshal(s.do("GET", "/api/v1/me/api-keys", c).Body.Bytes(), &listed)
	if len(listed.Keys) != 1 || !listed.Keys[0].Expired {
		t.Errorf("an expired key should be listed as expired: %+v", listed.Keys)
	}

	m2 := s.makeKey(t, c, "another", "full", 0)
	if err := s.auth.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: m2.Key}); rec.Code != http.StatusUnauthorized {
		t.Errorf("deleted account's key: HTTP %d, want 401", rec.Code)
	}
	if n := countKeys(t, s, u.ID); n != 0 {
		t.Errorf("%d keys outlived their account", n)
	}
}

// fakeKey is the i-th made-up key: shaped like a personal API key, never a real one.
func fakeKey(i int) string {
	const b32 = "abcdefghijklmnopqrstuvwxyz234567"
	return "arrk_" + string(b32[i%32]) + string(b32[(i/32)%32]) + strings.Repeat("q", 50)
}

// A stale key in a script slows only itself: ten wrong tries of the same key and that key
// gets 429, while every other key behind the same address — a shared NAT, the n8n host —
// keeps working, a different wrong key still gets its plain 401, and signing in from there
// is untouched.
func TestStaleKeyOnlyThrottlesItself(t *testing.T) {
	s := newRouteServer(t, nil)
	_, c := s.user(t, "kid@example.com", auth.RoleRequester)
	good := s.makeKey(t, c, "good", "read", 0)
	const shared = "203.0.113.9"
	stale := fakeKey(1)
	for i := 0; i < 10; i++ {
		if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: stale, ip: shared}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("stale key try %d: HTTP %d", i, rec.Code)
		}
	}
	rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: stale, ip: shared})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("stale key, eleventh try: HTTP %d, want 429 with Retry-After", rec.Code)
	}
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: good.Key, ip: shared}); rec.Code != http.StatusOK {
		t.Errorf("a good key behind the same address: HTTP %d", rec.Code)
	}
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: fakeKey(2), ip: shared}); rec.Code != http.StatusUnauthorized {
		t.Errorf("another wrong key from there: HTTP %d, want 401", rec.Code)
	}
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: stale, ip: "198.51.100.4"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("the stale key from another address: HTTP %d, want 401", rec.Code)
	}
	if rec := s.doFrom("POST", "/api/v1/auth/login", `{"username":"kid@example.com","password":"password123"}`, shared); rec.Code != http.StatusOK {
		t.Errorf("signing in from that address: HTTP %d %s", rec.Code, rec.Body)
	}
}

// Trying key after key from one address hits a much higher ceiling (keySprayMax), after
// which every wrong key from there gets 429 — but a key that validates still passes.
func TestKeySprayCeiling(t *testing.T) {
	s := newRouteServer(t, nil)
	_, c := s.user(t, "kid@example.com", auth.RoleRequester)
	good := s.makeKey(t, c, "good", "read", 0)
	if keySprayMax >= maxFailsKept || keySprayMax <= 10 {
		t.Fatalf("keySprayMax %d must sit above the per-key 10 and under the %d failures one record keeps", keySprayMax, maxFailsKept)
	}
	const sprayer = "203.0.113.66"
	for i := 0; i < keySprayMax; i++ {
		if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: fakeKey(i), ip: sprayer}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong key %d: HTTP %d, want 401", i, rec.Code)
		}
	}
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: fakeKey(keySprayMax + 1), ip: sprayer}); rec.Code != http.StatusTooManyRequests {
		t.Errorf("past the ceiling: HTTP %d, want 429", rec.Code)
	}
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: good.Key, ip: sprayer}); rec.Code != http.StatusOK {
		t.Errorf("a right key from a throttled address: HTTP %d, want 200", rec.Code)
	}
}

// Other things in the same headers — an audiobook app's bearer token, another service's
// key — aren't personal API keys: they're left alone, not refused or counted.
func TestOtherBearerTokensAreLeftAlone(t *testing.T) {
	s := newRouteServer(t, nil)
	for i := 0; i < 15; i++ {
		rec := s.withKey(keyReq{method: "GET", path: "/api/v1/status", key: "eyJhbGciOiJIUzI1NiJ9.audio-app-token", bearer: true, ip: "203.0.113.9"})
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d with a foreign bearer token: HTTP %d", i, rec.Code)
		}
	}
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/status", key: "widget-key-123"}); rec.Code != http.StatusOK {
		t.Errorf("a foreign X-Api-Key: HTTP %d", rec.Code)
	}
}

// The admin sees everyone's keys (whose, never from where) and can revoke any; nobody else
// can see that list.
func TestAdminListsAndRevokesKeys(t *testing.T) {
	s := newRouteServer(t, nil)
	_, kc := s.user(t, "kid@example.com", auth.RoleRequester)
	_, mc := s.user(t, "mgr@example.com", auth.RoleManager)
	_, ac := s.user(t, "owner", auth.RoleAdmin)
	m := s.makeKey(t, kc, "home assistant", "read", 0)
	s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: m.Key, ip: "203.0.113.9"})

	for _, c := range []*http.Cookie{kc, mc} {
		if rec := s.do("GET", "/api/v1/users/api-keys", c); rec.Code != http.StatusForbidden {
			t.Errorf("non-admin listing everyone's keys: HTTP %d", rec.Code)
		}
	}
	rec := s.do("GET", "/api/v1/users/api-keys", ac)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"username":"kid@example.com"`) || !strings.Contains(body, `"name":"home assistant"`) {
		t.Fatalf("admin list: HTTP %d %s", rec.Code, body)
	}
	if strings.Contains(body, "203.0.113") || strings.Contains(body, "last_used_network") || strings.Contains(body, m.Key) || strings.Contains(body, sha(m.Key)) {
		t.Errorf("admin list shows where from, or the key: %s", body)
	}
	// An admin's key can't reach the admin's list either.
	ak := s.makeKey(t, ac, "admin script", "full", 0)
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/users/api-keys", key: ak.Key}); rec.Code != http.StatusForbidden {
		t.Errorf("admin key on the key list: HTTP %d, want 403", rec.Code)
	}
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/system/support-bundle", key: ak.Key}); rec.Code != http.StatusForbidden {
		t.Errorf("admin key on the support bundle: HTTP %d, want 403", rec.Code)
	}
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/apikeys", key: ak.Key}); rec.Code != http.StatusForbidden {
		t.Errorf("admin key on outside services' keys: HTTP %d, want 403", rec.Code)
	}
	// The requester can't revoke through the admin route; the admin can.
	id := strconv.FormatInt(m.APIKey.ID, 10)
	if rec := s.do("DELETE", "/api/v1/users/api-keys/"+id, kc); rec.Code != http.StatusForbidden {
		t.Errorf("requester on the admin revoke: HTTP %d", rec.Code)
	}
	// Nor through their own route for someone else's key.
	if rec := s.do("DELETE", "/api/v1/me/api-keys/"+strconv.FormatInt(ak.APIKey.ID, 10), kc); rec.Code != http.StatusNotFound {
		t.Errorf("revoking another account's key as your own: HTTP %d, want 404", rec.Code)
	}
	if rec := s.do("DELETE", "/api/v1/users/api-keys/"+id, ac); rec.Code != http.StatusNoContent {
		t.Fatalf("admin revoke: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: m.Key}); rec.Code != http.StatusUnauthorized {
		t.Errorf("admin-revoked key: HTTP %d, want 401", rec.Code)
	}
}

// A requester's key sees exactly what their session sees: requests are shaped the same
// way (their own and followed ones, nobody else's).
func TestRequesterKeySeesWhatTheSessionSees(t *testing.T) {
	s := bookCardServer(t)
	kid, kc := s.user(t, "kid@example.com", auth.RoleRequester)
	aunt, _ := s.user(t, "aunt@example.com", auth.RoleRequester)
	for _, q := range []struct {
		title string
		by    int64
	}{{"Kid's Movie", kid.ID}, {"Aunt's Secret Movie", aunt.ID}} {
		if _, err := s.st.DB().Exec(`INSERT INTO requests (media_type, tmdb_id, title, status, requested_by, requested_by_name, note) VALUES ('movie', ?, ?, 'pending', ?, 'x', 'private note')`,
			len(q.title), q.title, q.by); err != nil {
			t.Fatal(err)
		}
	}
	m := s.makeKey(t, kc, "script", "read", 0)
	byKey := s.withKey(keyReq{method: "GET", path: "/api/v1/requests", key: m.Key})
	bySession := s.do("GET", "/api/v1/requests", kc)
	if byKey.Code != http.StatusOK || byKey.Body.String() != bySession.Body.String() {
		t.Fatalf("key and session differ:\nkey     %d %s\nsession %d %s", byKey.Code, byKey.Body, bySession.Code, bySession.Body)
	}
	if strings.Contains(byKey.Body.String(), "Aunt's Secret Movie") || !strings.Contains(byKey.Body.String(), "Kid's Movie") {
		t.Errorf("requester key list: %s", byKey.Body)
	}
}

// A Plex-only account (no password anyone knows) makes keys on its session alone.
func TestPlexOnlyAccountMakesKeysWithoutPassword(t *testing.T) {
	s := newRouteServer(t, nil)
	ctx := context.Background()
	u, err := s.auth.FindOrCreatePlexUser(ctx, "4242", "kidplex", auth.RoleRequester, auth.AutoApproval{})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, _ := s.auth.CreateSession(ctx, u.ID)
	c := &http.Cookie{Name: sessionCookieName, Value: tok}
	if rec := s.do("GET", "/api/v1/me/api-keys", c); !strings.Contains(rec.Body.String(), `"password_required":false`) {
		t.Errorf("list: %s", rec.Body)
	}
	if rec := s.doJSON("POST", "/api/v1/me/api-keys", c, `{"name":"x","scope":"read","expires_in_days":0}`); rec.Code != http.StatusCreated {
		t.Errorf("create: HTTP %d %s", rec.Code, rec.Body)
	}

	// The same browser an hour after signing in has nothing left to prove it's them:
	// sign in again (Plex is this account's step-up).
	if _, err := s.st.DB().Exec(`UPDATE sessions SET created_at = datetime('now', '-1 hour') WHERE user_id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if rec := s.do("GET", "/api/v1/me/api-keys", c); !strings.Contains(rec.Body.String(), `"reauth_required":true`) {
		t.Errorf("list after an hour: %s", rec.Body)
	}
	rec := s.doJSON("POST", "/api/v1/me/api-keys", c, `{"name":"y","scope":"read","expires_in_days":0}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"reauth"`) {
		t.Errorf("create an hour after signing in: HTTP %d %s, want 403 reauth", rec.Code, rec.Body)
	}
	// Signing in again makes it possible once more.
	tok2, _, _ := s.auth.CreateSession(ctx, u.ID)
	if rec := s.doJSON("POST", "/api/v1/me/api-keys", &http.Cookie{Name: sessionCookieName, Value: tok2}, `{"name":"y","scope":"read","expires_in_days":0}`); rec.Code != http.StatusCreated {
		t.Errorf("create after signing in again: HTTP %d %s", rec.Code, rec.Body)
	}
	// An account with a password isn't held to the window: its password is the step-up.
	_, pc := s.user(t, "aunt@example.com", auth.RoleRequester)
	if _, err := s.st.DB().Exec(`UPDATE sessions SET created_at = datetime('now', '-1 day')`); err != nil {
		t.Fatal(err)
	}
	s.makeKey(t, pc, "old session, with password", "read", 0)
}

// The two-factor routes are never open to keys, and a key can't sign in: it gets 403 on
// the sign-in routes before finishPasswordSignIn ever runs, so no session or ticket comes
// out of it.
func TestKeysNeverReachTwoFactorOrSignIn(t *testing.T) {
	rt := testRouter(t, true)
	specs := map[string]routeSpec{}
	for _, s := range rt.specs {
		specs[s.Method+" "+s.Pattern] = s
	}
	for _, p := range []string{
		"GET /api/v1/me/2fa", "POST /api/v1/me/2fa/setup", "POST /api/v1/me/2fa/confirm",
		"POST /api/v1/me/2fa/disable", "POST /api/v1/me/2fa/recovery-codes",
		"POST /api/v1/auth/login", "POST /api/v1/auth/login/code", "POST /api/v1/auth/login/setup",
		"POST /api/v1/auth/login/setup/confirm", "POST /api/v1/users/{id}/2fa/reset",
	} {
		spec, ok := specs[p]
		if !ok {
			t.Errorf("%s isn't registered (renamed? update this list)", p)
			continue
		}
		if spec.APIKey || apiKeyRoutes[p] {
			t.Errorf("%s is open to API keys", p)
		}
	}

	s := newRouteServer(t, nil)
	_, c := s.user(t, "kid@example.com", auth.RoleRequester)
	m := s.makeKey(t, c, "n8n", "full", 0)
	for _, call := range []struct{ path, body string }{
		{"/api/v1/auth/login", `{"username":"kid@example.com","password":"password123"}`},
		{"/api/v1/auth/login/code", `{"ticket":"x","code":"123456"}`},
		{"/api/v1/auth/login/setup", `{"ticket":"x"}`},
		{"/api/v1/me/2fa/setup", `{"password":"password123"}`},
	} {
		rec := s.withKey(keyReq{method: "POST", path: call.path, body: call.body, key: m.Key})
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s with a key: HTTP %d, want 403", call.path, rec.Code)
		}
		if ck := rec.Result().Cookies(); len(ck) > 0 {
			t.Errorf("POST %s with a key set cookies %v", call.path, ck)
		}
		if strings.Contains(rec.Body.String(), "ticket") {
			t.Errorf("POST %s with a key answered a ticket: %s", call.path, rec.Body)
		}
	}
}

// With two-factor sign-in on, making a key takes a current code as well as the password;
// a missing or wrong code makes nothing, and a bad form is refused before the code is
// spent.
func TestCreateKeyAsksForTwoFactorCode(t *testing.T) {
	f := newTFFixture(t)
	u, c := f.user(t, "kid@example.com", auth.RoleRequester)
	secret, _ := f.enrolMe(t, c, "password123")
	if rec := f.do("GET", "/api/v1/me/api-keys", c); !strings.Contains(rec.Body.String(), `"code_required":true`) {
		t.Fatalf("list: %s", rec.Body)
	}
	post := func(body map[string]any) *httptest.ResponseRecorder {
		return f.post("/api/v1/me/api-keys", body, c)
	}
	base := func(extra map[string]any) map[string]any {
		b := map[string]any{"name": "n8n", "scope": "full", "expires_in_days": 0, "password": "password123"}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	if rec := post(base(nil)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "code isn't right") {
		t.Errorf("no code: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := post(base(map[string]any{"code": "000000"})); rec.Code != http.StatusBadRequest {
		t.Errorf("wrong code: HTTP %d %s", rec.Code, rec.Body)
	}
	if n := countKeys(t, f.routeServer, u.ID); n != 0 {
		t.Fatalf("%d keys made without a right code", n)
	}
	f.clock.next()
	code := totpAt(t, secret, f.clock.now())
	// A bad form with a right code: refused, and the code isn't used up by it.
	if rec := post(base(map[string]any{"code": code, "scope": "admin"})); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "scope") {
		t.Errorf("bad scope: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := post(base(map[string]any{"code": code})); rec.Code != http.StatusCreated {
		t.Fatalf("right password and code: HTTP %d %s", rec.Code, rec.Body)
	}
	// The password is still asked for alongside the code.
	f.clock.next()
	if rec := post(base(map[string]any{"code": totpAt(t, secret, f.clock.now()), "password": "nope"})); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "password is wrong") {
		t.Errorf("right code, wrong password: HTTP %d %s", rec.Code, rec.Body)
	}
}

// A password change (the owner's or an admin's reset) and an admin's "sign out
// everywhere" revoke the account's keys at once; the answers say how many.
func TestPasswordChangeAndSignOutEverywhereRevokeKeys(t *testing.T) {
	s := newRouteServer(t, nil)
	u, c := s.user(t, "kid@example.com", auth.RoleRequester)
	_, admin := s.user(t, "owner", auth.RoleAdmin)
	works := func(key string) int {
		return s.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: key}).Code
	}
	id := strconv.FormatInt(u.ID, 10)

	k1 := s.makeKey(t, c, "one", "read", 0)
	k2 := s.makeKey(t, c, "two", "read", 0)
	rec := s.doJSON("POST", "/api/v1/me/password", c, `{"current":"password123","new":"brandnewpass"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"keys_revoked":2`) {
		t.Fatalf("own change: HTTP %d %s", rec.Code, rec.Body)
	}
	if works(k1.Key) != http.StatusUnauthorized || works(k2.Key) != http.StatusUnauthorized {
		t.Error("keys survived the owner's password change")
	}

	// The admin's reset (the session that changed it is gone; sign in again).
	tok, _, _ := s.auth.CreateSession(context.Background(), u.ID)
	c = &http.Cookie{Name: sessionCookieName, Value: tok}
	body := func(pw string) string {
		return `{"name":"x","scope":"read","expires_in_days":0,"password":"` + pw + `"}`
	}
	if rec := s.doJSON("POST", "/api/v1/me/api-keys", c, body("brandnewpass")); rec.Code != http.StatusCreated {
		t.Fatalf("make a key: HTTP %d %s", rec.Code, rec.Body)
	}
	var made madeKey
	_ = json.Unmarshal(s.doJSON("POST", "/api/v1/me/api-keys", c, body("brandnewpass")).Body.Bytes(), &made)
	if rec := s.doJSON("PUT", "/api/v1/users/"+id, admin, `{"password":"adminreset1"}`); rec.Code != http.StatusOK {
		t.Fatalf("admin reset: HTTP %d %s", rec.Code, rec.Body)
	}
	if works(made.Key) != http.StatusUnauthorized || countKeys(t, s, u.ID) != 0 {
		t.Error("keys survived the admin's password reset")
	}

	// Sign out everywhere.
	tok, _, _ = s.auth.CreateSession(context.Background(), u.ID)
	c = &http.Cookie{Name: sessionCookieName, Value: tok}
	_ = json.Unmarshal(s.doJSON("POST", "/api/v1/me/api-keys", c, body("adminreset1")).Body.Bytes(), &made)
	if works(made.Key) != http.StatusOK {
		t.Fatal("fresh key doesn't work")
	}
	rec = s.do("POST", "/api/v1/users/"+id+"/sessions/revoke", admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"keys_revoked":1`) {
		t.Fatalf("sign out everywhere: HTTP %d %s", rec.Code, rec.Body)
	}
	if works(made.Key) != http.StatusUnauthorized {
		t.Error("a key survived sign out everywhere")
	}
}

// Neither a key nor its hash turns up in the log or the support bundle, and a key some
// other tool wrote into a log line is blanked there.
func TestAPIKeysStayOutOfLogsAndBundle(t *testing.T) {
	f := newLogFixture(t, nil, nil)
	_, kc := f.user(t, "kid@example.com", auth.RoleRequester)
	m := f.makeKey(t, kc, "n8n", "full", 0)
	f.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: m.Key})
	f.withKey(keyReq{method: "GET", path: "/api/v1/me/api-keys", key: m.Key})                         // refused: logged as a 403
	f.withKey(keyReq{method: "GET", path: "/api/v1/auth/me", key: "arrk_" + strings.Repeat("q", 52)}) // a wrong key
	f.do("GET", "/api/v1/auth/me?apikey="+m.Key, nil)
	f.log.Warn("n8n: workflow failed calling with key " + m.Key)
	hash := sha(m.Key)

	for _, e := range f.ring.Snapshot(applog.Filter{Min: slog.LevelDebug}) {
		line := e.Message + " " + e.Attrs
		if strings.Contains(line, hash) {
			t.Errorf("log line has the key's hash: %s", line)
		}
		if strings.Contains(line, m.Key) && !strings.Contains(e.Message, "n8n: workflow failed") {
			t.Errorf("log line has the key: %s", line)
		}
	}

	rec := f.do("GET", "/api/v1/system/support-bundle", f.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("bundle: HTTP %d %s", rec.Code, rec.Body)
	}
	for name, body := range readZip(t, rec.Body.Bytes()) {
		if strings.Contains(body, m.Key) || strings.Contains(body, m.Key[5:]) || strings.Contains(body, hash) {
			t.Errorf("%s in the support bundle carries the key or its hash", name)
		}
	}
}

// A staff key reads history and a title's detail, the way a media server's scanner asks
// what changed; from outside the LAN too, as staff are. A requester's key still can't, and
// the rest of the staff library stays closed to keys.
func TestAPIKeyStaffLibraryReads(t *testing.T) {
	s := newRouteServer(t, nil)
	_, mc := s.user(t, "mgr@example.com", auth.RoleManager)
	_, kc := s.user(t, "kid@example.com", auth.RoleRequester)
	staff := s.makeKey(t, mc, "media server", "read", 0)
	kid := s.makeKey(t, kc, "kid", "full", 0)

	for _, path := range []string{"/api/v1/history", "/api/v1/movies/999", "/api/v1/series/999"} {
		for _, ip := range []string{"", "203.0.113.7"} {
			rec := s.withKey(keyReq{method: "GET", path: path, key: staff.Key, ip: ip})
			if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				t.Errorf("staff read-only key, GET %s from %q: HTTP %d %s", path, ip, rec.Code, rec.Body)
			}
		}
		if rec := s.withKey(keyReq{method: "GET", path: path, key: kid.Key}); rec.Code != http.StatusForbidden {
			t.Errorf("requester key, GET %s: HTTP %d, want 403", path, rec.Code)
		}
	}
	if rec := s.withKey(keyReq{method: "GET", path: "/api/v1/queue", key: staff.Key}); rec.Code != http.StatusForbidden {
		t.Errorf("staff key, GET /api/v1/queue: HTTP %d, want 403 (not opened to keys)", rec.Code)
	}
}
