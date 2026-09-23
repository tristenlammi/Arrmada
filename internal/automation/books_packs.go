package automation

import (
	"context"
	"fmt"
	"strings"

	"github.com/tristenlammi/arrmada/internal/books"
)

// finishForeignOnlyDownload closes out a download grabbed for b whose files all turned
// out to belong to other library books (and were filed there). "George R R Martin — Fire
// & Blood (HBO Tie-in Edition)" held only A Game of Thrones: the import filed it, found
// nothing for Fire & Blood, and reported the download as holding no book at all — so it
// was never marked done and A Game of Thrones was re-filed every 30 seconds.
//
// Now: the grab is marked imported (seeding rules apply, the pending-grab guard lets go),
// the release is blocklisted for b so the next search doesn't fetch the same mislabelled
// torrent, and b's history says what happened.
func (c *Coordinator) finishForeignOnlyDownload(ctx context.Context, b books.Book, hash, name string, diverted []string) {
	title, indexerName := name, ""
	if hash != "" {
		var t, ix string
		if err := c.db.QueryRowContext(ctx,
			`SELECT title, indexer FROM grabs WHERE media_type = 'book' AND movie_id = ? AND lower(info_hash) = ? ORDER BY id DESC LIMIT 1`,
			b.ID, strings.ToLower(hash)).Scan(&t, &ix); err == nil && t != "" {
			title, indexerName = t, ix
		}
	}
	others := strings.Join(diverted, ", ")
	c.log.Info("book import: download held only other books — filed them, blocklisted the release for this book",
		"book", b.Title, "release", name, "filed_into", others)
	c.addBlockBook(ctx, b.ID, title, indexerName, "held only "+others)
	c.markBookGrabImported(ctx, b.ID, hash, name)
	c.books.AddEvent(ctx, b.ID, "failed",
		fmt.Sprintf("%s held no copy of this book, only %s (filed there) — blocklisted for this book", name, others))
}

// regrabbedForVersion reports whether an already-imported torrent has been grabbed
// again for an audiobook version that still has no file. Grabbing the same release a
// second time "as Narrator" hands qBittorrent a torrent it already has; the import
// used to skip it for having been imported once already, so the version never filled.
func (c *Coordinator) regrabbedForVersion(ctx context.Context, hash string) bool {
	if hash == "" || c.books == nil {
		return false
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT movie_id, version_id FROM grabs
		 WHERE media_type = 'book' AND status = 'grabbed' AND version_id > 0 AND lower(info_hash) = ?`,
		strings.ToLower(hash))
	if err != nil {
		return false
	}
	type ref struct{ book, version int64 }
	var refs []ref
	for rows.Next() {
		var r ref
		if rows.Scan(&r.book, &r.version) == nil {
			refs = append(refs, r)
		}
	}
	rows.Close() // read the versions only once this cursor is closed
	for _, r := range refs {
		if v, err := c.books.GetAudioVersion(ctx, r.book, r.version); err == nil && v.File == nil {
			return true
		}
	}
	return false
}
