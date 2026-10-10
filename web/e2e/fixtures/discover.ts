import type { DiscoverCard, DiscoverRow, Genre, MediaDetail, MediaRequest, RequestList, SeriesSeason, UserNotification, WatchProvider } from "../../src/lib/api";
import type { PersonaInfo } from "./users";

// Discover's poster rows. The mix covers every badge a card can wear (none, Pending,
// Requested, Downloading, Wanted, Available) and one title long enough to test wrapping.
const poster = (n: number) => `https://image.tmdb.org/t/p/w500/e2e-poster-${n}.jpg`;

function card(n: number, title: string, extra: Partial<DiscoverCard> = {}): DiscoverCard {
  return {
    media_type: n % 3 === 0 ? "series" : "movie",
    tmdb_id: 1000 + n,
    title,
    year: 2020 + (n % 6),
    overview: `${title} is a fixture used by the browser smoke tests.`,
    poster_url: poster(n),
    backdrop_url: `https://image.tmdb.org/t/p/original/e2e-backdrop-${n}.jpg`,
    vote_average: 6 + (n % 4) * 0.7,
    release_date: `${2020 + (n % 6)}-0${1 + (n % 9)}-15`,
    genres: ["Drama", "Adventure"],
    in_library: false,
    has_file: false,
    ...extra,
  };
}

// The first card is the one the tap spec uses: requestable, so on a desktop it would
// carry the hover-only quick-request button.
export const requestableCard = card(1, "The Quiet Harbour");

export const cards: DiscoverCard[] = [
  requestableCard,
  card(2, "Lanterns Over the Northern Sea and Other Very Long Titles That Must Truncate Cleanly"),
  card(3, "Saltwind", { request_status: "pending" }),
  card(4, "Iron Tide", { request_status: "approved" }),
  card(5, "Driftwood", { request_status: "approved", download_progress: 0.42 }),
  card(6, "Anchor Point", { in_library: true, wanted: true }),
  card(7, "The Cartographer", { in_library: true, has_file: true }),
  card(8, "Gullwing", { poster_url: undefined }),
  card(9, "Undertow"),
  card(10, "Long Haul"),
];

// Shows for the season picker (REQ-13), kept out of the poster rows: a spec serves them
// where it needs them. One nobody has, one held up to its newest season, and one with 35
// seasons for the in-sheet scroll.
export const newShow = card(12, "Tidewater");
export const partlyOwnedShow = card(15, "The Lighthouse Keepers", { in_library: true, has_file: true });
export const longShow = card(18, "Evergreen Tide");

// seriesSeasons is GET /api/v1/media/series/{id}/seasons.
export function seriesSeasons(tmdbID: number): { seasons: SeriesSeason[] } {
  const s = (n: number, over: Partial<SeriesSeason> = {}): SeriesSeason => ({
    number: n, name: `Season ${n}`, episode_count: 10, air_date: `${1990 + n}-04-01`, have: 0, aired: 10,
    state: "requestable", requestable: true, ...over,
  });
  if (tmdbID === partlyOwnedShow.tmdb_id) {
    return { seasons: [1, 2, 3].map((n) => s(n, { state: "in_library", have: 10, requestable: false })).concat(s(4)) };
  }
  if (tmdbID === longShow.tmdb_id) return { seasons: Array.from({ length: 35 }, (_, i) => s(i + 1)) };
  // Anything else: two out, one asked for by someone else, one not out yet.
  return {
    seasons: [
      s(1), s(2),
      s(3, { state: "requested", requestable: false, request: { request_id: 40, status: "pending", mine: false } }),
      s(4, { state: "unaired", aired: 0, air_date: "2027-02-01", requestable: false }),
    ],
  };
}

export const items = { items: cards };

export const because: { rows: DiscoverRow[] } = {
  rows: [{ title: "Because you watched The Cartographer", seed: "The Cartographer", items: cards.slice(0, 6) }],
};

export const genres: { genres: Genre[] } = {
  genres: [{ id: 18, name: "Drama" }, { id: 12, name: "Adventure" }, { id: 35, name: "Comedy" }, { id: 878, name: "Science Fiction" }],
};

export const providers: { providers: WatchProvider[] } = {
  providers: [{ id: 8, name: "Netflix" }, { id: 337, name: "Disney Plus" }],
};

export function mediaDetail(media: string, tmdbID: number): MediaDetail {
  const c = [...cards, newShow, partlyOwnedShow, longShow].find((x) => x.tmdb_id === tmdbID) ?? requestableCard;
  return {
    media_type: media === "series" ? "series" : "movie",
    tmdb_id: c.tmdb_id,
    imdb_id: `tt${c.tmdb_id}`,
    title: c.title,
    year: c.year,
    overview: c.overview,
    poster_url: c.poster_url,
    backdrop_url: c.backdrop_url,
    runtime: 118,
    status: "Released",
    genres: c.genres,
    certification: "PG-13",
    studios: ["Fixture Pictures"],
    cast: [{ name: "Ada Mariner", character: "Captain" }, { name: "Theo Keel", character: "First mate" }],
    crew: [{ name: "Rowan Helm", job: "Director" }, { name: "Isla Bow", job: "Writer" }],
    ratings: { tmdb: c.vote_average, imdb: "7.4", rotten_tomatoes: "88%" },
    similar: cards.slice(2, 6),
    // The title's card with its badge state, which a title opened cold from its address uses.
    card: { ...c, media_type: media === "series" ? "series" : "movie" },
  };
}

// A title the server refuses (TMDB's adult flag or the adult filter): 404.
export const hiddenTitleID = 9999;

const at = "2026-10-01T09:00:00Z";

// GET /api/v1/requests (every section and the Discover strip answer the same rows): one
// waiting, two on their way (one a book asked for as an audiobook), and one the viewer
// follows rather than made.
export const requests: RequestList = {
  client_health: { ok: true },
  auto_approve: false,
  counts: { needs_approval: 1, in_progress: 3, ready: 0, declined: 0 },
  total: 4,
  requests: [
    { id: 1, media_type: "movie", tmdb_id: 1003, title: "Saltwind", year: 2023, poster_url: poster(3), status: "pending", requested_by: 3, requested_by_name: "deckhand", note: "The extended cut, if there is one", relation: "owner", available: false, tracking: { stage: "pending" }, created_at: at, updated_at: at },
    { id: 2, media_type: "movie", tmdb_id: 1005, title: "Driftwood", year: 2025, poster_url: poster(5), status: "approved", requested_by: 3, requested_by_name: "deckhand", relation: "owner", available: false, download_progress: 0.42, tracking: { stage: "downloading", progress: 0.42, eta_seconds: 900 }, created_at: at, updated_at: at },
    { id: 3, media_type: "series", tmdb_id: 1009, title: "Undertow", year: 2025, poster_url: poster(9), status: "approved", requested_by: 0, relation: "subscriber", available: false, tracking: { stage: "searching" }, created_at: at, updated_at: at },
    // A book asked for as an audiobook: its card carries the "Listen" badge.
    { id: 4, media_type: "book", tmdb_id: 0, ol_key: "OL9003W", author: "Hal Yard", formats: "audiobook", title: "Knots and Splices", year: 2017, status: "approved", requested_by: 3, requested_by_name: "deckhand", relation: "owner", available: false, tracking: { stage: "searching" }, created_at: at, updated_at: at },
  ],
};

// A pending request for three seasons of a show, opened by its id (?id=7) but kept off the
// lists so the Requests page's counts and bulk selection stay as they are.
export const seasonsRequest: MediaRequest = {
  id: 7, media_type: "series", tmdb_id: newShow.tmdb_id, title: newShow.title, year: newShow.year, poster_url: newShow.poster_url,
  status: "pending", requested_by: 3, requested_by_name: "deckhand", relation: "owner", seasons: [1, 2, 3], available: false,
  tracking: { stage: "pending" }, created_at: at, updated_at: at,
};

// requestDetail is GET /api/v1/requests/{id}: the listed request with that id.
export function requestDetail(id: number): { request: MediaRequest; client_health: { ok: boolean } } {
  const all = [...requests.requests, seasonsRequest];
  return { request: all.find((r) => r.id === id) ?? requests.requests[0], client_health: { ok: true } };
}

// approved is POST /api/v1/requests/{id}/approve: the request, approved (trimmed to the
// seasons sent, if any).
export function approved(id: number, body: unknown): MediaRequest {
  const r = requestDetail(id).request;
  const seasons = ((body ?? {}) as { seasons?: number[] }).seasons;
  return { ...r, status: "approved", tracking: { stage: "searching" }, ...(seasons ? { seasons } : {}) };
}

// bulk is POST /api/v1/requests/bulk: every id goes through.
export function bulk(body: unknown): { results: { id: number; ok: boolean }[] } {
  const ids = ((body ?? {}) as { ids?: number[] }).ids ?? [];
  return { results: ids.map((id) => ({ id, ok: true })) };
}

// created is what POST /api/v1/requests answers: a pending request for the card sent.
export function created(p: PersonaInfo, body: unknown): { request: MediaRequest; subscribed: boolean } {
  const b = (body ?? {}) as Partial<DiscoverCard> & { seasons?: number[] };
  return {
    subscribed: false,
    request: {
      id: 99, media_type: b.media_type ?? "movie", tmdb_id: b.tmdb_id ?? 0, title: b.title ?? "", year: b.year ?? 0, seasons: b.seasons,
      status: p.user.auto_approve ? "approved" : "pending", requested_by: p.user.id, requested_by_name: p.user.username,
      available: false, created_at: at, updated_at: at,
    },
  };
}

export const notifications: { notifications: UserNotification[]; unread: number } = {
  unread: 1,
  notifications: [{ id: 1, title: "The Cartographer is ready", body: "It's in the library now.", media_type: "movie", ref: "1007", read: false, created_at: Date.parse(at) }],
};
