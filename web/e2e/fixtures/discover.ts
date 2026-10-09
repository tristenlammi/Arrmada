import type { DiscoverCard, DiscoverRow, Genre, MediaDetail, MediaRequest, UserNotification, WatchProvider } from "../../src/lib/api";
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
  const c = cards.find((x) => x.tmdb_id === tmdbID) ?? requestableCard;
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
  };
}

const at = "2026-10-01T09:00:00Z";

export const requests: { requests: MediaRequest[]; auto_approve: boolean; client_health?: { ok: boolean } } = {
  client_health: { ok: true },
  auto_approve: false,
  requests: [
    { id: 1, media_type: "movie", tmdb_id: 1003, title: "Saltwind", year: 2023, poster_url: poster(3), status: "pending", requested_by: 3, requested_by_name: "deckhand", available: false, tracking: { stage: "pending" }, created_at: at, updated_at: at },
    { id: 2, media_type: "movie", tmdb_id: 1005, title: "Driftwood", year: 2025, poster_url: poster(5), status: "approved", requested_by: 3, requested_by_name: "deckhand", available: false, download_progress: 0.42, tracking: { stage: "downloading", progress: 0.42, eta_seconds: 900 }, created_at: at, updated_at: at },
  ],
};

// created is what POST /api/v1/requests answers: a pending request for the card sent.
export function created(p: PersonaInfo, body: unknown): { request: MediaRequest; subscribed: boolean } {
  const b = (body ?? {}) as Partial<DiscoverCard>;
  return {
    subscribed: false,
    request: {
      id: 99, media_type: b.media_type ?? "movie", tmdb_id: b.tmdb_id ?? 0, title: b.title ?? "", year: b.year ?? 0,
      status: p.user.auto_approve ? "approved" : "pending", requested_by: p.user.id, requested_by_name: p.user.username,
      available: false, created_at: at, updated_at: at,
    },
  };
}

export const notifications: { notifications: UserNotification[]; unread: number } = {
  unread: 1,
  notifications: [{ id: 1, title: "The Cartographer is ready", body: "It's in the library now.", media_type: "movie", ref: "1007", read: false, created_at: Date.parse(at) }],
};
