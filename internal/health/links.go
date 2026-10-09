package health

// Every place a health problem links to. The keys are the web UI's LINKS names
// (web/src/lib/links.ts), which the UI resolves first; the paths are the same addresses
// for any other client. When a page moves, repoint it here and in links.ts together.
var (
	FixIndexers        = &Fix{Key: "indexers", Path: "/indexers", Label: "Add an indexer"}
	FixIndexerStatus   = &Fix{Key: "indexers", Path: "/indexers", Label: "Check indexers"}
	FixDownloadClients = &Fix{Key: "downloadClients", Path: "/downloadclients", Label: "Check download clients"}
	FixLibraryFolders  = &Fix{Key: "libraryFolders", Path: "/settings/library#media-folders", Label: "Choose folders"}
	FixDiskGuard       = &Fix{Key: "diskGuard", Path: "/settings/downloads#disk-guard", Label: "Disk guard settings"}
	FixRecycleBin      = &Fix{Key: "recycleBin", Path: "/settings/downloads#recycle-bin", Label: "Recycle bin settings"}
	FixAPIKeys         = &Fix{Key: "apiKeys", Path: "/settings/system#api-keys", Label: "API keys"}
	FixAudiobookServer = &Fix{Key: "audiobookServer", Path: "/audiobooks", Label: "Audiobook server"}
	FixPlexConnection  = &Fix{Key: "plexConnection", Path: "/insights?tab=settings", Label: "Plex connection"}
	FixTasks           = &Fix{Key: "tasks", Path: "/settings/status#tasks", Label: "Tasks"}
	FixBackups         = &Fix{Key: "backups", Path: "/settings/system#backups", Label: "Backups"}
	FixStatus          = &Fix{Key: "status", Path: "/settings/status", Label: "Status"}

	// Where the Needs-you feed (internal/attention) sends each kind of item. The requests
	// page is /requests; until a build has it, the UI's LINKS.requests entry (which it
	// resolves first) still points at Discover.
	FixRequests           = &Fix{Key: "requests", Path: "/requests", Label: "Requests"}
	FixReview             = &Fix{Key: "review", Path: "/review", Label: "Review"}
	FixDownloadProblems   = &Fix{Key: "downloadProblems", Path: "/downloads?show=problems", Label: "Downloads"}
	FixDownloadsSearching = &Fix{Key: "downloadsSearching", Path: "/downloads?tab=searching", Label: "What's searching"}
)
