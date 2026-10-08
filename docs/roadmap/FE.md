# FE — Frontend foundation & design system

_Part of the [Arrmada roadmap](../../ROADMAP.md). 32 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Give the web UI one shared foundation: complete design tokens, a component kit, a data router with lazy routes, error and session recovery, URL-addressable state, a data layer fed by the websocket, and a small precompressed bundle. Every page should then look, load, recover and behave the same way, and a fix made once reaches every page.

**Why.** Every page in the frontend builds its own versions of the same parts, and users see the results:
- Two tokens used about 75 times are never defined (frontend-1). Every 'Wanted'/'Pending'/'Held' chip has no fill and no modal has a shadow. The most-used secondary text colour fails contrast in both themes (frontend-11).
- There are 34 hand-built overlays, 16 toast copies, 13 window.confirm calls and 56 pasted gradients (frontend-2). Only 3 dialogs have role=dialog, so Esc, focus and scrolling behave differently from modal to modal.
- A tab or installed PWA left open past its session shows red 'authentication required' banners on every panel, with no way back to sign-in (backend-13, system-6, frontend-9). One render exception blanks the whole app, and Login always throws away the page you were on.
- Requesters on phones download one uncompressed 915 KB bundle that includes every admin page. The service worker keeps every old build forever (frontend-7, walk-10).
- Tabs, filters, the Quality editor and scroll position live in component state (frontend-3, frontend-6, frontend-12). Back leaves the page, a refresh resets it, and a sidebar click silently throws away a half-built quality profile.
- About 29 timers poll every 1.5-30 s even in hidden tabs, while the realtime websocket goes mostly unused (frontend-14, backend-4).
- The nav groups, breadcrumbs, page widths and library toolbars disagree from page to page (frontend-10, product-8, walk-3, walk-8, walk-9). The nav has no counts, so nothing tells the owner that something needs them (product-4).
- ARRMADA_BASE_URL is advertised by compose and the scripts but produces a blank page (frontend-8, system-15). The theme choice is forgotten on every reload (frontend-15).

**Depends on:** SEC — per-role websocket topic filtering in internal/realtime/hub.go: [FE-26](#fe-26) publishes queue.changed as a staff-only topic, and [FE-27](#fe-27) mounts LiveProvider in the requester shell only after this lands. SEC's session work (sliding sessions, revoke on password change) feeds [FE-02](#fe-02)'s sign-out path. If SEC adds a CSP, [FE-05](#fe-05)'s inline theme script needs a hash.; OBS (or whichever epic owns product.t7) — GET /api/v1/attention counts and an attention.changed topic, for [FE-28](#fe-28)'s sidebar badges.; REQ — routed /requests page (product.t14) and request.* topics: [FE-16](#fe-16) and [FE-28](#fe-28) add the nav item and badge, and [FE-27](#fe-27) subscribes the Discover strip and NotificationBell. REQ also deletes the dead pages/Requests.tsx.; ACQ — merged /activity page (product.t30) collapses [FE-16](#fe-16)'s Activity group into one entry. The download snapshot (backend.t24) lets [FE-26](#fe-26) avoid a second qBittorrent poll.; BE — job runner job.updated topic (backend.t18), so [FE-27](#fe-27) can move Convert and Subtitles off polling. BE also owns gzip of API JSON.; CFG — Settings hub: replaces the Settings tabs ([FE-23](#fe-23) keeps the ?tab= redirects) and the Settings unsaved guard ([FE-25](#fe-25)) with auto-save, uses ui/Page and the kit, and owns the connection-card confirm copy that [FE-13](#fe-13) drafts.; APP — requester shell (bottom tab bar, safe areas, PNG icons, Discover detail-sheet routes) shares UserLayout and Discover with [FE-02](#fe-02), [FE-03](#fe-03), [FE-05](#fe-05), [FE-06](#fe-06), [FE-24](#fe-24) and [FE-32](#fe-32). [FE-10](#fe-10)'s overflow spec is test.fail() until APP's header fix lands. APP must use [FE-09](#fe-09)'s zIndex tokens and Modal sheet variant.; MOV, SER, BOOK, MUS, QUAL, CONV, SUB, PLEX, AUD — their pages are migrated by [FE-11](#fe-11) to [FE-15](#fe-15) and [FE-19](#fe-19). Once [FE-09](#fe-09) lands they should build new UI from web/src/ui and rebase onto the one-page-per-commit migrations.

#### Design

## Target shape of web/src

```
main.tsx
  <ErrorBoundary root>                         [FE-03](#fe-03)
    <MeProvider>  state: loading | unreachable | signed-out | ready   [FE-02](#fe-02)/03
      <ToastProvider><ConfirmProvider>         [FE-09](#fe-09)
        <App/> -> SetupGate (staff) -> <RouterProvider router={buildRoutes(role, external)} />   [FE-22](#fe-22) (basename = BASE, [FE-29](#fe-29))
          AppLayout | UserLayout
            <LiveProvider/>  (one socket per tab)                   [FE-27](#fe-27)
            useScrollMemory(mainRef)                                [FE-24](#fe-24)
            <ErrorBoundary resetKey={pathname}><Suspense fallback={<PageSkeleton/>}><Outlet/>   [FE-03](#fe-03)/06
```

**lib/** holds logic and providers only:
- base.ts: BASE, url().
- api.ts: one send() for req() and the raw uploads; ApiError {status}; dispatches the 'arrmada:signed-out' event on a 401 outside /api/v1/auth/.
- session.ts: next-path remember and sanitize.
- me.tsx, routes.tsx (buildRoutes, ModuleGate).
- query.ts: useQuery with a bounded cache that is cleared on sign-out.
- live.tsx: LiveProvider, useLiveEvent, useLiveQuery.
- usePoll.ts: pauses when the tab is hidden; setTimeout chain, so polls never overlap.
- useTabParam.ts, useSearchState.ts, useScrollMemory.ts, useUnsavedGuard.ts, useMediaQuery.ts.
- theme.ts, format.ts.
- nav.ts: groups with icon, module and badge per item, plus crumbFor().

**ui/** is the presentational kit; it makes no API calls. Barrel export in ui/index.ts. Components:
- Button, IconButton (label is required).
- Modal (dialog | sheet; portal, focus trap, Esc closes the topmost only, a scroll-lock counter, inner scroll).
- Confirm (useConfirm returns a Promise of boolean), Toast (useToast, one aria-live region), Menu.
- StatusChip (tone = accent | good | avoid | reject | faint), Tabs (bound to ?tab=).
- Field, Input, Select, Textarea, Switch.
- Skeleton, ErrorState, EmptyState, PageSkeleton.
- Page (width default 1240 | wide 1600; form column 760, left-aligned).
- LibraryToolbar, FilterBar.

**features/** holds shared domain composites: library/LibraryGrid (Movies and Series as configs) and discover/* (Hero, PosterRow, PosterCard, DetailSheet, rowRegistry, shared with BooksDiscover).

**pages/** become thin route components.

## Tokens and visual rules
- index.css is the only place colours live. All four theme blocks declare the same set of names. scripts/check-tokens.mjs runs as prebuild and fails the build on an undefined token or a block that is missing a name.
- Tailwind aliases every token:
  - colors, including the text-safe --accent-text, --good-text, --avoid-text and --reject-text;
  - boxShadow.panel and backgroundImage.accent-grad;
  - zIndex: header 30, tabbar 40, drawer 50, modal 60, toast 70;
  - maxWidth: page, page-wide, form;
  - fontSize scale ([FE-30](#fe-30)): 2xs 10.5, xs 11, sm 12, base 12.5, md 13.5, lg 15, xl 17, 2xl 20, 3xl 24, display 30/38.
- No new inline style={{ var(--x) }} where a class exists. After [FE-30](#fe-30) there are no arbitrary text-[Npx] sizes.
- Every FE task renders the same as before, except:
  - [FE-01](#fe-01) (contrast token values only) and [FE-30](#fe-30) (type scale) need the owner's sign-off on screenshots;
  - [FE-16](#fe-16) (nav regroup) and [FE-18](#fe-18) (widths) show before/after screenshots.
- The palette stays the dark warm theme with the terracotta accent.

## Page anatomy
- `<PageHeader title tail?>`: the crumb comes from nav.ts.
- `<Page width>`.
- Optional `<Tabs>` bound to ?tab=.
- Content renders as Skeleton (no data, loading) → ErrorState with Retry (no data, error) → EmptyState (empty) → content. If an error arrives while data is already shown, a thin stale banner appears.

## URL conventions
| What | Where |
|---|---|
| Tab | ?tab=<key> (push, so Back steps through tabs) |
| Quality media | /quality?media=movie, plus editor routes /quality/:media/new, /quality/:media/:key and /quality/:media/:key/copy |
| Library filter/search | ?filter=missing&q=dune (replace; q debounced 300 ms; defaults omitted) |
| Discover | ?tab=&q= kept in the URL (no longer cleared) |
| View/sort prefs | localStorage via usePersisted (remembered preference, not URL) |
| Legacy | /notifications → /insights?tab=notifications; /activity and /library redirects kept |

## Session and error rules
- Only a 401 from a non-/api/v1/auth/ path signs the user out. A 403 (role, externalGate) never does.
- After sign-out, Login shows 'You were signed out' and then returns to the sanitized next path. A next path must start with '/' and must not start with '//' or '/\'.
- A boot failure that is not a 401 shows 'Can't reach Arrmada' with automatic retry, not the login form.
- A root error boundary plus a per-layout boundary keyed by pathname. A chunk-load error reloads once, guarded by a timestamp in sessionStorage.

## Data and live updates
- useQuery(key, fetcher) returns {data, error, loading, refetch, mutate}. It dedupes in-flight requests, renders cached data at once on remount and revalidates in the background. The cache is LRU-bounded and is cleared on sign-out, so a shared device never shows the previous user's data.
- useLiveQuery adds {topics, fallbackMs = 30 s}. A matching websocket event triggers a debounced refetch (500 ms). There is a slow fallback poll, and everything pauses while document.hidden.
- Topics:
  - existing: release.grabbed, movie.downloaded, series.imported, book.imported, music.imported, download.imported, import.held, library.scanned, movie.*, series.renamed, file.removed;
  - new from [FE-26](#fe-26): queue.changed (per-hash progress and state);
  - from other epics: job.updated (BE), request.* (REQ), attention.changed (attention feed).
- Requester shells subscribe only after SEC's per-role topic filtering lands.

## Serving and caching
- `npm run build` runs: prebuild check-tokens → tsc → vite build → scripts/compress.mjs (.br q11 and .gz l9 for assets over 1 KB; stamps the build id into dist/sw.js) → scripts/check-size.mjs (requester first load ≤350 KB raw / ≤110 KB br).
- internal/webui:
  - newHandler(fs, built) negotiates br or gzip with ServeContent, sets the correct Content-Type and Vary, and keeps immutable caching for assets;
  - a missing assets/* path returns 404 instead of index.html;
  - index.html stays no-cache.
- The service worker uses the cache 'arrmada-<build>'. activate deletes the others. It is cache-first only for /assets/* with JS/CSS/image/font responses and network-first for everything else.

## Sub-path ([FE-29](#fe-29), if the owner keeps the option)
- The server templates index.html with `<base href="{base}/">` and `<meta name="arrmada-base" content="{base}">`. A meta tag rather than an inline script keeps it CSP-friendly.
- vite base is './'.
- Rule: the server emits base-less root-relative paths. The client adds the base through url(), and the service worker adds it from registration.scope.

## Quality gates
The CI web job runs npm run lint (eslint 9 with typescript-eslint, react-hooks and jsx-a11y; confirm/alert banned; 'fixed inset-0' banned outside ui/), then npm test (vitest, jsdom), then npm run build, then Playwright (chromium, mocked API, phone-width overflow and smoke specs). The Go side gets embed handler tests and queue watcher tests. Race tests run in Docker before pushing.

## Migration plan
1. Fix the visible breakage with small, independent changes (M1).
2. Shrink and quiet the app (M2).
3. Land the tooling and kit, then migrate one page per commit (M3). Once [FE-09](#fe-09) lands, module epics must build new UI from ui/.
4. Unify nav and layout (M4).
5. Move to the data router and URL state (M5).
6. Switch to live updates (M6).
7. Handle the sub-path option and the big consolidations (M7).

#### Milestone: M1 — Nothing visibly broken

_Chips, banners and modals render with their fills and shadows, and secondary text passes contrast. A session that ends lands on sign-in with a 'signed out' note and returns to the same page. A render error or a server outage no longer blanks the app. The bundle travels about 4x smaller, and the service worker can't serve stale or HTML-as-JS chunks. The theme choice is remembered._

<a id="fe-01"></a>
- [ ] **FE-01 · Define the missing tokens, alias every token in Tailwind, fix text contrast, and fail the build on undefined tokens** — `P1` · `S` · Phase 2
  - **Problem:** index.css has four token blocks (:8-77), and none of them defines --shadow (53 uses) or --avoid-soft (22 uses).
- The Wanted/Pending/Held chips, the Movies/Series 'Metadata not configured' banner and Insights' 'Not connected' pill render with no fill.
- Modals, popovers and the Login card (Login.tsx:67) have no shadow.
- Discover's sheet loses even its sm:shadow-2xl: the inline boxShadow var(--shadow) at Discover.tsx:1253 wins the cascade and computes to none.

tailwind.config.js aliases only 10 tokens, so pages fall back to inline style var() with ad-hoc fallbacks (Dashboard.tsx:74-75, MovieDetail.tsx:580, Insights.tsx:34).

Contrast:
- --ink-faint (about 625 uses) measures 4.07:1 on the dark panel and 2.75-3.29:1 in the light theme.
- Light-theme accent, good and avoid text measures 3.0-4.3:1.
- Dark --reject #cf5442 is 3.99:1 on panel.
  - **Approach:** 1) web/src/index.css, in all four blocks (:root, the @media light :root, :root[data-theme=dark] and :root[data-theme=light]):
    - --shadow: dark '0 18px 48px rgba(0,0,0,.45), 0 2px 8px rgba(0,0,0,.3)'; light '0 14px 36px rgba(60,40,20,.14), 0 2px 6px rgba(60,40,20,.08)'.
    - --avoid-soft: dark rgba(224,169,59,.15); light rgba(176,125,30,.12). Add --under-soft and --mismatch-soft in the same way.
    - Contrast, with ratios verified with the WCAG formula; write them in a comment beside each value:
      - dark --ink-faint #8c7a67 -> #9a8774: 5.35 on bg, 4.87 on panel, 5.19 on panel-2;
      - light --ink-faint #9c8b78 -> #776654: 4.77 on bg, 5.51 on panel, 4.60 on panel-2.
    - New text-safe status tokens --accent-text, --good-text, --avoid-text and --reject-text:
      - dark: #db7a54, #7fb069, #e0a93b and #e27a69. The base #cf5442 is only 3.99 on panel; #e27a69 gives 5.77 on panel and 4.90 on --reject-soft.
      - light: #a94e2d, #4a7539, #8a6112 and #b23f30, each ≥4.5 on panel-2 and on its *-soft fill.
    - The bg, panel and accent values and every hue stay unchanged.
    
    2) tailwind.config.js:
    - colors: alias every token — line-soft, accent-ink, accent-soft, accent-line, good, good-soft, avoid, avoid-soft, reject, reject-soft, under, under-soft, mismatch, mismatch-soft and the four *-text tokens.
    - boxShadow {panel:'var(--shadow)'}.
    - backgroundImage {'accent-grad':'linear-gradient(150deg, var(--accent), var(--accent-deep))'}.
    
    3) Switch the high-traffic status-text sites to the *-text tokens now: the status pills on MovieDetail, SeriesDetail and BookDetail, the Discover caption detail lines, and the Dashboard warnings. The rest move during the kit migration ([FE-11](#fe-11) to [FE-14](#fe-14)).
    
    4) Remove the inline fallbacks: Dashboard.tsx:74-75, MovieDetail.tsx:580, Insights.tsx:34 and any 'var(--avoid, #…)'. At Discover.tsx:1252-1253, keep a single shadow source: the shadow-panel class, with the inline style removed.
    
    5) Add web/scripts/check-tokens.mjs (plain node, no dependencies).
    - It parses the '--x:' declarations per block in src/index.css and every var(--x) in src/**/*.{ts,tsx,css}.
    - It exits 1, listing (a) names referenced but never defined and (b) names missing from any of the four blocks.
    - Wire it as 'prebuild' in web/package.json so CI's npm run build fails on a bad token.
  - **Files:** `web/src/index.css`, `web/tailwind.config.js`, `web/scripts/check-tokens.mjs`, `web/package.json`, `web/src/pages/Dashboard.tsx`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/SeriesDetail.tsx`, `web/src/pages/BookDetail.tsx`, `web/src/pages/Insights.tsx`, `web/src/pages/Discover.tsx`
  - **Acceptance:**
    - In both themes, the 'Wanted' pill on a movie with no file has an amber fill, and the Movies 'Metadata not configured' banner and the Reviews 'Held' tag are tinted.
    - Modals, popovers, the Login card and the Discover sheet show a visible shadow.
    - npm run build fails when any file references var(--nope) or a block lacks a token, and it passes on the fixed tree.
    - --ink-faint and the four *-text tokens measure ≥4.5:1 on --bg, --panel and --panel-2 in both themes. The ratios are recorded in comments.
    - The palette hues and the bg/panel/accent values are unchanged.
  - **Tests:** check-tokens.mjs runs in CI through prebuild. Locally, add var(--nope) to one file and confirm the build fails.; Manual check in the light and dark themes: MovieDetail Wanted pill, Reviews Held tag, Insights pill, Discover sheet shadow, Login card.; Contrast spot-check in the DevTools accessibility pane on Dashboard and on the Discover captions.; Owner reviews before/after screenshots of Dashboard, Discover and the Movies table in both themes.
  - **Risk:** Lighter secondary text slightly changes how dense pages read. The hues stay warm and unchanged; get the owner's OK on the screenshot pairs before pushing.
  - **Resolves:** frontend-1, frontend-11
<a id="fe-02"></a>
- [ ] **FE-02 · Global sign-out handling: a 401 shows sign-in with 'You were signed out' and returns you to the same page** — `P1` · `S` · Phase 2
  - **Problem:** req() in api.ts (:1053-1068) throws on any non-OK response and has no 401 branch. Nothing else in web/src handles 401 either.

After a session expires (the TTL is a fixed 30 days), an admin disables a user, or a password changes, an open tab or installed PWA shows red 'authentication required' banners on every panel and poll until someone reloads by hand. A reload does reach Login, but Login always sends you to /discover (Login.tsx:32/46), so the deep link is lost.

The two raw fetch() uploads (api.ts:1243 audioserver import, :1464 book cover) bypass req() entirely.
  - **Approach:** 1) web/src/lib/api.ts
    - Add `export class ApiError extends Error { status: number; path: string }`.
    - Add one internal send(path, init) that req() and the two raw uploads (api.ts:1243, :1464) all go through. It keeps today's message parsing from the JSON body.
    - On res.status === 401, when the path does not start with '/api/v1/auth/' (login, setup, Plex PIN poll and me legitimately return 401), call signalSignedOut().
      - signalSignedOut() dispatches window CustomEvent 'arrmada:signed-out' once, guarded by a module flag. resetSignedOut() clears the flag after a login.
    - Throw ApiError in every error case.
    
    2) web/src/lib/session.ts (new)
    - rememberNext() stores location.pathname + search in sessionStorage 'arrmada.next', inside try/catch.
    - sanitizeNext(s) returns s only if it starts with '/' and not with '//' or '/\\', and contains no scheme; otherwise null.
    - takeNext() reads, sanitizes and clears the value.
    
    3) web/src/lib/me.tsx
    - Add signedOut: boolean to the context.
    - Listen for the event: rememberNext(), setUser(null), setSignedOut(true).
    
    4) App.tsx: the !user branch renders <Login signedOut={signedOut}/>.
    
    5) web/src/pages/Login.tsx
    - When signedOut is set, show a quiet note in the existing style (ink-dim text on a panel-2 box, not red): 'You were signed out. Sign in again to carry on.'
    - Also accept ?next= from the URL, through sanitizeNext.
    - After a password, setup or Plex success, call window.location.replace(takeNext() ?? '/discover'). The full load re-runs MeProvider and /status, as today.
    
    6) web/src/lib/useLive.ts: on the event, set closed=true and close the socket, so it doesn't reconnect-loop against a 401 upgrade.
    
    7) Polls stop by themselves, because the layouts unmount.
  - **Files:** `web/src/lib/api.ts`, `web/src/lib/session.ts`, `web/src/lib/me.tsx`, `web/src/lib/useLive.ts`, `web/src/App.tsx`, `web/src/pages/Login.tsx`
  - **Acceptance:**
    - Delete your session row, or change the password from another device. The next action or poll shows the login screen with the signed-out note and no error banners.
    - Signing back in returns to the same URL (e.g. /movies/12), not /discover.
    - A wrong password shows the inline error, with no signed-out note and no redirect loop.
    - ?next=//evil.example and ?next=https://evil.example land on /discover.
    - A requester PWA resumed after its session expired shows the login screen, not broken Discover rows.
    - The two upload helpers also trigger the sign-out on a 401.
  - **Tests:** vitest (added when FE-08 lands; sanitizer kept as a pure function):
- sanitizeNext: '/movies/12' passes; '//evil', '/\\evil' and 'https://x' are rejected; empty input returns null.
- req(): dispatches the event exactly once for two concurrent 401s on /api/v1/movies, and never for /api/v1/auth/login.; Manual on a scratch instance (never the owner's DB): sqlite3 DELETE FROM sessions, then click around; repeat after a password change from another browser.; npm run build passes.
  - **Risk:** A misplaced 401 would sign people out unexpectedly. The server uses 403 for role failures (requireRole) and for externalGate, and 401 only from protected() and the auth handlers, so only real session loss triggers this. SEC's session work (sliding sessions, revoke on password change) will make this path fire more often by design; that is intended.
  - **Resolves:** backend-13, system-6, frontend-9
<a id="fe-03"></a>
- [ ] **FE-03 · Error boundaries, chunk-load recovery, and a 'Can't reach Arrmada' boot state** — `P1` · `S` · Phase 2
  - **Problem:** There is no ErrorBoundary anywhere. One render exception, for example a field arriving as null, unmounts the whole app and leaves a blank page.

MeProvider (me.tsx:27-35) treats every boot failure as 'signed out'. A transient network error, a restarting container or a 502 from the reverse proxy therefore shows the login form instead of 'server unreachable'. Once FE-06 splits the code, a deploy will also make old index.html files reference chunks that no longer exist.
  - **Approach:** 1) web/src/components/ErrorBoundary.tsx
    - A class component using getDerivedStateFromError, with console.error in componentDidCatch.
    - Props: resetKey (the boundary resets when it changes) and an optional fallback.
    - The fallback card uses the current style: panel, line border, rounded-2xl, accent-gradient Reload button.
      - Heading: 'Something broke on this page'.
      - The error message and stack inside <details>.
      - Buttons: Reload, and Go home (href '/', which redirects requesters to /discover).
    
    2) Chunk-load recovery
    - If the error message matches /Failed to fetch dynamically imported module|Importing a module script failed|error loading dynamically imported module|ChunkLoadError/ and the sessionStorage key 'arrmada.chunkReload' is missing or more than 10s old: write the timestamp, then call location.reload().
    - Otherwise show the fallback.
    
    3) Wiring
    - main.tsx: wrap <MeProvider><App/></MeProvider> in a root ErrorBoundary.
    - AppLayout.tsx and UserLayout.tsx: wrap <Outlet/> as <ErrorBoundary resetKey={location.pathname}>, so the sidebar and top bar survive and navigating away recovers.
    
    4) Boot state in me.tsx
    - If api.me() rejects with ApiError status 401 (from [FE-02](#fe-02)), set user to null, so Login shows.
    - Any other failure (TypeError from fetch, 5xx, a 502 from a proxy) sets unreachable=true.
    - App renders components/Unreachable.tsx: 'Can't reach Arrmada', a Retry button, and 'Retrying…'.
      - It retries me()+status() with backoff (5s, 10s, 20s, then every 30s) and on the window 'online' event.
      - It boots normally on success.
  - **Files:** `web/src/components/ErrorBoundary.tsx`, `web/src/components/Unreachable.tsx`, `web/src/main.tsx`, `web/src/App.tsx`, `web/src/lib/me.tsx`, `web/src/components/AppLayout.tsx`, `web/src/components/UserLayout.tsx`
  - **Acceptance:**
    - A thrown error in one page (a temporary dev throw) shows the fallback inside the layout. The sidebar still works, and navigating to another page renders it normally.
    - Stop the server and reload: 'Can't reach Arrmada' appears, not the login form. The app recovers by itself within 30s of the server coming back.
    - A simulated chunk-load error reloads exactly once, with no reload loop.
    - A 401 at boot still shows Login.
  - **Tests:** vitest (after FE-08):
- ErrorBoundary renders the fallback for a throwing child and resets when resetKey changes.
- The chunk-error matcher and the reload guard respect the 10s window.; Manual: a dev-only throw, the server stopped and restarted, and a requester PWA on a phone in airplane mode.
  - **Depends on:** [FE-02](#fe-02)
  - **Risk:** Low. The reload guard must use the timestamp so a permanently missing chunk cannot loop; FE-04's asset 404 makes that case fail fast.
  - **Resolves:** frontend-9
<a id="fe-04"></a>
- [ ] **FE-04 · Serve precompressed assets, 404 missing assets, and version the service-worker cache per build** — `P1` · `S` · Phase 2
  - **Problem:** internal/webui/dist/assets/index-*.js is 915,596 bytes. embed.go serves it through http.ServeFileFS, and the handler chain at server.go:485 has no compression anywhere; gzip would cut it to about 225 KB. Requesters on phones outside the LAN pay the full size after every deploy.

Two latent bugs would bite as soon as the bundle is split (FE-06):
1. A request for a missing /assets/*.js falls back to index.html with status 200 (embed.go:45-48).
2. sw.js caches every same-origin GET cache-first under the fixed name 'arrmada-v1', with no content-type check. Because sw.js never changes, activate never runs again: old hashed bundles pile up forever, and a stale chunk URL would get HTML cached as JavaScript.
  - **Approach:** 1) web/scripts/compress.mjs (node:zlib, no new dependency)
    - After vite build, write .br (brotliCompressSync, quality 11) and .gz (gzipSync, level 9) next to every dist file over 1 KB with extension .js, .css, .svg or .webmanifest.
    - Stamp dist/sw.js: replace the __BUILD__ placeholder with the first 10 hex characters of a sha256 of dist/index.html, which already contains the hashed asset names.
    - package.json build: 'tsc --noEmit && vite build && node scripts/compress.mjs'. The Dockerfile already runs npm run build on node:20, which has brotli.
    
    2) internal/webui/embed.go
    - Refactor Handler() into newHandler(sub fs.FS, built bool), so it can be tested.
    - (a) A path under assets/ that doesn't exist returns 404 with no-store. It no longer falls back to index.html; other paths still do, for SPA routes.
    - (b) For an existing file, pick br if Accept-Encoding allows it, else gzip, if name+'.br' or name+'.gz' exists. Then:
      - set Content-Type from mime.TypeByExtension(path.Ext(name));
      - set Content-Encoding and Vary: Accept-Encoding;
      - keep the immutable Cache-Control for assets/;
      - serve with http.ServeContent(w, r, name, time.Time{}, f.(io.ReadSeeker)). embed files implement Seek. ServeFileFS on a .br file would set the wrong type.
    - (c) index.html and sw.js stay uncompressed and no-cache.
    - A missing sibling falls back to the identity file.
    
    3) web/public/sw.js
    - const CACHE = 'arrmada-__BUILD__'.
    - activate deletes every other cache starting with 'arrmada-'. It now actually runs on each deploy, because sw.js changes byte for byte.
    - Cache-first only for /assets/*, and only when res.ok and the content-type is JS, CSS, an image or a font.
    - Never cache a text/html response for a non-navigation request.
    - Everything else is network-first. Navigations keep the network-first index.html shell fallback.
    
    4) API JSON gzip is left to BE.
  - **Files:** `web/scripts/compress.mjs`, `web/package.json`, `web/public/sw.js`, `internal/webui/embed.go`, `internal/webui/embed_test.go`
  - **Acceptance:**
    - curl -sI -H 'Accept-Encoding: br' http://host:7878/assets/index-<hash>.js shows Content-Encoding: br, a JavaScript Content-Type and Vary: Accept-Encoding. The transfer is ≤ about 200 KB.
    - Without Accept-Encoding the same file is served uncompressed with the right type.
    - /assets/does-not-exist.js returns 404, while /movies/12 still returns index.html with no-cache.
    - After two deploys, DevTools → Application → Cache Storage holds exactly one arrmada-* cache, and the old 'arrmada-v1' is gone.
  - **Tests:** Go tests in internal/webui/embed_test.go against an fstest.MapFS, so they don't depend on the gitignored dist:
- TestServesBrotliWhenAccepted
- TestFallsBackToGzip
- TestIdentityWithoutAcceptEncoding (checks Vary and Content-Type)
- TestMissingAsset404
- TestSPARouteServesIndexNoCache
- TestNoSiblingServesIdentity; Run the race tests in Docker before pushing.; Manual: load the app, rebuild, load again, and inspect Cache Storage and the Network tab's transfer sizes.
  - **Risk:** The first deploy replaces the 'arrmada-v1' worker. Navigations are already network-first, so the new index.html loads and the new sw.js installs and prunes. Verify this once on a phone PWA. A reverse proxy that already compresses is fine, and Vary keeps its caches correct.
  - **Resolves:** walk-10, frontend-7
<a id="fe-05"></a>
- [ ] **FE-05 · Remember the theme (System / Light / Dark), give requesters the switch, and sync the browser chrome colour** — `P3` · `S` · Phase 16
  - **Problem:** toggleTheme (Sidebar.tsx:8-14) only stamps data-theme on <html>. Nothing persists it, so every reload reverts to the OS preference.
- The button always shows a moon.
- UserLayout has no theme control at all.
- <meta name=theme-color> is fixed at #1a1310, so the light theme still gets a dark browser and PWA bar.
  - **Approach:** 1) web/src/lib/theme.ts
    - type ThemePref = 'system' | 'light' | 'dark', stored in localStorage 'arrmada.theme'. Reads and writes go through try/catch, like lib/persist.ts.
    - apply(pref): 'system' removes data-theme; otherwise set it. Then set meta[name=theme-color] content to the computed --sidebar value.
    - A matchMedia('(prefers-color-scheme: light)') change listener re-applies the meta while the pref is 'system'.
    - useTheme() returns [pref, setPref].
    
    2) web/index.html: a five-line inline script in <head>, before the stylesheet. It reads the pref inside try/catch and sets data-theme to light or dark before first paint, so there's no flash.
    
    3) Sidebar.tsx: a three-state button cycling System -> Light -> Dark.
    - Monitor, sun and moon icons, drawn in the existing inline-SVG style.
    - aria-label and title 'Theme: Light' (etc.).
    
    4) UserLayout.tsx avatar menu: a 'Theme' row with a three-segment control in the menu's existing styling.
    
    5) manifest theme_color stays dark, since the manifest can't be dynamic. The meta tag covers in-browser chrome.
    
    6) Server-side per-user storage is out of scope.
  - **Files:** `web/src/lib/theme.ts`, `web/index.html`, `web/src/components/Sidebar.tsx`, `web/src/components/UserLayout.tsx`
  - **Acceptance:**
    - Choose Light and reload: the page stays light, with no dark flash.
    - The theme icon matches the active choice.
    - A requester can switch the theme from the avatar menu.
    - In the light theme the browser/PWA status bar turns light.
    - 'System' follows OS changes live.
    - With storage blocked, the switch still works for the session and nothing throws.
  - **Tests:** vitest (after FE-08): persistence round-trip, the blocked-storage fallback, and system-mode updates via a mocked matchMedia.; Manual: reload and check for a flash in both shells and in the installed PWA.
  - **Risk:** Low. If SEC later adds a Content-Security-Policy, the inline boot script needs its sha256 in script-src. Leave a comment beside it.
  - **Resolves:** frontend-15, system-15

#### Milestone: M2 — Lighter and quieter

_Requesters download only their own pages (first load ≤350 KB raw / ≤110 KB br, enforced at build time). Hidden tabs make no periodic API calls, and polls never overlap._

<a id="fe-06"></a>
- [ ] **FE-06 · Split the bundle by route so requesters download only their own pages, with a size budget** — `P2` · `M` · Phase 3
  - **Problem:** App.tsx statically imports all 30 pages, and there is no lazy() or dynamic import anywhere. Every requester downloads Quality (1,475 lines), Convert, Insights, Settings, Subtitles and every other admin page in one 915 KB file. That includes external requesters limited to Discover, Books and Audiobooks.
  - **Approach:** 1) web/src/lib/lazyPage.ts
    - lazyPage(() => import('./pages/Quality'), 'Quality') maps the named export to default for React.lazy.
    - Use it for every page in App.tsx.
    - Keep Login, SetupGate, both layouts and Unreachable eager.
    - Make SetupWizard lazy too, since only admins see it, on first run.
    
    2) AppLayout.tsx and UserLayout.tsx
    - Inside [FE-03](#fe-03)'s per-layout ErrorBoundary, wrap <Outlet/> in <Suspense fallback={<PageSkeleton/>}>.
    - The sidebar and top bar stay put while a page loads.
    - components/PageSkeleton.tsx: a header bar plus three panel-coloured blocks in today's tokens.
    
    3) Discover.tsx: lazy-load BooksDiscover, so the Books tab's code loads only when chosen.
    
    4) Audiobooks.tsx: move the admin-only tabs (server, people, listening, import) into pages/AudiobooksAdmin.tsx and import it lazily. The requester chunk then carries only the 'you' tab.
    
    5) vite.config.ts: build.rollupOptions.output.manualChunks = { vendor: ['react', 'react-dom', 'react-router-dom'] }, so the vendor hash survives app-only deploys. Add rollup-plugin-visualizer behind an env flag (dev dependency only) to check the requester graph.
    
    6) web/scripts/check-size.mjs, run after compress.mjs in npm run build
    - Print a table of per-chunk raw and br sizes.
    - Compute the requester first load from dist/.vite/manifest.json (enable build.manifest): entry + vendor + the Discover chunk + their static imports.
    - Fail if that exceeds 350 KB raw or 110 KB br. If the first measurement is lower, set the budget to the measured value + 10%.
    
    7) After a staff user signs in, prefetch the Movies, Series and Downloads chunks in requestIdleCallback.
  - **Files:** `web/src/App.tsx`, `web/src/lib/lazyPage.ts`, `web/src/components/AppLayout.tsx`, `web/src/components/UserLayout.tsx`, `web/src/components/PageSkeleton.tsx`, `web/src/pages/Discover.tsx`, `web/src/pages/Audiobooks.tsx`, `web/src/pages/AudiobooksAdmin.tsx`, `web/vite.config.ts`, `web/scripts/check-size.mjs`, `web/package.json`
  - **Acceptance:**
    - npm run build emits a chunk per page plus a vendor chunk, prints the size table, and passes the requester budget.
    - A requester loading /discover on a cold cache fetches no chunk containing Quality, Convert, Insights, Settings or the audiobook admin tabs (check in the Network tab).
    - Every staff route still renders, and deep links such as /movies/12 load directly.
    - Delete a chunk from dist and navigate to its route: exactly one automatic reload happens, then the ErrorBoundary fallback. No blank page and no loop.
  - **Tests:** The build-time size script is the regression test.; Manual Network-tab runs with a cold cache as a LAN requester, an external requester and an admin.; Manual stale-deploy simulation as above. FE-04's Go tests cover the 404 for a missing chunk.
  - **Depends on:** [FE-03](#fe-03), [FE-04](#fe-04)
  - **Risk:** Pages use named exports, so every lazy() needs the mapping helper. Expect a brief skeleton on the first visit to each page; the staff prefetch hides most of it. Moving the audiobook admin tabs touches the AUD epic's file; coordinate if AUD is changing it at the same time.
  - **Resolves:** walk-10, frontend-7
<a id="fe-07"></a>
- [ ] **FE-07 · One usePoll hook: pause every poll in hidden tabs, refresh on return, never overlap** — `P2` · `S` · Phase 3
  - **Problem:** web/src has 29 setInterval timers, and only Discover.tsx:924 checks visibility.
- 3s: Downloads:104, Logs:63, MovieDetail:60, SeriesDetail:89 and Subtitles:119/803.
- 1.5s while active: Convert and Subtitles.
- 4s, plus a 1s clock: Insights.
- 10s: Dashboard.
- 8s: the Discover requests strip.
- 30s: NotificationBell.

Each poll hits the DB and often qBittorrent, even from background tabs and phones. setInterval also fires again while a slow request is still in flight.
  - **Approach:** 1) web/src/lib/usePoll.ts
    - Signature: usePoll(fn: () => Promise<unknown> | void, ms: number | null, {immediate = true, pauseHidden = true}).
    - It is a setTimeout chain: the next tick is scheduled after fn settles, so polls never overlap.
    - ms = null disables it, and a changing ms (Convert/Subtitles at 1.5s while active, 5s idle) re-arms.
    - On visibilitychange: stop when hidden; run immediately and resume when visible.
    - Stops on 'arrmada:signed-out' ([FE-02](#fe-02)).
    - fn is read from a ref, so callers don't re-arm on every render.
    
    2) Migrate every timer, keeping today's intervals:
    - NotificationBell:26
    - BookDetail:288
    - Books:86, 100, 226
    - Convert:84, 687, 843
    - Dashboard:50
    - Discover:550, 924
    - Downloads:104
    - History:25
    - Insights:123, 129
    - Logs:63
    - MovieDetail:60
    - Movies:60, 138
    - MyBooks:37
    - Series:109, 125
    - SeriesDetail:89
    - Subtitles:40, 119, 720, 803
    
    3) The hero carousel timers (Discover:436, BooksDiscover:160) use usePoll too, so they also pause while hidden.
    
    4) This is the cheap first slice of live updates. [FE-27](#fe-27) then replaces most of these polls with socket events.
  - **Files:** `web/src/lib/usePoll.ts`, `web/src/components/NotificationBell.tsx`, `web/src/pages/BookDetail.tsx`, `web/src/pages/Books.tsx`, `web/src/pages/BooksDiscover.tsx`, `web/src/pages/Convert.tsx`, `web/src/pages/Dashboard.tsx`, `web/src/pages/Discover.tsx`, `web/src/pages/Downloads.tsx`, `web/src/pages/History.tsx`, `web/src/pages/Insights.tsx`, `web/src/pages/Logs.tsx`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/Movies.tsx`
  - **Acceptance:**
    - grep finds no setInterval in web/src outside usePoll.ts.
    - A hidden tab makes no periodic /api calls (check the Network panel with the tab in the background for 2 minutes).
    - Returning to the tab refreshes within one request.
    - With the backend artificially slow (e.g. a 5s Downloads response), the requests run one after another instead of piling up.
  - **Tests:** vitest with fake timers (after FE-08):
- the next tick waits for the promise;
- visibility hidden stops it and visible fires immediately;
- ms = null disables it;
- the signed-out event stops it.; Manual Network-panel check on Downloads and Dashboard, visible and hidden.
  - **Depends on:** [FE-02](#fe-02)
  - **Risk:** Low. Keep each page's interval unchanged so this commit only changes when polls run.
  - **Resolves:** frontend-14, backend-4

#### Milestone: M3 — Guard rails and the shared kit

_Lint, vitest and a mocked-API Playwright suite run in CI. Every modal, toast, confirm, button, status chip and form field comes from web/src/ui. Dialogs are accessible (role, name, Esc, focus trap, inner scroll), failed actions show an error toast, and there is no window.confirm left._

<a id="fe-08"></a>
- [ ] **FE-08 · Frontend lint and unit tests: eslint 9 (react-hooks, jsx-a11y) and vitest, running in CI** — `P1` · `S` · Phase 1
  - **Problem:** There are 20 eslint-disable comments for a linter that isn't installed. The frontend has no unit tests, and CI's web job only runs tsc and vite build. That gap is how undefined tokens, unnamed icon buttons and silent failures shipped. The kit (FE-09) and the hooks in later tasks need a test runner.
  - **Approach:** 1) web/eslint.config.js (eslint 9 flat config)
    - typescript-eslint recommended.
    - eslint-plugin-react-hooks: rules-of-hooks is an error; exhaustive-deps is a warning.
    - eslint-plugin-jsx-a11y recommended, at warn for now; [FE-15](#fe-15) flips the key rules to error.
    - no-restricted-globals for confirm and alert, at warn; [FE-14](#fe-14) flips it to error.
    - no-restricted-syntax (warn), with Literal and TemplateElement selectors matching /fixed inset-0/ outside src/ui/**. This catches hand-built overlays.
    - Scripts: lint ('eslint src'), plus lint:fix.
    - The existing eslint-disable comments must resolve to loaded rules; delete any that are stale.
    
    2) vitest
    - Dev dependencies: vitest, jsdom, @testing-library/react, @testing-library/user-event, @testing-library/jest-dom.
    - vite.config.ts gets a test block (environment jsdom, setupFiles src/test/setup.ts).
    - 'test': 'vitest run'. Tests are colocated as *.test.ts(x).
    
    3) First tests:
    - [FE-02](#fe-02)'s sanitizeNext and the req() 401 dispatch, with a mocked fetch;
    - [FE-03](#fe-03)'s ErrorBoundary;
    - [FE-07](#fe-07)'s usePoll;
    - check-tokens against a fixture.
    
    4) .github/workflows/ci.yml web job: npm ci, then npm run lint, then npm test, then npm run build. Warnings don't fail; errors do.
  - **Files:** `web/package.json`, `web/package-lock.json`, `web/eslint.config.js`, `web/vite.config.ts`, `web/src/test/setup.ts`, `web/src/lib/session.test.ts`, `web/src/lib/api.test.ts`, `web/src/lib/usePoll.test.ts`, `web/src/components/ErrorBoundary.test.tsx`, `.github/workflows/ci.yml`
  - **Acceptance:**
    - npm run lint and npm test pass locally on Windows and in CI.
    - Adding window.confirm produces a lint warning, and the lint job runs in CI.
    - A deliberately failing vitest turns CI's web job red.
    - package.json gains dev dependencies only.
  - **Tests:** The suites themselves.; One deliberate-regression run locally.
  - **Risk:** Low. Its priority is raised above the draft's P2 because FE-09 and the later hooks need vitest. Expect a burst of exhaustive-deps warnings; leave them as warnings rather than mass-fixing them here.
  - **Resolves:** frontend-2, frontend-11
<a id="fe-09"></a>
- [ ] **FE-09 · UI kit core: Modal/Sheet, Confirm, Toast, Button/IconButton, StatusChip, Menu, and lib/format.ts, piloted on Discover** — `P1` · `M` · Phase 3
  - **Problem:** There is no shared component layer.
- 34 'fixed inset-0' overlays (about 32 of them modals) are each hand-built:
  - backdrops range from .55 to .68 opacity;
  - five have no inner scroll (MovieDetail 369/475/849, Movies 700, Series 600);
  - only 3 set role=dialog;
  - only 5 handle Escape.
- 16 pages each define their own 'const flash' toast.
- 13 window.confirm calls sit beside custom confirm modals.
- The primary gradient is pasted 56 times.
- Formatters are duplicated: fmtSize x7, gb x4, fmtBytes x4, bytes x4, ago x2, plus ageOf, fmtAgo, eta and etaText.
  - **Approach:** Create web/src/ui/ and web/src/lib/format.ts. Match today's look exactly (radii, panel/line tokens, terracotta gradient) and add no new runtime dependencies.
    
    - Tailwind zIndex tokens:
      - header 30 (the sticky PageHeader today);
      - tabbar 40 (the requester bar, later);
      - drawer 50 (the sidebar);
      - modal 60;
      - toast 70.
    
    - ui/Modal.tsx: generalise Discover.tsx:1191-1214 (focus trap, focus restore, Esc, body scroll lock).
      - Portal to document.body.
      - Props: open, onClose, title or labelledBy, size sm|md|lg|xl, variant dialog|sheet, dismissible, initialFocus, footer.
      - role=dialog, aria-modal and aria-labelledby.
      - A module-level stack:
        - Esc closes only the topmost modal;
        - scroll lock is a counter, so stacked modals restore correctly.
      - Backdrop rgba(0,0,0,.6).
      - The panel uses shadow-panel, max-h-[calc(100dvh-2rem)] and overflow-y-auto.
      - The sheet variant docks to the bottom below sm, with env(safe-area-inset-bottom) padding.
    
    - ui/Confirm.tsx: <ConfirmProvider> and useConfirm({title, body, confirmLabel, tone:'danger'|'default'}) => Promise<boolean>.
    
    - ui/Toast.tsx: <ToastProvider> and useToast() => toast(msg, {tone:'info'|'good'|'error', ms}).
      - One aria-live=polite region at bottom: calc(var(--bottom-chrome,0px) + 20px).
      - At most 3 stacked.
    
    - ui/Button.tsx:
      - Button: variants primary (bg-accent-grad text-accent-ink), secondary, ghost and danger; sizes sm and md; busy prop.
      - IconButton: a required 'label' prop, which becomes aria-label and title.
    
    - ui/StatusChip.tsx: tone accent|good|avoid|reject|faint, mapped to the border, the *-soft fill and the *-text colour. It replaces Discover's StatusChip/BADGE_BG and the Pill/Chip copies as pages migrate.
    
    - ui/Menu.tsx: an accessible dropdown.
      - Trigger button with aria-haspopup and aria-expanded.
      - Esc and click-outside close it; arrow keys and Home/End move focus.
      - Items: {label, onSelect, disabled, tone, busyLabel}.
      - Used by [FE-19](#fe-19) and by the UserLayout avatar menu ([FE-13](#fe-13)).
    
    - lib/format.ts: formatBytes (1024-based; '—' for <=0, as History does), formatDuration, formatAgo and formatEta (from etaText). Check what each call site expects before switching it.
    
    - Mount the providers in main.tsx.
    
    - Pilot on Discover:
      - RequestDetailModal -> Modal variant sheet, keeping its inner layout;
      - flash -> useToast;
      - the confirm at Discover.tsx:594 and the Withdraw/Decline confirms -> useConfirm.
  - **Files:** `web/src/ui/Modal.tsx`, `web/src/ui/Confirm.tsx`, `web/src/ui/Toast.tsx`, `web/src/ui/Button.tsx`, `web/src/ui/StatusChip.tsx`, `web/src/ui/Menu.tsx`, `web/src/ui/index.ts`, `web/src/lib/format.ts`, `web/tailwind.config.js`, `web/src/main.tsx`, `web/src/pages/Discover.tsx`
  - **Acceptance:**
    - Discover's detail sheet looks and behaves as before (focus trap, Esc, focus restore, scroll lock), but ui/Modal renders it.
    - A confirm opened over the sheet closes on Esc without closing the sheet, and body scrolling is restored only after both close.
    - Discover toasts come from the provider and sit above the requester bottom chrome.
    - Every kit component renders correctly in both themes, and package.json gains no runtime dependency.
  - **Tests:** vitest + Testing Library:
- Modal: Tab/Shift+Tab wrap; Esc closes only the topmost modal; focus returns to the opener; body overflow is restored on unmount.
- useConfirm resolves true and false.
- Menu: arrow-key navigation and Esc.
- format.ts: 0, 1023, 1024, 1.5 GiB and negative inputs, plus the ago/eta boundaries.; Manual: the Discover sheet on desktop and at 375px, in both themes.
  - **Depends on:** [FE-01](#fe-01), [FE-08](#fe-08)
  - **Risk:** Portaled modals escape stacking contexts. Verify z-order against the sticky PageHeader, the sidebar drawer and the requester bar with the new zIndex tokens. The APP epic's requester shell must use the same tokens.
  - **Resolves:** frontend-2, frontend-11
<a id="fe-10"></a>
- [ ] **FE-10 · Playwright smoke suite against a mocked API: phone-width overflow, tap behaviour, and the admin shell** — `P2` · `M` · Phase 3
  - **Problem:** Nothing exercises the UI in a browser. The requester header overflowing at 375px, tap targets that request on the first tap, and console errors in the admin shell all shipped unnoticed (frontend-5). The type-scale (FE-30) and LibraryGrid (FE-31) work also needs screenshot and regression specs.
  - **Approach:** 1) web/playwright.config.ts (@playwright/test, chromium only)
    - webServer: 'npm run build && npx vite preview --port 4173 --outDir ../internal/webui/dist'.
    - Projects: desktop 1440x900, and phone 375x812 with hasTouch.
    
    2) web/e2e/fixtures/*.ts
    - me (admin, manager, requester, external), status, discover rows, media detail, calendar, requests, dashboard, movies and downloads.
    - Each is typed against the lib/api.ts interfaces, so tsc catches drift.
    - web/e2e/mockApi.ts installs page.route('**/api/**') handlers, and page.routeWebSocket('**/api/v1/ws') for a silent socket.
    
    3) Specs
    - requester-mobile.spec.ts:
      - At 375x812 and 320x640 as a requester, on /discover, /calendar, /books and /audiobooks: document.documentElement.scrollWidth <= innerWidth, and main.scrollWidth <= main.clientWidth.
      - With hasTouch, tapping a poster opens a role=dialog and sends no POST /api/v1/requests.
      - The overflow cases are marked test.fail() until APP's requester-shell fix lands. CI stays green, and the mark is removed when they pass.
    - admin-smoke.spec.ts: as admin, visit every sidebar entry; there must be no console errors and an h1 must render.
    
    4) Scripts and CI
    - 'e2e': 'playwright test'.
    - ci.yml web job: npx playwright install --with-deps chromium (cache ~/.cache/ms-playwright), then npm run e2e after the build.
    - Upload the HTML report on failure.
  - **Files:** `web/package.json`, `web/playwright.config.ts`, `web/e2e/mockApi.ts`, `web/e2e/fixtures`, `web/e2e/requester-mobile.spec.ts`, `web/e2e/admin-smoke.spec.ts`, `.github/workflows/ci.yml`
  - **Acceptance:**
    - npm run e2e passes locally on Windows and in CI, with no Go backend.
    - Removing the test.fail() mark before APP's fix makes the overflow spec fail, which shows it detects the bug.
    - Introducing a console.error in Dashboard fails the admin smoke spec.
    - CI's web job grows by no more than about 2 minutes.
  - **Tests:** The suites themselves.; A local deliberate regression for the tap spec: make a poster tap call the request endpoint and see the spec fail.
  - **Depends on:** [FE-08](#fe-08)
  - **Risk:** Fixtures can drift from the real API shapes; typing them against api.ts limits that. Keep it to chromium so CI time stays small.
  - **Resolves:** frontend-5, frontend-2
<a id="fe-11"></a>
- [ ] **FE-11 · Move Movies and Series pages and the shared release/file modals onto the kit** — `P2` · `M` · Phase 10
  - **Problem:** The video library has the most hand-built overlays.
- MovieDetail: 3 (369, 475, 849), none with an inner scroll.
- Movies: 2 (612, and 700 with no inner scroll).
- Series: 2 (530, and 600 with no inner scroll).
- SeriesDetail has its own, and so do the shared ReleaseSearchModal, FileDetailsModal, UploadTorrentModal and SeriesSearchModal.

Each page has its own 'const flash' toast (Movies:73, MovieDetail:34, Series:53, SeriesDetail:68) and its own confirms (Series:101, SeriesDetail:438).
  - **Approach:** For Movies, MovieDetail, Series and SeriesDetail, plus components ReleaseSearchModal, FileDetailsModal, UploadTorrentModal and SeriesSearchModal:
    - Each overlay becomes <Modal>, with its title wired to aria-labelledby, a size that fits the content, and an inner scroll.
    - Each 'const flash' plus fixed toast becomes useToast.
    - Each window.confirm or custom delete-confirm becomes useConfirm. Flows with an 'also delete files' checkbox stay as Modal content with a footer.
    - Primary buttons become <Button>, status pills <StatusChip>, and local byte/age/eta helpers lib/format.
    
    Make one commit per page, with no behaviour change beyond the Esc, focus and scroll fixes. Coordinate with the MOV and SER epics: rebase their work onto these commits, or have them use the kit directly.
  - **Files:** `web/src/pages/Movies.tsx`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/Series.tsx`, `web/src/pages/SeriesDetail.tsx`, `web/src/components/ReleaseSearchModal.tsx`, `web/src/components/FileDetailsModal.tsx`, `web/src/components/UploadTorrentModal.tsx`, `web/src/components/SeriesSearchModal.tsx`
  - **Acceptance:**
    - None of these files contains 'fixed inset-0', 'const flash' or window.confirm.
    - Every modal in them closes on Esc, traps focus, and scrolls internally when taller than the window (e.g. a long release list in a 700px-high window).
    - The delete flows still ask the same questions (delete the files too?).
    - No visual change apart from consistent backdrops and shadows.
  - **Tests:** Manual pass in both themes, at desktop width and at 375px: add movie, delete movie (with and without files), manual import, release search, file details, versions, add series, series delete, upload torrent.; Extend admin-smoke (FE-10) to open and close the release search modal on a mocked movie.
  - **Depends on:** [FE-09](#fe-09)
  - **Risk:** This touches many flows. Keep commits small and click through every flow after each page.
  - **Resolves:** frontend-2, frontend-11
<a id="fe-12"></a>
- [ ] **FE-12 · Move Books, Music and their modals onto the kit** — `P2` · `M` · Phase 13
  - **Problem:** Books (3 overlays) and BookDetail (5) are built by hand, as are AuthorDetail, Music, ArtistDetail, AlbumDetail, MyBooks, BookReleaseModal and AudioVersions. Each has its own flash (ArtistDetail:21, AuthorDetail:22, BookDetail:24, Books:81, Music:49) and confirm (Music:239).
  - **Approach:** Apply the same mechanics as [FE-11](#fe-11) to Books, BookDetail, AuthorDetail, Music, ArtistDetail, AlbumDetail, MyBooks, components/BookReleaseModal and components/AudioVersions:
    - Modal, useToast, useConfirm, Button, StatusChip and lib/format.
    - The cover picker and book release picker become Modal size lg with an inner scroll.
    
    One commit per page. Coordinate with the BOOK and MUS epics.
  - **Files:** `web/src/pages/Books.tsx`, `web/src/pages/BookDetail.tsx`, `web/src/pages/AuthorDetail.tsx`, `web/src/pages/Music.tsx`, `web/src/pages/ArtistDetail.tsx`, `web/src/pages/AlbumDetail.tsx`, `web/src/pages/MyBooks.tsx`, `web/src/components/BookReleaseModal.tsx`, `web/src/components/AudioVersions.tsx`
  - **Acceptance:**
    - None of these files contains 'fixed inset-0', 'const flash' or window.confirm.
    - The book release picker, cover picker and audio versions close on Esc, trap focus and scroll internally.
    - Book and author delete flows ask the same questions as before.
  - **Tests:** Manual pass in both themes at desktop width and 375px: add book/author, book release picker, cover picker, audio versions, delete book, add artist, delete artist, MyBooks download.
  - **Depends on:** [FE-09](#fe-09)
  - **Risk:** Same as FE-11: many flows, so keep to one page per commit.
  - **Resolves:** frontend-2, frontend-11
<a id="fe-13"></a>
- [ ] **FE-13 · Move the ops pages onto the kit and make failed actions visible** — `P2` · `S` · Phase 9
  - **Problem:** Silent failures:
- Downloads.tsx:110 act() swallows failures ('next poll reflects reality'), so a failed pause, resume or remove shows nothing.
- The Indexers (:34-36) and DownloadClients (:43-46) remove handlers delete with no confirmation and no try/catch.

Hand-built overlays and toasts:
- Reviews (flash at :13) and Library.tsx's FolderPicker (flash at :24) have their own overlays and toasts.
- The UserLayout avatar menu uses a hand-built 'fixed inset-0' click catcher.
- The Sidebar drawer backdrop has no Esc.
  - **Approach:** 1) Downloads.tsx
    - act() catches the error and shows toast(e.message, {tone:'error'}). busy is cleared in finally, as today.
    - Deleting a torrent goes through useConfirm (quick win #21), keeping the delete-data checkbox as Modal content.
    
    2) Indexers.tsx and DownloadClients.tsx
    - remove() uses useConfirm ('Remove indexer <name>? Searches will stop using it.').
    - try/catch with an error toast.
    - CFG/INT may refine the wording later.
    
    3) Reviews, Library FolderPicker, Dashboard and Logs: overlays become Modal, flash becomes useToast, and buttons become Button.
    
    4) UserLayout's avatar menu becomes ui/Menu.
    
    5) The Sidebar drawer closes on Esc and returns focus to the hamburger. It stays a drawer, not a Modal.
    
    6) Requests.tsx is dead code; leave it for REQ to delete.
  - **Files:** `web/src/pages/Downloads.tsx`, `web/src/pages/Indexers.tsx`, `web/src/pages/DownloadClients.tsx`, `web/src/pages/Reviews.tsx`, `web/src/pages/Library.tsx`, `web/src/pages/Dashboard.tsx`, `web/src/pages/Logs.tsx`, `web/src/components/UserLayout.tsx`, `web/src/components/Sidebar.tsx`, `web/src/components/AppLayout.tsx`
  - **Acceptance:**
    - Pausing a torrent while qBittorrent is stopped shows an error toast.
    - Removing an indexer or download client asks first, and a failed removal shows an error.
    - The avatar menu works with the keyboard (Enter, arrows, Esc).
    - None of these files contains 'fixed inset-0' (outside ui/) or 'const flash'.
  - **Tests:** Manual: stop the bundled qBittorrent in a scratch instance, then pause, resume and remove.; Manual: indexer and client removal, both confirmed and cancelled.; vitest: Downloads act() surfaces the error toast when the api call rejects (mocked).
  - **Depends on:** [FE-09](#fe-09)
  - **Risk:** Low. Coordinate the confirmation copy with CFG's connections hub so the wording isn't written twice.
  - **Resolves:** frontend-2, frontend-9, frontend-11
<a id="fe-14"></a>
- [ ] **FE-14 · Move the module consoles (Settings, Quality, Convert, Subtitles, Insights, Audiobooks, BooksDiscover) onto the kit, then ban the old patterns** — `P2` · `M` · Phase 16
  - **Problem:** The module consoles repeat the same hand-built parts:
- Settings: EditUserModal, plus confirms at :356 and :373.
- Quality: TemplatePicker, plus confirms at :469, :1114 and :1212.
- Convert: a modal at :706, a confirm at :464 and a flash at :64.
- Subtitles: a confirm at :652 and a flash at :30.
- Insights: 2 overlays, a confirm at :873 and a flash at :23.
- Audiobooks: a confirm at :289.
- BooksDiscover: its request modal at :570 has no Esc and no focus trap, and the file re-declares BADGE_BG (:21).
  - **Approach:** 1) Migrate each page to Modal, useToast, useConfirm, Button and StatusChip, one commit per page. BooksDiscover's BADGE_BG and badgeFor become StatusChip tones.
    
    2) Quality's three builder leave() confirms become useConfirm for now; [FE-25](#fe-25) replaces them with the route-level unsaved guard.
    
    3) Once this lands and [FE-11](#fe-11) to [FE-13](#fe-13) are done, flip these eslint rules to error:
    - no-restricted-globals (confirm, alert);
    - no-restricted-syntax for 'fixed inset-0' outside src/ui;
    - no-restricted-syntax for VariableDeclarator[id.name='flash'].
  - **Files:** `web/src/pages/Settings.tsx`, `web/src/pages/Quality.tsx`, `web/src/pages/Convert.tsx`, `web/src/pages/Subtitles.tsx`, `web/src/pages/Insights.tsx`, `web/src/pages/Audiobooks.tsx`, `web/src/pages/BooksDiscover.tsx`, `web/eslint.config.js`
  - **Acceptance:**
    - grep finds no window.confirm and no 'const flash' anywhere in web/src, and no 'fixed inset-0' outside web/src/ui.
    - BooksDiscover's request modal closes on Esc and traps focus.
    - Every modal has role=dialog and an accessible name.
    - npm run lint fails on a new window.confirm.
  - **Tests:** Manual click-through of every modal and confirm on these pages, in both themes.; The lint rules at error in CI.
  - **Depends on:** [FE-09](#fe-09), [FE-11](#fe-11), [FE-12](#fe-12), [FE-13](#fe-13)
  - **Risk:** These are big files (Quality is 86 KB, Convert 68 KB, Insights 65 KB) that the QUAL, CONV, SUB, PLEX and AUD epics also edit. Work one page per commit and rebase often.
  - **Resolves:** frontend-2, frontend-11
<a id="fe-15"></a>
- [ ] **FE-15 · Real labels on form fields, named icon buttons, switches exposed as switches; jsx-a11y rules to error** — `P2` · `M` · Phase 16
  - **Problem:** There are 56 <label> elements but only 3 htmlFor attributes; for example, MovieDetail's 'Add a version' labels sit beside their inputs, not around them. 19 icon-only '✕' buttons have no aria-label. The Settings and Quality toggles aren't exposed as switches. Screen-reader and keyboard users can't tell what a field is for.
  - **Approach:** 1) Add these kit components:
    - ui/Field.tsx: label, hint and error. It uses useId and passes id and aria-describedby to its single child control.
    - ui/Input, ui/Select and ui/Textarea, styled like today's fieldStyle (panel-2 background, line border, ink text).
    - ui/Switch: a button with role=switch and aria-checked, whose label is clickable. It is styled like today's Toggle.
    
    2) Replace the local helpers:
    - Settings' Section/Field/Toggle;
    - Quality's field helpers;
    - the Indexers and DownloadClients forms;
    - the MovieDetail versions modal;
    - Login and SetupWizard;
    - the Convert, Subtitles and Audiobooks settings forms.
    
    3) Replace every icon-only button with <IconButton label=…>.
    
    4) Flip these jsx-a11y rules to error: label-has-associated-control, control-has-associated-label, no-static-element-interactions, click-events-have-key-events. Also set the [FE-08](#fe-08) rules still at warn to error where the tree is clean.
  - **Files:** `web/src/ui/Field.tsx`, `web/src/ui/Input.tsx`, `web/src/ui/Select.tsx`, `web/src/ui/Textarea.tsx`, `web/src/ui/Switch.tsx`, `web/src/pages/Settings.tsx`, `web/src/pages/Quality.tsx`, `web/src/pages/Indexers.tsx`, `web/src/pages/DownloadClients.tsx`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/Login.tsx`, `web/src/pages/SetupWizard.tsx`, `web/src/pages/Convert.tsx`, `web/src/pages/Subtitles.tsx`
  - **Acceptance:**
    - Clicking any field label focuses its control.
    - axe DevTools reports 0 'form label' and 0 'button name' violations on Settings, the Quality editor, Indexer add/edit and Login.
    - npm run lint passes with the jsx-a11y rules above at error.
    - The UI looks the same as before (side-by-side screenshots).
  - **Tests:** vitest: getByLabelText finds Field controls; Switch toggles aria-checked on click and on Space.; Manual axe pass, plus an NVDA (Windows) pass on the pages above.
  - **Depends on:** [FE-08](#fe-08), [FE-09](#fe-09), [FE-14](#fe-14)
  - **Risk:** Low. Visual output must not change; compare screenshots of Settings and the Quality editor.
  - **Resolves:** frontend-11, frontend-2

#### Milestone: M4 — Navigation and layout that agree

_The sidebar is grouped the way the app works and has icons. Breadcrumbs derive from it and can't drift. Pages share two widths, the four libraries share one toolbar and filter bar, and library pages fit a 375px phone._

<a id="fe-16"></a>
- [ ] **FE-16 · Regroup the admin sidebar around how the app works, with icons, and drive module visibility from nav.ts** — `P2` · `S` · Phase 3
  - **Problem:** nav.ts has 19 text-only entries. 'Services' mixes the requester product (Discover, Calendar), file tools (Subtitles, Convert), Plex monitoring (Insights) and the audiobook server. Books sits under Library while Audiobooks sits under Services. Downloads, History and Review sit in an unnamed top group. Sidebar.tsx hard-codes the books/music filter.
  - **Approach:** 1) web/src/lib/nav.ts
    - NavItem gains:
      - icon: IconName;
      - module?: 'books' | 'music';
      - badge?: 'requests' | 'activity' | 'review' | 'issues' (used by [FE-28](#fe-28)).
    - NavGroup gains an optional crumb (used by [FE-17](#fe-17)).
    - Groups. This reconciles the two drafts: Calendar and Audiobooks go to Library, because both show what is in the library.
      - top group, crumb 'Home': Dashboard, Discover. Requests (/requests) is added when REQ ships it.
      - Activity: Downloads, History, Review. These collapse into one Activity entry when ACQ merges them.
      - Library: Movies, Series, Books, Audiobooks, Music, Calendar.
      - Tools: Subtitles, Convert.
      - Plex: Insights.
      - System: Indexers, Download clients, Quality profiles, Logs, Settings. This group shrinks when CFG's hub lands.
    
    2) web/src/components/icons.tsx: about 18 inline SVG icons (16px, stroke 1.8, currentColor), in the style of the existing hamburger and sign-out icons.
    
    3) Sidebar.tsx
    - Render the icon before each label.
    - Filter items by module against booksEnabled and musicEnabled.
    - Keep the item size (13.5px), the active style (accent-soft), the group header style and the drawer behaviour.
    - Group headers keep the existing mono eyebrow style.
    
    4) Routes stay exactly as they are.
  - **Files:** `web/src/lib/nav.ts`, `web/src/components/icons.tsx`, `web/src/components/Sidebar.tsx`
  - **Acceptance:**
    - The sidebar shows Home (unlabelled), Activity, Library, Tools, Plex and System, and every entry has an icon.
    - Books, Music and their entries hide when those modules are off, with no hard-coded paths left in Sidebar.tsx.
    - Both themes and the mobile drawer look right; the before/after screenshots are shown to the owner.
  - **Tests:** vitest: the nav module filter (booksEnabled=false drops /books; musicEnabled=false drops /music).; Manual review at desktop width and in the drawer at 375px.
  - **Risk:** This changes the owner's muscle memory; mention it in the commit message. REQ (/requests) and ACQ (/activity) add or merge entries later, and only nav.ts changes then.
  - **Resolves:** frontend-10, product-8
<a id="fe-17"></a>
- [ ] **FE-17 · Derive page breadcrumbs from nav.ts instead of 36 hand-written strings** — `P2` · `S` · Phase 3
  - **Problem:** PageHeader (components/PageHeader.tsx) takes a free-text crumb, and 36 call sites each write their own.
- Subtitles:70 and Convert:130 say 'Library / …'.
- Downloads says 'Transfers', and History says 'Imported to library'.
- Review says 'Activity / Review' when no Activity group exists.
- Dashboard says 'Overview'.
Any nav change silently desynchronises them.
  - **Approach:** 1) nav.ts: export crumbFor(pathname).
    - Find the NAV item with the longest 'to' that prefixes the path. '/' matches only the exact path.
    - Return '<group crumb or group> / <item label>'. '/' returns 'Home'.
    - Unknown paths return undefined.
    
    2) PageHeader.tsx
    - Default the crumb to crumbFor(useLocation().pathname).
    - Add an optional 'tail' prop for detail levels: AuthorDetail 'Author', the Quality editors 'Edit profile', 'New profile' and 'Edit music profile'.
    - Keep 'crumb' only as an explicit override; ideally nothing uses it.
    
    3) Remove every crumb= literal (36, listed by grep -n 'crumb=' web/src/pages) and use tail where a detail level is needed.
    
    4) Expected results:
    - Convert 'Tools / Convert';
    - Downloads, History and Review 'Activity / …';
    - Insights 'Plex / Insights';
    - detail pages show the parent, e.g. 'Library / Movies';
    - the Quality editor 'System / Quality profiles / Edit profile'.
  - **Files:** `web/src/lib/nav.ts`, `web/src/components/PageHeader.tsx`, `web/src/pages/Subtitles.tsx`, `web/src/pages/Convert.tsx`, `web/src/pages/Downloads.tsx`, `web/src/pages/History.tsx`, `web/src/pages/Reviews.tsx`, `web/src/pages/Quality.tsx`, `web/src/pages/AuthorDetail.tsx`, `web/src/pages/Dashboard.tsx`, `web/src/pages/Insights.tsx`, `web/src/pages/Calendar.tsx`, `web/src/pages/Discover.tsx`, `web/src/pages/Audiobooks.tsx`
  - **Acceptance:**
    - For every route in the staff tree, the crumb's first segment equals the sidebar group that highlights the active NavLink.
    - grep -n 'crumb="' web/src/pages returns nothing.
    - Renaming a group in nav.ts changes the crumbs on its pages without touching any page file.
  - **Tests:** vitest: crumbFor covers '/', '/movies/12', '/books/author/x', '/quality' and an unknown path.; Manual: click every sidebar entry plus one detail page per library.
  - **Depends on:** [FE-16](#fe-16)
  - **Risk:** Very low. The dead pages/Requests.tsx has a crumb too; leave it for REQ to delete.
  - **Resolves:** walk-3, frontend-10
<a id="fe-18"></a>
- [ ] **FE-18 · Two page widths applied everywhere, with forms in a left-aligned column** — `P3` · `S` · Phase 16
  - **Problem:** Each page picks its own container width: 820, 960, 980, 1000, 1100, 1200, 1240, 1360, 1440 or 1600px. There are 37 'mx-auto w-full max-w-[…]' containers in pages/. All are centred under a full-width sticky PageHeader whose title sits at the far left, so on wide screens the content's left edge jumps from page to page.
  - **Approach:** 1) tailwind.config.js maxWidth: page 1240px, page-wide 1600px, form 760px.
    
    2) web/src/ui/Page.tsx: <Page width='default' | 'wide'> renders 'mx-auto w-full max-w-page (or max-w-page-wide) px-4 py-6 sm:px-6'.
    - wide: Discover, Movies, Series, Books, Music, AuthorDetail, Downloads.
    - default: everything else (Dashboard, History, Review, Calendar, Subtitles, Convert, Insights, Quality, Indexers, DownloadClients, Logs, Audiobooks, Settings, MyBooks and all the detail pages).
    
    3) Form-like content (the Settings sections, the Audiobooks account tab, the Quality editor) sits in an inner max-w-form column that is left-aligned, not centred.
    
    4) Replace the 37 hand-written page containers with <Page>. Modals keep their own widths.
    
    5) When CFG's Settings hub lands, it uses <Page width='default'>.
  - **Files:** `web/tailwind.config.js`, `web/src/ui/Page.tsx`, `web/src/pages/Dashboard.tsx`, `web/src/pages/Settings.tsx`, `web/src/pages/Audiobooks.tsx`, `web/src/pages/Indexers.tsx`, `web/src/pages/DownloadClients.tsx`, `web/src/pages/Logs.tsx`, `web/src/pages/Reviews.tsx`, `web/src/pages/History.tsx`, `web/src/pages/Calendar.tsx`, `web/src/pages/Quality.tsx`, `web/src/pages/Subtitles.tsx`, `web/src/pages/Convert.tsx`
  - **Acceptance:**
    - grep -n 'mx-auto w-full max-w-\[' web/src/pages matches only modal or dialog containers.
    - At 1920px the content's left edge is identical on Settings, Indexers, Audiobooks, Dashboard and Insights; the library pages and Discover use the wide width.
    - At 375px every page keeps the 16px side gutter.
  - **Tests:** Manual at 1920px and 1280px: compare getBoundingClientRect().left of the first content element on each sidebar page.; The Playwright overflow specs (FE-10) still pass at 375px.
  - **Risk:** This is a visual change the owner may have opinions on. Show before/after screenshots of Settings and Audiobooks first. The palette and type scale don't change.
  - **Resolves:** walk-9
<a id="fe-19"></a>
- [ ] **FE-19 · One library toolbar and filter bar for Movies, Series, Books and Music** — `P2` · `M` · Phase 10
  - **Problem:** The four library headers are hand-built and drift apart.
- Books (Books.tsx:236-265) packs up to 9 controls into a row that doesn't wrap: two segmented toggles, Re-match, Search missing, Find series, Scan library, Select, + Add author and + Add book, in mixed bordered, outline and gradient styles.
- Music's filter pills have no counts (Music.tsx:110-126).
- Placeholders differ: 'Search titles…', 'Search title or author…', 'Filter artists…'.
- Primary labels differ: '+ Add movie' versus 'Add artist'.
- Music puts its actions on the filter row.
  - **Approach:** 1) web/src/ui/LibraryToolbar.tsx. Props:
    - count and noun;
    - segmented groups (view grid|table; Books mode author|book);
    - select {active, onToggle};
    - primary {label, onClick, disabled}, plus an optional secondary primary such as '+ Add author';
    - tools: [{label, onClick, title, busy, busyLabel, hidden}].
    Layout: the count on the left. On the right: the segmented groups, Select, a '⋯ Library tools' ui/Menu and the primary button. flex flex-wrap with gap-2. Built from kit Button and Menu, with today's ghost and gradient looks.
    
    2) web/src/ui/FilterBar.tsx
    - Pills always show counts.
    - A search input with the placeholder 'Search <noun>…'. It is w-full on phones and w-[240px] ml-auto from sm up.
    
    3) Apply it:
    - Movies (Movies.tsx:162-220): tools are Scan library.
    - Series (~150-220): tools are Scan library.
    - Books (234-286): tools are Scan library, Find series, Re-match N to Hardcover and Search missing (N). Visible: the mode toggle, the view toggle, Select, + Add author and + Add book.
    - Music (110-170): tools are Scan library. Add counts to its FILTERS using the existing matches(), and rename the button '+ Add artist'.
    
    4) Running sweeps ('Re-matching… 3/10', 'Searching… 2/5') show on the menu item and as a small inline StatusChip, so the progress stays visible while the menu is closed.
    
    5) The rest of books-12 (the detail toolbar, hover-only card actions, the sort control) stays with BOOK.
  - **Files:** `web/src/ui/LibraryToolbar.tsx`, `web/src/ui/FilterBar.tsx`, `web/src/pages/Movies.tsx`, `web/src/pages/Series.tsx`, `web/src/pages/Books.tsx`, `web/src/pages/Music.tsx`
  - **Acceptance:**
    - All four libraries use LibraryToolbar and FilterBar, with no hand-built header rows left.
    - The Books header shows at most 6 visible controls. A running sweep or re-match still shows its progress chip.
    - Every library's filter pills show counts, Music included. Placeholders read 'Search <noun>…', and every primary button starts with '+ Add'.
    - Side-by-side screenshots at 1440px keep the current palette, button styles and type sizes.
  - **Tests:** Manual at 1440px and 1024px on all four libraries, including opening the tools menu by keyboard (Tab, Enter, arrows, Esc).; Manual: start a Books 'Search missing' and confirm the chip updates while the menu is closed.; vitest: FilterBar renders counts and calls onChange with the debounced query.
  - **Depends on:** [FE-09](#fe-09)
  - **Risk:** BOOK and MUS edit the same headers. Land this first, or have them build on the new components.
  - **Resolves:** walk-8
<a id="fe-20"></a>
- [ ] **FE-20 · Library pages at phone width: no sideways scrolling, and the grid instead of a 900px table** — `P2` · `S` · Phase 10
  - **Problem:** At 375px, the Movies header row (Movies.tsx:162, a justify-between row that doesn't wrap) squeezes '+ Add movie' into a narrow column, and its minimum width makes <main> scroll sideways. The table view has a 900px minimum width (Movies.tsx:454), which is unusable on a phone. Series and Books copy the same pattern.
  - **Approach:** 1) Through [FE-19](#fe-19)'s LibraryToolbar:
    - rows wrap;
    - buttons get whitespace-nowrap shrink-0;
    - below 640px the count moves to its own line and the search box goes full width.
    
    2) web/src/lib/useMediaQuery.ts. Below 640px, Movies, Series and Books render the grid even when the persisted view is 'table', and hide the view toggle. The persisted preference is left unchanged, so desktop keeps 'table'.
    
    3) Fix the remaining overflow sources at 375px with flex-wrap or min-w-0: the bulk-action bar, the upgrade banner, the Books mode toggle.
    
    4) Don't hide overflow on <main>; fix the sources.
  - **Files:** `web/src/lib/useMediaQuery.ts`, `web/src/ui/LibraryToolbar.tsx`, `web/src/ui/FilterBar.tsx`, `web/src/pages/Movies.tsx`, `web/src/pages/Series.tsx`, `web/src/pages/Books.tsx`, `web/src/pages/Music.tsx`, `web/e2e/admin-mobile.spec.ts`
  - **Acceptance:**
    - At 375x812, on Movies, Series, Books and Music, main.scrollWidth <= main.clientWidth, with multi-select both on and off.
    - Each '+ Add …' button renders on one line.
    - At 375px the table toggle is hidden and the grid shows; at desktop width the saved 'table' view returns.
  - **Tests:** New Playwright spec web/e2e/admin-mobile.spec.ts (mocked API, admin role): runs the overflow check on the four libraries at 375x812 and 768x1024.; Manual in the Browser pane with the mobile preset.
  - **Depends on:** [FE-19](#fe-19), [FE-10](#fe-10)
  - **Risk:** Low. The admin shell's mobile layouts on other pages (Insights, Calendar) belong to their own epics (PLEX, APP).
  - **Resolves:** walk-8

#### Milestone: M5 — State that survives Back and refresh

_Lists show skeleton, error, empty or content honestly and render from cache on Back. The data router gives per-page titles and route error elements. Tabs, filters and searches live in the URL, scroll position is restored, and the routed Quality editor and other dirty forms ask before discarding edits._

<a id="fe-21"></a>
- [ ] **FE-21 · useQuery data hook with honest loading, error, empty and content states, and a cache for Back** — `P2` · `M` · Phase 3
  - **Problem:** Movies, Series, Books, Indexers, DownloadClients and Quality start with a list of [] and branch on length===0.
- Before the first response they show the onboarding empty state ('No movies yet. Click Add movie…').
- When a fetch fails, they show that empty state next to the red error (Movies.tsx:252-261, Indexers.tsx:73-75, DownloadClients.tsx:80-82).
- There are 30 bare 'Loading…' strings; only Discover has skeletons.
- Every remount refetches from scratch, so Back shows an empty list, which also defeats scroll restoration.
  - **Approach:** 1) web/src/lib/query.ts
    - useQuery<T>(key: string, fetcher, {enabled?, staleMs = 15000}) returns {data, error, loading, refetch, mutate}. Polling stays in usePoll and later useLiveQuery.
    - A module-level cache Map<key, {data, at}>, LRU-bounded to 50 entries:
      - a remount renders cached data immediately and revalidates in the background when stale;
      - concurrent calls for the same key share one in-flight promise.
    - invalidate(prefix) and setQueryData(key, fn) for mutations.
    - The whole cache is cleared on 'arrmada:signed-out' ([FE-02](#fe-02)), so a shared device never shows the previous user's data.
    
    2) Shared state components:
    - ui/Skeleton: grid, list and table variants, sized like the real cards (taken from Discover's).
    - ui/ErrorState: message plus Retry. Copy in the spirit of Convert.tsx:562's 'this is an error, not an empty library'.
    - ui/EmptyState: icon, title, body and an optional action.
    
    3) Render order: Skeleton when there's no data and it's loading; ErrorState when there's no data and an error; EmptyState when the data is empty; otherwise the content. If an error arrives while data exists, show a thin stale-data banner.
    
    4) Convert the pages in this order: Movies, Series, Books, Indexers, DownloadClients, the Quality list, History, Reviews. Add and delete call invalidate or mutate.
  - **Files:** `web/src/lib/query.ts`, `web/src/ui/Skeleton.tsx`, `web/src/ui/ErrorState.tsx`, `web/src/ui/EmptyState.tsx`, `web/src/pages/Movies.tsx`, `web/src/pages/Series.tsx`, `web/src/pages/Books.tsx`, `web/src/pages/Indexers.tsx`, `web/src/pages/DownloadClients.tsx`, `web/src/pages/Quality.tsx`, `web/src/pages/History.tsx`, `web/src/pages/Reviews.tsx`
  - **Acceptance:**
    - Under Slow 3G throttling, Movies shows a skeleton grid and never 'No movies yet'.
    - With the backend stopped, Movies shows the error state with Retry and no onboarding text.
    - Back from a movie detail renders the grid instantly from cache, then revalidates.
    - After signing out and in as another user, no cached list from the first user appears.
  - **Tests:** vitest with fake timers:
- a cache hit on remount;
- deduped concurrent calls;
- an error, then a successful retry;
- invalidate(prefix);
- the cache is cleared on the signed-out event;
- LRU eviction.; Manual throttled and offline checks.
  - **Depends on:** [FE-08](#fe-08), [FE-09](#fe-09)
  - **Risk:** A stale cache can briefly show a deleted item. Every add and delete call invalidates its key prefix.
  - **Resolves:** frontend-9, frontend-3
<a id="fe-22"></a>
- [ ] **FE-22 · Move to a data router: per-role route tables, route error elements and per-page titles** — `P2` · `M` · Phase 3
  - **Problem:** main.tsx:10 uses <BrowserRouter>, with three inline <Routes> trees in App.tsx. That rules out:
- useBlocker, which FE-25 needs so navigation can't silently discard edits;
- a route-level errorElement;
- route handles for per-page titles. Every tab is titled 'Arrmada'.

The 404 route renders Placeholder, whose heading reads 'Not found isn't built yet'.
  - **Approach:** 1) web/src/lib/routes.tsx: buildRoutes({role, external}) returns RouteObject[] for the external, requester and staff shells.
    - The same paths as today, including the /activity, /notifications and /library redirects.
    - Each route has handle {title} and errorElement <RouteError/>, which reuses [FE-03](#fe-03)'s fallback card.
    - Module toggles do NOT rebuild the router. /books and /music elements are wrapped in <ModuleGate module='books'>, which redirects to the shell's home when the module is off. Admins can toggle a module live from Settings without resetting history.
    - A real NotFound page ('No such page' plus a link home) replaces Placeholder for '*'.
    
    2) App.tsx
    - Once MeProvider resolves: router = useMemo(() => createBrowserRouter(buildRoutes(...), {basename: BASE}), [role, external]). BASE is '' until [FE-29](#fe-29).
    - Render <RouterProvider router={router}/>.
    - Staff stay inside SetupGate. MeProvider stays above the router and uses no router hooks; [FE-02](#fe-02)'s rememberNext reads window.location.
    
    3) useDocumentTitle in both layouts, via useMatches(): document.title = '<title> · Arrmada'. Detail pages set a dynamic title through a small useTitle(title) hook.
    
    4) The lazy page elements from [FE-06](#fe-06) work unchanged.
    
    5) Remove BrowserRouter from main.tsx.
  - **Files:** `web/src/main.tsx`, `web/src/App.tsx`, `web/src/lib/routes.tsx`, `web/src/components/RouteError.tsx`, `web/src/components/ModuleGate.tsx`, `web/src/pages/NotFound.tsx`, `web/src/components/AppLayout.tsx`, `web/src/components/UserLayout.tsx`
  - **Acceptance:**
    - Every existing URL resolves as before for external, requester and staff sessions, redirects included.
    - The browser tab title changes per page (e.g. 'Movies · Arrmada', 'Dune · Arrmada').
    - An exception in a page renders RouteError inside the layout.
    - Turning Books off in Settings hides /books without a reload or a router reset.
    - useBlocker is available, for FE-25.
  - **Tests:** vitest: buildRoutes (the external tree has no /calendar; the requester tree has no /quality). ModuleGate redirects when a module is off.; Playwright (FE-10): admin-smoke visits every nav item as admin and as requester, and checks document.title.
  - **Depends on:** [FE-03](#fe-03), [FE-06](#fe-06)
  - **Risk:** Recreating the router when the role changes resets in-memory history. That only happens at login and logout, which already do a full load.
  - **Resolves:** frontend-12, frontend-9
<a id="fe-23"></a>
- [ ] **FE-23 · URL-addressable tabs on every tabbed page, with one accessible Tabs component** — `P2` · `M` · Phase 3
  - **Problem:** Seven pages keep their tab in useState: Audiobooks:27, Convert:54, Discover:21, Downloads:73, Insights:20, Settings:55 and Subtitles:26. Quality's media switch (:219) is plain state too.
- Back leaves the page instead of going back a tab.
- Refreshing resets the tab, and tabs can't be bookmarked.
- /notifications redirects to /insights, which always opens on Activity.
- Discover consumes ?q and ?tab and then clears them (Discover.tsx:37-50).
- There are four copies of the underline tab bar.
  - **Approach:** 1) web/src/lib/useTabParam.ts
    - useTabParam<T>(allowed: readonly T[], fallback, key = 'tab').
    - It reads the param and ignores unknown values. It writes with push, so Back steps through tabs, and drops the param when it equals the fallback.
    
    2) web/src/ui/Tabs.tsx
    - Today's underline style: a 2px accent bar and 13.5px semibold labels.
    - role=tablist and role=tab, aria-selected, aria-controls.
    - Roving tabIndex with arrow, Home and End keys.
    - An optional count badge (used by Downloads and Subtitles).
    - Scrolls horizontally on phones.
    
    3) Apply it, with these allowed values:
    - Audiobooks: you, server, people, listening, import. Requesters only get 'you'.
    - Convert: overview, library, problems, activity, settings.
    - Discover: discover, movies, series, books, plus ?q. The committed search writes ?q= with push on Enter or 'See all', not per keystroke. Stop clearing the params; NotificationBell's /discover?q= keeps working.
    - Downloads: downloads, seeding, searching, upcoming.
    - Insights: activity, history, users, graphs, reliability, notifications, settings. The onConfigure callbacks switch tabs through the URL.
    - Settings: media, library, system, users.
    - Subtitles: overview, queue, library, logs, settings.
    - Quality: ?media=movie|series|book|music.
    
    4) Routes: /notifications redirects to /insights?tab=notifications.
  - **Files:** `web/src/lib/useTabParam.ts`, `web/src/ui/Tabs.tsx`, `web/src/pages/Audiobooks.tsx`, `web/src/pages/Convert.tsx`, `web/src/pages/Discover.tsx`, `web/src/pages/Downloads.tsx`, `web/src/pages/Insights.tsx`, `web/src/pages/Settings.tsx`, `web/src/pages/Subtitles.tsx`, `web/src/pages/Quality.tsx`, `web/src/lib/routes.tsx`
  - **Acceptance:**
    - On Insights, click Graphs, then Users; Back returns to Graphs.
    - Refreshing /settings?tab=users stays on Users.
    - /notifications opens Insights on the Notifications tab.
    - A Discover search updates the URL to ?q=, and refreshing keeps the results.
    - Arrow keys move between tabs, and a screen reader announces 'tab, 2 of 7'.
  - **Tests:** vitest with MemoryRouter: useTabParam (an unknown value falls back; push history; the default is omitted from the URL). Tabs: arrow-key roving.; Manual Back and refresh checks on all eight pages.
  - **Depends on:** [FE-01](#fe-01), [FE-08](#fe-08)
  - **Risk:** CFG turns the Settings tabs into routes in its hub. Keep redirects from ?tab=media|library|system|users there. APP's Discover detail-sheet routes (/discover/:media/:id) must keep the ?tab and ?q params when the sheet opens and closes.
  - **Resolves:** frontend-6, frontend-2
<a id="fe-24"></a>
- [ ] **FE-24 · Scroll reset and restore per page; library filters and search in the URL** — `P2` · `M` · Phase 10
  - **Problem:** The only scroll container is the persistent <main overflow-y-auto> (AppLayout.tsx:22, UserLayout.tsx:57), and nothing resets or restores it on navigation. React Router's ScrollRestoration only handles window scroll, so it can't help.
- Pages that render tall content immediately (Discover's skeleton rows, Calendar) open part-way down.
- Back loses both your scroll position and the Movies/Series filter and search, which live in plain useState (Movies.tsx:43-44, Series.tsx:38-39).
- MovieDetail itself is not affected: its Loading block clamps scroll to 0.
  - **Approach:** 1) web/src/lib/useScrollMemory.ts(mainRef), used by both layouts
    - On a location.key change, save the previous entry's scrollTop in a sessionStorage-backed map, capped at 100 entries.
    - PUSH or REPLACE with a pathname change: set scrollTop to 0.
    - POP: restore the saved value. Retry through a ResizeObserver on main's first child until the content is tall enough, the user scrolls, or 2s pass.
    - Ignore REPLACEs that only change search params, so typing in a filter doesn't jump.
    - Uses useNavigationType and useLocation, which work under the data router.
    
    2) web/src/lib/useSearchState.ts: useSearchParamState(key, default, {allowed?, debounceMs?}).
    - Writes with replace and drops the param when it equals the default.
    - q is debounced by 300ms.
    
    3) Apply it to:
    - Movies and Series: ?filter=missing&q=dune.
    - Books: filter and q. Music: q.
    - Downloads: type filter and q.
    - The Subtitles and Convert library filters and search.
    
    4) View and sort stay in usePersisted, as remembered preferences.
  - **Files:** `web/src/lib/useScrollMemory.ts`, `web/src/lib/useSearchState.ts`, `web/src/components/AppLayout.tsx`, `web/src/components/UserLayout.tsx`, `web/src/pages/Movies.tsx`, `web/src/pages/Series.tsx`, `web/src/pages/Books.tsx`, `web/src/pages/Music.tsx`, `web/src/pages/Downloads.tsx`, `web/src/pages/Subtitles.tsx`, `web/src/pages/Convert.tsx`
  - **Acceptance:**
    - Scroll halfway down Movies with filter Missing and search 'dune', open a movie, then press Back: the filter, the search text and the scroll position (within about one card row) are all restored.
    - A sidebar link always opens the target page at the top.
    - Pasting /movies?filter=missing&q=dune into a new tab shows that filtered view.
    - Typing in the filter box doesn't jump the scroll position.
  - **Tests:** vitest: useSearchParamState with a MemoryRouter (the default is omitted from the URL; the allowed-list fallback; debounce).; Playwright (mocked /movies with 200 items): scroll, open a detail, go Back, and assert scrollTop within one row.; Manual Back/Forward checks on Movies, Series, Discover and Calendar.
  - **Depends on:** [FE-21](#fe-21)
  - **Risk:** A restore that runs before the content renders gets clamped to 0. The ResizeObserver retry and FE-21's cache handle this; without cached data, the restore gives up after 2s.
  - **Resolves:** frontend-3
<a id="fe-25"></a>
- [ ] **FE-25 · Route the Quality editor and guard unsaved edits everywhere** — `P2` · `M` · Phase 16
  - **Problem:** The Quality editor is a state swap inside /quality (Quality.tsx:257, 'if (editing) return <VideoBuilder/>'), so it has no URL. useUnsaved (Quality.tsx:430-437) only adds a beforeunload listener, so a sidebar click or Back silently throws away a half-built profile. editRef and duplicate (Quality.tsx:250-254) await api.qualityProfile with no catch, so a failed load shows nothing. Settings has no guard at all.
  - **Approach:** 1) Routes, in the staff table in lib/routes.tsx:
    - /quality: the list, with ?media=.
    - /quality/:media/new: the template arrives in navigation state from TemplatePicker. If it's missing on refresh, reopen the picker.
    - /quality/:media/:key: edit.
    - /quality/:media/:key/copy: duplicate, with id 0 and the name '<name> (copy)'.
    
    2) QualityEditorRoute
    - Loads the profile with useQuery(api.qualityProfile(key)). ErrorState if that fails, e.g. for a deleted profile.
    - It picks VideoBuilder, BookBuilder or MusicBuilder by media.
    - onCancel calls navigate('/quality?media=' + media). onSaved clears dirty, invalidates 'quality', shows a toast and navigates the same way.
    
    3) web/src/lib/useUnsavedGuard.ts(dirty, message) replaces useUnsaved:
    - useBlocker(({currentLocation, nextLocation}) => dirty && currentLocation.pathname !== nextLocation.pathname);
    - when blocked, show useConfirm ('Discard your changes to this profile?' with Keep editing / Discard), then blocker.proceed() or blocker.reset();
    - plus beforeunload.
    
    4) Remove the builders' own leave() confirms (Quality.tsx:469, 1114 and 1212, already moved to useConfirm in [FE-14](#fe-14)). Clear dirty before the editor's own Save navigation.
    
    5) Duplicate or edit failures show an error toast.
    
    6) Also guard, until CFG's auto-save lands: the Settings draft, EditUserModal, the Indexer and DownloadClient edit forms, and the Convert and Subtitles settings tabs.
  - **Files:** `web/src/pages/Quality.tsx`, `web/src/pages/QualityEditorRoute.tsx`, `web/src/lib/routes.tsx`, `web/src/lib/useUnsavedGuard.ts`, `web/src/pages/Settings.tsx`, `web/src/pages/Indexers.tsx`, `web/src/pages/DownloadClients.tsx`, `web/src/pages/Convert.tsx`, `web/src/pages/Subtitles.tsx`
  - **Acceptance:**
    - Edit a profile and click Movies in the sidebar: a confirm appears. Keep editing keeps every change; Discard leaves. Back behaves the same way.
    - Refreshing /quality/movie/<key> reopens that profile.
    - A deleted profile's URL shows an error state, not a blank page.
    - Saving navigates back without a prompt.
    - A failed Duplicate shows an error toast.
  - **Tests:** vitest with createMemoryRouter: useUnsavedGuard blocks when dirty and passes through when clean or after save.; Manual: the new, edit, duplicate, cancel and save flows for the video, book and music builders; Settings edit, then a sidebar click.
  - **Depends on:** [FE-22](#fe-22), [FE-09](#fe-09), [FE-21](#fe-21), [FE-14](#fe-14)
  - **Risk:** The guard must not block the editor's own post-save navigation; clear dirty synchronously before navigate. QUAL edits the same builders, so coordinate the order.
  - **Resolves:** frontend-12, frontend-6

#### Milestone: M6 — Live instead of polling

_One websocket per tab drives Downloads, detail pages, lists, History and the Dashboard, with a slow fallback poll. Download progress arrives as a queue.changed event, and the sidebar shows live 'needs you' counts._

<a id="fe-26"></a>
- [ ] **FE-26 · Publish queue.changed from the server, and carry the torrent hash in list download status** — `P2` · `S` · Phase 9
  - **Problem:** The event bus has no download-progress topic. Even with a websocket-driven frontend, progress bars would still need 3-second polls of /api/v1/downloads, which hit qBittorrent on every call. The movie and episode list payloads (movies.DownloadStatus at movie.go:41, series.EpisodeDownload at series.go:130) carry only state and progress, with no hash, so a list can't patch one item from an event.
  - **Approach:** 1) internal/download/queuewatch.go
    - type QueueWatcher struct {svc queueSource; bus *eventbus.Bus; active func() bool; every time.Duration}, where queueSource is an interface {Queue(ctx) ([]Item, error)}.
    - Run(ctx): a 2s ticker.
      - Skip the tick when !active(), i.e. no websocket clients are connected (wired to hub.Count() > 0, or SEC's staff-client count once role filtering exists).
      - Call svc.Queue(ctx).
      - Build map[hash]{state, pct int (progress*100), dlspeed, eta}.
      - Compute diffQueue(prev, next): items whose state or whole-percent progress changed, plus removed hashes.
      - If non-empty, Publish('queue.changed', QueueDelta{Items, Removed}).
    - Payload: hashes and numbers only, no names. It is still a staff-only topic under SEC's policy.
    
    2) cmd/arrmada/main.go: construct the watcher after hub := realtime.NewHub (line 273) and the downloads service (line 142); go watcher.Run(runCtx). If ACQ's download snapshot (backend.t24) has landed, read from it instead of a second qBittorrent poll.
    
    3) Add Hash string `json:"hash,omitempty"` to movies.DownloadStatus and series.EpisodeDownload. Set it at internal/httpapi/activity.go:242 and series.go:177 (it.Hash).
    
    4) web/src/lib/api.ts: add hash?: string to the two download types (api.ts:698, 1862).
  - **Files:** `internal/download/queuewatch.go`, `internal/download/queuewatch_test.go`, `cmd/arrmada/main.go`, `internal/movies/movie.go`, `internal/series/series.go`, `internal/httpapi/activity.go`, `internal/httpapi/series.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With a websocket client connected and a torrent downloading, queue.changed arrives at most every 2s and only when a state or whole-percent progress changed.
    - With no clients connected, the watcher makes no qBittorrent calls.
    - /api/v1/movies list items that are downloading include download.hash.
  - **Tests:** Go TestDiffQueue: an unchanged sub-1% move gives no event; a state change gives an event; a removed hash appears in Removed.; Go TestWatcherSkipsWhenInactive, with a fake queueSource counting calls.; Go TestWatcherPublishes, with a fake bus subscriber.; Race tests in Docker before pushing.
  - **Depends on:** SEC — per-role topic filtering in internal/realtime/hub.go, so queue.changed is staff-only
  - **Risk:** It adds one qBittorrent poll every 2s while a staff tab is open. That replaces the per-tab 3s polls, so the load drops overall. If ACQ's snapshot lands first, reuse it to avoid duplicate polling.
  - **Resolves:** backend-4, frontend-14
<a id="fe-27"></a>
- [ ] **FE-27 · One shared websocket per tab and useLiveQuery: replace the hot polls with events** — `P2` · `M` · Phase 9
  - **Problem:** After FE-07, polls pause in hidden tabs but still run every 3-10s while visible. useLive (lib/useLive.ts) is used only by Dashboard (connected) and MovieDetail (last), and each consumer opens its own socket. Movies.tsx:135-141 refetches the whole library every 4s while anything is downloading.
  - **Approach:** 1) web/src/lib/live.tsx
    - <LiveProvider> opens one WebSocket. The URL goes through lib/base ([FE-29](#fe-29)), wss when on https.
      - Exponential backoff from 1s to 30s.
      - Closes on 'arrmada:signed-out'.
      - Exposes {connected, subscribe(patterns, handler)}. Patterns support 'movie.*' and '*'.
    - useLiveEvent(patterns, handler).
    - useLiveQuery(key, fetcher, {topics, fallbackMs = 30000, debounceMs = 500}), built on useQuery ([FE-21](#fe-21)) and usePoll ([FE-07](#fe-07)):
      - refetches on a matching topic, debounced;
      - keeps a slow fallback poll;
      - refetches on reconnect;
      - pauses while hidden.
    - lib/useLive.ts becomes a thin wrapper, or is deleted.
    - Mount LiveProvider in AppLayout only, until SEC's role filtering ships; then in UserLayout too.
    
    2) Migrate:
    - MovieDetail and SeriesDetail: movie.*, series.*, release.grabbed, download.imported, file.removed, plus queue.changed filtered to their hash. Fallback 30s.
    - Downloads: patch rows in place from queue.changed. Refetch on release.grabbed, download.imported and removed hashes. Fallback 10s.
    - History: *.imported, *.downloaded, import.held.
    - Movies and Series lists: patch download.progress by hash from queue.changed. Refetch the list only on movie.downloaded, series.imported or library.scanned. This removes Movies.tsx:138 and Series' equivalent.
    - Dashboard: 60s fallback. Plex now-playing stays a 15s poll while visible. 'Realtime: connected' reads from the provider.
    - Discover requests strip and NotificationBell: request.* topics when REQ publishes them, staff only until SEC; 60s fallback otherwise.
    - Logs, Insights, Subtitles, Convert: keep their usePoll intervals. They switch to job.updated when BE's job runner publishes it.
  - **Files:** `web/src/lib/live.tsx`, `web/src/lib/useLive.ts`, `web/src/components/AppLayout.tsx`, `web/src/components/UserLayout.tsx`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/SeriesDetail.tsx`, `web/src/pages/Downloads.tsx`, `web/src/pages/History.tsx`, `web/src/pages/Movies.tsx`, `web/src/pages/Series.tsx`, `web/src/pages/Dashboard.tsx`, `web/src/pages/Discover.tsx`, `web/src/components/NotificationBell.tsx`
  - **Acceptance:**
    - On an idle Downloads page, the Network panel shows at most 6 /api calls per minute (about 20 today), and progress still updates within about 2s while a torrent downloads.
    - The Movies grid progress bar advances without refetching /api/v1/movies.
    - A hidden tab makes no periodic API calls.
    - Each tab opens exactly one WebSocket, and Dashboard's 'Realtime: connected' reflects it.
    - Killing and restarting the server reconnects within 30s and refetches.
  - **Tests:** vitest with a fake WebSocket class:
- a matching topic triggers a refetch;
- debounce coalesces bursts;
- a pattern 'movie.*' matches 'movie.downloaded';
- the hidden-tab pause;
- refetch on reconnect;
- close on sign-out.; Manual before/after comparison in the Network panel, during a real test download in a scratch instance.
  - **Depends on:** [FE-07](#fe-07), [FE-21](#fe-21), [FE-26](#fe-26), SEC — role-filtered websocket topics before any requester page subscribes
  - **Risk:** The hub drops messages for slow clients and the bus drops when a buffer is full, so a page could go stale. The fallback poll and the refetch on reconnect bound that.
  - **Resolves:** frontend-14, backend-4
<a id="fe-28"></a>
- [ ] **FE-28 · Live 'needs you' badges in the sidebar** — `P2` · `S` · Phase 9
  - **Problem:** No sidebar entry carries a count, so pending requests, held reviews and failures are invisible unless the owner opens each page. The Sidebar fetches only the audiobook dot (Sidebar.tsx:20-25).
  - **Approach:** 1) Sidebar.tsx
    - Read the counts with useLiveQuery('attention', api.attention, {topics: ['attention.changed', 'request.*', 'import.held'], fallbackMs: 60000}).
    - api.attention calls GET /api/v1/attention, which the attention-feed epic provides as {requests, review, activity_errors, issues}.
    - Render a pill badge on items whose NavItem.badge matches (from [FE-16](#fe-16)):
      - accent (accent-soft fill, accent-text) for 'needs you' counts: requests, review;
      - reject tone for errors: activity, issues.
      - 99+ cap.
    - Keep the audiobook dot.
    
    2) AppLayout's mobile top bar shows a small accent dot on the hamburger when any count is above 0.
    
    3) document.title is prefixed with '(n) ' when there are actionable counts (requests + review).
    
    4) Items stay hidden when their module is off.
  - **Files:** `web/src/components/Sidebar.tsx`, `web/src/components/AppLayout.tsx`, `web/src/lib/nav.ts`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Requests shows the pending count, Review the held count, and Activity the failures. Each equals /api/v1/attention.
    - Approving a request decrements the badge within about 2s, without a reload.
    - Light and dark themes both look right, and the drawer still opens and closes.
  - **Tests:** vitest: the badge renders from mocked attention data and updates on a fake 'attention.changed' event.; Manual: approve a request in a scratch instance and watch the badge.
  - **Depends on:** [FE-16](#fe-16), [FE-27](#fe-27), OBS (or the epic that owns product.t7) — GET /api/v1/attention and the attention.changed topic, REQ — /requests route (product.t14), ACQ — /activity route (product.t30)
  - **Risk:** If the attention endpoint is late, ship the badges for Review only, using the existing /reviews count and import.held, then extend them.
  - **Resolves:** product-4, product-8

#### Milestone: M7 — Sub-path installs and consolidation

_ARRMADA_BASE_URL either works end to end or is removed. A named type scale with an 11px floor replaces 26 arbitrary sizes. Movies and Series share one LibraryGrid, and Discover is split into shared pieces that BooksDiscover reuses._

<a id="fe-29"></a>
- [ ] **FE-29 · Make ARRMADA_BASE_URL (reverse-proxy sub-path) work end to end, or remove it** — `P2` · `M` · Phase 16
  - **Problem:** The backend mounts every route under base (server.go:105, 477-482), and externalGate strips it (pathAfterBase). docker-compose.yml:41, the Dockerfile:198 healthcheck, install.sh:77 and update.sh:18 all pass ARRMADA_BASE_URL through.

The frontend hard-codes root paths:
- vite has no base, so index.html references /assets/… and /manifest.webmanifest.
- req(), the uploads (api.ts:1243, 1464) and the URL builders (api.ts:1260, 1318, 1577) use bare '/api/v1' paths.
- useLive uses /api/v1/ws, BrowserRouter has no basename, and the service worker registers at /sw.js.
- window.location assignments go to '/', '/discover', '/music', '/books' and '/series'.
- Server-built URLs are root-relative too: insights/activity.go:202 builds '/api/v1/insights/image?…', and usernotify.go:243 pushes '/discover'.
A sub-path install gets a blank page.
  - **Approach:** 0) Decision gate. Ask the owner whether a sub-path will ever be used.
    - If no: the S alternative is to remove ARRMADA_BASE_URL from config.go:92, docker-compose.yml, the Dockerfile healthcheck, install.sh, update.sh and the docs, and stop here.
    - Default: implement it, since the server half exists and the scripts advertise it.
    
    1) Server: webui.Handler(base string), called from server.go:477 with d.Config.BaseURL.
    - Template index.html once at startup: right after <head>, insert <base href='{base}/'> and <meta name='arrmada-base' content='{base}'>, with base passed through html.EscapeString.
    - The SPA fallback serves the templated bytes, no-cache.
    - A meta tag rather than an inline script keeps a future CSP simple.
    
    2) Build and static files
    - vite.config.ts: base './'.
    - index.html: relative manifest and icon hrefs.
    - manifest: start_url './discover', scope './'.
    
    3) web/src/lib/base.ts
    - BASE = the meta content, or ''.
    - url(p) = BASE + p.
    - asset(u) prefixes server-provided root-relative URLs (starting with '/' but not '//').
    
    4) Route every path through it:
    - send()/req(), the uploads and the download URL builders;
    - LiveProvider's ws URL;
    - SW registration url('/sw.js') with scope url('/');
    - router basename BASE ([FE-22](#fe-22));
    - <img src> for server thumbs: the Insights image proxy, now-playing art and similar, via asset().
    
    5) Replace the window.location assignments (Sidebar.tsx:28, UserLayout.tsx:23, ArtistDetail.tsx:240, BookDetail.tsx:441, SeriesDetail.tsx:272, and Login's replace from [FE-02](#fe-02)). In-app ones become navigate(); full reloads use url().
    
    6) sw.js derives the base from self.registration.scope and uses it for the SHELL entries, the /api bypass check, and a push URL that is root-relative (notificationclick prefixes it).
    - Rule: the server emits base-less paths; the client and the service worker add the base. usernotify.go therefore needs no change.
    
    7) grep for href='#…' fragment links, which resolve differently under <base>, and turn them into buttons.
  - **Files:** `internal/webui/embed.go`, `internal/webui/embed_test.go`, `internal/httpapi/server.go`, `internal/httpapi/server_base_test.go`, `web/vite.config.ts`, `web/index.html`, `web/public/manifest.webmanifest`, `web/public/sw.js`, `web/src/lib/base.ts`, `web/src/lib/api.ts`, `web/src/lib/live.tsx`, `web/src/lib/routes.tsx`, `web/src/main.tsx`, `web/src/App.tsx`
  - **Acceptance:**
    - With ARRMADA_BASE_URL=/arrmada behind nginx (location /arrmada/ proxied to :7878):
- /arrmada/ loads, login works, and reloading /arrmada/movies/12 works;
- the websocket connects at /arrmada/api/v1/ws;
- uploads, Plex sign-in and Insights images work;
- the PWA installs with scope /arrmada/, and a push notification opens /arrmada/discover.
    - A root install (no base) behaves exactly as before, and the Playwright suites still pass.
  - **Tests:** Go TestIndexInjectsBase: the handler with base '/x' serves <base href="/x/"> and the meta; the base is escaped.; Go server test with BaseURL '/x': GET /x/ returns 200 with the base; GET /x/assets/<file> serves the asset; GET /x/movies/1 returns index.html; GET /x/api/health returns 200.; Manual: docker compose with an nginx sidecar at /arrmada, on a scratch instance.; Race tests in Docker.
  - **Depends on:** [FE-04](#fe-04), [FE-22](#fe-22), [FE-27](#fe-27)
  - **Risk:** Every hard-coded root path has to be found. Grep for '"/api', '`/api', 'location.href', 'href="/' and server-built '/api/v1/' strings. <base href> changes how fragment-only links resolve.
  - **Resolves:** frontend-8, system-15
<a id="fe-30"></a>
- [ ] **FE-30 · Named type scale with an 11px floor for text that carries information** — `P3` · `M` · Phase 16
  - **Problem:** The UI uses 26 distinct arbitrary font sizes, from 8px to 38px. There are 130 uses of 8-9.5px (text-[9.5px] x70, [9px] x46, [8.5px] x11, [8px] x3) and 117 of 10px, mostly in --ink-faint, carrying timestamps, sizes, hints and counts.
  - **Approach:** 1) tailwind.config.js fontSize tokens, mapped to today's common sizes:
    - 2xs 10.5px, only for uppercase mono eyebrow labels with tracking;
    - xs 11px, sm 12px, base 12.5px, md 13.5px;
    - lg 15px, xl 17px, 2xl 20px, 3xl 24px;
    - display 30px and display-lg 38px.
    Note: this deliberately overrides Tailwind's default text-xs/sm/base/lg/xl. First grep for existing uses of those default classes and convert them in the same codemod.
    
    2) web/scripts/type-scale.mjs, a codemod:
    - rewrites text-[Npx] to the nearest token;
    - raises 8-10px to 11px (xs), or to 10.5px (2xs) when the className also has 'uppercase' and 'font-mono';
    - prints a per-file report.
    
    3) Before merging, the owner reviews before/after Playwright screenshots ([FE-10](#fe-10) harness) of Discover, Dashboard, the Movies grid and table, MovieDetail, Calendar, Settings and Downloads, in both themes.
    
    4) Then add an eslint no-restricted-syntax rule rejecting new text-[..px] arbitrary sizes.
  - **Files:** `web/tailwind.config.js`, `web/scripts/type-scale.mjs`, `web/eslint.config.js`, `web/e2e/screens.spec.ts`, `web/src/pages`, `web/src/components`, `web/src/ui`
  - **Acceptance:**
    - grep -E 'text-\[[0-9.]+px\]' web/src finds nothing.
    - No text renders below 10.5px.
    - The owner approves the screenshot set.
    - There is no overflow at 375px or 1440px (Discover captions, Downloads rows); the Playwright overflow specs pass.
  - **Tests:** The Playwright before/after screenshot spec (web/e2e/screens.spec.ts, run on demand, not in CI).; The Playwright overflow specs.; The lint rule in CI.
  - **Depends on:** [FE-01](#fe-01), [FE-10](#fe-10), [FE-14](#fe-14)
  - **Risk:** This is the one task that deliberately changes the type scale in a dense UI. Get explicit owner sign-off on the screenshots, and run it after the kit migrations so the codemod touches each file once.
  - **Resolves:** frontend-11, frontend-2
<a id="fe-31"></a>
- [ ] **FE-31 · One config-driven LibraryGrid for Movies and Series** — `P3` · `L` · Phase 10
  - **Problem:** Movies.tsx (755 lines) and Series.tsx (643 lines) still differ on only about 680 lines once the media noun is normalised. Fixes keep landing on one page and not the other: sort memory, table columns, filters, hover actions.
  - **Approach:** 1) web/src/features/library/LibraryGrid.tsx, generic over T, takes a LibraryConfig<T>:
    - noun;
    - list, delete, search and scan API functions;
    - filters: [{key, label, predicate}];
    - sort options;
    - card slots: badge, progress, hoverActions;
    - table columns;
    - AddModal (the add-movie flow versus SeriesSearchModal);
    - bulk actions (profile change, monitor);
    - a live patcher for queue.changed (from [FE-27](#fe-27)).
    
    2) It builds on useQuery ([FE-21](#fe-21)), useSearchParamState ([FE-24](#fe-24)), LibraryToolbar and FilterBar ([FE-19](#fe-19)), Modal, Toast and Confirm ([FE-09](#fe-09)), and StatusChip.
    
    3) Movies.tsx and Series.tsx become thin configs. Media-specific behaviour (episode counts, the Anime toggle, the misfits table filter at Movies.tsx:399) goes in slots, not boolean flags.
    
    4) Ship it in two commits:
    - (a) extract the shared card and table pieces while both pages still exist;
    - (b) switch both pages to configs.
  - **Files:** `web/src/features/library/LibraryGrid.tsx`, `web/src/features/library/types.ts`, `web/src/features/library/LibraryTable.tsx`, `web/src/features/library/LibraryCard.tsx`, `web/src/pages/Movies.tsx`, `web/src/pages/Series.tsx`, `web/e2e/library.spec.ts`
  - **Acceptance:**
    - Movies and Series behave as before:
- grid/table toggle and sort memory;
- filters and search in the URL;
- multi-select bulk profile;
- delete with files;
- Search now and Scan library;
- live progress.
    - Their combined line count, including the shared files, drops by at least 40%.
  - **Tests:** Playwright (mocked API) for both pages: the grid/table toggle, a filter, the delete confirm, and bulk profile.; Manual regression pass on both pages in both themes.
  - **Depends on:** [FE-11](#fe-11), [FE-19](#fe-19), [FE-21](#fe-21), [FE-24](#fe-24)
  - **Risk:** Subtle per-media differences (episode counts, the Anime toggle). Use slots so neither page loses behaviour. MOV and SER must not be mid-change on these files.
  - **Resolves:** frontend-2
<a id="fe-32"></a>
- [ ] **FE-32 · Split Discover.tsx into features/discover and share its row, card and sheet with Books** — `P3` · `M` · Phase 16
  - **Problem:** Discover.tsx is 1,421 lines. BooksDiscover re-declares LoadError (23), badgeFor (438) and ArrowBtn (640), plus its own hero, row and skeleton. BADGE_BG and its request modal are handled by FE-14.
  - **Approach:** 1) Pure moves first, into web/src/features/discover/:
    - DiscoverPage, Hero, PosterRow, SearchBox (the omnibox), DetailSheet (already on ui/Modal from [FE-09](#fe-09)), RequestsRow, MediaCard, LoadError, ArrowBtn;
    - rowRegistry: the cross-row de-duplication from Discover.tsx:826-869, with its render-order semantics kept.
    
    2) A generic PosterCard (image, title, subtitle, badge, progress, onOpen, quickAction), used by both MediaCard and the Books catalogue card.
    
    3) BooksDiscover imports Hero, PosterRow, LoadError, ArrowBtn and PosterCard.
    
    4) pages/Discover.tsx re-exports DiscoverPage, so routes don't change.
    
    5) Coordinate with APP: its detail-sheet routes (/discover/:media/:id) and requester shell touch the same code. Land after them, or have APP build on this split.
  - **Files:** `web/src/pages/Discover.tsx`, `web/src/features/discover`, `web/src/pages/BooksDiscover.tsx`, `web/e2e/discover.spec.ts`
  - **Acceptance:**
    - Discover and the Books tab render and behave exactly as before: hero rotation, rows, cross-row dedupe, the omnibox, the sheet and the requests strip.
    - No file in features/discover is longer than about 400 lines.
    - BooksDiscover no longer defines LoadError, badgeFor or ArrowBtn.
  - **Tests:** Playwright Discover smoke for the Movies and Books tabs (mocked API): rows render, the sheet opens, Esc closes it, and no title repeats across rows.; Manual check of both tabs, as requester and as admin.
  - **Depends on:** [FE-09](#fe-09), [FE-14](#fe-14), APP — Discover detail-sheet routes and requester-shell rework (same files)
  - **Risk:** The dedupe registry depends on render order, so keep that ordering intact when moving the code. Discover is the requester product, so test on a phone-width viewport too.
  - **Resolves:** frontend-2

#### Risks

- Merge collisions: FE-11 to FE-15, FE-19, FE-30 and FE-31 touch the same big page files that module epics are changing (Quality 86 KB, Convert 68 KB, Insights 65 KB, SeriesDetail 57 KB). Mitigation: one page per commit, land the kit early, and have module epics adopt the kit instead of adding new hand-built modals.
- Visual drift: the dark warm palette and terracotta accent must not change. Only FE-01 (contrast token values) and FE-30 (type scale) change visuals on purpose, and both need owner screenshot sign-off. FE-16 (nav) and FE-18 (widths) show before/after screenshots.
- Unexpected sign-outs if a 401 is misused. Only protected() and the auth handlers return 401; roles and externalGate return 403. Keep that contract, and add a Go test in SEC if it changes.
- Stale deploys after code splitting. FE-04 (asset 404 plus a versioned SW cache) must land before FE-06, and FE-03's reload-once guard prevents loops. Verify the first SW upgrade on a real phone PWA.
- The service-worker change ships to every installed PWA. A bug there could pin an old shell. Keep navigations network-first and the activate pruning simple, and test across two builds before pushing.
- The websocket hub drops messages for slow clients and the bus drops when full. Every live view keeps a fallback poll and refetches on reconnect.
- CI time grows with eslint, vitest and Playwright (target under 2 extra minutes). Chromium only, mocked API, browsers cached.
- The fixtures for the mocked API can drift from real shapes. Type them against lib/api.ts so tsc catches the drift.
- Owner muscle memory: the nav regroup (FE-16) and route changes (the Quality editor) keep all old URLs working through redirects.

#### Out of scope

- The requester phone shell itself: bottom tab bar, safe-area padding, PNG/maskable icons, the orientation lock, an agenda calendar, Discover detail-sheet routes and push prompts (APP).
- The content and auto-save model of the Settings and System hub, the connection cards and their copy (CFG).
- The /requests page, the Activity page merge, and the attention-feed backend (REQ, ACQ, OBS). FE only wires nav items and badges.
- Server-side websocket role filtering, sliding sessions, an account and password page, and session revocation (SEC).
- Copy rewrites beyond the crumbs and the NotFound page (COPY).
- Module page redesigns: Downloads posters, History dates, the books-12 detail toolbar, hover-only card actions, Insights' phone layout (ACQ, BOOK, PLEX and others).
- Server-side per-user theme storage.
- gzip of JSON API responses (BE).
- Adopting a third-party component library, TanStack Query or a CSS-in-JS system. The plan adds no new runtime dependencies.

