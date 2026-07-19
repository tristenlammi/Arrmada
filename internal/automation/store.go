package automation

import (
	"context"
	"database/sql"
	"strings"
	"unicode"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// BlockEntry is a blocklisted release for a movie.
type BlockEntry struct {
	ID        int64  `json:"id"`
	MovieID   int64  `json:"movie_id"`
	Title     string `json:"title"`
	Indexer   string `json:"indexer,omitempty"`
	Reason    string `json:"reason,omitempty"`
	CreatedAt string `json:"created_at"`
}

// grab is a recorded automatic grab, tracked for stall detection and seed
// cleanup. The seed policy is snapshotted here so cleanup doesn't depend on the
// originating indexer still existing.
type grab struct {
	ID           int64
	MovieID      int64
	VersionID    int64
	Title        string
	Indexer      string
	Profile      string
	StallMinutes int
	GrabbedAt    string
	SeedEnabled  bool
	SeedRatio    float64
	SeedHours    int
	MediaType    string // "movie" | "series"
}

// addBlock blocklists a release for a movie.
func (c *Coordinator) addBlock(ctx context.Context, movieID int64, title, indexer, downloadURL, reason string) error {
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO blocklist (movie_id, norm_title, title, indexer, download_url, reason)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		movieID, normTitle(title), title, indexer, downloadURL, reason)
	return err
}

// listBlocks returns a movie's blocklisted releases (newest first).
func (c *Coordinator) listBlocks(ctx context.Context, movieID int64) ([]BlockEntry, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, movie_id, title, indexer, reason, created_at FROM blocklist
		 WHERE movie_id = ? AND media_type = 'movie' ORDER BY id DESC`, movieID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlockEntry
	for rows.Next() {
		var b BlockEntry
		if err := rows.Scan(&b.ID, &b.MovieID, &b.Title, &b.Indexer, &b.Reason, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// listBlocksSeries returns a series' blocklisted releases (newest first).
func (c *Coordinator) listBlocksSeries(ctx context.Context, seriesID int64) ([]BlockEntry, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, movie_id, title, indexer, reason, created_at FROM blocklist
		 WHERE movie_id = ? AND media_type = 'series' ORDER BY id DESC`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlockEntry
	for rows.Next() {
		var b BlockEntry
		if err := rows.Scan(&b.ID, &b.MovieID, &b.Title, &b.Indexer, &b.Reason, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// removeBlock un-blocklists an entry.
func (c *Coordinator) removeBlock(ctx context.Context, id int64) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM blocklist WHERE id = ?`, id)
	return err
}

// blockedSet returns the normalized titles blocklisted for a movie.
func (c *Coordinator) blockedSet(ctx context.Context, movieID int64) map[string]bool {
	return c.blockedSetOf(ctx, movieID, "movie")
}

// blockedSetSeries returns the normalized titles blocklisted for a series.
func (c *Coordinator) blockedSetSeries(ctx context.Context, seriesID int64) map[string]bool {
	return c.blockedSetOf(ctx, seriesID, "series")
}

func (c *Coordinator) blockedSetOf(ctx context.Context, id int64, mediaType string) map[string]bool {
	set := map[string]bool{}
	rows, err := c.db.QueryContext(ctx, `SELECT norm_title FROM blocklist WHERE movie_id = ? AND media_type = ?`, id, mediaType)
	if err != nil {
		return set
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		if rows.Scan(&t) == nil {
			set[t] = true
		}
	}
	return set
}

// addBlockSeries blocklists a release for a series (stall fail-over), so a re-search
// won't pick the same stalled release again.
func (c *Coordinator) addBlockSeries(ctx context.Context, seriesID int64, title, indexer, reason string) {
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO blocklist (movie_id, norm_title, title, indexer, download_url, reason, media_type)
		 VALUES (?, ?, ?, ?, '', ?, 'series')`,
		seriesID, normTitle(title), title, indexer, reason)
	if err != nil {
		c.log.Warn("series: blocklist failed", "err", err)
	}
}

// addBlockBook blocklists a release for a book (stall fail-over).
func (c *Coordinator) addBlockBook(ctx context.Context, bookID int64, title, indexer, reason string) {
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO blocklist (movie_id, norm_title, title, indexer, download_url, reason, media_type)
		 VALUES (?, ?, ?, ?, '', ?, 'book')`,
		bookID, normTitle(title), title, indexer, reason)
	if err != nil {
		c.log.Warn("book: blocklist failed", "err", err)
	}
}

// blockedSetBook returns the normalized titles blocklisted for a book.
func (c *Coordinator) blockedSetBook(ctx context.Context, bookID int64) map[string]bool {
	return c.blockedSetOf(ctx, bookID, "book")
}

// recordGrab logs an automatic grab for stall tracking + seed cleanup, capturing
// the originating indexer's seed policy so cleanup survives the indexer being
// removed or renamed later.
func (c *Coordinator) recordGrab(ctx context.Context, movieID, versionID int64, title, indexer, profile string, stallMinutes int) {
	seedEnabled, seedRatio, seedHours := c.seedRules(ctx, indexer)
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO grabs (movie_id, version_id, title, indexer, quality_profile, stall_minutes, seed_enabled, seed_ratio, seed_hours)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		movieID, versionID, title, indexer, profile, stallMinutes, boolToInt(seedEnabled), seedRatio, seedHours)
	if err != nil {
		c.log.Warn("automation: record grab failed", "err", err)
	}
}

// seedRules returns the seed policy of the named indexer (defaults to "don't
// seed" if the indexer can't be found, so an unknown source isn't kept forever).
func (c *Coordinator) seedRules(ctx context.Context, name string) (enabled bool, ratio float64, hours int) {
	idxs, err := c.indexers.List(ctx)
	if err != nil {
		return false, 0, 0
	}
	for _, ix := range idxs {
		if ix.Name == name {
			return ix.SeedEnabled, ix.SeedRatio, ix.SeedHours
		}
	}
	return false, 0, 0
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// pendingGrabs returns grabs still awaiting import.
func (c *Coordinator) pendingGrabs(ctx context.Context) ([]grab, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, movie_id, version_id, title, indexer, quality_profile, stall_minutes, grabbed_at, seed_enabled, seed_ratio, seed_hours, media_type
		 FROM grabs WHERE status = 'grabbed' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []grab
	for rows.Next() {
		g, err := scanGrab(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// pendingGrabTitles returns the normalized titles of a movie's still-pending grabs (downloaded /
// in-flight but not yet imported or failed). Used to stop the same release being grabbed again on
// the next sweep — a belt-and-suspenders guard against re-grab loops when the in-client name-match
// (inQueue) can't recognize a download.
func (c *Coordinator) pendingGrabTitles(ctx context.Context, movieID int64) map[string]bool {
	rows, err := c.db.QueryContext(ctx,
		`SELECT title FROM grabs WHERE movie_id = ? AND status = 'grabbed' AND media_type = 'movie'`, movieID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var title string
		if rows.Scan(&title) == nil {
			set[normTitle(title)] = true
		}
	}
	return set
}

// setGrabStatus marks a grab imported or failed.
func (c *Coordinator) setGrabStatus(ctx context.Context, id int64, status string) {
	_, _ = c.db.ExecContext(ctx, `UPDATE grabs SET status = ? WHERE id = ?`, status, id)
}

// importedGrabs returns grabs whose file has imported (candidates for seed cleanup).
func (c *Coordinator) importedGrabs(ctx context.Context) ([]grab, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, movie_id, version_id, title, indexer, quality_profile, stall_minutes, grabbed_at, seed_enabled, seed_ratio, seed_hours, media_type
		 FROM grabs WHERE status = 'imported' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []grab
	for rows.Next() {
		g, err := scanGrab(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// scanGrab reads a grab row (columns in the order the queries select them).
func scanGrab(row interface{ Scan(...any) error }) (grab, error) {
	var g grab
	var seedEnabled int
	err := row.Scan(&g.ID, &g.MovieID, &g.VersionID, &g.Title, &g.Indexer, &g.Profile,
		&g.StallMinutes, &g.GrabbedAt, &seedEnabled, &g.SeedRatio, &g.SeedHours, &g.MediaType)
	g.SeedEnabled = seedEnabled != 0
	return g, err
}

// markGrabsImportedForMovie flips a movie's pending grabs to imported (called
// when a version gains a file).
func (c *Coordinator) markGrabsImportedForMovie(ctx context.Context, movieID int64) {
	_, _ = c.db.ExecContext(ctx, `UPDATE grabs SET status = 'imported' WHERE movie_id = ? AND status = 'grabbed' AND media_type = 'movie'`, movieID)
}

// markSeriesGrabImported flips the ONE grab this download came from to imported.
//
// It used to flip EVERY pending grab for the series, which marked sibling torrents as
// imported before their data had landed. Seed cleanup only considers imported grabs, so
// it would then remove those torrents WITH their data (the default seed policy deletes
// data), the episodes stayed missing, and the next sweep re-grabbed them — a real
// grab → delete → re-grab loop.
func (c *Coordinator) markSeriesGrabImported(ctx context.Context, seriesID int64, releaseName string) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, title FROM grabs WHERE movie_id = ? AND status = 'grabbed' AND media_type = 'series'`, seriesID)
	if err != nil {
		return
	}
	want := normRelease(releaseName)
	var ids []int64
	for rows.Next() {
		var id int64
		var title string
		if rows.Scan(&id, &title) == nil && normRelease(title) == want {
			ids = append(ids, id)
		}
	}
	rows.Close() // close before writing — SQLite won't take a write while a read is open
	for _, id := range ids {
		if _, err := c.db.ExecContext(ctx, `UPDATE grabs SET status = 'imported' WHERE id = ?`, id); err != nil {
			c.log.Warn("series: mark grab imported failed", "err", err)
		}
	}
}

// normTitle normalizes a release title for blocklist/matching comparisons. Accents
// are folded first so "Pokémon" and "Pokemon" compare equal.
func normTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(parser.FoldAccents(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var _ = sql.ErrNoRows
