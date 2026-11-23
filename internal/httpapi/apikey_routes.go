package httpapi

// apiKeyRoutes are the only routes a personal API key (Me → Personal API keys) may call.
// Every other route answers a key with 403, including any route added later until it is
// listed here: keys are deny-by-default like everything else in the route table, and
// testdata/routes.golden shows them as "key".
//
// The rule for what belongs here (docs/api.md repeats it for people writing scripts):
//
//   - only routes already marked ext() — the surface built to face callers from outside
//     the home network — so a key never reaches anything its owner away from home
//     couldn't (TestAPIKeyRoutesTable). Staff routes marked ext() change nothing for
//     sessions: the outside limit never applies to staff, and a requester still gets 403
//     from the role check;
//   - a staff route here is a read (GET), unless it is approving or declining requests
//     (TestAPIKeyStaffRoutesOnlyRead): the deferred Admin key level is what would open
//     the rest of the staff library;
//   - and of those, only what a script has a use for: Discover and title lookups,
//     requests (and, for staff, approving and declining them), reporting problems, your
//     inbox, your shelf and quota, who you are, and for staff, reading history and a
//     movie's or show's detail.
//
// Never here, whatever their marks (TestAPIKeyRoutesExcludeAccountSecurity):
//   - signing in and out, sessions, passwords and two-factor sign-in;
//   - managing API keys themselves (a key can't make or revoke keys);
//   - linking Plex or Jellyfin, push devices, Apprise and the other account settings;
//   - the audiobook player and its password (the listening apps have their own sign-in);
//   - the support bundle, logs, outside services' API keys and every other admin route.
//
// A key still acts as its owner, capped by the role it was made with and by its scope
// (read-only keys get GET only); what each route shows a requester is unchanged.
var apiKeyRoutes = map[string]bool{
	// Is Arrmada up, and who am I?
	"GET /api/health":     true,
	"GET /api/v1/status":  true,
	"GET /api/v1/auth/me": true,

	// Discover: movies and TV.
	"GET /api/v1/discover":                  true,
	"GET /api/v1/discover/because":          true,
	"GET /api/v1/discover/browse":           true,
	"GET /api/v1/discover/collection/{id}":  true,
	"GET /api/v1/discover/collections":      true,
	"GET /api/v1/discover/genres":           true,
	"GET /api/v1/discover/person/{id}":      true,
	"GET /api/v1/discover/popular":          true,
	"GET /api/v1/discover/provider":         true,
	"GET /api/v1/discover/providers":        true,
	"GET /api/v1/discover/recently-added":   true,
	"GET /api/v1/discover/recommended":      true,
	"GET /api/v1/discover/rows/{kind}":      true,
	"GET /api/v1/discover/search":           true,
	"GET /api/v1/discover/trending":         true,
	"GET /api/v1/discover/upcoming":         true,
	"GET /api/v1/media/{media}/{id}":        true,
	"GET /api/v1/media/series/{id}/seasons": true,
	"GET /api/v1/media/links":               true,
	"GET /api/v1/plex/link":                 true,

	// Discover: books.
	"GET /api/v1/books/discover/authors":             true,
	"GET /api/v1/books/discover/authors/{key}":       true,
	"GET /api/v1/books/discover/authors/{key}/works": true,
	"GET /api/v1/books/discover/browse/{kind}":       true,
	"GET /api/v1/books/discover/detail":              true,
	"GET /api/v1/books/discover/recommended":         true,
	"GET /api/v1/books/discover/search":              true,
	"GET /api/v1/books/discover/similar":             true,
	"GET /api/v1/books/discover/subjects/{name}":     true,
	"GET /api/v1/books/discover/trending":            true,
	"GET /api/v1/books/{id}/cover-image":             true,
	"GET /api/v1/books/{id}/ebook":                   true,
	"GET /api/v1/me/books":                           true,

	// Requests: asking, following, withdrawing; staff approve and decline.
	"GET /api/v1/requests":                      true,
	"POST /api/v1/requests":                     true,
	"GET /api/v1/requests/{id}":                 true,
	"DELETE /api/v1/requests/{id}":              true,
	"DELETE /api/v1/requests/{id}/subscription": true,
	"POST /api/v1/requests/{id}/approve":        true,
	"POST /api/v1/requests/{id}/decline":        true,
	"POST /api/v1/requests/{id}/preview":        true,
	"POST /api/v1/requests/bulk":                true,
	"GET /api/v1/me/quota":                      true,

	// Reporting a problem with something in the library.
	"GET /api/v1/issues":                 true,
	"POST /api/v1/issues":                true,
	"GET /api/v1/issues/episodes/{tmdb}": true,

	// Reading the library's history and a title's files, for staff keys: what a media
	// server's scanner needs to rescan only what changed. Read-only; staff scope still
	// applies, so a requester's or read-only account's key gets 403. ext() on these changes
	// nothing for staff, whom the external gate never limits (external.go).
	"GET /api/v1/history":     true,
	"GET /api/v1/movies/{id}": true,
	"GET /api/v1/series/{id}": true,

	// Your inbox.
	"GET /api/v1/me/notifications":            true,
	"POST /api/v1/me/notifications/read-all":  true,
	"POST /api/v1/me/notifications/{id}/read": true,
}
