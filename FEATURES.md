# Arrmada — Feature Chart (Completed-Product Vision)

> This is the target feature set for the **finished** Arrmada, synthesized from a deep review of
> **Sonarr, Radarr, Readarr, Lidarr, Bazarr, and Unpackerr** — their complete feature inventories *and*
> their documented friction points (GitHub issues, TRaSH-Guides, Reddit, forums).
>
> It is organized around three product mandates:
> 1. **It must feel simple.** The `*arr` stack's biggest documented failure is that its power is
>    unusable without an external guide project (TRaSH-Guides) and an external sync tool (Recyclarr).
>    Arrmada's job is to make the simple path genuinely simple and keep the power reachable.
> 2. **Multi-version / multi-file** for Movies *and* Series (keep 1080p **and** 4K, theatrical **and**
>    director's cut, side by side) — a capability none of the originals have.
> 3. **Multi-season / bulk acquisition** for Series — "get me this whole show" in one action.

**Status:** Planning. Companion to [ROADMAP.md](ROADMAP.md). This defines *what*; the roadmap defines *when*.
**Last updated:** 2026-07-13

> **⚠️ Scope discipline.** This is the *completed-product* vision — it is deliberately large and spans
> years. The failure mode is letting breadth pull the MVP wide. The roadmap's "platform first → one
> reference module (Movies) → fan out" ordering guards against this: **Movies must ship narrow and
> excellent before any other module starts.** Tiers here (Core/Standard/Advanced/Future) are the lever
> for keeping each module's first release tight.

---

## How to read this chart

Every feature is tagged with a **Disposition** and a **Tier**.

**Disposition** — what we do relative to the originals:

| Tag | Meaning |
|---|---|
| ✅ **Keep** | Match the originals; it already works well. |
| ✨ **Simplify** | Same capability, radically simpler UX (this is where we win on "feels simple"). |
| ⬆️ **Improve** | Keep the capability but fix a documented weakness / do it better. |
| 🆕 **New** | Something none of the originals do (or only via extra tools/instances). |
| ⏭️ **Defer** | Planned, but not near-term. |
| ⛔ **Drop** | Deliberately not doing it (with reason). |

**Tier** — how essential to the product:

| Tier | Meaning |
|---|---|
| **Core** | Table stakes; the product is broken without it. |
| **Standard** | Expected by any serious user; part of "parity." |
| **Advanced** | Power-user depth, hidden behind progressive disclosure. |
| **Future** | Stretch / ecosystem / later phase. |

---

## Part I — The Three Headline Redesigns

These three areas get dedicated design attention because they are where Arrmada is deliberately
*better*, not just equal.

### 1. Quality & Release Selection — the "feels simple" redesign

**The problem we're fixing (documented across Sonarr + Radarr):**
- The system is so complex the community's official advice is *"don't configure it yourself — import
  TRaSH-Guides and keep it synced with Recyclarr."* That is an admission the built-in UX failed.
- **"Quality Trumps All"** is counter-intuitive: users load audio/HDR/release-group custom formats with
  big scores, then are baffled when a higher *video tier* with a low score wins anyway.
- **Scores live in the profile, not the format** — users create a custom format, assign no score, and
  it silently does nothing.
- **Magic numbers** (`Minimum Custom Format Score`, `Upgrade Until = 10000`) with zero in-app rationale.
- **Raw regex** with no builder, no validation, silent failures → log-diving.
- **Download loops** from search-vs-import score mismatches.
- **No "what would win" preview** — users tune blind and validate by trial and error.

**Arrmada's model — one concept, three depths (progressive disclosure):**

| Layer | Who it's for | What they see |
|---|---|---|
| **Presets ("Recipes")** ✨🆕 | Everyone (default) | Plain-language goals: *"Best 1080p,"* *"4K HDR,"* *"Best available under 15 GB,"* *"Smallest decent quality,"* *"Remux collector."* Pick one and it works. Curated, versioned, and auto-updatable — TRaSH-quality defaults **built in**, no external tool. |
| **Guided tuning** ✨ | Intermediate | Human-readable toggles on top of a preset: *Prefer* / *Avoid* / *Never* for attributes (HDR, DV, Atmos, specific groups, x265, etc.) via checkboxes and sliders — no regex, no raw scores. |
| **Advanced rule editor** ✅ | Power users | The full custom-format engine (regex, specs, negation/required, per-profile scores) — for people who want it. Tucked behind an "Advanced" switch. |

**Specific fixes baked in:**
- 🆕 **Live "what would win" simulator** in the profile editor: paste or auto-pull recent releases and
  see *exactly* which one Arrmada picks and **why** (ranked, with the deciding factor named). Kills the
  single biggest source of confusion.
- ⬆️ **One unified ranking model, explained in the UI.** Whatever the precedence (quality vs. score),
  the editor *shows* the order it applies and lets you see the effect — no more invisible
  "Quality Trumps All" surprise. Optionally let users choose whether a preferred attribute may
  outrank a quality tier (the thing people *think* scores do).
- ⬆️ **Scores attached to formats have a sane default**; "this format currently does nothing because
  it's unscored" is surfaced as a warning, not silent.
- 🆕 **Import-vs-search parity check**: warn when a format matches the release name but wouldn't match
  the imported file (the documented download-loop cause), and offer the fix automatically.
- ✨ **Regex builder with inline test + validation** for the rare case someone writes one.
- ⬆️ **Size targeting** uses the "preferred size" sweet-spot model (Sonarr v4) everywhere, exposed as a
  simple slider, not MB-per-minute math.

### 2. Multi-Version / Multi-File — keep several copies of one title

**The gap:** Neither Sonarr nor Radarr can keep multiple files of the same movie/episode. A new grab
**replaces** the old one. The feature requests are ancient and unshipped (Radarr #1910 open since 2017 →
punted to "v6"; Sonarr #4551 "planning only," #312 closed). The **only** workaround is running *two
separate instances* + Overseerr routing. This is one of the most-requested capabilities in the entire
ecosystem.

**Arrmada makes this first-class — "Versions" (a.k.a. quality/edition tracks):**

| Capability | Disposition | Tier | Notes |
|---|---|---|---|
| Multiple files per movie, distinguished by **quality track** (e.g. 1080p + 4K) | 🆕 | Standard | Each title can hold N "versions," each with its own quality target & monitoring. |
| Multiple files per **episode** and per **season** (quality tracks for TV) | 🆕 | Standard | The Sonarr #4551 dream, native. |
| Multiple **editions** side by side (Theatrical + Director's Cut + Extended) | 🆕 | Standard | Radarr parses editions but stores one file; Arrmada stores each as a version. |
| Per-version **quality profile / upgrade track** | 🆕 | Standard | "Upgrade my 4K track toward Remux; keep the 1080p track as-is." |
| **Naming that won't collide** (version/edition/quality tokens required in scheme) | ⬆️ | Core | Solves the documented filename-conflict blocker. |
| Media-server-friendly layout (Plex/Jellyfin multi-version or separate-library modes) | 🆕 | Standard | Route 4K to a 4K library, 1080p to the shared one — the Overseerr-routing use case, built in. |
| **Single instance** does all of the above | 🆕 | Core | Eliminates the "run two instances" workaround entirely. |
| Optional "keep best N, collapse the rest" cleanup | 🆕 | Advanced | Directly requested (Sonarr #312). |

> **⚠️ Design constraint (non-negotiable): multi-version must be strictly opt-in.** This is the
> sharpest tension in the whole product — multi-version is powerful but is *exactly* the kind of
> complexity that made Radarr confusing. The default mental model stays **"one title, one file."** A
> normal user never sees the word "version" until they explicitly click *"Add another version."* If
> version complexity leaks into the default path, we've reintroduced the disease we're curing.
>
> **Synergy — version-aware Requests:** because versions are native, the Requests module gets
> Overseerr-style **separate 4K request/approval flows for free** — "request the 4K version" simply
> creates a 4K track on the title. Overseerr needs a whole second Radarr for this; Arrmada doesn't.

### 3. Multi-Season & Bulk Acquisition — "get me the whole show"

**The gap (Sonarr):** Interactive search is **per-episode or per-season only** — there's no "download
the entire series" picker. Single-episode searches **don't return season packs**, so older/streaming
shows "rarely find results." No pack-vs-episode priority (#7828 closed not-planned). A third-party tool,
**Seasonarr**, exists purely to bulk-grab season packs — proof of unmet demand.

| Capability | Disposition | Tier | Notes |
|---|---|---|---|
| **Whole-series interactive search** ("get all seasons") in one action | 🆕 | Standard | The headline. Pick the best combination of packs/episodes across the whole show. |
| **Multi-season selection** in interactive search | 🆕 | Standard | Choose seasons 1–5 at once; Arrmada assembles best packs + fills gaps with singles. |
| **"Season packs only"** toggle | 🆕 | Standard | Long-requested (#4229). |
| **Pack-vs-episode priority / bandwidth control** | 🆕 | Advanced | Deprioritize huge packs, or prefer them — user's choice (#7828). |
| Smart **pack + gap-fill** planner | 🆕 | Advanced | Grab the season pack, then automatically fetch only the episodes the pack was missing. |
| Robust **partial-pack import** (bad/mismatched episode doesn't stall the whole pack) | ⬆️ | Core | Fixes the documented "whole pack stalls on one bad file / TBA title" pain. |
| Bulk monitoring + search across many series (mass editor → search) | ✅ | Standard | Keep Sonarr's mass editor strength. |

---

## Part II — Shared Platform Feature Charts

These are built once and used by every module (see ROADMAP §6).

### Media Management — Import, Rename, Organize

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Root folders (multiple), free-space display & guards | ✅ | Core | |
| Hardlink / atomic-move / copy with automatic fallback | ✅ | Core | |
| Recycle bin with retention cleanup | ✅ | Standard | |
| Naming token system (folder + file), per media type | ✅ | Core | Keep the power. |
| ✨ **Naming preview + visual token picker** (no wiki needed) | ✨ | Standard | Fixes "naming is trial-and-error." Live sample output as you build. |
| MediaInfo/ffprobe analysis (codec, HDR/DV, audio, langs) | ✅ | Standard | |
| Manual import / "fix match" interactive UI | ⬆️ | Standard | Better matching + clearer conflict resolution than the originals. |
| Permissions (chmod/chown), file-date, extra-file import | ✅ | Standard | |
| ⬆️ **Path/permission pre-flight validator** | ⬆️🆕 | Core | Auto-detects the #1 Docker footgun (client/extractor/library seeing different paths) *before* it fails. |
| Multi-episode file support (`S01E01-E02`) | ✅ | Standard | |
| Cross-filesystem hardlink warning (falls back to copy) surfaced clearly | ⬆️ | Standard | Documented silent-slow-copy pain. |
| 🆕 **Adopt existing library on disk** (match & import without re-downloading) | ⬆️🆕 | **Core** | Elevated from Migration. The #1 thing a new user does — point Arrmada at a 10 TB library and have it *not* re-grab everything. Make-or-break onboarding. |
| 🆕 **Local NFO + artwork writing** (Kodi/Jellyfin read-from-disk metadata) | 🆕 | Standard | Genuine gap — for users whose media server reads metadata from disk, not an online agent. |
| Extras/featurettes/trailers import (behind-the-scenes, deleted scenes, trailers) | ⬆️🆕 | Advanced | Plex/Jellyfin "Extras" folders; Radarr/Sonarr handle these poorly. |
| 🆕 **Disk / storage analytics** (per-title usage, multi-drive / mergerfs-pool awareness, "what's eating space") | 🆕 | Standard | The `*arr`s are weak here; a real differentiator. |

### Extraction (absorbs Unpackerr)

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Auto-extract completed downloads as a **native pipeline stage** | ⬆️🆕 | Core | Because Arrmada owns the download client + importer + paths, most Unpackerr friction (path mismatch, queue desync, "Unknown items," orphaned files) **structurally cannot occur.** |
| Formats: rar, multi-part rar, zip, 7z, tar, gz/bz2, ISO | ✅ | Core | |
| Encrypted rar/7z via password list | ✅ | Standard | |
| Nested / recursive archive extraction | ✅ | Standard | Fix documented deep-subfolder misses. |
| FLAC + embedded CUE split (for music, if Music module ships) | ✅ | Future | |
| Watch-folder mode (extract drops with no module involved) | ✅ | Advanced | |
| ⬆️ **File-lock-aware cleanup** (retry on locked handles; guaranteed orphan/empty-folder removal) | ⬆️ | Standard | Fixes Windows lock + leftover-folder complaints. |
| Uniform retry semantics across API & folder modes | ⬆️ | Standard | Unpackerr's modes differed; ours won't. |
| Seed-safe extraction (keep archive for ratio, import the content) | ✅ | Standard | |
| IO throttle / schedule window for extraction | ⬆️ | Advanced | Fixes documented high-CPU/IO spikes. |

### Indexers (shared search backbone — Prowlarr-class)

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Torznab / Newznab (Usenet + Torrent) | ✅ | Core | |
| Built-in indexer definition catalog (broad coverage out of the box) | ✅ | Core | Cardigann-style; no separate Prowlarr needed. |
| Per-indexer priority, categories, rate limits, flags | ✅ | Standard | |
| Indexer health monitoring + auto-disable + **clear per-indexer status** | ⬆️ | Standard | |
| FlareSolverr / proxy support | ✅ | Standard | |
| **Aggregated search** exposed to every module | ✅ | Core | |
| ⬆️ **Filterable/sortable interactive search** (live filter, negation, columns) | ⬆️ | Standard | Fixes Sonarr #8403 ("results dumped, no post-search filtering"). |
| Optional: expose Torznab/Newznab endpoints (bridge for external apps) | ⏭️ | Future | Migration aid. |
| 🆕 **Announce-based instant grabbing** (autobrr-style IRC/announce channels) | 🆕 | Future | Grabs private-tracker releases *seconds* after upload, before RSS updates. Committed but later-phase. Design the release-ingestion seam now. |
| 🆕 **Cross-seed** (auto cross-seeding across trackers for ratio) | 🆕 | Future | Power-user ratio maintenance. Committed but later-phase. |

### Download Clients

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Usenet: SABnzbd, NZBGet | ✅ | Core | |
| Torrent: qBittorrent, Transmission, Deluge, rTorrent, + blackhole | ✅ | Core | |
| **Bundled, managed qBittorrent** (fully driven via API) | 🆕 | Standard | See ROADMAP §6.4. |
| **First-class Transfers UI** (downloading/seeding/completed, in-app) | 🆕 | Standard | Closes the "can't see torrent state in the *arr stack" gap. |
| Remote path mappings | ✅ | Standard | ...with the pre-flight validator above to de-fang them. |
| Category/label auto-management | ✅ | Core | Auto-set so "Unknown items" never happens. |
| Failed/stalled detection, blocklist + auto-retry alternate release | ✅ | Standard | |
| Seeding/ratio management surfaced in UI | ⬆️ | Standard | |
| Delay profiles (protocol preference + wait-for-better) | ✅ | Advanced | Keep, but explain in plain language. |

### Metadata Providers

> **This is a Phase-1 platform pillar, not a per-module afterthought.** The single lesson that killed
> *two* of these apps (Readarr → Goodreads, Lidarr → MusicBrainz proxy) is metadata fragility.
> Multi-provider aggregation, cross-provider ID reconciliation, caching, local overrides, and graceful
> degradation together form a substantial subsystem that must be designed up front. Underestimating it
> is precisely how the originals died.

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Movies/TV: TMDB, TVDB, TVmaze, Trakt, OMDb, Fanart.tv | ✅ | Core | |
| ⬆️ **Multiple providers per media type with automatic fallback** | ⬆️🆕 | Core | **The #1 Readarr lesson** — never a single point of failure. |
| ⬆️ **Manual metadata override / add-by-ID / custom entry** | ⬆️🆕 | Standard | A bad/missing record must never be a dead end (Readarr's fatal 80% wall). |
| ⬆️ **Graceful degradation** (library stays usable if a provider is down) | ⬆️ | Core | |
| Artwork fetch + cache | ✅ | Standard | |
| Configurable anime numbering (reduce hard XEM dependence) | ⬆️ | Advanced | Fixes documented anime fragility (#5920). |

### Quality / Custom Formats / Scoring

*(Full redesign in Part I.1.)* Summary charts:

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Curated **Presets/Recipes** (built-in, versioned, auto-updatable) | ✨🆕 | Core | No TRaSH/Recyclarr required. |
| Guided Prefer/Avoid/Never toggles | ✨ | Standard | |
| **Full custom-profile authoring in Advanced** — create / clone / edit / delete named profiles from scratch | ✅ | Advanced | The Advanced tab must be a *complete* profile builder, not a read-only view of the preset's numbers: define custom formats (regex/specs/negation/required), per-format scores, the quality ladder & grouping, cutoffs, min/upgrade-until thresholds, and size limits. A power user should be able to ignore presets entirely and build a profile by hand. *(Validated in the mockup as the "same engine, exposed" surface.)* |
| Multiple named profiles, assignable per title / per module | ✅ | Core | |
| Live "what would win" simulator | 🆕 | Standard | Mockup-validated: plain-language result in Simple, scores exposed in Advanced. |
| Import/export profiles (community sharing) + import TRaSH JSON | ✅ | Standard | Interop, not dependence. |
| Propers/repacks handling | ✅ | Standard | |

### Lists / Import Lists

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Trakt, IMDb, TMDB, Letterboxd, Plex watchlist, MDBList, Simkl, StevenLu | ✅ | Standard | |
| Arrmada↔Arrmada sync between instances | ✅ | Advanced | |
| Per-list monitor/profile/root/tags + search-on-add | ✅ | Standard | |
| List exclusions | ✅ | Standard | |
| ⬆️ **Safer list-driven removal** (clear preview before auto-deleting library items) | ⬆️ | Standard | Fixes "list cleaning deleted my movies" surprise. |

### Notifications

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Discord, Telegram, Slack, Pushover, ntfy, Gotify, Email, Webhook, Apprise, Notifiarr | ✅ | Standard | |
| Custom script hooks (env-var payload) | ✅ | Advanced | |
| Event-driven, per-agent event filtering, shared across all modules | ✅ | Standard | Configure once, not 8×. |

### Calendar & Feeds

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Unified calendar across TV/Movies/Books | ⬆️ | Standard | One calendar, not three apps. |
| iCal feed export, per-module filtering, color-by-status | ✅ | Standard | |

### Activity / History / Queue

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Live queue with progress/ETA + manual actions | ✅ | Core | |
| Full searchable/filterable history (grab/import/upgrade/fail/rename) | ✅ | Standard | |
| Blocklist + "blocklist and search again" | ✅ | Standard | |
| ⬆️ Fewer "manual interaction required" dead-ends (clearer guided resolution) | ⬆️ | Standard | |

### System / Admin / Platform

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Backups (scheduled + manual + restore) | ✅ | Core | |
| Health checks with **plain-language remediation** | ⬆️ | Standard | Fix "cryptic health warnings." |
| Task scheduler + job queue | ✅ | Core | |
| Logs viewer with levels | ✅ | Standard | |
| SQLite default, PostgreSQL optional | ✅ | Core | |
| REST API + WebSocket + auto-generated docs | ✅ | Core | |
| ⬆️ **Multi-user + roles** (admin/manager/requester/read-only) | ⬆️🆕 | Standard | Every original is single-user; this is a real gap. |
| OIDC/SSO, forward-auth | ⏭️ | Future | |
| First-run onboarding wizard | ✨🆕 | Core | Reach a working setup in minutes. |
| Migration importers (Sonarr/Radarr/Prowlarr/Bazarr/Overseerr) | 🆕 | Standard | See ROADMAP §8. Adoption depends on it. |
| 🆕 **Config-as-code / declarative config** (git-manageable export/import) | 🆕 | Advanced | Finishes off Recyclarr/Buildarr entirely — power-users manage Arrmada from a repo. |
| ⬆️ **Internationalization (i18n) designed in from day one** | ⬆️ | Core | The `*arr`s are heavily translated; i18n is painful to retrofit, cheap to design in. Translations can follow later. |
| Bandwidth scheduling (throttle downloads in peak hours) | ⬆️ | Advanced | |

---

## Part III — Module Feature Charts

### Series (replaces Sonarr)

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Series→season→episode hierarchy, per-level monitoring | ✅ | Core | |
| Full monitoring modes (All/Future/Missing/Pilot/First/Last/Specials/None) | ✅ | Standard | |
| Series types: Standard / Daily / Anime | ✅ | Standard | |
| **Multi-season & whole-series bulk search** | 🆕 | Standard | Part I.3. |
| **Multi-version tracks per episode/season** | 🆕 | Standard | Part I.2. |
| Season-pack grab + smart gap-fill + robust partial import | ⬆️🆕 | Standard | |
| Scene/absolute numbering, anime handling | ✅ | Standard | |
| ⬆️ More configurable anime numbering (less hard XEM reliance) | ⬆️ | Advanced | |
| Auto-tagging by conditions (genre/network/year) | ✅ | Advanced | |
| TBA-title import delay | ✅ | Standard | |
| 🆕 **Dual-audio / multi-audio preference** (first-class, not a hand-rolled regex) | 🆕 | Advanced | Anime users specifically want "dual audio" as a real toggle. |
| Extras/featurettes/trailers per series | ⬆️🆕 | Advanced | Shared with Media Management. |

### Movies (replaces Radarr)

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Movie library, monitoring, availability (announced/cinemas/released) | ✅ | Core | |
| ⬆️ **Clearer "why isn't it searching?" availability UX** | ⬆️ | Standard | Fixes the "Min Availability = Released does nothing" confusion. |
| Collections (TMDB), monitor whole franchise | ✅ | Standard | With clear preview before mass-adding. |
| **Multi-version tracks (1080p + 4K) & multi-edition (theatrical + DC)** | 🆕 | Standard | Part I.2 — the flagship Movies win. |
| Editions parsing + naming/scoring | ✅ | Advanced | |
| Custom-format-driven upgrades | ✅ | Advanced | Via the simplified system. |
| 🆕 **Version-aware request routing** (4K request → 4K track natively) | 🆕 | Standard | Overseerr needs a second Radarr for this; Arrmada does it via native versions. |
| Extras/featurettes/trailers per movie | ⬆️🆕 | Advanced | Shared with Media Management. |

### Books (replaces Readarr — and learns from its death)

> Readarr is **archived/dead**. It died from a single proprietary metadata dependency (Goodreads) with
> no fallback. Every choice here is shaped by that post-mortem.

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Author→book→edition model | ✅ | Core | |
| ⬆️ **eBook AND audiobook in ONE instance** (per-format profiles) | ⬆️🆕 | Core | Readarr forced two instances — a defining pain point. |
| ⬆️ **Multiple swappable metadata providers + fallback** (Open Library, Hardcover, Google Books) | ⬆️🆕 | Core | The lesson that matters most. Self-hostable, no central single point of failure. |
| ⬆️ **Manual metadata override / add-by-ID / custom record** | ⬆️🆕 | Core | No unadjustable match wall. Large authors & new releases must always be addable. |
| ⬆️ **Graceful degradation** if metadata provider is down | ⬆️ | Core | Library management keeps working. |
| eBook formats (epub/mobi/pdf/azw3), audiobook (mp3/m4b/flac) | ✅ | Standard | |
| ⬆️ Audiobook multi-file → optional M4B merge / chapterization | ⬆️🆕 | Advanced | Fixes Readarr's weak audiobook handling & partial imports. |
| Calibre integration (optional) | ✅ | Advanced | |
| Metadata profiles (filter what gets auto-added) with clear "why excluded" feedback | ⬆️ | Advanced | Fixes silent exclusions. |

### Music (replaces Lidarr — and learns from its worst year)

> Lidarr's data model binds everything to **MusicBrainz via a proxy metadata server the user can't
> replace**. A MusicBrainz schema change in **May 2025 broke "add artist" and search for months**.
> Music is also uniquely hard: many releases per album, track-level (not file-level) matching,
> various-artists compilations, remasters/editions, and far higher tagging expectations than video.

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Artist→album→release→medium→track model | ✅ | Core | |
| ⬆️ **Track-first data model** (not album-only) | ⬆️🆕 | Core | Fixes the "misaligned with modern listening" critique; makes matching robust. |
| ⬆️ **Multiple swappable metadata providers + fallback** (MusicBrainz, Discogs, others) + local cache | ⬆️🆕 | Core | The Lidarr-outage lesson = the Readarr lesson. Never one fragile upstream. |
| ⬆️ **Manual album/release creation & override** | ⬆️🆕 | Core | Lidarr has *no* local override — "add it to MusicBrainz and wait" is not acceptable. |
| ⬆️ **Graceful degradation** when a provider is down | ⬆️ | Core | Library + imports keep working. |
| ⬆️ **First-class single-file + CUE splitting** | ⬆️🆕 | Standard | Lidarr #515 open **7 years**; blocks much of the lossless ecosystem. Shares Extraction's FLAC/CUE logic. |
| ⬆️ **Native rich tagging + ReplayGain** | ⬆️🆕 | Standard | Lidarr's weakest area (users offload to beets/Picard). |
| ⬆️ **Lossless/lossy tiers with Hi-Res / sample-rate / bit-depth granularity** | ⬆️ | Standard | Fixes #4153 (only one FLAC tier today). |
| ⬆️ **Matching that weights format/edition heavily** | ⬆️ | Standard | Lidarr weights format only 1.0 → grabs wrong pressings constantly. |
| **Multi-version tracks** (keep remaster *and* original) | 🆕 | Advanced | Part I.2 applied to music. |
| Various-Artists / compilation / soundtrack / box-set handling | ⬆️🆕 | Standard | Lidarr literally can't add Various Artists. |
| ✨ **Unified "what to collect" flow** (collapses Quality Profile + Metadata Profile) | ✨ | Standard | Fixes the #1 Lidarr confusion — two separate profile systems users constantly conflate. |
| Multi-disc album handling | ✅ | Standard | |
| Lists: Spotify, Last.fm, MusicBrainz series/collection, Trakt | ✅ | Standard | With better title→ID resolution to cut mismatches/429s. |
| Discography grab with a sane "studio LPs + notable EPs" middle setting | ⬆️ | Advanced | Fixes all-or-nothing discography bloat. |
| **Soulseek (slskd) / streaming-source acquisition as first-class backends** | 🆕 | Future | Where the community already votes (Soularr). Flagged as an open scope/legal question in ROADMAP §11. |

### Subtitles (replaces Bazarr)

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Works over shared Series/Movies catalogs | ✅ | Core | |
| Language profiles (Normal/HI/Forced, ordered, cutoff) | ✅ | Standard | |
| Broad provider set (OpenSubtitles, Titlovi, Addic7ed, embedded, etc.) | ✅ | Standard | |
| ⬆️ **Resilient provider layer + per-provider health/status** | ⬆️ | Standard | Fixes opaque provider rot. |
| ⬆️ **Pluggable providers without writing Python** | ⬆️🆕 | Advanced | Bazarr requires code to add sources. |
| ⬆️ **Transparent, tunable scoring (per-provider / per-language weighting)** | ⬆️🆕 | Advanced | Fixes opaque Subliminal math + "cutoff vs score vs cutoff-score" confusion. |
| Subtitle-to-video sync | ⬆️ | Standard | Better native sync than ffsubsync's first-minute-cutoff artifacts. |
| ⬆️ **Native AI generation (Whisper) + auto-translation**, sanely scored | ⬆️🆕 | Advanced | Reduce dependence on rate-limited OpenSubtitles. First-class, not bolted on. |
| Upgrades, blacklist, post-processing (encoding/HI-strip/OCR fixes) | ✅ | Standard | |
| ⬆️ Multi-instance-free (it's just a module) | ⬆️ | Core | Bazarr's single-Sonarr/Radarr limit disappears. |

### Requests (replaces Overseerr / Jellyseerr / "Seerr")

> **Project context:** Overseerr stagnated and was **archived (Feb 2026)**; Jellyseerr merged with it into
> **"Seerr,"** which isn't stable yet — users are unsure which to run. A built-in, always-current
> Requests module sidesteps that churn entirely.
>
> **The decisive advantage — it's a module, not a bolted-on app.** Requests shares Arrmada's metadata,
> users, and Movies/Series catalogs. So the two things Overseerr users fight most just *vanish*:
> (1) the **TMDB-vs-TVDB anime/season mismatch** (Seerr's #1 technical bug) can't happen when Requests
> and acquisition read the same metadata; (2) **4K/version routing needs no duplicate Radarr/Sonarr
> instances** — it's just a native version track.

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Discovery portal (trending/popular/upcoming, genres/networks/studios, cast browsing, recommendations/similar) | ✅ | Standard | TMDB-powered, skinned in the Arrmada design system. |
| Custom discovery sliders (genres/keywords/studios/networks, TMDB list IDs) | ✅ | Advanced | Incl. the TMDB-List-ID source users kept requesting. |
| Movie & TV requests | ✅ | Core | |
| ⬆️ **Per-episode requests + per-episode availability** | ⬆️🆕 | Standard | Overseerr is season-level only, with no indicator of *which* episodes are present — a top unmet request. |
| ⬆️ **Version-aware requests** (request 4K / a specific version → native version track) | ⬆️🆕 | Standard | No duplicate Radarr/Sonarr instance. Overseerr needs two full instances and supports only 2 tracks per title. |
| ⬆️ **Route to any target by rule** (language / quality / content-type), not just Standard+4K | ⬆️🆕 | Advanced | Fixes the heavily-upvoted ">2 instances" routing gap — trivial for us since it's one system. |
| Approval workflows (manual + auto-approve rules, split by type/4K) | ✅ | Standard | |
| ⬆️ **Pooled, transparent quotas** (single combined pool option, visible reset cycle, pending counts) | ⬆️ | Standard | Fixes Overseerr's separate movie/series pools + bypassable limits confusion. |
| ⬆️ **Role/group-based permissions** | ⬆️🆕 | Standard | Reuses platform RBAC. Overseerr is per-user only, with a confusing 25+-flag matrix. |
| User import from Plex / Jellyfin / Emby + local users | ✅ | Core | Shares the platform's user system. |
| Watchlist auto-request (Plex / Jellyfin) | ✅ | Standard | |
| Notifications with per-user prefs + richer embeds | ✅ | Standard | Reuses shared Notifications; fixes thin Discord payloads. |
| ⬆️ **Issue reporting that can act** (report → auto re-search / subtitle fetch / re-grab) | ⬆️🆕 | Standard | Overseerr issues are inert; users bolt on `overr-syncerr`. Ours ties into acquisition + Subtitles. |
| Request status tracking (pending → available, partial) | ✅ | Core | |
| ⬆️ **No TMDB-vs-TVDB mismatch** | ⬆️🆕 | Standard | Requests shares the Series module's metadata, so numbering always matches acquisition. Seerr's #1 bug can't occur. |

### Insights (replaces Tautulli)

> **Tautulli is Plex-only and single-server *by design*** — both are closed `wont-fix` issues, because its
> core is welded to Plex's API. The Jellyfin analytics landscape is fragmented across Jellystat,
> Streamystats, and the Playback Reporting plugin, none matching Tautulli's breadth. **A single pane
> across Plex + Jellyfin + Emby, multi-server, is the core differentiator.**

| Feature | Disposition | Tier | Notes |
|---|---|---|---|
| Live now-playing (streams, player/device, IP + geo, bandwidth, transcode-vs-direct per video/audio/subtitle) | ✅ | Core | |
| Terminate stream with a custom message | ✅ | Standard | |
| ⬆️ **Plex AND Jellyfin AND Emby** | ⬆️🆕 | Core | Tautulli refuses Jellyfin (`wont-fix`). One normalized schema across platforms — the headline win. |
| ⬆️ **Multi-server, first-class** (with cross-server identity reconciliation) | ⬆️🆕 | Core | Tautulli is single-server (declined, "would mean rewriting the core"). |
| Full watch history (per user / item / library / platform), completion tracking | ✅ | Core | |
| Stats & graphs (top content/users/platforms; plays by day/dow/hour/month; transcode breakdown) | ✅ | Standard | |
| Home stat cards | ✅ | Standard | |
| ⬆️ **History store designed for scale from day one** | ⬆️🆕 | Standard | Indexed pagination, no full-table join-then-slice. Fixes Tautulli's documented 350k-row slowdown & DB bloat. |
| User profiles & per-user stats | ✅ | Standard | Reuses platform users. |
| Library stats, recently added, media info | ✅ | Standard | |
| Notifications (playback start/stop/pause, transcode change, watched, buffer, concurrent streams, new device, server up/down) | ✅ | Standard | Reuses shared Notifications. |
| ⬆️ **Simplified notification conditions** (per-trigger, not per-agent; validation + preview) | ⬆️ | Standard | Fixes Tautulli's agent-sprawl and footgun AND/OR condition logic. |
| Recently-added newsletters | ✅ | Standard | With less fiddly image/email setup. |
| Server up/down + remote-access monitoring | ✅ | Standard | |
| Full API + data export | ✅ | Standard | |
| ⬆️ **Own geolocation / enrichment pipeline** | ⬆️ | Advanced | Jellyfin doesn't hand you Plex.tv geo; avoids reintroducing the GeoLite2 licensing hassle. |
| 🆕 **Acquisition → consumption link** (does grabbed content actually get watched?) | 🆕 | Advanced | Unique to a unified stack — Insights can see what the other modules acquired. |
| Mobile / responsive + push | ✅ | Standard | |

---

## Part IV — Cross-Cutting Simplicity Principles

These apply to *every* screen and are how "feels simple" is enforced consistently:

1. **Simple/Advanced toggle on every complex surface.** Nothing is removed in Simple mode — only tucked
   away. Defaults must produce a good result with zero tuning.
   - **Corollary (load-bearing):** the most powerful features — *multi-version* above all — are
     **strictly opt-in** and invisible until summoned. Power must never tax the default path. This is
     the discipline that separates Arrmada from "Radarr but with more knobs."
2. **Curated, built-in presets** for the hard stuff (quality, subtitle scoring, naming) so the *default*
   experience is expert-grade without external guides.
3. **Explain the outcome, not just the settings.** "What would win" simulators, naming previews,
   "why was this rejected," "why is this excluded" — surface the *result* of a config, live.
4. **Guided onboarding + inline validation.** Catch the footguns (paths, permissions, unscored formats,
   availability blocking search) *before* they cause silent failures.
5. **Configure once, share everywhere.** Indexers, clients, providers, notifications, quality — set up
   a single time across all modules.
6. **Plain-language health & errors** with a suggested fix, never a cryptic warning.

---

## Part V — Deliberate "Do Better" Wins (scorecard)

The concrete places Arrmada beats the originals, all traceable to documented pain:

| # | Win | Beats |
|---|---|---|
| 1 | Guided quality system with presets + live simulator (no TRaSH/Recyclarr needed) | Sonarr/Radarr custom-format complexity |
| 2 | Multi-version / multi-edition storage in one instance | Radarr #1910, Sonarr #4551 (unshipped for years) |
| 3 | Whole-series & multi-season bulk search + pack/gap planner | Sonarr #4229/#7828, Seasonarr |
| 4 | In-app Transfers view + bundled torrent client | The whole stack's blind spot |
| 5 | Extraction as a native stage (no path/queue/orphan class of bugs) | Unpackerr's entire friction surface |
| 6 | Multi-provider metadata + manual override + graceful degradation | What killed Readarr |
| 7 | eBook + audiobook in one instance | Readarr's two-instance requirement |
| 7b | Track-first music model: CUE splitting, ReplayGain, Hi-Res tiers, no single-metadata SPOF | Lidarr's #515, weak tagging, MusicBrainz-proxy outages |
| 8 | Transparent, tunable, AI-augmented subtitles; pluggable providers | Bazarr's opaque scoring + provider rot |
| 9 | Filterable/sortable interactive search | Sonarr #8403 |
| 10 | Native multi-user + roles | Every original is single-user |
| 11 | One unified, professional UI + command palette across all modules | Eight mismatched apps |
| 12 | One-click migration importers from each legacy app | No native path off the old stack |
| 13 | Requests: per-episode requests, rule-based routing, no duplicate instances, no TMDB/TVDB mismatch | Overseerr/Jellyseerr/Seerr limits & churn |
| 14 | Insights: Plex **and** Jellyfin/Emby, multi-server, history built for scale | Tautulli is Plex-only, single-server, DB-bloated |

---

## Part VI — Deliberately Dropped / Deferred

| Item | Decision | Reason |
|---|---|---|
| Legacy v3 "Release Profiles" as a separate system | ⛔ Drop | Folded into the unified quality model; don't carry two overlapping systems (a documented Sonarr confusion). |
| Being a media *player* / streaming server | ⛔ Drop | Integrate with Plex/Jellyfin/Emby; not a Non-Goal we cross (see ROADMAP §10). |
| Full third-party plugin *runtime* at v1 | ⏭️ Defer | Design the seams now (providers, indexers, notifiers); ship the runtime later. |
| Torznab/Newznab **outbound** bridge (Arrmada as an indexer source for other apps) | ⏭️ Defer | Migration aid, not launch-critical. |
| Music (Lidarr parity) | ✅ Committed | Now a first-class module (Part III + ROADMAP §7.4), paired with Books in Phase 6 since both share the multi-provider-metadata architecture. |
| Soulseek/streaming acquisition for Music | ⏭️ Defer | Real differentiator (community already uses Soularr→slskd) but scope/legal review needed — ROADMAP §11 open question. |
| Native mobile apps | ⏭️ Defer | Responsive web first. |
| **Comics / manga** in the Books module | ⛔ Drop | **Decided:** Books = eBooks + audiobooks only (Readarr's domain, done better). Comics/manga (Mylar/Komga/Kavita territory) is a separate domain with a different data model; left to dedicated tools. |
| autobrr-style announce grabbing / cross-seed | ⏭️ Defer | **Decided:** committed but later-phase (Future). Design the release-ingestion seam now; build the grabber later. |

---

*This chart is the "what." Sequencing lives in [ROADMAP.md](ROADMAP.md). Tiers here map loosely to
roadmap phases: **Core/Standard** land as each module is built; **Advanced** rides progressive
disclosure within those modules; **Future** is Phase 7+.*
