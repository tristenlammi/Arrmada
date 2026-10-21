// Package config loads Arrmada's runtime configuration. For M0 this is a small
// env-driven struct with sensible defaults; file-based config and a settings UI
// arrive in later M0 workstreams.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the resolved runtime configuration.
type Config struct {
	// Host is the interface the HTTP server binds to.
	Host string
	// Port is the HTTP port. One port serves both the API and the web UI.
	Port int
	// BaseURLIgnored holds ARRMADA_BASE_URL when a deployment still sets it to a sub-path.
	// The option is gone — Arrmada is served at the root of its own hostname — so this
	// only exists for the startup warning; nothing routes by it.
	BaseURLIgnored string
	// DataDir holds the database, config and logs, plus backups/, where a snapshot
	// of the database is taken automatically before any schema change and every night.
	DataDir string
	// DatabaseBackend is opt-in: sqlite (default) or postgres. URL is secret.
	DatabaseBackend        string
	PostgresURL            string `json:"-"`
	PostgresSchema         string
	PostgresBinDir         string
	PostgresMaxConnections int
	// LogLevel is one of: debug, info, warn, error.
	LogLevel string
	// ExternalHeader marks a request as coming from OUTSIDE the LAN when present:
	// a Cloudflare Tunnel / reverse proxy stamps it (default Cf-Connecting-Ip),
	// while direct LAN hits don't. External requests are limited to Discover.
	// Empty disables header-based detection (falls back to a public source IP).
	ExternalHeader string
	// AllowRemoteSetup (ARRMADA_ALLOW_REMOTE_SETUP=1) lets first-run setup — making the
	// first admin — be done from outside the LAN. Off, only a visitor on the LAN can make
	// it, so a fresh install reachable from the internet can't be claimed by a stranger.
	AllowRemoteSetup bool
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
	// AIOQbittorrentURL is set only in the Unraid all-in-one image: where its built-in
	// qBittorrent answers, whether or not it's switched on. With it switched off,
	// QbittorrentURL is empty and the bundled client registered earlier is switched off
	// too; switching it back on switches that client back on.
	AIOQbittorrentURL string
	// QbittorrentLogin is the file holding the bundled qBittorrent's web UI login, which
	// Arrmada signs in with and admins can look up in Settings → Connections. The
	// all-in-one image points it at the login it seeded (ARRMADA_QBITTORRENT_LOGIN).
	// Everywhere else it's qbittorrent-login.json in the data folder, which Arrmada writes
	// when it gives the Compose bundle's qBittorrent a password (download.Service.
	// SecureBundledQbit), and which doesn't exist before then.
	QbittorrentLogin string
	// SABnzbdURL is where the optional bundled SABnzbd answers (docker-compose.yml's
	// "sabnzbd" profile). Compose always passes it; the client is only registered once the
	// bundled SABnzbd's config (with its API key) shows up at SABnzbdConfig, which happens
	// when the profile is switched on. The Unraid all-in-one image sets it only while its
	// built-in SABnzbd is switched on, with its config in appdata.
	SABnzbdURL string
	// SABnzbdConfig is the bundled SABnzbd's sabnzbd.ini, mounted read-only (in the
	// all-in-one image: the one in its appdata, set by the image).
	SABnzbdConfig string
	// AIOSABnzbdURL is AIOQbittorrentURL for the all-in-one image's optional SABnzbd: set
	// only there, whether or not it's switched on. With it switched off SABnzbdURL is
	// empty, and the bundled SABnzbd's client is switched off to match.
	AIOSABnzbdURL string
	// LibraryDir is the root of the organized media library (imports land here) and the
	// default scan root for every module.
	LibraryDir string
	// Per-library roots. Each defaults to its own subfolder of LibraryDir (movies, tvshows,
	// ebooks, audiobooks, music) so the types don't share one directory. Point them anywhere
	// with the matching env var, or in Settings → Library. EbooksDir and AudiobooksDir may be
	// the same path (both books under one folder) or different.
	MoviesDir     string
	TVDir         string
	EbooksDir     string
	AudiobooksDir string
	MusicDir      string
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
	// AIOProwlarrURL is where the all-in-one image's optional Prowlarr answers, set only
	// while it's switched on: a Prowlarr there is Arrmada's own, like the Compose
	// install's arrmada-prowlarr (it's given Arrmada's FlareSolverr).
	AIOProwlarrURL string
	// OpenSubtitles credentials for the Subtitles module (opensubtitles.com REST API).
	// The API key enables search; a free username/password additionally enables download.
	OpenSubtitlesAPIKey   string
	OpenSubtitlesUsername string
	OpenSubtitlesPassword string
	// ConvertScratchDir is the working directory the Convert module encodes into before
	// atomically replacing the original. Ideally a fast local disk. Empty = <DataDir>/convert.
	ConvertScratchDir string
	// SkipMigrationSnapshot upgrades the database without the automatic pre-migrate
	// snapshot. Only for a data disk too full to hold the copy; the app refuses to
	// upgrade otherwise rather than risk the only copy.
	SkipMigrationSnapshot bool
	// AllowNewerSchema starts the app on a database a newer Arrmada has upgraded, which
	// it otherwise refuses (old code on a newer schema can damage it). A last resort.
	AllowNewerSchema bool
	// Channel is which Arrmada this install's updates bring (ARRMADA_CHANNEL, written by
	// install.sh and update.sh): "stable" (the published releases) or "main" (the newest
	// code, compiled on the server). "" when it isn't set (a plain `go run`, or a container
	// from before the setting). The update check compares the build with the same thing.
	Channel string
}

// The folder each library defaults to inside LibraryDir. Nothing in the app makes them:
// a missing library folder may be a share that isn't mounted, so the app never creates
// one on its own (Create it in Settings → Library does, on request).
const (
	SubdirMovies     = "movies"
	SubdirTV         = "tvshows"
	SubdirEbooks     = "ebooks"
	SubdirAudiobooks = "audiobooks"
	SubdirMusic      = "music"
)

// Load builds a Config from environment variables, falling back to defaults.
// Every key is prefixed ARRMADA_ so it never collides with other services.
func Load() (Config, error) {
	c := Config{
		Host:                  env("ARRMADA_HOST", "0.0.0.0"),
		BaseURLIgnored:        ignoredBaseURL(os.Getenv("ARRMADA_BASE_URL")),
		DataDir:               env("ARRMADA_DATA_DIR", "./data"),
		DatabaseBackend:       strings.ToLower(env("ARRMADA_DATABASE_BACKEND", "sqlite")),
		PostgresURL:           os.Getenv("ARRMADA_POSTGRES_URL"),
		PostgresSchema:        os.Getenv("ARRMADA_POSTGRES_SCHEMA"),
		PostgresBinDir:        os.Getenv("ARRMADA_POSTGRES_BIN"),
		LogLevel:              strings.ToLower(env("ARRMADA_LOG_LEVEL", "info")),
		ExternalHeader:        env("ARRMADA_EXTERNAL_HEADER", "Cf-Connecting-Ip"),
		QbittorrentURL:        env("ARRMADA_QBITTORRENT_URL", ""),
		AIOQbittorrentURL:     env("ARRMADA_AIO_QBITTORRENT_URL", ""),
		QbittorrentLogin:      env("ARRMADA_QBITTORRENT_LOGIN", ""),
		SABnzbdURL:            env("ARRMADA_SABNZBD_URL", ""),
		SABnzbdConfig:         env("ARRMADA_SABNZBD_CONFIG", "/bundled/sabnzbd/sabnzbd.ini"),
		AIOSABnzbdURL:         env("ARRMADA_AIO_SABNZBD_URL", ""),
		LibraryDir:            env("ARRMADA_LIBRARY_DIR", "./library"),
		DownloadCategory:      env("ARRMADA_DOWNLOAD_CATEGORY", "arrmada"),
		DownloadsDir:          env("ARRMADA_DOWNLOADS_DIR", "./downloads"),
		RecycleDir:            env("ARRMADA_RECYCLE_DIR", ""),
		FlaresolverrURL:       env("ARRMADA_FLARESOLVERR_URL", ""),
		TMDBAPIKey:            env("ARRMADA_TMDB_API_KEY", ""),
		OMDBAPIKey:            env("ARRMADA_OMDB_API_KEY", ""),
		ProwlarrURL:           env("ARRMADA_PROWLARR_URL", ""),
		AIOProwlarrURL:        env("ARRMADA_AIO_PROWLARR_URL", ""),
		OpenSubtitlesAPIKey:   env("ARRMADA_OPENSUBTITLES_API_KEY", ""),
		OpenSubtitlesUsername: env("ARRMADA_OPENSUBTITLES_USERNAME", ""),
		OpenSubtitlesPassword: env("ARRMADA_OPENSUBTITLES_PASSWORD", ""),
		ConvertScratchDir:     env("ARRMADA_CONVERT_SCRATCH_DIR", ""),
		PlexURL:               env("ARRMADA_PLEX_URL", ""),
		PlexToken:             env("ARRMADA_PLEX_TOKEN", ""),
		GeoIPDB:               env("ARRMADA_GEOIP_DB", ""),
	}
	if c.DatabaseBackend != "sqlite" && c.DatabaseBackend != "postgres" {
		return Config{}, fmt.Errorf("ARRMADA_DATABASE_BACKEND must be sqlite or postgres")
	}
	if c.DatabaseBackend == "postgres" && (c.PostgresURL == "" || c.PostgresSchema == "") {
		return Config{}, fmt.Errorf("PostgreSQL requires ARRMADA_POSTGRES_URL and ARRMADA_POSTGRES_SCHEMA; SQLite fallback is disabled")
	}
	max, err := strconv.Atoi(env("ARRMADA_POSTGRES_MAX_CONNECTIONS", "4"))
	if err != nil || max < 1 || max > 100 {
		return Config{}, fmt.Errorf("ARRMADA_POSTGRES_MAX_CONNECTIONS must be an integer from 1 to 100")
	}
	c.PostgresMaxConnections = max

	// Per-library roots default to their own subfolder of the shared library dir.
	//
	// They used to default to the library root ITSELF, which meant every unset type shared
	// one folder: movies, TV, books and music all writing into the same directory, and each
	// module's scan walking the others' files. A conventional per-type subfolder is what
	// people set these to by hand anyway, so it's the better default — and each is still
	// overridable by env var or in Settings → Library.
	c.MoviesDir = env("ARRMADA_MOVIES_DIR", filepath.Join(c.LibraryDir, SubdirMovies))
	c.TVDir = env("ARRMADA_TV_DIR", filepath.Join(c.LibraryDir, SubdirTV))
	c.EbooksDir = env("ARRMADA_EBOOKS_DIR", filepath.Join(c.LibraryDir, SubdirEbooks))
	c.AudiobooksDir = env("ARRMADA_AUDIOBOOKS_DIR", filepath.Join(c.LibraryDir, SubdirAudiobooks))
	c.MusicDir = env("ARRMADA_MUSIC_DIR", filepath.Join(c.LibraryDir, SubdirMusic))

	// The all-in-one image sets its own (empty while its qBittorrent is switched off).
	if c.QbittorrentLogin == "" && c.AIOQbittorrentURL == "" {
		c.QbittorrentLogin = filepath.Join(c.DataDir, "qbittorrent-login.json")
	}

	port, err := strconv.Atoi(env("ARRMADA_PORT", "7878"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid ARRMADA_PORT: %w", err)
	}
	c.Port = port

	// Optional; ignore a blank/garbage value (0 = don't manage the client's port).
	c.QbittorrentPort, _ = strconv.Atoi(env("ARRMADA_QBIT_PORT", "0"))

	// Anything that isn't clearly "yes" keeps the snapshot: skipping it is the risky
	// choice, so a typo shouldn't make it.
	c.SkipMigrationSnapshot, _ = strconv.ParseBool(env("ARRMADA_SKIP_MIGRATION_SNAPSHOT", "false"))
	c.AllowNewerSchema, _ = strconv.ParseBool(env("ARRMADA_ALLOW_NEWER_SCHEMA", "false"))
	c.AllowRemoteSetup, _ = strconv.ParseBool(env("ARRMADA_ALLOW_REMOTE_SETUP", "false"))
	// Anything else (a typo) is treated as unset: it only steers what the update check
	// compares with, never what runs.
	switch ch := strings.ToLower(strings.TrimSpace(env("ARRMADA_CHANNEL", ""))); ch {
	case "stable", "main":
		c.Channel = ch
	}

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

// ignoredBaseURL returns a leftover ARRMADA_BASE_URL worth warning about. Empty and "/"
// both meant the root, which is where Arrmada runs, so they need no warning.
func ignoredBaseURL(v string) string {
	v = strings.TrimSpace(v)
	if strings.Trim(v, "/") == "" {
		return ""
	}
	return v
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
