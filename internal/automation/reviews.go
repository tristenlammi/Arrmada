package automation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tristenlammi/arrmada/internal/extract"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/series"
)

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
		n, matched := c.importSeriesInto(ctx, s, r.ContentPath)
		if matched == 0 {
			return fmt.Errorf("no episode files could be imported into %q", s.Title)
		}
		c.series.AddEvent(ctx, s.ID, "imported", fmt.Sprintf("Imported %d episode%s from review: %s", n, plural(n), r.Name))
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
// library, marking each episode present. It returns two counts: placed is the number of
// episodes newly written to disk (for the "imported N" notification), and matched is the
// number recognized as belonging to a known episode (placed OR already present at
// equal-or-better quality). matched drives whether the download is considered handled —
// a pack whose episodes are all already on disk still counts as done so it drops out of
// the downloads view instead of being re-scanned forever.
func (c *Coordinator) importSeriesInto(ctx context.Context, s series.Series, contentPath string) (placed, matched int) {
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
		return 0, 0
	}
	if len(videos) == 0 {
		c.log.Warn("series import: no video files found in the download (all archives? nested oddly?)", "series", s.Title, "content_path", contentPath)
		return 0, 0
	}
	c.log.Info("series import: scanning download", "series", s.Title, "videos", len(videos), "content_path", contentPath)
	// Route episodes into the show's existing on-disk folder when it has one, so a
	// show already stored as "Below Deck" doesn't spawn a duplicate "Below Deck
	// (2013)" folder on the next grab.
	folder := c.series.ExistingFolderName(ctx, s.ID)
	for _, v := range videos {
		rel := parser.Parse(filepath.Base(v.Path))
		refs := c.series.ResolveEpisodes(ctx, s.ID, rel)
		if len(refs) == 0 {
			c.log.Warn("series import: couldn't place file",
				"file", filepath.Base(v.Path), "season", rel.Season, "episodes", rel.Episodes,
				"absolute", rel.AbsoluteEpisodes, "anime", s.IsAnime())
			continue
		}
		matched += len(refs) // recognized — counts as handled even if we don't re-place it
		// Quality gate: leave the existing file alone unless this candidate is a strictly
		// higher resolution. Without this, two releases of the same episode (e.g. a 1080p
		// and a 720p pack) supersede each other on every sweep, flooding the recycle bin.
		if !c.series.WantsFile(ctx, s.ID, refs[0].Season, refs[0].Episode, rel.Resolution) {
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
		ei, ok, err := c.imp.ImportEpisodeInto(folder, s.Title, s.Year, v.Path)
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
			continue // already imported and unchanged — don't re-count or re-notify
		}
		// A double-episode file marks both episodes present (all point at the one file);
		// anime files resolve to their real episode (absolute/positional) first.
		for _, ep := range episodesOf(ei) {
			rs, re := c.series.ResolveEpisode(ctx, s.ID, ei.Season, ep)
			if c.series.SupersedeEpisodeFile(ctx, s.ID, rs, re, ei.TargetPath, ei.SizeBytes, filepath.Base(ei.SourcePath)) == nil {
				placed++
			}
		}
	}
	return placed, matched
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
