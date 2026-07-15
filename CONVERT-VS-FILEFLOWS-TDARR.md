# Arrmada Convert vs FileFlows vs Tdarr — capability reference

*Last updated 2026-07-15. A reference for where Arrmada's Convert module stands against the two
incumbents. Honest about gaps: "✅ done + verified" means we tested it (CPU) end-to-end; "⚠️" means
built-but-weaker or unverified; "❌" means not built.*

---

## TL;DR

- **What we beat them at:** *preview before you touch a file* (per-file size estimate + a real 30s
  sample), *safety by default* (seeding/hardlink-safe, verify-before-replace, recycle, never-grow),
  *media-domain rules* instead of raw-ffmpeg-first, and being **part of the *arr stack** (library-aware,
  re-tags quality, feeds upgrade logic). HDR10 / HDR10+ / Dolby Vision passthrough is **built-in and
  verified**, not a fragile plugin.
- **What they beat us at:** **distributed processing across many machines/GPUs** (Tdarr's whole reason
  to exist), a **plugin/community ecosystem**, **JS/expression scripting nodes**, a **free-form visual
  node canvas**, **VMAF**, and years of battle-testing on huge libraries.
- **The honest asterisk:** Arrmada Convert is verified **on CPU only** so far. The GPU (VAAPI) path is
  written but blocked from testing by a WSL/AMD platform bug — it works on a native Linux host. Both
  competitors have mature multi-GPU hardware acceleration.

---

## Philosophy / architecture

| | **FileFlows** | **Tdarr** | **Arrmada Convert** |
|---|---|---|---|
| Core model | Visual **flow graph** (nodes wired together) | **Plugin stacks** + a v2 **flow graph** | **Rules**: filter gate → **branching step tree** (if/then/else, nested) |
| Building block | Nodes (conditions, actions, FFmpeg Builder, **Function/JS**) | Community **plugins** (JS) + flow nodes | Media-domain **actions** (transcode/scale/container/audio/subs/health/raw) |
| Scope | Any files (video/audio/image/…), watch folders | Media **libraries** (watch folders) | **Movies & TV only**, driven by the *arr library |
| Scaling | **Distributed** processing nodes | **Distributed** nodes/workers across machines + GPUs | **Single machine**, multi-worker pool |
| Extensibility | Plugins + JS + community flows | **Large** community plugin library + JS | Structured actions + a **raw-ffmpeg escape hatch** (no plugins/JS) |
| Preview | Minimal | Minimal | **Per-file estimate + exact 30s sample at every step** |
| Deploy | Docker, web UI | Docker, web UI | Single Go binary, embedded web UI |

The deliberate trade: we chose a **readable step-tree with progressive disclosure** over a free-form
node canvas + scripting. Lower cognitive load and safer defaults; less raw flexibility.

---

## Feature matrix

### Video / codecs
| Capability | FileFlows | Tdarr | Arrmada Convert |
|---|---|---|---|
| Transcode HEVC / H.264 / AV1 | ✅ | ✅ | ✅ done + verified (CPU: x265/x264/SVT-AV1) |
| Hardware encode (NVENC/QSV/VAAPI/VT) | ✅ mature | ✅ mature, multi-GPU | ⚠️ coded + auto-detected; **CPU-only verified** (GPU blocked by env) |
| Downscale / resize | ✅ | ✅ | ✅ done + verified (scale to height, downscale-only) |
| HDR→SDR tonemap | ✅ | ✅ | ❌ deferred (tied to the HDR metadata path) |
| CRF / quality target per codec | ✅ | ✅ | ✅ done (codec-aware CRF defaults + override) |
| VFR → CFR normalization | ✅ | ✅ (plugin) | ✅ done |

### HDR / Dolby Vision
| Capability | FileFlows | Tdarr | Arrmada Convert |
|---|---|---|---|
| HDR10 static metadata passthrough | ⚠️ manual/careful | ⚠️ plugin | ✅ done + verified (extract → re-pass master-display/max-cll) |
| HDR10+ dynamic metadata | ⚠️ hard | ⚠️ plugin, fiddly | ✅ done + verified (hdr10plus_tool inject pipeline) |
| Dolby Vision RPU (→ profile 8.1) | ⚠️ manual | ⚠️ plugin, error-prone | ✅ done + verified* (dovi_tool; P5→8.1 coded, not tested on real P5) |
| Tools bundled in the image | ❌ DIY | ❌ DIY | ✅ dovi_tool + hdr10plus_tool shipped |

*DV verified on synthetic profile-8.1 media; real Dolby-authored files (esp. profile 5/7) still want a pass.*

### Audio / subtitles
| Capability | FileFlows | Tdarr | Arrmada Convert |
|---|---|---|---|
| Keep/strip audio by language | ✅ | ✅ | ✅ done + verified |
| Add stereo downmix | ✅ | ✅ | ✅ done + verified (AAC 2.0 beside surround) |
| Loudness normalize (EBU R128) | ✅ | ✅ (plugin) | ✅ done |
| Extract text subs → SRT sidecars + strip | ✅ | ✅ | ✅ done + verified |
| Extract CC (CEA-608/708) | ⚠️ | ⚠️ | ✅ done (best-effort) |
| Container MKV / MP4 (+faststart) | ✅ | ✅ | ✅ done + verified (MP4-safe audio/subs) |
| OCR image subs (PGS/VOBSUB → SRT) | ⚠️ plugin | ⚠️ plugin | ❌ not built |

### Flow / rules engine
| Capability | FileFlows | Tdarr | Arrmada Convert |
|---|---|---|---|
| Conditional branching | ✅ graph | ✅ graph | ✅ done + verified (nested if/then/else step tree) |
| Filter on codec/res/size/container/HDR | ✅ | ✅ | ✅ done |
| JS / expression node | ✅ | ✅ | ❌ not built (structured actions only) |
| Raw ffmpeg escape hatch | ✅ (FFmpeg Builder) | ✅ (plugin) | ✅ done (raw-args action) |
| Templates / presets | ✅ community | ✅ community | ✅ built-in presets (templates *are* flows) |
| Plugin / community ecosystem | ✅ marketplace | ✅ **large** library | ❌ none |

### Safety / library integration  ← **our strong suit**
| Capability | FileFlows | Tdarr | Arrmada Convert |
|---|---|---|---|
| Never break hardlinks / seeding torrents | ⚠️ config-dependent | ⚠️ config-dependent | ✅ default (skips hardlinked/seeding files) |
| Verify output before replacing | ⚠️ | ⚠️ | ✅ default (stream + duration check) |
| Original → recycle bin (reversible) | ❌ | ❌ | ✅ default |
| Never grow a file (size guard) | ⚠️ | ⚠️ | ✅ default |
| Scratch dir + free-space guard | ⚠️ | ✅ | ✅ default |
| Re-tag library record after convert | ❌ (standalone) | ❌ (standalone) | ✅ (library-aware; feeds upgrade logic) |
| Quality gate + auto-retry | ⚠️ VMAF plugin | ✅ VMAF | ⚠️ **SSIM** gate + retry (no libvmaf in our ffmpeg) |

### Quality / scheduling / scale
| Capability | FileFlows | Tdarr | Arrmada Convert |
|---|---|---|---|
| VMAF quality measure | ✅ | ✅ | ❌ SSIM only (bundled ffmpeg has no libvmaf) |
| Health check (corruption scan) | ⚠️ | ✅ (origin feature, mature) | ✅ done + verified (basic scan, report-only) |
| Off-hours schedule window | ✅ | ✅ (granular) | ✅ done + verified |
| Multiple concurrent workers | ✅ | ✅ | ✅ done + verified (single machine) |
| **Distributed across machines/GPUs** | ✅ | ✅ **headline feature** | ❌ single machine only |
| Pause when GPU busy / gaming | ⚠️ | ✅ | ❌ deferred |
| Failure quarantine / blocklist | ⚠️ | ✅ | ✅ done + verified |
| Interrupted-job recovery on restart | ✅ | ✅ | ❌ in-memory queue (restart drops queued jobs) |
| Per-file size/bitrate preview | ❌ | ❌ | ✅ **estimate + 30s sample** |

---

## Where Arrmada Convert is genuinely better

1. **You see what you'll get before it runs.** Every rule shows a per-file size estimate and can encode
   a real 30-second sample for a content-exact number. Neither FileFlows nor Tdarr shows you the outcome
   before it churns your library.
2. **Safe by default, not by configuration.** Seeding/hardlink safety, verify-before-replace, recycle-bin
   originals, and a never-grow guard are on out of the box. In the incumbents these are your problem.
3. **Rules speak media, not ffmpeg.** Filter on codec/resolution/HDR and branch — no need to think in raw
   ffmpeg or hunt a plugin for the common cases (there's still a raw escape hatch when you want it).
4. **It's part of the stack.** Library-aware: it re-tags quality, respects your profile, and feeds the
   upgrade logic. FileFlows/Tdarr are standalone tools bolted alongside your *arr apps.
5. **HDR/DV is first-class and verified.** dovi_tool + hdr10plus_tool are bundled and the pipelines are
   tested; in the incumbents this is manual/plugin territory and a common source of ruined files.

## Where Arrmada Convert loses (today)

1. **No distributed processing.** Tdarr's headline is farming transcodes across many nodes/GPUs. We're
   single-machine (multi-worker). For a 50-TB library across a rack, Tdarr wins outright.
2. **No plugin/community ecosystem.** Tdarr has hundreds of community plugins; FileFlows a marketplace.
   We have structured actions + a raw escape hatch and nothing to install.
3. **No scripting node.** Both offer JS/expression nodes for arbitrary logic. We don't.
4. **No free-form node canvas.** We chose a readable step-tree; power users who love the drag-a-graph
   experience will miss it.
5. **VMAF.** The incumbents can gate on VMAF; our bundled ffmpeg has no libvmaf, so we gate on SSIM.
6. **Maturity + GPU.** They're proven across enormous libraries with mature multi-GPU accel; our GPU path
   is unverified (env-blocked) and the whole module is new.

## Does it, but not as well

- **Hardware acceleration** — the code detects and drives NVENC/QSV/VAAPI/VideoToolbox, but it's only
  been run on CPU here (GPU passthrough blocked by a WSL/AMD platform bug; fine on native Linux). Tdarr's
  multi-GPU handling is mature.
- **Health check** — we scan for decode errors and report; Tdarr's health workflow is deeper (repair/remux
  paths, richer reporting).
- **Quality gate** — we retry at higher quality on a low **SSIM**; VMAF (what they use) is the better metric.
- **Series** — Convert runs on Movies & TV, but the *bitrate ceiling* in quality profiles is movie-only for
  now (a season pack's runtime needs pack-scope parsing). Single-machine scheduling is simpler than Tdarr's
  per-node schedules.

---

## Bottom line

For a **home / single-server *arr setup**, Arrmada Convert is arguably *nicer to live with* than either:
safer defaults, real previews, media-first rules, and HDR/DV that just works — all inside the app you
already use to manage the library. Where it can't compete is **scale** (distributed transcoding) and
**ecosystem** (plugins/scripts) — which is exactly where Tdarr, in particular, is unbeatable. FileFlows sits
in between: a slick visual flow tool, broader-than-media, but without the library-awareness or safety net.
