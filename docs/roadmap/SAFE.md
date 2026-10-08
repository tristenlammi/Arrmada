# SAFE — Data safety & recovery

_Part of the [Arrmada roadmap](../../ROADMAP.md). 18 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Make every destructive action in Arrmada either reversible or deliberately confirmed. The database gets a snapshot before every schema change and every night, and it can be restored from the UI or the CLI. Every deleted or replaced file goes to a recycle bin on its own drive, and that bin never quietly hard-deletes and never purges what was just put in it. Every delete, removal or merge dialog defaults to keeping data and says exactly what will happen first.

**Why.** One SQLite file holds the whole household's state: accounts, requests, Insights watch history (often imported from Tautulli and irreplaceable), audiobook places, keys and settings. There is no backup of it anywhere. store.Open runs each update's migrations in place with no copy taken first, and update.sh prunes the previous image, so a bad update can't be undone (system-2, backend-5, product-6, all high). Several one-click paths destroy data:
- The X in Settings → Users deletes a person at once, and the delete cascades to their audiobook places and listening history (system-5).
- Delete on the Downloads page removes the torrent and its data with no question. The API even accepts `all`, which wipes every torrent in qBittorrent. Removing a download also quietly turns into "blocklist and re-grab" (ops-5).
- Deleting a whole series permanently deletes every episode by default, skips the recycle bin and leaves the subtitles behind (series-11).
- The audiobook merge hard-deletes the source files without checking that the output is complete (audiobooks-9).

The recycle bin, which is meant to be the safety net, isn't one:
- When recycling fails, four paths fall back to permanent delete.
- The 50 GB default cap purges a 4K remux within the hour, including the one just replaced (backend-8).
- On a documented install the bin sits in the managed Docker volume, so every delete is copied into docker.img and can fill it (system-3).

Deleting a movie mid-download leaves the torrent running, and the film later reappears in Plex untracked (movies-8). The movie dialogs still promise "recycle bin" when the bin is off, and version deletes fire on one click (movies-6, movies-7). The delete paths themselves have almost no tests (backend-16).

**Depends on:** FE: the dialog/modal primitive in the component kit. [SAFE-02](#safe-02) builds ConfirmDialog on it if it has landed; otherwise FE adopts [SAFE-02](#safe-02)'s component.; CFG: the Settings save fix, so recycle limits saved through the general Settings form actually persist ([SAFE-08](#safe-08)). The backup schedule has its own endpoint, so it doesn't wait on this.; CFG: one effective library-roots resolver (settings or env per library) for per-root bins ([SAFE-17](#safe-17)). Optional; [SAFE-17](#safe-17) has a fallback.; CFG: the Settings/System hub. The Backups and Recycle bin cards ([SAFE-12](#safe-12), [SAFE-16](#safe-16)) move into it, with no API changes.; SEC: an admin route group or deny-by-default auth for /system/backups ([SAFE-12](#safe-12), [SAFE-13](#safe-13), [SAFE-15](#safe-15)). Until then they use requireRole(RoleAdmin).; SEC: a Disable-user action (PUT /users/{id} {disabled}, revoke sessions and audio tokens). [SAFE-03](#safe-03) shows 'Disable instead' once it exists.; OBS: the health-check registry ([SAFE-11](#safe-11) backup age, [SAFE-16](#safe-16) bin on another drive or legacy bin). Falls back to handleSystemHealth.; OBS or CFG: a CLI subcommand dispatcher ([SAFE-15](#safe-15), [SAFE-18](#safe-18)). Optional; [SAFE-15](#safe-15) adds a minimal one if absent.; ACQ: the grab/download lifecycle work must adopt the statuses removed, cancelled and orphaned from [SAFE-04](#safe-04) and [SAFE-10](#safe-10). ACQ also owns the Importing / Held / Import failed states on the Downloads page from ops-5.; CONV: an originals hold outside the recycle bin (convert-2). It must be coordinated with [SAFE-08](#safe-08)'s cap protection, so cap semantics change once. Convert.retire moves to RemoveToBin in [SAFE-07](#safe-07).; PLEX and MOV/SUB: consume the movie.deleted event published by [SAFE-10](#safe-10) (Plex refresh, and dropping Convert/Subtitles queue items).; BE: a tracked job runner. The audiobook merge ([SAFE-06](#safe-06)) moves onto it once it exists.

#### Design

## Principles
1. **Default to keeping.** Every "also delete files" box starts unchecked. Removing a torrent keeps its files unless you choose otherwise.
2. **Deleting a file means moving it to the bin.** Only a bin that is deliberately switched off (`ARRMADA_RECYCLE_DIR=off`) hard-deletes, and every dialog says so. If the bin can't take a file, the delete is refused with a message. It never falls back to `os.Remove`.
3. **Say what will happen before it happens.** Each destructive endpoint has a preview that returns counts, bytes, destination, and any pending downloads. Dialogs show that preview. Typed confirmation is kept for things that are irreversible or very large: user delete with listening data, restore, and a series over 100 GB.
4. **Copy the database before anything rewrites it:** migrations, restore, user delete, and nightly.
5. **Privacy holds.** Impact counts and backup listings never expose titles or item keys. Admins see how much and when, never what.
6. **No new SQL schema.** All new state lives in files under `/data/backups` and in settings keys. Grab statuses are free TEXT.

## Delete semantics (target)
| Action | Default | Files go to | Confirm |
|---|---|---|---|
| Remove download (Downloads) | keep files | n/a, or client deletes | dialog: keep / delete / block+find another, plus "stop wanting"; never-imported warning |
| Delete movie / series / book | rows only | bin (or permanent when the bin is off, stated) | dialog with preview; series over 100 GB needs the title typed |
| Delete version / episode / book file | n/a | bin, sidecars included | dialog naming the file, size and destination |
| Import replacement / upgrade | n/a | old file to the bin; refused if the bin fails | none (logged plus item event) |
| Audiobook merge | n/a | sources to the bin, or `.arrmada-merge-backup/` for 14 days | inline confirm |
| Delete user | n/a | DB snapshot first | counts-only impact; username typed when they have listening data |
| Restore DB | n/a | current DB kept as pre-restore | type RESTORE |

## Recycle bin architecture
- `library.Bin` is an interface: `For(path) (dir string, err error)`. `SingleBin(dir)` is used today. `RootBins` arrives in [SAFE-17](#safe-17): `<library root>/.arrmada-recycle` for the longest matching root, an explicit `ARRMADA_RECYCLE_DIR` still wins, and `off` disables the bin.
- `library.RemoveToBin(bin, path)` is the only delete primitive:
  - `ErrRecycleDisabled` → `os.Remove`.
  - Any other error → `couldn't move X to the recycle bin (…) — nothing was deleted`.
- `library.Sidecars(video)` lists paired subtitles (factored out of `MoveEpisodeSubs`) so they travel with their video.
- Each bin contains a `.plexignore` holding `*`. Arrmada's own scanners skip dot-directories.
- `recyclebin.Service` manages a set of bins: the current bins plus the legacy `<LibraryDir>/.recycle` while it is non-empty.
  - Item IDs are `<binKey>/<rel>`.
  - Retention applies per item. The size cap applies to the total.
  - The cap pass never purges items under 72 h old or the single newest item, unless the bin's disk is below 5% free.
  - Rows show an "expires" date.
- `GET /recycle/mode` is a cheap, walk-free answer to "what happens to deleted files", used by every dialog.

## Grab lifecycle
`grabbed → imported | failed | removed (user removed from client) | cancelled (media deleted, torrent removed) | orphaned (media deleted, torrent kept)`.
- Only `grabbed` counts as pending, so stall detection, the pending-grab guard and requester progress ignore the rest.
- The movie import gate holds any finished download whose grab points at missing media for Review. It never imports it by name.

## Database safety
`/data/backups/arrmada-<kind>-<YYYYMMDDTHHMMSSZ>.db`, strict regex in `store.ParseBackupName`:
| kind | made by | keep |
|---|---|---|
| pre-migrate | boot, when migrations are pending on a non-fresh DB | 5 |
| nightly | `db-backup` task (hourly check; due when the newest is over 20 h old and past `backup_hour`, or over 36 h old) | `backup_keep_nightly` (7) |
| manual | Back up now | 10 |
| pre-restore | boot, while applying a staged restore | 3 |
| pre-delete-user | user delete | 3 |
| uploaded | upload, CLI | 3 |

**Snapshot primitive** (`Store.Snapshot`):
- free-space guard (1.2× db+wal)
- `VACUUM INTO ?` to a `.tmp` file
- read-only `quick_check`
- fsync, then rename

**Boot sequence** in `store.OpenWith`:
1. Apply a staged restore if `restore-pending.json` exists ([SAFE-13](#safe-13)).
2. Open, then ping (10 s).
3. Ensure `schema_migrations`. Refuse to start when applied versions are unknown to this binary ([SAFE-18](#safe-18)).
4. If migrations are pending and the DB isn't fresh, take a pre-migrate snapshot, or refuse (escape hatch `ARRMADA_SKIP_MIGRATION_SNAPSHOT=1`).
5. Apply migrations, each in its own transaction. Ones headed `-- arrmada:foreign-keys=off` run on a dedicated connection with FKs off and a `foreign_key_check` before commit ([SAFE-14](#safe-14)).

**Restore** is never a live swap:
- validate (integrity_check, users + schema_migrations present, no unknown versions)
- write the marker, then restart
- at the next boot: pre-restore snapshot, drop -wal/-shm, tmp + rename

A CLI (`arrmada restore …`, `arrmada backups`) covers an app that can't boot. `update.sh` tags `arrmada:previous`, and `--rollback` retags it and stages the matching pre-migrate snapshot.

## APIs
- `GET /api/v1/recycle/mode` (manager)
- `GET /recycle` and `GET /recycle/items` gain per-bin stats and `expires_at`
- `POST /recycle/empty {bin?}`
- `DELETE /queue/{hash}?mode=keep_files|delete_files|block&unmonitor=` (hex hash only)
- `GET /movies/{id}/delete-preview`
- `GET /series/{id}/delete-preview`
- `DELETE /movies/{id}?delete_files&cancel_downloads`
- `DELETE /series/{id}?delete_files&confirm`
- Refusals return 409 `{error, moved[], failed[]}`
- `GET /users/{id}/impact` (admin, counts only)
- `DELETE /users/{id}?confirm=<username>`
- Admin only, `/api/v1/system/backups`:
  - `GET`, `POST`
  - `PUT /settings`
  - `GET /{name}/download` (gzip)
  - `DELETE /{name}`
  - `POST /{name}/restore`
  - `DELETE /restore-pending`
  - `POST /upload` (.db or .db.gz, 4 GiB)

## UI shape
- `components/ConfirmDialog.tsx` supports choices, a checkbox slot, a typed phrase and inline server errors. It reuses the existing modal tokens (panel/line/shadow/reject), so nothing changes visually.
- `lib/disposal.ts` produces honest copy ("Moves 61.2 GB to the recycle bin…" / "Permanently deletes… the bin is off").
- Shared dialogs: `RemoveDownloadDialog`, `DeleteMovieDialog`, `DeleteSeriesDialog`.
- Settings → System gets:
  - a **Backups** card (`pages/settings/Backups.tsx`): list with kind chips, Back up now, Download, Delete, Restore, Upload, schedule controls, and a secrets/same-disk note
  - a **Recycle bin** card with one row per bin and expiry per item

  Both move into CFG's System hub when it lands.
- `lib/restart.ts` `restartAndWait()` waits for `/api/v1/status` `started_at` to change. It is shared with the SetupWizard.

## Rollout
- M1 ships the P0 fixes: pre-migrate snapshot, dialogs, user delete, download removal, series delete, merge.
- M2 makes every delete path honest.
- M3 adds backups and restore.
- M4 moves bins next to the files. It is a two-step change: first the manager learns several bins with no routing change, then routing flips.
- M5 adds image rollback.

Back-compat:
- `delete_data=true` is accepted for one release.
- Recycle item IDs change format in [SAFE-16](#safe-16); the UI always refetches.
- The legacy bin keeps being listed and aged out until it is empty.

Testing:
- Temp dirs and scratch data dirs only. Never the owner's library files or live DB.
- Inject a "bin" that is a regular file so MkdirAll fails on every OS.
- Race tests in Docker before every push.

#### Milestone: M1 — Nothing irreversible happens by default

_An update that ships a migration always leaves a pre-migrate snapshot. Deleting a user, removing a download, deleting a series and merging an audiobook all ask first, default to keeping data, and send files to the bin or a backup instead of erasing them._

<a id="safe-01"></a>
- [ ] **SAFE-01 · Snapshot the database automatically before pending migrations run** — `P0` · `M` · Phase 0
  - **Problem:** store.Open (internal/store/store.go:54) applies every pending migration in place with no copy taken first. There are 89 migrations, and seven of them drop or rebuild tables (0085 drops audio_app_passwords, for example). Each migration is transactional, so one that fails is safe. One that commits a logical mistake, or corruption during an update, loses every account, request, Insights history, audiobook place and setting with nothing to roll back to. config.go:22 and scheduler.go:2 say backups exist, but none do. One 10 s context also covers the ping and every migration, so a long migration fails the boot.
  - **Approach:** 1. New internal/store/backupname.go:
       - Kinds: pre-migrate, nightly, manual, pre-restore, pre-delete-user, uploaded.
       - BackupName(kind, t) returns 'arrmada-<kind>-<UTC 20061002T150405Z>.db'.
       - ParseBackupName(name) (kind, at, ok) uses the strict regex ^arrmada-(pre-migrate|nightly|manual|pre-restore|pre-delete-user|uploaded)-(\d{8}T\d{6}Z)\.db$.
       - BackupsDir(dataDir) is <dataDir>/backups.
    2. New internal/store/snapshot.go:
       - snapshotTo(ctx, db, dst) does MkdirAll, removes any stale dst.tmp (VACUUM INTO fails if the target exists), runs `VACUUM INTO ?` with the path as a bound parameter, opens the tmp file read-only ('file:<tmp>?mode=ro') and requires PRAGMA quick_check = 'ok', fsyncs, then renames to dst. On any error it deletes the tmp file.
       - Free-space guard: when diskspace.Of(dir) is ok, FreeBytes must be at least 1.2 × (size of arrmada.db + arrmada.db-wal). Otherwise return ErrNoSpace naming the need and what is available. On non-Linux the guard is skipped.
       - Store gains a dataDir field and `(s *Store) Snapshot(ctx, kind) (path string, err error)`.
       - `PruneBackups(dir, kind, keep)` deletes only files whose ParseBackupName kind matches, oldest first.
    3. migrate.go: make the migration source an fs.FS:
       - listMigrations(fsys)
       - pendingMigrations(ctx, db, fsys) (pending []string, lastApplied string, err), which ensures schema_migrations first
       - applyMigrations(ctx, db, fsys, pending), which logs each version with its duration
    4. store.go: add OpenWith(dataDir, Options{Log *slog.Logger; SkipMigrationSnapshot bool; migrations fs.FS}). The migrations field is unexported and set only through export_test.go. Open(dataDir) calls OpenWith(dataDir, Options{}). The 10 s timeout now applies to Ping only. Pending detection, the snapshot and the migrations use context.Background().
       - If pending is non-empty and lastApplied != '' (not a fresh install), take Snapshot(pre-migrate).
       - If the snapshot fails, return an error and apply nothing. The message reads: 'couldn't snapshot the database before upgrading it from <last> to <newest pending>: <err> — nothing was changed. Free space in <dir> or set ARRMADA_SKIP_MIGRATION_SNAPSHOT=1 to upgrade without one'.
       - On success, log Info 'database snapshot taken before migrations' with path, size, duration, from, to and count, then PruneBackups(pre-migrate, 5).
       - With the skip flag set, log a loud Warn and continue.
    5. config.go: add SkipMigrationSnapshot from env ARRMADA_SKIP_MIGRATION_SNAPSHOT. main.go calls store.OpenWith(cfg.DataDir, {Log, SkipMigrationSnapshot}).
    6. Make the comments true: config.go:22 and scheduler.go:2. In README's Update section, replace the 'untouched' promise with 'a database snapshot is taken automatically before any schema change (<data>/backups, newest 5 kept)'. update.sh's success output gets the same line.
  - **Files:** `internal/store/store.go`, `internal/store/migrate.go`, `internal/store/snapshot.go`, `internal/store/backupname.go`, `internal/store/export_test.go`, `internal/store/snapshot_test.go`, `internal/config/config.go`, `internal/scheduler/scheduler.go`, `cmd/arrmada/main.go`, `update.sh`, `README.md`
  - **Acceptance:**
    - Starting a build that has a new migration against an existing DB leaves /data/backups/arrmada-pre-migrate-<UTC>.db. The file opens in sqlite3, passes integrity_check and holds the pre-update schema_migrations rows. The log names the path, size, duration and the from/to versions.
    - A fresh install, and a restart with nothing pending, write no snapshot.
    - If the snapshot can't be written, Arrmada exits with a message naming the cause and the env override, and the live DB gains no schema_migrations rows. With ARRMADA_SKIP_MIGRATION_SNAPSHOT=1 it migrates and logs a warning.
    - Only the 5 newest pre-migrate snapshots are kept, and no other file in backups/ is touched.
    - Migrations no longer share the 10 s ping timeout.
  - **Tests:** Go TestOpenSnapshotsBeforePendingMigrations: MapFS {0001 create table t + insert}, close, reopen with {0001, 0002 DROP TABLE t}. The snapshot still has t and its row; the live DB doesn't.; Go TestOpenFreshInstallNoSnapshot and TestOpenNoPendingNoSnapshot.; Go TestSnapshotFailureBlocksMigration: backups path is a regular file, so Open errors and the live DB lacks 0002. Plus the skip-flag case.; Go TestSnapshotIncludesWALCommittedRow (WAL mode, row committed and not checkpointed).; Go TestPruneBackupsKeepsNewestPerKind: other kinds are untouched.; Go TestParseBackupNameRejects: '../arrmada.db', 'arrmada-nightly-x.db', an absolute path.; go test -race ./internal/store/... via the Docker one-liner before pushing.
  - **Risk:** Boot takes as long as VACUUM INTO: seconds for a few hundred MB, where Insights bandwidth samples dominate. Disk use on /data briefly doubles. Refusing to start is deliberate, and the env var is the escape hatch. Snapshots sit on the same disk as the DB, so they protect against bad migrations and corruption, not against losing the disk; SAFE-12 adds download. modernc SQLite supports VACUUM INTO.
  - **Resolves:** system-2, backend-5, product-6
<a id="safe-02"></a>
- [ ] **SAFE-02 · Shared destructive-action dialog and a cheap 'where do deleted files go' endpoint** — `P0` · `S` · Phase 0
  - **Problem:** Destructive actions are confirmed inconsistently:
- Downloads Delete (Downloads.tsx:288, 357) and user delete (Settings.tsx:238) don't ask at all.
- VersionCard deletes fire on one click.
- Other places use window.confirm, and each modal is hand-rolled.

The copy always promises the recycle bin (Movies.tsx:628), even when ARRMADA_RECYCLE_DIR=off makes deletes permanent. The UI has no cheap way to find out: GET /api/v1/recycle returns enabled and dir, but it walks the whole bin on every call.
  - **Approach:** 1. Backend: add recyclebin.Service.Mode(ctx), which returns {enabled, dirs []string, retention_days, max_gb} without walking anything. Add route GET /api/v1/recycle/mode (RoleManager) in internal/httpapi/recycle.go and server.go. [SAFE-16](#safe-16) later fills dirs with every bin.
    2. web/src/components/ConfirmDialog.tsx:
       - Props: title, body (ReactNode), confirmLabel, tone ('danger' | 'accent'), optional choices (radio list of {value, label, hint, danger}), optional extra slot for checkboxes, optional typedPhrase with the hint 'Type <phrase> to confirm', busy, error, onConfirm, onCancel.
       - Behaviour: Escape and a backdrop click cancel. Initial focus is on Cancel. Confirm stays disabled until the typed phrase matches exactly. A server error (such as a 409 recycle refusal) renders inline and keeps the dialog open.
       - Markup is lifted from the existing modal in BookDetail.tsx DeleteButton: rgba(0,0,0,.6) backdrop, var(--panel), var(--line), var(--shadow), and var(--reject) / var(--reject-soft) for danger. Width max-w-[440px], full width with 16px gutters at 375px.
    3. web/src/lib/disposal.ts:
       - useRecycleMode(): one fetch per page load, cached at module level.
       - disposalLine(bytes, mode) returns either 'Moves 61.2 GB to the recycle bin — restorable from Settings → System → Recycle bin for 30 days' or 'Permanently deletes 61.2 GB — the recycle bin is switched off'. Reuse the existing byte formatter.
    4. First adopters, which need no backend change: the two window.confirm calls in Settings.tsx RecycleBin (356, 373). [SAFE-03](#safe-03)/04/05/06/09/12/13 build on the dialog.
    5. If FE's dialog primitive has already landed, implement ConfirmDialog on top of it. Otherwise FE adopts this one.
  - **Files:** `internal/recyclebin/service.go`, `internal/httpapi/recycle.go`, `internal/httpapi/server.go`, `web/src/components/ConfirmDialog.tsx`, `web/src/lib/disposal.ts`, `web/src/lib/api.ts`, `web/src/pages/Settings.tsx`
  - **Acceptance:**
    - GET /api/v1/recycle/mode returns enabled:false when ARRMADA_RECYCLE_DIR=off and the bin path otherwise, without reading the bin's contents.
    - ConfirmDialog: Escape cancels, the typed phrase gates Confirm, and an API error shows inside the dialog.
    - The Recycle bin section's Empty and Delete-forever confirmations use the dialog and look like the existing modals.
  - **Tests:** Go TestRecycleModeOffAndOn (recyclebin).; Go httpapi: a requester gets 403 on /recycle/mode.; UI check: keyboard-only use, typed-phrase gating, layout at 375px and on desktop.
  - **Risk:** Low. Keep the props surface small so FE's component kit can absorb it later without touching callers.
  - **Resolves:** movies-6
<a id="safe-03"></a>
- [ ] **SAFE-03 · User delete asks first, states what will be lost (counts only), and snapshots the DB beforehand** — `P0` · `S` · Phase 0
  - **Problem:** The X next to Edit in Settings → Users calls api.deleteUser immediately (Settings.tsx:214-218, 238), with no confirmation. DeleteUser is a plain DELETE (auth/service.go:262), which cascades to listen_progress, listen_history (the 'put your place back' net), listen_log, bookmarks, audio passwords and tokens, sessions and API keys. One misclick permanently erases a family member's audiobook places, and there are no backups.
  - **Approach:** 1. Add auth.Service.DeletionImpact(ctx, id) (UserImpact, error). Counts only, every query indexed by user_id:
       - places: COUNT(*) FROM listen_progress
       - listening_hours: COALESCE(SUM(seconds),0)/3600.0 FROM listen_log
       - bookmarks: COUNT listen_bookmarks
       - devices: COUNT(DISTINCT family) FROM audio_tokens WHERE revoked=0
       - requests: COUNT from requests and book_requests WHERE requested_by=?
       - sessions
       - push_subscriptions
       - plex_linked: users.plex_id IS NOT NULL
       It never selects item_key, titles or any book column (audiobook privacy rule).
    2. Add GET /api/v1/users/{id}/impact (RoleAdmin).
    3. handleDeleteUser:
       - When places > 0 or listening_hours > 0, require ?confirm=<exact username> and return 400 otherwise, so the API enforces what the UI asks.
       - Then call Deps.Snapshot(ctx, 'pre-delete-user'). This func is injected from main.go: Store.Snapshot plus PruneBackups(kind, 3). [SAFE-11](#safe-11) rewires it to backup.Service.
       - If the snapshot fails, return 500 'couldn't take a safety copy first — nothing was deleted'. Otherwise run DeleteUser.
    4. UsersManager: the X opens ConfirmDialog.
       - Body: 'Delete <name>? This permanently removes their place in N audiobooks, H hours of listening history, B bookmarks and D signed-in devices. Their R requests stay. A copy of the database from just before is kept under Backups.'
       - typedPhrase is the username when N or H > 0.
       - Plex-linked users get the extra line: 'They can sign in again with Plex unless you disable them or remove their access in Plex.'
       - A [Disable instead] button appears once SEC's Disable-user action exists; until then it is hidden.
  - **Files:** `internal/auth/service.go`, `internal/auth/impact_test.go`, `internal/httpapi/users.go`, `internal/httpapi/server.go`, `internal/httpapi/users_test.go`, `cmd/arrmada/main.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Clicking X never deletes immediately. Escape or Cancel leaves the user intact.
    - The dialog shows correct counts and no book titles.
    - Deleting a user who has listening data needs the username typed, and the API refuses without ?confirm.
    - An arrmada-pre-delete-user-*.db exists after a delete. Only the newest 3 are kept.
  - **Tests:** Go TestUserImpactCounts: seed listen_progress, listen_log, audio_tokens (one revoked), requests and book_requests. Assert the counts, and that the marshalled JSON has no item_key or title keys.; Go TestDeleteUserRequiresConfirmWhenListeningData.; Go TestDeleteUserTakesSnapshot (fake Snapshot func is called before DELETE). A failing snapshot means the user still exists.; UI check: misclick X, then Cancel, and the user is still there.
  - **Depends on:** [SAFE-01](#safe-01), [SAFE-02](#safe-02), SEC (Disable user action; optional, only to unhide 'Disable instead')
  - **Risk:** Low. Keep the impact queries cheap; all are indexed by user_id. The snapshot adds a few seconds to a delete on a large DB. That is acceptable for a rare admin action.
  - **Resolves:** system-5
<a id="safe-04"></a>
- [ ] **SAFE-04 · Removing a download asks what to do with the files, can't wipe the whole client, and closes out the grab** — `P0` · `M` · Phase 0
  - **Problem:** Delete on both Downloads cards calls api.deleteDownload(hash, true) with no confirmation (Downloads.tsx:288, 357). That deletes the data, which may be the only copy of an un-imported download. handleDeleteDownload passes any path value through (downloads.go:33-34), and qBittorrent's Remove sets hashes=<value> (qbittorrent.go:262). So DELETE /queue/all?delete_data=true wipes every torrent in the client, and 'a|b' removes several. The grab row stays 'grabbed'. With a stall timeout set, stalledInQueue reads the missing torrent as stalled (coordinator.go:1165), then blocklists it and grabs another, so Delete quietly becomes 'block and re-grab'.
  - **Approach:** Backend
    1. Add download.ValidHash(h) to internal/download/infohash.go: 40 or 64 hex chars. handleDeleteDownload, handlePauseDownload, handleResumeDownload, handleTorrentAction and handleBlockDownload return 400 for anything else ('remove torrents one at a time'), so 'all', '' and 'a|b' never reach qBittorrent. download.Service.Remove checks it as well, as defence in depth.
    2. handleDeleteDownload:
       - Takes ?mode=keep_files|delete_files|block (default keep_files) plus &unmonitor=true.
       - The legacy delete_data=true maps to delete_files for one release. Remove the alias after that.
       - Returns 200 {kind, id, title, mode} instead of 204.
    3. Add Coordinator.RemoveDownload(ctx, hash, name, mode, unmonitor) (RemoveResult{Kind, ID, Title}, error) in a new internal/automation/remove.go:
       - Resolve the grab with `SELECT … FROM grabs WHERE lower(info_hash)=lower(?) ORDER BY id DESC LIMIT 1`. Fall back to matchGrab over liveGrabs by name.
       - mode=block delegates to BlockRelease, which already removes with data, marks the grab failed, blocklists and searches.
       - Otherwise call downloads.Remove(hash, mode==delete_files). A 'grabbed' grab becomes 'removed'; imported grabs stay imported. Add an event on the linked movie, series or book: 'Removed from the download client by you: <release> (files kept|deleted)'.
       - unmonitor:
         - movie → movies.SetMonitored(false)
         - book → books.SetMonitored(false)
         - album → music.SetAlbumMonitored(false)
         - series → parser.Parse(release). Episodes get SetEpisodeMonitored for those episode ids only. A season pack gets SetSeasonMonitored for each season covered. Never the whole show.
    4. New internal/automation/grabstatus.go holds the constants grabbed, imported, failed, removed, cancelled and orphaned, with a comment that only 'grabbed' is pending. 'removed' is therefore outside pendingGrabs, pendingGrabTitles, pendingSeriesGrabTitles, the book and music pending queries, and requests/progress.go:160. DetectStalled ignores the vanished torrent, nothing is blocklisted or re-grabbed, and requester progress stops showing it.
    
    Frontend
    5. web/src/components/RemoveDownloadDialog.tsx, built on ConfirmDialog choices:
       - 'Remove from the client, keep the files' (default)
       - 'Remove and delete the files'
       - 'Remove, delete, blocklist this release and find another' (linked items only)
       Plus:
       - A checkbox 'Also stop wanting <title>' for linked items. For series, the label names the season or episodes.
       - When progress is 1 and imported is false, the warning: 'This download was never imported — deleting its files destroys the only copy.'
       - The confirm label names the chosen action.
       DownloadCard and SeedingCard both use it.
    6. api.ts: deleteDownload(hash, {mode, unmonitor}). Update every caller.
  - **Files:** `internal/download/infohash.go`, `internal/download/service.go`, `internal/httpapi/downloads.go`, `internal/httpapi/downloads_test.go`, `internal/automation/remove.go`, `internal/automation/remove_test.go`, `internal/automation/grabstatus.go`, `internal/automation/store.go`, `web/src/components/RemoveDownloadDialog.tsx`, `web/src/pages/Downloads.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Clicking Delete opens a dialog. Nothing is removed until an option is picked and confirmed.
    - DELETE /api/v1/queue/all (with or without delete_data), /queue/%7C and any non-hex hash return 400 and touch nothing.
    - After 'Remove, keep files': the files remain, the grab is 'removed', the title isn't blocklisted, and no automatic re-grab of that release happens.
    - The removal appears in the title's history. 'Stop wanting' unmonitors the movie, book or album, or only the right season or episodes.
  - **Tests:** Go TestValidHash table: 40-hex, 64-hex, 'all', '', 'a|b', 39 chars.; Go TestRemoveDownloadKeepFiles: a fake downloader records Remove(hash, false) and the grab becomes 'removed'. A following DetectStalled with an elapsed window and the torrent gone does not call addBlock.; Go TestRemoveDownloadUnmonitor: a movie grab gets monitored=0. An S02 pack unmonitors only season 2. An S02E05 release unmonitors only that episode.; Go httpapi: DELETE /queue/all gives 400. DELETE /queue/{hash} with no mode calls Remove with deleteData=false. delete_data=true maps to delete_files.; UI check: the dialog defaults to keep files, and a completed, un-imported torrent shows the warning.
  - **Depends on:** [SAFE-02](#safe-02)
  - **Risk:** This changes the API contract, so update every api.ts caller. The series unmonitor scope must never widen to the whole show. Data is only lost when the owner explicitly picks delete. The Importing / Held / Import failed tab states from ops-5 belong to ACQ, not here.
  - **Resolves:** ops-5
<a id="safe-05"></a>
- [ ] **SAFE-05 · Whole-series delete goes to the recycle bin with its subtitles, refuses on bin failure, and doesn't delete files by default** — `P0` · `M` · Phase 0
  - **Problem:** Both delete dialogs start with 'Also delete files' checked: DeleteSeriesModal (Series.tsx:521) and DeleteButton (SeriesDetail.tsx:665). Service.Delete calls removeEpisodeFiles (series/service.go:699-733), which runs os.Remove on each tracked video only. There is no recycle bin and no file.removed event, and series.Service has no bus. Sidecar subtitles are left behind, so the folders never empty and Plex keeps scanning them. One hover-X plus Remove permanently wipes a whole show. DeleteEpisodeFile (service.go:66-74) also hard-deletes when recycling fails.
  - **Approach:** 1. internal/library/recycle.go:
       - `type Bin interface{ For(path string) (string, error) }`
       - `SingleBin(dir)`: '' returns ErrRecycleDisabled
       - `RemoveToBin(bin Bin, path) (dst string, err error)`: ErrRecycleDisabled means a deliberate os.Remove (IsNotExist is fine). Any other failure returns 'couldn't move <name> to the recycle bin (<err>) — nothing was deleted'. This is the epic's single delete primitive; [SAFE-07](#safe-07) moves every other caller onto it.
    2. Add library.Sidecars(video) []string to internal/library/importer.go, factored out of MoveEpisodeSubs (subtitleExts, same stem or stem+'.' prefix). MoveEpisodeSubs then uses it.
    3. series.Service:
       - Replace `recycle string` with `bin library.Bin`. SetRecycleDir(dir) stays as a wrapper over SingleBin.
       - Add SetBus(*eventbus.Bus), wired in main.go.
       - DeletePlan(ctx, id) returns {Files, Sidecars int; Bytes int64; Paths []string}. Double-episode paths are counted once.
       - Delete(ctx, id, deleteFiles) returns (DeleteSummary{Moved, Failed []string; Bytes int64}, error):
         - Pre-flight: bin.For on the first path, plus MkdirAll.
         - Each video and its sidecars go through RemoveToBin, and file.removed is published per path.
         - On the first failure, stop and call ClearEpisodeFile for the episodes already moved, so the library tells the truth. Keep the series row and return ErrFilesNotRemoved.
         - Prune empty season and series folders, deepest first.
         - Delete the DB rows only when everything moved.
       - DeleteEpisodeFile also recycles sidecars, publishes file.removed, and returns the RemoveToBin error instead of hard-deleting.
    4. HTTP:
       - GET /api/v1/series/{id}/delete-preview returns {files, sidecars, bytes, recycle: Mode}.
       - handleDeleteSeries returns 409 {error, moved, failed} on ErrFilesNotRemoved. When delete_files is set and bytes > 100 GiB, it requires &confirm=<series title>.
    5. UI: the two dialogs become one web/src/components/DeleteSeriesDialog.tsx on ConfirmDialog:
       - Checkbox unchecked by default.
       - When ticked it fetches the preview and shows disposalLine: 'Move 212 episode files (1.4 TB) and 380 subtitles to the recycle bin', or the permanent wording when the bin is off.
       - Typed title above 100 GB.
       - 409 details shown inline.
       - Replace the old 'Deletes every episode file and the show folder' copy.
  - **Files:** `internal/library/recycle.go`, `internal/library/importer.go`, `internal/series/service.go`, `internal/series/delete_test.go`, `internal/httpapi/series.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`, `web/src/components/DeleteSeriesDialog.tsx`, `web/src/pages/Series.tsx`, `web/src/pages/SeriesDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Removing a show with 'delete files' ticked puts its videos and .srt sidecars in the recycle bin, restorable from the bin page. Its folders are removed if nothing else was in them.
    - Both dialogs open unchecked and state the file count and size. Over 100 GB, the title must be typed (the API enforces it too).
    - A recycle failure leaves the series in the library, reports which files moved and which failed, and the moved episodes read as missing.
    - Each deleted file's import record is marked removed (file.removed), as for movie deletes. The same holds for single-episode deletes.
  - **Tests:** Go TestDeleteSeriesRecyclesVideosAndSidecars (temp root and bin; sidecars carry .en/.forced).; Go TestDeleteSeriesAbortsOnRecycleFailure: the bin is a regular file, so the series row remains and the summary lists failures.; Go TestDeleteSeriesSharedDoubleEpisodeFileOnce.; Go TestDeleteSeriesPublishesFileRemoved (bus subscriber).; Go TestDeleteSeriesBinOffHardDeletes.; Go TestDeleteEpisodeFileRefusesOnRecycleFailure.; UI check: unchecked by default, size in the copy, typed title over 100 GB.
  - **Depends on:** [SAFE-02](#safe-02)
  - **Risk:** Until SAFE-17, a bin on another filesystem means a full copy, and a 1 TB show copies for a long time inside the request. The preview's size sets expectations, and SAFE-17 makes it a rename. A partial failure leaves a half-recycled show. It is reported, and the files are restorable.
  - **Resolves:** series-11
<a id="safe-06"></a>
- [ ] **SAFE-06 · Audiobook merge never hard-deletes sources: temp output, duration check, sources to the bin or a 14-day backup, tags kept** — `P0` · `M` · Phase 0
  - **Problem:** MergeAudiobook (internal/automation/books.go:1503-1550) deletes every source with `_ = os.Remove(p)` once ffmpeg exits 0. It never compares the output's duration to the sum of the sources, which Merge already probes (merge.go:64-67), and it bypasses removeBookFile, the bin helper in the same file. ffmpeg writes straight to the final <title>.m4b in the book folder with -y:
- A failure or the 30-minute timeout leaves a partial .m4b that later scans pick up.
- A source with the same name is overwritten mid-read.

`-map_metadata 1` takes global tags from the chapters-only ffmetadata input, and `-map 0:a` drops the cover, so the output has no tags or artwork. The merge is one click with no confirmation, runs in an untracked goroutine (httpapi/books.go:370-388), can be started twice, and reports failure only to the server log.
  - **Approach:** 1. MergeAudiobook:
       - A per-book in-flight guard (sync.Map on Coordinator). A second request gets ErrAlreadyMerging, which the handler maps to 409.
       - Output goes to a hidden temp file in the same folder, '.<title>.m4b.merging'. The final name is <title>.m4b, or '<title> (merged).m4b' if a source already has that name.
       - audiobook.Merge(ctx, files, out, MergeOptions{Title, Author}) returns (MergeResult{SourceSeconds, OutputSeconds}, error). It adds an explicit '-f mp4' and uses a new audiobook.Duration(ctx, path) that wraps probeAudio.
       - Verify: every source duration must be known, and the output must be above 0 and within max(1%, 5 s) of the sum. Otherwise remove the temp file, keep the sources, and record a failure.
       - On any error or timeout, remove the temp file and add books.AddEvent(book, 'merge-failed', reason).
       - On success, rename temp to final. Then move each source with library.RemoveToBin(c.bin, p) when the bin is on. When the bin is off, use a new backupMergeSources into <audiobooks root>/.arrmada-merge-backup/<bookID>-<UTC ts>/, which is a same-filesystem rename. Never call os.Remove on a source. A failed move leaves the file and is recorded in the success event.
       - MarkImported points the edition at the single m4b. The success event says 'apps that downloaded this audiobook need to download it again'.
    2. Tags and cover in internal/audiobook/merge.go:
       - Read global tags from the first source with ffprobe format_tags (title, artist, album_artist, album, genre, date, comment) and pass them as -metadata, falling back to the DB title and author. Use -map_chapters 1 for chapters.
       - Add the first source as input 2 with `-map 2:v:0? -c:v copy -disposition:v:0 attached_pic`, so an embedded cover survives when there is one.
    3. A daily scheduler job 'book-merge-backup-prune' deletes backup folders older than 14 days.
    4. library.FindBookFiles (importer.go:55) skips directories whose name starts with '.', other than the root itself, so backups and partial files are never book files.
    5. handleMergeAudiobook runs through a.bg with a 30-minute budget instead of a bare goroutine. It returns 409 when a merge is already running.
    6. BookDetail.tsx MergeButton gets an inline ConfirmDialog with this text: 'Combine N files into one .m4b? The originals go to the recycle bin (or are kept for 14 days). Apps that downloaded this audiobook will need to download it again, and anyone listening right now will need to reopen it.' The wording is always the same and never says who is listening, per the privacy rule. Polling stops when a 'merged' or 'merge-failed' event appears, and the failure reason is shown.
  - **Files:** `internal/automation/books.go`, `internal/automation/coordinator.go`, `internal/automation/books_merge_test.go`, `internal/audiobook/merge.go`, `internal/audiobook/live_check_test.go`, `internal/library/importer.go`, `internal/library/importer_test.go`, `internal/httpapi/books.go`, `cmd/arrmada/main.go`, `web/src/pages/BookDetail.tsx`
  - **Acceptance:**
    - When ffmpeg fails, times out or produces a short output, the sources are untouched, no .m4b or temp file is left behind, and the book shows 'merge failed: …'.
    - After a successful merge, the sources are in the recycle bin or .arrmada-merge-backup. No code path hard-deletes them, and backups disappear after 14 days.
    - The merged m4b carries the title, author and album tags, plus the cover when the first source had one.
    - A second click while a merge runs is refused. The Merge button asks first, with the generic wording.
  - **Tests:** Go automation (merge and duration funcs injected through Coordinator fields): TestMergeKeepsSourcesWhenOutputIsShort, TestMergeMovesSourcesNotDeletes (bin on, and bin off → backup dir), TestMergeFailureLeavesNoPartialFile, TestSecondMergeIsRejectedWhileRunning, TestMergeBackupPrune.; Go library TestFindBookFilesSkipsDotDirs.; Go audiobook: extend TestMergeAgainstRealFFmpeg (skips without ffmpeg) to check the temp-name output, the duration check, and that a title tag is present. Use generated tones only, never the owner's library files.
  - **Depends on:** [SAFE-02](#safe-02), [SAFE-05](#safe-05)
  - **Risk:** Backups use extra space on the array for 14 days, which is fine for audiobooks. Re-probing adds seconds. If AUD also files a tags/cover task, it should build on this one.
  - **Resolves:** audiobooks-9

#### Milestone: M2 — Every delete is honest and undoable

_No code path falls back to a hard delete. The size cap can't purge what was just deleted. Every movie and book delete dialog tells the truth about where the files go. Deleting a movie cancels or quarantines its download instead of letting it reappear in Plex._

<a id="safe-07"></a>
- [ ] **SAFE-07 · Never fall back to hard delete: every delete and replace path refuses when the bin fails, and Movies.Delete aborts before touching rows** — `P1` · `M` · Phase 1
  - **Problem:** Several paths silently os.Remove a file when recycling fails:
- movies.removeFile (movies/service.go:977-989)
- series.SupersedeEpisodeFile (series/service.go:977-981)
- series duplicate delete (automation/series_interactive.go:742-749)
- Coordinator.removeBookFile (automation/books.go:1211-1224)

Importer.linkOrCopy (library/importer.go:1439-1443) overwrites the replaced file when recycling fails. Movies.Delete ignores removeFile's outcome and the DeleteVersionsForMovie error (service.go:349-361), and handleDeleteBook ignores DeleteAudioVersionFile errors (httpapi/books.go:~995). None of this is tested: there are no Service.Delete tests and no recycle-failure tests.
  - **Approach:** 1. Movies, Coordinator (books, series duplicates), both Importers and Convert take a library.Bin. main.go passes SingleBin(recycleDir) for now, and [SAFE-17](#safe-17) swaps in RootBins.
    2. movies:
       - removeFile(path) returns error. It uses RemoveToBin, recycles library.Sidecars(path) too, and publishes file.removed only on success.
       - Service.Delete(deleteFiles): first remove every version's file. On the first error, return ErrFilesNotRemoved{Moved, Failed} before touching any row. Check the DeleteVersionsForMovie error.
       - DeleteFile, DeleteVersion and DeleteVersionFile propagate the error.
       - In MarkImported's replacement branch (service.go:419), when the old file can't be recycled, keep it, add the movie event 'Old file kept: the recycle bin refused it (<err>)', and don't fail the import.
    3. series.SupersedeEpisodeFile: the same 'keep old + event' behaviour. DeleteSeriesDuplicate uses RemoveToBin and returns its error.
    4. books:
       - removeBookFile returns error.
       - DeleteBookFile, removeAudioFiles and DeleteAudioVersionFile propagate it.
       - handleDeleteBook stops on the first error before Books.Delete.
    5. Importer.linkOrCopy: if recycling the replaced dst fails, return 'replacement refused: couldn't move the old file to the recycle bin (<err>)' instead of overwriting, matching Convert (process.go:612-617). The import retries on the normal backoff. When the bin is deliberately off, the overwrite stays as today.
    6. Convert.retire uses RemoveToBin. Semantics are unchanged.
    7. HTTP: return 409 with the message from handleDeleteMovie, handleDeleteMovieFile, handleDeleteVersion, handleDeleteVersionFile, handleDeleteBookFile, handleDeleteBook, handleDeleteAudioVersion(File) and handleDeleteSeriesDuplicate. The UI shows it: ConfirmDialog error where a dialog exists, a flash elsewhere.
    8. Test seam: a 'bin' whose dir is a regular file, so MkdirAll fails on every OS. No chmod tricks and no Windows skips.
  - **Files:** `internal/library/recycle.go`, `internal/library/importer.go`, `internal/library/manager.go`, `internal/movies/service.go`, `internal/movies/delete_test.go`, `internal/series/service.go`, `internal/automation/series_interactive.go`, `internal/automation/books.go`, `internal/automation/books_versions.go`, `internal/automation/coordinator.go`, `internal/convert/service.go`, `internal/httpapi/movies.go`, `internal/httpapi/books.go`, `internal/httpapi/series.go`
  - **Acceptance:**
    - With a broken bin, deleting a movie, a version file, an episode file, a duplicate or a book file returns an error the UI shows, and the file and its DB rows are intact.
    - With the bin 'off', deletes still remove files.
    - An import that would replace a file whose old copy can't be recycled is refused and retried. The old file is never overwritten.
    - An upgrade whose old file can't be recycled keeps it and records an event.
  - **Tests:** Go movies TestDeleteWithBrokenBinKeepsFilesAndRows, TestDeleteBinOffRemoves, TestDeleteBinOnRecyclesWithSidecarsAndPublishes.; Go series TestSupersedeKeepsOldOnRecycleFailure; automation TestDeleteSeriesDuplicateRefuses, TestRemoveBookFileRefuses.; Go library TestImporterReplacementRefusedWhenBinFails (old file bytes unchanged).; Go httpapi TestDeleteMovieReturns409OnRecycleFailure.
  - **Depends on:** [SAFE-05](#safe-05)
  - **Risk:** Deletes that used to 'work' by hard-deleting now fail visibly when the bin is broken. That is intended, and the message says what to fix. The constructor signatures change in several packages, so expect conflicts with MOV, SER and CONV work touching the same files. Land this early in M2.
  - **Resolves:** backend-8, backend-16
<a id="safe-08"></a>
- [ ] **SAFE-08 · The size cap never purges what was just deleted; bin rows show when they go for good** — `P1` · `S` · Phase 0
  - **Problem:** recyclebin.Enforce (service.go:290-341) deletes oldest-first until the bin is under the cap (DefaultMaxGB=50), runs hourly (main.go:569) and has no protection for fresh items. A single 60-110 GB remux, the file just replaced, or one night of Convert originals is purged at the next run, together with everything older. The UI never says when an item will disappear.
  - **Approach:** 1. Enforce size-cap pass:
       - Skip entries whose deletion time (sidecar Deleted, else mtime) is within minHold (72 h).
       - Never remove the single newest entry.
       - Retention still applies as today.
       - Safety valve: when diskspace.Of(bin) reports less than 5% free, lift the protection (oldest first) and log a Warn naming what went.
       - When the bin stays over the cap only because of protected items, log a Warn.
    2. Stats gains over_cap_bytes, protected_bytes, largest_item_bytes and protected_until (unix).
    3. Item gains expires_at: deleted + retention days, or 0 when retention is off.
    4. Settings RecycleBin section:
       - Each row shows 'Deleted for good on <date>'.
       - When over the cap: 'Over the cap by X GB — items from the last 3 days are kept until they're older'.
       - When largest_item_bytes > cap: 'Your cap (50 GB) is smaller than the largest file here (78 GB)'.
    5. Coordinate with CONV: Convert originals move to their own hold in CONV. Until then this protection is what keeps a fresh original alive.
  - **Files:** `internal/recyclebin/service.go`, `internal/recyclebin/service_test.go`, `internal/httpapi/recycle.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A 60 GB item recycled today survives the hourly enforce with a 50 GB cap, while older unprotected items are purged.
    - With less than 5% free on the bin's disk, protection is lifted and the log says so.
    - Each bin row shows when it will be deleted for good, and the over-cap and cap-too-small notes appear when they apply.
  - **Tests:** Go TestEnforceKeepsNewestEvenIfOverCap.; Go TestEnforceSkipsItemsYoungerThan72h.; Go TestEnforceRetentionStillApplies.; Go TestItemsExpiresAt.; Go TestEnforceLowDiskLiftsProtection (inject the free-space func).
  - **Depends on:** [SAFE-02](#safe-02), CFG (settings save fix, so the recycle limits can actually be changed)
  - **Risk:** Protection can let the bin exceed its cap by a few large items for up to 3 days. The 5%-free valve stops that from filling the disk.
  - **Resolves:** backend-8
<a id="safe-09"></a>
- [ ] **SAFE-09 · Movie and book destructive actions: confirm version deletes, 'delete files' off by default, copy that says where files go** — `P1` · `S` · Phase 1
  - **Problem:** VersionCard's 'Delete file' and 'Remove version' (which also recycles the file) run on one click (MovieDetail.tsx:274-294, 327-329), while FilePanel asks first. DeleteMovieModal defaults 'Also delete files from disk' to on (Movies.tsx:601) and always says 'Moves the movie's file(s) to the recycle bin' (628), even when the bin is off. The detail page has no Delete at all, so touch users can only reach the hover-only grid X. BookDetail's DeleteButton (BookDetail.tsx:435-437) also defaults deleteFiles to true.
  - **Approach:** 1. GET /api/v1/movies/{id}/delete-preview returns {versions:[{id, label, file_name, size_bytes}], sidecars, bytes, recycle: Mode}. [SAFE-10](#safe-10) adds pending_downloads.
    2. Move DeleteMovieModal to web/src/components/DeleteMovieDialog.tsx, built on ConfirmDialog, so the grid, table, detail and bulk delete share it.
       - deleteFiles defaults to false.
       - The copy comes from disposalLine using the preview's bytes.
       - A 409 shows inline.
    3. VersionCard 'Delete file' and 'Remove version', and FilePanel, open a ConfirmDialog naming the file, its size and where it goes ('Moves 61.2 GB to the recycle bin…' or 'Permanently deletes…').
    4. Add 'Delete movie' to the MovieDetail toolbar. It is reachable on touch and uses DeleteMovieDialog. Grid kebab and long-press menus belong to MOV/FE.
    5. BookDetail DeleteButton: deleteFiles defaults to false, the copy comes from disposalLine, and errors show inline. AudioVersions delete gets the same confirmation.
  - **Files:** `internal/httpapi/movies.go`, `internal/httpapi/server.go`, `internal/movies/service.go`, `web/src/components/DeleteMovieDialog.tsx`, `web/src/pages/Movies.tsx`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/BookDetail.tsx`, `web/src/components/AudioVersions.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Deleting a version file or removing a version always asks first, showing the file name, size and destination.
    - With RECYCLE_DIR=off, every movie and book delete dialog says the delete is permanent.
    - The movie and book delete dialogs open with 'Also delete files' unchecked.
    - Delete movie is available on the movie detail page.
  - **Tests:** Go httpapi TestMovieDeletePreview (versions, bytes, recycle mode).; UI check: every destructive movie and book action shows a confirmation with truthful copy in both recycle modes, at 375px and on desktop.
  - **Depends on:** [SAFE-02](#safe-02), [SAFE-07](#safe-07)
  - **Risk:** Low. With SAFE-07 in place the copy is literally true, so there is no 'deletes permanently if it can't be moved' caveat.
  - **Resolves:** movies-7, movies-6
<a id="safe-10"></a>
- [ ] **SAFE-10 · Deleting a movie cancels its downloads, and a download for a deleted movie is held for review, never imported by name** — `P1` · `M` · Phase 1
  - **Problem:** Service.Delete recycles files and deletes the version and movie rows (service.go:349-361). It never touches the download queue, pending grabs, blocklist or events, and those tables have no foreign keys (0012). When the torrent finishes, HoldMovieImport finds the grab, movies.Get fails, and it returns hold=false (reviews.go:166-173). Manager.importOne then falls back to imp.Import(name) (manager.go:225-232) and links the file into the movies root. WatchImports matches nothing and records nothing. The deleted film reappears in Plex as an untracked folder, and the torrent keeps seeding with no rule. handleDeleteMovie publishes no event.
  - **Approach:** 1. Coordinator.DeleteMovie(ctx, id, DeleteMovieOpts{DeleteFiles, CancelDownloads}):
       - Load pending grabs (status='grabbed' AND media_type='movie' AND movie_id=?).
       - If CancelDownloads, call downloads.Remove(ctx, hash, true) per grab, only for ValidHash hashes; pending data lives only in the downloads dir. Mark those grabs 'cancelled'. Otherwise mark them 'orphaned'.
       - Call movies.Service.Delete. With [SAFE-07](#safe-07) it recycles files first and aborts on failure. If it aborts, restore those grabs to 'grabbed' when the torrents were left untouched.
       - On success, run `DELETE FROM blocklist WHERE movie_id=? AND media_type='movie'`. movies.Repo.Delete removes movie_events, the versions and the movie row in one transaction. Grab rows are kept for history.
       - Publish 'movie.deleted' {id, tmdb_id, folder}, which PLEX and CONV/SUB consumers can use.
    2. HoldMovieImport: when a grab exists for the hash but movies.Get returns ErrNotFound, return hold=true with reason 'Grabbed for a movie you deleted'. addReview it with MediaType movie and ExpectedID 0. It never falls through to the parse-by-name import.
    3. Reviews UI: for expected_id 0, offer 'Import into…' (pick an existing movie) and 'Remove download', which uses [SAFE-04](#safe-04)'s RemoveDownload with the keep/delete choice.
    4. HTTP:
       - handleDeleteMovie goes through Automation.DeleteMovie, with &cancel_downloads=true.
       - delete-preview ([SAFE-09](#safe-09)) adds pending_downloads [{hash, title, progress}] from the live queue.
    5. DeleteMovieDialog: when pending downloads exist, show 'Cancel the download in progress (<title> · 43%) and delete its partial data', checked by default.
  - **Files:** `internal/automation/remove.go`, `internal/automation/reviews.go`, `internal/automation/store.go`, `internal/automation/delete_movie_test.go`, `internal/movies/service.go`, `internal/movies/repo.go`, `internal/httpapi/movies.go`, `web/src/components/DeleteMovieDialog.tsx`, `web/src/pages/Reviews.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Deleting a movie that has a download in progress, with cancel ticked, removes that torrent and its data from qBittorrent and marks the grab cancelled.
    - Deleting without cancel leaves the torrent. When it finishes, it lands in Review as 'Grabbed for a movie you deleted' instead of being imported into the library.
    - No movie_events or movie blocklist rows remain for the deleted id.
    - movie.deleted is published.
  - **Tests:** Go TestDeleteMovieCancelsPendingGrabs: the fake downloader records Remove(hash, true), the grab is cancelled, and the events and blocklist rows are gone.; Go TestDeleteMovieWithoutCancelOrphansGrab.; Go TestHoldMovieImportForDeletedMovie: hold=true and a review is added.; Go TestDeleteMovieFileFailureRestoresGrabs (broken bin).
  - **Depends on:** [SAFE-04](#safe-04), [SAFE-07](#safe-07), [SAFE-09](#safe-09)
  - **Risk:** Medium. downloads.Remove with deleteData must only ever target that pending grab's hash. Series, books and music have no equivalent import gate. Deleting a series mid-download is a SER follow-up.
  - **Resolves:** movies-8

#### Milestone: M3 — Backups you can see, download and restore

_Nightly and manual snapshots with retention. An admin Backups card with download. One-click restore that is staged at boot and keeps the replaced DB. Table-rebuild migrations can no longer cascade-delete children. A backup can also be restored from an uploaded file or from the CLI when the app can't boot._

<a id="safe-11"></a>
- [ ] **SAFE-11 · Nightly and manual database backups with retention, and a health warning when they stop** — `P1` · `S` · Phase 1
  - **Problem:** Pre-migration snapshots don't cover corruption or accidental loss between updates. The scheduler comment still says 'backups later', and nothing makes a regular copy. With the default compose file, arrmada.db lives in a managed Docker volume, and an outside copy of a live WAL database can be inconsistent.
  - **Approach:** 1. New package internal/backup, with Service{store *store.Store, dir string, settings, log, now func() time.Time}:
       - Create(ctx, kind) (Backup, error) calls store.Snapshot and then prunes that kind.
       - List(ctx) returns []Backup{Name, Kind, SizeBytes, CreatedAt, SchemaVersion}. It reads every arrmada-*.db via ParseBackupName. SchemaVersion comes from a read-only open running 'SELECT max(version) FROM schema_migrations' and is cached by name, since files are immutable.
       - Retention: nightly uses setting backup_keep_nightly (default 7), manual 10, pre-restore 3, pre-delete-user 3, uploaded 3. pre-migrate stays with store (5).
       - Delete(name) accepts ParseBackupName-valid names only.
    2. dueNightly(newest, now time.Time, hour int) bool is a pure function. It is true when newest is zero or more than 36 h old, or when now - newest > 20 h and now.Hour() >= hour (local time, honouring TZ).
    3. main.go registers sched.Register('db-backup', time.Hour, true, …). The task skips when setting backup_enabled is false (default true). Hour comes from backup_hour (default 4). It logs size and duration, never contents.
    4. Deps.Snapshot (from [SAFE-03](#safe-03)) is rewired to backup.Service.Create.
    5. Health (handleSystemHealth, or OBS's registry if it has landed): warn 'No database backup in N days' when enabled, the newest nightly is over 48 h old, and uptime is over 24 h.
  - **Files:** `internal/backup/backup.go`, `internal/backup/backup_test.go`, `cmd/arrmada/main.go`, `internal/httpapi/health_system.go`, `internal/scheduler/scheduler.go`
  - **Acceptance:**
    - One arrmada-nightly-*.db appears per night after the configured hour. Restarts don't create extras, and a long downtime triggers one at once.
    - Only the configured number of nightlies is kept. pre-migrate files are never pruned by this service.
    - The Dashboard health shows a warning once the newest nightly is over 48 h old.
  - **Tests:** Go TestDueNightly table (fresh, 19 h, 21 h before the hour, 21 h after the hour, 37 h).; Go TestBackupCreateListPrune: 9 nightlies leave 7, and pre-migrate files survive.; Go TestDailyBackupIdempotent: running the task twice within 24 h gives one file.; Go TestListReadsSchemaVersion.
  - **Depends on:** [SAFE-01](#safe-01), OBS (health registry; optional)
  - **Risk:** Seven copies grow /data, so the Backups card shows the total. Backups sit on the DB's disk, so they protect against corruption, not disk loss. Download (SAFE-12) covers that.
  - **Resolves:** system-2, backend-5, product-6
<a id="safe-12"></a>
- [ ] **SAFE-12 · Backups card: list, back up now, download, delete, and schedule controls (admin only)** — `P1` · `M` · Phase 2
  - **Problem:** Even with snapshots on disk, the owner can't see, download or manage them in the app. Sonarr, Radarr and Tautulli users expect System → Backup. The DB lives in a Docker volume, so getting a copy off the server is hard today.
  - **Approach:** 1. Add internal/httpapi/backups.go. Every route is requireRole(RoleAdmin), or SEC's admin router when it lands, because backups contain API keys, the Plex token and password hashes:
       - GET /api/v1/system/backups returns {backups, total_bytes, free_bytes, settings:{enabled, hour, keep_nightly}, last_nightly_at}.
       - POST /api/v1/system/backups makes a manual backup, synchronously with a 5-minute ctx.
       - PUT /api/v1/system/backups/settings writes backup_enabled, backup_hour and backup_keep_nightly directly through settingsSvc.Set. That way it doesn't depend on the general Settings save.
       - GET /api/v1/system/backups/{name}/download streams gzip (compress/gzip over the file) with Content-Type application/gzip and 'attachment; filename=<name>.gz'.
       - DELETE /api/v1/system/backups/{name}.
       - {name} must pass store.ParseBackupName and equal filepath.Base(name). Anything else gets 400.
    2. UI: web/src/pages/settings/Backups.tsx, mounted in the Settings System tab (admin) and moved into CFG's System hub later. It shows:
       - A table with a kind chip (Before update, Nightly, Manual, Before restore, Before user delete, Uploaded), size, relative time and schema version.
       - Download and Delete actions; Delete goes through ConfirmDialog.
       - Back up now.
       - Schedule controls: on/off, hour, keep N.
       - Total size and free space.
       - The note: 'Backups contain your API keys and password hashes. They're stored next to the database (on Unraid: /mnt/user/appdata/arrmada/backups when the data dir is in appdata), so download one now and then to keep a copy off this server.'
       - The UI never shows anything from inside a backup (audiobook privacy rule).
    3. README: a 'Backups' section covering what is kept, where, and how to download.
  - **Files:** `internal/httpapi/backups.go`, `internal/httpapi/backups_test.go`, `internal/httpapi/server.go`, `web/src/pages/settings/Backups.tsx`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`, `README.md`
  - **Acceptance:**
    - An admin sees the list. 'Back up now' adds a row within seconds, and Download gives a .db.gz that decompresses to a DB passing integrity_check.
    - Schedule changes save and take effect at the next hourly check.
    - A manager or requester gets 403 on every /system/backups route.
    - Names like ../arrmada.db, /data/arrmada.db or an unknown kind return 400.
  - **Tests:** Go httpapi name-validation table test.; Go TestBackupRoutesAdminOnly (admin 200, manager 403, requester 403).; Go TestBackupDownloadIsGzip: Content-Type, gzip magic bytes, and the content decompresses to SQLite magic.; UI check: Back up now, then Download, then open in sqlite3, then Delete.
  - **Depends on:** [SAFE-11](#safe-11), [SAFE-02](#safe-02), SEC (admin route group; optional)
  - **Risk:** On-the-fly gzip of a large DB briefly uses CPU. Backup files hold secrets and per-user listening data keyed by book. Downloads are admin-only and contents are never listed or logged.
  - **Resolves:** system-2, backend-5, product-6
<a id="safe-13"></a>
- [ ] **SAFE-13 · Restore a backup from the list: validate, stage, swap at the next boot, keep the replaced DB** — `P1` · `M` · Phase 2
  - **Problem:** A backup is only useful if it can be put back. Today a restore means stopping the container and hand-copying SQLite files inside a managed Docker volume, with the -wal and -shm files left to trip over. A live swap while the scheduler is writing would corrupt data.
  - **Approach:** 1. internal/store/restore.go:
       - ValidateBackup(path, embedded []string) (BackupInfo{SchemaVersion string; Unknown []string}, error): open 'file:<p>?mode=ro'. Require PRAGMA integrity_check = 'ok' and the presence of the users and schema_migrations tables. Refuse when any applied version isn't embedded in this binary (ErrNewerSchema, naming them).
       - StageRestore(dataDir, name, requestedBy) writes <dataDir>/restore-pending.json {file: 'backups/<name>', requested_by, at}.
       - applyPendingRestore(dataDir, log) runs at the top of OpenWith, before sql.Open, when the marker exists:
         a. Re-validate.
         b. Open the current DB and VACUUM INTO backups/arrmada-pre-restore-<ts>.db, then close it.
         c. Copy the chosen file to arrmada.db.restore-tmp and fsync.
         d. Remove arrmada.db-wal and arrmada.db-shm.
         e. Rename the tmp file over arrmada.db.
         f. Delete the marker and write restore-result.json {at, ok:true, from}.
         On any failure before (e), keep the original, delete the marker, write restore-result.json {ok:false, error}, log at Error, and boot normally.
         Normal migrations then bring an older backup forward, taking their own pre-migrate snapshot.
    2. Routes (admin):
       - POST /api/v1/system/backups/{name}/restore {confirm:'RESTORE'}: validate, stage, respond, then call a.deps.Restart() when can_restart. The response is {staged, restarting, manual_command}.
       - DELETE /api/v1/system/backups/restore-pending cancels a staged restore that hasn't run.
       - GET /system/backups adds pending_restore and last_restore from the json files.
    3. Add web/src/lib/restart.ts restartAndWait(), extracted from SetupWizard.tsx's restart(). It polls /api/v1/status until started_at changes (2-minute limit), then reloads. SetupWizard uses it too.
    4. UI: a Restore button per row opens ConfirmDialog with typedPhrase 'RESTORE' and this copy: 'Everything since <date time> will be lost: requests, watch history, listening places, users and settings. Downloads grabbed since then keep going in qBittorrent but Arrmada won't know about them. You may need to sign in again. The current database is kept as Before restore.' Then a full-page 'Restarting…' via restartAndWait. When the app can't restart itself, the UI shows the manual command and a 'Cancel restore' button. A last_restore failure shows as a banner on the card.
  - **Files:** `internal/store/restore.go`, `internal/store/restore_test.go`, `internal/store/store.go`, `internal/httpapi/backups.go`, `internal/httpapi/server.go`, `web/src/lib/restart.ts`, `web/src/pages/SetupWizard.tsx`, `web/src/pages/settings/Backups.tsx`, `web/src/lib/api.ts`, `README.md`
  - **Acceptance:**
    - Restoring a nightly from the list restarts the app, and the data matches the backup time. An arrmada-pre-restore-*.db copy of the replaced DB is listed.
    - A backup from a newer schema or a corrupt file is refused with a clear message and nothing is staged.
    - If the swap fails at boot, Arrmada starts on the original DB and the card shows why.
    - Without self-restart, the staged restore waits for a manual restart and can be cancelled.
  - **Tests:** Go TestApplyPendingRestore: stage a backup containing a marker row, call OpenWith, and check the marker row is present, a pre-restore snapshot exists, and there are no stale -wal/-shm files.; Go TestValidateBackupRejectsNewerSchema and TestValidateBackupRejectsCorrupt (random bytes).; Go TestRestoreFailureKeepsOriginal (unreadable source): the marker is cleared and restore-result has ok:false.; UI check: a round trip on a scratch data dir in Docker. Never the owner's live DB.
  - **Depends on:** [SAFE-12](#safe-12)
  - **Risk:** A restore replaces every module's state, including audiobook positions, so the typed confirmation and the automatic pre-restore copy are mandatory. A partially copied file never becomes live, because of tmp plus rename.
  - **Resolves:** system-2, backend-5, product-6
<a id="safe-14"></a>
- [ ] **SAFE-14 · Rebuild-safe migrations: run table-rebuild migrations with foreign keys off and a foreign_key_check** — `P1` · `S` · Phase 0
  - **Problem:** store.Open sets foreign_keys(ON) in the DSN (store.go:33-36) and applies each migration inside a transaction, where PRAGMA foreign_keys is a no-op. A table rebuild, the DROP + recreate pattern used in 0034 and 0085, against a table with ON DELETE CASCADE children would therefore cascade-delete them. Rebuilding users would wipe sessions, listen_progress, listen_history and audio tokens, and the migration would 'succeed'.
  - **Approach:** 1. A directive on the migration's first line: '-- arrmada:foreign-keys=off'.
    2. The applyOne path for such migrations:
       a. Take a dedicated conn := db.Conn(ctx).
       b. Run 'PRAGMA foreign_keys=OFF' outside any transaction.
       c. BEGIN and execute the body.
       d. Run 'PRAGMA foreign_key_check'. Any row means rollback, with an error naming the table, rowid and parent.
       e. INSERT the schema_migrations row and COMMIT.
       f. Run 'PRAGMA foreign_keys=ON' and close the conn.
       Other migrations are unchanged.
    3. Document the 12-step rebuild pattern and the directive in a comment at the top of migrate.go.
    4. Lint test: for migrations numbered above 0089, if the body contains DROP TABLE <t> or ALTER TABLE <t> RENAME, and any migration declares REFERENCES <t>(, the directive must be present.
  - **Files:** `internal/store/migrate.go`, `internal/store/migrate_fk_test.go`
  - **Acceptance:**
    - A rebuild migration marked with the directive keeps every child row of the rebuilt parent.
    - A marked migration that leaves dangling references fails, and nothing from it is applied.
    - CI fails when a new migration rebuilds an FK-referenced table without the directive.
  - **Tests:** Go TestFKOffMigrationKeepsChildRows (MapFS: parent p, child c ON DELETE CASCADE, a migration that rebuilds p).; Go TestFKOffMigrationFailsOnViolations.; Go TestRebuildMigrationsMustDisableFKs (lint over the embedded set, versions > 0089).
  - **Depends on:** [SAFE-01](#safe-01)
  - **Risk:** Low. Only affects migrations that opt in. The pre-migrate snapshot from SAFE-01 is the backstop either way.
  - **Resolves:** backend-5
<a id="safe-15"></a>
- [ ] **SAFE-15 · Restore from an uploaded backup (.db or .db.gz) and an `arrmada restore` CLI for when the app won't boot** — `P2` · `M` · Phase 2
  - **Problem:** A backup downloaded off the server, which SAFE-12 produces as .db.gz, can't be brought back through the UI. When a bad state stops the app from booting, there is no UI at all. Today that means hand-copying files inside a Docker volume.
  - **Approach:** 1. POST /api/v1/system/backups/upload (admin):
       - Stream the body with r.MultipartReader() under http.MaxBytesReader(4 GiB); return 413 when it's over.
       - Write to backups/arrmada-uploaded-<ts>.db.part. If the first bytes are the gzip magic, gunzip on the fly.
       - Require the 'SQLite format 3\x00' header, rename to .db, then ValidateBackup. On failure delete it and return 400 with the reason.
       - The upload appears in the list as 'Uploaded' and is restored through the normal Restore flow.
    2. CLI in cmd/arrmada/cli.go. If OBS/CFG's dispatcher exists, register there. Otherwise add a minimal `if len(os.Args) > 1` switch at the top of main(), before logging and server start:
       - `arrmada backups` lists kind, time, size and schema.
       - `arrmada restore <name|path>` validates, copies an outside path into backups/ as uploaded, and stages the marker.
       - `arrmada restore --cancel` removes the marker.
       The entrypoint already forwards args ('exec gosu … arrmada "$@"'), so these work:
       - `docker exec Arrmada-app arrmada restore <name>` (then restart the container)
       - `docker compose run --rm --no-deps arrmada-app restore <name>` (works even when the app crash-loops)
    3. UI: 'Restore from file…' on the Backups card uploads with a progress bar, then offers Restore on the new row.
    4. README: 'Restoring when Arrmada won't start'.
  - **Files:** `internal/httpapi/backups.go`, `internal/httpapi/backups_test.go`, `internal/store/restore.go`, `cmd/arrmada/cli.go`, `cmd/arrmada/main.go`, `web/src/pages/settings/Backups.tsx`, `web/src/lib/api.ts`, `README.md`
  - **Acceptance:**
    - Uploading a .db.gz downloaded from the card lists it as Uploaded, and restoring it works.
    - A non-SQLite file, a corrupt DB or a newer-schema DB is refused before anything is staged. Oversize uploads get 413.
    - `arrmada restore <name>` run against a stopped or crash-looping app puts that backup in place on the next start.
  - **Tests:** Go TestUploadAcceptsGzipAndPlain.; Go TestUploadRejectsBadMagicAndCorrupt.; Go TestUploadTooLarge returns 413.; Go TestCLIRestoreStagesMarker (temp data dir).; Manual check in Docker on a scratch data dir: compose run restore, then up.
  - **Depends on:** [SAFE-13](#safe-13), OBS or CFG (CLI subcommand dispatcher; optional)
  - **Risk:** Uploads are large. Stream to disk and never buffer in memory. Behind Cloudflare, a 100 MB request body limit applies, so document that big uploads need LAN access.
  - **Resolves:** system-2, product-6

#### Milestone: M4 — Recycle bins live next to the files

_Each library root has its own .arrmada-recycle, so recycling is always a rename on the same drive. Nothing is copied into the Docker volume. The old shared bin is still listed and drains._

<a id="safe-16"></a>
- [ ] **SAFE-16 · The recycle-bin manager handles several bins (legacy bin first), with per-bin stats and wrong-drive warnings** — `P1` · `M` · Phase 3
  - **Problem:** recyclebin.Service manages exactly one dir (service.go:29-38). On a documented install that dir is <LibraryDir>/.recycle (main.go:200-206), inside the managed arrmada-media Docker volume. Every delete is copied across devices into Unraid's docker.img, which is 20 GB by default, while the bin's default cap is 50 GB. Before deletes can be routed to per-root bins, the manager must be able to list, restore, age and cap several bins, including the old one, which has to drain.
  - **Approach:** 1. recyclebin.New(bins func() []BinDir, settings, log), where BinDir is {Dir, Label string; Legacy bool}. main.go supplies [current recycleDir] plus the legacy <LibraryDir>/.recycle when it differs and is non-empty, so routing is unchanged in this task.
    2. walk() unions all bins. Item.ID becomes '<binKey>/<rel>', where binKey is the first 10 hex chars of sha256(abs dir). resolve() checks that binKey is a current bin and rel stays inside it. Item gains bin_label.
    3. Stats returns {bins:[{dir, label, legacy, files, bytes, free_bytes, other_drive}], total_*}.
       - other_drive comes from a new diskspace.SameDevice(a, b) (Linux: syscall.Stat_t.Dev; other OSes report unknown). It compares the bin with each library root it serves.
    4. Enforce applies retention per item and the size cap across the total, with [SAFE-08](#safe-08)'s protections. Empty takes an optional bin (POST /recycle/empty {bin}). Restore uses the sidecar's orig. pruneEmptyDirs runs per bin.
    5. Mode() ([SAFE-02](#safe-02)) lists all dirs.
    6. UI RecycleBin section:
       - One row per bin: path, size, files, free space.
       - A 'Legacy bin' chip with 'empty it once you've checked it', plus Empty this bin.
       - An 'on a different drive from your library — every delete is a full copy' warning.
       - The item list shows the bin label.
    7. Health: warn when any bin is flagged other_drive, or a non-empty legacy bin exists.
  - **Files:** `internal/recyclebin/service.go`, `internal/recyclebin/service_test.go`, `internal/diskspace/diskspace.go`, `internal/diskspace/diskspace_linux.go`, `internal/diskspace/diskspace_other.go`, `internal/httpapi/recycle.go`, `internal/httpapi/health_system.go`, `cmd/arrmada/main.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Items in the legacy Docker-volume bin are listed, restorable, aged and capped until it's emptied, and they're labelled as legacy.
    - Restore and Delete-forever work for an item in any bin, and crafted IDs with an unknown binKey or ../ are rejected.
    - A bin on a different drive from the library shows the warning in the card and in health.
  - **Tests:** Go TestRecycleListAcrossBinsAndRestore.; Go TestLegacyBinListedAndEnforced.; Go TestEnforceCapAcrossBins (total over the cap: oldest unprotected item across bins goes first).; Go TestResolveRejectsUnknownBinKeyAndTraversal.; Go TestSameDevice (Linux only; skip elsewhere).
  - **Depends on:** [SAFE-08](#safe-08), [SAFE-02](#safe-02)
  - **Risk:** Item IDs change format. The UI always refetches, so nothing persists them. On Unraid, /mnt/user is shfs and may report one device for every share, so other_drive can under-report there. The warning is advisory.
  - **Resolves:** system-3, backend-8
<a id="safe-17"></a>
- [ ] **SAFE-17 · Route every delete to a bin on the library root it came from, so recycling is always a rename** — `P1` · `M` · Phase 3
  - **Problem:** With a single bin, and especially with separately mounted libraries chosen in the folder picker, every recycle is a cross-device copy. RecycleFile falls back to a copy (recycle.go:76-86), and it runs synchronously inside the DELETE request or the import sweep. That can take minutes, hit Cloudflare's 100 s timeout, double disk use, and fail outright when the bin's disk is full.
  - **Approach:** 1. library.RootBins{Explicit string; Off bool; Roots func() []string; Legacy string} implements Bin, plus All() []BinDir:
       - Off returns ErrRecycleDisabled.
       - Explicit (ARRMADA_RECYCLE_DIR set) returns that single bin, as today.
       - Otherwise the longest root containing the path (filepath.Rel with no '..') gets <root>/.arrmada-recycle. On first use, MkdirAll it and write a '.plexignore' containing '*'.
       - No match falls back to Legacy, with one Warn per unmatched top dir.
    2. The Roots provider returns the effective lib_movies_dir, lib_tv_dir, lib_ebooks_dir, lib_audiobooks_dir and lib_music_dir settings with cfg fallbacks, the same logic as httpapi/library_paths.go. Use CFG's roots resolver if it has landed. In-app folder changes therefore apply without a restart.
    3. main.go builds one RootBins and passes it to movies, series, the coordinator, both Importers and convert (all take library.Bin since [SAFE-07](#safe-07)). recyclebin gets All() plus the legacy bin while it is non-empty.
    4. Scanners skip dot directories through a shared library.SkipScanDir(name):
       - automation/music_scan.go:198
       - library/importer.go:149 (book root scan)
       - FindBookFiles (from [SAFE-06](#safe-06))
       - the series library scan
       The movies scan already skips them (movies/service.go:211). Convert and Subtitles work from DB rows; confirm this while implementing.
    5. RecycleFile's copy fallback logs Warn 'cross-device recycle — copied N GB' so any remaining copy is visible.
    6. Docs and README: per-library bins, the hidden .arrmada-recycle folders inside each share, and that ARRMADA_RECYCLE_DIR is now only an override.
  - **Files:** `internal/library/recyclebins.go`, `internal/library/recyclebins_test.go`, `internal/library/recycle.go`, `internal/library/importer.go`, `internal/automation/music_scan.go`, `internal/series/service.go`, `internal/recyclebin/service.go`, `cmd/arrmada/main.go`, `README.md`
  - **Acceptance:**
    - Deleting a movie on its own mount moves it instantly by rename to <movies root>/.arrmada-recycle, with no copy line in the log.
    - Neither Plex (.plexignore) nor Arrmada's scans show the bin's contents as library items.
    - Restore puts files back where they were. Changing a library folder in the app routes new deletes to the new root's bin without a restart.
    - ARRMADA_RECYCLE_DIR=off still hard-deletes, and an explicit dir still gives a single bin.
  - **Tests:** Go TestRootBinsLongestPrefix (nested roots), TestRootBinsExplicitOverride, TestRootBinsOff, TestRootBinsUnmatchedFallsBackToLegacy.; Go TestRootBinWritesPlexignore.; Go TestRemoveToBinSameFSNeverCopies (inject a copy func that fails the test).; Go TestScannersSkipRecycleDir (music, book, series scans on a temp root with a populated .arrmada-recycle).; Never test against the owner's real library files.
  - **Depends on:** [SAFE-16](#safe-16), [SAFE-07](#safe-07), CFG (effective library roots resolver; optional)
  - **Risk:** This puts hidden folders inside library shares. Other tools such as the Unraid mover or other apps may see them; .plexignore covers Plex. Space is now spread over several bins, and the cap applies to their total, as the card states.
  - **Resolves:** system-3, backend-8

#### Milestone: M5 — Updates can be rolled back

_update.sh keeps the previous image. `./update.sh --rollback` restores the matching pre-migrate snapshot and starts the old build. A binary refuses to run against a schema it doesn't know, instead of corrupting it._

<a id="safe-18"></a>
- [ ] **SAFE-18 · Updates can be rolled back: keep the previous image, `./update.sh --rollback`, and refuse to run against an unknown newer schema** — `P2` · `M` · Phase 2
  - **Problem:** update.sh rebuilds the image tagged arrmada:dev and then runs `docker image prune -f` (update.sh:107), which deletes the now-dangling previous build. There is no way back to the last working version. If an older binary is ever started against a DB that a newer one migrated, runMigrations silently ignores the unknown versions and the old code runs against a schema it doesn't understand.
  - **Approach:** 1. store.OpenWith: after ensuring schema_migrations, compute the applied versions that aren't embedded. If there are any, refuse to start with: 'This database was upgraded by a newer Arrmada (<versions>). Update Arrmada again, or restore the snapshot taken before that upgrade: arrmada restore --for-schema <latest embedded>'. The override is ARRMADA_ALLOW_NEWER_SCHEMA=1, which logs a loud warning.
    2. CLI (from [SAFE-15](#safe-15)):
       - `arrmada version --schema` prints the latest embedded migration.
       - `arrmada restore --for-schema <v>` stages the newest arrmada-pre-migrate-*.db whose max schema version is <= v.
    3. update.sh:
       - Before `docker compose up -d --build`, run `docker image tag arrmada:dev arrmada:previous` when arrmada:dev exists. The prune stays, because a tagged image isn't dangling.
       - New `./update.sh --rollback`:
         a. Require arrmada:previous.
         b. Get LATEST from `docker run --rm --entrypoint /usr/local/bin/arrmada arrmada:previous version --schema`. Abort with a message if the previous build predates this feature.
         c. Stop the app.
         d. Run `docker compose run --rm --no-deps arrmada-app restore --for-schema $LATEST` with the current image.
         e. Retag: `docker image tag arrmada:previous arrmada:dev`.
         f. Run `docker compose up -d --no-deps --no-build arrmada-app`, then wait_healthy.
         g. Print which snapshot was restored and what data since the update is lost.
    4. README: 'Rolling back an update'.
  - **Files:** `internal/store/store.go`, `internal/store/migrate.go`, `internal/store/restore.go`, `cmd/arrmada/cli.go`, `update.sh`, `README.md`
  - **Acceptance:**
    - After an update, arrmada:previous exists and survives the prune.
    - `./update.sh --rollback` starts the previous build on the snapshot taken before that update's migrations, and the app is healthy.
    - Starting an older build against a newer schema refuses with a message naming the fix, unless the override is set.
  - **Tests:** Go TestOpenRefusesNewerSchema (DB with an extra applied version) and the override case.; Go TestRestoreForSchemaPicksNewestMatchingSnapshot.; Manual check: a scratch compose project (ARRMADA_DATA_HOST pointing at a scratch dir) does update → rollback. Never the owner's live install.
  - **Depends on:** [SAFE-15](#safe-15), [SAFE-01](#safe-01)
  - **Risk:** Rollback discards everything since the update, and the script must say so before acting. It only works back to builds that include this feature. Retagging must target the app image only, never the companion images.
  - **Resolves:** system-2

#### Risks

- Refusing to start when the pre-migrate snapshot fails (SAFE-01), or when the schema is newer (SAFE-18), could look like an outage after an update. The log message names the cause and the escape-hatch env var, and the README documents both.
- Snapshots and backups sit on the same disk as the DB and briefly double /data usage. They protect against bad migrations, corruption and mistakes, not disk loss, and the UI must say so. Download (SAFE-12) is the off-server copy.
- Backup files contain API keys, the Plex token, password hashes and per-user listening data keyed by book. They are admin-only and are never logged or listed in the UI, so the audiobook privacy rule holds within the app.
- Deletes that used to 'work' by hard-deleting now fail visibly when a bin is broken (SAFE-05, SAFE-07). That is intended, and the messages must say what to fix.
- Constructor changes to movies, series, the coordinator, the importer and convert (library.Bin) touch files that MOV, SER, CONV and BOOK are also editing, so merge conflicts are likely. Land SAFE-07 early and keep the changes mechanical.
- Per-root bins put hidden folders inside library shares, where the Unraid mover and other tools can see them. Unraid's shfs may report one device for every share, so the other-drive warnings can under-report.
- Restore discards everything since the backup, including audiobook places and requests. Torrents grabbed in between keep running in qBittorrent but are unknown to Arrmada. Typed confirmation and the automatic pre-restore copy make the restore itself reversible.
- Cap protection (SAFE-08) can hold the bin over its cap for up to 3 days. The 5%-free valve and CONV's originals hold bound that.
- Testing must never touch the owner's real library files or live DB. Use temp dirs, generated media and a scratch compose project, and run the race tests in Docker before every push (CI runs -race on Linux).

#### Out of scope

- Import-state tabs on the Downloads page (Importing / Held / Import failed / Not managed) from ops-5. Owned by ACQ.
- The Convert originals hold, convert_history ledger and Revert, and MKV-source restore. Owned by CONV.
- The user Disable action itself. Owned by SEC; SAFE-03 only links to it.
- Grid kebab and long-press menus for touch on Movies and Series cards. Owned by MOV and FE; SAFE-09 only adds Delete to the detail page.
- An import gate for series, books or music when the media was deleted mid-download. Follow-ups for SER and BOOK; SAFE-10 covers movies.
- Off-server or cloud backup destinations (S3, rclone, SMB) and encrypted backups. Download is the supported off-server path for now.
- Backing up media files or the appdata folder beyond arrmada.db (covers, logs). Unraid's appdata backup covers those.
- Partial or per-table restore, and merging data from two databases.
- A route-walk authorization test across all routes (from backend-16). Owned by SEC.
- PostgreSQL support.

