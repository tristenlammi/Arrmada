// Package httpapi wires Arrmada's HTTP surface: a versioned JSON API plus the
// embedded web UI, both served from one port. M0 ships the skeleton — health,
// status, request logging, and panic recovery — that later modules hang off.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/buildinfo"
	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/notify"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/realtime"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/convert"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/applog"
	"github.com/tristenlammi/arrmada/internal/recyclebin"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
	"github.com/tristenlammi/arrmada/internal/subtitles"
	"github.com/tristenlammi/arrmada/internal/webui"
)

// Deps bundles everything the HTTP layer needs. Grouping them keeps New's
// signature stable as more subsystems come online.
type Deps struct {
	Config   config.Config
	Log      *slog.Logger
	Store    *store.Store
	Bus      *eventbus.Bus
	Auth     *auth.Service
	Realtime  *realtime.Hub
	Indexers  *indexer.Service
	Downloads *download.Service
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
	Subtitles  *subtitles.Service
	Convert    *convert.Service
	Insights   *insights.Service
	Recycle    *recyclebin.Service
	Logs       *applog.Ring
}

type api struct {
	deps  Deps
	start time.Time
}

// New builds the HTTP server: JSON API routes, the embedded UI (with SPA
// fallback), and the middleware chain (recover → log → mux).
func New(d Deps) *http.Server {
	a := &api{deps: d, start: time.Now()}

	mux := http.NewServeMux()
	base := d.Config.BaseURL // "" for root, or "/sub-path"

	mux.HandleFunc("GET "+base+"/api/health", a.handleHealth)
	mux.HandleFunc("GET "+base+"/api/v1/health/system", a.protected(a.handleSystemHealth))
	mux.HandleFunc("GET "+base+"/api/v1/status", a.handleStatus)

	// App preferences
	mux.HandleFunc("GET "+base+"/api/v1/settings", a.protected(a.handleGetSettings))
	mux.HandleFunc("PUT "+base+"/api/v1/settings", a.requireRole(auth.RoleManager, a.handleUpdateSettings))
	// Library folders + filesystem browser (in-app folder picker).
	mux.HandleFunc("GET "+base+"/api/v1/system/library", a.requireRole(auth.RoleManager, a.handleGetLibraryPaths))
	mux.HandleFunc("PUT "+base+"/api/v1/system/library", a.requireRole(auth.RoleManager, a.handleSetLibraryPaths))
	mux.HandleFunc("GET "+base+"/api/v1/system/browse", a.requireRole(auth.RoleManager, a.handleBrowse))

	// Auth
	mux.HandleFunc("POST "+base+"/api/v1/auth/setup", a.handleSetup)
	mux.HandleFunc("POST "+base+"/api/v1/auth/login", a.handleLogin)
	mux.HandleFunc("POST "+base+"/api/v1/auth/plex/pin", a.handlePlexLoginStart)
	mux.HandleFunc("GET "+base+"/api/v1/auth/plex/pin/{id}", a.handlePlexLoginPoll)
	mux.HandleFunc("POST "+base+"/api/v1/auth/logout", a.handleLogout)
	mux.HandleFunc("GET "+base+"/api/v1/auth/me", a.protected(a.handleMe))
	// Per-user notifications (in-app inbox + personal Apprise URL).
	mux.HandleFunc("GET "+base+"/api/v1/me/notifications", a.protected(a.handleMyNotifications))
	mux.HandleFunc("POST "+base+"/api/v1/me/notifications/read-all", a.protected(a.handleMarkAllNotificationsRead))
	mux.HandleFunc("POST "+base+"/api/v1/me/notifications/{id}/read", a.protected(a.handleMarkNotificationRead))
	mux.HandleFunc("GET "+base+"/api/v1/me/apprise", a.protected(a.handleGetMyApprise))
	mux.HandleFunc("PUT "+base+"/api/v1/me/apprise", a.protected(a.handleSetMyApprise))

	// User management (admin only).
	mux.HandleFunc("GET "+base+"/api/v1/users", a.requireRole(auth.RoleAdmin, a.handleListUsers))
	mux.HandleFunc("POST "+base+"/api/v1/users", a.requireRole(auth.RoleAdmin, a.handleCreateUser))
	mux.HandleFunc("PUT "+base+"/api/v1/users/{id}", a.requireRole(auth.RoleAdmin, a.handleUpdateUser))
	mux.HandleFunc("DELETE "+base+"/api/v1/users/{id}", a.requireRole(auth.RoleAdmin, a.handleDeleteUser))

	// Realtime updates
	mux.HandleFunc("GET "+base+"/api/v1/ws", a.protected(a.handleWS))

	// Acquisition utilities
	mux.HandleFunc("GET "+base+"/api/v1/parse", a.protected(a.handleParse))

	// Quality profiles + custom-format builder
	mux.HandleFunc("GET "+base+"/api/v1/quality/preview", a.protected(a.handleQualityPreview))
	mux.HandleFunc("POST "+base+"/api/v1/quality/preview", a.protected(a.handleQualityPreview))
	mux.HandleFunc("GET "+base+"/api/v1/quality/profiles", a.protected(a.handleListQualityProfiles))
	mux.HandleFunc("POST "+base+"/api/v1/quality/profiles", a.requireRole(auth.RoleManager, a.handleCreateQualityProfile))
	mux.HandleFunc("POST "+base+"/api/v1/quality/default", a.requireRole(auth.RoleManager, a.handleSetDefaultProfile))
	mux.HandleFunc("GET "+base+"/api/v1/quality/profiles/{ref}", a.protected(a.handleGetQualityProfile))
	mux.HandleFunc("PUT "+base+"/api/v1/quality/profiles/{id}", a.requireRole(auth.RoleManager, a.handleUpdateQualityProfile))
	mux.HandleFunc("DELETE "+base+"/api/v1/quality/profiles/{id}", a.requireRole(auth.RoleManager, a.handleDeleteQualityProfile))

	// Indexers + search
	mux.HandleFunc("GET "+base+"/api/v1/indexers", a.protected(a.handleListIndexers))
	mux.HandleFunc("POST "+base+"/api/v1/indexers", a.requireRole(auth.RoleManager, a.handleCreateIndexer))
	mux.HandleFunc("PUT "+base+"/api/v1/indexers/{id}", a.requireRole(auth.RoleManager, a.handleUpdateIndexer))
	mux.HandleFunc("DELETE "+base+"/api/v1/indexers/{id}", a.requireRole(auth.RoleManager, a.handleDeleteIndexer))
	mux.HandleFunc("POST "+base+"/api/v1/indexers/{id}/test", a.protected(a.handleTestIndexer))
	mux.HandleFunc("GET "+base+"/api/v1/search", a.protected(a.handleSearch))

	// Download clients + queue
	mux.HandleFunc("GET "+base+"/api/v1/downloadclients", a.protected(a.handleListDownloadClients))
	mux.HandleFunc("POST "+base+"/api/v1/downloadclients", a.requireRole(auth.RoleManager, a.handleCreateDownloadClient))
	mux.HandleFunc("DELETE "+base+"/api/v1/downloadclients/{id}", a.requireRole(auth.RoleManager, a.handleDeleteDownloadClient))
	mux.HandleFunc("POST "+base+"/api/v1/downloadclients/{id}/test", a.protected(a.handleTestDownloadClient))
	mux.HandleFunc("GET "+base+"/api/v1/downloadclients/{id}/status", a.protected(a.handleDownloadClientStatus))
	mux.HandleFunc("GET "+base+"/api/v1/downloadclients/{id}/settings", a.protected(a.handleGetClientSettings))
	mux.HandleFunc("PUT "+base+"/api/v1/downloadclients/{id}/settings", a.requireRole(auth.RoleManager, a.handleSetClientSettings))
	mux.HandleFunc("GET "+base+"/api/v1/indexers/prowlarr", a.protected(a.handleProwlarrInfo))
	mux.HandleFunc("POST "+base+"/api/v1/indexers/prowlarr/sync", a.requireRole(auth.RoleManager, a.handleProwlarrSync))
	mux.HandleFunc("GET "+base+"/api/v1/notifications", a.protected(a.handleListNotifications))
	mux.HandleFunc("POST "+base+"/api/v1/notifications", a.requireRole(auth.RoleManager, a.handleCreateNotification))
	mux.HandleFunc("PUT "+base+"/api/v1/notifications/{id}", a.requireRole(auth.RoleManager, a.handleUpdateNotification))
	mux.HandleFunc("DELETE "+base+"/api/v1/notifications/{id}", a.requireRole(auth.RoleManager, a.handleDeleteNotification))
	mux.HandleFunc("POST "+base+"/api/v1/notifications/test", a.requireRole(auth.RoleManager, a.handleTestNotification))
	mux.HandleFunc("GET "+base+"/api/v1/queue", a.protected(a.handleQueue))
	mux.HandleFunc("GET "+base+"/api/v1/downloads", a.protected(a.handleDownloadsFeed))
	mux.HandleFunc("POST "+base+"/api/v1/queue/{hash}/pause", a.requireRole(auth.RoleManager, a.handlePauseDownload))
	mux.HandleFunc("POST "+base+"/api/v1/queue/{hash}/resume", a.requireRole(auth.RoleManager, a.handleResumeDownload))
	mux.HandleFunc("POST "+base+"/api/v1/queue/{hash}/block", a.requireRole(auth.RoleManager, a.handleBlockDownload))
	mux.HandleFunc("POST "+base+"/api/v1/queue/{hash}/action", a.requireRole(auth.RoleManager, a.handleTorrentAction))
	mux.HandleFunc("DELETE "+base+"/api/v1/queue/{hash}", a.requireRole(auth.RoleManager, a.handleDeleteDownload))

	// Grab: search result → download client (closes the acquisition loop).
	mux.HandleFunc("POST "+base+"/api/v1/grab", a.protected(a.handleGrab))
	mux.HandleFunc("POST "+base+"/api/v1/grab/preview", a.requireRole(auth.RoleManager, a.handleGrabPreview))
	mux.HandleFunc("POST "+base+"/api/v1/movies/{id}/grabtorrent", a.requireRole(auth.RoleManager, a.handleMovieGrabTorrent))
	mux.HandleFunc("POST "+base+"/api/v1/series/{id}/grabtorrent", a.requireRole(auth.RoleManager, a.handleSeriesGrabTorrent))

	// Import history
	mux.HandleFunc("GET "+base+"/api/v1/history", a.protected(a.handleHistory))

	// Import review — downloads held because their content didn't match what they
	// were grabbed for (admin-only).
	mux.HandleFunc("GET "+base+"/api/v1/reviews", a.requireRole(auth.RoleManager, a.handleListReviews))
	mux.HandleFunc("POST "+base+"/api/v1/reviews/{id}/reject", a.requireRole(auth.RoleManager, a.handleRejectReview))
	mux.HandleFunc("POST "+base+"/api/v1/reviews/{id}/dismiss", a.requireRole(auth.RoleManager, a.handleDismissReview))
	mux.HandleFunc("POST "+base+"/api/v1/reviews/{id}/import", a.requireRole(auth.RoleManager, a.handleImportReview))

	// Movies
	mux.HandleFunc("GET "+base+"/api/v1/movies", a.protected(a.handleListMovies))
	mux.HandleFunc("GET "+base+"/api/v1/movies/lookup", a.protected(a.handleLookupMovies))
	mux.HandleFunc("POST "+base+"/api/v1/movies/scan", a.requireRole(auth.RoleManager, a.handleScanLibrary))
	mux.HandleFunc("GET "+base+"/api/v1/movies/unmatched", a.requireRole(auth.RoleManager, a.handleMovieUnmatched))
	mux.HandleFunc("POST "+base+"/api/v1/movies/import", a.requireRole(auth.RoleManager, a.handleMovieImportFolder))
	mux.HandleFunc("GET "+base+"/api/v1/movies/{id}", a.protected(a.handleGetMovie))
	mux.HandleFunc("POST "+base+"/api/v1/movies", a.protected(a.handleAddMovie))
	mux.HandleFunc("POST "+base+"/api/v1/movies/{id}/search", a.protected(a.handleSearchMovie))
	mux.HandleFunc("GET "+base+"/api/v1/movies/{id}/releases", a.protected(a.handleMovieReleases))
	mux.HandleFunc("GET "+base+"/api/v1/movies/{id}/history", a.protected(a.handleMovieHistory))
	mux.HandleFunc("GET "+base+"/api/v1/movies/{id}/collection", a.protected(a.handleMovieCollection))

	// Series (TV).
	mux.HandleFunc("GET "+base+"/api/v1/series", a.protected(a.handleListSeries))
	mux.HandleFunc("GET "+base+"/api/v1/series/lookup", a.protected(a.handleLookupSeries))
	mux.HandleFunc("POST "+base+"/api/v1/series/scan", a.requireRole(auth.RoleManager, a.handleScanSeriesLibrary))
	mux.HandleFunc("GET "+base+"/api/v1/series/unmatched", a.requireRole(auth.RoleManager, a.handleSeriesUnmatched))
	mux.HandleFunc("POST "+base+"/api/v1/series/import", a.requireRole(auth.RoleManager, a.handleSeriesImportFolder))
	mux.HandleFunc("POST "+base+"/api/v1/series", a.protected(a.handleAddSeries))
	mux.HandleFunc("GET "+base+"/api/v1/series/{id}/history", a.protected(a.handleSeriesHistory))
	mux.HandleFunc("POST "+base+"/api/v1/series/{id}/search", a.protected(a.handleSearchSeries))
	mux.HandleFunc("GET "+base+"/api/v1/series/{id}/releases", a.protected(a.handleSeriesReleases))
	mux.HandleFunc("POST "+base+"/api/v1/series/{id}/grab", a.requireRole(auth.RoleManager, a.handleGrabSeries))
	mux.HandleFunc("POST "+base+"/api/v1/series/{id}/autograb", a.requireRole(auth.RoleManager, a.handleAutoGrabSeries))
	mux.HandleFunc("POST "+base+"/api/v1/series/{id}/refresh", a.protected(a.handleRefreshSeries))
	mux.HandleFunc("GET "+base+"/api/v1/series/{id}/manualimport", a.protected(a.handleSeriesManualImportList))
	mux.HandleFunc("POST "+base+"/api/v1/series/{id}/manualimport", a.requireRole(auth.RoleManager, a.handleSeriesManualImport))
	mux.HandleFunc("GET "+base+"/api/v1/series/{id}/rename", a.protected(a.handleSeriesRenamePreview))
	mux.HandleFunc("POST "+base+"/api/v1/series/{id}/rename", a.requireRole(auth.RoleManager, a.handleSeriesRename))
	mux.HandleFunc("GET "+base+"/api/v1/series/{id}", a.protected(a.handleGetSeries))
	mux.HandleFunc("PUT "+base+"/api/v1/series/{id}/monitor", a.protected(a.handleSetSeriesMonitored))
	mux.HandleFunc("PUT "+base+"/api/v1/series/{id}/profile", a.protected(a.handleSetSeriesProfile))
	mux.HandleFunc("PUT "+base+"/api/v1/series/{id}/type", a.protected(a.handleSetSeriesType))
	mux.HandleFunc("PUT "+base+"/api/v1/series/{id}/seasons/{season}/monitor", a.protected(a.handleSetSeasonMonitored))
	mux.HandleFunc("PUT "+base+"/api/v1/series/episodes/{eid}/monitor", a.protected(a.handleSetEpisodeMonitored))
	mux.HandleFunc("GET "+base+"/api/v1/series/{id}/blocklist", a.protected(a.handleSeriesBlocklist))
	mux.HandleFunc("POST "+base+"/api/v1/series/{id}/blocklist", a.requireRole(auth.RoleManager, a.handleSeriesBlock))
	mux.HandleFunc("DELETE "+base+"/api/v1/series/{id}/blocklist/{bid}", a.requireRole(auth.RoleManager, a.handleSeriesUnblock))
	mux.HandleFunc("POST "+base+"/api/v1/series/{id}/seasons/{season}/episodes/{episode}/regrab", a.requireRole(auth.RoleManager, a.handleRegrabEpisode))
	mux.HandleFunc("DELETE "+base+"/api/v1/series/{id}/seasons/{season}/episodes/{episode}/file", a.requireRole(auth.RoleManager, a.handleDeleteEpisodeFile))
	mux.HandleFunc("DELETE "+base+"/api/v1/series/{id}", a.requireRole(auth.RoleManager, a.handleDeleteSeries))

	// Requests (Overseerr-style): request media → approve → add to Movies/Series.
	mux.HandleFunc("GET "+base+"/api/v1/requests", a.protected(a.handleListRequests))
	mux.HandleFunc("POST "+base+"/api/v1/requests", a.requireRole(auth.RoleRequester, a.handleCreateRequest))
	mux.HandleFunc("POST "+base+"/api/v1/requests/{id}/approve", a.requireRole(auth.RoleManager, a.handleApproveRequest))
	mux.HandleFunc("POST "+base+"/api/v1/requests/{id}/decline", a.requireRole(auth.RoleManager, a.handleDeclineRequest))
	mux.HandleFunc("DELETE "+base+"/api/v1/requests/{id}", a.requireRole(auth.RoleManager, a.handleDeleteRequest))
	mux.HandleFunc("POST "+base+"/api/v1/requests/import/overseerr", a.requireRole(auth.RoleAdmin, a.handleImportOverseerr))
	mux.HandleFunc("POST "+base+"/api/v1/insights/import/tautulli", a.requireRole(auth.RoleAdmin, a.handleImportTautulli))

	// Convert (Tdarr replacement — GPU transcoding/cleanup over the Movies/Series catalogs).
	mux.HandleFunc("GET "+base+"/api/v1/logs", a.requireRole(auth.RoleManager, a.handleLogs))
	mux.HandleFunc("GET "+base+"/api/v1/recycle", a.requireRole(auth.RoleManager, a.handleRecycleStats))
	mux.HandleFunc("GET "+base+"/api/v1/recycle/items", a.requireRole(auth.RoleManager, a.handleRecycleItems))
	mux.HandleFunc("POST "+base+"/api/v1/recycle/empty", a.requireRole(auth.RoleManager, a.handleRecycleEmpty))
	mux.HandleFunc("POST "+base+"/api/v1/recycle/restore", a.requireRole(auth.RoleManager, a.handleRecycleRestore))
	mux.HandleFunc("POST "+base+"/api/v1/recycle/delete", a.requireRole(auth.RoleManager, a.handleRecycleDeleteItem))
	mux.HandleFunc("GET "+base+"/api/v1/convert/hardware", a.protected(a.handleConvertHardware))
	mux.HandleFunc("GET "+base+"/api/v1/convert/library", a.protected(a.handleConvertLibrary))
	mux.HandleFunc("GET "+base+"/api/v1/convert/jobs", a.protected(a.handleConvertJobs))
	mux.HandleFunc("GET "+base+"/api/v1/convert/logs", a.protected(a.handleConvertLogs))
	mux.HandleFunc("POST "+base+"/api/v1/convert/sweep", a.requireRole(auth.RoleManager, a.handleConvertSweep))
	mux.HandleFunc("POST "+base+"/api/v1/convert/movies/{id}", a.requireRole(auth.RoleManager, a.handleConvertMovie))
	mux.HandleFunc("POST "+base+"/api/v1/convert/movies/{id}/sample", a.requireRole(auth.RoleManager, a.handleConvertMovieSample))
	mux.HandleFunc("POST "+base+"/api/v1/convert/episodes/{series}/{season}/{episode}", a.requireRole(auth.RoleManager, a.handleConvertEpisode))

	// Insights (Tautulli replacement — Plex watch monitoring). I0: connection config + test.
	mux.HandleFunc("GET "+base+"/api/v1/insights/plex", a.requireRole(auth.RoleManager, a.handleInsightsConfig))
	mux.HandleFunc("PUT "+base+"/api/v1/insights/plex", a.requireRole(auth.RoleManager, a.handleUpdateInsightsConfig))
	mux.HandleFunc("POST "+base+"/api/v1/insights/plex/test", a.requireRole(auth.RoleManager, a.handleInsightsTest))
	mux.HandleFunc("POST "+base+"/api/v1/insights/plex/auth", a.requireRole(auth.RoleManager, a.handleInsightsPlexAuthStart))
	mux.HandleFunc("GET "+base+"/api/v1/insights/plex/auth/{id}", a.requireRole(auth.RoleManager, a.handleInsightsPlexAuthPoll))
	mux.HandleFunc("GET "+base+"/api/v1/insights/activity", a.requireRole(auth.RoleManager, a.handleInsightsActivity))
	mux.HandleFunc("GET "+base+"/api/v1/insights/history", a.requireRole(auth.RoleManager, a.handleInsightsHistory))
	mux.HandleFunc("GET "+base+"/api/v1/insights/stats", a.requireRole(auth.RoleManager, a.handleInsightsStats))
	mux.HandleFunc("GET "+base+"/api/v1/insights/graphs", a.requireRole(auth.RoleManager, a.handleInsightsGraphs))
	mux.HandleFunc("GET "+base+"/api/v1/insights/reliability", a.requireRole(auth.RoleManager, a.handleInsightsReliability))
	mux.HandleFunc("GET "+base+"/api/v1/insights/users", a.requireRole(auth.RoleManager, a.handleInsightsUsers))
	mux.HandleFunc("GET "+base+"/api/v1/insights/libraries", a.requireRole(auth.RoleManager, a.handleInsightsLibraries))
	mux.HandleFunc("GET "+base+"/api/v1/insights/recently-added", a.requireRole(auth.RoleManager, a.handleInsightsRecentlyAdded))
	mux.HandleFunc("GET "+base+"/api/v1/insights/image", a.protected(a.handleInsightsImage))

	// Subtitles (Bazarr replacement — external SRT sidecars over the Movies/Series catalogs).
	mux.HandleFunc("GET "+base+"/api/v1/subtitles/library", a.protected(a.handleSubtitleLibrary))
	mux.HandleFunc("GET "+base+"/api/v1/subtitles/models", a.protected(a.handleSubtitleModels))
	mux.HandleFunc("POST "+base+"/api/v1/subtitles/models/{name}", a.requireRole(auth.RoleManager, a.handleSubtitleDownloadModel))
	mux.HandleFunc("GET "+base+"/api/v1/subtitles/jobs", a.protected(a.handleSubtitleJobs))
	mux.HandleFunc("GET "+base+"/api/v1/subtitles/logs", a.protected(a.handleSubtitleLogs))
	mux.HandleFunc("POST "+base+"/api/v1/subtitles/sweep", a.requireRole(auth.RoleManager, a.handleSubtitleSweep))
	mux.HandleFunc("POST "+base+"/api/v1/subtitles/library/movies/{id}", a.requireRole(auth.RoleManager, a.handleSubtitleQueueMovie))
	mux.HandleFunc("POST "+base+"/api/v1/subtitles/library/episodes/{series}/{season}/{episode}", a.requireRole(auth.RoleManager, a.handleSubtitleQueueEpisode))
	mux.HandleFunc("GET "+base+"/api/v1/subtitles/settings", a.protected(a.handleGetSubtitleSettings))
	mux.HandleFunc("PUT "+base+"/api/v1/subtitles/settings", a.requireRole(auth.RoleManager, a.handleUpdateSubtitleSettings))
	mux.HandleFunc("GET "+base+"/api/v1/subtitles/movies", a.protected(a.handleSubtitleMovies))
	mux.HandleFunc("GET "+base+"/api/v1/subtitles/series", a.protected(a.handleSubtitleSeries))
	mux.HandleFunc("POST "+base+"/api/v1/subtitles/movies/{id}/search", a.requireRole(auth.RoleManager, a.handleSubtitleSearchMovie))
	mux.HandleFunc("POST "+base+"/api/v1/subtitles/series/{id}/search", a.requireRole(auth.RoleManager, a.handleSubtitleSearchSeries))

	// Books (Readarr replacement — Open Library metadata + ebook acquisition).
	mux.HandleFunc("GET "+base+"/api/v1/books", a.protected(a.handleListBooks))
	mux.HandleFunc("GET "+base+"/api/v1/books/lookup", a.protected(a.handleLookupBooks))
	mux.HandleFunc("POST "+base+"/api/v1/books/scan", a.requireRole(auth.RoleManager, a.handleScanBookLibrary))
	mux.HandleFunc("POST "+base+"/api/v1/books/author", a.requireRole(auth.RoleManager, a.handleAddAuthor))
	mux.HandleFunc("POST "+base+"/api/v1/books", a.protected(a.handleAddBook))
	mux.HandleFunc("POST "+base+"/api/v1/books/{id}/search", a.protected(a.handleSearchBook))
	mux.HandleFunc("POST "+base+"/api/v1/books/{id}/refresh", a.protected(a.handleRefreshBook))
	mux.HandleFunc("GET "+base+"/api/v1/books/{id}/releases", a.protected(a.handleBookReleases))
	mux.HandleFunc("POST "+base+"/api/v1/books/{id}/grab", a.requireRole(auth.RoleManager, a.handleGrabBook))
	mux.HandleFunc("GET "+base+"/api/v1/books/{id}/manualimport", a.protected(a.handleBookManualImportList))
	mux.HandleFunc("POST "+base+"/api/v1/books/{id}/manualimport", a.requireRole(auth.RoleManager, a.handleBookManualImport))
	mux.HandleFunc("POST "+base+"/api/v1/books/{id}/rename", a.requireRole(auth.RoleManager, a.handleBookRename))
	mux.HandleFunc("GET "+base+"/api/v1/books/{id}/edition-files", a.protected(a.handleBookEditionFiles))
	mux.HandleFunc("POST "+base+"/api/v1/books/{id}/merge-audiobook", a.requireRole(auth.RoleManager, a.handleMergeAudiobook))
	// Books Discover (Open Library browse/search + author catalogues).
	mux.HandleFunc("GET "+base+"/api/v1/books/discover/trending", a.protected(a.handleBookDiscoverTrending))
	mux.HandleFunc("GET "+base+"/api/v1/books/discover/search", a.protected(a.handleBookDiscoverSearch))
	mux.HandleFunc("GET "+base+"/api/v1/books/discover/authors", a.protected(a.handleBookAuthorSearch))
	mux.HandleFunc("GET "+base+"/api/v1/books/discover/authors/{key}/works", a.protected(a.handleBookAuthorWorks))
	mux.HandleFunc("GET "+base+"/api/v1/books/discover/subjects/{name}", a.protected(a.handleBookDiscoverSubject))
	mux.HandleFunc("GET "+base+"/api/v1/books/discover/detail", a.protected(a.handleBookDiscoverDetail))
	mux.HandleFunc("GET "+base+"/api/v1/books/{id}/covers", a.protected(a.handleBookCovers))
	mux.HandleFunc("GET "+base+"/api/v1/books/{id}/cover-image", a.protected(a.handleBookCoverImage))
	mux.HandleFunc("PUT "+base+"/api/v1/books/{id}/cover", a.protected(a.handleSetBookCover))
	mux.HandleFunc("POST "+base+"/api/v1/books/{id}/cover", a.protected(a.handleUploadBookCover))
	mux.HandleFunc("GET "+base+"/api/v1/books/{id}", a.protected(a.handleGetBook))
	mux.HandleFunc("PUT "+base+"/api/v1/books/{id}/monitor", a.protected(a.handleSetBookMonitored))
	mux.HandleFunc("PUT "+base+"/api/v1/books/{id}/profile", a.protected(a.handleSetBookProfile))
	mux.HandleFunc("PUT "+base+"/api/v1/books/{id}/metadata", a.requireRole(auth.RoleManager, a.handleOverrideBookMetadata))
	mux.HandleFunc("DELETE "+base+"/api/v1/books/{id}/file", a.requireRole(auth.RoleManager, a.handleDeleteBookFile))
	mux.HandleFunc("DELETE "+base+"/api/v1/books/{id}", a.requireRole(auth.RoleManager, a.handleDeleteBook))

	// Discover (browse trending/popular/upcoming/by-genre; enriched with library status).
	mux.HandleFunc("GET "+base+"/api/v1/calendar", a.protected(a.handleCalendar))
	mux.HandleFunc("GET "+base+"/api/v1/discover/trending", a.protected(a.handleDiscoverTrending))
	mux.HandleFunc("GET "+base+"/api/v1/discover/popular", a.protected(a.handleDiscoverPopular))
	mux.HandleFunc("GET "+base+"/api/v1/discover/upcoming", a.protected(a.handleDiscoverUpcoming))
	mux.HandleFunc("GET "+base+"/api/v1/discover/search", a.protected(a.handleDiscoverSearch))
	mux.HandleFunc("GET "+base+"/api/v1/discover/genres", a.protected(a.handleDiscoverGenres))
	mux.HandleFunc("GET "+base+"/api/v1/discover", a.protected(a.handleDiscoverByGenre))
	mux.HandleFunc("GET "+base+"/api/v1/media/{media}/{id}", a.protected(a.handleMediaDetail))
	mux.HandleFunc("GET "+base+"/api/v1/movies/{id}/blocklist", a.protected(a.handleListBlocklist))
	mux.HandleFunc("POST "+base+"/api/v1/movies/{id}/blocklist", a.requireRole(auth.RoleManager, a.handleBlocklist))
	mux.HandleFunc("DELETE "+base+"/api/v1/movies/{id}/blocklist/{bid}", a.requireRole(auth.RoleManager, a.handleUnblock))
	mux.HandleFunc("POST "+base+"/api/v1/movies/{id}/refresh", a.protected(a.handleRefreshMovie))
	mux.HandleFunc("PUT "+base+"/api/v1/movies/{id}/monitor", a.protected(a.handleSetMonitored))
	mux.HandleFunc("PUT "+base+"/api/v1/movies/{id}/profile", a.protected(a.handleSetProfile))
	mux.HandleFunc("POST "+base+"/api/v1/movies/{id}/regrab", a.requireRole(auth.RoleManager, a.handleRegrab))
	mux.HandleFunc("PUT "+base+"/api/v1/movies/{id}/availability", a.protected(a.handleSetAvailability))
	mux.HandleFunc("GET "+base+"/api/v1/movies/{id}/manualimport", a.protected(a.handleManualImportList))
	mux.HandleFunc("POST "+base+"/api/v1/movies/{id}/manualimport", a.requireRole(auth.RoleManager, a.handleManualImport))
	mux.HandleFunc("GET "+base+"/api/v1/movies/{id}/rename", a.protected(a.handleRenamePreview))
	mux.HandleFunc("POST "+base+"/api/v1/movies/{id}/rename", a.requireRole(auth.RoleManager, a.handleRename))
	// Multi-version tracks (opt-in)
	mux.HandleFunc("GET "+base+"/api/v1/movies/{id}/versions", a.protected(a.handleListVersions))
	mux.HandleFunc("POST "+base+"/api/v1/movies/{id}/versions", a.requireRole(auth.RoleManager, a.handleAddVersion))
	mux.HandleFunc("PUT "+base+"/api/v1/movies/{id}/versions/{vid}", a.requireRole(auth.RoleManager, a.handleUpdateVersion))
	mux.HandleFunc("DELETE "+base+"/api/v1/movies/{id}/versions/{vid}/file", a.requireRole(auth.RoleManager, a.handleDeleteVersionFile))
	mux.HandleFunc("DELETE "+base+"/api/v1/movies/{id}/versions/{vid}", a.requireRole(auth.RoleManager, a.handleDeleteVersion))
	mux.HandleFunc("DELETE "+base+"/api/v1/movies/{id}/file", a.requireRole(auth.RoleManager, a.handleDeleteMovieFile))
	mux.HandleFunc("DELETE "+base+"/api/v1/movies/{id}", a.requireRole(auth.RoleManager, a.handleDeleteMovie))

	ui := webui.Handler()
	if base != "" {
		mux.Handle(base+"/", http.StripPrefix(base, ui))
	} else {
		mux.Handle("/", ui)
	}

	// Chain: recover → authenticate (resolves the user) → external gate (LAN vs
	// outside) → log → routes.
	handler := a.recoverPanics(a.authenticate(a.externalGate(a.logRequests(mux))))

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

// module is a lightweight descriptor for the enable/disable model shown in /status.
type module struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Status  string `json:"status"`
}

// plannedModules reflects what actually ships. Movies/Series/Books/Requests/Subtitles/Convert are
// live; Insights and Music are still on the roadmap.
var plannedModules = []module{
	{"movies", "Movies", true, "available"},
	{"series", "Series", true, "available"},
	{"books", "Books", true, "available"},
	{"requests", "Requests", true, "available"},
	{"subtitles", "Subtitles", true, "available"},
	{"convert", "Convert", true, "available"},
	{"insights", "Insights", true, "available"},
	{"music", "Music", false, "planned"},
}

func (a *api) handleHealth(w http.ResponseWriter, r *http.Request) {
	dbOK := a.deps.Store.Ping(r.Context()) == nil

	status, code := "ok", http.StatusOK
	if !dbOK {
		status, code = "degraded", http.StatusServiceUnavailable
	}

	a.writeJSON(w, code, map[string]any{
		"status":         status,
		"version":        buildinfo.Version,
		"commit":         buildinfo.Commit,
		"uptime_seconds": int(time.Since(a.start).Seconds()),
		"checks": map[string]string{
			"database": boolStatus(dbOK),
		},
	})
}

func boolStatus(ok bool) string {
	if ok {
		return "ok"
	}
	return "down"
}

func (a *api) handleStatus(w http.ResponseWriter, r *http.Request) {
	// needs_setup lets the UI show first-run onboarding vs a login screen.
	// When auth is disabled (local dev) there's nothing to set up.
	needsSetup := false
	if a.deps.Config.AuthEnabled {
		// Needs setup until an admin exists (a lone requester shouldn't block bootstrap).
		if n, err := a.deps.Auth.CountAdmins(r.Context()); err == nil {
			needsSetup = n == 0
		}
	}
	_, authed := userFrom(r)

	a.writeJSON(w, http.StatusOK, map[string]any{
		"app":            "Arrmada",
		"version":        buildinfo.Version,
		"commit":         buildinfo.Commit,
		"started_at":     a.start.UTC().Format(time.RFC3339),
		"uptime_seconds": int(time.Since(a.start).Seconds()),
		"auth_enabled":   a.deps.Config.AuthEnabled,
		"needs_setup":    needsSetup,
		"authenticated":  authed,
		"plex_login":     a.deps.Settings.GetBool(r.Context(), "plex_login_enabled", false),
		"external":       isExternalRequest(r), // request came from outside the LAN → Discover-only

		"modules":        plannedModules,
		"books_enabled":  a.booksEnabled(r.Context()),
		"music_enabled":  a.musicEnabled(r.Context()),
	})
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
