# Arrmada — Roadmap

> One fleet. Every media job. Zero container sprawl.
>
> Arrmada is a single, self-hosted application that replaces the entire `*arr` stack —
> **Sonarr, Radarr, Readarr, Lidarr, Bazarr, Unpackerr, Tautulli, Overseerr/Jellyseerr, and Prowlarr** —
> with one coordinated, professionally designed system.

**Status:** Planning — this document is the source of truth for scope and sequencing.
**Last updated:** 2026-07-14

> **Companion docs:** [FEATURES.md](FEATURES.md) is the complete feature chart (*what*).
> [BUILD-PLAN.md](BUILD-PLAN.md) is the granular execution roadmap — milestones M0–M8, workstreams,
> deliverables, dependencies, definition-of-done, and the v0.1→v1.1 release train (*how & when*).

---

## Table of Contents

1. [Vision & Principles](#1-vision--principles)
2. [What Arrmada Replaces](#2-what-arrmada-replaces)
3. [Recommended Technical Foundation](#3-recommended-technical-foundation)
4. [Architecture Overview](#4-architecture-overview)
5. [The Unified Experience (Design System)](#5-the-unified-experience-design-system)
6. [Platform Layer (Cross-Cutting Foundations)](#6-platform-layer-cross-cutting-foundations)
7. [Feature Modules](#7-feature-modules)
8. [Migration Strategy](#8-migration-strategy-the-killer-feature)
9. [Delivery Roadmap (Phased)](#9-delivery-roadmap-phased)
10. [Non-Goals](#10-non-goals)
11. [Open Questions](#11-open-questions)

---

## 1. Vision & Principles

Arrmada exists because the modern self-hosted media stack is **operationally exhausting**: nine
separate apps, nine databases, nine config schemas, nine update cadences, nine UIs that don't
match, and a web of cross-integrations each user has to wire by hand.

Arrmada collapses that into **one process, one database, one config, one design language** — while
staying modular internally so users only enable the "ships" they need.

### Guiding Principles

- **One binary, one container, one port.** Deployment should be trivial. No sidecars required.
- **Modular but unified.** Internally decomposed into clean modules; externally it feels like a
  single, cohesive product. Disabling the Books module should feel like a product setting, not a
  missing dependency.
- **Simple by default, powerful on demand.** A first-time user sees a clean, guided experience.
  A power user can reach custom formats, release scoring, naming tokens, and raw indexer queries.
  This is achieved through **progressive disclosure**, never through a dumbed-down ceiling.
- **Convention over configuration** — but every convention is overridable.
- **Shared everything.** Indexers, download clients, notification agents, metadata providers, quality
  definitions, and users are configured **once** and shared across all modules. This is the single
  biggest advantage over running the apps separately.
- **API-first.** Every action in the UI is a documented API call. Automation and third-party clients
  are first-class.
- **Migration is a feature, not an afterthought.** Users must be able to import their existing
  Sonarr/Radarr/Prowlarr/etc. setups with minimal friction. Adoption depends on it.
- **Respectful of the ecosystem.** Compatible with existing indexers, download clients, and media
  servers. We replace the management layer, not the whole world.

---

## 2. What Arrmada Replaces

| Legacy App | Domain | Arrmada Module |
|---|---|---|
| **Prowlarr** | Indexer management & search aggregation | `Indexers` |
| **Sonarr** | TV series automation | `Series` (TV) |
| **Radarr** | Movie automation | `Movies` |
| **Readarr** | Book / audiobook automation | `Books` |
| **Lidarr** | Music automation | `Music` |
| **Bazarr** | Subtitle acquisition & sync | `Subtitles` |
| **Unpackerr** | Post-download archive extraction | `Extraction` (part of Downloads) |
| **Tautulli** | Media server monitoring & analytics | `Insights` |
| **Overseerr / Jellyseerr** | Requests & discovery | `Requests` |
| **Tdarr** | Automated transcoding & library optimization | `Transcode` |

> `Transcode` extends Arrmada beyond the classic `*arr` stack: it replaces **Tdarr** (media
> processing / library optimization). It is the largest single module and is deliberately built last.

Because these share a common core in Arrmada, several "apps" become **thin modules over shared
services** rather than standalone systems. For example, Unpackerr's entire job becomes a feature of
the shared Download pipeline; Prowlarr's indexer store becomes the search backbone every media
module queries.

---

## 3. Recommended Technical Foundation

> These are the working recommendations. They can change before code is written, but the roadmap
> below assumes this shape.

| Layer | Choice | Why |
|---|---|---|
| **Backend language** | **Go** | Single static binary, cheap concurrency for daemon workloads (indexer polling, download monitoring, folder watching), tiny Docker images, no runtime deps. Aligns with "replace 8 containers with 1." |
| **Architecture** | **Modular monolith** | One deployable, clean internal module boundaries, optional modules. Can split later if ever needed. |
| **Frontend** | **React + TypeScript** | Deepest ecosystem for dense, live dashboards; strong progressive-disclosure patterns. |
| **UI system** | **Tailwind + shadcn/ui (Radix primitives)** | Owned, consistent, accessible components → the "everything matches" mandate. |
| **Data grids / server state** | **TanStack Query + TanStack Table** | Purpose-built for queues, history, episode/movie lists. |
| **Database** | **SQLite (default), PostgreSQL (optional)** | SQLite = zero-config single-file for most users; Postgres for large/HA setups. |
| **Packaging** | Static assets embedded in the Go binary | One process serves API + UI on one port. |
| **Distribution** | Single binary + official Docker image + Compose example | Also target unRAID / TrueNAS app catalogs. |

**Deployment target:** `docker run` with a single volume for config/db and mounts for media/downloads.
No external services required to start.

---

## 4. Architecture Overview

```
┌──────────────────────────────────────────────────────────────────────┐
│                          Arrmada (single binary)                       │
│                                                                        │
│  ┌────────────────────────── Web UI (React SPA) ──────────────────┐    │
│  │   Unified design system · one shell · module views             │    │
│  └────────────────────────────────────────────────────────────────┘    │
│                     │ REST + WebSocket (live updates)                    │
│  ┌──────────────────┴─────────────────────────────────────────────┐    │
│  │                        API Gateway / Auth                        │    │
│  └──────────────────┬─────────────────────────────────────────────┘    │
│                     │                                                    │
│  ┌───────── Feature Modules (enable/disable) ─────────┐                  │
│  │  Series · Movies · Books · Music · Subtitles ·     │                  │
│  │  Requests · Insights · Transcode                   │                  │
│  └───────────────────────┬───────────────────────────┘                  │
│                          │ depend on                                     │
│  ┌───────── Shared Platform Services ─────────────────────────────┐     │
│  │  Indexers · Download Clients · Import/Rename Engine ·          │     │
│  │  Metadata Providers · Quality/Custom Formats · Notifications · │     │
│  │  Lists · Media Server Integration · Scheduler/Jobs ·          │     │
│  │  Users/Auth · Config · Health · Backup · Events/Bus           │     │
│  └───────────────────────┬───────────────────────────────────────┘     │
│                          │                                               │
│  ┌───────── Persistence (SQLite / PostgreSQL) · File System ──────┐     │
│  └────────────────────────────────────────────────────────────────┘     │
└──────────────────────────────────────────────────────────────────────┘
        │                    │                      │
   Indexers            Download Clients        Media Servers
 (Torznab/Newznab)   (SAB/qBit/etc.)        (Plex/Jellyfin/Emby)
```

**Key architectural ideas:**

- **Shared platform services** are the heart of the system. Media modules (Series, Movies, Books) are
  relatively thin: they define *what* to acquire and *how to organize it*, then delegate searching,
  downloading, importing, and notifying to shared services.
- **Internal event bus.** Modules communicate through events (`ReleaseGrabbed`, `DownloadImported`,
  `MediaAdded`) rather than direct calls where possible. This keeps modules decoupled and makes
  cross-cutting features (Insights, Notifications, Requests) easy to wire in.
- **Job scheduler + queue.** A single scheduler runs all recurring tasks (RSS sync, refresh, disk
  scan, backups) and a persistent job queue handles async work with ret/backoff.
- **Plugin-ready seams (future).** Metadata providers, indexer definitions, notification agents, and
  download clients are defined behind interfaces so community extensions become possible later
  without committing to a full plugin runtime on day one.

---

## 5. The Unified Experience (Design System)

> This section is a first-class requirement, not polish. The product must feel like **one premium
> application** — sleek, professional, simple when you want it, deep when you need it.

### Experience Pillars

1. **One shell, many modules.** A single persistent navigation shell (sidebar + top bar + command
   palette). Switching from Movies to Analytics never feels like switching apps.
2. **Progressive disclosure.** Every configuration surface has a **Simple / Advanced** posture.
   Beginners get sane defaults and guided setup; power users toggle into custom formats, scoring,
   naming tokens, and raw queries. Nothing is *removed* in simple mode, only *tucked away*.
3. **Density is a choice.** Comfortable and compact layouts. Poster-wall views for browsing,
   high-density tables for operations.
4. **Live, not static.** Queues, activity, and health update in real time via WebSocket. No manual
   refreshes.
5. **Command palette (⌘K).** Jump to any media item, setting, or action instantly. A power-user
   accelerator that stays invisible until summoned.
6. **Accessible & themeable.** Full light/dark support built on a single token system; keyboard
   navigable; WCAG-minded contrast. One theme system across the entire product.

### Design System Foundations (to be built once, used everywhere)

- **Design tokens:** color, spacing, radius, typography, elevation, motion — the single source that
  guarantees visual consistency across all modules.
- **Component library:** buttons, inputs, selects, tables, cards, posters, modals, drawers, toasts,
  tabs, badges/status pills, empty states, skeletons/loaders, wizards.
- **Patterns:** setup wizards, list/detail views, the interactive-search results table, quality-profile
  editor, calendar, activity/queue, the **Transfers view** (live torrent/download management), settings
  forms with Simple/Advanced toggle.
- **Onboarding flow:** first-run wizard (choose modules → add a media server → add an indexer → add a
  download client → add a root folder) so a new user reaches a working setup in minutes.

---

## 6. Platform Layer (Cross-Cutting Foundations)

These shared services are built **once** and power every module. They are the foundation everything
else stands on and therefore lead the roadmap.

### 6.1 Core Runtime
- Config management (file + env + UI), reload without restart where safe.
- **Config-as-code:** declarative, git-manageable export/import of full configuration — natively
  replaces the Recyclarr/Buildarr ecosystem the `*arr`s depend on.
- **Internationalization (i18n) designed in from day one** — hard to retrofit, cheap to build in;
  translations can follow later.
- Database layer with migrations (SQLite + Postgres).
- Structured logging with levels and per-area filtering; log viewer in UI.
- Job scheduler (cron-like recurring tasks) + persistent async job queue with retry/backoff.
- Internal event bus.
- Health-check framework + system status page.
- Backup & restore (automatic scheduled + manual, restore on fresh install).
- Base URL / reverse-proxy support, TLS, CORS.

### 6.2 Identity & Access
- Local users, sessions, password hashing.
- API keys (per-user and per-integration).
- Role-based access (admin / manager / requester / read-only).
- Optional external auth: forward-auth header trust, OIDC/SSO (later phase).
- Per-user preferences (theme, density, default module).

### 6.3 Indexers (replaces Prowlarr)
- Torznab / Newznab protocol support (Usenet + Torrent).
- Indexer definitions/catalog (Cardigann-style YAML definitions for broad coverage).
- Per-indexer capabilities, categories, priorities, rate limits.
- Indexer health monitoring & auto-disable on failure.
- FlareSolverr / proxy support for protected indexers.
- **Aggregated search** across all indexers, exposed to every media module.
- Manual/interactive search UI with release inspection.
- Optional: expose Torznab/Newznab endpoints so *external* apps could still query Arrmada (bridge).
- **Announce-based instant grabbing & cross-seed (later phase, committed):** an autobrr-style
  IRC/announce ingestion path that grabs private-tracker releases seconds after upload, plus
  cross-seed automation for ratio. Deferred to Phase 7, but the release-ingestion seam is designed
  now so it can slot in without rework.

### 6.4 Download Clients
- **External clients (default path):**
  - Usenet: SABnzbd, NZBGet.
  - Torrent: qBittorrent, Transmission, Deluge, rTorrent/ruTorrent.
- **Bundled torrent engine (flagship differentiator):** Arrmada ships with a managed
  **qBittorrent (`qbittorrent-nox`)** instance — the definitive choice, backed by the libtorrent
  (Rasterbar) library and exposing the richest Web API of any client.
  - Runs as a supervised companion (separate container, or a second managed process in a single
    image), headless and pre-configured. Zero setup for the user.
  - Arrmada drives it **entirely via API** and surfaces everything through its own UI (see
    §6.13 Transfers). The qBittorrent WebUI is effectively hidden — **users should have no reason to
    open it.** This closes a real gap in the current `*arr` stack, where torrent state is opaque.
  - All configured behind the shared `DownloadClient` interface, so bundled and external clients are
    interchangeable and users can opt out of the bundled one entirely.
- **Fully embedded engine (future / advanced mode):** an optional zero-sidecar torrent engine via a
  native Go library (e.g. `anacrolix/torrent`) for the purest single-process deployment. Same
  interface; later phase.
- Category/label management, completed/incomplete path handling.
- Remote path mappings.
- Queue monitoring, stalled/failed detection, blocklisting & auto-retry with alternate release.
- **Seeding/ratio management** for bundled/embedded torrents: enforce ratio & seed-time goals,
  private-tracker-safe behavior, and surface ratio state in the UI (a responsibility that shifts to
  Arrmada whenever it owns the torrent engine).

### 6.5 Extraction (replaces Unpackerr)
- Detect completed downloads containing archives (rar/zip/7z/multi-part).
- Extract to the correct location, verify, and clean up archives post-import.
- Hooks into the import pipeline so extraction is invisible to the user.

### 6.6 Import / Rename Engine (the crown jewel)
- Media file recognition & parsing (title, year, season/episode, quality, codec, group, edition).
- Quality detection and upgrade decisioning.
- Root folders & disk-space awareness.
- Configurable naming schemes with token system, per module.
- Hardlink / atomic move / copy strategies; permissions & ownership handling.
- Recycle bin for replaced/removed files.
- Manual import & "fix match" UI for edge cases.
- **Adopt existing library on disk (Core onboarding):** match and import an existing media library
  *without re-downloading* — the make-or-break first action for any new user.
- **Local NFO + artwork writing** for media servers that read metadata from disk (Kodi/Jellyfin).
- **Extras/featurettes/trailers** import (Plex/Jellyfin "Extras" folders).
- **Disk / storage analytics:** per-title usage, multi-drive / mergerfs-pool awareness.

### 6.7 Quality, Custom Formats & Release Scoring
- Quality definitions and size limits.
- Quality profiles with upgrade-until targets (per module, shared definitions).
- **Custom formats** (regex/attribute-based) with scoring for fine-grained release preference.
- Release/preferred-word scoring, indexer priority, propers/repacks handling.
- Importable/exportable profiles (community sharing).

### 6.8 Metadata Providers — *a Phase-1 platform pillar, not an afterthought*
> The single lesson that killed **two** of the apps we're replacing (Readarr→Goodreads,
> Lidarr→MusicBrainz proxy) is metadata fragility. This is treated as a first-class subsystem.
- Movies/TV: TMDB, TVDB, TVmaze, Trakt, OMDb, Fanart.tv.
- Books: Google Books, OpenLibrary, Hardcover, Audible (audiobooks).
- **Multiple providers per media type with automatic fallback** — never a single point of failure.
- **Cross-provider ID reconciliation** and aggressive local caching.
- **Manual override / add-by-ID / custom record creation** — a bad or missing record is never a
  dead end (the failure that made Readarr and Lidarr unusable).
- **Graceful degradation** — library management & imports keep working when a provider is down.
- Artwork fetching & caching (posters, banners, fanart).
- Provider abstraction so sources can be added without touching modules.

### 6.9 Lists / Import Lists
- Trakt, IMDb, TMDB, Letterboxd, MDBList, Trakt watchlists, other Arrmada instances.
- Auto-add + monitor from lists, with sync scheduling and exclusion lists.

### 6.10 Notifications
- Discord, Telegram, Slack, Pushover, ntfy, Gotify, email (SMTP), generic webhook, Apprise bridge.
- Event-driven (grab, import, upgrade, health issue, request approved, etc.).
- Per-agent event filtering; shared across all modules.

### 6.11 Media Server Integration
- Plex, Jellyfin, Emby.
- Library refresh/scan triggers after import.
- Fetch libraries & users (for Requests and Insights).
- Collections/playlists sync (later).

### 6.12 Calendar & Feeds
- Unified calendar across TV/Movies/Books (upcoming, airing, releases).
- iCal feed export; per-module filtering.

### 6.13 Activity, History & Queue — including a first-class **Transfers** view
- Live queue (downloading/importing) with progress.
- **Transfers screen (headline UX):** a full in-app torrent/download management surface so users
  never need the download client's own UI. Tabs/filters for **Downloading · Seeding · Completed ·
  Stalled · Errored**, showing per-item progress %, down/up speed, ETA, ratio, seed time, peers/seeds,
  size, category, and age. Live-updating via WebSocket (fed by the qBittorrent sync API for bundled
  torrents).
- **Inline controls:** pause/resume, force-start, set priority, re-announce, adjust ratio/seed limits,
  remove (with or without data), open in file browser — all driven through the `DownloadClient`
  interface, so the same controls work across bundled and external clients where supported.
- Full history (grabbed/imported/failed/upgraded) with search & filters.
- Blocklist management.

---

## 7. Feature Modules

Each module leans heavily on the platform layer. Descriptions focus on what's *unique* to the module.

### 7.1 Series (replaces Sonarr)
- Series → seasons → episodes hierarchy with per-series/season monitoring.
- Scene numbering, absolute numbering (anime), episode/season packs.
- Air-date & scene-name aware parsing.
- Season pass, series types (standard/daily/anime).
- Interactive & automatic search; RSS monitoring.

### 7.2 Movies (replaces Radarr)
- Movie library with monitoring & availability (announced/in-cinemas/released/physical).
- Editions, collections (from TMDB), minimum availability rules.
- Custom-format-driven quality upgrades.

### 7.3 Books (replaces Readarr)
- Authors → books → editions; ebook and audiobook support.
- Metadata & cover management; format preferences (epub/mobi/pdf, m4b/mp3).
- Author monitoring & list import.
- **Multi-provider metadata with fallback** (Open Library / Hardcover / Google Books) + manual
  override — the hard lesson from Readarr's death (single-source Goodreads dependency).

### 7.4 Music (replaces Lidarr) — *lowest priority; built last (Phase 8)*
- Artist → album → release → medium → track hierarchy; per-level monitoring.
- **Track-first data model** (not album-only) to match modern listening and make matching robust.
- **Multi-provider metadata with fallback + manual album/release creation** — the same lesson as
  Books, because Lidarr's single MusicBrainz-proxy dependency broke "add artist"/search for *months*
  in 2025. Never bind the library to one fragile upstream; allow local overrides.
- **First-class single-file + CUE splitting** (Lidarr's 7-year-open #515) — unlocks the large share
  of lossless releases that ship as one FLAC/APE + cue; shares the Extraction pipeline's FLAC/cue
  logic.
- **Rich audio tagging + ReplayGain** natively (Lidarr's weakest area; users offload to beets today).
- Lossless/lossy quality tiers **with Hi-Res / sample-rate / bit-depth granularity** (fixes #4153).
- Editions/masters awareness; **multi-version tracks** (keep the remaster *and* the original).
- Various-Artists / compilation / soundtrack / box-set handling (Lidarr can't add Various Artists).
- Lists: Spotify, Last.fm, MusicBrainz series/collection, Trakt.
- **Simplified profiles:** collapse Lidarr's confusing Quality-Profile-vs-Metadata-Profile split into
  one guided "what to collect" flow (see FEATURES.md).

### 7.5 Subtitles (replaces Bazarr) — *de-prioritized: an optional fallback, not the primary path*
> **Scope decision (2026-07-14):** most releases already carry subtitles, and the higher-value,
> zero-dependency workflow is to **extract embedded subs → SRT** in the `Transcode` module (§7.8), not
> to download them. External grabbing is therefore demoted to an **optional fallback** for the cases
> extraction can't cover (no embedded subs, a missing language, or image-only subs pending OCR). The v1
> module is built and works; it defaults to a **keyless** provider so it needs zero setup.
- Subtitle providers behind a fallback aggregator, **keyless-first**: **Podnapisi** (no account, no
  limit) as default; **SubDL** (free key, ~50/day) and **OpenSubtitles.com** (account, ~10/day free —
  its free tier was cut from 200→10, which is why it can't be the sole/default source) as opt-in.
- Per-language wanted lists with scoring & provider fallback.
- Automatic + manual search; external **SRT sidecars** saved alongside the media.
- Works across Series and Movies libraries (shares their media catalogs).

### 7.6 Requests (replaces Overseerr / Jellyseerr / "Seerr")
> Overseerr is archived (Feb 2026); Jellyseerr merged into the not-yet-stable "Seerr." A built-in
> module sidesteps that churn — and, because it *shares* Arrmada's metadata, users, and catalogs,
> avoids Overseerr's structural pain.
- User-facing discovery portal (trending/popular/upcoming, genres/networks/studios, cast, recs),
  skinned in the same design system.
- Request → approval → auto-add into Series/Movies/Books modules; approval workflows + auto-approve.
- **Per-episode requests + per-episode availability** (Overseerr is season-only).
- **Version-aware requests** — request 4K / a specific version → a native version track, *no duplicate
  Radarr/Sonarr instance*; and **rule-based routing** to any target (language/quality/content-type),
  not just Standard+4K.
- Media-server user import (Plex/Jellyfin/Emby) + **role/group-based permissions** & **pooled,
  transparent quotas** (fixes Overseerr's per-user 25-flag matrix and bypassable limits).
- **Issue reporting that can act** (report → auto re-search / subtitle fetch / re-grab).
- **No TMDB-vs-TVDB mismatch** — shares the Series module's metadata, so numbering always matches
  acquisition (Seerr's #1 technical bug can't occur).

### 7.7 Insights (replaces Tautulli)
> Tautulli is Plex-only and single-server *by design* (both `wont-fix`). Cross-platform, multi-server,
> built-for-scale is the whole opportunity.
- Live now-playing (streams, player/device, IP+geo, bandwidth, transcode-vs-direct decisions).
- **Plex AND Jellyfin AND Emby**, **multi-server first-class** with cross-server identity
  reconciliation (Tautulli refuses both).
- Watch history & statistics (users, top content, play counts, completion), on a **history store
  designed for scale** (fixes Tautulli's large-history slowdown & DB bloat).
- **Simplified notifications** (per-trigger conditions + validation/preview), newsletters, server
  up/down monitoring, full API/export.
- Library growth analytics, tying acquisition (from other modules) to consumption.
- Dashboards built on the shared data-viz system.

### 7.8 Transcode / Optimize (replaces Tdarr) — *the biggest single module; built last*
> Tdarr is powerful but notoriously hard to understand: opaque plugin stacks and JSON "flows," a
> confusing node/server split, and almost no visibility into what a job will actually do to a file.
> Arrmada's goal is the **same outcomes — automated transcoding, track cleanup, health checks, space
> saving — at a fraction of the cognitive load.** "Better and way easier than Tdarr" is the mandate.
- **Preset-first, plugin-never (by default).** One-click intents cover the common jobs: *Save space*
  (re-encode to H.265/AV1 to a quality target), *Strip unwanted tracks* (drop foreign-language audio
  & subtitle streams, keep configured languages), *Remux* (change container without re-encoding),
  *Standardize* (target codec/container/bitrate), *Health check* (detect corrupt/broken files). Power
  users get a **visual, readable pipeline builder** — never hand-edited JSON.
- **Extract embedded subtitles → external SRT (the primary subtitle workflow).** Pop *text* subtitle
  tracks already inside a file (SubRip/ASS/mov_text) out to synced `.srt` sidecars in one pass, then
  optionally strip them from the container. This is free, offline, and perfectly synced — and covers
  the common case, so it (not Bazarr-style downloading) is Arrmada's default way to get sidecar subs.
  **OCR for image-based subs** (BluRay PGS/SUP, DVD VOBSUB → SRT) is a later slice; downloading missing
  or non-embedded languages stays the job of the optional Subtitles fallback (see §7.5).
- **Show-before-you-run.** Every rule previews exactly what will happen per file: source→target codec,
  which audio/subtitle tracks get removed, container change, and an **estimated size delta** — before a
  single byte is written. No more guessing what a flow did.
- **Library-aware rules** with filters (resolution, codec, container, bitrate, size, age, language
  tracks) over the shared Movies/Series catalogs. Run **on import** (post-processing) or as a
  **scheduled library sweep**.
- **ffmpeg-powered, zero extra setup** — reuses the ffmpeg already bundled (audiobook merge). No
  separate transcode "nodes" to install for the single-machine case (Tdarr forces the node/server
  model on everyone).
- **Hardware acceleration** (QSV / NVENC / VAAPI / VideoToolbox) with safe auto-detection + CPU fallback.
- **Safe by default:** work on a copy, verify the output (duration/stream checks) before replacing the
  original, route replaced files through the shared **recycle bin**, and keep a full history with
  **one-click revert**.
- **Space & health analytics:** library-wide reclaimed-space totals, per-title before/after, and a
  corruption/health report — surfaced through the shared Insights data-viz system.
- **Distributed workers (later phase).** Optional extra worker processes/machines for large libraries;
  single-node "just works" out of the box.

> **Full module build plan:** [CONVERT-BUILD-PLAN.md](CONVERT-BUILD-PLAN.md) — the complete feature
> inventory (core + critical safety/correctness + should-have + nice-to-have) and the phased build
> (C0 analysis/hardware → C1 safe engine → C2 rules/preview → C3 track/HDR → C4 quality/scale →
> C5 UI → C6 analytics → C7 advanced), with dependencies and risks.

---

## 8. Migration Strategy (the killer feature)

Adoption hinges on making it painless to leave the old stack. Migration is a headline feature, not a
utility buried in settings.

- **Importers** for Sonarr, Radarr, Readarr, Lidarr, Prowlarr, Bazarr, Overseerr configs:
  - Read their databases/config (or via their APIs) to import libraries, quality profiles, custom
    formats, indexers, download clients, root folders, and naming schemes.
- **Side-by-side mode.** Run Arrmada alongside existing apps during transition; it can adopt an
  existing library without re-downloading (reads existing media on disk).
- **Import wizard** that maps legacy concepts → Arrmada concepts and previews changes before applying.
- **Config export/import** between Arrmada instances for backup and sharing.

---

## 9. Delivery Roadmap (Phased)

Sequencing favors building the **shared platform first**, then landing one full media module as the
reference pattern, then fanning out the remaining modules cheaply on top of shared services.

### Phase 0 — Foundation & Skeleton
*Goal: a running, empty-but-real application shell.*
- Repo, build tooling, CI, Docker image, dev environment.
- Core runtime: config, DB + migrations, logging, health, scheduler/job queue, event bus.
- Auth: users, sessions, API keys, roles.
- API gateway + WebSocket layer.
- **Design system foundations**: tokens, core component library, app shell, navigation, command
  palette, light/dark theming.
- First-run onboarding scaffold.

**Exit criteria:** You can log in, see the shell, toggle modules on/off (stubs), and the system passes
health checks. Nothing acquires media yet.

### Phase 1 — Shared Acquisition Platform
*Goal: everything needed to find, download, and organize a file — module-agnostic.*
- Indexers (Torznab/Newznab + definitions catalog + aggregated search).
- Download clients (at least SABnzbd + qBittorrent to cover Usenet + Torrent), behind the shared
  `DownloadClient` interface.
- **Bundled qBittorrent engine** shipped & managed by Arrmada, plus the **Transfers view** (live
  downloading/seeding/completed management) — so torrent state is fully visible in-app from day one.
- Import/rename engine + root folders + naming + hardlink/atomic move + recycle bin.
- Extraction (Unpackerr functionality).
- Quality definitions, quality profiles, custom formats + scoring.
- Metadata provider abstraction (TMDB to start).
- Notifications (Discord + webhook + email to start).
- Activity/History/Queue + blocklist.

**Exit criteria:** Given a manual release, Arrmada can grab → download → extract → import → rename →
notify → refresh media server. Proven with the Movies module next.

### Phase 2 — First Reference Module: Movies (Radarr parity path)
*Goal: one module fully end-to-end, proving the architecture.*
- Movies module: library, monitoring, availability, interactive + automatic search, RSS, upgrades.
- Lists/import lists (Trakt/TMDB/IMDb).
- Calendar (movies).
- **Radarr importer** for migration.

**Exit criteria:** A user could realistically replace Radarr with Arrmada.

### Phase 3 — Series Module (Sonarr parity path)
- Full TV hierarchy, monitoring, scene/absolute numbering, anime handling, season packs.
- Series calendar; **Sonarr importer**.
- Generalize any TV-specific gaps discovered while building Movies.

**Exit criteria:** Sonarr + Radarr both replaceable.

### Phase 4 — Subtitles + Requests
- Subtitles module over existing Series/Movies catalogs (Bazarr parity), **Bazarr importer**.
- Requests portal + approvals + media-server user import (Overseerr/Jellyseerr parity),
  **Overseerr importer**.

**Exit criteria:** The "acquire + subtitle + request" core loop is complete for TV & Movies.

### Phase 5 — Insights (Tautulli parity)
- Media server monitoring, watch history & stats, dashboards, newsletters/notifications.

### Phase 6 — Books (Readarr parity)
- Authors/books/editions, ebook + audiobook in one module, multi-provider book metadata
  (Open Library / Hardcover / Google Books) with manual override, **Readarr importer**.
- Establishes the **multi-provider-metadata-with-fallback** pattern that Music will reuse.

### Phase 7 — Hardening, Scale & Ecosystem
- PostgreSQL support at scale, performance passes, large-library optimization.
- OIDC/SSO, advanced RBAC.
- **Announce-based instant grabbing (autobrr-style) + cross-seed** — the power-user acquisition layer.
- Plugin seams for community indexer/metadata/notification extensions.
- unRAID/TrueNAS app catalog packaging.
- Comprehensive docs, API reference, migration guides.

### Phase 8 — Music (Lidarr parity) — *deliberately last*
> **Priority note:** Music is intentionally the **final** module. It's the hardest domain
> (track-level matching, single-file+CUE, fragile metadata, high tagging expectations) for arguably
> the least universal demand, so everything else ships first. It reuses the Books multi-provider
> metadata pattern and the Extraction FLAC/CUE logic, so building it last is also cheapest.
- Artist/album/release/track (track-first model), multi-provider music metadata (never a single
  fragile upstream), single-file+CUE splitting, native tagging + ReplayGain, Hi-Res quality tiers,
  simplified profiles, **Lidarr importer**.

### Phase 9 — Transcode / Optimize (Tdarr replacement) — *the biggest single module*
*Goal: automated, easy-to-understand media processing & library optimization.*
- ffmpeg-based processing engine with **presets** (save-space / strip-tracks / remux / standardize /
  health-check) and a **readable visual pipeline builder** (no JSON flows).
- **Per-file preview** with estimated size delta before running; library-wide rules with filters.
- Run **on import** (post-processing hook) and as a **scheduled library sweep**.
- Hardware-accelerated encoding (QSV/NVENC/VAAPI/VideoToolbox) with CPU fallback.
- **Safe verify-then-replace** with recycle-bin routing + one-click revert; reclaimed-space & health
  analytics into Insights. Optional **distributed workers** as a follow-on.

> Deliberately last: it's independent of the acquisition pipeline and reuses the Extraction/ffmpeg,
> recycle-bin, scheduler, and Insights foundations, so building it after everything else is cheapest.

> **Ordering rationale:** Phases 0–1 are ~60% of the total engineering effort but unlock every module.
> After that, each new module (Phases 2–6) is comparatively cheap because it reuses the platform.
> Hardening (7) is largely continuous, and Music (8) is deliberately deferred to the very end. This
> is the core payoff of the modular-monolith design.

---

## 10. Non-Goals

- **Not** a media *player* or streaming server. Arrmada manages acquisition/organization/analytics; it
  integrates with Plex/Jellyfin/Emby rather than replacing them.
- **Not** a general-purpose file manager or torrent client — it orchestrates existing download clients.
- **Not** a microservices platform. Deliberately a single deployable.
- **Not** shipping a full third-party plugin runtime in v1 (seams are designed for it; the runtime is a
  later phase).
- **Not** targeting mobile-native apps initially (responsive web first; native apps are a stretch goal).
- **Not** covering **comics / manga** in the Books module — Books is eBooks + audiobooks only (done
  better than Readarr). Comics (Mylar/Komga/Kavita territory) is a different data model, left to
  dedicated tools.

---

## 11. Open Questions

- **License & governance:** OSS license choice (e.g., GPL to align with the ecosystem, or more
  permissive) and contribution model.
- **Indexer definitions:** author our own definition set vs. adopt/adapt an existing open format
  (Cardigann/Jackett-style) for instant coverage.
- **Bundled torrent packaging:** ship `qbittorrent-nox` as a *separate companion container* (cleaner
  process isolation, easy independent updates) vs. a *second supervised process inside one image*
  (truest "one container" story). Both keep the same API-driven UX; this is a packaging decision.
- **Embedded engine timing:** is the zero-sidecar `anacrolix/torrent` mode a real target (and when),
  or does bundled qBittorrent satisfy the goal indefinitely?
- **Music acquisition sources:** the Lidarr community has largely routed around weak indexer results
  by feeding a wanted-list into **Soulseek (slskd) via Soularr** and streaming-source rippers. Do we
  treat P2P/Soulseek and streaming sources as first-class acquisition backends for Music (a real
  differentiator), or stick to Usenet/torrent indexers only? (Weigh legal/scope carefully.)
- **Legacy API bridges:** should Arrmada expose Sonarr/Radarr-compatible API endpoints so existing
  tools (e.g., mobile apps, Overseerr-alternatives) keep working during migration?
- **Config format:** single file (YAML/TOML) vs. DB-backed settings with file overrides.
- **Naming of internal modules in the UI:** functional names (Movies, Series) vs. themed "fleet"
  naming — likely functional for clarity, with fleet metaphor reserved for branding.
- **Minimum supported platforms** for first release (Linux x64/arm64 for sure; Windows/macOS native?).

---

*This roadmap is intentionally living. Scope will be refined per phase; the phase ordering — platform
first, reference module second, fan-out third — is the stable backbone.*
