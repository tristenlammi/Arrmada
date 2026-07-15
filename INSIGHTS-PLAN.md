# Insights — Build Plan

**Insights is Arrmada's Tautulli replacement: Plex watch-monitoring and analytics for the server
admin, in Arrmada's own design.** It is the first module that talks to an external media server.

Goal (user's words): *"basically tautulli's oversight, but in our app's styling. See each Plex
user's activity, the current activity and the details of the stream… a place where I can SEE
historically if things buffered. Basically Tautulli but better, and our styling."*

---

## 1. The core architecture reality

**Plex does not store the rich watch history Tautulli shows.** Plex keeps only a thin
`viewedAt` log — no durations, pauses, transcode decisions, bandwidth, or buffering. Tautulli
produces all of that by **polling `/status/sessions` every few seconds and recording each play
session into its own database.**

Arrmada does the same. Everything in this module sits on one foundation:

```
Plex server  ──HTTP (X-Plex-Token)──▶  plex client  ──▶  poller/recorder  ──▶  SQLite
                                                              │
                                          live sessions ◀─────┘  (also cached in memory
                                                                  for the Activity view)
```

The **poller is also what makes the buffering feature possible**: each poll reads every active
session's `Player.state`; when it reads `buffering`, we log a timestamped buffer event tied to
that stream. So the recorder must log buffer events from day one — the Reliability view is just a
read over that data.

---

## 2. Plex API (what we call, how)

- **Auth:** `X-Plex-Token` header (or `?X-Plex-Token=`). Request `Accept: application/json` — Plex
  defaults to XML but returns JSON on these endpoints.
- **`GET /identity`** — validate connection; grab `machineIdentifier`, version.
- **`GET /status/sessions`** — the heartbeat. `MediaContainer.Metadata[]`, each with:
  - identity: `sessionKey`, `ratingKey`, `type` (movie/episode/track), `title`,
    `grandparentTitle` (show), `parentTitle`, `index`/`parentIndex` (ep/season), `year`, `thumb`.
  - progress: `viewOffset` (ms), `duration` (ms).
  - `User` `{ id, title, thumb }`.
  - `Player` `{ address (IP), remotePublicAddress, device, platform, product, title, state, local }`
    — `state` ∈ playing / paused / **buffering**.
  - `Session` `{ id, bandwidth, location (lan/wan) }`.
  - `TranscodeSession` `{ videoDecision, audioDecision, container, protocol, throttled, progress,
    speed, transcodeHwRequested }` — **present ⇒ transcoding**; its `videoDecision`/`audioDecision`
    (`copy` vs `transcode`) distinguishes Direct Stream from full Transcode.
  - `Media → Part → Stream` — source vs stream resolution/codec/bitrate.
  - **Stream decision** = no TranscodeSession → *Direct Play*; TranscodeSession with both decisions
    `copy` → *Direct Stream*; otherwise → *Transcode*.
- **`GET /library/sections`** (+ `?X-Plex-Container-Size=0` per section for `totalSize`) — library
  stats (Movies count; Shows/Seasons/Episodes; Artists/Albums/Tracks).
- **`GET /library/recentlyAdded`** — the Recently Added strip.
- **`GET /accounts`** — user list + thumbs (merged with users seen in sessions).
- **`GET /status/sessions/history/all?sort=viewedAt:desc`** — Plex's thin native history; used
  **once** to optionally backfill a coarse history before our own recording takes over.

---

## 3. Data model (SQLite migrations)

- **Config** — stored as settings keys, not a table: `insights_plex_url`, `insights_plex_token`,
  `insights_plex_machine_id`, `insights_poll_seconds` (default 5), `insights_enabled`.
- **`plex_users`** — `id` (Plex account id) PK, `username`, `title`, `thumb`, `last_seen_at`.
- **`stream_sessions`** (the history; one row per completed play):
  `id`, `session_key`, `user_id`, `rating_key`, `media_type`, `title`, `grandparent_title`,
  `parent_title`, `media_index`, `parent_index`, `year`, `thumb`, `player_device`,
  `player_platform`, `player_product`, `player_title`, `ip_address`, `location`,
  `started_at`, `stopped_at`, `paused_ms`, `view_offset_ms`, `duration_ms`,
  `decision` (direct_play|direct_stream|transcode), `video_decision`, `audio_decision`,
  `transcode_container`, `orig_resolution`, `stream_resolution`, `orig_bitrate`, `stream_bitrate`,
  `bandwidth`, `buffer_count`.
  Indexes: `started_at`, `user_id`, `rating_key`, `media_type`.
- **`buffer_events`** — `id`, `session_id` FK→stream_sessions, `at`, `view_offset_ms`. Powers the
  Reliability view. (Written live during the session; session_id filled at finalize.)
- **`ip_locations`** — `ip` PK, `city`, `region`, `country`, `country_code`, `lat`, `lon`,
  `looked_up_at`. Geolocation cache (one lookup per distinct IP). Private/LAN IPs short-circuit to
  "Local".
- **`bandwidth_samples`** — `at`, `total_kbps`, `lan_kbps`, `wan_kbps`. One row per poll (or
  down-sampled) so bandwidth can be trended over time, not just shown live.
- `stream_sessions` gains the fields the **deep-dive** needs to be reconstructable from history:
  `subtitle_decision` (none|copy|transcode|burn), `transcode_protocol`, `transcode_hw` (bool),
  `throttled` (bool), `container_src`/`container_stream`, `video_codec_src`/`video_codec_stream`,
  `audio_codec_src`/`audio_codec_stream`, `audio_channels_src`/`audio_channels_stream`.

---

## 4. Poller/recorder lifecycle (`internal/insights/`)

- Background goroutine, tick = `insights_poll_seconds` (default 5s), only if `insights_enabled`
  and Plex is configured.
- In-memory `map[sessionKey]*liveSession` holding: firstSeen (started), last viewOffset, state,
  accumulated paused_ms, buffer spells, snapshot of media/player/transcode.
- Each tick:
  - **New key** → start a live session (record started_at, static media/player/decision fields).
  - **Existing key** → update viewOffset/state; if `state==paused` accrue paused_ms; if
    `state==buffering` and we weren't already buffering, append a `buffer_events` row + bump
    `buffer_count` (debounced: one spell = one event, not one-per-poll).
  - **Key gone** (was live, absent now) → **finalize**: write the `stream_sessions` row
    (stopped_at=now, final offsets/pauses/buffer_count), flush its buffered events' session_id.
- Users upserted into `plex_users` whenever seen.
- Live sessions are exposed to the Activity API straight from the in-memory map (no DB round-trip).

---

## 5. HTTP API (`internal/httpapi/insights.go`)

| Route | Purpose |
|---|---|
| `GET /insights/activity` | Live now-playing (from the poller's memory) — full stream detail. |
| `GET /insights/history?user=&type=&decision=&q=&page=` | Paginated history (the History tab). |
| `GET /insights/users` | Per-user aggregates: last streamed/IP/platform/player/last played, total plays, total duration. |
| `GET /insights/users/{id}` | One user's detail + recent history. |
| `GET /insights/stats?window=30&metric=plays\|duration` | Home watch-statistics cards. |
| `GET /insights/libraries` | Library stats (counts per section). |
| `GET /insights/recently-added` | Recently Added strip. |
| `GET /insights/graphs?window=30&metric=` | Time-series for the Graphs tab (incl. bandwidth trend). |
| `GET /insights/buffering?window=&user=&platform=` | Buffer events + reliability aggregates. |
| `GET /insights/session/{id}` | **Per-stream deep-dive**: full transcode path + human "why". Works live (from poller memory) and historical (from `stream_sessions`). |
| `GET /insights/bandwidth?window=` | Live totals + LAN/WAN trend. |
| `GET /insights/plex`, `PUT /insights/plex`, `POST /insights/plex/test` | Connection config + test. |

Geolocation is applied inline: every activity/history row's `ip_address` is resolved through the
`ip_locations` cache and returned with `city`/`country`/`flag`.

---

## 6. Frontend (`web/src/pages/Insights.tsx`), Arrmada terracotta styling

Tabs mirror the Tautulli screens, restyled:

- **Activity** — live now-playing cards: poster, title (show · S/E), user, player/platform +
  **geolocated IP** (city/country + flag), progress bar, a **decision badge** (Direct Play /
  Direct Stream / Transcode), per-stream bandwidth, and source→stream quality. A **total
  bandwidth** header (LAN / WAN / total). Click a card → **deep-dive**. "Nothing is playing" state.
- **Stream deep-dive** (modal, shared by Activity + History) — the full path: source vs stream
  container/video/audio/subtitle, protocol, HW-transcode, throttle/speed, bandwidth, and a
  plain-English **"why it's transcoding"** ("Client doesn't support HEVC → transcoding video";
  "Burning in subtitles"; "Converting TrueHD → the client can't decode it"). Tautulli's deep-dive
  is dense; ours is diagnostic.
- **History** — filterable table (user / Movies·TV·Music / Direct Play·Stream·Transcode / search),
  stream-type icon + **geolocated IP** per row, pagination, click-through to the deep-dive.
  Matches the Movies/Convert table look.
- **Users** — table: user, last streamed, last IP, last platform, last player, last played, total
  plays, total duration.
- **Graphs** — daily plays by media type, by day-of-week, by hour-of-day, top platforms, top users,
  and a **bandwidth-over-time** (LAN/WAN/total) chart. (Charting approach: **hand-rolled SVG/CSS
  bars & lines** in the Arrmada palette — no heavy chart dep, cohesive look, follows the dataviz
  guidance. Revisit if we want interactivity.)
- **Reliability** *(the headline — "better than Tautulli")* — buffering surfaced as first-class
  history: a timeline of buffer events, **buffer rate by user / platform / title**, and a
  "streams that buffered" list. This is the view Tautulli doesn't really give.
- **Settings** — Plex URL + token, **Test connection**, poll interval, enable toggle.

---

## 7. Config / connection

- Connection = **server URL + `X-Plex-Token`**, stored in settings (token is a secret — entered in
  the UI or `.env`, never surfaced back in full). Env overrides: `ARRMADA_PLEX_URL`,
  `ARRMADA_PLEX_TOKEN`. Added to `.env.example`.
- No new Docker container — Arrmada reaches the user's existing Plex ("Gym Server") over the network.
- **Geolocation** is **offline-first**: `internal/geoip/` reads a local MaxMind **GeoLite2-City.mmdb**
  (free with a MaxMind account; path via `ARRMADA_GEOIP_DB` or dropped in the data dir) and caches
  results in `ip_locations`. No DB present → geolocation is simply off (raw IP shown), no external
  calls. An opt-in public-API fallback (ip-api.com) is a later toggle — off by default so IPs never
  leave the box unless the admin says so.
- **Privacy:** this is an admin oversight tool over the user's own server; it surfaces real users'
  IPs and activity by design. Kept server-side, admin-only (role-gated like the rest).

---

## 8. Phasing

| Phase | Delivers | Verify |
|---|---|---|
| **I0** | `internal/plex/` client (identity/sessions/libraries/accounts/recentlyAdded) + connection settings + **Test connection**. | Connects to the real Gym Server, lists libraries. |
| **I1** | **Activity** tab — live now-playing with full stream/transcode/**bandwidth** detail + total-bandwidth header + **geolocated IPs** + the **stream deep-dive** modal (reads live, no recording yet). `internal/geoip/`. | A real stream shows correctly; decision badge + deep-dive "why" right; IP resolves to a city. |
| **I2** | **Poller/recorder** — migrations, session lifecycle, `stream_sessions` (incl. deep-dive fields) + `buffer_events` + `bandwidth_samples` + `ip_locations`; **History** tab with deep-dive + geo. | Play → pause → stop records one correct row; buffering logs an event; history deep-dive matches. |
| **I3** | **Users** + home **watch-statistics** cards + **library stats** + **recently added**. | Aggregates match Plex/Tautulli for the same window. |
| **I4** | **Graphs** — the five time-series charts + **bandwidth-over-time**. | Shapes match Tautulli's for the same window. |
| **I5** | **Reliability** — the buffering-history view (timeline + per-user/platform/title rates). | Buffer events surface and attribute correctly. |

Each phase is built and verified against the user's **real** Plex once the token is in `.env`
(Claude never sees the token). Before that, the client is built against documented Plex JSON shapes.

---

## 9. Beyond Tautulli — what makes this "better", not just parity

Confirmed in scope (this build):
- **Buffering as first-class analytics** (I5) — Reliability view Tautulli doesn't have.
- **Diagnostic stream deep-dive** (I1) — not just *what* is transcoding but *why*, in plain English.
- **IP geolocation** (I1) — offline GeoLite2, privacy-first.
- **Bandwidth totals + trend** (I1/I4) — live LAN/WAN + history.
- **`*arr` integration** — the structural edge: link transcode/buffer pain to Convert ("these titles
  transcode for everyone → convert them"). Tautulli can't, because it never sees the library side.

Deferred (agreed — later, not now):
- **Notifications / alerting** (Discord/Telegram/webhook on buffer/transcode/start/stop, server
  up-down). This is the big one still standing between us and clearly-better; the recorder and
  bandwidth sampling are being built so the alert engine can hook straight into them later.
- **Stream control** (kill / message a stream). Fast-follow after notifications.

## 10. Open sub-decisions (not blocking I0)

- **Concurrent-streams metric** (Tautulli's "Most Concurrent Streams") needs interval-overlap math
  over `stream_sessions` — cheap, added in I3/I4.
- **History backfill** from Plex's native `/status/sessions/history/all` — optional one-time import
  so the module isn't empty on day one; coarse (no pause/transcode/buffer data). Decide in I2.
- **Charting**: starting hand-rolled SVG; a small lib (e.g. a 5–10KB sparkline/bar helper) only if
  interactivity demands it.
