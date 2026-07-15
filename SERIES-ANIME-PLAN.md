# Series: anime / scene / daily support — scoping plan

*Scoping only — not built yet. Today the parser is `SxxExx`-only, there's no series `type`, and daily
(air-date-named) + anime (absolute-numbered) releases won't match. This is the plan to fix that.*

---

## Why it's needed

Three release-naming worlds the current engine can't handle:

| World | Example release name | Today's result |
|---|---|---|
| **Standard** | `Show.S03E07.1080p.WEB-DL...` | ✅ matches |
| **Daily / talk / news** | `The.Daily.Show.2024.01.15.1080p...` | ❌ no match (parser can't read the date) |
| **Anime (absolute)** | `[SubsPlease] One Piece - 1075 (1080p)` | ❌ no match (no SxxExx; it's an absolute episode number) |

Anime also brings: fansub group tags (`[Erai-raws]`, `[SubsPlease]`), version bumps (`v2`), dual-audio/
subs markers, and batch packs (`One Piece - 001-1075`). And some communities renumber ("scene numbering")
so a release's numbers don't match TMDB's.

---

## The four pieces

### 1. Series `type` (standard | daily | anime)
- Add `type TEXT NOT NULL DEFAULT 'standard'` to the `series` table (migration).
- Set it on add: TMDB gives enough signal — `type` field + genres (Animation + Japanese origin → anime;
  a talk/news format or a daily episode cadence → daily). Let the user override in the UI (a small
  select on the series detail).
- The type selects **which matching strategy** the acquire loop uses. Everything downstream keys off it.

### 2. Parser: three numbering modes (`internal/parser`)
Extend the parser to recognise, per release name:
- **SxxExx / SxxExxExx** (have it) → season+episode(s).
- **Absolute** — a bare number after the title (`One Piece - 1075`, `Naruto 220`, `... - 001-1075` batch).
  Return an `AbsoluteEpisode int` (and `AbsoluteRange [2]int` for batches). Only trust this for anime
  series (a bare number in a standard release is usually junk).
- **Daily** — `YYYY.MM.DD` / `YYYY-MM-DD` in the name → `AirDate string`.
- Also parse anime extras: **fansub group** (`[Group]` prefix), **version** (`v2` → prefer higher), and
  keep them for scoring/dedup.

### 3. Absolute → season/episode mapping
Anime is stored in TMDB as seasons, but released as absolute numbers. We need a map
`absolute → (season, episode)`.
- **Source:** TMDB already returns per-episode data with air order; build the absolute index by walking
  seasons in order (S1E1 = abs 1, … continuing across seasons, skipping specials/season 0). Cache it on
  the series (a small `episode_absolute` column on `episodes`, filled at add/refresh).
- **Match:** an anime release with `AbsoluteEpisode = 1075` resolves to whatever (season, episode) has
  `episode_absolute = 1075`. Then the normal "is this episode wanted / is it an upgrade" logic applies.
- **Batches** (`001-1075`) map to a *range* of episodes → treat like a season/complete pack in the
  existing pack-tier grab engine.

### 4. Scene numbering (optional, last)
Some releases use community renumbering that differs from TMDB. Two options, in order of effort:
- **v1 (cheap):** a per-series manual "scene offset" the user sets when a show is mis-numbered (covers the
  common "absolute vs seasonal off-by-a-cour" cases).
- **v2 (full):** integrate an external scene-mapping source (the XEM-style anime-lists mappings) — a
  fetch + cache of `tvdb/tmdb ↔ scene ↔ absolute` tables. Larger; defer until v1 proves insufficient.

---

## Where the code changes land

- **Migration:** `series.type`, `episodes.episode_absolute` (+ optional `series.scene_offset`).
- **`internal/parser`**: absolute + daily parsing, group/version extraction, `Release` fields
  `AbsoluteEpisode`, `AbsoluteRange`, `AirDate`, `Group`, `Version`.
- **`internal/series`**: build the absolute index at add/refresh; `type` on the model + a setter;
  `EpisodeByAbsolute(abs)` / `EpisodeByAirDate(date)` repo lookups.
- **`internal/automation/series*.go`**: the release-matching function (`seriesReleaseMatches`) branches on
  series type — SxxExx for standard, absolute-lookup for anime, air-date-lookup for daily. Scoring adds a
  small "prefer higher version" and keeps fansub-group as a tiebreaker (not a quality signal).
- **Frontend**: a `type` select on the series detail; the episode list can show the absolute number for
  anime; nothing else changes visually.

## Suggested phasing

1. **Daily** first — smallest (just air-date parse + lookup), unblocks talk/news/daily shows.
2. **Anime absolute** — the big one: type detection + absolute index + absolute matching + batch ranges +
   group/version parsing.
3. **Scene numbering v1** — the manual per-series offset.
4. **Scene numbering v2** — external mappings, only if needed.

## Risks / notes
- Absolute matching must be **gated to anime type** — a bare number in a standard release name is noise.
- Specials (season 0) are excluded from the absolute index (they don't get absolute numbers).
- TMDB's episode ordering occasionally disagrees with release-group ordering for long-running anime; the
  manual scene-offset (phase 3) is the escape hatch for those.
- Batch packs already have a home (the pack-tier grab engine) — anime batches just need to resolve to an
  episode range first.
