// Package automation is the coordinator that ties the pipeline together: it
// searches indexers for monitored-but-missing movies, ranks releases with the
// quality engine, grabs the best, and attaches finished imports back to the
// movie. It's the "add a movie and walk away" brain.
package automation

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/movies"
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
	downloadsDir string          // filesystem checked for free space before auto-grabs
	series       *series.Service // set post-construction via SetSeries
	books        *books.Service  // set post-construction via SetBooks
	imp          *library.Importer
	recycle      string // recycle-bin dir for book deletes ("" = hard delete); set via SetRecycleDir

	// unmatched counts how many import sweeps have failed to match a download to a
	// series, keyed by torrent hash. Without it the 30-second sweep logs the same failure
	// forever and nothing ever escalates. Guarded by unmatchedMu. Entries are pruned when
	// their download leaves the completed list, so the map can't grow for the process
	// lifetime.
	unmatchedMu sync.Mutex
	unmatched   map[string]int

	// stallProgress remembers each pending grab's last observed download progress and
	// when it last increased, keyed by grab ID. Stall detection compares against this:
	// "stalled" must mean NO PROGRESS for the profile's stall window, not "old and its
	// instantaneous speed read zero once" — a big pack legitimately downloading for
	// hours was being condemned on a single sample. Guarded by stallMu; entries are
	// pruned when their grab leaves the pending set. In-memory on purpose: a restart
	// just restarts the observation window.
	stallMu       sync.Mutex
	stallProgress map[int64]stallSample

	// onSeriesImported fires after episodes land, so the Convert library index can
	// refresh just that show rather than waiting for the nightly sweep. Optional.
	onSeriesImported func(ctx context.Context, seriesID int64)
}

// SetSeriesImportedHook registers a callback run after a series import writes episodes.
func (c *Coordinator) SetSeriesImportedHook(fn func(ctx context.Context, seriesID int64)) {
	c.onSeriesImported = fn
}

// seriesImported notifies the hook, if one is registered.
func (c *Coordinator) seriesImported(ctx context.Context, seriesID int64) {
	if c.onSeriesImported != nil {
		c.onSeriesImported(ctx, seriesID)
	}
}

// SetRecycleDir points book file deletion at the recycle bin (matching movies). Empty
// keeps the hard-delete behavior.
func (c *Coordinator) SetRecycleDir(dir string) { c.recycle = dir }

// SetSeries wires the series module + its importer for TV acquisition.
func (c *Coordinator) SetSeries(s *series.Service, imp *library.Importer) {
	c.series = s
	c.imp = imp
}

// SetBooks wires the books module (shares the importer set by SetSeries).
func (c *Coordinator) SetBooks(b *books.Service) { c.books = b }

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
	add := download.AddRequest{Name: title, SavePath: c.downloadsDir, Category: category}
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
		}
	default:
		return "", fmt.Errorf("nothing to download for this release")
	}
	if err := c.downloads.Add(ctx, add); err != nil {
		return "", err
	}
	c.bus.Publish("release.grabbed", map[string]any{"title": title, "indexer": indexerName})
	return hash, nil
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
	add := download.AddRequest{Name: title, SavePath: c.downloadsDir, Category: category, File: file, Filename: filename}
	if err := c.downloads.Add(ctx, add); err != nil {
		return "", err
	}
	hash, _ := download.InfoHashFromFile(file) // "" falls back to name matching
	c.bus.Publish("release.grabbed", map[string]any{"title": title, "indexer": "manual"})
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
		c.series.AddEvent(ctx, seriesID, "grabbed", "Uploaded torrent — "+title)
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
	c.recordGrab(ctx, movieID, 0, title, indexerName, m.QualityProfile, c.quality.StallMinutes(ctx, m.QualityProfile), infoHash)
}

// SearchMissing searches for and grabs any monitored version that has no file
// and isn't already downloading, across every movie.
func (c *Coordinator) SearchMissing(ctx context.Context) {
	all, err := c.movies.List(ctx)
	if err != nil {
		c.log.Warn("automation: list movies failed", "err", err)
		return
	}
	queue, _ := c.downloads.Queue(ctx)
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
		n, searched, err := c.searchAndGrab(ctx, m)
		switch {
		case err != nil:
			c.log.Warn("automation: search failed", "movie", m.Title, "err", err)
		case !searched:
			// Nothing wanted, no query spent — not a miss.
		case n > 0:
			c.movies.ResetSearchMisses(ctx, m.ID)
		default:
			c.movies.RecordSearchMiss(ctx, m.ID)
		}
	}
}

// RankedRelease is one interactive-search result, ranked and explained in
// plain language (no scores — that's the Simple-view mandate).
type RankedRelease struct {
	Title        string  `json:"title"`
	Indexer      string  `json:"indexer"`
	DownloadURL  string  `json:"download_url"`
	InfoURL      string  `json:"info_url,omitempty"` // the release's details page on the tracker
	SizeGB       float64 `json:"size_gb"`
	Bitrate      float64 `json:"bitrate_mbps,omitempty"` // size ÷ runtime; 0 when runtime unknown
	Seeders      int     `json:"seeders"`
	Summary      string  `json:"summary"` // "4K · DV+HDR10 · BluRay"
	Eligible     bool    `json:"eligible"`
	RejectReason string  `json:"reject_reason,omitempty"`
	Recommended  bool    `json:"recommended"`
	Blocklisted  bool    `json:"blocklisted,omitempty"`
	// Books only:
	Edition  string `json:"edition,omitempty"`  // ebook | audiobook
	Format   string `json:"format,omitempty"`   // EPUB, M4B, MP3…
	Narrator string `json:"narrator,omitempty"` // audiobook narrator, when detected
	Author   string `json:"author,omitempty"`   // structured author (e.g. from MyAnonaMouse)
	Series   string `json:"series,omitempty"`   // structured series + number
	Language string `json:"language,omitempty"` // language code/name when known
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
// ceiling can turn a release's size into a bitrate. 0 leaves the ceiling inert. (Series flows
// don't tag runtime yet — a season pack's runtime is its episode count × episode length, which
// needs pack-scope parsing; the ceiling simply doesn't apply there for now.)
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

func (c *Coordinator) RankReleases(ctx context.Context, id int64) (ReleaseList, error) {
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
	decision := c.quality.Decide(ctx, m.QualityProfile, tagRuntime(cands, m.Runtime))
	blocked := c.blockedSet(ctx, m.ID)

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
			InfoURL:      rel.InfoURL,
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
	return ReleaseList{Profile: m.QualityProfile, Why: decision.Why, Releases: out}, nil
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

// SearchMovie searches for and grabs a single movie (manual trigger).
func (c *Coordinator) SearchMovie(ctx context.Context, id int64) error {
	m, err := c.movies.Get(ctx, id)
	if err != nil {
		return err
	}
	_, _, err = c.searchAndGrab(ctx, m)
	return err
}

// searchAndGrab searches for a movie and grabs what the quality profile picks. It
// returns how many releases were grabbed so the sweep can back off a movie that keeps
// coming up empty, and whether a search actually ran — a movie with nothing wanted
// costs no indexer query and must not count as a "miss" (that ratcheted every
// fully-downloaded movie to the 12h backoff cap, delaying the first real search when
// a file was later deleted or a new version track added).
func (c *Coordinator) searchAndGrab(ctx context.Context, m movies.Movie) (int, bool, error) {
	want := c.missingVersions(ctx, m.ID)
	if len(want) == 0 {
		return 0, false, nil
	}
	query := m.Title
	if m.Year > 0 {
		query += " " + strconv.Itoa(m.Year)
	}
	result, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: query, MediaType: indexer.MediaMovie, Limit: 100})
	if err != nil {
		return 0, true, err
	}
	if len(result.Releases) == 0 {
		c.log.Info("automation: no releases found", "movie", m.Title)
		return 0, true, nil
	}
	// Only consider releases that are actually for THIS movie — a title search for a
	// short/common name (e.g. "Hope") returns unrelated films ("Romance at Hope
	// Ranch"), and the scorer would otherwise happily grab the wrong one.
	matching := matchingMovieReleases(m, result.Releases)
	byName, cands := c.candidatesFrom(ctx, m.ID, matching)
	// Say where the results went. A search that returns releases and grabs none looked
	// identical in the log to one that found nothing useful — the same movie re-searched
	// every cycle forever with no hint whether the releases were for a different film,
	// blocklisted, or simply below the quality profile's bar.
	if len(cands) == 0 {
		c.log.Info("automation: no usable releases",
			"movie", m.Title, "returned", len(result.Releases),
			"wrong_title", len(result.Releases)-len(matching), "blocklisted", len(matching))
	}
	return c.grabMissing(ctx, m, want, byName, cands), true, nil
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
	versions, err := c.movies.Versions(ctx, movieID)
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
func (c *Coordinator) candidatesFrom(ctx context.Context, movieID int64, releases []indexer.Release) (map[string]indexer.Release, []quality.Candidate) {
	blocked := c.blockedSet(ctx, movieID)
	releases = bestByTitle(grabbable(releases))
	byName := make(map[string]indexer.Release, len(releases))
	cands := make([]quality.Candidate, 0, len(releases))
	for _, rel := range releases {
		if blocked[normTitle(rel.Title)] {
			continue
		}
		byName[rel.Title] = rel
		cands = append(cands, quality.NewCandidate(rel.Title, rel.SizeGB(), rel.Seeders))
	}
	return byName, cands
}

// grabMissing grabs the best candidate for each still-missing version track.
// Shared by live search (searchAndGrab) and RSS sync — only the candidate source
// differs.
func (c *Coordinator) grabMissing(ctx context.Context, m movies.Movie, want []movies.Version, byName map[string]indexer.Release, cands []quality.Candidate) int {
	grabbed := map[string]bool{}
	pending := c.pendingGrabTitles(ctx, m.ID) // releases already grabbed for this movie, not yet imported
	// grabbedGB accumulates what this pass has already committed, so two version tracks
	// can't jointly overcommit the same free-space reading (the series path has done this).
	grabbedGB := 0.0
	for _, v := range want {
		decision := c.quality.Decide(ctx, v.QualityProfile, tagRuntime(cands, m.Runtime))
		if decision.Winner == nil {
			// The profile rejected everything on offer. Silent before, which made a
			// too-strict profile look identical to an indexer returning nothing — and
			// the search repeated every cycle either way, with no way to tell which.
			if len(cands) > 0 {
				c.log.Info("automation: no release met the quality profile",
					"movie", m.Title, "version", v.Label,
					"profile", v.QualityProfile, "candidates", len(cands))
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
		grabbedGB += decision.Winner.Candidate.SizeGB
		c.recordGrab(ctx, m.ID, v.ID, winner.Title, winner.Indexer, v.QualityProfile, c.quality.StallMinutes(ctx, v.QualityProfile), hash)
		detail := winner.Title + " · " + winner.Indexer
		if !v.IsDefault {
			detail += " → " + v.Label
		}
		c.movies.AddEvent(ctx, m.ID, "grabbed", detail)
	}
	return len(grabbed)
}

// diskOKFor reports whether there's room to grab a release of the given size,
// keeping a small safety buffer. If free space can't be measured (e.g. non-Linux
// dev), it doesn't block.
func (c *Coordinator) diskOKFor(sizeGB float64) bool {
	free, ok := diskspace.FreeGB(c.downloadsDir)
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
	all, err := c.movies.List(ctx)
	if err != nil {
		c.log.Warn("rss: list movies failed", "err", err)
		return
	}
	res, err := c.indexers.Recent(ctx, 100)
	if err != nil {
		c.log.Warn("rss: fetch feeds failed", "err", err)
		return
	}
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
		byName, cands := c.candidatesFrom(ctx, m.ID, matched)
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
	all, err := c.movies.List(ctx)
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
	for _, m := range all {
		if !m.Monitored || !m.HasFile {
			continue
		}
		if inQueue(queue, m) {
			continue // already grabbing something for this movie
		}
		if err := c.upgradeMovie(ctx, m); err != nil {
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
	versions, err := c.movies.Versions(ctx, m.ID)
	if err != nil {
		return err
	}
	var want []movies.Version
	for _, v := range versions {
		// Include any monitored version with a file whose profile allows upgrades — regardless of
		// whether Arrmada grabbed it or found it on a library scan. The AllowsUpgrades gate keeps
		// us from indexer-searching movies on a non-upgrading profile.
		if v.Monitored && v.HasFile && c.quality.AllowsUpgrades(ctx, v.QualityProfile) {
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

	blocked := c.blockedSet(ctx, m.ID)
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
	pending := c.pendingGrabTitles(ctx, m.ID)
	for _, v := range want {
		curSizeGB := gbOf(v.SizeBytes)
		if v.File != nil && v.File.SizeBytes > 0 {
			curSizeGB = gbOf(v.File.SizeBytes)
		}
		baseline := upgradeBaseline(m, v)
		pick, ok := c.quality.UpgradeCandidate(ctx, v.QualityProfile, baseline, curSizeGB, m.Runtime, cands)
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
		c.recordGrab(ctx, m.ID, v.ID, winner.Title, winner.Indexer, v.QualityProfile, c.quality.StallMinutes(ctx, v.QualityProfile), hash)
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

// RegrabMovie grabs the best release under each monitored version's current
// profile even when a file already exists — a deliberate re-grab, used when the
// user switches to a different (e.g. lower) profile and chooses to replace their
// file. On import the new file replaces the old one.
func (c *Coordinator) RegrabMovie(ctx context.Context, id int64) error {
	m, err := c.movies.Get(ctx, id)
	if err != nil {
		return err
	}
	versions, err := c.movies.Versions(ctx, m.ID)
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
	blocked := c.blockedSet(ctx, m.ID)
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
		decision := c.quality.Decide(ctx, v.QualityProfile, tagRuntime(cands, m.Runtime))
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
		c.recordGrab(ctx, m.ID, v.ID, winner.Title, winner.Indexer, v.QualityProfile, c.quality.StallMinutes(ctx, v.QualityProfile), hash)
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
		return c.BlocklistAndSearch(ctx, m.ID, name, "", "")
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
			return c.SearchSeriesNow(ctx, sid)
		}
	}
	return nil // not tied to tracked media — the removal is enough
}

// BlocklistAndSearch blocklists a release then re-searches for an alternate.
func (c *Coordinator) BlocklistAndSearch(ctx context.Context, movieID int64, title, indexerName, downloadURL string) error {
	if err := c.addBlock(ctx, movieID, title, indexerName, downloadURL, "manually blocklisted"); err != nil {
		return err
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
	return c.GrabBestForScope(ctx, seriesID, season, episode)
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
	if item.State == "error" || item.State == "missingFiles" {
		return true
	}
	return item.Progress < 1.0 && c.noProgressFor(g.ID, item.Progress, window)
}

// DetectStalled fails over grabs that haven't progressed within their profile's
// stall timeout: blocklist the release, remove it from the client, re-search.
func (c *Coordinator) DetectStalled(ctx context.Context) {
	pending, err := c.pendingGrabs(ctx)
	if err != nil || len(pending) == 0 {
		return
	}
	c.pruneStallSamples(pending)
	queue, err := c.downloads.Queue(ctx)
	if err != nil {
		// Without the queue every pending grab reads as "not found", and not-found means
		// stalled — one unreachable download client during a tick would mass-blocklist
		// perfectly healthy downloads and re-grab alternates for all of them. Skip the
		// cycle instead; the next tick is two minutes away.
		c.log.Warn("automation: stall check skipped — can't read the download queue", "err", err)
		return
	}
	for _, g := range pending {
		if g.MediaType == "series" {
			c.detectStalledSeries(ctx, g, queue)
			continue
		}
		if g.MediaType == "book" {
			c.detectStalledBook(ctx, g, queue)
			continue
		}
		// Already imported? (a version gained a file)
		if c.movieHasFileFor(ctx, g) {
			c.setGrabStatus(ctx, g.ID, "imported")
			continue
		}
		if g.StallMinutes <= 0 {
			continue // fail-over disabled for this profile
		}
		window := time.Duration(g.StallMinutes) * time.Minute
		age := time.Since(parseTime(g.GrabbedAt))
		if age < window {
			continue
		}
		item, found := findQueued(queue, g)
		if !c.stalledInQueue(g, item, found, window) {
			continue
		}
		c.log.Info("automation: download stalled, failing over", "movie", g.MovieID, "release", g.Title, "age_min", int(age.Minutes()))
		if err := c.addBlock(ctx, g.MovieID, g.Title, g.Indexer, "", fmt.Sprintf("stalled after %d min", g.StallMinutes)); err != nil {
			// If the blocklist insert fails, the re-search below would just re-grab the
			// release that stalled — skip the fail-over and retry next tick.
			c.log.Warn("automation: stall blocklist failed — leaving the grab for the next tick", "release", g.Title, "err", err)
			continue
		}
		if found {
			_ = c.downloads.Remove(ctx, item.Hash, true)
		}
		c.setGrabStatus(ctx, g.ID, "failed")
		c.movies.AddEvent(ctx, g.MovieID, "failed", g.Title+" stalled — blocklisted, searching for an alternate")
		if m, err := c.movies.Get(ctx, g.MovieID); err == nil {
			_, _, _ = c.searchAndGrab(ctx, m) // stall fail-over ignores the sweep backoff
		}
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
	for _, it := range queue {
		if it.Progress < 1 {
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
		if !g.SeedEnabled {
			over = true
		} else {
			over = g.SeedRatio > 0 && it.Ratio >= g.SeedRatio
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
		c.log.Info("automation: removed torrent", "release", g.Title, "indexer", g.Indexer, "reason", reason, "ratio", it.Ratio, "seed_time_s", it.SeedingTime)
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
	lower := strings.ToLower(s)
	for _, e := range videoExts {
		if strings.HasSuffix(lower, e) {
			s = s[:len(s)-len(e)]
			break
		}
	}
	return normTitle(s)
}

// movieHasFileFor reports whether the grab's target version now has a file.
func (c *Coordinator) movieHasFileFor(ctx context.Context, g grab) bool {
	versions, err := c.movies.Versions(ctx, g.MovieID)
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

// WatchImports listens for finished imports and attaches each to its movie,
// flipping it from Wanted to Downloaded. Returns when ctx is cancelled.
func (c *Coordinator) WatchImports(ctx context.Context) {
	events, cancel := c.bus.Subscribe("download.imported")
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-events:
			data, ok := ev.Data.(map[string]any)
			if !ok {
				continue
			}
			title, _ := data["title"].(string)
			target, _ := data["target"].(string)
			release, _ := data["name"].(string)
			year := toInt(data["year"])
			if title == "" {
				continue
			}
			if m, matched := c.movies.Match(ctx, title, year); matched {
				if err := c.movies.MarkImported(ctx, m.ID, target, release); err != nil {
					c.log.Warn("automation: mark imported failed", "movie", m.Title, "err", err)
					continue
				}
				c.markGrabImportedForMovie(ctx, m.ID, release)
				c.log.Info("automation: import attached to movie", "movie", m.Title)
				c.bus.Publish("movie.downloaded", map[string]any{"title": m.Title, "id": m.ID})
			}
		}
	}
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

func titleKey(s string) string {
	// "&" and "and" are the same word, and releases pick either freely: a library title of
	// "Love & Death" has to match "Love.and.Death.S01..." as well as "Love.&.Death.S01...".
	// Stripping the ampersand as punctuation made those two spellings different keys
	// (lovedeath vs loveanddeath), so an entire show's releases were rejected as belonging
	// to a different programme. Normalize to one form before dropping the rest.
	//
	// Accents fold too. unicode.IsLetter accepts 'é', so "Pokémon" kept its diacritic and
	// never matched a release named "Pokemon" — releases are named in ASCII. The searcher
	// already folds the outbound query (indexer.Service.Search), so the search found the
	// releases and then this check threw every one of them away. series.normKey has always
	// folded; this is the same normalization, now agreed on by both.
	lower := strings.ReplaceAll(strings.ToLower(parser.FoldAccents(s)), "&", " and ")
	var b strings.Builder
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
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
