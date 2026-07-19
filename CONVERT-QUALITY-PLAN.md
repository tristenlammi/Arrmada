# Convert — quality policy plan

## The promise

> Press Convert. Your library gets smaller. Nothing else about it changes.

No rule builder, no flow graph, no per-file decisions, no Tdarr. The user should not
need to know what CRF, QSV or x265 mean. The feature succeeds when someone converts
their library and **cannot tell** — the picture looks the same, the audio is the same
track it always was, subtitles and fonts still work, and the only difference is free
disk space.

Everything below serves that sentence. Where a proposed feature doesn't, it gets cut.

---

## What "no difference" means, concretely

These are the guarantees. Each one is a thing that can break, so each one needs a
mechanism that enforces it — not a hope.

| Guarantee | Mechanism | State |
|---|---|---|
| Audio is bit-identical | Always `-c:a copy`; never re-encode | Holds, but see "settings to remove" |
| Subtitles all survive, text and image | `-map 0:s? -c:s copy` | Holds |
| Embedded fonts / attachments survive | `-map 0:t? -c:t copy` | Fixed (was dropping every file) |
| Chapters + container metadata survive | `-map_metadata 0 -map_chapters 0`, and `1` on the HDR remux paths | Fixed |
| A/V stays in sync | `-fps_mode cfr` when VFR — on **all** paths | Fixed |
| HDR metadata survives | x265 passthrough: HDR10 static, HDR10+ dynamic, DV RPU, HLG transfer | Partly — see HLG fix |
| Bit depth preserved | 10-bit in → 10-bit out | Holds (12-bit fixed) |
| Picture is visually transparent | CRF target + SSIM gate | **Weakest link — see below** |

**The SSIM gate is load-bearing and currently isn't good enough.** If the promise is
"you won't notice", the gate is the only thing that enforces it. Today it defaults to
0.95, which is not a transparency threshold, and it **fails open** — an unmeasurable or
timed-out comparison is accepted and the original is recycled. For a feature whose whole
claim is "no visible difference", that is backwards. It should default to ~0.97 and fail
closed (keep the original when quality can't be verified).

---

## Encoder routing — ordered rules

Evaluated in order; first match wins. This ordering is the design.

1. **Target is AV1 and the source is HDR10 or HLG → AV1, on CPU, with metadata passthrough.**
   AV1 supports HDR perfectly well — the gap is ours, not the format's. SVT-AV1 accepts
   `--mastering-display` and `--content-light`, so this is the AV1 twin of `hdr10Args`,
   which today emits `-x265-params` and nothing else. HLG needs only the colour tags.
   *Implement this rather than routing around it.*

   For **Dolby Vision and HDR10+ with an AV1 target**, the tooling is genuinely immature
   (DV profile 10 exists but support is thin; SVT-AV1's HDR10+ path needs verifying against
   the bundled build). Those skip — but *visibly*, and the UI warns up front when AV1 is
   selected: "N files use Dolby Vision and will be left as they are."

2. **Source is HDR / HDR10+ / Dolby Vision / HLG (any resolution) → CPU x265.**
   Forced, not a preference. x265 is the only path that preserves the metadata: `hdr10Args`
   re-passes mastering-display and max-CLL, and the DV / HDR10+ pipelines encode an x265
   elementary stream and inject the RPU or dynamic metadata afterwards.
   **This is why 1080p HDR goes to CPU** — HDR is checked before resolution, so resolution
   never enters into it.

3. **Height ≥ threshold (default 2160) → CPU.**
   A preference, and the one real knob. 4K files are few and large, so an efficiency gain
   is tens of GB each — the best return on CPU time. Movable, including off.

4. **Otherwise → hardware**, vendor-appropriate: QSV on Intel, VAAPI on AMD, NVENC on
   NVIDIA. Currently `knownEncoders` lists VAAPI before QSV, so Intel Arc gets the
   fixed-quantiser VAAPI path instead of QSV's proper constant-quality mode. Fix the order
   using the vendor detection that already exists.

5. **Hardware encode fails → fall back to CPU.** Already implemented; keep it.

The three presets are just this rule set with the threshold moved:

| Preset | Threshold |
|---|---|
| Maximum quality | 0 — everything on CPU |
| **Balanced** (default) | 2160 — HDR and 4K on CPU, rest on hardware |
| Fastest | off — hardware for everything except forced-CPU HDR |

---

## The HLG fix

`arib-std-b67` is HLG — broadcast and streaming HDR. Today it is detected but labelled
`"HDR10"` (analyze.go:245), and `hdr10Args` then unconditionally emits:

```
-color_trc smpte2084 ... transfer=smpte2084
```

`smpte2084` is PQ. So **every HLG file is re-tagged with the wrong transfer curve.** On
playback it displays with wrong brightness — washed out or crushed — and the original has
already gone to the recycle bin. This is not an efficiency loss; it visibly breaks the
picture, which is a direct violation of the promise.

Fix:
- Track `"HLG"` as its own HDR label rather than folding it into HDR10.
- Carry the **source's actual** transfer characteristic through instead of hardcoding PQ.
- `master-display` / `max-cll` apply to PQ only; HLG is self-describing and needs neither.
- Routing rule 2 already covers HLG, so it goes to CPU either way.

---

## Skips must be durable and visible

Skipping a file is a legitimate outcome. Skipping it *invisibly and permanently* is not,
and that is what happens today:

- A skip records no strike, so it never reaches the Problems list.
- Job history is capped at 200 entries and lost on restart, so the only trace evaporates.
- The file stays a candidate forever, so "reclaimable" counts space that will never be
  recovered, and every sweep re-probes it — waking the array — to skip it again.
- The user pressed Convert expecting their library converted. A subset wasn't, and there
  is no way to find out which.

This is the actual defect behind "what's the issue with skipping?". Fix it and skipping
becomes honest rather than a silent failure:

- Persist skip reasons per item (reuse `convert_failures` with a `kind` column, or a
  sibling table) so they survive restarts.
- Surface them in the existing Problems tab, grouped by reason — "12 files skipped:
  Dolby Vision with an AV1 target", "340 files skipped: still seeding".
- Exclude permanently-skipped files from the "reclaimable" figure so the headline number
  stops promising space that isn't coming.
- Warn **before** the user commits, not after: selecting AV1 should say how many files it
  can't handle, and Convert All should say what it's going to leave behind.

## Resource budget

Without this, CPU encoding is unusable on a shared server and the whole routing design is
theoretical. It is the enabler, not an optimisation.

- **Core cap** — how many cores encoding may use. Default to half of `NumCPU`. Via x265
  `pools=N` / SVT-AV1 `-lp N`, plus ffmpeg `-threads`.
- **Low priority** — run niced so Plex, game servers and anything interactive win
  contention automatically. Build-tagged (`Setpriority` on Linux, no-op elsewhere),
  following the existing `hardlink_linux.go` / `hardlink_other.go` pattern.
- The schedule window already exists and covers the rest.

---

## Encoder settings

Currently `x265 -preset medium -crf 20` and `SVT-AV1 -preset 8` — both tuned for speed,
which contradicts the module's purpose.

- x265 `-preset slow`, CRF 18, plus `aq-mode=3`, `psy-rd=2.0:psy-rdoq=1.0`, `no-sao=1`,
  `bframes=8`, `rc-lookahead=40`.
- SVT-AV1 preset 8 → 5, `tune=0`.
- VAAPI `-rc_mode CQP` → ICQ; NVENC needs `-b:v 0` alongside `-cq`; VideoToolbox currently
  ignores the quality value entirely.

**Consequence to handle:** higher quality means bigger outputs, so more files will trip
"converted file wasn't smaller". That currently records a blocklist strike, so raising
quality mechanically causes more permanent exclusions. It should be reported as "already
efficient at your quality setting" and not treated as a failure.

---

## Settings: what exists, and what to remove

Against "it just works", every setting is a small failure. The defaults must be right and
the knobs must be an escape hatch.

**Keep / add:** quality preset (3 options), 4K threshold (advanced), core cap, target
codec, schedule window, SSIM threshold.

**Remove:** `convert_keep_audio_langs`, `convert_add_stereo`, `convert_loudnorm`. These
exist only to *break* the audio guarantee — loudnorm re-encodes to AAC, keep-langs drops
tracks. They were dead until I wired them live, and they have no UI. For a feature whose
promise is "the audio is untouched", the right move is to delete them rather than guard
them.

---

## Proving it — the A/B test

The user should not have to take anyone's word, including mine. I have no Arc, no VAAPI
and no QSV, so every claim I've made about relative encoder quality is from general
knowledge, not measurement — and I already got the vendor wrong once.

- **Numbers**: encode the same slice on CPU and hardware, report size, SSIM vs source, and
  elapsed time for each. This is what sets the threshold in rule 3 — from evidence, not
  from my priors.
- **Frames** (later): lossless PNG at identical PTS from source and both outputs, 1:1 zoom
  with pan, defaulting to the lowest-SSIM window so it shows the encoder at its worst.
  Disabled for HDR — tone-mapped stills on an SDR monitor produce confident wrong
  conclusions.
- **Not video playback.** HEVC doesn't play reliably in browsers, and transcoding the
  preview to H.264 would mean comparing artifacts of the preview rather than the encoders.

---

## Phases

| # | Work | Why this order |
|---|---|---|
| 1 | Core cap + nice | Unblocks CPU on a shared box; nothing else is usable without it |
| 2 | Ordered routing, QSV/VAAPI selection, HLG fix, AV1 HDR10 passthrough | The correctness core |
| 2b | Durable + visible skips | Turns silent permanent exclusions into something the user can see and act on |
| 3 | A/B numbers + episode sampling | Sets the phase-2 threshold from measurement |
| 4 | Encoder tuning, SSIM 0.97 + fail-closed, remove audio settings | Delivers the quality promise |
| 5 | Visual frame comparison | Trust; genuinely differentiating, but not capability |

Phase 3 moved ahead of tuning deliberately: measure on real hardware before committing to
a policy built on my assumptions.

---

## Self-critique

**Settings sprawl is the main risk.** Every knob is a step away from "just press Convert".
Mitigated by three presets as the primary UI and everything else behind an advanced
disclosure — but this is the thing to push back on.

**I reverse myself on 10-bit.** I earlier said to encode 8-bit sources as 10-bit. As a
*default* that's wrong for a media server: HEVC Main10 isn't universally supported by older
TV clients, and forcing it can turn direct-play files into Plex transcodes — costing far
more than the banding it fixes. Opt-in at most, and arguably not worth having at all.

**I cannot test any hardware path.** The QSV reorder changes which encoder the user's
server actually uses. The existing hardware→CPU fallback means an unsupported flag degrades
safely, but "degrades safely" is not "verified". One real file must be run before this
touches a library.

**I got AV1 wrong and it changed the design.** I wrote rule 1 around "AV1 can't do HDR",
which is false — AV1 supports HDR10, and the gap is that `hdr10Args` only emits
`-x265-params`. Designing a codec-substitution rule around our own missing feature would
have shipped a surprise (you pick AV1, you silently get HEVC) instead of a fix. The lesson
generalises: check whether a constraint is real before building policy on top of it.

**AV1 HDR10+ / Dolby Vision support is unverified.** SVT-AV1's HDR10+ path and DV profile
10 need testing against the bundled build before I claim either way. If HDR10+ works, the
skip list shrinks further.

**The frame comparison has a security edge.** Serving frames must read only from a
generated-ID scratch path, never anything user-supplied, or it becomes an arbitrary
file-read endpoint. 4K PNGs are ~20 MB each and need TTL cleanup.

**Deliberately excluded:** per-media-type routing (resolution is a good enough proxy),
VMAF (not in the bundled ffmpeg), and any form of rule builder.

---

## Open questions

1. **Core cap default** — half of `NumCPU`, or something more conservative on a box that
   also runs Plex and game servers?
2. **Should the 4K threshold be user-visible at all**, or should the A/B test just set it?
3. **Do the three audio settings get deleted**, or kept and guarded?
4. **Is the visual comparison worth building**, given the numbers already decide the policy?
5. **Does SVT-AV1 in the bundled ffmpeg support HDR10+?** Determines whether anything at
   all needs to skip on an AV1 target, or only Dolby Vision.
