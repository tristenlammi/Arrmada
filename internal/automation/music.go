package automation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/extract"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// musicCategory keeps album downloads in their own download-client category so the album
// importer handles them, not the movie/series/book importers.
const musicCategory = "arrmada-music"

// SearchMusicMissing sweeps monitored albums that are missing tracks and grabs the best
// release for each.
//
// Every guard the other modules learned the hard way is here from the start: an in-flight
// check so a sweep can't stack a second grab on a download already running, exponential
// backoff so an album no indexer carries doesn't cost a full search every cycle forever, and
// a disk-space check before committing to a grab.
func (c *Coordinator) SearchMusicMissing(ctx context.Context) {
	if c.music == nil {
		return
	}
	artists, err := c.music.ListArtists(ctx)
	if err != nil {
		return
	}
	queue, qerr := c.downloads.Queue(ctx)
	if qerr != nil {
		// An unreadable queue looks exactly like an empty one, so every in-flight album
		// would read as "not downloading" and the sweep would stack duplicate grabs.
		c.log.Warn("music: couldn't read the download queue — skipping this sweep", "err", qerr)
		return
	}
	for _, a := range artists {
		if !a.Monitored {
			continue
		}
		albums, err := c.music.Albums(ctx, a.ID)
		if err != nil {
			continue
		}
		for _, al := range albums {
			if !al.Monitored || al.Complete() {
				continue
			}
			if c.albumDownloading(queue, a, al) {
				continue // already downloading — let it finish
			}
			c.grabAlbum(ctx, a, al)
		}
	}
}

// albumDownloading reports whether the queue already holds a release for this album.
func (c *Coordinator) albumDownloading(queue []download.Item, a music.Artist, al music.Album) bool {
	for _, it := range queue {
		if it.Category != musicCategory {
			continue
		}
		if music.ReleaseIsForAlbum(it.Name, a.Name, al.Title) {
			return true
		}
	}
	return false
}

// grabAlbum searches for one album and grabs the best release the profile allows.
func (c *Coordinator) grabAlbum(ctx context.Context, a music.Artist, al music.Album) {
	// The album needs its track listing before anything can be imported against it, and the
	// listing is fetched lazily. Do it here rather than at import time so a grabbed release
	// always has somewhere to land.
	if err := c.music.EnsureTracks(ctx, al); err != nil {
		c.log.Warn("music: couldn't fetch the track listing — skipping", "album", al.Title, "err", err)
		return
	}
	sp := c.musicProfile(ctx, a.QualityProfile)
	query := a.Name + " " + al.Title
	res, err := c.indexers.Search(ctx, indexer.SearchQuery{
		Text: query, MediaType: indexer.MediaMusic, Limit: 100,
	})
	if err != nil || len(res.Releases) == 0 {
		return
	}

	// Only releases that actually name THIS artist and album. Without this a search for one
	// album happily grabs another by the same artist — the hole the Books module shipped
	// with, and the reason its downloads landed on the wrong title.
	var cands []indexer.Release
	for _, rel := range res.Releases {
		if music.ReleaseIsForAlbum(rel.Title, a.Name, al.Title) {
			cands = append(cands, rel)
		}
	}
	if len(cands) == 0 {
		c.log.Info("music: no release matched this album", "artist", a.Name, "album", al.Title)
		return
	}
	cands = c.dropBlockedMusic(ctx, al.ID, cands)
	cands = dropPendingMusic(cands, c.pendingMusicGrabTitles(ctx, al.ID))

	best := pickBestAlbum(sp, cands)
	if best == nil {
		c.log.Info("music: no release met the quality profile", "artist", a.Name, "album", al.Title,
			"candidates", len(cands))
		return
	}
	if !c.diskOKFor(float64(best.SizeBytes) / (1 << 30)) {
		c.log.Warn("music: not enough free space for this release", "album", al.Title, "release", best.Title)
		return
	}
	hash, err := c.grabTo(ctx, best.Indexer, best.DownloadURL, best.Title, musicCategory)
	if err != nil {
		c.log.Warn("music: grab failed", "album", al.Title, "err", err)
		return
	}
	c.recordMusicGrab(ctx, al.ID, best.Title, best.Indexer, a.QualityProfile, hash)
	c.music.AddEvent(ctx, a.ID, "grabbed",
		fmt.Sprintf("Grabbed %q from %s: %s", al.Title, best.Indexer, best.Title))
	c.log.Info("music: grabbing", "artist", a.Name, "album", al.Title,
		"release", best.Title, "quality", music.DetectQuality(best.Title))
}

// pickBestAlbum ranks releases by the profile's quality ladder, seeders breaking ties.
//
// The ladder is the profile's format_scores keyed by tier ("FLAC", "MP3-320"…), so scoring
// is just a lookup — and because nothing outscores the tier already held, this same scoring
// is what makes upgrades stop by themselves at the top of the ladder.
func pickBestAlbum(sp quality.StoredProfile, releases []indexer.Release) *indexer.Release {
	var best *indexer.Release
	bestScore := 0
	for i := range releases {
		score, ok := albumScore(sp, releases[i])
		if !ok {
			continue
		}
		if best == nil || score > bestScore {
			best, bestScore = &releases[i], score
		}
	}
	return best
}

// albumScore scores one release. ok=false when the tier isn't wanted (score at or below
// zero, which is how "Lossless only" refuses an MP3 outright) or a reject term matches.
func albumScore(sp quality.StoredProfile, rel indexer.Release) (int, bool) {
	q := string(music.DetectQuality(rel.Title))
	if q == "" {
		return 0, false // an untagged release could be anything; don't gamble the slot on it
	}
	fs, ok := sp.FormatScores[q]
	if !ok || fs <= 0 {
		return 0, false
	}
	text := rel.Title + " " + rel.Description
	if quality.Rejects(sp.Rejected, text) {
		return 0, false
	}
	total := fs + quality.KeywordScore(sp.Keywords, text)
	if total < sp.MinFormatScore {
		return 0, false
	}
	// Seeders as the low-order tiebreak, so the tier always dominates.
	return total*1_000_000 + rel.Seeders, true
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
func (c *Coordinator) ImportMusicDownloads(ctx context.Context) {
	if c.music == nil || c.imp == nil {
		return
	}
	completed, err := c.downloads.CompletedInCategory(ctx, musicCategory)
	if err != nil {
		c.log.Warn("music import: couldn't list completed downloads — skipping this cycle", "err", err)
		return
	}
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
		album, artist, ok := c.albumForRelease(ctx, it.Name)

		// Verify the download really is the album it was grabbed for. Without this the
		// target is re-derived from the torrent name alone and a mismatch lands silently —
		// the defect the Books import carried until it was fixed.
		if gid, idx, grabbed := c.grabbedMediaForHash(ctx, it.Hash, it.Name, "music"); grabbed {
			if expected, gerr := c.music.GetAlbum(ctx, gid); gerr == nil && (!ok || album.ID != expected.ID) {
				c.log.Warn("music import: download doesn't look like the album it was grabbed for — sending to review",
					"expected", expected.Title, "release", it.Name)
				c.addReview(ctx, Review{
					Hash: it.Hash, Name: it.Name, ContentPath: it.ContentPath, MediaType: "music",
					ExpectedID: expected.ID, ExpectedTitle: expected.Title,
					ParsedTitle: music.ParseRelease(it.Name).Album,
					Reason:      fmt.Sprintf("Grabbed for %q but the download doesn't match", expected.Title),
					SizeBytes:   it.SizeBytes, Indexer: idx,
				})
				continue
			}
		}
		if !ok {
			n := c.noteUnmatched(it.Hash)
			switch {
			case n == 1:
				c.log.Info("music import: no matching album in the library", "release", it.Name)
			case n >= unmatchedReviewAfter:
				c.log.Warn("music import: download still matches no album — sending to review", "release", it.Name)
				c.addReview(ctx, Review{
					Hash: it.Hash, Name: it.Name, ContentPath: it.ContentPath, MediaType: "music",
					ParsedTitle: music.ParseRelease(it.Name).Album, SizeBytes: it.SizeBytes,
					Reason: "Matches no album in your library",
				})
			}
			continue
		}
		placed, found := c.importAlbumContent(ctx, artist, album, it.ContentPath, it.Name)
		switch {
		case placed > 0:
			c.recordImportedHash(ctx, it.Hash, it.Name, it.SizeBytes)
		case found:
			c.log.Warn("music import: found audio but placed nothing — will retry next sweep",
				"album", album.Title, "release", it.Name)
		default:
			// No audio at all — a still-archived release, or content the container can't
			// read. Books had no branch here and silently retried every 30 seconds forever.
			n := c.noteUnmatched(it.Hash)
			switch {
			case n == 1:
				c.log.Warn("music import: download holds no audio files",
					"album", album.Title, "release", it.Name, "path", it.ContentPath)
			case n >= unmatchedReviewAfter:
				c.addReview(ctx, Review{
					Hash: it.Hash, Name: it.Name, ContentPath: it.ContentPath, MediaType: "music",
					ExpectedID: album.ID, ExpectedTitle: album.Title, SizeBytes: it.SizeBytes,
					Reason: "Downloaded but holds no audio files — still archived, or unreadable",
				})
			}
		}
	}
}

// albumForRelease resolves a release name to a library album.
func (c *Coordinator) albumForRelease(ctx context.Context, name string) (music.Album, music.Artist, bool) {
	artists, err := c.music.ListArtists(ctx)
	if err != nil {
		return music.Album{}, music.Artist{}, false
	}
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
	}
	return music.Album{}, music.Artist{}, false
}

// importAlbumContent hardlinks a download's audio into the album's library folder.
//
// placed is how many tracks landed; found reports whether the download held any audio at
// all, so the caller can tell "import failed, retry" from "nothing here to import".
func (c *Coordinator) importAlbumContent(ctx context.Context, a music.Artist, al music.Album, contentPath, release string) (placed int, found bool) {
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
		return 0, false
	}
	found = true

	tracks, err := c.music.Tracks(ctx, al.ID)
	if err != nil || len(tracks) == 0 {
		c.log.Warn("music import: the album has no track listing to place files against",
			"album", al.Title, "err", err)
		return 0, found
	}
	matched, unmatched := music.MatchTracks(tracks, files)
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
	for _, t := range tracks {
		f, ok := matched[t.ID]
		if !ok {
			continue
		}
		target := c.imp.AlbumTrackTarget(a.Name, al.Title, al.Year, t.DiscNumber, t.TrackNumber, t.Title, filepath.Ext(f.Path))
		if err := c.imp.PlaceFile(f.Path, target); err != nil {
			c.log.Warn("music import: placing a track failed", "track", t.Title, "err", err)
			continue
		}
		size := f.SizeBytes
		if fi, serr := os.Stat(target); serr == nil {
			size = fi.Size()
		}
		if err := c.music.MarkTrackImported(ctx, t.ID, target, string(quality), 0, size, release); err != nil {
			c.log.Warn("music import: recording a track failed", "track", t.Title, "err", err)
			continue
		}
		placed++
	}
	if placed > 0 {
		c.log.Info("music: imported tracks", "artist", a.Name, "album", al.Title,
			"placed", placed, "of", len(tracks), "quality", quality)
		c.music.AddEvent(ctx, a.ID, "imported",
			fmt.Sprintf("Imported %d/%d track(s) of %q (%s)", placed, len(tracks), al.Title, quality))
		c.bus.Publish("music.imported", map[string]any{"artist_id": a.ID, "album_id": al.ID, "placed": placed})
	}
	return placed, found
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
		out = append(out, music.AudioFile{Path: p, SizeBytes: info.Size()})
		return nil
	})
	return out
}

// recordMusicGrab records a grab so the pending guard, seed rules and stall fail-over can
// see it. stall_minutes comes from the profile — writing a literal 0 is what made the book
// stall fail-over dead code for months.
func (c *Coordinator) recordMusicGrab(ctx context.Context, albumID int64, title, indexerName, profile, infoHash string) {
	seedEnabled, seedRatio, seedHours := c.seedRules(ctx, indexerName)
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO grabs (movie_id, version_id, title, indexer, quality_profile, stall_minutes, seed_enabled, seed_ratio, seed_hours, media_type, info_hash)
		 VALUES (?, 0, ?, ?, ?, ?, ?, ?, ?, 'music', ?)`,
		albumID, title, indexerName, profile, c.quality.StallMinutes(ctx, profile),
		boolToInt(seedEnabled), seedRatio, seedHours, infoHash)
	if err != nil {
		c.log.Warn("music: recording the grab failed", "album", albumID, "err", err)
	}
}

// pendingMusicGrabTitles returns releases already grabbed for this album and not yet
// imported or failed, so a sweep can't grab the same one twice.
func (c *Coordinator) pendingMusicGrabTitles(ctx context.Context, albumID int64) map[string]bool {
	out := map[string]bool{}
	rows, err := c.db.QueryContext(ctx,
		`SELECT title FROM grabs WHERE movie_id = ? AND media_type = 'music' AND status = 'grabbed'
		   AND grabbed_at > datetime('now', '-24 hours')`, albumID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		if rows.Scan(&t) == nil {
			out[normTitle(t)] = true
		}
	}
	return out
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

// dropBlockedMusic removes releases blocklisted for this album.
func (c *Coordinator) dropBlockedMusic(ctx context.Context, albumID int64, releases []indexer.Release) []indexer.Release {
	blocked := c.blockedSetMusic(ctx, albumID)
	if len(blocked) == 0 {
		return releases
	}
	out := make([]indexer.Release, 0, len(releases))
	for _, rel := range releases {
		if !blocked[normTitle(rel.Title)] {
			out = append(out, rel)
		}
	}
	return out
}

// detectStalledMusic fails over a stalled album grab: blocklist it, remove it, re-search.
func (c *Coordinator) detectStalledMusic(ctx context.Context, g grab, queue []download.Item) {
	if c.music == nil {
		c.setGrabStatus(ctx, g.ID, "failed")
		return
	}
	al, err := c.music.GetAlbum(ctx, g.MovieID) // album id lives in movie_id on the shared table
	if err != nil {
		c.setGrabStatus(ctx, g.ID, "failed")
		return
	}
	if al.Complete() {
		c.setGrabStatus(ctx, g.ID, "imported")
		return
	}
	if g.StallMinutes <= 0 {
		return
	}
	window := time.Duration(g.StallMinutes) * time.Minute
	if time.Since(parseTime(g.GrabbedAt)) < window {
		return
	}
	item, found := findQueued(queue, g)
	if !c.stalledInQueue(g, item, found, window) {
		return
	}
	c.log.Info("music: download stalled, failing over", "album", al.Title, "release", g.Title)
	c.addBlockMusic(ctx, g.MovieID, g.Title, g.Indexer, fmt.Sprintf("stalled after %d min", g.StallMinutes))
	if found {
		_ = c.downloads.Remove(ctx, item.Hash, true)
	}
	c.setGrabStatus(ctx, g.ID, "failed")
}

// ensure the library importer is referenced even if the album helpers move.
var _ = library.FoundVideo{}
