import type { PersonaInfo } from "./users";
import * as discover from "./discover";
import * as media from "./media";
import * as mod from "./modules";
import * as sys from "./system";
import { NOW } from "./clock";

export interface MockRoute {
  method: string;
  /** An exact pathname (query ignored), or a regex whose groups become `params`. */
  path: string | RegExp;
  status?: number;
  /** The JSON to answer with (fixed). */
  body?: unknown;
  /** Or: build the answer from the request (path params from the regex groups). */
  respond?: (req: { url: URL; params: string[]; body: unknown }) => unknown;
}

const get = (path: string | RegExp, body: unknown): MockRoute => ({ method: "GET", path, body });

// routes is the fake backend: what each endpoint answers for this persona. Order
// matters only where a regex could shadow a later exact path.
export function routes(p: PersonaInfo): MockRoute[] {
  return [
    // Boot and shell
    get("/api/v1/status", sys.status(p)),
    get("/api/v1/auth/me", { user: p.user }),
    get("/api/health", sys.health),
    get("/api/v1/health/system", sys.systemHealth),
    get("/api/v1/setup", sys.setup),
    get("/api/v1/system/pending-restart", sys.pendingRestart),
    get("/api/v1/me/audio", sys.myAudio(p)),
    get("/api/v1/me/audio/listening", sys.myListening),
    get("/api/v1/me/notifications", discover.notifications),

    // Discover (requester and staff)
    get("/api/v1/requests", discover.requests),
    // Answered (not left unmocked) so a spec can assert on it: the tap spec checks
    // this is never called.
    { method: "POST", path: "/api/v1/requests", respond: ({ body }) => discover.created(p, body) },
    get("/api/v1/discover/trending", discover.items),
    get("/api/v1/discover/popular", discover.items),
    get("/api/v1/discover/upcoming", discover.items),
    get("/api/v1/discover/recommended", discover.items),
    get("/api/v1/discover/collections", discover.items),
    get("/api/v1/discover/provider", discover.items),
    get("/api/v1/discover", discover.items),
    get(/^\/api\/v1\/discover\/rows\/[a-z_]+$/, discover.items),
    get("/api/v1/discover/because", discover.because),
    get("/api/v1/discover/genres", discover.genres),
    get("/api/v1/discover/providers", discover.providers),
    get("/api/v1/discover/search", discover.items),
    { method: "GET", path: /^\/api\/v1\/media\/(movie|series)\/(\d+)$/, respond: ({ params }) => discover.mediaDetail(params[0], Number(params[1])) },
    get("/api/v1/calendar", media.calendar),
    get("/api/v1/me/books", media.myBooks),

    // Staff console
    get("/api/v1/dashboard", media.dashboard),
    get("/api/v1/downloads", media.activity),
    get("/api/v1/wanted", media.wanted),
    { method: "POST", path: /^\/api\/v1\/wanted\/(movie|series|book|music)\/(\d+)\/search$/, status: 202, body: media.wantedSearchStarted },
    get(`/api/v1/jobs/${media.wantedSearchStarted.job_id}`, media.wantedSearchJob),
    get("/api/v1/downloadclients", media.downloadClients),
    get(/^\/api\/v1\/downloadclients\/\d+\/status$/, { listen_port: 6881 }),
    get("/api/v1/indexers", media.indexers),
    get("/api/v1/indexers/prowlarr", { url: "", has_key: false }),
    get("/api/v1/flaresolverr/status", { configured: true, ok: true, url: "http://arrmada-flaresolverr:8191", version: "3.3.21", checked_at: new Date(NOW).toISOString() }),
    get("/api/v1/history", media.history),
    get("/api/v1/reviews", media.reviews),
    get("/api/v1/blocklist", media.blocklist),
    get("/api/v1/movies", media.movies),
    get("/api/v1/series", media.series),
    get("/api/v1/movies/unmatched", { unmatched: [] }),
    get("/api/v1/movies/search-queue", { running: [], queued: [] }),
    get("/api/v1/movies/downloads", media.movieDownloads),
    { method: "GET", path: "/api/v1/movies/wanted", respond: ({ url }) => (url.searchParams.get("tab") === "cutoff" ? media.moviesCutoff : media.moviesMissing) },
    { method: "POST", path: "/api/v1/movies/search", status: 202, respond: ({ body }) => ({ queued: ((body as { ids?: number[] })?.ids ?? []).length, duplicates: 0 }) },
    get("/api/v1/series/unmatched", { unmatched: [] }),
    get("/api/v1/books", media.books),
    get("/api/v1/books/upgrade", media.bookUpgrade),
    get("/api/v1/books/search-missing", media.bookSweep),
    get("/api/v1/books/authors/images", { images: {}, pending: 0 }),
    get("/api/v1/music/artists", mod.artists),
    get("/api/v1/quality/profiles", media.qualityProfiles),
    get("/api/v1/library/fit/profiles", media.fitProfiles),
    get("/api/v1/convert/stats", mod.convertStats),
    get("/api/v1/convert/hardware", mod.convertHardware),
    get("/api/v1/convert/settings", mod.convertSettings),
    get("/api/v1/convert/status", mod.convertStatus),
    get("/api/v1/convert/jobs", mod.convertJobs),
    get("/api/v1/recycle", sys.recycle),
    get("/api/v1/subtitles/coverage", mod.subtitleCoverage),
    get("/api/v1/subtitles/settings", mod.subtitleSettings),
    get("/api/v1/subtitles/jobs", mod.subtitleJobs),
    get("/api/v1/insights/plex", mod.plexConfig),
    get("/api/v1/insights/stats", mod.insightsStats),
    get("/api/v1/settings", sys.settings),
    get("/api/v1/system/library", sys.libraryPaths),
    { method: "GET", path: "/api/v1/system/library/check", respond: ({ url }) => sys.folderCheck(url.searchParams.get("path") ?? "") },
    get("/api/v1/logs", sys.logs),
    get("/api/v1/system/tasks", sys.tasks),
    get("/api/v1/jobs", sys.jobs),
  ];
}
