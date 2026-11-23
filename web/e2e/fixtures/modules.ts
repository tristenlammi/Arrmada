import type {
  Artist, ConvertDevices, ConvertEncoder, ConvertHistoryEntry, ConvertJob, ConvertLibraryStats, ConvertMediaStats, ConvertSettings, ConvertStatus, ImportRun, InsightsStats, Job,
  PlexConfig, SubtitleCoverage, SubtitleJob, SubtitleSettings, TautulliConfig, WhisperStatus,
} from "../../src/lib/api";
import { NOW } from "./clock";
import type { PlexScanView } from "../../src/lib/plexLibraryApi";
import type { Language } from "../../src/lib/languages";
import type { ProfileList } from "../../src/pages/subtitles/profileApi";
import type { SubFileView } from "../../src/lib/subtitleFileApi";
import type { MusicMatching, ReleaseDefaults, ReleaseTypeChoices } from "../../src/lib/musicApi";

// The optional modules (Convert, Subtitles, Insights, Music), idle and empty: the
// admin smoke only needs each page to load cleanly.

const zeroStats: ConvertMediaStats = {
  files: 0, convertible: 0, total_bytes: 0, est_bytes: 0, convertible_bytes: 0, reclaimable: 0, skipped: 0,
  hdr10: 0, hdr10_plus: 0, dolby_vision: 0, hlg: 0, h264: 0, hevc: 0, av1: 0, other: 0, dolby_vision_files: 0,
};

export const convertStats: ConvertLibraryStats = { movies: zeroStats, tv: zeroStats, total: zeroStats };

const encoders: ConvertEncoder[] = [{ codec: "hevc", name: "libx265", kind: "cpu", label: "x265 (CPU)", hardware: false, available: true }];

export const convertHardware = {
  encoders, using: "libx265", reclaimed_bytes: 0, scratch_dir: "/scratch", scratch_free_bytes: 500e9,
  render_devices: [] as { path: string; pci: string; vendor: string }[], vaapi_device: "",
  hold_check: [{ root: "/movies", label: "Movies", instant: true }],
  hold: { held_bytes: 0, held_saving_bytes: 0, held_count: 0, budget_bytes: 500 * 2 ** 30, hold_days: 14 },
};

export const convertSettings: ConvertSettings = {
  auto: false, hours_start: "01:00", hours_end: "07:00", allow_av1: false, use_gpu: false, pause_watching: true,
  keep_audio_langs: "en", keep_original_lang: true, drop_commentary: false, keep_sub_langs: "en",
  image_subs: "keep", tidy_tracks: true, crop: false, dolby_vision: "drop", ten_bit: "match_source", resumable: false, hold_days: 14, hold_max_gb: 500, scratch_dir: "/scratch", vaapi_device: "", cpu_cores: 8, workers: 1,
  scan_at: "03:00", server_time: "12:00", server_tz: "UTC", plex_watching_known: false, can_pause: false,
  has_gpu: false, gpu_does_av1: false, gpu_hdr: [], gpu_hdr_av1: [], hdr10plus_tool: false,
};

export const convertStatus: ConvertStatus = { state: "off", message: "Converting is off", auto: false, watching: false, requests: [], up_next: [], remaining: 0 };
export const convertJobs: { jobs: ConvertJob[] } = { jobs: [] };
export const convertHistory: { items: ConvertHistoryEntry[]; next: string } = { items: [], next: "" };
export const convertDevices: ConvertDevices = { monitoring: true, since: 0, devices: [] };

export const subtitleCoverage: SubtitleCoverage = {
  files: 0, covered: 0, missing: 0, movies: { files: 0, covered: 0, missing: 0 }, tv: { files: 0, covered: 0, missing: 0 },
  scanned_at: 0, scanning: false,
};

export const subtitleSettings: SubtitleSettings = {
  movies_auto: false, series_auto: false, languages: ["en"], provider_ready: false, can_download: false, ai_ready: false,
  ai_backend: "", ai_note: "", quota_remaining: 0, quota_reset_at: 0, pending: 0, min_score: 60,
  provider_state: "not_configured", provider_detail: "Add an OpenSubtitles API key to search, plus the account's username and password to download.", vip: false, allowed_downloads: -1, upgraded_week: 0,
};

// GET /subtitles/attention: nothing stuck, a few subtitles of known origin.
export const subtitleAttention = { buckets: [], total: 0, sources: { embedded: 3, opensubtitles: 2 }, untracked: 1, scanned: true };

// GET /subtitles/jobs: the live list plus the AI backlog (none here).
export const subtitleJobs: { jobs: SubtitleJob[]; gpu_backlog: number; gpu_backlog_audio_sec: number; gpu_eta_sec: number; rtf: number; next_before: number } =
  { jobs: [], gpu_backlog: 0, gpu_backlog_audio_sec: 0, gpu_eta_sec: 0, rtf: 0, next_before: 0 };

// Language profiles: just Default, the kept languages above.
export const subtitleProfiles: ProfileList = {
  profiles: [{ id: 1, name: "Default", is_default: true, titles: 0, items: [{ lang: "en", variant: "full", sdh: "any", only_if_audio_differs: false }] }],
  overrides: [],
};

// GET /subtitles/file: one video as the per-file drawer sees it — an English SRT from
// OpenSubtitles beside it, Spanish missing and parked, and one image track inside.
export function subtitleFile(url: URL): SubFileView {
  const episode = url.searchParams.get("kind") === "episode";
  const base = episode ? "Show - S01E01" : "Fixture Film (2020)";
  return {
    kind: episode ? "episode" : "movie",
    ...(episode
      ? { series_id: Number(url.searchParams.get("series")), season: Number(url.searchParams.get("season")), episode: Number(url.searchParams.get("episode")) }
      : { movie_id: Number(url.searchParams.get("id")) }),
    media_key: episode ? "e:1:1:1" : `m:${url.searchParams.get("id")}`,
    title: episode ? "Show - S01E01" : "Fixture Film",
    video_path: `/media/${base}/${base}.mkv`,
    kept: ["en", "es"],
    profile: { id: 1, name: "Default", override: false },
    languages: [
      { lang: "en", have: true },
      { lang: "es", have: false, reason: "no_match", next_retry_at: Math.floor(NOW / 1000) + 6 * 86400 },
    ],
    audio: [{ index: 0, lang: "eng", default: true }],
    embedded: [{ index: 0, codec: "hdmv_pgs_subtitle", lang: "eng", text: false }],
    sidecars: [{
      name: `${base}.en.srt`, lang: "en", ext: ".srt", size: 48_213, modified_at: Math.floor(NOW / 1000) - 86400, cue_count: 812,
      first_cues: [
        { start_ms: 61_200, end_ms: 63_900, text: "Where were you last night?" },
        { start_ms: 64_100, end_ms: 66_000, text: "That's so long ago, I don't remember." },
        { start_ms: 66_400, end_ms: 68_250, text: "Will I see you tonight?" },
      ],
      shiftable: true, owned: true, source: "opensubtitles", provider: "opensubtitles", provider_file_id: "991",
      release: `${base}.1080p.BluRay.x264-GRP`, score: 84, health: { state: "ok", score: 92, note: "in sync · checked 11 Oct 2026" }, created_at: Math.floor(NOW / 1000) - 86400,
    }],
    orphans: episode ? [] : [{ name: "Old.Name.eng.srt", lang: "en" }],
    backoffs: [],
    attempts: [{ id: 3, run_id: "r1", media_key: "m:1", video_path: "", lang: "es", rung: "opensubtitles", outcome: "no_match", detail: "no OpenSubtitles match", at: Math.floor(NOW / 1000) - 3600 }],
    jobs: [],
  };
}

// A slice of the shared language table (GET /api/v1/languages), regional variants included.
export const languages: Language[] = [
  { code: "de", name: "German", regional: false },
  { code: "en", name: "English", regional: false },
  { code: "es", name: "Spanish", regional: false },
  { code: "fr", name: "French", regional: false },
  { code: "ja", name: "Japanese", regional: false },
  { code: "pt", name: "Portuguese", regional: false },
  { code: "pt-br", name: "Portuguese (Brazil)", regional: true },
  { code: "pt-pt", name: "Portuguese (Portugal)", regional: true },
];

// Settings → Subtitles: whisper is in the build, with no model downloaded yet.
export const subtitleModels: WhisperStatus = {
  binary_ready: true, ready: false,
  models: [{ name: "ggml-large-v3-turbo.bin", label: "large-v3-turbo — fast transcription in any language (can't translate)", size_mb: 1600, present: false, downloading: false }],
};

export const plexConfig: PlexConfig = { url: "", token_set: false, enabled: false, poll_seconds: 30, status: "unconfigured" };

// Settings → Media servers' Plex library updates card, with no Plex server connected.
export const plexScan: PlexScanView = {
  enabled: true, configured: false, path_map: [], last_scan: null, pending: 0,
  roots: [
    { kind: "movie", arrmada_root: "/movies", sections: [], plex_path: "" },
    { kind: "show", arrmada_root: "/tv", sections: [], plex_path: "" },
  ],
};

// Its Jellyfin library updates card, with no Jellyfin server connected either.
export const jellyfinScan: PlexScanView = { ...plexScan, roots: plexScan.roots.map((r) => ({ ...r })) };

// No imported watch history either, so Insights shows its setup state and nothing else.
export const insightsStats: InsightsStats = {
  most_watched_movies: [], most_watched_shows: [], most_active_users: [], most_active_platforms: [], recently_watched: [],
};

export const artists: { artists: Artist[] } = { artists: [] };

// Settings → Naming & metadata → Music matching (MUS-30/31): the shipped defaults.
const matchingDefaults: MusicMatching = {
  strong: 0.04, expected: 0.15, review: 0.4, track_max: 0.3, gap: 0.03, partial_min: 0.5,
  edition_policy: "original", edition_countries: [], edition_media: ["Digital Media", "CD"], scan_workers: 4,
};
export const musicMatching = { settings: matchingDefaults, defaults: matchingDefaults };

// Settings → Naming & metadata → Music: new releases (MUS-13/14): the shipped defaults.
const releaseDefaults: ReleaseDefaults = { monitor_new: "all", release_types: { primary: ["Album", "EP"], secondary: [] } };
export const releaseTypeChoices: ReleaseTypeChoices = {
  primary: ["Album", "EP", "Single", "Broadcast", "Other"],
  secondary: ["Compilation", "Soundtrack", "Live", "Remix", "DJ-mix", "Mixtape/Street", "Demo", "Spokenword", "Interview", "Audiobook", "Audio drama", "Field recording"],
};
export const musicReleaseDefaults = { settings: releaseDefaults, defaults: releaseDefaults, choices: releaseTypeChoices };

// Settings → Import: a saved Tautulli connection (key masked), one finished import and one
// that timed out, and the job an undo runs as.
const nowSecs = Math.floor(NOW / 1000);
export const tautulliConfig: TautulliConfig = { url: "http://192.168.50.247:8181", api_key_set: true };
export const importRuns: { runs: ImportRun[] } = {
  runs: [
    {
      id: 2, job_id: 41, source: "tautulli", started_at: nowSecs - 3600, finished_at: nowSecs - 1800, status: "timeout",
      total: 20000, processed: 15000, imported: 400, duplicates: 14590, overlaps: 10, invalid: 0, after_cutoff: 0, failed: 0,
      error: "the import took longer than 30 minutes and was stopped — Retry carries on from where it got to",
      cutoff_at: 0, rows: 400, removed_at: 0, removed_rows: 0,
    },
    {
      id: 1, job_id: 40, source: "tautulli", started_at: nowSecs - 86400, finished_at: nowSecs - 86000, status: "done",
      total: 13533, processed: 13533, imported: 12340, duplicates: 210, overlaps: 980, invalid: 3, after_cutoff: 0, failed: 0,
      error: "", cutoff_at: 0, rows: 12340, removed_at: 0, removed_rows: 0,
    },
  ],
};
export const removeImportJob: Job = {
  id: 950, kind: "insights.remove-import", target: "run:1", trigger: "user:1", status: "succeeded", progress: 1,
  message: "Removed 12340 imported plays", error: "",
  created_at: new Date(NOW).toISOString(), started_at: new Date(NOW).toISOString(), finished_at: new Date(NOW).toISOString(),
};
