# ACQ — Acquisition core & Activity hub

_Part of the [Arrmada roadmap](../../ROADMAP.md). 32 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Make acquisition trustworthy and explainable. Every download Arrmada starts is tracked by its info hash from grab to seed removal. Stalled or dead torrents are detected and replaced without destroying the only copy. Every search leaves a readable outcome, and an indexer or download-client outage is never mistaken for 'nothing found'. One Activity page (Queue, Needs you, Wanted, History, Blocklist) shows what Arrmada is doing, what is stuck and what needs the owner, with actions that work for movies, series, books and music alike.

**Why.** Today the owner can't trust or explain what acquisition is doing, and several paths silently lose content.

- **Wrong-item imports (ops-1, music-11).** Review's 'Import into a different…' lists MOVIES for book and music downloads. It then files the content under whichever book or album shares that numeric id, and 'Import anyway' on an unmatched one answers 500.
- **Outages count as misses (integrations-1).** An indexer outage is recorded as a search miss, so titles back off up to 12h. A wanted book searched twice during an outage is never searched automatically again.
- **Disk guard defeated (ops-2).** 'Resume all' defeats the disk guard, the cache pool fills anyway, and the Dashboard keeps promising the torrents 'will resume automatically'.
- **Dead torrents look alive (ops-3, quality-9).** They read 'downloading' with ETA ∞. Stall fail-over is off by default (stall_minutes 0 everywhere, frozen into every grab row), and when it is on it deletes the only release before checking for an alternate. One stalled S03 pack also freezes S04 episodes out of every sweep.
- **Seeding forever (ops-6).** Downloads resolved through Review never leave the client, and requesters can see 'Importing' indefinitely.
- **Block misfires (ops-7).** Block on an audiobook deletes it but blocklists nothing, so the next sweep re-grabs it. A year-less TV torrent blocks a same-named movie. Global and book/music blocklist rows can't be seen or undone (ops-12).
- **Review dead ends (ops-8).** Review offers the same four buttons for five different problems, several of them dead ends, with no file list, age or link.
- **'Why isn't it downloading?' has no answer (ops-15, product-10, series-9, backend-9).** 'Searching…' pulses forever even though last_search_at and search_misses exist. Search results, including the 'found 37, took none' breakdown, only reach the log. 'Search now' reports nothing.
- **Identity is fuzzy (backend-15, backend-12, walk-6).** It rests on three drifting title normalizers ('Love & Death' vs 'Love.and.Death'). A dead qBittorrent looks like an empty queue with 'free 0 GB' and a green 'Live' dot, while sweeps keep querying indexers.
- **The admin view knows less than the requester's (ops-5, ops-9, ops-11, ops-14, product-8, product-12, frontend-10, ops-4).** Downloads, History and Review are three disconnected pages. Transfers are raw torrent names with no poster, title link or requester. Finished-but-not-imported downloads look like healthy seeds. History is a 100-row dump of the imports table with no dates. Pages blind-poll every 3-5s. requests/progress.go already has a better stage vocabulary than the admin page.

**Depends on:** CFG — the 'Save settings fails on every tab' fix (server_time/server_tz round-trip) must land before [ACQ-06](#acq-06) adds downloads_stall_minutes to Settings → Downloads.; SEC — deny-by-default route gating. /api/v1/downloads, /api/v1/history and /api/v1/queue are a.protected today. Every new ACQ route is registered RoleManager from day one, and SEC's route-walk test must cover them ([ACQ-11](#acq-11), 15, 18, 26, 27, 30).; SEC — a role-filtered websocket before [ACQ-19](#acq-19) and [ACQ-32](#acq-32) push search.finished, queue.changed and enriched import.held events.; SEC — the manual-import path restriction to library/download roots before [ACQ-31](#acq-31) offers 'Import into…' for unmanaged torrents. [ACQ-13](#acq-13)'s mapper reuses the same root check.; SAFE — download Delete confirmation and the /queue/all guard (ops-5). [ACQ-09](#acq-09) adds the grab 'removed' transition and an event in the same handler.; OBS — attention feed, sidebar badge and admin alerts (ops-4, ops.t11). OBS consumes [ACQ-12](#acq-12)'s enriched import.held, [ACQ-05](#acq-05)'s download.stalled, [ACQ-15](#acq-15)'s search outcomes and [ACQ-30](#acq-30)'s needs-you counts. Manager.Failures and WrongCategoryDownloads are shared with [ACQ-28](#acq-28), and whichever epic lands first adds them.; INT — per-indexer health/backoff (integrations.t1). [ACQ-02](#acq-02) treats an indexer skipped by backoff as failed. [ACQ-14](#acq-14)'s banner shows skipped/retry_at.; QUAL — quality engine and target-file work. [ACQ-15](#acq-15) adds Evaluation.RejectCode at the 9 RejectReason sites; QUAL must keep the codes. Seeders' weight in ranking (part of quality-9) stays in QUAL.; SER — series grab planner / GrabForScope (series.t1). [ACQ-08](#acq-08) and [ACQ-15](#acq-15) change grabSeriesLimited and searchSeriesOnce; sequence and rebase with SER.; BOOK — book search ladder replacing the 2-miss give-up. [ACQ-16](#acq-16), [ACQ-18](#acq-18) and [ACQ-20](#acq-20) read it through BookNextSearch so they follow automatically.; MUS — music module toggle and sweep backoff. [ACQ-01](#acq-01) shows 'Turn on Music' when the module is off. [ACQ-16](#acq-16) records the music outcomes MUS's backoff should read.; REQ — requester Discover and progress, and the stuck-search notification. [ACQ-20](#acq-20) adds the last-checked line, and [ACQ-28](#acq-28) keeps the requester stage strings identical.; FE — component kit (overflow menu, chips), lazy router, useLiveQuery and the URL-filter hook. All optional for [ACQ-07](#acq-07)/26/27/29/32; local implementations are acceptable.; BE — job runner (backend.t19). Optional for [ACQ-19](#acq-19): persisted search_attempts plus the search.finished event work without it.

#### Design

## North star
Every download Arrmada starts is one durable record keyed by its torrent info hash, from grab to seed removal.
- Every search leaves a readable outcome.
- Nothing stuck is silent, and nothing is removed without a replacement.
- One Activity page shows what Arrmada is doing, what is stuck and what needs the owner, with actions that work the same for movies, series, books and music.

## 1. The acquisition record: `grabs`, evolved in place
There is no parallel `acquisitions` table. `grabs` already carries:
- info_hash (0062)
- the seed-policy snapshot
- manual (0076)
- media_type

Seeding, stall detection, request progress, review lookup and file info all read it. It becomes the record, identified by lower(info_hash). Name matching survives only for legacy hashless rows, until AdoptTorrentHashes (automation/seedadopt.go) pairs them.

Two orthogonal fields:

**`status`** is the lifecycle, written only at decision points: grabbed → held → imported → seeded, with exits to failed, dismissed and removed.
- grabbed: handed to the client, not yet imported
- held: complete and waiting on a Review decision. Never re-grabbed, never seed-cleaned.
- imported: the library has it. Removed WITH its data at the seed goal.
- dismissed: the review was dismissed. Removed WITHOUT its data at the seed goal.
- failed: ended by stall fail-over, Block or Reject
- removed: the user removed it from Downloads
- seeded: removed after the seed goal

**`phase`** is the live client state, written by the reconciler from the client snapshot ([ACQ-24](#acq-24)): queued | metadata | downloading | stalled | paused | checking | moving | complete | seeding | error | missing. Alongside it the reconciler writes:
- progress, and progress_at (the last forward progress, which is the persisted stall clock)
- last_seen_at, completed_at, last_error, updated_at
- scope: 'v<id>', 'S03', 'S03E05', 'S01-S04', 'complete', 'ebook', 'audiobook' or 'album'
- client_id

Status sets are defined once in automation/store.go and used everywhere. A source-scanning test forbids raw status literals in any other file.

| set | statuses | used by |
|---|---|---|
| stallWatch | grabbed | DetectStalled |
| inFlight | grabbed (name guards: last 24h only), held | pending-grab guards, 'already downloading', requester progress |
| seedCleanup | imported, dismissed | ManageSeeding |
| live | grabbed, held, imported, dismissed | Seeding-tab rules, GrabLinks |

Transitions go through `setGrabStatusByHash(ctx, hash, name, mediaType, to)`.

## 2. Download client snapshot
`download.Service.Snapshot(ctx)` returns {Items (each tagged ClientID), Complete, Health[{ID, Name, Reachable, LastOK, Since, LastErr}], At}.
- 2s cache with single-flight.
- Invalidated by Add, Pause, Resume, Remove, Action and SetCategory.
- Queue and QueueComplete become wrappers over it.
- Any sweep that can grab skips its cycle when the snapshot errors or is incomplete.

`Item` keeps qBittorrent's RawState, seeds/peers, LastActivity, AddedOn and Availability. `Item.Phase()` maps the raw states, and State keeps its old meaning for every existing caller.

## 3. Stall policy (non-destructive)
**Window.** `stallWindow(g)` works out the timeout:
- the grab's own value > 0 wins
- < 0 means off
- 0 means the profile's current value, and a profile value of 0 means the global `downloads_stall_minutes` (default 360; 0 = off)

So grabs recorded before this change are covered too.

**Clock.** The clock holds while a torrent is paused (by the user or the disk guard), queued, checking or moving. Fetching metadata does not hold the clock: a magnet with no metadata after 6h is dead.

**Fail-over replaces first:**
1. Search the stalled scope, excluding the stalled release.
2. Only if something else is grabbed is the stalled release blocklisted and removed.
3. Otherwise the torrent stays. A 'still waiting' event is written once per window, and the clock restarts.
- A torrent missing from a complete snapshot fails over as it does today.
- At most 3 fail-overs per tick.
- Every fail-over publishes `download.stalled`.

## 4. Stage model (internal/pipeline)
A pure `Describe(Input) (Stage, Why)`. Its input is the record, the client item, any review, any import failure, the guard hold and the category.

Stages: queued, fetching_metadata, downloading, stalled, paused, paused_by_guard, error, downloaded, importing, import_retrying, needs_review, wrong_category, imported, seeding, not_managed, removed.

requests/progress.go maps these to the requester vocabulary; for example needs_review and import_retrying become Importing with the note 'Being checked'. Admin and requester views therefore never disagree, and requesters never see admin detail.

## 5. Search outcomes
Every search returns a `SearchOutcome` and writes a `search_attempts` row, keeping the newest 20 per item.
- **Triggers:** sweep, rss, manual, request, add, upgrade, stall, replace.
- **SearchOutcome fields:** Returned, WrongTitle, Blocklisted, Pending, OutOfScope, Rejected, ReasonsByCode, Eligible, Grabbed, GrabbedTitles, Example, IndexerErrors.
- **Outcome values:** grabbed | nothing_found | none_suitable | indexers_failed | skipped_in_flight | error.
- `quality.Evaluation` gains a stable RejectCode.
- A search where every indexer failed is `indexers_failed` and is never counted as a miss.
- The backoff is exposed through NextSearchAt and BookNextSearch, so Wanted can show the next automatic try.

## 6. Review, Block, Blocklist
**Review.** import_reviews gains reason_code (mismatch | unmatched | numbering | import_failed | no_media), resolution and resolved_at.
- Each code offers only the actions that can succeed.
- Import targets are listed per kind (movie, show, book, album).
- The file list is confined to the review's content_path.
- Numbering reviews get a per-file episode mapper.

**Block.** Block resolves hash → grab → kind. When there is no grab it falls back to the torrent's category, and a TV torrent is never matched against movies. It refuses torrents that aren't linked to anything.

**Blocklist.** One list/unblock API across movie, series, book, music and global rows.

## 7. History
The per-kind event tables (movie_events, series_events, book_events, artist_events) are the log, indexed on (created_at, id).
- One eventsFeed() UNION ALL query, with per-branch filters and keyset paging, serves both History and the Dashboard.
- Every lifecycle transition writes an event.
- The imports table remains a dedupe guard only.

## 8. HTTP surface
All routes are RoleManager. Never put 'activity' in an API path: ad-block lists block it (see the header comment in activity.go).
- GET /api/v1/downloads: aggregate today; later split into /downloads/transfers and /downloads/wanted
- POST /api/v1/queue/{hash}/resume|block|action: 409 explains a guard hold, 422 explains an unlinked block
- POST /api/v1/queue/{hash}/category
- GET /api/v1/reviews/{id}/targets?q=
- GET /api/v1/reviews/{id}/files
- POST /api/v1/reviews/{id}/map
- POST /api/v1/reviews/bulk
- POST /api/v1/reviews/{id}/reject {find_another}
- GET /api/v1/blocklist?type=&q=
- DELETE /api/v1/blocklist/{id}
- GET /api/v1/searches?kind=&id=&since=&limit=
- POST /api/v1/wanted/{kind}/{id}/search
- GET /api/v1/history?kind=&event=&q=&before=&limit=
- GET /api/v1/downloads/needs-you
- POST /api/v1/imports/{hash}/retry

Bus topics: import.held (enriched), download.stalled, search.finished, queue.changed and release.grabbed, plus the existing import topics.

## 9. UI: /activity/:tab
One sidebar entry, Activity, replaces Downloads, History and Review. The old URLs redirect.
- **Queue:** Downloading / Finishing / Seeding pills. Each card is poster-first:
  - the title, linking to its page
  - a scope chip (S03, Audiobook)
  - a 'for <requester>' chip
  - a stage chip with a why line
  - the release name as a secondary monospace line
  - the primary action plus a ⋯ menu (Reannounce, Recheck, priority, Block, Remove)
- **Needs you**, each kind with its own actions:
  - review cards, by reason code
  - failed imports in retry: Retry now
  - stalled or errored transfers: Reannounce, Recheck, Try another release
  - wrong-category downloads: Move to TV
  - unmanaged completed torrents: Import into…, Ignore
- **Wanted:** Searching / Upcoming rows for movies, series, books and albums. Each row has state chips and a Search now button, and a line such as 'Last searched 3h ago · 34 found, none suitable (mostly over your bitrate ceiling) · next try in ~6h'.
- **History:** a filterable event log grouped by day, with Load more.
- **Blocklist:** every type, with Unblock.

The page keeps the current warm dark palette, terracotta accent and type scale. It updates from websocket topics, and only the visible Queue tab polls (every 5s, paused while the browser tab is hidden).

## 10. Migration plan
Migration numbers are assigned at implementation time (next free number ≥ 0090), because other epics also add migrations.
1. Grab lifecycle ([ACQ-09](#acq-09)): import_reviews.resolution and resolved_at; backfill grabs to held or dismissed.
2. import_reviews.reason_code, with a backfill from the reason text ([ACQ-12](#acq-12)).
3. The search_attempts table ([ACQ-15](#acq-15)).
4. The grabs acquisition columns plus idx_grabs_item_status ([ACQ-24](#acq-24)).
5. created_at indexes on the four event tables ([ACQ-27](#acq-27)).

Behaviour changes, each called out in its commit:
- A profile stall value of 0 now means 'use the default (6h)'.
- Resume all skips guard-held torrents.
- Block refuses torrents that aren't linked to anything.

Standing rules:
- No audiobook listening data anywhere in Activity.
- Requester names only on manager-only endpoints.
- Indexer errors pass through sanitizeErr, so no apikey or MAM token reaches the browser.
- Mapper, stall and import tests use temp dirs and fake clients, never the owner's library.

#### Milestone: M1 — Nothing silently lost

_Book and music reviews import into the item the owner picks, and never into a movie id. An indexer outage no longer pushes titles into a 12h backoff or drops books from automatic search. 'Resume all' can no longer defeat the disk guard, and Downloads shows what the guard is holding._

<a id="acq-01"></a>
- [x] **ACQ-01 · Review: 'Import into a different…' and 'Import anyway' work for book and music reviews** — `P0` · `S` · Phase 0
  - **Problem:** ReassignModal (web/src/pages/Reviews.tsx:99-105) loads api.movies() for every review that isn't a series, and labels the button and modal 'movie'. ImportReview (internal/automation/reviews.go:305-407) uses targetID as the destination with no kind check. A picked movie id therefore goes straight to books.Get (reviews.go:389) or music.GetAlbum (:366), and the files are filed under whatever book or album shares that number. Unmatched book and music reviews carry ExpectedID 0 (books.go:626, music.go:258), so 'Import anyway' calls Get(0) and answers 500. api.ts:1883-1890 types media_type as series|movie only.
  - **Approach:** Backend
    1. internal/automation/reviews.go:
       - Add `type ReviewTarget struct{ID int64; Kind, Title string; Year int; Subtitle, PosterURL string}`.
       - Add `func (c *Coordinator) ReviewTargets(ctx, reviewID int64, q string, limit int) ([]ReviewTarget, error)`, switching on the review's MediaType:
         - movie → c.movies.List
         - series → c.series.List
         - book → c.books.List, with Subtitle = author
         - music → a new music.Service.SearchAlbums(ctx, q, limit), backed by a repo query `SELECT al.id, al.title, al.year, ar.name, al.cover_url FROM albums al JOIN artists ar ON ar.id = al.artist_id WHERE al.title LIKE ? OR ar.name LIKE ? ORDER BY ar.name, al.year LIMIT ?`, with Subtitle = artist
       - The in-memory kinds use a case-insensitive contains filter. Results are capped at 200.
       - A nil module (c.music == nil when Music is off) returns a new ErrModuleOff.
    2. Routes and handlers:
       - Add `GET /api/v1/reviews/{id}/targets?q=` (RoleManager) in server.go, with the handler in httpapi/reviews.go.
       - ErrModuleOff → 409 'Turn on Music to import this'.
    3. Change ImportReview to `ImportReview(ctx, id, targetID int64, targetKind string)`:
       - A non-empty targetKind that differs from r.MediaType → a new ErrWrongTargetKind.
       - dest == 0 after defaulting → a new ErrNeedsTarget ('this download isn't tied to a library <kind> — choose one').
       - An empty targetKind means 'same as the review', so an open old tab keeps working.
       - handleImportReview reads target_kind and maps both new errors to 422, next to the existing 410 and 422 cases.
    Frontend
    4. api.ts:
       - ImportReview.media_type becomes 'series'|'movie'|'book'|'music'.
       - Add api.reviewTargets(id, q).
       - Change api.importReview to (id, targetId?, targetKind?).
    5. Reviews.tsx:
       - Add a KIND_LABEL map {series:'show', movie:'movie', book:'book', music:'album'} and use it for the button and modal titles.
       - ReassignModal fetches reviewTargets with a 250ms-debounced q, shows 'Title (year)' with the author or artist subtitle, and sends target_kind.
       - Hide 'Import anyway' whenever expected_id === 0, for every kind.
       - Render 'Grabbed for: not tied to a title' instead of a blank.
       - One searchable album list with the artist as subtitle replaces music.t10's artist→album two-step picker; server-side search makes the second step unnecessary.
       - Keep the existing modal styling.
  - **Files:** `internal/automation/reviews.go`, `internal/automation/reviews_test.go`, `internal/httpapi/reviews.go`, `internal/httpapi/server.go`, `internal/music/repo.go`, `internal/music/service.go`, `web/src/lib/api.ts`, `web/src/pages/Reviews.tsx`
  - **Acceptance:**
    - A book review's reassign modal is titled 'Import into a different book…' and lists library books with their authors. A music review lists albums with their artists. Movies are never offered for either.
    - POSTing target_kind=movie with a movie id against a book review returns 422, and nothing is imported.
    - 'Import anyway' is hidden on every review with expected_id 0. A direct POST with no target returns 422 with a readable message, not 500.
    - Reassigning an unmatched audiobook places its files under the chosen book and removes the review from the list.
    - With Music turned off, a music review's picker says 'Turn on Music to import this'.
  - **Tests:** Go (new internal/automation/reviews_test.go): ImportReview on a 'book' review with targetKind 'movie' returns ErrWrongTargetKind and writes no book_events row.; Go: ImportReview on a music review with ExpectedID 0 and targetID 0 returns ErrNeedsTarget.; Go: ReviewTargets for a music review returns seeded album rows with the artist as subtitle and never movies. q filters on album or artist.; Go httpapi: handleImportReview maps ErrNeedsTarget and ErrWrongTargetKind to 422 and accepts an empty target_kind.; UI check: labels and lists on a book review and a music review. Import anyway is hidden on the unmatched one. The movie and series reassign flows are unchanged.
  - **Depends on:** MUS — module on/off toggle (music.t1), optional: until it lands, c.music == nil is the 'off' signal
  - **Risk:** Low. The kind check could reject a request from a stale browser tab, which is why an empty kind is treated as the review's own kind. The modal is shared by all four kinds, so re-check the series and movie reassign flows.
  - **Resolves:** ops-1, music-11
<a id="acq-02"></a>
- [x] **ACQ-02 · An indexer outage is never recorded as a search miss** — `P0` · `S` · Phase 0
  - **Problem:** When every indexer fails, Search still returns err=nil with no releases.
- Movies: searchAndGrab returns (0, true, nil) (coordinator.go:554-556), and SearchMissing records a miss (:339-340).
- Series: the same pattern (series.go:171-177); searchBackoff then climbs to 12h.
- Books are worse: grabBookEdition swallows the error (books.go:175-178) and the sweep records a miss (books.go:77-84). After bookSearchAttempts=2 misses a book is never searched automatically again.
A book requested during a Prowlarr, FlareSolverr or TorrentLeech outage can therefore drop out of automatic search permanently.
  - **Approach:** 1. New internal/indexer/errors.go:
       - `type AllFailedError struct{ Errors map[string]string }`. Its Error() reads like 'all 3 indexers failed: TorrentLeech: login failed; …', using the already-sanitized messages.
       - `var ErrNoIndexers`.
    2. Service.Search (indexer/service.go:359-450):
       - When at least one indexer was eligible and every eligible one errored, return (result, &AllFailedError{...}) with result.Errors still filled. An indexer skipped by INT's per-indexer backoff, once that lands, counts as failed.
       - When no enabled indexer serves q.MediaType, return ErrNoIndexers.
       - fetchRecent behaves the same way, and an all-failed RSS pull is never cached (service.go:258-271).
    3. Movies: confirm searchAndGrab propagates the error (coordinator.go:551-553), so SearchMissing takes its err branch and records no miss. Check that upgradeMovie and RegrabMovie (coordinator.go:776-990) don't advance any upgrade bookkeeping on this error.
    4. Series:
       - Confirm searchSeriesReleases (series.go:684-689) returns the broad-query error, so searchSeriesOnce errors and the sweep skips RecordSearchMiss.
       - Audit the per-season, alias and targeted loops (series.go:264, 701, 768): they keep 'continue on error' for partial failures, but must return AllFailedError when every query failed instead of turning it into (0, nil).
    5. Books:
       - grabBookEdition (books.go:175) and grabAudioVersion (books_versions.go:93) return (bool, error), propagating AllFailedError and ErrNoIndexers from searchBook.
       - searchBookOnce returns that error, and the sweep (books.go:72-84) logs it without calling RecordSearchMiss.
    6. Music: grabAlbum logs once per sweep and never treats an outage as album progress. MUS owns the music backoff and must honour AllFailedError.
    7. Extract the movie and series sweep outcome switch (coordinator.go:331-341, series.go:171-177) into `sweepOutcome(err error, grabbed int) (resetMisses, recordMiss bool)`, so it can be table-tested.
    8. Each sweep logs one Warn when it hit AllFailedError ('search sweep: every indexer failed; not counting misses'), not one line per title.
    9. Interactive endpoints return the AllFailedError text (502) instead of an empty list. [ACQ-14](#acq-14) turns this into a banner.
  - **Files:** `internal/indexer/errors.go`, `internal/indexer/service.go`, `internal/indexer/service_test.go`, `internal/automation/coordinator.go`, `internal/automation/series.go`, `internal/automation/books.go`, `internal/automation/books_versions.go`, `internal/automation/music.go`, `internal/automation/sweepoutcome_test.go`
  - **Acceptance:**
    - With every indexer unreachable, the missing-movies and missing-series sweeps leave each title's search_misses and last_search_at unchanged, and log one warning per sweep.
    - A wanted book whose two automatic searches both happen during an outage is still searched automatically once the indexers are back.
    - With no indexer serving books, the book sweep records no misses.
    - If one indexer fails and another answers with nothing, the miss is still recorded. Only a search where every indexer failed is exempt.
    - Opening Search on a movie while all indexers are down shows the failure reasons instead of 'No releases found'.
  - **Tests:** Go: Service.Search against two failing httptest servers returns an *AllFailedError (errors.As) naming both. One failing server plus one empty server returns a nil error.; Go: Search with no indexer scoped to 'book' returns ErrNoIndexers.; Go: fetchRecent with all indexers failing is not cached. The next call queries again.; Go: book sweep with a store-backed coordinator whose searchBook yields AllFailedError: books.SearchState misses stay 0, and searchBookOnce returns the error.; Go: sweepOutcome table (err → no miss and no reset; 0 grabbed → miss; n>0 → reset).
  - **Depends on:** INT — per-indexer health/backoff (integrations.t1), optional: an indexer skipped by backoff then counts as failed
  - **Risk:** About 19 indexers.Search/Recent callers relied on 'nil error, empty result'. Grep each one and keep 'continue on error' where that was intended (the series per-season and alias loops), so a partial outage doesn't abort a whole sweep. Until ACQ-14 lands, interactive modals show a 502 message on a total outage, which is more truthful than today.
  - **Resolves:** integrations-1
<a id="acq-03"></a>
- [x] **ACQ-03 · Disk guard: a manual Resume can't defeat it, and Downloads shows what it is holding** — `P0` · `S` · Phase 0
  - **Problem:** pauseActive (internal/download/diskguard.go:155-190) skips any hash in its held set, even when that torrent is downloading again. Resume and 'Resume all' (httpapi/downloads.go:22-29; Downloads.tsx sends hash 'all') go straight to qBittorrent. A torrent the guard paused and the user resumed is never paused again, so the cache pool fills. The Dashboard keeps saying 'N torrents will resume automatically' (health_system.go:56-62), and the Downloads page can't tell a guard pause from a manual one.
  - **Approach:** 1. diskguard.go pauseActive: while UsedPct ≥ pause, also re-pause held hashes whose live State is 'downloading', and log a Warn with the count re-paused. Read with svc.QueueComplete and prune held hashes that are absent from a complete queue (whole=true only), so Holding stays truthful. Compare hashes in lowercase.
    2. Export `Held(ctx) map[string]bool` (lowercased) and `Engaged(ctx) bool` (enabled, measurable, UsedPct > ResumePct and something held). [ACQ-06](#acq-06) and [ACQ-28](#acq-28) reuse Held.
    3. httpapi/downloads.go handleResumeDownload:
       - Hash 'all' is no longer forwarded. Read the queue, resume each item in State 'paused' that the guard doesn't hold, and return {resumed:n, held_by_guard:m}.
       - A single held hash while the guard is engaged returns 409 with a plain message, e.g. 'Held by the disk guard: /downloads is 87% full (pauses at 85%, resumes below 80%). It will resume on its own, or turn the guard off in Settings → Downloads.'
    4. health_system.go: base the warning count on held hashes still present in the queue.
    5. activity.go: add `held_by_guard` per item and a top-level `disk_guard {holding, used_pct, pause_pct, resume_pct}`.
    6. Downloads.tsx:
       - Held items get the chip 'Paused · disk 87% full' in the avoid tone, and their Resume button is disabled with a tooltip.
       - The header button reads 'Resume all (N held by disk guard)' when N>0.
       - The toast reports the {resumed, held} counts.
       - Keep the current palette and type scale.
  - **Files:** `internal/download/diskguard.go`, `internal/download/diskguard_test.go`, `internal/httpapi/downloads.go`, `internal/httpapi/downloads_test.go`, `internal/httpapi/health_system.go`, `internal/httpapi/activity.go`, `web/src/pages/Downloads.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - During a hold, 'Resume all' leaves guard-held torrents paused. Anything resumed outside Arrmada is re-paused within one guard tick (1 min).
    - Resuming a held torrent from its card returns the explanatory 409, and the UI shows that message.
    - Guard-held torrents show the disk chip on Downloads. Manually paused torrents show a plain 'Paused'.
    - The Dashboard's 'N torrents will resume automatically' count matches the torrents actually held.
  - **Tests:** Go diskguard_test.go: a held hash reported as 'downloading' with UsedPct ≥ pause makes Check call Pause on it again (a fake downloader records calls).; Go diskguard_test.go: a held hash absent from a complete queue is pruned. With a partial queue it is kept.; Go httpapi: POST /queue/all/resume with a fake queue of [paused and held, paused manually] resumes only the manual one and reports held_by_guard=1.; Go httpapi: POST /queue/{heldHash}/resume returns 409 while the guard is engaged.; UI check on a test instance with lowered thresholds: the chip and the 'Resume all (N held…)' label appear.
  - **Risk:** Re-pausing something the owner deliberately resumed is the intended behaviour, so the 409 text must say how to override it: free some space, or turn the guard off. Run the download tests with -race in Docker (the guard holds a mutex).
  - **Resolves:** ops-2

#### Milestone: M2 — Dead downloads are seen and replaced

_Downloads labels stalled, metadata-less and queued torrents honestly and offers Reannounce and Recheck. On a default install, a torrent with no progress for 6h is replaced by another release, but never removed when nothing else exists, and no more than 3 per check. One dead pack no longer freezes a show's other seasons._

<a id="acq-04"></a>
- [x] **ACQ-04 · Keep qBittorrent's raw state, swarm counts and last activity; add Item.Phase()** — `P0` · `S` · Phase 1
  - **Problem:** normalizeState (internal/download/qbittorrent.go:511-526) folds metaDL, stalledDL, queuedDL, allocating and checkingDL into 'downloading'. parseTorrentsInfo (:480-508) also throws away the raw state, seed and peer counts, and last activity. Nothing downstream can tell a dead torrent from a live one. Stall fail-over (ACQ-06) can't hold its clock for torrents qBittorrent itself is queueing. checkingDL counts as downloading, so a recheck burns the stall window. The Dashboard's stalledDL and 'UP' branches (dashboard.go:125-133) are dead code. This is the backend half of ops.t4; the UI is ACQ-07.
  - **Approach:** 1. qbittorrent.go: add num_seeds, num_leechs, num_complete, num_incomplete, last_activity, added_on and availability to qbitTorrent. parseTorrentsInfo fills new Item fields RawState, Seeds, Peers, SwarmSeeds, SwarmPeers, LastActivity (unix s), AddedOn and Availability.
    2. client.go: add `func (i Item) Phase() string`:
       - metadata: metaDL, forcedMetaDL
       - queued: queuedDL, queuedUP
       - stalled: stalledDL
       - checking: checkingDL, checkingUP, checkingResumeData
       - moving
       - allocating
       - downloading: downloading, forcedDL
       - seeding: uploading, stalledUP, forcedUP
       - paused: pausedDL/UP, stoppedDL/UP
       - error: error, missingFiles
       Unknown raw states fall back to State. State keeps its current meaning for every existing caller.
    3. normalizeState: move 'checkingDL' from downloading to checking, so stalledInQueue (coordinator.go:1164-1190) holds the stall clock during a recheck, as its comment intends.
    4. httpapi/activity.go: add raw_state, phase, seeds, peers, swarm_seeds, last_activity and added_on per item, plus totals.stalled.
    5. httpapi/dashboard.go: count by Phase: downloading, queued and metadata → Downloading; stalled → a new Queue.Stalled; error → Errored; paused; seeding. Delete the dead `it.State == "stalledDL"` and Contains("UP") branches.
    6. api.ts: types only.
  - **Files:** `internal/download/qbittorrent.go`, `internal/download/client.go`, `internal/download/qbittorrent_test.go`, `internal/download/item_test.go`, `internal/httpapi/activity.go`, `internal/httpapi/dashboard.go`, `internal/httpapi/dashboard_test.go`, `internal/automation/stallpause_test.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - The downloads feed exposes raw_state, phase, seeds, peers and last_activity for every torrent.
    - A rechecking torrent (checkingDL) holds its stall clock and is never failed over while checking.
    - The Dashboard queue summary reports stalled torrents separately from downloading ones.
  - **Tests:** Go qbittorrent_test.go: parseTorrentsInfo keeps the raw state, num_seeds and last_activity from a recorded payload.; Go item_test.go: table test of Phase() over every qBittorrent state string, including v5's stoppedDL/stoppedUP and forcedMetaDL.; Go stallpause_test.go: a checkingDL item past its window is not stalled.; Go dashboard_test.go: a stalledDL item counts as stalled, not downloading.
  - **Risk:** Moving checkingDL means the disk guard (which pauses only State 'downloading') leaves rechecking torrents alone. That's acceptable, since a recheck writes nothing new. Run the full download and automation suites with -race in Docker.
  - **Resolves:** ops-3
<a id="acq-05"></a>
- [x] **ACQ-05 · Stall fail-over replaces before it removes: never delete the only copy, cap per tick, say what happened** — `P0` · `M` · Phase 1
  - **Problem:** When a grab passes its stall window, four separate copies of the fail-over each blocklist the release, Remove(hash, true), and only then search for another:
- DetectStalled for movies (coordinator.go:1193-1263)
- detectStalledSeries (series_reliability.go:19-39)
- detectStalledBook (books.go:~1784-1806)
- detectStalledMusic (music.go:472-505)
Rare content with one intermittent seeder loses its only release even when no alternate exists. The copies have drifted: the series stall writes no event, music writes none, and none publishes a bus event, so OBS can't alert. Nothing limits how many fail-overs one tick performs, which matters once ACQ-06 switches the timeout on for every existing grab.
  - **Approach:** 1. New internal/automation/stallfailover.go with `func (c *Coordinator) failOver(ctx, g grab, item download.Item, found bool, minutes int) (handled bool)`, shared by all four detectors and replacing their inline blocklist/remove/search code.
    2. Scope of the replacement search:
       - movie → version g.VersionID
       - series → the episodes parse(g.Title) covers, intersected with wantedEpisodes: an episode, a season pack's season, or all for a complete pack
       - book → the edition (ebook/audiobook from bookEditionLanded's kind detection, or g.VersionID)
       - music → the album
    3. Not found (gone from a complete queue): nothing to protect, so behave as today: blocklist, set 'failed', search. Event: '<release> disappeared from the download client — searching for another'.
    4. Found but stalled or in error: search first, ignoring the sweep backoff, with exclude = {normTitle(g.Title)}:
       - movies: a new searchAndGrabExcluding; candidatesFrom (coordinator.go:642) gains an exclude set
       - series: grabSeriesLimited(ctx, s, releases, only=scope) with an exclude set merged into its blocked map
       - books: grabBookEdition with exclude
       - music: grabAlbum gains an exclude set and returns its grab count
       If grabbed>0: addBlock*(… 'stalled N min — replaced by <new>'), Remove(hash, true), setGrabStatus 'failed', and the kind's event 'Stalled for 6h — replaced by <new release>'.
       If grabbed==0, or the search returned AllFailedError ([ACQ-02](#acq-02)):
       - Leave the torrent and write nothing to the blocklist.
       - Restart the window with holdStallClock(g.ID, item.Progress).
       - Write one event per window: 'Stalled for 6h — no other release found, still waiting on <release>' (or '… indexers unavailable'), throttled via an in-memory stillWaitingAt[g.ID].
    5. Cap: at most 3 handled fail-overs per DetectStalled call, counted across kinds. 'Still waiting' outcomes count too, because each costs a search. Log 'deferring N stalled downloads to the next check'.
    6. Publish bus event `download.stalled` {kind, id, title, release, minutes, replaced, replacement}.
    7. Series and music now write events: series.AddEvent 'failed'; music.AddEvent(artistID, 'failed').
    8. Works with today's opt-in profiles. [ACQ-06](#acq-06) then turns the default on.
  - **Files:** `internal/automation/stallfailover.go`, `internal/automation/stallfailover_test.go`, `internal/automation/coordinator.go`, `internal/automation/series_reliability.go`, `internal/automation/series.go`, `internal/automation/books.go`, `internal/automation/music.go`
  - **Acceptance:**
    - A stalled torrent with an alternate available: the alternate is grabbed first, then the stalled release is blocklisted and removed. The title's history shows 'Stalled for 6h — replaced by <release>'.
    - With no alternate, or every indexer down, the stalled torrent stays in the client and nothing is blocklisted. One 'still waiting' event is written per window, and the next attempt comes one window later.
    - A grab whose torrent vanished from a complete queue still fails over as before.
    - No more than 3 fail-overs happen in one DetectStalled tick; the rest happen on later ticks.
    - Every fail-over publishes download.stalled, and series and music stalls write history events.
  - **Tests:** Go stallfailover_test.go (fake downloader and fake indexer): movie with no progress and an alternate available → the new grab is recorded before Remove(hash, true); a blocklist row is added; the grab becomes 'failed'.; Go: no alternate → no Remove, no blocklist row, the grab stays 'grabbed', and exactly one 'still waiting' event across two ticks inside one window.; Go: a stalled S03 pack → the replacement search is restricted to S03 episodes (the fake records the only set) and excludes the stalled title.; Go: 5 stalled grabs → 3 handled on the first call, 2 on the second.; Go: AllFailedError from the search → treated as no alternate.; Existing stallpause_test.go and inflight_test.go still pass. Run with -race in Docker.
  - **Depends on:** [ACQ-02](#acq-02)
  - **Risk:** Searching before removing means the client briefly holds both torrents. Disk use is bounded by diskOKFor and the disk guard. The in-memory 'still waiting' throttle and stall samples reset on restart (one extra event or window) until ACQ-24 persists the clock. The exclusion must use normTitle, so a re-listed copy of the same release on another indexer is excluded as well.
  - **Resolves:** quality-9, ops-3, ops-9
<a id="acq-06"></a>
- [x] **ACQ-06 · Stall fail-over on by default: 6h global default, profile override, existing grabs covered, queue- and guard-aware clock** — `P0` · `M` · Phase 1
  - **Problem:** stall_minutes defaults to 0 in migration 0010, in the starter profiles and in the Quality.tsx:169 template, and quality.StallMinutes returns 0 for unknown refs (quality/service.go:85-91). Every detector returns early on 0 (coordinator.go:1234, series_reliability.go:20, books.go:~1784, music.go:486). The value is frozen into each grab row at grab time, so dead torrents sit at 0% forever on a default install, and fixing the profile later doesn't help grabs already made. Torrents held by qBittorrent's max-active limit (queuedDL), or paused by the disk guard, would be condemned once a timeout is set.
  - **Approach:** 1. Semantics: profile stall_minutes 0 = use the default, -1 = off, >0 = custom minutes. A new settings key `downloads_stall_minutes`, default 360 (0 = off), appears in GET/PUT /api/v1/settings next to the disk-guard keys (httpapi/settings.go:86-112).
    2. Add Coordinator.SetStallDefault(func(ctx) int) and Coordinator.SetGuardHeld(func(ctx) map[string]bool), wired in cmd/arrmada/main.go from the settings service and DiskGuard.Held ([ACQ-03](#acq-03)).
    3. Add `func (c *Coordinator) stallWindow(ctx, g grab) (time.Duration, bool)`:
       - g.StallMinutes >0 → that value
       - <0 → off
       - 0 → c.quality.StallMinutes(ctx, g.Profile): >0 → that value, <0 → off, 0 or unknown profile → the global default
       Replace the four early returns and their 'stalled after %d min' strings with it.
    4. stalledInQueue holds the clock (holdStallClock) for Phase queued, checking, moving and allocating, for State paused, and for hashes in the guard's held set. Phase metadata does not hold.
    5. Add `Coordinator.StallInfo(ctx) map[string]StallState{IdleMinutes, WindowMinutes, Off}`, keyed by lower(hash), from stallProgress joined to pending grabs. The downloads feed adds `stall {idle_minutes, failover_in_minutes, off}` per in-flight item; [ACQ-07](#acq-07) renders it.
    6. UI:
       - Quality.tsx: the stall field becomes a select 'Use default (6h) / Off / Custom' with an hours input and the hint 'Try another release after this long without progress'. The profile summary (Quality.tsx:585) reads 'stall: default', 'off' or 'Nh'.
       - Server-side validation of -1..10080 in the quality profile create and update handlers.
       - Settings.tsx, Downloads section: 'Give up on a download with no progress after [6] hours (0 = never)'.
    7. This replaces quality.t22's migration (UPDATE quality_profiles SET stall_minutes=360). That migration would fix profiles but not the 0 frozen into existing grab rows; resolving 0 at check time covers both. The commit message calls out that a profile value of 0 changed meaning from 'off' to 'default'.
  - **Files:** `internal/automation/coordinator.go`, `internal/automation/series_reliability.go`, `internal/automation/books.go`, `internal/automation/music.go`, `internal/automation/stallwindow_test.go`, `internal/automation/stallpause_test.go`, `internal/quality/service.go`, `internal/httpapi/settings.go`, `internal/httpapi/quality.go`, `internal/httpapi/activity.go`, `cmd/arrmada/main.go`, `web/src/pages/Quality.tsx`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - On a default install, a grab with no progress for 6h is replaced as ACQ-05 describes. This covers movies, series, books and music, including grabs recorded before the change.
    - Torrents waiting in qBittorrent's queue, paused by the user, rechecking, moving or held by the disk guard are never failed over for that time.
    - A profile set to Off never fails over. Setting the global default to 0 restores the old behaviour.
    - Existing profiles show 'Use default (6h)', and the Downloads setting persists across a reload.
  - **Tests:** Go stallwindow_test.go table: grab 0 + profile 0 + default 360 → 6h; grab -1 → off; grab 120 → 2h; grab 0 + profile -1 → off; default 0 → off; unknown profile → default.; Go stallpause_test.go: a queuedDL item older than the window is not stalled. A guard-held hash is not stalled. A metaDL item past the window is stalled.; Go httpapi: a quality profile save with stall_minutes -2 or 10081 returns 400.; Go: existing stallpause, inflight and stallfailover tests pass, with -race in Docker.; UI check: the profile select and the Downloads setting round-trip.
  - **Depends on:** [ACQ-03](#acq-03), [ACQ-04](#acq-04), [ACQ-05](#acq-05), CFG — the 'Save settings fails on every tab' fix (server_time/server_tz round-trip), so the new setting can be saved
  - **Risk:** This turns on automated removal for every existing profile. It's acceptable only because ACQ-05 removes a torrent solely when it has a replacement, the clock holds while paused, queued, checking or guard-held, and fail-overs are capped at 3 per tick. Grabs whose torrents were deleted by hand long ago fail over after the upgrade: that's expected and capped. A restart resets the in-memory samples (one extra window) until ACQ-24.
  - **Resolves:** ops-3, quality-9
<a id="acq-07"></a>
- [x] **ACQ-07 · Downloads shows Stalled / Fetching metadata / Queued with idle time, and exposes Reannounce and Recheck** — `P1` · `S` · Phase 4
  - **Problem:** A torrent with no peers shows a blue 'downloading' chip with '—' speed and 'ETA ∞'. Recheck and reannounce are allowed server-side (downloads.go:11) and typed in api.ts, but no UI uses them. The Dashboard Downloading tile only warns on errors and never shows stalled torrents. This is the UI half of ops.t4, plus ops.t5's stall line on the card.
  - **Approach:** 1. Downloads.tsx DownloadCard:
       - The chip follows the phase from [ACQ-04](#acq-04): 'Fetching metadata', 'Queued', 'Stalled · 0 seeds · no data for 3h' (from seeds and last_activity), 'Checking', 'Moving', 'Error'. Stalled and error use the avoid tone; plain 'Downloading' only for phase downloading.
       - A second line from [ACQ-06](#acq-06)'s `stall`: 'No progress for 3h · trying another release in 3h', or 'Auto-retry off'.
    2. Pause/Resume, Block and Remove stay visible. Reannounce, Recheck and priority ↑/↓ move into a '⋯' menu that posts /queue/{hash}/action. Use a small local menu component (the FE kit menu is optional).
    3. Dashboard.tsx Downloading tile: the subtitle shows '2 stalled · 1 error' when present, and warn is set on either.
    4. Keep the dark warm palette, terracotta accent and current type scale. No layout change beyond the chip and the menu.
  - **Files:** `web/src/pages/Downloads.tsx`, `web/src/pages/Dashboard.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A torrent with no peers shows 'Stalled' with its seed count and how long it has been idle, not 'downloading'.
    - A magnet waiting for metadata shows 'Fetching metadata'. A torrent queued by qBittorrent's limits shows 'Queued'.
    - Reannounce and Recheck are reachable from every in-flight card, and a request succeeds against a test client.
    - The Dashboard Downloading tile shows stalled and errored counts and turns amber for either.
  - **Tests:** UI check: a dead torrent on a test client renders the Stalled chip and the idle/failover line. The ⋯ menu's Reannounce request returns 200.; UI check: Dashboard tile with one stalled torrent.; Type-check (tsc) passes.
  - **Depends on:** [ACQ-04](#acq-04), [ACQ-06](#acq-06), FE — component kit overflow menu (optional)
  - **Risk:** Low. The card already crowds on phones; the ⋯ menu reduces that, and ACQ-29 finishes the mobile layout.
  - **Resolves:** ops-3
<a id="acq-08"></a>
- [x] **ACQ-08 · A dead or slow TV torrent only blocks its own seasons, not every search for the show** — `P1` · `M` · Phase 4
  - **Problem:** seriesInFlight (series_reliability.go:263-288) returns a name for any incomplete arrmada-tv torrent whose parsed title equals the show's, whatever its state or scope. The missing sweep (series.go:151-155), RSSSyncSeries (series_reliability.go:66) and UpgradeSeries (:113) then skip the whole show. One stalled S03 pack blocks S04 episodes airing weeks later, and only an Info log says so. It also only compares the series title, so alias- and romaji-named packs aren't recognised as in flight.
  - **Approach:** 1. series_reliability.go: add `seriesInFlightScope(queue []download.Item, s series.Series) (seasons map[int]bool, whole bool, names []string)`:
       - Only incomplete torrents in category seriesCategory matching via seriesTitleMatches count, so aliases and romaji are included.
       - Parse each torrent's season(s): an episode or season pack marks its season. A multi-season pack, a complete-series pack, or an anime absolute-numbered pack sets whole=true.
       - Ignore items whose Phase or State is error: stall fail-over handles those.
    2. Missing sweep (series.go:151) and RSSSyncSeries (series_reliability.go:66):
       - Skip the show only when whole=true.
       - Otherwise compute `only` = wantedEpisodes minus in-flight seasons. Skip if it's empty; otherwise pass it to grabSeriesLimited.
       - Give searchSeriesOnce an excludeSeasons parameter (searchSeriesOnceScoped), so the per-season queries skip in-flight seasons and save indexer calls.
       - Log once per show per sweep: 'searching S04 only — S03 pack still downloading (<release>)'.
    3. UpgradeSeries keeps the show-level skip (no stacking of upgrades).
    4. Keep seriesInFlight/seriesDownloading as thin wrappers for existing callers.
    5. Export `SeriesInFlight(queue, s) (seasons, whole, names)` for the Wanted view ([ACQ-18](#acq-18)).
    6. [ACQ-25](#acq-25) later switches the source from queue parsing to the acquisition record's scope.
  - **Files:** `internal/automation/series_reliability.go`, `internal/automation/series.go`, `internal/automation/inflight_test.go`
  - **Acceptance:**
    - With an incomplete S03 pack in the client, a newly aired S04E01 is still searched and grabbed by the missing sweep and by RSS.
    - A complete-series or multi-season pack in flight still stops the show being searched (no stacking).
    - An errored torrent never blocks its show's searches.
    - A pack named under a series alias counts as in flight for that series.
  - **Tests:** Go inflight_test.go: stalled S03 pack plus aired, missing S04E01 → the sweep's only set is exactly {S04E01}.; Go: complete-series pack in flight → whole=true, and the show is skipped.; Go: anime show with an absolute-numbered pack in flight → whole=true.; Go: a torrent in error state → not in scope.; Go: an alias-named S02 pack marks season 2 in flight.
  - **Depends on:** [ACQ-04](#acq-04), SER — series grab planner / GrabForScope (series.t1): both reshape grabSeriesLimited and searchSeriesOnce; land in one order and rebase
  - **Risk:** Duplicate grabs inside the same season are still prevented by pendingSeriesGrabTitles. Matching through aliases widens what counts as in flight, which errs toward not stacking.
  - **Resolves:** ops-3, series-9

#### Milestone: M3 — Review, Block and Blocklist do what they say

_Resolving a review closes out its download, which then leaves the client at its seed goal, and requesters stop seeing 'Importing'. Block works for every media type and names what it blocked. Every blocklist row can be seen and undone. Review shows each item's reason with actions that fit it, its files, its age and a title link. Mis-numbered packs can be mapped to episodes by hand._

<a id="acq-09"></a>
- [x] **ACQ-09 · Grab lifecycle: one status vocabulary; resolving a review closes out its grab; held and dismissed downloads are handled** — `P1` · `M` · Phase 4
  - **Problem:** For series, the only flip to 'imported' is the automatic sweep (series.go:1070). ImportReview's series branch (reviews.go:337-352) and DismissReview (:293-300) never touch the grab row. ManageSeeding only considers status='imported' (store.go:327-344; coordinator.go:1267), so those torrents seed forever. The Seeding tab shows a goal bar from liveGrabs that fills but never removes anything, and requesters can see Importing indefinitely (progress.go:76-136). A reassigned or dismissed movie review flips only if the original version gains a file. Status literals are hand-typed in about 12 queries across store.go, books.go, books_packs.go, music.go and requests/progress.go, so a new status (needed for held and dismissed downloads) would easily be missed. Seed removal writes an event only for movies (coordinator.go:1322-1324).
  - **Approach:** 1. store.go: define status constants (grabbed, held, imported, dismissed, failed, removed, seeded) and set helpers (stallWatch, inFlight, seedCleanup, live) that return SQL IN-fragments. Route every query through them:
       - pendingGrabs (store.go:247): stallWatch
       - pendingGrabTitles (:273), pendingSeriesGrabTitles (:305), pendingBookGrabTitles (books.go:1621), music.go:428, books_packs.go:49: 'grabbed within 1 day' OR 'held'
       - liveGrabs (:356): live
       - importedGrabs → seedCleanupGrabs (status returned)
       - markGrabImportedForMovie (:394), setSeriesGrabStatus (:445), markBookGrabImported (books.go:1682): flip from grabbed OR held
       - requests/progress.go:160 activeGrabs: grabbed, held
       - check fileinfo.go:182, reviews.go:127 and books_versions.go:160
    2. Add `setGrabStatusByHash(ctx, hash, name, mediaType, to string)`. It updates the latest grabbed/held row with lower(info_hash)=lower(hash), falling back to normRelease(name) among hashless rows, and generalises setSeriesGrabStatus.
    3. Transitions:
       - addReview → 'held'
       - ImportReview success → 'imported' for every kind, including a movie reassigned elsewhere
       - DismissReview → 'dismissed'
       - RejectReview → 'failed'
       - ManualImportSeries: recordHashForContentPath (series_interactive.go:541) returns the matched item and flips its grab to 'imported'
       - handleDeleteDownload → 'removed', plus an event on the linked item. Coordinate with SAFE, which owns the confirmation and the /queue/all guard.
    4. ManageSeeding:
       - Iterate seedCleanupGrabs.
       - For 'dismissed', call downloads.Remove(hash, false), so data Arrmada didn't import is never deleted, then set 'seeded'.
       - Write a 'seeded' event for series (series.AddEvent), books (books.AddEvent) and music (music.AddEvent(artistID)), not just movies.
    5. Review resolution writes events on the expected item ('imported from review', 'review dismissed', 'rejected in review') and sets the new import_reviews.resolution and resolved_at columns.
    6. New migration (next free number ≥0090):
       - `ALTER TABLE import_reviews ADD COLUMN resolution TEXT NOT NULL DEFAULT ''`
       - `ALTER TABLE import_reviews ADD COLUMN resolved_at TIMESTAMP`
       - `UPDATE grabs SET status='held' WHERE status='grabbed' AND info_hash!='' AND lower(info_hash) IN (SELECT lower(hash) FROM import_reviews WHERE status='pending')`
       - `UPDATE grabs SET status='dismissed' WHERE status='grabbed' AND info_hash!='' AND lower(info_hash) IN (SELECT lower(hash) FROM import_reviews WHERE status='resolved') AND lower(info_hash) IN (SELECT lower(download_hash) FROM imports)`
       'dismissed' is the safe choice for the backfill: the torrent is removed at its goal with its files kept.
    7. Add a guard test (store_status_test.go) that scans the automation and requests packages' .go files and fails on a raw `status = 'grabbed'` or `status IN (` against grabs outside store.go.
  - **Files:** `internal/automation/store.go`, `internal/automation/store_status_test.go`, `internal/automation/reviews.go`, `internal/automation/series_interactive.go`, `internal/automation/coordinator.go`, `internal/automation/books.go`, `internal/automation/books_packs.go`, `internal/automation/music.go`, `internal/requests/progress.go`, `internal/httpapi/downloads.go`, `internal/httpapi/fileinfo.go`, `internal/store/migrations/`
  - **Acceptance:**
    - A series download imported through Review leaves the client once its seed goal is met.
    - A dismissed download is removed from the client at its goal, but its files are never deleted.
    - A release held for review is not grabbed again while the review is pending, and is never seed-cleaned while held.
    - A requester's poster stops showing 'Importing…' once the review is resolved.
    - After the migration, old resolved-review grabs stuck at 'grabbed' are 'dismissed', and grabs with a pending review are 'held'.
    - Seed removal writes a history event for every media type.
  - **Tests:** Go: series ImportReview → grab 'imported'. ManageSeeding with a fake completed torrent past its ratio calls Remove(hash, true) and sets 'seeded' with a series event.; Go: DismissReview → 'dismissed'. ManageSeeding calls Remove(hash, false).; Go: RejectReview → 'failed'. requests.Track no longer yields StageImporting for that request.; Go: addReview → 'held'. pendingGrabTitles contains it after 25h, and seedCleanupGrabs does not.; Go: the migration backfill on a temp DB flips only the intended rows.; Go: store_status_test.go finds no raw status literals outside store.go.
  - **Depends on:** SAFE — download Delete confirmation and /queue/all guard (ops-5): [ACQ-09](#acq-09) adds the 'removed' transition to the same handler
  - **Risk:** The grabs table drives removal decisions. A missed IN-list would either re-grab a held release or delete one at seed time, hence the central sets and the source-scanning test. Dismissed files left in the downloads folder are intentional.
  - **Resolves:** ops-6, ops-5, ops-9
<a id="acq-10"></a>
- [x] **ACQ-10 · Block works for every media type, resolves by info hash, and says what it blocked** — `P1` · `S` · Phase 4
  - **Problem:** BlockRelease (coordinator.go:1015-1041) removes the torrent and its data, then tries movies.MatchRelease first whatever the category. A year-less TV torrent such as 'Fargo.S05E01…' therefore blocklists the Fargo movie and searches it, while the series stays unblocked and re-grabs. It then does a name-only series lookup even though the hash is known. There is no book or music branch: blocking an audiobook deletes it, blocklists nothing, and the next sweep grabs the same release again. The handler runs in the background and always returns 202 (downloads.go:107-118), so the UI can't say what happened.
  - **Approach:** 1. store.go: add `grabForHash(ctx, hash) (grab, bool)`, the latest row of any status.
    2. Rework BlockRelease into `BlockRelease(ctx, hash, name) (BlockResult{Kind, ID, Title}, error)`. Resolve via the grab first:
       - movie → addBlock + event + SearchMovie
       - series → addBlockSeries + event + SearchSeriesNow
       - book → addBlockBook + books.AddEvent('blocklisted') + SearchBookNow
       - music → addBlockMusic + music.AddEvent(artistID, 'blocklisted') + a new `SearchAlbumNow(ctx, albumID)` that wraps grabAlbum
       Only when no grab row exists, fall back by the torrent's category, read from the queue by hash before removal:
       - arrmada-tv → series.MatchByTitle
       - arrmada-books → books.MatchByRelease
       - arrmada-music → albumForRelease
       - otherwise → movies.MatchRelease
       A TV-category torrent is never movie-matched. When nothing resolves, return ErrNothingToBlock and remove nothing. The grab goes to 'failed' via setGrabStatusByHash ([ACQ-09](#acq-09)).
    3. handleBlockDownload:
       - Resolve synchronously (a DB read plus one queue read).
       - ErrNothingToBlock → 422 'Not linked to anything in your library — use Remove instead'.
       - Otherwise return 202 {blocked_for:{kind, id, title}} and run the removal and search in a.bg.
    4. Downloads.tsx: toast 'Blocked for <title> — searching for another release', or the 422 message.
  - **Files:** `internal/automation/coordinator.go`, `internal/automation/store.go`, `internal/automation/music.go`, `internal/automation/blockrelease_test.go`, `internal/httpapi/downloads.go`, `web/src/pages/Downloads.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Blocking an audiobook or album release blocklists it for that book or album, and the next search never picks that release again.
    - Blocking a TV torrent never blocklists or searches a same-named movie.
    - The toast names the title that was blocked. Blocking an unlinked torrent says so and leaves the torrent alone.
  - **Tests:** Go: BlockRelease on a book grab's hash inserts a blocklist row with media_type='book' for that book_id and calls SearchBookNow (hook or fake indexer). blockedSetBook then contains it.; Go: 'Fargo.S05E01.1080p…' in arrmada-tv, with library movie Fargo and series Fargo → a series row, no movie row.; Go: a hash with no grab and no match → ErrNothingToBlock and no Remove call.; Go httpapi: handleBlockDownload returns 422 for ErrNothingToBlock, and 202 with blocked_for otherwise.
  - **Depends on:** [ACQ-09](#acq-09)
  - **Risk:** Behaviour change: Block no longer silently removes unlinked torrents. The UI points to Remove instead.
  - **Resolves:** ops-7
<a id="acq-11"></a>
- [x] **ACQ-11 · Blocklist page across every media type, including global entries, with Unblock** — `P2` · `S` · Phase 4
  - **Problem:** Blocklist endpoints exist only per movie and per series (server.go:289-291, 456-458), and listBlocks filters media_type='movie' (store.go:54-55). Several kinds of row can't be seen or undone:
- 'global' rows written by Reject on unmatched reviews (reviews.go:272) and by the executable detector
- every book row
- every music row
One mis-click on Reject blocks a release name for every title, permanently.
  - **Approach:** 1. automation: add `Coordinator.ListAllBlocks(ctx, BlockFilter{Type, Q, Limit, Offset}) ([]BlockRow, int, error)`. BlockRow = {ID, Type (movie|series|book|music|global), ItemID, ItemTitle, Title, Indexer, Reason, CreatedAt}. ItemTitle comes from LEFT JOINs on movies, series, books and albums, chosen by media_type. Ordered by id DESC. The q filter is a LIKE on title or item title.
    2. New internal/httpapi/blocklist.go with `GET /api/v1/blocklist?type=&q=&limit=&offset=` and `DELETE /api/v1/blocklist/{id}`, both RoleManager, reusing removeBlock. The per-title routes stay.
    3. New web/src/pages/Blocklist.tsx at /blocklist, with a nav entry until [ACQ-26](#acq-26) makes it an Activity tab:
       - type pills including Global, and search
       - rows show the release, reason, date and a source-title link
       - global rows are labelled 'Blocks this release for every title'
       - Unblock with confirmation
       Keep the current visual style.
  - **Files:** `internal/automation/store.go`, `internal/automation/coordinator.go`, `internal/automation/blocklist_test.go`, `internal/httpapi/blocklist.go`, `internal/httpapi/server.go`, `web/src/pages/Blocklist.tsx`, `web/src/App.tsx`, `web/src/lib/nav.ts`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Every blocklist row of every type (global, book, music, movie, series) can be listed, filtered and removed in the app.
    - Unblocking a global entry makes that release grabbable again on the next search.
  - **Tests:** Go: ListAllBlocks returns global and book rows under their type filters, with joined titles. q matches the release title.; Go httpapi: DELETE /api/v1/blocklist/{id} removes the row. A requester gets 403.; UI check: unblock a global entry and it disappears from the list.
  - **Risk:** Low.
  - **Resolves:** ops-12
<a id="acq-12"></a>
- [x] **ACQ-12 · Review 2.0: typed reason codes, actions that fit each reason, context, bulk actions and live refresh** — `P1` · `M` · Phase 4
  - **Problem:** Every held item gets the same four buttons (Reject, Import anyway, Import into different, Dismiss), but the queue mixes five different problems:
- content mismatch
- unmatched, with ExpectedID 0 (series.go:1046-1050)
- unresolved numbering (series.go:1103-1108), where Import anyway just re-runs the same parser and fails with 422
- import stuck after 5 tries (reviews.go:229-247)
- nothing importable (books.go:663, music.go:282)
Reject blocks but never searches (reviews.go:256-287). created_at and content_path are fetched but never shown, and there is no file list or title link. The intro copy (Reviews.tsx:29-33) describes only the mismatch case. The page loads once and never refreshes (Reviews.tsx:16). import.held carries only {name, reason}, so nothing else can act on it.
  - **Approach:** 1. Migration: `ALTER TABLE import_reviews ADD COLUMN reason_code TEXT NOT NULL DEFAULT ''`, backfilled in order:
       - 'import_failed' WHERE reason LIKE 'Import failed %'
       - 'unmatched' WHERE expected_id=0
       - 'numbering' WHERE reason LIKE '%could be matched to an episode%'
       - 'no_media' WHERE reason LIKE '%no ebook or audiobook%' OR reason LIKE '%no audio%'
       - 'mismatch' for everything else
    2. Review gains ReasonCode, set at each addReview call site:
       - mismatch: reviews.go:179, series.go:1023, books.go:605, music.go:241
       - unmatched: series.go:1046, books.go:626, music.go:258
       - numbering: series.go:1103
       - import_failed: reviews.go:243
       - no_media: books.go:663, music.go:282
       Enrich the import.held payload to {id, kind, expected_id, expected_title, reason_code, name}, for OBS alerts and live refresh.
    3. New `GET /api/v1/reviews/{id}/files` → Coordinator.ReviewFiles. It walks only r.ContentPath (no path parameter; symlinks are not followed; entries are checked with filepath.Rel) and returns [{rel_path, size, guess:{season, episodes, absolute}}], capped at 500.
    4. RejectReview(ctx, id, findAnother bool): after blocking, when ExpectedID>0, run the kind's search in a.bg (SearchMovie / SearchSeriesNow / SearchBookNow / SearchAlbumNow from [ACQ-10](#acq-10)). The route accepts {find_another:true}.
    5. Retry import for import_failed:
       - Add library.Manager.RetryNow(hash), which clears the failure backoff.
       - Add Coordinator.RetryReviewImport(ctx, id), which resolves the review with resolution 'retried' so the sweep tries again.
       - Route: POST /api/v1/reviews/{id}/retry.
    6. Add `POST /api/v1/reviews/bulk {ids, action: dismiss|reject}`.
    7. Reviews.tsx:
       - A reason chip on each card.
       - Actions by code:
         - mismatch: Import anyway, Choose different…, Reject & find another, Dismiss
         - unmatched: Choose title…, Reject (warning: 'blocks this release for every title'), Dismiss
         - numbering: Map files… ([ACQ-13](#acq-13)), Choose different show…, Dismiss
         - import_failed: Retry import, Dismiss
         - no_media: Reject & find another, Dismiss
       - Show the held age from created_at, the content path, an expandable file list with sizes, and a link to the expected title (/movies/:id, /series/:id, /books/:id, /music/album/:id).
       - Rewrite the intro copy to cover all five reasons.
       - Refresh live via useLive on topic import.held, with a 30s fallback poll.
       - Checkbox selection with bulk Dismiss and Reject.
       - Keep the current visual style.
  - **Files:** `internal/store/migrations/`, `internal/automation/reviews.go`, `internal/automation/series.go`, `internal/automation/books.go`, `internal/automation/music.go`, `internal/automation/titlemismatch_test.go`, `internal/library/manager.go`, `internal/httpapi/reviews.go`, `internal/httpapi/server.go`, `web/src/pages/Reviews.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Each review card shows a reason chip and only the actions that can succeed for that reason. A numbering review never offers Import anyway, and an unmatched one never shows a blank 'Grabbed for'.
    - Cards show how long ago the item was held, the download path, the files with sizes, and a link to the title.
    - Reject & find another blocklists the release and starts a search for the expected title.
    - Retry import on an import_failed review makes the import sweep attempt the download again.
    - A newly held import appears on the open Review page without a reload. Bulk Dismiss resolves several at once.
  - **Tests:** Go: each addReview path produces the expected reason_code (extend titlemismatch_test.go; add series, book and music cases).; Go: the migration backfill maps sample reason strings to codes on a temp DB.; Go: ReviewFiles never returns entries outside ContentPath (a symlink to /etc is skipped).; Go: RejectReview(findAnother=true) on a movie review calls the search hook once.; Go: RetryReviewImport clears the Manager failure for that hash and resolves the review.; UI check: import_failed shows Retry import; bulk Dismiss works; an import.held event re-renders the list.
  - **Depends on:** [ACQ-01](#acq-01), [ACQ-09](#acq-09), [ACQ-10](#acq-10), OBS — admin alerts and nav badge consume the enriched import.held (optional)
  - **Risk:** The text-pattern backfill can misclassify a few old rows. Those fall back to the mismatch actions, which is today's behaviour.
  - **Resolves:** ops-8, ops-4
<a id="acq-13"></a>
- [x] **ACQ-13 · Map files to episodes for numbering reviews** — `P2` · `M` · Phase 4
  - **Problem:** When a season pack's filenames can't be mapped to episodes (series.go:1103-1108), the only exits are Reject (which throws away a good release), Dismiss, or leaving the app. Import anyway re-runs the same parser with force=true. That only skips the quality gate, so it fails with 422 again.
  - **Approach:** 1. New route `POST /api/v1/reviews/{id}/map` (RoleManager), body {series_id, files:[{rel_path, season, episodes:[n,…]}]}.
    2. Coordinator.ImportReviewMapped(ctx, id, seriesID, mappings):
       - Require media_type series.
       - Resolve each rel_path under r.ContentPath and reject anything where filepath.Rel starts with '..' or that resolves through a symlink outside it.
       - For each file, call c.imp.ImportEpisodeAs(folder, s.Title, s.Year, season, episodes[0], path), then series.ResolveEpisode + SupersedeEpisodeFile for every listed episode (double episodes included).
       - Collect the placed refs and pass them to c.seriesImported (the Convert and Subtitles hooks). Write the series event 'Imported N episodes from review (mapped by hand)'.
       - Then call recordImportedHash, setGrabStatusByHash 'imported' ([ACQ-09](#acq-09)) and resolveReview with resolution 'mapped'.
       - Packs with more than 20 files run in a.bg and return 202, like handleSeriesManualImport.
    3. Reviews.tsx 'Map files…' dialog:
       - A table of files (rel path, size) with Season and Episode(s) inputs prefilled from [ACQ-12](#acq-12)'s ReviewFiles guesses.
       - A target show picker defaulting to the expected show (reuses [ACQ-01](#acq-01)'s targets).
       - Blank rows are skipped, and the button reads 'Import N files'.
  - **Files:** `internal/automation/reviews.go`, `internal/automation/reviewmap_test.go`, `internal/httpapi/reviews.go`, `internal/httpapi/server.go`, `web/src/pages/Reviews.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A mis-numbered pack can be imported from Review by mapping its files. The episodes show as present, the review disappears, and the torrent becomes seed-cleanable.
    - Paths outside the review's download folder are rejected.
  - **Tests:** Go (temp dirs only): mapping two files to S01E01 and S01E02 places both at the EpisodeTargetIn paths and marks the episodes has_file.; Go: a double-episode mapping (episodes [3,4]) marks both episodes.; Go: rel_path '../escape.mkv' returns an error and imports nothing.; UI check: resolve a numbering review end to end on a test library, never the owner's real files.
  - **Depends on:** [ACQ-12](#acq-12), [ACQ-09](#acq-09), SEC — manual-import path restriction helpers (reuse the same root check)
  - **Risk:** Hand mappings bypass the quality gate, which is right for user-chosen content. Overwrites go through SupersedeEpisodeFile, and so through the recycle bin.
  - **Resolves:** ops-8

#### Milestone: M4 — Why isn't it downloading?

_Search modals name the indexers that failed. Every search records what it found and why nothing was taken. Wanted shows, for movies, series, books and albums, the last search, the number of empty tries, the main rejection reason and the next automatic try, with a Search now button. Detail pages and Search now report the outcome, and requesters see when their request was last checked._

<a id="acq-14"></a>
- [x] **ACQ-14 · Name the failed indexers in every search modal** — `P1` · `S` · Phase 5
  - **Problem:** ReleaseList has no errors field (coordinator.go:378-383; api.ts ReleaseList). RankReleasesWith reads only result.Releases, and series_interactive.go:101-104 only logs the errors. Every modal says 'No releases found on your indexers' (ReleaseSearchModal.tsx:194), whether the trackers had nothing or were dead.
  - **Approach:** 1. automation.ReleaseList gains:
       - IndexerIssues []IndexerIssue `json:"indexer_issues,omitempty"`, where IndexerIssue is {indexer, error, skipped bool, retry_at}
       - Searched int: how many indexers were queried
    2. Add a helper issuesFrom(map[string]string) that merges and dedupes errors across the several Search calls each ranker makes:
       - RankReleasesWith (coordinator.go:441)
       - RankSeriesReleasesWith: broad, targeted and alias queries (series_interactive.go:29-110)
       - RankBookReleases: author+title, then title only (books.go:946)
       - the audio-version browse
       - the Quality 'test on a real title'
       [ACQ-15](#acq-15) reuses this helper for search outcomes.
    3. On *AllFailedError ([ACQ-02](#acq-02)), return 200 with empty Releases and the issues filled in, instead of an error.
    4. Frontend:
       - api.ts ReleaseList gains indexer_issues and searched.
       - New web/src/components/IndexerIssues.tsx in the existing palette (--reject-soft/--reject), e.g. '2 of 6 indexers failed: TorrentLeech (login failed), 1337x (FlareSolverr unreachable)', with a link to /indexers.
       - Render it at the top of ReleaseSearchModal (movies and series), BookReleaseModal, AudioVersions' browse and the Quality.tsx test panel.
       - Empty states: 'Couldn't search: every indexer failed' when all failed, otherwise 'No releases found on N indexers'.
  - **Files:** `internal/automation/coordinator.go`, `internal/automation/series_interactive.go`, `internal/automation/books.go`, `internal/automation/books_versions.go`, `internal/automation/indexerissues_test.go`, `web/src/lib/api.ts`, `web/src/components/IndexerIssues.tsx`, `web/src/components/ReleaseSearchModal.tsx`, `web/src/components/BookReleaseModal.tsx`, `web/src/components/AudioVersions.tsx`, `web/src/pages/Quality.tsx`
  - **Acceptance:**
    - With one indexer pointed at a dead URL, Search on a movie shows a banner naming that indexer and its error, above the other indexers' results.
    - With every indexer down, the modal says it couldn't search and lists each failure. It never says 'No releases found'.
    - The series modal's banner covers errors from the broad, per-season and alias queries, one line per indexer.
    - Banner text never contains an apikey or MAM token.
  - **Tests:** Go: RankReleasesWith with one failing and one working httptest indexer returns 1 issue plus the working indexer's releases.; Go: issuesFrom merges one indexer's errors from several searches into one issue.; Go: a failing Torznab URL containing apikey=SECRET produces indexer_issues with no 'SECRET' in the JSON.; UI check: the banner appears in the movie, series and book modals, the audio-version browse and the Quality test.
  - **Depends on:** [ACQ-02](#acq-02)
  - **Risk:** Per-indexer error strings now reach the browser. They pass through sanitizeErr today, but check native searchers' messages for URLs that carry tokens.
  - **Resolves:** integrations-1
<a id="acq-15"></a>
- [x] **ACQ-15 · Record every movie and series search outcome (what was found, why nothing was taken) with stable reject codes** — `P1` · `M` · Phase 5
  - **Problem:** Search, Auto-grab missing and the quick Grab buttons return 202 and run detached (httpapi/series.go:102-116, 456-478; movies handleSearchMovie). Failures, and the 'found releases but grabbed none' breakdown that grabSeriesLimited already computes (series.go:399-430), only reach the log. searchAndGrab logs its counts and throws them away (coordinator.go:563-571). The quality engine's RejectReason (quality.go:308-459) is a human sentence, discarded after ranking. The owner can't tell whether nothing was found, everything was for a different film, everything was blocklisted, or everything was over the bitrate ceiling.
  - **Approach:** 1. New migration (next free number) `search_attempts`, generic so books and music reuse it ([ACQ-16](#acq-16)):
       - Columns: id INTEGER PRIMARY KEY AUTOINCREMENT, media_type TEXT NOT NULL, media_id INTEGER NOT NULL, scope TEXT NOT NULL DEFAULT '' ('', 'S03', 'S03E04', 'v2'), trigger TEXT NOT NULL (sweep|rss|manual|request|add|upgrade|stall|replace), started_at INTEGER NOT NULL (unix ms), duration_ms INTEGER, returned, wrong_title, blocklisted, pending, out_of_scope, rejected, eligible, grabbed (INTEGER NOT NULL DEFAULT 0 each), reasons_json TEXT NOT NULL DEFAULT '{}' (code → count), top_reason TEXT NOT NULL DEFAULT '', example TEXT NOT NULL DEFAULT '', grabbed_titles TEXT NOT NULL DEFAULT '[]', indexer_errors TEXT NOT NULL DEFAULT '{}' (sanitized), outcome TEXT NOT NULL (grabbed|nothing_found|none_suitable|indexers_failed|skipped_in_flight|error), error TEXT NOT NULL DEFAULT ''.
       - `CREATE INDEX idx_search_attempts_item ON search_attempts(media_type, media_id, id DESC)`.
       - The insert prunes to the newest 20 per (media_type, media_id) in the same transaction.
    2. quality.Evaluation gains RejectCode, set next to each of the 9 RejectReason sites (quality.go:360-459): resolution, source_floor, prerelease, source_ceiling, bitrate_ceiling, seeders, rejected_term, missing_required, min_format_score. Decision.Rejected already carries the evaluations.
    3. New automation/searchlog.go:
       - `type SearchOutcome struct{Returned, WrongTitle, Blocklisted, Pending, OutOfScope, Rejected, Eligible, Grabbed int; Reasons map[string]int; Example string; GrabbedTitles []string; IndexerErrors map[string]string}` with a Kind() classifier.
       - `recordAttempt(ctx, mediaType, id, scope string, o SearchOutcome, err error)`.
       - `WithSearchTrigger(ctx, trigger)` / `searchTrigger(ctx)`, so the trigger is carried on the context instead of threading a parameter through every function (an unset trigger records 'other').
    4. Movies:
       - searchAndGrab returns (SearchOutcome, error) instead of (int, bool, error); grabMissing reports the grabbed titles.
       - Record in SearchMissing (not when the backoff skipped the movie), RSSSync (only for movies with matching releases), SearchMovie (manual), upgradeMovie (upgrade), RegrabMovie (replace) and the [ACQ-05](#acq-05) fail-over (stall).
    5. Series:
       - grabSeriesLimited returns its counters, and the existing 'found releases but grabbed none' log line is built from the outcome.
       - searchSeriesOnce aggregates the broad, alias, per-season and absolute passes into one outcome.
       - Record in SearchSeriesMissing (not when backoff skipped), RSSSyncSeries (only when releases matched), handleSearchSeries, handleAutoGrabSeries and RegrabEpisode (manual), upgradeSeries (upgrade), and the request-approve and add paths (request/add).
       - An in-flight skip ([ACQ-08](#acq-08)) records outcome skipped_in_flight with the release name in example.
    6. AllFailedError ([ACQ-02](#acq-02)) → outcome indexers_failed with the sanitized per-indexer errors (via [ACQ-14](#acq-14)'s issuesFrom).
    7. Publish bus 'search.finished' {media_type, media_id, attempt_id, outcome, grabbed}.
    8. API: `GET /api/v1/searches?kind=&id=&since=&limit=20` (RoleManager), plus an internal helper latestAttempts(ctx, kind, ids []int64) map[int64]Attempt for the feeds.
  - **Files:** `internal/store/migrations/`, `internal/automation/searchlog.go`, `internal/automation/searchlog_test.go`, `internal/automation/coordinator.go`, `internal/automation/series.go`, `internal/automation/series_reliability.go`, `internal/automation/series_interactive.go`, `internal/quality/quality.go`, `internal/quality/quality_test.go`, `internal/httpapi/searches.go`, `internal/httpapi/server.go`, `internal/requests/service.go`
  - **Acceptance:**
    - After Search on a show where 37 releases were found and none fit, GET /api/v1/searches?kind=series&id=… shows returned=37, eligible=0, counts by reason (e.g. wrong_title 22, bitrate_ceiling 15) and an example.
    - A sweep that grabs a pack records it in grabbed_titles.
    - A search during a total indexer outage records outcome indexers_failed with each indexer's error, and no miss is counted.
    - No item keeps more than 20 attempts.
    - Ranking and grabbing behaviour is unchanged (existing ranking tests pass untouched).
  - **Tests:** Go: TestSearchOutcomeCountsReasons (fake indexer + quality profile with a bitrate ceiling): counts and top_reason are correct.; Go: TestRecordSearchAttemptPrunes: the 21st insert leaves 20 rows.; Go quality: TestRejectCodeSetForEveryRejectReason: every Rejected evaluation has a non-empty code.; Go: movie wrong-title and blocklisted counts; the AllFailedError path writes indexers_failed.; Go: the trigger read from context is recorded ('manual' from the handler path).
  - **Depends on:** [ACQ-02](#acq-02), [ACQ-14](#acq-14), QUAL — quality engine/target-file work: RejectCode must survive QUAL's changes, SER — series grab planner (series.t1): outcome counters move into the planner if it lands first
  - **Risk:** Collecting reasons must not change ranking or grabbing, so it is purely observational and covered by the existing ranking tests. Adds one row per searched item per sweep, which is fine for SQLite with pruning. Signature changes to searchAndGrab ripple into the tests that call it.
  - **Resolves:** series-9, product-10, backend-9
<a id="acq-16"></a>
- [x] **ACQ-16 · Book and music searches record their outcomes too** — `P2` · `S` · Phase 5
  - **Problem:** Wanted books are often family requests, and they stop being searched after 2 misses (books.go:113-134). Their search pipeline is separate (searchBookOnce → grabBookEdition / grabAudioVersion, author+title then title-only). Music searches every incomplete album through grabAlbum. Neither records why nothing was taken, so a book that 'gave up' has no explanation.
  - **Approach:** 1. Books:
       - grabBookEdition (books.go:175) and grabAudioVersion (books_versions.go:93) build a SearchOutcome across both query passes (returned, wrong title/author, blocklisted, pending, profile rejects by RejectCode).
       - searchBookOnce records one attempt per edition, with scope 'ebook', 'audiobook' or 'v<id>'.
       - Triggers: the book sweep (sweep), RSSSyncBooks (rss, only on matches), SearchBookNow (manual), request approval (request), [ACQ-05](#acq-05) (stall).
       - The book sweep's miss rule is unchanged; BOOK owns the ladder.
    2. Music: grabAlbum returns its SearchOutcome and records one attempt per album (scope 'album'). The music sweep sets trigger 'sweep'.
    3. Both: AllFailedError → indexers_failed. Publish 'search.finished'.
  - **Files:** `internal/automation/books.go`, `internal/automation/books_versions.go`, `internal/automation/music.go`, `internal/automation/searchlog_books_test.go`
  - **Acceptance:**
    - After a book search that finds releases for other authors only, its latest attempt shows the wrong-title count and an example.
    - Each album search writes one attempt row with its counts.
    - The book sweep's give-up behaviour is unchanged.
  - **Tests:** Go: a fake indexer returns an ebook for the wrong author and an audiobook over the profile ceiling → two attempts (ebook, audiobook) with the expected codes.; Go: music grabAlbum with only blocklisted releases → an attempt with blocklisted=n and outcome none_suitable.
  - **Depends on:** [ACQ-15](#acq-15), [ACQ-02](#acq-02), BOOK — book search ladder (replaces bookSearchAttempts), MUS — music sweep backoff, which should read these attempts
  - **Risk:** Low. The books search code is large; keep the change to counting and recording only.
  - **Resolves:** product-10
<a id="acq-17"></a>
- [x] **ACQ-17 · Label music transfers correctly and share the download category constants** — `P3` · `S` · Phase 5
  - **Problem:** activity.go knows only 'arrmada-tv' and 'arrmada-books' (lines 20-21, 148-162). Music torrents ('arrmada-music', music.go:21) fall into the default branch, are labelled 'Movie', and are matched against movies for a profile. The Music filter pill always counts 0, and the Movies count is inflated. The category names are duplicated between automation and httpapi.
  - **Approach:** 1. Export the category constants from internal/automation: CategorySeries ('arrmada-tv', coordinator.go:35), CategoryBooks (books.go:28) and CategoryMusic (music.go:21). Keep the unexported aliases. Use them in activity.go instead of the duplicated seriesDownloadCategory and bookDownloadCategory.
    2. activity.go: add `case automation.CategoryMusic:` → media_type 'music'. The profile comes from the album's artist profile when a new exported wrapper Automation.AlbumForRelease (wrapping albumForRelease, music.go:293) matches, otherwise 'n/a'. The movie matcher is never consulted for music.
    3. Downloads.tsx needs no change beyond the counts; TypeChip already renders Music.
  - **Files:** `internal/automation/music.go`, `internal/automation/books.go`, `internal/automation/coordinator.go`, `internal/httpapi/activity.go`, `internal/httpapi/activity_test.go`
  - **Acceptance:**
    - Music torrents show a Music chip, count under the Music pill, and are not counted under Movies.
  - **Tests:** Go httpapi: the feed labels an 'arrmada-music' torrent media_type music and never calls the movie matcher (fake).; UI check: the Music pill count matches the music torrents.
  - **Risk:** Minimal.
  - **Resolves:** ops-10
<a id="acq-18"></a>
- [x] **ACQ-18 · Wanted view: last and next search, empty tries, main reason, honest states, Search now — including books and albums** — `P1` · `M` · Phase 5
  - **Problem:** AcqRow shows a pulsing 'Searching…' or an episode count forever (Downloads.tsx:376-386). The data to explain it exists but never reaches the UI:
- last_search_at and search_misses for movies, series and books (migrations 0055/0061/0074)
- the backoff, which runs from 30m to 12h (series.go:333-345)
- the book give-up after 2 misses (books.go:113-134)
SearchingItem (api.ts) and the feed entries (activity.go:62-112) omit all of it. There is no per-row action. Series frozen behind an in-flight torrent, and movies held for review, still read 'Searching'. Wanted books and albums, often family requests, never appear.
  - **Approach:** 1. automation exports:
       - `NextSearchAt(lastAt string, misses int) time.Time`, from searchBackoff
       - `BookNextSearch(lastAt string, misses int) (time.Time, gaveUp bool)`, from bookSearchWait
       - `SeriesInFlight`, from [ACQ-08](#acq-08)
    2. series.SeriesAcquisition (series/repo.go:771-795) gains LastSearchAt and SearchMisses.
    3. Each Searching row in the feed gains last_search_at, search_misses, next_search_at (null when due), last_outcome {outcome, returned, top_reason, top_reason_count, indexer_errors} from [ACQ-15](#acq-15)'s latestAttempts, and state:
       - searching
       - waiting_download {release, stalled}: series with whole=true, or every wanted season in flight; partial scope adds note 'S03 downloading'
       - held_for_review {review_id}: from pending reviews indexed by (media_type, expected_id)
       - not_released
       - gave_up: books past BookNextSearch
       - indexers_failed: the last attempt's outcome
    4. Add book rows (monitored books missing a wanted edition, labelled 'Audiobook' or 'Ebook', plus Upcoming rows by release date) and album rows (monitored incomplete albums, only when music is enabled). TypeChip shows Book and Music.
    5. New route `POST /api/v1/wanted/{kind}/{id}/search` (RoleManager, kind ∈ movie|series|book|music). It resets the misses (ResetSearchMisses) and runs the kind's search in a.bg with trigger 'manual' (SearchMovie, SearchSeriesNow, SearchBookNow, SearchAlbumNow from [ACQ-10](#acq-10)). Returns 202.
    6. Downloads.tsx AcqRow:
       - A second line, e.g. 'Last searched 3h ago · 34 found, none suitable (mostly: over your bitrate ceiling) · 6 tries · next automatic try in ~9h', or 'Waiting for the first search'.
       - State chips: 'Waiting on S03 pack (stalled)', 'Held for review' (links to Review), 'Not released', 'Stopped searching automatically' (books), 'Indexers failed last try'.
       - A Search now button, with stopPropagation inside the row Link.
       - The pulsing dot only for a search that is due now.
       - Keep the current visual style.
  - **Files:** `internal/automation/series.go`, `internal/automation/books.go`, `internal/automation/series_reliability.go`, `internal/series/repo.go`, `internal/httpapi/activity.go`, `internal/httpapi/wanted.go`, `internal/httpapi/server.go`, `internal/httpapi/activity_test.go`, `web/src/pages/Downloads.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Every Searching row says when it was last searched, how many tries came up empty, the main reason from the last search, and roughly when the next automatic try is.
    - A series blocked by an in-flight torrent shows 'Waiting on <release>', a held movie shows 'Held for review', and neither shows 'Searching'.
    - Wanted books and albums appear in Searching and Upcoming. Books that stopped are labelled and have a Search now button.
    - Search now runs a search immediately and resets the backoff.
  - **Tests:** Go: NextSearchAt matches searchBackoff for misses 0..8 (0 → due now; 3 misses, last search 1h ago → +1h; cap 12h).; Go httpapi: a series with an incomplete whole-show pack in a fake queue → state waiting_download. A movie with a pending mismatch review → held_for_review. A monitored book missing its audiobook → a searching row of kind book.; Go: the Search now handler resets search_misses to 0 and dispatches the right kind.; UI check: the AcqRow text for never-searched, missed, gave-up and held cases; after Search now the row reads 'last searched just now'.
  - **Depends on:** [ACQ-08](#acq-08), [ACQ-10](#acq-10), [ACQ-15](#acq-15), [ACQ-16](#acq-16), [ACQ-17](#acq-17), BOOK — book search ladder: read through BookNextSearch so the view follows it
  - **Risk:** These times are estimates: RSS and manual searches ignore the backoff, so the copy says 'next automatic try'. The feed gets heavier until ACQ-32 caches the Wanted half.
  - **Resolves:** ops-15, product-10, ops-10, ops-3
<a id="acq-19"></a>
- [x] **ACQ-19 · 'Search now' tells you what happened, and detail pages show the last search** — `P2` · `M` · Phase 5
  - **Problem:** handleSearchMovie returns 202 'searching' and the result goes only to the log. SearchMovie also skips the in-flight check that SearchMissing applies (coordinator.go:526-533), so a manual search during a download can grab a second copy, and there is no guard against a double click. SeriesDetail fakes 'Requested' with localStorage (SeriesDetail.tsx:10-48) because nothing server-side records a search. Movie, series and book detail pages never say when or how the last search went.
  - **Approach:** 1. automation:
       - A per-item single-flight for manual searches, a map keyed 'kind:id' under a mutex. A second click while one runs returns ErrSearchRunning → 409 'already searching'.
       - SearchMovie checks in-flight first: inQueue/pendingGrabTitles today, the acquisition lookup after [ACQ-25](#acq-25). If something is in flight it records outcome skipped_in_flight with example '<release>' and makes no indexer query.
       - The series manual search applies [ACQ-08](#acq-08)'s scope check the same way.
    2. Handlers: the movie, series and book search endpoints (and [ACQ-18](#acq-18)'s Wanted endpoint) return 202 {started_at_ms}.
    3. Frontend:
       - New hook useSearchResult(kind, id, startedAt) resolves on the websocket 'search.finished' for that item, falling back to polling GET /api/v1/searches?kind=&id=&since=startedAt every 2s for up to 120s.
       - If BE's job runner has landed, it may use the job result instead.
       - MovieDetail, SeriesDetail and BookDetail show 'Searching…', then an inline result line: 'Grabbed <title>', 'Found 34 releases — 20 for other titles, 14 over your bitrate ceiling', 'Every indexer failed: <names>' or 'Already downloading <release>'.
    4. Detail pages also show the latest attempt at rest, e.g. 'Last search 2h ago — 15 results, 12 rejected: over your bitrate ceiling (12 of 15) · 2 indexers failed: X, Y'. SeriesDetail shows the latest per scope.
    5. Remove SeriesDetail's localStorage 'Requested' mark (GRAB_KEY) in favour of the server's last search.
    6. Keep the current visual style.
  - **Files:** `internal/automation/coordinator.go`, `internal/automation/series.go`, `internal/automation/books.go`, `internal/automation/searchoutcome_test.go`, `internal/httpapi/movies.go`, `internal/httpapi/series.go`, `internal/httpapi/books.go`, `web/src/lib/useSearchResult.ts`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/SeriesDetail.tsx`, `web/src/pages/BookDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - After clicking Search on a movie, the page states the outcome within the search time, with no need to open Logs.
    - Searching a movie that is already downloading says so and makes no indexer query. A double click doesn't start two searches.
    - Movie, series and book detail pages show the last search's time, counts and main reason.
    - SeriesDetail no longer stores anything in localStorage for searches.
  - **Tests:** Go: SearchMovie with a fake indexer returning wrong-title and blocklisted releases → the recorded outcome counts match.; Go: SearchMovie with an in-flight download → outcome skipped_in_flight, and the fake indexer is not called.; Go: two concurrent manual searches for one movie → the second returns ErrSearchRunning (run with -race).; UI check: Search now shows the outcome line on movie, series and book pages; reload shows the last-search line.
  - **Depends on:** [ACQ-15](#acq-15), [ACQ-16](#acq-16), [ACQ-08](#acq-08), BE — job runner (backend.t19), optional: persisted attempts plus search.finished work without it, SEC — role-filtered websocket before search.finished is broadcast (the payload carries only ids and counts)
  - **Risk:** Low. The single-flight map must be released on panic (defer). Polling stops after 120s with 'still searching — check back'.
  - **Resolves:** backend-9, series-9, product-10
<a id="acq-20"></a>
- [x] **ACQ-20 · Requesters see when their request was last checked** — `P2` · `S` · Phase 6
  - **Problem:** A requester's approved request reads 'Looking for a release' with no time, whatever has happened (Discover.tsx:693; progress.go stage 'searching'). A book that stopped being searched automatically reads exactly like one searched an hour ago.
  - **Approach:** 1. requests.Tracking gains LastSearchAt and Misses (and SearchStopped for books). They are read from the linked library item through a small interface `SearchStates(ctx, kind string, ids []int64) map[int64]SearchState`, implemented over movies/series/books SearchState and wired in main.go.
    2. Discover requestStage 'searching' detail:
       - misses==0: 'Looking for a release'
       - misses>0: 'Still looking — nothing suitable yet (checked 2h ago)'
       - a stopped book: 'Couldn't find it yet — the admin has been told'
    3. Requesters never see indexer names, reject reasons or release titles: times only.
    4. The 'admin has been told' copy is shown only once REQ/OBS's stuck-search notification exists. Until then: 'Couldn't find it yet — searching again later'.
  - **Files:** `internal/requests/progress.go`, `internal/requests/service.go`, `cmd/arrmada/main.go`, `web/src/pages/Discover.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A requester's 'Searching' request says when it was last checked.
    - A book past its automatic searches says so, without admin detail.
  - **Tests:** Go: Track exposes misses and last_search_at for a movie request with a seeded search state.; UI check: the Discover card copy for never-searched, missed and stopped cases.
  - **Depends on:** [ACQ-18](#acq-18), REQ — requester Discover/progress work; the stuck-search notification (search.stuck) the copy refers to
  - **Risk:** Low. Keep the copy vague enough that a requester can't infer anything about trackers.
  - **Resolves:** product-10

#### Milestone: M5 — Identity by info hash

_One title normalizer. A single cached client snapshot with health: sweeps pause while qBittorrent is down, and Downloads says so. Each grab row carries its live phase, progress and scope, and the stall clock survives restarts. 'Already downloading', grid progress and the missing-version checks key on the hash, never on parsed names._

<a id="acq-21"></a>
- [x] **ACQ-21 · One title normalizer for every download and queue match** — `P1` · `S` · Phase 4
  - **Problem:** Three normalizers drift:
- automation.titleKey (coordinator.go:1587-1607) folds accents and maps & to 'and', but keeps bracket contents.
- httpapi.normKey (activity.go:251-259) strips brackets but has no & to 'and'.
- series.normKey (series/service.go:1143-1162) does both.
The Downloads feed's in-flight check and downloadFor therefore miss 'Love & Death' downloading as 'Love.and.Death'. Similar mismatches cause duplicate grabs, and the comments document past bugs from exactly this drift.
  - **Approach:** 1. Add parser.TitleKey(s string) string with series.normKey semantics: FoldAccents, lower-case, '&' → ' and ', strip bracketed groups (parser.StripBracketed), keep letters and digits. Its doc comment lists the cases.
    2. Replace automation.titleKey, httpapi.normKey and series.normKey with it. Keep thin unexported aliases only where call sites are many.
    3. Leave automation.normTitle (the blocklist key) and normRelease (release identity) alone: they serve different purposes. requests.normName is release-name keyed too and stays.
    4. This is the interim fix until [ACQ-25](#acq-25) removes name matching from the in-flight checks; the legacy adoption in [ACQ-24](#acq-24) also uses it.
  - **Files:** `internal/parser/titlekey.go`, `internal/parser/titlekey_test.go`, `internal/automation/coordinator.go`, `internal/automation/titlekey_test.go`, `internal/httpapi/activity.go`, `internal/series/service.go`
  - **Acceptance:**
    - A 'Love & Death' movie shows its download progress when the torrent is named Love.and.Death….
    - Accented titles (Pokémon) and bracketed alternates match in all three places.
    - Only one title-normalizer implementation exists.
  - **Tests:** Go: parser.TitleKey table: 'Love & Death' == 'Love.and.Death'; 'Pokémon' == 'Pokemon'; 'Show [2019]' == 'Show'; 'My Hero Academia (Boku no Hero Academia)' == 'My Hero Academia'; punctuation variants.; Go: the existing titlekey, grabmatch and inflight tests still pass.; Go httpapi: a downloadFor test with an &-titled movie.
  - **Risk:** Stripping bracket contents in automation could merge two titles that differ only in a bracketed part. Check the grabmatch tests and add such a case.
  - **Resolves:** backend-15
<a id="acq-22"></a>
- [x] **ACQ-22 · One shared download-queue snapshot with client health; sweeps pause while the client is down** — `P2` · `M` · Phase 5
  - **Problem:** Six callers do `queue, _ := Downloads.Queue(ctx)`: coordinator.go:313 (SearchMissing) and :739 (RSSSync), series_reliability.go:61, activity.go:30, movies.go:23 and requests.go:36. SearchSeriesMissing (series.go:143-146) proceeds with a nil queue on error, which disables its in-flight check. During a qBittorrent or VPN outage the sweeps keep querying indexers. Every 3-10s poll in every open tab, plus the import sweeps, makes its own qBittorrent call.
  - **Approach:** 1. download.Service.Snapshot(ctx) returns Snapshot{Items []Item (each tagged ClientID); Complete bool; Health []ClientHealth{ID, Name, Reachable bool, LastOK, Since time.Time, LastErr string}; At time.Time}.
       - Cached for 2s with single-flight (mutex + in-flight channel, or golang.org/x/sync/singleflight).
       - Per-client LastOK and Since are tracked in memory.
       - Invalidated by Add, Pause, Resume, Remove, Action and SetCategory.
       - Queue and QueueComplete become wrappers, so every existing caller shares it.
       - Item gains ClientID.
    2. Sweeps skip the cycle when the snapshot errors or is incomplete, with the Info log 'download client unreachable — skipping search so nothing is grabbed twice':
       - SearchMissing
       - RSSSync
       - SearchSeriesMissing (replacing its queue=nil fallback)
       - RSSSyncSeries
       - the music sweep
       UpgradeSeries and the book sweeps already skip. DetectStalled keeps its QueueComplete semantics.
    3. health_system.go uses Snapshot health instead of its own Queue call.
    4. Export `Health()` for [ACQ-23](#acq-23).
  - **Files:** `internal/download/service.go`, `internal/download/snapshot.go`, `internal/download/snapshot_test.go`, `internal/download/client.go`, `internal/automation/coordinator.go`, `internal/automation/series.go`, `internal/automation/series_reliability.go`, `internal/automation/music.go`, `internal/httpapi/health_system.go`
  - **Acceptance:**
    - With qBittorrent stopped, the 5-minute movie sweep and the series sweep log the skip and make no indexer queries.
    - With 3 tabs open on Downloads, qBittorrent sees at most one torrents/info call per 2s.
    - A Pause or Resume is reflected on the next poll (the cache is invalidated).
  - **Tests:** Go: two concurrent Snapshot calls → one client List call. A call within 2s is served from the cache. A Pause invalidates it.; Go: a client error → Health.Reachable=false, LastOK preserved, Since set at the first failure and kept across later failures.; Go: SearchMissing and SearchSeriesMissing with a failing snapshot → the fake indexer receives zero searches.; All with -race in Docker.
  - **Risk:** The 2s cache adds up to 2s of staleness to progress bars, which is acceptable. Stall detection must keep QueueComplete's semantics (Complete=false means don't conclude absence). Concurrency bugs here would be hard to reproduce, so use -race.
  - **Resolves:** backend-12
<a id="acq-23"></a>
- [x] **ACQ-23 · Downloads says when there is no download client, or it isn't answering, instead of 'free 0 GB'** — `P2` · `S` · Phase 5
  - **Problem:** handleDownloadsFeed (activity.go:30) ignores Queue errors, and QueueComplete returns (nil, true, nil) when no client is configured. The page shows a green 'Live' dot, 'Nothing downloading', and Pause all and Resume all buttons that look like they work. free_gb is `freeGB, _ := diskspace.FreeGB(...)` (activity.go:206), so an unmeasurable or missing downloads folder is sent as 0 and rendered 'free 0 GB' in red. A dead qBittorrent looks like an empty queue, and every wanted movie looks like it's 'Searching'.
  - **Approach:** Backend (activity.go, movies.go, requests.go):
    1. Read the queue via Snapshot ([ACQ-22](#acq-22)), or QueueComplete until it lands. Add `clients: {configured, enabled, ok, error, since}`:
       - configured = len(Downloads.List)
       - enabled = count of enabled clients
       - ok = false on error or an incomplete snapshot ('a download client didn't answer')
    2. Send free_gb only when diskspace reports ok; otherwise send null. Add disk_path: Config.DownloadsDir.
    3. While the queue is unknown, wanted items get state 'unknown' instead of 'searching'. The movies list and requests progress return client_health, so their cards read 'Status unknown' rather than 'Searching'.
    Frontend (Downloads.tsx, Movies.tsx, Discover.tsx):
    4. clients.configured === 0:
       - Hide the ↓/↑/active/free stats, the Live dot, and Pause/Resume all.
       - The Downloads and Seeding tabs show an empty-state card: 'No download client yet — Arrmada needs qBittorrent to download anything.' with [Add a download client →] linking to /downloadclients.
       - Searching and Upcoming keep working.
    5. clients.ok === false: an amber banner 'qBittorrent unreachable since 14:02 — searches are paused until it's back (<error>)' with a link, and the Live dot becomes 'Client offline'.
    6. The free stat is hidden when null and gets title='Free space on <disk_path>'.
    7. Keep the current visual style.
  - **Files:** `internal/httpapi/activity.go`, `internal/httpapi/activity_test.go`, `internal/httpapi/movies.go`, `internal/httpapi/requests.go`, `web/src/lib/api.ts`, `web/src/pages/Downloads.tsx`, `web/src/pages/Movies.tsx`, `web/src/pages/Discover.tsx`
  - **Acceptance:**
    - On a fresh install with no clients, Downloads shows no 'free 0 GB', no 'Live' and no Pause/Resume all, and does show the empty-state card linking to Download clients.
    - With a client pointed at a wrong host, the banner shows the client error within one poll, and wanted items read 'Status unknown'.
    - With an unmeasurable downloads folder, the free stat is absent rather than 0.
    - With a healthy client, the page looks exactly as it does today.
  - **Tests:** Go TestDownloadsFeedClientState (a download.Service with a fake registry): zero clients → configured 0, ok true; one failing client → ok false with the error; free_gb null for a non-existent DownloadsDir.; UI check: no client, then a broken client, then a working client.
  - **Depends on:** [ACQ-22](#acq-22)
  - **Risk:** Low. Coordinate with ACQ-26 so the banner and empty state move into the Activity Queue panel. The Discover change is requester-facing: copy only, no admin detail.
  - **Resolves:** walk-6, backend-12
<a id="acq-24"></a>
- [x] **ACQ-24 · Acquisition record on grabs: live phase, progress, scope and timestamps from the snapshot; a persisted stall clock** — `P1` · `M` · Phase 5
  - **Problem:** Grab rows have stored info_hash since 0062, but there is no single stored record of where each acquisition is. The pieces live in different places:
- 'already downloading' (inQueue), grid progress (downloadFor), feed buckets and the series in-flight checks re-derive state by parsing torrent names on every request
- missingVersions ignores pending grabs (coordinator.go:589-601)
- stall samples live in an in-memory map (coordinator.go:1105-1160), so a restart resets every stall clock
This is the 'durable acquisition record keyed by info hash' from the audit overhaul, adapted: grabs evolves in place rather than gaining a parallel table.
  - **Approach:** 1. New migration (next free number), adding to grabs:
       - phase TEXT NOT NULL DEFAULT ''
       - progress REAL NOT NULL DEFAULT 0
       - progress_at INTEGER NOT NULL DEFAULT 0 (unix ms of the last forward progress)
       - scope TEXT NOT NULL DEFAULT ''
       - client_id INTEGER NOT NULL DEFAULT 0
       - last_seen_at, completed_at, updated_at INTEGER NOT NULL DEFAULT 0
       - last_error TEXT NOT NULL DEFAULT ''
       - `CREATE INDEX idx_grabs_item_status ON grabs(media_type, movie_id, status)`
       The lifecycle stays in status ([ACQ-09](#acq-09)). There is no second state machine.
    2. New automation/acquisitions.go:
       - `type Acquisition` (grab + the new fields)
       - Active(ctx, mediaType, itemID) []Acquisition (inFlight set, excluding phase 'missing' older than 30 min)
       - ActiveForVersion(ctx, movieID, versionID)
       - ByHash(ctx, hash)
       - ActiveByItem(ctx, mediaType) map[int64][]Acquisition (bulk, for feeds)
    3. Writers:
       - recordGrab, recordSeriesGrab (series.go:1185), recordBookGrab (books.go:1660) and recordMusicGrab set scope at grab time: movie 'v<versionID>'; series from parse 'S03' | 'S03E05' | 'S01-S04' | 'complete' | 'abs'; book 'ebook' | 'audiobook' | 'v<id>'; music 'album'.
       - Add `ReconcileAcquisitions(ctx)` on the scheduler every 30s:
         - Reads the Snapshot ([ACQ-22](#acq-22)) and calls AdoptTorrentHashes (seedadopt.go) for legacy rows.
         - For each live grab, sets phase from Item.Phase(), plus progress, client_id and last_seen_at. Absent from a complete snapshot → phase 'missing'. Progress complete → completed_at.
         - Writes only rows whose phase changed, whose progress moved ≥1%, or whose last_seen_at is older than 5 min, in one transaction.
       - Import failure hooks (HandleMovieImportFailure, and the series/book/music import error paths) set last_error by hash.
    4. Persisted stall clock: noProgressFor and holdStallClock read and write progress_at on the grab row instead of c.stallProgress. The function signatures stay, so [ACQ-05](#acq-05) and [ACQ-06](#acq-06) are untouched. Remove pruneStallSamples. The 'still waiting' throttle from [ACQ-05](#acq-05) moves to an updated_at-based check.
  - **Files:** `internal/store/migrations/`, `internal/automation/acquisitions.go`, `internal/automation/acquisitions_test.go`, `internal/automation/store.go`, `internal/automation/series.go`, `internal/automation/books.go`, `internal/automation/music.go`, `internal/automation/coordinator.go`, `internal/automation/seedadopt.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - Every new grab has a row with info_hash, scope and a phase that moves queued → downloading → complete as the torrent progresses, and 'missing' when it disappears.
    - Legacy hashless grabs present in the client get their hash adopted on the next reconcile.
    - Restarting Arrmada does not reset stall clocks: a torrent idle for 5h before a restart fails over 1h after it, with a 6h window.
  - **Tests:** Go: the reconciler with a fake snapshot drives phase transitions by hash, including a prettified torrent name that differs from the listing title.; Go: the reconciler writes nothing when nothing changed (counted statements).; Go: progress_at persists across a new Coordinator over the same DB; noProgressFor returns true after the window.; Go: the migration applies on a temp DB copy of 0089.; All with -race in Docker.
  - **Depends on:** [ACQ-04](#acq-04), [ACQ-09](#acq-09), [ACQ-21](#acq-21), [ACQ-22](#acq-22)
  - **Risk:** The grabs table is shared by seeding, stall detection and request progress. Every new column is additive, and status stays authoritative. Reconciler writes every 30s must be throttled to changed rows only, to avoid SQLite write contention with the import sweeps.
  - **Resolves:** backend-15
<a id="acq-25"></a>
- [x] **ACQ-25 · Switch 'already downloading', progress and the missing-version checks to the acquisition record; retire name matching** — `P1` · `M` · Phase 5
  - **Problem:** With the record in place, consumers still guess by name:
- inQueue (coordinator.go:1577-1585)
- seriesInFlight / seriesDownloading (series_reliability.go:263-288; ACQ-08's scope version)
- downloadFor and the feed buckets (activity.go:30-50, 220-260)
- missingVersions (coordinator.go:589-601), which ignores in-flight grabs, so only an identical release title is deduped (grabMissing)
- the near-miss seed diagnostic logger (activity.go:262-329, NearestGrabs)
A movie whose torrent name parses to a different title is re-grabbed or shows no progress.
  - **Approach:** 1. Movies:
       - SearchMissing, RSSSync and SearchMovie skip a version when acq.ActiveForVersion is non-empty. missingVersions excludes versions with an active acquisition, so a differently named release of the same version is never grabbed twice.
       - [ACQ-19](#acq-19)'s in-flight check uses this lookup.
    2. Series: seriesInFlightScope reads Active('series', id) scopes instead of parsing the queue. A pack's scope blocks its seasons; an episode scope blocks itself; 'complete' or 'abs' sets whole. The [ACQ-08](#acq-08) tests are kept and re-pointed at the record.
    3. httpapi:
       - downloadFor (movies.go:23, 275) and the Downloads feed join queue items to acquisitions by lower(hash).
       - The movie grid shows progress by hash.
       - Torrents with no acquisition are 'Not managed by Arrmada' by hash only.
       - Delete normKey-based bucketing, logUnmatchedSeeds and the NearestGrabs/SharedPrefixLen diagnostics.
    4. Keep name matching only inside the legacy path (rows with info_hash == ''), behind a clearly named legacyMatchByName with a TODO to delete it once no hashless rows remain. Log at Info when a grab is recorded without a hash, so it's visible.
  - **Files:** `internal/automation/coordinator.go`, `internal/automation/series_reliability.go`, `internal/automation/series.go`, `internal/automation/acquisitions.go`, `internal/automation/inflight_test.go`, `internal/httpapi/activity.go`, `internal/httpapi/movies.go`, `internal/httpapi/server.go`
  - **Acceptance:**
    - A movie whose torrent name parses to a different title is still shown downloading on the grid and is never re-grabbed.
    - A season pack in flight prevents single-episode grabs for that season, and doesn't block other seasons.
    - The Seeding tab no longer shows Arrmada's own torrents as 'Not managed by Arrmada'.
  - **Tests:** Go: SearchMissing with an active acquisition for version 1 → the fake indexer is not queried for that movie.; Go: grabMissing doesn't grab a second, differently named release for a version with an active acquisition.; Go httpapi: the Downloads feed attributes a prettified torrent name via the hash.; Go: a series pack's scope blocks an episode grab in the same season and allows another season.
  - **Depends on:** [ACQ-24](#acq-24), [ACQ-08](#acq-08)
  - **Risk:** A grab recorded without a hash (a client that didn't report one) falls back to the legacy path. It's logged so it's visible. Removing the seed diagnostics loses a debugging aid that's no longer needed once matching is by hash.
  - **Resolves:** backend-15, ops-11

#### Milestone: M6 — One Activity page

_One /activity page with Queue, Needs you, Wanted, History and Blocklist tabs, a single nav entry, and redirects from the old URLs. History is a dated, filterable, linked event log across all four media types. The Queue splits Downloading, Finishing and Seeding using one stage model that admin and requester views share._

<a id="acq-26"></a>
- [ ] **ACQ-26 · Activity page: one /activity with Queue, Needs you, Wanted, History and Blocklist tabs** — `P2` · `M` · Phase 9
  - **Problem:** Downloads, History and Review are three top-level pages for one concept, and they don't link to each other (nav.ts:17-19). /activity just redirects to /downloads (App.tsx:85), yet the Search toast says 'it'll appear in Activity' (Movies.tsx:152). Hidden pages poll on their own: Downloads every 3s, History every 5s. Nowhere answers 'what is Arrmada doing, what is stuck, what needs me'.
  - **Approach:** 1. New web/src/pages/Activity.tsx at /activity/:tab, with tabs queue | needs-you | wanted | history | blocklist:
       - Queue: sub-pills Downloading / Seeding (Finishing arrives with [ACQ-28](#acq-28)).
       - Wanted: sub-pills Searching / Upcoming.
       - Sub-pills live in ?show=, so Back works and links can deep-link.
    2. Refactor without changing behaviour:
       - Downloads.tsx exports QueuePanel and WantedPanel.
       - Reviews.tsx exports ReviewsPanel, which is the Needs you content until [ACQ-30](#acq-30).
       - History.tsx exports HistoryPanel.
       - Blocklist.tsx ([ACQ-11](#acq-11)) exports BlocklistPanel.
       - The old page components become thin wrappers or are removed.
    3. Redirects, keeping query params:
       - /downloads → /activity/queue
       - /history → /activity/history
       - /review → /activity/needs-you
       - /blocklist → /activity/blocklist
       - /activity → /activity/queue
    4. Only the visible panel mounts, and only it polls; hidden panels unmount.
    5. nav.ts: one 'Activity' entry replaces Downloads, History and Review. Its badge is the pending review count, until OBS's attention feed supplies the total.
    6. PageHeader crumb 'Activity / <tab>'. The Movies.tsx:152 toast links to /activity/wanted. Dashboard links (queue tile, warnings) go to the matching tab.
    7. API paths never contain 'activity'. Keep the existing /api/v1/downloads and friends.
    8. Keep the current visual style.
  - **Files:** `web/src/pages/Activity.tsx`, `web/src/pages/Downloads.tsx`, `web/src/pages/Reviews.tsx`, `web/src/pages/History.tsx`, `web/src/pages/Blocklist.tsx`, `web/src/App.tsx`, `web/src/lib/nav.ts`, `web/src/components/Sidebar.tsx`, `web/src/pages/Movies.tsx`, `web/src/pages/Dashboard.tsx`
  - **Acceptance:**
    - The sidebar has one Activity entry with a count badge. /activity shows all five tabs.
    - Each old URL (/downloads, /history, /review, /blocklist) opens the matching tab, keeping its query params.
    - Browser Back moves between tabs, and Search's 'Activity' link opens Wanted.
    - Only one polling loop runs at a time (network log).
  - **Tests:** UI check: each redirect.; UI check: tab switching keeps the URL in sync, and Back works.; UI check: the network log shows one poll loop, and none from hidden panels.; UI check: the page loads with uBlock Origin (default lists) enabled.
  - **Depends on:** [ACQ-11](#acq-11), FE — lazy router (optional), OBS — attention feed for the badge total (optional)
  - **Risk:** Navigation churn for the owner, mitigated by redirects. The comment at the top of internal/httpapi/activity.go notes that ad-block lists block '/activity' URLs. This applies to API requests and is avoided by keeping the API under /api/v1/downloads, but check that the SPA route itself loads with uBlock on, and fall back to /hub/:tab if it doesn't.
  - **Resolves:** product-8, ops-11, ops-4, product-12
<a id="acq-27"></a>
- [ ] **ACQ-27 · History becomes a dated, filterable, linked event log across movies, series, books and music** — `P2` · `M` · Phase 9
  - **Problem:** History.tsx renders only title, target_path and size from the imports table:
- a hard LIMIT 100 (history.go:11; library.Recent)
- imported_at is never shown
- it polls every 5s
- the route is a.protected, so any signed-in user can read it
recordImportedHash writes rows with a blank path for dismissed reviews and for packs that placed nothing (reviews.go:298, 726-733), so those show as imports. Grabs, failures, stall fail-overs, blocklists, upgrades and seed removals are missing. The Dashboard's recentActivity UNION (dashboard.go:279-318) is richer, but covers only movies, series and books, capped at 20.
  - **Approach:** 1. Extract recentActivity's UNION into a shared `eventsFeed(ctx, Filter{Kinds, Events, Q, Before{AtMS, ID}, Limit})`:
       - UNION ALL over movie_events⋈movies(poster_url), series_events⋈series(poster_url), book_events⋈books(cover_url) and artist_events⋈artists(image_url).
       - Each branch applies the kind, event and title-LIKE filters, the keyset `(created_at, id) < cursor` and the LIMIT. The outer query orders by created_at DESC, id DESC.
       - The Dashboard calls it with Limit 20, which adds music.
    2. Add `eventCategory(event string) string`, mapping the free-text event names to a small fixed set (grabbed, imported, upgraded, failed, blocklisted, deleted, renamed, seeded, removed, review) for the filter.
    3. Replace handleHistory with `GET /api/v1/history?kind=&event=&q=&before=<at_ms>:<id>&limit=50`, RoleManager. It returns {events:[{kind, item_id, title, poster_url, event, category, detail, at_ms}], next_before}. The old imports list stays reachable at ?view=imports for one release, then is removed.
    4. New migration: CREATE INDEX on (created_at DESC, id DESC) for movie_events, series_events, book_events and artist_events.
    5. Missing events are written by [ACQ-05](#acq-05) (stall), [ACQ-09](#acq-09) (seed removal, review resolution, removed) and [ACQ-10](#acq-10) (block). Verify they all appear.
    6. History panel (HistoryPanel inside [ACQ-26](#acq-26)):
       - Grouped by day: Today, Yesterday, 'Mon 6 Oct'.
       - Rows: poster thumb, time (absolute in a tooltip), event chip, title linking to /movies/:id, /series/:id, /books/:id or /music/artist/:id, and the detail.
       - Kind pills, event chips and a search box. Filters live in the URL (FE's URL-filter hook if present).
       - 'Load more' via next_before.
       - No interval polling; refetch on window focus and on websocket import/grab topics.
       - Optional 'Files imported' toggle shows the old size/path list with imported_at.
       - Keep the current visual style.
  - **Files:** `internal/httpapi/history.go`, `internal/httpapi/history_test.go`, `internal/httpapi/dashboard.go`, `internal/httpapi/server.go`, `internal/store/migrations/`, `web/src/pages/History.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - History shows a timestamped, filterable log of grabs, imports, upgrades, failures, blocklists, removals and seed removals for all four media types, each row linking to its title.
    - Filtering to Series + Imported shows only series imports. Load more pages back with no duplicates or gaps. The URL reflects the filters.
    - Dismissed reviews and empty-path rows never appear as imports.
    - The page makes no requests while idle, and a requester gets 403 from /api/v1/history.
  - **Tests:** Go: eventsFeed over seeded events in all four tables returns newest first. Kind, event and q filters work, and keyset pages don't overlap.; Go: the Dashboard recentActivity includes an artist_events row.; Go: the migration applies; EXPLAIN shows the created_at index used per branch.; UI check: filters, links, day grouping and Load more; the network panel shows no periodic /history calls.
  - **Depends on:** [ACQ-26](#acq-26), [ACQ-05](#acq-05), [ACQ-09](#acq-09), [ACQ-10](#acq-10), SEC — deny-by-default route gating (the new route is RoleManager from day one), FE — URL-filter hook (frontend.t14), optional
  - **Risk:** UNION performance on large event tables is handled by the per-branch LIMIT and the new indexes. Event names are free text today; eventCategory keeps the filter stable as new names appear.
  - **Resolves:** ops-9, ops-14, frontend-10, product-12
<a id="acq-28"></a>
- [ ] **ACQ-28 · One stage model for admin and requester: Finishing shows Importing, Held, Import failed and Wrong category** — `P2` · `M` · Phase 9
  - **Problem:** Downloads splits its tabs purely on progress < 1 (Downloads.tsx:113-114) and never reads the `imported` flag the feed sends (activity.go:185). A torrent waiting to import, held for review, failing to import, or sitting in the wrong category (only logged, series.go:969-978) looks exactly like a healthy seed. requests/progress.go already models stages (queued, waiting for peers, paused, failed, importing, partial) better than the admin page, and the two vocabularies can disagree.
  - **Approach:** 1. New package internal/pipeline:
       - Stage constants: move Queued, Downloading, Paused, Failed and Importing from requests, and add FetchingMetadata, Stalled, PausedByGuard, Downloaded, ImportRetrying, NeedsReview, WrongCategory, Imported, Seeding, NotManaged and Removed.
       - A pure `Describe(in Input) (Stage, Why string)`. Input = {Phase, Complete, GrabStatus, Linked, Imported, Review{Code, ExpectedTitle}, Failure{Attempts, NextRetry, LastErr}, HeldByGuard, WrongCategory, StallIdle, StallWindow}.
       - Why examples: 'Import failed 3× (permission denied) — next try in 8 min'; 'Held for review: looks like <title>'; 'In category arrmada — it won't import as TV'.
    2. Supporting functions:
       - library.Manager.Failures() map[string]FailureInfo: a read-only snapshot of m.failures under its mutex. Add it here if OBS hasn't.
       - Coordinator.WrongCategoryDownloads(ctx, queue) []WrongCategory, extracted from the diagnostic at series.go:969-978 (completed TV-parsed torrents outside CategorySeries that match a library series).
    3. requests/progress.go uses the pipeline constants. activeGrabs includes 'held' (from [ACQ-09](#acq-09)). NeedsReview and ImportRetrying map to StageImporting with the note 'Being checked', so requesters never see admin detail.
    4. The feed adds `stage` and `why` per transfer, built from: the item, the grab by hash, pending reviews by hash, Manager.Failures, DiskGuard.Held ([ACQ-03](#acq-03)), WrongCategoryDownloads, and StallInfo ([ACQ-06](#acq-06)).
    5. The Queue panel ([ACQ-26](#acq-26)) splits on stage:
       - Downloading: queued through paused
       - Finishing: downloaded, importing, retrying, needs review, wrong category
       - Seeding: imported
       Each card shows its stage chip and why line; not-managed torrents get a muted chip.
  - **Files:** `internal/pipeline/stage.go`, `internal/pipeline/stage_test.go`, `internal/requests/progress.go`, `internal/library/manager.go`, `internal/automation/series.go`, `internal/automation/wrongcategory.go`, `internal/httpapi/activity.go`, `internal/httpapi/activity_test.go`, `web/src/pages/Downloads.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A finished torrent that hasn't imported shows under Finishing with the reason (waiting, retrying with the last error and next try, held for review, wrong category), never as a healthy seed.
    - Requester and admin views use the same stage names for the same download, and the requester view never shows admin detail.
  - **Tests:** Go pipeline: a table test covering every stage and its why string.; Go requests: a 'held' grab → StageImporting with the note 'Being checked'.; Go httpapi: a completed, not-imported torrent with a pending review → needs_review. With a manager failure → import_retrying, with the error in why.; UI check: the Finishing tab lists a held and a retrying download with readable reasons.
  - **Depends on:** [ACQ-03](#acq-03), [ACQ-04](#acq-04), [ACQ-06](#acq-06), [ACQ-09](#acq-09), [ACQ-12](#acq-12), [ACQ-26](#acq-26), OBS — attention feed (ops.t11) also uses Manager.Failures and WrongCategoryDownloads; whichever lands first adds them
  - **Risk:** Moving the stage constants out of requests touches requester-facing JSON. Keep the string values identical.
  - **Resolves:** ops-5, ops-11

#### Milestone: M7 — Activity hub complete

_Transfers are poster-first cards linked to their title, with scope and requester chips, and work on phones. Needs you gathers every stuck item with an action that fits it. Unmanaged torrents can be imported or ignored. Pages update from events instead of blind polling._

<a id="acq-29"></a>
- [ ] **ACQ-29 · Poster-first transfer cards linked to the title, with scope and requester** — `P2` · `M` · Phase 9
  - **Problem:** Cards show only the monospace release name (Downloads.tsx:271-289, 345). The feed parses each torrent's name to find a movie or series, uses the match only for a profile name, and throws away the id, title and poster (activity.go:145-162). You can't jump from a transfer to its title or see whose request it is. On phones, five flex-none buttons squeeze the name to a sliver.
  - **Approach:** 1. Add `Automation.GrabLinks(ctx) map[string]GrabLink{Kind, ID, VersionID, Scope, Status}`, keyed by lower(info_hash) (normRelease(title) only for hashless rows). After [ACQ-25](#acq-25) it reads ActiveByItem.
    2. The feed resolves each torrent by grab link first, with name parsing only as a fallback:
       - Returns `item {kind, id, title, year, poster_url, scope, requested_by[]}`. Scope is e.g. 'S03', 'S03E05' or 'Audiobook' (grab scope, or the parsed release).
       - Titles and posters come from the movie and series lists already loaded, plus books.List and music album lookups.
       - Requesters come from a new `requests.Service.RequestersByLibrary(ctx) map[string][]string` ('movie:12' → usernames), using List's library matching (requests/service.go:267-303).
       - The profile comes from the linked item.
    3. DownloadCard and SeedingCard:
       - A 40×60 poster thumb, the title linking to /movies/:id, /series/:id, /books/:id or /music/album/:id, and scope and 'for Mum' chips.
       - The release name as a secondary monospace line, plus the stage chip and why ([ACQ-28](#acq-28)).
       - Below 640px every action except the primary one moves into the ⋯ menu ([ACQ-07](#acq-07)).
    4. Unlinked torrents keep the release name as the title and a 'Not managed by Arrmada' chip.
    5. Keep the warm dark palette, terracotta accent and current type scale.
  - **Files:** `internal/automation/store.go`, `internal/automation/acquisitions.go`, `internal/httpapi/activity.go`, `internal/httpapi/activity_test.go`, `internal/requests/service.go`, `web/src/pages/Downloads.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Each managed transfer shows its poster and title, links to the detail page, and shows its episode or edition scope and who requested it.
    - At 375px wide the title is readable and the secondary actions sit in a menu.
    - Torrents whose names parse differently from the library title are still linked correctly, by hash.
  - **Tests:** Go httpapi: a torrent whose grab matches by hash links to the movie id, title and poster even when its name parses to another title. A pending request on that movie adds the requester's username.; Go requests: RequestersByLibrary maps movie, series and book requests to library ids.; UI check at 375px and desktop widths: poster, link, chips and overflow menu.
  - **Depends on:** [ACQ-28](#acq-28), [ACQ-25](#acq-25), FE — component kit menu (optional)
  - **Risk:** Requester names on an admin page are fine because the feed is manager-only (SEC). Never add audiobook listening data here.
  - **Resolves:** ops-11
<a id="acq-30"></a>
- [ ] **ACQ-30 · Needs you tab: every stuck item with an action that fits it** — `P2` · `M` · Phase 9
  - **Problem:** Failed imports in retry, stalled and errored downloads, wrong-category torrents and held reviews have no shared home with actions. The owner finds them only by reading logs or noticing a title isn't arriving. Nothing answers 'what needs me' (ops-4).
  - **Approach:** 1. New `GET /api/v1/downloads/needs-you` (RoleManager; no 'activity' in the path) returns:
       - reviews: [ACQ-12](#acq-12) cards
       - import_failures: hash, name, attempts, last_error, next_retry (from Manager.Failures, [ACQ-28](#acq-28))
       - stalled: phase stalled, metadata or error, with StallInfo
       - wrong_category (WrongCategoryDownloads)
       - counts
       OBS's attention feed can read the counts.
    2. Actions:
       - Retry now: `POST /api/v1/imports/{hash}/retry` → library.Manager.RetryNow (added in [ACQ-12](#acq-12)).
       - Reannounce and Recheck: the existing /queue/{hash}/action.
       - Try another release: `POST /api/v1/queue/{hash}/failover` → a new Coordinator.FailOverNow(ctx, hash), which runs [ACQ-05](#acq-05)'s replace-first path immediately, regardless of the window.
       - Move to TV: a new Downloader.SetCategory(ctx, dc, hash, category) → qBittorrent POST /api/v2/torrents/setCategory (hashes, category), exposed as download.Service.SetCategory and `POST /api/v1/queue/{hash}/category {category}`, restricted to Arrmada's own categories.
    3. NeedsYouPanel in Activity:
       - Sections in order: Reviews, Import failures, Stalled or errored, Wrong category, each with a count and empty states.
       - The Dashboard's 'Needs you' strip (OBS) links into these sections.
       - Keep the current visual style.
  - **Files:** `internal/httpapi/needsyou.go`, `internal/httpapi/needsyou_test.go`, `internal/httpapi/server.go`, `internal/library/manager.go`, `internal/automation/stallfailover.go`, `internal/download/client.go`, `internal/download/qbittorrent.go`, `internal/download/qbittorrent_test.go`, `internal/download/service.go`, `web/src/pages/Activity.tsx`, `web/src/pages/NeedsYou.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Needs you lists every kind of stuck item with an action that fits it: retry an import, move a wrong-category torrent, reannounce or replace a stalled one, resolve a review.
    - Retry now makes the next import sweep attempt that hash immediately.
    - A wrong-category TV torrent moved with the button imports on the next sweep (test client and test library only).
  - **Tests:** Go: the imports retry endpoint clears the Manager failure, so the next Process attempts the hash.; Go: SetCategory posts hashes and category to /api/v2/torrents/setCategory (httptest server). A category outside Arrmada's set → 400.; Go: FailOverNow on a stalled grab with an alternate → replace-first behaviour (ACQ-05 fakes).; UI check: each section renders and each action succeeds against a test client.
  - **Depends on:** [ACQ-05](#acq-05), [ACQ-12](#acq-12), [ACQ-26](#acq-26), [ACQ-28](#acq-28), OBS — attention feed, Dashboard 'Needs you' strip and nav badge (consume this endpoint's counts)
  - **Risk:** SetCategory changes how a torrent imports. Limit it to Arrmada's categories and only offer it when the torrent parses as TV and matches a library series.
  - **Resolves:** ops-4, ops-5, ops-11
<a id="acq-31"></a>
- [ ] **ACQ-31 · Unmanaged completed torrents: Import into… or Ignore** — `P3` · `S` · Phase 9
  - **Problem:** Completed torrents Arrmada didn't grab (added by hand, or left over from another tool) sit in Seeding labelled 'Not managed by Arrmada', with no way to bring them into the library or hide them.
  - **Approach:** 1. needs-you ([ACQ-30](#acq-30)) gains `unmanaged`: completed torrents with no grab by hash, not imported, and not in the ignore list.
    2. Import into… opens [ACQ-01](#acq-01)'s target picker (all kinds, chosen by the user). It calls the existing manual-import paths (movies.ManualImport, ManualImportSeries, the books/music import helpers) with the torrent's content_path, then records the hash via recordImportedHash and writes the item's event.
    3. Ignore: a settings key `downloads_ignored_hashes` (a JSON list, pruned to hashes still in the client on each read) and `POST /api/v1/queue/{hash}/ignore`.
    4. A small 'Unmanaged' section at the bottom of Needs you, collapsed by default.
  - **Files:** `internal/httpapi/needsyou.go`, `internal/httpapi/server.go`, `internal/automation/reviews.go`, `web/src/pages/NeedsYou.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - An unmanaged completed movie torrent can be imported into a chosen movie from Activity, and then shows as imported.
    - Ignored torrents stop appearing in Needs you.
  - **Tests:** Go: the ignore list round-trips and prunes hashes no longer in the snapshot.; Go: import of an unmanaged content_path outside the downloads root is rejected (temp dirs only).; UI check on a test client and test library.
  - **Depends on:** [ACQ-30](#acq-30), [ACQ-01](#acq-01), SEC — manual-import path restriction to library and download roots (must land first)
  - **Risk:** Import paths must stay restricted to the download and library roots, which is SEC's manual-import fix. Never run this against the owner's real library files in testing.
  - **Resolves:** ops-11
<a id="acq-32"></a>
- [ ] **ACQ-32 · Event-driven refresh: split the downloads feed, cache the slow parts, stop blind polling** — `P3` · `M` · Phase 9
  - **Problem:** Downloads polls /api/v1/downloads every 3s (Downloads.tsx:104). Each call runs Movies.List, Queue, Series.AcquisitionSummary, Series.List, ImportedHashes and SeedPolicies (a scan of every live grab), plus a regex parse of every torrent (activity.go:27-213), even though Searching and Upcoming change a few times a day. ACQ-18 adds books, albums and attempts to that payload. The Dashboard opens the realtime socket only to display 'connected' (Dashboard.tsx:28, 232).
  - **Approach:** 1. Split handleDownloadsFeed:
       - `GET /api/v1/downloads/transfers`: snapshot + links + stage + seed rules; cheap.
       - `GET /api/v1/downloads/wanted`: searching and upcoming ([ACQ-18](#acq-18)), cached in-process with a 60s TTL.
       The wanted cache is invalidated by bus topics: release.grabbed, download.imported, series.imported, book.imported, music.imported, movie.file_deleted, search.finished, and library add/remove events. /api/v1/downloads stays as a thin aggregate for one release.
    2. Cache SeedPolicies and GrabLinks in the Coordinator behind a version counter, bumped by recordGrab*, setGrabStatus/setGrabStatusByHash and the reconciler.
    3. Publish `queue.changed` from the pause, resume, remove, block, failover and category handlers and from the disk guard.
    4. Frontend:
       - Transfers refetch on socket topics, debounced 500ms, using useLive (or FE's useLiveQuery).
       - A 5s poll runs only while the Queue tab is visible and !document.hidden, because speeds change continuously.
       - Wanted refetches on events or every 2 min.
       - The Dashboard refetches its activity and queue tile on grab and import events.
       - The eventbus can drop messages, so the slow fallback poll always stays.
  - **Files:** `internal/httpapi/activity.go`, `internal/httpapi/wanted.go`, `internal/httpapi/server.go`, `internal/httpapi/downloads.go`, `internal/automation/coordinator.go`, `internal/automation/store.go`, `internal/download/diskguard.go`, `web/src/pages/Downloads.tsx`, `web/src/pages/Dashboard.tsx`, `web/src/lib/useLive.ts`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With the Queue tab open and idle, the server no longer runs Movies.List or AcquisitionSummary every few seconds. Wanted is rebuilt only on events or every 2 minutes.
    - A grab or import shows up on the open Activity page within about a second.
    - A background browser tab stops polling.
  - **Tests:** Go: publishing release.grabbed invalidates the wanted cache, so the next call rebuilds it (count calls on a fake).; Go httpapi: the transfers handler does not call Movies.List or AcquisitionSummary (fakes record calls).; Go: the SeedPolicies cache rebuilds after setGrabStatus bumps the version (with -race).; UI check (network panel): no /downloads/wanted requests over 2 idle minutes, and transfers stop while the tab is hidden.
  - **Depends on:** [ACQ-18](#acq-18), [ACQ-26](#acq-26), [ACQ-28](#acq-28), [ACQ-29](#acq-29), SEC — role-filtered websocket, so pushing more acquisition events is safe, FE — useLiveQuery hook (optional)
  - **Risk:** A missed cache invalidation shows stale Wanted rows for up to 60s, which is acceptable. Never cache the transfer list beyond the 2s snapshot. Run with -race in Docker (new caches and counters).
  - **Resolves:** ops-14

#### Risks

- The grabs status vocabulary drives removal decisions. A missed IN-list either re-grabs a held release or deletes one at seed time. ACQ-09 mitigates this with central status sets and a source-scanning test, and every grabs query must be grepped when adding a status.
- Turning stall fail-over on by default (ACQ-06) flips behaviour for every profile whose stall_minutes is 0. It's acceptable only with ACQ-05's replace-first rule, the clock holding in paused, queued, checking and guard-held states, and the 3-per-tick cap. Ship ACQ-05 first and call out the semantic change in the commit.
- Migration numbering: other epics also add migrations after 0089. Assign numbers at implementation time and rebase; never reuse a number already on main.
- Search-code churn: ACQ-08, ACQ-15 and ACQ-16 change searchAndGrab, grabSeriesLimited and the book search signatures, which SER, BOOK and QUAL may also reshape. Agree an order, and keep outcome collection observational so the ranking tests stay untouched.
- The /activity SPA route versus ad-block lists: the API comment says '/activity' URLs are blocked. Keep every API path under /api/v1/downloads etc., and verify the SPA route with uBlock Origin; fall back to /hub/:tab if needed.
- Concurrency: the snapshot cache (ACQ-22), the reconciler (ACQ-24), the manual-search single-flight (ACQ-19) and the caches (ACQ-32) add shared state. Every one needs go test -race in Docker before pushing, because Windows can't run -race locally.
- SQLite write contention: the reconciler (every 30s) and search_attempts inserts add writes alongside the import sweeps. Write only changed rows, batch them in one transaction, and prune attempts in the same transaction.
- Navigation churn for the owner when Downloads, History and Review merge. Mitigated by redirects that keep query params, and by doing the shell (ACQ-26) as a behaviour-preserving refactor.
- Indexer error strings reach the browser (ACQ-14, ACQ-15). They must pass through sanitizeErr; check native searchers for token-bearing URLs.
- Behaviour changes users will notice: Resume all skips guard-held torrents (ACQ-03), Block refuses unlinked torrents (ACQ-10), and dismissed downloads are removed from the client without their data at the seed goal (ACQ-09). Each needs clear UI copy.

#### Out of scope

- A separate `acquisitions` table and the overhaul's 'import writes the library record in the same step, with an outbox for side effects'. grabs evolves in place instead; an outbox can follow later if the event bus's dropped messages become a real problem.
- Usenet/SABnzbd or any second download-client implementation. The Downloader interface gains only SetCategory.
- Changing how releases are ranked (seeders' weight, min_seeders defaults): QUAL. ACQ only records reject codes.
- Admin notifications, Apprise routing, the Dashboard 'Needs you' strip and the attention-feed counts: OBS. ACQ supplies the events and the needs-you data.
- Deny-by-default auth, websocket role filtering and the manual-import path fix: SEC.
- Download Delete confirmation and the /queue/all foot-gun: SAFE.
- The series season-pack grab policy and planner (SER), the book search ladder and MAM RSS (BOOK), music backoff and the hide-or-rebuild decision (MUS).
- Requester-facing redesign beyond the last-checked line and stage-string parity: REQ.
- Per-file mapping for movie or book reviews. The mapper is series-only, since numbering reviews only arise there.
- A frontend design-system overhaul: UI tasks keep the current palette, accent and type scale.
- Any testing against the owner's real library files. Mapper, import and stall tests use temp dirs and fake clients only.

