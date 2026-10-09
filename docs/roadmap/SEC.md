# SEC — Access control, security & privacy

_Part of the [Arrmada roadmap](../../ROADMAP.md). 15 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Make the server, not the UI, decide what each account can reach and see. Requester and read-only accounts get exactly the Discover / requests / own-account / own-books surface, on the LAN as well as off it. Secrets (indexer API keys, tracker tokens, webhook secrets) never reach a browser. File access stays inside the library and download roots, never /data. Nothing records which audiobook anyone plays. Sign-in throttling can't be dodged or turned into an admin lockout, and sessions last as long as they're used and can be ended.

**Why.** Arrmada's family/friend accounts are auto-created by Plex sign-in. The UI shows them only Discover, but the API enforces that only off the LAN. 124 routes use `a.protected` (signed in, any role), and 77 of them are staff functionality (backend-2). On the home network a requester or read-only account can do the following:
- run 90-second live searches across the owner's private trackers, and read `download_url` values that carry the Prowlarr `apikey=` or the owner's MAM dl token (integrations-7, movies-3, books-10)
- make the server walk and list any directory on the host, the whole array included, through `?path=` on the three manual-import routes (backend-1, books-10, movies-3)
- read the transfer list, file paths and import history (ops-13)
- watch who in the household is streaming what, live over the websocket, which gets around the manager-only Insights gate (backend-4, ops-13)

Two standing rules are broken:
- **Audiobook privacy.** The audiobook server's request log records which book was opened, played or bookmarked, plus search text, and managers can read it on the Logs page (audiobooks-1).
- **Adult filter.** Books Discover skips the always-on adult filter entirely (books-14).
- **Never at /data.** Nothing stops a library folder being picked under /data (system-10, /data slice).

Smaller problems:
- Login throttling trusts a spoofable X-Forwarded-For and counts successful logins, so anyone on the internet can keep the owner's username locked out (backend-7).
- Managers can change API keys, module toggles and Plex sign-in, and can read full Apprise URLs with webhook secrets (system-6, insights-7).
- Requester-owned Apprise URLs can point the server at internal hosts (backend-3).
- Sessions hard-expire every 30 days, and expiry leaves the app in a broken state (backend-13).
- No test pins any route's authorization, so every new route is a fresh chance to regress (backend-16).

Exposure is mostly LAN-only family members, which is why most of these are rated medium. But the owner's stance on privacy and on credentials makes them real.

**Depends on:** AUD: the planned web-player routes under /api/v1/me/audio/* must be registered through [SEC-02](#sec-02)'s router (rt.user) and must not log item ids or titles. [SEC-04](#sec-04)'s route-pattern logging covers request lines automatically, but any new log call needs the same discipline.; REQ: live request progress over the websocket must be published as user.<id>.request.* topics. Under [SEC-03](#sec-03)'s policy every other topic is staff-only, so REQ depends on [SEC-03](#sec-03).; ACQ: [SEC-07](#sec-07)'s release tokens touch every grab and blocklist entry point. Sequence them with ACQ's acquisition-core rework of the grab path, or carry the tokens into it (ACQ may later back tokens with candidate rows).; INT/PLEX: [SEC-08](#sec-08)'s masked-URL UI goes in whichever page hosts notification connections (Insights today, the Alerts page after the insights.t8 move). [SEC-11](#sec-11)'s security.login_failures event can join INT's alert catalog.; CFG: the library-folder per-folder checks (rest of system-10) should reuse internal/pathguard and [SEC-06](#sec-06)'s a.dataDirConflict rather than re-implement the /data rule.; BOOK: the Books Discover rework must keep a single card funnel (today enrichBookCards) so [SEC-05](#sec-05)'s adult filter covers every surface.; OBS: [SEC-04](#sec-04) adds applog.RouteLabel/RedactPath/RewriteFiles and [SEC-09](#sec-09) makes /logs admin-only. OBS's Logs-page work should build on both.; FE: [SEC-14](#sec-14) adds global 401 handling in req(). If FE's api-client refactor lands first, the hook goes there.; APP: an Account page (password change etc., rest of system-6) would host [SEC-15](#sec-15)'s device list.; SAFE: [SEC-15](#sec-15) adds a migration. Prefer SAFE's pre-migration DB snapshot to be in place first.

#### Design

## Target design

### 1. One route table, deny by default ([SEC-02](#sec-02), [SEC-09](#sec-09), [SEC-10](#sec-10))
`internal/httpapi/router.go` replaces direct `mux.HandleFunc` / `a.protected` / `a.requireRole` calls.

```go
type scope int
const (
    scopePublic    scope = iota // no session (health, status, auth/login|setup|logout|plex pin, SPA)
    scopeUser                   // any signed-in account, readonly included
    scopeRequester              // requester and up (create/withdraw requests)
    scopeStaff                  // manager and admin
    scopeAdmin
)
type routeSpec struct { Method, Pattern string; Scope scope; External bool }
type router struct {
    a *api; mux *http.ServeMux; base string
    specs []routeSpec
    sentinel bool // tests: swap every handler for a 299 stub after the scope check
}
func (rt *router) public|user|requester|staff|admin(pattern string, h http.HandlerFunc, opts ...routeOpt)
func ext() routeOpt // reachable from outside the LAN for non-staff
```

Registration is split by audience:
- `routes_public.go`
- `routes_user.go`: **the** requester allowlist, one file to review
- `routes_staff.go`
- `routes_admin.go`

`New()` builds `rt := newRouter(a, mux, base)`, calls `a.registerRoutes(rt)`, then adds the SPA as `rt.public("/", ui, ext())`.

The tests are the invariant:
- `testdata/routes.golden` lists `METHOD pattern scope ext` and is regenerated with `-update`. A new requester or external route shows up as a visible diff.
- A route walk runs as anonymous, readonly, requester, manager and admin.
- A source check: no `mux.HandleFunc(` or `a.protected(` outside router.go.
- External-parity tests.

**Role matrix:**

| Scope | Who | What |
|---|---|---|
| public | anyone | health, status, auth (login/setup/logout/plex pin), SPA |
| user | readonly+ | `GET /auth/me`; all 17 `/me/*`; 12 `/discover*`; 10 `/books/discover/*`; `GET /media/{media}/{id}`; `GET /calendar`; `GET /requests`; `GET /books/{id}/ebook`, `/audiobook`, `/cover-image` (handlers check `mayDownloadBook`); `GET /ws` (topic-filtered) |
| requester | requester+ | `POST /requests`, `DELETE /requests/{id}` |
| staff | manager+ | everything else: today's 77 staff `a.protected` reads plus every existing manager route |
| admin | admin | users, setup, restart, audiobook server admin, imports; plus ([SEC-09](#sec-09)) API keys, system/library writes, folder browse, recycle empty/delete, logs, admin notification connection writes |

That allowlist is the full set of API calls reachable from Discover.tsx, BooksDiscover.tsx, Calendar.tsx, MyBooks.tsx, Audiobooks.tsx (non-admin branch), UserLayout.tsx, NotificationBell.tsx and lib/me.tsx, traced through their imports. `/search` is deleted: nothing calls it. Requests.tsx is not routed in App.tsx and is dead code.

**Off-LAN ([SEC-10](#sec-10)).** `externalGate` only classifies and stamps the request. The scope wrapper denies a non-staff external request when `!spec.External`. The prefix lists in external.go go away.

### 2. Realtime: per-viewer topic policy ([SEC-03](#sec-03))
- `realtime.Connect(Viewer{UserID int64; Staff bool})`.
- `policy.allowed(topic, v)`:
  - staff get everything;
  - non-staff get `server.heartbeat` and `user.<theirID>.*`;
  - everything else is staff-only by default (plex.*, import.*, release.*, movie.*, series.*, book.*, music.*, file.*, download.*, library.*, security.*).
- The hub marshals each event once and filters per client.
- REQ publishes per-user request progress as `user.<id>.request.updated`.

### 3. Filesystem confinement ([SEC-01](#sec-01), [SEC-06](#sec-06), [SEC-12](#sec-12))
New `internal/pathguard`:
- `Resolve(p)`: Clean → Abs → EvalSymlinks. For a missing tail, it evaluates the deepest existing parent and rejoins.
- `Within(p, roots...)`: compares on a separator boundary, against resolved roots.
- `Under(p, dir)`.

In httpapi:
- `importRoots()` = Downloads, Library, Movies, TV, Ebooks, Audiobooks (cfg, already settings-applied at boot by ApplySavedLibraryDirs) plus the settings-resolved music dir.
- `checkImportPath` rejects anything outside those roots and anything under `Config.DataDir`.
- `underLibraryRoot` (handleFileInfo) is reimplemented on pathguard.
- Library folder saves refuse any path under DataDir, and any path that contains DataDir.

Manual-import walks take a context, a 500-result cap and a ~100k-entry visit cap, and report `truncated`.

### 4. Secrets never reach a browser ([SEC-07](#sec-07), [SEC-08](#sec-08))
**Release tokens.**
- `RankedRelease.DownloadURL` becomes `json:"-"`, and every interactive-search response carries an opaque `token` instead.
- The in-memory `releaseTokens` store: 128-bit crypto/rand base64url token, about 2h TTL, about 20k entries with oldest-first eviction. Each token maps to {indexer, download URL, title, media kind/id, version id, issuing user}.
- Grab and blocklist endpoints take `{token}`, resolve it on the server, check its scope, and return 410 "This search result has expired — search again" when it's unknown or expired.
- No endpoint accepts a raw `download_url` any more. The .torrent upload path carries the file itself.

**Apprise admin connections.**
- Validated on save.
- Returned only as `url_hint` (e.g. `gotify://push.example.com/••••` or `discord://••••1f3a`) plus `url_set`. Omitting `url` on update keeps the stored one.
- Test-by-id uses the stored URL.

### 5. Privacy in logs ([SEC-04](#sec-04))
Rules:
- Request log lines carry the **route pattern** (`r.Pattern`, e.g. `POST /api/items/{id}/play`) and **query keys only**. For unmatched paths, `applog.RedactPath` swaps id-looking segments for `{id}`.
- No log line pairs a user with an item.

This applies to the audiobook server and the main API's `logRequests`, so it also covers AUD's future `/api/v1/me/audio/*` web-player routes. A one-time, marker-guarded scrub rewrites already-persisted log files. The Logs page becomes admin-only ([SEC-09](#sec-09)).

### 6. Identity edges ([SEC-11](#sec-11), [SEC-14](#sec-14), [SEC-15](#sec-15))
**Client IP.** `internal/netutil.ClientIP` believes forwarded headers only from a loopback or private peer. Order: Cf-Connecting-Ip, then the right-most non-private X-Forwarded-For hop, then the peer. httpapi, external.go and audioserver all share it.

**Login throttling.**
- Failures only, reset on success.
- The per-IP limit stays.
- The per-username dimension uses an exponential delay and applies to off-LAN sources only.
- `security.login_failures` is published at 20 or more failures an hour.

**Sessions.**
- They slide: any use past half-life extends them to now+TTL and re-sets the cookie.
- The UI treats any 401 outside `/auth/*` as signed out and shows the login page with a note.
- Users can list their own sessions (device, last seen) and revoke one or all others. Admins can sign a user out everywhere.

### 7. Content safety ([SEC-05](#sec-05))
`adultfilter.BookIsAdult(title, tags)` is tag-based: erotica/erotic/BDSM/porn on word boundaries, never "Adult Fiction", "New Adult" or "Young Adult", plus the existing title matcher. It is applied once in the Books Discover funnel (`enrichBookCards`) and on the discover detail. The manager-only `/books/lookup` stays unfiltered.

### Ground rules for every task
- `go vet` plus race tests in Docker before pushing. Commits end with the Co-Authored-By trailer.
- UI stays in the current dark warm palette, terracotta accent and type scale.
- No credentials in code or chat.
- No test-converting real library files.
- Any change to requester-reachable routes includes a requester click-through on the LAN.

#### Milestone: M1: Requesters reach only what their UI shows, on the LAN too

_Requester and read-only accounts get 403 from every staff API (searches, releases, queue, downloads, history, library, settings, logs and the rest). They can't list server folders or receive anyone's Plex activity over the websocket. A route-walk test and a golden route table stop regressions. Discover, Calendar, My Books, Audiobooks and the notification bell still work for them._

<a id="sec-01"></a>
- [x] **SEC-01 · Manual-import listing: staff only, confined to library/download roots, never /data** — `P0` · `S` · Phase 0
  - **Problem:** GET /api/v1/{movies,series,books}/{id}/manualimport are registered with a.protected (server.go:270, 388, 464), so any signed-in role can call them, readonly and requester included. ?path= goes straight to a recursive walk:
- movies.Service.ManualImportCandidates (filepath.WalkDir, movies/service.go:1146)
- library.FindVideos (via Coordinator.SeriesImportCandidates)
- library.FindBookFiles (via Coordinator.BookImportCandidates)

The walk returns the full path of every large video, ebook or audio file anywhere on the host, and ?path=/ walks every array disk. The POST manualimport bodies (handleManualImport, handleSeriesManualImport, handleBookManualImport) are manager-only, but they accept any source path too. handleFileInfo has a root check (underLibraryRoot, fileinfo.go:113), but it compares lexically and doesn't resolve symlinks.
  - **Approach:** 1. server.go: wrap the three GET manualimport routes in a.requireRole(auth.RoleManager, …). [SEC-02](#sec-02) later moves them into routes_staff.go.
    2. New package internal/pathguard (pure, no deps):
       - Resolve(p string) (string, error): filepath.Clean, then Abs, then EvalSymlinks. When the target doesn't exist, resolve the deepest existing ancestor and rejoin the remaining elements, so a missing tail can't hide a symlink hop.
       - Within(p string, roots ...string) bool: resolves the roots too, skips empty roots, and requires p == root or a root+Separator prefix.
       - Under(p, dir string) bool.
    3. New internal/httpapi/importroots.go:
       - (a *api) importRoots(r) []string returns cfg.DownloadsDir, LibraryDir, MoviesDir, TVDir, EbooksDir and AudiobooksDir (already settings-applied at boot by ApplySavedLibraryDirs), plus a.libMusic(r), because music is read lazily from settings.
       - (a *api) checkImportPath(r, p string) (string, error): an empty p becomes cfg.DownloadsDir. Resolve it. If pathguard.Under(resolved, cfg.DataDir), return "That's Arrmada's own data folder — pick a folder inside your downloads or library folders". If it isn't Within(importRoots), return "Pick a folder inside your downloads or library folders (Settings → Library)". Return the resolved path.
    4. Call checkImportPath first, before any os.Stat or walk, in the three GET list handlers and the three POST handlers. Answer 400 with the message. The series folder-import goroutine (series.go:612) uses the resolved path. The responses' "path" field echoes the resolved path.
    5. Reimplement underLibraryRoot on pathguard.Within (same roots plus music), so handleFileInfo gets symlink safety too. Keep the function name.
    6. UI: no change needed. All three ManualImportModal components (MovieDetail.tsx:824, SeriesDetail.tsx:523, BookDetail.tsx:360) already show err.message, and none passes ?path= today. Confirm the message reads well in each.
  - **Files:** `internal/pathguard/pathguard.go (new)`, `internal/pathguard/pathguard_test.go (new)`, `internal/httpapi/importroots.go (new)`, `internal/httpapi/importroots_test.go (new)`, `internal/httpapi/server.go`, `internal/httpapi/movies.go`, `internal/httpapi/series.go`, `internal/httpapi/books.go`, `internal/httpapi/fileinfo.go`
  - **Acceptance:**
    - A requester or read-only session gets 403 from GET /api/v1/movies/1/manualimport, /series/1/manualimport and /books/1/manualimport.
    - As a manager, ?path=/, ?path=/data, ?path=<downloads>/../etc and a symlink inside downloads that points at /etc all return 400 with the 'pick a folder inside…' message.
    - As a manager, no ?path= (the downloads default) and ?path=<downloads>/sub still list candidates.
    - POST manualimport for a movie, series or book with a source outside the roots returns 400 and imports nothing.
    - handleFileInfo still describes library files and refuses a symlink that escapes a root.
  - **Tests:** Go pathguard.TestWithin table: inside root, the root itself, a '/library-old' vs '/library' boundary, a '..' escape, a symlink escape (t.TempDir with os.Symlink, skipped on Windows when symlinks aren't permitted), a missing tail under a symlinked parent, an empty root.; Go httpapi.TestCheckImportPath: temp downloads/library dirs as roots, DataDir set to a temp dir. Expect inside → ok, DataDir child → error, outside → error, empty → downloads.; Go httpapi.TestManualImportListRequiresManager: route-level test with withUser for requester, readonly and manager against the three GET routes (403/403/not-403).; Existing TestFileInfoRefusesPathsOutsideTheLibrary still passes.
  - **Risk:** A manager who imports from an ad-hoc mount outside the configured roots loses that ability. The error message names the fix (add the folder in Settings → Library). EvalSymlinks on Unraid /mnt/user shares resolves fine. Note that this is not a download-client path mapping: Arrmada and qBittorrent share DownloadsDir, so no client save-path roots are needed.
  - **Resolves:** backend-1, books-10, movies-3
<a id="sec-02"></a>
- [x] **SEC-02 · Deny-by-default route scopes with one requester allowlist, a golden route table and a route-walk test** — `P0` · `M` · Phase 0
  - **Problem:** server.go registers 124 routes with a.protected (auth.go:57-65), which accepts any signed-in role. 77 of those are staff functionality. Examples:
- /search and the /movies, /series and /books/{id}/releases routes: live multi-indexer searches whose JSON carries download_url with the Prowlarr apikey or the MAM dl token.
- /queue (content_path), /downloads and /history.
- /indexers and /indexers/prowlarr (tracker URLs, the TorrentLeech username).
- /downloadclients plus their status and settings.
- /movies*, /series*, the non-discover /books/*, /music*, /settings, /library/fit*, /quality/*, /parse, /convert/* GETs and /subtitles/* GETs.

The UI shows non-staff only Discover (App.tsx:64-77). The server enforces that only off-LAN, through a separately maintained prefix list (external.go:89-103). No test checks per-route authorization, so every new route is a chance to regress (backend-16).
  - **Approach:** 1. New internal/httpapi/router.go:
       - type scope (scopePublic, scopeUser = any signed-in incl. readonly, scopeRequester, scopeStaff = manager+, scopeAdmin);
       - type routeSpec {Method, Pattern string; Scope scope; External bool};
       - type router {a *api; mux *http.ServeMux; base string; specs []routeSpec; sentinel bool};
       - methods rt.public/user/requester/staff/admin(pattern string, h http.HandlerFunc, opts ...routeOpt), plus an ext() option.
       - handle() builds the guard: 401 'authentication required' when no user and the scope isn't public; 403 'insufficient permissions' when the role is below the scope; maps scopeStaff to RoleManager. It records the spec, swaps h for a 299 stub when rt.sentinel is set, and calls mux.HandleFunc. This is the only mux.HandleFunc in the package.
    2. Move every registration out of New() into a.registerRoutes(rt), split by audience:
       - routes_public.go: /api/health, /api/v1/status, auth setup/login/logout, plex pin start and poll.
       - routes_user.go: the explicit allowlist below.
       - routes_staff.go and routes_admin.go: today's requireRole(Manager) and requireRole(Admin) routes keep their level.
       - Every one of the 77 remaining a.protected routes becomes rt.staff.
       - Delete a.protected's call sites. Keep requireRole only if a handler still needs it inline (none should).
    3. The allowlist in routes_user.go. Every one is rt.user unless noted:
       - GET /auth/me.
       - All 17 /me/* routes: audio (GET), audio/password (PUT, DELETE), audio/listening, audio/accept, audio/devices/{family} (DELETE), audio/history, audio/restore, notifications, notifications/read-all, notifications/{id}/read, push/key, push/subscribe, push/unsubscribe, apprise (GET, PUT), books.
       - The 12 /discover* routes: trending, popular, upcoming, recommended, search, genres, rows/{kind}, providers, provider, because, collections, and the bare /discover.
       - The 10 /books/discover/* routes: trending, browse/{kind}, recommended, search, authors, authors/{key}, authors/{key}/works, similar, subjects/{name}, detail.
       - GET /media/{media}/{id}, GET /calendar, GET /requests.
       - POST /requests and DELETE /requests/{id}: rt.requester.
       - GET /books/{id}/ebook, /books/{id}/audiobook and /books/{id}/cover-image. The handlers already check mayDownloadBook.
       - GET /ws: topic-filtered by [SEC-03](#sec-03).
       - /books/authors/images is staff (only AuthorDetail.tsx uses it). The lookups (/movies/lookup, /series/lookup, /books/lookup, /music/lookup) are staff. Requests.tsx, their only other caller, isn't routed.
    4. Record External on exactly today's externally reachable routes: public auth/status/health, /me/*, /discover*, /books/discover/*, /media/*, every /requests route, /books/{id}/ebook and /books/{id}/audiobook, and the SPA. externalGate keeps using the prefix list in this task; [SEC-10](#sec-10) switches it over. Add a parity test so the two can't disagree in the meantime.
    5. Delete GET /api/v1/search: handleSearch (indexers.go:205-224), api.search (api.ts:1105) and the unused TS SearchResult/Release types if nothing else imports them.
    6. Tests:
       - testdata/routes.golden with 'METHOD pattern scope ext' per spec, sorted, regenerated with -update.
       - The route walk.
       - A source check that server.go and routes_*.go contain no 'mux.HandleFunc(' or 'a.protected('.
    7. Before merging, re-run the requester reach check: every api.* used by Discover.tsx, BooksDiscover.tsx, Calendar.tsx, MyBooks.tsx, Audiobooks.tsx, UserLayout.tsx, NotificationBell.tsx and lib/me.tsx must map to a user/requester/public spec. The admin-only calls in Audiobooks.tsx/Discover.tsx (audioServer*, approve/decline) are behind role checks in those components. Then do a requester click-through on the LAN.
  - **Files:** `internal/httpapi/router.go (new)`, `internal/httpapi/routes_public.go (new)`, `internal/httpapi/routes_user.go (new)`, `internal/httpapi/routes_staff.go (new)`, `internal/httpapi/routes_admin.go (new)`, `internal/httpapi/server.go`, `internal/httpapi/auth.go`, `internal/httpapi/indexers.go`, `internal/httpapi/routes_test.go (new)`, `internal/httpapi/testdata/routes.golden (new)`, `web/src/lib/api.ts`
  - **Acceptance:**
    - On the LAN, a requester session gets 403 from these, among others: /api/v1/queue, /downloads, /history, /movies, /movies/1/releases, /series/1/releases, /books/1/releases, /indexers, /indexers/prowlarr, /downloadclients, /settings, /convert/logs, /subtitles/logs, /library/fit, /books/1.
    - GET /api/v1/search returns 404.
    - As a requester, Discover (movies, TV, books), Calendar, My Books, Audiobooks and the notification bell work with no failed API calls in the network panel. A read-only account can browse the same pages but can't request.
    - Staff pages are unchanged for managers and admins.
    - Off-LAN behaviour is identical to before: the golden ext column matches the old prefix list.
    - server.go and routes_*.go contain no raw mux.HandleFunc and no a.protected.
  - **Tests:** Go TestRouteScopesGolden: rt.specs vs testdata/routes.golden.; Go TestRouteAuthz: build a real mux with rt.sentinel=true and a.deps.Log set to a discard logger. For every spec, fill each {param} with '1' and request it as anonymous, readonly, requester, manager and admin via withUser. Expect: 401 for anonymous on non-public routes; 403 for readonly on requester/staff/admin; 403 for requester on staff/admin; 403 for manager on admin; the 299 sentinel otherwise.; Go TestExternalParity: for every spec with scope ≤ requester, externalAllowed(samplePath) == spec.External.; Go TestNoRawRouteRegistration: read the routes*.go and server.go sources and fail on 'mux.HandleFunc(' or 'a.protected('.; UI check: sign in as a requester and as a read-only account on the LAN and click through every requester page.
  - **Risk:** Missing a route the requester UI calls would break that page for family accounts. The import-graph reach check in step 7, the TS-to-spec mapping and a manual click-through cover this. The diff is large but mechanical. Land it in one commit, with the golden file reviewed line by line. SEC-01 may land first; if it does, its three GET routes simply move into routes_staff.go.
  - **Resolves:** backend-2, backend-16, ops-13, movies-3, integrations-7, books-10
<a id="sec-03"></a>
- [x] **SEC-03 · Websocket topic policy: non-staff receive only their own events and the heartbeat** — `P0` · `S` · Phase 0
  - **Problem:** realtime.Hub.Run subscribes to '*' and broadcasts every bus event to every connected client (hub.go:63-96). /api/v1/ws only needs a signed-in user. The Insights poller publishes plex.stream.started and plex.buffering with {user, title, player, platform, decision} (insights/poller.go:224-227), so any LAN requester or read-only account can watch live who is watching what. That data is manager-only everywhere else (Insights routes, server.go:337-350). release.grabbed, import.held, file.removed (with a path) and the rest leak too. Only staff pages (Dashboard.tsx, MovieDetail.tsx) use the socket today.
  - **Approach:** 1. internal/realtime/hub.go:
       - type Viewer struct{UserID int64; Staff bool}. Client gets a viewer field. Connect(v Viewer) *Client.
       - Run marshals each event once (as today) and calls h.broadcast(ev.Topic, msg).
       - broadcast checks allowed(topic, c.viewer) per client before the non-blocking send.
    2. New internal/realtime/policy.go: allowed(topic string, v Viewer) bool.
       - Staff → true.
       - topic == 'server.heartbeat' → true.
       - strings.HasPrefix(topic, 'user.'+strconv.FormatInt(v.UserID,10)+'.') → true.
       - Otherwise false.
       Doc comment: every new topic is staff-only unless policy.go says otherwise. Per-user events (REQ's request progress) must be published as user.<id>.<what>.
    3. internal/httpapi/ws.go: u, _ := userFrom(r); client := a.deps.Realtime.Connect(realtime.Viewer{UserID: u.ID, Staff: !u.Disabled && u.Role.AtLeast(auth.RoleManager)}).
    4. Update hub_test.go's existing tests to pass a staff Viewer.
  - **Files:** `internal/realtime/hub.go`, `internal/realtime/policy.go (new)`, `internal/realtime/hub_test.go`, `internal/realtime/policy_test.go (new)`, `internal/httpapi/ws.go`
  - **Acceptance:**
    - A requester connected to /api/v1/ws receives server.heartbeat frames but no plex.stream.started, plex.buffering, release.grabbed, movie.downloaded or file.removed frames while those events fire.
    - A staff client still receives every topic. The Dashboard 'connected' dot and MovieDetail live refresh still work.
    - An event published as user.7.request.updated reaches user 7's socket and nobody else's (staff included).
  - **Tests:** Go realtime.TestHubFiltersByViewer: one staff client and one requester client (UserID 7). Publish plex.stream.started, server.heartbeat, user.7.request.updated, user.8.request.updated and an unknown topic. Assert the requester got the heartbeat and user.7 only, and staff got all five.; Go realtime.TestPolicyAllowed table, including the boundary 'user.70.x' not matching user 7.; Existing TestRunForwardsBusEvents updated to Connect(Viewer{Staff:true}).
  - **Risk:** Low: only staff pages use the socket today. A role change takes effect on the next reconnect. A disabled account's sessions already fail validation, so its reconnect fails.
  - **Resolves:** backend-4, ops-13

#### Milestone: M2: Audiobook listening stays private

_No log line, live or already on disk, names a book, an author or a search term from the audiobook apps, and no line pairs a user with an item. The 'how much and when, never what' mandate holds again._

<a id="sec-04"></a>
- [x] **SEC-04 · Audiobook privacy: request logs carry route patterns and query keys only, plus a one-time scrub of old log files** — `P0` · `S` · Phase 0
  - **Problem:** internal/audioserver/server.go logRequest (582-599) logs every non-skipped request at INFO with the raw path and q.Encode(), and removes only `token`. That records:
- POST /api/items/b45/play and GET /api/items/{id};
- the bookmark routes;
- /api/authors/{aid};
- /api/libraries/{lib}/search?q=<terms>.

These lines go into the applog ring and into the persisted arrmada.log.jsonl and its rotated files. Managers can read them on the Logs page (GET /api/v1/logs is RoleManager, server.go:308), and the files get attached to bug reports. Matching timestamps with the 'signed in user=' line (server.go:310) or the admin Listening tab shows who played which book. At debug level, handlers_play.go:113-114 logs the username and item key together. The panic line (server.go:218) logs the raw path. The main API's logRequests (middleware.go) logs r.URL.Path at debug, which carries book ids for /api/v1/books/{id}/audiobook and any future /api/v1/me/audio/* web-player routes. This breaks the 'how much and when, never what' mandate. The logRequest doc comment ('never a token or query') contradicts the code.
  - **Approach:** 1. Add internal/applog/route.go:
       - RedactPath(p string) string replaces any segment that is an item key (^b\d+(v\d+)?$), a UUID, an author or series id (^(au|se)[0-9a-f]{12}$), a user id (^u\d+$) or all digits with '{id}'.
       - RouteLabel(r *http.Request) string returns r.Pattern when it is non-empty and not the catch-all '/', otherwise r.Method+' '+RedactPath(r.URL.Path).
       - QueryKeys(q url.Values, drop ...string) string returns the sorted, comma-joined parameter names, never the values.
    2. audioserver/server.go logRequest:
       - Log "route", applog.RouteLabel(r) instead of path.
       - Log "query_keys", applog.QueryKeys(q, "token") instead of query.
       - Keep method, status, bytes, the token-present flag and the client UA. The skip logic keeps working on r.URL.Path.
       - Fix the doc comment.
       withCommon passes r straight to the mux, so r.Pattern is set in place (Go 1.22+) and is visible after next.ServeHTTP and inside the deferred recover.
    3. server.go:218 panic line: log "route", applog.RouteLabel(r).
    4. handlers_play.go:113-114: drop the "user" and "item" attrs. Keep the message plus 'saved' and 'reported' positions.
    5. httpapi/middleware.go logRequests: log "route", applog.RouteLabel(r). logRequests wraps the mux directly, so the pattern is set after next.ServeHTTP. recoverPanics sits outside authenticate, which copies the request with WithContext, so it can't see the pattern: it logs applog.RedactPath(r.URL.Path).
    6. One-time scrub of lines already on disk:
       - Add applog.RewriteFiles(path string, fn func(Entry) (Entry, bool)) error. It rewrites arrmada.log.jsonl and arrmada.log.jsonl.1..FilesKept atomically (temp file in the same dir, fsync, rename), keeps torn or malformed lines skipped, and keeps entries fn returns true for.
       - audioserver.ScrubLegacyEntry(e applog.Entry) (applog.Entry, bool):
         * for Message 'audiobook server: request', it rewrites the path=… attr to route=<RedactPath> and query=… to query_keys=<keys>;
         * for 'audiobook server: holding a jump back…' and 'audiobook server: panic', it strips user=/item= and redacts the path;
         * for the main API 'request' debug lines, it redacts path=.
       - cmd/arrmada/main.go: before logRing.Restore(logPath), if <DataDir>/logs/.scrub-audiobook-v1 is missing, run RewriteFiles with ScrubLegacyEntry, then write the marker. On failure, log a Warn and continue booting.
    7. web/src/pages/Audiobooks.tsx Listening tab: reword the note to 'Nothing in Arrmada, logs included, records which book anyone plays.' Keep the existing style.
  - **Files:** `internal/applog/route.go (new)`, `internal/applog/route_test.go (new)`, `internal/applog/persist.go`, `internal/applog/persist_test.go`, `internal/audioserver/server.go`, `internal/audioserver/handlers_play.go`, `internal/audioserver/scrub.go (new)`, `internal/audioserver/server_test.go`, `internal/httpapi/middleware.go`, `cmd/arrmada/main.go`, `web/src/pages/Audiobooks.tsx`
  - **Acceptance:**
    - After signing in, browsing, opening, playing, bookmarking and searching from Lissen/Plappa, the Logs page shows lines like route="POST /api/items/{id}/play" query_keys="limit,q". There is no b<id>, no au…/se… id and no search text.
    - With ARRMADA_LOG_LEVEL=debug, no log line contains both a username and an item key.
    - Sign-in steps, failed sign-ins and every response of 400 or above are still logged, with placeholders, so client-compatibility debugging still works.
    - After the first boot of the new version, arrmada.log.jsonl and its rotated files contain no item paths or search terms from earlier runs. The marker exists, and the scrub doesn't run again.
  - **Tests:** Go audioserver.TestRequestLogNeverNamesABook: a debug-level slog handler over a buffer. Exercise GET /api/items/{key}, POST /api/items/{key}/play, a held-jump sync, POST /api/me/item/{key}/bookmark, GET /api/authors/{aid}, GET /api/libraries/{lib}/search?q=dungeon and an unknown /api/items/{key}/nope. Assert the buffer contains none of the key, 'dungeon' or the author id, and does contain '{id}' and 'query_keys'.; Go applog.TestRedactPath table: b12, b12v3, a UUID, au…/se… ids, digits, u5, a plain segment left alone.; Go httpapi.TestRequestLogUsesRoutePattern: a request to /api/v1/books/12/audiobook logs 'GET /api/v1/books/{id}/audiobook'.; Go applog.TestRewriteFilesScrubsEntries: the current and rotated files are rewritten, untouched entries are kept byte-for-byte, and a torn line is dropped.; Go audioserver.TestScrubLegacyEntry on the old attrs format.
  - **Risk:** Less detail when debugging a new audiobook client. Route pattern, status, bytes and query keys are enough to see which call failed. Copies of old lines already in docker's own stdout log survive until the container is recreated (./update.sh recreates it). Say so in the commit message. The scrub runs once on boot, before the ring restores, and touches at most about 100 MB, so boot is a little slower that one time.
  - **Resolves:** audiobooks-1

#### Milestone: M3: Standing-rule gaps closed

_Books Discover runs through a tag-based adult filter on every surface. No library or import path can sit under /data, and the folder picker no longer offers /data._

<a id="sec-05"></a>
- [x] **SEC-05 · Tag-based adult filter on every Books Discover surface** — `P1` · `S` · Phase 2
  - **Problem:** No book path calls the always-on adult filter. The browse rows, trending, Recommended, genre/subject rows, author works, similar books and Discover search all skip it, while every movie and TV Discover surface goes through it (metadata/discover.go). The adultfilter package matches video-porn studio and site tokens, so running it over book titles alone would catch almost nothing. Explicit titles can surface in the Romance, trending and search rows that children on shared accounts see.
  - **Approach:** 1. New internal/adultfilter/books.go, so the single-package policy holds:
       - BookIsAdult(title string, tags []string) bool is true when adultfilter.Matches(title), or when any tag matches on word boundaries, case-insensitive: erotica, erotic, erotic romance, erotic fiction, bdsm, pornography, porn, xxx.
       - It must not match 'Adult Fiction', 'New Adult', 'Young Adult' or 'Adult' alone.
       - FilterBooks(in []metadata.BookResult) []metadata.BookResult checks Genres plus Tags. This takes an import of metadata into adultfilter; if that creates a cycle, take a func(i int) (string, []string) accessor instead.
    2. Data:
       - Add Tags []string `json:"-"` to metadata.BookResult (provider.go:211), filter-only, so the UI payload doesn't change.
       - Open Library: add 'subject' to the fields= list at openlibrary.go:75 and :156, decode subject in search docs and in decodeWorkList (/subjects and /trending works), and map the first 30 into Tags.
       - Hardcover: Genres already come from cached_tags. Make sure the search and browse mappings in hardcover.go and hardcover_browse.go fill Genres, or Tags when there are more than the displayed few.
    3. Apply FilterBooks once, at the top of enrichBookCards (httpapi/books.go:553). Every discover handler goes through it: browse, recommended rows, trending, search, author works, subject and similar.
    4. handleBookDiscoverDetail: if BookIsAdult(details.Title, details.Subjects ∪ Genres), answer 404 'not available'.
    5. /books/lookup and the Add-book flow stay unfiltered (manager-only), matching the adultfilter package doc ('a rare legitimate title can still be added by exact search').
  - **Files:** `internal/adultfilter/books.go (new)`, `internal/adultfilter/books_test.go (new)`, `internal/metadata/provider.go`, `internal/metadata/openlibrary.go`, `internal/metadata/hardcover.go`, `internal/metadata/hardcover_browse.go`, `internal/httpapi/books.go`, `internal/httpapi/books_discover_test.go (new)`
  - **Acceptance:**
    - A book tagged 'Erotica' or 'Erotic Romance' appears in no Books Discover row (trending, browse, Recommended, Romance subject, similar, author works) and not in Discover search, for any role.
    - Ordinary Romance, 'New Adult' and 'Young Adult' books still appear.
    - The discover detail for a flagged key returns 404.
    - An admin can still find and add such a title through Books → + Add book (/books/lookup).
  - **Tests:** Go adultfilter.TestBookIsAdult table: Erotica yes, Erotic Romance yes, BDSM yes, Romance no, New Adult no, Young Adult no, Adult Fiction no, title with an adult studio token yes.; Go httpapi.TestBookDiscoverFiltersAdult: a fake BookSources provider returns a mix. Browse, subject and search drop the flagged entries; /books/lookup keeps them.; Go metadata: decodeWorkList fixture with a 'subject' array fills Tags.
  - **Risk:** Tag coverage differs by catalogue. Open Library subjects are noisy, and Hardcover tags are community-assigned, so some explicit books will still get through. The filter blocks only on clear tags, so mainstream romance isn't hidden. Coordinate with BOOK if its Discover rework replaces enrichBookCards: the filter must stay in the single funnel.
  - **Resolves:** books-14
<a id="sec-06"></a>
- [x] **SEC-06 · Never under /data: refuse library folders in or above the data dir, and drop /data from the folder picker** — `P1` · `S` · Phase 2
  - **Problem:** Standing rule: media must never be mounted at /data (the DB directory). handleSetLibraryPaths (library_paths.go:59-91) stores any trimmed string with no check, including the paths saved by the setup wizard. ApplySavedLibraryDirs applies them at boot. The folder picker's start candidates include '/data' (library_paths.go:96), and nothing rejects a library under DataDir. An import or Convert write into /data would mix media with the database, and the hourly DB/log housekeeping and backups would then treat media as app state. This is the /data slice of system-10; CFG owns the rest (live per-folder checks).
  - **Approach:** 1. handleSetLibraryPaths: for each provided non-empty path p:
       - r := pathguard.Resolve(p);
       - reject with 400 when pathguard.Under(r, cfg.DataDir) or pathguard.Under(cfg.DataDir, r), i.e. a library that contains the data dir, such as '/'. Message: '<Library> folder can't be inside or contain Arrmada's data folder (<DataDir>). Media must live on its own mount, e.g. /storage or /media.'
       - Validate every field before writing any, so a bad one doesn't leave a partial save.
    2. handleBrowse:
       - start candidates become {"/storage", "/media"} (drop "/data");
       - when listing a directory, skip the entry whose resolved path equals cfg.DataDir, or mark it with 'reserved': true and have FolderPicker grey it out with 'Arrmada's data folder'. Pick one; skipping is simpler.
    3. Boot and health:
       - in ApplySavedLibraryDirs / logEnvironment, log an Error per effective root (env or saved) that violates the rule;
       - handleSystemHealth adds an "error" warning '<Library> folder is inside Arrmada's data folder — move it to its own mount' so the dashboard health panel shows it.
    4. checkImportPath ([SEC-01](#sec-01)) already rejects DataDir for imports. Share the helper as a.dataDirConflict(path) error.
  - **Files:** `internal/httpapi/library_paths.go`, `internal/httpapi/library_paths_test.go`, `internal/httpapi/setup.go`, `internal/httpapi/health_system.go`, `web/src/pages/Library.tsx`
  - **Acceptance:**
    - Saving /data, /data/movies or / as any library folder (wizard or Settings → Library) returns 400 with the plain-words message, and nothing is saved.
    - The folder picker opens on /storage or /media and never offers the data dir.
    - An existing install whose env points a library under /data shows a red health warning on the dashboard and logs an Error at boot, but still starts.
  - **Tests:** Go TestSetLibraryPathsRefusesDataDir: DataDir and the library set to temp dirs. Expect a child of DataDir → 400, an ancestor of DataDir → 400, a sibling → 200. A partial body with one bad field saves nothing.; Go TestBrowseSkipsDataDir.; Go TestSystemHealthFlagsLibraryUnderDataDir.
  - **Depends on:** [SEC-01](#sec-01)
  - **Risk:** Low. The owner's real install keeps media on /storage. CFG's per-folder checks UI should display this same error rather than re-implement it, so tell CFG to reuse a.dataDirConflict and pathguard.
  - **Resolves:** system-10

#### Milestone: M4: Secrets stay on the server; admin means admin

_No API response carries an indexer apikey, a MAM token or a webhook secret. Grabs use opaque tokens, which also closes the arbitrary-URL fetch. Managers can no longer change API keys, module toggles, Plex sign-in, recycle purges or library folders, and can't read logs._

<a id="sec-07"></a>
- [x] **SEC-07 · Opaque release tokens: no download URL ever reaches the browser, and grabs can't fetch arbitrary URLs** — `P1` · `M` · Phase 4
  - **Problem:** RankedRelease.DownloadURL (json download_url, coordinator.go:350) is the Torznab enclosure. torznab.go:78 copies it verbatim, so for Prowlarr/Jackett-synced indexers it embeds apikey=, and MAM links carry the owner's personal dl token (myanonamouse.go:296-301). It reaches the browser for:
- movie, series, book and audio-version interactive searches;
- /quality/test.

Even manager-only, it ends up in devtools, HAR files and extensions. The grab and blocklist endpoints also trust whatever download_url the browser posts, and the server then fetches it (grabTo/fetchTorrentPayload), so any manager account can make the server fetch arbitrary URLs. These are POST /grab (grab.go), POST /series/{id}/grab (series.go:439), POST /books/{id}/grab (books.go:240) and POST /movies/{id}/blocklist (movies.go:152).
  - **Approach:** 1. New internal/automation/releasetokens.go:
       - type ReleaseRef struct{Indexer, DownloadURL, Title, MediaKind string; MediaID, VersionID, UserID int64; ExpiresAt time.Time}.
       - type releaseTokens: a mutex-guarded map plus an insertion-order slice. Tokens are 16 bytes from crypto/rand, base64url. TTL 2h, cap 20,000 with oldest-first eviction, lazy expiry on lookup.
       - Issue(ref) string.
       - Resolve(token string, kind string, mediaID, userID int64) (ReleaseRef, error), with ErrReleaseExpired for unknown/expired and ErrReleaseScope for a kind, media or user mismatch.
       - Coordinator gains tokens *releaseTokens and Tokens() accessor.
    2. RankedRelease: DownloadURL becomes `json:"-"`, plus Token string `json:"token,omitempty"`. Strip apikey/jackett_apikey/passkey query params from InfoURL defensively.
    3. httpapi helper (a *api) tokenize(list *automation.ReleaseList, kind string, mediaID int64, r *http.Request) sets each release's Token. Call it in handleMovieReleases, handleSeriesReleases and handleBookReleases (book releases carry version_id into the ref). /quality/test returns releases without tokens; it never grabs.
    4. Grab and blocklist take tokens; remove download_url from all four request structs, with no fallback:
       - POST /grab {token, movie_id};
       - POST /series/{id}/grab {token};
       - POST /books/{id}/grab {token, version_id};
       - POST /movies/{id}/blocklist {token, search_again} or {title, indexer}. Title-only blocks stay allowed, since blocklist entries are keyed by title.
       Each resolves the token on the server, checks the scope, then calls the existing Grab/GrabForSeries/GrabForBook/Blocklist with ref.Indexer, ref.DownloadURL and ref.Title. Errors: ErrReleaseExpired → 410 'This search result has expired — search again'; ErrReleaseScope → 400 'That result belongs to a different title'. The .torrent upload routes (grabtorrent) are unchanged; they carry the file itself.
    5. Frontend:
       - api.ts: RankedRelease drops download_url and gains token. grab, grabSeries and grabBook send {token,…}. blockRelease sends {token, search_again}.
       - Update the callers: MovieDetail.tsx:717-718, Movies.tsx:288-289, SeriesDetail.tsx:290/389/506, SeriesSearchModal.tsx:36, BookDetail.tsx:341, AudioVersions.tsx:213.
       - BookReleaseModal.tsx keys and busy/grabbed state switch from download_url to token.
       - On a 410, the modals show 'These results expired — search again' with the existing re-search action.
  - **Files:** `internal/automation/releasetokens.go (new)`, `internal/automation/releasetokens_test.go (new)`, `internal/automation/coordinator.go`, `internal/httpapi/grab.go`, `internal/httpapi/movies.go`, `internal/httpapi/series.go`, `internal/httpapi/books.go`, `internal/httpapi/releasetokens.go (new)`, `internal/httpapi/grab_test.go (new)`, `web/src/lib/api.ts`, `web/src/components/ReleaseSearchModal.tsx`, `web/src/components/SeriesSearchModal.tsx`, `web/src/components/BookReleaseModal.tsx`, `web/src/components/AudioVersions.tsx`
  - **Acceptance:**
    - No /releases or /quality/test response contains download_url, 'apikey=' or a MAM dl token (checked in the network tab against a Prowlarr-synced indexer).
    - Grabbing and blocklisting from the movie, series, book and audio-version modals all still work.
    - A token issued for movie 1 is rejected on movie 2's grab, and a token issued to one user is rejected for another.
    - A grab from a modal left open across a restart, or past 2h, says 'search again' (410) instead of failing obscurely.
    - POSTing {download_url: 'http://10.0.0.1/x'} to any grab endpoint is rejected as an invalid body. The server makes no fetch.
  - **Tests:** Go automation.TestReleaseTokensTTLEvictionScope: fake clock; expiry → ErrReleaseExpired; cap eviction is oldest-first; kind, media or user mismatch → ErrReleaseScope.; Go: json.Marshal(RankedRelease{DownloadURL: 'x?apikey=1'}) has no download_url key.; Go httpapi.TestGrabByToken: valid → Grab called with the stored URL (stub coordinator); expired → 410; other movie → 400; legacy download_url body → 400.; UI check: grab and blocklist from every modal listed.
  - **Depends on:** [SEC-02](#sec-02), ACQ
  - **Risk:** Every grab entry point has to move in one change, or a modal breaks silently. Grep web/src for download_url after the change; it should be zero hits. A restart invalidates open modals, which the 410 copy covers. ACQ: coordinate ordering with ACQ's acquisition-core rework of the grab path. Tokens are a thin in-memory layer with no schema, so ACQ can later back them with candidate rows.
  - **Resolves:** integrations-7, movies-3
<a id="sec-08"></a>
- [ ] **SEC-08 · Admin Apprise connections: validate on save, never return the URL, admin-only writes** — `P1` · `S` · Phase 6
  - **Problem:** handleCreateNotification and handleUpdateNotification (httpapi/notifications.go:23-58) never call notify.ValidateAppriseURL, which today guards only per-user URLs. GET /api/v1/notifications returns the full URL (discord webhook tokens, SMTP passwords, ntfy credentials) to every RoleManager user (server.go:205-209), and the Insights notifications card shows it in a plain text input. The Plex token, by contrast, is never sent back.
  - **Approach:** 1. Create and update call notify.ValidateAppriseURL(c.URL) when a URL is provided, and answer 400 with its message on failure.
    2. Add notify.URLHint(raw string) string:
       - for schemes whose host isn't secret (json/jsons/form/forms/xml/xmls/webhook/webhooks, gotify/gotifys, ntfy/ntfys with a host and path, matrix/matrixs, mailto/mailtos), return scheme://host/••••;
       - for everything else (discord, tgram/telegram, slack, pover, pbul, …), return scheme://•••• plus the last 4 characters;
       - never userinfo, path secrets or query values.
    3. API shape:
       - List returns a view struct: every Connection field except URL, plus url_hint, url_set, and invalid_reason when a stored URL no longer passes ValidateAppriseURL.
       - Update takes url *string: nil or empty keeps the stored URL.
       - New POST /api/v1/notifications/{id}/test sends using the stored URL. The existing body-based /notifications/test stays for testing before save.
    4. Roles:
       - create, update, delete and both test routes → admin;
       - list stays staff (it's redacted now).
       If [SEC-02](#sec-02) has landed, register them via rt.admin and rt.staff; otherwise use requireRole.
    5. UI: in the notifications ConnCard (Insights.tsx today, or the Alerts page if INT/PLEX's move has landed), the URL becomes a password-style input showing url_hint as its placeholder, with 'saved — leave blank to keep', mirroring the Plex token field. Test on a saved card calls the by-id route. Managers see the list read-only.
  - **Files:** `internal/notify/notify.go`, `internal/notify/hint_test.go (new)`, `internal/httpapi/notifications.go`, `internal/httpapi/notifications_test.go (new)`, `internal/httpapi/server.go (or routes_admin.go after SEC-02)`, `web/src/pages/Insights.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Saving 'http://evil' or '-config' as a connection URL is rejected with a clear error.
    - No API response contains a saved webhook token or password (network tab after saving discord://123/SECRET and mailtos://u:pw@host).
    - Renaming a connection or toggling its events without retyping the URL keeps it working, and Test on the saved card still succeeds.
    - A manager gets 403 on create, update, delete and test, and can still see the redacted list.
  - **Tests:** Go notify.TestURLHint table: discord, tgram, mailtos with a password, gotify, ntfy topic-only, json with a query secret. Neither secret appears in any hint.; Go httpapi.TestCreateNotificationRejectsInvalidURL.; Go httpapi.TestListNotificationsRedactsSecrets: store discord://123/SECRET and mailtos://u:pw@host; assert the body has no 'SECRET' or 'pw'.; Go httpapi.TestUpdateKeepsStoredURLWhenOmitted.; Go httpapi.TestTestNotificationByIDUsesStoredURL (stub apprise binary).
  - **Depends on:** INT
  - **Risk:** Stored URLs with schemes outside the allowlist fail validation on their next edit. invalid_reason surfaces that instead of silently dropping them. INT: if the Alerts-page move (insights.t8) is in flight, put the UI half in whichever page is current. The backend half doesn't depend on it.
  - **Resolves:** insights-7
<a id="sec-09"></a>
- [x] **SEC-09 · Admin means admin: API keys, system settings, folders, recycle purge and logs are admin-only** — `P1` · `S` · Phase 2
  - **Problem:** The UI hides System and Users from managers (Settings.tsx:84), but the API lets managers do all of the following (server.go:113-121, 308-313):
- PUT /apikeys (and read and test them);
- PUT /settings for module toggles, Plex sign-in and auto-approve, the disk guard and recycle limits;
- PUT /system/library and browse the host filesystem;
- empty or purge the recycle bin;
- read /logs.

The Logs page also exposes everything any module logs.
  - **Approach:** 1. Move these to admin (rt.admin after [SEC-02](#sec-02)):
       - GET /apikeys, PUT /apikeys/{id}, POST /apikeys/{id}/test;
       - PUT /system/library, GET /system/browse;
       - POST /recycle/empty, POST /recycle/delete;
       - GET /logs.
       GET /system/library, recycle stats/items/restore and GET /settings stay staff.
    2. handleUpdateSettings field gating:
       - Managers may set search_on_add, naming_*, write_nfo and download_artwork.
       - Admin-only fields: books_enabled, music_enabled, plex_login_enabled, plex_login_auto_approve, tmdb_region (edited only in the admin APIKeysSection), recycle_max_gb, recycle_retention_days, downloads_disk_guard, downloads_disk_guard_pause_pct, downloads_disk_guard_resume_pct.
       - For a non-admin, an admin-only field that is provided **and differs from the current effective value** gets 403 'Only an admin can change <label>'. Settings.tsx's SaveBar sends the whole settings object, so an unchanged resend must pass.
       - Validate before writing anything.
    3. Frontend:
       - nav.ts / Sidebar.tsx: hide Logs for non-admins (use isAdmin from lib/me.tsx).
       - Library.tsx LibraryFolders: managers see the paths read-only (no Browse, no Save folders). The scan buttons stay; they're manager routes.
       - The Settings Library tab keeps 'Search on add' for managers.
    4. Add to the [SEC-02](#sec-02) golden and route-walk test (the scopes change in the golden diff).
  - **Files:** `internal/httpapi/server.go (or routes_admin.go / routes_staff.go after SEC-02)`, `internal/httpapi/settings.go`, `internal/httpapi/settings_test.go (new)`, `internal/httpapi/testdata/routes.golden`, `web/src/lib/nav.ts`, `web/src/components/Sidebar.tsx`, `web/src/pages/Library.tsx`
  - **Acceptance:**
    - A manager gets 403 on PUT /api/v1/apikeys/{id}, PUT /system/library, GET /system/browse, POST /recycle/empty, POST /recycle/delete and GET /logs.
    - A manager can still toggle 'Search on add' on Movies and Series, and can save the Settings Media tab (which resends admin fields unchanged).
    - A manager who sends plex_login_enabled:true while it's false gets 403 naming the field, and nothing is saved.
    - Logs is gone from a manager's nav. An admin's behaviour is unchanged.
  - **Tests:** Go TestManagerCannotChangeAdminSettings: plex_login_enabled changed → 403; same value resent → 200; search_on_add → 200; a mixed body with one forbidden change saves nothing.; Go route-walk (SEC-02 TestRouteAuthz) covers the new admin routes automatically; regenerate the golden.
  - **Depends on:** [SEC-02](#sec-02)
  - **Risk:** Any existing manager accounts lose these abilities. Confirm with the owner before shipping (likely none exist, but family 'helpers' might). The 'unchanged resend' rule depends on comparing against effective defaults: use the same Get/GetBool defaults as handleGetSettings.
  - **Resolves:** system-6

#### Milestone: M5: Hardened edges

_The off-LAN gate reads the same route table, with no separate prefix list to drift. Login throttling keys on the real client IP, counts failures only, and can't lock the owner out from home. Import folder listings are bounded and cancellable. Requester Apprise links can't reach internal hosts._

<a id="sec-10"></a>
- [x] **SEC-10 · Drive the off-LAN gate from the route table and delete the prefix allowlist** — `P1` · `S` · Phase 2
  - **Problem:** externalGate (external.go:129-139) decides off-LAN reachability from externalAllowedPrefixes and the externalAllowedExact regex, a second list maintained separately from the routes. The two drift: /books/{id}/cover-image is used by My Books but blocked off-LAN, so uploaded covers break outside the house. A future route under an allowed prefix (any new /api/v1/me/* or /api/v1/requests/* route) silently becomes internet-reachable for non-staff.
  - **Approach:** 1. externalGate keeps classifyExternal and the staff exemption. It only stamps externalCtxKey; it no longer blocks.
    2. The router guard (router.go, [SEC-02](#sec-02)): when isExternalRequest(r) && !spec.External → 403 'not available outside your network'. Unmatched /api paths fall through to the mux's 404/405, which is fine. Non-/api SPA paths are registered public+ext.
    3. Delete externalAllowedPrefixes, externalAllowedExact and externalAllowed(). Update TestExternalAllowsOnlyTheEbookDownload (mybooks_test.go) and TestExternalGateExemptsStaff to go through the router.
    4. Deliberate golden diff: mark GET /books/{id}/cover-image ext(). It's a cover image, and the handler serves only the cover. Leave /calendar and /ws LAN-only, as today.
    5. Remove the [SEC-02](#sec-02) parity test (now meaningless) and keep the golden ext column as the single source.
  - **Files:** `internal/httpapi/external.go`, `internal/httpapi/router.go`, `internal/httpapi/externalgate_test.go`, `internal/httpapi/mybooks_test.go`, `internal/httpapi/testdata/routes.golden`
  - **Acceptance:**
    - An off-LAN requester can reach /api/v1/discover/trending, /api/v1/books/12/ebook and /api/v1/books/12/cover-image, but not /api/v1/books/12, /api/v1/calendar or /api/v1/ws.
    - An off-LAN admin or manager still gets the whole app.
    - external.go no longer contains any path list.
  - **Tests:** Go TestExternalGateUsesRouteSpecs: a public RemoteAddr (203.0.113.50) with a requester user, over a table of paths → expected 200 (sentinel) or 403.; Go: existing TestClassifyExternalForwarded unchanged (or updated by SEC-11).; Go: golden updated with the cover-image ext change.
  - **Depends on:** [SEC-02](#sec-02)
  - **Risk:** Low once SEC-02's golden is in. The behaviour change is limited to cover-image, which is now reachable off-LAN for requesters.
  - **Resolves:** backend-2
<a id="sec-11"></a>
- [x] **SEC-11 · Trusted-proxy client IP, and login throttling that counts failures only and can't lock the owner out** — `P1` · `S` · Phase 2
  - **Problem:** httpapi.clientIP (ratelimit.go:71-83) takes the leftmost X-Forwarded-For from any peer and ignores Cf-Connecting-Ip, even though compose sets ARRMADA_EXTERNAL_HEADER=Cf-Connecting-Ip. Rotating the header dodges the per-IP limit. handleLogin calls loginAllowed before Authenticate (auth.go:135-138), so successful logins count too, and the limiter has no reset. With 10 attempts per 15 minutes on 'login-user:<name>' (server.go:102), any internet visitor can keep the owner's username unable to sign in, from the LAN as well. external.go forwardedClientIP has the same leftmost-hop trust. The audiobook server's own helper is better (trusts headers only from private peers, resets on success, audioserver/server.go:395-411), but it still takes the leftmost XFF.
  - **Approach:** 1. New internal/netutil/clientip.go:
       - ClientIP(r) string and ForwardedClientIP(r) net.IP.
       - Forwarded headers are believed only when the TCP peer is loopback or private.
       - Order: Cf-Connecting-Ip; then walk X-Forwarded-For right-to-left and return the first non-private hop (the leftmost if all are private); then the Forwarded for=; then the peer.
       - Replace httpapi.clientIP (and its indexByte/trimSpace helpers), external.go forwardedClientIP and audioserver.clientIP with it.
    2. loginLimiter (ratelimit.go): keep allow(key) for setup and plex-pin (attempt-counted), and add:
       - blocked(key) (bool, time.Duration);
       - fail(key);
       - reset(key);
       - a per-key failure log with the same GC.
    3. handleLogin:
       - ipKey := 'login:'+netutil.ClientIP(r).
       - Check blocked(ipKey): 10 failures per 15 min.
       - For off-LAN requests only (a.classifyExternal(r)), also check blocked('login-user:'+name).
       - Call Authenticate. On ErrInvalidCredentials, fail() both keys. On success, reset() both. 429 keeps Retry-After.
    4. Username dimension: an exponential delay after 5 failures within an hour (30s, 1m, 2m, 4m … capped at 15m), instead of the hard 10-per-15 wall. LAN sources are exempt from it but still IP-limited.
    5. At 20 or more failures in an hour for one username: log Warn without the password, and publish bus 'security.login_failures' {username, count, last_ip}. That topic is staff-only via [SEC-03](#sec-03)'s default-deny; INT can add it to the alert catalog.
  - **Files:** `internal/netutil/clientip.go (new)`, `internal/netutil/clientip_test.go (new)`, `internal/httpapi/ratelimit.go`, `internal/httpapi/auth.go`, `internal/httpapi/auth_plex.go`, `internal/httpapi/external.go`, `internal/audioserver/server.go`, `internal/httpapi/security_test.go`
  - **Acceptance:**
    - Successful logins never consume the budget.
    - After 10 bad passwords for 'admin' from an internet IP, the owner can still sign in from the LAN immediately, and from the internet after the delay.
    - A public peer sending a fake X-Forwarded-For is limited by its real address.
    - Behind cloudflared (a private peer plus Cf-Connecting-Ip), the limiter keys on the visitor's address, and LAN/external classification is unchanged.
  - **Tests:** Go netutil table: public peer + XFF → peer; private peer + Cf-Connecting-Ip → that IP; private peer + 'XFF: 6.6.6.6, 203.0.113.9' → 203.0.113.9; all-private chain → leftmost; Forwarded for= with port and brackets.; Go limiter: fail/reset semantics, exponential schedule with a fake clock, GC of idle keys.; Go handleLogin with a stub auth service: 10 failures from an external IP block that username externally but not from a LAN RemoteAddr; a success resets both keys.; Go: TestClassifyExternalForwarded updated to the right-to-left rule; TestForwardedClientIP moves to netutil.
  - **Risk:** Changing how forwarded IPs are read affects the external/LAN classification. The existing externalgate tests plus the new table pin the behaviour behind cloudflared and a local TLS proxy. Run a real check through the tunnel after deploying.
  - **Resolves:** backend-7
<a id="sec-12"></a>
- [x] **SEC-12 · Bounded, cancellable manual-import walks with a 'showing the first 500' notice** — `P1` · `S` · Phase 2
  - **Problem:** Even confined to the roots (SEC-01), a manager listing a library root walks every file on the array. The walks keep running after the browser disconnects because no context is passed:
- movies.Service.ManualImportCandidates(dir) (service.go:1146);
- library.FindVideos (importer.go:672), via Coordinator.SeriesImportCandidates (series_interactive.go:461);
- library.FindBookFiles (importer.go:55), via Coordinator.BookImportCandidates (books.go:1134).

None has a result cap.
  - **Approach:** 1. Context-aware and capped variants:
       - movies.Service.ManualImportCandidates(ctx, dir string, max int) ([]ImportCandidate, bool, error);
       - library.FindVideosCtx(ctx, dir, maxResults, maxVisited) ([]FoundVideo, bool, error);
       - library.FindBookFilesCtx(ctx, dir, maxResults, maxVisited) ([]FoundFile, bool).
       The WalkDir callback returns ctx.Err() when cancelled, and fs.SkipAll after maxResults (500) candidates or maxVisited (100,000) entries, setting truncated. The old FindVideos/FindBookFiles stay as uncapped wrappers over context.Background(), so the import pipeline is unchanged.
    2. Coordinator.SeriesImportCandidates(ctx, dir) and BookImportCandidates(ctx, dir) return (cands, truncated).
    3. The three list handlers use ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second) and respond {path, candidates, truncated}. If the walk times out, they return what they found with truncated=true and a 'note'.
    4. UI: the three ManualImportModal components (MovieDetail.tsx, SeriesDetail.tsx, BookDetail.tsx) show 'Showing the first 500 files — pick a narrower folder' when truncated, in the existing muted note style. api.ts manualImportList, seriesManualImportList and bookManualImportList gain truncated?: boolean.
  - **Files:** `internal/movies/service.go`, `internal/library/importer.go`, `internal/library/importer_test.go`, `internal/automation/series_interactive.go`, `internal/automation/books.go`, `internal/httpapi/movies.go`, `internal/httpapi/series.go`, `internal/httpapi/books.go`, `web/src/lib/api.ts`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/SeriesDetail.tsx`, `web/src/pages/BookDetail.tsx`
  - **Acceptance:**
    - A folder with more than 500 importable files returns 500 candidates with truncated=true, and the modal shows the notice.
    - Closing the tab mid-walk stops the walk within a second (debug log shows the context was canceled).
    - The downloads-default listing is unchanged for normal folders.
  - **Tests:** Go movies: ManualImportCandidates with an already-cancelled ctx returns ctx.Err() without visiting.; Go library: FindVideosCtx stops at maxResults and sets truncated; maxVisited respected; samples and small files still skipped.; Go library: FindBookFilesCtx same.
  - **Depends on:** [SEC-01](#sec-01)
  - **Risk:** Low. Callers of the old functions keep their behaviour. The 50 MB sample cutoff is unchanged.
  - **Resolves:** backend-1
<a id="sec-13"></a>
- [ ] **SEC-13 · Keep requester-owned Apprise URLs off internal hosts (save-time and send-time checks)** — `P2` · `S` · Phase 6
  - **Problem:** PUT /api/v1/me/apprise is open to any role, off-LAN too, because /api/v1/me/ is externally allowed. notify.ValidateAppriseURL (notify.go:296-316) allows the generic json/form/xml/webhook schemes, plus self-hosted ones (gotify, ntfy with a host, matrix, hassio, apprise) that take any host. The server then posts approve, decline and ready messages there (requests/usernotify.go notifyParties:205-240). That is a blind, fixed-content SSRF into the Docker network or the LAN (qBittorrent, Plex, routers).
  - **Approach:** 1. New internal/notify/ssrf.go:
       - type Resolver interface{ LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) }; net.DefaultResolver satisfies it.
       - CheckPublicHost(ctx, host, res) error rejects IP literals or any resolved address that is loopback, private (RFC1918, fc00::/7), link-local, CGNAT 100.64.0.0/10, unspecified or multicast, and also a resolution failure.
    2. ValidateUserAppriseURL(ctx, raw string, staff bool, res Resolver) error:
       - Staff use the existing ValidateAppriseURL.
       - Non-staff schemes are limited to {discord, telegram, tgram, slack, pover, pushover, pbul, pushbullet, ntfy, ntfys, gotify, gotifys, matrix, matrixs, mailto, mailtos}.
       - json*, form*, xml*, webhook*, apprise*, hassio, home-assistant, signal* and twilio are refused for non-staff, with 'This kind of link isn't allowed for your account — use a Discord, Telegram, ntfy, Pushover… link'.
       - mailto/mailtos are refused if they carry an smtp= query override, and the domain host must be public.
       - ntfy/ntfys with a path (self-hosted server) and gotify/matrix must CheckPublicHost(host). ntfy://topic (no path, meaning ntfy.sh) needs no resolution.
    3. handleSetMyApprise passes staff = u.Role.AtLeast(RoleManager) and net.DefaultResolver.
    4. Send time:
       - requests.Service gains SetStaffLookup(func(ctx, uid int64) bool), wired in main.go from auth.Service.UserByID.
       - notifyParties re-runs ValidateUserAppriseURL for the recipient before notify.Send. This catches DNS changes and URLs saved before this change.
       - On failure it skips the Apprise push (the inbox and Web Push still happen) and logs Warn with the user id and reason, never the URL.
    5. GET /me/apprise returns blocked_reason when the stored URL no longer passes. NotificationBell.tsx shows it under the field in the existing warning tone.
  - **Files:** `internal/notify/ssrf.go (new)`, `internal/notify/ssrf_test.go (new)`, `internal/notify/notify.go`, `internal/httpapi/usernotify.go`, `internal/requests/usernotify.go`, `internal/requests/usernotify_test.go`, `cmd/arrmada/main.go`, `web/src/components/NotificationBell.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - As a requester, saving json://10.0.0.5/hook, gotify://192.168.1.10/token or ntfys://qbittorrent/topic returns 400 with a plain-words reason.
    - As a requester, discord://… and ntfy://mytopic still save and receive the ready message.
    - An existing requester URL pointing at a private IP is skipped at send time, the inbox still gets the message, and the bell shows why.
    - A manager's own Apprise URL keeps the current rules.
  - **Tests:** Go notify.TestValidateUserAppriseURL table: staff/non-staff × schemes × resolved IPs, using a stub resolver that maps 'internal.lan' to 192.168.1.5 and 'push.example.com' to 93.184.216.34.; Go requests: notifyParties with a stored private-host URL makes no Apprise call (fake apprise bin records invocations) but inserts the inbox row.
  - **Depends on:** [SEC-08](#sec-08)
  - **Risk:** There is a DNS-rebinding window between this check and apprise's own lookup. That's acceptable at this severity: the content is fixed and the trigger is event-only. SEC-08 also edits notify.go, so land it first to avoid conflicts.
  - **Resolves:** backend-3

#### Milestone: M6: Sessions that last and can be ended

_Family members stay signed in as long as they keep using the app. An expired or revoked session lands on the login page instead of error banners. Everyone can see their own devices and sign the others out, and an admin can sign a user out everywhere._

<a id="sec-14"></a>
- [x] **SEC-14 · Sliding sessions, and a clean 'you were signed out' when a session ends** — `P2` · `S` · Phase 2
  - **Problem:** auth.Service.sessionTTL is a fixed 30 days (service.go:67), and ValidateSession (service.go:361) never extends it, so every family member's session hard-expires mid-use once a month. PWA users get dropped silently. MeProvider loads the user once at boot, and req() (api.ts:1053-1069) just throws on 401, with no 401 handling anywhere in web/src. After expiry, a password reset or a revocation, the open app shows 'authentication required' banners until a manual reload.
  - **Approach:** 1. auth.Service:
       - add now func() time.Time (default time.Now, injectable).
       - ValidateSessionInfo(ctx, raw) (*User, time.Time, error) also selects s.expires_at and compares with `s.expires_at > ?` using sqlTime(s.now()), the same format as the insert. ValidateSession stays as a wrapper.
       - ExtendSession(ctx, raw) (time.Time, error) sets expires_at = now+TTL.
    2. The authenticate middleware (httpapi/auth.go): when a cookie session validates with time.Until(exp) < TTL/2, call ExtendSession and re-set the cookie with the new Expires. That's at most one write per session per ~15 days. Factor the cookie writing in startSession into a.setSessionCookie(w, r, token, expires), with the same Path/HttpOnly/Secure/SameSite.
    3. Revocation semantics stay as they are: SetPassword deletes all sessions, and disabled users fail validation.
    4. Frontend:
       - In req(), when res.status === 401 and the path doesn't start with /api/v1/auth/, dispatch window.dispatchEvent(new CustomEvent('arrmada:signed-out')) before throwing.
       - lib/me.tsx MeProvider listens, clears the user and sets signedOut=true.
       - Login.tsx shows 'You were signed out — sign in again.' in the existing muted note style when signedOut is set.
       If FE's api-client refactor lands first, put the hook there.
  - **Files:** `internal/auth/service.go`, `internal/auth/service_test.go`, `internal/httpapi/auth.go`, `internal/httpapi/auth_test.go (new)`, `web/src/lib/api.ts`, `web/src/lib/me.tsx`, `web/src/pages/Login.tsx`
  - **Acceptance:**
    - A session used on day 20 has its expiry moved to day 50, and the browser cookie's Expires updates.
    - A session used on day 5 causes no DB write.
    - An expired or disabled session is still rejected.
    - Revoking a session (or changing the password) while the app is open drops the user on the login page with the 'signed out' note on the next API call, with no error banners.
  - **Tests:** Go auth: fake clock covering extend vs no-op vs expired.; Go httpapi: a request with a session past half-life gets a Set-Cookie with the new expiry; one before half-life gets none.; UI check: delete the session row (or log out in another tab), click anything, and land on Login with the note.
  - **Depends on:** FE
  - **Risk:** Low. Sessions now live as long as they're used, and a password change remains the kill switch. FE: only to avoid two edits of req(); this doesn't block.
  - **Resolves:** backend-13, system-6
<a id="sec-15"></a>
- [ ] **SEC-15 · See and end your sessions: device list, sign out other devices, admin 'sign out everywhere'** — `P2` · `S` · Phase 7
  - **Problem:** Users can't see where they're signed in or revoke a lost device. An admin can't sign a user out except by changing their password. Nothing in the product calls auth.RevokeUserSessions (service.go:297).
  - **Approach:** 1. New migration, the next free number (0090+ at the time of writing): `ALTER TABLE sessions ADD COLUMN last_seen_at TIMESTAMP`, `ADD COLUMN user_agent TEXT NOT NULL DEFAULT ''`, `ADD COLUMN ip TEXT NOT NULL DEFAULT ''`.
    2. auth.Service:
       - CreateSession(ctx, userID, ua, ip). startSession passes r.UserAgent() and netutil.ClientIP(r) ([SEC-11](#sec-11)).
       - The middleware touches last_seen_at at most every 10 min, piggybacking on [SEC-14](#sec-14)'s ValidateSessionInfo, which also returns last_seen.
       - ListSessions(ctx, userID, currentRaw) returns []SessionInfo{ID: first 12 hex of token_hash, CreatedAt, LastSeenAt, Device: uaSummary(ua) such as 'Chrome on Windows' or 'Safari on iPhone', IP, Current bool}.
       - RevokeSession(ctx, userID, id): DELETE … WHERE user_id=? AND substr(token_hash,1,12)=?.
       - RevokeOtherSessions(ctx, userID, currentHash).
    3. Routes:
       - GET /api/v1/me/sessions, DELETE /api/v1/me/sessions/{id} and POST /api/v1/me/sessions/revoke-others: rt.user with ext(), like the other /me routes.
       - POST /api/v1/users/{id}/sessions/revoke: rt.admin, calls RevokeUserSessions.
    4. UI:
       - A 'Signed-in devices' modal, opened from the UserLayout account menu (next to 'Audiobook password') and the staff Sidebar user block. It lists device, last seen and 'this device', with a per-row 'Sign out' and a 'Sign out other devices' button, in the existing modal style.
       - Settings → Users → EditUserModal gets a 'Sign out everywhere' button.
       - If APP ships an Account page, it hosts this panel instead.
  - **Files:** `internal/store/migrations/0090_session_activity.sql (renumber to next free)`, `internal/auth/service.go`, `internal/auth/service_test.go`, `internal/httpapi/auth.go`, `internal/httpapi/sessions.go (new)`, `internal/httpapi/routes_user.go`, `internal/httpapi/routes_admin.go`, `internal/httpapi/testdata/routes.golden`, `web/src/lib/api.ts`, `web/src/components/UserLayout.tsx`, `web/src/components/Sidebar.tsx`, `web/src/components/SessionsModal.tsx (new)`, `web/src/pages/Settings.tsx`
  - **Acceptance:**
    - /me/sessions lists your devices, with the current one marked.
    - 'Sign out other devices' leaves the current session working and signs the others out on their next request (they land on Login via SEC-14).
    - An admin's 'Sign out everywhere' signs that user out on every device.
    - IPs are shown only to the session's own user. The admin endpoint returns no session details.
  - **Tests:** Go auth: TestRevokeOthersKeepsCurrent, TestRevokeSessionScopedToUser (can't revoke another user's id prefix), TestLastSeenThrottled (fake clock).; Go httpapi: route-walk covers the new routes; regenerate the golden.; UI check: sign in on two browsers, revoke one from the other.
  - **Depends on:** [SEC-14](#sec-14), [SEC-11](#sec-11), [SEC-02](#sec-02), APP
  - **Risk:** Storing the IP is personal data. It's shown only to its owner and never in admin views. Migrations run automatically on update, so take a DB backup first if SAFE's pre-migration snapshot hasn't landed. APP: placement only; this doesn't block.
  - **Resolves:** system-6

#### Risks

- Locking 77 routes to staff (SEC-02) can break a requester page if the allowlist misses a call. Mitigations: the import-graph reach check of every requester page's api.* calls, the golden route table, the route-walk test, and a LAN click-through as a requester and as a read-only account before pushing.
- Role tightening (SEC-09, SEC-08) removes abilities from any existing manager accounts. Confirm with the owner first.
- SEC-07 changes every grab and blocklist call in one commit. A missed caller fails silently as a 400, so grep web/src for download_url (expect zero hits) and click through every search modal. Open modals stop working across a restart (410 'search again').
- Changing forwarded-IP parsing (SEC-11) affects LAN-vs-external classification behind cloudflared or a local TLS proxy. The table tests pin it, but verify through the real tunnel after deploying.
- The log scrub (SEC-04) rewrites persisted log files once at boot. It must be atomic and must never block startup, so on failure it warns and continues. Copies already in docker's own stdout log survive until the container is recreated.
- Path confinement (SEC-01, SEC-06) can block an owner who imports from an ad-hoc mount, or who keeps a library on an unusual path. The errors name the fix (add the folder in Settings → Library).
- The tag-based book filter (SEC-05) will both miss some explicit books and could hide a borderline title. It blocks only on clear tags, and the admin's exact lookup stays unfiltered.
- Several tasks touch internal/httpapi/server.go and routes. Land SEC-02 early, and rebase the others onto the router rather than editing server.go in parallel.

#### Out of scope

- Password change, password reset (CLI or env), case-insensitive usernames and emails, request quotas and the Plex auto-approve default: the rest of system-6, owned by APP/REQ.
- Apprise presets, templates, delivery queue and retries, stream filters and the buffering cooldown: the rest of insights-7, owned by INT/PLEX.
- Using the websocket to replace the 3-second polls: the second half of backend-4, owned by OBS/FE.
- Live per-folder checks in setup and Settings → Library (exists, writable, hardlink probe, free space): the rest of system-10, owned by CFG.
- A strict Content-Security-Policy, which needs a nonce/hash pass over inline styles first.
- Multi-factor authentication and passkeys.
- Passing Apprise URLs to the CLI over stdin. The '--' separator and the leading '-' check already block argument injection.
- Database backups and pre-migration snapshots (SAFE).
- Deleting the dead Requests.tsx page: flagged for FE/COPY cleanup, harmless once its routes are staff-only.

