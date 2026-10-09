package httpapi

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/routes.golden from the current route table")

// testRouter builds the real route table. With sentinel set, every handler is a stub
// that answers 299 once the scope check has let the caller through.
func testRouter(t *testing.T, base string, sentinel bool) *router {
	t.Helper()
	a := &api{deps: Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	a.deps.Config.BaseURL = base
	rt := newRouter(a, base)
	rt.sentinel = sentinel
	a.registerRoutes(rt)
	return rt
}

func (s routeSpec) String() string {
	method := s.Method
	if method == "" {
		method = "ANY"
	}
	ext := "-"
	if s.External {
		ext = "ext"
	}
	return fmt.Sprintf("%s %s %s %s", method, s.Pattern, s.Scope, ext)
}

func routeTable(specs []routeSpec) string {
	lines := make([]string, 0, len(specs))
	for _, s := range specs {
		lines = append(lines, s.String())
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

// Every route and its audience, in one reviewable file. A route that becomes reachable
// by requesters, or from outside the LAN, shows up as a diff here. Regenerate with
// go test ./internal/httpapi -run TestRouteScopesGolden -update.
func TestRouteScopesGolden(t *testing.T) {
	got := routeTable(testRouter(t, "", true).specs)
	golden := filepath.Join("testdata", "routes.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != got {
		t.Errorf("route table changed; review the diff and rerun with -update if it's intended.\n%s",
			lineDiff(strings.ReplaceAll(string(want), "\r\n", "\n"), got))
	}

	// The base path is stripped, so a reverse-proxy sub-path doesn't change the table.
	if based := routeTable(testRouter(t, "/arr", true).specs); based != got {
		t.Errorf("route table differs under a base path:\n%s", lineDiff(got, based))
	}
}

func lineDiff(want, got string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
			m[l] = true
		}
		return m
	}
	w, g := in(want), in(got)
	var out []string
	for l := range w {
		if !g[l] {
			out = append(out, "- "+l)
		}
	}
	for l := range g {
		if !w[l] {
			out = append(out, "+ "+l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][2:] < out[j][2:] })
	return strings.Join(out, "\n")
}

var paramRE = regexp.MustCompile(`\{[^}]+\}`)

// samplePath turns a pattern into a concrete path by filling every {param} with "1".
func samplePath(pattern string) string {
	return paramRE.ReplaceAllString(pattern, "1")
}

type walkAs struct {
	name string
	role auth.Role // "" = anonymous
}

var walkRoles = []walkAs{
	{"anonymous", ""},
	{"readonly", auth.RoleReadonly},
	{"requester", auth.RoleRequester},
	{"manager", auth.RoleManager},
	{"admin", auth.RoleAdmin},
}

func asRole(r *http.Request, role auth.Role) *http.Request {
	if role == "" {
		return r
	}
	return withUser(r, &auth.User{ID: 7, Username: "someone", Role: role})
}

// Every route, called as every kind of account: anonymous gets 401 from anything not
// public, an account below the route's scope gets 403, and everyone else reaches the
// handler the pattern names.
func TestRouteAuthz(t *testing.T) {
	rt := testRouter(t, "", true)
	for _, spec := range rt.specs {
		method := spec.Method
		if method == "" {
			method = http.MethodGet
		}
		path := samplePath(spec.Pattern)
		if spec.Method == "" {
			path = "/movies" // the SPA: any non-API path
		}
		for _, who := range walkRoles {
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, asRole(httptest.NewRequest(method, path, nil), who.role))

			want := sentinelStatus
			switch {
			case spec.Scope == scopePublic:
			case who.role == "":
				want = http.StatusUnauthorized
			case !who.role.AtLeast(spec.Scope.role()):
				want = http.StatusForbidden
			}
			if rec.Code != want {
				t.Errorf("%s as %s: HTTP %d, want %d", spec, who.name, rec.Code, want)
				continue
			}
			wantPattern := spec.Pattern
			if spec.Method != "" {
				wantPattern = spec.Method + " " + spec.Pattern
			}
			if rec.Code == sentinelStatus && rec.Header().Get("X-Route") != wantPattern {
				t.Errorf("%s %s reached %q, not its own route", method, path, rec.Header().Get("X-Route"))
			}
		}
	}

	// A disabled account gets nothing, whatever its role says.
	rec := httptest.NewRecorder()
	r := withUser(httptest.NewRequest("GET", "/api/v1/discover/trending", nil), &auth.User{ID: 1, Role: auth.RoleAdmin, Disabled: true})
	rt.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("disabled admin: HTTP %d, want 403", rec.Code)
	}
}

// Everything the requester UI (UserLayout: Discover incl. Books, Calendar, My Books,
// Audiobooks, the notification bell, the account menu, sign-in) calls, traced from
// web/src. Each must stay reachable for a requester — if one of these fails, a family
// account's page breaks.
var requesterUICalls = []string{
	"GET /api/v1/status",
	"GET /api/v1/auth/me",
	"POST /api/v1/auth/login",
	"POST /api/v1/auth/logout",
	"POST /api/v1/auth/setup",
	"POST /api/v1/auth/plex/pin",
	"GET /api/v1/auth/plex/pin/1",
	// Discover (movies + TV)
	"GET /api/v1/discover/trending",
	"GET /api/v1/discover/popular",
	"GET /api/v1/discover/upcoming",
	"GET /api/v1/discover/recommended",
	"GET /api/v1/discover/rows/top_rated",
	"GET /api/v1/discover/providers",
	"GET /api/v1/discover/provider",
	"GET /api/v1/discover/because",
	"GET /api/v1/discover/collections",
	"GET /api/v1/discover/genres",
	"GET /api/v1/discover",
	"GET /api/v1/discover/search",
	"GET /api/v1/media/movie/1",
	"GET /api/v1/requests",
	"POST /api/v1/requests",
	"DELETE /api/v1/requests/1",
	// Books Discover
	"GET /api/v1/books/discover/browse/trending",
	"GET /api/v1/books/discover/recommended",
	"GET /api/v1/books/discover/search",
	"GET /api/v1/books/discover/authors",
	"GET /api/v1/books/discover/authors/OL1A/works",
	"GET /api/v1/books/discover/subjects/romance",
	"GET /api/v1/books/discover/similar",
	"GET /api/v1/books/discover/detail",
	// Calendar, My Books
	"GET /api/v1/calendar",
	"GET /api/v1/me/books",
	"GET /api/v1/books/1/ebook",
	"GET /api/v1/books/1/audiobook",
	"GET /api/v1/books/1/cover-image",
	// Audiobooks (the "You" tab)
	"GET /api/v1/me/audio",
	"PUT /api/v1/me/audio/password",
	"DELETE /api/v1/me/audio/password",
	"GET /api/v1/me/audio/listening",
	"POST /api/v1/me/audio/accept",
	"DELETE /api/v1/me/audio/devices/lissen",
	"GET /api/v1/me/audio/history",
	"POST /api/v1/me/audio/restore",
	// Notification bell, account menu, push
	"GET /api/v1/me/notifications",
	"POST /api/v1/me/notifications/1/read",
	"POST /api/v1/me/notifications/read-all",
	"GET /api/v1/me/apprise",
	"PUT /api/v1/me/apprise",
	"GET /api/v1/me/push/key",
	"POST /api/v1/me/push/subscribe",
	"POST /api/v1/me/push/unsubscribe",
}

func TestRequesterUIStillWorks(t *testing.T) {
	rt := testRouter(t, "", true)
	for _, call := range requesterUICalls {
		method, path, _ := strings.Cut(call, " ")
		for _, role := range []auth.Role{auth.RoleRequester, auth.RoleReadonly} {
			// Read-only accounts browse the same pages but can't request.
			if role == auth.RoleReadonly && (call == "POST /api/v1/requests" || call == "DELETE /api/v1/requests/1") {
				continue
			}
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, asRole(httptest.NewRequest(method, path, nil), role))
			if rec.Code != sentinelStatus {
				t.Errorf("%s as %s: HTTP %d — the requester UI calls this", call, role, rec.Code)
			}
		}
	}
}

// The staff APIs a requester could reach on the LAN before routes were scoped.
func TestRequesterGetsForbiddenFromStaffAPIs(t *testing.T) {
	rt := testRouter(t, "", true)
	for _, path := range []string{
		"/api/v1/queue", "/api/v1/downloads", "/api/v1/history", "/api/v1/movies",
		"/api/v1/movies/1/releases", "/api/v1/series/1/releases", "/api/v1/books/1/releases",
		"/api/v1/indexers", "/api/v1/indexers/prowlarr", "/api/v1/downloadclients",
		"/api/v1/settings", "/api/v1/convert/logs", "/api/v1/subtitles/logs",
		"/api/v1/library/fit", "/api/v1/books/1", "/api/v1/movies/lookup", "/api/v1/books/authors/images",
		"/api/v1/health/system", "/api/v1/movies/1/manualimport",
	} {
		for _, role := range []auth.Role{auth.RoleRequester, auth.RoleReadonly} {
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, asRole(httptest.NewRequest("GET", path, nil), role))
			if rec.Code != http.StatusForbidden {
				t.Errorf("GET %s as %s: HTTP %d, want 403", path, role, rec.Code)
			}
		}
	}
}

// Admin means admin: a manager runs the media day to day, but the owner's API keys,
// moving library folders, walking the host's folders, purging the recycle bin and the
// log are the admin's alone.
func TestManagerGetsForbiddenFromAdminAPIs(t *testing.T) {
	rt := testRouter(t, "", true)
	for _, call := range []string{
		"GET /api/v1/apikeys", "PUT /api/v1/apikeys/tmdb", "POST /api/v1/apikeys/tmdb/test",
		"PUT /api/v1/system/library", "GET /api/v1/system/browse",
		"POST /api/v1/recycle/empty", "POST /api/v1/recycle/delete", "GET /api/v1/logs",
	} {
		method, path, _ := strings.Cut(call, " ")
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, asRole(httptest.NewRequest(method, path, nil), auth.RoleManager))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s as manager: HTTP %d, want 403", call, rec.Code)
		}
		rec = httptest.NewRecorder()
		rt.ServeHTTP(rec, asRole(httptest.NewRequest(method, path, nil), auth.RoleAdmin))
		if rec.Code != sentinelStatus {
			t.Errorf("%s as admin: HTTP %d, want it let through", call, rec.Code)
		}
	}
	// What a manager still needs from the same areas.
	for _, call := range []string{
		"GET /api/v1/settings", "PUT /api/v1/settings", "GET /api/v1/system/library",
		"GET /api/v1/recycle", "GET /api/v1/recycle/items", "POST /api/v1/recycle/restore",
	} {
		method, path, _ := strings.Cut(call, " ")
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, asRole(httptest.NewRequest(method, path, nil), auth.RoleManager))
		if rec.Code != sentinelStatus {
			t.Errorf("%s as manager: HTTP %d, want it let through", call, rec.Code)
		}
	}
}

// The live multi-indexer search had no caller left and handed out raw download URLs
// (with indexer API keys in them). It's gone, and an unknown API path is a JSON 404
// rather than the web app's index page.
func TestUnknownAPIPathIsNotFound(t *testing.T) {
	rt := testRouter(t, "", false)
	for _, path := range []string{"/api/v1/search?q=x", "/api/v1/nope", "/api/nope"} {
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, asRole(httptest.NewRequest("GET", path, nil), auth.RoleAdmin))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Header().Get("Content-Type"), "json") {
			t.Errorf("GET %s: HTTP %d (%s), want a JSON 404", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	// Client-side routes still get the app.
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest("GET", "/movies/12", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /movies/12: HTTP %d, want the SPA", rec.Code)
	}
}

// Until externalGate reads the route table itself, the ext column must agree with its
// prefix list for every route, or off-LAN behaviour would silently differ from what
// the golden file says.
func TestExternalParity(t *testing.T) {
	for _, spec := range testRouter(t, "", true).specs {
		path := samplePath(spec.Pattern)
		if got := externalAllowed(path); got != spec.External {
			t.Errorf("%s: the external gate says reachable=%v", spec, got)
		}
	}
}

// Routes are registered only through the scoped router: no bare ServeMux, and the old
// any-signed-in helper is gone.
func TestNoRawRouteRegistration(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "router.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, bad := range []string{"http.NewServeMux(", "a.protected(", "rt.mux.", ".mux.Handle"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s contains %q: register routes through the router in server.go", f, bad)
			}
		}
	}
}
