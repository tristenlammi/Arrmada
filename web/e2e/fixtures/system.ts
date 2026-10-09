import type {
  AppSettings, AudioListening, FolderCheck, Health, Job, LibraryPaths, LogEntry, MyAudio, PendingRestart, RecycleStats, SetupState, Status, SystemHealth,
  TaskStatus,
} from "../../src/lib/api";
import type { PersonaInfo } from "./users";
import { NOW } from "./clock";

export function status(p: PersonaInfo): Status {
  return {
    app: "arrmada", version: "e2e", commit: "e2e0000", started_at: "2026-10-09T08:00:00Z", uptime_seconds: 14400,
    needs_setup: false, authenticated: true, external: p.external,
    modules: [
      { id: "movies", name: "Movies", enabled: true, status: "ok" },
      { id: "series", name: "Series", enabled: true, status: "ok" },
    ],
    books_enabled: true, music_enabled: true, plex_login: false, metadata_ready: true,
  };
}

export const health: Health = { status: "ok", version: "e2e", commit: "e2e0000", uptime_seconds: 14400, checks: { db: "ok" } };

export const systemHealth: SystemHealth = { status: "ok", warnings: [], disk: { free_gb: "812.4", path: "/media" } };

const paths: LibraryPaths = { movies: "/media/movies", tv: "/media/tv", ebooks: "/media/ebooks", audiobooks: "/media/audiobooks", music: "/media/music", downloads: "/media/downloads" };

// The Library section of the Settings hub reads the same folders.
export const libraryPaths: LibraryPaths = paths;

// Every folder checks out: there, writable, and hardlink-able with downloads.
export function folderCheck(path: string): FolderCheck {
  return {
    path, exists: true, is_dir: true, writable: true, hardlink_with_downloads: true, under_data_dir: false,
    free_bytes: 2.4e12, total_bytes: 8e12, entries: 12, entries_capped: false,
  };
}

// Setup is finished, so the staff console opens straight onto its pages.
export const setup: SetupState = {
  needed: false, complete: true, tmdb_configured: true, libraries_chosen: true, keys: [],
  library: paths, running: paths, restart_needed: false, can_restart: true, mounts: ["/media"], suggestions: {},
};

export const pendingRestart: PendingRestart = {
  restart_needed: false, can_restart: true, changed: [],
  busy: { convert_running: 0, convert_longest_sec: 0, convert_progress: 0, subtitles_running: 0, subtitles_queued: 0 },
};

export function myAudio(p: PersonaInfo): MyAudio {
  return {
    enabled: true, running: true, host_port: "13378", public_url: "",
    username: p.user.username, allowed: true, has_password: false, min_password_length: 8, devices: [], places: [],
  };
}

// Your own listening, never anyone's titles: an empty month is enough here.
export const myListening: AudioListening = { days: 30, since: "2026-09-09", daily: [], totals: [], sessions: [] };

export const settings: AppSettings = {
  search_on_add: true,
  naming_movie_folder: "{Title} ({Year})",
  naming_movie_file: "{Title} ({Year}) {Quality}",
  naming_series_folder: "{Title} ({Year})",
  naming_series_season: "Season {Season:00}",
  naming_series_episode: "{Title} - S{Season:00}E{Episode:00}",
  write_nfo: false,
  download_artwork: true,
  books_enabled: true,
  music_enabled: true,
  plex_login_enabled: false,
  plex_login_auto_approve: false,
  tmdb_region: "US",
  recycle_max_gb: "50",
  recycle_retention_days: "30",
  downloads_disk_guard: true,
  downloads_disk_guard_pause_pct: "95",
  downloads_disk_guard_resume_pct: "90",
  downloads_stall_minutes: 60,
  upgrade_max_grabs_per_sweep: 10,
};

export const recycle: RecycleStats = {
  enabled: true, dir: "/media/.recycle", files: 3, bytes: 4_200_000_000, max_gb: 50, retention_days: 30,
  over_cap_bytes: 0, protected_bytes: 0, largest_item_bytes: 2_100_000_000,
  bins: [
    {
      key: "movies", dir: "/media/movies/.arrmada-recycle", label: "Movies", legacy: false, files: 3, bytes: 4_200_000_000,
      free_bytes: 900_000_000_000, free_known: true, other_drive: false,
    },
  ],
};

// Settings → Status's Tasks table (GET /api/v1/system/tasks): one task that ran fine and
// one failing, so both result chips render.
const iso = (ms: number) => new Date(ms).toISOString();
export const tasks: TaskStatus[] = [
  {
    name: "rss-sync", label: "Check indexer feeds for new movies", description: "Reads each indexer's recent uploads and grabs any wanted movie.",
    interval_seconds: 900, running: false, last_start: iso(NOW - 120_000), last_end: iso(NOW - 118_000), last_duration_ms: 2000,
    last_status: "ok", last_ok: true, last_error: "", last_error_at: null, runs: 16, failures: 0, consecutive_failures: 0, skipped: 0,
    next_run: iso(NOW + 780_000),
  },
  {
    name: "db-backup", label: "Back up the database", description: "Takes the nightly database backup and keeps the newest seven.",
    interval_seconds: 3600, running: false, last_start: iso(NOW - 600_000), last_end: iso(NOW - 599_000), last_duration_ms: 1000,
    last_status: "failed", last_ok: false, last_error: "disk full", last_error_at: iso(NOW - 599_000), runs: 4, failures: 3,
    consecutive_failures: 3, skipped: 0, next_run: iso(NOW + 3_000_000),
  },
];

// The job runner (GET /api/v1/jobs): nothing recent.
export const jobs: { jobs: Job[] } = { jobs: [] };

export const logs: { entries: LogEntry[] } = {
  entries: [
    { time_ms: NOW - 60_000, level: "INFO", msg: "library scan finished", attrs: "movies=42" },
    { time_ms: NOW - 30_000, level: "WARN", msg: "indexer slow to answer", attrs: "indexer=Fixture" },
  ],
};
