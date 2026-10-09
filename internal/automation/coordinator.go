// Package automation is the coordinator that ties the pipeline together: it
// searches indexers for monitored-but-missing movies, ranks releases with the
// quality engine, grabs the best, and attaches finished imports back to the
// movie. It's the "add a movie and walk away" brain.
package automation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/audiobook"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/outbox"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// seriesCategory keeps TV downloads in a separate download-client category so the
// multi-file series importer processes them, not the single-file movie importer.
const seriesCategory = "arrmada-tv"

// Coordinator orchestrates search → grab → import-attach.
type Coordinator struct {
	movies       *movies.Service
	indexers     *indexer.Service
	downloads    *download.Service
	quality      *quality.Service
	db           *sql.DB
	bus          *eventbus.Bus
	log          *slog.Logger
	downloadsDir string          // the startup downloads folder; downloadsPath reads the live one
	downloadsFn  func() string   // the live downloads folder (Settings → Library); nil = downloadsDir
	series       *series.Service // set post-construction via SetSeries
	books        *books.Service  // set post-construction via SetBooks
	music        *music.Service  // set post-construction via SetMusic
	imp          *library.Importer
	recycle      string      // recycle-bin dir for book deletes ("" = bin off); set via SetRecycleDir
	binv         library.Bin // the bins set by SetBin; nil = the single bin at recycle

	// removeTorrent overrides downloads.Remove in tests; nil uses the real client.
	removeTorrent func(ctx context.Context, hash string, deleteData bool) error

	// merging holds the IDs of books with an audiobook merge running, so a second click
	// can't start another ffmpeg over the same files.
	merging sync.Map
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

	// stallProgress remembers each pending grab's last observed download progress and
	// when it last increased, keyed by grab ID. Stall detection compares against this:
	// "stalled" must mean NO PROGRESS for the profile's stall window, not "old and its
	// instantaneous speed read zero once" — a big pack legitimately downloading for
	// hours was being condemned on a single sample. Guarded by stallMu; entries are
	// pruned when their grab leaves the pending set. In-memory on purpose: a restart
	// just restarts the observation window.
	stallMu       sync.Mutex
	stallProgress map[int64]stallSample
	// stillWaitingAt is when a stalled grab last came up with no replacement, keyed by
	// grab ID. The torrent is left in place, and this keeps it from costing a fresh
	// search — and a fresh "still waiting" history line — more than once per window.
	// Guarded by stallMu and pruned with stallProgress.
	stillWaitingAt map[int64]time.Time
	// stallDefaultFn reads the global stall timeout (Settings → Downloads) that a grab and
	// profile left at 0 fall back to; guardHeldFn reads the hashes the disk guard has
	// paused. Both are set once at startup; nil means DefaultStallMinutes / none held.
	stallDefaultFn func(ctx context.Context) int
	guardHeldFn    func(ctx context.Context) map[string]bool

	// now is the books sweep's clock; nil means time.Now. Tests set it to step through
	// the search ladder.
	now func() time.Time

	// claims keeps two searches of one title from running at once (claims.go).
	claims claims

	// tokens holds the download links behind the opaque release tokens interactive
	// search hands the browser (releasetokens.go). Made on first use.
	tokensOnce sync.Once
	tokens     *releaseTokens

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
}

// SetOutbox installs where series and book imports queue their side effects.
func (c *Coordinator) SetOutbox(o outbox.Enqueuer) { c.outbox = o }

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
func New(m *movies.Service, ix *indexer.Service, dl *download.Service, q *quality.Service, db *sql.DB, bus *eventbus.Bus, log *slog.Logger, downloadsDir string) *Coordinator {
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
	return c.grabTo(ctx, indexerName, downloadURL, title, "")
}

// grabTo is Grab with an explicit download-client category (series use a separate
// one so the multi-file importer handles them). Empty category = client default.
// grabTo hands a release to the download client and returns the torrent's info hash so
// the caller can record it on the grab.
//
// The hash matters because names don't survive the round trip: an indexer's listing title
// is often a prettified rendering of the actual torrent ("EAC3" as "DD+", "10bit" and
// episode titles dropped), so a grab recorded under the listing can never be matched back
// to what the client holds. An empty hash is not an error — a tracker may serve something
// we can't parse — and the caller falls back to name matching as before.
func (c *Coordinator) grabTo(ctx context.Context, indexerName, downloadURL, title, category string) (string, error) {
	// The only download-client kind is a torrent client. Handing it an .nzb URL
	// "succeeds" (qBittorrent fetches URLs async and answers 2xx), records an
	// in-flight grab, and nothing ever arrives — refuse up front instead.
	if idxs, err := c.indexers.List(ctx); err == nil {
		for _, ix := range idxs {
			if ix.Name == indexerName && ix.Transport() == indexer.TransportUsenet {
				return "", fmt.Errorf("%q is a usenet indexer and no usenet download client is configured — this release can't be downloaded", indexerName)
			}
		}
	}
	res, err := c.indexers.Fetch(ctx, indexerName, downloadURL)
	if err != nil {
		return "", err
	}
	add := download.AddRequest{Name: title, SavePath: c.downloadsPath(), Category: category}
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
	c.bus.Publish("release.grabbed", map[string]any{"title": title, "indexer": indexerName})
	return hash, nil
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
// while logged in), in the given category, recorded as a manual grab source.
func (c *Coordinator) addTorrentFile(ctx context.Context, file []byte, filename, title, category string) (string, error) {
	if len(file) == 0 {
		return "", fmt.Errorf("no torrent file provided")
	}
	if filename == "" {
		filename = "arrmada.torrent"
	}
	add := download.AddRequest{Name: title, SavePath: c.downloadsPath(), Category: category, File: file, Filename: filename}
	if err := c.downloads.Add(ctx, add); err != nil {
		return "", err
	}
	hash, _ := download.InfoHashFromFile(file) // "" falls back to name matching
	c.bus.Publish("release.grabbed", map[string]any{"title": title, "indexer": "manual"})
	c.clearRemovedImport(ctx, hash)
	return hash, nil
}

// GrabMovieTorrent adds an uploaded .torrent file for a movie (default category) and
// tracks it like an auto grab so seed cleanup / stall detection manage it too.
func (c *Coordinator) GrabMovieTorrent(ctx context.Context, movieID int64, file []byte, filename, title string) error {
	hash, err := c.addTorrentFile(ctx, file, filename, title, "")
	if err != nil {
		return err
	}
	c.RecordManualGrab(ctx, movieID, title, "manual", hash)
	c.markGrabManual(ctx, hash) // the user chose this file; don't gate it on score
	if c.movies != nil {
		c.movies.AddEvent(ctx, movieID, "grabbed", "Uploaded torrent — "+title)
	}
	return nil
}

// GrabSeriesTorrent adds an uploaded .torrent file for a series (TV category, so the
// multi-file importer handles a pack) and records the grab.
func (c *Coordinator) GrabSeriesTorrent(ctx context.Context, seriesID int64, file []byte, filename, title string) error {
	hash, err := c.addTorrentFile(ctx, file, filename, title, seriesCategory)
	if err != nil {
		return err
	}
	if c.series != nil {
		if s, err := c.series.Get(ctx, seriesID); err == nil {
			c.recordSeriesGrab(ctx, seriesID, title, "manual", s.QualityProfile, hash)
		}
		// An uploaded torrent is as deliberate as a choice gets — import it whatever it
		// scores against what's already there.
		c.markGrabManual(ctx, hash)
		c.series.AddEvent(ctx, seriesID, "grabbed", "Uploaded torrent — "+title)
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
	hash, err := c.addTorrentFile(ctx, file, filename, title, bookCategory)
	if err != nil {
		return err
	}
	if c.books != nil {
		if b, err := c.books.Get(ctx, bookID); err == nil {
			// "manual" as the indexer, same as the movie and series paths: seedRules
			// treats an unknown indexer as seed-for-the-standard-window rather than
			// don't-seed, so an uploaded torrent from a private tracker isn't deleted
			// the moment it imports.
			c.recordBookGrab(ctx, bookID, versionID, title, "manual", b.QualityProfile, hash)
		}
		c.books.AddEvent(ctx, bookID, "grabbed", "Uploaded torrent — "+title)
	}
	return nil
}

// RecordManualGrab tracks a release grabbed by hand (interactive search) exactly
// like an automatic grab, so it's seed-managed and stall-detected too. movieID 0
// (not tied to a tracked movie) is a no-op.
func (c *Coordinator) RecordManualGrab(ctx context.Context, movieID int64, title, indexerName, infoHash string) {
	if movieID == 0 {
		return
	}
	m, err := c.movies.Get(ctx, movieID)
	if err != nil {
		return
	}
	profile := c.effectiveProfile(ctx, m.QualityProfile, quality.MediaMovie)
	c.recordGrab(ctx, movieID, 0, title, indexerName, profile, c.quality.StallMinutes(ctx, profile), infoHash)
}

// SearchMissing searches for and grabs any monitored version that has no file
// and isn't already downloading, across every movie.
//
// Only movies with a monitored, file-less track are visited (chosen in SQL), and every
// read below is a database read: a library whose movies all have their files costs this
// sweep no searches, no ffprobe and no stat.
func (c *Coordinator) SearchMissing(ctx context.Context) {
	all, err := c.movies.SearchTargets(ctx)
	if err != nil {
		c.log.Warn("automation: list movies failed", "err", err)
		return
	}
	queue, _ := c.downloads.Queue(ctx)
	var outage outageTally
	defer outage.report(c.log, "movie search sweep")
	for _, m := range all {
		if !c.movies.IsAvailable(m) {
			continue // not yet at its minimum-availability threshold
		}
		if inQueue(queue, m) {
			continue // a download for this title is already in flight
		}
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
}

// ReleaseList is the interactive-search response for one movie.
type ReleaseList struct {
	Profile  string          `json:"profile"`
	Why      []string        `json:"why,omitempty"` // why the recommended release won
	Releases []RankedRelease `json:"releases"`
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
	m, err := c.movies.Get(ctx, id)
	if err != nil {
		return ReleaseList{}, err
	}
	query := m.Title
	if m.Year > 0 {
		query += " " + strconv.Itoa(m.Year)
	}
	result, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: query, MediaType: indexer.MediaMovie, Limit: 100})
	if err != nil {
		return ReleaseList{}, err
	}

	byName := make(map[string]indexer.Release, len(result.Releases))
	cands := make([]quality.Candidate, 0, len(result.Releases))
	for _, rel := range bestByTitle(result.Releases) {
		if !releaseIsForMovie(rel.Title, m) {
			continue // a different film that merely shares a word with the title
		}
		byName[rel.Title] = rel
		cands = append(cands, quality.NewCandidate(rel.Title, rel.SizeGB(), rel.Seeders))
	}
	profile := c.effectiveProfile(ctx, m.QualityProfile, "movie")
	decision := c.decideWith(ctx, profile, spec, tagRuntime(cands, m.Runtime))
	blocked, err := c.blockedSet(ctx, m.ID)
	if err != nil {
		return ReleaseList{}, err
	}

	winnerName := ""
	if decision.Winner != nil {
		winnerName = decision.Winner.Candidate.Name
	}
	out := make([]RankedRelease, 0, len(cands))
	appendEval := func(ev quality.Evaluation) {
		rel := byName[ev.Candidate.Name]
		out = append(out, RankedRelease{
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
		})
	}
	for _, ev := range decision.Eligible {
		appendEval(ev)
	}
	for _, ev := range decision.Rejected {
		appendEval(ev)
	}
	return ReleaseList{Profile: profile, Why: decision.Why, Releases: out}, nil
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
	if r.Edition != "" {
		parts = append(parts, r.Edition)
	}
	if len(parts) == 0 {
		return "Standard quality"
	}
	return strings.Join(parts, " · ")
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
func (c *Coordinator) searchAndGrab(ctx context.Context, m movies.Movie) (SearchOutcome, error) {
	release, ok := c.claims.claim(movieKey(m.ID))
	if !ok {
		return SearchOutcome{Reason: ReasonAlreadySearching}, ErrAlreadySearching
	}
	defer release()
	want := c.missingVersions(ctx, m.ID)
	if len(want) == 0 {
		return SearchOutcome{Reason: ReasonNothingWanted}, nil
	}
	out := SearchOutcome{Searched: true}
	result, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: movieQuery(m), MediaType: indexer.MediaMovie, Limit: 100})
	if err != nil {
		out.noteSearchErr(err)
		return out, err
	}
	out.Returned = len(result.Releases)
	if len(result.Releases) == 0 {
		c.log.Info("automation: no releases found", "movie", m.Title)
		out.settle()
		return out, nil
	}
	// Only consider releases that are actually for THIS movie — a title search for a
	// short/common name (e.g. "Hope") returns unrelated films ("Romance at Hope
	// Ranch"), and the scorer would otherwise happily grab the wrong one.
	matching := matchingMovieReleases(m, result.Releases)
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

// movieQuery is the indexer search text for a movie: its title, and its year when known.
func movieQuery(m movies.Movie) string {
	if m.Year > 0 {
		return m.Title + " " + strconv.Itoa(m.Year)
	}
	return m.Title
}

// matchingMovieReleases keeps only releases whose parsed title + year match the
// movie — the guard that keeps auto-grab from picking a different film that merely
// shares a word with the title.
func matchingMovieReleases(m movies.Movie, releases []indexer.Release) []indexer.Release {
	out := make([]indexer.Release, 0, len(releases))
	for _, rel := range releases {
		if releaseIsForMovie(rel.Title, m) {
			out = append(out, rel)
		}
	}
	return out
}

// missingVersions returns the monitored version tracks that still need a file.
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

// bestByTitle collapses duplicate release titles to one copy each — the healthiest
// (most seeders). The same scene release routinely appears on several indexers; keeping
// duplicates meant the quality engine could score one copy while a later byName lookup
// returned another (last write wins), so the grab used a different indexer, seed policy
// and download link than the release that actually won.
func bestByTitle(releases []indexer.Release) []indexer.Release {
	idx := make(map[string]int, len(releases))
	out := make([]indexer.Release, 0, len(releases))
	for _, rel := range releases {
		if i, dup := idx[rel.Title]; dup {
			if rel.Seeders > out[i].Seeders {
				out[i] = rel
			}
			continue
		}
		idx[rel.Title] = len(out)
		out = append(out, rel)
	}
	return out
}

// grabbable filters out releases no configured download client can take. The only
// client kind is a torrent client, so usenet releases must not reach a grab: qBittorrent
// accepts the .nzb URL with a 2xx (it fetches async), the grab is recorded as in-flight,
// and nothing ever arrives.
func grabbable(releases []indexer.Release) []indexer.Release {
	out := make([]indexer.Release, 0, len(releases))
	for _, rel := range releases {
		if rel.Transport == indexer.TransportUsenet {
			continue
		}
		out = append(out, rel)
	}
	return out
}

// candidatesFrom builds the scoring candidates from a set of releases, dropping
// any that are blocklisted for this movie, ungrabbable (usenet), or duplicate
// copies of a title already kept.
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
	releases = bestByTitle(grabbable(releases))
	byName := make(map[string]indexer.Release, len(releases))
	cands := make([]quality.Candidate, 0, len(releases))
	for _, rel := range releases {
		if blocked[normTitle(rel.Title)] || exclude[normTitle(rel.Title)] {
			continue
		}
		byName[rel.Title] = rel
		cands = append(cands, quality.NewCandidate(rel.Title, rel.SizeGB(), rel.Seeders))
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
	var titles []string
	grabbed := map[string]bool{}
	pending, err := c.pendingGrabTitles(ctx, m.ID) // releases already grabbed for this movie, not yet imported
	if err != nil {
		c.skipUnreadable(m.Title, err)
		return nil
	}
	// grabbedGB accumulates what this pass has already committed, so two version tracks
	// can't jointly overcommit the same free-space reading (the series path has done this).
	grabbedGB := 0.0
	for _, v := range want {
		// Resolved once and used for the decision, the stall window and the grab record,
		// so a deleted profile means the default here rather than the permissive fallback.
		profile := c.effectiveProfile(ctx, v.QualityProfile, quality.MediaMovie)
		decision := c.quality.Decide(ctx, profile, tagRuntime(cands, m.Runtime))
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
			continue // already grabbed this exact release and it's still in flight — don't loop
		}
		if !c.diskOKFor(grabbedGB + decision.Winner.Candidate.SizeGB) {
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
// dev), it doesn't block.
func (c *Coordinator) diskOKFor(sizeGB float64) bool {
	free, ok := diskspace.FreeGB(c.downloadsPath())
	if !ok {
		return true
	}
	return free >= sizeGB+diskBufferGB
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
	queue, _ := c.downloads.Queue(ctx)
	for _, m := range all {
		if !c.movies.IsAvailable(m) || inQueue(queue, m) {
			continue
		}
		want := c.missingVersions(ctx, m.ID)
		if len(want) == 0 {
			continue
		}
		var matched []indexer.Release
		for _, rel := range res.Releases {
			if releaseIsForMovie(rel.Title, m) {
				matched = append(matched, rel)
			}
		}
		if len(matched) == 0 {
			continue
		}
		c.log.Info("rss: match", "movie", m.Title, "candidates", len(matched))
		byName, cands, err := c.candidatesFrom(ctx, m.ID, matched)
		if err != nil {
			c.skipUnreadable(m.Title, err)
			continue
		}
		c.grabMissing(ctx, m, want, byName, cands)
	}
}

// releaseIsForMovie reports whether a release title is for the given movie
// (normalized title match + year within one).
func releaseIsForMovie(relTitle string, m movies.Movie) bool {
	r := parser.Parse(relTitle)
	if titleKey(r.Title) != titleKey(m.Title) {
		return false
	}
	return r.Year == 0 || m.Year == 0 || abs(r.Year-m.Year) <= 1
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
	var outage outageTally
	defer outage.report(c.log, "movie upgrade sweep")
	for _, m := range all {
		if !m.Monitored || !m.HasFile {
			continue
		}
		if inQueue(queue, m) {
			continue // already grabbing something for this movie
		}
		err := c.upgradeMovie(ctx, m)
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
// quality profile is raised).
func (c *Coordinator) UpgradeMovie(ctx context.Context, id int64) error {
	m, err := c.movies.Get(ctx, id)
	if err != nil {
		return err
	}
	if !m.Monitored || !m.HasFile {
		return nil
	}
	return c.upgradeMovie(ctx, m)
}

// upgradeMovie searches and grabs an upgrade for any monitored version that
// already has a file. Versions without a file are handled by SearchMissing.
func (c *Coordinator) upgradeMovie(ctx context.Context, m movies.Movie) error {
	release, ok := c.claims.claim(movieKey(m.ID))
	if !ok {
		return ErrAlreadySearching
	}
	defer release()
	// Database rows only: the current size and quality come from the cached media info
	// (or the recorded release name), never from probing the file on every sweep.
	versions, err := c.movies.VersionRows(ctx, m.ID)
	if err != nil {
		return err
	}
	var want []movies.Version
	for _, v := range versions {
		// Include any monitored version with a file whose profile allows upgrades — regardless of
		// whether Arrmada grabbed it or found it on a library scan. The AllowsUpgrades gate keeps
		// us from indexer-searching movies on a non-upgrading profile.
		// A held file ("keep existing files" when its profile changed) is left as it is.
		if v.Monitored && v.HasFile && !v.UpgradeHold && c.quality.AllowsUpgrades(ctx, c.effectiveProfile(ctx, v.QualityProfile, quality.MediaMovie)) {
			want = append(want, v)
		}
	}
	if len(want) == 0 {
		return nil
	}

	query := m.Title
	if m.Year > 0 {
		query += " " + strconv.Itoa(m.Year)
	}
	result, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: query, MediaType: indexer.MediaMovie, Limit: 100})
	if err != nil {
		return err
	}
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
	for _, rel := range bestByTitle(grabbable(result.Releases)) {
		if blocked[normTitle(rel.Title)] || !releaseIsForMovie(rel.Title, m) {
			continue
		}
		byName[rel.Title] = rel
		cands = append(cands, quality.NewCandidate(rel.Title, rel.SizeGB(), rel.Seeders))
	}
	// Runtime on the candidates so the profile's bitrate ceiling applies to upgrades
	// too — without it a capped profile could "upgrade" to a remux it explicitly forbids.
	cands = tagRuntime(cands, m.Runtime)

	grabbed := map[string]bool{}
	grabbedGB := 0.0
	pending, err := c.pendingGrabTitles(ctx, m.ID)
	if err != nil {
		c.skipUnreadable(m.Title, err)
		return err
	}
	for _, v := range want {
		curSizeGB := gbOf(v.SizeBytes)
		if v.File != nil && v.File.SizeBytes > 0 {
			curSizeGB = gbOf(v.File.SizeBytes)
		}
		baseline := upgradeBaseline(m, v)
		profile := c.effectiveProfile(ctx, v.QualityProfile, quality.MediaMovie)
		pick, ok := c.quality.UpgradeCandidate(ctx, profile, baseline, curSizeGB, m.Runtime, cands)
		if !ok {
			continue
		}
		winner := byName[pick.Name]
		if grabbed[winner.DownloadURL] {
			continue
		}
		if pending[normTitle(winner.Title)] {
			continue // this exact upgrade is already in flight — don't stack a second copy
		}
		if !c.diskOKFor(grabbedGB + pick.SizeGB) {
			c.log.Warn("automation: low disk, skipping upgrade", "movie", m.Title, "need_gb", pick.SizeGB)
			continue
		}
		c.log.Info("automation: upgrading", "movie", m.Title, "version", v.Label, "from", baseline, "to", winner.Title)
		hash, err := c.Grab(ctx, winner.Indexer, winner.DownloadURL, winner.Title)
		if err != nil {
			c.log.Warn("automation: upgrade grab failed", "movie", m.Title, "err", err)
			continue
		}
		grabbed[winner.DownloadURL] = true
		grabbedGB += pick.SizeGB
		c.recordGrab(ctx, m.ID, v.ID, winner.Title, winner.Indexer, profile, c.quality.StallMinutes(ctx, profile), hash)
		detail := "Upgrade: " + winner.Title + " · " + winner.Indexer
		if !v.IsDefault {
			detail += " → " + v.Label
		}
		c.movies.AddEvent(ctx, m.ID, "grabbed", detail)
	}
	return nil
}

func gbOf(bytes int64) float64 { return float64(bytes) / (1024 * 1024 * 1024) }

// upgradeBaseline is the "what we already have" release string the upgrade comparison scores
// against. Files Arrmada grabbed carry their SourceRelease; files found by a library scan don't —
// so fall back to their probed quality (e.g. "Bambi 1942 1080p BluRay x264"), then the filename.
// Without this, disk-imported movies could never be considered for an upgrade at all.
func upgradeBaseline(m movies.Movie, v movies.Version) string {
	if s := strings.TrimSpace(v.SourceRelease); s != "" {
		return s
	}
	if v.File != nil && v.File.Quality != "" {
		parts := []string{m.Title}
		if m.Year > 0 {
			parts = append(parts, strconv.Itoa(m.Year))
		}
		parts = append(parts, v.File.Quality) // e.g. "1080p BluRay"
		if v.File.Codec != "" {
			parts = append(parts, v.File.Codec)
		}
		return strings.Join(parts, " ")
	}
	if v.FilePath != "" {
		return filepath.Base(v.FilePath)
	}
	return ""
}

// UpgradeBaseline is the release string the movie upgrade sweep scores a version's file
// by, for callers outside the sweep that must judge the file the same way (a profile
// edit's dry run).
func UpgradeBaseline(m movies.Movie, v movies.Version) string { return upgradeBaseline(m, v) }

// RegrabMovie grabs the best release under each monitored version's current
// profile even when a file already exists — a deliberate re-grab, used when the
// user switches to a different (e.g. lower) profile and chooses to replace their
// file. On import the new file replaces the old one.
func (c *Coordinator) RegrabMovie(ctx context.Context, id int64) error {
	m, err := c.movies.Get(ctx, id)
	if err != nil {
		return err
	}
	versions, err := c.movies.VersionRows(ctx, m.ID)
	if err != nil {
		return err
	}
	query := m.Title
	if m.Year > 0 {
		query += " " + strconv.Itoa(m.Year)
	}
	result, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: query, MediaType: indexer.MediaMovie, Limit: 100})
	if err != nil {
		return err
	}
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
	for _, rel := range bestByTitle(grabbable(result.Releases)) {
		if blocked[normTitle(rel.Title)] || !releaseIsForMovie(rel.Title, m) {
			continue
		}
		byName[rel.Title] = rel
		cands = append(cands, quality.NewCandidate(rel.Title, rel.SizeGB(), rel.Seeders))
	}
	grabbed := map[string]bool{}
	grabbedGB := 0.0
	for _, v := range versions {
		if !v.Monitored {
			continue
		}
		profile := c.effectiveProfile(ctx, v.QualityProfile, quality.MediaMovie)
		decision := c.quality.Decide(ctx, profile, tagRuntime(cands, m.Runtime))
		if decision.Winner == nil {
			continue
		}
		winner := byName[decision.Winner.Candidate.Name]
		if grabbed[winner.DownloadURL] {
			continue
		}
		if !c.diskOKFor(grabbedGB + decision.Winner.Candidate.SizeGB) {
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
		c.recordGrab(ctx, m.ID, v.ID, winner.Title, winner.Indexer, profile, c.quality.StallMinutes(ctx, profile), hash)
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
	return c.listBlocks(ctx, movieID)
}

// Unblock removes a blocklist entry.
func (c *Coordinator) Unblock(ctx context.Context, id int64) error { return c.removeBlock(ctx, id) }

// BlockRelease is the "block from the downloads list" action: remove the torrent
// (and its data), blocklist it for its media so it isn't grabbed again, and search
// for an alternate release. name is the torrent/release name.
//
// It used to only know about movies: blocking a TV torrent removed it but blocklisted
// nothing, so the very next sweep re-grabbed the identical release.
func (c *Coordinator) BlockRelease(ctx context.Context, hash, name string) error {
	_ = c.downloads.Remove(ctx, hash, true)
	// Whatever it was, its grab row must not stay 'grabbed' — that would hold the
	// pending-grab guard for a day and hide the block from stall detection.
	if pending, err := c.pendingGrabs(ctx); err == nil {
		if g := matchGrab(pending, hash, name); g != nil {
			c.setGrabStatus(ctx, g.ID, "failed")
		}
	}
	if m, ok := c.movies.MatchRelease(ctx, name); ok {
		_, err := c.BlocklistAndSearch(ctx, m.ID, name, "", "")
		if errors.Is(err, ErrAlreadySearching) {
			return nil // the search in flight runs without the blocked release
		}
		return err
	}
	if c.series != nil {
		sid, ix, grabbed := c.grabbedMediaFor(ctx, name, "series")
		if !grabbed {
			if s, ok := c.series.MatchByTitle(ctx, series.NormTitle(parser.Parse(name).Title)); ok {
				sid = s.ID
			}
		}
		if sid != 0 {
			c.addBlockSeries(ctx, sid, name, ix, "manually blocklisted")
			c.series.AddEvent(ctx, sid, "blocklisted", name)
			_, err := c.SearchSeriesNow(ctx, sid)
			if errors.Is(err, ErrAlreadySearching) {
				return nil // the search in flight runs without the blocked release
			}
			return err
		}
	}
	return nil // not tied to tracked media — the removal is enough
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
	return c.listBlocksSeries(ctx, seriesID)
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

// stallSample is one observation of a grab's download progress: how far along it was
// and when that value last increased.
type stallSample struct {
	progress float64
	at       time.Time
}

// noProgressFor reports whether grab id's download has made no progress for at least
// window. Each call updates the sample: any forward progress restarts the clock.
// The first observation of a grab always returns false — a genuinely dead download
// simply waits one extra window, which is far cheaper than condemning a live one.
func (c *Coordinator) noProgressFor(id int64, progress float64, window time.Duration) bool {
	c.stallMu.Lock()
	defer c.stallMu.Unlock()
	if c.stallProgress == nil {
		c.stallProgress = map[int64]stallSample{}
	}
	s, seen := c.stallProgress[id]
	if !seen || progress > s.progress {
		c.stallProgress[id] = stallSample{progress: progress, at: time.Now()}
		return false
	}
	return time.Since(s.at) >= window
}

// holdStallClock refreshes a grab's stall sample WITHOUT counting it as progress, so time
// spent in a state that cannot progress doesn't accumulate toward the stall window. The
// grab resumes being judged the moment the torrent is running again.
func (c *Coordinator) holdStallClock(id int64, progress float64) {
	c.stallMu.Lock()
	defer c.stallMu.Unlock()
	if c.stallProgress == nil {
		c.stallProgress = map[int64]stallSample{}
	}
	c.stallProgress[id] = stallSample{progress: progress, at: time.Now()}
}

// pruneStallSamples drops progress samples for grabs no longer pending, so the map
// tracks only live downloads.
func (c *Coordinator) pruneStallSamples(pending []grab) {
	c.stallMu.Lock()
	defer c.stallMu.Unlock()
	live := make(map[int64]bool, len(pending))
	for _, g := range pending {
		live[g.ID] = true
	}
	for id := range c.stallProgress {
		if !live[id] {
			delete(c.stallProgress, id)
		}
	}
	for id := range c.stillWaitingAt {
		if !live[id] {
			delete(c.stillWaitingAt, id)
		}
	}
}

// stalledInQueue is the shared verdict for a pending grab found (or not found) in the
// client queue: gone from a *successfully read* queue, in a hard-error state, or
// incomplete with no progress for the grab's stall window. "stalledDL" (no peers right
// now) and a momentary zero speed are NOT stalls on their own — only sustained lack of
// progress is; a single instantaneous sample condemned big packs that were downloading
// fine.
func (c *Coordinator) stalledInQueue(g grab, item download.Item, found bool, window time.Duration) bool {
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
		c.holdStallClock(g.ID, item.Progress)
		return false
	}
	switch item.Phase() {
	case "queued", "checking", "moving", "allocating":
		c.holdStallClock(g.ID, item.Progress)
		return false
	}
	// "missingFiles" isn't tested here: normalizeState already folds it into "error", so a
	// second check for it could never fire. A hard client error stays an immediate stall —
	// that's a deliberate call from the stall rewrite, pinned by TestStalledNeedsSustained-
	// NoProgress, and a full cache drive reaches this code as "paused" above.
	//
	// A COMPLETE torrent is never a stall, errored or not: the bytes are on disk (often
	// waiting in Review), and with fail-over on by default a client I/O error on a finished
	// download would otherwise get it replaced and its data deleted.
	if item.State == "error" {
		return !item.Complete()
	}
	return !item.Complete() && c.noProgressFor(g.ID, item.Progress, window)
}

// DetectStalled fails over grabs that haven't progressed within their profile's stall
// timeout. A torrent still in the client is replaced before it is removed, and kept when
// nothing else can be found — see failOver.
func (c *Coordinator) DetectStalled(ctx context.Context) {
	pending, err := c.pendingGrabs(ctx)
	if err != nil || len(pending) == 0 {
		return
	}
	c.pruneStallSamples(pending)
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
	for _, g := range pending {
		switch g.MediaType {
		case "series":
			c.detectStalledSeries(ctx, g, queue, tick)
		case "music":
			c.detectStalledMusic(ctx, g, queue, tick)
		case "book":
			c.detectStalledBook(ctx, g, queue, tick)
		default:
			c.detectStalledMovie(ctx, g, queue, tick)
		}
	}
	if tick.deferred > 0 {
		c.log.Info("automation: deferring stalled downloads to the next check",
			"deferred", tick.deferred, "limit_per_check", maxStallFailoversPerCheck)
	}
}

// ManageSeeding removes imported torrents once they hit their indexer's seed
// goal (ratio or time). Safe because the library keeps its own copy of the file.
func (c *Coordinator) ManageSeeding(ctx context.Context) {
	grabs, err := c.importedGrabs(ctx)
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
		if refreshed, err := c.importedGrabs(ctx); err == nil {
			grabs = refreshed
		}
	}
	for _, it := range queue {
		if !it.Complete() {
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
		var over bool
		ratio := it.SeedRatio()
		if !g.SeedEnabled {
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
		if err := c.downloads.Remove(ctx, it.Hash, true); err != nil {
			c.log.Warn("automation: remove seeded torrent failed", "release", g.Title, "err", err)
			continue
		}
		c.setGrabStatus(ctx, g.ID, "seeded")
		reason := "seed goal met"
		if !g.SeedEnabled {
			reason = "seeding off — removed after import"
		}
		c.log.Info("automation: removed torrent", "release", g.Title, "indexer", g.Indexer, "reason", reason,
			"ratio", ratio, "uploaded_bytes", it.UploadedBytes, "seed_time_s", it.SeedingTime)
		if g.MediaType == "movie" {
			c.movies.AddEvent(ctx, g.MovieID, "seeded", g.Title+" — "+reason+", download removed")
		}
	}
}

// SeedPolicy is the recorded seed goal for a grabbed release, so the downloads feed
// can show each seeding torrent's target (ratio and/or time) and whether it seeds.
type SeedPolicy struct {
	Enabled bool    `json:"enabled"`
	Ratio   float64 `json:"ratio"`
	Hours   int     `json:"hours"`
}

// SeedPolicies returns the seed policy for every live grab (downloading or seeding),
// keyed by a normalized release title (use NormReleaseKey on a download name to look it
// up). Built in one query so the feed can annotate seeding torrents without a per-item
// lookup. Uses liveGrabs, not importedGrabs: a torrent that has finished downloading but
// not yet imported still has a rule, and the UI should show it.
// Keyed by BOTH the torrent's info hash and its normalized title. The hash is the
// reliable key — an indexer's listing title is often a prettified rendering of the actual
// torrent, so name matching silently failed for whole trackers — but rows predating
// migration 0062 have no hash, and the name key keeps working for those.
func (c *Coordinator) SeedPolicies(ctx context.Context) map[string]SeedPolicy {
	grabs, err := c.liveGrabs(ctx)
	if err != nil {
		return nil
	}
	out := make(map[string]SeedPolicy, len(grabs)*2)
	for _, g := range grabs {
		p := SeedPolicy{Enabled: g.SeedEnabled, Ratio: g.SeedRatio, Hours: g.SeedHours}
		out[normRelease(g.Title)] = p
		if g.InfoHash != "" {
			out[strings.ToLower(g.InfoHash)] = p
		}
	}
	return out
}

// NormReleaseKey normalizes a download name to the key used by SeedPolicies.
func NormReleaseKey(name string) string { return normRelease(name) }

// GrabNearMiss is a grab row that ALMOST matches a download name.
type GrabNearMiss struct {
	Title  string // as recorded at grab time
	Status string
	Key    string // its normalized key, for comparison against the torrent's
}

// NearestGrabs returns the grab rows whose normalized key most closely resembles this
// download name's, best first.
//
// An exact-match lookup can't diagnose a matching failure — it uses the very comparison
// under suspicion, so "not found" is indistinguishable from "not recorded". Reporting the
// near misses instead shows the two strings side by side, which is the only way to see
// HOW they diverge: a year present on one side, a punctuation difference that survives
// normalization, a tracker listing that differs from the .torrent's own name.
func (c *Coordinator) NearestGrabs(ctx context.Context, name string, limit int) []GrabNearMiss {
	rows, err := c.db.QueryContext(ctx, `SELECT title, status FROM grabs ORDER BY id DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	want := normRelease(name)
	type scored struct {
		m GrabNearMiss
		n int
	}
	var best []scored
	for rows.Next() {
		var title, status string
		if rows.Scan(&title, &status) != nil {
			continue
		}
		key := normRelease(title)
		n := commonPrefix(key, want)
		if n < 8 {
			continue // unrelated release; not worth reporting
		}
		best = append(best, scored{GrabNearMiss{Title: title, Status: status, Key: key}, n})
	}
	sort.Slice(best, func(i, j int) bool { return best[i].n > best[j].n })
	out := make([]GrabNearMiss, 0, limit)
	for i := 0; i < len(best) && i < limit; i++ {
		out = append(out, best[i].m)
	}
	return out
}

// SharedPrefixLen exposes commonPrefix for diagnostics: it says where two normalized
// keys start to differ, which points straight at the token responsible.
func SharedPrefixLen(a, b string) int { return commonPrefix(a, b) }

// commonPrefix returns how many leading characters two keys share.
func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

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

// movieHasFileFor reports whether the grab's target version now has a file.
func (c *Coordinator) movieHasFileFor(ctx context.Context, g grab) bool {
	versions, err := c.movies.VersionRows(ctx, g.MovieID) // runs every two minutes: rows only
	if err != nil {
		return false
	}
	for _, v := range versions {
		if v.ID == g.VersionID {
			return v.HasFile
		}
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
	m, matched := movies.Movie{}, false
	if mid, ok := c.movieIDForGrabHash(ctx, rec.Hash); ok {
		got, err := c.movies.Get(ctx, mid)
		switch {
		case err == nil:
			m, matched = got, true
		case !errors.Is(err, movies.ErrNotFound):
			return library.AttachRetry, fmt.Errorf("load movie %d: %w", mid, err)
		}
	}
	if !matched && rec.Title != "" {
		m, matched = c.movies.Match(ctx, rec.Title, rec.Year)
	}
	if !matched {
		if ctx.Err() != nil {
			return library.AttachRetry, ctx.Err() // cut short, not a verdict
		}
		return library.Unmatched, fmt.Errorf("no movie in the library matches %q", titleYear(rec.Title, rec.Year))
	}
	if err := c.movies.MarkImported(ctx, m.ID, rec.TargetPath, rec.ReleaseName); err != nil {
		if errors.Is(err, movies.ErrWorseQuality) {
			return library.Refused, err
		}
		return library.AttachRetry, fmt.Errorf("attach to %q: %w", m.Title, err)
	}
	// MarkImported wrote the movie.imported outbox row with the file record and announced
	// movie.downloaded itself.
	c.markGrabImportedForMovie(ctx, m.ID, rec.ReleaseName)
	c.log.Info("automation: import attached to movie", "movie", m.Title)
	return library.Attached, nil
}

// inQueue reports whether the movie is already downloading (title+year match).
func inQueue(queue []download.Item, m movies.Movie) bool {
	for _, it := range queue {
		r := parser.Parse(it.Name)
		if titleKey(r.Title) == titleKey(m.Title) && (r.Year == 0 || m.Year == 0 || abs(r.Year-m.Year) <= 1) {
			return true
		}
	}
	return false
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
}
