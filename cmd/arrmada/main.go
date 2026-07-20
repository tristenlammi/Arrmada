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

	"github.com/tristenlammi/arrmada/internal/apikeys"
	"github.com/tristenlammi/arrmada/internal/applog"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/buildinfo"
	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/convert"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/geoip"
	"github.com/tristenlammi/arrmada/internal/httpapi"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/notify"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/realtime"
	"github.com/tristenlammi/arrmada/internal/recyclebin"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/scheduler"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
	"github.com/tristenlammi/arrmada/internal/subtitles"
	"github.com/tristenlammi/arrmada/internal/xem"
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
func (p libPrefs) SeriesNaming() library.SeriesNaming {
	ctx := context.Background()
	return library.SeriesNaming{
		Folder:       p.s.Get(ctx, "naming_series_folder", library.DefaultSeriesFolder),
		SeasonFolder: p.s.Get(ctx, "naming_series_season", library.DefaultSeasonFolder),
		EpisodeFile:  p.s.Get(ctx, "naming_series_episode", library.DefaultEpisodeFile),
	}
}
func (p libPrefs) WriteNFO() bool { return p.s.GetBool(context.Background(), "write_nfo", false) }
func (p libPrefs) DownloadArtwork() bool {
	return p.s.GetBool(context.Background(), "download_artwork", false)
}

// movieTitleResolver names movie imports from the matched library record (its
// metadata title/year) instead of the scene release, so folders are deterministic
// and match the movie Arrmada tracks.
type movieTitleResolver struct{ svc *movies.Service }

func (r movieTitleResolver) ResolveMovie(ctx context.Context, name string) (string, int, bool) {
	m, ok := r.svc.MatchRelease(ctx, name)
	if !ok {
		return "", 0, false
	}
	return m.Title, m.Year, true
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	log := newLogger(cfg.LogLevel)

	// Persist the log to disk and re-seed the in-memory view from the previous run, so
	// the Logs page still has history after a restart — which is exactly when you go
	// looking, since an update or a crash is usually what sent you there.
	logPath := filepath.Join(cfg.DataDir, "logs", "arrmada.log.jsonl")
	restored := logRing.Restore(logPath)
	stopLogFile, logFileErr := logRing.Persist(logPath)
	if logFileErr != nil {
		// Not fatal: the in-memory view and stdout both still work.
		log.Warn("logs will not be persisted to disk", "path", logPath, "err", logFileErr)
	} else {
		defer stopLogFile()
	}

	log.Info("starting Arrmada",
		"version", buildinfo.Version,
		"commit", buildinfo.Commit,
		"addr", cfg.Addr(),
		"base_url", orRoot(cfg.BaseURL),
	)
	if restored > 0 {
		log.Info("restored logs from the previous run", "lines", restored, "path", logPath)
	}

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
	settingsSvc := settings.NewService(st.DB())
	// API keys resolve settings-first, env-fallback, so a key added in the settings menu
	// takes effect without a restart while existing env-based setups keep working. Seed
	// the store with any env values so a fresh install with only compose vars still works.
	keyStore := apikeys.NewStore(settingsSvc)
	tmdb := metadata.NewTMDBFunc(keyStore.Func("tmdb"))
	omdb := metadata.NewOMDbFunc(keyStore.Func("omdb"))
	// Open Library primary, Google Books fallback — a book/author OL can't resolve still gets found.
	openlib := metadata.NewBooksWithFallback(metadata.NewOpenLibrary(), metadata.NewGoogleBooks())
	qualitySvc := quality.NewService(st.DB())
	notifySvc := notify.NewService(st.DB(), bus, log)
	// Episode NUMBERING comes from TVmaze; everything else about a show still comes from
	// TMDB. TMDB merges two-part episodes into single entries where releases keep them
	// separate, so its numbering drifts from the way files are actually named and every
	// episode after a merged pair lands one slot out. TVmaze follows the release
	// convention, needs no API key, and falls back to TMDB whenever it can't help.
	tvSeries := metadata.NewSeriesWithEpisodes(tmdb, metadata.NewTVmaze(), log)
	seriesSvc := series.NewService(st.DB(), tvSeries, cfg.TVDir, log)
	seriesSvc.SetSceneMapper(xem.New(cfg.FlaresolverrURL, log)) // TheXEM scene mapping (via FlareSolverr past Cloudflare)
	booksSvc := books.NewService(st.DB(), openlib, log)
	// Recycle bin: default to <library>/.recycle so deletes are undoable; "off" hard-deletes.
	recycleDir := cfg.RecycleDir
	switch recycleDir {
	case "":
		recycleDir = filepath.Join(cfg.LibraryDir, ".recycle")
	case "off":
		recycleDir = ""
	}
	movieSvc := movies.NewService(st.DB(), tmdb, qualitySvc, cfg.MoviesDir, recycleDir, bus, log)
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
		// Point qBittorrent's default save + incomplete paths at the downloads dir so
		// an existing client (seeded before the dir changed) still lands files on the
		// shared volume. Retry in the background; qBittorrent may still be booting.
		if cfg.DownloadsDir != "" {
			go func() {
				for i := 0; i < 20; i++ {
					if err := downloads.SetBundledSavePath(context.Background(), cfg.QbittorrentURL, cfg.DownloadsDir); err == nil {
						log.Info("qBittorrent save path set", "path", cfg.DownloadsDir)
						return
					}
					time.Sleep(3 * time.Second)
				}
				log.Warn("could not set qBittorrent save path", "path", cfg.DownloadsDir)
			}()
		}
		// Size qBittorrent's total-active cap to the per-kind limits so nothing sits
		// "Queued" behind its default cap of 5. Retry; the client may still be booting.
		go func() {
			for i := 0; i < 20; i++ {
				if err := downloads.EnsureBundledQueue(context.Background(), cfg.QbittorrentURL); err == nil {
					log.Info("qBittorrent queue limits reconciled")
					return
				}
				time.Sleep(3 * time.Second)
			}
			log.Warn("could not reconcile qBittorrent queue limits")
		}()
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
	// Route each media type to its own library folder (movies/TV/ebooks/audiobooks);
	// unset dirs fall back to LibraryDir, so a single-library setup is unchanged.
	imports.SetRoots(cfg.MoviesDir, cfg.TVDir, cfg.EbooksDir, cfg.AudiobooksDir)
	// Name movie imports from the matched library record (metadata title), not the
	// scene release — deterministic folders that match the movie Arrmada tracks.
	imports.SetTitleResolver(movieTitleResolver{movieSvc})
	// Hold a movie download for admin review when it doesn't match what it was
	// grabbed for (e.g. a wrong film), instead of importing the wrong thing.
	imports.SetGate(coordinator.HoldMovieImport)
	// Blocklist (and clean up) a movie download that finished but has nothing importable,
	// so the 30s import sweep stops retrying it forever.
	imports.SetFailureHook(coordinator.HandleMovieImportFailure)
	// Forget import records when files are deleted, so a re-grab re-imports.
	go imports.WatchDeletions(runCtx)
	// Wire the series module into the coordinator: TV downloads land in a separate
	// category and are hardlinked file-by-file (a season pack yields many episodes).
	bookImporter := library.NewImporter(cfg.LibraryDir, log)
	bookImporter.SetBookRoots(cfg.EbooksDir, cfg.AudiobooksDir)                       // scan ebooks + audiobooks (may be one folder)
	bookImporter.SetRoots(cfg.MoviesDir, cfg.TVDir, cfg.EbooksDir, cfg.AudiobooksDir) // this importer places TV episodes + book editions
	// Name episode files with their metadata title ("<Series> - SxxEyy - <Episode> - <quality>").
	bookImporter.SetEpisodeTitleFunc(func(seriesTitle string, year, season, episode int) string {
		return seriesSvc.EpisodeTitleByName(context.Background(), seriesTitle, year, season, episode)
	})
	bookImporter.SetSeriesNaming(prefs) // user-configurable series folder / season / episode formats
	coordinator.SetSeries(seriesSvc, bookImporter)
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
	requestsSvc := requests.NewService(st.DB(), movieSvc, seriesSvc, booksSvc, coordinator, qualitySvc, bus, notifySvc.AppriseBin(), log)
	go requestsSvc.RunNotifier(runCtx) // alert requesters when their request is imported

	// Subtitles module (Bazarr replacement): grabs external SRT sidecars over the
	// Movies/Series catalogs via OpenSubtitles.
	subsProvider := subtitles.NewOpenSubtitlesFunc(keyStore.Func("opensubtitles_api"), keyStore.Func("opensubtitles_username"), keyStore.Func("opensubtitles_password"))
	subtitlesSvc := subtitles.NewService(st.DB(), movieSvc, seriesSvc, settingsSvc, subsProvider, "ffmpeg", "ffprobe", filepath.Join(cfg.DataDir, "whisper"), log)
	go subtitlesSvc.Run(runCtx) // subtitle-ensure job worker
	sched.Register("subtitles-auto-grab", 6*time.Hour, false, func(ctx context.Context) error {
		subtitlesSvc.AutoGrab(ctx)
		return nil
	})

	// Convert module (Tdarr replacement): GPU/CPU transcoding over the Movies library.
	convertScratch := cfg.ConvertScratchDir
	if convertScratch == "" {
		// Default to the image's /transcode mount point (put a fast SSD/NVMe pool
		// there in compose) so the heavy encode stays off the array; fall back to
		// appdata only if /transcode isn't present.
		if _, err := os.Stat("/transcode"); err == nil {
			convertScratch = "/transcode"
		} else {
			convertScratch = filepath.Join(cfg.DataDir, "convert")
		}
	}
	convertSvc := convert.NewService(st.DB(), movieSvc, seriesSvc, settingsSvc, "ffmpeg", "ffprobe", convertScratch, recycleDir, log)
	go convertSvc.Run(runCtx)
	// Warm the probe cache off the request path so the first Convert page load after
	// a restart is instant instead of re-analyzing the whole library, then build the
	// library index the Convert list reads (both are incremental — see migration 0058).
	go func() {
		convertSvc.WarmCache(runCtx)
		convertSvc.IndexAll(runCtx)
	}()
	// Keep the Convert library index current. Imports reindex just their own series, so
	// this only catches changes made outside Arrmada; it ticks hourly but sweeps once a
	// day at the admin-configured time (Settings → Convert).
	sched.Register("convert-index", time.Hour, false, func(ctx context.Context) error {
		convertSvc.MaybeIndexSweep(ctx)
		return nil
	})
	// A finished import reindexes only that show, so a new episode is convertible
	// immediately without re-walking the whole library.
	coordinator.SetSeriesImportedHook(func(ctx context.Context, seriesID int64) {
		if err := convertSvc.IndexSeries(ctx, seriesID); err != nil {
			log.Warn("convert: reindex after import failed", "series_id", seriesID, "err", err)
		}
	})
	// Auto-convert sweep. Hourly, not 12-hourly: Sweep only queues while inside the encode
	// window, so two daily ticks against a typical few-hour window usually landed outside it
	// and auto-convert silently never fired. Re-queueing is cheap now that jobs are deduped,
	// and waitForWindow gates the actual encoding regardless.
	sched.Register("convert-sweep", time.Hour, false, func(ctx context.Context) error {
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
	insightsSvc := insights.NewService(st.DB(), settingsSvc, geoResolver, bus, log)
	insightsSvc.SeedFromEnv(runCtx, cfg.PlexURL, cfg.PlexToken)
	go insightsSvc.Run(runCtx) // Plex watch-monitoring poller (records when enabled + configured)

	// Recycle bin: enforce the user's size/age guard rails on a schedule (and once at startup).
	recycleSvc := recyclebin.New(recycleDir, settingsSvc, log)
	sched.Register("recycle-enforce", time.Hour, true, func(ctx context.Context) error {
		recycleSvc.Enforce(ctx)
		return nil
	})

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
		Recycle:    recycleSvc,
		Logs:       logRing,
		APIKeys:    keyStore,
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

// logRing captures recent logs for the in-app Logs viewer (also written to stdout and,
// once Persist is attached, to <DataDir>/logs).
//
// 50,000 lines rather than 5,000. A busy sweep emits a line per page per indexer per
// query, so real-world traffic runs tens of thousands of lines a day and the old buffer
// held roughly two hours — routinely too short to still contain the thing you came to
// look at. At ~200 bytes an entry this is ~10 MB of memory, which is cheap next to
// losing the evidence. The on-disk files hold considerably more than the ring does.
var logRing = applog.NewRing(50000)

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
	base := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	return slog.New(applog.NewHandler(base, logRing))
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
