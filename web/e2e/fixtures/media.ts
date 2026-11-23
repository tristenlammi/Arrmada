import type {
  APIKeyStatus, ActivityFeed, BlocklistRow, Book, BookSweepStatus, BookUpgradeStatus, CalendarItem, DashboardData, DeletePreview, DownloadClientList, FitCounts,
  FitItem, FormatInfo, Movie, MovieBulkUpdateReply, MovieFile, MovieVersion, MovieDeletePreview, RecycleMode, ReleaseList, SeriesFitSummary, HistoryEvent, HistoryPage, ImportRecord, ImportReview, Indexer, Job, MovieDownloadRow, MovieSummary, MovieWantedRow, MoviesMissing, MyBook, MyRequest, QualityProfileInfo, Series, WantedLists,
} from "../../src/lib/api";
import type { ConnectionSectionData } from "../../src/lib/connectionsApi";
import type { JellyfinConfig, JellyfinSave, JellyfinTestResult } from "../../src/lib/jellyfinApi";
import type { LibraryHit } from "../../src/lib/palette";
import { NOW, day } from "./clock";
import { person } from "./discover";

const poster = (n: number) => `https://image.tmdb.org/t/p/w500/e2e-lib-${n}.jpg`;

export const dashboard: DashboardData = {
  storage: [
    { roots: ["movies", "tv"], path: "/media", total_bytes: 8e12, free_bytes: 2.4e12, used_bytes: 5.6e12, used_pct: 70 },
  ],
  queue: { downloading: 2, stalled: 0, seeding: 5, paused: 0, errored: 0, down_speed: 12_400_000, up_speed: 800_000 },
  library: { movies: 42, movies_missing: 3, series: 12, episodes: 480, episodes_missing: 7, books: 30, books_missing: 2 },
  // MUS-24: Music's part (the fixtures run with Music on).
  music: {
    artists: 4, albums: 9, tracks: 112, size_bytes: 3_400_000_000, albums_wanted: 3, albums_missing: 2,
    new_releases: [
      { id: 51, title: "Low Tide Sessions", artist_name: "The Quiet Harbours", album_type: "EP", release_date: day(8), monitored: true, found_as: "new" },
      { id: 53, title: "Harbour Demos", artist_name: "The Quiet Harbours", album_type: "Album", release_date: "2009", monitored: false, found_as: "back_catalogue" },
    ],
    new_releases_total: 2,
    imports: [{ kind: "music", id: 5, title: "The Quiet Harbours", event: "imported", detail: "Imported 10/10 of \"Low Tide\"", at_ms: NOW - 5_400_000 }],
  },
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
  date: day(offset), type: "movie", title, subtitle: "Movie · 2026", poster_url: poster(ref), ref_id: ref,
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

// MUS-24: album releases, on staff's calendar only (the server leaves them out for
// requesters): one coming up, and one out yesterday that's already here.
const album = (offset: number, ref: number, title: string, artist: string, kind: string, over: Partial<CalendarItem> = {}): CalendarItem => ({
  date: day(offset), type: "album", title, subtitle: `${artist} · ${kind}`, ref_id: ref, has_file: false, monitored: true,
  tmdb_id: 0, media_type: "music", year: 2026, artist, album_type: kind, ...over,
});
export const calendarAlbums: CalendarItem[] = [
  album(8, 51, "Low Tide Sessions", "The Quiet Harbours", "EP"),
  album(-1, 52, "Breakwater Live", "Ana Lys", "Album", { has_file: true }),
];

// calendar answers GET /api/v1/calendar for the window asked, like the server does: albums
// only for staff.
export function calendar(url: URL, staff = false): { items: CalendarItem[]; start: string; end: string } {
  const start = url.searchParams.get("start") ?? day(-7);
  const end = url.searchParams.get("end") ?? day(42);
  const all = staff ? [...calendarItems, ...calendarAlbums] : calendarItems;
  return { start, end, items: all.filter((it) => it.date >= start && it.date <= end) };
}

// The library list is slim summaries (MOV-10): no cast, overview or file paths.
// librarySearch answers GET /api/v1/library/search from the movies and series above: every title
// containing what was typed (the server's own matching is tested in Go).
export function librarySearch(url: URL): { results: LibraryHit[] } {
  const q = (url.searchParams.get("q") ?? "").trim().toLowerCase();
  if (!q) return { results: [] };
  const rows: LibraryHit[] = [
    ...movies.movies.map((m): LibraryHit => ({ kind: "movie", id: m.id, title: m.title, year: m.year, poster_url: m.poster_url })),
    ...series.series.map((s): LibraryHit => ({ kind: "series", id: s.id, title: s.title, year: s.year, poster_url: s.poster_url })),
  ];
  return { results: rows.filter((r) => r.title.toLowerCase().includes(q)).slice(0, 20) };
}

export const movies: { movies: MovieSummary[]; metadata_available: boolean; client_health?: { ok: boolean } } = {
  client_health: { ok: true },
  metadata_available: true,
  movies: [
    { id: 1, title: "The Cartographer", sort_title: "cartographer", year: 2023, poster_url: poster(3), monitored: true, quality_profile: "hd-1080p", min_availability: "released", has_file: true, added_at: "2026-09-01T10:00:00Z",
      size_bytes: 9_000_000_000, media: { resolution: "1080p", codec: "x264", audio: ["DTS"], bitrate_mbps: 6.1, duration_min: 197, container: "mkv" },
      download: { state: "downloading", progress: 0.2, kind: "upgrade" } },
    { id: 2, title: "Anchor Point", sort_title: "anchor point", year: 2022, poster_url: poster(4), monitored: true, quality_profile: "hd-1080p", min_availability: "released", has_file: false, added_at: "2026-09-20T10:00:00Z" },
    { id: 3, title: "Driftwood", sort_title: "driftwood", year: 2025, poster_url: poster(5), monitored: true, quality_profile: "uhd-2160p", min_availability: "released", has_file: false, download: { state: "downloading", progress: 0.42, kind: "missing" } },
    // A 4K Dolby Vision + Atmos file nobody monitors (in the Kids folder, which shows only
    // with libraryFoldersMulti), and a file that's gone from disk.
    { id: 4, title: "Lanterns Over the Northern Sea", sort_title: "lanterns over the northern sea", year: 2019, poster_url: poster(2), monitored: false, quality_profile: "uhd-2160p", min_availability: "released", has_file: true, added_at: "2026-06-01T10:00:00Z", library_folder_id: 3,
      vote_average: 8.1, size_bytes: 52_000_000_000, media: { resolution: "2160p", codec: "HEVC", audio: ["TrueHD Atmos 7.1"], atmos: true, hdr: ["DV", "HDR10"], bitrate_mbps: 48.2, container: "mkv" } },
    { id: 5, title: "Iron Tide", sort_title: "iron tide", year: 2020, poster_url: poster(6), monitored: true, quality_profile: "hd-1080p", min_availability: "released", has_file: true, file_missing: true, added_at: "2026-07-15T10:00:00Z",
      vote_average: 6.4, size_bytes: 4_000_000_000, media: { resolution: "1080p", codec: "x265", audio: ["AAC"], bitrate_mbps: 3.2, container: "mp4" } },
  ],
};


// GET /api/v1/movies/{id}: a library film's detail page, from its summary. Anchor Point
// (id 2) is monitored with no file, so its one primary action is Search.
export function movieDetail(id: number): Movie | null {
  const m = movies.movies.find((x) => x.id === id);
  if (!m) return null;
  // The Cartographer's file is analysed: its audio badges (issue #12) — English and German,
  // the German one heard by the AI on an untagged track — fit its profile's language rules.
  const file: MovieFile | undefined = m.id === 1 ? {
    path: "/media/movies/The Cartographer (2023)/The Cartographer (2023) - 1080p WEB-DL.mkv",
    filename: "The Cartographer (2023) - 1080p WEB-DL.mkv", size_bytes: 9_000_000_000, quality: "1080p WEB-DL",
    codec: "x264", audio: ["DTS 5.1"], probed: true, missing: false,
  } : undefined;
  const audio: Pick<MovieVersion, "audio_langs" | "audio_lang_fit"> = m.id === 1 ? {
    audio_langs: [{ code: "en", name: "English" }, { code: "de", name: "German", detected: true }],
    audio_lang_fit: { status: "good", note: "Has German audio, as this profile asks" },
  } : {};
  return {
    id: m.id, tmdb_id: 9000 + m.id, title: m.title, year: m.year, poster_url: m.poster_url, monitored: m.monitored,
    quality_profile: m.quality_profile, min_availability: m.min_availability, has_file: m.has_file, added_at: m.added_at,
    overview: "A fixture film.", download: m.download, file, movie_file_path: file?.path,
    // The Cartographer's cast (#55): Ada has a person page; Theo's record predates ids, so
    // his tile stays a tile until the film's next metadata refresh.
    extra: m.id === 1 ? { cast: [{ id: person.id, name: person.name, character: "Cartographer" }, { name: "Theo Keel", character: "Deckhand" }] } : undefined,
    versions: [{ id: 0, is_default: true, label: "Default", quality_profile: m.quality_profile, monitored: m.monitored, has_file: m.has_file, file, file_path: file?.path, ...audio }],
    acquisition: { monitored: m.monitored, profile_known: true, upgrades_allowed: m.has_file, scanned_in: false, available: true, downloading: !!m.download, file_missing: false },
  };
}

// PUT /api/v1/movies/bulk: the mass editor's one update. A profile change finds The
// Cartographer (id 1) too big for it; a search for Driftwood (id 3) can't be queued.
export function bulkUpdate(body: unknown): MovieBulkUpdateReply {
  const b = (body ?? {}) as { ids?: number[]; quality_profile?: string; search?: string };
  const ids = b.ids ?? [];
  const exceeds = b.quality_profile && ids.includes(1)
    ? [{ id: 1, title: "The Cartographer", size_bytes: 9_000_000_000, reason: "Over your 20 Mbps ceiling (36.4 Mbps)", smaller: true }]
    : [];
  const errors = !b.quality_profile && b.search && b.search !== "none" && ids.includes(3) ? [{ id: 3, msg: "couldn't queue its search just now" }] : [];
  const searchable = b.search && b.search !== "none" ? ids.filter((id) => id === 2).length : 0;
  return { updated: b.quality_profile ? ids.length : 0, queued: searchable, duplicates: 0, not_monitored: 0, exceeds_profile: exceeds, errors };
}

// GET /api/v1/library/fit: the Cartographer's file is over its profile's ceiling.
export const movieFit: { items: FitItem[] } = {
  items: [
    { movie_id: 1, season: 0, facts: {} as FitItem["facts"], profile: "HD 1080p", fit: { status: "over", issues: [{ kind: "bitrate", msg: "6.1 Mb/s is over the 5 Mb/s ceiling" }], window: { min: 2, max: 5 } } },
    { movie_id: 5, season: 0, facts: {} as FitItem["facts"], profile: "HD 1080p", fit: { status: "fits", issues: [] } },
  ],
};
export const seriesFit: { items: SeriesFitSummary[] } = { items: [{ series_id: 1, checked: 14, fits: 12, over: 2, under: 0, mismatch: 0 }] };

// A release search with more rows than a short window shows: the modal scrolls inside.
export const movieReleases: ReleaseList = {
  profile: "HD 1080p",
  why: ["1080p WEB-DL, inside the bitrate window"],
  chosen_over: "Chosen over the 1080p WEB-DL — fewer seeders",
  searched: 2,
  // Each row explains its place (QUAL-15): what it matched or avoids, and what it lost on.
  releases: Array.from({ length: 12 }, (_, i) => ({
    title: `Anchor.Point.2022.1080p.WEB-DL.H264-GRP${i}`, indexer: "Fixture Tracker", token: `tok-${i}`, size_gb: 4 + i / 2, bitrate_mbps: 5 + i / 4,
    seeders: 40 - i, peers: 5, summary: `1080p WEB-DL · group ${i}`, eligible: true, recommended: i === 0,
    matched: i % 2 === 0 ? ["HEVC", "Atmos"] : undefined,
    avoided: i === 3 ? ["Dolby Vision", "a bitrate under your 5 Mbps floor"] : undefined,
    bonus_waived: i === 5 || undefined,
    lost_on: i === 0 ? undefined : i === 3 ? "it has Dolby Vision, a bitrate under your 5 Mbps floor, which you avoid" : "fewer seeders",
  })),
};

export const recycleMode: RecycleMode = { enabled: true, dirs: ["/media/movies/.arrmada-recycle"], retention_days: 30, max_gb: 0 };
export const movieDeletePreview: MovieDeletePreview = { versions: [], sidecars: 0, bytes: 0, recycle: recycleMode, pending_downloads: [] };
export const seriesDeletePreview: DeletePreview = { files: 14, sidecars: 0, bytes: 21_000_000_000, confirm_over_bytes: 500_000_000_000, recycle: recycleMode };

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
    // Continuing with an episode two days out, and an ended show that's complete.
    { id: 2, tmdb_id: 2002, title: "Saltwind", year: 2021, poster_url: poster(3), network: "Coastal", status: "Returning Series", monitored: true, monitor_new_seasons: true, quality_profile: "hd-1080p", added_at: "2026-09-25T10:00:00Z",
      stats: { episodes: 20, have_files: 18, size_bytes: 30_000_000_000, seasons: 2, missing: 2, unmonitored_missing: 0, next_air_date: "2026-10-11" } },
    { id: 3, tmdb_id: 2003, title: "The Lighthouse Keepers", year: 2015, poster_url: poster(4), network: "Fixture TV", status: "Ended", monitored: true, monitor_new_seasons: false, quality_profile: "hd-1080p", added_at: "2026-05-01T10:00:00Z", library_folder_id: 4,
      stats: { episodes: 40, have_files: 40, size_bytes: 80_000_000_000, seasons: 4, missing: 0, unmonitored_missing: 0 } },
  ],
};

export const books: { books: Book[]; metadata_available: boolean; upgradable?: number } = { books: [], metadata_available: true, upgradable: 0 };
export const bookUpgrade: BookUpgradeStatus = { running: false, total: 0, done: 0, upgraded: 0, flagged: 0, unmatched: 0 };
export const bookSweep: BookSweepStatus = { running: false, total: 0, done: 0, grabbed: 0, skipped: 0 };

export const myBooks: { books: MyBook[]; requests: MyRequest[]; listen: boolean; queue_known: boolean } = {
  // The audiobook server is on and this person may use it: audiobooks offer Listen.
  listen: true,
  queue_known: true,
  books: [
    // Arrived yesterday ("New this week"), the first of a two-book series on the shelf.
    {
      book_id: 1, title: "A Field Guide to Tides", author: "Marina Coves", year: 2019, series: "Coastlines #1", series_name: "Coastlines", series_position: 1,
      description: "A walk along the shore, tide by tide: what the moon pulls, what the sea leaves behind, and how to read the line it draws.",
      subjects: ["Nature", "Oceans"], ebook: { format: "epub", size_bytes: 2_400_000, kindle: true, kepub: true }, audiobook: false, mine: true, added_at: "2026-10-08 10:00:00",
    },
    { book_id: 2, title: "The Lighthouse Keeper's Very Long and Winding Account of Thirty Winters", author: "Elias Rook", audiobook: true, mine: false, added_at: "2026-08-01 10:00:00", audiobooks: [{ version_id: 7, item_key: "b2v7", format: "m4b", size_bytes: 310_000_000, files: 1 }] },
    // An AZW3: Send to Kindle is shown disabled with the reason.
    { book_id: 3, title: "Tide Tables", author: "Marina Coves", series: "Coastlines #2", series_name: "Coastlines", series_position: 2, ebook: { format: "azw3", size_bytes: 900_000, kindle: false }, audiobook: false, mine: false, added_at: "2026-07-01 10:00:00" },
  ],
  requests: [
    { id: 41, title: "Knots and Splices", author: "Hal Yard", status: "pending", requested_at: "2026-10-02T10:00:00Z", stage: "pending", tracking: { stage: "pending" } },
    // Searched three times and missed: the card says "Not found yet" and the next check.
    {
      id: 42, title: "Charts of the Lesser Sounds", author: "Ada Fenwick", status: "approved", requested_at: "2026-09-20T10:00:00Z", stage: "searching", note: "Not found yet", next_check_at: "2026-10-14T09:00:00Z",
      tracking: { stage: "searching", note: "Not found yet", next_check_at: "2026-10-14T09:00:00Z", last_search_at: "2026-10-07T09:00:00Z", misses: 3 },
    },
    // Asked for both formats; the ebook is here, the card says the audiobook is coming.
    { id: 43, title: "A Field Guide to Tides", author: "Marina Coves", status: "approved", requested_at: "2026-09-10T10:00:00Z", formats: "both", waiting: "audiobook", stage: "partial", note: "Ebook ready · audiobook on the way", tracking: { stage: "partial", note: "Ebook ready · audiobook on the way" } },
    // On its way: "Downloading · 40%".
    { id: 44, title: "Rope Work", author: "Hal Yard", status: "approved", requested_at: "2026-10-05T10:00:00Z", stage: "downloading", tracking: { stage: "downloading", progress: 0.4 } },
    // Not out yet: "Not out yet" and the day it is.
    { id: 45, title: "The Moon's Pull", author: "Marina Coves", status: "approved", requested_at: "2026-10-06T10:00:00Z", stage: "searching", note: "Not out yet", release_date: "2026-11-02", tracking: { stage: "searching", note: "Not out yet", release_date: "2026-11-02" } },
  ],
};

// Send to Kindle: outgoing mail is set up, and this person hasn't saved an address yet.
export const myKindle = { kindle_email: "", from_address: "books@example.com", smtp_configured: true };

export const qualityProfiles: { profiles: QualityProfileInfo[]; formats: FormatInfo[] } = {
  profiles: [
    { key: "hd-1080p", name: "HD 1080p", media_type: "movie", built_in: true, is_default: true, summary: "1080p, prefers WEB-DL and Blu-ray" },
    { key: "uhd-2160p", name: "Ultra HD", media_type: "movie", built_in: true, is_default: false, summary: "2160p with HDR" },
    { key: "hd-720p", name: "HD-720p", media_type: "movie", built_in: false, is_default: false, summary: "720p, small files" },
  ],
  formats: [{ name: "HDR10", description: "High dynamic range", group: "HDR" }],
};

// The series profiles (GET /quality/profiles?media=series): the list is per media. The
// keys the series pages pick (hd-1080p, uhd-2160p) are here too, with a 720p of its own.
export const seriesQualityProfiles: { profiles: QualityProfileInfo[]; formats: FormatInfo[] } = {
  profiles: [
    { key: "hd-1080p", name: "HD 1080p", media_type: "series", built_in: true, is_default: true, summary: "1080p episodes" },
    { key: "uhd-2160p", name: "Ultra HD", media_type: "series", built_in: true, is_default: false, summary: "2160p with HDR" },
    { key: "tv-720p", name: "HD-720p", media_type: "series", built_in: false, is_default: false, summary: "720p, small episodes" },
  ],
  formats: qualityProfiles.formats,
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

export const STALLED_QUEUE_HASH = "e2e0000000000000000000000000000000000006";

export const activity: ActivityFeed = {
  downloads: [
    {
      hash: "e2e0000000000000000000000000000000000001", name: "Driftwood.2025.2160p.WEB-DL.DV.HDR.H265-FIXTURE", state: "downloading",
      progress: 0.42, size_bytes: 18_000_000_000, down_speed: 12_400_000, up_speed: 400_000, eta_seconds: 900, ratio: 0.1,
      quality_profile: "uhd-2160p", media_type: "movie", seeds: 40, peers: 12, stage: "downloading", managed: true, client_id: 1, client_name: "qBittorrent",
      // ACQ-29: the title the grab was for, by hash: poster, link, version and who asked.
      item: { kind: "movie", id: 3, title: "Driftwood", year: 2025, poster_url: poster(5), scope: "4K", requested_by: ["Mum"] },
    },
    {
      hash: "e2e0000000000000000000000000000000000002", name: "Harbour.Lights.S02E04.1080p.WEB.H264-FIXTURE", state: "seeding",
      progress: 1, size_bytes: 2_100_000_000, down_speed: 0, up_speed: 400_000, eta_seconds: 0, ratio: 1.3,
      quality_profile: "hd-1080p", media_type: "series", imported: true, stage: "seeding", managed: true, client_id: 1, client_name: "qBittorrent",
      item: { kind: "series", id: 1, title: "Harbour Lights", year: 2024, scope: "S02E04", requested_by: ["Dad", "Gran"] },
    },
    // Finished, not in the library: one held for review, one whose import keeps failing.
    {
      hash: "e2e0000000000000000000000000000000000003", name: "Alien.1979.1080p.BluRay-FIXTURE", state: "seeding",
      progress: 1, size_bytes: 9_000_000_000, down_speed: 0, up_speed: 0, eta_seconds: 0, ratio: 0.2,
      quality_profile: "hd-1080p", media_type: "movie", stage: "needs_review", why: "Held for review: it doesn't look like Aliens", managed: true, client_id: 1, client_name: "qBittorrent",
      item: { kind: "movie", id: 4, title: "Aliens", year: 1986 },
    },
    {
      hash: "e2e0000000000000000000000000000000000004", name: "Heat.1995.1080p.BluRay-FIXTURE", state: "seeding",
      progress: 1, size_bytes: 11_000_000_000, down_speed: 0, up_speed: 0, eta_seconds: 0, ratio: 0.1,
      quality_profile: "hd-1080p", media_type: "movie", stage: "import_retrying", why: "Import failed 3× (permission denied) — next try in 8 min", managed: true, client_id: 1, client_name: "qBittorrent",
      item: { kind: "movie", id: 2, title: "Heat", year: 1995, upgrade: true },
    },
    // Nothing in Arrmada grabbed it: the release name is all there is.
    {
      hash: "e2e0000000000000000000000000000000000005", name: "Some.Linux.Distro.ISO-FIXTURE", state: "seeding",
      progress: 1, size_bytes: 3_000_000_000, down_speed: 0, up_speed: 0, eta_seconds: 0, ratio: 0.5,
      quality_profile: "n/a", media_type: "movie", stage: "not_managed", why: "Not managed by Arrmada", client_id: 1, client_name: "qBittorrent",
    },
    // Stalled: no progress for two hours by the grab's stall clock (the server's verdict).
    {
      hash: STALLED_QUEUE_HASH, name: "Old.Film.2019.1080p.WEB-DL-FIXTURE", state: "downloading", raw_state: "stalledDL", phase: "stalled",
      progress: 0.18, size_bytes: 6_000_000_000, down_speed: 0, up_speed: 0, eta_seconds: 8640000, ratio: 0, seeds: 0, swarm_seeds: 0,
      last_activity: NOW / 1000 - 2 * 3600, added_on: NOW / 1000 - 5 * 3600,
      quality_profile: "hd-1080p", media_type: "movie", stage: "stalled", why: "Nobody is sending data", managed: true, client_id: 1, client_name: "qBittorrent",
      stalled: true, stalled_since: NOW / 1000 - 2 * 3600, stall: { idle_minutes: 120, failover_in_minutes: 240, off: false },
      item: { kind: "movie", id: 5, title: "Old Film", year: 2019 },
    },
    // No peer sending this moment, but not for long enough to count as stalled.
    {
      hash: "e2e0000000000000000000000000000000000007", name: "Quiet.Bay.S01E02.1080p.WEB.H264-FIXTURE", state: "downloading", raw_state: "stalledDL", phase: "stalled",
      progress: 0.6, size_bytes: 1_500_000_000, down_speed: 0, up_speed: 0, eta_seconds: 8640000, ratio: 0, seeds: 2, swarm_seeds: 9,
      last_activity: NOW / 1000 - 4 * 60, added_on: NOW / 1000 - 3600,
      quality_profile: "hd-1080p", media_type: "series", stage: "downloading", why: "Waiting for peers", managed: true, client_id: 1, client_name: "qBittorrent",
      item: { kind: "series", id: 2, title: "Quiet Bay", year: 2025, scope: "S01E02" },
    },
  ],
  totals: { down_speed: 12_400_000, up_speed: 800_000, active: 2, stalled: 1 },
  free_gb: 2400,
  disk_path: "/downloads",
  clients: { configured: 1, enabled: 1, ok: true },
};

// The same queue with two qBittorrents: the stalled download and a seed sit in the second,
// so the rows name their client and the queue can be narrowed to one.
export const activityTwoClients: ActivityFeed = {
  ...activity,
  downloads: activity.downloads.map((d) =>
    d.hash === STALLED_QUEUE_HASH || d.hash === "e2e0000000000000000000000000000000000002" ? { ...d, client_id: 2, client_name: "qBittorrent 2" } : d),
  clients: { configured: 2, enabled: 2, ok: true },
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

export const importedFiles: { imports: ImportRecord[] } = {
  imports: [{ hash: "e2e0000000000000000000000000000000000009", source_path: "/media/downloads/The.Cartographer.2024.2160p", target_path: "/media/movies/The Cartographer (2024)/The Cartographer (2024).mkv", title: "The Cartographer", size_bytes: 21_000_000_000, imported_at: "2026-10-09 11:00:00" }],
};

// The History feed: a few days of events across the four kinds, newest first, with one
// bookkeeping event ("added") the default view leaves out.
const H = 3_600_000;
export const historyEvents: HistoryEvent[] = [
  { kind: "movie", item_id: 1, title: "The Cartographer", poster_url: poster(1), event: "imported", category: "imported", detail: "Imported The.Cartographer.2024.2160p.mkv", at_ms: NOW - 1 * H, id: 12 },
  { kind: "series", item_id: 1, title: "Harbour Lights", event: "grabbed", category: "grabbed", detail: "Harbour.Lights.S02E04.1080p.WEB.H264-FIXTURE", at_ms: NOW - 2 * H, id: 30 },
  { kind: "music", item_id: 2, title: "Low Tide", event: "imported", category: "imported", detail: "Saltwater (2024)", at_ms: NOW - 3 * H, id: 4 },
  { kind: "movie", item_id: 3, title: "Driftwood", event: "added", category: "other", detail: "Added to library", at_ms: NOW - 4 * H, id: 11 },
  { kind: "book", item_id: 7, title: "The Long Quiet", event: "failed", category: "failed", detail: "Stalled for 6h — trying another release", at_ms: NOW - 26 * H, id: 9 },
  { kind: "series", item_id: 1, title: "Harbour Lights", event: "imported", category: "imported", detail: "S02E03", at_ms: NOW - 50 * H, id: 29 },
  { kind: "movie", item_id: 3, title: "Driftwood", event: "blocklisted", category: "blocklisted", detail: "Driftwood.2025.CAM-FAKE", at_ms: NOW - 52 * H, id: 10 },
];

// historyPage answers GET /api/v1/history like the server: kind, event (default: all but
// "other"), q over title and detail, limit, and the before cursor (<at_ms>:<kind>:<id>).
export function historyPage(url: URL): HistoryPage {
  const kinds = (url.searchParams.get("kind") ?? "").split(",").filter(Boolean);
  const event = url.searchParams.get("event") ?? "";
  const q = (url.searchParams.get("q") ?? "").toLowerCase();
  const limit = Number(url.searchParams.get("limit") ?? 50);
  const before = url.searchParams.get("before");
  let list = historyEvents.filter((e) =>
    (kinds.length === 0 || kinds.includes(e.kind)) &&
    (event === "all" || (event === "" ? e.category !== "other" : event.split(",").includes(e.category))) &&
    (!q || e.title.toLowerCase().includes(q) || e.detail.toLowerCase().includes(q)));
  if (before) {
    const at = list.findIndex((e) => `${e.at_ms}:${e.kind}:${e.id}` === before);
    list = list.slice(at + 1);
  }
  const events = list.slice(0, limit);
  const last = events[events.length - 1];
  return { events, next_before: events.length === limit && last ? `${last.at_ms}:${last.kind}:${last.id}` : "" };
}
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
// Import anyway / Choose different… / Map files… start a review.import job (202); this is
// how that job ends by default. Specs swap the job's answer for a running or failed one.
export const reviewImportJob: Job = {
  id: 912, kind: "review.import", target: "review:1", trigger: "user:1", status: "succeeded", progress: 1,
  message: "Imported into Fixture Movie.", error: "",
  created_at: new Date(NOW).toISOString(), started_at: new Date(NOW).toISOString(), finished_at: new Date(NOW).toISOString(),
};
export const reviewImportStarted = { status: "importing", job_id: reviewImportJob.id, existing: false, title: "Fixture Movie" };
export const blocklist: { items: BlocklistRow[]; total: number } = {
  items: [
    { id: 2, type: "global", item_id: 0, item_title: "", title: "Totally.Legit.2024.1080p.exe", reason: "rejected in review", created_at: "2026-10-01 12:00:00" },
    { id: 1, type: "movie", item_id: 1, item_title: "Fixture Movie", title: "Fixture.Movie.2020.1080p.WEB-DL-GRP", indexer: "Fixture Indexer", reason: "manually blocklisted", created_at: "2026-09-30 08:00:00" },
  ],
  total: 2,
};

// A connection's Test: it answers, and its card comes back working (and set up: a spec may
// have swapped in a connected card the default list has as not set up).
export function connectionTest(kind: string, ref: string) {
  const item = connections.sections.flatMap((s) => s.items).find((i) => i.kind === kind && i.ref === ref);
  if (!item) return { message: "no such connection" };
  return {
    ok: true, detail: "Connected",
    item: { ...item, configured: true, state: "ok", last_error: undefined, failing_since: undefined, last_ok_at: new Date(NOW).toISOString(), checked_at: new Date(NOW).toISOString() },
  };
}

// The key statuses an admin's Connections page edits inside the metadata cards.
export const apiKeys: { keys: APIKeyStatus[] } = {
  keys: [
    { id: "tmdb", label: "TMDB", purpose: "Movie and TV metadata, artwork, and discovery. Required.", help_url: "https://www.themoviedb.org/settings/api", steps: "Use the v3 key.", secret: true, testable: true, tests_candidate: true, configured: true, source: "settings", hint: "…9e14", env_set: false },
    { id: "tvdb", label: "TheTVDB", purpose: "Anime episode numbering. Optional.", help_url: "https://www.thetvdb.com/dashboard/account/apikey", steps: "Generate a v4 API key.", secret: true, testable: true, tests_candidate: true, configured: false, source: "", env_set: false },
    { id: "flaresolverr", label: "FlareSolverr URL", purpose: "Gets TorrentLeech, 1337x and TheXEM past Cloudflare.", help_url: "https://github.com/FlareSolverr/FlareSolverr", steps: "The bundled container answers at http://arrmada-flaresolverr:8191.", secret: false, testable: true, configured: true, source: "env", hint: "http://arrmada-flaresolverr:8191", env_set: true, env_hint: "http://arrmada-flaresolverr:8191" },
  ],
};

// Settings → Connections: one of each card the page draws differently (working, failing,
// not set up, edited on another page).
export const connections: { sections: ConnectionSectionData[] } = {
  sections: [
    {
      id: "search", title: "Search", items: [
        { kind: "prowlarr", ref: "default", card: "prowlarr", name: "Prowlarr", subtitle: "http://arrmada-prowlarr:9696", configured: false, enabled: false, state: "unknown", testable: false, detail: "Not synced yet. Sync once to pull its indexers in." },
        { kind: "flaresolverr", ref: "default", card: "flaresolverr", name: "FlareSolverr", subtitle: "http://arrmada-flaresolverr:8191", configured: true, enabled: true, state: "ok", source: "env", testable: true, last_ok_at: new Date(NOW - 5 * 60_000).toISOString(), detail: "No enabled indexer needs it right now" },
        {
          kind: "indexer", ref: "1", card: "indexer-1", name: "Fixture Indexer", type: "torznab", subtitle: "http://indexer.invalid", configured: true, enabled: true, state: "ok", testable: true,
          last_ok_at: new Date(NOW - 3 * 60_000).toISOString(), counts: { queries_24h: 214, failures_24h: 3, grabs_24h: 2 },
        },
      ],
    },
    {
      id: "downloads", title: "Downloads", items: [
        {
          kind: "download_client", ref: "1", card: "download_client-1", name: "qBittorrent", type: "qbittorrent", subtitle: "http://qbittorrent:8080", configured: true, enabled: true, state: "failing", testable: true,
          last_error: "connection refused", last_error_at: new Date(NOW - 60_000).toISOString(), failing_since: new Date(NOW - 10 * 60_000).toISOString(), detail: "Comes with Arrmada",
        },
      ],
    },
    {
      id: "metadata", title: "Metadata", items: [
        { kind: "apikey", ref: "tmdb", card: "apikey-tmdb", name: "TMDB", subtitle: "Movie and TV metadata, artwork, and discovery. Required.", configured: true, enabled: true, state: "ok", source: "settings", testable: true, last_ok_at: new Date(NOW - 86_400_000).toISOString() },
        { kind: "apikey", ref: "tvdb", card: "apikey-tvdb", name: "TheTVDB", subtitle: "Anime episode numbering. Optional.", configured: false, enabled: false, state: "unknown", testable: false },
      ],
    },
    {
      id: "other", title: "Alerts and apps", items: [
        { kind: "plex", ref: "default", card: "plex", name: "Plex", configured: false, enabled: false, state: "unknown", testable: false, fix_path: "/settings/connections#plex" },
        { kind: "jellyfin", ref: "default", card: "jellyfin", name: "Jellyfin", configured: false, enabled: false, state: "unknown", testable: false, fix_path: "/settings/connections#jellyfin" },
        { kind: "notifications", ref: "default", card: "notifications", name: "Alerts", configured: false, enabled: false, state: "unknown", testable: false, fix_path: "/settings/alerts", detail: "No alert connections yet" },
      ],
    },
  ],
};

// The Jellyfin connection (GET /api/v1/jellyfin): nothing saved yet. Its editor saves and
// tests against the routes in routes.ts; the key is never part of an answer.
export const jellyfinConfig: JellyfinConfig = {
  url: "", public_url: "", key_set: false, server_name: "", server_id: "", version: "", monitoring_enabled: false, poll_seconds: 10,
};

// A connected Jellyfin, for specs that override the list and the config.
export const jellyfinConnected: JellyfinConfig = {
  url: "http://jellyfin:8096", public_url: "https://jellyfin.example.com", key_set: true, server_name: "Attic", server_id: "f00dfeed",
  version: "10.10.3", monitoring_enabled: true, poll_seconds: 10,
};

// PUT /api/v1/jellyfin: answers with what was saved, the key only as key_set. A public
// address the same as the URL is saved blank (it follows the URL), as the server does.
export function jellyfinSave(body: JellyfinSave): JellyfinConfig {
  const publicURL = body.public_url.replace(/\/+$/, "") === body.url.replace(/\/+$/, "") ? "" : body.public_url;
  const saved = { ...jellyfinConfig, url: body.url, public_url: publicURL, key_set: !!body.api_key, server_name: "Attic", server_id: "f00dfeed", version: "10.10.3" };
  // An Emby server (server_kind "emby") answers with its own kind and version.
  return body.server_kind ? { ...saved, server_kind: body.server_kind, ...(body.server_kind === "emby" ? { version: "4.8.10.0" } : {}) } : saved;
}

// POST /api/v1/jellyfin/test: an address with "unreachable" in it doesn't answer; Emby
// answers with its own version.
export function jellyfinTest(body: { url?: string; server_kind?: string }): JellyfinTestResult {
  if (body.url?.includes("unreachable")) return { ok: false, detail: "dial tcp: connection refused" };
  return { ok: true, detail: "Connected", server_name: "Attic", version: body.server_kind === "emby" ? "4.8.10.0" : "10.10.3" };
}
