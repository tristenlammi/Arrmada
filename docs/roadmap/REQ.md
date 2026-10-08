# REQ — Requests 2.0 & Discover

_Part of the [Arrmada roadmap](../../ROADMAP.md). 23 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Turn requests from a hover strip on Discover into a complete request product. Staff are alerted to new requests and decide on a real /requests page with a shared sheet: profile, seasons, a reason, and bulk actions. Requesters can ask for exactly the seasons they want, within fair limits, follow other people's requests, and are told 'ready' only once the title is actually in Plex, with a link that opens it. Discover adds Recently added, browse grids, person and collection pages, and a 'Report a problem' loop the owner can fix in place.

**Why.** Arrmada's Discover browsing is Netflix-grade, but its request management covers about 35% of what Overseerr does, and that is the part family and friends depend on.
- Staff are never told a request is waiting. Create publishes nothing, the admin notifier ignores requests, and there is no badge. The only approve and decline controls sit in an opacity-0 hover overlay on 150px posters, which a phone user can tap without seeing. The Requests page exists in code but isn't routed, though Settings still points to it (discover-1, frontend-4, frontend-10, product-2).
- A series request monitors and searches every season (discover-2, product-3).
- Plex sign-ins auto-approve by default, there are no quotas, and auto-approved users are notified about their own clicks. The Overseerr import floods inboxes (discover-2, system-6, discover-8).
- A decline carries no reason, the requester's note is never shown, and people who join a request can't see it or leave it (discover-10, discover-3).
- 'Your request is ready' fires when the file lands on disk, not when Plex has it, and the link opens /discover rather than Plex (insights-5, product-1, backend-14, movies-9). The card and the notification also disagree on whether a series is complete (discover-12).
- Every open Discover tab rebuilds the whole request history every 8 seconds (discover-9).
- Requesters hit dead ends: 'In library' offers no Watch button and no Recently added row (discover-5). There are no See-all grids or person pages, collections are flattened (discover-6), and they have no way to report a bad file (product-14).

**Depends on:** PLEX — needed by [REQ-15](#req-15), [REQ-16](#req-16) and [REQ-18](#req-18):
- a Plex locator that finds a movie or show by TMDB/TVDB/IMDb GUID and builds a WatchURL from the machine id (drafts insights.t6, movies.t19, product.t11, backend.t28)
- a post-import partial scan, so Plex has items within a minute
- a TMDB→rating-key resolver for titles that were never requested (draft discover.t15)
If PLEX hasn't shipped the locator, [REQ-15](#req-15) carries a minimal internal/plex/locate.go.; APP — Discover title routes /discover/:media/:tmdb (draft discover.t3), for deep links and Back-to-sheet in [REQ-13](#req-13), [REQ-16](#req-16), [REQ-19](#req-19), [REQ-20](#req-20) and [REQ-21](#req-21). Also the requester bottom tab bar and the bell in the shared layout, for nav entries in [REQ-05](#req-05), and the tap-safe MediaCard quick-request (draft discover.t1).; FE — needed by [REQ-04](#req-04), [REQ-05](#req-05), [REQ-08](#req-08) and [REQ-17](#req-17):
- component kit: Modal/Sheet, useConfirm, Select
- an ApiError carrying status and body
- one shared websocket connection
- a vitest harness
[REQ-04](#req-04) has a local fallback.; SEC — role-filtered websocket topics and a deny-by-default route table with a route-walk test. [REQ-07](#req-07) adds a local deny list for request.* and issue.* until then; [REQ-17](#req-17)'s requests.changed must be on the requester allowlist; every new route must be registered there.; SER — needed by [REQ-02](#req-02), [REQ-10](#req-10) and [REQ-12](#req-12):
- a non-cascading series monitor flag
- monitor-new-seasons and monitor presets (draft series.t8)
- allStats semantics; OBS — notification event catalog ([REQ-07](#req-07) registers request.created there if it replaces the per-column toggles), plus the attention feed and admin alerts for open issues ([REQ-22](#req-22)).; CFG — the People & access page hosts the per-user auto-approve types ([REQ-09](#req-09)) and quota fields ([REQ-14](#req-14)) if it is rebuilt first.; BOOK — requests store book_id when approved (catalogue-key rewrite fix); [REQ-03](#req-03)'s page-scoped lookups should key books by it once it lands.; BE — job runner or outbox, optional; [REQ-01](#req-01)'s bounded search queue can submit to it instead.; INT/BE — TMDB detail cache (draft discover.t23), used by [REQ-11](#req-11), [REQ-13](#req-13) and [REQ-20](#req-20).; SUB — a subtitle job completion callback, for [REQ-23](#req-23).; SAFE — recycle-bin semantics for blocklist-and-replace, for [REQ-23](#req-23).

#### Design

## Target: Requests 2.0 and a deeper Discover

### Lifecycle (one vocabulary for the API, UI and notifications)
```
pending --approve(profile, seasons)--> approved: searching > queued > downloading > importing > adding to Plex --> ready (ready_at set when the requester is told)
pending --decline(reason)--> declined --re-request(with a note)--> pending (rerequest=1)
pending --withdraw--> deleted (quota refunded)      follower --stop following--> removed from request_subscribers
```
Sections are plain SQL predicates, so paging and counts stay cheap:
- **needs_approval** = pending
- **in_progress** = approved and ready_at = 0
- **ready** = approved and ready_at > 0
- **declined**

### Data model
All changes are additive. Each task ships its own migration at the next free number (0090 or later) when it is implemented.

| Table | Change | Task |
|---|---|---|
| requests | `ready_at` INTEGER, backfilled from 'ready' inbox refs already sent; index (status, ready_at, updated_at) | [REQ-01](#req-01) |
| requests | decline_reason, decided_by, decided_by_name, decided_at, rerequest | [REQ-08](#req-08) |
| requests | `seasons` TEXT (JSON int array; '' means the whole show). idx_requests_tmdb is replaced by a unique idx_requests_movie and a non-unique idx_requests_series | [REQ-12](#req-12) |
| requests | on_disk_at INTEGER, plex_rating_key TEXT | [REQ-15](#req-15) |
| notifications | on_request INTEGER (or an entry in OBS's event catalog) | [REQ-07](#req-07) |
| users | auto_approve_movie / _series / _book, copied from auto_approve | [REQ-09](#req-09) |
| users + new request_usage ledger | quota_movies / _seasons / _books (-1 means the default) | [REQ-14](#req-14) |
| user_notifications | url, plex_url | [REQ-16](#req-16) |
| issues, issue_reporters | new tables | [REQ-22](#req-22) |

Movies and books keep one request per title. A series gets one row per ask (a set of seasons), so each ask has its own owner, status, decision, quota units and ready notice. Legacy rows keep seasons='' and their old notification refs.

### Service shape (internal/requests)
- **Narrow interfaces:** movieLib, seriesLib, bookLib, searcher, PushSender, StaffLister and PlexLocator. The optional ones are nil-safe, and all of them can be faked in tests.
- **Calls:**
  - `Create(ctx, in, CreateOptions{AutoApprove, Silent, DeferSearch, QuotaExempt})`
  - `Approve(ctx, id, ApproveOptions{Profile, Seasons, DecidedBy, Auto, Silent, DeferSearch})`
  - `Decline(ctx, id, DeclineOptions{Reason, DecidedBy})`
  - `Unsubscribe`
  - `List(ctx, ListFilter)` plus `Counts`
- **One bounded search queue** (2 workers) for every approval path: single, bulk, auto, and import-deferred.
- **One readiness rule:** monitored, aired, non-special episodes, limited to the row's seasons for a season row. A Plex gate sits in front of the ready send, and ready_at is stamped once the requester has been told.
- **Events:**
  - `request.created`, `request.decided` and `issue.reported` stay on the bus only. The realtime hub drops the request.* and issue.* prefixes, so requesters' browsers never receive other people's titles or names.
  - `requests.changed` carries no payload and is safe to broadcast. The UI refetches on it.

### HTTP API
- `GET /api/v1/requests`
  - params: `section=needs_approval|in_progress|ready|declined|strip|all`, `limit`, `offset`, `media_type`, `q`
  - returns `{requests, counts, total, auto_approve}`
  - each request carries relation (owner or subscriber) and library_id (staff only)
- `GET /api/v1/requests/{id}`
- `POST /api/v1/requests {…, note, seasons}`: 409 code=needs_note, 429 when over quota
- `POST /api/v1/requests/{id}/approve {quality_profile, seasons}`, `POST …/decline {reason}`, `POST /api/v1/requests/bulk`
- `DELETE /api/v1/requests/{id}` (withdraw), `DELETE /api/v1/requests/{id}/subscription` (stop following)
- `GET /api/v1/requests/pending-count` (manager), `GET /api/v1/me/quota`
- `GET /api/v1/media/series/{tmdb}/seasons`
- `GET /api/v1/discover/recently-added | browse | person/{id} | collection/{id}`, plus `page` on search
- `/api/v1/issues`: create, list, resolve, dismiss and action

Every new route gets an explicit `requireRole`. Routes requesters can reach sit under prefixes already on the external allowlist (/api/v1/requests, /api/v1/me/, /api/v1/media/, /api/v1/discover). The exception is /api/v1/issues, which gets added. If SEC's deny-by-default route table has landed, register the routes there too.

### UI shape
Everything keeps the current dark warm palette, terracotta accent, type scale, BADGE_BG chips and accent-gradient buttons.

- **/requests**, for everyone. `?tab=` and `?id=` live in the URL, and `?id=` is the deep-link target for notifications.
  - Staff tabs: *Needs approval · n / In progress / Ready / Declined / Issues*, with checkboxes and bulk approve or decline.
  - Requester tabs: *Waiting / In progress / Ready / Declined*, over their own and followed requests.
- **RequestSheet**, shared by the page, the Discover strip and notification deep links:
  - who asked, when, their note and the current stage
  - profile select (4K grouped), season trim, decline with a reason
  - withdraw or stop following
  - Open in library, Watch on Plex
- **Discover strip:** a capped, tap-safe view of the same feed. Staff see pending first. It has a 'See all' link.
- **Title sheet:**
  - season picker for series and 'Request more seasons'
  - note field and quota line
  - re-request flow that asks for a note
  - Watch on Plex and Report a problem
- **New rows and pages:** Recently added, Ready for you, See-all browse grids, person pages and named collection pages.
- **Staff alerts:** a sidebar pending badge, Apprise ('New request' toggle), Web Push and inbox entries.
- **Shared frontend libs:**
  - lib/requestStage.ts: stage text, formatSeasons, STAGE_ORDER
  - lib/useRequestsFeed.ts: websocket nudge plus visibility-gated polling

### Guardrails
- Requesters never see other requesters' names or the admin's monitoring state. Season states read 'Requested' or 'On the way'.
- Every new TMDB-sourced list goes through toItem and adultfilter.Matches: browse, person credits, collection members and recently added.
- No credentials in code or chat. The Plex token stays in the Insights config.
- Audiobooks are untouched, and nothing here records what anyone listens to.
- Upgrade behaviour is explicit:
  - existing users keep their auto-approve, now split per type
  - quotas default to unlimited
  - ready_at is backfilled so nobody is re-notified
  - legacy whole-show series requests keep their refs
- Race tests run in Docker before every push, and commits end with the Co-Authored-By trailer.

#### Milestone: M1 — Requests you can see and act on

_Staff hear about new requests (Apprise, push, inbox, sidebar badge) and handle them on a routed /requests page, with bulk actions and a tap-safe sheet where they can pick a profile. Requesters see the requests they joined and can stop following them. Auto-approvals and imports stop spamming. The Discover strip is capped, puts pending first for staff, and stops rebuilding every request ever made every 8 seconds. Request cards and 'ready' notifications agree on when a series is complete._

<a id="req-01"></a>
- [ ] **REQ-01 · Requests core: quiet auto-approvals, a bounded search queue, a silent Overseerr import, and a ready_at stamp** — `P1` · `M` · Phase 6
  - **Problem:** Auto-approved users are notified about their own click. Create calls Approve (service.go:109-111), which always calls notifyDecision(true) (service.go:235), sending an inbox entry, Web Push and Apprise.

The Overseerr import calls Create(bg, in, status=='approved') for every item (import_overseerr.go:104). Imported users' inboxes fill with 'approved' rows, and the 10-minute sweep (main.go:439) then adds a 'ready' row for every title already available.

Each approval spawns its own untracked search goroutine (service.go:183-203). Nothing records that a ready notice went out, so SweepReadyRequests re-checks every approved request forever.

The UI ignores res.request.status (Discover.tsx:60-62), so an auto-approved request reads 'Requested — pending approval' (Discover.tsx:1059, 1311).

Service holds concrete *movies/*series/*books/*Coordinator pointers, so Approve can't be unit-tested.
  - **Approach:** 1. Narrow dependencies, in a new internal/requests/deps.go:
       - movieLib {Add, Get, List, SetMonitored}
       - seriesLib {Add, Get, List, HasWantedEpisodes, SetSeasonMonitored}
       - bookLib {Add, Get, List, SetMonitored}
       - searcher {SearchMovie, SearchSeriesNow, SearchBookNow}
       NewService keeps its signature, since the concrete types satisfy the interfaces. Tests build the Service with fakes through an unexported constructor.
    2. Options instead of bools:
       - Create(ctx, in, CreateOptions{AutoApprove, Silent, DeferSearch bool})
       - Approve(ctx, id, ApproveOptions{Profile string; DecidedBy int64; DecidedByName string; Auto, Silent, DeferSearch bool})
       [REQ-12](#req-12) adds Seasons and [REQ-14](#req-14) adds quota fields later. In the same commit, update handleCreateRequest, handleApproveRequest (DecidedBy = caller) and import_overseerr.go.
    3. Decision notifications: notifyParties takes an exclude set.
       - Silent: notify nobody.
       - Auto, or DecidedBy == RequestedBy: skip the requester but still tell subscribers.
    4. Bounded search queue, in a new internal/requests/searchqueue.go:
       - searchJob{kind, id, title} on a 256-slot channel, drained by RunSearchQueue(ctx, 2). main.go starts it beside RunNotifier.
       - Each job keeps today's timeout: 3 min for a movie, 5 min for a series or book.
       - Enqueue never blocks. A full queue logs a warning and drops the job, and the existing missing-sweeps pick the item up.
       - DeferSearch skips the enqueue. Workers exit on ctx.Done().
       - If BE's job runner has landed, submit to it instead.
    5. Migration NNNN_request_ready_at.sql (next free number from 0090):
       - ALTER TABLE requests ADD COLUMN ready_at INTEGER NOT NULL DEFAULT 0;
       - Backfill from ready notices already sent: UPDATE requests SET ready_at = COALESCE((SELECT MIN(n.created_at) FROM user_notifications n WHERE n.ref = CASE WHEN requests.media_type='book' THEN 'book:'||requests.ol_key ELSE requests.media_type||':'||requests.tmdb_id END), 0) WHERE status='approved';
       - CREATE INDEX idx_requests_section ON requests(status, ready_at, updated_at);
    6. notifyReady stamps the row after fan-out via repo.MarkReady: UPDATE requests SET ready_at=? WHERE id=? AND ready_at=0. SweepReadyRequests reads repo.ListAwaitingReady (approved AND ready_at=0) instead of every approved row. Request JSON exposes ready_at.
    7. Overseerr import:
       - Use CreateOptions{AutoApprove: approved, Silent: true, DeferSearch: true}.
       - After each create, Service.MarkReadyIfAvailable(ctx, id) stamps ready_at without notifying when the media already has files.
       - Imported users start with a clean bell, and searches spread over the normal sweeps.
    8. UI (Discover.tsx):
       - doRequest keeps a Map<key, status> from res.request.status.
       - badgeFor maps approved to 'Requested'.
       - The sheet and toast say 'Requested — searching now' when approved and 'Requested — waiting for approval' when pending.
  - **Files:** `internal/requests/service.go`, `internal/requests/deps.go (new)`, `internal/requests/searchqueue.go (new)`, `internal/requests/usernotify.go`, `internal/requests/repo.go`, `internal/requests/service_test.go`, `internal/httpapi/requests.go`, `internal/httpapi/import_overseerr.go`, `internal/store/migrations/NNNN_request_ready_at.sql (new, next free ≥0090)`, `cmd/arrmada/main.go`, `web/src/pages/Discover.tsx`
  - **Acceptance:**
    - An auto-approved user requesting a movie gets no 'approved' inbox entry, push or Apprise message, and the sheet says 'Requested — searching now'.
    - When an admin approves someone else's request, the requester and every follower get exactly one 'approved' notification. An admin approving their own request sends nothing to themselves.
    - After an Overseerr import of 200 items, imported users have no unread 'approved' or 'ready' rows for titles already available. The log shows searches spread across sweeps, not a burst.
    - Approving 20 requests at once never runs more than 2 request-triggered searches concurrently.
    - After the upgrade, nobody is re-notified 'ready' for a request they were already told about, and the ready sweep skips stamped rows.
  - **Tests:** Go: TestAutoApproveSendsNoDecisionNotification (fake push and inbox; subscribers are still notified).; Go: TestStaffApprovingOwnRequestIsSilent.; Go: TestImportSilentAndPreSeedsReady (no inbox rows; ready_at stamped; SweepReadyRequests then sends nothing).; Go: TestSearchQueueConcurrencyBound with a blocking fake searcher, run with -race in Docker.; Go: TestDeferSearchDoesNotEnqueue and TestFullQueueDropsWithoutBlocking.; Go: TestReadyAtBackfill (on a fixture DB with an existing 'movie:603' inbox row, the migration stamps only that request).
  - **Risk:** The Create and Approve signature changes touch the handlers and the import together, so land them in one commit. The queue must never block an HTTP handler or shutdown. ready_at is per request row, which REQ-12's per-season rows rely on. The backfill ref must match requestRef exactly (movie:<tmdb>, series:<tmdb>, book:<ol_key>).
  - **Resolves:** discover-8
<a id="req-02"></a>
- [ ] **REQ-02 · One rule for 'is this series request complete', shared by tracking and the ready notification** — `P2` · `S` · Phase 6
  - **Problem:** The card and the notification use different rules.
- Track treats a series as complete when epHave >= epTotal (progress.go:76). epTotal comes from allStats, which counts every aired non-special episode whether or not it is monitored (series/repo.go:84-89), although the Stats comment says 'monitored seasons' (series.go:39).
- The ready notifier and the sweep use HasWantedEpisodes, which counts monitored episodes only (series/repo.go:758-765; usernotify.go:138, 293).
So a show with unmonitored older seasons gets 'X is ready to watch' while its card stays 'Partly ready · 20 of 150 episodes'. Season-scoped requests (REQ-12) need the same rule restricted to their own seasons.
  - **Approach:** 1. internal/series/repo.go:
       - Add Progress{Have, Total int}.
       - Add MonitoredProgress(ctx, seriesIDs []int64) (map[int64]Progress, error): one GROUP BY over episodes WHERE season_number > 0 AND monitored = 1, plus series_id IN (…) when ids are given. Total counts episodes where has_file=1 OR (air_date <> '' AND date(air_date) <= date('now')); Have counts has_file. This is the aired rule HasWantedEpisodes already uses.
       - Add SeasonProgress(ctx, seriesID) (map[int]SeasonProgress{Have, Aired, Monitored}, error) for [REQ-11](#req-11) and [REQ-12](#req-12).
       - Expose all of these on series.Service.
    2. requests.enrichAvailability fills epHave and epTotal from MonitoredProgress instead of Stats.
    3. Add one helper, seriesComplete(p) = p.Have > 0 && p.Have >= p.Total. Use it in track (progress.go:76), RunNotifier and SweepReadyRequests, and remove their separate HasWantedEpisodes checks.
    4. Fix the Stats.Episodes comment (series.go:39) to say it counts aired non-special episodes, monitored or not. Do not change allStats or the library grid; SER owns those semantics.
  - **Files:** `internal/series/repo.go`, `internal/series/series.go`, `internal/series/service.go`, `internal/series/repo_test.go`, `internal/requests/service.go`, `internal/requests/progress.go`, `internal/requests/usernotify.go`, `internal/requests/progress_test.go`
  - **Acceptance:**
    - A series with S1-2 unmonitored and S3 fully on disk shows its request as 'Ready' at the same moment the 'ready' notification is sent.
    - Shows with no unmonitored seasons behave exactly as today.
    - The Stats comment matches the query.
  - **Tests:** Go: TestMonitoredProgress (an unmonitored aired episode isn't in the total; an unaired monitored episode isn't counted; specials are excluded).; Go: TestSeasonProgress (per-season have/aired/monitored).; Go: TestTrackAndNotifierAgree (a fixture where the old code said partial vs ready; both now say ready).; Go: the existing TestTrackStages and TestNotifyRequester still pass.
  - **Depends on:** SER — if SER makes allStats monitored-only first, reuse that instead of the extra query
  - **Risk:** Low. Make sure the series library grid isn't changed by accident. The notifier now needs Have > 0 explicitly, which the old sweep enforced through HaveFiles > 0.
  - **Resolves:** discover-12
<a id="req-03"></a>
- [ ] **REQ-03 · Requests API v2: sections, paging, counts, joined requests, stop following, and cheap tracking** — `P1` · `M` · Phase 6
  - **Problem:** Repo.List has no LIMIT or sections (repo.go:122-152), so handleListRequests returns every request ever made. Each call then:
- lists all movies, all series (with a GROUP BY over every episode) and all books in enrichAvailability (service.go:260-305)
- calls Downloads.Queue live (requests.go:34-37)
- runs one activeGrabs query per request (progress.go:90-91, 158-176)
The Discover strip repeats this every 8 s.

People who joined through request_subscribers never see the request: List filters only on requested_by (repo.go:130-133). They can't leave it either, because delete requires RequestedBy == u.ID (requests.go:167). The Because and Recommended seeds (discover_rows.go:150, discover_recommended.go:174) miss followed titles.
  - **Approach:** 1. repo: ListFilter{Section, Status (legacy), MediaType, Query string; UserID int64; IncludeJoined bool; ReadyWithinDays, Limit, Offset int}.
       - List(ctx, f) ([]Request, total int, error).
       - Counts(ctx, f) (Counts{NeedsApproval, InProgress, Ready, Declined}, error): one GROUP BY over CASE.
       - Sections:
         - needs_approval: status='pending', ORDER BY created_at ASC
         - in_progress: status='approved' AND ready_at=0, ORDER BY updated_at DESC
         - ready: status='approved' AND ready_at>0 (plus a ready_at window when ReadyWithinDays is set), ORDER BY ready_at DESC
         - declined: status='declined', ORDER BY updated_at DESC
         - all: no section filter, id DESC (legacy)
       - User scope: (requested_by = ? OR id IN (SELECT request_id FROM request_subscribers WHERE user_id = ?)). Select CASE WHEN requested_by = ? THEN 'owner' ELSE 'subscriber' END into Request.Relation (json relation,omitempty).
       - Query is a title LIKE.
    2. Service.List(ctx, ListFilter) replaces List(ctx, status, requestedBy). Update every caller in the same change:
       - httpapi/requests.go:23
       - discover.go:109 (unscoped)
       - discover_rows.go:150 and discover_recommended.go:174 (UserID + IncludeJoined; still drop in-flight items)
       - books.go:574 (unscoped)
       - mybooks.go:77 (own only, unchanged)
    3. Service.Strip(ctx, viewer):
       - staff: needs_approval (limit 20) then in_progress (limit 20)
       - requesters: own and joined pending, in_progress, ready within 14 days and declined within 30 days, capped at 40
    4. Tracking scoped to the page:
       - enrichAvailability looks up only the page's media: movies.Service.ByTMDBIDs(ctx, []int) and books.Service.ByOLKeys(ctx, []string), each a single IN query, plus series MonitoredProgress for the page's series ids ([REQ-02](#req-02)).
       - Track loads active grabs once: SELECT media_type, movie_id, info_hash, title, grabbed_at FROM grabs WHERE status='grabbed', grouped in Go by (media_type, movie_id). This replaces the per-request activeGrabs.
       - The handler calls Downloads.Queue only when some request on the page is approved, has ready_at=0 and has a library item.
       - A Queue error degrades to file-only stages and never returns 500.
    5. GET /api/v1/requests handler:
       - section (default all, which keeps today's behaviour)
       - limit (default 50 when a section is given, max 200), offset, media_type, q, status (legacy)
       - response {requests, counts, total, auto_approve}
       - Non-managers are forced to own-plus-joined scope.
       - library_id is exposed for staff and zeroed for requesters.
    6. GET /api/v1/requests/{id} (a.protected): one request with tracking. Staff also get followers [{name}]. Requesters get it only as owner or subscriber, otherwise 404.
    7. DELETE /api/v1/requests/{id}/subscription (requireRole RoleRequester) runs Service.Unsubscribe(ctx, id, uid). RemoveSubscriber now reports RowsAffected, and no row means 404.
    8. api.ts:
       - requests({section, limit, offset, media_type, q})
       - getRequest(id) and unsubscribeRequest(id)
       - MediaRequest gains relation, library_id, ready_at and followers.
  - **Files:** `internal/requests/repo.go`, `internal/requests/service.go`, `internal/requests/progress.go`, `internal/requests/repo_test.go`, `internal/requests/progress_test.go`, `internal/movies/service.go`, `internal/movies/repo.go`, `internal/books/service.go`, `internal/books/repo.go`, `internal/httpapi/requests.go`, `internal/httpapi/server.go`, `internal/httpapi/discover.go`, `internal/httpapi/discover_rows.go`, `internal/httpapi/discover_recommended.go`
  - **Acceptance:**
    - GET /api/v1/requests?section=needs_approval returns only pending requests, oldest first, with counts for every section and a correct total.
    - User B requests a title A already requested. B's list includes it with relation=subscriber. B can stop following, after which B gets no further notifications for it. B still cannot withdraw A's request (403).
    - A requester never sees another person's request in any section or through GET /requests/{id}.
    - One list call issues a bounded number of queries (no per-request grabs query), and skips the download client when nothing on the page is in flight.
    - section=all (the default) returns exactly what today's endpoint returns, so the current Discover strip keeps working.
  - **Tests:** Go: TestListSections (filtering, ordering, counts, paging).; Go: TestListIncludesSubscriptions (owner and subscriber rows with the right relation; the status filter still applies).; Go: TestUnsubscribeOnlySelf (404 when not subscribed; another user's subscription untouched).; Go: TestTrackBatchedGrabs (one grabs query for N requests; same stages as the TestTrackStages fixtures).; Go: TestListSkipsQueueWhenNothingInFlight (handler with a fake Downloads counting calls).; Go: TestGetRequestScope (requester 404 for others; staff see followers).; Go: the existing TestCreateSubscribesDuplicates and TestDeleteRemovesSubscribers still pass.
  - **Depends on:** [REQ-01](#req-01), [REQ-02](#req-02), BOOK — once requests store book_id, key book lookups by it instead of ol_key
  - **Risk:** Every caller of the old List signature must change together. Requester scoping is enforced only server-side, so test it directly. Because and Recommended seeds now include followed titles, which is intended, but they must still drop items already in flight.
  - **Resolves:** product-2, discover-3, discover-9, discover-1
<a id="req-04"></a>
- [ ] **REQ-04 · RequestSheet: tap a request to see who asked, then approve with a profile, decline, withdraw or stop following** — `P1` · `S` · Phase 6
  - **Problem:** Approve, Decline and Withdraw live only in an opacity-0 group-hover overlay on 150px posters (Discover.tsx:637-653). On a phone they are tappable but invisible, and Decline has no confirmation. Nothing shows who asked, when, or with what note. Approve never sends a quality profile, although the API accepts one (Discover.tsx:641, requests.go:111-117).
  - **Approach:** 1. Move requestStage, etaText and STAGE_ORDER from Discover.tsx into a new web/src/lib/requestStage.ts, shared by the sheet, the strip and the page.
    2. New web/src/components/RequestSheet.tsx:
       - Built on the FE kit Modal (sheet variant on mobile) and useConfirm when they exist. Until then, lift RequestDetailModal's focus trap, Esc handling and scroll lock into a small local Sheet wrapper.
       - Props {requestId or request, onChanged, onClose}. It loads GET /api/v1/requests/{id} ([REQ-03](#req-03)) for fresh tracking.
    3. Content:
       - backdrop or poster, title, year, media type
       - stage badge and detail
       - requester avatar, name and relative age (staff)
       - note and followers (staff)
       - a 'Following' chip for subscribers
    4. Actions by role and state:
       - Staff, pending:
         - quality-profile select from api.qualityProfiles filtered by media type, preselecting the request's own profile or the default, with 2160p profiles grouped under '4K'
         - Approve → api.approveRequest(id, {quality_profile})
         - Decline with a confirm ([REQ-08](#req-08) adds the reason)
         - Delete with a confirm
       - Staff, approved: 'Open in library' to /movies/:id, /series/:id or /books/:id from library_id.
       - Owner, pending: Withdraw (confirm).
       - Subscriber: Stop following (confirm) → unsubscribeRequest.
       - Every action is a visible button at least 40px tall. Success shows a toast and calls onChanged; errors show inline.
    5. api.ts: approveRequest(id, body {quality_profile?, seasons?}), keeping a positional-profile wrapper for compatibility.
  - **Files:** `web/src/components/RequestSheet.tsx (new)`, `web/src/lib/requestStage.ts (new)`, `web/src/pages/Discover.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Opening a request shows its title, who requested it and how long ago, the note, and its current stage.
    - Approving with a non-default or 4K profile adds the title with that profile, visible on its library page.
    - Decline and Delete require confirmation. Esc or a backdrop click closes the sheet and focus returns to the opener.
    - Requesters see Withdraw only on their own pending requests and Stop following only on followed ones, and never see Approve or Decline.
  - **Tests:** UI check as admin: approve with a chosen profile, then verify via GET /api/v1/movies/{id}. Confirm a decline.; UI check as requester: withdraw your own pending request; stop following a joined one.; UI check: keyboard focus trap and Esc close at 375px.; vitest (if FE's harness exists): role- and state-based button rendering.
  - **Depends on:** [REQ-03](#req-03), FE — Modal/Sheet and useConfirm kit (preferred, not blocking)
  - **Risk:** Low. Reuse the Discover chip, badge and gradient-button styles so the sheet reads as the same product.
  - **Resolves:** frontend-4, product-2
<a id="req-05"></a>
- [ ] **REQ-05 · A routed Requests page with sections, filters and bulk approve/decline** — `P1` · `M` · Phase 6
  - **Problem:** Requests.tsx (264 lines, Pending/Approved/Declined filters) isn't routed. App.tsx has no /requests route and nav.ts has no entry. Its in-page RequestModal calls the staff-only lookup endpoints (api.lookupMovies and api.lookupSeries). Settings.tsx:789 tells the admin that imported requests 'will appear on the Requests page', which doesn't exist. Staff have no list view, filter or bulk action.
  - **Approach:** Backend:
    1. POST /api/v1/requests/bulk (requireRole RoleManager):
       - body {action: approve|decline, ids (at most 100), quality_profile?, reason?}
       - runs Approve or Decline sequentially with DecidedBy = caller, and returns {results: [{id, ok, error}]}
       - Searches go through [REQ-01](#req-01)'s bounded queue, so a bulk approve never fans out.
    
    Frontend:
    2. Rewrite web/src/pages/Requests.tsx as the routed page and delete RequestsPanel and RequestModal.
       - App.tsx: add /requests to the staff AppLayout tree and to the requester UserLayout tree, including external sessions (/api/v1/requests is allowlisted).
       - nav.ts: Requests after Discover in Services (FE/APP may regroup).
       - UserLayout nav: Requests after Discover.
    3. Tabs from ?tab=:
       - Staff: needs ('Needs approval · n', the default when counts.needs_approval > 0), active ('In progress'), ready ('Ready'), declined ('Declined').
       - Requesters: 'Waiting', 'In progress', 'Ready', 'Declined', over their own and followed requests.
       - Filters in the URL: type (movie/series/book) and q. Paging with 'Load more' (offset).
    4. Rows show:
       - poster thumb, title and year, type chip
       - requester (staff), age, note snippet
       - stage text from requestStage, and the relation chip
       - Staff on pending rows also get always-visible Approve and Decline buttons (Decline confirms), a checkbox, select-all, and a sticky 'Approve n / Decline n' bar. Bulk Decline confirms, and per-row failures show on the row.
    5. A row click, or ?id=<id>, opens RequestSheet ([REQ-04](#req-04)). ?id is the deep-link target for [REQ-07](#req-07) and [REQ-16](#req-16).
    6. Each tab has an empty state. At 375px the rows stack and there are no hover-only controls.
    7. Refresh every 15 s while the document is visible. [REQ-17](#req-17) later swaps in useRequestsFeed.
    8. Settings.tsx:789: make 'Requests page' a <Link to="/requests">.
  - **Files:** `internal/requests/service.go`, `internal/httpapi/requests.go`, `internal/httpapi/server.go`, `web/src/pages/Requests.tsx`, `web/src/App.tsx`, `web/src/lib/nav.ts`, `web/src/components/UserLayout.tsx`, `web/src/lib/api.ts`, `web/src/pages/Settings.tsx`
  - **Acceptance:**
    - /requests is reachable from the staff sidebar and from the requester navigation, including external sessions. Tab counts match the server's counts.
    - Selecting 5 pending rows and pressing 'Approve 5' approves all five. A failure on one row is reported on that row only.
    - A requester sees only their own and followed requests.
    - Tab, filters and an open request survive a reload because they live in the URL.
    - Each tab has an empty state, and the page works at 375px with no hover-only controls.
    - The Settings Overseerr import copy links to the page.
  - **Tests:** Go: TestBulkApproveReportsPerItem (an unknown id gives ok:false; the others are approved).; Go: TestBulkRequiresManager (a requester gets 403).; Go: TestBulkUsesSearchQueue (fake searcher; no more than 2 concurrent).; UI check: tabs, select-all plus bulk approve, requester scoping, phone width in the mobile preset.
  - **Depends on:** [REQ-01](#req-01), [REQ-03](#req-03), [REQ-04](#req-04), APP — requester shell navigation (bottom tab) may restyle the nav entry, FE — component kit (optional)
  - **Risk:** Tracking depends on the download client. A Queue failure must still show file-based stages and never fail the page. Use the existing chip, badge and modal styles to avoid visual drift.
  - **Resolves:** discover-1, frontend-10, product-2
<a id="req-06"></a>
- [ ] **REQ-06 · Discover strip: tap-safe posters, pending first for staff, capped, 'See all', followed requests, and a note when requesting** — `P1` · `S` · Phase 6
  - **Problem:** The strip has four problems:
- Approve, Decline and Withdraw sit in an opacity-0 overlay (Discover.tsx:637-652), so a blind tap on a phone can approve or decline.
- STAGE_ORDER puts pending (6) after downloading, importing, queued, paused, failed and searching (Discover.tsx:531-534), so staff scroll past in-flight items to find what needs them.
- It loads every request ever made every 8 s with no visibility check (Discover.tsx:547-552).
- Requesters can't attach a note although the API accepts one (requests.go:55), and followed requests never appear.
  - **Approach:** 1. MyRequestsRow loads section=strip ([REQ-03](#req-03)).
       - Staff header: 'Requests · 3 waiting' with 'See all →', linking to /requests?tab=needs when anything is waiting, else /requests.
       - Requester header: 'Your requests' with 'See all →'.
    2. Staff use the server order (pending oldest first, then in progress). Requesters keep the moving-first STAGE_ORDER from lib/requestStage.ts.
    3. RequestPoster:
       - Delete the hover overlay (Approve, Decline, Withdraw).
       - The poster becomes a <button> that opens RequestSheet ([REQ-04](#req-04)).
       - Keep the always-visible caption. Add a 'Following' chip for relation=subscriber.
    4. Polling:
       - every 8 s only while some item is downloading, importing or queued and document.visibilityState is 'visible'
       - otherwise every 60 s while visible
       - paused while hidden, via a visibilitychange listener
    5. RequestDetailModal gets a collapsed 'Add a note for the admin (optional)' textarea (max 500 chars), sent as note. handleCreateRequest trims the note and rejects more than 500 chars with 400.
    6. Subscribe toast: 'You're following this request — it's in your requests now.'
  - **Files:** `web/src/pages/Discover.tsx`, `web/src/lib/requestStage.ts`, `web/src/lib/api.ts`, `internal/httpapi/requests.go`
  - **Acceptance:**
    - As admin, pending requests are the first posters in the strip and the header shows how many are waiting, with a working 'See all'.
    - At 375px with touch emulation, tapping anywhere on a request poster opens its sheet. No tap approves, declines or withdraws.
    - A requester's note shows in the admin's sheet and on the Requests page.
    - With nothing in flight, the strip refreshes about once a minute (network log). A hidden tab makes no calls.
    - A followed request appears in the follower's strip with a 'Following' chip.
  - **Tests:** UI check (mobile preset, touch): tapping poster corners issues no approve, decline or delete call.; UI check: sort order for staff vs requester; note round-trip.; Go: TestCreateRejectsLongNote.
  - **Depends on:** [REQ-03](#req-03), [REQ-04](#req-04), [REQ-05](#req-05)
  - **Risk:** Low. Make sure the hero and the rest of Discover look the same. The MediaCard quick-request overlay is fixed by APP/FE's tap-safety task, not here (except the series path in REQ-13).
  - **Resolves:** product-2, product-7, frontend-4, discover-3, discover-9
<a id="req-07"></a>
- [ ] **REQ-07 · Tell staff a request is waiting: request.created, an Apprise 'New request' toggle, staff push and inbox, and a pending badge** — `P1` · `M` · Phase 6
  - **Problem:** Service.Create (service.go:95-113) only logs. The admin notifier subscribes only to release.grabbed, movie.downloaded, series.imported, plex.stream.started and plex.buffering (notify.go:148-152). Nothing alerts staff, puts a request in their inbox, or shows a badge, so family requests sit until the owner happens to open Discover. Separately, realtime.Hub.Run broadcasts every bus event to every websocket, requesters included (hub.go:63-84). A detailed request event would leak other people's titles and names.
  - **Approach:** 1. Events from requests.Service:
       - request.created {id, media_type, tmdb_id, title, year, requested_by_name, note, rerequest} when Create inserts a new pending row or re-opens a declined one. Never on subscribe, auto-approve or Silent.
       - request.decided {id, status} from Approve and Decline.
       - A payload-free requests.changed {} on every status change: create, re-open, approve, decline, delete, subscribe, unsubscribe.
    2. Keep details off requesters' sockets. realtime.Hub.Run skips topics with the prefixes 'request.' and 'issue.' through a small deny list whose comment points to SEC. requests.changed still passes. Once SEC's role-filtered hub lands, move the rule into its topic table.
    3. Apprise:
       - Migration NNNN_notify_on_request.sql: ALTER TABLE notifications ADD COLUMN on_request INTEGER NOT NULL DEFAULT 0;
       - notify.Connection.OnRequest goes through cols, scanConn, Create and Update. subscribes('request') returns OnRequest.
       - Run subscribes to request.created and calls fan(ctx, 'request', 'New request', '<name> requested <title> (<year>). It's waiting for your approval.'), adding the note when present and 're-requested' when the flag is set.
       - Insights.tsx connection form: add {key:'on_request', label:'New request (needs approval)'}; api.ts NotificationConn gains on_request.
       - If OBS's event catalog has replaced the per-column toggles, register request.created there instead.
    4. Staff inbox and Web Push:
       - requests.Service.SetStaffLister(func(ctx) ([]int64, error)), wired in main.go from auth ListUsers (role manager or above, not disabled).
       - For each staff id other than the requester: repo.addUserNotification(uid, 'New request', body, media_type, 'request:<id>:new:<unix>') and push.SendToUserAsync(uid, title, body, '/requests?tab=needs').
       - Respect per-user notification preferences once they exist.
    5. Badge:
       - GET /api/v1/requests/pending-count (requireRole RoleManager) returns {pending} from SELECT COUNT(*) FROM requests WHERE status='pending'.
       - Sidebar.tsx shows a terracotta count pill on the Requests nav item ([REQ-05](#req-05)). It refetches on the useLive topic requests.changed and every 60 s while the document is visible.
    6. NotificationBell.clickItem: refs starting 'request:' open /requests?id=<id>.
  - **Files:** `internal/requests/service.go`, `internal/requests/usernotify.go`, `internal/realtime/hub.go`, `internal/realtime/hub_test.go`, `internal/notify/notify.go`, `internal/store/migrations/NNNN_notify_on_request.sql (new)`, `internal/httpapi/requests.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`, `web/src/pages/Insights.tsx`, `web/src/lib/api.ts`, `web/src/components/Sidebar.tsx`, `web/src/components/NotificationBell.tsx`
  - **Acceptance:**
    - A requester without auto-approve requests a movie. Within seconds, every Apprise connection with 'New request' ticked receives exactly one message.
    - Each admin or manager with push enabled gets a Web Push that opens /requests?tab=needs, and their bell shows the entry.
    - The sidebar badge shows the pending count and drops as soon as a request is approved or declined.
    - Auto-approved requests and subscriptions alert nobody. A re-request of a declined title does alert.
    - A requester's websocket never receives a request.created frame (it does receive requests.changed).
  - **Tests:** Go: TestCreatePublishesRequestCreated (exactly one event on create and on re-open; none on subscribe, auto-approve or Silent).; Go: TestStaffAlertInbox (manager and admin get a row; the requester and disabled users don't; the ref is idempotent).; Go: TestConnectionSubscribesRequest, plus a migration check that a fresh store.Open has notifications.on_request.; Go: TestPendingCountRequiresManager (a requester gets 403).; Go (realtime): TestHubDropsRequestTopics.
  - **Depends on:** [REQ-01](#req-01), [REQ-05](#req-05), SEC — role-filtered websocket topics (the local deny list covers the gap until then), OBS — notification event catalog, if it lands first
  - **Risk:** The deny list is a stop-gap and must not swallow requests.changed (note the 'request.' vs 'requests.' prefixes). Staff alert refs include a timestamp so a re-request alerts again. Keep the payload free of anything a requester shouldn't see.
  - **Resolves:** discover-1

#### Milestone: M2 — Decisions with context, safer defaults

_Declines carry a reason that the requester sees. Notes reach staff. A re-request of a declined title must say why and is flagged. Auto-approve is set per media type, and new Plex sign-ins auto-approve movies only._

<a id="req-08"></a>
- [ ] **REQ-08 · Decisions with context: decline reasons, the decider, requester notes for staff, and a flagged re-request** — `P2` · `M` · Phase 6
  - **Problem:** Decline(ctx, id) takes no reason, and the handler reads no body (service.go:241-252, requests.go:133-147), so the notification only says 'was declined' (usernotify.go:198). Approve records no decider. A declined card reads 'Declined / Declined' (Discover.tsx:694-695) and offers 'Request again' immediately (Discover.tsx:1321). The re-request silently resurrects the row. Decision refs are de-duplicated per (user, ref), so a second decline of the same title notifies nobody.
  - **Approach:** 1. Migration NNNN_request_decisions.sql:
       - requests ADD decline_reason TEXT NOT NULL DEFAULT ''
       - decided_by INTEGER NOT NULL DEFAULT 0
       - decided_by_name TEXT NOT NULL DEFAULT ''
       - decided_at INTEGER NOT NULL DEFAULT 0
       - rerequest INTEGER NOT NULL DEFAULT 0
       Repo cols and scan, and the Request JSON exposes all five.
    2. Decline(ctx, id, DeclineOptions{Reason, DecidedBy, DecidedByName}). Approve records decided_by and decided_at too.
       - handleDeclineRequest decodes an optional {reason}, trimmed, at most 280 chars, else 400.
       - Bulk decline ([REQ-05](#req-05)) passes the reason.
       - Body: 'Your request for “X” was declined: <reason>'. With no reason it stays 'was declined.'.
    3. Decision refs become unique per decision: requestRef + ':approved:' + decided_at and requestRef + ':declined:' + decided_at, so a second decline after a re-request notifies again. Anything that parses refs uses only the first two parts.
    4. Re-request:
       - Create on a declined row without a note returns 409 {message, code: 'needs_note', decline_reason}.
       - With a note, Resurrect sets pending and rerequest=1 and stores the new note. It keeps decline_reason as the previous reason until the next decision overwrites it.
       - request.created carries rerequest, so staff alerts say 're-requested'.
    5. Frontend:
       - api.ts req() throws an ApiError (extends Error, adds status and the parsed body). Reuse FE's if it already exists.
       - RequestDetailModal on a declined title: show the reason (from the 409, or straight away when the card is declined), a 'Tell them why you'd still like it' box, and the 'Request again' button.
       - RequestSheet decline: a reason textarea with quick picks ('Already available elsewhere', 'Not something we'll add', 'Couldn't find a good copy').
       - Requests page rows and the sheet show the note, the decider, and a 'Re-request' chip with the previous reason.
       - requestStage: the declined detail line is the reason.
       - Notes and reasons always render as text, never HTML.
  - **Files:** `internal/store/migrations/NNNN_request_decisions.sql (new)`, `internal/requests/repo.go`, `internal/requests/service.go`, `internal/requests/usernotify.go`, `internal/httpapi/requests.go`, `web/src/lib/api.ts`, `web/src/pages/Discover.tsx`, `web/src/pages/Requests.tsx`, `web/src/components/RequestSheet.tsx`, `web/src/lib/requestStage.ts`
  - **Acceptance:**
    - Declining with 'Already on Netflix' shows that reason on the requester's card and in their inbox, push and Apprise message.
    - Declining the same title a second time, after a re-request, notifies the requester again.
    - A requester's note is visible to staff on the Requests page, in the sheet, and in the 'New request' alert.
    - 'Request again' on a declined title asks for a note. The resulting pending request shows a 'Re-request' chip with the previous reason.
    - Requests show who approved or declined them and when.
  - **Tests:** Go: TestDeclineStoresReasonAndNotifies (the body includes the reason; over 280 chars returns 400).; Go: TestRepeatDeclineNotifiesAgain (unique refs).; Go: TestReRequestNeedsNote (409 with code and reason without a note; rerequest=1 with one).; Go: TestApproveRecordsDecider.; UI check: decline-reason prompt, re-request flow, chips.
  - **Depends on:** [REQ-01](#req-01), [REQ-04](#req-04), [REQ-05](#req-05), [REQ-07](#req-07), FE — ApiError in api.ts, if FE owns it
  - **Risk:** Old ':approved' and ':declined' inbox rows no longer block anything, which is intended. Keep reason and note lengths bounded. The 409 changes the re-request contract, so ship the server and the UI together.
  - **Resolves:** discover-10, product-2
<a id="req-09"></a>
- [ ] **REQ-09 · Auto-approve per media type, with new Plex sign-ins defaulting to movies only** — `P1` · `M` · Phase 6
  - **Problem:** Auto-approve is one per-user bool (users.auto_approve, migration 0029). Plex sign-ins get it from plex_login_auto_approve, which defaults to true (auth_plex.go:110, settings.go:73, import_overseerr.go:59). Any Plex user the owner shares with therefore has their series requests (every season, until M3) approved and searched immediately.
  - **Approach:** 1. Migration NNNN_auto_approve_types.sql:
       - users ADD auto_approve_movie, auto_approve_series, auto_approve_book INTEGER NOT NULL DEFAULT 0, then UPDATE users SET each = auto_approve. Every existing user keeps their behaviour. The old column stays for rollback but is no longer read.
       - Seed the setting: INSERT OR IGNORE INTO settings(key, value) SELECT 'plex_login_auto_approve_types', CASE WHEN lower(value) IN ('true','1') THEN 'movie,series,book' ELSE '' END FROM settings WHERE key='plex_login_auto_approve'. SetBool writes strconv.FormatBool.
       - When the key is absent, the Go default is 'movie'.
    2. internal/auth:
       - User gets AutoApproveMovie, AutoApproveSeries and AutoApproveBook (json auto_approve_movie and so on).
       - Keep AutoApprove bool (json auto_approve), meaning all three, for older clients.
       - Add func (u User) AutoApproves(mediaType string) bool.
       - Update every scan path together (service.go ~96, 119, 142, 185, 205, 242, 311, 368, 414): CreateUser, FindOrCreatePlexUser, UpdateUser, ListUsers, the user lookup, Authenticate and both session queries.
    3. handleCreateRequest passes CreateOptions{AutoApprove: u.AutoApproves(in.MediaType)}. handleListRequests also returns the viewer's auto_approve_types.
    4. auth_plex.go:110 and import_overseerr.go:59 read plex_login_auto_approve_types (CSV into the three flags).
    5. users.go create and update accept auto_approve_movie, auto_approve_series and auto_approve_book. The legacy auto_approve bool still sets all three.
    6. Settings.tsx:
       - Users editor: three checkboxes, 'Auto-approve: Movies / Series / Books'. The list chip shows which types are on.
       - Plex sign-in section: the single toggle becomes the same three, defaulting to Movies, with a hint that series requests can pull many seasons.
       - settings.go GET and PUT carry the key.
       - The users list flags existing Plex users who still auto-approve series, so the owner can tighten them.
    Decision: system.t14's 'keep all types for installs already using Plex sign-in' is narrowed. Only an owner who explicitly turned the toggle on keeps all types. Untouched installs move new sign-ins to movies only, and the release note and Settings hint say so.
  - **Files:** `internal/store/migrations/NNNN_auto_approve_types.sql (new)`, `internal/auth/service.go`, `internal/auth/service_test.go`, `internal/httpapi/auth_plex.go`, `internal/httpapi/import_overseerr.go`, `internal/httpapi/requests.go`, `internal/httpapi/users.go`, `internal/httpapi/settings.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A first-time Plex sign-in, with the setting never touched, gets movies auto-approved. Their series request lands in Needs approval.
    - Existing users keep exactly their current behaviour after the migration, with all three flags set from the old one.
    - An admin can set Movies-only, Series-only or Books-only per user, and the next request follows it.
    - If the owner had explicitly enabled 'Auto-approve their requests', new Plex sign-ins keep all three types.
  - **Tests:** Go: migration test (old auto_approve=1 becomes 1/1/1 and 0 becomes 0/0/0; the settings key is seeded correctly for true, false and absent).; Go: TestAutoApprovesByMediaType (handler: a movie-only user's series request stays pending; their movie request is approved).; Go: TestFindOrCreatePlexUserDefaults (a new Plex user gets movie-only by default).; UI check: users editor and Plex sign-in checkboxes round-trip.
  - **Depends on:** [REQ-01](#req-01), CFG — if the People & access page is rebuilt first, put the three checkboxes there
  - **Risk:** auth.User is scanned in many places, including session lookups, so every path must change together or users silently lose auto-approve. Existing Plex users keep series auto-approve; surface that in the users list.
  - **Resolves:** discover-2, system-6

#### Milestone: M3 — Series by season, with fair-use quotas

_Approving a title already in the library actually monitors and searches it. Requesters pick seasons, ask for 'more seasons' of shows the server partly has, and get per-season 'ready' notices. Staff can trim seasons when approving. Optional per-user quotas count movies, seasons and books, and refund on withdraw or decline._

<a id="req-10"></a>
- [ ] **REQ-10 · Approving a title already in the library monitors and searches it, and series adds become season-aware** — `P1` · `M` · Phase 6
  - **Problem:** Approve does nothing for media already in the library (series.ErrExists, movies.ErrExists, books.ErrExists; requests/service.go:183-218). A show added unmonitored by a library scan stays unmonitored and unsearched. Approval always calls series.Add(…, true), and seasonsFromDetails then monitors every non-special season (series/service.go:158-186), so there is no way to add only some seasons. On Discover, any in-library title without a file reads 'Wanted' and hides Request (Discover.tsx:1062, 1078, 1112), even when nobody monitors it, so requesters cannot ask for a show the scan picked up.
  - **Approach:** 1. series package:
       - AddOptions{Monitored bool; Seasons map[int]bool (nil = every non-special season); MonitorNewSeasons *bool}.
       - AddWithOptions(ctx, tmdbID, profile, opts). Add delegates with nil seasons.
       - seasonsFromDetails takes a predicate: season n is monitored iff Monitored && n > 0 && (Seasons == nil || Seasons[n]), and episodes follow their season.
       - MonitorNewSeasons is stored only if SER's monitor-new-seasons field exists. Otherwise new seasons keep today's 'inherit the series flag' behaviour; document this.
    2. series.Service.EnsureMonitored(ctx, seriesID, seasons []int /* nil = all non-special */, reason string):
       - New Repo.SetSeriesFlag sets series.monitored=1 on the series row only, without Repo.SetMonitored's cascade.
       - Then SetSeasonMonitored(true) for each wanted season.
       - Add the series event 'Monitored by request from <name>: S1–2'.
       - Coordinate with SER if it has already replaced the cascade.
    3. requests.Approve on ErrExists:
       - Movie with no file: SetMonitored(true), then enqueue SearchMovie.
       - Series: EnsureMonitored (nil for whole-show requests), then enqueue SearchSeriesNow, which searches only monitored, aired, missing episodes.
       - Book with no file: same as movies, with SearchBookNow.
       - Items that are already complete are just marked approved, and the ready path stamps them.
    4. Discover:
       - buildDiscoverSnapshot adds movWanted and serWanted (in library, monitored, missing files), and discoverCard gains wanted bool.
       - badgeFor shows 'Wanted' only when wanted is true. An in-library item that is unmonitored and has no file shows no badge and keeps its Request button. Monitoring state is otherwise not exposed.
       - A show with files still reads 'In library'; [REQ-13](#req-13) adds 'Request more seasons'.
       - series.t9's monitor-preset select is superseded by [REQ-13](#req-13)'s season checklist and its quick chips.
  - **Files:** `internal/series/service.go`, `internal/series/repo.go`, `internal/series/service_test.go`, `internal/requests/service.go`, `internal/requests/deps.go`, `internal/requests/service_test.go`, `internal/httpapi/discover.go`, `web/src/pages/Discover.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Approving a request for a library-scanned, unmonitored show monitors it and starts a search, visible in its History as 'Monitored by request from <name>'.
    - Approving a request for an in-library movie with no file monitors it and searches.
    - AddWithOptions with Seasons {1,2} monitors only S1-2 and their episodes, never specials.
    - A requester sees an unmonitored, fileless in-library title with a Request button instead of 'Wanted'.
  - **Tests:** Go (series): TestAddWithOptionsMonitorsOnlyChosenSeasons.; Go (series): TestEnsureMonitoredDoesNotCascade (other seasons' flags are untouched).; Go (requests): TestApproveExistingUnmonitoredSeriesMonitorsAndSearches (a stub searcher records SearchSeriesNow).; Go (requests): TestApproveExistingMovieWithoutFileSearches.; UI check: Discover badge and Request button for an unmonitored in-library title.
  - **Depends on:** [REQ-01](#req-01), SER — non-cascading series monitor flag and monitor-new-seasons (series.t8); coordinate if SER lands first
  - **Risk:** Don't route this through Repo.SetMonitored, or seasons the owner deliberately skipped get re-monitored. Requester-facing labels must not reveal admin state beyond 'In library' and 'Wanted'.
  - **Resolves:** series-6, discover-2
<a id="req-11"></a>
- [ ] **REQ-11 · Season data: TMDB season summaries and a per-season state endpoint** — `P1` · `S` · Phase 6
  - **Problem:** Nothing tells the requester side which seasons exist, which are on disk, or which can be asked for. MediaDetail has no seasons, and tmdbSeries.Seasons doesn't parse episode_count (metadata/tmdb.go:281-287). The only signal is has_file, which is true once any episode exists (discover.go:99), so a partly owned show reads 'In library'.
  - **Approach:** 1. metadata/tmdb.go:
       - tmdbSeries.Seasons gains EpisodeCount (json episode_count).
       - MediaDetail gains Seasons []SeasonSummary{Number, Name, EpisodeCount, AirDate, PosterURL} for series, with season 0 excluded.
       - Fill it in seriesDetail from the /tv/{id} payload it already fetches, so there are no extra TMDB calls.
    2. series.Service.SeasonStates(ctx, tmdbID) returns {Have, Aired, Monitored} per season via [REQ-02](#req-02)'s SeasonProgress.
    3. New endpoint GET /api/v1/media/series/{tmdb}/seasons:
       - a.protected, under the allowlisted /api/v1/media/ prefix. Its path is longer than /api/v1/media/{media}/{id}, so the two don't clash.
       - returns {seasons: [{number, name, episode_count, air_date, have, aired, state}]}
       - state is one of:
         - in_library: have >= aired > 0
         - partial: 0 < have < aired
         - on_the_way: monitored and missing, so the server will grab it
         - unaired: nothing has aired yet
         - requestable: anything else
       - [REQ-12](#req-12) adds the 'requested' state.
       - Requesters never see monitored flags or names, only the state.
    4. api.ts: seriesSeasons(tmdb) and a SeasonState type.
  - **Files:** `internal/metadata/tmdb.go`, `internal/metadata/discover.go`, `internal/series/service.go`, `internal/httpapi/discover.go`, `internal/httpapi/server.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - For a show with S1-3 on disk and S4 missing and unmonitored, the endpoint returns in_library for S1-3 and requestable for S4.
    - For a show not in the library, aired seasons are requestable and a future season is unaired.
    - The response contains no usernames and no monitored flags.
  - **Tests:** Go: TestSeasonSummariesParsed (episode_count, specials dropped).; Go: TestSeasonStates (handler table test across all states with a fake library).
  - **Depends on:** [REQ-02](#req-02), INT/BE — TMDB detail cache (draft discover.t23), so each sheet open doesn't cost a TMDB call
  - **Risk:** Anime shows numbered absolutely still list TMDB seasons, which matches how the library stores them. Without the detail cache, each sheet open costs one TMDB detail call.
  - **Resolves:** discover-2, product-3
<a id="req-12"></a>
- [ ] **REQ-12 · Season-scoped series requests: store seasons, monitor only those, allow 'more seasons', notify per season** — `P1` · `L` · Phase 6
  - **Problem:** A series request is the whole show. The create body has no seasons (requests.go:46-57), and Approve monitors every season (service.go:192). idx_requests_tmdb allows only one request per show (migration 0034), so nobody can ask for a missing or newly aired season of a show the server partly has. 'Ready' waits until no monitored aired episode is missing anywhere (usernotify.go:133-140, 289-294), so one unfindable old episode means the requester never hears.
  - **Approach:** Data model:
    1. Migration NNNN_request_seasons.sql:
       - ALTER TABLE requests ADD COLUMN seasons TEXT NOT NULL DEFAULT ''. It holds a JSON int array; '' means the whole show, which every legacy row and an 'All seasons' ask use.
       - DROP INDEX idx_requests_tmdb.
       - CREATE UNIQUE INDEX idx_requests_movie ON requests(tmdb_id) WHERE media_type='movie'.
       - CREATE INDEX idx_requests_series ON requests(tmdb_id) WHERE media_type='series'.
       Migration 0034 already made these partial indexes, so no table rebuild is needed.
    2. Request gains Seasons []int (json seasons,omitempty), encoded in repo cols, scan and Create. Add Repo.ListByMedia(ctx, 'series', tmdb). GetByMedia stays for movies; its two series callers (lookupExisting and notifyRequester) move to ListByMedia.
    
    Creating a series request:
    3. The handler passes in.Seasons plus the TMDB season list from the detail it already fetches for canonicalisation (requests.go:72-73). Create runs under a service mutex.
    4. Work out what is new:
       - Drop unknown seasons and specials.
       - requested = the list, or every known season for 'All seasons'.
       - covered = the union over pending and approved rows ('' covers everything), plus the seasons already complete on disk.
       - remainder = requested minus covered.
    5. What gets stored:
       - Empty remainder: subscribe the caller to every row that covers a requested season, and return subscribed=true.
       - Otherwise: insert a row for the remainder. It is '' only when the caller asked for the whole show and nothing was covered.
       - When a new row is inserted, also subscribe the caller to the rows that covered the rest, and add the requesters of overlapping declined rows as subscribers.
       - A declined row whose seasons equal the new ask is re-opened instead, under [REQ-08](#req-08)'s needs_note rule.
       - Movies and books keep the single-row path.
    
    Approving:
    6. ApproveOptions.Seasons must be a subset of the row's seasons.
       - Staff may trim. row.seasons is rewritten, and the notification names what was approved and what wasn't.
       - New show: AddWithOptions with the season set. MonitorNewSeasons is false for season-scoped rows and true for '' rows.
       - Existing show: EnsureMonitored(seasons), then an enqueued SearchSeriesNow ([REQ-10](#req-10)).
    
    Tracking and ready notices:
    7. Season-scoped rows compute Have/Total over their own seasons with SeasonProgress. '' rows use MonitoredProgress ([REQ-02](#req-02)).
    8. RunNotifier (series.imported) and SweepReadyRequests iterate ListByMedia approved rows with ready_at=0. A row is ready when it is complete over its own seasons.
       - The ref is 'series:<tmdb>:r<id>' for season-scoped rows. Legacy '' rows keep 'series:<tmdb>', so nobody is re-notified.
       - Rows with two or more seasons also send 'Season N of “X” is ready' once per season as each completes (ref 'series:<tmdb>:r<id>:s<N>'), unless the whole row completes in the same pass.
    
    Discover and API:
    9. discoverSnapshot.reqStatus aggregates several rows per show (approved > pending > declined). [REQ-11](#req-11)'s seasons endpoint adds the 'requested' state with {request_id, status, mine}. requested_by_name is included for staff only.
    10. The create body accepts seasons: number[], ignored for movies and books. The Overseerr import keeps ''.
  - **Files:** `internal/store/migrations/NNNN_request_seasons.sql (new)`, `internal/requests/repo.go`, `internal/requests/service.go`, `internal/requests/usernotify.go`, `internal/requests/progress.go`, `internal/requests/seasons_test.go (new)`, `internal/series/service.go`, `internal/httpapi/requests.go`, `internal/httpapi/discover.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Requesting S4 of a show whose S1-3 are on disk creates a pending request for [4]. Approving it monitors only S4 episodes, leaves S1-3 monitoring untouched, and searches.
    - A brand-new show requested for [1,2] has only S1-2 monitored after approval.
    - A second user requesting [2] is subscribed to the first request. Requesting [2,3] subscribes them to [2] and creates a new request for [3].
    - Staff can approve [1,2] of a [1,2,3] request. The requester is told S3 wasn't approved.
    - 'Season 4 of “X” is ready' fires when S4 is complete, even if older seasons have gaps. A [1,2] request sends per-season notices and one final 'ready'.
    - Legacy whole-show requests track and notify exactly as before, with no duplicate 'ready' after the migration.
    - Movie requests are still unique per tmdb_id.
  - **Tests:** Go: TestCreateSeriesSeasonsOverlap (table test of the covered, remainder, subscribe and new-row outcomes, including declined rows).; Go: TestCreateSeriesConcurrent under -race (parallel creates for one show never double-cover a season).; Go: TestApproveMonitorsOnlyRequestedSeasons and TestApproveTrimsSeasons (fake series adder and search queue).; Go: TestReadyPerSeasonRequest (new ref formats; the legacy ref is unchanged).; Go: migration test (series uniqueness is gone; a duplicate movie insert still fails). Run go test -race in Docker before pushing.
  - **Depends on:** [REQ-01](#req-01), [REQ-02](#req-02), [REQ-10](#req-10), [REQ-11](#req-11), SER — monitor-new-seasons, so season-scoped shows don't auto-grab future seasons
  - **Risk:** Dropping series uniqueness touches every per-show lookup (lookupExisting, notifyRequester, the snapshot's reqStatus, attachToExisting), so audit them all. The migration must leave legacy rows intact. Until SER's monitor-new-seasons exists, new seasons appearing on a refresh follow today's behaviour; document this. Overlap logic is the subtle part, so keep it in one pure function with a table test.
  - **Resolves:** discover-2, product-3
<a id="req-13"></a>
- [ ] **REQ-13 · Season picker in the title sheet, 'Request more seasons', series quick-request opens the sheet, and season trimming on approve** — `P1` · `M` · Phase 6
  - **Problem:** Every card has a hover '+ Request' that fires without opening the sheet (Discover.tsx:1082-1088, 1112-1122), and the Hero quick-request does the same, so a series is requested whole in one click. An owned series shows only '✓ In your library' (Discover.tsx:1311), a dead end even when seasons are missing. Staff have no way to approve only part of a series request.
  - **Approach:** 1. RequestDetailModal for series fetches api.seriesSeasons and renders a checklist.
       - Each row shows name, episode count, year and a state chip: 'In library ✓', '4 of 10', 'Requested', 'On the way', 'Not out yet'.
       - Only requestable seasons get a checkbox.
       - Quick chips: 'All missing', 'Latest season', and 'All seasons' (the whole show, sends seasons: []).
       - Nothing is pre-selected. The button reads 'Request 2 seasons' and is disabled at zero.
       - The list scrolls inside the sheet for long shows.
    2. An in-library series with any requestable season shows 'Request more seasons' instead of '✓ In your library'.
    3. MediaCard and Hero quick-request open the sheet for series instead of calling doRequest. Movie quick-request is unchanged.
    4. requestStage.ts gets formatSeasons([1,2,3,5]) → 'S1–3, S5', with '' reading 'All seasons'. It appears on strip cards, Requests page rows and the sheet.
    5. RequestSheet for staff on a pending series request:
       - The request's seasons appear as ticked checkboxes. Unticking trims them before Approve: approveRequest(id, {quality_profile, seasons}).
       - This replaces series.t9's monitor-preset select.
    6. api.ts: createRequest gains seasons; MediaRequest gains seasons.
  - **Files:** `web/src/pages/Discover.tsx`, `web/src/pages/Requests.tsx`, `web/src/components/RequestSheet.tsx`, `web/src/lib/requestStage.ts`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Opening a series sheet lists its seasons with their library and request state. Requesting S2 and S4 creates one request with seasons [2,4].
    - A show with S1-3 on disk and S4 missing shows 'Request more seasons'. The request covers only S4.
    - Tapping or clicking '+ Request' on a series card opens the sheet, and no request is created.
    - Request cards and Requests page rows show 'S1–3, S5' style summaries.
    - Staff can untick a season in the approve sheet, and only the ticked seasons are monitored.
  - **Tests:** UI check: season states for a show not in the library, a partly owned show and a fully owned show.; UI check: series quick-request opens the sheet; movie quick-request still works on desktop.; UI check: a 35-season show scrolls within the sheet at 375px.; npm run build passes.
  - **Depends on:** [REQ-11](#req-11), [REQ-12](#req-12), [REQ-04](#req-04), [REQ-05](#req-05), APP — title routes (draft discover.t3) if quick-request should navigate rather than open the modal
  - **Risk:** Each sheet open costs one seasons call (one TMDB detail call unless it is cached). Keep the requester view free of other people's names.
  - **Resolves:** discover-2, product-3, series-6
<a id="req-14"></a>
- [ ] **REQ-14 · Per-user request quotas: movies, seasons and books per N days, refunded on withdraw or decline** — `P2` · `M` · Phase 6
  - **Problem:** There is no quota code anywhere in requests; the only quota grep hits are in subtitles. A trusted or auto-approved requester can queue unlimited titles and seasons. Overseerr users expect to see something like 'You have 3 requests left this week'.
  - **Approach:** 1. Migration NNNN_request_quotas.sql:
       - CREATE TABLE request_usage (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, request_id INTEGER NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('movie','season','book')), units INTEGER NOT NULL, created_at INTEGER NOT NULL);
       - CREATE INDEX idx_request_usage_user ON request_usage(user_id, created_at);
       - users ADD quota_movies, quota_seasons, quota_books INTEGER NOT NULL DEFAULT -1. -1 means the global default, 0 means unlimited, n is a limit.
    2. Global settings:
       - request_quota_days (7) and request_quota_movies, _seasons and _books, all defaulting to 0 (unlimited) so nothing changes on upgrade.
       - A 'Requests' section in Settings with plain copy and a 'Typical household' button (10 movies, 5 seasons, 10 books per week).
    3. internal/requests/quota.go:
       - Check(ctx, userID, kind, units) error returns ErrQuotaExceeded{Kind, Limit, Used, ResetsAt}. ResetsAt is the oldest usage in the window plus the window length.
       - Staff (manager and above) and Silent imports are exempt via CreateOptions.QuotaExempt.
       - Following someone else's request is free.
    4. Create:
       - Check runs before the insert, inside the same mutex as [REQ-12](#req-12)'s overlap logic.
       - Units: 1 per movie, 1 per book, or len(remainder) seasons. 'All seasons' counts the known non-special seasons.
       - The ledger row is inserted after a successful create or re-open.
       - Withdraw (Delete), Decline and staff season trimming delete or reduce that request's rows in the same code path.
    5. handleCreateRequest maps ErrQuotaExceeded to 429 {message, kind, limit, used, resets_at}, for example 'You've used your 10 movie requests this week. The next one frees up Tue 14 Oct.'
    6. GET /api/v1/me/quota (protected; /me/ is allowlisted) returns {days, movie:{limit, used, resets_at}, season:{…}, book:{…}}.
    7. UI:
       - The detail sheet shows '3 movie requests left this week' under the button. At zero the button is disabled and shows the reset date.
       - The season picker can't select more seasons than are left.
       - The Requests page header shows the quota for requesters.
       - The users editor gets three optional quota fields (blank = default, 0 = unlimited).
  - **Files:** `internal/store/migrations/NNNN_request_quotas.sql (new)`, `internal/requests/quota.go (new)`, `internal/requests/quota_test.go (new)`, `internal/requests/service.go`, `internal/auth/service.go`, `internal/httpapi/requests.go`, `internal/httpapi/usernotify.go`, `internal/httpapi/users.go`, `internal/httpapi/settings.go`, `internal/httpapi/server.go`, `web/src/pages/Discover.tsx`, `web/src/pages/Requests.tsx`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With a 2-movies-per-7-days quota, a requester's third movie request returns 429 with the reset date, and the sheet shows 0 left.
    - Withdrawing a pending request, or having it declined, gives the unit back immediately.
    - Following an existing request never uses quota, and staff are never limited.
    - Requesting 3 seasons with 2 left is refused with a message saying 2 seasons are left.
    - On upgrade every quota is unlimited until the owner sets one.
  - **Tests:** Go: TestQuotaWindow (usage outside the window doesn't count; ResetsAt is right).; Go: TestQuotaRefundOnWithdrawDeclineAndTrim.; Go: TestQuotaExemptStaffImportAndSubscribe.; Go: TestCreateReturns429 (handler message, status and fields).; UI check: sheet counter, disabled state, users editor overrides.
  - **Depends on:** [REQ-08](#req-08), [REQ-09](#req-09), [REQ-12](#req-12), CFG — People & access page, if it hosts per-user fields
  - **Risk:** Ledger rows must be removed in the same Delete, Decline and trim paths, or refunds leak. Check and insert must happen under the create mutex, or two parallel requests could both pass. Leaving the defaults unlimited avoids surprising existing households.
  - **Resolves:** discover-2, system-6

#### Milestone: M4 — Ready means watchable in Plex

_'Ready' is sent only once Plex has the title, with a 30-minute fallback, and cards show 'Adding to Plex…' meanwhile. Notifications, ready cards and the title sheet open the item in Plex. Request views update live over the websocket instead of polling. M4 can run in parallel with M2 and M3 once PLEX's locator has landed._

<a id="req-15"></a>
- [ ] **REQ-15 · Send 'ready' only once Plex has the title, with an 'Adding to Plex…' stage and a grace-period fallback** — `P1` · `M` · Phase 7
  - **Problem:** A request counts as ready when the file is on disk (service.go:259-304; usernotify.go:262-296). notifyReady then immediately says '“X” is ready to watch.' (usernotify.go:184), on the import event (usernotify.go:115-148) or in the sweep. Nothing checks Plex. A family member opens Plex straight away and the title isn't there yet. The Plex client has only GET helpers and no lookup by GUID (client.go:65-165).
  - **Approach:** 1. Locator interface. Add requests.PlexLocator, nil-safe like PushSender:
       - Configured(ctx) bool
       - Locate(ctx, mediaType string, tmdbID, tvdbID int, imdbID, title string, year int) (ratingKey string, found bool, err error)
       - WatchURL(ctx, ratingKey) string
       SetPlexLocator is wired in main.go to PLEX's implementation.
    2. Fallback locator, only if PLEX hasn't shipped one. Add internal/plex/locate.go:
       - For each section of the matching type, GET /library/sections/{key}/all?type=1|2&title=<title>&includeGuids=1.
       - Match Guid[].id against tmdb://, tvdb:// and imdb://, and the legacy guid attribute against com.plexapp.agents.themoviedb://, thetvdb:// and imdb://.
       - Fall back to an exact title+year match when an item has no guids.
       - WatchURL = https://app.plex.tv/desktop/#!/server/<machineIdentifier>/details?key=%2Flibrary%2Fmetadata%2F<ratingKey>, with the machine id from Identity (cached).
       - Expose it through insights.Service so the token stays in the Insights config.
    3. Migration NNNN_request_plex_ready.sql: requests ADD on_disk_at INTEGER NOT NULL DEFAULT 0 and plex_rating_key TEXT NOT NULL DEFAULT ''.
    4. notifyReady for movies and series:
       - Locator nil or not configured: send now, as today.
       - Locate finds the item: store plex_rating_key, send '“X” is ready to watch on Plex.', and stamp ready_at.
       - Not found: set on_disk_at (if it is 0) and return.
       - Books are never gated.
    5. A scheduled task 'request-plex-check' (every 2 min, in main.go) runs over approved rows with on_disk_at > 0 AND ready_at = 0:
       - Found: send.
       - now − on_disk_at exceeds request_plex_grace_minutes (a setting, default 30): send '“X” is ready — it may take a few more minutes to show up in Plex.' and log a warning with the ids.
       - If PLEX publishes a library-scanned event, also recheck matching rows on it.
       - The 10-minute ready sweep goes through the same gate.
    6. Track:
       - A new StageAdding ('adding') sits between importing and available, used when files are complete, ready_at = 0 and on_disk_at > 0.
       - Web requestStage: badge 'Almost ready', detail 'Adding to Plex…'. Update the RequestStage union and STAGE_ORDER.
    7. Log Locate misses at Info level with the TMDB and TVDB ids, so a GUID mismatch can be diagnosed.
  - **Files:** `internal/requests/usernotify.go`, `internal/requests/progress.go`, `internal/requests/service.go`, `internal/requests/repo.go`, `internal/requests/plexready_test.go (new)`, `internal/plex/locate.go (new, only if PLEX hasn't added it)`, `internal/plex/locate_test.go`, `internal/insights/service.go`, `internal/store/migrations/NNNN_request_plex_ready.sql (new)`, `internal/httpapi/settings.go`, `cmd/arrmada/main.go`, `web/src/lib/requestStage.ts`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With Plex connected, a requester's 'ready' notice arrives only after the title is visible in Plex.
    - If Plex never matches the title, the notice still arrives after the grace period with the 'may take a few minutes' wording, and a warning is logged.
    - While waiting, the request shows 'Almost ready · Adding to Plex…' on Discover and on the Requests page.
    - Without Plex configured, behaviour is exactly as today. Book requests are unaffected.
    - Each person is notified once per request, including across a restart (state lives in the requests row).
  - **Tests:** Go: TestReadyWaitsForPlex (a fake locator returns not-found then found: no inbox row first, then one row).; Go: TestReadyWithoutPlexImmediate.; Go: TestReadyGraceFallback (on_disk_at 31 min ago → sends the fallback copy once; later checks send nothing).; Go: TestBooksNotGated.; Go: TestTrackAddingStage.; Go (plex): TestFindByTMDBGuid (an httptest section listing with includeGuids returns the rating key for both the new and the legacy guid forms, plus the title+year fallback).
  - **Depends on:** [REQ-01](#req-01), [REQ-02](#req-02), [REQ-12](#req-12), PLEX — Plex locator by GUID and WatchURL (drafts insights.t6, movies.t19, product.t11, backend.t28), and the post-import partial scan so items appear within a minute
  - **Risk:** GUID formats differ between Plex agents, so verify against the owner's libraries. If matching fails, every ready notice waits the full grace period; the Info logs and the 30-minute cap bound the damage. Without PLEX's partial scan, Plex's own watcher decides how long the wait is.
  - **Resolves:** insights-5, product-1, backend-14, movies-9
<a id="req-16"></a>
- [ ] **REQ-16 · Notifications and cards that take you there: deep links, Watch on Plex in push, inbox, ready cards and the title sheet** — `P2` · `M` · Phase 7
  - **Problem:** Every request push opens /discover (usernotify.go:243). Clicking an inbox item runs a Discover title search (NotificationBell.tsx:61-65). An owned title's sheet shows only '✓ In your library' (Discover.tsx:1311). A grep for app.plex.tv or 'Watch on Plex' finds only the OAuth URL. The moment a request becomes ready, no button anywhere takes anyone to it.
  - **Approach:** 1. Migration NNNN_notification_links.sql: user_notifications ADD url TEXT NOT NULL DEFAULT '' and plex_url TEXT NOT NULL DEFAULT ''. The UserNotification JSON exposes both.
    2. notifyParties takes a message struct {Title, Body, Ref, Kind, URL, PlexURL}.
       - Request notifications use URL '/requests?id=<id>', or APP's /discover/<media>/<tmdb> title route once it exists.
       - Ready notices carry PlexURL ([REQ-15](#req-15)).
    3. Push:
       - push.Message{Title, Body, URL, PlexURL}, and Payload gains plex_url.
       - requests.PushSender becomes SendToUserAsync(userID, push.Message). The only caller is usernotify.go:243.
    4. web/public/sw.js:
       - When plex_url is set, showNotification adds the actions [{action:'plex', title:'Watch on Plex'}, {action:'open', title:'Details'}].
       - notificationclick opens plex_url for the 'plex' action or a plain tap on a ready notice, and otherwise opens url.
    5. NotificationBell:
       - clickItem navigates to n.url, falling back to today's title search.
       - Ready items with a plex_url show a 'Watch on Plex' link.
    6. The personal Apprise ready message appends 'Watch: <plex_url>'.
    7. Request JSON exposes plex_url once ready.
       - Ready cards on the strip and in the Requests page Ready tab get a 'Watch' pill.
       - RequestSheet shows '▶ Watch on Plex'.
    8. Title sheet:
       - handleMediaDetail adds plex_url from PLEX's TMDB→rating-key resolver when it exists, otherwise from a request row that has a plex_rating_key.
       - RequestDetailModal shows a primary '▶ Watch on Plex' button (target=_blank, rel=noopener) beside Trailer. It replaces the dead-end badge as the main action.
       - Everything Plex-related is hidden when Plex isn't configured.
  - **Files:** `internal/store/migrations/NNNN_notification_links.sql (new)`, `internal/requests/usernotify.go`, `internal/requests/service.go`, `internal/push/push.go`, `internal/httpapi/discover.go`, `internal/httpapi/usernotify.go`, `web/public/sw.js`, `web/src/components/NotificationBell.tsx`, `web/src/components/RequestSheet.tsx`, `web/src/pages/Discover.tsx`, `web/src/pages/Requests.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Tapping a ready push opens the title in Plex. Tapping 'Details' (where actions are supported) opens the request in Arrmada.
    - Inbox items open the request or the title, not a search. Ready items show 'Watch on Plex'.
    - A requester opening an owned title that Plex has sees '▶ Watch on Plex', which opens the item in Plex.
    - With Plex disconnected, every Watch button is hidden and links fall back to Arrmada.
  - **Tests:** Go: TestInboxRowsCarryURLs (url and plex_url stored for a ready notice).; Go: TestReadyAppriseIncludesPlexLink.; Go (push): TestPayloadIncludesPlexURL.; UI check: push click on Android and desktop; iOS PWA tap opens the Plex URL; bell 'Watch on Plex'; sheet button at 375px.
  - **Depends on:** [REQ-05](#req-05), [REQ-15](#req-15), PLEX — TMDB→Plex rating-key resolver for titles that were never requested (draft discover.t15; optional), APP — Discover title routes (draft discover.t3; optional, for the title URL)
  - **Risk:** Notification actions aren't supported everywhere, so a plain tap on a ready notice must open Plex. app.plex.tv links work for users the owner shares with, but opening the native app on phones depends on the platform.
  - **Resolves:** product-1, discover-5
<a id="req-17"></a>
- [ ] **REQ-17 · Live request feed: a requests.changed nudge, one shared hook, a cached queue, no hidden-tab polling** — `P2` · `S` · Phase 9
  - **Problem:** Request views poll on their own timers. REQ-05 and REQ-06 bound the worst of it, but the strip, the Requests page and the badge still each refresh separately. Each list call can still hit the download client, the strip still waits up to a minute to show an approval made elsewhere, and the existing websocket hub and useLive hook go unused (Discover.tsx:547-552).
  - **Approach:** 1. requests.changed carries no payload. [REQ-07](#req-07) publishes it on status changes. Also publish it:
       - from RunNotifier when release.grabbed, movie.downloaded, series.imported or book.imported concern an approved request with ready_at = 0
       - from the plex-check task when it sends
    2. One cached download queue: a.cachedQueue(ctx) with a 3 s TTL, shared by buildDiscoverSnapshot and handleListRequests.
    3. Collapse concurrent identical list calls with a small mutex-guarded map keyed by (scope, section, limit, offset, media_type, q), holding the tracked page for 3 s. go.mod has no singleflight. Scope is the user id for requesters and 0 for staff, so a requester never gets staff data.
    4. New web/src/lib/useRequestsFeed.ts, one hook used by the Discover strip, the Requests page and the Sidebar badge:
       - Refetch on requests.changed, via useLive or FE's shared websocket when it exists.
       - Poll every 10 s while visible and something is downloading, importing, queued or adding.
       - Otherwise poll every 60 s while visible, and never while hidden.
    5. Remove the ad-hoc timers added in [REQ-05](#req-05), [REQ-06](#req-06) and [REQ-07](#req-07).
  - **Files:** `internal/requests/service.go`, `internal/requests/usernotify.go`, `internal/httpapi/requests.go`, `internal/httpapi/discover.go`, `web/src/lib/useRequestsFeed.ts (new)`, `web/src/lib/useLive.ts`, `web/src/pages/Discover.tsx`, `web/src/pages/Requests.tsx`, `web/src/components/Sidebar.tsx`
  - **Acceptance:**
    - With Discover open and nothing downloading, the network panel shows no /api/v1/requests calls for 60 s, and a hidden tab makes none.
    - Approving a request in another tab updates an open Discover strip and the badge within about 2 s.
    - While something downloads, progress still advances (10 s refresh while visible).
    - Several requests for the same list within 3 s run one List+Track.
  - **Tests:** Go: TestRequestsChangedPublished (on approve, and on movie.downloaded for a requested title).; Go: TestListCacheScopedPerUser (a requester never receives the staff page).; Go: TestCachedQueueTTL.; UI check: polling stops when hidden; websocket-driven refresh.
  - **Depends on:** [REQ-03](#req-03), [REQ-05](#req-05), [REQ-06](#req-06), [REQ-07](#req-07), SEC — once topics are role-filtered, requests.changed must be on the requester allowlist, FE — one shared websocket connection (useLive currently opens one per hook use)
  - **Risk:** useLive opens a websocket per hook instance, so share one connection (FE) or keep the hook in a single provider. The cache must be per scope.
  - **Resolves:** discover-9

#### Milestone: M5 — Discover depth

_Requesters get the payoff: Recently added and Ready for you rows. 'See all' opens filterable infinite grids, search pages past 20 results, and people and collections get their own pages. The adult filter holds on every new surface._

<a id="req-18"></a>
- [ ] **REQ-18 · Requester payoff rows: 'Recently added' and 'Ready for you' on Discover** — `P2` · `M` · Phase 8
  - **Problem:** DiscoverTab has no recently-added or available-now row (Discover.tsx:386-414). Recommended and Because deliberately drop in-library titles (discover_recommended.go:77, discover_rows.go:182). /insights/recently-added is manager-only (server.go:349). Requesters never see what just arrived, and nothing lists what is ready for them.
  - **Approach:** 1. New endpoint GET /api/v1/discover/recently-added (a.protected, every role; /api/v1/discover is allowlisted).
       - Source: Arrmada's own import history. movie_events and series_events rows with event='imported' in the last 30 days, joined to movies and series for poster, overview, year and TMDB id.
       - Deduplicated per show, newest first, at most 40.
       - When PLEX's TMDB index or GUID parsing exists, merge Plex RecentlyAdded(60) mapped to TMDB (episodes via their show), so items added outside Arrmada appear. Unmapped Plex items are dropped.
       - Every title passes adultfilter.Matches. The result goes through enrichCards and is cached for 2 min.
       - No Plex usernames, file paths or library names leave the server.
    2. DiscoverTab adds a PosterRow 'Recently added' (hideOnError) before Recommended.
    3. A 'Ready for you' row comes from the viewer's own and followed requests (section=ready&ready_within_days=30). Each card gets its Watch pill ([REQ-16](#req-16)) when Plex is configured. The row is hidden when empty.
  - **Files:** `internal/httpapi/discover_rows.go`, `internal/httpapi/server.go`, `internal/movies/repo.go`, `internal/series/repo.go`, `web/src/pages/Discover.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Every role sees a 'Recently added' row with the latest imports, one card per show, and no adult titles.
    - With Plex disconnected the row still fills from Arrmada's import history.
    - A requester whose request became ready this week sees it in 'Ready for you' with a Watch button when Plex is set up.
  - **Tests:** Go: TestRecentlyAddedFromEvents (dedupe per show; 30-day window).; Go: TestRecentlyAddedAdultFilter.; Go: TestRecentlyAddedMapsEpisodesToShows (when the Plex source is wired; fake index).; UI check: both rows for a requester at 375px and on desktop.
  - **Depends on:** [REQ-03](#req-03), [REQ-16](#req-16), PLEX — TMDB index / GUID parsing on RecentlyAdded (optional second source)
  - **Risk:** Plex RecentlyAdded can include libraries Arrmada doesn't manage, so only show items that map to a TMDB id. Keep the endpoint requester-safe.
  - **Resolves:** discover-5
<a id="req-19"></a>
- [ ] **REQ-19 · Browse grid with filters, 'See all' on list rows, and paginated search** — `P2` · `M` · Phase 8
  - **Problem:** PosterRow (Discover.tsx:875-959) has no 'See all'. DiscoverByGenre and Search go through cachedDiscoverList with pages=1 (discover.go:417-419, 569-604), and the handlers take no page parameter. A genre or a search therefore tops out at about 20 titles, with no sort or filters.
  - **Approach:** 1. internal/metadata: BrowseQuery{Media; Genres []int; YearFrom, YearTo int; RatingMin float64; RuntimeMin, RuntimeMax int; Provider int; Language, Sort, List string; Page int}, and (t *TMDB) Browse(ctx, q) (items []DiscoverItem, totalPages int, err error).
       - Whitelist sort: popularity.desc, vote_average.desc, primary_release_date.desc / first_air_date.desc, revenue.desc. Clamp page to 1-500.
       - Always set include_adult=false and a vote_count.gte floor (200 for rating sorts). Keep noise genres excluded for TV unless chosen. Use regionCode() for watch_region.
       - Run every result through toItem (the adult filter).
       - List=trending|popular|upcoming|top_rated|now_playing pages those fixed lists.
       - Add a cachedDiscoverPage variant that returns total_pages and keys on the page, keeping discoverCacheCap.
    2. New endpoint GET /api/v1/discover/browse?… returns {items (enriched), page, total_pages}. GET /api/v1/discover/search gains page and returns total_pages.
    3. Frontend:
       - A /discover/browse route renders in place of the rows.
       - Filter bar: media toggle, genre chips, year range, minimum rating, runtime, provider chips (from discoverProviders), language and sort. All state lives in search params.
       - Infinite grid via IntersectionObserver, with skeletons and the existing LoadError.
    4. PosterRow gets an optional seeAll prop that renders 'See all →'. Wire it for Trending, Popular, Top rated, In cinemas, Hidden gems, Upcoming/Airing soon, the GenreExplorer genre and the StreamingRow provider.
    5. SearchResults pages infinitely.
  - **Files:** `internal/metadata/discover.go`, `internal/metadata/discover_test.go`, `internal/httpapi/discover.go`, `internal/httpapi/server.go`, `web/src/pages/Discover.tsx`, `web/src/pages/DiscoverBrowse.tsx (new)`, `web/src/App.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - 'See all' on 'Popular movies' opens /discover/browse?list=popular&media=movie and keeps loading past 20 titles while scrolling.
    - Filtering to Sci-Fi, 1990-1999, rating 7+ returns matching titles, and the URL reproduces the result after a reload.
    - Searching 'star' loads more results while scrolling.
    - No adult titles appear under any filter combination. An invalid sort or page is rejected or clamped, never passed to TMDB raw.
  - **Tests:** Go: TestBrowseQueryWhitelist (bad sort rejected, page clamped, include_adult always false, vote floor always set; asserted on the URL with a fake HTTP server).; Go: TestBrowseUsesToItem (adult and posterless items dropped).; Go: TestSearchPaging (page forwarded; total_pages returned).; UI check: See all links, filter round-trip through the URL, infinite scroll at 375px.
  - **Depends on:** APP — Discover child routes and search-param state (draft discover.t3; optional)
  - **Risk:** Free-form filters multiply cache keys and TMDB calls; the whitelist and the cache cap bound them. Don't prefetch pages.
  - **Resolves:** discover-6
<a id="req-20"></a>
- [ ] **REQ-20 · Person pages, clickable cast and crew, and people in search** — `P2` · `M` · Phase 8
  - **Problem:** Search uses /search/multi, and toItem drops person rows (discover.go:378-379), so searching for an actor returns nothing useful. Cast tiles are plain divs with no handler (Discover.tsx:1362-1376). CastMember and CrewMember carry no TMDB id.
  - **Approach:** 1. metadata:
       - Add ID int to tmdbCast, CastMember and CrewMember, filled in castOf and movieCrew. This stays JSON-compatible with the stored SeriesExtra and movie Extra.
       - If the TMDB detail cache exists, bump its key version so old entries without ids are refetched.
    2. (t *TMDB) Person(ctx, id) calls /person/{id}?append_to_response=combined_credits,external_ids.
       - Returns {id, name, biography, profile_url, birthday, place_of_birth, known_for_department, credits []DiscoverItem}.
       - Returns ErrNotFound when person.adult is true.
       - Credits (cast, plus Director and Creator crew) go through toItem with a low vote floor (about 10), are deduplicated by media and id, and sorted by popularity.
       - Cached with stale-while-revalidate for 24 h.
    3. Search gains People []PersonResult{id, name, profile_url, known_for_department, known_for []string}.
       - Adult people are dropped, and known_for titles go through adultfilter.Matches.
       - People without a profile image, or with popularity under 1, are dropped.
       - The /discover/search response adds people.
    4. New endpoint GET /api/v1/discover/person/{id} returns the person with credits enriched through enrichCards.
    5. UI:
       - A /discover/person/:id page: photo, name, department and a clamped bio with 'More', then 'Known for' and a filmography grid of MediaCards.
       - Cast tiles and the Director/Creator facts in the sheet become links.
       - The search dropdown and SearchResults show a 'People' group.
  - **Files:** `internal/metadata/tmdb.go`, `internal/metadata/discover.go`, `internal/metadata/provider.go`, `internal/httpapi/discover.go`, `internal/httpapi/server.go`, `web/src/pages/Discover.tsx`, `web/src/pages/DiscoverPerson.tsx (new)`, `web/src/lib/api.ts`, `web/src/App.tsx`
  - **Acceptance:**
    - Searching 'Florence Pugh' shows her in a People group. Opening her page shows a filmography grid with library and request badges.
    - Tapping a cast tile in any title sheet opens that person's page. Back returns to Discover, or to the sheet once APP's title routes exist.
    - Adult performers never appear in search, and their person pages return 404.
    - Stored cast JSON without ids still renders, and the Movies and Series detail pages are unaffected.
  - **Tests:** Go: TestPersonMapsCreditsThroughToItem (adult and posterless credits dropped; dedupe).; Go: TestPersonAdultIsNotFound.; Go: TestSearchReturnsPeopleFiltered.; Go: TestCastIDsParsed.; UI check: cast tile → person → title → back.
  - **Depends on:** APP — Discover title routes (draft discover.t3) for 'Back returns to the sheet', INT/BE — TMDB detail cache (draft discover.t23); bump its key version when adding ids
  - **Risk:** Person credits are where adult titles most often leak. Every credit must pass through toItem and the adult filter, with no shortcut mapping. Adding ID to the shared CastMember type touches the Movies and Series detail pages.
  - **Resolves:** discover-6
<a id="req-21"></a>
- [ ] **REQ-21 · Collection pages and named 'Complete the X collection' rows** — `P3` · `M` · Phase 8
  - **Problem:** 'Finish your collections' merges up to 8 collections into one unnamed strip capped at 30 (discover_rows.go:268-289). Users can't tell which franchise a card belongs to, and no collection can be opened on its own.
  - **Approach:** 1. metadata:
       - Collection gains Overview, PosterURL and BackdropURL (TMDB /collection/{id} returns them).
       - Members go through adultfilter.Matches.
       - MediaDetail gains Collection *struct{ID int; Name string} from the movie's belongs_to_collection (add it to tmdbMovie).
    2. New endpoint GET /api/v1/discover/collection/{id} returns {id, name, overview, backdrop_url, items}, with items enriched and owned members flagged via in_library. Cached for 6 h.
    3. handleDiscoverCollections returns rows: [{collection_id, title: 'Complete the <name>', items}]. It returns up to 4 collections with at least 2 missing released members, most complete first.
    4. UI:
       - DiscoverTab renders one PosterRow per collection, with seeAll → /discover/collection/:id.
       - The collection page shows a backdrop header, overview, an owned/total count and a member grid.
       - Staff get a 'Request the rest' action through the bulk path, so it is quota- and notification-aware. Requesters request one title at a time.
       - The movie sheet links 'Part of the <name> →'.
  - **Files:** `internal/metadata/tmdb.go`, `internal/metadata/provider.go`, `internal/metadata/discover.go`, `internal/httpapi/discover_rows.go`, `internal/httpapi/server.go`, `web/src/pages/Discover.tsx`, `web/src/pages/DiscoverCollection.tsx (new)`, `web/src/lib/api.ts`, `web/src/App.tsx`
  - **Acceptance:**
    - Discover shows separate rows such as 'Complete the Alien Collection', each with its own 'See all'.
    - The collection page lists every released member with an In library, Pending or requestable badge.
    - A movie's sheet links to its collection.
    - No adult member titles are shown.
  - **Tests:** Go: TestCollectionsRowsGrouped (one row per collection, ordering, minimum-missing rule).; Go: TestCollectionEndpointAdultFilter.; Go: TestMediaDetailCollectionParsed.; UI check: row → collection page → title sheet → back.
  - **Depends on:** [REQ-19](#req-19), [REQ-05](#req-05), APP — Discover routes (optional)
  - **Risk:** Changing the /discover/collections response shape breaks the old frontend, so ship the backend and the UI together.
  - **Resolves:** discover-6

#### Milestone: M6 — Problems reported and fixed in place

_Family members can report a bad file from the title sheet. The owner sees it under Requests → Issues and fixes it with one click (blocklist and re-search, or a subtitle job). The issue resolves itself when the new file lands, and the reporters are told._

<a id="req-22"></a>
- [ ] **REQ-22 · 'Report a problem' for requesters, routed into an Issues tab on /requests** — `P2` · `M` · Phase 12
  - **Problem:** There is no issue or report concept anywhere in internal/, web/src or the migrations. For an in-library item the Discover sheet shows only the status badge and a trailer link (Discover.tsx:1306-1331). Family members text the owner something like 'episode 4 has no English audio', and the owner has to find the item and the file by hand.
  - **Approach:** 1. Migration NNNN_issues.sql:
       - issues(id INTEGER PRIMARY KEY, media_type TEXT NOT NULL, library_id INTEGER NOT NULL, tmdb_id INTEGER NOT NULL DEFAULT 0, title TEXT NOT NULL, season INTEGER NOT NULL DEFAULT 0, episode INTEGER NOT NULL DEFAULT 0, category TEXT NOT NULL CHECK(category IN ('video','audio','subtitles','wrong_file','other')), note TEXT NOT NULL DEFAULT '', reported_by INTEGER NOT NULL, reported_by_name TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'open', resolution TEXT NOT NULL DEFAULT '', action TEXT NOT NULL DEFAULT '', action_file TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, resolved_at INTEGER NOT NULL DEFAULT 0, resolved_by INTEGER NOT NULL DEFAULT 0)
       - status is open, fixing, resolved or dismissed
       - issue_reporters(issue_id, user_id, PRIMARY KEY(issue_id, user_id)), so a second report of the same problem joins the first
    2. New internal/issues package:
       - Create checks server-side that the library item exists and has a file (for series, that the episode exists and has a file), and that its title passes the adult filter.
       - Create deduplicates against an open issue with the same media, season, episode and category.
       - List(filter), Resolve(id, resolution, by) and Dismiss(id, reason, by).
       - Resolve and Dismiss notify every reporter through a NotifyUsers helper extracted from requests.notifyParties, which the issues package uses through an interface.
       - Publish issue.reported on the bus. [REQ-07](#req-07)'s hub deny list keeps it off websockets.
       - Staff inbox and push reuse [REQ-07](#req-07)'s staff fan-out.
    3. API:
       - POST /api/v1/issues (requireRole RoleRequester)
       - GET /api/v1/issues (staff see all; requesters see their own)
       - POST /api/v1/issues/{id}/resolve and /dismiss (requireRole RoleManager)
       - Add /api/v1/issues to externalAllowedPrefixes.
    4. UI:
       - RequestDetailModal (in-library titles) and RequestSheet (ready requests) get 'Report a problem'.
       - It opens a sheet with category radios ('Picture', 'Sound', 'Subtitles', 'Wrong file', 'Something else'), season and episode selects for series, and a note of at most 500 chars.
       - Toast: 'Thanks — we'll let you know when it's fixed.'
       - Staff get an 'Issues · n' tab on /requests. Requesters see their own reports and their status.
    5. Open issues feed OBS's attention feed and admin alerts through issue.reported.
  - **Files:** `internal/store/migrations/NNNN_issues.sql (new)`, `internal/issues/service.go (new)`, `internal/issues/repo.go (new)`, `internal/issues/service_test.go (new)`, `internal/httpapi/issues.go (new)`, `internal/httpapi/server.go`, `internal/httpapi/external.go`, `internal/requests/usernotify.go`, `cmd/arrmada/main.go`, `web/src/pages/Discover.tsx`, `web/src/components/RequestSheet.tsx`, `web/src/components/IssueReportSheet.tsx (new)`, `web/src/pages/Requests.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A requester can report 'Sound · S01E04 · no English audio' from the title's sheet on their phone, including from outside the LAN.
    - The owner sees it in Requests → Issues, and in the Dashboard's attention feed once OBS wires it.
    - Resolving it with a note sends the reporter one inbox entry and one push.
    - A second person reporting the same thing is attached to the existing issue instead of creating a duplicate.
  - **Tests:** Go: TestCreateValidation (unknown item, item without a file, bad category).; Go: TestDedupeJoinsReporters.; Go: TestRequesterCannotListOthers.; Go: TestResolveNotifiesEachReporterOnce.; UI check: report flow at 375px.
  - **Depends on:** [REQ-04](#req-04), [REQ-05](#req-05), [REQ-07](#req-07), OBS — attention feed and admin alert catalog for issue.reported
  - **Risk:** Reporters must only reference items they're allowed to see, so validate on the server. Free-text notes are shown to staff only and rendered as text.
  - **Resolves:** product-14
<a id="req-23"></a>
- [ ] **REQ-23 · One-click fixes for reported problems, with auto-resolve when a new file lands** — `P2` · `M` · Phase 12
  - **Problem:** Recording an issue only helps if the owner can act on it in place. The fixes already exist but are spread across other pages: blocklist plus re-search (POST /api/v1/movies/{id}/blocklist, /series/{id}/blocklist, /movies/{id}/search, /series/{id}/search), subtitle actions (/subtitles/library/movies/{id}, /subtitles/library/episodes/{series}/{season}/{episode}) and file details. Overseerr records issues but can't fix anything itself.
  - **Approach:** 1. The staff issue sheet (new web/src/components/IssueSheet.tsx, opened from Requests → Issues):
       - shows the current file through FileDetailsModal (movie or episode)
       - 'Blocklist this copy & find another', with a confirm, through the existing blocklist and search endpoints
       - 'Search again'
       - for subtitles issues, the Subtitles module's per-file actions
       - 'Open title'
       Taking an action sets status 'fixing', records issues.action, and records action_file (the current path and size).
    2. Auto-resolve on new files:
       - issues.Service.Run subscribes to movie.downloaded (which carries id) and series.imported.
       - Extend the series.imported payload with an additive "episodes": [[season, episode], …] (automation/series.go:1075), so resolution matches episode identity. This avoids contending for the single-slot SetSeriesImportedHook.
       - A new file for an item or episode with an issue in 'fixing' resolves it as 'A new copy was downloaded' and notifies the reporters.
    3. Subtitle actions resolve when the Subtitles job completes, through a completion callback added to subtitles.Service.
    4. A 10-minute backstop sweep resolves 'fixing' issues whose current file path or size differs from action_file. This covers dropped bus events.
    5. Manual Resolve with a note and Dismiss with a reason stay available.
  - **Files:** `web/src/pages/Requests.tsx`, `web/src/components/IssueSheet.tsx (new)`, `internal/issues/service.go`, `internal/issues/service_test.go`, `internal/automation/series.go`, `internal/subtitles/service.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - From an audio issue, 'Blocklist this copy & find another' blocklists the release and starts a search, and the issue shows 'Fixing'.
    - When the replacement imports, the issue resolves by itself and the reporter is notified once.
    - A subtitles issue can trigger a subtitle fetch or generate from the issue, and resolves when the job completes.
    - An issue on S01E04 is not resolved by an import of S01E05.
  - **Tests:** Go: TestAutoResolveOnImport (same movie or episode resolves; a different episode doesn't).; Go: TestSubtitleCompletionResolves.; Go: TestBackstopResolvesOnFileChange.; UI check: each action calls the right endpoint (network log) and updates the status.
  - **Depends on:** [REQ-22](#req-22), SUB — subtitle job completion callback, SAFE — recycle-bin semantics for blocklist-and-replace (always confirm first)
  - **Risk:** Blocklisting replaces the file through the existing delete paths, whose recycle-bin behaviour belongs to SAFE, so always confirm first. Auto-resolve must match episode identity, not the title. Never trigger anything that converts or test-encodes the owner's files.
  - **Resolves:** product-14

#### Risks

- The Requests Service API changes shape twice: options in REQ-01, and ListFilter/List in REQ-03. Every caller must move in the same commit:
- handlers
- Overseerr import
- discover snapshot
- Because and Recommended seeds
- books.go and mybooks.go
- Dropping per-show uniqueness for series requests (REQ-12) touches every per-show lookup. The migration must keep legacy rows and their 'series:<tmdb>' notification refs intact, or people get duplicate 'ready' notices.
- Upgrade behaviour must be deliberate and called out in the release note:
- ready_at is backfilled from refs already sent
- existing users keep auto-approve, now split per type
- new Plex sign-ins on untouched installs move to movies-only
- quotas default to unlimited
- Plex GUID formats vary by agent, and a matching failure delays every ready notice to the grace cap. Verify against the owner's libraries and log misses with ids.
- The realtime hub broadcasts every bus event to every socket today. New detailed events (request.*, issue.*) must stay off it until SEC's role filtering lands, and only payload-free requests.changed is broadcast.
- Adult-content leakage through new TMDB surfaces: person credits, collections, browse filters and recently added. Every list must go through toItem and adultfilter.Matches with no shortcut mapping.
- Bulk approvals and Overseerr imports can trigger search storms unless every approval path goes through REQ-01's bounded queue.
- Migration numbering collides with other epics working from 0090. Take the next free number at implementation time and run the store migration tests and go test -race in Docker before pushing.
- Visual drift on the new pages: /requests, the sheets, browse, person and collection pages. Reuse the Discover chips, poster cards, accent-gradient buttons and the existing type scale.

#### Out of scope

- Triggering Plex library scans and building the Plex GUID index or locator internals (PLEX). REQ-15 only consumes a locator.
- Requester shell work (bottom tab bar, safe-area padding, PNG icons, bell in the shared layout) and title routes (APP). The MediaCard quick-request tap-safety fix for movies (APP/FE).
- Fixing Series monitoring semantics: SetMonitored's cascade, the bulk Monitor action, the add-series presets in AddSeriesModal, and new-season inheritance (SER).
- Book request identity (book_id instead of ol_key) and a Read/Listen/Both choice when requesting (BOOK/AUD).
- Music requests. Requests stay movie, series and book.
- Plex watchlist auto-requests, and separate 4K servers or parallel 4K libraries in the Overseerr style. Only profile choice, including 4K profiles, is in scope.
- Per-user notification preference controls (draft discover.t22). REQ-07 and REQ-22 respect them once they exist.
- The Dashboard attention feed and the admin alert catalog themselves (OBS). REQ-22 only publishes into them.
- Anything touching audiobook listening data or the audiobook privacy model.
- Encoding or test-converting library files as an issue fix.

