# INT — Integrations: indexers, download clients, metadata, FlareSolverr

_Part of the [Arrmada roadmap](../../ROADMAP.md). 25 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Every integration Arrmada depends on (indexers, Prowlarr, FlareSolverr, TorrentLeech/MyAnonaMouse/1337x, download clients, metadata keys) reports whether it works and backs off when it doesn't. Each one can be tested before and after saving and is managed from one Connections hub. None of them silently undoes the owner's settings. Searches use each indexer's real capabilities (IMDb/TMDB/TVDB ids, categories, book and music modes) instead of title text alone.

**Why.** The integration clients themselves are well built: redaction at error creation, single-flight qBittorrent login, a 409 counted as success, a partial-queue check. The product layer around them is thin, and in places it misleads (audit score 5: backend 7, experience 4).

- A dead tracker looks exactly like "no results". Failures only reach SearchResult.Errors and a log line. Nothing is stored and nothing backs off, so a broken TorrentLeech login is retried on every search of every sweep (integrations-1, product-5).
- Clicking "Sync from Prowlarr" wipes per-indexer scoping and re-enables indexers the owner turned off. It matches rows by display name, and indexers removed upstream stay forever (integrations-2). Usenet indexers are imported and queried even though nothing can download them (integrations-4).
- The Torznab Test reports "Connected" for any HTTP 200, including Prowlarr's web page. A wrong key surfaces later as an XML parser error (integrations-5).
- Download clients can't be edited or disabled, and the bundled one comes back on every restart. A free-text Category field silently breaks every movie import on installs without the bundled client. There is no path mapping for an external qBittorrent (integrations-3, -6, -11; frontend-10).
- TorrentLeech keeps a stale Cloudflare session until restart and retries failed logins on every search, which risks the private-tracker account. It also has no RSS (integrations-8).
- FlareSolverr is an invisible env-only dependency, and the error messages point to a setting that doesn't exist (integrations-13).
- TMDB, the one required key, can't be tested, so a v4/v3 mix-up just shows an empty Discover. OMDb calls are uncached and can stall the detail sheet for 12s (integrations-14, system-10, discover-14).
- Integrations are spread across six screens with no single status view (system-9, frontend-10).
- Searches send title text only: no imdbid/tvdbid, no categories, and caps are thrown away. Alt-titled films and books on general trackers are missed or polluted (integrations-10, movies-11).

**Depends on:** SEC: release tokens that keep tracker download URLs server-side (integrations-7 / integrations.t6). They must land before [INT-20](#int-20) ships TorrentLeech RSS-key download links, which embed a secret.; SEC: Manager-only access to tracker data and role-filtered realtime topics (movies-3, integrations-7). Until then, [INT-02](#int-02) gates the indexer status fields by role inside the a.protected GET /indexers handler, and [INT-01](#int-01) publishes integration.status events with no names or error text.; FE: the component kit (ConfirmDialog, Toast, Drawer, StatusDot). [INT-04](#int-04), [INT-16](#int-16), [INT-17](#int-17) and [INT-18](#int-18) use it when it has landed, otherwise window.confirm and local components that can be swapped later.; CFG: the Settings hub (system-9) that hosts Connections as a section ([INT-16](#int-16) mounts at /settings/connections when it exists), and the key-field semantics where blank never silently clears a key ([INT-06](#int-06), [INT-17](#int-17)).; OBS: the health panel structure (system-8, product-5) that [INT-14](#int-14) adds integration warnings to, including the warning 'link' field the Dashboard renders.; PLEX: moves the Plex connection editor into the Plex card slot that [INT-15](#int-15)/[INT-16](#int-16) provide. [INT-16](#int-16) only links to Insights → Settings.; SUB: shares the OpenSubtitles login verifier (subtitles-11) with [INT-06](#int-06), so there is one login path.

#### Design

## Target architecture

### 1. One status model for every integration: `internal/connstatus`
- **Tables** (one migration, next free number from 0090):
  - `integration_status(kind, ref, last_ok_at, last_error_at, last_error, consecutive_failures, failing_since, backoff_until, avg_ms, updated_at, PRIMARY KEY(kind, ref))`
  - `integration_counts(kind, ref, hour 'YYYY-MM-DDTHH', queries, failures, PRIMARY KEY(kind, ref, hour))`, pruned after 48h.
- **Kinds and refs:** `indexer` (ref = id), `download_client` (id), `flaresolverr` ('default'), `prowlarr` ('default'), `apikey` (catalogue id: tmdb/tvdb/omdb/hardcover/opensubtitles_api), `plex` ('default').
- **Tracker API:**
  - `New(db, log)`, `Load(ctx)`, `Record(kind, ref, Outcome{Err, Dur, RetryAfter, Backoff})`.
  - `Allow(kind, ref, interactive) (bool, State)`, `Reset`, `Forget`, `Get`, `List(kind)`, `Counts24h`.
  - `Flush(ctx)` runs as scheduler job `integration-status-flush` every 60s.
  - `OnChange(fn)` publishes `integration.status` `{kind, ref, state}` on the event bus. The event carries no names and no error text, because the websocket has no topic filtering today.
- **Writes:** state transitions are written immediately; counters and avg_ms only on flush. There is never one write per search.
- **Errors:** every stored error goes through `connstatus.Redact`, which strips `apikey=`, `passkey=`, `token=` and `api_key=` values and URL query strings, and caps the text at 300 chars.
- **Backoff (indexers only):**
  - Ladder: 1st failure none, then 5m, 15m, 1h, 3h, 6h cap. Retry-After wins when it is longer.
  - Interactive searches and Test always probe, and a success clears the backoff.
  - Errors caused by the caller's ctx (modal closed, shutdown) are never counted.
- **Derived state:** `ok | failing | backing_off | disabled | unknown`.
- **Who reads it:** the Indexers rows, the Connections hub, the health panel and (ACQ) the search-modal banners all use the same data.

### 2. Indexer layer
- `SearchQuery` gains `IMDBID, TMDBID, TVDBID, Author, BookTitle, Artist, Album`.
- `Release` gains `IMDBID/TMDBID/TVDBID` (read from Torznab attrs and TorrentLeech's `imdbID`) and `MatchedByID`.
- `SearchResult` gains `Skipped map[name]reason`. ACQ uses it to show banners and to avoid recording a search miss when every indexer failed or was skipped.
- `indexer.WithInteractive(ctx)` / `IsInteractive(ctx)` mark user-started searches.
- **Caps:** stored per indexer (`caps_json`, `caps_at`) from `t=caps`, parsed case-insensitively. Caps drive the id params, `t=book`/`t=music` and default categories. With no caps stored, the built URL is exactly today's.
- **Typed errors:** `*HTTPStatusError{Code, RetryAfter}`, `*TorznabError{Code, Description}`, and detection of HTML web pages. `parseFeedPage` reports the indexer's own error text.
- **Throttle:** the per-host 1 req/s spacing stays. It becomes a two-lane dispatcher (interactive first), and the queue wait sits outside the per-indexer deadline.
- `Registry.Reset(id)` drops native sessions and login backoff when an indexer is edited or deleted.
- Newznab rows are not searched until a usenet download client exists.

### 3. Prowlarr as a managed connection
- **New columns:** `indexers.prowlarr_id`, `disabled_by ('' | 'user' | 'prowlarr')`, `managed_note`.
- **Sync rules:**
  - Match on `prowlarr_id`.
  - Update only upstream-owned fields (name, url, api_key). Never touch enabled, media_types, categories, priority, min_seeders or seed rules.
  - An indexer disabled or deleted upstream gets `disabled_by='prowlarr'` plus a note. Rows are never deleted.
  - Usenet indexers are skipped.
  - New rows get media types from Prowlarr's categories.
  - A failed or empty indexer list changes nothing locally.
  - A rename also rewrites `grabs.indexer` and `blocklist.indexer`, because seed rules and Fetch look indexers up by name.
- **Schedule:** hourly job, with the last sync result stored.
- **FlareSolverr proxy:** added only to the bundled Prowlarr (host `arrmada-prowlarr`) or on explicit opt-in.
- **UI:** a 'Managed by Prowlarr' badge, with name, URL and key locked. The 'Open Prowlarr' link uses the installed host port.

### 4. FlareSolverr connection
- The URL is a catalogue entry `flaresolverr`: saved setting first, `ARRMADA_FLARESOLVERR_URL` as fallback.
- `flaresolverr.NewFunc(endpoint func() string)` reads the URL on every call, so a change applies without a restart. `Ping` uses `sessions.list`, and `Status` is cached for 60s.
- TorrentLeech, 1337x, TheXEM and the Prowlarr proxy all use the same resolver.
- Error copy names the container or the setting, whichever is actually at fault.

### 5. Download clients
- **CRUD:** `PUT /downloadclients/{id}` (a blank password keeps the stored one), an enabled toggle, and `priority`. Adds go to clients in priority order and fail over only on transport errors.
- **Bundled client:** a `bundled` flag plus the `download_bundled_removed` setting, so a disabled or deleted bundled client stays that way. `POST /downloadclients/restore-bundled` brings it back.
- **Categories:** Arrmada owns them. The movie category comes from `cfg.DownloadCategory` and is passed explicitly; the per-client field is retired.
- **Path mapping:** optional per-client `path_maps` for an external or seedbox qBittorrent. Test checks `save_path` visibility and the hardlink filesystem, and local paths under DataDir (/data) are rejected.

### 6. Metadata keys
- **Testing:** every catalogued key is testable. `POST /apikeys/{id}/test {value?}` tests a candidate without persisting it, using `Deps.KeyVerifiers` built in main.go.
- **TMDB detail cache:** `swr` key `tmdb:detail:v1:<media>:<id>`, 12h.
- **OMDb cache:** `omdb:<imdb>`, 7d. 'Not found' is cached for 1d.
- **Ratings never block:** the detail sheet waits at most 2s for ratings, then they finish in the background and fill the cache.
- **OMDb errors:** quota errors are recorded as the omdb apikey status.
- **Alternate titles:** movies store TMDB `original_title` and alternative titles in `MovieExtra`. `releaseIsForMovie` matches any known title, and the year check is unchanged.

### 7. Connections hub
- **API:**
  - `GET /api/v1/connections` (Manager) returns `{sections:[{id,title,items:[ConnectionItem]}]}`.
  - `ConnectionItem = {kind, ref, name, subtitle, configured, state, detail, last_ok_at, last_error, last_error_at, failing_since, backoff_until, checked_at, counts{queries_24h,failures_24h,grabs_24h}, testable, managed_by, fix_path}`. It never contains secrets.
  - `POST /api/v1/connections/{kind}/{ref}/test`.
- **UI:** `/connections`, which becomes a section of CFG's Settings hub when that exists.
  - **Sections:** Search (Prowlarr, FlareSolverr, indexers), Downloads (clients), Metadata (TMDB, TVDB, OMDb, Hardcover, OpenSubtitles), Playback & other (Plex, Apprise, audiobook server).
  - **Each card:** a status dot, 'Last OK 3 min ago' or 'Failing since 14:02: login failed · next try 15:00', 24h counts, one primary action (Test, or Fix when failing), and an overflow menu (Edit, Disable, Delete with confirmation).
  - **Behaviour:** live refresh on `integration.status`, edit drawers, and an add-indexer preset modal with test-before-save.
  - **Navigation:** `/indexers` and `/downloadclients` redirect into the hub, and the sidebar has one 'Connections' entry.
  - **Style:** existing palette and type scale; dots use `--good`, `--avoid`, `--reject` and `--ink-faint`.
- **Health panel:** `handleSystemHealth` reads the same status (failing indexers, FlareSolverr down while a dependent indexer exists, any enabled client unreachable, Prowlarr sync failing, TMDB key rejected). Each warning links to its card.

### 8. Migration plan
Numbers are assigned at implementation time in landing order, starting from the next free number at or above 0090, coordinated with other epics:
1. `integration_status` + `integration_counts` ([INT-01](#int-01)).
2. `indexers`: `prowlarr_id`, `disabled_by`, `managed_note`, plus an idempotent Go backfill of `prowlarr_id` from `<prowlarr_url>/<n>/api` URLs ([INT-07](#int-07)).
3. `indexers`: `caps_json`, `caps_at` ([INT-09](#int-09)).
4. `download_clients`: `priority`, `bundled`, plus startup marking of the row whose URL equals `cfg.QbittorrentURL` ([INT-11](#int-11)).
5. `download_clients`: `path_maps` ([INT-25](#int-25)).

No migration is needed for:
- category retirement (the column is kept and ignored);
- alt titles (JSON in `MovieExtra`);
- the FlareSolverr URL (settings key `apikey:flaresolverr`);
- the Prowlarr last sync (settings key `prowlarr_last_sync`).

### Standing rules applied
- Credentials are entered only in the UI and never returned: handler tests marshal responses and grep for `api_key`, `password` and `token`.
- Live third-party verifiers run on demand only, never in CI or in health polling.
- Tracker logins are tested by the owner through the UI with their own accounts.
- Path-mapping tests use a throwaway qBittorrent container, never the real library.
- Race tests run in the Linux Docker one-liner before pushing (tracker, single-flight login, throttle dispatcher).
- Commits end with the Co-Authored-By trailer.

#### Milestone: M1: See what's broken

_Indexer failures are persisted with escalating backoff, so sweeps stop hammering dead trackers, and every Indexers row shows a status dot and its last error. FlareSolverr is configurable and testable in Settings. Deletes are confirmed. Download clients can be edited in place. Every metadata key, TMDB included, can be tested before and after saving._

<a id="int-01"></a>
- [x] **INT-01 · Integration status store and indexer health with escalating backoff** — `P1` · `M` · Phase 4
  - **Problem:** Indexer failures are never stored, so there is nothing for the UI, the health panel or the search modals to show:
- A failing indexer (expired TorrentLeech login, dead Prowlarr, 429s) only lands in SearchResult.Errors and a log line (indexer/service.go:313-318, 423-426).
- The indexers table has no status columns (repo.go:22), and the Test result lives only in React state (Indexers.tsx:10).
- A 429 is the bare string 'HTTP 429', and Retry-After is ignored (torznab.go:314-316).
- A broken login is retried on every search of every sweep.
- Download clients have the same gap: QueueComplete logs a per-client failure and moves on (download/service.go:325-345).
  - **Approach:** 1. Migration 00xx_integration_status.sql (next free number from 0090):
       - integration_status(kind TEXT, ref TEXT, last_ok_at TIMESTAMP, last_error_at TIMESTAMP, last_error TEXT NOT NULL DEFAULT '', consecutive_failures INTEGER NOT NULL DEFAULT 0, failing_since TIMESTAMP, backoff_until TIMESTAMP, avg_ms INTEGER NOT NULL DEFAULT 0, updated_at TIMESTAMP, PRIMARY KEY(kind, ref));
       - integration_counts(kind, ref, hour TEXT, queries INTEGER, failures INTEGER, PRIMARY KEY(kind, ref, hour)).
    2. New package internal/connstatus:
       - State{Kind, Ref, LastOKAt, LastErrorAt, LastError, ConsecutiveFailures, FailingSince, BackoffUntil, AvgMS} and Outcome{Err, Dur, RetryAfter, Backoff bool}.
       - Tracker with New(db, log), Load(ctx), Record(kind, ref, Outcome), Allow(kind, ref, interactive) (bool, State), Reset, Forget, Get, List(kind), Counts24h(kind, ref), Flush(ctx) and OnChange(fn).
       - It keeps state in memory behind a mutex. It writes a row immediately only on a transition (ok→failing, failing→ok, the next backoff step) and writes counters and avg_ms on Flush. Flush also prunes counts older than 48h.
       - Redact(msg) strips apikey=, passkey=, token= and api_key= values and URL query strings, and caps the text at 300 runes. It is applied to every stored error.
    3. Backoff: backoffFor(n) returns 0 for the 1st failure, then 5m, 15m, 1h, 3h, capped at 6h. Retry-After wins when it is longer.
    4. indexer package:
       - torznab.go get() returns &HTTPStatusError{Code, RetryAfter} for non-200 responses. Error() is still 'HTTP 429'.
       - New context.go with WithInteractive(ctx) / IsInteractive(ctx).
       - Service.SetStatus(*connstatus.Tracker), nil-safe.
       - Search and fetchRecent call Allow before launching each indexer goroutine. A backing-off indexer is skipped, and SearchResult.Skipped[name] = 'paused until 15:00 after 3 failures: login failed'. Add the Skipped field with json:"skipped,omitempty" and copy it in recentCache.
       - After each indexer finishes, Record(err, dur, retryAfter via errors.As). An error is dropped when the caller's own ctx is done (modal closed, shutdown); a per-indexer 25s deadline still counts.
       - Service.Test: success → Reset; failure → Record with Backoff=false, so the error shows but the ladder does not advance.
       - Update → Reset plus Registry.Reset(id); Delete → Forget plus Registry.Reset(id).
       - Registry.Reset calls every searcher implementing interface{ Reset(id int64) }. TorrentLeech drops its session for now.
    5. Mark interactive contexts in httpapi:
       - handleMovieReleases, handleSeriesReleases, handleBookReleases, handleQualityTest (fit_profiles.go:191) and handleSearch;
       - the context.Background() goroutines in handleSearchMovie (movies.go:125), handleSearchSeries, handleSearchBook and handleSearchAudioVersion.
    6. download.Service.SetStatus:
       - QueueComplete records each enabled client's List outcome (kind download_client, Backoff=false). Test records its outcome too.
       - Delete calls Forget.
    7. main.go:
       - tracker := connstatus.New(st.DB(), log); tracker.Load(ctx);
       - indexers.SetStatus(tracker); downloads.SetStatus(tracker);
       - sched.Register("integration-status-flush", time.Minute, false, tracker.Flush);
       - tracker.OnChange → bus.Publish("integration.status", {kind, ref, state}), with no names and no error text.
    8. Log once per transition, for example 'indexer backing off' with name, failures, until and the redacted error.
  - **Files:** `internal/store/migrations/00xx_integration_status.sql`, `internal/connstatus/connstatus.go`, `internal/connstatus/backoff.go`, `internal/connstatus/redact.go`, `internal/connstatus/connstatus_test.go`, `internal/indexer/context.go`, `internal/indexer/service.go`, `internal/indexer/torznab.go`, `internal/indexer/searcher.go`, `internal/indexer/service_health_test.go`, `internal/download/service.go`, `internal/httpapi/movies.go`, `internal/httpapi/series.go`, `internal/httpapi/books.go`
  - **Acceptance:**
    - An indexer pointed at a dead URL is skipped by background sweeps from its 2nd consecutive failure. The log says 'indexer backing off' with the error and the next-try time, and SearchResult.Skipped names it with the reason.
    - Manual searches (release modals, Search now, quality test) still query a backing-off indexer, and one success clears the backoff.
    - A 429 with 'Retry-After: 600' keeps the indexer backed off for at least 10 minutes.
    - Closing a release modal mid-search records no failure for any indexer; a per-indexer 25s timeout does.
    - Status survives a container restart.
    - During normal successful sweeps, integration_status gets at most one write per indexer per minute.
    - Editing or deleting an indexer clears its status and drops any cached native session.
    - Every enabled download client's last list or Test outcome is recorded.
    - No API key, passkey or token appears in any stored last_error.
  - **Tests:** Go connstatus_test: the ladder (none, 5m, 15m, 1h, 3h, 6h cap); a success resets it; Retry-After overrides a shorter step; Allow with interactive=true is always true; Flush writes only dirty rows; Load restores state (store.Open(t.TempDir())).; Go connstatus Redact: strips apikey=/passkey=/token=/api_key= values and query strings, and caps the length.; Go indexer service_health_test with two httptest Torznab servers, one returning 500: after two sweeps the bad one is in Skipped and receives no request; under WithInteractive it is queried; a cancelled caller ctx records nothing.; Go torznab: get() on a 429 with Retry-After returns *HTTPStatusError{429, 600s}, whose Error() is 'HTTP 429'.; Go download: QueueComplete with one closed httptest client records a failure for that client only.; go test -race on connstatus, indexer and download in the Linux Docker one-liner before pushing.
  - **Risk:** Writing on every search would hammer SQLite, so the tracker writes only on transitions plus the 60s flush. Backing off a flaky but useful public tracker hides it from sweeps; the ladder therefore starts at the 2nd failure, and interactive searches always probe. The per-host Torznab throttle stays as it is. ACQ will consume Skipped and Errors (the search-modal banner, and no search miss when every indexer failed or was skipped), so keep those field names stable.
  - **Resolves:** integrations-1, integrations-9, product-5
<a id="int-02"></a>
- [x] **INT-02 · Indexer status in the API and a status dot on each Indexers row** — `P1` · `S` · Phase 4
  - **Problem:** Even once health is recorded (INT-01), the owner can only find a dead indexer by reading Logs:
- The Indexers page has only a manual Test button, and its result is gone on reload (Indexers.tsx:10, 24-30).
- Requests just sit at 'Searching'.
  - **Approach:** 1. httpapi/indexers.go handleListIndexers returns []indexerView{indexer.Indexer; Status *indexerStatus json:"status,omitempty"}.
       - indexerStatus = {state: ok|failing|backing_off|disabled|unknown, last_ok_at, last_error, last_error_at, failing_since, backoff_until, consecutive_failures, queries_24h, failures_24h}.
       - It is filled only when userFrom(r) has Role.AtLeast(auth.RoleManager). The route is a.protected (server.go:188), so requesters must not see tracker errors.
    2. web/src/lib/api.ts: add an IndexerStatus type and Indexer.status?.
    3. Indexers.tsx row:
       - An 8px status dot before the name: var(--good) for ok, var(--avoid) for failing (1-2 failures), var(--reject) for backing_off, var(--ink-faint) for unknown or disabled. Its title attribute names the state.
       - One 11.5px ink-dim line under the row: 'Last OK 3 min ago · 214 searches, 3 failed (24h)', or 'Failing since 14:02: login failed · next try 15:00', or 'Not used yet'.
       - Reuse an existing relative-time helper if web/src/lib has one, otherwise add a small one.
    4. Call refresh() after Test so the dot updates. useLive(): when last.topic === 'integration.status', refresh, debounced 1s.
  - **Files:** `internal/httpapi/indexers.go`, `internal/httpapi/indexers_test.go`, `web/src/lib/api.ts`, `web/src/pages/Indexers.tsx`
  - **Acceptance:**
    - An indexer pointed at a dead URL shows a red dot after 2 failed sweeps, with 'Failing since HH:MM: <error> · next try HH:MM'. It is still red after a restart.
    - A passing Test turns the dot green without a reload.
    - A requester calling GET /api/v1/indexers gets no status field.
    - The row keeps the existing palette and type scale.
  - **Tests:** Go handler test: a manager sees status and a RoleUser does not; the marshalled JSON contains no api_key or password.; UI check: a broken indexer's dot and line, the green state after Test, and the live refresh on an integration.status event.
  - **Depends on:** [INT-01](#int-01)
  - **Risk:** Low. Confirm --avoid exists in the CSS tokens; otherwise use the amber token the Quality page already uses for 'Want'.
  - **Resolves:** integrations-1, product-5
<a id="int-03"></a>
- [x] **INT-03 · FlareSolverr as a configurable, testable connection** — `P1` · `S` · Phase 4
  - **Problem:** FlareSolverr is a dependency you can't see, set or test:
- Its URL comes only from ARRMADA_FLARESOLVERR_URL and is fixed at startup (config.go:101, indexer/service.go:76-80, xem.New at main.go:182).
- It isn't in the API-key catalogue or the health panel.
- TorrentLeech errors say 'configure FlareSolverr' (torrentleech.go:151, 284), but there is nowhere to do that.
- Indexers.tsx says it is 'wired up' even when it's dead (179, 205-207, 322, 327).
- When the roughly 1 GB Chromium container dies, 1337x (x1337.go:76-81) and TorrentLeech both fail with only a log line.
  - **Approach:** 1. internal/flaresolverr/client.go:
       - add NewFunc(endpoint func() string), keeping New(url) as a wrapper, and read the endpoint on every call;
       - add a nil-safe Configured();
       - add Ping(ctx) (version string, err error): POST /v1 {"cmd":"sessions.list"}, expecting status 'ok' and returning the 'version' field;
       - add Status(ctx), cached for 60s, returning {configured, ok, version, error, checked_at};
       - add an optional OnResult(func(err error)) hook called from Get and Ping.
    2. apikeys.Catalog gets {ID:'flaresolverr', Label:'FlareSolverr URL', Purpose:'Gets TorrentLeech, 1337x and TheXEM past Cloudflare. Bundled by default.', EnvVar:'ARRMADA_FLARESOLVERR_URL', Secret:false, Testable:true}. That gives a saved setting first, the env var as fallback, and the existing Status UI.
    3. main.go:
       - build fs := flaresolverr.NewFunc(keyStore.Func("flaresolverr"));
       - indexer.NewService takes *flaresolverr.Client instead of a URL string;
       - xem.New takes a func() string, read per Fetch (xem.go:37-71);
       - when [INT-01](#int-01) is in, fs.OnResult → tracker.Record('flaresolverr','default').
    4. TorrentLeech (torrentleech.go:120, 149-152, 283-285) and 1337x (x1337.go:76) use fs.Configured() instead of fs != nil.
    5. httpapi/prowlarr.go:44 passes a.deps.APIKeys.Value(ctx,'flaresolverr') instead of Config.FlaresolverrURL.
    6. handleTestAPIKey: case 'flaresolverr' → Ping, reporting e.g. 'FlareSolverr 3.3.21 is answering'.
    7. New GET /api/v1/flaresolverr/status (RoleManager) returning fs.Status(ctx).
    8. Copy:
       - not configured: 'Cloudflare blocked TorrentLeech and FlareSolverr isn't set up; add its URL in Settings → API keys';
       - configured but unreachable: 'FlareSolverr at <url> isn't answering; is the Arrmada-flaresolverr container running?';
       - Indexers.tsx shows 'FlareSolverr: ready / not answering / not set up' from the status endpoint instead of always claiming it's wired.
  - **Files:** `internal/flaresolverr/client.go`, `internal/flaresolverr/client_test.go`, `internal/apikeys/apikeys.go`, `internal/httpapi/apikeys.go`, `internal/httpapi/flaresolverr.go`, `internal/httpapi/prowlarr.go`, `internal/httpapi/server.go`, `internal/indexer/service.go`, `internal/indexer/searcher.go`, `internal/indexer/torrentleech.go`, `internal/indexer/x1337.go`, `internal/xem/xem.go`, `cmd/arrmada/main.go`, `web/src/pages/Indexers.tsx`
  - **Acceptance:**
    - Settings → API keys lists 'FlareSolverr URL' with its source (env or settings), and its Test button reports 'ready' or the exact failure.
    - Changing the URL in Settings applies to the next TorrentLeech, 1337x or TheXEM request without a restart.
    - With the container stopped, the Indexers page says FlareSolverr isn't answering, and TorrentLeech errors name the container rather than a nonexistent setting.
  - **Tests:** Go: Ping against httptest for ok, an error status and unreachable.; Go: NewFunc picks up an endpoint change between calls; Status caches for 60s.; Go: apikeys Value precedence for 'flaresolverr' (the setting beats env).; Go: TorrentLeech newSession error text when FlareSolverr isn't configured vs unreachable.
  - **Risk:** A URL in the 'API keys' list is slightly odd, but it reuses the settings-first/env-fallback code, and it moves to its own card in the hub (INT-16). The NewService signature change touches tests that call NewService(db, log, ""), so update them to pass nil.
  - **Resolves:** integrations-13
<a id="int-04"></a>
- [x] **INT-04 · Confirm deletes and remove the foot-guns on the Indexers and Download clients pages** — `P1` · `S` · Phase 4
  - **Problem:** Both pages have traps:
- Delete fires on one click, with no confirmation and no error handling (Indexers.tsx:34-37, 110; DownloadClients.tsx:43-46, 116).
- A new indexer's name starts as '1337x' and doesn't follow Kind (Indexers.tsx:226-227).
- The 'Used for' pills are inverted: an unscoped indexer shows every pill unlit, and clicking one restricts it to that area. Pill save errors are swallowed by try/finally (536-569).
- 'prio 25' is unexplained, though it only breaks ties after seeders (service.go:438-444).
- The client URL defaults to localhost:8080, which inside Docker is Arrmada itself (DownloadClients.tsx:137).
- Forms are fixed grid-cols-2 with no breakpoint (Indexers.tsx:185, 267, 403, 501; DownloadClients.tsx:163).
  - **Approach:** 1. AddForm: the name follows the kind label (TorrentLeech, MyAnonaMouse, 1337x, Torznab) until the user edits it (nameTouched flag).
    2. Delete on both pages goes through a confirm: the FE kit's ConfirmDialog if it has landed, otherwise window.confirm. try/catch shows an inline error.
       - Indexer: 'Delete TorrentLeech? Searches stop using it; seed rules on downloads already grabbed fall back to the 14-day default.'
       - Client: 'Remove qBittorrent? Arrmada stops sending downloads to it; torrents already there are untouched.'
       - Bundled client, until [INT-11](#int-11): add 'It will be re-added on the next restart; disable it instead to keep it off.'
    3. MediaPills:
       - an unscoped indexer shows every pill lit, with '· all areas';
       - clicking a lit pill excludes that area (media_types = all minus key), and re-lighting every pill stores [];
       - the last lit pill can't be turned off (hint: 'disable the indexer instead');
       - add a catch that shows the error and reverts the optimistic state.
    4. Replace 'prio 25' with 'tie-break 25', with help text: 'Only breaks ties between equally seeded copies of the same release. 1 = preferred.' Use the same text in the forms.
    5. Responsive grids: grid-cols-1 sm:grid-cols-2, and Labeled span2 becomes sm:col-span-2.
    6. The client URL starts empty, with the placeholder 'http://qbittorrent:8080 (as reached from the Arrmada container)' and a note that localhost means the Arrmada container itself.
  - **Files:** `web/src/pages/Indexers.tsx`, `web/src/pages/DownloadClients.tsx`
  - **Acceptance:**
    - Choosing TorrentLeech in Add names it 'TorrentLeech' unless the user typed a name.
    - Deleting an indexer or client asks first and names the consequence, and a failed delete shows the server error.
    - An unscoped indexer shows all four pills lit. Clicking 'Music' excludes music only, and a failed save shows an error and reverts.
    - Existing scoped indexers render with only their areas lit.
    - At 375px wide the forms are one column with no horizontal scroll.
    - Priority is explained wherever it is shown or edited.
  - **Tests:** UI check: add-form naming, delete confirm and error, pill exclude semantics including the last-pill guard, a 375px layout, and the client URL placeholder.
  - **Risk:** The pill change flips the meaning of an existing control; verify that scoped indexers still render correctly before shipping. Keep the current palette and type scale.
  - **Resolves:** integrations-12, frontend-10
<a id="int-05"></a>
- [x] **INT-05 · Edit download clients in place, with an enable toggle** — `P1` · `M` · Phase 4
  - **Problem:** Download clients can't be changed or switched off:
- There is no update route: server.go:196-202 has only POST, DELETE, test, status and settings. Changing a qBittorrent password or URL means deleting and re-adding the client.
- There is no repo Update (download/repo.go).
- The UI has only Test and Delete buttons (DownloadClients.tsx:113-118) and no enabled toggle, although the column exists.
  - **Approach:** Backend
    1. download/repo.go Update(ctx, c):
       - UPDATE download_clients SET name, url, username, enabled WHERE id;
       - password only when non-empty, the same pattern as the indexer repo.Update (indexer/repo.go:96-122);
       - category untouched ([INT-10](#int-10) retires it); ErrNotFound on 0 rows.
    2. download/service.go Update(ctx, c):
       - repo.Update, then drop cached sessions through an optional interface{ Forget(id int64) } on the registry impls;
       - QBittorrent.Forget deletes sessions[id] only and keeps loginMu[id], so an in-flight login keeps its single-flight;
       - Reset the client's connstatus when [INT-01](#int-01) is wired.
    3. httpapi/downloadclients.go handleUpdateDownloadClient for PUT /api/v1/downloadclients/{id} (RoleManager):
       - body is createClientRequest; validate name, url and kind=qbittorrent as create does;
       - 404 when missing; return the updated client (Password is json:"-").
    4. The list response adds bundled = (url == cfg.QbittorrentURL) per client. EnsureBundled re-creates a row whenever that URL is missing (service.go:28-49), so editing the bundled URL would spawn a duplicate until [INT-11](#int-11).
    
    Frontend (DownloadClients.tsx)
    5. An Edit button opens the add form prefilled (name, url, username). The password placeholder reads 'unchanged; leave blank to keep'. On the bundled row the URL is read-only, with a hint.
    6. An Enabled toggle on each card saves via PUT. Disabled cards are dimmed with 'Disabled: gets no new downloads'.
    7. After Save, run Test automatically and show the result inline.
  - **Files:** `internal/download/repo.go`, `internal/download/service.go`, `internal/download/qbittorrent.go`, `internal/download/repo_test.go`, `internal/httpapi/downloadclients.go`, `internal/httpapi/downloadclients_test.go`, `internal/httpapi/server.go`, `web/src/lib/api.ts`, `web/src/pages/DownloadClients.tsx`
  - **Acceptance:**
    - After changing the qBittorrent password in Edit and saving, Test passes with the new password without restarting Arrmada.
    - Leaving the password blank keeps the stored one.
    - Disabling a client stops new grabs going to it, and it is excluded from the queue and stall checks (QueueComplete already uses ListEnabled). The disabled state survives a restart.
    - The bundled client's URL can't be edited from the UI.
  - **Tests:** Go: Repo.Update keeps the password when blank and replaces it when set.; Go: PUT returns 200/400/404, and 403 for a requester.; Go qbittorrent_test: Forget drops the cached session, so the next call logs in again (httptest counts /api/v2/auth/login).; Manual: the edit flow against a throwaway qBittorrent container with a password (the bundled one bypasses auth).
  - **Risk:** The password must never be returned or echoed into the form. Keep loginMu intact on Forget so concurrent callers don't trigger two logins, which qBittorrent bans after repeated attempts.
  - **Resolves:** integrations-3, frontend-10
<a id="int-06"></a>
- [x] **INT-06 · A live Test for every metadata key, including before saving** — `P1` · `M` · Phase 4
  - **Problem:** Only Hardcover's key can be tested:
- Only Hardcover is Testable (apikeys.go:57), and handleTestAPIKey handles only 'hardcover' (httpapi/apikeys.go:27-33).
- TMDB, the one required key, has no test. A mistyped key, or a v4 token pasted where the v3 key belongs, looks saved, and Discover is simply empty.
- The setup wizard validates nothing (SetupWizard.tsx:103-109).
  - **Approach:** 1. apikeys.Catalog: Testable=true for tmdb, tvdb, omdb and opensubtitles_api. The OpenSubtitles username and password are tested through the api-key row.
    2. Verifiers. Each takes an optional candidate key; empty means the stored one.
       - metadata.TMDB.VerifyKey(ctx, key): GET /3/configuration. 200 → 'OK: images from <secure_base_url>'. 401 → 'TMDB rejected the key; use the v3 API key, not the v4 read access token', adding 'this looks like a v4 token' when the key starts with 'eyJ'.
       - metadata.TVDB.VerifyKey(ctx, key): POST /v4/login with the key, without touching the cached token (tvdb.go:186).
       - metadata.OMDb.VerifyKey(ctx, key): GET ?apikey=…&i=tt0111161 and check Response=True. OMDb's own Error text is passed through ('Invalid API key!', 'Request limit reached!').
       - subtitles.OpenSubtitles.Verify(ctx, override): with only an API key, a cheap Api-Key request; with a username and password, login (opensubtitles.go:143) then GET /infos/user, reporting 'N downloads left today'. It names whichever credential is missing. SUB's credential test (subtitles-11) shares it.
       - Hardcover keeps VerifyHardcover (stored key only, unless a candidate is trivial to pass).
    3. httpapi.Deps.KeyVerifiers map[string]func(ctx context.Context, candidate string) (string, error), built in main.go. handleTestAPIKey dispatches on it and keeps the hardcover case. Unknown ids → 400. It takes an optional body {value}: a candidate is tested and never persisted.
    4. Testing the stored value records the outcome in connstatus (kind 'apikey', ref = id) when [INT-01](#int-01) is in, so the hub and the health panel can say 'TMDB key rejected'.
    5. Settings.tsx APIKeysSection:
       - a Test button for every testable configured key (the condition at ~line 530);
       - a Test next to an unsaved typed value (candidate test);
       - auto-test after Save, showing ✓/✗ with the detail text.
    6. SetupWizard.tsx TMDB step: test on paste or blur via the candidate route. On ✗ it shows TMDB's message, and Next asks 'Save anyway?'. If CFG/APP rework the wizard, they reuse the same endpoint.
    7. Never log key values. TMDB sends api_key as a query parameter, which sanitizeErr already covers; add a test that proves it.
  - **Files:** `internal/apikeys/apikeys.go`, `internal/httpapi/apikeys.go`, `internal/httpapi/apikeys_test.go`, `internal/httpapi/server.go`, `internal/metadata/tmdb.go`, `internal/metadata/tvdb.go`, `internal/metadata/omdb.go`, `internal/metadata/verify_test.go`, `internal/subtitles/opensubtitles.go`, `cmd/arrmada/main.go`, `web/src/pages/Settings.tsx`, `web/src/pages/SetupWizard.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - TMDB, TheTVDB, OMDb, Hardcover and OpenSubtitles each show a Test button when configured. Each makes a real request and reports the provider's message.
    - A wrong TMDB key, or a v4 token, gets the v3 hint. Pasting a wrong TMDB key in the wizard shows ✗ before saving.
    - Testing a candidate value never persists it, and the stored key is unchanged afterwards.
    - Missing OpenSubtitles username or password is named in the result.
  - **Tests:** Go: TMDB.VerifyKey via httptest with an overridable base (200, 401, and the JWT-shaped hint).; Go: OMDb.VerifyKey passes 'Invalid API key!' through.; Go: handleTestAPIKey dispatches to fake verifiers, rejects unknown ids, and a candidate test leaves the settings store untouched.; Go: a TMDB transport error string never contains the key.
  - **Risk:** Each verifier makes a live third-party call, so run them on demand only, never in health polling or CI. The owner tests with real keys through the UI. Coordinate the OpenSubtitles verifier with SUB to avoid two login paths.
  - **Resolves:** integrations-14, system-10

#### Milestone: M2: Integrations stop sabotaging themselves

_Prowlarr re-sync keeps the owner's scoping and disables indexers that are gone upstream. TorrentLeech recovers stale sessions and backs off failed logins. Torznab Test is honest and works on unsaved settings. Movie imports can't be broken by a category field. Clients have priority, fail-over, and a bundled client that stays removed. Usenet is no longer advertised or queried. Detail sheets open from cache._

<a id="int-07"></a>
- [x] **INT-07 · Prowlarr re-sync keyed on Prowlarr id, non-destructive, and no usenet imports** — `P1` · `M` · Phase 5
  - **Problem:** SyncProwlarr overwrites the owner's settings and leaves stale rows behind:
- It matches rows by the display name 'Prowlarr · <name>' (prowlarr.go:97-100).
- On update it writes nil MediaTypes and Categories, Enabled:true and Prowlarr's priority through repo.Update (prowlarr.go:113-126, repo.go:99-102). That wipes per-indexer scoping and re-enables indexers the owner turned off.
- Indexers disabled or deleted in Prowlarr are never disabled locally.
- Usenet indexers are imported as Newznab (105-108), though nothing can download them.
- It writes a FlareSolverr proxy pointing at arrmada-flaresolverr into any Prowlarr, and reports success only from whether the POST errored (144-196, Indexers.tsx:156).
- It has no tests.
  - **Approach:** 1. Migration 00xx_indexer_prowlarr_managed.sql:
       - indexers ADD prowlarr_id INTEGER NOT NULL DEFAULT 0, ADD disabled_by TEXT NOT NULL DEFAULT '', ADD managed_note TEXT NOT NULL DEFAULT '';
       - add them to indexerCols/scan and to the Indexer struct (ProwlarrID, DisabledBy, ManagedNote).
    2. Idempotent startup backfill, Service.BackfillProwlarrIDs(ctx, savedURL): rows whose URL is '<prowlarr_url>/<n>/api' and whose prowlarr_id=0 get prowlarr_id=n.
    3. Rewrite SyncProwlarr. Fetch /api/v1/indexer first.
       a. Safety: on an error or bad JSON, or an empty list while managed rows exist, return the error or 'Prowlarr returned no indexers; nothing changed' and touch nothing.
       b. Fixture: extend prowlarrIndexer with capabilities.categories [{id, subCategories}]. Verify the field names against the bundled Prowlarr 2.6.5 and save the JSON as testdata/prowlarr_indexers.json.
       c. Matching: match on prowlarr_id. Rows not yet backfilled fall back to the name and get prowlarr_id set.
       d. Existing rows: new repo.UpdateManaged(ctx, id, name, url, apiKey) writes only name, url and api_key. It never writes enabled, media_types, categories, priority, min_seeders or seed_*.
       e. Upstream disabled or missing → enabled=0, disabled_by='prowlarr', managed_note 'Disabled in Prowlarr' or 'Removed from Prowlarr'. Rows are never deleted.
       f. Upstream enabled again → re-enable only rows with disabled_by='prowlarr'. 'user' rows stay off.
       g. Usenet: protocol=usenet is skipped. Existing synced Newznab rows are disabled with the note 'Needs a usenet download client'.
       h. New rows: KindTorznab, prowlarr_id set, Prowlarr's priority (25 if outside 1-50), and the seed defaults. Media types come from the categories: 2000→movie, 5000→series, 7000 or 3030→book, 3000 other than 3030→music; nothing mapped → all areas.
       i. Renames: when a name changes, in the same transaction UPDATE grabs SET indexer=new WHERE indexer=old, and the same for blocklist. seedRules (automation/store.go:191) and Service.Fetch (service.go:117-131) look indexers up by name.
       j. SyncResult becomes {added, updated, disabled, reenabled, skipped_usenet, flaresolverr_ready, notes}.
    4. repo.Update (UI edits) sets disabled_by on transitions: enabled 1→0 sets 'user'; 0→1 clears disabled_by and managed_note.
    5. handleUpdateIndexer ignores name, url, api_key and kind from the body for rows with prowlarr_id>0.
    6. FlareSolverr proxy:
       - add it only when the Prowlarr host is the bundled one (u.Hostname()=='arrmada-prowlarr') or the request sets add_flaresolverr_proxy=true;
       - flaresolverr_ready is true only if GET /api/v1/indexerproxy lists a FlareSolverr proxy afterwards;
       - read the FlareSolverr URL from the [INT-03](#int-03) resolver if it has landed.
    7. Indexers.tsx:
       - the sync message lists what was added, updated, disabled and re-enabled;
       - line 156 claims FlareSolverr only when flaresolverr_ready;
       - a non-bundled URL shows a checkbox, 'Add Arrmada's FlareSolverr to this Prowlarr'.
  - **Files:** `internal/store/migrations/00xx_indexer_prowlarr_managed.sql`, `internal/indexer/prowlarr.go`, `internal/indexer/prowlarr_test.go`, `internal/indexer/testdata/prowlarr_indexers.json`, `internal/indexer/repo.go`, `internal/indexer/indexer.go`, `internal/indexer/service.go`, `internal/httpapi/prowlarr.go`, `internal/httpapi/indexers.go`, `cmd/arrmada/main.go`, `web/src/pages/Indexers.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Scope an indexer to Books only, disable another and change a third's seed rules, then sync: all three settings are unchanged.
    - Disabling an indexer in Prowlarr disables it in Arrmada on the next sync, with the note 'Disabled in Prowlarr', and re-enabling it upstream turns it back on. An indexer the user disabled stays off.
    - Renaming an indexer in Prowlarr renames the same Arrmada row with no duplicate, and existing grabs keep their seed rules.
    - Usenet indexers in Prowlarr are not imported.
    - Syncing an external (non-bundled) Prowlarr creates no FlareSolverr proxy unless the box is ticked, and the message never claims FlareSolverr is set up when it isn't.
    - A Prowlarr that errors or returns an empty list changes no local rows.
  - **Tests:** Go prowlarr_test with an httptest Prowlarr serving the fixture: (a) re-sync preserves user-disabled state, media_types, seed rules, min_seeders and local priority; (b) upstream disable → disabled_by=prowlarr, then re-enable; (c) removed upstream → disabled with a note, not deleted; (d) a rename keeps the row id and rewrites grabs.indexer; (e) usenet skipped; (f) the URL backfill sets prowlarr_id; (g) no proxy POST for a non-bundled host; (h) categories → media types for new rows; (i) an empty list or HTTP 500 → no changes.; Go: handleUpdateIndexer ignores name and URL changes on a managed row; the disabled_by transitions in repo.Update.
  - **Risk:** Prowlarr's API field names (capabilities, protocol, enable) must be checked against the real bundled instance before shipping. Disabling rows because of an API hiccup would be bad, hence the empty or failed-list guard. The rename rewrite of grabs and blocklist must be one transaction.
  - **Resolves:** integrations-2, integrations-4
<a id="int-08"></a>
- [x] **INT-08 · TorrentLeech: recover stale sessions, single-flight logins and back off failed logins** — `P1` · `M` · Phase 5
  - **Problem:** TorrentLeech keeps broken sessions and keeps retrying failed logins:
- The session, including FlareSolverr's cf_clearance cookie, is cached forever (torrentleech.go:157-172).
- It is dropped only on 'not logged in' (187-194) or an HTML 200 in Fetch (281-287). A Cloudflare 403 or 503, or a non-JSON reply, keeps returning 'try again' until the container restarts.
- A failed login caches nothing, so every search in a sweep repeats a FlareSolverr solve and a login POST against a private tracker, which can get the account flagged.
- Concurrent searches can each log in.
- The first login runs inside the 25s per-indexer budget (service.go:351-355), but a solve can take 60s.
  - **Approach:** 1. Session recovery:
       - search() returns a typed errTLSession for non-JSON, 403 or 503 replies and for login-form pages;
       - Search drops the session and retries exactly once on that error;
       - Fetch also drops the session on 403 and 503.
    2. Login single-flight: guard newSession with a per-indexer mutex and re-check the cache after acquiring it (the qbittorrent.go:72-105 pattern), so concurrent searches cause one login.
    3. Login backoff, per indexer as {n, until, lastErr}:
       - after a failed login, 15m doubling to a 6h cap; 'check username/password' and '2FA' failures go straight to 6h;
       - while paused, session() returns 'TorrentLeech login paused until 15:42 after: <err>' without contacting the site;
       - Test bypasses the backoff and clears it on success;
       - TorrentLeechSearcher.Reset(id) is called through [INT-01](#int-01)'s Registry.Reset from Service.Update and Delete. It clears the session and the backoff, so changed credentials apply immediately.
    4. Run the first login outside the search budget:
       - start it with context.WithoutCancel(ctx) and a 90s timeout inside the single-flight;
       - the search waits only within its own ctx;
       - if the search times out, the login still finishes and the next search uses the cached session.
    5. Record login outcomes in connstatus (kind indexer) so the last login error shows on the row. The search outcome is already recorded by [INT-01](#int-01).
    6. Make tlBaseURL a searcher field (default unchanged) so tests can point it at httptest.
  - **Files:** `internal/indexer/torrentleech.go`, `internal/indexer/torrentleech_test.go`, `internal/indexer/searcher.go`, `internal/indexer/service.go`
  - **Acceptance:**
    - After a Cloudflare 403 or 503, or a non-JSON reply, the next search logs in again once and succeeds, with no container restart.
    - With a wrong password there is at most one login attempt per backoff window, none once the cap is reached until credentials are edited or Test is pressed, and the error says when logins resume.
    - Five concurrent searches cause exactly one login POST.
    - A first search that needs a FlareSolverr solve doesn't permanently fail with 'context deadline exceeded', and the following search reuses the session.
  - **Tests:** Go torrentleech_test against an httptest TorrentLeech stand-in: a 403 drops the session and retries once; login failures follow the ladder; Reset clears them; N goroutines → 1 login POST; a login slower than the search ctx still populates the cache.; Go: Test bypasses the backoff and clears it on success.; go test -race ./internal/indexer/... in Docker.
  - **Depends on:** [INT-01](#int-01)
  - **Risk:** A TorrentLeech account ban is the worst outcome, so the backoff errs toward fewer attempts. Live verification is by the owner through the UI with their own credentials, never scripted. The detached login goroutine must respect shutdown (the 90s cap).
  - **Resolves:** integrations-8
<a id="int-09"></a>
- [x] **INT-09 · Torznab Test parses caps and error documents; test unsaved indexer settings** — `P2` · `M` · Phase 5
  - **Problem:** The Torznab Test can't tell a working indexer from a broken one:
- TorznabSearcher.Test requests t=caps and returns only get()'s error (torznab.go:286-294).
- get() checks only for HTTP 200 (296-318), so Prowlarr's web page, served at the root URL, passes as '✓ Connected'.
- Torznab/Newznab <error code description> documents are never recognised, so a bad key shows up later as 'parse feed: expected element type <rss>'.
- Caps are thrown away.
- Test works only on saved ids, so a broken indexer has to be saved before it can be tested.
  - **Approach:** 1. New internal/indexer/caps.go: parseCaps(body) (Caps, error).
       - Caps = {Search, TV, Movie, Music, Audio, Book SearchMode{Available bool, Params []string}, Categories []int (subcats included), LimitsMax, LimitsDefault}.
       - Element and attribute names are matched case-insensitively.
       - The root must be <caps>. A root <error> becomes *TorznabError{Code, Description}, whose message is the description.
       - HTML or anything else gives: 'This is a web page, not a Torznab API; the URL should end in /api (for Prowlarr: http://host:9696/<id>/api)'.
    2. parseFeedPage checks for <error> and HTML before RSS parsing, so search errors read e.g. 'Incorrect user credentials'.
    3. TorznabSearcher.Test returns caps through an optional CapsTester interface. Service.Test stores them with the new repo.SetCaps(id, json).
    4. Migration 00xx_indexer_caps.sql: indexers ADD caps_json TEXT NOT NULL DEFAULT '', ADD caps_at TIMESTAMP.
    5. Caps refresh:
       - after a Prowlarr sync, for added rows (each Prowlarr indexer answers t=caps at <prowlarr>/<id>/api);
       - a daily scheduler job 'indexer-caps-refresh' for Torznab rows whose caps are missing or older than 7 days, through the normal throttle.
    6. New POST /api/v1/indexers/test (RoleManager):
       - takes a createIndexerRequest body and tests unsaved settings for every kind (Torznab caps, TorrentLeech login, MAM, 1337x);
       - an optional id fills in blank secrets from the stored row (Edit's 'blank = keep');
       - returns {ok, error, caps_summary} without creating a row.
    7. GET /indexers includes caps_summary, e.g. 'Movies (imdbid, tmdbid) · TV (tvdbid, season, ep) · 23 categories'. Indexers.tsx shows it under the row, and EditForm gets a 'Test these settings' button that uses the new route.
  - **Files:** `internal/indexer/caps.go`, `internal/indexer/torznab.go`, `internal/indexer/torznab_test.go`, `internal/indexer/testdata/caps_prowlarr.xml`, `internal/indexer/testdata/caps_jackett.xml`, `internal/indexer/service.go`, `internal/indexer/repo.go`, `internal/store/migrations/00xx_indexer_caps.sql`, `internal/httpapi/indexers.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`, `web/src/lib/api.ts`, `web/src/pages/Indexers.tsx`
  - **Acceptance:**
    - Testing a Torznab indexer set to Prowlarr's root URL fails with the 'web page, not a Torznab API' message.
    - A wrong API key reports the indexer's own description (e.g. 'Incorrect user credentials').
    - A passing Test stores caps, and the row shows the supported search modes and category count.
    - A search against an endpoint returning <error> shows its description in the per-indexer error, not an XML parser error.
    - POST /indexers/test checks unsaved settings and creates no row; with an id, a blank secret uses the stored one.
  - **Tests:** Go: parseCaps on the Prowlarr and Jackett fixtures, an <error> document, an HTML page, and mixed-case attributes.; Go: Test against an httptest server returning HTML with 200 → error.; Go: parseFeedPage on an <error> document → TorznabError with the description.; Go: handler test for POST /indexers/test, unsaved and with an id merging the stored secret; row count unchanged.; Go: errors produced by caps parsing still pass redaction.
  - **Risk:** Some Jackett or Prowlarr builds vary in attribute casing, so parse case-insensitively. Old rows have no caps until they are tested or refreshed, and must keep working exactly as today without them.
  - **Resolves:** integrations-5, integrations-10, integrations-12
<a id="int-10"></a>
- [x] **INT-10 · Arrmada owns download categories: retire the free-text Category field** — `P2` · `S` · Phase 5
  - **Problem:** The Category field on the Add client form can silently stop every movie from importing:
- The field is free text (DownloadClients.tsx:140, 176-181).
- Movie grabs pass category "" (coordinator.go:125-126, and the movie upload path at ~228), so qBittorrent falls back to dc.Category (qbittorrent.go:203-209).
- The movie import sweep only reads cfg.DownloadCategory (main.go:334).
- Any other value means movies download and are never imported. On installs without the bundled client that is every movie.
- Series, books and music hard-code arrmada-tv, arrmada-books and arrmada-music.
  - **Approach:** 1. Coordinator gets a movieCategory field, set from cfg.DownloadCategory in main.go (default 'arrmada') with a setter or constructor option. Grab (coordinator.go:126) and the movie addTorrentFile path pass it explicitly.
    2. qbittorrent.Add stops falling back to dc.Category. An empty category is sent as no category, which no longer happens.
    3. createClientRequest.Category is ignored on create and on update ([INT-05](#int-05)). The Add/Edit forms lose the field. The client card shows the categories read-only: 'Categories: arrmada (movies) · arrmada-tv · arrmada-books · arrmada-music', with the movie value taken from a new field on the list response.
    4. Keep the DB column (no migration). Stop showing the per-client category badge.
  - **Files:** `internal/automation/coordinator.go`, `internal/download/qbittorrent.go`, `internal/download/qbittorrent_test.go`, `internal/httpapi/downloadclients.go`, `cmd/arrmada/main.go`, `web/src/pages/DownloadClients.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - The client forms have no Category field, and the card lists Arrmada's fixed categories.
    - A movie grabbed through a client whose stored category is 'movies' is added under the configured movie category ('arrmada') and imports.
  - **Tests:** Go: a fake Downloader captures AddRequest.Category for a movie Grab and a movie torrent upload, and it equals the configured category.; Go qbittorrent_test: the multipart 'category' field comes from the request, never from dc.Category.
  - **Risk:** Low. Movies already stuck in a custom category on non-bundled installs won't move by themselves; they need a re-grab, or their category changed in qBittorrent. Mention that in the commit message.
  - **Resolves:** integrations-6
<a id="int-11"></a>
- [x] **INT-11 · Download client priority, add fail-over, and a bundled client that stays disabled or removed** — `P2` · `M` · Phase 5
  - **Problem:** With more than one client, the order is fixed, the bundled client keeps coming back, and one dead client stops stall checks:
- Add always uses clients[0] (service.go:80).
- EnsureBundled re-creates the bundled client on every start if its URL is missing (service.go:28-49, main.go:212-215), so it can be neither removed nor moved.
- SetBundledPort, SetBundledSavePath and EnsureBundledQueue act on it even when it is disabled.
- One unreachable second client makes QueueComplete partial, and stall detection then skips every cycle with only a log line (coordinator.go:1208-1215).
  - **Approach:** 1. Migration 00xx_download_client_priority.sql: download_clients ADD priority INTEGER NOT NULL DEFAULT 25, ADD bundled INTEGER NOT NULL DEFAULT 0. At startup, EnsureBundled marks the row whose URL equals cfg.QbittorrentURL as bundled=1.
    2. EnsureBundled is a no-op when any bundled row exists (enabled or not, whatever its URL), or when the settings key download_bundled_removed=1. Deleting the bundled row sets that key.
       - SetBundledPort, SetBundledSavePath and EnsureBundledQueue look the client up by the bundled flag and skip it when it is disabled.
       - [INT-05](#int-05)'s URL lock on the bundled row can then be lifted.
    3. ListEnabled orders by priority, then id. Service.Add tries clients in order and fails over to the next only on transport errors (no HTTP response: *url.Error, dial or timeout before headers), never on 'add rejected' or HTTP errors. Log which client took the add.
    4. PUT /downloadclients/{id} ([INT-05](#int-05)) accepts priority.
    5. New POST /api/v1/downloadclients/restore-bundled (RoleManager): clears download_bundled_removed and calls EnsureBundled.
    6. The DownloadClients.tsx card shows:
       - a 'bundled' badge and a priority field labelled 'Order (1 = first)';
       - 'Restore bundled qBittorrent' when no bundled row exists;
       - when connstatus says an enabled client is unreachable: 'Unreachable since 14:02. While it's down, stalled-download fail-over is paused for all clients; disable it if it's gone.'
    7. Update [INT-04](#int-04)'s delete-confirm text for the bundled client: it now stays removed and can be restored.
  - **Files:** `internal/store/migrations/00xx_download_client_priority.sql`, `internal/download/repo.go`, `internal/download/service.go`, `internal/download/client.go`, `internal/download/service_test.go`, `internal/httpapi/downloadclients.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`, `web/src/lib/api.ts`, `web/src/pages/DownloadClients.tsx`
  - **Acceptance:**
    - Disabling or deleting the bundled client survives a restart, and 'Restore bundled qBittorrent' brings it back.
    - With two enabled clients, adds go to the lowest priority number; if that client is unreachable the add goes to the next one, and an add rejected by the first client is not retried elsewhere.
    - Disabling a dead second client brings stall fail-over back, and the card explains why while it is down.
    - Bundled port, save-path and queue tuning are skipped for a disabled bundled client.
  - **Tests:** Go repo: ListEnabled orders by priority; the bundled flag is set at startup.; Go: EnsureBundled respects a disabled bundled row, an edited bundled URL and the bundled-removed flag.; Go: Add fails over only on transport errors (two httptest qBittorrent servers, one closed; one returning 'Fails.' is not failed over).
  - **Depends on:** [INT-05](#int-05), [INT-01](#int-01)
  - **Risk:** Failing over after a timeout could double-add if the first client actually accepted the torrent, so only fail over when no HTTP response was received. The owner runs only the bundled client, so this mostly protects other setups. Test against a throwaway second qBittorrent container.
  - **Resolves:** integrations-3
<a id="int-12"></a>
- [x] **INT-12 · Stop advertising usenet; don't query Newznab indexers without a usenet client** — `P3` · `S` · Phase 5
  - **Problem:** Usenet is offered but can never work, and it still costs search time:
- Indexers.tsx:45 says 'Torznab (torrent) and Newznab (usenet) search sources', and the Add form offers Newznab (277).
- The only download client kind is qBittorrent (client.go:12-14), usenet grabs are refused (coordinator.go:140-148), and grabbable() drops usenet releases (628-637).
- Usenet indexers are still queried on every search and RSS pull, taking slots in the shared per-host throttle for results that are always discarded.
  - **Approach:** 1. Indexers.tsx:
       - header copy becomes 'Torrent search sources: trackers and Prowlarr. Every module searches through these.';
       - remove the Newznab option from the Kind select.
    2. handleCreateIndexer rejects kind newznab with 400 'Usenet isn't supported yet; Arrmada has no usenet download client'. Existing rows can still be edited or deleted.
    3. indexer.Service.Search and fetchRecent skip indexers whose Transport() is usenet unless an injected usenetAvailable func() bool returns true. It returns false today; wire it from download.Service if a usenet kind is ever added. Log the skip once per indexer (sync.Map, like unknownKindLogged).
    4. Existing Newznab rows show the badge 'Not searched: needs a usenet download client'.
    5. Prowlarr sync skipping usenet is in [INT-07](#int-07). MOV handles usenet rows in interactive search (movies-10).
  - **Files:** `web/src/pages/Indexers.tsx`, `internal/httpapi/indexers.go`, `internal/indexer/service.go`, `internal/indexer/indexer.go`
  - **Acceptance:**
    - The Add form has no Newznab option, and the page copy doesn't mention usenet.
    - Creating a newznab indexer through the API returns 400 with the explanation.
    - An existing Newznab indexer receives no search or RSS requests and shows the 'not searched' badge.
  - **Tests:** Go: Search with a newznab httptest server and no usenet client → 0 requests reach it.; Go: the handler rejects kind=newznab on create but still accepts an update of an existing newznab row.
  - **Risk:** Low. If usenet support is ever added, flip usenetAvailable and restore the option. SABnzbd and NZBGet stay out of scope.
  - **Resolves:** integrations-4
<a id="int-13"></a>
- [x] **INT-13 · Cache title details and OMDb ratings; ratings never hold up the detail sheet** — `P2` · `S` · Phase 5
  - **Problem:** Detail sheets refetch everything and can wait on OMDb:
- MediaDetails calls t.get directly, with no memory or disk cache (metadata/discover.go:104-186; tmdb.go:105-128).
- OMDb.Ratings has no cache and a 12s timeout (omdb.go:27-84). It is wired with no DiskCache (main.go:159), unlike TMDB and Hardcover.
- handleMediaDetail runs both one after the other on every sheet open and every 'More like this' hop (httpapi/discover.go:262-273). It drops OMDb errors, including the free tier's 'Request limit reached!'.
- Every request create calls MediaDetails again (requests.go:72-73).
  - **Approach:** 1. metadata/discover.go: wrap MediaDetails in swr(ctx, t.disk, "tmdb:detail:v1:"+media+":"+id, 12*time.Hour, fetch). swr never stores errors.
    2. metadata/omdb.go:
       - add SetDiskCache(c *DiskCache) and wrap Ratings in swr with key "omdb:"+imdbID and a 7-day TTL;
       - a 'Response False' movie-not-found becomes an empty Ratings with a nil error, cached for 1 day;
       - key and quota errors are returned and not cached;
       - keep lastErr and lastErrAt behind a mutex, exposed as LastError(), and record them in connstatus (kind apikey, ref omdb) when [INT-01](#int-01) is in.
    3. cmd/arrmada/main.go: call omdb.SetDiskCache(diskCache) next to the TMDB and Hardcover calls (~155-164).
    4. handleMediaDetail fetches ratings in a goroutine on a detached context with a 12s budget, and waits at most 2s. If they're late, it returns without them; the fetch finishes in the background and fills the cache for the next open. Log a quota error at most once an hour.
    5. handleGetAPIKeys adds last_error and last_error_at for omdb, and Settings.tsx shows them under the key.
  - **Files:** `internal/metadata/discover.go`, `internal/metadata/omdb.go`, `internal/metadata/omdb_test.go`, `internal/httpapi/discover.go`, `internal/httpapi/apikeys.go`, `cmd/arrmada/main.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Reopening a title within 12h makes no TMDB detail call, and request-create canonicalisation hits the cache.
    - Ratings badges appear on later opens without another OMDb call for 7 days.
    - With OMDb slow, unreachable or over quota, the sheet returns within about 2s plus TMDB time, showing the TMDB score only.
    - When OMDb returns 'Request limit reached!', Settings shows that message with its time.
  - **Tests:** Go: TestMediaDetailsCached; a fake TMDB server counts hits, and the second call is served from the DiskCache (store.Open temp DB).; Go: TestOMDbRatingsCachedIncludingNotFound; a quota error is not cached and is recorded as lastErr.; Go: TestMediaDetailRatingsTimeout; with a slow fake OMDb the handler returns under the budget without ratings.
  - **Risk:** A 12h cache can show a stale TMDB status or certification, which is fine for display. Canonicalisation only uses title, poster and overview. The cache key is versioned (v1) so later field additions (cast ids, seasons, collection) can invalidate it cleanly.
  - **Resolves:** discover-14, integrations-14

#### Milestone: M3: One Connections hub

_The health panel warns about failing integrations and links to the fix. One /connections page shows every integration with live status, Test, Edit, Disable and Delete. Indexers are added from presets with test-before-save. The old Indexers and Download clients pages redirect into the hub._

<a id="int-14"></a>
- [ ] **INT-14 · Health panel warnings from integration status** — `P2` · `S` · Phase 9
  - **Problem:** handleSystemHealth (health_system.go:22-100) misses the failures that actually stop acquisition:
- It only checks that at least one indexer is enabled and that Queue() succeeds. Queue() errors only when every client fails (download/service.go:342-344).
- A failing tracker, a dead FlareSolverr while TorrentLeech or 1337x depend on it, an unreachable second client that has paused stall fail-over, and a rejected TMDB key all leave the panel green.
- Its warnings are plain text with no link to the fix.
  - **Approach:** 1. healthWarning gains Link string json:"link,omitempty" (e.g. '/connections#indexer-12', or '/indexers' before the hub exists). Dashboard.tsx renders the message as a link when Link is set. Coordinate the field with OBS if it is reshaping this endpoint.
    2. New checks, all read from connstatus (no live calls except FlareSolverr's cached Status):
       - Indexers: each enabled indexer that is backing_off, or failing for more than 1h → warning 'TorrentLeech has been failing since 14:02: login failed (next try 15:00)'. With more than 3, collapse to one line, '4 indexers are failing'. When every enabled indexer is failing → error 'No indexer is answering; searches will find nothing'.
       - FlareSolverr: an enabled torrentleech or 1337x indexer exists and FlareSolverr is not configured or not answering → warning naming the dependent indexers.
       - Download clients: any enabled client with last outcome failing → warning 'qBittorrent (seedbox) is unreachable: <err>. Stalled-download fail-over is paused while it's down.' This sits alongside the existing all-clients check.
       - TMDB: not configured → error 'Movie and TV metadata need a TMDB key'. Last recorded test failed → warning 'TMDB rejected the key'.
       - Prowlarr: once [INT-19](#int-19) lands, a last scheduled sync error older than 2 runs → warning.
    3. Warnings never include secrets (connstatus.Redact already applies).
  - **Files:** `internal/httpapi/health_system.go`, `internal/httpapi/health_system_test.go`, `internal/httpapi/server.go`, `web/src/pages/Dashboard.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A tracker that has been backing off for more than an hour produces a health warning naming it, its error and its next try, and the warning links to its card or row.
    - Stopping the FlareSolverr container while a TorrentLeech indexer is enabled produces a FlareSolverr warning within a minute.
    - An unreachable second download client produces a warning naming it, even though the queue partly loads.
  - **Tests:** Go: handleSystemHealth with a fake connstatus: failing indexer → warning with a link; all failing → error; FlareSolverr down plus a TorrentLeech indexer → warning; one client failing → warning; nothing failing → status ok.
  - **Depends on:** [INT-01](#int-01), [INT-03](#int-03)
  - **Risk:** Health is polled, so these checks must read cached status only and never make live third-party calls. If OBS rebuilds the health panel (system-8), port these checks into its structure rather than adding them twice.
  - **Resolves:** product-5, integrations-1, integrations-3, integrations-13
<a id="int-15"></a>
- [ ] **INT-15 · Connections API: one aggregated status endpoint and a per-connection Test** — `P1` · `M` · Phase 9
  - **Problem:** No single view answers 'is everything connected and healthy?':
- Plex lives in Insights → Settings, Apprise in Insights → Notifications, and API keys in Settings → System.
- Indexers and Download clients are separate pages, and FlareSolverr and Prowlarr appear nowhere.
- The Plex sign-in toggle tells you to connect Plex in Insights but can't tell whether it is connected (system-9, frontend-10).
The hub UI needs one backend source.
  - **Approach:** 1. New internal/httpapi/connections.go, GET /api/v1/connections (RoleManager), returns {sections:[{id, title, items:[ConnectionItem]}]}.
       - ConnectionItem = {kind, ref, name, subtitle, configured, enabled, state: ok|failing|backing_off|warn|disabled|unknown, detail, last_ok_at, last_error, last_error_at, failing_since, backoff_until, checked_at, counts:{queries_24h, failures_24h, grabs_24h}, testable, managed_by, fix_path}.
       - All status comes from connstatus. Anything never checked is 'unknown', with a Test action.
    2. Sections:
       - search: Prowlarr (saved URL and has_key, last sync from settings key prowlarr_last_sync when [INT-19](#int-19) exists, status kind prowlarr); FlareSolverr (configured, source, fs.Status); each indexer (status, Counts24h, grabs_24h via SELECT indexer, COUNT(*) FROM grabs WHERE grabbed_at > datetime('now','-1 day') GROUP BY indexer, managed_by 'prowlarr' when prowlarr_id>0);
       - downloads: each client (enabled, bundled, priority, status kind download_client);
       - metadata: each apikeys.Status entry except the OpenSubtitles username and password rows, plus status kind apikey;
       - other: Plex (insights_plex_url configured, token present as a boolean, server name if stored, status kind plex), Apprise (count of notification targets, links to its editor), audiobook server (enabled and AudioManager.Running()).
    3. New POST /api/v1/connections/{kind}/{ref}/test (RoleManager) dispatches to:
       - indexer → Indexers.Test; download_client → Downloads.Test; flaresolverr → Ping; apikey → the KeyVerifiers from [INT-06](#int-06);
       - prowlarr → a new indexer.ProwlarrStatus(ctx, url, key) calling GET /api/v1/system/status, which returns the version;
       - plex → the existing handleInsightsTest logic with the stored credentials;
       - audioserver → Running().
       It records the outcome in connstatus and returns the refreshed ConnectionItem. Unknown kind or ref → 404.
    4. Record Plex outcomes: handleInsightsTest also writes connstatus kind plex, so the card has a last check.
    5. Never include secrets: no api_key, password, token or full URL query strings. Prowlarr's URL is shown without its key.
  - **Files:** `internal/httpapi/connections.go`, `internal/httpapi/connections_test.go`, `internal/httpapi/server.go`, `internal/httpapi/insights.go`, `internal/indexer/prowlarr.go`, `cmd/arrmada/main.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - GET /connections lists Prowlarr, FlareSolverr, every indexer, every download client, TMDB, TVDB, OMDb, Hardcover, OpenSubtitles, Plex, Apprise and the audiobook server, each with a state and its last-OK or last-error data.
    - Indexer items carry 24h queries, failures and grabs.
    - POST /connections/{kind}/{ref}/test runs a real check, records it, and returns the updated item; an unknown id returns 404.
    - A requester gets 403, and no response contains a secret.
  - **Tests:** Go TestConnectionsListShape with fakes for each kind.; Go TestConnectionsNeverLeakSecrets: marshal the response, then grep for api_key, password, token and the fake key values.; Go TestConnectionTestUnknownID404 and the requester 403.
  - **Depends on:** [INT-01](#int-01), [INT-03](#int-03), [INT-06](#int-06)
  - **Risk:** It aggregates many dependencies, some of which may be nil in tests or old wiring; every section must tolerate nil deps and show 'unknown'. Keep it read-only apart from /test, because editing stays with the existing endpoints.
  - **Resolves:** system-9, frontend-10, integrations-12, integrations-1
<a id="int-16"></a>
- [ ] **INT-16 · Connections page: status cards with Test and live refresh** — `P1` · `M` · Phase 9
  - **Problem:** Answering 'is everything working?' means visiting six screens. The Indexers and Download clients pages are bare stacked forms with no status. The Plex sign-in toggle hint can't say whether Plex is connected.
  - **Approach:** 1. New web/src/pages/Connections.tsx at /connections, in AppLayout. If CFG's Settings hub has landed, mount it as its Connections section at /settings/connections and redirect /connections there.
    2. Components under web/src/components/connections/:
       - StatusDot: --good ok, --avoid failing or warn, --reject backing_off, --ink-faint unknown or disabled.
       - ConnectionCard: name, a mono uppercase kind label in the existing style, the dot, one status line ('Last OK 3 min ago' or 'Failing since 14:02: login failed · next try 15:00'), 24h counts for indexers ('214 searches · 3 grabs · 3 failed'), and a 'Managed by Prowlarr' badge when relevant.
       - ConnectionSection: section title, anchor ids #search, #downloads, #metadata and #other.
    3. One primary action per card: Test (POST /connections/{kind}/{ref}/test, updating the card in place), or Fix when failing, which deep-links to where it is edited today (/indexers#<id>, /downloadclients, /settings API keys, Insights → Settings for Plex, Insights → Notifications for Apprise). [INT-17](#int-17) replaces those links with in-hub drawers.
    4. Responsive grid: 1 column on phones, 2 on tablet, 3 on desktop. Existing tokens and type scale only.
    5. Live refresh: useLive(); on topic 'integration.status', refetch GET /connections (debounced 1s).
    6. Sidebar (lib/nav.ts): add 'Connections' to the System group. Indexers and Download clients stay until [INT-17](#int-17).
    7. The Plex sign-in toggle hint in Settings → System reads the plex item: 'Plex connected ✓ (<server>)' or 'Connect Plex first', with a link to the card.
    8. Each card has a stable id (indexer-12, download_client-1, flaresolverr), so [INT-14](#int-14)'s health links land on the right card.
  - **Files:** `web/src/pages/Connections.tsx`, `web/src/components/connections/ConnectionCard.tsx`, `web/src/components/connections/StatusDot.tsx`, `web/src/components/connections/ConnectionSection.tsx`, `web/src/App.tsx`, `web/src/lib/nav.ts`, `web/src/lib/api.ts`, `web/src/pages/Settings.tsx`
  - **Acceptance:**
    - One page shows cards for Prowlarr, FlareSolverr, every indexer, every download client, TMDB, TVDB, OMDb, Hardcover, OpenSubtitles, Plex, Apprise and the audiobook server, each with a status dot and its last-OK or last-error text.
    - Test updates the card in place, and a failing qBittorrent shows red with the error.
    - Stopping FlareSolverr turns its dot red after Test, or within a minute through live refresh, without a page reload.
    - The Plex sign-in toggle shows 'Plex connected ✓' when the Plex item is ok.
    - It works at 375px with no horizontal scroll and matches the existing palette and type scale.
  - **Tests:** UI check at desktop and 375px: every section, Test in place, and the Fix deep links.; UI check: websocket-driven refresh after stopping the FlareSolverr container.; UI check: a health warning link scrolls to the matching card.
  - **Depends on:** [INT-15](#int-15)
  - **Risk:** This is a large visible change, so keep strictly to the existing palette and type scale. Use the FE kit's components if they have landed, otherwise local ones that can be swapped later. The page must stay useful when some statuses are 'unknown' (never tested).
  - **Resolves:** system-9, frontend-10, integrations-12
<a id="int-17"></a>
- [ ] **INT-17 · Edit, disable and delete from the hub; the old pages redirect** — `P2` · `M` · Phase 9
  - **Problem:** Once INT-16 ships, editing still bounces to the old pages, and the sidebar has three entries for one concept.
  - **Approach:** 1. Move EditForm, SeedingRules, MediaPills and ProwlarrSync from Indexers.tsx, and the client form from DownloadClients.tsx, into web/src/components/connections/ (IndexerEditor.tsx, ClientEditor.tsx, ProwlarrPanel.tsx). Keep [INT-04](#int-04)'s fixes: pill semantics, delete confirm, responsive grids.
    2. An EditDrawer (the FE kit Drawer if available) hosts them. Each card's overflow menu offers Edit, Disable/Enable and Delete (confirmed, with an error shown on failure). Disable on a Prowlarr-managed indexer sets disabled_by='user' ([INT-07](#int-07)).
    3. Search section header: Prowlarr panel (sync, last sync), '+ Add indexer' ([INT-18](#int-18), or the existing AddForm until then). Downloads header: '+ Add client' and 'Restore bundled qBittorrent' ([INT-11](#int-11)).
    4. The FlareSolverr card edits its URL inline through PUT /apikeys/flaresolverr. Metadata cards edit keys inline with CFG's key-field semantics (blank never silently clears), plus Test before save ([INT-06](#int-06)).
    5. App.tsx: /indexers → /connections#search and /downloadclients → /connections#downloads (Navigate replace). Delete Indexers.tsx and DownloadClients.tsx once nothing imports them.
    6. lib/nav.ts: replace the Indexers and Download clients entries with the single Connections entry.
    7. Plex and Apprise cards keep linking to their editors. PLEX moves the Plex editor into its card.
  - **Files:** `web/src/components/connections/IndexerEditor.tsx`, `web/src/components/connections/ClientEditor.tsx`, `web/src/components/connections/ProwlarrPanel.tsx`, `web/src/components/connections/EditDrawer.tsx`, `web/src/pages/Connections.tsx`, `web/src/pages/Indexers.tsx`, `web/src/pages/DownloadClients.tsx`, `web/src/App.tsx`, `web/src/lib/nav.ts`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Every indexer and download client can be edited, disabled, re-enabled and deleted (confirmed) from its card.
    - /indexers and /downloadclients redirect to the matching hub section, and the sidebar shows one Connections entry.
    - The FlareSolverr URL and the metadata keys can be changed and tested from their cards.
    - Everything works at 375px.
  - **Tests:** UI check: the edit, disable, delete and add flows for an indexer and a client; the redirects; FlareSolverr URL edit plus Test; 375px.
  - **Depends on:** [INT-16](#int-16), [INT-04](#int-04), [INT-05](#int-05)
  - **Risk:** This moves a lot of form code, so move it first and then change behaviour, and diff carefully to avoid regressing the seed-rule and pill semantics. The old routes must keep working through redirects, because Dashboard and health links point at them.
  - **Resolves:** integrations-12, frontend-10
<a id="int-18"></a>
- [ ] **INT-18 · Add-indexer preset catalogue with test-before-save** — `P3` · `M` · Phase 14
  - **Problem:** Adding an indexer gives no guidance and no test:
- You pick a kind in a generic form and save it untested.
- Seed rules exist only in Edit (Indexers.tsx:445 vs 312-317), although the backend does default new indexers to 14 days (httpapi/indexers.go:76-89).
- There is no per-tracker guidance, such as MyAnonaMouse being books-only.
  - **Approach:** 1. New web/src/components/connections/AddIndexerModal.tsx with preset tiles: TorrentLeech, MyAnonaMouse, 1337x, Prowlarr (opens the Prowlarr panel), and Jackett / other Torznab. Use monogram tiles, not third-party logos. There is no Newznab tile ([INT-12](#int-12)).
    2. Preset metadata in TS (web/src/lib/indexerPresets.ts):
       - the fields shown and the default name;
       - the default scope: MAM → ['book']; 1337x → ['movie','series'];
       - help copy: the existing per-kind paragraphs;
       - the private-tracker seed hint.
    3. SeedingRules is shown at add time with the 14-day default, editable.
    4. 'Test' calls POST /indexers/test ([INT-09](#int-09)) and shows the result plus the caps summary. 'Save' is primary after a passing test; 'Save without testing' stays secondary.
    5. It mounts in the Connections hub, or on the Indexers page if [INT-17](#int-17) hasn't landed.
  - **Files:** `web/src/components/connections/AddIndexerModal.tsx`, `web/src/lib/indexerPresets.ts`, `web/src/pages/Connections.tsx`, `web/src/pages/Indexers.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Adding TorrentLeech shows only username, password and seed rules, names it 'TorrentLeech', and can be tested before saving.
    - A Torznab preset with a bad URL fails the test with the specific reason before any row exists.
    - MyAnonaMouse defaults to Books only.
  - **Tests:** UI check: each preset's fields, test then save, save without testing, default scope, and a 375px layout.
  - **Depends on:** [INT-09](#int-09), [INT-12](#int-12)
  - **Risk:** Low. It is frontend only, on top of the existing create API and INT-09's test route.
  - **Resolves:** integrations-12

#### Milestone: M4: Prowlarr and trackers on autopilot

_Prowlarr syncs itself hourly, with managed rows locked and the correct port link. TorrentLeech and MyAnonaMouse feed RSS sync. Manual searches overtake queued sweep requests without breaking the per-host spacing._

<a id="int-19"></a>
- [ ] **INT-19 · Prowlarr on autopilot: hourly sync, managed rows locked, the correct port link** — `P2` · `M` · Phase 9
  - **Problem:** Prowlarr still needs manual attention, and its link can point at the wrong instance:
- Even when non-destructive (INT-07), the sync runs only when the button is clicked (httpapi/prowlarr.go:42). Indexers added, removed or disabled in Prowlarr aren't reflected until someone remembers.
- Synced rows look editable, but their name, URL and key belong to Prowlarr.
- The 'Open Prowlarr' link hard-codes :9696 (Indexers.tsx:172), but install.sh picks the first free port of 9696-9698 (install.sh:178, 201). When 9696 is taken by the user's old Prowlarr, they copy the wrong key and the sync fails with 'invalid API key'.
  - **Approach:** 1. Scheduler job 'prowlarr-sync' in main.go:
       - hourly, plus one run a minute after start via time.AfterFunc;
       - runs only when prowlarr_url and the key are saved;
       - stores {at, added, updated, disabled, reenabled, error} as JSON in the settings key prowlarr_last_sync;
       - records connstatus kind prowlarr, ref default.
    2. GET /api/v1/indexers/prowlarr returns last_sync, ui_port and ui_url:
       - docker-compose.yml passes ARRMADA_PROWLARR_HOST_PORT: "${ARRMADA_PROWLARR_PORT:-9696}" to the app, the same way ARRMADA_AUDIOBOOK_HOST_PORT is passed (docker-compose.yml:46);
       - ui_port comes from that variable (default 9696), and ui_url from an optional saved setting prowlarr_ui_url for an external Prowlarr.
    3. Prowlarr panel (Indexers.tsx, or ProwlarrPanel after [INT-17](#int-17)):
       - 'Last synced 14:00: 2 added, 1 disabled' or the last error;
       - the link is ui_url, otherwise `${protocol}//${hostname}:${ui_port}`;
       - the API-key placeholder names the port: 'Bundled Prowlarr (port 9697) → Settings → General'.
    4. Managed rows (prowlarr_id>0):
       - a 'Managed by Prowlarr' badge plus managed_note ('Disabled in Prowlarr');
       - the edit form locks name, URL and API key with the hint 'Change this in Prowlarr; it syncs hourly'; scope, seed rules, min seeders, tie-break and enabled stay editable.
    5. update.sh already re-reads compose. Confirm on the owner's Unraid that one ./update.sh passes the new variable.
  - **Files:** `cmd/arrmada/main.go`, `internal/httpapi/prowlarr.go`, `internal/httpapi/prowlarr_test.go`, `docker-compose.yml`, `web/src/pages/Indexers.tsx`, `web/src/components/connections/ProwlarrPanel.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Disabling an indexer in Prowlarr is reflected in Arrmada within an hour without clicking Sync, and the panel shows the last sync time and counts.
    - A failing scheduled sync shows its error in the panel and on the Prowlarr card.
    - On an install where ARRMADA_PROWLARR_PORT=9697, the 'Open Prowlarr' link points at :9697; with prowlarr_ui_url saved, the link uses it.
    - Synced rows show 'Managed by Prowlarr', and their name, URL and key can't be edited.
  - **Tests:** Go: handleProwlarrInfo returns ui_port from t.Setenv and the 9696 default, plus last_sync from settings.; Go: the sync job is a no-op without a saved URL and key, and stores the result JSON and the connstatus outcome.; UI check: badge, locked fields, link and placeholder text.
  - **Depends on:** [INT-07](#int-07)
  - **Risk:** An hourly sync must never be destructive, so it relies on INT-07's empty-list and error guard. It needs one ./update.sh so compose passes the new variable. Implement the lock in whichever form is live (EditDrawer if INT-17 has landed).
  - **Resolves:** integrations-2, integrations-15
<a id="int-20"></a>
- [ ] **INT-20 · TorrentLeech and MyAnonaMouse join RSS sync; decide the TorrentLeech RSS key** — `P2` · `M` · Phase 9
  - **Problem:** Neither TorrentLeech nor MyAnonaMouse implements Recent(), so RSS sync skips both (service.go:300-303; only Torznab and 1337x implement it). New uploads on the owner's two private trackers are found only by per-title sweeps. TorrentLeech's 'RSS key' field is collected (Indexers.tsx:293-295, 421-423) but downloadURL ignores it (torrentleech.go:303-305).
  - **Approach:** 1. TorrentLeech Recent(ctx, idx, limit) reuses search() with an empty Text, which takes the existing /newfilter/2 path (torrentleech.go:216-218). It goes through the [INT-08](#int-08) session, backoff and throttle.
    2. MyAnonaMouse Recent(ctx, idx, limit) uses the same JSON search with Text '', SortType 'dateDesc', the book main categories and perpage=min(limit,100). Verify with the owner's session through the UI that MAM accepts an empty-text browse; if it doesn't, leave MAM out and say so in the code.
    3. RSS key:
       - First confirm the cookie-free /rss/download/<fid>/<rsskey>/<filename> format against the owner's live TorrentLeech RSS feed (the owner checks in a browser; the key is never pasted into chat).
       - If confirmed, downloadURL builds cookie-free links when APIKey is set, and Fetch tries the cookie-free link first and falls back to the session. Ship this only after SEC's release tokens keep tracker download URLs server-side, because the link embeds a secret.
       - If it can't be confirmed, remove the field from Add and Edit, and stop storing it for TorrentLeech.
    4. RSS sync logs per-indexer item counts for both, so the owner can see they are polled.
  - **Files:** `internal/indexer/torrentleech.go`, `internal/indexer/torrentleech_test.go`, `internal/indexer/myanonamouse.go`, `internal/indexer/myanonamouse_test.go`, `web/src/pages/Indexers.tsx`
  - **Acceptance:**
    - RSS sync logs TorrentLeech items, and a wanted movie uploaded to TorrentLeech is grabbed from the feed.
    - RSS sync logs MyAnonaMouse items for the book sweep, or the code documents why MAM can't browse.
    - The RSS key either visibly works for downloads or is no longer offered.
  - **Tests:** Go: TorrentLeech Recent() requests the /newfilter/2 path (httptest stand-in).; Go: the MAM Recent() request body has sortType dateDesc and empty text.; Go: downloadURL uses the RSS key when set (only if the format is confirmed).
  - **Depends on:** [INT-08](#int-08)
  - **Risk:** More requests to private trackers: RSS runs every 15 minutes, goes through the same throttle and the recentCache 60s sharing, and must respect INT-08's login backoff. RSS-key URLs embed a secret, so don't ship them before SEC's release tokens.
  - **Resolves:** integrations-8
<a id="int-21"></a>
- [ ] **INT-21 · User-started searches jump the Torznab queue; waiting in the throttle doesn't use up the deadline** — `P3` · `M` · Phase 14
  - **Problem:** Searches you start yourself can time out behind background sweeps:
- The Torznab throttle allows one request per second per host (torznab.go:108-152), and every Prowlarr-synced indexer shares one host.
- The wait happens inside each indexer's 25s context (service.go:390-396), and sweeps and manual searches share the queue.
- With many trackers, deep paging (series ask for 400 results; up to 10 pages each) or an overlapping sweep, a manual search waits behind background work, and the last indexers hit 'context deadline exceeded'.
The per-host spacing was added on purpose to stop 429s and must stay.
  - **Approach:** 1. Use indexer.IsInteractive(ctx) from [INT-01](#int-01).
    2. Replace TorznabSearcher.throttle with a per-host two-lane dispatcher (internal/indexer/throttle.go):
       - one goroutine per active host, exiting after 1 minute idle;
       - it releases one waiter every torznabRequestDelay, preferring the interactive lane;
       - the background lane gets at least 1 grant per 5 interactive grants;
       - cancelled waiters free their slot.
    3. Move the queue wait outside the per-indexer deadline:
       - Service.Search passes the 45s search ctx to Torznab searchers;
       - TorznabSearcher.get applies a 20s per-request timeout only after its slot is granted;
       - native searchers keep perIndexerTimeout.
    4. Log queue_wait_ms with host and lane when a wait exceeds 2s, so real contention is measured.
    5. Keep the spacing per host, not per URL.
  - **Files:** `internal/indexer/throttle.go`, `internal/indexer/torznab.go`, `internal/indexer/service.go`, `internal/indexer/throttle_test.go`
  - **Acceptance:**
    - With a sweep in flight against a Prowlarr serving 15 indexers, a manual movie search's Torznab requests are dispatched ahead of the queued sweep requests (log order), and none fail with 'context deadline exceeded' while waiting in the queue.
    - Requests to one host are never closer together than 1s.
    - Queue waits over 2s are logged with host and lane.
  - **Tests:** Go throttle_test with the delay shortened via a package var: 5 background waiters plus 1 interactive → the interactive one is served next; any two grants are at least the delay apart; a cancelled waiter's slot is reused; the background lane is not starved (1 per 5); the idle goroutine exits.; Run with -race in the Linux Docker one-liner before pushing.
  - **Depends on:** [INT-01](#int-01)
  - **Risk:** A dispatcher goroutine per host adds concurrency, so it needs a clean idle shutdown, no goroutine leak on ctx cancel, and race tests in Docker. Moving the wait outside the per-indexer deadline means one slow host can use more of the 45s budget; the 20s per-request cap bounds that.
  - **Resolves:** integrations-9

#### Milestone: M5: Smarter searches

_Movies and non-anime TV search by IMDb, TMDB or TVDB id where the indexer supports it, falling back to text. Books and music use proper search modes and default categories. Alt-titled films match._

<a id="int-22"></a>
- [ ] **INT-22 · ID-based movie and TV searches from stored caps, with text fallback** — `P2` · `M` · Phase 10
  - **Problem:** Searches never use the ids Arrmada already has:
- Every movie search sends only Text: title + year to t=movie (coordinator.go:446-450, 546-550, 840, 948), and SearchQuery has no id field (indexer.go:96-122).
- buildURL never sends imdbid, tmdbid or tvdbid (torznab.go:334-371), although movies store IMDBID/TMDBID (movies/movie.go:9-10) and series store TVDBID (series/series.go:11).
- Short or common titles return junk, and films listed under other titles are missed, even though Prowlarr and Jackett support id search.
  - **Approach:** 1. indexer.SearchQuery gains IMDBID, TMDBID, TVDBID. Release gains IMDBID, TMDBID, TVDBID and MatchedByID.
       - parseFeedPage reads the imdb/imdbid/tmdbid/tvdbid attrs.
       - TorrentLeech sets Release.IMDBID from tlTorrent.ImdbID.
    2. buildURL, using the stored caps ([INT-09](#int-09)). For t=movie and t=tvsearch, send imdbid (digits without 'tt', as Radarr does), tmdbid and tvdbid only when that mode is available and lists the param. Send no q when an id is sent. With no caps, build exactly today's URL.
    3. TorznabSearcher.Search is id-first: when an id param applies, run the id query, mark those results MatchedByID, and if it returns 0 items run today's text query for that indexer. Native searchers (TorrentLeech, 1337x, MAM) ignore the ids.
    4. Automation:
       - Add a movieQuery(m) helper in coordinator.go, used by RankReleasesWith (~450), searchAndGrab (~550), the upgrade search (~840) and RegrabMovie (~948). It returns Text title+year plus IMDBID and TMDBID.
       - Series: searchSeriesReleases (series.go:684), the season search (768), the targeted episode search (264) and series_interactive.go:43 pass TVDBID (and IMDBID) only for non-anime series. Anime and absolute-number queries never use ids, so the layered numbering resolver behaves as before. Alias queries (701) stay text-only.
    5. Matching: releaseIsForMovie (coordinator.go:765) also accepts a release whose own IMDBID equals m.IMDBID, as long as the year check passes, even when the title differs. MatchedByID alone does not override the title check, because some indexers ignore the param and return their RSS feed.
  - **Files:** `internal/indexer/indexer.go`, `internal/indexer/release.go`, `internal/indexer/torznab.go`, `internal/indexer/torznab_test.go`, `internal/indexer/torrentleech.go`, `internal/automation/coordinator.go`, `internal/automation/series.go`, `internal/automation/series_interactive.go`, `internal/automation/coordinator_test.go`
  - **Acceptance:**
    - For a movie with an IMDb id, on an indexer whose caps list imdbid for movie-search, the logged Torznab URL contains t=movie&imdbid=<digits>; if that returns nothing, a text query follows for that indexer.
    - A non-anime TV search on an indexer supporting tvdbid sends tvdbid plus season/ep. Anime series searches are unchanged.
    - An indexer with no stored caps builds exactly today's URLs.
    - A release titled differently but tagged with the movie's IMDb id and the right year is accepted.
  - **Tests:** Go buildURL table: imdbid has 'tt' stripped; tvdbid with season/ep; no id params when caps lack support; no caps → today's URL.; Go: Search falls back to text when the id query returns 0 items (httptest counts requests).; Go: an anime series search sends no id params.; Go automation: TestMatchedByIMDbAcceptsForeignTitle, and a release without an imdb attr still needs a title match.
  - **Depends on:** [INT-09](#int-09)
  - **Risk:** Trackers with incomplete id tagging could hide releases; the fallback to text on empty results covers that. Some indexers ignore unsupported params, which is why ids are sent only when caps advertise them and foreign titles are accepted only on a matching imdb attr. Coordinate with SER (series-7/8 matching work) so the series query changes don't collide.
  - **Resolves:** movies-11, integrations-10
<a id="int-23"></a>
- [ ] **INT-23 · Default categories and book/music search modes from caps** — `P2` · `M` · Phase 10
  - **Problem:** Book and music searches use the wrong search mode and no categories:
- No automation code sets SearchQuery.Categories, and Prowlarr-synced rows have nil Categories, so they are searched across every category.
- searchType maps books and music to plain t=search (torznab.go:465-476), never t=book or t=music, and author/title or artist/album params are never sent.
- Book searches on general trackers can return video results.
  - **Approach:** 1. SearchQuery gains Author, BookTitle, Artist and Album. searchBook (books.go:470-477) and the music searches (music.go:89, 524) fill them.
    2. searchType and buildURL use stored caps:
       - t=book with author and title when book-search is available and lists them;
       - t=music with artist and album when music-search lists them;
       - otherwise today's t=search q=….
    3. Default categories, used when an indexer has caps but no explicit Categories:
       - movie 2000, series 5000, ebook 7000 (7020 included), audiobook 3030 (by BookEdition), music 3000;
       - intersect with the indexer's caps categories, and send no cat if nothing overlaps;
       - explicit indexer Categories still win; native searchers are unaffected.
    4. A setting indexer_default_categories (default on), toggled from the Indexers or Connections page, can turn this off. Log 'cat filter applied' per indexer so result counts can be compared.
    5. Optional: an 'Advanced: categories' field in the indexer editor.
  - **Files:** `internal/indexer/indexer.go`, `internal/indexer/torznab.go`, `internal/indexer/searchtype_test.go`, `internal/indexer/torznab_test.go`, `internal/automation/books.go`, `internal/automation/music.go`, `internal/httpapi/indexers.go`, `web/src/pages/Indexers.tsx`
  - **Acceptance:**
    - An audiobook search on a general tracker whose caps include 3030 sends cat=3030 and returns no video releases.
    - A book search on an indexer whose caps list book-search author,title uses t=book&author=…&title=….
    - Explicit indexer categories override the defaults, and turning the setting off restores today's URLs.
    - An indexer with no stored caps builds exactly today's URLs.
  - **Tests:** Go buildURL table: default categories intersect with caps; explicit categories win; the setting off → no cat; t=book and t=music only when caps say so; no caps → today's URL.
  - **Depends on:** [INT-09](#int-09), [INT-22](#int-22)
  - **Risk:** Category filters can hide mis-categorised releases. Compare result counts on the owner's indexers (from the log line) before relying on the default, and keep the off switch. MAM and TorrentLeech keep their own category handling.
  - **Resolves:** integrations-10
<a id="int-24"></a>
- [ ] **INT-24 · Movie alternate titles for search and matching** — `P3` · `S` · Phase 10
  - **Problem:** releaseIsForMovie keeps a release only if titleKey(parsed title) equals titleKey(m.Title) (coordinator.go:765-771), and MovieExtra stores no original or alternative titles (movies/movie.go:48-59). Unlike series (series_reliability.go:302), the movie path never uses an original title. Per the verify note, titleKey already folds accents and punctuation, so the real impact is retitled or alt-titled films and some foreign and anime films.
  - **Approach:** 1. metadata/tmdb.go GetMovie: append alternative_titles to append_to_response (credits,release_dates,alternative_titles). MovieDetails gains OriginalTitle and AltTitles []string, keeping titles from the US, GB, the original-language country and type ''. Drop entries in non-Latin script, since releases are named in ASCII.
    2. movies.MovieExtra gains original_title and alt_titles, set where the extra is built (movies/service.go:331). No migration: it's a JSON blob.
    3. Backfill: existing movies get them on the next movies.Service.Refresh. A one-off startup job, 'movie-alt-titles-backfill', refreshes movies missing the field at about 1 per second through the TMDB disk cache, and stops when none remain.
    4. releaseIsForMovie(relTitle, m) accepts titleKey equal to any of Title, OriginalTitle (Latin script only) or AltTitles. The year check is unchanged.
    5. searchAndGrab and RankReleasesWith: if the primary text query (and [INT-22](#int-22)'s id query) yields no release for this movie, run one more text query with the original title when it differs and is Latin script. That is at most one extra query.
  - **Files:** `internal/metadata/tmdb.go`, `internal/metadata/provider.go`, `internal/movies/movie.go`, `internal/movies/service.go`, `internal/automation/coordinator.go`, `internal/automation/coordinator_test.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - A film whose releases use an alternative English title (e.g. a UK title) is matched and grabbed.
    - The year check still rejects a same-titled remake from another year.
    - Existing movies gain alt titles without a manual refresh.
  - **Tests:** Go: releaseIsForMovie table with primary, original and alt titles and wrong-year rejection.; Go: GetMovie parses alternative_titles (fixture) and filters non-Latin script.; Go: the fallback original-title query runs only when the primary yields nothing.
  - **Risk:** Broader matching risks cross-grabs between films sharing an alternate title; the year check (±1) and the quality pipeline mitigate it. Coordinate with MOV in case it is also touching releaseIsForMovie, and with SER, which owns series alt titles and romaji (series-7/8).
  - **Resolves:** movies-11

#### Milestone: M6: External and seedbox qBittorrent

_A qBittorrent with different mounts works through per-client path mappings, and Test tells you exactly what Arrmada can and can't see._

<a id="int-25"></a>
- [ ] **INT-25 · Remote path mapping and a path-aware download client Test** — `P3` · `M` · Phase 14
  - **Problem:** An external or seedbox qBittorrent with different mounts downloads fine, but nothing ever imports:
- Every add forces savepath to Arrmada's own downloads directory (coordinator.go:154, qbittorrent.go:213-215).
- Imports read content_path untranslated (main.go:338-341), and there is no path mapping anywhere.
- The client Test only logs in and reads the version (qbittorrent.go:157-174).
  - **Approach:** 1. Migration 00xx_download_client_path_maps.sql: download_clients ADD path_maps TEXT NOT NULL DEFAULT '', a JSON list of {remote, local}.
       - Client gets PathMaps, with ToLocal and ToRemote doing longest-prefix matching with normalised trailing slashes.
       - Validation rejects any local prefix under cfg.DataDir (/data), because media must never live in the DB dir.
    2. download.Service.QueueComplete maps each Item.ContentPath through ToLocal, so imports, stall detection and delete paths see local paths. Service.Add maps req.SavePath through ToRemote before impl.Add. With no mappings both are a no-op.
    3. A qBittorrent TestPaths step, returned as part of Test's detail:
       - GET /api/v2/app/preferences → save_path;
       - map it, then check it exists locally and is on the same filesystem as the library, using the existing same-filesystem helper (httpapi/downloads.go:133-138);
       - check that the newest completed torrent's mapped content_path exists;
       - report either 'qBittorrent saves to /downloads; Arrmada sees it as /media/downloads ✓' or a suggested mapping.
    4. The client edit form ([INT-05](#int-05), or ClientEditor after [INT-17](#int-17)) gets a 'Path mappings' list.
    5. Health ([INT-14](#int-14)): warn when a completed torrent's mapped content_path doesn't exist locally, naming the client. The bundled client has no mappings and is unaffected.
  - **Files:** `internal/store/migrations/00xx_download_client_path_maps.sql`, `internal/download/client.go`, `internal/download/repo.go`, `internal/download/service.go`, `internal/download/qbittorrent.go`, `internal/download/pathmap_test.go`, `internal/httpapi/downloadclients.go`, `internal/httpapi/health_system.go`, `web/src/pages/DownloadClients.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With the mapping /downloads → /media/downloads, adds send savepath=/downloads, and completed torrents import from /media/downloads/...
    - Test reports qBittorrent's save path and whether Arrmada can see it, and suggests a mapping when it can't.
    - A mapping whose local side is under /data is rejected with an explanation.
    - A completed torrent whose path isn't visible locally produces a health warning naming the client.
    - The bundled client behaves exactly as before.
  - **Tests:** Go pathmap_test: ToLocal and ToRemote, longest prefix, trailing slashes, no match, and the DataDir rejection.; Go: QueueComplete returns the mapped ContentPath, and Add sends the mapped SavePath (httptest qBittorrent).; Go: TestPaths reads /api/v2/app/preferences and reports a missing path.
  - **Depends on:** [INT-05](#int-05)
  - **Risk:** Mapping in QueueComplete changes paths for every consumer, so it must be a strict no-op when no mapping exists. The owner doesn't use this, so test it against a second throwaway qBittorrent container, never the real library.
  - **Resolves:** integrations-11

#### Risks

- Backing off a flaky but useful public tracker hides it from sweeps. The ladder starts at the 2nd consecutive failure, interactive searches and Test always probe, and a single success clears the backoff.
- SQLite write load from status tracking: rows are written only on state transitions plus a 60s counter flush, never once per search.
- Prowlarr's API field names (capabilities, protocol, enable) are unverified. Capture a fixture from the bundled Prowlarr 2.6.5 before shipping INT-07, and never change local rows when the indexer list call fails or comes back empty.
- Indexer names are used as foreign keys (grabs.indexer drives seedRules at automation/store.go:191; blocklist; Service.Fetch). A Prowlarr rename must rewrite those rows in the same transaction, or private-tracker seed rules fall back to the default.
- TorrentLeech and MyAnonaMouse account safety: login backoff errs toward fewer attempts, RSS polling goes through the same throttle, and live checks are done only by the owner through the UI with their own credentials.
- Migration numbering: five INT migrations, starting from 0090, will interleave with other epics. Take the next free number at implementation time and never renumber a shipped migration.
- ID search and default categories can hide releases on indexers with poor tagging. Ids are sent only when caps advertise them, with text fallback on empty results; foreign titles are accepted only on a matching imdb attr; categories have an off switch and a comparison log.
- Live third-party verifiers (TMDB, TVDB, OMDb, OpenSubtitles, Prowlarr, FlareSolverr) must never run in CI or in health polling. Health reads cached status only.
- Concurrency additions (connstatus tracker, TorrentLeech single-flight and detached login, two-lane throttle dispatcher) need go test -race in the Linux Docker one-liner before every push.
- ACQ consumes SearchResult.Errors/Skipped for search-modal banners and for not recording a miss when every indexer failed or was skipped. Keep those field names stable once INT-01 ships.
- The hub moves a lot of form code (INT-17). Move first and change behaviour second, so the seed-rule, pill and delete-confirm fixes from INT-04 aren't lost.
- Path mapping (INT-25) changes paths for every queue consumer. It must be a strict no-op without mappings, reject local paths under /data, and be tested only with a throwaway qBittorrent container.

#### Out of scope

- Usenet support (SABnzbd/NZBGet) and reviving Newznab. Usenet is hidden instead (INT-12).
- Transmission/Deluge download clients. The Downloader interface stays open for them.
- Search-modal error banners and not recording a search miss when every indexer failed (ACQ). INT-01 provides the Skipped/Errors data.
- Restricting indexer, search and release routes, and release tokens (SEC: movies-3, integrations-7).
- Plex features: scan hooks, Watch-on-Plex links, the HW transcode badge, server discovery and moving the Plex editor (PLEX). The hub only shows Plex status.
- Notifications/Apprise improvements (event types, delivery reporting). The Apprise card is a read-only link.
- OpenSubtitles 429/24h pause behaviour and subtitle filtering (SUB).
- Series alternate titles, romaji and year/country matching (SER: series-7, series-8). Movie usenet rows in interactive search (MOV: movies-10).
- The setup wizard redesign and Settings hub/nav regrouping beyond the single Connections entry (CFG/APP/FE). INT-06 only adds the TMDB key test to the existing wizard step.
- Removing the per-host Torznab spacing or throttling per URL. The deliberate 1 req/s per-host guard stays.
- Third-party logos on the preset tiles (monograms only).

