# Convert — Rules v2 (the Flow engine)

> The design for Arrmada Convert's rule system: **as powerful as FileFlows / Tdarr, honestly easier.**
> Companion to [CONVERT-BUILD-PLAN.md](CONVERT-BUILD-PLAN.md) (§3b is the summary; this is the full plan).

**Status:** design (2026-07-14). **R1 + R2 + R3 + R4 + R5 (core) SHIPPED** (§5). The flow (filters[] gate + a
branching step tree) is live with precise per-file preview; codec/scale/container/health/raw-args parity landed
in R5. The v1 fixed-column rules are superseded. R5 escape-hatch extras (JS node, distributed workers, HDR
tonemap) are explicitly deferred — see R5 below.

---

## 1. Thesis

FileFlows and Tdarr are powerful because they're **visual flow graphs**: a file enters, flows through
condition nodes that branch and action nodes that transform, and exits. That model is genuinely the right one —
it can express anything. Their weakness isn't the model, it's the **experience**: the graph is intimidating,
Tdarr's classic plugin-stacks and JSON flows are opaque, you configure raw ffmpeg semantics, there's **almost no
preview** (you run a library-wide job and hope), and neither is safe with hardlinked/seeding files.

**So Arrmada uses the flow-graph engine as the power core — and wins on experience via four things they lack:**
1. **Media-domain nodes, not raw ffmpeg** — "Keep English + Japanese audio, add a stereo track," never `-map`.
2. **Preview at every node + a whole-flow dry-run** — per-file source→target spec, tracks kept/removed, and
   **estimated size delta**, before a byte is written; plus a **30-second sample encode**.
3. **Templates *are* flows** — presets are pre-built flows; a beginner picks one and tweaks it in a simple
   **linear** view and never sees the graph. Progressive disclosure, per the Arrmada mandate.
4. **Safety is a built-in node, not your problem** — the safe encode→verify→replace, hardlink/seeding skip,
   recycle + one-click revert, never-larger guard, and VMAF gate are part of the terminal "Replace" node.

**Progressive disclosure is the core UX bet:** the *same engine* renders two ways. **Simple mode** = a linear
rule (`Trigger → Conditions[all] → ordered Actions → Replace`) shown as the mockup's `When → Do → Preview`
builder, with the full node palette. **Advanced mode** = the full branching graph. Adding an "if/else" to a
simple rule promotes it to a flow. One model, two levels of disclosure.

---

## 2. Capability parity — everything FileFlows / Tdarr do, mapped to Arrmada nodes

Legend: ✅ built (C0–C3) · 🟡 planned this design · 🔵 later phase.

### Triggers / inputs
| Capability | FF | Tdarr | Arrmada node |
|---|---|---|---|
| Library scan (Movies/TV) | ✅ | ✅ | **On library sweep** ✅ (schedule) |
| On import (post-download) | ✅ | ✅ | **On import** ✅ (`import.completed`) |
| Manual / bulk-select | ✅ | ✅ | **Manual** ✅ |
| Watch arbitrary folder | ✅ | ✅ | 🔵 (Arrmada is library-scoped by design) |

### Conditions (branch nodes)
| Check | FF | Tdarr | Arrmada node |
|---|---|---|---|
| Video codec | ✅ | ✅ | **If video codec** 🟡 (have the probe ✅) |
| Resolution (class) | ✅ | ✅ | **If resolution** 🟡 (resRank ✅) |
| Bitrate | ✅ | ✅ | **If bitrate** 🟡 |
| File size | ✅ | ✅ | **If size** 🟡 (v1 ✅) |
| Container | ✅ | ✅ | **If container** 🟡 |
| HDR type (SDR/HDR10/HDR10+/DV) | ✅ | ✅ | **If HDR type** 🟡 (probe ✅) |
| Frame rate / VFR | ✅ | ✅ | **If VFR** 🟡 (probe ✅) |
| Has audio/sub in language | ✅ | ✅ | **If has track (lang)** 🟡 (probe ✅) |
| Has closed captions | 🟡 | 🟡 | **If has CC** 🟡 (probe ✅) |
| Age / date | ✅ | ✅ | **If age** 🟡 |
| Filename / path regex | ✅ | ✅ | **If filename matches** 🟡 |
| Hardlinked / from library | ✅ | 🟡 | **If hardlinked** ✅ (fileLinks) |
| Already processed by this flow | ✅ | ✅ | **If processed** 🟡 (per-flow tag) |
| Custom expression / script | ✅ (JS) | ✅ (JS) | **Expression** 🔵 |

### Video actions
| Action | FF | Tdarr | Arrmada node |
|---|---|---|---|
| Transcode → HEVC/AV1/H.264 | ✅ | ✅ | **Transcode video** ✅ (HEVC; AV1/H264 🟡) |
| Quality mode (CRF/CQ/bitrate/target-size) | ✅ | ✅ | quality param 🟡 (CRF/CQ ✅) |
| Encoder auto/HW/CPU | ✅ | ✅ | ✅ (detect + fallback; VAAPI ready) |
| Resolution scale / downscale | ✅ | ✅ | **Scale** 🟡 |
| HDR → SDR tonemap | ✅ | ✅ | **Tonemap** 🔵 |
| Keep HDR10 / HDR10+ / DV RPU | 🟡 | ✅(DoVi) | 🔵 (needs dovi_tool/hdr10plus_tool) |
| Deinterlace / denoise / deband | ✅ | ✅ | 🔵 |
| VFR → CFR | ✅ | ✅ | ✅ |

### Audio actions
| Action | FF | Tdarr | Arrmada node |
|---|---|---|---|
| Keep/remove by language | ✅ | ✅ | **Keep audio langs** ✅ |
| Convert codec | ✅ | ✅ | **Convert audio** 🟡 |
| Downmix / add stereo compat | ✅ | ✅ | **Add stereo** ✅ |
| Loudness normalize (R128) | ✅ | ✅ | **Normalize** ✅ |
| Set default / reorder | ✅ | ✅ | 🟡 |

### Subtitle actions
| Action | FF | Tdarr | Arrmada node |
|---|---|---|---|
| Extract text → SRT sidecar | ✅ | ✅ | **Extract subs** ✅ |
| Extract embedded CC → SRT | 🟡 | 🟡 | **Extract CC** ✅ (best-effort) |
| Strip (text/image/all) | ✅ | ✅ | ✅ (text; per-type 🟡) |
| Keep by language | ✅ | ✅ | 🟡 |
| OCR image → text | 🔵 | 🔵 | 🔵 |
| Burn-in / set forced/default | ✅ | ✅ | 🔵 |

### Container / file / flow control
| Capability | FF | Tdarr | Arrmada node |
|---|---|---|---|
| Remux container (MKV/MP4) + faststart | ✅ | ✅ | **Container** ✅ (MKV) / 🟡 MP4 |
| Clean metadata / chapters | ✅ | ✅ | ✅ (metadata+chapters copied) |
| Health check (corruption) | ✅ | ✅ | 🟡 |
| Rename (naming scheme) | ✅ | ✅ | ✅ (re-tag+rename on replace) |
| Move / copy / replace-original | ✅ | ✅ | **Replace** ✅ (safe pipeline) |
| Recycle / revert | 🟡 | 🟡 | ✅ (recycle; revert 🟡) |
| Branch / goto / iterate / fail / reprocess | ✅ | ✅ | 🟡 (graph engine) |
| GPU-busy / schedule automations | ✅ | ✅ | 🟡 (C4) |
| Distributed worker nodes | ✅ | ✅ | 🔵 |
| Raw ffmpeg / script escape hatch | ✅ | ✅ | 🔵 |
| **Per-file + whole-flow PREVIEW** | ❌ | ❌ | **✅ our differentiator** |
| **Hardlink/seeding safety by default** | ❌ | ❌ | **✅ our differentiator** |
| **Library DB + media-server integration** | ❌ | ❌ | **✅ our differentiator** |

**Takeaway:** we already have the hard primitives (C0–C3). Rules v2 is mostly about (a) a generalized
*compiler* so those primitives compose, and (b) the flow model + builder UI on top. Nothing on the FF/Tdarr
feature list is out of reach, and preview + safety + library-integration are things neither of them has.

---

## 3. Architecture

The pivotal idea: **separate "decide what to do" (the plan) from "do it" (the compiler).** This is what makes
preview *exact* — the thing you preview is literally the thing that runs.

```
Flow (DAG of nodes)  ──walk per file──▶  Action Plan  ──compile──▶  ffmpeg cmd + sidecar/rename/move steps
   (conditions gate,                     (ordered, typed          (the generalized replacement for today's
    actions accumulate)                   video/audio/sub/            hard-coded saveSpaceOutputArgs)
                                          container/fileops steps)         │
                                                 │                         ▼
                                          PREVIEW reads the plan      Safe pipeline: scratch→verify→
                                          (est. size/time, tracks,    recycle→replace→re-tag→refresh
                                          per-node, no execution)
```

- **Node interface (Go):** `Eval(ctx, fc *FileCtx) Outcome`. `FileCtx` carries the probed `MediaInfo` + the
  accumulating `ActionPlan`. Condition nodes return which output edge to follow; action nodes append a typed
  step to the plan and continue.
- **ActionPlan:** an ordered, typed list — `TranscodeVideo{codec,quality,encoder,scale,hdr}`,
  `AudioKeepLangs{...}`, `AudioAddStereo`, `AudioNormalize`, `SubExtract`, `SubStrip{types}`, `Container{...}`,
  `HealthCheck`, `Rename`, `Replace{safe opts}`. This is the real refactor of today's `encodeOpts`.
- **Compiler:** `ActionPlan → (ffmpeg args, []sidecarStep, renameSpec)`. Reuses every C0–C3 primitive (encoder
  pick, audio mapping, sub extraction, VFR/CFR, HDR-skip) as plan-step emitters. **Replaces `saveSpaceOutputArgs`.**
- **Preview:** given a plan + `MediaInfo`, estimate output size (per-action heuristics), tracks kept/removed,
  and time — *without* running. The 30s sample runs the real compiled command on a 30s slice.
- **Persistence:** `convert_flows` table stores the graph as validated JSON (`nodes[]`, `edges[]`, trigger,
  priority, enabled). v1 rules migrate to a single-path flow; the global `convert_*` settings become the
  default flow's action nodes.
- **Execution:** the existing job queue/worker runs the compiled plan through the existing safe pipeline. Multi-
  worker + scheduling window come in C4.

---

## 4. UX — progressive disclosure

- **Simple (default):** the mockup's `When → Do → Preview`. Conditions = the "When" chips (all AND); Actions =
  the "Do" list (add/remove/reorder steps from the palette); Preview = live match count + size delta + sample.
  This is a linear flow under the hood — 90% of users never leave here.
- **Templates:** the preset chips (Save space, Strip tracks, Extract subs, Remux, Standardize, Health check)
  are pre-built flows that drop into the simple editor ready to tweak.
- **Advanced:** a **visual node graph** (drag nodes, connect edges, branch on conditions) for the cases a
  linear rule can't express ("if 4K → HEVC CQ22, else → HEVC CQ24"; "if has DV → skip, else convert"). You get
  here by adding a branch in the simple view, or opening "Advanced".
- **Preview & safety are always visible** — the estimate panel and the safeguard chips (seeding-safe, verify,
  recycle, VMAF) are on every flow, simple or advanced.

---

## 5. Delivery phasing (R1 → R5)

- **R1 — ActionPlan model + compiler (invisible plumbing, the load-bearing refactor).** ✅ **SHIPPED
  2026-07-14.** `plan.go`: `Plan{VideoCodec,Quality,VFRToCFR,Audio{KeepLangs,AddStereo,Loudnorm},Subs{ExtractText,
  ExtractCC},Container}` + `saveSpacePlan(...)` builds it from the global settings. `preset.go`:
  `saveSpaceOutputArgs` → **`compileOutputArgs(enc, mi, plan)`** — the generalized compiler every Plan runs
  through. Engine builds a Plan then compiles → ffmpeg (sub/CC extraction now keyed off `plan.Subs`). Behavior
  **verified identical** (video+audio+sub file → HEVC, sidecar extracted, sub stripped — same as before). This is
  the pivot from "one hard-coded action" to "any composition"; R2–R5 sit on it.
- **R2 — Flow data model + simple linear builder UI.** ✅ **SHIPPED 2026-07-14.** Migration `0036` adds
  `filters_json` + `actions_json` to `convert_rules`; `rules.go` reworked to `Rule{Filters []Filter, Actions
  []Action}` with `matches()` (walks filters: codec/resolution/size/container/hdr, ALL-AND) and `plan()`
  (walks actions → a `Plan`, replacing the settings-derived plan). Each job carries its own `plan` (rule runs
  use `rule.plan()`; the per-movie button uses the settings default). The `When → Do → Preview` builder is now
  **editable** — filter rows (field/op/value) + `+ Add filter`, and action toggles (Transcode always +
  Extract-subs / Keep-audio-langs / Add-stereo / Loudnorm) — and the read view renders each rule's *real*
  filters + action pipeline. VERIFIED live: a 3-filter rule (codec+res+size) matched Shawshank; the builder
  showed the real "keep ENG,JPN" pipeline. **Not yet: the visual branching graph (R4) and `convert_flows` as a
  full node-graph — R2 is the *linear* flow, which is the 90% case.**
- **R3 — Preview engine on the plan.** ✅ **SHIPPED 2026-07-14.** `estimate.go` — `estimatePlanSize(mi, plan)`
  splits the source into estimated video vs audio bytes (`audioBytes` per codec+channels), applies a per-codec
  transcode ratio (`codecRatio`: h264→hevc .55, mpeg2 .30, +quality adj, AV1 ×0.80), and adjusts audio for
  language-strip / stereo-add / loudnorm — replacing the flat 45% heuristic in Library + rule preview + summarize
  (each rule now estimates with *its own* plan). **30-second sample encode** (`SampleRule`/`sample`): encodes a
  ~30s slice (35% into a real film) with the compiled plan and extrapolates by duration for a **content-exact**
  size — `POST /convert/rules/{id}/sample`, wired to the builder's "Encode 30s sample" button (shows "measured on
  real content, not estimated"). VERIFIED: 30.1 MB→17.1 MB heuristic == 30.1→17.1 MB (−43%) sampled.
- **R4 — Branching flow engine + recursive editor.** ✅ **SHIPPED 2026-07-15.** Migration `0037` adds
  `steps_json`. A rule body is now a tree of `Step`s (`action` | `condition` with `then[]`/`else[]`,
  arbitrarily nested); `Steps` takes precedence over the linear `Actions`. `planFor(mi)` walks the tree,
  evaluating each condition against the file's `MediaInfo` so branches resolve **per file** — the Plan is now
  file-dependent, which makes preview/estimate/sample all branch-aware. UI: read-only `StepView` renders the
  If/then/else tree in the rule card; a **Simple ↔ Branching** toggle in the builder switches to a recursive
  `StepListEditor`/`StepEditorRow` (add action from a menu, add `If…`, nested then/else). Chose a readable
  step-tree over a free-form drag canvas (progressive disclosure). Verified end-to-end with real ffmpeg: one
  rule, resolution condition → a 4K file took `then` (kept ENG audio, dropped JPN, added stereo) while a 1080p
  file took `else` (subs extracted to sidecar + stripped). **Not done: `+If…` capped at depth 3 in the UI;
  no per-branch preview breakdown.**
- **R5 — Parity fill + escape hatches.** ✅ **CORE SHIPPED 2026-07-15.** The Plan/compiler gained everything a
  codec node in FileFlows/Tdarr does, all flowing through `compileOutputArgs` + `estimatePlanSize` (so preview
  stays exact) and verified with real CPU encodes:
  - **Codec targets HEVC / H.264 / AV1** (+ `remux` = copy video). `hardware.go` now detects all three codec
    families (libx265 / libx264 / libsvtav1 on CPU; hevc/h264/av1 × nvenc/qsv/vaapi/videotoolbox when a device
    exists); `encoderFor(codec)` picks hw-first-else-CPU per plan; codec-aware CRF defaults (`crfDefault`) + a
    quality override. Verified: h264 → av1 (SVT-AV1 encoder selected, output `av1`).
  - **Downscale** (`scale` action, height; downscale-only, `-2` even width). Verified: 4K 3840×1600 → 2592×1080.
  - **Container MKV / MP4** (`container` action) → extension follows + `-movflags +faststart`; MP4-safe audio
    (TrueHD/DTS/… → AAC) and subs (image subs dropped, text → mov_text). Verified: `.mp4`, moov before mdat.
  - **Health check** (`health_check` action) — read-only `ffmpeg -v error … -f null -` corruption scan that
    reports issue count without touching the file. Verified: good file → "healthy", corrupted file → "4 decode
    issue(s) found". Also: `remux` relaxes the "must be smaller" guard; `wouldBeNoOp` makes candidacy codec-aware.
  - **Raw-ffmpeg escape hatch** (`raw_ffmpeg` action) — appends verbatim output args (`plan.ExtraArgs`).
  - UI: transcode row gets a codec select + CRF; scale/container/health/raw editors in the flow builder; the
    Simple form gained codec + container + downscale selectors.

  **Explicitly deferred (not built — with reasons):** JS/expression node (needs a sandboxed JS runtime; security +
  bundle cost — revisit only if a real rule can't be expressed structurally); distributed/remote workers (the
  single in-process worker is right for a home server; a worker protocol is a large infra project); HDR→SDR
  tonemap (belongs with the HDR10/Dolby-Vision metadata path, which is skipped for safety pending
  `dovi_tool`/`hdr10plus_tool` in the image). Also open: `+If…` depth-3 UI cap, per-branch preview breakdown,
  an `allow_larger` toggle for quality-up transcodes.

**Recommended start: R1.** It's mostly invisible, but it's the pivot from "one hard-coded action" to "any
composition," and it de-risks R2–R5 (they all sit on the plan+compiler). Then R2 makes the power visible.

---

## 6. Open questions

- **Graph library for the advanced editor** — build a lightweight custom React canvas vs adopt a flow library
  (React Flow). Custom keeps the bundle small + on-brand; a library is faster. Decide at R4.
- **Per-flow vs global "processed" tracking** — a `convert_processed(flow_id, movie_id, signature)` table so a
  flow doesn't re-touch a file unless the flow (or file) changed. Needed once flows run on schedules.
- **Series support** — v2 should target episodes too (the engine is media-agnostic; the trigger + catalog need
  the Series service wired, mirroring Movies).
