# PLEX — Plex & Insights

_Part of the [Arrmada roadmap](../../ROADMAP.md). 24 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Make Plex a working two-way integration and make Insights numbers right. Arrmada should tell Plex exactly which folder changed after every import, upgrade, rename, delete and Convert swap. It should link every in-library title to app.plex.tv, and sign-in should work on iPhones, pick only the owner's own server, turn monitoring on and link existing accounts. Insights should record plays once, without duplicates, across restarts and Tautulli imports, and become a navigable, phone-friendly Tautulli replacement (People, History, Library, Graphs). Plex setup should live on one settings page, and Alerts should move to its own page.

**Why.** The owner runs Plex for playback and has family members request titles, yet Arrmada only ever reads from Plex.

- **Plex never hears about changes.** Nothing calls /library/sections/{id}/refresh (insights-5, product-1, backend-14, movies-9, convert-11). A requester can get "ready to watch" before Plex has the title. Renames and Convert swaps leave Plex pointing at files that no longer exist until its next scheduled scan, and its watcher is unreliable on Unraid FUSE shares.
- **Nothing links to Plex** (discover-5, product-1). Discover stops at "In your library". The movie page shows only IMDb and TMDB links, and doesn't say who has watched the title.
- **Insights numbers are wrong in ways the owner can't see.**
  - A Tautulli import double-counts every period that was also recorded live, because duplicates only match on an exact start second (insights-2).
  - The History "Watched" column ignores the watched_ms fix (insights-1).
  - Every ./update.sh restart splits each stream in progress into two plays and re-sends "Now playing" (insights-4).
  - The HW badge shows "HW" even when Plex quietly fell back to the CPU on the Arc GPU (insights-9).
  - Imports run blind, with no progress or summary (insights-13).
- **Setup is fragile.**
  - Sign in with Plex opens its popup only after an await, so iPhone Safari blocks it (insights-8, system-11).
  - Discovery can pick a friend's shared server, and the URL it finds is never tested (insights-15).
  - Monitoring stays off after sign-in while the badge says "connected" (insights-3).
  - The owner tapping Sign in with Plex gets a second, requester-only account, and local accounts can't link Plex for personalised rows (insights-14).
  - Plex settings are split across three screens (insights-13).
- **Insights is hard to use.** It is a Tautulli table copy with no user pages, posters, filters or drill-downs (insights-10). It scrolls sideways on a phone (insights-12) and shows "Coming soon" on finished tabs (insights-11).
- **Naming can't produce Plex-native folder names.** The {tmdb-…} folder ids Plex recommends can't be produced, and a template using {edition-{edition}} leaves a stray "{edition-}" when the release has no edition (system-14).

**Depends on:** ACQ — import outbox (backend.t16): [PLEX-05](#plex-05)'s movie and series import hooks should move onto it when it lands. Until then the direct hooks replace the lossy bus.; MOV — movie.deleted event and delete flow (movies.t18) may reshape handleDeleteMovie, which [PLEX-05](#plex-05) touches. The editions work (movies-2) keeps the {edition} token consistent with [PLEX-13](#plex-13).; CONV — Convert's finalizeOutput and swaps reconcile get a LibraryChanged callback ([PLEX-05](#plex-05)). A convert.done event (convert.t16) is not required.; REQ — consumes [PLEX-10](#plex-10) Locate/WatchURL and [PLEX-04](#plex-04) OnRefreshed for 'ready only once Plex has it' and the push and inbox 'Watch on Plex' deep links. [PLEX-11](#plex-11) covers only the Discover sheet, Requests cards and detail pages.; CFG — the Settings hub's Connections section is where [PLEX-17](#plex-17)'s Plex page mounts if it exists. Otherwise [PLEX-17](#plex-17) adds a Plex tab under Settings.; FE — the createBrowserRouter overhaul: [PLEX-18](#plex-18)'s /insights/:tab and /alerts routes become nested routes there if it lands first.; APP — a Me/Account tab will host [PLEX-14](#plex-14)'s 'Link Plex account' row. Until then it lives in the Sidebar footer and the UserLayout menu.; OBS — owns the Alerts page content (insights-6, insights-7: event catalog, presets, delivery queue). [PLEX-18](#plex-18) only moves the tab to /alerts. The OBS health registry can consume [PLEX-08](#plex-08)'s insights.Status().; SAFE — DB backups are recommended before the destructive repairs: [PLEX-02](#plex-02) overlap removal, [PLEX-16](#plex-16) import undo, [PLEX-15](#plex-15) account merge.; SEC — coordinate [PLEX-15](#plex-15)'s plex_signin_staff policy and owner auto-link with the deny-by-default auth work. A future CSP img-src rule would require proxying [PLEX-21](#plex-21)'s plex.tv avatars.

#### Design

## Target architecture

### 1. `internal/plex`: a pure Plex/plex.tv API client, read and write
- **Write calls:**
  - `RefreshPath(ctx, sectionKey, dir)` sends GET `/library/sections/{key}/refresh?path=<QueryEscape(dir)>`.
  - `RefreshSection(ctx, key)`.
  - A shared `do(ctx, method, path)` that checks the status code without decoding the body.
- **Library listing:**
  - `Library.Locations []string`, parsed from `Directory[].Location[].path`.
  - `SectionItems(ctx, key, typ)`, paged at 500 with `includeGuids=1`. It returns ratingKey, title, year, addedAt and the parsed external ids. It handles new-agent `Guid[].id` values (`tmdb://`, `tvdb://`, `imdb://`) and legacy primary guids (`com.plexapp.agents.themoviedb|imdb|thetvdb://…?lang=`).
- **Session fields:** hardware fields `transcodeHwDecoding`, `transcodeHwEncoding`, `transcodeHwFullPipeline` and their titles, plus `grandparentThumb`, `grandparentRatingKey` and `art`.
- **plex.tv:**
  - `plexTVBase` becomes a package `var` so tests can point it at httptest.
  - `DiscoverServers` returns owned servers only, with their connections.
  - `AuthURL(..., forwardURL)` sets `forwardUrl`.
- **Fixtures:** recorded from the owner's PMS with read-only GETs and kept in `internal/plex/testdata/`.

### 2. `internal/plexscan` (new): one scan engine, called directly
- **Entry point:** `Scanner.Request(kind "movie"|"show", dir)`, which never blocks. Callers:
  - the coordinator movie and series import hooks
  - the movie and series rename and delete handlers
  - the Convert swap callback
- **No event bus.** It is not driven by the event bus, which drops events when a subscriber's buffer is full. When ACQ's import outbox lands, the import hooks move onto it.
- **Path resolution,** in order, for Arrmada's container path → Plex's path:
  1. The path already sits under a section Location: use it as is.
  2. The longest `from` prefix in the `plex_path_map` setting (JSON `[{from,to}]`, edited in the UI).
  3. Auto-guess: the Arrmada root's last segment equals a Location's last segment.
  4. Fall back to refreshing every section of that kind, and log once per root.
- **Debouncing and limits:**
  - 15 s debounce per target path.
  - A path whose ancestor is already pending is dropped.
  - At most one refresh per path per 60 s, from a single worker.
  - The section list is cached for 10 minutes.
- **Outputs:**
  - Status for the UI: last_scan_at, last path, how it resolved, last error.
  - `OnRefreshed` callbacks, used by the link index and by REQ.
- **Settings:** `plex_scan_on_import` (default on), `plex_path_map`.

### 3. `insights.Service`: owns the connection, monitoring and Plex knowledge
- **Connection:** URL and token (entered in the UI, never logged), `insights_plex_machine_id`, `insights_plex_server_name`.
- **Monitoring status:** `unconfigured | off | recording | unreachable`, built from atomics that hold poll health. `MonitoringActive()` feeds Convert's pause gate, and `Status()` feeds the OBS health registry.
- **Live sessions** are persisted in `insights_live_sessions` and resumed on boot. Finalizing a play is one transaction: insert the play, delete the live row.
- **Imports:** `ImportHistory(ctx, rows, ImportOptions) ImportCounts`.
  - It skips rows that overlap a live play: same user, same rating_key or title identity, intersecting interval.
  - Each run is tracked in `insights_import_runs`, and inserted rows carry `stream_sessions.import_run_id` so a run can be undone.
- **Plex library index** (`plexlinks.go`), held in memory:
  - Per-section items with their external ids, rebuilt in the background every 30 minutes, single-flight, and about 90 s after each `OnRefreshed`.
  - API: `WatchURL`, `Locate`, `TMDBForRatingKey`, `Items(kind)`.
  - No new table: one paged pass over a few thousand items is cheaper than per-item lookups.
- **Read models:** People (`people.go`), Library (`library.go`), History (filters, grouping, CSV) and Graphs (plays or duration).

### 4. Data model (migrations numbered from 0090 at implementation time, in landing order)
- `insights_live_sessions(session_key PK, rating_key, user_id, started_at, last_seen_at, paused_ms, state, steady, buffering, spell_counted, last_offset_ms, buf_count, buf_events JSON, snapshot JSON)`
- `stream_sessions` gains `grandparent_thumb` and `grandparent_rating_key` (both TEXT NOT NULL DEFAULT '').
- `insights_import_runs(id, source, started_at, finished_at, status, total, processed, imported, duplicates, overlaps, invalid, failed, error, cutoff_at)`, plus `stream_sessions.import_run_id INTEGER NOT NULL DEFAULT 0`.
- `users.plex_username TEXT NOT NULL DEFAULT ''`.
- New settings keys: `plex_scan_on_import`, `plex_path_map`, `insights_plex_machine_id`, `insights_plex_server_name`, `plex_signin_staff` (default off), `tautulli_url`, `tautulli_api_key` (masked on read).

### 5. API surface (additions)
- **Scan:** `GET|PUT /api/v1/insights/plex/scan`, `POST /api/v1/insights/plex/scan/test`.
- **Links:**
  - `GET /api/v1/plex/link?media_type=&tmdb_id=` for any signed-in user. Returns `{url}` or 204.
  - `plex_url` on Discover media detail and on available requests.
- **Watch stats:** `GET /api/v1/movies/{id}/watch-stats` and `GET /api/v1/series/{id}/watch-stats`.
- **Import:**
  - `GET /api/v1/insights/import/overlaps` and `POST /api/v1/insights/import/overlaps/remove`.
  - `GET /api/v1/insights/import/runs`, `POST /api/v1/insights/import/runs/{id}/retry`, `DELETE /api/v1/insights/import/runs/{id}/rows`.
- **Read models:**
  - `GET /api/v1/insights/users/{id}` (People).
  - `GET /api/v1/insights/library`.
  - `GET /api/v1/insights/history` gains user, from, to and group, and there is a `history.csv` export.
  - `GET /api/v1/insights/graphs` gains `metric`.
- **Account linking:** `POST|DELETE /api/v1/me/plex/link`, `GET /api/v1/me/plex/link/{pin}`, and `POST /api/v1/users/{id}/plex/merge` (admin).
- **Rule:** no response ever carries the Plex token or the server's LAN URL.

### 6. UI shape (existing dark warm palette, terracotta accent, current type scale)
- **Insights routes:** `/insights/:tab` with tabs Activity, History, People, Graphs, Reliability and Library, plus `/insights/people/:id`.
  - The tab bar scrolls sideways on phones and grids use `min(…,100%)`.
  - The header badge shows the status in four states.
  - When monitoring is off, a banner offers a Turn on button.
  - Database-backed tabs always render.
- **Alerts:** the notifications view moves to `/alerts` (OBS rebuilds its content). `/notifications` and `/insights/notifications` redirect there.
- **One Plex settings page:** Settings → Plex, or Connections → Plex if the CFG hub has landed. It has sections for Connection (sign-in hook, owned-server picker, status), Monitoring, Family sign-in, Library updates (scan toggle, mapping, last scan), Linked accounts and Tautulli import (progress, summary, retry, undo).
- **Watch on Plex:** a button on the Discover sheet and on available requests; an "Open in Plex" pill plus a "Watched by N · last played …" line on Movie and Series detail.
- **Sign-in:** a shared `usePlexPinSignIn` hook opens the popup synchronously, detects when it is closed, and falls back to a forwardUrl redirect. Login, the Plex settings page and account linking all use it.

### 7. Invariants
- Insights never reads `listen_*` or audiobook data. Watch stats come from Plex video sessions only.
- An account merge moves audiobook rows as an opaque same-person transfer and never lists titles.
- Insights and Library pages only link to Convert. They never queue a conversion.
- Credentials (Plex token, Tautulli key) are entered in the UI only and masked on read.
- Plex sign-in into a staff account is off unless `plex_signin_staff` is on. The server owner is never silently given a duplicate requester account.
- Every destructive repair (overlap removal, import undo, account merge) shows exact counts, asks for confirmation, and recommends a SAFE backup first.

#### Milestone: M1 — Insights numbers you can trust

_History, Users and Graphs agree with each other. A Tautulli import no longer double-counts live periods, past double-counts can be removed, and an ./update.sh restart no longer splits plays or re-sends 'Now playing'._

<a id="plex-01"></a>
- [x] **PLEX-01 · History 'Watched' column uses watchedSecs(), matching the Users and Stats totals** — `P1` · `S` · Phase 7
  - **Problem:** Service.History (internal/insights/activity.go:248-271) still computes `(StoppedAt-StartedAt) - PausedMS/1000` inline. The watchedSecs() helper (activity.go:216-235) is the Go twin of watchedExpr (stats.go:25) and exists because of migration 0072, but nothing calls it. Grouped or bad-stop-time imported Tautulli rows therefore show multi-hour 'Watched' values that contradict the Users and Stats totals, which are summed in SQL. Stats().Recent (stats.go:194-196) builds HistoryEntry without WatchedSecs at all. The comment in watched_test.go wrongly says History uses the helper.
  - **Approach:** 1. In activity.go, add `func (s *Service) toHistoryEntry(r HistoryRow) HistoryEntry`. It sets ThumbURL: proxyImage(r.Thumb), Geo: s.geo.Lookup(r.IPAddress), Subtitle: historySubtitle(r) and WatchedSecs: watchedSecs(r). It sets ProgressPct only when DurationMS > 0, clamped to 0..100.
    2. Use it in History() and in Stats() for the Recent list (stats.go:194-196), so the two paths can't drift apart again.
    3. Delete the inline formula.
    4. Fix the comment in watched_test.go so it describes the real call path.
  - **Files:** `internal/insights/activity.go`, `internal/insights/stats.go`, `internal/insights/watched_test.go`
  - **Acceptance:**
    - An imported row with watched_ms = 34 min and a 34 h start-to-stop span shows '34m' in the History table, the detail popup and the Recently watched card
    - Summing watched_secs over every History row for one user equals that user's total_secs on the Users tab
    - Live rows with no watched_ms still show wall time minus paused time
  - **Tests:** Go TestHistoryWatchedUsesWatchedMS: insert one imported row (session_key='', watched_ms set, huge span) and one live row through newDataTestService, call Service.History, and assert WatchedSecs for both.; Go TestHistoryTotalsMatchSQL: the sum of History().Rows[].WatchedSecs equals `SELECT "+watchedSum+" FROM stream_sessions` over the same rows.; Go TestStatsRecentHasWatchedSecs.
  - **Risk:** Very low. It changes one display field and touches no stored data.
  - **Resolves:** insights-1
<a id="plex-02"></a>
- [x] **PLEX-02 · Tautulli import skips plays already recorded live; a repair tool removes past double-counts** — `P1` · `M` · Phase 7
  - **Problem:** ImportHistory (internal/insights/import.go:79) treats a row as a duplicate only when sessionExists finds an exact (user_id, rating_key, started_at) match (repo.go:67-73). A live row's started_at is the first poll that saw the stream (poller.go:116), so it never equals Tautulli's own start time, and every play recorded both ways is inserted twice. Every COUNT and SUM in stats.go, graphs.go and repo.go then inflates plays and watch time for the overlap period. get_history is called without `grouping` (tautulli/client.go:118), so a grouped row can span several sessions. Failed inserts are dropped by a silent `continue` (import.go:105-107). The Settings copy (Settings.tsx:837) claims re-running is safe, and rows already imported may be doubled with no way to undo them.
  - **Approach:** 1. tautulli/client.go History(): always pass `grouping=0`, so each row is a single session.
    2. repo.go: add `overlapsLive(ctx, uid, ratingKey, title, gpTitle string, parentIdx, idx int, start, stop int64) bool`. It runs: `SELECT 1 FROM stream_sessions WHERE session_key <> '' AND user_id=? AND (rating_key=? OR (title=? AND grandparent_title=? AND parent_index=? AND media_index=?)) AND started_at < ? AND stopped_at > ? LIMIT 1`.
       - It is an interval-overlap test against live rows only, and uses idx_stream_sessions_dedup for the rating_key branch.
       - The title-identity branch covers a rebuilt Plex database with new rating keys.
       - Keep the exact-match sessionExists, so re-running against imported rows stays idempotent.
    3. Change the signature to `ImportHistory(ctx, rows, opts ImportOptions) ImportCounts`.
       - ImportOptions{Before int64} is an optional cutoff.
       - ImportCounts{Imported, Duplicate, Overlap, Invalid, Failed int; FirstError string}.
       - Count failed inserts and keep the first error text.
       - Update the handler in import_tautulli.go to sum and log ImportCounts. The run stays async until [PLEX-16](#plex-16).
    4. repo.firstLiveStart(): `MIN(started_at) WHERE session_key<>''`. Return it from a new endpoint and show it as a hint next to an optional 'Only import plays before <date>' checkbox in the Tautulli section.
    5. Repair for data imported before this fix:
       - `Service.ImportOverlaps(ctx) (count int, firstLive int64, err error)` and `RemoveImportOverlaps(ctx) (int64, error)`. They delete imported rows (session_key='') that overlap a live row by the same predicate, plus their buffer_events, in one transaction.
       - Endpoints (RoleAdmin): GET /api/v1/insights/import/overlaps → {count, first_live_at}; POST /api/v1/insights/import/overlaps/remove → {removed}.
       - UI: a 'Remove N double-counted plays' button with a confirm that states the count and recommends a backup.
    6. Rewrite the Settings.tsx copy: 'Plays Arrmada already recorded live are skipped; re-running only adds what's missing.'
  - **Files:** `internal/insights/import.go`, `internal/insights/repo.go`, `internal/insights/import_test.go`, `internal/tautulli/client.go`, `internal/httpapi/import_tautulli.go`, `internal/httpapi/insights.go`, `internal/httpapi/server.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Importing a Tautulli history that covers a week Arrmada also recorded live leaves that week's play count and watch time unchanged
    - Re-running the import adds 0 rows
    - An imported play that overlaps no live row (e.g. while monitoring was off) is still imported
    - The repair button shows how many imported rows overlap live ones; after confirmation those rows and their buffer events are gone, and totals drop by exactly that amount
    - Live rows are never deleted by the repair
    - The Tautulli request URL contains grouping=0
  - **Tests:** Go TestImportSkipsOverlapWithLive: seed a live row (session_key='42', 1000-4000), import a Tautulli row for the same user and rating_key at 1003-3990, and expect Overlap=1, Imported=0.; Go TestImportOverlapByTitleIdentity: a different rating_key with the same title, S/E and user is still detected.; Go TestImportKeepsNonOverlapping.; Go TestImportCountsFailedInserts: a forced insert error gives Failed>0 and FirstError set.; Go TestRemoveImportOverlaps: removes only imported overlapping rows and their buffer_events, never live rows.; Go (tautulli) httptest: the get_history query includes grouping=0.
  - **Depends on:** SAFE — DB snapshot/backup to recommend before the repair (soft)
  - **Risk:** The repair deletes stats rows, so it shows the count first, requires confirmation, and deletes only imported rows. The title-identity branch could match two genuinely separate plays of the same episode only if they overlap in time for the same user, which is not a real case.
  - **Resolves:** insights-2
<a id="plex-03"></a>
- [x] **PLEX-03 · Persist live sessions so restarts and crashes neither split plays nor re-send 'Now playing'** — `P1` · `M` · Phase 7
  - **Problem:** Live sessions exist only in the in-memory `live` map (service.go:40). On shutdown, flushAll (poller.go:279-284) finalizes them. After boot, reconcile treats every running stream as new (started=now) and publishes plex.stream.started again (poller.go:115-120). Every ./update.sh therefore splits each in-progress stream into two plays and sends duplicate 'Now playing' alerts, and a crash skips flushAll, so those plays are lost.
  - **Approach:** 1. New migration (next free number ≥0090) `insights_live_sessions.sql`: insights_live_sessions(session_key TEXT PRIMARY KEY, rating_key TEXT, user_id TEXT, started_at INTEGER, last_seen_at INTEGER, paused_ms INTEGER, state TEXT, steady INTEGER, buffering INTEGER, spell_counted INTEGER, last_offset_ms INTEGER, buf_count INTEGER, buf_events TEXT /*JSON*/, snapshot TEXT /*JSON plex.Session*/).
    2. Make repo methods transaction-aware through a small `execer` interface (ExecContext/QueryRowContext) so insertSession and insertBufferEvent work on *sql.Tx. Add saveLive(ex, key, ls), deleteLive(ex, key) and loadLive(ctx) ([]persistedLive, error). Make bufEvent serializable through a DTO with exported fields.
    3. reconcile: after folding a poll, upsert every observed session in ONE transaction per poll. finalize: run insertSession, insertBufferEvent for each event, and deleteLive in a single tx, so a crash can never record a play twice or lose one.
    4. In Run(), before the loop, restoreLive() loads rows into s.live marked `resumed`. In reconcile, if a key is present with the same rating_key and user_id and `now - lastSeen <= maxResumeGap` (15 min), continue silently with no publish.
       - Downtime is not credited as watch time: if the offset advanced less than the wall gap, add (gap - offsetAdvance) to pausedMS.
       - If the gap is longer, finalize at the saved lastSeen and start a fresh session.
       - Keys absent from Plex are finalized at lastSeen by the existing vanished loop.
    5. On ctx.Done, stop calling flushAll: the rows are persisted and resume on boot. Keep flushAll for the disabled or unconfigured branch, and make it delete the persisted rows.
  - **Files:** `internal/store/migrations/ (new 009x_insights_live_sessions.sql)`, `internal/insights/poller.go`, `internal/insights/repo.go`, `internal/insights/service.go`, `internal/insights/poller_test.go`
  - **Acceptance:**
    - Restarting the container mid-movie produces ONE stream_sessions row for that play, with started_at from before the restart
    - No plex.stream.started event is published for a resumed session
    - Killing the process (SIGKILL) mid-stream and restarting after the stream has ended still records the play, credited up to its last-seen time
    - Turning monitoring off finalizes the play and empties insights_live_sessions
    - A restart gap is not counted as watch time unless the view offset advanced
  - **Tests:** Go TestRestartResumesLiveSession: service 1 reconciles session A; service 2 on the same DB restores, reconciles A, then ends it. Expect one row with the original started_at and one started event in total (fake bus subscriber).; Go TestCrashRecoveryFinalizesVanished: persist A, skip flushAll, restore on a new service, reconcile with no sessions. Expect a row with stopped_at = saved last_seen.; Go TestResumeGapTooLongStartsNew: lastSeen 2 h ago means the old play is finalized and a new one starts.; Go TestResumeGapCreditedAsPausedWhenOffsetStalled.; Go TestFinalizeIsAtomic: the live row is deleted in the same tx as the insert (inject an insert failure, and the live row survives).; Race tests in Docker (go test -race ./internal/insights/...).
  - **Risk:** This adds one small write transaction per poll per active stream, which is negligible at the 5 s interval. Resuming must re-check rating_key and user_id to keep the sessionKey-reuse protection. If PLEX-20 lands first, the snapshot JSON carries its new fields automatically.
  - **Resolves:** insights-4

#### Milestone: M2 — Plex sees every change

_Each import, upgrade, rename, delete and Convert swap triggers one debounced partial scan of the right Plex folder. The owner can see and correct the path mapping._

<a id="plex-04"></a>
- [x] **PLEX-04 · Plex scan engine: section locations, path mapping, debounced partial refresh, settings and status UI** — `P1` · `M` · Phase 7
  - **Problem:** internal/plex is read-only: Identity, Libraries, SectionTotal, RecentlyAdded, Image and Sessions (client.go:65-165, sessions.go:103). Nothing calls /library/sections/{id}/refresh, so Plex learns about new, renamed, deleted or converted files only when its own watcher or scheduled scan notices. That watcher is unreliable on Unraid /mnt/user FUSE shares. Arrmada's container paths (e.g. /movies/Title (2010)) usually differ from the Plex container's paths (e.g. /data/media/movies), so a mapping is needed and must be visible.
  - **Approach:** 1. internal/plex/client.go:
       - Library gains `Locations []string`, decoded from Directory[].Location[].path (keep the JSON tags of the existing fields).
       - Add a generic `do(ctx, method, path string) error` that checks the status and does not decode the body.
       - `RefreshPath(ctx, sectionKey, dir string) error` sends GET `/library/sections/{key}/refresh?path=` + url.QueryEscape(dir). Confirm GET vs PUT once against the owner's PMS.
       - `RefreshSection(ctx, key) error`.
    2. New package internal/plexscan:
       - mapping.go has a pure `Resolve(kind, dir, arrRoot string, sections []plex.Library, maps []PathMap) Resolution{SectionKeys []string, PlexPath, How string}`. How is one of direct | mapped | guessed | section, tried in that order:
         - (a) dir under a Location of a section of matching type (movie | show) → as is
         - (b) longest-prefix From in plex_path_map → To + remainder, then the section whose Location is the longest prefix
         - (c) the Arrmada root's last segment equals a Location's last segment → location + path relative to the root
         - (d) every section of that kind, with a once-per-root Info log: 'add a path mapping for faster scans'
       - scanner.go has `Scanner{client func(ctx) *plex.Client; configured func(ctx) bool; settings; roots func() map[string]string; log}`.
         - `Request(kind, dir)` is a non-blocking enqueue into a mutex-guarded pending map.
         - `Run(ctx)` is a single worker with a 15 s debounce per target. It drops a path when an ancestor is already pending, allows at most one refresh per path per 60 s, and caches the section list for 10 minutes.
         - Status is kept in atomics: LastAt, LastPath, LastSection, How, LastErr.
         - `OnRefreshed(func(kind, sectionKey, plexPath string))` registers listeners.
         - It is a no-op when Plex isn't configured or `plex_scan_on_import` is off. Failures log at Warn and never propagate to callers.
    3. insights.Service exports `PlexClient(ctx) *plex.Client` and `Configured(ctx) bool`. library.Importer exports `MovieRoot()` and `TVRoot()`, which wrap movieDir() and tvDir().
    4. Settings: `plex_scan_on_import` (bool, default true) and `plex_path_map` (JSON [{from,to}]).
    5. API (RoleManager, like the other /insights/plex routes):
       - GET /api/v1/insights/plex/scan → {enabled, path_map, roots:[{kind, arrmada_root, sections:[title], plex_path, how}], last_scan}
       - PUT /api/v1/insights/plex/scan {enabled, path_map}
       - POST /api/v1/insights/plex/scan/test {kind, run?:bool}, which resolves (and with run=true refreshes) that root
    6. UI: a 'Library updates' section in PlexSettings (Insights.tsx ~923-1042), styled with the existing Section and Toggle components. [PLEX-17](#plex-17) later moves it to the Plex page. It contains:
       - a toggle 'Tell Plex to scan after changes'
       - one line per library, e.g. 'Movies: /movies → /data/media/movies · section Movies (auto)', with an amber note when How=section
       - From/To mapping rows, a Test button and a 'Scan now' button
       - 'Last scan: 2 min ago · /data/media/movies/Heat (1995)' or the last error
    7. main.go: build the scanner after insightsSvc with roots from imports.MovieRoot()/TVRoot(), and start `go scanner.Run(runCtx)`. Triggers come in [PLEX-05](#plex-05).
  - **Files:** `internal/plex/client.go`, `internal/plex/client_test.go`, `internal/plexscan/mapping.go (new)`, `internal/plexscan/scanner.go (new)`, `internal/plexscan/mapping_test.go (new)`, `internal/plexscan/scanner_test.go (new)`, `internal/insights/service.go`, `internal/library/importer.go`, `internal/httpapi/insights.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`, `web/src/pages/Insights.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - 'Scan now' on the Movies root makes the PMS log a partial scan of the Plex-side movies path
    - The settings view shows each library's resolved mapping and how it was found (auto, mapped or whole library)
    - With Arrmada at /movies and Plex at /data/media/movies, auto-guess resolves correctly with no manual mapping; with an unrelated layout and no mapping, the whole Movies section is refreshed and the UI says so
    - With Plex unconfigured or the toggle off, no HTTP calls are made and nothing logs repeatedly
    - The token never appears in logs or API responses
  - **Tests:** Go (plex) TestLibrariesDecodeLocations (fixture /library/sections JSON).; Go (plex) TestRefreshPathEscaping: an httptest PMS records the path and query for folder names with spaces, parentheses, apostrophes and '&'. A 401 surfaces as an error.; Go (plexscan) TestResolve table: in-location, user map, longest prefix wins, suffix guess, section fallback, type filtering (movie vs show).; Go (plexscan) TestScannerDebounce: 10 requests in one folder within the window give one refresh (short window, fake clock).; Go (plexscan) TestScannerAncestorCoalesce and TestScannerRateLimitPerPath.; Go (plexscan) TestScannerNoopWhenUnconfigured.
  - **Risk:** A wrong mapping makes Plex scan a path that doesn't exist, which is harmless but does nothing, so the resolution is shown in the UI. The full-section fallback is heavier on large libraries but correct. The refresh HTTP method and its behaviour must be checked against the owner's PMS once.
  - **Resolves:** insights-5, product-1, backend-14, movies-9, convert-11
<a id="plex-05"></a>
- [x] **PLEX-05 · Trigger Plex scans from imports, upgrades, renames, deletes and Convert swaps through direct hooks** — `P1` · `M` · Phase 7
  - **Problem:** Even with a scan engine, nothing tells it what changed. The relevant events carry no paths: movie.downloaded is published at coordinator.go:1570 and httpapi/movies.go:667; movie.renamed at movies.go:702; movie.file_deleted at movies.go:418 and 511; series.imported at series.go:1075; series.renamed at series_interactive.go:628. Convert publishes nothing after a swap (process.go:625-655). Movie imports also reach Convert and Subtitles only through a lossy bus goroutine (main.go:523-541), and the eventbus drops events when a subscriber's buffer is full (eventbus.go:67-81).
  - **Approach:** 1. Movies, automatic import: add `Coordinator.SetMovieImportedHook(fn func(ctx, movieID int64, filePath string))`, mirroring SetSeriesImportedHook (coordinator.go:79-92). Call it right before the movie.downloaded publish at coordinator.go:1570; `target` is the final file path. Upgrades go through the same path.
    2. main.go: define one `onMovieImported(ctx, id, path)` that runs convertSvc.IndexMovie, subtitlesSvc.OnMovieImported and scanner.Request("movie", filepath.Dir(path)).
       - Register it with the coordinator and pass it to httpapi Deps as `MovieImported`.
       - Delete the bus goroutine at main.go:523-541. Keep publishing movie.downloaded, because notify and usernotify subscribe to it.
    3. Manual import (handleManualImport, movies.go:~650-668): after ManualImport succeeds, Movies.Get the movie and call deps.MovieImported(ctx, id, m.MovieFilePath).
    4. Series import: in the SetSeriesImportedHook closure (main.go:513), add scanner.Request("show", seriesSvc.FolderPath(ctx, id)). FolderPath is a new helper next to ExistingFolderName (series/service.go:755) that returns the full path, one level above a season folder.
    5. Renames and deletes in httpapi: add Deps `PlexScan interface{ Request(kind, dir string) }`, nil-safe through `a.plexScan(kind, dir)`. Capture the folder BEFORE the change:
       - handleRename (movies.go:689): the old folder, plus the new folder after the rename.
       - handleDeleteMovieFile (498), handleDeleteVersionFile (405) and handleDeleteMovie with deleteFiles (706): the folder.
       - handleSeriesRename (series.go:653): the old and new series folder.
       - handleDeleteEpisodeFile (739): the series folder.
       - handleDeleteSeries with files (336): the series folder.
    6. Coordinator-side series rename (series_interactive.go:628) and any coordinator-side file replacement: add `Coordinator.SetLibraryChangedHook(func(kind, dir string))`, wired to scanner.Request.
    7. Convert:
       - Add `convertSvc.SetLibraryChanged(func(kind, path string))`. In finalizeOutput (process.go), after markConverted succeeds, call it with kind movie, or show for episode jobs, and filepath.Dir(finalPath).
       - Call it as well in the swaps.go startup reconcile after a successful rename.
    8. When ACQ's import outbox lands, the movie and series hooks move onto it, with no change to the scanner API.
  - **Files:** `internal/automation/coordinator.go`, `internal/automation/series_interactive.go`, `internal/series/service.go`, `internal/httpapi/server.go`, `internal/httpapi/movies.go`, `internal/httpapi/series.go`, `internal/convert/service.go`, `internal/convert/process.go`, `internal/convert/swaps.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - Importing a movie produces one GET /library/sections/<movies key>/refresh?path=<Plex-side movie folder> within about 20 s, logged at Info
    - Five episodes of one show imported within a minute produce one refresh of that show's folder
    - A movie rename refreshes the old and the new folder; after it, Plex shows no 'unavailable' item
    - Deleting a movie file or the whole movie refreshes its folder, and Plex drops the item (with Plex's 'empty trash automatically' on)
    - After a Convert swap from .mp4 to .mkv, Plex plays the new file without a manual scan
    - Movie imports still reindex Convert and queue subtitles, now through the hook, including manual imports
  - **Tests:** Go (automation) TestMovieImportedHookFiresWithPath: the automatic import path calls the hook once with the target path.; Go (httpapi) TestDeleteMovieFileRequestsScanOfFolder (fake PlexScan records requests; the folder is captured before the delete).; Go (httpapi) TestRenameRequestsOldAndNewFolder.; Go (convert) TestFinalizeOutputCallsLibraryChanged: a successful swap calls it once with the dir, and a failed swap calls nothing.; Go (series) TestFolderPathSeasonAndFlat (season-folder layout vs flat layout).; Manual: on the owner's Plex with a test title (never a real library file for Convert), confirm import, rename and delete behave as above.
  - **Depends on:** [PLEX-04](#plex-04), ACQ — import outbox (backend.t16) can later replace the direct hooks (soft), MOV — movie.deleted event and movie delete flow (movies.t18), only if it reshapes handleDeleteMovie (soft), CONV — convert.done event (convert.t16) is not required; the direct callback is used (soft)
  - **Risk:** It touches hot paths owned by ACQ, MOV and CONV, so keep each hook call a one-liner to keep merge conflicts small. Moving Convert and Subtitles off the bus changes their trigger, so keep both behaviours tested. Whether a partial scan of a deleted folder makes Plex drop the item must be checked live; if it doesn't, scan the parent folder for deletes.
  - **Resolves:** insights-5, product-1, backend-14, movies-9, convert-11

#### Milestone: M3 — Sign-in that works, status that tells the truth

_Sign in with Plex works on iPhone and in the PWA, picks only an owned server, tests its URL and turns monitoring on. The badge, banners, Convert hint and HW badges report what is actually happening._

<a id="plex-06"></a>
- [x] **PLEX-06 · Sign in with Plex works on iPhone: synchronous popup, closed or blocked detection, and a forwardUrl redirect fallback** — `P1` · `S` · Phase 7
  - **Problem:** Login.tsx:22-38 and the admin flow in Insights.tsx:941-969 call window.open(auth_url) only after `await api.plexLoginStart()` or `insightsPlexAuthStart()`, and Safari blocks popups that aren't opened directly in the click handler. Login never checks for a null or closed popup and keeps polling for about 3 minutes (90 tries). plex.AuthURL (oauth.go:95-101) never sets forwardUrl, so there is no full-page redirect fallback. Family members on iPhones and in the installed PWA tap the button and nothing happens.
  - **Approach:** 1. New hook web/src/lib/plexSignIn.ts: `usePlexPinSignIn({start, poll, onDone, onError})`.
       - In the click handler, synchronously call `const w = window.open('about:blank','plex-auth','width=620,height=720')`.
       - When start() resolves, set `w.location.href = auth_url`.
       - If w is null (blocked, or the standalone PWA), store {pinId, kind} in sessionStorage and set `window.location.href = auth_url` (redirect mode).
       - Each poll tick checks `w.closed` and stops with 'Sign-in window closed before finishing.'
    2. Backend: change the signature to `plex.AuthURL(clientID, code, product, forwardURL string)`.
       - handlePlexLoginStart (auth_plex.go:30) and handleInsightsPlexAuthStart (insights.go:137) build the forward URL server-side from requestIsHTTPS(r) (auth.go:213), r.Host (or X-Forwarded-Host when present) and cfg.BaseURL.
       - Login uses /login?plexpin=<id>. Admin uses the Plex settings route (today /insights?tab=settings&plexpin=<id>). The URL is never taken from the client.
    3. Login.tsx on mount: when `plexpin` is in the query or sessionStorage, poll plexLoginPoll(id) for up to 30 s with 'Finishing Plex sign-in…', then navigate to /discover or show the error. PlexSettings does the same with insightsPlexAuthPoll.
    4. Use the hook in Login.tsx and PlexSettings. [PLEX-14](#plex-14) reuses it for account linking.
  - **Files:** `web/src/lib/plexSignIn.ts (new)`, `web/src/pages/Login.tsx`, `web/src/pages/Insights.tsx`, `internal/plex/oauth.go`, `internal/httpapi/auth_plex.go`, `internal/httpapi/insights.go`, `internal/insights/service.go`
  - **Acceptance:**
    - On iPhone Safari and in the installed PWA, tapping Sign in with Plex opens the Plex page, as a popup or a full-page redirect, and lands the user on /discover after approval
    - Closing the Plex window shows a message within about 2 s instead of a 3-minute timeout
    - A blocked popup falls back to the redirect, with no silent wait
    - The admin connect flow on the Plex settings page works the same way
  - **Tests:** Go (plex) TestAuthURLIncludesForwardUrl (escaped correctly).; Go (httpapi) TestPlexLoginStartBuildsForwardURLFromRequest: honours BaseURL and X-Forwarded-Proto, and ignores any client-supplied URL.; UI check: desktop with popups blocked completes through the redirect.; UI check: iPhone sign-in as a test Plex Home user (not the owner's credentials typed by the agent).
  - **Risk:** The forwardUrl host comes from the request's Host header. It only affects the requester's own redirect back to Arrmada and is not an open redirect, because Plex only redirects to what Arrmada built for that PIN.
  - **Resolves:** insights-8, system-11
<a id="plex-07"></a>
- [x] **PLEX-07 · Sign in with Plex finishes setup: owned-server discovery, a tested URL, monitoring on, and the server name** — `P2` · `S` · Phase 7
  - **Problem:** insights_enabled defaults to false (service.go:83). PollPlexAuth (service.go:159-177) saves the token but never turns monitoring on, so a fresh sign-in records no history. DiscoverServer (plex/oauth.go:107-155) returns the first local HTTP connection of ANY server resource without checking `owned`, so a friend's shared server can be picked. The URL is saved without calling Identity(), and discovery only runs when no URL is set yet.
  - **Approach:** 1. plex/oauth.go: make `plexTVBase` a package var so tests can point it at httptest. Replace DiscoverServer with `DiscoverServers(ctx, clientID, token) ([]ServerCandidate, error)`.
       - ServerCandidate is {Name, MachineID, Owned, Conns []Conn{URI, Address, Port, Local, Protocol, Relay}}.
       - Decode `name`, `clientIdentifier` and `owned` from /api/v2/resources?includeHttps=1, the same payload serverIDs in account.go reads.
       - Keep owned servers only, and order connections local http, then local https, then remote non-relay.
    2. insights/service.go PollPlexAuth now returns PlexAuthResult{Authorized bool, ServerName string, Choices []ServerChoice}.
       - For each owned candidate, try each connection with plex.New(uri, token).Identity under a 3 s timeout, and require MachineIdentifier == candidate.MachineID. Keep the first that answers.
       - Run discovery when the URL is empty OR the stored URL fails Identity.
       - With more than one owned server answering, return Choices. The UI shows a picker and saves the chosen URL through PUT /insights/plex.
    3. Monitoring default: if `settings.Get(keyEnabled, "") == ""` (never set), set it to true after a successful sign-in, and on the first SetConfig that leaves URL and token configured. An explicit off is never overridden.
    4. Store keyMachineID (`insights_plex_machine_id`, also used by [PLEX-10](#plex-10)) and keyServerName on sign-in and on a successful Test(). Config returns `server_name` and `machine_id`, and the UI shows 'Connected to <name>'.
  - **Files:** `internal/plex/oauth.go`, `internal/plex/account.go`, `internal/plex/oauth_test.go (new)`, `internal/insights/service.go`, `internal/httpapi/insights.go`, `web/src/pages/Insights.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - On a fresh install, after Sign in with Plex, Config shows enabled=true, a URL that passed Identity(), and the server name
    - An account that also has a friend's server shared to it is never pointed at the friend's server
    - With two owned servers, the UI asks which one to use
    - If the admin turned 'Enable monitoring' off earlier, a later re-sign-in leaves it off
  - **Tests:** Go (plex) TestDiscoverServersOwnedOnly: a fake plex.tv returns an owned and a shared server, and only the owned one comes back.; Go (insights) TestPollPlexAuthEnablesMonitoringWhenUnset and TestPollPlexAuthRespectsExplicitOff (fake PIN endpoint and fake PMS /identity).; Go (insights) TestPollPlexAuthPicksRespondingConnection: the first URI is dead, the second answers with the matching machine id, and the second is saved.
  - **Risk:** The plex.tv resource fields (owned, connections) should be checked against one live reply. The owner's install already has monitoring on, so the main benefit is for re-setup and other installs.
  - **Resolves:** insights-3, insights-15
<a id="plex-08"></a>
- [x] **PLEX-08 · Truthful monitoring status: four-state badge, 'monitoring is off' banner, and an honest Convert pause hint** — `P2` · `S` · Phase 7
  - **Problem:** The green 'Plex connected' badge only checks that a token and URL are set (Insights.tsx:26). Activity calls Plex directly, so it works even when nothing is being recorded, and History says it 'fills in as people watch' while monitoring is off. Convert's PlexWatching is `s.watching.Load() != nil` (convert/settings.go:64), which is always true after main.go:555. So the 'Pause while someone is watching Plex' hint (Convert.tsx:948) promises a pause that never happens when monitoring is off, because Watching() returns false when there are no polls.
  - **Approach:** 1. insights.Service: the poller records health in atomics: lastPollOK (unix), lastPollErr (atomic.Value string) and consecutiveFails. poll() updates them on success and failure.
    2. Config gains `status` (unconfigured | off | recording | unreachable, where unreachable means ≥3 consecutive failed polls), `last_poll_at` and `last_error`.
       - Add `func (s *Service) MonitoringActive(ctx) bool` (enabled && configured).
       - Add `func (s *Service) Status(ctx) Status` for the OBS health registry.
    3. convert: change the signature to `SetWatching(watching func() bool, known func() bool)`. Settings.PlexWatching = known(). main.go passes insightsSvc.Watching and a closure over MonitoringActive. Update convert/integration_test.go:46.
    4. The Insights.tsx header badge has four states, using existing tokens:
       - Not connected (--avoid)
       - Connected · not recording (--avoid)
       - Recording (--good)
       - Plex unreachable (--reject, with last_error as the tooltip)
    5. History, Users, Graphs and Reliability show a slim banner when status=off: 'Monitoring is off — nothing new is recorded.', with a [Turn on] button that PUTs enabled=true.
    6. Convert.tsx shows a new hint when PlexWatching is false: 'Needs Plex monitoring turned on (Plex settings) — until then this does nothing.'
  - **Files:** `internal/insights/service.go`, `internal/insights/poller.go`, `internal/convert/service.go`, `internal/convert/settings.go`, `internal/convert/integration_test.go`, `cmd/arrmada/main.go`, `web/src/pages/Insights.tsx`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With token and URL set but monitoring off, the badge reads 'Connected · not recording', and History shows the banner with a working Turn on button
    - With Plex stopped, the badge flips to 'Plex unreachable' within about 3 poll intervals and recovers when Plex returns
    - With monitoring off, Convert shows the 'needs monitoring' hint; with it on, it shows the pause promise
  - **Tests:** Go (insights) TestConfigStatus: a table over enabled, configured and poll-failure combinations, setting the atomics directly.; Go (convert) TestSettingsPlexWatchingKnownFollowsCallback.; UI check: turn monitoring off and confirm the badge, the History banner and the Convert hint.
  - **Depends on:** OBS — the health registry can consume insights.Status() (soft, consumer side)
  - **Risk:** The SetWatching signature change touches convert tests. Otherwise low.
  - **Resolves:** insights-3
<a id="plex-09"></a>
- [x] **PLEX-09 · HW transcode badge and buffer diagnosis based on what Plex actually uses (hwDecoding/hwEncoding)** — `P2` · `S` · Phase 7
  - **Problem:** plex/sessions.go:159 maps TranscodeHW from `transcodeHwRequested`, which is true whenever hardware transcoding is enabled, even if Plex silently fell back to CPU. BufferCause (sessions.go:69-89) picks 'hardware transcode falling behind' or 'CPU transcode … hardware not in use' from that flag. The HW badges (Insights.tsx:351, 382, 546; Dashboard.tsx:263) and the recorded hw_transcode column use it too, so on the owner's Arc a CPU fallback still shows 'HW' and blames the GPU.
  - **Approach:** 1. rawSession.TranscodeSession parses transcodeHwDecoding, transcodeHwDecodingTitle, transcodeHwEncoding, transcodeHwEncodingTitle and transcodeHwFullPipeline. A tolerant `flexStr` accepts a string or bool, since Plex returns 'qsv' or 'vaapi'. Keep transcodeHwRequested.
    2. Session gains HWRequested, HWDecode, HWEncode, HWFullPipeline and HWTitle. TranscodeHW = HWEncode || HWDecode.
    3. BufferCause:
       - HWEncode → 'hardware transcode falling behind'
       - HWRequested && !HWEncode → new cause 'transcode_fallback': 'Plex fell back to CPU — hardware transcoding was requested but not used'
       - otherwise → transcode_cpu
       Add the cause to causeLabel (reliability.go:60) and the CAUSE map in Insights.tsx.
    4. Stream JSON adds hw_decode, hw_encode, hw_requested and hw_title. The recorder stores hw_transcode = HWEncode.
    5. Badges read 'HW dec+enc', 'HW enc', 'HW dec' or 'CPU', with a 'fell back to CPU' warning chip (--avoid) when hardware was requested but not used. They appear in StreamCard, DeepDive, HistoryDetail and the Dashboard now-playing. History tooltip: 'Plays before <date> show HW as requested.'
    6. Capture a real /status/sessions JSON from the owner's PMS during one HW transcode and one forced CPU transcode, by playing an existing title at a lower quality (no file changes, no Convert). Save both as plex/testdata fixtures.
  - **Files:** `internal/plex/sessions.go`, `internal/plex/buffercause_test.go`, `internal/plex/testdata/ (new fixtures)`, `internal/insights/activity.go`, `internal/insights/poller.go`, `internal/insights/reliability.go`, `web/src/pages/Insights.tsx`, `web/src/pages/Dashboard.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A stream Plex transcodes on the Arc shows 'HW dec+enc' (or similar), and the same title forced to CPU shows 'CPU' with the 'fell back' chip
    - Buffering during a CPU fallback is classified as transcode_fallback, not as the hardware falling behind
    - The Activity badge matches the '(hw)' marker on the Plex dashboard for the same stream
  - **Tests:** Go (plex) TestFlattenHWFields: the HW, requested-but-CPU and direct-play fixtures set the right flags.; Go (plex) TestBufferCauseFallback.; UI check against the Plex dashboard on a live transcode.
  - **Risk:** The field types are undocumented, so base them on the live fixture capture. Historical hw_transcode rows keep the old meaning, which the UI copy states.
  - **Resolves:** insights-9

#### Milestone: M4 — Watch on Plex

_In-library titles link straight to app.plex.tv on Discover, Requests, Movie and Series pages. Detail pages show who watched them, and new imports can use Plex-native {tmdb-…} folder names._

<a id="plex-10"></a>
- [x] **PLEX-10 · Plex library index: map TMDB/TVDB/IMDb ids to rating keys and build app.plex.tv deep links** — `P1` · `M` · Phase 7
  - **Problem:** Nothing in Arrmada can find a title in Plex. Identity() returns MachineIdentifier (client.go:59-76) and RecentlyAdded returns RatingKey (client.go:116-161), but there is no lookup from TMDB to ratingKey. Without one there can be no 'Watch on Plex' link, no 'is it in Plex yet' check for REQ, and no reliable Insights-to-Arrmada title mapping for the People and Library pages.
  - **Approach:** 1. plex/client.go: `SectionItems(ctx, key string, typ int) ([]Item, error)`, where typ is 1 (movie) or 2 (show). It pages `/library/sections/{key}/all?type=<typ>&includeGuids=1&X-Plex-Container-Start=N&X-Plex-Container-Size=500`.
       - Item is {RatingKey, Type, Title, Year, AddedAt, SectionKey, TMDB, TVDB int, IMDB string}.
       - The pure `parseGuids(primary string, guids []string)` handles new-agent `tmdb://603`, `tvdb://81189` and `imdb://tt0133093`, and legacy `com.plexapp.agents.themoviedb://603?lang=en`, `com.plexapp.agents.imdb://tt0133093?lang=en` and `com.plexapp.agents.thetvdb://81189?lang=en`. `plex://` primaries are ignored.
       - Add GrandparentRatingKey to RecentItem.
    2. New internal/insights/plexlinks.go holds the index {machineID, movieByTMDB, movieByIMDB, showByTMDB, showByTVDB, byKey map[ratingKey]Item, builtAt}.
       - It is built from Libraries() for movie and show sections, with a 30-minute TTL.
       - Rebuilds run in the background, single-flight (golang.org/x/sync/singleflight or a mutex flag), stale-while-revalidate. The first lookup on an empty index triggers a build and returns not found without blocking.
       - It is marked stale and rebuilt about 90 s after scanner OnRefreshed ([PLEX-04](#plex-04)), and also from the import hooks when scanning is off.
       - machineID comes from the `insights_plex_machine_id` setting ([PLEX-07](#plex-07)), or lazily from Identity().
    3. API on insights.Service:
       - `Locate(ctx, media string, ids ExternalIDs{TMDB, TVDB int; IMDB string}) (Item, bool)`. Movies try tmdb then imdb; shows try tmdb, tvdb, then imdb.
       - `WatchURL(ctx, media string, ids ExternalIDs) string` returns `https://app.plex.tv/desktop/#!/server/<machineID>/details?key=%2Flibrary%2Fmetadata%2F<ratingKey>`, or "" when Plex isn't configured or the title isn't found.
       - `TMDBForRatingKey(key) (media string, tmdb int, ok bool)` and `Items(kind) []Item` are used by [PLEX-24](#plex-24).
    4. httpapi: an optional `PlexLinker` interface on Deps.
       - GET /api/v1/plex/link?media_type=movie|series&tmdb_id=N, for any signed-in user including external sessions. It looks up tvdb and imdb from the library record when present, and returns {url} or 204.
       - The response never contains the token or the server URL.
  - **Files:** `internal/plex/client.go`, `internal/plex/guids_test.go (new)`, `internal/plex/testdata/ (section listing fixtures)`, `internal/insights/plexlinks.go (new)`, `internal/insights/plexlinks_test.go (new)`, `internal/insights/service.go`, `internal/httpapi/server.go`, `internal/httpapi/insights.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - For a movie and a show in Plex, /api/v1/plex/link returns a URL that opens the item in app.plex.tv on the owner's server
    - A title imported a few minutes ago gets its link after the post-scan rebuild, with no restart
    - With Plex not configured, the endpoint returns 204 and nothing logs errors
    - Legacy-agent libraries resolve through IMDb (movies) or TVDB (shows)
    - The index rebuild for a library of about 5,000 items takes a few seconds in the background and never blocks a request
  - **Tests:** Go (plex) TestParseSectionGuids: fixtures for tmdb://, legacy themoviedb with ?lang, legacy imdb movie, tvdb-only show, plex:// primary ignored.; Go (insights) TestWatchURLFormat (URL-escaped key, machine id).; Go (insights) TestIndexStaleOnRefresh: OnRefreshed marks it stale, and the next lookup triggers one rebuild (single-flight under concurrent lookups, with -race).; Go (insights) TestNoPlexNoURL.; Manual: check a new-agent and a legacy-agent title on the owner's server.
  - **Depends on:** [PLEX-04](#plex-04), [PLEX-07](#plex-07)
  - **Risk:** Large libraries mean several thousand items per rebuild, so keep it paged, in the background and single-flight. Requesters who sign in with a local password and have no Plex access will hit a Plex login wall, which is expected. Legacy-agent libraries may lack TMDB guids, so the IMDb and TVDB fallbacks matter.
  - **Resolves:** discover-5, product-1, movies-9
<a id="plex-11"></a>
- [x] **PLEX-11 · 'Watch on Plex' buttons on Discover, Requests, Movie and Series pages** — `P1` · `S` · Phase 7
  - **Problem:** Requesters reach '✓ In your library' on the Discover sheet (Discover.tsx:1311) and stop there. Available requests have no way into Plex. MovieDetail's ExternalLinks shows only IMDb and TMDB (MovieDetail.tsx:395-404), and SeriesDetail has no Plex link either.
  - **Approach:** 1. handleMediaDetail (httpapi/discover.go:253): when the title has a file, add `plex_url` from PlexLinker.WatchURL.
    2. handleListRequests (httpapi/requests.go:13): set `plex_url` on requests whose tracking stage is available, resolved in one pass and cheap because the index is in memory.
    3. Discover.tsx RequestDetailModal: when plex_url is set, a primary '▶ Watch on Plex' button (target=_blank rel=noopener) next to the in-library chip, in the existing accent button style.
    4. Requests view: the same small button on available request cards.
    5. MovieDetail ExternalLinks and SeriesDetail header links: a 'Plex' pill, fetched lazily from /api/v1/plex/link and hidden on 204.
    6. Hand-off note for REQ: push and inbox deep links (usernotify.go:243, NotificationBell.tsx:61-65) can use the same WatchURL; that work belongs to REQ.
  - **Files:** `internal/httpapi/discover.go`, `internal/httpapi/requests.go`, `web/src/pages/Discover.tsx`, `web/src/pages/Requests.tsx`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/SeriesDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Opening an in-library movie or show on Discover shows 'Watch on Plex', which opens that title's page on the owner's server
    - An available request shows the button; a requested-but-not-yet-in-Plex one doesn't
    - Movie and Series detail pages show a Plex pill when the title is in Plex
    - Without Plex configured, nothing new appears and nothing errors
  - **Tests:** Go (httpapi) TestMediaDetailIncludesPlexURLWhenLinked (fake PlexLinker), and TestMediaDetailOmitsWhenUnconfigured.; Go (httpapi) TestListRequestsPlexURLOnlyWhenAvailable.; UI check with Plex connected and disconnected, at phone width.
  - **Depends on:** [PLEX-10](#plex-10), REQ — push and inbox deep links and 'ready once Plex has it' consume WatchURL/Locate (consumer side, soft)
  - **Risk:** Low. Local-account requesters without Plex access see a Plex login page, which is acceptable.
  - **Resolves:** discover-5, product-1, movies-9
<a id="plex-12"></a>
- [x] **PLEX-12 · 'Watched by' line on Movie and Series detail pages** — `P2` · `S` · Phase 7
  - **Problem:** Insights already records plays (stream_sessions with rating_key, title, year and grandparent_title), but the owner can't see on a title's own page whether anyone has watched it. Questions like 'is it safe to delete this?' or 'did Mum watch this yet?' need a trip to History.
  - **Approach:** 1. insights/repo.go: `watchedBy(ctx, ratingKey string, fallback TitleMatch) ([]UserPlays, error)`.
       - Movies: `WHERE media_type='movie' AND (rating_key=? OR (title=? AND year=?))`.
       - Shows: `WHERE media_type='episode' AND (grandparent_rating_key=? OR grandparent_title=?)`. Use the grandparent_rating_key branch only when the [PLEX-20](#plex-20) column exists; until then grandparent_title alone.
       - Group by user_id → {user_id, user_name, plays, last_played}.
    2. Service.WatchStats(ctx, media, ids, title, year) uses [PLEX-10](#plex-10) Locate for the rating key.
    3. Endpoints (RoleManager): GET /api/v1/movies/{id}/watch-stats and GET /api/v1/series/{id}/watch-stats → {available, plays, users:[{id,name,plays,last_played}], last_played}.
    4. UI: a line under the MovieDetail and SeriesDetail header: 'Watched by 3 · last played 2 days ago', with a hover or tap list of names. The names link to the People page once [PLEX-21](#plex-21) lands. Hidden when Insights isn't configured or there are 0 plays.
    5. Only Plex video sessions are used. Audiobook listen_log is never touched.
  - **Files:** `internal/insights/repo.go`, `internal/insights/watchstats.go (new)`, `internal/httpapi/movies.go`, `internal/httpapi/series.go`, `internal/httpapi/server.go`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/SeriesDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A movie watched by three Plex users shows 'Watched by 3 · last played <relative time>', matching History
    - A show's line counts plays across all its episodes
    - Without Plex or Insights configured, nothing appears and nothing errors
  - **Tests:** Go (insights) TestWatchedByAggregation: rating_key match plus the title/year fallback, imported and live rows, two users.; Go (insights) TestWatchedByShowUsesGrandparent.; UI check: connected and disconnected.
  - **Depends on:** [PLEX-10](#plex-10)
  - **Risk:** Low. Title and year fallback can over-match same-titled remakes in the rare case the rating key is unknown, so the rating_key branch is preferred whenever the index knows it.
  - **Resolves:** movies-9
<a id="plex-13"></a>
- [ ] **PLEX-13 · Plex-native naming: {tmdbid}/{imdbid}/{tvdbid} tokens, drop empty {edition-…} groups, 'Plex recommended' preset** — `P2` · `M` · Phase 10
  - **Problem:** There are no id tokens, so Plex's recommended `Title (Year) {tmdb-12345}` folders can't be produced, and those ids are what make Plex match the right item (and make PLEX-10 links reliable). A `{edition-{edition}}` template already renders Plex editions, but leaves a stray `{edition-}` when a release has no edition, because renderName (library/importer.go:519) only strips () and []. There is no preset.
  - **Approach:** 1. importer.go renderName: after token substitution, remove empty Plex groups with `regexp.MustCompile(`\{(edition|tmdb|imdb|tvdb)-\}`)` before the existing cleanup.
    2. Movies:
       - Add `type MovieIDs struct{TMDB int; IMDB string}` and change the signature to `MovieTarget(title string, year int, ids MovieIDs, qualitySource, ext string)`. movieParts adds the tokens tmdbid ("" when 0) and imdbid.
       - Update the callers: movies/service.go:1133, 1196 and 1209 pass m.TMDBID and m.IMDBID; ImportAs (importer.go:1087-1096) takes ids.
       - The automatic import path: extend TitleResolver (library/manager.go:26) to return the ids, i.e. `ResolveMovie(ctx, releaseName) (title string, year int, ids MovieIDs, ok bool)`. movieTitleResolver (main.go:88) returns the matched movie's ids.
    3. Series: episodeTargetIn (importer.go:954) gains an ids map, used only when deriving a NEW series folder (tmdbid, tvdbid, imdbid). The coordinator and importer pass the series' ids from seriesSvc. An existing folder is still reused as is.
    4. Settings.tsx:
       - Add the tokens to TOKENS and SERIES_TOKENS, with ids in the samples. renderWith mirrors the empty-group removal.
       - A 'Plex recommended' button fills: movie folder `{title} ({year}) {tmdb-{tmdbid}}`; movie file `{title} ({year}) {edition-{edition}} - {quality}`; series folder `{title} ({year}) {tvdb-{tvdbid}}`, falling back to tmdb when there is no tvdb id.
    5. A UI note: 'Existing folders only change when you rename them.'
  - **Files:** `internal/library/importer.go`, `internal/library/manager.go`, `internal/library/importer_test.go`, `internal/movies/service.go`, `internal/automation/series.go`, `internal/automation/series_interactive.go`, `cmd/arrmada/main.go`, `web/src/pages/Settings.tsx`
  - **Acceptance:**
    - New movie imports with the preset land in 'Title (Year) {tmdb-123}' folders
    - A release without an edition produces no stray '{edition-}'
    - The Settings preview matches the backend output for the sample
    - Existing series keep their on-disk folder
  - **Tests:** Go TestRenderNameDropsEmptyPlexGroups.; Go TestMovieTargetTMDBToken.; Go TestEpisodeFolderTVDBTokenFallsBackToTMDB.; Go TestTitleResolverReturnsIDs (movieTitleResolver).
  - **Depends on:** MOV — editions as a Must condition / {edition} source (movies-2) and the rename preview, so the {edition} token stays consistent (soft)
  - **Risk:** MovieTarget and TitleResolver signatures change across packages, so expect small merge conflicts with MOV and ACQ work. After a preset switch, PLEX-04 auto-mapping is unaffected because it works on roots, not folder names.
  - **Resolves:** system-14

#### Milestone: M5 — One Plex page and linked accounts

_All Plex setup lives on one page. Local accounts, the admin included, can link Plex. The owner's Plex sign-in reaches their own account under an explicit staff policy, and Tautulli imports run as tracked jobs with retry and undo._

<a id="plex-14"></a>
- [x] **PLEX-14 · Link a Plex account to an existing local account (and block silent staff sign-in through Plex)** — `P2` · `M` · Phase 7
  - **Problem:** FindOrCreatePlexUser (auth/service.go:112-149) matches only on plex_id and otherwise creates a requester. plex_id is written nowhere else, apart from the Overseerr import, and there is no link endpoint or UI. The local admin and manually created family accounts get no personalized 'Recommended for you' rows, because PlexIDForUser feeds discover_recommended.go:149-151 and discover_rows.go:128. Once linking exists, FindOrCreatePlexUser would also hand a linked admin row to anyone who signs in with that Plex account, silently creating a Plex-to-admin path.
  - **Approach:** 1. New migration (≥0090) adds users.plex_username TEXT NOT NULL DEFAULT ''.
    2. auth.Service:
       - `LinkPlex(ctx, userID int64, plexID, plexUsername string) error`. It maps the idx_users_plex_id unique violation to `ErrPlexAlreadyLinked{UserID, Username}`.
       - `UnlinkPlex(ctx, userID)`, which sets plex_id NULL and plex_username ''.
       - FindOrCreatePlexUser stores plex_username on insert.
    3. Endpoints (protected, acting on the current user):
       - POST /api/v1/me/plex/link starts a PIN (plex.RequestPIN + AuthURL with forwardUrl, as in [PLEX-06](#plex-06)).
       - GET /api/v1/me/plex/link/{pin} polls, then calls GetAccount and LinkPlex. It returns {linked, plex_username} or 409 {already_linked_to}.
       - DELETE /api/v1/me/plex/link.
       - Admin: PUT /api/v1/users/{id} accepts {plex_unlink:true}, and the users list returns plex_username.
    4. Staff guard: in handlePlexLoginPoll (auth_plex.go:56), if the returned user's role is admin or manager, refuse with 403. The message reads 'This Plex account is linked to a staff account — sign in with your password.' [PLEX-15](#plex-15) makes this configurable.
    5. UI:
       - A 'Plex account: Link / Linked as <name> · Unlink' item in the Sidebar user footer (staff) and the UserLayout menu (requesters), using usePlexPinSignIn from [PLEX-06](#plex-06). It moves to APP's Me tab when that exists.
       - Settings → Users shows the linked Plex name.
       - On a conflict, the UI shows 'Already linked to <username>'. For admins it adds a 'Move link here' action, which is [PLEX-15](#plex-15)'s merge.
  - **Files:** `internal/store/migrations/ (new 009x_user_plex_username.sql)`, `internal/auth/service.go`, `internal/auth/service_test.go`, `internal/httpapi/auth_plex.go`, `internal/httpapi/server.go`, `internal/httpapi/users.go`, `web/src/components/Sidebar.tsx`, `web/src/components/UserLayout.tsx`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - The local admin can link their Plex account, and their Discover 'Recommended for you' row then uses their Plex watch history
    - Linking a Plex account already linked elsewhere shows which account has it
    - Unlink clears plex_id, and the next Plex sign-in for that Plex account creates or uses the requester path as before
    - A linked admin account cannot be entered through Sign in with Plex (403 with a clear message)
  - **Tests:** Go (auth) TestLinkPlexSetsPlexID, TestLinkPlexConflict, TestUnlinkPlex.; Go (httpapi) TestMePlexLinkFlow: pending returns pending, and once authorized plex_id and plex_username are stored (fake plex.tv via the plexTVBase var from PLEX-07).; Go (httpapi) TestPlexLoginRefusesLinkedStaff.
  - **Depends on:** [PLEX-06](#plex-06), [PLEX-07](#plex-07), APP — Me/Account tab will host the link row when it exists (soft)
  - **Risk:** Linking changes who personalised rows belong to, but grants no rights, because staff Plex sign-in is refused. The unique index already prevents double links.
  - **Resolves:** insights-14, system-11
<a id="plex-15"></a>
- [x] **PLEX-15 · Owner and staff Plex sign-in policy, and merging a duplicate Plex requester into the linked account** — `P2` · `M` · Phase 7
  - **Problem:** If the owner taps Sign in with Plex, they pass HasServerAccess and get a separate requester account with no admin rights. Their requests, inbox and push devices then split across two accounts. The Overseerr import also creates Plex-linked requester rows, so the owner's plex_id may already sit on a requester, which blocks PLEX-14's LinkPlex with a conflict. There is no way to choose whether staff may sign in through Plex.
  - **Approach:** 1. New setting `plex_signin_staff` (default false), shown on the Plex page with a clear warning: 'Anyone who controls this Plex account gets admin.'
       - When on, Plex sign-in into a linked staff account starts that session.
       - When off, [PLEX-14](#plex-14)'s 403 applies.
    2. Owner detection in handlePlexLoginPoll, before FindOrCreatePlexUser: `plex.OwnedServerID(ctx, clientID, userToken) == serverID` means this Plex account owns the server. If no user holds this plex_id:
       - setting on and exactly one admin without a Plex link → link that admin (LinkPlex) and start the admin session
       - otherwise → 409 'This Plex account owns the server — sign in with your password and link Plex from your account menu.'
       - The owner is never given a new requester account.
    3. Merge (admin only): POST /api/v1/users/{targetID}/plex/merge {from_user_id}. It applies only when from_user holds the plex_id and is a requester. In ONE transaction it:
       - moves requests.requested_by, book_requests.requested_by (and requested_by_name), request_subscribers, user_notifications (ignoring duplicate refs) and push_subscriptions
       - moves audiobook-server rows (0084/0085 tables) only when the target has none of that kind, keeping the target's audio password on conflict
       - moves plex_id and plex_username onto the target
       - deletes the duplicate user
       A preview GET returns counts per category, with audiobook data shown only as 'has audiobook progress: yes/no' and never titles.
    4. UI: in Settings → Users and in [PLEX-14](#plex-14)'s 'Move link here' flow, a confirm dialog lists exactly what moves, e.g. '12 requests, 3 push devices, audiobook progress', and recommends a backup.
  - **Files:** `internal/auth/service.go`, `internal/auth/merge.go (new)`, `internal/auth/merge_test.go (new)`, `internal/httpapi/auth_plex.go`, `internal/httpapi/users.go`, `internal/httpapi/server.go`, `internal/plex/account.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With plex_signin_staff on and exactly one unlinked admin, the owner tapping Sign in with Plex lands in the admin account
    - With it off, or with several unlinked admins, the owner gets a 409 with instructions, and no requester account is created
    - An admin can merge a duplicate Plex requester into their account, and its requests then show under the admin
    - The merge is all-or-nothing: an injected failure leaves both accounts untouched
  - **Tests:** Go (httpapi) TestPlexLoginOwnerMapsToAdminWhenAllowed (fake plex.tv where the user token owns the server).; Go (httpapi) TestPlexLoginOwnerAmbiguousAdmins409 and TestPlexLoginOwnerSettingOff409.; Go (auth) TestMergePlexDuplicateMovesRequests (transactional; request_subscribers and user_notifications de-duplicated).; Go (auth) TestMergeRollsBackOnError.
  - **Depends on:** [PLEX-14](#plex-14), SEC — coordinate the staff-sign-in policy with deny-by-default auth work (soft), SAFE — backup before merge (soft)
  - **Risk:** Auto-linking the owner to admin grants admin through Plex sign-in. It is gated on proof of server ownership, exactly one unlinked admin, and an opt-in setting that is off by default. The merge touches many tables, so it runs in one transaction with a preview.
  - **Resolves:** insights-14, system-11
<a id="plex-16"></a>
- [x] **PLEX-16 · Tautulli import as a tracked job: progress bar, summary, retry and undo** — `P2` · `M` · Phase 7
  - **Problem:** handleImportTautulli (httpapi/import_tautulli.go:42-75) returns 202 'started', then runs a goroutine with a 30-minute limit. The counts and any error go only to the server log. The UI only says 'importing … in the background', so the owner can't tell whether the import finished, how many plays came in, or whether it timed out, and there's no way to undo a bad run.
  - **Approach:** 1. New migration (≥0090):
       - insights_import_runs(id INTEGER PK, source TEXT, started_at, finished_at, status TEXT /*running|done|failed|timeout|interrupted*/, total, processed, imported, duplicates, overlaps, invalid, failed INTEGER, error TEXT, cutoff_at INTEGER)
       - `ALTER TABLE stream_sessions ADD COLUMN import_run_id INTEGER NOT NULL DEFAULT 0` plus an index on it
    2. tautulli.Client.History passes `recordsFiltered` to the callback as the total.
    3. The handler stores tautulli_url and tautulli_api_key in settings (entered in the UI; the key is masked on read as api_key_set). It creates a run row and returns {run_id}.
       - The goroutine updates processed and counts after each 500-row page, using ImportCounts from [PLEX-02](#plex-02).
       - It ends with done, failed or timeout plus the error text.
       - ImportOptions gains RunID, stamped onto inserted rows.
    4. On boot (main.go, after migrations), mark any 'running' row as interrupted.
    5. Endpoints (RoleAdmin):
       - GET /api/v1/insights/import/runs (latest 5)
       - POST /api/v1/insights/import/runs/{id}/retry, which re-runs from saved settings; the idempotency check prevents duplicates
       - DELETE /api/v1/insights/import/runs/{id}/rows, which deletes that run's rows and their buffer_events after a confirm that shows the count
    6. UI (the Tautulli section; [PLEX-17](#plex-17) moves it to the Plex page):
       - a progress bar (processed/total), polled every 2 s while running
       - a summary such as 'Imported 12,340 · 210 already there · 980 recorded live · 3 invalid · 0 failed'
       - the 'Only import plays before <first live play>' option from [PLEX-02](#plex-02)
       - Retry and 'Remove this import' buttons
       Rows imported before this change have import_run_id 0 and are covered by [PLEX-02](#plex-02)'s overlap repair.
  - **Files:** `internal/store/migrations/ (new 009x_insights_import_runs.sql)`, `internal/httpapi/import_tautulli.go`, `internal/insights/import.go`, `internal/insights/repo.go`, `internal/insights/import_test.go`, `internal/tautulli/client.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Starting an import shows a live progress bar that reaches 100%, then a final summary with every count
    - A timed-out or failed run shows its status and error, and Retry completes it without duplicates
    - 'Remove this import' deletes exactly that run's rows after confirmation
    - Restarting Arrmada mid-import marks the run 'interrupted' instead of leaving it 'running' forever
    - The Tautulli API key is never returned by any endpoint
  - **Tests:** Go TestImportRunProgress: a fake Tautulli with 1,200 rows over 3 pages gives total=1200, processed=1200, and the right imported and duplicate counts.; Go TestImportRunUndoDeletesOnlyThatRun (and its buffer_events).; Go TestInterruptedRunsMarkedOnBoot.; Go TestTautulliKeyMasked.
  - **Depends on:** [PLEX-02](#plex-02), SAFE — backup before undo (soft)
  - **Risk:** Storing the Tautulli key in settings is consistent with the UI-entered credentials rule, but it must be masked in every response. Undo deletes rows, so it requires confirmation.
  - **Resolves:** insights-13
<a id="plex-17"></a>
- [ ] **PLEX-17 · One Plex settings page: connection, monitoring, family sign-in, library updates, linked accounts, Tautulli import** — `P2` · `M` · Phase 9
  - **Problem:** Plex setup is split three ways. The connection lives under Insights → Settings (Insights.tsx:923-1042). The 'Plex sign-in' toggles (Settings.tsx:166-177) and the Tautulli import (Settings.tsx:816-846) live under Settings → System. The copy points between the screens, e.g. auth_plex.go: 'connect your Plex server in Insights first'.
  - **Approach:** 1. New web/src/pages/settings/PlexIntegration.tsx, built from the existing Section, Toggle and Field components with no new visual style. Sections:
       - (a) Connection: the Sign in with Plex hook ([PLEX-06](#plex-06)), the owned-server picker ([PLEX-07](#plex-07)), URL and token (masked), Test, 'Connected to <server> · Plex <version>', and the four-state status ([PLEX-08](#plex-08)).
       - (b) Monitoring: enable and poll interval.
       - (c) Family sign-in: the two toggles moved from Settings → System, plus plex_signin_staff ([PLEX-15](#plex-15)) with its warning.
       - (d) Library updates: the scan toggle, mapping rows, Test, Scan now and last scan ([PLEX-04](#plex-04)).
       - (e) Linked accounts: who is linked to which Plex name, with an admin Unlink or Merge ([PLEX-14](#plex-14)/15).
       - (f) Import from Tautulli: the job UI ([PLEX-16](#plex-16)) and the overlap repair ([PLEX-02](#plex-02)).
    2. Mount it as Settings → Connections → Plex if CFG's Settings hub exists, otherwise as a new 'Plex' tab in Settings at a stable route (e.g. /settings/plex).
    3. Remove the Insights 'Settings' tab and put a 'Plex settings' link in the Insights header. Remove the moved sections from Settings → System. Update [PLEX-06](#plex-06)'s admin forwardUrl to the new route.
    4. Fix every cross-reference in copy: auth_plex.go:80, the Settings.tsx:168 subtitle, and the Convert hint from [PLEX-08](#plex-08).
    5. The API endpoints stay unchanged.
  - **Files:** `web/src/pages/settings/PlexIntegration.tsx (new)`, `web/src/pages/Settings.tsx`, `web/src/pages/Insights.tsx`, `web/src/App.tsx`, `internal/httpapi/auth_plex.go`, `internal/httpapi/insights.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Everything Plex-related (connect, monitor, family sign-in, scan hook, account links, Tautulli import) is configured on one page
    - No copy anywhere says 'in Insights' or 'in Settings → System' about Plex
    - Insights has no Settings tab, and its header links to the Plex page
    - A fresh Plex setup can be completed from the new page alone
  - **Tests:** UI check: complete a fresh Plex setup from the new page on a dev DB.; grep: no remaining 'connect your Plex server in Insights' strings in internal/ or web/src.; UI check at 375 px: the page has no horizontal scroll.
  - **Depends on:** [PLEX-04](#plex-04), [PLEX-07](#plex-07), [PLEX-08](#plex-08), [PLEX-14](#plex-14), [PLEX-16](#plex-16), CFG — Settings hub with a Connections section, if it is being built (soft)
  - **Risk:** This is UI reshuffling over unchanged endpoints. The risk is a stale link or forwardUrl, which the grep and the PLEX-06 redirect test catch.
  - **Resolves:** insights-13

#### Milestone: M6 — Insights you can navigate

_Insights is a routed, phone-friendly Tautulli replacement. It has People pages, History with posters, filters, grouping and CSV, show posters and art, Graphs by plays or hours, and a Library tab with never-watched titles and transcode hot spots. Alerts lives on its own page._

<a id="plex-18"></a>
- [x] **PLEX-18 · Insights tabs in the URL, a phone-friendly layout, and Alerts moved to its own page** — `P2` · `S` · Phase 7
  - **Problem:** The active tab is local state (Insights.tsx:20), so a refresh or Back resets it and nothing can deep-link. The 7-tab bar (line 41) is a non-wrapping flex row about 650 px wide. The Activity grid uses minmax(360px,1fr) (line 177) inside a px-4 container, so on a 375 px phone the main pane scrolls sideways. Notifications (the admin Apprise alerts) sits as a tab inside Plex monitoring, although it is an app-wide feature.
  - **Approach:** 1. Routes: /insights/:tab? (default activity), with /insights/people/:id reserved for [PLEX-21](#plex-21). In App.tsx, map `/insights/*` and read the tab with useParams. Tab buttons use navigate(), and the Users tab is relabelled 'People'.
    2. Alerts split: move NotificationsView (Insights.tsx ~834) unchanged into web/src/pages/Alerts.tsx at /alerts, add an admin sidebar entry 'Alerts', and redirect /notifications and /insights/notifications to /alerts. OBS rebuilds the content later.
    3. Tab bar: `overflow-x-auto whitespace-nowrap` with a hidden scrollbar and scroll-snap. Scroll the active tab into view and keep the current underline style.
    4. Grids: Activity uses `repeat(auto-fill, minmax(min(360px,100%),1fr))`. The 300 px HomeExtras and Reliability grids (lines 225, 723) and the 320 px Graphs grid (line 599) get the same min() guard.
    5. Check that the History and Users tables scroll inside their own overflow-x-auto wrapper, not the page.
  - **Files:** `web/src/pages/Insights.tsx`, `web/src/pages/Alerts.tsx (new)`, `web/src/App.tsx`, `web/src/components/Sidebar.tsx`
  - **Acceptance:**
    - At 375 px width, no Insights tab causes page-level horizontal scrolling
    - All tabs can be reached by swiping the tab bar
    - Refreshing /insights/history stays on History, and Back returns to the previous tab
    - /alerts shows the notification connections; /notifications and /insights/notifications redirect there, and Insights has no Notifications tab
  - **Tests:** UI check at 375×812: on every tab, document.scrollingElement.scrollWidth <= clientWidth.; UI check: deep-link /insights/graphs directly.; UI check: /notifications lands on /alerts.
  - **Depends on:** FE — if the createBrowserRouter overhaul lands first, express these as nested routes there (soft), OBS — owns the Alerts page content (event catalog, presets, delivery queue: insights-6/7) (soft)
  - **Risk:** Low. Route changes must keep the existing /notifications redirect working for bookmarked links.
  - **Resolves:** insights-12, insights-10
<a id="plex-19"></a>
- [x] **PLEX-19 · Replace 'Coming soon' with real setup and empty states; never hide database tabs or stats behind Plex errors** — `P2` · `S` · Phase 7
  - **Problem:** ComingSoon (Insights.tsx:901-918) shows a 'Coming soon' pill for finished tabs when Plex isn't configured. History, Users, Graphs and Reliability return it when !connected, even though they read only the DB, so imported Tautulli history is invisible before Plex is configured. When the live Activity call fails, the whole tab, including the DB-backed HomeExtras stats, is replaced by the error (line 154). The header always says 'Connect your server in Settings to begin' (line 33). The comments at Insights.tsx:5-7 and service.go:1-4 are stale.
  - **Approach:** 1. Replace ComingSoon with `SetupState`. It keeps the NEXT description, has no pill, and shows a 'Connect your Plex server →' button (linking to the Plex settings route) only when status=unconfigured.
    2. History, Users, Graphs and Reliability always query. They show SetupState only when unconfigured AND the result is empty; otherwise they show the data, with [PLEX-08](#plex-08)'s banner when monitoring is off.
    3. ActivityView: on error, render the error card in place of the live stream grid only, and still render HomeExtras below it. HomeExtras' Plex-live parts (libraries, recently added) show their own inline 'Plex unreachable' note.
    4. Header copy depends on status: unconfigured shows 'Connect your server to begin'; otherwise nothing extra.
    5. Fix the stale comments in Insights.tsx:5-7 and the package doc at internal/insights/service.go:1-4.
  - **Files:** `web/src/pages/Insights.tsx`, `internal/insights/service.go`
  - **Acceptance:**
    - No 'Coming soon' text remains anywhere in Insights
    - With Plex unconfigured but Tautulli history imported, History, People and Graphs show that data
    - With Plex stopped, the Activity tab shows the live-stream error and still shows the watch-statistics cards
  - **Tests:** UI check: unset the Plex URL on a dev DB with sessions; History still lists rows.; UI check: stop Plex; the Activity error appears above HomeExtras, which still renders.; grep: no 'Coming soon' in web/src/pages/Insights.tsx.
  - **Depends on:** [PLEX-08](#plex-08), [PLEX-18](#plex-18)
  - **Risk:** Low.
  - **Resolves:** insights-11
<a id="plex-20"></a>
- [ ] **PLEX-20 · Show posters and background art: record grandparentThumb, grandparentRatingKey and art for episodes** — `P3` · `S` · Phase 17
  - **Problem:** rawSession parses only `thumb` (plex/sessions.go:130), which for an episode is the landscape still, so Activity cards and Most watched TV squeeze stills into portrait slots. RecentlyAdded (client.go:152) already prefers grandparentThumb. Most watched TV uses MAX(thumb), mixing imported show posters with live episode stills. Without grandparent_rating_key, shows can't be matched reliably by later features (PLEX-12, PLEX-24).
  - **Approach:** 1. rawSession adds grandparentThumb, grandparentRatingKey, art and grandparentArt. Session gains ShowThumb, ShowRatingKey and Art.
    2. New migration (≥0090): stream_sessions ADD grandparent_thumb and grandparent_rating_key (TEXT NOT NULL DEFAULT ''), plus an index on grandparent_rating_key.
       - The recorder (poller.go record()) writes them.
       - sessionRecord, HistoryRow and historyCols gain the fields.
       - The Tautulli import maps get_history grandparent_thumb and grandparent_rating_key; add both to tautulli.Row and ImportedSession.
    3. Activity Stream JSON: `thumb` is the show poster for episodes, plus new `still` and `art` (all through proxyImage). StreamCard gets a subtle art background under a dark gradient built from the existing tokens, so the palette is unchanged.
    4. topTitles for episodes selects `COALESCE(NULLIF(MAX(grandparent_thumb),''), MAX(thumb))`. History rows use grandparent_thumb for episodes.
  - **Files:** `internal/plex/sessions.go`, `internal/insights/activity.go`, `internal/insights/poller.go`, `internal/insights/repo.go`, `internal/insights/stats.go`, `internal/insights/import.go`, `internal/tautulli/client.go`, `internal/httpapi/import_tautulli.go`, `internal/store/migrations/ (new 009x_session_grandparent.sql)`, `web/src/pages/Insights.tsx`
  - **Acceptance:**
    - Activity cards for TV show the show poster, with the episode still or art as background
    - Most watched TV shows portrait show posters for live-recorded shows
    - New live and imported episode rows store grandparent_rating_key
  - **Tests:** Go (plex) TestFlattenGrandparentThumb.; Go (insights) TestTopShowsPreferGrandparentThumb.; Go (insights) TestImportMapsGrandparentRatingKey.
  - **Risk:** Low. Old rows keep the episode still until they age out of the stats window.
  - **Resolves:** insights-10
<a id="plex-21"></a>
- [ ] **PLEX-21 · People page per Plex user (avatar, totals, top titles, devices, IPs, completion, stalls), with names linked everywhere** — `P2` · `M` · Phase 17
  - **Problem:** Users table rows can't be clicked (Insights.tsx:296-322), and UserEntry (stats.go:218-229) drops the thumb even though users() selects u.thumb. 'Most active users' and 'Top users' link nowhere, despite topNames' comment about drill-down links. Questions like 'what has Mum been watching' or 'which device keeps transcoding' take manual searching.
  - **Approach:** 1. New internal/insights/people.go: `Service.Person(ctx, userID string, windowDays int) (PersonProfile, error)`. PersonProfile is:
       - {ID, Username, AvatarURL, FirstSeen, LastSeen, TotalPlays, TotalSecs (watchedSum)}
       - CompletionPct: rows with duration_ms>0 and view_offset ≥90% of duration, over rows with duration_ms>0
       - TopTitles []TitleStat (movie title, or show grandparent_title, with posters)
       - Platforms and Players []NameStat
       - IPs []{ip, geo, plays, last_seen}
       - Decisions {direct_play, direct_stream, transcode}
       - Stalls {events, stall_ms, rate_pct} from buffer_events joined on session_id
       - Recent []HistoryEntry (10, through toHistoryEntry from [PLEX-01](#plex-01))
       All queries filter by user_id using idx_stream_sessions_user.
    2. Avatar: plex_users.thumb is an absolute plex.tv URL. Return it only if it starts with https://plex.tv/, otherwise ''. UserEntry gains avatar_url. If SEC later adds a CSP img-src, switch to a proxied endpoint.
    3. Route GET /api/v1/insights/users/{id} (RoleManager, like the other insights routes).
    4. Frontend: new web/src/pages/InsightsPerson.tsx at /insights/people/:id, in the current card style:
       - an avatar header with stat tiles
       - a top-titles poster row
       - a devices and IPs table
       - recent plays, with an 'All plays' link to /insights/history?user=<id>
       User names link to it everywhere: People table rows (with avatars), History rows, Most active users, Top users chart labels, Activity cards, Reliability boards, and [PLEX-12](#plex-12)'s 'Watched by' list.
    5. Plex video sessions only. No audiobook or listen data appears.
  - **Files:** `internal/insights/people.go (new)`, `internal/insights/people_test.go (new)`, `internal/insights/stats.go`, `internal/httpapi/insights.go`, `internal/httpapi/server.go`, `web/src/pages/Insights.tsx`, `web/src/pages/InsightsPerson.tsx (new)`, `web/src/App.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Clicking any user name in Insights opens their People page, with avatar, totals matching the People tab, top titles, devices, IPs and stall stats
    - 'All plays' opens History filtered to that user
    - No audiobook listening data appears
    - The page has no horizontal scroll at 375 px
  - **Tests:** Go (insights) TestPersonProfile: seed live and imported rows plus buffer events for two users; assert totals, completion %, top-titles order, IP grouping and stall rate for one user only.; Go (insights) TestAvatarURLAllowlist.; UI check: navigate from a History row's user name to the profile and back.
  - **Depends on:** [PLEX-18](#plex-18), [PLEX-01](#plex-01)
  - **Risk:** Low. These are read-only queries on indexed columns. Watch query cost on large histories.
  - **Resolves:** insights-10
<a id="plex-22"></a>
- [ ] **PLEX-22 · History upgrades: posters, user and date filters, watched/partial badge, grouped resumes, CSV export** — `P2` · `M` · Phase 17
  - **Problem:** History never renders thumb_url or progress_pct, though both are computed (activity.go). HistoryFilter.UserID exists in the backend (repo.go:176), but api.insightsHistory (api.ts:1588) has no user parameter, and there is no date range. Each resume of the same title is a separate row, and there is no export.
  - **Approach:** 1. HistoryFilter gains From and To (epoch) and Group bool. historyWhere adds started_at bounds. handleInsightsHistory (insights.go:178) reads user, from, to and group.
    2. Grouping (group=1) is a window-function query:
       - LAG(stopped_at) OVER (PARTITION BY user_id, rating_key ORDER BY started_at), starting a new group when the gap is over 3 h
       - an outer GROUP BY user_id, rating_key, grp selecting MIN(started_at), MAX(stopped_at), SUM(watchedExpr), MAX(view_offset_ms), COUNT(*) AS sessions and MAX(id) for the detail row
       - ORDER BY start DESC with LIMIT and OFFSET; the total is COUNT(*) over the grouped CTE
       modernc SQLite supports window functions; confirm in a test.
    3. GET /api/v1/insights/history.csv takes the same filters, is capped at 50k rows, and uses encoding/csv. Columns: when, user, title, show, S/E, player, platform, ip, decision, watched_secs, progress_pct.
    4. HistoryView:
       - a small poster column (thumb_url, 2:3, loading=lazy; the show poster after [PLEX-20](#plex-20))
       - a user select fed from /insights/users
       - date chips (7d / 30d / 90d / all) plus custom from/to
       - a 'Group resumed plays' toggle, with an 'n sessions' chip on grouped rows
       - a badge: 'Watched' when progress_pct ≥ 90, 'Partial nn%' otherwise, hidden when the duration is unknown
       - an 'Export CSV' button
       - all filters kept in the query string
  - **Files:** `internal/insights/repo.go`, `internal/insights/activity.go`, `internal/insights/history_test.go (new)`, `internal/httpapi/insights.go`, `internal/httpapi/server.go`, `web/src/pages/Insights.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - History shows posters (an episode shows its show poster once PLEX-20 has landed)
    - Filtering by user and 'last 7 days' returns only those plays, and the count matches
    - With grouping on, a movie paused overnight and resumed the next morning stays two rows (gap over 3 h), while two sessions 20 minutes apart become one row with '2 sessions' and summed watch time
    - Export CSV downloads the filtered rows
    - Filters survive a page refresh
  - **Tests:** Go (insights) TestHistoryDateFilter.; Go (insights) TestHistoryGrouping: sessions 20 min apart merge, a 5 h gap doesn't, and the total counts groups.; Go (httpapi) TestHistoryCSV: header row and escaping of a title containing a comma.; Seeded 100k-row benchmark for the grouped query (should finish in under 200 ms).
  - **Depends on:** [PLEX-18](#plex-18), [PLEX-01](#plex-01)
  - **Risk:** The window-function query must keep the paging total consistent, and its speed must be tested on a large seeded DB.
  - **Resolves:** insights-10
<a id="plex-23"></a>
- [ ] **PLEX-23 · Graphs: plays/hours toggle, stream type over time, and a time axis on bandwidth** — `P3` · `S` · Phase 17
  - **Problem:** Graphs only count plays, with no watch-time view. There is no chart of direct play vs transcode over time. The bandwidth chart passes empty x labels (Insights.tsx:612), so it has no time axis even though BWPoint.T carries the hour.
  - **Approach:** 1. Change the signature to `Service.Graphs(ctx, windowDays int, metric string)`, where metric is plays or duration. The daily, day-of-week, hour, platform and user aggregates (graphs.go:30-100, scanBuckets) use COUNT(*) or watchedSum/3600 accordingly.
    2. Add DailyDirectPlay, DailyDirectStream and DailyTranscode series aligned to Days.
    3. Frontend:
       - 'Plays | Hours' chips next to the window chips
       - a new 'Stream type over time' line chart using the existing DECISION colours
       - bandwidth xLabels from p.t: the date at day boundaries, '' otherwise
    4. Keep the metric in the query string.
  - **Files:** `internal/insights/graphs.go`, `internal/insights/graphs_test.go (new)`, `internal/httpapi/insights.go`, `web/src/pages/Insights.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Switching to Hours changes every Graphs chart to watch hours, and per-user totals equal the People tab hours for the window
    - The stream-type chart lines sum to the daily play count
    - The bandwidth chart shows date labels
  - **Tests:** Go (insights) TestGraphsDurationMetric (seeded rows with watched_ms).; Go (insights) TestGraphsDecisionSeriesAligned.
  - **Risk:** Low.
  - **Resolves:** insights-10
<a id="plex-24"></a>
- [ ] **PLEX-24 · Insights Library tab: most watched, never watched, and transcode hot spots linked to Arrmada and Convert** — `P3` · `M` · Phase 17
  - **Problem:** There is no library-level view. The owner can't see what has never been watched (cleanup candidates), or which titles keep forcing transcodes, which is exactly what Convert could fix.
  - **Approach:** 1. New internal/insights/library.go: `Service.Library(ctx, windowDays int) (LibraryView, error)`.
       - (a) MostWatched: movies and shows by plays and hours, reusing topTitles with limit 25.
       - (b) NeverWatched: from [PLEX-10](#plex-10)'s index Items(kind), items with AddedAt older than 30 days and no stream_sessions row by rating_key (movies) or grandparent_rating_key (shows, from [PLEX-20](#plex-20), falling back to grandparent_title). Sorted by addedAt ascending. When Plex is unreachable or the index is empty, it returns an `unavailable` flag that the UI shows as an inline note.
       - (c) TranscodeHotSpots: `GROUP BY rating_key` (or grandparent_rating_key for episodes) `WHERE decision='transcode'` within the window, with plays, the top video_src→video_stream pair, the top platforms, and the most common buffer cause.
    2. Map each item to Arrmada through index.TMDBForRatingKey, then movies or series lookup by TMDB id. Each item links to /movies/:id or /series/:id and to /convert?q=<title>. The page only links; it never queues a conversion (standing rule).
    3. GET /api/v1/insights/library?window=90 (RoleManager), and a 'Library' tab at /insights/library ([PLEX-18](#plex-18) routing) in the current card and table style.
  - **Files:** `internal/insights/library.go (new)`, `internal/insights/library_test.go (new)`, `internal/httpapi/insights.go`, `internal/httpapi/server.go`, `web/src/pages/Insights.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - The Library tab lists never-watched titles older than 30 days, with their date added
    - A title transcoded repeatedly appears in Hot spots with its codec reason and working links to its Arrmada detail page and to Convert
    - With Plex unreachable, Most watched and Hot spots still render (database only), and Never watched shows an inline note
    - Nothing on the page starts a conversion
  - **Tests:** Go (insights) TestTranscodeHotSpots: seed transcode and direct rows; assert grouping, order and the top codec pair.; Go (insights) TestNeverWatched: a fake index Items list plus seeded sessions, including the show grandparent fallback.
  - **Depends on:** [PLEX-10](#plex-10), [PLEX-18](#plex-18), [PLEX-20](#plex-20)
  - **Risk:** Large libraries are handled by the PLEX-10 index, which is paged and cached, so no extra Plex listing is needed. The never-queue-a-conversion rule is enforced by having only links.
  - **Resolves:** insights-10

#### Risks

- Several Plex behaviours are undocumented or unverified and must be confirmed once against the owner's PMS with read-only calls: the refresh method (GET vs PUT) and behaviour on a deleted path, the types of transcodeHwDecoding and transcodeHwEncoding, the guid formats in section listings (new vs legacy agents), and the owned and connections fields of plex.tv resources. Capture fixtures into internal/plex/testdata; never test-convert a real library file to produce them.
- Container path mismatch: if Arrmada's and Plex's paths differ and auto-guess fails, partial scans quietly fall back to whole-section refreshes. PLEX-04 shows the resolution and the last scan result so this is visible.
- Destructive data operations (PLEX-02 overlap removal, PLEX-16 import undo, PLEX-15 account merge) delete or move rows. Each shows exact counts, requires confirmation, runs in one transaction, and recommends a backup.
- Granting admin through Plex sign-in is a privilege path. PLEX-14 refuses staff Plex sign-in outright, and PLEX-15 only allows it behind an opt-in setting that is off by default and requires proof of server ownership.
- Migration numbering collides with other epics working in parallel (latest is 0089). Number each PLEX migration at implementation time, never hardcode 0090 in the plan, and keep each migration additive (ADD COLUMN / CREATE TABLE).
- PLEX-05 touches hot code owned by ACQ, MOV and CONV (coordinator import, movie handlers, Convert finalize). Keep each hook call a one-liner and land PLEX-05 promptly to limit conflicts.
- PLEX-03 adds a write transaction per poll and changes shutdown semantics. The race tests in Docker must cover the new transaction paths, and resume must re-check rating_key and user to keep the sessionKey-reuse protection.
- The library index (PLEX-10) on very large libraries costs a few seconds of Plex CPU per rebuild. It stays paged, in the background, single-flight and on a 30-minute TTL.
- Historical rows keep their old meanings (hw_transcode = requested; no grandparent fields). The UI copy notes the difference instead of rewriting history.

#### Out of scope

- The Alerts page content: event catalog across modules, Apprise presets, message templates, delivery queue with retries, admin Web Push, stream filters, buffering cooldown (insights-6, insights-7, owned by OBS). PLEX-18 only moves the tab to /alerts.
- Requester notifications that wait for Plex to have the item, and 'Watch on Plex' deep links in pushes and the inbox (REQ, using PLEX-10 and PLEX-04 hooks).
- A 'Recently added' row on Discover for requesters (the second half of discover-5, REQ/APP). The PLEX-10 index can feed it.
- Plex watchlist auto-requests.
- Subscribing to the Plex websocket (/:/websockets/notifications) instead of polling sessions.
- A Plex Pass 'stop stream with message' button on Activity cards.
- Bulk renaming existing folders into the Plex-recommended scheme (MOV/SER rename work). PLEX-13 affects new imports and per-title renames only.
- Plex scans for music (Plexamp) and audiobooks. music.imported carries no path and MUS is deciding the module's future. Books are not in Plex.
- Managing Plex Home users or library sharing from Arrmada.
- Any Insights view of audiobook listening content (standing privacy rule; admins see how much and when, never what).

