import type { PersonaInfo } from "./users";
import * as books from "./books";
import * as discover from "./discover";
import * as media from "./media";
import * as mod from "./modules";
import * as sys from "./system";
import * as alerts from "./alerts";
import * as audio from "./audio";
import * as seriesDetail from "./seriesDetail";
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
  const staff = p.user.role === "admin" || p.user.role === "manager";
  return [
    // Boot and shell
    get("/api/v1/status", sys.status(p)),
    get("/api/v1/auth/me", { user: p.user }),
    get("/api/health", sys.health),
    get("/api/v1/health/system", sys.systemHealth),
    get("/api/v1/setup", sys.setup),
    get("/api/v1/setup/checklist", sys.setupChecklist),
    get("/api/v1/system/backups", sys.backups),
    get("/api/v1/system/pending-restart", sys.pendingRestart),
    // Manual import's picker opens with the finished downloads that match: none here.
    get(/^\/api\/v1\/(movies|series|books)\/\d+\/manualimport\/suggestions$/, { suggestions: [] }),
    get("/api/v1/system/status", sys.systemStatus),
    // Settings → Status → Updates: the switch answers with the check off; Check now with
    // the same answer as Status.
    { method: "PUT", path: "/api/v1/system/update-check", respond: ({ body }) => ({ ...sys.systemStatus.update, ...(body as object), latest: null, behind: null }) },
    { method: "POST", path: "/api/v1/system/update-check", body: sys.systemStatus.update },
    // Settings → System → Dashboard widget: no key made yet.
    get("/api/v1/system/widget-key", { enabled: false, outside: false }),
    get("/api/v1/me/audio", sys.myAudio(p)),
    get("/api/v1/me/audio/listening", sys.myListening),
    // The Listen tab (the player specs answer the rest of the listening API).
    get("/api/v1/me/audio/shelves", { shelves: audio.shelves }),
    get("/api/v1/me/audio/library", { items: [audio.carl, audio.doomsday, audio.finished], total: 3, page: 0, limit: 60 }),
    // Covers are images; the answer only has to exist (an empty one draws the blank cover).
    { method: "GET", path: /^\/api\/v1\/me\/audio\/items\/[^/]+\/cover$/, status: 204 },
    get("/api/v1/me/notifications", discover.notifications),
    { method: "POST", path: /^\/api\/v1\/me\/notifications\/\d+\/read$/, status: 204 },
    get("/api/v1/me/quota", sys.quota(p)),
    // The Me page's calendar feed card: no link yet (APP-18).
    get("/api/v1/me/calendar-feed", { enabled: false, mine: p.user.role !== "admin" && p.user.role !== "manager" }),
    // The Me page's notification settings: no push key (it says push isn't set up), no Apprise link.
    get("/api/v1/me/push/key", { key: "" }),
    get("/api/v1/me/apprise", { set: false, hint: "" }),
    // Which notices reach your phones: everything, until changed.
    get("/api/v1/me/notify-prefs", { approved: true, declined: true, ready: true, new_request: true }),
    { method: "PUT", path: "/api/v1/me/notify-prefs", respond: ({ body }) => ({ approved: true, declined: true, ready: true, new_request: true, ...(body as object) }) },
    // Me → Account: a password-holding account, signed in here and on one other device.
    get("/api/v1/me/account", { password_set: true }),
    get("/api/v1/me/sessions", { sessions: [
      { id: "a1b2c3d4e5f6", created_at: new Date(NOW - 86_400_000).toISOString(), last_seen_at: new Date(NOW).toISOString(), device: "Chrome on Android", network: "local", current: true },
      { id: "0f9e8d7c6b5a", created_at: new Date(NOW - 20 * 86_400_000).toISOString(), last_seen_at: new Date(NOW - 3 * 86_400_000).toISOString(), device: "Safari on Mac", network: "203.0.113.x", current: false },
    ] }),
    // Me → Personal API keys: none yet; making one asks for the password.
    get("/api/v1/me/api-keys", { keys: [], expiry_days: [0, 30, 90, 365], max: 25, password_required: true }),
    // Me → Account → Two-factor sign-in: off, and not required.
    get("/api/v1/me/2fa", { enabled: false, recovery_codes_left: 0, password_set: true, required: false }),
    // Your own Plex link (sidebar footer, Me page): not linked.
    get("/api/v1/me/plex", { linked: false, can_unlink: false }),
    // Your own Jellyfin link (Me page): none, and no Jellyfin connected to link to.
    get("/api/v1/me/jellyfin", { linked: false, can_unlink: false, available: false, quickconnect: false }),

    // Discover (requester and staff)
    // Staff see what approving a waiting request would grab; a requester never does.
    { method: "GET", path: "/api/v1/requests", respond: ({ url }) => discover.requestList(url, staff) },
    // Answered (not left unmocked) so a spec can assert on it: the tap spec checks
    // this is never called.
    { method: "POST", path: "/api/v1/requests", respond: ({ body }) => discover.created(p, body) },
    { method: "POST", path: "/api/v1/requests/bulk", respond: ({ body }) => discover.bulk(body) },
    // Reported problems: none open (the Requests page reads the count for its Issues tab).
    get("/api/v1/issues", { issues: [], total: 0, counts: { open: 0, closed: 0 } }),
    { method: "POST", path: "/api/v1/issues", respond: ({ body }) => ({ issue: { id: 1, status: "open", reporter_count: 1, ...(body as object) }, joined: false }) },
    get(/^\/api\/v1\/issues\/episodes\/\d+$/, { seasons: [{ season: 1, episodes: [{ episode: 3, title: "Low Water" }, { episode: 4, title: "Fog Bank" }] }] }),
    { method: "GET", path: /^\/api\/v1\/requests\/(\d+)$/, respond: ({ params }) => discover.requestDetail(Number(params[0]), staff) },
    { method: "POST", path: /^\/api\/v1\/requests\/(\d+)\/preview$/, status: 202, body: { job_id: discover.previewJob.id } },
    get(`/api/v1/jobs/${discover.previewJob.id}`, discover.previewJob),
    { method: "POST", path: /^\/api\/v1\/requests\/(\d+)\/approve$/, respond: ({ params, body }) => discover.approved(Number(params[0]), body) },
    { method: "POST", path: /^\/api\/v1\/requests\/(\d+)\/decline$/, body: { status: "declined" } },
    get("/api/v1/discover/trending", discover.items),
    get("/api/v1/discover/popular", discover.items),
    get("/api/v1/discover/upcoming", discover.items),
    get("/api/v1/discover/recommended", discover.items),
    get("/api/v1/discover/collections", discover.collectionRows),
    get(`/api/v1/discover/collection/${discover.collection.id}`, discover.collection),
    get("/api/v1/discover/recently-added", discover.recentlyAdded),
    get("/api/v1/discover/provider", discover.items),
    get("/api/v1/discover", discover.items),
    get(/^\/api\/v1\/discover\/rows\/[a-z_]+$/, discover.items),
    get("/api/v1/discover/because", discover.because),
    get("/api/v1/discover/genres", discover.genres),
    get("/api/v1/discover/providers", discover.providers),
    { method: "GET", path: "/api/v1/discover/search", respond: ({ url }) => discover.gridPage(url) },
    { method: "GET", path: "/api/v1/discover/browse", respond: ({ url }) => discover.gridPage(url) },
    get(`/api/v1/discover/person/${discover.person.id}`, discover.person),
    // Anyone else: not found (TMDB doesn't know them, or the adult filter keeps them out).
    { method: "GET", path: /^\/api\/v1\/discover\/person\/\d+$/, status: 404, body: { message: "That person isn't available." } },
    { method: "GET", path: `/api/v1/media/movie/${discover.hiddenTitleID}`, status: 404, body: { message: "That title isn't available." } },
    { method: "GET", path: /^\/api\/v1\/media\/(movie|series)\/(\d+)$/, respond: ({ params }) => discover.mediaDetail(params[0], Number(params[1])) },
    { method: "GET", path: /^\/api\/v1\/media\/series\/(\d+)\/seasons$/, respond: ({ params }) => discover.seriesSeasons(Number(params[0])) },
    { method: "GET", path: "/api/v1/calendar", respond: ({ url }) => media.calendar(url, staff) },
    get("/api/v1/me/books", media.myBooks),
    get("/api/v1/me/kindle", media.myKindle),

    // Discover → Books
    get(/^\/api\/v1\/books\/discover\/browse\/[a-z_]+$/, books.browse),
    get("/api/v1/books/discover/recommended", { rows: [] }),
    // Hardcover ("hc:") books also ask for "readers also liked".
    get("/api/v1/books/discover/similar", { books: [] }),
    { method: "GET", path: "/api/v1/books/discover/detail", respond: ({ url }) => books.detail(url.searchParams.get("key") ?? "") },

    // Staff console
    get("/api/v1/dashboard", media.dashboard),
    // The staff shell polls this on every page (sidebar badges, the Dashboard card).
    get("/api/v1/attention", sys.attention),
    get("/api/v1/downloads", media.activity),
    get("/api/v1/wanted", media.wanted),
    { method: "POST", path: /^\/api\/v1\/wanted\/(movie|series|book|music)\/(\d+)\/search$/, status: 202, body: media.wantedSearchStarted },
    get(`/api/v1/jobs/${media.wantedSearchStarted.job_id}`, media.wantedSearchJob),
    get("/api/v1/downloadclients", media.downloadClients),
    get(/^\/api\/v1\/downloadclients\/\d+\/status$/, { listen_port: 6881 }),
    get("/api/v1/indexers", media.indexers),
    get("/api/v1/indexers/prowlarr", { url: "", has_key: false, ui_port: "9696", ui_url: "" }),
    get("/api/v1/connections", media.connections),
    { method: "POST", path: /^\/api\/v1\/connections\/([a-z_]+)\/([^/]+)\/test$/, respond: ({ params }) => media.connectionTest(params[0], params[1]) },
    // The Jellyfin connection, edited in its card on Connections.
    get("/api/v1/jellyfin", media.jellyfinConfig),
    { method: "PUT", path: "/api/v1/jellyfin", respond: ({ body }) => media.jellyfinSave(body as Parameters<typeof media.jellyfinSave>[0]) },
    { method: "POST", path: "/api/v1/jellyfin/test", respond: ({ body }) => media.jellyfinTest(body as { url?: string }) },
    { method: "DELETE", path: "/api/v1/jellyfin", body: media.jellyfinConfig },
    // Its library updates and watch monitoring, on Settings → Media servers.
    get("/api/v1/jellyfin/scan", mod.jellyfinScan),
    { method: "PUT", path: "/api/v1/jellyfin/monitoring", respond: ({ body }) => ({ ...media.jellyfinConfig, ...(body as object) }) },
    get("/api/v1/apikeys", media.apiKeys),
    { method: "GET", path: "/api/v1/history", respond: ({ url }) => (url.searchParams.get("view") === "imports" ? media.importedFiles : media.historyPage(url)) },
    get("/api/v1/reviews", media.reviews),
    { method: "POST", path: /^\/api\/v1\/reviews\/(\d+)\/(import|map)$/, status: 202, body: media.reviewImportStarted },
    get(`/api/v1/jobs/${media.reviewImportJob.id}`, media.reviewImportJob),
    // Activity → Needs you's actions.
    { method: "POST", path: /^\/api\/v1\/imports\/([0-9a-f]+)\/retry$/, body: { status: "retrying" } },
    { method: "POST", path: /^\/api\/v1\/queue\/([0-9a-f]+)\/failover$/, status: 202, body: { status: "searching", job_id: sys.failOverJob.id, existing: false } },
    get(`/api/v1/jobs/${sys.failOverJob.id}`, sys.failOverJob),
    { method: "POST", path: /^\/api\/v1\/queue\/([0-9a-f]+)\/category$/, respond: ({ body }) => ({ status: "moved", category: (body as { category?: string })?.category ?? "" }) },
    { method: "POST", path: /^\/api\/v1\/queue\/([0-9a-f]+)\/action$/, body: { status: "ok" } },
    // The Queue's select mode: every picked download removed.
    { method: "POST", path: "/api/v1/queue/remove", respond: ({ body }) => { const b = body as { items?: unknown[]; mode?: string }; return { mode: b?.mode ?? "keep_files", removed: (b?.items ?? []).length, failed: [] }; } },
    // Finished torrents Arrmada didn't grab (ACQ-31).
    get("/api/v1/downloads/unmanaged", sys.unmanaged),
    { method: "GET", path: /^\/api\/v1\/queue\/([0-9a-z]+)\/targets$/, respond: ({ url }) => sys.unmanagedTargets(url) },
    { method: "POST", path: /^\/api\/v1\/queue\/([0-9a-z]+)\/import$/, status: 202, body: sys.unmanagedImportStarted },
    get(`/api/v1/jobs/${sys.unmanagedImportJob.id}`, sys.unmanagedImportJob),
    { method: "POST", path: /^\/api\/v1\/queue\/([0-9a-z]+)\/ignore$/, body: { status: "ignored" } },
    get("/api/v1/blocklist", media.blocklist),
    get("/api/v1/movies", media.movies),
    get("/api/v1/series", media.series),
    // The command palette's library lookup: staff only, like the server.
    staff
      ? { method: "GET", path: "/api/v1/library/search", respond: ({ url }) => media.librarySearch(url) }
      : { method: "GET", path: "/api/v1/library/search", status: 403, body: { message: "forbidden" } },
    // The series detail page (its own fixture file).
    ...seriesDetail.routes(),
    get("/api/v1/movies/unmatched", { unmatched: [] }),
    get("/api/v1/movies/search-queue", { running: [], queued: [] }),
    get("/api/v1/movies/downloads", media.movieDownloads),
    { method: "GET", path: "/api/v1/movies/wanted", respond: ({ url }) => (url.searchParams.get("tab") === "cutoff" ? media.moviesCutoff : media.moviesMissing) },
    { method: "POST", path: "/api/v1/movies/search", status: 202, respond: ({ body }) => ({ queued: ((body as { ids?: number[] })?.ids ?? []).length, duplicates: 0 }) },
    // A film's page and its panels (MOV-18); its delete is answered below.
    { method: "GET", path: /^\/api\/v1\/movies\/(\d+)$/, respond: ({ params }) => media.movieDetail(Number(params[0])) },
    get(/^\/api\/v1\/movies\/\d+\/history$/, { events: [] }),
    get(/^\/api\/v1\/movies\/\d+\/blocklist$/, { blocklist: [] }),
    // The mass editor (MOV-19): a profile change finds The Cartographer's file too big;
    // Driftwood's search can't be queued (a partial failure).
    { method: "PUT", path: "/api/v1/movies/bulk", respond: ({ body }) => media.bulkUpdate(body) },
    { method: "POST", path: "/api/v1/movies/bulk/regrab", status: 202, respond: ({ body }) => ({ queued: ((body as { ids?: number[] })?.ids ?? []).length, duplicates: 0, errors: [] }) },
    { method: "DELETE", path: "/api/v1/movies/bulk", respond: ({ body }) => {
      const ids = (body as { ids?: number[] })?.ids ?? [];
      return { deleted: ids.length, failed: 0, results: ids.map((id) => ({ id, deleted: true })) };
    } },
    get("/api/v1/series/unmatched", { unmatched: [] }),
    // The library pages: fit reports, removing a title, and the bulk bar's per-title calls.
    { method: "GET", path: "/api/v1/library/fit", respond: ({ url }) => (url.searchParams.get("media") === "series" ? media.seriesFit : media.movieFit) },
    get("/api/v1/recycle/mode", media.recycleMode),
    get(/^\/api\/v1\/movies\/\d+\/delete-preview$/, media.movieDeletePreview),
    get(/^\/api\/v1\/series\/\d+\/delete-preview$/, media.seriesDeletePreview),
    { method: "DELETE", path: /^\/api\/v1\/(movies|series)\/\d+$/, status: 204 },
    { method: "PUT", path: /^\/api\/v1\/(movies|series)\/\d+\/profile$/, respond: ({ body }) => ({ quality_profile: (body as { quality_profile?: string })?.quality_profile ?? "", downgrade: false }) },
    { method: "PUT", path: /^\/api\/v1\/(movies|series)\/\d+\/monitor$/, respond: ({ body }) => ({ monitored: !!(body as { monitored?: boolean })?.monitored, monitor_new_seasons: true }) },
    { method: "POST", path: /^\/api\/v1\/movies\/\d+\/search$/, status: 202, body: { status: "queued", queued: true, position: 1 } },
    { method: "POST", path: /^\/api\/v1\/series\/\d+\/search$/, status: 202, body: { status: "searching" } },
    get(/^\/api\/v1\/movies\/\d+\/releases$/, media.movieReleases),
    get("/api/v1/books", media.books),
    get("/api/v1/books/upgrade", media.bookUpgrade),
    get("/api/v1/books/search-missing", media.bookSweep),
    get("/api/v1/books/authors/images", { images: {}, pending: 0 }),
    get("/api/v1/books/duplicates", { groups: [], past_merges: [] }),
    get("/api/v1/music/artists", mod.artists),
    get("/api/v1/music/matching", mod.musicMatching),
    { method: "PUT", path: "/api/v1/music/matching", respond: ({ body }) => ({ settings: { ...mod.musicMatching.settings, ...(body as object) } }) },
    get("/api/v1/music/release-defaults", mod.musicReleaseDefaults),
    { method: "PUT", path: "/api/v1/music/release-defaults", respond: ({ body }) => ({ settings: { ...mod.musicReleaseDefaults.settings, ...(body as object) } }) },
    get("/api/v1/music/new-releases", { albums: [], days: 30 }),
    // Album and artist searching (MUS-09, MUS-16, MUS-17); e2e/music-search.spec.ts
    // answers them with more.
    get(/^\/api\/v1\/music\/albums\/\d+\/search-state$/, { upgrade: { profile: "Lossless", state: "incomplete", cutoff: "FLAC" } }),
    get(/^\/api\/v1\/music\/albums\/\d+\/releases$/, { profile: "lossless", releases: [] }),
    { method: "POST", path: /^\/api\/v1\/music\/artists\/\d+\/search$/, status: 202, body: { status: "searching" } },
    // MUS-15: albums with track files gone from disk (none in the fixtures), and the scan's
    // last summary (none yet).
    get("/api/v1/music/search-missing", { albums: 0 }),
    get("/api/v1/music/scan", { last: null }),
    // Per media, as the server answers: ?media=series lists the series profiles.
    { method: "GET", path: "/api/v1/quality/profiles", respond: ({ url }) => (url.searchParams.get("media") === "series" ? media.seriesQualityProfiles : media.qualityProfiles) },
    // Settings → Downloads and → Book server (only the full Settings sweep visits them).
    get("/api/v1/downloads/disk-guard", { enabled: true, measurable: true, path: "/downloads", used_pct: 42, pause_pct: 95, resume_pct: 90, holding: 0, measure_path: "", measured_path: "/downloads", override: false, measure_unreadable: false, total_bytes: 4398046511104, free_bytes: 2551066976256, shared_with: [], shared_with_library: false }),
    get("/api/v1/audioserver", { enabled: false, running: false, host_port: "13378", public_url: "", users: [], devices: [], items: 0, items_ready: 0, trace_until: 0 }),
    get(/^\/api\/v1\/quality\/ranking-shadow/, { mode: "v2", decisions: 0, disagreements: 0, days: [], disagreement_rows: [] }),
    get("/api/v1/library/fit/profiles", media.fitProfiles),
    get("/api/v1/convert/stats", mod.convertStats),
    get("/api/v1/convert/hardware", mod.convertHardware),
    get("/api/v1/convert/settings", mod.convertSettings),
    get("/api/v1/convert/status", mod.convertStatus),
    get("/api/v1/convert/jobs", mod.convertJobs),
    get("/api/v1/convert/history", mod.convertHistory),
    get("/api/v1/convert/devices", mod.convertDevices),
    get("/api/v1/recycle", sys.recycle),
    get("/api/v1/subtitles/coverage", mod.subtitleCoverage),
    get("/api/v1/subtitles/settings", mod.subtitleSettings),
    get("/api/v1/subtitles/models", mod.subtitleModels),
    get("/api/v1/subtitles/jobs", mod.subtitleJobs),
    get("/api/v1/subtitles/profiles", mod.subtitleProfiles),
    get("/api/v1/subtitles/attention", mod.subtitleAttention),
    // The per-file drawer and the detail pages' subtitle chips.
    { method: "GET", path: "/api/v1/subtitles/file", respond: ({ url }) => mod.subtitleFile(url) },
    get("/api/v1/subtitles/library", { items: [], groups: [], scanning: false, stale: false }),
    get("/api/v1/languages", mod.languages),
    get("/api/v1/insights/plex", mod.plexConfig),
    // The media servers Insights can watch: Plex only, not connected (Jellyfin isn't wired).
    get("/api/v1/insights/servers", { servers: [{ server: "plex", configured: false, monitoring: false, status: "unconfigured", poll_seconds: 30 }] }),
    get("/api/v1/insights/plex/scan", mod.plexScan),
    // Watch links: no media server connected, so no title is on one.
    { method: "GET", path: "/api/v1/plex/link", status: 204 },
    get("/api/v1/media/links", { links: [] }),
    get(/^\/api\/v1\/(movies|series)\/\d+\/watch-stats$/, { available: false, plays: 0, last_played: 0, users: [] }),
    get("/api/v1/insights/stats", mod.insightsStats),
    // Settings → Import (admin): no double-counted plays, live recording since NOW - 30d.
    get("/api/v1/insights/import/overlaps", { count: 0, first_live_at: Math.floor(NOW / 1000) - 30 * 86400 }),
    get("/api/v1/insights/import/tautulli", mod.tautulliConfig),
    get("/api/v1/insights/import/runs", mod.importRuns),
    { method: "DELETE", path: /^\/api\/v1\/insights\/import\/runs\/(\d+)\/rows$/, status: 202, body: { job_id: mod.removeImportJob.id, existing: false } },
    get(`/api/v1/jobs/${mod.removeImportJob.id}`, mod.removeImportJob),
    get("/api/v1/settings", sys.settings),
    // Settings → Naming & metadata → Book naming: the presets, and a live preview answered
    // from the templates sent, so a spec can type and watch it follow.
    get("/api/v1/books/naming/presets", sys.bookNamingPresets),
    { method: "GET", path: "/api/v1/books/naming/preview", respond: ({ url }) => sys.bookNamingPreview(url) },
    // ...and Music naming & tags, the same way.
    get("/api/v1/music/naming/presets", sys.musicNamingPresets),
    { method: "GET", path: "/api/v1/music/naming/preview", respond: ({ url }) => sys.musicNamingPreview(url) },
    // Settings → Users: two accounts, one a Plex sign-in that still auto-approves shows.
    get("/api/v1/users/plex-blocks", { blocks: [] }),
    // People & access → Personal API keys: nobody has made one.
    get("/api/v1/users/api-keys", { keys: [] }),
    get("/api/v1/users/jellyfin-blocks", { blocks: [] }),
    get("/api/v1/users", sys.users),
    get("/api/v1/people", sys.people),
    // Answers like the server: the account as it now stands.
    { method: "PUT", path: /^\/api\/v1\/users\/(\d+)$/, respond: ({ params, body }) => ({ disabled: false, ...sys.users.users.find((u) => u.id === Number(params[0])), id: Number(params[0]), ...(body as object) }) },
    get("/api/v1/system/library", sys.libraryPaths),
    get("/api/v1/library/folders", sys.libraryFolders),
    { method: "GET", path: "/api/v1/library/folders/suggest", body: { folder_id: 0 } },
    { method: "POST", path: "/api/v1/import/arr/preview", body: sys.arrPreview },
    { method: "POST", path: "/api/v1/import/arr/run", status: 202, body: { status: "started", name: "Radarr 4K", job_id: sys.arrImportJob.id, existing: false } },
    get(`/api/v1/jobs/${sys.arrImportJob.id}`, sys.arrImportJob),
    { method: "GET", path: "/api/v1/system/library/check", respond: ({ url }) => sys.folderCheck(url.searchParams.get("path") ?? "") },
    get("/api/v1/logs", sys.logs),
    get("/api/v1/logs/level", sys.logLevel),
    get("/api/v1/system/tasks", sys.tasks),
    get("/api/v1/jobs", sys.jobs),
    get("/api/v1/system/jobs", sys.recentJobs),

    // Settings → Alerts
    get("/api/v1/notifications", alerts.connections),
    get("/api/v1/notifications/catalog", alerts.catalog),
    get("/api/v1/notifications/presets", alerts.presets),
    { method: "POST", path: "/api/v1/notifications", respond: ({ body }) => ({ id: 3, url_set: true, url_hint: "discord://••••", ...(body as object) }) },
    get(/^\/api\/v1\/notifications\/\d+\/deliveries$/, alerts.deliveries),
    { method: "POST", path: /^\/api\/v1\/notifications\/\d+\/test$/, body: { ok: true } },
    // Settings → Book server → Send to Kindle email (admin): not set up yet.
    get("/api/v1/settings/musicbrainz", {
      base_url: "", default_base_url: "https://musicbrainz.org", mirror_rate: 1, max_mirror_rate: 50, contact: "",
      cache: { entries: 1240, bytes: 3_400_000, stale: 12 }, paused: false,
    }),
    get("/api/v1/settings/smtp", { configured: false, host_set: false, port: 587, security: "starttls", from: "", username_set: false, password_set: false }),
  ];
}
