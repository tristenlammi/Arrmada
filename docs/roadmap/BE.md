# BE — Backend architecture & quality

_Part of the [Arrmada roadmap](../../ROADMAP.md). 13 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Make Arrmada's backend fail safe. Background work can't crash the app and can be seen and followed. Anything that changes state (attaching an import, forgetting a deleted file, reindexing, telling a requester their title is ready) is a direct call or a durable outbox row, never a lossy bus message. Database access fails closed and uses one transaction helper. The migration runner can't cascade-delete children. The paths that destroy or replace files are pinned by tests. A few shared primitives (safego, jobs, outbox, store.WithTx, a queue snapshot) let the ACQ, PLEX, REQ, CFG, OBS and FE epics build on them instead of each inventing their own.

**Why.** The leaf code is solid. The structure underneath is where the next bugs come from.

- **Finished movie imports can stay "Wanted" forever (backend-6).** library.Manager.Process records the import, then announces it on an event bus that drops messages when a subscriber's 64-slot buffer fills (eventbus.go:77-83). The only code that flips the movie to Downloaded is the WatchImports subscriber (coordinator.go:1530). After that, a recorded import is never retried (manager.go:166-170). A dropped event, or a restart at the wrong moment, therefore means: no Downloaded state, no "your request is ready" message, no Convert or Subtitles indexing, and a possible re-grab. `file.removed` has the same weakness, so deliberately deleted files can come back. The review "Import anyway" path for movies publishes nothing at all.
- **One panic restarts the app, and "Search now" tells you nothing (backend-9).** recover() exists only in the HTTP middleware and the audiobook server. scheduler.exec, every bus loop in main.go, about 25 HTTP-spawned goroutines (using context.Background()) and the request-approval searches have none. A nil dereference in a sweep kills mid-encode and mid-stream. Search returns 202 and reports the result only to the log. Two clicks run two concurrent searches for the same movie.
- **A future table rebuild would silently wipe child rows (backend-5).** Every migration runs in a transaction with foreign_keys(ON). PRAGMA foreign_keys is a no-op inside a transaction. So a future rebuild of users, series, books, artists or albums would cascade-delete audiobook progress, sessions, episodes and similar rows while the app still looks healthy.
- **SQLite usage fails open (backend-10).** Transactions are DEFERRED, so read-then-write transactions can hit BUSY_SNAPSHOT. Multi-step writes aren't atomic. settings.Get returns the default on any error, so the music root can silently revert. blockedSetOf and pendingGrabTitles return empty sets on error, so blocklisted fakes become grabbable.
- **Weak tests and a real bug on the destructive paths (backend-16).** httpapi has 10.9k source lines and 769 test lines; movies has 1.9k and 244. Delete, recycle-on replacement and the import→attach handoff are untested. The import "same size means already imported" shortcut (importer.go:1437, 1466) means a different release of identical byte size is never placed, yet its name and quality get stamped on the old file.
- **Live progress can't be pushed (frontend-14).** About 29 UI polling loops exist partly because the bus has no download-progress or request-changed topic. Each poll fans out to qBittorrent separately: 23 Queue() call sites, none cached.

**Depends on:** SAFE: [BE-12](#be-12) needs SAFE's 'refuse instead of hard-delete when recycling fails' (draft backend.t13) and series delete going through the recycle bin, which define the failing-recycle test expectations; SAFE (other direction): SAFE's pre-migration VACUUM INTO snapshot plugs into [BE-01](#be-01)'s store.OpenWith BeforeMigrate hook, so land [BE-01](#be-01) first or together. SAFE's per-root recycle bins reduce the sweep-time risk [BE-03](#be-03) adds; SEC (soft): websocket topic filtering by role (backend-4) must exist before job.updated, queue.progress or request.updated carry anything beyond ids. New staff routes (/api/v1/jobs, /api/v1/system/tasks) go in SEC's staff router and its route-walk test if that has landed; FE (soft): [BE-10](#be-10)'s useJob should subscribe through FE's shared socket/useLiveQuery once it exists. FE consumes [BE-13](#be-13)'s queue.progress and request.updated to retire its polling loops (FE depends on [BE-13](#be-13)); ACQ (other direction): the acquisition core builds on [BE-03](#be-03) (attach state), [BE-06](#be-06) (outbox), [BE-07](#be-07)/[BE-09](#be-09) (jobs and claims), [BE-04](#be-04) (WithTx, fail-closed reads) and [BE-13](#be-13) (queue snapshot), and adds indexer-failure detail to [BE-10](#be-10)'s SearchOutcome; PLEX/REQ (other direction): the Plex partial scan after import and 'ready only once Plex has it' should be outbox consumers ([BE-06](#be-06)), not new bus subscribers; CFG/OBS (other direction): the System → Tasks page reads [BE-08](#be-08)'s tasks API and [BE-07](#be-07)'s jobs API. The health registry reads safego.Panics(), outbox Stats() and eventbus Drops()

#### Design

## Target architecture

### Rules (enforced by `internal/archtest`)
1. **The event bus is best-effort and only for the UI and admin alerts.** The realtime hub (websocket) and notify (admin Apprise alerts) are the only allowed `bus.Subscribe` callers. Anything that must happen goes another way:
   - a direct synchronous call (import attach, forget-on-delete), or
   - a durable **outbox** row (reindex, request-ready, audiobook cache, and later the PLEX scan).
2. **No naked goroutines.** Every long-running loop runs in a `safego.Group` tied to `runCtx`. Every fire-and-forget unit runs through `safego.Go`/`Call`. Every on-demand unit of work (search, scan, import, sweep) is a **job**. A panic logs a stack, publishes `system.panic`, and never kills the process.
3. **One way to do multi-step writes:** `store.WithTx` (BEGIN IMMEDIATE through `_txlock=immediate`). Safety reads (blocklist, pending grabs) return errors, and their callers skip the grab rather than proceed.
4. **Settings live in memory with write-through.** `settings.Service` is the only code that touches the `settings` table.
5. **Migrations are transactional by default.** Rebuilding a table that other tables reference requires the `-- arrmada:foreign-keys-off` directive, which runs the body with FKs off outside the tx plus `foreign_key_check`. A lint test enforces this.
6. **httpapi handlers stay thin:** parse, authorize, call one service method or submit one job, respond. No `go` statements and no `context.Background()`.

archtest is a test-only package. It walks `internal/` with go/parser and regexes, and each task adds its own rule.

### New primitives
- **`internal/safego`**
  - `Go(log, name, fn)`, `Call(log, name, fn) error` (a panic becomes an error), and `Loop(ctx, log, name, fn)` (restarts after a panic with 1 s→1 min backoff).
  - `Group{Go, Loop, Wait(timeout) []string}` reports the names of goroutines still running at shutdown.
  - `SetPanicHook(func(name string))`, which main wires to `bus.Publish("system.panic", {name})`.
  - A panic counter for OBS.
- **`internal/jobs`**: `Runner{Submit, Run, Get, List, Cancel, Shutdown}`.
  - Single-flight per (kind, target). A second submit returns the running job's id with `existing=true`.
  - Per-class concurrency limits: indexer-search 2, library-scan 1, import 1, external-import 1, default 2.
  - Progress writes are throttled to 1/s. A panic sets status `panicked`.
  - On boot, rows left queued or running are marked `interrupted`.
  - Publishes `job.updated` (staff topic).
- **Scheduler on the runner.** Each tick goes through an injected executor: single-flight with Run now, panic-safe.
  - Per-task state lives in memory and in `scheduled_tasks`, one row per task, persisted on status change or every 5 min.
  - Scheduled ticks create no `jobs` rows. Run now does.
- **`internal/outbox`**: `Register(topic, consumer, handler)`, `Enqueue(ctx, q Execer, topic, payload, dedupeKey)` (one row per registered consumer, works on `*sql.DB` or `*sql.Tx`), and `Run(ctx)`.
  - The dispatcher polls every 2 s or on nudge, with a per-handler timeout and recover.
  - Backoff runs 30 s→1 h. After 20 attempts a row is marked failed and stays visible.
  - Done rows are pruned after 7 days. Delivery is at-least-once, so handlers must be idempotent.
- **`store.WithTx(ctx, db, fn)`**: retries BEGIN on SQLITE_BUSY up to 3× with jitter, and rolls back on error or panic.
- **`download.Service.Snapshot`**: a cached queue read with a 2 s TTL, single-flight, invalidated by any write action. HTTP read paths use it. ACQ moves the sweeps onto it later.

### Data model (new migrations take the next free number at implementation time; latest today is 0089, and other epics add migrations too)
- `imports` gains `attach_state TEXT NOT NULL DEFAULT 'attached'`. States: pending, attached, unmatched, refused, gone. Legacy rows stay 'attached'.
- `imports` also gains `attach_attempts`, `attach_error`, `attach_next_at`, `release_name` and `year`. Pending attaches retry from the DB, so they don't depend on the torrent still being in the client.
- `outbox(id, topic, consumer, payload JSON, dedupe_key, created_at, attempts, next_at, done_at, failed_at, last_error)`, with a due index and a partial unique index on (consumer, dedupe_key) for pending rows.
- `jobs(id, kind, target, trigger, status, progress, message, error, result JSON, created_at, started_at, finished_at)`. Indexes on (kind, target, id DESC), status and finished_at. Pruned at 14 days or 5000 rows.
- `scheduled_tasks(name PK, every_seconds, last_started_at, last_finished_at, last_duration_ms, last_status, last_error, runs, failures)`.

### Import flow (after [BE-03](#be-03) and [BE-06](#be-06))
1. The 30 s `import-completed` sweep calls `Manager.Process`. The file is placed and `imports` is recorded as pending, with release_name and year.
2. Process calls `attach(rec)`, which is `Coordinator.AttachMovieImport`. It looks up the grab hash, then falls back to Match, then calls `movies.MarkImported`.
3. MarkImported does the replacement, then the DB writes, then `outbox.Enqueue('movie.imported')`.
4. The row is set to attached. `movie.downloaded` and `download.imported` still go on the bus for the UI and admin alerts.
5. A crash anywhere leaves the row pending, and `RetryPendingAttach` finishes it on the next sweep. MarkImported is idempotent for the same path.
6. Outbox consumers:
   - movie.imported: `convert`, `subtitles`, `requests.ready`
   - series.imported: `convert`, `subtitles`, `requests.ready`
   - book.imported: `requests.ready`, `audioserver.cache`
   - later: `plex.scan` (PLEX)
7. Deleting a file calls `imports.MarkRemovedByTarget` synchronously from `movies.removeFile`. `file.removed` stays on the bus for the UI only.

### API surface (staff routes; registered in SEC's staff router if that has landed)
- `GET /api/v1/jobs?kind&target&status&limit`, `GET /api/v1/jobs/{id}`, `POST /api/v1/jobs/{id}/cancel`
- `GET /api/v1/system/tasks`, `POST /api/v1/system/tasks/{name}/run` (admin)
- Every 'search/scan/import/sweep' POST keeps its existing fields and adds `job_id` and `existing`, so the change is additive.
- New websocket topics are declared in `internal/eventbus/topics.go` with an audience, for SEC's role filter:

| Topic | Audience | Payload |
|---|---|---|
| `job.updated` | staff | id, kind, target, status, progress, message, error |
| `queue.progress` | all | hash, progress, eta, state only |
| `request.updated` | owner + staff | id, status, media_type, requested_by |
| `system.panic` | staff | name |

  - Payloads carry ids only (no titles) until SEC's filtering ships.
  - Nothing carries what anyone listens to (audiobook privacy).

### UX payoff
- Search, Scan and Import buttons show "Searching…" while running and then report the result in the page's existing toast ("Grabbed …", "No releases found", "Search failed: …"). `useJob(jobId)` polls `/jobs/{id}` and uses `job.updated` when the socket is up.
- The visual style is unchanged.
- The CFG/OBS System hub reads the tasks and jobs APIs for its Tasks page.

### Package boundaries
There is no big-bang split.
- httpapi shrinks because background logic moves into jobs, and SEC splits the routers.
- automation's four pipelines are ACQ's rewrite. BE gives ACQ the primitives (attach state, outbox, jobs, WithTx, queue snapshot) so the rewrite doesn't add more ad-hoc goroutines, atomics or bus subscribers.
- Module-internal queues (subtitles ensure-jobs, the convert runner) keep their own engines and only gain panic safety.

### Process rules for every task
- Run `go test -race` in the Docker Linux runner before pushing.
- Commit straight to main, ending with the Co-Authored-By trailer.
- Never hardcode credentials.
- Never test-convert or delete the owner's real library files; tests use t.TempDir().
- main.go is touched by [BE-02](#be-02), [BE-03](#be-03), [BE-06](#be-06), [BE-08](#be-08) and [BE-13](#be-13), so land those in order.

#### Milestone: M1: Safety rails

_Migrations can no longer cascade-delete child rows. A panic anywhere in background work is logged and contained instead of restarting the app mid-encode or mid-stream. Every finished movie import attaches to its movie even after a dropped event or a crash, so movies stop sticking at Wanted and requesters get their ready message._

<a id="be-01"></a>
- [ ] **BE-01 · Migration runner: safe parent-table rebuilds (FKs off outside the tx + foreign_key_check), lint test, pre-migrate hook for SAFE's snapshot** — `P1` · `S` · Phase 0
  - **Problem:** applyOne (internal/store/migrate.go:80-94) runs every migration inside a tx on a pool whose DSN sets foreign_keys(ON) (store.go:32-36). PRAGMA foreign_keys is a no-op inside a tx. A future 'create new / copy / DROP old / rename' rebuild of users (10 child tables), series, books, artists or albums would therefore cascade-delete audiobook progress, sessions, episodes and similar rows on the owner's next ./update.sh, while the app still looks healthy. That pattern has already been used on non-parent tables in 0003, 0034 and 0059. Separately, SAFE's pre-migration VACUUM INTO snapshot needs a way to see that migrations are pending before they apply.
  - **Approach:** 1) migrate.go changes:
       - `runMigrations(ctx, db, fsys fs.FS)` takes the FS so tests can inject one; production passes migrationsFS.
       - Split out `pending(ctx, db, fsys) ([]string, error)`.
       - Add `store.OpenWith(dataDir string, opt Options)` with `Options{BeforeMigrate func(ctx context.Context, db *sql.DB, pending []string) error}`. `Open(dataDir)` becomes `OpenWith(dataDir, Options{})`. SAFE's snapshot task plugs into BeforeMigrate, which runs only when pending is non-empty.
    2) Directive handling: if the first non-blank line of a file is `-- arrmada:foreign-keys-off`, applyFKOff runs instead of applyOne:
       - `conn, _ := db.Conn(ctx)`, then `conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF")`, then verify `PRAGMA foreign_keys` returns 0.
       - `tx, _ := conn.BeginTx(ctx, nil)` and exec the body.
       - Inside the tx, run `PRAGMA foreign_key_check`. If it returns any rows, roll back and return an error listing up to 20 (table, rowid, parent, fkid).
       - Insert the schema_migrations row and commit.
       - `PRAGMA foreign_keys=ON`, verify it returns 1, then conn.Close() (which returns the conn to the pool, now safe). If restoring fails, discard the conn via `conn.Raw(func(any) error { return driver.ErrBadConn })` and return an error.
    3) A comment block at the top of migrate.go documents SQLite's 12-step rebuild recipe: new table, copy, drop, rename new→old, recreate indexes/triggers/views, keep legacy_alter_table OFF, and no BEGIN/COMMIT in the file.
    4) migrations_lint_test.go walks migrationsFS in order:
       - Collect FK parents from `(?i)REFERENCES\s+"?(\w+)` with any ON DELETE action, because DROP TABLE on any FK parent with FKs on does an implicit DELETE (cascade, set-null or failure).
       - Fail when a file without the directive contains `DROP TABLE [IF EXISTS] <parent>` or `ALTER TABLE <parent> RENAME TO`.
       - Fail when a directive file contains BEGIN or COMMIT.
       - Fail when two files share a numeric prefix. Several epics are adding migrations in parallel.
       Current parents are users, series, books, artists and albums. The existing drops (indexers, requests, convert_failures, audio_app_passwords) are not parents, so the current set passes.
  - **Files:** `internal/store/migrate.go`, `internal/store/store.go`, `internal/store/migrate_test.go`, `internal/store/migrations_lint_test.go`
  - **Acceptance:**
    - A test migration that rebuilds a parent table with the directive keeps every CASCADE child row
    - The same migration without the directive is rejected by the lint test with the file name and table
    - A rebuild that orphans a child row aborts boot with a readable foreign_key_check error, records no schema_migrations row and leaves the data unchanged
    - After a directive migration, every pooled connection reports PRAGMA foreign_keys = 1
    - store.OpenWith calls BeforeMigrate exactly once with the pending list when migrations are pending, and never when none are
  - **Tests:** Go: fstest.MapFS with parent p and child c REFERENCES p ON DELETE CASCADE, then 0002 rebuilds p with the directive. Child rows are intact and fk=1 afterwards (db.SetMaxOpenConns(1) so the same conn is reused).; Go: a directive rebuild that drops a referenced parent row returns a foreign_key_check error and rolls back; Go: without the directive the cascade really happens (documents the trap), and the lint test catches the same text; Go: lint passes on the real 0001-0089 set and fails on synthetic `DROP TABLE users;`, `ALTER TABLE series RENAME TO x;` and duplicate prefixes; Go: OpenWith BeforeMigrate is called with pending names; an error from it aborts Open without applying anything
  - **Risk:** Low. The new path runs only for opted-in files. Make sure the dedicated conn never goes back to the pool with FKs off: verify, and discard it with ErrBadConn on failure. Coordinate with SAFE so the snapshot hook lands with or after this split.
  - **Resolves:** backend-5
<a id="be-02"></a>
- [ ] **BE-02 · Panic safety net: internal/safego, a run group for every background loop, panic-safe scheduler, and HTTP goroutines tied to runCtx** — `P1` · `M` · Phase 1
  - **Problem:** recover() exists only in httpapi/middleware.go:70 and audioserver/server.go:217. The following have no recovery:
- scheduler.exec (scheduler.go:90-97)
- every long-running loop launched in cmd/arrmada/main.go: WatchImports, notifySvc.Run, hub.Run, WatchDeletions, RunNotifier, subtitlesSvc.Run, convertSvc.Run, insightsSvc.Run, audioSrv.WatchImports, the movie.downloaded loop, the book merge/upgrade goroutine and three qBittorrent retry loops
- about 25 `go func` sites in internal/httpapi, all using context.Background()
- the three approve→search goroutines in requests/service.go:183-221
One nil dereference restarts the whole app mid-encode or mid-stream. Shutdown cancels runCtx but waits only for scheduler tasks (main.go:675). HTTP-spawned work ignores shutdown entirely.
  - **Approach:** 1) New package internal/safego:
       - `Go(log, name, fn func())`: recovers, logs Error with debug.Stack(), calls the panic hook and bumps a counter.
       - `Call(log, name, fn func() error) error`: turns a panic into `ErrPanic{Name, Value}`.
       - `Loop(ctx, log, name, fn func(ctx))`: re-runs fn after a panic with backoff 1s, 2s … 1 min until ctx is done. A normal return ends it.
       - `type Group`: `NewGroup(ctx, log)`, `Go(name, fn func(ctx))`, `Loop(name, fn func(ctx))`, and `Wait(timeout) (stillRunning []string)`.
       - `SetPanicHook(func(name string))` and `Panics() uint64`.
    2) scheduler.go:
       - exec wraps t.fn in safego.Call, so a panicking task logs and the ticker keeps going.
       - Track per-task LastStart, LastDuration, LastErr, Runs, Failures and Running under the mutex, and expose `Tasks() []TaskInfo` (consumed by [BE-08](#be-08)).
    3) main.go:
       - Create runCtx and `grp := safego.NewGroup(runCtx, log)` before the first background launch. Today runCtx is created at line 264, after the book-merge goroutine at line 187.
       - Replace every `go X.Run(runCtx)` with `grp.Loop("name", X.Run)`.
       - Book merge/upgrade and the qBittorrent retries use `grp.Go` with runCtx and a ctx-aware sleep instead of time.Sleep and context.Background().
       - `safego.SetPanicHook` publishes `system.panic` {name}.
       - Shutdown: cancelRun, srv.Shutdown(8 s), sched.Wait, then `grp.Wait(5 s)`. Log any names still running.
    4) httpapi:
       - Add `RunCtx context.Context` to Deps; main passes runCtx.
       - Replace `a.bg(fn, what, id)` with `a.bg(kind, target string, timeout time.Duration, fn func(ctx) error)`. It runs safego.Go with `context.WithTimeout(a.deps.RunCtx, timeout)` and logs failures with kind and target. The signature is deliberately what [BE-09](#be-09) needs to swap in the job runner.
       - Convert every raw site to a.bg: movies.go:31 (EnsureMedia), 106, 125, 171, 206, 361; series.go:91, 108, 370, 470, 548, 617; books.go:157, 312, 325, 380, 742, 973; book_versions.go:52; music.go:225; subtitles.go:255, 277; import_overseerr.go:63; import_tautulli.go:42; audioserver.go:96.
       - activity.go:292's sleep-then-reset goroutine becomes an atomic next-allowed timestamp.
    5) Package-level fire-and-forget sites:
       - safego.Go: movies/service.go:461 (sidecar metadata), requests/service.go:183/197/213 (also switching to a ctx from an injected run context), audioserver/cache.go:122, books/upgrade.go:94, automation/books_sweep.go:55, convert/index.go:651, metadata/diskcache.go:98, push/push.go:162.
       - Fan-outs that report through channels use Call inside the goroutine, so a panicking indexer becomes that indexer's error: indexer/service.go:306 and 386, metadata/tmdb.go:227.
       - convert worker (runner.go:31) and subtitles jobs worker: wrap each job execution in safego.Call so a panic fails that one job through the module's existing failure path and the worker continues.
    6) Create internal/archtest/arch_test.go with the first rule: no ast.GoStmt in non-test files of internal/httpapi or cmd/arrmada.
  - **Files:** `internal/safego/safego.go`, `internal/safego/safego_test.go`, `internal/scheduler/scheduler.go`, `internal/scheduler/scheduler_test.go`, `cmd/arrmada/main.go`, `internal/httpapi/server.go`, `internal/httpapi/movies.go`, `internal/httpapi/series.go`, `internal/httpapi/books.go`, `internal/httpapi/book_versions.go`, `internal/httpapi/music.go`, `internal/httpapi/subtitles.go`, `internal/httpapi/import_overseerr.go`, `internal/httpapi/import_tautulli.go`
  - **Acceptance:**
    - A task that panics (test build) logs a stack and the app stays up; the next tick runs
    - A panic inside an HTTP-triggered search logs, publishes system.panic and leaves the server serving
    - On shutdown, in-flight HTTP-triggered searches see context canceled and the process exits inside the 10 s Docker stop window; leftover goroutines are named in the log
    - archtest fails if a `go` statement is added to internal/httpapi or cmd/arrmada
    - A panicking indexer in a multi-indexer search becomes that indexer's error and the other results still return
  - **Tests:** Go: scheduler, a task that panics on its first run executes again on the next tick, and TaskInfo.Failures increments; Go: safego.Loop restarts a panicking fn with backoff and stops on ctx cancel; Group.Wait returns the names of loops that ignore ctx; Go: safego.Call returns ErrPanic carrying the name; Go: a.bg with a cancelled RunCtx gives fn a done ctx immediately; Go: an indexer fan-out with one panicking fake indexer returns the other's releases plus an error for the panicking one; go test -race in Docker before pushing
  - **Risk:** A loop that panics deterministically restarts at most once a minute and logs an Error each time. OBS can alert on the system.panic topic. The compose files set no stop_grace_period, so Docker's 10 s default caps shutdown. Keep the budgets above inside it.
  - **Resolves:** backend-9
<a id="be-03"></a>
- [ ] **BE-03 · Movie imports attach synchronously with a durable attach_state; file deletions forget their import synchronously** — `P1` · `M` · Phase 1
  - **Problem:** library.Manager.Process records the import, then publishes 'download.imported' (manager.go:201-217). The only path to MarkImported is Coordinator.WatchImports, a bus subscriber (coordinator.go:1530-1574). Its events are dropped when the 64-slot buffer is full (eventbus.go:77-83), and nothing retries: a recorded import whose file exists is skipped (manager.go:166-170). A dropped event, or a restart between record and attach, leaves the movie Wanted forever with no movie.downloaded and no request-ready notification. Convert and Subtitles never index it, and it may be re-grabbed. A lower-resolution import refused by MarkImported is also silently lost today. 'file.removed' (movies.removeFile → WatchDeletions) has the same drop risk, so a deliberately deleted file whose torrent still seeds can be imported straight back.
  - **Approach:** 1) Migration (next free number, e.g. 00NN_import_attach_state.sql) adds these columns to imports:
       - attach_state TEXT NOT NULL DEFAULT 'attached' (legacy rows count as attached)
       - attach_attempts INTEGER NOT NULL DEFAULT 0
       - attach_error TEXT NOT NULL DEFAULT ''
       - attach_next_at INTEGER NOT NULL DEFAULT 0
       - release_name TEXT NOT NULL DEFAULT ''
       - year INTEGER NOT NULL DEFAULT 0
       importRepo.record writes attach_state='pending' plus release_name and year. reviews.go:731's insert keeps the 'attached' default.
    2) In library:
       - `type AttachOutcome int` with Attached, Unmatched, Refused, Gone.
       - `type AttachFunc func(ctx, rec ImportRecord) (AttachOutcome, error)` and `Manager.SetAttach`.
       - Process calls attach right after record. Attached → 'attached'. Unmatched or Refused → terminal, error stored. An error → attempts++, attach_error set, attach_next_at = now + min(1m<<attempts, 30m).
       - New `Manager.RetryPendingAttach(ctx)` runs at the end of every Process call: `SELECT … FROM imports WHERE attach_state='pending' AND removed=0 AND attach_next_at <= now LIMIT 20`, and retries from the stored release_name and year. It doesn't depend on the torrent still being in the client. A target that no longer exists becomes 'gone'.
       - If record itself fails, still call attach and log.
       - Export `MarkRemovedByTarget`.
       - download.imported is still published for UI and notifications.
    3) In automation, move the WatchImports body into `(c *Coordinator) AttachMovieImport(ctx, rec)`:
       - movieIDForGrabHash first, then movies.Match(title, year). Neither found → Unmatched.
       - MarkImported. `errors.Is(err, movies.ErrWorseQuality)` → Refused.
       - markGrabImportedForMovie, then publish movie.downloaded.
       - Delete WatchImports.
    4) Deletions:
       - movies.Service gets `SetOnFileRemoved(func(ctx, path string))`.
       - removeFile takes ctx and calls the hook synchronously before publishing file.removed (which stays, for the UI).
       - Wire the hook to imports.MarkRemovedByTarget and remove WatchDeletions.
    5) main.go: `imports.SetAttach(coordinator.AttachMovieImport)`, and drop `go coordinator.WatchImports` and `go imports.WatchDeletions`.
    6) The replacement work in MarkImported (recycling the old file) now runs inside the 30 s import sweep. That's fine because one task's runs never overlap.
    7) attach_state is surfaced read-only in GET /api/v1/history imports rows, so ACQ and OPS can show 'needs attention'.
  - **Files:** `internal/store/migrations/00NN_import_attach_state.sql`, `internal/library/manager.go`, `internal/library/repo.go`, `internal/library/manager_attach_test.go`, `internal/automation/coordinator.go`, `internal/automation/attach_test.go`, `internal/movies/service.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - Killing the app right after an import is recorded, then restarting, flips the movie to Downloaded on the next 30 s sweep, and the requester is notified
    - A forced attach error is retried with backoff and visible in imports.attach_error until it succeeds
    - A pending attach still completes after the torrent has been removed from qBittorrent
    - A lower-resolution import refused by the quality gate is recorded as 'refused' with the reason and is not retried
    - Deleting a movie file never lets its still-seeding torrent be re-imported, even with the bus saturated
    - cmd/arrmada no longer starts WatchImports or WatchDeletions
  - **Tests:** Go: manager, attach fails once → pending with error; after attach_next_at, RetryPendingAttach → attached; Go: a pre-seeded pending row with an existing target (simulated crash) and a new Manager → attached without re-importing (importer not invoked); Go: Unmatched → 'unmatched', Refused → 'refused', no further attach calls; legacy default rows are never re-attached; a missing target → 'gone'; Go: coordinator AttachMovieImport with a grab row carrying the hash → MarkImported for that movie id, grab marked imported, movie.downloaded published (test bus subscriber); Go: removeFile with a bus whose only subscriber never drains → imports.removed is still set; go test -race in Docker before pushing
  - **Risk:** Sweeps take longer when several imports replace big files across devices. SAFE's per-root recycle bins remove those slow copies. ACQ's acquisition core may later replace attach_state with its acquisitions table; keep this change small and its tests portable. Land after BE-02 if both touch main.go in the same week.
  - **Resolves:** backend-6, backend-16

#### Milestone: M2: Data and files fail safe

_Read-then-write transactions stop hitting SQLITE_BUSY. An unreadable blocklist means no grab that cycle instead of grabbing a known fake. Settings never silently revert to env defaults. A same-size but different upgrade is actually placed in the library._

<a id="be-04"></a>
- [ ] **BE-04 · SQLite: IMMEDIATE transactions, store.WithTx, and safety reads that fail closed** — `P2` · `S` · Phase 2
  - **Problem:** The DSN has no _txlock (store.go:32-36), so BeginTx is DEFERRED. The read-then-write transactions in series/repo.go:234 (RebuildEpisodes) and 476, and music/repo.go:147 and 302, can fail with BUSY_SNAPSHOT when another connection commits in between; the Insights poller writes every 5 s. Multi-step writes aren't atomic: movies.Service.Delete runs DeleteVersionsForMovie then Delete, ignoring the first error. Safety reads fail open:
- blockedSetOf returns an empty set on error (automation/store.go:107-125), so globally blocklisted fakes become grabbable.
- pendingGrabTitles and pendingSeriesGrabTitles (store.go:267-310) and pendingBookGrabTitles (books.go:1618) return nil, which allows re-grabs.
  - **Approach:** 1) store.go DSN: add `&_txlock=immediate`. modernc.org/sqlite v1.53.0 supports it (sqlite.go:266).
    2) New internal/store/tx.go:
       - `WithTx(ctx, db *sql.DB, fn func(*sql.Tx) error) (err error)`: BeginTx, retrying up to 3× with 20–80 ms jitter when errors.As finds *sqlite.Error with Code()&0xff == SQLITE_BUSY; on panic, roll back and re-panic; roll back on error; commit.
       - Also `type Execer interface{ ExecContext; QueryContext; QueryRowContext }`, shared with the outbox ([BE-06](#be-06)).
    3) Use WithTx in:
       - series/repo.go:234 and 476, music/repo.go:147 and 302 (replacing the BeginTx boilerplate).
       - movies.Service.Delete: add repo methods that take an Execer. Collect version file paths, delete the version rows and the movie row in one tx, and recycle files only after commit. A failure then leaves an orphan file, never a dangling row pointing at a recycled file.
       - books foldInto only if BOOK keeps the automatic merge.
    4) Fail-closed safety reads:
       - These return (map[string]bool, error): blockedSetOf/blockedSet/blockedSetSeries/blockedSetBook/blockedSetMusic, pendingGrabTitles, pendingSeriesGrabTitles and pendingBookGrabTitles.
       - Grab paths skip that item this cycle with Warn "blocklist unreadable — not grabbing <title> this round": coordinator.go:643/662/848/864/955, series.go:370/373/994, series_reliability.go:198/224, books.go:198/1722/1840, books_versions.go:104 and music.go:458.
       - Interactive ranking (RankReleasesWith coordinator.go:466, series_interactive.go:165) returns the error, so the UI shows a failure rather than presenting blocklisted releases as grabbable.
    5) archtest rule: no `.BeginTx(` outside internal/store in non-test files.
  - **Files:** `internal/store/store.go`, `internal/store/tx.go`, `internal/store/tx_test.go`, `internal/automation/store.go`, `internal/automation/coordinator.go`, `internal/automation/series.go`, `internal/automation/series_reliability.go`, `internal/automation/series_interactive.go`, `internal/automation/books.go`, `internal/automation/books_versions.go`, `internal/automation/music.go`, `internal/movies/service.go`, `internal/movies/repo.go`, `internal/series/repo.go`
  - **Acceptance:**
    - No 'database is locked'/BUSY_SNAPSHOT from read-then-write transactions under concurrent autocommit writers in a stress test
    - With blocklist reads failing, no grab happens that cycle and a Warn line names the title
    - movies.Service.Delete is atomic: an injected failure on the second statement leaves both rows in place and no file recycled
    - archtest fails on a new BeginTx outside internal/store
  - **Tests:** Go: two goroutines × 200 WithTx read-then-write iterations plus a third goroutine doing autocommit inserts complete with zero errors; Go: WithTx rolls back on error and on panic (the panic is re-raised); Go: coordinator grabMissing with the blocklist table dropped (simulated read failure) performs no grab (a fake download client records zero Adds); Go: movies Delete rollback test; go test -race in Docker before pushing
  - **Risk:** IMMEDIATE takes the write lock at BEGIN, so writers serialize sooner. Keep transactions short, never hold one across network I/O or ffprobe, and use busy_timeout(5000) for real contention. Migrations also become IMMEDIATE, which is harmless at boot.
  - **Resolves:** backend-10
<a id="be-05"></a>
- [ ] **BE-05 · Settings served from memory with write-through; no silent fallback to defaults** — `P2` · `S` · Phase 2
  - **Problem:** settings.Get returns the default on any error (settings.go:19-26), so a transient DB error silently reverts a setting. The music import root resolver (main.go:315-317) can send albums to the env default folder mid-import, and module toggles, the disk guard and naming can flip for one call. There are about 80 Get/GetBool call sites. quality/repo.go:143-160 reads and writes the settings table directly (default_profile:<type>), bypassing the service.
  - **Approach:** 1) settings.go:
       - `NewService(db) (*Service, error)` loads every row into a map under RWMutex.
       - Get and GetBool read the map and return def only when the key is absent.
       - Set holds a write mutex, upserts the DB first, updates the map only on success, and returns the error.
       - Add `Lookup(key) (string, bool)`, `All() map[string]string` (a copy) and `Reload(ctx) error` (used by tests and the restore flow in SAFE).
       - Callers' signatures don't change.
    2) main.go: fail boot loudly if NewService errors. Update the 10 test call sites.
    3) quality:
       - NewService(db, settings *settings.Service), 3 call sites.
       - service.go:36/51 use settings.Get/Set for `default_profile:<type>`.
       - Delete getSetting and setSetting from repo.go.
    4) archtest rule: no SQL matching `(?i)\b(FROM|INTO|UPDATE)\s+settings\b` outside internal/settings and the migrations.
  - **Files:** `internal/settings/settings.go`, `internal/settings/settings_test.go`, `internal/quality/repo.go`, `internal/quality/service.go`, `cmd/arrmada/main.go`, `internal/archtest/arch_test.go`
  - **Acceptance:**
    - A DB read error during an import can no longer change which library folder music goes to (values come from memory)
    - Saving a setting is visible immediately to every reader, including the quality default-profile lookup
    - A failed Set returns an error to the caller (Settings PUT shows it) and leaves the old value in effect
    - No code outside internal/settings touches the settings table
  - **Tests:** Go: Set then Get returns the new value; Set against a closed DB returns an error and Get still returns the old value; Go: NewService fails when the settings table can't be read; Go: quality DefaultProfile and SetDefaultProfile round-trip through settings.Service; Go: archtest settings rule; go test -race in Docker before pushing
  - **Risk:** Migrations that write settings run before the load, which is fine. Any future direct SQL write would bypass the cache; archtest guards that. SAFE's restore-on-next-boot works naturally because the cache loads at boot.
  - **Resolves:** backend-10
<a id="be-11"></a>
- [ ] **BE-11 · Fix the same-size import shortcut (content check) and cover importer replacement and cross-device paths** — `P2` · `S` · Phase 2
  - **Problem:** Importer.linkOrCopy (importer.go:1437) and the package-level linkOrCopy (importer.go:1466) treat any same-size destination as 'already imported'. A different release of identical byte size is never placed, yet the import is recorded and MarkImported stamps the new release's name and quality onto the old file. placeSub (importer.go:1382) has the same size-only check for subtitles. The cross-device copy branch and the replace-with-recycle path have no direct tests.
  - **Approach:** 1) importer.go: `sameContent(src, dst string, si, di os.FileInfo) (bool, error)`.
       - os.SameFile → true. Different sizes or size 0 → false.
       - Files ≤ 3 MiB: compare the full bytes. Otherwise compare SHA-256 of three 1 MiB samples (start, middle, end).
       - Use it in Importer.linkOrCopy (deciding whether to recycle the old file) and in linkOrCopy (deciding 'already').
       - placeSub uses a full-bytes compare (subtitles are small).
       - On a read error, treat the files as different and log. The replace path recycles first, so nothing is lost.
    2) A package var `linkFn = os.Link` so tests can force EXDEV and exercise the copy branch.
    3) Tests in importer_replace_test.go, all on t.TempDir() files:
       - (a) same size, different content → replaced, old file in the recycle dir
       - (b) identical content via copy → 'already', nothing recycled
       - (c) hardlink, same inode → 'already'
       - (d) EXDEV forced → 'copy', dst content equals src, no *.arrmada-tmp left
       - (e) copy failure (unreadable src) → error, old dst intact, no temp litter
       - (f) subtitle with same size and different content is replaced
  - **Files:** `internal/library/importer.go`, `internal/library/importer_replace_test.go`
  - **Acceptance:**
    - A same-size, different-content upgrade replaces the library file and the old one goes to the recycle bin
    - Re-running an import over an identical copy or a hardlink is still a no-op
    - The cross-device copy branch is exercised in CI
  - **Tests:** Go: cases (a)–(f) above, under -race in the Docker Linux runner
  - **Risk:** Hashing three 1 MiB chunks costs a little I/O, and only when sizes match, which is rare. Recycle-failure semantics during replacement are SAFE's to change; this task only pins the success paths.
  - **Resolves:** backend-16

#### Milestone: M3: Durable side effects and pinned destructive paths

_Convert and Subtitles reindexing, requester ready notifications and the audiobook catalogue refresh survive restarts and panics, and also run for review and manual imports. Movie and series delete and replace behaviour (recycle on, off and failing) is pinned by tests._

<a id="be-06"></a>
- [ ] **BE-06 · Durable outbox for import side effects (Convert/Subtitles reindex, request-ready, audiobook catalogue); bus reserved for UI and admin alerts** — `P2` · `M` · Phase 3
  - **Problem:** After an import, the side effects ride the lossy bus or an in-process hook:
- Convert IndexMovie and Subtitles OnMovieImported (the main.go:523-546 subscriber)
- the requester 'ready' notifier (requests/usernotify.go:115-148)
- the audiobook catalogue refresh (audioserver/cache.go:110)
- the series hook (coordinator.SetSeriesImportedHook, run inline in the import sweep)
A dropped event means Convert never sees a same-path upgrade, because the daily sweep skips known paths. Ready notifications then wait for the 10-minute backstop. A crash mid-hook loses the work. The review 'Import anyway' path for movies (reviews.go:357) publishes nothing, so it gets none of these side effects.
  - **Approach:** 1) Migration (next free number, e.g. 00NN_outbox.sql):
       ```sql
       CREATE TABLE outbox (id INTEGER PRIMARY KEY, topic TEXT NOT NULL, consumer TEXT NOT NULL, payload TEXT NOT NULL, dedupe_key TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next_at INTEGER NOT NULL DEFAULT 0, done_at INTEGER NOT NULL DEFAULT 0, failed_at INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '');
       CREATE INDEX idx_outbox_due ON outbox(done_at, failed_at, next_at);
       CREATE UNIQUE INDEX idx_outbox_pending_dedupe ON outbox(consumer, dedupe_key) WHERE dedupe_key != '' AND done_at = 0 AND failed_at = 0;
       ```
    2) Package internal/outbox:
       - `New(db, log)`, `Register(topic, consumer string, h func(ctx, json.RawMessage) error, opts ...Option)` with a per-consumer timeout (default 2 m).
       - `Enqueue(ctx, q store.Execer, topic string, payload any, dedupeKey string) error` uses INSERT OR IGNORE, one row per registered consumer, and nudges the dispatcher.
       - `Run(ctx)` (started with grp.Loop): every 2 s or on nudge, select up to 20 due rows and run each handler through safego.Call with its timeout. Success sets done_at. Failure sets attempts++, last_error and next_at = now + min(30s·2^attempts, 1h). After 20 attempts, failed_at is set.
       - Rows whose consumer isn't registered get last_error='no handler'.
       - `Stats(ctx)` returns pending and failed counts for OBS. `Retry(ctx, id)` is the staff action.
       - A daily 'outbox-prune' task deletes done rows older than 7 days.
       - The package doc states that handlers must be idempotent (at-least-once delivery).
    3) Producers:
       - movies.Service gets `SetOutbox(Enqueuer)`. markImported (auto, manual and review paths alike) enqueues 'movie.imported' {movie_id, path, upgrade} after its DB writes. A crash before the enqueue leaves [BE-03](#be-03)'s attach_state pending, so the retry re-enqueues; MarkImported is idempotent for the same path. Skip the duplicate 'imported' history event when the path is unchanged.
       - automation: the c.seriesImported(...) call sites (series.go import sweep, reviews.go series case) enqueue 'series.imported' {series_id, episodes} with dedupe_key 'series:<id>' only when episodes is empty. SetSeriesImportedHook is removed.
       - Book imports (books.go:914, 1549, books_versions.go:202) enqueue 'book.imported' {book_id, edition}.
    4) Consumers, registered in main.go before sched.Start:
       - movie.imported: 'convert' → convertSvc.IndexMovie; 'subtitles' → subtitlesSvc.OnMovieImported; 'requests.ready' → requestsSvc.NotifyMovieReady(ctx, id), extracted from RunNotifier.
       - series.imported: 'convert' → IndexSeries; 'subtitles' → OnSeriesImported; 'requests.ready' → NotifySeriesReady (keeps the HasWantedEpisodes check).
       - book.imported: 'requests.ready' → NotifyBookReady; 'audioserver.cache' → Invalidate plus an async Warm through safego when enabled.
       Remove the main.go movie.downloaded loop, requests.RunNotifier and audioSrv.WatchImports. The bus keeps movie.downloaded, series.imported and book.imported for the UI and notify's admin alerts. The request-ready-sweep backstop stays.
    5) Bus reliability:
       - eventbus.Bus counts drops per topic (`Drops() map[string]uint64`, for OBS).
       - The package doc states the rule: UI and admin alerts only.
       - archtest rule: `.Subscribe(` on the eventbus only in internal/realtime and internal/notify.
  - **Files:** `internal/store/migrations/00NN_outbox.sql`, `internal/outbox/outbox.go`, `internal/outbox/outbox_test.go`, `internal/movies/service.go`, `internal/automation/series.go`, `internal/automation/reviews.go`, `internal/automation/books.go`, `internal/automation/books_versions.go`, `internal/automation/coordinator.go`, `internal/requests/usernotify.go`, `internal/audioserver/cache.go`, `internal/eventbus/eventbus.go`, `internal/archtest/arch_test.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - Stopping the app between an import and its Convert reindex, then starting it, still reindexes the movie (the outbox row is processed after restart)
    - A handler that panics is recorded with last_error and retried; the app keeps running
    - Requester 'ready' notifications fire within seconds of attach, with no dependence on the 10-minute sweep
    - A movie imported through Review 'Import anyway' or manual import is indexed by Convert and Subtitles and notifies its requester
    - No eventbus.Subscribe remains outside realtime and notify
  - **Tests:** Go outbox:
  - an error is retried with backoff
  - a panic is captured as last_error
  - after 20 attempts failed_at is set
  - a new Outbox on the same DB processes leftovers
  - Enqueue in a rolled-back tx leaves no row
  - a pending dedupe_key collapses duplicates
  - prune keeps undone rows; Go: AttachMovieImport → outbox row per consumer → registered fake convert handler called exactly once; Go: reviews movie import path enqueues movie.imported; Go: eventbus drop counter increments when a subscriber buffer is full; go test -race in Docker before pushing
  - **Depends on:** [BE-02](#be-02), [BE-03](#be-03), [BE-04](#be-04)
  - **Risk:** Handlers must be idempotent. Convert IndexMovie upserts. notifyParties dedupes via the unique inbox ref and only pushes when the row is new. Verify that Subtitles OnMovieImported dedupes its ensure-job. Registration must happen before producers run (main registers before sched.Start). PLEX's 'scan after import' and REQ's 'ready once Plex has it' should be added as consumers here, not as new bus subscribers.
  - **Resolves:** backend-6
<a id="be-12"></a>
- [ ] **BE-12 · Pin movie and series delete/replace behaviour with tests (recycle on, off and failing) plus the re-import path** — `P2` · `S` · Phase 2
  - **Problem:** movies has 1,892 source lines and 244 test lines. Nothing tests Service.Delete, DeleteFile, DeleteVersionFile, removeFile with recycling on, or MarkImported replacement with recycling on (TestMarkImportedQualityGate covers only the recycle-off case). series.DeleteEpisodeFile is untested with recycling on or failing. These paths destroy or replace files, so a regression costs the owner media.
  - **Approach:** Every test works on t.TempDir() libraries and bins, never real media.
    1) movies/delete_test.go:
       - Delete(deleteFiles=true) with recycling on → the default file and every version file are in the bin, the rows are gone (atomically, [BE-04](#be-04)), and the [BE-03](#be-03) OnFileRemoved hook fired for each path.
       - Recycling off → files removed.
       - Recycling failing (bin dir read-only or on a path that can't be created) → SAFE's semantics: the call returns an error, files stay in place, rows are kept.
       - Same three cases for DeleteFile and DeleteVersionFile.
    2) movies/servicefixes_test.go: MarkImported replacement with recycling on → the old file is in the bin and the new one is recorded. The quality gate still refuses a lower resolution.
    3) series/delete_test.go: DeleteEpisodeFile with recycling on, off and failing, and whole-series delete following SAFE's 'recycle by default' rule.
    4) library/manager_test.go: when a recorded target has vanished, forgetByHash leads to a re-import that sets attach_state back to pending, and attach runs again.
  - **Files:** `internal/movies/delete_test.go`, `internal/movies/servicefixes_test.go`, `internal/series/delete_test.go`, `internal/library/manager_test.go`
  - **Acceptance:**
    - go test -race ./internal/movies/... ./internal/series/... ./internal/library/... covers every case listed
    - A change that reintroduces 'hard-delete when recycling fails' fails a test
  - **Tests:** Go: the cases listed in the approach, run under -race in the Docker Linux runner
  - **Depends on:** [BE-03](#be-03), [BE-04](#be-04), SAFE — the 'refuse instead of hard-delete when recycling fails' task (draft backend.t13) defines the failing-recycle expectations, SAFE — series delete goes through the recycle bin by default
  - **Risk:** If SAFE hasn't landed, write the failing-recycle cases as t.Skip with a pointer to the SAFE task rather than pinning today's hard-delete behaviour.
  - **Resolves:** backend-16

#### Milestone: M4: Jobs you can see

_Search, Scan and Import clicks are deduplicated, cancellable on shutdown and recorded. The buttons report what actually happened. Scheduled tasks have persisted last-run, next-run and error state, plus a Run-now API for the System Tasks page._

<a id="be-07"></a>
- [ ] **BE-07 · Job runner core: jobs table, single-flight per (kind,target), class limits, progress, cancellation, staff API** — `P2` · `M` · Phase 3
  - **Problem:** Background work leaves no record. Nobody can see what is running, what last failed or why. Two clicks on Search, or a click during the 5-minute sweep, run concurrent searches for the same movie. Ad-hoc guards stand in for single-flight in five places, each with its own status shape: api.refreshAll, api.musicScan, Insights.TryStartImport, the bookSweep status struct and the books upgrade status struct.
  - **Approach:** 1) Migration (next free number, e.g. 00NN_jobs.sql):
       ```sql
       CREATE TABLE jobs (id INTEGER PRIMARY KEY, kind TEXT NOT NULL, target TEXT NOT NULL DEFAULT '', trigger TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, progress REAL NOT NULL DEFAULT 0, message TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', result TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, started_at INTEGER NOT NULL DEFAULT 0, finished_at INTEGER NOT NULL DEFAULT 0);
       CREATE INDEX idx_jobs_kind_target ON jobs(kind, target, id DESC);
       CREATE INDEX idx_jobs_status ON jobs(status);
       CREATE INDEX idx_jobs_finished ON jobs(finished_at);
       ```
    2) Package internal/jobs:
       - `Runner{db, log, bus, root ctx}`.
       - `Spec{Kind, Target, Trigger, Class string; Timeout time.Duration; Fn func(ctx, *Progress) (any, error)}`.
       - `Submit(ctx, Spec) (id int64, existing bool, err error)`: single-flight on (kind,target) across queued and running jobs, using an in-memory map backed by the row.
       - `Run(ctx, Spec) (Job, error)` is the synchronous variant.
       - Class semaphores default to indexer-search 2, library-scan 1, import 1, external-import 1, other 2.
       - Each job runs through safego with ctx = root ctx + Timeout. Status goes queued → running → succeeded | failed | cancelled | panicked.
       - `Progress.Set(pct, msg)` writes to the DB and the bus at most once a second. The result is stored as JSON.
       - `Cancel(id)`, `Get`, `List(Filter)`, and `Shutdown(timeout)` (cancels root, waits, marks leftovers cancelled).
       - On New, rows left 'queued' or 'running' by a crash become 'interrupted'.
       - Every transition and throttled progress publishes 'job.updated' {id, kind, target, status, progress, message, error}. This is a staff topic, declared in internal/eventbus/topics.go.
       - A daily scheduled 'jobs-prune' keeps 14 days and at most 5000 finished rows.
    3) Staff API (manager+): GET /api/v1/jobs?kind&target&status&limit (default 50, max 500), GET /api/v1/jobs/{id} and POST /api/v1/jobs/{id}/cancel, in internal/httpapi/jobs.go. Deps gains `Jobs JobRunner`, an interface so handler tests can inject a fake.
    4) main.go: build the runner after the store and bus, and call `runner.Shutdown(5s)` in the shutdown sequence before grp.Wait.
    5) Privacy: kinds and targets name library items (movie:12), never listening activity. Audiobook-server jobs use target 'all'.
  - **Files:** `internal/store/migrations/00NN_jobs.sql`, `internal/jobs/runner.go`, `internal/jobs/store.go`, `internal/jobs/runner_test.go`, `internal/eventbus/topics.go`, `internal/httpapi/jobs.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - Submitting the same (kind,target) twice while the first is queued or running returns the same id with existing=true
    - A panicking job ends 'panicked' with the panic value in error, and the app stays up
    - Shutdown cancels running jobs and records them as cancelled; jobs found running at boot are marked interrupted
    - GET /api/v1/jobs/{id} shows status, progress, message, result and timings; requester and anonymous users get 403/401
  - **Tests:** Go runner:
  - single-flight id reuse
  - a class limit of 1 queues the second job
  - panic → 'panicked'
  - root cancel → 'cancelled'
  - Timeout → 'failed' with context deadline
  - progress persisted and throttled
  - interrupted-on-boot
  - prune; Go: job.updated published on each transition (test bus subscriber); Go: handler tests for list, get and cancel with a fake runner; go test -race in Docker before pushing
  - **Depends on:** [BE-02](#be-02)
  - **Risk:** Don't route the scheduler's 30 s tasks through the jobs table (BE-08 keeps them out), or it grows by thousands of rows a day. The module-internal queues stay as they are: subtitles ensure-jobs in subtitles/jobs.go and the convert runner. The name overlap is conceptual only.
  - **Resolves:** backend-9
<a id="be-08"></a>
- [ ] **BE-08 · Scheduler on the runner: persisted task state, Run-now with single-flight, tasks API** — `P2` · `S` · Phase 3
  - **Problem:** The scheduler (scheduler.go) logs the outcome of its ~25 recurring tasks and keeps nothing. A Tasks page can't show last or next run, duration or last error. A run can't be triggered on demand without risking overlap with the ticker run.
  - **Approach:** 1) scheduler.go:
       - `SetExecutor(func(ctx, name, trigger string, fn TaskFunc) error)`. The default is safego.Call.
       - Per-task `running atomic.Bool`. A tick that finds the task running is skipped with a Debug log, which keeps today's 'one task never overlaps itself' invariant.
       - `RunNow(name, trigger) (jobID int64, existing bool, err error)` submits through the jobs runner (kind 'task', target name), which makes a jobs row. If the task is running it returns the current job id, or existing=true when the run is a ticker run.
       - TaskInfo gains NextRun.
       - `Register(..., opts)` with `Quiet()` for heartbeat, so the state isn't persisted.
    2) Migration (next free number):
       ```sql
       CREATE TABLE scheduled_tasks (name TEXT PRIMARY KEY, every_seconds INTEGER NOT NULL, last_started_at INTEGER NOT NULL DEFAULT 0, last_finished_at INTEGER NOT NULL DEFAULT 0, last_duration_ms INTEGER NOT NULL DEFAULT 0, last_status TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', runs INTEGER NOT NULL DEFAULT 0, failures INTEGER NOT NULL DEFAULT 0);
       ```
       Upsert when status changes or at least 5 min after the last persist for that task. Load at boot, so 'last run 3 h ago' survives a restart.
    3) API:
       - GET /api/v1/system/tasks (manager+) → [{name, every_seconds, last_started_at, last_duration_ms, last_status, last_error, next_run_at, running, runs, failures}].
       - POST /api/v1/system/tasks/{name}/run (admin) → 202 {job_id, existing}.
       - The CFG/OBS System hub builds the Tasks page on these.
    4) Register the daily 'jobs-prune' and 'outbox-prune' tasks here if [BE-06](#be-06) and [BE-07](#be-07) haven't already.
  - **Files:** `internal/scheduler/scheduler.go`, `internal/scheduler/state.go`, `internal/scheduler/scheduler_test.go`, `internal/store/migrations/00NN_scheduled_tasks.sql`, `internal/httpapi/system_tasks.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - GET /api/v1/system/tasks lists every registered task with last run, duration, status, last error and next run, and the values survive a restart
    - Run now on 'search-missing-movies' while its ticker run is in flight does not run it twice
    - Run now on an idle task creates a jobs row that ends succeeded or failed with the error
  - **Tests:** Go: RunNow during a ticker run doesn't double-execute (counter stays 1); a tick during RunNow is skipped; Go: state persists on status change and is throttled otherwise; it reloads at boot; Go: a panicking task records last_status 'panicked'; go test -race in Docker before pushing
  - **Depends on:** [BE-02](#be-02), [BE-07](#be-07)
  - **Risk:** Low. A task that never finishes (a hung network call) blocks its own ticks. That is the same as today, but it's now visible as running=true with a start time. A per-task timeout is a later option.
  - **Resolves:** backend-9
<a id="be-09"></a>
- [ ] **BE-09 · Move HTTP- and request-triggered background work onto the job runner; per-item search claims; one media-backfill job** — `P2` · `M` · Phase 3
  - **Problem:** Searches, regrabs, scans, imports and sweeps start from handlers and from request approval with no record, no single-flight and no way to follow the outcome. Five bespoke guards and status machines duplicate single-flight:
- api.refreshAll
- api.musicScan
- Insights.TryStartImport
- the bookSweep struct with StartBookSweep's own goroutine
- books StartUpgrade's goroutine
handleListMovies spawns one EnsureMedia goroutine per stale movie on every 4 s poll (movies.go:31). A manual search and the 5-minute sweep can search the same movie at once. SearchMovie also skips the sweep's guards (coordinator.go:526-533).
  - **Approach:** 1) a.bg(kind, target, timeout, fn) (from [BE-02](#be-02)) becomes `a.deps.Jobs.Submit(...)` and returns (jobID, existing). Responses keep their existing fields and add `job_id` and `existing`. Kinds and targets:
       - movie.search movie:<id> (add, search, version add, profile-change re-search, blocklist-and-search)
       - movie.upgrade movie:<id>
       - movie.regrab movie:<id>
       - movie.scan all
       - series.search series:<id> (add, search)
       - series.grab-scope series:<id>:s<S>e<E>
       - series.regrab-episode series:<id>:s<S>e<E>
       - series.scan all
       - series.refresh-all all (drop api.refreshAll)
       - series.import-folder series:<id>
       - book.search book:<id>
       - book.search-version book:<id>:v<vid>
       - books.add-author-search author:<key>
       - books.scan all
       - books.backfill-series all
       - book.merge-audiobook book:<id>
       - books.search-missing all (StartBookSweep becomes a synchronous RunBookSweep(ctx) and keeps its BookSweepStatus)
       - books.upgrade all (StartUpgrade becomes RunUpgrade; MaybeStartUpgrade at boot submits with trigger 'system')
       - music.scan all (drop api.musicScan)
       - subtitles.search movie:<id> / series:<id>
       - convert.reindex all
       - requests.import-overseerr all
       - insights.import-tautulli all (replaces TryStartImport)
       - download.block hash:<h>
       - audioserver.warm all
    2) Where the old guards answered 409 'already running' (refresh-all, music scan, Tautulli), keep the 409 and its text and add job_id, so the UI is unchanged.
    3) Existing status GETs (books/upgrade, books/search-missing, convert/reindex) keep their response shapes.
    4) requests.Service.Approve gets an injected `jobs.Submitter` and submits movie.search, series.search or book.search with trigger 'request:<id>' instead of raw goroutines.
    5) Per-item claims in automation:
       - New internal/automation/claims.go: `c.claim(key string) (release func(), ok bool)`.
       - searchAndGrab, SearchSeriesNow, searchBookOnce and upgradeMovie claim 'movie:<id>', 'series:<id>' or 'book:<id>'.
       - Sweeps skip claimed items (Debug).
       - A manual job finding a claim returns outcome 'already being searched' and succeeds.
    6) handleListMovies: instead of per-movie goroutines, submit one 'movie.media-backfill' all job that calls a new `movies.Service.BackfillStaleMedia(ctx, ids)`, which keeps the existing probeSem bound. Single-flight makes repeated polls free.
    7) archtest rule: no `context.Background()` in internal/httpapi non-test files, with an explicit allowlist for anything that must remain.
    8) web/src/lib/api.ts: 202 response types gain `job_id?: number; existing?: boolean`. There is no visible UI change in this task.
  - **Files:** `internal/httpapi/movies.go`, `internal/httpapi/series.go`, `internal/httpapi/books.go`, `internal/httpapi/book_versions.go`, `internal/httpapi/music.go`, `internal/httpapi/subtitles.go`, `internal/httpapi/convert.go`, `internal/httpapi/import_overseerr.go`, `internal/httpapi/import_tautulli.go`, `internal/httpapi/downloads.go`, `internal/httpapi/audioserver.go`, `internal/httpapi/server.go`, `internal/httpapi/jobs_handlers_test.go`, `internal/automation/claims.go`
  - **Acceptance:**
    - Clicking Search twice on a movie produces one job; the second response has existing=true and the same job_id
    - Every search, scan, import or sweep POST returns a job_id visible at GET /api/v1/jobs/{id} with its final status and error
    - Approving a request creates a movie.search, series.search or book.search job with trigger request:<id>
    - A manual search while the sweep is searching the same movie runs no second indexer search
    - The Movies grid poll no longer spawns per-movie goroutines
    - The Books sweep and upgrade progress UIs and the Convert reindex indicator behave exactly as before
  - **Tests:** Go: handler tests with a fake runner assert Submit's kind, target and trigger, and the 202 body for each converted route (table-driven over the route list); Go: concurrent handleSearchMovie calls hit SearchMovie once; Go: claims, sweep skips a claimed movie; release on panic (deferred); Go: request Approve submits the right job; go test -race in Docker before pushing
  - **Depends on:** [BE-02](#be-02), [BE-07](#be-07)
  - **Risk:** The UIs that poll the book sweep and upgrade status endpoints must keep working, so keep their shapes and test them. Class limits (indexer-search 2) can queue a burst of 'search on add' from an author import. That is intended, and the queued jobs are visible.
  - **Resolves:** backend-9
<a id="be-10"></a>
- [ ] **BE-10 · Search, Scan and Import buttons report what actually happened (SearchOutcome + useJob)** — `P2` · `M` · Phase 3
  - **Problem:** handleSearchMovie, handleSearchSeries and handleSearchBook return 202 'searching'. Success, failure or 'nothing found' goes only to the log (movies.go:118-132). Users click Search and never learn whether anything was grabbed or why not. Library scans behave the same way.
  - **Approach:** 1) automation:
       - SearchMovie returns `(SearchOutcome, error)`, where `SearchOutcome{Searched bool; Returned, Matching, Usable, Grabbed int; GrabbedTitles []string; Reason string}`. Reason is one of nothing-wanted, no-releases, none-for-this-title, all-blocklisted-or-below-profile, grabbed or already-searching. searchAndGrab already computes these counts at coordinator.go:541-575.
       - SearchSeriesNow, SearchBookNow and SearchAudioVersionNow return the same struct.
       - ACQ will add the failed indexers when it stops counting outages as misses.
       - The job result is the outcome JSON, and the job message is a plain sentence, e.g. "Grabbed Dune.Part.Two.2024.2160p.WEB-DL", "No releases found on 3 indexers" or "12 releases found, none matched this movie".
       - Scans return their existing result structs (Imported, Skipped, Unmatched) as the job result.
    2) web/src/lib/useJob.ts: `useJob(jobId?: number)` returns {status, message, result, error}.
       - It listens for `job.updated` through the existing useLive socket when connected, and otherwise polls GET /api/v1/jobs/{id} every 1.5 s.
       - It stops at a terminal status or after 5 min, and pauses polling while document.hidden.
    3) Wire it into MovieDetail, SeriesDetail and BookDetail Search buttons, the Movies, Series, Books and Music Scan buttons, and the series folder import.
       - The button shows its existing busy state ('Searching…') while the job runs.
       - On finish, the page's existing toast shows the message (an error variant on failure).
       - Use the same components and classes already on each page: dark warm palette, terracotta accent, current type scale. No new visual language.
  - **Files:** `internal/automation/coordinator.go`, `internal/automation/series.go`, `internal/automation/books.go`, `internal/automation/books_versions.go`, `internal/automation/outcome.go`, `internal/automation/outcome_test.go`, `internal/httpapi/movies.go`, `internal/httpapi/series.go`, `internal/httpapi/books.go`, `web/src/lib/useJob.ts`, `web/src/lib/api.ts`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/SeriesDetail.tsx`, `web/src/pages/BookDetail.tsx`
  - **Acceptance:**
    - Clicking Search on a movie with no releases shows 'No releases found…' within seconds of the search finishing
    - A search that grabs shows the grabbed release name; a failed search shows the error text
    - A scan reports added and unmatched counts when it finishes
    - The buttons and toasts look exactly like today's (style unchanged)
    - Leaving the page mid-search cancels nothing; the job finishes and is visible at /jobs/{id}
  - **Tests:** Go: SearchOutcome reasons for nothing-wanted, no-releases, wrong-title, blocklisted and grabbed (fake indexer and download client); Go: job result and message for movie.search; Frontend: npm run build and typecheck; a manual check on a local instance (not the owner's library) of each button's busy → result flow; go test -race in Docker before pushing
  - **Depends on:** [BE-09](#be-09), FE — if FE's shared socket/useLiveQuery has landed, useJob subscribes through it instead of its own useLive call (soft)
  - **Risk:** There are 16 toast implementations today. Use each page's own toast rather than adding a 17th, and FE's component kit can unify them later. The extra JSON in job results is small.
  - **Resolves:** backend-9

#### Milestone: M5: Live updates

_Download progress and request status are pushed over the websocket, so the FE epic can retire the 3–8 s polling loops. qBittorrent is read at most once every 2 s however many tabs are open._

<a id="be-13"></a>
- [ ] **BE-13 · Shared download-queue snapshot; publish queue.progress and request.updated over the websocket** — `P3` · `M` · Phase 5
  - **Problem:** The bus has no download-progress or request-changed topic, so the frontend's progress bars and the Discover requests strip have to poll (3–8 s loops). Even with FE's useLiveQuery, they would still need to poll. Every poll also fans out to qBittorrent: download.Service.Queue has 23 call sites with no caching, including handleQueue (Downloads page), handleListMovies (every 4 s while anything downloads), requests, discover, dashboard, activity, fileinfo and series. The draft placed the publish in Coordinator.WatchImports, but that function never reads the queue, and BE-03 removes it.
  - **Approach:** 1) download.Service:
       - `Snapshot(ctx) (items []Item, complete bool, at time.Time, err error)`: a QueueComplete result cached for 2 s, with a small mutex-based single-flight (no new dependency).
       - Add, Remove, Pause, Resume and Action invalidate the cache.
       - Switch the HTTP read paths to Snapshot: downloadclients.go:116, movies.go:23 and 275, requests.go:36, discover.go:79, dashboard.go:118, activity.go:30, fileinfo.go:201 and series.go:143. Automation sweeps keep calling Queue/QueueComplete until ACQ moves them.
    2) Progress publisher (internal/download/progress.go):
       - `RunProgress(ctx, svc, bus, active func() bool)` runs with grp.Loop. Every 2 s, only while `hub.Count() > 0`, it reads Snapshot and builds [{hash, progress rounded to 0.5%, eta_seconds, state}] for incomplete items.
       - It publishes 'queue.progress' only when that differs from the last publish.
       - It publishes one empty list when the queue goes idle, and nothing while idle.
    3) requests.Service publishes 'request.updated' {id, status, media_type, requested_by}:
       - on Create (new and subscribed), Approve, Decline and Delete
       - in notifyReady with status 'available'
       There are no titles until SEC's topic filtering lands.
    4) internal/eventbus/topics.go declares both topics with audiences: queue.progress → all (hashes and numbers only), request.updated → owner and staff. SEC's websocket filter reads these.
    5) FE consumes these through useLiveQuery (FE epic). Nothing changes in the UI here.
  - **Files:** `internal/download/service.go`, `internal/download/snapshot.go`, `internal/download/snapshot_test.go`, `internal/download/progress.go`, `internal/download/progress_test.go`, `internal/requests/service.go`, `internal/requests/usernotify.go`, `internal/requests/service_test.go`, `internal/eventbus/topics.go`, `internal/httpapi/downloadclients.go`, `internal/httpapi/movies.go`, `internal/httpapi/requests.go`, `internal/httpapi/discover.go`, `internal/httpapi/dashboard.go`
  - **Acceptance:**
    - While a torrent downloads and a browser is connected, a websocket client receives queue.progress about every 2 s; with the queue idle, or no clients, it receives none
    - Approving, declining or withdrawing a request emits request.updated with ids and status only
    - With three browser tabs polling Downloads and Movies, qBittorrent sees at most one torrents/info call every 2 s
    - Pausing or removing a torrent is reflected on the next Downloads poll (cache invalidated)
  - **Tests:** Go: snapshot single-flight, 10 concurrent callers make 1 fake-client List call; TTL expiry; invalidation on Pause; Go: progress diff, no publish when unchanged, when moved by less than 0.5% or when idle; one publish when moved by 0.5% or more; one empty publish on the transition to idle; nothing when active() is false; Go: requests publishes request.updated on Approve and Decline (test bus subscriber); go test -race in Docker before pushing
  - **Depends on:** [BE-02](#be-02), SEC — websocket topic filtering by role (backend-4) before payloads may carry titles; until then they stay id-only (soft)
  - **Risk:** Until SEC's filtering lands, requesters' sockets also receive these events. Keep payloads to hashes, ids and status. ACQ's overhaul also calls for a shared queue snapshot. This task delivers it as a download.Service primitive so ACQ builds on it rather than writing a second one.
  - **Resolves:** frontend-14

#### Risks

- Migration numbering collisions: BE-03, BE-06, BE-07 and BE-08 add migrations while other epics add theirs. Take the next free number at implementation time; BE-01's lint test fails on duplicate prefixes
- main.go churn: BE-02, BE-03, BE-06, BE-08 and BE-13 all rewire cmd/arrmada/main.go. Land them in milestone order and rebase rather than developing them in parallel
- Moving MarkImported (recycling the replaced file) into the 30 s import sweep lengthens a sweep when big cross-device replacements happen. Acceptable because one task's runs never overlap; SAFE's per-root bins remove the slow copies
- IMMEDIATE transactions serialize writers sooner. Never hold a WithTx across network calls, ffprobe or file copies, and recycle files after commit
- Outbox delivery is at-least-once. Every consumer must be idempotent: Convert upserts and the requests inbox has a unique ref, but Subtitles' ensure-job dedupe must be verified in BE-06
- A loop that panics deterministically restarts at most once a minute. It logs an Error and publishes system.panic each time, so OBS should alert on it
- Docker's default 10 s stop window caps shutdown, and compose sets no stop_grace_period. Keep srv.Shutdown, jobs, scheduler and group budgets inside it
- The settings cache is bypassed by any future direct SQL write; the archtest rule guards that
- Job and event payloads must never carry what someone listens to (the audiobook privacy rule). Audiobook-server jobs use target 'all', and no new topic includes listening data
- ACQ's acquisition core may later replace attach_state and the attach callback. BE-03 is kept small and its tests are written against behaviour (a finished import ends attached after a crash), so they carry over

#### Out of scope

- Database backups, nightly VACUUM INTO snapshots and the Backups/restore page (SAFE). BE-01 only provides the pre-migration hook
- The acquisition core rewrite (info-hash-keyed acquisitions table, merged normalizers, indexer outages not counted as misses, re-grab guards) (ACQ)
- Splitting httpapi into staff/requester/system routers, websocket topic filtering, and the route-walk authorization test (SEC)
- The System → Tasks and Health UI pages (CFG/OBS); BE delivers their APIs
- Replacing the ~29 frontend polling loops with useLiveQuery, and the shared socket and toast kit (FE)
- Recycle-bin semantics, the cap policy and per-filesystem bins (SAFE)
- The Plex partial scan after import and 'ready once Plex has it' (PLEX/REQ, as outbox consumers)
- Re-engineering module-internal queues (convert runner, subtitles ensure-jobs); they only gain panic safety
- A big-bang reorganisation of httpapi or automation into new packages; boundaries are enforced with archtest rules instead
- PostgreSQL support

