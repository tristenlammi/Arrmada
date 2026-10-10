import type {
  HistoryEntry, InsightsActivity, InsightsGraphs, InsightsStats, InsightsStream, LibraryStat, PlexConfig, RecentItem, Reliability, UserEntry,
} from "../../src/lib/api";
import type { MockRoute } from "./routes";
import { NOW } from "./clock";

// Insights with a Plex server saved and some recorded plays, for the specs that look
// past the empty Connect state. Each spec picks the monitoring status it needs.

const now = Math.floor(NOW / 1000);

export const config = (status: PlexConfig["status"], extra: Partial<PlexConfig> = {}): PlexConfig => ({
  url: status === "unconfigured" ? "" : "http://plex.lan:32400",
  token_set: status !== "unconfigured",
  enabled: status === "recording" || status === "unreachable",
  poll_seconds: 5,
  status,
  ...extra,
});

const geo = { ip: "192.168.1.20", local: true };

const play = (id: number, over: Partial<HistoryEntry> = {}): HistoryEntry => ({
  id, user_id: "7", user_name: "Jesse", title: "The Cartographer", grandparent_title: "", parent_title: "",
  media_index: 0, parent_index: 0, year: 2024, media_type: "movie", thumb: "", thumb_url: "",
  player: "Living Room TV", platform: "webOS", product: "Plex for LG", ip_address: "192.168.1.20", location: "lan",
  decision: "transcode", started_at: now - id * 3600, stopped_at: now - id * 3600 + 5400, paused_ms: 0,
  view_offset_ms: 5_400_000, duration_ms: 6_000_000,
  video_src: "HEVC 4K", video_stream: "H264 1080p", audio_src: "EAC3", audio_stream: "AAC", container_src: "MKV", container_stream: "MPEGTS",
  hw_transcode: true, buffer_count: id === 1 ? 2 : 0, subtitle: "2024", geo, watched_secs: 5400, progress_pct: 90,
  ...over,
});

export const history = { rows: [play(1), play(2, { title: "Harbour Lights", decision: "direct_play", hw_transcode: false })], total: 2 };

export const users: { users: UserEntry[] } = {
  users: [{ id: "7", username: "Jesse", last_seen: now - 3600, last_ip: "192.168.1.20", last_platform: "webOS", last_player: "Living Room TV", last_title: "The Cartographer", total_plays: 2, total_secs: 10800, geo }],
};

export const stats: InsightsStats = {
  most_watched_movies: [{ title: "The Cartographer", thumb_url: "", plays: 2, secs: 10800 }],
  most_watched_shows: [], most_active_users: [{ id: "7", name: "Jesse", plays: 2, secs: 10800 }],
  most_active_platforms: [{ id: "webOS", name: "webOS", plays: 2, secs: 10800 }], recently_watched: [],
};

const days = Array.from({ length: 30 }, (_, i) => new Date(NOW - (29 - i) * 86_400_000).toISOString().slice(0, 10));
export const graphs: InsightsGraphs = {
  days, daily_tv: days.map(() => 0), daily_movies: days.map((_, i) => (i === 29 ? 2 : 0)), daily_music: days.map(() => 0),
  by_day_of_week: [0, 0, 0, 0, 0, 2, 0], by_hour: Array.from({ length: 24 }, (_, i) => (i === 20 ? 2 : 0)),
  top_platforms: [{ id: "webOS", name: "webOS", plays: 2, secs: 10800 }], top_users: [{ id: "7", name: "Jesse", plays: 2, secs: 10800 }],
  bandwidth: [{ t: "2026-10-09 20:00", total_kbps: 12000, lan_kbps: 12000, wan_kbps: 0 }],
};

export const reliability: Reliability = {
  summary: { total_sessions: 2, buffered_sessions: 1, total_events: 2, total_stall_ms: 47_000, buffer_rate_pct: 50 },
  causes: [{ cause: "transcode_fallback", label: "Plex fell back to CPU", count: 2, stall_ms: 47_000 }],
  by_user: [{ name: "Jesse", sessions: 2, buffered_sessions: 1, events: 2, stall_ms: 47_000, rate_pct: 50 }],
  by_platform: [{ name: "webOS", sessions: 2, buffered_sessions: 1, events: 2, stall_ms: 47_000, rate_pct: 50 }],
  by_title: [{ name: "The Cartographer", sessions: 2, buffered_sessions: 1, events: 2, stall_ms: 47_000, rate_pct: 50 }],
  events: [{
    at: now - 3000, offset_ms: 1_500_000, duration_ms: 30_000, user: "Jesse", title: "The Cartographer — a very long title that has to fit on a phone", platform: "webOS",
    decision: "transcode", cause: "transcode_fallback", detail: "Plex fell back to CPU — hardware transcoding was requested but not used (0.7× realtime)",
  }],
};

const stream = (over: Partial<InsightsStream>): InsightsStream => ({
  session_key: "1", user: "Jesse", title: "The Cartographer", subtitle: "2024", type: "movie", thumb: "",
  progress_pct: 40, offset_ms: 2_400_000, duration_ms: 6_000_000, state: "playing",
  player: "Living Room TV", platform: "webOS", product: "Plex for LG", decision: "transcode",
  bandwidth_kbps: 12000, location: "lan", ip: "192.168.1.20", geo,
  video: { src: "HEVC 4K", stream: "H264 1080p" }, audio: { src: "EAC3", stream: "AAC" }, container: { src: "MKV", stream: "MPEGTS" },
  hw_transcode: true, hw_decode: true, hw_encode: true, hw_requested: true, hw_title: "Intel (QuickSync)",
  throttled: false, reasons: ["Converting video (HEVC 4K → H264 1080p)"],
  ...over,
});

export const activity: InsightsActivity = {
  streams: [
    stream({}),
    stream({ session_key: "2", title: "Harbour Lights", hw_transcode: false, hw_decode: false, hw_encode: false, hw_requested: true, hw_title: "" }),
  ],
  bandwidth: { total_kbps: 24000, lan_kbps: 24000, wan_kbps: 0 }, geo_active: false,
};

export const libraries: { libraries: LibraryStat[] } = { libraries: [{ title: "Movies", type: "movie", count: 1496 }] };
export const recent: { items: RecentItem[] } = { items: [{ title: "Sunshine", subtitle: "2007", type: "movie", thumb_url: "", added_at: now }] };

// routes answers every Insights read for a saved server with recorded plays. The config
// route is a function so a spec can flip the status (e.g. after Turn on).
export function routes(cfg: () => PlexConfig, opts: { plexDown?: boolean } = {}): MockRoute[] {
  const plexDown = { status: 502, body: { message: "dial tcp 192.168.1.10:32400: connect: connection refused" } };
  return [
    { method: "GET", path: "/api/v1/insights/plex", respond: () => cfg() },
    { method: "GET", path: "/api/v1/insights/history", body: history },
    { method: "GET", path: "/api/v1/insights/users", body: users },
    { method: "GET", path: "/api/v1/insights/stats", body: stats },
    { method: "GET", path: "/api/v1/insights/graphs", body: graphs },
    { method: "GET", path: "/api/v1/insights/reliability", body: reliability },
    opts.plexDown ? { method: "GET", path: "/api/v1/insights/activity", ...plexDown } : { method: "GET", path: "/api/v1/insights/activity", body: activity },
    opts.plexDown ? { method: "GET", path: "/api/v1/insights/libraries", ...plexDown } : { method: "GET", path: "/api/v1/insights/libraries", body: libraries },
    opts.plexDown ? { method: "GET", path: "/api/v1/insights/recently-added", ...plexDown } : { method: "GET", path: "/api/v1/insights/recently-added", body: recent },
  ];
}
