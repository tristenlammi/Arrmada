# SER — Series

_Part of the [Arrmada roadmap](../../ROADMAP.md). 22 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Make Series safe to click and safe to leave running. Every manual button goes through one scope-aware grab policy that cannot overwrite a library, renumbering and renames never move a file without a collision-safe plan the owner can see, show metadata and monitoring follow what TMDB and the owner actually say, and releases are matched to the right show under every name. On top of that foundation, the series detail page becomes Plex/Overseerr-like: a hero with the next episode and stats, a season rail with episode cards, one menu per episode, and Files/Activity/Numbering tabs that show the server's real search outcomes.

**Why.** The automatic Series sweep is battle-hardened, but the parts the owner touches directly are not safe.
- **Quick buttons can overwrite the library (series-1, series-2).** The season Grab, the episode Grab and Replace go through GrabBestForScope. It ignores the pack-tier and proportionality rules and flags the grab as manual, so the import skips the quality gate for every file in the release. One click on an airing show can download a complete-series box set and silently replace every season. The recycle bin makes this recoverable, but the download is huge and the downgrade is silent. On Specials the same buttons become a whole-show grab.
- **Renumbering can rename files onto the wrong episodes (series-4).**
  - Every show gets counted absolute numbers.
  - The episode source silently falls back from TVDB to TVmaze to TMDB on any error.
  - A one-episode count change triggers a rebuild, then a rename that is a bare os.Rename able to replace an existing file.
  - The Rename button also moves files without showing the preview it just fetched (series-12).
- **Show data goes stale (series-3).** Refresh never updates status, title or poster. An ended show is never treated as ended, and a revived show is never refreshed again.
- **Monitoring ignores the owner's choices (series-6, series-10).**
  - Pausing and resuming a show re-monitors seasons the owner excluded.
  - There are no add-time presets, and new seasons inherit the series flag.
  - Progress, Missing and Partial count unmonitored seasons, so a show where only the latest season is wanted reads '12/180 · PARTIAL' forever.
- **Matching picks the wrong show or misses the right one (series-7, series-8).**
  - Year and country are ignored, so Doctor Who (1963) and (2005) cross-grab and 'The.Office.US' never matches.
  - Imports route by title only, so alias-grabbed releases land in review.
  - The 'romaji' title is really TMDB's Japanese-script original_name, and it is never searched.
- **The page is an admin console (series-12, series-13, series-9).**
  - It has 9 toolbar buttons, every season collapsed, and episode stills, overviews and season posters fetched but never shown.
  - It polls the full detail every 3 s and re-parses the queue for each missing episode, and History never refreshes.
  - Search outcomes only reach the logs, so the UI fakes a 'Requested' state in localStorage.
- **Upgrades miss what matters for finished shows (series-14).** They see one 100-result page and only single-episode releases, so BluRay season packs and absolute-numbered anime are never upgrades.

**Depends on:** OBS — persisted per-scope search attempts (draft series.t14, ops-15/product-10). [SER-19](#ser-19) needs it to show server-side search state, and [SER-20](#ser-20)'s Activity tab needs it. [SER-02](#ser-02) and [SER-22](#ser-22) publish GrabOutcome on the series.searched bus topic for OBS to store.; QUAL — TV release candidates tagged with runtime so the profile's bitrate window applies (draft series.t13, series-5/quality-1). [SER-22](#ser-22)'s season-pack upgrade decision needs it; [SER-02](#ser-02)'s planner must call the same tagging helper once it lands.; FE — web/src/ui component kit (Menu, Modal, Tabs; frontend-2) and useLiveQuery (frontend-14). Preferred by [SER-14](#ser-14) to [SER-20](#ser-20). [SER-14](#ser-14) builds a local Menu only if the kit has not landed.; ACQ — acquisitions core keyed by info hash. Optional: when it lands, [SER-01](#ser-01)'s grabs.scope/manual and [SER-11](#ser-11)'s route-by-grab-hash lookup move onto it.

#### Design

## Target design

### 1. One grab policy for every path ([SER-01](#ser-01), [SER-02](#ser-02), [SER-03](#ser-03), [SER-16](#ser-16), [SER-21](#ser-21))

**Scope type.** `automation.GrabScope{Season, Episode int}` is the single scope type everywhere.
- Whole show is Season -1, season N is {N,0}, and one episode is {N,E}. Season 0 is Specials, a real scope after [SER-03](#ser-03).
- The string form is `""`, `"S03"`, `"S03E04"`, `"S00E05"`. It is stored in a new `grabs.scope` column. The existing `grabs.manual` column stays as the "user's say-so" flag.
- The import gate is skipped for a ref only when `manual=1 AND scope.Covers(ref)`. Legacy rows (manual=1, scope='') keep today's whole-release behaviour, so no backfill is needed.

| Path | Who selects | grabs.manual | grabs.scope | Import gate |
|---|---|---|---|---|
| Missing sweep / RSS / stall re-search | `planSeriesGrabs` (unscoped, passes 1-4 unchanged) | 0 | '' | applies |
| Season **Grab missing** | `GrabForScope` → planner (scoped, pack fallback) | 0 | S03 | applies |
| Episode **Grab** / **Search automatically** | `GrabForScope` (never a pack) | 0 | S03E04 | applies |
| **Replace…** | blocklist source release + `GrabForScope{Replace}` | 1 | S03E04 | skipped for S03E04 only |
| **Choose release…** from an episode, season or show modal | the user's pick | 1 | S03E04 / S03 / '' | skipped only inside that scope |
| Upload torrent, manual folder import, approved review | the user's file | 1 | '' | skipped |
| Upgrade sweep | `UpgradeCandidate` / pack rule ([SER-22](#ser-22)) | 0 | '' | applies |

**Scoped planner rules.**
- Wanted = aired, file-less episodes in scope.
  - A season click waives the season flag but requires the episode flag.
  - A single-episode click waives monitoring.
  - Replace includes the episode even though it has a file.
- Complete and multi-season packs need an ended show plus `packIsProportionate`. Running shows only take single-season packs.
- An episode scope never takes a pack. A season scope takes a pack in pass 2 when it is worth it (at least half the season missing), or in pass 4 as a last resort when no single episodes exist.
- Selection uses `effectiveProfile`, including in the sweep, which today passes the raw profile name.

**GrabOutcome.** Every user-triggered search returns
`GrabOutcome{Scope, Found, WrongShow, OutOfScope, Blocklisted, Pending, Rejected map[reason]int, Eligible, Grabbed []string, IndexerErrors, Note}`.
- It is written as a `searched` series event and published as the `series.searched` bus topic `{id, scope, outcome}`.
- OBS persists it per scope, and [SER-19](#ser-19) renders it.

### 2. Numbering and renames you can trust ([SER-04](#ser-04), [SER-05](#ser-05), [SER-07](#ser-07))

- `series.numbering_source` holds 'tvdb', 'tvmaze', 'tmdb' or '' (legacy). It is set from `SeriesDetails.NumberingSource` / `NumberingFallback` (both `json:"-"`), which `seriesWithEpisodeSource.GetSeries` fills.
- The signature becomes `Refresh(ctx, id, RefreshOptions{AllowRebuild}) (Series, RefreshResult{Renumbered, Remaps, Fallback, Pending}, error)`. Only the manual Refresh button sets AllowRebuild.

| Situation | Episode rows | Files on disk | numbering_source |
|---|---|---|---|
| Preferred source errored (fallback) | INSERT OR IGNORE new (season, episode) only; no metadata overwrite, prune, rebuild or absolute reassign | never move | unchanged |
| Same model | InsertSeasons + `ReassignAbsolutes` (by season/episode) + prune | never move | stored |
| Model changed, manual, and (stored = fresh = tvdb, or anime with '' → tvdb) | `RebuildEpisodes` | collision-safe `SeriesRename` | stored |
| Model changed otherwise | additive only; store a pending proposal ([SER-07](#ser-07)) | none until Apply | unchanged |

- Standard shows never rebuild by absolute number. Their identity is (season, episode).
- `numberingModelChanged` counts a season-set difference always, and absolute disagreement only when both sides are authoritative.
- **Renames.** `library.Importer.Move` refuses an existing target (`ErrTargetExists`). For a hardlink to the same inode it removes the source name instead. `SeriesRename` plans the moves, then applies them:
  1. Group rows by file, so a multi-episode file is moved once.
  2. Move chained or cyclic moves through temporary names.
  3. Skip and report foreign targets.
  4. Update the DB only for files that actually moved.
  5. Return `{moved, skipped[]}`.
- The UI always shows the preview and confirms. POST /rename can carry the previewed items, and anything that changed since the preview is skipped.
- **Proposal.** A `series_numbering_pending` row holds a deterministic plan hash and the remaps.
  - Apply re-fetches, re-plans and returns 409 on a hash mismatch.
  - Dismiss remembers the hash.
  - `RebuildEpisodes` keeps season monitored flags.

### 3. The show row follows TMDB ([SER-06](#ser-06))

- Refresh writes title, year, overview, poster, status, network and the extra blob through `UpdateSeriesMetadata`, which never blanks a field with an empty one.
- An old title becomes a title-only alias. Status and title changes are logged as series events.
- `last_refreshed_at` is stored. Ended or canceled monitored shows are re-checked weekly so revivals are noticed.

### 4. Monitoring model ([SER-08](#ser-08), [SER-09](#ser-09), [SER-10](#ser-10))

- `series.monitored` is a gate. It never rewrites season or episode flags. Enabling a show with nothing monitored applies 'all', which keeps the fix the cascade was added for.
- A season's flag is derived as "any monitored episode in it". `series.monitor_new_seasons` decides whether a season that appears on refresh is monitored. A new episode in an existing season inherits that season's flag. Specials are never auto-monitored.
- Presets are applied in one transaction:

| Preset | Monitors | monitor_new_seasons |
|---|---|---|
| all | every regular episode | 1 |
| future | episodes not yet aired (or without a date) | 1 |
| missing | episodes without a file | 1 |
| existing | episodes with a file, plus unaired ones | 0 |
| first_season | season 1 | 0 |
| latest_season | the highest season with episodes | 1 |
| none | nothing | 0 |

- **Stats** count only what is monitored:
  - `episodes` = has a file, or (monitored, in a monitored season, and aired);
  - `missing` = monitored, aired and file-less;
  - `unmonitored_missing` is reported separately;
  - `next_air_date` is the earliest future monitored episode.
- `Service.Get` returns the stats as well.

### 5. Identity and aliases ([SER-11](#ser-11), [SER-12](#ser-12), [SER-13](#ser-13))

- `seriesIdentity(p, s)` requires all three:
  - the title key matches, or a romaji or alias rule matches;
  - the year is compatible: the parser's new `TitleYear`, read only before the SxxExx marker, must be within ±1 of the series year;
  - the country is compatible: `parser.SplitCountry` against TMDB `origin_country`, with UK ≡ GB.
- `series.Service.MatchRelease` replaces `MatchByTitle` for import routing. An ambiguous match without a year goes to review instead of to the newest show.
- A download grabbed for series X imports into X when `seriesTitleMatches` holds.
- TMDB `alternative_titles` seed `series_aliases` rows with `source='tmdb'`:
  - Latin-script romaji/JP titles and US/GB variants, at most 5;
  - exact-key matching only;
  - a collision guard against other library titles;
  - a `disabled` flag that keeps a removed row from returning.
- The alias panel shows on every series.
- Anime absolute-number queries go through `indexerQuery`, use the zero-padded number, and include the romaji alias.

### 6. Series pages ([SER-14](#ser-14) to [SER-20](#ser-20))

```
[backdrop at about 0.45 opacity, gradient to var(--bg)]
[poster]  Title (2019)   [Continuing] [HBO]
          Next: S03E05 · Thu 9 Oct    52/60 monitored · 210 GB
          [Monitor v] [Profile v] [Search missing] [Choose release...] [...]
Tabs:  Episodes | Files | Activity | Numbering (anime, pending proposal, or unplaced files)
Episodes: season rail (posters + progress) -> episode cards
          (16:9 still, 'E05 · Title', air date · runtime, 2-line overview,
           status chip, fit chip, progress bar, ... menu); select mode for bulk
```

- The overflow menu holds: Refresh & rescan, Rename… (preview modal), Manual import, Upload torrent, Numbering: Standard/Anime, and Delete….
- The page is split into `web/src/pages/series/` (Hero, SeasonRail, EpisodeCard, FilesTab, ActivityTab, NumberingTab, RenameModal).
- It polls `GET /series/{id}/downloads` while something downloads, and reloads on `series.imported` / `series.searched` / `release.grabbed`.
- Panels key on `last_event_id`.
- The library grid gets:
  - a persisted sort (Title, Recently added, Next airing, Size, Missing);
  - 'network · Next Thu' on cards;
  - an always-visible menu on touch and keyboard focus.
- The visual style stays as it is: dark warm palette, terracotta accent, the current type scale.

### 7. API surface (new or changed)

- `POST /series/{id}/grab` gains optional `season` and `episode` (the modal's scope).
- `POST /series/{id}/autograb` uses `GrabForScope`. `{season:0, episode:0}` returns 400.
- `POST /series/{id}/search` keeps its no-body behaviour (the whole-show sweep). With `{episodes:[{season,episode}]}` it runs a scoped grab.
- `GET /series/{id}/releases`: no season param means the whole show; `season=0` means Specials.
- `GET/POST /series/{id}/rename`: the preview reports conflicts. POST takes `{items}` and returns `{renamed, skipped}`.
- `GET /series/{id}/numbering`, `POST .../numbering/apply {plan_hash}`, `DELETE .../numbering/pending`.
- `PUT /series/{id}/monitor {monitored, preset?, monitor_new_seasons?}`. `POST /series` takes `{monitor, monitor_new_seasons}`. A new setting, `series_monitor_default`, is added.
- `PUT /series/{id}/episodes/monitor {episode_ids, monitored}`.
- `GET /series/{id}/downloads`. The detail response adds `stats`, `numbering_source`, `monitor_new_seasons`, `last_event_id` and (from OBS) `last_search`.
- `GET /reviews?media_type=series&expected_id=`.

### 8. Data model

Each task takes the next free migration number at implementation time. Applied migrations are never edited.
- `grabs.scope`
- `series.numbering_source`
- `series.last_refreshed_at`
- `series_numbering_pending`
- `series.monitor_new_seasons` (backfilled from `monitored`)
- `series_aliases.source` + `disabled`
- `series.search_upgrade_cursor`

The series package tests move from the hand-rolled schema in refreshmeta_test.go to `store.Open(t.TempDir())`, so new columns don't break them.

### 9. Contracts with other epics

- **OBS** stores `series.searched` outcomes per (media, scope), and `handleGetSeries` attaches `last_search: {scope: {started_at, finished_at, found, eligible, grabbed, reasons, example}}`.
- **QUAL** provides the TV runtime-tagging helper. The planner and the [SER-22](#ser-22) pack-upgrade rule call it.
- **FE** provides Menu, Modal and Tabs. [SER-14](#ser-14) builds a local Menu only if the kit is not there yet.
- **REQ**'s season picker calls `Service.Add` with a preset and `ApplyMonitorPreset` from [SER-09](#ser-09).
- **ACQ**, when it lands, can own `scope` and `manual` on its acquisitions row. [SER-11](#ser-11)'s routing by grab hash becomes its lookup.

### 10. Working rules

- Run race tests in Docker before every push, and end commits with the Co-Authored-By trailer.
- Rename, renumber and import tests use `t.TempDir()` fixtures only. They never touch, convert or rename the owner's real library.
- Audiobook privacy and the adult-content filter are untouched.
- No credentials are hardcoded. The TVDB and TMDB keys stay in the Settings UI.

#### Milestone: M1 — Safe buttons, safe renames, no surprise renumbering

_The quick Grab, Replace, Specials and Choose-release paths can no longer pull a box set and overwrite the library. A rename can never destroy a file, and it always shows its preview first. A metadata-source outage or a shifted counted number can no longer move or rename files._

<a id="ser-01"></a>
- [x] **SER-01 · Quick Grab and Replace stop bypassing the import gate: record each grab's scope and force only inside it** — `P0` · `M` · Phase 0
  - **Problem:** GrabBestForScope (internal/automation/series_interactive.go:329-361) grabs through GrabForSeries, which passes manual=true (lines 300-322). markGrabManual (store.go:217) flags the grab, and ImportSeriesDownloads then passes grabWasManual(hash) as `force` into importSeriesInto (series.go:1056). force skips wantsEpisodeFile for every ref of every file in the release (reviews.go:555-556). So a season Grab or an episode Replace that lands a complete-series pack replaces every episode of every season with no quality comparison.

Interactive picks have the same blanket force: a complete pack chosen from the Season 3 modal overwrites seasons 1-10 too. Replaced files go to the recycle bin, so this is recoverable, but it is a silent library-wide downgrade.
  - **Approach:** 1. **Migration.** Add `internal/store/migrations/NNNN_grab_scope.sql` (next free number; 0090 if nothing else has landed): `ALTER TABLE grabs ADD COLUMN scope TEXT NOT NULL DEFAULT ''`.
       - `grabs.manual` stays as the "user's say-so" flag.
       - No backfill. Legacy manual=1 rows have scope '' (whole release), which is exactly today's behaviour.
    2. **New `internal/automation/grabscope.go`:**
       - `type GrabScope struct{ Season, Episode int }`, with `WholeShow = GrabScope{Season: -1}`.
       - `String()` returns "", "S03", "S03E04" or "S00E05". `parseGrabScope(string)` is its inverse.
       - `Covers(ref series.EpisodeRef) bool`: whole show covers everything, a season covers ref.Season, an episode covers one ref.
       - `type forceRule struct{ On bool; Scope GrabScope }` and `forceAll = forceRule{true, WholeShow}`.
       - A pure `refsToPlace(refs []series.EpisodeRef, force forceRule, wants func(series.EpisodeRef) bool) (place, forced []series.EpisodeRef)`.
    3. **store.go.**
       - Replace the body of markGrabManual with `markGrab(ctx, hash string, manual bool, scope GrabScope)`: `UPDATE grabs SET manual=?, scope=?` on the latest row for the hash.
       - Keep `markGrabManual(hash)` as `markGrab(hash, true, WholeShow)` for the movie and book callers (coordinator.go:233, 253).
       - Replace grabWasManual with `grabForce(ctx, hash) forceRule`, which reads manual and scope from the latest row.
    4. **reviews.go importSeriesInto.** Change the signature to take `force forceRule` instead of `force bool`. The per-ref loop at lines 554-559 becomes `refsToPlace(refs, force, func(ref) bool { return c.wantsEpisodeFile(...) })`. The 'replacing on the user's say-so' log names only the forced refs. Callers:
       - ImportSeriesDownloads uses `c.grabForce(ctx, it.Hash)`.
       - ManualImportSeries folder import (series_interactive.go:495) and the approved-review import (reviews.go:333) use forceAll.
    5. **Grab entry points.**
       - `grabForSeries(ctx, id, indexer, url, title, manual bool, scope GrabScope)` records both.
       - `GrabForSeries` (interactive pick) gains a scope argument and uses manual=true.
       - `GrabForSeriesAuto` uses manual=false with WholeShow.
       - GrabSeriesTorrent keeps markGrabManual (whole show).
    6. **Interim, until [SER-02](#ser-02) deletes it.**
       - `GrabBestForScope(ctx, id, season, episode, manual bool)` grabs with manual=false and scope Sxx or SxxEyy from the quick buttons, so the gate applies.
       - RegrabEpisode passes manual=true with scope SxxEyy, because Replace means 'replace this episode'.
    7. **HTTP and UI wiring.**
       - handleGrabSeries: the body gains optional `season *int` and `episode *int`; absent means WholeShow. api.ts `grabSeries` adds `season?` and `episode?`.
       - SeasonBlock and EpisodeRow's ReleaseSearchModal onGrab pass their scope, as does the SeriesSearchModal season tab. The Toolbar's whole-show modal passes none.
    8. **Quick UI fixes in SeriesDetail.tsx.**
       - Replace asks first: `window.confirm('Blocklist the current release of S03E04 and download a different one? Only this episode's file is replaced.')`.
       - The season Grab is hidden when no aired, monitored episode in the season is missing.
  - **Files:** `internal/store/migrations/NNNN_grab_scope.sql`, `internal/automation/grabscope.go`, `internal/automation/store.go`, `internal/automation/reviews.go`, `internal/automation/series.go`, `internal/automation/series_interactive.go`, `internal/automation/coordinator.go`, `internal/httpapi/series.go`, `web/src/lib/api.ts`, `web/src/pages/SeriesDetail.tsx`, `web/src/components/SeriesSearchModal.tsx`, `internal/automation/manualgrab_test.go`
  - **Acceptance:**
    - Importing a pack grabbed by a quick Grab leaves episodes whose current file scores equal or better untouched, with the log line 'skipping file — its episodes already have equal-or-better files'
    - A complete-series pack picked from the Season 3 'Choose release' modal replaces only season 3 episodes regardless of score; every other season goes through the quality gate
    - Replace on S03E04 replaces that episode even with a lower-scoring release and gates every other episode the release contains
    - Uploaded torrents, manual folder imports and approved reviews still force-import everything
    - The grabs rows hold manual/scope as follows: quick season Grab 0/'S03'; episode Grab 0/'S03E04'; Replace 1/'S03E04'; season-modal pick 1/'S03'; whole-show modal pick 1/''; sweep 0/''
    - Grabs flagged manual=1 before the migration import exactly as before
    - Replace asks for confirmation; the season Grab is not shown on a season with no missing monitored aired episodes
  - **Tests:** Go: TestGrabScopeCovers (table: whole show, S03, S03E04, S00E05 against refs in and out of scope); Go: TestGrabScopeStringRoundTrip; Go: TestRefsToPlaceHonoursForceScope (force off → only wanted refs; force S01 → S01 refs forced, S02 gated; force all → every ref); Go: extend manualgrab_test.go with TestMarkGrabRecordsScope and TestLegacyManualGrabForcesAll (store.Open on t.TempDir); Go: existing manualgrab, sourcerelease and inheritquality tests still pass; UI check: Replace confirm dialog; a complete season shows no Grab button
  - **Risk:** A wrong Covers() either re-opens the overwrite (too permissive) or blocks imports the user chose (too strict), so test both directions. The importSeriesInto signature change touches three callers. Movie and book callers of markGrabManual must keep their behaviour; that is why the wrapper stays.
  - **Resolves:** series-1
<a id="ser-02"></a>
- [x] **SER-02 · One scope-aware grab planner behind Grab missing, episode Grab and Replace (GrabForScope)** — `P0` · `M` · Phase 1
  - **Problem:** Even with SER-01's gate in place, GrabBestForScope (series_interactive.go:329-361) takes the first eligible release from the interactive ranking:
- For a season it accepts any non-episode release. parser CoversSeason is true for every Complete pack, so a box set qualifies on an airing show.
- It never applies isPackTier, packIsProportionate or packIsWorthIt.
- Ties go to the bigger file (quality.go:528-533).
- With no pack, it falls back to any episode in the season, missing or not.
The result is huge unwanted downloads. RegrabEpisode (coordinator.go:1076-1103) uses the same path. The missing sweep (grabSeriesLimited) passes the raw `s.QualityProfile` to Decide (series.go:403), while the interactive and upgrade paths use effectiveProfile. Search outcomes are only logged.
  - **Approach:** 1. **Extract the planner.** In new `internal/automation/series_plan.go`, extract `planSeriesGrabs(in planInput) (remaining []epKey)` from grabSeriesLimited (series.go:494-597).
       - `planInput` holds: eligible []quality.Evaluation, wanted []epKey, seriesSeasons map[int]bool, counts map[int]int, ended bool, opts planOpts{Scoped, AllowPackFallback bool}, `cover func(parser.Release, map[epKey]bool) []epKey` and `try func(name, label string) bool`.
       - grabSeriesLimited keeps its filtering, logging and `grab` closure and calls the planner with Scoped=false. Passes 1-4 stay identical.
       - In scoped mode, pass 4 (oversized packs) runs only when AllowPackFallback is set, and only accepts packs where `packIsProportionate(neededSeasons, packSeasonsOf(r, seriesSeasons))` holds.
    2. **Effective profile in the sweep.** grabSeriesLimited's Decide uses `c.effectiveProfile(ctx, s.QualityProfile, "series")`.
    3. **Shared query building.** Factor the query-building half of RankSeriesReleasesWith (lines 37-104) into `searchSeriesScope(ctx, s, season, episode int) ([]indexer.Release, indexerErrors int)`. It covers: tvsearch season/episode params for non-anime, the whole-show per-season fan-out, AliasSearchTerms, and alias titles. RankSeriesReleasesWith calls it, with no behaviour change.
    4. **`func (c *Coordinator) GrabForScope(ctx, seriesID int64, sc SeriesScope) (GrabOutcome, error)`.** `SeriesScope{Season, Episode int; Episodes []series.EpisodeRef; Replace bool; Trigger string}`.
       - Season < 1 returns `ErrSpecialsScope` until [SER-03](#ser-03) lands.
       - Wanted for a season click: aired, no file, episode monitored (the season flag is waived because the click names the season).
       - Wanted for an episode click: that episode if aired and file-less (monitoring waived). Replace: that episode even though it has a file. Episodes list: each listed episode (used by [SER-16](#ser-16)).
       - Nothing wanted returns early: `GrabOutcome{Note: "Nothing missing in Season 3"}`.
       - It searches with searchSeriesScope, then filters out: seriesTitleMatches failures, releaseMatchesScope failures, blockedSetSeries hits and pendingSeriesGrabTitles hits. Each filter is counted into the outcome.
       - It decides with the effective profile and runs the planner with Scoped=true and AllowPackFallback = (season scope && !Replace && Episodes==nil).
       - It grabs through `grabForSeries(manual=sc.Replace, scope)` from [SER-01](#ser-01).
       - Result: an episode scope never takes a pack, because packIsWorthIt fails for one episode and there is no fallback. When the only eligible candidates were packs, the outcome note says 'Only season packs are available — use Choose release'.
    5. **GrabOutcome.** `GrabOutcome{Scope string; Found, WrongShow, OutOfScope, Blocklisted, Pending, Eligible, IndexerErrors int; Rejected map[string]int (keyed by the profile's RejectReason first clause); Example map[string]string; Grabbed []string; Note string}` plus `Summary()`, e.g. "37 found · 0 fit: 22 other shows, 15 over the size ceiling". It reports 'indexers failed' instead of 'nothing found' when every indexer errored.
    6. **Wiring.**
       - Delete GrabBestForScope.
       - RegrabEpisode keeps blocklisting the source release, then calls `GrabForScope{Season, Episode, Replace: true, Trigger: "replace"}`.
       - handleAutoGrabSeries validates synchronously (season < 1 returns 400 with the ErrSpecialsScope text), then runs `a.bg(...)`. On completion it adds a series event 'searched' with `outcome.Summary()` and publishes the bus topic `series.searched` `{id, scope, outcome}`. handleRegrabEpisode does the same. OBS persists the outcome later, and [SER-19](#ser-19) shows it.
       - The automatic sweep does not emit these events.
    7. **UI (SeriesDetail.tsx).**
       - The season button is relabelled 'Grab missing', with the title 'Grabs Season N's missing episodes — a season pack when most of the season is missing or no single episodes exist'.
       - It is hidden on Specials until [SER-03](#ser-03).
       - The episode Grab shows only on aired, file-less episodes.
       - The toast after a click says 'Searching…' ([SER-19](#ser-19) replaces it with server state).
    8. **QUAL coordination.** If QUAL's runtime-tagging helper for TV candidates has landed, GrabForScope must tag candidates exactly as grabSeriesLimited does.
  - **Files:** `internal/automation/series_plan.go`, `internal/automation/series.go`, `internal/automation/series_interactive.go`, `internal/automation/coordinator.go`, `internal/httpapi/series.go`, `web/src/pages/SeriesDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - On a running show, the season 'Grab missing' never grabs a multi-season or complete-series release, even when one outranks the season pack
    - On a partly filled season it only fetches releases covering missing episodes, and takes the season pack only when at least half the season is missing or no single-episode release exists
    - Episode Grab and Replace only ever grab single- or multi-episode releases covering that episode; with only packs available they grab nothing, and the 'searched' event says why
    - The season 'Grab missing' is not shown on a season with no missing monitored aired episodes
    - Every click produces a 'searched' series event with counts and reasons, and a series.searched bus message
    - The automatic missing sweep grabs exactly what it did before for the same inputs (existing pack tests unchanged)
  - **Tests:** Go: TestPlanScopedSeasonRunningShowRejectsCompletePack (planner with stub cover/try; no indexer or client); Go: TestPlanScopedSeasonPackOnlyWhenMostMissing and TestPlanScopedSeasonPackFallbackWhenNoEpisodes; Go: TestPlanScopedEpisodePrefersEpisodeOverPack and TestPlanScopedEpisodeNeverTakesPack; Go: TestPlanUnscopedMatchesLegacyPasses (table replaying packsize_test fixtures through the planner); Go: TestScopeWantedSkipsEpisodesWithFiles, TestScopeWantedReplaceIncludesTheEpisode, TestScopeWantedSeasonIgnoresSeasonFlag; Go: TestGrabOutcomeSummary (all-indexers-failed vs nothing-fit wording); Go: existing packsize_test.go and series_tier_test.go pass unchanged; UI check: 'Grab missing' label and visibility; episode Grab hidden on unaired and downloaded episodes
  - **Depends on:** [SER-01](#ser-01)
  - **Risk:** The planner extraction must keep the sweep's behaviour exactly, which is the purpose of the replay test. A tighter episode scope means some clicks that used to grab a pack now grab nothing; the outcome note must say so clearly. With SER-01 already in place, a wrong pick here wastes bandwidth but cannot overwrite files.
  - **Resolves:** series-1
<a id="ser-03"></a>
- [x] **SER-03 · Specials (season 0) become a real scope: explicit whole-show sentinel, S00Exx-only matching, no season-level Grab** — `P0` · `S` · Phase 1
  - **Problem:** api.ts:1302 drops season 0 (`if (season)`). On the backend, season<=0 means the whole show: RankSeriesReleasesWith fans out over every season, and seriesReleaseMatches and releaseMatchesScope return p.IsTV() for everything (series_interactive.go:217-218, 242-245). The Specials block renders the same Grab and Search buttons as a normal season (SeriesDetail.tsx:359-373). Its Grab sent autoGrabSeries(id,0,0), a packs-first pick across the whole show. A special episode's Grab (0,N) can take any TV release of the show.
  - **Approach:** 1. **Whole-show sentinel.**
       - handleSeriesReleases reads season only when the param is present: `season := -1; if v := q.Get("season"); v != "" { season, _ = strconv.Atoi(v) }`.
       - RankSeriesReleasesWith and searchSeriesScope ([SER-02](#ser-02)) treat season < 0 as the whole show (fan-out) and season == 0 as Specials.
       - fit_profiles.go:215-219 keeps forcing season >= 1.
       - The SeriesSearchModal 'Full show' tab sends no season, which still means the whole show.
    2. **Parser.** Add `Release.SeasonExplicit bool`, set when the reSxxExx or reNxNN form matched (parser.go:165-175, ~302), so 'Show.S00E05' can be told apart from an absolute-numbered '[Grp] Show - 05'.
    3. **Matching.**
       - In seriesReleaseMatches, releaseMatchesScope and coveredBy/coveredByFor, season 0 accepts only `Kind()==KindEpisode && SeasonExplicit && Season==0` releases whose Episodes contain the requested episode (any S00 episode when episode==0).
       - Packs, Complete releases, AliasEpisodes hits and absolute-numbered releases never match season 0.
       - The `season <= 0` branches become `season < 0`.
    4. **Season-0 search.** Instead of the fan-out, send one text query `indexerQuery(title) + " S00E" + %02d` per wanted special, at most 5.
    5. **GrabForScope.**
       - Accepts {Season:0, Episode:N}: wanted is that special, with no pack passes.
       - handleAutoGrabSeries returns 400 'Specials have no packs — grab a single special' for {season:0, episode:0}.
       - RegrabEpisode works for specials.
    6. **Frontend.**
       - api.ts seriesReleases uses `season !== undefined` and `episode !== undefined`.
       - SeasonBlock hides the season-level Grab for season 0 and keeps Search with `seriesReleases(id, 0)`.
       - The Specials modal subtitle reads 'Releases tagged S00 for this show'.
  - **Files:** `web/src/lib/api.ts`, `web/src/pages/SeriesDetail.tsx`, `internal/httpapi/series.go`, `internal/httpapi/fit_profiles.go`, `internal/automation/series_interactive.go`, `internal/automation/series.go`, `internal/parser/parser.go`
  - **Acceptance:**
    - The Specials header has no Grab button, and its Search lists only S00-tagged releases
    - POST /api/v1/series/{id}/autograb {season:0, episode:0} returns 400 and grabs nothing
    - A special's Grab only ever grabs a release parsed as S00E<that episode>; when there is none it grabs nothing, and the 'searched' event says so
    - A whole-show search (no season param) still fans out across seasons as before, and 'Choose release' on Season 1 is unchanged
  - **Tests:** Go: TestSeriesReleaseMatchesSpecials (S00E05 accepted; S01E05, Show.Complete, an S01-S03 pack and '[Grp] Show - 05' rejected); Go parser: TestSeasonExplicit (S00E05, 0x05 → true; '- 05', 'S02' pack → false); Go httpapi: TestAutoGrabRejectsSpecialsSeasonGrab; Go: TestHandleSeriesReleasesSeasonSentinel (absent → -1, '0' → 0); UI check: the Specials block shows Search only, and the releases request carries season=0
  - **Depends on:** [SER-02](#ser-02)
  - **Risk:** This breaks the 'season<=0 means whole show' convention. Every caller of RankSeriesReleases / searchSeriesScope must be checked: httpapi/series.go, fit_profiles.go, and the SeriesSearchModal 'Full show' tab. Grep for `season <= 0` and `season > 0` in automation before merging.
  - **Resolves:** series-2
<a id="ser-04"></a>
- [x] **SER-04 · Renames can never overwrite a file, and Rename shows its preview before moving anything** — `P0` · `M` · Phase 0
  - **Problem:** SeriesRename (series_interactive.go:591-631) walks episode rows in order and calls Importer.Move, a bare os.Rename with no existence check (library/importer.go:878-886). On Linux that silently replaces the target. After a renumber, the S3E01 row, now holding S2E22's file, is renamed onto the real S3E01 file's path and destroys it, and the loss cascades down the season.

Other problems:
- A multi-episode file is moved by its first row, and its second row then fails on a stale path.
- MoveEpisodeSubs, the book renames (automation/books.go:1252, 1270) and movies.Service.Rename (movies/service.go:1216, raw os.Rename) have the same hole.
- In the UI, Rename fetches the preview, uses only `matches`, and moves files immediately (SeriesDetail.tsx:224-229).
  - **Approach:** 1. **library.Importer.Move.**
       - `from == to` → nil.
       - Otherwise `os.Lstat(to)`. When it exists and `os.SameFile(fromInfo, toInfo)` is true (hardlinks to one inode, common because imports hardlink), remove the `from` name and return nil.
       - When it exists otherwise, return new `library.ErrTargetExists` (wrapped with the path).
       - MoveEpisodeSubs logs and skips on ErrTargetExists.
       - books.go:1252/1270 skip the item, log a warning and do not update the stored path.
       - movies.Service.Rename switches from os.Rename to `s.imp.Move`. That is a one-line change; coordinate with MOV's rename-preview task.
    2. **Pure planner.** `planSeriesRename(rows []renameRow, exists func(string) bool) (steps []renameStep, conflicts []RenameSkip)`. renameRow is {season, episode, path, target}.
       - Group rows by FilePath, so a multi-episode file is one step that updates all its rows. EpisodesSharingPath already exists in the repo.
       - Drop no-ops.
       - A target that is another step's source is a chain or cycle, and is routed through a temporary name.
       - A target that exists on disk and is not a planned source is a conflict with the reason 'a different file already exists there'.
    3. **Plan-then-apply SeriesRename(ctx, id, only []SeriesRenameItem) (SeriesRenameResult, error).**
       - Phase 1 moves chained sources to `.<base>.arrmada-rename-<n><ext>` in their own directory.
       - Phase 2 moves temps and plain sources to their targets.
       - If a phase-2 step fails, move that step's temp file back to its original name (best effort) and report it as skipped.
       - The DB is updated (MarkEpisodeImported for every row sharing the path) only for files actually moved. Subtitles follow each successful move. Emptied season folders are pruned as today.
       - `SeriesRenameResult{Moved int; Skipped []RenameSkip{From, To, Reason}}`.
       - When `only` is non-nil (the previewed list), steps not in it or whose target changed are skipped with 'changed since preview'.
    4. **SeriesRenamePreview** uses the same planner and returns items with a `conflict` reason field.
    5. **httpapi.**
       - GET /series/{id}/rename returns `{items:[{from,to,season,episode,conflict}], matches}`.
       - POST accepts an optional `{items}` and returns `{renamed, skipped}`.
       - The renumber callers (handleRefreshSeries, refreshSeriesSweep, RefreshContinuingSeries) log skipped items as warnings and add a series event: 'Rename skipped N files: <first 5 basenames>'.
    6. **UI.** Add a new `RenameModal`, in `web/src/pages/series/RenameModal.tsx` (the start of the page split).
       - It lists the preview grouped by season, as basename from → to, with a count and conflicts flagged in the reject colour.
       - Confirm posts the previewed items. The result shows moved and skipped.
       - 'Already named correctly' when the list is empty.
       - Existing tokens and type scale.
  - **Files:** `internal/library/importer.go`, `internal/automation/series_interactive.go`, `internal/automation/series_rename.go`, `internal/automation/books.go`, `internal/movies/service.go`, `internal/automation/series_refresh.go`, `internal/httpapi/series.go`, `web/src/pages/SeriesDetail.tsx`, `web/src/pages/series/RenameModal.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Renaming a chain (A takes B's name, B takes C's name) and a swap (A↔B) ends with every file present under its new name and the DB pointing at the new paths
    - When a target path already holds an untracked file, that move is skipped and reported in the response and the series event, and both files survive
    - A double-episode file is moved once and both episode rows point to the new path
    - Clicking Rename shows the list of changes and moves nothing until Confirm; changes made after the preview are skipped, not applied
    - Book and movie renames never overwrite an existing file
  - **Tests:** Go library: TestMoveRefusesExistingTarget, TestMoveSameInodeRemovesSourceName (hardlink in t.TempDir), TestMoveSamePathIsNoop; Go automation: TestPlanSeriesRenameChainAndSwap (pure), TestPlanSeriesRenameForeignTargetConflict (pure), TestPlanSeriesRenameMultiEpisodeFileOnce (pure); Go automation: TestSeriesRenameSwapChain (temp-dir library with S2E22, S3E01, S3E02 remapped one slot; all three files survive); Go automation: TestSeriesRenameSkipsChangedSincePreview; UI check: the Rename modal lists items grouped by season and requires Confirm
  - **Risk:** Move is shared with books and movies, so its new error must be handled at every call site; grep for `.Move(` and `os.Rename(` in internal/. Temporary names must never be left behind; the failure path restores them, and RescanSeries ignores dot-prefixed files. Tests must use t.TempDir fixtures only, never the real library.
  - **Resolves:** series-4, series-12
<a id="ser-05"></a>
- [x] **SER-05 · Pin the numbering source per series; scheduled, import-time and fallback refreshes never renumber or move files** — `P0` · `M` · Phase 0
  - **Problem:** Counted absolute numbers drive automatic file moves and renames:
- seasonsFromDetails (series/service.go:158-187) gives every show counted absolute numbers 1..N unless TVDB supplied real ones.
- numberingModelChanged (service.go:291-308) fires on any single shifted absolute, which contradicts its own comment.
- RebuildEpisodes (repo.go:233-379) then carries files by those counted absolutes. Every refresh caller renames on disk: the manual refresh, refresh-all, and the 6-hourly RefreshContinuingSeries (series_refresh.go:49-61).
- The import-time refresh (reviews.go:499) ignores `renumbered`, so it can remap rows without renaming files at all.
- The episode source silently falls back from TVDB to TVmaze to TMDB on any error (metadata/series_episodes.go:73-79), and nothing records which source numbered the show.
Result: a TVmaze timeout or 429, or a one-episode count change, relinks S06E22's file to S07E01 and renames it, and the next good refresh moves it back.
  - **Approach:** 1. **metadata.**
       - Add `Name() string` to EpisodeSource: TVDB returns "tvdb", TVmaze "tvmaze".
       - Add `NumberingSource string` and `NumberingFallback bool` to SeriesDetails, both `json:"-"`.
       - TMDB.GetSeries sets NumberingSource="tmdb". seriesWithEpisodeSource.GetSeries overwrites it with the source whose listing it used.
       - It sets NumberingFallback=true when a higher-priority source that was Available() returned an error. Skipping an unavailable source (no key), an unusable listing or an incompatible model is deterministic and does not count.
    2. **Migration** `NNNN_series_numbering_source.sql`: `ALTER TABLE series ADD COLUMN numbering_source TEXT NOT NULL DEFAULT ''`.
       - Add it to seriesCols/scanSeries, `Series.NumberingSource` (`json:"numbering_source"`) and `Repo.SetNumberingSource`.
       - Switch the series package test helper (refreshmeta_test.go testRepo) from its hand-rolled schema to `store.Open(t.TempDir())`, so this and later columns don't break tests.
    3. **New signature.** `Service.Refresh(ctx, id, opts RefreshOptions{AllowRebuild bool}) (Series, RefreshResult, error)`, with `RefreshResult{Renumbered bool; Remaps []EpisodeRemap; Fallback bool; ModelChanged bool}`.
       - handleRefreshSeries passes AllowRebuild=true.
       - refreshSeriesSweep, RefreshContinuingSeries and the import-time knownEpisode refresh pass false.
    4. **Rules inside Refresh.**
       - (a) **Fallback.** INSERT OR IGNORE only genuinely new (season, episode) rows, with a new `Repo.InsertNewEpisodes` that does not overwrite existing rows' metadata, because a fallback listing may be numbered differently. No RebuildEpisodes, PruneSeasonsNotIn or absolute reassign, and numbering_source is unchanged. Log a warning, and return Fallback=true.
       - (b) **Standard shows** never rebuild by absolute number; identity is (season, episode). A new `Repo.ReassignAbsolutes(ctx, seriesID, seasons)` runs `UPDATE episodes SET absolute_number=? WHERE series_id=? AND season_number=? AND episode_number=?` in one transaction and never touches files.
       - (c) **Model change.** `numberingModelChanged(desired, stored, authoritative bool)` returns true for a season-set difference, or for absolute disagreement only when `authoritative` (stored and fresh are both 'tvdb').
       - (d) **When a rebuild is allowed.** Only with AllowRebuild, and only when stored and fresh are both 'tvdb', or when stored is '' or 'tmdb', fresh is 'tvdb', and the show is anime. The second case is the TVDB-key-added scenario RebuildEpisodes was built for.
       - (e) **Otherwise** (model changed but not allowed), take the additive path and add one series event: 'Numbering from <fresh> differs from what's stored — N files would move. Press Refresh to apply, or review it under Numbering.' [SER-07](#ser-07) turns this into a stored proposal.
       - (f) Store numbering_source only when the stored rows now reflect the fresh source: the same model with no fallback, or a rebuild applied.
    5. **Import-time refresh.** In reviews.go knownEpisode, when RefreshResult.Fallback is set, count the file as `failed` (retried next sweep) rather than `unresolved`, so a TVmaze outage doesn't send new episodes to Review.
    6. **Renames after a rebuild** go through [SER-04](#ser-04)'s collision-safe SeriesRename.
  - **Files:** `internal/metadata/series_episodes.go`, `internal/metadata/provider.go`, `internal/metadata/tmdb.go`, `internal/metadata/tvdb.go`, `internal/metadata/tvmaze.go`, `internal/series/service.go`, `internal/series/repo.go`, `internal/series/series.go`, `internal/series/refreshmeta_test.go`, `internal/series/rebuild_test.go`, `internal/automation/series_refresh.go`, `internal/automation/reviews.go`, `internal/httpapi/series.go`, `internal/store/migrations/NNNN_series_numbering_source.sql`
  - **Acceptance:**
    - With TVmaze returning an error, a scheduled refresh of a show whose TMDB listing has one episode fewer in an earlier season moves and renames nothing; the log says 'numbering source failed — metadata only'
    - A standard show whose season 1 gains an episode keeps every file on its (season, episode); only absolute numbers change
    - A manual Refresh of an anime show after a TVDB key is added still rebuilds to TVDB's season model and renames through the safe rename
    - series.numbering_source is filled after the first successful non-fallback refresh and appears in GET /series/{id}
    - Scheduled, refresh-all and import-time refreshes never return Renumbered=true
    - An import during a TVmaze outage retries next sweep instead of creating a review item
  - **Tests:** Go metadata: TestGetSeriesReportsFallbackWhenSourceErrors (stub sources: one errors, one returns an empty listing → fallback true; unavailable source → false); Go metadata: TestGetSeriesNumberingSourceName; Go series (fake metadata.SeriesProvider in a new fakemeta_test.go): TestRefreshFallbackNeverRebuilds, TestRefreshStandardCountShiftKeepsFilesOnSE, TestRefreshScheduledNeverRebuilds, TestRefreshManualTVDBRebuildStillWorks, TestRefreshDeclinedRebuildKeepsSource; Go: update TestNumberingModelChanged and add TestNumberingModelChangedIgnoresCountedShift; Go: TestReassignAbsolutesLeavesFiles
  - **Depends on:** [SER-04](#ser-04)
  - **Risk:** The Refresh signature change touches four callers. Shows that relied on scheduled rebuilds now need a manual Refresh, or SER-07's proposal; the event text must say so. Legacy rows start with numbering_source='', and the rules treat '' as non-authoritative, so nothing is auto-rebuilt after the upgrade.
  - **Resolves:** series-4

#### Milestone: M2 — Show data, monitoring and progress you can trust

_Ended and revived shows are recognised, and titles, posters and network follow TMDB. A numbering change becomes a proposal you review before anything moves. Pausing a show keeps per-season choices. Shows can be added with Sonarr-style presets. Progress, Missing and Partial count only what is monitored._

<a id="ser-06"></a>
- [x] **SER-06 · Refresh updates the show itself (status, title, poster, network, year, extra); ended shows are re-checked weekly** — `P1` · `S` · Phase 4
  - **Problem:** Series.Refresh (service.go:213-284) fetches fresh details but only writes episodes, seasons, tvdb_id and the original language. series.status, title, poster_url, overview, network and year are only written by the INSERT on add (repo.go:142). The stored status matters in three places:
- it gates complete and multi-season packs via showEnded(s.Status) (automation/series.go:494);
- it makes RefreshContinuingSeries skip stored-ended shows forever (series_refresh.go:46);
- it drives the Continuing/Ended badge and filters.
A show that ends is never treated as ended. A revived show is never auto-refreshed, so its new season never appears.
  - **Approach:** 1. **New method.** `Repo.UpdateSeriesMetadata(ctx, id, m SeriesMeta{Title, Year, Overview, PosterURL, Status, Network string/int; Extra *SeriesExtra})`.
       - It overwrites a column only with a non-empty or non-zero fresh value.
       - The extra blob is rebuilt with extraFrom(d), merged field by field so an empty fresh field keeps the stored one. Genres, backdrop, cast, original title and language are refreshed.
       - Movies already do the same (movies.Service.Refresh → UpdateMetadata).
    2. **Call it in Refresh** after every successful GetSeries, including the fallback path; the show row doesn't depend on numbering. It is independent of [SER-05](#ser-05)'s numbering rules.
       - If the title key changed, keep the old title as a title-only alias via the existing `AddAlias(ctx, id, oldTitle, 0)`, ignoring an 'already an alias' error. Then add the event 'Title changed: A → B'. The library folder keeps its name, because ExistingFolderName is used for imports.
       - If the status changed, add the event 'Status: Returning Series → Ended'.
    3. **Migration** `NNNN_series_last_refreshed.sql`: `ALTER TABLE series ADD COLUMN last_refreshed_at TEXT NOT NULL DEFAULT ''`, set to datetime('now') on every successful refresh.
    4. **RefreshContinuingSeries** also refreshes monitored ended or canceled shows whose last_refreshed_at is empty or more than 7 days old, keeping the 1.5 s pacing.
  - **Files:** `internal/series/repo.go`, `internal/series/service.go`, `internal/series/series.go`, `internal/automation/series_refresh.go`, `internal/store/migrations/NNNN_series_last_refreshed.sql`
  - **Acceptance:**
    - A show stored as 'Returning Series' that TMDB now lists as 'Ended' reads Ended after one refresh, and the next sweep may take multi-season or complete packs
    - A show stored as Ended that TMDB lists as Returning Series with a new season gains that season within 7 days without a manual refresh
    - Poster, overview, network and title follow TMDB, and a renamed show still matches releases under its old title
    - Empty fields from the provider never blank out stored values
    - History shows 'Status: … → …' and 'Title changed' events
  - **Tests:** Go: TestRefreshUpdatesStatusTitlePosterNetwork (fake SeriesProvider from SER-05, in refreshmeta_test.go); Go: TestRefreshKeepsOldTitleAsAlias; Go: TestRefreshDoesNotBlankOnEmptyFields; Go: TestRefreshContinuingIncludesStaleEndedShows (pure selector over a List snapshot with last_refreshed_at)
  - **Depends on:** [SER-05](#ser-05)
  - **Risk:** A provider returning partial data must not blank stored fields; the merge rule and its test cover this. A weekly refresh of ended shows adds TMDB calls, so keep the existing pacing. A changed title does not rename the on-disk folder, on purpose.
  - **Resolves:** series-3
<a id="ser-07"></a>
- [x] **SER-07 · Numbering changes become a reviewable proposal with a remap preview, Apply and Dismiss** — `P1` · `M` · Phase 4
  - **Problem:** After SER-05, a source change or an authoritative renumber that a scheduled refresh declines is only logged. The owner cannot see which files would move, or accept the change without pressing Refresh blind. RebuildEpisodes also resets season-level monitored flags, because it re-inserts seasons with flags computed from the series (repo.go:280-296).
  - **Approach:** 1. **Migration** `NNNN_series_numbering_pending.sql`: `series_numbering_pending(series_id INTEGER PRIMARY KEY REFERENCES series(id) ON DELETE CASCADE, from_source TEXT NOT NULL DEFAULT '', to_source TEXT NOT NULL DEFAULT '', plan_hash TEXT NOT NULL, remaps_json TEXT NOT NULL, dismissed_hash TEXT NOT NULL DEFAULT '', created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`.
    2. **One shared planner.** Factor the snapshot and absolute mapping out of RebuildEpisodes into a pure `planRemaps(current []placementRow, desired []Season) []EpisodeRemap`, used by both RebuildEpisodes and the new `Repo.PlanRebuild(ctx, seriesID, seasons) ([]EpisodeRemap, error)`, which writes nothing.
       - `planHash(remaps)` is sha256 over the remaps sorted by (OldSeason, OldEpisode).
    3. **Storing proposals.** Where [SER-05](#ser-05) declines a rebuild (model changed, not allowed), Refresh calls PlanRebuild and upserts the pending row, unless the hash equals dismissed_hash. It adds one series event per distinct plan hash.
    4. **API (httpapi/series.go + server.go).**
       - `GET /api/v1/series/{id}/numbering` returns `{source, pending: {from, to, created_at, plan_hash, remaps: [{absolute, old:'S02E22', new:'S03E01', file}]}|null}`.
       - `POST /api/v1/series/{id}/numbering/apply {plan_hash}` (manager only) re-fetches metadata and re-plans; it returns 409 if the hash differs. Otherwise it runs RebuildEpisodes, [SER-04](#ser-04)'s SeriesRename and RescanSeries, stores numbering_source, deletes the pending row, and returns `{moved, skipped}`.
       - `DELETE /api/v1/series/{id}/numbering/pending` records dismissed_hash and keeps the row hidden.
       - Apply and Refresh hold a new per-series mutex in series.Service, so a scheduled refresh can't interleave with an apply.
    5. **Monitored flags.** RebuildEpisodes snapshots `monBySeason` and restores season flags by season number. A season that is new in the model takes series.monitor_new_seasons once [SER-08](#ser-08) lands; until then, the series flag.
    6. **Event text.** The 'renumbered' event lists remaps: the first 20 'S02E22 → S03E01', then 'and N more'.
    7. **UI.** Until [SER-20](#ser-20) adds the Numbering tab, SeriesDetail shows a banner when pending is set: 'Numbering from TVDB differs — 14 files would move. Review'. It opens a modal with the remap table (old, new, file basename) and Apply / Dismiss buttons, showing the apply result's moved and skipped.
  - **Files:** `internal/series/repo.go`, `internal/series/service.go`, `internal/series/rebuild_test.go`, `internal/httpapi/series.go`, `internal/httpapi/server.go`, `internal/store/migrations/NNNN_series_numbering_pending.sql`, `web/src/pages/SeriesDetail.tsx`, `web/src/pages/series/NumberingReviewModal.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A refresh whose numbering would move files creates exactly one pending proposal and moves nothing
    - The review modal lists every remap with old and new episode and the file name; Apply performs exactly those moves through the collision-safe rename
    - After Dismiss, the same plan is not proposed again; a different plan is
    - Applying a stale plan (metadata changed since it was shown) returns 409 and moves nothing
    - Season monitored flags survive a rebuild
  - **Tests:** Go: TestPlanRebuildMatchesRebuildRemaps (same fixture through both paths); Go: TestPlanHashDeterministic (shuffled input gives the same hash); Go: TestRefreshStoresPendingInsteadOfRebuilding, TestDismissedPlanNotReproposed (fake provider); Go: TestApplyPendingRejectsStalePlan; Go: TestRebuildKeepsSeasonMonitoredFlags (extend rebuild_test.go); UI check: banner and modal on a seeded pending row
  - **Depends on:** [SER-04](#ser-04), [SER-05](#ser-05)
  - **Risk:** The plan hash must be deterministic (sorted remaps), or Apply returns spurious 409s. Apply re-fetches metadata; if the source is down at that moment, the hash differs, and the 409 message must say 'metadata changed or is unavailable — try again'.
  - **Resolves:** series-4
<a id="ser-08"></a>
- [x] **SER-08 · The series monitor toggle becomes a gate that keeps season choices; new seasons follow 'monitor new seasons'** — `P1` · `S` · Phase 4
  - **Problem:** Several monitoring behaviours override what the owner chose:
- Repo.SetMonitored (repo.go:475-498) cascades to every season and episode. Pausing and resuming a show re-monitors seasons the owner excluded, and the next sweep grabs them. The list page's bulk Monitor does the same (Series.tsx:78-86).
- Refresh gives new episodes and seasons the series flag (service.go:220, seasonsFromDetails(d, sr.Monitored)), whatever the season's own choice.
- Nothing in the UI says an unmonitored series ignores its monitored seasons; the sweep skips it at series.go:148.
- The dashboard's 'episodes missing' (httpapi/dashboard.go:267) counts monitored file-less episodes of paused shows and unaired episodes.
  - **Approach:** 1. **Migration** `NNNN_series_monitor_new_seasons.sql`: `ALTER TABLE series ADD COLUMN monitor_new_seasons INTEGER NOT NULL DEFAULT 1; UPDATE series SET monitor_new_seasons = monitored;`. Library-scanned shows, which are added unmonitored, then don't start auto-monitoring new seasons.
       - Add `Series.MonitorNewSeasons` (`json:"monitor_new_seasons"`) to seriesCols/scanSeries.
       - Add `Repo.SetMonitorNewSeasons`.
    2. **The gate.**
       - Repo.SetMonitored only updates series.monitored.
       - Service.SetMonitored: when enabling and `!repo.HasMonitoredRegularEpisode(id)`, it monitors every non-special season and episode and sets monitor_new_seasons=1. That is today's enable cascade, now only for the nothing-monitored case, which keeps the fix monitor_cascade_test.go documents. [SER-09](#ser-09) replaces 'all' with the requested preset.
       - Disabling touches nothing else.
    3. **HTTP.** PUT /series/{id}/monitor accepts optional `monitor_new_seasons`.
    4. **Refresh.** seasonsFromDetails takes `monitorFor func(seasonNumber int) bool` instead of a bool.
       - For an existing season, it returns the stored season flag, so a new episode in that season inherits it.
       - For a season new to the show, it returns series.MonitorNewSeasons. This is independent of the pause gate: a paused show still gets the right flags for when it resumes.
       - Specials are always false.
       - Add keeps today's rule (the series flag) until [SER-09](#ser-09).
    5. **Consumers of the gate.**
       - The sweep, RSS and upgrades already skip !s.Monitored, and AcquisitionSummary filters s.monitored.
       - Fix dashboard.go:267 to count only aired, file-less, monitored episodes in monitored seasons of monitored series, using a join on seasons and series.
    6. **UI.**
       - SeriesDetail: when !series.monitored, the season and episode toggles render dimmed with the hint 'Series paused — nothing is searched'.
       - Add a 'Monitor new seasons' checkbox next to the monitor switch ([SER-14](#ser-14) moves it into the Monitor menu).
       - Series.tsx bulk Monitor/Unmonitor keep calling setSeriesMonitored, which now has gate semantics, and the toast says 'paused' and 'resumed'.
  - **Files:** `internal/series/repo.go`, `internal/series/service.go`, `internal/series/series.go`, `internal/series/monitor_cascade_test.go`, `internal/httpapi/series.go`, `internal/httpapi/dashboard.go`, `internal/store/migrations/NNNN_series_monitor_new_seasons.sql`, `web/src/pages/SeriesDetail.tsx`, `web/src/pages/Series.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Unmonitor seasons 1-3, pause the show, resume it: seasons 1-3 stay unmonitored and the next sweep doesn't search them
    - Turning on a library-scanned show with nothing monitored still monitors all regular episodes
    - A new season that appears on refresh is monitored only when the series has 'monitor new seasons' on; a new episode in an existing season takes that season's flag
    - The dashboard's missing-episodes count excludes paused shows, unmonitored seasons and unaired episodes
    - A paused show's season and episode toggles are dimmed with the 'Series paused' hint
  - **Tests:** Go: rewrite monitor_cascade_test.go as TestPauseResumeKeepsSeasonChoices and TestEnableWithNothingMonitoredMonitorsAllRegular; Go: TestRefreshNewSeasonFollowsMonitorNewSeasons, TestRefreshNewEpisodeInheritsSeasonFlag (fake provider); Go: TestMonitorNewSeasonsMigrationBackfill (store.Open); Go httpapi: TestDashboardMissingIgnoresPausedShows (extend dashboard_test.go); UI check: paused show dims its toggles
  - **Depends on:** [SER-05](#ser-05)
  - **Risk:** This changes behaviour for anyone who relied on the cascade to bulk re-monitor everything. The 'nothing monitored → all' rule keeps the common case working, and SER-09's presets give an explicit 'monitor all' action.
  - **Resolves:** series-6
<a id="ser-09"></a>
- [x] **SER-09 · Monitor presets at add time and on the series page, with season flags derived from their episodes** — `P1` · `M` · Phase 4
  - **Problem:** AddSeriesModal always sends monitored:true for every non-special season, with no Sonarr-style choice (All, Future, Missing, Existing, First season, Latest season, None). Its 'Search on add' toggle silently rewrites the global setting (Series.tsx:576-584). Season and episode flags can disagree: monitoring one episode in an unmonitored season does nothing, because wantedEpisodes requires both flags (automation/series.go:802-807). REQ's season picker needs a preset API to call.
  - **Approach:** 1. **`series.Service.ApplyMonitorPreset(ctx, id, preset string) error`.** One transaction, presets as in the design table (all, future, missing, existing, first_season, latest_season, none).
       - Episode flags are set by rule from has_file and air_date (today's date).
       - Specials are never auto-monitored.
       - It also sets monitor_new_seasons per preset.
       - It finishes with `syncSeasonMonitored`: season.monitored = EXISTS(monitored episode in it).
       - Unknown preset → ErrUnknownPreset (400).
    2. **Derived season flag everywhere.**
       - SetEpisodeMonitored (and [SER-16](#ser-16)'s bulk version) call `Repo.syncSeasonMonitored(seriesID, season)`.
       - SetSeasonMonitored keeps setting all of its episodes, so it stays consistent.
    3. **Add.** `Service.Add(ctx, tmdbID, profile, AddOptions{Monitored bool; Preset string; MonitorNewSeasons *bool})` inserts seasons, then applies the preset (default 'all').
       - ScanLibrary's internal Add (service.go:865) passes Monitored=false and preset 'none'.
       - requests/service.go:192 passes the default; REQ adds its season picker on top of this API.
    4. **HTTP.**
       - POST /series accepts `monitor` (a preset) and `monitor_new_seasons`. The default preset comes from a new setting, `series_monitor_default` (default 'all', in httpapi/settings.go, exposed in Settings → Library defaults).
       - `search_on_add` from the modal is per request only: false means the series gate is off, while episode flags still follow the preset.
       - PUT /series/{id}/monitor accepts `{monitored, preset?, monitor_new_seasons?}`. A preset applies first. [SER-08](#ser-08)'s nothing-monitored rule uses the given preset instead of 'all'.
    5. **UI.**
       - AddSeriesModal gets a Monitor select (All episodes, Future episodes, Missing episodes, Existing episodes, First season, Latest season, None), pre-selected from the setting.
       - The modal stops calling api.updateSettings; its toggle is local state.
       - SeriesDetail gets a temporary 'Apply monitoring…' select beside the switch; [SER-14](#ser-14) folds it into the Monitor dropdown.
       - The Series.tsx bulk bar gets 'Monitoring preset…'.
  - **Files:** `internal/series/service.go`, `internal/series/repo.go`, `internal/httpapi/series.go`, `internal/httpapi/settings.go`, `internal/requests/service.go`, `web/src/pages/Series.tsx`, `web/src/pages/SeriesDetail.tsx`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Adding a show with 'Future episodes' leaves every aired episode unmonitored, and the first sweep grabs nothing old
    - 'Latest season' monitors only the highest season with episodes and turns 'monitor new seasons' on; 'First season' turns it off
    - Monitoring a single episode in an unmonitored season makes the season monitored and the episode searchable
    - Flipping 'Search on add' in the modal no longer changes Settings
    - Each preset applies to an existing show from the series page in one action
  - **Tests:** Go: TestApplyMonitorPreset (table over every preset on a seeded 3-season show with files, past and future air dates and specials); Go: TestEpisodeMonitorSyncsSeasonFlag; Go: TestAddAppliesPreset and TestScanAddIsNone (fake provider); Go httpapi: TestAddSeriesSearchOnAddDoesNotWriteSetting, TestUnknownPresetIs400; UI check: preset select in the add modal; bulk preset on the list page
  - **Depends on:** [SER-08](#ser-08)
  - **Risk:** 'missing' and 'existing' change which episodes the upgrade sweep considers (it needs Monitored && HasFile). The preset help text must say 'episodes with files won't be upgraded' for 'missing'. Air-date edge cases (empty date counts as unaired) must match automation's aired().
  - **Resolves:** series-6
<a id="ser-10"></a>
- [x] **SER-10 · Progress, Missing and Partial count only monitored episodes; GET /series/{id} returns stats** — `P1` · `S` · Phase 4
  - **Problem:** allStats (series/repo.go:84-89) counts every aired, non-special episode regardless of monitoring, although the Stats comment says 'aired episodes in monitored seasons' (series.go:39). These totals drive:
- the list's Missing filter (Series.tsx:30);
- statusOf 'Partial' (Series.tsx:293-298);
- the card's 'Search now' (Series.tsx:310, 348);
- the progress bars.
A show where only the latest season is wanted reads '12/180 · PARTIAL' forever. Service.Get never sets Stats, so the detail page recomputes progress client-side, and its History and Duplicates panels never refresh.
  - **Approach:** 1. **Rewrite the allStats SQL** to join seasons on (series_id, season_number), for season_number > 0:
       - `episodes` = has_file OR (e.monitored AND sn.monitored AND aired);
       - `have_files`, `size_bytes`;
       - `missing` = e.monitored AND sn.monitored AND aired AND has_file=0;
       - `unmonitored_missing` = aired AND has_file=0 AND NOT (e.monitored AND sn.monitored);
       - `next_air_date` = MIN(air_date) WHERE air_date > date('now') AND e.monitored AND sn.monitored AND has_file=0.
       'aired' means `air_date <> '' AND date(air_date) <= date('now')`, matching automation aired().
    2. **`Repo.StatsFor(ctx, id)`** uses the same SQL with a WHERE clause, via one shared query builder. Service.Get sets `sr.Stats`.
    3. **Stats struct.** It gains `Missing`, `UnmonitoredMissing` and `NextAirDate` (json `missing`, `unmonitored_missing`, `next_air_date`), and the comment is fixed.
    4. **Frontend.**
       - The Missing filter becomes `s.monitored && missing > 0`. statusOf uses `missing`. The card shows Search now only when `missing > 0`.
       - The table shows 'have/episodes', with a greyed '+N not monitored' and a tooltip.
       - SeriesDetail's overall progress and the season headers read from the series and season data with monitored-only counting (for example '8/10 · 2 not monitored').
  - **Files:** `internal/series/repo.go`, `internal/series/series.go`, `internal/series/service.go`, `internal/series/repo_test.go`, `web/src/pages/Series.tsx`, `web/src/pages/SeriesDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A show with only its latest season monitored, and that season complete, reads Complete, is not in the Missing filter and has no Search now button
    - The detail page's progress matches the list card
    - GET /series/{id} includes stats with missing and next_air_date
    - The '+N not monitored' remainder is shown greyed where totals appear
  - **Tests:** Go: TestAllStatsIgnoresUnmonitoredSeasons, TestStatsForMatchesAllStats, TestNextAirDateMonitoredOnly (repo_test.go); UI check: the Missing filter count drops for partly monitored shows
  - **Depends on:** [SER-08](#ser-08)
  - **Risk:** Owners used to the old totals may read the smaller numbers as lost episodes. The greyed '+N not monitored' remainder makes the difference explicit.
  - **Resolves:** series-10

#### Milestone: M3 — The right show under every name

_Remakes and US/UK variants stop cross-grabbing. Releases grabbed through an alias or romaji title import automatically. Romaji anime is found without hand-typed aliases, and old anime episodes can be searched by absolute number._

<a id="ser-11"></a>
- [x] **SER-11 · Release identity: compare year and country, match imports with MatchRelease, and route downloads to the show they were grabbed for** — `P1` · `M` · Phase 5
  - **Problem:** releaseIsForSeries (series_reliability.go:290-294) compares title keys only. The parser takes the year out of the title ('Doctor.Who.2005' → 'Doctor Who'), and the series path never compares it, although the movie path does (coordinator.go:770, 1580). MatchByTitle returns the first same-titled show in added_at DESC order (service.go:1096-1107). Country tags are not stripped, so 'The.Office.US.S01E01' (key 'theofficeus') never equals TMDB's 'The Office'. ImportSeriesDownloads routes on MatchByTitle(parsed.Title) only (series.go:988), so a release grabbed through an alias or romaji title matches no series and lands in review as 'Grabbed for X but the download looks like Y' (series.go:1020-1028).
  - **Approach:** 1. **Parser.**
       - Add `Release.TitleYear int`: the year token only when it sits before the season/episode marker. Today `Year` takes the last year token, so an air year after SxxExx counts; TitleYear does not.
       - Add `parser.SplitCountry(title string) (base, cc string)` for a trailing US, UK, GB, AU, NZ or CA token.
    2. **TMDB.** Parse `origin_country` into `SeriesDetails.OriginCountry []string` and store it in `SeriesExtra.OriginCountry`, filled on Add and on Refresh via [SER-06](#ser-06)'s extra refresh.
    3. **New `seriesIdentity(p parser.Release, s series.Series) (ok bool, why string)`** in series_reliability.go. All three must hold:
       - the title key matches, or the existing romaji or alias rules match;
       - TitleYear is 0, or the series year is 0, or the two are within ±1, or the series title itself contains a 4-digit year ('1923');
       - the country token is absent, or in OriginCountry (UK ≡ GB). A key that only matches after stripping the country counts only when the country matches.
       releaseIsForSeries and seriesTitleMatches call it, which covers the sweep, RSS, interactive search, upgrades and the in-flight check.
    4. **`series.Service.MatchRelease(ctx, p parser.Release) (Series, bool, []Series)`.**
       - Among series whose key or alias key matches exactly, prefer the one whose year and country fit.
       - An ambiguous match without a year returns no match plus the candidates, so the caller can name them in the review reason.
       - TitleMatcher gets a `ReleaseMatcher(all)` variant with the same rules.
       - EpisodeTitleByName (the naming callback) and the wrong-category diagnostic (series.go:974) use the year-aware match.
    5. **ImportSeriesDownloads.**
       - First, `grabbedMediaForHash`: when the hash was grabbed for series X and `seriesTitleMatches(it.Name, X)` holds (covering alias, romaji and country grabs), import into X directly.
       - Otherwise use MatchRelease.
       - An ambiguous match goes to review with the reason 'Matches several shows: A (1963), B (2005)'.
       - The existing review path stays for genuine mismatches.
       - When ACQ's acquisitions table lands, the grab-hash lookup moves onto it.
  - **Files:** `internal/parser/parser.go`, `internal/parser/titlekey.go`, `internal/metadata/tmdb.go`, `internal/metadata/provider.go`, `internal/series/series.go`, `internal/series/service.go`, `internal/automation/series_reliability.go`, `internal/automation/series.go`
  - **Acceptance:**
    - With 'Doctor Who' (1963) and 'Doctor Who' (2005) in the library, 'Doctor.Who.2005.S01E01' is only accepted and imported for the 2005 show
    - 'The.Office.US.S02E01' matches TMDB 'The Office' (origin US) and not 'The Office' (origin GB)
    - A release grabbed through an alias ('BLEACH Thousand-Year Blood War S02E02') auto-imports into Bleach instead of going to review
    - A yearless release whose title matches two library shows goes to review naming both, instead of the newest one
    - 'Show.S01E01.2019.1080p' (an air year after the marker) is not rejected on year
  - **Tests:** Go parser: TestTitleYearOnlyBeforeMarker, TestSplitCountry; Go automation: TestSeriesIdentityYear, TestSeriesIdentityCountry (tables); Go series: TestMatchReleasePrefersYear, TestMatchReleaseAmbiguousNoYear, TestMatchReleaseAliasExact; Go: TestImportRoutesAliasGrabToGrabbedSeries; Go: existing titlemismatch_test.go and grabmatch_test.go pass as a regression corpus
  - **Depends on:** [SER-06](#ser-06)
  - **Risk:** Over-strict year matching could reject real releases, such as P2P names that include air years. Reading the year only before the marker, plus the ±1 tolerance, limits that. Two title normalizers exist (automation titleKey, series normKey); seriesIdentity must use one consistently, and BE may unify them later.
  - **Resolves:** series-7
<a id="ser-12"></a>
- [x] **SER-12 · Seed aliases from TMDB alternative titles (romaji and US/UK variants) and show the alias panel for every series** — `P1` · `M` · Phase 5
  - **Problem:** Extra.OriginalTitle is TMDB's original_name (tmdb.go:193, 271), which for Japanese shows is kana or kanji (葬送のフリーレン). seriesTitleMatches nonetheless treats it as romaji (series_reliability.go:302), and it is never searched. alternative_titles is never fetched (append_to_response is 'credits,external_ids', tmdb.go:180). So SubsPlease and Erai-raws releases ('Sousou no Frieren - 13') only match after the owner types an alias by hand, and US/UK variant titles never seed aliases. The Alternate titles panel only renders for anime (SeriesDetail.tsx:274).
  - **Approach:** 1. **TMDB GetSeries.** append_to_response becomes 'credits,external_ids,alternative_titles'. Add `SeriesDetails.AltTitles []AltTitle{Title, Country, Type}`.
    2. **Migration** `NNNN_series_alias_source.sql`: `ALTER TABLE series_aliases ADD COLUMN source TEXT NOT NULL DEFAULT 'user'; ALTER TABLE series_aliases ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0;`.
       - Alias gains `Source` and `Disabled`.
       - Repo.Aliases and AliasTitlesFor return only enabled rows for matching. The list endpoint returns enabled rows with their source.
    3. **`Service.syncTMDBAliases(ctx, id, d)`** runs on Add and Refresh.
       - It picks Latin-script titles (new `parser.IsLatin`) whose type is romaji or romanized or whose country is JP, plus US/GB English variants that differ from the display title. At most 5, romaji first.
       - It skips keys equal to the series title, an existing alias, or another library series' title key (collision guard).
       - It upserts rows with source='tmdb' and TMDBSeason 0 (title-only).
       - It never touches user rows, and never re-adds a disabled key.
    4. **Matching.**
       - TMDB aliases match by exact title key in seriesTitleMatches and MatchRelease. The whole-word prefix match (parser.TitleHasPrefix) stays for user aliases.
       - OriginalTitle is used only when parser.IsLatin(OriginalTitle).
       - Fix the 'romaji' comments in series.go:50-52 and service.go:1170.
    5. **Search.** searchSeriesReleases and searchSeriesScope include at most 2 TMDB aliases per search (romaji first). User aliases are unchanged, to protect indexer limits.
    6. **UI.**
       - AliasPanel renders for every series, collapsed by default, with rows badged 'from TMDB' or 'added by you'.
       - Removing a TMDB alias calls DELETE, which sets disabled=1 for source='tmdb' and deletes user rows.
  - **Files:** `internal/metadata/tmdb.go`, `internal/metadata/provider.go`, `internal/series/alias.go`, `internal/series/repo.go`, `internal/series/service.go`, `internal/series/series.go`, `internal/parser/titlekey.go`, `internal/automation/series_reliability.go`, `internal/automation/series.go`, `internal/automation/series_interactive.go`, `internal/httpapi/series.go`, `internal/store/migrations/NNNN_series_alias_source.sql`, `web/src/pages/SeriesDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - After a refresh, Frieren has the alias 'Sousou no Frieren' (from TMDB), and '[SubsPlease] Sousou no Frieren - 13 (1080p)' is matched and grabbable without manual setup
    - A show whose TMDB alternative titles include another library show's title does not get that alias
    - A TMDB alias the user removed stays gone after the next refresh; a sync never changes user aliases
    - The alias panel appears, collapsed, on a non-anime show
  - **Tests:** Go: TestSyncTMDBAliasesPicksRomaji (fixture JSON), TestSyncTMDBAliasesCollisionGuard, TestDisabledAliasNotReAdded, TestUserAliasUntouchedBySync; Go: TestTMDBAliasExactMatchOnly, TestOriginalTitleNonLatinIgnored; Go parser: TestIsLatin; UI check: alias panel on a standard show
  - **Depends on:** [SER-11](#ser-11)
  - **Risk:** Alias queries add indexer load, which the 2-per-search cap bounds. Exact-match-only for automatic aliases avoids new false positives. alternative_titles quality varies by show, so the collision guard plus Disable keep a bad seed harmless.
  - **Resolves:** series-7, series-8
<a id="ser-13"></a>
- [x] **SER-13 · Anime episode searches query the absolute number with a cleaned title and the romaji alias** — `P2` · `S` · Phase 5
  - **Problem:** searchByAbsolute builds its query with fmt.Sprintf("%s %d", s.Title, abs) from the raw title (automation/series.go:257). That bypasses indexerQuery, so punctuation narrows the results. The interactive anime episode search (series_interactive.go:43-75) sends no season or episode and no absolute-number query, only AliasSearchTerms, which return nothing without a season-pinned alias (alias.go:310-326). With a 400-result limit on a title search, an older episode is usually outside the results.
  - **Approach:** 1. **Pure helper** `absoluteQueries(s series.Series, abs int) []string`. It returns `indexerQuery(s.Title) + " " + pad(abs)`, where pad gives two digits below 100 to match the fansub '- 05' convention. It adds the same for the first romaji or TMDB alias from [SER-12](#ser-12), or for OriginalTitle when it is Latin script. Deduplicated.
    2. **searchByAbsolute** uses it, still bounded by maxAbsoluteQueries per sweep.
    3. **Interactive search.** For anime with season > 0 and episode > 0, searchSeriesScope ([SER-02](#ser-02)) adds absoluteQueries for that episode's absolute number, merged and deduplicated like the alias results.
       - The query list comes from a pure `seriesScopeQueries(s, season, episode, abs int, aliasTerms []string) []indexer.SearchQuery`, so it can be tested without an indexer.
  - **Files:** `internal/automation/series.go`, `internal/automation/series_interactive.go`, `internal/automation/abssearch_test.go`, `internal/automation/query_test.go`
  - **Acceptance:**
    - Searching episode 137 of a long anime from the detail page sends a 'Title 137' query and lists that episode's releases
    - Absolute queries for 'Dr. Stone' go out as 'Dr Stone 13'
    - A show with a TMDB romaji alias also queries 'Sousou no Frieren 13'
  - **Tests:** Go: TestAbsoluteQueriesCleanAndPad (pure); Go: TestSeriesScopeQueriesAnimeEpisode (the pure builder returns the absolute and alias queries; standard shows unchanged)
  - **Depends on:** [SER-02](#ser-02), [SER-12](#ser-12)
  - **Risk:** At most two extra queries per interactive search. The title-cleaning half can ship before SER-12, without the alias term, if SER-12 slips.
  - **Resolves:** series-8

#### Milestone: M4 — A Plex-like series page

_The series page has a hero with the next episode, monitored progress and size, three primary actions plus an overflow menu, a season rail with episode stills and overviews, one menu per episode, and bulk select. The library grid gains sorting, next-airing info and touch-reachable actions._

<a id="ser-14"></a>
- [ ] **SER-14 · Series detail redesign, part 1: hero with next episode and stats, Monitor menu, three primary actions plus overflow** — `P1` · `M` · Phase 10
  - **Problem:** The hero has no next air date, size or episode summary, and the backdrop sits at 18% opacity (SeriesDetail.tsx:119). The toolbar is 9 equal buttons plus the profile select (lines 235-272): monitor, Refresh & rescan, Anime, Auto-grab missing, Search indexers, Upload torrent, Manual import, Rename and Delete. Two expert anime panels sit inline above the episodes. The owner wants a Plex or Overseerr-like page, not an admin console. SeriesDetail.tsx is 1026 lines, which makes every UI task conflict with every other.
  - **Approach:** 1. **Split the file.** Start `web/src/pages/series/`: move Toolbar, ManualImportModal, ProfileSelector, DeleteButton, HistoryPanel, SeriesBlocklistPanel, AliasPanel, SceneMapPanel and DuplicatesPanel into their own files, with no behaviour change. SeriesDetail.tsx becomes the page shell.
    2. **Hero (series/Hero.tsx).**
       - A full-bleed backdrop at about 0.45 opacity, with the existing gradient to var(--bg), plus the poster and title.
       - Chips for status (fresh after [SER-06](#ser-06)), network and year.
       - 'Next: S03E05 · Thu 9 Oct' from `stats.next_air_date` ([SER-10](#ser-10)), with the episode label from the loaded seasons.
       - '52/60 monitored · 210 GB' from stats, plus the overview and genres.
    3. **Primary actions (series/HeroActions.tsx).**
       - A Monitor dropdown: on/off ([SER-08](#ser-08) gate), the presets ([SER-09](#ser-09)) and a 'Monitor new seasons' toggle. The quality profile select sits beside it.
       - 'Search missing' as the single accent button (POST /search).
       - 'Choose release…', which opens the whole-show modal.
       - An overflow menu: Refresh & rescan, Rename… ([SER-04](#ser-04) RenameModal), Manual import, Upload torrent, 'Numbering: Standard / Anime', and Delete….
    4. **Menu component.** Use the FE kit's Menu if it exists by then. Otherwise build a small `web/src/components/Menu.tsx`:
       - button plus popover, arrow-key navigation, Esc and click-outside close, touch support;
       - it uses the existing tokens (--panel, --line, --accent, --shadow) and the current type scale;
       - it is reused by [SER-15](#ser-15), [SER-16](#ser-16) and [SER-17](#ser-17).
    5. **Move the expert panels.** AliasPanel and SceneMapPanel move from above the episodes to collapsed sections below them, until [SER-20](#ser-20)'s Numbering tab takes them. [SER-07](#ser-07)'s numbering banner stays above the episodes.
  - **Files:** `web/src/pages/SeriesDetail.tsx`, `web/src/pages/series/Hero.tsx`, `web/src/pages/series/HeroActions.tsx`, `web/src/pages/series/panels.tsx`, `web/src/components/Menu.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - The hero shows the next episode, monitored progress and size, and the backdrop is clearly visible in the dark theme
    - At most three primary buttons plus the overflow menu are visible, and every previous action can be reached from the menu, by keyboard and on a 375px phone
    - The visual style is unchanged: warm dark palette, terracotta accent, current type scale
    - No behaviour regressions in the moved panels
  - **Tests:** UI check at desktop width and 375px; UI check: the keyboard opens and closes the overflow menu, and each item performs its old action; npm run build (tsc --noEmit) passes
  - **Depends on:** [SER-04](#ser-04), [SER-09](#ser-09), [SER-10](#ser-10), FE
  - **Risk:** This overlaps with FE's component kit. Prefer the shared component if it lands first, and keep the local Menu API-compatible so swapping it later is mechanical. The file split should land as its own commit so later tasks rebase cleanly.
  - **Resolves:** series-12
<a id="ser-15"></a>
- [ ] **SER-15 · Series detail redesign, part 2: season rail and episode cards with stills, overviews and one menu per episode** — `P1` · `M` · Phase 10
  - **Problem:** SeriesDetail never renders episode still_url, overview or runtime, or season posters and names, although the API returns all of them (series/series.go:90-127). Every season, the current one included, starts collapsed (defaultOpen={false}, line 170). Each episode row has up to four small controls: the monitor dot, Replace, Delete and Search (lines 447-494).
  - **Approach:** 1. **Season rail (series/SeasonRail.tsx).**
       - A horizontal scroller of season chips with posters (season.poster_url via posterThumb) and have/monitored progress.
       - Default selection: the first season with missing monitored aired episodes, else the latest season with aired episodes. Specials come last.
       - The selection is kept in the URL (?season=).
       - Header actions: Grab missing ([SER-02](#ser-02) rules; hidden on complete seasons and on Specials), Choose release…, and Monitor season.
    2. **Episode cards (series/EpisodeCard.tsx)** for the selected season:
       - a 16:9 still, lazy-loaded with a warm gradient fallback;
       - 'E05 · Title', air date and runtime;
       - a two-line overview that expands on click;
       - a status chip (Downloaded, Missing, Unaired or Not monitored), the Fit chip and inline download progress;
       - the monitored dot doubles as the toggle.
       Only the selected season's cards are mounted. Seasons over 100 episodes render in pages of 50 with 'Show more', plus `content-visibility: auto` on cards (no new dependency).
    3. **One overflow menu per episode** (the Menu from [SER-14](#ser-14)):
       - Search automatically (autograb → GrabForScope);
       - Choose release…;
       - Replace… (confirm);
       - Delete file… (confirm);
       - File details;
       - Monitor/Unmonitor.
       It works on touch.
    4. **Removals.** Remove SeasonBlock and EpisodeRow once the cards replace them, and keep their logic in the new components.
  - **Files:** `web/src/pages/SeriesDetail.tsx`, `web/src/pages/series/SeasonRail.tsx`, `web/src/pages/series/EpisodeCard.tsx`, `web/src/lib/api.ts`, `web/src/lib/img.ts`
  - **Acceptance:**
    - Opening a show lands on the current or first incomplete season, with episode stills and overviews visible without any click
    - Each episode shows one overflow button holding all the previous per-episode actions, and it works on touch
    - A 1000-episode anime renders the selected season without lag
    - The Specials season shows no Grab missing
  - **Tests:** UI check: stills, overview expand and the overflow menu at 375px; UI check: a long anime season scrolls smoothly and 'Show more' pages in; npm run build passes
  - **Depends on:** [SER-02](#ser-02), [SER-14](#ser-14)
  - **Risk:** Long anime seasons need paging, which is part of this task. Stills come from TMDB, so missing stills need a fallback that keeps the warm palette. Keep the season selection in the URL so a reload doesn't jump.
  - **Resolves:** series-12
<a id="ser-16"></a>
- [ ] **SER-16 · Multi-select on the episode cards: bulk monitor, unmonitor and search selected episodes** — `P2` · `S` · Phase 10
  - **Problem:** Monitoring or searching a handful of episodes means one click per episode, and each click on a long show is a separate indexer search. Nothing on the backend accepts a list of episodes.
  - **Approach:** 1. **Backend bulk monitor.** `PUT /api/v1/series/{id}/episodes/monitor {episode_ids:[...], monitored}` (manager only) calls the new `Repo.SetEpisodesMonitored(ctx, seriesID, ids, monitored)`.
       - One transaction.
       - It rejects ids not belonging to the series.
       - It finishes with [SER-09](#ser-09)'s syncSeasonMonitored for the touched seasons.
    2. **Backend bulk search.** The existing `POST /api/v1/series/{id}/search` keeps its no-body behaviour (SearchSeriesNow). With a body `{episodes:[{season, episode}]}` (max 200) it runs `GrabForScope{Episodes: refs, Trigger: "bulk"}` in the background and emits the same 'searched' event and bus outcome as [SER-02](#ser-02).
       - The planner runs Scoped with no pack fallback. Pass 2 can still pick a season pack when the selection covers at least half of a season.
    3. **UI.**
       - A 'Select' toggle in the season rail header turns cards into checkboxes.
       - A sticky bar shows the count, Monitor, Unmonitor, Search and Clear.
       - The selection is per season, and is cleared on season change.
  - **Files:** `internal/httpapi/series.go`, `internal/httpapi/server.go`, `internal/series/repo.go`, `internal/series/service.go`, `internal/automation/series_interactive.go`, `web/src/pages/series/SeasonRail.tsx`, `web/src/pages/series/EpisodeCard.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Selecting 5 episodes and choosing Search starts one scoped search covering exactly those episodes, and one 'searched' event
    - Bulk Monitor of episodes in an unmonitored season makes that season monitored
    - POST /search with no body still runs the whole-show sweep
  - **Tests:** Go: TestSetEpisodesMonitoredBulk (rejects foreign ids; syncs season flags); Go httpapi: TestSearchEpisodeListScope (the handler builds a GrabForScope episode list; no body → SearchSeriesNow); UI check: select mode at 375px
  - **Depends on:** [SER-02](#ser-02), [SER-09](#ser-09), [SER-15](#ser-15)
  - **Risk:** Low. The 200-episode cap and a single planner run keep a bulk search from turning into hundreds of indexer queries.
  - **Resolves:** series-12
<a id="ser-17"></a>
- [ ] **SER-17 · Series library page: sort options in grid view, network and next airing on cards, touch-reachable actions** — `P2` · `S` · Phase 10
  - **Problem:** The grid view is sorted by title only (Series.tsx:64-71). Cards show only year, season count and a status string (lines 368-384). Delete and Search now appear only on group-hover (lines 328-359), so they cannot be reached on touch screens or a TV browser.
  - **Approach:** 1. **Grid sort.** Add a persisted sort select (`usePersisted('series.grid.sort', 'title', [...])`): Title, Recently added (added_at), Next airing (stats.next_air_date, empties last), Size (stats.size_bytes) and Missing (stats.missing).
    2. **Card meta.** 'Network · Next Thu' for continuing shows with next_air_date (a weekday within 7 days, otherwise the short date). Otherwise 'year · N seasons'. Status uses [SER-10](#ser-10)'s monitored-only counts.
    3. **Actions.** Replace the hover-only controls with an overflow button (Menu from [SER-14](#ser-14)) holding 'Search missing' and 'Remove…'.
       - It is always visible under `@media (hover: none)` and on `:focus-within`, and on hover for mouse users.
       - The table view's actions are unchanged.
  - **Files:** `web/src/pages/Series.tsx`
  - **Acceptance:**
    - On a phone, every card's actions can be reached without hover
    - Sorting by Next airing puts the soonest-airing shows first and persists across reloads
    - Cards show network and next airing for continuing shows
  - **Tests:** UI check at 375px and with keyboard focus; UI check: the sort persists after a reload
  - **Depends on:** [SER-10](#ser-10), [SER-14](#ser-14)
  - **Risk:** Low. If FE's shared config-driven Movies/Series grid lands first, apply these options there instead of in Series.tsx.
  - **Resolves:** series-12

#### Milestone: M5 — See what happened and why

_The detail page polls a light endpoint and History refreshes itself. Every Grab and Search shows 'Searching…', 'Grabbed' or 'Nothing found (why)' from the server, on any device. Files, Activity and Numbering tabs give every panel a home._

<a id="ser-18"></a>
- [x] **SER-18 · Detail endpoint: parse the queue once, poll a light downloads endpoint, refresh panels on change, and match RSS before Get()** — `P2` · `S` · Phase 5
  - **Problem:** attachEpisodeDownloads (httpapi/series.go:139-180) calls episodeDownload once per file-less episode, and each call runs parser.Parse over every incomplete queue item. Seeding items are skipped, so the audit's 300k-parses figure is overstated, but the work still scales with episodes × downloads.

Other costs:
- The page re-fetches the full detail every 3 s while anything downloads (SeriesDetail.tsx:86-91).
- History and Duplicates key on s.stats?.have_files, which is always undefined in Get, so they never reload. The blocklist, keyed on s.seasons, refetches on every poll.
- RSSSyncSeries calls Get() for every monitored series before checking any title match (series_reliability.go:62-82).
  - **Approach:** 1. **One pass in attachEpisodeDownloads.**
       - Keep only incomplete items in seriesCategory, and parse each once.
       - Keep items whose title matches the series (seriesIdentity from [SER-11](#ser-11) when present, else titleKey).
       - Build `map[season]→{all bool; eps set}`, then annotate episodes.
       - The parse function is injectable for the test.
    2. **New `GET /api/v1/series/{id}/downloads`** returns `[{season, episode, state, progress}]`.
       - While anything downloads, SeriesDetail polls it every 3 s and merges the result into state.
       - It reloads the full detail once on the useLive topics 'series.imported', 'series.searched' and 'release.grabbed' for this series id.
    3. **Panel refresh.** The detail response gains `last_event_id` (MAX(series_events.id), via a new Repo.LastEventID). The History, Duplicates and Blocklist panels key on it instead of stats or seasons.
    4. **RSSSyncSeries** matches release titles against the List() snapshot, which already carries Aliases and Extra, and calls Get() only for series with at least one match.
  - **Files:** `internal/httpapi/series.go`, `internal/httpapi/server.go`, `internal/series/repo.go`, `internal/series/service.go`, `internal/automation/series_reliability.go`, `web/src/pages/SeriesDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - While a download runs, the page polls only /downloads, and the full detail is fetched once per import
    - After an import, History shows the new 'imported' event without a reload
    - The blocklist panel no longer refetches every 3 s
    - RSS sync calls Get() only for series that have a matching release
  - **Tests:** Go: TestEpisodeDownloadsSinglePass (a pack, an episode and an other-show item in the queue; correct annotations and one parse per item via the injectable parse func); Go: TestRSSSyncSkipsGetForNonMatching (pure matcher over the List snapshot); UI check: the network panel during a download
  - **Depends on:** [SER-10](#ser-10)
  - **Risk:** Low. If FE's useLiveQuery lands first, use it instead of a hand-rolled useLive subscription.
  - **Resolves:** series-13
<a id="ser-19"></a>
- [x] **SER-19 · Server-side search state replaces the localStorage 'Requested' marks** — `P1` · `S` · Phase 5
  - **Problem:** SeriesDetail.tsx:10-48 keeps a 24-hour 'Requested' mark in localStorage, because nothing on the server recorded the request (handleSearchSeries and handleAutoGrabSeries only log, httpapi/series.go:102-116, 456-478). A search that found nothing looks the same as one still running, and the mark exists in only one browser. After SER-02 every user-triggered search produces a GrabOutcome; OBS persists it per scope.
  - **Approach:** 1. **Remove the local marks.** Delete GRAB_KEY, loadGrabMarks, saveGrabMarks, markGrabRequested, clearGrabRequested and the `requested` state from the season header and episode cards/rows, whichever exist when this lands.
    2. **Read `last_search` from the detail response**, keyed by scope ('series', 'S03', 'S03E04'). OBS's persisted search attempts (draft series.t14) attach it in handleGetSeries:
       - 'Searching…' while the attempt has no finished_at;
       - 'Grabbed' when grabbed > 0;
       - 'Nothing found · 14:02', with a popover summarising found, eligible and the top reasons ('37 found · 0 fit: 22 other shows, 15 over ceiling'). Indexer failures read 'Indexers failed — not counted as a miss'.
    3. **Hero line.** Under the hero actions, show 'Last searched 14:02 — 0 of 37 fit', linking to the Activity tab ([SER-20](#ser-20)) or the History panel.
    4. **Live reload.** Subscribe with useLive to 'series.searched' and 'series.imported' for this series id and reload, instead of relying on fixed toasts.
    5. **Interim fallback** if OBS slips: derive last_search from the latest 'searched' series event per scope, which [SER-02](#ser-02) writes, and switch to OBS's table when it lands.
  - **Files:** `web/src/pages/SeriesDetail.tsx`, `web/src/pages/series/EpisodeCard.tsx`, `web/src/pages/series/SeasonRail.tsx`, `web/src/lib/api.ts`, `internal/httpapi/series.go`
  - **Acceptance:**
    - Clicking Grab shows 'Searching…' and then 'Nothing found' with reasons, and this survives a reload and appears on another device
    - No 'arrmada.grabRequested' key is written to localStorage
    - Chips keep the current terracotta and neutral styling
  - **Tests:** Go httpapi: TestGetSeriesAttachesLastSearchByScope; UI check: Grab on an episode with no releases shows the reason popover; a reload keeps it; UI check: localStorage has no grabRequested key after use
  - **Depends on:** [SER-02](#ser-02), OBS
  - **Risk:** Low. It depends on OBS's storage shape; the design section fixes the contract ({scope: {started_at, finished_at, found, eligible, grabbed, reasons, example}}) so both sides can build to it.
  - **Resolves:** series-9
<a id="ser-20"></a>
- [ ] **SER-20 · Series detail redesign, part 3: Files, Activity and Numbering tabs** — `P2` · `M` · Phase 10
  - **Problem:** Duplicates, blocklist, history and the anime numbering panels are stacked below the episodes with no structure. The alias and scene-map panels only existed for anime. Numbering problems (source, pending proposal, files that could not be placed) and search outcomes have no place in the UI.
  - **Approach:** 1. **Tabs under the hero:** Episodes ([SER-15](#ser-15)), Files, Activity and Numbering.
       - The selected tab lives in the URL (?tab=).
       - Use FE's Tabs component if present, otherwise a small local one with the existing tokens.
       - Each tab can ship on its own as its dependency lands.
    2. **Files (series/FilesTab.tsx).** A per-season fit summary (libraryFitEpisodes), DuplicatesPanel, SeriesBlocklistPanel, and [SER-04](#ser-04)'s rename preview shown inline with a Rename… button.
    3. **Activity (series/ActivityTab.tsx).** One newest-first timeline merging series_events (history) and OBS's search attempts, with expandable reason breakdowns and type filters (grabbed, imported, searched, renamed, numbering).
    4. **Numbering (series/NumberingTab.tsx).**
       - Shown for anime, or for any show with a pending proposal ([SER-07](#ser-07)) or unplaced files.
       - Contents: the numbering source badge ([SER-05](#ser-05)), the pending remap review (moved here from [SER-07](#ser-07)'s banner modal), aliases ([SER-12](#ser-12)), the scene map, and files that could not be placed.
       - The unplaced files come from `GET /api/v1/reviews?media_type=series&expected_id={id}`, a new filter in handleListReviews, with a link to Review.
       - The Standard/Anime numbering switch is mirrored here.
  - **Files:** `web/src/pages/SeriesDetail.tsx`, `web/src/pages/series/FilesTab.tsx`, `web/src/pages/series/ActivityTab.tsx`, `web/src/pages/series/NumberingTab.tsx`, `web/src/lib/api.ts`, `internal/httpapi/series.go`, `internal/httpapi/reviews.go`
  - **Acceptance:**
    - Each tab deep-links (?tab=activity) and survives a reload
    - Activity shows grabs, imports and 'nothing found' searches with reasons in one list
    - On a standard show the Numbering tab appears only when there is a pending proposal or unplaced files
    - The review filter returns only this series' held downloads
  - **Tests:** Go httpapi: TestListReviewsFilterByExpectedSeries; UI check: tab state in the URL; UI check: Activity shows a seeded search attempt; UI check: Numbering is hidden on a clean standard show and visible with a seeded pending proposal
  - **Depends on:** [SER-04](#ser-04), [SER-05](#ser-05), [SER-07](#ser-07), [SER-12](#ser-12), [SER-15](#ser-15), [SER-19](#ser-19), OBS
  - **Risk:** The dependency set is wide; ship one tab at a time (Files first, then Numbering, then Activity once OBS is in). Moving panels must not lose their refresh keys (last_event_id from SER-18).
  - **Resolves:** series-12, series-9, series-4

#### Milestone: M6 — More coverage: specials by name, season-pack upgrades

_Specials released by name are found and imported. Finished shows get BluRay season-pack upgrades, absolute-numbered anime can be upgraded, and every season is searched over successive sweeps._

<a id="ser-21"></a>
- [ ] **SER-21 · Find and import specials by episode title** — `P2` · `M` · Phase 14
  - **Problem:** Most specials are released by name ('Show.The.Christmas.Special.1080p'), not as S00Exx. After SER-03 the Specials buttons are safe, but they rarely find anything. A special named only by title can't be imported either: its filename has no numbering, so it ends up in the review queue.
  - **Approach:** 1. **Search.** For a season-0 scope (searchSeriesScope with season 0), also query `indexerQuery(title) + " " + indexerQuery(special.Title)` for each wanted special (within [SER-03](#ser-03)'s 5-query cap).
    2. **Pure matcher** `specialTitleMatch(p parser.Release, name string, ep series.Episode) bool`. It requires:
       - the parsed series title matches (seriesIdentity);
       - the release is not a pack;
       - the words after the series title contain the special's title key (at least 2 significant words, or the whole title when it is shorter).
       It rejects any release that carries an SxxExx with season > 0.
       - releaseMatchesScope for season 0 accepts S00Exx ([SER-03](#ser-03)) or specialTitleMatch.
       - resolvesLabel shows 'S00E05 (by title)'.
    3. **No new column.** [SER-01](#ser-01)'s `grabs.scope` already records the target. GrabForScope and an interactive pick from that special's modal store scope 'S00E05'.
    4. **Import.** In importSeriesInto, when a download has exactly one video, nothing resolves, and grabForce/grab scope names a single S00 episode, place the file on that episode instead of counting it unresolved. [SER-01](#ser-01)'s quality gate still applies unless the grab was manual. Log 'placed by the grab's target episode'.
  - **Files:** `internal/automation/series_interactive.go`, `internal/automation/series.go`, `internal/automation/reviews.go`, `internal/automation/store.go`
  - **Acceptance:**
    - Searching a special titled 'The Christmas Special' lists 'Show.The.Christmas.Special.2019.1080p.WEB' resolving to its S00Exx
    - Grabbing it imports onto that special without a review item
    - A regular-episode release (S01E05) whose episode title contains the special's title is not offered
    - A two-video download with no numbering still goes to review
  - **Tests:** Go: TestSpecialTitleMatch (table: positive, partial-word, regular-episode rejection, pack rejection); Go: TestImportSingleFilePlacesOnGrabScopeSpecial (pure placement decision)
  - **Depends on:** [SER-01](#ser-01), [SER-02](#ser-02), [SER-03](#ser-03)
  - **Risk:** Title matching is fuzzy. Keep it strictly limited to season-0 scopes so regular episodes are never routed by title, and limit target placement to single-video downloads.
  - **Resolves:** series-2
<a id="ser-22"></a>
- [ ] **SER-22 · Series upgrade sweep: per-season search with a cursor, absolute-numbered anime, and season packs that upgrade most of a season** — `P2` · `M` · Phase 14
  - **Problem:** upgradeSeries runs one title query per show with Limit 100 (series_reliability.go:194). It lacks the per-season fan-out and cursor the missing sweep got. It accepts only single-episode releases with a matching SxxExx (episodeRelease, lines 228, 324-334). So long or finished shows, whose upgrades mostly appear as BluRay season packs, are effectively never upgraded. Neither are anime releases numbered absolutely, which have no SxxExx.
  - **Approach:** 1. **Migration** `NNNN_series_upgrade_cursor.sql`: `ALTER TABLE series ADD COLUMN search_upgrade_cursor INTEGER NOT NULL DEFAULT 0`, with Repo get/set helpers next to SearchCursors.
    2. **Per-season search.** After the existing at-ceiling and source-release filters, collect the seasons that have upgradeable episodes. Query them through searchSeasons with a rotation like rotateSeasons on the new cursor, bounded by maxSeasonQueries, keeping the broad title query too.
    3. **Anime resolution.** Resolve single-episode candidates through coveredByFor / ResolveEpisodes instead of episodeRelease, so '[Grp] Show - 137' maps to its (season, episode) for anime.
    4. **Season packs: pure `packUpgradeDecision(pack, haveEps, verdict func(ep) (better, worse bool)) (ok bool, upgraded int)`.**
       - Single-season, fully aired seasons only.
       - UpgradeCandidate runs per covered episode with a file, using QUAL's runtime tagging for the pack (episode runtime × episode count).
       - Accept when at least half of the season's episodes with files would be upgraded and none would be downgraded.
       - The disk guard and pending checks stay as today.
       - Grab via GrabForSeriesAuto (manual=0, scope ''), so the per-episode import gate keeps every episode the pack does not beat.
    5. **Record attempts** with trigger 'upgrade' through the same outcome hook as [SER-02](#ser-02), so OBS can store them.
  - **Files:** `internal/automation/series_reliability.go`, `internal/automation/series.go`, `internal/series/repo.go`, `internal/series/service.go`, `internal/store/migrations/NNNN_series_upgrade_cursor.sql`
  - **Acceptance:**
    - A finished show with 720p WEB-DL files and a 1080p BluRay season pack available gets the pack grabbed when the profile allows the upgrade; on import, only the episodes the pack beats are replaced
    - Over successive sweeps, every season of a 20-season show is searched for upgrades
    - An anime episode released as '[Grp] Show - 137 [1080p]' can be an upgrade
    - A pack that would downgrade any episode, or upgrade fewer than half, is not grabbed
  - **Tests:** Go: TestUpgradePackAcceptedWhenMajorityBetter and TestUpgradePackRejectedForMinorityOrDowngrade (pure decision function); Go: TestUpgradeSeasonRotation; Go: TestUpgradeResolvesAnimeAbsolute; Go: existing upgradetitle_test.go passes
  - **Depends on:** [SER-02](#ser-02), QUAL
  - **Risk:** Packs are large. The half-the-season rule, the disk guard and excluding running seasons must all hold, or the sweep becomes a bandwidth hog. Without QUAL's runtime tagging the bitrate comparison is meaningless for packs, so this task must not ship before it.
  - **Resolves:** series-14

#### Risks

- Import-gate semantics change (SER-01): a wrong GrabScope.Covers() either re-opens the library-wide overwrite or blocks imports the owner chose. Mitigation: table tests in both directions; legacy manual=1 rows keep today's behaviour without a backfill.
- Planner extraction (SER-02) could subtly change what the automatic sweep grabs. Mitigation: the replay test drives packsize/series_tier fixtures through the extracted planner before and after.
- Shows that relied on automatic scheduled renumbering stop changing on their own (SER-05). The owner must press Refresh or apply SER-07's proposal, so the event text must say so explicitly.
- Migration collisions with other epics: every task takes the next free number at implementation time and never edits an applied migration. The series package tests move to store.Open (SER-05) so new columns don't break a hand-rolled schema.
- SeriesDetail.tsx (1026 lines) is touched by SER-01 to SER-20 and overlaps with FE's kit. Mitigation: SER-14 splits it into web/src/pages/series/ in a standalone commit first, and later UI tasks build on that split.
- Indexer load: TMDB alias queries (SER-12), specials text queries (SER-03, SER-21), absolute queries (SER-13) and the upgrade fan-out (SER-22) all add searches. Mitigation: per-feature caps and the existing per-host throttle; RSS no longer calls Get() per show (SER-18).
- Over-strict identity (SER-11) could reject real releases with air years. Mitigation: read the year only before the SxxExx marker, allow ±1, and run the titlemismatch/grabmatch tests as a corpus.
- Two title normalizers (automation titleKey and series normKey) can drift apart. seriesIdentity and MatchRelease must use one consistently; BE may unify them.
- Data safety in testing: rename, renumber and import tests must use t.TempDir fixtures only, never the owner's real library, and race tests must pass in Docker before each push.

#### Out of scope

- series-5 / quality-1: applying the profile's bitrate window and ceiling to TV grabs via runtime tagging (QUAL epic, draft series.t13); SER only consumes it
- series-11: whole-series delete through the recycle bin with 'delete files' off by default (SAFE epic, draft series.t17)
- series-15: the 'set ARRMADA_TMDB_API_KEY and restart' banner copy (COPY epic)
- Request-side season picker, request monitor presets and requests for existing unmonitored shows (REQ epic, draft series.t9); REQ calls SER-09's Add options and ApplyMonitorPreset
- Storage and the global UI for persisted search attempts (OBS); SER only emits outcomes and renders the per-series view
- Narrowing the 'grab still downloading' skip to the seasons an in-flight torrent covers, and dead-torrent handling (ops-3; ACQ/OBS)
- The durable acquisitions table keyed by info hash, and merging Downloads, History and Review into an Activity hub (ACQ)
- A Plex partial scan after rename, renumber or import (PLEX / backend-14)
- A shared config-driven Movies/Series grid component (FE); SER-17 adds options to the current Series grid
- Movie rename preview and versions (MOV); SER-04 only switches movies.Service.Rename to the collision-safe Move

