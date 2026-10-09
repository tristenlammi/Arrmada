# CFG — Settings & System hub, setup, users, deploy

_Part of the [Arrmada roadmap](../../ROADMAP.md). 31 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Make configuration trustworthy and findable. Every setting saves and says so. Folder changes take effect without a hidden restart. Folders are checked before they are used. The owner can deploy, roll back and see exactly what is running. People and access are managed in one place. All configuration lives in one Settings hub with a left rail, and a fresh install walks from first launch to a working first grab.

**Why.** For seven weeks the main Settings page has saved nothing. GET /settings gained server_time and server_tz, the page PUTs the whole object back, and the decoder rejects unknown fields (system-1). So naming, NFO, module toggles, Plex sign-in, the disk guard and the recycle limits cannot be changed at all.

Folder changes are worse than broken, because they are silently half-applied. Scans use the new folder at once, but imports, qBittorrent's save path and the disk guard keep the old one until a restart that nobody is told about. The library ends up split (system-4, walk-5). Nothing checks that a folder exists, is writable, can take hardlinks from Downloads, or stays out of /data (system-10). One empty-field click on Save clears the TMDB key (system-13).

Deploying is risky:
- A failed `git pull` still prints '✓ Arrmada updated'.
- Every update prunes the previous image, so there is no way back.
- The Dashboard always says 'dev-docker' (system-7, system-2).
- An update quietly throws away a multi-night 4K encode (convert-7).

Accounts:
- One unconfirmed click deletes a person and their audiobook history, and nothing can disable an account even though the column exists (system-5).
- Emails are case-sensitive.
- Nobody can change their own password, and a locked-out owner has to edit SQLite by hand (system-6).

Configuration is spread over about eight pages that save in five different ways. The Plex connection, which sign-in depends on, sits inside Insights (system-9, frontend-10, frontend-12, product-8). The setup wizard stops before indexers and Plex, and admins land on Discover (product-9).

Other problems:
- The Logs page can't turn on debug and misstates its own retention (system-12).
- ARRMADA_BASE_URL is advertised but produces a blank app (backend-17).
- Several System strings are simply untrue (system-15).
- There's no single place that shows version, DB size, disk per library or the last backup (system-8).

**Depends on:** SAFE — store.Snapshot (VACUUM INTO) and the pre-migration snapshot (system.t2): [CFG-06](#cfg-06)'s `arrmada backup` uses them, and whichever epic lands first implements it once.; SAFE — `arrmada restore` (system.t4) and its image label: [CFG-06](#cfg-06)'s `--rollback --with-db`.; SAFE — the backup listing (system.t3): 'last backup' in [CFG-26](#cfg-26).; SAFE — the delete-user confirmation modal (system.t7): [CFG-10](#cfg-10) and [CFG-18](#cfg-18) wire its 'Disable instead' and 'Also block their Plex account' options.; SAFE — per-filesystem recycle bins: these retire the last LibraryDir use ([CFG-20](#cfg-20)).; SEC — Logs admin-only (system.t10) and audiobook-server request-log redaction (audiobooks-1): needed before [CFG-30](#cfg-30)'s debug switch and [CFG-31](#cfg-31)'s support bundle.; SEC — session last-seen/device columns and listing, sliding sessions and global 401 handling (system.t11): [CFG-12](#cfg-12)'s devices list.; SEC — route-role alignment (settings, apikeys and folder writes admin-only): decides the roles of [CFG-02](#cfg-02), [CFG-03](#cfg-03) and [CFG-04](#cfg-04)'s new routes.; SEC — websocket topic filtering: [CFG-25](#cfg-25)'s scan events are staff-only.; SEC — the manual-import path guard: should reuse libroots.UnderDataDir from [CFG-04](#cfg-04).; OBS — health registry (system.t20) and scheduler Tasks with Run now (system.t19): render in the Settings → System slots from [CFG-13](#cfg-13).; OBS — cached integration probes (product.t6) and indexer health (product.t5): [CFG-15](#cfg-15) reads these instead of probing itself once they exist.; INT — TMDB/OMDb/TVDB/OpenSubtitles key Test: used by [CFG-15](#cfg-15)'s cards and the wizard.; INT — Prowlarr sync that keeps scoping, and a real Torznab Test: [CFG-24](#cfg-24)'s Indexers step.; INT — download-client and indexer editors folded into Connections: later [CFG-15](#cfg-15) cards link to them.; PLEX — Insights → Plex/Alerts split, 'sign-in turns monitoring on' and owned-server-only discovery: [CFG-16](#cfg-16) and [CFG-23](#cfg-23) put the Plex UI in Settings, and the Alerts page may take over [CFG-17](#cfg-17)'s Notifications section.; FE — data router (createBrowserRouter, frontend.t15/t17) and the useBlocker unsaved guard (frontend.t18): optional upgrades for [CFG-13](#cfg-13) and [CFG-14](#cfg-14). The router must not add a basename, because [CFG-08](#cfg-08) removes ARRMADA_BASE_URL.; FE — the mocked-API Playwright harness (frontend.t11) for hub smoke tests, and theme persistence (system.t42) for the General section.; CONV — resumable segmented encode (convert.t25): changes [CFG-07](#cfg-07)'s warning text.; CONV, SUB, AUD — their settings UIs move into the hub in [CFG-17](#cfg-17); agree whether the move or the rebuild lands first.; REQ — per-user quotas (system.t14) as an optional People column ([CFG-18](#cfg-18)), and Plex auto-approve defaults ([CFG-23](#cfg-23) stores explicit choices).; COPY — the disk-guard '.env' note, the Music 'roadmap' hint and the 'Requests page' mentions. [CFG-13](#cfg-13), [CFG-16](#cfg-16) and [CFG-20](#cfg-20) move these strings, so coordinate to avoid conflicting edits.

#### Design

## Target shape

### 1. One Settings hub (`/settings/:section`)
A left rail at lg and up; below lg it becomes a select or horizontally scrolling pills. A search box filters a static index of `{label, keywords, section, anchor, adminOnly}`. It reuses the existing Section, Field and Toggle look (dark warm palette, terracotta accent, current type scale), with no restyle.

| Section | Contents | Who |
|---|---|---|
| General | Module toggles (each links to its module), Discovery region (moved out of API keys), About (version) | admin |
| Library | Library folders (with checks), movie/series naming, NFO/artwork, search on add, link to Quality profiles | manager+ (same as today's Media/Library tabs) |
| Downloads | Links to Download clients / Indexers pages (until INT folds the editors in), disk guard, recycle bin | admin |
| Connections | One card per integration with a status dot, last error and Test/Edit (Plex, download clients, indexers/Prowlarr, FlareSolverr, TMDB/OMDb/TVDB/Hardcover/OpenSubtitles keys, notifications, audiobook server). The Plex editor lives here. | admin |
| Notifications | Admin Apprise connections (moved from Insights; coordinate with the PLEX epic's Alerts page) | admin |
| Tools | Subtitles settings and Convert settings (moved from their module tabs) | manager+ |
| Audiobook server | The server tab from Audiobooks | admin |
| People & access | One people list (role, auto-approve, Plex link, last seen, sessions, audiobook access, Disable/Delete), Plex sign-in options, blocked Plex accounts | admin |
| Import & migrate | Overseerr, Tautulli, plus a link to the Audiobookshelf import | admin |
| System | Status (version, commit, DB size, last backup, disk per root, update available), Restart, Run setup again, Logs link. OBS adds Health and Tasks and SAFE adds Backups into slots here. | admin |

Module pages keep only their working views, plus a header 'Configure →' link. Old URLs redirect: `?tab=` on /settings, /insights?tab=settings|notifications, /subtitles?tab=settings, /convert?tab=settings, /audiobooks?tab=server, and /library.

Every role gets `/account`: change password, sign out other devices, and a link to the audiobook password. `/setup` reruns the wizard.

### 2. One save model
Each control saves itself.
- Toggles and selects commit on change.
- Text and number fields commit on blur or Enter when changed.
- A 'Saved ✓' tick shows inline, and a server 400 message shows under the field.

A shared `SettingsProvider` holds a single snapshot of GET /settings. `commit(key, value)` PUTs `{[key]: value}` only and merges the response back, so no section can clobber another (this was the cause of the region bug).

Exceptions that keep an explicit action:
- Credentials: Save is disabled while the field is blank, and Clear is a separate confirmed DELETE.
- Folder paths: a folder is committed when it is picked in the browser or when 'Use this folder' is pressed. The server validates it, and a warning chip may follow.

We chose this over frontend.t27's dirty-aware save bar because it matches the overhaul and removes the 'which button saves what' problem entirely. The PUT /settings contract stays partial and is round-trip tested.

### 3. Folders: validated, then live
- `internal/libroots` is the single resolver. It has `Movies/TV/Ebooks/Audiobooks/Music/Downloads(ctx)`; each reads `lib_*_dir`, and an empty value falls back to the env default. `CheckFolder(path, downloads, dataDir)` returns Exists, Writable, HardlinkWithDownloads (a real `os.Link` probe), UnderDataDir, Free/Total and Entries.
- PUT /system/library rejects the hard errors: a folder under the data dir or /data, a missing folder (unless `create:true`), or a path that isn't a directory. Warnings come from GET /system/library/check.
- The folder picker never starts at /data and greys out anything under DataDir.
- Phase 1 ([CFG-03](#cfg-03)) is GET /system/pending-restart plus a staff banner with Restart-and-wait. The wait polls until /api/v1/status `started_at` changes.
- Phase 2 ([CFG-19](#cfg-19)/20) makes the importer, coordinator SavePath and free-space check, disk guard, qBittorrent save path, health and fileinfo all read through libroots. Folder changes then apply live, and pending-restart reports nothing for folders.
- `cfg.LibraryDir` survives only as the env default. The one exception is the recycle default, until SAFE moves the bins onto each filesystem.

### 4. People & access
- `users`: a case-insensitive lookup index (exact match first, then lower(); fails closed on ambiguity). Emails are lowercased on create. A `password_set` flag lets Plex-only accounts show 'You sign in with Plex'.
- `auth.Service` gains SetDisabled (revokes web sessions and audio tokens), CheckPassword, ChangePassword (keeps the current session) and RevokeOtherSessions.
- Settings key `plex_login_blocked`, a JSON list of `{plex_id, name, at}`, is checked in Plex sign-in before FindOrCreatePlexUser.
- GET /api/v1/people joins users with sessions, audio tokens, listen sessions (times only), audio access and audio password state. It never returns a book, item or title. listen_log is untouched.

### 5. Deploy pipeline
`update.sh` flags:
- `--force-local`: build what's here when the pull fails. Otherwise a failed pull exits 1.
- `--rollback [--with-db]`
- `-y` or `ARRMADA_UPDATE_FORCE=1`
- `--no-backup`

Before each build:
- Tag the running `arrmada:dev` as `arrmada:previous` and write `.arrmada-previous`. `docker image prune` then only drops the image before that.
- Stamp the build: `ARRMADA_VERSION=$(git describe --tags --always --dirty)` and `ARRMADA_COMMIT=$(git rev-parse --short HEAD)` go to compose as build args.
- If the image carries `LABEL org.arrmada.cli=1`, run `docker exec -u PUID:PGID Arrmada-app arrmada backup --kind pre-update`. Old images without the label are never exec'd with arguments, because their main() ignores os.Args and would start a second server.
- If an encode has been running longer than 2h, ask first. update.sh reads the `busy` block of /api/health, which is returned only to loopback callers with no external or forwarded header, and never with titles.

The `arrmada` binary gains subcommands: `version`, `backup [--kind]` and `reset-password <email> [--password-stdin]`. SAFE adds `restore`. Run as root, the CLI drops to the owner of arrmada.db before touching anything.

CI:
- `shellcheck` on the scripts.
- `image.yml` builds the Docker image on every PR (no push).
- It publishes `ghcr.io/tristenlammi/arrmada:{sha-xxxx,main,<semver>,stable}` stamped with VERSION and COMMIT.

compose uses `image: ${ARRMADA_IMAGE:-arrmada:dev}`. With ARRMADA_IMAGE set, update.sh pulls instead of compiling. An in-app check (default on, says it contacts GitHub) shows 'Update available' with release notes. ARRMADA_BASE_URL is removed: the app is served at the root of its own hostname.

### 6. First run
Wizard steps: Metadata → Folders (with live checks) → Plex (PIN sign-in, owned-server picker, family sign-in and auto-approve toggles, both default off) → Indexers (Prowlarr sync, or one tracker with Test) → Finish.
- Finish renders the live checklist from GET /api/v1/setup/checklist as ✓/✗ links.
- 'Skip' appears only on step 1.
- Staff land on the Dashboard after login, where a 'Getting started' card ticks itself off until it is done or dismissed.

### 7. System, status and logs
GET /api/v1/system/status (admin) returns:
- version, commit, Go version, started_at and uptime
- data dir and DB bytes (db + wal + shm)
- the last backup's time and kind
- each resolved root's total, free and used %, deduplicated by filesystem
- the update-check result

The Dashboard System card shows `v… (abc1234)` and 'Last backup' instead of the constant 'Auth: enabled'.

Logs:
- A runtime level switch (debug for N minutes, auto-revert, back to the env level on restart). The request logger logs the route pattern, not the raw path.
- Cursor-based `after_seq` polling, and a `module` facet.
- Full-history download, including rotated files.
- A redacted support bundle: no keys, tokens or passwords, and no audiobook item paths.

## Data model changes
- Migration `users_username_lower` (next free number after 0089): `CREATE INDEX IF NOT EXISTS idx_users_username_lower ON users(lower(username))`. It is not unique, so existing case-duplicates still boot.
- Migration `users_password_set`: `ALTER TABLE users ADD COLUMN password_set INTEGER NOT NULL DEFAULT 1; UPDATE users SET password_set=0 WHERE plex_id IS NOT NULL AND plex_id<>''`.
- Settings keys: `plex_login_blocked`, `getting_started_dismissed` and `update_check_enabled`. No other schema changes. The listen tables are not touched.

## New or changed API (summary)
- **Settings and keys:**
  - GET /settings (read-only fields removed); PUT /settings (partial; 400 names the unknown field).
  - DELETE /apikeys/{id}; PUT /apikeys/{id} with an empty value → 400; key status gains `env_set` and `env_hint`.
- **Folders, restart and health:**
  - GET /system/pending-restart; GET /system/library/check; PUT /system/library (validated, `create`); GET /system/browse (`disabled` entries).
  - /api/health `busy` (loopback only).
- **Users and accounts:**
  - PUT /users/{id} `disabled`; POST /users/{id}/block-plex; GET /users/plex-blocks; DELETE /users/plex-blocks/{plexID}; DELETE /users/{id}?block_plex=1; POST /users/{id}/sessions/revoke; GET /people.
  - POST /me/password; POST /me/sessions/revoke-others.
- **Hub, setup and Plex:**
  - GET /system/connections; GET /setup/checklist; GET /setup adds plex_connected, indexers_enabled and prowlarr_available.
  - GET /insights/plex/servers.
- **Library scans:** GET /library/scans; ws `library.scan.progress`.
- **System and logs:**
  - GET /system/status.
  - GET and PUT /logs/level; GET /logs?after_seq=&module=; GET /logs/download?scope=all; GET /system/support-bundle.

## Invariants every task keeps
- Audiobook privacy: admin views show how much and when, never what. People, the support bundle and debug logs are tested for this.
- Never at /data: enforced by validation and the picker.
- Credentials are only entered in the UI. Nothing secret is hardcoded, committed, echoed back, or put in a support bundle.
- UI keeps the current visual style.
- Go changes run `go test -race` in Docker before pushing, and commits end with the Co-Authored-By trailer.

## Sequencing
1. M1 fixes what's broken (save, keys, restart honesty, folder checks).
2. M2 makes deploys reversible.
3. M3 covers accounts.
4. M4 moves everything into the hub: shell first as a pure move, then auto-save, then content moves.
5. M5 makes folders live.
6. M6 is the guided first run.
7. M7 covers status, releases and logs.

#### Milestone: M1 — Settings save again and folders tell the truth

_Every Settings tab saves. API keys can't be cleared by accident. Changing a folder shows a banner with Restart-and-wait. Folders are checked (exists, writable, hardlinks, never under /data) before they are stored._

<a id="cfg-01"></a>
- [x] **CFG-01 · Fix 'Save settings': drop read-only fields from GET /settings, send only changed keys, name unknown fields in 400s** — `P0` · `S` · Phase 0
  - **Problem:** Since 8b04078 (2026-08-17), handleGetSettings returns `server_time` and `server_tz` (internal/httpapi/settings.go:76-77). Settings.tsx:70 PUTs the whole `s` object back. handleUpdateSettings decodes through decodeJSONLimit, which calls DisallowUnknownFields (auth.go:246) and turns any decode error into a bare 400 'invalid request body'.

Effects:
- 'Save settings' on the Media, Library and System tabs has failed for 7 weeks: naming, NFO/artwork, module toggles, Plex sign-in, the disk guard and the recycle limits.
- Latent bug: the page's `s.tmdb_region` is fixed at load, so once Save works it would overwrite a region saved separately by APIKeysSection.saveRegion (Settings.tsx:467-479).
  - **Approach:** 1) internal/httpapi/settings.go handleGetSettings:
       - Remove the `server_time` and `server_tz` entries. Convert reads its clock from GET /convert/settings (api.ts:1004, Convert.tsx:926), and nothing in web/src reads them from /settings.
       - Delete `serverZone()`; settings.go:77 is its only caller.
    2) web/src/lib/api.ts: remove `server_time?` and `server_tz?` from AppSettings (~line 475).
    3) web/src/pages/Settings.tsx:
       - Keep a `loaded` snapshot next to `s`.
       - `save()` builds `diff = keys where s[k] !== loaded[k]` and PUTs only that.
       - An empty diff flashes 'Nothing to change'.
       - On success, set both `s` and `loaded` from the response.
       - APIKeysSection.saveRegion gets an `onSaved(region)` callback that patches both `s.tmdb_region` and `loaded.tmdb_region`, so the two views can't diverge.
    4) internal/httpapi/auth.go decodeJSONLimit: when the error text starts with `json: unknown field `, return 400 `invalid request body: unknown field "x"` so the next regression can be diagnosed from the UI. Other syntax errors keep the generic message.
    5) New internal/httpapi/settings_test.go with a `settingsAPI(t)` helper modelled on pathsAPI in library_paths_test.go (store.Open(t.TempDir()) + settings.NewService).
  - **Files:** `internal/httpapi/settings.go`, `internal/httpapi/auth.go`, `internal/httpapi/settings_test.go`, `internal/httpapi/decode_test.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Clicking 'Save settings' on the Media, Library and System tabs returns 200 and shows 'Saved ✓'
    - Turning on 'Allow Sign in with Plex' and changing the disk-guard pause % both survive a page reload
    - A region saved with the Discovery-region Save button is not overwritten by a later 'Save settings' on the same page load
    - GET /api/v1/settings contains neither server_time nor server_tz, and the Convert page still shows the server clock
    - PUT /settings with a bogus key returns 400 naming the key
  - **Tests:** Go TestSettingsGetPutRoundTrip: GET /settings, PUT the body back verbatim, expect 200 and identical values. This guards against any future read-only field.; Go TestSettingsPartialUpdate: PUT {"write_nfo":true} changes only write_nfo; Go TestDecodeUnknownFieldMessage (decode_test.go): the 400 message names the unknown field; UI check: change a naming template and the Plex sign-in toggle, save, reload
  - **Risk:** Low. Anyone adding a read-only field to GET /settings later must keep the round-trip test green, which is exactly what the test is for. CFG-14 later replaces this save path with per-key commits that use the same partial PUT.
  - **Resolves:** system-1
<a id="cfg-02"></a>
- [x] **CFG-02 · API keys: Save never clears a key; Clear becomes an explicit, confirmed DELETE that names the fallback** — `P1` · `S` · Phase 2
  - **Problem:** In APIKeysSection, saveKey sends `drafts[id] ?? ""` (Settings.tsx:484), and Save is disabled only while busy. apikeys.Store.Set treats an empty value as a clear (apikeys.go:117-124). So clicking Save on an empty field silently wipes the saved TMDB or OpenSubtitles key.

The wizard does the opposite: blank means keep (SetupWizard.tsx:52, 232).

Clear calls setDrafts('') and saveKey in the same tick (Settings.tsx:543), so saveKey reads the stale draft from its closure and would save a typed value instead of clearing.
  - **Approach:** 1) Backend:
       - internal/httpapi/apikeys.go handleSetAPIKey: a blank (after TrimSpace) value returns 400 'value is required — use Clear to remove a key'.
       - New handler handleClearAPIKey on DELETE /api/v1/apikeys/{id}, registered in server.go with the same role as PUT (Manager today; SEC may tighten it to Admin). It calls a new `apikeys.Store.Clear(ctx, id)`, which writes '' to the setting, and returns the fresh Status. An unknown id gives 404.
       - Clearing hardcover leaves books on their current keys; the confirm text says new lookups go back to Open Library.
    2) internal/apikeys/apikeys.go KeyStatus gains `EnvSet bool json:"env_set"` and `EnvHint string json:"env_hint,omitempty"`, computed from os.Getenv(k.EnvVar) even when a settings value wins, so the UI can name the fallback.
    3) web/src/lib/api.ts: add `clearAPIKey(id)` (DELETE).
    4) Settings.tsx APIKeysSection:
       - Save is disabled unless `drafts[id]?.trim()` is non-empty.
       - Clear opens a confirm. Examples: 'Clear the saved TMDB key? Discover, Movies and TV stop finding anything.', or when env_set, '…the key from your install (…1a2b) will be used instead.'
       - Clear then calls api.clearAPIKey(id) directly, not through drafts.
       - Keep the visual style.
    5) The wizard's 'leave blank to keep' behaviour stays as it is.
  - **Files:** `internal/httpapi/apikeys.go`, `internal/httpapi/apikeys_test.go`, `internal/httpapi/server.go`, `internal/apikeys/apikeys.go`, `internal/apikeys/apikeys_test.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Save is disabled on an empty or whitespace-only key field
    - Clear asks for confirmation and says what happens next, including the install-time fallback when one exists
    - PUT /apikeys/tmdb with an empty value returns 400; DELETE /apikeys/tmdb clears the key and the status falls back to source=env when the env var is set
    - Typing a value and then pressing Clear clears; it doesn't save the typed value
  - **Tests:** Go TestSetAPIKeyEmptyRejected; Go TestDeleteAPIKeyFallsBackToEnv (t.Setenv the env var, Clear, Status shows configured=true, source=env, env_set=true); Go TestStatusReportsEnvSetWhenSettingsWins; UI check: Save with an empty field does nothing; Clear then Cancel keeps the key
  - **Risk:** Low. Any external script that clears keys with an empty PUT would break; none is known. CFG-15 later moves this section into Settings → Connections unchanged.
  - **Resolves:** system-13
<a id="cfg-03"></a>
- [x] **CFG-03 · Saved folders that aren't in use yet: a persistent 'Restart to apply' banner with Restart-and-wait and a busy-aware confirm** — `P1` · `S` · Phase 2
  - **Problem:** Saved folders reach the importer, qBittorrent's save path, the coordinator's DownloadsDir and the disk guard only at startup:
- ApplySavedLibraryDirs runs once (setup.go:42-58; main.go:146).
- The roots are captured at construction (main.go:181, 207, 233-243, 261, 294, 318-319, 371).

LibraryFolders.save only flashes 'Saved' (Library.tsx:32), so new episodes keep importing into the old folder and the library splits. `restart_needed` and `can_restart` exist, but only the first-run wizard uses them (setup.go:168-193, SetupWizard.tsx:65-75).

Music is the exception: it resolves live (main.go:315-317), so a naive compare would falsely flag it. The wizard's restart wait also stops at the first OK /api/health, which the old process can still answer during its 500 ms shutdown sleep.
  - **Approach:** Backend:
    1) internal/httpapi/setup.go: extract `func (a *api) folderRestartState(ctx context.Context) restartState`.
       - restartState is {Needed, CanRestart bool; Changed []folderChange{Library, Saved, Running string}}.
       - It compares the saved value (Settings.Get(key, ""), trimmed) with the running value (Config.*Dir) for movies, tv, ebooks, audiobooks and downloads.
       - It skips music (live) and blank saved values (blank means the install default).
       - handleSetupState uses it for `restart_needed`; its JSON shape is unchanged.
    2) New `internal/httpapi/busy.go` with `func (a *api) busySummary() busy`, returning {ConvertRunning int, ConvertLongestSec int64, ConvertProgress float64, SubtitlesRunning int}.
       - Add `convert.Service.ActiveSummary() (n int, longestSec int64, progress float64)` in internal/convert/runner.go. It reads s.jobs under s.mu using activeState() and Job.StartedAt/Progress.
       - For subtitles, use the running count from the same source handleSubtitleJobs reads.
       - No titles anywhere.
    3) New route GET /api/v1/system/pending-restart, with the same role as PUT /system/library (Manager today). It returns {restart_needed, can_restart, changed, busy}. It is a separate endpoint, so /system/library's flat JSON (which LibraryFolders' dirty check iterates) doesn't change.
    
    Frontend:
    4) web/src/lib/restart.ts `restartAndWait()`:
       - Read /api/v1/status `started_at`, then POST /system/restart.
       - Poll /api/v1/status every 1.5 s for up to 120 s until started_at changes, then location.reload().
       - On timeout, show 'Arrmada hasn't come back — check docker compose logs arrmada-app'.
       - SetupWizard.restart switches to it.
    5) web/src/components/RestartBanner.tsx, rendered by AppLayout for staff:
       - It fetches pending-restart on mount and on a `arrmada:folders-saved` window event.
       - When needed it shows: 'New folders are saved but not in use yet: Downloads /media/downloads → /storage/torrents. Downloads and imports keep using the old folders until Arrmada restarts.'
       - An admin with can_restart gets [Restart now]. Its confirm lists the busy work, e.g. 'A conversion has been running 3h 12m (41%) and will start over.' and 'N subtitle jobs will be re-queued.'
       - Otherwise: 'Restart the Arrmada container the way you started it.'
       - Managers see the banner without the button.
    6) Library.tsx LibraryFolders.save: on success dispatch `arrmada:folders-saved`, re-fetch pending-restart, and flash 'Saved — restart to apply to downloads and imports' when needed. The copy also says files already imported stay where they are.
    7) Settings.tsx DiskGuardSection 'Currently watching': when downloads is in `changed`, append '→ <new path> after restart'.
    Keep the existing visual style: banner colours use --avoid and --panel-2.
  - **Files:** `internal/httpapi/setup.go`, `internal/httpapi/busy.go`, `internal/httpapi/server.go`, `internal/httpapi/setup_test.go`, `internal/convert/runner.go`, `web/src/lib/api.ts`, `web/src/lib/restart.ts`, `web/src/components/RestartBanner.tsx`, `web/src/components/AppLayout.tsx`, `web/src/pages/Library.tsx`, `web/src/pages/Settings.tsx`, `web/src/pages/SetupWizard.tsx`
  - **Acceptance:**
    - Changing the Downloads folder and saving shows a banner naming old → new; it survives a reload and appears on every staff page, Dashboard included
    - As admin in Docker, Restart now brings the app back (a new started_at), the banner disappears, and the disk guard shows the new path
    - The restart confirm names a running conversion's age and progress when one exists
    - A manager sees the banner without a Restart button
    - Changing only the Music folder, or saving the same folders again, shows no banner
  - **Tests:** Go TestFolderRestartState (table-driven): saved == running gives false; downloads differs gives true with one changed entry; music differs gives false; blank saved gives false; Go TestPendingRestartRoles: requester 403, manager 200; Go TestConvertActiveSummary (fake jobs map, longest StartedAt wins); UI check on a Docker install: change folder, see banner, Restart now, banner clears
  - **Risk:** A restart interrupts running Convert encodes (convert-7) and in-flight imports, which is why the confirm lists them. Once CFG-19/20 make folders live, folderRestartState returns nothing for folders, but the banner, endpoint and restart.ts remain for any future startup-only setting.
  - **Resolves:** system-4, walk-5
<a id="cfg-04"></a>
- [x] **CFG-04 · Validate library and download folders: exists, writable, hardlink-ready, free space, never under /data; picker never starts at /data** — `P1` · `M` · Phase 2
  - **Problem:** handleSetLibraryPaths (library_paths.go:55-84) stores any trimmed string, with no check for existence, writability or filesystem. The wizard's hardlink advice (SetupWizard.tsx:129-131) is never verified, even though a same-filesystem test exists in downloads.go:133-138.

handleBrowse lists '/data' among its start candidates (library_paths.go:93), and nothing stops a library from being placed under the DB directory. That breaks the standing rule that media must never be mounted at /data.
  - **Approach:** 1) New package `internal/libroots` with check.go:
       - `CheckFolder(path, downloads, dataDir string) FolderCheck`, where FolderCheck is {Path, Exists, IsDir, Writable bool; HardlinkWithDownloads *bool; UnderDataDir bool; FreeBytes, TotalBytes uint64; Entries int; EntriesCapped bool}.
       - Writable: an os.CreateTemp(path, ".arrmada-probe-*") probe, removed with a defer.
       - HardlinkWithDownloads is a real probe: create a temp file in `downloads`, os.Link it into `path`, then remove both on every path. A link error (EXDEV, EPERM, or Windows equivalents) gives false. An unwritable Downloads gives nil (unknown). It is skipped when path == downloads.
       - UnderDataDir: filepath.Clean, plus EvalSymlinks when the path exists, then filepath.Rel against cfg.DataDir and against the literal '/data'. Also refuse '/' itself.
       - Free/Total come from diskspace.Of.
       - Entries: f.ReadDir in chunks, counting top-level directories, capped at 10,000.
       - `UnderDataDir(path, dataDir) bool` is exported so SEC's manual-import path guard reuses this one implementation.
    2) PUT /system/library (handleSetLibraryPaths):
       - Add `Create bool json:"create"`.
       - Validate only folders that are provided and differ from the currently saved value, so an existing odd config never blocks unrelated saves.
       - Hard errors return 400 `{status:"error", message:"TV: /data/tv is inside Arrmada's database folder (/data). Pick a folder from your media mount.", folder:"tv"}`. The hard errors are: under the data dir; missing (unless create, which does MkdirAll 0o775 as the app user); not a directory.
       - '' stays allowed and means 'use the install default'.
       - The success response shape is unchanged.
    3) New GET /api/v1/system/library/check?path=&kind= (same role as PUT) returns the FolderCheck as JSON for live chips. kind=downloads skips the link probe.
    4) handleBrowse:
       - Start candidates become /storage, then /media, then '/'.
       - Each entry gets `disabled: true` when it is under DataDir.
       - The response gets `path_disabled` for the current path.
    5) UI:
       - Library.tsx LibraryFolders rows and the wizard's folders step show chips after a debounce: ✓ Exists, ✓ Writable, ✓ Hardlinks with Downloads or ⚠ 'Will copy, not hardlink', Free 3.2 TB, 412 folders.
       - A missing folder offers [Create it], which re-PUTs with create:true.
       - FolderPicker greys out disabled entries and disables 'Select this folder' when path_disabled.
       - The wizard blocks Next on hard errors.
       - Keep the existing chip and visual style.
  - **Files:** `internal/libroots/check.go`, `internal/libroots/check_test.go`, `internal/httpapi/library_paths.go`, `internal/httpapi/library_paths_test.go`, `internal/httpapi/server.go`, `web/src/pages/Library.tsx`, `web/src/pages/SetupWizard.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Choosing /data/movies, or any path under the data dir, is refused with an explanation naming the folder
    - A mistyped path is refused, or created only after the user clicks Create it
    - A Downloads folder on a different filesystem shows 'Will copy, not hardlink'
    - The picker never opens at /data and can't select a folder under it
    - Saving one folder never fails because a different, untouched folder is odd
  - **Tests:** Go TestCheckFolderUnderDataDirRejected (temp dataDir, child path); Go TestCheckFolderHardlinkProbe (two dirs in one temp dir give true, and no probe files are left behind); Go TestSetLibraryPathsRejectsMissing, and TestSetLibraryPathsCreate; Go TestBrowseMarksDataDirDisabled; Update TestLibraryPathsRoundTrip to use t.TempDir() paths, since it currently saves non-existent /storage/... paths that validation now rejects; Go race tests in Docker
  - **Risk:** The probe must clean up on every path and runs as the app user. On Unraid shfs, hardlinks across different user shares can fail, and reporting that truthfully is the point. It overlaps SEC's '/data guard' for manual import, so keep one implementation (libroots.UnderDataDir). CFG-19 extends the same package into the live resolver.
  - **Resolves:** system-10

#### Milestone: M2 — Deploys you can undo

_A bad pull fails loudly. Each update keeps the previous image and a pre-update DB snapshot, and `./update.sh --rollback` brings the old build back. The Dashboard shows the real version and commit. Updates warn before killing a long encode. The broken BASE_URL option is gone._

<a id="cfg-05"></a>
- [x] **CFG-05 · update.sh: fail loudly on a bad pull, keep the old image as :previous, add --rollback, stamp version and commit, shellcheck in CI** — `P1` · `S` · Phase 2
  - **Problem:** Three update.sh problems:
- When `git pull --ff-only` fails, update.sh warns, rebuilds the old code anyway, and still prints '✓ Arrmada updated' (update.sh:73, 112).
- It always tags arrmada:dev (compose line 10), then `docker image prune -f` (line 107) deletes the now-dangling previous build, so there is no way back.
- Compose hardcodes `VERSION: dev-docker` and never passes COMMIT (Dockerfile ARG COMMIT=unknown), so the Dashboard can't say what is running.
  - **Approach:** 1) Replace the single `${1:-}` check with an argument loop accepting `--pulled`, `--force-local`, `--rollback`, `--with-db` (wired by [CFG-06](#cfg-06)), `-y` and `--no-backup` ([CFG-06](#cfg-06)).
    2) Pull failure: print the git error and 'Fix the checkout (git status) or run ./update.sh --force-local to rebuild the code that's here', then exit 1. Never print the ✓ line on this path.
    3) Before building:
       - `git rev-parse --short HEAD > .arrmada-previous`. This is the code currently running, recorded before the pull. Add it to .gitignore.
       - `docker image inspect arrmada:dev >/dev/null 2>&1 && docker image tag arrmada:dev arrmada:previous`.
       - Keep `docker image prune -f` after a healthy start. It now drops only the build before :previous, so exactly one rollback image is kept.
    4) `./update.sh --rollback`:
       - Requires arrmada:previous.
       - Tag the current arrmada:dev as arrmada:rolled-back (so the owner can roll forward), retag previous → dev, then `docker compose up -d --no-build --no-deps arrmada-app` and wait_healthy.
       - Print the commit from /api/health.
       - Then print: 'If this update ran a database migration, also restore the pre-update backup (./update.sh --rollback --with-db, or System → Backups).'
    5) Version stamping: export `ARRMADA_VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev-docker)` and `ARRMADA_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)`. In docker-compose.yml, the build args become `VERSION: ${ARRMADA_VERSION:-dev-docker}` and `COMMIT: ${ARRMADA_COMMIT:-unknown}`.
    6) .github/workflows/ci.yml: add a `scripts` job running `shellcheck -s sh update.sh install.sh` (preinstalled on ubuntu-latest), and fix whatever it flags.
    7) README Update section:
       - Explain :previous, --rollback and --force-local.
       - Correct the 'untouched' promise.
       - If the owner deploys through Komodo, mirror the build args and :previous tagging there (documented, not scripted).
  - **Files:** `update.sh`, `docker-compose.yml`, `.github/workflows/ci.yml`, `.gitignore`, `README.md`
  - **Acceptance:**
    - A failing git pull makes update.sh exit non-zero, without the ✓ line
    - After an update, `docker images arrmada` shows both :dev and :previous
    - `./update.sh --rollback` brings the previous build back, and /api/health reports the previous commit
    - The Dashboard Version shows a git describe value, not 'dev-docker'
    - CI shellcheck passes
  - **Tests:** CI shellcheck step green; Manual on a scratch checkout: diverge the branch, run update.sh, expect exit 1; Manual: update, then --rollback, then check the health commit; then update again and confirm :previous moves; Go: no change; confirm buildinfo.Version/Commit appear in /api/health and /api/v1/status
  - **Risk:** Keeping :previous costs one extra image of disk, several GB with the whisper and oneAPI layers; say so in the README. Unattended runs must still work: no prompts are added here. If deploys actually go through Komodo, these protections apply only once mirrored there.
  - **Resolves:** system-7, system-2
<a id="cfg-06"></a>
- [x] **CFG-06 · arrmada CLI scaffold with `version` and `backup`; update.sh takes a pre-update snapshot; `--rollback --with-db`** — `P1` · `S` · Phase 2
  - **Problem:** main.go never reads os.Args, so scripts have no safe way to take a consistent DB snapshot from outside the app. update.sh rebuilds and migrates with no snapshot, and the whole household's history lives in one SQLite file.

There is also a trap: running `docker exec Arrmada-app arrmada <anything>` against today's image starts a second full server inside the container. Background jobs, MergeDuplicates and qBittorrent calls all run until the port bind fails.
  - **Approach:** 1) cmd/arrmada/cli.go: at the top of main(), `if len(os.Args) > 1 { os.Exit(runCLI(os.Args[1:])) }`, before the logger and server boot. An unknown subcommand prints usage and exits 2, so the server never starts with stray arguments again.
    2) Subcommands:
       - `version` prints buildinfo.Version, Commit and runtime.Version().
       - `backup [--kind manual|pre-update]` uses config.Load and a new `store.OpenNoMigrate(dataDir)` (same DSN and pragmas, never migrates). It calls `store.Snapshot(ctx, db, dest)`, a `VACUUM INTO` owned by SAFE; whichever epic lands first implements it, and there is one implementation. The destination is `DataDir/backups/arrmada-<kind>-<UTC yyyymmdd-hhmmss>-<version>.db`, the path is printed, and the newest 3 pre-update files are kept unless SAFE's prune covers it.
    3) Privilege handling: cmd/arrmada/cli_unix.go (build tag linux) runs when os.Geteuid()==0. It stats DataDir/arrmada.db and calls syscall.Setgid and then Setuid to the file's owner before opening anything. This prevents root-owned -wal/-shm or backup files that the app (PUID) can't touch. cli_other.go is a no-op for Windows dev.
    4) Dockerfile: in the final stage, `LABEL org.arrmada.cli="1"`. SAFE adds `org.arrmada.restore="1"` when it ships `arrmada restore`.
    5) update.sh, before the build:
       - If Arrmada-app is running and `docker inspect -f '{{index .Config.Labels "org.arrmada.cli"}}' Arrmada-app` is 1, run `docker exec -u "$PUID:$PGID" Arrmada-app arrmada backup --kind pre-update`, with PUID and PGID read from ARRMADA_PUID/ARRMADA_PGID in .env (default 1000). If it fails, abort with the error unless `--no-backup` is given.
       - If the label is missing (an older image), never exec with arguments. Print '! This build can't take a pre-update backup; continuing' and continue (SAFE's pre-migration snapshot in store.Open covers schema changes once it lands).
    6) `--rollback --with-db`: if the label org.arrmada.restore=1 is present on the rolled-back image, run `arrmada restore --stage <newest pre-update file>` and restart. Otherwise print the manual steps (stop, copy the file over arrmada.db, remove -wal/-shm, start).
    7) README: Backups/Update notes.
  - **Files:** `cmd/arrmada/main.go`, `cmd/arrmada/cli.go`, `cmd/arrmada/cli_unix.go`, `cmd/arrmada/cli_other.go`, `cmd/arrmada/cli_test.go`, `internal/store/store.go`, `Dockerfile`, `update.sh`, `README.md`
  - **Acceptance:**
    - `docker exec Arrmada-app arrmada version` prints version and commit and does not start a server
    - `arrmada backup` writes a consistent .db while the app keeps running, owned by PUID:PGID even when exec'd as root
    - After CFG-06 ships, an update leaves a pre-update backup in /data/backups
    - update.sh against an older image (no label) prints the warning and never runs `arrmada backup` in it
    - Running the server with no arguments is unchanged
  - **Tests:** Go TestCLIUnknownSubcommandExits2; Go TestCLIBackupWhileOpen (one *sql.DB holds the DB with a write in flight; runCLI backup produces a file that opens and has the row); Go TestOpenNoMigrateDoesNotMigrate (a fresh dir gives an error or empty schema_migrations, never applied migrations); Manual: update.sh on a scratch host for both the labelled and unlabelled image paths
  - **Depends on:** [CFG-05](#cfg-05), SAFE (store.Snapshot / VACUUM INTO and pre-migration snapshot, system.t2; `arrmada restore`, system.t4, for --with-db)
  - **Risk:** Anyone with docker exec can read the DB, which was already true. The setuid drop must happen before any file is opened. Test it in Docker as root with PUID 99. The label check is what keeps old images safe, so never call the CLI without it.
  - **Resolves:** system-2
<a id="cfg-07"></a>
- [x] **CFG-07 · update.sh warns before restarting during a long conversion (loopback-only busy info in /api/health)** — `P2` · `S` · Phase 2
  - **Problem:** update.sh restarts the container unconditionally. The owner deploys often, and each restart during a 4K x265 encode (~24 h per film at 3.8 fps, preset.go:717) throws away all the work done so far. runner.go:21-24 and cleanScratch (service.go:237-265) abandon in-flight work. CONV's resumable encode would cut the loss to one segment, but even then the current segment is lost.
  - **Approach:** 1) internal/httpapi/server.go handleHealth: when `isLocalCaller(r)` is true, add `busy: {convert_running, convert_running_sec, convert_progress, subtitles_running}` from a.busySummary() ([CFG-03](#cfg-03)).
       - isLocalCaller: the host from net.SplitHostPort(r.RemoteAddr) parses to a loopback IP, the request has no Config.ExternalHeader, and it has no X-Forwarded-For.
       - Never include titles. Non-local callers (including through a tunnel) get today's payload exactly.
    2) update.sh, before the build and after the [CFG-06](#cfg-06) backup:
       - Fetch the same JSON with `docker exec Arrmada-app curl -fsS http://127.0.0.1:7878/api/health`.
       - Extract convert_running_sec and convert_progress with sed (no jq dependency).
       - Above 7200 s, print 'A conversion has been running for 9h (62%). Updating now restarts it from the beginning.' Once CONV's resumable encode ships, the text becomes '…loses the current ~5-minute segment', keyed off a `convert_resumable` field it adds.
       - Prompt [y/N] only when stdin is a TTY (`[ -t 0 ]`).
       - `-y` or `ARRMADA_UPDATE_FORCE=1` skips the prompt. A non-TTY run prints the warning and proceeds, so unattended updates keep working.
    3) README: mention -y and the warning.
  - **Files:** `internal/httpapi/server.go`, `internal/httpapi/busy.go`, `internal/httpapi/health_test.go`, `update.sh`, `README.md`
  - **Acceptance:**
    - Running ./update.sh during a 3-hour-old encode shows the warning and waits for confirmation; N aborts with exit 0 and nothing rebuilt
    - ./update.sh -y proceeds without prompting
    - /api/health from a non-loopback address, or with the external header, contains no busy block
  - **Tests:** Go TestHealthBusyOnlyForLoopback (RemoteAddr 127.0.0.1:1234 and [::1]:1234 include busy; 172.17.0.1 and loopback-with-Cf-Connecting-Ip don't); Manual on a test container with a fake long-running job: the prompt path and the -y path
  - **Depends on:** [CFG-03](#cfg-03), [CFG-05](#cfg-05), CONV (resumable segmented encode, convert.t25) — only changes the warning text
  - **Risk:** Low. The non-interactive default must stay 'proceed', or unattended updates hang. cloudflared on the host network would reach the app through the Docker bridge, not loopback; the external-header check covers the rest.
  - **Resolves:** convert-7
<a id="cfg-08"></a>
- [x] **CFG-08 · Remove the half-built ARRMADA_BASE_URL option** — `P3` · `S` · Phase 2
  - **Problem:** The backend honours BaseURL: routes (server.go:105, 477-481), the cookie path (auth.go:226), pathAfterBase (external.go:143) and the book cover URLs (books.go:500). The SPA doesn't:
- About 124 hardcoded '/api/v1' paths.
- Vite builds with base '/'.
- useLive builds host + '/api/v1/ws'.
- BrowserRouter has no basename.

docker-compose.yml:40-41 advertises the option, so setting it gives a blank app. The owner serves Arrmada on its own hostname through Cloudflare.
  - **Approach:** Remove the option rather than finish it. A proper sub-path would also touch the PWA scope, the service worker, push and the separate audiobook port, for no current user.
    1) internal/config/config.go:
       - Drop the BaseURL field and normalizeBaseURL.
       - Add `BaseURLIgnored string`, set when ARRMADA_BASE_URL is non-empty.
       - cmd/arrmada/main.go logs an Error at boot: 'ARRMADA_BASE_URL is no longer supported — serve Arrmada at the root of its own hostname (it is running at the root)'.
    2) internal/httpapi:
       - server.go: remove the `base` prefixing and the StripPrefix branch.
       - auth.go: cookiePath() returns '/'.
       - external.go: remove pathAfterBase.
       - books.go:500: drop the prefix.
       - main.go: drop the orRoot and displayURL base use.
    3) docker-compose.yml: remove the ARRMADA_BASE_URL lines.
    4) install.sh:77 and update.sh:18/26 wait_healthy: drop `_base`; the health URL is the root.
    5) Release note in README.
  - **Files:** `internal/config/config.go`, `internal/config/config_test.go`, `internal/httpapi/server.go`, `internal/httpapi/auth.go`, `internal/httpapi/external.go`, `internal/httpapi/books.go`, `cmd/arrmada/main.go`, `docker-compose.yml`, `install.sh`, `update.sh`, `README.md`
  - **Acceptance:**
    - With ARRMADA_BASE_URL set, the app still loads at the root and the log explains that the option is unsupported
    - compose and the scripts no longer mention it
  - **Tests:** Go config test: env set → BaseURLIgnored non-empty; routes still served at the root; Go: existing externalgate_test still passes without pathAfterBase; Manual: run update.sh with a test .env containing ARRMADA_BASE_URL; the health check passes
  - **Depends on:** [CFG-05](#cfg-05), FE (the router migration must not add a basename for ARRMADA_BASE_URL)
  - **Risk:** Anyone actually relying on a sub-path proxy (none known) would need a hostname. Call it out in the release notes.
  - **Resolves:** backend-17, system-15

#### Milestone: M3 — Accounts you can manage safely

_Emails are case-insensitive. Accounts can be disabled, Plex identities blocked, and Read-only chosen at creation. A locked-out owner can reset a password with docker exec. Everyone can change their own password and sign out other devices._

<a id="cfg-09"></a>
- [x] **CFG-09 · Case-insensitive emails for sign-in and account creation** — `P1` · `S` · Phase 2
  - **Problem:** users.username is plain UNIQUE (0001_init.sql:14). Authenticate matches `WHERE username = ?` exactly (service.go:310-313), and handleCreateUser stores the email as typed. 'Mum@gmail.com' and 'mum@gmail.com' are therefore different accounts, and a family member whose email was entered with a capital letter can't sign in. Only UserByUsername (used by the audiobook server) falls back to lower().
  - **Approach:** 1) New migration, the next free number after 0089: `<NNNN>_users_username_lower.sql` with `CREATE INDEX IF NOT EXISTS idx_users_username_lower ON users(lower(username));`. It is not unique, so existing case-duplicates still boot.
    2) internal/auth/service.go:
       - CreateUser lowercases the username when it contains '@'; Plex-derived names keep their case. It returns ErrUserExists when `SELECT 1 FROM users WHERE lower(username)=lower(?)` matches.
       - Authenticate tries the exact match first. If that has no row, it tries `lower(username)=lower(?)`. With exactly one row it proceeds. With more than one it fails closed with ErrInvalidCredentials and logs a Warn naming the duplicate ids (never the password). The dummy bcrypt runs on the no-row and ambiguous paths, so timing is unchanged.
       - uniqueUsername compares with lower().
    3) internal/httpapi/users.go handleCreateUser: normalise on the server; the client keeps trimming only, so client and server can't diverge.
    4) At startup, log a one-time Warn when case-duplicates exist, so the owner can merge or delete one.
  - **Files:** `internal/store/migrations/0090_users_username_lower.sql`, `internal/auth/service.go`, `internal/auth/service_test.go`, `internal/httpapi/users.go`
  - **Acceptance:**
    - An account created as 'Mum@Gmail.com' is stored lowercased and signs in with 'mum@gmail.com' or 'MUM@gmail.com'
    - Creating 'MUM@gmail.com' when 'mum@gmail.com' exists returns 409
    - A legacy mixed-case account still signs in with its exact spelling
    - Login timing for unknown, disabled and ambiguous users still runs a full bcrypt
  - **Tests:** Go TestAuthenticateCaseInsensitive; Go TestCreateUserRejectsCaseDuplicate; Go TestAuthenticateAmbiguousCaseDuplicatesFailsClosed (insert two rows directly); Go TestCreateUserKeepsPlexNameCase
  - **Risk:** The migration number may clash with other epics; take the next free one at implementation time. The audiobook server's UserByUsername stays consistent with this.
  - **Resolves:** system-6
<a id="cfg-10"></a>
- [x] **CFG-10 · Disable accounts (revokes sessions and audio tokens), block a Plex identity, offer Read-only at creation** — `P1` · `S` · Phase 2
  - **Problem:** users.disabled exists, and Authenticate, ValidateSession, ValidateAPIKey and Plex sign-in all honour it, but no route sets it. handleUpdateUser accepts only role, auto_approve and password, and RevokeUserSessions has no caller.

Deleting a Plex user doesn't keep them out: FindOrCreatePlexUser just creates a new account on their next Plex sign-in. Delete cascades away their listen_progress and listen_history (0084).

Read-only can be chosen when editing a user but not when creating one (Settings.tsx:252-256 vs 296-301).
  - **Approach:** 1) internal/auth/service.go:
       - `SetDisabled(ctx, id int64, disabled bool) error`. On disable it runs `UPDATE users SET disabled=1`, RevokeUserSessions(id), and `UPDATE audio_tokens SET revoked=1 WHERE user_id=?`, in one tx where possible. Enabling only clears the flag. Nothing is deleted, so listening data survives a re-enable.
       - ListUsers and userWhere also return `PlexLinked bool json:"plex_linked"` (plex_id non-empty).
    2) internal/httpapi/users.go handleUpdateUser accepts `disabled *bool`. It refuses to disable yourself, or the last enabled admin (count role=admin AND disabled=0).
    3) Plex block list:
       - Settings key `plex_login_blocked` holds a JSON array of {plex_id, name, at}.
       - Helpers in a new internal/httpapi/plex_blocks.go: `plexBlocked(ctx, plexID) bool`, add and remove.
       - Routes (admin): GET /api/v1/users/plex-blocks; POST /api/v1/users/{id}/block-plex (blocks that user's plex_id); DELETE /api/v1/users/plex-blocks/{plexID}. `DELETE /users/{id}?block_plex=1` blocks, then deletes; SAFE's delete-confirmation modal uses this for its checkbox.
       - auth_plex.go handlePlexLoginPoll checks `plexBlocked` right after the account id is known and before FindOrCreatePlexUser, returning 403 'This Plex account isn't allowed to sign in here.'. Extract the decision as `plexSignInAllowed(ctx, plexID string, u *auth.User) (bool, string)` so it can be unit-tested.
       - The Overseerr importer skips creating accounts for blocked ids.
    4) UI (Settings.tsx UsersManager), keeping the existing style:
       - Rows show a 'Disabled' pill using the existing pill style.
       - EditUserModal gets a 'Can sign in' Toggle that calls updateUser({disabled}).
       - Plex-linked users get a 'Block their Plex account' action.
       - A 'Blocked Plex accounts' sub-list with Unblock.
       - The create form's role select adds Read-only.
  - **Files:** `internal/auth/service.go`, `internal/auth/service_test.go`, `internal/httpapi/users.go`, `internal/httpapi/users_test.go`, `internal/httpapi/plex_blocks.go`, `internal/httpapi/auth_plex.go`, `internal/httpapi/server.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Disabling a user signs them out of the web app and the audiobook apps on their next request
    - A disabled Plex user who taps 'Sign in with Plex' sees 'This account is disabled'
    - A deleted-and-blocked Plex user can't create a new account through Plex sign-in; Unblock restores that
    - You can't disable yourself or the last enabled admin
    - Read-only can be picked when adding a user
  - **Tests:** Go TestDisableUserRevokesSessionsAndAudioTokens; Go TestCannotDisableSelfOrLastAdmin; Go TestPlexBlockedIDRejected (unit-test plexSignInAllowed); Go TestDeleteWithBlockPlex; UI check: disable, then the user's next action lands on sign-in
  - **Depends on:** SAFE (delete-user confirmation modal, system.t7) — its 'Disable instead' button and 'Also block their Plex account' checkbox call these routes; not needed to ship
  - **Risk:** Disable must never delete anything, so listening data stays for a re-enable. A blocked id is stored without other personal data beyond the Plex display name the owner already saw.
  - **Resolves:** system-5, system-6
<a id="cfg-11"></a>
- [x] **CFG-11 · `arrmada reset-password` for a locked-out owner** — `P2` · `S` · Phase 2
  - **Problem:** There is no forgot-password flow and no CLI or env reset, because main.go never read os.Args. A forgotten sole-admin password can only be fixed by editing SQLite by hand.
  - **Approach:** 1) cmd/arrmada/cli.go (scaffold from [CFG-06](#cfg-06)) adds `reset-password <email> [--password-stdin]`.
       - It looks the user up with auth.Service.UserByUsername, which matches exactly and then case-insensitively.
       - Without --password-stdin it generates a random 16-character password (crypto/rand, an unambiguous alphabet) and prints it once.
       - It calls auth.SetPassword, which also revokes all web sessions, and sets password_set=1 once [CFG-12](#cfg-12)'s column exists.
       - If no account matches, it prints the admin usernames (no other data) and exits 1.
    2) It uses store.OpenNoMigrate and the root → DB-owner privilege drop from [CFG-06](#cfg-06).
    3) README 'Locked out?' section: `docker exec -it -u 99:100 Arrmada-app arrmada reset-password you@example.com`. Note that anyone with shell access to the container can do this, which is acceptable for a self-hosted single-owner app.
  - **Files:** `cmd/arrmada/cli.go`, `cmd/arrmada/cli_test.go`, `README.md`
  - **Acceptance:**
    - `docker exec Arrmada-app arrmada reset-password owner@x` prints a new password that works at the login page and signs out old sessions
    - --password-stdin sets the given password without echoing it
    - An unknown email lists the admin usernames and exits 1
  - **Tests:** Go TestCLIResetPassword (temp data dir: create user, runCLI, Authenticate with the new password, old session invalid); Go TestCLIResetPasswordUnknownEmail
  - **Depends on:** [CFG-06](#cfg-06), [CFG-09](#cfg-09)
  - **Risk:** Shell access means password reset; document it. The generated password appears in the terminal only, never in logs.
  - **Resolves:** system-6
<a id="cfg-12"></a>
- [ ] **CFG-12 · Account page for every role: change your own password, sign out other devices** — `P2` · `M` · Phase 7
  - **Problem:** There is no /me/password route. Requesters can't change their own password; their account menu offers only 'Audiobook password' (UserLayout.tsx:50). Nobody can sign out their other devices. An admin changing their own password in Edit user deletes every session, including the current one.
  - **Approach:** 1) New migration, next free number: `<NNNN>_users_password_set.sql` with `ALTER TABLE users ADD COLUMN password_set INTEGER NOT NULL DEFAULT 1; UPDATE users SET password_set=0 WHERE plex_id IS NOT NULL AND plex_id<>'';`. FindOrCreatePlexUser inserts 0, and SetPassword sets 1. Defaulting Plex-linked accounts to 'sign in with Plex' is the safe side of an inexact backfill.
    2) internal/auth/service.go:
       - `CheckPassword(ctx, id, pw) bool`, with a constant-time bcrypt compare.
       - `ChangePassword(ctx, id, newPw, keepRawToken string)`: update the hash, set password_set=1, `DELETE FROM sessions WHERE user_id=? AND token_hash<>hashToken(keep)`.
       - `RevokeOtherSessions(ctx, id, keepRawToken)`.
       - User gains `PasswordSet bool json:"password_set"`, so /auth/me carries it.
    3) New internal/httpapi/me_account.go:
       - POST /api/v1/me/password {current, new}. It is throttled via a.loginLimiter.allow("pw:<userID>"). A wrong current password gives 400 'Current password is wrong'. A weak one gives 400. When password_set=0 it returns 409 'This account signs in with Plex'.
       - POST /api/v1/me/sessions/revoke-others.
       - Both are under /api/v1/me/, which the external allowlist already permits.
    4) UI:
       - New web/src/pages/Account.tsx, routed at /account in all three route trees in App.tsx (external, requester, staff).
       - Sections: Password (current, new, confirm; or 'You sign in with Plex' when !password_set); 'Sign out everywhere else'; a link to the audiobook password card.
       - When SEC adds session last-seen and device columns plus a listing, add a 'Signed-in devices' list with per-row Sign out.
       - UserLayout menu gets 'Account' above 'Audiobook password'. The staff Sidebar footer gets an 'Account' link.
       - Keep the existing visual style and fit 375px.
  - **Files:** `internal/store/migrations/0091_users_password_set.sql`, `internal/auth/service.go`, `internal/auth/service_test.go`, `internal/httpapi/me_account.go`, `internal/httpapi/me_account_test.go`, `internal/httpapi/server.go`, `web/src/pages/Account.tsx`, `web/src/components/UserLayout.tsx`, `web/src/components/Sidebar.tsx`, `web/src/App.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A requester on a phone can open Account and change their password; other devices are signed out and this one isn't
    - A wrong current password returns an error, and repeated attempts are throttled
    - A Plex-only account sees no password form
    - 'Sign out everywhere else' keeps the current session
  - **Tests:** Go TestChangePasswordRequiresCurrent; Go TestChangePasswordKeepsCurrentSession; Go TestChangePasswordRefusedForPlexOnly; Go TestRevokeOtherSessions; UI check at 375px in UserLayout and AppLayout
  - **Depends on:** [CFG-09](#cfg-09), SEC (session last-seen and device columns plus listing, system.t11) for the devices list only
  - **Risk:** The password_set backfill can't be exact for old Plex accounts that an admin gave a password. Those users can ask the admin, and setting a password via Edit user flips the flag.
  - **Resolves:** system-6

#### Milestone: M4 — One Settings hub

_All configuration lives under /settings/:section with a left rail and search. Controls auto-save. Connections shows every integration with a live status dot. Plex, notifications, Subtitles, Convert and audiobook server settings move into the hub, and one People & access list replaces three._

<a id="cfg-13"></a>
- [ ] **CFG-13 · Settings hub shell: /settings/:section with a left rail and search; split Settings.tsx into section files (pure move)** — `P2` · `M` · Phase 3
  - **Problem:** Settings is four tabs (Media, Library, System, Users) with mixed contents in one 852-line Settings.tsx. Other configuration sits on about seven more pages.

Within Settings itself:
- Discovery region is inside API keys.
- The disk guard, a downloads concern, is under System.
- The Save bar renders mid-page above the Overseerr and Tautulli imports it doesn't affect (Settings.tsx:175-177).
- The tab is not in the URL.
  - **Approach:** 1) App.tsx (staff tree):
       - Route `/settings` element `<SettingsLayout/>`, with an index `<Navigate to="general" replace/>` (or 'library' for managers) and a `:section` child.
       - SettingsLayout reads `?tab=media|library|system|users` and redirects to /settings/library, /settings/library, /settings/general and /settings/people.
       - Keep `/library` → `/settings/library`.
       - This works with today's `<Routes>` and with FE's data router later.
    2) web/src/pages/settings/:
       - `SettingsLayout.tsx`: rail (sticky at lg+, a select below lg), search box, Outlet, and hash scroll to `#anchor` on load.
       - `sections.ts`: the registry `{id, label, adminOnly, component}` plus a static search index `{label, keywords, section, anchor}` covering every control.
       - Section files: `General.tsx` (module toggles, Discovery region moved here as its own card using the existing saveRegion logic), `LibrarySettings.tsx` (LibraryFolders, movie and series naming, metadata, search on add, a 'Quality profiles →' link), `Downloads.tsx` (links to /downloadclients and /indexers, DiskGuardSection, RecycleBin), `Connections.tsx` (APIKeysSection for now), `People.tsx` (UsersManager plus the Plex sign-in toggles), `ImportMigrate.tsx` (OverseerrImport, TautulliImport), `SystemSettings.tsx` (Restart button via restartAndWait when can_restart, Logs link). Status, Health, Tasks and Backups slots render only once those features exist, with no 'coming soon' copy.
       - Extract Section, Field, Toggle, Note, Preview and TokenList into `web/src/components/settings/ui.tsx` unchanged.
    3) Until [CFG-14](#cfg-14) lands, the [CFG-01](#cfg-01) diff-save model moves into a shared `SettingsProvider` (one snapshot plus loaded). Each section that edits /settings keys keeps its own Save button, with behaviour identical to today.
    4) Managers see exactly what they see today (Library only). Admin-only sections are hidden from managers and from the search index.
    5) nav.ts: 'Settings' stays in the System group pointing to /settings.
    6) Copy fixes made in passing:
       - The Library section's duplicated intro sentence.
       - '(Has its own Save folders button…)' stays until [CFG-14](#cfg-14).
  - **Files:** `web/src/App.tsx`, `web/src/lib/nav.ts`, `web/src/pages/Settings.tsx`, `web/src/pages/settings/SettingsLayout.tsx`, `web/src/pages/settings/sections.ts`, `web/src/pages/settings/General.tsx`, `web/src/pages/settings/LibrarySettings.tsx`, `web/src/pages/settings/Downloads.tsx`, `web/src/pages/settings/Connections.tsx`, `web/src/pages/settings/People.tsx`, `web/src/pages/settings/ImportMigrate.tsx`, `web/src/pages/settings/SystemSettings.tsx`, `web/src/components/settings/ui.tsx`, `web/src/lib/useSettings.tsx`
  - **Acceptance:**
    - Every control from the old Settings tabs is reachable in the hub within two clicks, with the same behaviour
    - Each section has a URL that survives refresh; /settings/downloads#recycle opens the recycle bin card
    - Searching 'recycle', 'plex' or 'naming' finds the control
    - A manager sees only the sections they can use today
    - No horizontal scroll at 375px, and the look matches the current palette and type scale
    - npm run build (tsc) passes
  - **Tests:** UI check: walk every old control to its new place, as admin and as manager; UI check at phone width; Playwright nav smoke over /settings/* once FE's mocked-API harness (frontend.t11) exists
  - **Depends on:** [CFG-01](#cfg-01), FE (data router, frontend.t15/t17) optional — the nested routes work on both
  - **Risk:** A large file move. Land it as a pure move with no behaviour change before CFG-14, to keep the diffs reviewable. Coordinate with SAFE (delete-user modal) and COPY, which edit the same components: rebase onto the moved files.
  - **Resolves:** system-9, frontend-10
<a id="cfg-14"></a>
- [ ] **CFG-14 · Auto-save each setting: toggles on change, text on blur, inline 'Saved' tick; remove the Save bars** — `P2` · `M` · Phase 9
  - **Problem:** Settings alone saves in five ways: a page Save bar, a separate 'Save folders' button, per-key Save, region Save, and immediate actions. Nothing marks dirty state or guards unsaved edits (only Quality.tsx has a beforeunload guard), and the copy has to explain which button covers which field (Settings.tsx:153, 434).

Decision: per-control auto-save, as the overhaul and system-9 recommend, rather than frontend.t27's sticky dirty bar. Credentials and folder paths keep an explicit, validated action.
  - **Approach:** 1) web/src/lib/useSettings.tsx (the provider from [CFG-13](#cfg-13)):
       - `useSetting<K>(key)` returns {value, setLocal, commit, status: idle|saving|saved|error, error}.
       - `commit()` PUTs `{[key]: value}` only and merges the response into the shared snapshot. That fixes cross-section clobbering for good.
    2) In web/src/components/settings/:
       - AutoToggle commits on change.
       - AutoText and AutoNumber commit on blur or Enter when changed; Esc reverts. Server 400 messages appear inline under the field.
       - SavedTick shows for 2 s.
       - All use the existing Toggle and Field styling.
    3) Specific controls:
       - The disk-guard pause/resume pair commits both keys together when either blurs, so the server's joint validation (settings.go:130-141) applies.
       - Naming templates commit on blur; the live preview is unchanged.
       - Recycle numbers commit on blur.
       - Module toggles call useMe's setBooksEnabled and setMusicEnabled on success.
       - Discovery region commits on blur.
    4) LibraryFolders (Library.tsx):
       - Per-row commit when a folder is picked in FolderPicker, or when 'Use this folder' or Enter is pressed on a typed path.
       - The commit goes through [CFG-04](#cfg-04) validation (a 400 shows inline on that row) and then the restart banner or live roots.
       - Remove 'Save folders', the dirty gate on Scan, and the apology copy.
    5) Remove SaveBar and the 'values save with the button below' copy everywhere.
    6) A beforeunload guard is active only while an AutoText is dirty and focused. When FE ships useBlocker, use it for in-app navigation too.
    7) Credentials ([CFG-02](#cfg-02)) keep explicit Save and Clear.
  - **Files:** `web/src/lib/useSettings.tsx`, `web/src/components/settings/AutoToggle.tsx`, `web/src/components/settings/AutoText.tsx`, `web/src/components/settings/ui.tsx`, `web/src/pages/settings/General.tsx`, `web/src/pages/settings/LibrarySettings.tsx`, `web/src/pages/settings/Downloads.tsx`, `web/src/pages/settings/People.tsx`, `web/src/pages/Library.tsx`, `internal/httpapi/settings_test.go`
  - **Acceptance:**
    - Turning Books off updates the nav immediately, without pressing any Save
    - An invalid pause % shows an inline error and isn't saved; a valid one shows 'Saved ✓'
    - Picking a new TV folder saves just that row (or shows its validation error) with no separate Save
    - No Save buttons remain in Settings except on credential fields
    - Edits survive a reload
  - **Tests:** Go TestUpdateSettingsSingleKey (table-driven over every key handleUpdateSettings accepts: each alone returns 200 and changes only that key); Go TestDiskGuardPairValidatedTogether; UI check of each control type, including blur-to-save, Esc revert and the error display
  - **Depends on:** [CFG-13](#cfg-13), [CFG-02](#cfg-02), [CFG-04](#cfg-04), FE (useBlocker unsaved guard, frontend.t18) optional
  - **Risk:** Many small PUTs, which is fine for a single-owner app. A folder auto-commit re-points imports (after CFG-19), so it commits only on an explicit pick or Enter, never on every keystroke or blur.
  - **Resolves:** system-9, system-1, frontend-12
<a id="cfg-15"></a>
- [ ] **CFG-15 · Settings → Connections: every integration as a card with a live status dot, last error and Test/Edit** — `P2` · `M` · Phase 9
  - **Problem:** Nothing shows at a glance what is and isn't connected:
- The Plex connection, which sign-in, recommendations, Insights and Convert's pause depend on, sits in Insights → Settings.
- Keys sit in Settings → System.
- Download clients and indexers have their own pages.
- FlareSolverr and Prowlarr are env-only.
- The Plex sign-in toggle says 'Requires your Plex server to be connected in Insights' but can't tell whether it is (Settings.tsx:168).
  - **Approach:** 1) New internal/httpapi/connections.go: GET /api/v1/system/connections (admin). It returns `[{key, name, configured, ok, detail, last_error, checked_at, link}]` from concurrent probes (errgroup), each with a 3 s timeout. Results are cached in memory for 60 s, and `?refresh=1` re-probes.
       - plex: insights Config has URL and token; reachable via the same call handleInsightsTest makes; detail is the server name.
       - download clients: one card each, from Downloads.List plus a per-client Queue reachability check.
       - indexers: enabled count, plus the failing count when INT or OBS expose indexer health.
       - prowlarr and flaresolverr: the configured URL plus a cheap GET reachability check.
       - metadata keys (tmdb, tvdb, omdb, hardcover, opensubtitles): configured and source from APIKeys.Status. Validity comes from the INT key tests where available (hardcover today).
       - notifications: connection count from Notify.
       - audiobook server: AudioManager running state and port.
       - Never returns a secret, only APIKeys hints.
    2) web/src/pages/settings/Connections.tsx: a card grid in the current style.
       - A status dot: --good, --avoid (configured but untested or degraded), --reject (failing), or --ink-faint (not set).
       - Name, detail, last error, and [Test] [Edit →] / [Set up →].
       - Download clients and indexers link to their pages until INT folds the editors in.
       - Metadata keys expand inline to the APIKeysSection row ([CFG-02](#cfg-02) behaviour) instead of a separate list.
    3) People section copy: 'Requires Plex to be connected' becomes a live line ('Plex: connected to <name>' or 'not connected → Set up') linking to /settings/connections#plex.
  - **Files:** `internal/httpapi/connections.go`, `internal/httpapi/connections_test.go`, `internal/httpapi/server.go`, `web/src/pages/settings/Connections.tsx`, `web/src/pages/settings/People.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Settings → Connections shows a card with a status dot for Plex, each download client, indexers, Prowlarr, FlareSolverr, each metadata key, notifications and the audiobook server
    - A broken integration's card is red, with its last error and a link to fix it
    - No response contains a key, token or password
    - Repeated page loads within 60 s don't re-probe
  - **Tests:** Go TestConnectionsPlexUnconfigured (configured=false); Go TestConnectionsDownloadClientError (ok=false with detail); Go TestConnectionsRoles (requester 403); Go TestConnectionsNoSecrets (seeded key values never appear in the body); UI check of each card's dot and link
  - **Depends on:** [CFG-13](#cfg-13), [CFG-02](#cfg-02), INT (key Test for TMDB/OMDb/TVDB/OpenSubtitles; indexer health and backoff state), OBS (cached integration probes, product.t6) — reuse when present instead of probing here
  - **Risk:** Probes must not hammer Plex or the clients; that's why results are cached for 60 s. Once OBS's health registry lands, this should read from it rather than run its own probes. Keep the probe functions small so they can move there.
  - **Resolves:** product-8, frontend-10, system-9
<a id="cfg-16"></a>
- [ ] **CFG-16 · Plex connection moves to Settings → Connections; one 'Import & migrate' section for all importers** — `P2` · `M` · Phase 9
  - **Problem:** The Plex editor (PlexSettings, Insights.tsx:923+) lives in an Insights tab, while Settings tells people to 'connect in Insights' and Insights says 'Connect your server in Settings'. The Overseerr and Tautulli importers sit in Settings → System, and the Audiobookshelf import sits in Audiobooks → Import.
  - **Approach:** 1) Move PlexSettings (with the PIN sign-in, auto-discovery and path mapping) out of Insights.tsx into a reusable `web/src/pages/settings/PlexConnection.tsx`.
       - It exports a `PlexConnect` component (sign in with Plex, server URL, test) that [CFG-23](#cfg-23)'s wizard step reuses.
       - Render it as the expanded Plex card at /settings/connections#plex.
       - It keeps calling the existing /insights/plex, /insights/plex/test and /insights/plex/auth routes. No backend change.
    2) Insights.tsx:
       - The 'settings' tab becomes a link card ('The Plex connection now lives in Settings → Connections') for one release, then is removed.
       - Every `onConfigure={() => setTab("settings")}` becomes navigate('/settings/connections#plex').
       - /insights?tab=settings redirects.
    3) ImportMigrate.tsx (from [CFG-13](#cfg-13)) adds an 'Audiobookshelf' card linking to the Audiobooks import (or hosting it, if it extracts cleanly).
       - The Overseerr copy's 'appear on the Requests page' becomes 'appear in Discover → Requests'. Coordinate with COPY.
    4) Coordinate with PLEX:
       - Their 'sign-in turns monitoring on' and owned-server-only discovery land inside PlexConnect, not in Insights.
       - Their Insights → Plex/Alerts split takes Notifications in [CFG-17](#cfg-17).
  - **Files:** `web/src/pages/settings/PlexConnection.tsx`, `web/src/pages/settings/Connections.tsx`, `web/src/pages/settings/ImportMigrate.tsx`, `web/src/pages/Insights.tsx`, `web/src/pages/Audiobooks.tsx`, `web/src/App.tsx`
  - **Acceptance:**
    - Plex can be connected, tested and signed in from Settings → Connections; PIN sign-in and auto-discovery still work
    - Insights no longer hosts the editor, and every 'configure' link there goes to the Plex card
    - All three importers are reachable from one Import & migrate section
    - Old /insights?tab=settings bookmarks land on the Plex card
  - **Tests:** UI check: full Plex connect, test and sign-in flow from the new place (test Plex account only); UI check: the Insights not-connected states link correctly; npm run build
  - **Depends on:** [CFG-15](#cfg-15), PLEX (Insights → Plex/Alerts split; sign-in turns monitoring on; owned-server discovery)
  - **Risk:** Moving the editor must keep insights StartPlexAuth/PollPlexAuth working. Refetch config via the API in both places; don't share component state.
  - **Resolves:** product-8, frontend-10
<a id="cfg-17"></a>
- [ ] **CFG-17 · Module settings into the hub: Notifications, Subtitles, Convert and Audiobook server sections, with 'Configure →' links and redirects** — `P2` · `M` · Phase 9
  - **Problem:** Subtitles, Convert, Insights (Notifications) and Audiobooks (Server) each carry their own settings tab. The owner has to remember which page holds each control, and the module pages mix working views with configuration.
  - **Approach:** 1) Lift each module's settings view into an exported component, unchanged, and render it in a hub section:
       - Insights NotificationsView and ConnCard (Insights.tsx:831-907) → settings/Notifications.tsx.
       - Subtitles SettingsTab → settings/SubtitlesSettings.tsx.
       - Convert's settings panel → settings/ConvertSettings.tsx.
       - Audiobooks' Server tab → settings/AudiobookServer.tsx.
       - The section registry gains Notifications, Tools (Subtitles plus Convert sub-anchors) and Audiobook server, with the same roles as today's routes.
    2) Module pages:
       - Drop those tabs and add a header 'Configure →' link to their section.
       - Overview widgets that read settings (e.g. the Subtitles Overview `settings` prop, the Convert overview) refetch via their GET endpoints instead of sharing component state.
    3) Redirects: /insights?tab=notifications → /settings/notifications; /subtitles?tab=settings → /settings/tools#subtitles; /convert?tab=settings → /settings/tools#convert; /audiobooks?tab=server → /settings/audiobook-server. Each module page reads useSearchParams on mount and `<Navigate>`s.
    4) OpenSubtitles credentials stay on Connections; the Subtitles section links there.
    5) Move code as-is. CONV, SUB, AUD and PLEX rebuild these UIs in their new home.
  - **Files:** `web/src/pages/Insights.tsx`, `web/src/pages/Subtitles.tsx`, `web/src/pages/Convert.tsx`, `web/src/pages/Audiobooks.tsx`, `web/src/pages/settings/Notifications.tsx`, `web/src/pages/settings/SubtitlesSettings.tsx`, `web/src/pages/settings/ConvertSettings.tsx`, `web/src/pages/settings/AudiobookServer.tsx`, `web/src/pages/settings/sections.ts`, `web/src/App.tsx`
  - **Acceptance:**
    - Insights shows only monitoring tabs
    - Subtitles, Convert and Audiobooks have no settings tab, and their Configure link opens the right section
    - Old bookmarks land on the new sections
    - No setting is lost: a Subtitles setting, a Convert setting and the audiobook server port changed in the hub are picked up by each module
  - **Tests:** Manual: change one setting per module from the hub and confirm the module reflects it; Playwright redirect checks once FE's harness exists; npm run build
  - **Depends on:** [CFG-13](#cfg-13), [CFG-16](#cfg-16), PLEX (Alerts page may take Notifications instead), CONV, SUB and AUD (their settings UIs move here; rebuilds happen in the new location)
  - **Risk:** Merge conflicts with CONV, SUB and AUD rebuilds of the same components. Agree the order: either this move lands first and the rebuilds edit the new files, or the move happens after their rebuilds.
  - **Resolves:** frontend-10, system-9
<a id="cfg-18"></a>
- [ ] **CFG-18 · People & access: one list with role, auto-approve, Plex link, last seen, sessions, audiobook access, Disable/Delete** — `P2` · `M` · Phase 9
  - **Problem:** Users live in Settings → Users, audiobook access in Audiobooks → People (Audiobooks.tsx:22-34), and the Plex link nowhere. To manage one person the owner visits three places, and no list shows last seen, active sessions or disabled state.
  - **Approach:** 1) New internal/httpapi/people.go: GET /api/v1/people (admin). One query plus the audio lookups returns, per user:
       - id, username, role, auto_approve, disabled, created_at, plex_linked
       - sessions: the count of `sessions` with expires_at > now
       - web_last: max(sessions.created_at), or last_seen_at once SEC adds it
       - audio_last: max(audio_tokens.last_used_at) where not revoked
       - listen_last: max(listen_sessions.last_at), time only
       - audio_allowed: not in AudioServer.DeniedUsers
       - audio_password_set: from the audio_passwords table
       - quota and usage, when REQ adds them
       It never selects item_key, book or title columns, and listen_log is untouched.
    2) Admin route POST /api/v1/users/{id}/sessions/revoke calls RevokeUserSessions ('Sign out everywhere').
    3) web/src/pages/settings/People.tsx replaces UsersManager. Each row has:
       - an inline role select and auto-approve toggle (auto-save, via PUT /users/{id})
       - a Plex badge
       - last seen (relative, the max of the three times)
       - a sessions count with 'Sign out everywhere'
       - an audiobook access toggle (PUT /audioserver/users/{id} {allowed}) and a password-set marker
       - a Disabled pill and Disable/Enable ([CFG-10](#cfg-10))
       - Delete, through SAFE's confirmation modal
       The add-user form includes Read-only. The Plex sign-in options (allow, auto-approve) sit at the top with the live Plex status line ([CFG-15](#cfg-15)), and blocked Plex accounts sit at the bottom. Rows collapse to a stacked layout at 375px, in the current style.
    4) Audiobooks → People keeps the listening-amount stats and replaces its access toggle with 'Manage access in Settings → People & access'.
  - **Files:** `internal/httpapi/people.go`, `internal/httpapi/people_test.go`, `internal/httpapi/server.go`, `internal/auth/service.go`, `web/src/pages/settings/People.tsx`, `web/src/pages/Audiobooks.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Each person appears once, with role, Plex link, last seen, sessions, audiobook access and Disable/Delete
    - Audiobook access can be switched on this page, and Audiobooks → People reflects it
    - The /people response contains no book, item or title identifiers
    - Works at phone width
  - **Tests:** Go TestPeopleListNoListeningContent (seed listen_progress, listen_history and listen_log rows with distinctive item keys; assert none appear in the body, and assert the JSON keys match an allowlist); Go TestPeopleLastSeenFromSessionsAndAudio; Go TestRevokeUserSessionsRoute (admin only); UI check at 375px
  - **Depends on:** [CFG-10](#cfg-10), [CFG-13](#cfg-13), [CFG-14](#cfg-14), SAFE (delete-user confirmation modal, system.t7), REQ (per-user quotas, system.t14) optional column
  - **Risk:** Audiobook privacy: show when and how much only, never what, and the privacy test enforces it. 'Last seen' is a time, not an activity description.
  - **Resolves:** system-9, system-5, system-6, product-8

#### Milestone: M5 — Folder changes apply live

_Imports, grabs, qBittorrent's save path, the disk guard and the health checks follow the saved folders with no restart. LibraryDir no longer quietly decides anything._

<a id="cfg-19"></a>
- [ ] **CFG-19 · Folder changes apply live: libroots resolver for importer, coordinator and disk guard** — `P2` · `M` · Phase 3
  - **Problem:** Library and download roots are copied from cfg at construction and never re-read:
- imports.SetRoots (main.go:294)
- bookImporter.SetRoots and SetBookRoots (318-319)
- movies.NewService(cfg.MoviesDir) and series.NewService(cfg.TVDir) (207, 181)
- the coordinator's downloadsDir (261), used as the qBittorrent SavePath on every grab (coordinator.go:154, 215) and for the free-space check (713)
- NewDiskGuard(cfg.DownloadsDir) (371)

Only music resolves live. The Importer also silently falls back to `im.root` (cfg.LibraryDir) whenever a per-type root is empty.
  - **Approach:** 1) internal/libroots/libroots.go (the package from [CFG-04](#cfg-04)):
       - `type Roots struct{ get func(ctx, key, def string) string; cfg config.Config }` with `Movies/TV/Ebooks/Audiobooks/Music/Downloads(ctx) string` and `All(ctx) []Root{Name, Path}`.
       - A saved '' or whitespace value falls back to the cfg default. Note that settings.Get returns a stored '' as-is.
       - Move the keyLib* constants here (KeyMovies = "lib_movies_dir" and so on); httpapi's libMovies() and friends delegate.
       - No cache: Settings.Get is one indexed row read and roots are resolved a few times per import or minute. That avoids invalidation races. Add a cache only if profiling shows a need.
    2) internal/library/importer.go and manager.go:
       - Replace SetRoots, SetBookRoots and SetMusicRootFunc with `SetRootFuncs(RootFuncs{Movie, TV, Ebook, Audiobook, Music func() string; BookScan func() []string})`.
       - movieDir(), tvDir(), ebookDir(), audiobookDir(), MusicDir() and the book scan roots call the funcs.
       - Drop the `root` (LibraryDir) fallback. An empty resolved root makes checkRoot fail loudly instead of writing into the managed volume.
       - NewImporter and NewManager stop taking LibraryDir.
    3) movies.Service and series.Service get `SetRootFunc(func() string)`, used when ScanLibrary has no override.
    4) automation.Coordinator: `New(..., downloadsDir func() string)`, used for AddRequest.SavePath and diskspace.FreeGB.
    5) download.NewDiskGuard(svc, set, log, dirFn func() string). Status().Path and Check read dirFn() each pass.
    6) cmd/arrmada/main.go builds one `roots := libroots.New(settingsSvc.Get, cfg)` and passes closures (`func() string { return roots.TV(context.Background()) }`). ApplySavedLibraryDirs is reduced to logging the resolved roots at startup.
    7) internal/httpapi/setup.go folderRestartState ([CFG-03](#cfg-03)) now returns Needed=false for all folders. Keep the endpoint and banner for future startup-only settings.
    8) LibraryFolders copy: 'Changing a folder applies to new imports right away. What's already imported stays where it is.' An import that is running finishes with the root it resolved at its start.
  - **Files:** `internal/libroots/libroots.go`, `internal/libroots/libroots_test.go`, `internal/library/importer.go`, `internal/library/manager.go`, `internal/movies/service.go`, `internal/series/service.go`, `internal/automation/coordinator.go`, `internal/download/diskguard.go`, `internal/httpapi/library_paths.go`, `internal/httpapi/setup.go`, `cmd/arrmada/main.go`, `web/src/pages/Library.tsx`
  - **Acceptance:**
    - Changing the TV folder in Settings makes the next imported episode land in the new folder, with no restart
    - Changing Downloads makes the next grab's SavePath and the disk guard's next check use the new folder
    - GET /system/pending-restart reports nothing after any folder change
    - An empty per-type root makes the import fail with a clear error instead of writing to LibraryDir
  - **Tests:** Go TestRootsFallbackOnEmptySaved; Go TestImporterUsesLiveRoots (change the setting between two MovieTarget calls); Go TestDiskGuardFollowsDownloadsSetting; Go TestCoordinatorSavePathLive (fake download service records AddRequest.SavePath); Go race tests in Docker (concurrent root reads during imports)
  - **Depends on:** [CFG-03](#cfg-03), [CFG-04](#cfg-04)
  - **Risk:** This touches many constructors in main.go, so do it in one focused session and keep the old setter names compiling until the call sites move. Removing the LibraryDir fallback could surface installs with an empty per-type root, and the clear error is the intended outcome. Files already imported stay in the old folder.
  - **Resolves:** system-4, system-3
<a id="cfg-20"></a>
- [ ] **CFG-20 · Downloads change re-points qBittorrent live; retire LibraryDir from health, the disk-guard 'same drive' check, fileinfo and startup** — `P2` · `S` · Phase 3
  - **Problem:** Several things still use the startup or legacy folders:
- qBittorrent's default save and incomplete paths are set only at boot (main.go:233-243).
- The health check probes Config.LibraryDir for writability (health_system.go:48). With a documented install that is the managed arrmada-media volume, not the user's Movies or TV folders.
- The disk guard's 'same drive as your library' comparison also uses LibraryDir (downloads.go:131-137), as do fileinfo's allowed roots (fileinfo.go:116) and the startup same-filesystem warning (startup.go:89-92).
  - **Approach:** 1) internal/httpapi/server.go Deps gains `OnFoldersChanged func(changed []string)`. handleSetLibraryPaths calls it, in a goroutine using a context detached from the request, whenever any saved folder actually changed.
    2) main.go supplies the hook. When 'downloads' changed and cfg.QbittorrentURL != "", it runs the same 20×3 s retry loop as boot, calling downloads.SetBundledSavePath(ctx, url, roots.Downloads()). It logs the result and publishes `system.folders.applied` for the UI.
    3) health_system.go: replace the single LibraryDir check with one per resolved root in use (movies, tv, plus ebooks, audiobooks and music when their modules are enabled, and downloads), deduplicated by path. Each unwritable folder becomes its own error naming the library.
    4) downloads.go handleDiskGuardStatus:
       - SharedWithLibrary compares downloads against each library root, using the existing total/free identity test.
       - Return `shared_with: ["movies","tv"]` and drop `library_path`.
       - Update the DiskGuardSection copy to match. Its '.env and re-run update.sh' note now points to the Downloads folder in Settings → Library (coordinate with COPY's disk-guard copy fix).
    5) fileinfo.go allowed roots: libroots.All() plus downloads plus the recycle dir.
    6) startup.go: log the resolved roots, and compare downloads with the movies and tv roots for the same-filesystem warning.
    7) Afterwards, cfg.LibraryDir is read only as the env default for the per-type dirs and for the recycle default (main.go:203). SAFE moves the bins onto each filesystem.
  - **Files:** `internal/httpapi/server.go`, `internal/httpapi/library_paths.go`, `internal/httpapi/health_system.go`, `internal/httpapi/downloads.go`, `internal/httpapi/fileinfo.go`, `cmd/arrmada/main.go`, `cmd/arrmada/startup.go`, `web/src/pages/settings/Downloads.tsx`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Changing Downloads re-points qBittorrent's save path within a minute, with no restart
    - The health panel reports an unwritable TV folder by name, and no longer reports on the managed library volume
    - The disk guard warns when Downloads shares a drive with Movies or TV
    - grep shows cfg.LibraryDir used only in config defaults and the recycle default
  - **Tests:** Go TestFoldersChangedHookCalledOnDownloadsChange (fake hook); Go TestHealthChecksEachRoot (temp dirs, one read-only); Go TestDiskGuardSharedWithLibraryRoots; Go race tests in Docker
  - **Depends on:** [CFG-19](#cfg-19), SAFE (per-filesystem recycle bins replace the LibraryDir recycle default)
  - **Risk:** A wrong qBittorrent save path would misplace downloads, so log the old and new values and keep the boot retry semantics. External (non-bundled) clients are untouched, as today.
  - **Resolves:** system-3, system-4

#### Milestone: M6 — Guided first run

_A fresh install goes Metadata → Folders → Plex → Indexers → Finish with live ✓/✗ links. Staff land on a Dashboard 'Getting started' card that ticks itself off. Setup can be run again. Library scans report progress and results._

<a id="cfg-21"></a>
- [ ] **CFG-21 · Getting started: a self-ticking checklist endpoint, a Dashboard card, and staff landing on the Dashboard after login** — `P2` · `S` · Phase 16
  - **Problem:** After sign-in, everyone, the admin included, is sent to /discover (Login.tsx:46). The first Dashboard visit offers only a red 'No indexers are enabled' line with no link (health_system.go:36). Getting from a fresh install to the first movie depends on reading text and hunting for pages.
  - **Approach:** 1) internal/httpapi/setup.go: GET /api/v1/setup/checklist (admin) returns `{items:[{key, done, label, link}], dismissed}`. Items:
       - tmdb: APIKeys.Value("tmdb") != "" → /settings/connections#tmdb
       - folders: librariesChosen → /settings/library
       - download_client: Downloads.List non-empty and Queue reachable, with a 3 s timeout → /downloadclients
       - indexer: any enabled indexer → /indexers
       - plex: the insights config has a token → /settings/connections#plex
       - first_title: movies plus series count > 0 → /discover
       - first_import: Library.Recent(ctx, 1) non-empty → /history
       Setting `getting_started_dismissed` is set by POST /api/v1/setup/checklist/dismiss.
    2) web/src/pages/Dashboard.tsx: a 'Getting started' card above everything while any item is not done and the card isn't dismissed. Each row is ✓ or ○ with a Link, and there's a Dismiss button. It refetches on focus and every 30 s while visible. Use the existing Card style.
    3) web/src/pages/Login.tsx: after password login and after Plex sign-in, read `user.role` from the response (startSession already writes {user}). Staff go to '/', everyone else to '/discover'. Setup mode goes to '/' (where SetupGate shows the wizard).
  - **Files:** `internal/httpapi/setup.go`, `internal/httpapi/setup_test.go`, `internal/httpapi/server.go`, `web/src/pages/Dashboard.tsx`, `web/src/pages/Login.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - An admin signing in lands on the Dashboard; a requester lands on Discover
    - On a fresh install the Dashboard shows the checklist with unticked items linking to the right pages, and items tick as they are completed
    - Once all items are done, or after Dismiss, the card stays gone
  - **Tests:** Go TestSetupChecklist (a seeded half-configured state: 0 indexers gives indexer=false; a TMDB key set gives true); Go TestSetupChecklistDismiss; UI check: admin and requester login destinations; UI check: the checklist updates after adding an indexer
  - **Risk:** Low. Links point to the hub paths; before CFG-13 lands, use /settings?tab=… with the redirects.
  - **Resolves:** product-9
<a id="cfg-22"></a>
- [ ] **CFG-22 · Wizard: Finish shows the live checklist with links, 'Skip' only on step 1, 'Run setup again' from System** — `P2` · `S` · Phase 16
  - **Problem:** The wizard's Finish 'Next:' list is bold text with no links and never mentions Plex (SetupWizard.tsx:182-189). 'Skip setup' sits on every step and is permanent, and nothing can reopen the wizard.
  - **Approach:** 1) SetupWizard.tsx Finish step:
       - Render the [CFG-21](#cfg-21) checklist items as ✓/✗ rows with react-router Links.
       - Keep the restart path (now via restartAndWait) for the case where pending-restart is still needed before [CFG-19](#cfg-19).
    2) Nav: 'Skip' renders only on the first step, labelled 'Skip for now — you can run it again from Settings → System'. Later steps get Back and Next (with 'Skip this step' where a step is optional).
    3) Run setup again:
       - A staff route `/setup` (admin), outside AppLayout, renders `<SetupWizard onDone={() => navigate('/settings/system')} />`.
       - The wizard already pre-fills current keys and folders from GET /setup, so no backend reset is needed. Drop the drafted POST /setup/reset; setup_complete stays true.
       - A 'Run setup again' button in Settings → System.
    4) SetupGate is unchanged for first run.
  - **Files:** `web/src/pages/SetupWizard.tsx`, `web/src/App.tsx`, `web/src/pages/settings/SystemSettings.tsx`, `web/src/lib/restart.ts`
  - **Acceptance:**
    - Finish shows live ✓/✗ items with working links
    - 'Skip' appears only on the first step
    - 'Run setup again' reopens the wizard with current values pre-filled, and finishing returns to Settings → System
  - **Tests:** UI walkthrough on a scratch data dir in Docker: fresh install, skip, then run setup again; npm run build
  - **Depends on:** [CFG-21](#cfg-21), [CFG-04](#cfg-04), [CFG-13](#cfg-13)
  - **Risk:** Low. Keep Skip on step 1 so a headless restore or scripted install isn't blocked.
  - **Resolves:** system-10, product-9
<a id="cfg-23"></a>
- [ ] **CFG-23 · Wizard Plex step: sign in with Plex, pick your own server, family sign-in toggles** — `P2` · `M` · Phase 16
  - **Problem:** A fresh install ends with no Plex connection, though sign-in, recommendations, Insights and Convert's pause all depend on it. The wizard covers only keys and folders, and server discovery can pick a friend's server.
  - **Approach:** 1) internal/plex/account.go: `OwnedServers(ctx, clientID, token) ([]Server{Name, MachineID, URIs []string, Local bool}, error)`, built from the plex.tv resources call that serverIDs(ownedOnly=true) already makes, keeping the connection URIs.
    2) internal/httpapi/insights.go: GET /api/v1/insights/plex/servers (manager+) lists the owned servers for the saved token. Choosing one is PUT /insights/plex {url} (existing), followed by POST /insights/plex/test.
    3) internal/httpapi/setup.go: GET /setup adds `plex_connected`.
    4) SetupWizard.tsx: a new 'plex' step between folders and finish, reusing `PlexConnect` from [CFG-16](#cfg-16).
       - [Sign in with Plex]: the existing POST /insights/plex/auth plus a poll.
       - If more than one owned server is found, a picker.
       - Test, and show the result.
       - Two toggles, both default OFF in the wizard and saved explicitly via PUT /settings: 'Let family sign in with Plex' (plex_login_enabled) and 'Auto-approve their requests' (plex_login_auto_approve).
       - Skippable. When the PIN flow fails (no internet from the server), show 'Couldn't reach plex.tv from the server — you can connect later in Settings → Connections' and allow Next.
    5) Coordinate with PLEX: monitoring switches on at sign-in, per their quick win.
  - **Files:** `internal/plex/account.go`, `internal/plex/account_test.go`, `internal/httpapi/insights.go`, `internal/httpapi/server.go`, `internal/httpapi/setup.go`, `internal/httpapi/setup_test.go`, `web/src/pages/SetupWizard.tsx`, `web/src/pages/settings/PlexConnection.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A fresh install walks Metadata → Folders → Plex → Finish
    - With Plex connected in the wizard, Insights is connected and, if chosen, Plex sign-in is enabled for family
    - An account with two owned servers gets a picker; a shared (friend's) server is never offered
    - Without internet, the step explains and can be skipped
  - **Tests:** Go TestOwnedServersParsing (recorded plex.tv resources fixture with one owned and one shared server); Go TestSetupStatePlexConnected; UI walkthrough in Docker with a test Plex account (test values only)
  - **Depends on:** [CFG-22](#cfg-22), [CFG-16](#cfg-16), PLEX (owned-server-only discovery; monitoring on at sign-in)
  - **Risk:** The PIN flow needs outbound internet. Never log the Plex token. The family toggles default to off in the wizard regardless of the stored default (REQ owns changing that default).
  - **Resolves:** system-10, product-9
<a id="cfg-24"></a>
- [ ] **CFG-24 · Wizard Indexers step: one-click Prowlarr sync, or add one tracker and test it** — `P2` · `M` · Phase 16
  - **Problem:** The wizard ends one step short of the first grab: no indexer is configured. The Dashboard then shows 'No indexers are enabled' with no link.
  - **Approach:** 1) Extract the add/edit form from Indexers.tsx into `web/src/components/IndexerForm.tsx`, so both places use one form. Indexers.tsx keeps using it with no behaviour change.
    2) internal/httpapi/setup.go: GET /setup adds `indexers_enabled` (count) and `prowlarr_available`. The latter comes from the same reachability check handleProwlarrInfo uses, with a 3 s timeout.
    3) SetupWizard.tsx: a new 'indexers' step after plex.
       - If Prowlarr is available: [Sync from Prowlarr] (POST /indexers/prowlarr/sync), then 'Added N indexers'.
       - Otherwise, IndexerForm for one tracker (Torznab name, URL and key, or a built-in kind) with Test.
       - Shows 'N indexers enabled ✓' when some already exist.
       - Skippable, and Back keeps entries.
    4) The step order becomes Metadata → Folders → Plex → Indexers → Finish.
  - **Files:** `web/src/components/IndexerForm.tsx`, `web/src/pages/Indexers.tsx`, `web/src/pages/SetupWizard.tsx`, `internal/httpapi/setup.go`, `internal/httpapi/setup_test.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - On a fresh install, an admin can sync from Prowlarr, or add and test one tracker, without leaving the wizard
    - The Indexers page behaves exactly as before
    - The Finish checklist shows the indexer item ticked afterwards
  - **Tests:** Go TestSetupStateIndexerFields (seeded: 0 enabled, then 1 enabled); UI walkthrough: the Prowlarr path and the manual path on a scratch install with test values only
  - **Depends on:** [CFG-22](#cfg-22), INT (Prowlarr sync that keeps per-indexer scoping; a Torznab Test that checks a real caps/search response)
  - **Risk:** Until INT's fixes land, a Torznab Test can pass on any HTTP 200 and a Prowlarr re-sync re-enables disabled indexers. Show the INT-provided result as-is and don't over-promise in the copy.
  - **Resolves:** product-9
<a id="cfg-25"></a>
- [ ] **CFG-25 · Library scans report progress and results instead of reloading after a fixed 5 seconds** — `P3` · `S` · Phase 16
  - **Problem:** In Settings → Library, a scan flashes a message and reloads the review list after a fixed 5 s (Library.tsx:41), with no progress or result count. Overlapping movie or series scans aren't prevented, unlike musicScan.
  - **Approach:** 1) movies.Service.ScanLibrary and series.Service.ScanLibrary take an optional `progress func(done, total int)`. The handlers (handleScanLibrary, handleScanSeriesLibrary) publish `library.scan.progress` {media, done, total} on the bus at most every 25 folders, and `library.scanned` {media, imported, skipped, unmatched} at the end.
    2) The api struct gets `movieScan` and `seriesScan` atomic.Bool fields. A concurrent scan returns 409 'A Movies scan is already running'.
    3) An in-memory last-scan record per media is exposed at GET /api/v1/library/scans (manager+) as {media, running, started_at, finished_at, done, total, imported, unmatched}.
    4) Library.tsx LibraryFolders: subscribe via useLive and show 'Scanning Movies… 120/412', then 'Done: 37 added, 3 need review'. Reload the review list on `library.scanned`, and remove the setTimeout.
    5) Books and music scans can adopt the same events later.
  - **Files:** `internal/movies/service.go`, `internal/series/service.go`, `internal/httpapi/movies.go`, `internal/httpapi/series.go`, `internal/httpapi/library_scans.go`, `internal/httpapi/server.go`, `web/src/pages/Library.tsx`
  - **Acceptance:**
    - A scan shows live progress and a final result count
    - Clicking Scan twice gives 'already scanning'
    - The review list refreshes when the scan finishes, not after 5 s
  - **Tests:** Go TestScanPublishesProgressAndResult (fake bus subscriber); Go TestConcurrentScanRejected; Go race tests in Docker
  - **Depends on:** SEC (websocket topic filtering by role — scan events are staff-only)
  - **Risk:** Low. The events carry folder counts only, no paths.
  - **Resolves:** system-10

#### Milestone: M7 — Know what's running: status, releases, logs

_System → Status shows version, commit, DB size, last backup, disk per library and 'update available'. CI builds and publishes the image so update.sh can pull instead of compiling. Logs gain a timed debug switch, incremental live mode, full download and a redacted support bundle._

<a id="cfg-26"></a>
- [ ] **CFG-26 · System → Status: real version and commit, DB size, last backup, disk use per library; fix the Dashboard System card** — `P2` · `S` · Phase 9
  - **Problem:** The Dashboard's System card shows Version 'dev-docker' and a constant 'Auth: enabled' (Dashboard.tsx:228-231; server.go:561). Nowhere shows the DB size, the last backup or disk use per library.
  - **Approach:** 1) New internal/httpapi/system_status.go: GET /api/v1/system/status (admin) returns:
       - version, commit, go (runtime.Version()), started_at, uptime_seconds
       - data_dir and db_bytes (sizes of arrmada.db, -wal and -shm)
       - last_backup {at, kind, file} from SAFE's backup listing helper, or until then the newest file in DataDir/backups
       - roots [{name, path, total, free, used_pct, shared_with}], via diskspace.Of over libroots.All() plus downloads and the data dir, deduplicated by the existing total/free identity test
       - update (filled in by [CFG-29](#cfg-29))
    2) A Status card at the top of Settings → System, in the current style, with simple bars per filesystem.
    3) Dashboard System card:
       - Version shows `v1.2.0 (abc1234)`.
       - Replace 'Auth: enabled' with 'Last backup' ('2h ago' or 'never', in avoid tone when older than 48 h).
       - Keep Uptime, Database and Realtime.
       - /status keeps `auth_enabled` for API compatibility, but the UI stops showing it.
  - **Files:** `internal/httpapi/system_status.go`, `internal/httpapi/system_status_test.go`, `internal/httpapi/server.go`, `web/src/pages/settings/SystemSettings.tsx`, `web/src/pages/Dashboard.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - After an update.sh build, the Dashboard shows the git version and commit
    - Status shows the DB size and the free space of each library drive, with shared drives grouped
    - The 'Auth' stat is gone
  - **Tests:** Go TestSystemStatusReportsDBSizeAndRoots (temp dirs, fake DB file sizes); Go TestSystemStatusAdminOnly; UI check of the Dashboard card
  - **Depends on:** [CFG-05](#cfg-05), [CFG-13](#cfg-13), SAFE (backup listing for 'last backup', system.t3)
  - **Risk:** Low. Paths are shown to admins only.
  - **Resolves:** system-8, system-7, system-15
<a id="cfg-27"></a>
- [x] **CFG-27 · CI builds the Docker image on every PR and push (no publish yet)** — `P2` · `S` · Phase 2
  - **Problem:** CI never runs `docker build` (.github/workflows/ci.yml), so a broken apt repo, whisper tag or oneAPI base first fails on the Unraid server during an update.
  - **Approach:** 1) New .github/workflows/image.yml, triggered on pull_request and push to main.
       - A free-disk step first (e.g. jlumbroso/free-disk-space).
       - docker/setup-buildx-action, then docker/build-push-action with `push: false`, `cache-from/to: type=gha,mode=max`, and build args VERSION=${{ github.ref_name }}-${{ github.sha }} and COMMIT=${{ github.sha }}.
       - concurrency cancel-in-progress, like ci.yml.
    2) If the oneAPI and whisper stages exceed runner disk or time, split them into a base image built by a separate workflow only when WHISPER_VERSION or the oneAPI tag changes. That is docker/whisper-base.Dockerfile → ghcr.io/tristenlammi/arrmada-whisper:<version>, referenced by the main Dockerfile via ARG.
    3) Check that .dockerignore excludes .git, web/node_modules and data/ to keep the context small.
  - **Files:** `.github/workflows/image.yml`, `.dockerignore`, `Dockerfile`
  - **Acceptance:**
    - A PR that breaks the Dockerfile fails CI
    - A green run caches layers, so a second run is much faster
  - **Tests:** CI run green on a PR and on main; Deliberately break a RUN line on a scratch branch: CI goes red
  - **Risk:** GitHub-hosted runners have about 14 GB free and the oneAPI image is very large, so the base-image split may be needed from the start. Each red push emails the owner, so land this on a branch first.
  - **Resolves:** system-7
<a id="cfg-28"></a>
- [ ] **CFG-28 · Publish the image to GHCR with version and commit; update.sh can pull a tag instead of compiling** — `P2` · `M` · Phase 16
  - **Problem:** Every update compiles two whisper.cpp builds and the oneAPI stage on the Unraid server from main HEAD. There are no releases and no tags to pin or roll back to.
  - **Approach:** 1) image.yml (from [CFG-27](#cfg-27)):
       - On push to main and on v* tags, log in to ghcr.io with GITHUB_TOKEN (permissions packages: write) and push.
       - Tags: `sha-<short>` and `main`; on a v* tag also `<semver>` and `stable`.
       - Build args: VERSION is the tag name, or `main-<short>` on main; COMMIT is the short sha.
       - Add OCI labels (source, revision).
    2) docker-compose.yml: the arrmada-app service gets `image: ${ARRMADA_IMAGE:-arrmada:dev}`, and the build section stays for local builds.
    3) update.sh pull mode, when ARRMADA_IMAGE is set in .env:
       - Tag the running image as arrmada:previous (`docker image tag "$(docker inspect -f '{{.Image}}' Arrmada-app)" arrmada:previous`).
       - Take the [CFG-06](#cfg-06) backup, then `docker compose pull arrmada-app` and `docker compose up -d --no-build --no-deps arrmada-app`.
       - `--rollback` in pull mode runs `ARRMADA_IMAGE=arrmada:previous docker compose up -d --no-build --no-deps arrmada-app` and says that the next ./update.sh returns to the pinned tag.
       - Build mode is unchanged when ARRMADA_IMAGE is unset.
    4) README 'Releases' section:
       - Tag vX.Y.Z and CI publishes it with GitHub-generated release notes.
       - How to switch to `ARRMADA_IMAGE=ghcr.io/tristenlammi/arrmada:stable`.
       - The GHCR package must be made public by the owner in GitHub's UI, or the server logs in once with its own token. Never commit credentials.
  - **Files:** `.github/workflows/image.yml`, `docker-compose.yml`, `update.sh`, `README.md`
  - **Acceptance:**
    - A push to main publishes ghcr.io/tristenlammi/arrmada:sha-<short> stamped with VERSION and COMMIT
    - With ARRMADA_IMAGE set, update.sh pulls in minutes instead of compiling, and the Dashboard shows the tag
    - --rollback works in both build and pull modes
  - **Tests:** CI run publishes on main (check the package page); Manual: update.sh in pull mode on a scratch host, then --rollback; CI shellcheck still green
  - **Depends on:** [CFG-27](#cfg-27), [CFG-05](#cfg-05), [CFG-06](#cfg-06)
  - **Risk:** Image size and storage on GHCR. The package visibility is an owner action, and no token goes in the repo. If deploys go through Komodo, point Komodo at the GHCR tag instead of building.
  - **Resolves:** system-7
<a id="cfg-29"></a>
- [ ] **CFG-29 · 'Update available' with release notes in System → Status, and an Unraid template** — `P3` · `S` · Phase 16
  - **Problem:** The owner can't tell whether an update exists or what changed. There is no Unraid Community Apps template for app-only installs.
  - **Approach:** 1) New internal/updatecheck package: `Checker{client, version, commit, enabled func() bool}` with `Run(ctx)` registered as scheduler task 'update-check' (12 h, run at start).
       - For semver builds: GET https://api.github.com/repos/tristenlammi/arrmada/releases/latest with If-None-Match/ETag, then compare semver.
       - For main-<sha> builds: GET /compare/<commit>...main to read `behind_by`.
       - The result {latest, url, notes (markdown truncated to 4 KB), behind, checked_at} is cached in memory.
       - Setting `update_check_enabled` defaults to true. The toggle in Settings → System says 'Checks GitHub every 12 hours'. No other data is sent beyond a User-Agent of `Arrmada/<version>`.
    2) /system/status ([CFG-26](#cfg-26)) gains `update`. The Status card shows 'Update available: v1.3.0 — what changed' with collapsible notes and 'Run ./update.sh', or 'Up to date' or 'N commits behind main'.
    3) templates/arrmada.xml: an Unraid CA template for the app container only, assuming an external qBittorrent.
       - Uses the GHCR :stable image, PUID 99 and PGID 100, and maps /data to /mnt/user/appdata/arrmada.
       - Media goes to /storage, never /data. The web port and the audiobook port are included.
       - It documents that the companions (qBittorrent, FlareSolverr) come from compose or other templates.
  - **Files:** `internal/updatecheck/updatecheck.go`, `internal/updatecheck/updatecheck_test.go`, `cmd/arrmada/main.go`, `internal/httpapi/system_status.go`, `web/src/pages/settings/SystemSettings.tsx`, `templates/arrmada.xml`, `README.md`
  - **Acceptance:**
    - A running build older than the latest release shows 'Update available' with notes
    - With the check disabled, no GitHub calls are made
    - The Unraid template installs with media outside /data
  - **Tests:** Go TestUpdateCheckComparesSemver (httptest fake GitHub); Go TestUpdateCheckBehindMain (compare endpoint fixture); Go TestUpdateCheckDisabled (no request made); Manual: template install on a test Unraid share
  - **Depends on:** [CFG-26](#cfg-26), [CFG-28](#cfg-28)
  - **Risk:** GitHub's unauthenticated limit (60/h) is fine at 12 h intervals. The check makes an outbound call, which is why it is visible and has a toggle.
  - **Resolves:** system-7
<a id="cfg-30"></a>
- [ ] **CFG-30 · Logs: a runtime debug switch that turns itself off; request logs show the route pattern, not the raw path; honest footer** — `P3` · `S` · Phase 16
  - **Problem:** compose hardcodes `ARRMADA_LOG_LEVEL: "info"` (docker-compose.yml:35), and applog.Handler.Enabled defers to that base level, so the Logs page's Debug filter shows nothing on a standard install. The footer says 'kept in memory, up to 5000' (Logs.tsx:154), while the ring holds 50,000 (main.go:687) and more is on disk.

Turning debug on naively would also log `r.URL.Path` for every request (middleware.go:40). Paths such as /api/v1/books/{id}/audiobook reveal which book someone fetched, which breaks the audiobook-privacy rule.
  - **Approach:** 1) cmd/arrmada/main.go newLogger: use a package-level `slog.LevelVar` as HandlerOptions.Level, initialised from cfg.LogLevel. Wrap it in a small `applog.LevelSwitch{var *slog.LevelVar; envLevel slog.Level; until time.Time; timer *time.Timer; mu}` with Set(level, d) and Reset(), passed to httpapi via `Deps.LogLevel`.
    2) internal/httpapi/middleware.go logRequests: log `r.Pattern` (set by ServeMux on the same request in Go 1.22+, e.g. 'GET /api/v1/books/{id}/audiobook') instead of r.URL.Path. Fall back to the first two path segments when Pattern is empty (static and SPA routes). Never log query strings.
    3) Routes GET and PUT /api/v1/logs/level {level, minutes}, admin-only.
       - PUT sets the level and schedules a revert to the env level with time.AfterFunc: default 30 min, maximum 240.
       - GET returns {level, env_level, until}. A restart returns to the env level.
    4) Logs.tsx:
       - A 'Debug for 30 min' button with a countdown and 'Back to normal'.
       - Footer: 'Showing the newest N matching lines of the last 50,000 kept in memory. Older lines are in /data/logs on disk. Same stream as the container logs.'
       - Keep the current style.
  - **Files:** `cmd/arrmada/main.go`, `internal/applog/level.go`, `internal/applog/level_test.go`, `internal/httpapi/middleware.go`, `internal/httpapi/logs.go`, `internal/httpapi/logs_test.go`, `internal/httpapi/server.go`, `web/src/pages/Logs.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Turning on debug from the Logs page shows debug lines immediately
    - It returns to info after the chosen time, or on restart
    - Debug request lines show route patterns such as /api/v1/books/{id}/audiobook, never concrete ids
    - The footer numbers match reality
  - **Tests:** Go TestLogLevelSwitchAndRevert (short duration); Go TestDebugLinesCapturedWhenEnabled; Go TestRequestLogUsesPattern (a request to /api/v1/books/42/audiobook logs the pattern; '42' is absent); Go race tests in Docker (LevelSwitch concurrent Set and read)
  - **Depends on:** SEC (Logs admin-only, system.t10; audiobook-server request-log redaction, audiobooks-1)
  - **Risk:** Audiobook privacy: the audiobook server has its own request logger (audioserver/server.go:582-599), and SEC must strip item ids there at every level before this switch ships.
  - **Resolves:** system-12
<a id="cfg-31"></a>
- [ ] **CFG-31 · Logs: incremental live fetch, module filter, full-history download, and a redacted support bundle** — `P3` · `M` · Phase 16
  - **Problem:** In Live mode the Logs page re-fetches a 2,000-line snapshot every 3 s (Logs.tsx:55-65), which is hundreds of KB through the tunnel. Download exports only what is on screen. Older on-disk lines (about 4×24 MB of JSONL, persist.go:23-27) can't be reached from the app. There is no per-module filter; Convert and Subtitles keep separate log endpoints.
  - **Approach:** 1) internal/applog/applog.go:
       - Entry gains a monotonic `Seq uint64`, assigned in Ring.add under the lock. Re-seeded entries get fresh sequence numbers at Restore.
       - Filter gains `AfterSeq` and `Module`. GET /api/v1/logs accepts `after_seq` and `module`.
       - Logs.tsx Live mode sends after_seq, appends, and trims the client buffer to 5,000.
    2) Module facet: in main.go, pass `log.With("module", "convert")` (and subtitles, indexer, import, download, audioserver, insights, books, music) into each service constructor. applog stores the module attribute in Entry.Module. A module select goes in Logs.tsx.
    3) Admin GET /api/v1/logs/download?scope=all streams a zip (archive/zip, written straight to the response) of arrmada.log.jsonl plus its rotated .1–.3 files.
    4) Admin GET /api/v1/system/support-bundle builds a zip of:
       - the log files
       - /system/status ([CFG-26](#cfg-26)) and /health/system JSON, and version
       - settings run through an allowlist-minded redactor: drop keys matching `apikey:*`, `*token*`, `*password*`, `*secret*`, `insights_plex_*`, `*apprise*`, `*url*` with credentials, and any key not on a known-safe list
       Log lines in the bundle go through applog redaction plus a pass that rewrites audiobook-server item paths and ids to `{id}`. Never the DB.
  - **Files:** `internal/applog/applog.go`, `internal/applog/persist.go`, `internal/applog/applog_test.go`, `internal/httpapi/logs.go`, `internal/httpapi/support.go`, `internal/httpapi/support_test.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`, `web/src/pages/Logs.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - In Live mode each poll returns only new lines (a few KB)
    - 'Download full log' includes the rotated files
    - The support bundle contains no API keys, tokens or passwords, and no audiobook titles or ids
    - Filtering by module works
  - **Tests:** Go TestLogsAfterSeqReturnsOnlyNewer; Go TestLogsModuleFilter; Go TestLogsDownloadIncludesRotated; Go TestSupportBundleRedactsSecrets (seeded settings with fake keys; none appear in the zip); Go TestSupportBundleStripsAudiobookPaths; Go race tests in Docker
  - **Depends on:** [CFG-30](#cfg-30), [CFG-26](#cfg-26), SEC (Logs admin-only, system.t10)
  - **Risk:** Redaction must be allowlist-minded: when unsure, drop the setting from the bundle. The bundle is generated for the owner to share, so treat it as public.
  - **Resolves:** system-12

#### Risks

- The Settings hub split (CFG-13) is a large file move that collides with other epics editing Settings.tsx: SAFE's delete modal, COPY's string fixes, SEC role changes. Land it early in M4 as a pure move with no behaviour change, and have others rebase onto the new section files.
- Auto-save (CFG-14) is a deliberate choice over frontend.t27's dirty-aware save bar. If the owner prefers the save-bar model, CFG-14 is the only task to swap; the shared SettingsProvider and partial PUT support either.
- Live roots (CFG-19/20) touch many constructors in main.go and change import destinations at runtime. Do them in one focused session, run the race tests in Docker, and keep in-flight imports on the root they started with. Removing the LibraryDir fallback can surface installs with an empty per-type root; the clear error is intended.
- Calling the CLI inside an old image starts a second server, because today's main() ignores os.Args. update.sh must gate every `docker exec … arrmada <cmd>` on the org.arrmada.cli image label (CFG-06).
- update.sh changes must not break unattended updates. Prompts appear only on a TTY, and -y or ARRMADA_UPDATE_FORCE skip them. If the owner deploys through Komodo, the :previous tagging, version args and snapshot have to be mirrored there.
- Keeping arrmada:previous costs one extra multi-GB image. GitHub runners may not fit the oneAPI and whisper stages without the base-image split (CFG-27).
- Migration numbers (two in this epic, after 0089) may clash with other epics; take the next free number at implementation time.
- Audiobook privacy is at risk in three places: the People list, debug request logs and the support bundle. Each has a dedicated test, and CFG-30 waits for SEC's audiobook-server log redaction.
- Folder validation must not lock out existing installs with unusual paths. Only folders that are being changed are validated, and '' keeps meaning 'install default'.
- CFG-15's Connections probes could hammer Plex or the clients if uncached. Results are cached for 60 s, with a 3 s timeout per probe, and should move to OBS's registry when it exists.

#### Out of scope

- The backup engine itself: scheduled daily VACUUM INTO, retention, the Backups page, restore upload and staging (SAFE). This epic only calls it from the CLI and update.sh and shows 'last backup'.
- Route-role alignment, sliding sessions, global 401 redirect and login-throttle fixes (SEC).
- The health-check registry, scheduler Tasks with Run now, admin health alerts, and Dashboard warnings as links (OBS). This epic provides the System section slots.
- Indexer and download-client editors, PUT /downloadclients/{id}, FlareSolverr configuration in the UI, and key validity tests (INT).
- Insights/Alerts split, Plex monitoring behaviour, partial scans and Watch-on-Plex links (PLEX).
- Sub-path (ARRMADA_BASE_URL) support: removed, not implemented.
- Request quotas, auto-approve policy and defaults (REQ).
- Resumable convert encodes and the time forecast (CONV); convert-7 is addressed here only as the update.sh warning.
- Recycle-bin relocation to each filesystem and the purge rules (SAFE).
- Design-system restyle, theme persistence, nav regrouping and the requester app shell (FE, APP). Hub pages keep the current visual style.
- Rewriting module settings UIs (Convert 2.0, Subtitles orchestration, audiobook server): CFG-17 moves them as-is.

