// Package automation is the coordinator that ties the pipeline together: it
// searches indexers for monitored-but-missing movies, ranks releases with the
// quality engine, grabs the best, and attaches finished imports back to the
// movie. It's the "add a movie and walk away" brain.
package automation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tristenlammi/arrmada/internal/audiobook"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/outbox"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

// seriesCategory keeps TV downloads in a separate download-client category so the
// multi-file series importer processes them, not the single-file movie importer.
const seriesCategory = download.CategoryTV

// Coordinator orchestrates search → grab → import-attach.
type Coordinator struct {
	movies       *movies.Service
	indexers     *indexer.Service
	downloads    *download.Service
	quality      *quality.Service
	db           store.Pool
	bus          *eventbus.Bus
	log          *slog.Logger
	downloadsDir string          // the startup downloads folder; downloadsPath reads the live one
	downloadsFn  func() string   // the live downloads folder (Settings → Library); nil = downloadsDir
	movieCat     string          // the movie download category (SetMovieCategory); "" = download.DefaultMovieCategory
	series       *series.Service // set post-construction via SetSeries
	fileFacts    FileFactsSource // Convert's probed facts for library files; nil = release names only
	books        *books.Service  // set post-construction via SetBooks
	bookCovers   string          // <data>/covers, for a Calibre folder's cover.jpg (SetBookCoverDir); "" = not used
	music        *music.Service  // set post-construction via SetMusic
	imp          *library.Importer
	recycle      string      // recycle-bin dir for book deletes ("" = bin off); set via SetRecycleDir
	binv         library.Bin // the bins set by SetBin; nil = the single bin at recycle

	// folderDownloadsFn is a library folder's own downloads folder ("" = none): torrent
	// grabs for a title in that folder save there (grabfolder.go). nil = none anywhere.
	folderDownloadsFn func(ctx context.Context, media, root string) string
	// freeGBFn overrides diskspace.FreeGB in tests; nil measures the real disk.
	freeGBFn func(path string) (float64, bool)

	// removeTorrent overrides downloads.Remove in tests; nil uses the real client.
	removeTorrent func(ctx context.Context, hash string, deleteData bool) error
	// fetchRelease overrides indexers.Fetch in tests (a usenet grab's NZB); nil uses the
	// indexer service.
	fetchRelease func(ctx context.Context, indexerName, downloadURL string) (indexer.FetchResult, error)

	// guard caches the library's own title keys (movies.PrimaryKeys) for movieKeys, so a
	// sweep over every movie reads them once rather than once per movie.
	guardMu sync.Mutex
	guard   movies.PrimaryKeys
	guardAt time.Time
	// importRetry drops the movie importer's back-off for a download (SetImportRetry);
	// nil = nothing to drop.
	importRetry func(hash string)

	// merging holds the IDs of books with an audiobook merge running, so a second click
	// can't start another ffmpeg over the same files.
	merging sync.Map
	// bookLocks holds one *sync.Mutex per book: an import, a rename, a rescan and a merge
	// of the same book's files never run at once (books_guard.go).
	bookLocks sync.Map
	// mergeFn, durationFn and decodedDurationFn default to the audiobook package; tests
	// swap them.
	mergeFn           func(ctx context.Context, files []string, out string, opts audiobook.MergeOptions) (audiobook.MergeResult, error)
	durationFn        func(ctx context.Context, path string) (float64, error)
	decodedDurationFn func(ctx context.Context, path string) (float64, error)

	// moduleGate says whether a switchable module is on right now. Asked on every run
	// rather than captured at startup, so flipping a module in Settings takes effect on
	// the next cycle without a restart. nil (tests, or nothing wired) means on.
	moduleGate func(ctx context.Context, module string) bool

	// Test seams for the music path's calls to the indexers and the download client;
	// nil means the real service. See searchMusic, musicQueue and grabMusic.
	musicSearchFn func(ctx context.Context, q indexer.SearchQuery) (indexer.SearchResult, error)
	musicQueueFn  func(ctx context.Context) ([]download.Item, error)
	musicGrabFn   func(ctx context.Context, indexerName, url, title, category string) (string, error)
	// musicRefreshBudget overrides refresh-music-artists' MusicBrainz call budget (0: the
	// default). See RefreshMusicArtists.
	musicRefreshBudget int64
	// probeAudioFn measures an audio file (nil: ffprobe through mediainfo). See probeAudio.
	probeAudioFn func(ctx context.Context, path string) (mediainfo.AudioInfo, error)
	// musicTagsFn reads an audio file's tags and length for the matcher (nil: audiotag).
	// See readMatchFile.
	musicTagsFn func(path string) (music.FileTags, int, error)
	// fingerprinter identifies files by sound for the matcher's second pass (MUS-32); nil
	// leaves it off.
	fingerprinter MusicFingerprinter

	// unmatched counts how many import sweeps have failed to match a download to a
	// series, keyed by torrent hash. Without it the 30-second sweep logs the same failure
	// forever and nothing ever escalates. Guarded by unmatchedMu. Entries are pruned when
	// their download leaves the completed list, so the map can't grow for the process
	// lifetime.
	unmatchedMu sync.Mutex
	unmatched   map[string]int
	// metaRetry counts import sweeps that deferred a series download because the show's
	// episode listing couldn't be trusted, keyed by content path (see importSeriesInto).
	// Also guarded by unmatchedMu, and pruned the same way.
	metaRetry map[string]int
	// knownNames remembers, by torrent hash, the show a series download was confirmed for
	// under one of its names in another language (seriesKnownAs), so a seeding download
	// isn't re-checked every sweep. Also guarded by unmatchedMu, and pruned the same way.
	knownNames map[string]int64
	// musicRetry holds back a music download whose import failed for a reason of ours (the
	// recycle bin refusing, a placement error), keyed by torrent hash: how many times in a
	// row, and when to try again. Guarded by unmatchedMu.
	musicRetry map[string]musicRetryState
	// measureCursor is the last track id the music scan's measuring pass reached, so a file
	// that can't be measured doesn't hold every later one back (see measureUnmeasuredTracks).
	measureCursor atomic.Int64
	// musicPlace is music placement's wiring: house tags, covers, extras, album locks
	// (music_placement.go).
	musicPlace musicPlacement
	// musicVerify is the music verify pass's last outcome (music_verify.go).
	musicVerify musicVerifyState

	// The stall clock — when each pending grab's download last moved forward — and when a
	// stalled one last found no replacement are kept on its grab row (acquisitions.go), so
	// a restart doesn't reset them.
	// stallDefaultFn reads the global stall timeout (Settings → Downloads) that a grab and
	// profile left at 0 fall back to; guardHeldFn reads the hashes the disk guard has
	// paused. Both are set once at startup; nil means DefaultStallMinutes / none held.
	stallDefaultFn func(ctx context.Context) int
	guardHeldFn    func(ctx context.Context) map[string]bool

	// stallSeen is when this process first saw each pending grab's torrent gone from
	// the client or in a hard error, so one read can't condemn it (stallevidence.go).
	stallSeenMu sync.Mutex
	stallSeen   map[int64]stallSighting
	// trying is when this process first saw each download (by lowercased hash) trying to
	// download, for the queue's stall rule (queuestall.go).
	tryingMu sync.Mutex
	trying   map[string]time.Time

	// upgradeBudgetFn reads how many upgrades one sweep may grab (Settings → Downloads),
	// set once at startup; nil means DefaultUpgradeBudget. See upgradebudget.go.
	upgradeBudgetFn func(ctx context.Context) int

	// bookLangsFn reads the languages automatic book grabs accept (Settings → Library),
	// set once at startup; nil accepts every language. See booklangs.go.
	bookLangsFn func(ctx context.Context) []string

	// now is the books sweep's clock; nil means time.Now. Tests set it to step through
	// the search ladder.
	now func() time.Time

	// claims keeps two searches of one title from running at once (claims.go).
	claims claims

	// mq is the movie search queue's view of the movie searches waiting and running
	// (moviequeue.go); the sweeps leave those movies alone.
	mq movieQueue

	// tokens holds the download links behind the opaque release tokens interactive
	// search hands the browser (releasetokens.go). Made on first use.
	tokensOnce sync.Once
	tokens     *releaseTokens
	// previewTok holds the release tokens of request previews (preview.go), apart from
	// the interactive ones. Made on first use.
	previewOnce sync.Once
	previewTok  *previewTokenStore

	// The manual missing-editions sweep for books (books_sweep.go).
	bookSweepMu sync.Mutex
	bookSweep   BookSweepStatus

	// danglingLogged remembers which deleted profile refs effectiveProfile has already
	// reported, so a library full of titles on one deleted profile logs it once per
	// process instead of on every sweep.
	danglingLogged sync.Map

	// outbox receives the durable side effects of series and book imports (Convert and
	// Subtitles for the episodes that landed, the requester's "ready", the audiobook
	// catalogue). nil means nothing is wired (tests).
	outbox outbox.Enqueuer
	// onLibraryChanged hears about folders a rename changed (Plex's scanner); nil = none.
	onLibraryChanged func(kind, dir string)

	// attentionKick asks the Needs-you feed to refresh (attentionkick.go); nil = nothing.
	attentionKick atomic.Pointer[func()]
	// onGrabbed hears that a grab was recorded for a library item (grabhook.go); nil = nothing.
	onGrabbed atomic.Pointer[func(ctx context.Context, mediaType string, itemID int64)]
	// importNotes is what the series import sweep found that the feed reports: packs
	// whose files keep failing to place, and misfiled TV downloads (importattention.go).
	importNotes importNotes
	// feed caches what the Activity queue reads on every refresh (feedcache.go).
	feed feedCache
	// usenet is what this process has seen of the usenet client: whether its jobs are
	// moving, failures already reported, and finished jobs whose folders were left behind
	// (usenetlife.go).
	usenet usenetWatch
	// handoffs are usenet grabs whose NZB SABnzbd was handed the link to fetch, waiting for
	// their grab to be recorded (nzbhandoff.go).
	handoffs handoffNotes
}

// SetOutbox installs where series and book imports queue their side effects.
func (c *Coordinator) SetOutbox(o outbox.Enqueuer) { c.outbox = o }

// SetLibraryChanged installs who hears that a library folder's files changed outside an
// import — a series rename moving episode files — so Plex can rescan it. kind is "movie"
// or "show". Call it at startup, before anything runs.
func (c *Coordinator) SetLibraryChanged(fn func(kind, dir string)) { c.onLibraryChanged = fn }

func (c *Coordinator) libraryChanged(kind, dir string) {
	if c.onLibraryChanged != nil && dir != "" && dir != "." {
		c.onLibraryChanged(kind, dir)
	}
}

// enqueue writes an outbox row on its own (series and book imports aren't one
// transaction). A failure is logged, not fatal: the files are in place either way, and
// the nightly Convert sweep, the 6-hourly Subtitles pass and the request-ready sweep
// still catch up.
func (c *Coordinator) enqueue(ctx context.Context, topic string, payload any, key string) {
	if c.outbox == nil {
		return
	}
	if err := c.outbox.Enqueue(ctx, c.db, topic, payload, key); err != nil {
		c.log.Warn("couldn't queue an import's follow-up work — the periodic sweeps will catch up", "topic", topic, "err", err)
	}
}

// seriesImported queues the follow-up work for episodes an import just placed: Convert
// reindexes the show, Subtitles fetches for exactly those episodes, and the requester
// hears once nothing is still wanted. episodes is what the import placed — just those,
// not the whole show.
func (c *Coordinator) seriesImported(ctx context.Context, seriesID int64, episodes []series.EpisodeRef) {
	p := outbox.SeriesImported{SeriesID: seriesID}
	for _, e := range episodes {
		p.Episodes = append(p.Episodes, outbox.Episode{Season: e.Season, Episode: e.Episode})
	}
	// Rows naming episodes each carry their own list and must all run; a bare "the show
	// changed" row is the same work however many times it's queued.
	key := ""
	if len(p.Episodes) == 0 {
		key = fmt.Sprintf("series:%d", seriesID)
	}
	c.enqueue(ctx, outbox.TopicSeriesImported, p, key)
}

// bookImported queues the follow-up work for a book edition or audiobook version that
// just landed: the requester's "ready" and the audiobook catalogue refresh.
func (c *Coordinator) bookImported(ctx context.Context, bookID int64, edition string) {
	c.enqueue(ctx, outbox.TopicBookImported, outbox.BookImported{BookID: bookID, Edition: edition}, fmt.Sprintf("book:%d", bookID))
}

// bookFilesChanged queues the audiobook catalogue's re-read after a book's files were
// renamed or deleted. It is never book.imported, which would tell the requester the book
// is ready.
func (c *Coordinator) bookFilesChanged(ctx context.Context, bookID int64) {
	c.enqueue(ctx, outbox.TopicBookFilesChanged, outbox.BookFilesChanged{BookID: bookID}, fmt.Sprintf("book:%d", bookID))
}

// SetRecycleDir points book file deletion at the recycle bin (matching movies). Empty
// means the bin is switched off and deletes are permanent.
func (c *Coordinator) SetRecycleDir(dir string) { c.recycle = dir }

// SetBin points book file and series duplicate deletion at bin (the per-library bins in
// the app). Call it at startup, before anything runs.
func (c *Coordinator) SetBin(b library.Bin) { c.binv = b }

// bin is where book files and series duplicates go when deleted (off = permanent).
func (c *Coordinator) bin() library.Bin {
	if c.binv != nil {
		return c.binv
	}
	return library.SingleBin(c.recycle)
}

// SetDownloadsDirFunc makes the downloads folder live: every grab's save path and the
// free-space check before an auto-grab read it, so a folder changed in Settings → Library
// is used from the next grab. Call it at startup, before anything runs.
func (c *Coordinator) SetDownloadsDirFunc(fn func() string) { c.downloadsFn = fn }

// SetMovieCategory sets the download-client category movie grabs are filed under — the
// one the movie import sweep reads (ARRMADA_DOWNLOAD_CATEGORY). Call it at startup.
func (c *Coordinator) SetMovieCategory(cat string) { c.movieCat = strings.TrimSpace(cat) }

// movieCategory is the category every movie grab and upload is added under. It's always
// passed explicitly: a blank one used to let the client's own category setting decide,
// and anything but this value meant the movie downloaded and never imported.
func (c *Coordinator) movieCategory() string {
	if c.movieCat != "" {
		return c.movieCat
	}
	return download.DefaultMovieCategory
}

// downloadsPath is the downloads folder now.
func (c *Coordinator) downloadsPath() string {
	if c.downloadsFn != nil {
		return strings.TrimSpace(c.downloadsFn())
	}
	return c.downloadsDir
}

// SetSeries wires the series module + its importer for TV acquisition.
func (c *Coordinator) SetSeries(s *series.Service, imp *library.Importer) {
	c.series = s
	c.imp = imp
}

// SetBooks wires the books module (shares the importer set by SetSeries).
func (c *Coordinator) SetBooks(b *books.Service) { c.books = b }

// SetBookCoverDir names where uploaded book covers live (<data>/covers, served by
// GET /books/{id}/cover-image). A library scan copies a Calibre folder's cover.jpg there
// for a book the catalogue has no cover for; the Calibre folder itself is never written.
func (c *Coordinator) SetBookCoverDir(dir string) { c.bookCovers = dir }

// SetMusic wires the music module (shares the importer set by SetSeries).
func (c *Coordinator) SetMusic(m *music.Service) { c.music = m }

// SetModuleGate wires the check for whether a module is switched on (see moduleGate).
func (c *Coordinator) SetModuleGate(fn func(ctx context.Context, module string) bool) {
	c.moduleGate = fn
}

// moduleOn reports whether a module's background work and actions should run.
func (c *Coordinator) moduleOn(ctx context.Context, module string) bool {
	return c.moduleGate == nil || c.moduleGate(ctx, module)
}

// New wires the coordinator.
func New(m *movies.Service, ix *indexer.Service, dl *download.Service, q *quality.Service, db store.Pool, bus *eventbus.Bus, log *slog.Logger, downloadsDir string) *Coordinator {
	return &Coordinator{
		movies: m, indexers: ix, downloads: dl, quality: q, db: db,
		bus: bus, log: log, downloadsDir: downloadsDir,
	}
}

// KnownProfile reports whether ref names a quality profile (preset or custom).
func (c *Coordinator) KnownProfile(ctx context.Context, ref string) bool {
	return c.quality.Known(ctx, ref)
}

// Grab resolves a release's download link and hands it to a download client.
// Shared by the manual grab endpoint and automatic search.
func (c *Coordinator) Grab(ctx context.Context, indexerName, downloadURL, title string) (string, error) {
	return c.grabTo(ctx, indexerName, downloadURL, title, c.movieCategory())
}

// GrabForMovie is Grab for a hand-picked release of film movieID: it saves beside the
// film's library folder when that folder has a downloads folder of its own.
func (c *Coordinator) GrabForMovie(ctx context.Context, movieID int64, indexerName, downloadURL, title string) (string, error) {
	return c.Grab(c.forMovieID(ctx, movieID), indexerName, downloadURL, title)
}

// grabTo is Grab with an explicit download-client category (series use a separate
// one so the multi-file importer handles them).
// grabTo hands a release to the download client and returns the torrent's info hash so
// the caller can record it on the grab.
//
// The hash matters because names don't survive the round trip: an indexer's listing title
// is often a prettified rendering of the actual torrent ("EAC3" as "DD+", "10bit" and
// episode titles dropped), so a grab recorded under the listing can never be matched back
// to what the client holds. An empty hash is not an error — a tracker may serve something
// we can't parse — and the caller falls back to name matching as before.
func (c *Coordinator) grabTo(ctx context.Context, indexerName, downloadURL, title, category string) (string, error) {
	// A usenet release goes only to a usenet client. Handing a torrent client an .nzb URL
	// "succeeds" (qBittorrent fetches URLs async and answers 2xx), records an in-flight
	// grab, and nothing ever arrives.
	if c.indexerTransport(ctx, indexerName) == download.TransportUsenet {
		return c.grabUsenet(ctx, indexerName, downloadURL, title, category)
	}
	res, err := c.indexers.Fetch(ctx, indexerName, downloadURL)
	if err != nil {
		return "", err
	}
	add := download.AddRequest{Name: title, SavePath: c.savePath(ctx), Category: category}
	var hash string
	switch {
	case len(res.File) > 0:
		add.File = res.File
		add.Filename = res.Filename
		if h, herr := download.InfoHashFromFile(res.File); herr == nil {
			hash = h
		} else {
			c.log.Warn("grab: could not read the torrent's info hash — seed rules will fall back to name matching",
				"release", title, "err", herr)
		}
	case res.URL != "":
		add.URL = res.URL
		if h, herr := download.InfoHashFromMagnet(res.URL); herr == nil {
			hash = h
		} else {
			// Only a magnet carries its hash in the link; an indexer that hands back a
			// plain http download URL leaves us nothing to record. This branch used to
			// swallow that, so the grab was written with an empty info_hash and every
			// later lookup — seed rules, stall detection, import — fell back to matching
			// the indexer's listing title against the torrent's own name. Those differ
			// often enough (an "Atmos" in the listing that isn't in the release name) to
			// strand the download as unmanaged, which is what the Downloads page reports
			// as "no seed rule matched". Say so, so it's diagnosable rather than silent.
			c.log.Warn("grab: no info hash in the download link — seed rules will fall back to name matching",
				"release", title, "indexer", indexerName, "err", herr)
		}
	default:
		return "", fmt.Errorf("nothing to download for this release")
	}
	if err := c.downloads.Add(ctx, add); err != nil {
		return "", err
	}
	c.clearRemovedImport(ctx, hash)
	c.bus.Publish("release.grabbed", map[string]any{"title": title, "indexer": indexerName, "network": string(download.TransportTorrent), "media_type": c.grabMediaType(category)})
	return hash, nil
}

// grabMediaType is what a grab is, read from the download-client category it was filed
// under, for the "Grabbed" alert's webhook data ("" when the category says nothing).
func (c *Coordinator) grabMediaType(category string) string {
	switch category {
	case download.CategoryTV:
		return "series"
	case download.CategoryBooks:
		return "book"
	case download.CategoryMusic:
		return "music"
	case c.movieCategory():
		return "movie"
	}
	return ""
}

// indexerTransport is the transport of the named indexer's releases: usenet for a Newznab
// indexer, torrent for everything else — including a name no indexer has ("manual", a
// removed indexer) and an unreadable indexer list, which is what grabbing has always done.
func (c *Coordinator) indexerTransport(ctx context.Context, indexerName string) download.Transport {
	if c.indexers == nil {
		return download.TransportTorrent
	}
	idxs, err := c.indexers.List(ctx)
	if err != nil {
		return download.TransportTorrent
	}
	for _, ix := range idxs {
		if ix.Name == indexerName && ix.Transport() == indexer.TransportUsenet {
			return download.TransportUsenet
		}
	}
	return download.TransportTorrent
}

// grabUsenet hands a usenet release to a usenet client and returns its download key
// ("sab-…"), which is what the grab records.
//
// The NZB is fetched here and sent as a file, not as a link for the client to fetch: an
// indexer's NZB link carries its API key, which would then sit in the client's history.
// The one exception is an indexer that blocks Arrmada's download (a Cloudflare page where
// the NZB should be): then the client, which the indexer admits, is handed the link
// (nzbhandoff.go). Any other failed fetch fails the grab.
func (c *Coordinator) grabUsenet(ctx context.Context, indexerName, downloadURL, title, category string) (string, error) {
	if !c.downloads.HasTransport(ctx, download.TransportUsenet) {
		return "", fmt.Errorf("%q is a usenet indexer and no usenet download client is configured — this release can't be downloaded", indexerName)
	}
	fetch := c.fetchRelease
	if fetch == nil {
		fetch = c.indexers.Fetch
	}
	res, err := fetch(ctx, indexerName, downloadURL)
	var blocked *indexer.BlockedError
	if errors.As(err, &blocked) && blocked.ClientLink() != "" {
		return c.addNZBLink(ctx, blocked, title, category, indexerName)
	}
	if err != nil {
		return "", err
	}
	if len(res.File) == 0 {
		return "", fmt.Errorf("couldn't fetch the NZB for this release from %q", indexerName)
	}
	return c.addNZB(ctx, res.File, res.Filename, title, category, indexerName)
}

// addNZB sends NZB bytes to the usenet clients and returns the job's download key ("" when
// the client didn't say it — the grab is recorded all the same, so nothing grabs it twice).
func (c *Coordinator) addNZB(ctx context.Context, file []byte, filename, title, category, source string) (string, error) {
	if filename == "" {
		filename = "arrmada.nzb"
	}
	// Always the Downloads folder: SABnzbd files by category, with no folder per job, so a
	// library folder's own downloads folder is for torrents only (grabfolder.go).
	added, err := c.downloads.AddDownload(ctx, download.AddRequest{
		Name: title, SavePath: c.usenetSavePath(ctx), Category: category,
		File: file, Filename: filename, Transport: download.TransportUsenet,
	})
	if err != nil {
		return "", err
	}
	c.clearRemovedImport(ctx, added.Key)
	c.bus.Publish("release.grabbed", map[string]any{"title": title, "indexer": source, "network": string(download.TransportUsenet), "media_type": c.grabMediaType(category)})
	return added.Key, nil
}

// looksLikeNZB reports whether an uploaded file is an NZB rather than a .torrent. A
// torrent is a bencoded dictionary and always starts with "d"; an NZB is XML.
func looksLikeNZB(file []byte, filename string) bool {
	if len(file) == 0 || file[0] == 'd' {
		return false
	}
	if strings.HasSuffix(strings.ToLower(filename), ".nzb") {
		return true
	}
	head := file
	if len(head) > 4096 {
		head = head[:4096]
	}
	return bytes.Contains(bytes.ToLower(head), []byte("<nzb"))
}

// clearRemovedImport lets a release whose imported file the user deleted import again,
// now that it has been grabbed again on purpose. Without a new grab, the deletion
// stands and the still-seeding torrent is left alone.
func (c *Coordinator) clearRemovedImport(ctx context.Context, hash string) {
	if hash == "" || c.db == nil {
		return
	}
	if _, err := c.db.ExecContext(ctx, `DELETE FROM imports WHERE lower(download_hash) = ? AND removed = 1`, strings.ToLower(hash)); err != nil {
		c.log.Warn("grab: couldn't clear a deleted import's flag", "err", err)
	}
}

// addTorrentFile hands an uploaded .torrent file straight to the download client (no
// indexer fetch, so it works for private trackers where the user downloaded the file
// while logged in), in the given category, recorded as a manual grab source. An uploaded
// .nzb goes to the usenet clients instead, and its key is the usenet job's.
//
// It answers the download's key and what was uploaded, which the grab is recorded with
// even when the client didn't say the key (withUploadedTransport).
func (c *Coordinator) addTorrentFile(ctx context.Context, file []byte, filename, title, category string) (string, download.Transport, error) {
	if len(file) == 0 {
		return "", "", fmt.Errorf("no torrent file provided")
	}
	if looksLikeNZB(file, filename) {
		key, err := c.addNZB(ctx, file, filename, title, category, "manual")
		return key, download.TransportUsenet, err
	}
	if filename == "" {
		filename = "arrmada.torrent"
	}
	add := download.AddRequest{Name: title, SavePath: c.savePath(ctx), Category: category, File: file, Filename: filename}
	if err := c.downloads.Add(ctx, add); err != nil {
		return "", "", err
	}
	hash, _ := download.InfoHashFromFile(file) // "" falls back to name matching
	c.bus.Publish("release.grabbed", map[string]any{"title": title, "indexer": "manual", "network": string(download.TransportTorrent), "media_type": c.grabMediaType(category)})
	c.clearRemovedImport(ctx, hash)
	return hash, download.TransportTorrent, nil
}

// uploadedEvent is the history line for an uploaded file: an .nzb isn't a torrent.
func uploadedEvent(t download.Transport, title string) string {
	if t == download.TransportUsenet {
		return "Uploaded NZB — " + title
	}
	return "Uploaded torrent — " + title
}

// GrabMovieTorrent adds an uploaded .torrent file for a movie (movie category) and
// tracks it like an auto grab so seed cleanup / stall detection manage it too.
// versionID is the track it's for (0 = the default track).
func (c *Coordinator) GrabMovieTorrent(ctx context.Context, movieID, versionID int64, file []byte, filename, title string) error {
	ctx = c.forMovieID(ctx, movieID)
	hash, tr, err := c.addTorrentFile(ctx, file, filename, title, c.movieCategory())
	if err != nil {
		return err
	}
	ctx = withUploadedTransport(ctx, tr)
	c.RecordManualGrab(ctx, movieID, versionID, title, "manual", hash)
	c.markGrabManual(ctx, hash) // the user chose this file; don't gate it on score
	if c.movies != nil {
		c.movies.AddEvent(ctx, movieID, "grabbed", uploadedEvent(tr, title))
	}
	return nil
}

// GrabSeriesTorrent adds an uploaded .torrent file for a series (TV category, so the
// multi-file importer handles a pack) and records the grab.
func (c *Coordinator) GrabSeriesTorrent(ctx context.Context, seriesID int64, file []byte, filename, title string) error {
	ctx = c.forSeriesID(ctx, seriesID)
	hash, tr, err := c.addTorrentFile(ctx, file, filename, title, seriesCategory)
	if err != nil {
		return err
	}
	ctx = withUploadedTransport(ctx, tr)
	if c.series != nil {
		if s, err := c.series.Get(ctx, seriesID); err == nil {
			c.recordSeriesGrab(ctx, seriesID, title, "manual", s.QualityProfile, hash)
		}
		// An uploaded torrent is as deliberate as a choice gets — import it whatever it
		// scores against what's already there.
		c.markGrabManual(ctx, hash)
		c.series.AddEvent(ctx, seriesID, "grabbed", uploadedEvent(tr, title))
	}
	return nil
}

// GrabBookTorrent adds an uploaded .torrent file for a book and records the grab.
//
// Book trackers are the ones you most often have to reach into by hand: a private tracker
// may hold the only copy of an edition, and a title that search can't phrase a query for
// (an author's initials punctuated differently, a series name the listing spells its own
// way) is otherwise unreachable from inside Arrmada.
//
// The book category matters — it's what routes the finished download through the book
// importer, which knows an ebook from an audiobook and hardlinks each edition into place.
func (c *Coordinator) GrabBookTorrent(ctx context.Context, bookID, versionID int64, file []byte, filename, title string) error {
	if versionID > 0 && c.books != nil {
		if _, err := c.books.GetAudioVersion(ctx, bookID, versionID); err != nil {
			return err
		}
	}
	hash, tr, err := c.addTorrentFile(ctx, file, filename, title, bookCategory)
	if err != nil {
		return err
	}
	ctx = withUploadedTransport(ctx, tr)
	if c.books != nil {
		if b, err := c.books.Get(ctx, bookID); err == nil {
			// "manual" as the indexer, same as the movie and series paths: seedRules
			// treats an unknown indexer as seed-for-the-standard-window rather than
			// don't-seed, so an uploaded torrent from a private tracker isn't deleted
			// the moment it imports.
			c.recordBookGrab(ctx, bookID, versionID, title, "manual", b.QualityProfile, hash)
			c.markGrabManual(ctx, hash) // imports into this book whatever the torrent is called
		}
		c.books.AddEvent(ctx, bookID, "grabbed", uploadedEvent(tr, title))
	}
	return nil
}

// RecordManualGrab tracks a release grabbed by hand (interactive search) exactly
// like an automatic grab, so it's seed-managed and stall-detected too. movieID 0
// (not tied to a tracked movie) is a no-op.
//
// versionID is the track it was grabbed for (0 = the default track): the grab is
// recorded with that track's profile and stall window, and its download imports into
// that track. A track that isn't the movie's falls back to the default track.
func (c *Coordinator) RecordManualGrab(ctx context.Context, movieID, versionID int64, title, indexerName, infoHash string) {
	if movieID == 0 {
		return
	}
	versions, err := c.movies.VersionRows(ctx, movieID)
	if err != nil || len(versions) == 0 {
		return
	}
	v := versions[0]
	for _, o := range versions {
		if o.ID == versionID {
			v = o
		}
	}
	profile := c.effectiveProfile(ctx, v.QualityProfile, quality.MediaMovie)
	// When the track has a file, this grab replaces it.
	replaces := ""
	if v.HasFile {
		replaces = v.FilePath
	}
	c.recordGrabReplacing(ctx, movieID, v.ID, title, indexerName, profile, c.quality.StallMinutes(ctx, profile), infoHash, replaces)
}

// SearchMissing searches for and grabs any monitored version that has no file
// and isn't already downloading, across every movie.
//
// Only movies with a monitored, file-less track are visited (chosen in SQL), and every
// read below is a database read: a library whose movies all have their files costs this
// sweep no searches, no ffprobe and no stat.
func (c *Coordinator) SearchMissing(ctx context.Context) {
	ctx = WithDefaultSearchTrigger(ctx, TriggerSweep)
	all, err := c.movies.SearchTargets(ctx)
	if err != nil {
		c.log.Warn("automation: list movies failed", "err", err)
		return
	}
	queue, ok := c.sweepQueue(ctx, "movie search sweep")
	if !ok {
		return
	}
	untracked, err := c.untrackedQueue(ctx, queue)
	if err != nil {
		c.log.Warn("automation: movie search sweep skipped — can't read what's already downloading", "err", err)
		return
	}
	// Films added in bulk (a Radarr import) are searched a few per run, not all at once.
	marked, err := c.movies.PacedSearchIDs(ctx)
	if err != nil {
		c.log.Warn("automation: movie search sweep skipped — can't read which imported films wait their turn", "err", err)
		return
	}
	pace := newPacer(marked, func(ctx context.Context, id int64) error { return c.movies.SetSearchPaced(ctx, id, false) }, c.log, "movie")
	var outage outageTally
	defer outage.report(c.log, "movie search sweep")
	for _, m := range all {
		if !c.movies.IsAvailable(m) {
			continue // not yet at its minimum-availability threshold
		}
		if untrackedMovie(untracked, m, c.movieKeys(ctx, m)) {
			continue // a torrent nobody grabbed through Arrmada is already fetching it
		}
		if c.MovieSearchBusy(m.ID) {
			continue // a search someone started is queued or running; it covers this one
		}
		// Versions already downloading are left out by searchAndGrab (wantedVersions),
		// by the acquisition record — whatever the torrent is called.
		// Exponential backoff for a movie that keeps finding nothing grabbable — an
		// unreleased title, or one whose every result is for a different film of the
		// same name, used to cost a full multi-indexer search every cycle forever.
		// Same policy as the series sweep (migration 0055); this is migration 0061.
		lastAt, misses := c.movies.SearchState(ctx, m.ID)
		if wait := searchBackoff(misses); wait > 0 {
			if last := parseTime(lastAt); !last.IsZero() && time.Since(last) < wait {
				continue
			}
		}
		if pace.hold(m.ID) {
			continue // imported in bulk: its turn comes in a later run
		}
		out, err := c.searchAndGrab(ctx, m)
		n, searched := out.Grabbed, out.Searched
		if errors.Is(err, ErrAlreadySearching) {
			c.log.Debug("automation: skipping a movie that is already being searched", "movie", m.Title)
			continue
		}
		if outage.note(err) {
			if outage.stop() {
				break // the indexers are down: the rest would only fail the same way
			}
			continue
		}
		pace.searched(ctx, m.ID)
		if err != nil {
			c.log.Warn("automation: search failed", "movie", m.Title, "err", err)
		}
		reset, miss := sweepOutcome(err, searched, n)
		if reset {
			c.movies.ResetSearchMisses(ctx, m.ID)
		}
		if miss {
			c.movies.RecordSearchMiss(ctx, m.ID)
		}
	}
}

// RankedRelease is one interactive-search result, ranked and explained in
// plain language (no scores — that's the Simple-view mandate).
type RankedRelease struct {
	Title   string `json:"title"`
	Indexer string `json:"indexer"`
	// DownloadURL never leaves the server: it can carry an indexer apikey or a tracker's
	// personal download token. The browser gets Token instead, and grabs by it.
	DownloadURL  string  `json:"-"`
	InfoHash     string  `json:"-"`
	Token        string  `json:"token,omitempty"`
	InfoURL      string  `json:"info_url,omitempty"` // the release's details page on the tracker, credentials stripped
	SizeGB       float64 `json:"size_gb"`
	Bitrate      float64 `json:"bitrate_mbps,omitempty"` // size ÷ runtime; 0 when runtime unknown
	Seeders      int     `json:"seeders"`
	Summary      string  `json:"summary"` // "4K · DV+HDR10 · BluRay"
	Eligible     bool    `json:"eligible"`
	RejectReason string  `json:"reject_reason,omitempty"`
	Recommended  bool    `json:"recommended"`
	Blocklisted  bool    `json:"blocklisted,omitempty"`

	// Peers is what the indexer reports beside seeders (leechers, or the whole swarm on
	// Torznab); PublishedAt is when the release was posted (RFC3339, "" when unknown).
	Peers       int    `json:"peers"`
	PublishedAt string `json:"published_at,omitempty"`
	Transport   string `json:"transport,omitempty"` // torrent | usenet

	// Resolves says which library episode(s) this release actually maps to, e.g.
	// "S17E45". An anime arc is numbered in its own universe — "S04E06" is the arc's
	// fourth cour, not the show's fourth season — so the release's own label looks like
	// the wrong season entirely until you say what it resolved to.
	Resolves string `json:"resolves,omitempty"`
	// Books only:
	Edition  string `json:"edition,omitempty"`  // ebook | audiobook
	Format   string `json:"format,omitempty"`   // EPUB, M4B, MP3…
	Narrator string `json:"narrator,omitempty"` // audiobook narrator, when detected
	Author   string `json:"author,omitempty"`   // structured author (e.g. from MyAnonaMouse)
	Series   string `json:"series,omitempty"`   // structured series + number
	Language string `json:"language,omitempty"` // language code/name when known
	// VersionID/Version name the extra audiobook version this release belongs to (its
	// words match the version's search terms); 0/"" is the standard audiobook.
	VersionID int64  `json:"version_id,omitempty"`
	Version   string `json:"version,omitempty"`

	// Why an eligible row ranks where it does (explainRow): the formats it matched and the
	// ones it has that the profile avoids, whether its preferences were waived because its
	// bitrate collapsed, and what it lost on against the release ranked just above it.
	Matched     []string `json:"matched,omitempty"`
	Avoided     []string `json:"avoided,omitempty"`
	BonusWaived bool     `json:"bonus_waived,omitempty"`
	LostOn      string   `json:"lost_on,omitempty"`     // "lower resolution", "fewer seeders"…
	LostOnKey   string   `json:"lost_on_key,omitempty"` // the quality.RankKey behind LostOn
	// AudioLangs are the audio languages the name carries as the profile's language rules
	// read it (base codes), and AudioLangsNote qualifies them ("not tagged — read as the
	// original language"). Set on every row, eligible or not.
	AudioLangs     []string `json:"audio_langs,omitempty"`
	AudioLangsNote string   `json:"audio_langs_note,omitempty"`
}

// ReleaseList is the interactive-search response for one movie.
type ReleaseList struct {
	Profile  string          `json:"profile"`
	Why      []string        `json:"why,omitempty"` // why the recommended release won
	Releases []RankedRelease `json:"releases"`
	// ChosenOver names the runner-up and what the recommended release beat it on.
	ChosenOver string `json:"chosen_over,omitempty"`
	// IndexerIssues names the indexers that couldn't answer, so an empty list can say
	// "every indexer failed" rather than "nothing found". Searched is how many indexers
	// were asked.
	IndexerIssues []IndexerIssue `json:"indexer_issues,omitempty"`
	Searched      int            `json:"searched"`
}

// RankReleases runs an interactive search: it queries indexers for the movie,
// scores every release against its quality profile, and returns them ranked
// (best first) with a plain-language summary — WITHOUT grabbing anything.
// tagRuntime stamps the movie's runtime (minutes) onto each candidate so the profile's bitrate
// ceiling can turn a release's size into a bitrate. 0 leaves the ceiling inert. Series
// candidates get theirs per release from newSeriesCandidate (a pack sums its episodes).
func tagRuntime(cands []quality.Candidate, runtimeMin int) []quality.Candidate {
	if runtimeMin <= 0 {
		return cands
	}
	for i := range cands {
		cands[i].RuntimeMin = runtimeMin
	}
	return cands
}

// tagMovie attaches what the profile needs to know about the movie to each candidate: its
// runtime (the bitrate window) and its original language (the Unwanted pack's foreign-only
// check). Every movie decision — the sweep, RSS, upgrades, re-grabs and the interactive
// list — tags through here, so a release is judged the same wherever it is looked at.
func tagMovie(cands []quality.Candidate, m movies.Movie) []quality.Candidate {
	cands = tagRuntime(cands, m.Runtime)
	if lang := movieOrigLanguage(m); lang != "" {
		for i := range cands {
			cands[i] = cands[i].WithLanguage(lang)
		}
	}
	return cands
}

// movieOrigLanguage is TMDB's original language for the movie, "" when unknown.
func movieOrigLanguage(m movies.Movie) string {
	if m.Extra == nil {
		return ""
	}
	return m.Extra.OriginalLanguage
}

// bitrateMbps is a release's average bitrate from its GiB size and a minutes runtime (0 when the
// runtime is unknown). Same maths as the quality profile's bitrate ceiling.
func bitrateMbps(sizeGB float64, runtimeMin int) float64 {
	if sizeGB <= 0 || runtimeMin <= 0 {
		return 0
	}
	return sizeGB * (1024 * 1024 * 1024 * 8 / 1e6) / float64(runtimeMin*60)
}

// decideWith decides under spec when it's set, else under the profile reference.
func (c *Coordinator) decideWith(ctx context.Context, ref string, spec *quality.StoredProfile, cands []quality.Candidate) quality.Decision {
	if spec != nil {
		return c.quality.DecideSpec(*spec, cands)
	}
	return c.quality.Decide(ctx, ref, cands)
}

// effectiveProfile substitutes the user's configured default profile when a title carries
// no real profile of its own — "n/a", as library-scanned movies and series do, or a
// profile that has since been deleted. Without it, scoring falls back to a generic preset
// that PREFERS Dolby Vision, HDR10 and Atmos (so a manual search recommended a Dolby
// Vision release even to someone who set DV to Avoid), and for a deleted profile it also
// turned off upgrades and stall fail-over without a word. Every acquisition path resolves
// through here, so a title is judged the same way wherever it is looked at; only when
// there are no profiles at all does the generic fallback remain.
func (c *Coordinator) effectiveProfile(ctx context.Context, profile, mediaType string) string {
	ref := c.quality.Effective(ctx, profile, mediaType)
	if ref != profile && profile != "" && profile != "n/a" && c.log != nil {
		if _, seen := c.danglingLogged.LoadOrStore(profile, true); !seen {
			c.log.Warn("quality: title's profile no longer exists — using the default",
				"ref", profile, "default", ref, "media", mediaType)
		}
	}
	return ref
}

func (c *Coordinator) RankReleases(ctx context.Context, id int64) (ReleaseList, error) {
	return c.RankReleasesWith(ctx, id, nil)
}

// RankReleasesWith ranks a movie's releases under spec instead of the movie's own profile
// when spec is set — the quality builder's "test on a real title", run with unsaved edits.
func (c *Coordinator) RankReleasesWith(ctx context.Context, id int64, spec *quality.StoredProfile) (ReleaseList, error) {
	return c.rankReleases(ctx, id, -1, spec)
}

// RankReleasesFor ranks a movie's releases for one of its tracks (0 = the default track):
// under that track's profile, with the releases it can't take — another edition, or the
// release another track's file came from — listed as not eligible, saying why. A track
// that isn't the movie's is movies.ErrNotFound.
func (c *Coordinator) RankReleasesFor(ctx context.Context, id, versionID int64, spec *quality.StoredProfile) (ReleaseList, error) {
	if versionID < 0 {
		return ReleaseList{}, movies.ErrNotFound
	}
	return c.rankReleases(ctx, id, versionID, spec)
}

// rankReleases is RankReleasesWith for track versionID, or for the movie as a whole (its
// own profile, no track filter) when versionID is -1.
func (c *Coordinator) rankReleases(ctx context.Context, id, versionID int64, spec *quality.StoredProfile) (ReleaseList, error) {
	m, err := c.movies.Get(ctx, id)
	if err != nil {
		return ReleaseList{}, err
	}
	profileRef := m.QualityProfile
	var track *movies.Version
	var siblings []movies.Version
	if versionID >= 0 {
		if siblings, err = c.movies.VersionRows(ctx, id); err != nil {
			return ReleaseList{}, err
		}
		for i := range siblings {
			if siblings[i].ID == versionID {
				track = &siblings[i]
			}
		}
		if track == nil {
			return ReleaseList{}, movies.ErrNotFound
		}
		profileRef = track.QualityProfile
	}
	ctx, notes := withSearchNotes(ctx)
	keys := c.movieKeys(ctx, m)
	result, err := c.searchMovie(ctx, m, keys)
	if allIndexersFailed(err) {
		// Nobody could answer: an empty list naming each failure, not an error that
		// reads like the search itself broke.
		return ReleaseList{Profile: c.effectiveProfile(ctx, profileRef, "movie")}.withIssues(notes), nil
	}
	if err != nil {
		return ReleaseList{}, err
	}

	blocked, err := c.blockedSet(ctx, m.ID)
	if err != nil {
		return ReleaseList{}, err
	}
	// The recommendation is decided over exactly what auto-grab decides over (candidatesFrom:
	// grabbable, deduped, not blocklisted), so Recommended is the release a search would
	// take. Blocklisted releases and those no client can take are still listed, ranked on
	// their own.
	nets := c.networks(ctx)
	usable, unusable := nets.split(matchingMovieReleases(m, keys, result.Releases))
	byName := make(map[string]indexer.Release, len(usable)+len(unusable))
	var pool, side, offTrack []quality.Candidate
	// offReason is why a release can't go to this track (its edition filter), by title.
	offReason := map[string]string{}
	for _, rel := range append(usable, unusable...) {
		byName[rel.Title] = rel
		cand := releaseCandidate(rel)
		switch {
		case !nets.canGrab(rel) || blocked[normTitle(rel.Title)]:
			side = append(side, cand)
		case track != nil && len(editionFilter(nil, []quality.Candidate{cand}, *track, siblings)) == 0:
			offReason[rel.Title] = offTrackReason(cand, *track, siblings)
			offTrack = append(offTrack, cand)
		default:
			pool = append(pool, cand)
		}
	}
	profile := c.effectiveProfile(ctx, profileRef, "movie")
	decision := c.decideWith(ctx, profile, spec, tagMovie(pool, m))
	others := c.decideWith(ctx, profile, spec, tagMovie(side, m))
	off := c.decideWith(ctx, profile, spec, tagMovie(offTrack, m))

	winnerName := ""
	if decision.Winner != nil {
		winnerName = decision.Winner.Candidate.Name
	}
	out := make([]RankedRelease, 0, len(byName))
	// ranked: the release is in the main ranking (pool), so its place there can be explained.
	appendEval := func(ev quality.Evaluation, ranked bool) {
		rel := byName[ev.Candidate.Name]
		rr := RankedRelease{
			Title:        ev.Candidate.Name,
			Indexer:      rel.Indexer,
			DownloadURL:  rel.DownloadURL,
			InfoHash:     rel.InfoHash,
			InfoURL:      safeInfoURL(rel.InfoURL, rel.DownloadURL),
			SizeGB:       ev.Candidate.SizeGB,
			Bitrate:      bitrateMbps(ev.Candidate.SizeGB, m.Runtime),
			Seeders:      ev.Candidate.Seeders,
			Summary:      summarize(ev.Candidate.Release),
			Eligible:     ev.Eligible,
			RejectReason: ev.RejectReason,
			Recommended:  ev.Candidate.Name == winnerName,
			Blocklisted:  blocked[normTitle(ev.Candidate.Name)],
		}
		if why := offReason[ev.Candidate.Name]; why != "" {
			rr.Eligible, rr.Recommended, rr.RejectReason = false, false, why
		}
		out = append(out, explainRow(withIndexerFacts(rr, rel, nets), ev, ranked))
	}
	for _, ev := range decision.Eligible {
		appendEval(ev, true)
	}
	for _, evs := range [][]quality.Evaluation{others.Eligible, off.Eligible, decision.Rejected, others.Rejected, off.Rejected} {
		for _, ev := range evs {
			appendEval(ev, false)
		}
	}
	return ReleaseList{Profile: profile, Why: decision.Why, ChosenOver: decision.ChosenOver, Releases: out}.withIssues(notes), nil
}

// Why an interactive list can't grab a release: no download client of its network is set
// up (netPolicy.canGrab).
const (
	usenetReject  = "Usenet release — no usenet download client is set up"
	torrentReject = "Torrent release — no torrent download client is set up"
)

// withIndexerFacts adds what every interactive row shows from the indexer — its age,
// peers and transport — and makes a release no client can take ineligible, saying why,
// so it can never be the recommended pick.
func withIndexerFacts(rr RankedRelease, rel indexer.Release, nets netPolicy) RankedRelease {
	rr.Peers = rel.Peers
	rr.Transport = string(rel.Transport)
	if !rel.PublishedAt.IsZero() {
		rr.PublishedAt = rel.PublishedAt.UTC().Format(time.RFC3339)
	}
	if why := nets.rejectReason(rel); why != "" {
		rr.Eligible, rr.Recommended = false, false
		rr.RejectReason = why
	}
	return rr
}

// summarize renders a release's key attributes in plain language.
func summarize(r parser.Release) string {
	var parts []string
	switch r.Resolution {
	case parser.Res2160p:
		parts = append(parts, "4K")
	case "":
		// unknown resolution — skip
	default:
		parts = append(parts, string(r.Resolution))
	}
	if len(r.HDR) > 0 {
		parts = append(parts, strings.Join(r.HDR, "+"))
	}
	if r.Source != "" {
		parts = append(parts, string(r.Source))
	}
	if r.Streaming != "" {
		parts = append(parts, r.Streaming)
	}
	if r.Edition != "" {
		parts = append(parts, r.Edition)
	}
	if len(parts) == 0 {
		return "Standard quality"
	}
	return strings.Join(parts, " · ")
}

// SearchMovieManual is the movie page's Search button. It is SearchMovie, except that a
// movie with a download already in the client isn't searched at all — the way the sweep
// leaves it alone — so a click while it downloads can't grab a second copy. It says so
// ("Already downloading <release>") and records that as the attempt.
//
// Only the button: a re-search after a Block or a review runs while the old torrent may
// still be listed, and must not be stopped by it.
func (c *Coordinator) SearchMovieManual(ctx context.Context, id int64) (SearchOutcome, error) {
	m, err := c.movies.Get(ctx, id)
	if err != nil {
		return SearchOutcome{}, err
	}
	if c.downloads != nil && len(c.missingVersions(ctx, m.ID)) > 0 {
		// In flight by the acquisition record (whatever the torrent is called), or a
		// torrent nobody grabbed through Arrmada that is for this film. An unreadable
		// record or queue doesn't stop the search: searchAndGrab's busy-version and
		// pending-grab guards still keep a second copy from being grabbed.
		name := ""
		if acqs, aerr := c.Active(ctx, "movie", m.ID); aerr == nil && len(acqs) > 0 {
			name = acqs[0].Title
		} else if queue, qerr := c.downloads.Queue(ctx); qerr == nil {
			if untracked, uerr := c.untrackedQueue(ctx, queue); uerr == nil {
				name = untrackedMovieItem(untracked, m, c.movieKeys(ctx, m))
			}
		}
		if name != "" {
			out := SearchOutcome{Reason: ReasonAlreadyDownloading, Example: name}
			c.recordAttempt(ctx, nil, AttemptMovie, m.ID, "", &out, nil)
			return out, nil
		}
	}
	return c.searchAndGrab(ctx, m)
}

// SearchMovie searches for and grabs a single movie (manual trigger) and says what it
// found. A movie already being searched answers ErrAlreadySearching with that reason.
func (c *Coordinator) SearchMovie(ctx context.Context, id int64) (SearchOutcome, error) {
	m, err := c.movies.Get(ctx, id)
	if err != nil {
		return SearchOutcome{}, err
	}
	return c.searchAndGrab(ctx, m)
}

// searchAndGrab searches for a movie and grabs what the quality profile picks. The
// outcome says how many releases were grabbed, so the sweep can back off a movie that
// keeps coming up empty, and whether a search actually ran — a movie with nothing wanted
// costs no indexer query and must not count as a "miss" (that ratcheted every
// fully-downloaded movie to the 12h backoff cap, delaying the first real search when
// a file was later deleted or a new version track added). Its counts and reason are
// what the Search button reports.
func (c *Coordinator) searchAndGrab(ctx context.Context, m movies.Movie) (out SearchOutcome, err error) {
	// Every search that runs leaves a search_attempts row (searchlog.go).
	ctx, notes := newSearchNotes(ctx)
	defer func() { c.recordAttempt(ctx, notes, AttemptMovie, m.ID, "", &out, err) }()
	release, ok := c.claims.claim(movieKey(m.ID))
	if !ok {
		return SearchOutcome{Reason: ReasonAlreadySearching}, ErrAlreadySearching
	}
	defer release()
	want, inFlight, err := c.wantedVersions(ctx, m.ID)
	if err != nil {
		c.skipUnreadable(m.Title, err)
		return SearchOutcome{}, err
	}
	if len(want) == 0 {
		if inFlight {
			return SearchOutcome{Reason: ReasonAlreadyDownloading}, nil
		}
		return SearchOutcome{Reason: ReasonNothingWanted}, nil
	}
	out = SearchOutcome{Searched: true}
	keys := c.movieKeys(ctx, m)
	result, err := c.searchMovie(ctx, m, keys)
	if err != nil {
		out.noteSearchErr(err)
		return out, err
	}
	out.Returned = len(result.Releases)
	notes.consider(result.Releases)
	if len(result.Releases) == 0 {
		c.log.Info("automation: no releases found", "movie", m.Title)
		out.settle()
		return out, nil
	}
	// Only consider releases that are actually for THIS movie — a title search for a
	// short/common name (e.g. "Hope") returns unrelated films ("Romance at Hope
	// Ranch"), and the scorer would otherwise happily grab the wrong one.
	matching := matchingMovieReleases(m, keys, result.Releases)
	notes.dropped(result.Releases, matching, DropWrongTitle)
	out.Matching = len(matching)
	byName, cands, err := c.candidatesFrom(ctx, m.ID, matching)
	if err != nil {
		c.skipUnreadable(m.Title, err)
		return out, err
	}
	out.Usable = len(cands)
	// Say where the results went. A search that returns releases and grabs none looked
	// identical in the log to one that found nothing useful — the same movie re-searched
	// every cycle forever with no hint whether the releases were for a different film,
	// blocklisted, or simply below the quality profile's bar.
	if len(cands) == 0 {
		c.log.Info("automation: no usable releases",
			"movie", m.Title, "returned", len(result.Releases),
			"wrong_title", len(result.Releases)-len(matching), "blocklisted", len(matching))
	}
	out.GrabbedTitles = c.grabMissingTitles(ctx, m, want, byName, cands)
	out.Grabbed = len(out.GrabbedTitles)
	out.settle()
	return out, nil
}

// movieQuery is the indexer search for a movie: its title, and its year when known, plus
// its IMDb and TMDB ids, which an indexer whose caps take them is asked by instead (see
// indexer.TorznabSearcher.Search). Every movie search is built here.
func movieQuery(m movies.Movie) indexer.SearchQuery {
	text := m.Title
	if m.Year > 0 {
		text += " " + strconv.Itoa(m.Year)
	}
	return indexer.SearchQuery{Text: text, MediaType: indexer.MediaMovie, Limit: 100, IMDBID: m.IMDBID, TMDBID: m.TMDBID}
}

// searchMovie is a movie's indexer search (movieQuery) and, when nothing it returned is for
// the film, at most one more: a text search by the film's original title and year, when
// that title differs from its own, is in the Latin alphabet and survived the collision
// guard (movieKeys). A foreign film's releases are often named by its original title
// only, which neither the title query nor an indexer without id search finds.
func (c *Coordinator) searchMovie(ctx context.Context, m movies.Movie, keys []string) (indexer.SearchResult, error) {
	res, err := c.search(ctx, movieQuery(m))
	if err != nil {
		return res, err
	}
	text := originalTitleQuery(m, keys)
	if text == "" || len(matchingMovieReleases(m, keys, res.Releases)) > 0 {
		return res, nil
	}
	// Text only: where an indexer takes ids, the id search already ran as the primary.
	more, err := c.search(ctx, indexer.SearchQuery{Text: text, MediaType: indexer.MediaMovie, Limit: 100})
	if err != nil {
		// The primary search answered; this one only adds to it.
		c.log.Info("automation: original-title search failed", "movie", m.Title, "query", text, "err", err)
		return res, nil
	}
	c.log.Info("automation: searched by original title", "movie", m.Title, "query", text, "returned", len(more.Releases))
	res.Releases = append(res.Releases, more.Releases...)
	return res, nil
}

// originalTitleQuery is the text of the original-title search (searchMovie): the original
// title and year, or "" when there's no original title worth asking for.
func originalTitleQuery(m movies.Movie, keys []string) string {
	if m.Extra == nil || m.Extra.OriginalTitle == "" {
		return ""
	}
	k := titleKey(m.Extra.OriginalTitle)
	if k == titleKey(m.Title) || !slices.Contains(keys, k) {
		return "" // the same title, or one the keys left out (non-Latin, or another film's)
	}
	text := m.Extra.OriginalTitle
	if m.Year > 0 {
		text += " " + strconv.Itoa(m.Year)
	}
	return text
}

// movieKeys are the title keys a release for m may carry (movies.TitleKeys), guarded
// against the library's other films. When the library's titles can't be read the guard
// fails closed: the film's own title only.
func (c *Coordinator) movieKeys(ctx context.Context, m movies.Movie) []string {
	guard, err := c.titleGuard(ctx)
	if err != nil {
		c.log.Warn("automation: couldn't read the library's titles; matching by the movie's own title only", "movie", m.Title, "err", err)
		return []string{titleKey(m.Title)}
	}
	return movies.TitleKeys(m, guard)
}

// titleGuardTTL is how long movieKeys reuses the library's title keys. Short: a film added
// just now must start guarding its title against the others' alternatives promptly.
const titleGuardTTL = 10 * time.Second

// titleGuard is the library's own title keys, cached for titleGuardTTL.
func (c *Coordinator) titleGuard(ctx context.Context) (movies.PrimaryKeys, error) {
	c.guardMu.Lock()
	defer c.guardMu.Unlock()
	if c.guard != nil && time.Since(c.guardAt) < titleGuardTTL {
		return c.guard, nil
	}
	g, err := c.movies.PrimaryKeys(ctx)
	if err != nil {
		return nil, err
	}
	c.guard, c.guardAt = g, time.Now()
	return g, nil
}

// matchingMovieReleases keeps only releases that are for the movie (movieReleaseMatches
// under keys) — the guard that keeps auto-grab from picking a different film that merely
// shares a word with the title.
func matchingMovieReleases(m movies.Movie, keys []string, releases []indexer.Release) []indexer.Release {
	out := make([]indexer.Release, 0, len(releases))
	for _, rel := range releases {
		if movieReleaseMatches(rel, m, keys) {
			out = append(out, rel)
		}
	}
	return out
}

// missingVersions returns the monitored version tracks that still need a file, whether or
// not something is already downloading for them (see wantedVersions).
func (c *Coordinator) missingVersions(ctx context.Context, movieID int64) []movies.Version {
	versions, err := c.movies.VersionRows(ctx, movieID)
	if err != nil {
		return nil
	}
	var want []movies.Version
	for _, v := range versions {
		if v.Monitored && !v.HasFile {
			want = append(want, v)
		}
	}
	return want
}

// candidatesFrom builds the scoring candidates from a set of releases, dropping
// any that are blocklisted for this movie, ungrabbable (no client of their network), or
// duplicate copies of a title already kept (netPolicy.bestByTitle).
func (c *Coordinator) candidatesFrom(ctx context.Context, movieID int64, releases []indexer.Release) (map[string]indexer.Release, []quality.Candidate, error) {
	return c.candidatesExcluding(ctx, movieID, releases, nil)
}

// candidatesExcluding is candidatesFrom that also drops the normalized titles in exclude —
// the stalled release a fail-over is replacing, which isn't blocklisted yet because it
// stays put until something else is found. An unreadable blocklist is an error, so
// nothing is grabbed rather than everything looking clean.
func (c *Coordinator) candidatesExcluding(ctx context.Context, movieID int64, releases []indexer.Release, exclude map[string]bool) (map[string]indexer.Release, []quality.Candidate, error) {
	blocked, err := c.blockedSet(ctx, movieID)
	if err != nil {
		return nil, nil, err
	}
	notes := notesFrom(ctx)
	releases = c.networks(ctx).usable(ctx, releases)
	byName := make(map[string]indexer.Release, len(releases))
	cands := make([]quality.Candidate, 0, len(releases))
	for _, rel := range releases {
		if blocked[normTitle(rel.Title)] || exclude[normTitle(rel.Title)] {
			// The stalled release a fail-over is replacing counts as blocklisted: it is
			// on its way to the blocklist once something replaces it.
			notes.mark(rel.Title, DropBlocklisted)
			continue
		}
		byName[rel.Title] = rel
		cands = append(cands, releaseCandidate(rel))
	}
	return byName, cands, nil
}

// grabMissing grabs the best candidate for each still-missing version track.
// Shared by live search (searchAndGrab) and RSS sync — only the candidate source
// differs.
func (c *Coordinator) grabMissing(ctx context.Context, m movies.Movie, want []movies.Version, byName map[string]indexer.Release, cands []quality.Candidate) int {
	return len(c.grabMissingTitles(ctx, m, want, byName, cands))
}

// grabMissingTitles is grabMissing returning the release titles it grabbed, so a stall
// fail-over can say what replaced the stalled download.
func (c *Coordinator) grabMissingTitles(ctx context.Context, m movies.Movie, want []movies.Version, byName map[string]indexer.Release, cands []quality.Candidate) []string {
	return c.grabMissingTitlesExcept(ctx, m, want, byName, cands)
}

// grabMissingTitlesExcept is grabMissingTitles that doesn't count the grabs exceptGrabs as
// in flight — the stalled grab a fail-over is replacing, and its duplicate rows.
func (c *Coordinator) grabMissingTitlesExcept(ctx context.Context, m movies.Movie, want []movies.Version, byName map[string]indexer.Release, cands []quality.Candidate, exceptGrabs ...int64) []string {
	ctx = c.forMovie(ctx, m) // saved beside the film's folder when it has its own downloads folder
	var titles []string
	grabbed := map[string]bool{}
	pending, err := c.pendingGrabTitles(ctx, m.ID) // releases already grabbed for this movie, not yet imported
	if err != nil {
		c.skipUnreadable(m.Title, err)
		return nil
	}
	// Versions with a download already in flight, by the acquisition record. The pending
	// guard above only stops the SAME release; this stops a differently named one for a
	// version that's already on its way.
	busy, err := c.busyVersions(ctx, m.ID, exceptGrabs...)
	if err != nil {
		c.skipUnreadable(m.Title, err)
		return nil
	}
	// Every track of the movie, for the edition filter: a track's edition is a requirement,
	// and no track takes another's edition or the release another's file came from.
	siblings := want
	if c.movies != nil {
		if siblings, err = c.movies.VersionRows(ctx, m.ID); err != nil {
			c.skipUnreadable(m.Title, err)
			return nil
		}
	}
	// grabbedGB accumulates what this pass has already committed, so two version tracks
	// can't jointly overcommit the same free-space reading (the series path has done this).
	grabbedGB := 0.0
	for _, v := range want {
		if busy[v.ID] {
			continue // already downloading for this version
		}
		// Resolved once and used for the decision, the stall window and the grab record,
		// so a deleted profile means the default here rather than the permissive fallback.
		profile := c.effectiveProfile(ctx, v.QualityProfile, quality.MediaMovie)
		mine := editionFilter(notesFrom(ctx), cands, v, siblings)
		if len(mine) == 0 && len(cands) > 0 && v.Edition != "" {
			c.log.Info("automation: no release carried the edition this version wants",
				"movie", m.Title, "version", v.Label, "edition", v.Edition, "candidates", len(cands))
			continue
		}
		decision := c.quality.Decide(ctx, profile, tagMovie(mine, m))
		notesFrom(ctx).decided(decision)
		if decision.Winner == nil {
			// The profile rejected everything on offer. Silent before, which made a
			// too-strict profile look identical to an indexer returning nothing — and
			// the search repeated every cycle either way, with no way to tell which.
			if len(cands) > 0 {
				c.log.Info("automation: no release met the quality profile",
					"movie", m.Title, "version", v.Label,
					"profile", profile, "candidates", len(cands))
			}
			continue
		}
		winner := byName[decision.Winner.Candidate.Name]
		if grabbed[winner.DownloadURL] {
			continue
		}
		if pending[normTitle(winner.Title)] {
			notesFrom(ctx).mark(winner.Title, DropPending)
			continue // already grabbed this exact release and it's still in flight — don't loop
		}
		if !c.diskOKFor(ctx, winner.Indexer, grabbedGB+decision.Winner.Candidate.SizeGB) {
			c.log.Warn("automation: low disk, skipping grab", "movie", m.Title, "need_gb", decision.Winner.Candidate.SizeGB, "already_queued_gb", grabbedGB)
			c.movies.AddEvent(ctx, m.ID, "failed", "Not enough free disk space to grab "+winner.Title)
			continue
		}
		c.log.Info("automation: grabbing", "movie", m.Title, "version", v.Label, "release", winner.Title, "indexer", winner.Indexer)
		hash, err := c.Grab(ctx, winner.Indexer, winner.DownloadURL, winner.Title)
		if err != nil {
			c.log.Warn("automation: grab failed", "movie", m.Title, "version", v.Label, "err", err)
			continue
		}
		grabbed[winner.DownloadURL] = true
		titles = append(titles, winner.Title)
		grabbedGB += decision.Winner.Candidate.SizeGB
		c.recordGrab(ctx, m.ID, v.ID, winner.Title, winner.Indexer, profile, c.quality.StallMinutes(ctx, profile), hash)
		detail := winner.Title + " · " + winner.Indexer
		if !v.IsDefault {
			detail += " → " + v.Label
		}
		c.movies.AddEvent(ctx, m.ID, "grabbed", detail)
	}
	return titles
}

// diskOKFor reports whether there's room to grab a release of the given size,
// keeping a small safety buffer. If free space can't be measured (e.g. non-Linux
// dev), it doesn't block. It measures the folder the grab will save to: a torrent for a
// title whose library folder has its own downloads folder fills that disk, a usenet job
// (from indexerName's network) the Downloads folder's whatever the title.
func (c *Coordinator) diskOKFor(ctx context.Context, indexerName string, sizeGB float64) bool {
	free, ok := c.freeGB(c.grabSavePath(ctx, indexerName))
	if !ok {
		return true
	}
	return free >= sizeGB+diskBufferGB
}

// grabSavePath is the folder a release from indexerName would save to under ctx.
func (c *Coordinator) grabSavePath(ctx context.Context, indexerName string) string {
	if c.indexerTransport(ctx, indexerName) == download.TransportUsenet {
		return c.usenetSavePath(ctx)
	}
	return c.savePath(ctx)
}

// freeGB measures free space (diskspace.FreeGB; a test swaps it via freeGBFn).
func (c *Coordinator) freeGB(path string) (float64, bool) {
	if c.freeGBFn != nil {
		return c.freeGBFn(path)
	}
	return diskspace.FreeGB(path)
}

const diskBufferGB = 2.0

// RSSSync polls each indexer's recent-uploads feed and grabs anything that
// matches a monitored, still-missing movie — the promptly-and-gently way to
// catch new releases (vs a title search per movie on a timer).
func (c *Coordinator) RSSSync(ctx context.Context) {
	all, err := c.movies.SearchTargets(ctx) // only movies still missing a monitored track
	if err != nil {
		c.log.Warn("rss: list movies failed", "err", err)
		return
	}
	// Read before the feeds, so a down client costs no indexer queries.
	queue, ok := c.sweepQueue(ctx, "movie rss sync")
	if !ok {
		return
	}
	res, err := c.indexers.Recent(ctx, 100)
	if errors.Is(err, indexer.ErrNoIndexers) {
		return // no indexer has a feed — nothing to sync, and nothing to warn about every cycle
	}
	if err != nil {
		c.log.Warn("rss: fetch feeds failed", "err", err)
		return
	}
	res.Releases = withoutBookUploads(res.Releases) // the feed is shared with the book sweep
	if len(res.Releases) == 0 {
		return
	}
	untracked, err := c.untrackedQueue(ctx, queue)
	if err != nil {
		c.log.Warn("rss: movie sync skipped — can't read what's already downloading", "err", err)
		return
	}
	for _, m := range all {
		if !c.movies.IsAvailable(m) || untrackedMovie(untracked, m, c.movieKeys(ctx, m)) {
			continue
		}
		want, _, err := c.wantedVersions(ctx, m.ID)
		if err != nil {
			c.skipUnreadable(m.Title, err)
			continue
		}
		if len(want) == 0 {
			continue
		}
		matched := matchingMovieReleases(m, c.movieKeys(ctx, m), res.Releases)
		if len(matched) == 0 {
			continue
		}
		c.log.Info("rss: match", "movie", m.Title, "candidates", len(matched))
		mctx, notes := newSearchNotes(WithDefaultSearchTrigger(ctx, TriggerRSS))
		notes.consider(matched)
		byName, cands, err := c.candidatesFrom(mctx, m.ID, matched)
		if err != nil {
			c.skipUnreadable(m.Title, err)
			continue
		}
		// Recorded only when it grabs: the feed repeats the same uploads for hours, and a
		// row every 15 minutes saying they still don't fit would bury the real searches.
		if c.grabMissing(mctx, m, want, byName, cands) > 0 {
			out := SearchOutcome{Searched: true}
			c.recordAttempt(mctx, notes, AttemptMovie, m.ID, "", &out, nil)
		}
	}
}

// movieReleaseMatches reports whether an indexer's release is for the movie: its title is
// one of keys (movies.TitleKeys — its own, original or alternative title) with a year
// within one, or the indexer tagged it with the movie's own IMDb id and its year fits — a
// film listed under another title still carries its IMDb id. A release tagged with a
// DIFFERENT IMDb id is another film whatever it's called (a remake, a namesake).
//
// Coming back from an id search (MatchedByID) is deliberately not enough: an indexer that
// ignores the id answers with its newest uploads, which are anything.
func movieReleaseMatches(rel indexer.Release, m movies.Movie, keys []string) bool {
	tag := indexer.NormalizeIMDB(rel.IMDBID)
	own := indexer.NormalizeIMDB(m.IMDBID)
	if tag != "" && own != "" {
		if tag != own {
			return false
		}
		r := parser.Parse(rel.Title)
		return r.Year == 0 || m.Year == 0 || abs(r.Year-m.Year) <= 1
	}
	r := parser.Parse(rel.Title)
	return movies.TitleMatches(keys, m, r.Title, r.Year)
}

// UpgradeMovies sweeps every monitored movie that already has a file and grabs a
// better release when the profile allows upgrades and one clearly beats what's on
// disk. Runs on a timer alongside SearchMissing.
func (c *Coordinator) UpgradeMovies(ctx context.Context) {
	all, err := c.movies.UpgradeTargets(ctx) // monitored, with a file
	if err != nil {
		c.log.Warn("automation: list movies failed", "err", err)
		return
	}
	queue, err := c.downloads.Queue(ctx)
	if err != nil {
		// Without the queue, "already grabbing" can't be checked — skip this cycle
		// rather than risk stacking a second copy of a large upgrade. Upgrades are
		// not urgent; the next 6h sweep will run.
		c.log.Warn("automation: upgrade sweep skipped — can't read the download queue", "err", err)
		return
	}
	untracked, err := c.untrackedQueue(ctx, queue)
	if err != nil {
		c.log.Warn("automation: upgrade sweep skipped — can't read what's already downloading", "err", err)
		return
	}
	var outage outageTally
	defer outage.report(c.log, "movie upgrade sweep")
	budget := c.newSweepBudget(ctx)
	titlesLeft := 0
	defer func() { c.logBudget("movies", budget, titlesLeft) }()
	for i, m := range all {
		if budget.spent() {
			// Stop before the next indexer search: nothing found now could be grabbed.
			titlesLeft = len(all) - i
			break
		}
		if !m.Monitored || !m.HasFile {
			continue
		}
		if c.MovieSearchBusy(m.ID) {
			continue // a search someone started is queued or running for it
		}
		if busy, err := c.movieBusy(ctx, m, untracked); err != nil || busy {
			continue // already grabbing something for this movie (or that can't be told)
		}
		err := c.upgradeMovie(ctx, m, budget)
		if errors.Is(err, ErrAlreadySearching) {
			c.log.Debug("automation: skipping an upgrade search for a movie already being searched", "movie", m.Title)
			continue
		}
		if outage.note(err) {
			if outage.stop() {
				break
			}
			continue
		}
		if err != nil {
			c.log.Warn("automation: upgrade search failed", "movie", m.Title, "err", err)
		}
	}
}

// UpgradeMovie runs an upgrade search for a single movie (e.g. right after its
// quality profile is raised). A deliberate one-title action, so the sweep's upgrade
// budget doesn't apply.
func (c *Coordinator) UpgradeMovie(ctx context.Context, id int64) error {
	m, err := c.movies.Get(ctx, id)
	if err != nil {
		return err
	}
	if !m.Monitored || !m.HasFile {
		return nil
	}
	return c.upgradeMovie(ctx, m, nil)
}

// movieUpgrade is one version's chosen upgrade, before it's grabbed.
type movieUpgrade struct {
	v       movies.Version
	profile string
	from    string // the release the version holds now, for the log
	pick    quality.Candidate
}

// upgradeMovie searches and grabs an upgrade for any monitored version that
// already has a file. Versions without a file are handled by SearchMissing. b is the
// sweep's upgrade budget (nil = unlimited).
func (c *Coordinator) upgradeMovie(ctx context.Context, m movies.Movie, b *upgradeBudget) (err error) {
	// Recorded under scope "upgrade", so the Wanted view's summaries can leave it out.
	ctx, notes := newSearchNotes(WithDefaultSearchTrigger(ctx, TriggerUpgrade))
	defer func() {
		var out SearchOutcome
		c.recordAttempt(ctx, notes, AttemptMovie, m.ID, ScopeUpgrade, &out, err)
	}()
	release, ok := c.claims.claim(movieKey(m.ID))
	if !ok {
		return ErrAlreadySearching
	}
	defer release()
	// The upgrade downloads beside the folder the film is in now (after a Move to folder,
	// the new one's).
	ctx = c.forMovie(ctx, m)
	// Database rows only: the current size and quality come from the cached media info
	// (or the recorded release name), never from probing the file on every sweep.
	versions, err := c.movies.VersionRows(ctx, m.ID)
	if err != nil {
		return err
	}
	var want []movies.Version
	held := 0
	for _, v := range versions {
		// Include any monitored version with a file whose profile allows upgrades — regardless of
		// whether Arrmada grabbed it or found it on a library scan. The AllowsUpgrades gate keeps
		// us from indexer-searching movies on a non-upgrading profile.
		if !v.Monitored || !v.HasFile {
			continue
		}
		// A held file ("keep existing files" when its profile changed) is left as it is.
		if v.UpgradeHold {
			held++
			continue
		}
		if c.quality.AllowsUpgrades(ctx, c.effectiveProfile(ctx, v.QualityProfile, quality.MediaMovie)) {
			want = append(want, v)
		}
	}
	if len(want) == 0 {
		if held > 0 {
			c.log.Info("automation: nothing to upgrade — files are kept as they are (upgrades paused)",
				"movie", m.Title, "held", held)
		}
		return nil
	}

	keys := c.movieKeys(ctx, m)
	result, err := c.search(ctx, movieQuery(m))
	if err != nil {
		return err
	}
	notes.consider(result.Releases)
	if len(result.Releases) == 0 {
		return nil
	}

	blocked, err := c.blockedSet(ctx, m.ID)
	if err != nil {
		c.skipUnreadable(m.Title, err)
		return err
	}
	byName := make(map[string]indexer.Release, len(result.Releases))
	cands := make([]quality.Candidate, 0, len(result.Releases))
	for _, rel := range c.networks(ctx).usable(ctx, result.Releases) {
		if blocked[normTitle(rel.Title)] {
			notes.mark(rel.Title, DropBlocklisted)
			continue
		}
		if !movieReleaseMatches(rel, m, keys) {
			notes.mark(rel.Title, DropWrongTitle)
			continue
		}
		byName[rel.Title] = rel
		cands = append(cands, releaseCandidate(rel))
	}
	// Runtime on the candidates so the profile's bitrate ceiling applies to upgrades
	// too — without it a capped profile could "upgrade" to a remux it explicitly forbids.
	cands = tagMovie(cands, m)

	grabbed := map[string]bool{}
	grabbedGB := 0.0
	pending, err := c.pendingGrabTitles(ctx, m.ID)
	if err != nil {
		c.skipUnreadable(m.Title, err)
		return err
	}
	// Every release grabbed for each track before, whatever became of it: an upgrade is
	// tried once. A grab the import gate kept out (or found already imported) leaves the
	// track's file as it was, and without this the same release would win again on every
	// sweep — the loop the series sweep had (movieGrabbedTitles).
	tried, err := c.movieGrabbedTitles(ctx, m.ID)
	if err != nil {
		c.skipUnreadable(m.Title, err)
		return err
	}
	picks := c.movieUpgradePicks(ctx, m, want, versions, cands, tried)
	takeUpgrades(picks, b, func(u movieUpgrade) bool {
		winner := byName[u.pick.Name]
		if grabbed[winner.DownloadURL] {
			return false
		}
		if pending[normTitle(winner.Title)] {
			return false // this exact upgrade is already in flight — don't stack a second copy
		}
		if !c.diskOKFor(ctx, winner.Indexer, grabbedGB+u.pick.SizeGB) {
			c.log.Warn("automation: low disk, skipping upgrade", "movie", m.Title, "need_gb", u.pick.SizeGB)
			return false
		}
		c.log.Info("automation: upgrading", "movie", m.Title, "version", u.v.Label, "from", u.from, "to", winner.Title)
		hash, err := c.Grab(ctx, winner.Indexer, winner.DownloadURL, winner.Title)
		if err != nil {
			c.log.Warn("automation: upgrade grab failed", "movie", m.Title, "err", err)
			return false
		}
		grabbed[winner.DownloadURL] = true
		grabbedGB += u.pick.SizeGB
		c.recordGrabReplacing(ctx, m.ID, u.v.ID, winner.Title, winner.Indexer, u.profile, c.quality.StallMinutes(ctx, u.profile), hash, u.v.FilePath)
		detail := "Upgrade: " + winner.Title + " · " + winner.Indexer
		if !u.v.IsDefault {
			detail += " → " + u.v.Label
		}
		c.movies.AddEvent(ctx, m.ID, "grabbed", detail)
		return true
	})
	return nil
}

// movieUpgradePicks decides, for each version, the release that would upgrade its file —
// the decision half of upgradeMovie, apart from the grabbing so it can be tested without
// an indexer or a download client. tried is movieGrabbedTitles: a release grabbed for a
// track before is never that track's upgrade again.
func (c *Coordinator) movieUpgradePicks(ctx context.Context, m movies.Movie, want, siblings []movies.Version, cands []quality.Candidate, tried map[int64]map[string]bool) []movieUpgrade {
	var picks []movieUpgrade
	for _, v := range want {
		// A held file ("keep existing files" when its profile changed) is left as it is.
		// upgradeMovie already leaves them out of want, before searching; this keeps the
		// decision honest for any other caller.
		if v.UpgradeHold {
			continue
		}
		cur := c.currentMovieFile(ctx, m, v)
		profile := c.effectiveProfile(ctx, v.QualityProfile, quality.MediaMovie)
		// Only releases this track may take: its edition, never another track's file, never
		// one it has tried before.
		mine := editionFilter(notesFrom(ctx), untriedCands(cands, tried[v.ID]), v, siblings)
		if pick, ok := c.upgradePick(ctx, profile, cur, mine); ok {
			picks = append(picks, movieUpgrade{v: v, profile: profile, from: cur.Release, pick: pick})
		}
	}
	return picks
}

// movieGrabbedTitles is every release ever grabbed for a movie, by track (the grab's
// version_id, 0 for the default track) and then normTitle — imported, failed, removed,
// whatever became of it. The movie upgrade sweep tries a release once per track, as the
// series sweep does per show (seriesGrabbedTitles): after an upgrade arrives the import
// gate may still keep the old file, or find the download already imported, and the same
// release mustn't come round again every sweep.
//
// Per track, not per movie: the tracks are judged apart (each its own profile and
// edition), so a release one track tried can still be another's upgrade — editionFilter
// already keeps a release another track's file came from away from the rest. It fails
// closed like the blocklist: an unreadable history would make every tried release new.
func (c *Coordinator) movieGrabbedTitles(ctx context.Context, movieID int64) (map[int64]map[string]bool, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT version_id, title FROM grabs WHERE movie_id = ? AND media_type = 'movie'`, movieID)
	if err != nil {
		return nil, fmt.Errorf("grab history unreadable: %w", err)
	}
	defer rows.Close()
	out := map[int64]map[string]bool{}
	for rows.Next() {
		var vid int64
		var title string
		if err := rows.Scan(&vid, &title); err != nil {
			return nil, fmt.Errorf("grab history unreadable: %w", err)
		}
		if out[vid] == nil {
			out[vid] = map[string]bool{}
		}
		out[vid][normTitle(title)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("grab history unreadable: %w", err)
	}
	return out, nil
}

// untriedCands is cands without the releases in before (one track's movieGrabbedTitles).
func untriedCands(cands []quality.Candidate, before map[string]bool) []quality.Candidate {
	if len(before) == 0 {
		return cands
	}
	out := make([]quality.Candidate, 0, len(cands))
	for _, cand := range cands {
		if !before[normTitle(cand.Name)] {
			out = append(out, cand)
		}
	}
	return out
}

// UpgradeBaseline is the release string the movie upgrade sweep scores a version's file
// by, for callers outside the sweep that must judge the file the same way (a profile
// edit's dry run).
func UpgradeBaseline(m movies.Movie, v movies.Version) string { return upgradeBaseline(m, v) }

func gbOf(bytes int64) float64 { return float64(bytes) / (1024 * 1024 * 1024) }

// RegrabMovie grabs the best release under each monitored version's current
// profile even when a file already exists — a deliberate re-grab, used when the
// user switches to a different (e.g. lower) profile and chooses to replace their
// file. On import the new file replaces the old one.
func (c *Coordinator) RegrabMovie(ctx context.Context, id int64) (err error) {
	ctx, notes := newSearchNotes(WithSearchTrigger(ctx, TriggerReplace)) // a re-grab is a replace, whoever asked
	defer func() {
		var out SearchOutcome
		c.recordAttempt(ctx, notes, AttemptMovie, id, "", &out, err)
	}()
	m, err := c.movies.Get(ctx, id)
	if err != nil {
		return err
	}
	ctx = c.forMovie(ctx, m)
	versions, err := c.movies.VersionRows(ctx, m.ID)
	if err != nil {
		return err
	}
	keys := c.movieKeys(ctx, m)
	result, err := c.search(ctx, movieQuery(m))
	if err != nil {
		return err
	}
	notes.consider(result.Releases)
	if len(result.Releases) == 0 {
		return nil
	}
	blocked, err := c.blockedSet(ctx, m.ID)
	if err != nil {
		c.skipUnreadable(m.Title, err)
		return err
	}
	byName := make(map[string]indexer.Release, len(result.Releases))
	cands := make([]quality.Candidate, 0, len(result.Releases))
	for _, rel := range c.networks(ctx).usable(ctx, result.Releases) {
		if blocked[normTitle(rel.Title)] {
			notes.mark(rel.Title, DropBlocklisted)
			continue
		}
		if !movieReleaseMatches(rel, m, keys) {
			notes.mark(rel.Title, DropWrongTitle)
			continue
		}
		byName[rel.Title] = rel
		cands = append(cands, releaseCandidate(rel))
	}
	grabbed := map[string]bool{}
	grabbedGB := 0.0
	for _, v := range versions {
		if !v.Monitored {
			continue
		}
		profile := c.effectiveProfile(ctx, v.QualityProfile, quality.MediaMovie)
		decision := c.quality.Decide(ctx, profile, tagMovie(editionFilter(notes, cands, v, versions), m))
		notesFrom(ctx).decided(decision)
		if decision.Winner == nil {
			continue
		}
		winner := byName[decision.Winner.Candidate.Name]
		if grabbed[winner.DownloadURL] {
			continue
		}
		if !c.diskOKFor(ctx, winner.Indexer, grabbedGB+decision.Winner.Candidate.SizeGB) {
			c.log.Warn("automation: low disk, skipping regrab", "movie", m.Title, "need_gb", decision.Winner.Candidate.SizeGB)
			continue
		}
		hash, err := c.Grab(ctx, winner.Indexer, winner.DownloadURL, winner.Title)
		if err != nil {
			c.log.Warn("automation: regrab failed", "movie", m.Title, "err", err)
			continue
		}
		grabbed[winner.DownloadURL] = true
		grabbedGB += decision.Winner.Candidate.SizeGB
		replaces := ""
		if v.HasFile {
			replaces = v.FilePath
		}
		c.recordGrabReplacing(ctx, m.ID, v.ID, winner.Title, winner.Indexer, profile, c.quality.StallMinutes(ctx, profile), hash, replaces)
		c.movies.AddEvent(ctx, m.ID, "grabbed", "Re-grab: "+winner.Title+" · "+winner.Indexer)
	}
	return nil
}

// Blocklist adds a release to a movie's blocklist.
func (c *Coordinator) Blocklist(ctx context.Context, movieID int64, title, indexerName, downloadURL, reason string) error {
	return c.addBlock(ctx, movieID, title, indexerName, downloadURL, reason)
}

// Blocklisted lists a movie's blocklisted releases.
func (c *Coordinator) Blocklisted(ctx context.Context, movieID int64) ([]BlockEntry, error) {
	out, err := c.listBlocks(ctx, movieID)
	return c.withNetwork(ctx, out, err)
}

// withNetwork marks the usenet releases among blocklist entries.
func (c *Coordinator) withNetwork(ctx context.Context, out []BlockEntry, err error) ([]BlockEntry, error) {
	if err != nil {
		return out, err
	}
	c.markUsenetBlocks(ctx, len(out), func(i int) string { return out[i].Title }, func(i int, n string) { out[i].Network = n })
	return out, nil
}

// Unblock removes a blocklist entry.
func (c *Coordinator) Unblock(ctx context.Context, id int64) error { return c.removeBlock(ctx, id) }

// ErrNothingToBlock means a download isn't tied to anything in the library, so there is
// nothing to blocklist it against. Block leaves such a torrent alone; Remove is the way
// to take it out of the client.
var ErrNothingToBlock = errors.New("not linked to anything in your library — use Remove instead")

// BlockTarget is what a Block resolved to: the library item the release is blocked for.
// Kind is movie | series | book | music; for a music discography ID is the artist.
type BlockTarget struct {
	Kind  string `json:"kind"`
	ID    int64  `json:"id"`
	Title string `json:"title"`

	release     string // the release name blocklisted
	alsoBlock   string // the torrent's own name, when it differs from the grab's listing title
	indexer     string // where it came from, when a grab recorded it
	discography bool   // a music discography, recorded against the artist
}

// BlockRelease is the "block from the downloads list" action: resolve what the download
// is for, blocklist the release for that item, remove the torrent and its data, and
// search for another. name is the torrent/release name. See ResolveBlock and
// BlockResolved, which the API calls separately so it can answer with the title at once.
func (c *Coordinator) BlockRelease(ctx context.Context, hash, name string) (BlockTarget, error) {
	t, err := c.ResolveBlock(ctx, hash, name)
	if err != nil {
		return t, err
	}
	return t, c.BlockResolved(ctx, hash, t)
}

// ResolveBlock works out which library item a download should be blocked for — a DB read
// and at most one queue read, quick enough to answer the click with.
//
// The grab recorded for the info hash decides it whenever there is one: it says exactly
// what the release was grabbed for. Only without a grab does the release name decide,
// and then within the torrent's own category, so a TV torrent is never matched to a movie.
// That used to happen: "Fargo.S05E01…" carries no year, matched the Fargo movie first,
// blocklisted and re-searched the movie, and left the show free to grab it again. Books and
// music weren't handled at all, so blocking one deleted it and the next sweep grabbed it
// straight back.
func (c *Coordinator) ResolveBlock(ctx context.Context, hash, name string) (BlockTarget, error) {
	if g, status := c.grabForDownload(ctx, hash, name); g != nil && status != "" {
		// The grab's title is the indexer's listing — what a search will offer again — so
		// that's what is blocked. The torrent's own name is blocked too when it differs
		// (a tracker's prettified listing vs the .torrent's name).
		if t, ok := c.blockTargetFor(ctx, g.MediaType, g.MovieID, g.Title, g.Indexer); ok {
			if name != "" && normTitle(name) != normTitle(g.Title) {
				t.alsoBlock = name
			}
			return t, nil
		}
	}
	category := ""
	if c.downloads != nil {
		if queue, err := c.downloads.Queue(ctx); err == nil {
			for _, it := range queue {
				if strings.EqualFold(it.Hash, hash) {
					category = it.Category
					if name == "" {
						name = it.Name
					}
					break
				}
			}
		}
	}
	if name == "" {
		return BlockTarget{}, ErrNothingToBlock
	}
	kind := "movie"
	switch {
	case category == seriesCategory:
		kind = "series"
	case category == bookCategory:
		kind = "book"
	case category == musicCategory:
		kind = "music"
	case category == "" && parser.Parse(name).IsTV():
		// The torrent wasn't found in the queue (or carries no category): an episode or
		// season name is still never a movie's.
		kind = "series"
	}
	var id int64
	switch kind {
	case "series":
		if c.series != nil {
			if s, ok, _ := c.series.MatchRelease(ctx, parser.Parse(name)); ok {
				id = s.ID
			}
		}
	case "book":
		if c.books != nil {
			if b, ok := c.books.MatchByRelease(ctx, name); ok {
				id = b.ID
			}
		}
	case "music":
		if c.music != nil {
			if music.ParseRelease(name).Discography {
				// A discography names an artist, not an album; it's blocked for the artist.
				if a, ok := c.artistForRelease(ctx, name); ok {
					kind, id = mediaMusicArtist, a.ID
				}
			} else if al, _, ok := c.albumForRelease(ctx, name); ok {
				id = al.ID
			}
		}
	default:
		if c.movies != nil {
			if m, ok := c.movies.MatchRelease(ctx, name); ok {
				id = m.ID
			}
		}
	}
	if id > 0 {
		if t, ok := c.blockTargetFor(ctx, kind, id, name, ""); ok {
			return t, nil
		}
	}
	return BlockTarget{}, ErrNothingToBlock
}

// blockTargetFor names the library item a block applies to, checking it still exists.
func (c *Coordinator) blockTargetFor(ctx context.Context, kind string, id int64, release, indexerName string) (BlockTarget, bool) {
	t := BlockTarget{Kind: kind, ID: id, release: release, indexer: indexerName}
	switch kind {
	case "movie":
		if c.movies == nil {
			return t, false
		}
		m, err := c.movies.Get(ctx, id)
		if err != nil {
			return t, false
		}
		t.Title = titleYear(m.Title, m.Year)
	case "series":
		if c.series == nil {
			return t, false
		}
		s, err := c.series.Get(ctx, id)
		if err != nil {
			return t, false
		}
		t.Title = s.Title
	case "book":
		if c.books == nil {
			return t, false
		}
		b, err := c.books.Get(ctx, id)
		if err != nil {
			return t, false
		}
		t.Title = b.Title
	case "music":
		if c.music == nil {
			return t, false
		}
		al, err := c.music.GetAlbum(ctx, id)
		if err != nil {
			return t, false
		}
		t.Title = al.Title
	case mediaMusicArtist:
		// A discography is grabbed against the artist (see GrabDiscography), and its
		// blocklist rows are kept against the artist id too. It answers as music, so the
		// page that asked needn't know the difference.
		if c.music == nil {
			return t, false
		}
		a, err := c.music.GetArtist(ctx, id)
		if err != nil {
			return t, false
		}
		t.Kind, t.Title, t.discography = "music", a.Name+" (discography)", true
	default:
		return t, false
	}
	return t, true
}

// BlockResolved does the work of a Block once ResolveBlock has said what it's for:
// blocklist the release for that item (first, so a failed removal can't leave it
// grabbable), remove the torrent and its data, close out its grab and any review it was
// held in, and search for another release.
func (c *Coordinator) BlockResolved(ctx context.Context, hash string, t BlockTarget) error {
	const reason = "manually blocklisted"
	for _, title := range []string{t.release, t.alsoBlock} {
		if title == "" {
			continue
		}
		switch t.Kind {
		case "movie":
			if err := c.addBlock(ctx, t.ID, title, t.indexer, "", reason); err != nil {
				return err
			}
		case "series":
			c.addBlockSeries(ctx, t.ID, title, t.indexer, reason)
		case "book":
			c.addBlockBook(ctx, t.ID, title, t.indexer, reason)
		case "music":
			if t.discography {
				c.addBlockMusicArtist(ctx, t.ID, title, t.indexer, reason)
			} else {
				c.addBlockMusic(ctx, t.ID, title, t.indexer, reason)
			}
		default:
			return ErrNothingToBlock
		}
	}
	if err := c.dropTorrent(ctx, hash, true); err != nil {
		c.log.Warn("downloads: block — couldn't remove the torrent", "release", t.release, "err", err)
	}
	// Whatever it was, its grab row must not stay in flight — that would hold the
	// pending-grab guard and hide the block from stall detection.
	grabKind := t.Kind
	if t.discography {
		grabKind = mediaMusicArtist
	}
	c.setGrabStatusByHash(ctx, hash, t.release, grabKind, grabStatusFailed)
	if _, err := c.db.ExecContext(ctx,
		`UPDATE import_reviews SET status = 'resolved', resolution = ?, resolved_at = CURRENT_TIMESTAMP
		  WHERE lower(hash) = lower(?) AND status = 'pending'`, ResolutionRejected, hash); err != nil {
		c.log.Warn("downloads: block — couldn't settle the review", "hash", hash, "err", err)
	} else {
		c.reviewsChanged()
	}
	what := &grab{MediaType: t.Kind, MovieID: t.ID}
	var err error
	switch {
	case t.Kind == "music" && t.discography:
		// A discography is fetched only when asked for; the block is the whole answer.
		c.music.AddEvent(ctx, t.ID, "blocklisted", t.release)
	case t.Kind == "movie":
		c.addMediaEvent(ctx, what, "blocklisted", t.release)
		_, err = c.SearchMovie(ctx, t.ID)
	case t.Kind == "series":
		c.addMediaEvent(ctx, what, "blocklisted", t.release)
		_, err = c.SearchSeriesNow(ctx, t.ID)
	case t.Kind == "book":
		c.addMediaEvent(ctx, what, "blocklisted", t.release)
		_, err = c.SearchBookNow(ctx, t.ID)
	case t.Kind == "music":
		c.addMediaEvent(ctx, what, "blocklisted", t.release)
		_, err = c.SearchAlbumNow(ctx, t.ID)
	}
	if errors.Is(err, ErrAlreadySearching) {
		return nil // the search in flight runs without the blocked release
	}
	return err
}

// BlocklistAndSearch blocklists a release then re-searches for an alternate, and says
// what that search found.
func (c *Coordinator) BlocklistAndSearch(ctx context.Context, movieID int64, title, indexerName, downloadURL string) (SearchOutcome, error) {
	if err := c.addBlock(ctx, movieID, title, indexerName, downloadURL, "manually blocklisted"); err != nil {
		return SearchOutcome{}, err
	}
	c.movies.AddEvent(ctx, movieID, "blocklisted", title)
	return c.SearchMovie(ctx, movieID)
}

// --- series blocklist + per-episode actions (mirrors the movie surface) ---

// BlocklistedSeries lists a series' blocklisted releases.
func (c *Coordinator) BlocklistedSeries(ctx context.Context, seriesID int64) ([]BlockEntry, error) {
	out, err := c.listBlocksSeries(ctx, seriesID)
	return c.withNetwork(ctx, out, err)
}

// BlocklistSeries adds a release to a series' blocklist (so a re-search won't pick it again).
func (c *Coordinator) BlocklistSeries(ctx context.Context, seriesID int64, title, indexer, reason string) error {
	c.addBlockSeries(ctx, seriesID, title, indexer, reason)
	if c.series != nil {
		c.series.AddEvent(ctx, seriesID, "blocklisted", title)
	}
	return nil
}

// RegrabEpisode replaces one episode: blocklist its current release (so the same one isn't
// re-selected), then search + grab the best available for that episode.
//
// The blocklist entry must be the episode's SOURCE RELEASE, not its library filename:
// library files are renamed to a clean scheme with no release tags, so a filename-keyed
// entry matches no indexer release — the regrab would happily re-select the identical
// release, download the same bytes, and the import quality gate would then refuse the
// equal file. A silent no-op from the user's point of view.
func (c *Coordinator) RegrabEpisode(ctx context.Context, seriesID int64, season, episode int) error {
	if c.series == nil {
		return fmt.Errorf("series module not available")
	}
	// Refused before anything is blocklisted: a scope the grab will reject must not cost
	// the episode its current release.
	if err := (SeriesScope{Season: season, Episode: episode, Replace: true}).Validate(); err != nil {
		return err
	}
	blockedCurrent := false
	if s, err := c.series.Get(ctx, seriesID); err == nil {
		for _, sn := range s.Seasons {
			if sn.SeasonNumber != season {
				continue
			}
			for _, e := range sn.Episodes {
				if e.EpisodeNumber == episode && e.SourceRelease != "" {
					c.addBlockSeries(ctx, seriesID, e.SourceRelease, "", "replaced by regrab")
					blockedCurrent = true
				}
			}
		}
	}
	if !blockedCurrent {
		// No recorded source release (imported before tracking existed) — fall back to
		// the filename entry. It rarely matches a release title, but it's all there is.
		if path, _ := c.series.EpisodeFilePath(ctx, seriesID, season, episode); path != "" {
			title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			c.addBlockSeries(ctx, seriesID, title, "", "replaced by regrab")
		}
	}
	// Replace is the user saying "replace this episode", so its file goes in whatever it
	// scores — but only this episode's. It never takes a pack, and anything else a
	// multi-episode release carries is gated.
	_, err := c.GrabForScope(ctx, seriesID, SeriesScope{Season: season, Episode: episode, Replace: true, Trigger: "replace"})
	return err
}

// stalledInQueue is the shared verdict for a pending grab found (or not found) in the
// client queue: gone from a *successfully read* queue, in a hard-error state, or
// incomplete with no progress for the grab's stall window. "stalledDL" (no peers right
// now) and a momentary zero speed are NOT stalls on their own — only sustained lack of
// progress is; a single instantaneous sample condemned big packs that were downloading
// fine.
func (c *Coordinator) stalledInQueue(ctx context.Context, g grab, item download.Item, found bool, window time.Duration) bool {
	if !found {
		return true
	}
	// A DELIBERATE pause is not a stall. A user script reacting to a full cache drive,
	// qBittorrent's own queueing, or the user hitting pause all produce a torrent that
	// cannot progress by definition — and the plain no-progress test then condemns it:
	// blocklist the release, delete the torrent AND its data, grab an alternate that can't
	// download either because the disk is still full. Hold the clock instead, so the stall
	// window measures time spent actually trying.
	//
	// "checking" gets the same treatment: after a disk-full crash qBittorrent rechecks its
	// torrents, which on a large pack takes a long while and moves no progress meanwhile.
	// So do the phases where the client itself is holding the torrent back — waiting for
	// a slot under its max-active limit, moving or allocating files. Fetching metadata is
	// NOT held: a magnet nobody will send a file list for is as dead as one with no seeds.
	if item.State == "paused" || item.State == "checking" {
		c.holdStallClock(ctx, g.ID, item.Progress)
		return false
	}
	switch item.Phase() {
	case "queued", "checking", "moving", "allocating":
		c.holdStallClock(ctx, g.ID, item.Progress)
		return false
	}
	// "missingFiles" isn't tested here: normalizeState already folds it into "error", so a
	// second check for it could never fire. A hard client error stays an immediate stall
	// verdict — a deliberate call from the stall rewrite, pinned by TestStalledNeedsSustained-
	// NoProgress, and a full cache drive reaches this code as "paused" above. judgeStall only
	// acts on it once the error has held for stallEvidenceGrace (stallevidence.go).
	//
	// A COMPLETE torrent is never a stall, errored or not: the bytes are on disk (often
	// waiting in Review), and with fail-over on by default a client I/O error on a finished
	// download would otherwise get it replaced and its data deleted.
	if item.State == "error" {
		return !item.Complete()
	}
	return !item.Complete() && c.noProgressFor(ctx, g.ID, item.Progress, window)
}

// DetectStalled fails over grabs that haven't progressed within their profile's stall
// timeout. A torrent still in the client is replaced before it is removed, and kept when
// nothing else can be found — see failOver.
func (c *Coordinator) DetectStalled(ctx context.Context) {
	pending, err := c.pendingGrabs(ctx)
	if err != nil || len(pending) == 0 {
		return
	}
	queue, whole, err := c.downloads.QueueComplete(ctx)
	if err != nil {
		// Without the queue every pending grab reads as "not found", and not-found means
		// stalled — one unreachable download client during a tick would mass-blocklist
		// perfectly healthy downloads and re-grab alternates for all of them. Skip the
		// cycle instead; the next tick is two minutes away.
		c.log.Warn("automation: stall check skipped — can't read the download queue", "err", err)
		return
	}
	if !whole {
		// Same reasoning, for the case the error branch misses: Queue reports success as
		// long as ANY client answered, so with several clients one being down silently
		// hides all of its torrents. Absence is the evidence stall detection acts on, so a
		// partial list is not something it may act on at all.
		c.log.Warn("automation: stall check skipped — a download client didn't answer, so a missing torrent can't be told from a down client")
		return
	}
	// One budget across every kind: each fail-over costs a search, and once the timeout is
	// on for every existing grab a backlog of dead torrents mustn't all go in one tick.
	tick := &stallTick{left: maxStallFailoversPerCheck, held: c.guardHeld(ctx)}
	// Rows recording the same download of the same title are judged once, together: a
	// release re-grabbed on several days must not fail over (and search) once per row.
	for _, g := range collapseDuplicateGrabs(pending) {
		switch g.MediaType {
		case "series":
			c.detectStalledSeries(ctx, g, queue, tick)
		case "music":
			c.detectStalledMusic(ctx, g, queue, tick)
		case mediaMusicArtist:
			// A discography: judged on its own download, never through the movie branch
			// below or as whichever album shares the artist's id.
			c.detectStalledDiscography(ctx, g, queue, tick)
		case "book":
			c.detectStalledBook(ctx, g, queue, tick)
		default:
			c.detectStalledMovie(ctx, g, queue, tick)
		}
	}
	c.pruneStallSightings(pending)
	c.pruneUsenetReports(pending)
	// One line per check that did anything: a backlog of stale grabs closing reads as one
	// count, not a line each. Each fail-over still logs its own line (at most three).
	if tick.closed > 0 || tick.failedOver > 0 || tick.deferred > 0 {
		c.log.Info("automation: stall check finished",
			"closed", tick.closed, "failed_over", tick.failedOver,
			"deferred", tick.deferred, "limit_per_check", maxStallFailoversPerCheck)
	}
}

// ManageSeeding removes finished torrents once they hit their indexer's seed goal (ratio
// or time). An imported one goes with its data — safe, the library keeps its own copy. A
// dismissed one goes with its files kept: Arrmada never imported them, and the user said
// they'd handle them, so they may be the only copy.
func (c *Coordinator) ManageSeeding(ctx context.Context) {
	grabs, err := c.seedCleanupGrabs(ctx)
	if err != nil || len(grabs) == 0 {
		return
	}
	queue, err := c.downloads.Queue(ctx)
	if err != nil {
		return
	}
	// Repair name-only grabs before matching. A grab recorded under the indexer's
	// listing title never matches the torrent that actually arrived, so its seed rule
	// silently doesn't apply — pairing once and writing the hash makes every subsequent
	// pass exact. Re-reads the grabs when anything changed so this pass uses them.
	if n := c.AdoptTorrentHashes(ctx, queue); n > 0 {
		if refreshed, err := c.seedCleanupGrabs(ctx); err == nil {
			grabs = refreshed
		}
	}
	for _, it := range queue {
		usenet := it.Transport == download.TransportUsenet
		if !it.Complete() {
			// A usenet job that failed although what it was for is in the library (it
			// landed another way first) has nothing left to do in SABnzbd: clear it out,
			// partial files and all, rather than leave a failure on the queue for good.
			if usenet && it.Phase() == "error" {
				if g := matchGrab(grabs, it.Hash, it.Name); g != nil && g.Status == grabStatusImported {
					c.clearFailedUsenetJob(ctx, it, *g)
				}
			}
			continue // still downloading — never remove before it's done + imported
		}
		g := matchGrab(grabs, it.Hash, it.Name)
		if g == nil {
			continue // untracked torrent — leave it alone
		}
		// Seed policy is snapshotted on the grab (see recordGrab), so this works
		// even if the originating indexer was later removed or renamed.
		//   off → remove as soon as it's imported (no seeding).
		//   on  → remove once it hits a ratio or seeding-time goal (both 0 = forever).
		// A usenet job never seeds, whatever its row says (one recorded before the
		// transport was known may say it does): it goes as soon as it's imported.
		var over bool
		ratio := it.SeedRatio()
		if !g.SeedEnabled || usenet {
			over = true
		} else {
			// ratio < 0 means the client couldn't give us an honest number — see
			// seedRatioOf. Then only the time goal may end the seed.
			over = g.SeedRatio > 0 && ratio >= 0 && ratio >= g.SeedRatio
			if !over && g.SeedHours > 0 {
				over = it.SeedingTime >= int64(g.SeedHours)*3600
			}
		}
		if !over {
			continue
		}
		// The same torrent can be held for review under a later grab (re-grabbed for
		// another item, or an audiobook version). Removing it — with its data — would take
		// away the files that review is waiting on. Leave it until the review is decided.
		if c.heldForReview(ctx, it.Hash) {
			continue
		}
		// Only data whose content Arrmada imported is deleted with the torrent.
		deleteData := g.Status == grabStatusImported
		if usenet {
			c.cleanUpUsenetJob(ctx, it, *g, deleteData)
			continue
		}
		if err := c.dropTorrent(ctx, it.Hash, deleteData); err != nil {
			c.log.Warn("automation: remove seeded torrent failed", "release", g.Title, "err", err)
			continue
		}
		c.setGrabStatus(ctx, g.ID, grabStatusSeeded)
		// The same torrent recorded again by a later re-grab that never closed: it left the
		// client with this one, and left 'grabbed' it would read as a vanished download.
		c.closeStaleTwins(ctx, *g, grabStatusSeeded)
		reason := "seed goal met"
		if !g.SeedEnabled {
			reason = "seeding off — removed after import"
		}
		what := "download removed"
		if !deleteData {
			what = "torrent removed, files kept in the downloads folder"
		}
		c.log.Info("automation: removed torrent", "release", g.Title, "indexer", g.Indexer, "reason", reason,
			"delete_data", deleteData, "ratio", ratio, "uploaded_bytes", it.UploadedBytes, "seed_time_s", it.SeedingTime)
		c.addMediaEvent(ctx, g, "seeded", g.Title+" — "+reason+", "+what)
	}
}

// heldForReview reports whether a download is waiting on a decision in Review: a pending
// review for its hash, or any grab of it held for review. Seed cleanup must not remove it.
func (c *Coordinator) heldForReview(ctx context.Context, hash string) bool {
	if hash == "" {
		return false
	}
	if c.hasReview(ctx, hash) {
		return true
	}
	var n int
	_ = c.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM grabs WHERE status = ? AND info_hash != '' AND lower(info_hash) = lower(?)`,
		grabStatusHeld, hash).Scan(&n)
	return n > 0
}

// dropTorrent removes a torrent from the client through the test seam when one is set.
func (c *Coordinator) dropTorrent(ctx context.Context, hash string, deleteData bool) error {
	if c.removeTorrent != nil {
		return c.removeTorrent(ctx, hash, deleteData)
	}
	return c.downloads.Remove(ctx, hash, deleteData)
}

// SeedPolicy is the recorded seed goal for a grabbed release, so the downloads feed
// can show each seeding torrent's target (ratio and/or time) and whether it seeds.
type SeedPolicy struct {
	Enabled bool    `json:"enabled"`
	Ratio   float64 `json:"ratio"`
	Hours   int     `json:"hours"`
}

// SeedPolicies returns the seed policy for every live grab (downloading or seeding), keyed
// by lowercased info hash. Built in one query so the feed can annotate seeding torrents
// without a per-item lookup. Uses liveGrabs, not seedCleanupGrabs: a torrent that has
// finished downloading but not yet imported still has a rule, and the UI should show it.
// Rows predating migration 0062 have no hash; those alone are keyed by their normalized
// release title (use NormReleaseKey on a download name to look one up). Name keys for
// hashed rows let a different torrent of the same name borrow a rule that isn't its own.
func (c *Coordinator) SeedPolicies(ctx context.Context) map[string]SeedPolicy {
	grabs, err := c.liveGrabs(ctx)
	if err != nil {
		return nil
	}
	out := make(map[string]SeedPolicy, len(grabs)*2)
	for _, g := range grabs {
		p := SeedPolicy{Enabled: g.SeedEnabled, Ratio: g.SeedRatio, Hours: g.SeedHours}
		if g.InfoHash != "" {
			out[strings.ToLower(g.InfoHash)] = p
			continue
		}
		// A legacy row without a hash can only be found by its release name.
		// TODO: drop with legacyMatchByName once no hashless grabs remain.
		out[normRelease(g.Title)] = p
	}
	return out
}

// NormReleaseKey normalizes a download name to the key used by SeedPolicies.
func NormReleaseKey(name string) string { return normRelease(name) }

// matchGrab finds the grab a download belongs to, by info hash first and falling back to
// the normalized name for rows predating migration 0062.
//
// Hash first because names are unreliable: the indexer's listing title is frequently a
// prettified rendering of the torrent ("EAC3" as "DD+", episode titles dropped), so
// matching on it failed for entire trackers — and every consumer of this function
// silently did nothing as a result.
func matchGrab(grabs []grab, hash, name string) *grab {
	if hash != "" {
		want := strings.ToLower(hash)
		for i := range grabs {
			if grabs[i].InfoHash != "" && strings.ToLower(grabs[i].InfoHash) == want {
				return &grabs[i]
			}
		}
	}
	want := normRelease(name)
	for i := range grabs {
		if normRelease(grabs[i].Title) == want {
			return &grabs[i]
		}
	}
	return nil
}

// videoExts are single-file torrent extensions stripped before matching a torrent
// name to a release title (a torrent is often named "<release>.mkv" while the
// grab record holds just "<release>").
var videoExts = []string{".mkv", ".mp4", ".avi", ".m4v", ".mov", ".ts", ".wmv", ".mpg", ".mpeg", ".webm", ".flv"}

// normRelease normalizes a torrent name / release title for comparison, first
// stripping a trailing video-file extension so "<name>.mkv" matches "<name>".
func normRelease(s string) string {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	for _, e := range videoExts {
		// Both spellings, because the two sides carry the container differently: the
		// torrent is a filename ending ".mp4", while the indexer's listing often leaves it
		// as a trailing WORD ("…x264-Cherzo mp4"). Stripping only the dotted form left two
		// keys differing by "mp4" that could never match, so the download showed as not
		// managed by Arrmada and its seed rule was never found.
		//
		// Matched against the raw string rather than the normalized key on purpose: the
		// key has no separators left, and a blind suffix trim there would eat the "ts" off
		// a group like GHOSTS.
		if strings.HasSuffix(lower, e) {
			s = s[:len(s)-len(e)]
			break
		}
		if ext := " " + e[1:]; strings.HasSuffix(lower, ext) {
			s = s[:len(s)-len(ext)]
			break
		}
	}
	return normTitle(s)
}

// movieHasFileFor reports whether the grab's own file has landed on its target track.
//
// A grab for a missing file has landed once the track has a file. A grab meant to replace
// a file (g.ReplacesPath: an upgrade, a re-grab, a manual grab over a file) has landed only
// once the track holds a different file — or the same path now recorded as this release,
// for an upgrade that kept its name. Taking the old file for the new one marked an
// upgrade imported while it was still downloading: stall fail-over skipped it, the re-grab
// guard let go, and seed cleanup could remove the torrent with its data before the import.
func (c *Coordinator) movieHasFileFor(ctx context.Context, g grab) bool {
	versions, err := c.movies.VersionRows(ctx, g.MovieID) // runs every two minutes: rows only
	if err != nil {
		return false
	}
	for _, v := range versions {
		if v.ID != g.VersionID {
			continue
		}
		if !v.HasFile {
			return false
		}
		if g.ReplacesPath == "" {
			return true
		}
		if filepath.Clean(v.FilePath) != filepath.Clean(g.ReplacesPath) {
			return true
		}
		return v.SourceRelease != "" && normRelease(v.SourceRelease) == normRelease(g.Title)
	}
	return false
}

// findQueued locates a grab's torrent in the client, by info hash first.
//
// Getting this wrong is dangerous, not merely ineffective: stall detection treats "not
// in the queue" as a stalled download and blocklists the release. A name mismatch would
// therefore condemn a torrent that is downloading perfectly well.
func findQueued(queue []download.Item, g grab) (download.Item, bool) {
	if g.InfoHash != "" {
		want := strings.ToLower(g.InfoHash)
		for _, it := range queue {
			if strings.ToLower(it.Hash) == want {
				return it, true
			}
		}
	}
	want := normRelease(g.Title)
	for _, it := range queue {
		if normRelease(it.Name) == want {
			return it, true
		}
	}
	return download.Item{}, false
}

func parseTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// AttachMovieImport attaches a recorded movie import to its movie, flipping it from
// Wanted to Downloaded. The import manager calls it directly, right after recording the
// import and again from the stored row until it settles — it used to run off the
// download.imported bus event, and a dropped event (or a restart at the wrong moment)
// left the movie Wanted for good.
//
// It returns Unmatched when no movie fits, Refused when the quality gate keeps the
// better file already there, and AttachRetry with the error for anything transient.
func (c *Coordinator) AttachMovieImport(ctx context.Context, rec library.ImportRecord) (library.AttachOutcome, error) {
	if c.movies == nil {
		return library.AttachRetry, errors.New("the movies module isn't wired up")
	}
	// Identity first: the download's grab row knows exactly which movie it was for.
	// Re-deriving it from a (differently normalized) title match orphaned imports for
	// accented/&-titled movies and could attach a year-less release to the wrong
	// same-titled film.
	//
	// The track: the one the importer decided and named the file for (kept on the import
	// record), so the attach can't put it anywhere else. A record without one (from before
	// tracks were decided at import, or a download no movie matched) routes as it always has.
	m, matched := movies.Movie{}, false
	mid, versionID := rec.MovieID, int64(-1)
	if mid > 0 {
		versionID = rec.VersionID
	} else if g, ok := c.MovieGrabForHash(ctx, rec.Hash); ok {
		mid = g.MovieID
		if g.VersionID > 0 {
			versionID = g.VersionID // a grab for an extra track lands on it
		}
	}
	if mid > 0 {
		got, err := c.movies.Get(ctx, mid)
		switch {
		case err == nil:
			m, matched = got, true
		case !errors.Is(err, movies.ErrNotFound):
			return library.AttachRetry, fmt.Errorf("load movie %d: %w", mid, err)
		}
	}
	if !matched {
		versionID = -1 // the decided movie is gone: whatever matches now routes afresh
		if rec.Title != "" {
			m, matched = c.movies.Match(ctx, rec.Title, rec.Year)
		}
	}
	if !matched {
		if ctx.Err() != nil {
			return library.AttachRetry, ctx.Err() // cut short, not a verdict
		}
		return library.Unmatched, fmt.Errorf("no movie in the library matches %q", titleYear(rec.Title, rec.Year))
	}
	if err := c.movies.MarkImportedTo(ctx, m.ID, versionID, rec.TargetPath, rec.ReleaseName, false); err != nil {
		if errors.Is(err, movies.ErrWorseQuality) || errors.Is(err, movies.ErrPathOwnedByOtherTrack) {
			return library.Refused, err
		}
		return library.AttachRetry, fmt.Errorf("attach to %q: %w", m.Title, err)
	}
	// MarkImported wrote the movie.imported outbox row with the file record and announced
	// movie.downloaded itself. The grab this download came from is found by its info hash;
	// the release name is only the fallback for rows without one.
	if !c.markMovieGrabImportedByHash(ctx, m.ID, rec.Hash) {
		c.markGrabImportedForMovie(ctx, m.ID, rec.ReleaseName)
	}
	c.log.Info("automation: import attached to movie", "movie", m.Title)
	return library.Attached, nil
}

// untrackedMovie reports whether a torrent no grab knows (see untrackedQueue) is for the
// movie, by its parsed title (any of keys, movieKeys) and year — the only way to tell for
// a torrent Arrmada didn't grab. Arrmada's own downloads are found by hash, through the
// acquisition record.
func untrackedMovie(untracked []download.Item, m movies.Movie, keys []string) bool {
	return untrackedMovieItem(untracked, m, keys) != ""
}

// untrackedMovieItem is the name of the untracked torrent untrackedMovie matched, "" for none.
func untrackedMovieItem(untracked []download.Item, m movies.Movie, keys []string) string {
	for _, it := range untracked {
		r := parser.Parse(it.Name)
		if movies.TitleMatches(keys, m, r.Title, r.Year) {
			return it.Name
		}
	}
	return ""
}

// titleKey is parser.TitleKey, the one title normalizer every download, queue and library
// match shares (accents, "&"/"and", trailing bracketed alternates). A local copy of the
// rules drifted from the Downloads feed's and series', and each drift was a missed match.
func titleKey(s string) string { return parser.TitleKey(s) }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// unmatchedReviewAfter is how many failed match attempts before a download is escalated
// to review. At one sweep every 30s that's about five minutes of trying.
const unmatchedReviewAfter = 10

// noteUnmatched records another failed match for a download and returns the running count.
func (c *Coordinator) noteUnmatched(hash string) int {
	c.unmatchedMu.Lock()
	defer c.unmatchedMu.Unlock()
	if c.unmatched == nil {
		c.unmatched = map[string]int{}
	}
	c.unmatched[hash]++
	return c.unmatched[hash]
}

// metadataRetryReviewAfter is how many sweeps a series file is retried while the show's
// episode listing can't be trusted, before it's left for Review instead. At one sweep every
// 30s that's ten minutes: long enough for a numbering source's blip to pass, short of
// hiding a download for good behind a renumber that waits on the owner.
const metadataRetryReviewAfter = 20

// noteMetadataRetry records another deferred import of a series download and returns the
// running count.
func (c *Coordinator) noteMetadataRetry(contentPath string) int {
	c.unmatchedMu.Lock()
	defer c.unmatchedMu.Unlock()
	if c.metaRetry == nil {
		c.metaRetry = map[string]int{}
	}
	c.metaRetry[contentPath]++
	return c.metaRetry[contentPath]
}

// forgetMetadataRetry clears a download's deferred-import count once an import of it could
// check the metadata properly.
func (c *Coordinator) forgetMetadataRetry(contentPath string) {
	c.unmatchedMu.Lock()
	defer c.unmatchedMu.Unlock()
	delete(c.metaRetry, contentPath)
}

// pruneMetadataRetries drops deferred-import counts for downloads no longer in the
// completed list.
func (c *Coordinator) pruneMetadataRetries(active map[string]bool) {
	c.unmatchedMu.Lock()
	defer c.unmatchedMu.Unlock()
	for p := range c.metaRetry {
		if !active[p] {
			delete(c.metaRetry, p)
		}
	}
}

// pruneUnmatched drops counters for downloads no longer in the completed list (removed,
// imported, or sent to review), so the map doesn't grow for the process lifetime.
func (c *Coordinator) pruneUnmatched(active map[string]bool) {
	c.unmatchedMu.Lock()
	defer c.unmatchedMu.Unlock()
	for h := range c.unmatched {
		if !active[h] {
			delete(c.unmatched, h)
		}
	}
	for h := range c.knownNames {
		if !active[h] {
			delete(c.knownNames, h)
		}
	}
}
