import type { AudioLang, Episode, Job, MovieEvent, SearchAttempt, Season, Series, SeriesNumbering } from "../../src/lib/api";
import type { MisplacedItem, MisplacedPlan } from "../../src/lib/seriesApi";
import type { MockRoute } from "./routes";
import { NOW, day } from "./clock";
import { person } from "./discover";

// The series detail page (SER-14/15/16/20). Show 1 is the library's Harbour Lights: a
// finished season, a season with gaps, a season still airing (one episode downloading, the
// next one in eleven days) and Specials. Show 2 is a long anime season (130 episodes, the
// last 30 missing) for paging. Show 3 is a standard show with a pending numbering change;
// show 4 a clean standard one, except for two files an import misplaced (#56). (The shared
// review fixture holds a download for show 1.)

const still = (s: number, e: number) => `https://image.tmdb.org/t/p/w1280/e2e-still-${s}-${e}.jpg`;

function ep(season: number, n: number, over: Partial<Episode> = {}): Episode {
  return {
    id: season * 1000 + n,
    season_number: season,
    episode_number: n,
    title: `Episode ${n} of season ${season}`,
    overview: `What happens in S${season}E${n}. The harbour wakes before dawn and the crew argue about the tide tables, the weather and who left the lantern lit all night long — again.`,
    air_date: day(-400 + season * 60 + n * 7),
    runtime: 44,
    still_url: n % 4 === 0 ? undefined : still(season, n),
    monitored: true,
    has_file: true,
    file_path: `/tv/Harbour Lights/Season ${String(season).padStart(2, "0")}/Harbour Lights - S${String(season).padStart(2, "0")}E${String(n).padStart(2, "0")}.mkv`,
    size_bytes: 1_400_000_000,
    ...over,
  };
}

const missing = { has_file: false, file_path: undefined, size_bytes: 0 } as const;

// Season 1's files have been analysed (issue #12's badges) under a profile that requires
// German and prefers the original English: E1–E8 carry both, E9 only German (orange), E10
// English with an untagged track the AI heard as French — a German miss that rests on a
// detection, so the server sends no fit (no colour). Season 2 isn't analysed yet.
const EN: AudioLang = { code: "en", name: "English" };
const DE: AudioLang = { code: "de", name: "German" };
function s1Audio(n: number): Partial<Episode> {
  if (n === 9) return { audio_langs: [DE], audio_lang_fit: { status: "partial", note: "No English audio, which this profile prefers" } };
  if (n === 10) return { audio_langs: [EN, { code: "fr", name: "French", detected: true }] };
  return { audio_langs: [EN, DE], audio_lang_fit: { status: "good", note: "Has English and German audio, as this profile asks" } };
}

const harbourSeasons: Season[] = [
  { id: 10, season_number: 0, name: "Specials", monitored: false, episodes: [ep(0, 1, { monitored: false }), ep(0, 2, { ...missing, monitored: false })] },
  { id: 11, season_number: 1, name: "Season 1", poster_url: "https://image.tmdb.org/t/p/w500/e2e-s1.jpg", monitored: true, episodes: Array.from({ length: 10 }, (_, i) => ep(1, i + 1, s1Audio(i + 1))) },
  {
    id: 12, season_number: 2, name: "Season 2", poster_url: "https://image.tmdb.org/t/p/w500/e2e-s2.jpg", monitored: true,
    episodes: [
      ...Array.from({ length: 5 }, (_, i) => ep(2, i + 1)),
      ep(2, 6, missing),
      ep(2, 7, missing),
      ep(2, 8, { ...missing, monitored: false }),
    ],
  },
  {
    id: 13, season_number: 3, name: "Season 3", monitored: true,
    episodes: [
      ep(3, 1, { air_date: day(-7) }),
      ep(3, 2, { ...missing, air_date: day(-1), download: { state: "downloading", progress: 0.42, hash: "abc123" } }),
      ep(3, 3, { ...missing, air_date: day(11), title: "Slack Water" }),
      ep(3, 4, { ...missing, air_date: day(18) }),
    ],
  },
];

function show(id: number, over: Partial<Series>): Series {
  return {
    id, tmdb_id: 2000 + id, imdb_id: "tt0000001", title: "Harbour Lights", year: 2024, network: "Fixture TV", status: "Returning Series",
    overview: "A fishing town, a lighthouse and the families who keep both running.",
    poster_url: "https://image.tmdb.org/t/p/w500/e2e-lib-1.jpg",
    monitored: true, monitor_new_seasons: true, quality_profile: "hd-1080p", series_type: "standard", numbering_source: "tmdb",
    last_refreshed_at: "2026-10-08T10:00:00Z", last_event_id: 7,
    extra: { genres: ["Drama", "Mystery"], backdrop_url: "https://image.tmdb.org/t/p/w1280/e2e-backdrop-1.jpg", cast: [{ id: person.id, name: person.name, character: "Keeper" }, { name: "Ada Fenwick", character: "Harbourmaster" }] },
    seasons: harbourSeasons,
    stats: { episodes: 21, have_files: 17, size_bytes: 210_000_000_000, seasons: 3, missing: 3, unmonitored_missing: 1, next_air_date: day(11) },
    ...over,
  };
}

const anime: Series = show(2, {
  title: "Endless Voyage", series_type: "anime", status: "Ended",
  seasons: [{
    id: 21, season_number: 1, name: "Season 1", monitored: true,
    episodes: Array.from({ length: 130 }, (_, i) => ep(1, i + 1, { id: 20_000 + i, absolute_number: i + 1, air_date: day(-2000 + i * 7), ...(i >= 100 ? missing : {}) })),
  }],
  stats: { episodes: 130, have_files: 100, size_bytes: 140_000_000_000, seasons: 1, missing: 30, unmonitored_missing: 0 },
});

// Show 5 runs ten finished seasons: more than a desktop rail shows at once.
const longRun: Series = show(5, {
  title: "Long Run", status: "Ended",
  seasons: Array.from({ length: 10 }, (_, i): Season => ({
    id: 50 + i, season_number: i + 1, name: `Season ${i + 1}`, monitored: true,
    episodes: Array.from({ length: 4 }, (_, n) => ep(i + 1, n + 1, { id: 50_000 + i * 10 + n })),
  })),
  stats: { episodes: 40, have_files: 40, size_bytes: 56_000_000_000, seasons: 10, missing: 0, unmonitored_missing: 0 },
});

export const seriesDetails: Record<number, Series> = {
  1: show(1, {}),
  2: anime,
  3: show(3, { title: "Tidewater" }),
  4: show(4, { title: "Anchor Point" }),
  5: longRun,
};

export const numbering: Record<number, SeriesNumbering> = {
  3: {
    source: "tmdb",
    pending: { from: "tmdb", to: "tvdb", created_at: "2026-10-08T10:00:00Z", plan_hash: "h1", files: 2, remaps: [
      { old: "S01E09", new: "S02E01", absolute: 0, file: "Tidewater - S01E09.mkv" },
      { old: "S01E10", new: "S02E02", absolute: 0, file: "Tidewater - S01E10.mkv" },
    ] },
  },
};

const attempt = (id: number, scope: string, minutesAgo: number, over: Partial<SearchAttempt>): SearchAttempt => ({
  id, media_type: "series", media_id: 1, scope, trigger: "manual", started_at: NOW - minutesAgo * 60_000, duration_ms: 1200,
  returned: 0, wrong_title: 0, blocklisted: 0, pending: 0, out_of_scope: 0, rejected: 0, eligible: 0, grabbed: 0,
  reasons: {}, top_reason: "", example: "", grabbed_titles: [], indexer_errors: {}, outcome: "nothing_found", reason: "", ...over,
});

export const searchAttempts: SearchAttempt[] = [
  attempt(31, "S02", 30, { returned: 34, wrong_title: 20, rejected: 14, reasons: { wrong_title: 20, bitrate_ceiling: 14 }, top_reason: "wrong_title", outcome: "none_suitable" }),
  attempt(30, "S02E06", 90, { returned: 0, outcome: "nothing_found" }),
];

export const history: MovieEvent[] = [
  { event: "imported", detail: "S03E01 — Harbour.Lights.S03E01.1080p.WEB-DL", created_at: new Date(NOW - 3 * 3_600_000).toISOString() },
  { event: "grabbed", detail: "S03E02 — Harbour.Lights.S03E02.1080p.WEB-DL", created_at: new Date(NOW - 2 * 3_600_000).toISOString() },
  { event: "searched", detail: "Season 2: Grabbed nothing · 34 found · 0 fit", created_at: new Date(NOW - 30 * 60_000).toISOString() },
  { event: "renamed", detail: "Renamed 4 files", created_at: new Date(NOW - 26 * 3_600_000).toISOString() },
];


export const searchJob: Job = {
  id: 951, kind: "series.grab-scope", target: "series:1:bulk", trigger: "user:1", status: "succeeded", progress: 1,
  message: "Grabbed nothing", error: "", created_at: new Date(NOW).toISOString(), started_at: new Date(NOW).toISOString(), finished_at: new Date(NOW).toISOString(),
};

const id = (params: string[]) => Number(params[0]);

// Show 4 has two episode files an import put in /tv/S01/Season 1 before #56 was fixed: one
// goes back into the show's S01 folder, and one is a lower-quality copy of an episode the
// show's folder already has.
export const misplaced: Record<number, MisplacedPlan> = {
  4: {
    show_folder: "/tv/Anchor Point",
    items: [
      { from: "/tv/S01/Season 1/Anchor Point - S01E02.mkv", to: "/tv/Anchor Point/S01/Anchor Point - S01E02.mkv", season: 1, episode: 2, action: "move", note: "/tv/S01 is named like a season folder" },
      {
        from: "/tv/S01/Season 1/Anchor Point - S01E03 - 720p.mkv", season: 1, episode: 3, action: "recycle", existing: "/tv/Anchor Point/S01/Anchor Point - S01E03 - 1080p.mkv",
        note: "A better copy is already in the show's folder (Anchor Point - S01E03 - 1080p.mkv); this lower-quality one goes to the recycle bin.",
      },
    ],
  },
};

// routes answers every call the series page makes for the shows above.
export function routes(): MockRoute[] {
  return [
    { method: "GET", path: /^\/api\/v1\/series\/(\d+)$/, respond: ({ params }) => seriesDetails[id(params)] ?? seriesDetails[1] },
    { method: "GET", path: /^\/api\/v1\/series\/(\d+)\/downloads$/, respond: ({ params }) => (id(params) === 1 ? [{ season: 3, episode: 2, state: "downloading", progress: 0.42, hash: "abc123" }] : []) },
    { method: "GET", path: /^\/api\/v1\/series\/(\d+)\/history$/, respond: ({ params }) => ({ events: id(params) === 1 ? history : [] }) },
    { method: "GET", path: /^\/api\/v1\/series\/(\d+)\/blocklist$/, body: { blocklist: [] } },
    { method: "GET", path: /^\/api\/v1\/series\/(\d+)\/duplicates$/, body: { duplicates: [], extra_files: 0, reclaimable_bytes: 0 } },
    { method: "GET", path: /^\/api\/v1\/series\/(\d+)\/aliases$/, body: [] },
    { method: "GET", path: /^\/api\/v1\/series\/(\d+)\/scene-map$/, body: { overrides: [] } },
    { method: "GET", path: /^\/api\/v1\/series\/(\d+)\/numbering$/, respond: ({ params }) => numbering[id(params)] ?? { source: "tmdb", pending: null } },
    { method: "GET", path: /^\/api\/v1\/series\/(\d+)\/rename$/, body: { items: [], matches: true } },
    { method: "GET", path: /^\/api\/v1\/series\/(\d+)\/misplaced$/, respond: ({ params }) => misplaced[id(params)] ?? { show_folder: "", items: [] } },
    {
      method: "POST", path: /^\/api\/v1\/series\/(\d+)\/misplaced$/,
      respond: ({ body }) => {
        const items = (body as { items?: MisplacedItem[] })?.items ?? [];
        return { moved: items.filter((it) => it.action === "move").length, recycled: items.filter((it) => it.action === "recycle").length, skipped: [], removed_folders: ["/tv/S01/Season 1"] };
      },
    },
    // The profile picker names the show's profile before the list arrives.
    { method: "GET", path: /^\/api\/v1\/quality\/profiles\/([\w-]+)$/, respond: ({ params }) => ({ key: params[0], name: "HD 1080p" }) },
    // Where a deleted episode file goes (the delete confirm says so).
    { method: "GET", path: "/api/v1/recycle/mode", body: { enabled: true, dirs: ["/tv/.arrmada-recycle"], retention_days: 30, max_gb: 0 } },
    { method: "GET", path: "/api/v1/searches", respond: ({ url }) => ({ attempts: url.searchParams.get("kind") === "series" && url.searchParams.get("id") === "1" ? searchAttempts : [] }) },
    { method: "POST", path: /^\/api\/v1\/series\/(\d+)\/search$/, status: 202, body: { status: "searching", job_id: searchJob.id, existing: false } },
    { method: "POST", path: /^\/api\/v1\/series\/(\d+)\/autograb$/, status: 202, body: { status: "searching", job_id: searchJob.id, existing: false } },
    { method: "GET", path: `/api/v1/jobs/${searchJob.id}`, body: searchJob },
    { method: "PUT", path: /^\/api\/v1\/series\/(\d+)\/monitor\/episodes$/, respond: ({ body }) => ({ updated: ((body as { episode_ids?: number[] })?.episode_ids ?? []).length }) },
    { method: "PUT", path: /^\/api\/v1\/series\/episodes\/(\d+)\/monitor$/, respond: ({ body }) => body },
    { method: "PUT", path: /^\/api\/v1\/series\/(\d+)\/seasons\/(\d+)\/monitor$/, respond: ({ body }) => body },
    { method: "PUT", path: /^\/api\/v1\/series\/(\d+)\/monitor$/, respond: ({ body }) => ({ monitored: true, monitor_new_seasons: true, ...(body as object) }) },
    { method: "POST", path: /^\/api\/v1\/series\/(\d+)\/refresh$/, respond: ({ params }) => seriesDetails[id(params)] },
  ];
}
