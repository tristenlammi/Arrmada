# Convert — format choice and conversion rules

## The promise

> Press Convert. Your library gets smaller. Nothing else about it changes.

No rule builder, no flow graph, no per-file decisions. The user should never need to know
what CRF, QSV or x265 mean. The feature succeeds when someone converts their library and
**cannot tell** — same picture, same audio track, subtitles and fonts still working, and
the only difference is free disk space.

## Constraint

**No packaging changes.** We use what's in the image today:

| Component | Version | Relevant capability |
|---|---|---|
| ffmpeg | 6.1.1 (Alpine 3.20) | libx265, libsvtav1, VAAPI, libvpl (QSV available) |
| SVT-AV1 | as packaged | `yuv420p` + `yuv420p10le` — 10-bit works |
| dovi_tool | 2.3.3 | **HEVC only** — profiles 4/5/7/8, no AV1 |
| hdr10plus_tool | 1.7.2 | **HEVC only** — inject takes an HEVC bitstream |

The two metadata tools being HEVC-only is what shapes every rule below. AV1 *as a format*
supports HDR10, HDR10+ and Dolby Vision profile 10 — Netflix ships AV1-HDR10+ at scale —
but nothing in our image can write that metadata into an AV1 bitstream. That would need an
SVT-AV1 built with `enable-dovi` / `enable-hdr10plus`, i.e. a different ffmpeg. Out of scope.

**One open question:** whether this SVT-AV1 accepts `mastering-display` / `content-light`
via `-svtav1-params`. It decides whether plain HDR10 can go to AV1. Test:

```bash
docker exec Arrmada-app ffmpeg -hide_banner -f lavfi -i testsrc=d=1:s=64x64 \
  -c:v libsvtav1 -svtav1-params mastering-display="G(0.265,0.690)B(0.150,0.060)R(0.680,0.320)WP(0.3127,0.3290)L(1000,0.0001)" \
  -f null - 2>&1 | tail -5
```

---

## Option 1 — HEVC (H.265)

**Converts everything. Preserves everything.** Nothing is skipped for metadata reasons.

| Source | Treatment |
|---|---|
| Already HEVC | Skip — nothing to do |
| H.264 / MPEG-2 / MPEG-4 / VC-1, SDR | → HEVC |
| HDR10 | → HEVC, CPU x265, mastering-display + max-CLL re-passed |
| HLG | → HEVC, CPU x265, **source transfer curve preserved** (see HLG fix) |
| HDR10+ | → HEVC, CPU x265, dynamic metadata re-injected by `hdr10plus_tool` |
| Dolby Vision | → HEVC, CPU x265, RPU re-injected by `dovi_tool` (P5 → 8.1 via mode 3) |
| Audio, any codec | Always stream-copied — Atmos / TrueHD / DTS-HD untouched |
| Subtitles, fonts, chapters | Always copied |

This is the safe default and what most users should pick.

## Option 2 — AV1

**~30% smaller than HEVC, but it can't carry Dolby Vision or HDR10+ metadata with our
toolchain, so those files are left alone.**

| Source | Treatment |
|---|---|
| Already AV1 | Skip |
| H.264 / MPEG-2 / MPEG-4 / VC-1, SDR | → AV1 |
| HLG | → AV1 — only colour tags needed, nothing to inject |
| HDR10 | → AV1 **if** `mastering-display` is supported; otherwise skip |
| HDR10+ | **Skip** — `hdr10plus_tool` cannot inject into AV1 |
| Dolby Vision | **Skip** — `dovi_tool` cannot inject into AV1 |
| Existing HEVC | **Skip by default** — see below |
| Audio, subtitles, fonts, chapters | Identical to HEVC: all preserved |

### Why AV1 does not re-encode existing HEVC by default

HEVC → AV1 is a **second lossy generation**. The source is already a compressed encode;
re-encoding compounds its artifacts for maybe 20–30% more space. That's a poor trade for a
module promising maximum quality retention, and on a library like this one it means
re-encoding 14,000+ files that are already efficient.

H.264 → AV1 is a different story — a big efficiency jump from a wasteful source, worth one
generation of loss.

So: **`convert_av1_recode_hevc`, default off.** When on, existing HEVC becomes a candidate
and the UI says plainly what that costs.

### Note on Dolby Vision and HDR10+ in practice

Both are essentially always already HEVC — DV profiles 5/7/8 are HEVC-only, and HDR10+ ships
as HEVC. So the files AV1 skips are files that are *already efficient*. You aren't leaving a
bloated H.264 file unconverted; you're leaving a good HEVC file alone. That makes AV1's
limitation much cheaper than it first sounds.

---

## Encoder routing — ordered rules

Applies to both targets. First match wins.

1. **Source is HDR of any kind (HDR10 / HDR10+ / DV / HLG) → CPU.**
   Forced, not a preference. All metadata handling is software-only: `hdr10Args` is
   `-x265-params`, and the DV / HDR10+ pipelines encode an x265 elementary stream before
   re-injecting. For AV1, `mastering-display` is an SVT-AV1 parameter — also CPU.
   **This is why 1080p HDR goes to CPU** — HDR is checked before resolution, so resolution
   never enters into it.

2. **Height ≥ threshold (default 2160) → CPU.**
   A preference, and the one real knob. 4K files are few and large, so an efficiency gain is
   tens of GB each — the best return on CPU time.

3. **Otherwise → hardware.** Vendor-appropriate: **QSV on Intel** (`--enable-libvpl` is in
   the build, so it's available), VAAPI on AMD, NVENC on NVIDIA. Arc also has AV1 hardware
   encode (`av1_qsv`), so SDR AV1 conversions can use the GPU too.
   Currently `knownEncoders` lists VAAPI *before* QSV, so Intel gets the fixed-quantiser
   VAAPI path instead of QSV's proper constant-quality mode. Fix the order.

4. **Hardware encode fails → fall back to CPU.** Already implemented.

The three quality presets are this rule set with the threshold moved:

| Preset | Threshold |
|---|---|
| Maximum quality | 0 — everything on CPU |
| **Balanced** (default) | 2160 — HDR and 4K on CPU, rest on hardware |
| Fastest | off — hardware for everything except forced-CPU HDR |

---

## The HLG fix

`arib-std-b67` is HLG. Today it's detected but labelled `"HDR10"` (analyze.go:245), and
`hdr10Args` then unconditionally emits `transfer=smpte2084` — which is **PQ, not HLG**. Every
HLG file is re-tagged with the wrong transfer curve, so it plays back with wrong brightness,
and the original is already in the recycle bin. Not an efficiency loss — a visibly broken
picture.

Fix: track `"HLG"` as its own label, carry the **source's actual** transfer characteristic
through instead of hardcoding PQ, and apply `master-display` / `max-cll` only to PQ (HLG is
self-describing and needs neither).

---

## User flow

### First run

Convert opens on a setup state rather than the normal tabs — there is nothing sensible to
show before a format is chosen.

**Step 1 — Choose your format.** Two cards, each carrying *this library's real numbers*,
pulled from the index (which already stores every file's codec and HDR type):

> **HEVC (H.265)** — recommended
> Converts 6,449 files · 4.2 TB → ~2.1 TB
> Keeps everything: Dolby Vision, HDR10+, HDR10, Atmos, subtitles, fonts
> Plays on virtually every device
>
> **AV1**
> Converts 6,390 files · 4.1 TB → ~1.7 TB
> Leaves 47 Dolby Vision and 12 HDR10+ files as they are — AV1 can't carry their metadata
> Audio, subtitles and fonts unaffected
> Fewer pre-2020 devices can play it

The counts make the trade self-evident without anyone learning what a codec is. A user with
no DV content sees "leaves 0 files" and picks AV1 freely; one with 400 DV files sees the cost.

**Step 2 — Quality vs speed.** Three presets, defaulting to Balanced, with an honest
estimate: *"About 3 weeks at your current speed"*, derived from measured throughput.

**Step 3 — Done.** Convert unlocks. Everything else has a working default.

### Ongoing

- **Overview** — space reclaimed, what's left, codec breakdown across the whole library.
- **Library** — what's convertible under the chosen format, filtered to actionable rows by
  default, searchable, with per-show/season/episode and per-movie actions.
- **Convert all** — confirms with counts, estimated space saved, estimated wall-clock, and
  **what it will skip and why**.
- **Queue** — running vs waiting, cancel individually or clear the queue.
- **Problems** — everything skipped or blocklisted, grouped by reason, with Retry.

### Changing format later

Switching HEVC → AV1 re-evaluates candidacy; existing HEVC files only become candidates if
the opt-in above is on. Switching AV1 → HEVC leaves AV1 files alone (they're already
efficient). No rescan needed either way — candidacy is derived at query time from the stored
codec, never stored.

---

## Skips must be durable and visible

Skipping is legitimate. Skipping *invisibly and permanently* is not, and that's today's
behaviour: a skip records nothing, the 200-entry job history is lost on restart, the file
stays a candidate forever inflating "reclaimable", and every sweep re-probes it — waking the
array — to skip it again.

- Persist skip reasons per item so they survive restarts.
- Group them in Problems: *"47 files skipped: Dolby Vision can't be converted to AV1"*.
- Exclude permanently-skipped files from "reclaimable" so the headline stops promising space
  that isn't coming.
- Warn **before** committing, not after — the format cards above do exactly this.

---

## Resource budget

Without this, CPU encoding is unusable on a server also running Plex and game servers, and
the whole routing design is theoretical.

- **Core cap** — how many cores encoding may use. Default half of `NumCPU`. Via x265
  `pools=N` / SVT-AV1 `-lp N`, plus ffmpeg `-threads`.
- **Low priority** — run niced so interactive workloads win contention. Build-tagged,
  following the existing `hardlink_linux.go` / `hardlink_other.go` pattern.
- The schedule window already exists.

---

## Encoder settings

Currently `x265 -preset medium -crf 20` and `SVT-AV1 -preset 8` — both speed-tuned, which
contradicts the module's purpose.

- x265 `-preset slow`, CRF 18, plus `aq-mode=3`, `psy-rd=2.0:psy-rdoq=1.0`, `no-sao=1`,
  `bframes=8`, `rc-lookahead=40`.
- SVT-AV1 preset 8 → 5, `tune=0`.
- VAAPI `-rc_mode CQP` → ICQ; NVENC needs `-b:v 0` alongside `-cq`; VideoToolbox ignores the
  quality value entirely.
- SSIM gate: default 0.95 → 0.97, and **fail closed** — today an unmeasurable or timed-out
  comparison is accepted and the original recycled, which is backwards for a feature whose
  claim is "no visible difference".

**Consequence:** higher quality means bigger outputs, so more files trip "wasn't smaller".
That currently records a blocklist strike, so raising quality mechanically causes more
permanent exclusions. Report it as "already efficient at your quality setting" instead.

---

## Settings — keep it small

Every knob is a small failure against "just press Convert".

**Keep:** format (the setup choice), quality preset (3 options), schedule window, core cap.
**Advanced:** 4K threshold, SSIM threshold, AV1-recodes-HEVC opt-in.
**Delete:** `convert_keep_audio_langs`, `convert_add_stereo`, `convert_loudnorm`. These exist
only to *break* the audio guarantee — loudnorm re-encodes Atmos to 256k AAC. They were dead
until recently wired live, and have no UI.

---

## Phases

| # | Work | Why this order |
|---|---|---|
| 1 | Core cap + nice | Unblocks CPU on a shared box; nothing else is usable without it |
| 2 | Format rules, ordered routing, QSV/VAAPI order, HLG fix | The correctness core |
| 3 | HDR breakdown in stats + first-run setup flow | The format cards need real counts |
| 4 | Durable + visible skips | Turns silent exclusions into something actionable |
| 5 | Encoder tuning, SSIM 0.97 + fail-closed, delete audio settings | Delivers the quality promise |
| 6 | A/B sample test (CPU vs GPU numbers) | Sets the phase-2 threshold from measurement |

---

## Self-critique

**Phase 6 is arguably too late.** The 4K threshold in phase 2 is set from my assumptions
about relative encoder quality, which I cannot measure and have already been wrong about
once this session. Moving the A/B test earlier would ground it — the counter-argument is that
Balanced is a safe default either way and the test is more work than the rule.

**I reverse myself on 10-bit.** I earlier said encode 8-bit sources as 10-bit. As a default
that's wrong for a media server: HEVC Main10 isn't universally supported by older TV clients
and can turn direct-play files into transcodes, costing more than the banding it fixes.
Dropped from the plan.

**AV1's value is smaller than it looks on this library.** With HEVC re-encoding off by
default, AV1 only applies to the H.264 remainder — so for a library that's already 70% HEVC,
the two options converge. That's honest, but the settings screen must not oversell AV1.

**I cannot test any hardware path.** The QSV reorder changes which encoder the server
actually uses. The existing hardware→CPU fallback means an unsupported flag degrades safely,
but "degrades safely" is not "verified". One real file before this touches a library.

**Settings count is still the main risk.** Four visible plus three advanced. Every one needs
to justify itself against "it just works".

**Not doing:** package changes, VMAF (not in this ffmpeg build), per-media-type routing
(resolution is a good enough proxy), any form of rule builder.
