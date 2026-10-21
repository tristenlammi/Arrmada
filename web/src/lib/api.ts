// Thin typed client for Arrmada's JSON API.

import type { PipelineStage } from "./stages";
import type { MediaServerKind, WatchLink } from "../components/brand/servers";

export interface Module {
  id: string;
  name: string;
  enabled: boolean;
  status: string;
}

export interface Status {
  app: string;
  version: string;
  commit: string;
  started_at: string;
  uptime_seconds: number;
  needs_setup: boolean;
  /** First-run setup is refused from where this visitor is (outside the LAN). */
  setup_blocked?: boolean;
  authenticated: boolean;
  external: boolean;
  modules: Module[];
  books_enabled: boolean;
  music_enabled: boolean;
  plex_login: boolean;
  /** Sign in with Jellyfin is on (and a Jellyfin server is connected). */
  jellyfin_login?: boolean;
  /** Its sheet offers Quick Connect too. */
  jellyfin_quickconnect?: boolean;
  /** Whether TMDB browsing works (a key is set). Sent to signed-in callers only. */
  metadata_ready?: boolean;
}

export interface Health {
  status: string;
  version: string;
  commit: string;
  uptime_seconds: number;
  checks: Record<string, string>;
}

export interface Indexer {
  id: number;
  name: string;
  kind: string;
  url?: string;
  username?: string;
  categories?: number[];
  media_types?: string[];
  priority: number;
  min_seeders?: number;
  seed_enabled?: boolean;
  seed_ratio?: number;
  seed_hours?: number;
  enabled: boolean;
  /** The Prowlarr indexer this row mirrors; its name, URL and key come from Prowlarr. */
  prowlarr_id?: number;
  /** Who switched it off: "user" or "prowlarr" (a sync only turns back on its own). */
  disabled_by?: string;
  /** The line a Prowlarr sync left on it, e.g. "Removed from Prowlarr". */
  managed_note?: string;
  /** What it last said it supports: "Movies (imdbid, tmdbid) · TV (tvdbid, season, ep) · 23 categories". */
  caps_summary?: string;
  /** How it has been answering; sent to managers and admins only. */
  status?: IndexerStatus;
}

/** A Test's answer; caps_summary is what a Torznab indexer said it supports. */
export interface IndexerTestResult {
  ok: boolean;
  error?: string;
  caps_summary?: string;
}

/** What a Prowlarr sync changed. flaresolverr_ready is Prowlarr's own answer afterwards. */
export interface ProwlarrSyncResult {
  added: number;
  updated: number;
  unchanged: number;
  disabled: number;
  reenabled: number;
  skipped_usenet: number;
  flaresolverr_ready: boolean;
  notes?: string[];
}

/** How the latest Prowlarr sync went, hourly or clicked. failed_runs counts scheduled runs in a row. */
export interface ProwlarrLastSync {
  at: string;
  trigger: "schedule" | "manual";
  added: number;
  updated: number;
  disabled: number;
  reenabled: number;
  error?: string;
  failed_runs?: number;
}

/** The Prowlarr panel: the sync URL, whether a key is saved, the last sync, and where Prowlarr's web UI is. */
export interface ProwlarrInfo {
  url: string;
  has_key: boolean;
  /** The host port Prowlarr's UI is published on (install.sh may pick 9697 or 9698). */
  ui_port?: string;
  /** A saved address for a Prowlarr on another machine; wins over ui_port. */
  ui_url?: string;
  /** The Unraid all-in-one image's own Prowlarr, while it's switched on (e.g. http://127.0.0.1:9696). */
  bundled_url?: string;
  last_sync?: ProwlarrLastSync;
}

/**
 * IndexerStatus is one indexer's health from the integration status tracker. "failing"
 * failed last time but sweeps still ask it; "backing_off" is left alone by sweeps until
 * backoff_until (a person's own search still asks it); "unknown" hasn't been asked since
 * it was added or edited. Times are RFC 3339, absent when they never happened.
 */
export interface IndexerStatus {
  state: "ok" | "failing" | "backing_off" | "disabled" | "unknown";
  last_ok_at?: string;
  last_error?: string;
  last_error_at?: string;
  failing_since?: string;
  backoff_until?: string;
  consecutive_failures: number;
  queries_24h: number;
  failures_24h: number;
}

export interface NewIndexer {
  name: string;
  kind: string;
  url?: string;
  api_key?: string;
  username?: string;
  password?: string;
  categories?: number[];
  media_types?: string[];
  priority?: number;
  min_seeders?: number;
  seed_enabled?: boolean;
  seed_ratio?: number;
  seed_hours?: number;
  enabled?: boolean;
}

export interface ParsedRelease {
  title: string;
  year?: number;
  resolution?: string;
  source?: string;
  codec?: string;
  hdr?: string[];
  audio?: string[];
  edition?: string;
  group?: string;
  proper?: boolean;
  repack?: boolean;
  /** Read from the name's conventions rather than written in it (a fansub is a web capture). */
  source_inferred?: boolean;
  /** Read from an SD-era signal (DVD, HDTV, XviD) rather than written in the name. */
  resolution_inferred?: boolean;
  // The parser's newer detections, shown when the server sends them.
  streaming?: string;
  languages?: string[];
  multi?: boolean;
  full_disc?: boolean;
  three_d?: boolean;
  upscaled?: boolean;
  extras?: boolean;
}

/** One format tested against a release name in the builder's "Test a release name". */
export interface FormatCheck {
  name: string;
  matched: boolean;
  score: number;
  required?: boolean;
  custom?: boolean;
}

/** A release name judged by a profile as edited (POST /api/v1/quality/explain). */
export interface QualityExplain {
  release: ParsedRelease;
  evaluation: Evaluation;
  formats: FormatCheck[] | null;
}

export interface Candidate {
  name: string;
  release: ParsedRelease;
  size_gb: number;
  seeders: number;
  runtime_min?: number;
}

export interface Evaluation {
  candidate: Candidate;
  eligible: boolean;
  reject_reason?: string;
  quality_score: number;
  format_score: number;
  size_score: number;
  total: number;
  matched?: string[];
  avoided?: boolean; // ranked in the lower tier (an avoided format, or under the bitrate floor)
  avoided_formats?: string[];
  /** Its preferred formats stopped counting: its bitrate collapsed against the best on offer. */
  bonus_waived?: boolean;
  /** Why it ranks below the release just above it: the deciding key and the words for it. */
  lost_on?: string;
  lost_on_text?: string;
}

export interface Decision {
  winner: Evaluation | null;
  why?: string[];
  chosen_over?: string;
  eligible: Evaluation[];
  rejected: Evaluation[];
  /** What put the winner above the runner-up (a rank key: resolution, preferences…). */
  decided_by?: string;
}

export interface QualityPreview {
  preset?: string;
  profile?: string;
  decision: Decision;
  /** What the samples are: "Dune: Part Two (2024), 166 min" or "An episode of a 50-minute show". */
  sample_label?: string;
  /** Target settings that work against each other, shown as warnings under their rows. */
  conflicts?: ProfileConflict[] | null;
}

/** One contradiction in a profile's target; row is codec | hdr | audio | bitrate. */
export interface ProfileConflict {
  row: string;
  msg: string;
}

export interface QualityProfileInfo {
  key: string;
  name: string;
  media_type: string;
  built_in: boolean;
  is_default: boolean;
  summary: string;
  // Files on the profile kept out of upgrades ("keep existing files"). Video only.
  kept?: number;
}

// What a profile delete moved onto its replacement.
export interface ProfileMoveCounts {
  movies: number;
  versions: number;
  series: number;
  books: number;
  artists: number;
  requests: number;
  grabs: number;
}

/** What is really happening to a wanted title (GET /api/v1/wanted). */
export type WantedState =
  | "searching" // the sweep searches it on its backoff ladder
  | "unknown" // the download client can't be read: it may be downloading; searches paused
  | "waiting_download" // a download in flight covers everything it is missing
  | "held_for_review" // its download finished but is held in Review
  | "indexers_failed" // its last search reached no indexer (not a miss)
  | "slowed" // so many empty searches it is checked rarely (books monthly, albums weekly)
  | "not_released"; // not out yet: Upcoming

/** One title on the Wanted view: a movie, series, book or album still being looked for. */
export interface WantedRow {
  media_type: "movie" | "series" | "book" | "music";
  id: number;
  movie_id?: number;
  series_id?: number;
  book_id?: number;
  album_id?: number;
  artist_id?: number;
  title: string;
  year: number;
  poster_url?: string;
  quality_profile: string;
  byline?: string; // a book's author, an album's artist
  missing?: string[]; // a book: "Ebook", "Audiobook", "Audio version"
  episode_count?: number; // series: how many aired episodes are being searched
  state: WantedState;
  waiting_on?: string; // the release a waiting row waits for
  stalled?: boolean;
  waiting_note?: string; // "S03 downloading" while the rest of the show is searched
  review_id?: number;
  last_search_at?: string; // RFC 3339; absent = never searched
  search_misses: number;
  next_search_at?: string; // RFC 3339; absent when no automatic search is coming
  due?: boolean; // the next automatic search is on the sweep's next run
  last_search?: AttemptSummary;
  available_at?: string; // Upcoming: release or air date (YYYY-MM-DD, or a bare year)
  next_label?: string; // Upcoming series: "S02E13"
}

export interface WantedLists {
  searching: WantedRow[];
  upcoming: WantedRow[];
  /** false while the download client can't be read: rows read "unknown". */
  queue_known: boolean;
}

/** One row of Movies → Wanted: the shared Wanted row plus the movie-only fields. */
export interface MovieWantedRow extends Omit<WantedRow, "state"> {
  /** The shared states, and on the Cutoff tab: upgrading (the sweep will look) or not_upgrading. */
  state: WantedState | "upgrading" | "not_upgrading";
  /** A search of it is waiting or running in the movie search queue right now. */
  queued: boolean;
  /** Versions rows: the missing extra tracks. */
  tracks?: string[];
  /** Cutoff rows: the track judged (0 = the default; track names any other). */
  version_id?: number;
  track?: string;
  /** Cutoff rows: what about the file misses its profile's target. */
  detail?: string;
  issues?: { kind: string; msg: string }[];
  will_upgrade?: boolean;
  /** Why the upgrade sweep won't act, when it won't. */
  why_not?: string;
}

export interface MoviesMissing {
  searching: MovieWantedRow[];
  upcoming: MovieWantedRow[];
  /** Films whose own file is in, but a monitored extra version isn't. */
  versions: MovieWantedRow[];
  queue_known: boolean;
}

export interface ActivityDownload {
  hash: string;
  name: string;
  state: string;
  raw_state?: string; // the client's own state, e.g. "stalledDL"
  // finer than state: stalled | metadata | queued | checking | moving | allocating |
  // downloading | seeding | paused | error
  phase?: string;
  seeds?: number; // seeds connected right now
  peers?: number; // leechers connected right now
  swarm_seeds?: number; // seeds in the whole swarm, per the tracker
  last_activity?: number; // unix seconds; 0 = never
  added_on?: number; // unix seconds
  progress: number;
  size_bytes: number;
  down_speed: number;
  up_speed: number;
  eta_seconds: number;
  ratio: number;
  seeding_time?: number; // seconds seeded after completion
  seed_known?: boolean; // true when a seed goal was recorded for this release
  seed_enabled?: boolean; // false = remove as soon as imported (no seeding)
  seed_ratio?: number; // target ratio (0 = no ratio target)
  seed_hours?: number; // target seed time in hours (0 = no time target)
  imported?: boolean; // Arrmada has imported this download into the library
  /** Where it has got to (lib/stages), and for staff why it is there ("Import failed 3× …"). */
  stage?: PipelineStage;
  why?: string;
  quality_profile: string;
  media_type?: string;
  held_by_guard?: boolean; // paused by the disk guard, which won't let it resume yet
  // How an in-flight grab stands against its stall window; absent for torrents Arrmada
  // didn't grab. idle_minutes stays 0 while the clock is held (paused, queued, checking).
  stall?: { idle_minutes: number; failover_in_minutes: number; off: boolean };
  /** Stalled by the rule the stall fail-over's clock follows (lib/queueFilter isStalled), and since when (unix seconds). */
  stalled?: boolean;
  stalled_since?: number;
  /** The download client it's in, and that client's name in Settings → Connections. */
  client_id?: number;
  client_name?: string;
  /** An Arrmada grab recorded this torrent (by info hash). */
  managed?: boolean;
  /** Which network it came over; a usenet job's hash is its "sab-…" key either way. */
  transport?: "torrent" | "usenet";
  /** A failed usenet job: SABnzbd's own reason ("Out of retention"). */
  fail_message?: string;
  /** What the client still has to fetch, when it says. */
  remaining_bytes?: number;
  /** The title the grab was for (absent for a torrent no grab knows). */
  item?: TransferItem;
}

/** The library title a transfer was grabbed for, from the acquisition record. */
export interface TransferItem {
  /** music_artist: a discography, grabbed for the artist (id is the artist's). */
  kind: "movie" | "series" | "book" | "music" | "music_artist";
  id: number;
  title: string;
  year?: number;
  poster_url?: string;
  byline?: string;
  /** What part of the title the torrent covers: "S03", "S03E05", "Audiobook", a version's label. */
  scope?: string;
  /** The grab replaces a file the title already has. */
  upgrade?: boolean;
  /** Who is waiting on it (open requests' owners and followers). */
  requested_by?: string[];
}

// What the disk guard is holding; present only while it holds something.
export interface DiskGuardHold {
  holding: number;
  used_pct: number;
  pause_pct: number;
  resume_pct: number;
}

export type RemoveDownloadMode = "keep_files" | "delete_files" | "block";
// What a removed download was for, and what "stop wanting" switched off.
export type BlockType = "movie" | "series" | "book" | "music" | "global";

// One blocklist entry of any kind (the Blocklist page).
export interface BlocklistRow {
  id: number;
  type: BlockType;
  item_id: number; // 0 for global; the artist for a music discography
  item_title: string; // "" when the item has since been deleted
  discography?: boolean;
  title: string; // the release
  indexer?: string;
  reason?: string;
  created_at: string;
  /** The network the release was on, when it's known. */
  network?: "torrent" | "usenet";
}

// The library item a Block blocklisted a release for.
export interface BlockTarget {
  kind: "movie" | "series" | "book" | "music";
  id: number;
  title: string;
}

export interface RemoveDownloadResult {
  kind?: string;
  id?: number;
  title?: string;
  mode: RemoveDownloadMode;
  unmonitored?: string;
}

export interface ActivityFeed {
  downloads: ActivityDownload[];
  totals?: { down_speed: number; up_speed: number; active: number; stalled?: number };
  /** null (or absent) when the downloads folder can't be measured. */
  free_gb?: number | null;
  /** The downloads folder the free figure is for. */
  disk_path?: string;
  /** Whether there is a download client and whether it answered; absent when unreadable. */
  clients?: DownloadClientsState;
  disk_guard?: DiskGuardHold;
}

/** The Downloads feed's view of the download clients. ok is false when the last queue
 *  read failed or missed a client; name/error/since then say which and since when. */
export interface DownloadClientsState {
  configured: number;
  enabled: number;
  ok: boolean;
  name?: string;
  error?: string;
  since?: string;
}

/** Whether downloads could be checked just now. When not, a title's status is unknown. */
export interface QueueHealth {
  ok: boolean;
}

// Resume's answer. "all" also reports what it resumed and what it left for the disk guard.
export interface ResumeResult {
  status: string;
  resumed?: number;
  held_by_guard?: number;
}

export interface APIKeyStatus {
  id: string;
  label: string;
  purpose: string;
  help_url: string;
  steps: string;
  secret: boolean;
  testable?: boolean;
  /** Can be tested with a typed value before it's saved (sent once, never stored). */
  tests_candidate?: boolean;
  configured: boolean;
  source: "settings" | "env" | "";
  hint?: string;
  // The install-time value, reported even when a saved one wins: what Clear falls back to.
  env_set: boolean;
  env_hint?: string;
  /** The provider's last complaint about the key in use (OMDb's "Request limit reached!") and when. */
  last_error?: string;
  last_error_at?: string;
}

export interface ClientSettings {
  dl_limit: number;
  up_limit: number;
  alt_dl_limit: number;
  alt_up_limit: number;
  schedule_enabled: boolean;
  from_hour: number;
  from_min: number;
  to_hour: number;
  to_min: number;
  days: number;
  max_active_downloads: number;
  max_active_uploads: number;
}

export interface FormatInfo {
  name: string;
  description: string;
  group: string; // hdr | audio | codec
  target?: boolean; // set through the target file; the rest are advanced scores
}

export interface QualityCondition {
  type: string;
  value: string;
  negate?: boolean;
}

export interface QualityCustomFormat {
  name: string;
  match?: "all" | "any"; // how the conditions combine; missing reads as "all"
  conditions: QualityCondition[];
}

export interface StoredProfile {
  id: number;
  media_type: string;
  name: string;
  base?: string;
  allowed_resolutions: string[];
  min_source: string;
  max_source: string;
  bitrate_cap_mbps: number;
  small_bias: number;
  min_format_score: number;
  format_scores: Record<string, number>;
  required_formats?: string[]; // formats a release must have; anything without them is rejected
  custom_formats?: QualityCustomFormat[];
  keywords?: { term: string; score: number }[];
  rejected?: string[];
  min_seeders: number;
  stall_minutes: number;
  upgrades_enabled: boolean;
  upgrade_min_percent: number;
  // Which gains replace a file ("Replace for"): any | source | format | resolution. The
  // server reads a missing one as "any".
  upgrade_trigger?: UpgradeTrigger;
  allow_prerelease?: boolean; // grab cams/telesyncs/screeners (movie & series; off = refused)
  // A MULTi/Dual release naming no language counts as maybe having a Must audio language.
  // Off (the default): a Must has to be named in the release, or be the original language.
  multi_counts_language?: boolean;
  ideal?: IdealFile; // the target file: drives grabbing, ranking and the library fit check
  unwanted?: UnwantedSettings; // the Unwanted pack's choices (movie & series)
  group_tiers?: GroupTiers; // release-group tiers (movie & series); missing = the built-in suggestions
}

// Release-group tiers (quality/grouptiers.go): order releases under ranking v2, never reject.
export interface GroupTiers { tier1: string[]; tier2: string[] }

// The Unwanted pack (quality/unwanted.go): each item refused, avoided or allowed per profile.
export type UnwantedAction = "reject" | "avoid" | "allow";
export interface UnwantedSettings {
  items?: Record<string, UnwantedAction>; // a missing item is its default
  low_quality_groups: string[] | null; // null = the default list
}
export interface UnwantedItemInfo { key: string; label: string; description: string; default: UnwantedAction }

// An Evaluation's rank keys (quality/rankkeys.go): what ranking v2 compares, key by key.
// Declared apart and merged into Evaluation, for the Advanced raw view and row explanations.
export interface Evaluation {
  target_avoided?: boolean; // a built-in format Avoid (target or Other formats), the bitrate floor or an Unwanted avoid
  custom_avoided?: boolean; // a negative custom format or keyword score
  res_tier?: number; // 2160p 5 … 480p 1
  pref_count?: number; // the target's preferred/required formats it has (0 when waived)
  custom_score?: number;
  source_tier?: number; // Remux 6, BluRay 5, WEB 4, HDTV 2, DVD 1, unstated 0, cam -1
  proper_fix?: boolean;
  group_tier?: number;
  eff_bitrate?: number; // H.264-equivalent Mb/s, 0 without a runtime
  in_window?: boolean;
}

// RankKey names a ranking step (quality.RankKey); "only" = the one eligible release.
// "network" = the preferred network (Settings → Downloads → When a release is on both
// Usenet and torrents) decided between releases that otherwise score the same.
export type RankKey = "avoid" | "resolution" | "preferences" | "custom" | "source" | "proper" | "group" | "bitrate" | "network" | "health" | "size" | "only" | "tie";

// Decision fields from ranking v2 (shadow mode): merged into Decision.
export interface Decision {
  ranking?: "v1" | "v2"; // the mode that ordered it
  // The other mode's pick: classic's while v2 (the default) decides, v2's while classic does.
  shadow?: { mode?: "v1" | "v2"; winner: string; decided_by: RankKey; differs: boolean; over_key?: RankKey; v1_key?: RankKey };
}

export type UpgradeTrigger = "any" | "source" | "format" | "resolution";

// ProfileImpact mirrors quality.Impact: the files an edit would make eligible for
// replacement — no longer meeting the profile (replace), or searched again (search).
export interface ImpactBucket { files: number; bytes: number; examples: string[] }
export interface ProfileImpact { replace: ImpactBucket; search: ImpactBucket; files: number }

// TargetPref is one option's state in the target file. "" = no opinion; "want" is shown as
// Prefer (ranks releases, doesn't decide what fits). "ok" is retired — the server drops it.
export type TargetPref = "" | "ok" | "want" | "must" | "avoid";
// IdealFile mirrors quality.IdealFile: every part optional.
export interface IdealFile {
  codec?: Record<string, TargetPref>; // "hevc" | "av1" | "h264"
  hdr?: Record<string, TargetPref>; // "SDR" | "HDR10" | "HDR10+" | "HLG" | "DV"
  audio?: Record<string, TargetPref>; // "atmos" | "lossless"
  bitrate?: Record<string, { min: number; max: number }>; // per "2160p" | "1080p" | "720p" | "SD", Mb/s
  // Audio languages: base ISO 639-1 codes or "original" (the title's own) → must/want/avoid.
  language?: Record<string, TargetPref>;
}
// AudioLang is one audio language a library file carries (the detail pages' badges):
// code "" is a track nothing identified; detected = heard by the AI on an untagged track.
export interface AudioLang { code: string; name?: string; detected?: boolean }
// AudioLangFit is a file's languages against its profile's Audio language rules.
export interface AudioLangFit { status: "good" | "partial" | "bad"; note: string }
export interface FitCounts { titles: number; files: number; fits: number; over: number; under: number; mismatch: number }
export interface MusicPreset { name: string; description: string; format_scores: Record<string, number>; min_format_score: number; upgrades_enabled: boolean }
export interface FileFacts { resolution: string; codec: string; hdr: string; dolby_vision?: boolean; atmos?: boolean; lossless?: boolean; bitrate_mbps: number }
export type FitStatus = "fits" | "over" | "under" | "mismatch";
export interface FileFit { status: FitStatus; window?: { min: number; max: number }; issues?: { kind: string; msg: string }[] }
export interface FitItem { movie_id?: number; series_id?: number; season: number; episode?: number; facts: FileFacts; fit?: FileFit; profile?: string }
export interface SeriesFitSummary { series_id: number; checked: number; fits: number; over: number; under: number; mismatch: number }

export interface DownloadClient {
  id: number;
  name: string;
  kind: string;
  url: string;
  username?: string;
  enabled: boolean;
  /** Place in the order new downloads try (1 = first); the next is tried only when this one can't be reached. */
  priority: number;
  /** A packaged client (qBittorrent, or the optional SABnzbd). Deleted, it stays deleted until restored. */
  bundled?: boolean;
  /** A bundled client's own web page's host port: the bundled SABnzbd's (where the Usenet provider is added), or the bundled qBittorrent's. */
  ui_port?: string;
  /** The bundled qBittorrent (all-in-one image or Compose): Arrmada made its web UI login, and an admin can look it up. */
  has_login?: boolean;
  /** The all-in-one image's qBittorrent, running inside Arrmada's own container. */
  built_in?: boolean;
  /** A password (SABnzbd: API key) is saved; its value never reaches the browser. */
  auth_saved?: boolean;
  /** Its own page is published on the server's loopback only (ARRMADA_QBIT_WEBUI_BIND), so another computer can't open ui_port. */
  ui_local?: boolean;
  /** How it has been answering; absent when nothing is known yet. */
  status?: DownloadClientStatus;
  /** Remote → local folder mappings for a client that sees its downloads under another path. */
  path_maps?: PathMap[];
}

/** One remote path mapping: the folder as the download client names it, and inside Arrmada. */
export interface PathMap {
  remote: string;
  local: string;
}

export interface DownloadClientStatus {
  state: "ok" | "failing" | "backing_off" | "unknown";
  failing_since?: string;
  last_error?: string;
  last_error_at?: string;
}

/** The categories Arrmada files downloads under; not a per-client setting. */
export interface DownloadCategories {
  movies: string;
  tv: string;
  books: string;
  music: string;
}

export interface DownloadClientList {
  clients: DownloadClient[];
  categories?: DownloadCategories;
  /** This install has a bundled qBittorrent and its row was deleted. */
  can_restore_bundled?: boolean;
  /** This install runs the optional bundled SABnzbd and its row was deleted. */
  can_restore_bundled_sabnzbd?: boolean;
}

export interface NewDownloadClient {
  name: string;
  kind: string;
  url: string;
  username?: string;
  /** On an edit, blank keeps the stored password (it is never sent back). */
  password?: string;
  enabled?: boolean;
  /** 1..99; left out, a new client gets 25 and an edited one keeps its own. */
  priority?: number;
  /** Replaces the path mappings; left out, an edited client keeps its own. */
  path_maps?: PathMap[];
}

// An alert connection as the server shows it. The saved Apprise URL never comes back:
// url_hint stands in for it ("discord://••••OKEN", "ntfys://ntfy.example.com/••••").
export interface NotificationConn {
  id?: number;
  name: string;
  kind: string; // free-form label / service hint
  /** Catalog keys this connection subscribes to (GET /notifications/catalog). */
  events: string[];
  enabled: boolean;
  /** For kind "webpush" ("This device"): the account whose devices get the pushes. */
  config?: { user_id?: number };
  url_hint?: string;
  url_set?: boolean;
  /** The form it's edited with: a preset kind, "webpush", or "custom" (a raw Apprise URL). */
  preset?: string;
  /** A preset's saved form values: each secret is "" with `<name>_set: true` beside it. */
  fields?: Record<string, string | boolean>;
  /** A URL saved before validation existed that no longer passes it (it still sends). */
  invalid_reason?: string;
  /** The latest delivery: "" never sent, "retrying" = the last try failed and another is due. */
  last_status?: "" | "sent" | "failed" | "retrying";
  last_error?: string;
  last_sent_at?: number;
  /** The admin's message choices: whose Plex streams, remote or transcodes only. */
  options?: AlertOptions;
}

/** A connection's message choices (none of them secret). */
export interface AlertOptions {
  /** Plex user ids whose streams alert; empty or absent = everyone. */
  stream_users?: string[];
  remote_only?: boolean;
  transcode_only?: boolean;
  /** This connection's own message per event key, with {placeholders}. */
  templates?: Record<string, string>;
}

// One row of a connection's delivery log.
export interface AlertDelivery { id: number; event_key: string; title: string; body: string; status: "queued" | "sending" | "sent" | "failed"; attempts: number; last_error: string; created_at: number; next_attempt_at: number; sent_at: number }

// What create, update and test take. On update, a missing or blank url keeps the saved one.
export type NotificationInput = Pick<NotificationConn, "name" | "kind" | "events" | "enabled"> & {
  url?: string;
  /** A preset's form values; the server builds the URL. A secret left "" keeps the saved one. */
  fields?: Record<string, string>;
  /** On a Test of an unsaved edit: the saved connection whose secrets fill the blanks. */
  id?: number;
  /** Left out keeps the saved choices. */
  options?: AlertOptions;
  /** On a Test: the catalog event whose realistic sample is sent ("" or absent = a plain test). */
  event?: string;
};

/** One input on an alert preset's form. options = a choice, the first being the default. */
export interface AlertPresetField {
  name: string; label: string; hint?: string; placeholder?: string; secret?: boolean; required?: boolean; options?: string[];
  /** The words shown for options, in the same order. */
  option_labels?: string[];
  /** Shown only while another field has a value: "auth=header". */
  show_when?: string;
  /** The page makes a random value (a signing secret) to copy before saving. */
  generate?: boolean;
}
/** A "Where should alerts go?" tile. */
export interface AlertPreset { kind: string; label: string; hint: string; fields: AlertPresetField[] }

// The alert event catalog: what a connection can subscribe to, grouped for the page.
export interface AlertEvent { key: string; group: string; label: string; hint: string; default_on: boolean; module?: "books" | "music" }
export interface AlertCatalog {
  groups: { key: string; label: string }[]; events: AlertEvent[];
  /** The message every event sends unless a connection has its own ("{message}\n{link}"). */
  template?: string;
  /** The placeholders a template may use. */
  placeholders?: string[];
  /** Whether alerts can link back to the app (an outside address is set). */
  links?: boolean;
}

export interface MyApprise { set: boolean; hint: string; blocked_reason?: string }

export interface UserNotification {
  id: number; title: string; body: string; media_type: string; ref: string; read: boolean; created_at: number;
  /** A 'ready' notice for a title the owner's Plex has: its app.plex.tv page (absent while Plex is off). */
  plex_url?: string;
  /** A 'ready' notice's Watch buttons: one per media server that has the title, the reader's own first. */
  watch_links?: WatchLink[];
}

export interface CalendarItem {
  date: string; type: "episode" | "movie" | "album"; title: string; subtitle: string; poster_url?: string; ref_id: number; has_file: boolean;
  // Searched for (an episode: it's wanted and its show's searching is on). searching_off: a
  // wanted episode of a show whose searching is off (an album: of an artist switched off).
  monitored: boolean; searching_off?: boolean;
  // The title's TMDB id and kind open its Discover page (APP-16); the episode's numbers and name.
  tmdb_id?: number; media_type?: "movie" | "series" | "music"; year?: number; season?: number; episode?: number; episode_title?: string;
  // An album's artist and kind (MUS-24). Albums are on staff's calendar only, while Music is on.
  artist?: string; album_type?: string;
  // The viewer asked for this title or follows a request for it (APP-17).
  requested_by_me?: boolean;
}

export interface LibraryPaths { movies: string; tv: string; ebooks: string; audiobooks: string; music: string; downloads: string }
// Audiobook server (listening apps like Lissen).
export interface AudioDevice { id: string; user_id: number; username: string; client: string; device: string; created_at: number; last_used_at: number }
export interface AudioConnection { enabled: boolean; running: boolean; error?: string; host_port: string; public_url: string }
export interface AudioServerAdmin extends AudioConnection {
  users: { id: number; username: string; role: UserRole; disabled: boolean; eligible: boolean; allowed: boolean; has_password: boolean }[];
  devices: AudioDevice[];
  items: number;
  items_ready: number;
  /** While an admin traces app requests: when it stops by itself (unix ms); 0 when off. */
  trace_until: number;
}
export interface AudioPlace { item_key: string; book_id: number; title: string; author?: string; cover_url?: string; position: number; duration: number; finished: boolean; updated_at: number; device?: string;
  /** A big jump back that's being held until it proves itself (or the person confirms it). */
  pending_position?: number | null; pending_at?: number;
  /** A later spot an app sent that wasn't used (e.g. offline listening uploaded after another device moved the place). */
  offer?: AudioOffer | null }
/** A place an app sent that wasn't used, offered back: restore it with history_id, or dismiss it. */
export interface AudioOffer { history_id: number; position: number; at: number; device?: string; reason: string }
/** A place an app removed ("discard progress"), restorable for 90 days. device is who removed it. */
export interface AudioRemoved { item_key: string; book_id: number; title: string; author?: string; cover_url?: string; position: number; duration: number; finished: boolean; discarded_at: number; device?: string }
export interface MyAudio extends AudioConnection { username: string; allowed: boolean; has_password: boolean; min_password_length: number; devices: AudioDevice[]; places: AudioPlace[]; removed: AudioRemoved[];
  /** What setting the password asks for: the account password, a two-factor code, or (no password to ask for) a recent sign-in. */
  password_required?: boolean; code_required?: boolean; reauth_required?: boolean }
/** What a timeline row is: the place moved (applied), the place just before a change (before), a place an app sent that
 *  wasn't used (rejected), a jump waiting for proof (held), an app removing the place (discarded), or a place put back (restored). */
export type AudioHistoryKind = "applied" | "before" | "rejected" | "held" | "discarded" | "restored";
export interface AudioHistoryEntry { id: number; position: number; at: number; device?: string; reason: string; kind: AudioHistoryKind; dismissed?: boolean }
// Arrmada's own listening API (/api/v1/me/audio/*, the web player). Every call is about
// the signed-in person; a 403 means audiobooks are switched off or the account isn't
// allowed ("message" says which).
/** A place in an audiobook as the listening API returns it. pending_position is a held jump. */
export interface AudioProgress { item_key: string; position: number; duration: number; finished: boolean; finished_at?: number; updated_at: number; device?: string; hidden?: boolean; pending_position?: number | null }
/** An audiobook as the web player lists it. cover is same-origin (cache-busted with ?v=); duration is 0 until its files are read. */
export interface AudioCard { key: string; book_id: number; version_id?: number; title: string; author?: string; series?: string; series_seq?: string; cover?: string; duration: number; added_at: number; progress: AudioProgress | null }
export interface AudioShelf { id: "continue-listening" | "continue-series" | "recently-added" | "listen-again" | string; label: string; items: AudioCard[] }
export interface AudioChapter { id: number; start: number; end: number; title: string }
/** One audio file in play order; start_offset is where it starts in the whole book; url streams it (Range works). */
export interface AudioTrack { ino: string; index: number; start_offset: number; duration: number; mime: string; url: string }
export interface AudioBookmark { item_key: string; time: number; title: string; created_at: number }
export interface AudioItemDetail extends AudioCard {
  description?: string; year?: number; genres: string[]; chapters: AudioChapter[]; tracks: AudioTrack[];
  /** Who reads it: typed on the Books page, else from the audio files' tags. */
  narrator?: string;
  /** Other audiobooks of the same book (full cast, another narrator). */
  versions: { key: string; title: string }[];
  bookmarks: AudioBookmark[];
}
export type AudioLibrarySort = "title" | "author" | "added" | "duration";
export type AudioLibraryFilter = "" | "in-progress" | "finished" | "not-started";
export interface AudioLibraryPage { items: AudioCard[]; total: number; page: number; limit: number }
/** A web-player session just opened: play from start_time. restart = a finished book starting again from 0:00. */
export interface AudioPlayStart { session_id: string; start_time: number; duration: number; tracks: AudioTrack[]; chapters: AudioChapter[]; restart: boolean }
/** A web-player report. Leave current_time out when the player doesn't know where it is — that never moves the place.
 *  time_listened is wall-clock seconds actually played since the last report. */
export interface AudioSyncReport { current_time?: number; time_listened: number; duration?: number }
/** The saved place after a sync or close. held_position is a big jump waiting for proof (null when none). */
export interface AudioSyncResult { position: number; held_position: number | null; finished: boolean; duration: number }
export interface AudioListening {
  days: number;
  since: string; // YYYY-MM-DD, first day covered
  daily: { user_id: number; username: string; day: string; seconds: number }[];
  totals: { user_id: number; username: string; today: number; week: number; month: number; all_time: number; last_listen: number }[];
  sessions: { user_id: number; username: string; device: string; client: string; started_at: number; ended_at: number; seconds: number }[];
}
export interface AudioImportPreview {
  users: { abs_id: string; abs_username: string; user_id: number; username?: string; progress_rows: number }[];
  progress: number; matched: number; bookmarks: number; unmatched_books: string[] | null;
}
export interface AudioImportResult { imported: number; kept: number; bookmarks: number; skipped: number }

// SetupState drives the first-run wizard: whether it's needed, the keys, the folders
// saved vs the ones the running app uses, and folders that look right on the mount.
export interface SetupState {
  needed: boolean;
  complete: boolean;
  tmdb_configured: boolean;
  libraries_chosen: boolean;
  keys: APIKeyStatus[];
  library: LibraryPaths;
  running: LibraryPaths;
  restart_needed: boolean;
  can_restart: boolean;
  mounts: string[];
  /** /media is a folder of the owner's (ARRMADA_MEDIA_HOST), not Arrmada's managed volume. */
  media_host_folder?: boolean;
  suggestions: Partial<LibraryPaths>;
  /** The Media server step: Plex is connected (address and token saved), and its name. */
  plex_connected?: boolean;
  plex_server?: string;
  /** Plex's family sign-in switches; auto_approve_types is "" until the owner has chosen. */
  plex_family?: { enabled: boolean; auto_approve_types: string };
  /** The same for Jellyfin (address and API key saved; Sign in with Jellyfin's switches). */
  jellyfin_connected?: boolean;
  jellyfin_server?: string;
  jellyfin_family?: { enabled: boolean; auto_approve_types: string };
  /** The Indexers step: how many indexers searches can use. */
  indexers_enabled?: number;
  /** Whether a Prowlarr answers at the saved address; null unless asked (?prowlarr=1). */
  prowlarr_available?: boolean | null;
}
// path_disabled: the folder on screen holds (or is) Arrmada's data folder, so it can be
// walked through but not selected.
export interface BrowseResult { path: string; parent: string; dirs: { name: string; path: string }[]; path_disabled?: boolean }
// ManualImportList is one manual-import listing. The server stops after 500 files (or 30
// seconds) so a library root can't walk the whole array: truncated says the list is cut
// short, and note says why when it ran out of time.
export interface ManualImportList<T> { path: string; candidates: T[]; truncated?: boolean; note?: string }

// importListNotice is the line a manual-import modal shows under a cut-short list.
export function importListNotice(r: { truncated?: boolean; note?: string }): string | null {
  if (r.note) return r.note;
  return r.truncated ? "Showing the first 500 files — pick a narrower folder." : null;
}

// PendingRestart: folders saved in the app that the running app isn't using yet (they
// apply at the next start), and what a restart would interrupt. Counts only, no titles.
export interface PendingRestart {
  restart_needed: boolean;
  can_restart: boolean;
  changed: { library: keyof LibraryPaths; saved: string; running: string }[];
  busy: { convert_running: number; convert_longest_sec: number; convert_progress: number; subtitles_running: number; subtitles_queued: number };
}
// FolderCheck is what a folder looks like before it's saved (Settings → Library, the
// wizard). hardlink_with_downloads is null when it couldn't be tried; error is the reason
// a save would refuse it, in the server's words.
export interface FolderCheck {
  path: string; exists: boolean; is_dir: boolean; writable: boolean;
  hardlink_with_downloads: boolean | null; under_data_dir: boolean;
  free_bytes: number; total_bytes: number; entries: number; entries_capped: boolean;
  error?: string;
}

// One problem a health check found. key is stable across runs; link_key names the LINKS
// entry (lib/links.ts) where it's fixed, and link is the same address from the server for
// keys this build doesn't know.
export interface HealthWarning {
  key?: string;
  check?: string;
  level: string; // "error" | "warning"
  message: string;
  link?: string;
  link_key?: string;
  link_label?: string;
  since?: string; // when it was first seen
}

// One background health check's latest outcome. level is "pending" before its first run;
// stale means its last run timed out and the findings are from before.
export interface HealthCheck {
  key: string;
  name: string;
  category: string; // "Storage" | "Downloads" | "Indexers" | "Integrations" | "Tasks"
  level: "ok" | "warning" | "error" | "pending";
  checked_at: string;
  duration_ms: number;
  stale: boolean;
  findings: HealthWarning[];
}

export interface SystemHealth {
  status: string; // "ok" | "warning" | "error"
  warnings: HealthWarning[];
  checks?: HealthCheck[];
  disk?: { free_gb: string; path: string };
}

// The Needs-you feed (GET /api/v1/attention, staff only): what's waiting on a person.
export type AttentionKind =
  | "request" | "review" | "download" | "stalled" | "import" | "wrongcat" | "search" | "health" | "convert" | "issue" | "subtitles";
export interface AttentionCounts {
  requests: number;
  reviews: number;
  downloads: number; // errored + stalled
  imports: number; // failing imports + wrong-category downloads
  searches: number; // titles whose searches keep coming up empty
  health: number;
  health_errors: number;
  convert?: number; // reasons Convert is stuck waiting for disk space
  issues?: number; // reported problems still open
  subtitles?: number; // file languages waiting on a login, a model or a whisper fix
  total: number;
}
export interface AttentionItem {
  key: string; // "<kind>:<id>", stable while the problem lasts
  kind: AttentionKind;
  level: "warning" | "error";
  title: string;
  detail?: string;
  link?: string;
  link_key?: string;
  link_label?: string;
  since: number; // unix ms
  count?: number; // an aggregate's size (search:stuck)
  /** The torrent a download, stalled, import or wrongcat item is about. */
  hash?: string;
  /** An Arrmada grab recorded that torrent (so another release can be tried). */
  grabbed?: boolean;
}
export interface AttentionGroup {
  kind: AttentionKind;
  level: "warning" | "error";
  count: number;
  title: string; // "3 requests are waiting for approval"
  link?: string;
  link_key?: string;
  sample?: string[];
}
export interface Attention {
  at: string | null;
  stale: boolean;
  counts: AttentionCounts;
  groups: AttentionGroup[];
  items: AttentionItem[];
}

export interface StorageVolume {
  roots: string[];
  path: string;
  total_bytes: number;
  free_bytes: number;
  used_bytes: number;
  used_pct: number;
}
export interface QueueSummary {
  downloading: number; stalled?: number; seeding: number; paused: number; errored: number;
  down_speed: number; up_speed: number;
}
export interface LibraryCounts {
  movies: number; movies_missing: number;
  series: number; episodes: number; episodes_missing: number;
  books: number; books_missing: number;
}
// DashboardMusic is Music's part of the Dashboard (MUS-24), sent only while Music is on.
// albums: albums with a track on disk; albums_wanted: incomplete wanted albums by the
// Wanted page's rules (downloading ones included), of which albums_missing are released
// (the rest are on its Upcoming tab).
export interface DashboardMusic {
  artists: number; albums: number; tracks: number; size_bytes: number;
  albums_wanted: number; albums_missing: number;
  /** The newest of the New releases list (last 30 days); new_releases_total is how many it holds. */
  new_releases: DashboardNewRelease[]; new_releases_total: number;
  /** The newest music imports (title is the artist). */
  imports: ActivityEvent[];
}
export interface DashboardNewRelease {
  id: number; title: string; artist_name: string; album_type?: string; release_date?: string; monitored: boolean; found_as?: string;
}
export interface ActivityEvent {
  kind: HistoryKind; // music is the artist
  id: number; title: string; event: string; detail: string; at_ms: number;
}
export interface DashboardData {
  storage: StorageVolume[];
  streams?: InsightsActivity;
  streams_note?: string;
  /** A Plex URL and token are set; without them streams_note stays empty. */
  plex_configured: boolean;
  /** A Jellyfin address and key are set: streams then include Jellyfin's, each with its server. */
  jellyfin_configured?: boolean;
  queue: QueueSummary;
  queue_note?: string;
  library: LibraryCounts;
  music?: DashboardMusic;
  activity: ActivityEvent[];
  listening?: NowListening[];
  audio_off?: boolean;
}

// NowListening is one live audiobook session. Others' sessions say who, on what and for
// how long — never which book; the book fields are only set on your own (mine).
export interface NowListening {
  user: string;
  device: string;
  client: string;
  started_at: number;
  last_at: number;
  seconds: number;
  playing: boolean;
  mine: boolean;
  book_id?: number;
  title?: string;
  author?: string;
  cover_url?: string;
  position?: number;
  duration?: number;
}

export interface DiskGuardVolume {
  path: string;
  measurable: boolean;
  used_pct: number;
  measured_path: string;
  override: boolean;
  total_bytes: number;
  free_bytes: number;
  zfs?: boolean;
}

export interface DiskGuardStatus {
  enabled: boolean;
  measurable: boolean;
  /** The downloads folder (the fullest one, with several). */
  path: string;
  used_pct: number;
  pause_pct: number;
  resume_pct: number;
  holding: number;
  /** The saved "Measure a different drive" path; "" = none. */
  measure_path: string;
  /** The path whose drive used_pct, total_bytes and free_bytes are read from. */
  measured_path: string;
  /** measured_path is the "Measure a different drive" path, not the downloads folder. */
  override: boolean;
  /** The saved measure_path can't be used now: the guard pauses and resumes nothing for it. */
  measure_unreadable: boolean;
  /** Set when that's because it reads as a RAM or container filesystem ("tmpfs", "overlay"…). */
  measure_not_disk?: string;
  total_bytes: number;
  free_bytes: number;
  /** measured_path is a ZFS pool: its percentage only counts part of it. */
  zfs?: boolean;
  /** The free-space limit in bytes (0 = off), and the free space needed to resume. */
  pause_free_bytes?: number;
  resume_free_bytes?: number;
  /** Each downloads folder, when there's more than one. */
  volumes?: DiskGuardVolume[];
  // The library folders (the ones picked in Settings → Library) on the same drive as
  // the one the guard reads. shared_with_library is just shared_with.length > 0.
  shared_with: { role: string; label: string; path: string }[];
  shared_with_library: boolean;
}

// source: "tmdb" for an alias seeded from TMDB's alternative titles (or a renamed show's old title),
// "user" for one the owner typed. Removing a TMDB one switches it off rather than deleting it.
export interface SeriesAlias { id: number; title: string; tmdb_season: number; source?: "user" | "tmdb" }

export interface AudioStreamInfo { aud_index: number; codec: string; lang: string; channels: number }
export interface SubStreamInfo { sub_index: number; codec: string; lang: string; text: boolean }
export interface FileMediaInfo {
  container: string; video_codec: string; width: number; height: number; resolution: string;
  hdr: string; dv_profile?: number; bitrate_kbps: number; frame_rate: number; duration_sec: number;
  size_bytes: number; audio_tracks: number; sub_tracks: number; ten_bit: boolean;
  interlaced?: boolean; vfr: boolean; has_cc: boolean;
  audio?: AudioStreamInfo[]; subs?: SubStreamInfo[];
}
export interface FileSource {
  release: string; indexer?: string; info_hash?: string; quality_profile?: string;
  grabbed_ms?: number; imported_ms?: number; source_path?: string;
  manual: boolean; seed_enabled: boolean; seed_ratio?: number; seed_hours?: number;
  from_pack: boolean; in_client: boolean; state?: string; ratio?: number;
}
export interface FileDetails {
  path: string; name: string; dir: string; exists: boolean;
  size_bytes: number; modified_ms: number; missing_reason?: string;
  media?: FileMediaInfo; media_note?: string;
  source?: FileSource; source_note?: string;
}

export interface SubSeriesGroup {
  series_id: number;
  title: string;
  year?: number;
  poster_url?: string;
  episodes: number;
  missing: number;
  covered: number;
  seasons: number;
  profile_id?: number; // the language profile the show is pinned to; absent = Default
}

export interface AppSettings {
  search_on_add: boolean;
  /** Indexers with no categories of their own are searched in the media type's usual ones. */
  indexer_default_categories: boolean;
  /** The monitoring preset a new series gets: "all" | "future" | "missing" | "existing" | "first_season" | "latest_season" | "none". */
  series_monitor_default: string;
  naming_movie_folder: string;
  naming_movie_file: string;
  naming_series_folder: string;
  naming_series_season: string;
  naming_series_episode: string;
  /** A new show folder keeps the series folder's {year} only when another show shares its title. Off by default. */
  naming_series_year_when_needed: boolean;
  /** Book naming: folder ("/" between levels), single file, and each file of a multi-file audiobook. */
  naming_audiobook_folder: string;
  naming_audiobook_file: string;
  naming_audiobook_track: string;
  naming_ebook_folder: string;
  naming_ebook_file: string;
  /** Music naming: the album folder ("/" between levels), a track's file, a compilation's track file. */
  naming_music_folder: string;
  naming_music_track: string;
  naming_music_compilation: string;
  /** Music: write the house tag style (into a copy; a seeding download is never written). Off keeps files as downloaded. */
  music_write_tags: boolean;
  /** Music: file types brought along beside an album (".jpg .pdf .lrc …"), space-separated. */
  music_extras: string;
  write_nfo: boolean;
  download_artwork: boolean;
  books_enabled: boolean;
  music_enabled: boolean;
  plex_login_enabled: boolean;
  /** Sign in with Plex may open an admin or manager account linked to that Plex account. */
  plex_signin_staff: boolean;
  /** Admins signing in with a password must use two-factor sign-in (set up at sign-in). */
  two_factor_admins: boolean;
  /** Convert EPUB to KEPUB for Kobo (Settings → Book server): the Kobo download and the
   *  KEPUB link in the OPDS feeds. On by default. */
  books_kepub: boolean;
  /** Legacy: true when a new Plex sign-in auto-approves every type. */
  plex_login_auto_approve: boolean;
  /** The types a new Plex sign-in auto-approves, "movie,series,book" (any subset). */
  plex_login_auto_approve_types: string;
  /** Sign in with Jellyfin (username + password). */
  jellyfin_login_enabled: boolean;
  /** Jellyfin sign-in and linking also offer Quick Connect. */
  jellyfin_quickconnect_enabled: boolean;
  /** Sign in with Jellyfin may open an admin or manager account linked to that user. */
  jellyfin_signin_staff: boolean;
  /** The types a new Jellyfin sign-in auto-approves, "movie,series,book" (any subset). */
  jellyfin_login_auto_approve_types: string;
  /** Request limits per person per window of days (0 = unlimited). */
  request_quota_days: number;
  request_quota_movies: number;
  request_quota_seasons: number;
  request_quota_books: number;
  /** Discovery region for TMDB lists (ISO 3166-1 alpha-2, e.g. "AU"); "" = global. */
  tmdb_region: string;
  // Recycle bin guard rails.
  recycle_max_gb: string;
  recycle_retention_days: string;
  downloads_disk_guard: boolean;
  downloads_disk_guard_pause_pct: string;
  downloads_disk_guard_resume_pct: string;
  /** "Measure a different drive": a path whose drive the guard reads instead of the downloads folder's; "" = the downloads folder. Admin only. */
  downloads_disk_guard_measure_path: string;
  /** "Also pause when free space drops below" in whole GB (1024³ bytes); "0" = off. Admin only. */
  downloads_disk_guard_pause_free_gb: string;
  /** Minutes with no progress before another release is tried; 0 = never. Default 360. */
  downloads_stall_minutes: number;
  /** How many upgrades one upgrade sweep may grab; 0 = no limit. Default 10. */
  upgrade_max_grabs_per_sweep: number;
  /** Languages automatic book grabs accept (canonical codes); [] = any. Default ["en"]. */
  books_languages: string[];
  /** Extra languages whose translated titles scans and imports match (canonical codes); [] = English and original titles only. */
  match_title_languages: string[];
  /** Settings → Library → "Show titles as": english (TMDB's, the default), original or
   *  language (display_title_language). */
  display_title_mode: "english" | "original" | "language";
  display_title_language: string;
  /** Works originally in these languages show their original title in any mode. */
  display_title_original_in: string[];
  /** How releases are ranked: "v2" ordered keys (default) or "v1" classic (the rollback). Admin-only. */
  quality_ranking?: "v1" | "v2";
  /** When a release is on both Usenet and torrents: the better release wins (best, the default), or the preferred network between equals. */
  download_network_preference?: NetworkPreference;
  /** The address Arrmada is reached at from outside ("https://arrmada.example.com"; "" = none). Admin-only. */
  app_public_url: string;
}

export interface TorrentPreview {
  name: string;
  size_bytes: number;
  files?: { path: string; size_bytes: number }[];
}
export interface LogEntry {
  time_ms: number;
  level: string; // DEBUG | INFO | WARN | ERROR
  msg: string;
  attrs?: string;
  /** Monotonic within one run of the server: the Live view's cursor. */
  seq?: number;
  /** The part of the app that wrote it (convert, indexer, …); absent for core lines. */
  module?: string;
}
// Why a database backup was taken; part of its file name.
export type BackupKind = "pre-migrate" | "nightly" | "manual" | "pre-restore" | "pre-delete-user" | "pre-delete-empty-user" | "pre-merge-user" | "uploaded" | "pre-insights-repair";

// One database backup file. Nothing from inside it is ever sent, beyond its schema version.
export interface BackupFile {
  backend?: "sqlite" | "postgres";
  name: string;
  kind: BackupKind;
  size_bytes: number;
  created_at: string;
  schema_version: string;
}

export interface BackupSchedule {
  enabled: boolean;
  hour: number; // local hour the nightly is due from, 0-23
  keep_nightly: number;
}

export interface BackupsState {
  backend?: "sqlite" | "postgres";
  can_restore?: boolean;
  can_upload?: boolean;
  backups: BackupFile[];
  total_bytes: number;
  free_bytes: number | null; // null where free space can't be measured
  dir: string;
  settings: BackupSchedule;
  last_nightly_at: string | null;
  can_restart: boolean; // the app can restart itself (inside Docker)
  pending_restore: { name: string; requested_by: string; at: string } | null; // staged, runs at the next start
  last_restore: RestoreResult | null;
}

// How the last restore at boot went.
export interface RestoreResult {
  at: string;
  ok: boolean;
  from?: string;
  pre_restore?: string; // the copy of the database it replaced
  error?: string;
}

export interface RestoreStaged {
  staged: boolean;
  restarting: boolean; // false: restart by hand (manual_command), or cancel
  manual_command: string;
  schema_version: string;
}

// What a POST that starts background work adds to its answer: the job that is doing it
// (GET /api/v1/jobs/{id}), and whether that job was already running from an earlier click.
export interface JobRef {
  job_id?: number;
  existing?: boolean;
}

// GET /insights/import/overlaps: imported plays that repeat a live-recorded play, and
// when live recording began (epoch seconds, 0 = never).
export interface ImportOverlaps {
  count: number;
  first_live_at: number;
}

// The saved Tautulli connection: the key itself is never sent back.
export interface TautulliConfig {
  url: string;
  api_key_set: boolean;
}

// One Tautulli import (GET /insights/import/runs, newest first). Times are epoch seconds;
// rows is how many of its plays are in the history now (what "Remove this import" deletes).
export type ImportRunStatus = "running" | "done" | "failed" | "timeout" | "interrupted";
export interface ImportRun {
  id: number;
  job_id: number;
  source: string;
  started_at: number;
  finished_at: number;
  status: ImportRunStatus;
  total: number;
  processed: number;
  imported: number;
  duplicates: number;
  overlaps: number;
  invalid: number;
  after_cutoff: number;
  failed: number;
  error: string;
  cutoff_at: number;
  rows: number;
  removed_at: number;
  removed_rows: number;
}

// A movie search goes through the movie search queue (two at a time): the answer says
// it was queued and its place among the searches waiting (1 = next; 0 = running now).
export interface MovieQueuedRef extends JobRef {
  queued?: boolean;
  position?: number;
}

// The movie search queue right now, oldest first (GET /api/v1/movies/search-queue).
export interface MovieSearchQueue {
  running: { id: number; title: string; kind: string }[];
  queued: { id: number; title: string; kind: string }[];
}

// The counts every movie.search.queued / .started / .done event carries.
export interface MovieSearchQueueEvent {
  id: number;
  kind: string;
  running: number;
  depth: number;
  position?: number;
}

export type JobStatus = "queued" | "running" | "succeeded" | "failed" | "cancelled" | "panicked" | "interrupted";

// A background job (staff only): a search, scan, import or Run now, with how it ended.
// message is a plain sentence for a toast ("Grabbed …", "Added 3 movies").
export interface Job {
  id: number;
  kind: string;
  target: string;
  trigger: string;
  status: JobStatus;
  progress: number; // 0..1
  message: string;
  error: string;
  result?: unknown;
  created_at: string | null;
  started_at: string | null;
  finished_at: string | null;
}

// What a search job's result holds.
export interface SearchOutcome {
  searched: boolean;
  returned: number;
  matching: number;
  usable: number;
  grabbed: number;
  grabbed_titles?: string[];
  reason:
    | "nothing-wanted" | "no-releases" | "none-for-this-title" | "all-blocklisted-or-below-profile" | "grabbed"
    | "already-searching" | "indexers-paused" | "indexers-failed" | "no-indexers" | "already-downloading";
  // Where the releases went (ACQ-15): counts over the distinct releases seen, reasons by code.
  wrong_title?: number;
  blocklisted?: number;
  pending?: number;
  out_of_scope?: number;
  rejected?: number;
  eligible?: number;
  reasons?: Record<string, number>;
  top_reason?: string;
  example?: string;
  indexer_errors?: Record<string, string>;
  attempt_id?: number;
}

// One stored search attempt (GET /api/v1/searches): what a title search found and why
// nothing was taken. started_at is unix ms.
export interface SearchAttempt {
  id: number;
  media_type: "movie" | "series" | "book" | "music";
  media_id: number;
  scope: string;
  trigger: string;
  started_at: number;
  duration_ms: number;
  returned: number;
  wrong_title: number;
  blocklisted: number;
  pending: number;
  out_of_scope: number;
  rejected: number;
  eligible: number;
  grabbed: number;
  reasons: Record<string, number>;
  top_reason: string;
  example: string;
  grabbed_titles: string[];
  indexer_errors: Record<string, string>;
  outcome: "grabbed" | "nothing_found" | "none_suitable" | "indexers_failed" | "skipped_in_flight" | "error";
  reason: string;
  error?: string;
}

// A title's searches in brief: the latest attempt, how many in a row since its last grab
// found nothing usable, and the reason most often on top over those.
export interface AttemptSummary {
  latest: SearchAttempt;
  empty_tries: number;
  main_reason?: string;
}

// One row of Recent jobs (GET /api/v1/system/jobs): the work in words, what it was for
// (target_path opens it), who started it, and how it went.
export interface RecentJob {
  id: number;
  kind: string;
  label: string;
  status: JobStatus;
  message?: string;
  error?: string;
  target: string;
  target_label?: string;
  target_path?: string;
  trigger_label: string;
  trigger_path?: string;
  created_at: string | null;
  started_at: string | null;
  finished_at: string | null;
  duration_ms: number;
}

// One recurring task as GET /api/v1/system/tasks reports it. Times are ISO strings, null
// until they happen; last_error is empty once a run succeeds.
export interface TaskStatus {
  name: string;
  label: string;
  description: string;
  interval_seconds: number;
  running: boolean;
  last_start: string | null;
  last_end: string | null;
  last_duration_ms: number;
  last_status: "" | "ok" | "failed" | "panicked";
  last_ok: boolean;
  last_error: string;
  last_error_at: string | null;
  runs: number;
  failures: number;
  consecutive_failures: number;
  skipped: number;
  next_run: string | null;
  job_id?: number; // the Run now in progress
}

// The download link for a backup (a .db.gz streamed by the server, admin only).
export const backupDownloadURL = (name: string) => `/api/v1/system/backups/${encodeURIComponent(name)}/download`;

// uploadBackup sends a .db or .db.gz to become an "Uploaded" backup. It uses XHR rather
// than fetch for the upload progress (0..1) a multi-gigabyte file needs.
export function uploadBackup(file: File, onProgress?: (fraction: number) => void): Promise<BackupFile> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", "/api/v1/system/backups/upload");
    xhr.setRequestHeader("Accept", "application/json");
    xhr.upload.onprogress = (e) => { if (e.lengthComputable && onProgress) onProgress(e.loaded / e.total); };
    xhr.onload = () => {
      let body: Record<string, unknown> | undefined;
      try { body = JSON.parse(xhr.responseText) as Record<string, unknown>; } catch { /* non-JSON answer */ }
      if (xhr.status >= 200 && xhr.status < 300 && body) { resolve(body as unknown as BackupFile); return; }
      const msg = typeof body?.message === "string" && body.message ? body.message
        : xhr.status === 413 ? "The file is too large to upload here." : `HTTP ${xhr.status}`;
      reject(new ApiError(msg, xhr.status, body));
    };
    xhr.onerror = () => reject(new Error("The upload was cut off. Behind Cloudflare, uploads over 100 MB fail; use Arrmada's LAN address."));
    const form = new FormData();
    form.append("file", file);
    xhr.send(form);
  });
}

// One recycle bin: there's one inside each library folder (so a delete is a rename on the
// same drive), plus the old shared bin while it still holds files.
export interface RecycleBinStats {
  key: string;
  dir: string;
  label: string;
  legacy: boolean;
  files: number;
  bytes: number;
  free_bytes: number;
  free_known: boolean;
  other_drive: boolean; // not on the drive of the library it serves: every delete is a copy
}
// The counts and sizes are totals across every bin; the size cap applies to the total.
export interface RecycleStats {
  enabled: boolean;
  dir: string;
  bins: RecycleBinStats[];
  files: number;
  bytes: number;
  oldest_unix?: number;
  max_gb: number;
  retention_days: number;
  over_cap_bytes: number; // how far over the size cap (0 = under, or no cap)
  protected_bytes: number; // held from the cap: deleted in the last 3 days, plus the newest item
  largest_item_bytes: number;
  protected_until?: number; // unix: when the last recent item becomes purgeable
}
// Where deleted files go right now — the cheap answer every delete dialog words itself from.
export interface RecycleMode {
  enabled: boolean;
  dirs: string[];
  retention_days: number;
  max_gb: number;
}
// What deleting something with its files would move, and where it goes.
export interface DeletePreview {
  files: number;
  sidecars: number;
  bytes: number;
  confirm_over_bytes: number; // above this, the title must be typed
  recycle: RecycleMode;
}
// One track's planned rename (MOV-16). status ok: it moves to proposed; same: already
// named right; conflict: something else has that name (reason says what), so it stays put;
// missing: the recorded file isn't on disk.
export interface MovieRenameItem {
  media: "movie";
  movie_id: number;
  version_id: number; // 0 = the default track
  title: string;
  year?: number;
  label: string;
  current: string;
  proposed: string;
  status: "ok" | "same" | "conflict" | "missing";
  reason?: string;
  // Everything else that moves with the file (an ok item only): its subtitles and, when it
  // leaves the movie's own folder, that folder's metadata and artwork. Nothing else moves.
  extras?: { kind: "subtitle" | "folder"; from: string; to: string }[];
  kept?: string; // why the old folder's other files stay where they are
}
// What applying one item did.
export interface MovieRenameResult {
  movie_id: number;
  version_id: number;
  label: string;
  from: string;
  to: string;
  renamed: boolean;
  reason?: string;
}
export interface MovieRenameReply {
  renamed: number;
  skipped: number;
  results: MovieRenameResult[];
}
// Organize's dry run (MOV-17): counts for every track, then a page of the ones that would
// move and the conflicts; token names exactly this plan for the apply.
export interface OrganizePreview {
  counts: { rename: number; same: number; conflict: number; missing: number };
  items: MovieRenameItem[];
  total: number; // items to list (renames + conflicts) across every page
  offset: number;
  token: string;
}
// The mass editor's one update (MOV-19): what changed and what it queued. exceeds_profile
// lists films whose file the profile no longer accepts — left alone, with "Download smaller
// versions" on offer (smaller: above a ceiling; otherwise a different kind of release).
export interface MovieBulkUpdate {
  ids: number[];
  monitored?: boolean;
  quality_profile?: string;
  min_availability?: string;
  search?: "none" | "missing" | "missing_and_upgrade";
}
export interface MovieBulkUpdateReply {
  updated: number;
  queued: number;
  duplicates: number;
  not_monitored: number;
  exceeds_profile: { id: number; title: string; size_bytes?: number; reason?: string; smaller: boolean }[];
  errors: { id: number; msg: string }[];
}
export interface MovieBulkDeleteReply {
  deleted: number;
  failed: number;
  results: { id: number; deleted: boolean; error?: string; moved?: string[]; failed?: string[] }[];
}
// The organize job's result.
export interface OrganizeSummary {
  total: number;
  renamed: number;
  skipped: number;
}
// What deleting a movie with its files would move (each file still on disk), and where.
export interface MovieDeletePreview {
  versions: { id: number; label: string; file_name: string; size_bytes: number }[];
  sidecars: number;
  // Artwork and metadata in the movie's own folder that go too (name is relative to it).
  // The delete is sent back their paths: only the ones shown here can go.
  extras?: { folder: string; name: string; path: string; size_bytes: number }[];
  // Each folder holding its files: removed, or why its other files stay.
  folders?: { name: string; path: string; removed: boolean; kept?: string }[];
  bytes: number;
  recycle: RecycleMode;
  // Downloads still in flight for it; progress 0..1 (0 when the client doesn't list it).
  pending_downloads: { hash: string; title: string; progress: number }[];
}
export interface RecycleItem {
  id: string;
  name: string;
  bin: string; // the bin's key
  bin_label: string;
  legacy: boolean;
  orig_path?: string;
  size_bytes: number;
  deleted_unix: number;
  restorable: boolean;
  expires_at: number; // unix: when retention deletes it for good (0 = retention off)
}

export interface MediaRequest {
  id: number;
  media_type: "movie" | "series" | "book";
  tmdb_id: number;
  ol_key?: string;
  author?: string;
  /** Books: read (ebook), listen (audiobook) or both. Absent on a request made before
   *  the choice existed. */
  formats?: "ebook" | "audiobook" | "both";
  title: string;
  /** The title as the library shows it, when the movie or show is there under another name. */
  display_title?: string;
  year: number;
  poster_url?: string;
  overview?: string;
  status: "pending" | "approved" | "declined";
  quality_profile?: string;
  requested_by: number;
  requested_by_name?: string;
  note?: string;
  seasons?: number[]; // series: the seasons asked for, ascending; absent = the whole show
  available: boolean;
  download_progress?: number; // 0..1 while the requested item is downloading
  tracking?: RequestTracking;
  created_at: string;
  updated_at: string;
  /** When the requester was told it's ready (unix seconds); 0 until then. */
  ready_at?: number;
  /** How the viewer stands to it in their own list: they asked, or they follow it. */
  relation?: "owner" | "subscriber";
  /** Staff only: the library item it became (movie, series or book id). */
  library_id?: number;
  /** Staff only, on GET /requests/{id}: who else follows it. */
  followers?: { name: string }[];
  /** What staff told the requester when declining; a re-request keeps the previous one. */
  decline_reason?: string;
  /** Staff only: who made the last decision (absent for an auto-approval). */
  decided_by_name?: string;
  /** When the last decision was made (unix seconds); absent while undecided. */
  decided_at?: number;
  /** How often it was asked for again after a decline; > 0 flags a re-request. */
  rerequest?: number;
  /** Delivered and in the owner's Plex: the title's app.plex.tv page. */
  plex_url?: string;
  /** Delivered: a Watch link per media server that has it, the viewer's own server first. */
  watch_links?: WatchLink[];
  /** Staff only, on a request waiting for approval: the release approving it would grab. */
  preview?: RequestPreview;
  /** On an approval's answer: whether it grabs the previewed release or searches. */
  approve_grab?: { mode: "preview" | "search"; reason?: "expired" | "profile" | "seasons" | "none" | "already queued" };
}

/** A request's release preview (staff only): what a search would grab, checked in advance. */
export interface RequestPreview {
  state: "queued" | "checking" | "done" | "busy";
  /** Unix seconds of the last finished check; 0 = never. */
  checked_at: number;
  /** The quality profile it was ranked under. */
  profile: string;
  seasons?: number[];
  /** Never checked, or more than six hours ago: opening the request checks again. */
  stale: boolean;
  /** "Approve & grab this" would take exactly these releases. */
  grabbable: boolean;
  result?: PreviewResult;
}

export interface PreviewResult {
  outcome: "found" | "none" | "unavailable" | "busy" | "not_wanted";
  reason: string;
  profile: string;
  releases: PreviewRelease[];
  why?: string[];
  chosen_over?: string;
  returned: number;
  matching: number;
  eligible: number;
  top_reasons?: { code: string; label: string; count: number }[];
  indexer_issues?: IndexerIssue[];
  searched: number;
  queries: number;
  season?: number;
  seasons_note?: string;
  /** The title is in the library already: ranked under its own profile, whatever the approval picks. */
  in_library?: boolean;
  /** A caveat about this preview ("Preview may miss anime not yet in the library"). */
  note?: string;
}

export interface PreviewRelease {
  title: string;
  indexer: string;
  summary: string;
  resolution?: string;
  source?: string;
  codec?: string;
  hdr?: string[];
  size_gb: number;
  seeders: number;
  transport?: "torrent" | "usenet";
  kind?: "complete series" | "pack" | "episode" | "ebook" | "audiobook";
  covers?: string;
  format?: string;
  matched?: string[];
  avoided?: string[];
}

/** A section of the request list (GET /api/v1/requests?section=). */
export type RequestSection = "needs_approval" | "in_progress" | "ready" | "declined" | "all" | "strip";

/** How many requests each section holds, for the viewer's scope and filters. */
export interface RequestCounts {
  needs_approval: number;
  in_progress: number;
  ready: number;
  declined: number;
}

export interface RequestList {
  requests: MediaRequest[];
  counts?: RequestCounts;
  total?: number;
  auto_approve: boolean;
  /** The viewer's own per-type auto-approve, "movie,series,book" (any subset). */
  auto_approve_types?: string;
  client_health?: QueueHealth;
}

// RequestTracking is where a request has got to, from its own downloads.
export type RequestStage =
  | "pending" | "declined" | "searching" | "queued" | "downloading" | "paused"
  | "failed" | "importing" | "partial" | "adding" | "available"; // adding: on disk, waiting for a media server
export interface RequestTracking {
  stage: RequestStage;
  progress?: number; // 0..1 across its active downloads
  eta_seconds?: number;
  speed_bps?: number;
  size_bytes?: number;
  downloads?: number;
  have?: number; // series: episodes on disk
  total?: number; // series: aired episodes wanted
  note?: string;
  /** Every download for it is stalled (the staff queue's rule): the card says Stalled. */
  stalled?: boolean;
  /** Books not found yet: when the next search is due (RFC3339; format in the viewer's locale). */
  next_check_at?: string;
  /** A book not out yet: the day it is (YYYY-MM-DD, a calendar day) — "Out 12 Mar". */
  release_date?: string;
  /** While searching: when it was last looked for (RFC3339), and how many looks in a row found nothing. */
  last_search_at?: string;
  misses?: number;
  /** A book the searches have slowed to a monthly check. */
  search_stopped?: boolean;
  /** Stage "adding": the media servers it's being added to ("Adding to Plex…"; both = "your library"). */
  adding_to?: MediaServerKind[];
}

// MyBook is one library book as a requester sees it: an ebook to download, an
// audiobook that lives in Audiobookshelf, or both.
export interface MyBook {
  book_id: number;
  title: string;
  author?: string;
  year?: number;
  cover_url?: string;
  added_at?: string;
  series?: string;
  /** The book sheet and series rows: the blurb (capped by the server), the series and the
   *  place in it apart (0 = number unknown), and up to three subjects. */
  description?: string;
  series_name?: string;
  series_position?: number;
  subjects?: string[];
  /** kindle: Send to Kindle's email takes this format (EPUB, PDF). kepub: "Download for
   *  Kobo (KEPUB)" is offered (an EPUB, with the server's conversion on). */
  ebook?: { format: string; size_bytes: number; kindle?: boolean; kepub?: boolean };
  audiobook: boolean;
  mine: boolean; // the signed-in user requested it
  /** Downloadable audiobooks: the standard one (version_id 0) and any extra versions.
   *  item_key is the same audiobook in the listening API (what Listen plays). */
  audiobooks?: { version_id: number; item_key?: string; label?: string; format: string; size_bytes: number; files: number }[];
}

// MyRequest is one of the signed-in user's book requests that hasn't produced a file.
export interface MyRequest {
  id?: number;
  title: string;
  author?: string;
  year?: number;
  cover_url?: string;
  status: "pending" | "approved" | "declined";
  requested_at: string;
  /** What was asked for (absent on an older request), and which of it is still on its way. */
  formats?: "ebook" | "audiobook" | "both";
  waiting?: "ebook" | "audiobook" | "both";
  /** Where it has got to; absent when the download client couldn't be read. */
  stage?: RequestStage;
  note?: string; // "Not found yet"
  next_check_at?: string; // RFC3339
  /** The whole of where it's got to, as the Requests page reads it (lib/requestStage). */
  tracking?: RequestTracking;
  decline_reason?: string;
  release_date?: string; // the book isn't out yet: the day it is (YYYY-MM-DD)
}

export interface DiscoverCard {
  media_type: "movie" | "series";
  tmdb_id: number;
  title: string;
  /** The title as the library shows it, for a title there under another name than TMDB's. */
  display_title?: string;
  year: number;
  overview?: string;
  /** The overview in the language titles are shown in, when TMDB has one; overview stays English. Use lib/displayTitle shownOverview. */
  display_overview?: string;
  poster_url?: string;
  backdrop_url?: string;
  vote_average: number;
  release_date?: string;
  genres?: string[]; // up to three
  in_library: boolean;
  has_file: boolean;
  wanted?: boolean; // in the library, monitored and missing files: the server is already after it
  request_status?: "pending" | "approved" | "declined";
  download_progress?: number; // 0..1 while downloading
}
export interface WatchProvider { id: number; name: string; logo_url?: string }
export interface DiscoverRow { title: string; seed: string; items: DiscoverCard[] }
/** One page of a paged Discover grid (browse, search). Search pages carry the people on them. */
export interface DiscoverPage { items: DiscoverCard[]; page: number; total_pages: number; people?: PersonResult[] }
/** A person in Discover search. */
export interface PersonResult { id: number; name: string; profile_url?: string; known_for_department?: string; known_for?: string[] }
/** A person's page: who they are and their credits as cards. */
export interface PersonDetail {
  id: number;
  name: string;
  biography?: string;
  profile_url?: string;
  birthday?: string;
  place_of_birth?: string;
  known_for_department?: string;
  credits: DiscoverCard[];
}

export interface Genre {
  id: number;
  name: string;
}

export type UserRole = "admin" | "manager" | "requester" | "readonly";
export interface AuthUser {
  id: number;
  username: string;
  role: UserRole;
  disabled?: boolean;
  /** Every type auto-approves; the per-type flags are what requests follow. */
  auto_approve: boolean;
  auto_approve_movie?: boolean;
  auto_approve_series?: boolean;
  auto_approve_book?: boolean;
  /** Users list only: their own request limits. */
  quota?: UserQuota;
  created_at?: string;
  // Signs in with Plex; plex_blocked means that Plex account is on the block list.
  plex_linked?: boolean;
  plex_blocked?: boolean;
  /** The Plex name the link was made with. */
  plex_username?: string;
  /** Plex is their only way in (no password anyone knows): unlinking would strand them. */
  plex_only?: boolean;
  // The same for a linked Jellyfin user.
  jellyfin_linked?: boolean;
  jellyfin_blocked?: boolean;
  jellyfin_username?: string;
  jellyfin_only?: boolean;
}

/** One filesystem on System → Status, with the other folders that share it. */
export interface SystemDisk {
  name: string;
  path: string;
  measurable: boolean;
  total: number;
  free: number;
  used_pct: number;
  shared_with: { name: string; path: string }[];
}
/** GET /system/status (admin). */
export interface SystemStatus {
  version: string;
  commit: string;
  go: string;
  started_at: string;
  uptime_seconds: number;
  data_dir: string;
  /** The database, its write-ahead log and index, together. */
  db_bytes: number;
  last_backup: { at: string; kind: string; file: string } | null;
  disks: SystemDisk[];
  /** The "Update available" check; null/absent when the server has none wired. */
  update?: UpdateStatus | null;
}

/** The newest published release, as the update check last saw it. */
export interface UpdateRelease {
  tag: string;
  name: string;
  /** Its github.com page, or "" when GitHub gave anything else. */
  url: string;
  /** Markdown as GitHub has it, cut to 4 KB. Show as text, never as HTML. */
  notes: string;
  notes_truncated: boolean;
  published_at: string;
}

/** GET /system/status `update`, and PUT/POST /system/update-check (admin). */
export interface UpdateStatus {
  enabled: boolean;
  current: string;
  /**
   * release: a v-tag build, compared with the newest release; main: a commit build on an install
   * that follows main, compared with main; build: a commit build on an install that follows the
   * releases (updates keep it until a release includes it), compared with the newest release;
   * dev: none of these.
   */
  channel: "release" | "main" | "build" | "dev";
  checked_at: string | null;
  latest: UpdateRelease | null;
  available: boolean;
  /** Commits main has that this build lacks; null = unknown or not a build of main. */
  behind: number | null;
  /** channel build: commits this build has that the newest release lacks; null = unknown. */
  ahead?: number | null;
  /** Why the last attempt couldn't check, in words. */
  problem?: string;
  /** "unraid": Unraid's own Docker manager runs this container, so it updates from Unraid's Docker page. */
  host?: "unraid";
}

/**
 * One person on Settings → People & access (GET /people, admin). Times are unix ms, 0 =
 * never. Listening is when, never what: there is no book in here.
 */
/** POST /auth/login: a session (user), or the two-factor step's ticket (never a session). */
export type LoginAnswer = { user: AuthUser; two_factor?: undefined } | { two_factor: "code" | "setup"; ticket: string; user?: undefined };

export interface Person {
  id: number;
  username: string;
  role: UserRole;
  disabled: boolean;
  created_at?: string;
  auto_approve_movie: boolean;
  auto_approve_series: boolean;
  auto_approve_book: boolean;
  plex_linked: boolean;
  plex_username?: string;
  plex_only?: boolean;
  plex_blocked?: boolean;
  jellyfin_linked?: boolean;
  jellyfin_username?: string;
  jellyfin_only?: boolean;
  jellyfin_blocked?: boolean;
  quota: UserQuota;
  /** Browsers signed in (only they see which). */
  sessions: number;
  web_last: number;
  audio_last: number;
  listen_last: number;
  /** The latest of web_last, audio_last and listen_last. */
  last_seen: number;
  /** An enabled account that may request: read-only accounts can't connect apps. */
  audio_eligible: boolean;
  audio_allowed: boolean;
  audio_password_set: boolean;
  audio_devices: number;
  /** Their password sign-in asks for an authenticator code too. */
  two_factor?: boolean;
}

/** One kind of request limit: limit 0 is unlimited; resets_at is when the oldest use frees up. */
export interface QuotaUse { limit: number; used: number; resets_at?: string }
/** GET /me/quota: the caller's request limits per window of `days`. */
export interface MyQuota { days: number; movie: QuotaUse; season: QuotaUse; book: QuotaUse }
/** A user's own limits: -1 follows the global limit, 0 is unlimited. */
export interface UserQuota { movies: number; seasons: number; books: number }

/** Which media types a user's requests approve themselves for. */
export interface AutoApproveFlags {
  auto_approve_movie?: boolean;
  auto_approve_series?: boolean;
  auto_approve_book?: boolean;
}

// A Plex account kept from signing in (it would otherwise make a new account each time).
export interface PlexBlock {
  plex_id: string;
  name: string;
  at: string;
}

// The same for a Jellyfin user.
export interface JellyfinBlock {
  jellyfin_id: string;
  name: string;
  at: string;
}

// What deleting a user takes with them — counts only, never which books (privacy rule).
export interface UserImpact {
  places: number;
  listening_hours: number;
  bookmarks: number;
  devices: number;
  requests: number;
  sessions: number;
  push_subscriptions: number;
  plex_linked: boolean;
  jellyfin_linked?: boolean;
}

export interface CrewMember {
  id?: number; // TMDB person id: opens their page on Discover (absent on old records)
  name: string;
  job: string;
  profile_url?: string;
}
export interface DetailRatings {
  tmdb?: number;
  imdb?: string;
  rotten_tomatoes?: string;
  metacritic?: string;
}
export interface MediaDetail {
  media_type: "movie" | "series";
  tmdb_id: number;
  imdb_id?: string;
  title: string;
  year: number;
  overview?: string;
  /** The overview in the language titles are shown in, when TMDB has one; overview stays English. */
  display_overview?: string;
  poster_url?: string;
  backdrop_url?: string;
  runtime?: number;
  status?: string;
  network?: string;
  genres?: string[];
  certification?: string;
  studios?: string[];
  cast?: { id?: number; name: string; character?: string; profile_url?: string }[];
  crew?: CrewMember[];
  ratings: DetailRatings;
  // Enrichment added by the metadata worker — render defensively (only when present).
  trailer_url?: string; // YouTube (or similar) trailer link
  similar?: DiscoverCard[]; // "more like this" — same shape as a Discover card
  seasons?: SeasonSummary[]; // series: regular seasons, specials left out
  /** The title as a Discover card with this viewer's badge state, for a sheet opened cold from its address. */
  card?: DiscoverCard;
  plex_url?: string; // app.plex.tv page for the title, when the owner's Plex has it
  watch_links?: WatchLink[]; // a Watch link per media server that has it, the viewer's own first
  collection?: { id: number; name: string }; // a movie's franchise, for "Part of the … collection"
}
/** One "Complete the <name>" row on Discover. */
export interface CollectionRow { collection_id: number; title: string; items: DiscoverCard[] }
/** A movie collection's page: every member as a card, and how much of what's out is here. */
export interface CollectionDetail {
  id: number;
  name: string;
  overview?: string;
  poster_url?: string;
  backdrop_url?: string;
  items: DiscoverCard[];
  owned: number;
  total: number;
}

export interface SeasonSummary { number: number; name?: string; episode_count: number; air_date?: string; poster_url?: string }

/**
 * A season's state for the requester: on disk, partly there, already being fetched,
 * not out yet, or free to ask for. Never carries monitoring flags or anyone's name.
 */
export type SeasonState = "in_library" | "requested" | "partial" | "on_the_way" | "unaired" | "requestable";
/** The request standing for a requested season. `requested_by_name` is sent to staff only. */
export interface SeasonRequest { request_id: number; status: "pending" | "approved"; mine: boolean; requested_by_name?: string }
export interface SeriesSeason {
  number: number;
  name?: string;
  episode_count: number;
  air_date?: string;
  have: number; // episodes on disk
  aired: number; // episodes out so far
  state: SeasonState;
  requestable: boolean; // a request may ask for it (requestable, or partial with nothing fetching the rest)
  request?: SeasonRequest; // state "requested"
}

// --- Series (TV) ---
// A metadata search hit offered as a manual-pick candidate for an unmatched folder.
export interface MatchCandidate {
  tmdb_id: number;
  title: string;
  year: number;
  poster_url?: string;
  overview?: string;
}

// A library folder the scan couldn't confidently identify, with candidates to pick from.
export interface UnmatchedFolder {
  folder: string;
  /** The library folder it was found in (a Movies or TV library can have several). */
  root?: string;
  title: string;
  year: number;
  candidates: MatchCandidate[];
}

// A finished movie library scan's job result (GET /api/v1/jobs/{id}.result): unmatched is
// how many folders landed on the needs-review list.
export interface MovieScanResult {
  imported: number;
  skipped: number;
  unmatched: number;
  attached: number;
  duplicates: number;
}

export interface SeriesLookup {
  tmdb_id: number;
  title: string;
  year: number;
  overview: string;
  poster_url: string;
  vote_average: number;
}
// A series' roll-up, specials left out. Only monitored episodes in monitored seasons count
// as wanted: episodes = files + aired wanted episodes; missing = aired wanted episodes with
// no file; unmonitored_missing = aired, no file, not monitored ("+N not monitored").
export interface SeriesStats {
  episodes: number;
  have_files: number;
  size_bytes: number;
  seasons: number;
  missing?: number;
  unmonitored_missing?: number;
  next_air_date?: string;
}
export interface SeriesExtra {
  genres?: string[];
  backdrop_url?: string;
  cast?: CastMember[];
}
export interface Episode {
  id: number;
  season_number: number;
  episode_number: number;
  title?: string;
  overview?: string;
  air_date?: string;
  runtime?: number;
  still_url?: string;
  absolute_number?: number;
  monitored: boolean;
  has_file: boolean;
  file_path?: string;
  size_bytes?: number;
  // Kept out of profile-driven upgrades ("keep existing files"); Resume lifts it.
  upgrade_hold?: boolean;
  download?: { state: string; progress: number; hash?: string };
  // The file's audio languages (detail endpoint only; absent until the file is analysed).
  audio_langs?: AudioLang[];
  audio_lang_fit?: AudioLangFit;
}
export interface Season {
  id: number;
  season_number: number;
  name?: string;
  overview?: string;
  poster_url?: string;
  monitored: boolean;
  episodes?: Episode[];
}
// SceneOverride pins where a scene/broadcast season starts in TMDB numbering, for anime
// whose cours don't line up (e.g. release "S02E01" is really S01E13).
export interface SceneOverride {
  scene_season: number;
  tmdb_season: number;
  tmdb_episode: number;
}

export interface Series {
  id: number;
  tmdb_id: number;
  /** The TV folder it belongs to (0: the default), when there are several. */
  library_folder_id?: number;
  imdb_id?: string;
  title: string;
  /** The title the app shows (Settings → Library → "Show titles as", or the title's own
   *  override); title stays TMDB's English title. Use lib/displayTitle shownTitle. */
  display_title?: string;
  /** The per-show "Display title…" pick; absent follows the setting. */
  title_override?: string;
  year: number;
  overview?: string;
  /** The overview in the language titles are shown in, when one is stored; overview stays English. */
  display_overview?: string;
  poster_url?: string;
  status?: string;
  network?: string;
  monitored: boolean; // the search gate ("Searching: On / Off"): off means nothing is searched, choices kept
  monitor_new_seasons?: boolean; // a season new on refresh is monitored
  // The monitoring preset last applied ("all", "missing", …), "custom" once seasons or
  // episodes were changed one by one since, "" / absent when not known.
  monitor_preset?: string;
  quality_profile: string;
  series_type?: string; // "standard" | "anime"
  // Whose listing the stored episode numbering follows: "tvdb" | "tvmaze" | "tmdb", or ""
  // when not yet recorded.
  numbering_source?: string;
  scene_overrides?: SceneOverride[];
  added_at?: string;
  last_refreshed_at?: string; // when metadata was last pulled ("" / absent = never)
  extra?: SeriesExtra;
  seasons?: Season[];
  stats?: SeriesStats;
  // Detail endpoint only: the newest history event's id. Panels that show the show's
  // history reload when it moves.
  last_event_id?: number;
}

// One episode's in-flight download, from the light poll the series page runs while
// something downloads (GET /series/{id}/downloads).
export interface SeriesEpisodeDownload { season: number; episode: number; state: string; progress: number; hash?: string }
// --- Books ---
export type BookSource = "openlibrary" | "hardcover";
export interface BookUpgradeStatus {
  running: boolean; total: number; done: number; upgraded: number; flagged: number; unmatched: number;
  started_at?: number; ended_at?: number; error?: string;
  notes?: string[]; // why the first few books didn't match
  left?: { id: number; title: string; author?: string; reason: string; flagged?: boolean }[]; // every book left as it was, flagged duplicates included
}
export interface BookSweepStatus {
  running: boolean; total: number; done: number; grabbed: number; skipped: number;
  started_at?: number; ended_at?: number;
  notes?: string[]; // the first few books nothing was found for
}
export interface BookLookup {
  key: string;
  title: string;
  author: string;
  year: number;
  cover_url?: string;
}
export interface BookFile {
  path: string;
  format: string;
  size_bytes: number;
  file_count: number;
  /** The ebook on the book page: Send to Kindle's email takes this format (EPUB, PDF). */
  kindle?: boolean;
  /** The ebook on the book page: "Download for Kobo (KEPUB)" is offered. */
  kepub?: boolean;
}
export interface BookFileEntry {
  name: string;
  size_bytes: number;
}
export interface Book {
  id: number;
  ol_key: string;
  title: string;
  author: string;
  /** The author row the author string means (lib/bookAuthorApi.ts); absent = not linked. */
  author_id?: number;
  year: number;
  /** Learned from the release that matched this book; position 0 = number unknown. */
  series_name?: string;
  series_position?: number;
  cover_url?: string;
  description?: string;
  subjects?: string[];
  monitored: boolean;
  quality_profile: string;
  ebook?: BookFile;
  audiobook?: BookFile;
  has_file: boolean;
  want_ebook: boolean;
  want_audiobook: boolean;
  added_at?: string;
  /** Extra audiobooks beyond the standard one — a full-cast production, another narrator. */
  audio_versions?: AudioVersion[];
  /** Where the metadata came from, with a link to the book there. Detail endpoints only;
   *  absent when the key is from no known catalogue. */
  catalogue?: CatalogueRef;
  /** Catalogue keys the book had before its current one (book_keys). Detail endpoints
   *  only; a card carrying any of them is this book. */
  aliases?: BookKeyAlias[];
  /** The search ladder: when the sweep last looked (RFC3339; absent = never), how many
   *  searches in a row found nothing, and when it looks next (absent when nothing is
   *  wanted, it isn't monitored, or it's due now). */
  last_search_at?: string;
  search_misses: number;
  next_search_at?: string;
  /** The day the book comes out (YYYY-MM-DD) when the catalogue says; not searched before it. */
  release_date?: string;
  /** Who reads the standard audiobook, typed by hand; absent = the audio files' tags say. */
  audiobook_narrator?: string;
  /** Details only a hand edit fills (language is a canonical code, isbn bare digits). */
  subtitle?: string;
  language?: string;
  isbn?: string;
  publisher?: string;
  /** The details edited by hand on the book page, each with the catalogue's value under
   *  it (what Reset puts back). The fields above already show the edits. */
  edited?: Partial<Record<BookEditField, string | number | string[]>>;
}
/** A book detail that can be edited by hand (books.EditableFields). */
export type BookEditField = "title" | "subtitle" | "author" | "series_name" | "series_position" | "description"
  | "year" | "language" | "subjects" | "isbn" | "narrator" | "publisher";
export interface CatalogueRef {
  name: "Hardcover" | "Open Library" | "Google Books";
  url: string;
}
export interface BookKeyAlias {
  key: string;
  /** The catalogue that issued it, or "request" for a key learned from a request. */
  source: "openlibrary" | "hardcover" | "google" | "request";
  added_at?: string;
}
// AudioVersion is one extra audiobook of a book. A release belongs to it when it
// mentions one of its terms; with no terms it is filled by hand only.
export interface AudioVersion {
  id: number;
  book_id: number;
  label: string;
  terms: string[];
  monitored: boolean;
  file?: BookFile;
  added_at?: string;
  /** Who reads it, typed by hand; absent = the audio files' tags say. */
  narrator?: string;
}
export interface BookImportCandidate {
  path: string;
  filename: string;
  edition: "ebook" | "audiobook";
  format: string;
  size_bytes: number;
}
// Books Discover (Open Library browse/search + author catalogues)
export interface BookDiscoverCard {
  key: string;
  title: string;
  author: string;
  year: number;
  cover_url?: string;
  // Catalogue signals (Hardcover only): rating out of 5, how many rated, how many shelved, top genres.
  rating?: number;
  ratings?: number;
  readers?: number;
  genres?: string[];
  series_name?: string;
  series_position?: number;
  in_library: boolean;
  has_file: boolean;
  requested: boolean;
  request_status?: "pending" | "approved" | "declined" | "";
  /** Per format, for a book in the library: what's on disk and what its profile wants;
   *  and what the request badging the card asked for. */
  has_ebook?: boolean;
  has_audiobook?: boolean;
  want_ebook?: boolean;
  want_audiobook?: boolean;
  request_formats?: "ebook" | "audiobook" | "both" | "";
}
export interface BookAuthor {
  key: string;
  name: string;
  work_count: number;
  top_work?: string;
  birth_date?: string;
  image_url?: string; // Hardcover authors have photos
  bio?: string;
}
export interface BookMeta {
  key: string;
  title: string;
  author: string;
  year: number;
  cover_url?: string;
  description?: string;
  subjects?: string[];
  pages?: number;
  rating?: number;
  ratings?: number;
  readers?: number;
  genres?: string[];
  series_name?: string;
  series_position?: number;
  /** The day it comes out (YYYY-MM-DD), when the catalogue knows it. */
  release_date?: string;
  /** Detail only: the editions of the owner's default book profile, where the request
   *  control starts when the viewer hasn't picked before. */
  default_book_formats?: "ebook" | "audiobook" | "both";
}
export interface BookRecommendedRow { title: string; seed: string; seed_id: number; books: BookDiscoverCard[] }

export interface SeriesImportCandidate {
  path: string;
  filename: string;
  season: number;
  episode: number;
  size_bytes: number;
  quality?: string;
}

// One proposed episode-file rename. conflict is set when it can't happen (another file
// already has the new name) and says why.
export interface SeriesRenameItem {
  from: string;
  to: string;
  season: number;
  episode: number;
  conflict?: string;
}

// A rename the server left alone, and why.
export interface RenameSkip {
  from: string;
  to: string;
  season: number;
  episode: number;
  reason: string;
}

// A renumber a refresh found but didn't apply: every file it would move, for review.
export interface NumberingRemap {
  absolute: number;
  old: string; // "S02E22"
  new: string; // "S03E01", or "" when the new numbering has no place for the file
  file: string; // base name
}
export interface NumberingPending {
  from: string;
  to: string;
  created_at: string;
  plan_hash: string;
  files: number; // distinct files that would move
  remaps: NumberingRemap[];
}
export interface SeriesNumbering {
  source: string;
  pending: NumberingPending | null;
}
// The Apply job's result.
export interface NumberingApplied {
  moved: number;
  skipped: RenameSkip[];
  unplaced?: number; // files with no episode in the new numbering, left where they are
}

export interface QueueItem {
  hash: string;
  name: string;
  state: string;
  progress: number;
  size_bytes: number;
  downloaded_bytes: number;
  down_speed: number;
  up_speed: number;
  eta_seconds: number;
  ratio: number;
  category?: string;
}

export type NetworkPreference = "best" | "usenet" | "torrent";

// --- Subtitles ---
export interface SubtitleSettings {
  movies_auto: boolean;
  series_auto: boolean;
  languages: string[]; // the Default profile's languages (the kept languages)
  keep_languages?: string[]; // every language any profile wants: what Convert's "Same as Subtitles" keeps
  provider_ready: boolean;
  can_download: boolean;
  ai_ready: boolean;
  ai_backend: string; // "sycl" | "vulkan" | "cpu" | "" until the first run
  ai_note: string; // why the Intel oneAPI build was set aside, when it was ("" otherwise)
  ai_cpu_only?: boolean; // the bundled whisper has no GPU backend (the ARM image)
  quota_remaining: number; // -1 = unknown
  quota_reset_at: number; // unix seconds; 0 = not paused
  pending: number;
  min_score: number; // 0-100: what a download without a hash match must score
  // What OpenSubtitles actually said; only "ok" means a login has worked.
  provider_state: SubtitleProviderState;
  provider_detail: string;
  vip: boolean;
  allowed_downloads: number; // per day; -1 = unknown
  upgraded_week?: number; // weak subtitles the daily upgrade replaced in the last 7 days
  stale?: boolean; // the Library/Overview figures were counted for other languages; a recount is under way
}
export type SubtitleProviderState = "not_configured" | "needs_account" | "unverified" | "ok" | "auth_failed" | "quota_spent" | "rate_limited";
export interface SubTrack { index: number; codec: string; lang: string; text: boolean; title?: string; forced?: boolean; sdh?: boolean; default?: boolean }
export interface SubLangStatus {
  lang: string; have: boolean; source?: "extract" | "ocr" | "download" | "ai"; fallback?: "ai"; orphan?: boolean;
  variant?: "forced"; // a profile's forced want; absent = the full subtitle
  // Set when the sweep is leaving this missing language alone for now (backoff).
  reason?: SubtitleResult;
  next_retry_at?: number; // unix seconds; absent = waiting on retry_on, or a manual retry
  retry_on?: string; // CSV: creds | model | langs
  health?: SubLangHealth; // present languages: how well it lines up with the speech
  file?: string; // present languages: the sidecar's file name
}
export type SubHealthState = "ok" | "uncertain" | "bad" | "unknown" | "unchecked";
export interface SubLangHealth {
  state: SubHealthState;
  score?: number; // sync confidence 0-100, when checked
  offset_ms?: number; // how late it ran when checked (+ = late)
  shift_ms?: number; // the shift Arrmada applied
  stretched?: boolean; // a frame-rate correction was applied too
  checked_at?: number; // unix seconds
  source?: string;
  note: string; // "shifted −3.0 s · checked 2026-10-12", "not checked"
}
export interface SubHealth { state: SubHealthState; score?: number; notes?: string[] }
export interface SubFileEntry {
  kind: "movie" | "episode";
  movie_id?: number; series_id?: number; season?: number; episode?: number;
  title: string; year?: number; poster_url?: string; path: string; duration_sec?: number;
  audio_langs?: string[]; embedded: SubTrack[]; external: string[];
  languages: SubLangStatus[]; health?: SubHealth; missing: number;
  orphans?: { name: string; lang?: string; variant?: string }[]; // movies: subtitles paired with no video
  profile_id?: number; // the language profile the title is pinned to; absent = Default
}
export interface SubtitleJob {
  id: number; kind: "movie" | "episode"; movie_id?: number; series_id?: number; season?: number; episode?: number;
  title: string; state: "queued" | "running" | "done" | "skipped" | "failed" | "cancelled"; note?: string; at: number;
  progress?: number; // 0-100 during an AI run
  stage?: string;    // what a running job is doing
  started_at?: number; // unix seconds the worker picked it up
  redo?: boolean; // replacing the sidecars already there
  mode?: string; // "next" | "ai" | "extract" for a one-language redo; "repair-forced"; "upgrade" for the daily upgrade
  priority: number; // 0 import · 1 manual · 2 sweep — the worker takes the lowest first
  run_id?: string; // this run's rows in GET /subtitles/history?run_id=
  outcomes?: SubtitleOutcome[]; // what each rung did per language, in the order tried
  lane?: "fast" | "gpu"; // fast: extract + download · gpu: whisper
  langs?: string[]; // only these languages (a GPU job carries what the fast lane couldn't make)
  audio_sec?: number; // the file's running time, once known
  finished_at?: number; // unix seconds
}
// One rung's typed result for one language (internal/subtitles/ledger.go Outcome).
export type SubtitleRung = "extract" | "opensubtitles" | "ai" | "upgrade" | "repair";
export type SubtitleResult =
  | "ok" | "empty" | "extract_failed" | "no_match" | "quota" | "rate_limited" | "auth_failed" | "provider_error"
  | "below_score" | "out_of_sync" | "model_missing" | "ai_failed" | "no_audio" | "impossible" | "ocr_needed" | "cancelled";
export interface SubtitleOutcome { lang: string; variant?: string; rung: SubtitleRung; result: SubtitleResult; detail?: string; score?: number; reasons?: string[] }
// A row of GET /api/v1/subtitles/history (newest first; page with ?before=next_before).
export interface SubtitleAttempt {
  id: number; run_id: string; media_key: string; video_path: string; lang: string; variant?: string;
  rung: SubtitleRung; outcome: SubtitleResult; detail?: string; at: number;
}
// SubtitleCoverage is the Overview's totals, from the last library pass (not a live walk).
export interface SubtitleCoverage {
  files: number; covered: number; missing: number;
  movies: { files: number; covered: number; missing: number };
  tv: { files: number; covered: number; missing: number };
  scanned_at: number; // 0 until the first pass completes
  scanning: boolean;
  stale?: boolean; // counted for a different set of kept languages; a recount is under way
}
export interface WhisperModel { name: string; label: string; size_mb: number; present: boolean; downloading: boolean }
export interface WhisperStatus {
  binary_ready: boolean; ready: boolean; models: WhisperModel[];
  waiting?: number; // file languages parked until a model is downloaded (model_missing)
}

// --- Convert ---
export interface ConvertEncoder { codec: string; name: string; kind: string; label: string; hardware: boolean; available: boolean }
export interface ConvertMediaInfo {
  container: string; video_codec: string; width: number; height: number; resolution: string; hdr: string; dv_base?: string;
  bitrate_kbps: number; frame_rate: number; duration_sec: number; size_bytes: number; audio_tracks: number; sub_tracks: number; ten_bit: boolean;
}
export interface ConvertSkipped {
  key: string; kind: string; reason: string; permanent: boolean; updated_at: string;
  retry_after: number; attempts: number; // unix seconds (0 = no wait); same-kind repeats in a row
  media_kind: string; movie_id?: number; version_id?: number; series_id?: number; season: number; episode: number; title: string;
}

export interface ConvertBlocked {
  key: string; kind: string; movie_id?: number; version_id?: number; series_id?: number; season: number; episode: number;
  title: string; count: number; last_error: string; updated_at: string;
}

export interface ConvertMediaStats {
  files: number; convertible: number; total_bytes: number; est_bytes: number; convertible_bytes: number; reclaimable: number;
  skipped: number;
  hdr10: number; hdr10_plus: number; dolby_vision: number; hlg: number;
  h264: number; hevc: number; av1: number; other: number;
  // Every Dolby Vision file the Dolby Vision choice applies to, convertible or not.
  dolby_vision_files: number;
}
// A book's series as the library can see it: the entries you own, in reading order, and
// the numbered holes between them.
export interface BookSeriesEntry {
  book_id?: number; title: string; position?: number; has_file: boolean; missing: boolean;
  // From the catalogue's listing: a missing entry carries the key it can be added under.
  key?: string; author?: string; year?: number; cover_url?: string;
}
export interface BookSeries { name: string; entries: BookSeriesEntry[]; gaps: number; source?: "catalogue" | "library"; total?: number; key?: string }

export interface ConvertLibraryStats { movies: ConvertMediaStats; tv: ConvertMediaStats; total: ConvertMediaStats; as_of?: number }

export interface ConvertSeriesRollup {
  series_id: number;
  title: string;
  year?: number;
  poster_url?: string;
  files: number;
  convertible: number;
  reencode: number;
  tidy_only: number;
  total_bytes: number;
  est_bytes: number;
  save_bytes: number;
}

export interface ConvertNeeds { video: boolean; subs: boolean; audio: boolean; why?: string; save: number; worth: boolean; measured?: boolean }
// version_id/version_label: an extra version track of the movie (absent = its default file);
// title stays the film's own.
export interface ConvertCandidate { kind: "movie" | "episode"; key: string; movie_id?: number; series_id?: number; season?: number; episode?: number; title: string; year?: number; poster_url?: string; path: string; info?: ConvertMediaInfo; candidate: boolean; worth: boolean; save_bytes: number; needs: ConvertNeeds; est_bytes: number; tracks?: string; version_id?: number; version_label?: string;
  revert_history_id?: number /* its conversion can still be reverted (the original is in the hold) */ }
export interface ConvertJob {
  id: number; key: string; kind?: string; movie_id?: number; version_id?: number; series_id?: number; season?: number; episode?: number; title: string;
  state: "preparing" | "testing" | "encoding" | "verifying" | "replacing" | "done" | "failed" | "skipped" | "cancelled";
  progress: number; fps: number; speed_x: number; duration_sec?: number; encoder: string; codec?: string;
  src_bytes: number; out_bytes: number; ssim?: number; note?: string; requested: boolean; paused?: string;
  started_at: number; finished_at?: number;
}
export interface ConvertTrackDecision {
  type: "audio" | "subtitle"; index: number; codec: string; lang?: string; title?: string; channels?: number;
  forced?: boolean; image?: boolean; keep: boolean; reason?: string;
}
export type ConvertHistoryOutcome = "in_progress" | "done" | "failed" | "skipped" | "cancelled";
// One row of the conversion ledger (GET /convert/history). src_info/out_info only on the single-row read.
export interface ConvertHistoryEntry {
  id: number; key: string; kind: "movie" | "episode"; movie_id?: number; version_id?: number; series_id?: number; season: number; episode?: number; title: string;
  outcome: ConvertHistoryOutcome; outcome_kind?: string; note?: string; requested: boolean;
  src_path?: string; src_release?: string; src_size: number; src_spec?: string;
  out_path?: string; out_size: number; out_spec?: string; codec?: string; crf?: number; encoder?: string; crop?: string;
  ssim_mean?: number; ssim_min?: number; ssim_windows?: number[]; vmaf_mean?: number; // vmaf_mean: advisory, only with a libvmaf ffmpeg
  kept_tracks?: ConvertTrackDecision[]; dropped_tracks?: ConvertTrackDecision[]; warnings?: string[]; reclaim_deferred?: boolean;
  started_at: number; finished_at?: number; encode_secs?: number; repeats?: number; poster_url?: string;
  // The originals hold: where the original waits and until when, and what became of it.
  hold_path?: string; hold_until?: number; hold_state?: ConvertHoldState; released_at?: number; reverted_at?: number;
  src_info?: ConvertMediaInfo; out_info?: ConvertMediaInfo;
}
export type ConvertHoldState = "" | "held" | "released" | "reverted" | "missing";
// What the startup self-check found for one library folder's originals hold.
export interface ConvertHoldCheck { root: string; label: string; instant: boolean; err?: string }
// What the originals hold keeps (GET /convert/hardware → hold). budget_bytes 0 = no limit.
export interface ConvertHoldStats {
  held_bytes: number; held_saving_bytes: number; held_count: number; next_release_at?: number; budget_bytes: number; hold_days: number;
}
export interface ConvertUpNext {
  key: string; title: string; kind: string; video: boolean; saving: number; size: number; tracks: string; reason: string; codec: string; current: string;
  version_id?: number; version_label?: string;
  eta_sec?: number; nights?: number; // the time forecast: seconds, and nights of the encode hours
}
export interface ConvertRequest { key: string; title: string; requested_at: number }
// GET /api/v1/convert/devices: what each Plex player did with HEVC, HEVC 10-bit and AV1 in
// the last 90 days of recorded plays (Insights). "yes" = played as it was at least once,
// "no" = only ever had its video transcoded, "unknown" = never played that format.
export interface ConvertDeviceCodec { state: "yes" | "no" | "unknown"; direct: number; transcoded: number }
export interface ConvertDevice {
  /** The server whose app it is: the same TV plays through Plex's app and Jellyfin's. */
  server?: InsightsServer;
  product: string; platform: string; player: string; last_seen: number; plays: number;
  hevc: ConvertDeviceCodec; hevc10: ConvertDeviceCodec; av1: ConvertDeviceCodec;
}
export interface ConvertDevices { monitoring: boolean; since: number; devices: ConvertDevice[] }
export interface ConvertStatus {
  state: "working" | "starting" | "paused" | "waiting" | "off" | "done"; message: string;
  auto: boolean; window?: string; watching: boolean;
  requests: ConvertRequest[]; up_next: ConvertUpNext[]; remaining: number;
  /** Files that would convert but sit in Problems (skipped or failing) — not in remaining. */
  problems?: number;
  // The time forecast: the remaining list's encoding time (spread over the conversions that
  // run at once) and in nights of the hours; the encoding a restart now would lose.
  backlog_sec?: number; backlog_nights?: number; in_flight_secs?: number;
}
export interface ConvertSettings {
  auto: boolean; hours_start: string; hours_end: string; allow_av1: boolean; use_gpu: boolean; pause_watching: boolean;
  keep_audio_langs: string; keep_original_lang: boolean; drop_commentary: boolean; keep_sub_langs: string;
  image_subs: "keep" | "when_text" | "remove"; tidy_tracks: boolean; crop: boolean;
  hold_days: number; hold_max_gb: number;
  // Dolby Vision files: drop the layer and keep the HDR10/HLG base, leave them alone, or
  // (a preview, needs dovi_tool) convert and keep the layer as profile 8.1.
  dolby_vision: "drop" | "skip" | "keep";
  /** dovi_tool is installed, so "keep" can be chosen. */
  dovi_tool?: boolean;
  /** 8-bit SDR sources: "always" written 10-bit, or "match_source" (kept 8-bit). */
  ten_bit?: "always" | "match_source";
  /** Encode long CPU films in pieces a restart can't throw away (off by default). */
  resumable?: boolean;
  scratch_dir: string; vaapi_device: string; cpu_cores: number; workers: number; scan_at: string;
  server_time: string; server_tz: string; plex_watching_known: boolean; can_pause: boolean;
  has_gpu: boolean; gpu_does_av1: boolean; hdr10plus_tool: boolean;
}
export interface ConvertTrialSide { codec: string; encoder: string; bytes: number; ssim: number; est_bytes: number }
export interface ConvertTrialResult { key: string; title: string; src_bytes: number; clips: number; seconds: number; hevc: ConvertTrialSide; av1: ConvertTrialSide; pick: string; why: string; files?: string[] }
export interface ConvertCompareStatus { running: boolean; key?: string; title?: string; started?: number; result?: ConvertTrialResult; error?: string }
// One file's conversion plan (GET /convert/plan?key=): built from the index by the runner's own
// decision code, so it is what a conversion would do. to_codec "" = the picture is copied.
export interface ConvertPlanVideo {
  from_codec: string; from_res: string; from_hdr: string; from_bits: number;
  to_codec?: string; to_bits?: number; to_hdr?: string; crf?: number;
  est_bytes: number; measured?: boolean; why?: string; codec_note?: string; crop_note?: string; hdr_note?: string;
}
export interface ConvertPlan {
  key: string; kind: "movie" | "episode"; title: string; poster_url?: string; size_bytes: number; needs: ConvertNeeds;
  video: ConvertPlanVideo; audio: ConvertTrackDecision[]; subs: ConvertTrackDecision[];
  container: { from: string; to: string }; warnings: string[]; eta_sec: number | null;
}
// What switching automatic conversion on would do (GET /convert/auto-preview), or converting
// one show or season now (?series=&season=), counted from the runner's cached candidates.
export interface ConvertAutoPreview {
  files: number; reencode: number; bytes: number; est_bytes: number;
  dv_files: number; cc_files: number; image_sub_tracks: number; filtered_audio_tracks: number;
  container_changes: number; forced_tracks: number; as_of: number;
}
// The answer to a Convert-now request: starting now, or its place among the waiting requests
// (1 = next) and what it waits behind; reason when nothing can start (someone is watching).
export interface ConvertRequestResult { requested: string; starting?: boolean; position?: number; ahead?: string; reason?: string }

// Insights (Plex watch monitoring).
export interface PlexLibrary { key: string; title: string; type: string }
// How Plex monitoring is actually going, worked out server-side from the poller's record:
// unconfigured (no URL/token), off (saved but not recording), recording, or unreachable
// (monitoring on, Plex not answering).
export type PlexStatus = "unconfigured" | "off" | "recording" | "unreachable";
export interface PlexConfig {
  url: string; token_set: boolean; enabled: boolean; poll_seconds: number;
  /** Someone has switched monitoring on or off (until then the form starts with it on). */
  enabled_set?: boolean;
  /** The connected server, from its own identity: set on sign-in, save and Test. */
  server_name?: string; machine_id?: string;
  status: PlexStatus;
  /** Unix seconds of Plex's last answer. */
  last_poll_at?: number;
  /** Why the last exchange with Plex failed, while it's failing. */
  last_error?: string;
  /** Unix seconds: plays recorded before this stored "HW requested", not "HW used". */
  hw_since?: number;
}
export interface PlexTestResult { ok: boolean; error?: string; machine_id?: string; server_name?: string; version?: string; libraries?: PlexLibrary[] }
export interface GeoLocation { ip: string; local: boolean; city?: string; country?: string; country_code?: string; lat?: number; lon?: number }
export interface StreamDetail { src: string; stream?: string }
/** The media server a stream, play, person or title row belongs to ("" = both, on a title watched on each). */
export type InsightsServer = "plex" | "jellyfin";
/** Each media server Insights can watch (GET /insights/servers): connected, and what its monitoring is doing. */
export interface InsightsServerStatus {
  server: InsightsServer; name?: string; configured: boolean; monitoring: boolean; status: PlexStatus;
  poll_seconds: number; last_poll_at?: number; last_error?: string;
}
export interface InsightsStream {
  server?: InsightsServer;
  /** thumb is the portrait poster (an episode's show poster); still is an episode's own frame, art the background. */
  session_key: string; user: string; user_id?: string; title: string; subtitle: string; type: string; thumb: string; still?: string; art?: string;
  progress_pct: number; offset_ms: number; duration_ms: number; state: string;
  player: string; platform: string; product: string; decision: string;
  bandwidth_kbps: number; location: string; ip: string; geo: GeoLocation;
  video: StreamDetail; audio: StreamDetail; container: StreamDetail;
  hw_transcode: boolean; throttled: boolean; reasons: string[];
  /** What Plex is really running on the GPU; hw_requested without hw_encode = fell back to CPU.
   *  Jellyfin: hw_transcode with hw_title = its configured acceleration ("vaapi"); it doesn't say more. */
  hw_decode: boolean; hw_encode: boolean; hw_requested: boolean; hw_title?: string;
}
export interface InsightsActivity {
  streams: InsightsStream[]; bandwidth: { total_kbps: number; lan_kbps: number; wan_kbps: number }; geo_active: boolean;
  /** A connected server that didn't answer, by server, while another did. */
  errors?: Partial<Record<InsightsServer, string>>;
}
export interface HistoryEntry {
  server?: InsightsServer;
  id: number; user_id: string; user_name: string; title: string; grandparent_title: string; parent_title: string;
  media_index: number; parent_index: number; year: number; media_type: string; thumb: string; thumb_url: string;
  /** The show an episode belongs to, recorded since PLEX-20 ("" before that and for movies). */
  grandparent_thumb: string; grandparent_rating_key: string;
  player: string; platform: string; product: string; ip_address: string; location: string; decision: string;
  started_at: number; stopped_at: number; paused_ms: number; view_offset_ms: number; duration_ms: number;
  video_src: string; video_stream: string; audio_src: string; audio_stream: string; container_src: string; container_stream: string;
  hw_transcode: boolean; buffer_count: number; subtitle: string; geo: GeoLocation; watched_secs: number; progress_pct: number;
  /** How many sittings a grouped row folds together (History with group=1). */
  sessions?: number;
}
export interface InsightsHistory { rows: HistoryEntry[]; total: number }
export interface TitleStat { title: string; thumb_url: string; plays: number; secs: number; server?: InsightsServer | "" }
export interface NameStat { id: string; name: string; plays: number; secs: number; server?: InsightsServer | "" }
export interface InsightsStats { most_watched_movies: TitleStat[]; most_watched_shows: TitleStat[]; most_active_users: NameStat[]; most_active_platforms: NameStat[]; recently_watched: HistoryEntry[] }
export interface UserEntry { id: string; server?: InsightsServer; username: string; avatar_url?: string; last_seen: number; last_ip: string; last_platform: string; last_player: string; last_title: string; total_plays: number; total_secs: number; geo: GeoLocation }
export interface LibraryStat { title: string; type: string; count: number; server?: InsightsServer }
export interface RecentItem { title: string; subtitle: string; type: string; thumb_url: string; added_at: number; server?: InsightsServer }
export interface BWPoint { t: string; total_kbps: number; lan_kbps: number; wan_kbps: number }
// Every play series is in `metric`: plays, or hours watched ("duration").
export interface InsightsGraphs {
  metric: "plays" | "duration";
  days: string[]; daily_tv: number[]; daily_movies: number[]; daily_music: number[];
  /** The same plays by stream type, aligned to days; per day they add up to tv + movies + music. */
  daily_direct_play: number[]; daily_direct_stream: number[]; daily_transcode: number[];
  by_day_of_week: number[]; by_hour: number[];
  top_platforms: NameStat[]; top_users: NameStat[]; bandwidth: BWPoint[];
}
export interface ReliabilitySummary { total_sessions: number; buffered_sessions: number; total_events: number; total_stall_ms: number; buffer_rate_pct: number }
export interface BufferGroup { id?: string; name: string; sessions: number; buffered_sessions: number; events: number; stall_ms: number; rate_pct: number }
export interface BufferEvent { at: number; offset_ms: number; duration_ms: number; user: string; user_id?: string; title: string; platform: string; decision: string; cause: string; detail: string }
export interface CauseCount { cause: string; label: string; count: number; stall_ms: number }
// The Insights Library tab (GET /insights/library). media/tmdb come from the Plex library
// index; arrmada_id is set when Arrmada has the title.
export interface LibraryItem {
  kind: "movie" | "show"; title: string; year?: number; thumb_url: string; rating_key?: string;
  media?: "movie" | "series"; tmdb?: number; arrmada_id?: number;
  plays?: number; secs?: number; added_at?: number;
  server?: InsightsServer | "";
}
export interface HotSpot extends LibraryItem {
  transcodes: number; total_plays: number; top_pair: string; platforms: string[]; top_cause?: string; cause_label?: string;
}
export interface InsightsLibrary {
  window_days: number; most_watched_movies: LibraryItem[]; most_watched_shows: LibraryItem[];
  /** Oldest first, capped; never_watched_unavailable = Plex's library list can't be read right now. */
  never_watched: LibraryItem[]; never_watched_total: number; never_watched_unavailable: boolean;
  hot_spots: HotSpot[];
}
// One person's People page (GET /insights/users/{id}): their video plays on one server
// (someone on both servers has a page per server). Jellyfin reports no buffering, so a
// Jellyfin person's stalls stay zero.
export interface PersonProfile {
  id: string; server?: InsightsServer; username: string; avatar_url: string; window_days: number;
  first_seen: number; last_seen: number; total_plays: number; total_secs: number;
  /** Share of plays with a known runtime that reached 90%, out of completion_of plays. */
  completion_pct: number; completion_of: number;
  top_titles: TitleStat[]; platforms: NameStat[]; players: NameStat[];
  ips: { ip: string; geo: GeoLocation; plays: number; last_seen: number }[];
  decisions: { direct_play: number; direct_stream: number; transcode: number };
  stalls: { events: number; stall_ms: number; rate_pct: number };
  recent: HistoryEntry[];
}
export interface Reliability {
  summary: ReliabilitySummary; causes: CauseCount[]; by_user: BufferGroup[]; by_platform: BufferGroup[]; by_title: BufferGroup[]; events: BufferEvent[];
  /** Jellyfin plays in the window, left out of every figure (Jellyfin reports no buffering). */
  jellyfin_plays?: number;
}

// ApiError is still an Error (every existing catch keeps reading .message), but it also
// carries the status and the decoded body, so a refusal can show its details — e.g.
// which files a delete moved to the recycle bin and which it couldn't.
export class ApiError extends Error {
  status: number;
  path: string;
  body?: Record<string, unknown>;
  constructor(message: string, status: number, body?: Record<string, unknown>, path = "") {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
    this.path = path;
  }
}

// A 401 from anything but the auth endpoints means this browser's session has ended
// (expired, revoked, password changed, account turned off). The app is told once, through
// this window event, and swaps to the sign-in screen instead of every panel and poll
// showing its own "authentication required". The auth endpoints are left out because a
// wrong password, a pending Plex PIN or a signed-out /me legitimately answer 401.
export const SIGNED_OUT_EVENT = "arrmada:signed-out";
let signedOut = false;

export function signalSignedOut(): void {
  if (signedOut) return;
  signedOut = true;
  window.dispatchEvent(new CustomEvent(SIGNED_OUT_EVENT));
}

// resetSignedOut re-arms the signal after a sign-in.
export function resetSignedOut(): void {
  signedOut = false;
}

// send is the one place every API call goes through, JSON or upload: it turns a non-OK
// response into an ApiError carrying the server's message, and spots a lost session.
async function send(path: string, init?: RequestInit): Promise<Response> {
  const res = await fetch(path, init);
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith("/api/v1/auth/")) signalSignedOut();
    let msg = `HTTP ${res.status}`;
    let body: Record<string, unknown> | undefined;
    try {
      body = (await res.json()) as Record<string, unknown>;
      if (typeof body.message === "string" && body.message) msg = body.message;
    } catch {
      /* non-JSON error */
    }
    throw new ApiError(msg, res.status, body, path);
  }
  return res;
}

// releaseErrorMessage is what a search modal shows when a grab or block fails. A 410 means
// the result's token has expired (two hours, or the server restarted), so the fix is to
// search again rather than anything about the release.
export function releaseErrorMessage(e: unknown): string {
  if (e instanceof ApiError && e.status === 410) return "These results expired — search again.";
  return (e as Error).message;
}

// req is a JSON call. Exported for the few call groups that live in their own lazily
// loaded modules (lib/plexApi.ts, lib/plexLibraryApi.ts), so they stay out of a
// requester's first download.
export async function req<T>(path: string, opts?: RequestInit): Promise<T> {
  const res = await send(path, {
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    ...opts,
  });
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const api = {
  status: () => req<Status>("/api/v1/status"),
  // System → Status (admin): build, database size, last backup, drive space.
  systemStatus: () => req<SystemStatus>("/api/v1/system/status"),
  health: () => req<Health>("/api/health"),

  me: () => req<{ user: AuthUser }>("/api/v1/auth/me").then((r) => r.user),
  // A deliberate sign-out mutes the signed-out signal: a poll that 401s before the page
  // reloads must not flash "You were signed out" or remember the page to come back to.
  logout: () => {
    signedOut = true;
    return req<unknown>("/api/v1/auth/logout", { method: "POST" });
  },
  /** With two-factor sign-in on (or required of an admin who hasn't set it up), the answer
   * is a ticket for the code step instead of a session: see components/TwoFactorLogin. */
  login: (username: string, password: string) =>
    req<LoginAnswer>("/api/v1/auth/login", { method: "POST", body: JSON.stringify({ username, password }) }),
  setupAdmin: (username: string, password: string) =>
    req<{ user: AuthUser }>("/api/v1/auth/setup", { method: "POST", body: JSON.stringify({ username, password }) }),
  activity: () => req<ActivityFeed>("/api/v1/downloads"),
  wanted: () => req<WantedLists>("/api/v1/wanted"),
  /** Search now from a Wanted row: clears the title's backoff and runs its search as a job. */
  wantedSearch: (kind: WantedRow["media_type"], id: number) =>
    req<{ status: string; started_at_ms?: number } & MovieQueuedRef>(`/api/v1/wanted/${kind}/${id}/search`, { method: "POST" }),
  pauseDownload: (hash: string) => req<{ status: string }>(`/api/v1/queue/${hash}/pause`, { method: "POST" }),
  resumeDownload: (hash: string) => req<ResumeResult>(`/api/v1/queue/${hash}/resume`, { method: "POST" }),
  // mode: keep_files keeps what was downloaded (the default), delete_files deletes it, block
  // deletes it, blocklists the release and finds another. unmonitor stops wanting exactly
  // what the download was for. name helps find the grab when it has no recorded hash.
  deleteDownload: (hash: string, opts: { mode: RemoveDownloadMode; unmonitor?: boolean; name?: string }) => {
    const q = new URLSearchParams({ mode: opts.mode });
    if (opts.unmonitor) q.set("unmonitor", "true");
    if (opts.name) q.set("name", opts.name);
    return req<RemoveDownloadResult>(`/api/v1/queue/${encodeURIComponent(hash)}?${q}`, { method: "DELETE" });
  },
  // Answers with what the release was blocked for; the removal and the search for another
  // run as a job. A download tied to nothing in the library is refused (422) and left alone.
  blockDownload: (hash: string, name: string) =>
    req<{ status: string; blocked_for: BlockTarget } & JobRef>(`/api/v1/queue/${hash}/block`, { method: "POST", body: JSON.stringify({ name }) }),
  // "retry" is a failed usenet job's (SABnzbd tries it again); the rest are qBittorrent's.
  torrentAction: (hash: string, action: "recheck" | "reannounce" | "prio_up" | "prio_down" | "retry") =>
    req<{ status: string }>(`/api/v1/queue/${hash}/action`, { method: "POST", body: JSON.stringify({ action }) }),
  // Activity → Needs you: try another release now (a job), move a misfiled TV download
  // into the TV category, and retry a failing import now.
  failOverDownload: (hash: string) =>
    req<{ status: string } & JobRef>(`/api/v1/queue/${hash}/failover`, { method: "POST" }),
  setDownloadCategory: (hash: string, category: string) =>
    req<{ status: string; category: string }>(`/api/v1/queue/${hash}/category`, { method: "POST", body: JSON.stringify({ category }) }),
  retryImport: (hash: string) =>
    req<{ status: string }>(`/api/v1/imports/${hash}/retry`, { method: "POST" }),
  // Finished torrents Arrmada didn't grab: list, import into a picked title, or ignore.
  unmanagedDownloads: () => req<{ unmanaged: UnmanagedDownload[]; ignored: number }>("/api/v1/downloads/unmanaged"),
  unmanagedTargets: (hash: string, kind: ReviewKind, q: string) =>
    req<{ targets: ReviewTarget[]; truncated?: boolean }>(`/api/v1/queue/${hash}/targets?kind=${kind}&q=${encodeURIComponent(q)}`),
  // What it can refuse it refuses at once; the import itself is a job (202).
  importUnmanaged: (hash: string, kind: ReviewKind, targetId: number) =>
    req<ReviewImportStarted>(`/api/v1/queue/${hash}/import`, { method: "POST", body: JSON.stringify({ kind, target_id: targetId }) }),
  ignoreDownload: (hash: string) =>
    req<{ status: string }>(`/api/v1/queue/${hash}/ignore`, { method: "POST" }),
  // External service credentials, settable in-app (settings-first, env-fallback). The
  // server never returns the secret itself — only whether it's set, from where, and a hint.
  apiKeys: () => req<{ keys: APIKeyStatus[] }>(`/api/v1/apikeys`).then((r) => r.keys),
  // With a candidate, tests that value instead of the saved key; it is never stored.
  testAPIKey: (id: string, candidate?: string) =>
    req<{ ok: boolean; detail: string }>(`/api/v1/apikeys/${id}/test`, candidate ? { method: "POST", body: JSON.stringify({ value: candidate }) } : { method: "POST" }),
  setAPIKey: (id: string, value: string) =>
    req<{ keys: APIKeyStatus[] }>(`/api/v1/apikeys/${id}`, { method: "PUT", body: JSON.stringify({ value }) }).then((r) => r.keys),
  // Clearing is its own DELETE (a blank PUT is refused), so an empty Save can't wipe a key.
  clearAPIKey: (id: string) => req<{ keys: APIKeyStatus[] }>(`/api/v1/apikeys/${id}`, { method: "DELETE" }).then((r) => r.keys),
  clientSettings: (id: number) => req<ClientSettings>(`/api/v1/downloadclients/${id}/settings`),
  setClientSettings: (id: number, body: ClientSettings) =>
    req<{ status: string }>(`/api/v1/downloadclients/${id}/settings`, { method: "PUT", body: JSON.stringify(body) }),
  // Per-user notifications (in-app inbox + personal Apprise URL)
  myNotifications: () => req<{ notifications: UserNotification[]; unread: number }>("/api/v1/me/notifications"),
  markNotificationRead: (id: number) => req<void>(`/api/v1/me/notifications/${id}/read`, { method: "POST" }),
  pushKey: () => req<{ key: string }>("/api/v1/me/push/key").then((r) => r.key),
  pushSubscribe: (sub: { endpoint: string; keys: { p256dh: string; auth: string } }) =>
    req<{ subscribed: boolean }>("/api/v1/me/push/subscribe", { method: "POST", body: JSON.stringify(sub) }),
  pushUnsubscribe: (endpoint: string) =>
    req<{ subscribed: boolean }>("/api/v1/me/push/unsubscribe", { method: "POST", body: JSON.stringify({ endpoint }) }),
  /** Whether this browser's endpoint is registered to the signed-in user. */
  pushStatus: (endpoint: string) =>
    req<{ subscribed: boolean }>("/api/v1/me/push/status", { method: "POST", body: JSON.stringify({ endpoint }) }),
  markAllNotificationsRead: () => req<void>("/api/v1/me/notifications/read-all", { method: "POST" }),
  calendar: (start: string, end: string, signal?: AbortSignal) => req<{ items: CalendarItem[]; start: string; end: string }>(`/api/v1/calendar?start=${start}&end=${end}`, { signal }),
  // The saved URL never comes back: a hint stands in, and blocked_reason says why pushes
  // to it are being skipped (e.g. it points at the local network).
  myApprise: () => req<MyApprise>("/api/v1/me/apprise"),
  /** "" removes the saved URL. */
  setMyApprise: (url: string) => req<MyApprise>("/api/v1/me/apprise", { method: "PUT", body: JSON.stringify({ url }) }),

  // refresh re-runs every check first (the server allows that once per 10 s).
  systemHealth: (refresh = false) =>
    req<SystemHealth>(`/api/v1/health/system${refresh ? "?refresh=1" : ""}`),
  dashboard: () => req<DashboardData>("/api/v1/dashboard"),
  attention: () => req<Attention>("/api/v1/attention"),
  diskGuard: () => req<DiskGuardStatus>("/api/v1/downloads/disk-guard"),
  fileInfo: (path: string) => req<FileDetails>(`/api/v1/files/info?path=${encodeURIComponent(path)}`),
  seriesAliases: (id: number) => req<SeriesAlias[]>(`/api/v1/series/${id}/aliases`),
  addSeriesAlias: (id: number, body: { title: string; tmdb_season: number }) =>
    req<SeriesAlias>(`/api/v1/series/${id}/aliases`, { method: "POST", body: JSON.stringify(body) }),
  deleteSeriesAlias: (id: number, aliasID: number) =>
    req<{ status: string }>(`/api/v1/series/${id}/aliases/${aliasID}`, { method: "DELETE" }),

  queue: () => req<{ items: QueueItem[] }>("/api/v1/queue").then((r) => r.items),

  // A grab names the release by the token its search handed out; the download link
  // itself never leaves the server. movie_id must be the movie the search was for, and
  // version_id (a movie track; 0/absent = the default track) the track it was for.
  grab: (body: { token: string; movie_id: number; version_id?: number }) =>
    req<{ status: string; title: string }>("/api/v1/grab", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  /** Activity → History: the event log across every title, newest first, paged by next_before. */
  history: (f: HistoryFilter = {}) => {
    const qs = new URLSearchParams();
    for (const [k, v] of Object.entries(f)) if (v !== undefined && v !== "") qs.set(k, String(v));
    const q = qs.toString();
    return req<HistoryPage>(`/api/v1/history${q ? `?${q}` : ""}`);
  },
  /** The files imported most recently and where each went (History's "Files imported"). */
  importedFiles: () => req<{ imports: ImportRecord[] }>("/api/v1/history?view=imports").then((r) => r.imports),
  // The blocklist across every media type, global entries included, newest first.
  blocklistAll: (opts: { type?: BlockType | ""; q?: string; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    if (opts.type) p.set("type", opts.type);
    if (opts.q) p.set("q", opts.q);
    if (opts.limit) p.set("limit", String(opts.limit));
    if (opts.offset) p.set("offset", String(opts.offset));
    return req<{ items: BlocklistRow[]; total: number }>(`/api/v1/blocklist?${p}`);
  },
  unblockAny: (id: number) => req<void>(`/api/v1/blocklist/${id}`, { method: "DELETE" }),
  reviews: () => req<{ reviews: ImportReview[] }>("/api/v1/reviews").then((r) => r.reviews),
  // One show's held downloads (the series page's files it couldn't place).
  seriesReviews: (id: number) =>
    req<{ reviews: ImportReview[] }>(`/api/v1/reviews?media_type=series&expected_id=${id}`)
      .then((r) => (r.reviews ?? []).filter((rv) => rv.media_type === "series" && rv.expected_id === id)),
  // findAnother: once the release is blocklisted, search the title it was grabbed for again.
  rejectReview: (id: number, findAnother = false) =>
    req<{ status: string; searching?: boolean; search_error?: string } & Partial<JobRef>>(`/api/v1/reviews/${id}/reject`, findAnother
      ? { method: "POST", body: JSON.stringify({ find_another: true }) }
      : { method: "POST" }),
  dismissReview: (id: number) => req<{ status: string }>(`/api/v1/reviews/${id}/dismiss`, { method: "POST" }),
  // Clears an "import keeps failing" review so the import sweep tries the download again.
  retryReview: (id: number) => req<{ status: string }>(`/api/v1/reviews/${id}/retry`, { method: "POST" }),
  reviewFiles: (id: number) => req<{ files: ReviewFile[]; truncated: boolean }>(`/api/v1/reviews/${id}/files`),
  // Imports a held show download by mapping its files to episodes by hand, as a job
  // (202 with job_id; the same job when the review is already importing).
  mapReview: (id: number, seriesId: number, files: { rel_path: string; season: number; episodes: number[] }[]) =>
    req<ReviewImportStarted>(`/api/v1/reviews/${id}/map`, { method: "POST", body: JSON.stringify({ series_id: seriesId, files }) }),
  bulkReviews: (ids: number[], action: "dismiss" | "reject") =>
    req<{ done: number; failed: { id: number; error: string }[] }>("/api/v1/reviews/bulk", { method: "POST", body: JSON.stringify({ ids, action }) }),
  // targetKind names what targetId is; the server refuses one that isn't the review's own
  // kind. What it can refuse it refuses at once; the import itself is a job (202).
  importReview: (id: number, targetId?: number, targetKind?: ReviewKind) =>
    req<ReviewImportStarted>(`/api/v1/reviews/${id}/import`, { method: "POST", body: JSON.stringify({ target_id: targetId ?? 0, target_kind: targetKind ?? "" }) }),
  // truncated: more items matched than the server lists — the picker asks for a filter.
  reviewTargets: (id: number, q: string) =>
    req<{ targets: ReviewTarget[]; truncated?: boolean }>(`/api/v1/reviews/${id}/targets?q=${encodeURIComponent(q)}`),

  movies: () => req<{ movies: MovieSummary[]; metadata_available: boolean; client_health?: QueueHealth }>("/api/v1/movies"),
  /** Only the films downloading right now (id: just that one) — what the grid and the movie page poll. */
  movieDownloads: (id?: number) =>
    req<{ downloads: MovieDownloadRow[]; client_health?: QueueHealth }>(`/api/v1/movies/downloads${id ? `?id=${id}` : ""}`),
  lookupMovies: (q: string) =>
    req<{ results: MovieLookup[] }>(`/api/v1/movies/lookup?q=${encodeURIComponent(q)}`).then((r) => r.results),
  settings: () => req<AppSettings>("/api/v1/settings"),
  updateSettings: (body: Partial<AppSettings>) =>
    req<AppSettings>("/api/v1/settings", { method: "PUT", body: JSON.stringify(body) }),
  recycleStats: () => req<RecycleStats>("/api/v1/recycle"),
  recycleMode: () => req<RecycleMode>("/api/v1/recycle/mode"),
  // A title's stored search attempts, newest first (staff). since is unix ms.
  searches: (kind: SearchAttempt["media_type"], id: number, opts: { since?: number; limit?: number } = {}) => {
    const p = new URLSearchParams({ kind, id: String(id) });
    if (opts.since) p.set("since", String(opts.since));
    if (opts.limit) p.set("limit", String(opts.limit));
    return req<{ attempts: SearchAttempt[] }>(`/api/v1/searches?${p}`).then((r) => r.attempts ?? []);
  },
  // Background jobs: follow the work a button started (useJob), list and cancel (staff).
  job: (id: number) => req<Job>(`/api/v1/jobs/${id}`),
  jobs: (q: { kind?: string; target?: string; status?: string; limit?: number } = {}) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "") p.set(k, String(v));
    const qs = p.toString();
    return req<{ jobs: Job[] }>(`/api/v1/jobs${qs ? `?${qs}` : ""}`).then((r) => r.jobs);
  },
  // Recurring tasks (System → Status): staff can list them; Run now is admin-only and
  // answers 409 when the task is already running.
  // Catalog the movies folder: new films are added unmonitored on "n/a" unless monitor is
  // set (with a profile; "" = the default), and films already in the library without a
  // file get the one found. One scan at a time: a second answers 409 naming the running
  // job (ApiError body.job_id).
  scanLibrary: (opts?: { monitor: boolean; quality_profile?: string }) =>
    req<{ status: string } & JobRef>("/api/v1/movies/scan", { method: "POST", ...(opts ? { body: JSON.stringify(opts) } : {}) }),
  moviesUnmatched: () => req<{ unmatched: UnmatchedFolder[] }>("/api/v1/movies/unmatched").then((r) => r.unmatched),
  importMovieFolder: (folder: string, tmdb_id: number) =>
    req<{ status: string }>("/api/v1/movies/import", { method: "POST", body: JSON.stringify({ folder, tmdb_id }) }),
  libraryPaths: () => req<LibraryPaths>("/api/v1/system/library"),
  audioServer: () => req<AudioServerAdmin>("/api/v1/audioserver"),
  setAudioServer: (body: { enabled?: boolean; public_url?: string; trace_hours?: number }) => req<AudioServerAdmin>("/api/v1/audioserver", { method: "PUT", body: JSON.stringify(body) }),
  setAudioUser: (id: number, allowed: boolean) => req<AudioServerAdmin>(`/api/v1/audioserver/users/${id}`, { method: "PUT", body: JSON.stringify({ allowed }) }),
  revokeAudioDevice: (id: string) => req<void>(`/api/v1/audioserver/devices/${encodeURIComponent(id)}`, { method: "DELETE" }),
  audioListening: (days = 30, userId?: number) => req<AudioListening>(`/api/v1/audioserver/listening?days=${days}${userId ? `&user_id=${userId}` : ""}`),
  audioImportUpload: async (file: File): Promise<AudioImportPreview> => {
    const fd = new FormData();
    fd.append("file", file);
    // Through send, not req: the browser sets the multipart Content-Type itself.
    const res = await send("/api/v1/audioserver/import", { method: "POST", body: fd });
    return res.json();
  },
  audioImportApply: (userMap: Record<string, number>) => req<AudioImportResult>("/api/v1/audioserver/import/apply", { method: "POST", body: JSON.stringify({ user_map: userMap }) }),
  myAudio: () => req<MyAudio>("/api/v1/me/audio"),
  setAudioPassword: (password: string, signOutDevices: boolean, proof: { current_password?: string; code?: string } = {}) =>
    req<MyAudio>("/api/v1/me/audio/password", { method: "PUT", body: JSON.stringify({ password, sign_out_devices: signOutDevices, ...proof }) }),
  removeAudioPassword: () => req<MyAudio>("/api/v1/me/audio/password", { method: "DELETE" }),
  myAudioListening: (days = 30) => req<AudioListening>(`/api/v1/me/audio/listening?days=${days}`),
  acceptAudioJump: (item: string) => req<{ position: number }>("/api/v1/me/audio/accept", { method: "POST", body: JSON.stringify({ item }) }),
  revokeMyDevice: (id: string) => req<void>(`/api/v1/me/audio/devices/${encodeURIComponent(id)}`, { method: "DELETE" }),
  audioHistory: (item: string) => req<{ history: AudioHistoryEntry[] }>(`/api/v1/me/audio/history?item=${encodeURIComponent(item)}`),
  restoreAudioPlace: (item: string, historyId: number) => req<{ position: number }>("/api/v1/me/audio/restore", { method: "POST", body: JSON.stringify({ item, history_id: historyId }) }),
  audioShelves: () => req<{ shelves: AudioShelf[] }>("/api/v1/me/audio/shelves"),
  audioLibrary: (o: { q?: string; sort?: AudioLibrarySort; filter?: AudioLibraryFilter; page?: number; limit?: number } = {}) => {
    const qs = new URLSearchParams();
    for (const [k, v] of Object.entries(o)) if (v !== undefined && v !== "") qs.set(k, String(v));
    const s = qs.toString();
    return req<AudioLibraryPage>(`/api/v1/me/audio/library${s ? `?${s}` : ""}`);
  },
  audioItem: (key: string) => req<AudioItemDetail>(`/api/v1/me/audio/items/${encodeURIComponent(key)}`),
  audioPlay: (key: string, device: { device_id?: string; device_name?: string } = {}) =>
    req<AudioPlayStart>(`/api/v1/me/audio/items/${encodeURIComponent(key)}/play`, { method: "POST", body: JSON.stringify(device) }),
  audioSync: (sid: string, r: AudioSyncReport) =>
    req<AudioSyncResult>(`/api/v1/me/audio/sessions/${encodeURIComponent(sid)}/sync`, { method: "POST", body: JSON.stringify(r) }),
  audioClose: (sid: string, r: AudioSyncReport) =>
    req<AudioSyncResult>(`/api/v1/me/audio/sessions/${encodeURIComponent(sid)}/close`, { method: "POST", body: JSON.stringify(r) }),
  /** Close from pagehide/visibilitychange, when a fetch may be cut off: true if the browser queued it. */
  audioCloseBeacon: (sid: string, r: AudioSyncReport): boolean =>
    typeof navigator !== "undefined" && typeof navigator.sendBeacon === "function" &&
    navigator.sendBeacon(`/api/v1/me/audio/sessions/${encodeURIComponent(sid)}/close`, new Blob([JSON.stringify(r)], { type: "application/json" })),
  audioBookmarks: (key: string) => req<{ bookmarks: AudioBookmark[] }>(`/api/v1/me/audio/items/${encodeURIComponent(key)}/bookmarks`),
  addAudioBookmark: (key: string, time: number, title = "") =>
    req<AudioBookmark>(`/api/v1/me/audio/items/${encodeURIComponent(key)}/bookmarks`, { method: "POST", body: JSON.stringify({ time, title }) }),
  deleteAudioBookmark: (key: string, time: number) =>
    req<void>(`/api/v1/me/audio/items/${encodeURIComponent(key)}/bookmarks/${Math.floor(time)}`, { method: "DELETE" }),
  undiscardAudio: (item: string) => req<{ position: number }>("/api/v1/me/audio/undiscard", { method: "POST", body: JSON.stringify({ item }) }),
  dismissAudioOffer: (item: string, historyId: number) => req<void>("/api/v1/me/audio/dismiss", { method: "POST", body: JSON.stringify({ item, history_id: historyId }) }),
  audiobookDownloadURL: (bookId: number, versionId = 0) => `/api/v1/books/${bookId}/audiobook${versionId ? `?version=${versionId}` : ""}`,
  setupState: () => req<SetupState>("/api/v1/setup"),
  completeSetup: () => req<{ status: string }>("/api/v1/setup/complete", { method: "POST" }),
  restartApp: () => req<{ status: string }>("/api/v1/system/restart", { method: "POST" }),
  pendingRestart: () => req<PendingRestart>("/api/v1/system/pending-restart"),
  // create: make any missing folder instead of refusing it (the "Create it" button).
  setLibraryPaths: (body: Partial<LibraryPaths> & { create?: boolean }) => req<LibraryPaths>("/api/v1/system/library", { method: "PUT", body: JSON.stringify(body) }),
  checkLibraryFolder: (path: string, kind: keyof LibraryPaths, downloads?: string) =>
    req<FolderCheck>(`/api/v1/system/library/check?kind=${kind}&path=${encodeURIComponent(path)}${downloads ? `&downloads=${encodeURIComponent(downloads)}` : ""}`),
  browseFolders: (path?: string) => req<BrowseResult>(`/api/v1/system/browse${path ? `?path=${encodeURIComponent(path)}` : ""}`),
  // search_on_add applies to this add only; the global default changes only in Settings
  // (or through updateSettings when the dialog's "Remember as default" is ticked).
  addMovie: (body: { tmdb_id: number; quality_profile: string; monitored?: boolean; search_on_add?: boolean; min_availability?: MinAvailability; folder_id?: number }) =>
    req<Movie>("/api/v1/movies", { method: "POST", body: JSON.stringify(body) }),
  // Several films in one call (a collection's Add all): films already in the library are
  // skipped, each new one's search is queued. search defaults to the search-on-add setting.
  bulkAddMovies: (body: { tmdb_ids: number[]; quality_profile?: string; min_availability?: MinAvailability; monitored?: boolean; search?: boolean }) =>
    req<{ results: BulkAddResult[]; added: number; existing: number; failed: number }>("/api/v1/movies/bulk-add", { method: "POST", body: JSON.stringify(body) }),
  previewTorrent: (torrent: string) =>
    req<TorrentPreview>("/api/v1/grab/preview", { method: "POST", body: JSON.stringify({ torrent }) }),
  grabMovieTorrent: (id: number, torrent: string, filename: string, title: string, versionId = 0) =>
    req<{ status: string }>(`/api/v1/movies/${id}/grabtorrent`, { method: "POST", body: JSON.stringify({ torrent, filename, title, version_id: versionId }) }),
  grabSeriesTorrent: (id: number, torrent: string, filename: string, title: string) =>
    req<{ status: string }>(`/api/v1/series/${id}/grabtorrent`, { method: "POST", body: JSON.stringify({ torrent, filename, title }) }),
  bookSeries: (id: number) => req<BookSeries>(`/api/v1/books/${id}/series`),
  addMissingInSeries: (id: number, quality_profile?: string) =>
    req<{ added: number; skipped: number }>(`/api/v1/books/${id}/series/add-missing`, { method: "POST", body: JSON.stringify({ quality_profile: quality_profile ?? "" }) }),
  bookAuthorDetail: (key: string) => req<BookAuthor>(`/api/v1/books/discover/authors/${encodeURIComponent(key)}`),
  bookDiscoverSimilar: (key: string) => req<{ books: BookDiscoverCard[] }>(`/api/v1/books/discover/similar?key=${encodeURIComponent(key)}`).then((r) => r.books),
  bookAuthorImages: () => req<{ images: Record<string, string>; pending: number }>("/api/v1/books/authors/images"),
  backfillBookSeries: () =>
    req<{ status: string } & JobRef>("/api/v1/books/series-backfill", { method: "POST" }),
  grabBookTorrent: (id: number, torrent: string, filename: string, title: string, versionId?: number) =>
    req<{ status: string }>(`/api/v1/books/${id}/grabtorrent`, { method: "POST", body: JSON.stringify({ torrent, filename, title, version_id: versionId || 0 }) }),
  /** leftovers: the artwork and metadata paths the delete preview listed; only those can go. */
  deleteMovie: (id: number, deleteFiles?: boolean, cancelDownloads?: boolean, leftovers?: string[]) => {
    const q = new URLSearchParams();
    if (deleteFiles) q.set("delete_files", "true");
    if (cancelDownloads) q.set("cancel_downloads", "true");
    const qs = q.toString();
    return req<void>(`/api/v1/movies/${id}${qs ? `?${qs}` : ""}`, {
      method: "DELETE",
      ...(deleteFiles && leftovers?.length ? { body: JSON.stringify({ leftovers }) } : {}),
    });
  },
  movieDeletePreview: (id: number) => req<MovieDeletePreview>(`/api/v1/movies/${id}/delete-preview`),
  searchMovie: (id: number) =>
    req<{ status: string; started_at_ms?: number } & MovieQueuedRef>(`/api/v1/movies/${id}/search`, { method: "POST" }),
  movieSearchQueue: () => req<MovieSearchQueue>("/api/v1/movies/search-queue"),
  moviesMissing: () => req<MoviesMissing>("/api/v1/movies/wanted?tab=missing"),
  moviesCutoff: () => req<{ rows: MovieWantedRow[]; queue_known: boolean }>("/api/v1/movies/wanted?tab=cutoff"),
  /** Queue searches for many movies at the throttled rate: missing searches, or upgrades under one shared upgrade budget. */
  bulkMovieSearch: (ids: number[], kind: "missing" | "upgrade") =>
    req<{ queued: number; duplicates: number }>("/api/v1/movies/search", { method: "POST", body: JSON.stringify({ ids, kind }) }),
  movie: (id: number) => req<Movie>(`/api/v1/movies/${id}`),
  movieCollection: (id: number) =>
    req<{ name: string; members: CollectionMember[] }>(`/api/v1/movies/${id}/collection`),

  series: () => req<{ series: Series[]; metadata_available: boolean }>("/api/v1/series"),
  lookupSeries: (q: string) =>
    req<{ results: SeriesLookup[] }>(`/api/v1/series/lookup?q=${encodeURIComponent(q)}`).then((r) => r.results),
  // monitor is a monitoring preset; left out, the server uses Settings' default.
  addSeries: (body: { tmdb_id: number; quality_profile?: string; monitored?: boolean; search_on_add?: boolean; monitor?: string; monitor_new_seasons?: boolean; folder_id?: number }) =>
    req<Series>("/api/v1/series", { method: "POST", body: JSON.stringify(body) }),
  seriesDetail: (id: number) => req<Series>(`/api/v1/series/${id}`),
  seriesDownloads: (id: number) => req<SeriesEpisodeDownload[]>(`/api/v1/series/${id}/downloads`),
  searchSeries: (id: number) =>
    req<{ status: string; started_at_ms?: number } & JobRef>(`/api/v1/series/${id}/search`, { method: "POST" }),
  // One scoped search of episodes picked in one season (at most 200).
  searchSeriesEpisodes: (id: number, episodes: { season: number; episode: number }[]) =>
    req<{ status: string; episodes: number } & JobRef>(`/api/v1/series/${id}/search`, { method: "POST", body: JSON.stringify({ episodes }) }),
  // Monitors or unmonitors several of a show's episodes at once; their seasons follow.
  setEpisodesMonitored: (id: number, episode_ids: number[], monitored: boolean) =>
    req<{ updated: number; monitored: boolean }>(`/api/v1/series/${id}/monitor/episodes`, { method: "PUT", body: JSON.stringify({ episode_ids, monitored }) }),
  seriesReleases: (id: number, season?: number, episode?: number) => {
    const q = new URLSearchParams();
    // Season 0 is Specials, not "no season" — send it whenever it's given.
    if (season !== undefined) q.set("season", String(season));
    if (episode !== undefined) q.set("episode", String(episode));
    const qs = q.toString();
    return req<ReleaseList>(`/api/v1/series/${id}/releases${qs ? `?${qs}` : ""}`);
  },
  // The token carries the search it came from (whole show, season or episode); the server
  // skips the import quality gate only for episodes inside that scope.
  grabSeries: (id: number, body: { token: string }) =>
    req<{ status: string }>(`/api/v1/series/${id}/grab`, { method: "POST", body: JSON.stringify(body) }),
  autoGrabSeries: (id: number, season: number, episode: number) =>
    req<{ status: string } & JobRef>(`/api/v1/series/${id}/autograb`, { method: "POST", body: JSON.stringify({ season, episode }) }),
  refreshSeries: (id: number) => req<Series>(`/api/v1/series/${id}/refresh`, { method: "POST" }),
  // Bulk refresh: re-pulls metadata and rescans the disk for every series. Runs in the
  // background — the response only reports how many were queued.
  refreshAllSeries: () => req<{ queued: number } & JobRef>(`/api/v1/series/refresh`, { method: "POST" }),
  // Requests
  /** listen: this person may play audiobooks in Arrmada (the audiobook server is on and they're allowed). */
  myBooks: () => req<{ books: MyBook[]; requests: MyRequest[]; listen?: boolean; queue_known?: boolean }>("/api/v1/me/books"),
  // A plain link, not a fetch: the browser saves the file with the server's filename.
  ebookDownloadURL: (bookId: number) => `/api/v1/books/${bookId}/ebook`,
  // Without a section: every request the viewer may see, newest first. With one: a page
  // of that section (default 50), plus counts for every section.
  // ready_within_days keeps ready requests stamped in the last N days; mine=1 narrows staff
  // to their own and followed requests too (everyone else always is).
  requests: (opts: { section?: RequestSection; limit?: number; offset?: number; media_type?: string; q?: string; status?: string; ready_within_days?: number; mine?: 1 } = {}) => {
    const qs = new URLSearchParams();
    for (const [k, v] of Object.entries(opts)) if (v !== undefined && v !== "") qs.set(k, String(v));
    const s = qs.toString();
    return req<RequestList>(`/api/v1/requests${s ? `?${s}` : ""}`);
  },
  getRequest: (id: number) =>
    req<{ request: MediaRequest; client_health?: QueueHealth }>(`/api/v1/requests/${id}`),
  // Stop following a request someone else made: no more notifications, and it leaves
  // your list. The request itself stays.
  unsubscribeRequest: (id: number) =>
    req<void>(`/api/v1/requests/${id}/subscription`, { method: "DELETE" }),
  // Approve or decline several at once; each is decided on its own, so one failure
  // doesn't stop the rest.
  bulkRequests: (body: { action: "approve" | "decline"; ids: number[]; quality_profile?: string; reason?: string }) =>
    req<{ results: { id: number; ok: boolean; error?: string }[] }>("/api/v1/requests/bulk", { method: "POST", body: JSON.stringify(body) }),
  // Returns 200 even for already-requested titles: subscribed=true means "you were
  // attached to an existing request and will be notified too". Requesting a declined
  // title resurrects it as pending. A series request may name `seasons` (empty or absent:
  // the whole show); seasons already covered by another request are followed instead,
  // and asking only for seasons already on disk answers 409.
  // formats (books): read / listen / both; left out, the server uses the owner's default.
  createRequest: (body: { media_type: "movie" | "series" | "book"; tmdb_id?: number; ol_key?: string; author?: string; title: string; year: number; poster_url?: string; overview?: string; quality_profile?: string; note?: string; formats?: "ebook" | "audiobook" | "both"; seasons?: number[] }) =>
    req<{ request: MediaRequest; subscribed: boolean } | MediaRequest>("/api/v1/requests", { method: "POST", body: JSON.stringify(body) })
      .then((r): { request: MediaRequest; subscribed: boolean } => ("request" in r ? r : { request: r, subscribed: false })),
  // body.quality_profile: "" or absent keeps the request's own profile (or the default).
  // body.seasons trims a series request to a subset of what it asked for (the rest isn't
  // approved); empty or absent approves it as asked. A plain string is still accepted as
  // the profile, for older callers.
  approveRequest: (id: number, body: string | { quality_profile?: string; seasons?: number[] } = {}) => {
    const b = typeof body === "string" ? { quality_profile: body } : body;
    return req<MediaRequest>(`/api/v1/requests/${id}/approve`, {
      method: "POST",
      body: JSON.stringify({ quality_profile: b.quality_profile ?? "", ...(b.seasons?.length ? { seasons: b.seasons } : {}) }),
    });
  },
  myQuota: () => req<MyQuota>("/api/v1/me/quota"),
  // reason (≤ 280 characters) is what the requester is told.
  declineRequest: (id: number, reason = "") =>
    req<{ status: string }>(`/api/v1/requests/${id}/decline`, { method: "POST", body: JSON.stringify({ reason }) }),
  deleteRequest: (id: number) =>
    req<void>(`/api/v1/requests/${id}`, { method: "DELETE" }),

  // Discover
  discoverTrending: (media?: string) =>
    req<{ items: DiscoverCard[] }>(`/api/v1/discover/trending${media ? `?media=${media}` : ""}`).then((r) => r.items),
  discoverPopular: (media: string) =>
    req<{ items: DiscoverCard[] }>(`/api/v1/discover/popular?media=${media}`).then((r) => r.items),
  // media is optional: no arg keeps the movie default the backend already assumes,
  // "series" asks for shows airing soon.
  discoverUpcoming: (media?: string) =>
    req<{ items: DiscoverCard[] }>(`/api/v1/discover/upcoming${media ? `?media=${media}` : ""}`).then((r) => r.items),
  discoverRecommended: () =>
    req<{ items: DiscoverCard[] }>(`/api/v1/discover/recommended`).then((r) => r.items),
  discoverRow: (kind: "now_playing" | "top_rated" | "anime" | "hidden_gems" | "region", media?: string) =>
    req<{ items: DiscoverCard[] }>(`/api/v1/discover/rows/${kind}${media ? `?media=${media}` : ""}`).then((r) => r.items),
  discoverProviders: (media: string) =>
    req<{ providers: WatchProvider[] }>(`/api/v1/discover/providers?media=${media}`).then((r) => r.providers),
  discoverProviderNew: (media: string, id: number) =>
    req<{ items: DiscoverCard[] }>(`/api/v1/discover/provider?media=${media}&id=${id}`).then((r) => r.items),
  discoverBecause: () => req<{ rows: DiscoverRow[] }>(`/api/v1/discover/because`).then((r) => r.rows),
  // "Complete the <name>" rows: one per collection the library has started.
  discoverCollections: () => req<{ rows: CollectionRow[] }>(`/api/v1/discover/collections`).then((r) => r.rows),
  discoverCollection: (id: number) => req<CollectionDetail>(`/api/v1/discover/collection/${id}`),
  // What just arrived in the library (Arrmada's imports, plus Plex's when it's set up).
  discoverRecentlyAdded: () => req<{ items: DiscoverCard[] }>(`/api/v1/discover/recently-added`).then((r) => r.items),
  discoverByGenre: (media: string, genre: number) =>
    req<{ items: DiscoverCard[] }>(`/api/v1/discover?media=${media}&genre=${genre}`).then((r) => r.items),
  discoverGenres: (media: string) =>
    req<{ genres: Genre[] }>(`/api/v1/discover/genres?media=${media}`).then((r) => r.genres),
  mediaDetail: (media: string, tmdbId: number) =>
    req<MediaDetail>(`/api/v1/media/${media}/${tmdbId}`),
  /** Which seasons of a show exist and, for each, what asking for it would mean. */
  seriesSeasons: (tmdbId: number) =>
    req<{ seasons: SeriesSeason[] }>(`/api/v1/media/series/${tmdbId}/seasons`).then((r) => r.seasons),
  // The search dropdown: the first page's titles and people.
  discoverSearch: (q: string) =>
    req<{ items: DiscoverCard[]; people?: PersonResult[] }>(`/api/v1/discover/search?q=${encodeURIComponent(q)}`),
  discoverPerson: (id: number) => req<PersonDetail>(`/api/v1/discover/person/${id}`),
  // One page of a search, for the results grid that keeps loading as it scrolls.
  discoverSearchPage: (q: string, page: number) =>
    req<DiscoverPage>(`/api/v1/discover/search?q=${encodeURIComponent(q)}&page=${page}`),
  // One page of a browse grid; query is the grid's filters (list, media, genre, …) as a query string.
  discoverBrowse: (query: string, page: number) =>
    req<DiscoverPage>(`/api/v1/discover/browse?${query}${query ? "&" : ""}page=${page}`),

  books: () => req<{ books: Book[]; metadata_available: boolean; metadata_source?: BookSource; upgradable?: number }>("/api/v1/books"),
  startBookUpgrade: () => req<{ started: boolean; status: BookUpgradeStatus } & JobRef>("/api/v1/books/upgrade", { method: "POST" }),
  bookUpgradeStatus: () => req<BookUpgradeStatus>("/api/v1/books/upgrade"),
  // source: "openlibrary" asks Open Library explicitly ("show Open Library results too");
  // the response says which catalogue the default search uses.
  lookupBooks: (q: string, source?: "openlibrary" | "hardcover") =>
    req<{ results: BookLookup[]; source: BookSource }>(`/api/v1/books/lookup?q=${encodeURIComponent(q)}${source ? `&source=${source}` : ""}`),
  addBook: (body: { ol_key: string; quality_profile?: string; monitored?: boolean; search_on_add?: boolean; title?: string; author?: string; year?: number; cover_url?: string }) =>
    req<Book>("/api/v1/books", { method: "POST", body: JSON.stringify(body) }),
  bookDetail: (id: number) => req<Book>(`/api/v1/books/${id}`),
  searchBook: (id: number) => req<{ status: string; started_at_ms?: number } & JobRef>(`/api/v1/books/${id}/search`, { method: "POST" }),
  refreshBook: (id: number) => req<Book>(`/api/v1/books/${id}/refresh`, { method: "POST" }),
  bookReleases: (id: number) => req<ReleaseList>(`/api/v1/books/${id}/releases`),
  grabBook: (id: number, body: { token: string; version_id?: number }) =>
    req<{ status: string }>(`/api/v1/books/${id}/grab`, { method: "POST", body: JSON.stringify(body) }),
  bookManualImportList: (id: number) =>
    req<ManualImportList<BookImportCandidate>>(`/api/v1/books/${id}/manualimport`),
  bookManualImport: (id: number, path: string, versionId?: number) =>
    req<{ status: string }>(`/api/v1/books/${id}/manualimport`, { method: "POST", body: JSON.stringify({ path, version_id: versionId || 0 }) }),
  addAudioVersion: (id: number, body: { label: string; terms: string[]; monitored: boolean }) =>
    req<AudioVersion>(`/api/v1/books/${id}/audio-versions`, { method: "POST", body: JSON.stringify(body) }),
  updateAudioVersion: (id: number, vid: number, body: { label?: string; terms?: string[]; monitored?: boolean; narrator?: string }) =>
    req<AudioVersion>(`/api/v1/books/${id}/audio-versions/${vid}`, { method: "PUT", body: JSON.stringify(body) }),
  deleteAudioVersion: (id: number, vid: number, deleteFiles: boolean) =>
    req<void>(`/api/v1/books/${id}/audio-versions/${vid}${deleteFiles ? "?delete_files=true" : ""}`, { method: "DELETE" }),
  deleteAudioVersionFile: (id: number, vid: number) =>
    req<{ status: string }>(`/api/v1/books/${id}/audio-versions/${vid}/file`, { method: "DELETE" }),
  searchAudioVersion: (id: number, vid: number) =>
    req<{ grabbed: boolean; message?: string; outcome?: SearchOutcome }>(`/api/v1/books/${id}/audio-versions/${vid}/search`, { method: "POST" }),
  // Renames the book's editions to the book naming scheme; skipped says which couldn't be, and why.
  renameBook: (id: number) => req<{ renamed: number; skipped?: { kind: string; reason?: string }[] }>(`/api/v1/books/${id}/rename`, { method: "POST" }),
  deleteBookFile: (id: number, edition: "ebook" | "audiobook") =>
    req<{ status: string }>(`/api/v1/books/${id}/file?edition=${edition}`, { method: "DELETE" }),
  scanBooks: () => req<{ status: string } & JobRef>("/api/v1/books/scan", { method: "POST" }),
  // The manual sweep: every monitored book missing an edition its profile wants.
  startBookSweep: () => req<{ started: boolean; status: BookSweepStatus } & JobRef>("/api/v1/books/search-missing", { method: "POST" }),
  bookSweepStatus: () => req<BookSweepStatus>("/api/v1/books/search-missing"),
  bookEditionFiles: (id: number, edition: "ebook" | "audiobook") =>
    req<{ files: BookFileEntry[] }>(`/api/v1/books/${id}/edition-files?edition=${edition}`).then((r) => r.files),
  mergeAudiobook: (id: number) =>
    req<{ status: string } & JobRef>(`/api/v1/books/${id}/merge-audiobook`, { method: "POST" }),
  bookCovers: (id: number) =>
    req<{ covers: string[] }>(`/api/v1/books/${id}/covers`).then((r) => r.covers),
  setBookCover: (id: number, url: string) =>
    req<{ cover_url: string }>(`/api/v1/books/${id}/cover`, { method: "PUT", body: JSON.stringify({ url }) }).then((r) => r.cover_url),
  // Books Discover
  bookDiscoverBrowse: (kind: "trending" | "new_releases" | "top_rated" | "popular") =>
    req<{ books: BookDiscoverCard[] }>(`/api/v1/books/discover/browse/${kind}`).then((r) => r.books),
  bookDiscoverRecommended: () =>
    req<{ rows: BookRecommendedRow[] }>("/api/v1/books/discover/recommended").then((r) => r.rows),
  bookDiscoverSearch: (q: string, source?: "openlibrary" | "hardcover") =>
    req<{ authors: BookAuthor[]; books: BookDiscoverCard[]; source: BookSource }>(`/api/v1/books/discover/search?q=${encodeURIComponent(q)}${source ? `&source=${source}` : ""}`),
  searchBookAuthors: (q: string) =>
    req<{ authors: BookAuthor[] }>(`/api/v1/books/discover/authors?q=${encodeURIComponent(q)}`).then((r) => r.authors),
  addAuthor: (body: { author_key: string; name?: string; quality_profile?: string; monitored?: boolean; search_on_add?: boolean }) =>
    req<{ added: number; skipped: number; total: number; author_id?: number }>("/api/v1/books/author", { method: "POST", body: JSON.stringify(body) }),
  bookAuthorWorks: (key: string) =>
    req<{ author_key: string; books: BookDiscoverCard[] }>(`/api/v1/books/discover/authors/${encodeURIComponent(key)}/works`).then((r) => r.books),
  bookDiscoverSubject: (name: string) =>
    req<{ subject: string; books: BookDiscoverCard[] }>(`/api/v1/books/discover/subjects/${encodeURIComponent(name)}`).then((r) => r.books),
  bookDiscoverDetail: (key: string) =>
    req<BookMeta>(`/api/v1/books/discover/detail?key=${encodeURIComponent(key)}`),
  uploadBookCover: async (id: number, file: File): Promise<string> => {
    const fd = new FormData();
    fd.append("file", file);
    const res = await send(`/api/v1/books/${id}/cover`, { method: "POST", body: fd });
    return ((await res.json()) as { cover_url: string }).cover_url;
  },
  // Tell the Hardcover re-match to leave a book on its current entry (or to try again).
  keepBookCatalogue: (id: number, keep: boolean) =>
    req<{ keep: boolean }>(`/api/v1/books/${id}/keep-catalogue`, { method: "PUT", body: JSON.stringify({ keep }) }),
  setBookMonitored: (id: number, monitored: boolean) =>
    req<{ monitored: boolean }>(`/api/v1/books/${id}/monitor`, { method: "PUT", body: JSON.stringify({ monitored }) }),
  setBookProfile: (id: number, quality_profile: string) =>
    req<{ quality_profile: string }>(`/api/v1/books/${id}/profile`, { method: "PUT", body: JSON.stringify({ quality_profile }) }),
  deleteBook: (id: number, deleteFiles?: boolean) =>
    req<void>(`/api/v1/books/${id}${deleteFiles ? "?delete_files=true" : ""}`, { method: "DELETE" }),
  bookHistory: (id: number) =>
    req<{ events: MovieEvent[] }>(`/api/v1/books/${id}/history`).then((r) => r.events),

  // ---- Music ----
  artists: () => req<{ artists: Artist[] }>("/api/v1/music/artists").then((r) => r.artists ?? []),
  lookupArtists: (q: string) =>
    req<{ results: ArtistLookup[] }>(`/api/v1/music/lookup?q=${encodeURIComponent(q)}`).then((r) => r.results ?? []),
  addArtist: (body: {
    mbid: string;
    quality_profile?: string;
    monitored?: boolean;
    /** Which of today's albums are wanted. */
    monitor?: "all" | "future" | "latest" | "none";
    monitor_new?: "" | "all" | "none";
    release_types?: { primary: string[]; secondary: string[] };
    /** Search the wanted albums straight away (left out: Settings' search on add). */
    search_now?: boolean;
  }) =>
    req<Artist>("/api/v1/music/artists", { method: "POST", body: JSON.stringify(body) }),
  scanMusic: () => req<{ status: string } & JobRef>("/api/v1/music/scan", { method: "POST" }),
  artistDetail: (id: number) => req<Artist>(`/api/v1/music/artists/${id}`),
  refreshArtist: (id: number) => req<Artist>(`/api/v1/music/artists/${id}/refresh`, { method: "POST" }),
  grabDiscography: (id: number) =>
    req<{ status: string }>(`/api/v1/music/artists/${id}/discography`, { method: "POST" }),
  setArtistMonitored: (id: number, monitored: boolean) =>
    req<{ monitored: boolean }>(`/api/v1/music/artists/${id}/monitor`, { method: "PUT", body: JSON.stringify({ monitored }) }),
  setArtistProfile: (id: number, quality_profile: string) =>
    req<{ quality_profile: string }>(`/api/v1/music/artists/${id}/profile`, { method: "PUT", body: JSON.stringify({ quality_profile }) }),
  deleteArtist: (id: number) => req<void>(`/api/v1/music/artists/${id}`, { method: "DELETE" }),
  artistHistory: (id: number) =>
    req<{ events: MovieEvent[] }>(`/api/v1/music/artists/${id}/history`).then((r) => r.events ?? []),
  albumDetail: (id: number) => req<MusicAlbum>(`/api/v1/music/albums/${id}`),
  setAlbumMonitored: (id: number, monitored: boolean) =>
    req<{ monitored: boolean }>(`/api/v1/music/albums/${id}/monitor`, { method: "PUT", body: JSON.stringify({ monitored }) }),

  // Subtitles
  subtitleSettings: () => req<SubtitleSettings>("/api/v1/subtitles/settings"),
  updateSubtitleSettings: (body: { movies_auto?: boolean; series_auto?: boolean; languages?: string[]; min_score?: number }) =>
    req<SubtitleSettings>("/api/v1/subtitles/settings", { method: "PUT", body: JSON.stringify(body) }),
  subtitleLibrary: () => req<{ items: SubFileEntry[]; scanning?: boolean; stale?: boolean }>("/api/v1/subtitles/library"),
  subtitleCoverage: () => req<SubtitleCoverage>("/api/v1/subtitles/coverage"),
  subtitleRescan: () => req<{ started: boolean }>("/api/v1/subtitles/library/rescan", { method: "POST" }),
  // TV rolled up per show. The flat list is one probed row per episode, which at
  // library scale is tens of thousands of rows before the page can render.
  subtitleSeriesGroups: () =>
    req<{ groups: SubSeriesGroup[]; scanning?: boolean; stale?: boolean }>("/api/v1/subtitles/library?media=tv&group=series"),
  subtitleSeriesEpisodes: (seriesID: number) =>
    req<{ items: SubFileEntry[] }>(`/api/v1/subtitles/library?media=tv&series=${seriesID}`).then((r) => r.items),
  subtitleJobs: () => req<{ jobs: SubtitleJob[] }>("/api/v1/subtitles/jobs").then((r) => r.jobs),
  subtitleCancelJob: (id: number) => req<{ status: string }>(`/api/v1/subtitles/jobs/${id}/cancel`, { method: "POST" }),
  subtitleClearQueue: () => req<{ cleared: number }>("/api/v1/subtitles/jobs/clear", { method: "POST" }),
  subtitleLogs: () => req<{ lines: { at: number; level: string; msg: string }[] }>("/api/v1/subtitles/logs").then((r) => r.lines),
  // redo: replace the sidecars already there rather than fill in what's missing.
  subtitleQueueMovie: (id: number, redo = false) => req<SubtitleJob>(`/api/v1/subtitles/library/movies/${id}${redo ? "?redo=1" : ""}`, { method: "POST" }),
  subtitleQueueEpisode: (seriesID: number, season: number, episode: number, redo = false) => req<SubtitleJob>(`/api/v1/subtitles/library/episodes/${seriesID}/${season}/${episode}${redo ? "?redo=1" : ""}`, { method: "POST" }),
  subtitleQueueSeries: (seriesID: number) => req<{ queued: number }>(`/api/v1/subtitles/library/series/${seriesID}`, { method: "POST" }),
  subtitleSweep: (media: "movies" | "tv" = "movies") => req<{ queued: number }>(`/api/v1/subtitles/sweep${media === "tv" ? "?media=tv" : ""}`, { method: "POST" }),
  subtitleModels: () => req<WhisperStatus>("/api/v1/subtitles/models"),
  subtitleDownloadModel: (name: string) => req<{ status: string }>(`/api/v1/subtitles/models/${encodeURIComponent(name)}`, { method: "POST" }),

  scanSeries: () => req<{ status: string } & JobRef>("/api/v1/series/scan", { method: "POST" }),
  seriesUnmatched: () => req<{ unmatched: UnmatchedFolder[] }>("/api/v1/series/unmatched").then((r) => r.unmatched),
  importSeriesFolder: (folder: string, tmdb_id: number) =>
    req<{ status: string }>("/api/v1/series/import", { method: "POST", body: JSON.stringify({ folder, tmdb_id }) }),
  seriesDuplicates: (id: number) =>
    req<{ duplicates: DuplicateEpisodeFile[]; extra_files: number; reclaimable_bytes: number }>(`/api/v1/series/${id}/duplicates`),
  deleteSeriesDuplicate: (id: number, path: string) =>
    req<{ status: string }>(`/api/v1/series/${id}/duplicates`, { method: "DELETE", body: JSON.stringify({ path }) }),
  seriesHistory: (id: number) =>
    req<{ events: MovieEvent[] }>(`/api/v1/series/${id}/history`).then((r) => r.events),
  // versionId ranks for one track (0 = the default track): its profile and edition, with
  // the releases it can't take marked ineligible. Without it the movie is ranked as a whole.
  movieReleases: (id: number, versionId?: number) =>
    req<ReleaseList>(`/api/v1/movies/${id}/releases${versionId === undefined ? "" : `?version=${versionId}`}`),
  /** The editions release names are recognised by, for the version form's picker. */
  movieEditions: () => req<{ editions: string[] }>("/api/v1/movies/editions").then((r) => r.editions),
  blocklist: (id: number) => req<{ blocklist: BlockEntry[] }>(`/api/v1/movies/${id}/blocklist`).then((r) => r.blocklist),
  // Block a search result by its token, or any release by title alone.
  blockRelease: (id: number, body: { token: string; search_again?: boolean } | { title: string; indexer?: string; search_again?: boolean }) =>
    req<{ status: string; search_error?: string } & MovieQueuedRef>(`/api/v1/movies/${id}/blocklist`, { method: "POST", body: JSON.stringify(body) }),
  unblock: (id: number, bid: number) => req<void>(`/api/v1/movies/${id}/blocklist/${bid}`, { method: "DELETE" }),
  setMonitored: (id: number, monitored: boolean) =>
    req<{ monitored: boolean }>(`/api/v1/movies/${id}/monitor`, {
      method: "PUT",
      body: JSON.stringify({ monitored }),
    }),
  deleteMovieFile: (id: number) => req<void>(`/api/v1/movies/${id}/file`, { method: "DELETE" }),
  // Clears the record of a track whose file is gone from disk (version 0 = the default
  // track). Never deletes anything; answers 409 when the file is back on disk.
  forgetMissingFile: (id: number, versionId = 0) =>
    req<{ status: string }>(`/api/v1/movies/${id}/file/forget`, { method: "POST", body: JSON.stringify({ version_id: versionId }) }),
  setQualityProfile: (id: number, quality_profile: string) =>
    req<{ quality_profile: string; downgrade: boolean; downgrade_reason?: string; downgrade_kind?: "smaller" | "different"; downgrade_ceiling?: string; queued?: boolean; position?: number }>(`/api/v1/movies/${id}/profile`, {
      method: "PUT",
      body: JSON.stringify({ quality_profile }),
    }),
  regrabMovie: (id: number) => req<{ status: string } & MovieQueuedRef>(`/api/v1/movies/${id}/regrab`, { method: "POST" }),
  refreshMovie: (id: number) => req<Movie>(`/api/v1/movies/${id}/refresh`, { method: "POST" }),
  movieHistory: (id: number) =>
    req<{ events: MovieEvent[] }>(`/api/v1/movies/${id}/history`).then((r) => r.events),
  setAvailability: (id: number, min_availability: string) =>
    req<{ min_availability: string }>(`/api/v1/movies/${id}/availability`, {
      method: "PUT",
      body: JSON.stringify({ min_availability }),
    }),
  manualImportList: (id: number, path?: string) =>
    req<ManualImportList<ImportCandidate>>(
      `/api/v1/movies/${id}/manualimport${path ? `?path=${encodeURIComponent(path)}` : ""}`,
    ),
  // versionId imports into that track (0 = the default track); absent, the file goes to
  // whichever track its name's resolution and edition route it to.
  manualImport: (id: number, path: string, versionId?: number) =>
    req<{ status: string }>(`/api/v1/movies/${id}/manualimport`, {
      method: "POST",
      body: JSON.stringify(versionId === undefined ? { path } : { path, version_id: versionId }),
    }),
  // Every track's current file and the name the naming scheme gives it; nothing moves.
  renamePreview: (id: number) =>
    req<{ items: MovieRenameItem[]; matches: boolean }>(`/api/v1/movies/${id}/rename`),
  // Applies the previewed items the person ticked; anything changed since is skipped.
  renameMovie: (id: number, items: MovieRenameItem[]) =>
    req<MovieRenameReply>(`/api/v1/movies/${id}/rename`, { method: "POST", body: JSON.stringify({ items }) }),
  // The mass editor: every field commits together or not at all (404 names films that are
  // gone); searches join the throttled movie search queue.
  bulkUpdateMovies: (body: MovieBulkUpdate) =>
    req<MovieBulkUpdateReply>("/api/v1/movies/bulk", { method: "PUT", body: JSON.stringify(body) }),
  bulkRegrabMovies: (ids: number[]) =>
    req<{ queued: number; duplicates: number; errors: { id: number; msg: string }[] }>("/api/v1/movies/bulk/regrab", { method: "POST", body: JSON.stringify({ ids }) }),
  // Per-film results; stops at the first film the recycle bin refuses.
  bulkDeleteMovies: (ids: number[], deleteFiles: boolean, cancelDownloads: boolean) =>
    req<MovieBulkDeleteReply>("/api/v1/movies/bulk", { method: "DELETE", body: JSON.stringify({ ids, delete_files: deleteFiles, cancel_downloads: cancelDownloads }) }),
  // Organize: a dry run over the library (no ids) or a selection; nothing moves.
  organizePreview: (ids?: number[], offset = 0, limit = 200) =>
    req<OrganizePreview>(`/api/v1/movies/organize?ids=${ids?.length ? ids.join(",") : "all"}&offset=${offset}&limit=${limit}`),
  // Starts the previewed renames as a job; 409 when the plan changed since the preview.
  organizeApply: (token: string, ids?: number[]) =>
    req<{ status: string; total: number } & JobRef>("/api/v1/movies/organize", { method: "POST", body: JSON.stringify({ token, ids: ids ?? [] }) }),
  addVersion: (id: number, body: { label: string; quality_profile: string; edition?: string; monitored?: boolean }) =>
    req<MovieVersion>(`/api/v1/movies/${id}/versions`, { method: "POST", body: JSON.stringify(body) }),
  updateVersion: (id: number, vid: number, body: { label: string; quality_profile: string; edition?: string; monitored: boolean }) =>
    req<{ status: string; queued?: boolean; position?: number }>(`/api/v1/movies/${id}/versions/${vid}`, { method: "PUT", body: JSON.stringify(body) }),
  deleteVersion: (id: number, vid: number) =>
    req<void>(`/api/v1/movies/${id}/versions/${vid}`, { method: "DELETE" }),
  deleteVersionFile: (id: number, vid: number) =>
    req<void>(`/api/v1/movies/${id}/versions/${vid}/file`, { method: "DELETE" }),
};

export interface MovieVersion {
  id: number;
  is_default: boolean;
  label: string;
  quality_profile: string;
  edition?: string;
  monitored: boolean;
  has_file: boolean;
  file_path?: string;
  size_bytes?: number;
  file?: MovieFile;
  upgrade_hold?: boolean; // this track's file is kept out of profile-driven upgrades
  // The file's audio languages (detail endpoint only; absent until the file is analysed).
  audio_langs?: AudioLang[];
  audio_lang_fit?: AudioLangFit;
}

export interface CastMember {
  id?: number;
  name: string;
  character?: string;
  profile_url?: string;
}

export interface CollectionMember {
  tmdb_id: number;
  title: string;
  year: number;
  poster_url?: string;
  overview?: string;
  vote_average?: number;
  in_library: boolean;
  library_id?: number; // the film's id in the library, when it's there
}

export interface MovieExtra {
  genres?: string[];
  studios?: string[];
  original_language?: string;
  certification?: string;
  backdrop_url?: string;
  release_date?: string;
  collection_id?: number;
  collection_name?: string;
  vote_average?: number;
  cast?: CastMember[];
}

export interface DuplicateCopy {
  path: string;
  filename: string;
  size_bytes: number;
}
export interface DuplicateEpisodeFile {
  season: number;
  episode: number;
  keeping: DuplicateCopy;
  extras: DuplicateCopy[];
}

// ---- Music ----
export interface ArtistLookup {
  mbid: string;
  name: string;
  sort_name?: string;
  disambiguation?: string;
  country?: string;
  type?: string;
  /** The alias the search matched when the name itself doesn't show it ("also known as"). */
  aka?: string;
}
export interface ArtistStats {
  albums: number;
  tracks: number;
  have_tracks: number;
  size_bytes: number;
  /** Album counts, from each album's target edition. An unlisted album is unknown, never complete. */
  albums_monitored?: number;
  albums_complete?: number;
  albums_unlisted?: number;
  albums_with_files?: number;
  /** Monitored albums that aren't complete (unlisted ones included). */
  albums_wanted?: number;
}
export interface MusicTrack {
  id: number;
  album_id: number;
  disc_number: number;
  track_number: number;
  /** The number as the release prints it ("A1"), when it isn't just the position. */
  number?: string;
  title: string;
  duration_sec?: number;
  monitored: boolean;
  /** Set only when the track's credit differs from the album's ("feat." on one song). */
  artist_credit?: string;
  has_file: boolean;
  /** A file is recorded but has gone missing from disk. */
  missing?: boolean;
  /** The last file check couldn't find the file (not missing until the next one agrees). */
  not_found_since?: string;
  /** The file check found it changed outside Arrmada (measured again since). */
  changed_at?: string;
  file_path?: string;
  /** The measured tier ("FLAC-24", "MP3-320"), or an extension tag ("MP3") when unmeasured. */
  format?: string;
  bitrate_kbps?: number;
  size_bytes?: number;
  codec?: string;
  lossless?: boolean;
  sample_rate_hz?: number;
  bit_depth?: number;
  channels?: number;
  /** as_found | house | failed */
  tag_state?: string;
}
/** One edition of an album (a pressing, a reissue, the deluxe version). */
export interface MusicEdition {
  id: number;
  album_id: number;
  mbid?: string;
  title: string;
  status?: string;
  release_date?: string;
  country?: string;
  label?: string;
  disambiguation?: string;
  media_summary?: string;
  track_count: number;
  disc_count: number;
  source: string;
  /** The listing Arrmada had before editions existed, not yet matched to a MusicBrainz release. */
  legacy?: boolean;
  target?: boolean;
}
export interface MusicDisc {
  id: number;
  position: number;
  format?: string;
  title?: string;
  track_count: number;
}
/** A file the album holds that is on no track of its target edition: kept and listed. */
export interface MusicExtraFile {
  id: number;
  path: string;
  size_bytes: number;
  quality?: string;
  lossless: boolean;
}
export interface MusicAlbum {
  id: number;
  artist_id: number;
  mbid: string;
  title: string;
  year?: number;
  album_type?: string;
  cover_url?: string;
  release_date?: string;
  monitored: boolean;
  tracks?: MusicTrack[];
  /** The target edition's wanted tracks, and how many of them have a file. */
  track_count: number;
  have_tracks: number;
  size_bytes: number;
  /** Every track of the target edition; 0 means the listing isn't known yet. */
  edition_tracks?: number;
  /** The lowest tier among the tracks on disk (album detail only). */
  held_tier?: string;
  /** Track files the file check found gone from disk, and changed outside Arrmada (MUS-15). */
  missing_tracks?: number;
  changed_tracks?: number;
  primary_type?: string;
  secondary_types?: string;
  /** The album's credit as it reads ("Daft Punk feat. Pharrell Williams"). */
  artist_credit?: string;
  target_edition_id?: number;
  /** A person chose the edition and keeps it: no automatic switching (MUS-30). */
  edition_pinned?: boolean;
  /** Album detail only. */
  editions?: MusicEdition[];
  discs?: MusicDisc[];
  extras?: MusicExtraFile[];
  /** The upgrade's open notes: tracks the old library said had a file that it couldn't give one. */
  notes?: MusicNote[];
  /** Set when a refresh found it after the artist was added (MUS-13); found_as says why it
   *  is or isn't wanted: new, not_new, back_catalogue, too_many, single_pending, single_asked, single_on_album, undated_pending. */
  found_at?: string;
  found_as?: string;
  /** MusicBrainz no longer lists it: kept, files and all. */
  stale_since?: string;
}
/** A track the upgrade to editions couldn't give its file (two tracks on one file, or a file with no path). */
export interface MusicNote {
  id: number;
  legacy_track_id?: number;
  path?: string;
  kind: string;
  note: string;
}
export interface Artist {
  id: number;
  mbid: string;
  name: string;
  sort_name?: string;
  overview?: string;
  image_url?: string;
  genres?: string[];
  monitored: boolean;
  quality_profile: string;
  added_at?: string;
  disambiguation?: string;
  type?: string;
  country?: string;
  /** False for an artist that is only credited (featured on another artist's album). */
  in_library?: boolean;
  /** The artist's own new-release choice ("" = the library default) and release types
   *  (absent = the library default). */
  monitor_new?: "" | "all" | "none";
  release_types?: { primary: string[]; secondary: string[] };
  last_refreshed_at?: string;
  /** MusicBrainz isn't answering for it; everything is kept. */
  stale_since?: string;
  /** The albums filed under this artist (detail only). */
  albums?: MusicAlbum[];
  /** Albums filed under other artists that credit this one (detail only). */
  appears_on?: MusicAlbum[];
  stats?: ArtistStats;
}

export interface MovieEvent {
  event: string;
  detail?: string;
  created_at: string;
}

export interface ImportCandidate {
  path: string;
  filename: string;
  size_bytes: number;
  quality?: string;
}

export interface MovieFile {
  path: string;
  filename: string;
  size_bytes: number;
  quality?: string;
  codec?: string;
  audio?: string[];
  hdr?: string[];
  group?: string;
  resolution?: string;
  duration_min?: number;
  probed?: boolean;
  atmos?: boolean; // Dolby Atmos in any audio track (from the file's own stream profile)
  subtitles?: string[]; // paired with this file (named for it)
  orphan_subtitles?: string[]; // in the folder but named for no video — Plex won't show them
  missing: boolean;
}

export interface RankedRelease {
  title: string;
  indexer: string;
  /** Opaque, short-lived handle the grab endpoints take instead of a download link. Absent
   * on the quality test's results (it never grabs) and on a release with nothing to fetch. */
  token?: string;
  info_url?: string;
  size_gb: number;
  bitrate_mbps?: number;
  seeders: number;
  /** Beside seeders, as the indexer reports it (leechers, or the swarm on Torznab). */
  peers?: number;
  /** When the release was posted (RFC3339); absent when the indexer didn't say. */
  published_at?: string;
  /** Which network it's on. One no client of its network can take is ineligible, and its reject_reason says so. */
  transport?: "torrent" | "usenet";
  summary: string;
  eligible: boolean;
  reject_reason?: string;
  recommended: boolean;
  blocklisted?: boolean;
  resolves?: string;
  // Books only:
  edition?: "ebook" | "audiobook";
  format?: string;
  narrator?: string;
  author?: string;
  series?: string;
  language?: string;
  /** The extra audiobook version this release belongs to (0/absent = standard). */
  version_id?: number;
  version?: string;
  // Why an eligible row ranks where it does (video searches):
  matched?: string[];
  avoided?: string[];
  bonus_waived?: boolean;
  /** "lower resolution", "fewer seeders"… — against the release ranked just above it. */
  lost_on?: string;
  lost_on_key?: string;
  /** The audio languages the name carries, as base codes, and a note when they're assumed. */
  audio_langs?: string[];
  audio_langs_note?: string;
}

export interface BlockEntry {
  id: number;
  title: string;
  indexer?: string;
  reason?: string;
  created_at: string;
}

// One indexer that couldn't answer a search: it errored, or (background searches only) it
// was paused after repeated failures and not asked.
export interface IndexerIssue {
  indexer: string;
  error: string;
  skipped?: boolean;
}

export interface ReleaseList {
  profile: string;
  why?: string[];
  releases: RankedRelease[];
  /** The runner-up and what the recommended release beat it on. */
  chosen_over?: string;
  indexer_issues?: IndexerIssue[];
  /** How many indexers were asked. */
  searched?: number;
}

export interface Movie {
  id: number;
  tmdb_id: number;
  /** The Movies folder it belongs to (0: the default), when there are several. */
  library_folder_id?: number;
  imdb_id?: string;
  title: string;
  /** The title the app shows (Settings → Library → "Show titles as", or the title's own
   *  override); title stays TMDB's English title. Use lib/displayTitle shownTitle. */
  display_title?: string;
  /** The per-title "Display title…" pick; absent follows the setting. */
  title_override?: string;
  year: number;
  overview?: string;
  /** The overview in the language titles are shown in, when one is stored; overview stays English. */
  display_overview?: string;
  poster_url?: string;
  runtime?: number;
  status?: string;
  monitored: boolean;
  quality_profile: string;
  min_availability: string;
  has_file: boolean;
  movie_file_path?: string;
  added_at?: string;
  extra?: MovieExtra;
  file?: MovieFile;
  versions?: MovieVersion[];
  download?: MovieDownload;
  /** Detail only: whether the upgrade sweep will look at this movie (monitored, has a file, profile upgrades). */
  upgrades_allowed?: boolean;
  /** The default file is kept out of profile-driven upgrades ("keep existing files"). */
  upgrade_hold?: boolean;
  /** The recorded default file is gone from disk (the daily missing-file check, or the
   * detail page's own look); file_missing_since says when it was noticed. MOV-21. */
  file_missing?: boolean;
  file_missing_since?: string;
  /** Detail only: what Arrmada will do about this movie, from the facts the sweeps act on. */
  acquisition?: MovieAcquisition;
  // Detail only (MOV-04): the missing-sweep's backoff, when it next searches (absent when
  // no automatic search is coming) and how the stored searches went.
  last_search_at?: string;
  search_misses?: number;
  next_search_at?: string;
  last_search?: AttemptSummary;
}

/**
 * A film's download in flight: what it is for — its own missing file, an upgrade that
 * replaces the file it has, or an extra version's first file (version_label names it).
 */
export interface MovieDownload {
  state: string;
  progress: number;
  /** The torrent's info hash: a queue.progress event moves this bar by it. */
  hash?: string;
  kind?: "missing" | "upgrade" | "version";
  version_id?: number;
  version_label?: string;
}

/** One film's download, as GET /api/v1/movies/downloads lists it. */
export interface MovieDownloadRow extends MovieDownload {
  movie_id: number;
}

/** The media facts the library table shows about a film's file. */
export interface SummaryMedia {
  resolution?: string;
  quality?: string; // "2160p BluRay", from the name when not probed
  codec?: string;
  audio?: string[];
  atmos?: boolean;
  hdr?: string[];
  bitrate_mbps?: number;
  duration_min?: number;
  container?: string; // "mkv"
}

/**
 * A film as the library list sends it (GET /api/v1/movies): what the grid, the table and
 * the filters read, and nothing more — no cast, overview or file paths. The detail page
 * keeps the full Movie.
 */
export interface MovieSummary {
  id: number;
  title: string;
  /** The title the app shows (Settings → Library → "Show titles as", or the title's own
   *  override); title stays TMDB's English title. Use lib/displayTitle shownTitle. */
  display_title?: string;
  sort_title: string;
  year: number;
  poster_url?: string;
  monitored: boolean;
  has_file: boolean;
  file_missing?: boolean;
  quality_profile: string;
  min_availability: string;
  added_at?: string;
  vote_average?: number;
  size_bytes?: number;
  /** The Movies folder it belongs to; absent: the default (0). */
  library_folder_id?: number;
  media?: SummaryMedia;
  download?: MovieDownload;
}

export interface MovieAcquisition {
  monitored: boolean;
  /** False for "n/a" (scanned in) or a profile that no longer exists: the default applies. */
  profile_known: boolean;
  upgrades_allowed: boolean;
  scanned_in: boolean;
  available: boolean;
  available_from?: string;
  downloading: boolean;
  download_title?: string;
  download_progress?: number;
  file_missing: boolean;
}

export interface MovieLookup {
  tmdb_id: number;
  title: string;
  year: number;
  overview: string;
  poster_url: string;
  vote_average: number;
  /** Already in the library (MOV-23): the Add dialog links to library_id instead. */
  in_library?: boolean;
  library_id?: number;
}

/** A movie's minimum availability: when it becomes eligible for searching. */
export type MinAvailability = "announced" | "inCinemas" | "released";

/** One film of a bulk add (POST /api/v1/movies/bulk-add). */
export interface BulkAddResult {
  tmdb_id: number;
  status: "added" | "exists" | "failed";
  id?: number;
  title?: string;
  error?: string;
  queued?: boolean; // its search was queued
}

// The History feed (GET /api/v1/history). Music events belong to the artist.
export type HistoryKind = "movie" | "series" | "book" | "music";
export type HistoryCategory = "grabbed" | "imported" | "upgraded" | "failed" | "blocklisted" | "removed" | "seeded" | "deleted" | "renamed" | "review" | "other";
export interface HistoryEvent {
  kind: HistoryKind;
  item_id: number;
  title: string;
  poster_url?: string;
  event: string; // the raw event name ("file.deleted")
  category: HistoryCategory;
  detail: string;
  at_ms: number;
  id: number; // the row id within its kind
  /** The network a grab came over, when the event knows it. */
  network?: "torrent" | "usenet";
}
export interface HistoryPage { events: HistoryEvent[]; next_before: string }
// kind and event take one value or a comma-separated list; event "all" includes "other".
export interface HistoryFilter { kind?: string; event?: string; q?: string; before?: string; limit?: number }

export interface ImportRecord {
  hash: string;
  source_path: string;
  target_path: string;
  title: string;
  size_bytes: number;
  imported_at: string;
}

export type ReviewKind = "series" | "movie" | "book" | "music";

/** A finished torrent nothing in Arrmada grabbed or imported, outside its categories. */
export interface UnmanagedDownload {
  hash: string;
  name: string;
  size_bytes: number;
  category?: string;
  added_on?: number;
  /** A guess at what it is, for the picker's first kind. */
  kind: ReviewKind;
  /** The name parsed into a title, to start the picker's search with. */
  title?: string;
  /** Its Import into… job while that runs, or the last one when it failed. */
  import_job?: Job;
}

// One library item a held download could be imported into (same kind as the review).
export interface ReviewTarget {
  id: number;
  kind: ReviewKind;
  title: string;
  year?: number;
  subtitle?: string; // author for books, artist for albums
  poster_url?: string;
}

export interface ImportReview {
  id: number;
  hash: string;
  name: string;
  content_path: string;
  media_type: ReviewKind;
  expected_id: number;
  expected_title: string;
  parsed_title: string;
  reason: string;
  reason_code: ReviewReason;
  size_bytes: number;
  indexer: string;
  created_at: string;
  // Its import job: running or queued (someone chose to import it), or the last one,
  // when that failed. Absent otherwise.
  import_job?: Job;
  /** The music matcher's explanation of why it held the download (MUS-31). */
  match?: MusicMatchReport;
  /** "library_scan": a folder already in the music library that the scan couldn't place
   *  (MUS-37). It is matched where it is and can never be rejected. */
  source?: "library_scan";
}

/** One field's part of a music match distance. */
export interface MusicMatchComponent {
  name: string;
  weight: number;
  penalty: number;
  count: number;
  share: number;
}
/** One candidate edition the music matcher weighed. */
export interface MusicMatchCandidate {
  album_id: number;
  release_id: number;
  album: string;
  artist?: string;
  edition: string;
  target?: boolean;
  distance: number;
  fit: number;
  components: MusicMatchComponent[];
  tracks?: { file: string; track: string; distance: number; notes?: string[] }[];
  missing?: string[];
  extra?: string[];
}
/** The music matcher's explanation stored on a review. */
export interface MusicMatchReport {
  reason?: string;
  summary: string;
  expected?: boolean;
  threshold: number;
  candidates?: MusicMatchCandidate[];
  unplaced?: { file: string; why: string }[];
  /** "used": AcoustID was asked; "off": it would have been, but no key is set. */
  fingerprint?: "used" | "off";
}

// A review import that started (or was already running: existing).
export interface ReviewImportStarted extends JobRef {
  status: string;
  title?: string; // the library item it goes into
}

// Why a download is held: content that doesn't match, tied to nothing, unreadable episode
// numbering, an import that keeps failing, or nothing importable inside.
// The music matcher adds its own (MUS-31): a doubtful best fit, two editions that fit
// almost equally, too few of an album's tracks, tags naming another album's recordings, and
// AcoustID disagreeing with the tags.
export type ReviewReason = "mismatch" | "unmatched" | "numbering" | "import_failed" | "no_media"
  | "low_confidence" | "close_editions" | "partial" | "tag_conflict" | "fingerprint_conflict";

// One file inside a held download, with what its name says about episode numbering.
export interface ReviewFile {
  rel_path: string;
  size: number;
  video: boolean;
  guess: { season?: number; episodes?: number[]; absolute?: number[] };
}
