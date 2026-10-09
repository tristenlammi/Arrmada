// Package httpapi wires Arrmada's HTTP surface: a versioned JSON API plus the
// embedded web UI, both served from one port. M0 ships the skeleton — health,
// status, request logging, and panic recovery — that later modules hang off.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/apikeys"
	"github.com/tristenlammi/arrmada/internal/applog"
	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/backup"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/buildinfo"
	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/convert"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/flaresolverr"
	"github.com/tristenlammi/arrmada/internal/health"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/notify"
	"github.com/tristenlammi/arrmada/internal/push"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/realtime"
	"github.com/tristenlammi/arrmada/internal/recyclebin"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/safego"
	"github.com/tristenlammi/arrmada/internal/scheduler"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
	"github.com/tristenlammi/arrmada/internal/subtitles"
	"github.com/tristenlammi/arrmada/internal/webui"
)

// Deps bundles everything the HTTP layer needs. Grouping them keeps New's
// signature stable as more subsystems come online.
type Deps struct {
	Config    config.Config
	Log       *slog.Logger
	Store     *store.Store
	Bus       *eventbus.Bus
	Auth      *auth.Service
	Realtime  *realtime.Hub
	Indexers  *indexer.Service
	Downloads *download.Service
	// DiskGuard holds downloads while the downloads volume is too full. Optional:
	// nil simply means the health panel can't report on it.
	DiskGuard  *download.DiskGuard
	Library    *library.Manager
	Movies     *movies.Service
	Quality    *quality.Service
	Settings   *settings.Service
	Automation *automation.Coordinator
	Notify     *notify.Service
	Series     *series.Service
	Requests   *requests.Service
	Discovery  metadata.DiscoveryProvider
	Ratings    metadata.RatingProvider
	Books      *books.Service
	Music      *music.Service
	Subtitles  *subtitles.Service
	Convert    *convert.Service
	Insights   *insights.Service
	Push       *push.Service
	Recycle    *recyclebin.Service
	Logs       *applog.Ring
	APIKeys    *apikeys.Store
	// FlareSolverr, whose URL is the "flaresolverr" API key (read on every use).
	FlareSolverr *flaresolverr.Client
	// KeyVerifiers back the API key Test button, by key id: each makes one real request
	// with candidate (a value typed and not yet saved) or, when that's empty, the saved
	// key, and answers in words. Built in main; Hardcover is tested through Books.
	KeyVerifiers map[string]func(ctx context.Context, candidate string) (string, error)
	// The audiobook server for listening apps, and its listener.
	AudioServer  *audioserver.Server
	AudioManager *audioserver.Manager
	// Restart shuts the app down cleanly so Docker's restart policy starts it again
	// (first-run setup uses it to apply new library folders). nil = not offered.
	Restart func()
	// Snapshot copies the database to <data>/backups/arrmada-<kind>-<UTC>.db before an
	// action that erases data a backup is the only way back from (deleting a user takes
	// their audiobook places with it). nil = no copy possible, so those actions refuse.
	Snapshot func(ctx context.Context, kind string) (string, error)
	// RunGroup holds the work requests start in the background: it carries the run
	// context that shutdown cancels and names whatever is still going at exit. nil (tests)
	// runs that work panic-safe but untracked.
	RunGroup *safego.Group
	// Backups makes the nightly and manual database copies. nil = no backups wired: the
	// manual action answers 503 and the health panel says nothing about them.
	Backups *backup.Service
	// Health holds the background health checks; New registers the ones built from these
	// deps, and /health/system serves its cached results. nil = an empty, healthy report.
	Health *health.Registry
	// OnFoldersChanged runs in the background after Settings → Library saves folders
	// whose resolved path changed ("movies", "downloads", …), with a context that
	// outlives the request but not shutdown. Everything else reads the folders live;
	// this is for what has to be told, like qBittorrent's default save path. nil = none.
	OnFoldersChanged func(ctx context.Context, changed []string)
	// Scheduler runs the recurring tasks; the Tasks API reads and triggers it. nil = the
	// task list is empty and Run now answers 503.
	Scheduler *scheduler.Scheduler
	// Jobs runs and records the work requests start (searches, scans, imports). nil
	// (tests, tools) runs that work untracked on the run group instead.
	Jobs JobRunner
}

type api struct {
	deps         Deps
	start        time.Time
	loginLimiter *loginLimiter // throttles auth attempts (login/setup/plex-pin)
}

// New builds the HTTP server: JSON API routes, the embedded UI (with SPA
// fallback), and the middleware chain (recover → log → mux).
func New(d Deps) *http.Server {
	a := &api{deps: d, start: time.Now(), loginLimiter: newLoginLimiter(10, 15*time.Minute)}

	rt := newRouter(a)
	a.registerRoutes(rt)
	if d.Health != nil {
		a.registerHealthChecks(d.Health)
	}

	// Chain: recover → authenticate (resolves the user) → external gate (LAN vs
	// outside) → log → routes.
	handler := a.recoverPanics(a.securityHeaders(a.authenticate(a.externalGate(a.logRequests(rt)))))

	return &http.Server{
		Addr:              d.Config.Addr(),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// Keep idle keep-alive connections open well past the UI's 3s poll so the
		// server never closes a connection the browser is about to reuse (which
		// surfaces as a spurious "failed to fetch").
		IdleTimeout: 120 * time.Second,
	}
}

// registerRoutes is the route table. mux is the scoped router, not a bare ServeMux:
// its HandleFunc only takes a guard (public, signedIn or requireRole), so every route
// says who may call it. Anything that isn't part of the requester-facing surface is
// staff (requireRole(auth.RoleManager, …)) or admin; testdata/routes.golden lists the
// result.
func (a *api) registerRoutes(mux *router) {
	mux.HandleFunc("GET /api/health", a.public(a.handleHealth).ext())
	mux.HandleFunc("GET /api/v1/health/system", a.requireRole(auth.RoleManager, a.handleSystemHealth))
	mux.HandleFunc("GET /api/v1/status", a.public(a.handleStatus).ext())
	mux.HandleFunc("GET /api/v1/dashboard", a.requireRole(auth.RoleManager, a.handleDashboard))

	// App preferences. API keys are the owner's credentials for outside services, so
	// only an admin sees or changes them. Managers keep the settings they need day to
	// day; handleUpdateSettings refuses them the admin-only fields.
	mux.HandleFunc("GET /api/v1/apikeys", a.requireRole(auth.RoleAdmin, a.handleGetAPIKeys))
	mux.HandleFunc("PUT /api/v1/apikeys/{id}", a.requireRole(auth.RoleAdmin, a.handleSetAPIKey))
	mux.HandleFunc("DELETE /api/v1/apikeys/{id}", a.requireRole(auth.RoleAdmin, a.handleClearAPIKey))
	mux.HandleFunc("POST /api/v1/apikeys/{id}/test", a.requireRole(auth.RoleAdmin, a.handleTestAPIKey))
	mux.HandleFunc("GET /api/v1/settings", a.requireRole(auth.RoleManager, a.handleGetSettings))
	mux.HandleFunc("PUT /api/v1/settings", a.requireRole(auth.RoleManager, a.handleUpdateSettings))
	// Library folders + filesystem browser (in-app folder picker). Managers can read the
	// folders (Settings → Library shows them, and they scan from there) but only an admin
	// may move a library or walk the host's filesystem.
	mux.HandleFunc("GET /api/v1/system/library", a.requireRole(auth.RoleManager, a.handleGetLibraryPaths))
	mux.HandleFunc("PUT /api/v1/system/library", a.requireRole(auth.RoleAdmin, a.handleSetLibraryPaths))
	mux.HandleFunc("GET /api/v1/system/library/check", a.requireRole(auth.RoleAdmin, a.handleCheckLibraryFolder))
	mux.HandleFunc("GET /api/v1/system/pending-restart", a.requireRole(auth.RoleManager, a.handlePendingRestart))
	mux.HandleFunc("GET /api/v1/system/browse", a.requireRole(auth.RoleAdmin, a.handleBrowse))
	// First-run setup wizard + restart to apply new folders.
	mux.HandleFunc("GET /api/v1/setup", a.requireRole(auth.RoleAdmin, a.handleSetupState))
	mux.HandleFunc("POST /api/v1/setup/complete", a.requireRole(auth.RoleAdmin, a.handleSetupComplete))
	mux.HandleFunc("POST /api/v1/system/restart", a.requireRole(auth.RoleAdmin, a.handleRestart))
	// Recurring tasks: staff can see how they're doing; starting one by hand (a backup, a
	// whole-library sweep) is a system action, so Run now is admin-only.
	mux.HandleFunc("GET /api/v1/system/tasks", a.requireRole(auth.RoleManager, a.handleListTasks))
	mux.HandleFunc("POST /api/v1/system/tasks/{name}/run", a.requireRole(auth.RoleAdmin, a.handleRunTask))
	// Background jobs (searches, scans, imports, Run now): staff follow and cancel them.
	mux.HandleFunc("GET /api/v1/jobs", a.requireRole(auth.RoleManager, a.handleListJobs))
	mux.HandleFunc("GET /api/v1/jobs/{id}", a.requireRole(auth.RoleManager, a.handleGetJob))
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", a.requireRole(auth.RoleManager, a.handleCancelJob))

	// Audiobook server (listening apps): admin panel + each user's own connection card.
	mux.HandleFunc("GET /api/v1/audioserver", a.requireRole(auth.RoleAdmin, a.handleAudioServer))
	mux.HandleFunc("PUT /api/v1/audioserver", a.requireRole(auth.RoleAdmin, a.handleSetAudioServer))
	mux.HandleFunc("PUT /api/v1/audioserver/users/{id}", a.requireRole(auth.RoleAdmin, a.handleSetAudioUser))
	mux.HandleFunc("DELETE /api/v1/audioserver/devices/{family}", a.requireRole(auth.RoleAdmin, a.handleRevokeAudioDevice))
	mux.HandleFunc("GET /api/v1/audioserver/listening", a.requireRole(auth.RoleAdmin, a.handleAudioListening))
	mux.HandleFunc("POST /api/v1/audioserver/import", a.requireRole(auth.RoleAdmin, a.handleAudioImportUpload))
	mux.HandleFunc("POST /api/v1/audioserver/import/apply", a.requireRole(auth.RoleAdmin, a.handleAudioImportApply))
	mux.HandleFunc("GET /api/v1/me/audio", a.signedIn(a.handleMyAudio).ext())
	mux.HandleFunc("PUT /api/v1/me/audio/password", a.signedIn(a.handleSetMyAudioPassword).ext())
	mux.HandleFunc("DELETE /api/v1/me/audio/password", a.signedIn(a.handleRemoveMyAudioPassword).ext())
	mux.HandleFunc("GET /api/v1/me/audio/listening", a.signedIn(a.handleMyAudioListening).ext())
	mux.HandleFunc("POST /api/v1/me/audio/accept", a.signedIn(a.handleMyAudioAccept).ext())
	mux.HandleFunc("DELETE /api/v1/me/audio/devices/{family}", a.signedIn(a.handleRevokeMyDevice).ext())
	mux.HandleFunc("GET /api/v1/me/audio/history", a.signedIn(a.handleMyAudioHistory).ext())
	mux.HandleFunc("POST /api/v1/me/audio/restore", a.signedIn(a.handleMyAudioRestore).ext())
	mux.HandleFunc("GET /api/v1/books/{id}/audiobook", a.signedIn(a.handleBookAudiobook).ext())

	// Auth
	mux.HandleFunc("POST /api/v1/auth/setup", a.public(a.handleSetup).ext())
	mux.HandleFunc("POST /api/v1/auth/login", a.public(a.handleLogin).ext())
	mux.HandleFunc("POST /api/v1/auth/plex/pin", a.public(a.handlePlexLoginStart).ext())
	mux.HandleFunc("GET /api/v1/auth/plex/pin/{id}", a.public(a.handlePlexLoginPoll).ext())
	mux.HandleFunc("POST /api/v1/auth/logout", a.public(a.handleLogout).ext())
	mux.HandleFunc("GET /api/v1/auth/me", a.signedIn(a.handleMe).ext())
	// Per-user notifications (in-app inbox + personal Apprise URL).
	mux.HandleFunc("GET /api/v1/me/notifications", a.signedIn(a.handleMyNotifications).ext())
	mux.HandleFunc("POST /api/v1/me/notifications/read-all", a.signedIn(a.handleMarkAllNotificationsRead).ext())
	mux.HandleFunc("POST /api/v1/me/notifications/{id}/read", a.signedIn(a.handleMarkNotificationRead).ext())
	// Web Push (PWA notifications): key + per-device subscribe/unsubscribe.
	mux.HandleFunc("GET /api/v1/me/push/key", a.signedIn(a.handlePushKey).ext())
	mux.HandleFunc("POST /api/v1/me/push/subscribe", a.signedIn(a.handlePushSubscribe).ext())
	mux.HandleFunc("POST /api/v1/me/push/unsubscribe", a.signedIn(a.handlePushUnsubscribe).ext())
	mux.HandleFunc("GET /api/v1/me/apprise", a.signedIn(a.handleGetMyApprise).ext())
	mux.HandleFunc("GET /api/v1/me/books", a.signedIn(a.handleMyBooks).ext())
	mux.HandleFunc("PUT /api/v1/me/apprise", a.signedIn(a.handleSetMyApprise).ext())

	// User management (admin only).
	mux.HandleFunc("GET /api/v1/users", a.requireRole(auth.RoleAdmin, a.handleListUsers))
	mux.HandleFunc("POST /api/v1/users", a.requireRole(auth.RoleAdmin, a.handleCreateUser))
	mux.HandleFunc("PUT /api/v1/users/{id}", a.requireRole(auth.RoleAdmin, a.handleUpdateUser))
	mux.HandleFunc("GET /api/v1/users/{id}/impact", a.requireRole(auth.RoleAdmin, a.handleUserImpact))
	mux.HandleFunc("DELETE /api/v1/users/{id}", a.requireRole(auth.RoleAdmin, a.handleDeleteUser))
	mux.HandleFunc("GET /api/v1/users/plex-blocks", a.requireRole(auth.RoleAdmin, a.handleListPlexBlocks))
	mux.HandleFunc("POST /api/v1/users/{id}/block-plex", a.requireRole(auth.RoleAdmin, a.handleBlockUserPlex))
	mux.HandleFunc("DELETE /api/v1/users/plex-blocks/{plexID}", a.requireRole(auth.RoleAdmin, a.handleUnblockPlex))

	// Realtime updates
	mux.HandleFunc("GET /api/v1/ws", a.signedIn(a.handleWS))

	// Acquisition utilities
	mux.HandleFunc("GET /api/v1/parse", a.requireRole(auth.RoleManager, a.handleParse))

	// Quality profiles + custom-format builder
	mux.HandleFunc("GET /api/v1/quality/preview", a.requireRole(auth.RoleManager, a.handleQualityPreview))
	mux.HandleFunc("POST /api/v1/quality/preview", a.requireRole(auth.RoleManager, a.handleQualityPreview))
	mux.HandleFunc("POST /api/v1/quality/test", a.requireRole(auth.RoleManager, a.handleQualityTest))
	mux.HandleFunc("POST /api/v1/quality/impact", a.requireRole(auth.RoleManager, a.handleQualityImpact))
	mux.HandleFunc("GET /api/v1/quality/profiles", a.requireRole(auth.RoleManager, a.handleListQualityProfiles))
	mux.HandleFunc("POST /api/v1/quality/profiles", a.requireRole(auth.RoleManager, a.handleCreateQualityProfile))
	mux.HandleFunc("POST /api/v1/quality/default", a.requireRole(auth.RoleManager, a.handleSetDefaultProfile))
	mux.HandleFunc("GET /api/v1/quality/profiles/{ref}", a.requireRole(auth.RoleManager, a.handleGetQualityProfile))
	mux.HandleFunc("PUT /api/v1/quality/profiles/{id}", a.requireRole(auth.RoleManager, a.handleUpdateQualityProfile))
	mux.HandleFunc("DELETE /api/v1/quality/profiles/{id}", a.requireRole(auth.RoleManager, a.handleDeleteQualityProfile))
	mux.HandleFunc("POST /api/v1/quality/profiles/{id}/hold-existing", a.requireRole(auth.RoleManager, a.handleHoldExisting))
	mux.HandleFunc("POST /api/v1/movies/{id}/resume-upgrades", a.requireRole(auth.RoleManager, a.handleResumeMovieUpgrades))
	mux.HandleFunc("POST /api/v1/series/{id}/resume-upgrades", a.requireRole(auth.RoleManager, a.handleResumeSeriesUpgrades))

	// Indexers + search
	mux.HandleFunc("GET /api/v1/indexers", a.requireRole(auth.RoleManager, a.handleListIndexers))
	mux.HandleFunc("POST /api/v1/indexers", a.requireRole(auth.RoleManager, a.handleCreateIndexer))
	mux.HandleFunc("PUT /api/v1/indexers/{id}", a.requireRole(auth.RoleManager, a.handleUpdateIndexer))
	mux.HandleFunc("DELETE /api/v1/indexers/{id}", a.requireRole(auth.RoleManager, a.handleDeleteIndexer))
	mux.HandleFunc("POST /api/v1/indexers/{id}/test", a.requireRole(auth.RoleManager, a.handleTestIndexer))
	mux.HandleFunc("GET /api/v1/flaresolverr/status", a.requireRole(auth.RoleManager, a.handleFlareSolverrStatus))

	// Download clients + queue
	mux.HandleFunc("GET /api/v1/downloadclients", a.requireRole(auth.RoleManager, a.handleListDownloadClients))
	mux.HandleFunc("POST /api/v1/downloadclients", a.requireRole(auth.RoleManager, a.handleCreateDownloadClient))
	mux.HandleFunc("PUT /api/v1/downloadclients/{id}", a.requireRole(auth.RoleManager, a.handleUpdateDownloadClient))
	mux.HandleFunc("DELETE /api/v1/downloadclients/{id}", a.requireRole(auth.RoleManager, a.handleDeleteDownloadClient))
	mux.HandleFunc("POST /api/v1/downloadclients/{id}/test", a.requireRole(auth.RoleManager, a.handleTestDownloadClient))
	mux.HandleFunc("GET /api/v1/downloadclients/{id}/status", a.requireRole(auth.RoleManager, a.handleDownloadClientStatus))
	mux.HandleFunc("GET /api/v1/downloadclients/{id}/settings", a.requireRole(auth.RoleManager, a.handleGetClientSettings))
	mux.HandleFunc("PUT /api/v1/downloadclients/{id}/settings", a.requireRole(auth.RoleManager, a.handleSetClientSettings))
	mux.HandleFunc("GET /api/v1/indexers/prowlarr", a.requireRole(auth.RoleManager, a.handleProwlarrInfo))
	mux.HandleFunc("POST /api/v1/indexers/prowlarr/sync", a.requireRole(auth.RoleManager, a.handleProwlarrSync))
	mux.HandleFunc("GET /api/v1/notifications", a.requireRole(auth.RoleManager, a.handleListNotifications))
	mux.HandleFunc("POST /api/v1/notifications", a.requireRole(auth.RoleManager, a.handleCreateNotification))
	mux.HandleFunc("PUT /api/v1/notifications/{id}", a.requireRole(auth.RoleManager, a.handleUpdateNotification))
	mux.HandleFunc("DELETE /api/v1/notifications/{id}", a.requireRole(auth.RoleManager, a.handleDeleteNotification))
	mux.HandleFunc("POST /api/v1/notifications/test", a.requireRole(auth.RoleManager, a.handleTestNotification))
	mux.HandleFunc("GET /api/v1/queue", a.requireRole(auth.RoleManager, a.handleQueue))
	mux.HandleFunc("GET /api/v1/downloads/disk-guard", a.requireRole(auth.RoleManager, a.handleDiskGuardStatus))
	mux.HandleFunc("GET /api/v1/files/info", a.requireRole(auth.RoleManager, a.handleFileInfo))
	mux.HandleFunc("GET /api/v1/downloads", a.requireRole(auth.RoleManager, a.handleDownloadsFeed))
	mux.HandleFunc("POST /api/v1/queue/{hash}/pause", a.requireRole(auth.RoleManager, a.handlePauseDownload))
	mux.HandleFunc("POST /api/v1/queue/{hash}/resume", a.requireRole(auth.RoleManager, a.handleResumeDownload))
	mux.HandleFunc("POST /api/v1/queue/{hash}/block", a.requireRole(auth.RoleManager, a.handleBlockDownload))
	mux.HandleFunc("POST /api/v1/queue/{hash}/action", a.requireRole(auth.RoleManager, a.handleTorrentAction))
	mux.HandleFunc("DELETE /api/v1/queue/{hash}", a.requireRole(auth.RoleManager, a.handleDeleteDownload))

	// Grab: search result → download client (closes the acquisition loop).
	mux.HandleFunc("POST /api/v1/grab", a.requireRole(auth.RoleManager, a.handleGrab))
	mux.HandleFunc("POST /api/v1/grab/preview", a.requireRole(auth.RoleManager, a.handleGrabPreview))
	mux.HandleFunc("POST /api/v1/movies/{id}/grabtorrent", a.requireRole(auth.RoleManager, a.handleMovieGrabTorrent))
	mux.HandleFunc("POST /api/v1/series/{id}/grabtorrent", a.requireRole(auth.RoleManager, a.handleSeriesGrabTorrent))
	mux.HandleFunc("POST /api/v1/books/{id}/grabtorrent", a.requireRole(auth.RoleManager, a.handleBookGrabTorrent))
	mux.HandleFunc("GET /api/v1/books/{id}/series", a.requireRole(auth.RoleManager, a.handleBookSeries))
	mux.HandleFunc("POST /api/v1/books/series-backfill", a.requireRole(auth.RoleManager, a.handleBackfillBookSeries))

	// Import history
	mux.HandleFunc("GET /api/v1/history", a.requireRole(auth.RoleManager, a.handleHistory))

	// Import review — downloads held because their content didn't match what they
	// were grabbed for (admin-only).
	mux.HandleFunc("GET /api/v1/reviews", a.requireRole(auth.RoleManager, a.handleListReviews))
	mux.HandleFunc("POST /api/v1/reviews/{id}/reject", a.requireRole(auth.RoleManager, a.handleRejectReview))
	mux.HandleFunc("POST /api/v1/reviews/{id}/dismiss", a.requireRole(auth.RoleManager, a.handleDismissReview))
	mux.HandleFunc("POST /api/v1/reviews/{id}/import", a.requireRole(auth.RoleManager, a.handleImportReview))
	mux.HandleFunc("GET /api/v1/reviews/{id}/targets", a.requireRole(auth.RoleManager, a.handleReviewTargets))
	mux.HandleFunc("GET /api/v1/reviews/{id}/files", a.requireRole(auth.RoleManager, a.handleReviewFiles))
	mux.HandleFunc("POST /api/v1/reviews/{id}/retry", a.requireRole(auth.RoleManager, a.handleRetryReview))
	mux.HandleFunc("POST /api/v1/reviews/{id}/map", a.requireRole(auth.RoleManager, a.handleMapReview))
	mux.HandleFunc("POST /api/v1/reviews/bulk", a.requireRole(auth.RoleManager, a.handleBulkReviews))

	// The blocklist across every media type, global entries included.
	mux.HandleFunc("GET /api/v1/blocklist", a.requireRole(auth.RoleManager, a.handleListAllBlocks))
	mux.HandleFunc("DELETE /api/v1/blocklist/{id}", a.requireRole(auth.RoleManager, a.handleUnblockAny))

	// Movies
	mux.HandleFunc("GET /api/v1/movies", a.requireRole(auth.RoleManager, a.handleListMovies))
	// How each library file fits its profile's ideal file (report-only; from the Convert index).
	mux.HandleFunc("GET /api/v1/library/fit", a.requireRole(auth.RoleManager, a.handleLibraryFit))
	mux.HandleFunc("GET /api/v1/library/fit/profiles", a.requireRole(auth.RoleManager, a.handleLibraryFitProfiles))
	mux.HandleFunc("POST /api/v1/library/fit/preview", a.requireRole(auth.RoleManager, a.handleLibraryFitPreview))
	mux.HandleFunc("GET /api/v1/movies/lookup", a.requireRole(auth.RoleManager, a.handleLookupMovies))
	mux.HandleFunc("POST /api/v1/movies/scan", a.requireRole(auth.RoleManager, a.handleScanLibrary))
	mux.HandleFunc("GET /api/v1/movies/unmatched", a.requireRole(auth.RoleManager, a.handleMovieUnmatched))
	mux.HandleFunc("POST /api/v1/movies/import", a.requireRole(auth.RoleManager, a.handleMovieImportFolder))
	mux.HandleFunc("GET /api/v1/movies/{id}", a.requireRole(auth.RoleManager, a.handleGetMovie))
	mux.HandleFunc("POST /api/v1/movies", a.requireRole(auth.RoleManager, a.handleAddMovie))
	mux.HandleFunc("POST /api/v1/movies/{id}/search", a.requireRole(auth.RoleManager, a.handleSearchMovie))
	mux.HandleFunc("GET /api/v1/movies/{id}/releases", a.requireRole(auth.RoleManager, a.handleMovieReleases))
	mux.HandleFunc("GET /api/v1/movies/{id}/history", a.requireRole(auth.RoleManager, a.handleMovieHistory))
	mux.HandleFunc("GET /api/v1/movies/{id}/collection", a.requireRole(auth.RoleManager, a.handleMovieCollection))

	// Series (TV).
	mux.HandleFunc("GET /api/v1/series", a.requireRole(auth.RoleManager, a.handleListSeries))
	mux.HandleFunc("GET /api/v1/series/lookup", a.requireRole(auth.RoleManager, a.handleLookupSeries))
	mux.HandleFunc("POST /api/v1/series/scan", a.requireRole(auth.RoleManager, a.handleScanSeriesLibrary))
	mux.HandleFunc("GET /api/v1/series/unmatched", a.requireRole(auth.RoleManager, a.handleSeriesUnmatched))
	mux.HandleFunc("POST /api/v1/series/import", a.requireRole(auth.RoleManager, a.handleSeriesImportFolder))
	mux.HandleFunc("POST /api/v1/series", a.requireRole(auth.RoleManager, a.handleAddSeries))
	mux.HandleFunc("GET /api/v1/series/{id}/history", a.requireRole(auth.RoleManager, a.handleSeriesHistory))
	mux.HandleFunc("POST /api/v1/series/{id}/search", a.requireRole(auth.RoleManager, a.handleSearchSeries))
	mux.HandleFunc("GET /api/v1/series/{id}/releases", a.requireRole(auth.RoleManager, a.handleSeriesReleases))
	mux.HandleFunc("POST /api/v1/series/{id}/grab", a.requireRole(auth.RoleManager, a.handleGrabSeries))
	mux.HandleFunc("POST /api/v1/series/{id}/autograb", a.requireRole(auth.RoleManager, a.handleAutoGrabSeries))
	mux.HandleFunc("POST /api/v1/series/refresh", a.requireRole(auth.RoleManager, a.handleRefreshAllSeries))
	mux.HandleFunc("POST /api/v1/series/{id}/refresh", a.requireRole(auth.RoleManager, a.handleRefreshSeries))
	mux.HandleFunc("GET /api/v1/series/{id}/manualimport", a.requireRole(auth.RoleManager, a.handleSeriesManualImportList))
	mux.HandleFunc("POST /api/v1/series/{id}/manualimport", a.requireRole(auth.RoleManager, a.handleSeriesManualImport))
	mux.HandleFunc("GET /api/v1/series/{id}/duplicates", a.requireRole(auth.RoleManager, a.handleSeriesDuplicates))
	mux.HandleFunc("DELETE /api/v1/series/{id}/duplicates", a.requireRole(auth.RoleManager, a.handleDeleteSeriesDuplicate))
	mux.HandleFunc("GET /api/v1/series/{id}/numbering", a.requireRole(auth.RoleManager, a.handleSeriesNumbering))
	mux.HandleFunc("POST /api/v1/series/{id}/numbering/apply", a.requireRole(auth.RoleManager, a.handleApplySeriesNumbering))
	mux.HandleFunc("DELETE /api/v1/series/{id}/numbering/pending", a.requireRole(auth.RoleManager, a.handleDismissSeriesNumbering))
	mux.HandleFunc("GET /api/v1/series/{id}/rename", a.requireRole(auth.RoleManager, a.handleSeriesRenamePreview))
	mux.HandleFunc("POST /api/v1/series/{id}/rename", a.requireRole(auth.RoleManager, a.handleSeriesRename))
	mux.HandleFunc("GET /api/v1/series/{id}", a.requireRole(auth.RoleManager, a.handleGetSeries))
	mux.HandleFunc("PUT /api/v1/series/{id}/monitor", a.requireRole(auth.RoleManager, a.handleSetSeriesMonitored))
	mux.HandleFunc("PUT /api/v1/series/{id}/profile", a.requireRole(auth.RoleManager, a.handleSetSeriesProfile))
	mux.HandleFunc("PUT /api/v1/series/{id}/type", a.requireRole(auth.RoleManager, a.handleSetSeriesType))
	// Manual scene-season mapping (anime whose cours don't match TMDB numbering).
	mux.HandleFunc("GET /api/v1/series/{id}/scene-map", a.requireRole(auth.RoleManager, a.handleListSceneOverrides))
	mux.HandleFunc("PUT /api/v1/series/{id}/scene-map", a.requireRole(auth.RoleManager, a.handleSetSceneOverride))
	mux.HandleFunc("DELETE /api/v1/series/{id}/scene-map/{season}", a.requireRole(auth.RoleManager, a.handleDeleteSceneOverride))
	mux.HandleFunc("GET /api/v1/series/{id}/aliases", a.requireRole(auth.RoleManager, a.handleListAliases))
	mux.HandleFunc("POST /api/v1/series/{id}/aliases", a.requireRole(auth.RoleManager, a.handleAddAlias))
	mux.HandleFunc("DELETE /api/v1/series/{id}/aliases/{alias}", a.requireRole(auth.RoleManager, a.handleDeleteAlias))
	mux.HandleFunc("PUT /api/v1/series/{id}/seasons/{season}/monitor", a.requireRole(auth.RoleManager, a.handleSetSeasonMonitored))
	mux.HandleFunc("PUT /api/v1/series/episodes/{eid}/monitor", a.requireRole(auth.RoleManager, a.handleSetEpisodeMonitored))
	mux.HandleFunc("GET /api/v1/series/{id}/blocklist", a.requireRole(auth.RoleManager, a.handleSeriesBlocklist))
	mux.HandleFunc("POST /api/v1/series/{id}/blocklist", a.requireRole(auth.RoleManager, a.handleSeriesBlock))
	mux.HandleFunc("DELETE /api/v1/series/{id}/blocklist/{bid}", a.requireRole(auth.RoleManager, a.handleSeriesUnblock))
	mux.HandleFunc("POST /api/v1/series/{id}/seasons/{season}/episodes/{episode}/regrab", a.requireRole(auth.RoleManager, a.handleRegrabEpisode))
	mux.HandleFunc("DELETE /api/v1/series/{id}/seasons/{season}/episodes/{episode}/file", a.requireRole(auth.RoleManager, a.handleDeleteEpisodeFile))
	mux.HandleFunc("GET /api/v1/series/{id}/delete-preview", a.requireRole(auth.RoleManager, a.handleSeriesDeletePreview))
	mux.HandleFunc("DELETE /api/v1/series/{id}", a.requireRole(auth.RoleManager, a.handleDeleteSeries))

	// Requests (Overseerr-style): request media → approve → add to Movies/Series.
	mux.HandleFunc("GET /api/v1/requests", a.signedIn(a.handleListRequests).ext())
	mux.HandleFunc("POST /api/v1/requests", a.requireRole(auth.RoleRequester, a.handleCreateRequest).ext())
	mux.HandleFunc("POST /api/v1/requests/{id}/approve", a.requireRole(auth.RoleManager, a.handleApproveRequest).ext())
	mux.HandleFunc("POST /api/v1/requests/{id}/decline", a.requireRole(auth.RoleManager, a.handleDeclineRequest).ext())
	// Owner-withdraw is allowed (own request, still pending), so the route admits
	// requesters; the handler enforces ownership vs manager.
	mux.HandleFunc("DELETE /api/v1/requests/{id}", a.requireRole(auth.RoleRequester, a.handleDeleteRequest).ext())
	mux.HandleFunc("POST /api/v1/requests/import/overseerr", a.requireRole(auth.RoleAdmin, a.handleImportOverseerr).ext())
	mux.HandleFunc("POST /api/v1/insights/import/tautulli", a.requireRole(auth.RoleAdmin, a.handleImportTautulli))

	// Convert (Tdarr replacement — GPU transcoding/cleanup over the Movies/Series catalogs).
	// The log carries whatever any module writes (paths, client names, sign-in lines), so
	// it's the admin's to read.
	mux.HandleFunc("GET /api/v1/logs", a.requireRole(auth.RoleAdmin, a.handleLogs))
	mux.HandleFunc("GET /api/v1/recycle", a.requireRole(auth.RoleManager, a.handleRecycleStats))
	mux.HandleFunc("GET /api/v1/recycle/mode", a.requireRole(auth.RoleManager, a.handleRecycleMode))
	mux.HandleFunc("GET /api/v1/recycle/items", a.requireRole(auth.RoleManager, a.handleRecycleItems))
	// Emptying the bin, or purging one item from it, is the last step before a file is
	// gone for good: admin only. Restoring stays with managers.
	mux.HandleFunc("POST /api/v1/recycle/empty", a.requireRole(auth.RoleAdmin, a.handleRecycleEmpty))
	mux.HandleFunc("POST /api/v1/recycle/restore", a.requireRole(auth.RoleManager, a.handleRecycleRestore))
	mux.HandleFunc("POST /api/v1/recycle/delete", a.requireRole(auth.RoleAdmin, a.handleRecycleDeleteItem))
	// Admin only: a backup holds API keys, the Plex token and password hashes.
	mux.HandleFunc("GET /api/v1/system/backups", a.requireRole(auth.RoleAdmin, a.handleBackupsList))
	mux.HandleFunc("POST /api/v1/system/backups", a.requireRole(auth.RoleAdmin, a.handleBackupNow))
	mux.HandleFunc("PUT /api/v1/system/backups/settings", a.requireRole(auth.RoleAdmin, a.handleBackupSettings))
	mux.HandleFunc("GET /api/v1/system/backups/{name}/download", a.requireRole(auth.RoleAdmin, a.handleBackupDownload))
	mux.HandleFunc("DELETE /api/v1/system/backups/{name}", a.requireRole(auth.RoleAdmin, a.handleBackupDelete))
	mux.HandleFunc("POST /api/v1/system/backups/{name}/restore", a.requireRole(auth.RoleAdmin, a.handleBackupRestore))
	mux.HandleFunc("DELETE /api/v1/system/backups/restore-pending", a.requireRole(auth.RoleAdmin, a.handleBackupRestoreCancel))
	mux.HandleFunc("POST /api/v1/system/backups/upload", a.requireRole(auth.RoleAdmin, a.handleBackupUpload))
	mux.HandleFunc("GET /api/v1/convert/hardware", a.requireRole(auth.RoleManager, a.handleConvertHardware))
	mux.HandleFunc("GET /api/v1/convert/status", a.requireRole(auth.RoleManager, a.handleConvertStatus))
	mux.HandleFunc("GET /api/v1/convert/settings", a.requireRole(auth.RoleManager, a.handleConvertSettings))
	mux.HandleFunc("PUT /api/v1/convert/settings", a.requireRole(auth.RoleManager, a.handleConvertSettingsUpdate))
	mux.HandleFunc("GET /api/v1/convert/library", a.requireRole(auth.RoleManager, a.handleConvertLibrary))
	mux.HandleFunc("GET /api/v1/convert/stats", a.requireRole(auth.RoleManager, a.handleConvertStats))
	mux.HandleFunc("GET /api/v1/convert/jobs", a.requireRole(auth.RoleManager, a.handleConvertJobs))
	mux.HandleFunc("GET /api/v1/convert/logs", a.requireRole(auth.RoleManager, a.handleConvertLogs))
	mux.HandleFunc("GET /api/v1/convert/history", a.requireRole(auth.RoleManager, a.handleConvertHistory))
	mux.HandleFunc("GET /api/v1/convert/history/{id}", a.requireRole(auth.RoleManager, a.handleConvertHistoryEntry))
	mux.HandleFunc("POST /api/v1/convert/reindex", a.requireRole(auth.RoleManager, a.handleConvertReindex))
	mux.HandleFunc("GET /api/v1/convert/reindex", a.requireRole(auth.RoleManager, a.handleConvertReindexStatus))
	mux.HandleFunc("POST /api/v1/convert/requests", a.requireRole(auth.RoleManager, a.handleConvertRequest))
	mux.HandleFunc("DELETE /api/v1/convert/requests", a.requireRole(auth.RoleManager, a.handleConvertRequestCancel))
	mux.HandleFunc("POST /api/v1/convert/series/{series}", a.requireRole(auth.RoleManager, a.handleConvertSeries))
	mux.HandleFunc("POST /api/v1/convert/jobs/{id}/cancel", a.requireRole(auth.RoleManager, a.handleConvertCancel))
	mux.HandleFunc("POST /api/v1/convert/compare", a.requireRole(auth.RoleManager, a.handleConvertCompare))
	mux.HandleFunc("GET /api/v1/convert/compare", a.requireRole(auth.RoleManager, a.handleConvertCompareStatus))
	mux.HandleFunc("GET /api/v1/convert/compare/files/{name}", a.requireRole(auth.RoleManager, a.handleConvertCompareFile))
	mux.HandleFunc("GET /api/v1/convert/blocklist", a.requireRole(auth.RoleManager, a.handleConvertBlocklist))
	mux.HandleFunc("GET /api/v1/convert/skips", a.requireRole(auth.RoleManager, a.handleConvertSkips))
	mux.HandleFunc("POST /api/v1/convert/skips/clear", a.requireRole(auth.RoleManager, a.handleConvertSkipsClear))
	mux.HandleFunc("POST /api/v1/convert/blocklist/clear", a.requireRole(auth.RoleManager, a.handleConvertBlocklistClear))

	// Insights (Tautulli replacement — Plex watch monitoring). I0: connection config + test.
	mux.HandleFunc("GET /api/v1/insights/plex", a.requireRole(auth.RoleManager, a.handleInsightsConfig))
	mux.HandleFunc("PUT /api/v1/insights/plex", a.requireRole(auth.RoleManager, a.handleUpdateInsightsConfig))
	mux.HandleFunc("POST /api/v1/insights/plex/test", a.requireRole(auth.RoleManager, a.handleInsightsTest))
	mux.HandleFunc("POST /api/v1/insights/plex/auth", a.requireRole(auth.RoleManager, a.handleInsightsPlexAuthStart))
	mux.HandleFunc("GET /api/v1/insights/plex/auth/{id}", a.requireRole(auth.RoleManager, a.handleInsightsPlexAuthPoll))
	mux.HandleFunc("GET /api/v1/insights/activity", a.requireRole(auth.RoleManager, a.handleInsightsActivity))
	mux.HandleFunc("GET /api/v1/insights/history", a.requireRole(auth.RoleManager, a.handleInsightsHistory))
	mux.HandleFunc("GET /api/v1/insights/stats", a.requireRole(auth.RoleManager, a.handleInsightsStats))
	mux.HandleFunc("GET /api/v1/insights/graphs", a.requireRole(auth.RoleManager, a.handleInsightsGraphs))
	mux.HandleFunc("GET /api/v1/insights/reliability", a.requireRole(auth.RoleManager, a.handleInsightsReliability))
	mux.HandleFunc("GET /api/v1/insights/users", a.requireRole(auth.RoleManager, a.handleInsightsUsers))
	mux.HandleFunc("GET /api/v1/insights/libraries", a.requireRole(auth.RoleManager, a.handleInsightsLibraries))
	mux.HandleFunc("GET /api/v1/insights/recently-added", a.requireRole(auth.RoleManager, a.handleInsightsRecentlyAdded))
	mux.HandleFunc("GET /api/v1/insights/image", a.requireRole(auth.RoleManager, a.handleInsightsImage))

	// Subtitles (Bazarr replacement — external SRT sidecars over the Movies/Series catalogs).
	mux.HandleFunc("GET /api/v1/subtitles/library", a.requireRole(auth.RoleManager, a.handleSubtitleLibrary))
	mux.HandleFunc("GET /api/v1/subtitles/coverage", a.requireRole(auth.RoleManager, a.handleSubtitleCoverage))
	mux.HandleFunc("POST /api/v1/subtitles/library/rescan", a.requireRole(auth.RoleManager, a.handleSubtitleRescan))
	mux.HandleFunc("GET /api/v1/subtitles/models", a.requireRole(auth.RoleManager, a.handleSubtitleModels))
	mux.HandleFunc("POST /api/v1/subtitles/models/{name}", a.requireRole(auth.RoleManager, a.handleSubtitleDownloadModel))
	mux.HandleFunc("GET /api/v1/subtitles/jobs", a.requireRole(auth.RoleManager, a.handleSubtitleJobs))
	mux.HandleFunc("POST /api/v1/subtitles/jobs/clear", a.requireRole(auth.RoleManager, a.handleSubtitleClearQueue))
	mux.HandleFunc("POST /api/v1/subtitles/jobs/{id}/cancel", a.requireRole(auth.RoleManager, a.handleSubtitleCancelJob))
	mux.HandleFunc("GET /api/v1/subtitles/logs", a.requireRole(auth.RoleManager, a.handleSubtitleLogs))
	mux.HandleFunc("POST /api/v1/subtitles/sweep", a.requireRole(auth.RoleManager, a.handleSubtitleSweep))
	mux.HandleFunc("POST /api/v1/subtitles/library/movies/{id}", a.requireRole(auth.RoleManager, a.handleSubtitleQueueMovie))
	mux.HandleFunc("POST /api/v1/subtitles/library/episodes/{series}/{season}/{episode}", a.requireRole(auth.RoleManager, a.handleSubtitleQueueEpisode))
	mux.HandleFunc("POST /api/v1/subtitles/library/series/{id}", a.requireRole(auth.RoleManager, a.handleSubtitleQueueSeries))
	mux.HandleFunc("GET /api/v1/subtitles/settings", a.requireRole(auth.RoleManager, a.handleGetSubtitleSettings))
	mux.HandleFunc("PUT /api/v1/subtitles/settings", a.requireRole(auth.RoleManager, a.handleUpdateSubtitleSettings))
	mux.HandleFunc("GET /api/v1/subtitles/movies", a.requireRole(auth.RoleManager, a.handleSubtitleMovies))
	mux.HandleFunc("GET /api/v1/subtitles/series", a.requireRole(auth.RoleManager, a.handleSubtitleSeries))
	mux.HandleFunc("POST /api/v1/subtitles/movies/{id}/search", a.requireRole(auth.RoleManager, a.handleSubtitleSearchMovie))
	mux.HandleFunc("POST /api/v1/subtitles/series/{id}/search", a.requireRole(auth.RoleManager, a.handleSubtitleSearchSeries))

	// Books (Readarr replacement — Open Library metadata + ebook acquisition).
	mux.HandleFunc("GET /api/v1/books", a.requireRole(auth.RoleManager, a.handleListBooks))
	mux.HandleFunc("GET /api/v1/books/lookup", a.requireRole(auth.RoleManager, a.handleLookupBooks))
	mux.HandleFunc("POST /api/v1/books/upgrade", a.requireRole(auth.RoleManager, a.handleStartBookUpgrade))
	mux.HandleFunc("GET /api/v1/books/upgrade", a.requireRole(auth.RoleManager, a.handleBookUpgradeStatus))
	mux.HandleFunc("POST /api/v1/books/scan", a.requireRole(auth.RoleManager, a.handleScanBookLibrary))
	mux.HandleFunc("POST /api/v1/books/search-missing", a.requireRole(auth.RoleManager, a.handleStartBookSweep))
	mux.HandleFunc("GET /api/v1/books/search-missing", a.requireRole(auth.RoleManager, a.handleBookSweepStatus))
	mux.HandleFunc("POST /api/v1/books/author", a.requireRole(auth.RoleManager, a.handleAddAuthor))
	mux.HandleFunc("POST /api/v1/books", a.requireRole(auth.RoleManager, a.handleAddBook))
	mux.HandleFunc("POST /api/v1/books/{id}/search", a.requireRole(auth.RoleManager, a.handleSearchBook))
	mux.HandleFunc("POST /api/v1/books/{id}/refresh", a.requireRole(auth.RoleManager, a.handleRefreshBook))
	mux.HandleFunc("GET /api/v1/books/{id}/releases", a.requireRole(auth.RoleManager, a.handleBookReleases))
	mux.HandleFunc("POST /api/v1/books/{id}/grab", a.requireRole(auth.RoleManager, a.handleGrabBook))
	mux.HandleFunc("GET /api/v1/books/{id}/manualimport", a.requireRole(auth.RoleManager, a.handleBookManualImportList))
	mux.HandleFunc("POST /api/v1/books/{id}/manualimport", a.requireRole(auth.RoleManager, a.handleBookManualImport))
	mux.HandleFunc("POST /api/v1/books/{id}/rename", a.requireRole(auth.RoleManager, a.handleBookRename))
	mux.HandleFunc("GET /api/v1/books/{id}/history", a.requireRole(auth.RoleManager, a.handleBookHistory))
	mux.HandleFunc("POST /api/v1/books/{id}/rematch", a.requireRole(auth.RoleManager, a.handleRematchBook))
	mux.HandleFunc("GET /api/v1/books/{id}/edition-files", a.requireRole(auth.RoleManager, a.handleBookEditionFiles))
	mux.HandleFunc("GET /api/v1/books/{id}/ebook", a.signedIn(a.handleBookEbook).ext())
	mux.HandleFunc("POST /api/v1/books/{id}/audio-versions", a.requireRole(auth.RoleManager, a.handleAddAudioVersion))
	mux.HandleFunc("PUT /api/v1/books/{id}/audio-versions/{vid}", a.requireRole(auth.RoleManager, a.handleUpdateAudioVersion))
	mux.HandleFunc("DELETE /api/v1/books/{id}/audio-versions/{vid}", a.requireRole(auth.RoleManager, a.handleDeleteAudioVersion))
	mux.HandleFunc("DELETE /api/v1/books/{id}/audio-versions/{vid}/file", a.requireRole(auth.RoleManager, a.handleDeleteAudioVersionFile))
	mux.HandleFunc("POST /api/v1/books/{id}/audio-versions/{vid}/search", a.requireRole(auth.RoleManager, a.handleSearchAudioVersion))
	mux.HandleFunc("POST /api/v1/books/{id}/merge-audiobook", a.requireRole(auth.RoleManager, a.handleMergeAudiobook))
	// Music (Lidarr replacement - MusicBrainz metadata + album acquisition). A preview that's
	// off by default: musicRoute 404s every endpoint while it's switched off. It sits inside
	// the auth wrapper so an anonymous caller still gets 401 and learns nothing about it.
	mux.HandleFunc("GET /api/v1/music/artists", a.requireRole(auth.RoleManager, a.musicRoute(a.handleListArtists)))
	mux.HandleFunc("GET /api/v1/music/lookup", a.requireRole(auth.RoleManager, a.musicRoute(a.handleLookupArtists)))
	mux.HandleFunc("POST /api/v1/music/scan", a.requireRole(auth.RoleManager, a.musicRoute(a.handleScanMusicLibrary)))
	mux.HandleFunc("POST /api/v1/music/artists", a.requireRole(auth.RoleManager, a.musicRoute(a.handleAddArtist)))
	mux.HandleFunc("GET /api/v1/music/artists/{id}", a.requireRole(auth.RoleManager, a.musicRoute(a.handleGetArtist)))
	mux.HandleFunc("POST /api/v1/music/artists/{id}/refresh", a.requireRole(auth.RoleManager, a.musicRoute(a.handleRefreshArtist)))
	mux.HandleFunc("POST /api/v1/music/artists/{id}/discography", a.requireRole(auth.RoleManager, a.musicRoute(a.handleGrabDiscography)))
	mux.HandleFunc("PUT /api/v1/music/artists/{id}/monitor", a.requireRole(auth.RoleManager, a.musicRoute(a.handleSetArtistMonitored)))
	mux.HandleFunc("PUT /api/v1/music/artists/{id}/profile", a.requireRole(auth.RoleManager, a.musicRoute(a.handleSetArtistProfile)))
	mux.HandleFunc("DELETE /api/v1/music/artists/{id}", a.requireRole(auth.RoleManager, a.musicRoute(a.handleDeleteArtist)))
	mux.HandleFunc("GET /api/v1/music/artists/{id}/history", a.requireRole(auth.RoleManager, a.musicRoute(a.handleArtistHistory)))
	mux.HandleFunc("GET /api/v1/music/albums/{id}", a.requireRole(auth.RoleManager, a.musicRoute(a.handleGetAlbum)))
	mux.HandleFunc("PUT /api/v1/music/albums/{id}/monitor", a.requireRole(auth.RoleManager, a.musicRoute(a.handleSetAlbumMonitored)))

	// Books Discover (Open Library browse/search + author catalogues).
	mux.HandleFunc("GET /api/v1/books/discover/trending", a.signedIn(a.handleBookDiscoverTrending).ext())
	mux.HandleFunc("GET /api/v1/books/discover/browse/{kind}", a.signedIn(a.handleBookDiscoverBrowse).ext())
	mux.HandleFunc("GET /api/v1/books/discover/recommended", a.signedIn(a.handleBookDiscoverRecommended).ext())
	mux.HandleFunc("GET /api/v1/books/discover/search", a.signedIn(a.handleBookDiscoverSearch).ext())
	mux.HandleFunc("GET /api/v1/books/discover/authors", a.signedIn(a.handleBookAuthorSearch).ext())
	mux.HandleFunc("GET /api/v1/books/discover/authors/{key}/works", a.signedIn(a.handleBookAuthorWorks).ext())
	mux.HandleFunc("GET /api/v1/books/discover/authors/{key}", a.signedIn(a.handleBookAuthorDetail).ext())
	mux.HandleFunc("GET /api/v1/books/discover/similar", a.signedIn(a.handleBookDiscoverSimilar).ext())
	mux.HandleFunc("GET /api/v1/books/authors/images", a.requireRole(auth.RoleManager, a.handleBookAuthorImages))
	mux.HandleFunc("POST /api/v1/books/{id}/series/add-missing", a.requireRole(auth.RoleManager, a.handleAddMissingInSeries))
	mux.HandleFunc("GET /api/v1/books/discover/subjects/{name}", a.signedIn(a.handleBookDiscoverSubject).ext())
	mux.HandleFunc("GET /api/v1/books/discover/detail", a.signedIn(a.handleBookDiscoverDetail).ext())
	mux.HandleFunc("GET /api/v1/books/{id}/covers", a.requireRole(auth.RoleManager, a.handleBookCovers))
	// Off-LAN too: My Books shows uploaded covers away from home, and the handler serves
	// nothing but the cover file.
	mux.HandleFunc("GET /api/v1/books/{id}/cover-image", a.signedIn(a.handleBookCoverImage).ext())
	mux.HandleFunc("PUT /api/v1/books/{id}/cover", a.requireRole(auth.RoleManager, a.handleSetBookCover))
	mux.HandleFunc("POST /api/v1/books/{id}/cover", a.requireRole(auth.RoleManager, a.handleUploadBookCover))
	mux.HandleFunc("GET /api/v1/books/{id}", a.requireRole(auth.RoleManager, a.handleGetBook))
	mux.HandleFunc("PUT /api/v1/books/{id}/monitor", a.requireRole(auth.RoleManager, a.handleSetBookMonitored))
	mux.HandleFunc("PUT /api/v1/books/{id}/keep-catalogue", a.requireRole(auth.RoleManager, a.handleSetBookKeepCatalogue))
	mux.HandleFunc("PUT /api/v1/books/{id}/profile", a.requireRole(auth.RoleManager, a.handleSetBookProfile))
	mux.HandleFunc("PUT /api/v1/books/{id}/metadata", a.requireRole(auth.RoleManager, a.handleOverrideBookMetadata))
	mux.HandleFunc("DELETE /api/v1/books/{id}/file", a.requireRole(auth.RoleManager, a.handleDeleteBookFile))
	mux.HandleFunc("DELETE /api/v1/books/{id}", a.requireRole(auth.RoleManager, a.handleDeleteBook))

	// Discover (browse trending/popular/upcoming/by-genre; enriched with library status).
	mux.HandleFunc("GET /api/v1/calendar", a.signedIn(a.handleCalendar))
	mux.HandleFunc("GET /api/v1/discover/trending", a.signedIn(a.handleDiscoverTrending).ext())
	mux.HandleFunc("GET /api/v1/discover/popular", a.signedIn(a.handleDiscoverPopular).ext())
	mux.HandleFunc("GET /api/v1/discover/upcoming", a.signedIn(a.handleDiscoverUpcoming).ext())
	mux.HandleFunc("GET /api/v1/discover/recommended", a.signedIn(a.handleDiscoverRecommended).ext())
	mux.HandleFunc("GET /api/v1/discover/search", a.signedIn(a.handleDiscoverSearch).ext())
	mux.HandleFunc("GET /api/v1/discover/genres", a.signedIn(a.handleDiscoverGenres).ext())
	mux.HandleFunc("GET /api/v1/discover/rows/{kind}", a.signedIn(a.handleDiscoverRow).ext())
	mux.HandleFunc("GET /api/v1/discover/providers", a.signedIn(a.handleDiscoverProviders).ext())
	mux.HandleFunc("GET /api/v1/discover/provider", a.signedIn(a.handleDiscoverProviderNew).ext())
	mux.HandleFunc("GET /api/v1/discover/because", a.signedIn(a.handleDiscoverBecause).ext())
	mux.HandleFunc("GET /api/v1/discover/collections", a.signedIn(a.handleDiscoverCollections).ext())
	mux.HandleFunc("GET /api/v1/discover", a.signedIn(a.handleDiscoverByGenre).ext())
	mux.HandleFunc("GET /api/v1/media/{media}/{id}", a.signedIn(a.handleMediaDetail).ext())
	mux.HandleFunc("GET /api/v1/movies/{id}/blocklist", a.requireRole(auth.RoleManager, a.handleListBlocklist))
	mux.HandleFunc("POST /api/v1/movies/{id}/blocklist", a.requireRole(auth.RoleManager, a.handleBlocklist))
	mux.HandleFunc("DELETE /api/v1/movies/{id}/blocklist/{bid}", a.requireRole(auth.RoleManager, a.handleUnblock))
	mux.HandleFunc("POST /api/v1/movies/{id}/refresh", a.requireRole(auth.RoleManager, a.handleRefreshMovie))
	mux.HandleFunc("PUT /api/v1/movies/{id}/monitor", a.requireRole(auth.RoleManager, a.handleSetMonitored))
	mux.HandleFunc("PUT /api/v1/movies/{id}/profile", a.requireRole(auth.RoleManager, a.handleSetProfile))
	mux.HandleFunc("POST /api/v1/movies/{id}/regrab", a.requireRole(auth.RoleManager, a.handleRegrab))
	mux.HandleFunc("PUT /api/v1/movies/{id}/availability", a.requireRole(auth.RoleManager, a.handleSetAvailability))
	mux.HandleFunc("GET /api/v1/movies/{id}/manualimport", a.requireRole(auth.RoleManager, a.handleManualImportList))
	mux.HandleFunc("POST /api/v1/movies/{id}/manualimport", a.requireRole(auth.RoleManager, a.handleManualImport))
	mux.HandleFunc("GET /api/v1/movies/{id}/rename", a.requireRole(auth.RoleManager, a.handleRenamePreview))
	mux.HandleFunc("POST /api/v1/movies/{id}/rename", a.requireRole(auth.RoleManager, a.handleRename))
	// Multi-version tracks (opt-in)
	mux.HandleFunc("GET /api/v1/movies/{id}/versions", a.requireRole(auth.RoleManager, a.handleListVersions))
	mux.HandleFunc("POST /api/v1/movies/{id}/versions", a.requireRole(auth.RoleManager, a.handleAddVersion))
	mux.HandleFunc("PUT /api/v1/movies/{id}/versions/{vid}", a.requireRole(auth.RoleManager, a.handleUpdateVersion))
	mux.HandleFunc("DELETE /api/v1/movies/{id}/versions/{vid}/file", a.requireRole(auth.RoleManager, a.handleDeleteVersionFile))
	mux.HandleFunc("DELETE /api/v1/movies/{id}/versions/{vid}", a.requireRole(auth.RoleManager, a.handleDeleteVersion))
	mux.HandleFunc("DELETE /api/v1/movies/{id}/file", a.requireRole(auth.RoleManager, a.handleDeleteMovieFile))
	mux.HandleFunc("GET /api/v1/movies/{id}/delete-preview", a.requireRole(auth.RoleManager, a.handleMovieDeletePreview))
	mux.HandleFunc("DELETE /api/v1/movies/{id}", a.requireRole(auth.RoleManager, a.handleDeleteMovie))

	ui := webui.Handler()
	mux.HandleFunc("/", a.public(a.spa(ui)).ext())
}

// module is a lightweight descriptor for the enable/disable model shown in /status.
type module struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Status  string `json:"status"`
}

// modules lists what ships and whether each is switched on right now. Music is a preview,
// off by default; Books and Music follow their Settings toggles, the rest are always on.
func (a *api) modules(ctx context.Context) []module {
	return []module{
		{"movies", "Movies", true, "available"},
		{"series", "Series", true, "available"},
		{"books", "Books", a.booksEnabled(ctx), "available"},
		{"requests", "Requests", true, "available"},
		{"subtitles", "Subtitles", true, "available"},
		{"convert", "Convert", true, "available"},
		{"insights", "Insights", true, "available"},
		{"music", "Music", a.musicEnabled(ctx), "preview"},
	}
}

// musicOffMessage is what every Music endpoint answers while the module is switched off.
const musicOffMessage = "The Music module is turned off (Settings → System → Modules)"

// musicRoute 404s a Music endpoint while the module is switched off, so turning it off
// stops the manual actions (add, discography, scan…) as well as the background sweep.
// Checked per request, so switching it back on needs no restart.
func (a *api) musicRoute(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.musicEnabled(r.Context()) {
			a.writeError(w, http.StatusNotFound, musicOffMessage)
			return
		}
		h(w, r)
	}
}

func (a *api) handleHealth(w http.ResponseWriter, r *http.Request) {
	dbOK := a.deps.Store.Ping(r.Context()) == nil

	status, code := "ok", http.StatusOK
	if !dbOK {
		status, code = "degraded", http.StatusServiceUnavailable
	}

	body := map[string]any{
		"status":         status,
		"version":        buildinfo.Version,
		"commit":         buildinfo.Commit,
		"uptime_seconds": int(time.Since(a.start).Seconds()),
		"checks": map[string]string{
			"database": boolStatus(dbOK),
		},
	}
	// Only for callers inside the container (update.sh); everyone else gets the payload
	// above exactly.
	if a.isLocalCaller(r) {
		body["busy"] = a.healthBusy()
	}
	a.writeJSON(w, code, body)
}

func boolStatus(ok bool) string {
	if ok {
		return "ok"
	}
	return "down"
}

func (a *api) handleStatus(w http.ResponseWriter, r *http.Request) {
	// needs_setup lets the UI show first-run onboarding vs a login screen. Needs setup
	// until an admin exists (a lone requester shouldn't block bootstrap).
	needsSetup := false
	if n, err := a.deps.Auth.CountAdmins(r.Context()); err == nil {
		needsSetup = n == 0
	}
	_, authed := userFrom(r)

	out := map[string]any{
		"app":            "Arrmada",
		"version":        buildinfo.Version,
		"commit":         buildinfo.Commit,
		"started_at":     a.start.UTC().Format(time.RFC3339),
		"uptime_seconds": int(time.Since(a.start).Seconds()),
		"auth_enabled":   true, // always enforced; kept in the payload for the UI/API
		"needs_setup":    needsSetup,
		"authenticated":  authed,
		"plex_login":     a.deps.Settings.GetBool(r.Context(), "plex_login_enabled", false),
		"external":       isExternalRequest(r), // request came from outside the LAN → Discover-only

		"modules":       a.modules(r.Context()),
		"books_enabled": a.booksEnabled(r.Context()),
		"music_enabled": a.musicEnabled(r.Context()),
	}
	// Whether a TMDB key is set is configuration, so only signed-in callers learn it. The UI
	// uses it to show one role-aware "not set up" message instead of a failing row per feed.
	if authed {
		out["metadata_ready"] = a.metadataReady()
	}
	a.writeJSON(w, http.StatusOK, out)
}

func (a *api) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Live data — never let a browser/proxy serve a stale API response.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		a.deps.Log.Error("failed to encode json response", "err", err)
	}
}

// writeError sends a uniform JSON error envelope.
func (a *api) writeError(w http.ResponseWriter, status int, message string) {
	a.writeJSON(w, status, map[string]string{"status": "error", "message": message})
}
