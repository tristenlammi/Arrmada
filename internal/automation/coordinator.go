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
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/books"
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
func (c *Coordinator) Grab(ctx context.Context, indexerName, downloadURL, title string) error {
	return c.grabTo(ctx, indexerName, downloadURL, title, "")
}

// grabTo is Grab with an explicit download-client category (series use a separate
// one so the multi-file importer handles them). Empty category = client default.
func (c *Coordinator) grabTo(ctx context.Context, indexerName, downloadURL, title, category string) error {
	res, err := c.indexers.Fetch(ctx, indexerName, downloadURL)
	if err != nil {
		return err
	}
	add := download.AddRequest{Name: title, SavePath: c.downloadsDir, Category: category}
	switch {
	case len(res.File) > 0:
		add.File = res.File
		add.Filename = res.Filename
	case res.URL != "":
		add.URL = res.URL
	default:
		return fmt.Errorf("nothing to download for this release")
	}
	if err := c.downloads.Add(ctx, add); err != nil {
		return err
	}
	c.bus.Publish("release.grabbed", map[string]any{"title": title, "indexer": indexerName})
	return nil
}

// RecordManualGrab tracks a release grabbed by hand (interactive search) exactly
// like an automatic grab, so it's seed-managed and stall-detected too. movieID 0
// (not tied to a tracked movie) is a no-op.
func (c *Coordinator) RecordManualGrab(ctx context.Context, movieID int64, title, indexerName string) {
	if movieID == 0 {
		return
	}
	m, err := c.movies.Get(ctx, movieID)
	if err != nil {
		return
	}
	c.recordGrab(ctx, movieID, 0, title, indexerName, m.QualityProfile, c.quality.StallMinutes(ctx, m.QualityProfile))
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
		if err := c.searchAndGrab(ctx, m); err != nil {
			c.log.Warn("automation: search failed", "movie", m.Title, "err", err)
		}
	}
}

// RankedRelease is one interactive-search result, ranked and explained in
// plain language (no scores — that's the Simple-view mandate).
type RankedRelease struct {
	Title        string  `json:"title"`
	Indexer      string  `json:"indexer"`
	DownloadURL  string  `json:"download_url"`
	SizeGB       float64 `json:"size_gb"`
	Bitrate      float64 `json:"bitrate_mbps,omitempty"` // size ÷ runtime; 0 when runtime unknown
	Seeders      int     `json:"seeders"`
	Summary      string  `json:"summary"` // "4K · DV+HDR10 · BluRay"
	Eligible     bool    `json:"eligible"`
	RejectReason string  `json:"reject_reason,omitempty"`
	Recommended  bool    `json:"recommended"`
	Blocklisted  bool    `json:"blocklisted,omitempty"`
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
	result, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: query, Limit: 100})
	if err != nil {
		return ReleaseList{}, err
	}

	byName := make(map[string]indexer.Release, len(result.Releases))
	cands := make([]quality.Candidate, 0, len(result.Releases))
	for _, rel := range result.Releases {
		if _, dup := byName[rel.Title]; dup {
			continue
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
	return c.searchAndGrab(ctx, m)
}

func (c *Coordinator) searchAndGrab(ctx context.Context, m movies.Movie) error {
	want := c.missingVersions(ctx, m.ID)
	if len(want) == 0 {
		return nil
	}
	query := m.Title
	if m.Year > 0 {
		query += " " + strconv.Itoa(m.Year)
	}
	result, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: query, Limit: 100})
	if err != nil {
		return err
	}
	if len(result.Releases) == 0 {
		c.log.Info("automation: no releases found", "movie", m.Title)
		return nil
	}
	byName, cands := c.candidatesFrom(ctx, m.ID, result.Releases)
	c.grabMissing(ctx, m, want, byName, cands)
	return nil
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

// candidatesFrom builds the scoring candidates from a set of releases, dropping
// any that are blocklisted for this movie.
func (c *Coordinator) candidatesFrom(ctx context.Context, movieID int64, releases []indexer.Release) (map[string]indexer.Release, []quality.Candidate) {
	blocked := c.blockedSet(ctx, movieID)
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
func (c *Coordinator) grabMissing(ctx context.Context, m movies.Movie, want []movies.Version, byName map[string]indexer.Release, cands []quality.Candidate) {
	grabbed := map[string]bool{}
	for _, v := range want {
		decision := c.quality.Decide(ctx, v.QualityProfile, tagRuntime(cands, m.Runtime))
		if decision.Winner == nil {
			continue
		}
		winner := byName[decision.Winner.Candidate.Name]
		if grabbed[winner.DownloadURL] {
			continue
		}
		if !c.diskOKFor(decision.Winner.Candidate.SizeGB) {
			c.log.Warn("automation: low disk, skipping grab", "movie", m.Title, "need_gb", decision.Winner.Candidate.SizeGB)
			c.movies.AddEvent(ctx, m.ID, "failed", "Not enough free disk space to grab "+winner.Title)
			continue
		}
		c.log.Info("automation: grabbing", "movie", m.Title, "version", v.Label, "release", winner.Title, "indexer", winner.Indexer)
		if err := c.Grab(ctx, winner.Indexer, winner.DownloadURL, winner.Title); err != nil {
			c.log.Warn("automation: grab failed", "movie", m.Title, "version", v.Label, "err", err)
			continue
		}
		grabbed[winner.DownloadURL] = true
		c.recordGrab(ctx, m.ID, v.ID, winner.Title, winner.Indexer, v.QualityProfile, c.quality.StallMinutes(ctx, v.QualityProfile))
		detail := winner.Title + " · " + winner.Indexer
		if !v.IsDefault {
			detail += " → " + v.Label
		}
		c.movies.AddEvent(ctx, m.ID, "grabbed", detail)
	}
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
	queue, _ := c.downloads.Queue(ctx)
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
		if v.Monitored && v.HasFile && v.SourceRelease != "" {
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
	result, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: query, Limit: 100})
	if err != nil {
		return err
	}
	if len(result.Releases) == 0 {
		return nil
	}

	blocked := c.blockedSet(ctx, m.ID)
	byName := make(map[string]indexer.Release, len(result.Releases))
	cands := make([]quality.Candidate, 0, len(result.Releases))
	for _, rel := range result.Releases {
		if blocked[normTitle(rel.Title)] {
			continue
		}
		byName[rel.Title] = rel
		cands = append(cands, quality.NewCandidate(rel.Title, rel.SizeGB(), rel.Seeders))
	}

	grabbed := map[string]bool{}
	for _, v := range want {
		curSizeGB := gbOf(v.SizeBytes)
		if v.File != nil && v.File.SizeBytes > 0 {
			curSizeGB = gbOf(v.File.SizeBytes)
		}
		pick, ok := c.quality.UpgradeCandidate(ctx, v.QualityProfile, v.SourceRelease, curSizeGB, cands)
		if !ok {
			continue
		}
		winner := byName[pick.Name]
		if grabbed[winner.DownloadURL] {
			continue
		}
		c.log.Info("automation: upgrading", "movie", m.Title, "version", v.Label, "from", v.SourceRelease, "to", winner.Title)
		if err := c.Grab(ctx, winner.Indexer, winner.DownloadURL, winner.Title); err != nil {
			c.log.Warn("automation: upgrade grab failed", "movie", m.Title, "err", err)
			continue
		}
		grabbed[winner.DownloadURL] = true
		c.recordGrab(ctx, m.ID, v.ID, winner.Title, winner.Indexer, v.QualityProfile, c.quality.StallMinutes(ctx, v.QualityProfile))
		detail := "Upgrade: " + winner.Title + " · " + winner.Indexer
		if !v.IsDefault {
			detail += " → " + v.Label
		}
		c.movies.AddEvent(ctx, m.ID, "grabbed", detail)
	}
	return nil
}

func gbOf(bytes int64) float64 { return float64(bytes) / (1024 * 1024 * 1024) }

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
	result, err := c.indexers.Search(ctx, indexer.SearchQuery{Text: query, Limit: 100})
	if err != nil {
		return err
	}
	if len(result.Releases) == 0 {
		return nil
	}
	blocked := c.blockedSet(ctx, m.ID)
	byName := make(map[string]indexer.Release, len(result.Releases))
	cands := make([]quality.Candidate, 0, len(result.Releases))
	for _, rel := range result.Releases {
		if blocked[normTitle(rel.Title)] {
			continue
		}
		byName[rel.Title] = rel
		cands = append(cands, quality.NewCandidate(rel.Title, rel.SizeGB(), rel.Seeders))
	}
	grabbed := map[string]bool{}
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
		if err := c.Grab(ctx, winner.Indexer, winner.DownloadURL, winner.Title); err != nil {
			c.log.Warn("automation: regrab failed", "movie", m.Title, "err", err)
			continue
		}
		grabbed[winner.DownloadURL] = true
		c.recordGrab(ctx, m.ID, v.ID, winner.Title, winner.Indexer, v.QualityProfile, c.quality.StallMinutes(ctx, v.QualityProfile))
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
// (and its data), blocklist it for its movie so it isn't grabbed again, and search
// for an alternate release. name is the torrent/release name.
func (c *Coordinator) BlockRelease(ctx context.Context, hash, name string) error {
	_ = c.downloads.Remove(ctx, hash, true)
	m, ok := c.movies.MatchRelease(ctx, name)
	if !ok {
		return nil // not tied to a tracked movie — the removal is enough
	}
	return c.BlocklistAndSearch(ctx, m.ID, name, "", "")
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
func (c *Coordinator) RegrabEpisode(ctx context.Context, seriesID int64, season, episode int) error {
	if c.series == nil {
		return fmt.Errorf("series module not available")
	}
	if path, _ := c.series.EpisodeFilePath(ctx, seriesID, season, episode); path != "" {
		title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		c.addBlockSeries(ctx, seriesID, title, "", "replaced by regrab")
	}
	return c.GrabBestForScope(ctx, seriesID, season, episode)
}

// DetectStalled fails over grabs that haven't progressed within their profile's
// stall timeout: blocklist the release, remove it from the client, re-search.
func (c *Coordinator) DetectStalled(ctx context.Context) {
	pending, err := c.pendingGrabs(ctx)
	if err != nil || len(pending) == 0 {
		return
	}
	queue, _ := c.downloads.Queue(ctx)
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
		age := time.Since(parseTime(g.GrabbedAt))
		if age < time.Duration(g.StallMinutes)*time.Minute {
			continue
		}
		item, found := findQueued(queue, g.Title)
		stalled := !found ||
			item.State == "error" || item.State == "stalledDL" || item.State == "missingFiles" ||
			(item.Progress < 1.0 && item.DownSpeed == 0)
		if !stalled {
			continue
		}
		c.log.Info("automation: download stalled, failing over", "movie", g.MovieID, "release", g.Title, "age_min", int(age.Minutes()))
		_ = c.addBlock(ctx, g.MovieID, g.Title, g.Indexer, "", fmt.Sprintf("stalled after %d min", g.StallMinutes))
		if found {
			_ = c.downloads.Remove(ctx, item.Hash, true)
		}
		c.setGrabStatus(ctx, g.ID, "failed")
		c.movies.AddEvent(ctx, g.MovieID, "failed", g.Title+" stalled — blocklisted, searching for an alternate")
		if m, err := c.movies.Get(ctx, g.MovieID); err == nil {
			_ = c.searchAndGrab(ctx, m)
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
		g := matchGrab(grabs, it.Name)
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

// matchGrab finds the imported grab a download name belongs to.
func matchGrab(grabs []grab, name string) *grab {
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

func findQueued(queue []download.Item, title string) (download.Item, bool) {
	want := normRelease(title)
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
				c.markGrabsImportedForMovie(ctx, m.ID)
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
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
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
