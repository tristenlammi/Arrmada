import type {
  Artist, ConvertEncoder, ConvertJob, ConvertLibraryStats, ConvertMediaStats, ConvertSettings, ConvertStatus, InsightsStats, PlexConfig,
  SubtitleCoverage, SubtitleJob, SubtitleSettings,
} from "../../src/lib/api";

// The optional modules (Convert, Subtitles, Insights, Music), idle and empty: the
// admin smoke only needs each page to load cleanly.

const zeroStats: ConvertMediaStats = {
  files: 0, convertible: 0, total_bytes: 0, est_bytes: 0, convertible_bytes: 0, reclaimable: 0, skipped: 0,
  hdr10: 0, hdr10_plus: 0, dolby_vision: 0, hlg: 0, h264: 0, hevc: 0, av1: 0, other: 0,
};

export const convertStats: ConvertLibraryStats = { movies: zeroStats, tv: zeroStats, total: zeroStats };

const encoders: ConvertEncoder[] = [{ codec: "hevc", name: "libx265", kind: "cpu", label: "x265 (CPU)", hardware: false, available: true }];

export const convertHardware = {
  encoders, using: "libx265", reclaimed_bytes: 0, scratch_dir: "/scratch", scratch_free_bytes: 500e9,
  render_devices: [] as { path: string; pci: string; vendor: string }[], vaapi_device: "",
};

export const convertSettings: ConvertSettings = {
  auto: false, hours_start: "01:00", hours_end: "07:00", allow_av1: false, use_gpu: false, pause_watching: true,
  keep_audio_langs: "en", keep_original_lang: true, drop_commentary: false, keep_sub_langs: "en",
  image_subs: "keep", tidy_tracks: true, crop: false, scratch_dir: "/scratch", vaapi_device: "", cpu_cores: 8, workers: 1,
  scan_at: "03:00", server_time: "12:00", server_tz: "UTC", plex_watching_known: false, can_pause: false,
  has_gpu: false, gpu_does_av1: false, hdr10plus_tool: false,
};

export const convertStatus: ConvertStatus = { state: "off", message: "Converting is off", auto: false, watching: false, requests: [], up_next: [], remaining: 0 };
export const convertJobs: { jobs: ConvertJob[] } = { jobs: [] };

export const subtitleCoverage: SubtitleCoverage = {
  files: 0, covered: 0, missing: 0, movies: { files: 0, covered: 0, missing: 0 }, tv: { files: 0, covered: 0, missing: 0 },
  scanned_at: 0, scanning: false,
};

export const subtitleSettings: SubtitleSettings = {
  movies_auto: false, series_auto: false, languages: ["en"], provider_ready: false, can_download: false, ai_ready: false,
  ai_backend: "", ai_note: "", quota_remaining: 0, quota_reset_at: 0, pending: 0,
};

export const subtitleJobs: { jobs: SubtitleJob[] } = { jobs: [] };

export const plexConfig: PlexConfig = { url: "", token_set: false, enabled: false, poll_seconds: 30, status: "unconfigured" };

// No imported watch history either, so Insights shows its Connect state and nothing else.
export const insightsStats: InsightsStats = {
  most_watched_movies: [], most_watched_shows: [], most_active_users: [], most_active_platforms: [], recently_watched: [],
};

export const artists: { artists: Artist[] } = { artists: [] };
