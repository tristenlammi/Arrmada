# MUS — Music

_Part of the [Arrmada roadmap](../../ROADMAP.md). 25 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Make Music cost nothing while it is unfinished: hidden by default, labelled Preview, fully off when switched off, and no longer hammering indexers. Then record a dated decision to rebuild it, park it or cut it. If the decision is Rebuild, turn it into a Lidarr-lite the owner can trust unattended: it grabs the right album at the right quality, imports it without downgrades or duplicates, says why an album isn't downloading, keeps itself current, and looks like the rest of Arrmada. If the decision is Cut, remove it cleanly and leave all data in place.

**Why.** Music is the lowest-scoring area in the audit (3/10). It went into the nav on by default after a single day of commits (2026-08-02) and has had none of the live hardening the other modules got. In practice it fails the owner in three ways.

1. **It costs something even when nobody uses it.**
   - The missing-album sweep has no backoff, even though its doc comment says it does. It searches every indexer for every incomplete album every 30 minutes, forever (music-1). An artist with 20 unfindable albums costs about 960 tracker searches a day, on the same indexers Movies and Series depend on.
   - The Settings toggle only hides the nav entry. The jobs, the discography grab, the scan and the API all keep running (music-12).
   - The UI contradicts itself: Music is in the nav, while /status says 'planned' and Settings says 'still on the roadmap' (music-12).

2. **It grabs and files the wrong things.**
   - Release matching keeps only a-z/0-9, so ASCII-spelled releases never match Björk, Motörhead, AC/DC, "Sgt. Pepper's" or "Simon & Garfunkel" (music-2).
   - Quality detection is substring-based. 'Escape', 'Palace', 'New Wave' and 'Mixtape' read as lossless, which breaks 'Lossless only' profiles (music-3).
   - Song titles that start with a number ('99 Problems', '7 Rings') are misparsed. With a '01 - ' prefix they are silently misfiled (music-4).
   - Partial albums seed forever, and the next grab can downgrade tracks or leave duplicates (music-5).
   - Discography grabs are stored under the artist id in the album-id column, so stall, blocklist and seeding logic act on an unrelated album (music-6).

3. **It doesn't do what Lidarr does, and doesn't say why.**
   - Artists are never refreshed, so new releases are never found. Add-artist offers no profile, monitor or release-type options (music-8).
   - There is no per-album Search, no interactive search and no explanation of why an album isn't downloading. The reasons exist only in the server log (music-9).
   - Completeness stats count only albums whose listing was fetched, and the artist page always shows 0/0 (music-10).
   - Upgrades are advertised but no upgrade code exists (music-7).
   - The scan reports nothing back, misreads CD1/CD2 folders and never notices deleted files (music-14).
   - The library is bare text with no artwork (music-13), and album searches let concert videos and fake releases through (music-15).

The audit's verdict is to hide it now, then rebuild it properly or cut it. Shipping it as it stands is the worst option. This epic does exactly that, in that order.

**Depends on:** CFG: needs the fix for the broken Save settings round-trip first. GET returns server_time/server_tz and PUT rejects them, so the Music toggle can't be switched off from Settings until that lands ([MUS-01](#mus-01) toggle acceptance, [MUS-12](#mus-12)). Default-off and the keep-on rule work without it.; SEC: deny-by-default route roles. The music GET routes (artists, lookup, albums, history) are currently open to any signed-in user. [MUS-01](#mus-01) adds only the module gate; SEC owns the roles. Every new MUS route is RoleManager from the start. [MUS-23](#mus-23) also relies on SEC's secret masking and log redaction for the fanart.tv and TheAudioDB keys.; SAFE: a pre-migration DB snapshot and backups should exist before MUS ships its ~9 additive migrations. The recycle-bin fix (never purge the newest item to meet the cap) should land before [MUS-08](#mus-08) and [MUS-16](#mus-16) start recycling replaced tracks.; ACQ:
- The Reviews fix for music-11 (real media types, a music album picker in ReassignModal, 'Import anyway' hidden at id 0) is owned there, not in MUS.
- [MUS-02](#mus-02) follows ACQ's 'don't record a miss when every indexer errored' rule.
- ops-10 labels Downloads and Activity by the arrmada-music category.
- [MUS-09](#mus-09) and [MUS-18](#mus-18) should match ACQ's shared 'why isn't this downloading' outcome wording.; BOOK: quick win #13 (fold accents, apostrophes and & in book matching). Share one fold helper with [MUS-04](#mus-04) rather than writing two.; COPY (or QUAL): the music-7 copy task (draft music.t4) hides the music 'Automatically upgrade' switch and fixes its copy until [MUS-16](#mus-16) restores them.; BE: the job runner for background work ([MUS-09](#mus-09) artist search, [MUS-18](#mus-18) sweep-now, [MUS-20](#mus-20) scan goroutine). Migration-number coordination too: 0090+ is shared, so take the next free number at commit time.; INT:
- Torznab caps and audio categories (integrations-10) complement [MUS-21](#mus-21)'s result filter.
- The Connections / metadata-key card pattern is needed for the [MUS-23](#mus-23) fanart.tv and TheAudioDB keys.; OBS: the music.imported admin notification (insights-6) and attention-feed entries for music (related to [MUS-24](#mus-24)).; FE: the shared component kit, useLiveQuery and modal patterns. [MUS-17](#mus-17), [MUS-20](#mus-20) and [MUS-22](#mus-22) reuse them if they exist and follow the current page patterns otherwise.

#### Design

## Target state

Music ends in one of three states, chosen by [MUS-03](#mus-03):
- **Rebuild:** an honest, opt-in Lidarr-lite.
- **Park:** hidden, inert, revisited later.
- **Cut:** removed cleanly, with all data kept.

Milestone M1 ([MUS-01](#mus-01), [MUS-02](#mus-02)) is right for all three.

### 1. Module gate ([MUS-01](#mus-01)): one setting, three enforcement layers

- **Setting.** `settings.KeyModuleMusic = "module_music_enabled"`, with `settings.ModuleMusicDefault = false`.
- **Upgrade safety.** On upgrade, `settings.EnsureModuleDefault(ctx, key, musicSvc.HasArtists)` writes `true` once, and only when the key is unset and the library already has artists. It logs why.
- **Coordinator.** `SetModuleGate(func(ctx, module string) bool)` and `moduleOn(ctx, "music")`. A nil gate means on, so tests and other modules are unaffected.
  - Every music job and manual action returns early or returns `ErrModuleOff`: SearchMusicMissing, ImportMusicDownloads, GrabDiscography, ScanMusicLibrary, and later RefreshMusicArtists, UpgradeMusic and VerifyMusicFiles.
  - DetectStalled's music branch is deliberately not gated, so in-flight grabs still resolve.
- **HTTP.** A `musicRoute(h)` wrapper sits inside `protected` / `requireRole`. When off it returns 404 `{"error":"The Music module is turned off (Settings → System → Modules)"}`. Auth still answers 401 first.
- **SPA.** The /music routes are rendered only when `musicEnabled` (the books pattern at App.tsx:56/71). The nav entry and the Downloads pill follow the toggle. The nav shows a mono 'Preview' pill until [MUS-12](#mus-12).
- **/status.** `modules` is built per request. Music is `{enabled: <live>, status: "preview"}`, and becomes `"available"` at [MUS-12](#mus-12).

The gate is shaped so Books can adopt it later (Books' toggle has the same don't-stop-jobs gap), but Books is not changed here.

### 2. Album search pipeline (sweep, Search now, upgrade sweep)

```
due albums  = monitored artist AND monitored album AND NOT Complete
              AND release_date <= today (future = skip, not a miss)
              AND not already in the arrmada-music queue
              AND now >= last_search_at + musicSearchWait(search_misses)
  → order: never-searched first, then oldest last_search_at → cap 25 per sweep
  → EnsureTracks (no listing → outcome no_listing: miss, no indexer call)
  → c.musicSearch(query)                      (seam; Search now ignores the wait)
  → candidate filter: ReleaseIsForAlbum (foldMusic, whole words, artist check)
                      → audio category / size window ([MUS-21](#mus-21))
                      → blocklist (music | music_artist) → pending grabs
                      → held-tier floor ([MUS-08](#mus-08))
  → albumScore: token DetectQuality → profile ladder, reject terms, min score
  → disk check → grabTo(arrmada-music) → recordMusicGrab(media_type)
  → albumOutcome{Code, Detail} → persisted on the album; artist_event only when the code changes
```

Outcome codes are introduced in [MUS-02](#mus-02) and persisted and shown from [MUS-09](#mus-09):

| code | miss? | example detail |
|---|---|---|
| grabbed | resets misses | "FLAC · 412 MB from TL" |
| no_listing | miss, no indexer call | "MusicBrainz has no track listing yet" |
| no_results | miss | "Indexers returned nothing" |
| no_match | miss | "14 releases, none named this album" |
| blocked | miss | "3 matching releases, all blocklisted or already tried" |
| below_profile | miss | "best was MP3-320; profile needs FLAC" |
| no_space | not a miss; ends this sweep | "412 MB needed, disk guard says no" |
| indexer_error / listing_error / grab_failed | not a miss | transient |
| not_released | skipped | "Out 2026-11-14" |

`musicSearchWait(misses)` returns 0 for 0 misses and `searchBackoff(misses)` for 1–6 misses (30 min doubling to 12 h). From the 7th miss it returns 7 days. The upgrade sweep ([MUS-16](#mus-16)) uses the same function on its own `upgrade_misses` counter.

### 3. Import pipeline

```
completed torrent in arrmada-music (module on)
  ├─ discography name → importDiscographyContent (per folder; CD1/CD2 merged, [MUS-15](#mus-15))
  │     placed>0 → imports hash + grab(music_artist) → imported           ([MUS-07](#mus-07))
  ├─ manual grab (grabs.manual=1) → import into the grabbed album, no name gate ([MUS-17](#mus-17))
  └─ albumForRelease + grab-hash check → review on mismatch (existing)
        → MatchTracks: compact disc-track only, disc hints, title agreement ([MUS-06](#mus-06))
        → files ≠ listing? pick the MB release whose track count matches → RelistAlbum ([MUS-19](#mus-19))
        → per track: place if no file, or BetterTier(new, held); recycle the replaced
          sibling on an extension change; never hard-delete ([MUS-08](#mus-08))
        → ProbeAudio → tracks.format = measured tier, bitrate_kbps ([MUS-11](#mus-11))
        → placed>0 → record imports hash, grab → 'imported' (seed rules apply) ([MUS-08](#mus-08))
          still incomplete → search miss + event naming the missing tracks
```

### 4. Pure-function home: `internal/music`

- `fold.go`: `foldMusic` ([MUS-04](#mus-04)).
- `release.go`: token `DetectQuality` ([MUS-05](#mus-05)), `TierFromProbe` ([MUS-11](#mus-11)), and the expected-size window ([MUS-21](#mus-21)).
- `match.go`: safe track and disc parsing ([MUS-06](#mus-06)).
- `format.go`: `HeldTier` / `BetterTier`, replacing the never-called `FormatRank` / `BetterFormat` ([MUS-08](#mus-08)).

Each gets a table test.

### 5. Data model

Each task ships its own additive migration and takes the next free number at commit time. 0090+ is shared with other epics, so never renumber a shipped file. There is no DROP anywhere.

| task | migration (tentative name) | change |
|---|---|---|
| [MUS-02](#mus-02) | album_search_backoff | albums.last_search_at TIMESTAMP, albums.search_misses INTEGER NOT NULL DEFAULT 0 |
| [MUS-07](#mus-07) | music_discography_grabs | data: discography rows in grabs and blocklist → media_type 'music_artist' |
| [MUS-08](#mus-08) | music_partial_grabs | data: 'grabbed' music rows whose hash is in imports → 'imported' |
| [MUS-09](#mus-09) | album_search_outcome | albums.last_search_outcome TEXT, albums.last_search_detail TEXT (both NOT NULL DEFAULT '') |
| [MUS-13](#mus-13) | artist_refresh | artists.last_refreshed_at TIMESTAMP |
| [MUS-14](#mus-14) | artist_monitor_options | artists.monitor_new TEXT DEFAULT 'all', artists.release_types TEXT DEFAULT 'Album,EP' |
| [MUS-16](#mus-16) | album_upgrade_backoff | albums.last_upgrade_search_at TIMESTAMP, albums.upgrade_misses INTEGER NOT NULL DEFAULT 0 |
| [MUS-19](#mus-19) | album_edition | albums.mb_release_id TEXT NOT NULL DEFAULT '' |
| [MUS-20](#mus-20) | music_scan_results | music_scan_unmatched(path PK, artist, album, reason, seen_at); summary in settings 'music_last_scan' |
| [MUS-23](#mus-23) | artist_banner | artists.banner_url TEXT NOT NULL DEFAULT '' |

Grab media types become `music` (album id in movie_id) and `music_artist` (artist id in movie_id).

### 6. API

All routes are behind `musicRoute`. Mutations need RoleManager. The existing GET roles are left to SEC's deny-by-default work.

| route | purpose |
|---|---|
| POST /api/v1/music/albums/{id}/search | sync Search now, ignores the wait, 90 s timeout → outcome ([MUS-09](#mus-09)) |
| POST /api/v1/music/artists/{id}/search | background search of the artist's monitored incomplete albums → 202 {count} ([MUS-09](#mus-09)) |
| GET /api/v1/music/wanted?outcome=&page= | Wanted list with outcomes and next try ([MUS-18](#mus-18)) |
| POST /api/v1/music/sweep | run the sweep now (respects wait and cap) → 202 ([MUS-18](#mus-18)) |
| GET /api/v1/music/albums/{id}/releases · POST …/grab | interactive search and manual grab ([MUS-17](#mus-17)) |
| PUT /api/v1/music/albums/{id}/edition | pick the MusicBrainz edition ([MUS-19](#mus-19)) |
| PUT /api/v1/music/artists/{id}/options | profile, monitor_new, release_types ([MUS-14](#mus-14)) |
| GET /api/v1/music/scan · POST /api/v1/music/scan/match | last scan results and Match… ([MUS-20](#mus-20)) |
| GET /api/v1/music/artists?sort=name\|added\|completeness | sorting ([MUS-22](#mus-22)) |

### 7. Jobs

Every job is registered in cmd/arrmada/main.go and gated inside the coordinator.

| job | every | task |
|---|---|---|
| search-missing-music | 30 m | existing, with backoff and cap from [MUS-02](#mus-02) |
| import-music | 30 s | existing |
| refresh-music-artists | 24 h | [MUS-13](#mus-13) (≤50 artists per run, oldest first) |
| upgrade-music | 6 h | [MUS-16](#mus-16) (≤20 albums per run) |
| verify-music-files | 24 h | [MUS-15](#mus-15) (skips when the root is missing or >20% of files are gone) |
| detect-stalled (music branches) | 2 m | not gated; [MUS-07](#mus-07) and [MUS-08](#mus-08) fix it |

The MusicBrainz client (one instance, 1.1 s pacing) is shared by refresh, listings, editions and scan. Every job is capped per run.

### 8. UI

Keep the existing dark warm palette, terracotta accent and type scale. Reuse the existing pill, badge, modal and toast patterns, or the FE kit once it exists.

- **Nav.** 'Music' with a 'Preview' pill until [MUS-12](#mus-12), hidden when off.
- **Library (Music.tsx).**
  - Artists grid with cover tiles, sort and type filter ([MUS-22](#mus-22)).
  - Filters with counts using album-based completeness ([MUS-10](#mus-10)).
  - A 'Wanted (n)' tab listing every missing album with its last outcome, next try and Search now ([MUS-18](#mus-18)).
  - 'Scan library' shows 'Scanning…' and opens a results drawer with Match… ([MUS-20](#mus-20)).
- **Artist page.**
  - Header with real album and track counts ([MUS-10](#mus-10)), 'Search monitored' ([MUS-09](#mus-09)) and an Edit panel for profile, monitor-new and release types ([MUS-14](#mus-14)).
  - Album rows show a one-line outcome. 'Grab discography' appears only when the artist owns under 25% of albums.
  - Shelves by album type and a hero with bio ([MUS-22](#mus-22)/23).
  - History gets a 'searched' tone, and the page refreshes on music.imported via useLive.
- **Album page.**
  - 240 px cover, 'Held: MP3-320', and per-track measured format with a bitrate tooltip; green only for lossless ([MUS-11](#mus-11)).
  - Outcome line: 'Searched 2 h ago: 14 releases, none named this album. Next automatic search in about 6 h.'
  - Search now, Interactive search (ReleaseSearchModal variant="audio"), and an Edition select.

### 9. Decision and cut path

- [MUS-03](#mus-03) records the decision in docs/decisions/music.md, based on Insights music plays (stream_sessions.media_type='track', 90 days), library counts, household asks, and whether Lidarr still runs.
- **Rebuild:** run M3–M7. The Preview label lifts at [MUS-12](#mus-12) after a 7-day soak.
- **Park:** M1 stays, everything else is marked won't-do-for-now, and the decision is revisited in 6 months.
- **Cut:** run [MUS-25](#mus-25). Code, routes, jobs and UI are removed. Tables, files on disk and arrmada-music torrents are untouched. DetectStalled must skip leftover music rows so the movie branch never acts on album ids.

### 10. Testing approach

- Table tests for every pure function in internal/music.
- `musicTestCoord(t)`, added in [MUS-02](#mus-02) and modelled on packTestCoord:
  - store.Open(t.TempDir())
  - music.NewService(db, fakeMusicProvider, slog)
  - quality.NewService(db)
  - library.NewImporter(t.TempDir())
  - seams `musicSearchFn`, `musicQueueFn`, `musicGrabFn`, plus a `probeAudioFn` seam from [MUS-11](#mus-11)
- "Audio" files are generated byte blobs of 300 KB or more in temp dirs. ffprobe output is canned JSON.
- Never point tests or manual trials at the owner's real library.
- Run go vet and go test -race in the Docker one-liner before every push. Commits end with the Co-Authored-By trailer.

#### Milestone: M1: Contain (ship this week, needed whatever MUS-03 decides)

_Music is off on fresh installs and labelled Preview. Switching it off stops its jobs, manual actions and API. When it is on, each incomplete album is searched on a 30 min → 12 h → weekly backoff, at most 25 albums per sweep, never for albums with no listing or not yet released._

<a id="mus-01"></a>
- [ ] **MUS-01 · Music off by default and labelled Preview; switching it off stops its jobs, actions and API** — `P0` · `S` · Phase 0
  - **Problem:** module_music_enabled defaults to true (internal/httpapi/settings.go:51-55), so Music shows in the nav of every install. Meanwhile:
- /status reports {music, false, 'planned'} (server.go:517). The plannedModules comment also wrongly says Insights is on the roadmap.
- The Settings hint says the module is 'still on the roadmap' (Settings.tsx:166).

The toggle only filters the Sidebar (Sidebar.tsx:34) and the Downloads type pill. Everything else stays live:
- the search-missing-music and import-music jobs (cmd/arrmada/main.go:396-404)
- GrabDiscography and the library scan
- the API routes (server.go:402-414) and the SPA routes (App.tsx:94-96)

Switching Music off therefore leaves background grabbing running for every monitored artist.
  - **Approach:** 1. internal/settings/settings.go:
       - Export KeyModuleMusic = "module_music_enabled" and KeyModuleBooks = "module_books_enabled" (move them out of the httpapi consts), plus ModuleMusicDefault = false.
       - Add EnsureModuleDefault(ctx, key string, keepOn func(context.Context) (bool, error)) (changed bool, err error). When Get(key, "") == "" and keepOn returns true, it calls SetBool(key, true). It never touches an explicit value. This is generic so Books can reuse it.
    2. internal/music: add Repo.HasArtists / Service.HasArtists (SELECT EXISTS(SELECT 1 FROM artists)).
    3. cmd/arrmada/main.go, right after musicSvc is built (~line 198): call EnsureModuleDefault(ctx, settings.KeyModuleMusic, musicSvc.HasArtists). When it changes the value, log Info: "music: kept the Music module on because your library has artists. Switch it off in Settings → System → Modules."
    4. automation/coordinator.go:
       - Add field moduleGate func(context.Context, string) bool, SetModuleGate(fn), and moduleOn(ctx, m) bool (nil gate → true).
       - Add var ErrModuleOff = errors.New("the Music module is turned off").
       - Guard the top of SearchMusicMissing and ImportMusicDownloads (return), and GrabDiscography and ScanMusicLibrary (return ErrModuleOff). detectStalledMusic is NOT gated, so in-flight grabs still resolve.
       - main.go wires SetModuleGate next to SetMusic (main.go:330): "music" → settingsSvc.GetBool(ctx, settings.KeyModuleMusic, settings.ModuleMusicDefault); every other module → true.
    5. httpapi/settings.go: musicEnabled/booksEnabled read the settings constants (default ModuleMusicDefault). Delete the local key consts and fix the stale doc comment.
    6. httpapi/server.go:
       - Add func (a *api) musicRoute(h http.HandlerFunc) http.HandlerFunc. When !a.musicEnabled(ctx) it calls a.writeError(w, 404, "The Music module is turned off (Settings → System → Modules)").
       - Wrap every /api/v1/music/* handler INSIDE protected/requireRole (lines 402-414), e.g. a.requireRole(auth.RoleManager, a.musicRoute(a.handleAddArtist)), so unauthenticated calls still get 401.
       - Role changes on the GET routes are left to SEC.
    7. /status: replace the plannedModules var with a.modules(ctx) []module. Books is enabled from booksEnabled; music is {enabled: musicEnabled, status: "preview"}. Delete the stale roadmap comment.
    8. Web:
       - me.tsx: musicEnabled defaults to false (context default and useState).
       - App.tsx: render the three /music routes only when musicEnabled (pattern at :56/:71).
       - nav.ts gains an optional badge: "Preview" on the Music item, and Sidebar.tsx renders it as a small mono pill in the existing chip style (rounded-full font-mono text-[10.5px] uppercase, var(--panel-2) / var(--ink-faint)).
       - Settings.tsx:166: label "Music (preview)", hint "Artists and albums from MusicBrainz with automatic album downloads. Still being hardened. Turning it off hides Music and stops its searches and imports; finished downloads wait until it's back on. Nothing is deleted."
       - Leave the Modules subtitle generic. It must not claim that Books stops its jobs, because Books doesn't.
    9. README.md Features: 'Music (preview, off by default): artists, albums and whole-discography grabs.'
  - **Files:** `internal/settings/settings.go`, `internal/settings/settings_test.go`, `internal/music/repo.go`, `internal/music/service.go`, `internal/automation/coordinator.go`, `internal/automation/music.go`, `internal/automation/music_scan.go`, `internal/httpapi/settings.go`, `internal/httpapi/server.go`, `internal/httpapi/music_gate_test.go`, `cmd/arrmada/main.go`, `web/src/lib/me.tsx`, `web/src/App.tsx`, `web/src/lib/nav.ts`
  - **Acceptance:**
    - Fresh install:
- no Music entry in the nav
- GET /api/v1/music/artists returns 404 with the module-off message
- /music deep links render the 404 page
- no 'music:' search log lines appear
    - Upgrading a DB that has at least one artist and no saved setting keeps Music on. The settings row reads 'true', and the startup log line says why.
    - Switching Music off (once the CFG Save fix is in):
- no new arrmada-music torrents are added, and the next cycle logs no music search lines
- POST discography and POST scan return 404
- switching it back on restores everything without a restart
    - An in-flight music grab still resolves while the module is off (the detect-stalled music branch keeps running).
    - /status reports music with status 'preview' and the live enabled flag. The word 'roadmap' no longer appears in Settings or in the server.go module comment.
  - **Tests:** Go (settings): EnsureModuleDefault:
- unset + keepOn true → value 'true', changed=true
- unset + keepOn false → stays unset (GetBool returns false)
- explicit 'false' is never overwritten
- keepOn error → no write; Go (music): HasArtists is false on an empty DB and true after CreateArtist.; Go (httpapi, security_test.go setup): a music route returns 404 when off and 200 when on; an unauthenticated request still gets 401.; Go (automation): with the gate returning false:
- SearchMusicMissing and ImportMusicDownloads return before touching c.downloads or c.indexers (nil services don't panic)
- GrabDiscography and ScanMusicLibrary return ErrModuleOff
- a nil gate behaves as on; UI (manual):
- off → no Music in the Sidebar, /music renders NotFound, no Music pill in Downloads
- on → the Preview pill shows
  - **Depends on:** CFG-xx (fix the broken Save settings round-trip: GET returns server_time/server_tz, which PUT rejects). Without it the toggle can't be switched off from the UI; default-off and the keep-on rule work regardless., SEC-xx (deny-by-default route roles) for the GET music routes; soft
  - **Risk:** The startup keep-on check is the only thing that stops an active music user from having the module silently switched off, so it must log clearly and must never overwrite an explicit value. While the module is off, finished music torrents sit unimported and import once it is switched back on (the hint says so). Books keeps its current toggle behaviour. The gate is built so Books can reuse it later.
  - **Resolves:** music-12
<a id="mus-02"></a>
- [ ] **MUS-02 · Per-album search backoff, 25-album cap per sweep, and no searches for albums with no listing or not yet released** — `P0` · `M` · Phase 0
  - **Problem:** The SearchMusicMissing doc comment promises exponential backoff, but the loop (internal/automation/music.go:45-62) only skips albums that are unmonitored, complete or downloading, then calls grabAlbum. grabAlbum runs a full multi-indexer search every time and never records a miss. The albums table (0070_music.sql) has no search-state columns. The job runs every 30 minutes (main.go:396).

Costs:
- An artist with 20 unfindable albums costs about 960 indexer searches a day. That risks tracker API caps and bans that would also hit Movies and Series.
- Albums with no MusicBrainz listing (EnsureTracks returns nil, service.go:133-134) also cost a MusicBrainz call every sweep.
- Pre-announced albums are searched before they exist.

There is also no test harness for the music sweep.
  - **Approach:** 1. Migration (next free number, tentatively 00NN_album_search_backoff.sql, mirroring 0074_book_search_backoff):
       ALTER TABLE albums ADD COLUMN last_search_at TIMESTAMP;
       ALTER TABLE albums ADD COLUMN search_misses INTEGER NOT NULL DEFAULT 0;
    2. internal/music:
       - Album gains LastSearchAt string `json:"last_search_at,omitempty"` and SearchMisses int `json:"search_misses"` (albumCols/scanAlbum).
       - Repo/Service gain RecordSearchMiss(ctx, id) (misses+1, last_search_at = CURRENT_TIMESTAMP) and ResetSearchMisses(ctx, id) (0, last_search_at = now), shaped like books/service.go:331-341.
       - New Repo.WantedAlbums(ctx) returns monitored albums of monitored artists with track counts and have counts in ONE grouped query (albums JOIN artists LEFT JOIN tracks GROUP BY al.id). This replaces the per-album fillAlbumCounts loop in the sweep, and [MUS-18](#mus-18) reuses it.
    3. automation/music.go: musicSearchWait(misses int) time.Duration: 0 for ≤0, searchBackoff(misses) (series.go:333) for 1-6, 7*24h from 7.
    4. Introduce type albumOutcome struct{ Code, Detail, Release string } with code constants and a miss() method:
       - miss: no_listing, no_results, no_match, blocked, below_profile
       - not a miss: indexer_error, listing_error, grab_failed, no_space
       - grabbed resets the misses
       grabAlbum returns it instead of returning silently. Detail strings come from what it already knows (candidate counts, best tier vs profile). This follows ACQ's rule: no miss when every indexer errored.
    5. SearchMusicMissing:
       - Read the queue once (existing).
       - Build the due list from WantedAlbums: !Complete; release_date empty or ≤ today (YYYY-MM-DD string compare; a year-only date in the future also counts as future); not albumDownloading; now ≥ last_search_at + musicSearchWait(misses).
       - Sort never-searched first, then oldest last_search_at.
       - Process at most musicSweepCap = 25.
       - Miss → RecordSearchMiss. Grabbed → ResetSearchMisses. no_space → stop the sweep for this cycle, since every other album would hit the same wall.
       - Log once at the 7-miss transition: "music: nothing found 7 times, searching weekly from now on".
       - Future-dated albums are skipped and NOT counted as misses.
    6. grabAlbum: after EnsureTracks, re-read the track count via c.music.Tracks. If it is still 0, return no_listing without an indexer search.
    7. Test seams on Coordinator (nil means the real service), used by SearchMusicMissing, grabAlbum and GrabDiscography:
       - musicSearchFn func(context.Context, indexer.SearchQuery) (indexer.SearchResult, error)
       - musicQueueFn func(context.Context) ([]download.Item, error)
       - musicGrabFn func(ctx, indexerName, url, title, category string) (string, error)
    8. New internal/automation/music_harness_test.go: musicTestCoord(t), modelled on packTestCoord (books_packs_test.go:17):
       - store.Open(t.TempDir())
       - music.NewService(db, &fakeMusicProvider{}, slog.Default())
       - quality.NewService(db)
       - library.NewImporter(t.TempDir(), …)
       - an eventbus
       fakeMusicProvider implements metadata.MusicProvider from canned artists, albums and tracks.
    9. Rewrite the doc comment at music.go:23-29 to describe the real behaviour.
  - **Files:** `internal/store/migrations/00NN_album_search_backoff.sql`, `internal/music/music.go`, `internal/music/repo.go`, `internal/music/service.go`, `internal/automation/music.go`, `internal/automation/coordinator.go`, `internal/automation/music_harness_test.go`, `internal/automation/music_sweep_test.go`
  - **Acceptance:**
    - Two sweeps back to back: the second makes no indexer search for an album that just missed.
    - An album with no MusicBrainz track listing never triggers an indexer search.
    - A future-dated album is not searched and its search_misses stays 0.
    - A successful grab sets search_misses back to 0 in the DB.
    - For an artist with 20 unavailable albums, a 24 h log shows at most about 7 searches per album and never more than 25 album searches in one sweep, then weekly retries.
  - **Tests:** Go: TestMusicSearchWait table: 0→0, -1→0, 1→30m, 2→1h, 6→12h, 7→168h, 50→168h.; Go (music repo): RecordSearchMiss increments and stamps last_search_at; ResetSearchMisses zeroes; WantedAlbums returns the right counts and excludes unmonitored artists and albums.; Go (sweep, musicTestCoord, with musicSearchFn counting calls):
- an immediate second sweep makes 0 searches
- a no-listing album makes 0 searches and records a miss
- 40 due albums → exactly 25 searched, oldest first
- a future-dated album → 0 searches and 0 misses
- musicSearchFn returning an error → no miss recorded
- no_space stops the sweep; Go: grabAlbum outcome codes for no_results, no_match (non-matching releases), below_profile (MP3 against a lossless-only profile) and grabbed (musicGrabFn returns a hash).
  - **Risk:** An album that has missed a lot is retried automatically only weekly. MUS-09 (Search now) and MUS-13 (immediate search of new releases) cover that. Existing rows start at 0 misses, so the first sweeps after deploy still search everything once, 25 per sweep. Land this after MUS-01 because both edit SearchMusicMissing.
  - **Resolves:** music-1

#### Milestone: M2: Decide

_A dated, evidence-backed Rebuild, Park or Cut decision is recorded, and every later MUS task is marked go or won't-do._

<a id="mus-03"></a>
- [ ] **MUS-03 · Decide: rebuild Music as a Lidarr-lite, park it, or cut it** — `P1` · `S` · Phase 1
  - **Problem:** The audit's verdict is to hide Music now and then either rebuild it properly or cut it, because shipping it as it stands is the worst option. Rebuild phases M3–M7 add up to roughly 6-8 weeks of sessions. They only pay off if the household actually listens through Plex/Plexamp or the owner wants Lidarr retired. The decision must be explicit, or the epic drifts into sunk-cost work.
  - **Approach:** 1. Gather evidence read-only. The owner runs these against a read-only copy or with sqlite3 -readonly on the appdata DB. Never ask for credentials in chat.
       - Plex music plays in 90 days: SELECT COUNT(*) AS plays, COUNT(DISTINCT user_id) AS listeners FROM stream_sessions WHERE media_type='track' AND started_at >= CAST(strftime('%s','now','-90 days') AS INTEGER); (the same data as Insights → Graphs' music line, internal/insights/graphs.go)
       - Library counts: SELECT (SELECT COUNT(*) FROM artists), (SELECT COUNT(*) FROM artists WHERE monitored=1), (SELECT COUNT(*) FROM albums WHERE monitored=1), (SELECT COUNT(*) FROM tracks WHERE has_file=1);
       - Grab history: SELECT status, COUNT(*) FROM grabs WHERE media_type='music' GROUP BY status;
       - Insights → Library tab: does Plex have an Artist library, and how big is it?
       - Ask the owner: does anyone use Plexamp, has anyone asked for music, and is Lidarr still running?
    2. Rule of thumb:
       - Rebuild: regular music listening (roughly ≥20 plays a week, or 2+ listeners), or the owner wants Lidarr gone.
       - Cut: about zero plays and no Lidarr.
       - Park: anything in between. M1 stays, everything else becomes won't-do-for-now, revisit in 6 months.
    3. Record the date, evidence numbers, decision and rationale in a new docs/decisions/music.md. Update the README Music line to match.
    4. Mark tasks to match the decision:
       - Rebuild: [MUS-04](#mus-04)..[MUS-24](#mus-24) go. The Preview label stays until [MUS-12](#mus-12).
       - Cut: [MUS-25](#mus-25) goes; [MUS-04](#mus-04)..[MUS-24](#mus-24) are won't-do.
       - Park: everything after M1 is deferred.
       [MUS-01](#mus-01) and [MUS-02](#mus-02) stay done in every case.
  - **Files:** `docs/decisions/music.md`, `README.md`
  - **Acceptance:**
    - A written decision exists, dated, that cites the 90-day Plex music play count, the listener count, and the artist and album counts.
    - Every MUS task after M1 is marked go, won't-do or deferred to match the decision.
  - **Tests:** None (decision task). A reviewer checks that docs/decisions/music.md cites the numbers above and names the chosen path.
  - **Depends on:** [MUS-01](#mus-01), [MUS-02](#mus-02)
  - **Risk:** Insights only sees Plex plays. Listening through other players, or music the household wants but can't get yet, is invisible, so the owner must be asked directly as well. Deferring indefinitely is a real risk; a Park decision must set a revisit date.
  - **Resolves:** 

#### Milestone: M2-alt: Cut path (only if MUS-03 decides Cut; replaces M3–M7)

_Music is gone from the UI, API and scheduler. Its tables, files and torrents are untouched, and it can be restored from git history._

<a id="mus-25"></a>
- [ ] **MUS-25 · If the decision is Cut: retire the Music module cleanly, keeping every byte of data** — `P2` · `S` · Phase 2
  - **Problem:** This task runs only if MUS-03 decides Cut. A half-removed module would leave dead routes, jobs and copy behind. Worse, leftover 'grabbed' music rows would fall through to DetectStalled's default movie branch, which would treat album ids as movie ids: blocklisting under a movie and removing torrents.
  - **Approach:** 1. Remove:
       - the Music nav entry (nav.ts), the SPA routes (App.tsx) and pages (Music.tsx, ArtistDetail.tsx, AlbumDetail.tsx)
       - the Downloads 'Music' pill and the Quality page's music tab and presets UI
       - the API route block (server.go:401-414) and handlers (httpapi/music.go), and the job registrations (main.go:395-404)
       - the Settings toggle and the music entry in /status
       - the README line (replace it with a line in docs/decisions/music.md)
    2. DetectStalled: case "music", "music_artist" → leave the row alone (log once). The movie branch must never see them. ManageSeeding may keep applying recorded seed rules to imported music grabs; the data is hardlinked, so that is safe.
    3. Delete internal/music, automation/music*.go and metadata/musicbrainz.go only after `grep -rn` shows nothing else uses them.
       - The Reviews 'music' branch (reviews.go:277, 360) keeps Reject and Dismiss working for old held items; Import returns a clear 'Music has been removed' error.
       - Keep quality.MediaMusic constants if other code references them.
    4. Keep the DB tables (artists, albums, tracks, artist_events) and every file on disk: no DROP TABLE, no file deletion, no new migration. Old migrations stay, so the module can be restored from git history.
    5. Leave torrents in the arrmada-music category alone.
  - **Files:** `web/src/App.tsx`, `web/src/lib/nav.ts`, `web/src/pages/Music.tsx`, `web/src/pages/ArtistDetail.tsx`, `web/src/pages/AlbumDetail.tsx`, `web/src/pages/Downloads.tsx`, `web/src/pages/Quality.tsx`, `web/src/pages/Settings.tsx`, `internal/httpapi/server.go`, `internal/httpapi/music.go`, `internal/automation/coordinator.go`, `internal/automation/reviews.go`, `cmd/arrmada/main.go`, `internal/automation/music.go`
  - **Acceptance:**
    - No Music appears anywhere in the UI or API, and no music jobs are registered.
    - The music tables and files are untouched (row counts are the same before and after the deploy).
    - A leftover 'grabbed' music row is not blocklisted or removed by DetectStalled.
    - go vet and go test -race pass (race tests run in Docker before pushing).
  - **Tests:** Go: full build plus go vet and go test -race (Docker one-liner).; Go: DetectStalled with a pending 'music' row and an empty queue leaves the row and the blocklist untouched.; UI (manual): no Music in the nav, Settings, Quality or Downloads; an old music review can still be rejected or dismissed.
  - **Depends on:** [MUS-03](#mus-03) (decided Cut)
  - **Risk:** Some code may share the music helpers (quality ladder, reviews), so grep before deleting. The data stays in the DB, so the module can come back.
  - **Resolves:** 

#### Milestone: M3: Rebuild phase 1a: grab and import the right thing

_Accented, punctuated and scene-style names match. Quality tiers are read from whole tokens. Songs with numeric titles land on the right track. Discography and partial-album grabs stall, blocklist and seed correctly. A held track is never downgraded or duplicated._

<a id="mus-04"></a>
- [ ] **MUS-04 · One music text-folding function for release matching and scan keys (accents, apostrophes, &, +, slashes, acronyms)** — `P1` · `S` · Phase 15
  - **Problem:** normWords (internal/music/match.go:150-166) and NormKey (release.go:231-240) keep only ASCII a-z/0-9, and music never calls parser.FoldAccents. A release that keeps the original spelling does match. Matching fails whenever a release spells the name differently from MusicBrainz, which is common:
- Bjork vs Björk, Motorhead vs Motörhead
- ACDC vs AC/DC
- 'Sgt Peppers' vs "Sgt. Pepper's"
- 'and' vs '&'
- scene underscore names ('The_Beatles-Sgt_Peppers_…-FLAC')

indexer/service.go:363 folds accents out of the outgoing query, so results lean ASCII and then fail ReleaseIsForAlbum. The same gate controls in-flight detection (music.go:71), import routing (music.go:304), discography matching and the library scan (music_scan.go:104-123, 179-190). Failures leave only an Info log line.
  - **Approach:** 1. New internal/music/fold.go, foldMusic(s string) string:
       - parser.FoldAccents, then lowercase
       - delete apostrophes (' ’ ‘ ` ´) without leaving a gap
       - standalone '&' and '+' → 'and'
       - join letter/letter slashes and single-letter dotted acronyms ('ac/dc' → 'acdc', 'r.e.m.' → 'rem')
       - treat '_' and '.' as separators in scene names (already true once they are non-alphanumeric)
       - keep unicode.IsLetter/IsDigit runes, so CJK and Cyrillic names don't fold to ''
       - every other run of characters becomes one space
    2. normWords becomes foldMusic. NormKey becomes foldMusic with the spaces removed.
    3. ReleaseIsForAlbum and ReleaseIsDiscographyFor:
       - The artist check also accepts the artist with a leading 'the ' dropped when other words remain ('The Beatles' ↔ 'Beatles'). This applies to the artist only, never the album.
       - An artist that folds to '' returns false. Today `a != "" &&` silently skips the artist check.
    4. Coordinate with BOOK's quick win #13 (fold accents, apostrophes and & in book matching). If BOOK lands a shared parser-level helper first (e.g. parser.FoldWords), build foldMusic on it. Otherwise keep foldMusic self-contained so BOOK can lift it.
    5. Callers need no changes. albumDownloading, grabAlbum, albumForRelease, artistForRelease, scanResolveArtist, matchAlbumByTitle and MatchTracks' title pass all go through these functions.
  - **Files:** `internal/music/fold.go`, `internal/music/fold_test.go`, `internal/music/match.go`, `internal/music/release.go`, `internal/music/match_test.go`, `internal/music/release_test.go`
  - **Acceptance:**
    - When indexers return ASCII-spelled releases ('Bjork - Homogenic (1997) [FLAC]'), a monitored Björk album is grabbed.
    - Scan library matches a library folder 'Bjork/Homogenic' to MusicBrainz 'Björk'.
    - 'Kid A' still does not match a 'Kid Amnesiae' release.
  - **Tests:** Go table, ReleaseIsForAlbum true:
- Björk vs 'Bjork - Homogenic (1997) [FLAC]'
- Motörhead vs 'Motorhead - Ace of Spades [FLAC]'
- Sigur Rós vs 'Sigur Ros - Takk [MP3 320]'
- AC/DC vs 'ACDC - Back in Black'
- "Sgt. Pepper's Lonely Hearts Club Band" vs 'The_Beatles-Sgt_Peppers_Lonely_Hearts_Club_Band-REMASTERED-FLAC'
- "(What's the Story) Morning Glory?" vs 'Oasis - Whats the Story Morning Glory'
- 'Simon & Garfunkel' vs 'Simon and Garfunkel - Bookends'
- 'Florence + the Machine' vs 'Florence and the Machine - Lungs'
- 'The Beatles' vs 'Beatles - Abbey Road'
- R.E.M. vs 'REM - Automatic for the People'; Go table, ReleaseIsForAlbum false:
- 'Kid A' vs 'Radiohead - Kid Amnesiae'
- discography names
- an artist that folds to empty
- 'The The' (dropping 'the' must not leave an empty artist); Go: NormKey('Björk') == NormKey('Bjork'); NormKey of a CJK title is non-empty; matchAlbumByTitle matches '&' against 'and'.; Go: all existing match_test.go and release_test.go cases still pass.
  - **Depends on:** [MUS-03](#mus-03) (decided Rebuild), BOOK-xx (quick win #13 fold helper); soft, coordinate rather than block
  - **Risk:** Looser matching could accept a different album with a near-identical title. Whole-word matching and the artist check still apply, and the 'the' drop applies only to the artist. The acronym join must only fire on single letters separated by dots, so 'Mr. Bungle' is unaffected.
  - **Resolves:** music-2
<a id="mus-05"></a>
- [ ] **MUS-05 · Token-based DetectQuality so 'Escape', 'Palace', 'New Wave', 'Mixtape', 'Isaac' and 'Doggystyle' can't set the tier** — `P1` · `S` · Phase 15
  - **Problem:** DetectQuality (internal/music/release.go:94-108) does plain substring matching on the lowercased name. Current failures:
- 'Journey - Escape [MP3 128]' → FLAC
- 'Palace … [MP3 320]' → ALAC
- 'New Wave [MP3 V0]' → WAV
- 'Roberta Flack' and every 'Escape the Fate' release → FLAC
- 'Mixtape ' matches 'ape '
The lossy fallbacks have the same flaw: 'Isaac' contains 'aac' and 'Doggystyle' contains 'ogg'.

albumScore (automation/music.go:156-175) scores on this tier alone. So 'Lossless only' profiles grab MP3s, and those MP3s outrank real FLAC on seeders.
  - **Approach:** 1. Rewrite DetectQuality to tokenize the lowercased name with strings.FieldsFunc on non-letter/non-digit runes.
    2. Lossless tokens:
       - 'flac' or ^flac\d+$ → FLAC, or FLAC-24 when reHiRes matches (regex unchanged)
       - 'alac' → ALAC
       - 'ape', 'wv', 'wavpack', 'tak' → FLAC tier
       - 'wav' → WAV
    3. Lossy codec fallbacks also go by token: 'aac'/'m4a' → AAC, 'opus', 'ogg'/'vorbis', 'mp3'.
    4. The VBR and bitrate regexes stay as they are (already \b-bounded). '24bit' stays a hi-res modifier only.
    5. Delete containsAny.
    6. ParseRelease keeps calling DetectQuality, so discography and import paths pick up the fix automatically.
    The other half of music-3, the release tier being stored as each track's format, is [MUS-11](#mus-11).
  - **Files:** `internal/music/release.go`, `internal/music/release_test.go`
  - **Acceptance:**
    - A 'Lossless only' profile never grabs a release whose only lossless-looking text sits inside another word.
    - All existing release_test.go cases still pass.
  - **Tests:** Go table, false positives that must now read correctly:
- 'Journey - Escape (1981) [MP3 128]' → MP3-128
- 'Palace - Life After (2019) [MP3 320]' → MP3-320
- 'Against Me! - New Wave (2007) [MP3 V0]' → MP3-V0
- 'Roberta Flack - First Take [MP3 320]' → MP3-320
- 'Escape the Fate - Ungrateful [MP3 320]' → MP3-320
- 'Ed Sheeran - Shape of You [MP3 320]' → MP3-320
- 'Chance the Rapper - Coloring Book (Mixtape) [MP3 320]' → MP3-320
- 'Isaac Hayes - Hot Buttered Soul [MP3 256]' → MP3-256
- 'Snoop Dogg - Doggystyle [MP3]' → MP3-192; Go table, positives that must still be detected:
- '[FLAC]' → FLAC
- 'FLAC 24bit 96kHz' → FLAC-24
- 'WEB-FLAC' → FLAC
- '(ALAC)' → ALAC
- 'WavPack' → FLAC
- '[WAV]' → WAV
- 'Album.FLAC24' → FLAC-24
- '[APE]' → FLAC
- 'AAC 256' → AAC-256
  - **Depends on:** [MUS-03](#mus-03) (decided Rebuild)
  - **Risk:** Releases that say only 'Lossless' with no codec now read as Unknown and are refused, which is the conservative choice. A title word that is exactly 'ape', 'tak' or 'wav' as a whole token (e.g. an album called 'Ape') would still read as lossless. That is rare, and interactive search (MUS-17) shows the tier so it can be spotted.
  - **Resolves:** music-3
<a id="mus-06"></a>
- [ ] **MUS-06 · Safer track-number parsing so '99 Problems', '7 Rings' and '4 Minutes' land on the right track** — `P1` · `S` · Phase 15
  - **Problem:** reLeadingNum (internal/music/match.go:18) reads any 'NN - ' as a disc number whenever another number follows:
- '04 - 99 Problems' parses as disc 4, track 99.
- '10 - 7 Rings' parses as disc 10, track 7.
- '05. 4 Minutes' parses as disc 5, track 4.
Pass 2 fails too, because titleOf strips the same prefix and leaves only 'Problems'.

When the prefix parses as disc 1, the file is silently misfiled: '01 - 7 Rings' lands on track 7, the real track 7 file is left unmatched, and "01 - 22 (Taylor's Version)" reads as track 22. That is exactly the failure match.go:50-58 says it prevents. The tests have no numeric-title cases.
  - **Approach:** Changes in internal/music/match.go:
    1. Split the regex in two:
       - reDiscTrack `^\s*(\d{1,2})[-.](\d{2,3})(?:[\s._)-]|$)`: compact and unspaced, like '1-04' or '2.07'.
       - reTrack `^\s*(\d{1,3})\s*[-._)\s]`.
    2. MatchTracks decides per folder, not per file. A disc-track reading is accepted only when:
       - the disc exists in the listing (≤ max DiscNumber), and
       - at least 2 files in the folder have the compact shape.
       Otherwise every file is parsed track-only. A 3-digit '101 - Title' on a multi-disc listing reads as disc 1, track 1.
    3. titleOf strips only the number token that was actually used.
    4. Agreement check: if a file's stripped title (NormKey) exactly matches a DIFFERENT track than its number points to, prefer the title. If the title matches nothing, keep the number. This prevents 'one off' misfiles.
    5. Pass 2 tries the full base name (minus extension) against track titles before trying the stripped name.
    6. Add AudioFile.Disc (a hint, 0 = unknown) for [MUS-15](#mus-15). When it is set and the filename carries only a track number, use it as the disc.
  - **Files:** `internal/music/match.go`, `internal/music/match_test.go`
  - **Acceptance:**
    - Albums whose song titles start with a number import with every track in the right slot.
    - No file lands on a wrong track in any test case; when in doubt, files are left unmatched.
    - Existing multi-disc '1-04' naming still works.
  - **Tests:** Go TrackNumberOf/MatchTracks cases:
- '04 - 99 Problems.flac' → track 4
- '10 - 7 Rings.flac' → track 10
- '05. 4 Minutes.flac' → track 5
- "01 - 22 (Taylor's Version).flac" → track 1; Go misfile regression: an album with tracks [1 '7 Rings', 7 'Fake Smile'] and files '01 - 7 Rings.flac' and '07 - Fake Smile.flac' → both placed correctly.; Go multi-disc:
- '1-04 Airbag.flac' + '2-01 X.flac' → discs 1 and 2
- '04-99 Problems.flac' alone on a 1-disc listing → track 4
- a folder with a single compact-shaped file falls back to track-only
- an AudioFile.Disc=2 hint with '03 - X.flac' → disc 2, track 3; Go: all existing match_test.go cases still pass.
  - **Depends on:** [MUS-03](#mus-03) (decided Rebuild), [MUS-04](#mus-04) (titleOf/NormKey change underneath); soft
  - **Risk:** Odd naming schemes that mix shapes in one folder fall back to track-only, which may leave some files unmatched. That is deliberate: unmatched is recoverable, a misfile isn't.
  - **Resolves:** music-4
<a id="mus-07"></a>
- [ ] **MUS-07 · Record discography grabs as media_type 'music_artist' so stall, blocklist and seeding act on the artist** — `P1` · `S` · Phase 15
  - **Problem:** GrabDiscography records its grab with recordMusicGrab(ctx, artistID, …) and media_type 'music' (internal/automation/music.go:553), and blocklists with dropBlockedMusic(ctx, artistID) (:539). Every 2 minutes, detectStalledMusic reads g.MovieID as an album id (:477). The artist's discography is therefore judged by whichever album happens to share its numeric id:
- No such album: the grab is marked failed within 2 minutes and is never seed-managed.
- That album is complete: the grab is marked 'imported', so ManageSeeding may remove a torrent that is still importing (with seeding off for that indexer).
- Stall fail-over blocklists the release under an unrelated album.
  - **Approach:** 1. recordMusicGrab gains a mediaType parameter. grabAlbum passes 'music'; GrabDiscography passes 'music_artist' with the artist id.
    2. Blocklist helpers become media-type aware:
       - blockedSetMusicArtist(ctx, artistID) → blockedSetOf(ctx, artistID, "music_artist")
       - addBlockMusicArtist(ctx, artistID, title, indexer, reason)
       GrabDiscography uses both.
    3. Add one shared helper, setMusicGrabStatus(ctx, mediaType, id, infoHash, releaseName, status). It mirrors setSeriesGrabStatus (store.go:443): hash first, then a normRelease title fallback, and it closes rows before writing. [MUS-08](#mus-08) reuses it.
    4. DetectStalled (coordinator.go:1216-1228) gets case 'music_artist' → detectStalledDiscography:
       - hash in imports (hashAlreadyImported) → 'imported'
       - otherwise the usual stall window → addBlockMusicArtist → remove the torrent → 'failed', plus the artist event 'Discography stalled after N min, blocklisted'
       - The default movie branch must never see these rows.
    5. ImportMusicDownloads discography branch (music.go:210-231): on placed > 0, also setMusicGrabStatus(ctx, 'music_artist', art.ID, it.Hash, it.Name, 'imported').
    6. Migration (next free number, tentatively 00NN_music_discography_grabs.sql). Rows in grabs and blocklist with media_type='music' and a lower(title) LIKE any of '%discography%', '%complete collection%', '%all albums%', '%box set%', '%boxset%' or '%anthology%' become 'music_artist'. The patterns mirror reDiscographyTG (release.go).
    7. No label change is needed here. Downloads and Activity label by download category; arrmada-music labelling is ACQ's ops-10 fix.
  - **Files:** `internal/automation/music.go`, `internal/automation/coordinator.go`, `internal/automation/store.go`, `internal/store/migrations/00NN_music_discography_grabs.sql`, `internal/automation/music_discography_test.go`
  - **Acceptance:**
    - After a discography grab, the grabs row has media_type 'music_artist' and the artist id.
    - The grab is never marked failed or imported because of an unrelated album.
    - Once the discography imports something, the grab is 'imported' and its seed rule applies.
    - A stalled discography is blocklisted under the artist and writes an artist event.
  - **Tests:** Go (musicTestCoord + seams): GrabDiscography records media_type music_artist with the artist id.; Go: DetectStalled with a music_artist grab whose artist id equals an unrelated complete album's id leaves the status 'grabbed'. With its hash in imports, the status becomes 'imported'.; Go: a stalled music_artist grab (musicQueueFn with no item) is blocklisted under media_type music_artist, and blockedSetMusicArtist returns it.; Go: the migration reclassifies 'Artist - Discography (1990-2010) [FLAC]' and leaves 'Artist - Album [FLAC]' alone.
  - **Depends on:** [MUS-03](#mus-03) (decided Rebuild), [MUS-02](#mus-02) (search and grab seams for the tests)
  - **Risk:** The LIKE patterns could reclassify an old single-album row titled '…Anthology…'. ParseRelease already treats such names as discographies, so this stays consistent with import routing.
  - **Resolves:** music-6
<a id="mus-08"></a>
- [ ] **MUS-08 · Partial albums: mark the grab imported, never overwrite a track with a worse copy, recycle replaced siblings** — `P1` · `M` · Phase 15
  - **Problem:** A music grab only becomes 'imported' when the album is Complete (internal/automation/music.go:482-485). ManageSeeding reads only 'imported' grabs (store.go:327-330), so any partial import (say 11/12 tracks) seeds forever and its seed rules never run.

importAlbumContent re-places every matched track with no HasFile check (music.go:350-369; the scan has one at music_scan.go:160). pickBestAlbum never compares against the tier already held. BetterFormat/FormatRank are never called. On a re-grab:
- A same-extension lower tier replaces the existing file. linkOrCopy recycles the old one (importer.go:1433-1451), so it is a silent downgrade.
- A different extension writes a second sibling file (.mp3 next to .flac) and orphans the old one.

When a seeding torrent is removed, detectStalledMusic blocklists the good release with a false 'stalled' reason, because findQueued no longer finds it.
  - **Approach:** 1. ImportMusicDownloads (music.go:266-269): when placed > 0, call setMusicGrabStatus(ctx, 'music', album.ID, it.Hash, it.Name, 'imported') (the [MUS-07](#mus-07) helper). ManageSeeding then applies the indexer's seed rule.
    2. If the album is still incomplete after import:
       - call c.music.RecordSearchMiss ([MUS-02](#mus-02))
       - write the artist event 'Imported 11/12 of "X"; still missing: "Bonus Track"', listing up to 5 titles
    3. Migration (next free number, tentatively 00NN_music_partial_grabs.sql) heals existing rows: UPDATE grabs SET status='imported' WHERE media_type='music' AND status='grabbed' AND info_hash != '' AND lower(info_hash) IN (SELECT lower(download_hash) FROM imports).
    4. detectStalledMusic: a grab whose hash is already in imports is marked 'imported' and never blocklisted as stalled. The Complete() shortcut stays.
    5. Tier helpers in internal/music/format.go replace the unused FormatRank/BetterFormat (remove them and move format_test.go cases over):
       - HeldTier(format string, bitrate int) Quality accepts both tier names ('MP3-320', written by imports) and extension tags ('MP3', 'FLAC', written by the scan; a bitrate maps MP3 to a tier).
       - BetterTier(cand, held Quality) bool: strictly higher QualityRank, except that lossless vs lossless never churns (FLAC over ALAC is not an upgrade).
       - Repo.HeldTiers(ctx, albumID) returns the lowest held tier.
    6. importAlbumContent and importAlbumFolder (music.go:350-369, 649-667): for a track that already HasFile, place the new file only when BetterTier(newTier, HeldTier(t.Format, t.BitrateKbps)). Otherwise skip it and count it as 'kept'.
    7. If the better file lands at a different path (extension change), recycle the old file with a new library.Importer.RecycleReplaced(path) (string, error): RecycleFile(im.recycleDir, path). With no recycle dir it keeps the old file and logs. Never hard-delete.
    8. grabAlbum: when HaveTracks > 0, drop candidates whose DetectQuality rank is below the album's lowest held tier. Report them as outcome below_profile with the detail 'releases below the MP3-320 you already have'.
  - **Files:** `internal/automation/music.go`, `internal/automation/store.go`, `internal/music/format.go`, `internal/music/format_test.go`, `internal/music/repo.go`, `internal/library/importer.go`, `internal/store/migrations/00NN_music_partial_grabs.sql`, `internal/automation/music_import_test.go`
  - **Acceptance:**
    - Partial-album torrents leave the client according to their seed rule (visible on the Seeding tab).
    - After a re-grab, an album folder never holds both '05 - X.flac' and '05 - X.mp3'.
    - A held FLAC track is never replaced by a lower tier.
    - Removing a seeding music torrent no longer blocklists its release as 'stalled'.
    - A partial import writes an artist event naming the missing tracks and backs the album off.
  - **Tests:** Go (musicTestCoord with temp-dir byte-blob tracks): a partial import sets the grab status to 'imported' and records a search miss.; Go: re-importing an MP3-320 download over FLAC tracks leaves the files on disk and the DB format unchanged.; Go: a FLAC download over MP3 tracks records the new .flac path and moves the old .mp3 into the temp recycle dir; with no recycle dir the old file stays.; Go: HeldTier/BetterTier table (MP3 tag + 320 → MP3-320; FLAC over ALAC → false; FLAC over MP3-320 → true; unknown held → any known tier is better).; Go: the candidate filter drops releases below the held tier.; Go: the heal SQL marks a grabbed music row imported when its hash is in imports and leaves others alone.; Go: detectStalledMusic with the hash in imports and no queue item → 'imported', no blocklist row.
  - **Depends on:** [MUS-02](#mus-02) (miss accounting, harness), [MUS-07](#mus-07) (setMusicGrabStatus helper; both edit detectStalledMusic), SAFE-xx (recycle-bin cap never purges the newest item); soft, should land before replaced tracks start going to the bin
  - **Risk:** Marking a grab imported on the first partial import lets ManageSeeding remove the torrent (and its data, when seeding is off) while unmatched files exist only in the download. This matches movie behaviour, and the Review flow still works while the torrent seeds. Replaced files take space in the recycle bin. HeldTier reads the stored tier string until MUS-11 makes it a measured value.
  - **Resolves:** music-5

#### Milestone: M4: Rebuild phase 1b: tell the truth, then graduate

_Every wanted album shows when it was searched and why nothing was grabbed, and offers Search now. Library and artist stats are real. Track formats are measured, not taken from the release name. After a 7-day soak the Preview label comes off._

<a id="mus-09"></a>
- [ ] **MUS-09 · Say why an album isn't downloading: persisted search outcomes, History events, and Search now** — `P1` · `M` · Phase 15
  - **Problem:** There is no album search route (internal/httpapi/server.go:401-414). grabAlbum returns silently on an indexer error or an empty result (automation/music.go:92-93). 'No release matched', 'no release met the profile' and 'not enough space' are log lines only (:105-121) and never become artist_events. detectStalledMusic writes no event. The album page offers only a Monitor toggle, and music pages don't use useLive.

The owner sees a wall of wanted albums with no way to tell whether Arrmada searched, found nothing, rejected everything or ran out of space, and no way to try again. Without this there is also no workaround for matching bugs.
  - **Approach:** 1. Migration (next free number, tentatively 00NN_album_search_outcome.sql): albums.last_search_outcome TEXT NOT NULL DEFAULT '' and albums.last_search_detail TEXT NOT NULL DEFAULT ''.
    2. music.Repo.RecordSearchOutcome(ctx, albumID, code, detail, miss bool). It sets last_search_at, the outcome and the detail, and increments or resets misses per [MUS-02](#mus-02)'s rules, all in one UPDATE. The sweep switches to it.
    3. Events: write an artist_events 'searched' row only when the code differs from the stored one, e.g. 'OK Computer: 14 releases, none named this album', so History doesn't flood. 'grabbed' keeps its existing event. detectStalledMusic writes a 'failed' event ('Download stalled after 60 min, blocklisted: <release>').
    4. Album JSON adds last_search_at, last_search_outcome, last_search_detail and a computed next_search_at: last_search_at + musicSearchWait(misses), 'now' when never searched, the release date when it is in the future.
    5. Coordinator:
       - SearchAlbumNow(ctx, albumID) (albumOutcome, error) ignores the wait but still honours albumDownloading, the module gate and the disk check.
       - SearchArtistNow(ctx, artistID) (int, error) runs the artist's monitored incomplete albums one at a time in a goroutine with a 30 min context. A sync.Map guard prevents a second run for the same artist.
    6. Routes, all RoleManager and wrapped in musicRoute:
       - POST /api/v1/music/albums/{id}/search: synchronous, 90 s timeout, returns {outcome, detail, release}.
       - POST /api/v1/music/artists/{id}/search: 202 {count}.
    7. UI:
       - AlbumDetail.tsx: a 'Search now' button and an outcome line ('Searched 2 h ago: 14 releases, none named this album. Next automatic search in about 6 h.'). Copy for each code lives in one map in web/src/lib/api.ts.
       - ArtistDetail.tsx: album rows show the short outcome under the title, and the header gets 'Search monitored'.
       - HistoryPanel: a tone for 'searched'.
       - Both pages refetch on 'music.imported' via useLive (web/src/lib/useLive.ts).
       - Wording should match ACQ's shared 'why isn't this downloading' copy where it exists.
  - **Files:** `internal/store/migrations/00NN_album_search_outcome.sql`, `internal/music/music.go`, `internal/music/repo.go`, `internal/music/service.go`, `internal/automation/music.go`, `internal/httpapi/music.go`, `internal/httpapi/server.go`, `web/src/pages/AlbumDetail.tsx`, `web/src/pages/ArtistDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Every monitored missing album shows when it was last searched, why nothing was grabbed, and when the next automatic search is.
    - 'Search now' answers in the UI within 90 s, even while the album is backed off.
    - Artist History shows when an album's search result changes, not one entry per sweep.
    - A stalled album download appears in History.
  - **Tests:** Go: each outcome code is produced by a scripted case: musicSearchFn returning nothing, non-matching releases, below-profile releases, an error; musicGrabFn success; a disk-full stub via downloadsDir.; Go: event dedupe: two identical outcomes in a row write one event; a different code writes a second.; Go (httpapi): the album search endpoint searches even with misses=5 and last_search_at=now, and returns 404 when the module is off.; Go: next_search_at for never-searched, backed-off and future-dated albums.
  - **Depends on:** [MUS-02](#mus-02)
  - **Risk:** The synchronous search holds a request for up to 90 s, the same pattern as book releases. The background artist search is an untracked goroutine until BE's job runner exists; move it there when that lands.
  - **Resolves:** music-9
<a id="mus-10"></a>
- [ ] **MUS-10 · Count completeness by album, and show real stats on the artist page** — `P1` · `S` · Phase 15
  - **Problem:** artistStats LEFT JOINs tracks (internal/music/repo.go:71-96), so albums whose listing hasn't been fetched add nothing. Music.tsx then marks an artist Complete when have >= total (:20-22). A scanned artist with 1 of 20 albums shows '12/12' and is listed under Complete.

Service.GetArtist (service.go:36-47) never fills Stats, so the artist page always shows '0 albums · 0/0 tracks' (ArtistDetail.tsx:93). As a knock-on, mostlyMissing (:72) offers 'Grab discography' for every artist with more than one album, however much of it the user owns.
  - **Approach:** 1. repo.artistStats: aggregate per album first (track count, have count, monitored) in a subquery, then per artist. New ArtistStats fields (JSON snake_case), alongside the existing Albums, Tracks, HaveTracks and SizeBytes:
       - AlbumsMonitored
       - AlbumsComplete (track_count > 0 && have >= count)
       - AlbumsUnlisted (no tracks)
       - AlbumsWithFiles (have > 0)
    2. Factor the per-album aggregation into statsFromAlbums([]Album) so Service.GetArtist fills Stats from the albums it already loads, with no extra query.
    3. Music.tsx:
       - isComplete = albums_monitored > 0 && albums_complete == albums_monitored && albums_unlisted == 0 among monitored albums.
       - Cards show '3/20 albums' first, tracks second.
       - The Incomplete filter uses the new rule; unlisted albums count as unknown, never complete.
       - Filter chips show counts.
    4. ArtistDetail.tsx: the header shows real album and track counts. mostlyMissing = albums_with_files / albums < 0.25.
    5. Listings for unlisted monitored albums are fetched in the background by [MUS-13](#mus-13).
  - **Files:** `internal/music/repo.go`, `internal/music/service.go`, `internal/music/music.go`, `internal/music/repo_test.go`, `web/src/pages/Music.tsx`, `web/src/pages/ArtistDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A scanned artist with 1 of 20 albums shows '1/20 albums' and appears under Incomplete.
    - The artist page header shows the real album and track counts.
    - 'Grab discography' is hidden for artists who own at least 25% of their albums.
  - **Tests:** Go (repo): an artist with one complete album and one unlisted album is not complete and reports AlbumsUnlisted=1 and AlbumsComplete=1.; Go: GetArtist returns non-nil Stats that equal the ListArtists stats for the same artist.; UI (manual, test library in a temp dir): filter counts and the artist header.
  - **Depends on:** [MUS-03](#mus-03) (decided Rebuild)
  - **Risk:** Low. Until MUS-13 fills listings, many scanned artists show 'unknown' albums. That is honest, but the Incomplete list will look long at first.
  - **Resolves:** music-10
<a id="mus-11"></a>
- [ ] **MUS-11 · Record each track's real codec and bitrate with ffprobe; keep the release tier separate** — `P2` · `M` · Phase 15
  - **Problem:** On import, the tier read from the release name is stored as every track's format (internal/automation/music.go:349, 364, 642-663). AlbumDetail shows it as a green badge (AlbumDetail.tsx:161-165), so a mislabeled release shows MP3s as FLAC. The scan stores only the extension tag ('MP3') with bitrate 0 (music_scan.go:162). The no-downgrade check (MUS-08) and the upgrade sweep (MUS-16) need the tier actually held.
  - **Approach:** 1. internal/mediainfo: ProbeAudio(path) (AudioInfo{Codec, BitrateKbps, BitsPerSample, SampleRateHz, DurationSec}, error).
       - Use the existing ffprobe invocation (-show_format -show_streams) and the first audio stream.
       - Read stream bit_rate, falling back to format.bit_rate, plus bits_per_raw_sample / sample_fmt and sample_rate.
       - The JSON parse is a separate parseAudio([]byte) for tests.
    2. internal/music: TierFromProbe(AudioInfo) Quality:
       - flac/alac/wav(pcm_*)/ape/wavpack by codec; FLAC-24 when 24-bit or above 48 kHz
       - mp3 by bitrate: ≥315 → MP3-320; 230-314 VBR → MP3-V0; 250-260 CBR → MP3-256; 170-229 → MP3-V2/192 by mode; <170 → MP3-128
       - aac ≥240 → AAC-256; opus → OPUS; vorbis → OGG
    3. Coordinator seam probeAudioFn (nil → mediainfo.ProbeAudio). importAlbumContent, importAlbumFolder and recordAlbumFiles (scan) store tracks.format = measured tier and bitrate_kbps = measured bitrate.
       - When mediainfo.Available() is false or the probe fails, use the release tier only if it agrees with FormatOf(ext) on lossless vs lossy; otherwise use the extension tag.
       - tracks.source_release keeps the release name.
    4. Backfill: ScanMusicLibrary re-probes up to 200 tracks per run where has_file=1 AND bitrate_kbps=0 (read-only ffprobe; files are never modified).
    5. Album JSON gains held_tier, the lowest tier among held tracks (music.Repo.HeldTiers from [MUS-08](#mus-08)). HeldTier now reads exact values.
    6. AlbumDetail.tsx:
       - the per-track badge is green only for lossless, with the bitrate in a tooltip
       - the header says 'Held: MP3-320'
       - when the release name claimed a better tier than measured, show a small 'labelled FLAC' note
  - **Files:** `internal/mediainfo/mediainfo.go`, `internal/mediainfo/mediainfo_test.go`, `internal/music/release.go`, `internal/music/release_test.go`, `internal/music/repo.go`, `internal/automation/music.go`, `internal/automation/music_scan.go`, `web/src/pages/AlbumDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - An album grabbed from a release mislabeled 'FLAC' that actually contains MP3s shows MP3-xxx on the album page.
    - After a rescan, previously scanned files show real tiers and bitrates.
    - With ffprobe missing, imports still work and fall back as described.
  - **Tests:** Go: parseAudio with canned ffprobe JSON for FLAC 24/96, MP3 320 CBR, MP3 V0 and AAC 256. No real library files are used.; Go: TierFromProbe table, including pcm_s16le → WAV and opus.; Go (musicTestCoord, probeAudioFn stub): an import stores the measured format and bitrate; a disagreeing release tier with no probe falls back to the extension tag.
  - **Depends on:** [MUS-05](#mus-05) (correct release tier for the fallback agreement check), [MUS-08](#mus-08) (HeldTiers / held_tier); either order works, but land after [MUS-08](#mus-08) to avoid conflicts in importAlbumContent
  - **Risk:** ffprobe on every track adds a few seconds per album import. Inferring VBR tiers is approximate, and the bands should be generous so MUS-16 doesn't churn on borderline files.
  - **Resolves:** music-3, music-5
<a id="mus-12"></a>
- [ ] **MUS-12 · Graduate Music out of Preview after a 7-day soak** — `P2` · `S` · Phase 15
  - **Problem:** MUS-01 labels Music 'Preview' until the Phase 1 correctness work (M3 and M4) has shipped. Someone has to check that it holds up on the real server and then lift the label, or the label either lingers forever or comes off untested.
  - **Approach:** 1. Soak: with [MUS-04](#mus-04)..[MUS-11](#mus-11) deployed, run Music enabled on the owner's server for 7 days. Watch:
       - Logs, filtered to the 'music:' prefix: searches per day stay within the backoff/cap expectation.
       - Indexers page: no rate-limit or ban warnings.
       - At least one album goes end to end: Search now → grab → import, with a measured tier that matches the profile.
       - No 'stalled' blocklist rows for completed music torrents.
       - The Seeding tab removes music torrents per their rule.
       Record the results in docs/decisions/music.md.
    2. Then:
       - /status music status → 'available'
       - remove the Sidebar 'Preview' badge (nav.ts)
       - Settings label 'Music', hint 'Artists and albums from MusicBrainz. Monitored albums download automatically, and each album says why it isn't downloading yet. Turning it off hides Music and stops its searches and imports; nothing is deleted.'
       - README line 'Music: artists, albums, automatic album downloads, discography grabs'
    3. The default stays off for fresh installs (opt-in module) unless the owner decides otherwise in docs/decisions/music.md.
  - **Files:** `internal/httpapi/server.go`, `web/src/lib/nav.ts`, `web/src/components/Sidebar.tsx`, `web/src/pages/Settings.tsx`, `README.md`, `docs/decisions/music.md`
  - **Acceptance:**
    - docs/decisions/music.md holds the 7-day soak results: searches per day, indexer warnings (none), and one end-to-end album with its tier.
    - No 'Preview' or 'still being hardened' copy remains, and /status reports 'available'.
  - **Tests:** Go (httpapi): /status reports music status 'available' and the live enabled flag.; UI (manual): no Preview pill; the Settings hint is updated.
  - **Depends on:** [MUS-04](#mus-04), [MUS-05](#mus-05), [MUS-06](#mus-06), [MUS-07](#mus-07), [MUS-08](#mus-08), [MUS-09](#mus-09), [MUS-10](#mus-10), [MUS-11](#mus-11)
  - **Risk:** A soak on one server with few artists may not exercise edge cases. Keep the Preview label if any acceptance item fails rather than graduating on a partial pass.
  - **Resolves:** music-12

#### Milestone: M5: Lidarr-lite: keeps itself current

_New releases from monitored artists are found and searched within a day. Adding an artist offers profile, monitor mode and release types. Multi-disc folders scan as one album, and vanished files go back to Wanted. Albums upgrade along the profile ladder when upgrades are on._

<a id="mus-13"></a>
- [ ] **MUS-13 · Scheduled artist refresh that finds new releases, searches them, and fills missing listings** — `P2` · `M` · Phase 15
  - **Problem:** New release groups only arrive through Service.Refresh, and that runs only from the manual Refresh button (internal/httpapi/music.go:92). No refresh-artists job is registered (cmd/arrmada/main.go:395-404). Monitored artists therefore never pick up a new album, which is the main reason to run Lidarr. Scanned artists' other albums also never get listings, so their completeness stays unknown (MUS-10).
  - **Approach:** 1. Migration (next free number, tentatively 00NN_artist_refresh.sql): artists.last_refreshed_at TIMESTAMP.
    2. music.Service.syncAlbums returns ([]Album newlyInserted, total int, error). It checks for an existing mbid (repo.AlbumIDByMBID) before UpsertAlbum, whose ON CONFLICT already preserves the user's monitored flag. New albums are monitored per the artist's policy: artist.Monitored now, monitor_new once [MUS-14](#mus-14) lands. Refresh stamps last_refreshed_at.
    3. automation RefreshMusicArtists(ctx), module-gated:
       - Pick artists with last_refreshed_at NULL or older than 20 h, nulls first, oldest first, at most 50 per run. The shared MusicBrainz client already paces at 1.1 s.
       - Call Service.Refresh for each.
       - For each new monitored album, write the event 'New release: Title (EP, 2026-10-03)'.
       - If the album is out (release_date ≤ today), EnsureTracks and run SearchAlbumNow ([MUS-09](#mus-09)) once. That bypasses the wait and records the outcome.
       - Afterwards fill listings for up to 30 monitored albums that have none (EnsureTracks only, no search).
    4. Future-dated albums are skipped by the [MUS-02](#mus-02) sweep without misses, so a pre-announced album is searched from its release date on.
    5. main.go: sched.Register("refresh-music-artists", 24*time.Hour, false, …) calling coordinator.RefreshMusicArtists.
  - **Files:** `internal/store/migrations/00NN_artist_refresh.sql`, `internal/music/service.go`, `internal/music/repo.go`, `internal/automation/music.go`, `cmd/arrmada/main.go`, `internal/music/service_test.go`, `internal/automation/music_refresh_test.go`
  - **Acceptance:**
    - Within a day, without pressing Refresh, a new release group for a monitored artist appears on the artist page, writes an event, and is searched once.
    - An album the user unmonitored stays unmonitored after a refresh.
    - Future-dated albums are not searched before their release date.
  - **Tests:** Go (service, fakeMusicProvider whose second ArtistAlbums call returns an extra release group): the group is inserted, returned as new and monitored per the policy; an existing unmonitored album is untouched.; Go (automation): the 50-artist cap, oldest-first ordering and the 20 h freshness skip.; Go: a new, released, monitored album triggers exactly one search via musicSearchFn; a future one triggers none.; Go: the listing fill stops at 30 albums.
  - **Depends on:** [MUS-02](#mus-02), [MUS-09](#mus-09)
  - **Risk:** MusicBrainz load is about 50 artists × 2-3 calls plus up to 30 listing fills (2 calls each) a day, well under the limit. Big libraries take several days to cycle, which is fine. Avoid running at the same moment as a manual scan, because both queue on the same paced client.
  - **Resolves:** music-8
<a id="mus-14"></a>
- [ ] **MUS-14 · Add-artist options (quality profile, monitor mode, release types, search now) and a seeded default music profile** — `P2` · `L` · Phase 15
  - **Problem:** AddArtistModal always sends {mbid, monitored: true} (web/src/pages/Music.tsx:398), and syncAlbums monitors every Album and EP (internal/music/service.go:106-121). Missing pieces:
- no profile picker
- no monitor option
- no release-type choice: ArtistAlbums hard-codes album|ep and drops every secondary type (metadata/musicbrainz.go:190-200)
- no search on add
- no per-artist profile picker on the artist page (the only one is the bulk action)

handleAddArtist falls back to DefaultProfile(music), which falls back to the first custom music profile. No music profile is seeded, so a user who never made one ends up on the permissive FallbackMusicProfile ('Any quality', MP3-128 accepted).
  - **Approach:** Ship this in two commits: the backend, then the UI.
    1. Migration (next free number, tentatively 00NN_artist_monitor_options.sql):
       - artists.monitor_new TEXT NOT NULL DEFAULT 'all' ('all'|'none')
       - artists.release_types TEXT NOT NULL DEFAULT 'Album,EP'
    2. Default profile seeding happens in Go, not SQL, so the ladder isn't duplicated from quality.MusicPresets(). quality.Service.EnsureDefaultMusicProfile(ctx, settings) creates 'Lossless, or the best MP3' when no music profile exists AND the settings flag music_default_profile_seeded is unset, then sets the flag so a profile the user later deletes is never resurrected. It is called from main.go at startup.
    3. metadata MusicBrainz.ArtistAlbums(ctx, mbid, types []string):
       - Request type=album|ep, plus |single only when Singles are selected, so big artists don't page through hundreds of singles.
       - Map album_type to the first secondary type when present (Live, Compilation, Soundtrack, Remix), else the primary type.
       - Return everything; the service filters by artist.release_types (Album, EP, Single, Live, Compilation, Soundtrack).
       - The defaults reproduce today's list exactly.
    4. music.Service.AddArtist(ctx, mbid, profile, AddOptions{Monitor, ReleaseTypes, SearchNow}). Monitor values:
       - all: every album monitored
       - future: existing albums unmonitored, monitor_new='all'
       - latest: the newest released album plus future ones
       - none: nothing monitored, monitor_new='none'
       syncAlbums and the [MUS-13](#mus-13) refresh use monitor_new for newly inserted albums. scanResolveArtist keeps AddArtist(…, Monitor:'none').
    5. httpapi:
       - handleAddArtist accepts monitor, release_types and search_now. search_now defaults to the search_on_add setting and starts SearchArtistNow ([MUS-09](#mus-09)).
       - New PUT /api/v1/music/artists/{id}/options {quality_profile, monitor_new, release_types} (RoleManager, musicRoute). It validates the profile with KnownProfile and the enum values (400 otherwise).
    6. UI:
       - AddArtistModal gets a second step after a result is picked: profile select (default DefaultProfile(music)), monitor radio (All / Future only / Latest / None), release-type checkboxes, and 'Start searching now'.
       - ArtistDetail gets an 'Edit' panel with the same options.
       - Existing modal and form styles are kept.
  - **Files:** `internal/store/migrations/00NN_artist_monitor_options.sql`, `internal/quality/service.go`, `internal/metadata/musicbrainz.go`, `internal/metadata/provider.go`, `internal/music/service.go`, `internal/music/repo.go`, `internal/music/music.go`, `internal/automation/music_scan.go`, `internal/httpapi/music.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`, `web/src/pages/Music.tsx`, `web/src/pages/ArtistDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Adding an artist with 'Future only' makes nothing wanted now, and the artist's next release is wanted.
    - The add dialog shows the chosen profile, and the artist page can change it.
    - A fresh install gets one sensible music profile, and 'Any quality' is no longer the silent default.
    - Existing artists' album lists don't change after a refresh (release_types defaults to Album,EP).
  - **Tests:** Go (service): each monitor option ('future' leaves existing albums unmonitored; 'latest' monitors exactly one released album; 'none' monitors nothing).; Go: the release-type filter keeps Singles only when selected and maps a Live secondary type to album_type 'Live'.; Go (quality): EnsureDefaultMusicProfile seeds exactly once on an empty DB, not when a music profile exists, and not again after the seeded profile is deleted.; Go (httpapi): unknown monitor values and unknown profiles are rejected with 400.; Go (metadata, canned JSON): the query includes 'single' only when requested.
  - **Depends on:** [MUS-13](#mus-13) (refresh consumes monitor_new), [MUS-09](#mus-09) (SearchArtistNow for Search now)
  - **Risk:** Including Singles for big artists can add hundreds of albums, so it stays opt-in. The seeding flag lives in settings; if CFG restructures settings storage, keep the key.
  - **Resolves:** music-8
<a id="mus-15"></a>
- [ ] **MUS-15 · Scan reads CD1/CD2 folders as one album, and tracks whose files vanish go back to Wanted** — `P2` · `M` · Phase 15
  - **Problem:** findAlbumFolders reads Artist/Album/CD1 as artist 'Album', album 'CD1' (internal/automation/music_scan.go:220-228). Multi-disc albums never catalogue, and scanResolveArtist may even create an unrelated artist on an exact name hit (:111-129).

ClearTrackFile (internal/music/service.go:197-200) has no callers and the scan only adds files. Tracks deleted on disk stay has_file=1 forever, and their albums are never wanted again.
  - **Approach:** 1. findAlbumFolders: when the leaf matches reDiscFolder `(?i)^(cd|disc|disk)\s*0*(\d{1,2})\b` (optionally followed by a title), merge it into the parent album:
       - album = parent, artist = grandparent; if the grandparent is the root, parse the parent as 'Artist - Album'
       - all discs become one albumFolder
       - each file carries AudioFile.Disc = n ([MUS-06](#mus-06)), which MatchTracks uses when filenames carry only a track number
       importDiscographyContent gets this for free, since it uses findAlbumFolders.
    2. New Coordinator.VerifyMusicFiles(ctx) (int, error), module-gated:
       - Load has_file tracks (repo.TracksWithFiles: id, album_id, artist_id, album title, file_path).
       - os.Stat each path. For each missing file call music.ClearTrackFile, and write ONE artist event per album: '3 track files of "X" are gone from disk, wanted again'.
       - Missing tracks also reset the album's search_misses, so the sweep picks it up promptly.
    3. Safety: skip the whole pass with a warning, changing nothing, when:
       - the music root (c.imp.MusicDir()) is missing or unreadable, or
       - more than 20% of recorded files are missing at once (an unmounted share or a changed root)
       It never mass-flips a library.
    4. Run it at the end of ScanMusicLibrary and as a daily 'verify-music-files' job registered in main.go.
  - **Files:** `internal/automation/music_scan.go`, `internal/automation/music_scan_test.go`, `internal/music/match.go`, `internal/music/repo.go`, `internal/automation/music.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - A library laid out as Artist/Album/CD1 and CD2 catalogues as one album on Scan library.
    - A track deleted on disk shows as missing after the next verify.
    - An unmounted music share or a changed music root does not flip the library to missing.
  - **Tests:** Go: TestFindAlbumFolders gains Artist/Album/CD1 and CD2 → album=Album, artist=Artist, disc hints 1 and 2; 'Artist - Album/Disc 2' under the root parses too.; Go (temp dirs only): VerifyMusicFiles clears a deleted file and writes one event; it does nothing when the root is missing, and nothing when more than 20% of files are missing.
  - **Depends on:** [MUS-03](#mus-03) (decided Rebuild), [MUS-06](#mus-06) (AudioFile.Disc hint)
  - **Risk:** A slow or flaky share could look like vanished files; the thresholds guard against that. Clearing only changes DB state and never deletes anything.
  - **Resolves:** music-14
<a id="mus-16"></a>
- [ ] **MUS-16 · Music upgrade sweep that honours upgrades_enabled** — `P2` · `M` · Phase 15
  - **Problem:** Music profiles carry upgrades_enabled and the presets turn it on, but no code ever upgrades an album. SearchMusicMissing skips complete albums, and no upgrade-music job exists. Comments in pickBestAlbum (automation/music.go:134-138) and quality/music.go:9-14 assume an upgrade loop that doesn't exist. An owner on 'Lossless, or the best MP3' keeps their MP3s forever. Until this ships, the COPY task for music-7 hides the music 'Automatically upgrade' switch.
  - **Approach:** 1. Migration (next free number, tentatively 00NN_album_upgrade_backoff.sql): albums.last_upgrade_search_at TIMESTAMP, albums.upgrade_misses INTEGER NOT NULL DEFAULT 0.
    2. automation UpgradeMusic(ctx), module-gated, considers complete, monitored albums of monitored artists where:
       - the profile has UpgradesEnabled, and
       - held_tier ([MUS-11](#mus-11)) ranks below the profile's top-scored tier, and
       - now ≥ last_upgrade_search_at + musicSearchWait(upgrade_misses)
       It processes at most 20 albums per run, oldest first, and skips albums in the queue.
    3. Candidates must pass the shared filter (ReleaseIsForAlbum, [MUS-21](#mus-21) sanity, blocklist, pending) and albumScore, and BetterTier(cand, held) must be true. Grab with the event 'Upgrading "X" from MP3-320 to FLAC'. The import replaces files through [MUS-08](#mus-08)'s path, with old files going to the recycle bin. A miss increments upgrade_misses; a grab resets it.
    4. main.go: register 'upgrade-music' every 6 h.
    5. Restore the Quality.tsx music 'Automatically upgrade' switch and copy (hidden by the COPY music-7 task). Rewrite the comments at automation/music.go:134-138 and quality/music.go:9-14 to describe the real loop.
  - **Files:** `internal/store/migrations/00NN_album_upgrade_backoff.sql`, `internal/automation/music.go`, `internal/music/repo.go`, `cmd/arrmada/main.go`, `web/src/pages/Quality.tsx`, `internal/quality/music.go`, `internal/automation/music_upgrade_test.go`
  - **Acceptance:**
    - An album held as MP3-320 under 'Lossless, or the best MP3' is replaced by FLAC when a FLAC release appears, and the MP3s go to the recycle bin.
    - With the switch off, no upgrade searches are logged.
    - An album already at the profile's top tier is never searched.
  - **Tests:** Go: candidate selection: with MP3-320 held and FLAC on top, a FLAC release is accepted and MP3-V0 is rejected.; Go: upgrades_enabled=false → no search (musicSearchFn never called).; Go: held FLAC with FLAC on top → album skipped.; Go: upgrade_misses backoff, and the 20-album cap.
  - **Depends on:** [MUS-11](#mus-11), [MUS-08](#mus-08), [MUS-02](#mus-02), COPY-xx (music-7 copy task that hides the music upgrade switch, draft music.t4)
  - **Risk:** Indexer load, bounded by the per-run cap and the backoff. Replaced files fill the recycle bin, so SAFE's recycle-bin cap fix matters here. VBR tier inference must not cause churn (BetterTier is strict, and lossless never replaces lossless).
  - **Resolves:** music-7

#### Milestone: M6: Lidarr-lite: owner tools

_The owner can see and fix anything from the UI: interactive search with plain-words reasons and manual grabs, a Wanted view, edition switching, and scan results with Match…. Concert videos and fake releases are filtered out._

<a id="mus-17"></a>
- [ ] **MUS-17 · Interactive album search with reasons, and manual grabs that import into the chosen album** — `P2` · `M` · Phase 15
  - **Problem:** There is no interactive search for an album. The owner can't see what indexers return, can't tell why each result was refused, and can't override when the automatic gate is wrong (the music-2/3/4 class of bugs).
  - **Approach:** 1. automation RankAlbumReleases(ctx, albumID) (ReleaseList, error) runs the grabAlbum query plus a foldMusic variant ([MUS-04](#mus-04)) when it differs, deduplicated by normTitle. For each release it fills a RankedRelease (coordinator.go:347):
       - Format = DetectQuality tier
       - Summary like 'FLAC · 412 MB · 12 seeders'
       - Blocklisted
       - Eligible, with a plain-words RejectReason: "doesn't name this album", "MP3-320 isn't on this profile's ladder", "rejected term 'web'", 'blocklisted: stalled', 'lower than the FLAC you have', plus [MUS-21](#mus-21)'s size and category reasons
       - Recommended = the pickBestAlbum choice
       ReleaseList.Why explains the pick.
    2. GrabForAlbum(ctx, albumID, indexer, url, title):
       - grabTo on musicCategory
       - recordMusicGrab('music', albumID)
       - markGrabManual (store.go:217, column from 0076)
       - an event 'Grabbed by hand: <release>'
    3. Import routing: in ImportMusicDownloads, when grabbedMediaForHash finds the album and grabWasManual(hash) is true, import into that album without the release-name check. The user chose it, and MatchTracks still refuses to guess positions.
    4. Routes, both RoleManager and wrapped in musicRoute:
       - GET /api/v1/music/albums/{id}/releases
       - POST /api/v1/music/albums/{id}/grab
    5. UI:
       - AlbumDetail.tsx 'Interactive search' opens ReleaseSearchModal with a new variant="audio" prop that hides the resolution/HDR/feature chips and the bitrate sort and shows the Format column.
       - Movie and series callers don't pass variant, so they are unchanged.
  - **Files:** `internal/automation/music.go`, `internal/automation/coordinator.go`, `internal/httpapi/music.go`, `internal/httpapi/server.go`, `web/src/components/ReleaseSearchModal.tsx`, `web/src/pages/AlbumDetail.tsx`, `web/src/lib/api.ts`, `internal/automation/music_interactive_test.go`
  - **Acceptance:**
    - From an album page the owner sees every release, whether it is eligible and why not, and can grab any of them; the download lands on that album.
    - Movie and series interactive search look unchanged.
  - **Tests:** Go: ranking over a fixed release list asserts eligibility, the reason strings and the recommended pick.; Go: a manual-grab import whose release name doesn't name the album still imports into the grabbed album; a non-manual one still goes to review.; UI (manual): the audio variant of the modal shows no video chips; movie search is unchanged.
  - **Depends on:** [MUS-09](#mus-09), [MUS-04](#mus-04), [MUS-05](#mus-05), [MUS-21](#mus-21) (size and category reasons); soft, FE-xx (shared modal kit); soft
  - **Risk:** A manual override can import the wrong audio. That is the user's explicit choice, and per-track matching still won't guess. Two queries per interactive search double the indexer load for that one action only.
  - **Resolves:** music-9
<a id="mus-18"></a>
- [ ] **MUS-18 · Music Wanted view: every missing album, its last outcome, next try and Search now** — `P2` · `S` · Phase 15
  - **Problem:** Even with per-album outcomes (MUS-09), the owner has to open artist pages one by one to see what's missing and why. The audit's Phase 2 calls for a Music → Wanted view listing every missing album with when it was last searched and why nothing was grabbed. Movies and Series have the same gap; this follows the shared 'why isn't this downloading' pattern.
  - **Approach:** 1. Repo: extend [MUS-02](#mus-02)'s WantedAlbums with paging and an outcome filter: WantedAlbums(ctx, WantedQuery{Outcome string; Limit, Offset int}) ([]WantedAlbum, total int, countsByOutcome map[string]int, error). Each row has album fields, artist name, cover_url, track_count, have_tracks, last_search_at, search_misses, outcome and detail.
    2. GET /api/v1/music/wanted?outcome=&page= (musicRoute; RoleManager, since it drives searches) returns {albums:[…, next_search_at, downloading], total, counts}. downloading comes from one queue read and albumDownloading.
    3. POST /api/v1/music/sweep (RoleManager, musicRoute) runs SearchMusicMissing once in the background, guarded by an atomic.Bool like musicScan, and returns 202. It respects the waits and the 25 cap.
    4. Music.tsx:
       - an 'Artists | Wanted (n)' tab switch
       - Wanted rows: small cover, 'Artist: Album (year)', '3/12 tracks', outcome line, 'next try in ~6 h', Search now ([MUS-09](#mus-09) endpoint)
       - outcome filter chips with counts (No match / Below profile / No listing / Nothing found / Downloading)
       - a 'Run search sweep now' button
       Existing list and chip styles are kept.
  - **Files:** `internal/music/repo.go`, `internal/music/music.go`, `internal/httpapi/music.go`, `internal/httpapi/server.go`, `web/src/pages/Music.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - The Wanted tab lists every monitored missing album with its last outcome and next automatic search, filterable by outcome.
    - Search now on a row updates that row without a reload.
    - 'Run search sweep now' never exceeds the 25-album cap or ignores backoff.
  - **Tests:** Go (repo): WantedAlbums paging, the outcome filter and counts; complete and unmonitored albums are excluded.; Go (httpapi): /music/wanted returns 404 when the module is off; a second POST /music/sweep while one is running returns 409.; UI (manual): filter chips and Search now on a row.
  - **Depends on:** [MUS-09](#mus-09)
  - **Risk:** Low. A large library makes the list long, hence paging.
  - **Resolves:** music-9
<a id="mus-19"></a>
- [ ] **MUS-19 · Pick the MusicBrainz edition that matches the download's track count** — `P2` · `M` · Phase 15
  - **Problem:** AlbumTracks always takes the earliest official release's listing (internal/metadata/musicbrainz.go:236-245). A download of a different edition (deluxe, regional, bonus track) leaves files unmatched or tracks missing forever. The album never completes, keeps being re-searched, and feeds the partial-album problems.
  - **Approach:** 1. metadata:
       - AlbumReleases(ctx, rgMBID) → []ReleaseSummary{ID, Date, Country, Status, TrackCount, Discs}, from one browse call /release?release-group=…&inc=media&limit=100
       - ReleaseTracks(ctx, releaseID)
       Both are added to the MusicProvider interface; fakeMusicProvider implements them.
    2. Migration (next free number, tentatively 00NN_album_edition.sql): albums.mb_release_id TEXT NOT NULL DEFAULT ''. EnsureTracks records the release it picked.
    3. music.Service.RelistAlbum(ctx, album, releaseID), in one transaction:
       - replace the listing
       - keep every row that has a file, re-keyed by disc/track or else by NormKey title
       - delete only file-less rows that aren't in the new listing
       - store mb_release_id
    4. importAlbumContent: when there are audio files and either some are unmatched or len(files) != TrackCount, call AlbumReleases. If an official release has TrackCount == len(files) (ties → earliest) and differs from the current mb_release_id:
       - RelistAlbum
       - re-run MatchTracks
       - write the event 'Switched "X" to the 15-track 2003 edition to match the download'
       This runs at most once per download hash.
    5. AlbumDetail: an 'Edition' select (from AlbumReleases) backed by PUT /api/v1/music/albums/{id}/edition (RoleManager, musicRoute).
  - **Files:** `internal/metadata/musicbrainz.go`, `internal/metadata/provider.go`, `internal/store/migrations/00NN_album_edition.sql`, `internal/music/service.go`, `internal/music/repo.go`, `internal/automation/music.go`, `internal/httpapi/music.go`, `internal/httpapi/server.go`, `web/src/pages/AlbumDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A deluxe-edition download completes the album instead of leaving bonus tracks unmatched or missing forever.
    - The owner can switch an album's edition by hand, and tracks with files are never lost.
  - **Tests:** Go (fake provider with a 12-track standard and a 15-track deluxe edition): a 15-file download relists to deluxe and places all 15.; Go: a held file on a row absent from the new listing is preserved.; Go (httpapi): a manual edition switch returns the new listing; an unknown release id → 400.; Go (metadata, canned JSON): AlbumReleases parses track counts from media.
  - **Depends on:** [MUS-06](#mus-06), [MUS-08](#mus-08)
  - **Risk:** Relisting changes which tracks count as missing. Rows with files are never deleted, so a relist may leave the album with more rows than the edition has; the UI shows these as 'extra'. Each mismatch costs up to two MusicBrainz calls, and only on import.
  - **Resolves:** music-5
<a id="mus-20"></a>
- [ ] **MUS-20 · Scan results the owner can see and act on: unmatched folders with reasons, and Match…** — `P2` · `M` · Phase 15
  - **Problem:** The scan runs in a goroutine and only logs and publishes counts (internal/httpapi/music.go:220-242). MusicScanResult.Unmatched is thrown away apart from len(), and no music page listens for live events. The UI just toasts 'this runs in the background' (Music.tsx:136-151). After a scan the owner can't tell what was imported or why half the collection didn't match.
  - **Approach:** 1. MusicScanResult.Unmatched becomes []UnmatchedFolder{Path, Artist, Album, Reason, Detail}. scanResolveArtist and ScanMusicLibrary return the reason instead of a bool:
       - artist_not_found
       - artist_ambiguous ('2 MusicBrainz artists are named Nirvana')
       - album_not_in_catalogue
       - no_track_matched
    2. Migration (next free number, tentatively 00NN_music_scan_results.sql): music_scan_unmatched(path TEXT PRIMARY KEY, artist TEXT, album TEXT, reason TEXT, detail TEXT, seen_at TIMESTAMP). The last-scan summary (counts, started_at, finished_at) goes in settings key music_last_scan as JSON. Each scan replaces the table contents.
    3. Persistence lives in automation via music.Repo (SaveScanResult, ScanUnmatched(limit, offset), DeleteScanUnmatched), not in the HTTP goroutine.
    4. New endpoints (RoleManager, musicRoute):
       - GET /api/v1/music/scan returns {running, last, unmatched, total}.
       - POST /api/v1/music/scan/match {path, album_id} runs recordAlbumFiles for that folder against that album (MatchTracks still decides positions), then removes the row.
       - POST /api/v1/music/scan/match {path, artist_mbid} adds an ambiguous artist explicitly (unmonitored) and returns their albums to choose from.
    5. UI on Music.tsx:
       - 'Scan library' shows 'Scanning…' and listens for 'library.scanned' via useLive.
       - A results drawer shows the counts and the unmatched list grouped by reason, paginated.
       - Each folder has 'Match…': search the library or MusicBrainz for the artist, then pick the album.
  - **Files:** `internal/automation/music_scan.go`, `internal/store/migrations/00NN_music_scan_results.sql`, `internal/music/repo.go`, `internal/httpapi/music.go`, `internal/httpapi/server.go`, `web/src/pages/Music.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - After a scan the owner sees how many artists, albums and tracks were imported, plus every refused folder with a reason.
    - An unmatched folder can be fixed through Match… without renaming anything on disk.
  - **Tests:** Go (temp-dir fixture, fakeMusicProvider): a scan produces each reason code.; Go (httpapi): the match endpoint records the folder's files against the chosen album and clears the row; module off → 404.; UI (manual): the drawer lists unmatched folders and Match… completes.
  - **Depends on:** [MUS-15](#mus-15), [MUS-04](#mus-04), BE-xx (job runner for the scan goroutine); soft
  - **Risk:** Big libraries produce long unmatched lists, hence pagination. Manual matching still goes through MatchTracks, which never guesses track positions.
  - **Resolves:** music-14
<a id="mus-21"></a>
- [ ] **MUS-21 · Drop video and implausibly sized releases from album searches** — `P3` · `S` · Phase 15
  - **Problem:** grabAlbum searches with no Categories (internal/automation/music.go:89-91), and albumScore never checks Release.Categories or size. torznab.go:347-352 falls back to the indexer's configured categories, so only mixed-category indexers are affected. On those, a name like 'Pink Floyd The Wall 1982 1080p BluRay FLAC 2.0 x264' passes and scores FLAC, and tiny fake 'FLAC' torrents can win on seeders.
  - **Approach:** 1. A pure helper in internal/music/size.go: ReleaseSane(rel Release-like fields, tier Quality, listingSeconds int) (ok bool, reason string).
       - Drop when rel.Categories is non-empty, contains none of 3000/3010/3040/3050/3060, and has no custom tracker category (≥100000). Reason: 'listed as video'.
       - Drop names with video markers (2160p, 1080p, 720p, x264, x265, HEVC, BluRay, WEB-DL) via one regex. Reason: 'looks like a video release'.
       - Size window: expected = listing seconds (sum of tracks.duration_sec, else track count × 240) × the tier's rate in MB/min (FLAC-24 ≈ 20, FLAC/ALAC/WAV ≈ 8 (WAV ≈ 10), MP3-320 ≈ 2.4, V0 ≈ 1.8, 256 ≈ 1.9, 192 ≈ 1.4, 128 ≈ 1). Reject below 35% or above 400% of expected, with a reason like '30 MB is too small for a 45-minute FLAC album'.
    2. The automation candidate filter (shared by grabAlbum, UpgradeMusic and RankAlbumReleases) calls it after ReleaseIsForAlbum. Rejections count toward the no_match/below_profile detail.
    3. Outgoing query categories don't change. Caps-aware audio categories belong to INT's integrations-10 task.
  - **Files:** `internal/music/size.go`, `internal/music/size_test.go`, `internal/automation/music.go`
  - **Acceptance:**
    - No concert-video torrent is grabbed for an album.
    - A 30 MB 'FLAC' release for a 45-minute album is refused, and the reason shows in interactive search.
  - **Tests:** Go filter table:
- '1080p BluRay FLAC 2.0 x264' name → rejected
- 30 MB FLAC for a 45-min listing → rejected
- 300 MB FLAC → accepted
- categories [2040] → rejected
- [3040] → accepted
- [] → accepted
- [100123] → accepted
- a listing with no durations uses track count × 4 min
  - **Depends on:** [MUS-03](#mus-03) (decided Rebuild), INT-xx (integrations-10 Torznab caps and audio categories); related, not blocking
  - **Risk:** Unusually long or hi-res albums could fall outside the window, so the bounds are generous and the reason is visible in interactive search.
  - **Resolves:** music-15

#### Milestone: M7: Feel (optional)

_Music looks like the rest of Arrmada: a cover-art grid, artist shelves, hero images and bios, and music on the Dashboard and Calendar._

<a id="mus-22"></a>
- [ ] **MUS-22 · Artwork-first music library: cover grid, album shelves on the artist page, sorting and a type filter** — `P3` · `M` · Phase 15
  - **Problem:** Artist cards are text plus a progress bar (web/src/pages/Music.tsx:309-346). Album rows on the artist page are plain text (ArtistDetail.tsx:192-198), even though every album stores a Cover Art Archive URL (metadata/musicbrainz.go:204). The only artwork is a 160 px cover on the album page. ListArtists is fixed newest-first (internal/music/repo.go:39) with no sort control. Hundreds of artists are hard to browse, and the pages feel nothing like Movies and Series.
  - **Approach:** 1. Backend:
       - ListArtists(ctx, sort) accepts name|added|completeness; name orders by COALESCE(NULLIF(sort_name,''), name) COLLATE NOCASE.
       - Each artist row returns cover_url from its newest album with a cover, via one correlated subquery.
       - GET /api/v1/music/artists?sort=.
    2. Music.tsx: a grid of square tiles with:
       - the cover (front-250 derived from the stored front-500 URL), falling back to an initials tile
       - the name, '3/20 albums' and a thin completeness bar (stats from [MUS-10](#mus-10))
       - a sort select (remembered in localStorage, wrapped in try/catch) and a type filter
       It keeps the warm dark palette, terracotta accent and current type scale, and has no horizontal scroll at 375 px.
    3. ArtistDetail.tsx:
       - shelves for Albums, EPs, Singles, and Live & other (by album_type)
       - each shelf is a row of cover tiles: cover, title, year, completeness ring, monitor toggle on hover or focus
       - a list-view toggle for long discographies
    4. AlbumDetail: a 240 px cover. All thumbnails use loading="lazy" and an onError fallback.
  - **Files:** `internal/music/repo.go`, `internal/music/service.go`, `internal/httpapi/music.go`, `web/src/pages/Music.tsx`, `web/src/pages/ArtistDetail.tsx`, `web/src/pages/AlbumDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - The music library reads like the poster grids in Movies and Series.
    - Alphabetical sort works across hundreds of artists.
    - At phone width there is no horizontal scroll.
    - Broken covers fall back to initials.
  - **Tests:** Go (repo): each sort order, and cover_url comes from the newest album that has one.; UI (manual): desktop and 375 px width; a cover 404 shows the initials fallback; shelves group by album_type.
  - **Depends on:** [MUS-10](#mus-10), FE-xx (shared card/grid components); soft
  - **Risk:** Cover Art Archive has gaps, which the fallback covers. Large libraries load many images, so thumbnails are lazy-loaded. Covers are hotlinked from coverartarchive.org, the same pattern as TMDB posters.
  - **Resolves:** music-13
<a id="mus-23"></a>
- [ ] **MUS-23 · Artist images and bios from fanart.tv or TheAudioDB (keys entered in the app), with a keyless Wikipedia fallback** — `P3` · `M` · Phase 15
  - **Problem:** Artist.ImageURL and Overview are never set. AddArtist omits ImageURL, Refresh writes back the existing empty value (internal/music/service.go:84-87, 152-153), and the MusicBrainz GetArtist mapping has no bio (metadata/musicbrainz.go:163-166). Nothing renders them anyway.
  - **Approach:** 1. metadata: an ArtistArt provider interface with two implementations:
       - fanart.tv /v3/music/{mbid}: artistthumb, artistbackground
       - TheAudioDB artist-mb.php?i={mbid}: strArtistThumb, strArtistFanart, strBiographyEN
       Keys:
       - Entered in the app UI only (the Connections / metadata-keys card INT provides, with a Test button), stored in settings.
       - Never committed, never logged (redact them in request logging), and never returned by the API (GET returns {configured: true}).
    2. Bio fallback with no key: MusicBrainz url-rels (inc=url-rels) → Wikidata → Wikipedia REST summary.
    3. Cache every response in metadata_cache (migration 0080) for 30 days.
    4. Migration (next free number, tentatively 00NN_artist_banner.sql): artists.banner_url TEXT NOT NULL DEFAULT ''.
    5. AddArtist and Refresh write image_url, banner_url and overview (repo.UpdateArtistMeta gains banner_url). Refresh no longer passes back the old a.ImageURL.
    6. ArtistDetail: a hero banner that fades into the page background via the existing CSS tokens, and a bio clamped to 4 lines with 'more'. [MUS-22](#mus-22)'s grid tiles prefer the artist image over the album cover.
  - **Files:** `internal/metadata/musicbrainz.go`, `internal/metadata/provider.go`, `internal/metadata/artistart.go`, `internal/music/service.go`, `internal/music/repo.go`, `internal/store/migrations/00NN_artist_banner.sql`, `internal/httpapi/settings.go`, `web/src/pages/ArtistDetail.tsx`, `web/src/pages/Music.tsx`, `web/src/pages/Settings.tsx`
  - **Acceptance:**
    - Artists with fanart get a hero image and a bio.
    - With no key configured, artists that have a Wikipedia link still get a bio.
    - Keys never appear in logs or API responses.
  - **Tests:** Go: provider parse tests with canned JSON for fanart.tv, TheAudioDB and the Wikipedia summary.; Go: Refresh writes image_url, banner_url and overview.; Go: with no key set, no fanart.tv or TheAudioDB calls are made and the Wikipedia fallback is used.; Go (httpapi): the settings GET masks the keys.
  - **Depends on:** [MUS-22](#mus-22), INT-xx (Connections / metadata-key card pattern), SEC-xx (secret masking and log redaction); soft
  - **Risk:** Third-party rate limits and terms of use; caching for 30 days keeps calls low. Hotlinked images can disappear, so the initials and cover fallbacks stay.
  - **Resolves:** music-13
<a id="mus-24"></a>
- [ ] **MUS-24 · Show Music on the Dashboard and Calendar** — `P3` · `S` · Phase 15
  - **Problem:** From the audit's Phase 3 overhaul (not a single finding). Music is invisible outside its own pages. The dashboard computes artist and album counts (internal/httpapi/dashboard.go:270-271) but never renders them, and the Calendar has no upcoming album releases.
  - **Approach:** 1. dashboard.go:
       - add MissingAlbums: monitored albums of monitored artists that are not complete, one grouped query
       - include the music counts only when musicEnabled
       - Dashboard.tsx shows Artists / Albums / Missing tiles, linking Missing to Music → Wanted ([MUS-18](#mus-18))
    2. calendar.go: when musicEnabled, add monitored albums whose release_date falls in the window as Type 'album' entries (Title = album, Subtitle = artist) linking to /music/album/:id. Calendar.tsx gets the entry style.
    3. recentActivity: include artist_events (grabbed / imported / failed).
    4. Coordinate rather than duplicate: the music.imported notification is OBS's insights-6, and Downloads labelling is ACQ's ops-10.
  - **Files:** `internal/httpapi/dashboard.go`, `internal/httpapi/calendar.go`, `internal/httpapi/dashboard_test.go`, `web/src/pages/Dashboard.tsx`, `web/src/pages/Calendar.tsx`
  - **Acceptance:**
    - With Music on, the dashboard shows music counts and recent music activity; with it off, they are hidden.
    - Upcoming monitored album releases appear on the Calendar when Music is on.
  - **Tests:** Go: dashboard_test asserts the artist, album and missing-album counts, and their absence when the module is off.; Go: the calendar handler returns an upcoming album entry.; UI (manual): tiles are hidden when the module is off.
  - **Depends on:** [MUS-01](#mus-01), [MUS-13](#mus-13), [MUS-18](#mus-18) (Wanted link); soft, OBS-xx (insights-6 music notification); related, ACQ-xx (ops-10 labels); related
  - **Risk:** Low.
  - **Resolves:** 

#### Risks

- The Settings Save button is broken until CFG fixes it, so an owner who keeps Music on through the keep-on rule can't switch it off from the UI. Ship MUS-01 with the CFG fix, or right after it.
- The keep-on rule (artists exist → stay on) means the owner's install may keep Music visible after upgrading. The startup log line and the Preview pill make that visible; the owner switches it off by hand if they want it hidden.
- Until MUS-02 ships, every incomplete album is searched every 30 minutes on the shared indexers. MUS-01 and MUS-02 should deploy together to remove the tracker-ban risk for Movies and Series.
- The rebuild is roughly 6-8 weeks of sessions competing with higher-value epics. MUS-03's gate (with the Park option) exists to stop sunk-cost drift; don't start M3 without a recorded Rebuild decision.
- Migration-number collisions: other epics also add migrations from 0090. Take the next free number at commit time and never renumber a shipped migration.
- Looser matching (MUS-04) and manual-grab imports (MUS-17) could place the wrong audio. Mitigations: whole-word matching, the artist check, MatchTracks never guessing positions, and the review queue staying in place.
- File safety: MUS-08, MUS-16 and MUS-19 replace or relist tracks. Never hard-delete; recycle only. Tests use temp dirs and byte blobs, and manual trials must never touch the owner's real music library.
- MusicBrainz allows 1 request per second on one shared client. Refresh (MUS-13), listing fills, edition lookups (MUS-19) and the scan all queue behind it, so every job is capped per run.
- ffprobe on every imported track (MUS-11) adds a few seconds per album, and VBR tier inference is approximate. BetterTier must stay strict so upgrades don't churn.
- Third-party artwork and bio sources (MUS-23) have rate limits and terms of use. Keys are entered only in the app, masked in API responses and kept out of logs.
- Decision evidence is incomplete: Insights only sees Plex plays, so the owner must also be asked directly.

#### Out of scope

- music-11, the Reviews 'Import into a different…' music album picker and the server-side type check. It is owned by the ACQ/Reviews quick win; MUS only depends on it.
- A Music tab in Discover or music requests for requesters. Revisit with REQ after M6, and only if the household asks.
- Making the Books toggle stop its jobs and routes. The gap is the same, and MUS-01's gate is built so BOOK/CFG can reuse it, but Books is not changed here.
- Asking Plex to scan its music library after imports, or linking albums to Plex/Plexamp (PLEX epic).
- Writing tags, retagging files, ReplayGain, lyrics, AcoustID fingerprinting or other beets-style processing.
- Caps-aware Torznab audio categories in outgoing searches (INT integrations-10).
- Music notifications and attention-feed entries (OBS insights-6).
- Playing music inside Arrmada. Plex/Plexamp is the player.
- Dropping music tables or deleting files on a Cut decision. Data is always kept.

