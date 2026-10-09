package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
)

// callFromOutside sends a request from an internet address through the external gate
// and the real route table (sentinel handlers), as user (nil = not signed in).
func callFromOutside(rt *router, method, path string, user *auth.User) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://x"+path, nil)
	r.RemoteAddr = "203.0.113.50:5000" // internet visitor
	if user != nil {
		r = withUser(r, user)
	}
	rec := httptest.NewRecorder()
	rt.a.externalGate(rt).ServeHTTP(rec, r)
	return rec
}

// The Discover-only scope is for requesters, read-only accounts and strangers. A
// signed-in admin or manager gets the whole app from wherever they are — the owner
// opening the app through their own tunnel hostname was getting the requester's view.
func TestExternalGateExemptsStaff(t *testing.T) {
	rt := testRouter(t, "", true)
	call := func(user *auth.User) int { return callFromOutside(rt, "GET", "/api/v1/series", user).Code }
	if code := call(nil); code != http.StatusForbidden {
		t.Errorf("anonymous internet visitor reached a LAN-only endpoint (HTTP %d)", code)
	}
	if code := call(&auth.User{Role: auth.RoleRequester}); code != http.StatusForbidden {
		t.Errorf("requester from the internet reached a LAN-only endpoint (HTTP %d)", code)
	}
	if code := call(&auth.User{Role: auth.RoleAdmin}); code != sentinelStatus {
		t.Errorf("admin from the internet: HTTP %d; want the full app", code)
	}
	if code := call(&auth.User{Role: auth.RoleManager}); code != sentinelStatus {
		t.Errorf("manager from the internet: HTTP %d; want the full app", code)
	}
	if code := call(&auth.User{Role: auth.RoleAdmin, Disabled: true}); code != http.StatusForbidden {
		t.Errorf("a disabled admin account still bypassed the gate (HTTP %d)", code)
	}
	// Discover stays reachable for everyone outside (the scope check then asks for a
	// sign-in).
	if code := callFromOutside(rt, "GET", "/api/v1/discover/trending", nil).Code; code != http.StatusUnauthorized {
		t.Errorf("Discover from outside, not signed in: HTTP %d, want 401", code)
	}

	// The staff exemption is visible to handlers too: /status tells the UI which shell to draw.
	var seen bool
	gate := rt.a.externalGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = isExternalRequest(r) }))
	r := httptest.NewRequest("GET", "http://x/api/v1/status", nil)
	r.RemoteAddr = "203.0.113.50:5000"
	gate.ServeHTTP(httptest.NewRecorder(), withUser(r, &auth.User{Role: auth.RoleAdmin}))
	if seen {
		t.Error("an admin from outside was stamped external")
	}
	gate.ServeHTTP(httptest.NewRecorder(), withUser(r, &auth.User{Role: auth.RoleRequester}))
	if !seen {
		t.Error("a requester from outside wasn't stamped external")
	}
}

// What a requester can reach from outside is the route table's ext column, nothing
// else: the gate keeps no path list of its own.
func TestExternalGateUsesRouteSpecs(t *testing.T) {
	rt := testRouter(t, "", true)
	requester := &auth.User{ID: 7, Role: auth.RoleRequester}
	for call, want := range map[string]int{
		"GET /api/v1/discover/trending":          sentinelStatus,
		"GET /api/v1/books/12/ebook":             sentinelStatus,
		"GET /api/v1/books/12/audiobook":         sentinelStatus,
		"GET /api/v1/books/12/cover-image":       sentinelStatus,
		"GET /api/v1/books/discover/trending":    sentinelStatus,
		"GET /api/v1/me/books":                   sentinelStatus,
		"GET /api/v1/requests":                   sentinelStatus,
		"POST /api/v1/requests":                  sentinelStatus,
		"GET /api/v1/auth/me":                    sentinelStatus,
		"GET /api/v1/status":                     sentinelStatus,
		"GET /api/v1/books/12":                   http.StatusForbidden,
		"GET /api/v1/books/12/edition-files":     http.StatusForbidden,
		"GET /api/v1/calendar":                   http.StatusForbidden,
		"GET /api/v1/ws":                         http.StatusForbidden,
		"GET /api/v1/queue":                      http.StatusForbidden,
		"POST /api/v1/requests/12/approve":       http.StatusForbidden, // staff route: the role check refuses
		"GET /movies/12":                         sentinelStatus,       // the app shell itself
		"GET /api/v1/discover/rows/top_rated":    sentinelStatus,
		"GET /api/v1/media/movie/12":             sentinelStatus,
		"DELETE /api/v1/me/audio/devices/lissen": sentinelStatus,
	} {
		method, path, _ := strings.Cut(call, " ")
		if got := callFromOutside(rt, method, path, requester).Code; got != want {
			t.Errorf("%s as an off-LAN requester: HTTP %d, want %d", call, got, want)
		}
	}

	// A path no route claims is the API's JSON 404 from outside too, not a 403 and not
	// the app's index page.
	live := testRouter(t, "", false)
	for _, path := range []string{"/api/v1/books/12/ebook/extra", "/api/v1/nope"} {
		if got := callFromOutside(live, "GET", path, requester).Code; got != http.StatusNotFound {
			t.Errorf("GET %s from outside: HTTP %d, want 404", path, got)
		}
	}

	// Every route in the table behaves as its ext column says.
	for _, spec := range rt.specs {
		if spec.Method == "" {
			continue
		}
		rec := callFromOutside(rt, spec.Method, samplePath(spec.Pattern), &auth.User{ID: 7, Role: auth.RoleAdmin})
		if rec.Code != sentinelStatus {
			t.Errorf("%s as an off-LAN admin: HTTP %d, want the route", spec, rec.Code)
		}
		rec = callFromOutside(rt, spec.Method, samplePath(spec.Pattern), &auth.User{ID: 7, Role: auth.RoleReadonly})
		blocked := rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "outside your network")
		if blocked == spec.External {
			t.Errorf("%s as an off-LAN read-only account: HTTP %d %s", spec, rec.Code, rec.Body.String())
		}
	}
}
