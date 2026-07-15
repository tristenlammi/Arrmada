# Arrmada — Convert module Build Plan

> **Convert** is Arrmada's Tdarr replacement: automated, GPU-accelerated transcoding, remuxing and
> cleanup for the **Movies & TV** libraries. The mandate is *"same power as Tdarr, a fraction of the
> cognitive load"* — preset-first, a readable pipeline instead of JSON flows, and **every rule previews
> exactly what it will do (including estimated size saved) before touching a file.**
>
> This is the largest single module (roadmap §7.8 / Phase 9). This document is its granular execution
> plan. Companion: [ROADMAP.md](ROADMAP.md) §7.8, and the design mockup (Overview · Rules · Queue · Library).

**Last updated:** 2026-07-14 · **Status:** C0 + C1 + C2 + full 4-tab UI (C5) SHIPPED; C3 started (sub extraction + VFR→CFR) — see §6.

---

## 0. Guiding principles (module-specific)

- **Never damage a file.** Every operation is copy → verify → safe-replace → revertable. Safety is not a
  setting you find later; it's the default and it's visible.
- **Preset-first, plugin-never.** One-click intents cover the common jobs; the readable pipeline builder
  covers the rest; raw ffmpeg args are the escape hatch — never the starting point.
- **Show before you run.** Match count, estimated size delta, estimated time, and a 30-second sample
  encode — before a batch is queued.
- **Library-scoped.** Operates over the shared Movies/Series catalogs (not arbitrary watch folders).
- **Reuse the platform.** ffmpeg (bundled), recycle bin, scheduler, event bus, download client, naming
  engine, notifications, Insights — Convert is thin where the platform already does the work.

---

## 1. Dependencies & prerequisites

| Needs | Status | Note |
|---|---|---|
| **ffmpeg + ffprobe** | ✅ bundled | Already in the image (audiobook merge). |
| **Recycle bin** | ✅ built | `library.RecycleFile` — reuse for replaced originals. |
| **Scheduler + job queue** | ✅ built | Reuse for the sweep + a persistent Convert job queue. |
| **Event bus** | ✅ built | `import.completed` → on-import trigger; emit `convert.done`. |
| **Download client (qBittorrent)** | ✅ built | Need a "is this path actively seeding?" query for seeding-safety. |
| **Naming engine** | ✅ (movies) | Reuse to rename post-transcode; generalize for series. |
| **Movies/Series services** | ✅ built | Source catalog; update stored quality/source-release post-convert. |
| **ffmpeg built with `libvmaf`** | ⚠️ verify | Needed for the quality gate; confirm the bundled build has it or add it. |
| **`dovi_tool` + `hdr10plus_tool`** | ❌ add | External binaries to bundle for Dolby Vision RPU + HDR10+ metadata. |
| **Media Server Integration (Plex/Jellyfin/Emby)** | ❌ not built | For post-replace refresh + streaming-aware pause. Convert degrades gracefully without it. |
| **Insights** | ❌ not built | Analytics can be self-contained first, then feed Insights when it lands. |
| **Hardware in the container** | ⚠️ deploy | GPU passthrough (`--gpus`, `/dev/dri`) + ffmpeg with nvenc/qsv/vaapi. Document per-vendor. |

---

## 2. Feature inventory (everything, mapped to phases)

### Core (from the original design)
- HW accel: NVIDIA NVENC/NVDEC, AMD AMF/VAAPI, Intel QSV/VAAPI, Apple VideoToolbox; per-codec + AV1
  availability detection; per-rule encoder choice (auto / device / CPU fallback); live GPU load; workers.
- Presets: Save space · Convert codec · Remux · Strip tracks · Extract subs→SRT · Standardize · Downscale
  · Normalize audio · Health check.
- Rule builder — **Filters** (library, path/regex, container, video/audio codec, resolution, bitrate,
  size, age, HDR type, framerate, track languages, "not processed by this rule") → **Actions** (video /
  audio / subtitle / container) → **Preview**.
- Video actions: target codec, quality mode (CRF/CQ/target-bitrate/target-size), encoder + speed preset,
  resolution scale, 10-bit, multi-pass.
- Audio actions: keep/convert codecs, keep-lossless + add AAC stereo compat track, channel downmix,
  language keep/strip, default flags.
- Subtitle actions: extract text→SRT, strip image/text, keep languages, forced handling, default track,
  optional burn-in.
- Container/metadata: target container, faststart, title/metadata cleanup, chapters, attachments/fonts.
- Preview: match count · estimated size + % + time · per-file preview · **30s sample encode**.
- Triggers: **on import** · **scheduled sweep** · **manual**.
- Library catalog: per-file specs, filter/sort, bulk-select → apply rule, processed-flagging.
- Analytics: reclaimed space (all-time / per-rule / per-title), codec breakdown + opportunity callout,
  health report, power estimate. Preset import/export. Notifications.
- Escape hatch: raw ffmpeg args + per-encoder tuning.

### Critical (safety / correctness — must ship in v1)
- **C-1 Hardlink & seeding safety** — detect hardlinks; never modify a file that's actively seeding
  (query the download client); transcode-to-new-file so seeding & hardlinks aren't broken.
- **C-2 A/V sync correctness** — auto **VFR→CFR**, preserve audio delay/offset.
- **C-3 Dolby Vision + HDR10+** — preserve/convert **DV RPU (profiles 5/7/8, P7→8.1)** and HDR10+
  dynamic metadata; keep HDR10; optional tonemap→SDR.
- **C-4 Scratch dir + disk-space guard** — encode to a local temp dir; refuse a job without enough free
  space for the output.
- **C-5 Rename + re-tag after transcode** — re-run naming; update the Movies/Series record's
  quality/source-release so upgrade logic & displayed quality stay correct.
- **C-6 Media-server + streaming awareness** — trigger a Plex/Jellyfin/Emby refresh after replace; never
  transcode a file that's being actively streamed.
- **C-7 Failure quarantine + interrupted-job recovery** — blocklist after N fails; resume/clean up after
  a crash or restart.
- Safe-replace core: copy → **verify** (duration/stream/playability, never-larger guard) → recycle
  original → atomic replace → **one-click revert** + full history.
- **VMAF/SSIM quality gate** with auto-retry at a higher bitrate.

### Should-have (bake in early)
- Embedded **closed captions (CEA-608/708)** extraction → SRT (they live in the video stream, not a track).
- **Exclusions / "never touch" allowlist** + per-title **keep-original** flag; skip files below a
  size/bitrate threshold.
- **Per-job ffmpeg log viewer**.
- **Encoder tuning presets** — `tune=grain/animation`, film-grain retention, b-frames/ref/AQ/lookahead,
  SVT-AV1 vs HW-AV1 quality trade-off.
- **Loudness normalization (EBU R128)** + correct 5.1→2.0 downmix coefficients.
- **Per-storage-device concurrency + IO/bandwidth caps** (don't hammer one spinning disk).
- **Permissions/ownership (PUID/PGID)** on output files.

### Nice-to-have (deferred — last)
- **Subtitle OCR** (image PGS/VOBSUB → SRT).
- **A/B quality compare UI** (thumbnail grid + VMAF side-by-side).
- **"Worth it?" smarts** — skip low-value jobs (tiny saving vs long encode).
- **HandBrake preset import**.
- **Distributed workers** (extra machines join the pool).
- Per-title **energy/cost** estimate.
- **Requests "issue → act"** hook (report a problem → auto re-convert/extract).
- Video filters: deinterlace auto-detect, denoise/deband, crop detection, AV1 film-grain synthesis.

---

## 3. Phased build

Each phase is independently shippable/testable and ends with a concrete Definition of Done (DoD).

### Phase C0 — Analysis & hardware foundation
*Know every file and every encoder before touching anything.*
- `ffprobe`-based media analyzer → per-file spec: container, video codec/profile, resolution, **HDR type
  (SDR/HDR10/HDR10+/DV + profile)**, bitrate, framerate (CFR/VFR), audio tracks (codec/channels/lang),
  subtitle tracks (type/lang/forced), embedded CC presence, chapters, size, duration.
- Convert catalog store over Movies/Series (specs + per-rule processed flags), incremental rescan.
- **Hardware detection**: enumerate GPUs (NVIDIA/AMD/Intel/Apple) and probe which ffmpeg encoders/
  decoders are actually usable at runtime (nvenc/qsv/vaapi/amf/videotoolbox, per codec, AV1 by GPU gen).
- Persistent **job queue** (retry/backoff) + **worker pool** + **scratch dir** config + **disk-space guard**.
- **DoD:** scan the library into a spec catalog; report detected GPUs + usable encoders; queue infra runs.
- Covers: HW-accel detection, library catalog, scratch/disk guard (C-4), queue.

### Phase C1 — Core conversion engine (one job, safe end-to-end)
*The safe-replace pipeline is the spine of the whole module.*
- ffmpeg command builder from a structured action spec (video transcode: encoder, quality mode; remux).
- Live progress parse (%, fps, speed×, ETA) from ffmpeg stderr.
- **Safe-replace pipeline:** encode→scratch → **verify** (duration/stream/playability, never-larger) →
  recycle original → atomic replace → **rename via naming engine + update Movies/Series record** (C-5) →
  emit `convert.done`.
- **Hardlink & seeding safety (C-1):** detect hardlinks; query the download client; skip/defer seeding
  files; new-file semantics so seeding & hardlinks survive.
- **Media-server refresh + streaming-aware skip (C-6)** (best-effort until Media Server Integration lands).
- **DoD:** convert one movie H.264→HEVC on GPU (CPU fallback), verified & safely replaced, renamed &
  re-tagged, **without breaking a seeding hardlink or a live stream**, revertable.

### Phase C2 — Rules, presets & preview
*The product surface: define once, preview, run.*
- Rule model: Filters (all types) + ordered Action pipeline + schedule/trigger + enable toggle.
- **Presets** (save space / strip tracks / extract subs / remux / standardize / health check) as starting
  points that expand into editable rules.
- **Preview engine:** match count; **estimated output size** (bitrate/quality-target heuristics) &
  % saved; estimated time on the detected hardware; per-file preview list; **30s sample encode**.
- Triggers: manual (bulk-select/apply), **scheduled sweep**, **on-import hook** (`import.completed`).
- **DoD:** build a rule from a preset, see accurate match count + savings estimate + sample, queue the batch.

### Phase C3 — Track & HDR intelligence
*The hard media-correctness work.*
- Audio: keep/convert, keep-lossless+add-stereo, downmix coefficients, language keep/strip, default flags,
  **EBU R128 loudness**.
- Subtitles: extract text→SRT, strip, keep langs, forced handling, UTF-8, **embedded CC (CEA-608/708)**.
- **HDR/DV (C-3):** preserve HDR10; **Dolby Vision RPU** via `dovi_tool` (P5/7/8, P7→8.1); **HDR10+** via
  `hdr10plus_tool`; tonemap→SDR option.
- **VFR→CFR + audio-delay preservation (C-2)**.
- **DoD:** a rule strips foreign tracks, extracts subs + CC, and transcodes a DV/HDR10+ title with metadata
  and A/V sync intact.

### Phase C4 — Quality, scheduling & scale ✅ **CORE SHIPPED 2026-07-15**
*Run it across a whole library, unattended, safely.* All CPU-verified end-to-end.
- **Quality gate (SSIM) + auto-retry** — `convert_quality_gate` / `convert_min_ssim`. After a transcode,
  `computeSSIM` scores the output vs the source (reference scaled to the output res, so a deliberate
  downscale isn't penalised); below threshold it re-encodes at a lower CRF (`higherQuality`, up to 2 retries),
  then keeps the original. **VMAF isn't available — the bundled ffmpeg has no libvmaf — so SSIM is the metric;**
  swap in VMAF if a libvmaf build is ever bundled. Verified: reject path (SSIM 0.9986 < 0.999 → 3 tries → kept
  original) and pass path (0.996 ≥ 0.95 → converted), SSIM shown on the job.
- **Scheduling: off-hours window** — `convert_sweep_start`/`_end` ("HH:MM", wraps past midnight); `Sweep()`
  skips outside it, manual runs always allowed. Verified both ways.
- **Concurrency: multi-worker** — `convert_workers` (1–8, applies on restart); `Run()` spawns N goroutines,
  shared state mutex-guarded (`reclaimMu`). Verified: 3 files encoded concurrently with workers=3.
- **Failure quarantine/blocklist** — `convert_failures` table (migration 0038); a hard failure increments,
  a success clears; the sweep skips a movie past `convert_max_failures` (default 3); manual runs override.
  Verified: 3 induced failures → sweep skips the blocklisted movie.
- **UI:** a **Settings tab** on the Convert page (quality gate, automation/schedule/blocklist, and the global
  default transcode options — which had no UI before). Overview's "VMAF gate — SOON" safeguard updated to the
  shipped SSIM gate.
- **DoD met:** a nightly sweep runs with the quality gate, multiple workers, the schedule window, and the
  blocklist, with no runaway failures.
- **Deferred (documented, not built):** pause-when-GPU-busy + streaming-aware pause (need a GPU-busy signal /
  media-server integration — neither present here), per-GPU/per-storage-device caps + IO/bandwidth limits,
  PUID/PGID, and interrupted-job recovery (the queue is in-memory; a restart drops queued jobs — needs a
  persisted queue, a larger change).

### Phase C5 — UI (the four tabs)
*Wire the mockup to the real engine.*
- **Overview** (reclaimed + sparkline, codec breakdown + opportunity, hardware, **Safeguards & storage**,
  encoding-now, health/schedule).
- **Rules** (preset chips + the readable builder: When → Do → Preview + sample + safety).
- **Queue** (schedule bar, live jobs with fps/speed/ETA, drag-reorder up-next, completed + revert).
- **Library** (spec table with codec/HDR/bitrate/audio/subs pills, filter chips, bulk convert).
- **Per-job ffmpeg log viewer**; exclusions / keep-original controls.
- **DoD:** the mockup, live and operating the engine end-to-end.

### Phase C6 — Analytics, history & sharing
- Reclaimed-space analytics (all-time/per-rule/per-title), codec-mix trend, health report → self-contained,
  then feed **Insights** when it exists.
- Full **history + one-click revert**; rule **preset import/export**; **notifications** (done/failed/
  corruption/space milestones) via the shared agents.
- **DoD:** history/revert works; analytics render; presets are shareable; notifications fire.

### Phase C7 — Advanced & nice-to-haves (last)
- Raw ffmpeg args escape hatch + **per-encoder tuning UI** (NVENC presets, AQ, b-frames, multipass,
  tune=grain/animation, film-grain synthesis); deinterlace auto-detect, denoise/deband, crop detect.
- **Subtitle OCR** (PGS/VOBSUB → SRT).
- **A/B compare UI**; **"worth it?" smarts**; **HandBrake preset import**; per-title **energy/cost**.
- **Distributed workers**; **Requests "issue → act"** integration.

---

## 3b. Rules engine: v1 (shipped) → v2 (the real thing)

> **Full v2 design (with a FileFlows/Tdarr capability audit + the flow-engine architecture):**
> [CONVERT-RULES-V2.md](CONVERT-RULES-V2.md). Summary below.

**v1 (shipped, C2) — intentionally minimal.** A rule = `name + enabled + filters(codec / min-resolution-class /
min-size) + auto`, applying the single hard-coded **Save space → HEVC** action. The C3 actions built so far
(extract-subs, keep-audio-langs, HDR-skip) are **global settings, not per-rule** — a stopgap so each action could
ship + be tested in isolation. This is the light system a user rightly notices.

**v2 (planned) — the mockup's builder made real.** The rule becomes a structured *filters + action pipeline*:
- **Filters** (combined ALL / ANY, add/remove in the UI): library (Movies/Series), path/regex, container, video
  codec, resolution, bitrate, size, age, **HDR type**, framerate, has-language-track (audio/sub), and
  "not-processed-by-this-rule". (v1 has only codec / min-res / min-size.)
- **Action pipeline** (ordered, configured *per rule* — not global): **video** (target codec HEVC/AV1/H.264,
  quality mode CRF/CQ/target-bitrate/size, encoder, resolution scale, HDR handling) · **audio** (keep/convert
  codecs, keep languages, downmix, add-stereo-compat, EBU R128) · **subtitle** (extract→SRT, strip, keep
  languages, OCR later) · **container** (target, faststart, metadata) · **health-check** / **remux-only**.
- **Trigger per rule** (manual / scheduled sweep / on-import) + **priority/ordering**.
- **Presets** = rule templates that pre-fill the pipeline (the preset chips in the mockup).

**Delivery steps:**
1. **Schema v2** (migration): store `filters` + `actions` as structured JSON on the rule (keep name / enabled /
   trigger / priority as columns). The v1 rule maps forward to `{filter: codec+res+size} + {action: transcode HEVC}`.
2. **Migrate the global `convert_*` settings into per-rule action params** (retire the stopgap globals; keep a
   sensible default rule so behavior is unchanged out of the box).
3. **Generalize the ffmpeg compiler** — an action pipeline → ffmpeg args (replaces today's hard-coded
   `saveSpaceOutputArgs`). This is the load-bearing refactor; the C0–C3 primitives (encoder pick, sub-extract,
   audio-map, VFR/CFR) become composable building blocks.
4. **Full editable builder UI** — the `When → Do → Preview` panel becomes interactive (add/remove filters,
   add/configure/reorder action steps), replacing today's read-only render of a fixed rule.

**Sequencing:** v2 depends on the individual C3 actions existing (so there's something to compose), so it lands
**after C3's actions** (audio/sub done; HDR/DV + downmix/loudness remaining) — then step 3 (the compiler) is the
pivot from "one hard-coded action" to "any pipeline." It's the single biggest remaining structural piece of Convert.

---

## 4. Key risks & decisions

- **ffmpeg feature parity in the container.** The bundled build must have nvenc/qsv/vaapi + `libvmaf`.
  Decide: extend the current alpine ffmpeg, switch to a jellyfin-ffmpeg-style build, or a GPU base image.
- **GPU passthrough is a deploy concern**, not just code — needs per-vendor docs (`--gpus all` / `/dev/dri`),
  and graceful CPU-only fallback when no GPU is present.
  - **AMD (e.g. RX 9070 XT) → VAAPI.** Deploy on a **native Linux host** (Docker Desktop on Windows/WSL2 has
    **no `/dev/dri`**, so AMD hw-encode can't run there — verified 2026-07-14). Requirements: compose
    `devices: ["/dev/dri:/dev/dri"]` + `group_add: [render, video]`; the image needs the Mesa VAAPI driver
    (`mesa-va-gallium` + `libva`/`libva-utils` on alpine, or use a jellyfin-ffmpeg base). The VAAPI encode
    path + AMD-vs-Intel vendor detection are **coded and ready but UNVERIFIED** (no AMD `/dev/dri` in the dev
    env); the runtime CPU fallback protects against any mismatch. Confirm on real hardware with
    `ffmpeg -vaapi_device /dev/dri/renderD128 -i in.mkv -vf format=nv12,hwupload -c:v hevc_vaapi out.mkv`.
- **Dolby Vision is genuinely hard** (esp. P7 dual-layer). Bundling `dovi_tool` and getting RPU
  round-trips right is the single biggest technical risk; scope it carefully in C3 and fail safe (skip DV
  titles rather than strip their metadata) until proven.
- **Seeding-safety needs a real "is this seeding?" signal** from the download client — verify qBittorrent
  exposes it per-path/hash before relying on it; default to *skip if unsure*.
- **In-place vs new-file semantics.** Standardize on transcode-to-new-file + safe replace so hardlinks &
  seeding never break; document the disk-space implication (original may persist in downloads while seeding).

---

## 5. Suggested slice to prove the architecture first

If we want an early, high-value vertical slice before the full build: **C0 + C1 + the "Save space" preset
only** — scan the library, convert one H.264 movie → HEVC on the GPU with the full safe-replace +
hardlink/seeding safety + rename/re-tag, revertable. That single path exercises every load-bearing risk
(hardware, ffmpeg, safe replace, seeding safety, metadata update) and de-risks everything after it.

---

## 6. Shipped: first vertical slice (2026-07-14)

`internal/convert/` + `httpapi/convert.go` + `web/src/pages/Convert.tsx` (nav + `/convert` route).

- **C0:** `analyze.go` (ffprobe → MediaInfo: codec/res/HDR/bitrate/fps/tracks/10-bit), `hardware.go`
  (detect libx265/nvenc/qsv/vaapi/videotoolbox + device presence, pick best HEVC), build-tagged
  `fileLinks`/`freeBytes` (Linux syscall; no-op elsewhere so the Windows host build stays green).
- **C1:** job queue + single worker; **Save space** preset (→HEVC, keep audio/subs, MKV out); pipeline =
  probe → skip-if-hardlinked (seeding guard, `convert_skip_hardlinked` default on) → disk-space guard →
  encode-to-scratch w/ live progress (`-progress` parse: %/fps/speed) → **verify** (has video, duration,
  never-larger) → recycle original → atomic replace → `movies.MarkImported` (re-tag) → hardware→CPU
  fallback on encode failure.
- **API:** `GET /convert/{hardware,library,jobs}`, `POST /convert/movies/{id}` (manager). Config
  `ARRMADA_CONVERT_SCRATCH_DIR` (default `<DataDir>/convert`). Worker started in main.go.
- **UI:** minimal single-view page (encoder banner, opportunity stat, active jobs w/ live progress, recent
  results w/ size delta, library table + per-movie "Save space"). Full 4-tab mockup is still phase C5.
- **VERIFIED live end-to-end:** adopted a 9.47 MB H.264 test movie → converted to **3.81 MB HEVC (−60%)**
  on CPU x265; original recycled (recoverable); scratch cleaned; library record updated to HEVC; **and a
  hardlinked (nlink=2, simulated-seeding) file was correctly SKIPPED.** Dev container has **no GPU** (alpine
  ffmpeg lacks nvenc; QSV/VAAPI need `/dev/dri`), so it ran on CPU — hardware accel is a deploy/image concern.

### C2 — rules engine (shipped 2026-07-14)
- Migration `0035_convert_rules.sql` (`convert_rules` table); `rules.go` (Rule model + repo + `matches()`).
- Service: `ListRules` (with live match count + est savings), `CreateRule`/`SetRuleEnabled`/`DeleteRule`,
  `PreviewRule` (matched movies), `RunRule` (queue all matches), `Sweep` (scheduler `convert-sweep` 12h).
- Filters: source codec(s) / **min resolution CLASS** / min size. **Matching compares resolution class, not
  raw pixel height** (a "1080p" movie is often ~800px tall — a raw-height compare wrongly excluded it; caught
  in test and fixed via `resRank`).
- API: `GET/POST /convert/rules`, `GET /convert/rules/{id}/preview`, `POST /convert/rules/{id}/run`,
  `PUT/DELETE /convert/rules/{id}`. UI: a **Rules** section on the Convert page (list w/ match count + est
  savings + enable toggle + Run + delete; a create form: name / codec / min-res / min-size / auto).
- VERIFIED live: rule "H.264 · ≥1080p · >8 GB" → matched Shawshank (11.9 GB, ~6.6 GB to save); create/list/
  preview/toggle/delete all work. (Did NOT run it — that'd transcode the user's real 12.8 GB file on CPU.)

**AMD/VAAPI readiness (2026-07-14):** implemented a real `hevc_vaapi` encode path (global `-vaapi_device` +
`format=nv12,hwupload` filter) + AMD-vs-Intel vendor detection (reads `/sys/class/drm/renderD*/device/vendor`);
QSV gated to Intel, VAAPI to AMD/Intel. **UNVERIFIED** — this dev env is Docker Desktop on Windows/WSL2 with no
`/dev/dri` (confirmed). CPU fallback protects it. See §4 for the AMD Linux deploy requirements.

### Full four-tab UI (C5 pulled forward, 2026-07-14)
`web/src/pages/Convert.tsx` rewritten as a faithful port of the design mockup — **Overview** (reclaimed
[persisted `convert_reclaimed_bytes`] + reclaimable, live codec breakdown, hardware panel w/ available
encoders, Safeguards card, encoding-now), **Rules** (preset chips + the When→Do→Preview builder wired to real
rule filters/preview + safeguards + Queue-all), **Queue** (live jobs w/ fps/speed), **Library** (spec table +
per-movie Save space). Real data throughout; features not yet built are shown honestly (encoder chip shows the
REAL encoder e.g. "CPU (x265)" not a fake NVENC; unbuilt Do-steps/sample/VMAF/streaming-aware marked "soon" or
disabled). New endpoint `POST /convert/sweep` (Run sweep button); `/convert/hardware` now returns
`reclaimed_bytes`. VERIFIED live: all four tabs render; builder shows Shawshank preview.

### C3 — track & HDR (started 2026-07-14)
- **Subtitle extraction → SRT sidecars (the primary sub path):** `analyze.go` now probes subtitle streams
  (index/codec/lang/text-vs-image) + avg frame rate. On convert, embedded **text** subs are extracted to
  `<base>.<lang>.srt` sidecars (`extractTextSubs`) and stripped from the container (`saveSpaceOutputArgs` maps
  only image subs when `convert_extract_subs`, default on); image subs (PGS/VOBSUB) stay in the container
  pending OCR. VERIFIED live: H.264 movie w/ embedded English subrip → converted to HEVC, `.eng.srt` sidecar
  written with correct content, output container = HEVC video only (text sub stripped).
- **VFR→CFR:** VFR detected when avg vs nominal frame rate differ >2%; adds `-fps_mode cfr`. Coded (test file
  was CFR so the flag path wasn't exercised).
- **Audio language keep/strip:** `analyze.go` probes audio streams (index/codec/lang/channels). Setting
  `convert_keep_audio_langs` (CSV, empty = keep all) → `saveSpaceOutputArgs` maps only matching-language audio
  (falls back to all if none match, so never a silent file); `langIn` tolerates 2-vs-3-letter codes (en↔eng).
  VERIFIED live: movie w/ eng+jpn audio, keep=eng → output kept English audio only, dropped Japanese.
- **HDR / Dolby Vision passthrough — SHIPPED 2026-07-15** (HDR is no longer skipped; all three verified end-to-end
  with real encodes). Re-encode preserves metadata on the **CPU/libx265/HEVC** path only (`canPreserveHDR`); other
  targets / hardware still skip, and a plain remux/copy is always safe. **Two build realities drove the design:**
  (a) a naive libx265 re-encode keeps the BT.2020/PQ colour tags but *drops* mastering-display/max-cll, and
  (b) the bundled Alpine x265 isn't built with HDR10_PLUS (`--dhdr10-info disabled`), so dynamic metadata is
  re-embedded **post-encode** with the tools, not through x265.
  - **HDR10 static:** `probeHDR10` reads the frame's mastering-display + content-light side data into x265 form;
    `hdr10Args` re-passes them (`hdr10=1:repeat-headers=1:colorprim/transfer/colormatrix=bt2020…:master-display:
    max-cll`) in the single encode pass. Verified: values survive a 1080p→720p re-encode.
  - **HDR10+:** `encodeHDR10Plus` = encode HEVC ES → `hdr10plus_tool inject` → remux. Extraction doubles as
    detection (attempted on any HDR10 file). Verified: "Dynamic HDR10+ metadata detected" in the re-encoded output.
  - **Dolby Vision:** `encodeDolbyVision` = extract RPU (mode 0) → detect profile → encode ES →
    `dovi_tool -m {2|3} inject-rpu` (→ **profile 8.1**) → remux. DV RPU is *frame*-level side data, so `probeHDR10`
    detects it and upgrades `mi.HDR`. Verified on synthetic P8.1 (output keeps `dovi_profile:8`); the profile-5
    → mode-3 branch is coded but **not yet validated on real P5 media**.
  - **Infra:** Dockerfile `hdrtools` stage bundles `dovi_tool` 2.3.3 + `hdr10plus_tool` 1.7.2 (musl static);
    the service LookPath-detects them and degrades to skip if absent. The ES→MKV remux goes via a temp MP4
    (this ffmpeg won't mux a raw HEVC ES straight to Matroska).
- **Audio: add stereo compatibility track** (`convert_add_stereo`, opt-in): for each kept surround track
  (>2ch), keeps the original **and** adds an AAC 2.0 downmix titled "Stereo" — the "keep TrueHD, add stereo so
  any TV plays it" pattern. VERIFIED live: 5.1 (ac3 6ch) movie → output = ac3 6ch copy + aac 2ch "Stereo".
- **Audio: EBU R128 loudness** (`convert_loudnorm`, opt-in): re-encodes kept audio to AAC through
  `loudnorm=I=-16:TP=-1.5:LRA=11`. Coded (opt-in; the simpler filter variant, not separately exercised).
- Track options refactored into an `encodeOpts` struct threaded through `encode`/`saveSpaceOutputArgs`.
- Convert settings on the shared settings API: `convert_extract_subs`, `convert_skip_hardlinked`,
  `convert_keep_audio_langs`, `convert_add_stereo`, `convert_loudnorm` — **global for now**; per-rule config
  + a Convert settings UI panel are a follow-up (Rules v2, §3b).

- **Embedded closed captions (CEA-608/708):** `analyze.go` detects them (ffprobe `closed_captions`); on convert
  they're extracted to a `<base>.cc.srt` sidecar via the lavfi `movie[...+subcc]` filter (best-effort — never
  fails the job; single-quote-escaped path). Coded; **unverified** (couldn't generate a 608 source in the dev env).
- **Resolution-label fix:** `resolutionLabel` now classifies by **width** (a 1080p movie is 1920px wide but often
  only ~800px tall → height-only mislabeled it 720p, which also broke the ≥1080p rule filter). Verified: Matrix
  Reloaded 1920×800 → 1080p.

**C3 is complete** — audio (keep/strip languages + stereo compat + loudness), subtitles + CC + VFR, and now
**HDR10 + HDR10+ + Dolby Vision passthrough** (above). Rules v2 (§3b) R1–R5 also shipped.
**C4 core shipped** (quality gate, workers, schedule window, blocklist + a Convert Settings tab). **Remaining:**
the deferred C4 items (pause-when-GPU-busy, streaming-aware pause, per-storage IO caps, PUID/PGID,
interrupted-job recovery), the deferred R5 escape hatches (JS node, distributed workers), and HDR→SDR tonemap.
Open follow-ups: validate DV profile-5 on real media; real-GPU (VAAPI) hardware-encode testing (blocked by a
WSL AMD GPU-PV platform bug — see scripts/wsl-gpu-check.sh; works on a native Linux host).
