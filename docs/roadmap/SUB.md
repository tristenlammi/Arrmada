# SUB — Subtitles

_Part of the [Arrmada roadmap](../../ROADMAP.md). 35 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Make the Subtitles module a Bazarr replacement the owner can trust, built around the existing whisper engine. Every wanted language should get the best subtitle that actually exists, tried in order: embedded text, then a scored and sync-checked OpenSubtitles download, then local AI. Coverage should count only the subtitles Plex will actually show as the full track for that exact video. Every source and attempt should be recorded, so failures are visible, retried on a sensible schedule and fixable in one click. The owner should be able to inspect, nudge, replace and choose subtitles for each file, by language and profile.

**Why.** The audit scored Subtitles 5/10. It called the engine great and said the orchestration around it defeats it.
- AI never runs once OpenSubtitles is configured (subtitles-1). bestSource() sends every language without an embedded track to 'download'. A no-match, a provider error or a spent quota then ends the job as 'skipped — nothing produced', and the 6-hourly sweep searches the same files again forever.
- Forced (foreign-parts-only) tracks and .forced.srt files count as the full subtitle (subtitles-2). Bazarr-style .en.hi.srt files are read as Hindi (subtitles-10), so the library reports 'covered' while Plex shows only a few lines.
- After a movie upgrade, the old release's sidecar is credited to the new file, but Plex can't pair it (subtitles-3).
- Non-hash downloads are taken blindly, with no release or fps scoring and no sync check, and the Health column has been a stub since launch (subtitles-4). Redo fetches the same bad file again and redoes every language (subtitles-5).
- In translate mode whisper gets no source language, and untagged English audio fails on turbo-only installs (subtitles-6).
- The queue is a single in-memory FIFO: a new import waits behind the AI backlog, and everything is lost on restart (subtitles-7).
- Failures show up only as console lines (subtitles-8). The provider pill is green whenever three fields are filled in, and a 429 pauses downloads for 24h (subtitles-11).
- There is no interactive search, per-file view, detail-page status or movie search (subtitles-12).
- Languages come from three disagreeing 16-entry tables (subtitles-10). Convert's keep-list is separate and the page copy is stale (subtitles-9).
- Smaller gaps: legacy endpoints (subtitles-13), no Plex refresh (subtitles-14), stale counts after a language change (subtitles-15).
The owner ends up with a module that says everything is fine while Plex shows nothing, the wrong subtitle, or a few forced lines, and the only way to find out why is to read a scrolling log.

**Depends on:** SAFE — recycle-bin rework and the no-hard-delete policy. Sidecars follow their video's removal semantics ([SUB-04](#sub-04)), and retireSidecar follows SAFE's rule when the bin is off ([SUB-10](#sub-10), [SUB-20](#sub-20), [SUB-22](#sub-22), [SUB-27](#sub-27)).; PLEX — the partial-scan hook (plex.Client.RefreshPath, section lookup, Arrmada→Plex path mapping, per-folder debounce). It subscribes to 'subtitles.sidecar_changed' ([SUB-30](#sub-30)).; COPY — the Subtitles stale-copy and Health-stub cleanup (draft subtitles.t26: '(soon) AI', 'SOON' pills, Health '—'). [SUB-19](#sub-19) re-adds a real Health column after it, and [SUB-14](#sub-14) rewrites the Overview card, so coordinate to avoid double edits.; CONV — Convert settings and decide.go ownership. [SUB-32](#sub-32) adds the '@subtitles' sentinel and the 'Same as Subtitles' choice, and [SUB-08](#sub-08) replaces convert/preset.go's language tables.; SEC — the deny-by-default route-walk test. [SUB-16](#sub-16) adds the /api/v1/subtitles/* expectations (all RoleManager). Soft dependency.; OBS — the 'needs you' feed and health registry, which consume the subtitles attention count ([SUB-14](#sub-14)). Soft: a getter is provided if OBS hasn't landed.; FE — component kit: drawer/sheet, chips, picker and image components ([SUB-26](#sub-26), [SUB-27](#sub-27), [SUB-29](#sub-29), [SUB-31](#sub-31)). Soft: these build with existing tokens if the kit isn't there.; INT — the API keys page and Testable catalogue. [SUB-13](#sub-13) marks the OpenSubtitles keys Testable and adds inline credential fields using PUT /api/v1/apikeys/{id}.; BE — job-runner/persistence patterns. [SUB-23](#sub-23) persists the subtitle queue; it's related but not blocked.

#### Design

## Target design

### 1. Principles
- **Keep the engine.** chunks.go, words.go, cues.go, casing.go, sycl.go and stockphrase.go stay as they are. Only language handling around them changes ([SUB-06](#sub-06), [SUB-21](#sub-21), [SUB-35](#sub-35)).
- **A language is never parked because one source failed.** Each rung hands what it couldn't produce to the next.
- **Coverage = what Plex will show as the full subtitle for this exact video.** That means a sidecar paired by base name, in the full or SDH variant. A forced file counts only for a forced want (profiles, [SUB-33](#sub-33)).
- **Arrmada owns only sidecars it wrote, adopted or was told to use** (rows in `subtitle_files`). Automatic upgrades and redos never replace an unowned file.
- **The module never hard-deletes.** Replaced or removed sidecars go through `retireSidecar()`, which moves them to the recycle bin. When the bin is switched off it follows SAFE's app-wide policy. A recycle error aborts the action and keeps the file.
- **Every attempt gets a typed outcome.** The sweep checks backoff before spending an API call or GPU time.

### 2. Pipeline (process → ladder)
```
wants (profile: lang + variant)  −  present (paired sidecars, variant-aware)  =  remaining
remaining → extract   pickFullTrack: non-forced non-SDH text > non-forced SDH text; a forced want → forced text track
          → download  skipped (no Search call) when not configured / auth_failed / quota paused
                       candidates minus blocklist, minus foreign_parts_only / ai_translated
                       hash match → score ≥ subs_min_score → 35..min only if the sync check passes → ≤3 tries
          → AI        audio language = tag, else detected (cached); transcribe (turbo) if it matches,
                       translate→en (large-v3, -l <src|auto>), else impossible / model_missing
each rung → []Outcome{Lang, Variant, Rung, Result, Detail}
```
- **Outcome vocabulary:** ok, no_match, quota, rate_limited, auth_failed, provider_error, below_score, out_of_sync, model_missing, ai_failed, no_audio, impossible, ocr_needed, cancelled.
- **Job state:** done if anything was written; failed if every attempted language ended in an error class; skipped only when nothing was needed or nothing was possible; cancelled when stopped. The note is generated from the outcomes, e.g. 'en: no OpenSubtitles match → AI-generated (turbo, 4.1× realtime)'.
- **Test seams.** Service fields `resolve`, `probe` and `extract` (funcs) and `ai aiRunner` (interface) default to the real implementations. Tests use a fake Provider and a fake aiRunner and never touch real media.

### 3. Sidecar naming and parsing
- **Write:** `<base>.<lang>.srt` (full), `<base>.<lang>.sdh.srt` (SDH source), `<base>.<lang>.forced.srt` (forced). The language tag comes from `langs.SidecarTag` (pt-br, zh-tw…). Writes go to a temp file and are renamed into place.
- **Read:** `langs.ParseSidecarTag(segments) → (lang, variant)`.
  - Trailing qualifiers {forced, sdh, hi, cc, default} are peeled off first. 'hi' counts as a qualifier only when a language comes before it, so .en.hi = English SDH and a bare .hi = Hindi.
  - Unknown tokens are never credited to a language. Only a bare `<base>.srt` counts as wanted[0].
- **Pairing:** movies pair by base name, like TV. A subtitle in a movie folder that pairs with no video there is an **orphan**. Orphans are listed and adoptable, and they block the sweep for their language until the owner decides.
- **Sidecars travel with their video** on upgrade and delete (`library.PairedSidecars`). The exception is a same-base container swap, where the sidecars stay.

### 4. Data model
Migrations take the next free number at implementation time (0090+ today).
- **subtitle_files** ([SUB-11](#sub-11)): one live row per owned sidecar.
  - Columns: media_key ('m:<id>' or 'e:<series>:<s>:<e>'), kind, ids, video_path, path, lang, variant, source (embedded|release|opensubtitles|ai-transcribe|ai-translate|adopted|manual|unknown), embedded_index, provider, provider_file_id, release_name, hash_match, score, model, sync_offset_ms, sync_confidence, locked, created_at, superseded_at.
  - Unique on path WHERE superseded_at IS NULL.
- **subtitle_attempts** ([SUB-11](#sub-11)): history, one row per (run, language, rung). Columns: run_id, media_key, video_path, lang, variant, rung, outcome, detail, at. Pruned at 180 days.
- **subtitle_blocklist** ([SUB-11](#sub-11)): (provider, file_id, media_key) → lang, reason (redo|out_of_sync|manual), at.
- **subtitle_backoff** ([SUB-12](#sub-12)): current state, PK (media_key, lang, variant).
  - Columns: video_path, reasons_json ({rung: outcome}), primary_reason, detail, count, next_retry_at (NULL = only an event can clear it), clear_on (CSV of events: creds|model|langs), updated_at.
- **subtitle_jobs** ([SUB-23](#sub-23)): the persisted queue and history, with a priority and a lane column. Unique on (media_key, lane) for queued and running jobs.
- **subtitle_profiles** and **subtitle_profile_overrides** ([SUB-33](#sub-33)).
- **Probe cache** (info_json, version 2):
  - SubTrack gains Title and SDH.
  - mediaInfo gains ProbeVersion=2 and FPS ([SUB-03](#sub-03)), plus `Detected map[int]{Lang, Prob}` ([SUB-21](#sub-21)).
  - Movies re-probe on the next library pass; episodes re-probe lazily.
- **Settings keys:** subs_min_score (60), subs_upgrade_days (21), subs_upgrade_quota_share (0.3), subs_ai_rtf (EWMA per backend), subs_fast_lane_workers (1), subs_forced_repair_done.

### 5. Queue
- **Priority:** 0 = import hook, 1 = manual, 2 = sweep ([SUB-02](#sub-02)). An import or manual enqueue of a file already queued at sweep priority bumps that job instead of adding a duplicate.
- **Persisted** in subtitle_jobs ([SUB-23](#sub-23)). Running jobs are re-queued on restart, and the sweep runs 10 minutes after boot.
- **Two lanes** ([SUB-25](#sub-25)):
  - 'fast': extract, download and sync check; 1 worker by default.
  - 'gpu': whisper; 1 worker.
  - A fast job hands its leftover languages to a gpu job, and no two jobs run on the same media_key at once.
- **Backoff:** sweep jobs honour it; manual and import jobs bypass it.

### 6. Provider
- **OpenSubtitles states:** not_configured | needs_account | unverified | ok | auth_failed | quota_spent | rate_limited.
  - `Verify()` does a fresh /login and /infos/user.
  - The token is cached with a credential fingerprint.
  - A 406 is a quota pause until reset_time_utc. A 429 is a short Retry-After wait, never a quota pause.
- **Provider interface** gains `Paused() bool`.
- **SearchRequest** gains `ForeignParts` (exclude|only).
- **SubtitleResult** gains FPS, Trusted, FileName, UploaderRank, AITranslated, ForeignPartsOnly and Season/Episode, for scoring.
- **Credentials** are entered only in the app UI: Settings → API keys, or inline on the Subtitles Settings tab, using the same PUT /api/v1/apikeys/{id}.

### 7. Languages
`internal/langs` is a leaf package (no internal imports) holding about 185 ISO 639-1 languages with their 639-2/B, 639-2/T and English names, plus the regional tags pt-br, pt-pt, zh-hans/zh-cn, zh-hant/zh-tw, es-419 and fr-ca.
- API: Canonical, Match, IsToken(strict), Name, ISO1, OpenSubtitles, SidecarTag and ParseSidecarTag.
- Used by subtitles, library/importer.go and convert/preset.go.
- Convert can follow the Subtitles languages with the '@subtitles' sentinel.

### 8. API
All routes are RoleManager. Subtitles reads become staff-only ([SUB-16](#sub-16)).
- **Existing:** GET library, coverage, jobs (now with history paging), logs, settings and models.
- **Redo, retry, history and attention:**
  - POST /subtitles/redo
  - POST /subtitles/retry
  - GET /subtitles/history
  - GET /subtitles/attention
- **Orphans:**
  - POST /subtitles/movies/{id}/orphans/adopt and /orphans/replace
  - POST /subtitles/orphans/bulk
- **Per file:**
  - GET /subtitles/file
  - POST /subtitles/file/shift
  - DELETE /subtitles/file
  - POST /subtitles/file/extract
  - POST /subtitles/sync-check
- **Interactive search:** POST /subtitles/search and POST /subtitles/pick.
- **Profiles:** CRUD on /subtitles/profiles, and PUT /subtitles/overrides/{kind}/{id}.
- **Languages:** GET /api/v1/languages.
- **Removed:** /subtitles/movies, /subtitles/series and the two legacy /search routes.

### 9. UI
All of it keeps the dark warm palette, terracotta accent and current type scale, and reuses the FE kit components where they've landed.
- **Overview** becomes a health board:
  - real provider and AI states;
  - coverage by source;
  - 'needs attention' buckets (auth, quota, model missing, AI failed, out of sync, no match, orphaned) with one-click fixes;
  - the AI backlog with an ETA at the measured realtime factor;
  - a 'Recounting…' state while counts are stale.
- **Library:**
  - movie search, posters and paging;
  - an 'Orphaned' filter with bulk Adopt and Replace;
  - chips that show the fallthrough ('en · download → AI'), when the next try is ('next try in 6d') and Health;
  - row click opens the per-file drawer.
- **Drawer:**
  - sidecars with source, Health, a 3-cue preview, ±0.5/1 s nudges and delete-to-bin;
  - embedded tracks with Extract;
  - Generate with AI;
  - OpenSubtitles candidates with score and reasons, and 'Use this'.
- **Queue:** lanes, priority badges, and persistent history with filters, retry, title links and per-language outcomes.
- **Settings:**
  - inline OpenSubtitles credentials with a Test button and a state line;
  - minimum match score;
  - profile editor;
  - model cards showing 'N files waiting for large-v3'.
- **MovieDetail and SeriesDetail:** per-want chips and a 'Find subtitles' button.

### 10. Rollout
- **[SUB-01](#sub-01) and [SUB-02](#sub-02) ship in the same deploy.** The first sweep after [SUB-01](#sub-01) pushes every previously skipped file into the AI rung, and [SUB-02](#sub-02) keeps imports ahead of that flood.
- **Release notes:**
  - an AI backlog is expected after [SUB-01](#sub-01);
  - orphans appear after [SUB-05](#sub-05), so 'adopt or replace' is needed;
  - a forced-repair count after [SUB-07](#sub-07);
  - the first library pass after [SUB-03](#sub-03) is slower (movies re-probe).
- **Plex check:** verify Plex recognises .sdh and regional sidecar tags with a test file in a test folder, never by touching library media. If Plex doesn't, fall back to plain .<lang>.srt and keep the variant in subtitle_files.
- **Testing:** all tests use synthetic files, httptest servers and ffmpeg lavfi audio (skipped when ffmpeg is absent). Run race tests in Docker before every push.

#### Milestone: M1 — P0: AI fallthrough and truthful coverage

_With OpenSubtitles configured, files with no match, a provider error or a spent quota get an AI subtitle in the same job, and new imports still run ahead of the resulting backlog. Forced tracks are never extracted or counted as the full subtitle, and .en.hi.srt counts as English. Upgrades and deletes take their sidecars along, and a movie counts as covered only when Plex will actually pair the sidecar._

<a id="sub-01"></a>
- [ ] **SUB-01 · Per-language source ladder: fall through extract → OpenSubtitles → AI** — `P0` · `M` · Phase 1
  - **Problem:** Once OpenSubtitles credentials exist, bestSource() (internal/subtitles/library.go:129-144) routes every language without an embedded text track to 'download'. process() fills aiTasks only in the 'ai' case (process.go:62-76).
- A search with no results (grabOne returns false,nil at service.go:333-335), a provider error (process.go:123-124), or ErrQuotaExhausted (goto afterDownloads, process.go:117-122) never moves the language on to whisper.
- The job then ends 'skipped — nothing produced' or 'N pending (OCR/AI)', and the GPU sits idle.
- An empty extracted track (pruned 0-byte output in extractForLangs) is also a dead end.
- While the quota is paused, Search still runs, because only Download checks quotaPaused() (opensubtitles.go:255-258).
  - **Approach:** 1) Resolve the file once into `fileRef{Path, IMDB, Title, Year, Season, Episode, MediaKey, SourceRelease}`. MediaKey uses the Job.key() format. SourceRelease is Movie.SourceRelease, or the episode's SourceRelease from series.Get. Change resolveFile to return it.
    - Add test seams as Service fields that default to the real implementations:
      - `resolve func(ctx, *Job) (fileRef, bool)`;
      - `probe func(ctx, path) (*mediaInfo, error)` (probeCached);
      - `extract func(ctx, path string, picks []extractPick) error` (the ffmpeg call in extractForLangs);
      - `ai aiRunner`, an interface {available() bool; canRun(translate bool) bool; generate(ctx, ffmpeg, video, srt, lang string, translate bool, stream int, progress func(int)) error; Backend() string; Device() string}.
    - *whisperGen implements it; canRun(translate) = modelPath(translate) != ''. Keep s.whisper for WhisperStatus and DownloadModel.
    
    2) Rewrite process() as an explicit ladder:
    ```
    remaining := langs − present
    remaining, outs := s.rungExtract(ctx, ref, mi, remaining)
    remaining, outs2 := s.rungDownload(ctx, ref, remaining)
    outs3 := s.rungAI(ctx, ref, mi, remaining)
    ```
    - Each rung returns the languages it did NOT produce, plus []langOutcome{Lang, Rung, Result, Detail}. This stays in memory for now; [SUB-11](#sub-11) persists it.
    - rungExtract treats a pruned 0-byte output as not produced.
    - Replace the goto with a quotaHit flag. Further downloads are skipped, but the leftover languages still reach AI.
    
    3) Skip the download rung entirely (no Search call) when the provider is nil, !CanDownload(), or paused. Add an optional `interface{ Paused() bool }` that OpenSubtitles implements over quotaPaused(), and have grabOne also check it before Search. Record the outcome 'quota' for the skipped languages.
    
    4) rungAI: for each leftover language, compute aiPlan(audioLangs, l) and audioStreamFor.
    - !ai.available() → 'model_missing: no whisper model installed'.
    - plan == '' → 'impossible: whisper can't translate into <lang>'.
    - !ai.canRun(translate) → 'model_missing: needs large-v3 to translate'.
    - A generate error → 'ai_failed: <tail>'.
    Keep the existing speed and backend log line.
    
    5) Build the note from the outcomes, e.g. 'en: no OpenSubtitles match → AI-generated · es: quota spent → whisper can't translate into es'. It never reads 'nothing produced' when something was attempted.
    - State: Done if anything was written; Skipped when nothing was missing or no rung could act. Keep Failed for 'file is gone'; [SUB-11](#sub-11) refines this.
    - Delete the 'need OCR/AI — coming soon' note at process.go:83.
    
    6) Add `Fallback string` to LangStatus. fillCoverage sets it to 'ai' when the first source is extract or download and AI could produce that language (ai.available() and aiPlan != '').
    - CoverChip (web/src/pages/Subtitles.tsx:699) renders 'en · download → AI'.
    - Update the stale header comments at process.go:13-15 and library.go:126-128.
  - **Files:** `internal/subtitles/process.go`, `internal/subtitles/service.go`, `internal/subtitles/opensubtitles.go`, `internal/subtitles/provider.go`, `internal/subtitles/library.go`, `internal/subtitles/whisper.go`, `internal/subtitles/process_test.go`, `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With OpenSubtitles configured, an English-audio file with no text track and no OpenSubtitles match gets an AI-generated <base>.en.srt in the same job. The job ends Done with a note naming both rungs.
    - While the download quota is paused, a job makes zero /subtitles search requests and goes straight to AI for the missing languages.
    - An HTTP 500 from OpenSubtitles for one language doesn't stop that language from being generated by AI.
    - An embedded text track that extracts to 0 bytes falls through to download, then AI.
    - With no whisper model installed, the note says the AI rung was unavailable and why. It never says 'nothing produced'.
    - The Library chip for a missing language shows 'download → AI' when AI is ready.
  - **Tests:** Go: TestProcessFallsThroughToAIOnNoMatch — fake Provider returns no results; fake aiRunner writes the sidecar; assert the sidecar exists, the state is done and the note names both rungs.; Go: TestProcessQuotaPausedSkipsSearch — fake provider with Paused()=true; Search call count == 0 and the AI rung ran.; Go: TestProcessProviderErrorFallsThrough and TestProcessEmptyExtractFallsThrough (the fake extract writes a 0-byte file).; Go: TestProcessAIUnavailableNote and TestProcessTranslateNeedsLargeV3 (fake canRun(false)).; Go: TestFillCoverageFallback.; Go: the existing cancel_test.go and autofetch_test.go still pass; go test -race in Docker before pushing.
  - **Risk:** The first sweep after deploy pushes every previously skipped file into the single GPU worker. Ship it in the same deploy as SUB-02, so imports stay ahead of the backlog, and say so in the release note; Clear queue still works. More AI runs also expose the missing source language in translate mode more often, so land SUB-06 soon after.

The seams must not change production behaviour: every default func points at the existing code path.
  - **Resolves:** subtitles-1
<a id="sub-02"></a>
- [ ] **SUB-02 · Import and manual jobs jump ahead of the sweep (in-memory queue priority)** — `P0` · `S` · Phase 1
  - **Problem:** pop() takes pending[0] from a single FIFO (internal/subtitles/jobs.go:182-191), and import hooks, manual clicks and the 6-hourly sweep all share it. After SUB-01 the sweep queues hundreds of hour-long AI jobs, so tonight's imported episode would wait days behind them. This rollout guard is required to ship SUB-01 safely; persistence comes later in SUB-23.
  - **Approach:** 1) Job gains `Priority int` (JSON 'priority'): 0 import, 1 manual, 2 sweep.
    - Replace `pending []*Job` with `pending [3][]*Job`, one FIFO per priority.
    - pop() takes from the lowest non-empty bucket.
    - dropPendingLocked, ClearQueue and Pending() iterate every bucket.
    - The history trim in enqueue uses the total pending count.
    
    2) enqueue: when the file's active job is still queued at a higher number, move it to the lower bucket. Keep the existing 'stronger Redo wins' rule.
    
    3) QueueMovie and QueueEpisode gain a `prio` parameter:
    - OnMovieImported and OnSeriesImported → 0;
    - handleSubtitleQueueMovie, handleSubtitleQueueEpisode and QueueSeries (button) → 1;
    - SweepMissing → 2.
    
    4) UI: the Queue tab shows an import/manual/sweep badge on queued rows. sortActive (Subtitles.tsx:241) orders queued rows by priority, then At.
  - **Files:** `internal/subtitles/jobs.go`, `internal/subtitles/service.go`, `internal/httpapi/subtitles.go`, `internal/subtitles/cancel_test.go`, `internal/subtitles/jobs_test.go`, `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With 50 sweep jobs queued, an episode imported now is the next job the worker runs.
    - Pressing Ensure on a file already queued by the sweep moves it to manual priority without creating a duplicate.
    - Clear queue empties every priority bucket, and Pending counts all of them.
  - **Tests:** Go: TestPopPrefersImportOverSweep and TestPopFIFOWithinPriority.; Go: TestEnqueueBumpsPriority (sweep then manual: same job, priority 1).; Go: TestClearQueueClearsAllPriorities; existing never-blocks/dedupe tests in autofetch_test.go and cancel_test.go pass under -race (Docker).
  - **Risk:** Low. A steady stream of imports could starve sweep jobs, which is acceptable because imports are bounded by downloads.
  - **Resolves:** subtitles-7
<a id="sub-03"></a>
- [ ] **SUB-03 · Forced and SDH are variants: detect them, extract the full track, count coverage correctly** — `P0` · `M` · Phase 1
  - **Problem:** Several places treat a forced (foreign-parts-only) subtitle as the full subtitle, so the module reports a file as covered while Plex shows a handful of lines.
- extractForLangs takes the first text track whose language matches and ignores Forced (process.go:239-246). bestSource ignores Forced too (library.go:130-133).
- probeSubs drops subtitle stream titles (probe.go:93-101), so title-only 'Forced' and 'Signs & Songs' tracks go undetected.
- presentLanguages counts <base>.en.forced.srt as English (sidecar.go:99), and sidecar_test.go:49-50 locks this in.
- langTokenFromSegments checks the last segment first, so Bazarr-style <base>.en.hi.srt reads as Hindi; English stays missing and a duplicate is fetched.
- OpenSubtitles Search never excludes foreign_parts_only or ai_translated uploads (opensubtitles.go:178-198).
  - **Approach:** 1) probe.go:
    - SubTrack gains Title and SDH. Parse disposition.hearing_impaired.
    - Forced = disposition.forced, or a title matching `(?i)\b(forced|signs( ?(&|and) ?songs)?|foreign parts?)\b`.
    - SDH = hearing_impaired, or a title matching `(?i)\b(sdh|cc|hearing.impaired)\b`. Apply both regexes to subtitle streams only.
    - In the same version bump, also parse the first video stream's r_frame_rate (avg_frame_rate fallback) into mediaInfo.FPS, so [SUB-17](#sub-17) never needs a second re-probe.
    - mediaInfo gains ProbeVersion; new probes write 2. probeCached re-probes entries below 2, like the existing missing-Audio refresh (probe.go:157-160).
    - Correction to the draft: only movies re-probe on the next library pass, because Library('movies') probes and SeriesGroups doesn't. Episodes re-probe lazily when a show is opened or a job runs.
    
    2) Add pickFullTrack(subs, lang) (SubTrack, bool). Order: non-forced non-SDH text, then non-forced SDH text; default disposition breaks ties. bestSource and extractForLangs use it. A language with only forced text tracks falls through to download or AI via the [SUB-01](#sub-01) ladder.
    
    3) In the same ffmpeg pass, and only when an extraction is already running, also map the first forced text track for each wanted language to <base>.<lang>.forced.srt if that file doesn't exist. This preserves the forced flag for Plex. An SDH source writes <base>.<lang>.sdh.srt (sidecarPathV(media, lang, variant)).
    
    4) sidecar.go: replace langTokenFromSegments with parseSidecarTag(segments) (lang, variant).
    - Peel trailing qualifiers {forced, sdh, hi, cc, default}. 'hi' is a qualifier only when the segment before it is a language.
    - presentLanguages counts only full and SDH variants for a full want.
    - Add presentVariants(path) []LangVariant for [SUB-33](#sub-33) and the drawer.
    - Write the parser as a pure function over an isLang predicate, so [SUB-08](#sub-08) can move it into internal/langs unchanged.
    
    5) Fix sidecar_test.go: '.en.forced.srt still counts' becomes not-present. Add cases .en.hi.srt → en, .hi.srt → hi, .en.sdh.srt → en, and .en.forced.srt + .en.srt → en.
    
    6) opensubtitles.go Search:
    - Send foreign_parts_only=exclude and ai_translated=exclude. SearchRequest gains ForeignParts ('exclude' default, 'only' for [SUB-33](#sub-33)'s forced wants).
    - Parse the foreign_parts_only, ai_translated and machine_translated attributes and drop flagged results defensively. Per the verify note, the API already excludes machine_translated by default.
    - A downloaded HearingImpaired result is written as .<lang>.sdh.srt.
  - **Files:** `internal/subtitles/probe.go`, `internal/subtitles/probe_test.go`, `internal/subtitles/process.go`, `internal/subtitles/library.go`, `internal/subtitles/library_test.go`, `internal/subtitles/sidecar.go`, `internal/subtitles/sidecar_test.go`, `internal/subtitles/opensubtitles.go`, `internal/subtitles/provider.go`, `internal/subtitles/service.go`
  - **Acceptance:**
    - A file with subtitle streams [eng forced 'English (Forced)', eng 'English', eng 'English SDH'] produces <base>.en.srt from stream 1 and <base>.en.forced.srt from stream 0.
    - A file whose only English text track is SDH produces <base>.en.sdh.srt, which counts as English coverage.
    - A folder holding only <base>.en.forced.srt shows English as missing in the Library and Overview.
    - <base>.en.hi.srt counts as English coverage, not Hindi; a bare <base>.hi.srt is Hindi.
    - OpenSubtitles search requests include foreign_parts_only=exclude and ai_translated=exclude, and flagged results are dropped.
    - After deploy the next library pass re-probes movies once (ProbeVersion 2); later passes hit the cache.
  - **Tests:** Go: TestPickFullTrack table — forced-first, SDH-only, title-only forced ('Signs & Songs'), untagged and default-disposition cases.; Go: TestProbeParsesSubtitleTitlesDispositionsAndFPS on a synthetic ffprobe JSON fixture (factor the parse out of probeSubs so no real media is needed).; Go: TestProbeCachedReprobesOldVersion.; Go: updated TestPresentLanguages and new TestParseSidecarTag (qualifier and Hindi cases).; Go: TestSearchExcludesForeignPartsAndAI — httptest server asserts the query params; flagged results filtered.; Go: TestExtractAlsoWritesForced (fake extract seam asserts the picks).
  - **Depends on:** [SUB-01](#sub-01)
  - **Risk:** The re-probe makes the first library pass after deploy slower (movies only). Title regexes could misfire on odd track names; keep them word-bounded and limited to subtitle streams. Check that Plex labels .sdh.srt using a test file in a test folder; if it doesn't, write SDH as plain .<lang>.srt and keep the variant only in subtitle_files (SUB-11).
  - **Resolves:** subtitles-2, subtitles-10
<a id="sub-04"></a>
- [ ] **SUB-04 · Sidecars travel with their video on movie/episode upgrade and delete** — `P0` · `S` · Phase 1
  - **Problem:** On a movie upgrade, markImported calls removeFile(target.FilePath) (internal/movies/service.go:419). That moves only the video, so the old release's 'Old Name.en.srt' stays behind, and the os.Remove(dir) cleanup fails because the folder isn't empty.
- Delete (355), DeleteVersion (784), DeleteVersionFile (803) and DeleteFile (968) behave the same way.
- series DeleteEpisodeFile (series/service.go:61) and SupersedeEpisodeFile (961) also leave the old episode's sidecars behind.
- MoveEpisodeSubs (importer.go:840-861) is only used by Rename.
  - **Approach:** 1) New internal/library/sidecars.go:
    - PairedSidecars(videoPath) []string: subtitle files (subtitleExts, including .idx and .sub) in the same folder whose stem equals the video base or starts with base+'.', case-insensitive.
    - SharesBase(videoPath) bool: true when another video file in the folder has the same base, i.e. a container swap such as X.mp4 → X.mkv.
    - Refactor MoveEpisodeSubs onto PairedSidecars.
    
    2) movies.Service.removeFile(path): unless SharesBase(path), each paired sidecar shares the video's fate.
    - Recycle via library.RecycleFile into the same bin folder. RecycleFile keeps the parent folder name and writes .arrmeta, so the sidecars sit next to the old video and restore works.
    - If the video was hard-deleted (bin off, or a recycle error that already triggered the existing hard-delete fallback), delete its sidecars too.
    - Then run the existing os.Remove(dir).
    This covers upgrade, Delete, DeleteVersion, DeleteVersionFile and DeleteFile.
    
    3) series.Service: DeleteEpisodeFile and SupersedeEpisodeFile do the same, and only when the old file is actually removed. They skip it when EpisodesSharingPath > 0, i.e. a sibling still uses the file.
    
    4) No subtitles-module changes. The subtitle import hook for the new file then sees the language as missing and runs the ladder.
  - **Files:** `internal/library/sidecars.go`, `internal/library/sidecars_test.go`, `internal/library/importer.go`, `internal/movies/service.go`, `internal/movies/service_test.go`, `internal/series/service.go`, `internal/series/service_test.go`
  - **Acceptance:**
    - Upgrading 'Dune (2021) - WEBDL-1080p.mkv' to 'Dune (2021) - Bluray-2160p.mkv' moves 'Dune (2021) - WEBDL-1080p.en.srt' into the recycle bin next to the old video. The subtitle import hook for the new file then queues a real job instead of 'all kept languages already have subtitles'.
    - Deleting a movie file or version leaves no orphaned sidecars, and the empty folder is removed.
    - A same-base container swap (X.mp4 replaced by X.mkv) keeps X.en.srt in place.
    - Superseding one half of a double-episode file that a sibling still uses keeps the sidecars.
  - **Tests:** Go: TestPairedSidecars (case-insensitive; 'Movie 2.srt' next to 'Movie.mkv' is not matched; .idx/.sub included).; Go: TestRemoveFileRecyclesPairedSidecars (temp library and recycle dir; .arrmeta written per sidecar).; Go: TestRemoveFileKeepsSidecarsOnSameBaseSwap.; Go: TestSupersedeEpisodeFileRecyclesSidecars and TestSupersedeKeepsSidecarsWhenShared.; Go: the existing MoveEpisodeSubs and rename tests stay green.
  - **Depends on:** SAFE — recycle-bin rework / no-hard-delete policy (sidecars mirror the video's fate; follow whatever SAFE decides for videos)
  - **Risk:** With the recycle bin off, sidecars are hard-deleted together with the video, which mirrors today's video behaviour until SAFE removes hard deletes. A wrong prefix match could recycle a neighbour's subtitle; the base+'.' rule and the tests guard this.
  - **Resolves:** subtitles-3
<a id="sub-05"></a>
- [ ] **SUB-05 · Movie coverage pairs sidecars by base name; unpaired ones are orphans, not coverage** — `P0` · `M` · Phase 1
  - **Problem:** presentLanguages(singleFolder=true) credits any subtitle in a movie folder to the movie (sidecar.go:101-102), and an untagged one goes to wanted[0] (sidecar.go:113-114). Plex only pairs sidecars that share the video's base name.
- After an upgrade or rename, the module says 'covered' while Plex shows nothing.
- Multi-version folders (1080p and 4K side by side) credit each other's sidecars.
- MovieDetail lists every subtitle in the folder (movies/service.go:941-956).
  - **Approach:** 1) Replace presentLanguages(path, wanted, singleFolder) with scanSidecars(videoPath, wanted, kind). It does a single ReadDir and returns {Present []string, Variants []LangVariant, Orphans []Orphan}.
    - Pairing is by base name for movies and TV alike.
    - Orphans are computed only for movies: subtitle files whose stem pairs with no video in the folder. Video extensions: mkv, mp4, m4v, avi, ts, m2ts, mov, wmv, webm. Each Orphan is {Name, Lang, Variant}, with Lang parsed by parseSidecarTag; untagged orphans have Lang ''.
    - Update the call sites: process.go:35, library.go fillCoverage:109, jobs.go SweepMissing:352 and missingEpisodes, snapshot.go groupFor, and service.go MovieStatuses/GrabMovie (until [SUB-16](#sub-16) deletes them).
    
    2) FileSubs gains Orphans. LangStatus gains Orphan bool, set when the language is missing but an orphan covers it; an untagged orphan covers wanted[0], mirroring today's tolerance. CoverChip shows 'en · orphaned'.
    
    3) SweepMissing skips (movie, lang) pairs covered by an orphan, and skips the movie when nothing else is missing. This keeps the 6-hourly sweep from mass-regenerating subtitles the owner may want to keep. The import hook and manual Ensure still run.
    
    4) movies: sidecarSubtitles(path) returns paired files only. MovieFile gains OrphanSubtitles []string (movie.go:88). MovieDetail.tsx (770-777) lists orphans greyed as 'not linked to this file'; [SUB-10](#sub-10) adds the Adopt button.
    
    5) Update sidecar_test: 'differently-named .srt still counts (renamed video)' becomes an orphan case.
  - **Files:** `internal/subtitles/sidecar.go`, `internal/subtitles/sidecar_test.go`, `internal/subtitles/library.go`, `internal/subtitles/jobs.go`, `internal/subtitles/process.go`, `internal/subtitles/snapshot.go`, `internal/subtitles/service.go`, `internal/movies/service.go`, `internal/movies/movie.go`, `web/src/lib/api.ts`, `web/src/pages/Subtitles.tsx`, `web/src/pages/MovieDetail.tsx`
  - **Acceptance:**
    - A folder holding 'Anchorman (2004) Bluray-2160p.mkv' and 'Anchorman.eng.srt' shows English as missing, the chip reads 'en · orphaned', and the orphan is listed.
    - In a folder with 1080p and 4K versions, one version's sidecars no longer count as coverage for the other.
    - The 6-hourly sweep doesn't queue movies whose only gap is a language covered by an orphan, but a manual Ensure still runs.
    - MovieDetail's subtitle list shows only the file's paired subtitles, with orphans listed separately and greyed.
  - **Tests:** Go: TestScanSidecarsPairsByBase, TestOrphanSidecars (multi-version folder, untagged orphan) and the updated TestPresentLanguages.; Go: TestSweepSkipsOrphanedLanguage and TestManualEnsureIgnoresOrphan.; Go: TestSidecarSubtitlesPairedOnly in internal/movies.; UI check: orphan chip and MovieDetail orphan list, in both themes.
  - **Depends on:** [SUB-03](#sub-03), [SUB-04](#sub-04)
  - **Risk:** Libraries that relied on the old tolerance will suddenly show orphans and lower coverage, so explain this in the release note and point to SUB-10's Adopt all. Sequence after SUB-03, since both rewrite presentLanguages, and after SUB-04, so upgrades stop creating new orphans before the tolerance goes away.
  - **Resolves:** subtitles-3

#### Milestone: M2 — Follow-through: AI source language, damaged files, release subs, orphans

_Whisper translates with the right source language. Files that were already mis-extracted get repaired. One shared language table stops unknown sidecars from being credited to English. Release forced and SDH subtitles keep their qualifiers. The owner can adopt or replace orphaned movie subtitles, one at a time or in bulk._

<a id="sub-06"></a>
- [ ] **SUB-06 · Whisper translate mode passes the audio's source language (-l src|auto)** — `P1` · `S` · Phase 12
  - **Problem:** In translate mode, args() adds --translate with no -l (whisper.go:451-457), and whisper-cli then defaults to 'en'. Foreign audio is decoded under an English language token. The aiPlan comment claims an 'auto-detect + translate' that doesn't exist (whisper.go:545-561). SUB-01 makes this path run much more often.
  - **Approach:** 1) generate() gains a srcLang parameter: the chosen audio track's language, which process.go already has as track.Lang from audioStreamFor.
    - args(model, wav, out, lang, srcLang, translate, dtw): in translate mode append `-l <iso1(srcLang)>` when the language is known, and `-l auto` otherwise. Transcribe mode is unchanged.
    - Debug-log the final args.
    
    2) iso1(): invert twoToThree, plus the T/B variants fra/fre, deu/ger, nld/dut and zho/chi, and full names. [SUB-08](#sub-08) later replaces this with langs.ISO1.
    
    3) Fix the aiPlan comment. Untagged audio still maps to 'translate' until [SUB-21](#sub-21) adds detection.
    
    4) Replace the 'Get turbo for English' line (Subtitles.tsx:856) with accurate copy: turbo transcribes audio that is already in the wanted language; large-v3 is needed to translate foreign audio into English.
  - **Files:** `internal/subtitles/whisper.go`, `internal/subtitles/whisper_test.go`, `internal/subtitles/process.go`, `web/src/pages/Subtitles.tsx`
  - **Acceptance:**
    - A film whose audio is tagged 'fre' is translated with `-l fr --translate`, visible in the debug-logged args.
    - An untagged-audio translate run uses `-l auto`.
    - Transcribe runs are byte-identical in their args to today's.
  - **Tests:** Go: TestArgsTranslatePassesSourceLang, TestArgsTranslateUnknownUsesAuto and TestArgsTranscribeUnchanged.; Go: TestISO1Mapping (eng/fre/fra/ger/deu/chi/zho/full names).
  - **Depends on:** [SUB-01](#sub-01)
  - **Risk:** Check `-l xx --translate` against the pinned whisper.cpp build (≥1.9.2). Don't introduce -ml, -sow or --vad (known to break output).
  - **Resolves:** subtitles-6
<a id="sub-07"></a>
- [ ] **SUB-07 · One-shot repair of sidecars that were extracted from a forced track** — `P1` · `S` · Phase 12
  - **Problem:** Before SUB-03, extractForLangs wrote the first matching text track as <base>.<lang>.srt even when that track was forced. Files already processed carry a few-line forced subtitle as their 'full' English, and the module considers them covered.
  - **Approach:** 1) Guard the pass with the settings key subs_forced_repair_done. It starts after the first library pass completes following deploy.
    
    2) Find candidates cheaply: for every movie and episode with a <base>.<lang>.srt for a kept language, read the sidecar (small) and count cues with parseSRT (cues.go). Only sidecars with fewer than 80 cues are suspects. This avoids probing 23k episodes.
    
    3) For each suspect, probe it (cached or a fresh ProbeVersion-2 probe). The bug is confirmed when the first langMatches text track for that language is Forced and pickFullTrack finds a non-forced track.
    
    4) Queue confirmed files as sweep-priority (2) jobs with Job.Mode 'repair-forced', so they're visible and cancellable. process() runs a dedicated branch:
    - re-extract the full track to a temp file;
    - if the existing sidecar has fewer than a third of the new file's cues, rename the existing one to <base>.<lang>.forced.srt (unless that already exists, in which case recycle it via the bin like the video's removal semantics) and rename the new one into place;
    - never touch the video.
    
    5) Log the count ('forced-repair: fixed N of M suspects') and set the guard key when the pass finishes, even if some failed (the failures are logged).
  - **Files:** `internal/subtitles/repair.go`, `internal/subtitles/repair_test.go`, `internal/subtitles/process.go`, `internal/subtitles/jobs.go`, `internal/subtitles/service.go`
  - **Acceptance:**
    - After deploy, the repair pass logs how many mis-extracted files it fixed.
    - Those files end up with the full track as <base>.<lang>.srt and the forced one as <base>.<lang>.forced.srt.
    - A sidecar with a normal cue count is never probed or rewritten by the pass.
    - The pass runs once; a restart after completion doesn't run it again.
  - **Tests:** Go: TestForcedRepairSwapsShortSidecar (temp files, fake extract seam produces a 900-cue SRT next to a 12-cue existing one).; Go: TestForcedRepairSkipsNormalSidecar and TestForcedRepairRunsOnce (guard key).
  - **Depends on:** [SUB-03](#sub-03), [SUB-02](#sub-02)
  - **Risk:** It reads whole video files from the spinning array for confirmed suspects only. It rewrites sidecars, so it must write to a temp file and rename. A downloaded subtitle that happens to be short, next to a forced-first file, could be swapped; the one-third cue ratio and the probe confirmation make that unlikely, and the old file is kept as .forced.srt rather than deleted.
  - **Resolves:** subtitles-2
<a id="sub-08"></a>
- [ ] **SUB-08 · One shared language table (internal/langs) for Subtitles, the importer and Convert** — `P1` · `M` · Phase 12
  - **Problem:** Language matching is split across three hard-coded tables that disagree.
- Subtitles' twoToThree has 16 languages with B-codes only (library.go:146-162), so the MP4 tags 'fra', 'deu', 'zho' and 'nld' never match fr/de/zh/nl.
- knownLangs is built from that table (sidecar.go:18-36). Sidecars such as X.da.srt or X.pt-br.srt are therefore unrecognised and credited to wanted[0] (sidecar.go:113-114), which marks English as covered.
- The importer has its own langNames table without sv/pl/tr/hi (importer.go:1256-1269).
- Convert has a third one (internal/convert/preset.go:34-65).
  - **Approach:** 1) Create the leaf package internal/langs (no internal imports). The committed table_gen.go is generated by a small generator under internal/langs/gen from a vendored ISO 639 TSV; the build never touches the network. It holds about 185 ISO 639-1 languages with their 639-2/B, 639-2/T and English names, plus the regional tags pt-br, pt-pt, zh-hans/zh-cn, zh-hant/zh-tw, es-419, es-mx and fr-ca.
    - Canonical(tok): fre/fra/french → fr; pt-BR/pob/pt_br/brazilian → pt-br.
    - Match(trackTag, want): a base want matches regional tracks. A regional want matches the same region, or an untagged-region track only when nothing better exists.
    - IsToken(seg, strict): strict mode accepts only 3-letter codes and full names, for free-text sniffing.
    - Name(code), ISO1(code) for whisper -l, OpenSubtitles(code) (pt → pt-pt, pt-br → pt-br, zh → zh-cn, zh-tw → zh-tw) and SidecarTag(code).
    - ParseSidecarTag(segments): moved verbatim from [SUB-03](#sub-03).
    
    2) Replace the old tables:
    - subtitles: twoToThree, langAliases, knownLangs, normLang, langMatches and [SUB-06](#sub-06)'s iso1;
    - importer: langNames and detectLang;
    - convert: twoToThree, biblioToTerm and normLang.
    Keep each package's semantics; Convert still keeps untagged tracks.
    - Important: detectLang sniffs tokens anywhere in a free-form name. With 185 two-letter codes, words such as 'it', 'is', 'to', 'be' and 'no' would match, so detectLang must use IsToken(strict).
    
    3) Sidecar parsing: a non-language token is no longer credited to wanted[0]. Only a bare <base>.srt is.
    
    4) OpenSubtitles Search sends langs.OpenSubtitles(code). SetSettings canonicalizes the codes it is given and rejects unknown ones with 400 (UI picker in [SUB-31](#sub-31)).
  - **Files:** `internal/langs/langs.go`, `internal/langs/table_gen.go`, `internal/langs/gen/main.go`, `internal/langs/gen/iso639.tsv`, `internal/langs/langs_test.go`, `internal/subtitles/library.go`, `internal/subtitles/sidecar.go`, `internal/subtitles/sidecar_test.go`, `internal/subtitles/opensubtitles.go`, `internal/subtitles/whisper.go`, `internal/subtitles/service.go`, `internal/library/importer.go`, `internal/library/importer_test.go`, `internal/convert/preset.go`
  - **Acceptance:**
    - An embedded track tagged 'fra' (or 'deu', 'zho', 'nld') is extracted when fr (de, zh, nl) is wanted.
    - A Danish X.da.srt sidecar no longer marks English as covered.
    - Wanting pt-br searches OpenSubtitles with languages=pt-br and writes <base>.pt-br.srt, which counts as pt-br coverage.
    - Release subtitles in sv/pl/tr/hi are imported with their language tag, while a subtitle named 'Its.A.Trap.srt' in a release folder is not tagged Italian.
    - Convert's existing language-filter tests pass unchanged.
  - **Tests:** Go: langs_test covering Canonical on B/T/name/alias/regional forms, the Match matrix, IsToken strict vs loose, ISO1 and OpenSubtitles mapping, and ParseSidecarTag.; Go: presentLanguages cases for da, pt-br and zh-tw sidecars; importer detectLang false-positive cases; the convert preset tests unchanged.; Go: TestSetSettingsRejectsUnknownLanguage.
  - **Depends on:** [SUB-03](#sub-03)
  - **Risk:** Plex's recognition of regional sidecar tags varies. Test with a file in a test folder; if .pt-br.srt isn't picked up, write .pt.srt and keep the region only in subtitle_files. Importer naming changes for languages that were previously unknown. Coordinate with CONV so convert/preset.go edits don't collide.
  - **Resolves:** subtitles-10
<a id="sub-09"></a>
- [ ] **SUB-09 · Keep .forced/.sdh/.hi qualifiers when importing release subtitles** — `P1` · `S` · Phase 12
  - **Problem:** The importer drops subtitle qualifiers. subLangFor cuts 'en.forced' to 'en' (internal/library/importer.go:1396-1406), and placeSub writes <base>.en<ext>. WalkDir runs in lexical order, so with both Movie.en.forced.srt and Movie.en.srt in a release, the forced file takes <base>.en.srt and the full one is pushed to <base>.en.2.srt. Plex and the module then treat the forced file as the main English subtitle.
  - **Approach:** 1) subLangFor(stem, srcBase, ownFolder) returns (lang, variant, ok), using langs.ParseSidecarTag on the segments after the base.
    - For own-folder names, also sniff the words 'forced' and 'sdh'/'hi'/'cc' (e.g. '2_English_Forced.srt' → en/forced).
    - Language sniffing uses langs.IsToken(strict).
    
    2) placeSub(src, targetBase, lang, variant) builds <base>.<lang>[.forced|.sdh]<ext>, keeping its existing already-imported and numbering logic.
    
    3) importSidecarSubs and importPackSubs collect candidates first, then place them sorted with full before SDH before forced. Ordering can then never put a forced file on the plain name.
    
    4) Size heuristic in importPackSubs and importSidecarSubs: when one release folder has two or more same-language subtitles with no qualifier, and one is under 25% of the largest's size, mark the smaller one forced.
  - **Files:** `internal/library/importer.go`, `internal/library/importer_test.go`
  - **Acceptance:**
    - A release folder with Movie.en.forced.srt and Movie.en.srt imports as <target>.en.forced.srt and <target>.en.srt.
    - A season-pack Subs/ folder with 2_English.srt (12 KB) and 3_English.srt (80 KB) imports as <ep>.en.forced.srt and <ep>.en.srt.
    - A release <base>.en.sdh.srt keeps .sdh, and <base>.en.hi.srt is imported as English SDH (<target>.en.sdh.srt).
  - **Tests:** Go: TestSubLangForQualifiers table test.; Go: TestImportSidecarSubsKeepsForced (temp dir with both files; assert the target names regardless of WalkDir order).; Go: TestImportPackSubsSizeHeuristic.
  - **Depends on:** [SUB-08](#sub-08), [SUB-03](#sub-03)
  - **Risk:** The size heuristic could misclassify a short special's full subtitle. It applies only when a much larger subtitle in the same language sits beside it.
  - **Resolves:** subtitles-2, subtitles-10
<a id="sub-10"></a>
- [ ] **SUB-10 · Adopt or replace orphaned movie subtitles (single and bulk)** — `P1` · `S` · Phase 12
  - **Problem:** After SUB-05, subtitles left under an old name are correctly treated as orphans: not coverage, and skipped by the sweep. The owner still needs a way to decide for each one, either keep it (rename it to pair with the video) or replace it with a fresh subtitle, without renaming files by hand.
  - **Approach:** 1) The subtitles Service gains SetRecycleDir(dir), wired in cmd/arrmada/main.go next to seriesSvc.SetRecycleDir.
    - Add a helper retireSidecar(path) error: library.RecycleFile. On ErrRecycleDisabled, follow SAFE's policy (today: delete). On any other error, return it and keep the file.
    - Every later task that replaces or removes a sidecar uses this helper.
    
    2) POST /api/v1/subtitles/movies/{id}/orphans/adopt {name, lang?, variant?} (RoleManager):
    - name must be one of scanSidecars(...).Orphans for that movie's MovieFilePath, never a client-supplied path;
    - lang defaults to the orphan's parsed language, or wanted[0] when it is untagged;
    - the target is sidecarPathV(video, lang, variant); if it exists → 409;
    - os.Rename within the folder, then refreshSnapshot.
    
    3) POST /api/v1/subtitles/movies/{id}/orphans/replace {name}: queue a manual-priority job for that language with Job.RetireOnSuccess=[name]. process() retires the orphan only if the language was produced.
    
    4) POST /api/v1/subtitles/orphans/bulk {action: 'adopt'|'replace', movie_ids?: []} works over the snapshot's movies with orphans and returns {done, skipped, errors}. Adopt skips orphans whose target exists or whose language is ambiguous (several orphans in one language).
    
    5) UI:
    - an 'Orphaned' filter pill in the movie Library, with 'Adopt all' and 'Replace all' (confirm text states counts and that replaced files go to the recycle bin);
    - per-row Adopt and Replace on orphan chips;
    - an Adopt button on MovieDetail's orphan list.
  - **Files:** `internal/subtitles/orphans.go`, `internal/subtitles/orphans_test.go`, `internal/subtitles/service.go`, `internal/subtitles/process.go`, `internal/subtitles/jobs.go`, `internal/httpapi/subtitles.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`, `web/src/lib/api.ts`, `web/src/pages/Subtitles.tsx`, `web/src/pages/MovieDetail.tsx`
  - **Acceptance:**
    - Adopt on 'Anchorman.eng.srt' renames it to 'Anchorman (2004) Bluray-2160p.en.srt', and the chip turns ✓.
    - Replace queues a job; the orphan moves to the recycle bin only after the new subtitle is written.
    - 'Adopt all' on 200 orphaned movies adopts the unambiguous ones and reports the skipped ones with reasons.
    - Requests naming a file that isn't an orphan of that movie return 400.
  - **Tests:** Go: TestAdoptOrphanRenames, TestAdoptRefusesExistingTarget (409) and TestAdoptRejectsNonOrphan (400).; Go: TestReplaceRetiresOnlyOnSuccess (fake ladder produces / doesn't produce).; Go: TestBulkAdoptSkipsAmbiguous and the RoleManager requirement on all three routes.; UI check: Orphaned filter, bulk confirm, MovieDetail Adopt.
  - **Depends on:** [SUB-05](#sub-05), [SUB-02](#sub-02), SAFE — no-hard-delete policy for retireSidecar
  - **Risk:** Adopting an orphan that belonged to a different cut keeps an out-of-sync subtitle. That's the owner's explicit choice; SUB-19's on-demand sync check can verify it afterwards.
  - **Resolves:** subtitles-3

#### Milestone: M3 — Every attempt recorded, nothing retried forever, honest provider status

_Each sidecar has a recorded source and each attempt a typed outcome. Jobs end as failed with a real reason, and no-match or impossible files back off instead of being searched every 6h. The OpenSubtitles pill reflects a real login, and a 429 means seconds, not a day. The Overview lists what needs attention with one-click fixes. Coverage recounts as soon as the languages change, and the legacy endpoints are gone._

<a id="sub-11"></a>
- [ ] **SUB-11 · Provenance and attempt ledger: subtitle_files, subtitle_attempts, blocklist, honest job outcomes** — `P1` · `M` · Phase 12
  - **Problem:** Nothing records where a sidecar came from: no provider file_id, release, score or source.
- Failures (download errors, 401 logins, whisper crashes) only emit console lines, and the job ends StateSkipped. StateFailed is used only for 'file is gone' (process.go:19, 197-207).
- A spent quota shows as 'N pending (OCR/AI)'.
- Reasons live in a 500-line in-memory log (jobs.go:428-434) and are lost on restart.
Redo, blocklisting, Health, backoff, upgrades and the attention board all need this data.
  - **Approach:** 1) New migration (next free number, 0090 today):
    - subtitle_files(id INTEGER PRIMARY KEY, media_key TEXT NOT NULL, kind TEXT NOT NULL, movie_id INTEGER, series_id INTEGER, season INTEGER, episode INTEGER, video_path TEXT NOT NULL, path TEXT NOT NULL, lang TEXT NOT NULL, variant TEXT NOT NULL DEFAULT 'full', source TEXT NOT NULL, embedded_index INTEGER, provider TEXT, provider_file_id TEXT, release_name TEXT, hash_match INTEGER NOT NULL DEFAULT 0, score INTEGER, model TEXT, sync_offset_ms INTEGER, sync_confidence INTEGER, locked INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, superseded_at INTEGER);
      - UNIQUE INDEX on path WHERE superseded_at IS NULL; INDEX on media_key.
    - subtitle_attempts(id INTEGER PRIMARY KEY, run_id TEXT NOT NULL, media_key TEXT NOT NULL, video_path TEXT NOT NULL, lang TEXT NOT NULL, variant TEXT NOT NULL DEFAULT 'full', rung TEXT NOT NULL /* extract|opensubtitles|ai|upgrade|repair */, outcome TEXT NOT NULL, detail TEXT, at INTEGER NOT NULL);
      - INDEX (media_key, lang, variant, at DESC); INDEX (at).
    - subtitle_blocklist(provider TEXT NOT NULL, file_id TEXT NOT NULL, media_key TEXT NOT NULL, lang TEXT NOT NULL, reason TEXT NOT NULL, at INTEGER NOT NULL, PRIMARY KEY(provider, file_id, media_key)).
    
    2) internal/subtitles/ledger.go (Service gets the *sql.DB it already passes to probeCache):
    - recordFile (supersedes any live row for the same path);
    - recordAttempts(runID, []Outcome);
    - filesFor(mediaKey);
    - liveFile(path);
    - blocklisted(provider, mediaKey) map[string]bool;
    - block(...);
    - pruneAttempts(older than 180d), called from AutoGrab.
    
    3) [SUB-01](#sub-01)'s langOutcome becomes the persisted Outcome{Lang, Variant, Rung, Result, Detail}. Classify errors with sentinels:
    - ErrQuotaExhausted → quota;
    - login 401/403 → new ErrAuthFailed (opensubtitles login()) → auth_failed;
    - other HTTP → provider_error;
    - no results → no_match;
    - AI: model_missing, ai_failed, impossible;
    - context cancel → cancelled.
    rate_limited is added in [SUB-13](#sub-13).
    - grabOne returns the chosen SubtitleResult, so the file row gets provider_file_id, release and hash_match. Extraction records source=embedded with the track index; AI records source ai-transcribe or ai-translate and the model file name.
    
    4) Job gains Outcomes []Outcome (JSON 'outcomes').
    - State: Done if anything was written; Failed if every attempted language ended in an error class (auth_failed, provider_error, ai_failed, model_missing); Skipped only when nothing was needed or nothing was possible.
    - The note is generated from the outcomes, e.g. 'en: OpenSubtitles login rejected (401) → AI failed: whisper exit 1'.
    
    5) Ledger write errors are logged, never fatal to the job. Writes happen once per language per rung, never per progress tick. There is no backfill: existing sidecars have no rows and so are unowned.
    
    6) GET /api/v1/subtitles/history?media_key=&before=&limit= (RoleManager) pages subtitle_attempts.
  - **Files:** `internal/store/migrations/0090_subtitle_ledger.sql`, `internal/subtitles/ledger.go`, `internal/subtitles/ledger_test.go`, `internal/subtitles/process.go`, `internal/subtitles/service.go`, `internal/subtitles/jobs.go`, `internal/subtitles/opensubtitles.go`, `internal/httpapi/subtitles.go`, `internal/httpapi/server.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - After a job writes subtitles, subtitle_files holds one live row per sidecar with the correct source. Downloads carry provider_file_id, release and hash_match; extractions carry the track index; AI rows carry the model.
    - subtitle_attempts has a row for every (language, rung) the job tried, with a typed outcome.
    - With a wrong OpenSubtitles password and no AI model, the job ends 'failed' with 'OpenSubtitles login rejected (401)' in its note.
    - Outcomes are still visible via GET /api/v1/subtitles/history after a container restart.
  - **Tests:** Go: TestMigrationsApplyFresh covers the new tables (internal/store/migrations_apply_test.go), plus insert and read.; Go: ledger_test covering record, supersede, blocklisted and prune.; Go: TestProcessOutcomeClassification — the fake provider returns each error class; assert the outcome, job state and note.; Go: TestJobOutcomesJSON shape; race test in Docker.
  - **Depends on:** [SUB-01](#sub-01)
  - **Risk:** Migration numbers can collide with other epics working in parallel, so take the next free number when implementing. media_key 'm:<id>' covers only the movie's default file; extra versions are out of scope, and rows carry video_path so they never mix.
  - **Resolves:** subtitles-8, subtitles-5, subtitles-4
<a id="sub-12"></a>
- [ ] **SUB-12 · Back off per file and language instead of re-searching every 6 hours** — `P1` · `S` · Phase 12
  - **Problem:** SweepMissing decides 'missing' from the sidecars on disk alone (jobs.go:321-360) and runs every 6 hours (cmd/arrmada/main.go:455). Files with no match, an impossible AI plan, a failed login or a missing model are queued again every 6 hours forever, each time spending a search API call. enqueue only dedupes active jobs (jobs.go:196-211).
  - **Approach:** 1) New migration: subtitle_backoff(media_key TEXT NOT NULL, lang TEXT NOT NULL, variant TEXT NOT NULL DEFAULT 'full', video_path TEXT NOT NULL, primary_reason TEXT NOT NULL, reasons_json TEXT NOT NULL, detail TEXT, count INTEGER NOT NULL DEFAULT 1, next_retry_at INTEGER /* NULL = only an event clears it */, clear_on TEXT NOT NULL DEFAULT '' /* CSV: creds|model|langs */, updated_at INTEGER NOT NULL, PRIMARY KEY(media_key, lang, variant)).
    
    2) At the end of a run, for each language still missing, upsert one row; delete the row when the language was produced.
    - Each rung's outcome maps to a delay or an event:
      - no_match, below_score and out_of_sync: 7d, then 30d, then 90d;
      - quota: the provider's reset time;
      - rate_limited: +10m;
      - provider_error: 6h, 24h, 72h;
      - ai_failed: 1d, then 7d, then manual only;
      - auth_failed → event creds;
      - model_missing → event model;
      - impossible → event langs.
    - next_retry_at = the earliest time-based retry among the rungs (NULL when all are event-based). clear_on = the union of events. count increments when primary_reason repeats.
    - primary_reason is the most actionable outcome, in this order: auth_failed > model_missing > quota > rate_limited > provider_error > ai_failed > out_of_sync > below_score > no_match > impossible.
    
    3) SweepMissing and missingEpisodes load every active backoff with one query per sweep into map[media_key|lang|variant]row. A pair is blocked when next_retry_at is NULL or in the future, and the row's video_path equals the current file. An upgraded file resets.
    - process() for a sweep-priority job also drops blocked languages, in case the job was queued earlier.
    - Import and manual jobs ignore backoff.
    
    4) Clear rows on these events:
    - ClearBackoff('creds'), called from handleSetAPIKey for opensubtitles_* ids (internal/httpapi/apikeys.go:43-69);
    - DownloadModel success → 'model';
    - SetSettings language change → 'langs'.
    
    5) LangStatus gains NextRetryAt and Reason, filled from one backoff load per snapshot pass. CoverChip shows 'en · next try in 6d'.
  - **Files:** `internal/store/migrations/0091_subtitle_backoff.sql`, `internal/subtitles/backoff.go`, `internal/subtitles/backoff_test.go`, `internal/subtitles/jobs.go`, `internal/subtitles/process.go`, `internal/subtitles/service.go`, `internal/subtitles/models.go`, `internal/subtitles/library.go`, `internal/subtitles/snapshot.go`, `internal/httpapi/apikeys.go`, `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A file with no OpenSubtitles match where AI isn't possible is searched once, then not again for 7 days. Its chip shows the next try date.
    - After saving new OpenSubtitles credentials, files parked as auth_failed are queued by the next sweep.
    - After downloading large-v3, files parked as model_missing are queued by the next sweep.
    - Pressing Ensure on a backed-off file queues and runs it immediately.
    - A sweep over the full library makes exactly one backoff query.
  - **Tests:** Go: TestBackoffSchedule table (outcomes and count → next_retry_at / clear_on / primary_reason).; Go: TestSweepHonoursBackoff (seeded row with a future next_retry_at → not queued) and TestBackoffResetsOnFileChange.; Go: TestCredentialChangeClearsAuthBackoff, TestModelDownloadClearsModelBackoff and TestManualQueueIgnoresBackoff.
  - **Depends on:** [SUB-11](#sub-11), [SUB-02](#sub-02)
  - **Risk:** Long backoffs hide subtitles that are uploaded later. The 90-day cap, manual Retry (SUB-14) and the upgrade loop (SUB-22) cover that.
  - **Resolves:** subtitles-1
<a id="sub-13"></a>
- [ ] **SUB-13 · Truthful OpenSubtitles status: credential test, token refresh, short 429 backoff, inline credentials** — `P1` · `M` · Phase 12
  - **Problem:** The green 'OpenSubtitles ready' pill comes from CanDownload(), which only checks that three strings are non-empty (opensubtitles.go:105-111).
- The OpenSubtitles keys aren't Testable (internal/apikeys/apikeys.go:60-73), so a wrong password fails every job silently.
- login() reuses the cached token until a 401 (opensubtitles.go:142-171), so changed credentials are ignored.
- A 429 goes through noteQuota(0, …, true), which becomes a 24-hour pause showing '0 downloads left' (opensubtitles.go:277-293).
- The header pill reads 'AI + embedded only' whenever the provider isn't ready, even when AI isn't either (Subtitles.tsx:58).
- The Settings copy promises a 'search only' tier the module never uses (Subtitles.tsx:777-791).
  - **Approach:** 1) opensubtitles.go:
    - Verify(ctx) (UserInfo, error): a fresh POST /login, then GET /infos/user. Returns allowed_downloads, remaining_downloads, level, vip and the reset time, and updates the quota fields.
    - authState{ok, msg, at} is set by Verify, login and Download.
    - login() caches the token together with a credential fingerprint (sha256 of apikey|user|pass). A changed fingerprint drops the token. A 401/403 sets authState failed and returns ErrAuthFailed.
    - Paused() also returns true while authState is failed for the current fingerprint, so the [SUB-01](#sub-01) ladder skips the download rung (outcome auth_failed) and goes to AI instead of failing each file.
    
    2) Rate limits:
    - 406 → quota pause using reset_time_utc (default 24h).
    - 429 on Search or Download → ErrRateLimited, with retryAfter from the Retry-After or ratelimit-reset header (default 10 s, max 10 min). Wait once inside the job, then record outcome rate_limited. Never a quota pause.
    - Add the rate_limited outcome to the [SUB-11](#sub-11) classifier.
    
    3) Settings JSON gains provider_state ('not_configured' | 'needs_account' | 'unverified' | 'ok' | 'auth_failed' | 'quota_spent' | 'rate_limited'), provider_detail, vip and allowed_downloads. Keep provider_ready and can_download for compatibility.
    - The header pill is built from provider_state and ai_ready. It's green only on ok, meaning a successful login has been seen. Fix the 'AI + embedded only' pill to reflect ai_ready.
    
    4) API keys:
    - Mark opensubtitles_api, opensubtitles_username and opensubtitles_password Testable.
    - handleTestAPIKey adds cases calling a.deps.Subtitles.VerifyProvider(ctx).
    - handleSetAPIKey for those ids resets authState and calls ClearBackoff('creds') ([SUB-12](#sub-12)).
    
    5) Subtitles Settings tab:
    - inline credential fields (API key, username, password) using the existing PUT /api/v1/apikeys/{id}, masked like the API keys page;
    - a Test button, debounced to at most one login per 2 s;
    - a state line, e.g. 'Logged in · VIP no · 14/20 downloads left today'.
    Remove the 'Searching works' copy. Credentials are only ever entered in the app UI.
  - **Files:** `internal/subtitles/opensubtitles.go`, `internal/subtitles/opensubtitles_test.go`, `internal/subtitles/service.go`, `internal/subtitles/provider.go`, `internal/subtitles/process.go`, `internal/apikeys/apikeys.go`, `internal/httpapi/apikeys.go`, `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With a wrong password, Test reports 'Login rejected (401)', the header pill is red, and jobs skip the download rung and go to AI.
    - After changing the password in the Subtitles Settings tab, Test logs in fresh without a restart, and auth_failed backoffs are cleared.
    - A 429 burst pauses for seconds (console: 'rate-limited, retrying in 10s'); quota and downloads-left are unaffected.
    - A 406 shows 'Daily quota used — resumes <time>' using the API's reset time.
  - **Tests:** Go (httptest): TestVerifyReadsUserInfo, TestLoginRefreshesOnCredentialChange, Test429IsShortBackoffNotQuota, Test406PausesUntilReset and TestAuthFailedPausesDownloads.; Go: TestHandleTestAPIKeyOpenSubtitles (fake verifier).; UI check: inline fields save, Test result message, pill state transitions in both themes.
  - **Depends on:** [SUB-01](#sub-01), [SUB-11](#sub-11), [SUB-12](#sub-12), INT — coordinate if the API keys page or Testable catalogue is reworked
  - **Risk:** OpenSubtitles limits logins (about 1 per second), so debounce Test and never log in per job while a valid token exists. Don't log the fingerprint or any credential.
  - **Resolves:** subtitles-11, subtitles-9
<a id="sub-14"></a>
- [ ] **SUB-14 · Needs-attention board with one-click fixes and retry on the Overview** — `P1` · `M` · Phase 12
  - **Problem:** Without reading a scrolling 500-line in-memory console, the owner can't tell 'OpenSubtitles has nothing' from 'your password is wrong' from 'whisper crashed on the GPU'. The Overview's 'Sources & automation' card is static on/off rows with 'soon' pills (Subtitles.tsx:152-181). There is no list of what needs attention and no one-click fix.
  - **Approach:** 1) GET /api/v1/subtitles/attention (RoleManager), built in internal/subtitles/attention.go from subtitle_backoff rows plus the snapshot's orphans. Cheap: one query, no window functions.
    - Buckets by primary_reason: auth_failed, quota (with reset time), rate_limited, provider_error, no_match, below_score, out_of_sync, model_missing, ai_failed, impossible, orphaned.
    - Each bucket has {count, items (max 50): {title, kind, movie_id/series_id/season/episode, lang, variant, detail, at, next_retry_at}, fix}, where fix is one of creds | download_large | retry | try_ai | adopt.
    - Exclude languages the snapshot now shows as present.
    
    2) POST /api/v1/subtitles/retry {items: [{media_key, lang, variant}] | all_in_bucket: reason}: deletes those backoff rows and enqueues them at manual priority.
    
    3) Overview:
    - Replace the static card with real provider and AI states (provider_state, ai_ready, backend).
    - Add an attention list grouped by reason with one-click fixes:
      - 'Fix credentials' → Settings tab inline fields ([SUB-13](#sub-13));
      - 'Download large-v3' → existing models API;
      - 'Retry now';
      - 'Try AI now' (redo mode ai once [SUB-20](#sub-20) lands; until then a retry at manual priority);
      - 'Adopt / Replace' → orphans ([SUB-10](#sub-10)).
    - Each item links to /movies/{id} or /series/{id}.
    - Add coverage-by-source counts from subtitle_files grouped by source, with an 'unknown' remainder.
    
    4) Expose the attention total through a getter (AttentionCount(ctx)) so OBS's 'needs you' feed or health registry can consume it. If OBS has already landed, register it there.
  - **Files:** `internal/subtitles/attention.go`, `internal/subtitles/attention_test.go`, `internal/subtitles/backoff.go`, `internal/httpapi/subtitles.go`, `internal/httpapi/server.go`, `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With a wrong OpenSubtitles password, the Overview shows 'OpenSubtitles login failed — N files waiting' with a button that opens the credentials.
    - 'Retry now' on the no_match bucket queues those files at once, ignoring the backoff.
    - Each attention item links to its movie or series page.
    - The Overview no longer shows 'soon' pills for shipped features.
  - **Tests:** Go: TestAttentionBuckets (seeded backoff rows and snapshot → bucket counts; present languages excluded; orphans bucket).; Go: TestRetryHandlerClearsBackoffAndQueues and the RoleManager check on both routes.; UI check: buckets render with working fix buttons and links, in both themes.
  - **Depends on:** [SUB-11](#sub-11), [SUB-12](#sub-12), [SUB-13](#sub-13), [SUB-10](#sub-10), OBS — 'needs you' feed / health registry (soft: getter provided if OBS hasn't landed)
  - **Risk:** The Overview copy overlaps COPY's Subtitles copy cleanup (draft subtitles.t26), so coordinate to avoid editing the same lines twice.
  - **Resolves:** subtitles-8
<a id="sub-15"></a>
- [ ] **SUB-15 · Recount coverage as soon as the kept languages change** — `P2` · `S` · Phase 12
  - **Problem:** SetSettings only writes the languages CSV (service.go:128-154), and handleUpdateSubtitleSettings returns without calling Rescan (internal/httpapi/subtitles.go:120-138). The snapshot's Missing counts and chips were computed against the old list (snapshot.go:50-86), so adding Spanish leaves the Overview at 100% for up to 6 hours.
  - **Approach:** 1) librarySnapshot records langsKey, the canonical joined list it was computed with. SetSettings compares the old and new canonical lists; if they differ, it calls s.Rescan(context.WithoutCancel(ctx)). [SUB-33](#sub-33) later calls the same hook on profile changes.
    
    2) Coverage() returns stale=true while snap.langsKey differs from the current list. The Settings and Coverage JSON gain 'stale'.
    
    3) Rescan already dedupes a pass in flight. Add a followUp flag: if a pass is running with the old languages, exactly one more pass runs when it finishes.
    
    4) The Overview shows 'Recounting for your new languages…' instead of the old percentage while stale or scanning. Its existing polling (Subtitles.tsx:117-121) picks up the finished pass, and the Library tab reloads after it.
  - **Files:** `internal/subtitles/service.go`, `internal/subtitles/snapshot.go`, `internal/subtitles/snapshot_test.go`, `internal/httpapi/subtitles.go`, `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Adding Spanish shows 'Recounting…' on the Overview immediately. When the pass finishes, the percentage reflects Spanish and the Library shows Spanish chips.
    - Saving the same language list doesn't start a pass.
    - Changing languages during a running pass leads to exactly one more pass.
  - **Tests:** Go: TestSetSettingsLanguagesTriggersRescan, TestSameLanguagesNoRescan and TestFollowUpPassWhenScanning (small temp library via store.Open and real movies and series services, or a seam on Rescan).; UI check: the recounting message, then the updated numbers.
  - **Risk:** A full pass takes minutes on the array; that's acceptable with the honest 'recounting' state.
  - **Resolves:** subtitles-15
<a id="sub-16"></a>
- [ ] **SUB-16 · Remove legacy subtitle endpoints and the flat TV walk; make Subtitles reads staff-only** — `P2` · `S` · Phase 12
  - **Problem:** MovieStatuses, SeriesStatuses, GrabMovie and GrabSeries (service.go:170-304), their handlers (internal/httpapi/subtitles.go:221-287) and routes (server.go:368-371) are dead code. The api.ts wrappers at 1540-1543 are unused.
- GrabMovie and GrabSeries run in detached goroutines outside the queue, with no extract, AI, logging or dedupe.
- GET /subtitles/library?media=tv without group still runs Library(ctx,'tv'), which probes every episode (subtitles.go:183).
- The verify note says the security angle is overstated: externalGate blocks outside-LAN requesters, and plain protected GETs are an app-wide pattern owned by SEC. This is low-severity cleanup.
  - **Approach:** 1) Delete:
    - Service.MovieStatuses, SeriesStatuses, GrabMovie, GrabSeries and the MovieStatus/SeriesStatus types;
    - handleSubtitleMovies, handleSubtitleSeries, handleSubtitleSearchMovie, handleSubtitleSearchSeries and their 4 routes;
    - the api.ts wrappers subtitleMovies, subtitleSeries, searchMovieSubs and searchSeriesSubs, and the MovieSubStatus/SeriesSubStatus types;
    - Library()'s 'tv' branch.
    Keep grabOne, which the ladder uses.
    
    2) handleSubtitleLibrary: media=tv without group or series → 400 'use group=series or series=<id>'.
    
    3) Switch every remaining GET /api/v1/subtitles/* route (library, coverage, models, jobs, logs, settings, and history/attention once present) from a.protected to a.requireRole(auth.RoleManager, …). Requesters have no Subtitles page.
    
    4) Add the subtitles routes to the expectations of SEC's deny-by-default route-walk test if it exists; otherwise add a local route-table test.
  - **Files:** `internal/subtitles/service.go`, `internal/subtitles/library.go`, `internal/httpapi/subtitles.go`, `internal/httpapi/server.go`, `internal/httpapi/subtitles_routes_test.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - GET /api/v1/subtitles/movies and POST /api/v1/subtitles/movies/{id}/search return 404.
    - As a requester, GET /api/v1/subtitles/library returns 403; as a manager, every Subtitles tab still loads.
    - GET /api/v1/subtitles/library?media=tv without group returns 400 and probes nothing.
    - go vet and tsc are clean, with no unused exports left.
  - **Tests:** Go: TestSubtitleRoutesRequireManager (route-table walk for /api/v1/subtitles/*).; Go: TestLibraryFlatTVRejected.; UI check: every Subtitles tab loads as a manager.
  - **Depends on:** SEC — deny-by-default route-walk test (soft: add expectations if it exists)
  - **Risk:** Anything outside the app that calls the old endpoints breaks; nothing in the repo does. Confirm no requester-visible page (Discover, request cards) calls a /subtitles GET before switching to RoleManager.
  - **Resolves:** subtitles-13

#### Milestone: M4 — The right subtitle, in sync

_Downloads are scored on release, source, edition and fps, and weak matches are refused. Non-hash downloads are sync-checked and auto-shifted, and Health shows real scores. Redo works per language and never returns the same file. Untagged audio is detected, so turbo-only installs work. AI and weak downloads are upgraded once a hash-matched subtitle appears._

<a id="sub-17"></a>
- [ ] **SUB-17 · Score OpenSubtitles candidates by release match and refuse weak non-hash picks** — `P1` · `M` · Phase 12
  - **Problem:** Candidates are ranked only by hash match, then non-HI, then download count (opensubtitles.go:242-250), and grabOne downloads results[0] regardless (service.go:336). Release name, source, edition and fps are never compared, and there is no minimum score or trusted filtering. On WEB-DLs, remuxes and repacks without a hash match, the most-downloaded subtitle (often a different cut or frame rate) is saved and marked covered.
  - **Approach:** 1) opensubtitles.go: parse fps, from_trusted, uploader.rank, votes, files[0].file_name and feature_details.season_number/episode_number. SubtitleResult gains FPS, Trusted, FileName, UploaderRank, Season and Episode.
    
    2) MatchContext{SourceRelease, FileBase, FPS, Season, Episode}:
    - SourceRelease comes from [SUB-01](#sub-01)'s fileRef (Movie.SourceRelease / Episode.SourceRelease);
    - FPS comes from [SUB-03](#sub-03)'s probe;
    - the file base name is the fallback when SourceRelease is empty.
    
    3) New internal/subtitles/scoring.go: scoreCandidate(c, m) (score int, reasons []string). It compares parser.Parse(c.Release or c.FileName) with parser.Parse(m.SourceRelease or m.FileBase).
    - Hash match: 100.
    - Same release group: +25.
    - Same source tier (BluRay/Remux, WEB-DL, WEBRip, HDTV): +20; mismatch −15.
    - Same resolution: +5.
    - Same edition: +10; edition mismatch (Extended vs Theatrical): −30.
    - FPS equal (±0.01): +15; FPS different: −40.
    - Trusted: +5.
    - Reject outright: episode mismatch, ai_translated, or foreign_parts_only for a full want.
    - Download count is a log-scale tie-breaker only.
    
    4) grabOne ranks by score and skips blocklisted file_ids ([SUB-11](#sub-11)).
    - Best ≥ subs_min_score (setting, default 60): download.
    - 35 to below the minimum: until [SUB-19](#sub-19) lands, record below_score and fall through; afterwards, accept only if the sync check passes.
    - Below 35: below_score, next rung.
    - Store the score in subtitle_files.score and the reasons in the attempt detail.
    - Console line, e.g. 'best non-hash candidate 42: WEBRip vs BluRay, 25 vs 23.976 fps'.
    
    5) An advanced 'Minimum match score' field in the Subtitles Settings tab (GET/PUT settings gain min_score). It is read per job, so no restart is needed.
  - **Files:** `internal/subtitles/opensubtitles.go`, `internal/subtitles/provider.go`, `internal/subtitles/scoring.go`, `internal/subtitles/scoring_test.go`, `internal/subtitles/service.go`, `internal/subtitles/process.go`, `internal/httpapi/subtitles.go`, `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - For a Bluray-2160p file without a hash match, a 25 fps WEBRip candidate is not downloaded. The job falls through to AI with reason below_score and the scoring explanation.
    - A candidate from the same group and source is downloaded even when it has fewer downloads than a mismatched one.
    - Every downloaded subtitle has its score saved in subtitle_files and shown in the console line.
    - Changing the minimum score in Settings takes effect on the next job without a restart.
  - **Tests:** Go: TestScoreCandidate table (group, source, edition, fps, episode mismatch, AI-translated and hash cases).; Go: TestSearchParsesExtendedAttributes against a synthetic httptest JSON response.; Go: TestGrabOnePicksHighestScore, TestGrabOneBelowThresholdFallsThrough and TestGrabOneSkipsBlocklisted.
  - **Depends on:** [SUB-11](#sub-11), [SUB-03](#sub-03), [SUB-01](#sub-01)
  - **Risk:** A threshold that's too strict sends most WEB-DLs to AI. Log the score distribution for a couple of weeks and tune the default.
  - **Resolves:** subtitles-4
<a id="sub-18"></a>
- [ ] **SUB-18 · Subtitle-to-speech sync checker (offset and frame-rate drift) in pure Go** — `P1` · `M` · Phase 12
  - **Problem:** Nothing checks whether a non-hash download, or any existing sidecar, lines up with the audio. SubHealth is declared but never assigned (library.go:18-41). The module already extracts 16 kHz mono audio and runs silencedetect for whisper (whisper.go:323-341, chunks.go), but nothing reuses that for timing checks.
  - **Approach:** 1) Factor the audio extraction out of whisperGen.generate into extractWAV(ctx, ffmpeg, video string, audioStream int, dst string) error, so whisper and sync share it. Whisper's output stays byte-identical.
    
    2) New internal/subtitles/sync.go:
    - speechEnvelope(wavPath) []float32: RMS over 10 ms frames read straight from the PCM, with an adaptive threshold (noise-floor percentile + 10 dB), a 200 ms hangover and an optional 300–3400 Hz biquad band-pass.
    - cueEnvelope(cues []cue, frames int): 1 inside cue spans, using parseSRT from cues.go.
    - align(speech, cueEnv) SyncResult{OffsetMs, Scale, Confidence 0-100, Coverage}:
      - coarse normalized cross-correlation on 100 ms frames over ±120 s lags, refined at 10 ms around the peak;
      - then 8 windows with local offsets fitted by linear regression to detect drift, snapping to known frame-rate ratios (25/23.976, 24/23.976, 25/24);
      - Confidence comes from the peak correlation and the peak-to-second-peak ratio.
    - shiftCues(cues, offsetMs, scale), and writeShifted(path) through formatSRT (temp file + rename).
    
    3) No new dependencies. Keep the brute-force correlation bounded (about 2400 lags × 72k frames) and profile it.
  - **Files:** `internal/subtitles/sync.go`, `internal/subtitles/sync_test.go`, `internal/subtitles/whisper.go`, `internal/subtitles/cues.go`
  - **Acceptance:**
    - Given a synthetic speech envelope and an SRT shifted by +2.35 s, align() returns an offset within ±20 ms of 2350 with confidence ≥ 80.
    - Given 25 → 23.976 drift, align() reports Scale ≈ 1.0427, and the shifted cues land within 100 ms across the timeline.
    - An unrelated SRT scores confidence < 30.
    - Aligning a 2-hour envelope takes under 2 s on the server CPU.
  - **Tests:** Go: TestAlignOffset, TestAlignDrift, TestAlignRejectsUnrelated and TestShiftCuesRoundTrip, all on synthetic envelopes and SRT text, never the owner's media.; Go: BenchmarkAlign2h.; Go: TestExtractWAVArgs (command construction only); the existing whisper tests stay green.
  - **Risk:** Sparse-dialogue or music-heavy films give weak envelopes. Report low confidence as 'unknown' rather than 'bad' for existing files.
  - **Resolves:** subtitles-4
<a id="sub-19"></a>
- [ ] **SUB-19 · Sync-check non-hash downloads automatically and show a real Health score** — `P1` · `M` · Phase 12
  - **Problem:** Downloads that aren't hash-matched are saved without any timing check. The Library 'Health' column is a permanent '—' (Subtitles.tsx:674) next to copy saying 'Health scoring lands with the sync phase' (Subtitles.tsx:434). An out-of-sync subtitle is marked covered with no remedy.
  - **Approach:** 1) Download rung: after downloading a candidate with no hash match, run [SUB-18](#sub-18)'s checker against the audio track audioStreamFor would pick for that language. Extract the WAV at most once per job, share it across languages, and delete it when the job ends.
    - Confidence ≥ 70, |offset| ≤ 300 ms, scale 1: keep as is.
    - Confidence ≥ 70 but a shift or scale is needed: apply it, then keep.
    - Confidence < 50: reject, blocklist (reason out_of_sync), try the next candidate (at most 3), then AI.
    - In between: keep, with Health 'uncertain'.
    - This also enables [SUB-17](#sub-17)'s 35-to-minimum band: accept only with confidence ≥ 70.
    - Record sync_offset_ms and sync_confidence in subtitle_files. Extracted embedded tracks and hash-matched downloads skip the check.
    
    2) On-demand check: POST /api/v1/subtitles/sync-check {kind, ids, lang, variant} (RoleManager). The sidecar path is resolved server-side from the media ref, never taken from the client. The check runs as a manual-priority job and creates or updates the subtitle_files row (source 'unknown' for unowned files).
    
    3) Health: fillCoverage fills LangStatus.Health {score, offset_ms, checked_at} and FileSubs.Health (the worst language) from the live subtitle_files rows; never-checked files show 'not checked'. Remove the 'Nil until the scoring phase lands' comment.
    - The Library Health column shows the score per language, with a tooltip such as 'shifted −3.0 s · checked 2026-10-12'.
    - If COPY's stale-copy task (draft subtitles.t26) removed the stub column, re-add it here; if not, replace the stub and its copy at Subtitles.tsx:434 in this task.
    
    4) Never run sync checks from the 6-hourly sweep over the whole library.
  - **Files:** `internal/subtitles/process.go`, `internal/subtitles/service.go`, `internal/subtitles/sync.go`, `internal/subtitles/library.go`, `internal/subtitles/ledger.go`, `internal/httpapi/subtitles.go`, `internal/httpapi/server.go`, `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A non-hash download that is 3 s late is written corrected (first cue moved by −3.0 s), and Health shows ≥ 70 with 'shifted −3.0 s'.
    - An unrelated candidate is rejected as out_of_sync and blocklisted, and the next candidate or the AI rung runs.
    - 'Check sync' on an existing sidecar fills in its Health score.
    - Files that have never been checked show 'not checked' instead of a bare dash.
  - **Tests:** Go: integration test with an ffmpeg lavfi-generated WAV of tone bursts at known times plus a synthetic SRT, asserting accept/shift/reject (skipped when ffmpeg is absent; never touches library media).; Go: process test with a fake checker covering reject → next candidate → AI, and the WAV extracted once for two languages.; Go: TestSyncCheckResolvesPathServerSide (unknown media → 404; no client path accepted).; UI check: Health values and tooltip in both themes.
  - **Depends on:** [SUB-17](#sub-17), [SUB-18](#sub-18), [SUB-11](#sub-11), COPY — Subtitles stale copy and Health stub cleanup (draft subtitles.t26)
  - **Risk:** Audio extraction reads the whole video from the spinning array (about 1 minute for a remux). That's acceptable per non-hash download in the fast lane, but not as a library-wide pass.
  - **Resolves:** subtitles-4
<a id="sub-20"></a>
- [ ] **SUB-20 · Per-language Redo that never hands back the same bad subtitle** — `P1` · `M` · Phase 12
  - **Problem:** Redo has three problems.
- It clears 'present' for every kept language (process.go:31-38), so languages that were fine are redone too, including a full AI re-run for a 2-hour film.
- The download path takes results[0] from a deterministic sort (service.go:336, opensubtitles.go:242-250), so Redo fetches the same out-of-sync file again and spends a daily download, while the confirm dialog promises it fixes 'a bad download' (Subtitles.tsx:651-653).
- A sidecar named .eng.srt gets a duplicate .en.srt next to it instead of being replaced.
  - **Approach:** 1) Job gains Langs []string (empty means all missing) and Mode ('' | 'next' | 'ai' | 'extract' | 'pick' | 'repair-forced'), with optional AudioStream and TrackIndex overrides.
    - New route POST /api/v1/subtitles/redo {kind, movie_id | series_id+season+episode, lang, variant, mode, audio_stream?, track_index?} (RoleManager), queued at manual priority.
    - The legacy ?redo=1 maps to Mode 'next' for every present language.
    
    2) In process(), for each redo language, read the live subtitle_files row for the current sidecar:
    - source opensubtitles: insert a blocklist row (reason 'redo'). grabOne already filters blocklisted ids ([SUB-17](#sub-17)); when nothing is left, fall to AI and say so.
    - source embedded: use pickFullTrack skipping the recorded index, else download, else AI.
    - unowned (no row): the 'next' mode runs the normal ladder.
    - Mode 'ai' forces the AI rung, preferring large-v3 when installed, with the optional audio stream override.
    - Mode 'extract' re-extracts from the chosen track.
    
    3) Write to the canonical <base>.<lang>[.variant].srt. Retire the previous file with retireSidecar ([SUB-10](#sub-10)), including a non-canonical .eng.srt, and supersede its row. Never hard-delete. If the retire fails, abort the redo for that language and keep the old file.
    
    4) UI (Subtitles.tsx):
    - a present-language CoverChip opens a small menu: 'Replace with next download', 'Re-generate with AI', 'Re-extract (track N)';
    - the confirm text names the exact file, says what replaces it, and says the old file goes to the recycle bin;
    - the row-level Redo becomes a secondary 'Redo all languages…';
    - queue rows show e.g. 'redo en (next download)'.
  - **Files:** `internal/subtitles/process.go`, `internal/subtitles/service.go`, `internal/subtitles/jobs.go`, `internal/subtitles/ledger.go`, `internal/httpapi/subtitles.go`, `internal/httpapi/server.go`, `web/src/lib/api.ts`, `web/src/pages/Subtitles.tsx`
  - **Acceptance:**
    - Redo on a downloaded English subtitle writes one with a different provider file_id, and the replaced file_id never comes back for that file.
    - Redo of English leaves the Spanish sidecar untouched (same mtime).
    - 'Re-generate with AI' runs whisper even when OpenSubtitles is configured.
    - An existing Movie.eng.srt is replaced (the old file is in the recycle bin), not joined by a second Movie.en.srt.
    - When every candidate is blocklisted, Redo falls through to AI and says so in the note.
  - **Tests:** Go: TestRedoBlocklistsAndPicksNext (fake provider with 2 results).; Go: TestRedoOnlyTouchesNamedLanguage and TestRedoAIModeForcesAIRung.; Go: TestRedoReplacesNonCanonicalSidecar (old file recycled, row superseded) and TestRedoAbortsWhenRetireFails.; Go: handler test for /subtitles/redo (RoleManager; legacy ?redo=1 mapping).; UI check: the chip menu, confirm text and queue row labels.
  - **Depends on:** [SUB-11](#sub-11), [SUB-10](#sub-10), [SUB-03](#sub-03), [SUB-17](#sub-17)
  - **Risk:** The retired sidecar must actually leave the folder, or it would pair again; the retireSidecar contract covers this. Free-tier quota: each 'next download' redo spends one download, so the confirm text says so.
  - **Resolves:** subtitles-5
<a id="sub-21"></a>
- [ ] **SUB-21 · Detect untagged audio language before AI; record model_missing instead of failing** — `P1` · `M` · Phase 12
  - **Problem:** Untagged audio ('und', common on WEB-DLs) is dropped from AudioLangs (probe.go:83), so aiPlan routes it to 'translate', which needs large-v3 (modelPath(true), whisper.go:253-259). The UI tells people to 'get turbo for English', so turbo-only installs fail on every untagged English file, and before SUB-12 those were retried every 6 hours.
  - **Approach:** 1) detectAudioLang(ctx, wav):
    - cut a 30 s sample from the first speech chunk after 10% of the runtime, using planChunks output;
    - run whisper-cli with turbo (large-v3 if turbo is absent) and `-dl -l auto`;
    - parse 'auto-detected language: xx (p = 0.97)'.
    Reuse [SUB-18](#sub-18)'s extractWAV so the WAV is extracted once and then shared with generate.
    
    2) Cache the result in mediaInfo.Detected map[int]DetectedLang{Lang, Prob}, keyed by audio stream index, written through probeCache.put. It stays valid while size and mtime are unchanged. Detection runs only for audio streams with no language tag, and only when the AI rung needs them.
    
    3) aiPlan(mi, want) uses the tagged language, or the detected one when prob ≥ 0.6.
    - Transcribe (turbo) when it matches the wanted language.
    - Translate (large-v3, -l <detected>) only when the audio is truly foreign and English is wanted.
    - Otherwise impossible.
    Update the comment.
    
    4) When a translation is needed and large-v3 is missing, record outcome model_missing ('needs large-v3 to translate fr audio to English') rather than failing the job. [SUB-12](#sub-12) parks it until a model download clears it.
    - The LocalAI card in Settings shows 'N files are waiting for large-v3' (count of subtitle_backoff rows with primary_reason model_missing) next to its Download button.
    
    5) Replace twoToThree lookups with langs ([SUB-08](#sub-08)).
  - **Files:** `internal/subtitles/whisper.go`, `internal/subtitles/whisper_test.go`, `internal/subtitles/probe.go`, `internal/subtitles/process.go`, `internal/subtitles/models.go`, `internal/subtitles/backoff.go`, `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - An untagged-English WEB-DL on a turbo-only install is transcribed with turbo, and the job is Done.
    - With only turbo installed, a French film appears as 'waiting for large-v3' in Settings and on the attention board, and isn't re-run every 6 hours.
    - The detected language is cached: a second job on the same file doesn't run detection again.
    - Tagged audio never triggers detection.
  - **Tests:** Go: TestParseDetectedLanguage using captured whisper-cli output lines as a fixture.; Go: TestAIPlanUsesDetectedLanguage and TestAIPlanModelMissingOutcome.; Go: TestDetectionCachedInProbeCache and TestTaggedAudioSkipsDetection (fake aiRunner counts calls).
  - **Depends on:** [SUB-01](#sub-01), [SUB-06](#sub-06), [SUB-08](#sub-08), [SUB-11](#sub-11), [SUB-12](#sub-12), [SUB-18](#sub-18)
  - **Risk:** Detection costs one model load (a few seconds on the GPU) per untagged file before caching. Check the -dl behaviour against the pinned whisper.cpp build, and don't introduce -ml, -sow or --vad.
  - **Resolves:** subtitles-6
<a id="sub-22"></a>
- [ ] **SUB-22 · Upgrade loop: replace AI and weak downloads when a hash-matched subtitle appears** — `P3` · `M` · Phase 17
  - **Problem:** With the ladder, new episodes get AI subtitles on air night, even though human hash-matched subtitles usually appear on OpenSubtitles within days. Nothing ever revisits an AI or non-hash subtitle, so the library keeps the weaker one forever.
  - **Approach:** 1) A daily scheduled task, 'subtitles-upgrade' (registered in cmd/arrmada/main.go), selects owned rows from subtitle_files:
    - source in (ai-transcribe, ai-translate), or source opensubtitles with hash_match=0;
    - locked=0 and not superseded;
    - created_at within subs_upgrade_days (default 21).
    Adopted, manual, unknown and release files are never candidates.
    
    2) Search OpenSubtitles while respecting Paused() and rate limits. Use at most subs_upgrade_quota_share (default 30%) of the day's remaining downloads, from Quota(), so the backlog still gets downloads.
    
    3) Replace the subtitle only when:
    - a hash match, or a candidate scoring ≥ 90, appears; or
    - a non-hash candidate passes the sync check ([SUB-19](#sub-19)) with confidence ≥ 80 and beats the current row's score.
    
    4) Retire the old file with retireSidecar, supersede its row and record the new one. Record the attempt with rung 'upgrade' (ok, or no_match with the next check date).
    
    5) Add an Overview line: 'N subtitles upgraded this week'.
  - **Files:** `internal/subtitles/upgrade.go`, `internal/subtitles/upgrade_test.go`, `internal/subtitles/ledger.go`, `cmd/arrmada/main.go`, `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - An episode that got an AI subtitle on import is switched to a hash-matched OpenSubtitles file once one exists within the 21-day window, and the AI file is in the recycle bin.
    - Manually picked (locked), adopted and release subtitles are never replaced.
    - The loop never uses more than its configured share of the day's remaining downloads.
  - **Tests:** Go: TestUpgradeCandidatesQuery (owned only; age window; locked and superseded excluded).; Go: TestUpgradeReplacesOnHashMatch and TestUpgradeRespectsQuotaShare (fake provider).
  - **Depends on:** [SUB-11](#sub-11), [SUB-12](#sub-12), [SUB-13](#sub-13), [SUB-17](#sub-17), [SUB-19](#sub-19), [SUB-10](#sub-10)
  - **Risk:** It competes with the backlog for the free-tier quota, so keep the share conservative. The replaced file must leave the folder, or it would pair again.
  - **Resolves:** subtitles-4, subtitles-1

#### Milestone: M5 — A queue that survives restarts and never blocks on the GPU

_Queued work and history survive Unraid updates, and the Queue tab shows filterable history with retry. Extractions and downloads run in a fast lane while whisper works through the GPU lane, with a backlog ETA._

<a id="sub-23"></a>
- [ ] **SUB-23 · Persist the subtitle queue and job history across restarts** — `P1` · `M` · Phase 12
  - **Problem:** pending and jobs are in-memory slices (service.go:48-57). A restart, which happens on every Unraid update, drops every queued job and the visible history. The sweep is registered with runAtStart=false (main.go:455), and the scheduler keeps no last-run state, so nothing re-queues for up to 6 hours.
  - **Approach:** 1) New migration: subtitle_jobs(id INTEGER PRIMARY KEY, media_key TEXT NOT NULL, kind TEXT NOT NULL, movie_id INTEGER, series_id INTEGER, season INTEGER, episode INTEGER, title TEXT NOT NULL, priority INTEGER NOT NULL, lane TEXT NOT NULL DEFAULT 'fast', state TEXT NOT NULL, mode TEXT, langs TEXT, redo INTEGER NOT NULL DEFAULT 0, note TEXT, outcomes_json TEXT, queued_at INTEGER NOT NULL, started_at INTEGER, finished_at INTEGER).
    - INDEX (state, lane, priority, queued_at).
    - UNIQUE INDEX (media_key, lane) WHERE state IN ('queued','running').
    - The lane column exists now, so [SUB-25](#sub-25) needs no migration.
    
    2) jobs.go:
    - enqueue inserts the row; the job ID is the row id. The in-memory active map and the priority buckets stay as caches.
    - A priority bump ([SUB-02](#sub-02)) updates the row.
    - finish, Cancel and ClearQueue update rows.
    - Stage and Progress stay in memory only; persist state transitions, never ticks.
    - Keep the last 500 finished rows, pruned in AutoGrab.
    - SweepMissing batches its inserts in one transaction.
    
    3) Startup, in Service.Run before the worker loop: rows still 'running' become 'queued' with the note 'interrupted by restart', then queued rows are loaded into the buckets in priority/queued_at order.
    
    4) Run a sweep 10 minutes after boot. The scheduler has no delay option (scheduler.go:41), so use a timer inside Service.Run. Restarts then catch up on missed work without hammering the disks at boot; backoff ([SUB-12](#sub-12)) keeps this cheap.
    
    5) Jobs() returns persisted history merged with in-memory progress. GET /subtitles/jobs gains ?before=&state=&limit= paging for [SUB-24](#sub-24).
  - **Files:** `internal/store/migrations/0092_subtitle_jobs.sql`, `internal/subtitles/jobs.go`, `internal/subtitles/jobstore.go`, `internal/subtitles/service.go`, `internal/subtitles/cancel_test.go`, `internal/subtitles/autofetch_test.go`, `internal/subtitles/jobstore_test.go`, `internal/httpapi/subtitles.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Restarting the container mid-queue brings back the same queued jobs, in the same priority order.
    - The interrupted job runs again, and its note says 'interrupted by restart'.
    - Recent history in the Queue tab survives a restart.
    - Ten minutes after boot a sweep runs, queuing only what isn't in backoff.
  - **Tests:** Go: TestQueueSurvivesRestart (a second Service on the same store.Open DB reloads pending) and TestRunningJobRequeuedOnStartup.; Go: TestPriorityBumpPersists and TestHistoryPrunedTo500.; Go: the existing never-blocks and dedupe tests pass under -race in Docker (the sweep insert batch must not block the scheduler).
  - **Depends on:** [SUB-02](#sub-02), [SUB-11](#sub-11), [SUB-12](#sub-12), BE — related job-runner work (not blocking)
  - **Risk:** Writing to the DB on every enqueue during a 25k-file sweep; batch the sweep's inserts in one transaction. The unique index must match the in-memory dedupe exactly, or enqueue can fail with a constraint error; treat a conflict as 'already queued'.
  - **Resolves:** subtitles-7
<a id="sub-24"></a>
- [ ] **SUB-24 · Queue tab history: filters, per-row retry, title links, per-language outcomes** — `P1` · `S` · Phase 12
  - **Problem:** The Queue tab's Recent list shows 30 rows with no filter, no retry and no link to the title (Subtitles.tsx:222-235). Per-language reasons exist only in the console.
  - **Approach:** 1) Recent list:
    - filter pills: failed / skipped / done / cancelled;
    - per-row Retry, which calls POST /subtitles/retry ([SUB-14](#sub-14)) for that job's missing languages at manual priority;
    - the title links to /movies/{id} or /series/{id};
    - expandable per-language outcome lines from job.outcomes (rung → outcome → detail);
    - an 'Older history' button that pages GET /subtitles/jobs?before= ([SUB-23](#sub-23)).
    
    2) The Queue tab groups active rows by priority, with lanes once [SUB-25](#sub-25) lands.
    
    3) Keep the existing palette, row density and StateBadge.
  - **Files:** `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - After a restart, the Recent list still shows yesterday's failed jobs with their per-language reasons.
    - Filtering to 'failed' and pressing Retry on a row queues it at manual priority.
    - Each row's title opens the movie or series page.
  - **Tests:** UI check: filters, retry, links, outcome expansion and paging, at desktop and 375 px widths in both themes.; Go: TestJobsPagingHandler (before, state and limit params).
  - **Depends on:** [SUB-23](#sub-23), [SUB-11](#sub-11), [SUB-14](#sub-14)
  - **Risk:** Low.
  - **Resolves:** subtitles-8
<a id="sub-25"></a>
- [ ] **SUB-25 · Split the queue into a fast lane and a GPU lane, with an AI backlog ETA** — `P2` · `M` · Phase 12
  - **Problem:** There is one worker, so a one-second extraction or download waits behind hour-long whisper runs. The UI can't say how long the AI backlog will take, even though the realtime factor is measured on every run (process.go:166-170).
  - **Approach:** 1) Run two worker loops over subtitle_jobs:
    - lane 'fast': extract, download and sync check; concurrency from subs_fast_lane_workers (default 1, max 2);
    - lane 'gpu': whisper; concurrency 1.
    - Every job starts in the fast lane. When languages still need AI, the fast job enqueues a gpu-lane job for that media_key with only those languages (Job.Langs), inheriting its priority.
    - Enforce per-media_key mutual exclusion across lanes (an in-memory set checked in pop), so a redo can't race an AI run on the same file.
    - running and cancelRun become per-worker.
    
    2) Cancel and Stop work per job and per lane. Add a 'Clear AI backlog' action (POST /subtitles/jobs/clear?lane=gpu).
    
    3) Keep an EWMA of the realtime factor per backend in the settings key subs_ai_rtf.
    
    4) The jobs API returns {gpu_backlog_audio_sec, gpu_eta_sec, rtf}, summing mediaInfo.DurationSec of queued gpu jobs from the probe cache (cached only, never a fresh probe).
    
    5) The Overview 'Working now' card shows e.g. 'AI backlog: 37 files · ~14h at 4.2× realtime'. The Queue tab groups jobs by lane.
  - **Files:** `internal/subtitles/jobs.go`, `internal/subtitles/process.go`, `internal/subtitles/service.go`, `internal/subtitles/jobstore.go`, `internal/httpapi/subtitles.go`, `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - While a 2-hour whisper run is in progress, a newly imported episode with an embedded English text track gets its .en.srt within a minute.
    - The Overview shows an AI backlog count and ETA that shrinks as jobs finish.
    - Stopping the GPU job doesn't affect fast-lane jobs, and vice versa.
  - **Tests:** Go: TestFastLaneRunsDuringAI (the fake aiRunner blocks on a channel; the extract job still completes).; Go: TestFastJobSpawnsGPUJobForLeftovers and TestNoConcurrentJobsSameFile.; Go: TestBacklogETAFromRTF; race test in Docker.; UI check: lane grouping and the backlog line.
  - **Depends on:** [SUB-23](#sub-23), [SUB-01](#sub-01)
  - **Risk:** More concurrency on the spinning array, so the fast lane defaults to 1. Whisper shares the GPU with Plex (compute units versus the video engine), which is known to be fine. The per-file exclusion must also cover SUB-07 repair jobs and SUB-22 upgrades.
  - **Resolves:** subtitles-7

#### Milestone: M6 — Per-title control (Bazarr parity)

_The movie Library has search and posters. A per-file drawer lists, previews, nudges, deletes and extracts subtitles. Interactive OpenSubtitles search lets the owner pick a release. Movie and Series pages show subtitle status. Plex refreshes the folder after every sidecar change._

<a id="sub-26"></a>
- [ ] **SUB-26 · Movie Library: text search, poster thumbnails and paged rendering** — `P2` · `S` · Phase 12
  - **Problem:** The movie Library tab has no text search, unlike TV (Subtitles.tsx:508-514), and renders every row at once. poster_url is sent for every row (library.go:34) but never rendered, by either the movie or the TV rows.
  - **Approach:** 1) Movie Library: a search input like TVLibrary's, filtering on title and year client-side, debounced.
    
    2) Poster thumbnails (32×48, lazy-loaded) on movie rows and TV show rows, using the same image source as the Movies grid.
    
    3) Render in pages of 100 with a 'Show more' button. There's no virtualization dependency in web/package.json, so don't add one unless FE decides to.
    
    4) Keep the palette and type scale, and reuse the FE kit if it has landed.
  - **Files:** `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Typing 'dune' in the Movies library filters instantly, and rows show posters.
    - With 5,000 movies the Library stays responsive: typing and filters react in under 200 ms.
    - TV show rows show posters too.
  - **Tests:** UI check at desktop and 375 px widths in both themes: search, posters, paging.
  - **Depends on:** FE — component kit / image component (soft)
  - **Risk:** Low. Use lazy image loading so the first render doesn't fetch 5,000 posters.
  - **Resolves:** subtitles-12
<a id="sub-27"></a>
- [ ] **SUB-27 · Per-file subtitle drawer: list, preview, nudge timing, delete, extract, generate** — `P2` · `M` · Phase 12
  - **Problem:** Library row actions are only 'Ensure subs' and 'Redo' (Subtitles.tsx:655-691). The owner can't see which sidecars exist, where they came from or what they say, and can't delete one or nudge its timing without editing files by hand.
  - **Approach:** 1) GET /api/v1/subtitles/file?kind=movie&id=… or kind=episode&series=…&season=…&episode=… (RoleManager). It returns:
    - the video path, audio tracks (with detected language), and embedded subtitle tracks (title, forced, SDH);
    - the wants;
    - sidecars: [{name, lang, variant, source, provider_file_id, release, hash_match, score, sync_confidence, sync_offset_ms, created_at, size, cue_count, first_cues: [3]}], joining scanSidecars with subtitle_files;
    - orphans ([SUB-05](#sub-05));
    - the backoff row and the latest attempts per language and rung.
    
    2) POST /api/v1/subtitles/file/shift {media ref, name, offset_ms}: parseSRT → shiftCues → formatSRT, written to a temp file and renamed into place. Update subtitle_files.sync_offset_ms, creating the row with source 'unknown' if none exists.
    
    3) DELETE /api/v1/subtitles/file {media ref, name}: retireSidecar, supersede the row, refreshSnapshot.
    
    4) POST /api/v1/subtitles/file/extract {media ref, track_index, lang, variant}: queue a manual-priority job with Mode 'extract'.
    
    5) Path safety for all of these: the server resolves the video from the media ref via the DB. name must be a bare filename (no separators or '..') with a subtitle extension that pairs with that video, or is one of its orphans. Anything else returns 400.
    
    6) New web/src/components/SubtitleDrawer.tsx: a right-side drawer using the existing panel and line tokens, a full-height sheet on phones. It contains:
    - sidecar cards with a source badge, Health, a 3-line preview, ±0.5 s and ±1 s nudges with Apply, and Delete (the confirm says the file goes to the recycle bin);
    - embedded tracks with Extract;
    - 'Generate with AI' (redo mode ai, [SUB-20](#sub-20)).
    It opens from movie and episode rows in the Library.
  - **Files:** `internal/subtitles/fileview.go`, `internal/subtitles/fileview_test.go`, `internal/subtitles/sync.go`, `internal/httpapi/subtitles.go`, `internal/httpapi/server.go`, `web/src/components/SubtitleDrawer.tsx`, `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Clicking a movie row opens a drawer listing each sidecar with its source, Health and first three lines.
    - Nudging +1.0 s and pressing Apply rewrites the file, and the preview timestamps move by one second.
    - Delete moves the sidecar into the recycle bin, and the language chip turns missing.
    - A name with a path separator, one from another folder, or one that doesn't pair with the video returns 400.
    - The drawer is usable at 375 px width.
  - **Tests:** Go: TestFileViewJoinsDiskAndLedger and TestShiftRoundTrip.; Go: TestFileHandlersRejectForeignPaths (traversal, other folder, non-subtitle extension) and TestDeleteRecyclesSidecar.; UI check: open, nudge, delete, extract and generate flows on desktop and phone widths in both themes.
  - **Depends on:** [SUB-11](#sub-11), [SUB-05](#sub-05), [SUB-10](#sub-10), [SUB-18](#sub-18), [SUB-20](#sub-20), FE — drawer/sheet component (soft)
  - **Risk:** Path validation is a security boundary: resolve the video from the DB, never trust a client path. Plex may read a file mid-write, which the temp-file-and-rename write avoids.
  - **Resolves:** subtitles-12
<a id="sub-28"></a>
- [ ] **SUB-28 · Interactive OpenSubtitles search and manual pick in the drawer** — `P2` · `M` · Phase 12
  - **Problem:** Bazarr's core workflow doesn't exist: open a title, search manually, pick the right release. The only recovery is Redo, which can't choose a particular candidate.
  - **Approach:** 1) POST /api/v1/subtitles/search {media ref, lang, variant} (RoleManager). It returns candidates with score and reasons ([SUB-17](#sub-17)): release, file name, uploader, HI, hash match, fps, downloads, the AI and foreign flags, and a blocklisted flag. It uses no download quota and is refused while provider_state is auth_failed or not_configured.
    
    2) POST /api/v1/subtitles/pick {media ref, lang, variant, provider, file_id} queues a manual-priority job with Mode 'pick' that:
    - downloads that file_id;
    - runs the sync check ([SUB-19](#sub-19)), which for manual picks warns rather than rejects and applies a confident shift;
    - writes the file, retires the previous one, and records provenance with source 'manual' and locked=1, so the upgrade loop never replaces it;
    - blocklists the replaced file_id when it was a download.
    
    3) Drawer: a 'Search OpenSubtitles' panel per language.
    - A candidate table: Release · Score (reasons on hover) · HI · Hash · FPS · Downloads · Use this.
    - Quota left and the reset time are shown above it. With the quota spent, 'Use this' is disabled and shows the reset time.
    - The search is debounced and a refresh is manual.
  - **Files:** `internal/subtitles/search.go`, `internal/subtitles/search_test.go`, `internal/subtitles/process.go`, `internal/subtitles/ledger.go`, `internal/httpapi/subtitles.go`, `internal/httpapi/server.go`, `web/src/components/SubtitleDrawer.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Searching English in the drawer lists candidates, flags the hash-matched ones and shows scores with reasons.
    - 'Use this' on the second candidate replaces the current sidecar with that file, and the drawer then shows source manual and that release.
    - With the quota spent, 'Use this' is disabled and shows the reset time.
  - **Tests:** Go: TestSearchHandlerScoresAndFlagsBlocklisted (fake provider).; Go: TestPickJobWritesChosenFileAndLocks, including blocklisting the previous file_id and retiring the old sidecar.; UI check: the candidate table and the Use this flow.
  - **Depends on:** [SUB-27](#sub-27), [SUB-17](#sub-17), [SUB-19](#sub-19), [SUB-20](#sub-20), [SUB-13](#sub-13)
  - **Risk:** Manual search spends API calls (rate-limited by OpenSubtitles), so debounce it. Manual picks bypass the score threshold by design; the sync warning keeps that honest.
  - **Resolves:** subtitles-12, subtitles-5
<a id="sub-29"></a>
- [ ] **SUB-29 · Subtitle status and 'Find subtitles' on Movie and Series detail pages** — `P2` · `M` · Phase 12
  - **Problem:** MovieDetail lists sidecar filenames with no actions (MovieDetail.tsx:770-777), and SeriesDetail shows no subtitle state at all. The owner has to go to the Subtitles page and find the title again.
  - **Approach:** 1) MovieDetail.tsx: replace the filename-only Subs row with per-want chips from GET /api/v1/subtitles/file, e.g. '✓ en · OpenSubtitles · Health 92' or 'es missing · next try in 6d'. Add a 'Find subtitles' button that opens SubtitleDrawer. Orphans keep their Adopt button from [SUB-10](#sub-10).
    
    2) SeriesDetail.tsx: a compact per-episode subtitle chip (✓, or a missing count). It loads lazily, only when the episodes section is visible, from the existing GET /api/v1/subtitles/library?media=tv&series=ID; that response gains next_retry_at and reason per language from [SUB-12](#sub-12). Clicking an episode chip opens the drawer for that episode.
    
    3) Keep the current palette and type scale, and reuse the FE component kit if it has landed. These pages are staff-only, and the subtitles GETs require RoleManager ([SUB-16](#sub-16)).
  - **Files:** `web/src/pages/MovieDetail.tsx`, `web/src/pages/SeriesDetail.tsx`, `web/src/components/SubtitleDrawer.tsx`, `web/src/lib/api.ts`, `internal/subtitles/library.go`
  - **Acceptance:**
    - MovieDetail shows language chips with source and Health, and 'Find subtitles' opens the drawer.
    - SeriesDetail episode rows show subtitle state, and a chip click opens the drawer for that episode.
    - Opening SeriesDetail doesn't trigger ffprobe until the episodes section is scrolled into view.
  - **Tests:** Go: TestSeriesEpisodesIncludesWantStatus (JSON has per-language status, reason and next retry).; UI check at desktop and 375 px widths in both themes: chips, drawer launch, lazy load.
  - **Depends on:** [SUB-27](#sub-27), [SUB-12](#sub-12), FE — component kit (soft)
  - **Risk:** SeriesEpisodes probes a show's episodes when called, so keep it lazy on SeriesDetail so the page load doesn't trigger ffprobe. Coordinate with any FE/SER work that reshapes these pages.
  - **Resolves:** subtitles-12
<a id="sub-30"></a>
- [ ] **SUB-30 · Tell Plex when a subtitle is written, shifted, removed or adopted** — `P2` · `S` · Phase 12
  - **Problem:** After writing a sidecar, process() only patches Arrmada's own snapshot (process.go:202-204, snapshot.go:136). The Plex client has no refresh call (internal/plex/client.go), so a subtitle generated tonight may not be selectable on the TV until Plex's next scan. FUSE change detection on Unraid is unreliable.
  - **Approach:** 1) Give subtitles.Service the event bus: SetBus(*eventbus.Bus), wired in cmd/arrmada/main.go:446-448, where bus already exists at line 139.
    
    2) Add one helper, s.sidecarChanged(videoPath, path, action), that publishes 'subtitles.sidecar_changed' {video_path, path, action}. Call it from every write site present at implementation time:
    - ladder writes (process.go);
    - redo, pick and upgrade replacements;
    - shift and delete (fileview.go);
    - adopt (orphans.go);
    - forced repair (repair.go).
    
    3) The PLEX epic's partial-scan hook subscribes to this topic alongside its import, rename and delete events. That hook owns:
    - plex.Client.RefreshPath (GET /library/sections/{id}/refresh?path=…);
    - the section lookup by library Location;
    - Arrmada→Plex path mapping;
    - a per-folder debounce of about 30 s.
    If the hook hasn't landed, this task delivers only the publish side plus tests.
  - **Files:** `internal/subtitles/service.go`, `internal/subtitles/process.go`, `internal/subtitles/fileview.go`, `internal/subtitles/orphans.go`, `internal/subtitles/repair.go`, `internal/subtitles/events_test.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - About 30 s after an AI subtitle is written, Plex's activity shows a partial scan of that folder, and the subtitle is selectable on a client without a manual scan (once the PLEX hook is live).
    - Ten sidecars written into one season folder within a minute produce a single refresh (PLEX debounce).
  - **Tests:** Go: TestSidecarWritePublishesEvent (subscribe on a real eventbus.Bus) for write, shift, delete and adopt.; The debounce and path-mapping tests live with the PLEX hook.
  - **Depends on:** PLEX — Plex partial-scan hook (RefreshPath, section lookup, path mapping, debounce)
  - **Risk:** Arrmada's container paths differ from Plex's. The PLEX hook's path mapping must handle this, or refreshes silently miss. Publish never blocks (Bus.Publish drops on a full buffer), so a slow subscriber can't stall a job.
  - **Resolves:** subtitles-14

#### Milestone: M7 — Languages and profiles

_A searchable language picker supports regional variants, and Convert can keep the same languages as Subtitles. Language profiles (forced and full variants, SDH preference, 'only when the audio differs') come with per-title overrides and an editor. Chinese and Japanese AI subtitles are laid out by characters._

<a id="sub-31"></a>
- [ ] **SUB-31 · Searchable language picker with regional variants (GET /api/v1/languages)** — `P2` · `S` · Phase 12
  - **Problem:** The UI offers 16 hard-coded chips with no regional variants (Subtitles.tsx:10-18). pt-br, zh-tw and es-419 can't be chosen, and chips elsewhere show upper-cased codes for languages missing from the list.
  - **Approach:** 1) GET /api/v1/languages (protected; read-only static data) returns the internal/langs table: code, name and regional flag.
    
    2) Replace the LANGS constant and chip grid in SettingsTab with:
    - a searchable picker (typing 'port' offers Portuguese and Portuguese (Brazil));
    - an ordered list of kept languages with remove and up/down. Order matters because wanted[0] is the language an untagged sidecar counts as.
    
    3) langName() reads from the fetched table, cached for the session, so every chip shows the proper name.
    
    4) Write the picker as a small reusable component (web/src/components/LanguagePicker.tsx) so [SUB-34](#sub-34)'s profile editor and Convert can use it.
  - **Files:** `internal/httpapi/server.go`, `internal/httpapi/languages.go`, `web/src/components/LanguagePicker.tsx`, `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - The owner can add Portuguese (Brazil) and reorder languages. Saving persists the order, and the snapshot recounts (SUB-15).
    - Chips everywhere show language names from the API.
  - **Tests:** Go: TestLanguagesHandler shape.; UI check: search, add, remove and reorder at desktop and 375 px widths in both themes.
  - **Depends on:** [SUB-08](#sub-08), [SUB-15](#sub-15), FE — component kit (soft)
  - **Risk:** Low.
  - **Resolves:** subtitles-10
<a id="sub-32"></a>
- [ ] **SUB-32 · Convert can keep the same subtitle languages as Subtitles ('Same as Subtitles')** — `P2` · `S` · Phase 12
  - **Problem:** The Subtitles page claims its kept languages control stripping ('everything else is stripped from the video (once stripping ships)', Subtitles.tsx:152 and 755). Convert actually uses a separate convert_keep_sub_langs setting (internal/convert/decide.go:27, 84), so the two can disagree and the copy is false.
  - **Approach:** 1) convert_keep_sub_langs accepts the sentinel '@subtitles', meaning 'same languages as Subtitles'. Before [SUB-33](#sub-33) that's the subs_languages list; afterwards, the Default profile's languages.
    - Convert reads the list through a small interface `SubtitleLangs(ctx) []string` injected in cmd/arrmada/main.go (convert must not import subtitles).
    - decide.go resolves the sentinel when it builds its plan.
    - If the getter fails or returns nothing, fall back to keeping all subtitles; never interpret it as 'keep nothing'.
    
    2) Convert settings UI (Convert.tsx) offers 'Same as Subtitles' as a choice beside the explicit list.
    
    3) The Subtitles page replaces both 'once stripping ships' lines with a read-only line, 'Convert keeps embedded subtitles in: <list or Same as Subtitles>', and a link to Convert settings.
    
    4) Existing installs keep their current Convert value; the new choice is only offered.
  - **Files:** `internal/convert/decide.go`, `internal/convert/settings.go`, `internal/convert/decide_test.go`, `cmd/arrmada/main.go`, `web/src/pages/Convert.tsx`, `web/src/pages/Subtitles.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With Convert set to 'Same as Subtitles', a planned conversion keeps exactly the Subtitles languages.
    - The Subtitles page states what Convert keeps and links to it, and no 'once stripping ships' copy remains.
    - Existing Convert settings are unchanged after upgrade.
  - **Tests:** Go: TestConvertKeepSubsFollowsSubtitles (the sentinel resolves through the injected getter) and TestSentinelFallbackKeepsAll; the existing convert decide tests stay green.
  - **Depends on:** [SUB-08](#sub-08), CONV — Convert settings UI / decide.go ownership (coordinate edits)
  - **Risk:** An empty list from the sentinel must mean keep-all, never keep-nothing. Coordinate with CONV so its rebuild work doesn't overwrite the sentinel handling.
  - **Resolves:** subtitles-9
<a id="sub-33"></a>
- [ ] **SUB-33 · Language profiles: forced/full variants, SDH preference, 'only when audio differs', per-title overrides** — `P2` · `M` · Phase 12
  - **Problem:** Wanted languages are a flat CSV (service.go:21, 156-168). There's no way to:
- ask for forced subtitles specifically;
- prefer or avoid SDH;
- want English subtitles only on foreign-audio films;
- use a different set for one series (e.g. anime).
Now that forced and full are separate variants (SUB-03), coverage and the ladder need to know which variants the owner wants.
  - **Approach:** 1) New migration:
    - subtitle_profiles(id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, items_json TEXT NOT NULL, is_default INTEGER NOT NULL DEFAULT 0). The migration seeds 'Default' from the subs_languages setting: one item per language, {lang, variant: 'full', sdh: 'any', only_if_audio_differs: false}. If the setting is unset, the Go side seeds ['en'] on first read.
    - subtitle_profile_overrides(media_kind TEXT NOT NULL, media_id INTEGER NOT NULL, profile_id INTEGER NOT NULL, PRIMARY KEY(media_kind, media_id)). This avoids altering the movies and series tables.
    
    2) Service.wants(ctx, ref) []Want{Lang, Variant (full|forced|both), SDH (prefer|avoid|any), OnlyIfAudioDiffers}. Load profiles and overrides once per sweep or snapshot pass.
    - process, fillCoverage, SweepMissing, missingEpisodes, groupFor and the backoff keys all use (lang, variant) from wants.
    - A forced want extracts the forced track or searches OpenSubtitles with ForeignParts='only'.
    - The SDH preference feeds pickFullTrack ([SUB-03](#sub-03)) and scoreCandidate ([SUB-17](#sub-17)).
    - OnlyIfAudioDiffers skips the want when the file's primary audio (default, else first) matches the language.
    - languages(ctx) stays as a shim over the Default profile, so old callers and [SUB-32](#sub-32)'s getter keep working. SetSettings(languages) rewrites the Default profile's items.
    
    3) API: GET/POST/PUT/DELETE /api/v1/subtitles/profiles and PUT/DELETE /api/v1/subtitles/overrides/{kind}/{id} (RoleManager). The Default profile can't be deleted.
    
    4) Any profile or override change triggers [SUB-15](#sub-15)'s recount, and clears 'langs' backoffs ([SUB-12](#sub-12)).
  - **Files:** `internal/store/migrations/0093_subtitle_profiles.sql`, `internal/subtitles/profiles.go`, `internal/subtitles/profiles_test.go`, `internal/subtitles/service.go`, `internal/subtitles/process.go`, `internal/subtitles/library.go`, `internal/subtitles/jobs.go`, `internal/subtitles/snapshot.go`, `internal/subtitles/backoff.go`, `internal/httpapi/subtitles.go`, `internal/httpapi/server.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - After migration, the Default profile holds exactly the previously configured languages, and coverage numbers are unchanged.
    - A profile item {en, forced} produces <base>.en.forced.srt and counts only that file as coverage for the item.
    - With 'only when audio differs' on English, English-audio films aren't counted as missing English, while foreign films are.
    - A per-series override applies to that series' episodes only.
  - **Tests:** Go: migration seed test (CSV → Default profile) via TestMigrationsApplyFresh plus a seeded-settings case.; Go: TestWantsResolution (default vs override), TestOnlyIfAudioDiffers, TestForcedWantExtractsForced and TestCoverageCountsVariants; race test in Docker.
  - **Depends on:** [SUB-03](#sub-03), [SUB-08](#sub-08), [SUB-15](#sub-15), [SUB-12](#sub-12)
  - **Risk:** Coverage semantics change, so the Default profile must reproduce today's numbers exactly before anyone edits it; add a snapshot test comparing old and new coverage on a fixture library. The migration can't read Go defaults, so handle the unset-setting case in Go.
  - **Resolves:** subtitles-2, subtitles-10
<a id="sub-34"></a>
- [ ] **SUB-34 · Profile editor and per-title subtitle profile selector** — `P3` · `M` · Phase 17
  - **Problem:** Once profiles exist (SUB-33) there's no UI to create them, set forced/SDH preferences, or assign a profile to a movie or series. The kept-languages UI stays a flat list.
  - **Approach:** 1) Subtitles Settings tab: a profile editor listing profiles. Each row uses LanguagePicker ([SUB-31](#sub-31)), with Variant (full/forced/both), SDH (prefer/avoid/any) and an 'Only when the audio is another language' toggle. The Default profile is marked and can't be deleted. The flat kept-languages list becomes 'Default profile'.
    
    2) MovieDetail.tsx and SeriesDetail.tsx: a 'Subtitles: Default ▾' selector that writes the override (PUT /subtitles/overrides/{kind}/{id}). Library rows show the profile name when overridden.
    
    3) The Subtitles page's Convert line ([SUB-32](#sub-32)) reads 'Same as Subtitles (Default profile)'.
  - **Files:** `web/src/pages/Subtitles.tsx`, `web/src/pages/MovieDetail.tsx`, `web/src/pages/SeriesDetail.tsx`, `web/src/components/LanguagePicker.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - The owner can create a profile 'Anime' (ja full + en full, SDH avoid) and assign it to a series from its detail page. After the recount, its episodes' coverage chips reflect it.
    - Deleting a profile that's in use asks first, then moves its overrides back to Default.
  - **Tests:** UI check: profile editor create/edit/delete, the detail-page selector and Library profile labels, at desktop and 375 px widths in both themes.; Go: TestDeleteProfileResetsOverrides.
  - **Depends on:** [SUB-33](#sub-33), [SUB-31](#sub-31), [SUB-32](#sub-32)
  - **Risk:** Low. Keep the editor compact; most owners will only ever edit Default.
  - **Resolves:** subtitles-9, subtitles-2
<a id="sub-35"></a>
- [ ] **SUB-35 · Lay out Chinese and Japanese AI subtitles by characters, not spaces** — `P3` · `S` · Phase 17
  - **Problem:** segmentWords starts a new word only on tokens with a leading space (words.go:90). Whisper's CJK tokens have none, so a whole Japanese or Chinese segment collapses into one 'word'. shapeWordCues then joins words with spaces (words.go:202), so AI subtitles come out as long unbroken lines with spaces inserted.
  - **Approach:** 1) words.go: add isNoSpaceScript(rune), true for Han, Hiragana and Katakana (not Hangul). In segmentWords, a token whose first rune is in a no-space script starts its own word.
    
    2) Add a joinWords(a, b) helper that inserts no space between two no-space-script words. Use it in shapeWordCues and in the cue merge in cues.go.
    
    3) cues.go line layout: count runes (utf8) and pick per-language limits from the lang passed into shapeWordCues. zh and ja use 16 characters per line and 32 per cue. Split lines on rune boundaries, preferring a break after 、。！？.
    
    4) Make sure fixCasing and filterStockPhrases are no-ops for CJK. Engine behaviour for every other language must stay byte-identical.
  - **Files:** `internal/subtitles/words.go`, `internal/subtitles/cues.go`, `internal/subtitles/casing.go`, `internal/subtitles/words_test.go`, `internal/subtitles/cues_test.go`
  - **Acceptance:**
    - A Japanese whisper JSON fixture produces cues of at most 2 lines of 16 characters, with no spaces between kana and kanji.
    - English output is byte-identical before and after the change on the existing fixtures.
  - **Tests:** Go: TestSegmentWordsCJK, TestShapeCuesJapaneseNoSpaces and TestLineSplitRunes on synthetic whisper JSON.; Go: the existing words, cues and casing tests unchanged.
  - **Risk:** Low. Korean keeps space-based words; test the joinWords rule on mixed-script lines.
  - **Resolves:** subtitles-10

#### Risks

- GPU flood after SUB-01. The first sweep pushes every previously skipped file into the AI rung, so SUB-02 (import priority) must ship in the same deploy. The release note should mention the backlog and the Clear queue button.
- Coverage numbers drop when the truth arrives. SUB-03 (forced no longer counts) and SUB-05 (movies pair by base name; orphans aren't coverage) will lower reported coverage overnight. Release notes must explain this and point to Adopt all (SUB-10) and the forced repair pass (SUB-07).
- Disk I/O on the spinning array: the ProbeVersion-2 re-probe (movies on the next pass, episodes lazily), forced-repair re-extraction (suspects only), and sync-check WAV extraction (non-hash downloads only). None of these may become a library-wide pass in the 6-hourly sweep.
- Migration number collisions with other epics working in parallel (four new migrations: ledger, backoff, jobs, profiles). Always take the next free number at implementation time.
- Plex sidecar naming support for .sdh and regional tags (pt-br, zh-tw) is unverified. Check with a test file in a test folder, never by altering library media, and fall back to plain .<lang>.srt with the variant kept in subtitle_files.
- OpenSubtitles free-tier limits: 20 downloads/day and about 1 login/s. Redo, manual picks and the upgrade loop all compete for the quota, so keep the upgrade share conservative and show the quota before spending it.
- Path-handling endpoints (adopt, shift, delete, sync-check) are a security boundary. Always resolve the video from the DB and accept only bare filenames that pair with it.
- Score threshold and sync-confidence defaults (60 / 70 / 50) are guesses. Too strict sends WEB-DLs to AI; too loose keeps the current blind behaviour. Log the distributions for a few weeks and tune.
- whisper.cpp flag behaviour (-l with --translate, -dl detection) must be checked against the pinned ≥1.9.2 build. Never add -ml, -sow or --vad.
- Large epic touching one big file (web/src/pages/Subtitles.tsx, about 880 lines) many times. Consider splitting it into per-tab components early (e.g. alongside SUB-14) to reduce conflicts with COPY and FE work.

#### Out of scope

- OCR of image subtitles (PGS/VobSub). bestSource still falls through to download and AI; the ladder has a slot where OCR could go later.
- Subtitle providers other than OpenSubtitles (Addic7ed, Podnapisi, Subdl, etc.). The Provider interface stays ready for them.
- Subtitles for a movie's non-default versions. Jobs act on MovieFilePath only; rows carry video_path so they never mix.
- Machine translation into non-English languages (an LLM or other MT); whisper translates to English only.
- Embedding or stripping subtitle tracks inside containers (Convert's domain; only the shared language list is in scope).
- Listing or cleaning historical orphan sidecars in TV season folders. TV already pairs by base name; only movie orphans are surfaced.
- Preserving ASS/SSA styling. Extraction and downloads keep producing SRT.
- Requester-facing subtitle features. Subtitles stays staff-only.
- Notifying Jellyfin/Emby, and Plex-side default subtitle selection settings.
- Changing the whisper engine's chunking, DTW, cue shaping, casing or SYCL/Vulkan fallback, beyond the language handling in SUB-06, SUB-21 and SUB-35.

