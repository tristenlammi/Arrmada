# CONV — Convert

_Part of the [Arrmada roadmap](../../ROADMAP.md). 29 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Keep Convert's encode engine (decide/preset/hdr/preflight/quality/swaps) and rebuild the shell around it. Every conversion should be planned and confirmed before it runs, checked against a quality gate that justifies the 'looks the same' promise, recorded in a durable ledger afterwards, and reversible for a retention window from an originals hold that never purges anything. The scheduler must not hot-loop or make hand requests wait, and every sentence on the page must be literally true.

**Why.** The engine is careful, but the safety net around it is mostly missing.

- **The recycle bin destroys originals (convert-2, high).** The bin's 50 GB default cap is enforced hourly, oldest first. A 60–110 GB 4K original therefore pushes out everything older in the bin, including the family's own deletions, and is deleted itself within the hour. Restore cannot work anyway: for MKV sources the converted file sits at the original path. There is no Revert, yet the page says 'originals go to the recycle bin'.
- **Transient failures retry in a hot loop (convert-1, high).** 'Scratch full' and 'library disk full' are recorded as neither a failure nor a skip. With the default single worker, the same file is picked again at once: about 40 crop seeks per pass, and for a library ENOSPC a whole day-long re-encode that fails the same way. Meanwhile the log floods.
- **Nothing is recorded or shown in advance (convert-5).** History is in memory and capped at 200, so it is empty after every restart. 'Convert now' and 'Switch on' are one click, with no view of which tracks go.
- **The copy overclaims (convert-4, convert-8, convert-12, convert-14).** It says every audio track is copied, results 'must look the same' and HEVC 'plays everywhere', and it never mentions that Dolby Vision is removed. The quality gate behind 'looks the same' is four 15 s SSIM windows at 0.97, re-measured on the very windows preflight tuned the CRF to pass, and unmeasurable windows are silently dropped.
- **Forced subtitles can be lost under default settings (convert-9).**
- **'Convert now' waits behind a frozen auto job for nights (convert-6).**
- **Multi-night encodes lose all progress on any ./update.sh (convert-7).**
- **Upgrades can undo conversions (convert-3).** The upgrade system can re-grab the remux Convert just shrank. QUAL's quality-4 shows AV1 stamps reopen this.
- **Convert is invisible elsewhere (convert-10, convert-11).** Extra movie versions are never indexed, and Convert publishes no events.
- **Side steps ignore the gentle guarantees (convert-13).** SSIM, crop detection, HDR10+ extraction and the remux run at normal priority and ignore pause-while-watching.

The owner believes every conversion can be undone and that Convert is gentle and honest. Today neither is true.

**Depends on:** SAFE — recycle-bin rework (never purge the newest item, refuse instead of hard-deleting, per-filesystem bins). [CONV-02](#conv-02)'s Headroom API must match SAFE's cap semantics, and [CONV-08](#conv-08)'s holdRoot/sameDevice helper should be shared with SAFE's per-filesystem bins. This is coordination, not a blocker.; QUAL — [CONV-20](#conv-20) changes UpgradeCandidate's signature and needs QUAL's quality-4 fix (an AV1 stamp parsed as H.264 reopens the convert → re-download loop) to land first or together. [CONV-15](#conv-15)'s 'profile prefers DV' warning needs a stable profile lookup.; FE — the component kit (ConfirmDialog/Modal, Tabs, Table) for [CONV-11](#conv-11), [CONV-12](#conv-12) and [CONV-13](#conv-13) if it has landed. Otherwise those tasks use local components on the existing tokens.; PLEX — [CONV-25](#conv-25) extends plex/sessions.go parsing (source bitDepth/profile). Separately, the PLEX epic consumes [CONV-21](#conv-21)'s convert.done for a partial library refresh (backend-14).; INT — Insights monitoring has to be switched on (insights-3: it is off by default) before [CONV-25](#conv-25)'s devices panel has any data.; MOV — the versions/editions model and labels for [CONV-24](#conv-24). Library fit across versions stays with MOV.; OBS — event catalog naming and payload conventions for [CONV-21](#conv-21). OBS routes convert.done/failed/blocked/stalled to admin notifications (insights-6).; SUB — a shared forced/SDH sidecar parsing rule. [CONV-04](#conv-04) fixes convert's copy, and SUB fixes subtitles/sidecar.go langTokenFromSegments.; COPY — voice and tone rules for [CONV-05](#conv-05)'s rewritten Convert copy, if COPY defines them.

#### Design

## Convert 2.0: target design

### Principles
- **The engine stays.** Changes inside it are limited to the gate ([CONV-17](#conv-17)/19), the tool runner ([CONV-18](#conv-18)), the bit-depth choice ([CONV-26](#conv-26)), resumable pieces ([CONV-27](#conv-27)) and the Dolby Vision keep path ([CONV-29](#conv-29)).
- **Nothing destroys an original the owner thinks is recoverable.** Originals go to a Convert-owned hold, not the shared bin. When the hold is full, conversions pause.
- **One source of truth for decisions.** `prefs.planFor` and the new `trackDecisions(mi, plan)` drive the encode, the plan view, the auto-preview and the ledger. No view adds its own decision logic.
- **Every UI sentence must be literally true.** Each task updates the strings it affects.
- **Test on synthetic media only.** Encode tests use synthetic lavfi or dovi_tool fixtures, never library files. The hold is verified with a self-check probe file, not a real film.

### Item lifecycle
1. **Candidate.** Built from the index plus `planFor`.
2. **Guards, before any heavy I/O.** Hardlink, scratch free space, library-volume free space and hold budget.
3. **Job.** Pending claim, then HDR10+ probe, crop, codec test, preflight and encode.
4. **Verify.** Duration, tracks and size; then about ten SSIM windows checked on mean and floor (independent of preflight); then a full-decode integrity check and frame count.
5. **Ledger row.** `begin` writes an `in_progress` row.
6. **Journaled swap.** States `staging`, `staged`, `held`, `renamed`.
7. **Hold.** The original moves to the hold and the row becomes `held`. It ends `released` (on expiry or by hand), `reverted` or `missing`.

**Skips.** They are either permanent or temporary with backoff.
- Permanent: `hdr_unsupported`, `not_smaller`, `quality_gate`, `reverted`.
- Temporary: `hardlinked` (12 h) and `cancelled` (30 d), as today. `no_scratch`, `library_full`, `source_gone` and `transient` back off 1 h → 6 h → 24 h by `attempts`. `hold_full` waits until the next hold release.
- `bin_full` exists only in the interim, until the hold ships.

### Data model
New migrations take the next free number ≥0090 at implementation time; other epics are adding migrations too.
1. `convert_skips.attempts INTEGER NOT NULL DEFAULT 0` ([CONV-01](#conv-01)).
2. `convert_history`, the ledger ([CONV-06](#conv-06)).
   - Identity: id, item_key, kind, movie_id, version_id, series_id, season, episode, title.
   - Outcome: outcome, outcome_kind, note, requested.
   - Source: src_path, src_release, src_size, src_info_json.
   - Output: out_path, out_size, out_info_json, codec, crf, encoder, pix_fmt.
   - Quality: ssim_mean, ssim_min, ssim_windows_json, vmaf_mean, crop.
   - Tracks and warnings: kept_tracks_json, dropped_tracks_json, warnings_json, reclaim_deferred.
   - Hold: hold_path, hold_until, hold_state (''|held|released|reverted|missing), released_at, reverted_at.
   - Timing: started_at, finished_at, encode_secs.
   - Indexes on (item_key, finished_at), finished_at and hold_state.
3. `convert_swaps` gets `state` (staging|staged), `hold_path`, `op` (convert|revert) and `history_id`, all in one migration ([CONV-07](#conv-07)).
4. `movies`, `movie_versions` and `episodes` get `converted_from_release TEXT` and `converted_from_size INTEGER`. This is the pre-conversion baseline the upgrade system judges against ([CONV-20](#conv-20)).
5. `convert_library.version_id` ([CONV-24](#conv-24)). Keys become `movie:ID` for the default version and `movie:ID:v:VID` for other versions.
6. `stream_sessions.video_src_bitdepth` and `video_src_profile` ([CONV-25](#conv-25)).

There is no resume table. Pieced encoding keeps its progress in fingerprinted `resume-<hash>/` scratch folders with a manifest (the stash design, [CONV-27](#conv-27)).

**Settings** (one settings key each):
- `convert_hold_days`: 14; 0 means no undo.
- `convert_hold_max_gb`: 500; 0 means no limit.
- `convert_dolby_vision`: drop | skip | keep. Default `drop`, which is today's behaviour.
- `convert_ten_bit`: always | match_source. Default stays `always` until the owner picks.
- `convert_crop`: defaults to false when unset.
- `convert_resumable`: off, then on in [CONV-28](#conv-28).
- `convert_scan_at`: now exposed in the UI.

### Originals hold
- Path: `<library root>/.arrmada-hold/<history_id>/<original basename>`.
  - The root is the longest configured library root (MoviesDir, TVDir, LibraryDir) that contains the file.
  - It is moved with a same-filesystem `os.Rename`. On EXDEV it falls back to copy, fsync and remove, logged as slow.
  - The hold never sits under the data/DB dir.
- Hidden from Plex by the dot folder plus a `.plexignore` containing `*`. Arrmada's own scanners already skip dot dirs; this gets tested.
- Startup self-check: a tiny probe file is renamed within each root, and the result is shown in Settings ('instant move ✓' or 'would copy ⚠').
- An hourly `convert-hold-expire` job releases expired originals. 'Space saved' counts bytes only when they are released.
- Budget: when the hold is over budget, new conversions pause (`hold_full`) instead of deleting originals.
- Revert puts the original back, repoints the database, restores source_release, clears the baseline and records a permanent `reverted` skip.
- The recycle bin no longer receives Convert originals.

### APIs
All new endpoints need the manager role.
- `GET /api/v1/convert/history?outcome=&media=&q=&before=&limit=`
- `GET /api/v1/convert/history/{id}`
- `POST /api/v1/convert/history/{id}/revert`
- `POST /api/v1/convert/history/{id}/release`
- `GET /api/v1/convert/plan?key=`
- `GET /api/v1/convert/auto-preview[?series=&season=]`
- `GET /api/v1/convert/devices`
- `POST /api/v1/convert/skips/clear?kind=`
- `POST /convert/requests` now returns `{position, ahead}`.
- `GET /convert/hardware` adds scratch_need_bytes, hold_check, held_bytes, held_saving_bytes, held_count, next_release_at and budget_bytes.
- `GET /convert/status` adds up_next[].eta_sec and nights, backlog_sec, backlog_nights, in_flight_secs and problems.
- Skipped JSON adds retry_after and attempts.

### UI shape
The page keeps Convert.tsx's dark warm palette, terracotta accent, type scale and card/table styles. It uses the FE component kit if that has landed. Tabs are Overview, Library, Problems, **History**, Activity and Settings.
- **Overview:**
  - A status line with the backlog forecast ('≈ 9 months of nights at 01:00–07:00').
  - A 'Space saved' card showing 'X freed · Y held so you can undo (N files, next release in D days)'.
  - A 'Recent' card showing the last 8 ledger rows.
- **Library:**
  - 32px posters.
  - A chevron that opens a **Conversion plan** card: video before→after, every track kept or removed with a reason, and warnings in `--avoid`.
  - Version sub-rows, and Revert on converted rows.
- **Confirmations:**
  - Convert now: lists the changes and the queue position.
  - Bulk convert: shows counts.
  - Switch on: shows library-wide counts ('N files, X TB → ~Y TB; N lose Dolby Vision; N lose CC…').
- **Problems:** new kinds with GB figures and 'tries again in 6 h'. Retrying a group is one call.
- **History:** the ledger with filters and Load more, plus Revert and Release now.
- **Settings:**
  - Format: a Dolby Vision radio with the library count, the 10-bit choice and a 'Your devices' panel.
  - Undo: hold days and budget, plus the hold self-check.
  - Advanced: the daily scan time.
  - The scratch indicator is coloured against the next file's need, and the crop hint states exactly what is guaranteed.

### Scheduler
- `maxWorkers` normal workers plus one request lane. Auto jobs stay frozen while the lane is in use, so a request never waits behind a paused auto job.
- Wake-ups are broadcast, so every idle worker re-checks.
- Every ffmpeg and tool call goes through `runTool`: nice 19, idle I/O class, its own process group, and it is tracked so pause-while-watching freezes it.
- The forecast comes from the ledger's measured throughput.
- Later, resumable pieces mean a restart costs at most one piece.

### Integrations
- Eventbus topics `convert.done/failed/blocked/stalled/reverted`. OBS routes them to admin alerts; PLEX uses `convert.done` for a partial refresh.
- The upgrade sweep and the import gates respect `converted_from_*`.
- Insights device data drives the codec and bit-depth warnings.
- All movie versions are indexed.

### Rollout
Milestones M1→M7 each ship on their own via ./update.sh. M1 makes today's behaviour safe and honest. M2 delivers real undo. M3 makes conversion plan-first. M4 hardens the gate and the upgrade interplay. M5–M7 add forecasting, events, household fit and the two big bets: resumable encoding and keeping Dolby Vision as profile 8.1. Runner, hold and request-lane changes are race-tested in Docker before pushing, and commits carry the Co-Authored-By trailer.

#### Milestone: M1 — Stop the damage

_No hot retry loops and no wasted re-encodes on full disks. Convert never makes the recycle bin purge anything. Forced subtitles are never lost under the defaults. Every sentence on the Convert page is literally true about today's behaviour._

<a id="conv-01"></a>
- [ ] **CONV-01 · Back off after transient failures and check scratch/library space before any heavy work** — `P0` · `M` · Phase 0
  - **Problem:** transientFailure() notes ('not enough scratch space', 'source file is gone', 'no space left on device') are recorded as neither a failure nor a skip (service.go:614-649). finish() then calls invalidateLibraryCache()+wakeUp(), so the worker goes straight back to pickJob. computeCandidates rescans the index plus sidecar ReadDirs, and the same top-saving file is claimed again. In process() the HDR10+ probe, withCrop (40 keyframe seeks) and chooseCodec all run before the scratch check at process.go:110. The whole-file HDR10+ read only happens for files that carry HDR10+ (hdr.go:49-52). A library-disk ENOSPC while staging (process.go:606) is also 'transient', so the next pick re-runs a full day-long encode that fails the same way. With the default single worker this stalls the whole library.
  - **Approach:** 1) skips.go: add temporary kinds SkipNoScratch='no_scratch', SkipLibraryFull='library_full', SkipSourceGone='source_gone' and SkipTransient='transient' (the defensive fallback). New migration NNNN_convert_skip_attempts.sql (next free number ≥0090): ALTER TABLE convert_skips ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0. skipStore.record reads the existing (kind, attempts) and upserts attempts = same kind ? attempts+1 : 1, then returns the new attempts ([CONV-21](#conv-21) uses it). retryDelay(kind, attempts): transient kinds get 1h → 6h → 24h (cap); hardlinked and cancelled are unchanged. Add an optional retryAt override ([CONV-09](#conv-09)'s hold_full uses it). list() adds retry_after and attempts to the Skipped JSON. StateDone already clears the skip, which resets the backoff, and ClearSkip / 'Try again' clear it at once.
    2) process.go: resolveSource !ok becomes finishSkip(SkipSourceGone, 'the library file is missing — checking again later'). Add s.spaceCheck(ctx, job, src, mi, plan, scratch), called right after planFor/needs and the hardlink check and BEFORE the HDR10+ probe, withCrop and chooseCodec. Scratch need = scratchNeeded(mi, plan, false) on planFor's provisional plan, which sizeCap already accepts. Library volume: when !sameDevice(scratch, filepath.Dir(src)), require freeBytes(dir(src)) ≥ sizeCap(mi, plan)+512 MiB, or mi.SizeBytes+slack for a track tidy. A failure becomes finishSkip(SkipNoScratch, 'needs ~91 GB of scratch in /transcode, it has 40 GB free'), or SkipLibraryFull with the same figures.
    3) hdr.go: split extractHDR10Plus into hasHDR10Plus (the existing extract(100)) and the full read. Between the two, re-check scratch against scratchNeeded(…, true) (twice the cap), so the whole-file read never runs when the HDR10+ pipeline can't fit. Keep the exact check before preflight (process.go:110) as the final guard, now a finishSkip(SkipNoScratch, …) instead of StateFailed.
    4) finalizeOutput: when moveFile(dst, part) fails with errors.Is(err, syscall.ENOSPC), call finishSkip(SkipLibraryFull, 'the library disk had X free; staging needed Y — the encode was discarded and the original kept').
    5) finish(StateFailed): if transientFailure(note) still matches, record SkipTransient with backoff instead of nothing.
    6) hardlink_linux.go: add sameDevice(a, b string) bool comparing Stat_t.Dev; the hardlink_other.go stub returns true. Add package vars freeBytesFn, sameDeviceFn and moveFileFn for tests.
    7) Convert.tsx Problems: add SKIP_LABEL entries for the new kinds. Each row shows its reason with GB figures and 'tries again in 6 h' from retry_after.
  - **Files:** `internal/convert/skips.go`, `internal/convert/process.go`, `internal/convert/service.go`, `internal/convert/hdr.go`, `internal/convert/hardlink_linux.go`, `internal/convert/hardlink_other.go`, `internal/store/migrations/NNNN_convert_skip_attempts.sql`, `internal/httpapi/convert.go`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - If the scratch folder is smaller than the top candidate needs, that file appears once in Problems as 'needs ~X GB of scratch … has Y GB free' with a retry time, and the runner moves on to the next candidate in the same encode window.
    - No crop detection, HDR10+ read or HEVC/AV1 test runs for a file that fails the space check; the Activity log shows none of them.
    - A library-disk ENOSPC while staging records library_full with a 1h/6h/24h backoff instead of re-encoding on the next pick.
    - A successful conversion, 'Try again' or a hand Request clears the backoff immediately.
  - **Tests:** Go: process() with freeBytesFn stubbed below the need records no_scratch with retry_after ≈ now+1h; a second failure gives ≈ now+6h and a third ≈ now+24h; StateDone clears it.; Go: pickJob after a no_scratch skip returns the next candidate, not the same key.; Go: a call counter on detectCrop and trial (via func vars) stays 0 when the early space check fails.; Go: a moveFileFn error wrapping syscall.ENOSPC is classified as library_full and the original is untouched.; Go: skipStore.record increments attempts for the same kind and resets to 1 when the kind changes.; go test -race in Docker before pushing.
  - **Risk:** Low. A 24 h backoff must not hide a condition that has been fixed, so 'Try again' and ClearSkip clear it immediately. Always go through finishSkip so the wasCancelled handling is kept; never call skips.record directly from process(). The early check uses the provisional plan, so the exact guard before preflight must stay.
  - **Resolves:** convert-1
<a id="conv-02"></a>
- [ ] **CONV-02 · Interim guard: never convert a file whose original the recycle-bin cap would purge** — `P0` · `S` · Phase 0
  - **Problem:** retire() (service.go:730) moves originals into the shared recycle bin, whose default cap is 50 GB (recyclebin/service.go:244). Enforce runs hourly and at startup (main.go:569) and deletes oldest-first until the bin is under the cap. A 60-110 GB 4K original therefore wipes out every older bin item, the users' own deletions included, and is then deleted itself within the hour. The UI still says 'originals go to the recycle bin' (Convert.tsx:248, 464), and Settings calls the bin the place where 'a mistake is recoverable'. The real fix is the originals hold (CONV-08), which is a multi-task job. Until it lands, Convert must stop making the bin destroy data.
  - **Approach:** 1) recyclebin.Service.Headroom(ctx) (free int64, capped, enabled bool): enabled = dir != ''; capped = maxGB > 0; free = maxGB<<30 − Σ walk() sizes, floored at 0. Cache the walk for 60 s.
    2) main.go: construct recycleSvc (currently line 568) before convert.NewService (line 472), and call convertSvc.SetBinHeadroom(recycleSvc.Headroom) before `go convertSvc.Run`. Use the same atomic.Pointer pattern as SetWatching.
    3) process(): inside [CONV-01](#conv-01)'s spaceCheck (or right after the hardlink check if [CONV-01](#conv-01) hasn't landed), when enabled && capped && mi.SizeBytes > free, call finishSkip with the new temporary kind SkipBinFull='bin_full' (retry 24 h). Reason: 'the original (75 GB) doesn't fit in the recycle bin's free room (12 of 50 GB), so it would be permanently deleted within the hour. Raise the cap in Settings → Recycle bin.' Re-check in finalizeOutput immediately before retire(src). If it no longer fits, remove the staged part, keep the original and skip with the same kind; the encode is lost but nothing is destroyed.
    4) Bin off: behaviour is unchanged (hard delete after verification), but the copy says so plainly.
    5) Copy: Convert.tsx:248 and :464 and the Settings.tsx:383 bin subtitle say that originals go to the recycle bin for N days within its cap, that Convert only starts a file whose original fits, and that restoring a converted film means removing the converted file first. In bin-off mode: 'originals are deleted once the conversion is verified — there is no undo'.
    6) Problems: a bin_full label that links to Settings → Recycle bin.
    [CONV-08](#conv-08) removes this guard again once originals go to the hold.
  - **Files:** `internal/recyclebin/service.go`, `internal/convert/service.go`, `internal/convert/process.go`, `internal/convert/skips.go`, `cmd/arrmada/main.go`, `web/src/pages/Convert.tsx`, `web/src/pages/Settings.tsx`
  - **Acceptance:**
    - With the default 50 GB cap, a 75 GB remux candidate is listed in Problems as bin_full with the GB figures, and its original is never moved.
    - A conversion never pushes the bin over its cap, so no unrelated recycled item is purged because of Convert.
    - Convert and Settings copy describe the cap and the bin-off behaviour accurately.
  - **Tests:** Go: recyclebin Headroom with files totalling 30 GB under a 50 GB cap returns 20 GB, and reports uncapped when the cap is 0.; Go: process() with headroom below the source size records bin_full, leaves the source untouched and runs no encode.; Go: when the finalizeOutput re-check fails, the original is kept and the staged .arrpart is removed.
  - **Depends on:** SAFE
  - **Risk:** Large 4K remuxes stop converting until the owner raises the cap or CONV-08 ships. That is intended (safe over fast) and must be stated in Problems. Coordinate with SAFE's recycle-bin fix (never purge the newest item, per-filesystem bins) so Headroom matches SAFE's cap semantics and isn't duplicated. A file deleted by the user during the hour can still push a converted original out; that is acceptable for an interim guard.
  - **Resolves:** convert-2
<a id="conv-03"></a>
- [ ] **CONV-03 · Collapse repeated activity-log lines and colour the scratch indicator against the next file's need** — `P1` · `S` · Phase 2
  - **Problem:** Identical lines from a retry loop fill the 5,000-line activity log (logstore.go:9) and push out the history that matters. The Settings scratch indicator turns green above a fixed 20 GB (Convert.tsx:993), while a single 4K remux can need ~90 GB. The owner gets no warning that the next file won't fit.
  - **Approach:** 1) event() (service.go:162): under logMu, if the last logBuf line has the same level and the same message (ignoring a trailing ' (×N)'), rewrite it as 'msg (×N+1)' with the new timestamp instead of appending. Mirror this with a new logStore.updateLast(ctx, ln): UPDATE convert_logs SET at = ?, msg = ? WHERE id = (SELECT MAX(id) FROM convert_logs). slog still receives the first occurrence and every 10th repeat.
    2) autoCand (runner.go:150) gains an unexported needScratch int64, filled in computeCandidates from scratchNeeded(&mi, plan, maybeHDR10Plus), where maybeHDR10Plus = mi.HDR=='HDR10+' || (codecClass(mi.VideoCodec)=='hevc' && mi.EncodeHDR()=='HDR10'), because the HDR10+ pipeline doubles the need.
    3) handleConvertHardware (httpapi/convert.go:13) adds scratch_need_bytes and scratch_need_title: the largest needScratch over the first 20 candidates that are not waiting or blocked.
    4) Convert.tsx:993: green when free ≥ need, amber otherwise, with the text 'largest of the next 20 files (“Title”) needs ~91 GB'.
  - **Files:** `internal/convert/service.go`, `internal/convert/logstore.go`, `internal/convert/runner.go`, `internal/httpapi/convert.go`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - The Activity log shows one collapsed line '… (×N)' instead of N identical consecutive lines, and the collapse survives a restart.
    - The Settings scratch indicator is amber when free space is below the largest upcoming file's need, and names that file.
  - **Tests:** Go: event() collapses identical consecutive lines in logBuf and convert_logs; two alternating messages are not collapsed.; Go: logStore.updateLast rewrites only the newest row.; Go: computeCandidates sets needScratch, doubled for an HEVC HDR10 fixture.; Go: the hardware handler returns scratch_need_bytes as the maximum over the first 20 eligible candidates.
  - **Depends on:** [CONV-01](#conv-01)
  - **Risk:** Low. With two workers, lines from different jobs interleave, so only identical consecutive lines collapse. That is intended.
  - **Resolves:** convert-1
<a id="conv-04"></a>
- [ ] **CONV-04 · Never drop forced subtitles; don't treat forced-only sidecars as full coverage** — `P1` · `S` · Phase 1
  - **Problem:** dropCoveredImageSubs (preset.go:252-279) ignores SubStream.Forced. Under the default image_subs=when_text (decide.go:90-97), a forced PGS track (foreign-dialogue translations) is removed whenever any full text track or .srt for that language exists, for example the en.srt the Subtitles module writes. sidecarLangs (preset.go:725-768) takes the segment right after the base name, so '<base>.en.forced.srt' counts as full English coverage, and the full English PGS can then be dropped. The original is gone within the hour (convert-2), so this loses subtitles the family relies on.
  - **Approach:** preset.go: sidecarLangs returns (full, forced []string). It parses every dot-segment after the base: the first segment of 3 characters or fewer is the language; 'forced' marks the track as forced; 'sdh', 'cc' and 'hi' count as full. SubPlan gains TextSidecarForcedLangs, and withSidecars fills both lists.
    dropCoveredImageSubs builds textFull and textForced sets from embedded text tracks (using s.Forced) and from the sidecars. A forced image track is covered only by textForced[lang]; a full image track only by textFull[lang]. An untagged forced image track is never dropped. ImageSubsRemove, an explicit choice, is unchanged, but planWarnings adds 'forced subtitles (foreign dialogue) removed' whenever a forced track goes.
    analyze.go already sets Forced from the disposition. Add Title to SubStream (json 'title,omitempty', from tags.title) and treat a title containing 'forced' as forced. Do not bump probeSchemaVersion: new probes pick it up and old cache rows simply lack the title. [CONV-12](#conv-12) and the ledger also need Title.
    The same '.forced' flaw exists in subtitles/sidecar.go langTokenFromSegments; that belongs to the SUB epic. Name the shared rule in a comment so both converge.
  - **Files:** `internal/convert/preset.go`, `internal/convert/plan.go`, `internal/convert/analyze.go`, `internal/convert/tracks_test.go`
  - **Acceptance:**
    - A file with only '<base>.en.forced.srt' beside it keeps its full English PGS track under when_text.
    - A forced English PGS track is kept when only a full English text track exists. It is dropped only when a forced English text track (embedded or .forced.srt) exists.
    - Job notes and the Activity log warn whenever a forced track is removed under 'remove'.
  - **Tests:** Go tracks_test: a forced-only sidecar keeps the full PGS.; Go tracks_test: full en.srt + forced PGS en + full PGS en drops the full PGS and keeps the forced PGS.; Go tracks_test: embedded forced text en + forced PGS en drops the forced PGS.; Go TestSidecarLangs: '.en.forced.srt', '.en.sdh.srt', '.eng.srt' and '.en.hi.srt' are classified correctly.; Go: a title containing 'Forced' sets Forced on a newly probed SubStream.
  - **Risk:** Low. It changes which files need subtitle work (Needs.Subs), so the cached candidate list and the reclaimable figure shift slightly. The cache key covers this after a deploy; call invalidateLibraryCache at startup if needed.
  - **Resolves:** convert-9
<a id="conv-05"></a>
- [ ] **CONV-05 · Truthful Convert copy and an archived plan doc** — `P1` · `S` · Phase 2
  - **Problem:** Several statements on the Convert page are untrue:
- The header (Convert.tsx:133-137) says 'every audio track [is] copied untouched' even when the language or commentary filters remove tracks.
- It promises the result 'must look the same' (Convert.tsx:247), backed only by four 15 s SSIM windows at 0.97.
- The header never mentions that Dolby Vision is removed.
- Format says HEVC 'plays everywhere' (Convert.tsx:951).
- The crop hint says 'only what's black across the whole film is removed' (Convert.tsx:959).
- The request toasts say a file 'starts right away, whatever the hours' (Convert.tsx:459, 464), even when it waits behind a frozen auto job.
- The analyze.go:1-4 package comment describes an old 'first slice… Save space preset'.
- CONVERT-QUALITY-PLAN.md still promises DV RPU re-injection (line 63) and a first-run chooser (155-180) that were never built.
  - **Approach:** Rewrite the strings to describe today's behaviour; later tasks update their own strings.
    - Header: 'Makes your library smaller at a quality checked against the original. Wasteful video is re-encoded to HEVC… Audio is never re-encoded: Atmos, TrueHD and DTS-HD pass through, and tracks you filter out in Settings are removed. HDR10, HDR10+ and HLG are kept; Dolby Vision files keep their HDR10 base and lose the Dolby Vision layer.'
    - How it works: 'checked against the original on four sampled scenes (average SSIM ≥ 0.97) and must save at least 20%'. [CONV-17](#conv-17) updates the numbers.
    - Format desc: 'plays on most devices made since ~2016'. Add a DV line with the count from LibraryStats.Total.DolbyVision (index.go:458).
    - Crop hint: 'bars that were black in all 40 sampled frames are removed — a brief wider shot between samples could still be trimmed'.
    - HDR column 'DV → HDR10' title: 'the Dolby Vision layer is removed when converted'.
    - Request toast (Convert.tsx:459): '“X” is first in line — it starts as soon as a conversion slot is free (a paused automatic file holds its slot until it finishes)'. Fix the bulk confirm text at :464 to match. [CONV-14](#conv-14) replaces both with the real position.
    - Delete the stale package comment in analyze.go; service.go:1-4 is the canonical one.
    - Move CONVERT-QUALITY-PLAN.md to docs/archive/CONVERT-QUALITY-PLAN.md with a header listing what shipped and what didn't: DV re-injection (now [CONV-29](#conv-29)), the first-run chooser (now [CONV-13](#conv-13)) and the throughput forecast (now [CONV-22](#conv-22)).
    The bin wording belongs to [CONV-02](#conv-02). This task's acceptance lists every touched string, so later tasks know what to keep true.
  - **Files:** `web/src/pages/Convert.tsx`, `internal/convert/analyze.go`, `CONVERT-QUALITY-PLAN.md`, `docs/archive/CONVERT-QUALITY-PLAN.md`
  - **Acceptance:**
    - Every sentence in the Convert page header, How it works, Format, Audio & subtitles, the crop hint and the request toasts is literally true for current behaviour, reviewed against keptAudio/keptSubs, planWarnings, quality.go, crop.go and runner.go.
    - Dolby Vision removal is stated before conversion, with the library's DV count.
    - The plan doc is archived and marked superseded, listing its unshipped items and the tasks that now own them.
  - **Tests:** UI check: read every Convert page string against the code paths it describes.; go vet and go build pass after the doc-comment change.
  - **Depends on:** COPY
  - **Risk:** Copy goes stale again as CONV-08/14/15/16/17 change behaviour. Each of those tasks must update its own strings. If CONV-02 lands first, don't overwrite its bin wording.
  - **Resolves:** convert-4, convert-8, convert-12, convert-14, convert-15

#### Milestone: M2 — Every conversion recorded and undoable

_A durable ledger of every conversion. Originals sit in a same-disk hold outside the bin, with retention and a budget that pauses conversions instead of deleting. One-click Revert, a History tab, and crash-safe swaps with no orphan .arrpart files._

<a id="conv-06"></a>
- [ ] **CONV-06 · Persist a convert_history ledger of every conversion outcome** — `P1` · `M` · Phase 1
  - **Problem:** Job history is in memory and capped at 200 (service.go:38 maxJobHistory), so the Overview 'Recent' card is empty after every restart. The activity log (logstore.go) does record source/output spec, SSIM and warnings, but it is unstructured, capped at 5,000 lines and floodable. Nothing stores the per-file before/after spec, per-window SSIM, crop, dropped tracks by name, the source release or where the original went. Revert, the History tab, the upgrade baseline and the time forecast all need this.
  - **Approach:** Migration NNNN_convert_history.sql creates convert_history with these columns:
    - id INTEGER PRIMARY KEY AUTOINCREMENT, item_key TEXT NOT NULL, kind TEXT NOT NULL.
    - movie_id, version_id, series_id, season and episode: INTEGER NOT NULL DEFAULT 0.
    - title TEXT, outcome TEXT NOT NULL (in_progress|done|failed|skipped|cancelled), outcome_kind TEXT, note TEXT, requested INTEGER.
    - src_path TEXT, src_release TEXT, src_size INTEGER, src_info_json TEXT.
    - out_path TEXT, out_size INTEGER, out_info_json TEXT, codec TEXT, crf INTEGER, encoder TEXT, pix_fmt TEXT.
    - ssim_mean REAL, ssim_min REAL, ssim_windows_json TEXT, vmaf_mean REAL, crop TEXT.
    - kept_tracks_json TEXT, dropped_tracks_json TEXT, warnings_json TEXT, reclaim_deferred INTEGER DEFAULT 0.
    - hold_path TEXT DEFAULT '', hold_until INTEGER DEFAULT 0, hold_state TEXT DEFAULT '', released_at INTEGER DEFAULT 0, reverted_at INTEGER DEFAULT 0.
    - started_at INTEGER, finished_at INTEGER, encode_secs REAL.
    - Indexes on (item_key, finished_at), (finished_at) and (hold_state).
    
    New history.go: historyStore{db} with:
    - begin(ctx, job) (id): writes an in_progress row. finalizeOutput calls it before the swap so [CONV-08](#conv-08)'s hold can name its folder by id.
    - finish(ctx, rec).
    - list(ctx, HistoryFilter{Outcome, Media, Q, Before, Limit}) (rows, nextCursor).
    - get(ctx, id), setHold(ctx, id, path, until, state), prune(ctx).
    
    preset.go: factor the existing keep logic into trackDecisions(mi, plan) (audio, subs []TrackDecision{Type, Index, Codec, Lang, Title, Channels, Forced, Image, Keep, Reason}). keptAudio and keptSubs become filters over it, so the ledger, the plan view ([CONV-12](#conv-12)) and the encode can never disagree. Reasons: 'language not kept', 'commentary', 'image subtitle covered by text', 'image subtitles removed', 'kept — nothing else would remain'.
    
    Job gets an unexported *jobRecord {srcInfo, outInfo *MediaInfo; plan Plan; ssimWindows []float64; encodeStart, encodeEnd time.Time; srcRelease string; historyID int64}, filled in process() and finalizeOutput. computeSSIM returns (mean float64, windows []float64, err); this change is shared with [CONV-17](#conv-17). src_release is read before markConverted: for movies, the movies.Versions() entry whose FilePath == src gives SourceRelease; for episodes, series.CurrentEpisodeFile(...).SourceRelease.
    
    finish() writes the row, outside s.mu, for:
    - done;
    - non-transient failed;
    - skips after an encode (finishAfterEncode and verifyOutput's not_smaller);
    - cancelled jobs whose encode had started.
    Pre-encode skips (already_target, hardlinked, space) write nothing; Problems covers those. Run() turns rows left in_progress by a crash into failed 'interrupted by a restart' ([CONV-08](#conv-08) refines this for held originals).
    
    In MaybeIndexSweep, prune non-done rows older than 90 days. Done rows are kept: they are the record and the revert source.
    
    API (manager role): GET /api/v1/convert/history?outcome=&media=&q=&before=&limit= (default 50, max 200) and GET /api/v1/convert/history/{id}. Add the types to web/src/lib/api.ts.
  - **Files:** `internal/store/migrations/NNNN_convert_history.sql`, `internal/convert/history.go`, `internal/convert/service.go`, `internal/convert/process.go`, `internal/convert/quality.go`, `internal/convert/preset.go`, `internal/convert/runner.go`, `internal/convert/index.go`, `internal/httpapi/convert.go`, `internal/httpapi/server.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - After a conversion and a container restart, GET /convert/history returns the row with source/output spec, sizes, SSIM per window, crop, kept and dropped tracks by language/title with reasons, warnings and the source release.
    - A not_smaller or quality_gate outcome after an encode is recorded with its reason; 'already matches' skips are not recorded.
    - keptAudio/keptSubs results are unchanged for every existing tracks_test case; the refactor is behaviour-neutral.
  - **Tests:** Go: the done path (fake encode via the existing test helpers) inserts exactly one row whose dropped_tracks_json lists the commentary track removed by drop_commentary, with reason 'commentary'.; Go: a new Service on the same DB lists the row (it survives a restart), and a leftover in_progress row becomes failed at Run().; Go: list paginates by finished_at cursor and applies the outcome/media filters.; Go: prune removes skipped rows older than 90 days and keeps done rows.; Go: the existing tracks_test and encode_test suites pass unchanged.
  - **Risk:** Row size: info_json for files with 30+ subtitle tracks is a few KB, which is fine. Keep DB writes outside s.mu. The trackDecisions refactor must be behaviour-neutral, so run the whole convert test suite. File paths in the ledger make the endpoints manager-only.
  - **Resolves:** convert-5, convert-2
<a id="conv-07"></a>
- [ ] **CONV-07 · Swap journal hardening: journal before staging, safer recovery, orphan .arrpart sweep, guarded same-stem retire** — `P1` · `S` · Phase 11
  - **Problem:** Four weaknesses in the swap:
- The swap journal row is written only after the cross-device copy to .arrpart (process.go:606-611). A crash mid-copy leaves a multi-GB orphan .arrpart in the movie folder until that file next reaches the swap.
- A failed journal write is only logged, and the swap proceeds (swaps.go:20-26).
- For non-MKV sources, recovery renames the part into place even though the original was never retired, leaving an .mp4 and an .mkv side by side (swaps.go:59-69).
- retire(finalPath) moves aside any existing same-stem .mkv without checking whether another library record owns it (process.go:618-623).
The originals hold (CONV-08) and Revert (CONV-10) build on this journal, so it has to be right first.
  - **Approach:** One migration, NNNN_convert_swaps_v2.sql: ALTER TABLE convert_swaps ADD COLUMN state TEXT NOT NULL DEFAULT 'staged', ADD hold_path TEXT NOT NULL DEFAULT '', ADD op TEXT NOT NULL DEFAULT 'convert', ADD history_id INTEGER NOT NULL DEFAULT 0. The last three are written by [CONV-08](#conv-08) and [CONV-10](#conv-10); adding them now avoids three migrations.
    swaps.go: recordSwap returns an error and takes the state. finalizeOutput calls recordSwap(state='staging') BEFORE moveFile and aborts on error: the output is discarded, the original kept, and the job ends StateFailed 'could not journal the swap'. After the copy, markStaged(part) sets state='staged'.
    recoverSwaps:
    - state 'staging' → remove the part and clear the row.
    - part exists, final missing, src != final and src still exists → the original was never retired: remove the part, keep the original, clear the row (no duplicate).
    - MKV sources and everything else behave as today.
    Orphan sweep in IndexAll: for each indexed file's directory, using the dirs already visited, remove *.arrpart files older than 24 h that no convert_swaps row references and no s.pending job owns.
    Same-stem guard: before retiring finalPath when finalPath != src, ask whether the library tracks it via new movies.Service.FileOwner(ctx, path) (movieID, versionID, ok) and series.Service.EpisodeFileOwner(ctx, path) (seriesID, ok). These are small queries on movies.movie_file_path, movie_versions.file_path and the episode file table. If it is tracked, finishSkip with a temporary reason 'another library record owns <name>.mkv'. If not, retire it as today; [CONV-08](#conv-08) moves it into the hold instead.
  - **Files:** `internal/convert/process.go`, `internal/convert/swaps.go`, `internal/convert/index.go`, `internal/store/migrations/NNNN_convert_swaps_v2.sql`, `internal/movies/service.go`, `internal/movies/repo.go`, `internal/series/service.go`, `internal/series/repo.go`
  - **Acceptance:**
    - Killing the process mid-staging leaves no orphan .arrpart after the next startup.
    - Recovery for an MP4 source whose original was never retired keeps the .mp4 and leaves no .mkv duplicate.
    - A failed journal write aborts the swap with the original untouched.
    - A conversion never moves aside a same-stem .mkv that another library record points at.
  - **Tests:** Go swaps_test: a 'staging' row with a partial part leaves the part removed and the row cleared.; Go: MP4 case with src present, part present and final absent removes the part and keeps src.; Go: a recordSwap error (closed DB) aborts before any file is moved.; Go: the orphan sweep removes an old unjournaled .arrpart and keeps a journaled one and one owned by a pending job.; Go: a tracked same-stem file leads to a skip; an untracked one is retired.
  - **Depends on:** [CONV-06](#conv-06)
  - **Risk:** Low. The sweep must never touch a part belonging to a running job, so check s.pending under s.mu and the journal. FileOwner queries must handle Windows path case in tests only; production is Linux.
  - **Resolves:** convert-13
<a id="conv-08"></a>
- [ ] **CONV-08 · Originals hold: keep converted originals on the same filesystem with retention, outside the recycle bin** — `P1` · `L` · Phase 11
  - **Problem:** retire() (service.go:730) moves originals into the shared recycle bin. Its default 50 GB cap is enforced hourly oldest-first (recyclebin/service.go:244, 322-341), so a 60-110 GB 4K original pushes out everything older and is then deleted itself. Restore refuses when the original path is occupied (recyclebin 139-141), which is always true for MKV sources (finalPath == src, process.go:603). With the bin off the original is hard-deleted (service.go:733-738). Convert needs its own same-filesystem hold, outside the bin cap, with its own retention.
  - **Approach:** hold.go:
    - SetLibraryRoots(roots ...string) and SetDataDir(dir), called from main.go with cfg.MoviesDir, cfg.TVDir and cfg.LibraryDir (fallback), plus the DB dir. holdRoot(src) = the longest configured root containing src (filepath.Rel with no '..'). Fallback: walk up from filepath.Dir(src) while sameDevice() holds ([CONV-01](#conv-01)). A root under the data dir is refused with finishSkip temporary 'no_hold' and a reason, because media must never be at /data.
    - hold(ctx, src, historyID) (held string, err error): mkdir <root>/.arrmada-hold/<historyID>/, ensure <root>/.arrmada-hold/.plexignore containing '*', then os.Rename(src, held). On EXDEV, fall back to moveFile (copy+fsync+remove) and log 'slow hold: copied N GB across filesystems'.
    - holdSelfCheck(): at Run() start, for each root, create a 1 KB probe file in the hold dir, rename it within the root, compare devices and inodes, then delete it. Expose the result as hold_check [{root, instant, err}] in GET /convert/hardware and log it. This verifies the owner's Unraid mounts without touching a library file.
    
    finalizeOutput, after [CONV-07](#conv-07), runs in this order:
    1) history.begin → id;
    2) recordSwap(staging, history_id) → moveFile(dst, part) → markStaged;
    3) held := hold(src, id) → journal hold_path;
    4) an untracked same-stem file also goes to the hold;
    5) os.Rename(part, final) → markConverted;
    6) history.setHold(id, held, now+holdDays, 'held') → clearSwap.
    Any failure before the rename moves the held original back (os.Rename(held, src)) and discards the part.
    
    recoverSwaps:
    - hold_path set, final missing, part present → finish the rename, as today.
    - Part missing → move the held file back to src and mark the ledger row failed 'interrupted — original restored'.
    - Final present → repoint as today and setHold 'held'.
    
    Setting convert_hold_days (default 14; read in settings.go/decide.go). 0 = no undo: the held file is deleted right after markConverted succeeds and the ledger row is 'released'.
    
    Expiry: Service.ExpireHolds(ctx), registered in main.go as sched.Register('convert-hold-expire', time.Hour, true, …). For held rows past hold_until: remove the file and its <id> dir, set released + released_at, then addReclaimed(src_size − out_size) unless reclaim_deferred. A held file found missing becomes 'missing' (logged, not counted).
    
    Space accounting: remove the swap-time addReclaimed (process.go:641-645); bytes count when released.
    
    Remove [CONV-02](#conv-02)'s bin_full guard and the SetBinHeadroom wiring. Convert no longer sends originals to the bin.
    
    Scanners: confirm Arrmada's own walkers skip the dot dir: the movies library scan (movies/service.go:211 already skips dot names), the series root scan, httpapi/library_paths.go:113, setup.go:100 and [CONV-07](#conv-07)'s .arrpart sweep. Add a test for the series scan.
    
    Copy: the How-it-works bullet becomes 'originals are kept for 14 days on the same disk, so a conversion can be undone'.
  - **Files:** `internal/convert/hold.go`, `internal/convert/process.go`, `internal/convert/service.go`, `internal/convert/swaps.go`, `internal/convert/settings.go`, `internal/convert/decide.go`, `internal/convert/runner.go`, `internal/convert/history.go`, `internal/convert/hardlink_linux.go`, `internal/httpapi/convert.go`, `cmd/arrmada/main.go`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A converted original is moved (same inode, instant) into <library root>/.arrmada-hold/<id>/ and is not in the recycle bin. Its ledger row says held, with hold_until = now+14 d.
    - Plex does not list the held file (dot folder plus .plexignore), and no Arrmada scan imports or flags it.
    - Killing the process between hold and rename leaves either the converted file in place with the record repointed, or the original back at its path — never neither.
    - After hold_until the hourly job deletes the original and marks it released, and only then does 'Space saved' increase.
    - Convert → Settings shows whether each library root supports an instant move.
  - **Tests:** Go: hold() on one filesystem keeps the inode (os.SameFile before/after), writes .plexignore and lands in .arrmada-hold/<id>/.; Go: holdRoot picks the longest matching root and refuses a root under the data dir.; Go: recoverSwaps with hold_path set and the part missing restores the original; with the part present it completes the rename, for both MP4 and MKV sources.; Go: ExpireHolds deletes a row past hold_until, sets released and increases Reclaimed by src−out; a missing held file becomes 'missing' with no Reclaimed change.; Go: with hold_days=0, the held original is deleted only after markConverted succeeds.; Go: the series root scan ignores .arrmada-hold.; go test -race in Docker (expiry vs finalize).
  - **Depends on:** [CONV-06](#conv-06), [CONV-07](#conv-07), [CONV-01](#conv-01), SAFE
  - **Risk:** On Unraid user shares, a rename inside one /mnt/user mount stays on the file's disk and is instant; the self-check proves this per root, and EXDEV falls back to a slow copy. Per-library mounts mean one hold per root. The hold must never sit under /data. Until CONV-09 there is no budget, but held originals occupy only the space they already used, and CONV-01's library-volume check covers the new output. Coordinate with SAFE's per-filesystem bins so both use one holdRoot/sameDevice helper.
  - **Resolves:** convert-2
<a id="conv-09"></a>
- [ ] **CONV-09 · Hold budget that pauses conversions, Release now, and freed vs held space** — `P1` · `M` · Phase 11
  - **Problem:** Even with a hold (CONV-08), the owner needs a visible budget and a way to free space early. Conversions must pause, not delete originals, when the hold is full. 'Space saved' counted savings at swap time even while the original still occupied the disk (process.go addReclaimed), and the Overview card can't show what is freed and what is merely held.
  - **Approach:** - Setting convert_hold_max_gb (default 500; 0 = no limit).
    - [CONV-01](#conv-01)'s spaceCheck adds a budget check: if heldBytes (SELECT SUM(src_size) FROM convert_history WHERE hold_state='held') + mi.SizeBytes > budget, call finishSkip(SkipHoldFull='hold_full', temporary). Its retryAt is the earliest hold_until, via skipStore.record's override. Reason: 'the originals hold is full (480 of 500 GB) — this converts after the next original is released on 14 Oct, or raise the budget / release some in History'.
    - runner.Status: when auto is on and the best remaining candidate is waiting on hold_full, State becomes 'paused' with the message 'Paused — the originals hold is full (480 of 500 GB); next release in 3 days'.
    - POST /api/v1/convert/history/{id}/release (manager) deletes the held file now, marks it released and calls addReclaimed. POST /api/v1/convert/history/release?older_than_days=N releases in bulk.
    - GET /convert/hardware adds held_bytes, held_saving_bytes (Σ src−out), held_count, next_release_at and budget_bytes.
    - Overview 'Space saved' card (Convert.tsx:211-215): 'X freed', plus a second line 'Y held so you can undo · N files · next release in D days' and a hold meter against the budget, in the existing card style.
    - Settings: a new 'Undo' section with 'Keep originals for [14] days' (0 shows 'Originals are deleted once the conversion is verified — there is no undo'), 'Hold up to [500] GB', and the hold_check result per library root. Add both keys to SETTING_KEYS (Convert.tsx:902).
    - Problems: a label for hold_full.
  - **Files:** `internal/convert/hold.go`, `internal/convert/process.go`, `internal/convert/runner.go`, `internal/convert/settings.go`, `internal/convert/skips.go`, `internal/convert/history.go`, `internal/httpapi/convert.go`, `internal/httpapi/server.go`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - The Overview shows freed and held space separately, and the held figure matches the files on disk.
    - When the budget would be exceeded, the next conversion is paused with a clear message, no original is deleted, and the file retries right after the next release.
    - Release now deletes one held original and moves its bytes from held to freed.
    - The Settings 'Undo' section saves both values, and 0 days shows the no-undo warning.
  - **Tests:** Go: the budget check with 480 GB held plus a 40 GB source under a 500 GB budget records hold_full, with retry_after equal to the earliest hold_until, and leaves the source in place.; Go: Status reports 'paused' with the hold message when the top candidate waits on hold_full.; Go: the release handler deletes the file, sets released and increases Reclaimed by src−out.; UI check: the Space saved card shows freed and held, and the Undo section round-trips via Save.
  - **Depends on:** [CONV-08](#conv-08), [CONV-01](#conv-01)
  - **Risk:** Low. The 500 GB default is a guess for the owner's array, so state it in the release notes and let the owner set it. Bulk release is irreversible, so it needs a confirm dialog.
  - **Resolves:** convert-2, convert-15
<a id="conv-10"></a>
- [ ] **CONV-10 · One-click Revert of a conversion from the hold** — `P1` · `M` · Phase 11
  - **Problem:** No code in internal/convert can undo a conversion. Recycle-bin Restore refuses when a file exists at the original path, which is always the case for MKV sources (finalPath == src). For MP4 sources, a restore leaves a duplicate while the DB still points at the .mkv. The owner believes every conversion can be undone.
  - **Approach:** revert.go adds Service.Revert(ctx, historyID) error.
    1) Load the ledger row. Require outcome done, hold_state 'held', the held file present at src_size, and the library record still at out_path with out_size (resolveSource for the row's item; version-aware once [CONV-24](#conv-24) lands). Otherwise return a typed error shown verbatim: 'the library file has changed since (an upgrade or re-import), so reverting would undo that'.
    2) Claim the item with a new claimItem(key) (release func, ok bool). It inserts a sentinel into s.pending without adding a visible Job, refuses while the runner holds the item, and is released on exit.
    3) Journal in convert_swaps with op='revert', history_id and hold_path (columns from [CONV-07](#conv-07)).
    4) MKV source (out_path == src_path): rename the converted file to <hold dir>/<name>.converted.mkv, then rename held → src_path. Otherwise rename held → src_path, then move out_path into the hold dir.
    5) DB for movies: movies.RepointMovieFile(ctx, movieID, out_path, src_path, src_size, ''), then restore source_release with a new movies.Service.SetFileSourceRelease(ctx, movieID, versionID, rel) that wraps repo.SetSourceRelease/SetVersionSourceRelease. For episodes: series.RepointEpisodeFile (by path, so multi-episode files follow) plus SetEpisodeSourceRelease for each episode sharing the path. The value restored is converted_from_release when [CONV-20](#conv-20) has landed (clear the baseline columns too), else the ledger's src_release.
    6) skips.record(key, SkipReverted='reverted', permanent, 'you reverted this conversion') so auto never picks it again. A hand Request clears it, because Request already clears skips.
    7) Ledger: hold_state 'reverted', reverted_at. Delete the converted copy only after the repoint succeeds. Then reindex, measured.forget(src), invalidateLibraryCache and log 'Reverted X'. [CONV-21](#conv-21) publishes convert.reverted.
    8) recoverSwaps handles op='revert': if the files were swapped but the DB wasn't, finish the repoint; if neither rename completed, roll back to the converted state.
    API: POST /api/v1/convert/history/{id}/revert (manager). Library API rows with a held ledger row carry revert_history_id. UI: a Revert button on converted Library rows and on History rows ([CONV-11](#conv-11)). Its confirm dialog states exactly what will happen: 'puts back the original 74.2 GB H.264 remux, removes the 21.3 GB HEVC file, and stops Convert from picking this film again'.
  - **Files:** `internal/convert/revert.go`, `internal/convert/swaps.go`, `internal/convert/skips.go`, `internal/convert/service.go`, `internal/convert/history.go`, `internal/movies/service.go`, `internal/movies/repo.go`, `internal/series/service.go`, `internal/httpapi/convert.go`, `internal/httpapi/server.go`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Reverting an MKV-source conversion puts the byte-identical original back at the same path. The movie's file path and source_release match their pre-conversion values, and the item no longer appears in Up next.
    - Reverting an MP4-source conversion removes the .mkv, restores the .mp4 and repoints the DB.
    - Revert is refused, with a clear message, when the file was upgraded or replaced after the conversion.
    - A crash mid-revert is reconciled at startup from the journal.
  - **Tests:** Go: round trip on temp files for an MKV source (same path); compare checksums and the movie record.; Go: round trip for an MP4 source leaves no .mkv, and the DB points at the .mp4.; Go: Revert refuses when the movie record's path or size differs from out_path/out_size.; Go: a two-episode file is repointed for both episodes.; Go: recoverSwaps with op='revert' and a half-done revert completes or rolls back consistently.; UI check: the Revert confirm dialog, and the row changing to 'reverted'.
  - **Depends on:** [CONV-06](#conv-06), [CONV-07](#conv-07), [CONV-08](#conv-08)
  - **Risk:** Must not race with an import landing on the same item. The pending claim plus the path/size precondition covers it. Restoring source_release must not re-trigger the upgrade loop: with CONV-20, the restored name is the true original, which is correct.
  - **Resolves:** convert-2
<a id="conv-11"></a>
- [ ] **CONV-11 · History tab backed by the ledger, replacing the in-memory Recent card** — `P1` · `M` · Phase 11
  - **Problem:** The Overview 'Recent' card (Convert.tsx:286-310) reads in-memory jobs and is empty after every restart. There is no place to answer 'what did Convert do to The Two Towers last week?', or to act on it with Revert or Release.
  - **Approach:** Add a 'History' tab between Problems and Activity (TABS at Convert.tsx:120). It lists /convert/history rows newest first. Each row shows:
    - a 32px poster (the handler fills poster_url from convert_library by movie_id/series_id);
    - the title (with S/E for episodes), the date and an outcome chip;
    - before→after spec (codec · res · HDR · size) and saved %;
    - the SSIM mean and worst window;
    - dropped tracks by language/title and the warnings;
    - the hold status: 'held · 11 days left', 'released', 'reverted' or 'missing'.
    Actions are Revert ([CONV-10](#conv-10)) and Release now ([CONV-09](#conv-09)), shown only when hold_state is 'held'; they ship hidden until those tasks land. Filters: outcome, Movies/TV and search, with 'Load more' cursor pagination. The Overview 'Recent' card shows the last 8 ledger rows plus a 'See all in History' link. Keep the existing card/table styling, tokens and type scale, and use the FE kit's Table/Tabs if they have landed.
  - **Files:** `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`, `internal/httpapi/convert.go`
  - **Acceptance:**
    - After a restart, the Overview Recent card and the History tab still show previous conversions.
    - Each History row shows before/after spec, SSIM, dropped tracks and hold status.
    - Revert and Release buttons appear only when hold_state is 'held'.
  - **Tests:** Go: the history list handler returns the cursor, poster_url, and respects the outcome/media filters.; UI check: History renders done/failed/skipped rows, filters work, and Load more appends without duplicates.
  - **Depends on:** [CONV-06](#conv-06), FE
  - **Risk:** Low. Follow the FE epic's component kit if it lands first. Otherwise reuse Convert.tsx's existing card/table classes.
  - **Resolves:** convert-5

#### Milestone: M3 — Plan first, and 'Convert now' means now

_Each file shows its conversion plan before anything runs: tracks kept or removed and the warnings. Convert now and Switch on ask for confirmation with real counts. A hand request starts within seconds, even with a frozen auto job. Dolby Vision and black-bar crop become explicit, safe choices._

<a id="conv-12"></a>
- [ ] **CONV-12 · Conversion plan view: per-file plan API, expandable plan card, posters** — `P1` · `M` · Phase 11
  - **Problem:** A Library row shows only codec/res/HDR/bitrate/size/Saves plus a 'tracks' tag whose details are in a hover title. The backend already knows which tracks go (keptAudio/keptSubs), the warnings (planWarnings: CC, DV) and the container change. None of it is shown before an in-place replacement. poster_url is returned (index.go:223) but unused.
  - **Approach:** Backend (plan.go/decide.go): Service.PlanView(ctx, key) builds from the index row's info_json, prefs.planFor and trackDecisions ([CONV-06](#conv-06)). There is no probing; the only I/O is the sidecar ReadDir. It returns {video: {from_codec, from_res, from_hdr, from_bits, to_codec, to_bits, crf, est_bytes, measured, crop_note, hdr_note}, audio: [TrackDecision], subs: [TrackDecision], container: {from, to}, warnings: [], eta_sec: null}; [CONV-22](#conv-22) fills eta_sec. It uses the same code path as the runner and adds no decision logic of its own.
    Extend planWarnings (preset.go:478):
    - container change: MP4/AVI/M2TS → MKV;
    - 8-bit → 10-bit on CPU encodes ([CONV-26](#conv-26) makes this conditional);
    - CC loss (exists);
    - DV wording 'Dolby Vision layer removed — kept as HDR10' (exists; [CONV-15](#conv-15)/29 adjust it);
    - 'black bars are checked at conversion time' when crop is on;
    - image subs dropped because a sidecar exists;
    - forced subs removed ([CONV-04](#conv-04)).
    GET /api/v1/convert/plan?key= (manager).
    UI: a chevron on each Library row expands an inline 'Conversion plan' card. It shows video before→after with estimated or measured size (the Needs.Measured flag), each audio and subtitle track marked kept or removed with language/title/reason, and the warnings in var(--avoid). Add 32px posters to Library rows from poster_url. Use the existing card/table styling.
  - **Files:** `internal/convert/plan.go`, `internal/convert/preset.go`, `internal/convert/decide.go`, `internal/convert/index.go`, `internal/httpapi/convert.go`, `internal/httpapi/server.go`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Before any conversion, expanding a DV remux row shows video before→after, each audio/subtitle track marked kept or removed with language/title and a reason, and the DV warning.
    - For a converted fixture, the plan's kept/dropped tracks match the ledger row and the output probe.
    - Library rows show posters.
  - **Tests:** Go: PlanView for a fixture (MP4, DV P8, CC, a commentary track, a forced PGS, a sidecar) returns the right keep flags, reasons, container change and warnings.; Go: PlanView's kept tracks equal keptAudio/keptSubs for every tracks_test fixture.; UI check: expand/collapse, long track lists and mobile width (no horizontal scroll).
  - **Depends on:** [CONV-06](#conv-06), [CONV-04](#conv-04)
  - **Risk:** PlanView must reuse planFor/trackDecisions, or the plan and the real result could disagree. Crop and the HEVC/AV1 pick are decided at conversion time, so the card says 'checked at conversion time' rather than guessing.
  - **Resolves:** convert-5, convert-4
<a id="conv-13"></a>
- [ ] **CONV-13 · Confirm before converting: Convert-now dialog and a Switch-on summary with library-wide counts** — `P1` · `S` · Phase 11
  - **Problem:** 'Convert now', 'Fix tracks' and 'Switch on' fire on one click with no confirmation (Convert.tsx:110-118, 454-462). The overhaul and the archived plan doc call for a first-run summary: N files, X TB → ~Y TB, how many lose DV or CC. Switching auto on starts irreversible in-place replacements across the whole library.
  - **Approach:** GET /api/v1/convert/auto-preview[?series=&season=] aggregates computeCandidates (the cached candCache). Add per-candidate flags to autoCand where plan and mi are at hand in computeCandidates: dv, cc, imageSubsDropped, audioDropped, containerChange, forcedDropped, estBytes. Response: {files, bytes, est_bytes, dv_files, cc_files, image_sub_tracks, filtered_audio_tracks, container_changes, forced_tracks}.
    'Convert now' / 'Fix tracks' (Convert.tsx:454-462) opens a confirm dialog with one line per change, taken from PlanView, and the queue position once [CONV-14](#conv-14) is in. Bulk season/show convert (Convert.tsx:464) shows auto-preview counts filtered by series/season. 'Switch on' (toggleAuto in the header) opens a modal: 'N files, X TB → ~Y TB; N lose Dolby Vision; N lose closed captions; N image-subtitle tracks removed; N audio tracks filtered', with a Confirm button. Auto is enabled only on Confirm, and Switch off stays one click. [CONV-25](#conv-25) adds 'devices that may transcode' to this modal. Replace window.confirm with the FE kit's ConfirmDialog if it exists; otherwise build a local dialog on the existing tokens.
  - **Files:** `internal/convert/runner.go`, `internal/convert/plan.go`, `internal/httpapi/convert.go`, `internal/httpapi/server.go`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - 'Convert now' requires a confirmation that lists what will change.
    - 'Switch on' shows library-wide counts and enables auto only after Confirm; Cancel leaves the settings unchanged.
    - Bulk season convert shows counts for that season.
  - **Tests:** Go: auto-preview counts on an index fixture with 3 DV files, 1 CC file and 2 commentary tracks, with and without a series filter.; UI check: the Convert-now confirm, the Switch-on modal and Cancel paths.
  - **Depends on:** [CONV-12](#conv-12), FE
  - **Risk:** Low. Counts come from the cached candidates, so they can be minutes stale after a settings change; say 'about'.
  - **Resolves:** convert-5
<a id="conv-14"></a>
- [ ] **CONV-14 · Requested files never wait behind a frozen auto job (request lane, broadcast wake)** — `P1` · `S` · Phase 11
  - **Problem:** Workers only pick when free, and only those with idx < workerCount (default 1; runner.go:61). Outside the hours an auto job is SIGSTOPped in place, so worker 0 sits in cmd.Wait(). A hand-picked request then waits in 'Up next' until the auto job finishes, possibly nights later, while the UI says it 'starts right away, whatever the hours' (Convert.tsx:459). Status meanwhile reads 'Paused — outside your encode hours'. The wake channel has capacity 1 (service.go wake), so only one idle worker is nudged.
  - **Approach:** runner.go: Run starts maxWorkers+1 goroutines. Worker idx == maxWorkers is the request lane. Its nextJob calls only a new pickRequest(ctx), which claims from s.requests, and only when every normal slot is busy and no other job is on the lane. That way requests never queue behind auto jobs, and at most one extra encode runs.
    pauseReason(job, p, watching, laneBusy): while the lane is in use, an auto (non-requested) job returns 'waiting for your requested file to finish'. The frozen auto job stays SIGSTOPped and resumes afterwards, if the hours allow. applyPauses computes laneBusy under s.mu, and waitAllowed uses a locked helper.
    Replace the cap-1 wake chan with a broadcast: wakeCh() returns a chan that wakeUp closes and replaces under a small wakeMu, so every idle worker re-checks.
    POST /convert/requests returns {position, ahead}. The toast says '“X” is starting now' or '“X” is #2 — after “Y”'. Replace [CONV-05](#conv-05)'s interim toast and bulk copy (Convert.tsx:459, 464) with these accurate messages. Status shows requested jobs running even while auto is paused.
  - **Files:** `internal/convert/runner.go`, `internal/convert/service.go`, `internal/convert/requests.go`, `internal/httpapi/convert.go`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With an auto 4K job paused outside the hours, clicking Convert now on an episode starts it within seconds. The auto job's status reads 'waiting for your requested file to finish'.
    - When the request completes, and the hours allow, the auto job resumes where it was.
    - The toast text matches what actually happens: position in line and who is ahead.
  - **Tests:** Go: runner test with workerCount=1, an active paused auto job and a request added → the lane's pickRequest claims it after one wake.; Go: pauseReason for an auto job is non-empty while the lane is busy and empty after it finishes (inside the hours).; Go: a broadcast wake releases all waiting nextJob loops (goroutine test with a timeout).; go test -race in Docker (runner concurrency).
  - **Depends on:** [CONV-05](#conv-05)
  - **Risk:** Two encodes' working sets sit in RAM, because the frozen auto job keeps its memory. That is acceptable on the owner's 285K but should be documented. Watch for deadlock between s.mu and wakeMu: never take wakeMu while holding s.mu.
  - **Resolves:** convert-6
<a id="conv-15"></a>
- [ ] **CONV-15 · Dolby Vision as an explicit, counted choice: leave alone or convert and drop the DV layer** — `P2` · `M` · Phase 11
  - **Problem:** Every DV file is converted with its DV layer dropped. That was the owner's deliberate Oct-1 choice, but there is no way to protect DV titles for a DV-capable TV short of not converting at all. Once converted, Facts reports DolbyVision=false (facts.go:68), so profiles that prefer DV mark the file as not fitting.
  - **Approach:** New setting convert_dolby_vision: 'drop' (today's behaviour, and the default so nothing changes silently) or 'skip' (leave DV files alone). The 'keep' value is reserved and added by [CONV-29](#conv-29).
    decide.go: add dvPolicy to prefs and to cacheKey. In needsOf/videoWorth, when mi.HDR == 'Dolby Vision' and the policy is skip: Video=false, Why='Dolby Vision — left alone (Convert → Settings → Format)'. Track-only tidies still apply.
    Format section: a radio group 'Dolby Vision files (N in your library): Leave them alone / Convert and drop the Dolby Vision layer (keeps HDR10)', with N from LibraryStats.Total.DolbyVision.
    The Conversion plan ([CONV-12](#conv-12)) adds a warning when the item's quality profile prefers DV: 'your profile prefers Dolby Vision — after conversion this file won't fit it'. Use a new convert.SetProfilePrefersDV(func(ctx, kind string, id int64) bool), wired in main.go from quality.Service. The auto-preview ([CONV-13](#conv-13)) counts DV files under the current policy.
  - **Files:** `internal/convert/decide.go`, `internal/convert/settings.go`, `internal/convert/plan.go`, `internal/convert/index.go`, `internal/convert/service.go`, `cmd/arrmada/main.go`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With 'Leave them alone', no DV file appears in Up next or in the reclaimable figure, and its Library row says why.
    - The Format section shows the DV file count and the current choice.
    - Switching the policy updates the Library and stats without a rescan.
  - **Tests:** Go decide_test: DV P8 with policy skip → not Worth, with Why set; with drop → Worth by bitrate.; Go: the prefs cacheKey changes with the policy.; UI check: the radio group saves, and the count matches stats.
  - **Depends on:** [CONV-12](#conv-12), [CONV-13](#conv-13), QUAL
  - **Risk:** Low. The default must stay 'drop' for the existing install unless the owner chooses otherwise. The profile lookup is optional; skip the warning if QUAL's profile API isn't stable yet.
  - **Resolves:** convert-4
<a id="conv-16"></a>
- [ ] **CONV-16 · Black-bar crop: off by default, dense sampling, never crop films that change shape** — `P2` · `S` · Phase 1
  - **Problem:** crop defaults to true (decide.go:87) for about a 2% size gain, by the code's own measurement (crop.go:16-19). Detection takes 40 samples (crop.go:39), about one every 3 minutes on a 2 h film, and crops to their union. An IMAX or open-matte shot between samples is therefore permanently cropped, and the SSIM reference is cropped the same way, so the gate can't catch it. The UI promises 'films that change shape (IMAX scenes) keep their full frame'.
  - **Approach:** decide.go: prefs crop default becomes false (GetBool(keyCrop, false)). An explicitly saved value is respected. Log once at startup ('black-bar crop is now off unless you turned it on') for installs that never saved the setting.
    crop.go: samples = clamp(duration/25 s, 40, 400), still keyframe-only and per-seek. Measure the wall time on a synthetic 2 h lavfi file and keep it under ~2 min.
    unionCrop: among non-dark samples, if the detected picture height (or width) varies by more than 2% of the frame, the film changes shape → return nil (no crop), instead of trusting that the union covers unsampled shots.
    The crop is recorded in the ledger ([CONV-06](#conv-06)) and shown in the Conversion plan. Copy: 'Only bars black in every one of ~N sampled frames (one every ~25 s) are removed, and films whose shape changes are never cropped. A brief wider shot between samples could still be trimmed, which is why this is off by default.'
  - **Files:** `internal/convert/decide.go`, `internal/convert/crop.go`, `internal/convert/crop_test.go`, `web/src/pages/Convert.tsx`
  - **Acceptance:**
    - A fresh install has crop off.
    - A film whose samples include both 2.39:1 and 1.78:1 pictures is never cropped.
    - The settings hint describes exactly what the detector guarantees.
  - **Tests:** Go crop_test: unionCrop with mixed-aspect frames → nil; a uniform 2.39 letterbox → crop.; Go: the sample count is 288 for 7200 s, 72 for 30 min, and capped at 400.; Go: prefs crop is false when the key is unset and true when 'true' is stored.
  - **Risk:** More seeks on the array per conversion: a few hundred keyframe reads, run through runTool once CONV-18 lands. Owners who want crop must turn it on.
  - **Resolves:** convert-14

#### Milestone: M4 — A gate that earns 'looks the same', and upgrades that respect it

_Verification uses independent windows with a per-window floor and fails closed. A full-decode integrity check runs before any swap. Side steps run niced, with idle I/O and pausable. Quality upgrades judge converted files against what they originally were, so they never re-grab a shrunk remux._

<a id="conv-17"></a>
- [ ] **CONV-17 · Stricter SSIM gate: independent windows, per-window floor, fail closed** — `P1` · `S` · Phase 1
  - **Problem:** computeSSIM averages four 15 s windows (quality.go:59-77), 60 s of a 2 h film. It silently drops windows that error (42-44), so one readable window can pass a film. The mean hides a single bad scene. The bar is 0.97 (decide.go:43), while the code's own comment says 0.98+ is near-transparent. Preflight tunes the CRF on the same ssimWindows fractions (preflight.go:51, 81) with a 0.002 margin, so the final gate re-measures exactly the windows the CRF was tuned to pass.
  - **Approach:** quality.go: verifyWindows(dur) returns ~10 windows of 10 s at fractions at least 2% from preflight's {0.15, 0.38, 0.61, 0.84}, e.g. 0.07, 0.19, 0.28, 0.45, 0.52, 0.67, 0.73, 0.79, 0.90, 0.95. Short files keep one whole pass. computeSSIM(ctx, job, dst, src, crop) returns (mean, windows []float64, err) and errors if ANY window fails to measure (fail closed). Make ssimWindow a func var for tests.
    decide.go: replace minSSIM with minSSIMMean (proposed 0.98 for ≥720p, 0.975 below) and minSSIMWindow (proposed 0.96). These are proposals and need the owner's sign-off before merge. process.go: a pass requires mean ≥ bar AND worst window ≥ floor. A miss retries at higher quality as today, and the note names the failing window: 'the scene at 1:12:40 scored 0.951'.
    preflight.go: target the same mean plus its margin, and the floor, on its clips.
    Ledger: ssim_mean, ssim_min and ssim_windows_json ([CONV-06](#conv-06)). Update How it works to the real numbers: 'ten scenes, average ≥ 0.98, none below 0.96'.
  - **Files:** `internal/convert/quality.go`, `internal/convert/preflight.go`, `internal/convert/process.go`, `internal/convert/decide.go`, `internal/convert/encode_test.go`, `web/src/pages/Convert.tsx`
  - **Acceptance:**
    - A conversion is rejected, and the original kept, when any single verification window scores below the floor, even if the mean passes.
    - A window that cannot be measured fails the check instead of being skipped.
    - Final verification windows never coincide with the preflight clip windows.
    - The ledger and Activity log record per-window scores.
  - **Tests:** Go: computeSSIM with a stubbed ssimWindow: one window error → error; scores {0.99 ×9, 0.95} → fail on the floor; {0.985 ×10} → pass.; Go: verifyWindows fractions never fall within 2% of the preflight fractions.; Go integration (synthetic lavfi sources only): a deliberately degraded segment in one window fails the gate.
  - **Depends on:** [CONV-06](#conv-06)
  - **Risk:** A higher bar means lower CRF, bigger outputs and more quality_gate skips; expect some files to move from 'convert' to 'kept original'. Measure the impact on synthetic or sample clips, never the owner's library. Ten windows add a few minutes of decode per 4K film.
  - **Resolves:** convert-8
<a id="conv-18"></a>
- [ ] **CONV-18 · One tool runner for every ffmpeg/tool call: nice, idle I/O, process group, pause** — `P2` · `M` · Phase 11
  - **Problem:** Only runWithProgress and encodeClip get lowPriority/applyNice/trackProc. SSIM verification, crop detection (40+ seeks), the HDR10+ extraction pipe, the hdr10plus inject and the final remux all use bare exec.CommandContext. They run at normal priority and ignore pause-while-watching. Verification isn't gated by waitAllowed (process.go:172). Nothing sets I/O priority, so Plex viewers can buffer while these steps hammer the array.
  - **Approach:** New exec.go: s.runTool(ctx, job, bin string, args []string, opt toolOpts) ([]byte, error). It runs exec.CommandContext, then lowPriority (Setpgid), applyNice(pid, encodeNice) and ioIdle(pid), then trackProc/untrackProc so pauseLoop freezes it, with an optional waitAllowed before start. toolOpts{IOClass: idle|besteffort7, Combined bool, Stdout io.Writer}.
    ioIdle is Linux-only, in priority_linux.go: ioprio_set(IOPRIO_WHO_PGRP, pid, IOPRIO_CLASS_IDLE) via golang.org/x/sys/unix, which is already in go.mod as indirect; make it direct. No-op stubs go in priority_other.go.
    Add a pipe variant for pipeCommands (hdr.go:165) that registers both processes in the job's procs.
    Convert ssimWindow, detectCrop, extractHDR10Plus, the hdr10plus inject, both remuxVideoStream steps, trial clip scoring and [CONV-19](#conv-19)'s integrity decode. computeSSIM takes the job, and process() calls waitAllowed before verification. The final remux uses best-effort class 7 rather than idle, so the swap can't starve. ffprobe stays direct, because it is light.
  - **Files:** `internal/convert/exec.go`, `internal/convert/priority_linux.go`, `internal/convert/priority_other.go`, `internal/convert/quality.go`, `internal/convert/crop.go`, `internal/convert/hdr.go`, `internal/convert/trial.go`, `internal/convert/process.go`, `go.mod`
  - **Acceptance:**
    - During verification, crop detection or HDR10+ extraction, the ffmpeg processes run at nice 19 with the idle I/O class (visible via /proc or `ionice -p`).
    - Starting a Plex stream freezes those steps too, and they resume when it stops.
  - **Tests:** Go (linux build tag, runs in CI): runTool on `sleep 2` → getpriority(PRIO_PGRP) == 19 and ioprio_get class idle.; Go (linux): with job.Paused set, a runTool process is stopped (state 'T' in /proc/<pid>/stat) and resumes on clear.; go test -race in Docker for the procs map access.
  - **Risk:** The idle I/O class can starve verification on a busy array and lengthen jobs. That is acceptable, and the class is per call, so a step can move to best-effort 7 if starvation shows up. A frozen SSIM step holds its 25-minute timeout context, so extend or pause the timeout while the job is paused.
  - **Resolves:** convert-13
<a id="conv-19"></a>
- [ ] **CONV-19 · Full-decode integrity check before the swap, plus VMAF when the ffmpeg build has it** — `P1` · `M` · Phase 11
  - **Problem:** verifyOutput checks only duration ±2 s, track counts and size (process.go:527-558). ffmpeg exits 0 on most mid-stream decode errors, so a corrupted or truncated stretch outside the sampled SSIM windows passes, and then the original leaves the library. The quality.go:12-14 comment claims libvmaf is unavailable, which predates jellyfin-ffmpeg7 and was never re-checked.
  - **Approach:** After verifyOutput passes and before the swap, run a full decode of the output's video through runTool ([CONV-18](#conv-18)), after waitAllowed: `ffmpeg -v error -xerror -i out -map 0:v:0 -f null -`. On the Arc, add -hwaccel vaapi -hwaccel_device <vaapiDev> when the output codec decodes in hardware. If hardware init itself fails, retry on the CPU before judging, because a driver hiccup is not corruption.
    Count the decoded frames (parse -progress / the final frame=) and compare with the source's frame count. Take the source count from stream tags (Matroska NUMBER_OF_FRAMES / nb_frames), or, when absent, duration × r_frame_rate; never do a second full read of the source. Tolerance is 0.1%; VFRToCFR plans compare against duration × output fps.
    Any decode error or mismatch → finishAfterEncode(SkipQualityGate, 'the converted file has decode errors / is missing N frames — kept the original').
    VMAF: at NewService, detect libvmaf in `ffmpeg -hide_banner -filters`. If present, compute VMAF on the same verifyWindows and store vmaf_mean in the ledger. It is advisory for the first release (logged and shown in History); gating at mean ≥ 93 and min ≥ 85 comes only after the owner reviews real numbers. If libvmaf is absent, rewrite the stale comment to say what was detected.
  - **Files:** `internal/convert/quality.go`, `internal/convert/process.go`, `internal/convert/service.go`, `internal/convert/analyze.go`, `internal/convert/integration_test.go`, `web/src/pages/Convert.tsx`
  - **Acceptance:**
    - An output with a corrupted or truncated stretch outside the sampled windows is rejected by the full-decode check, and the original is kept.
    - A hardware-decode init failure falls back to the CPU instead of rejecting the file.
    - The ledger records vmaf_mean when libvmaf is present.
  - **Tests:** Go integration (synthetic lavfi sources only, never library files): corrupt bytes mid-file in a test output → integrity check fails; dropped frames → mismatch fails; a clean encode passes.; Go: source frame count from NUMBER_OF_FRAMES tags vs the duration×fps fallback.; Go: libvmaf detection parses `-filters` output with and without the filter.
  - **Depends on:** [CONV-18](#conv-18), [CONV-17](#conv-17), [CONV-06](#conv-06)
  - **Risk:** The full decode adds roughly 10-30 min per 4K film on the CPU, much less with Arc hardware decode; it is niced, idle-I/O and pausable via CONV-18. A file with wrong NUMBER_OF_FRAMES tags (a stale mkvmerge statistic) could false-fail, so fall back to duration×fps when the tag disagrees with the source duration by more than 1%.
  - **Resolves:** convert-8
<a id="conv-20"></a>
- [ ] **CONV-20 · Record a pre-conversion baseline so quality upgrades never undo a conversion** — `P1` · `M` · Phase 4
  - **Problem:** After a swap, markConverted only repoints the path and appends a codec token to source_release (process.go:672-717; movies/service.go:623-664). The upgrade sweep then uses the live converted size (coordinator.go:866-871), and IsBitrateUpgrade compares an ~80 Mb/s remux against the ~30 Mb/s converted file (quality/service.go:238-253). UpgradeCandidate excludes only a candidate equal to curKey, so other groups' remuxes are eligible. With upgrades on, upgrade_min_percent > 0 (opt-in) and no satisfied target window or ceiling, Arrmada can re-grab the remux it just shrank, an endless download → re-encode loop. QUAL's quality-4 (high) shows the appended 'AV1' token parses as H.264 and breaks the curKey/group match, which reopens the loop more widely. Episodes (stampEpisodeCodec) and the series import gate (reviews.go:828-841) have the same gap.
  - **Approach:** Migration NNNN_converted_from_baseline.sql: ADD COLUMN converted_from_release TEXT NOT NULL DEFAULT '' and converted_from_size INTEGER NOT NULL DEFAULT 0 on movies, movie_versions and episodes.
    markConverted writes them only when empty, so re-conversions keep the first original. It uses the pre-swap source_release and mi.SizeBytes, via new movies.Service.SetConvertedFrom(ctx, movieID, versionID, rel, size) and series.Service.SetEpisodeConvertedFrom(ctx, seriesID, season, episode, rel, size). They are cleared in movies.markImported (service.go:392) and in the series import path, but NOT in RepointMovieFile/SetFile, which Convert itself uses. Revert ([CONV-10](#conv-10)) clears them too. Expose the fields on movies.Version and series.EpisodeFile.
    quality.UpgradeCandidate gains a baseline: either a new struct param quality.Current{Release string; SizeGB float64; OrigRelease string; OrigSizeGB float64}, or a sibling UpgradeCandidateFrom, agreed with QUAL. TargetMet is still judged on the actual converted file. Scoring cur.Total, the curKey exclusion and IsBitrateUpgrade's current side use the ORIGINAL release and size when present, and the original release name is excluded explicitly, so a candidate must beat what the file originally was.
    Callers: coordinator.go:871 (the movie sweep; upgradeBaseline at :910 prefers converted_from_release), series_reliability.go:235 (the episode sweep), reviews.go:828-841 (the series import gate), and the movie import replacement path if it compares size or release.
    Backfill in the migration: for rows whose source_release ends with a Convert-appended ' x265' or ' AV1' token, set converted_from_release to the stripped name. Size stays 0, which means 'unknown, use current'.
    Coordinate with QUAL quality-4 (stop appending the token, or make the parser prefer the last codec token). With this baseline, the token only matters for display.
  - **Files:** `internal/store/migrations/NNNN_converted_from_baseline.sql`, `internal/convert/process.go`, `internal/movies/service.go`, `internal/movies/repo.go`, `internal/movies/movie.go`, `internal/series/service.go`, `internal/series/repo.go`, `internal/quality/service.go`, `internal/automation/coordinator.go`, `internal/automation/series_reliability.go`, `internal/automation/reviews.go`
  - **Acceptance:**
    - After converting a remux, an upgrade sweep against a release list containing that remux and another group's remux grabs nothing, with a profile that has upgrades on and upgrade_min_percent 20.
    - A release genuinely better than the original (e.g. higher resolution) is still accepted.
    - Importing a real upgrade clears the baseline, and so does Revert; Convert's own repoint does not.
  - **Tests:** Go quality: UpgradeCandidate with orig={remux 80 GB} and current={x265 30 GB} rejects another remux of equal size and accepts a 2160p candidate over a 1080p original.; Go automation: an end-to-end sweep test (fake indexer) after markConverted produces no grab, for both AV1 and HEVC conversions.; Go series: the episode sweep and the import gate honour converted_from.; Go movies: markImported clears converted_from; RepointMovieFile does not.; Go: the migration backfill strips ' x265' and ' AV1' tokens only.
  - **Depends on:** QUAL
  - **Risk:** It touches the upgrade path shared with QUAL's ranking work, so agree the UpgradeCandidate signature with QUAL first to avoid merge churn. Files converted before this ships have no size baseline, so protection is best effort only (release-name exclusion still works). Never change files on disk in the migration.
  - **Resolves:** convert-3

#### Milestone: M5 — Know what's coming

_Convert publishes lifecycle events for notifications and Plex. Up next and the Overview show per-file ETAs and a backlog forecast in nights. Small honesty and settings gaps are closed._

<a id="conv-21"></a>
- [ ] **CONV-21 · Publish Convert lifecycle events on the eventbus** — `P2` · `S` · Phase 11
  - **Problem:** internal/convert publishes nothing on the bus. Plex can't be told about swapped files. Failures, blocklists and 'scratch full' only surface if the admin opens the Convert page, and notify subscribes to five unrelated topics (notify.go:148-152).
  - **Approach:** Pass the *eventbus.Bus into convert via a new SetBus(bus), called in main.go next to SetWatching. Publish after the DB updates, using eventbus.Publish, which is non-blocking:
    - convert.done: {title, key, kind, movie_id, version_id, series_id, season, episode, path, old_path, src_bytes, out_bytes, codec, history_id}
    - convert.failed: {title, key, note}
    - convert.blocked: when failureStore.recordFailure crosses maxFailures. Change recordFailure to return the new count.
    - convert.stalled: only when skipStore.record returns attempts==1 for no_scratch, library_full or hold_full. Payload {reason, need_bytes, free_bytes}.
    - convert.reverted: from [CONV-10](#conv-10), if it has landed.
    Document the topics and payloads in a comment block next to a publish helper, for OBS's event catalog. The PLEX epic consumes convert.done for a partial refresh, and OBS routes convert.* to admin alerts.
  - **Files:** `internal/convert/service.go`, `internal/convert/failures.go`, `internal/convert/process.go`, `internal/convert/skips.go`, `internal/convert/revert.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - A successful conversion emits exactly one convert.done carrying the new path and the old path.
    - The third failure of a file emits convert.blocked once.
    - A scratch-full condition emits convert.stalled once per episode of the condition, not on every retry.
  - **Tests:** Go: subscribe to the bus in a test, run the done path with stubs → one convert.done with path and old_path.; Go: recordFailure returns counts, and the blocked event fires at maxFailures only.; Go: the stalled event fires only when attempts==1.
  - **Depends on:** [CONV-01](#conv-01), OBS
  - **Risk:** Low. Publishing must never block the worker; eventbus.Publish drops messages when a subscriber is slow, which is acceptable for notifications. Payloads carry file paths, so keep them off requester-facing channels.
  - **Resolves:** convert-11
<a id="conv-22"></a>
- [ ] **CONV-22 · Time forecast: per-file ETA in Up next, backlog in nights, and in-flight hours** — `P2` · `M` · Phase 11
  - **Problem:** Up next shows bytes saved but never time. A 4K CPU encode at ~3.8 fps takes about 24 h, which is about four nights at the default 01:00-07:00 window. Nothing tells the owner the backlog is months of nights, or that an update right now throws away 20 hours of encoding. The plan's promised 'About 3 weeks at your current speed' was never built.
  - **Approach:** throughput.go: a speed model from convert_history done rows. Throughput = pixel-seconds per wall second = (duration × width × height) / encode_secs, grouped by encoder class (cpu-hevc, cpu-av1, cpu-hevc-hdr10plus, gpu-hevc, gpu-av1) and resolution bucket (SD/HD/4K), taking the median of the last 20 rows. With no history, seed from defaults (cpu-hevc 4K ≈ 3.8 fps, scaled per pixel and by the configured core count).
    estimateSeconds(cand) adds preflight and verification overheads. nights(sec, start, end) handles windows that wrap past midnight; 'any time' means 24 h. Ignore watch pauses but label results 'about'.
    Status: UpNext items get eta_sec and nights; Status gets backlog_sec and backlog_nights (summed over the remaining candidates, cached with candCache) and in_flight_secs (the age of the longest running encode). PlanView's eta_sec ([CONV-12](#conv-12)) uses the same function.
    UI: Up next shows '~24 h · ~4 nights'. The Overview status bar shows 'Backlog ≈ 9 months of nights at 01:00–07:00'. The Settings hours hint shows the forecast at the edited hours. While in_flight_secs exceeds 3 h, the Overview shows 'a restart or update now loses ~N h of encoding'. Deploy tooling can read the same field; the update.sh change is out of scope.
  - **Files:** `internal/convert/throughput.go`, `internal/convert/runner.go`, `internal/convert/history.go`, `internal/convert/plan.go`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Up next shows a time and nights estimate per file.
    - The Overview shows a backlog forecast that changes when the hours change.
    - Estimates come from this machine's measured history once a few conversions have run.
    - A long in-flight encode shows how much a restart would lose.
  - **Tests:** Go: the estimate uses defaults with no history, and their median with 3 fixture history rows.; Go: nights() for 01:00-07:00 with 24 h of work → 4; a 22:00-06:00 wrap works; an empty window counts as 1 per day.; UI check: Up next ETA and the backlog line render.
  - **Depends on:** [CONV-06](#conv-06)
  - **Risk:** Estimates for HDR10+ and AV1 vary a lot, so show '~' and round generously. Backlog summing must reuse the cached candidates, never re-probe.
  - **Resolves:** convert-7
<a id="conv-23"></a>
- [ ] **CONV-23 · Convert polish: scan time setting, one-call group retry, honest 'done' state** — `P3` · `S` · Phase 17
  - **Problem:** scan_at exists in the API (settings.go:35, 88) but is missing from SETTING_KEYS and the form (Convert.tsx:902), while index.go:296 points users to 'Settings → Convert'. 'Try these again' for a group sends one request per item in sequence (Convert.tsx:762). The 'done' status says 'Everything's converted' even when skipped or blocklisted files remain, because Remaining excludes waiting and blocked keys (runner.go:377, 398-399). The freed/held split is handled in CONV-09.
  - **Approach:** - Add 'scan_at' to SETTING_KEYS and an Advanced field 'Daily library scan at' (time input). Fix the index.go:296 hint to 'Convert → Settings → Advanced'.
    - skipStore.clearKind(ctx, kind), plus POST /api/v1/convert/skips/clear?kind= (the existing handler accepts kind). Problems 'Try these again' makes one call.
    - runner.Status 'done': when waiting or blocked keys exist, report 'Everything convertible is done — N files need you (see Problems)', with state 'done' and a new problems count the UI links to the Problems tab.
  - **Files:** `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`, `internal/convert/skips.go`, `internal/convert/service.go`, `internal/convert/runner.go`, `internal/convert/index.go`, `internal/httpapi/convert.go`
  - **Acceptance:**
    - The daily scan time can be changed in Convert → Settings → Advanced, and the sweep runs at it.
    - 'Try these again' on a 300-file group completes in one request.
    - With problems outstanding, the status never says 'Everything's converted' unqualified.
  - **Tests:** Go: clearKind removes only that kind.; Go: Status with a permanent skip present gives a message that mentions problems and a count.; UI check: the scan_at field round-trips via Save.
  - **Depends on:** [CONV-01](#conv-01)
  - **Risk:** Low.
  - **Resolves:** convert-15

#### Milestone: M6 — Fit the household's libraries and devices

_Non-default movie versions (often the biggest remuxes) are converted. A 'Your devices' panel built from Insights warns before AV1 or 10-bit output would cause transcodes. 10-bit output for 8-bit sources is a stated choice._

<a id="conv-24"></a>
- [ ] **CONV-24 · Index and convert non-default movie versions** — `P2` · `M` · Phase 11
  - **Problem:** IndexAll and IndexMovie index only m.MovieFilePath, the default version (index.go:210-235, 257-272), and IndexMovie deletes any other row for the movie. resolveSource converts only the default (process.go:226-230). A 4K remux kept as an extra version, often the largest file, never appears in Convert's lists, stats or reclaimable figure. It is also invisible to the ideal-file check, which reads IndexedFiles.
  - **Approach:** Migration: ALTER TABLE convert_library ADD COLUMN version_id INTEGER NOT NULL DEFAULT 0.
    Keys: 'movie:ID' stays for the default; versions use 'movie:ID:v:VID'. Update parseKey/item.key/ItemKey (requests.go:72-100), movieKey/jobKey and parseItemKey (failures.go), and add Job.VersionID. The ledger already has version_id ([CONV-06](#conv-06)).
    IndexMovie iterates movies.Versions(ctx, id) (HasFile && FilePath) and upserts one row per version, with the title suffixed by the version label. The cleanup DELETE keeps rows whose path is any current version path, and IndexAll's keep set includes all version paths.
    resolveSource for VersionID>0 finds that version's FilePath; markConverted already repoints by path via RepointMovieFile. Revert ([CONV-10](#conv-10)) and the baseline ([CONV-20](#conv-20)) use version_id.
    Candidate and autoCand carry version_id and version_label. The Library shows versions as indented sub-rows under their movie, with a version chip.
    IndexedFile gains VersionID. httpapi/fit.go libraryFitMovies and fit_profiles.go keep using VersionID==0 rows only, so per-movie fit badges don't duplicate, until the MOV epic extends fit to versions. History, skip and failure keys work unchanged as strings.
  - **Files:** `internal/store/migrations/NNNN_convert_library_version.sql`, `internal/convert/index.go`, `internal/convert/requests.go`, `internal/convert/failures.go`, `internal/convert/process.go`, `internal/convert/service.go`, `internal/convert/facts.go`, `internal/convert/revert.go`, `internal/httpapi/fit.go`, `internal/httpapi/fit_profiles.go`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A movie with a default 1080p file and a 4K remux version shows both in Convert's Library, stats and reclaimable figure.
    - Converting the version repoints that version's record only; the default file is untouched.
    - Library fit badges are unchanged (no duplicates).
  - **Tests:** Go: IndexMovie with two versions creates two rows, and removing a version prunes its row.; Go: parseKey/ItemKey round-trip for 'movie:5:v:2'.; Go: resolveSource for a version job returns the version path, and markConverted updates movie_versions.file_path.; Go: libraryFitMovies output count is unchanged with versions indexed.
  - **Depends on:** [CONV-06](#conv-06), MOV
  - **Risk:** Old 'movie:ID' keys in convert_requests, skips and failures keep meaning the default version, so no key migration is needed. Coordinate with MOV's versions/editions work so labels and the version model match.
  - **Resolves:** convert-10
<a id="conv-25"></a>
- [ ] **CONV-25 · 'Your devices' panel from Insights: who direct-plays HEVC, HEVC 10-bit and AV1** — `P2` · `M` · Phase 11
  - **Problem:** Format says HEVC 'plays everywhere' (Convert.tsx:951), and the AV1 toggle asks the admin to vouch for every device. CPU encodes always output 10-bit Main10 (preset.go:695, 718), which can turn direct play into transcodes on older Rokus, browsers and 1080p sticks. Insights already records player, platform, product, source video codec and the decision for every session (insights/repo.go:25-43), but nothing uses it to warn before converting.
  - **Approach:** Insights capture: plex/sessions.go parses the source video Stream's bitDepth and profile. A new migration adds stream_sessions.video_src_bitdepth INTEGER DEFAULT 0 and video_src_profile TEXT DEFAULT '', and the poller's record() fills them.
    insights.Service.DeviceCodecSupport(ctx, since) groups the last 90 days by (product, platform, player) into buckets h264 / hevc8 / hevc10 / av1, with direct-play and transcode counts by video decision. Where Insights records a transcode reason, a bandwidth-driven transcode counts as capable.
    convert gets SetDeviceSupport(fn), wired in main.go. Add GET /api/v1/convert/devices (manager).
    Format section: a 'Your devices' table (device · last seen · HEVC · HEVC 10-bit · AV1), each cell ✓ direct-played, ✗ transcoded or ? not seen.
    Warnings: the Switch-on modal ([CONV-13](#conv-13)) and enabling AV1 list the active devices that transcoded the target in the last 90 days.
    Copy: replace 'plays everywhere' with a sentence pointing at the panel.
  - **Files:** `internal/plex/sessions.go`, `internal/insights/poller.go`, `internal/insights/repo.go`, `internal/insights/service.go`, `internal/store/migrations/NNNN_insights_src_bitdepth.sql`, `internal/convert/service.go`, `internal/httpapi/convert.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - The Format section lists each device seen in the last 90 days, with HEVC / HEVC 10-bit / AV1 status derived from real sessions.
    - Turning on AV1 when a recently active device transcoded AV1 shows a warning naming the device before saving.
    - No 'plays everywhere' claim remains.
  - **Tests:** Go insights: the aggregate over fixture sessions (a Roku that transcodes hevc10, a Shield that direct-plays) yields the right ticks.; Go plex: session parsing fills bitDepth and profile from a JSON fixture.; UI check: panel rendering and the AV1 warning dialog.
  - **Depends on:** [CONV-13](#conv-13), PLEX, INT
  - **Risk:** Historic sessions lack bit depth, so the hevc10 column shows '?' until new sessions accrue. Plex 'transcode' can be bandwidth-driven, so word warnings as 'transcoded', not 'can't play'. Needs Insights monitoring on (insights-3: off by default), so the panel says so when it has no data. Device names are household data, so the endpoint is manager-only.
  - **Resolves:** convert-12
<a id="conv-26"></a>
- [ ] **CONV-26 · Make 10-bit output for 8-bit SDR sources a stated choice** — `P2` · `S` · Phase 11
  - **Problem:** cpuVideoArgs always outputs yuv420p10le (preset.go:695, 718), including for 8-bit SDR H.264 sources. The plan's own self-critique dropped that default (CONVERT-QUALITY-PLAN.md:283-286) because Main10 can turn direct-play files into transcodes. The hardware paths already follow the source bit depth (preset.go:392-394, 413-414).
  - **Approach:** New setting convert_ten_bit: 'always' (current) or 'match_source'. The owner chooses the default. Propose 'match_source' for new installs and keep 'always' for the existing one unless changed.
    cpuVideoArgs/compileOutputArgs take pixFmt: yuv420p10le when mi.TenBit || isHDR(EncodeHDR) || setting == always, else yuv420p (x265 main / SVT-AV1 8-bit). Add it to prefs.cacheKey, to estimate.go's efficiency factor if that assumes 10-bit, and to the trial/preflight, which share the args path. Record pix_fmt in the ledger, and include it in [CONV-27](#conv-27)'s resume fingerprint.
    The Format toggle explains the trade-off (banding vs compatibility) and links to the devices panel ([CONV-25](#conv-25)). planWarnings adds '8-bit → 10-bit' only when it applies ([CONV-12](#conv-12)).
  - **Files:** `internal/convert/preset.go`, `internal/convert/decide.go`, `internal/convert/settings.go`, `internal/convert/estimate.go`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With 'match source', an 8-bit SDR H.264 source converts to 8-bit HEVC (the probe shows yuv420p); HDR sources stay 10-bit.
    - The Format section states which applies.
  - **Tests:** Go preset test: cpuVideoArgs for 8-bit SDR + match_source → '-pix_fmt yuv420p'; HDR10 → yuv420p10le regardless.; Go: the cacheKey differs between the settings.; Go integration (synthetic lavfi 8-bit source): the output probe reports 8-bit.
  - **Depends on:** [CONV-25](#conv-25)
  - **Risk:** 8-bit output bands more on gradients, which is why the current default exists. Keep the choice visible and the default the owner's call.
  - **Resolves:** convert-12

#### Milestone: M7 — Big bets

_Restarts and updates cost at most one encoded piece instead of days of 4K encoding. Dolby Vision files can be converted while keeping DV as profile 8.1._

<a id="conv-27"></a>
- [ ] **CONV-27 · Revive the parked resumable pieced encode behind a setting (off by default)** — `P2` · `L` · Phase 11
  - **Problem:** Run() starts with cleanScratch, and runJob marks in-flight jobs 'stopped by a restart' (runner.go:138-141). Every ./update.sh during a multi-night 4K CPU encode (~3.8 fps, ~24 h per film) throws away all progress, so the biggest remuxes, where the space is, may rarely finish. A resumable implementation is parked as stash@{0} ('resumable pieced encoding (unverified — integration run never reported)'), plus chunk.go and chunk_test.go in stash@{0}^3, based on 3e99fc9. Only two commits have touched internal/convert since. The owner parked it deliberately, so reviving it is a priority call.
  - **Approach:** 1) Recover onto current main. Apply the stash diff: analyze.go VideoStartSec, hdr.go, process.go, the runner.go resumable-first pick, service.go dropResume in finish, and the integration_test.go scenario. Restore chunk.go and chunk_test.go from stash@{0}^3. Resolve against crop, measured and preflight, and against [CONV-01](#conv-01), [CONV-17](#conv-17) and [CONV-19](#conv-19).
    2) Keep the stash's design; it is better than a DB table, so the draft's convert_resume table is dropped:
    - frame-exact spans (chunkPlan, chunkSeconds 300) for CFR progressive films only;
    - pieces in a scratch folder resume-<fingerprint>/, where resumeDir hashes src path+size, encoder and plan (codec, CRF, crop);
    - cleanScratch already leaves resume-* alone, and pruneResume drops folders untouched for 30 days or without an item;
    - pickJob takes a resumable key first, and finish() drops the folder whatever the outcome;
    - pieces are joined with explicit per-piece durations, and the original start offset is restored.
    3) Add a manifest.json to the resume folder holding the settled plan (codec, CRF after preflight, crop, encoder, pix_fmt, HDR10+ json) and the source size+mtime. A resumed job with an unchanged source skips the HEVC/AV1 test and preflight and reuses the manifest, so a re-run preflight can't pick a different CRF and orphan the pieces. Confirm the fingerprint includes everything that changes the bitstream, including pix_fmt from [CONV-26](#conv-26).
    4) The size cap applies to the running sum of finished pieces (errTooBig as today). A quality-gate retry at higher CRF discards the pieces.
    5) Scratch accounting: [CONV-01](#conv-01)'s spaceCheck and [CONV-03](#conv-03)'s scratch_need subtract the bytes already in the item's resume folder.
    6) Setting convert_resumable, default OFF in this task. [CONV-19](#conv-19)'s full-decode integrity check runs on the joined output and is the safety net for seams.
    7) Verify with the real-encode recipe on synthetic lavfi sources only (never library files): the stash's 'restart in the middle of a pieced encode' scenario with checkTiming and countFrames, plus an HDR10 run.
  - **Files:** `internal/convert/chunk.go`, `internal/convert/chunk_test.go`, `internal/convert/process.go`, `internal/convert/hdr.go`, `internal/convert/analyze.go`, `internal/convert/runner.go`, `internal/convert/service.go`, `internal/convert/settings.go`, `internal/convert/integration_test.go`
  - **Acceptance:**
    - With the setting on, restarting the container mid-encode resumes from the next unfinished piece; the Activity log says 'resuming from piece 37 of 60'.
    - The resumed output has the source's frame count, even timing and the original A/V offset, and passes the SSIM gate and the integrity check.
    - Changing the file or the conversion settings discards stale pieces.
    - With the setting off, behaviour is unchanged.
  - **Tests:** Go integration (synthetic lavfi sources only): the stash's restart-mid-pieced-encode scenario with checkTiming and countFrames, run in CI.; Go chunk_test: chunkPlan spans are frame-exact and cover the duration; the fingerprint changes when CRF, crop or pix_fmt change.; Go: a manifest present with an unchanged source skips preflight and the codec test.; Go: cleanScratch keeps resume dirs, and pruneResume removes stale or orphaned ones.
  - **Depends on:** [CONV-01](#conv-01), [CONV-17](#conv-17), [CONV-19](#conv-19)
  - **Risk:** A/V sync and seam artefacts (open-GOP at joins); the stash's integration run never reported a result. The owner parked this deliberately, so get explicit sign-off before starting. Keep it behind the setting until CONV-28. Never use library files for verification.
  - **Resolves:** convert-7
<a id="conv-28"></a>
- [ ] **CONV-28 · Turn resumable encoding on by default once verified** — `P2` · `S` · Phase 11
  - **Problem:** Even after CONV-27, the safety benefit only arrives when resumable encoding is the default. The Settings hint 'picks up again next time' (Convert.tsx:934) is still only true without a restart.
  - **Approach:** Do this only after [CONV-27](#conv-27)'s integration scenario has been green in CI for a week, and after a non-library sample 4K film (downloaded test material, not the owner's library) checks out on the owner's box with the owner's sign-off.
    - Default convert_resumable to on.
    - Settings hint: 'a restart or update costs at most one piece (~5 min of film, ~30 min of encoding)'.
    - Overview and Up next show 'resumes at piece 37 of 60' for interrupted items.
    - [CONV-22](#conv-22)'s forecast subtracts finished pieces, and its in-flight warning becomes 'a restart now costs at most ~30 min'.
    - Replace the 'picks up again next time' hint with accurate text.
  - **Files:** `internal/convert/settings.go`, `internal/convert/decide.go`, `internal/convert/throughput.go`, `internal/convert/runner.go`, `web/src/pages/Convert.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - New and existing installs encode long CFR films in pieces by default.
    - The UI shows piece progress for interrupted items, and the forecast accounts for finished pieces.
    - No Convert string promises more than at most one lost piece.
  - **Tests:** Go: the default reads as on when the key is unset and off when 'false' is stored.; Go: the forecast subtracts finished pieces for a fixture resume folder.; UI check: hint text and piece progress.
  - **Depends on:** [CONV-27](#conv-27), [CONV-22](#conv-22)
  - **Risk:** Flipping the default affects every long encode, so keep the setting as a kill switch and mention it in the release notes.
  - **Resolves:** convert-7
<a id="conv-29"></a>
- [ ] **CONV-29 · Keep Dolby Vision through conversion as profile 8.1 using dovi_tool** — `P3` · `L` · Phase 17
  - **Problem:** dovi_tool is in the image (Dockerfile:35-42, 162-163) but is used only for test fixtures (integration_test.go). The plan doc's RPU re-injection was never built, so converting a DV file always loses the DV layer. That is fine for the owner's 'drop' choice, but leaves no way to shrink DV titles for a DV-capable TV.
  - **Approach:** Add the policy value 'keep' to [CONV-15](#conv-15)'s setting. A new dv.go mirrors the HDR10+ pipeline in hdr.go:
    1) Extract the RPU through runTool's pipe variant ([CONV-18](#conv-18)): `ffmpeg -c copy -bsf hevc_mp4toannexb -f hevc - | dovi_tool -m 2 extract-rpu - -o rpu.bin`. Mode 2 converts profile 7 FEL/MEL to 8.1; profile 5 stays refused (DVUnconvertible).
    2) Encode with encodeHEVCStream (raw ES, bframes=0, HDR10 base parameters) under the size cap.
    3) Check that the RPU count equals the encoded frame count before injecting; otherwise fail closed with SkipHDRUnsupported. Inject with `dovi_tool inject-rpu -i enc.hevc --rpu-in rpu.bin -o inj.hevc`. When the file also carries HDR10+, inject HDR10+ first, then the RPU.
    4) remuxVideoStream as today.
    chooseCodec forces HEVC for keep-DV, and canPreserveHDR accepts DV when the policy is keep and the tool is present. Verify the output with `dovi_tool info --frame 0` and ffprobe side data showing DV profile 8 with base-layer compatibility HDR10. scratchNeeded doubles, as for HDR10+. planWarnings no longer says 'DV dropped' under keep, and Facts reports DolbyVision=true for the output. The Format radio gains 'Convert and keep Dolby Vision (profile 8.1)', shown only when dovi_tool is detected.
  - **Files:** `internal/convert/dv.go`, `internal/convert/hdr.go`, `internal/convert/process.go`, `internal/convert/preset.go`, `internal/convert/decide.go`, `internal/convert/facts.go`, `internal/convert/service.go`, `internal/convert/integration_test.go`, `web/src/pages/Convert.tsx`
  - **Acceptance:**
    - With 'keep', a synthetic DV P8 source converts to an HEVC MKV whose probe reports Dolby Vision profile 8 with an HDR10 base, and passes the SSIM gate and the integrity check.
    - A synthetic profile 7 FEL source comes out as 8.1.
    - If the RPU frame count doesn't match the encode, the original is kept (fail closed).
    - On the owner's DV TV, a non-library DV sample converted with 'keep' plays as Dolby Vision through Plex.
  - **Tests:** Go integration (synthetic fixtures built with dovi_tool as integration_test.go already does; never library files): P8 → keep → the output probe shows DV profile 8, and the gate passes.; Go: an RPU/frame-count mismatch returns an error and the job is skipped.; Go: scratchNeeded doubles for keep-DV.
  - **Depends on:** [CONV-15](#conv-15), [CONV-18](#conv-18), [CONV-19](#conv-19)
  - **Risk:** DV RPU and encoder frames must stay aligned, which requires no B-frames, as with HDR10+, and costs ~15% size. Some players mishandle 8.1 converted from P7 sources, so this is not done until it has been checked on the owner's DV TV with a non-library sample.
  - **Resolves:** convert-4

#### Risks

- The Unraid user-share rename behaviour decides whether the hold is instant. CONV-08's startup self-check proves it per library root with a probe file, and EXDEV falls back to a slow copy. The hold must never land under /data.
- Held originals must stay invisible. Plex should skip them via the dot folder and .plexignore, and Arrmada's own scanners must skip dot dirs. Covered by tests in CONV-08.
- Migration numbers will collide with other epics. Every migration here is named NNNN_* and takes the next free number (≥0090) at implementation time.
- The stricter gate (CONV-17, CONV-19) means lower CRF, bigger outputs and more 'kept original' skips. The thresholds need the owner's sign-off and are measured on synthetic or sample clips only. VMAF starts advisory.
- The interim bin guard (CONV-02) stops large 4K remuxes converting until the hold ships. This is intended, and Problems must say why.
- Concurrency risks: the request lane, the broadcast wake, hold expiry vs finalize/revert, and the claimItem sentinel. All need go test -race in Docker before pushing.
- Plan/result drift. The plan view, auto-preview, ledger and encode must all use planFor and trackDecisions. The CONV-06 refactor has to be behaviour-neutral.
- Copy goes stale as behaviour changes. Each task owns its strings, and CONV-05's acceptance lists the touchpoints.
- Upgrade-path churn with QUAL (CONV-20). Agree the signature first. Files converted before CONV-20 only have a release-name baseline.
- Resumable encoding has seam and A/V-sync risk, and the owner parked it deliberately. It stays behind a setting until CI and a non-library sample verify it.
- Some players mishandle DV profile 8.1 converted from P7 sources. CONV-29 is not done until it has been checked on the owner's DV TV with a non-library sample.
- Insights device data is sparse at first (no bit depth on historic sessions), and Plex 'transcode' is not always codec-driven, so warnings must be worded carefully.

#### Out of scope

- Plex partial refresh after a Convert swap. Owned by the PLEX epic (backend-14); CONV-21 publishes the convert.done event it needs.
- Routing convert.* events to admin notifications or a daily digest. Owned by OBS/notify (insights-6).
- An update.sh warning or deferral while a long encode is in flight. Belongs to deploy tooling; CONV-22 exposes in_flight_secs for it.
- Encoding SDR pieces on the Arc GPU while HDR goes to the CPU. A follow-up after CONV-28.
- The recycle-bin rework itself: never purge the newest item, refuse instead of hard-deleting, per-filesystem bins. Owned by SAFE.
- Library fit badges and the ideal-file check across movie versions. Owned by MOV; CONV-24 keeps fit on default rows.
- Subtitles-module forced/SDH variants and its sidecar parser. Owned by SUB.
- Fixing the AV1 codec-token parse (quality-4). Owned by QUAL and coordinated with CONV-20.
- Restricting the existing GET /api/v1/convert/* endpoints by role. Belongs to SEC; all new Convert endpoints here are manager-only.
- Re-tuning encoder presets or CRF ladders beyond the gate thresholds, and any encode tests on the owner's real library files.

