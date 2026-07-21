package automation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tristenlammi/arrmada/internal/extract"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// ErrDownloadGone means the review's files are no longer on disk. Distinguished from a
// real failure so the API can answer with "this download is gone" rather than a 500.
var ErrDownloadGone = errors.New("the download is no longer on disk — it was removed or cleaned up since this was held for review")

// Review is a finished download held back from import because its content doesn't
// match what it was grabbed for (e.g. a "Below Deck Mediterranean" pack grabbed
// for "Below Deck"). The admin resolves it: reject, import anyway, import into a
// different library item, or dismiss.
type Review struct {
	ID            int64  `json:"id"`
	Hash          string `json:"hash"`
	Name          string `json:"name"`
	ContentPath   string `json:"content_path"`
	MediaType     string `json:"media_type"` // series | movie
	ExpectedID    int64  `json:"expected_id"`
	ExpectedTitle string `json:"expected_title"`
	ParsedTitle   string `json:"parsed_title"`
	Reason        string `json:"reason"`
	SizeBytes     int64  `json:"size_bytes"`
	Indexer       string `json:"indexer"`
	CreatedAt     string `json:"created_at"`
}

// hasReview reports whether a download hash already has a review row (pending or
// resolved) — so the import loop neither re-flags nor re-imports it.
func (c *Coordinator) hasReview(ctx context.Context, hash string) bool {
	if hash == "" {
		return false
	}
	var n int
	_ = c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM import_reviews WHERE hash = ?`, hash).Scan(&n)
	return n > 0
}

func (c *Coordinator) addReview(ctx context.Context, r Review) {
	if c.hasReview(ctx, r.Hash) {
		return
	}
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO import_reviews (hash, name, content_path, media_type, expected_id, expected_title, parsed_title, reason, size_bytes, indexer)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Hash, r.Name, r.ContentPath, r.MediaType, r.ExpectedID, r.ExpectedTitle, r.ParsedTitle, r.Reason, r.SizeBytes, r.Indexer)
	if err != nil {
		c.log.Warn("review: record failed", "name", r.Name, "err", err)
		return
	}
	c.log.Info("import held for review", "name", r.Name, "reason", r.Reason)
	c.bus.Publish("import.held", map[string]any{"name": r.Name, "reason": r.Reason})
}

// ListReviews returns the pending review items, newest first.
func (c *Coordinator) ListReviews(ctx context.Context) ([]Review, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, hash, name, content_path, media_type, expected_id, expected_title, parsed_title, reason, size_bytes, indexer, created_at
		 FROM import_reviews WHERE status = 'pending' ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		var r Review
		if err := rows.Scan(&r.ID, &r.Hash, &r.Name, &r.ContentPath, &r.MediaType, &r.ExpectedID, &r.ExpectedTitle, &r.ParsedTitle, &r.Reason, &r.SizeBytes, &r.Indexer, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (c *Coordinator) getReview(ctx context.Context, id int64) (Review, error) {
	var r Review
	err := c.db.QueryRowContext(ctx,
		`SELECT id, hash, name, content_path, media_type, expected_id, expected_title, parsed_title, reason, size_bytes, indexer, created_at
		 FROM import_reviews WHERE id = ?`, id).
		Scan(&r.ID, &r.Hash, &r.Name, &r.ContentPath, &r.MediaType, &r.ExpectedID, &r.ExpectedTitle, &r.ParsedTitle, &r.Reason, &r.SizeBytes, &r.Indexer, &r.CreatedAt)
	return r, err
}

func (c *Coordinator) resolveReview(ctx context.Context, id int64) error {
	_, err := c.db.ExecContext(ctx, `UPDATE import_reviews SET status = 'resolved' WHERE id = ?`, id)
	return err
}

// grabbedMediaFor finds the media a finished download was grabbed for, matching
// the download name to a grab record (normalized). Returns the media id + indexer.
func (c *Coordinator) grabbedMediaFor(ctx context.Context, name, mediaType string) (id int64, indexer string, ok bool) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT movie_id, title, indexer FROM grabs WHERE media_type = ? ORDER BY id DESC LIMIT 1000`, mediaType)
	if err != nil {
		return 0, "", false
	}
	defer rows.Close()
	target := normTitle(name)
	for rows.Next() {
		var mid int64
		var title, idx string
		if rows.Scan(&mid, &title, &idx) != nil {
			continue
		}
		if normTitle(title) == target {
			return mid, idx, true
		}
	}
	return 0, "", false
}

// HoldMovieImport is the import gate for the generic movie importer: it holds a
// finished movie download for review when it was grabbed for one movie but its
// content parses to a different one. Returns (reason, hold).
func (c *Coordinator) HoldMovieImport(ctx context.Context, hash, name, contentPath string) (string, bool) {
	if c.movies == nil {
		return "", false
	}
	if c.hasReview(ctx, hash) {
		return "held for review", true // already flagged or resolved — never auto-import
	}
	mid, indexer, grabbed := c.grabbedMediaFor(ctx, name, "movie")
	if !grabbed {
		return "", false // not something we grabbed for a specific movie — import as usual
	}
	expected, err := c.movies.Get(ctx, mid)
	if err != nil {
		return "", false
	}
	parsed := parser.Parse(name)
	if m, matched := c.movies.Match(ctx, parsed.Title, parsed.Year); matched && m.ID == expected.ID {
		return "", false // content matches the movie it was grabbed for
	}
	reason := fmt.Sprintf("Grabbed for %q but the download looks like %q", titleYear(expected.Title, expected.Year), titleYear(parsed.Title, parsed.Year))
	c.addReview(ctx, Review{
		Hash: hash, Name: name, ContentPath: contentPath, MediaType: "movie",
		ExpectedID: expected.ID, ExpectedTitle: expected.Title, ParsedTitle: parsed.Title,
		Reason: reason, Indexer: indexer,
	})
	return reason, true
}

// HandleMovieImportFailure reacts to a movie download that finished but could not be
// imported. The import sweep runs every 30 seconds, so without this it retried the same
// broken download forever and the release stayed a valid candidate for re-grabbing.
//
// Only a download with NO video is blocklisted and removed. That's junk — a fake, an
// empty folder, an archive set that won't unpack — and can never import. A failure on a
// download that DOES contain video is deliberately left alone: those are far more likely
// transient (disk full, permissions, a file still being moved), and blocklisting a good
// release because of a temporary error would be worse than retrying.
func (c *Coordinator) HandleMovieImportFailure(ctx context.Context, hash, name, contentPath string, cause error) {
	if c.movies == nil {
		return
	}
	if vids, err := library.FindVideos(contentPath); err != nil || len(vids) > 0 {
		return // has video, or we can't tell — treat as transient and retry
	}
	mid, indexerName, grabbed := c.grabbedMediaFor(ctx, name, "movie")
	if !grabbed {
		m, ok := c.movies.MatchRelease(ctx, name)
		if !ok {
			return // not a release we can attribute to a movie — nothing to blocklist against
		}
		mid = m.ID
	}
	reason := "downloaded but contained no video"
	if hasExecutable(contentPath) {
		reason = "download contained executables and no video (possible fake/malware)"
		// Hostile release — block it library-wide, not just for this movie.
		c.addBlockGlobal(ctx, name, indexerName, reason)
	}
	if err := c.addBlock(ctx, mid, name, indexerName, "", reason); err != nil {
		c.log.Warn("movie: blocklist after failed import failed", "release", name, "err", err)
	}
	c.log.Warn("movie import: nothing importable — blocklisted so it isn't re-grabbed",
		"release", name, "reason", reason, "err", cause)
	c.removeIfNoVideo(ctx, hash, name, contentPath)
}

// --- review actions -------------------------------------------------------

// RejectReview removes the download (and its files), blocklists the release so
// auto-search won't grab it again, and resolves the review.
func (c *Coordinator) RejectReview(ctx context.Context, id int64) error {
	r, err := c.getReview(ctx, id)
	if err != nil {
		return err
	}
	if r.Hash != "" && c.downloads != nil {
		if err := c.downloads.Remove(ctx, r.Hash, true); err != nil {
			c.log.Warn("review: remove download failed", "hash", r.Hash, "err", err)
		}
	}
	switch r.MediaType {
	case "series":
		c.addBlockSeries(ctx, r.ExpectedID, r.Name, r.Indexer, "rejected in review")
	case "movie":
		_ = c.addBlock(ctx, r.ExpectedID, r.Name, r.Indexer, "", "rejected in review")
	}
	return c.resolveReview(ctx, id)
}

// DismissReview resolves the review without touching the download (admin will
// handle it manually).
func (c *Coordinator) DismissReview(ctx context.Context, id int64) error {
	return c.resolveReview(ctx, id)
}

// ImportReview imports a held download into a library item and resolves it. When
// targetID > 0 the content is imported into that item (reassign); otherwise into
// the item it was originally grabbed for (import anyway).
func (c *Coordinator) ImportReview(ctx context.Context, id, targetID int64) error {
	r, err := c.getReview(ctx, id)
	if err != nil {
		return err
	}
	// A review can outlive its download: the torrent gets removed, or the folder is
	// cleaned up, long after the item was held. That's an ordinary situation and the user
	// should be told plainly — it surfaced as a bare HTTP 500 with the raw stat error
	// buried in the server log, which tells them nothing about what to do.
	if r.ContentPath == "" {
		return ErrDownloadGone
	}
	if _, serr := os.Stat(r.ContentPath); serr != nil {
		return fmt.Errorf("%w: %s", ErrDownloadGone, r.ContentPath)
	}
	dest := r.ExpectedID
	if targetID > 0 {
		dest = targetID
	}
	switch r.MediaType {
	case "series":
		if c.series == nil {
			return fmt.Errorf("series module unavailable")
		}
		s, err := c.series.Get(ctx, dest)
		if err != nil {
			return err
		}
		n, matched, _ := c.importSeriesInto(ctx, s, r.ContentPath)
		if matched == 0 {
			return fmt.Errorf("no episode files could be imported into %q", s.Title)
		}
		c.series.AddEvent(ctx, s.ID, "imported", fmt.Sprintf("Imported %d episode%s from review: %s", n, plural(n), r.Name))
		c.seriesImported(ctx, s.ID)
	case "movie":
		if c.movies == nil {
			return fmt.Errorf("movies module unavailable")
		}
		if err := c.movies.ManualImport(ctx, dest, r.ContentPath); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown media type %q", r.MediaType)
	}
	return c.resolveReview(ctx, id)
}

// importSeriesInto hardlinks every episode file in contentPath into the given series'
// library, marking each episode present. It returns three counts: placed is the number of
// episodes newly written to disk (for the "imported N" notification), matched is the
// number recognized as belonging to a known episode (placed OR already present at
// equal-or-better quality), and unresolved is the number of video files whose numbering
// couldn't be mapped onto a known episode at all. matched drives whether the download is
// considered handled — a pack whose episodes are all already on disk still counts as done
// so it drops out of the downloads view instead of being re-scanned forever.
//
// unresolved is what separates "this release's numbering doesn't line up with the
// metadata" from "this release simply doesn't contain the rest of the season". Both look
// identical from placed/matched alone, and conflating them told users a perfectly good
// partial pack had a numbering fault.
func (c *Coordinator) importSeriesInto(ctx context.Context, s series.Series, contentPath string) (placed, matched, unresolved int) {
	// Unpack any archives first (scene releases ship the episode inside a RAR set — this
	// is the Unpackerr job). Recursive, so a season pack's per-episode subfolders unpack.
	if fi, err := os.Stat(contentPath); err == nil && fi.IsDir() {
		if n, xerr := extract.ExtractTree(contentPath); xerr != nil {
			c.log.Warn("series: archive extraction failed", "path", contentPath, "err", xerr)
		} else if n > 0 {
			c.log.Info("series: extracted archives before import", "count", n, "path", contentPath)
		}
	}
	videos, err := library.FindVideos(contentPath)
	if err != nil {
		c.log.Warn("series import: couldn't scan the download folder for videos",
			"series", s.Title, "content_path", contentPath, "err", err)
		return 0, 0, 0
	}
	if len(videos) == 0 {
		c.log.Warn("series import: no video files found in the download (all archives? nested oddly?)", "series", s.Title, "content_path", contentPath)
		return 0, 0, 0
	}
	c.log.Info("series import: scanning download", "series", s.Title, "videos", len(videos), "content_path", contentPath)
	// Route episodes into the show's existing on-disk folder when it has one, so a
	// show already stored as "Below Deck" doesn't spawn a duplicate "Below Deck
	// (2013)" folder on the next grab.
	folder := c.series.ExistingFolderName(ctx, s.ID)
	// Following the existing folder is right when it's this show's own. It is NOT right
	// when another series already stores episodes there: two shows in one directory merge
	// their season folders, and any episode number they share overwrites. Observed with
	// "Teen Titans Go!" writing into a folder named "Teen Titans".
	//
	// Reported rather than repaired — moving a library's files unprompted is far more
	// dangerous than the collision itself, and which show should own the folder is the
	// user's call.
	if folder != "" {
		if others := c.series.FolderSharedWith(ctx, s.ID, folder); len(others) > 0 {
			names := make([]string, 0, len(others))
			for _, id := range others {
				if o, err := c.series.Get(ctx, id); err == nil {
					names = append(names, o.Title)
				}
			}
			c.log.Warn("series import: two shows share one library folder — episodes may overwrite each other",
				"folder", folder, "importing", s.Title, "also_stored_here", strings.Join(names, ", "))
		}
	}
	// Quality lives on the RELEASE, not always on each file. Packs routinely name files
	// "Show - 1x01 - Title.mkv" and put "1080p BDRip x265" only on the folder, so every
	// file parsed to unknown resolution, lost the upgrade comparison against whatever was
	// already on disk, and the whole pack was skipped as "already has an equal-or-better
	// file" — 120 of 122 episodes in one case.
	release := parser.Parse(filepath.Base(contentPath))
	for _, v := range videos {
		rel := inheritQuality(parser.Parse(filepath.Base(v.Path)), release)
		refs := c.series.ResolveEpisodes(ctx, s.ID, rel)
		if len(refs) == 0 {
			unresolved++
			c.log.Warn("series import: couldn't place file",
				"file", filepath.Base(v.Path), "season", rel.Season, "episodes", rel.Episodes,
				"absolute", rel.AbsoluteEpisodes, "anime", s.IsAnime())
			continue
		}
		refs = c.correctRefsByTitle(ctx, s, filepath.Base(v.Path), refs)
		c.warnAnimeTitleMismatch(ctx, s, filepath.Base(v.Path), refs)
		matched += len(refs) // recognized — counts as handled even if we don't re-place it
		// Quality gate: leave the existing file alone unless this candidate is a strictly
		// higher resolution. Without this, two releases of the same episode (e.g. a 1080p
		// and a 720p pack) supersede each other on every sweep, flooding the recycle bin.
		if !c.wantsEpisodeFile(ctx, s, refs[0].Season, refs[0].Episode, rel, v.Size) {
			// Say so. Without this a whole pack can resolve onto episodes that already
			// have a file and be skipped in total silence — the download looks handled
			// and nothing explains why nothing appeared. Includes what it resolved TO,
			// which is what tells you a scene-season split was mapped wrongly.
			c.log.Info("series import: skipping file — that episode already has an equal-or-better file",
				"series", s.Title, "file", filepath.Base(v.Path),
				"resolved_to", fmt.Sprintf("S%02dE%02d", refs[0].Season, refs[0].Episode),
				"candidate_resolution", string(rel.Resolution))
			continue
		}
		ei, ok, err := c.imp.ImportEpisodeIntoWith(folder, s.Title, s.Year, v.Path, release)
		if err != nil {
			continue
		}
		if !ok {
			// No SxxExx — for anime this is an absolute-numbered file ("Show - 137"):
			// resolve the absolute number and place it under that season/episode.
			placed += c.importAbsoluteEpisode(ctx, s, folder, v.Path)
			continue
		}
		if ei.Method == "already" {
			// The file is in place and unchanged, so there's nothing to import — but the
			// recorded source release may still be wrong. Anything imported before the
			// pack-quality fix recorded a bare filename carrying no resolution, which
			// leaves the episode looking like unknown quality forever: every future
			// release outranks it, and upgrade scoring has no baseline. Re-running the
			// import is the natural way to repair that, and it did nothing because this
			// short-circuit sits in front of the write.
			c.repairSourceRelease(ctx, s, ei, contentPath, release)
			continue // already imported and unchanged — don't re-count or re-notify
		}
		// A double-episode file marks both episodes present (all point at the one file);
		// anime files resolve to their real episode (absolute/positional) first.
		// Record the name that actually describes the quality. A pack's per-file names
		// often don't ("Parks and Recreation - 1x01 - Make My Pit a Park.mkv"), and the
		// library file is renamed on import, so recording the bare filename left the
		// episode with NO resolution recorded anywhere — which any future 1080p release
		// would then outrank, re-importing the same quality forever. The release name
		// carries it, so use that when the file's own name doesn't.
		sourceName := filepath.Base(ei.SourcePath)
		if parser.Parse(sourceName).Resolution == "" && release.Resolution != "" {
			sourceName = filepath.Base(contentPath)
		}
		for _, ep := range episodesOf(ei) {
			rs, re := c.series.ResolveEpisode(ctx, s.ID, ei.Season, ep)
			if c.series.SupersedeEpisodeFile(ctx, s.ID, rs, re, ei.TargetPath, ei.SizeBytes, sourceName) == nil {
				placed++
			}
		}
	}
	return placed, matched, unresolved
}

// importAbsoluteEpisode places an anime file that carries only an absolute episode
// number (no SxxExx). It resolves the number to a (season, episode) via the series'
// metadata, then places + marks it. Returns how many episodes it imported.
func (c *Coordinator) importAbsoluteEpisode(ctx context.Context, s series.Series, folder, path string) int {
	if !s.IsAnime() {
		return 0
	}
	refs := c.series.ResolveEpisodes(ctx, s.ID, parser.Parse(filepath.Base(path)))
	n := 0
	for _, ref := range refs {
		ei, ok, err := c.imp.ImportEpisodeAs(folder, s.Title, s.Year, ref.Season, ref.Episode, path)
		if err != nil || !ok || ei.Method == "already" {
			continue
		}
		if c.series.SupersedeEpisodeFile(ctx, s.ID, ref.Season, ref.Episode, ei.TargetPath, ei.SizeBytes, filepath.Base(ei.SourcePath)) == nil {
			n++
		}
	}
	return n
}

// episodesOf returns every episode number an imported file covers (a double-
// episode file covers two), falling back to the single Episode field.
func episodesOf(ei *library.EpisodeImport) []int {
	if len(ei.Episodes) > 0 {
		return ei.Episodes
	}
	return []int{ei.Episode}
}

// recordImportedHash notes a finished download as imported in the shared imports
// table, so the downloads view drops it once it's in the library (it keeps
// seeding in the client). Series/book imports run outside the movie import flow,
// which is the only one that records automatically.
func (c *Coordinator) recordImportedHash(ctx context.Context, hash, title string, size int64) {
	if hash == "" {
		return
	}
	_, _ = c.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO imports (download_hash, source_path, target_path, title, size_bytes)
		 VALUES (?, '', '', ?, ?)`,
		hash, title, size)
}

// hashAlreadyImported reports whether a finished download has already been imported
// (its hash is recorded). Guards the series import loop from re-importing the same
// torrent every cycle — which, with quality-upgrade supersede, otherwise ping-pongs
// two packs for the same episodes and floods the recycle bin.
func (c *Coordinator) hashAlreadyImported(ctx context.Context, hash string) bool {
	if hash == "" {
		return false
	}
	var one int
	_ = c.db.QueryRowContext(ctx, `SELECT 1 FROM imports WHERE download_hash = ? LIMIT 1`, hash).Scan(&one)
	return one == 1
}

func titleYear(title string, year int) string {
	if year > 0 {
		return fmt.Sprintf("%s (%d)", title, year)
	}
	return title
}

// inheritQuality fills in quality attributes a pack states only once, on the release,
// rather than on every file inside it.
//
// A file that names its own resolution always wins — a pack can hold mixed quality, and
// the file is the more specific claim. Only genuinely-absent fields are borrowed.
func inheritQuality(file, release parser.Release) parser.Release {
	if file.Resolution == "" {
		file.Resolution = release.Resolution
	}
	if file.Source == "" {
		file.Source = release.Source
	}
	return file
}

// wantsEpisodeFile decides whether a downloaded file should replace what an episode
// already holds, applying the SAME rule the searcher used when it decided to grab.
//
// series.WantsFile can only compare resolution — it sees a filename and nothing else. But
// a profile may also say "upgrade when a release is at least N Mbps better", and the
// upgrade searcher honours that. With the import gate ignoring it, a bitrate upgrade was
// grabbed, downloaded, and then refused for not raising the resolution: bandwidth spent,
// file discarded, episode unchanged, and free to happen again on the next sweep.
//
// Resolution still governs first — it must never DROP, and a genuine resolution increase
// is always taken. The bitrate margin only decides the equal-resolution case, which is
// exactly where the old rule said "no" to everything.
func (c *Coordinator) wantsEpisodeFile(ctx context.Context, s series.Series, season, episode int, cand parser.Release, candBytes int64) bool {
	res := cand.Resolution
	cur := c.series.CurrentEpisodeFile(ctx, s.ID, season, episode)
	if cur.Path == "" {
		return true // nothing there yet
	}
	if _, err := os.Stat(cur.Path); err != nil {
		return true // recorded file is gone from disk — re-import it
	}

	// The library file is renamed on import, so its name often carries neither the
	// resolution nor the codec. The release it came from does, so prefer that and fall
	// back to the filename.
	curParsed := parser.Parse(filepath.Base(cur.Path))
	if cur.SourceRelease != "" {
		src := parser.Parse(cur.SourceRelease)
		if curParsed.Resolution == "" {
			curParsed.Resolution = src.Resolution
		}
		if curParsed.Codec == "" {
			curParsed.Codec = src.Codec
		}
	}
	curRes := curParsed.Resolution
	switch {
	case parser.ResolutionRank(res) > parser.ResolutionRank(curRes):
		return true // a real resolution upgrade
	case parser.ResolutionRank(res) < parser.ResolutionRank(curRes):
		return false // never downgrade
	}

	// Equal resolution: defer to the profile's bitrate margin, if it set one.
	const bytesPerGB = 1 << 30
	if c.quality.IsBitrateUpgrade(ctx, s.QualityProfile,
		quality.Encode{SizeGB: float64(candBytes) / bytesPerGB, Codec: cand.Codec},
		quality.Encode{SizeGB: float64(cur.SizeBytes) / bytesPerGB, Codec: curParsed.Codec},
		cur.RuntimeMin) {
		c.log.Info("series import: replacing an equal-resolution file — the profile's bitrate margin is met",
			"series", s.Title, "episode", fmt.Sprintf("S%02dE%02d", season, episode),
			"current_gb", float64(cur.SizeBytes)/bytesPerGB, "candidate_gb", float64(candBytes)/bytesPerGB)
		return true
	}
	return false
}

// repairSourceRelease upgrades an episode's recorded source release when the stored one
// carries no resolution and a better name is available.
//
// Only ever adds information: if the stored name already states a resolution it's left
// alone, since it's the faithful record of what the file actually came from.
func (c *Coordinator) repairSourceRelease(ctx context.Context, s series.Series, ei *library.EpisodeImport, contentPath string, release parser.Release) {
	better := filepath.Base(ei.SourcePath)
	if parser.Parse(better).Resolution == "" {
		if release.Resolution == "" {
			return // nothing better to record
		}
		better = filepath.Base(contentPath)
	}
	for _, ep := range episodesOf(ei) {
		rs, re := c.series.ResolveEpisode(ctx, s.ID, ei.Season, ep)
		cur := c.series.CurrentEpisodeFile(ctx, s.ID, rs, re)
		if parser.Parse(cur.SourceRelease).Resolution != "" {
			continue // already records a resolution — leave the faithful record alone
		}
		if err := c.series.SetEpisodeSourceRelease(ctx, s.ID, rs, re, better); err != nil {
			continue
		}
		c.log.Info("series import: recorded the release name for an already-imported episode",
			"series", s.Title, "episode", fmt.Sprintf("S%02dE%02d", rs, re), "source_release", better)
	}
}

// warnAnimeTitleMismatch surfaces — without ever moving anything — an anime file whose own
// episode title disagrees with the episode its number resolved to.
//
// Anime is placed by absolute number and TheXEM, and has no title safety net:
// correctRefsByTitle deliberately skips it, because fansub titles are romanized too
// inconsistently to place by. But that also means a release whose absolute scheme diverges
// from TVDB's (a group counting recaps TVDB doesn't, say) would be renamed to the wrong
// episode in silence. So when a fansub file DOES carry a title and it clearly doesn't match
// the resolved episode, log a warning to make a suspected misnumber visible. It never
// re-places the file — the title isn't trustworthy enough to act on, only to flag.
func (c *Coordinator) warnAnimeTitleMismatch(ctx context.Context, s series.Series, fileName string, refs []series.EpisodeRef) {
	if !s.IsAnime() || len(refs) != 1 {
		return // scoped to a single clear placement; packs/multi-refs are out of scope
	}
	fileTitle := parser.EpisodeTitleFrom(fileName)
	if fileTitle == "" {
		return // most fansub files carry no title — nothing to check against
	}
	got := c.series.EpisodeTitle(ctx, s.ID, refs[0].Season, refs[0].Episode)
	if got == "" || titlesAlike(fileTitle, got) {
		return
	}
	c.log.Warn("series import: anime file's title doesn't match the episode its number resolved to — the release may number episodes differently than TVDB; verify this placement",
		"series", s.Title, "file", fileName, "file_title", fileTitle,
		"resolved_to", fmt.Sprintf("S%02dE%02d", refs[0].Season, refs[0].Episode), "resolved_title", got)
}

// correctRefsByTitle re-points a file at the episode its own TITLE identifies, when the
// number it carries resolves somewhere else.
//
// Metadata sources genuinely disagree about episode numbering. TMDB merges Parks and
// Recreation's two-part "London" into one 44-minute episode 1; TVDB — which nearly every
// release is numbered against — splits it into episodes 1 and 2. Everything after it is
// therefore one slot apart, so a release numbered 6x03 lands on TMDB's episode 3 when it
// is really TMDB's episode 2, and the whole rest of the season shifts with it.
//
// The number is ambiguous between sources; the title is not. When a file names an episode
// and exactly one episode in that season carries that title, the title wins.
//
// Deliberately conservative: it needs a title in the filename, a unique match, and does
// nothing when the resolved episode already agrees. Most releases carry no title at all
// and are untouched.
func (c *Coordinator) correctRefsByTitle(ctx context.Context, s series.Series, fileName string, refs []series.EpisodeRef) []series.EpisodeRef {
	if len(refs) == 0 {
		return refs
	}
	// Anime is resolved through absolute numbering and TheXEM scene maps, which are
	// purpose-built and far more authoritative than a fuzzy title match — and fansub
	// titles are romanized inconsistently, so they'd match badly. Leave that path alone.
	if s.IsAnime() {
		return refs
	}
	// Single-episode files only. A multi-episode file spans several metadata episodes and
	// its one title can legitimately match just the first of them — "Space House" against
	// TMDB's "Space House (1)" — so acting on that would collapse a file covering four
	// episodes down to one and lose the other three.
	//
	// The shifted season still comes out right without it: the two-part file keeps its
	// number-derived episodes, and the correctly-titled single that belongs on the second
	// of those supersedes it a moment later.
	if len(refs) != 1 {
		return refs
	}
	fileTitle := parser.EpisodeTitleFrom(fileName)
	if fileTitle == "" {
		return refs // no title to reason from
	}
	season := refs[0].Season
	titles := c.series.SeasonEpisodeTitles(ctx, s.ID, season)
	if len(titles) == 0 {
		return refs
	}

	var match int
	hits := 0
	for num, t := range titles {
		if titlesAlike(fileTitle, t) {
			match, hits = num, hits+1
		}
	}
	if hits != 1 {
		return refs // ambiguous or unknown — the number is all we have
	}
	if refs[0].Episode == match {
		return refs // already right
	}

	was := make([]int, 0, len(refs))
	for _, r := range refs {
		was = append(was, r.Episode)
	}
	c.log.Info("series import: placed by episode title, not the number in the filename — the release numbers episodes differently from the metadata",
		"series", s.Title, "file", fileName, "title", fileTitle,
		"season", season, "filename_said", was, "metadata_says", match)
	return []series.EpisodeRef{{Season: season, Episode: match}}
}

// minPrefixTitleLen is how much of a title must be present before a PREFIX counts as a
// match. Releases truncate long titles, so a prefix has to be allowed — but "Go" is a
// prefix of "Go Big or Go Home", and acting on that would move a file onto an episode it
// has nothing to do with. Long enough to be evidence, short enough to accept a real
// truncation.
const minPrefixTitleLen = 12

// titlesAlike reports whether a filename's episode title and a metadata title name the
// same episode. Punctuation, case and accents differ freely between the two and none of
// that is a real disagreement.
//
// This decides where a file is PLACED, so it errs toward saying no. An empty side means
// no evidence, not a match — an episode whose title is punctuation only ("!!!") normalizes
// to nothing, and treating that as alike made it match every file in the season.
func titlesAlike(a, b string) bool {
	ka, kb := titleKey(a), titleKey(b)
	if ka == "" || kb == "" {
		return false // nothing to compare — not evidence of anything
	}
	if ka == kb {
		return true
	}
	short, long := ka, kb
	if len(short) > len(long) {
		short, long = long, short
	}
	return len(short) >= minPrefixTitleLen && strings.HasPrefix(long, short)
}
