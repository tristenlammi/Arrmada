package health

// Every place a health problem links to. The keys are the web UI's LINKS names
// (web/src/lib/links.ts), which the UI resolves first; the paths are the same addresses
// for any other client. When a page moves, repoint it here and in links.ts together.
var (
	FixIndexers        = &Fix{Key: "indexers", Path: "/indexers", Label: "Add an indexer"}
	FixDownloadClients = &Fix{Key: "downloadClients", Path: "/downloadclients", Label: "Check download clients"}
	FixLibraryFolders  = &Fix{Key: "libraryFolders", Path: "/settings?tab=library#media-folders", Label: "Choose folders"}
	FixDiskGuard       = &Fix{Key: "diskGuard", Path: "/settings?tab=system#disk-guard", Label: "Disk guard settings"}
	FixRecycleBin      = &Fix{Key: "recycleBin", Path: "/settings?tab=system#recycle-bin", Label: "Recycle bin settings"}
	FixAPIKeys         = &Fix{Key: "apiKeys", Path: "/settings?tab=system#api-keys", Label: "API keys"}
	FixAudiobookServer = &Fix{Key: "audiobookServer", Path: "/audiobooks", Label: "Audiobook server"}
	FixPlexConnection  = &Fix{Key: "plexConnection", Path: "/insights?tab=settings", Label: "Plex connection"}
	FixTasks           = &Fix{Key: "tasks", Path: "/settings/status#tasks", Label: "Tasks"}
	FixBackups         = &Fix{Key: "backups", Path: "/settings?tab=system#backups", Label: "Backups"}
)
