# APP — Requester app shell & mobile

_Part of the [Arrmada roadmap](../../ROADMAP.md). 19 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Turn the requester side of Arrmada (what family and friends install on their phones) into a real phone app. Nothing hidden should react to taps, nothing should scroll sideways, and it should install with a proper icon and sign in with Plex on an iPhone. Every title gets a link that Back, notifications and pushes respect. Audiobooks play in the browser with lock-screen controls. Notifications, the calendar and books go where non-technical people will find them.

**Why.** Family members mostly use Arrmada as the app they installed on their phones (iPhone push needs that), and today that shell breaks at phone width.
- **Accidental requests and approvals (discover-7, frontend-4, product-7).** Discover's '+ Request' is an invisible opacity-0 span inside the poster button (Discover.tsx:1111-1122), so tapping the lower-left of any poster files a request, which is auto-approved for Plex sign-ins. Admins can approve or decline blind through the invisible strip on request posters (Discover.tsx:637-653), and Decline has no confirmation.
- **The shell doesn't fit or install properly (discover-7, frontend-5, product-7).**
  - The header is one non-wrapping row about 500px wide (UserLayout.tsx:28-56), so every requester page scrolls sideways at 375px.
  - Nothing uses the safe-area insets that viewport-fit=cover asks for, so the installed iOS app draws under the clock.
  - The home-screen icon is an SVG, which iOS ignores, so it falls back to a screenshot.
  - The bell exists only on Discover (Discover.tsx:100).
  - 'Books' means the shelf in the top bar but the catalogue in Discover's tabs.
- **Nothing has a URL (discover-4, frontend-6).** Back exits Discover instead of closing a sheet. Notifications regex-parse the title out of the body and run a fuzzy search (NotificationBell.tsx:8-11, 64), so 'Dune' 2021 and 1984 can't be told apart. Web Push always opens /discover (usernotify.go:243).
- **/audiobooks is a settings form, not somewhere to listen (audiobooks-7).** The only app it names is Android-only, so iPhone users have no verified way to listen.
- **Push is hard to find (discover-13, product-7).** It sits behind a bare gear, next to a second section with the same 'Push notifications' title that asks family members for an Apprise URL.
- **The calendar doesn't work on a phone (discover-11, product-15, frontend-5).** It's a 7-column grid with 10px titles at every width. Episode details only appear in hover tooltips, '+N more' can't be tapped, items are dead ends, and there's no 'my requests' view and no iCal feed.
- **Plex sign-in can hang on iPhone (system-11).** The popup opens after an await, so Safari can block it, and the button then sits on 'Waiting for Plex…' for 3 minutes.
- **MyBooks is a flat grid (books-7).** It has no detail sheet and no series grouping, and it polls every 15 seconds even when hidden.

**Depends on:** SEC: agree to relaxing COOP to same-origin-allow-popups ([APP-04](#app-04)); review the token-authenticated /api/v1/calendar.ics reachable from outside the LAN ([APP-18](#app-18)); filter websocket topics by role so polling can move to realtime ([APP-19](#app-19)); AUD: the web player API on new /api/v1/me/audio routes backed by listening.Store (library, shelves, item detail with tracks and chapters, Range file stream, play/sync/close, bookmarks; draft audiobooks.t10) blocks [APP-09](#app-09) to [APP-11](#app-11). Place timeline components (draft audiobooks.t5) are optional for [APP-10](#app-10). iOS client verification (draft audiobooks.t8) sets the app statuses in [APP-12](#app-12); REQ: a routed requester /requests page (draft discover.t5) adds the Requests tab and request:<id> deep links ([APP-05](#app-05), [APP-08](#app-08)). The staff request.created alert (draft discover.t6) honours the new_request preference ([APP-15](#app-15)); BOOK: requests matched by book_id (draft books.t3) and request stage/note/next_check_at (draft books.t5) for [APP-19](#app-19); Send to Kindle (draft books.t13) is optional; OPDS stays in BOOK; FE: kit Modal/Sheet/Toast/useConfirm (draft frontend.t8) — [APP-06](#app-06) builds on it if it exists; router migration and basename (draft frontend.t20) must coordinate with [APP-07](#app-07)'s child routes and the push URL prefix; useTabParam (draft frontend.t15) for ?tab; Playwright harness (draft frontend.t11) for the 375px overflow and touch-tap specs; vitest for the pure helpers; PLEX: 'Link Plex account', so the owner's Plex sign-in resolves to their admin account (the second half of system-11), is PLEX's job; [APP-04](#app-04) fixes only the popup; MOV: MovieDetail has no 'Remove movie' action, and the grid's hover-only Remove was never reachable on touch (noted in [APP-01](#app-01))

#### Design

## Target experience
On a phone (<640px), every requester page has the same frame, top to bottom:
- A slim top bar with the mark, the section title, the bell and the avatar, padded below the status bar.
- The page content.
- An optional audiobook mini-player.
- A 56px bottom tab bar, padded above the home indicator.

**Tabs: Discover · Requests · Shelf · Listen · Me.**
- Until REQ ships a routed /requests page, Calendar takes the Requests slot. After that it moves under Me.
- External sessions never get Calendar.

**At 640px and wider,** today's top-bar layout stays, with the bell added. Staff keep the sidebar and drawer and also get the bell, the mini-player and /me.

## Layout contract (defined once)
- **CSS variables in index.css:**
  - `--tabbar-h` is 56px on phones while `<html class="has-tabbar">` is set.
  - `--player-h` is 64px while `has-player` is set.
  - `--bottom-chrome = --tabbar-h + --player-h + env(safe-area-inset-bottom)`.
  - `<main>` uses `.pb-chrome`, and toasts use `.bottom-above-chrome`.
- **z-index scale** in tailwind.config.js `zIndex`: tabbar 40 < player 41 < dropdown 45 < sheet 50 < toast 60.
- **Safe areas:** `.pt-safe` goes on top bars and `.px-safe` handles landscape notches. The tab bar and sheets pad by `env(safe-area-inset-bottom)`.
- **One nav config,** `web/src/lib/requesterNav.ts`, drives the header links, the bottom tabs and the section title, so they can't drift apart.
- **Sheet primitive:** `components/Sheet.tsx` plus `lib/useDialogA11y.ts`.
  - It is a bottom sheet on phones and a centred modal on desktop.
  - It handles the focus trap, Esc, backdrop click and the body scroll lock.
  - Back closes it, through a react-router state entry.
  - Every new sheet uses it, or FE's kit equivalent once that exists.

## Naming
| Where | Label | Route |
|---|---|---|
| Requester nav and tab for the ebook/audiobook shelf | My shelf (tab: Shelf) | /shelf (/books redirects to it for requesters) |
| Discover catalogue tab | Books | /discover?tab=books |
| Requester nav and tab for audiobooks | Listen | /audiobooks (tabs: Listen, Apps & devices, plus the admin tabs) |
| Staff sidebar | unchanged (Books, Audiobooks) | unchanged |

## URL map
- `/discover?tab=movies|series|books&q=…` puts the tab and the committed search in the URL. Nothing consumes and wipes them any more.
- `/discover/movie/:tmdbId` and `/discover/series/:tmdbId` are child routes; Discover stays mounted underneath them. `/discover/tv/:id` redirects to the series route. The segment equals the API media_type and the notification ref prefix.
- `/discover?tab=books&work=<book key>` opens the Books sheet.
- `/requests?focus=<id>` belongs to REQ.
- `/shelf` and `/audiobooks?tab=listen|apps|server|people|listening|import&book=<item_key>`.
- `/calendar?view=agenda|month`.
- `/me` and `/me#notifications`.
- Sign-in keeps the URL it was opened on, so a deep link survives logging in.

**Notification ref to path:** one rule, written twice — Go `requests.RefPath` and TS `refToPath` — with the same test vectors.
- Refs are parsed from the right: strip the `:approved`, `:declined`, `:ready` or `:r<n>` suffix, because book keys can contain ':' (Hardcover keys are `hc:<id>`).
- The push URL is `Config.BaseURL + RefPath(ref)`.

## Touch rules
- Tailwind `hoverOnlyWhenSupported` is on.
- Nothing interactive sits in an opacity-0 layer unless it is `pointer-events-none` until revealed.
- Nothing interactive is nested inside a `<button>`.
- Every hover action has a visible path on touch: the detail sheet, or caption buttons.
- Destructive actions ask for confirmation.
- `useCanHover()` decides what to render; CSS decides what is revealed.

## Built-in audiobook player
- **Structure:**
  - `PlayerProvider` sits at the app root (main.tsx, inside MeProvider) and owns one HTMLAudioElement.
  - Both layouts render a `MiniPlayer`. `FullPlayer` is a sheet.
  - Pure position math lives in `lib/playerMath.ts`.
- **Client contract with AUD.** AUD owns the server side through `listening.Store`, so the sync guards and history still apply, and may rename endpoints:
  - `GET /api/v1/me/audio/library`
  - `GET /api/v1/me/audio/shelves` (continue, continue_series, recent, finished)
  - `GET /api/v1/me/audio/items/{key}` returns tracks `[{ino,start_offset,duration,mime}]`, chapters `[{start,end,title}]`, versions and the place.
  - `GET …/items/{key}/file/{ino}` must support Range requests.
  - `POST …/items/{key}/play` takes `{device_id,device,client}` and returns `{sid,position}`.
  - `POST /api/v1/me/audio/sessions/{sid}/sync` takes `{current_time,time_listened,duration}` and returns `{position,held_position?}`.
  - `POST …/sessions/{sid}/close`.
  - `GET/POST/DELETE …/items/{key}/bookmarks`.
  - `POST /api/v1/me/audio/accept` (this one exists today).
- **Sync timing:** sync about every 15s while playing, and also on pause, seek end and track change. On pagehide, send a keepalive request.
- **Media Session:** lock-screen controls with cover art; previous/next track means chapter skip.
- **One player per browser:** a BroadcastChannel pauses other tabs when one starts playing.
- **Privacy:** the player talks only to the caller's own `/me/audio` endpoints. Admin views see time and a device name ('Arrmada web · iPhone'), never the title.

## Notifications
- **`/me` is the single settings home:**
  - The device push switch comes first. `lib/push.ts` reports one of: on, off, blocked, needs-install (iOS outside the home-screen app), insecure (http on the LAN) or unavailable (no push key).
  - Per-event checkboxes come next.
  - Apprise sits under 'Advanced'.
- **The bell** links to it with a labelled 'Settings' button.
- **Push prompt:** shown once, after the person's first successful request, using the same gesture-safe enable path.
- **Preferences:** stored in `users.notify_prefs` as JSON. A missing or unknown key means on.
  - The inbox row is always written, which keeps idempotency refs and the ready sweep intact.
  - Only Web Push and Apprise are gated.

## Calendar
- `CalendarItem` gains `tmdb_id`, `media_type`, `season`, `episode`, `episode_title` and `requested_by_me`.
- Views: Agenda is the default on phones and Month on desktop. A DaySheet opens for busy days.
- Items open the Discover sheet for requesters and the detail page for staff.
- `mine=1` keeps only the caller's own and subscribed requests.
- A personal ICS feed (`calendar_feeds` table, sha256 of the token, `GET /api/v1/calendar.ics?token=`) is allowed from outside the LAN and rate-limited. The token sits in the query string, which the request log never records.

## Data model and API changes
- **Migrations** take the next free numbers after whatever has landed (0090 or later):
  - `NNNN_user_notify_prefs.sql`: `ALTER TABLE users ADD COLUMN notify_prefs TEXT NOT NULL DEFAULT ''`
  - `NNNN_calendar_feeds.sql`: `calendar_feeds(user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, token_hash TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL)`
- **Endpoints:**
  - `GET /api/v1/media/{media}/{id}` adds `card` (library, request and progress state) and returns 404 for adult titles.
  - `POST /api/v1/auth/plex/pin?mode=redirect` builds the Plex forwardUrl on the server.
  - `GET/PUT /api/v1/me/notify-prefs`.
  - `GET /api/v1/calendar` gains `mine=1` and the new fields.
  - `POST/DELETE /api/v1/me/calendar-feed`.
  - `GET /api/v1/calendar.ics`.
  - `GET /api/v1/me/books` adds `description`, `series_name`, `series_position`, `subjects`, `audiobooks[].item_key`, and request `stage`/`note`/`next_check_at`.
- **Headers:** `Cross-Origin-Opener-Policy: same-origin-allow-popups`, pending SEC's agreement.

## Verification
- **Every UI task:**
  - The browser pane mobile preset (375×812, touch) and desktop at 1280.
  - The overflow check `document.documentElement.scrollWidth === document.documentElement.clientWidth`.
  - `npm run build`.
- **Backend changes:** Go tests, plus race tests in Docker before pushing.
- **Real devices:** the owner's iPhone home-screen app and an Android phone, for icons, push, Plex sign-in and the player.
- **Once FE's Playwright harness exists:** add the 375px overflow spec and the touch-tap 'no POST /requests' spec.

#### Milestone: M1 — Safe and usable on a phone today

_On a phone, no tap can file, approve or decline a request without a visible button. No requester page scrolls sideways at 375px. Adding Arrmada to the Home Screen gives the real icon. Sign in with Plex works on an iPhone and in the installed app._

<a id="app-01"></a>
- [x] **APP-01 · No invisible tap targets on touch: Discover/Books quick-request and request-strip Approve/Decline** — `P0` · `S` · Phase 0
  - **Problem:** On touch devices, opacity-0 hover overlays still take taps.
- MediaCard's '＋ Request' is a `<span role=button>` nested inside the card's `<button>` (Discover.tsx:1111-1122). It calls stopPropagation and ctx.doRequest, so tapping the lower-left of any poster files a request. Plex sign-ins get that request auto-approved.
- BooksDiscover.tsx:484-497 copies the same pattern.
- RequestPoster's Approve/Decline/Withdraw strip (Discover.tsx:637-653) is invisible but tappable. Decline has no confirmation. On touch there is no visible way to approve.
- Tailwind 3.4.17 runs without hoverOnlyWhenSupported, so sticky hover adds to the confusion.
  - **Approach:** 1. web/tailwind.config.js: add `future: { hoverOnlyWhenSupported: true }`. hover: and group-hover: then only apply under `@media (hover:hover) and (pointer:fine)`.
    2. New web/src/lib/useCanHover.ts:
       - `useCanHover(): boolean` reads `matchMedia('(hover: hover) and (pointer: fine)')` and listens for changes.
       - Read the initial value synchronously so nothing flashes.
    3. MediaCard (Discover.tsx:1073-1142):
       - Wrap the poster in `<div className='group relative' style={{aspectRatio:'2/3'}}>` and move the lift/scale group-hover transform from the button onto this wrapper.
       - Inside: the details `<button>` (poster, StatusChip, progress bar, and a decorative gradient overlay with `pointer-events-none` that says 'Details').
       - Only when `canHover && ctx.canRequest && requestable`, add a SIBLING `<button>` '＋ Request' positioned bottom-left with `opacity-0 pointer-events-none group-hover:opacity-100 group-hover:pointer-events-auto focus-visible:opacity-100 focus-visible:pointer-events-auto`.
       - Delete the span and its stopPropagation.
       - On touch, the whole poster opens the sheet, and the sheet already has a visible Request button.
    4. BooksDiscover card (BooksDiscover.tsx ~452-500): the same restructure and the same sibling button.
    5. RequestPoster (Discover.tsx:578-665):
       - Render the hover strip only when canHover, and add `pointer-events-none group-hover:pointer-events-auto group-focus-within:opacity-100 group-focus-within:pointer-events-auto`.
       - When !canHover, add a visible action row to the always-visible caption: staff on a pending request get 'Approve' and 'Decline' (min-h 32px, existing accent and neutral styles); the requester's own pending request gets 'Withdraw'.
       - The poster image itself has no tap actions.
    6. Decline always confirms first: `window.confirm('Decline “' + title + '”' + (name ? ' requested by ' + name : '') + '? They\'ll be told.')`, before api.declineRequest.
       - Apply this in RequestPoster and in Requests.tsx RequestRow (unrouted today; REQ will route it).
       - Switch to the kit's useConfirm when FE ships it.
    7. BookDetail.tsx:104: keep the full-cover 'Change cover' overlay only when canHover, with pointer-events gating. On touch, render a small visible 'Change cover' button under the cover.
    8. AuthorDetail.tsx:162: the caption overlay is decorative, so give it pointer-events-none.
    9. Audit with `rg -n 'opacity-0' web/src`: every interactive element in an opacity-0 layer must be pointer-events-none until it is revealed.
    10. Note in the commit message: staff grid actions on Movies/Series/Books (`hidden group-hover:grid|flex`) were never reachable on touch and stay desktop-only. MovieDetail has Search but no Remove; flag that to MOV.
  - **Files:** `web/tailwind.config.js`, `web/src/lib/useCanHover.ts`, `web/src/pages/Discover.tsx`, `web/src/pages/BooksDiscover.tsx`, `web/src/pages/BookDetail.tsx`, `web/src/pages/AuthorDetail.tsx`, `web/src/pages/Requests.tsx`
  - **Acceptance:**
    - With Chrome touch emulation at 375x812, and on a real phone: tapping anywhere on a Discover or Books poster, including the lower-left corner, opens the detail sheet. The Network panel shows no POST /api/v1/requests.
    - Desktop with a mouse: hovering shows '＋ Request', which still creates a request and shows the toast. Tab reaches the button and Enter requests.
    - Staff on touch see visible Approve/Decline under each pending request. Decline asks for confirmation before any network call. No tap on the poster image approves, declines or withdraws.
    - Requesters on touch see 'Withdraw' under their own pending requests.
    - React logs no validateDOMNesting warning on Discover or the Books tab.
    - The BookDetail cover picker is reachable on touch.
  - **Tests:** UI check (browser pane mobile preset with touch): tap the poster corners on Discover rows, search results, the hero and the Books tab. Tap request-strip posters as admin and as requester.; UI check (desktop): hover quick-request, keyboard Tab/Enter, hover Approve/Decline including the Decline confirm.; npm run build (tsc --noEmit) passes.; Once FE's Playwright harness lands: a hasTouch spec taps a poster at (10%,90%), expects role=dialog, and asserts no POST to /api/v1/requests.
  - **Risk:** hoverOnlyWhenSupported applies globally, so any control reachable only through sticky hover becomes unreachable on touch. Today that means the 5 opacity-0 overlays handled here, plus staff grid actions that were already unreachable on touch. Touch laptops report hover:hover and keep the desktop behaviour.
  - **Resolves:** discover-7, frontend-4, product-7
<a id="app-02"></a>
- [x] **APP-02 · No sideways scroll at 375px: requester header stopgap and a Discover toolbar that fits a phone** — `P0` · `S` · Phase 0
  - **Problem:** The UserLayout header (UserLayout.tsx:28-56) is a single non-wrapping justify-between row with a min-content width of about 450-520px. Every requester page therefore scrolls horizontally at 375-430px, and index.css has no overflow guard.

Discover's search also overflows:
- The input is w-[210px] focus:w-[300px].
- The dropdown is `absolute right-0 w-[340px]` (Discover.tsx:252, 261), so on a phone it runs off the left edge.
- The tabs and search share one wrapping row.
  - **Approach:** 1. UserLayout header stopgap ([APP-05](#app-05) replaces it):
       - The wordmark becomes `hidden sm:inline`, and the header gap becomes gap-2.
       - The nav becomes `min-w-0 flex-1 overflow-x-auto whitespace-nowrap thin-scroll`, with NavLink `px-2 sm:px-3`.
       - The avatar becomes flex-none.
    2. Discover toolbar (Discover.tsx:75-104):
       - The wrapper becomes `flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between`.
       - The tab strip becomes `flex min-w-0 overflow-x-auto thin-scroll`, with tab buttons `flex-none px-3 sm:px-4`.
       - The search wrapper becomes `w-full sm:w-auto`.
    3. SearchBox:
       - The input becomes `w-full sm:w-[210px] sm:focus:w-[300px]`.
       - The dropdown becomes `fixed inset-x-3 z-50 mt-2 sm:absolute sm:inset-x-auto sm:right-0 sm:w-[340px]`, the same fix NotificationBell.tsx:76-80 already has.
    4. Sweep the other requester pages at 320px and 375px:
       - MyBooks filter row.
       - Audiobooks Field copy rows and tab strip.
       - Calendar header controls: let them wrap.
       - Fix anything wider than the viewport with min-w-0, flex-wrap or break-all on long values.
       - Do NOT add a global overflow-x:hidden. It hides bugs and breaks position:sticky.
  - **Files:** `web/src/components/UserLayout.tsx`, `web/src/pages/Discover.tsx`, `web/src/pages/MyBooks.tsx`, `web/src/pages/Audiobooks.tsx`, `web/src/pages/Calendar.tsx`
  - **Acceptance:**
    - At 320, 375, 390 and 430px, as a LAN requester and as an external session, document.documentElement.scrollWidth === clientWidth on /discover (every tab, and with the search dropdown open), /calendar, /books and /audiobooks.
    - The Discover search dropdown stays fully inside the viewport at 375px.
    - At 640px and wider the requester header and Discover toolbar look as they do today.
  - **Tests:** UI check: browser pane mobile preset, plus a javascript_tool snippet that asserts no overflow on each route and tab.; npm run build passes.
  - **Risk:** Low. The header part is a stopgap that APP-05 replaces, so keep it to class changes only.
  - **Resolves:** discover-7, frontend-5, product-7
<a id="app-03"></a>
- [x] **APP-03 · PNG app icons, maskable icon, notification badge and manifest fixes** — `P1` · `S` · Phase 7
  - **Problem:** apple-touch-icon points at /icon.svg (web/index.html:10). iOS ignores SVG touch icons and falls back to a page screenshot.
- The manifest ships only the SVG, reuses it as the maskable icon, and locks orientation to portrait.
- sw.js uses icon.svg for both the push icon and the badge, but Android needs a monochrome PNG badge.
- web/public has no PNGs at all.
  - **Approach:** 1. Add web/scripts/gen-icons.mjs and an `npm run icons` script in web/package.json. It uses the devDependency @resvg/resvg-js; nothing is added at runtime.
       - It renders web/public/icon.svg, which is already full-bleed with the mark inside the 80% maskable safe zone. Outputs: apple-touch-icon.png (180, opaque), icon-192.png, icon-512.png, icon-maskable-512.png (same art; verify with maskable.app) and favicon-32.png.
       - Add a new web/public/badge.svg (just the three-layer mark, white on transparent) and render it to badge-96.png.
       - Commit the PNGs; the script is only for regenerating them.
    2. web/index.html:
       - Add `<link rel='apple-touch-icon' sizes='180x180' href='/apple-touch-icon.png'>`. iOS also probes that path by default.
       - Add a PNG favicon fallback `<link rel='icon' type='image/png' sizes='32x32' href='/favicon-32.png'>`.
       - Keep the SVG favicon.
    3. manifest.webmanifest:
       - Icons become 192 any, 512 any and 512 maskable (all PNG), plus the SVG as any.
       - Remove 'orientation'.
       - Change the description to 'Discover, request and listen'.
       - Leave start_url and scope unchanged; changing them makes browsers treat it as a different installed app.
    4. web/public/sw.js:
       - Bump CACHE to 'arrmada-v2'.
       - Add the PNGs to SHELL.
       - showNotification uses icon '/icon-192.png' and badge '/badge-96.png'.
    5. Confirm internal/webui serves the PNGs as image/png (http.ServeFileFS picks the type by extension).
    6. Release note: iOS caches touch icons, so existing home-screen installs need to be removed and re-added. Paths become base-relative when FE's basename work lands.
  - **Files:** `web/scripts/gen-icons.mjs`, `web/package.json`, `web/public/badge.svg`, `web/public/apple-touch-icon.png`, `web/public/icon-192.png`, `web/public/icon-512.png`, `web/public/icon-maskable-512.png`, `web/public/badge-96.png`, `web/public/favicon-32.png`, `web/index.html`, `web/public/manifest.webmanifest`, `web/public/sw.js`
  - **Acceptance:**
    - Add to Home Screen on iOS shows the Arrmada mark, not a screenshot.
    - An Android install shows a correctly padded adaptive icon.
    - DevTools > Application > Manifest shows no icon warnings, and Lighthouse's installability and maskable checks pass.
    - The installed app rotates with the device.
    - An Android push notification shows the icon and the monochrome badge.
    - GET /icon-192.png returns 200 with image/png.
  - **Tests:** Lighthouse PWA audit on /discover.; curl -I against /icon-192.png, /badge-96.png and /apple-touch-icon.png.; Manual: iOS and Android install, plus a test push.
  - **Risk:** The service worker cache bump forces one reload of cached assets for installed users, which is expected. iOS icon caching means existing installs keep the old icon until re-added.
  - **Resolves:** frontend-5, product-7
<a id="app-04"></a>
- [x] **APP-04 · Sign in with Plex works when the popup is blocked, on iPhone and in the installed app** — `P1` · `S` · Phase 7
  - **Problem:** Login.tsx:25-26 opens the Plex popup only after `await api.plexLoginStart()`, outside the click gesture, which Safari and iOS commonly block. The popup is never null-checked, so the button sits on 'Waiting for Plex…' for up to 90×2s.

The server also sends `Cross-Origin-Opener-Policy: same-origin` (middleware.go:55). That severs the opener's handle to a cross-origin popup, so:
- popup.close() at Login.tsx:32 already does nothing.
- The obvious 'stop when popup.closed' fix would fire immediately.
  - **Approach:** 1. internal/httpapi/middleware.go securityHeaders: change COOP to `same-origin-allow-popups`. Arrmada stays isolated from cross-origin openers but keeps handles to popups it opens, which is the standard OAuth-popup setting. SEC must agree.
    2. Login.tsx plexLogin becomes a synchronous click handler:
       - `standalone = matchMedia('(display-mode: standalone)').matches || navigator.standalone`.
       - `popup = standalone ? null : window.open('', 'plex-auth', 'width=620,height=720')`, called BEFORE any await.
       - `mode = popup ? 'popup' : 'redirect'`.
    3. api.ts: `plexLoginStart(mode)` calls POST /api/v1/auth/plex/pin?mode=redirect|popup.
    4. Server side:
       - internal/plex/oauth.go: `AuthURL(clientID, code, product, forwardURL string)` sets `forwardUrl` when it is non-empty.
       - handlePlexLoginStart builds forwardURL itself when mode=redirect: scheme (requestIsHTTPS) + r.Host + Config.BaseURL + '/?plexpin=' + pin.ID. r.Host is validated as host[:port], with no '/', '@' or whitespace.
       - No client-supplied URL is accepted, so this can't become an open redirect.
       - Factor this into `plexForwardURL(r, base string, pinID int) (string, bool)`.
    5. Popup mode:
       - Set `popup.location.href = auth_url` and poll every 2s.
       - Each tick, check popup.closed; if closed, stop with 'Plex window closed — try again'.
       - Show a 'Cancel' link while waiting. After 5s, also show 'Plex window didn't open? Continue in this tab', which restarts in redirect mode with a new PIN.
       - On success, call popup.close().
    6. Redirect mode: set sessionStorage 'arrmada.plexpin' = id and 'arrmada.returnTo' = current path+search (try/catch), then `window.location.href = auth_url`.
    7. On Login mount, if ?plexpin= is present:
       - Accept it only if it equals sessionStorage's PIN. This guards against login CSRF: a crafted link can't sign a victim into someone else's account.
       - Poll up to 15×2s, and strip the param with history.replaceState.
       - On success, go to returnTo (same-origin path only) or /discover. A mismatched PIN is silently ignored.
    8. Account linking for the owner's Plex identity is PLEX's work, not this task's.
  - **Files:** `web/src/pages/Login.tsx`, `web/src/lib/api.ts`, `internal/plex/oauth.go`, `internal/plex/oauth_test.go`, `internal/httpapi/auth_plex.go`, `internal/httpapi/auth_plex_test.go`, `internal/httpapi/middleware.go`, `internal/httpapi/security_test.go`
  - **Acceptance:**
    - On iPhone Safari and in the installed PWA, Sign in with Plex completes through a same-tab redirect and lands on the page the person started from.
    - On desktop, closing the Plex popup stops 'Waiting for Plex…' within about 2s. On success the popup closes itself.
    - If the popup is blocked, 'Continue in this tab' completes the sign-in.
    - Opening /?plexpin=<someone else's pin> does not sign anyone in.
  - **Tests:** Go TestPlexAuthURLForwardURL: forwardUrl is present and escaped when given, and absent when empty.; Go TestPlexForwardURL: http and https (X-Forwarded-Proto through requestIsHTTPS), BaseURL prefix, and rejection of a Host containing '/' or '@'.; Go TestSecurityHeadersCOOP: the header is same-origin-allow-popups.; UI check: Chrome with popups blocked, desktop popup closed early, iOS Safari, iOS home-screen app.
  - **Depends on:** SEC (ack the COOP change to same-origin-allow-popups)
  - **Risk:** Relaxing COOP is a security-header change. If SEC declines it, drop the popup.closed check and rely on Cancel plus 'Continue in this tab'. Plex's forwardUrl behaviour inside an iOS home-screen app opens an in-app browser and should return into the app; verify that on a real iPhone first.
  - **Resolves:** system-11

#### Milestone: M2 — Requester phone shell

_Requesters get a proper phone app frame:
- a bottom tab bar
- safe-area padding on iPhone
- the bell on every page (staff too)
- a 'My shelf' name that no longer clashes with Discover's Books
- a basic Me page
- a shared bottom sheet that Back closes_

<a id="app-05"></a>
- [x] **APP-05 · Requester phone shell: bottom tab bar, safe areas, bell in every layout, 'My shelf' naming, basic Me page** — `P1` · `M` · Phase 7
  - **Problem:** The requester shell has no phone navigation.
- NotificationBell is mounted only inside Discover.tsx:100, so Calendar, Books and Audiobooks have no inbox, and staff see it only on Discover.
- index.html:5,13 sets viewport-fit=cover and black-translucent, but nothing uses env(safe-area-inset-*). The installed iOS app draws its header under the status bar and Dynamic Island, and the bottom-5 toasts sit on the home indicator.
- 'Books' in the top bar is the shelf (UserLayout.tsx:18), while 'Books' in Discover's tabs is the catalogue (Discover.tsx:20).
  - **Approach:** 1. New web/src/lib/requesterNav.ts, one array of {key, to, label, tabLabel, icon, show(ctx)} that drives header links, tabs and the section title. Entries:
       - Discover → /discover.
       - Requests → /requests, shown only when REQ's routed page exists (one REQUESTS_ROUTE constant that REQ flips).
       - Calendar → /calendar, not for external sessions. On phones it is a tab only while Requests is absent; after that it moves under Me.
       - My shelf → /shelf, only when booksEnabled; tab label 'Shelf'.
       - Listen → /audiobooks.
       - Me → /me, as a phone tab only.
    2. New web/src/components/ShellIcons.tsx: six inline SVG icons (24 viewBox, stroke 2, currentColor), matching the existing hamburger and bell stroke style.
    3. UserLayout.tsx:
       - The top bar gets `.pt-safe .px-safe` and holds the mark (wordmark `hidden md:inline`), the section title on phones, inline NavLinks `hidden sm:flex`, NotificationBell, and the avatar menu.
       - Avatar menu items are 44px tall: 'Me & notifications' → /me, 'Audiobook apps & password', 'Sign out'.
    4. New web/src/components/BottomTabs.tsx:
       - `sm:hidden fixed inset-x-0 bottom-0 z-tabbar` with aria-label 'Primary'.
       - Height is 56px plus padding-bottom env(safe-area-inset-bottom). Background is var(--sidebar) with a top border of var(--line). The active tab uses var(--accent) on a var(--accent-soft) pill.
       - It hides while a text input or textarea has focus on phones (focusin/focusout), so it doesn't ride above the keyboard.
       - UserLayout adds class has-tabbar to `<html>` on mount and removes it on unmount.
    5. index.css layout contract:
       - `:root{--tabbar-h:0px;--player-h:0px;--bottom-chrome:calc(var(--tabbar-h) + var(--player-h) + env(safe-area-inset-bottom,0px))}`.
       - `@media (max-width:639px){:root.has-tabbar{--tabbar-h:56px}}` and `:root.has-player{--player-h:64px}`.
       - Utilities .pt-safe (padding-top:max(10px,env(safe-area-inset-top))), .px-safe, .pb-chrome and .bottom-above-chrome (bottom:calc(var(--bottom-chrome) + 20px)).
       - tailwind.config.js theme.extend.zIndex: {tabbar:'40', player:'41', dropdown:'45', sheet:'50', toast:'60'}, documented in one comment.
       - UserLayout's `<main>` gets pb-chrome.
    6. Toasts: replace `fixed bottom-5` with `fixed bottom-above-chrome` in the 16 page toasts (`rg 'fixed bottom-5' web/src`). If FE's kit Toast has landed, give the kit Toast that offset instead.
    7. Bell everywhere:
       - Remove NotificationBell from Discover.tsx:100.
       - Mount it in UserLayout's top bar, in AppLayout's compact bar (lg:hidden, ml-auto), and in the Sidebar brand row on lg.
       - AppLayout's compact bar gets pt-safe/px-safe, and the Sidebar drawer gets padding-top env(safe-area-inset-top).
       - The search dropdown moves to z-dropdown.
    8. Naming:
       - In the requester and external trees, /shelf renders MyBooks and /books redirects to /shelf (`<Navigate replace>`), so installed shortcuts keep working.
       - MyBooks h1 becomes 'My shelf'.
       - Discover's catalogue tab stays 'Books', and the staff Sidebar is unchanged.
    9. New minimal web/src/pages/Me.tsx at /me in all three trees:
       - Avatar initial, username and role.
       - Rows: 'Calendar' (LAN only), 'Audiobook apps & password' → /audiobooks, 'Sign out'.
       - [APP-13](#app-13) adds the notification sections.
    10. Verify that RequestDetailModal (z-50) and the other sheets cover the tab bar.
  - **Files:** `web/src/lib/requesterNav.ts`, `web/src/components/ShellIcons.tsx`, `web/src/components/BottomTabs.tsx`, `web/src/components/UserLayout.tsx`, `web/src/components/AppLayout.tsx`, `web/src/components/Sidebar.tsx`, `web/src/components/NotificationBell.tsx`, `web/src/pages/Discover.tsx`, `web/src/pages/MyBooks.tsx`, `web/src/pages/Me.tsx`, `web/src/App.tsx`, `web/src/index.css`, `web/tailwind.config.js`
  - **Acceptance:**
    - At 360 and 375px, the bottom tabs show only routes this session can reach. External sessions get Discover, Shelf, Listen and Me; LAN requesters also get Calendar until Requests lands. The active tab is highlighted, nothing is hidden behind the bar, and nothing scrolls horizontally.
    - In the installed iPhone app, the header sits below the status bar and Dynamic Island, the tab bar clears the home indicator, and toasts appear above the tab bar.
    - The bell with its unread badge appears on every requester page and every staff page, and only one bell instance renders.
    - The nav and page title read 'My shelf'. Discover's catalogue tab still reads 'Books'. A requester's old /books bookmark lands on /shelf, and staff /books is unchanged.
    - At 640px and wider, the header looks as it does today plus the bell, with no bottom bar.
    - Opening the Discover detail sheet covers the bar. Typing in Discover search on Android doesn't push the bar over the keyboard.
  - **Tests:** UI check at 360, 375, 430, 768 and 1280px as LAN requester, external session and staff.; UI check: the computed style shows env(safe-area-inset-*) padding applied. Verify on the owner's iPhone home-screen app.; npm run build passes.; Once FE's Playwright harness exists, add the 375px overflow spec for the requester routes.
  - **Depends on:** [APP-02](#app-02), REQ (routed requester /requests page, draft discover.t5, adds the Requests tab), FE (kit Toast, draft frontend.t8) optional
  - **Risk:** A fixed bar can collide with sheets, toasts and the coming mini-player. The z scale and --bottom-chrome are defined once to prevent that, and every requester page needs checking. On iOS, a keyboard over a fixed bar is handled by hiding the bar while an input has focus. Any later tab renames should happen only in requesterNav.ts.
  - **Resolves:** discover-7, frontend-5, product-7
<a id="app-06"></a>
- [x] **APP-06 · Shared bottom Sheet that Back closes (phones) and dialog a11y hook** — `P1` · `S` · Phase 7
  - **Problem:** This epic needs about six new sheets: day lists, the push prompt, the audiobook BookSheet, the full player, the MyBooks detail and the Calendar subscribe dialog. The app has no sheet primitive; RequestDetailModal carries its own focus-trap, Esc and scroll-lock code inline (Discover.tsx:1192-1215). In the installed app, Android Back and the iOS swipe leave the page instead of closing whatever is open (discover-4).
  - **Approach:** 1. Extract web/src/lib/useDialogA11y.ts(ref, onClose) from RequestDetailModal. It handles:
       - Moving focus in and trapping Tab.
       - Esc to close (capture phase).
       - Restoring focus on close.
       - Locking body scroll.
       Switch RequestDetailModal to the hook; there is no visual change.
    2. New web/src/components/Sheet.tsx with props {open, onClose, title, children, footer?, closeOnBack = true}.
       - Below 640px it is a bottom sheet: fixed inset-x-0 bottom-0 rounded-t-2xl, max-h-[90dvh], overflow-y-auto, padding-bottom env(safe-area-inset-bottom), and a grab-handle visual.
       - At 640px and wider it is a centred modal, max-w-[560px].
       - The backdrop closes it, it uses z-sheet, and it uses the existing panel, line and shadow tokens.
    3. closeOnBack: on open, push a router entry with `navigate(location, {state:{...location.state, sheet:id}})`.
       - When location.state.sheet no longer equals id (the user pressed Back), call onClose.
       - A programmatic close calls navigate(-1) while that entry is still current.
       - Use router navigation rather than raw history.pushState, so BrowserRouter's index stays consistent.
    4. If FE's kit Modal has landed, build Sheet as a variant of it instead of in parallel.
  - **Files:** `web/src/lib/useDialogA11y.ts`, `web/src/components/Sheet.tsx`, `web/src/pages/Discover.tsx`
  - **Acceptance:**
    - Esc, a backdrop tap, Android Back and the iOS swipe-back each close an open Sheet without leaving the page.
    - Focus returns to the element that opened the sheet, and the page behind doesn't scroll.
    - The sheet respects the bottom safe area and sits above the tab bar and mini-player.
    - RequestDetailModal behaves exactly as before.
  - **Tests:** UI check with a test harness page or the first consumer: open, Esc, backdrop, browser Back, focus return, at 375px and on desktop.; npm run build passes.
  - **Depends on:** [APP-05](#app-05), FE (kit Modal, draft frontend.t8) — build on it if it exists
  - **Risk:** Router-state entries can stack if a sheet opens another sheet. Allow one sheet entry at a time, or replace rather than push when a sheet swaps its contents.
  - **Resolves:** discover-4, frontend-5

#### Milestone: M3 — Every title has a link

_Each Discover title has its own URL. Back closes the sheet, links can be shared and opened cold, and bell and push notifications open the exact title, or the exact book on the Books tab._

<a id="app-07"></a>
- [x] **APP-07 · Discover titles get real URLs: /discover/movie/:id and /discover/series/:id, tab and search in the URL** — `P1` · `M` · Phase 7
  - **Problem:** Whether a detail sheet is open is local useState in MediaCard, Hero and SearchBox (Discover.tsx:1074/1139, 422/525, 177/305). Back leaves Discover, and titles can't be shared or opened cold.
- ?q and &tab are read once and then wiped with setParams({}) (Discover.tsx:35-50).
- handleMediaDetail (internal/httpapi/discover.go:253) returns no library or request state, so a cold link can't show the right badge.
- MediaDetails never applies the always-on adult filter to the title itself; only the 'similar' list is filtered.
- After sign-in, Login hard-codes /discover (Login.tsx:32, 46), so a shared link is lost.
  - **Approach:** 1. App.tsx, all three trees (external, requester, staff):
       - `/discover` becomes a parent route with children `movie/:tmdbId` and `series/:tmdbId`, both rendering DiscoverTitleRoute. The segment equals the API media_type, so api.mediaDetail and notification refs map 1:1.
       - Add `tv/:tmdbId` as a redirect to the series route.
       - Check that the requester and external `*` redirects don't swallow these routes.
    2. Discover renders `<Outlet context={ctx}/>`. The parent stays mounted under the child route, so rows, loaded data and the `<main>` scroll position survive. Don't key Discover on location.
    3. URL state:
       - `tab = params.get('tab') ?? 'discover'` and the committed search is `params.get('q') ?? ''`.
       - setTab pushes ?tab=; 'See all' or Enter pushes ?q= (keeping tab); clearing the search replaces the entry.
       - Delete the consume-and-wipe effect and bookSeed. When tab=books, BooksDiscover's initialQuery comes from ?q.
       - Use FE's useTabParam if it has landed.
    4. New web/src/lib/refLink.ts with `titlePath(media, tmdbId)`. The three local modal mounts become `navigate(titlePath(c) + location.search, {state:{card:c, depth:1}})`.
    5. DiscoverTitleRoute (in Discover.tsx):
       - Reads useParams, useOutletContext<RowCtx>() and location.state?.card.
       - Passes RequestDetailModal either that card or a stub {media_type, tmdb_id}; the stub is filled from the detail response's `card`.
       - 'More like this' (setCurrent at Discover.tsx:1391) becomes `navigate(titlePath(s)+search, {state:{card:s, depth:depth+1}})`, and the modal re-fetches when the route params change.
       - onClose: if depth is set, navigate(-depth); otherwise navigate({pathname:'/discover', search}, {replace:true}).
    6. Backend handleMediaDetail:
       - Respond with `struct{ *metadata.MediaDetail; Card discoverCard `json:"card"` }`, where Card = a.enrichCards(ctx, []metadata.DiscoverItem{itemFromDetail(d)})[0].
       - New helper itemFromDetail maps MediaType, TMDBID, Title, Year, Overview, PosterURL, BackdropURL and Genres.
       - api.ts: MediaDetail gains `card?: DiscoverCard`.
    7. Adult filter: MediaDetails parses TMDB's `adult` into a new MediaDetail.Adult (json:"-"). handleMediaDetail returns 404 when d.Adult || adultfilter.Matches(d.Title), so a deep link can never surface a title the rows filter out.
    8. The sheet header gets a 'Share' icon button: navigator.share({title, url}) where available, otherwise copy to the clipboard and show a toast.
    9. Login: after password or Plex sign-in, go to the current path+search (a same-origin path; '/' becomes '/discover') instead of /discover. App already renders `<Login/>` at the requested URL.
  - **Files:** `web/src/App.tsx`, `web/src/pages/Discover.tsx`, `web/src/pages/BooksDiscover.tsx`, `web/src/lib/refLink.ts`, `web/src/lib/api.ts`, `web/src/pages/Login.tsx`, `internal/httpapi/discover.go`, `internal/httpapi/discover_detail_test.go`, `internal/metadata/discover.go`
  - **Acceptance:**
    - Clicking a poster changes the URL to /discover/movie/<id> or /discover/series/<id>. Android Back or a PWA swipe-back closes the sheet, and the list keeps its scroll position.
    - Reloading /discover/series/1399 opens that sheet directly with the correct badge (In library, Pending, or requestable). /discover/tv/1399 redirects there.
    - After two 'More like this' hops, Back returns to the previous title and the close button returns to the list.
    - Reloading /discover?tab=series&q=dune restores both the tab and the search.
    - Title routes work for staff, requester and external sessions.
    - A signed-out person opening a shared title link lands on that title after signing in.
    - An adult title's detail URL returns 404 and the sheet shows 'Not available'.
  - **Tests:** Go TestMediaDetailIncludesCard: with a fake Discovery, library and request, card.in_library, card.has_file and card.request_status are set.; Go TestMediaDetailHidesAdult: both the TMDB adult flag and an adultfilter title match return 404.; UI check: Back on mobile emulation, reloading a deep link, opening a link as a second user, an external session opening /discover/movie/<id>, and sign-in from a deep link.; npm run build passes.
  - **Depends on:** FE (router migration / createBrowserRouter, basename — coordinate so the child routes land in whichever router is current)
  - **Risk:** Discover is mounted in three route trees, so missing the child routes in one sends deep links to the redirect. Keying Discover on location would reset rows and scroll. The detail endpoint is uncached, so deep links add TMDB load until Discover detail caching lands (draft discover.t23, another epic).
  - **Resolves:** discover-4, frontend-6
<a id="app-08"></a>
- [x] **APP-08 · Notifications and Web Push open the exact title (ref-based links, BaseURL-aware)** — `P1` · `S` · Phase 7
  - **Problem:** NotificationBell regex-parses a quoted title out of the body (NotificationBell.tsx:8-11) and navigates to a fuzzy search (line 64). Every row already carries a structured ref (usernotify.go:156-161, typed in api.ts:299), but it goes unused, so 'Dune' 2021 and 1984 are ambiguous.

Web Push always opens '/discover' (usernotify.go:243) and ignores ARRMADA_BASE_URL. Decision refs carry suffixes such as 'movie:123:approved', and book keys can contain ':' (Hardcover keys are 'hc:<id>', metadata/hardcover.go:27).
  - **Approach:** 1. web/src/lib/refLink.ts `refToPath(ref): string | null` parses from the right:
       - First strip a trailing known suffix: ':approved', ':declined', ':ready', or REQ's future ':r<digits>'.
       - 'movie:<id>' → /discover/movie/<id>.
       - 'series:<id>' → /discover/series/<id>.
       - 'book:<key>' → /discover?tab=books&work=<encodeURIComponent(key)>.
       - 'request:<id>' → /requests?focus=<id> once REQ's route exists, otherwise /discover.
       - Anything else → null.
    2. NotificationBell.clickItem: use refToPath(n.ref). Keep today's title search only as the fallback for legacy rows with an empty or unknown ref. Close the panel, then navigate.
    3. BooksDiscover gets an `initialWork` prop, read from ?work=. It opens BookRequestModal with a stub card {key}; the modal already fetches bookDiscoverDetail(key). Closing removes `work` with replace.
    4. Go, internal/requests/usernotify.go:
       - Add `RefPath(ref string) string` with identical rules.
       - notifyParties passes `s.basePath + RefPath(ref)` to push.SendToUserAsync instead of '/discover'.
       - The Service gains SetBasePath(string), wired from cfg.BaseURL in cmd/arrmada/main.go next to SetPushSender (main.go:434).
    5. web/public/sw.js notificationclick: tab.navigate(url) rejects for uncontrolled clients, so catch that and fall back to clients.openWindow(url).
    6. Keep one test-vector table, copied into both the Go and TS tests, with a comment pointing at the twin.
  - **Files:** `web/src/lib/refLink.ts`, `web/src/components/NotificationBell.tsx`, `web/src/pages/BooksDiscover.tsx`, `web/src/pages/Discover.tsx`, `web/public/sw.js`, `internal/requests/usernotify.go`, `internal/requests/usernotify_test.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - Tapping a 'ready' inbox notification or its Web Push opens that exact title; Dune 2021 and Dune 1984 are distinguished.
    - Approved and declined notifications open the title.
    - Book notifications open the book's sheet on the Books tab, including Hardcover ('hc:') keys.
    - Legacy notifications without a ref still land on a search.
    - With ARRMADA_BASE_URL=/arr, a push opens /arr/discover/movie/<id>.
  - **Tests:** Go TestRefPath: movie, series, ':approved' and ':declined' suffixes, OL book key, 'book:hc:123:approved', URL escaping, unknown ref.; Go TestReadyPushURL: a fake PushSender captures the URL, including the base path.; UI check: bell click, Android Chrome push click, iPhone home-screen app push click.
  - **Depends on:** [APP-07](#app-07), REQ (request:<id> refs and the routed /requests page)
  - **Risk:** BOOK's book_id and alias work (draft books.t3) may change book refs, so RefPath must keep accepting the old 'book:<olkey>' form.
  - **Resolves:** discover-4, frontend-6

#### Milestone: M4 — Listen in Arrmada

_Family members, including iPhone users, can listen to audiobooks in the browser or home-screen app:
- a Listen tab with shelves and a book sheet
- a mini-player that persists across pages
- lock-screen controls
- a full player with chapters, speed, sleep timer and bookmarks
- a setup checklist for third-party apps that ticks green when a device signs in

This milestone needs AUD's web player API. If that isn't ready, M5 to M7 can go first._

<a id="app-09"></a>
- [x] **APP-09 · Built-in audiobook player core: PlayerProvider, mini-player, sync, lock-screen controls, resume** — `P1` · `L` · Phase 8
  - **Problem:** There's no way to listen inside Arrmada. The only app the page names is Lissen (Android-only, Audiobooks.tsx:242), and no iOS client has been verified. iPhone family members can only download zip files.

The overhaul calls for an HTML5 player that:
- stays at the bottom across pages
- works as a home-screen app on iPhone
- goes through listening.Store, so the place guards and history still apply
  - **Approach:** 1. Client contract: the AUD endpoints listed in the epic design (library, shelves, item detail with tracks/chapters, Range file stream, play, sync, close, bookmarks, accept). AUD owns the server side.
    2. web/src/lib/playerMath.ts, pure functions: locate(tracks, t) → {idx, offset}, globalOf(tracks, idx, offset), chapterAt(chapters, t), and formatting.
    3. web/src/lib/player.tsx: a PlayerProvider mounted in main.tsx inside MeProvider, wrapping both layouts.
       - It owns one lazily created HTMLAudioElement (preload='metadata').
       - Context API: open(itemKey, {at?, autoplay?}), toggle, seek(global), skip(±s), prevChapter/nextChapter, setRate, close.
       - It stops when the user signs out.
    4. iOS gesture unlock: open() is always called from a click. It calls audio.play() synchronously on the element before any await (using the first track URL if known, otherwise a 0.1s silent data URI), then sets the real src and seeks after loadedmetadata.
    5. Multi-file books:
       - Each track's src is /api/v1/me/audio/items/{key}/file/{ino}.
       - On 'ended', move to the next track at offset 0.
       - A seek across tracks swaps src and sets currentTime once metadata loads.
    6. Sync:
       - play returns {sid, position}; resume at that position or at opts.at.
       - While playing, sync about every 15s, and also on pause, seek end and track change.
       - time_listened is the wall-clock seconds actually spent playing since the last sync (performance.now deltas, not scaled by playback rate).
       - On pagehide or visibilitychange=hidden, send `fetch(sync, {method:'POST', keepalive:true})`, falling back to navigator.sendBeacon.
       - close calls /close.
       - A failed sync keeps the newest unsent payload and retries on 'online' or the next tick.
    7. Device identity:
       - A stable per-browser device_id in localStorage (try/catch).
       - Device name 'Arrmada web · iPhone|Android|Mac|Windows|Linux', so the devices list and the admin Listening tab show it like any other app.
       - The title never goes anywhere except the user's own endpoints.
    8. One player per browser: BroadcastChannel('arrmada-player'). Starting playback in one tab pauses the others.
    9. Resume: localStorage holds {item_key, position, at}. After a reload, the MiniPlayer shows 'Resume <title> at 1:23:45', paused.
    10. Media Session:
        - Metadata: title, author, series as album, artwork at 96, 256 and 512px from cover_url.
        - Handlers: play, pause, seekbackward/seekforward (30s), previoustrack/nexttrack as chapter skip, seekto, stop.
        - setPositionState, throttled to once a second.
    11. web/src/components/audiobooks/MiniPlayer.tsx, rendered by both UserLayout and AppLayout:
        - Fixed, z-player, bottom = var(--tabbar-h) + safe-area; `lg:left-[236px]` in AppLayout (the Sidebar width).
        - Shows cover, title, current chapter, play/pause, back 30s and close.
        - Sets has-player on `<html>` while visible, so `<main>` padding and toasts move up.
        - Tapping it opens the full player ([APP-11](#app-11)). Until then it expands an inline scrubber row.
    12. Errors:
        - 401: stop and show 'Signed out'.
        - 404 on a file: re-fetch the item detail (files may have been merged) and resume at the same global position.
        - Decode error: skip to the next track with a note.
    13. Entry points before the Listen tab exists:
        - 'Play here' on each place in Audiobooks.tsx PlacesCard: open(place.item_key, {at: place.position}).
        - 'Listen' on MyBooks audiobook cards. internal/httpapi/mybooks.go MyAudiobook gains `item_key` = audioserver.ItemKey(b.ID, versionID).
    14. Keep the existing palette, accent and type scale.
  - **Files:** `web/src/lib/player.tsx`, `web/src/lib/playerMath.ts`, `web/src/lib/playerMath.test.ts`, `web/src/components/audiobooks/MiniPlayer.tsx`, `web/src/main.tsx`, `web/src/components/UserLayout.tsx`, `web/src/components/AppLayout.tsx`, `web/src/pages/Audiobooks.tsx`, `web/src/pages/MyBooks.tsx`, `web/src/lib/api.ts`, `web/package.json`, `internal/httpapi/mybooks.go`, `internal/httpapi/mybooks_test.go`
  - **Acceptance:**
    - Single-file m4b and multi-file books play continuously across track boundaries, and playback survives navigating between pages and between tabs of the shell.
    - On iPhone (Safari and the home-screen app) and Android Chrome, the lock screen shows cover and title with play/pause, ±30s and next/previous chapter. Playback continues with the screen locked.
    - The place is saved about every 15s and on pause, close and page hide. It shows in Arrmada and is where Lissen resumes next time.
    - Reopening the app offers Resume at the last place.
    - Opening a second tab and pressing play pauses the first.
    - At 375px the mini-player covers neither page content nor the home indicator, and it sits above the tab bar.
    - The admin Listening tab shows web-player time under the person with an 'Arrmada web · …' device and no title.
    - 'Play here' on a place and 'Listen' on a MyBooks audiobook both start playback.
  - **Tests:** vitest for playerMath: locate and globalOf round-trip across 3 tracks, chapter lookup at boundaries. Add vitest as a devDependency if FE hasn't yet.; Go TestMyBooksAudiobookItemKey: item_key matches audioserver.ItemKey for the default and an extra version.; UI check matrix on iPhone Safari, iPhone home-screen app, Android Chrome and desktop Chrome/Firefox: play/pause, lock-screen controls, track boundary, reload and resume, offline then reconnect (sync resumes), two tabs.; npm run build passes.
  - **Depends on:** AUD (web player API: library, item detail with tracks/chapters, Range file stream, play/sync/close — draft audiobooks.t10), [APP-05](#app-05), [APP-03](#app-03)
  - **Risk:** iOS Safari audio is the main risk: autoplay needs a gesture, background-playback limits vary by version, and Range handling is strict. Test on a real iPhone in the first session. Several tabs on one book open separate sessions; AUD's sync rules handle that, and BroadcastChannel reduces it.
  - **Resolves:** audiobooks-7
<a id="app-10"></a>
- [x] **APP-10 · 'Listen' tab: shelves, all-audiobooks grid and a book sheet as the default /audiobooks view** — `P1` · `L` · Phase 8
  - **Problem:** For requesters, /audiobooks (UserLayout.tsx:19) is a settings page: connect card, password form, places, stats and devices (Audiobooks.tsx:238-267). It has no catalogue, no covers and no play button. A family member expecting something like Plex finds a form.
  - **Approach:** 1. Audiobooks.tsx:
       - Everyone gets the tabs 'Listen' (the default) and 'Apps & devices' (today's YouView minus places and stats, until [APP-12](#app-12) reworks it), plus the existing admin tabs.
       - The tab lives in the URL as ?tab=listen|apps|server|people|listening|import, using FE's useTabParam if present.
       - The tab strip shows for everyone, overflow-x-auto as today.
    2. New components in web/src/components/audiobooks/:
       - ShelfRow: a horizontal row in the Discover PosterRow style, with square covers about 140px wide.
       - BookCard: posterThumb cover, title, author and an accent progress bar.
       - AllAudiobooks: a grid from GET /me/audio/library with search and sort (title, author, recently added), paged 60 at a time on the client.
       - BookSheet, built on [APP-06](#app-06)'s Sheet. It is deep-linkable as ?book=<item_key>, so Back closes it, and shows:
         - cover, title, author, narrator and series #n
         - description
         - chapter list; tapping a chapter calls player.open(key, {at: chapter.start})
         - other versions
         - 'Your place': position or finished, plus AUD's PlaceTimeline and OfferBanner once audiobooks.t5 lands
         - Play/Resume
         - Download via api.audiobookDownloadURL when allowed
    3. Shelves come from GET /me/audio/shelves: Continue listening, Continue series, Recently added, Finished. Empty shelves are hidden.
    4. MyListeningCard ('Your listening') and 'Recently removed' move to the bottom of Listen. Continue listening replaces PlacesCard. Place history and restore stay reachable from the BookSheet's 'Your place'.
    5. Empty and switched-off states reuse the existing copy ('switched off', 'Your account isn't set up…').
    6. The intro at Audiobooks.tsx:39-41 becomes 'Listen right here, or in a listening app. Your place follows you between them.'
    7. Keep the existing palette, accent and type scale.
  - **Files:** `web/src/pages/Audiobooks.tsx`, `web/src/components/audiobooks/ShelfRow.tsx`, `web/src/components/audiobooks/BookCard.tsx`, `web/src/components/audiobooks/AllAudiobooks.tsx`, `web/src/components/audiobooks/BookSheet.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A requester opening /audiobooks lands on Listen. The Continue listening, Continue series, Recently added and Finished shelves show covers and progress.
    - Tapping a book opens the sheet with chapters, versions and 'Your place'. Play or Resume starts the mini-player at the saved place, and tapping a chapter plays from that chapter.
    - /audiobooks?tab=apps opens Apps & devices, and /audiobooks?book=<key> opens that book's sheet. Back closes the sheet.
    - The page works at 375px with no horizontal scroll.
    - Admins still see Server, People, Listening and Import. Nobody sees anyone else's places.
  - **Tests:** UI check on desktop, at 375px and on iPhone Safari: shelves, sheet, play from a chapter, empty and switched-off states, tab and book deep links.; npm run build passes.; Go coverage comes from AUD's web player API tests.
  - **Depends on:** [APP-09](#app-09), [APP-06](#app-06), AUD (library/shelves/item detail endpoints — draft audiobooks.t10), AUD (place timeline components — draft audiobooks.t5) optional
  - **Risk:** Moving places and stats out of the old 'You' view changes where returning users look, so put a one-line note on the Apps & devices tab: 'Your places moved to Listen'. Large libraries need paging to keep the grid responsive.
  - **Resolves:** audiobooks-7
<a id="app-11"></a>
- [x] **APP-11 · Full player sheet: chapters, speed, sleep timer, bookmarks and the held-jump note** — `P1` · `M` · Phase 8
  - **Problem:** The core player (APP-09) only plays, pauses and skips. Listening to a long book needs:
- chapter navigation
- speed control
- a sleep timer
- bookmarks
- an honest note when a far-back scrub is held by the place guards

The AUD guards hold a big backward jump until the listener carries on, so the person has to be told.
  - **Approach:** 1. web/src/components/audiobooks/FullPlayer.tsx, opened from the MiniPlayer as a Sheet ([APP-06](#app-06)), full-height on phones:
       - cover, title and current chapter
       - a chapter scrubber plus a thin whole-book progress line
       - transport: back 30, play/pause, forward 30, previous/next chapter
       - a chapter list with the current one highlighted; tap to jump
    2. Speed:
       - 0.8 to 3.0 in 0.1 steps through playbackRate, with preservesPitch on.
       - Remembered per device in localStorage (try/catch) and re-applied on every track src swap.
    3. Sleep timer:
       - 15, 30, 45 or 60 minutes, or end of chapter.
       - Fades the volume over the last 10s, then pauses and syncs.
       - Shows the remaining time in the mini and full players, and can be cancelled.
    4. Bookmarks:
       - List, add (at the current position, optional note) and delete, via AUD's bookmark endpoints.
       - Tapping a bookmark seeks to it.
    5. Held jump: when a sync reply carries held_position, show 'Jumped back — kept once you listen on', with 'Keep it now', which calls api.acceptAudioJump (POST /api/v1/me/audio/accept, already exists).
    6. Desktop keyboard (ignored while focus is in an input):
       - Space toggles playback.
       - ←/→ skip 30s.
       - Shift+←/→ change chapter.
    7. Keep the existing tokens and type scale.
  - **Files:** `web/src/components/audiobooks/FullPlayer.tsx`, `web/src/components/audiobooks/MiniPlayer.tsx`, `web/src/lib/player.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Tapping the mini-player opens the full player, and Back or a swipe closes it.
    - Chapter list, scrubber and previous/next chapter work across multi-file books.
    - The speed setting survives track changes and reloads.
    - The sleep timer fades out and pauses at the chosen time or at the end of the chapter, and the place is saved.
    - Bookmarks can be added, listed, jumped to and deleted, and they show up in Lissen too (same store).
    - A far-back scrub shows the hold note. 'Keep it now' accepts the jump immediately; otherwise it is kept after about 30s of listening.
  - **Tests:** UI check on iPhone home-screen app, Android Chrome and desktop: speed, sleep timer (use a 1-minute debug option behind a query flag), chapters, bookmarks, held jump.; vitest: the sleep-timer end-of-chapter calculation in playerMath.; npm run build passes.
  - **Depends on:** [APP-09](#app-09), [APP-06](#app-06), AUD (bookmark endpoints and held_position in sync replies — draft audiobooks.t10)
  - **Risk:** Some iOS versions ignore volume changes on HTMLAudioElement. If so, fall back to pausing at the deadline without a fade.
  - **Resolves:** audiobooks-7
<a id="app-12"></a>
- [x] **APP-12 · 'Apps & devices' tab: guided setup checklist that ticks green when a device signs in, and an honest app list** — `P2` · `M` · Phase 8
  - **Problem:** The connect card names only Lissen ('Use Lissen (Android) or another Audiobookshelf app', Audiobooks.tsx:242). MyBooks sends people there with 'Listen in an app — audiobooks in Lissen' (MyBooks.tsx:90-93).

Setup means copying three values into another app, with no confirmation that it worked. The device list exists but isn't tied into setup, and iPhone users get no guidance.
  - **Approach:** 1. Audiobooks.tsx: the Apps & devices tab replaces YouView with an AppSetupChecklist:
       1. Set your audiobook password. Ticked when has_password; reuses PasswordCard.
       2. Pick your app.
       3. Copy the address. Reuses addresses() and Field.
       4. 'Waiting for your phone…'.
       Below the checklist come the existing devices list and the note 'Your places moved to Listen'.
    2. New web/src/lib/audioApps.ts registry, each entry {id, name, platform: android|ios|any, url, status: verified|untested, fields: [label, value][]}:
       - Lissen: android, verified. Fields: Server type = Audiobookshelf, Server address, Username, Password = your audiobook password.
       - ShelfPlayer and the official Audiobookshelf app: untested until AUD's iOS verification.
       - 'This page' (the web player): any platform, verified.
       The platform is preselected from the user agent. iPhone users get 'Listen here — add Arrmada to your Home Screen (Share → Add to Home Screen)' as the recommended option, and untested apps carry a 'Not tested yet' badge.
    3. Step 4 polls api.myAudio() every 4s while it is visible. It stops when the page is hidden or after 10 minutes. It turns green when a device with created_at after the step started appears, and names it ('Pixel 8 · Lissen signed in ✓').
    4. Copy fixes:
       - MyBooks.tsx:90-93 becomes 'Listen — in your browser or a listening app'.
       - The UserLayout and Me menu item becomes 'Audiobook apps & password' → /audiobooks?tab=apps.
       - ServerView's 'How places are kept' text is updated to match AUD's rules once its PATCH and forward-jump tasks land.
  - **Files:** `web/src/pages/Audiobooks.tsx`, `web/src/lib/audioApps.ts`, `web/src/pages/MyBooks.tsx`, `web/src/components/UserLayout.tsx`, `web/src/pages/Me.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A requester with no password sees step 1 open. After setting it, step 2 shows apps for their platform with the exact field names to fill in.
    - Signing in from Lissen flips step 4 to ✓ within about 5s and names the device.
    - On an iPhone, the web player is the recommended path, and no untested app is presented as working.
    - No page names Lissen as the only way to listen. The user-menu link opens the Apps & devices tab directly.
  - **Tests:** UI check: a fresh requester flow (no password → set password → app signs in → step 4 flips), an iPhone user agent showing iOS guidance, an Android user agent showing Lissen first, layout at 375px, and the deep link from the menu.
  - **Depends on:** [APP-10](#app-10), [APP-09](#app-09), AUD (iOS client verification updates app statuses — draft audiobooks.t8)
  - **Risk:** The device poll is a bounded loop that stops when hidden or after 10 minutes. FE's live-update channel can replace it later.
  - **Resolves:** audiobooks-7

#### Milestone: M5 — Get notified

_There's one clear place to turn on device push, with plain answers for http, iPhone-not-installed and no-key cases. A gentle prompt appears after the first request. People choose which events push them, and Apprise moves under Advanced._

<a id="app-13"></a>
- [x] **APP-13 · Me page as the notifications home: one 'Get notified' switch, Apprise under Advanced, bell 'Settings' link** — `P2` · `M` · Phase 7
  - **Problem:** Behind a bare '⚙' (NotificationBell.tsx:85), the panel stacks PushSetting ('Push notifications', line 194) on top of AppriseSetting ('Push notifications (optional)', line 232), which has an 'ntfy://topic or discord://id/token' placeholder.

When the server has no push key, PushSetting returns null (line 189), leaving only the admin-style Apprise box. Push also silently hides on http LAN addresses, which aren't a secure context, and on iPhones outside the installed app. Family members never find the one option that works for them.
  - **Approach:** 1. New web/src/lib/push.ts, moving the logic out of NotificationBell:
       - pushEnvironment() → {secure: isSecureContext, supported, ios, standalone}.
       - pushStatus() → 'unavailable' (empty key) | 'insecure' | 'needs-install' (iOS, not standalone) | 'blocked' | 'off' | 'on'.
       - enablePush(): calls Notification.requestPermission() FIRST, inside the gesture, then awaits the service worker, subscribes, and calls api.pushSubscribe.
       - disablePush().
       - urlBase64ToUint8Array.
    2. Me.tsx 'Get notified' section (id 'notifications'):
       - The device push switch comes first, with a status line ('On for this iPhone' / 'Off on this device').
       - Plain answers for each state:
         - insecure: 'Notifications need the secure address — open Arrmada from your https link'
         - needs-install: Add to Home Screen steps
         - unavailable: 'Push isn't set up on this server yet — ask the admin'
         - blocked: how to unblock
       - Event checkboxes slot in here ([APP-15](#app-15)).
       - Then a collapsed `<details>` titled 'Advanced: Discord, ntfy, email (Apprise)' containing AppriseSetting with its own heading.
    3. Other Me rows: Audiobook apps & password (→ /audiobooks?tab=apps), Calendar (LAN), Calendar feed ([APP-18](#app-18)), Sign out.
    4. Staff: the Sidebar footer avatar and name become a Link to /me, and the BottomTabs 'Me' tab already points there.
    5. NotificationBell:
       - Remove PushSetting, AppriseSetting and the '⚙'.
       - The header gets a labelled 'Settings' text button → /me#notifications.
       - The empty state adds 'Turn on notifications in Settings'.
    6. Keep the existing visual style.
  - **Files:** `web/src/lib/push.ts`, `web/src/pages/Me.tsx`, `web/src/components/NotificationBell.tsx`, `web/src/components/Sidebar.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - The bell shows a 'Settings' button and no gear. The bell no longer contains two 'Push notifications' sections.
    - /me shows one 'Get notified' section with device push first, and no two options share a name.
    - With no push key, on http, or on an iPhone outside the home-screen app, the section explains why and what to do instead of hiding push.
    - Apprise is still configurable under Advanced.
  - **Tests:** UI check: the toggle subscribes and unsubscribes (POST /api/v1/me/push/subscribe and /unsubscribe in the network log).; UI check of each state: LAN http (insecure), iOS Safari not installed, deps.Push nil (unavailable), permission denied (blocked). At 375px and on desktop.; npm run build passes.
  - **Depends on:** [APP-05](#app-05)
  - **Risk:** iOS only allows the permission request from a user gesture inside the installed app, so enablePush must call requestPermission before any other await.
  - **Resolves:** discover-13, product-7
<a id="app-14"></a>
- [x] **APP-14 · 'Get notified when it's ready?' prompt after someone's first request** — `P2` · `S` · Phase 7
  - **Problem:** Push is the main way family members learn a request is ready, but nothing ever offers it to them. The switch is buried in settings, and on iPhone it only works from the installed app.
  - **Approach:** 1. New web/src/components/PushPrompt.tsx, mounted in UserLayout and AppLayout:
       - It listens for a window CustomEvent 'arrmada:requested'.
       - Discover's doRequest and BooksDiscover's request dispatch that event on success.
    2. It shows a Sheet ([APP-06](#app-06)): 'Get notified when it's ready?' with [Turn on] and [Not now], when all of these hold:
       - pushStatus() is 'off'.
       - localStorage 'arrmada.pushPrompt' is not 'dismissed' or 'done' (try/catch). If storage is unavailable, show it at most once per page load.
    3. When pushStatus() is 'needs-install', the same sheet explains Add to Home Screen instead and offers 'Got it', which counts as dismissed.
    4. It never prompts for 'unavailable', 'insecure', 'blocked' or 'on'.
    5. [Turn on] calls enablePush() directly in its click handler and sets 'done' on success. [Not now] sets 'dismissed'.
  - **Files:** `web/src/components/PushPrompt.tsx`, `web/src/components/UserLayout.tsx`, `web/src/components/AppLayout.tsx`, `web/src/pages/Discover.tsx`, `web/src/pages/BooksDiscover.tsx`, `web/src/lib/push.ts`
  - **Acceptance:**
    - After their first request, a requester sees the prompt once. 'Turn on' brings up the browser permission and registers the device.
    - 'Not now' stops the prompt from appearing again on that device.
    - On an iPhone outside the home-screen app, the prompt explains how to install instead of failing.
    - With push already on, unavailable or blocked, no prompt appears.
  - **Tests:** UI check: the first request shows the prompt, and a second request after dismissing it doesn't.; UI check: 'Turn on' produces POST /api/v1/me/push/subscribe.; Manual: the iPhone home-screen app flow.
  - **Depends on:** [APP-13](#app-13), [APP-06](#app-06)
  - **Risk:** Prompt fatigue. Keep it to one prompt per device, only after a real success, and never on page load.
  - **Resolves:** product-7
<a id="app-15"></a>
- [x] **APP-15 · Per-event notification preferences (approved, declined, ready, new request)** — `P2` · `M` · Phase 7
  - **Problem:** There are no per-event preferences. Every approved, declined and ready event pushes to every device and every Apprise URL, so people who only want 'ready' alerts turn push off completely.
  - **Approach:** 1. Migration `internal/store/migrations/NNNN_user_notify_prefs.sql` (next free number, 0090 or later): `ALTER TABLE users ADD COLUMN notify_prefs TEXT NOT NULL DEFAULT ''`.
    2. New internal/requests/notifyprefs.go:
       - `type NotifyPrefs map[string]bool` with known keys approved, declined, ready and new_request.
       - `parseNotifyPrefs(string) NotifyPrefs`: empty or bad JSON means everything is on.
       - `func (p NotifyPrefs) Wants(key string) bool { v, ok := p[key]; return !ok || v }`.
       - Repo getNotifyPrefs/setNotifyPrefs (a missing users row, such as the dev bypass user, means defaults), and Service NotifyPrefs/SetNotifyPrefs.
    3. notifyParties (usernotify.go:205):
       - The inbox insert is unchanged, so refs, idempotency and the ready sweep are untouched.
       - After `inserted`, Apprise and Web Push are gated by `prefs.Wants(prefKey(kind))`.
       - prefKey maps request-ready → ready, request-approved → approved, request-declined → declined. Unknown kinds always deliver.
    4. internal/httpapi/usernotify.go: GET and PUT /api/v1/me/notify-prefs, scoped to the caller. PUT accepts only known keys and ignores the rest. Register the routes in server.go next to /me/apprise; they are already externally allowed through /api/v1/me/.
    5. Me 'Get notified' gets checkboxes:
       - 'A request is approved'
       - 'A request is declined'
       - 'It's ready to watch or read'
       - For staff only: 'Someone requests something' (key new_request), which REQ's request.created alert honours.
  - **Files:** `internal/store/migrations/NNNN_user_notify_prefs.sql`, `internal/requests/notifyprefs.go`, `internal/requests/notifyprefs_test.go`, `internal/requests/usernotify.go`, `internal/httpapi/usernotify.go`, `internal/httpapi/server.go`, `web/src/pages/Me.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Turning off 'approved' stops approved pushes and Apprise messages for that user, while the inbox still records them.
    - New users and users with empty or invalid prefs get everything, as today.
    - An event kind added later (for example REQ's) delivers by default until someone turns it off.
    - A user can only read and change their own preferences.
  - **Tests:** Go TestNotifyPrefsGateDelivery: with a fake PushSender and a stub Apprise, push and Apprise are skipped while the inbox row is written.; Go TestNotifyPrefsDefaultsTrue: empty JSON, garbage JSON, missing key.; Go TestNotifyPrefsUnknownKindDelivers.; Go TestNotifyPrefsEndpointScopedToCaller.; UI check: the checkboxes save and reload. Run race tests in Docker.
  - **Depends on:** [APP-13](#app-13), REQ (staff request.created alert honours new_request — draft discover.t6)
  - **Risk:** Preference checks must stay off the inbox path, or the idempotency refs and the ready sweep break. Unknown keys must default to on, so new event types never go silent.
  - **Resolves:** discover-13

#### Milestone: M6 — Calendar on a phone

_The calendar reads well on a phone as an agenda. Busy days expand, items open their title, requesters see their own requests by default, and anyone can subscribe from Google or Apple Calendar._

<a id="app-16"></a>
- [x] **APP-16 · Calendar for phones: agenda view, tappable items, '+N more' day sheet, no stale-month race** — `P2` · `M` · Phase 8
  - **Problem:** Calendar.tsx:67-90 is a fixed grid-cols-7 month at every width, with min-h 92px cells and 10px truncated titles. At 375px each cell is about 45px wide.
- SxxEyy and the episode name appear only in a hover tooltip (lines 105-107).
- '+N more' is a plain span (line 85).
- poster_url is returned (calendar.go:15) but never rendered.
- Requester items are unlinked, and CalendarItem has no tmdb_id (calendar.go:10-19). movies.UpcomingMovie has no TMDB id either.
- The fetch effect has no alive guard (lines 30-35).
  - **Approach:** 1. Backend:
       - CalendarItem gains `TMDBID int json:"tmdb_id"` and `MediaType string json:"media_type"` ('movie'|'series'), plus `Season`, `Episode` and `EpisodeTitle` (omitempty). Keep subtitle for compatibility.
       - series.UpcomingEpisode gains TMDBID (SELECT s.tmdb_id in internal/series/calendar.go:22).
       - movies.UpcomingMovie gains TMDBID (m.TMDBID).
       - Move item building into `a.calendarItems(ctx, start, end) []CalendarItem`, which [APP-17](#app-17) and [APP-18](#app-18) reuse.
    2. New web/src/lib/calendar.ts:
       - ymd and monthCells(cursor), moved out of Calendar.tsx.
       - groupByDate(items), returning dates in order.
       - dayLabel(date, today) → 'Today' / 'Tomorrow' / 'Thu 16 Oct'.
       - episodeCode(s, e) → 'S02E05'.
    3. Calendar.tsx gets a Month | Agenda toggle:
       - Agenda is the default when matchMedia('(max-width: 639px)') matches. The choice is remembered in localStorage (try/catch) and reflected as ?view=.
       - MonthGrid is today's markup, unchanged on desktop, with day numbers and weekday labels raised to 11px.
       - AgendaList covers today−1 to today+42, and 'Show more' extends it by 6 weeks. It has sticky date headers and opens scrolled to Today.
       - Agenda rows: a 40px poster (posterThumb), the title, 'S02E05 · Episode name' or 'Movie · 2026', and a status chip: ✓ In library, Upcoming, or Not monitored (dimmed).
    4. Tap-through: rows are 44px `<Link>`s. Requesters go to /discover/<media_type>/<tmdb_id> ([APP-07](#app-07)); staff go to /series/<ref_id> or /movies/<ref_id>.
    5. In MonthGrid, '+N more' becomes a `<button>` that opens a DaySheet ([APP-06](#app-06) Sheet) listing every item for that day with the same row component.
    6. Fetch race: a sequence ref, so an older, slower response never replaces a newer month's items.
  - **Files:** `internal/httpapi/calendar.go`, `internal/httpapi/calendar_test.go`, `internal/series/calendar.go`, `internal/movies/calendar.go`, `web/src/pages/Calendar.tsx`, `web/src/lib/calendar.ts`, `web/src/lib/calendar.test.ts`, `web/src/lib/api.ts`
  - **Acceptance:**
    - At 375px the Calendar opens in Agenda view with readable rows showing posters, episode numbers and names, with no tooltips needed and no horizontal scroll.
    - Tapping an episode as a requester opens that show's Discover sheet; staff go to the series page.
    - '+3 more' in Month view opens the full day list, and Esc or Back closes it.
    - At 640px and wider, the Month grid looks as it does today.
    - Flipping months quickly never shows a previous month's items.
  - **Tests:** Go TestCalendarIncludesTMDBID: movie and episode items carry tmdb_id and media_type.; vitest: groupByDate ordering, and the Today/Tomorrow labels across a month boundary.; UI check: agenda at 375px, month on desktop, rapid month flipping, tap-through as requester and as staff.
  - **Depends on:** [APP-06](#app-06), [APP-07](#app-07)
  - **Risk:** The requester calendar shows the whole library's schedule, as it does today; that is deliberate. It stays off the external allowlist.
  - **Resolves:** discover-11, frontend-5, product-15
<a id="app-17"></a>
- [x] **APP-17 · Calendar 'My requests' filter (own and subscribed), default for requesters** — `P2` · `S` · Phase 8
  - **Problem:** Requesters get the Calendar as a top-level tab, but it shows the whole library rather than what they asked for. There's no way to filter it to their own or followed requests.
  - **Approach:** 1. internal/requests/repo.go: new `MediaKeysForUser(ctx, userID) (map[string]bool, error)`. It returns 'movie:<tmdb>' and 'series:<tmdb>' keys from:
       - requests WHERE requested_by=?
       - UNION requests joined to request_subscribers (0065) WHERE s.user_id=?
       Declined requests are excluded. Expose it through Service.
    2. handleCalendar: every item gets `requested_by_me`, and `?mine=1` keeps only those items.
    3. Calendar.tsx:
       - A chip pair, 'My requests' | 'Everything'. Requesters default to My requests and staff to Everything; the choice is remembered in localStorage.
       - Agenda rows show a 'You asked for this' chip.
       - The 'mine' empty state reads: 'Nothing you asked for is airing in this window', with a 'Show everything' button.
  - **Files:** `internal/requests/repo.go`, `internal/requests/service.go`, `internal/httpapi/calendar.go`, `internal/httpapi/calendar_test.go`, `web/src/pages/Calendar.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - 'My requests' shows only titles the user requested or joined, and never someone else's or declined ones.
    - Requesters open the Calendar on My requests and can switch to Everything; staff open on Everything.
  - **Tests:** Go TestCalendarMineFilter: own, subscribed, someone else's and declined requests.; UI check: toggle and empty state at 375px.
  - **Depends on:** [APP-16](#app-16)
  - **Risk:** Low. 'Followed' means request_subscribers today. If REQ adds a separate follow model, extend MediaKeysForUser.
  - **Resolves:** discover-11, product-15
<a id="app-18"></a>
- [ ] **APP-18 · Personal iCal feed for the Calendar (token-scoped, rotatable)** — `P3` · `M` · Phase 17
  - **Problem:** There is no iCal output anywhere (no .ics or text/calendar), so nobody can see upcoming episodes and releases in Google or Apple Calendar the way Sonarr and Radarr allow.
  - **Approach:** 1. Migration `internal/store/migrations/NNNN_calendar_feeds.sql` (next free number): `calendar_feeds(user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, token_hash TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL)`.
    2. POST /api/v1/me/calendar-feed creates or rotates the token: 32 random bytes, base64url. It stores only the sha256 hex and returns the plaintext URL once. DELETE revokes it. Both sit under the already-allowed /api/v1/me/ prefix.
    3. GET /api/v1/calendar.ics?token=…&mine=0|1, in new internal/httpapi/calendar_ics.go:
       - Registered WITHOUT a.protected; it finds the user by token hash.
       - A missing token returns 400. An unknown token, revoked token or disabled user returns 404.
       - It reuses a.calendarItems over the window from 30 days back to 120 days ahead, and applies mine like [APP-17](#app-17).
    4. The pure writer `writeICS(w, items, host, now)` produces:
       - VCALENDAR with PRODID and X-WR-CALNAME 'Arrmada'
       - all-day VEVENTs (DTSTART;VALUE=DATE and DTEND the next day)
       - DTSTAMP
       - SUMMARY 'Show — S02E05 · Title' or 'Movie (Year)'
       - stable UIDs: 'arrmada-ep-<ref_id>-<s>x<e>@<host>' and 'arrmada-movie-<ref_id>@<host>'
       - CRLF line endings, folding at 75 octets (UTF-8 safe), and escaping of , ; \ and newlines
       The response sets Content-Type text/calendar; charset=utf-8 and Cache-Control max-age=900.
    5. external.go: extend externalAllowedExact to match ^/api/v1/calendar\.ics$, because calendar services fetch from the internet. Other calendar paths stay blocked from outside.
    6. Add a dedicated limiter (newLoginLimiter, about 60 per minute per client IP).
    7. The request logger records only r.URL.Path (middleware.go:33), so the token in the query string is never logged. Add a test that locks this in.
    8. UI:
       - A 'Subscribe' button in the Calendar header opens a Sheet with:
         - the webcal:// link and the https link (for Google's 'From URL')
         - Copy
         - 'Reset link' and 'Turn off'
         - the note 'Google Calendar fetches from the internet — this only works with your outside (tunnel) address'
       - The same block appears on /me ([APP-13](#app-13)).
       - Requester feeds default to &mine=1.
  - **Files:** `internal/store/migrations/NNNN_calendar_feeds.sql`, `internal/httpapi/calendar_ics.go`, `internal/httpapi/calendar_ics_test.go`, `internal/httpapi/calendar.go`, `internal/httpapi/external.go`, `internal/httpapi/externalgate_test.go`, `internal/httpapi/server.go`, `web/src/pages/Calendar.tsx`, `web/src/pages/Me.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Subscribing with the copied link in Google Calendar or Apple Calendar shows upcoming episodes and releases.
    - Resetting the link makes the old URL return 404 straight away.
    - A requester's default feed contains only their requested titles.
    - An invalid or missing token returns no data, and the token never appears in logs.
  - **Tests:** Go TestICSFormat: CRLF, folding at 75 octets including multi-byte titles, escaping, all-day dates, stable UIDs.; Go TestICSRequiresValidToken and TestTokenRotationRevokesOld.; Go TestICSRequesterScope.; Go TestICSExternalAllowed: externalAllowed('/api/v1/calendar.ics') is true, and '/api/v1/calendar' stays false.; Go TestRequestLogOmitsQuery.; Manual: subscribe from Apple Calendar on the LAN and from Google Calendar through the tunnel.
  - **Depends on:** [APP-17](#app-17), [APP-13](#app-13), SEC (review of a token-authenticated endpoint reachable from the internet: rate limit, log redaction, no user enumeration)
  - **Risk:** This opens a token-authenticated, read-only endpoint to the internet, so SEC must review it. The token sits in a URL that calendar services store, which is standard practice, so rotation has to be easy. Google can only fetch it if the instance is reachable from outside.
  - **Resolves:** discover-11, product-15

#### Milestone: M7 — My shelf

_The requester's shelf has a detail sheet, series grouping, a 'New this week' row, and request cards that show their real stage. It stops polling while hidden._

<a id="app-19"></a>
- [ ] **APP-19 · My shelf as a real shelf: detail sheet, series grouping, 'New this week', real request stages, no hidden polling** — `P2` · `M` · Phase 13
  - **Problem:** MyBooks (web/src/pages/MyBooks.tsx) is one grid of covers with download buttons, though it does have search, sort and an 'Ebooks only' filter.
- There's no detail sheet or description.
- Series appear only as a text line.
- There's no 'new' row.
- Request cards show only a status.
- The page polls /me/books every 15s even when the tab is hidden (MyBooks.tsx:37).

books.Book already has Description, Subjects, SeriesName and SeriesPosition (internal/books/repo.go:43-60), but handleMyBooks flattens series into one 'Name #3' string and drops the rest.
  - **Approach:** 1. internal/httpapi/mybooks.go:
       - MyBook gains `description`, `series_name` and `series_position` as separate fields, and `subjects` (the first 3). The `series` string stays for compatibility.
       - MyRequest gains `stage`, `note` and `next_check_at`, from BOOK's request tracking (draft books.t5).
       - 'mine' is matched by book_id once BOOK's draft books.t3 lands.
    2. MyBooks.tsx, titled 'My shelf':
       - Tapping a card opens a detail sheet ([APP-06](#app-06) Sheet) with cover, title, author, series #n, description and formats, plus the actions Download ebook, Send to Kindle (only if BOOK's draft books.t13 has shipped), Listen (player.open(item_key), [APP-09](#app-09)) and the audiobook downloads. It also shows 'More in this series' from the shelf.
       - Rows, in order:
         - 'New this week' (added_at within 7 days)
         - 'Your requests', with the real stage and note, e.g. 'Not found yet · next check Tue' or 'Downloading 40%'
         - 'Series', grouped by series_name in reading order
         - 'All books', with the existing search, sort and Ebooks-only filter, paged 120 at a time
    3. Polling: new web/src/lib/usePollWhileVisible.ts replaces the 15s setInterval. It refreshes on visibilitychange and window focus, and polls every 60s only while visible. Use FE's useLiveQuery or the realtime 'book.imported' event once SEC filters websocket topics by role.
    4. Keep the palette, accent and type scale. Check at 375px with safe areas.
  - **Files:** `internal/httpapi/mybooks.go`, `internal/httpapi/mybooks_test.go`, `web/src/pages/MyBooks.tsx`, `web/src/lib/usePollWhileVisible.ts`, `web/src/lib/api.ts`
  - **Acceptance:**
    - On a phone, tapping a book opens a sheet with its description and the Download, Listen and (when available) Send to Kindle actions. Back closes it.
    - Books from one series are grouped together in reading order.
    - A book added today appears in 'New this week'.
    - A pending request shows its real stage ('Not found yet · next check Tue', 'Downloading 40%').
    - While the tab is hidden, no /me/books requests are made.
  - **Tests:** Go: handleMyBooks returns description, separate series fields and subjects, plus request stage and note (extend mybooks_test.go).; UI check: 375px and desktop layout, sheet open/close with Escape and the Back gesture, no horizontal scroll.; UI check: the network panel shows no polling while the tab is hidden.
  - **Depends on:** [APP-06](#app-06), [APP-09](#app-09), BOOK (requests matched by book_id — draft books.t3), BOOK (request stage/note/next_check_at — draft books.t5), BOOK (Send to Kindle — draft books.t13) optional
  - **Risk:** Low. Large shelves need paging to stay responsive, and request stages depend on BOOK's tracking shipping first. Until then, show today's status text.
  - **Resolves:** books-7

#### Risks

- iOS Safari and home-screen app audio quirks (gesture-only playback, background limits, strict Range handling) can sink the web player. Test on the owner's real iPhone in the first APP-09 session, before building the Listen tab on top of it.
- Three route trees in App.tsx and an FE router migration in flight. Child routes or redirects missed in one tree send deep links to /discover. Land APP-07 before FE's createBrowserRouter migration, or as part of it.
- Fixed bottom chrome (tab bar, mini-player, toasts, sheets, iOS keyboard) can overlap. Keep everything on the single z scale and --bottom-chrome contract from APP-05, and check every requester page at 375px.
- hoverOnlyWhenSupported is global. Any control reachable only through sticky hover disappears on touch. APP-01 audits the opacity-0 sites; staff grid actions are already desktop-only.
- Security-relevant changes (COOP relaxation, an internet-reachable token endpoint) need SEC sign-off. Each task has a fallback if it's refused: drop the popup.closed check; ship the feed LAN-only.
- Notification preferences must never touch the inbox insert. The inbox insert drives idempotency refs and the ready sweep.
- Privacy: the web player must stay on the caller's own /me/audio endpoints. Admin views must keep showing how much and when, never what (listen_log has no book column).
- Several tasks are gated on AUD, REQ and BOOK. If those slip, ship M5 to M7 first; they only depend on APP tasks.

#### Out of scope

- The server side of the audiobook web player (routes, listening.Store integration, sync guards). That's AUD; this epic consumes it.
- The routed /requests page, approval sheets, decline reasons, follow/subscribe changes and staff request alerts (REQ).
- OPDS catalogue, Send-to-Kindle delivery and ebookFile on the ABS-compatible server (BOOK). APP-19 only shows a Kindle button when BOOK ships it.
- Linking a local admin account to a Plex identity (PLEX).
- The shared component kit, router migration, lazy chunks, Playwright harness and useLiveQuery (FE). APP builds on them when present.
- URL-addressable tabs on staff pages (Settings, Insights, Convert, and so on) — FE's useTabParam work.
- Offline audiobook downloads cached in the home-screen app, and native iOS or Android apps.
- Music parity in the requester shell; the mobile hamburger for staff stays as-is.
- A theme toggle or other appearance settings for requesters.
- Changing the always-on adult filter itself. APP-07 only makes deep links respect it.

