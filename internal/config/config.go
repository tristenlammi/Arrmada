// Package config loads Arrmada's runtime configuration. For M0 this is a small
// env-driven struct with sensible defaults; file-based config and a settings UI
// arrive in later M0 workstreams (see BUILD-PLAN.md).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is the resolved runtime configuration.
type Config struct {
	// Host is the interface the HTTP server binds to.
	Host string
	// Port is the HTTP port. One port serves both the API and the web UI.
	Port int
	// BaseURL is an optional reverse-proxy sub-path (e.g. "/arrmada"). Empty = root.
	BaseURL string
	// DataDir holds the database, config, logs, and backups.
	DataDir string
	// LogLevel is one of: debug, info, warn, error.
	LogLevel string
	// AuthEnabled gates login/session/API-key enforcement. Default false while
	// in early local development — every request runs as a local admin. Flip on
	// (ARRMADA_AUTH_ENABLED=true) before exposing Arrmada to a network.
	AuthEnabled bool
	// PlexURL / PlexToken, when set, seed the Insights module's Plex connection on
	// startup (the UI can also set them). Token is a secret — keep it in .env.
	PlexURL   string
	PlexToken string
	// GeoIPDB is an optional MaxMind GeoLite2-City.mmdb for IP geolocation in
	// Insights. Empty → geolocation shows LAN as "Local" and public IPs raw.
	GeoIPDB string
	// QbittorrentURL, when set, is the bundled qBittorrent companion; Arrmada
	// auto-registers it as a download client on startup.
	QbittorrentURL string
	// QbittorrentPort is the incoming BitTorrent port to enforce on the bundled
	// client (0 = leave it alone). Set once at install to a random value so it
	// matches the Docker-published port; the user forwards this on their router.
	QbittorrentPort int
	// LibraryDir is the root of the organized media library (imports land here).
	LibraryDir string
	// DownloadCategory is the download-client category Arrmada imports from.
	DownloadCategory string
	// DownloadsDir is where the download client writes files (shared volume);
	// used as the default scan location for manual import.
	DownloadsDir string
	// RecycleDir is where deleted files are moved instead of being hard-deleted.
	// Empty = default to <LibraryDir>/.recycle; "off" = hard delete.
	RecycleDir string
	// FlaresolverrURL, when set, is used to get past Cloudflare on protected
	// trackers (e.g. TorrentLeech).
	FlaresolverrURL string
	// TMDBAPIKey enables movie/TV metadata (a free v3 key from themoviedb.org).
	TMDBAPIKey string
	// OMDBAPIKey enables external ratings (IMDB score, Rotten Tomatoes, Metacritic)
	// on the detail modal — a free key from omdbapi.com. Optional; without it the
	// modal still shows the TMDB score and full cast/crew.
	OMDBAPIKey string
	// ProwlarrURL, when set, is the default address of an (optional) Prowlarr
	// instance used by "Sync from Prowlarr". Must be reachable from the server.
	ProwlarrURL string
	// OpenSubtitles credentials for the Subtitles module (opensubtitles.com REST API).
	// The API key enables search; a free username/password additionally enables download.
	OpenSubtitlesAPIKey   string
	OpenSubtitlesUsername string
	OpenSubtitlesPassword string
	// ConvertScratchDir is the working directory the Convert module encodes into before
	// atomically replacing the original. Ideally a fast local disk. Empty = <DataDir>/convert.
	ConvertScratchDir string
}

// Load builds a Config from environment variables, falling back to defaults.
// Every key is prefixed ARRMADA_ so it never collides with other services.
func Load() (Config, error) {
	c := Config{
		Host:     env("ARRMADA_HOST", "0.0.0.0"),
		BaseURL:  normalizeBaseURL(env("ARRMADA_BASE_URL", "")),
		DataDir:  env("ARRMADA_DATA_DIR", "./data"),
		LogLevel: strings.ToLower(env("ARRMADA_LOG_LEVEL", "info")),
		// Default off during local development.
		AuthEnabled:      envBool("ARRMADA_AUTH_ENABLED", false),
		QbittorrentURL:   env("ARRMADA_QBITTORRENT_URL", ""),
		LibraryDir:       env("ARRMADA_LIBRARY_DIR", "./library"),
		DownloadCategory: env("ARRMADA_DOWNLOAD_CATEGORY", "arrmada"),
		DownloadsDir:     env("ARRMADA_DOWNLOADS_DIR", "./downloads"),
		RecycleDir:       env("ARRMADA_RECYCLE_DIR", ""),
		FlaresolverrURL:  env("ARRMADA_FLARESOLVERR_URL", ""),
		TMDBAPIKey:       env("ARRMADA_TMDB_API_KEY", ""),
		OMDBAPIKey:       env("ARRMADA_OMDB_API_KEY", ""),
		ProwlarrURL:      env("ARRMADA_PROWLARR_URL", ""),
		OpenSubtitlesAPIKey:   env("ARRMADA_OPENSUBTITLES_API_KEY", ""),
		OpenSubtitlesUsername: env("ARRMADA_OPENSUBTITLES_USERNAME", ""),
		OpenSubtitlesPassword: env("ARRMADA_OPENSUBTITLES_PASSWORD", ""),
		ConvertScratchDir:     env("ARRMADA_CONVERT_SCRATCH_DIR", ""),
		PlexURL:               env("ARRMADA_PLEX_URL", ""),
		PlexToken:             env("ARRMADA_PLEX_TOKEN", ""),
		GeoIPDB:               env("ARRMADA_GEOIP_DB", ""),
	}

	port, err := strconv.Atoi(env("ARRMADA_PORT", "7878"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid ARRMADA_PORT: %w", err)
	}
	c.Port = port

	// Optional; ignore a blank/garbage value (0 = don't manage the client's port).
	c.QbittorrentPort, _ = strconv.Atoi(env("ARRMADA_QBIT_PORT", "0"))

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("invalid ARRMADA_LOG_LEVEL %q (want debug|info|warn|error)", c.LogLevel)
	}

	return c, nil
}

// Addr returns the host:port the server binds to.
func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// normalizeBaseURL trims trailing slashes and guarantees a single leading slash
// (or empty string for root hosting).
func normalizeBaseURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "/" {
		return ""
	}
	v = "/" + strings.Trim(v, "/")
	return v
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
