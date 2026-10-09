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
	"syscall"
	"time"

	// The scratch image carries no tzdata package, so every named zone failed to load
	// and time.Now() silently stayed UTC — which made the convert encode window run on
	// UTC hours while the user was entering local ones. A "01:00-03:30" overnight window
	// became 11:00-13:30 local: nothing ran overnight, and a parked worker logs nothing,
	// so it looked like scheduling was simply broken. Embedding the database (~450 KB)
	// makes TZ=Australia/Sydney and friends resolve without an OS package.
	_ "time/tzdata"

	"github.com/tristenlammi/arrmada/internal/apikeys"
	"github.com/tristenlammi/arrmada/internal/applog"
	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/backup"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/buildinfo"
	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/connstatus"
	"github.com/tristenlammi/arrmada/internal/convert"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/flaresolverr"
	"github.com/tristenlammi/arrmada/internal/geoip"
	"github.com/tristenlammi/arrmada/internal/health"
	"github.com/tristenlammi/arrmada/internal/httpapi"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/libroots"
	"github.com/tristenlammi/arrmada/internal/listening"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/notify"
	"github.com/tristenlammi/arrmada/internal/outbox"
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
	// `arrmada <command>` runs one maintenance command and exits (cli.go). Checked before
	// the logger and everything else, so arguments can never start a second server.
	if len(os.Args) > 1 {
		os.Exit(runCLI(os.Args[1:]))
	}

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
	// Once, before the old lines are read back: take book, author and search details out
	// of request lines earlier versions wrote. Reported after Persist, so the warning
	// lands in the log it's about.
	scrubErr := scrubLegacyLogs(logPath)
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
	)
	if cfg.BaseURLIgnored != "" {
		// Sub-path hosting was never finished (the web app always asked for /api/v1 at the
		// root), so the option is gone. Say so rather than refuse to start: the app still
		// works, just at the root of the hostname.
		log.Warn("ARRMADA_BASE_URL is no longer supported and is ignored — serve Arrmada at the root of its own hostname (it is running at the root)",
			"ARRMADA_BASE_URL", cfg.BaseURLIgnored)
	}
	if restored > 0 {
		log.Info("restored logs from the previous run", "lines", restored, "path", logPath)
	}
	if scrubErr != nil {
		log.Warn("couldn't scrub audiobook details from older log files; will retry next start", "err", scrubErr)
	}
	logEnvironment(log, cfg)

	st, err := store.OpenWith(cfg.DataDir, store.Options{
		Log:                   log,
		SkipMigrationSnapshot: cfg.SkipMigrationSnapshot,
		AllowNewerSchema:      cfg.AllowNewerSchema,
	})
	if err != nil {
		log.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()
	log.Info("database ready", "data_dir", cfg.DataDir)
	// Settings are read once, here, and served from memory from now on. Starting without
	// them would mean running on defaults the owner never chose (the music library going
	// to the env folder, a module switching itself back on), so an unreadable settings
	// table stops the boot.
	settingsSvc, err := settings.Open(context.Background(), st.DB())
	if err != nil {
		log.Error("failed to load settings", "err", err)
		os.Exit(1)
	}
	// One-time: converted files used to have " AV1" / " x265" appended to their recorded
	// release, which read back as the old codec and hid the group. Rewrite those in place.
	if _, err := convert.RepairCodecStamps(context.Background(), st.DB(), settingsSvc, log); err != nil {
		log.Warn("convert: codec stamp repair failed", "err", err)
	}
	// One-time: files converted before their pre-conversion baseline was recorded get it
	// from the Convert ledger, so upgrades can't undo those conversions either.
	if _, err := convert.BackfillConvertedFrom(context.Background(), st.DB(), settingsSvc, log); err != nil {
		log.Warn("convert: pre-conversion baseline backfill failed", "err", err)
	}

	bus := eventbus.New(log)
	// What must happen after an import (Convert and Subtitles reindexing, the requester's
	// "ready", the audiobook catalogue) is written here with the import and run until it
	// succeeds. The bus above is only for the UI and admin alerts. Consumers are
	// registered further down, before the scheduler starts.
	box := outbox.New(st.DB(), log)

	// Background work stops when runCtx is cancelled during shutdown. Every long-running
	// loop and one-off goroutine below runs in grp: a panic in one is logged with its stack
	// and contained (a loop restarts, backing off), and shutdown names anything that
	// didn't stop. Created before the first background launch so nothing escapes it.
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	grp := safego.NewGroup(runCtx, log)
	// Every contained panic is also announced, so the UI and admin alerts can see that
	// something broke even though the app stayed up.
	safego.SetPanicHook(func(name string) {
		bus.Publish("system.panic", map[string]any{"name": name})
	})
	// The job runner records the work people and events start (searches, scans, imports,
	// Run now) with single-flight per item and class limits. Jobs a crash left unfinished
	// are marked interrupted here.
	jobRunner, err := jobs.New(runCtx, st.DB(), log, bus)
	if err != nil {
		log.Error("failed to start the job runner", "err", err)
		os.Exit(1)
	}
	authSvc := auth.NewService(st.DB())
	authSvc.SetLogger(log)
	// Accounts that differ only by case predate case-insensitive names; say so once.
	authSvc.ReportCaseDuplicates(context.Background())
	// API keys resolve settings-first, env-fallback, so a key added in the settings menu
	// takes effect without a restart while existing env-based setups keep working. Seed
	// the store with any env values so a fresh install with only compose vars still works.
	keyStore := apikeys.NewStore(settingsSvc)
	// FlareSolverr's URL is a setting like the API keys (ARRMADA_FLARESOLVERR_URL as the
	// fallback), read on every use, so changing it needs no restart.
	flare := flaresolverr.NewFunc(keyStore.Func("flaresolverr"))
	indexers := indexer.NewService(st.DB(), log, flare)
	downloads := download.NewService(st.DB(), log)
	// How each indexer and download client has been answering, kept across restarts: a
	// failing indexer backs off for background searches, and the Indexers page and the
	// health panel show it. Each visible change is announced (no names or error text —
	// pages re-read the details through the API).
	connStatus := connstatus.New(st.DB(), log)
	if err := connStatus.Load(context.Background()); err != nil {
		log.Warn("couldn't load saved integration status; starting without it", "err", err)
	}
	connStatus.OnChange(func(s connstatus.State) {
		bus.Publish("integration.status", map[string]any{"kind": s.Kind, "ref": s.Ref, "state": s.Phase(time.Now())})
	})
	indexers.SetStatus(connStatus)
	downloads.SetStatus(connStatus)
	// FlareSolverr answering or not is recorded like any integration (no backoff: the
	// searches that need it are already paced by their indexers' own status).
	flare.OnResult(func(err error) {
		connStatus.Record(connstatus.KindFlareSolverr, "default", connstatus.Outcome{Err: err, Name: "FlareSolverr"})
	})
	// Library folders chosen in the app (first-run setup, Settings → Library) win over the
	// environment's, for everything — importer, qBittorrent save path, disk guard — and
	// they're resolved on every use, so a change applies without a restart. cfg keeps the
	// environment's folders: they're what a blank setting falls back to.
	roots := libroots.New(settingsSvc.Get, cfg)
	rootFuncs := library.RootFuncs{
		Movie:     libroots.Func(roots.Movies),
		TV:        libroots.Func(roots.TV),
		Ebook:     libroots.Func(roots.Ebooks),
		Audiobook: libroots.Func(roots.Audiobooks),
		Music:     libroots.Func(roots.Music),
	}
	httpapi.LogLibraryDirs(context.Background(), roots, cfg, log)
	logFolders(log, roots.Config(context.Background()),
		settingsSvc.GetBool(context.Background(), settings.KeyModuleBooks, true),
		settingsSvc.GetBool(context.Background(), settings.KeyModuleMusic, settings.ModuleMusicDefault))
	// Catalogue answers are kept in SQLite across restarts and served stale while a
	// refresh runs, so Discover opens instantly even right after a deploy.
	diskCache := metadata.NewDiskCache(st.DB())
	tmdb := metadata.NewTMDBFunc(keyStore.Func("tmdb"))
	tmdb.SetDiskCache(diskCache)
	// Discovery region (Settings → API keys): localizes popular/upcoming/genre
	// lists. Read lazily like the key, so changing it needs no restart.
	tmdb.SetRegionFunc(func() string { return settingsSvc.Get(context.Background(), "tmdb_region", "") })
	omdb := metadata.NewOMDbFunc(keyStore.Func("omdb"))
	// Books: Open Library (with a Google Books fallback) out of the box; Hardcover takes
	// over the moment a key is in Settings, with Open Library still reachable on request.
	olProvider := metadata.NewOpenLibrary()
	hardcover := metadata.NewHardcoverFunc(keyStore.Func("hardcover"), olProvider)
	hardcover.SetDiskCache(diskCache)
	openlib := metadata.NewBookSources(hardcover, metadata.NewBooksWithFallback(olProvider, metadata.NewGoogleBooks()))
	qualitySvc := quality.NewServiceWith(st.DB(), settingsSvc)
	// Titles left on a profile deleted before deletes reassigned them already run on the
	// default; point their stored ref there too, so the UI and the database agree. Only
	// refs naming a missing profile are touched.
	if fixed, err := qualitySvc.RepairDanglingRefs(context.Background()); err != nil {
		log.Warn("quality: dangling profile repair failed", "err", err)
	} else if len(fixed) > 0 {
		args := []any{}
		for table, n := range fixed {
			args = append(args, table, n)
		}
		log.Info("quality: repointed titles on deleted profiles to the default", args...)
	}
	notifySvc := notify.NewService(st.DB(), bus, log)
	pushSvc := push.New(st.DB(), settingsSvc, log)
	// Episode NUMBERING comes from TVmaze; everything else about a show still comes from
	// TMDB. TMDB merges two-part episodes into single entries where releases keep them
	// separate, so its numbering drifts from the way files are actually named and every
	// episode after a merged pair lands one slot out. TVmaze follows the release
	// convention, needs no API key, and falls back to TMDB whenever it can't help.
	// Episode numbering: TVDB first (authoritative, matches releases and gives real
	// absolute numbers — but needs a key), then TVmaze (free, handles the common
	// two-parter), then TMDB itself. Each falls back cleanly when it can't help.
	tvdb := metadata.NewTVDB(keyStore.Func("tvdb"))
	tvSeries := metadata.NewSeriesWithEpisodes(tmdb, log,
		tvdb,
		metadata.NewTVmaze(),
	)
	seriesSvc := series.NewService(st.DB(), tvSeries, cfg.TVDir, log)
	seriesSvc.SetRootFunc(rootFuncs.TV)                                   // scans and manual imports follow Settings → Library
	seriesSvc.SetSceneMapper(xem.New(keyStore.Func("flaresolverr"), log)) // TheXEM scene mapping (via FlareSolverr past Cloudflare)
	// The monitoring preset an add or a request uses when it doesn't pick one.
	seriesSvc.SetMonitorDefaultFunc(func(ctx context.Context) string {
		return settingsSvc.Get(ctx, series.KeyMonitorDefault, series.DefaultMonitorPreset)
	})
	booksSvc := books.NewService(st.DB(), openlib, log)
	// Hardcover is the catalogue when a key is set; anything still on Open Library keys
	// is re-matched without being asked. Nothing here merges book rows any more: the old
	// boot-time fold deleted prefix siblings ('Mistborn: The Final Empire' / 'Mistborn:
	// Secret History') along with their audio versions and everyone's listening place.
	// Possible duplicates are flagged on the book's timeline for a person to review.
	grp.Go("books: catalogue upgrade check", func(ctx context.Context) { booksSvc.MaybeSubmitUpgrade(ctx, jobRunner, "system") })
	// MusicBrainz needs no key, the way Open Library needs none for books.
	musicSvc := music.NewService(st.DB(), metadata.NewMusicBrainz(), log)
	// Music is off by default now (it's a preview), but an install already using it must
	// not have it switched off underneath it by an upgrade: an unsaved toggle on a library
	// with artists is pinned on. A saved choice, either way, is never touched.
	if changed, err := settingsSvc.EnsureModuleDefault(context.Background(), settings.KeyModuleMusic, musicSvc.HasArtists); err != nil {
		log.Warn("music: couldn't check whether to keep the Music module on", "err", err)
	} else if changed {
		log.Info("music: kept the Music module on because your library has artists. Switch it off in Settings → System → Modules.")
	}
	// Recycle bins: every delete goes to a hidden .arrmada-recycle folder on the library
	// folder it came from, so it's a rename on the same drive (and Plex ignores the folder).
	// ARRMADA_RECYCLE_DIR is now only an override: a folder makes one bin for everything,
	// "off" hard-deletes. The old shared bin (<library>/.recycle, inside the managed
	// volume) takes only files outside every library folder, and stays listed while it
	// holds anything so it can drain.
	bins := &library.RootBins{
		Roots:  func() []libroots.Root { return roots.Libraries(context.Background()) },
		Legacy: filepath.Join(cfg.LibraryDir, ".recycle"),
		Log:    log,
	}
	switch cfg.RecycleDir {
	case "":
	case "off":
		bins.Off = true
	default:
		bins.Explicit = cfg.RecycleDir
	}
	movieSvc := movies.NewService(st.DB(), tmdb, qualitySvc, cfg.MoviesDir, "", bus, log)
	movieSvc.SetRootFunc(rootFuncs.Movie) // scans, manual imports and renames follow Settings → Library
	movieSvc.SetBin(bins)                 // deletes and replaced files go to the bins
	seriesSvc.SetBin(bins)                // per-episode file deletes go to the recycle bin, like movies
	seriesSvc.SetBus(bus)                 // deletes announce file.removed so imports forget them
	prefs := libPrefs{s: settingsSvc}
	movieSvc.SetNaming(prefs)
	movieSvc.SetPrefs(prefs)
	movieSvc.SetOutbox(box) // an import's file record and its follow-up work land together
	if cfg.QbittorrentURL != "" {
		if err := downloads.EnsureBundled(context.Background(), cfg.QbittorrentURL); err != nil {
			log.Warn("could not register bundled qBittorrent", "err", err)
		}
		// Pin the incoming port to match the Docker-published one. qBittorrent may
		// still be starting, so retry in the background rather than block boot.
		if cfg.QbittorrentPort > 0 {
			grp.Go("qbittorrent: set incoming port", func(ctx context.Context) {
				switch retryBoot(ctx, func(ctx context.Context) error {
					return downloads.SetBundledPort(ctx, cfg.QbittorrentURL, cfg.QbittorrentPort)
				}) {
				case nil:
					log.Info("qBittorrent incoming port set", "port", cfg.QbittorrentPort)
				case errRetriesExhausted:
					log.Warn("could not set qBittorrent incoming port", "port", cfg.QbittorrentPort)
				}
			})
		}
		// Point qBittorrent's default save + incomplete paths at the downloads dir so
		// an existing client (seeded before the dir changed) still lands files on the
		// shared volume. Retry in the background; qBittorrent may still be booting.
		if dl := roots.Downloads(context.Background()); dl != "" {
			grp.Go("qbittorrent: set save path", func(ctx context.Context) {
				switch retryBoot(ctx, func(ctx context.Context) error {
					return downloads.SetBundledSavePath(ctx, cfg.QbittorrentURL, dl)
				}) {
				case nil:
					log.Info("qBittorrent save path set", "path", dl)
				case errRetriesExhausted:
					log.Warn("could not set qBittorrent save path", "path", dl)
				}
			})
		}
		// Size qBittorrent's total-active cap to the per-kind limits so nothing sits
		// "Queued" behind its default cap of 5. Retry; the client may still be booting.
		grp.Go("qbittorrent: reconcile queue limits", func(ctx context.Context) {
			switch retryBoot(ctx, func(ctx context.Context) error {
				return downloads.EnsureBundledQueue(ctx, cfg.QbittorrentURL)
			}) {
			case nil:
				log.Info("qBittorrent queue limits reconciled")
			case errRetriesExhausted:
				log.Warn("could not reconcile qBittorrent queue limits")
			}
		})
	}
	// The coordinator is the "add a movie and walk away" brain: it searches
	// indexers for monitored-but-missing movies, ranks releases, grabs the best,
	// and attaches finished imports back to the movie.
	coordinator := automation.New(movieSvc, indexers, downloads, qualitySvc, st.DB(), bus, log, cfg.DownloadsDir)
	coordinator.SetOutbox(box) // series and book imports queue their follow-up work
	// Every grab's save path and the free-space check read the downloads folder live.
	coordinator.SetDownloadsDirFunc(libroots.Func(roots.Downloads))

	// Deliver grab/import notifications to configured connections.
	grp.Loop("notify", notifySvc.Run)

	// Realtime hub bridges the event bus to connected websocket clients.
	hub := realtime.NewHub(log)
	grp.Loop("realtime hub", func(ctx context.Context) { hub.Run(ctx, bus) })

	appStart := time.Now()
	sched := scheduler.New(log)
	// Each task's last run, result and failure streak survive a restart, so the Tasks
	// table can still say what failed overnight after a deploy.
	sched.SetStore(scheduler.NewSQLStore(st.DB()))
	// Run now is a job: it leaves a jobs row with its outcome, and shutdown handles it
	// like any other job. Scheduled ticks stay out of the jobs table.
	sched.SetJobs(scheduler.ViaJobs(runCtx, jobRunner))
	// Announce finished runs to staff pages — but only the ones worth a refresh (hourly-ish
	// tasks, a result that flipped, a Run now), not every 30-second import sweep. Names
	// and outcome only: the bus reaches every staff client.
	sched.OnFinish(func(ts scheduler.TaskStatus, notable bool) {
		if notable {
			bus.Publish("task.finished", map[string]any{"name": ts.Name, "ok": ts.LastOK, "dur_ms": ts.LastDurationMS})
		}
	})
	sched.Register("prune-expired-sessions", 15*time.Minute, true, func(ctx context.Context) error {
		_, err := st.DB().ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < CURRENT_TIMESTAMP`)
		return err
	}, scheduler.Label("Sign out expired sessions"), scheduler.Description("Removes login sessions that have passed their expiry."))
	// Heartbeat so the realtime channel has something to emit until modules do.
	sched.Register("heartbeat", 10*time.Second, false, func(context.Context) error {
		bus.Publish("server.heartbeat", map[string]any{
			"uptime_seconds": int(time.Since(appStart).Seconds()),
		})
		return nil
	}, scheduler.Hidden())
	// Import finished downloads into the library.
	imports := library.NewManager(st.DB(), "", bus, log)
	imports.SetNaming(prefs)
	// Route each media type to its own library folder, resolved on every import. A type
	// with no folder fails loudly rather than landing in the managed volume.
	imports.SetRootFuncs(rootFuncs)
	// When an import replaces a same-named library file, recycle the old one first
	// (instead of silently overwriting it) — same bin the delete paths use.
	imports.SetBin(bins)
	// Name movie imports from the matched library record (metadata title), not the
	// scene release — deterministic folders that match the movie Arrmada tracks.
	imports.SetTitleResolver(movieTitleResolver{movieSvc})
	// Hold a movie download for admin review when it doesn't match what it was
	// grabbed for (e.g. a wrong film), instead of importing the wrong thing.
	imports.SetGate(coordinator.HoldMovieImport)
	// Blocklist (and clean up) a movie download that finished but has nothing importable,
	// so the 30s import sweep stops retrying it forever.
	imports.SetFailureHook(coordinator.HandleMovieImportFailure)
	imports.SetStuckHook(coordinator.HandleMovieImportStuck)
	// Review's "Retry import" drops the importer's back-off for that download.
	coordinator.SetImportRetry(imports.RetryNow)
	// Attach each finished import to its movie (Wanted → Downloaded) in the same sweep
	// that records it, retrying from the database until it settles — not off a bus event
	// that a busy moment or a restart can lose.
	imports.SetAttach(coordinator.AttachMovieImport)
	// Forget the import behind a deleted movie file as it's deleted, so the torrent that's
	// still seeding isn't imported straight back; a re-grab of the release imports again.
	movieSvc.SetOnFileRemoved(func(ctx context.Context, path string) {
		if err := imports.MarkRemovedByTarget(ctx, path); err != nil {
			log.Warn("forget import failed", "path", path, "err", err)
		}
	})
	// Wire the series module into the coordinator: TV downloads land in a separate
	// category and are hardlinked file-by-file (a season pack yields many episodes).
	bookImporter := library.NewImporter("", log)
	// This importer places TV episodes, book editions and albums, and scans the book
	// folders; every folder is resolved on each use, so Settings → Library applies live.
	bookImporter.SetRootFuncs(rootFuncs)
	bookImporter.SetBin(bins) // replaced files go to the bins here too
	// Name episode files with their metadata title ("<Series> - SxxEyy - <Episode> - <quality>").
	bookImporter.SetEpisodeTitleFunc(func(seriesTitle string, year, season, episode int) string {
		return seriesSvc.EpisodeTitleByName(context.Background(), seriesTitle, year, season, episode)
	})
	bookImporter.SetSeriesNaming(prefs) // user-configurable series folder / season / episode formats
	coordinator.SetSeries(seriesSvc, bookImporter)
	// Books share the importer set above; ebooks land in their own category.
	coordinator.SetBooks(booksSvc)
	// Music shares the same importer; albums land in their own category.
	coordinator.SetMusic(musicSvc)
	// Switching Music off in Settings stops its searches, imports and manual actions on the
	// next cycle; Books keeps its old behaviour (the toggle only hides it) for now.
	coordinator.SetModuleGate(func(ctx context.Context, module string) bool {
		if module == "music" {
			return settingsSvc.GetBool(ctx, settings.KeyModuleMusic, settings.ModuleMusicDefault)
		}
		return true
	})
	// Book file deletion honors the same recycle bin as movies.
	coordinator.SetBin(bins)
	// The stall timeout a profile left at "use the default" falls back to, read each check.
	coordinator.SetStallDefault(func(ctx context.Context) int {
		return automation.ParseStallMinutes(settingsSvc.Get(ctx, automation.KeyStallMinutes, ""))
	})
	// How many upgrades one sweep may grab (Settings → Downloads), read at each sweep.
	coordinator.SetUpgradeBudget(func(ctx context.Context) int {
		return automation.ParseUpgradeBudget(settingsSvc.Get(ctx, automation.KeyUpgradeBudget, ""))
	})
	sched.Register("import-completed", 30*time.Second, false, func(ctx context.Context) error {
		completed, err := downloads.CompletedInCategory(ctx, cfg.DownloadCategory)
		if err != nil {
			// The client being unreachable mustn't hold up imports already on disk that
			// are still waiting to be attached to their movie.
			imports.RetryPendingAttach(ctx)
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
	}, scheduler.Label("Import finished movie downloads"), scheduler.Description("Moves completed movie downloads into the library and attaches them to their movie."))
	// Integration status changes that matter are saved as they happen; this saves the
	// rest (hourly counters, average response times) once a minute.
	sched.Register("integration-status-flush", time.Minute, false, connStatus.Flush,
		scheduler.Label("Save integration status"),
		scheduler.Description("Saves how often each indexer and download client was used and how it answered, for the Indexers page."))
	// Periodically sweep for monitored movies that still have no file and grab them.
	sched.Register("search-missing-movies", 5*time.Minute, false, func(ctx context.Context) error {
		coordinator.SearchMissing(ctx)
		return nil
	}, scheduler.Label("Search for missing movies"), scheduler.Description("Searches the indexers for monitored movies that don't have a file yet."))
	// RSS sync: poll indexer feeds for new uploads matching wanted movies.
	sched.Register("rss-sync", 15*time.Minute, false, func(ctx context.Context) error {
		coordinator.RSSSync(ctx)
		return nil
	}, scheduler.Label("Check indexer feeds for new movies"), scheduler.Description("Reads each indexer's recent uploads and grabs any wanted movie."))
	// Look for better releases for movies that already have a file (upgrades).
	sched.Register("upgrade-movies", 6*time.Hour, false, func(ctx context.Context) error {
		coordinator.UpgradeMovies(ctx)
		return nil
	}, scheduler.Label("Look for better movie releases"), scheduler.Description("Grabs a better release when a movie's quality profile allows upgrades, up to the per-sweep limit (Settings → Downloads)."))
	// Fail over stalled downloads: replace, then remove, after the profile's (or the global
	// default) timeout with no progress.
	sched.Register("detect-stalled", 2*time.Minute, false, func(ctx context.Context) error {
		coordinator.DetectStalled(ctx)
		return nil
	}, scheduler.Label("Replace stalled downloads"), scheduler.Description("Swaps out downloads that have made no progress for too long."))

	// Hold downloads while the downloads volume is too full. A minute is frequent
	// enough to catch a big torrent filling a cache pool, and the check is a statfs
	// plus one queue read, so it costs nothing to run often.
	diskGuard := download.NewDiskGuard(downloads, settingsSvc, log, cfg.DownloadsDir)
	diskGuard.SetDirFunc(libroots.Func(roots.Downloads)) // watches the folder in use now
	sched.Register("downloads-disk-guard", time.Minute, true, diskGuard.Check, scheduler.Label("Watch download disk space"), scheduler.Description("Pauses downloads while the downloads drive is too full."))
	// A torrent the guard paused is waiting for space, not stalled: its clock holds.
	coordinator.SetGuardHeld(diskGuard.Held)
	// Import finished TV downloads (arrmada-tv category): hardlink every episode file
	// out of a completed torrent (season packs yield many) into the library.
	sched.Register("import-series", 30*time.Second, false, func(ctx context.Context) error {
		coordinator.ImportSeriesDownloads(ctx)
		return nil
	}, scheduler.Label("Import finished TV downloads"), scheduler.Description("Moves completed episode and season downloads into the library."))
	// Sweep monitored series for missing, aired episodes and grab packs/episodes.
	sched.Register("search-missing-series", 15*time.Minute, false, func(ctx context.Context) error {
		coordinator.SearchSeriesMissing(ctx)
		return nil
	}, scheduler.Label("Search for missing episodes"), scheduler.Description("Searches the indexers for monitored episodes that have aired but have no file."))
	// Keep continuing shows' episode lists current, so an episode TMDB added after the
	// show was last refreshed exists before its download arrives.
	sched.Register("refresh-continuing-series", 6*time.Hour, false, func(ctx context.Context) error {
		coordinator.RefreshContinuingSeries(ctx)
		return nil
	}, scheduler.Label("Refresh running shows"), scheduler.Description("Updates the episode lists of shows that are still airing."))
	// A show stored as ended can come back; re-check those once a week (checked daily, so
	// each one is at most a day past its week) so a revived show gains its new season.
	sched.Register("refresh-ended-series", 24*time.Hour, false, func(ctx context.Context) error {
		coordinator.RefreshEndedSeries(ctx)
		return nil
	}, scheduler.Label("Re-check ended shows"), scheduler.Description("Once a week, checks whether shows that ended or were cancelled have come back with new episodes."))
	// Sweep monitored, file-less books and grab the best-format release.
	sched.Register("search-missing-books", 30*time.Minute, false, func(ctx context.Context) error {
		coordinator.SearchBooksMissing(ctx)
		return nil
	}, scheduler.Label("Search for missing books"), scheduler.Description("Searches the indexers for monitored books missing a wanted edition."))
	// Sweep monitored albums missing tracks and grab the best release for each.
	sched.Register("search-missing-music", 30*time.Minute, false, func(ctx context.Context) error {
		coordinator.SearchMusicMissing(ctx)
		return nil
	}, scheduler.Label("Search for missing albums"), scheduler.Description("Searches the indexers for monitored albums that are missing tracks."))
	// Import finished album downloads (arrmada-music category).
	sched.Register("import-music", 30*time.Second, false, func(ctx context.Context) error {
		coordinator.ImportMusicDownloads(ctx)
		return nil
	}, scheduler.Label("Import finished music downloads"), scheduler.Description("Moves completed album downloads into the library."))
	// Import finished ebook downloads (arrmada-books category).
	sched.Register("import-books", 30*time.Second, false, func(ctx context.Context) error {
		coordinator.ImportBookDownloads(ctx)
		return nil
	}, scheduler.Label("Import finished book downloads"), scheduler.Description("Moves completed ebook and audiobook downloads into the library."))
	// RSS sync for series: poll indexer feeds for new episodes of running shows.
	sched.Register("rss-sync-series", 15*time.Minute, false, func(ctx context.Context) error {
		coordinator.RSSSyncSeries(ctx)
		return nil
	}, scheduler.Label("Check indexer feeds for new episodes"), scheduler.Description("Reads each indexer's recent uploads and grabs any wanted episode."))
	sched.Register("rss-sync-books", 15*time.Minute, false, func(ctx context.Context) error {
		coordinator.RSSSyncBooks(ctx)
		return nil
	}, scheduler.Label("Check indexer feeds for new books"), scheduler.Description("Reads each indexer's recent uploads and grabs any wanted book."))
	// Look for better releases for episodes that already have a file (upgrades).
	sched.Register("upgrade-series", 6*time.Hour, false, func(ctx context.Context) error {
		coordinator.UpgradeSeries(ctx)
		return nil
	}, scheduler.Label("Look for better episode releases"), scheduler.Description("Grabs a better release when a show's quality profile allows upgrades, up to the per-sweep limit (Settings → Downloads)."))
	// Remove imported torrents once they hit their indexer's seed goal (also on
	// startup, so anything left over from a previous run is tidied promptly).
	sched.Register("manage-seeding", 10*time.Minute, true, func(ctx context.Context) error {
		coordinator.ManageSeeding(ctx)
		return nil
	}, scheduler.Label("Stop seeding finished torrents"), scheduler.Description("Removes imported torrents once they reach their indexer's seeding goal."))
	// Requests module sits on top of Movies/Series: an approval adds the media and
	// triggers a search through the existing acquisition pipeline. Created before
	// sched.Start so its sweep is in the snapshot the scheduler launches.
	requestsSvc := requests.NewService(st.DB(), movieSvc, seriesSvc, booksSvc, coordinator, qualitySvc, bus, notifySvc.AppriseBin(), log)
	requestsSvc.SetPushSender(pushSvc) // Web Push alongside inbox + Apprise
	requestsSvc.SetRunner(grp)         // approval searches stop at shutdown
	requestsSvc.SetJobs(jobRunner)     // and are jobs, single-flight with a Search click
	// Book requests made before they remembered their library row are linked to it by
	// title and author, so the ones whose book was re-matched to a new catalogue key
	// stop showing "Searching" and get their "ready".
	grp.Go("requests: book link backfill", func(ctx context.Context) {
		if _, _, err := requestsSvc.BackfillBookIDs(ctx); err != nil {
			log.Warn("requests: book link backfill failed", "err", err)
		}
	})
	// Backstop for request-ready notifications: catches availability that arrived
	// without an import (a library scan). Idempotent (unique inbox ref), so re-running
	// never double-notifies.
	sched.Register("request-ready-sweep", 10*time.Minute, false, func(ctx context.Context) error {
		return requestsSvc.SweepReadyRequests(ctx)
	}, scheduler.Label("Tell requesters their request is ready"), scheduler.Description("Catches requests that became available without an import, and lets the requester know."))

	// Subtitles module (Bazarr replacement): grabs external SRT sidecars over the
	// Movies/Series catalogs via OpenSubtitles.
	subsProvider := subtitles.NewOpenSubtitlesFunc(keyStore.Func("opensubtitles_api"), keyStore.Func("opensubtitles_username"), keyStore.Func("opensubtitles_password"))
	subtitlesSvc := subtitles.NewService(st.DB(), movieSvc, seriesSvc, settingsSvc, subsProvider, "ffmpeg", "ffprobe", filepath.Join(cfg.DataDir, "whisper"), log)
	grp.Loop("subtitles: worker", subtitlesSvc.Run) // subtitle-ensure job worker
	// The library pass behind the Subtitles Overview/Library pages. The worker runs the
	// first one at startup; this keeps it from going stale.
	sched.Register("subtitles-library-scan", 6*time.Hour, false, func(ctx context.Context) error {
		subtitlesSvc.Rescan(ctx)
		return nil
	}, scheduler.Label("Check the library for subtitles"), scheduler.Description("Refreshes which movies and episodes have the subtitles they need."))
	sched.Register("subtitles-auto-grab", 6*time.Hour, false, func(ctx context.Context) error {
		subtitlesSvc.AutoGrab(ctx)
		return nil
	}, scheduler.Label("Download missing subtitles"), scheduler.Description("Fetches subtitles for anything still missing a wanted language."))

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
	convertSvc := convert.NewService(st.DB(), movieSvc, seriesSvc, settingsSvc, "ffmpeg", "ffprobe", convertScratch, "", log)
	convertSvc.SetBin(bins) // originals go to the bin on their own library folder
	// Upgrade decisions judge a library file by what Convert's analysis found in it, not
	// only by its release name — the same facts the Library fit bars read.
	coordinator.SetFileFacts(convertSvc)
	// The manager looks after every bin: one per library folder (or the one override),
	// plus the old shared bin while it still holds files, so what's in it stays listed,
	// restorable, aged and capped until it drains. Convert's originals go to the bins, so
	// it has to know how much room is left under the cap: an original that doesn't fit
	// would make Enforce purge it (and everything older) within the hour.
	recycleSvc := recyclebin.NewBins(bins.All, settingsSvc, log)
	convertSvc.SetBinHeadroom(recycleSvc.Headroom)
	grp.Loop("convert: runner", convertSvc.Run)
	// Warm the probe cache off the request path so the first Convert page load after
	// a restart is instant instead of re-analyzing the whole library, then build the
	// library index the Convert list reads (both are incremental — see migration 0058).
	grp.Go("convert: warm cache and index", func(ctx context.Context) {
		convertSvc.WarmCache(ctx)
		convertSvc.IndexAll(ctx)
	})
	// Keep the Convert library index current. Imports reindex just their own series, so
	// this only catches changes made outside Arrmada; it ticks hourly but sweeps once a
	// day at the admin-configured time (Settings → Convert).
	// Warm the Discover rows (movies, TV and books) at startup and every six hours, so
	// the page never waits on an upstream — the disk cache serves what's here while the
	// refresh runs. Books rows go through Hardcover's one-a-second pacing, which is why
	// this runs in the background rather than on first open.
	sched.Register("discover-warm", 6*time.Hour, true, func(ctx context.Context) error {
		if tmdb.Available() {
			tmdb.WarmGenres(ctx) // genre names for the cards; loaded once, kept a day
			_, _ = tmdb.Trending(ctx, "all")
			for _, m := range []string{"movie", "series"} {
				_, _ = tmdb.Popular(ctx, m)
				_, _ = tmdb.Upcoming(ctx, m)
				_, _ = tmdb.TopRated(ctx, m)
				_, _ = tmdb.HiddenGems(ctx, m)
			}
			_, _ = tmdb.NowPlaying(ctx)
			_, _ = tmdb.Anime(ctx)
		}
		for _, kind := range []string{metadata.BrowseTrending, metadata.BrowseNewReleases, metadata.BrowseTopRated, metadata.BrowsePopular} {
			_, _ = booksSvc.Browse(ctx, kind)
		}
		_, _ = booksSvc.Recommended(ctx)
		return nil
	}, scheduler.Label("Pre-load Discover rows"), scheduler.Description("Refreshes the Discover rows in the background so the page opens instantly."))
	sched.Register("convert-index", time.Hour, false, func(ctx context.Context) error {
		convertSvc.MaybeIndexSweep(ctx)
		return nil
	}, scheduler.Label("Index the library for Convert"), scheduler.Description("Keeps Convert's list of files current; the full sweep runs once a day at the set time."))

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
	grp.Loop("insights: poller", insightsSvc.Run) // Plex watch-monitoring poller (records when enabled + configured)
	// Convert pauses its encodes while someone is watching.
	convertSvc.SetWatching(insightsSvc.Watching)
	// Prune raw bandwidth samples older than 90 days: the poller writes one row per
	// cycle, so at the 5s default that's ~17k/day and every graph query scans them all.
	// Watch history itself is kept — only the high-frequency bandwidth series is rolled off.
	sched.Register("insights-prune-bandwidth", 24*time.Hour, true, func(ctx context.Context) error {
		n, err := insightsSvc.PruneBandwidth(ctx, time.Now().Add(-90*24*time.Hour))
		if err == nil && n > 0 {
			log.Info("insights: pruned old bandwidth samples", "rows", n)
		}
		return err
	}, scheduler.Label("Prune old bandwidth samples"), scheduler.Description("Removes Plex bandwidth samples older than 90 days."))

	// Recycle bin: enforce the user's size/age guard rails on a schedule (and once at startup).
	sched.Register("recycle-enforce", time.Hour, true, func(ctx context.Context) error {
		recycleSvc.Enforce(ctx)
		return nil
	}, scheduler.Label("Tidy the recycle bin"), scheduler.Description("Keeps the recycle bin within its size and age limits."))
	// Originals of merged audiobooks kept while the bin is off go after 14 days.
	sched.Register("book-merge-backup-prune", 24*time.Hour, true, coordinator.PruneMergeBackups, scheduler.Label("Remove old audiobook merge backups"), scheduler.Description("Deletes originals kept from audiobook merges after 14 days."))

	// Database backups: checked hourly, taken once a night after the configured hour (and
	// at once after a long downtime), newest 7 kept in <data>/backups.
	backupSvc := backup.New(st, settingsSvc, log)
	sched.Register("db-backup", time.Hour, true, backupSvc.RunNightly, scheduler.Label("Back up the database"), scheduler.Description("Takes the nightly database backup and keeps the newest seven."))
	// The jobs table keeps two weeks of finished work, at most 5000 rows.
	sched.Register("jobs-prune", 24*time.Hour, true, jobRunner.PruneDefault,
		scheduler.Label("Tidy the job history"), scheduler.Description("Removes finished background jobs older than 14 days."))

	// Audiobook server: listening apps (Lissen and other Audiobookshelf clients) connect to
	// its own port. Off until an admin switches it on in Books → Audiobook server.
	listenStore := listening.NewStore(st.DB())
	audioSrv := audioserver.New(audioserver.Options{
		DB: st.DB(), Books: booksSvc, Listen: listenStore, Users: authSvc, Settings: settingsSvc,
		Log: log, FFprobe: "ffprobe", DataDir: cfg.DataDir,
	})
	audioPort := os.Getenv("ARRMADA_AUDIOBOOK_LISTEN_PORT")
	if audioPort == "" {
		audioPort = "13378"
	}
	audioMgr := audioserver.NewManager(audioSrv, ":"+audioPort, log)
	audioMgr.Apply(settingsSvc.GetBool(context.Background(), audioserver.KeyEnabled, false))
	sched.Register("audioserver-prune", 24*time.Hour, false, func(ctx context.Context) error {
		return listenStore.Prune(ctx)
	}, scheduler.Label("Tidy audiobook server records"), scheduler.Description("Removes old audiobook server records."))
	sched.Register("audioserver-warm", 30*time.Minute, true, func(ctx context.Context) error {
		if settingsSvc.GetBool(ctx, audioserver.KeyEnabled, false) {
			if n := audioSrv.Warm(ctx); n > 0 {
				log.Info("audiobook server: read chapters and durations", "audiobooks", n)
			}
		}
		return nil
	}, scheduler.Label("Read audiobook chapters"), scheduler.Description("Reads chapters and durations for new audiobooks so listening apps start quickly."))

	// Health checks run in the background on their own intervals (the health-check task
	// below drives them); the health endpoint serves their cached results. httpapi.New
	// registers the checks built from its deps.
	healthReg := health.NewRegistry(bus, log)

	// Everything that acts on an import is an outbox consumer. Registered here, once every
	// consumer exists and before the scheduler starts the import sweeps (and before the
	// HTTP server takes manual imports): Enqueue writes rows only for the consumers
	// registered at that moment. Rows a previous run left unfinished run as soon as the
	// dispatcher starts.
	importConsumers{
		convert: convertSvc, subtitles: subtitlesSvc, requests: requestsSvc, audio: audioSrv, grp: grp,
	}.register(box)
	grp.Loop("outbox: dispatcher", box.Run)
	// Finished rows are kept a week for anyone tracing what happened to an import; failed
	// ones stay until someone retries them.
	sched.Register("outbox-prune", 24*time.Hour, true, func(ctx context.Context) error {
		n, err := box.Prune(ctx, 7*24*time.Hour)
		if err == nil && n > 0 {
			log.Info("outbox: pruned finished rows", "rows", n)
		}
		return err
	}, scheduler.Label("Tidy the import outbox"), scheduler.Description("Removes finished import follow-ups older than a week; failed ones stay until retried."))
	// The scheduler starts last: every task above, and the import sweeps in particular,
	// must only run once the outbox has its consumers.
	sched.Start(runCtx)

	// The API key Test: one live request each, on demand only (never in health polling).
	keyVerifiers := map[string]func(context.Context, string) (string, error){
		"tmdb":              tmdb.VerifyKey,
		"tvdb":              tvdb.VerifyKey,
		"omdb":              omdb.VerifyKey,
		"opensubtitles_api": subsProvider.Verify,
	}
	restartCh := make(chan struct{}, 1)
	srv := httpapi.New(httpapi.Deps{
		Config:       cfg,
		Log:          log,
		Store:        st,
		Bus:          bus,
		Auth:         authSvc,
		Realtime:     hub,
		Indexers:     indexers,
		Downloads:    downloads,
		DiskGuard:    diskGuard,
		Library:      imports,
		Movies:       movieSvc,
		Quality:      qualitySvc,
		Settings:     settingsSvc,
		Automation:   coordinator,
		Notify:       notifySvc,
		Series:       seriesSvc,
		Requests:     requestsSvc,
		Discovery:    tmdb,
		Ratings:      omdb,
		Books:        booksSvc,
		Music:        musicSvc,
		Subtitles:    subtitlesSvc,
		Convert:      convertSvc,
		Insights:     insightsSvc,
		Push:         pushSvc,
		Recycle:      recycleSvc,
		Logs:         logRing,
		APIKeys:      keyStore,
		FlareSolverr: flare,
		KeyVerifiers: keyVerifiers,
		AudioServer:  audioSrv,
		AudioManager: audioMgr,
		Restart: func() {
			select {
			case restartCh <- struct{}{}:
			default:
			}
		},
		// Safety copies before destructive admin actions (user delete). Newest 3 of each
		// kind are kept in <data>/backups; deleting an account with no listening data is a
		// kind of its own, so it can't push out a copy that holds someone's places.
		Snapshot: func(ctx context.Context, kind string) (string, error) {
			b, err := backupSvc.Create(ctx, store.BackupKind(kind))
			if err != nil {
				return "", err
			}
			return filepath.Join(backupSvc.Dir(), b.Name), nil
		},
		RunGroup:  grp,
		Backups:   backupSvc,
		Health:    healthReg,
		Scheduler: sched,
		Jobs:      jobRunner,
		// Everything else reads the folders live; the bundled qBittorrent's default save
		// path is the one thing that has to be told. Same retries as at boot — the client
		// may be restarting — and each try reads the folder afresh, so of two quick saves
		// the later one wins.
		OnFoldersChanged: func(ctx context.Context, changed []string) {
			for _, name := range changed {
				if name != "downloads" || cfg.QbittorrentURL == "" {
					continue
				}
				var dl string
				switch retryBoot(ctx, func(ctx context.Context) error {
					dl = roots.Downloads(ctx)
					if dl == "" {
						return nil // nothing to point it at; grabs pass no save path either
					}
					return downloads.SetBundledSavePath(ctx, cfg.QbittorrentURL, dl)
				}) {
				case nil:
					log.Info("qBittorrent save path moved to the new Downloads folder", "path", dl)
				case errRetriesExhausted:
					log.Warn("could not move qBittorrent's save path to the new Downloads folder — grabs still pass it with each torrent", "path", dl)
				}
			}
			bus.Publish("system.folders.applied", map[string]any{"changed": changed})
		},
	})
	// A task that keeps failing is a health problem too (it warns at three in a row).
	healthReg.Register(health.TasksFailingCheck(func() []health.TaskState {
		var out []health.TaskState
		for _, t := range sched.Snapshot() {
			if t.Hidden {
				continue
			}
			out = append(out, health.TaskState{Name: t.Name, Label: t.Label, LastError: t.LastError, ConsecutiveFailures: int(t.ConsecutiveFailures)})
		}
		return out
	}))
	sched.Register("health-check", 30*time.Second, true, healthReg.RunDue,
		scheduler.Label("Check system health"), scheduler.Description("Keeps the health list in Settings → Status current, running each check on its own schedule."))

	errCh := make(chan error, 1)
	grp.Go("http server", func(context.Context) {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	})

	log.Info("Arrmada is online", "url", displayURL(cfg))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		log.Error("server failed", "err", err)
		os.Exit(1)
	case sig := <-stop:
		log.Info("shutdown requested", "signal", sig.String())
	case <-restartCh:
		// Give the HTTP response a moment to reach the browser, then shut down; Docker's
		// restart policy starts the container again with the new settings applied.
		time.Sleep(500 * time.Millisecond)
		log.Info("restarting to apply new settings")
	}

	// Docker kills the container ten seconds after asking it to stop (the compose files
	// set no stop_grace_period), so the steps below share a budget that fits inside it.
	cancelRun() // signal background jobs (and request-started work) to stop
	audioMgr.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}

	// Let in-flight jobs drain, then name whatever ignored the cancel: those are the
	// ones Docker's kill will cut short.
	// Everything was cancelled at the same moment, so these waits overlap: their sum is
	// the worst case, and it stays inside Docker's window.
	clean := true
	if left := jobRunner.Shutdown(2 * time.Second); len(left) > 0 {
		clean = false
		log.Warn("shutdown: jobs still running were recorded as cancelled", "jobs", left)
	}
	if busy := sched.WaitFor(time.Second); len(busy) > 0 {
		clean = false
		log.Warn("shutdown: scheduled tasks still running", "tasks", busy)
	}
	if left := grp.Wait(time.Second); len(left) > 0 {
		clean = false
		log.Warn("shutdown: background work still running", "count", len(left), "names", summarizeNames(left, 20))
	}
	// The last minute of integration counters, which the flush task would have saved.
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), 2*time.Second)
	if err := connStatus.Flush(flushCtx); err != nil {
		log.Warn("shutdown: couldn't save integration status", "err", err)
	}
	cancelFlush()
	if clean {
		log.Info("stopped cleanly")
	} else {
		log.Info("stopped")
	}
}

// errRetriesExhausted is retryBoot giving up after every attempt failed.
var errRetriesExhausted = errors.New("retries exhausted")

// retryBoot retries a boot-time call to the bundled qBittorrent, which may still be
// starting: up to 20 tries three seconds apart. It returns nil on success,
// errRetriesExhausted when every try failed, or ctx's error when shutdown cut it short.
func retryBoot(ctx context.Context, fn func(ctx context.Context) error) error {
	for i := 0; i < 20; i++ {
		if fn(ctx) == nil {
			return nil
		}
		t := time.NewTimer(3 * time.Second)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	return errRetriesExhausted
}

// summarizeNames collapses repeated names ("media backfill ×12") and keeps the first max,
// so a shutdown with hundreds of stragglers still logs one readable line.
func summarizeNames(names []string, max int) []string {
	counts := map[string]int{}
	var order []string
	for _, n := range names {
		if counts[n] == 0 {
			order = append(order, n)
		}
		counts[n]++
	}
	out := make([]string, 0, len(order))
	for _, n := range order {
		if len(out) == max {
			out = append(out, fmt.Sprintf("…and %d more", len(order)-max))
			break
		}
		if c := counts[n]; c > 1 {
			n = fmt.Sprintf("%s ×%d", n, c)
		}
		out = append(out, n)
	}
	return out
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

// scrubLegacyMarker records that the one-time audiobook scrub has run.
const scrubLegacyMarker = ".scrub-audiobook-v1"

// scrubLegacyLogs rewrites log files written before request logging switched to route
// patterns, so no line on disk still names a book someone opened, played or bookmarked,
// or what they searched for. It runs once: a marker beside the logs stops it repeating,
// and is only written when the rewrite succeeded. Copies of old lines in Docker's own
// stdout log aren't reachable from here; recreating the container drops them.
func scrubLegacyLogs(logPath string) error {
	dir := filepath.Dir(logPath)
	marker := filepath.Join(dir, scrubLegacyMarker)
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if err := applog.RewriteFiles(logPath, audioserver.ScrubLegacyEntry); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
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
	base := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	return slog.New(applog.NewHandler(base, logRing))
}

// displayURL builds a clickable local URL, swapping a wildcard bind address for
// localhost so the logged link actually works.
func displayURL(cfg config.Config) string {
	host := cfg.Host
	if host == "0.0.0.0" || host == "" || host == "::" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s:%d", host, cfg.Port)
}
