# Arrmada — Build Plan (Execution Roadmap)

> The granular, sequenced plan for **building** Arrmada. Where [ROADMAP.md](ROADMAP.md) is the strategy
> (*what & why*) and [FEATURES.md](FEATURES.md) is the feature chart (*the complete target*), this
> document is the execution track: milestones, workstreams, concrete deliverables, dependencies, and a
> definition of done for each — plus the release train that turns them into shippable versions.

**Status:** Planning locked; ready to start M0.
**Last updated:** 2026-07-13

---

## How to read this

- **Milestones (M0–M8)** map 1:1 to the roadmap phases but are broken into **workstreams** with
  checkable **deliverables**.
- **Size** is relative T-shirt effort, not a date: **S** < **M** < **L** < **XL**. (Deliberately no
  calendar estimates — they'd be fiction at this stage. Sequence and dependencies are what matter.)
- **DoD** = Definition of Done: the objective bar that says the milestone is really finished.
- **▲ Risk** flags the parts most likely to hurt; see the [Risk Register](#risk-register).

---

## Locked decisions (v1)

These are settled enough to build on. Revisit only with cause.

| Area | Decision |
|---|---|
| Backend | **Go** — single static binary, embedded web assets, one process/port. |
| Frontend | **React + TypeScript**, Vite build, served by the Go binary. |
| UI system | **Tailwind + shadcn/ui (Radix)**, TanStack Query + TanStack Table. |
| Design language | **Warm terracotta** identity, validated in mockups (see below). Light + dark. |
| Database | **SQLite** default (single file), **PostgreSQL** optional (Phase 7). |
| Architecture | **Modular monolith** — shared platform services + enable/disable feature modules. |
| Torrent | **Bundled qBittorrent (`qbittorrent-nox`)** as a managed companion; embedded engine later. |
| Packaging | Official **Docker** image + Compose; unRAID/TrueNAS catalogs in Phase 7. |
| License | **MIT** — fully open source, free to use/fork/redistribute; aligns with the `*arr` ecosystem. Resolved 2026-07-13. |

### Design system — already validated
The two interactive mockups settled the hardest UX questions, so M0 starts from proven ground, not a
blank canvas:
- **Tokens:** warm-night `#181310` / cream `#F4EEE4` grounds, terracotta accent `#DB7A54` (dark) /
  `#C15E39` (light), semantic amber/brick kept off the accent, mono-for-data typographic rule.
- **Shell:** sidebar + grouped nav + top bar + view router; light/dark toggle.
- **Patterns proven:** poster grid, month calendar, transfers table, and the **quality profile editor**
  — Simple (presets → guided → plain-language result, *no scores*) vs Advanced (full engine exposed).
- **Reference:** the mockup is the visual spec for M0's design-system work.

---

## Milestone map

| # | Milestone | Roadmap phase | Ships as | Size |
|---|---|---|---|---|
| **M0** | Foundation & Skeleton | Phase 0 | internal alpha | L |
| **M1** | Shared Acquisition Platform | Phase 1 | internal alpha | XL |
| **M2** | Movies (reference module) | Phase 2 | **v0.1 — first public** | L |
| **M3** | Series | Phase 3 | v0.2 | L |
| **M4** | Subtitles + Requests | Phase 4 | v0.3 | L |
| **M5** | Insights | Phase 5 | v0.4 | M |
| **M6** | Books | Phase 6 | v0.5 | L |
| **M7** | Hardening, Scale & Ecosystem | Phase 7 | **v1.0** | L |
| **M8** | Music | Phase 8 | v1.1 | XL |

> **~60% of total effort is M0+M1.** They unlock every module. After that each module is comparatively
> cheap because it reuses the platform — the payoff of the modular monolith.

---

## M0 — Foundation & Skeleton  ·  *Phase 0*  ·  Size L

**Goal:** a running, empty-but-real application — you can log in, see the shell, toggle modules, and it
passes health checks. Nothing acquires media yet.

**Workstreams & deliverables**
- **Repo & tooling** — monorepo (`/backend` Go, `/web` React), `git init`, CI (build + test + lint),
  Dockerfile (multi-stage: Vite build → embed in Go binary), Compose example, dev hot-reload.
- **Core runtime** — config (file + env + UI, hot-reload where safe); **SQLite** layer + migration
  framework; structured logging + in-UI log viewer; **job scheduler** (cron-style) + persistent async
  **job queue** with retry/backoff; **internal event bus**; health-check framework + status page;
  backup & restore; base-URL/reverse-proxy/TLS/CORS.
- **Config-as-code seam** — declarative export/import scaffolding from day one (fill in per feature).
- **Identity & access** — users/sessions/password hashing; API keys; **RBAC** (admin/manager/
  requester/read-only); per-user preferences. ▲ get the role model right early — retrofitting auth is
  painful.
- **API + realtime** — REST gateway, versioned; **WebSocket** channel for live updates; auto-generated
  API docs.
- **Design system** — Tailwind + shadcn setup; **tokens from the mockup**; app shell (sidebar/topbar/
  router/command palette); light/dark; the core component library (buttons, inputs, tables, cards,
  posters, modals, toasts, tabs, pills, empty/skeleton states, wizard).
- **Onboarding scaffold** — first-run wizard shell; module enable/disable settings.
- **i18n foundation** — externalized strings + locale plumbing from the start (translations later).

**Dependencies:** none (this is the base).
**DoD:** fresh `docker run` → first-run wizard → create admin → see the shell with module toggles →
health checks green → a scheduled job runs and logs. No media features.

---

## M1 — Shared Acquisition Platform  ·  *Phase 1*  ·  Size XL

**Goal:** everything needed to *find → download → extract → organize → notify* for a file — completely
module-agnostic. This is the engine every module plugs into.

**Workstreams & deliverables**
- **Indexers** (Prowlarr-class) — Torznab/Newznab (Usenet + torrent); indexer definition catalog
  (Cardigann-style); per-indexer priority/categories/limits/flags; health + auto-disable;
  FlareSolverr/proxy; **aggregated search API**; **filterable/sortable interactive search** (fixes
  Sonarr #8403). ▲ definition coverage is a long tail — decide build-vs-adopt early.
- **Download clients** — SABnzbd + qBittorrent first (covers Usenet + torrent) behind a shared
  `DownloadClient` interface; category/label auto-management (kills "Unknown items"); remote path
  mappings + **pre-flight path/permission validator** (defuses the #1 Docker footgun); failed/stalled
  detection, blocklist + auto-retry.
- **Bundled qBittorrent** — ship & supervise `qbittorrent-nox`; drive fully via API; **Transfers UI**
  (Downloading/Seeding/Completed, live over WebSocket) — mockup-validated. Packaging decision
  (companion container vs in-image process) resolved here.
- **Extraction** (Unpackerr-absorbed) — native pipeline stage: rar/multi-part/zip/7z/tar/gz/ISO,
  encrypted rar/7z, nested archives; file-lock-aware cleanup; seed-safe extraction; IO throttle.
- **Import / rename engine** (the crown jewel) — parsing (title/year/S·E/quality/codec/group/edition);
  root folders + free-space; naming token system **with live preview + token picker**; hardlink/
  atomic-move/copy with fallback + clear cross-FS warning; recycle bin; **adopt-existing-library**
  (match on disk, no re-download — Core onboarding); manual "fix match" UI; local NFO/artwork writing.
  ▲ correctness here is load-bearing for every module.
- **Quality / custom formats / scoring** — the engine behind the mockup: quality definitions + ladder,
  named profiles, custom formats (regex/specs/scores), min/upgrade-until thresholds, size targeting.
  **Simple UX** (presets → guided → plain-language result) **and Advanced** (full profile authoring:
  create/clone/edit from scratch). Live "what-would-win" evaluation shared by search + the editor.
  Import/export profiles + TRaSH-JSON import (interop, not dependence).
- **Metadata platform** ▲▲ *(elevated — this is a subsystem, not a bullet; the thing that killed
  Readarr & Lidarr)* — provider abstraction; **multiple providers per type + automatic fallback**;
  cross-provider ID reconciliation; aggressive local cache; **manual override / add-by-ID / custom
  record**; **graceful degradation**. TMDB first; architecture must make adding providers trivial.
- **Notifications** — Discord + webhook + email first; event-driven; per-agent filtering; shared across
  modules.
- **Activity/History/Queue** — live queue, searchable history, blocklist.

**Dependencies:** M0.
**DoD:** given a manual release for a test title, the platform can **grab → download → extract →
import → rename → write NFO → notify**, with the whole flow visible in the Transfers/Activity UI and
the quality engine choosing correctly. Proven end-to-end (validated by M2 next).

---

## M2 — Movies (reference module)  ·  *Phase 2*  ·  Size L  ·  **Ships as v0.1**

**Goal:** one module, fully end-to-end, proving the architecture. A user could realistically replace
Radarr.

**Workstreams & deliverables**
- **Movies core** — library (poster/table views, filters, mass editor); monitoring; availability
  (announced/in-cinemas/released) with **clear "why isn't it searching?" UX**; TMDB collections with
  add-preview.
- **Multi-version** ▲ *(flagship — but strictly opt-in; the design constraint from FEATURES §I.2)* —
  a movie can hold multiple versions (1080p + 4K, theatrical + director's cut), each with its own
  quality track & monitoring; collision-safe naming; media-server layout modes. Default UX stays
  "one movie, one file."
- **Search** — automatic (RSS + on-add) + interactive/manual with override.
- **Lists** — Trakt / TMDB / IMDb import lists with safe removal preview.
- **Calendar (movies)** — month grid + agenda (mockup-validated) + iCal.
- **Radarr importer** — one-click migration of library, profiles, custom formats, indexers, clients,
  root folders, naming.
- **Release readiness** — docs, first-run polish, **license decided**, security pass.

**Dependencies:** M1.
**DoD:** fresh install → import from Radarr *or* add movies fresh → monitor → auto-grab → import →
appears correctly (including a multi-version title). Ship **v0.1** publicly.

---

## M3 — Series  ·  *Phase 3*  ·  Size L  ·  Ships as v0.2

**Goal:** Sonarr replaceable. TV is where the multi-* features shine.

**Workstreams & deliverables**
- **Series core** — series→season→episode hierarchy; all monitoring modes; series types
  (standard/daily/anime); auto-tagging.
- **Multi-season & bulk acquisition** ▲🆕 — **whole-series & multi-season interactive search**;
  "season packs only" toggle (Sonarr #4229); pack-vs-episode priority (#7828); **smart pack + gap-fill
  planner**; robust **partial-pack import** (one bad episode doesn't stall the pack).
- **Multi-version tracks** per episode/season (the Sonarr #4551 dream, native).
- **Anime** — scene/absolute numbering; **more configurable numbering** (less hard XEM reliance); dual-
  audio as a first-class preference.
- **Series calendar**; **Sonarr importer**; generalize any TV gaps found while building Movies.

**Dependencies:** M2 (reuses/extends its patterns).
**DoD:** Sonarr + Radarr both fully replaceable; a user can "get this whole show" in one action and keep
1080p + 4K of an episode. Ship **v0.2**.

---

## M4 — Subtitles + Requests  ·  *Phase 4*  ·  Size L  ·  Ships as v0.3

**Goal:** complete the "acquire + subtitle + request" loop for TV & Movies.

**Workstreams & deliverables**
- **Subtitles** (Bazarr-absorbed) — over the shared Series/Movies catalogs (no separate instance);
  language profiles (Normal/HI/Forced, cutoff); broad providers + **per-provider health/status**;
  **pluggable providers without writing code** ▲; **transparent, tunable scoring** (per-provider /
  per-language weighting); native sync (better than ffsubsync artifacts); **AI generation (Whisper) +
  auto-translation**, sanely scored; upgrades, blacklist, post-processing; **Bazarr importer**.
- **Requests** (Overseerr/Jellyseerr/Seerr-absorbed) — discovery portal (skinned in the design system);
  request → approval → auto-add into Movies/Series; **per-episode requests + availability** (Overseerr
  is season-only); **version-aware + rule-based routing** (4K/version → native track, no second
  instance; route by language/quality/content-type); media-server user import + **role/group
  permissions** + **pooled transparent quotas**; **issue reporting that can act** (re-search / subtitle
  fetch); **no TMDB-vs-TVDB mismatch** (shares Series metadata); **Overseerr/Seerr importer**.

**Dependencies:** M2, M3 (subtitles/requests operate over their catalogs; version-aware requests need
multi-version from M2).
**DoD:** request a movie/show as a normal user → approved → grabbed → subtitled → visible; 4K request
routes to a 4K version. Ship **v0.3**.

---

## M5 — Insights  ·  *Phase 5*  ·  Size M  ·  Ships as v0.4

**Goal:** Tautulli replaceable, tying acquisition to consumption.

**Workstreams & deliverables**
- Media-server monitoring — **Plex AND Jellyfin AND Emby, multiple servers at once** (Tautulli refuses
  both), with cross-server identity reconciliation; now-playing, streams, transcode decisions, bandwidth.
- Watch history & statistics on a **history store designed for scale** ▲ (indexed pagination, no
  full-table join-then-slice — fixes Tautulli's large-history slowdown/DB bloat); library-growth
  analytics linking what was acquired to what's watched.
- Dashboards on the shared data-viz system; **simplified notifications** (per-trigger conditions +
  validation/preview); newsletters; **own geolocation/enrichment pipeline** (no GeoLite2 hassle);
  **playback issue tracking** (ties into Requests).

**Dependencies:** M0 (platform), media-server integration (built incrementally since M1); richer tied
to M2–M4 libraries.
**DoD:** connect a media server → see live activity + historical stats + a recently-added newsletter.
Ship **v0.4**.

---

## M6 — Books  ·  *Phase 6*  ·  Size L  ·  Ships as v0.5

**Goal:** Readarr replaceable — **done right**, learning from its death.

**Workstreams & deliverables**
- Author→book→edition; **eBook AND audiobook in one module** (per-format profiles) — no two-instance
  requirement.
- **Multi-provider book metadata + fallback + manual override** ▲ (Open Library / Hardcover / Google
  Books) — reuses M1's metadata platform; this is the whole ballgame for Books.
- eBook (epub/mobi/pdf/azw3) + audiobook (mp3/m4b/flac) handling; **audiobook M4B merge /
  chapterization**; optional Calibre integration; metadata profiles with clear "why excluded" feedback;
  **Readarr importer**.
- *Scope guard:* **eBooks + audiobooks only — comics/manga explicitly out.**

**Dependencies:** M1 metadata platform (hard dependency — do not attempt Books until it's solid).
**DoD:** add a prolific author (the case that broke Readarr) → books resolve via fallback providers →
manual override works when a record is missing. Ship **v0.5**.

---

## M7 — Hardening, Scale & Ecosystem  ·  *Phase 7*  ·  Size L  ·  **Ships as v1.0**

**Goal:** production-grade at scale; the ecosystem power features; **1.0**.

**Workstreams & deliverables**
- **PostgreSQL** support; performance passes; large-library (100k+ item) optimization.
- **OIDC/SSO**, forward-auth, advanced RBAC.
- **Announce-based instant grabbing (autobrr-style) + cross-seed** — the power-user acquisition layer
  (seam designed back in M1; built here).
- **Plugin seams** for community indexer/metadata/notification extensions (runtime, not just
  interfaces).
- **unRAID / TrueNAS** app-catalog packaging; comprehensive docs, API reference, migration guides.
- Full **config-as-code** maturity (git-manageable) — finishes off Recyclarr/Buildarr.

**Dependencies:** M2–M6 (hardening the whole surface).
**DoD:** runs on Postgres at scale with SSO; autobrr-style grabbing works; a community plugin loads.
Ship **v1.0**.

---

## M8 — Music  ·  *Phase 8*  ·  Size XL  ·  Ships as v1.1  ·  *deliberately last*

**Goal:** Lidarr replaceable — the hardest domain, intentionally last (least universal demand, cheapest
to build once Books' metadata pattern & the FLAC/CUE logic exist).

**Workstreams & deliverables**
- Artist→album→release→track, **track-first model**; multi-provider music metadata + fallback +
  **manual album/release creation** ▲ (Lidarr's MusicBrainz-proxy outage lesson).
- **Single-file + CUE splitting** (Lidarr #515, 7 years open) — reuses Extraction's FLAC/CUE logic.
- Native **rich tagging + ReplayGain**; lossless/lossy tiers with **Hi-Res / sample-rate / bit-depth
  granularity**; edition/master awareness + multi-version.
- Various-Artists/compilation/soundtrack/box-set handling; Spotify/Last.fm/MusicBrainz lists;
  **unified "what to collect" flow** (collapses Lidarr's Quality-vs-Metadata-Profile confusion);
  **Lidarr importer**.
- **Open scope question:** Soulseek (slskd) / streaming-source acquisition as first-class backends —
  decide before starting (legal/scope review).

**Dependencies:** M1 metadata platform + M6 (Books) patterns + M1 extraction (CUE).
**DoD:** add a prolific artist → correct releases via fallback → single-file+CUE album splits &
tags correctly. Ship **v1.1**.

---

## Release train

| Version | Contains | Headline |
|---|---|---|
| **v0.1** | M0 + M1 + M2 | "Replace Radarr" — one binary, bundled torrent + in-app transfers, simple-but-powerful quality, multi-version movies, one-click Radarr import. |
| **v0.2** | + M3 | "Replace Sonarr too" — whole-series/multi-season grabbing, multi-version episodes. |
| **v0.3** | + M4 | Subtitles + request portal, all in one app; version-aware 4K requests. |
| **v0.4** | + M5 | Built-in analytics (Tautulli parity). |
| **v0.5** | + M6 | Books/audiobooks in one module, resilient metadata. |
| **v1.0** | + M7 | Postgres/SSO/scale, autobrr+cross-seed, plugins, catalogs. |
| **v1.1** | + M8 | Music. |

---

## Risk Register

| ▲ Risk | Where | Mitigation |
|---|---|---|
| **Metadata fragility** (killed Readarr & Lidarr) | M1, M6, M8 | Treat as a first-class subsystem in M1: multi-provider + fallback + local cache + manual override + graceful degradation. Never a single upstream. |
| **Multi-version complexity leaking into the simple path** | M2, M3 | Hard constraint: strictly opt-in, invisible until "Add another version." Default model stays one-title-one-file. |
| **Quality UX regressing into a scored spreadsheet** | M1 | Mockup-validated pattern is the spec: plain-language result in Simple, scores only in Advanced. |
| **Import/rename correctness** (silent mis-imports erode trust) | M1 | Extensive parser test corpus; manual "fix match" UI; pre-flight path validator. |
| **Indexer definition long-tail** | M1 | Decide build-vs-adopt (Cardigann) early; ship broad coverage, not a trickle. |
| **Scope creep pulling the MVP wide** | all | v0.1 = Movies only, narrow and excellent. Tiers (Core/Standard/Advanced/Future) gate each module's first cut. |
| **qBittorrent bundling/supervision** edge cases | M1 | Same `DownloadClient` interface as external clients so users can always opt out. |
| **Analytics history store bloats/slows at scale** (Tautulli's documented flaw) | M5 | Design the Insights schema for scale from day one: indexed pagination, no full-table join-then-slice, cross-server identity mapping. |

---

## Open decisions to resolve before they block

| Decision | Needed by | Notes |
|---|---|---|
| ~~OSS license~~ | ~~M2~~ | ✅ **Resolved: MIT** (2026-07-13). |
| **Indexer definitions:** build vs adopt Cardigann-style | M1 | Affects coverage at launch. |
| **qBittorrent packaging:** companion container vs in-image process | M1 | Same UX either way; a packaging choice. |
| **Legacy API bridges** (expose Sonarr/Radarr-compatible endpoints) | M2–M3 | Adoption lever for existing mobile apps/tools during migration. |
| **Music acquisition sources** (Soulseek/streaming first-class?) | M8 | Legal/scope review. |
| **Embedded torrent engine** (`anacrolix/torrent`) timing | post-v1.0 | Or does bundled qBittorrent satisfy indefinitely? |

---

*This plan is living: each milestone's deliverables get refined into tasks as it's picked up, and DoDs
are the contract for "done." The backbone — platform first (M0–M1), Movies as the reference (M2), then
cheap module fan-out — is stable.*
