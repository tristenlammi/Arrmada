package automation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/extract"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

// musicCategory keeps album downloads in their own download-client category so the album
// importer handles them, not the movie/series/book importers.
const musicCategory = download.CategoryMusic

// mediaMusicArtist is the grabs and blocklist media type of a discography: one release
// covering an artist's whole catalogue, recorded against the ARTIST id (in movie_id). An
// album grab is "music" with the album id. They were both "music" once, so the stall check,
// the blocklist and seed cleanup judged a discography by whichever album happened to share
// the artist's number.
const mediaMusicArtist = "music_artist"

// SearchMusicMissing sweeps monitored albums that are missing tracks and grabs the best
// release for each.
//
// An album is due when it isn't complete, isn't already downloading, has been released, and
// its backoff has run out (see musicSearchWait). Never-searched albums go first, then the
// ones searched longest ago, and no more than musicSweepCap albums are searched per sweep, so
// a big wanted list is worked through over several sweeps instead of hammering every indexer
// at once. An album with no MusicBrainz track listing is counted as a miss without an indexer
// search: there'd be nothing to import a download against. A grab resets the backoff; a
// search that failed for reasons of our own (indexers down, a grab error) doesn't count,
// though it still moves the album to the back of the line.
// Running out of disk space ends the sweep, since every other album would hit the same wall.
func (c *Coordinator) SearchMusicMissing(ctx context.Context) {
	if c.music == nil || !c.moduleOn(ctx, "music") {
		return
	}
	ctx = WithDefaultSearchTrigger(ctx, TriggerSweep)
	artists, err := c.music.LibraryArtists(ctx)
	if err != nil {
		return
	}
	byID := make(map[int64]music.Artist, len(artists))
	for _, a := range artists {
		byID[a.ID] = a
	}
	albums, err := c.music.WantedAlbums(ctx)
	if err != nil {
		c.log.Warn("music: couldn't list wanted albums", "err", err)
		return
	}
	queue, qerr := c.musicQueue(ctx)
	if qerr != nil {
		// An unreadable (or partial) queue looks exactly like an empty one, so every
		// in-flight album would read as "not downloading" and the sweep would stack
		// duplicate grabs.
		c.log.Info(clientDownSkip, "sweep", "music search sweep", "err", qerr)
		return
	}

	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	type dueAlbum struct {
		artist music.Artist
		album  music.Album
		last   time.Time
	}
	var due []dueAlbum
	for _, al := range albums {
		a, ok := byID[al.ArtistID]
		if !ok || al.Complete() {
			continue
		}
		if !albumReleased(al, today) {
			continue // announced but not out yet — not a miss, just not yet
		}
		if c.albumDownloading(ctx, queue, a, al) {
			continue // already downloading — let it finish
		}
		last := parseTime(al.LastSearchAt)
		if wait := musicSearchWait(al.SearchMisses); wait > 0 && !last.IsZero() && now.Sub(last) < wait {
			continue
		}
		due = append(due, dueAlbum{artist: a, album: al, last: last})
	}
	// Never searched first (a zero time sorts first), then the longest-waiting.
	sort.SliceStable(due, func(i, j int) bool { return due[i].last.Before(due[j].last) })
	if len(due) > musicSweepCap {
		c.log.Info("music: more albums due than one sweep searches — the rest wait for the next",
			"due", len(due), "searching", musicSweepCap)
		due = due[:musicSweepCap]
	}

	for _, d := range due {
		out := c.grabAlbum(ctx, d.artist, d.album)
		switch {
		case out.Code == outcomeGrabbed:
			if err := c.music.ResetSearchMisses(ctx, d.album.ID); err != nil {
				c.log.Warn("music: clearing the search backoff failed", "album", d.album.Title, "err", err)
			}
		case out.miss():
			if err := c.music.RecordSearchMiss(ctx, d.album.ID); err != nil {
				c.log.Warn("music: recording a search miss failed", "album", d.album.Title, "err", err)
			}
			// Say so once, at the transition. An album that has quietly dropped to weekly
			// looks identical to one nobody has got to yet.
			if d.album.SearchMisses+1 == musicWeeklyAfter {
				c.log.Info("music: nothing found 7 times, searching weekly from now on",
					"artist", d.artist.Name, "album", d.album.Title, "last", out.Code)
			}
		case out.Code == outcomeNoSpace:
			return
		default:
			// Failed on our side. Not a miss, but stamp the time anyway: left at zero, an
			// album that fails the same way every time (a dead .torrent link, say) would
			// sort first every sweep and, 25 of them, take every slot.
			if err := c.music.TouchSearch(ctx, d.album.ID); err != nil {
				c.log.Warn("music: stamping the search time failed", "album", d.album.Title, "err", err)
			}
		}
	}
}

// musicSweepCap is how many albums one sweep searches at most. Each is a search on every
// indexer, and a newly added discography can make dozens due at once.
const musicSweepCap = 25

// musicWeeklyAfter is the miss count from which an album is only searched weekly.
const musicWeeklyAfter = 7

// musicSearchWait is how long the sweep leaves an album alone after `misses` searches in a
// row found nothing: the same 30 min → 12 h ladder series use for the first six misses, then
// once a week. An album that never turns up is retried rarely, not forgotten.
func musicSearchWait(misses int) time.Duration {
	switch {
	case misses <= 0:
		return 0
	case misses < musicWeeklyAfter:
		return searchBackoff(misses)
	default:
		return 7 * 24 * time.Hour
	}
}

// albumReleased reports whether an album is out by today (YYYY-MM-DD). MusicBrainz dates
// come as a full date, a year-month or a bare year; each is compared at its own precision,
// so "2027" is future in 2026 and "2026" counts as out. An album with no date at all is
// treated as out unless its year says otherwise — most old records have no exact date.
func albumReleased(al music.Album, today string) bool {
	d := strings.TrimSpace(al.ReleaseDate)
	if d == "" {
		return al.Year == 0 || fmt.Sprintf("%04d", al.Year) <= today[:4]
	}
	if len(d) > len(today) {
		d = d[:len(today)]
	}
	return d <= today[:len(d)]
}

// Album search outcomes. A miss lengthens the album's backoff; the others either succeeded
// or failed for a reason of our own, which says nothing about whether the album exists.
const (
	outcomeGrabbed      = "grabbed"
	outcomeNoListing    = "no_listing"    // MusicBrainz has no track listing for it
	outcomeNoResults    = "no_results"    // the indexers returned nothing at all
	outcomeNoMatch      = "no_match"      // releases came back, none of them this album
	outcomeBlocked      = "blocked"       // every match is blocklisted or already grabbed
	outcomeBelowProfile = "below_profile" // matches exist, none the profile accepts
	outcomeIndexerError = "indexer_error" // the search failed, or every indexer asked errored
	outcomeListingError = "listing_error" // couldn't reach MusicBrainz for the listing
	outcomeGrabFailed   = "grab_failed"   // the download client refused the release
	outcomeNoSpace      = "no_space"      // not enough free disk for the release
	outcomeUnreadable   = "unreadable"    // the blocklist or pending grabs couldn't be read
)

// albumOutcome is what one album search came to, so the sweep can decide its backoff.
type albumOutcome struct {
	Code    string
	Detail  string
	Release string // the release grabbed, or the one that didn't fit on disk
}

// miss reports whether the outcome counts towards the album's backoff. Errors on our side
// never do: an indexer outage must not push every album out to weekly searches.
func (o albumOutcome) miss() bool {
	switch o.Code {
	case outcomeNoListing, outcomeNoResults, outcomeNoMatch, outcomeBlocked, outcomeBelowProfile:
		return true
	}
	return false
}

// albumDownloading reports whether the queue already holds a release for this album.
func (c *Coordinator) albumDownloading(ctx context.Context, queue []download.Item, a music.Artist, al music.Album) bool {
	names := c.music.ArtistNames(ctx, a)
	for _, it := range queue {
		if it.Category != musicCategory {
			continue
		}
		if names.ReleaseIsForAlbum(it.Name, al.Title) {
			return true
		}
	}
	return false
}

// grabAlbum searches for one album and grabs the best release the profile allows. Each
// call is one search attempt (scope "album"); the sweep's backoff still reads the
// albumOutcome as before.
func (c *Coordinator) grabAlbum(ctx context.Context, a music.Artist, al music.Album) albumOutcome {
	out, _, _ := c.grabAlbumRecorded(ctx, a, al)
	return out
}

// grabAlbumRecorded is grabAlbum that also returns the recorded outcome, with what the
// search saw folded in (counts, each indexer's error): what Search now reports.
func (c *Coordinator) grabAlbumRecorded(ctx context.Context, a music.Artist, al music.Album) (albumOutcome, SearchOutcome, error) {
	ctx, notes := newSearchNotes(ctx)
	out := c.grabAlbumExcluding(ctx, a, al, nil)
	so, err := out.searchOutcome()
	c.recordAttempt(ctx, notes, AttemptMusic, al.ID, ScopeAlbum, &so, err)
	return out, so, err
}

// searchOutcome is an album search's result in the shared outcome's terms. A failure on our
// side (indexers, the client, disk, MusicBrainz) is an error, not a miss.
func (o albumOutcome) searchOutcome() (SearchOutcome, error) {
	res := SearchOutcome{Searched: true}
	switch o.Code {
	case outcomeGrabbed:
		res.Grabbed, res.GrabbedTitles = 1, []string{o.Release}
		res.Reason = ReasonGrabbed
	case outcomeNoResults:
		res.Reason = ReasonNoReleases
	case outcomeNoMatch:
		res.Reason = ReasonNoneForTitle
	case outcomeBlocked, outcomeBelowProfile:
		res.Reason = ReasonBlockedOrBelow
	case outcomeNoListing:
		res.Searched, res.Reason = false, ReasonNothingWanted
	case outcomeDeferred:
		// Found, and left for the next sweep by the upgrade budget: settled from the counts.
	case outcomeIndexerError:
		// Nobody could answer: recorded as indexers_failed, with each indexer's error.
		res.Reason = ReasonIndexersFailed
		return res, fmt.Errorf("album search: %s %s", o.Code, o.Detail)
	default:
		return res, fmt.Errorf("album search: %s %s", o.Code, o.Detail)
	}
	return res, nil
}

// SearchAlbumNow searches for one album now and grabs the best release, the way the
// sweep would, and says what it found: a person's Search now (the album page, Wanted),
// and the search after a Block. It ignores the album's backoff, but not the rest:
//   - nothing is searched while the Music module is off;
//   - an album with a download in flight isn't searched again (the outcome names it);
//   - a complete album gets an upgrade search, and only when its profile allows upgrades
//     and it is below its cutoff: another copy of what it has is never grabbed.
func (c *Coordinator) SearchAlbumNow(ctx context.Context, albumID int64) (SearchOutcome, error) {
	if c.music == nil || !c.moduleOn(ctx, "music") {
		return SearchOutcome{Reason: ReasonNothingWanted}, nil
	}
	release, ok := c.claims.claim(albumKey(albumID))
	if !ok {
		return SearchOutcome{Reason: ReasonAlreadySearching}, ErrAlreadySearching
	}
	defer release()
	al, err := c.music.GetAlbum(ctx, albumID)
	if err != nil {
		return SearchOutcome{}, err
	}
	a, err := c.music.GetArtist(ctx, al.ArtistID)
	if err != nil {
		return SearchOutcome{}, err
	}
	busy, err := c.albumInFlight(ctx, al.ID)
	if err != nil {
		return SearchOutcome{}, err
	}
	if busy != "" {
		ctx, notes := newSearchNotes(ctx)
		out := SearchOutcome{Reason: ReasonAlreadyDownloading, Example: busy}
		c.recordAttempt(ctx, notes, AttemptMusic, al.ID, ScopeAlbum, &out, nil)
		return out, nil
	}
	if al.Complete() {
		return c.upgradeAlbumNow(ctx, a, al)
	}
	_, so, err := c.grabAlbumRecorded(ctx, a, al)
	return so, err
}

// albumInFlight names a download still in flight for an album ("" for none). An
// unreadable answer is an error: a second copy mustn't be grabbed on a guess.
func (c *Coordinator) albumInFlight(ctx context.Context, albumID int64) (string, error) {
	acqs, err := c.Active(ctx, "music", albumID)
	if err != nil {
		return "", err
	}
	for _, acq := range acqs {
		if acq.Downloading() {
			return acq.Title, nil
		}
	}
	return "", nil
}

// grabAlbumExcluding is grabAlbum that also skips the normalized titles in exclude: a stall
// fail-over keeps the stalled release out of the replacement search without blocklisting
// it, because it stays in the client until something else is found.
func (c *Coordinator) grabAlbumExcluding(ctx context.Context, a music.Artist, al music.Album, exclude map[string]bool) albumOutcome {
	// A partly held album only takes releases at least as good as the worst track it has
	// (albumJudge.tierCheck).
	var mode albumMode
	if al.HaveTracks > 0 {
		held, ok, err := c.music.HeldTiers(ctx, al.ID)
		if err != nil {
			c.skipUnreadable(a.Name+" — "+al.Title, err)
			return albumOutcome{Code: outcomeUnreadable, Detail: err.Error()}
		}
		if ok {
			mode.held = held
		}
	}
	judged, sp, fail := c.searchAlbumReleases(ctx, a, al, mode, exclude)
	if fail != nil {
		return *fail
	}
	cands, fail := albumVerdictOutcome(judged, sp, mode)
	if fail != nil {
		switch fail.Code {
		case outcomeNoMatch:
			c.log.Info("music: no release matched this album", "artist", a.Name, "album", al.Title)
		case outcomeBelowProfile:
			c.log.Info("music: no release met the quality profile", "artist", a.Name, "album", al.Title, "why", fail.Detail)
		}
		return *fail
	}
	best := pickBestAlbum(c.networks(ctx), sp, cands)
	if best == nil {
		return albumOutcome{Code: outcomeBelowProfile, Detail: fmt.Sprintf("%d match(es), none accepted by %q", len(cands), sp.Name)}
	}
	if !c.diskOKFor(ctx, best.Indexer, float64(best.SizeBytes)/(1<<30)) {
		c.log.Warn("music: not enough free space for this release", "album", al.Title, "release", best.Title)
		return albumOutcome{Code: outcomeNoSpace, Release: best.Title}
	}
	hash, err := c.grabMusic(ctx, best.Indexer, best.DownloadURL, best.Title, musicCategory)
	if err != nil {
		c.log.Warn("music: grab failed", "album", al.Title, "err", err)
		return albumOutcome{Code: outcomeGrabFailed, Detail: err.Error(), Release: best.Title}
	}
	c.recordMusicGrab(ctx, "music", al.ID, best.Title, best.Indexer, a.QualityProfile, hash)
	c.music.AddEvent(ctx, a.ID, "grabbed",
		fmt.Sprintf("Grabbed %q from %s: %s", al.Title, best.Indexer, best.Title))
	c.log.Info("music: grabbing", "artist", a.Name, "album", al.Title,
		"release", best.Title, "quality", music.DetectQuality(best.Title))
	return albumOutcome{Code: outcomeGrabbed, Release: best.Title}
}

// searchMusic, musicQueue and grabMusic are the music path's calls out to the indexers and
// the download client, each overridable by a test seam (nil means the real service).
func (c *Coordinator) searchMusic(ctx context.Context, q indexer.SearchQuery) (indexer.SearchResult, error) {
	if c.musicSearchFn != nil {
		res, err := c.musicSearchFn(ctx, q)
		notesFrom(ctx).note(res, err)
		return res, err
	}
	return c.search(ctx, q)
}

func (c *Coordinator) musicQueue(ctx context.Context) ([]download.Item, error) {
	if c.musicQueueFn != nil {
		return c.musicQueueFn(ctx)
	}
	items, whole, err := c.downloads.QueueComplete(ctx)
	if err == nil && !whole {
		err = errClientPartial
	}
	return items, err
}

func (c *Coordinator) grabMusic(ctx context.Context, indexerName, url, title, category string) (string, error) {
	if c.musicGrabFn != nil {
		return c.musicGrabFn(ctx, indexerName, url, title, category)
	}
	return c.grabTo(ctx, indexerName, url, title, category)
}

// pickBestAlbum ranks releases by the profile's quality ladder, seeders breaking ties
// between torrents (nets.beats: a usenet release isn't compared on seeders).
//
// The ladder is the profile's format_scores keyed by tier ("FLAC", "MP3-320"…), so scoring
// is just a lookup. The upgrade sweep picks with it too, among releases already shown to be
// an upgrade (music.UpgradeTiers), which is what stops it at the top of the ladder.
func pickBestAlbum(nets netPolicy, sp quality.StoredProfile, releases []indexer.Release) *indexer.Release {
	var best *indexer.Release
	bestScore := 0
	for i := range releases {
		score, ok := albumScore(sp, releases[i])
		if !ok {
			continue
		}
		if best == nil || nets.beats(releases[i], score, *best, bestScore) {
			best, bestScore = &releases[i], score
		}
	}
	return best
}

// albumScore scores one release. ok=false when the tier isn't wanted (score at or below
// zero, which is how "Lossless only" refuses an MP3 outright) or a reject term matches.
func albumScore(sp quality.StoredProfile, rel indexer.Release) (int, bool) {
	score, code := albumVerdict(sp, rel)
	return score, code == ""
}

// albumVerdict is albumScore saying why a release was turned down ("" when it's
// acceptable), for the search attempt's counts.
func albumVerdict(sp quality.StoredProfile, rel indexer.Release) (int, string) {
	q := string(music.DetectQuality(rel.Title))
	if q == "" {
		return 0, DropNotWanted // an untagged release could be anything; don't gamble the slot on it
	}
	fs, ok := sp.FormatScores[q]
	if !ok || fs <= 0 {
		return 0, DropNotWanted
	}
	text := rel.Title + " " + rel.Description
	if quality.Rejects(sp.Rejected, text) {
		return 0, quality.RejectTerm
	}
	total := fs + quality.KeywordScore(sp.Keywords, text)
	if total < sp.MinFormatScore {
		return 0, quality.RejectMinFormatScore
	}
	// Seeders as the low-order tiebreak, so the tier always dominates.
	return total*1_000_000 + rel.Seeders, ""
}

// musicProfile resolves an artist's profile, falling back to the user's configured default
// for music and only then to a permissive ladder — the same routing the video path uses, so
// a scanned or profile-less artist still follows the user's preferences.
func (c *Coordinator) musicProfile(ctx context.Context, ref string) quality.StoredProfile {
	if sp, err := c.quality.GetStored(ctx, c.effectiveProfile(ctx, ref, quality.MediaMusic)); err == nil && len(sp.FormatScores) > 0 {
		return sp
	}
	return quality.FallbackMusicProfile()
}

// ImportMusicDownloads imports finished album downloads.
//
// Paused while the Music module is off: the finished torrents simply wait in the client and
// import on the first cycle after it's switched back on.
func (c *Coordinator) ImportMusicDownloads(ctx context.Context) {
	if c.music == nil || c.imp == nil || !c.moduleOn(ctx, "music") {
		return
	}
	completed, err := c.downloads.CompletedInCategory(ctx, musicCategory)
	if err != nil {
		c.log.Warn("music import: couldn't list completed downloads — skipping this cycle", "err", err)
		return
	}
	c.pruneMusicRetry(completed)
	for _, it := range completed {
		if it.ContentPath == "" {
			continue
		}
		if c.hashAlreadyImported(ctx, it.Hash) {
			continue
		}
		if c.hasReview(ctx, it.Hash) {
			continue // already held for review — don't re-flag or import
		}
		if !c.musicRetryDue(it.Hash) {
			continue // failed on our side lately; waiting before the next try
		}
		// A discography covers many albums, so it takes a different import path: each folder
		// inside is resolved to its own album. Routed on the release name because that's what
		// distinguishes the two, and the grab for one is recorded against the ARTIST id.
		if music.ParseRelease(it.Name).Discography {
			art, aok := c.artistForRelease(ctx, it.Name)
			if !aok {
				n := c.noteUnmatched(it.Hash)
				if n == 1 {
					c.log.Info("music import: a discography matches no artist in the library", "release", it.Name)
				}
				continue
			}
			res := c.importDiscographyContent(ctx, art, it.ContentPath, it.Name)
			switch {
			case res.taken > 0:
				c.musicTakenReview(ctx, it, res, 0, art.Name+" (discography)")
			case res.splitFailed > 0:
				c.musicSplitReview(ctx, it, res, 0, art.Name+" (discography)")
			case res.tagFailed > 0:
				c.musicTagReview(ctx, it, res, 0, art.Name+" (discography)")
			case res.refused > 0 || res.failed > 0:
				c.musicImportRetry(ctx, it, res, 0, art.Name+" (discography)")
			case res.placed > 0 || res.kept > 0:
				c.settleMusicImport(ctx, it, mediaMusicArtist, art.ID, art.ID, res)
			case res.found:
				c.log.Warn("music import: discography held albums but none matched the library — will retry",
					"artist", art.Name, "release", it.Name)
			default:
				c.log.Warn("music import: discography holds no audio files",
					"artist", art.Name, "release", it.Name, "path", it.ContentPath)
			}
			continue
		}
		// The matcher (MUS-31) decides which album and edition the files are, checks them
		// against the album the download was grabbed for, and holds anything doubtful for
		// review with its reasons (music_match.go).
		c.importMusicDownload(ctx, it)
	}
}

// settleMusicImport closes out a download whose import is done: something landed, or
// what it held is no better than what the album has. Its hash is recorded so it isn't
// imported again, and its grab is closed:
//   - everything in it accounted for: 'imported', so its seed rule applies (removing the
//     torrent at its goal may take its data with it — nothing in it is still needed);
//   - files that matched no track (left for a person to look at), or discography folders
//     that matched no album: 'dismissed', which seed cleanup removes at its goal while
//     KEEPING the files. Marking it imported would let seed cleanup delete the only copy
//     of what the import deliberately left alone.
func (c *Coordinator) settleMusicImport(ctx context.Context, it download.Item, mediaType string, id, artistID int64, res albumImport) {
	c.clearMusicRetry(it.Hash)
	c.recordImportedHash(ctx, it.Hash, it.Name, it.SizeBytes)
	status := grabStatusImported
	if res.unmatched > 0 {
		status = grabStatusDismissed
		c.log.Info("music import: some files matched nothing and stay in the download — its torrent keeps its files at the seed goal",
			"release", it.Name, "left", res.unmatched)
		c.music.AddEvent(ctx, artistID, "imported", fmt.Sprintf(
			"%d file(s) or folder(s) in %s matched nothing and were left in the download for you; its files are kept when it stops seeding",
			res.unmatched, it.Name))
	}
	c.setMusicGrabStatus(ctx, mediaType, id, it.Hash, it.Name, status)
}

// musicRetryState is one held-back music download (see Coordinator.musicRetry).
type musicRetryState struct {
	tries int
	next  time.Time
}

// musicRetryReviewAfter is how many failed tries in a row send a music download to Review
// instead of trying again: by then the recycle bin (or the disk) needs a person.
const musicRetryReviewAfter = 5

// musicRetryDue reports whether a download held back after a failure may be tried again.
func (c *Coordinator) musicRetryDue(hash string) bool {
	c.unmatchedMu.Lock()
	defer c.unmatchedMu.Unlock()
	st, ok := c.musicRetry[hash]
	return !ok || !c.clock().Before(st.next)
}

// pruneMusicRetry forgets held-back downloads that have left the client.
func (c *Coordinator) pruneMusicRetry(completed []download.Item) {
	active := make(map[string]bool, len(completed))
	for _, it := range completed {
		active[it.Hash] = true
	}
	c.unmatchedMu.Lock()
	defer c.unmatchedMu.Unlock()
	for h := range c.musicRetry {
		if !active[h] {
			delete(c.musicRetry, h)
		}
	}
}

func (c *Coordinator) clearMusicRetry(hash string) {
	c.unmatchedMu.Lock()
	delete(c.musicRetry, hash)
	c.unmatchedMu.Unlock()
}

// musicImportRetry holds a download back after an import that failed on our side (the
// recycle bin couldn't take a file a better copy replaces, or a track couldn't be placed),
// for longer each time (30 min doubling to 12 h). After musicRetryReviewAfter tries it goes
// to Review, which stops the retries: something there needs a person. Nothing is lost
// meanwhile — what's held stays held, and the download stays in the client.
func (c *Coordinator) musicImportRetry(ctx context.Context, it download.Item, res albumImport, albumID int64, title string) {
	c.unmatchedMu.Lock()
	if c.musicRetry == nil {
		c.musicRetry = map[string]musicRetryState{}
	}
	st := c.musicRetry[it.Hash]
	st.tries++
	st.next = c.clock().Add(searchBackoff(st.tries))
	c.musicRetry[it.Hash] = st
	c.unmatchedMu.Unlock()

	why := fmt.Sprintf("%d track(s) couldn't be placed", res.failed)
	if res.refused > 0 {
		why = fmt.Sprintf("the recycle bin couldn't take the %d file(s) a better copy would replace", res.refused)
	}
	if st.tries < musicRetryReviewAfter {
		c.log.Warn("music import: failed on our side — keeping everything and trying again later",
			"what", title, "release", it.Name, "why", why, "try", st.tries, "next", st.next)
		return
	}
	c.clearMusicRetry(it.Hash)
	c.log.Warn("music import: still failing — sending to review", "what", title, "release", it.Name, "why", why)
	c.addReview(ctx, Review{
		Hash: it.Hash, Name: it.Name, ContentPath: it.ContentPath, MediaType: "music", ReasonCode: ReasonImportFailed,
		ExpectedID: albumID, ExpectedTitle: title, SizeBytes: it.SizeBytes,
		Reason: "Couldn't import: " + why + " (nothing was changed)",
	})
}

// musicTakenReview sends a download to Review when some of its tracks would land on a file
// another track already has. Retrying wouldn't change that, so it goes straight there;
// tracks already placed stay placed, and nothing else was replaced.
func (c *Coordinator) musicTakenReview(ctx context.Context, it download.Item, res albumImport, albumID int64, title string) {
	c.clearMusicRetry(it.Hash)
	c.log.Warn("music import: tracks would replace another track's file — sending to review",
		"what", title, "release", it.Name, "tracks", res.taken)
	c.addReview(ctx, Review{
		Hash: it.Hash, Name: it.Name, ContentPath: it.ContentPath, MediaType: "music", ReasonCode: ReasonImportFailed,
		ExpectedID: albumID, ExpectedTitle: title, SizeBytes: it.SizeBytes,
		Reason: fmt.Sprintf("%d track(s) would land on a file another track of the album already has; nothing was replaced (the album page explains the clash)", res.taken),
	})
}

// AlbumMatcher resolves release names (torrents in the music category) to the library
// album and artist each was grabbed for; false when music is off or nothing matches. It
// is albumForRelease for many names at once: the artists and their albums are read on the
// first call and reused, so labelling a queue of music torrents costs one library read
// rather than one per torrent.
func (c *Coordinator) AlbumMatcher(ctx context.Context) func(name string) (music.Album, music.Artist, bool) {
	type entry struct {
		artist music.Artist
		albums []music.Album
	}
	var lib []entry
	loaded := false
	return func(name string) (music.Album, music.Artist, bool) {
		if c == nil || c.music == nil {
			return music.Album{}, music.Artist{}, false
		}
		if !loaded {
			loaded = true
			if artists, err := c.music.LibraryArtists(ctx); err == nil {
				for _, a := range artists {
					if albums, err := c.music.Albums(ctx, a.ID); err == nil {
						lib = append(lib, entry{a, albums})
					}
				}
			}
		}
		// The artist's own name first, everywhere; an alias (MUS-38) only when no artist's
		// name fits, so an alias never takes a release another artist's name claims.
		for _, e := range lib {
			for _, al := range e.albums {
				if music.ReleaseIsForAlbum(name, e.artist.Name, al.Title) {
					return al, e.artist, true
				}
			}
		}
		for _, e := range lib {
			names := c.music.ArtistNames(ctx, e.artist)
			if len(names.Aliases) == 0 {
				continue
			}
			for _, al := range e.albums {
				if names.ReleaseIsForAlbum(name, al.Title) {
					return al, e.artist, true
				}
			}
		}
		return music.Album{}, music.Artist{}, false
	}
}

// albumForRelease resolves a release name to a library album.
func (c *Coordinator) albumForRelease(ctx context.Context, name string) (music.Album, music.Artist, bool) {
	artists, err := c.music.LibraryArtists(ctx)
	if err != nil {
		return music.Album{}, music.Artist{}, false
	}
	type entry struct {
		artist music.Artist
		albums []music.Album
	}
	var aliased []entry
	for _, a := range artists {
		albums, err := c.music.Albums(ctx, a.ID)
		if err != nil {
			continue
		}
		for _, al := range albums {
			if music.ReleaseIsForAlbum(name, a.Name, al.Title) {
				return al, a, true
			}
		}
		aliased = append(aliased, entry{a, albums})
	}
	// No artist's own name fits: an alias may (MUS-38), as the release's whole artist part.
	for _, e := range aliased {
		names := c.music.ArtistNames(ctx, e.artist)
		if len(names.Aliases) == 0 {
			continue
		}
		for _, al := range e.albums {
			if names.ReleaseIsForAlbum(name, al.Title) {
				return al, e.artist, true
			}
		}
	}
	return music.Album{}, music.Artist{}, false
}

// importAlbumContent hardlinks a download's audio into the album's library folder.
//
// The result says how many tracks landed or were kept, and whether the download held any
// audio at all, so the caller can tell "import failed, retry" from "nothing here to import".
func (c *Coordinator) importAlbumContent(ctx context.Context, a music.Artist, al music.Album, contentPath, release string) (res albumImport) {
	// Scene music releases routinely ship inside a RAR set.
	if fi, err := os.Stat(contentPath); err == nil && fi.IsDir() {
		if n, xerr := extract.ExtractTree(contentPath); xerr != nil {
			c.log.Warn("music import: archive extraction failed", "path", contentPath, "err", xerr)
		} else if n > 0 {
			c.log.Info("music import: extracted archives before import", "count", n, "path", contentPath)
		}
	}
	files := findAudioFiles(contentPath)
	if len(files) == 0 {
		return res
	}
	res.found = true
	// A disc image with a cue sheet stands in for its tracks (MUS-35).
	files, cues, err := c.expandCueSheets(ctx, contentPath, files)
	if err != nil {
		var problem *cueProblem
		if errors.As(err, &problem) {
			res.splitFailed, res.splitWhy = 1, err.Error()
			c.log.Warn("music import: a cue-sheet album can't be split", "album", al.Title, "why", err)
		} else {
			res.failed = len(files) // read again later
			c.log.Warn("music import: couldn't read a disc image just now", "album", al.Title, "err", err)
		}
		return res
	}
	ctx = withCueSet(ctx, cues)

	// A person chose this album (or a test did): the matcher places what fits, on the
	// best of its editions (MUS-31).
	tracks, matched, unmatched, err := c.matchInto(ctx, al, files, release, false)
	if err != nil || len(tracks) == 0 {
		c.log.Warn("music import: the album has no track listing to place files against",
			"album", al.Title, "err", err)
		return res
	}
	if len(unmatched) > 0 {
		names := make([]string, 0, len(unmatched))
		for _, f := range unmatched {
			names = append(names, filepath.Base(f.Path))
		}
		// Reported rather than force-placed: guessing at these is how an album ends up
		// labelled one track out, which is worse than leaving them for a human.
		c.log.Warn("music import: some files matched no track and were left alone",
			"album", al.Title, "files", strings.Join(names, ", "))
	}

	quality := music.DetectQuality(release)
	res = c.placeAlbumTracks(ctx, a, al, tracks, matched, quality, release, contentPath)
	res.found, res.unmatched = true, len(unmatched)
	c.switchBack(ctx, al, res)
	if res.placed > 0 {
		c.log.Info("music: imported tracks", "artist", a.Name, "album", al.Title,
			"placed", res.placed, "kept", res.kept, "of", len(tracks), "quality", quality)
		c.music.AddEvent(ctx, a.ID, "imported",
			fmt.Sprintf("Imported %d/%d track(s) of %q (%s)", res.placed, len(tracks), al.Title, quality))
		c.bus.Publish("music.imported", map[string]any{
			"artist_id": a.ID, "album_id": al.ID, "placed": res.placed, "artist": a.Name, "album": al.Title, "quality": quality,
		})
	} else if res.kept > 0 && res.refused == 0 {
		c.music.AddEvent(ctx, a.ID, "imported",
			fmt.Sprintf("Kept the copies already held of %q: %s is no better", al.Title, release))
	}
	return res
}

// albumImport is what importing one download into one album came to.
type albumImport struct {
	placed  int // tracks that landed
	kept    int // tracks left as they were: the file held is as good or better
	refused int // better copies not placed because the recycle bin couldn't take the old file
	failed  int // tracks that couldn't be placed or recorded for any other reason
	// taken counts tracks whose file would land on a file another track already has:
	// nothing is replaced, and the download goes to Review rather than retrying.
	taken int
	// unmatched counts files that matched no track (and, for a discography, folders that
	// matched no album): left in the download for a person, so its files must be kept.
	unmatched int
	found     bool // the download held any audio at all
	// tagFailed counts files whose house tags couldn't be written or didn't check out
	// (MUS-33): the download is untouched and goes to Review with tagWhy.
	tagFailed int
	tagWhy    string
	// splitFailed counts disc images (MUS-35) that couldn't be split, or whose split didn't
	// prove exact: nothing from them was placed, and the download goes to Review with
	// splitWhy.
	splitFailed int
	splitWhy    string
}

func (r *albumImport) add(o albumImport) {
	r.placed += o.placed
	r.kept += o.kept
	r.refused += o.refused
	r.failed += o.failed
	r.taken += o.taken
	r.unmatched += o.unmatched
	r.tagFailed += o.tagFailed
	if r.tagWhy == "" {
		r.tagWhy = o.tagWhy
	}
	r.splitFailed += o.splitFailed
	if r.splitWhy == "" {
		r.splitWhy = o.splitWhy
	}
}

// placeAlbumTracks places a download's matched files onto an album's tracks: today's
// filename matching, handed to placement as a match result (music_placement.go).
func (c *Coordinator) placeAlbumTracks(ctx context.Context, a music.Artist, al music.Album, tracks []music.Track, matched map[int64]music.AudioFile, releaseTier music.Quality, release, contentRoot string) albumImport {
	return c.placeFiles(ctx, a, al, tracks, matched, releaseTier, release, contentRoot)
}

// trackTier is the tier and bitrate recorded for an audio file: measured by ffprobe when it
// can be (music.TierFromProbe), so a release labelled FLAC that holds MP3s is recorded as
// the MP3s it is, along with what the probe measured. Without a measurement it falls back
// to the release name's tier, but only when that agrees with the file's extension on
// lossless against lossy; otherwise to the extension's own tag ("MP3", "FLAC"), which
// HeldTier reads conservatively. probe is nil then, so the scan measures it later.
func (c *Coordinator) trackTier(ctx context.Context, path string, releaseTier music.Quality) (music.Quality, int, *mediainfo.AudioInfo) {
	if info, err := c.probeAudio(ctx, path); err == nil {
		if q := music.TierFromProbe(info); q != music.QualityUnknown {
			return q, info.BitrateKbps, &info
		}
	}
	ext := music.FormatOf(path)
	if releaseTier != music.QualityUnknown && ext != "" && music.IsLosslessQuality(releaseTier) == music.IsLossless(ext) {
		return releaseTier, 0, nil
	}
	return music.Quality(ext), 0, nil
}

// heldTier is the tier of the file a track already has. A held file whose tier can't be
// read from what's stored (an old scan's "MP3" with no bitrate) is measured now, and the
// measurement kept: deciding a replacement on a guess could swap a 320 for a 128.
func (c *Coordinator) heldTier(ctx context.Context, t music.Track) music.Quality {
	held := music.HeldTier(t.Format, t.BitrateKbps)
	if held != music.QualityUnknown || t.FilePath == "" {
		return held
	}
	info, err := c.probeAudio(ctx, t.FilePath)
	if err != nil {
		return held
	}
	if q := music.TierFromProbe(info); q != music.QualityUnknown {
		if err := c.music.SetFileMeasured(ctx, t.FileID, t.FilePath, q, info); err != nil {
			c.log.Warn("music: recording a measured tier failed", "track", t.Title, "err", err)
		}
		return q
	}
	return held
}

// probeAudio measures an audio file with ffprobe (read-only), through the test seam when
// set. With no ffprobe installed it reports an error, and callers fall back.
func (c *Coordinator) probeAudio(ctx context.Context, path string) (mediainfo.AudioInfo, error) {
	if part, ok := cueSetFrom(ctx).part(path); ok {
		return part.probe(), nil // a cue sheet's track: what it will be once split
	}
	if c.probeAudioFn != nil {
		return c.probeAudioFn(ctx, path)
	}
	if !mediainfo.Available() {
		return mediainfo.AudioInfo{}, errNoFFprobe
	}
	return mediainfo.ProbeAudio(ctx, path)
}

// errNoFFprobe: the probe can't run here; the tier falls back to names (trackTier).
var errNoFFprobe = errors.New("ffprobe isn't installed")

// afterAlbumImport handles an album a download has just been imported into. Still missing
// tracks: the import counts as a search miss, so the album backs off rather than being
// searched again at once, and the artist's history names what's missing.
func (c *Coordinator) afterAlbumImport(ctx context.Context, a music.Artist, al music.Album) {
	fresh, err := c.music.AlbumRecord(ctx, al.ID)
	if err != nil || fresh.Complete() {
		return
	}
	tracks, err := c.music.Tracks(ctx, al.ID)
	if err != nil {
		return
	}
	var missing []string
	for _, t := range tracks {
		if !t.HasFile {
			missing = append(missing, strconv.Quote(t.Title))
		}
	}
	if len(missing) == 0 {
		return
	}
	if err := c.music.RecordSearchMiss(ctx, al.ID); err != nil {
		c.log.Warn("music: recording a search miss failed", "album", al.Title, "err", err)
	}
	more := ""
	if len(missing) > 5 {
		more = fmt.Sprintf(" and %d more", len(missing)-5)
		missing = missing[:5]
	}
	c.music.AddEvent(ctx, a.ID, "imported", fmt.Sprintf("Imported %d/%d of %q; still missing: %s%s",
		fresh.HaveTracks, fresh.TrackCount, al.Title, strings.Join(missing, ", "), more))
}

// findAudioFiles walks a download for audio, skipping the tiny files that are artwork or
// stray tags rather than music.
func findAudioFiles(contentPath string) []music.AudioFile {
	var out []music.AudioFile
	fi, err := os.Stat(contentPath)
	if err != nil {
		return nil
	}
	if !fi.IsDir() {
		if music.IsAudioFile(contentPath) {
			out = append(out, music.AudioFile{Path: contentPath, SizeBytes: fi.Size()})
		}
		return out
	}
	_ = filepath.WalkDir(contentPath, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !music.IsAudioFile(p) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() < 256<<10 {
			return nil // under 256 KB is a jingle or a broken file, not a track
		}
		out = append(out, music.AudioFile{Path: p, SizeBytes: info.Size(), Disc: discOfFolder(filepath.Base(filepath.Dir(p)))})
		return nil
	})
	return out
}

// reDiscFolder is a folder named for one disc of an album: "CD2", "Disc 1", "disk03".
var reDiscFolder = regexp.MustCompile(`(?i)^(?:cd|dis[ck])[\s._-]*0*(\d{1,2})\b`)

// discOfFolder is the disc a folder name says its tracks are on (0: it doesn't say). It's
// what lets "CD2/03 - X.flac" land on disc 2 rather than on disc 1's track 3.
func discOfFolder(name string) int {
	m := reDiscFolder.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// recordMusicGrab records a grab so the pending guard, seed rules and stall fail-over can
// see it: an album ("music", the album id) or a discography (mediaMusicArtist, the artist
// id). stall_minutes comes from the profile — writing a literal 0 is what made the book
// stall fail-over dead code for months. It returns the grab row's id (0 when it couldn't
// be recorded).
func (c *Coordinator) recordMusicGrab(ctx context.Context, mediaType string, id int64, title, indexerName, profile, infoHash string) int64 {
	defer c.grabsChanged()
	notesFrom(ctx).grabbed(title) // the search attempt this grab came from, if any
	// Same profile the album was chosen under (musicProfile), so the stall window matches.
	profile = c.effectiveProfile(ctx, profile, quality.MediaMusic)
	transport, seedEnabled, seedRatio, seedHours := c.grabPolicy(ctx, indexerName, infoHash)
	// An upgrade grab says so (decided_mode 'upgrade'): its import mustn't move the album
	// to another edition (importMusicDownload).
	key, mode := pickOf(ctx, title)
	rowID, err := store.InsertID(ctx, c.db,
		`INSERT INTO grabs (movie_id, version_id, title, indexer, quality_profile, stall_minutes, seed_enabled, seed_ratio, seed_hours, media_type, info_hash, decided_by, decided_mode, transport)
		 VALUES (?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, title, indexerName, profile, c.quality.StallMinutes(ctx, profile),
		boolToInt(seedEnabled), seedRatio, seedHours, mediaType, infoHash, key, mode, transport)
	if err != nil {
		c.log.Warn("music: recording the grab failed", "kind", mediaType, "id", id, "err", err)
		return 0
	}
	c.noteHashless(mediaType, title, infoHash)
	c.noteHandoff(ctx, mediaType, id, title, infoHash)
	return rowID
}

// pendingMusicGrabTitles returns releases already grabbed for this album and not yet
// imported or failed, so a sweep can't grab the same one twice.
func (c *Coordinator) pendingMusicGrabTitles(ctx context.Context, albumID int64) (map[string]bool, error) {
	return c.pendingTitlesOf(ctx, albumID, "music")
}

func dropPendingMusic(releases []indexer.Release, pending map[string]bool) []indexer.Release {
	if len(pending) == 0 {
		return releases
	}
	out := make([]indexer.Release, 0, len(releases))
	for _, rel := range releases {
		if !pending[normTitle(rel.Title)] {
			out = append(out, rel)
		}
	}
	return out
}

// ensure the library importer is referenced even if the album helpers move.
var _ = library.FoundVideo{}

// GrabDiscography grabs a whole-catalogue pack for an artist.
//
// Deliberately an explicit action rather than the automatic first choice. A discography is
// one torrent covering many albums, which fights the upgrade path in a way album-sized
// releases don't: replace one album with a FLAC later and you either keep seeding files
// you've superseded or break the torrent. It's the right tool for a new artist you own
// nothing of, and the wrong one for topping up a collection — so the caller decides.
func (c *Coordinator) GrabDiscography(ctx context.Context, artistID int64) error {
	if c.music == nil {
		return fmt.Errorf("music module unavailable")
	}
	if !c.moduleOn(ctx, "music") {
		return ErrModuleOff
	}
	a, err := c.music.GetArtist(ctx, artistID)
	if err != nil {
		return err
	}
	sp := c.musicProfile(ctx, a.QualityProfile)
	res, err := c.searchMusic(ctx, indexer.SearchQuery{
		Text: a.Name + " discography", MediaType: indexer.MediaMusic, Limit: 100,
	})
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}
	var cands []indexer.Release
	nets := c.networks(ctx)
	for _, rel := range nets.grabbable(res.Releases) { // only what a client can take
		if music.ReleaseIsDiscographyFor(rel.Title, a.Name) {
			cands = append(cands, rel)
		}
	}
	if len(cands) == 0 {
		return fmt.Errorf("no discography release found for %q", a.Name)
	}
	// Blocks and in-flight grabs of a discography are kept against the artist.
	blocked, err := c.blockedSetMusicArtist(ctx, artistID)
	var pending map[string]bool
	if err == nil {
		pending, err = c.pendingTitlesOf(ctx, artistID, mediaMusicArtist)
	}
	if err != nil {
		c.skipUnreadable(a.Name+" discography", err)
		return err
	}
	cands = dropPendingMusic(dropPendingMusic(cands, blocked), pending) // both are normalized-title sets
	if len(cands) == 0 {
		return fmt.Errorf("every discography release found for %q is blocklisted or already grabbed", a.Name)
	}
	best := pickBestAlbum(nets, sp, cands)
	if best == nil {
		return fmt.Errorf("found discography releases for %q, but none met the quality profile", a.Name)
	}
	if !c.diskOKFor(ctx, best.Indexer, float64(best.SizeBytes)/(1<<30)) {
		return fmt.Errorf("not enough free space for %q (%.1f GB)", best.Title, float64(best.SizeBytes)/(1<<30))
	}
	hash, err := c.grabMusic(ctx, best.Indexer, best.DownloadURL, best.Title, musicCategory)
	if err != nil {
		return err
	}
	// Recorded against the ARTIST id, not an album: the pack covers many, and the import
	// resolves each folder to its own album.
	c.recordMusicGrab(ctx, mediaMusicArtist, artistID, best.Title, best.Indexer, a.QualityProfile, hash)
	c.music.AddEvent(ctx, artistID, "grabbed",
		fmt.Sprintf("Grabbed a discography from %s: %s", best.Indexer, best.Title))
	c.log.Info("music: grabbing discography", "artist", a.Name, "release", best.Title,
		"quality", music.DetectQuality(best.Title))
	return nil
}

// artistForRelease resolves a release name to a library artist (used for discographies,
// which name the artist but no single album).
func (c *Coordinator) artistForRelease(ctx context.Context, name string) (music.Artist, bool) {
	artists, err := c.music.LibraryArtists(ctx)
	if err != nil {
		return music.Artist{}, false
	}
	// Longest name wins, so "Bob Marley and the Wailers" beats "Bob Marley" on a release
	// that names the full act.
	var best music.Artist
	found := false
	for _, a := range artists {
		if music.ReleaseIsDiscographyFor(name, a.Name) && (!found || len(a.Name) > len(best.Name)) {
			best, found = a, true
		}
	}
	return best, found
}

// importDiscographyContent imports a whole-catalogue pack: each album folder inside it is
// matched to one of the artist's albums and imported separately.
//
// Folders that match no album are reported, never force-placed. A discography routinely
// carries bootlegs, singles and live records the artist's release-group list doesn't have,
// and filing those against a near-miss album would put the wrong audio on a real record.
func (c *Coordinator) importDiscographyContent(ctx context.Context, a music.Artist, contentPath, release string) (res albumImport) {
	if fi, err := os.Stat(contentPath); err == nil && fi.IsDir() {
		if n, xerr := extract.ExtractTree(contentPath); xerr != nil {
			c.log.Warn("music import: archive extraction failed", "path", contentPath, "err", xerr)
		} else if n > 0 {
			c.log.Info("music import: extracted archives before import", "count", n, "path", contentPath)
		}
	}
	folders := findAlbumFolders(contentPath)
	if len(folders) == 0 {
		return res
	}
	res.found = true

	albums, err := c.music.Albums(ctx, a.ID)
	if err != nil {
		return res
	}
	var skipped []string
	for _, f := range folders {
		title := f.album
		if title == "" {
			// A folder named just "OK Computer (1997)" has no " - " to split on, so the
			// parser leaves the whole thing in Artist.
			title = f.artist
		}
		al, ok := matchAlbumByTitle(albums, title)
		if !ok {
			skipped = append(skipped, filepath.Base(f.path))
			continue
		}
		r := c.importAlbumFolder(ctx, a, al, f, release)
		res.add(r)
	}
	res.unmatched += len(skipped)
	if len(skipped) > 0 {
		c.log.Warn("music import: discography folders that match no album in the library — left alone",
			"artist", a.Name, "folders", strings.Join(skipped, ", "))
	}
	if res.placed > 0 {
		c.music.AddEvent(ctx, a.ID, "imported",
			fmt.Sprintf("Imported %d track(s) from a discography (%d folder(s) unmatched)", res.placed, len(skipped)))
	}
	return res
}

// importAlbumFolder places one album folder's audio against a known album.
func (c *Coordinator) importAlbumFolder(ctx context.Context, a music.Artist, al music.Album, f albumFolder, release string) albumImport {
	if err := c.music.EnsureTracks(ctx, al); err != nil {
		c.log.Warn("music import: couldn't fetch a track listing", "album", al.Title, "err", err)
		return albumImport{found: true, unmatched: len(f.files)} // nothing to place them against: left for a person
	}
	files, cues, err := c.expandCueSheets(ctx, f.path, f.files)
	if err != nil {
		var problem *cueProblem
		if !errors.As(err, &problem) {
			c.log.Warn("music import: couldn't read a discography folder's disc image just now", "album", al.Title, "err", err)
			return albumImport{found: true, failed: len(f.files)} // tried again later
		}
		c.log.Warn("music import: a discography folder's cue sheet can't be used — left in the download", "album", al.Title, "folder", filepath.Base(f.path), "why", err)
		return albumImport{found: true, unmatched: len(f.files)}
	}
	ctx = withCueSet(ctx, cues)
	tracks, matched, unmatched, err := c.matchGrabbedFolder(ctx, al, files, filepath.Base(f.path))
	if err != nil || len(tracks) == 0 {
		return albumImport{found: true, unmatched: len(f.files)}
	}
	q := music.DetectQuality(release)
	if fq := music.DetectQuality(filepath.Base(f.path)); fq != music.QualityUnknown {
		// A mixed-quality pack often tags each album folder; that's more specific than the
		// pack name and is exactly the case where the overall tag would be wrong.
		q = fq
	}
	res := c.placeAlbumTracks(ctx, a, al, tracks, matched, q, release, f.path)
	res.found, res.unmatched = true, len(unmatched)
	c.switchBack(ctx, al, res)
	if res.placed > 0 {
		c.log.Info("music: imported album from discography", "artist", a.Name, "album", al.Title,
			"placed", res.placed, "kept", res.kept, "of", len(tracks), "quality", q)
	}
	return res
}
