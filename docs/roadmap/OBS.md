# OBS — Health, attention feed & alerts

_Part of the [Arrmada roadmap](../../ROADMAP.md). 20 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Give the owner one trustworthy answer to "is everything working, and does anything need me?": on the Dashboard, as sidebar badges on every page, on a System → Status page (Health and Tasks), and as alerts on their phone. Every problem carries a link to where it gets fixed.

**Why.** Today the owner finds problems only when a family member asks where their show is.
- Nothing announces work that needs a person (ops-4, product-4). Review is a plain NavLink with no count (Sidebar.tsx:65-82). import.held is published (reviews.go:76), but only the websocket "*" broadcast consumes it. requests.Create publishes nothing. notify.Run listens to five topics only (notify.go:148-152): release.grabbed, movie.downloaded, series.imported, plex.stream.started and plex.buffering. So a pending request, a held import, a stuck import, a full disk or a dead client all happen silently.
- The health panel checks the wrong things, once (system-8, system-3, product-5).
  - The Dashboard fetches /health/system once on mount (Dashboard.tsx:30-39), on a page people leave open.
  - It probes ARRMADA_LIBRARY_DIR, the managed volume, instead of the folders the user picked. Its MkdirAll silently creates a missing mount point (health_system.go:48, 105-121).
  - It can't see a failing indexer, a dead second qBittorrent or a down FlareSolverr (integrations-1/3/13), a revoked Plex token, an invalid TMDB key, or failing background tasks.
  - Warnings are plain text with no link to the fix (frontend-10, product-9).
- The 28 scheduled tasks are invisible. scheduler.exec only logs (scheduler.go:90-97): no last run, next run, error or Run now, and no recover, so one panicking task kills the process (backend-9). The scheduler's own comment admits four jobs "sat dead" unnoticed.
- Admin alerts are hard to set up and fragile (insights-6, insights-7, product-8).
  - They live inside the Plex Insights page (Insights.tsx:822-899, /notifications → /insights).
  - Setup takes a raw Apprise URL with secrets shown in plain text, and it isn't validated on save.
  - fan() delivers serially inside the bus loop with a 20s timeout per send (notify.go:220-233), so a slow endpoint fills the 64-slot bus buffer and events get dropped.
  - There's no retry and no delivery status, and stream alerts fire for every autoplayed episode.
  - Convert never alerts at all (convert-11).

**Depends on:** SEC: the websocket needs topic filtering so staff-only topics (health.changed, attention.changed, task.finished, import.held) stop reaching requesters. Until then, OBS payloads stay content-free. SEC also owns manager access to Apprise URLs and secrets, and the generic-scheme SSRF policy ([OBS-16](#obs-16) coordinates).; INT: persisted per-indexer status with consecutive failures and failing-since (draft integrations.t1), needed by [OBS-15](#obs-15).; INT: FlareSolverr as a saved setting with Ping() (draft integrations.t9), needed by [OBS-15](#obs-15).; ACQ: queue Phase/stalled classification (draft ops.t4) makes the stalled counts precise in [OBS-07](#obs-07) and [OBS-08](#obs-08). Review reason codes (ops.t9) give better review titles. The stall fail-over publisher download.stalled (ops.t5) feeds the download.stalled alert in [OBS-12](#obs-12). All three degrade gracefully if absent.; PLEX: Insights poll-health counters (draft insights.t4) for [OBS-05](#obs-05) (optional). No duplicate stream-started after a restart (insights.t5) for [OBS-17](#obs-17). The secret-masking model (insights.t10) should match [OBS-16](#obs-16).; CONV: convert.done/failed/blocked/stalled bus events (draft convert.t16), needed by [OBS-18](#obs-18).; SAFE: nightly DB backups with a last-success time, for [OBS-15](#obs-15)'s backup-age check. The per-filesystem recycle bin (system.t17) retires [OBS-01](#obs-01)'s recycle-drive warning.; BE: the job runner and jobs table (draft backend.t18), needed by [OBS-20](#obs-20). Coordinate scheduler panic recovery and the TaskStatus shape with [OBS-03](#obs-03).; CFG: the Settings/System hub will host HealthList, TasksTable and AlertsSettings. Fix-link constants in internal/health/links.go must be repointed when routes move. app_public_url may move to CFG's General section.; FE: the shared live-query websocket hook (draft frontend.t32), so badges and the Status page don't each open their own socket. Also the component kit, if it has landed.; REQ: the /requests page becomes the link target for pending-request rows and badges ([OBS-07](#obs-07)/08), and issue reports become an attention source later.

#### Design

## Target architecture

### Principles
1. **Three signals, each with one source of truth.**
   - **Task state** (`internal/scheduler`): did each background job run, when, for how long, and did it fail.
   - **Health** (`internal/health`): is the machine and every integration working. This answers "why isn't anything downloading?". Checks run in the background on their own interval and timeout, and an HTTP GET never triggers a live call to qBittorrent, Plex, TMDB or a tracker.
   - **Attention** (`internal/attention`): work that needs a human decision or fix. That covers pending requests, held imports, errored or long-stalled downloads, imports that keep failing, wrong-category downloads, titles that never find a release, and every health problem.
     - It is **pull-based**: recomputed from the DB plus one queue read every 30s, and immediately on bus "kicks" (request.created, import.held, health.changed).
     - A dropped bus event (the bus drops when a 64-slot buffer is full) therefore delays an item by at most 30s and never loses it.
2. **Surfaces read signals and never compute them.** These are the Dashboard "Needs you" card, the sidebar badges, System → Status (Health and Tasks tabs) and Alerts.
3. **"Needs you" alerts are a persisted diff of attention state** (`attention_state`).
   - A restart doesn't resend, and the first deploy seeds without alerting.
   - Health problems alert on the transition and again on "Resolved".
   - Activity events (grabbed, imported) and Plex events stay bus-driven and fire-and-forget.
   - Disk-guard holds and Plex outages are health checks, so they alert through health.problem/resolved. There are no separate diskguard.* or plex.down topics.
4. **Every warning has a stable key and a fix link.** All link paths live in `internal/health/links.go`, so the CFG hub can repoint them in one place.
5. **Staff only.** Every new endpoint is RoleManager, and `/health/system` tightens from `protected` to RoleManager.
   - The realtime hub broadcasts every topic to every client (`realtime/hub.go Run`), so the new topics carry content-free payloads (counts, keys, task names) until SEC's topic filter lands. Clients re-fetch details from role-gated REST.
6. **Privacy.** Audiobook listening is never an event or an alert. Alerts may name an imported book, which is a library change and not a listen.

### Data model
Migration numbers: claim the next free number (≥0090) at implementation time, because other epics add migrations concurrently.
- `task_runs(name TEXT PK, last_start, last_end, duration_ms, last_error, last_error_at, runs, failures, consecutive_failures)`, with times as INTEGER unix.
- `notification_subscriptions(connection_id → notifications(id) ON DELETE CASCADE, event_key TEXT, PK(connection_id, event_key))`.
  - It is backfilled from on_grab, on_import, on_stream and on_buffering.
  - The old columns are kept for rollback and no longer read. foreign_keys is ON (store.go:35).
- `notifications.config TEXT NOT NULL DEFAULT '{}'` holds:
  - preset fields and secrets
  - the web-push user_id
  - stream filters
  - template overrides
- `attention_state(key TEXT PK, kind, level, title, first_seen, last_seen, alerted_at, resolved_at, misses)`.
- `notification_deliveries(id, connection_id, event_key, title, body, link, attach, status queued|sending|sent|failed, attempts, last_error, created_at, next_attempt_at, sent_at)`.
  - The Apprise URL is NOT copied here. It's resolved from the connection at send time, so secrets don't spread to a second table.

### Packages and APIs
- **scheduler**:
  - `Register(name, every, runAtStart, fn, opts ...Option)` with `Label`, `Description` and `Hidden` options.
  - `Snapshot() []TaskStatus`, `RunNow(name)` (returns ErrUnknownTask or ErrBusy), `SetStore(Store)` and `OnFinish(func(TaskStatus))`.
  - exec recovers panics, skips overlapping runs, and tracks NextRun.
- **health**:
  - `Registry{Register(Check), RunDue, RunAll, Results, Warnings}`.
  - `Check{Key, Name, Category, Interval (60s default), Timeout (3s default), Run func(ctx) []Finding}`.
  - `Finding{Key, Level warning|error, Message, Fix{Path, Label}}`.
  - Driven by the scheduler task `health-check` (30s).
- **attention**:
  - `Service{Refresh, Snapshot, Kick, Run}`. Providers receive a per-refresh `Frame` with the queue fetched once.
  - `Alerter.Diff(snapshot)` runs after each refresh.
  - Driven by the scheduler task `attention-refresh` (30s).
- **notify**:
  - `catalog.go` holds `EventDef{Key, Topic, Group, Label, Hint, DefaultOn, Format}`.
  - `Dispatch(ctx, key, Message{Title, Body, Link, Attach})`.
  - Run uses one subscription across all catalog topics.
  - `queue.go` is the delivery worker. `presets.go`, `filters.go` and the `Pusher` (Web Push) complete the package.
- **Endpoints:**
  - `GET /api/v1/health/system` (manager): cached, returns `{status, warnings[{key,level,message,link,link_label,since}], checks[], disk}`. `?refresh=1` re-runs the checks, rate-limited.
  - `GET /api/v1/system/tasks` and `POST /api/v1/system/tasks/{name}/run`.
  - `GET /api/v1/attention` returns `{at, stale, counts, groups[], items[]}`.
  - `GET /api/v1/notifications/catalog` and `GET /api/v1/notifications/{id}/deliveries`.
- **Bus topics:**
  - Added: `task.finished {name, ok, dur_ms}`, `health.changed {status, errors, warnings}`, `attention.changed {counts}`, `request.created`, `request.auto_approved`.
  - Consumed: `import.held` (enriched with the review id), `book.imported`, `music.imported` (enriched with names), `convert.*` (from CONV), `download.stalled` (from ACQ).

### Health vs attention boundary
| Lives in Health (system state) | Lives in Attention (work for a person) |
|---|---|
| library folders exist/writable, recycle drive, free space, disk guard holding | pending requests, held reviews |
| each download client reachable, Plex token/reachability, TMDB key, indexers failing, Prowlarr, FlareSolverr, audiobook server | errored/long-stalled torrents, imports in retry, wrong-category downloads |
| tasks with ≥3 consecutive failures, backup age | titles with no release after many searches, convert gave up (CONV) |
Attention includes every health warning as a `health:<key>` item, so the Dashboard card, the badges and the alerts read one list.

### UI (existing dark warm palette, terracotta accent and type scale)
- **Dashboard:**
  - A "Needs you" card at the very top, replacing today's plain warnings list. It shows grouped rows ("3 requests are waiting for approval → Review them"), and health rows with a "Fix →" link. When nothing is pending it shows a quiet "All clear" line.
  - The Downloading tile says when the client is unreachable and how many torrents errored.
- **Sidebar:**
  - Mono 10px count pills on Review, Downloads, Discover (pending requests, until REQ's /requests page) and System → Status (red for errors, amber for warnings).
  - A dot on the mobile hamburger in AppLayout.tsx when anything needs attention.
- **System → Status (`/system/status`):**
  - Health tab: checks by category with a level dot, message, checked-ago, Fix link and "Check now".
  - Tasks tab: label, interval, last run and duration, result chip, next run, Run now. Recent jobs are added later.
- **System → Alerts (`/alerts`, with `/notifications` redirecting):**
  - Connection cards built from preset forms, with masked secrets.
  - A grouped event checklist: Needs you / Library / Downloads / Plex / Convert.
  - A status dot and recent deliveries per connection, plus Test.
  - "This device (push)".
- The components (`HealthList`, `TasksTable`, `AlertsSettings`) are standalone, so CFG's Settings/System hub can mount them unchanged.

### Rollout
1. M1: quick truth fixes.
2. M2: task state, the health registry and the Status page.
3. M3: the attention feed, the Needs-you card and badges.
4. M4: the Alerts module (page, catalog, attention-driven alerts, delivery queue, admin push).
5. M5: checks that wait on INT and SAFE.
6. M6: alert polish (presets, stream filters, Convert, templates, recent jobs).

Every task runs `go vet` and `go test -race` in Docker before pushing, and every commit ends with the Co-Authored-By trailer.

#### Milestone: M1 — Health that tells the truth

_The health panel checks the Movies/TV/Books/Music/Downloads folders the owner actually picked, and never creates a missing mount. The Dashboard's warnings and Downloading tile update live and say when the client is down. The Review page refreshes itself._

<a id="obs-01"></a>
- [x] **OBS-01 · Health and disk-guard checks probe the folders the user picked, not ARRMADA_LIBRARY_DIR** — `P1` · `S` · Phase 2
  - **Problem:** handleSystemHealth probes a.deps.Config.LibraryDir (health_system.go:48), which is the managed arrmada-media volume. The banner can therefore say the library is writable while the real Movies share is read-only. Its writable() (health_system.go:105-121) calls os.MkdirAll first, which silently creates a missing mount point. The disk guard's 'same drive as your library' comparison (downloads.go:131-137) and startup.go's same-filesystem warning both compare Downloads against LibraryDir, so they report the wrong disk. The recycle dir defaults to <LibraryDir>/.recycle (main.go:203), so deletes from the array are copied across filesystems without any warning.
  - **Approach:** 1. Create package internal/health with folders.go only (the registry joins it in [OBS-04](#obs-04)):
       - `type Folder struct{Role, Label, Path string}`.
       - `func ProbeFolder(path string) FolderState{Exists, IsDir, ParentExists, Writable bool; Err error}`. The write probe is os.CreateTemp(dir, ".arrmada-write-check-") plus Remove. Move startup.go's writable() here and have startup.go call it. It must never MkdirAll.
       - `func SameFilesystem(a, b string) bool`, using the diskspace.Of identity trick that dashboard.go and downloads.go already use.
       - `func LibraryFolders(cfg config.Config, booksOn, musicOn bool) []Folder`:
         - Movies (cfg.MoviesDir) and TV (cfg.TVDir).
         - Ebooks and Audiobooks when books are enabled, deduped by path into 'Books & audiobooks' when they're the same folder.
         - Music when music is enabled.
         - Downloads.
         - cfg already holds the effective folders, because ApplySavedLibraryDirs (httpapi/setup.go:46) rewrites them at boot.
    2. health_system.go: replace the LibraryDir probe with a loop over LibraryFolders.
       - Missing folder whose parent exists and is writable → warning: 'The TV folder /storage/tv doesn't exist yet. Arrmada will create it on first import; if it should be an existing share, check the container's volume mapping.'
       - Missing folder whose parent is also missing → error: 'The Movies folder /x isn't there — the share isn't mounted.'
       - Path is not a directory → error.
       - Not writable → error: 'Arrmada can't write to the TV folder /y (check PUID/PGID and the share's permissions).'
       - Delete httpapi's writable().
    3. Recycle drive warning:
       - recyclebin.Service gains Dir() (it already holds s.dir).
       - If the recycle dir isn't on the same filesystem as Movies or TV, add a warning: 'Deleted files are copied to <dir> on a different drive, which is slow and fills that drive.'
       - Mark it with a TODO for removal when SAFE's per-filesystem recycle bin lands.
    4. downloads.go handleDiskGuardStatus:
       - Replace LibraryPath/SharedWithLibrary with `shared_with: [{role,label,path}]`, comparing Downloads against each library folder.
       - Keep `shared_with_library` (= len>0) for one release.
       - Settings.tsx DiskGuardSection shows 'Shares a drive with: Movies, TV'.
    5. cmd/arrmada/startup.go: run the same per-folder comparison for the 'downloads and library are on the same filesystem' log line.
  - **Files:** `internal/health/folders.go`, `internal/health/folders_test.go`, `internal/httpapi/health_system.go`, `internal/httpapi/health_system_test.go`, `internal/httpapi/downloads.go`, `internal/recyclebin/service.go`, `cmd/arrmada/startup.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Making the Movies folder read-only (in a scratch container, never the real library) produces a health error that names the Movies folder and its path.
    - A missing TV mount is reported, and the folder is NOT created by the health check.
    - With books or music turned off, their folders are not checked.
    - The disk guard panel lists which library folders share the downloads drive.
    - On an install whose recycle dir is on the managed volume, a 'different drive' warning appears.
  - **Tests:** Go TestProbeFolderDoesNotCreate: probing a missing temp path leaves it missing.; Go TestSystemHealthChecksEachLibraryFolder: temp dirs for Movies/TV/Downloads with TV missing (parent present) gives a warning naming TV; a missing parent gives an error.; Go TestFolderNotWritable: chmod 0555. Skip when os.Geteuid()==0, because CI's Docker race run is root.; Go TestLibraryFoldersRespectsModuleToggles, plus dedupe when ebooks == audiobooks.; Go TestDiskGuardSharedWithReportsRoots: two temp dirs on one filesystem → both listed.
  - **Risk:** Low. The probes run on every /health/system call until OBS-04 caches them, but CreateTemp plus Remove per folder is cheap. Permission tests must not rely on chmod when running as root.
  - **Resolves:** system-3, system-8
<a id="obs-02"></a>
- [x] **OBS-02 · Dashboard stays live: re-polled warnings, an honest Downloading tile, and a Review page that refreshes** — `P1` · `S` · Phase 2
  - **Problem:** Dashboard.tsx:30-39 fetches status, health and systemHealth once on mount, on a page its own comment says people leave open. dashboard.go:119 sets queue_note when the client can't be listed, and api.ts:386 types it, but nothing renders it. A down qBittorrent therefore shows '0 · 0 seeding'. The Downloading tile turns amber on errors but never says how many (Dashboard.tsx:191-203). Reviews.tsx:16 loads once and never refreshes.
  - **Approach:** 1. Dashboard.tsx:
       - Fold api.systemHealth() into the existing pull loop, re-fetched every third tick (30s).
       - Skip ticks while document.hidden, and pull immediately on visibilitychange back to visible.
    2. Downloading tile:
       - When data.queue_note is set: value '—', sub 'Client unreachable', warn, with the raw note in a title attribute.
       - When queue.errored > 0: sub 'N errored · ↓ x/s · N seeding'.
    3. dashboard.go QueueSummary gains `stalled`: qBittorrent stalledDL/metaDL, which are currently counted as downloading. Show 'N waiting for peers' in the sub only when > 0. This is informational, not a warn.
    4. Reviews.tsx:
       - Reload every 30s while visible.
       - Reload immediately when the page's live event (useLive().last) has topic import.held.
    5. Keep the existing tile and card styles.
  - **Files:** `web/src/pages/Dashboard.tsx`, `web/src/pages/Reviews.tsx`, `internal/httpapi/dashboard.go`, `internal/httpapi/dashboard_test.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With the qBittorrent container stopped, the Downloading tile reads 'Client unreachable' within one refresh instead of '0 seeding'.
    - With 2 errored torrents, the tile says '2 errored'.
    - A system warning appears and clears on an open Dashboard within ~30s, without a reload.
    - A newly held import appears on an open Review page within 30s.
    - A hidden tab stops polling.
  - **Tests:** Go dashboard_test: a fake download service returning an error gives queue_note set and zero counts; a stalledDL item is counted in stalled, not downloading.; UI check: stop and start the bundled qBittorrent and watch the tile; leave Review open while a held import is created in a scratch setup.
  - **Risk:** Low. Health moves to a 30s cadence on top of the existing 10s dashboard poll. It goes away when OBS-08 replaces the warnings list with the attention card.
  - **Resolves:** ops-4

#### Milestone: M2 — Health registry and scheduled tasks

_Every background job records its last run, duration and error, survives a panic, and can be run on demand. Health runs in the background with keyed checks: each download client, Plex token, TMDB key and failing tasks. Every warning links to its fix. A System → Status page shows Health and Tasks._

<a id="obs-03"></a>
- [ ] **OBS-03 · Scheduler records every task's runs, survives panics, offers Run now, and exposes a tasks API** — `P1` · `M` · Phase 3
  - **Problem:** cmd/arrmada/main.go registers 28 recurring tasks, from prune-expired-sessions to audioserver-warm. scheduler.exec (scheduler.go:90-97) only logs a failure: there is no last run, duration, next run or last error, and no way to run a task now. exec has no recover(), so a nil dereference in any task kills the whole process (backend-9). The scheduler's own comment admits four real jobs 'sat dead' unnoticed.
  - **Approach:** 1. internal/scheduler/scheduler.go:
       - Store tasks as []*task.
       - Register keeps its signature and gains variadic `opts ...Option`: Label(s), Description(s) and Hidden() (for heartbeat).
       - Each task gets:
         - `mu sync.Mutex` guarding state {LastStart, LastEnd, LastDuration, LastError, LastErrorAt, LastOK, Runs, Failures, ConsecutiveFailures, Skipped, NextRun}
         - `running atomic.Bool`
         - `kick chan struct{}` (cap 1)
       - run() selects on ctx, the ticker and kick. NextRun = ticker start + every, then each fire + every. A kick doesn't reset the cadence.
       - exec():
         - CompareAndSwap(running) fails → Skipped++ and return. A ticker fire during a run is skipped, never queued.
         - defer recover() turns a panic into the error 'panic: <v>' and logs the stack.
         - Update state, then call OnFinish hooks.
       - New API:
         - `Snapshot() []TaskStatus`, sorted by label.
         - `RunNow(name) error`: returns ErrUnknownTask, or ErrBusy when running. Otherwise it sends on kick and returns once the run has started.
         - `SetStore(Store)` and `OnFinish(func(TaskStatus))`.
       - Tasks registered after Start keep working (the existing late-registration test stays).
    2. Persistence:
       - New migration NNNN_task_runs.sql (next free ≥0090): task_runs(name TEXT PRIMARY KEY, last_start INTEGER, last_end INTEGER, duration_ms INTEGER, last_error TEXT NOT NULL DEFAULT '', last_error_at INTEGER, runs INTEGER, failures INTEGER, consecutive_failures INTEGER).
       - internal/scheduler/sqlstore.go implements `Store{Load(ctx) (map[string]Persisted, error); Save(ctx, name, Persisted) error}`.
       - Load at Start.
       - Save after every run for tasks with every ≥ 1m. For sub-minute tasks (import-completed and the like), save only when the outcome flips or at most every 5 min. Hidden tasks are never saved.
    3. main.go: give every registration a plain label and description. Examples:
       - rss-sync → 'Check indexer feeds for new movies'
       - import-completed → 'Import finished movie downloads'
       - detect-stalled → 'Replace stalled downloads'
       - downloads-disk-guard → 'Watch download disk space'
       - manage-seeding → 'Stop seeding finished torrents'
       - request-ready-sweep → 'Tell requesters their request is ready'
       - recycle-enforce → 'Tidy the recycle bin'
       - discover-warm → 'Pre-load Discover rows'
       - heartbeat → Hidden()
    4. OnFinish hook in main.go publishes 'task.finished' {name, ok, dur_ms}, but only for non-hidden tasks, and only when every ≥ 1m, the outcome flipped, or it was a RunNow. The payload is content-free, because the hub reaches every client until SEC's filter lands.
    5. API in internal/httpapi/tasks.go (Deps gains Scheduler *scheduler.Scheduler):
       - `GET /api/v1/system/tasks` (RoleManager) → {tasks:[{name,label,description,every_ms,running,last_start,last_end,last_duration_ms,last_ok,last_error,last_error_at,runs,failures,consecutive_failures,next_run}]}. Hidden tasks are omitted.
       - `POST /api/v1/system/tasks/{name}/run` (RoleManager) → 202 when started, 404 for an unknown task, 409 {error:'already running'} when busy.
    6. api.ts gains tasks() and runTask(name). The UI comes in [OBS-06](#obs-06).
  - **Files:** `internal/scheduler/scheduler.go`, `internal/scheduler/state.go`, `internal/scheduler/sqlstore.go`, `internal/scheduler/scheduler_test.go`, `internal/store/migrations/NNNN_task_runs.sql`, `internal/httpapi/tasks.go`, `internal/httpapi/tasks_test.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - GET /system/tasks lists every non-hidden task with its label, interval, last run, duration, result and next run.
    - A task that returns an error shows that error and its consecutive failure count. The next success resets the count.
    - A task that panics is recorded as failed with 'panic: …', and the app keeps running.
    - POST run starts the task immediately. A second POST while it runs returns 409, and only one copy runs.
    - Last-run data and failure counts survive a restart.
  - **Tests:** Go TestSchedulerRecordsFailureAndSuccess: ConsecutiveFailures goes 1→2, then a success resets it to 0.; Go TestExecRecoversPanic.; Go TestRunNowTriggersImmediately (hour-long interval, RunNow → runs within 100ms).; Go TestRunNowWhileRunningReturnsErrBusy, and an overlapping ticker fire increments Skipped.; Go TestNextRunComputed.; Go TestLateRegisteredTaskTracked.; Go TestStoreLoadedOnStart, using an in-memory fake Store and the SQL store on a temp DB.; Go handler tests: manager 200, requester 403, unknown 404, busy 409.; go test -race ./internal/scheduler/... in Docker.
  - **Risk:** Concurrency in run, exec and RunNow needs the race run in Docker. Run now makes heavy tasks (convert-index, discover-warm) easy to start by hand; the running flag prevents overlap. Coordinate with BE's job runner (backend.t18): keep TaskStatus and the API stable so the job runner can feed the same page instead of building a second registry.
  - **Resolves:** system-8, product-5, backend-9
<a id="obs-04"></a>
- [ ] **OBS-04 · Health registry: cached background checks with keys, levels and fix links; manager-only /health/system** — `P1` · `M` · Phase 3
  - **Problem:** handleSystemHealth (health_system.go:22-100) hard-codes six checks and runs them on every request. Results are shown only on the Dashboard, fetched once. Warnings are plain text with no link to the fix (Dashboard.tsx:66-84). For example, 'No indexers are enabled' right after setup doesn't take you to Indexers. The route is a.protected, so requesters can read messages that contain server paths and errors. There's no way to add per-integration checks without making the endpoint slower.
  - **Approach:** 1. internal/health/registry.go:
       - Types:
         - `type Finding struct{Key, Level, Message string; Fix *Fix}`
         - `type Fix struct{Path, Label string}`
         - `type Check struct{Key, Name, Category string; Interval, Timeout time.Duration; Run func(ctx context.Context) []Finding}`. An empty slice means OK. Defaults are Interval 60s and Timeout 3s.
       - `Registry{Register(Check); RunDue(ctx) error; RunAll(ctx); Results() Report; Warnings() []Warning}`.
       - RunDue runs the checks that are due, in parallel, each under its own timeout. A timed-out check keeps its previous findings, marked stale, and adds the warning '<Name> check timed out'.
       - The registry remembers `since` per finding key (the first time it went bad).
       - It publishes 'health.changed' {status, errors, warnings} only when the set of (key, level, message) changes. The payload never carries message text.
    2. internal/health/checks_core.go ports today's six checks as constructors over narrow interfaces, so they can be unit-tested with fakes:
       - indexers.none
       - downloads.client.none
       - downloads.reachable: keep today's Queue()-based check until [OBS-05](#obs-05) replaces it with per-client checks
       - library.<role>, from [OBS-01](#obs-01)'s folder logic
       - recycle.drive
       - disk.guard
       - disk.free
       - audiobooks.server
    3. internal/health/links.go: one place for every fix path:
       - /indexers 'Add an indexer'
       - /downloadclients 'Check download clients'
       - /settings?tab=library 'Choose folders'
       - /settings?tab=system#disk-guard 'Disk guard settings'
       - /settings?tab=system#api-keys 'API keys'
       - /audiobooks 'Audiobook server'
       - /insights?tab=settings 'Plex connection'
       - /system/status?tab=tasks 'Tasks'
       CFG's hub repoints these later.
    4. main.go: build the registry, register the checks, and add `sched.Register("health-check", 30*time.Second, true, reg.RunDue, scheduler.Label("Check system health"))`. Deps gains Health *health.Registry.
    5. health_system.go serves reg.Results() with no live calls.
       - It keeps the {status, warnings, disk} shape. warnings gain key, link, link_label and since, and the response adds checks:[{key,name,category,level,checked_at,duration_ms,stale}].
       - `?refresh=1` runs RunAll, rate-limited to once per 10s.
       - The route changes to requireRole(auth.RoleManager). Only Dashboard.tsx:38 calls it (verified with grep in web/src).
    6. Frontend:
       - The SystemHealth and HealthWarning types gain the new fields.
       - A Dashboard warning row with a link renders as a react-router Link row ending in 'Fix →', in the existing warning style.
       - Settings.tsx reads ?tab= via useSearchParams and scrolls to #hash anchors. Add id='api-keys' on APIKeysSection and id='disk-guard' on DiskGuardSection.
  - **Files:** `internal/health/registry.go`, `internal/health/registry_test.go`, `internal/health/checks_core.go`, `internal/health/checks_core_test.go`, `internal/health/links.go`, `internal/httpapi/health_system.go`, `internal/httpapi/health_system_test.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`, `web/src/lib/api.ts`, `web/src/pages/Dashboard.tsx`, `web/src/pages/Settings.tsx`
  - **Acceptance:**
    - GET /api/v1/health/system answers from cache in well under 100 ms, even when a check hangs.
    - With every indexer disabled, the Dashboard shows the warning with an 'Add an indexer' link that opens /indexers.
    - A disk-guard warning link opens Settings on the System tab, scrolled to the disk guard section.
    - A requester session gets 403 from /health/system.
    - A check that hangs is reported as timed out within ~3s and doesn't delay the others.
  - **Tests:** Go TestRegistryRunsDueChecks (fake clock: a 60s check runs once across two 30s ticks).; Go TestRegistryTimeout: a sleeping check gives a timed-out warning while other checks still report.; Go TestRegistryPublishesOnChangeOnly (fake bus).; Go TestHandlerServesCachedResults: GET doesn't increment a check's run counter, and refresh=1 does, rate-limited.; Go TestHealthRequesterForbidden.; Go: port the existing behaviours (no indexers gives an error with a link; disk guard holding gives a warning) to checks_core_test.; UI check: warnings render as links and land on the right Settings section.
  - **Depends on:** [OBS-01](#obs-01), [OBS-03](#obs-03) (soft: only for the Label option on the health-check task)
  - **Risk:** Results are up to 30-60s old. That's acceptable, and 'Check now' covers impatience. The fix paths point at today's routes and must be updated in links.go when CFG/FE move pages.
  - **Resolves:** system-8, product-5, product-9, frontend-10
<a id="obs-05"></a>
- [ ] **OBS-05 · Health checks for each download client, the Plex connection, the TMDB key and failing scheduled tasks** — `P1` · `M` · Phase 3
  - **Problem:** Queue() errors only when every client fails (download/service.go:339-344, health_system.go:43). A dead second qBittorrent therefore leaves the panel green, while DetectStalled silently skips every cycle (coordinator.go:1208-1215). A revoked Plex token surfaces only as 'Plex isn't reachable — …' in the Now-playing card, and Insights poll errors are logged at Debug (insights/poller.go:76-87). A missing or invalid TMDB key isn't checked, and nothing checks whether scheduled tasks keep failing.
  - **Approach:** 1. Download clients:
       - download.Service.ClientStates(ctx) []ClientState{ID, Name, Kind, OK bool, Err string, LatencyMS}. It lists each ENABLED client on its own, with a 5s per-client timeout, using the same call as handleTestDownloadClient.
       - The check 'downloads.client.<id>' (60s) reports:
         - error when the unreachable client is the only enabled one
         - otherwise a warning: 'qBittorrent (seedbox) is unreachable — its torrents aren't tracked and stall fail-over is paused until it answers or is removed'
         - link /downloadclients
       - This replaces [OBS-04](#obs-04)'s interim Queue()-based check. Stall detection keeps its own fresh QueueComplete read and must never reuse this result.
    2. Plex:
       - insights.Service gains PollHealth() {Configured, Monitoring bool; LastOKAt, LastErrAt time.Time; LastErr string; Consecutive int}, recorded in poll() under a mutex.
       - poll() logs Warn on the first failure and Info on recovery, instead of Debug on every failure.
       - When monitoring is off but Plex is configured, the check probes plex.Client.Identity() with a 5-min check Interval.
       - The check 'plex.connection' is skipped when Plex isn't configured.
         - A 401 ('plex rejected the token', plex/client.go:50) gives an error at once: 'Plex rejected Arrmada's token — reconnect Plex. Plex sign-in for family, recommendations and Insights stop working until you do.'
         - Unreachable continuously for ≥10 min gives a warning: 'Plex hasn't answered since 14:02: <err>'.
         - Link: Plex connection.
       - Reuse PLEX's poll-health counters (draft insights.t4) if that lands first.
    3. TMDB:
       - metadata.TMDB.Validate(ctx) does GET /3/configuration and maps a 401 to ErrInvalidKey.
       - The check 'tmdb.key' has a 6h Interval:
         - missing key → error: 'No TMDB key is set — search, Discover and new titles won't work'
         - 401 → error: 'TMDB rejected the API key'
         - network failure → warning, not 'invalid'
         - link /settings?tab=system#api-keys
       - handleTestAPIKey gains case 'tmdb' (the same Validate), and handleSetAPIKey asks the registry to re-run tmdb.key after a save (Registry.RunNow(key)).
    4. Tasks:
       - The check 'tasks.failing' (60s) reads scheduler.Snapshot().
       - Each task with ConsecutiveFailures ≥ 3 gives a warning: '“Check indexer feeds for new movies” has failed 3 times in a row: <last error>'.
       - Link /system/status?tab=tasks.
    5. Register everything in main.go and remove the interim checks. Messages never include tokens or keys.
  - **Files:** `internal/download/service.go`, `internal/download/service_test.go`, `internal/insights/poller.go`, `internal/insights/service.go`, `internal/insights/poller_test.go`, `internal/metadata/tmdb.go`, `internal/metadata/tmdb_test.go`, `internal/health/checks_integrations.go`, `internal/health/checks_integrations_test.go`, `internal/httpapi/apikeys.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - With two clients and one stopped, a warning names the stopped client within ~1 minute. With only the bundled client stopped, it's an error.
    - Revoking or garbling the Plex token gives a red warning linking to the Plex connection within one poll. Stopping Plex for under 10 minutes gives nothing.
    - An invalid TMDB key shows as an error, not 'ok'. Saving a valid key clears it without waiting 6h.
    - A task failing 3 times in a row produces a warning linking to Tasks, which clears after its next success.
    - GET /health/system stays fast while Plex or a client is down.
  - **Tests:** Go TestClientStates: one httptest qBittorrent up and one down.; Go TestPlexCheck: a 401 gives an error immediately; an unreachable state at 9 min gives nothing and at 11 min a warning; not configured is skipped.; Go TestTMDBValidate: an httptest 401 gives ErrInvalidKey, and a 200 is ok.; Go TestTasksFailingCheck with a fake snapshot.; Go: a requester can't reach the test endpoint (existing RoleManager).
  - **Depends on:** [OBS-04](#obs-04), [OBS-03](#obs-03), PLEX — Insights poll-health counters (draft insights.t4), optional; this task adds a minimal version if it hasn't landed
  - **Risk:** External probes must honour their intervals: TMDB every 6h, Plex identity every 5 min. Plex or qBittorrent restarts can cause false positives, hence the 10-minute persistence rule for Plex. OBS-12's alerting adds a two-run confirmation. Never reuse ClientStates for stall decisions.
  - **Resolves:** integrations-3, product-5, system-8
<a id="obs-06"></a>
- [ ] **OBS-06 · System → Status page: Health list with Fix links and a Tasks table with Run now** — `P1` · `M` · Phase 3
  - **Problem:** There's no Sonarr-style System view. The owner can't see what's running, what last failed or which checks are bad, and can't trigger a sweep on demand. Health is visible only as a list on the Dashboard.
  - **Approach:** 1. New components:
       - web/src/components/system/HealthList.tsx: checks grouped by category (Storage, Downloads, Indexers, Integrations, Tasks).
         - Each row has a level dot (var(--good) / var(--avoid) / var(--reject)), the name, the message, 'checked 20s ago' and a 'Fix →' link when one exists.
         - A 'Check now' button calls /health/system?refresh=1.
       - web/src/components/system/TasksTable.tsx: columns Name (label, with the description as a tooltip or tap-expand), Every (human: '15 min'), Last run (relative time plus duration), Result chip (OK / Failed / Running; tapping Failed shows the error), Next run (relative), and 'Run now'.
         - Run now is disabled while running. A 409 shows the toast 'Already running'.
         - Below 640px, rows collapse to cards with the 16px gutter.
    2. New page web/src/pages/SystemStatus.tsx at /system/status, with tabs Health | Tasks selected by ?tab=, in the existing PageHeader and tab style. Add the route in App.tsx (staff routes) and the nav item 'Status' under System in nav.ts, above Logs.
    3. Refresh:
       - Poll every 10s while visible, and pause when document.hidden.
       - If FE's shared live hook (frontend.t32) exists, also refetch on 'task.finished' and 'health.changed'.
       - Do NOT add another useLive() websocket. Each useLive() call opens its own socket (useLive.ts).
    4. Export both components so CFG's Settings/System hub can mount them unchanged.
  - **Files:** `web/src/pages/SystemStatus.tsx`, `web/src/components/system/HealthList.tsx`, `web/src/components/system/TasksTable.tsx`, `web/src/App.tsx`, `web/src/lib/nav.ts`, `web/src/lib/api.ts`
  - **Acceptance:**
    - System → Status shows every check with its level, message and a working Fix link.
    - The Tasks tab lists every non-hidden task with sensible last and next run times.
    - Run now on 'Search for missing movies' flips the row to Running, then to OK or Failed, without a reload.
    - A failed task shows its error text on hover or tap.
    - The page works at 375px width with no horizontal scroll.
  - **Tests:** npm run build and the type-check pass.; UI check: stop the bundled qBittorrent and see the check turn red with a Fix link. Run now on a cheap task (prune-expired-sessions) and watch it go Running → OK. Click twice to see 'Already running'.
  - **Depends on:** [OBS-03](#obs-03), [OBS-04](#obs-04), FE — shared live-query websocket hook (frontend.t32), optional, CFG — will re-host these components in the Settings/System hub
  - **Risk:** Low. Keep the styles to the existing tokens and type scale. Don't open extra websockets.
  - **Resolves:** system-8, product-5, backend-9

#### Milestone: M3 — Needs you

_One attention snapshot drives the Dashboard 'Needs you' card and the sidebar badges (Review, Downloads, Discover, Status, plus the mobile hamburger dot). Pending requests, held imports, errored/stalled downloads, failing imports, wrong-category downloads, stuck searches and health problems are visible on every page within 30s._

<a id="obs-07"></a>
- [ ] **OBS-07 · Attention feed: one pull-based 'needs you' snapshot and GET /api/v1/attention** — `P1` · `M` · Phase 6
  - **Problem:** Nothing aggregates what needs the owner. Pending requests appear only in a Discover strip. Held reviews wait until someone opens Review. Errored torrents are an amber tile without a count, and health is a separate list. Building badges or alerts on bus events alone would be unreliable, because the bus drops events when a subscriber's 64-slot buffer is full (eventbus.Publish).
  - **Approach:** 1. New package internal/attention:
       - `Item{Key, Kind (request|review|download|stalled|import|wrongcat|search|health|convert|issue), Level (warning|error), Title, Detail, Link string; Since int64; Count int}`.
       - `Counts{Requests, Reviews, Downloads, Imports, Searches, Health, HealthErrors, Total}`.
       - `Group{Kind, Level, Count, Title, Link string; Sample []string}`, with up to 3 names per group.
       - `Snapshot{At, Counts, Groups, Items}`, with Items capped at 200.
    2. Refresh and providers:
       - `type Provider interface{ Name() string; Collect(ctx context.Context, f *Frame) ([]Item, error) }`. Frame carries a queue read made ONCE per refresh: download.Service.QueueComplete, plus a whole/ok flag.
       - Service.Refresh runs providers in parallel with a 5s timeout each.
       - When a provider errors, keep its previous items and log a throttled warning.
       - Store the snapshot in an atomic.Pointer.
       - Publish 'attention.changed' {counts} only when the counts change.
    3. Providers:
       - requests: requests.Service.List(ctx, "pending", 0). Key request:<id>, title 'Sam requested Dune (2021)', link /discover (REQ's /requests page later).
       - reviews: Coordinator.ListReviews. Key review:<id>, title '<name> is held: <reason>' (the reason is truncated, or a reason-code label once ACQ's reason codes land), link /review.
       - downloads, from the Frame:
         - Errored items (state contains error or missingFiles): key download:<hash>, level error.
         - Long-stalled items: stalledDL or metaDL continuously for ≥1h, tracked in an in-memory firstSeenStalled map. Key stalled:<hash>, level warning, link /downloads?show=problems.
         - Switch to ACQ's Phase classification when it lands.
         - If the client couldn't be listed, emit nothing here. The health item covers it.
       - health: health.Registry.Warnings(). Key health:<finding key>, with the finding's level, message and fix link.
       - searches: a single aggregated item 'search.stuck' with Count N, title 'N titles still haven't found a release', linking to the Downloads Searching tab.
         - Add COUNT helpers: movies.Service.CountSearchStuck(ctx, 10), series.Service.CountSearchStuck(ctx, 10), books.Service.CountSearchGivenUp(ctx). Each counts monitored, still-missing rows with search_misses ≥ threshold (books use bookSearchAttempts). Verify the column names at implementation.
    4. Run(ctx) goroutine: subscribe to request.created, import.held and health.changed. Debounce for 2s, then Refresh. A dropped kick is backstopped by the scheduler task.
    5. Scheduler task 'attention-refresh' every 30s, runAtStart, labelled 'Update the Needs-you list'.
    6. internal/httpapi/attention.go: `GET /api/v1/attention` (RoleManager) serves the snapshot {at, stale (older than 2 min), counts, groups, items[≤50]}. It makes no live calls. Deps gains Attention.
    7. Keep links in internal/health/links.go so the CFG, REQ and ACQ route changes happen in one place.
  - **Files:** `internal/attention/attention.go`, `internal/attention/providers.go`, `internal/attention/attention_test.go`, `internal/httpapi/attention.go`, `internal/httpapi/attention_test.go`, `internal/httpapi/server.go`, `internal/movies/repo.go`, `internal/series/repo.go`, `internal/books/repo.go`, `internal/health/links.go`, `cmd/arrmada/main.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With 2 pending requests, 1 held review, 1 errored torrent and 1 torrent stalled for 2h, /attention reports requests=2, reviews=1 and downloads=2, with the right links.
    - Approving the requests drops the count within ~30s.
    - A new request shows up within a few seconds, via the kick.
    - A requester gets 403.
    - With qBittorrent down, the endpoint still answers instantly and shows the health item, not a download error.
  - **Tests:** Go aggregator test with a seeded temp DB and a fake queue: the counts, keys and groups are as expected.; Go: a stalled item is reported only after 1h on an injectable clock.; Go: a provider error keeps the previous items.; Go: attention.changed is published only when the counts change (fake bus).; Go handler role test: manager 200, requester 403.; go test -race ./internal/attention/... in Docker.
  - **Depends on:** [OBS-04](#obs-04), ACQ — queue Phase/stalled classification (draft ops.t4) and review reason codes (draft ops.t9); both degrade gracefully if absent, REQ — /requests page as the link target (Discover until then)
  - **Risk:** Query cost: every provider is a COUNT or LIMIT query on indexed columns, plus one queue read every 30s, regardless of how many tabs poll. The attention.changed payload must stay counts-only until SEC filters websocket topics.
  - **Resolves:** ops-4, product-4, frontend-10
<a id="obs-08"></a>
- [ ] **OBS-08 · "Needs you" card at the top of the Dashboard, sidebar count badges, and a mobile dot** — `P1` · `M` · Phase 6
  - **Problem:** The sidebar fetches only the audiobook dot (Sidebar.tsx:20-25, 79-81). Review, Downloads and Discover show no counts. The Dashboard has no pending-request, held-review or failure counts, and its health warnings are a separate plain list. On phones the sidebar is a drawer, so even a badge would be hidden.
  - **Approach:** 1. web/src/lib/useAttention.ts: a module-level store shared by every component.
       - One poller hits /api/v1/attention every 30s, pauses while document.hidden, and refetches on visibilitychange and after a mutation via a `refreshAttention()` export.
       - If FE's shared live hook exists, it also refetches on 'attention.changed'.
       - It must not create a websocket per component.
    2. Dashboard.tsx: a 'Needs you' card at the very top, replacing today's warnings list (which [OBS-04](#obs-04) made linkable). It keeps the existing card style.
       - One row per group: a level dot, a plain sentence with the count ('3 requests are waiting for approval', '1 import needs review', '2 downloads errored'), and an action link ('Review them →').
       - Health groups expand into one row per warning, each with its 'Fix →' link.
       - When the total is 0, show a quiet 'All clear — nothing needs you' line.
       - The Recent activity and Storage sections remain below.
    3. Sidebar.tsx: count pills in the mono 10px style, next to the existing audiobook-dot slot (ml-auto).
       - Review: counts.reviews.
       - Downloads: counts.downloads + counts.imports.
       - Discover: counts.requests, until REQ's /requests page exists.
       - Status (System): HealthErrors as a red pill (var(--reject)), or a warnings count as an amber pill.
       - Pills use var(--accent-soft) with var(--accent) text by default, and stay hidden at 0.
    4. AppLayout.tsx: a small dot on the mobile hamburger when counts.total > 0.
    5. Downloads.tsx: read `?show=problems` to filter to errored and stalled items (by state until ACQ's Phase lands).
    6. Approving or declining a request and resolving a review call refreshAttention(), so the badges update at once.
  - **Files:** `web/src/lib/useAttention.ts`, `web/src/pages/Dashboard.tsx`, `web/src/components/Sidebar.tsx`, `web/src/components/AppLayout.tsx`, `web/src/pages/Downloads.tsx`, `web/src/pages/Reviews.tsx`, `web/src/pages/Discover.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With 2 pending requests and 1 held import, the Dashboard's first card shows both rows with counts and links, and the sidebar shows 2 on Discover and 1 on Review.
    - Stopping the qBittorrent container turns the Status pill red within ~1 minute on any page, and starting it clears the pill without a reload.
    - Resolving the review removes its badge immediately.
    - With nothing pending, the card shows the quiet 'All clear' line.
    - On a 375px phone, the hamburger shows a dot when anything needs attention.
  - **Tests:** npm run build and the type-check pass.; UI check against a scratch setup: create a request as a test requester, hold an import, and stop and start qBittorrent. Verify the strip, the badges, the links and the hidden-tab pause (no requests in the network panel while hidden).
  - **Depends on:** [OBS-07](#obs-07), FE — shared live-query websocket hook (frontend.t32), optional, REQ — /requests page for the request link and badge target
  - **Risk:** Sidebar polling must stay cheap: one poller per tab hitting a cached endpoint. Badge styling must use the existing tokens. Avoid duplicating health in two places on the Dashboard: the card replaces the warnings list.
  - **Resolves:** ops-4, product-4, frontend-10
<a id="obs-09"></a>
- [ ] **OBS-09 · Attention: imports stuck in retry and completed TV downloads in the wrong category** — `P2` · `S` · Phase 6
  - **Problem:** library.Manager keeps a per-hash failure map (attempts, next retry, last error; manager.go:54-118), but nothing outside it can read it, so an import that keeps failing is invisible until the movie path holds it for review after 5 tries. The series import path counts importFailed (series.go:1056-1064) and only logs. A completed TV pack in the wrong qBittorrent category is only a log warning (series.go:968-978) and silently never imports.
  - **Approach:** 1. library.Manager:
       - importFailure gains name, and noteFailure(hash, name, err).
       - New `Failures() []FailureInfo{Hash, Name, Attempts int; NextRetry time.Time; LastErr string}`, a copy taken under failMu.
    2. Coordinator: a small `seriesImportFails map[hash]*{name, sweeps, lastErr}` behind a mutex.
       - Increment it where importFailed > 0 (series.go:1058).
       - Clear it when a sweep places files or the review resolves.
       - Expose `SeriesImportFailures() []library.FailureInfo`.
       - Drop this if ACQ's acquisitions table lands first and exposes failed imports.
    3. Factor the diagnostic at series.go:968-978 into `Coordinator.WrongCategoryDownloads(ctx, items []download.Item) []download.Item`: completed, TV-parsing items that match a library series and whose category isn't seriesCategory. ImportSeriesDownloads keeps its log line through it, throttled to once per hash per hour.
    4. Attention providers, using the Frame's queue rather than another qBittorrent call:
       - imports: key import:<hash>. Warning at ≥2 attempts, error at ≥5. Title 'Importing <name> keeps failing (3 tries): <err>', link /downloads?show=problems. Skip hashes that already have a pending review, so a movie stuck after 5 tries shows once, as the review.
       - wrongcat: key wrongcat:<hash>, warning, title '<name> finished in category “<cat>” — Arrmada only imports TV from “<seriesCategory>”'.
    5. Counts.Imports comes from these items. The sidebar's Downloads pill already includes them ([OBS-08](#obs-08)).
  - **Files:** `internal/library/manager.go`, `internal/library/manager_backoff_test.go`, `internal/automation/series.go`, `internal/automation/series_wrongcat_test.go`, `internal/attention/providers.go`, `internal/attention/attention_test.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - An import failing on a read-only folder (in a scratch setup) shows 'keeps failing (2 tries)' in Needs you within a minute of the second failure, and clears after a successful import.
    - A completed TV download in another category that matches a library series shows as a wrong-category item.
    - A movie that reached 5 failures shows once, as a held review, not twice.
  - **Tests:** Go: Failures() reports attempts, the last error and the next retry after two failures, and clearFailure removes the entry.; Go: WrongCategoryDownloads returns a completed TV-parsing item in category 'other' that matches a seeded series, and ignores non-matching or incomplete items.; Go attention test: an import with a pending review is skipped.
  - **Depends on:** [OBS-07](#obs-07), ACQ — if the acquisitions core lands first, read failed imports from it instead of the new maps
  - **Risk:** Low. The in-memory failure maps reset on restart, which is acceptable because retries restart too. Keep the wrong-category log throttled.
  - **Resolves:** ops-4

#### Milestone: M4 — Alerts that reach the owner

_Alerts get their own page with a grouped event catalog, including book and music imports. New requests, held/stuck imports, failed downloads and health problems (with 'Resolved') alert exactly once, survive restarts and never flood. Deliveries are queued with retries and per-connection status. The owner can get them as Web Push on their phone._

<a id="obs-10"></a>
- [ ] **OBS-10 · Admin alerts get their own Alerts page, out of Insights, and admin URLs are validated on save** — `P1` · `S` · Phase 6
  - **Problem:** Admin notification connections are a tab inside the Plex monitoring page (Insights.tsx:822-899). /notifications redirects to /insights (App.tsx:106), which opens on Activity, not Notifications. The admin create and update handlers (notifications.go:23-58) never call notify.ValidateAppriseURL, which is only used for per-user URLs (usernotify.go:99), so a typo or an option-looking string is stored and fails at send time.
  - **Approach:** 1. Move EVENTS, BLANK_CONN, NotificationsView and ConnCard from Insights.tsx into web/src/pages/Alerts.tsx. Export an `AlertsSettings` component (the list plus editor) and a page wrapper with a PageHeader (title 'Alerts', crumb 'System / Alerts'), in the existing card style.
    2. Write the intro in plain words: 'Get a message when something needs you or when new things arrive.' Apprise is mentioned only in the Custom field hint.
    3. App.tsx: add Route /alerts, and change /notifications to `<Navigate to="/alerts" replace />`. nav.ts gets 'Alerts' under System (staff-only, like the rest of System).
    4. Insights.tsx: remove 'notifications' from TABS, and add a one-line link card on the Settings tab ('Alert settings moved to Alerts →') for one release.
    5. notifications.go: Create and Update trim the URL and call notify.ValidateAppriseURL, answering 400 with its message. Test also validates before shelling out. Existing stored URLs still load and send; editing requires a valid URL.
    6. If CFG's Settings hub lands first, mount AlertsSettings there as Settings → Notifications and redirect /alerts to it instead.
  - **Files:** `web/src/pages/Alerts.tsx`, `web/src/pages/Insights.tsx`, `web/src/App.tsx`, `web/src/lib/nav.ts`, `internal/httpapi/notifications.go`, `internal/httpapi/notifications_test.go`
  - **Acceptance:**
    - The sidebar shows System → Alerts, and an old /notifications link lands on /alerts.
    - Insights no longer has a Notifications tab.
    - Existing connections are listed and editable on /alerts exactly as before.
    - Saving 'discord//missing-colon' or '-config=/etc/x' returns a clear validation error.
  - **Tests:** Go: the notifications handler rejects an invalid URL with 400, and accepts discord:// and ntfys://.; npm run build and the type-check pass.; UI check: /notifications redirects; Insights shows 6 tabs.
  - **Depends on:** CFG — if the Settings/System hub exists, mount there instead
  - **Risk:** Low; this is mostly a move. Muscle memory is the only cost, which is why the link card stays for one release.
  - **Resolves:** insights-7, product-8
<a id="obs-11"></a>
- [ ] **OBS-11 · Alert event catalog with per-connection subscriptions; book, music and request alerts** — `P1` · `M` · Phase 6
  - **Problem:** Connections have four booleans (on_grab, on_import, on_stream, on_buffering; notify.go:30-33), and Run (notify.go:147-195) has a hand-written case per topic. book.imported (books.go:914, 1549, books_versions.go:202) and music.imported (music.go:375) have no admin subscriber. requests.Create (requests/service.go:76-113) publishes nothing, although the service already holds the bus. Adding any event means a migration and another bool.
  - **Approach:** 1. Migration NNNN_notification_subscriptions.sql (next free ≥0090):
       - `CREATE TABLE notification_subscriptions(connection_id INTEGER NOT NULL REFERENCES notifications(id) ON DELETE CASCADE, event_key TEXT NOT NULL, PRIMARY KEY(connection_id, event_key))`.
       - Backfill:
         - on_grab=1 → release.grabbed
         - on_import=1 → movie.imported, episodes.imported and book.imported
         - on_stream=1 → plex.stream.started
         - on_buffering=1 → plex.buffering
       - Keep the old columns and stop reading them.
    2. internal/notify/catalog.go:
       - `type EventDef struct{Key, Topic, Group, Label, Hint string; DefaultOn bool; Format func(data map[string]any) (Message, bool)}`, where `Message{Title, Body, Link string}`.
       - Groups: needs_you, library, downloads, plex, convert.
       - This task registers:
         - release.grabbed
         - movie.imported (topic movie.downloaded)
         - episodes.imported (series.imported)
         - book.imported: '📚 <title> (audiobook)' or '(ebook)' from edition, plus the version label
         - music.imported
         - request.auto_approved: '✅ Auto-approved: <title> for <user>'
         - plex.stream.started
         - plex.buffering
       - Needs-you defs are added by [OBS-12](#obs-12) and convert defs by [OBS-18](#obs-18). Each task registers only events that have a producer.
    3. Service:
       - Connection gains `Events []string`. The JSON on_* fields go.
       - List and Get load subscriptions in one query; Create and Update write them in the same transaction.
       - `subscribes(key)` checks the set.
       - New `Dispatch(ctx, key string, m Message)` fans out to subscribed, enabled connections. It's the single entry point that [OBS-12](#obs-12), [OBS-13](#obs-13) and [OBS-18](#obs-18) use.
       - A `send` func field (defaulting to deliver) is the test seam.
       - Run does one `bus.Subscribe(topics…)` across the catalog, maps topic → EventDef → Format → Dispatch.
    4. Publishers:
       - requests.Service.Create publishes 'request.created' {id, title, year, media_type, requested_by} when the request is left pending, including a declined request re-opened by attachToExisting. It publishes 'request.auto_approved' when autoApprove succeeds.
       - music.go:375 adds artist and album names to the music.imported payload.
       - addReview (reviews.go:76) adds id (LastInsertId), media_type and expected_title to import.held.
    5. API: `GET /api/v1/notifications/catalog` (RoleManager) → [{key, group, label, hint, default_on}].
    6. Alerts.tsx ConnCard renders a grouped checklist from the catalog, with a group-level 'all' toggle. New connections pre-tick the DefaultOn events. Music events are hidden when music is disabled.
    7. Never add listening events (privacy rule).
  - **Files:** `internal/store/migrations/NNNN_notification_subscriptions.sql`, `internal/notify/catalog.go`, `internal/notify/notify.go`, `internal/notify/notify_test.go`, `internal/notify/catalog_test.go`, `internal/requests/service.go`, `internal/requests/service_test.go`, `internal/automation/music.go`, `internal/automation/reviews.go`, `internal/httpapi/notifications.go`, `internal/httpapi/server.go`, `web/src/pages/Alerts.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Existing connections keep exactly their previous choices after the migration, and 'Imported' connections also get book imports.
    - A connection subscribed to 'Book imported' receives '📚 <title> (audiobook)' when an audiobook imports.
    - An auto-approved request alerts subscribed connections.
    - Deleting a connection removes its subscriptions.
    - Event checkboxes save and reload, grouped.
  - **Tests:** Go TestSubscriptionsBackfill: migrate a DB seeded with on_grab=1, on_import=1, on_stream=1 and check the rows.; Go TestCatalogFormat: every EventDef formats a sample payload into a non-empty title and body, and returns ok=false when the title is missing.; Go TestRunDispatchesByTopic: fake bus plus fake send; publishing book.imported reaches the subscribed connection only.; Go TestConnectionCRUDRoundTripsEvents.; Go (requests) TestCreatePublishesRequestCreated for a pending request and auto_approved for an auto request; an attach to an existing pending request publishes nothing new.
  - **Depends on:** [OBS-10](#obs-10)
  - **Risk:** The backfill must not lose subscriptions, which the migration test covers. Bus events here are still fire-and-forget. Needs-you alerts go through OBS-12's persisted path precisely so they don't depend on the bus.
  - **Resolves:** insights-6, product-4, product-8, ops-4
<a id="obs-12"></a>
- [ ] **OBS-12 · Needs-you and health alerts driven by the attention feed: exactly once, restart-safe, no floods** — `P1` · `M` · Phase 6
  - **Problem:** Nothing alerts the owner when something needs them. That includes a request waiting, an import held in Review (import.held has no subscriber), a stuck import, an errored download, the disk guard pausing downloads, a dead client, or Plex becoming unreachable. Alerts built only on bus events would be unreliable, since the bus drops events under load, and naive health alerts would fire on every restart.
  - **Approach:** 1. Migration NNNN_attention_state.sql:
       - `attention_state(key TEXT PRIMARY KEY, kind TEXT NOT NULL, level TEXT NOT NULL, title TEXT NOT NULL DEFAULT '', first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL, alerted_at INTEGER NOT NULL DEFAULT 0, resolved_at INTEGER NOT NULL DEFAULT 0, misses INTEGER NOT NULL DEFAULT 0)`.
       - Same migration: insert DefaultOn needs-you subscriptions for existing connections that have on_grab or on_import, so admin channels get them and Plex-only family channels don't.
    2. Catalog defs (group needs_you, no bus Topic, produced here):
       - request.pending: '🙋 Sam requested Dune (2021) — approve it in Arrmada', on
       - import.held: '✋ Held for review: <release> — <reason>', on
       - import.stuck: covers import retries and wrong-category downloads, on
       - download.failed: errored torrent, on
       - download.stalled: off by default
       - health.problem: '🔴 <message>', on
       - health.resolved: '✅ Resolved: <check name>', on
       Disk-guard holds and Plex outages are health checks, so they arrive as health.problem and health.resolved ('Downloads are paused: /downloads is 87% full'). This replaces the separate diskguard.* and plex.down/up topics in the drafts.
    3. internal/attention/alerts.go: `Alerter{db, dispatch, clock, bootAt}`. Diff(ctx, snap) runs at the end of every Refresh, serialized by a mutex.
       - Seeding: when the table is empty and the setting 'attention_alerts_seeded' is unset, insert every current item with alerted_at=now, send nothing, and set the flag.
       - New key:
         - Insert a row.
         - Health items must be present in 2 consecutive refreshes, and the process must be up ≥5 min (boot grace), before they send.
         - Other kinds send at once.
         - Set alerted_at.
       - A key that resolved and comes back sends again only if now − alerted_at ≥ 6h (flap guard).
       - Keys missing from the snapshot: misses++. At 2 misses, set resolved_at. For health rows that alerted, send health.resolved once.
       - Batching: more than 5 new sends of one kind in one diff become one message: '6 downloads failed: A, B, C and 3 more'.
       - Prune non-health rows resolved more than 7 days ago.
    4. Immediacy comes from [OBS-07](#obs-07)'s kicks (request.created and import.held trigger a refresh within ~2s). Alerts send through notify.Dispatch, which becomes the queue's Enqueue after [OBS-13](#obs-13).
    5. Message text uses request, review, download and health titles only. Never anything from listening.
  - **Files:** `internal/store/migrations/NNNN_attention_state.sql`, `internal/attention/alerts.go`, `internal/attention/alerts_test.go`, `internal/attention/attention.go`, `internal/notify/catalog.go`, `cmd/arrmada/main.go`, `web/src/pages/Alerts.tsx`
  - **Acceptance:**
    - A new pending request sends exactly one alert to a subscribed connection within ~5 seconds.
    - A held review, an errored download and an import that keeps failing each alert once.
    - Restarting Arrmada re-sends nothing.
    - Stopping qBittorrent sends one 'unreachable' alert after ~1-2 minutes, and starting it sends one 'Resolved'. A restart where qBittorrent comes up within 5 minutes sends nothing.
    - The disk guard engaging sends 'Downloads are paused…' once, and releasing sends 'Resolved'.
    - The first run after deploying sends nothing for items that already existed.
    - A connection with Needs-you events unticked receives none of these.
  - **Tests:** Go TestAlertNewKeyOnce: a new key sends one alert, and a second diff sends none.; Go TestAlertStateSurvivesRestart: a fresh Alerter on the same DB sends none.; Go TestAlertFirstRunSeedsOnly.; Go TestHealthNeedsTwoRunsAndBootGrace.; Go TestHealthResolvedOnce: missing for 2 runs gives exactly one resolved.; Go TestFlapGuard: resolved then back within 6h gives no re-send; after 6h, one.; Go TestBatchingAboveFive.; Go: request.created kick plus the periodic refresh together give a single send (dedupe by key).; go test -race ./internal/attention/... in Docker.
  - **Depends on:** [OBS-07](#obs-07), [OBS-11](#obs-11), [OBS-04](#obs-04), [OBS-05](#obs-05) (client and Plex checks make the health alerts meaningful), ACQ — stall fail-over publisher (draft ops.t5) if download.stalled should cover fail-overs too
  - **Risk:** Alert noise. Seeding, boot grace, two-run confirmation, the flap guard and batching are all part of this task, not follow-ups. Quiet hours are out of scope. Back up the DB, or test on a scratch DB, before running the migration's subscription backfill on the real install.
  - **Resolves:** product-4, ops-4, system-8, insights-6
<a id="obs-13"></a>
- [ ] **OBS-13 · Notification delivery queue with retries, restart survival and a per-connection delivery log** — `P2` · `M` · Phase 6
  - **Problem:** fan() (notify.go:220-233) delivers serially inside the bus-subscriber loop, with a 20s apprise timeout per send. While a slow endpoint blocks the loop, the 64-slot bus buffer fills and later events are dropped ('event dropped: subscriber buffer full'). Failures go only to the log, so a broken endpoint fails silently with no status in the UI and no retry.
  - **Approach:** 1. Migration NNNN_notification_deliveries.sql:
       - `notification_deliveries(id INTEGER PK, connection_id INTEGER NOT NULL REFERENCES notifications(id) ON DELETE CASCADE, event_key TEXT, title TEXT, body TEXT, link TEXT NOT NULL DEFAULT '', attach TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'queued', attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', created_at INTEGER, next_attempt_at INTEGER, sent_at INTEGER)`.
       - Indexes: `(status, next_attempt_at)` and `(connection_id, id DESC)`.
       - The URL is NOT stored. The worker reads the connection at send time, so secrets stay in one table, and a deleted connection's rows cascade away.
    2. Dispatch becomes Enqueue: it renders the message and INSERTs one row per subscribed, enabled connection. That's fast, so the bus channel drains immediately.
    3. internal/notify/queue.go, `RunWorker(ctx)`, started from main.go:
       - At start, reset 'sending' rows to 'queued' (crash recovery).
       - Every 2s, or at once on an enqueue wake channel, claim up to 20 due rows.
       - At most 3 concurrent sends, serialised per connection through an in-flight set.
       - On error: attempts++, next_attempt_at = now + [1m, 5m, 30m][attempts-1]. After 4 attempts the row is failed.
       - The connection being disabled marks pending rows failed ('connection disabled').
    4. Test stays synchronous, so the UI gets the answer, and also records a delivery row.
    5. Scheduler task 'notify-prune' daily (label 'Clear old alert history'): delete rows older than 30 days.
    6. API:
       - `GET /api/v1/notifications/{id}/deliveries?limit=20` (RoleManager).
       - List rows gain last_status, last_error and last_sent_at, via one aggregate query.
    7. Alerts.tsx: a status dot on each card (green: last sent OK; red: last failed, with the error on hover or tap; grey: never sent) and a 'Recent deliveries' expander (time, event, status, error).
  - **Files:** `internal/store/migrations/NNNN_notification_deliveries.sql`, `internal/notify/queue.go`, `internal/notify/queue_test.go`, `internal/notify/notify.go`, `internal/httpapi/notifications.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`, `web/src/pages/Alerts.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With one connection pointed at a dead endpoint, other connections still receive alerts promptly. The dead one shows a red dot with the apprise error.
    - A failed delivery is retried at about 1, 5 and 30 minutes, then marked failed.
    - Alerts queued before a restart are delivered after it.
    - A burst of 100 events produces no 'event dropped' warnings for the notify subscriber.
  - **Tests:** Go TestQueueRetriesWithBackoff: a fake sender fails twice then succeeds; check attempts, next_attempt_at and the final status, using an injectable clock.; Go TestQueueMarksFailedAfterMax.; Go TestQueueSurvivesRestart: a queued row is delivered by a fresh worker, and a stuck 'sending' row is reset.; Go TestSlowConnectionDoesNotBlockOthers.; Go TestDeletedConnectionCascades.; go test -race ./internal/notify/... in Docker.
  - **Depends on:** [OBS-11](#obs-11)
  - **Risk:** Worker concurrency needs the race run in Docker. The apprise CLI contract and the '--' separator are unchanged. Keep message bodies free of secrets, since they're now persisted.
  - **Resolves:** insights-7
<a id="obs-14"></a>
- [ ] **OBS-14 · Admin Web Push as an alert channel ('This device'), reusing the existing VAPID setup** — `P2` · `S` · Phase 6
  - **Problem:** Admin alerts go out only through Apprise. The app already has Web Push (internal/push: VAPID keys, push_subscriptions, SendToUser and SendToUserAsync), but only requesters' 'ready' pings use it. An admin can't get pending-request or failure alerts on their phone without setting up an outside service.
  - **Approach:** 1. Migration NNNN_notifications_config.sql: `ALTER TABLE notifications ADD COLUMN config TEXT NOT NULL DEFAULT '{}'`. Connection gains `Config json.RawMessage`. [OBS-16](#obs-16) and [OBS-17](#obs-17) build on this column.
    2. Connection kind 'webpush' with config {user_id}. It needs no URL, and ValidateAppriseURL is skipped for this kind only. Create forces user_id to the current session's user, so an admin can't target someone else.
    3. notify.Service gains `SetPusher(Pusher)`, with `type Pusher interface{ SendToUser(ctx, userID int64, title, body, url string) }`, set from pushSvc in main.go. deliver(), or the queue worker after [OBS-13](#obs-13), routes kind=webpush to Pusher.SendToUser with the message's Link (default '/').
    4. Alerts.tsx gets a 'This device (push)' option:
       - It subscribes the current browser through the same flow NotificationBell.tsx uses (api push key → serviceWorker pushManager.subscribe → /api/v1/me/push/subscribe).
       - Then it creates the connection.
       - The card shows 'No devices subscribed' when HasSubscription is false for this browser.
       - Copy: 'On iPhone, push works only when Arrmada is added to the Home Screen.'
  - **Files:** `internal/store/migrations/NNNN_notifications_config.sql`, `internal/notify/notify.go`, `internal/notify/notify_test.go`, `internal/push/push.go`, `internal/httpapi/notifications.go`, `cmd/arrmada/main.go`, `web/src/pages/Alerts.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - An admin can enable push alerts from the Alerts page on their phone (installed PWA), and Test delivers a push.
    - With request.pending ticked on the push connection, a new request produces a push that opens the right page when tapped.
    - A manager can't create a push connection targeting another user.
  - **Tests:** Go TestDeliverWebPushRoutesToPusher, with a fake Pusher: correct user, title, body and link; no apprise call.; Go: create with a webpush kind and a different user_id stores the session user.; UI check on the owner's phone PWA: add the push channel, press Test, and the notification arrives.
  - **Depends on:** [OBS-10](#obs-10), [OBS-11](#obs-11), [OBS-13](#obs-13) (optional: route through the queue if it has landed)
  - **Risk:** iOS Web Push works only in an installed PWA; the copy says so. Push failures are already logged, and dead subscriptions are pruned by push.go.
  - **Resolves:** insights-6, product-4

#### Milestone: M5 — Integration coverage (as INT and SAFE land)

_Failing indexers, Prowlarr outages, a down FlareSolverr and stale backups show as health warnings with fix links. They then appear in Needs-you and in alerts automatically._

<a id="obs-15"></a>
- [ ] **OBS-15 · Health checks that wait on other epics: failing indexers, Prowlarr, FlareSolverr, backup age** — `P1` · `S` · Phase 9
  - **Problem:** A failing TorrentLeech or 1337x looks like 'no results'. Per-indexer errors reach only result.Errors and the log (indexer/service.go:309-318), and the panel only checks that at least one indexer is enabled. FlareSolverr is an environment-only dependency with no status (config.go:101). When it's down, both native trackers fail with errors that point at a setting that doesn't exist. Nothing warns when backups stop.
  - **Approach:** Register these checks with the [OBS-04](#obs-04) registry. None makes a live tracker call.
    1. indexers.failing reads INT's persisted per-indexer status (last_ok, last_error, consecutive_failures, failing_since):
       - An enabled indexer failing for 30+ min or 3+ consecutive times → warning: 'TorrentLeech has failed since 14:02: login failed', link /indexers.
       - Every enabled indexer failing → one error: 'Every indexer is failing — nothing can be searched'.
       - Prowlarr-synced rows (URL under the saved prowlarr_url) failing with the same error collapse into one line: 'Prowlarr is unreachable (12 indexers)'.
    2. flaresolverr.reachable: only when an enabled torrentleech or 1337x indexer exists. It calls INT's FlareSolverr Ping (POST /v1 {cmd:'sessions.list'}) on a 60s Interval with a 3s timeout. Failure → warning: 'FlareSolverr isn't answering, so TorrentLeech and 1337x can't get past Cloudflare', linking to the FlareSolverr setting INT adds.
    3. backups.stale: once SAFE's backups exist, a last successful backup older than 48h → warning (error past 7 days), linking to the backups section.
    4. Add each link constant to internal/health/links.go. They flow automatically into Needs-you ([OBS-07](#obs-07)) and health alerts ([OBS-12](#obs-12)).
  - **Files:** `internal/health/checks_external.go`, `internal/health/checks_external_test.go`, `internal/health/links.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - An indexer failing for 30+ minutes shows a warning that names it with its last error and a Fix link, and the warning clears after it recovers.
    - With every indexer failing, a single error appears; 12 failing Prowlarr rows collapse into one line.
    - Stopping the FlareSolverr container (with a TorrentLeech or 1337x indexer enabled) shows the warning within ~1 minute. Without such an indexer, there's no warning.
    - GET /api/v1/system/health still answers from cache while FlareSolverr is down.
  - **Tests:** Go table test with a fake indexer status snapshot: failing for 40 min gives a warning; all failing gives an error; Prowlarr rows collapse.; Go: the FlareSolverr check is skipped without a TL or 1337x indexer; a fake Ping failure gives a warning.; Go: backup age thresholds against a fake clock.
  - **Depends on:** [OBS-04](#obs-04), INT — persisted per-indexer status with consecutive failures (draft integrations.t1), INT — FlareSolverr saved setting and Ping (draft integrations.t9), SAFE — nightly DB backups with a last-success timestamp (drafts system.t3 / product.t2)
  - **Risk:** Flaky public trackers could make the panel noisy, hence the 30-minute or 3-failure threshold and the Prowlarr collapse. Ship each check as soon as its dependency lands; they're independent.
  - **Resolves:** integrations-1, integrations-13, product-5, system-8

#### Milestone: M6 — Alert polish

_Non-technical setup through Discord/Telegram/ntfy/Pushover/email presets with masked secrets. Stream alerts that don't spam on binge-watching. Convert problems and a daily Convert summary. Templates with posters and deep links. Recent jobs on the Tasks page._

<a id="obs-16"></a>
- [ ] **OBS-16 · Preset forms (Discord, Telegram, ntfy, Pushover, email) that build the Apprise URL, with masked secrets** — `P2` · `M` · Phase 9
  - **Problem:** Setup is a name, a free-text Apprise URL and checkboxes (Insights.tsx:853-899), so a non-technical admin has to know discord://id/token syntax. The full URL, including Discord webhook tokens and SMTP passwords, goes to Manager-role users in list responses and sits in a plain text input. The Plex token, by contrast, is masked.
  - **Approach:** 1. internal/notify/presets.go: `BuildURL(kind string, f map[string]string) (string, error)`, plus `Presets []PresetDef{Kind, Label, Fields []FieldDef{Name, Label, Hint, Secret, Required}}`:
       - discord: a pasted https://discord.com/api/webhooks/{id}/{token} → discord://{id}/{token}
       - telegram: bot token + chat id → tgram://{token}/{chat}
       - ntfy: server URL + topic + optional access token → ntfy(s)://[token@]host/topic
       - pushover: user key + app token → pover://{user}@{token}
       - email: SMTP host, port, user, password, from, to, TLS → mailtos://user:pass@host:port?from=&to=, with every part URL-escaped
       - custom: a raw Apprise URL
       - webpush, from [OBS-14](#obs-14)
    2. On save, the server builds the URL, runs ValidateAppriseURL, and stores both the fields (in notifications.config) and the URL.
    3. Masking: List and Get blank every Secret field and return `<field>_set: true`. A blank secret on update keeps the stored one. For custom kinds the URL is returned masked as scheme://host/…last4, and an update with the masked value or an empty value keeps the stored URL. Test with an id uses the stored secrets server-side.
    4. `GET /api/v1/notifications/presets` returns the PresetDefs.
    5. Alerts.tsx:
       - 'Add alert' first asks 'Where should alerts go?' with preset tiles in the existing card style.
       - Then it shows the preset's fields with one-line hints (for example 'Discord: Server Settings → Integrations → Webhooks → Copy URL'), a Test button and the event checklist.
       - Secrets show as '•••• saved' with a Replace button.
       - Existing raw connections show as 'Custom (Apprise URL)'.
    6. Credentials are always entered in the UI; nothing goes in chat or the repo.
  - **Files:** `internal/notify/presets.go`, `internal/notify/presets_test.go`, `internal/notify/notify.go`, `internal/httpapi/notifications.go`, `internal/httpapi/notifications_test.go`, `web/src/pages/Alerts.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - An admin can add a Discord alert by pasting only the webhook URL, and Test delivers.
    - An email preset with a password containing '@' and '/' builds a working mailtos URL.
    - Saved secrets show as 'saved' and never come back in API responses, for custom URLs too.
    - Old raw-URL connections still work and are labelled Custom.
  - **Tests:** Go TestBuildURL: a table test per preset, covering escaping and missing-field errors.; Go TestPresetUpdateKeepsSecret, and TestListMasksCustomURL.; UI check: create one connection per preset against the owner's own services, with the credentials entered in the UI, and send Test.
  - **Depends on:** [OBS-10](#obs-10), [OBS-14](#obs-14) (notifications.config column), SEC — role access to notification settings and Apprise URL exposure (coordinate; SEC may make this admin-only), PLEX — secret-masking model (draft insights.t10), to stay consistent if it lands first
  - **Risk:** Apprise URL formats per service must match the bundled Apprise version, so verify each preset with a real Test. Masking must cover every response path (list, get, create echo).
  - **Resolves:** insights-7, product-8
<a id="obs-17"></a>
- [ ] **OBS-17 · Stream alert filters (users, remote only, transcode only), autoplay suppression and a buffering cooldown** — `P2` · `S` · Phase 17
  - **Problem:** plex.stream.started fires for every autoplayed episode, because the rating_key split at poller.go:110 starts a new session. plex.buffering fires for every counted stall. There are no filters or cooldown, so a binge-watcher produces one ping per episode. publish() (poller.go:216-228) sends only user, title, player, platform and decision, so filters can't key on user id or LAN/WAN.
  - **Approach:** 1. insights publish() payload adds:
       - user_id
       - rating_key
       - grandparent_title (ShowTitle)
       - media_type
       - location: lan/wan, using the same isLocalIP logic as the bandwidth split
       - session_key
       - buffer cause (from BufferCause) for buffering
    2. Connection filters live in notifications.config: {stream_users:[plex user ids], remote_only, transcode_only}. internal/notify/filters.go applies them before enqueueing plex.stream.started and plex.buffering.
    3. Autoplay suppression: an in-memory map in notify, keyed by connection+user_id+grandparent_title (rating_key for movies), drops a stream.started within 90 min of the previous one for the same key.
    4. Buffering cooldown: at most one plex.buffering alert per connection+user_id+rating_key per 15 min.
    5. Alerts.tsx: under the Plex events, add a user multi-select (from /insights/users), 'Only remote streams' and 'Only transcodes'.
  - **Files:** `internal/insights/poller.go`, `internal/insights/poller_test.go`, `internal/notify/filters.go`, `internal/notify/filters_test.go`, `internal/notify/notify.go`, `web/src/pages/Alerts.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Watching 5 episodes back-to-back sends one 'Now playing' alert.
    - With 'Only remote streams' on, a LAN stream sends nothing.
    - A stream that stalls 10 times in 10 minutes sends one buffering alert.
    - Filtering to one user sends only that user's streams.
  - **Tests:** Go TestStreamFilters: a matrix over users, remote_only and transcode_only.; Go TestAutoplaySuppression: the same user and show within 90 min gives one alert; a different show gives two.; Go TestBufferingCooldown.; Go (insights): the publish payload includes location and user_id.
  - **Depends on:** [OBS-11](#obs-11), [OBS-14](#obs-14) (config column), PLEX — no duplicate stream-started after restart (draft insights.t5)
  - **Risk:** Suppression state is in memory and resets on restart, which is acceptable once PLEX's restart fix lands. Plex events are the owner's watch monitoring of their own server; none of this touches audiobook listening.
  - **Resolves:** insights-7
<a id="obs-18"></a>
- [ ] **OBS-18 · Convert alerts: immediate problems (gave up, stuck) and one daily summary** — `P2` · `S` · Phase 11
  - **Problem:** Convert publishes nothing on the bus, and notify subscribes to none of its outcomes. A file blocklisted after repeated failures, or a queue stuck for lack of scratch space or hold space, is visible only on the Convert page.
  - **Approach:** 1. Catalog defs:
       - group convert (Needs-you styling):
         - convert.failed: '⚠️ Convert gave up on <title> after 3 tries — <note>', DefaultOn
         - convert.stalled: '⏸ Convert is stuck: needs 91 GB scratch, has 40 GB', DefaultOn
       - group convert (activity):
         - convert.summary: 'Convert: 7 files, 312 GB freed, 2 problems', DefaultOn off
       Topics are those CONV publishes (convert.blocked or convert.failed, convert.stalled, convert.done).
    2. convert.failed and convert.stalled dispatch immediately. An in-memory dedupe allows one stalled message per reason per 6h.
    3. Digest:
       - notify accumulates convert.done {saved_bytes} and problem counts into the settings key 'notify_convert_digest' {date, files, saved_bytes, problems}, persisted on each update so a restart keeps it.
       - A scheduler task 'convert-digest' (every 15 min, label 'Send the daily Convert summary') dispatches convert.summary once local time is ≥ 09:00, last_sent_date ≠ today, and there is something to report. Then it resets.
    4. If CONV exposes a stalled status, add an attention provider item 'convert:stalled', so it also shows in Needs you and flows through [OBS-12](#obs-12).
  - **Files:** `internal/notify/catalog.go`, `internal/notify/convert_digest.go`, `internal/notify/convert_digest_test.go`, `internal/attention/providers.go`, `cmd/arrmada/main.go`, `web/src/pages/Alerts.tsx`
  - **Acceptance:**
    - With a connection subscribed to Convert problems, a blocklisted file sends one alert immediately.
    - A day with conversions produces one summary message, not one per file.
    - Restarting mid-day neither loses the counts nor sends a second summary.
  - **Tests:** Go: a fake bus convert.failed gives one dispatch with key convert.failed.; Go: the digest aggregates 3 convert.done into one message at 09:00 on an injectable clock, and doesn't resend the same day after a simulated restart.
  - **Depends on:** [OBS-11](#obs-11), [OBS-12](#obs-12) (for the Needs-you item), CONV — convert.done/failed/blocked/stalled bus events (draft convert.t16)
  - **Risk:** Low. Persist last_sent_date so restarts don't spam. Never run real conversions against the owner's library to test this: use fake events.
  - **Resolves:** convert-11
<a id="obs-19"></a>
- [ ] **OBS-19 · Alert templates with an attached poster and a deep link back to the app** — `P3` · `M` · Phase 17
  - **Problem:** Messages are fixed emoji strings (notify.go:166-193) with no poster and no link back to the item in Arrmada. There's no setting for the app's public URL, so links can't be built at all.
  - **Approach:** 1. New setting `app_public_url` (Settings → System → General, for example https://arrmada.example.lan), validated as an http(s) URL. CFG may move it later.
    2. Each EventDef gets a default template with the placeholders {title} {user} {year} {media} {link}. An optional per-connection override per event key lives in notifications.config.templates, edited in a collapsible 'Customise message' section.
    3. Payload enrichment: movie, series and book publishers include the stored poster URL (TMDB or Open Library, already public). For Plex stream events, fetch the image server-side through plex.Client.Image into a temp file under the scratch dir, and delete it after delivery.
    4. Migration: add an `attach` value when enqueueing; the column exists from [OBS-13](#obs-13). appriseArgs gains `-a <attach>` placed BEFORE the '--' separator, and the separator stays so URLs can't inject flags.
    5. Links: {public}/movies/{id}, /series/{id}, /books/{id}, /review, /discover and /system/status. They're omitted when app_public_url is unset. Web Push uses the relative path regardless.
  - **Files:** `internal/notify/catalog.go`, `internal/notify/templates.go`, `internal/notify/templates_test.go`, `internal/notify/notify.go`, `internal/notify/notify_test.go`, `internal/notify/queue.go`, `internal/httpapi/settings.go`, `web/src/pages/Settings.tsx`, `web/src/pages/Alerts.tsx`
  - **Acceptance:**
    - A movie import alert on Discord shows the poster and a link that opens the movie in Arrmada.
    - With app_public_url unset, alerts still send, without links.
    - A custom template on one connection changes only that connection's text.
  - **Tests:** Go TestAppriseArgsAttachBeforeSeparator (extends the existing separator test).; Go TestTemplateRender, with and without a public URL, and with unknown placeholders left blank.; Go TestPlexPosterTempFileCleanedUp, including on delivery failure.; Go: the Plex token never appears in an attach path or URL.
  - **Depends on:** [OBS-11](#obs-11), [OBS-13](#obs-13), [OBS-16](#obs-16)
  - **Risk:** Temp files must be removed after the final attempt, not after the first. TMDB poster URLs are public, but the Plex token must never appear in an attach URL; fetch server-side.
  - **Resolves:** insights-7
<a id="obs-20"></a>
- [ ] **OBS-20 · Recent jobs list on the Tasks page (after BE's job runner)** — `P3` · `S` · Phase 17
  - **Problem:** 'Search now' and other HTTP-triggered work runs in untracked goroutines that report only to the log (handleSearchMovie returns 202 'searching'). Once BE adds a job runner with a jobs table, the owner still needs to see recent jobs next to the scheduled tasks.
  - **Approach:** 1. `GET /api/v1/system/jobs?limit=50` (RoleManager) returns BE's jobs rows: kind in words, a linked target (movie, series or book), trigger (who, or which task), status, duration and error.
    2. TasksTable.tsx gets a 'Recent jobs' section below the tasks, in the same row style, with a link to the target.
    3. It refreshes live on 'job.updated' through FE's shared live hook, falling back to a 15s poll that pauses in hidden tabs.
    4. Tasks rows show a job's running state when BE routes scheduled tasks through the runner, using the same TaskStatus shape as [OBS-03](#obs-03).
  - **Files:** `internal/httpapi/tasks.go`, `internal/httpapi/tasks_test.go`, `web/src/components/system/TasksTable.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Clicking 'Search now' on a movie shows a job row that goes Running → OK or Failed, with the error.
    - Each job links to its movie, series or book.
  - **Tests:** Go: the jobs handler merges jobs rows with their targets (fake runner).; UI check: Search now on a test title shows Running → OK.
  - **Depends on:** [OBS-06](#obs-06), BE — job runner with a persisted jobs table and job.updated events (draft backend.t18)
  - **Risk:** Low. Keep the API stable if BE replaces the scheduler internals.
  - **Resolves:** backend-9

#### Risks

- Alert floods on first deploy or after an outage. OBS-12 builds in first-run seeding, a 5-minute boot grace and two-run confirmation for health, a 6h flap guard and batching above 5.
- Leaks to requesters. The realtime hub broadcasts every bus topic to every connected client (realtime/hub.go Run), including requesters. New topics must carry only counts, keys and task names until SEC's topic filter lands, and /health/system moves to RoleManager.
- Each useLive() call opens its own WebSocket (web/src/lib/useLive.ts). The sidebar badges and the Status page must use one shared poller, or FE's shared live hook, not per-component sockets.
- Migration number collisions. Several epics add migrations after 0089 at the same time. Claim the next free number at implementation time, pull main first, and keep each migration self-contained.
- Hammering external services. Health checks have per-check intervals (TMDB 6h, Plex identity 5 min, FlareSolverr 60s) and 3s timeouts, and HTTP GETs never trigger live calls. Attention reads the queue once per 30s refresh no matter how many tabs poll.
- False positives around container restarts, for example qBittorrent or Plex coming up after Arrmada. Handled by Plex's 10-minute persistence rule, the alert boot grace and two-run confirmation.
- Concurrency bugs. The scheduler rewrite (running flag, kick channel, persistence), the attention refresh and the delivery worker are all concurrent. Run go test -race in Docker before every push.
- Stall detection must never reuse the cached per-client states or the attention queue snapshot; it needs a fresh QueueComplete.
- Preset URL syntax must match the bundled Apprise version. Verify each preset with a real Test from the UI, with the owner entering their own credentials.
- Privacy: no alert, event or attention item may reveal what anyone listens to. Book alerts name imports only.

#### Out of scope

- Per-indexer status persistence and backoff, error banners in search modals, and status dots on the Indexers page (INT).
- Editing, enabling or prioritising download clients, and the FlareSolverr settings UI (INT).
- A Plex partial scan after an import or Convert swap: the first half of convert-11 (PLEX/CONV).
- The job runner, recovery in HTTP-spawned goroutines, and single-flight 'Search now' (BE). OBS-03 only adds recover() inside scheduler.exec.
- Websocket topic filtering and manager-versus-admin permissions (SEC).
- Nav regrouping, the Activity hub that merges Downloads/History/Review, the Settings/System hub layout, and the Dashboard System card's real version and DB size (FE/ACQ/CFG).
- Requester-facing notifications such as request ready, the inbox and the requester push prompt (REQ/APP).
- Quiet hours, and digests other than Convert's daily summary.
- Any alert or event about audiobook listening (standing privacy rule).
- A restart-pending warning when library folders are changed in the app (CFG).

