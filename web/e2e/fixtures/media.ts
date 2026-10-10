import type {
  ActivityFeed, BlocklistRow, Book, BookSweepStatus, BookUpgradeStatus, CalendarItem, DashboardData, DownloadClientList, FitCounts,
  FormatInfo, ImportRecord, ImportReview, Indexer, Job, MovieDownloadRow, MovieSummary, MovieWantedRow, MoviesMissing, MyBook, MyRequest, QualityProfileInfo, Series, WantedLists,
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

// The Calendar's library schedule. day(0) is "today" in the browser. Busy day(5) has five
// items, so the month grid shows three and a '+2 more' button. Items carry the TMDB ids of
// Discover's fixture titles, so a requester's tap opens a title the fixtures know.
// requested_by_me marks what the requester persona owns or follows (Saltwind, Undertow).
const ep = (offset: number, ref: number, tmdb: number, title: string, season: number, episode: number, name: string, over: Partial<CalendarItem> = {}): CalendarItem => ({
  date: day(offset), type: "episode", title, subtitle: `S${season} · E${episode} · ${name}`, poster_url: poster(ref), ref_id: ref,
  has_file: false, monitored: true, tmdb_id: tmdb, media_type: "series", season, episode, episode_title: name, ...over,
});
const mv = (offset: number, ref: number, tmdb: number, title: string, over: Partial<CalendarItem> = {}): CalendarItem => ({
  date: day(offset), type: "movie", title, subtitle: "2026 · Movie", poster_url: poster(ref), ref_id: ref,
  has_file: false, monitored: true, tmdb_id: tmdb, media_type: "movie", year: 2026, ...over,
});
export const calendarItems: CalendarItem[] = [
  ep(0, 9, 1009, "Undertow", 1, 3, "Rip Current", { requested_by_me: true }),
  mv(0, 2, 1002, "Lanterns Over the Northern Sea and Other Very Long Titles"),
  ep(3, 9, 1009, "Undertow", 1, 4, "Slack Water", { requested_by_me: true }),
  mv(-2, 7, 1007, "The Cartographer", { has_file: true }),
  ep(5, 3, 1003, "Saltwind", 2, 1, "New Moorings", { requested_by_me: true }),
  ep(5, 6, 1006, "Anchor Point", 1, 8, "Ballast"),
  ep(5, 12, 1012, "Tidewater", 3, 2, "Neap"),
  ep(5, 15, 1015, "The Lighthouse Keepers", 4, 9, "Fog Signal", { monitored: false }),
  ep(5, 18, 1018, "Evergreen Tide", 1, 1, "Pilot"),
  // Next month, for paging.
  mv(30, 4, 1004, "Iron Tide"),
];

// calendar answers GET /api/v1/calendar for the window asked, like the server does.
export function calendar(url: URL): { items: CalendarItem[]; start: string; end: string } {
  const start = url.searchParams.get("start") ?? day(-7);
  const end = url.searchParams.get("end") ?? day(42);
  return { start, end, items: calendarItems.filter((it) => it.date >= start && it.date <= end) };
}

// The library list is slim summaries (MOV-10): no cast, overview or file paths.
export const movies: { movies: MovieSummary[]; metadata_available: boolean; client_health?: { ok: boolean } } = {
  client_health: { ok: true },
  metadata_available: true,
  movies: [
    { id: 1, title: "The Cartographer", sort_title: "cartographer", year: 2023, poster_url: poster(3), monitored: true, quality_profile: "hd-1080p", min_availability: "released", has_file: true, added_at: "2026-09-01T10:00:00Z",
      size_bytes: 9_000_000_000, media: { resolution: "1080p", codec: "x264", audio: ["DTS"], bitrate_mbps: 6.1, duration_min: 197, container: "mkv" },
      download: { state: "downloading", progress: 0.2, kind: "upgrade" } },
    { id: 2, title: "Anchor Point", sort_title: "anchor point", year: 2022, poster_url: poster(4), monitored: true, quality_profile: "hd-1080p", min_availability: "released", has_file: false, added_at: "2026-09-20T10:00:00Z" },
    { id: 3, title: "Driftwood", sort_title: "driftwood", year: 2025, poster_url: poster(5), monitored: true, quality_profile: "uhd-2160p", min_availability: "released", has_file: false, download: { state: "downloading", progress: 0.42, kind: "missing" } },
  ],
};

// GET /api/v1/movies/downloads: what the grid polls while those two download.
export const movieDownloads: { downloads: MovieDownloadRow[]; client_health: { ok: boolean } } = {
  client_health: { ok: true },
  downloads: [
    { movie_id: 1, state: "downloading", progress: 0.25, kind: "upgrade" },
    { movie_id: 3, state: "downloading", progress: 0.5, kind: "missing" },
  ],
};

export const series: { series: Series[]; metadata_available: boolean } = {
  metadata_available: true,
  series: [
    { id: 1, tmdb_id: 2001, title: "Harbour Lights", year: 2024, poster_url: poster(1), network: "Fixture TV", monitored: true, monitor_new_seasons: true, quality_profile: "hd-1080p", added_at: "2026-08-01T10:00:00Z",
      stats: { episodes: 16, have_files: 14, size_bytes: 21_000_000_000, seasons: 2, missing: 2, unmonitored_missing: 3, next_air_date: "2026-10-20" } },
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
    // Asked for both formats; the ebook is here, the card says the audiobook is coming.
    { title: "A Field Guide to Tides", author: "Marina Coves", status: "approved", requested_at: "2026-09-10T10:00:00Z", formats: "both", waiting: "audiobook", stage: "partial", note: "Ebook ready · audiobook on the way" },
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

// The Wanted view (Downloads → Searching / Upcoming): one film searched without luck, and
// a book waiting for its first search.
export const wanted: WantedLists = {
  queue_known: true,
  searching: [
    {
      media_type: "movie", id: 2, movie_id: 2, title: "Anchor Point", year: 2022, poster_url: poster(4), quality_profile: "HD 1080p",
      state: "searching", search_misses: 3, last_search_at: new Date(NOW - 3600_000).toISOString(),
      next_search_at: new Date(NOW + 3600_000).toISOString(),
      last_search: {
        latest: {
          id: 1, media_type: "movie", media_id: 2, scope: "", trigger: "sweep", started_at: NOW - 3600_000, duration_ms: 900,
          returned: 12, wrong_title: 9, blocklisted: 0, pending: 0, out_of_scope: 0, rejected: 3, eligible: 0, grabbed: 0,
          reasons: { wrong_title: 9, bitrate_ceiling: 3 }, top_reason: "wrong_title", example: "", grabbed_titles: [], indexer_errors: {},
          outcome: "none_suitable", reason: "none-for-this-title",
        },
        empty_tries: 3, main_reason: "wrong_title",
      },
    },
    {
      media_type: "book", id: 3, book_id: 3, title: "Charts of the Lesser Sounds", year: 2024, quality_profile: "Ebook", byline: "Ada Fenwick",
      missing: ["Ebook"], state: "searching", search_misses: 0, due: true, next_search_at: new Date(NOW).toISOString(),
    },
  ],
  upcoming: [],
};

// Movies → Wanted (MOV-08): Missing is the Wanted view's movie rows, plus films missing
// only an extra version; Cutoff unmet lists files below their profile's target.
export const moviesMissing: MoviesMissing = {
  queue_known: true,
  searching: wanted.searching.filter((r) => r.media_type === "movie").map((r) => ({ ...r, queued: false })),
  upcoming: [],
  versions: [
    { media_type: "movie", id: 1, movie_id: 1, title: "The Cartographer", year: 2023, poster_url: poster(3), quality_profile: "HD 1080p",
      missing: ["4K"], tracks: ["4K"], state: "searching", search_misses: 0, due: true, next_search_at: new Date(NOW).toISOString(), queued: false },
  ],
};
export const moviesCutoff: { rows: MovieWantedRow[]; queue_known: boolean } = {
  queue_known: true,
  rows: [
    { media_type: "movie", id: 1, movie_id: 1, title: "The Cartographer", year: 2023, poster_url: poster(3), quality_profile: "HD 1080p",
      state: "upgrading", search_misses: 0, queued: false, detail: "x264 isn't a codec this target wants",
      issues: [{ kind: "codec", msg: "x264 isn't a codec this target wants" }], will_upgrade: true },
  ],
};

// Search now from a Wanted row: the job it starts, and that job finished with nothing usable.
export const wantedSearchStarted = { status: "searching", job_id: 900, existing: false, started_at_ms: NOW };
export const wantedSearchJob: Job = {
  id: 900, kind: "movie.search", target: "movie:2", trigger: "user:1", status: "succeeded", progress: 1,
  message: "12 releases found, none for this movie", error: "",
  result: { searched: true, returned: 12, matching: 0, usable: 0, grabbed: 0, reason: "none-for-this-title", reasons: { wrong_title: 12 } },
  created_at: new Date(NOW).toISOString(), started_at: new Date(NOW).toISOString(), finished_at: new Date(NOW).toISOString(),
};

export const activity: ActivityFeed = {
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
  disk_path: "/downloads",
  clients: { configured: 1, enabled: 1, ok: true },
};

export const downloadClients: DownloadClientList = {
  clients: [{ id: 1, name: "qBittorrent", kind: "qbittorrent", url: "http://qbittorrent:8080", enabled: true, priority: 25, bundled: true }],
  categories: { movies: "arrmada", tv: "arrmada-tv", books: "arrmada-books", music: "arrmada-music" },
};

export const indexers: { indexers: Indexer[] } = {
  indexers: [{
    id: 1, name: "Fixture Indexer", kind: "torznab", url: "http://indexer.invalid", priority: 25, enabled: true,
    caps_summary: "Movies (imdbid, tmdbid) · TV (season, ep, tvdbid) · 12 categories",
    status: { state: "ok", last_ok_at: new Date(NOW - 3 * 60_000).toISOString(), consecutive_failures: 0, queries_24h: 214, failures_24h: 3 },
  }],
};

export const history: { imports: ImportRecord[] } = { imports: [] };
// One of each kind of hold the Review page renders differently.
export const reviews: { reviews: ImportReview[] } = {
  reviews: [
    {
      id: 2, hash: "2222222222222222222222222222222222222222", name: "Fixture.Show.S01.1080p.WEB-DL-GRP", content_path: "/media/downloads/Fixture.Show.S01.1080p.WEB-DL-GRP",
      media_type: "series", expected_id: 1, expected_title: "Fixture Show", parsed_title: "Fixture Show",
      reason: "Downloaded, but none of its 8 video files could be matched to an episode", reason_code: "numbering",
      size_bytes: 8e9, indexer: "Fixture Indexer", created_at: "2026-10-08 09:00:00",
    },
    {
      id: 1, hash: "1111111111111111111111111111111111111111", name: "Other.Movie.2019.1080p.BluRay-GRP", content_path: "/media/downloads/Other.Movie.2019.1080p.BluRay-GRP",
      media_type: "movie", expected_id: 1, expected_title: "Fixture Movie", parsed_title: "Other Movie",
      reason: "Grabbed for \"Fixture Movie (2020)\" but the download looks like \"Other Movie (2019)\"", reason_code: "mismatch",
      size_bytes: 4e9, indexer: "Fixture Indexer", created_at: "2026-10-07 09:00:00",
    },
  ],
};
export const blocklist: { items: BlocklistRow[]; total: number } = {
  items: [
    { id: 2, type: "global", item_id: 0, item_title: "", title: "Totally.Legit.2024.1080p.exe", reason: "rejected in review", created_at: "2026-10-01 12:00:00" },
    { id: 1, type: "movie", item_id: 1, item_title: "Fixture Movie", title: "Fixture.Movie.2020.1080p.WEB-DL-GRP", indexer: "Fixture Indexer", reason: "manually blocklisted", created_at: "2026-09-30 08:00:00" },
  ],
  total: 2,
};
