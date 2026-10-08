# MOV — Movies

_Part of the [Arrmada roadmap](../../ROADMAP.md). 24 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Make the Movies module quiet, truthful and complete. Periodic jobs never touch the disks. Every search reports what it found and when it will try again. A Wanted view and a server-backed mass editor run on one throttled search queue. Versions and Editions work end to end and use Plex {edition-…} naming. Renames, scans and deletes are previewed, never overwrite a file, and can be reached on any device.

**Why.** At library scale the Movies module costs the owner more than it gives back, and too often it either says nothing or says something false.

- Every 5-minute missing sweep and 15-minute RSS sweep forks ffprobe on every movie file in the library, and each detail-page load probes the default file twice (movies-1). The Unraid array never gets a quiet moment.
- Searches run in the background and report nothing: no toast and no history entry. There is no Wanted or Cutoff-unmet view, even though last_search_at and search_misses are already stored (movies-5). Bulk 'Set quality profile' starts one search per film behind a 1 request/s per-host throttle, and most of them silently time out (movies-4).
- Versions promise Director's Cut tracks, but nothing uses Edition. Imports ignore which track a grab was for, and two tracks at the same resolution overwrite each other on disk (movies-2).
- The movie page promises upgrade watching to unmonitored, library-scanned films (movies-6). It shows a green DOWNLOADED badge over a file that is gone, and its 'Clear record' button can recycle or delete a file that has come back (walk-7).
- A library scan skips films that were already added without a file, so automation downloads copies of files the owner already has (movies-13).
- The grid refetches the whole library every 4 seconds and never shows progress for upgrades or extra versions (movies-12).
- Library management is thin: rename runs without showing a preview, there is no organize step, the mass editor has two actions, delete is hover-only so touch devices can't reach it, and naming can't be applied to the whole library (movies-7, movies-14, system-14).
- Matching ignores original and alternative titles (movies-11). Interactive search can mark an undownloadable usenet release as 'Recommended' (movies-10).
- File-change events carry no paths and nothing subscribes to them, so Convert and Subtitles keep stale rows after a rename or delete (movies-9).

The engines underneath are sound: import safety, hash-first grab matching and the quality decision. This epic rebuilds the layer around them.

**Depends on:** BE — background job runner: [MOV-07](#mov-07)'s search queue, [MOV-17](#mov-17)'s organize and [MOV-20](#mov-20)'s scan become job kinds if it lands first. Event-bus delivery reliability matters for [MOV-15](#mov-15)'s file-change events (soft).; FE — component kit (Menu, Modal, Toast, a shared library grid/toolbar) used by [MOV-18](#mov-18), [MOV-19](#mov-19) and [MOV-22](#mov-22). [MOV-18](#mov-18) adds a local OverflowMenu if the kit isn't there yet (soft).; QUAL — [MOV-04](#mov-04) adds RejectKind to quality.Evaluation inside the quality engine; coordinate so QUAL's engine work keeps it and the RejectReason text stays unchanged.; ACQ — 'all indexers failed is not a miss' quick win ([MOV-04](#mov-04) skips its own version if done); the cross-media Activity → Wanted hub reuses [MOV-08](#mov-08)'s row shape; id-based (imdbid/tmdbid) Torznab search (draft movies.t24), which [MOV-24](#mov-24) defers to.; SAFE — deleting a movie cancels its in-flight download (draft movies.t13, finding movies-8) and recycle-bin fixes; [MOV-18](#mov-18) and [MOV-19](#mov-19)'s delete paths call it.; COPY — truthful recycle-aware delete copy and version-delete wording (draft movies.t14, movies-6/movies-7), 'Wanted' vs 'Missing' terminology ([MOV-08](#mov-08), [MOV-22](#mov-22)), and 'Activity' → 'Downloads' toasts ([MOV-04](#mov-04)).; SEC — manual-import path restriction and role checks (movies-3); [MOV-14](#mov-14)'s per-track manual import must keep them.; PLEX — partial Plex scan on import, upgrade, rename and delete (movies-9) subscribes to [MOV-15](#mov-15)'s path-carrying events; Open-in-Plex and watched status on the detail page belong to PLEX.; CONV — convert.ForgetMovie ([MOV-15](#mov-15)) lands in internal/convert; Convert's index covers only default tracks, which limits [MOV-08](#mov-08)'s Cutoff tab to default tracks.; SUB — subtitles OnMovieRemoved hook ([MOV-15](#mov-15)) lands in internal/subtitles.; CFG — naming tokens {tmdbid}/{imdbid} and the Plex preset (draft system.t38), which [MOV-12](#mov-12)'s {plexedition} and [MOV-17](#mov-17)'s organize coordinate with.; SER — series organize and safe renames reuse [MOV-17](#mov-17)'s endpoint shape; the series missing-file sweep mirrors [MOV-21](#mov-21); the library_unmatched table ([MOV-20](#mov-20)) is media-agnostic for series.; OBS — health registry entry for [MOV-21](#mov-21)'s 'Movies folder looks unmounted' warning (soft).

#### Design

## North star

The quality and import core stays as it is. Around it, Movies gets three surfaces (Library, Wanted, Detail), one search queue on the server, and a strict split between code that may touch the disk and code that may not.

### 1. Three read paths for track state ([MOV-01](#mov-01), [MOV-02](#mov-02))

| Path | Who calls it | What it reads |
|---|---|---|
| `movies.Service.VersionRows(ctx,id)` | every periodic job (search, RSS, upgrade, stall detection), import routing, delete, repoint | DB only: the movie row plus `movie_versions`, with `File` taken from the cached `media_json`. **No stat, ReadDir or ffprobe, ever.** |
| `movies.Service.VersionsLive(ctx,id)` | detail endpoints only (`GET /movies/{id}`, `/versions`, refresh) | VersionRows plus one `stat` per track and one ReadDir per folder. A stale cache (size, mtime or MediaVersion changed) schedules a background re-probe and is never probed inline. |
| `probeFile(path)` | import, rename/repoint, refresh, scan attach, backfill | Exactly one ffprobe per file event, stored as per-track `media_json`, keyed by path+size+mtime (the same contract as `convert_probe_cache`). |

Sweeps iterate SQL-filtered sets (`SearchTargets`, `UpgradeTargets`) instead of `List()`.

### 2. Track identity end to end ([MOV-09](#mov-09), [MOV-12](#mov-12), [MOV-13](#mov-13))

1. **Grab.** The `grabs` row records `movie_id`, `version_id`, `info_hash` and the new `replaces_path` (the file the grab is meant to replace, or '' when the track is missing). The edition filter runs before the decision: a track with an Edition only takes releases carrying that edition, and the default track never takes a sibling track's edition or the release already filling another track.
2. **Import.** The import resolver turns the hash into the track and returns a name built from Title, Year, the track's Edition, its Label and the paths that track may not touch. The Importer renders `{plexedition}` → `{edition-Director's Cut}`, gives each track a distinct name, and never recycles or overwrites a protected path.
3. **Attach.** `download.imported` carries `{movie_id, version_id}`. `MarkImportedTo(track)` attaches the file to that track. `routeVersion` runs only for downloads Arrmada didn't grab.
4. **Done.** A grab stays 'grabbed' until its own track's file changes. WatchImports marks the grab row imported by hash.

### 3. Search pipeline ([MOV-04](#mov-04), [MOV-07](#mov-07), [MOV-08](#mov-08))

All non-sweep movie searches go through one in-memory queue on the server:

```
UI/handlers → MovieSearchQueue.Enqueue(movie, kind, trigger)   (dedupe (movie,kind); concurrency = setting movie_search_concurrency, default 2; 3-min budget starts at dequeue)
           → searchAndGrab / upgradeMovie / RegrabMovie → movies.SearchOutcome
           → movies.last_search_outcome (+ backoff counters) and a 'searched' movie_event (manual: always; sweep: only when Summary changes)
           → bus: movie.search.queued / movie.search.started / movie.searched
           → toasts, Wanted rows, Acquisition card, queue pill
```

Sweeps keep their schedule and skip movies that are queued or running. If BE's job runner lands first, the queue becomes a job kind with concurrency 2 instead.

### 4. Data model changes

Each migration takes the next free number when it is implemented (0090 is next at time of writing; other epics also add migrations).

| Change | Task |
|---|---|
| `movie_versions.media_json TEXT NOT NULL DEFAULT ''`; `MovieFile.MtimeUnix` | [MOV-02](#mov-02) |
| `movies.last_search_outcome TEXT NOT NULL DEFAULT ''`; `last_search_at`, `search_misses` and the parsed outcome added to `movieCols` | [MOV-04](#mov-04) |
| `grabs.replaces_path TEXT NOT NULL DEFAULT ''` | [MOV-09](#mov-09) |
| `library_unmatched(media, root, folder, title, year, candidates_json, scanned_at, PK(media, folder))`, reusable by series | [MOV-20](#mov-20) |
| `movies.file_missing_since TEXT` (NULL = present) | [MOV-21](#mov-21) |
| `MovieExtra.OriginalTitle`, `AltTitles` (in the extra_json blob, no migration) | [MOV-24](#mov-24) |

`movies.SearchOutcome` (and its JSON) lives in `internal/movies`, so the Movie DTO can carry it without an import cycle. Reject buckets are stored as `map[string]int`, keyed by the `quality.RejectKind` string.

### 5. API surface (manager-only unless noted)

- `GET /movies` returns `MovieSummary[]`: no cast, overview or paths; `sort_title`; media badges; a download object with `kind` = missing|upgrade|version. `?full=1` keeps full rows. `GET /movies/downloads[?id=]` returns active progress only.
- `GET /movies/{id}` returns versions from VersionsLive plus an `acquisition{}` block, `last_search`, `next_search_at` and `file_missing`.
- Searches:
  - `POST /movies/{id}/search` returns 202 `{position}`.
  - `POST /movies/search {ids, kind}` and `GET /movies/search-queue`.
- Wanted: `GET /movies/wanted?tab=missing|cutoff`, with a media-agnostic row shape that ACQ's Activity hub can embed.
- Bulk:
  - `PUT /movies/bulk` (transactional) returns `{updated, queued, exceeds_profile, errors}`.
  - `POST /movies/bulk/regrab`.
  - `DELETE /movies/bulk`.
  - `POST /movies/bulk-add`.
- Rename and organize:
  - `GET/POST /movies/{id}/rename` work per track.
  - `GET /movies/organize?ids=|all` returns the plan; `POST /movies/organize` applies it as a background job.
- Versions:
  - `GET /movies/editions`.
  - `GET /movies/{id}/releases?version=VID`.
  - `POST /movies/{id}/manualimport {path, version_id}`.
  - `POST /grab {…, movie_id, version_id}`.
- File and scan:
  - `POST /movies/{id}/file/forget {version_id}` returns 409 if the file is back on disk.
  - `POST /movies/scan {monitor, quality_profile}` returns 409 while a scan is running.
  - `GET /movies/lookup` gains `in_library` and `library_id`.

### 6. Events (published from `internal/movies`, not from handlers)

- File changes, each carrying the affected paths:
  - `movie.downloaded {id, version_id, path}`
  - `movie.renamed {id, version_id, old_path, new_path}`
  - `movie.file_deleted {id, version_id, path}`
  - `movie.deleted {id, folder}`
- Searches: `movie.searched {id, trigger, summary, grabbed}`, `movie.search.queued`, `movie.search.started`.
- Long-running work: `movie.organize.progress`, `library.scan.progress`, and `library.scanned {imported, attached, skipped, duplicates, unmatched}`.

Convert, Subtitles and PLEX's partial scan subscribe in `cmd/arrmada/main.go`.

### 7. UI shape (keeps the dark warm palette, terracotta accent and type scale)

**Movies page header.** A Library | Wanted switch (no new nav item) and a queue pill ('Searching 2 · 298 queued').

**Library.**
- A sort menu (Title by `sort_title`, Added, Year, Rating, Size) and filter pills (All · Wanted · Downloading · No file · File missing · Doesn't fit · Unmonitored).
- Cards carry a resolution/HDR/Atmos badge, a monitored dot and a progress ring for any kind of download.
- A ⋯ card menu replaces the hover-only buttons and is always visible on touch. Table rows get the same menu.
- The mass-editor bar covers Monitor, Profile, Availability, Search, Rename… and Delete….

**Wanted.**
- A Missing tab and a Cutoff-unmet tab.
- Each row shows last searched, the outcome summary, the next retry and a Search button.
- 'Search all (N)' goes through the queue.

**Detail page, top to bottom.**
1. Hero.
2. Status chip from a shared `movieStatus()` helper, which shows 'File missing' when the file is gone.
3. Monitor switch, one contextual primary button (Search or Search for upgrade) and a ⋯ overflow menu: Interactive search, Manual import, Upload torrent, Rename…, Refresh & rescan, Delete movie….
4. An **Acquisition status** card built from real state, replacing WhyPanel.
5. Tabs: Files & versions · History · Blocklist. Every track gets the full FilePanel (details modal, subtitles, missing state, fit badge, Edit, per-track Search and Import).

History and Blocklist refresh live from websocket events.

### 8. Invariants every task keeps

- Periodic jobs never stat or probe a library file. The only exception is the guarded daily missing-file sweep ([MOV-21](#mov-21)), which stats sequentially and never probes.
- No import or rename overwrites or recycles a file that another track owns. Renames use link+remove, so they can't clobber, and they are re-checked at apply time.
- Defaults don't change for existing libraries: movies without extra tracks keep today's file names, and scanned films stay unmonitored unless the owner chooses otherwise.
- Testing touches only temp dirs and throwaway test titles, never the owner's library files. Race tests run in Docker before pushing, and commits end with the Co-Authored-By trailer.
- Audiobook privacy and the adult-content filter are untouched. No credentials appear in code.

### 9. Rollout order

1. **M1, quiet sweeps.** Ship first (P0).
2. **M2, honest page.** Depends only on M1.
3. **M3, Wanted, queue and live list.** The queue lands before the Wanted view and the mass editor.
4. **M4, versions.** [MOV-12](#mov-12) must ship as a unit: routing without per-track naming, or the other way round, loses a file just as today does.
5. **M5, rename, organize, mass edit and delete.** Needs the M4 naming.
6. **M6, polish.** Scan progress, the missing-file sweep, grid, add flow and alternative titles.

Each milestone is deployable on its own with `./update.sh`.

#### Milestone: M1 — Quiet sweeps

_Search-missing, RSS, upgrade and stall sweeps run from database rows only: zero ffprobe and zero library stats per cycle. A movie page reads cached media info, with one stat per track and a re-probe only when a file actually changed._

<a id="mov-01"></a>
- [ ] **MOV-01 · Periodic movie jobs read only the database: DB-only track rows and SQL search targets** — `P0` · `M` · Phase 1
  - **Problem:** Every periodic movie job reads the library files on disk:
- SearchMissing (every 5 min), RSSSync (15 min), UpgradeMovies (6 h) and DetectStalled (2 min, through movieHasFileFor) all call movies.Service.Versions (service.go:671).
- Versions runs fileInfo (service.go:858) for the default track and every extra track. fileInfo does an os.Stat, a ReadDir for sidecar subtitles, and mediainfo.Probe, which forks ffprobe with no cache (mediainfo.go:38-49).
- SearchMissing walks the whole library (repo.List) with no monitored/has_file pre-filter. Fully downloaded movies return searched=false, so they never enter backoff.
- handleGetMovie probes the default file twice: FileInfo, then Versions (httpapi/movies.go:269-274).

The result is one ffprobe fork per library file every 5 minutes across the Unraid array.
  - **Approach:** 1) internal/movies/service.go: add VersionRows(ctx, id) ([]Version, error).
       - The default track is built from the movie row. Its File is the media_json entry that repo.scan already parses, so SizeBytes, Quality, Codec and Resolution come from cache.
       - Extra tracks come from repo.ListVersions with File=nil for now ([MOV-02](#mov-02) adds their cache). SizeBytes comes from movie_versions.size_bytes.
       - No os.Stat, os.ReadDir or ffprobe.
       - Rename today's Versions to VersionsLive, with the doc comment 'detail page only — periodic jobs must never call this'.
    2) Add Service fields probe func(string) (mediainfo.Info, error) and stat func(string) (os.FileInfo, error). They default to mediainfo.Probe and os.Stat. Route fileInfo, resolutionOf and markImported's size stat through them so tests can count calls.
    3) Switch every caller outside the detail page to VersionRows:
       - automation: missingVersions (coordinator.go:589); upgradeMovie (:818), taking curSizeGB from v.SizeBytes or the cached File.SizeBytes with upgradeBaseline unchanged; RegrabMovie (:935); movieHasFileFor (:1483).
       - movies: Delete (:349), RepointMovieFile (:623) and markImported (:392). routeVersion and existingResolution only need SourceRelease, the cached File and FilePath.
       Afterwards, grep for '.Versions(': only the httpapi detail handlers may still call VersionsLive.
    4) internal/movies/repo.go:
       - Add SearchTargets(ctx): SELECT movieCols FROM movies m WHERE (m.monitored=1 AND m.has_file=0) OR EXISTS (SELECT 1 FROM movie_versions v WHERE v.movie_id=m.id AND v.monitored=1 AND v.has_file=0). The EXISTS arm keeps today's behaviour of searching a monitored extra track on an unmonitored movie row.
       - Add UpgradeTargets(ctx): monitored=1 AND has_file=1, today's in-loop filter.
       - Remove the unused MonitoredMissing.
    5) coordinator.go: SearchMissing (:307) and RSSSync (:725) iterate c.movies.SearchTargets(ctx). UpgradeMovies (:776) iterates UpgradeTargets. IsAvailable, inQueue and the backoff checks inside those loops stay as they are.
    6) internal/httpapi/movies.go:
       - handleGetMovie drops the separate FileInfo call, calls VersionsLive once and sets m.File = versions[0].File.
       - handleRefreshMovie does the same.
       - handleListVersions uses VersionsLive.
       - Delete Service.FileInfo once nothing calls it.
  - **Files:** `internal/movies/service.go`, `internal/movies/repo.go`, `internal/automation/coordinator.go`, `internal/httpapi/movies.go`, `internal/movies/versionrows_test.go (new)`, `internal/automation/searchtargets_test.go (new)`
  - **Acceptance:**
    - Over a library where every movie has its files, one full cycle of search-missing-movies, rss-sync, upgrade-movies and detect-stalled spawns zero ffprobe processes and stats no library file (counted by the injected probe and stat functions in tests). On the server, the owner samples `pgrep -c ffprobe` during a sweep and sees 0.
    - SearchMissing and RSSSync only visit movies with a monitored track that has no file
    - A movie page load probes each file at most once; the FileInfo + Versions double probe is gone
    - Existing upgrade and import tests pass unchanged: upgradetitle_test, sourcerelease_test and servicefixes_test
  - **Tests:** Go movies.TestVersionRowsNeverProbesOrStats: a movie with 2 extra tracks that have files; 10 calls give probe count 0 and stat count 0; Go movies.TestSearchTargets: rows for monitored-missing, unmonitored-missing, complete-with-missing-monitored-extra, fully-complete, and an unmonitored movie with a monitored missing extra. Only the 1st, 3rd and 5th are returned; Go automation.TestSearchMissingCompleteLibraryNoSearchNoProbe: fake indexer and counting probe; zero searches, zero probes; Run go test -race ./internal/movies/... ./internal/automation/... in Docker before pushing
  - **Risk:** Low to medium. Until MOV-02 lands, extra tracks have no cached File inside automation. upgradeBaseline then falls back to SourceRelease and then the filename. Extra tracks are always grabbed by Arrmada, so SourceRelease is set. A file swapped outside Arrmada keeps stale cached facts until the next Refresh, as it already does for the table view.
  - **Resolves:** movies-1
<a id="mov-02"></a>
- [ ] **MOV-02 · Per-track media cache: probe once per import, read the cache everywhere else** — `P1` · `M` · Phase 5
  - **Problem:** The cache has three gaps:
- Only the default track has cached media info (movies.media_json). Extra version tracks are probed live on every detail view.
- An import probes the incoming file twice: resolutionOf for routing, then setDefaultFile.
- The detail page re-probes every file each time it reloads, and MovieDetail reloads every 3 s while a download runs (MovieDetail.tsx:56-62).

The cache has no size/mtime key, so it can't tell when a file changed.
  - **Approach:** 1) movie.go: MovieFile gains MtimeUnix int64 (json 'mtime'). A cache entry is valid for path+size+mtime, the same contract as convert_probe_cache.
    2) New migration (next free number, e.g. NNNN_movie_version_media.sql): ALTER TABLE movie_versions ADD COLUMN media_json TEXT NOT NULL DEFAULT ''. In repo.go:
       - versionCols gains media_json, and scanVersion parses it into v.File when has_file.
       - SetVersionFile becomes SetVersionFile(ctx, id, path, size, mediaJSON).
       - ClearVersionFile clears media_json.
       - Add SetVersionMediaInfo(ctx, id, json).
    3) service.go: split fileInfo into two parts:
       - probeFile(path) *MovieFile does the stat, ffprobe and filename facts. Only import, rename/repoint, refresh, scan attach and backfill call it.
       - A pure fromCache helper.
       VersionRows now fills each extra track's File from its cache.
    4) VersionsLive(ctx, id):
       - Runs VersionRows, then one s.stat per track (Missing, SizeBytes) and one ReadDir per distinct folder for sidecar subtitles.
       - When a track's cache is empty, has MediaVersion below movies.MediaVersion, or differs from the stat in size or mtime, it schedules EnsureTrackMedia(movieID, versionID) in the background. That reuses probeSem and the probing map, keyed by movie+version.
       - That request is answered with filename-parsed facts. It never probes inline.
    5) Probe once per import and once per file change:
       - markImported calls probeFile(path) once and passes the result to routeVersion (for the resolution) and to setDefaultFile / SetVersionFile. This removes the second probe in resolutionOf.
       - Refresh re-probes once.
       - RepointMovieFile (Convert, rename) rewrites the cached Path/Filename when size+mtime still match. Otherwise it clears the entry and schedules EnsureTrackMedia, because Convert changed the codec and size.
    6) EnsureMedia(movieID) also backfills that movie's extra tracks, so the list handler's lazy backfill (httpapi/movies.go:30) covers them.
  - **Files:** `internal/movies/movie.go`, `internal/movies/repo.go`, `internal/movies/service.go`, `internal/store/migrations/NNNN_movie_version_media.sql (new)`, `internal/movies/mediacache_test.go (new)`
  - **Acceptance:**
    - Opening a movie page whose cache is current runs no ffprobe, at most one stat per track and one ReadDir per folder
    - A new import, rename or refresh probes the file exactly once; codec, HDR, Atmos and resolution then show on the detail page and in the table
    - Extra version tracks show their quality badge, codec and HDR from cache on page load
    - A file replaced outside Arrmada with a different size or mtime is re-probed in the background the next time its page opens, and the page never blocks on ffprobe
  - **Tests:** Go movies.TestVersionsLiveReprobesOnlyWhenStale: a current cache gives 0 probes; a changed size or mtime schedules exactly one background probe; Go movies.TestMarkImportedProbesOnce: a counting probe function gives exactly 1 probe per import, routing included; Go movies.TestVersionMediaCachedOnImport: an extra-track import fills movie_versions.media_json; ClearVersionFile empties it; Go movies.TestRepointKeepsCacheOnPureRename / TestRepointReprobesOnSizeChange; Go store: migrations apply cleanly (existing migration test)
  - **Depends on:** [MOV-01](#mov-01)
  - **Risk:** Low. Facts may be stale until the background re-probe finishes, which is acceptable. Bound the background probes with the existing probeSem (3) so a page with many tracks can't start an ffprobe storm.
  - **Resolves:** movies-1

#### Milestone: M2 — An honest movie page

_A gone file shows as missing, and 'Clear record' can never delete anything. Every search records what it found and when it will retry, and that shows up as a toast and in History. The Acquisition card tells the truth about monitoring and upgrades. Library scans attach files to films already added instead of letting automation download them again._

<a id="mov-03"></a>
- [ ] **MOV-03 · Show a missing file as missing everywhere on the movie page, and make 'Clear record' never delete** — `P1` · `S` · Phase 5
  - **Problem:** statusOf (MovieDetail.tsx:182) checks only has_file. A movie whose tracked file is gone therefore gets a green DOWNLOADED badge above the 'File missing from disk' FilePanel. The page contradicts itself in other places too:
- WhyPanel says 'You have this movie'.
- 'Auto-grab best' is hidden.
- Rename is offered.
- VersionCard (MovieDetail.tsx:296) says 'Downloaded' for a missing version file.

The missing-file 'Clear record' button calls DeleteFile (service.go:962), which goes through removeFile. That recycles the file, or hard-deletes it when recycling fails, and logs a 'deleted' event. If the file reappears before the click (a remounted disk, for example), 'Clear record' deletes a real file.
  - **Approach:** Frontend:
    1) New web/src/lib/movieStatus.ts. movieStatus(m: Movie, track?) returns {key, label, tone, soft}:
       - download in flight → 'Downloading'
       - has_file && file?.missing → 'File missing' (avoid tone)
       - has_file → 'Downloaded'
       - monitored → 'Wanted'
       - otherwise → 'Unmonitored'
       Use it at MovieDetail.tsx:182 (header chip), Movies.tsx:306 and the VersionCard status line (using f.missing).
    2) WhyPanel gets a missing branch: 'Arrmada has a record of this file but it isn't on disk. Refresh & rescan to look again; if it's really gone, clear the record and Arrmada will search for it (when monitored).' [MOV-05](#mov-05) later replaces WhyPanel and keeps this branch.
    3) Toolbar: when the file is missing, show 'Auto-grab best' and hide Rename.
    4) FilePanel, missing case:
       - The confirm button reads 'Clear record', with the note 'Nothing on disk is touched — the file is already gone.'
       - It calls the new endpoint below.
       - On success, a monitored movie gets a one-click 'Search now'.
    
    Backend:
    5) movies.Service.ForgetMissingFile(ctx, id, versionID):
       - s.stat the tracked path of that track (0 = default).
       - If the file exists, return ErrFileExists and delete nothing.
       - Otherwise call repo.ClearFile (or ClearVersionFile) and AddEvent('missing_cleared', 'Cleared record of missing file <name>').
    6) Add POST /api/v1/movies/{id}/file/forget {version_id} (RoleManager) in server.go. ErrFileExists returns 409 'the file is back on disk — refresh instead'.
  - **Files:** `web/src/lib/movieStatus.ts (new)`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/Movies.tsx`, `web/src/lib/api.ts`, `internal/movies/service.go`, `internal/movies/servicefixes_test.go`, `internal/httpapi/movies.go`, `internal/httpapi/server.go`
  - **Acceptance:**
    - A movie whose file was removed from disk shows a 'FILE MISSING' chip in the avoid tone, never 'DOWNLOADED'. The page also shows the missing-file explanation and the Auto-grab button, and hides Rename
    - 'Clear record' on a missing file leaves the disk untouched and records a 'missing_cleared' event, not 'deleted'
    - If the file is put back before 'Clear record' is clicked, the request returns 409 and nothing is deleted
    - An extra track whose file is missing shows 'File missing' in the versions list and gets the same safe Clear record
  - **Tests:** Go movies.TestForgetMissingFile: with a temp file present it returns ErrFileExists and the file still exists; with the file removed, has_file is cleared and the event is recorded; the same for an extra track; Go httpapi: the forget handler returns 409 when the file exists and 200 when it is missing; UI check: move a throwaway test movie's file away (a test fixture, never the owner's library), then load its detail page
  - **Risk:** Low. The list views still say 'Downloaded' until something notices the file is gone. MOV-21 covers that. Use a throwaway test folder only.
  - **Resolves:** walk-7, movies-6
<a id="mov-04"></a>
- [ ] **MOV-04 · Record and show what each movie search found: outcome, reasons and next retry** — `P1` · `M` · Phase 5
  - **Problem:** 'Search now', 'Auto-grab best', a profile change and a new version all start searches that return 202 and run in the background. searchAndGrab and grabMissing (coordinator.go:541-571, 660-710) only log 'no releases', 'no usable releases' or 'no release met the quality profile'. No movie event is written and nothing reaches the browser.

last_search_at and search_misses exist but aren't in movieCols (repo.go:22). HistoryPanel and BlocklistPanel reload only when has_file flips (MovieDetail.tsx:169-170). Nothing in the app answers 'why hasn't this downloaded?'.
  - **Approach:** 1) internal/quality/quality.go (coordinate with QUAL): add a RejectKind string enum to Evaluation (resolution, source, cam, max_source, bitrate, seeders, term, required, score, plus edition for [MOV-13](#mov-13)). Set it next to each RejectReason at quality.go:360-459, leaving the RejectReason text unchanged.
    2) internal/movies/movie.go: add SearchOutcome {At, Kind (missing|upgrade|regrab), Trigger (sweep|manual|rss|profile|version|add|bulk|blocklist), Returned, OtherFilms, Usenet, Blocklisted, InFlight, Rejected map[string]int, WrongEdition, Grabbed []string, IndexerErrors map[string]string, Summary string}. It lives in movies so the Movie DTO can carry it.
    3) internal/automation/search_outcome.go (new): helpers to build the outcome from indexer.SearchResult (including Errors), matchingMovieReleases, candidatesFrom drops and decision.Rejected. searchAndGrab, upgradeMovie, RegrabMovie and grabMissing return or fill it. Summary is plain text, e.g. '41 releases · 12 other films · 29 rejected (22 over the bitrate ceiling, 7 not 2160p) · nothing grabbed'.
    4) New migration (e.g. NNNN_movie_search_outcome.sql): ALTER TABLE movies ADD COLUMN last_search_outcome TEXT NOT NULL DEFAULT ''. repo.RecordSearchOutcome(ctx, id, json, miss, reset) replaces RecordSearchMiss/ResetSearchMisses with the same backoff semantics. When every queried indexer errored (no releases and Errors non-empty), record 'indexers failed: X, Y' and don't count a miss. Skip that part if ACQ's quick win already does it.
    5) movieCols and Movie gain last_search_at, search_misses and last_search (the parsed outcome). handleGetMovie adds next_search_at = last_search_at + searchBackoff(misses), where searchBackoff moves from automation/series.go to an exported helper. It is set only for monitored, missing, available movies.
    6) Events:
       - A Coordinator.SearchMovieWith(ctx, id, trigger) variant carries the trigger.
       - Non-sweep triggers always add a movie_events 'searched' row with the Summary.
       - Sweep searches add one only when the Summary differs from the stored one.
       - RSS records an outcome only when it grabs.
       - Publish 'movie.searched' {id, trigger, summary, grabbed} on the bus.
    7) UI:
       - MovieDetail toasts on movie.searched for its id, then reloads.
       - HistoryPanel and BlocklistPanel take a refreshKey that bumps on any movie.*, release.grabbed or download.imported event for this movie (from useLive's last).
       - The Movies grid toasts the outcome when movie.searched arrives after 'Search now'.
       - The toast copy says 'Downloads', not 'Activity' (movies-6 copy, coordinate with COPY).
  - **Files:** `internal/quality/quality.go`, `internal/movies/movie.go`, `internal/movies/repo.go`, `internal/automation/search_outcome.go (new)`, `internal/automation/coordinator.go`, `internal/automation/series.go`, `internal/httpapi/movies.go`, `internal/store/migrations/NNNN_movie_search_outcome.sql (new)`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/Movies.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - After 'Search now' on a movie with no acceptable release, a toast shows the summary (e.g. '37 releases, 0 met your profile — 22 over the bitrate ceiling'), and a 'searched' row appears in History without navigating away
    - GET /movies/{id} returns last_search_at, search_misses, last_search and next_search_at
    - A search where every indexer failed leaves search_misses unchanged and names the indexers that failed
    - 5-minute sweeps with an unchanged result add no new History rows
    - A grab or blocklist made from the page shows up in History and Blocklist live
  - **Tests:** Go automation.TestOutcomeBuckets: a Decision with mixed RejectKinds gives the right counts and summary text; Go automation.TestAllIndexersFailedIsNotAMiss; Go automation.TestSweepOutcomeEventOnlyOnChange: manual searches always write, sweeps write only on change; Go quality.TestEveryRejectPathSetsKind: table over the reject paths; RejectReason strings unchanged; UI check: History and Blocklist update live after a grab and a blocklist
  - **Depends on:** [MOV-01](#mov-01), QUAL — RejectKind is added to quality.Evaluation; coordinate so QUAL's engine work keeps it, ACQ — 'all indexers failed is not a miss' quick win (soft; skip step 4's part if it has landed)
  - **Risk:** Low to medium. Writing events only when the outcome changes keeps their volume bounded. RejectKind touches the quality engine, so keep the reason text byte-identical. The Movie JSON gains fields, which is additive.
  - **Resolves:** movies-5
<a id="mov-05"></a>
- [ ] **MOV-05 · Replace the static WhyPanel with a truthful Acquisition status card** — `P1` · `S` · Phase 5
  - **Problem:** WhyPanel (MovieDetail.tsx:618-637) promises that for any has_file movie Arrmada 'keeps watching for a clearly-better release… (checked every 6 hours)'. In fact UpgradeMovies skips unmonitored movies (coordinator.go:790), and every library-scanned film is created Monitored:false with profile 'n/a' (service.go:256-260). The claim is false for the owner's whole existing library. A monitored film on a profile with upgrades turned off gets the same promise.
  - **Approach:** 1) handleGetMovie adds acquisition: {monitored, profile_known (false for 'n/a' or an unknown ref), upgrades_allowed (Quality.AllowsUpgrades on the effective profile), scanned_in (profile 'n/a'), available (IsAvailable), available_from (Extra.ReleaseDate), last_search, next_search_at ([MOV-04](#mov-04)), downloading (pending grab or queue item), file_missing (default track File.Missing), queued ([MOV-07](#mov-07), when present)}.
    2) Replace WhyPanel with an AcquisitionStatus card built only from those fields, keeping the current panel style:
       - Has a file, monitored, upgrades allowed: 'Arrmada looks for a clearly better release every 6 hours (last checked …)'.
       - Has a file, monitored, upgrades off: 'Your profile doesn't allow upgrades, so this file stays as it is.'
       - Has a file, unmonitored or scanned in: 'Found in your library and not monitored, so it won't be upgraded.' Offer an inline Monitor + profile picker that reuses ProfileSelector, including its downgrade prompt.
       - Missing and monitored: the availability threshold (or 'available from <date>'), the last search and its outcome summary, and the next retry.
       - Unmonitored and missing: say so, with a Monitor button.
       - File missing from disk: the [MOV-03](#mov-03) copy with Rescan / Clear record / Search.
       - Downloading: progress and the release name.
    3) Keep the existing visual style and tones (good / accent / avoid).
  - **Files:** `internal/httpapi/movies.go`, `internal/movies/movie.go`, `web/src/pages/MovieDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A scanned, unmonitored film no longer claims upgrade watching and offers 'Monitor + choose profile' inline
    - A monitored film on a profile with upgrades off says it won't be upgraded
    - A missing, monitored film shows its last search, the result and the next retry
    - A film whose file is gone shows the missing-file state, never the 'you have this movie' copy
  - **Tests:** Go httpapi.TestMovieAcquisitionFields: scanned 'n/a' unmonitored; monitored with upgrades off; missing file; not yet available; UI check: each card state renders the right copy (use test titles)
  - **Depends on:** [MOV-03](#mov-03), [MOV-04](#mov-04) (last/next search lines; the state logic can ship first)
  - **Risk:** Low. The rest of movies-6 (the TMDB env-var banner, 'Activity' wording and recycle-bin delete copy) belongs to COPY.
  - **Resolves:** movies-6
<a id="mov-06"></a>
- [ ] **MOV-06 · Library scan attaches files to movies already in the library and lets the owner monitor what it adds** — `P1` · `S` · Phase 5
  - **Problem:** ScanLibrary counts a folder as Skipped whenever its TMDB id is already in the DB (service.go:234-237), even when that movie has no file. Films added earlier, for example by the Overseerr import or a request, stay Wanted, and automation downloads copies of files the owner already has.

ImportFolderAs (the manual pick) fails with 'already in library' in the same case. Every scanned film is created Monitored:false with profile 'n/a' (service.go:256-260), and the scan offers no choice.
  - **Approach:** 1) ScanLibrary: build a map from tmdb to Movie, plus a set of every path owned by any track (VersionRows).
       - Matched movie exists and !HasFile: call setDefaultFile(video), which probes once ([MOV-02](#mov-02)). Add the event 'detected' 'Attached during library scan: <file>', increment res.Attached and publish movie.downloaded {id, version_id:0, path} so Convert, Subtitles and requests react.
       - Matched movie exists with a file at a different path that no track owns: append {movie_id, title, folder, path} to res.Duplicates and don't attach. It could be another cut, which the owner decides.
    2) ImportFolderAs: when the TMDB id is already in the library without a file, attach the same way instead of erroring. With a file, return a clear 'already in library with <file>' error.
    3) Scan options: add ScanOptions{Monitor bool, QualityProfile string}. ScanLibrary(ctx, root, opts) and importMovieFile use them. The default stays unmonitored with 'n/a'. POST /movies/scan accepts an optional body {monitor, quality_profile}; the handler validates the profile with Automation.KnownProfile and returns 400 if it's unknown.
    4) ScanResult JSON gains attached and duplicates. The library.scanned event payload gains attached and duplicates counts.
    5) UI: 'Scan library' (Movies.tsx scanLibrary) opens a small dialog: 'Catalog existing films — [ ] Monitor them so they can be upgraded · Profile [default]', with a one-line explanation that unmonitored films are never searched or upgraded. It then calls the endpoint with the options. The 12-tick polling stays until [MOV-20](#mov-20) replaces it.
  - **Files:** `internal/movies/service.go`, `internal/httpapi/movies.go`, `web/src/pages/Movies.tsx`, `web/src/lib/api.ts`, `internal/movies/scan_test.go (new)`
  - **Acceptance:**
    - Scanning a folder for a film already in the library without a file attaches the file; the film flips to Downloaded and no new download starts
    - A folder for a film that already has a different file is reported as a duplicate and nothing changes
    - 'Import as' on a needs-review folder whose film exists without a file attaches it instead of failing
    - The scan dialog lets the owner monitor imported films with a chosen profile, and the default is unchanged
  - **Tests:** Go movies.TestScanAttachesToExistingMovieWithoutFile: temp dir 'Movie (2010)/Movie.2010.1080p.mkv' with fake metadata; has_file flips and the event is recorded; Go movies.TestScanReportsDuplicateForMovieWithOtherFile; Go movies.TestScanOptionsMonitorAndProfile; Go movies.TestImportFolderAsAttachesToExisting
  - **Depends on:** [MOV-01](#mov-01), [MOV-02](#mov-02) (probe-once on attach; soft)
  - **Risk:** Low to medium. Attaching publishes movie.downloaded, which sends request-ready notifications for requested films. That is correct, but call it out in the commit message. Test only in temp dirs.
  - **Resolves:** movies-13

#### Milestone: M3 — Wanted, one search queue, a live library

_All manual and bulk movie searches run through one throttled queue with live progress. The Wanted view (Missing and Cutoff unmet) shows last and next search and the outcome of each. Upgrade grabs stay tracked until their file lands. The grid uses a slim DTO and websocket updates and shows upgrade and version progress. Interactive search never recommends usenet._

<a id="mov-07"></a>
- [ ] **MOV-07 · One throttled movie search queue for every manual and bulk search, with progress over the websocket** — `P1` · `M` · Phase 5
  - **Problem:** Six handlers each start their own goroutine with a 3-minute timeout that begins at enqueue time: handleSearchMovie, handleSetProfile (both branches), handleAddMovie (search on add), handleAddVersion, handleRegrab and handleBlocklist(search_again), all in internal/httpapi/movies.go.

Bulk 'Set quality profile' on 300 films queues 300 full searches behind the 1 request/s per-host Torznab throttle (torznab.go:109-170). Every Prowlarr indexer shares one host. Most searches exceed their 3-minute budget while waiting and fail silently, and nothing shows what is queued or running.
  - **Approach:** 1) internal/automation/moviequeue.go (new), MovieSearchQueue:
       - A job channel plus a pending map keyed by (movieID, kind).
       - A worker pool sized by the settings key 'movie_search_concurrency' (default 2, clamped to 1..4).
       - Enqueue(movieID, kind missing|upgrade|regrab|blocklist_retry, trigger, userID) returns (position, duplicate).
       - Each job's 3-minute context starts when a worker dequeues it, not at enqueue. Jobs call SearchMovieWith / UpgradeMovie / RegrabMovie with the trigger so [MOV-04](#mov-04) records the outcome.
       - Busy(movieID) reports queued or running.
       - Run(ctx) is started from cmd/arrmada/main.go with runCtx and stops on cancel.
    2) Publish movie.search.queued {id, kind, position, depth} and movie.search.started {id, kind, depth}. Completion is [MOV-04](#mov-04)'s movie.searched. Add GET /api/v1/movies/search-queue → {running:[{id,title,kind}], queued:[…]} (protected; titles only).
    3) Replace every go func / a.bg movie-search path in internal/httpapi/movies.go with queue.Enqueue, including BlocklistAndSearch's re-search. Responses become 202 {queued: true, position}. Delete a.bg once nothing uses it.
    4) SearchMissing and UpgradeMovies keep their schedule but skip movies where queue.Busy(id).
    5) The queue is in-memory on purpose: after a restart the sweeps re-cover missing movies. Document this in the type comment. If BE's job runner has landed by then, implement this as a job kind with concurrency 2 instead.
    6) UI: a small pill in the Movies header, 'Searching 2 · 298 queued', fed by the events and seeded from GET /search-queue. api.ts types the new 202 shape, and toasts read 'Queued (position N)'.
  - **Files:** `internal/automation/moviequeue.go (new)`, `internal/automation/moviequeue_test.go (new)`, `internal/automation/coordinator.go`, `internal/httpapi/movies.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`, `web/src/pages/Movies.tsx`, `web/src/pages/MovieDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Changing the profile of 300 monitored films runs at most 2 movie searches at a time; none time out while waiting, and every one ends with an outcome
    - Clicking Search twice on the same movie queues it once
    - The Movies header shows the running and waiting counts live
    - The scheduled sweeps don't search a movie that is already queued or running
  - **Tests:** Go automation.TestMovieQueueDedupe; Go automation.TestMovieQueueConcurrencyBound: fake search function with an atomic in-flight counter; max ≤ 2; Go automation.TestMovieQueueTimeoutStartsAtDequeue: a job that waits longer than the timeout still runs with its full budget; Go automation.TestMovieQueueStopsOnCancel; Go automation.TestSearchMissingSkipsBusy; Race test in Docker: the queue is concurrency-heavy
  - **Depends on:** [MOV-04](#mov-04) (movie.searched completion event; soft), BE — background job runner, if it lands first (implement as a job kind instead)
  - **Risk:** Medium. HTTP responses change to 202 with a position, so every frontend caller and toast must follow; grep api.ts for searchMovie, setQualityProfile, regrab, addVersion and blockRelease. Series and books are not touched.
  - **Resolves:** movies-4, movies-5
<a id="mov-08"></a>
- [ ] **MOV-08 · Movies Wanted view: Missing and Cutoff-unmet tabs with last and next search and a throttled Search all** — `P1` · `M` · Phase 5
  - **Problem:** There is no Wanted page and no nav entry. The library 'Missing' filter is !has_file (Movies.tsx:27), which includes unmonitored films. The fit data that would show films below the profile's target is already computed (httpapi/fit.go:116), but it only appears as a table checkbox. The owner has no single list of what Arrmada is still looking for, why it hasn't found it, or when it will try again.
  - **Approach:** 1) Backend: GET /api/v1/movies/wanted?tab=missing|cutoff (RoleManager). It returns a media-agnostic row shape {kind:'movie', id, title, year, poster_url, profile_name, detail, last_search_at, last_search_summary, search_misses, next_search_at, downloading, queued}, so ACQ's cross-media Activity → Wanted hub can embed it later.
    2) Missing tab: built from repo.SearchTargets ([MOV-01](#mov-01)). Each row adds the missing track labels, available and available_from (IsAvailable / release date), and the outcome fields from [MOV-04](#mov-04).
    3) Cutoff tab:
       - Refactor httpapi/fit.go libraryFitMovies into fitMovies(ctx) ([]fitItem, error), shared by the existing handler. Keep movies whose status is not 'fits'.
       - Each row adds the fit issues plus will_upgrade = monitored && quality.AllowsUpgrades(profile), with a reason when false: 'scanned in — not monitored' or 'profile doesn't allow upgrades'.
       - Note: the Convert index covers default tracks only, so extra tracks are not judged here (CONV).
    4) POST /api/v1/movies/search {ids, kind: missing|upgrade} enqueues into [MOV-07](#mov-07)'s queue and returns {queued, duplicates}.
    5) Frontend:
       - A /movies/wanted route (App.tsx), shown as a Library | Wanted switch in the Movies page header, not a new nav item.
       - web/src/pages/MoviesWanted.tsx (new) has two tabs. Rows show a poster thumb, the missing tracks or fit issues, 'last searched 3 h ago', the outcome summary, 'next try in 6 h' and a row Search button.
       - A header 'Search all (N)' asks for confirmation ('queues N searches at the throttled rate').
       - Add empty states, and live-update on movie.searched, movie.search.* and release.grabbed.
    6) Library filters (Movies.tsx FILTERS): 'Missing' becomes 'Wanted' (monitored, no file), and add 'No file (unmonitored)'. Use COPY's agreed terminology.
  - **Files:** `internal/httpapi/movies.go`, `internal/httpapi/fit.go`, `internal/httpapi/server.go`, `internal/movies/repo.go`, `internal/automation/coordinator.go`, `web/src/pages/MoviesWanted.tsx (new)`, `web/src/pages/Movies.tsx`, `web/src/App.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Wanted → Missing lists only monitored movies with a monitored track that has no file, each with last searched, the result summary and the next retry
    - Cutoff unmet lists films whose file doesn't fit the profile target and says whether automation will upgrade them; when it won't, it says why
    - 'Search all' queues searches through the throttled queue and the page shows queue progress; nothing fires all at once
    - The library's Wanted filter count matches the Missing tab count
  - **Tests:** Go httpapi.TestMoviesWantedMissing: unmonitored and complete movies excluded; next_search_at computed from misses; Go httpapi.TestMoviesWantedCutoff: a misfit with an 'n/a' profile has will_upgrade=false and the scanned-in reason; Go httpapi.TestBulkSearchEnqueues; UI check: 'Search all' over 50 rows shows them queued, and outcomes arrive row by row
  - **Depends on:** [MOV-01](#mov-01), [MOV-04](#mov-04), [MOV-07](#mov-07), ACQ — agree the row shape with the cross-media Activity → Wanted hub (soft), COPY — 'Wanted' vs 'Missing' terminology (soft)
  - **Risk:** Low. Fit depends on Convert's index being current; MOV-15 keeps it current after renames and deletes.
  - **Resolves:** movies-5
<a id="mov-09"></a>
- [ ] **MOV-09 · Upgrade, re-grab and extra-track grabs stay pending until their own file lands** — `P1` · `S` · Phase 5
  - **Problem:** Found while planning the upgrade-progress part of movies-12; confirm it with the first test below before changing anything. DetectStalled (coordinator.go:1230) flips any pending movie grab to 'imported' as soon as movieHasFileFor reports that its target track has a file. For an upgrade, a re-grab, or a manual grab on a movie that already has a file, the track had a file before the grab, so the row is marked 'imported' within 2 minutes while the torrent is still downloading. As a result:
(a) stall fail-over never applies to upgrades;
(b) pendingGrabTitles' 24 h re-grab guard drops the release after 2 minutes;
(c) ManageSeeding treats the row as imported, so when the torrent completes on an indexer with seeding off, it removes the torrent and its data. That loses the upgrade if its import is held for review or refused as lower resolution;
(d) a download join keyed on pending grabs (MOV-10) can't show upgrade progress.
Separately, WatchImports marks movie grabs imported by normalized release name only (markGrabImportedForMovie, store.go:392). That misses prettified tracker titles, which the series path already fixed by matching on the hash.
  - **Approach:** 1) New migration (e.g. NNNN_grab_replaces_path.sql): ALTER TABLE grabs ADD COLUMN replaces_path TEXT NOT NULL DEFAULT ''. The grab struct, scanGrab, pendingGrabs and liveGrabs read it.
    2) recordGrab gains a replacesPath argument:
       - grabMissing passes ''.
       - upgradeMovie and RegrabMovie pass v.FilePath.
       - RecordManualGrab passes the target track's current FilePath when it has a file.
    3) movieHasFileFor(g) uses VersionRows ([MOV-01](#mov-01)). It returns true only when the target track HasFile AND either its FilePath differs from g.ReplacesPath or normRelease(track.SourceRelease) == normRelease(g.Title). The second arm covers a same-name upgrade that lands at the same path. Legacy rows with replaces_path '' keep today's semantics.
    4) WatchImports: after attaching, mark the grab row matched by info hash as imported with a new markGrabImportedByHash (the same pattern as markSeriesGrabImported). Fall back to markGrabImportedForMovie by name for rows without a hash.
  - **Files:** `internal/automation/store.go`, `internal/automation/coordinator.go`, `internal/store/migrations/NNNN_grab_replaces_path.sql (new)`, `internal/automation/upgradegrab_test.go (new)`
  - **Acceptance:**
    - An upgrade grab stays 'grabbed' while it downloads, gets stall fail-over per its profile, and flips to 'imported' when the new file is attached
    - Grabs for missing files behave exactly as today
    - ManageSeeding never removes an upgrade torrent whose file hasn't been imported
    - A movie grab whose tracker title differs from the torrent name is still marked imported, by hash
  - **Tests:** Go automation.TestUpgradeGrabNotMarkedImportedEarly: track has file A; grab with replaces_path A; DetectStalled leaves it 'grabbed'; after the path becomes B it flips; Go automation.TestSameNameUpgradeFlipsOnSourceRelease; Go automation.TestWatchImportsMarksMovieGrabByHash; Go automation.TestLegacyGrabRowsUnchanged (replaces_path '')
  - **Depends on:** [MOV-01](#mov-01)
  - **Risk:** Medium. This changes stall and seeding timing for upgrades. With stall timeouts on, a slow upgrade can now be failed over, which is the intended behaviour. Exercise it against a scratch qBittorrent category only.
  - **Resolves:** movies-12
<a id="mov-10"></a>
- [ ] **MOV-10 · Slim, live library list: summary DTO, O(n) download join, and progress for upgrades and extra versions** — `P1` · `M` · Phase 5
  - **Problem:** Three problems with the library list:
- While any movie is downloading, the grid re-requests /movies every 4 s (Movies.tsx:134-141). The response holds every movie with Extra: genres, studios, and up to 15 cast members with image URLs.
- Each request reads the qBittorrent queue and parses every queue item once per movie (httpapi/movies.go:22-27, activity.go:228-245), which is O(movies × queue). downloadFor returns nil whenever m.HasFile, so upgrades and extra-version downloads never show progress.
- Movies.tsx doesn't use useLive, so a grab started asynchronously (search on add, the queue) isn't noticed until a manual reload. MovieDetail reloads the whole movie every 3 s while downloading.
  - **Approach:** 1) internal/movies, new MovieSummary: {id, title, sort_title, year, poster_url, monitored, has_file, file_missing, quality_profile, min_availability, added_at, vote_average, size_bytes, original_title, media {resolution, codec, audio[], atmos, hdr[], bitrate_mbps, duration_min}, download {progress, state, kind: missing|upgrade|version, version_label}}.
       - sort_title is lowercased, has a leading The/A/An stripped, and is accent-folded with parser.FoldAccents.
       - repo.ListSummaries selects only the needed columns. It takes vote_average and original_title from extra_json with json_extract and the media facts from media_json. No cast, no overview, no file path.
    2) GET /movies returns summaries. Keep ?full=1 for callers that need full rows. Move Quality.tsx:950 and Reviews.tsx:103 (id/title/year/poster only) onto summaries. Keep the EnsureMedia lazy backfill.
    3) Download join, O(movies + queue):
       - One query of pending movie grabs (status 'grabbed') builds a map from hash to (movie_id, version_id, label, replaces_path != ''), which relies on [MOV-09](#mov-09).
       - Queue items match by hash first, then by a title-key map built once.
       - Report kind upgrade when the grab replaces a file, and kind version when version_id > 0.
       - Replace downloadFor in activity.go with this shared joiner.
    4) Add GET /api/v1/movies/downloads[?id=] returning only active items [{movie_id, version_id, progress, state, kind, version_label}].
    5) Movies.tsx:
       - useLive. On release.grabbed, movie.downloaded, movie.file_deleted, movie.renamed, movie.deleted, movie.searched or library.scanned, refetch /movies, debounced by 1 s.
       - While any download is active, poll /movies/downloads every 4 s and merge progress into state instead of refetching everything.
       MovieDetail polls /movies/downloads?id= instead of the full movie every 3 s.
  - **Files:** `internal/movies/movie.go`, `internal/movies/repo.go`, `internal/httpapi/movies.go`, `internal/httpapi/activity.go`, `internal/httpapi/server.go`, `web/src/pages/Movies.tsx`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/Quality.tsx`, `web/src/pages/Reviews.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - /movies contains no cast, overview or file paths, and for a large library is a fraction of today's size
    - A grab started by search on add or by the queue appears on the grid without a reload
    - A 4K upgrade or an extra-version download shows a progress indicator labelled 'Upgrade' or with the version label
    - While downloads run, the browser polls only the small downloads endpoint
    - Every table column (resolution, codec, audio, Atmos, HDR, bitrate, fit) still renders from the summary
  - **Tests:** Go httpapi.TestMovieDownloadsJoinByHash: a has_file movie with a pending upgrade grab reports kind=upgrade; the title fallback still works; Go movies.TestSummaryJSONOmitsExtra; Go movies.TestSortTitle: 'The Matrix' → 'matrix'; 'Amélie' → 'amelie'; UI check: devtools network shows /movies/downloads polling, not /movies
  - **Depends on:** [MOV-09](#mov-09), [MOV-04](#mov-04) (movie.searched; soft)
  - **Risk:** Low to medium. Check every consumer of the Movie list type (Movies grid and table, Quality, Reviews); the detail page keeps the full Movie.
  - **Resolves:** movies-12
<a id="mov-11"></a>
- [ ] **MOV-11 · Interactive search: never recommend an undownloadable usenet release, and show release age and peers** — `P2` · `S` · Phase 5
  - **Problem:** RankReleasesWith (coordinator.go:441-498) builds candidates from bestByTitle(result.Releases) without the grabbable() filter that automatic search, upgrade and regrab all use. Once Prowlarr sync adds usenet indexers as Newznab, the highlighted 'Recommended' release can be an NZB that grabTo refuses, and it can differ from what Auto-grab would pick. RankedRelease has no PublishedAt or Peers, so release age is never shown.
  - **Approach:** 1) RankReleasesWith decides over the same set candidatesFrom uses (grabbable, deduped), so Recommended equals the auto-grab pick. Usenet rows are still listed, with Eligible=false and RejectReason 'Usenet release — no usenet download client is set up', and are never Recommended. Add Transport to RankedRelease.
    2) RankedRelease gains published_at (RFC3339, omitempty) and peers, taken from indexer.Release.PublishedAt and Peers. Fill them in the movie, series (series_interactive.go:169) and book (books.go:1012) rank paths.
    3) ReleaseSearchModal shows the age ('3 d', '2 y') and 'seeders/peers' on each row, and adds a 'Newest' sort.
  - **Files:** `internal/automation/coordinator.go`, `internal/automation/series_interactive.go`, `internal/automation/books.go`, `web/src/components/ReleaseSearchModal.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A usenet release is never marked Recommended, and its row says why it can't be grabbed
    - The recommended release matches what Auto-grab would choose
    - Each release row shows its age and peers, and the list can be sorted by newest
  - **Tests:** Go automation.TestRankReleasesSkipsUsenetForRecommendation: the top-scoring NZB is not recommended and has Eligible=false; the torrent winner is recommended; Go automation.TestRankedReleaseJSONHasAgeAndPeers
  - **Risk:** Low. The series and book modals share the component, so check all three.
  - **Resolves:** movies-10

#### Milestone: M4 — Versions and editions that work

_A grab imports into the track it was made for, under a distinct Plex {edition-…} name, and never overwrites another track's file. An Edition is a real requirement when grabbing. Every track has the full file panel plus Edit, Search and Import._

<a id="mov-12"></a>
- [ ] **MOV-12 · Versions: decide the track before placing the file, name it per track (Plex {edition-…}), and never overwrite another track's file** — `P1` · `M` · Phase 10
  - **Problem:** WatchImports resolves only movie_id from the grab hash (store.go:480 movieIDForGrabHash) and calls MarkImported. markImported then re-routes by resolution through routeVersion, where the strict '>' (service.go:716) sends ties to the default track.

The default file name is '{title} ({year}) - {quality}', and qualityTag is resolution+source only, so two tracks at the same resolution render the same path. Importer.linkOrCopy (importer.go:1433) recycles or overwrites the existing file.

The result: a Director's Cut 2160p download replaces the theatrical 2160p on the default track, and the DC track stays missing forever. Manual grabs (POST /grab) always record version_id 0.

Plex's {edition-…} can only be produced by hand-writing '{edition-{edition}}', which leaves a stray '{edition-}' when there is no edition (system-14). Routing and naming must ship together: either one alone loses the other track's file, exactly as today.
  - **Approach:** 1) internal/automation/store.go: replace movieIDForGrabHash with an exported MovieGrabForHash(ctx, hash) (GrabRef{ID, MovieID, VersionID}, bool).
    2) library.TitleResolver: replace ResolveMovie(ctx, name) with ResolveMovieImport(ctx, hash, name) (MovieImportName{MovieID, VersionID int64 (-1 = unknown), Title, Year, Edition, Label string, Protected []string}, bool).
       - movieTitleResolver (cmd/arrmada/main.go:88) resolves the hash with MovieGrabForHash and falls back to MatchRelease by name (VersionID -1, or the routeVersion choice on the release name).
       - It fills Edition and Label from that track, and Protected with every file path owned by the movie's other tracks (VersionRows).
       - Manager.importOne (manager.go:225) passes c.Hash and calls Importer.ImportAsNamed. The download.imported event gains movie_id and version_id.
    3) internal/library/importer.go:
       - movieParts accepts an edition override (the track's Edition).
       - Add a token {plexedition} that renders '{edition-Director's Cut}'. When empty it renders nothing, and renderName strips a bare '{edition-}' left by hand-written templates.
       - If the naming scheme has neither {edition} nor {plexedition} and the track has an Edition, append ' {edition-<Edition>}' to the file name.
       - ImportAsNamed computes the target. If the target equals a Protected path, or exists and is owned by another track, it appends ' - <Label>' (then ' (2)', …). It never recycles or overwrites.
       - Importer.linkOrCopy refuses to recycle a Protected destination.
    4) internal/movies/service.go: MarkImportedTo(ctx, movieID, versionID, path, release, manual).
       - versionID >= 0 targets that track (0 = default) and skips routeVersion. -1 keeps routeVersion. A versionID whose track was deleted falls back to routeVersion.
       - The lower-resolution gate compares against the target track's own file.
       - As defence in depth, markImported refuses with ErrPathOwnedByOtherTrack, plus a 'failed' movie event, when the path equals another track's FilePath.
       - WatchImports (coordinator.go:1530) takes movie_id and version_id from the event (or MovieGrabForHash) and calls MarkImportedTo.
    5) Manual grab: grabRequest (httpapi/grab.go) gains version_id. RecordManualGrab(ctx, movieID, versionID, …) stores it with that track's profile and stall minutes. GrabMovieTorrent gains an optional version_id the same way.
    6) Settings → Movie naming: add 'plexedition' to TOKENS (web/src/pages/Settings.tsx:19) and to the preview sample. In api.ts, the grab body gains version_id.
    7) Coordinate with CFG's naming-token work (draft system.t38: {tmdbid}, {imdbid}, presets). If it has landed, reuse its conditional-segment support for {plexedition}.
  - **Files:** `internal/automation/store.go`, `internal/automation/coordinator.go`, `internal/movies/service.go`, `internal/library/manager.go`, `internal/library/importer.go`, `cmd/arrmada/main.go`, `internal/httpapi/grab.go`, `internal/httpapi/movies.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A download grabbed for a 'Director's Cut' 2160p track imports as 'Title (Year) {edition-Director's Cut} - 2160p BluRay.mkv' and lands on that track. The theatrical 2160p file on the default track stays where it is and is not recycled
    - Two same-resolution tracks without editions get distinct file names, and no import recycles a file another track owns
    - A manual grab made for a specific track is recorded with that version_id and imports into that track
    - Grabs with version_id 0 and untracked downloads still route by resolution, as today
    - Default-track file names don't change for movies with no extra tracks, so existing libraries see no rename churn
    - Plex lists both files as editions of one movie (owner checks with a throwaway test title, never a real library file)
  - **Tests:** Go automation.TestWatchImportsRoutesByGrabVersion: a grab row (movie 1, version 7) at the same resolution as the default track attaches to version 7; Go movies.TestMarkImportedRefusesPathOwnedByOtherTrack; Go library.TestMovieNamingPlexEdition: {plexedition} renders; the edition auto-appends when the scheme lacks it; an empty token and a bare '{edition-}' leave no stray separators; Go library.TestImportAsNamedNeverRecyclesProtected (temp dirs): a collision gets a label suffix and the protected file stays intact; Go automation.TestRecordManualGrabStoresVersion
  - **Depends on:** [MOV-01](#mov-01), [MOV-09](#mov-09) (grab rows marked by hash; replaces_path for manual grabs), CFG — naming tokens/presets (draft system.t38; coordinate, not blocking)
  - **Risk:** Medium. The TitleResolver interface change touches the import wiring in main.go. The naming change only affects tracks with an edition or a name collision. Ship routing and naming in one deploy. Test only in temp dirs and never rename real library files.
  - **Resolves:** movies-2, system-14
<a id="mov-13"></a>
- [ ] **MOV-13 · Versions: make Edition a real requirement when grabbing, and keep the default track off other tracks' editions** — `P1` · `M` · Phase 10
  - **Problem:** grabMissing decides with v.QualityProfile alone (coordinator.go:667). Version.Edition is stored and displayed but never filters candidates. A Director's Cut track therefore grabs whatever release scores highest, usually the theatrical release already on disk. The importer skips it as already imported, the DC card keeps saying 'searching', and the same release is grabbed again once the 24 h pending window expires.
  - **Approach:** 1) internal/parser/parser.go: export Editions() and NormalizeEdition(s) over the existing editions table (parser.go:783): Director's Cut, Extended, Theatrical, IMAX, Unrated, Remastered, Final Cut. Add bounded needles such as 'extended cut', and 'dc' only as a bounded token.
    2) internal/automation, new editionFilter(cands, track, siblings):
       - A track with an Edition keeps only candidates whose parsed Edition normalizes to the same value.
       - A track without an Edition drops candidates whose edition equals any other monitored track's edition.
       - Every track drops candidates whose normTitle equals the SourceRelease of another track of the same movie.
       Apply it in grabMissing, upgradeMovie and RegrabMovie. Count drops into SearchOutcome.WrongEdition ([MOV-04](#mov-04)), whose summary reads 'no release carried the Director's Cut edition'.
       A candidate filter is used instead of injecting a Must CondEdition, so stored profiles are untouched and the why-text stays plain.
    3) Per-track ranking: add RankReleasesFor(ctx, id, versionID, spec), which ranks under that track's profile with the filter applied. Filtered rows come back with Eligible=false and RejectKind 'edition', RejectReason 'Not the Director's Cut edition this track wants'. GET /movies/{id}/releases?version=VID calls it; the UI comes in [MOV-14](#mov-14).
    4) API: AddVersion and UpdateVersion normalize the edition. Add GET /api/v1/movies/editions listing the known editions.
    5) UI: AddVersionModal's Edition field (MovieDetail.tsx:383) becomes a select of known editions plus 'Other…' free text, with a hint that releases must carry that edition in their name.
  - **Files:** `internal/parser/parser.go`, `internal/automation/coordinator.go`, `internal/automation/edition_filter.go (new)`, `internal/movies/service.go`, `internal/httpapi/movies.go`, `internal/httpapi/server.go`, `web/src/pages/MovieDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A Director's Cut track only grabs releases whose names carry the DC edition. If none exist it stays Wanted, and the outcome says why
    - While a DC track exists, the default track never grabs a DC release
    - A release that already fills one track is never grabbed for another
    - Interactive search for a track marks wrong-edition releases as not eligible, with a plain reason
  - **Tests:** Go automation.TestEditionFilter (table): DC track; default track with a DC sibling; a movie without editions is unchanged; 'Directors.Cut' and 'Director's Cut' normalize equal; Go automation.TestGrabMissingEditionTracks: candidates [theatrical 2160p, DC 2160p], tracks [default 4K, DC 4K]; the fake Grab is called with theatrical for the default track and DC for the DC track; Go automation.TestExcludesSiblingSourceRelease; Go parser.TestNormalizeEdition
  - **Depends on:** [MOV-12](#mov-12), [MOV-04](#mov-04) (WrongEdition in the outcome; soft)
  - **Risk:** Low to medium. Scene naming of editions is inconsistent, so some real DC releases may be missed until the needles are extended. Only movies with extra tracks are affected.
  - **Resolves:** movies-2
<a id="mov-14"></a>
- [ ] **MOV-14 · Versions UI parity: full file panel, Edit, per-track Search and Import, and confirmed deletes for every track** — `P2` · `M` · Phase 10
  - **Problem:** As soon as any extra track exists, VersionsArea (MovieDetail.tsx:218) replaces the full FilePanel (FileDetailsModal, subtitles, missing state, fit badge and 'Bring to target') with a sparse VersionCard. VersionCard's deleteFile and removeVersion fire with no confirmation (MovieDetail.tsx:274-294, 327-329), unlike FilePanel.

There is no UI to edit a track's label, profile or edition, although PUT /movies/{id}/versions/{vid} exists, and handleUpdateVersion doesn't validate the profile. Interactive search and Manual import always target the default track.
  - **Approach:** 1) MovieDetail.tsx: split FilePanel into a FileBody component taking {movieId, versionId, file}. Every VersionCard renders it, so all tracks get FileDetailsModal, subtitles, the Missing state with [MOV-03](#mov-03)'s safe Clear record, the fit badge, and the same confirm-before-delete. removeVersion also confirms ('Remove the Director's Cut track and recycle its file?').
    2) Edit on extra tracks: a modal reusing the AddVersionModal fields (with [MOV-13](#mov-13)'s edition select) calls api.updateVersion. handleUpdateVersion validates the profile with KnownProfile (400 if unknown), and when the profile or edition changes it queues a search through [MOV-07](#mov-07).
    3) Per-track 'Search indexers' opens ReleaseSearchModal with fetchReleases = api.movieReleases(id, versionId) ([MOV-13](#mov-13)'s ?version=). onGrab sends version_id ([MOV-12](#mov-12)).
    4) Manual import gets an 'Import into track' select. POST /movies/{id}/manualimport accepts version_id and calls MarkImportedTo(…, manual=true). It must keep SEC's path restriction and role checks (movies-3).
    5) Each track's status line shows its own state from movieStatus and the last outcome ([MOV-04](#mov-04)), not 'Arrmada is searching for this track'.
  - **Files:** `web/src/pages/MovieDetail.tsx`, `web/src/components/FileDetailsModal.tsx`, `web/src/components/ReleaseSearchModal.tsx`, `web/src/lib/api.ts`, `internal/httpapi/movies.go`, `internal/movies/service.go`
  - **Acceptance:**
    - Every track, default and extra, shows the same file details, subtitles, missing state and fit badge
    - Deleting a track's file or removing a track asks for confirmation first, the same way FilePanel does
    - A track's label, profile and edition can be edited; an unknown profile is rejected with 400
    - Searching from a track ranks under that track's profile and edition, and the grab imports into that track
    - Manual import can target a specific track
  - **Tests:** Go automation.TestRankReleasesForVersionUsesTrackProfile; Go httpapi.TestUpdateVersionRejectsUnknownProfile; Go httpapi.TestManualImportIntoVersion (temp dirs); UI check: a movie with 2 tracks shows two full file panels; Edit saves; per-track search grabs into the right track; deletes confirm
  - **Depends on:** [MOV-02](#mov-02), [MOV-12](#mov-12), [MOV-13](#mov-13), [MOV-07](#mov-07) (queued search after edit; soft), SEC — manual-import path restriction and role checks (movies-3)
  - **Risk:** Low. Mostly frontend. Keep FilePanel's existing visual style.
  - **Resolves:** movies-2, movies-7

#### Milestone: M5 — Rename, organize, mass edit, delete anywhere

_File-change events carry paths, so Convert and Subtitles stay in step. Rename shows old → new for every track and never clobbers. Organize applies a naming scheme across the library after a dry-run. The mass editor is one transactional bulk endpoint. Delete is reachable from the grid, the table and the detail page at 375 px._

<a id="mov-15"></a>
- [ ] **MOV-15 · Movie file-change events carry paths, come from the service layer, and keep Convert and Subtitles in step** — `P2` · `S` · Phase 3
  - **Problem:** movie.renamed and movie.file_deleted are published only from HTTP handlers (httpapi/movies.go:418, 511, 702), without paths, and nothing on the backend subscribes to them. Only movie.downloaded reindexes Convert and Subtitles (main.go:523-541). handleDeleteMovie and handleDeleteVersion publish nothing.

After a rename, the Convert index keeps the old path until the nightly sweep. After a delete, convert.IndexMovie returns early because !HasFile, so the stale row lingers. That also feeds MOV-08's Cutoff tab. PLEX's partial scan needs these events with paths.
  - **Approach:** 1) Publish from internal/movies/service.go so every code path emits events, including bulk, organize, scan attach and MarkImportedTo:
       - movie.downloaded {id, version_id, path}
       - movie.renamed {id, version_id, old_path, new_path}
       - movie.file_deleted {id, version_id, path}
       - movie.deleted {id, folder}
       Remove the duplicate publishes from the handlers. Keep 'id' as int64 so the existing subscriber's type assertion still holds.
    2) Add convert.ForgetMovie(ctx, movieID), which deletes the convert_library rows for that movie_id. In internal/convert/index.go, IndexMovie calls it when the movie has no file. Add subtitles.Service.OnMovieRemoved(ctx, movieID) next to OnMovieImported (subtitles/jobs.go:284) to forget per-path state.
    3) cmd/arrmada/main.go: extend the movie subscriber goroutine (main.go:523):
       - renamed → convertSvc.IndexMovie + subtitlesSvc.OnMovieImported.
       - file_deleted → IndexMovie (which forgets the row when no file is left).
       - deleted → ForgetMovie + OnMovieRemoved.
    4) Note the dependency on BE's event-bus reliability work: slow subscribers can drop events today, and the nightly sweep stays as the backstop.
  - **Files:** `internal/movies/service.go`, `internal/httpapi/movies.go`, `internal/convert/index.go`, `internal/subtitles/jobs.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - After a rename, the Convert library shows the new path immediately
    - After deleting a movie or its file, the movie disappears from the Convert and Subtitles libraries without waiting for the nightly sweep
    - Every movie file change publishes an event that includes the affected path(s)
  - **Tests:** Go movies.TestRenamePublishesPaths: a bus subscriber captures old and new paths; Go movies.TestDeletePublishesMovieDeleted; Go convert.TestForgetMovieRemovesRows
  - **Depends on:** BE — event-bus delivery guarantees (soft; the nightly sweep is the backstop), CONV — ForgetMovie lands in internal/convert (coordinate), SUB — OnMovieRemoved hook in internal/subtitles (coordinate), PLEX — consumes these path-carrying events for partial scans (movies-9)
  - **Risk:** Low. The events are additive, but check the frontend's useLive topic lists still match.
  - **Resolves:** movies-9
<a id="mov-16"></a>
- [ ] **MOV-16 · Rename with a preview for every track, applied without ever overwriting a file** — `P1` · `M` · Phase 10
  - **Problem:** The Rename button fetches the preview but only uses it to say 'Already named correctly', then renames immediately (MovieDetail.tsx:659-668). The user never sees the proposed name.

Service.Rename (service.go:1201) handles only the default file and ignores version files. It uses a bare os.Rename, which silently replaces an existing target on Linux; with same-name tracks that can overwrite another track's file. It moves the video and subtitles but leaves movie.nfo and the artwork behind, so the old folder is never removed.
  - **Approach:** 1) internal/movies/service.go, RenamePlan(ctx, ids []int64) []RenameItem{movie_id, version_id, label, current, proposed, status: ok|same|conflict|missing}:
       - Covers the default track and every extra track, using [MOV-12](#mov-12)'s per-track naming (edition, label, protected paths).
       - Status is conflict when the proposed path exists and isn't the same file (os.SameFile), or is owned by another track.
       - Status is missing when the current file is gone (one stat per item).
    2) ApplyRename(ctx, items):
       - Re-validates each item at apply time.
       - Moves with os.Link + os.Remove, which can't clobber an existing target. Only if Link fails (EXDEV/EPERM) does it fall back to os.Rename, after an immediate existence check.
       - Moves sidecar subtitles (Importer.MoveEpisodeSubs).
       - When the folder changes, also moves known sidecars (movie.nfo, poster.jpg, fanart.jpg), then removes the empty old folder.
       - Updates records through RepointMovieFile, keeping source_release and media_json ([MOV-02](#mov-02)).
       - Adds a 'renamed' event per track and publishes movie.renamed with paths ([MOV-15](#mov-15)).
       Rename(ctx, id) becomes a thin wrapper over plan+apply.
    3) HTTP: GET /api/v1/movies/{id}/rename returns {items[]}. POST /movies/{id}/rename accepts {version_ids} and returns per-item results.
    4) UI: Rename opens a modal listing old → new per track, with checkboxes. Conflicts are shown with their reason and can't be selected. Apply shows per-item results.
  - **Files:** `internal/movies/service.go`, `internal/library/importer.go`, `internal/httpapi/movies.go`, `web/src/pages/MovieDetail.tsx`, `web/src/lib/api.ts`, `internal/movies/rename_test.go (new)`
  - **Acceptance:**
    - Rename always shows old → new for every track before anything moves
    - Version files are renamed too, using their edition/label naming
    - A rename whose target exists is reported as a conflict and never overwrites anything
    - A folder rename leaves no orphaned old folder holding the .nfo and artwork
  - **Tests:** Go movies.TestRenamePlanIncludesVersions (temp dir); Go movies.TestApplyRenameRefusesExistingTarget: a target created between plan and apply is refused, and both files stay intact; Go movies.TestApplyRenameKeepsSourceReleaseAndMedia; Go movies.TestApplyRenameMovesNFOAndArtwork
  - **Depends on:** [MOV-12](#mov-12), [MOV-15](#mov-15), [MOV-02](#mov-02) (keep media_json across rename; soft)
  - **Risk:** Medium. This moves real files: exercise it only in temp dirs and on a throwaway test title. Hardlinks on Unraid user shares can fail across disks, which is why the fallback path exists.
  - **Resolves:** movies-14
<a id="mov-17"></a>
- [ ] **MOV-17 · Organize library: a library-wide rename preview and background apply after a naming change** — `P2` · `M` · Phase 10
  - **Problem:** A naming change affects only new imports. The only rename tools work per title (movies/{id}/rename), so a new scheme, for example Plex {edition-…} tags, can't be applied to the existing library. After changing the scheme, the owner has to open every movie and rename it blind.
  - **Approach:** 1) Backend, reusing [MOV-16](#mov-16)'s RenamePlan and ApplyRename:
       - GET /api/v1/movies/organize?ids=…|all returns {counts: {rename, same, conflict, missing}, items (changed and conflict only, paged), token}. token is a hash of the (id, version, current, proposed) set.
       - POST /api/v1/movies/organize {token, ids?} runs as a background job. It rejects with 409 if a recomputed plan no longer matches the token for the requested ids. It skips conflicts and items that changed, publishes movie.organize.progress {done, total, renamed, skipped}, keeps the last summary in memory and returns 202.
       - It runs through [MOV-07](#mov-07)'s queue infrastructure as kind 'organize' (one at a time), or as a BE job kind if the runner exists.
    2) Shape the endpoint and row type so SER can add series later (media field on rows); series is out of scope here.
    3) UI:
       - An 'Organize library…' entry in the Movies overflow menu, and a 'Preview rename' button in Settings → Movie naming.
       - Both open a modal with counts first ('120 to rename · 880 already correct · 2 conflicts'), then a list of from → to with conflicts. Apply asks for confirmation and shows live progress.
       - The mass editor's 'Rename…' ([MOV-19](#mov-19)) opens the same modal scoped to the selection.
    4) After apply, trigger PLEX's partial-scan hook if it exists, through [MOV-15](#mov-15)'s movie.renamed events.
  - **Files:** `internal/movies/service.go`, `internal/httpapi/movies.go`, `internal/httpapi/server.go`, `internal/automation/moviequeue.go`, `web/src/pages/Movies.tsx`, `web/src/pages/Settings.tsx`, `web/src/components/OrganizeModal.tsx (new)`, `web/src/lib/api.ts`, `internal/httpapi/organize_test.go (new)`
  - **Acceptance:**
    - The preview lists only the titles whose path would change, and makes no changes
    - Apply renames only the listed items, skips conflicts and items that changed since the preview, and reports counts with live progress
    - Seeding hardlinks are unaffected: the download copy is untouched
  - **Tests:** Go httpapi.TestOrganizePreviewNoChangesWhenSchemeMatches; Go httpapi.TestOrganizePreviewDetectsConflict; Go httpapi.TestOrganizeApplySkipsConflictsAndStaleToken (temp library, never real files)
  - **Depends on:** [MOV-16](#mov-16), [MOV-07](#mov-07), BE — job runner (soft), CFG — naming tokens/presets (draft system.t38; soft), SER — series organize reuses this shape (not blocking)
  - **Risk:** Mass renames make Plex re-scan and can briefly duplicate items. Test only on scratch libraries; the owner runs the first real organize on a small selection.
  - **Resolves:** movies-14, system-14
<a id="mov-18"></a>
- [ ] **MOV-18 · Movie detail page: one contextual primary action, an overflow menu with Delete, and card menus that work on touch** — `P1` · `M` · Phase 10
  - **Problem:** The grid's remove (X) and 'Search now' buttons are 'hidden … group-hover:grid' (Movies.tsx:546-567), so they only appear on mouse hover, and table rows only offer Search. The detail Toolbar (MovieDetail.tsx:639-724) has Monitor, Refresh, Auto-grab, Search indexers, Upload torrent, Manual import and Rename, but no Delete. On a phone or tablet there is no way to remove a movie at all, and the toolbar is a flat row of equally weighted buttons.
  - **Approach:** 1) MovieDetail Toolbar:
       - The primary button depends on state. It is 'Search' when the movie is monitored and missing (queue kind missing), and 'Search for upgrade' when it has a file that doesn't fit and upgrades are allowed (from [MOV-05](#mov-05)'s acquisition block). Otherwise there is no primary button.
       - The Monitor switch stays.
       - Everything else moves into a ⋯ overflow menu: Interactive search, Manual import, Upload torrent, Rename… ([MOV-16](#mov-16) modal), Refresh & rescan, Delete movie….
       - The menu is keyboard accessible: Escape and outside-click close it, and focus returns to the trigger.
       - Use the FE kit's Menu if it exists, otherwise add web/src/components/OverflowMenu.tsx.
    2) Extract the inline DeleteMovieModal (Movies.tsx:600) to web/src/components/DeleteMovieModal.tsx with a bulk mode. Its copy and options come from COPY/SAFE (recycle-aware wording, 'cancel download'; drafts movies.t14 and movies.t13) when those land; this task only moves it. Delete from the detail page uses it and navigates to /movies afterwards.
    3) Grid cards: replace the hover-only X/Search with a ⋯ button. It is always visible under @media (hover: none), and appears on hover or focus for mouse users. The menu holds Search now, Interactive search and Delete…. Table rows get the same menu.
    4) Detail sections become tabs, Files & versions · History · Blocklist, keeping the current visual style.
  - **Files:** `web/src/pages/MovieDetail.tsx`, `web/src/pages/Movies.tsx`, `web/src/components/OverflowMenu.tsx (new)`, `web/src/components/DeleteMovieModal.tsx (new)`
  - **Acceptance:**
    - At 375 px a movie can be deleted from the grid, the table and the detail page
    - No hover-only controls remain on the Movies pages
    - Every action is reachable by keyboard
    - The detail page shows at most one primary button, and it matches the movie's state
  - **Tests:** UI check at 375 px (mobile preset) and desktop: delete from grid, table and detail (test titles only); UI check: Tab / Enter / Escape through the overflow menu
  - **Depends on:** [MOV-05](#mov-05) (acquisition state for the primary button), [MOV-07](#mov-07) (queue kinds; soft), [MOV-16](#mov-16) (Rename… modal; soft — the old button can stay until it lands), FE — component kit Menu/Modal (soft), SAFE/COPY — delete cancels the download and recycle-aware delete copy (drafts movies.t13 / movies.t14; soft)
  - **Risk:** Low. Keep the existing visual style.
  - **Resolves:** movies-7, movies-14
<a id="mov-19"></a>
- [ ] **MOV-19 · Mass editor backed by one bulk endpoint: monitor, profile, availability, search, rename and delete** — `P1` · `M` · Phase 10
  - **Problem:** bulkMonitor and bulkProfile (Movies.tsx:110-133) run Promise.all over per-movie calls inside try/finally with no catch. One failure gives no toast and no refresh, and the selection stays. bulkProfile ignores each movie's 'downgrade' answer, so films whose file is better than the new profile are silently left alone. The only bulk actions are monitor and profile: there is no availability, search, rename or delete.
  - **Approach:** 1) Backend: PUT /api/v1/movies/bulk {ids[], monitored?, quality_profile?, min_availability?, search: none|missing|missing_and_upgrade}.
       - Validate the profile once with KnownProfile and the availability value once.
       - Apply all changes in one SQL transaction (repo.BulkUpdate). An invalid request changes nothing.
       - After commit, classify each item: monitored with no file → enqueue a missing search ([MOV-07](#mov-07)); has a file and Quality.WouldReject → exceeds_profile; otherwise enqueue an upgrade when search allows it.
       - Response: {updated, queued, exceeds_profile:[{id,title,size_gb}], errors:[{id,msg}]}.
    2) POST /api/v1/movies/bulk/regrab {ids} backs 'Download smaller versions' and enqueues kind regrab.
    3) DELETE /api/v1/movies/bulk {ids, delete_files, cancel_downloads} calls the movie delete (with SAFE's cancel-download semantics when present) per id and returns per-item results.
    4) Frontend (Movies.tsx): the mass-editor bar uses these endpoints and gains:
       - an Availability select,
       - 'Search selected',
       - 'Rename…' ([MOV-17](#mov-17)'s organize modal scoped to the selection),
       - 'Delete…' (DeleteMovieModal from [MOV-18](#mov-18) in bulk mode).
       On errors, toast the counts and keep the failed ids selected; on success, clear the selection. When exceeds_profile isn't empty, ask 'N files are better than this profile — Download smaller versions / Keep them'. It works in grid and table views. Keep the per-movie endpoints for the detail page.
  - **Files:** `internal/httpapi/movies.go`, `internal/httpapi/server.go`, `internal/movies/repo.go`, `internal/movies/service.go`, `internal/automation/moviequeue.go`, `web/src/pages/Movies.tsx`, `web/src/components/DeleteMovieModal.tsx`, `web/src/lib/api.ts`, `internal/httpapi/bulk_test.go (new)`
  - **Acceptance:**
    - Setting a profile on 300 films is one request that commits atomically and reports updated, queued and exceeds-profile counts
    - Films whose file exceeds the new profile are listed, with a one-click 'Download smaller versions'
    - A partial failure shows a toast with counts and leaves the failed films selected
    - Monitor, profile, availability, search, rename and delete all work on a selection from the grid or the table
  - **Tests:** Go httpapi.TestBulkUpdateTransactional: an unknown profile rejects the whole request and changes nothing; Go httpapi.TestBulkUpdateClassifiesItems: missing → queued; fits → upgrade queued; too big → exceeds_profile; Go httpapi.TestBulkDeletePerItemResults (temp dirs); UI check: select 20, set a profile, see the summary; force a failure and see the toast
  - **Depends on:** [MOV-07](#mov-07), [MOV-17](#mov-17) (bulk rename), [MOV-18](#mov-18) (shared DeleteMovieModal), SAFE — delete cancels in-flight downloads (draft movies.t13, finding movies-8; soft)
  - **Risk:** Low to medium. Bulk delete moves real files to the recycle bin, so test on throwaway titles.
  - **Resolves:** movies-4, movies-14

#### Milestone: M6 — Import, browse and match better

_Large scans show progress and a summary, and the needs-review list survives a restart. Vanished files are flagged across the library, with a guard against an unmounted array. The grid has real sorting, badges and consistent filters. The add flow marks films already in the library and takes an availability choice. Releases named with original or alternative titles match._

<a id="mov-20"></a>
- [ ] **MOV-20 · Library scan: live progress, an end-of-scan summary, and a needs-review list that survives restarts** — `P2` · `M` · Phase 10
  - **Problem:** The Movies page polls 12 × 2.5 s and then stops (Movies.tsx:53-71), with no summary and no link to 'needs review'. The library.scanned bus event (httpapi/movies.go:215) has no frontend consumer. lastUnmatched is an in-memory field (service.go:57, 295-306), so it's lost on restart. Settings → Library reloads the review list 5 s after the scan starts. Two scans can run at once, and a fixed 15-minute timeout cuts off large libraries.
  - **Approach:** 1) ScanLibrary(ctx, root, opts, progress func(done, total int)).
       - The handler publishes library.scan.progress {media:'movie', done, total} every ~25 folders, and library.scanned {media, imported, attached, skipped, duplicates, unmatched} at the end.
       - A service-level mutex blocks concurrent scans (409 'a scan is already running').
       - Replace the fixed 15-minute timeout with one proportional to the folder count (e.g. 2 s per folder, minimum 15 min).
    2) Persist unmatched folders with a new migration (e.g. NNNN_library_unmatched.sql): CREATE TABLE library_unmatched(media TEXT NOT NULL, root TEXT NOT NULL, folder TEXT NOT NULL, title TEXT, year INT, candidates_json TEXT, scanned_at TEXT, PRIMARY KEY(media, folder)).
       - A new scan replaces that media/root's rows in one transaction.
       - ImportFolderAs deletes the row.
       - LastUnmatched reads the table, so series can reuse it later.
    3) UI:
       - Movies.tsx drops the 12-tick polling. It shows a progress bar ('Scanning 312 / 1,204 folders'), then a summary banner ('812 added · 14 attached · 9 need review →') linking to the needs-review list.
       - Library.tsx reloads the review list on library.scanned instead of after 5 s.
  - **Files:** `internal/movies/service.go`, `internal/movies/repo.go`, `internal/httpapi/movies.go`, `internal/store/migrations/NNNN_library_unmatched.sql (new)`, `web/src/pages/Movies.tsx`, `web/src/pages/Library.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A large scan shows live progress and ends with a summary linking to the folders that need review
    - The needs-review list survives a restart
    - Starting a second scan while one runs returns a clear 'already scanning' message
  - **Tests:** Go movies.TestUnmatchedPersisted: a new Service instance still lists them; ImportFolderAs removes the row; Go movies.TestScanProgressCallback; Go httpapi.TestConcurrentScanRejected
  - **Depends on:** [MOV-06](#mov-06), BE — job runner (soft; the scan can become a job kind)
  - **Risk:** Low. Keep the table media-agnostic so SER can adopt it.
  - **Resolves:** movies-13
<a id="mov-21"></a>
- [ ] **MOV-21 · Notice movie files that vanish from disk (guarded daily missing-file sweep)** — `P2` · `M` · Phase 10
  - **Problem:** Only the per-movie Refresh & rescan (service.go:1104-1125) notices that a tracked file is gone. Until someone opens the movie, it stays 'Downloaded' in the grid and table views, has_file stays true, and it is never searched again. walk-7 is how the owner found one by accident.
  - **Approach:** 1) New migration (e.g. NNNN_movies_file_missing.sql): ALTER TABLE movies ADD COLUMN file_missing_since TEXT. NULL means present. Extra tracks use their File.Missing from [MOV-02](#mov-02)'s live stat on the detail page and are not swept in this task.
    2) movies.Service.CheckMissingFiles(ctx):
       - Stats MovieFilePath for each has_file movie, sequentially, with no probes.
       - Guard first: if the movies root (Config.MoviesDir) doesn't exist or is empty, or more than max(20, 5%) of tracked files are missing in one pass, mark nothing. Log one warning, 'Movies folder looks unmounted', and expose it to the health registry if OBS has one.
       - Otherwise set file_missing_since for newly missing files (AddEvent 'missing' once) and clear it for files that came back.
       - VersionsLive ([MOV-02](#mov-02)) also sets or clears the column when it sees the default track's state.
    3) Register it in cmd/arrmada/main.go: sched.Register('movies-missing-files', 24*time.Hour, false, …). Daily rather than 6-hourly, so the array isn't woken more than Convert's daily index sweep already does. Also run it at the end of a library scan.
    4) Expose file_missing (bool, read from the column, no stat on list) on Movie and on MovieSummary ([MOV-10](#mov-10)). The Movies grid and table use movieStatus ([MOV-03](#mov-03)), which shows a 'File missing' badge, and gain a 'File missing' filter pill.
    5) Nothing is cleared, deleted or searched automatically. The owner decides from the detail page ([MOV-03](#mov-03)).
  - **Files:** `internal/store/migrations/NNNN_movies_file_missing.sql (new)`, `internal/movies/movie.go`, `internal/movies/repo.go`, `internal/movies/service.go`, `internal/movies/missing_test.go (new)`, `cmd/arrmada/main.go`, `web/src/pages/Movies.tsx`, `web/src/lib/movieStatus.ts`
  - **Acceptance:**
    - Within one sweep after a test movie's file is removed, the Movies grid and table show 'File missing' and the 'File missing' pill counts it
    - Restoring the file clears the flag on the next sweep or the next detail view
    - Unmounting or emptying the movies root, or removing more files than the threshold, flags nothing and logs one warning
    - The sweep never deletes or moves a file
  - **Tests:** Go movies.TestCheckMissingFiles (temp dirs, in-memory store): a removed file is flagged and an event recorded; a restored file is cleared; a missing root flags nothing; above-threshold missing flags nothing; Go store: migrations apply cleanly; UI check with a throwaway test library only
  - **Depends on:** [MOV-03](#mov-03), [MOV-02](#mov-02), [MOV-10](#mov-10) (summary DTO field; soft), OBS — health registry for the unmounted warning (soft)
  - **Risk:** An unmounted Unraid array or a spun-down network share could mass-flag files; the root check and threshold guard mitigate this. This is the one periodic job allowed to stat library files: it stats sequentially, once a day, and never probes. Series episodes have the same gap, which belongs to SER.
  - **Resolves:** walk-7
<a id="mov-22"></a>
- [ ] **MOV-22 · Library grid: sort options, quality badges, monitored dot, better search and consistent filters** — `P2` · `M` · Phase 10
  - **Problem:** The grid sorts only by plain title localeCompare (Movies.tsx:93-97), so 'The …' titles clump under T, and search matches the title only. Cards show no quality badge, monitored state or added date. Filter names and meanings disagree with the Wanted view. At 375 px the toolbar wraps '+ Add movie' into a narrow column.
  - **Approach:** 1) Add a sort menu, persisted with usePersisted: Title (sort_title from [MOV-10](#mov-10)), Added (newest), Year, Rating, Size. The table keeps its column sorts.
    2) Search matches the title, original title and alternative titles ([MOV-24](#mov-24), when present) plus the year, e.g. 'dune 2021'.
    3) Card badges from the summary media facts: resolution (4K/1080p), DV/HDR, Atmos, a monitored dot, a progress ring for any download kind (missing, upgrade, version), and a 'Wanted' label for monitored films without a file. Status comes from movieStatus ([MOV-03](#mov-03)).
    4) Filters: All · Wanted (monitored, no file) · Downloading · No file (unmonitored) · File missing ([MOV-21](#mov-21)) · Doesn't fit (fit data) · Unmonitored, each with a count. Use the terminology agreed with COPY.
    5) The toolbar wraps cleanly at 375 px, and '+ Add movie' no longer collapses into a narrow column.
    6) Stretch (P3, separate commit): Netflix-style rows above the grid (Recently added, Downloading, Doesn't fit).
  - **Files:** `web/src/pages/Movies.tsx`, `web/src/lib/api.ts`, `web/src/lib/movieStatus.ts`
  - **Acceptance:**
    - 'The Matrix' sorts under M; sorting by Added, Year, Rating and Size works and is remembered
    - Cards show resolution, HDR and Atmos badges and the monitored state
    - Filter counts match the Wanted view
    - The toolbar is usable at 375 px
  - **Tests:** UI check: every sort and filter on a library with mixed states; toolbar layout at 375 px; Go: sort_title is covered by MOV-10's TestSortTitle
  - **Depends on:** [MOV-10](#mov-10), [MOV-08](#mov-08) (Wanted filter semantics), [MOV-21](#mov-21) (File missing pill; soft), [MOV-24](#mov-24) (alt-title search; soft), FE — shared library grid/toolbar components if FE builds one (soft)
  - **Risk:** Low. Keep the current card visual style; the badges reuse existing tokens.
  - **Resolves:** movies-14
<a id="mov-23"></a>
- [ ] **MOV-23 · Add-movie flow: in-library flags, availability choice, no hidden global toggle, and a one-call collection add** — `P2` · `M` · Phase 10
  - **Problem:** Problems with the add flow:
- The Add modal (Movies.tsx:643) adds on a single row click.
- It doesn't flag films already in the library, because MovieLookup (api.ts:1865) has no in_library field, so the user gets a 409.
- It has no minimum-availability choice.
- Its 'Search on add / Add unmonitored' toggle silently rewrites the global search_on_add setting, which Series and Books share (Movies.tsx:665-670).
- Collection 'Add all' runs addOne one at a time and reloads the collection from TMDB after each add (MovieDetail.tsx:455-472).
The monitor choice itself already exists.
  - **Approach:** 1) handleLookupMovies annotates results with in_library and library_id, using a repo map from tmdb to id (extend ExistingTMDBIDs to return ids). The modal shows an 'In library →' link instead of adding.
    2) Clicking a result opens a small confirm panel instead of adding at once. It offers Quality profile, Minimum availability (announced / in cinemas / released) and the existing Search-on-add vs Add-unmonitored choice, which applies to this add only. The global default is edited in Settings. An explicit 'Remember as default' checkbox is the only thing that writes it. addMovieRequest gains min_availability, validated like SetMinAvailability.
    3) Add POST /api/v1/movies/bulk-add {tmdb_ids[], quality_profile, min_availability, monitored, search}. It adds server-side (sequential TMDB fetches through the metadata cache), skips existing films, returns per-id results, and queues searches through [MOV-07](#mov-07). Collection 'Add all' calls it once and reloads the collection once.
  - **Files:** `internal/httpapi/movies.go`, `internal/httpapi/server.go`, `internal/movies/service.go`, `internal/movies/repo.go`, `web/src/pages/Movies.tsx`, `web/src/pages/MovieDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Search results already in the library are marked and link to the existing film, and the 409 error no longer appears
    - Availability can be chosen when adding
    - Adding a film no longer changes the global search-on-add default unless 'Remember as default' is ticked
    - 'Add all' on a 10-film collection makes one request and reloads the collection once
  - **Tests:** Go httpapi.TestLookupAnnotatesInLibrary; Go httpapi.TestBulkAddSkipsExisting: per-item results; searches queued, not run inline; Go httpapi.TestAddMovieMinAvailability; UI check: add flow with availability; collection Add all
  - **Depends on:** [MOV-07](#mov-07)
  - **Risk:** Low. Series and Books keep reading the shared global default.
  - **Resolves:** movies-14
<a id="mov-24"></a>
- [ ] **MOV-24 · Match movie releases against the original and alternative titles** — `P2` · `M` · Phase 10
  - **Problem:** releaseIsForMovie (coordinator.go:765), inQueue, movies.Match and Matcher (service.go:1247-1300) only accept a release when titleKey(parsed title) equals titleKey(m.Title). MovieExtra stores no original or alternative titles, and unlike series, the movie path never uses an original title. Re-titled and alt-titled films, some anime and some foreign films come back from indexers and are thrown away as 'wrong title', so they sit in Wanted forever. titleKey already folds accents and punctuation, so the gap is narrower than the audit's Léon example (verify note).
  - **Approach:** 1) internal/metadata/tmdb.go GetMovie: add 'alternative_titles' to append_to_response. MovieDetails gains OriginalTitle and AltTitles []string: deduped, Latin-script only, from US/GB/the original country, capped at 15, and cached through the existing metadata cache.
    2) movies.MovieExtra gains OriginalTitle and AltTitles. They live in the extra_json blob, so no migration is needed. extraFrom fills them. Existing movies backfill on Refresh and through a one-time, low-rate background job (one movie every 2 s) that sets a settings flag when it finishes.
    3) Matching: add movies.TitleKeys(m), the set of keys for Title, OriginalTitle and AltTitles.
       - releaseIsForMovie, inQueue, Service.Match, Matcher and MatchRelease (and so HoldMovieImport and the import resolver) accept any key, still with the ±1 year check.
       - Drop any alt key that equals another library movie's primary key, to avoid cross-matching. Matcher builds that collision set once per snapshot.
    4) Search: when the primary text search finds no matching release, and OriginalTitle differs and is Latin-script, run one extra text search with OriginalTitle + year. Skip this when ACQ's id-based search (imdbid/tmdbid, draft movies.t24) already ran for that indexer. Count the extra query in the outcome ([MOV-04](#mov-04)).
  - **Files:** `internal/metadata/tmdb.go`, `internal/metadata/provider.go`, `internal/movies/movie.go`, `internal/movies/service.go`, `internal/automation/coordinator.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - A release named with a movie's original title is accepted for it and imported to it
    - An alternative title that equals another library movie's title does not cross-match
    - The year check still rejects same-titled films from other years
    - Existing movies gain alternative titles without a burst of TMDB requests
  - **Tests:** Go automation.TestReleaseIsForMovieAltTitles; Go movies.TestMatchAltTitleCollisionIgnored; Go metadata.TestParseAlternativeTitles (fixture JSON)
  - **Depends on:** ACQ — id-based Torznab search (draft movies.t24; soft, avoids double queries)
  - **Risk:** Medium. Short or generic alternative titles could cause false matches; the year check and the collision guard mitigate this. Keep the matching keys identical on the search side and the import side (titleKey and normalizeTitle must agree), or grabs will never import.
  - **Resolves:** movies-11

#### Risks

- Migration numbering: this epic adds 5 migrations (version media, search outcome, grabs.replaces_path, library_unmatched, file_missing_since) while other epics add theirs. Take the next free number at implementation time and run the store migration test.
- MOV-12 must ship as a unit. Routing grabs to tracks without per-track naming, or the other way round, recycles another track's file just as today. Don't deploy half of it.
- Cached media facts can go stale when files are swapped outside Arrmada. The size+mtime check on the detail page and Refresh cover it, and the sweeps deliberately never re-check.
- MOV-09 changes stall and seeding timing for upgrade grabs. Verify the early-'imported' behaviour with the first test before changing it, and exercise it against a scratch download category.
- HTTP shape changes (202 {position} from search endpoints, MovieSummary from GET /movies) break frontend callers that aren't updated. Grep api.ts and every page that uses api.movies(): Movies, Quality and Reviews.
- File-moving tasks (MOV-12, MOV-16, MOV-17, MOV-19, MOV-06) touch real paths. Test only in temp dirs and on throwaway test titles, never the owner's library. Plex will re-scan renamed items.
- The in-memory search queue loses queued work on restart. The periodic sweeps re-cover missing movies; this is documented and accepted.
- The missing-file sweep (MOV-21) could mass-flag files on an unmounted array. The root check and threshold guard are mandatory, and nothing acts on the flag automatically.
- Scene edition naming and alternative titles are fuzzy. MOV-13 can miss some real DC releases until the needles grow, and MOV-24 can false-match short alt titles; the year check and the collision guard are the mitigations.
- Concurrency-heavy code (the queue, background re-probes) must pass go test -race in Docker before pushing; CI fails on Linux race errors that Windows can't reproduce.

#### Out of scope

- Plex partial scans after import, upgrade, rename or delete, 'Open in Plex' and watched status on the detail page (PLEX, movies-9). MOV-15 only provides the events.
- Role and path restrictions on interactive search and manual import (SEC, movies-3).
- Cancelling in-flight downloads when a movie is deleted, and the recycle-bin fixes (SAFE, movies-8).
- The TMDB env-var banner, 'Activity' wording elsewhere, and recycle-bin delete copy (COPY, the rest of movies-6).
- IMDb/TMDB id-based Torznab searching (ACQ, draft movies.t24).
- Series and books equivalents: Wanted view, organize, missing-file sweep, mass editor (SER/BOOK).
- Indexing extra version tracks in Convert, so the Cutoff tab can judge them (CONV).
- New naming tokens {tmdbid}/{imdbid}/{tvdbid} and naming presets (CFG, draft system.t38).
- Persisting the movie search queue across restarts.
- Netflix-style rows above the grid. This is a stretch item in MOV-22 and is not required for its acceptance.

