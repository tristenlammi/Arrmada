// Command arrmada is the single entrypoint for the Arrmada media-automation
// server. M0: config → logger → HTTP server (API + embedded UI) → graceful
// shutdown. Everything else (DB, scheduler, modules) hangs off this.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/buildinfo"
	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/convert"
	"github.com/tristenlammi/arrmada/internal/geoip"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/httpapi"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/notify"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/realtime"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/scheduler"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
	"github.com/tristenlammi/arrmada/internal/subtitles"
)

// libPrefs adapts the settings store to the library/movies preference interfaces
// (naming scheme, .nfo/artwork writing), read fresh so changes apply live.
type libPrefs struct{ s *settings.Service }

func (p libPrefs) Naming() library.Naming {
	ctx := context.Background()
	return library.Naming{
		Folder: p.s.Get(ctx, "naming_movie_folder", library.DefaultMovieFolder),
		File:   p.s.Get(ctx, "naming_movie_file", library.DefaultMovieFile),
	}
}
func (p libPrefs) WriteNFO() bool { return p.s.GetBool(context.Background(), "write_nfo", true) }
func (p libPrefs) DownloadArtwork() bool {
	return p.s.GetBool(context.Background(), "download_artwork", true)
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	log := newLogger(cfg.LogLevel)
	log.Info("starting Arrmada",
		"version", buildinfo.Version,
		"commit", buildinfo.Commit,
		"addr", cfg.Addr(),
		"base_url", orRoot(cfg.BaseURL),
	)

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()
	log.Info("database ready", "data_dir", cfg.DataDir)

	bus := eventbus.New(log)
	authSvc := auth.NewService(st.DB())
	indexers := indexer.NewService(st.DB(), log, cfg.FlaresolverrURL)
	downloads := download.NewService(st.DB(), log)
	tmdb := metadata.NewTMDB(cfg.TMDBAPIKey)
	omdb := metadata.NewOMDb(cfg.OMDBAPIKey)
	// Open Library primary, Google Books fallback — a book/author OL can't resolve still gets found.
	openlib := metadata.NewBooksWithFallback(metadata.NewOpenLibrary(), metadata.NewGoogleBooks())
	qualitySvc := quality.NewService(st.DB())
	settingsSvc := settings.NewService(st.DB())
	notifySvc := notify.NewService(st.DB(), bus, log)
	seriesSvc := series.NewService(st.DB(), tmdb, cfg.LibraryDir, log)
	booksSvc := books.NewService(st.DB(), openlib, log)
	// Recycle bin: default to <library>/.recycle so deletes are undoable; "off" hard-deletes.
	recycleDir := cfg.RecycleDir
	switch recycleDir {
	case "":
		recycleDir = filepath.Join(cfg.LibraryDir, ".recycle")
	case "off":
		recycleDir = ""
	}
	movieSvc := movies.NewService(st.DB(), tmdb, qualitySvc, cfg.LibraryDir, recycleDir, bus, log)
	seriesSvc.SetRecycleDir(recycleDir) // per-episode file deletes go to the recycle bin, like movies
	prefs := libPrefs{s: settingsSvc}
	movieSvc.SetNaming(prefs)
	movieSvc.SetPrefs(prefs)
	if cfg.QbittorrentURL != "" {
		if err := downloads.EnsureBundled(context.Background(), cfg.QbittorrentURL); err != nil {
			log.Warn("could not register bundled qBittorrent", "err", err)
		}
		// Pin the incoming port to match the Docker-published one. qBittorrent may
		// still be starting, so retry in the background rather than block boot.
		if cfg.QbittorrentPort > 0 {
			go func() {
				for i := 0; i < 20; i++ {
					if err := downloads.SetBundledPort(context.Background(), cfg.QbittorrentURL, cfg.QbittorrentPort); err == nil {
						log.Info("qBittorrent incoming port set", "port", cfg.QbittorrentPort)
						return
					}
					time.Sleep(3 * time.Second)
				}
				log.Warn("could not set qBittorrent incoming port", "port", cfg.QbittorrentPort)
			}()
		}
	}
	if !cfg.AuthEnabled {
		log.Warn("authentication is DISABLED — every request runs as a local admin; set ARRMADA_AUTH_ENABLED=true before exposing to a network")
	}

	// The coordinator is the "add a movie and walk away" brain: it searches
	// indexers for monitored-but-missing movies, ranks releases, grabs the best,
	// and attaches finished imports back to the movie.
	coordinator := automation.New(movieSvc, indexers, downloads, qualitySvc, st.DB(), bus, log, cfg.DownloadsDir)

	// Background jobs stop when runCtx is cancelled during shutdown.
	runCtx, cancelRun := context.WithCancel(context.Background())

	// Attach finished imports to their movies (Wanted → Downloaded).
	go coordinator.WatchImports(runCtx)

	// Deliver grab/import notifications to configured connections.
	go notifySvc.Run(runCtx)

	// Realtime hub bridges the event bus to connected websocket clients.
	hub := realtime.NewHub(log)
	go hub.Run(runCtx, bus)

	appStart := time.Now()
	sched := scheduler.New(log)
	sched.Register("prune-expired-sessions", 15*time.Minute, true, func(ctx context.Context) error {
		_, err := st.DB().ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < CURRENT_TIMESTAMP`)
		return err
	})
	// Heartbeat so the realtime channel has something to emit until modules do.
	sched.Register("heartbeat", 10*time.Second, false, func(context.Context) error {
		bus.Publish("server.heartbeat", map[string]any{
			"uptime_seconds": int(time.Since(appStart).Seconds()),
		})
		return nil
	})
	// Import finished downloads into the library.
	imports := library.NewManager(st.DB(), cfg.LibraryDir, bus, log)
	imports.SetNaming(prefs)
	// Forget import records when files are deleted, so a re-grab re-imports.
	go imports.WatchDeletions(runCtx)
	// Wire the series module into the coordinator: TV downloads land in a separate
	// category and are hardlinked file-by-file (a season pack yields many episodes).
	coordinator.SetSeries(seriesSvc, library.NewImporter(cfg.LibraryDir, log))
	// Books share the importer set above; ebooks land in their own category.
	coordinator.SetBooks(booksSvc)
	// Book file deletion honors the same recycle bin as movies.
	coordinator.SetRecycleDir(recycleDir)
	sched.Register("import-completed", 30*time.Second, false, func(ctx context.Context) error {
		completed, err := downloads.CompletedInCategory(ctx, cfg.DownloadCategory)
		if err != nil {
			return err
		}
		cands := make([]library.Candidate, 0, len(completed))
		for _, it := range completed {
			cands = append(cands, library.Candidate{
				Hash: it.Hash, Name: it.Name, ContentPath: it.ContentPath, Category: it.Category,
			})
		}
		imports.Process(ctx, cands)
		return nil
	})
	// Periodically sweep for monitored movies that still have no file and grab them.
	sched.Register("search-missing-movies", 5*time.Minute, false, func(ctx context.Context) error {
		coordinator.SearchMissing(ctx)
		return nil
	})
	// RSS sync: poll indexer feeds for new uploads matching wanted movies.
	sched.Register("rss-sync", 15*time.Minute, false, func(ctx context.Context) error {
		coordinator.RSSSync(ctx)
		return nil
	})
	// Look for better releases for movies that already have a file (upgrades).
	sched.Register("upgrade-movies", 6*time.Hour, false, func(ctx context.Context) error {
		coordinator.UpgradeMovies(ctx)
		return nil
	})
	// Fail over stalled downloads (blocklist + re-search) per each profile's timeout.
	sched.Register("detect-stalled", 2*time.Minute, false, func(ctx context.Context) error {
		coordinator.DetectStalled(ctx)
		return nil
	})
	// Import finished TV downloads (arrmada-tv category): hardlink every episode file
	// out of a completed torrent (season packs yield many) into the library.
	sched.Register("import-series", 30*time.Second, false, func(ctx context.Context) error {
		coordinator.ImportSeriesDownloads(ctx)
		return nil
	})
	// Sweep monitored series for missing, aired episodes and grab packs/episodes.
	sched.Register("search-missing-series", 15*time.Minute, false, func(ctx context.Context) error {
		coordinator.SearchSeriesMissing(ctx)
		return nil
	})
	// Sweep monitored, file-less books and grab the best-format release.
	sched.Register("search-missing-books", 30*time.Minute, false, func(ctx context.Context) error {
		coordinator.SearchBooksMissing(ctx)
		return nil
	})
	// Import finished ebook downloads (arrmada-books category).
	sched.Register("import-books", 30*time.Second, false, func(ctx context.Context) error {
		coordinator.ImportBookDownloads(ctx)
		return nil
	})
	// RSS sync for series: poll indexer feeds for new episodes of running shows.
	sched.Register("rss-sync-series", 15*time.Minute, false, func(ctx context.Context) error {
		coordinator.RSSSyncSeries(ctx)
		return nil
	})
	sched.Register("rss-sync-books", 15*time.Minute, false, func(ctx context.Context) error {
		coordinator.RSSSyncBooks(ctx)
		return nil
	})
	// Look for better releases for episodes that already have a file (upgrades).
	sched.Register("upgrade-series", 6*time.Hour, false, func(ctx context.Context) error {
		coordinator.UpgradeSeries(ctx)
		return nil
	})
	// Remove imported torrents once they hit their indexer's seed goal (also on
	// startup, so anything left over from a previous run is tidied promptly).
	sched.Register("manage-seeding", 10*time.Minute, true, func(ctx context.Context) error {
		coordinator.ManageSeeding(ctx)
		return nil
	})
	sched.Start(runCtx)

	// Requests module sits on top of Movies/Series: an approval adds the media and
	// triggers a search through the existing acquisition pipeline.
	requestsSvc := requests.NewService(st.DB(), movieSvc, seriesSvc, booksSvc, coordinator, qualitySvc, log)

	// Subtitles module (Bazarr replacement): grabs external SRT sidecars over the
	// Movies/Series catalogs via OpenSubtitles.
	subsProvider := subtitles.NewOpenSubtitles(cfg.OpenSubtitlesAPIKey, cfg.OpenSubtitlesUsername, cfg.OpenSubtitlesPassword)
	subtitlesSvc := subtitles.NewService(movieSvc, seriesSvc, settingsSvc, subsProvider, log)
	sched.Register("subtitles-auto-grab", 6*time.Hour, false, func(ctx context.Context) error {
		subtitlesSvc.AutoGrab(ctx)
		return nil
	})

	// Convert module (Tdarr replacement): GPU/CPU transcoding over the Movies library.
	convertScratch := cfg.ConvertScratchDir
	if convertScratch == "" {
		convertScratch = filepath.Join(cfg.DataDir, "convert")
	}
	convertSvc := convert.NewService(st.DB(), movieSvc, settingsSvc, "ffmpeg", "ffprobe", convertScratch, recycleDir, log)
	go convertSvc.Run(runCtx)
	// Nightly sweep: run auto-enabled convert rules.
	sched.Register("convert-sweep", 12*time.Hour, false, func(ctx context.Context) error {
		convertSvc.Sweep(ctx)
		return nil
	})

	// Insights (Plex watch monitoring — Tautulli replacement).
	geoDB := cfg.GeoIPDB
	if geoDB == "" { // auto-detect a GeoLite2 DB dropped in the data dir
		if p := filepath.Join(cfg.DataDir, "GeoLite2-City.mmdb"); func() bool { _, err := os.Stat(p); return err == nil }() {
			geoDB = p
		}
	}
	geoResolver := geoip.New(geoDB)
	insightsSvc := insights.NewService(st.DB(), settingsSvc, geoResolver, log)
	insightsSvc.SeedFromEnv(runCtx, cfg.PlexURL, cfg.PlexToken)
	go insightsSvc.Run(runCtx) // Plex watch-monitoring poller (records when enabled + configured)

	srv := httpapi.New(httpapi.Deps{
		Config:     cfg,
		Log:        log,
		Store:      st,
		Bus:        bus,
		Auth:       authSvc,
		Realtime:   hub,
		Indexers:   indexers,
		Downloads:  downloads,
		Library:    imports,
		Movies:     movieSvc,
		Quality:    qualitySvc,
		Settings:   settingsSvc,
		Automation: coordinator,
		Notify:     notifySvc,
		Series:     seriesSvc,
		Requests:   requestsSvc,
		Discovery:  tmdb,
		Ratings:    omdb,
		Books:      booksSvc,
		Subtitles:  subtitlesSvc,
		Convert:    convertSvc,
		Insights:   insightsSvc,
	})

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	log.Info("Arrmada is online", "url", displayURL(cfg))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		log.Error("server failed", "err", err)
		os.Exit(1)
	case sig := <-stop:
		log.Info("shutdown requested", "signal", sig.String())
	}

	cancelRun() // signal background jobs to stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}

	sched.Wait() // let in-flight jobs drain
	log.Info("stopped cleanly")
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}

func orRoot(base string) string {
	if base == "" {
		return "/"
	}
	return base
}

// displayURL builds a clickable local URL, swapping a wildcard bind address for
// localhost so the logged link actually works.
func displayURL(cfg config.Config) string {
	host := cfg.Host
	if host == "0.0.0.0" || host == "" || host == "::" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s:%d%s", host, cfg.Port, strings.TrimSuffix(cfg.BaseURL, "/"))
}
