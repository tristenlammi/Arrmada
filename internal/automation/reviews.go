package automation

import (
	"context"
	"fmt"

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
		n := c.importSeriesInto(ctx, s, r.ContentPath)
		if n == 0 {
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

// importSeriesInto hardlinks every episode file in contentPath into the given
// series' library, marking each episode present. Returns the count imported.
func (c *Coordinator) importSeriesInto(ctx context.Context, s series.Series, contentPath string) int {
	videos, err := library.FindVideos(contentPath)
	if err != nil {
		return 0
	}
	// Route episodes into the show's existing on-disk folder when it has one, so a
	// show already stored as "Below Deck" doesn't spawn a duplicate "Below Deck
	// (2013)" folder on the next grab.
	folder := c.series.ExistingFolderName(ctx, s.ID)
	imported := 0
	for _, v := range videos {
		ei, ok, err := c.imp.ImportEpisodeInto(folder, s.Title, s.Year, v.Path)
		if err != nil || !ok {
			continue
		}
		if c.series.MarkEpisodeImported(ctx, s.ID, ei.Season, ei.Episode, ei.TargetPath, ei.SizeBytes) == nil {
			imported++
		}
	}
	return imported
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

func titleYear(title string, year int) string {
	if year > 0 {
		return fmt.Sprintf("%s (%d)", title, year)
	}
	return title
}
