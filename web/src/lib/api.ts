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
  auth_enabled: boolean;
  needs_setup: boolean;
  authenticated: boolean;
  modules: Module[];
  books_enabled: boolean;
  music_enabled: boolean;
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
  priority: number;
  min_seeders?: number;
  seed_enabled?: boolean;
  seed_ratio?: number;
  seed_hours?: number;
  enabled: boolean;
}

export interface NewIndexer {
  name: string;
  kind: string;
  url?: string;
  api_key?: string;
  username?: string;
  password?: string;
  categories?: number[];
  priority?: number;
  min_seeders?: number;
  seed_enabled?: boolean;
  seed_ratio?: number;
  seed_hours?: number;
  enabled?: boolean;
}

export interface Release {
  title: string;
  download_url: string;
  info_hash?: string;
  size_bytes: number;
  seeders?: number;
  peers?: number;
  indexer: string;
  protocol: string;
  categories?: number[];
}

export interface SearchResult {
  releases: Release[];
  errors?: Record<string, string>;
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
}

export interface SearchingItem {
  movie_id: number;
  title: string;
  year: number;
  poster_url?: string;
  quality_profile: string;
}

export interface ActivityDownload {
  hash: string;
  name: string;
  state: string;
  progress: number;
  size_bytes: number;
  down_speed: number;
  up_speed: number;
  eta_seconds: number;
  ratio: number;
  quality_profile: string;
  media_type?: string;
}

export interface ActivityFeed {
  searching: SearchingItem[];
  downloads: ActivityDownload[];
  totals?: { down_speed: number; up_speed: number; active: number };
  free_gb?: number;
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
  custom_formats?: QualityCustomFormat[];
  keywords?: { term: string; score: number }[];
  rejected?: string[];
  min_seeders: number;
  stall_minutes: number;
  upgrades_enabled: boolean;
  upgrade_gb: number;
}

export interface DownloadClient {
  id: number;
  name: string;
  kind: string;
  url: string;
  username?: string;
  category?: string;
  enabled: boolean;
}

export interface NewDownloadClient {
  name: string;
  kind: string;
  url: string;
  username?: string;
  password?: string;
  category?: string;
}

export interface NotificationConn {
  id?: number;
  name: string;
  kind: string; // "discord" | "webhook"
  url: string;
  on_grab: boolean;
  on_import: boolean;
  enabled: boolean;
}

export interface HealthWarning {
  level: string; // "error" | "warning"
  message: string;
}

export interface SystemHealth {
  status: string; // "ok" | "warning" | "error"
  warnings: HealthWarning[];
  disk?: { free_gb: string; path: string };
}

export interface AppSettings {
  search_on_add: boolean;
  naming_movie_folder: string;
  naming_movie_file: string;
  write_nfo: boolean;
  download_artwork: boolean;
  books_enabled: boolean;
  music_enabled: boolean;
  // Convert module (focused model: target codec + subs + schedule + safety).
  convert_target_codec: string;
  convert_auto: boolean;
  convert_extract_subs: boolean;
  convert_skip_hardlinked: boolean;
  convert_keep_audio_langs: string;
  convert_add_stereo: boolean;
  convert_loudnorm: boolean;
  convert_quality_gate: boolean;
  convert_min_ssim: string;
  convert_workers: string;
  convert_sweep_start: string;
  convert_sweep_end: string;
  convert_max_failures: string;
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
  created_at: string;
  updated_at: string;
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
  in_library: boolean;
  has_file: boolean;
  request_status?: "pending" | "approved" | "declined";
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
  auto_approve: boolean;
  created_at?: string;
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
}

// --- Series (TV) ---
export interface SeriesLookup {
  tmdb_id: number;
  title: string;
  year: number;
  overview: string;
  poster_url: string;
  vote_average: number;
}
export interface SeriesStats {
  episodes: number;
  have_files: number;
  size_bytes: number;
  seasons: number;
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
  monitored: boolean;
  has_file: boolean;
  size_bytes?: number;
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
  monitored: boolean;
  quality_profile: string;
  added_at?: string;
  extra?: SeriesExtra;
  seasons?: Season[];
  stats?: SeriesStats;
}
// --- Books ---
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
  in_library: boolean;
  has_file: boolean;
  requested: boolean;
}
export interface BookAuthor {
  key: string;
  name: string;
  work_count: number;
  top_work?: string;
  birth_date?: string;
}
export interface BookMeta {
  key: string;
  title: string;
  author: string;
  year: number;
  cover_url?: string;
  description?: string;
  subjects?: string[];
}

export interface SeriesImportCandidate {
  path: string;
  filename: string;
  season: number;
  episode: number;
  size_bytes: number;
  quality?: string;
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
}
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
  container: string; video_codec: string; width: number; height: number; resolution: string; hdr: string;
  bitrate_kbps: number; frame_rate: number; duration_sec: number; size_bytes: number; audio_tracks: number; sub_tracks: number; ten_bit: boolean;
}
export interface ConvertCandidate { movie_id: number; title: string; year: number; poster_url?: string; path: string; info?: ConvertMediaInfo; candidate: boolean; est_bytes: number }
export interface ConvertSample { movie_id: number; title: string; src_bytes: number; est_bytes: number; percent: number; sample_sec: number }
export interface ConvertJob { id: number; movie_id: number; title: string; state: string; progress: number; fps: number; speed_x: number; encoder: string; src_bytes: number; out_bytes: number; note?: string }

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

async function req<T>(path: string, opts?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    ...opts,
  });
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const body = (await res.json()) as { message?: string };
      if (body.message) msg = body.message;
    } catch {
      /* non-JSON error */
    }
    throw new Error(msg);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const api = {
  status: () => req<Status>("/api/v1/status"),
  health: () => req<Health>("/api/health"),

  me: () => req<{ user: AuthUser }>("/api/v1/auth/me").then((r) => r.user),
  logout: () => req<unknown>("/api/v1/auth/logout", { method: "POST" }),
  login: (username: string, password: string) =>
    req<{ user: AuthUser }>("/api/v1/auth/login", { method: "POST", body: JSON.stringify({ username, password }) }),
  setupAdmin: (username: string, password: string) =>
    req<{ user: AuthUser }>("/api/v1/auth/setup", { method: "POST", body: JSON.stringify({ username, password }) }),
  users: () => req<{ users: AuthUser[] }>("/api/v1/users").then((r) => r.users),
  createUser: (body: { email: string; password: string; role: string; auto_approve: boolean }) =>
    req<AuthUser>("/api/v1/users", { method: "POST", body: JSON.stringify(body) }),
  updateUser: (id: number, body: { role?: string; auto_approve?: boolean; password?: string }) =>
    req<{ id: number; role: string; auto_approve: boolean }>(`/api/v1/users/${id}`, { method: "PUT", body: JSON.stringify(body) }),
  deleteUser: (id: number) => req<void>(`/api/v1/users/${id}`, { method: "DELETE" }),

  indexers: () => req<{ indexers: Indexer[] }>("/api/v1/indexers").then((r) => r.indexers),
  createIndexer: (body: NewIndexer) =>
    req<Indexer>("/api/v1/indexers", { method: "POST", body: JSON.stringify(body) }),
  updateIndexer: (id: number, body: NewIndexer) =>
    req<void>(`/api/v1/indexers/${id}`, { method: "PUT", body: JSON.stringify(body) }),
  deleteIndexer: (id: number) => req<void>(`/api/v1/indexers/${id}`, { method: "DELETE" }),
  testIndexer: (id: number) =>
    req<{ ok: boolean; error?: string }>(`/api/v1/indexers/${id}/test`, { method: "POST" }),
  prowlarrInfo: () => req<{ url: string; has_key: boolean }>("/api/v1/indexers/prowlarr"),
  syncProwlarr: (body: { url: string; api_key: string }) =>
    req<{ synced: number; flaresolverr_ready: boolean }>("/api/v1/indexers/prowlarr/sync", { method: "POST", body: JSON.stringify(body) }),

  search: (q: string) => req<SearchResult>(`/api/v1/search?q=${encodeURIComponent(q)}`),

  activity: () => req<ActivityFeed>("/api/v1/downloads"),
  pauseDownload: (hash: string) => req<{ status: string }>(`/api/v1/queue/${hash}/pause`, { method: "POST" }),
  resumeDownload: (hash: string) => req<{ status: string }>(`/api/v1/queue/${hash}/resume`, { method: "POST" }),
  deleteDownload: (hash: string, deleteData: boolean) =>
    req<void>(`/api/v1/queue/${hash}${deleteData ? "?delete_data=true" : ""}`, { method: "DELETE" }),
  blockDownload: (hash: string, name: string) =>
    req<{ status: string }>(`/api/v1/queue/${hash}/block`, { method: "POST", body: JSON.stringify({ name }) }),
  torrentAction: (hash: string, action: "recheck" | "reannounce" | "prio_up" | "prio_down") =>
    req<{ status: string }>(`/api/v1/queue/${hash}/action`, { method: "POST", body: JSON.stringify({ action }) }),
  clientSettings: (id: number) => req<ClientSettings>(`/api/v1/downloadclients/${id}/settings`),
  setClientSettings: (id: number, body: ClientSettings) =>
    req<{ status: string }>(`/api/v1/downloadclients/${id}/settings`, { method: "PUT", body: JSON.stringify(body) }),
  qualityProfiles: (media: string) =>
    req<{ profiles: QualityProfileInfo[]; formats: FormatInfo[] }>(`/api/v1/quality/profiles?media=${media}`),
  setDefaultProfile: (media: string, profile: string) =>
    req<{ media: string; profile: string }>("/api/v1/quality/default", { method: "POST", body: JSON.stringify({ media, profile }) }),
  qualityProfile: (ref: string) => req<StoredProfile>(`/api/v1/quality/profiles/${encodeURIComponent(ref)}`),
  createQualityProfile: (sp: StoredProfile) =>
    req<StoredProfile>("/api/v1/quality/profiles", { method: "POST", body: JSON.stringify(sp) }),
  updateQualityProfile: (id: number, sp: StoredProfile) =>
    req<{ status: string }>(`/api/v1/quality/profiles/${id}`, { method: "PUT", body: JSON.stringify(sp) }),
  deleteQualityProfile: (id: number) =>
    req<void>(`/api/v1/quality/profiles/${id}`, { method: "DELETE" }),
  qualityPreviewSpec: (sp: StoredProfile) =>
    req<QualityPreview>("/api/v1/quality/preview", { method: "POST", body: JSON.stringify(sp) }),

  downloadClients: () =>
    req<{ clients: DownloadClient[] }>("/api/v1/downloadclients").then((r) => r.clients),
  createDownloadClient: (body: NewDownloadClient) =>
    req<DownloadClient>("/api/v1/downloadclients", { method: "POST", body: JSON.stringify(body) }),
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

  systemHealth: () =>
    req<SystemHealth>("/api/v1/health/system"),

  queue: () => req<{ items: QueueItem[] }>("/api/v1/queue").then((r) => r.items),

  grab: (release: { indexer: string; download_url: string; title: string; movie_id?: number }) =>
    req<{ status: string; title: string }>("/api/v1/grab", {
      method: "POST",
      body: JSON.stringify(release),
    }),

  history: () => req<{ imports: ImportRecord[] }>("/api/v1/history").then((r) => r.imports),

  movies: () => req<{ movies: Movie[]; metadata_available: boolean }>("/api/v1/movies"),
  lookupMovies: (q: string) =>
    req<{ results: MovieLookup[] }>(`/api/v1/movies/lookup?q=${encodeURIComponent(q)}`).then((r) => r.results),
  settings: () => req<AppSettings>("/api/v1/settings"),
  updateSettings: (body: Partial<AppSettings>) =>
    req<AppSettings>("/api/v1/settings", { method: "PUT", body: JSON.stringify(body) }),
  scanLibrary: () => req<{ status: string }>("/api/v1/movies/scan", { method: "POST" }),
  addMovie: (body: { tmdb_id: number; quality_profile: string; monitored?: boolean; search_on_add?: boolean }) =>
    req<Movie>("/api/v1/movies", { method: "POST", body: JSON.stringify(body) }),
  deleteMovie: (id: number, deleteFiles?: boolean) =>
    req<void>(`/api/v1/movies/${id}${deleteFiles ? "?delete_files=true" : ""}`, { method: "DELETE" }),
  searchMovie: (id: number) =>
    req<{ status: string }>(`/api/v1/movies/${id}/search`, { method: "POST" }),
  movie: (id: number) => req<Movie>(`/api/v1/movies/${id}`),
  movieCollection: (id: number) =>
    req<{ name: string; members: CollectionMember[] }>(`/api/v1/movies/${id}/collection`),

  series: () => req<{ series: Series[]; metadata_available: boolean }>("/api/v1/series"),
  lookupSeries: (q: string) =>
    req<{ results: SeriesLookup[] }>(`/api/v1/series/lookup?q=${encodeURIComponent(q)}`).then((r) => r.results),
  addSeries: (body: { tmdb_id: number; quality_profile?: string; monitored?: boolean; search_on_add?: boolean }) =>
    req<Series>("/api/v1/series", { method: "POST", body: JSON.stringify(body) }),
  seriesDetail: (id: number) => req<Series>(`/api/v1/series/${id}`),
  searchSeries: (id: number) =>
    req<{ status: string }>(`/api/v1/series/${id}/search`, { method: "POST" }),
  seriesReleases: (id: number, season?: number, episode?: number) => {
    const q = new URLSearchParams();
    if (season) q.set("season", String(season));
    if (episode) q.set("episode", String(episode));
    const qs = q.toString();
    return req<ReleaseList>(`/api/v1/series/${id}/releases${qs ? `?${qs}` : ""}`);
  },
  grabSeries: (id: number, body: { indexer?: string; download_url: string; title: string }) =>
    req<{ status: string }>(`/api/v1/series/${id}/grab`, { method: "POST", body: JSON.stringify(body) }),
  autoGrabSeries: (id: number, season: number, episode: number) =>
    req<{ status: string }>(`/api/v1/series/${id}/autograb`, { method: "POST", body: JSON.stringify({ season, episode }) }),
  refreshSeries: (id: number) => req<Series>(`/api/v1/series/${id}/refresh`, { method: "POST" }),
  // Requests
  requests: (status?: string) =>
    req<{ requests: MediaRequest[]; auto_approve: boolean }>(`/api/v1/requests${status ? `?status=${status}` : ""}`),
  createRequest: (body: { media_type: "movie" | "series" | "book"; tmdb_id?: number; ol_key?: string; author?: string; title: string; year: number; poster_url?: string; overview?: string; quality_profile?: string; note?: string }) =>
    req<MediaRequest>("/api/v1/requests", { method: "POST", body: JSON.stringify(body) }),
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
  discoverUpcoming: () =>
    req<{ items: DiscoverCard[] }>(`/api/v1/discover/upcoming`).then((r) => r.items),
  discoverByGenre: (media: string, genre: number) =>
    req<{ items: DiscoverCard[] }>(`/api/v1/discover?media=${media}&genre=${genre}`).then((r) => r.items),
  discoverGenres: (media: string) =>
    req<{ genres: Genre[] }>(`/api/v1/discover/genres?media=${media}`).then((r) => r.genres),
  mediaDetail: (media: string, tmdbId: number) =>
    req<MediaDetail>(`/api/v1/media/${media}/${tmdbId}`),
  discoverSearch: (q: string) =>
    req<{ items: DiscoverCard[] }>(`/api/v1/discover/search?q=${encodeURIComponent(q)}`).then((r) => r.items),

  seriesManualImportList: (id: number) =>
    req<{ candidates: SeriesImportCandidate[] }>(`/api/v1/series/${id}/manualimport`),
  seriesManualImport: (id: number, path: string) =>
    req<{ status: string }>(`/api/v1/series/${id}/manualimport`, { method: "POST", body: JSON.stringify({ path }) }),
  seriesRenamePreview: (id: number) =>
    req<{ items: { from: string; to: string }[]; matches: boolean }>(`/api/v1/series/${id}/rename`),
  renameSeries: (id: number) =>
    req<{ renamed: number }>(`/api/v1/series/${id}/rename`, { method: "POST" }),
  setSeriesMonitored: (id: number, monitored: boolean) =>
    req<{ monitored: boolean }>(`/api/v1/series/${id}/monitor`, { method: "PUT", body: JSON.stringify({ monitored }) }),
  setSeriesProfile: (id: number, quality_profile: string) =>
    req<{ quality_profile: string }>(`/api/v1/series/${id}/profile`, { method: "PUT", body: JSON.stringify({ quality_profile }) }),
  setSeasonMonitored: (id: number, season: number, monitored: boolean) =>
    req<{ monitored: boolean }>(`/api/v1/series/${id}/seasons/${season}/monitor`, { method: "PUT", body: JSON.stringify({ monitored }) }),
  setEpisodeMonitored: (eid: number, monitored: boolean) =>
    req<{ monitored: boolean }>(`/api/v1/series/episodes/${eid}/monitor`, { method: "PUT", body: JSON.stringify({ monitored }) }),
  deleteSeries: (id: number, deleteFiles?: boolean) =>
    req<void>(`/api/v1/series/${id}${deleteFiles ? "?delete_files=true" : ""}`, { method: "DELETE" }),
  seriesBlocklist: (id: number) => req<{ blocklist: BlockEntry[] }>(`/api/v1/series/${id}/blocklist`).then((r) => r.blocklist),
  unblockSeries: (id: number, bid: number) => req<void>(`/api/v1/series/${id}/blocklist/${bid}`, { method: "DELETE" }),
  regrabEpisode: (id: number, season: number, episode: number) =>
    req<{ status: string }>(`/api/v1/series/${id}/seasons/${season}/episodes/${episode}/regrab`, { method: "POST" }),
  deleteEpisodeFile: (id: number, season: number, episode: number) =>
    req<void>(`/api/v1/series/${id}/seasons/${season}/episodes/${episode}/file`, { method: "DELETE" }),

  // Books
  books: () => req<{ books: Book[]; metadata_available: boolean }>("/api/v1/books"),
  lookupBooks: (q: string) =>
    req<{ results: BookLookup[] }>(`/api/v1/books/lookup?q=${encodeURIComponent(q)}`).then((r) => r.results),
  addBook: (body: { ol_key: string; quality_profile?: string; monitored?: boolean; search_on_add?: boolean; title?: string; author?: string; year?: number; cover_url?: string }) =>
    req<Book>("/api/v1/books", { method: "POST", body: JSON.stringify(body) }),
  bookDetail: (id: number) => req<Book>(`/api/v1/books/${id}`),
  searchBook: (id: number) => req<{ status: string }>(`/api/v1/books/${id}/search`, { method: "POST" }),
  refreshBook: (id: number) => req<Book>(`/api/v1/books/${id}/refresh`, { method: "POST" }),
  bookReleases: (id: number) => req<ReleaseList>(`/api/v1/books/${id}/releases`),
  grabBook: (id: number, body: { indexer?: string; download_url: string; title: string }) =>
    req<{ status: string }>(`/api/v1/books/${id}/grab`, { method: "POST", body: JSON.stringify(body) }),
  bookManualImportList: (id: number) =>
    req<{ candidates: BookImportCandidate[] }>(`/api/v1/books/${id}/manualimport`),
  bookManualImport: (id: number, path: string) =>
    req<{ status: string }>(`/api/v1/books/${id}/manualimport`, { method: "POST", body: JSON.stringify({ path }) }),
  renameBook: (id: number) => req<{ renamed: number }>(`/api/v1/books/${id}/rename`, { method: "POST" }),
  deleteBookFile: (id: number, edition: "ebook" | "audiobook") =>
    req<{ status: string }>(`/api/v1/books/${id}/file?edition=${edition}`, { method: "DELETE" }),
  scanBooks: () => req<{ status: string }>("/api/v1/books/scan", { method: "POST" }),
  bookEditionFiles: (id: number, edition: "ebook" | "audiobook") =>
    req<{ files: BookFileEntry[] }>(`/api/v1/books/${id}/edition-files?edition=${edition}`).then((r) => r.files),
  mergeAudiobook: (id: number) =>
    req<{ status: string }>(`/api/v1/books/${id}/merge-audiobook`, { method: "POST" }),
  bookCovers: (id: number) =>
    req<{ covers: string[] }>(`/api/v1/books/${id}/covers`).then((r) => r.covers),
  setBookCover: (id: number, url: string) =>
    req<{ cover_url: string }>(`/api/v1/books/${id}/cover`, { method: "PUT", body: JSON.stringify({ url }) }).then((r) => r.cover_url),
  // Books Discover
  bookDiscoverTrending: () =>
    req<{ books: BookDiscoverCard[] }>("/api/v1/books/discover/trending").then((r) => r.books),
  bookDiscoverSearch: (q: string) =>
    req<{ authors: BookAuthor[]; books: BookDiscoverCard[] }>(`/api/v1/books/discover/search?q=${encodeURIComponent(q)}`),
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
    const res = await fetch(`/api/v1/books/${id}/cover`, { method: "POST", body: fd });
    if (!res.ok) {
      let msg = `HTTP ${res.status}`;
      try {
        const b = (await res.json()) as { message?: string };
        if (b.message) msg = b.message;
      } catch {
        /* non-JSON error */
      }
      throw new Error(msg);
    }
    return ((await res.json()) as { cover_url: string }).cover_url;
  },
  setBookMonitored: (id: number, monitored: boolean) =>
    req<{ monitored: boolean }>(`/api/v1/books/${id}/monitor`, { method: "PUT", body: JSON.stringify({ monitored }) }),
  setBookProfile: (id: number, quality_profile: string) =>
    req<{ quality_profile: string }>(`/api/v1/books/${id}/profile`, { method: "PUT", body: JSON.stringify({ quality_profile }) }),
  overrideBookMetadata: (id: number, body: { title: string; author: string; year: number; overview: string; cover_url: string }) =>
    req<{ status: string }>(`/api/v1/books/${id}/metadata`, { method: "PUT", body: JSON.stringify(body) }),
  deleteBook: (id: number, deleteFiles?: boolean) =>
    req<void>(`/api/v1/books/${id}${deleteFiles ? "?delete_files=true" : ""}`, { method: "DELETE" }),

  // Subtitles
  subtitleSettings: () => req<SubtitleSettings>("/api/v1/subtitles/settings"),
  updateSubtitleSettings: (body: { movies_auto?: boolean; series_auto?: boolean; languages?: string[] }) =>
    req<SubtitleSettings>("/api/v1/subtitles/settings", { method: "PUT", body: JSON.stringify(body) }),
  subtitleMovies: () => req<{ movies: MovieSubStatus[] }>("/api/v1/subtitles/movies").then((r) => r.movies),
  subtitleSeries: () => req<{ series: SeriesSubStatus[] }>("/api/v1/subtitles/series").then((r) => r.series),
  searchMovieSubs: (id: number) => req<{ status: string }>(`/api/v1/subtitles/movies/${id}/search`, { method: "POST" }),
  searchSeriesSubs: (id: number) => req<{ status: string }>(`/api/v1/subtitles/series/${id}/search`, { method: "POST" }),

  // Convert
  convertHardware: () => req<{ encoders: ConvertEncoder[]; selected: ConvertEncoder; reclaimed_bytes: number }>("/api/v1/convert/hardware"),
  convertSweep: () => req<{ status: string }>("/api/v1/convert/sweep", { method: "POST" }),
  convertLibrary: () => req<{ items: ConvertCandidate[] }>("/api/v1/convert/library").then((r) => r.items),
  convertJobs: () => req<{ jobs: ConvertJob[] }>("/api/v1/convert/jobs").then((r) => r.jobs),
  convertMovie: (id: number) => req<ConvertJob>(`/api/v1/convert/movies/${id}`, { method: "POST" }),
  convertSampleMovie: (id: number) => req<ConvertSample>(`/api/v1/convert/movies/${id}/sample`, { method: "POST" }),

  // Insights (Plex)
  insightsConfig: () => req<PlexConfig>("/api/v1/insights/plex"),
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
  scanSeries: () => req<{ status: string }>("/api/v1/series/scan", { method: "POST" }),
  seriesHistory: (id: number) =>
    req<{ events: MovieEvent[] }>(`/api/v1/series/${id}/history`).then((r) => r.events),
  movieReleases: (id: number) => req<ReleaseList>(`/api/v1/movies/${id}/releases`),
  blocklist: (id: number) => req<{ blocklist: BlockEntry[] }>(`/api/v1/movies/${id}/blocklist`).then((r) => r.blocklist),
  blockRelease: (id: number, body: { title: string; indexer?: string; download_url?: string; search_again?: boolean }) =>
    req<{ status: string }>(`/api/v1/movies/${id}/blocklist`, { method: "POST", body: JSON.stringify(body) }),
  unblock: (id: number, bid: number) => req<void>(`/api/v1/movies/${id}/blocklist/${bid}`, { method: "DELETE" }),
  setMonitored: (id: number, monitored: boolean) =>
    req<{ monitored: boolean }>(`/api/v1/movies/${id}/monitor`, {
      method: "PUT",
      body: JSON.stringify({ monitored }),
    }),
  deleteMovieFile: (id: number) => req<void>(`/api/v1/movies/${id}/file`, { method: "DELETE" }),
  setQualityProfile: (id: number, quality_profile: string) =>
    req<{ quality_profile: string; downgrade: boolean }>(`/api/v1/movies/${id}/profile`, {
      method: "PUT",
      body: JSON.stringify({ quality_profile }),
    }),
  regrabMovie: (id: number) => req<{ status: string }>(`/api/v1/movies/${id}/regrab`, { method: "POST" }),
  refreshMovie: (id: number) => req<Movie>(`/api/v1/movies/${id}/refresh`, { method: "POST" }),
  movieHistory: (id: number) =>
    req<{ events: MovieEvent[] }>(`/api/v1/movies/${id}/history`).then((r) => r.events),
  setAvailability: (id: number, min_availability: string) =>
    req<{ min_availability: string }>(`/api/v1/movies/${id}/availability`, {
      method: "PUT",
      body: JSON.stringify({ min_availability }),
    }),
  manualImportList: (id: number, path?: string) =>
    req<{ path: string; candidates: ImportCandidate[] }>(
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
  subtitles?: string[];
  missing: boolean;
}

export interface RankedRelease {
  title: string;
  indexer: string;
  download_url: string;
  size_gb: number;
  bitrate_mbps?: number;
  seeders: number;
  summary: string;
  eligible: boolean;
  reject_reason?: string;
  recommended: boolean;
  blocklisted?: boolean;
}

export interface BlockEntry {
  id: number;
  title: string;
  indexer?: string;
  reason?: string;
  created_at: string;
}

export interface ReleaseList {
  profile: string;
  why?: string[];
  releases: RankedRelease[];
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
