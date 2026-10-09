import type {
  ActivityFeed, Book, BookSweepStatus, BookUpgradeStatus, CalendarItem, DashboardData, DownloadClient, FitCounts,
  FormatInfo, ImportRecord, ImportReview, Indexer, Movie, MyBook, MyRequest, QualityProfileInfo, Series,
} from "../../src/lib/api";
import { NOW, day } from "./clock";

const poster = (n: number) => `https://image.tmdb.org/t/p/w500/e2e-lib-${n}.jpg`;

export const dashboard: DashboardData = {
  storage: [
    { roots: ["movies", "tv"], path: "/media", total_bytes: 8e12, free_bytes: 2.4e12, used_bytes: 5.6e12, used_pct: 70 },
  ],
  queue: { downloading: 2, stalled: 0, seeding: 5, paused: 0, errored: 0, down_speed: 12_400_000, up_speed: 800_000 },
  library: { movies: 42, movies_missing: 3, series: 12, episodes: 480, episodes_missing: 7, books: 30, books_missing: 2, artists: 4, albums: 9 },
  activity: [
    { kind: "movie", id: 1, title: "The Cartographer", event: "imported", detail: "2160p WEB-DL", at_ms: NOW - 3_600_000 },
    { kind: "series", id: 1, title: "Harbour Lights", event: "grabbed", detail: "S02E04", at_ms: NOW - 7_200_000 },
  ],
  listening: [],
  plex_configured: false,
};

export const calendar: { items: CalendarItem[]; start: string; end: string } = {
  start: day(-9),
  end: day(22),
  items: [
    { date: day(0), type: "episode", title: "Harbour Lights", subtitle: "S02E05 · The Breakwater", poster_url: poster(1), ref_id: 1, has_file: false, monitored: true },
    { date: day(0), type: "movie", title: "Lanterns Over the Northern Sea and Other Very Long Titles", subtitle: "Digital release", poster_url: poster(2), ref_id: 2, has_file: false, monitored: true },
    { date: day(3), type: "episode", title: "Harbour Lights", subtitle: "S02E06 · Slack Water", poster_url: poster(1), ref_id: 1, has_file: false, monitored: true },
    { date: day(-2), type: "movie", title: "The Cartographer", subtitle: "Digital release", poster_url: poster(3), ref_id: 1, has_file: true, monitored: true },
  ],
};

export const movies: { movies: Movie[]; metadata_available: boolean } = {
  metadata_available: true,
  movies: [
    { id: 1, tmdb_id: 1007, title: "The Cartographer", year: 2023, poster_url: poster(3), monitored: true, quality_profile: "hd-1080p", min_availability: "released", has_file: true, added_at: "2026-09-01T10:00:00Z" },
    { id: 2, tmdb_id: 1006, title: "Anchor Point", year: 2022, poster_url: poster(4), monitored: true, quality_profile: "hd-1080p", min_availability: "released", has_file: false, added_at: "2026-09-20T10:00:00Z" },
    { id: 3, tmdb_id: 1005, title: "Driftwood", year: 2025, poster_url: poster(5), monitored: true, quality_profile: "uhd-2160p", min_availability: "released", has_file: false, download: { state: "downloading", progress: 0.42 } },
  ],
};

export const series: { series: Series[]; metadata_available: boolean } = {
  metadata_available: true,
  series: [
    { id: 1, tmdb_id: 2001, title: "Harbour Lights", year: 2024, poster_url: poster(1), network: "Fixture TV", monitored: true, quality_profile: "hd-1080p", added_at: "2026-08-01T10:00:00Z" },
  ],
};

export const books: { books: Book[]; metadata_available: boolean; upgradable?: number } = { books: [], metadata_available: true, upgradable: 0 };
export const bookUpgrade: BookUpgradeStatus = { running: false, total: 0, done: 0, upgraded: 0, flagged: 0, unmatched: 0 };
export const bookSweep: BookSweepStatus = { running: false, total: 0, done: 0, grabbed: 0, skipped: 0 };

export const myBooks: { books: MyBook[]; requests: MyRequest[] } = {
  books: [
    { book_id: 1, title: "A Field Guide to Tides", author: "Marina Coves", year: 2019, series: "Coastlines", ebook: { format: "epub", size_bytes: 2_400_000 }, audiobook: false, mine: true, added_at: "2026-09-12T10:00:00Z" },
    { book_id: 2, title: "The Lighthouse Keeper's Very Long and Winding Account of Thirty Winters", author: "Elias Rook", audiobook: true, mine: false, audiobooks: [{ version_id: 7, format: "m4b", size_bytes: 310_000_000, files: 1 }] },
  ],
  requests: [
    { title: "Knots and Splices", author: "Hal Yard", status: "pending", requested_at: "2026-10-02T10:00:00Z", stage: "pending" },
    // Searched three times and missed: the card says "Not found yet" and the next check.
    { title: "Charts of the Lesser Sounds", author: "Ada Fenwick", status: "approved", requested_at: "2026-09-20T10:00:00Z", stage: "searching", note: "Not found yet", next_check_at: "2026-10-14T09:00:00Z" },
  ],
};

export const qualityProfiles: { profiles: QualityProfileInfo[]; formats: FormatInfo[] } = {
  profiles: [
    { key: "hd-1080p", name: "HD 1080p", media_type: "movies", built_in: true, is_default: true, summary: "1080p, prefers WEB-DL and Blu-ray" },
    { key: "uhd-2160p", name: "Ultra HD", media_type: "movies", built_in: true, is_default: false, summary: "2160p with HDR" },
  ],
  formats: [{ name: "HDR10", description: "High dynamic range", group: "HDR" }],
};

export const fitProfiles: { profiles: Record<string, FitCounts> } = {
  profiles: { "hd-1080p": { titles: 40, files: 40, fits: 36, over: 2, under: 1, mismatch: 1 } },
};

export const activity: ActivityFeed = {
  searching: [{ movie_id: 2, media_type: "movie", title: "Anchor Point", year: 2022, poster_url: poster(4), quality_profile: "hd-1080p" }],
  downloads: [
    {
      hash: "e2e0000000000000000000000000000000000001", name: "Driftwood.2025.2160p.WEB-DL.DV.HDR.H265-FIXTURE", state: "downloading",
      progress: 0.42, size_bytes: 18_000_000_000, down_speed: 12_400_000, up_speed: 400_000, eta_seconds: 900, ratio: 0.1,
      quality_profile: "uhd-2160p", media_type: "movie", seeds: 40, peers: 12,
    },
    {
      hash: "e2e0000000000000000000000000000000000002", name: "Harbour.Lights.S02E04.1080p.WEB.H264-FIXTURE", state: "seeding",
      progress: 1, size_bytes: 2_100_000_000, down_speed: 0, up_speed: 400_000, eta_seconds: 0, ratio: 1.3,
      quality_profile: "hd-1080p", media_type: "series", imported: true,
    },
  ],
  totals: { down_speed: 12_400_000, up_speed: 800_000, active: 2 },
  free_gb: 2400,
  clients: 1,
};

export const downloadClients: { clients: DownloadClient[] } = {
  clients: [{ id: 1, name: "qBittorrent", kind: "qbittorrent", url: "http://qbittorrent:8080", enabled: true }],
};

export const indexers: { indexers: Indexer[] } = {
  indexers: [{ id: 1, name: "Fixture Indexer", kind: "torznab", url: "http://indexer.invalid", priority: 25, enabled: true }],
};

export const history: { imports: ImportRecord[] } = { imports: [] };
export const reviews: { reviews: ImportReview[] } = { reviews: [] };
