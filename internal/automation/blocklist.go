package automation

import (
	"context"
	"errors"
	"strings"

	"github.com/tristenlammi/arrmada/internal/store"
)

// ErrBlockNotFound means the blocklist entry doesn't exist (already unblocked).
var ErrBlockNotFound = errors.New("blocklist entry not found")

// BlockRow is one blocklist entry of any kind, for the Blocklist page.
type BlockRow struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // movie | series | book | music | global
	// ItemID is the library item it blocks the release for (0 for global). For a music
	// discography it is the artist, and Discography says so.
	ItemID      int64  `json:"item_id"`
	ItemTitle   string `json:"item_title"` // "" when the item has since been deleted
	Discography bool   `json:"discography,omitempty"`
	Title       string `json:"title"` // the release
	Indexer     string `json:"indexer,omitempty"`
	Reason      string `json:"reason,omitempty"`
	CreatedAt   string `json:"created_at"`
	// Network is "usenet" for a release grabbed from Usenet, absent otherwise.
	Network string `json:"network,omitempty"`
}

// BlockFilter narrows ListAllBlocks: Type is one of BlockRow's types ("" = all), Q matches
// the release or the item's title.
type BlockFilter struct {
	Type   string
	Q      string
	Limit  int
	Offset int
}

// blockTypes are the blocklist's media_type values.
var blockTypes = map[string]bool{"movie": true, "series": true, "book": true, "music": true, "global": true}

// ValidBlockType reports whether t is a blocklist type ("" means all).
func ValidBlockType(t string) bool { return t == "" || blockTypes[t] }

// maxBlockPage bounds one page of the Blocklist.
const maxBlockPage = 200

// ListAllBlocks returns blocklist entries of every kind, newest first, with the title of
// what each blocks the release for, and how many match in all.
//
// Only movie and series rows had a page before, so a book's or an album's block — and the
// 'global' rows Reject writes for a release tied to nothing, which block it for every
// title — could be neither seen nor undone.
func (c *Coordinator) ListAllBlocks(ctx context.Context, f BlockFilter) ([]BlockRow, int, error) {
	if f.Limit <= 0 || f.Limit > maxBlockPage {
		f.Limit = maxBlockPage
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	// Each kind's title comes from its own table (a film's or show's shown title): ids are
	// only unique per table, so the join must be on the row's own kind.
	const item = `CASE b.media_type
			WHEN 'movie' THEN COALESCE(NULLIF(m.display_title, ''), m.title, '')
			WHEN 'series' THEN COALESCE(NULLIF(s.display_title, ''), s.title, '')
			WHEN 'book' THEN COALESCE(bk.title, '')
			WHEN 'music' THEN COALESCE(al.title, '')
			WHEN '` + mediaMusicArtist + `' THEN COALESCE(ar.name, '')
			ELSE '' END`
	// A film's or show's own (TMDB English) title, which a search also finds it by when
	// it's shown under another name.
	const canonical = `CASE b.media_type
			WHEN 'movie' THEN COALESCE(m.title, '')
			WHEN 'series' THEN COALESCE(s.title, '')
			ELSE '' END`
	const from = ` FROM blocklist b
		LEFT JOIN movies m ON b.media_type = 'movie' AND m.id = b.movie_id
		LEFT JOIN series s ON b.media_type = 'series' AND s.id = b.movie_id
		LEFT JOIN books bk ON b.media_type = 'book' AND bk.id = b.movie_id
		LEFT JOIN albums al ON b.media_type = 'music' AND al.id = b.movie_id
		LEFT JOIN artists ar ON b.media_type = '` + mediaMusicArtist + `' AND ar.id = b.movie_id
		WHERE 1 = 1`
	where, args := "", []any{}
	switch f.Type {
	case "":
	case "music":
		// A discography's block is music too; it's kept against the artist (music_artist).
		where += ` AND b.media_type IN ('music', '` + mediaMusicArtist + `')`
	default:
		where += ` AND b.media_type = ?`
		args = append(args, f.Type)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		like := "%" + escapeLike(strings.ToLower(q)) + "%"
		where += ` AND (` + store.LikeASCII(c.db, "lower(b.title)", "?", true) + ` OR ` + store.LikeASCII(c.db, "lower("+item+")", "?", true) +
			` OR ` + store.LikeASCII(c.db, "lower("+canonical+")", "?", true) + `)`
		args = append(args, like, like, like)
	}
	var total int
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*)`+from+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT b.id, b.media_type, b.movie_id, `+item+`, b.title, b.indexer, b.reason, b.created_at`+from+where+
			` ORDER BY b.id DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	out := []BlockRow{}
	for rows.Next() {
		var r BlockRow
		if err := rows.Scan(&r.ID, &r.Type, &r.ItemID, &r.ItemTitle, &r.Title, &r.Indexer, &r.Reason, &r.CreatedAt); err != nil {
			rows.Close()
			return nil, 0, err
		}
		if r.Type == "global" {
			r.ItemID, r.ItemTitle = 0, ""
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close() // closed before the artist lookups below
	if err != nil {
		return nil, 0, err
	}
	c.markUsenetBlocks(ctx, len(out), func(i int) string { return out[i].Title }, func(i int, n string) { out[i].Network = n })
	// A discography is blocked against its artist (media_type music_artist, see
	// GrabDiscography). It reads as a music block naming the artist.
	for i := range out {
		if out[i].Type == mediaMusicArtist {
			out[i].Type, out[i].Discography = "music", true
		}
	}
	return out, total, nil
}

// escapeLike makes q match literally inside a LIKE pattern (ESCAPE '\'), so a search for
// "100%" or "a_b" isn't a wildcard.
func escapeLike(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}

// RemoveBlockEntry un-blocklists one entry of any kind. ErrBlockNotFound when it's gone.
func (c *Coordinator) RemoveBlockEntry(ctx context.Context, id int64) error {
	res, err := c.db.ExecContext(ctx, `DELETE FROM blocklist WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBlockNotFound
	}
	return nil
}
