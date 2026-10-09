// Thin typed client for Arrmada's JSON API.

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
  authenticated: boolean;
  external: boolean;
  modules: Module[];
  books_enabled: boolean;
  music_enabled: boolean;
  plex_login: boolean;
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
}

export interface Candidate {
  name: string;
  release: ParsedRelease;
  size_gb: number;
  seeders: number;
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
}

export interface Decision {
  winner: Evaluation | null;
  why?: string[];
  chosen_over?: string;
  eligible: Evaluation[];
  rejected: Evaluation[];
}

export interface QualityPreview {
  preset?: string;
  profile?: string;
  decision: Decision;
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
  quality_profile: string;
  media_type?: string;
  held_by_guard?: boolean; // paused by the disk guard, which won't let it resume yet
  // How an in-flight grab stands against its stall window; absent for torrents Arrmada
  // didn't grab. idle_minutes stays 0 while the clock is held (paused, queued, checking).
  stall?: { idle_minutes: number; failover_in_minutes: number; off: boolean };
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

/** FlareSolverr as the Indexers page shows it; error is the exact failure. */
export interface FlareSolverrStatus {
  configured: boolean;
  ok: boolean;
  url?: string;
  version?: string;
  error?: string;
  checked_at: string;
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
  ideal?: IdealFile; // the target file: drives grabbing, ranking and the library fit check
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
}
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
  /** The packaged qBittorrent. Deleted, it stays deleted until restored. */
  bundled?: boolean;
  /** How it has been answering; absent when nothing is known yet. */
  status?: DownloadClientStatus;
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
}

export interface NotificationConn {
  id?: number;
  name: string;
  kind: string; // free-form label / service hint
  url: string; // an Apprise URL
  on_grab: boolean;
  on_import: boolean;
  on_stream?: boolean;
  on_buffering?: boolean;
  enabled: boolean;
}

export interface UserNotification { id: number; title: string; body: string; media_type: string; ref: string; read: boolean; created_at: number }

export interface CalendarItem { date: string; type: "episode" | "movie"; title: string; subtitle: string; poster_url?: string; ref_id: number; has_file: boolean; monitored: boolean }

export interface LibraryPaths { movies: string; tv: string; ebooks: string; audiobooks: string; music: string; downloads: string }
// Audiobook server (listening apps like Lissen).
export interface AudioDevice { id: string; user_id: number; username: string; client: string; device: string; created_at: number; last_used_at: number }
export interface AudioConnection { enabled: boolean; running: boolean; error?: string; host_port: string; public_url: string }
export interface AudioServerAdmin extends AudioConnection {
  users: { id: number; username: string; role: UserRole; disabled: boolean; eligible: boolean; allowed: boolean; has_password: boolean }[];
  devices: AudioDevice[];
  items: number;
  items_ready: number;
}
export interface AudioPlace { item_key: string; book_id: number; title: string; author?: string; cover_url?: string; position: number; duration: number; finished: boolean; updated_at: number; device?: string;
  /** A big jump back that's being held until it proves itself (or the person confirms it). */
  pending_position?: number | null; pending_at?: number }
export interface MyAudio extends AudioConnection { username: string; allowed: boolean; has_password: boolean; min_password_length: number; devices: AudioDevice[]; places: AudioPlace[] }
export interface AudioHistoryEntry { id: number; position: number; at: number; device?: string; reason: string }
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
  suggestions: Partial<LibraryPaths>;
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
  artists: number; albums: number;
}
export interface ActivityEvent {
  kind: "movie" | "series" | "book";
  id: number; title: string; event: string; detail: string; at_ms: number;
}
export interface DashboardData {
  storage: StorageVolume[];
  streams?: InsightsActivity;
  streams_note?: string;
  /** A Plex URL and token are set; without them streams_note stays empty. */
  plex_configured: boolean;
  queue: QueueSummary;
  queue_note?: string;
  library: LibraryCounts;
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

export interface DiskGuardStatus {
  enabled: boolean;
  measurable: boolean;
  path: string;
  used_pct: number;
  pause_pct: number;
  resume_pct: number;
  holding: number;
  // The library folders (the ones picked in Settings → Library) on the same drive as
  // the downloads folder. shared_with_library is just shared_with.length > 0.
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
}

export interface AppSettings {
  search_on_add: boolean;
  /** The monitoring preset a new series gets: "all" | "future" | "missing" | "existing" | "first_season" | "latest_season" | "none". */
  series_monitor_default: string;
  naming_movie_folder: string;
  naming_movie_file: string;
  naming_series_folder: string;
  naming_series_season: string;
  naming_series_episode: string;
  write_nfo: boolean;
  download_artwork: boolean;
  books_enabled: boolean;
  music_enabled: boolean;
  plex_login_enabled: boolean;
  plex_login_auto_approve: boolean;
  /** Discovery region for TMDB lists (ISO 3166-1 alpha-2, e.g. "AU"); "" = global. */
  tmdb_region: string;
  // Recycle bin guard rails.
  recycle_max_gb: string;
  recycle_retention_days: string;
  downloads_disk_guard: boolean;
  downloads_disk_guard_pause_pct: string;
  downloads_disk_guard_resume_pct: string;
  /** Minutes with no progress before another release is tried; 0 = never. Default 360. */
  downloads_stall_minutes: number;
  /** How many upgrades one upgrade sweep may grab; 0 = no limit. Default 10. */
  upgrade_max_grabs_per_sweep: number;
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
}
// Why a database backup was taken; part of its file name.
export type BackupKind = "pre-migrate" | "nightly" | "manual" | "pre-restore" | "pre-delete-user" | "pre-delete-empty-user" | "uploaded";

// One database backup file. Nothing from inside it is ever sent, beyond its schema version.
export interface BackupFile {
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
// What deleting a movie with its files would move (each file still on disk), and where.
export interface MovieDeletePreview {
  versions: { id: number; label: string; file_name: string; size_bytes: number }[];
  sidecars: number;
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
  title: string;
  year: number;
  poster_url?: string;
  overview?: string;
  status: "pending" | "approved" | "declined";
  quality_profile?: string;
  requested_by: number;
  requested_by_name?: string;
  note?: string;
  available: boolean;
  download_progress?: number; // 0..1 while the requested item is downloading
  tracking?: RequestTracking;
  created_at: string;
  updated_at: string;
}

// RequestTracking is where a request has got to, from its own downloads.
export type RequestStage =
  | "pending" | "declined" | "searching" | "queued" | "downloading" | "paused"
  | "failed" | "importing" | "partial" | "available";
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
  /** Books not found yet: when the next search is due (RFC3339; format in the viewer's locale). */
  next_check_at?: string;
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
  ebook?: { format: string; size_bytes: number };
  audiobook: boolean;
  mine: boolean; // the signed-in user requested it
  /** Downloadable audiobooks: the standard one (version_id 0) and any extra versions. */
  audiobooks?: { version_id: number; label?: string; format: string; size_bytes: number; files: number }[];
}

// MyRequest is one of the signed-in user's book requests that hasn't produced a file.
export interface MyRequest {
  title: string;
  author?: string;
  year?: number;
  cover_url?: string;
  status: "pending" | "approved" | "declined";
  requested_at: string;
  /** Where it has got to; absent when the download client couldn't be read. */
  stage?: RequestStage;
  note?: string; // "Not found yet"
  next_check_at?: string; // RFC3339
}

export interface DiscoverCard {
  media_type: "movie" | "series";
  tmdb_id: number;
  title: string;
  year: number;
  overview?: string;
  poster_url?: string;
  backdrop_url?: string;
  vote_average: number;
  release_date?: string;
  genres?: string[]; // up to three
  in_library: boolean;
  has_file: boolean;
  request_status?: "pending" | "approved" | "declined";
  download_progress?: number; // 0..1 while downloading
}
export interface WatchProvider { id: number; name: string; logo_url?: string }
export interface DiscoverRow { title: string; seed: string; items: DiscoverCard[] }

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
  auto_approve: boolean;
  created_at?: string;
  // Signs in with Plex; plex_blocked means that Plex account is on the block list.
  plex_linked?: boolean;
  plex_blocked?: boolean;
}

// A Plex account kept from signing in (it would otherwise make a new account each time).
export interface PlexBlock {
  plex_id: string;
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
}

export interface CrewMember {
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
  poster_url?: string;
  backdrop_url?: string;
  runtime?: number;
  status?: string;
  network?: string;
  genres?: string[];
  certification?: string;
  studios?: string[];
  cast?: { name: string; character?: string; profile_url?: string }[];
  crew?: CrewMember[];
  ratings: DetailRatings;
  // Enrichment added by the metadata worker — render defensively (only when present).
  trailer_url?: string; // YouTube (or similar) trailer link
  similar?: DiscoverCard[]; // "more like this" — same shape as a Discover card
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
  title: string;
  year: number;
  candidates: MatchCandidate[];
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
  download?: { state: string; progress: number };
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
  imdb_id?: string;
  title: string;
  year: number;
  overview?: string;
  poster_url?: string;
  status?: string;
  network?: string;
  monitored: boolean; // the pause gate: off means nothing is searched, choices kept
  monitor_new_seasons?: boolean; // a season new on refresh is monitored
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
export interface SeriesEpisodeDownload { season: number; episode: number; state: string; progress: number }
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
  /** The search ladder: when the sweep last looked (RFC3339; absent = never), how many
   *  searches in a row found nothing, and when it looks next (absent when nothing is
   *  wanted, it isn't monitored, or it's due now). */
  last_search_at?: string;
  search_misses: number;
  next_search_at?: string;
}
export interface CatalogueRef {
  name: "Hardcover" | "Open Library" | "Google Books";
  url: string;
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

// --- Subtitles ---
export interface SubtitleSettings {
  movies_auto: boolean;
  series_auto: boolean;
  languages: string[];
  provider_ready: boolean;
  can_download: boolean;
  ai_ready: boolean;
  ai_backend: string; // "sycl" | "vulkan" | "cpu" | "" until the first run
  ai_note: string; // why the Intel oneAPI build was set aside, when it was ("" otherwise)
  quota_remaining: number; // -1 = unknown
  quota_reset_at: number; // unix seconds; 0 = not paused
  pending: number;
}
export interface SubTrack { index: number; codec: string; lang: string; text: boolean; title?: string; forced?: boolean; sdh?: boolean; default?: boolean }
export interface SubLangStatus { lang: string; have: boolean; source?: "extract" | "ocr" | "download" | "ai"; fallback?: "ai"; orphan?: boolean }
export interface SubHealth { score: number; notes?: string[] }
export interface SubFileEntry {
  kind: "movie" | "episode";
  movie_id?: number; series_id?: number; season?: number; episode?: number;
  title: string; year?: number; poster_url?: string; path: string; duration_sec?: number;
  audio_langs?: string[]; embedded: SubTrack[]; external: string[];
  languages: SubLangStatus[]; health?: SubHealth; missing: number;
  orphans?: { name: string; lang?: string; variant?: string }[]; // movies: subtitles paired with no video
}
export interface SubtitleJob {
  id: number; kind: "movie" | "episode"; movie_id?: number; series_id?: number; season?: number; episode?: number;
  title: string; state: "queued" | "running" | "done" | "skipped" | "failed" | "cancelled"; note?: string; at: number;
  progress?: number; // 0-100 during an AI run
  stage?: string;    // what a running job is doing
  started_at?: number; // unix seconds the worker picked it up
  redo?: boolean; // replacing the sidecars already there
  priority: number; // 0 import · 1 manual · 2 sweep — the worker takes the lowest first
}
// SubtitleCoverage is the Overview's totals, from the last library pass (not a live walk).
export interface SubtitleCoverage {
  files: number; covered: number; missing: number;
  movies: { files: number; covered: number; missing: number };
  tv: { files: number; covered: number; missing: number };
  scanned_at: number; // 0 until the first pass completes
  scanning: boolean;
}
export interface WhisperModel { name: string; label: string; size_mb: number; present: boolean; downloading: boolean }
export interface WhisperStatus { binary_ready: boolean; ready: boolean; models: WhisperModel[] }
export interface MovieSubStatus {
  id: number;
  title: string;
  year: number;
  poster_url?: string;
  present: string[];
  missing: string[];
}
export interface SeriesSubStatus {
  id: number;
  title: string;
  year: number;
  poster_url?: string;
  episodes: number;
  complete: number;
  missing_subs: number;
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
  media_kind: string; movie_id?: number; series_id?: number; season: number; episode: number; title: string;
}

export interface ConvertBlocked {
  key: string; kind: string; movie_id?: number; series_id?: number; season: number; episode: number;
  title: string; count: number; last_error: string; updated_at: string;
}

export interface ConvertMediaStats {
  files: number; convertible: number; total_bytes: number; est_bytes: number; convertible_bytes: number; reclaimable: number;
  skipped: number;
  hdr10: number; hdr10_plus: number; dolby_vision: number; hlg: number;
  h264: number; hevc: number; av1: number; other: number;
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
export interface ConvertCandidate { kind: "movie" | "episode"; key: string; movie_id?: number; series_id?: number; season?: number; episode?: number; title: string; year?: number; poster_url?: string; path: string; info?: ConvertMediaInfo; candidate: boolean; worth: boolean; save_bytes: number; needs: ConvertNeeds; est_bytes: number; tracks?: string }
export interface ConvertJob {
  id: number; key: string; kind?: string; movie_id?: number; series_id?: number; season?: number; episode?: number; title: string;
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
  id: number; key: string; kind: "movie" | "episode"; movie_id?: number; series_id?: number; season: number; episode?: number; title: string;
  outcome: ConvertHistoryOutcome; outcome_kind?: string; note?: string; requested: boolean;
  src_path?: string; src_release?: string; src_size: number; src_spec?: string;
  out_path?: string; out_size: number; out_spec?: string; codec?: string; crf?: number; encoder?: string; crop?: string;
  ssim_mean?: number; ssim_min?: number; ssim_windows?: number[];
  kept_tracks?: ConvertTrackDecision[]; dropped_tracks?: ConvertTrackDecision[]; warnings?: string[]; reclaim_deferred?: boolean;
  started_at: number; finished_at?: number; encode_secs?: number;
  src_info?: ConvertMediaInfo; out_info?: ConvertMediaInfo;
}
export interface ConvertUpNext { key: string; title: string; kind: string; video: boolean; saving: number; size: number; tracks: string; reason: string; codec: string; current: string }
export interface ConvertRequest { key: string; title: string; requested_at: number }
export interface ConvertStatus {
  state: "working" | "starting" | "paused" | "waiting" | "off" | "done"; message: string;
  auto: boolean; window?: string; watching: boolean;
  requests: ConvertRequest[]; up_next: ConvertUpNext[]; remaining: number;
}
export interface ConvertSettings {
  auto: boolean; hours_start: string; hours_end: string; allow_av1: boolean; use_gpu: boolean; pause_watching: boolean;
  keep_audio_langs: string; keep_original_lang: boolean; drop_commentary: boolean; keep_sub_langs: string;
  image_subs: "keep" | "when_text" | "remove"; tidy_tracks: boolean; crop: boolean;
  scratch_dir: string; vaapi_device: string; cpu_cores: number; workers: number; scan_at: string;
  server_time: string; server_tz: string; plex_watching_known: boolean; can_pause: boolean;
  has_gpu: boolean; gpu_does_av1: boolean; hdr10plus_tool: boolean;
}
export interface ConvertTrialSide { codec: string; encoder: string; bytes: number; ssim: number; est_bytes: number }
export interface ConvertTrialResult { key: string; title: string; src_bytes: number; clips: number; seconds: number; hevc: ConvertTrialSide; av1: ConvertTrialSide; pick: string; why: string; files?: string[] }
export interface ConvertCompareStatus { running: boolean; key?: string; title?: string; started?: number; result?: ConvertTrialResult; error?: string }

// Insights (Plex watch monitoring).
export interface PlexLibrary { key: string; title: string; type: string }
export interface PlexConfig { url: string; token_set: boolean; enabled: boolean; poll_seconds: number }
export interface PlexTestResult { ok: boolean; error?: string; machine_id?: string; version?: string; libraries?: PlexLibrary[] }
export interface GeoLocation { ip: string; local: boolean; city?: string; country?: string; country_code?: string; lat?: number; lon?: number }
export interface StreamDetail { src: string; stream?: string }
export interface InsightsStream {
  session_key: string; user: string; title: string; subtitle: string; type: string; thumb: string;
  progress_pct: number; offset_ms: number; duration_ms: number; state: string;
  player: string; platform: string; product: string; decision: string;
  bandwidth_kbps: number; location: string; ip: string; geo: GeoLocation;
  video: StreamDetail; audio: StreamDetail; container: StreamDetail;
  hw_transcode: boolean; throttled: boolean; reasons: string[];
}
export interface InsightsActivity { streams: InsightsStream[]; bandwidth: { total_kbps: number; lan_kbps: number; wan_kbps: number }; geo_active: boolean }
export interface HistoryEntry {
  id: number; user_id: string; user_name: string; title: string; grandparent_title: string; parent_title: string;
  media_index: number; parent_index: number; year: number; media_type: string; thumb: string; thumb_url: string;
  player: string; platform: string; product: string; ip_address: string; location: string; decision: string;
  started_at: number; stopped_at: number; paused_ms: number; view_offset_ms: number; duration_ms: number;
  video_src: string; video_stream: string; audio_src: string; audio_stream: string; container_src: string; container_stream: string;
  hw_transcode: boolean; buffer_count: number; subtitle: string; geo: GeoLocation; watched_secs: number; progress_pct: number;
}
export interface InsightsHistory { rows: HistoryEntry[]; total: number }
export interface TitleStat { title: string; thumb_url: string; plays: number; secs: number }
export interface NameStat { id: string; name: string; plays: number; secs: number }
export interface InsightsStats { most_watched_movies: TitleStat[]; most_watched_shows: TitleStat[]; most_active_users: NameStat[]; most_active_platforms: NameStat[]; recently_watched: HistoryEntry[] }
export interface UserEntry { id: string; username: string; last_seen: number; last_ip: string; last_platform: string; last_player: string; last_title: string; total_plays: number; total_secs: number; geo: GeoLocation }
export interface LibraryStat { title: string; type: string; count: number }
export interface RecentItem { title: string; subtitle: string; type: string; thumb_url: string; added_at: number }
export interface BWPoint { t: string; total_kbps: number; lan_kbps: number; wan_kbps: number }
export interface InsightsGraphs {
  days: string[]; daily_tv: number[]; daily_movies: number[]; daily_music: number[];
  by_day_of_week: number[]; by_hour: number[];
  top_platforms: NameStat[]; top_users: NameStat[]; bandwidth: BWPoint[];
}
export interface ReliabilitySummary { total_sessions: number; buffered_sessions: number; total_events: number; total_stall_ms: number; buffer_rate_pct: number }
export interface BufferGroup { name: string; sessions: number; buffered_sessions: number; events: number; stall_ms: number; rate_pct: number }
export interface BufferEvent { at: number; offset_ms: number; duration_ms: number; user: string; title: string; platform: string; decision: string; cause: string; detail: string }
export interface CauseCount { cause: string; label: string; count: number; stall_ms: number }
export interface Reliability { summary: ReliabilitySummary; causes: CauseCount[]; by_user: BufferGroup[]; by_platform: BufferGroup[]; by_title: BufferGroup[]; events: BufferEvent[] }

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

async function req<T>(path: string, opts?: RequestInit): Promise<T> {
  const res = await send(path, {
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    ...opts,
  });
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const api = {
  status: () => req<Status>("/api/v1/status"),
  health: () => req<Health>("/api/health"),

  me: () => req<{ user: AuthUser }>("/api/v1/auth/me").then((r) => r.user),
  // A deliberate sign-out mutes the signed-out signal: a poll that 401s before the page
  // reloads must not flash "You were signed out" or remember the page to come back to.
  logout: () => {
    signedOut = true;
    return req<unknown>("/api/v1/auth/logout", { method: "POST" });
  },
  login: (username: string, password: string) =>
    req<{ user: AuthUser }>("/api/v1/auth/login", { method: "POST", body: JSON.stringify({ username, password }) }),
  setupAdmin: (username: string, password: string) =>
    req<{ user: AuthUser }>("/api/v1/auth/setup", { method: "POST", body: JSON.stringify({ username, password }) }),
  users: () => req<{ users: AuthUser[] }>("/api/v1/users").then((r) => r.users),
  createUser: (body: { email: string; password: string; role: string; auto_approve: boolean }) =>
    req<AuthUser>("/api/v1/users", { method: "POST", body: JSON.stringify(body) }),
  // disabled: true turns off their sign-in and signs them out everywhere; nothing is deleted.
  updateUser: (id: number, body: { role?: string; auto_approve?: boolean; password?: string; disabled?: boolean }) =>
    req<{ id: number; role: string; auto_approve: boolean; disabled: boolean }>(`/api/v1/users/${id}`, { method: "PUT", body: JSON.stringify(body) }),
  userImpact: (id: number) => req<UserImpact>(`/api/v1/users/${id}/impact`),
  // confirm is the username, required by the server when the user has listening data.
  // blockPlex also blocks their Plex account, so they can't come straight back via Plex.
  deleteUser: (id: number, confirm?: string, blockPlex?: boolean) => {
    const q = new URLSearchParams();
    if (confirm) q.set("confirm", confirm);
    if (blockPlex) q.set("block_plex", "1");
    const qs = q.toString();
    return req<void>(`/api/v1/users/${id}${qs ? `?${qs}` : ""}`, { method: "DELETE" });
  },
  plexBlocks: () => req<{ blocks: PlexBlock[] }>("/api/v1/users/plex-blocks").then((r) => r.blocks),
  blockUserPlex: (id: number) => req<{ blocks: PlexBlock[] }>(`/api/v1/users/${id}/block-plex`, { method: "POST" }).then((r) => r.blocks),
  unblockPlex: (plexID: string) =>
    req<{ blocks: PlexBlock[] }>(`/api/v1/users/plex-blocks/${encodeURIComponent(plexID)}`, { method: "DELETE" }).then((r) => r.blocks),
  importOverseerr: (url: string, api_key: string) =>
    req<{ status: string; found: number } & JobRef>("/api/v1/requests/import/overseerr", { method: "POST", body: JSON.stringify({ url, api_key }) }),
  importTautulli: (url: string, api_key: string) =>
    req<{ status: string } & JobRef>("/api/v1/insights/import/tautulli", { method: "POST", body: JSON.stringify({ url, api_key }) }),

  indexers: () => req<{ indexers: Indexer[] }>("/api/v1/indexers").then((r) => r.indexers),
  createIndexer: (body: NewIndexer) =>
    req<Indexer>("/api/v1/indexers", { method: "POST", body: JSON.stringify(body) }),
  updateIndexer: (id: number, body: NewIndexer) =>
    req<void>(`/api/v1/indexers/${id}`, { method: "PUT", body: JSON.stringify(body) }),
  deleteIndexer: (id: number) => req<void>(`/api/v1/indexers/${id}`, { method: "DELETE" }),
  testIndexer: (id: number) =>
    req<IndexerTestResult>(`/api/v1/indexers/${id}/test`, { method: "POST" }),
  /** Tests settings without saving them; with id, a blank key or password means the saved one. */
  testIndexerSettings: (body: NewIndexer & { id?: number }) =>
    req<IndexerTestResult>("/api/v1/indexers/test", { method: "POST", body: JSON.stringify(body) }),
  flareSolverrStatus: () => req<FlareSolverrStatus>("/api/v1/flaresolverr/status"),
  prowlarrInfo: () => req<{ url: string; has_key: boolean }>("/api/v1/indexers/prowlarr"),
  syncProwlarr: (body: { url: string; api_key: string; add_flaresolverr_proxy?: boolean }) =>
    req<ProwlarrSyncResult>("/api/v1/indexers/prowlarr/sync", { method: "POST", body: JSON.stringify(body) }),

  activity: () => req<ActivityFeed>("/api/v1/downloads"),
  wanted: () => req<WantedLists>("/api/v1/wanted"),
  /** Search now from a Wanted row: clears the title's backoff and runs its search as a job. */
  wantedSearch: (kind: WantedRow["media_type"], id: number) =>
    req<{ status: string; started_at_ms?: number } & JobRef>(`/api/v1/wanted/${kind}/${id}/search`, { method: "POST" }),
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
  torrentAction: (hash: string, action: "recheck" | "reannounce" | "prio_up" | "prio_down") =>
    req<{ status: string }>(`/api/v1/queue/${hash}/action`, { method: "POST", body: JSON.stringify({ action }) }),
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
  qualityProfiles: (media: string) =>
    req<{ profiles: QualityProfileInfo[]; formats: FormatInfo[]; music_ladder?: string[]; music_presets?: MusicPreset[] }>(`/api/v1/quality/profiles?media=${media}`),
  libraryFitProfiles: (media: string) => req<{ profiles: Record<string, FitCounts> }>(`/api/v1/library/fit/profiles?media=${media}`),
  libraryFitPreview: (profile: StoredProfile) =>
    req<{ scope: "profile" | "library"; counts: FitCounts }>("/api/v1/library/fit/preview", { method: "POST", body: JSON.stringify({ profile }) }),
  qualityTest: (body: { profile: StoredProfile; movie_id?: number; series_id?: number; season?: number }) =>
    req<ReleaseList>("/api/v1/quality/test", { method: "POST", body: JSON.stringify(body) }),
  setDefaultProfile: (media: string, profile: string) =>
    req<{ media: string; profile: string }>("/api/v1/quality/default", { method: "POST", body: JSON.stringify({ media, profile }) }),
  libraryFitMovies: () => req<{ items: FitItem[] }>("/api/v1/library/fit?media=movies"),
  libraryFitSeries: () => req<{ items: SeriesFitSummary[] }>("/api/v1/library/fit?media=series"),
  libraryFitEpisodes: (seriesID: number) => req<{ items: FitItem[] }>(`/api/v1/library/fit?media=series&series=${seriesID}`),
  qualityProfile: (ref: string) => req<StoredProfile>(`/api/v1/quality/profiles/${encodeURIComponent(ref)}`),
  createQualityProfile: (sp: StoredProfile) =>
    req<StoredProfile>("/api/v1/quality/profiles", { method: "POST", body: JSON.stringify(sp) }),
  updateQualityProfile: (id: number, sp: StoredProfile) =>
    req<{ status: string }>(`/api/v1/quality/profiles/${id}`, { method: "PUT", body: JSON.stringify(sp) }),
  // Deletes a profile and moves everything on it to moveTo ("" = the media's default).
  deleteQualityProfile: (id: number, moveTo: string) =>
    req<{ moved: ProfileMoveCounts; moved_to: string }>(
      `/api/v1/quality/profiles/${id}?move_to=${encodeURIComponent(moveTo)}`, { method: "DELETE" }),
  qualityPreviewSpec: (sp: StoredProfile) =>
    req<QualityPreview>("/api/v1/quality/preview", { method: "POST", body: JSON.stringify(sp) }),
  // The dry run before saving an edit to a saved profile: which files it would make eligible
  // for replacement.
  qualityImpact: (profile: StoredProfile) =>
    req<ProfileImpact>("/api/v1/quality/impact", { method: "POST", body: JSON.stringify({ profile }) }),
  // "Keep existing files": hold a profile's files out of upgrades — with onlyAffected, just the
  // ones the edited profile would make eligible for replacement (call it before saving).
  holdExistingFiles: (id: number, edited?: StoredProfile) =>
    req<{ movies: number; versions: number; episodes: number; held: number }>(
      `/api/v1/quality/profiles/${id}/hold-existing`, { method: "POST", body: JSON.stringify(edited ? { only_affected: true, profile: edited } : {}) }),
  resumeMovieUpgrades: (id: number) =>
    req<{ resumed: number }>(`/api/v1/movies/${id}/resume-upgrades`, { method: "POST" }),
  resumeSeriesUpgrades: (id: number, season?: number) =>
    req<{ resumed: number }>(`/api/v1/series/${id}/resume-upgrades${season != null ? `?season=${season}` : ""}`, { method: "POST" }),

  downloadClients: () =>
    req<DownloadClientList>("/api/v1/downloadclients").then((r) => r.clients),
  downloadClientList: () => req<DownloadClientList>("/api/v1/downloadclients"),
  createDownloadClient: (body: NewDownloadClient) =>
    req<DownloadClient>("/api/v1/downloadclients", { method: "POST", body: JSON.stringify(body) }),
  updateDownloadClient: (id: number, body: NewDownloadClient) =>
    req<DownloadClient>(`/api/v1/downloadclients/${id}`, { method: "PUT", body: JSON.stringify(body) }),
  restoreBundledClient: () =>
    req<{ restored: boolean }>("/api/v1/downloadclients/restore-bundled", { method: "POST" }),
  deleteDownloadClient: (id: number) =>
    req<void>(`/api/v1/downloadclients/${id}`, { method: "DELETE" }),
  testDownloadClient: (id: number) =>
    req<{ ok: boolean; error?: string }>(`/api/v1/downloadclients/${id}/test`, { method: "POST" }),
  downloadClientStatus: (id: number) =>
    req<{ listen_port: number }>(`/api/v1/downloadclients/${id}/status`),

  notifications: () =>
    req<{ notifications: NotificationConn[] }>("/api/v1/notifications").then((r) => r.notifications),
  createNotification: (body: NotificationConn) =>
    req<NotificationConn>("/api/v1/notifications", { method: "POST", body: JSON.stringify(body) }),
  updateNotification: (id: number, body: NotificationConn) =>
    req<{ status: string }>(`/api/v1/notifications/${id}`, { method: "PUT", body: JSON.stringify(body) }),
  deleteNotification: (id: number) =>
    req<void>(`/api/v1/notifications/${id}`, { method: "DELETE" }),
  testNotification: (body: NotificationConn) =>
    req<{ ok: boolean; error?: string }>("/api/v1/notifications/test", { method: "POST", body: JSON.stringify(body) }),

  // Per-user notifications (in-app inbox + personal Apprise URL)
  myNotifications: () => req<{ notifications: UserNotification[]; unread: number }>("/api/v1/me/notifications"),
  markNotificationRead: (id: number) => req<void>(`/api/v1/me/notifications/${id}/read`, { method: "POST" }),
  pushKey: () => req<{ key: string }>("/api/v1/me/push/key").then((r) => r.key),
  pushSubscribe: (sub: { endpoint: string; keys: { p256dh: string; auth: string } }) =>
    req<{ subscribed: boolean }>("/api/v1/me/push/subscribe", { method: "POST", body: JSON.stringify(sub) }),
  pushUnsubscribe: (endpoint: string) =>
    req<{ subscribed: boolean }>("/api/v1/me/push/unsubscribe", { method: "POST", body: JSON.stringify({ endpoint }) }),
  markAllNotificationsRead: () => req<void>("/api/v1/me/notifications/read-all", { method: "POST" }),
  calendar: (start: string, end: string) => req<{ items: CalendarItem[]; start: string; end: string }>(`/api/v1/calendar?start=${start}&end=${end}`),
  myApprise: () => req<{ url: string; set: boolean }>("/api/v1/me/apprise"),
  setMyApprise: (url: string) => req<{ url: string; set: boolean }>("/api/v1/me/apprise", { method: "PUT", body: JSON.stringify({ url }) }),

  // refresh re-runs every check first (the server allows that once per 10 s).
  systemHealth: (refresh = false) =>
    req<SystemHealth>(`/api/v1/health/system${refresh ? "?refresh=1" : ""}`),
  dashboard: () => req<DashboardData>("/api/v1/dashboard"),
  diskGuard: () => req<DiskGuardStatus>("/api/v1/downloads/disk-guard"),
  fileInfo: (path: string) => req<FileDetails>(`/api/v1/files/info?path=${encodeURIComponent(path)}`),
  seriesAliases: (id: number) => req<SeriesAlias[]>(`/api/v1/series/${id}/aliases`),
  addSeriesAlias: (id: number, body: { title: string; tmdb_season: number }) =>
    req<SeriesAlias>(`/api/v1/series/${id}/aliases`, { method: "POST", body: JSON.stringify(body) }),
  deleteSeriesAlias: (id: number, aliasID: number) =>
    req<{ status: string }>(`/api/v1/series/${id}/aliases/${aliasID}`, { method: "DELETE" }),

  queue: () => req<{ items: QueueItem[] }>("/api/v1/queue").then((r) => r.items),

  // A grab names the release by the token its search handed out; the download link
  // itself never leaves the server. movie_id must be the movie the search was for.
  grab: (body: { token: string; movie_id: number }) =>
    req<{ status: string; title: string }>("/api/v1/grab", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  history: () => req<{ imports: ImportRecord[] }>("/api/v1/history").then((r) => r.imports),
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
  // findAnother: once the release is blocklisted, search the title it was grabbed for again.
  rejectReview: (id: number, findAnother = false) =>
    req<{ status: string; searching?: boolean; search_error?: string } & Partial<JobRef>>(`/api/v1/reviews/${id}/reject`, findAnother
      ? { method: "POST", body: JSON.stringify({ find_another: true }) }
      : { method: "POST" }),
  dismissReview: (id: number) => req<{ status: string }>(`/api/v1/reviews/${id}/dismiss`, { method: "POST" }),
  // Clears an "import keeps failing" review so the import sweep tries the download again.
  retryReview: (id: number) => req<{ status: string }>(`/api/v1/reviews/${id}/retry`, { method: "POST" }),
  reviewFiles: (id: number) => req<{ files: ReviewFile[]; truncated: boolean }>(`/api/v1/reviews/${id}/files`),
  // Imports a held show download by mapping its files to episodes by hand. A big pack
  // imports as a job (job_id set); a small one answers with how many episodes landed.
  mapReview: (id: number, seriesId: number, files: { rel_path: string; season: number; episodes: number[] }[]) =>
    req<{ status: string; placed?: number; background?: boolean } & JobRef>(`/api/v1/reviews/${id}/map`, { method: "POST", body: JSON.stringify({ series_id: seriesId, files }) }),
  bulkReviews: (ids: number[], action: "dismiss" | "reject") =>
    req<{ done: number; failed: { id: number; error: string }[] }>("/api/v1/reviews/bulk", { method: "POST", body: JSON.stringify({ ids, action }) }),
  // targetKind names what targetId is; the server refuses one that isn't the review's own kind.
  importReview: (id: number, targetId?: number, targetKind?: ReviewKind) =>
    req<{ status: string }>(`/api/v1/reviews/${id}/import`, { method: "POST", body: JSON.stringify({ target_id: targetId ?? 0, target_kind: targetKind ?? "" }) }),
  // truncated: more items matched than the server lists — the picker asks for a filter.
  reviewTargets: (id: number, q: string) =>
    req<{ targets: ReviewTarget[]; truncated?: boolean }>(`/api/v1/reviews/${id}/targets?q=${encodeURIComponent(q)}`),

  movies: () => req<{ movies: Movie[]; metadata_available: boolean; client_health?: QueueHealth }>("/api/v1/movies"),
  lookupMovies: (q: string) =>
    req<{ results: MovieLookup[] }>(`/api/v1/movies/lookup?q=${encodeURIComponent(q)}`).then((r) => r.results),
  settings: () => req<AppSettings>("/api/v1/settings"),
  updateSettings: (body: Partial<AppSettings>) =>
    req<AppSettings>("/api/v1/settings", { method: "PUT", body: JSON.stringify(body) }),
  plexLoginStart: () => req<{ id: number; auth_url: string }>("/api/v1/auth/plex/pin", { method: "POST" }),
  plexLoginPoll: (id: number) => req<{ pending?: boolean; user?: AuthUser }>(`/api/v1/auth/plex/pin/${id}`),
  logs: (opts?: { limit?: number; level?: string; q?: string; hide?: string }) => {
    const p = new URLSearchParams();
    if (opts?.limit) p.set("limit", String(opts.limit));
    if (opts?.level) p.set("level", opts.level);
    if (opts?.q) p.set("q", opts.q);
    if (opts?.hide) p.set("hide", opts.hide);
    const qs = p.toString();
    return req<{ entries: LogEntry[] }>(`/api/v1/logs${qs ? `?${qs}` : ""}`).then((r) => r.entries);
  },
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
  cancelJob: (id: number) => req<{ status: string; job_id: number }>(`/api/v1/jobs/${id}/cancel`, { method: "POST" }),
  // Recurring tasks (System → Status): staff can list them; Run now is admin-only and
  // answers 409 when the task is already running.
  tasks: () => req<TaskStatus[]>("/api/v1/system/tasks"),
  runTask: (name: string) =>
    req<{ status: string; job_id: number; existing: boolean }>(`/api/v1/system/tasks/${encodeURIComponent(name)}/run`, {
      method: "POST",
    }),
  // Database backups — admin only (a backup holds every secret the app has).
  backups: () => req<BackupsState>("/api/v1/system/backups"),
  // A manual database backup, taken synchronously.
  backupNow: () => req<BackupFile>("/api/v1/system/backups", { method: "POST" }),
  saveBackupSchedule: (p: Partial<BackupSchedule>) =>
    req<BackupSchedule>("/api/v1/system/backups/settings", { method: "PUT", body: JSON.stringify(p) }),
  deleteBackup: (name: string) =>
    req<{ status: string }>(`/api/v1/system/backups/${encodeURIComponent(name)}`, { method: "DELETE" }),
  // Stages a restore for the next start ("RESTORE" is the typed confirmation).
  restoreBackup: (name: string) =>
    req<RestoreStaged>(`/api/v1/system/backups/${encodeURIComponent(name)}/restore`, { method: "POST", body: JSON.stringify({ confirm: "RESTORE" }) }),
  cancelRestore: () => req<{ cancelled: boolean }>("/api/v1/system/backups/restore-pending", { method: "DELETE" }),
  recycleItems: () => req<{ items: RecycleItem[] }>("/api/v1/recycle/items").then((r) => r.items),
  // bin: one bin's key; omitted empties every bin.
  emptyRecycle: (bin?: string) => req<{ freed_bytes: number }>("/api/v1/recycle/empty", { method: "POST", body: JSON.stringify(bin ? { bin } : {}) }),
  restoreRecycle: (id: string) => req<{ status: string }>("/api/v1/recycle/restore", { method: "POST", body: JSON.stringify({ id }) }),
  deleteRecycleItem: (id: string) => req<{ status: string }>("/api/v1/recycle/delete", { method: "POST", body: JSON.stringify({ id }) }),
  // Catalog the movies folder: new films are added unmonitored on "n/a" unless monitor is
  // set (with a profile; "" = the default), and films already in the library without a
  // file get the one found.
  scanLibrary: (opts?: { monitor: boolean; quality_profile?: string }) =>
    req<{ status: string } & JobRef>("/api/v1/movies/scan", { method: "POST", ...(opts ? { body: JSON.stringify(opts) } : {}) }),
  moviesUnmatched: () => req<{ unmatched: UnmatchedFolder[] }>("/api/v1/movies/unmatched").then((r) => r.unmatched),
  importMovieFolder: (folder: string, tmdb_id: number) =>
    req<{ status: string }>("/api/v1/movies/import", { method: "POST", body: JSON.stringify({ folder, tmdb_id }) }),
  libraryPaths: () => req<LibraryPaths>("/api/v1/system/library"),
  audioServer: () => req<AudioServerAdmin>("/api/v1/audioserver"),
  setAudioServer: (body: { enabled?: boolean; public_url?: string }) => req<AudioServerAdmin>("/api/v1/audioserver", { method: "PUT", body: JSON.stringify(body) }),
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
  setAudioPassword: (password: string, signOutDevices: boolean) => req<MyAudio>("/api/v1/me/audio/password", { method: "PUT", body: JSON.stringify({ password, sign_out_devices: signOutDevices }) }),
  removeAudioPassword: () => req<MyAudio>("/api/v1/me/audio/password", { method: "DELETE" }),
  myAudioListening: (days = 30) => req<AudioListening>(`/api/v1/me/audio/listening?days=${days}`),
  acceptAudioJump: (item: string) => req<{ position: number }>("/api/v1/me/audio/accept", { method: "POST", body: JSON.stringify({ item }) }),
  revokeMyDevice: (id: string) => req<void>(`/api/v1/me/audio/devices/${encodeURIComponent(id)}`, { method: "DELETE" }),
  audioHistory: (item: string) => req<{ history: AudioHistoryEntry[] }>(`/api/v1/me/audio/history?item=${encodeURIComponent(item)}`),
  restoreAudioPlace: (item: string, historyId: number) => req<{ position: number }>("/api/v1/me/audio/restore", { method: "POST", body: JSON.stringify({ item, history_id: historyId }) }),
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
  addMovie: (body: { tmdb_id: number; quality_profile: string; monitored?: boolean; search_on_add?: boolean }) =>
    req<Movie>("/api/v1/movies", { method: "POST", body: JSON.stringify(body) }),
  previewTorrent: (torrent: string) =>
    req<TorrentPreview>("/api/v1/grab/preview", { method: "POST", body: JSON.stringify({ torrent }) }),
  grabMovieTorrent: (id: number, torrent: string, filename: string, title: string) =>
    req<{ status: string }>(`/api/v1/movies/${id}/grabtorrent`, { method: "POST", body: JSON.stringify({ torrent, filename, title }) }),
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
  deleteMovie: (id: number, deleteFiles?: boolean, cancelDownloads?: boolean) => {
    const q = new URLSearchParams();
    if (deleteFiles) q.set("delete_files", "true");
    if (cancelDownloads) q.set("cancel_downloads", "true");
    const qs = q.toString();
    return req<void>(`/api/v1/movies/${id}${qs ? `?${qs}` : ""}`, { method: "DELETE" });
  },
  movieDeletePreview: (id: number) => req<MovieDeletePreview>(`/api/v1/movies/${id}/delete-preview`),
  searchMovie: (id: number) =>
    req<{ status: string; started_at_ms?: number } & JobRef>(`/api/v1/movies/${id}/search`, { method: "POST" }),
  movie: (id: number) => req<Movie>(`/api/v1/movies/${id}`),
  movieCollection: (id: number) =>
    req<{ name: string; members: CollectionMember[] }>(`/api/v1/movies/${id}/collection`),

  series: () => req<{ series: Series[]; metadata_available: boolean }>("/api/v1/series"),
  lookupSeries: (q: string) =>
    req<{ results: SeriesLookup[] }>(`/api/v1/series/lookup?q=${encodeURIComponent(q)}`).then((r) => r.results),
  // monitor is a monitoring preset; left out, the server uses Settings' default.
  addSeries: (body: { tmdb_id: number; quality_profile?: string; monitored?: boolean; search_on_add?: boolean; monitor?: string; monitor_new_seasons?: boolean }) =>
    req<Series>("/api/v1/series", { method: "POST", body: JSON.stringify(body) }),
  seriesDetail: (id: number) => req<Series>(`/api/v1/series/${id}`),
  seriesDownloads: (id: number) => req<SeriesEpisodeDownload[]>(`/api/v1/series/${id}/downloads`),
  searchSeries: (id: number) =>
    req<{ status: string; started_at_ms?: number } & JobRef>(`/api/v1/series/${id}/search`, { method: "POST" }),
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
  myBooks: () => req<{ books: MyBook[]; requests: MyRequest[] }>("/api/v1/me/books"),
  // A plain link, not a fetch: the browser saves the file with the server's filename.
  ebookDownloadURL: (bookId: number) => `/api/v1/books/${bookId}/ebook`,
  requests: (status?: string) =>
    req<{ requests: MediaRequest[]; auto_approve: boolean; client_health?: QueueHealth }>(`/api/v1/requests${status ? `?status=${status}` : ""}`),
  // Returns 200 even for already-requested titles: subscribed=true means "you were
  // attached to an existing request and will be notified too". Requesting a declined
  // title resurrects it as pending.
  createRequest: (body: { media_type: "movie" | "series" | "book"; tmdb_id?: number; ol_key?: string; author?: string; title: string; year: number; poster_url?: string; overview?: string; quality_profile?: string; note?: string }) =>
    req<{ request: MediaRequest; subscribed: boolean } | MediaRequest>("/api/v1/requests", { method: "POST", body: JSON.stringify(body) })
      .then((r): { request: MediaRequest; subscribed: boolean } => ("request" in r ? r : { request: r, subscribed: false })),
  approveRequest: (id: number, quality_profile?: string) =>
    req<MediaRequest>(`/api/v1/requests/${id}/approve`, { method: "POST", body: JSON.stringify({ quality_profile: quality_profile ?? "" }) }),
  declineRequest: (id: number) =>
    req<{ status: string }>(`/api/v1/requests/${id}/decline`, { method: "POST" }),
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
  discoverCollections: () => req<{ items: DiscoverCard[] }>(`/api/v1/discover/collections`).then((r) => r.items),
  discoverByGenre: (media: string, genre: number) =>
    req<{ items: DiscoverCard[] }>(`/api/v1/discover?media=${media}&genre=${genre}`).then((r) => r.items),
  discoverGenres: (media: string) =>
    req<{ genres: Genre[] }>(`/api/v1/discover/genres?media=${media}`).then((r) => r.genres),
  mediaDetail: (media: string, tmdbId: number) =>
    req<MediaDetail>(`/api/v1/media/${media}/${tmdbId}`),
  discoverSearch: (q: string) =>
    req<{ items: DiscoverCard[] }>(`/api/v1/discover/search?q=${encodeURIComponent(q)}`).then((r) => r.items),

  seriesManualImportList: (id: number) =>
    req<ManualImportList<SeriesImportCandidate>>(`/api/v1/series/${id}/manualimport`),
  seriesManualImport: (id: number, path: string) =>
    req<{ status: string; background?: boolean } & JobRef>(`/api/v1/series/${id}/manualimport`, { method: "POST", body: JSON.stringify({ path }) }),
  seriesRenamePreview: (id: number) =>
    req<{ items: SeriesRenameItem[]; matches: boolean }>(`/api/v1/series/${id}/rename`),
  // Applies only the previewed items: anything that changed since the preview is skipped
  // and reported, never moved blind.
  renameSeries: (id: number, items: SeriesRenameItem[]) =>
    req<{ renamed: number; skipped: RenameSkip[] }>(`/api/v1/series/${id}/rename`, { method: "POST", body: JSON.stringify({ items }) }),
  // A numbering change waiting for review (pending: null when there's none).
  seriesNumbering: (id: number) => req<SeriesNumbering>(`/api/v1/series/${id}/numbering`),
  // Applies the reviewed plan as a job (its result is a NumberingApplied). A plan that
  // changed since it was shown is refused with a 409 and nothing moves.
  applySeriesNumbering: (id: number, plan_hash: string) =>
    req<JobRef>(`/api/v1/series/${id}/numbering/apply`, { method: "POST", body: JSON.stringify({ plan_hash }) }),
  dismissSeriesNumbering: (id: number) =>
    req<void>(`/api/v1/series/${id}/numbering/pending`, { method: "DELETE" }),
  // The series switch is a pause gate: season and episode choices are kept either way.
  setSeriesMonitored: (id: number, monitored: boolean) =>
    req<{ monitored: boolean; monitor_new_seasons: boolean }>(`/api/v1/series/${id}/monitor`, { method: "PUT", body: JSON.stringify({ monitored }) }),
  // Applies a monitoring preset to the show's episodes (the pause switch is untouched).
  applySeriesMonitorPreset: (id: number, preset: string) =>
    req<{ monitored: boolean; monitor_new_seasons: boolean }>(`/api/v1/series/${id}/monitor`, { method: "PUT", body: JSON.stringify({ preset }) }),
  setSeriesMonitorNewSeasons: (id: number, monitor_new_seasons: boolean) =>
    req<{ monitored: boolean; monitor_new_seasons: boolean }>(`/api/v1/series/${id}/monitor`, { method: "PUT", body: JSON.stringify({ monitor_new_seasons }) }),
  setSeriesProfile: (id: number, quality_profile: string) =>
    req<{ quality_profile: string }>(`/api/v1/series/${id}/profile`, { method: "PUT", body: JSON.stringify({ quality_profile }) }),
  setSeriesType: (id: number, series_type: string) =>
    req<{ series_type: string }>(`/api/v1/series/${id}/type`, { method: "PUT", body: JSON.stringify({ series_type }) }),
  // Manual scene-season mapping (anime whose broadcast cours don't match TMDB numbering).
  sceneOverrides: (id: number) =>
    req<{ overrides: SceneOverride[] }>(`/api/v1/series/${id}/scene-map`),
  setSceneOverride: (id: number, o: SceneOverride) =>
    req<SceneOverride>(`/api/v1/series/${id}/scene-map`, { method: "PUT", body: JSON.stringify(o) }),
  deleteSceneOverride: (id: number, sceneSeason: number) =>
    req<void>(`/api/v1/series/${id}/scene-map/${sceneSeason}`, { method: "DELETE" }),
  setSeasonMonitored: (id: number, season: number, monitored: boolean) =>
    req<{ monitored: boolean }>(`/api/v1/series/${id}/seasons/${season}/monitor`, { method: "PUT", body: JSON.stringify({ monitored }) }),
  setEpisodeMonitored: (eid: number, monitored: boolean) =>
    req<{ monitored: boolean }>(`/api/v1/series/episodes/${eid}/monitor`, { method: "PUT", body: JSON.stringify({ monitored }) }),
  // confirm is the series title, required by the server when deleting files over its size
  // threshold. A 409 (ApiError) carries body.moved / body.failed.
  deleteSeries: (id: number, deleteFiles?: boolean, confirm?: string) => {
    const q = new URLSearchParams();
    if (deleteFiles) q.set("delete_files", "true");
    if (deleteFiles && confirm) q.set("confirm", confirm);
    const qs = q.toString();
    return req<void>(`/api/v1/series/${id}${qs ? `?${qs}` : ""}`, { method: "DELETE" });
  },
  seriesDeletePreview: (id: number) => req<DeletePreview>(`/api/v1/series/${id}/delete-preview`),
  seriesBlocklist: (id: number) => req<{ blocklist: BlockEntry[] }>(`/api/v1/series/${id}/blocklist`).then((r) => r.blocklist),
  unblockSeries: (id: number, bid: number) => req<void>(`/api/v1/series/${id}/blocklist/${bid}`, { method: "DELETE" }),
  regrabEpisode: (id: number, season: number, episode: number) =>
    req<{ status: string } & JobRef>(`/api/v1/series/${id}/seasons/${season}/episodes/${episode}/regrab`, { method: "POST" }),
  deleteEpisodeFile: (id: number, season: number, episode: number) =>
    req<void>(`/api/v1/series/${id}/seasons/${season}/episodes/${episode}/file`, { method: "DELETE" }),

  // Books
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
  updateAudioVersion: (id: number, vid: number, body: { label?: string; terms?: string[]; monitored?: boolean }) =>
    req<AudioVersion>(`/api/v1/books/${id}/audio-versions/${vid}`, { method: "PUT", body: JSON.stringify(body) }),
  deleteAudioVersion: (id: number, vid: number, deleteFiles: boolean) =>
    req<void>(`/api/v1/books/${id}/audio-versions/${vid}${deleteFiles ? "?delete_files=true" : ""}`, { method: "DELETE" }),
  deleteAudioVersionFile: (id: number, vid: number) =>
    req<{ status: string }>(`/api/v1/books/${id}/audio-versions/${vid}/file`, { method: "DELETE" }),
  searchAudioVersion: (id: number, vid: number) =>
    req<{ grabbed: boolean; message?: string; outcome?: SearchOutcome }>(`/api/v1/books/${id}/audio-versions/${vid}/search`, { method: "POST" }),
  renameBook: (id: number) => req<{ renamed: number }>(`/api/v1/books/${id}/rename`, { method: "POST" }),
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
  bookDiscoverTrending: () =>
    req<{ books: BookDiscoverCard[] }>("/api/v1/books/discover/trending").then((r) => r.books),
  bookDiscoverBrowse: (kind: "trending" | "new_releases" | "top_rated" | "popular") =>
    req<{ books: BookDiscoverCard[] }>(`/api/v1/books/discover/browse/${kind}`).then((r) => r.books),
  bookDiscoverRecommended: () =>
    req<{ rows: BookRecommendedRow[] }>("/api/v1/books/discover/recommended").then((r) => r.rows),
  bookDiscoverSearch: (q: string, source?: "openlibrary" | "hardcover") =>
    req<{ authors: BookAuthor[]; books: BookDiscoverCard[]; source: BookSource }>(`/api/v1/books/discover/search?q=${encodeURIComponent(q)}${source ? `&source=${source}` : ""}`),
  searchBookAuthors: (q: string) =>
    req<{ authors: BookAuthor[] }>(`/api/v1/books/discover/authors?q=${encodeURIComponent(q)}`).then((r) => r.authors),
  addAuthor: (body: { author_key: string; quality_profile?: string; monitored?: boolean; search_on_add?: boolean }) =>
    req<{ added: number; skipped: number; total: number }>("/api/v1/books/author", { method: "POST", body: JSON.stringify(body) }),
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
  overrideBookMetadata: (id: number, body: { title: string; author: string; year: number; overview: string; cover_url: string }) =>
    req<{ status: string }>(`/api/v1/books/${id}/metadata`, { method: "PUT", body: JSON.stringify(body) }),
  deleteBook: (id: number, deleteFiles?: boolean) =>
    req<void>(`/api/v1/books/${id}${deleteFiles ? "?delete_files=true" : ""}`, { method: "DELETE" }),
  bookHistory: (id: number) =>
    req<{ events: MovieEvent[] }>(`/api/v1/books/${id}/history`).then((r) => r.events),
  // Re-point a book at a different Open Library work, keeping its files and settings.
  rematchBook: (id: number, body: { ol_key: string; title: string; author: string; year: number; cover_url: string }) =>
    req<Book>(`/api/v1/books/${id}/rematch`, { method: "POST", body: JSON.stringify(body) }),

  // ---- Music ----
  artists: () => req<{ artists: Artist[] }>("/api/v1/music/artists").then((r) => r.artists ?? []),
  lookupArtists: (q: string) =>
    req<{ results: ArtistLookup[] }>(`/api/v1/music/lookup?q=${encodeURIComponent(q)}`).then((r) => r.results ?? []),
  addArtist: (body: { mbid: string; quality_profile?: string; monitored?: boolean }) =>
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
  updateSubtitleSettings: (body: { movies_auto?: boolean; series_auto?: boolean; languages?: string[] }) =>
    req<SubtitleSettings>("/api/v1/subtitles/settings", { method: "PUT", body: JSON.stringify(body) }),
  subtitleLibrary: (media: "movies" | "tv" = "movies") => req<{ items: SubFileEntry[]; scanning?: boolean }>(`/api/v1/subtitles/library${media === "tv" ? "?media=tv" : ""}`),
  subtitleCoverage: () => req<SubtitleCoverage>("/api/v1/subtitles/coverage"),
  subtitleRescan: () => req<{ started: boolean }>("/api/v1/subtitles/library/rescan", { method: "POST" }),
  // TV rolled up per show. The flat list is one probed row per episode, which at
  // library scale is tens of thousands of rows before the page can render.
  subtitleSeriesGroups: () =>
    req<{ groups: SubSeriesGroup[]; scanning?: boolean }>("/api/v1/subtitles/library?media=tv&group=series"),
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
  subtitleMovies: () => req<{ movies: MovieSubStatus[] }>("/api/v1/subtitles/movies").then((r) => r.movies),
  subtitleSeries: () => req<{ series: SeriesSubStatus[] }>("/api/v1/subtitles/series").then((r) => r.series),
  searchMovieSubs: (id: number) => req<{ status: string } & JobRef>(`/api/v1/subtitles/movies/${id}/search`, { method: "POST" }),
  searchSeriesSubs: (id: number) => req<{ status: string } & JobRef>(`/api/v1/subtitles/series/${id}/search`, { method: "POST" }),

  // Convert
  convertHardware: () => req<{ encoders: ConvertEncoder[]; using: string; reclaimed_bytes: number; scratch_dir: string; scratch_free_bytes: number; scratch_need_bytes?: number; scratch_need_title?: string; render_devices: { path: string; pci: string; vendor: string }[]; vaapi_device: string }>("/api/v1/convert/hardware"),
  convertStatus: () => req<ConvertStatus>("/api/v1/convert/status"),
  convertSettings: () => req<ConvertSettings>("/api/v1/convert/settings"),
  updateConvertSettings: (patch: Partial<ConvertSettings>) => req<ConvertSettings>("/api/v1/convert/settings", { method: "PUT", body: JSON.stringify(patch) }),
  convertReindex: () => req<{ started: boolean; reason?: string } & JobRef>("/api/v1/convert/reindex", { method: "POST" }),
  convertReindexStatus: () => req<{ running: boolean }>("/api/v1/convert/reindex"),
  convertLibrary: (media: "movies" | "tv" = "movies", seriesID?: number, convertibleOnly = false) => {
    const q = new URLSearchParams();
    if (media === "tv") q.set("media", "tv");
    if (seriesID) q.set("series", String(seriesID));
    if (convertibleOnly) q.set("convertible", "1");
    const qs = q.toString();
    return req<{ items: ConvertCandidate[] }>(`/api/v1/convert/library${qs ? `?${qs}` : ""}`).then((r) => r.items);
  },
  convertStats: () => req<ConvertLibraryStats>("/api/v1/convert/stats"),
  convertRequest: (key: string) => req<{ requested: string }>("/api/v1/convert/requests", { method: "POST", body: JSON.stringify({ key }) }),
  convertCancelRequest: (key?: string) => req<{ cancelled: number }>(`/api/v1/convert/requests?${key ? `key=${encodeURIComponent(key)}` : "all=1"}`, { method: "DELETE" }),
  convertCancel: (id: number) => req<{ cancelled: number }>(`/api/v1/convert/jobs/${id}/cancel`, { method: "POST" }),
  convertBlocklist: () => req<{ items: ConvertBlocked[] }>("/api/v1/convert/blocklist").then((r) => r.items),
  convertSkips: () => req<{ items: ConvertSkipped[] }>("/api/v1/convert/skips").then((r) => r.items),
  convertSkipsClear: (key?: string) =>
    req<{ cleared: boolean }>(`/api/v1/convert/skips/clear?${key ? `key=${encodeURIComponent(key)}` : "all=1"}`, { method: "POST" }),
  convertBlocklistClear: (key?: string) =>
    req<{ cleared: boolean }>(`/api/v1/convert/blocklist/clear?${key ? `key=${encodeURIComponent(key)}` : "all=1"}`, { method: "POST" }),
  convertLibrarySeries: () => req<{ series: ConvertSeriesRollup[] }>("/api/v1/convert/library?media=tv").then((r) => r.series),
  convertSeries: (seriesID: number, season?: number) =>
    req<{ requested: number }>(`/api/v1/convert/series/${seriesID}${season === undefined ? "" : `?season=${season}`}`, { method: "POST" }),
  convertJobs: () => req<{ jobs: ConvertJob[] }>("/api/v1/convert/jobs").then((r) => r.jobs),
  convertLogs: () => req<{ lines: { at: number; level: string; msg: string }[] }>("/api/v1/convert/logs").then((r) => r.lines),
  convertHistory: (f: { outcome?: ConvertHistoryOutcome; media?: "movie" | "episode"; q?: string; before?: string; limit?: number } = {}) => {
    const q = new URLSearchParams();
    if (f.outcome) q.set("outcome", f.outcome);
    if (f.media) q.set("media", f.media);
    if (f.q) q.set("q", f.q);
    if (f.before) q.set("before", f.before);
    if (f.limit) q.set("limit", String(f.limit));
    const qs = q.toString();
    return req<{ items: ConvertHistoryEntry[]; next: string }>(`/api/v1/convert/history${qs ? `?${qs}` : ""}`);
  },
  convertHistoryEntry: (id: number) => req<ConvertHistoryEntry>(`/api/v1/convert/history/${id}`),
  convertCompare: (key: string) => req<ConvertCompareStatus>("/api/v1/convert/compare", { method: "POST", body: JSON.stringify({ key }) }),
  convertCompareStatus: () => req<ConvertCompareStatus>("/api/v1/convert/compare"),
  convertCompareFileURL: (name: string) => `/api/v1/convert/compare/files/${encodeURIComponent(name)}`,

  // Insights (Plex)
  insightsConfig: () => req<PlexConfig>("/api/v1/insights/plex"),
  insightsPlexAuthStart: () => req<{ id: number; auth_url: string }>("/api/v1/insights/plex/auth", { method: "POST" }),
  insightsPlexAuthPoll: (id: number) => req<{ authorized: boolean }>(`/api/v1/insights/plex/auth/${id}`),
  updateInsightsConfig: (body: { url: string; token?: string; enabled?: boolean; poll_seconds?: number }) =>
    req<PlexConfig>("/api/v1/insights/plex", { method: "PUT", body: JSON.stringify(body) }),
  testInsights: (body: { url?: string; token?: string }) =>
    req<PlexTestResult>("/api/v1/insights/plex/test", { method: "POST", body: JSON.stringify(body) }),
  insightsActivity: () => req<InsightsActivity>("/api/v1/insights/activity"),
  insightsHistory: (p: { type?: string; decision?: string; q?: string; page?: number; page_size?: number }) => {
    const qs = new URLSearchParams();
    if (p.type) qs.set("type", p.type);
    if (p.decision) qs.set("decision", p.decision);
    if (p.q) qs.set("q", p.q);
    qs.set("page", String(p.page ?? 1));
    qs.set("page_size", String(p.page_size ?? 50));
    return req<InsightsHistory>(`/api/v1/insights/history?${qs.toString()}`);
  },
  insightsStats: (window = 30, metric?: "plays" | "duration") =>
    req<InsightsStats>(`/api/v1/insights/stats?window=${window}${metric ? `&metric=${metric}` : ""}`),
  insightsUsers: () => req<{ users: UserEntry[] }>("/api/v1/insights/users").then((r) => r.users),
  insightsLibraries: () => req<{ libraries: LibraryStat[] }>("/api/v1/insights/libraries").then((r) => r.libraries),
  insightsRecentlyAdded: (limit = 20) => req<{ items: RecentItem[] }>(`/api/v1/insights/recently-added?limit=${limit}`).then((r) => r.items),
  insightsGraphs: (window = 30) => req<InsightsGraphs>(`/api/v1/insights/graphs?window=${window}`),
  insightsReliability: (window = 30) => req<Reliability>(`/api/v1/insights/reliability?window=${window}`),
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
  movieReleases: (id: number) => req<ReleaseList>(`/api/v1/movies/${id}/releases`),
  blocklist: (id: number) => req<{ blocklist: BlockEntry[] }>(`/api/v1/movies/${id}/blocklist`).then((r) => r.blocklist),
  // Block a search result by its token, or any release by title alone.
  blockRelease: (id: number, body: { token: string; search_again?: boolean } | { title: string; indexer?: string; search_again?: boolean }) =>
    req<{ status: string }>(`/api/v1/movies/${id}/blocklist`, { method: "POST", body: JSON.stringify(body) }),
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
    req<{ quality_profile: string; downgrade: boolean; downgrade_reason?: string; downgrade_kind?: "smaller" | "different"; downgrade_ceiling?: string }>(`/api/v1/movies/${id}/profile`, {
      method: "PUT",
      body: JSON.stringify({ quality_profile }),
    }),
  regrabMovie: (id: number) => req<{ status: string } & JobRef>(`/api/v1/movies/${id}/regrab`, { method: "POST" }),
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
  manualImport: (id: number, path: string) =>
    req<{ status: string }>(`/api/v1/movies/${id}/manualimport`, {
      method: "POST",
      body: JSON.stringify({ path }),
    }),
  renamePreview: (id: number) =>
    req<{ current: string; proposed: string; matches: boolean }>(`/api/v1/movies/${id}/rename`),
  renameMovie: (id: number) => req<{ status: string }>(`/api/v1/movies/${id}/rename`, { method: "POST" }),
  addVersion: (id: number, body: { label: string; quality_profile: string; edition?: string; monitored?: boolean }) =>
    req<MovieVersion>(`/api/v1/movies/${id}/versions`, { method: "POST", body: JSON.stringify(body) }),
  updateVersion: (id: number, vid: number, body: { label: string; quality_profile: string; edition?: string; monitored: boolean }) =>
    req<{ status: string }>(`/api/v1/movies/${id}/versions/${vid}`, { method: "PUT", body: JSON.stringify(body) }),
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
}

export interface CastMember {
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
}
export interface ArtistStats {
  albums: number;
  tracks: number;
  have_tracks: number;
  size_bytes: number;
}
export interface MusicTrack {
  id: number;
  album_id: number;
  disc_number: number;
  track_number: number;
  title: string;
  duration_sec?: number;
  monitored: boolean;
  has_file: boolean;
  file_path?: string;
  format?: string;
  size_bytes?: number;
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
  track_count: number;
  have_tracks: number;
  size_bytes: number;
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
  albums?: MusicAlbum[];
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
  /** A usenet release is listed but never grabbable: there is no usenet client. */
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
  indexer_issues?: IndexerIssue[];
  /** How many indexers were asked. */
  searched?: number;
}

export interface Movie {
  id: number;
  tmdb_id: number;
  imdb_id?: string;
  title: string;
  year: number;
  overview?: string;
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
  download?: { state: string; progress: number };
  /** Detail only: whether the upgrade sweep will look at this movie (monitored, has a file, profile upgrades). */
  upgrades_allowed?: boolean;
  /** The default file is kept out of profile-driven upgrades ("keep existing files"). */
  upgrade_hold?: boolean;
  /** Detail only: what Arrmada will do about this movie, from the facts the sweeps act on. */
  acquisition?: MovieAcquisition;
  // Detail only (MOV-04): the missing-sweep's backoff, when it next searches (absent when
  // no automatic search is coming) and how the stored searches went.
  last_search_at?: string;
  search_misses?: number;
  next_search_at?: string;
  last_search?: AttemptSummary;
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
}

export interface ImportRecord {
  hash: string;
  source_path: string;
  target_path: string;
  title: string;
  size_bytes: number;
  imported_at: string;
}

export type ReviewKind = "series" | "movie" | "book" | "music";

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
}

// Why a download is held: content that doesn't match, tied to nothing, unreadable episode
// numbering, an import that keeps failing, or nothing importable inside.
export type ReviewReason = "mismatch" | "unmatched" | "numbering" | "import_failed" | "no_media";

// One file inside a held download, with what its name says about episode numbering.
export interface ReviewFile {
  rel_path: string;
  size: number;
  video: boolean;
  guess: { season?: number; episodes?: number[]; absolute?: number[] };
}
