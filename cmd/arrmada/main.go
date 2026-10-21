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
	"slices"
	"strconv"
	"syscall"
	"time"

	// The scratch image carries no tzdata package, so every named zone failed to load
	// and time.Now() silently stayed UTC — which made the convert encode window run on
	// UTC hours while the user was entering local ones. A "01:00-03:30" overnight window
	// became 11:00-13:30 local: nothing ran overnight, and a parked worker logs nothing,
	// so it looked like scheduling was simply broken. Embedding the database (~450 KB)
	// makes TZ=Australia/Sydney and friends resolve without an OS package.
	_ "time/tzdata"

	"github.com/tristenlammi/arrmada/internal/acoustid"
	"github.com/tristenlammi/arrmada/internal/apikeys"
	"github.com/tristenlammi/arrmada/internal/applog"
	"github.com/tristenlammi/arrmada/internal/attention"
	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/backup"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/buildinfo"
	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/connstatus"
	"github.com/tristenlammi/arrmada/internal/convert"
	"github.com/tristenlammi/arrmada/internal/displaytitle"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/flaresolverr"
	"github.com/tristenlammi/arrmada/internal/geoip"
	"github.com/tristenlammi/arrmada/internal/health"
	"github.com/tristenlammi/arrmada/internal/httpapi"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/issues"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/kepub"
	"github.com/tristenlammi/arrmada/internal/libfolders"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/libroots"
	"github.com/tristenlammi/arrmada/internal/listening"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/music/stage"
	"github.com/tristenlammi/arrmada/internal/notify"
	"github.com/tristenlammi/arrmada/internal/outbox"
	"github.com/tristenlammi/arrmada/internal/plexscan"
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
	"github.com/tristenlammi/arrmada/internal/updatecheck"
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
		// Off by default: {year} is then always filled, as it always was.
		YearWhenNeeded: p.s.GetBool(ctx, settings.KeyNamingSeriesYearWhenNeeded, false),
	}
}
func (p libPrefs) AudiobookNaming() library.BookNaming {
	ctx := context.Background()
	return library.BookNaming{
		Folder: p.s.Get(ctx, "naming_audiobook_folder", library.DefaultAudiobookFolder),
		File:   p.s.Get(ctx, "naming_audiobook_file", library.DefaultAudiobookFile),
		Track:  p.s.Get(ctx, "naming_audiobook_track", library.DefaultAudiobookTrack),
	}
}
func (p libPrefs) EbookNaming() library.BookNaming {
	ctx := context.Background()
	return library.BookNaming{
		Folder: p.s.Get(ctx, "naming_ebook_folder", library.DefaultEbookFolder),
		File:   p.s.Get(ctx, "naming_ebook_file", library.DefaultEbookFile),
	}
}
func (p libPrefs) MusicNaming() library.MusicNaming {
	ctx := context.Background()
	return library.MusicNaming{
		Folder:      p.s.Get(ctx, settings.KeyNamingMusicFolder, library.DefaultMusicFolder),
		Track:       p.s.Get(ctx, settings.KeyNamingMusicTrack, library.DefaultMusicTrack),
		Compilation: p.s.Get(ctx, settings.KeyNamingMusicCompilation, library.DefaultMusicCompilationTrack),
	}
}
func (p libPrefs) WriteNFO() bool { return p.s.GetBool(context.Background(), "write_nfo", false) }
func (p libPrefs) DownloadArtwork() bool {
	return p.s.GetBool(context.Background(), "download_artwork", false)
}

// movieTitleResolver names movie imports from the matched library record (its
// metadata title/year) instead of the scene release, so folders are deterministic
// and match the movie Arrmada tracks — and settles the track before the file is placed.
// The download's grab row (by info hash) says which movie and track it was for; one
// Arrmada didn't grab is matched by its release name and routed by its resolution and
// edition.
type movieTitleResolver struct {
	svc   *movies.Service
	grabs interface {
		MovieGrabForHash(ctx context.Context, hash string) (automation.GrabRef, bool)
	}
}

func (r movieTitleResolver) ResolveMovieImport(ctx context.Context, hash, name string) (library.MovieImportName, bool) {
	movieID, versionID, replaces := int64(0), int64(-1), ""
	if r.grabs != nil {
		if g, ok := r.grabs.MovieGrabForHash(ctx, hash); ok {
			movieID, replaces = g.MovieID, g.ReplacesPath
			// A grab for an extra track lands on it. One for the default track routes by
			// resolution and edition, as default-track grabs always have (MOV-12).
			if g.VersionID > 0 {
				versionID = g.VersionID
			}
		}
	}
	if movieID > 0 {
		if n, err := r.svc.ImportNaming(ctx, movieID, versionID, replaces, name); err == nil {
			return n, true
		}
		// The movie the grab was for is gone: match the release like any other.
	}
	m, ok := r.svc.MatchRelease(ctx, name)
	if !ok {
		return library.MovieImportName{}, false
	}
	n, err := r.svc.ImportNaming(ctx, m.ID, -1, "", name)
	if err != nil {
		return library.MovieImportName{}, false
	}
	return n, true
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

	st, err := openConfiguredStore(cfg, log)
	if err != nil {
		log.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()
	log.Info("database ready", "backend", st.Backend(), "data_dir", cfg.DataDir)
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
	// One-time: a download leaving the client used to blocklist its release, and one update
	// failed over hundreds of long-gone grabs at once. Those rows block good releases.
	if _, err := automation.PurgeVanishedBlocks(context.Background(), st.DB(), settingsSvc, modLog(log, "automation")); err != nil {
		log.Warn("automation: vanished-download blocklist clean-up failed", "err", err)
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
	indexers := indexer.NewService(st.DB(), modLog(log, "indexer"), flare)
	indexers.SetBundledProwlarr(cfg.AIOProwlarrURL) // the all-in-one image's, while it runs
	downloads := download.NewService(st.DB(), modLog(log, "download"))
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
	indexers.SetDefaultCategories(func(ctx context.Context) bool {
		return settingsSvc.GetBool(ctx, indexer.KeyDefaultCategories, true)
	})
	// Rows synced from Prowlarr before rows remembered their Prowlarr id get it from their
	// feed address, so they're treated as Prowlarr's (name and address owned by the sync)
	// before the next sync runs.
	if _, err := indexers.BackfillProwlarrIDs(context.Background(), settingsSvc.Get(context.Background(), "prowlarr_url", cfg.ProwlarrURL)); err != nil {
		log.Warn("couldn't link synced indexers to their Prowlarr ids", "err", err)
	}
	downloads.SetStatus(connStatus)
	downloads.SetFlags(settingsSvc) // remembers that the owner removed the bundled qBittorrent
	wireUsenet(indexers, downloads)
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
	// Extra Movies and TV folders (Settings → Library): the folder above stays the
	// default; these sit beside it. roots.Libraries — the recycle bins, Convert's holds,
	// the download client checks, manual import — includes them from here on.
	libFolders := libfolders.New(st.DB(), roots, cfg.DataDir)
	roots.WithExtras(libFolders.Extras)
	// The default Movies and TV folders' own downloads folders are settings (GitHub #11).
	libFolders.SetSettingWriter(settingsSvc.Set)
	rootFuncs := library.RootFuncs{
		Movie:     libroots.Func(roots.Movies),
		TV:        libroots.Func(roots.TV),
		Ebook:     libroots.Func(roots.Ebooks),
		Audiobook: libroots.Func(roots.Audiobooks),
		Music:     libroots.Func(roots.Music),
	}
	// SABnzbd files by Arrmada's categories (the movie one is configurable) and, once a
	// finished job is imported, has its leftover folder deleted only inside Arrmada's
	// downloads folder or SABnzbd's own completed folder — never a library or the database.
	downloads.SetMovieCategory(cfg.DownloadCategory)
	downloads.SetFolders(func(ctx context.Context) download.PathEnv {
		return httpapi.DownloadPathEnv(ctx, roots, cfg.DataDir)
	})
	httpapi.LogLibraryDirs(context.Background(), roots, cfg, log)
	logFolders(log, roots.Config(context.Background()),
		settingsSvc.GetBool(context.Background(), settings.KeyModuleBooks, true),
		settingsSvc.GetBool(context.Background(), settings.KeyModuleMusic, settings.ModuleMusicDefault),
		roots.PerFolderDownloads(context.Background()),
		roots.Extras(context.Background())...)
	// Catalogue answers are kept in SQLite across restarts and served stale while a
	// refresh runs, so Discover opens instantly even right after a deploy.
	diskCache := metadata.NewDiskCache(st.DB())
	tmdb := metadata.NewTMDBFunc(keyStore.Func("tmdb"))
	tmdb.SetDiskCache(diskCache)
	// Discovery region (Settings → API keys): localizes popular/upcoming/genre
	// lists. Read lazily like the key, so changing it needs no restart.
	tmdb.SetRegionFunc(func() string { return settingsSvc.Get(context.Background(), "tmdb_region", "") })
	omdb := metadata.NewOMDbFunc(keyStore.Func("omdb"))
	omdb.SetDiskCache(diskCache) // ratings for a week, so the free key's daily quota lasts
	omdb.SetLogger(log)
	// Books: Open Library (with a Google Books fallback) out of the box; Hardcover takes
	// over the moment a key is in Settings, with Open Library still reachable on request.
	olProvider := metadata.NewOpenLibrary()
	hardcover := metadata.NewHardcoverFunc(keyStore.Func("hardcover"), olProvider)
	hardcover.SetDiskCache(diskCache)
	openlib := metadata.NewBookSources(hardcover, metadata.NewBooksWithFallback(olProvider, metadata.NewGoogleBooks()))
	qualitySvc := quality.NewServiceWith(st.DB(), settingsSvc)
	// Ranking v2's shadow-mode disagreements are logged as well as recorded.
	qualitySvc.SetLogger(log)
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
	// One sensible music profile on an install that has none, once (MUS-14): never a
	// second one, and never again after the owner deletes it.
	if created, err := qualitySvc.EnsureDefaultMusicProfile(context.Background()); err != nil {
		log.Warn("quality: couldn't set up the default music profile", "err", err)
	} else if created {
		log.Info("quality: added the music profile " + quality.DefaultMusicPreset)
	}
	notifySvc := notify.NewService(st.DB(), bus, modLog(log, "notify"))
	pushSvc := push.New(st.DB(), settingsSvc, modLog(log, "notify"))
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
	seriesSvc := series.NewService(st.DB(), tvSeries, cfg.TVDir, modLog(log, "series"))
	seriesSvc.SetRootFunc(rootFuncs.TV)                                   // scans and manual imports follow Settings → Library
	seriesSvc.SetFolders(libFolders)                                      // and every extra TV folder
	seriesSvc.SetSceneMapper(xem.New(keyStore.Func("flaresolverr"), log)) // TheXEM scene mapping (via FlareSolverr past Cloudflare)
	// The monitoring preset an add or a request uses when it doesn't pick one.
	seriesSvc.SetMonitorDefaultFunc(func(ctx context.Context) string {
		return settingsSvc.Get(ctx, series.KeyMonitorDefault, series.DefaultMonitorPreset)
	})
	booksSvc := books.NewService(st.DB(), openlib, modLog(log, "books"))
	// Hardcover is the catalogue when a key is set; anything still on Open Library keys
	// is re-matched without being asked. Nothing here merges book rows any more: the old
	// boot-time fold deleted prefix siblings ('Mistborn: The Final Empire' / 'Mistborn:
	// Secret History') along with their audio versions and everyone's listening place.
	// Possible duplicates are flagged on the book's timeline for a person to review.
	grp.Go("books: catalogue upgrade check", func(ctx context.Context) { booksSvc.MaybeSubmitUpgrade(ctx, jobRunner, "system") })
	// MusicBrainz needs no key, the way Open Library needs none for books. This is the
	// process's only client (an archtest forbids another): every caller shares its cache,
	// and every client shares the one rate limiter. Its address, mirror rate and contact
	// are read from Settings on every call, so a change applies without a restart.
	mbClient := metadata.NewMusicBrainz(st.DB(), func() metadata.MBConfig {
		ctx := context.Background()
		rate, _ := strconv.Atoi(settingsSvc.Get(ctx, settings.KeyMusicBrainzMirrorRate, ""))
		return metadata.MBConfig{
			BaseURL:    settingsSvc.Get(ctx, settings.KeyMusicBrainzURL, ""),
			MirrorRate: rate,
			Contact:    settingsSvc.Get(ctx, settings.KeyMusicBrainzContact, ""),
		}
	})
	mbClient.SetLogger(modLog(log, "music"))
	musicSvc := music.NewService(st.DB(), mbClient, modLog(log, "music"))
	musicSvc.SetSettingsLookup(settingsSvc.Lookup) // the matcher's thresholds, the edition policy
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
	movieSvc := movies.NewService(st.DB(), tmdb, qualitySvc, cfg.MoviesDir, "", bus, modLog(log, "movies"))
	movieSvc.SetRootFunc(rootFuncs.Movie) // scans, manual imports and renames follow Settings → Library
	movieSvc.SetFolders(libFolders)       // and every extra Movies folder
	movieSvc.SetBin(bins)                 // deletes and replaced files go to the bins
	seriesSvc.SetBin(bins)                // per-episode file deletes go to the recycle bin, like movies
	seriesSvc.SetBus(bus)                 // deletes announce file.removed so imports forget them
	// A movie delete never removes or empties another library's folder or a downloads folder.
	movieSvc.SetProtectedDirs(func() []string {
		ctx := context.Background()
		out := []string{cfg.LibraryDir, roots.Downloads(ctx)}
		for _, r := range roots.Libraries(ctx) {
			out = append(out, r.Path)
		}
		for _, d := range roots.PerFolderDownloads(ctx) {
			out = append(out, d.Path)
		}
		return out
	})
	prefs := libPrefs{s: settingsSvc}
	movieSvc.SetNaming(prefs)
	movieSvc.SetPrefs(prefs)
	movieSvc.SetOutbox(box) // an import's file record and its follow-up work land together
	// Every name a title goes by (the import gate's check of a download grabbed for it), and
	// the extra languages whose translated titles match library-wide (Settings → Library →
	// "Also match titles in"; empty by default), read per use so a change needs no restart.
	titleLangs := func() []string {
		return settings.TitleLanguageBases(settingsSvc.Get(context.Background(), settings.KeyTitleLanguages, ""))
	}
	movieSvc.SetTitlesSource(tmdb)
	movieSvc.SetTitleLanguages(titleLangs)
	seriesSvc.SetTitlesSource(tmdb)
	seriesSvc.SetTitleLanguages(titleLangs)
	// Which name the app SHOWS for a film or show (Settings → Library → "Show titles as"),
	// read per use. Presentation only: matching and naming keep the canonical title.
	displayPolicy := func() displaytitle.Policy {
		return settings.DisplayTitlePolicy(func(key string) string { return settingsSvc.Get(context.Background(), key, "") })
	}
	movieSvc.SetDisplayPolicy(displayPolicy)
	seriesSvc.SetDisplayPolicy(displayPolicy)
	// The Unraid all-in-one image runs its qBittorrent inside this container, and it can be
	// switched off there: its client follows that switch, before the tuning below reads it.
	if cfg.AIOQbittorrentURL != "" {
		if err := downloads.SyncBundledSwitch(context.Background(), cfg.AIOQbittorrentURL, cfg.QbittorrentURL == cfg.AIOQbittorrentURL); err != nil {
			log.Warn("could not match the built-in qBittorrent's client to its on/off setting", "err", err)
		}
	}
	// Its optional SABnzbd likewise (it ships switched off, so this is usually nothing).
	if cfg.AIOSABnzbdURL != "" {
		if err := downloads.SyncBundledSwitchOf(context.Background(), download.KindSABnzbd, cfg.AIOSABnzbdURL, cfg.SABnzbdURL == cfg.AIOSABnzbdURL); err != nil {
			log.Warn("could not match the built-in SABnzbd's client to its on/off setting", "err", err)
		}
	}
	if cfg.QbittorrentURL != "" {
		if err := ensureBundledQbit(context.Background(), downloads, cfg); err != nil {
			log.Warn("could not register bundled qBittorrent", "err", err)
		}
		// The Compose bundle's qBittorrent was seeded to let the private Docker networks in
		// without a password: give it one first, then tune it with that login.
		secured := make(chan struct{})
		if isComposeBundleQbit(cfg) {
			grp.Go("qbittorrent: give it a password", func(ctx context.Context) {
				defer close(secured)
				secureBundledQbit(ctx, log, downloads, cfg)
			})
		} else {
			noteUnsecuredBundle(context.Background(), log, downloads, cfg)
			close(secured)
		}
		afterSecured := func(ctx context.Context) bool {
			select {
			case <-secured:
				return true
			case <-ctx.Done():
				return false
			}
		}
		// Pin the incoming port to match the Docker-published one. qBittorrent may
		// still be starting, so retry in the background rather than block boot.
		if cfg.QbittorrentPort > 0 {
			grp.Go("qbittorrent: set incoming port", func(ctx context.Context) {
				if !afterSecured(ctx) {
					return
				}
				tuneBundled(ctx, log, "incoming port", func(ctx context.Context) error {
					return downloads.SetBundledPort(ctx, cfg.QbittorrentPort)
				}, "port", cfg.QbittorrentPort)
			})
		}
		// Point qBittorrent's default save + incomplete paths at the downloads dir so
		// an existing client (seeded before the dir changed) still lands files on the
		// shared volume. Retry in the background; qBittorrent may still be booting.
		if dl := roots.Downloads(context.Background()); dl != "" {
			grp.Go("qbittorrent: set save path", func(ctx context.Context) {
				if !afterSecured(ctx) {
					return
				}
				tuneBundled(ctx, log, "save path", func(ctx context.Context) error {
					return downloads.SetBundledSavePath(ctx, dl)
				}, "path", dl)
			})
		}
		// Size qBittorrent's total-active cap to the per-kind limits so nothing sits
		// "Queued" behind its default cap of 5. Retry; the client may still be booting.
		grp.Go("qbittorrent: reconcile queue limits", func(ctx context.Context) {
			if !afterSecured(ctx) {
				return
			}
			tuneBundled(ctx, log, "queue limits", downloads.EnsureBundledQueue)
		})
	}
	// The coordinator is the "add a movie and walk away" brain: it searches
	// indexers for monitored-but-missing movies, ranks releases, grabs the best,
	// and attaches finished imports back to the movie.
	coordinator := automation.New(movieSvc, indexers, downloads, qualitySvc, st.DB(), bus, modLog(log, "automation"), cfg.DownloadsDir)
	coordinator.SetOutbox(box) // series and book imports queue their follow-up work
	// Every grab's save path and the free-space check read the downloads folder live.
	coordinator.SetDownloadsDirFunc(libroots.Func(roots.Downloads))
	// ...except a torrent for a title whose library folder has its own downloads folder,
	// which saves there, on the library's disk, so its import is a hardlink (GitHub #11).
	coordinator.SetFolderDownloadsFunc(roots.OwnDownloads)
	// Movie grabs are filed under the category the movie import sweep reads, whatever a
	// download client's own settings say.
	coordinator.SetMovieCategory(cfg.DownloadCategory)

	// Admin alerts: the catalog's bus topics become queued deliveries, and the worker
	// sends them (retrying, a few at a time, rows surviving a restart).
	// "This device" alert connections push to their admin's browsers and phones, while
	// that account is still staff.
	notifySvc.SetPusher(pushSvc, func(ctx context.Context, uid int64) bool {
		u, err := authSvc.UserByID(ctx, uid)
		return err == nil && !u.Disabled && u.Role.AtLeast(auth.RoleManager)
	})
	grp.Loop("notify", notifySvc.Run)
	grp.Loop("notify: delivery worker", notifySvc.RunWorker)

	// Realtime hub bridges the event bus to connected websocket clients.
	hub := realtime.NewHub(log)
	grp.Loop("realtime hub", func(ctx context.Context) { hub.Run(ctx, bus) })
	// Live download progress for the bars and the queue's shape (queue.progress and
	// queue.changed, both staff-only), read from the shared queue snapshot and only while
	// a staff browser is connected: a requester's socket can't receive them, so it
	// mustn't cost a client read every two seconds either.
	grp.Loop("queue progress", func(ctx context.Context) {
		download.RunProgress(ctx, downloads, bus, func() bool { return hub.StaffCount() > 0 })
	})

	appStart := time.Now()
	sched := scheduler.New(modLog(log, "scheduler"))
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
	imports := library.NewManager(st.DB(), "", bus, modLog(log, "import"))
	imports.SetNaming(prefs)
	// Route each media type to its own library folder, resolved on every import. A type
	// with no folder fails loudly rather than landing in the managed volume.
	imports.SetRootFuncs(rootFuncs)
	// When an import replaces a same-named library file, recycle the old one first
	// (instead of silently overwriting it) — same bin the delete paths use.
	imports.SetBin(bins)
	// Name movie imports from the matched library record (metadata title), not the
	// scene release — deterministic folders that match the movie Arrmada tracks.
	imports.SetTitleResolver(movieTitleResolver{svc: movieSvc, grabs: coordinator})
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
	bookImporter := library.NewImporter("", modLog(log, "import"))
	// This importer places TV episodes, book editions and albums, and scans the book
	// folders; every folder is resolved on each use, so Settings → Library applies live.
	bookImporter.SetRootFuncs(rootFuncs)
	bookImporter.SetBin(bins) // replaced files go to the bins here too
	// Name episode files with their metadata title ("<Series> - SxxEyy - <Episode> - <quality>").
	bookImporter.SetEpisodeTitleFunc(func(seriesTitle string, year, season, episode int) string {
		return seriesSvc.EpisodeTitleByName(context.Background(), seriesTitle, year, season, episode)
	})
	bookImporter.SetSeriesNaming(prefs) // user-configurable series folder / season / episode formats
	// Folders that season template names are season folders, never a show's own folder,
	// when working out which folder a show lives in (#56).
	seriesSvc.SetSeasonFolderMatcher(bookImporter.IsSeasonFolderName)
	bookImporter.SetBookNaming(prefs) // user-configurable audiobook and ebook folder / file names
	// A new series folder can carry the show's ids ("Show (2022) {tvdb-12345}", the Plex
	// recommended scheme); existing folders are reused as they are.
	bookImporter.SetSeriesIDsFunc(func(seriesTitle string, year int) library.SeriesIDs {
		tmdb, tvdb, imdb, _ := seriesSvc.IDsByName(context.Background(), seriesTitle, year)
		return library.SeriesIDs{TMDB: tmdb, TVDB: tvdb, IMDB: imdb}
	})
	// "Add the year only when it's needed": a new show folder keeps {year} only when another
	// show shares its title (series.FolderYears). Asked only with that setting on.
	folderYears := series.NewFolderYears(seriesSvc)
	bookImporter.SetSeriesYearNeededFunc(func(seriesTitle string, year int, yearless string) bool {
		return folderYears.Needed(context.Background(), seriesTitle, year, yearless)
	})
	coordinator.SetSeries(seriesSvc, bookImporter)
	// Books share the importer set above; ebooks land in their own category.
	coordinator.SetBooks(booksSvc)
	coordinator.SetBookCoverDir(filepath.Join(cfg.DataDir, "covers"))
	// A book rename a crash or power cut stopped part way is rolled back now, before any
	// import, scan or rename touches book files again.
	if n := coordinator.RecoverBookRenames(runCtx); n > 0 {
		log.Warn("rolled back interrupted book renames", "editions", n)
	}
	// Music shares the same importer; albums land in their own category.
	coordinator.SetMusic(musicSvc)
	// Music placement (MUS-33, MUS-34, MUS-36): the naming templates, house tags written in
	// a staging copy (a seeding download is never written), covers from the Cover Art
	// Archive, extras brought along — each read from Settings on every import.
	bookImporter.SetMusicNaming(prefs)
	coordinator.SetMusicPlacement(func(ctx context.Context) automation.MusicPlacementPrefs {
		return automation.MusicPlacementPrefs{
			WriteTags: settingsSvc.GetBool(ctx, settings.KeyMusicWriteTags, settings.MusicWriteTagsDefault),
			Extras:    stage.ParseExtras(settingsSvc.Get(ctx, settings.KeyMusicExtras, stage.DefaultExtras)),
		}
	}, music.NewCAA(), automation.MusicAudioHash)
	// A music import, retag or rename a crash stopped part way is finished or rolled back
	// now, before anything stages music again.
	if rep, err := coordinator.RecoverMusicStaging(runCtx); err != nil {
		log.Error("music: some interrupted staging jobs couldn't be settled — they're kept, nothing was deleted", "err", err, "kept", rep.Kept)
	}
	// AcoustID, the music matcher's second pass (MUS-32): off until the owner sets a key.
	acoustidClient := acoustid.New(keyStore.Func(acoustid.KeyID))
	coordinator.SetMusicFingerprinter(acoustid.NewFingerprinter(acoustidClient))
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
	// The languages automatic book grabs accept (Settings → Library), read per search.
	coordinator.SetBookLanguages(func(ctx context.Context) []string {
		return automation.ParseBookLanguages(settingsSvc.Get(ctx, automation.KeyBookLanguages, automation.DefaultBookLanguages))
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
	// What each Torznab indexer supports (its t=caps), for the Indexers page: read when
	// never read or over a week old. Paced by the per-host throttle; paused indexers wait.
	sched.Register("indexer-caps-refresh", 24*time.Hour, false, indexers.RefreshStaleCaps,
		scheduler.Label("Read indexer capabilities"),
		scheduler.Description("Asks each Torznab indexer which searches and categories it supports, when that's unknown or over a week old."))
	// Prowlarr on autopilot: the same non-destructive sync as the Sync now button, every
	// hour with the saved URL and key, so a tracker added, disabled or removed in Prowlarr
	// reaches Arrmada without anyone remembering to click. It does nothing until one sync
	// has worked (that's what saves the URL and key). The first run waits a minute after
	// start so Prowlarr's own container has time to come up alongside this one.
	sched.Register("prowlarr-sync", time.Hour, false, func(ctx context.Context) error {
		return indexers.ScheduledProwlarrSync(ctx, settingsSvc, func(ctx context.Context) string { return keyStore.Value(ctx, "flaresolverr") })
	}, scheduler.Label("Sync indexers from Prowlarr"),
		scheduler.Description("Brings in indexers added in Prowlarr and turns off ones disabled or removed there."))
	grp.Go("prowlarr: first sync", func(ctx context.Context) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
		if _, _, err := sched.RunNow("prowlarr-sync", scheduler.TriggerSchedule); err != nil && !errors.Is(err, scheduler.ErrBusy) {
			log.Warn("couldn't start the first Prowlarr sync", "err", err)
		}
	})
	// The optional bundled SABnzbd registers itself once its config (with the API key its
	// init container generated) shows up, and gets its categories and folders. Adding it
	// runs the Prowlarr sync, as adding one by hand does: usenet indexers only come in
	// while a usenet client exists.
	var sab *bundledSAB
	if cfg.SABnzbdURL != "" && cfg.SABnzbdConfig != "" {
		sab = &bundledSAB{
			url: cfg.SABnzbdURL, config: cfg.SABnzbdConfig, downloads: downloads,
			downloadsDir: roots.Downloads, log: modLog(log, "download"),
			interval: 30 * time.Second, tuneTries: 60, // five minutes of 5 s retries for a slow start
			added: func() {
				if _, _, err := sched.RunNow("prowlarr-sync", scheduler.TriggerSchedule); err != nil && !errors.Is(err, scheduler.ErrBusy) {
					log.Warn("couldn't start a Prowlarr sync for the bundled SABnzbd", "err", err)
				}
			},
		}
		grp.Go("sabnzbd: bundled", sab.run)
	}
	sched.Register("rss-sync", 15*time.Minute, false, func(ctx context.Context) error {
		coordinator.RSSSync(ctx)
		return nil
	}, scheduler.Label("Check indexer feeds for new movies"), scheduler.Description("Reads each indexer's recent uploads and grabs any wanted movie."))
	// Look for better releases for movies that already have a file (upgrades).
	sched.Register("upgrade-movies", 6*time.Hour, false, func(ctx context.Context) error {
		coordinator.UpgradeMovies(ctx)
		return nil
	}, scheduler.Label("Look for better movie releases"), scheduler.Description("Grabs a better release when a movie's quality profile allows upgrades, up to the per-sweep limit (Settings → Downloads)."))
	// Notice movie files that vanished from disk (MOV-21). Daily, not more often: it's the
	// one periodic job that stats library files, and each look can wake an array disk. It
	// only flags; with the movies folder looking unmounted it flags nothing and warns.
	sched.Register("movies-missing-files", 24*time.Hour, false, func(ctx context.Context) error {
		_, err := movieSvc.CheckMissingFiles(ctx)
		return err
	}, scheduler.Label("Check for missing movie files"), scheduler.Description("Marks movies whose file has vanished from disk as File missing. Never deletes or searches; if the movies folder looks unmounted it marks nothing and raises a health warning."))
	// Fail over stalled downloads: replace, then remove, after the profile's (or the global
	// default) timeout with no progress.
	sched.Register("detect-stalled", 2*time.Minute, false, func(ctx context.Context) error {
		coordinator.DetectStalled(ctx)
		return nil
	}, scheduler.Label("Replace stalled downloads"), scheduler.Description("Swaps out downloads that have made no progress for too long."))
	// Keep each grab's record (phase, progress, last seen) in step with the download client,
	// by info hash. Reads the shared queue snapshot, and writes only rows that changed.
	sched.Register("reconcile-acquisitions", 30*time.Second, false, func(ctx context.Context) error {
		_, err := coordinator.ReconcileAcquisitions(ctx)
		return err
	}, scheduler.Label("Track download progress"), scheduler.Description("Records where each grabbed download has got to in the download client, so a restart keeps stall clocks and progress."))

	// Hold downloads while the downloads volume is too full. A minute is frequent
	// enough to catch a big torrent filling a cache pool, and the check is a statfs
	// plus one queue read, so it costs nothing to run often.
	diskGuard := download.NewDiskGuard(downloads, settingsSvc, modLog(log, "download"), cfg.DownloadsDir)
	diskGuard.SetDirFunc(libroots.Func(roots.Downloads)) // watches the folder in use now
	// ...and every library folder's own downloads folder, each volume on its own.
	diskGuard.SetDirsFunc(func() []string {
		var out []string
		for _, d := range roots.DownloadFolders(context.Background()) {
			out = append(out, d.Path)
		}
		return out
	})
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
	// Fill in catalogue details (description, genres, series, release date, author) for
	// books added in bulk, and keep the rest fresh: 25 a run, about 100 a day, inside the
	// Hardcover budget. meta_checked_at makes it restart-safe.
	sched.Register("refresh-books", 6*time.Hour, false, func(ctx context.Context) error {
		if !settingsSvc.GetBool(ctx, settings.KeyModuleBooks, true) {
			return nil
		}
		n, skipped, err := booksSvc.RefreshDue(ctx, 25)
		if skipped != "" {
			log.Info(fmt.Sprintf("refresh-books: refreshed %d, skipped: %s", n, skipped))
		} else {
			log.Info(fmt.Sprintf("refresh-books: refreshed %d", n))
		}
		return err
	}, scheduler.Label("Refresh book details"), scheduler.Description("Fills in descriptions, genres, series and release dates from the book catalogue, a few books at a time, never-refreshed books first. Holds off when most of the day's Hardcover budget is used."))
	// New books by followed authors: each one's catalogue about once a week, 10 authors a
	// run, inside the Hardcover budget. works_checked_at makes it restart-safe.
	releaseJob := authorReleaseJob{
		check:          booksSvc.CheckAuthorReleases,
		defaultProfile: func(ctx context.Context) string { return qualitySvc.DefaultProfile(ctx, "book") },
		emit:           notifySvc.EmitOnce,
		search:         coordinator.SearchBookNow,
		log:            log,
		now:            time.Now,
	}
	sched.Register("check-author-releases", 6*time.Hour, false, func(ctx context.Context) error {
		if !settingsSvc.GetBool(ctx, settings.KeyModuleBooks, true) {
			return nil
		}
		return releaseJob.run(ctx)
	}, scheduler.Label("Check followed authors for new books"), scheduler.Description("Looks at each followed author's catalogue about once a week and adds their new books, monitored, then searches the ones that are out. The first look at an author only notes what's there. Holds off when most of the day's Hardcover budget is used."))
	// Sweep monitored albums missing tracks and grab the best release for each.
	sched.Register("search-missing-music", 30*time.Minute, false, func(ctx context.Context) error {
		coordinator.SearchMusicMissing(ctx)
		return nil
	}, scheduler.Label("Search for missing albums"), scheduler.Description("Searches the indexers for monitored albums that are missing tracks."))
	// Look for better copies of complete albums, up to each profile's cutoff (MUS-16).
	sched.Register("upgrade-music", 6*time.Hour, false, func(ctx context.Context) error {
		coordinator.UpgradeMusic(ctx)
		return nil
	}, scheduler.Label("Look for better album releases"), scheduler.Description("Grabs a better copy of a complete album when its quality profile allows upgrades and the files are below the profile's top tier. Searches at most 10 albums a run and grabs no more than the per-sweep limit (Settings → Downloads); replaced files go to the recycle bin."))
	// Check recorded music files against the disk (MUS-15). Daily: it stats every recorded
	// file, which can wake array disks. It only marks; it never deletes or forgets a file.
	sched.Register("verify-music-files", 24*time.Hour, false, func(ctx context.Context) error {
		_, err := coordinator.VerifyMusicFiles(ctx)
		if errors.Is(err, automation.ErrModuleOff) {
			return nil
		}
		return err
	}, scheduler.Label("Check music files"), scheduler.Description("Notices music files that vanished from disk or changed outside Arrmada. A file not found twice, a day apart, is marked as gone from disk; it is never downloaded again by itself (Search for missing tracks on the album page). A changed one is measured again. Never deletes anything; if the music folder or a share in it looks unmounted it marks nothing and raises a health warning."))
	// Bring stale MusicBrainz answers up to date, slowly, on the background lane: cached
	// answers are never removed, and a page open never waits on this.
	sched.Register("refresh-music-cache", 6*time.Hour, false, func(ctx context.Context) error {
		if !settingsSvc.GetBool(ctx, settings.KeyModuleMusic, settings.ModuleMusicDefault) {
			return nil
		}
		n, err := mbClient.RefreshStale(ctx, musicCacheRefreshPerRun)
		if n > 0 {
			log.Info("music: refreshed stale MusicBrainz answers", "count", n)
		}
		return err
	}, scheduler.Label("Refresh MusicBrainz answers"), scheduler.Description("Rechecks cached MusicBrainz answers that are past their freshness, at most 300 a run at MusicBrainz's pace. Nothing cached is ever removed; when MusicBrainz is down, Music keeps working from the cache."))
	// Keep followed artists current: new releases, merged entries, missing listings and
	// the upgrade's legacy editions, within a MusicBrainz call budget a run.
	sched.Register("refresh-music-artists", 6*time.Hour, false, func(ctx context.Context) error {
		rep, err := coordinator.RefreshMusicArtists(ctx)
		if rep.Artists+rep.Listings+rep.SinglesSettled+rep.EditionsTried > 0 {
			log.Info("music: refreshed followed artists", "artists", rep.Artists, "unanswered", rep.Unanswered,
				"added", rep.Added, "wanted", rep.Wanted, "searched", rep.Searched, "no_longer_listed", rep.Stale,
				"merged", rep.Merged, "singles_checked", rep.SinglesSettled, "listings", rep.Listings,
				"editions_tried", rep.EditionsTried, "editions_rekeyed", rep.EditionsRekeyed,
				"musicbrainz_calls", rep.Calls, "stopped", rep.Stopped)
		}
		return err
	}, scheduler.Label("Refresh followed artists"), scheduler.Description("Rereads each followed artist's albums from MusicBrainz about once a day (through the cache, at most 200 requests a run) and adds new releases: wanted when the artist's New releases setting says so, older music only listed. Searches new releases that are out, fills missing track listings, and moves a few upgraded albums a run onto their real MusicBrainz edition. Nothing is removed when MusicBrainz is down."))
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
	requestsSvc := requests.NewService(st.DB(), movieSvc, seriesSvc, booksSvc, coordinator, qualitySvc, bus, notifySvc.AppriseBin(), modLog(log, "requests"))
	// Every download whose import keeps failing — the movie importer's retry records and
	// the series sweep's — read by request cards ("being checked"), the Activity queue's
	// Finishing stages and the Needs-you feed alike.
	importFailures := func() []library.FailureInfo {
		return append(imports.Failures(), coordinator.SeriesImportFailures()...)
	}
	requestsSvc.SetImportFailures(importFailures)
	requestsSvc.SetPushSender(pushSvc) // Web Push alongside inbox + Apprise
	requestsSvc.SetRunner(grp)         // approval searches stop at shutdown
	requestsSvc.SetJobs(jobRunner)     // and are jobs, single-flight with a Search click
	// A grab moves an open request from Searching to Downloading: its views hear so at once.
	coordinator.SetGrabbed(requestsSvc.NudgeItem)
	// Staff's personal Apprise URLs keep the ordinary rules; everyone else's are held off
	// internal hosts, re-checked before each send. A lookup failure counts as not staff.
	requestsSvc.SetStaffLookup(func(ctx context.Context, uid int64) bool {
		u, err := authSvc.UserByID(ctx, uid)
		return err == nil && !u.Disabled && u.Role.AtLeast(auth.RoleManager)
	})
	// A new request waiting for approval reaches staff: the "New request" alert on the
	// admin's connections (queued exactly once), and each manager's and admin's inbox and
	// Web Push — skipping a push the admin's own "This device" connection already sends.
	requestsSvc.SetStaffAlerts(requests.StaffAlerts{
		Emit: func(ctx context.Context, key, dedupe string, data map[string]any) error {
			_, err := notifySvc.EmitOnce(ctx, key, dedupe, data)
			return err
		},
		Staff:          authSvc.StaffIDs,
		PushedByAlerts: notifySvc.PushUsersFor,
		// Alerts name people by their Plex or Jellyfin name, never the sign-in name.
		Name: func(ctx context.Context, uid int64) string {
			if u, err := authSvc.UserByID(ctx, uid); err == nil {
				return notify.PersonName(u.PlexUsername, u.JellyfinUsername)
			}
			return ""
		},
	})
	// Request limits: a person's own (Settings → Users → edit), else the global ones
	// (Settings → Users → Request limits). Unlimited until the owner sets one.
	requestsSvc.SetQuotaLimits(func(ctx context.Context, uid int64) (requests.Limits, error) {
		own, err := authSvc.Quota(ctx, uid)
		if err != nil {
			return requests.Limits{}, err
		}
		global := requests.GlobalLimits(func(key string) string { return settingsSvc.Get(ctx, key, "") })
		return requests.LimitsFrom(global, own.Movies, own.Seasons, own.Books), nil
	})
	// Books carried only an author name before authors were rows of their own: link each
	// to its author (spelling variants together, namesakes with different catalogue keys
	// apart, an ambiguous name left unlinked). Idempotent; counts only in the log.
	grp.Go("books: author backfill", func(ctx context.Context) {
		if _, _, err := booksSvc.BackfillAuthors(ctx); err != nil && ctx.Err() == nil {
			log.Warn("books: author backfill failed", "err", err)
		}
	})
	// Book requests made before they remembered their library row are linked to it by
	// title and author, so the ones whose book was re-matched to a new catalogue key
	// stop showing "Searching" and get their "ready".
	grp.Go("requests: book link backfill", func(ctx context.Context) {
		if _, _, err := requestsSvc.BackfillBookIDs(ctx); err != nil {
			log.Warn("requests: book link backfill failed", "err", err)
		}
	})
	// Movies stored before original and alternative titles were kept read them from TMDB,
	// one every two seconds, so searches and imports match them without a manual refresh.
	// The same pass (and the series one after it) reads the translated titles of the extra
	// title languages for titles not yet read for them; saving new languages runs it again.
	// The same passes read the display names "Show titles as" needs; first, every title's
	// shown title is put right from what's stored (a setting saved before an update, or a
	// title change a refresh made while the shown title couldn't be stored).
	grp.Go("movies: alt titles backfill", func(ctx context.Context) {
		if _, err := movieSvc.SyncDisplayTitles(ctx); err != nil && ctx.Err() == nil {
			log.Warn("movies: couldn't update the titles shown", "err", err)
		}
		if _, err := seriesSvc.SyncDisplayTitles(ctx); err != nil && ctx.Err() == nil {
			log.Warn("series: couldn't update the titles shown", "err", err)
		}
		n, err := movieSvc.BackfillAltTitles(ctx, 2*time.Second)
		if err != nil && ctx.Err() == nil {
			log.Warn("movies: alternative titles backfill failed", "filled", n, "err", err)
		} else if n > 0 {
			log.Info("movies: read alternative titles", "movies", n)
		}
		n, err = seriesSvc.BackfillLocalTitles(ctx, 2*time.Second)
		if err != nil && ctx.Err() == nil {
			log.Warn("series: translated titles backfill failed", "filled", n, "err", err)
		} else if n > 0 {
			log.Info("series: read translated titles", "series", n)
		}
	})
	// Reported problems (Requests → Issues): a requester reports a problem with a title in
	// the library, staff are alerted the way a new request alerts them, and everyone who
	// reported it is told through their own channels when it's closed.
	issuesSvc := issues.NewService(st.DB(), issues.MediaLibrary(movieSvc, seriesSvc), log)
	issuesSvc.SetPublisher(bus)
	issuesSvc.SetNotifier(func(ctx context.Context, userIDs []int64, n issues.Notice) (int, error) {
		return requestsSvc.NotifyUsers(ctx, userIDs, requests.UserNotice{Title: n.Title, Body: n.Body, Ref: n.Ref, Kind: n.Kind, MediaType: n.MediaType})
	})
	issuesSvc.SetStaffAlert(func(ctx context.Context, i issues.Issue, data map[string]any) {
		data["reported_by_name"] = requestsSvc.AlertName(ctx, i.ReportedBy, i.ReportedByName)
		requestsSvc.AlertStaff(ctx, requests.StaffNotice{
			Event: notify.EventIssueReported, Dedupe: issues.StaffAlertDedupe(i.ID), Data: data,
			Ref: fmt.Sprintf("issue:%d:new", i.ID), MediaType: i.MediaType,
			Except: i.ReportedBy, Pref: requests.PrefNewRequest, What: "issue", ID: i.ID,
		})
	})
	// Settles the problems being fixed (a finished subtitle job, a new file whose import
	// signal was missed, a fix that brought nothing in a week) and re-sends a closed
	// issue's notices to any reporter an inbox write missed.
	sched.Register("issues-check", time.Minute, false, issuesSvc.Sweep,
		scheduler.Label("Follow up reported problems"),
		scheduler.Description("Resolves reported problems once their fix has landed, and tells the people who reported them."))
	// Backstop for request-ready notifications: catches availability that arrived
	// without an import (a library scan). Idempotent (unique inbox ref), so re-running
	// never double-notifies.
	sched.Register("request-ready-sweep", 10*time.Minute, false, func(ctx context.Context) error {
		return requestsSvc.SweepReadyRequests(ctx)
	}, scheduler.Label("Tell requesters their request is ready"), scheduler.Description("Catches requests that became available without an import, and lets the requester know."))
	// Release previews for requests waiting for approval: a dry-run search of a few queued
	// requests a minute, so whoever approves one sees what it would grab. Paced inside
	// (requests/preview.go) so a burst of requests can't flood the indexers.
	sched.Register("request-previews", time.Minute, false, requestsSvc.RunPreviewQueue,
		scheduler.Label("Preview what pending requests would grab"),
		scheduler.Description("Looks up the release each request waiting for approval would download, a few a minute, without grabbing anything."))

	// Subtitles module (Bazarr replacement): grabs external SRT sidecars over the
	// Movies/Series catalogs via OpenSubtitles.
	subsProvider := subtitles.NewOpenSubtitlesFunc(keyStore.Func("opensubtitles_api"), keyStore.Func("opensubtitles_username"), keyStore.Func("opensubtitles_password"))
	subtitlesSvc := subtitles.NewService(st.DB(), movieSvc, seriesSvc, settingsSvc, subsProvider, "ffmpeg", "ffprobe", filepath.Join(cfg.DataDir, "whisper"), modLog(log, "subtitles"))
	// Replaced and removed sidecars go to the recycle bins, like their videos.
	subtitlesSvc.SetBin(bins)
	// A reported problem's subtitles fix resolves once its job has made subtitles. Hooked
	// up before the worker starts: the queue saved at shutdown runs straight away.
	issuesSvc.SetSubtitles(subtitleOutcomes{runCtx, subtitlesSvc, log})
	subtitlesSvc.SetJobDoneHook(subtitleJobDone(runCtx, issuesSvc, log))
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
	sched.Register("subtitles-upgrade", 24*time.Hour, false, func(ctx context.Context) error {
		subtitlesSvc.UpgradeSweep(ctx)
		return nil
	}, scheduler.Label("Upgrade weak subtitles"), scheduler.Description("Replaces recent AI-made subtitles and loose downloads when a subtitle made for the exact file appears, using a small share of the day's OpenSubtitles downloads."))

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
	convertSvc := convert.NewService(st.DB(), movieSvc, seriesSvc, settingsSvc, "ffmpeg", "ffprobe", convertScratch, "", modLog(log, "convert"))
	// Converted originals wait in a hidden hold on their own library folder (same drive, a
	// rename) for the undo window — never in the recycle bin, whose cap would purge them.
	convertSvc.SetHoldRoots(func() []libroots.Root { return roots.Libraries(context.Background()) }, cfg.DataDir)
	// "Same as Subtitles" for the subtitle tracks Convert keeps reads the Subtitles list.
	convertSvc.SetSubtitleLangs(subtitlesSvc.Languages)
	// An untagged audio track is read as the language the subtitle AI heard on it, for the
	// language badges and the quality profile's Audio language rules (from its cache only).
	convertSvc.SetDetectedAudio(subtitlesSvc.DetectedAudio)
	// Upgrade decisions judge a library file by what Convert's analysis found in it, not
	// only by its release name — the same facts the Library fit bars read.
	coordinator.SetFileFacts(convertSvc)
	// The Unwanted pack's foreign-only check: the audio languages Convert keeps are the
	// household's own, so a release dubbed into one of them is never foreign.
	qualitySvc.SetOwnerLanguages(convertSvc.AudioLanguages)
	// A conversion plan warns when dropping a file's Dolby Vision layer would leave it
	// outside the target of the profile its title runs under.
	convertSvc.SetProfilePrefersDV(func(ctx context.Context, kind string, id int64) bool {
		if kind == "episode" {
			sr, err := seriesSvc.Get(ctx, id)
			return err == nil && qualitySvc.PrefersDolbyVision(ctx, sr.QualityProfile, quality.MediaSeries)
		}
		m, err := movieSvc.Get(ctx, id)
		return err == nil && qualitySvc.PrefersDolbyVision(ctx, m.QualityProfile, quality.MediaMovie)
	})
	// The manager looks after every bin: one per library folder (or the one override),
	// plus the old shared bin while it still holds files, so what's in it stays listed,
	// restorable, aged and capped until it drains.
	recycleSvc := recyclebin.NewBins(bins.All, settingsSvc, log)
	// A music staging job a crash left unsettled may need a replaced file back out of the
	// bin: the bin never purges, empties or deletes those (MUS-33).
	recycleSvc.SetHeld(func() map[string]bool { return stage.HeldInBin(bookImporter.MusicDir()) })
	grp.Loop("convert: runner", convertSvc.Run)
	// Originals whose undo window has ended leave the hold — the only deletes it makes on
	// its own, and only by the retention you set.
	sched.Register("convert-hold-expire", time.Hour, true, convertSvc.ExpireHolds,
		scheduler.Label("Release converted originals"),
		scheduler.Description("Deletes originals Convert kept for undo once their undo window ends (Convert settings, Undo)."))
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
	insightsSvc := insights.NewService(st.DB(), settingsSvc, geoResolver, bus, modLog(log, "insights"))
	insightsSvc.SeedFromEnv(runCtx, cfg.PlexURL, cfg.PlexToken)
	// A Tautulli import the last run was in the middle of will never finish: say so,
	// rather than showing it as running forever.
	if n, err := insightsSvc.MarkInterruptedImports(runCtx); err != nil {
		log.Warn("insights: couldn't close imports left running", "err", err)
	} else if n > 0 {
		log.Info("insights: marked imports left unfinished by the last run as interrupted", "runs", n)
	}
	grp.Loop("insights: poller", insightsSvc.Run) // Plex watch-monitoring poller (records when enabled + configured)
	// Alerts link back to the app at its outside address and carry the title's poster:
	// the library's stored TMDB / Open Library image, or a Plex image fetched server-side.
	notifySvc.SetPublicURL(settingsSvc.PublicURL)
	notifySvc.SetPosters(alertPosters(movieSvc, seriesSvc, booksSvc), insightsSvc.Image)
	// Convert pauses its encodes while someone is watching — which it can only know while
	// monitoring is on, so its settings page says so when it isn't.
	convertSvc.SetWatching(insightsSvc.Watching, func() bool { return insightsSvc.MonitoringActive(runCtx) })
	// convert.done / failed / blocked / stalled (staff-only topics): the live UI and the
	// admin alerts hear them (internal/notify/convert_digest.go).
	convertSvc.SetBus(bus)
	// "Convert gave up on a file" at once, and one summary a morning (notify/convert_digest.go;
	// its counts survive a restart in a setting). Stuck-for-space is a Needs-you item below.
	convertAlerts := notify.NewConvertAlerts(notifySvc, settingsSvc, log)
	grp.Loop("notify: convert alerts", func(ctx context.Context) { convertAlerts.Run(ctx, bus) })
	sched.Register("convert-digest", 15*time.Minute, false, convertAlerts.SendDigest,
		scheduler.Label("Send the daily Convert summary"),
		scheduler.Description("After 09:00, sends one message with what Convert did since the last one: files converted, space freed and any problems."))
	// Tell Plex which folder changed after an import, upgrade, rename, delete or Convert
	// swap: one debounced partial scan per folder, translated to Plex's own paths. Plex
	// being down or unset never holds anything up — requests are queued in memory and
	// the worker logs what it couldn't send.
	scanRoots := func(ctx context.Context) map[string]string {
		return map[string]string{plexscan.KindMovie: roots.Movies(ctx), plexscan.KindShow: roots.TV(ctx)}
	}
	// The extra Movies and TV folders, each resolved against its own media-server library.
	extraScanRoots := func(ctx context.Context) map[string][]string {
		out := map[string][]string{}
		for _, e := range roots.Extras(ctx) {
			kind := plexscan.KindMovie
			if e.Media == libroots.MediaSeries {
				kind = plexscan.KindShow
			}
			out[kind] = append(out[kind], e.Path)
		}
		return out
	}
	plexScanner := plexscan.New(plexscan.Options{
		Client:     func(ctx context.Context) plexscan.Client { return insightsSvc.PlexClient(ctx) },
		Configured: insightsSvc.Configured,
		Settings:   settingsSvc,
		Roots:      scanRoots,
		Log:        log,
	})
	plexScanner.SetExtraRoots(extraScanRoots)
	grp.Loop("plex: library scans", plexScanner.Run)
	// Jellyfin next to Plex: its connection, library index and scanner, and one fan-out
	// (media.Request) that tells every server about a changed folder (mediaservers.go).
	media := setupMediaServers(mediaServerDeps{
		Group: grp, Settings: settingsSvc, Keys: keyStore, Insights: insightsSvc, Plex: plexScanner,
		Roots: scanRoots, ExtraRoots: extraScanRoots, Log: modLog(log, "jellyfin"),
	})
	// The Plex library index (TMDB/TVDB/IMDb → rating key) behind Watch on Plex: rebuilt
	// every 30 minutes and about 90 seconds after each scan, once Plex has added the files.
	grp.Loop("plex: library index", insightsSvc.RunPlexIndex)
	plexScanner.OnRefreshed(func(string, string, string) { insightsSvc.PlexIndexStale(90 * time.Second) })
	// 'Ready' means watchable on a media server: with Plex or Jellyfin set up, a request's
	// notice waits until either server's index shows the title (looked at again after
	// every rebuild of either, and every two minutes so the 30-minute fallback fires even
	// if no index ever builds). Watch links lead with each person's own server.
	requestsSvc.SetPlexLocator(mediaReadyLocator{media.Registry})
	requestsSvc.SetHomeServer(homeServer(authSvc))
	media.Registry.OnIndexRebuilt(requestsSvc.KickPlexCheck)
	grp.Loop("requests: plex check", requestsSvc.RunPlexChecks)
	sched.Register("request-plex-check", 2*time.Minute, false, requestsSvc.CheckPlexWaiting,
		scheduler.Label("Tell requesters once their title is on Plex or Jellyfin"),
		scheduler.Description("Sends the 'ready' notice for requests waiting for Plex or Jellyfin to add the title, or after 30 minutes if neither shows it."))
	// Imports, upgrades and movie renames/deletes reach it through the outbox (registered
	// below); the changes that don't go through the outbox call it directly.
	seriesSvc.SetLibraryChanged(media.Request)   // episode and show deletes
	coordinator.SetLibraryChanged(media.Request) // series renames
	convertSvc.SetLibraryChanged(media.Request)  // a converted file swapped in
	// A subtitle written, shifted, removed or adopted: the servers pick it up without a
	// full scan.
	subtitlesSvc.SetLibraryChanged(media.Request)
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
	// "Update available" on Settings → Status: GitHub's public releases, read twice a day
	// unless switched off there (then it asks nothing).
	updates := updatecheck.New(updatecheck.Options{
		Version: buildinfo.Version, Commit: buildinfo.Commit, Install: cfg.Channel, Log: log,
		Host:    updatecheck.HostFromEnv(os.Getenv),
		Enabled: func(ctx context.Context) bool { return settingsSvc.GetBool(ctx, updatecheck.KeyEnabled, true) },
	})
	sched.Register("update-check", updatecheck.Interval, true, updates.Run,
		scheduler.Label("Check for updates"), scheduler.Description("Asks GitHub every 12 hours whether a newer Arrmada release is out. Switch it off on Settings → Status."))

	// Audiobook server: listening apps (Lissen and other Audiobookshelf clients) connect to
	// its own port. Off until an admin switches it on in Settings → Book server.
	listenStore := listening.NewStore(st.DB())
	// A book merge moves everyone's listening places with the book, and queues the kept
	// book's catalogue refresh and requesters' "ready" in the same transaction.
	booksSvc.SetMergeHooks(listenStore, func(ctx context.Context, q store.Execer, keepID int64) error {
		return box.Enqueue(ctx, q, outbox.TopicBookImported, outbox.BookImported{BookID: keepID, Edition: "merge"},
			fmt.Sprintf("book:%d", keepID))
	})
	audioSrv := audioserver.New(audioserver.Options{
		DB: st.DB(), Books: booksSvc, Listen: listenStore, Users: authSvc, Settings: settingsSvc,
		Log: modLog(log, "audioserver"), FFprobe: "ffprobe", DataDir: cfg.DataDir,
	})
	// Files probed before their ids were stored get the id they've always been given, from
	// the database alone, before any rename can move them.
	if n, err := audioSrv.BackfillFileIDs(runCtx); err != nil {
		log.Warn("audiobook server: couldn't store the ids of known files", "err", err)
	} else if n > 0 {
		log.Info("audiobook server: stored the ids of known files", "files", n)
	}
	audioPort := os.Getenv("ARRMADA_AUDIOBOOK_LISTEN_PORT")
	if audioPort == "" {
		audioPort = "13378"
	}
	audioMgr := audioserver.NewManager(audioSrv, ":"+audioPort, modLog(log, "audioserver"))
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
	// The one full walk of the audiobook folders: the warm-up and every listing read what
	// is known from the database, so a file deleted outside Arrmada is noticed here.
	sched.Register("audioserver-verify", 24*time.Hour, false, func(ctx context.Context) error {
		if settingsSvc.GetBool(ctx, audioserver.KeyEnabled, false) {
			if n := audioSrv.Verify(ctx); n > 0 {
				log.Info("audiobook server: forgot audio files that are gone", "files", n)
			}
		}
		return nil
	}, scheduler.Label("Check audiobook files"), scheduler.Description("Once a day, checks the audiobook folders for files added or removed outside Arrmada."))

	// Health checks run in the background on their own intervals (the health-check task
	// below drives them); the health endpoint serves their cached results. httpapi.New
	// registers the checks built from its deps.
	healthReg := health.NewRegistry(bus, log)

	// The Needs-you feed: one snapshot of everything waiting on a person, refreshed every
	// 30 seconds and a second after anything it reports on changes. The sidebar badges and
	// the Dashboard card read it from memory, however many tabs are open.
	needsYou := attention.New(attention.QueueFrame(downloads.Snapshot, coordinator.LiveByHash, coordinator.TryingSince), bus, log,
		attention.Requests(requestsSvc),
		attention.Reviews(coordinator),
		attention.Downloads(),
		attention.Imports(importFailures, coordinator),
		attention.WrongCategory(coordinator.WrongCategoryDownloads),
		attention.Health(healthReg),
		attention.Convert(convertSvc),
		attention.Issues(issuesSvc),
		attention.Subtitles(subtitlesSvc),
		attention.Searches(
			func(ctx context.Context) (int, error) {
				return movieSvc.CountSearchStuck(ctx, attention.SearchStuckAfter)
			},
			func(ctx context.Context) (int, error) {
				return seriesSvc.CountSearchStuck(ctx, attention.SearchStuckAfter)
			},
			func(ctx context.Context) (int, error) {
				if !settingsSvc.GetBool(ctx, settings.KeyModuleBooks, true) {
					return 0, nil
				}
				return booksSvc.CountSearchGivenUp(ctx)
			},
		),
	)
	// Each refresh ends by diffing the feed into Needs-you and health alerts: once per
	// problem, never repeated by a restart (attention/alerts.go).
	needsYou.SetAlerts(attention.NewAlerter(st.DB(), notifySvc, settingsSvc, log))
	healthReg.SetOnChange(needsYou.Kick)
	coordinator.SetAttentionKick(needsYou.Kick)
	requestsSvc.SetAttentionKick(needsYou.Kick)
	issuesSvc.SetAttentionKick(needsYou.Kick)
	grp.Loop("attention: refresh on change", needsYou.Run)
	sched.Register("attention-refresh", 30*time.Second, true, needsYou.Refresh,
		scheduler.Label("Update the Needs-you list"),
		scheduler.Description("Gathers pending requests, held imports, download problems, stuck searches and health problems for the Dashboard and the sidebar badges."))

	// Everything that acts on an import is an outbox consumer. Registered here, once every
	// consumer exists and before the scheduler starts the import sweeps (and before the
	// HTTP server takes manual imports): Enqueue writes rows only for the consumers
	// registered at that moment. Rows a previous run left unfinished run as soon as the
	// dispatcher starts.
	importConsumers{
		convert: convertSvc, subtitles: subtitlesSvc, requests: requestsSvc, issues: issuesSvc, audio: audioSrv, grp: grp,
		plex: media, showFolder: seriesSvc.FolderPath,
		// With scans switched off nothing calls OnRefreshed; the server's own watcher adds
		// the file in its own time, so look again a few minutes after the import.
		plexIndexStale: media.IndexStaleAfterImport,
	}.register(box)
	grp.Loop("outbox: dispatcher", box.Run)
	// Alert deliveries are each connection's log on the Alerts page: a month of it.
	sched.Register("notify-prune", 24*time.Hour, true, func(ctx context.Context) error {
		n, err := notifySvc.PruneDeliveries(ctx, notify.HistoryKeep)
		if err == nil && n > 0 {
			log.Info("notify: cleared old alert history", "rows", n)
		}
		return err
	}, scheduler.Label("Clear old alert history"), scheduler.Description("Deletes alert deliveries older than 30 days."))
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
		// A typed Hardcover key only; the saved one is tested through Books.
		"hardcover": hardcover.VerifyKey,
		"acoustid":  acoustidClient.Verify,
	}
	restartCh := make(chan struct{}, 1)
	// "Move to folder": a title's files to another Movies or TV folder, as a job. A
	// download on its way in would land in the old folder half-way, so a title with one
	// in flight waits (fail closed: an unreadable check refuses too).
	folderMover := &libfolders.Mover{
		DB: st.DB(), Folders: libFolders, Bin: bins, Scan: media.Request, ShowFolders: seriesSvc.ShowFoldersOf, Unclear: seriesSvc.MoveBlocked,
		Hold: coordinator.HoldSearches, // no search grabs into the old folder half-way
		// A film's folder moves whole only when it is provably the film's own.
		OwnFolder: movieSvc.MoveFolderCheck,
		Log:       modLog(log, "import"),
		Busy: func(ctx context.Context, lib string, id int64) string {
			acqs, err := coordinator.Active(ctx, lib, id)
			if err != nil {
				return "Arrmada couldn't check for downloads in flight just now — try again"
			}
			if len(acqs) > 0 {
				return "a download for it is on its way in — move it once that has imported"
			}
			return ""
		},
	}
	srv := httpapi.New(httpapi.Deps{
		Folders:      libFolders,
		FolderMover:  folderMover,
		SeriesByTVDB: tmdb.SeriesByTVDB, // Sonarr imports: TVDB ids → TMDB
		Config:       cfg,
		Log:          modLog(log, "api"),
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
		Issues:       issuesSvc,
		Discovery:    tmdb,
		Ratings:      omdb,
		Books:        booksSvc,
		Music:        musicSvc,
		MusicBrainz:  mbClient,
		Subtitles:    subtitlesSvc,
		Convert:      convertSvc,
		Insights:     insightsSvc,
		Push:         pushSvc,
		Recycle:      recycleSvc,
		Logs:         logRing,
		LogLevel:     logLevel,
		APIKeys:      keyStore,
		FlareSolverr: flare,
		ConnStatus:   connStatus,
		KeyVerifiers: keyVerifiers,
		AudioServer:  audioSrv,
		AudioManager: audioMgr,
		// KEPUBs for Kobo's stock reader, made on demand into a bounded cache under the data
		// folder (never next to the library file). Settings → Book server switches it off.
		Kepub: kepub.New(kepub.Options{
			Dir:     filepath.Join(cfg.DataDir, "cache", "kepub"),
			Enabled: func(ctx context.Context) bool { return settingsSvc.GetBool(ctx, kepub.KeyEnabled, true) },
		}),
		// Setup's OpenSubtitles check: the key, username and password typed together.
		SubtitleLoginVerifier: subsProvider.VerifyLogin,
		Restart: func() {
			select {
			case restartCh <- struct{}{}:
			default:
			}
		},
		ImportFailures: importFailures,
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
		Updates:   updates,
		Health:    healthReg,
		Scheduler: sched,
		Jobs:      jobRunner,
		Attention: needsYou,
		PlexScan:  plexScanner,
		PlexLinks: insightsSvc,
		// Jellyfin's connection card, its library updates, and every server together.
		Jellyfin:     media.Jellyfin,
		JellyfinScan: media.JellyfinScan,
		MediaServers: media.Registry,
		HomeServer:   homeServer(authSvc), // whose Watch link a person sees first
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
				inactive := false
				switch retryBoot(ctx, func(ctx context.Context) error {
					dl = roots.Downloads(ctx)
					if dl == "" {
						return nil // nothing to point it at; grabs pass no save path either
					}
					err := downloads.SetBundledSavePath(ctx, dl)
					if errors.Is(err, download.ErrBundledInactive) {
						inactive = true // switched off or removed: nothing to move
						return nil
					}
					return err
				}) {
				case nil:
					if !inactive {
						log.Info("qBittorrent save path moved to the new Downloads folder", "path", dl)
					}
				case errRetriesExhausted:
					log.Warn("could not move qBittorrent's save path to the new Downloads folder — grabs still pass it with each torrent", "path", dl)
				}
			}
			bus.Publish("system.folders.applied", map[string]any{"changed": changed})
			// The bundled SABnzbd's folders follow the Downloads folder too; after the rest,
			// so a SABnzbd that's down doesn't hold up qBittorrent's move or the page.
			if sab != nil && slices.Contains(changed, "downloads") {
				sab.foldersChanged(ctx)
			}
		},
	})
	// The Compose bundle's qBittorrent letting whole networks in without a password.
	if isComposeBundleQbit(cfg) {
		healthReg.Register(bundledQbitCheck(downloads, cfg))
	}
	// A task that keeps failing is a health problem too (it warns at three in a row).
	healthReg.Register(health.TasksFailingCheck(func() []health.TaskState {
		var out []health.TaskState
		for _, t := range sched.Snapshot() {
			// The Prowlarr sync has a health line of its own (the "prowlarr" check) that
			// says more; a second "task failing" line for it would only repeat it.
			if t.Hidden || t.Name == "prowlarr-sync" {
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
	case <-st.Done():
		log.Error("PostgreSQL lifecycle ownership was lost; shutting down instead of reconnecting as a second instance")
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
// tuneBundled applies one setting to the bundled qBittorrent at boot, retrying while it
// starts, and logs the result. A bundled client that's switched off or removed is left
// alone: it may not be running, and retrying it would only fill the log.
func tuneBundled(ctx context.Context, log *slog.Logger, what string, fn func(ctx context.Context) error, attrs ...any) {
	inactive := false
	switch retryBoot(ctx, func(ctx context.Context) error {
		err := fn(ctx)
		if errors.Is(err, download.ErrBundledInactive) {
			inactive = true
			return nil
		}
		return err
	}) {
	case nil:
		if inactive {
			log.Info("bundled qBittorrent is switched off or removed; not setting its "+what, attrs...)
			return
		}
		log.Info("qBittorrent "+what+" set", attrs...)
	case errRetriesExhausted:
		log.Warn("could not set qBittorrent "+what, attrs...)
	}
}

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

// musicCacheRefreshPerRun caps the MusicBrainz calls one refresh-music-cache run makes:
// about six minutes against musicbrainz.org at its pace.
const musicCacheRefreshPerRun = 300

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

// logLevel is the level every log line is checked against. It starts at the install's
// ARRMADA_LOG_LEVEL; the Logs page can raise it for a while (it reverts by itself, and a
// restart starts back at the install's level).
var logLevel *applog.LevelSwitch

// modLog tags a service's log lines with its module, which the Logs page filters on.
// applog.Modules lists the names; keep the two in step.
func modLog(log *slog.Logger, module string) *slog.Logger { return log.With("module", module) }

func newLogger(level string) *slog.Logger {
	l, ok := applog.ParseLevel(level)
	if !ok {
		l = slog.LevelInfo
	}
	logLevel = applog.NewLevelSwitch(l)
	base := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel.Leveler()})
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
