# COPY — Truthful copy & polish sweep

_Part of the [Arrmada roadmap](../../ROADMAP.md). 15 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Every word Arrmada shows is true for the build that is running: no env-var or restart instructions for UI-managed keys, no "soon" on features that have shipped, no promises with no code behind them, no links to pages that don't exist, one meaning for each status word, and copy that changes with the viewer's role. A CI guard stops the known stale phrases from coming back.

**Why.** The audit found copy that sends people the wrong way, and the owner, their family and the AI agent all trust what the screen says:
- **TMDB banner.** Movies and Series tell the admin to set ARRMADA_TMDB_API_KEY and restart (movies-6, series-15, frontend-13, product-13). The key actually lives in Settings → API keys and takes effect immediately. On Discover a requester sees the same admin instruction about five times, pointing at a page they can't open (walk-1).
- **Upgrade promises with no code.**
  - The movie detail page promises upgrade watching on every film that has a file. The upgrade sweep skips unmonitored films, and every scanned-in film is unmonitored, so the promise is false for the owner's whole existing library (movies-6).
  - Music offers a working-looking "Automatically upgrade" switch, but no music upgrade code exists (music-7).
- **Subtitles and Insights say "soon" on finished work.**
  - Subtitles says "(soon) AI" and "once stripping ships", and shows a Health column that is always "—" (subtitles-9, walk-2). Whisper shipped in September, and stripping is Convert's job.
  - Insights shows "Coming soon" on finished tabs and hides imported Tautulli history (insights-11, product-13).
- **Pages and links that don't exist.** A bad URL reads "Not found isn't built yet". Toasts point at an "Activity" page and a "Requests page" that don't exist (frontend-13, frontend-10, product-13). Every Hardcover book links to a broken Open Library URL (books-11).
- **Settings and Dashboard are wrong about the setup.**
  - Settings says requesters see "only the Discover page", which is false.
  - It tells the admin to edit .env for the disk guard, which a saved Downloads folder silently overrides (walk-4, walk-5, system-15).
  - It calls Music "on the roadmap" while Music ships (system-15).
  - The Dashboard says "Plex isn't reachable — plex is not configured" and shows a constant "Auth: enabled" (product-13, system-15).
- **Status words mean different things on different pages.** "Missing" is a filter while "Wanted" is a badge, "Partial" and "In progress" mean the same thing, and "Paused" and "Unmonitored" mean the same thing. A movie whose file is gone still wears a green DOWNLOADED badge.

Each of these is small, but together they teach people to ignore the UI.

**Depends on:** FE — URL state for tabs and filters: [COPY-01](#copy-01) adds useTabParam for the five pages copy links into. FE should adopt and extend it rather than build a second hook, or [COPY-01](#copy-01) uses FE's hook if it lands first.; FE — design tokens (frontend-1: --avoid-soft, --shadow undefined): the Wanted/Partial chips in [COPY-12](#copy-12) and the avoid-tone banners in [COPY-02](#copy-02) and [COPY-14](#copy-14) only get their fill once FE defines the tokens.; FE — nav regroup (frontend-10 navigation part): [COPY-13](#copy-13) derives crumbs from nav.ts so FE's regroup flows through without page edits. [COPY-09](#copy-09)'s requesterNav() also lives in nav.ts.; FE — shared EmptyState/ErrorState and loading order (frontend-9): [COPY-14](#copy-14) uses them if present. FE owns the loading → error → empty ordering and the error boundary.; CFG — Settings hub / Connections and restart banner: when CFG moves API keys, the disk guard or the Plex connection, it updates lib/links.ts ([COPY-01](#copy-01)). The restart banner (walkthrough.t5) makes [COPY-09](#copy-09)'s disk-guard path true right after a folder change.; SEC — route re-gating ('deny by default'): [COPY-09](#copy-09)'s role legend describes today's requireRole gates and must be re-verified after SEC tightens the a.protected routes.; MUS — the music upgrade sweep (music.t17) restores the switch and copy [COPY-04](#copy-04) removes. MUS's 'Music off by default / early label / toggle stops jobs' task (music-12) extends [COPY-03](#copy-03)'s Music hint.; SUB — Health scoring (subtitles.t10) brings back the column [COPY-05](#copy-05) removes. Deriving Convert's keep list from the Subtitles languages changes [COPY-05](#copy-05)'s kept-languages sentence.; PLEX — the 'recording vs connected' badge (insights-3) owns the Insights pill [COPY-08](#copy-08) leaves alone. Moving the Plex connection out of Insights means repointing LINKS.plexConnection.; REQ — a restored /requests page: repoint LINKS.requests and the Overseerr import copy ([COPY-03](#copy-03)). REQ also owns the Discover request chips ('Pending' when auto-approved, 'Partly ready', the in-library 'Wanted').; MOV — outcome toasts (movies.t7) replace [COPY-03](#copy-03)'s 'follow it in Downloads → Searching' toasts. The search-outcome WhyPanel extends [COPY-06](#copy-06)'s branches.; SER — monitored-only episode counts (series-10) make [COPY-12](#copy-12)'s Partial/Complete/Wanted labels accurate for series.; OBS — the Logs page task (system-12) supersedes [COPY-10](#copy-10)'s footer fix if it lands first. A Last-backup stat takes the Dashboard slot freed by the Auth stat.; SAFE — the recycle bin never silently hard-deletes (backend-8): this makes [COPY-06](#copy-06)'s 'moves to the recycle bin' copy fully true.; ACQ — if Downloads is renamed Activity, change PAGE.downloads and the LINKS entries in lib/links.ts; all toasts follow.; BOOK — Hardcover client ownership: [COPY-15](#copy-15) adds an isolated slug query alongside BOOK's Hardcover work.

#### Design

## Principles
1. **Copy describes state, not intent.** A string may only claim what the code does today. Labels like "soon", "planned" or "once X ships" don't appear in the UI. If a feature isn't built, the control is absent and nothing promises it.
2. **Copy is role-aware.** An instruction is only shown to someone who can act on it:
   - Admins get the link to fix the problem.
   - Managers get "ask an admin".
   - Requesters get a plain sentence with no mention of Settings, keys or env vars.
3. **Credentials are a UI concern.** The UI never asks anyone to set an env var or restart for a key. Install-time knobs (ports, ARRMADA_RECYCLE_DIR=off) may be named where they are the true cause.
4. **Keep the existing look.** Use the dark warm palette, terracotta accent, current type scale and the existing banner, chip and dashed-card patterns. No new visual language; that is FE's job.

## Shared pieces (web/src)
| Module | What it holds | Who updates it later |
|---|---|---|
| `lib/links.ts` | `LINKS` (every deep link copy points at: apiKeys, libraryFolders, diskGuard, users, plexConnection, downloadsSearching, downloadClients, convertSettings, subtitlesSettings, requests) and `PAGE` (page names used in sentences, e.g. `PAGE.downloads = "Downloads"`) | CFG (Settings hub), ACQ (Activity rename), REQ (/requests), PLEX (Plex connection move): each changes one line here |
| `lib/useTabParam.ts` | `useTabParam(allowed, fallback)`: tab ⇄ `?tab=`, validated against the tabs the viewer may see, written with `replace` | FE adopts it in its URL-state sweep |
| `lib/status.ts` | Glossary plus `libraryStatus()` returning `{label, tone, soft}` for every library item | SER, MOV, BOOK, MUS reuse it |
| `components/MetadataMissing.tsx` | The single role-aware "no TMDB key" banner or empty card | none |
| `pages/NotFound.tsx` | A real 404 page (Placeholder.tsx is deleted) | none |

## Status glossary (lib/status.ts header comment, the single source)
| Word | Meaning | Tone |
|---|---|---|
| Downloaded | A file is on disk | good |
| Complete | Every monitored episode or track is downloaded (series, albums) | good |
| Partial | Some downloaded, more wanted | avoid |
| Wanted | Monitored and no file yet: Arrmada is searching for it | avoid |
| Unmonitored | Arrmada won't search for it | faint |
| File missing | Arrmada recorded a file but it is gone from disk | reject |
| Searching / Upcoming | Only on Downloads' live tabs and request cards | accent |

Library filters use the same words: "Wanted" (not "Missing") and "Downloaded" (not "Available"). Monitor toggles read "Monitored" or "Monitor" everywhere. Discover request chips stay with REQ.

## Backend touch-points (small, tested)
- `/api/v1/status` adds `metadata_ready` for authenticated callers only.
- `discoveryReady(w, r)` returns a role-aware message.
- `plannedModules` lists Music as `early`.
- The Dashboard payload adds `plex_configured`, and `streams_note` carries only real failures.
- Movie GET adds a computed `upgrades_allowed`.
- Book GET adds a computed `catalogue {name,url}` via `books.CatalogueLink`.
- `subtitles.skipNote()` names the real reason a language can't be made.
- The downloads feed omits `free_gb` when it can't be measured and reports `clients`.
- Compose and update.sh stamp the real version and commit.

## Guard
`internal/webui/copy_test.go` runs under the existing `go test -race ./...` in CI. It walks `web/src/**/*.ts(x)`, plus Go string literals in `internal/**` (excluding tests), for banned phrases. Each phrase carries a reason:
- ARRMADA_TMDB_API_KEY
- "coming soon", "(soon)", "once … ships", "isn't built yet", "on the roadmap", "lands with the"
- "in Activity", "Requests page"
- "Discover-only", "only the Discover page"
- "update.sh" (in the UI)

A `copy-ok` line marker allows a deliberate exception. Each later COPY task appends the phrases it retires. The test skips if web/src is absent.

## Order
M1 fixes everything that actively misleads (P1) and adds the guard. M2 rewrites Settings, Insights, Dashboard and Books so they describe the real setup. M3 unifies the vocabulary and the empty pages.

#### Milestone: M1 — Nothing tells you to do the wrong thing

_The TMDB key state is one truthful, role-aware message. Music, movie and subtitle copy no longer promise upgrades, AI or stripping that don't exist. Delete dialogs say whether files really go to the recycle bin. A bad URL shows 'Page not found'. No toast names a page that doesn't exist. CI fails if any of the retired phrases comes back._

<a id="copy-01"></a>
- [x] **COPY-01 · Deep-link foundation: lib/links.ts plus URL-addressable tabs on Settings, Insights, Subtitles, Convert and Downloads** — `P1` · `S` · Phase 2
  - **Problem:** Copy across the app sends people to 'Settings → API keys', 'Insights → Settings', 'Settings → Library' and 'Downloads → Searching'. Every one of those pages keeps its tab in React state, so a link can only land on the first tab:
- Settings.tsx:55
- Insights.tsx:20
- Subtitles.tsx:26
- Convert.tsx:54
- Downloads.tsx:73
The link targets are also hard-coded in each string. When CFG (Settings hub), ACQ (Activity rename) or REQ (/requests page) move a page, every sentence has to be hunted down again.
  - **Approach:** 1. New web/src/lib/useTabParam.ts with signature `useTabParam<T extends string>(allowed: readonly T[], fallback: T): [T, (t: T) => void]`.
       - It reads `?tab=` with useSearchParams and returns `fallback` when the value isn't in `allowed`. Settings passes only the tabs the viewer can see, so a manager can't open System or Users by URL.
       - The setter calls `setSearchParams(p => { p.set('tab', t); return p; }, { replace: true })`. It keeps other params, and Back still leaves the page.
    2. New web/src/lib/links.ts.
       - `export const LINKS = { apiKeys: '/settings?tab=system#api-keys', diskGuard: '/settings?tab=system#disk-guard', libraryFolders: '/settings?tab=library#media-folders', users: '/settings?tab=users', plexConnection: '/insights?tab=settings', downloadsSearching: '/downloads?tab=searching', downloadClients: '/downloadclients', indexers: '/indexers', subtitlesSettings: '/subtitles?tab=settings', convertSettings: '/convert?tab=settings', requests: '/discover' } as const`.
       - `export const PAGE = { downloads: 'Downloads', settings: 'Settings', insights: 'Insights' } as const`.
       - A header comment says: every deep link that copy points at lives here; when a page moves, change it here.
    3. Settings.tsx:
       - Replace `useState<Tab>('media')` with `useTabParam(tabs.map(t => t.key), 'media')`. Move the `tabs` array above the hook.
       - Give `Section` (line 605) an optional `id` prop on its root div, with `scroll-mt-20` so the sticky PageHeader doesn't cover it.
       - Set ids on: APIKeysSection 'api-keys', DiskGuardSection 'disk-guard', the Media folders section 'media-folders', RecycleBin 'recycle-bin', UsersManager 'users'.
       - Add an effect: once `s` has loaded and `location.hash` is set, call `document.getElementById(hash.slice(1))?.scrollIntoView({ block: 'start' })`.
    4. Insights.tsx, Subtitles.tsx, Convert.tsx and Downloads.tsx: swap the tab useState for useTabParam over each page's existing Tab keys.
    5. Books.tsx:346 (the Hardcover hint link) uses LINKS.apiKeys instead of the bare '/settings'.
  - **Files:** `web/src/lib/useTabParam.ts`, `web/src/lib/links.ts`, `web/src/pages/Settings.tsx`, `web/src/pages/Insights.tsx`, `web/src/pages/Subtitles.tsx`, `web/src/pages/Convert.tsx`, `web/src/pages/Downloads.tsx`, `web/src/pages/Books.tsx`
  - **Acceptance:**
    - As admin, /settings?tab=system#api-keys opens the System tab scrolled to the API keys section.
    - As a manager, /settings?tab=system and /settings?tab=users open the Media tab, not an admin-only tab.
    - /insights?tab=settings, /subtitles?tab=settings, /convert?tab=settings and /downloads?tab=searching each open that tab.
    - Clicking a tab updates ?tab= without adding history entries; browser Back leaves the page.
    - An unknown ?tab=foo falls back to the page's default tab.
  - **Tests:** `npm run build` (tsc --noEmit + vite build) passes.; Manual: open each LINKS entry as admin and as manager and confirm the landing tab and scroll position.
  - **Risk:** Low. If FE's URL-state work ships a generic hook first, use that hook and keep only links.ts. Settings renders 'Loading…' until its settings load, so the hash scroll must wait for `s`, or it scrolls to nothing.
  - **Resolves:** 
<a id="copy-02"></a>
- [x] **COPY-02 · One truthful, role-aware 'no TMDB key' state on Movies, Series and Discover** — `P1` · `M` · Phase 2
  - **Problem:** Movies.tsx:249 and Series.tsx:247 tell the user to set ARRMADA_TMDB_API_KEY and restart.
- The key is a UI-managed credential (apikeys.go; the env var is only a fallback).
- It takes effect without a restart (Settings.tsx:495).
- The backend errors already say 'add a TMDB key in Settings → API keys' (httpapi/movies.go:47, series.go:40, discover.go:174).

On Discover, every TMDB row fetches on its own and renders LoadError with that admin instruction (Discover.tsx:402-410). The affected rows are Trending, Popular movies, Popular series, Upcoming and GenreExplorer. A requester therefore sees 'Couldn't load — metadata isn't configured — add a TMDB key in Settings → API keys' about five times, pointing at a page they can't open. Managers can't open Settings → System either; the tab is admin-only in the UI (Settings.tsx:84).
  - **Approach:** Backend:
    1. In server.go, add `func (a *api) metadataReady() bool { return a.deps.Discovery != nil && a.deps.Discovery.Available() }`.
       - handleStatus (server.go:546) adds `"metadata_ready": a.metadataReady()` only when `authed`, so anonymous callers aren't told about the configuration.
       - /api/v1/status is already externally allowed (external.go:89).
    2. Change `discoveryReady(w)` to `discoveryReady(w, r)` and update its 8 callers in discover.go.
       - Staff (`isStaffRequest(r)`) keep today's message.
       - Everyone else gets 'Movie and TV browsing isn't set up on this server yet.'
       - The status stays 400.
    
    Frontend:
    3. api.ts: add `metadata_ready?: boolean` to the status type. me.tsx: add `metadataReady` (default true when the field is absent) and `setMetadataReady` to MeState, filled from /status.
    4. New components/MetadataMissing.tsx with `variant: 'banner' | 'empty'`. The role comes from useMe():
       - Admin: 'Movie and TV metadata isn't set up. Add a free TMDB key in Settings → System → API keys — it takes effect straight away, no restart.' The link uses LINKS.apiKeys.
       - Manager: 'Movie and TV metadata isn't set up. Ask an admin to add a TMDB key (Settings → System → API keys).'
       - Requester or read-only: 'Movie and TV browsing isn't set up on this server yet — ask the person who runs it.'
       - The banner keeps today's avoid-tone styling (border var(--avoid), background var(--avoid-soft)). The empty variant uses the dashed panel card already used on Discover.
    5. Movies.tsx:247-251 and Series.tsx:245-249: replace the hard-coded banner with `<MetadataMissing variant="banner" />`. It still shows when `!metaOK`.
    6. Discover.tsx: when `!metadataReady` and the tab is discover, movies or series:
       - Render MyRequestsRow, then one `<MetadataMissing variant="empty" />`.
       - Skip Hero, PosterRow, StreamingRow, BecauseRows and GenreExplorer.
       - Disable the search box with the placeholder 'Search isn't available yet'.
       - Leave the Books tab untouched; Open Library needs no key.
    7. Settings.tsx APIKeysSection.saveKey: when `id === 'tmdb'` and the returned status shows it configured, call `setMetadataReady(true)` so Movies and Discover recover without a reload.
    8. SetupWizard.tsx:180: 'No TMDB key yet — Movies and TV won't find anything until you add one in Settings → System → API keys', with a Link to LINKS.apiKeys.
  - **Files:** `internal/httpapi/server.go`, `internal/httpapi/discover.go`, `internal/httpapi/discover_status_test.go`, `web/src/lib/api.ts`, `web/src/lib/me.tsx`, `web/src/components/MetadataMissing.tsx`, `web/src/pages/Movies.tsx`, `web/src/pages/Series.tsx`, `web/src/pages/Discover.tsx`, `web/src/pages/Settings.tsx`, `web/src/pages/SetupWizard.tsx`
  - **Acceptance:**
    - `rg ARRMADA_TMDB_API_KEY web/src` returns nothing.
    - With no TMDB key, Movies and Series each show exactly one banner with no mention of restarting. For an admin, its link opens Settings → System scrolled to API keys.
    - A requester opening /discover with no key, on the LAN or from outside, sees their requests row and one plain-language message. There are no 'Couldn't load' lines and no mention of Settings or API keys.
    - A manager sees the 'ask an admin' wording, not a link to a tab they can't open.
    - After an admin saves a TMDB key, Movies and Discover load normally without a restart or a page reload.
    - /api/v1/status called without a session has no metadata_ready field.
  - **Tests:** Go TestMetadataReady (internal/httpapi/discover_status_test.go): a stub type embeds metadata.DiscoveryProvider and overrides Available(). The test covers a nil provider, Available()=false and Available()=true.; Go TestDiscoveryReadyMessageByRole: `a := &api{}`, with requests wrapped by withUser(). A requester gets the non-admin message and an admin gets the 'Settings → API keys' message, both with status 400.; Manual: clear the key, then walk Movies, Series and Discover as admin, manager and requester (LAN and external). Re-add the key and confirm the rows load.
  - **Depends on:** [COPY-01](#copy-01)
  - **Risk:** Low. Changing discoveryReady's signature touches 8 handlers, but `go vet` catches any that are missed. Leave room in the role wording for CFG's decision on whether managers can see API keys, since the backend already allows them (server.go:113).
  - **Resolves:** walk-1, series-15, movies-6, frontend-13, product-13
<a id="copy-03"></a>
- [x] **COPY-03 · Retire stale page names and roadmap copy: a real 404 page, 'Downloads' instead of 'Activity', Music and Books hints, Overseerr text** — `P1` · `S` · Phase 2
  - **Problem:** Several visible strings describe an older build:
- The catch-all route renders Placeholder title='Not found', so a mistyped URL reads 'Not found isn't built yet' (App.tsx:111-114, Placeholder.tsx:28).
- The search toasts say 'it'll appear in Activity' (Movies.tsx:152, MovieDetail.tsx:690). /activity only redirects to /downloads (App.tsx:85) and isn't in the nav.
- The Music module toggle says 'The Music module itself is still on the roadmap' (Settings.tsx:166). /status lists music as 'planned' (server.go:517). Yet README ships 'Music (early)' and about 13 music routes exist.
- The Books toggle says 'Open Library metadata' (Settings.tsx:165), though Hardcover takes over when a key is set.
- The Overseerr import says requests 'will appear on the Requests page' (Settings.tsx:789). That page was removed from routing.
- The Insights file-top comment (Insights.tsx:4-7) and App.tsx:35 still describe placeholder slices.
  - **Approach:** 1. New web/src/pages/NotFound.tsx:
       - PageHeader title 'Page not found'. Body: 'That address doesn't exist in Arrmada.'
       - Buttons: Dashboard (/) and Discover for staff, Discover only otherwise (via useMe/isStaff).
       - Reuse Placeholder's centred card layout and accent icon tile so the style is unchanged.
       - Use it for App.tsx's '*' route. Delete Placeholder.tsx, its import and the 'Module routes still awaiting their build' comment.
    2. Movies.tsx:152 becomes `Searching for “${m.title}” — follow it in ${PAGE.downloads} → Searching.` MovieDetail.tsx:690 becomes `Searching — follow it in ${PAGE.downloads} → Searching.` MOV's outcome-toast task (movies.t7) later replaces both.
    3. Settings.tsx:165 Books hint: 'Ebook and audiobook library, and the Books tab in Discover. Metadata comes from Hardcover when a key is set, otherwise Open Library.'
    4. Settings.tsx:166 Music hint: 'The Music library (early): artists, albums and their downloads. Off hides it from the navigation.' This is true today; MUS extends it when the toggle also stops music jobs.
    5. server.go:507-518: music becomes `{"music", "Music", true, "early"}`. Rewrite the comment above it ('what ships; early = usable but young') and the musicEnabled comment at settings.go:51-52.
    6. Settings.tsx:789: '…approved titles are added to your library and searched; they'll show in the requests row on Discover as they process.' REQ repoints this to /requests through LINKS.requests.
    7. Insights.tsx:4-7: replace the stale comment with a one-line description of the tabs that exist.
    8. Sweep web/src for 'roadmap', "isn't built" and 'in Activity' and fix any stragglers. Subtitles and Insights pills are handled in [COPY-05](#copy-05) and [COPY-08](#copy-08).
  - **Files:** `web/src/pages/NotFound.tsx`, `web/src/pages/Placeholder.tsx`, `web/src/App.tsx`, `web/src/pages/Movies.tsx`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/Settings.tsx`, `web/src/pages/Insights.tsx`, `internal/httpapi/server.go`, `internal/httpapi/settings.go`, `internal/httpapi/status_modules_test.go`
  - **Acceptance:**
    - /does-not-exist shows 'Page not found' with working buttons for an admin.
    - No toast names a page that doesn't exist; `rg -i 'in Activity' web/src` is empty.
    - No string says Music is on the roadmap or that Books uses only Open Library.
    - /api/v1/status lists music as available with status 'early'.
    - Placeholder.tsx is gone and `npm run build` passes.
  - **Tests:** Go TestStatusModulesMusicEarly (internal/httpapi/status_modules_test.go): plannedModules contains music with Available=true and Status='early', and no entry has Status 'planned'.; Manual: visit a bad URL as admin; trigger a movie search from the list and from the detail page and read the toast.
  - **Depends on:** [COPY-01](#copy-01)
  - **Risk:** None. These toasts change again when MOV's outcome toasts and REQ's /requests page land; both read from lib/links.ts.
  - **Resolves:** frontend-13, product-13, frontend-10, system-15, movies-6
<a id="copy-04"></a>
- [x] **COPY-04 · Stop promising music upgrades until an upgrade sweep exists** — `P1` · `S` · Phase 2
  - **Problem:** The Quality page and the presets promise music upgrades that no code performs.

The promises:
- The page says a music profile 'keeps upgrading until it reaches the top of the ladder you set' (Quality.tsx:279).
- The ladder hint says 'upgrading stops at…' (Quality.tsx:~1151).
- A working-looking 'Automatically upgrade' switch reads 'Replace an album when a higher tier on your ladder turns up' (Quality.tsx:1170-1179).
- The 'Lossless, or the best MP3' preset 'upgrades later if one appears' (internal/quality/music.go:57).

Why none of it happens:
- SearchMusicMissing skips complete albums (automation/music.go:54).
- main.go registers only search-missing-music and import-music.
- UpgradesEnabled is never read for music, and BetterFormat/FormatRank are never called.
- The package comment at quality/music.go:9-14 describes an upgrade loop that doesn't exist.
  - **Approach:** 1. Quality.tsx:279, music intro: 'A music profile is a ladder of audio qualities. Arrmada grabs the best tier available on your ladder. Replacing albums you already have with a better tier isn't automatic yet — use Search on an album to look for a better copy.'
    2. Quality.tsx:~1151 ladder hint, for music: 'Best first. Every tier you keep can be grabbed and the highest available wins. Tiers you leave off are never grabbed.' The Top chip stays and now just means 'the best tier you want'.
    3. Quality.tsx:1168-1180: for the music editor, replace the Upgrades section's Switch with a plain line in the same panel style: 'Automatic album upgrades aren't built yet.' Keep `upgrades_enabled` untouched in the saved payload so MUS can switch it back on.
    4. internal/quality/music.go:
       - Preset description: 'Prefers FLAC; takes MP3 320 or V0 when there's no lossless release.'
       - Rewrite lines 9-14 to say the scoring picks the best tier at grab time and that no music upgrade sweep exists yet.
    5. Add 'automatically upgrade' (music context) to MUS's upgrade-sweep task notes as copy to restore.
  - **Files:** `web/src/pages/Quality.tsx`, `internal/quality/music.go`, `internal/quality/music_test.go`
  - **Acceptance:**
    - No music copy on the Quality page or in MusicPresets() claims albums are upgraded.
    - The music profile editor shows no live upgrade switch.
    - Movie, series and book profile editors are unchanged, including their upgrade controls.
    - Saving a music profile keeps its existing upgrades_enabled value.
  - **Tests:** Go TestMusicPresetsDontPromiseUpgrades (internal/quality/music_test.go): no MusicPresets() Description contains 'upgrade' (case-insensitive).; Manual: Quality → Music tab and a music profile editor show the new text and no switch; a movie profile still shows its upgrade controls.
  - **Risk:** Minimal. If MUS ships the music upgrade sweep before this task, skip this task and let MUS keep the copy. Otherwise MUS's sweep task must restore the switch and the wording.
  - **Resolves:** music-7
<a id="copy-05"></a>
- [x] **COPY-05 · Subtitles page and job notes describe what actually ships** — `P1` · `S` · Phase 2
  - **Problem:** The Subtitles page treats shipped features as unreleased and claims work that belongs to Convert:
- The intro promises '(soon) AI transcription' and 'stripping the rest' (Subtitles.tsx:73).
- Kept languages are 'stripped from the video (once stripping ships)' (lines 152 and 755). Stripping is Convert's job, under the separate convert_keep_sub_langs setting (convert/decide.go:27,84; Convert.tsx:971).
- The page shows 'AI transcription — SOON' whenever no model is downloaded (line 160, `soon={!settings?.ai_ready}`), though whisper shipped in Sept 2026.
- Line 434: 'image subs + AI are coming… Health scoring lands with the sync phase.'
- Line 777: '(soon) AI'.
- The header pill reads 'AI + embedded only' even when AI isn't ready (line 58).
- The Health column always shows '—', because SubHealth is never filled (library.go:18-41).

The backend job note 'N language(s) need OCR/AI — coming soon' (process.go:83) is also wrong. bestSource never returns 'ocr' (library.go:135-139), so a language is only left pending in two cases:
(a) AI is the only source and no model is installed.
(b) A model is installed, but whisper can't make that language: the audio isn't in it, and the target isn't English (aiPlan, whisper.go:551).
  - **Approach:** Subtitles.tsx:
    1. Header pill (58): build it from real state as a list of working sources, e.g. 'Sources: embedded · OpenSubtitles · AI'.
       - Use 'OpenSubtitles (search only)' when provider_ready && !can_download.
       - Show only 'Sources: embedded' when nothing else works.
       - Green when anything beyond embedded works.
    2. Row2 (177-183): replace `soon` with `state: 'on' | 'off' | 'setup' | 'unsupported'` plus an optional onClick.
       - Embedded extract: on.
       - OpenSubtitles: on, or 'set up' as a button that switches to the Settings tab.
       - AI transcription: on when ai_ready, otherwise 'needs a model' as a button that switches to the Settings tab (LocalAI, line 798).
       - Image-sub OCR: 'not supported'.
    3. Line 73: 'One external .srt per language next to every video, made from the best source available: an embedded text track, an OpenSubtitles download, or local AI transcription when a model is installed. Pick languages in Settings.'
    4. Lines 152 and 755: 'Embedded subtitle tracks stay inside the video. Convert can remove the languages you don't keep — set that in Convert → Settings.' The link uses LINKS.convertSettings.
    5. Line 434: 'Ensure subs makes any missing kept-language .srt from the best available source.'
    6. Line 777: 'Optional — embedded extraction and local AI work without it.'
    7. Remove the Health column: the HEADERS entry (line 400), the episode table header (630) and the cell (674). Keep the SubHealth type in api.ts and library.go for SUB's scoring task.
    
    internal/subtitles/process.go:
    8. In the source loop (lines 53-80), count `noModel` (an 'ai' source while !aiOK) and `noAIPath` (aiOK but aiPlan returned '') instead of a single `pending`.
    9. Extract `func skipNote(noModel, noAIPath int) string`:
       - 0, 0: 'all kept languages already have subtitles'.
       - noModel > 0: '%d language(s) need AI transcription — install a model in Subtitles → Settings, or set up OpenSubtitles'.
       - noAIPath > 0: '%d language(s) can't be made: no subtitle track or download, and AI can only transcribe the spoken language or translate into English'.
       - Join both with '; ' when both apply.
    10. Fix the header comment at process.go:13-15 and the 'default: // ocr — not implemented yet' comment.
    11. Append 'coming soon', '(soon)', 'once stripping ships' and 'Health scoring lands' to the [COPY-07](#copy-07) guard if it has landed.
  - **Files:** `web/src/pages/Subtitles.tsx`, `internal/subtitles/process.go`, `internal/subtitles/skipnote_test.go`
  - **Acceptance:**
    - Subtitles.tsx and internal/subtitles contain no 'soon', 'once stripping ships' or 'Health scoring lands' in user-visible text.
    - With no whisper model installed, the Overview shows 'AI transcription — needs a model', and clicking it opens the Settings tab. With a model it shows 'on'.
    - No Subtitles copy claims Arrmada strips languages from the video here; the mention links to Convert settings.
    - The Library tables have no Health column.
    - A skipped job's note names the real reason (no model, or a language AI can't make), never 'coming soon'.
  - **Tests:** Go TestSkipNote (internal/subtitles/skipnote_test.go): covers (0,0), (2,0), (0,1) and (1,1), and checks that no output contains 'soon'.; `go test ./internal/subtitles/...` still passes; grep the tests for the old note text (none expected).; Manual: Overview, Library and Settings tabs with and without a model, and with and without OpenSubtitles, in both themes.
  - **Depends on:** [COPY-01](#copy-01)
  - **Risk:** Low. If SUB derives Convert's keep list from the Subtitles languages, the kept-languages sentence changes again; coordinate with that task. The Health column comes back only with SUB's real scores.
  - **Resolves:** subtitles-9, walk-2
<a id="copy-06"></a>
- [x] **COPY-06 · Movie detail and delete dialogs say what really happens: upgrade watching and the recycle bin** — `P1` · `S` · Phase 2
  - **Problem:** The WhyPanel and the delete dialogs promise things that don't happen.

WhyPanel (MovieDetail.tsx:621-623):
- For any movie with a file it says Arrmada 'keeps watching for a clearly-better release… (checked every 6 hours)'.
- But UpgradeMovies skips !m.Monitored (coordinator.go:790-792), and scanned imports are created Monitored:false with profile 'n/a' (movies/service.go:256-260). So the claim is false for the owner's whole scanned library.
- It hedges 'if your profile allows upgrades' instead of checking quality.Service.AllowsUpgrades (quality/service.go:300).

Delete dialogs always say files go to the recycle bin, even when ARRMADA_RECYCLE_DIR=off hard-deletes (main.go:199-206):
- Movies.tsx:628
- Books.tsx:520
- BookDetail.tsx:455
- SeriesDetail.tsx:438 and :477
  - **Approach:** 1. movies.Movie (internal/movies/movie.go): add the computed field `UpgradesAllowed bool json:"upgrades_allowed"`.
       - In handleGetMovie (httpapi/movies.go:255), set `m.UpgradesAllowed = upgradeWatched(m.Monitored, m.HasFile, a.deps.Quality.AllowsUpgrades(ctx, m.QualityProfile))`.
       - `upgradeWatched` is a small pure helper in movies.go.
    2. WhyPanel builds its text from that state:
       - File recorded but gone (`movie.file?.missing`): 'Arrmada recorded a file for this movie but it's no longer on disk. Refresh & rescan, or search again.' Tone: reject.
       - Has a file and !monitored: 'You have this movie. It isn't monitored, so Arrmada won't look for upgrades — turn on Monitor to allow them.'
       - Monitored and !upgrades_allowed: 'You have this movie. Its quality profile doesn't upgrade, so this file stays as it is.'
       - upgrades_allowed: 'You have this movie. Arrmada checks for a clearly better release every 6 hours and grabs it automatically.' Before shipping, confirm the 6-hour cadence against the upgrade job's registration in cmd/arrmada/main.go.
    3. New web/src/lib/useRecycle.ts:
       - `useRecycleEnabled(): boolean | null` calls api.recycleStats() once and caches the result at module level. The route is manager+, and every delete is staff-only.
       - `deleteFilesNote(enabled, what)` returns:
         - enabled: 'Moves {what} to the recycle bin.'
         - false: 'Permanently deletes {what} — the recycle bin is turned off.'
         - null: 'Deletes {what}.'
    4. Use it at Movies.tsx:628, Books.tsx:520, BookDetail.tsx:455, the SeriesDetail.tsx:438 confirm text and the SeriesDetail.tsx:477 title. Convert's copy stays with CONV.
  - **Files:** `internal/movies/movie.go`, `internal/httpapi/movies.go`, `internal/httpapi/movies_upgrade_test.go`, `web/src/lib/api.ts`, `web/src/lib/useRecycle.ts`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/Movies.tsx`, `web/src/pages/Books.tsx`, `web/src/pages/BookDetail.tsx`, `web/src/pages/SeriesDetail.tsx`
  - **Acceptance:**
    - A scanned-in (unmonitored) movie's detail page says upgrades are off until it's monitored, never that Arrmada is watching.
    - A monitored movie whose profile has upgrades off says the file stays as it is.
    - A movie whose file vanished shows the 'no longer on disk' explanation.
    - With ARRMADA_RECYCLE_DIR=off, every delete dialog says the delete is permanent; with recycling on, it names the recycle bin.
  - **Tests:** Go TestUpgradeWatched: a table over (monitored, hasFile, profileAllows). The result is true only for (true, true, true).; Manual: open a scanned movie, a monitored movie on an upgrade profile and one on a no-upgrade profile. Toggle recycling off in a dev container and open each delete dialog.
  - **Risk:** Low. OBS/MOV's search-outcome work ('last searched 3h ago…') extends WhyPanel later and should keep these branches. If SAFE makes the recycle bin stop silently hard-deleting on failure, this copy becomes fully true.
  - **Resolves:** movies-6
<a id="copy-07"></a>
- [x] **COPY-07 · Copy guard: a Go test that fails CI when retired phrases come back** — `P2` · `S` · Phase 2
  - **Problem:** Every stale string the audit found had been true once and was never revisited. Nothing stops 'coming soon', an env-var instruction or a dead page name from being reintroduced. The frontend has no test runner, but CI already runs `go test -race ./...` on a full checkout.
  - **Approach:** 1. New internal/webui/copy_test.go (package webui).
       - Locate the repo root by walking up from the test's directory to go.mod, then walk web/src for *.ts and *.tsx. Call t.Skip if web/src is absent, so the Docker race run still works.
       - Also scan non-test *.go files under internal/ and cmd/ for string literals (lines containing a quote) matching the same patterns.
    2. Build a table of `{pattern *regexp.Regexp, reason string}`, case-insensitive:
       - ARRMADA_TMDB_API_KEY → 'keys are entered in Settings → API keys'
       - coming soon
       - \(soon\)
       - once [a-z ]+ ships
       - isn['’]t built yet
       - on the roadmap
       - lands with the
       - (appear|show)[a-z]* in Activity
       - Requests page
       - Discover-only
       - only the Discover page
       - update\.sh (web/src only)
    3. A line containing `copy-ok` is exempt, for deliberate exceptions such as code comments that explain history.
    4. On failure, print each `file:line`, the matched text and the reason, e.g. 'Insights tabs are built — show a Connect Plex state instead'.
    5. Document in the test header: when a COPY task retires a phrase, add it here.
  - **Files:** `internal/webui/copy_test.go`
  - **Acceptance:**
    - `go test ./internal/webui/` passes on main after COPY-02, COPY-03, COPY-04 and COPY-05.
    - Re-adding 'coming soon' to any .tsx file makes the test fail with file:line and the reason.
    - The test runs in CI's `go test -race ./...` and is skipped cleanly when web/src is missing.
  - **Tests:** The test itself.; A self-check sub-test: run the matcher over a small in-memory sample containing one of each banned phrase and one copy-ok line; it must flag every phrase except the exempt line.
  - **Depends on:** [COPY-02](#copy-02), [COPY-03](#copy-03), [COPY-04](#copy-04), [COPY-05](#copy-05)
  - **Risk:** False positives on legitimate words, e.g. 'as soon as' is fine, which is why the bare word 'soon' isn't banned. Keep the patterns specific. COPY-08 and COPY-09 append 'Coming soon' (Insights) and 'Discover-only' only once their strings are gone, or this test goes red.
  - **Resolves:** 

#### Milestone: M2 — Settings, Insights, Dashboard and Books describe the real setup

_Insights shows a Connect Plex state instead of 'Coming soon' and shows imported history. Settings explains each role from the same list the nav uses. The disk guard points at the Downloads folder picker. The Dashboard says 'Plex isn't connected yet' with a link, shows the real version and drops the constant Auth stat. Book pages link to the catalogue the book is actually on._

<a id="copy-08"></a>
- [x] **COPY-08 · Insights: a Connect Plex empty state instead of 'Coming soon', imported history visible, live errors scoped** — `P2` · `S` · Phase 3
  - **Problem:** When Plex isn't configured, every built Insights tab returns ComingSoon with a 'Coming soon' pill (Insights.tsx:153, 299, 477, 572, 694, 909-918).

History, Users, Graphs and Reliability read only from the database, but they are gated on `connected`, meaning a URL and token are set (line 26). So a Tautulli import done before connecting Plex is invisible.

When the live Activity call fails, the error replaces the whole tab, including the database-backed HomeExtras stats (line 154).

The intro always ends 'Connect your server in Settings to begin' (line 33), even when connected. It is also ambiguous next to Settings' 'connected in Insights' (frontend-10).
  - **Approach:** 1. Replace ComingSoon (909-918) with `ConnectPlex({ tab, onConfigure })`.
       - It shows the tab title, the description from NEXT (renamed ABOUT), and the existing 'Connect your Plex server →' button that switches to the Settings tab. No pill.
    2. HistoryView, UsersView, GraphsView and ReliabilityView: drop `if (connected)` from the fetch effects and drop the early return. After loading:
       - Empty data and !connected: show ConnectPlex.
       - Data present and !connected: show a slim notice above the data, 'Showing imported history — connect Plex to record new plays', with the same button.
       - Check each view's empty rendering: Users with [], Graphs with anyPlays false, Reliability with no groups.
    3. ActivityView:
       - !connected: show ConnectPlex, and still render `<HomeExtras/>` below it when it has stats.
       - Connected with a live-call error: render the 'Couldn't reach Plex: …' box inside the live-streams block only, then always render HomeExtras.
       - Restructure so the live section computes error, loading or streams, and HomeExtras sits outside that branch.
    4. Line 33: always show the base sentence. When !connected, append a text button: 'Connect your Plex server in the Settings tab to begin.'
    5. Remove the stale header comment if [COPY-03](#copy-03) hasn't.
    6. Add 'coming soon' to the [COPY-07](#copy-07) guard if it isn't already there.
    7. Leave the 'Plex connected' pill (line 36) to PLEX's 'recording' badge task (insights-3).
  - **Files:** `web/src/pages/Insights.tsx`, `internal/webui/copy_test.go`
  - **Acceptance:**
    - With Plex unconfigured, no Insights tab shows 'Coming soon'; each shows a Connect Plex state with a working button.
    - With Tautulli-imported history and Plex unconfigured, the History tab lists the imported plays under the notice. Users, Graphs and Reliability show their data the same way.
    - With Plex configured but unreachable, the Activity tab shows the error inside the live section and the watch-stat cards still render.
    - When Plex is connected, the intro has no 'to begin' call to action.
  - **Tests:** Manual: fresh dev DB, then a Tautulli import with Plex unconfigured; walk all tabs.; Manual: set a wrong Plex URL and check the Activity tab.; `npm run build` passes; the COPY-07 guard passes with 'coming soon' banned.
  - **Depends on:** [COPY-01](#copy-01), [COPY-07](#copy-07)
  - **Risk:** Low. Un-gating the database tabs means each view must render empty data gracefully. Check every one with an empty DB. PLEX may later move the Plex connection out of Insights; ConnectPlex should then use LINKS.plexConnection.
  - **Resolves:** insights-11, walk-2, product-13
<a id="copy-09"></a>
- [x] **COPY-09 · Settings: role descriptions built from the real nav, Read-only in Add user, Library intro once, disk guard points to the folder picker** — `P2` · `M` · Phase 3
  - **Problem:** Roles:
- Settings → Users says 'Requesters see only the Discover page' (Settings.tsx:221).
- Plex sign-in says 'Requester account (Discover-only)' (line 168).
- In fact non-staff get Discover, Calendar, Books (when the module is on) and Audiobooks on the LAN, and Discover, Books and Audiobooks from outside (App.tsx:51-77, UserLayout.tsx:16-19). That list is maintained by hand in two places.
- Manager and Read-only aren't explained, and Add user (lines 252-256) doesn't offer Read-only, though Edit user does (line 300).
- README:39 says outside visitors 'only see Discover'.

Library and disk guard:
- The Settings → Library section subtitle (line 153) repeats Library.tsx:47 and apologises '(Has its own Save folders button below the list.)'.
- The disk-guard note (lines 662-669) says to set ARRMADA_DOWNLOADS_DIR in .env and re-run ./update.sh.
- But the Downloads folder picker writes lib_downloads_dir, and ApplySavedLibraryDirs (setup.go:42) makes it override .env at startup, so that advice can do nothing at all.
  - **Approach:** 1. web/src/lib/nav.ts: add `export function requesterNav({ external, booksEnabled }): NavItem[]`. It returns Discover, Calendar (only when !external), Books (booksEnabled) and Audiobooks.
       - UserLayout.tsx:16-19 uses it.
       - App.tsx builds the non-staff and external route trees from it through a `REQUESTER_ELEMENTS: Record<string, JSX.Element>` map, keeping `*` → /discover.
       - Diff the generated routes against today's trees; they must be identical.
    2. UsersManager:
       - Subtitle: 'Add people who can use Arrmada. Auto-approve lets a user's requests download without waiting for you.'
       - Add a compact RoleLegend under it, styled like the existing 11px faint hint text:
         - Requester: the pages from requesterNav (Calendar marked 'at home only'), plus requesting.
         - Read-only: the same pages, but can't request (POST /requests needs RoleRequester, server.go:298).
         - Manager: the whole console except Settings → System and Users, the audiobook server settings and the Overseerr/Tautulli imports (admin gates at server.go:123-134, 165-168, 304-305).
         - Admin: everything.
       - Re-verify each line against requireRole in server.go when implementing, and again after SEC's re-gating.
    3. Add `<option value="readonly">Read-only</option>` to the Add-user select.
    4. Plex sign-in Section (line 168): change `Section.subtitle` to ReactNode. New text: 'Let your Plex Home members and shared users sign in with Plex — no accounts to hand out. They get a Requester account ({requesterNav labels}), and only people with access to your Plex server get in. Needs your Plex server connected in Insights → Settings.' The 'Insights → Settings' part is a Link to LINKS.plexConnection.
    5. Settings → Library Section (line 153): the subtitle becomes 'Where each library lives on disk.' and the section gets id 'media-folders'. Library.tsx:47 stays the single explanation.
    6. DiskGuardSection Note:
       - New text: 'The guard measures one folder: your Downloads folder, set in Settings → Library (currently {status.path}). If that folder is on your main array rather than the torrent or cache drive, the percentage measures the array — choose the right folder there.' 'Settings → Library' links to LINKS.libraryFolders.
       - Remove the .env and update.sh advice.
       - Fix the comments at Settings.tsx:649 and httpapi/downloads.go:120 to name lib_downloads_dir with the env var as a fallback.
    7. Library.tsx:14 downloads hint: 'where the download client saves files — the disk guard watches this folder'.
    8. README.md:39: 'Visitors from outside your network get Discover, their books shelf and audiobooks — never the admin pages.'
    9. Append 'Discover-only', 'only the Discover page' and 'update.sh' (web/src) to the [COPY-07](#copy-07) guard.
  - **Files:** `web/src/lib/nav.ts`, `web/src/components/UserLayout.tsx`, `web/src/App.tsx`, `web/src/pages/Settings.tsx`, `web/src/pages/Library.tsx`, `internal/httpapi/downloads.go`, `README.md`, `internal/webui/copy_test.go`
  - **Acceptance:**
    - No 'only the Discover page' or 'Discover-only' text remains in web/src or the README.
    - The legend's Requester line lists exactly the pages a requester's top bar shows. Turning the Books module off removes Books from both the bar and the legend.
    - Add user offers Requester, Read-only, Manager and Admin.
    - Settings → Library shows its introduction once.
    - The disk-guard note has no '.env' or 'update.sh', shows the folder being measured, and links to Settings → Library.
  - **Tests:** Manual: log in as a requester on the LAN and as an external requester; compare the top bar with the legend. Toggle the Books module and repeat.; Manual: before/after check that the requester and external route trees in App.tsx render the same paths (try /calendar externally; it must still redirect).; `npm run build` passes; the COPY-07 guard passes with the new phrases.
  - **Depends on:** [COPY-01](#copy-01), [COPY-07](#copy-07)
  - **Risk:** Low. Generating the route trees must not widen requester access; the backend external gate stays the authority. The legend describes today's gates, and SEC's route re-gating may change Manager or Read-only, so re-check it then. A changed Downloads folder only takes effect after a restart; CFG's restart banner covers that, and the note already shows the folder actually being measured.
  - **Resolves:** walk-4, walk-5, system-15, product-13
<a id="copy-10"></a>
- [x] **COPY-10 · Dashboard and system strings: 'Plex isn't connected yet', the real version, no constant Auth stat, honest Logs and BASE_URL notes** — `P2` · `S` · Phase 3
  - **Problem:** Several system strings on the Dashboard, the Logs page and in docker-compose are wrong:
- With Plex not set up, the Dashboard reads 'Plex isn't reachable — plex is not configured' (Dashboard.tsx:110). handleDashboard passes the raw error from plex/client.go:36 into streams_note.
- The Dashboard shows the constant 'Auth: enabled' (Dashboard.tsx:231; server.go:561 hard-codes true).
- It shows version 'dev-docker', because docker-compose.yml:9 hard-codes VERSION and never passes COMMIT.
- The Logs footer says lines are 'kept in memory, up to 5000' (Logs.tsx:154). The ring actually holds 50,000 (main.go:687) and is mirrored to rotating files of up to 24 MB × 4 (applog/persist.go); 5000 is only the API's per-request cap (logs.go:22).
- docker-compose.yml:40-41 offers ARRMADA_BASE_URL to 'serve under a path behind a reverse proxy'. But the SPA hard-codes /api/v1 and /discover, vite has no `base` and the router has no basename, so a sub-path deployment can't load.
  - **Approach:** 1. dashboard.go:
       - Add `PlexConfigured bool json:"plex_configured"`. Before calling Activity: `cfg := a.deps.Insights.Config(ctx); out.PlexConfigured = cfg.URL != "" && cfg.TokenSet`.
       - Call Activity only when configured, so StreamsNote carries only real failures.
       - Add the sentinel `var ErrNotConfigured = errors.New("plex is not configured")` in internal/plex/client.go and return it at lines 36 and 167.
    2. Dashboard.tsx:107-112:
       - !plex_configured: 'Plex isn't connected yet.' with a Link 'Connect Plex' to LINKS.plexConnection.
       - Configured with a note: 'Plex isn't reachable — {note}'.
       - Otherwise: 'Nothing is streaming right now.'
    3. Dashboard.tsx:231: remove the Auth stat and set the grid to `sm:grid-cols-4`. OBS may add Last backup there. The Version stat shows `{version} · {commit}` when commit isn't 'unknown'.
    4. docker-compose.yml build args: `VERSION: ${ARRMADA_VERSION:-dev}` and `COMMIT: ${ARRMADA_COMMIT:-unknown}`. The Dockerfile already declares both ARGs.
       - update.sh before `docker compose up -d --build` (line 102), and the same in install.sh:
         - `export ARRMADA_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)`
         - `export ARRMADA_VERSION=$(git log -1 --format=%cd --date=format:%Y.%m.%d 2>/dev/null || echo dev)`
    5. Logs.tsx:154, unless OBS's Logs task has already replaced the footer: 'Showing the newest {n} lines (the viewer loads up to 5,000). Arrmada keeps the last 50,000 in memory and writes every line to rotating log files in its data folder.'
    6. docker-compose.yml:40: the comment becomes 'Sub-path hosting isn't supported by the web UI yet — leave this empty and serve Arrmada at the root of its own port or hostname.' Add a startup Warn in cmd/arrmada/main.go when cfg.BaseURL != "". This holds until CFG/INT implement real base-path support.
  - **Files:** `internal/httpapi/dashboard.go`, `internal/httpapi/dashboard_test.go`, `internal/plex/client.go`, `web/src/lib/api.ts`, `web/src/pages/Dashboard.tsx`, `web/src/pages/Logs.tsx`, `docker-compose.yml`, `update.sh`, `install.sh`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - With Plex not set up, the Dashboard says 'Plex isn't connected yet' with a working Connect Plex link and no error text.
    - With a wrong Plex URL, the Dashboard still says 'Plex isn't reachable — <real error>'.
    - The Dashboard has no Auth stat, and after ./update.sh it shows a dated version and the short commit instead of 'dev-docker'.
    - The Logs footer states the real retention.
    - The compose comment no longer advertises sub-path hosting.
  - **Tests:** Go TestDashboardPlexNotConfigured (dashboard_test.go, using dashAPI's temp store): build an insights.Service over that store's settings with no URL or token. handleDashboard then returns plex_configured=false and an empty streams_note.; Manual: run ./update.sh on a test checkout and read the Dashboard version; open Logs.
  - **Depends on:** [COPY-01](#copy-01)
  - **Risk:** Low. update.sh runs on the owner's Unraid box, so keep the git calls guarded (`|| echo`) so a non-git copy still builds. The Logs footer may be superseded by OBS's Logs work.
  - **Resolves:** system-15, product-13
<a id="copy-11"></a>
- [x] **COPY-11 · Book pages name and link the catalogue each book actually comes from** — `P2` · `S` · Phase 3
  - **Problem:** Book pages assume every book comes from Open Library:
- The BookDetail badge always links to `https://openlibrary.org/works/${b.ol_key}` (BookDetail.tsx:61), a broken URL for every hc: and gb: book.
- The 'Change match' tooltip says 'search Open Library' (BookDetail.tsx:331), although the rematch search uses the current catalogue (api.lookupBooks returns its source).
- The cover picker says 'Covers from Open Library editions and Google Books' (BookDetail.tsx:146), though Hardcover books get Hardcover editions only (hardcover.go:565).
- AuthorDetail's empty state says 'on Open Library' (AuthorDetail.tsx:123) even for Hardcover authors.
  - **Approach:** 1. New internal/books/cataloguelink.go:
       - `type CatalogueRef struct { Name string json:"name"; URL string json:"url" }` and `func CatalogueLink(key, slug, title, author string) CatalogueRef`:
         - metadata.IsHardcoverKey(key): with a slug, Hardcover at https://hardcover.app/books/<slug>. Without one, Hardcover at a search URL with url.QueryEscape(title + " " + author). Verify Hardcover's search URL in a browser first, and fall back to https://hardcover.app if there's no search route.
         - 'gb:<id>': Google Books at https://books.google.com/books?id=<id>.
         - '/works/OL…W' or bare 'OL…W': Open Library at https://openlibrary.org/works/OL…W.
         - Anything else: the zero value, and the UI hides the badge.
    2. books.Book: add the computed field `Catalogue *CatalogueRef json:"catalogue,omitempty"`. handleGetBook (httpapi/books.go:168) fills it with an empty slug; [COPY-15](#copy-15) adds slugs.
    3. BookDetail.tsx:61: the badge label is `b.catalogue.name` and the link is `b.catalogue.url`. Keep the existing chip shape: the current navy for Open Library, and the panel/line style for the others so the palette doesn't change. Hide the badge when there's no catalogue.
    4. BookDetail.tsx:331 tooltip: 'This is the wrong book — search the catalogue and re-link it to the right one, keeping your files.'
    5. BookDetail.tsx:146 cover copy:
       - catalogue.name === 'Hardcover': 'Covers from Hardcover editions — or upload your own.'
       - Otherwise: 'Covers from Open Library editions and Google Books — or upload your own.'
       - Fix the handler comment at httpapi/books.go:408.
    6. AuthorDetail.tsx:123: name 'Hardcover' when the resolved author key starts with 'hc:a:' (already checked around line 35), otherwise 'Open Library'.
    7. Grep web/src for other visible 'Open Library' strings and make them conditional where Hardcover can be the source. Books.tsx:346 is already conditional.
  - **Files:** `internal/books/cataloguelink.go`, `internal/books/cataloguelink_test.go`, `internal/books/repo.go`, `internal/httpapi/books.go`, `web/src/lib/api.ts`, `web/src/pages/BookDetail.tsx`, `web/src/pages/AuthorDetail.tsx`
  - **Acceptance:**
    - On a Hardcover book the badge reads 'Hardcover' and opens a hardcover.app page for that book (a search until COPY-15).
    - Open Library and Google Books books link to their own sites.
    - With Hardcover as the source, nothing on BookDetail or AuthorDetail says 'Open Library'.
  - **Tests:** Go TestCatalogueLink (table): hc with slug, hc without slug (the title and author are query-escaped), gb:abc, /works/OL123W, OL123W, and empty or unknown keys.; Manual: one book from each catalogue; open the badge, the cover picker and an author page.
  - **Risk:** Hardcover's search URL format is unverified; check it before shipping. The fallback is the Hardcover home page.
  - **Resolves:** books-11

#### Milestone: M3 — One vocabulary and honest empty pages

_Every status word means one thing on every page, and a vanished file says 'File missing'. Breadcrumbs match the sidebar. Empty Downloads and library pages say what to do next. Hardcover books link straight to their hardcover.app page._

<a id="copy-12"></a>
- [ ] **COPY-12 · One status vocabulary: Downloaded / Complete / Partial / Wanted / Unmonitored / File missing, everywhere** — `P2` · `M` · Phase 5
  - **Problem:** The same state has different names on different pages, and the same name means different things.

Missing vs Wanted:
- Library filters say 'Missing', meaning no file even when unmonitored (Movies.tsx:17/27, Series.tsx:15/30, Books.tsx:11/21).
- The badges on the same rows say 'Wanted', meaning monitored with no file (Movies.tsx:308, Books.tsx:38, MovieDetail.tsx:184).

Other mismatches:
- Movies' 'Available' filter means downloaded.
- SeriesDetail says 'In progress' (205) where Series says 'Partial' (296).
- ArtistDetail says 'Paused' (90) where everything else says 'Unmonitored'.
- AlbumDetail tracks say 'have / missing' (171), and BookDetail editions say 'on disk / wanted' (736).
- The MovieDetail Versions toggle reads 'Monitored / Unmonitored' (327) while every other toggle reads 'Monitored / Monitor'.
- A movie whose file is gone keeps a green 'Downloaded' header badge above the 'File missing from disk' panel (MovieDetail.tsx:183 vs 752).
- Search placeholders vary: 'Search titles…', 'Search title or author…', 'Filter artists…'.
  - **Approach:** 1. New web/src/lib/status.ts.
       - The header comment is the glossary from the epic design.
       - Export `libraryStatus(x: { hasFile: boolean; monitored: boolean; fileMissing?: boolean; have?: number; total?: number; multi?: boolean }): { label; tone; soft }`. Resolution order:
         - fileMissing: 'File missing' (reject)
         - multi && total > 0 && have >= total: 'Complete' (good)
         - !multi && hasFile: 'Downloaded' (good)
         - have > 0: 'Partial' (avoid)
         - monitored: 'Wanted' (avoid)
         - otherwise 'Unmonitored' (faint)
    2. Replace the local status helpers:
       - Movies.tsx:307
       - MovieDetail.tsx:183, passing `fileMissing: m.file?.missing`
       - MovieDetail.tsx:296 (versions)
       - Series.tsx:295
       - SeriesDetail.tsx:205 ('In progress' becomes 'Partial')
       - Books.tsx:36
       - BookDetail.tsx:36
       - AuthorDetail.tsx:139
       - AlbumDetail.tsx:96
       - ArtistDetail.tsx:90 ('Paused' becomes 'Unmonitored')
       - Keep the Books 'EPUB ✓'-style tag labels as a 'Downloaded' variant.
    3. Filters:
       - 'Missing' becomes 'Wanted' and means monitored with no file: Movies, Books, and Series (monitored && have < episodes).
       - Movies 'Available' becomes 'Downloaded'.
       - Music 'Incomplete' becomes 'Wanted' (monitored && !complete).
       - Persisted filter values are mapped when read: 'missing' and 'incomplete' → 'wanted', 'available' → 'downloaded'. Any other unknown value → 'all'.
    4. Row labels: AlbumDetail.tsx:171 becomes 'downloaded / wanted'; BookDetail.tsx:736 becomes 'downloaded / wanted'.
    5. MovieDetail.tsx:327 versions toggle reads 'Monitored' / 'Monitor', like the others.
    6. Search placeholders: 'Search movies…', 'Search series…', 'Search books or authors…', 'Search artists…'.
    7. Discover/BooksDiscover request chips are left to REQ.
  - **Files:** `web/src/lib/status.ts`, `web/src/pages/Movies.tsx`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/Series.tsx`, `web/src/pages/SeriesDetail.tsx`, `web/src/pages/Books.tsx`, `web/src/pages/BookDetail.tsx`, `web/src/pages/AuthorDetail.tsx`, `web/src/pages/Music.tsx`, `web/src/pages/ArtistDetail.tsx`, `web/src/pages/AlbumDetail.tsx`
  - **Acceptance:**
    - Each status word in the glossary means the same thing on every library and detail page; 'In progress', 'Paused' and the bare status word 'Missing' no longer appear as statuses.
    - The 'Wanted' filter on Movies, Series, Books and Music shows exactly the items whose badge says Wanted or Partial.
    - A movie whose file vanished shows a red 'File missing' header badge, never a green Downloaded.
    - A user whose saved filter was 'missing' or 'available' lands on the renamed filter, not on a blank list.
  - **Tests:** `npm run build` passes.; Manual: walk Movies, Series, Books and Music lists and details with monitored, unmonitored, partial and complete items, and a movie whose file was deleted on disk.
  - **Depends on:** [COPY-06](#copy-06)
  - **Risk:** Changing the filter from 'no file' to 'monitored and no file' hides unmonitored titles from it. Movies keeps its 'Unmonitored' filter; Series and Books don't have one, so say so in the release note. 'Partial' and 'Complete' for series are only as accurate as SER's monitored-only episode counts (series-10). Chips with the avoid tone need FE's --avoid-soft token (frontend-1) to get their fill.
  - **Resolves:** 
<a id="copy-13"></a>
- [x] **COPY-13 · Breadcrumbs derived from the sidebar groups** — `P3` · `S` · Phase 3
  - **Problem:** Page crumbs are hard-coded strings that contradict the nav:
- Subtitles and Convert say 'Library / …' but sit under Services (Subtitles.tsx:70, Convert.tsx:130).
- Review says 'Activity / Review', though no Activity group exists (Reviews.tsx:27).
- Downloads and History use descriptive subtitles ('Transfers', 'Imported to library') instead.

About 35 PageHeader calls carry their own crumb, so FE's nav regroup would leave all of them stale again.
  - **Approach:** 1. web/src/lib/nav.ts: add `crumbFor(pathname: string): string`. It finds the NAV item whose `to` is the longest prefix match ('/' only matches exactly) and returns `${group} / ${label}`. The ungrouped first section maps to 'Overview'.
    2. PageHeader.tsx:
       - `crumb` becomes optional with default `crumbFor(useLocation().pathname)`.
       - Add `crumbExtra?: string`, appended after ' / ', e.g. Quality's ' / Music', AuthorDetail's ' / Author'.
    3. Remove the hard-coded crumb props from Movies, Series, Books, Music, the detail pages, Subtitles, Convert, Insights, Audiobooks, Calendar, Discover, Indexers, DownloadClients, Logs, Settings, Quality, Reviews, Downloads, History and Dashboard. Keep only the crumbExtra tails.
  - **Files:** `web/src/lib/nav.ts`, `web/src/components/PageHeader.tsx`, `web/src/pages/*.tsx`
  - **Acceptance:**
    - Every page's crumb matches its sidebar group and label (Subtitles shows 'Services / Subtitles' with today's nav).
    - After FE regroups NAV, crumbs follow with no page edits.
    - Detail pages show their parent crumb, e.g. 'Library / Movies' on a movie.
  - **Tests:** `npm run build` passes.; Manual: click through every sidebar entry and one detail page per library and compare the crumb with the sidebar.
  - **Risk:** Low. If FE's nav regroup lands first, this simply follows it. Requests.tsx is unrouted, so leave it alone; REQ decides its fate.
  - **Resolves:** frontend-10
<a id="copy-14"></a>
- [ ] **COPY-14 · Empty states that tell the truth: no download client, unknown free space, scanning existing libraries** — `P3` · `S` · Phase 16
  - **Problem:** Some empty pages report a fake value or give incomplete advice:
- With no measurable downloads folder, Downloads shows 'free 0 GB'. handleDownloadsFeed ignores the ok flag (`freeGB, _ := diskspace.FreeGB(...)`, httpapi/activity.go:~206) and the page renders it (Downloads.tsx:169).
- With no download client configured, Downloads just looks empty and never says why nothing can be grabbed.
- The library empty states only say 'Click Add movie…' (Movies.tsx:260, Series.tsx:254, Books.tsx:352, Music.tsx:255), although the owner's existing files are imported by scanning in Settings → Library.
  - **Approach:** 1. httpapi/activity.go:
       - Include `free_gb` only when `diskspace.FreeGB` reports ok.
       - Add `"clients": n`, the number of configured clients from a.deps.Downloads.List(ctx); on error, omit it.
    2. Downloads.tsx:
       - Hide the 'free' stat when free_gb is absent.
       - When `clients === 0`, show one card above the tabs: 'No download client is set up, so Arrmada can't grab anything. Add one on the Download clients page.' Link to LINKS.downloadClients, using the existing avoid-tone banner style.
    3. Library empty states:
       - 'No movies yet. Click Add movie to search for one, or scan the folder you already have in Settings → Library.' The link uses LINKS.libraryFolders.
       - Do the same for Series, Books and Music.
    4. If FE's shared EmptyState component has landed, use it; otherwise keep the current markup.
    5. Showing a 'download client unreachable' banner is ACQ/OBS's backend-12 work, not this task.
  - **Files:** `internal/httpapi/activity.go`, `web/src/lib/api.ts`, `web/src/pages/Downloads.tsx`, `web/src/pages/Movies.tsx`, `web/src/pages/Series.tsx`, `web/src/pages/Books.tsx`, `web/src/pages/Music.tsx`
  - **Acceptance:**
    - Downloads never shows 'free 0 GB' for a folder it can't measure.
    - With every download client removed, Downloads explains that nothing can be grabbed and links to Download clients.
    - Each empty library page mentions scanning an existing folder, with a working link.
  - **Tests:** Go: extract `freeGBField(path string) (float64, bool)` and test it with a temp dir (ok) and a nonexistent path (not ok).; Manual: remove the bundled client in a dev DB and open Downloads; open each library with an empty DB.
  - **Depends on:** [COPY-01](#copy-01)
  - **Risk:** Low. The bundled qBittorrent normally re-creates itself (download.Service.EnsureBundled), so the no-client card mostly appears after a deliberate removal. Check it doesn't flash during the first poll.
  - **Resolves:** 
<a id="copy-15"></a>
- [ ] **COPY-15 · Hardcover books link straight to their hardcover.app page (slug lookup, isolated and cached)** — `P3` · `S` · Phase 16
  - **Problem:** COPY-11 links Hardcover books to a search because Arrmada doesn't store Hardcover's slug. A direct link needs the slug, but adding `slug` to the shared hcBookFields (hardcover.go:173) is risky: if the field name is wrong live, the GraphQL error breaks every Hardcover search, browse and GetBook. The project memory notes that the Hardcover queries are unverified live.
  - **Approach:** 1. internal/metadata/hardcover.go: add `func (h *Hardcover) BookSlug(ctx context.Context, key string) (string, error)` with its own query, `books(where: {id: {_eq: $id}}, limit: 1) { slug }`.
       - It goes through the existing budgeted, cached request path with hcTTLBook (24h).
       - It is NOT added to hcBookFields, so a schema mismatch can only break this link.
       - When the day's budget is spent (ErrHardcoverBudget), return the error.
    2. BookSources.BookSlug routes hc keys to Hardcover and returns '' for others. books.Service.CatalogueSlug(ctx, key) is best-effort.
    3. handleGetBook: for hc keys, `slug, err := …`. On error, log once at Debug and pass '' so CatalogueLink falls back to the search URL. The page never fails because of the slug.
  - **Files:** `internal/metadata/hardcover.go`, `internal/metadata/bookswitch.go`, `internal/metadata/hardcover_slug_test.go`, `internal/books/service.go`, `internal/httpapi/books.go`
  - **Acceptance:**
    - A Hardcover book's badge opens its hardcover.app/books/<slug> page.
    - With Hardcover failing or out of budget, the badge still works, using the search URL, and the book page loads normally.
    - Opening the same book repeatedly within 24h makes at most one slug request.
  - **Tests:** Go TestHardcoverBookSlug (httptest server): a {data:{books:[{slug:'dune'}]}} response returns 'dune'; a GraphQL errors response returns an error; an empty books list returns ''.; Go: a second call within the TTL is served from the cache (request counter on the test server).; Live: after deploy, open one Hardcover book and check the log for GraphQL errors first.
  - **Depends on:** [COPY-11](#copy-11)
  - **Risk:** The slug field name on Hardcover's books type is unverified live. The isolated query and search fallback keep a wrong guess harmless, but check the log right after deploy.
  - **Resolves:** books-11

#### Risks

- Copy churn: several strings change again when MOV, REQ, CFG, ACQ and PLEX land. Mitigation: every link and page name goes through lib/links.ts, so a later move is a one-line edit, and the COPY-07 guard keeps the retired phrases out.
- The COPY-07 guard could go red on legitimate wording or block unrelated work. Mitigation: specific patterns (no bare 'soon'), a `copy-ok` line exemption, and an error message that explains what to write instead.
- COPY-12 changes the 'Missing' filter semantics (unmonitored titles leave it) and renames persisted filter keys. Mitigation: map the legacy persisted values when read, and keep the Movies 'Unmonitored' filter.
- COPY-09 generates the requester route trees from requesterNav. A mistake could widen what a requester can open in the UI. The backend external gate stays the authority; diff the route trees before and after.
- COPY-04 hides a switch MUS may be about to wire up. Skip COPY-04 if the MUS sweep is already in flight.
- Hardcover search URL and slug field are unverified live (COPY-11, COPY-15). Mitigation: a home-page fallback, an isolated slug query, and checking the log right after deploy.
- COPY-10 edits update.sh and install.sh, which run on the owner's Unraid box. Keep every git call guarded so a non-git copy still builds.

#### Out of scope

- Convert's own overclaiming copy (convert-4 Dolby Vision removal, convert-15 polish, the 'originals go to the recycle bin' text in Convert.tsx): CONV.
- The Insights 'Plex connected' pill vs actually recording (insights-3): PLEX.
- Remembering the theme across reloads (system-15 sub-claim, quick win #25): FE.
- Real ARRMADA_BASE_URL sub-path support (vite base, router basename, prefixed fetches): CFG/INT. COPY-10 only makes the compose comment honest and logs a warning.
- Discover request chips ('Pending' for auto-approved, 'Partly ready', the in-library 'Wanted' chip) and restoring or deleting Requests.tsx: REQ.
- The nav regroup itself and the Settings hub with sub-navigation: FE and CFG. COPY-13 only derives crumbs from whatever NAV says.
- Loading → error → empty ordering, skeletons, the root error boundary and 401 handling (frontend-9): FE.
- Setup wizard next steps as links (product-9) and the 'Books' label meaning two things in the requester bar (product-7): CFG and APP/REQ.
- Music torrents labelled 'Movie' and books and albums missing from Searching/Upcoming (ops-10): ACQ/OBS.
- A 'download client unreachable' banner and stopping grabs while the client is down (backend-12): ACQ/OBS.
- Moving every user-facing string into one i18n/strings module: not worth it for a one-owner app. COPY-07's guard covers the review need frontend-13 raised.
- The Health column's real sync scores and deriving Convert's keep list from the Subtitles languages: SUB.

