package books

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Catalogue keys.
//
// A book's key (Book.OLKey) is the one it is on now. Keys change — a Change match, the
// Hardcover upgrade, a catalogue answering with its canonical id — and every key a book
// has had is kept in book_keys, so a Discover card, a request or a series listing still
// carrying an old key finds the book. A key names one book; deleting the book drops its
// keys with it.

// Key sources: the catalogue that issued a key, or "request" for a key learned from a
// request linked to the book.
const (
	KeySourceOpenLibrary = "openlibrary"
	KeySourceHardcover   = "hardcover"
	KeySourceGoogle      = "google"
	KeySourceRequest     = "request"
)

// BookKey is one catalogue key a book has had.
type BookKey struct {
	Key     string `json:"key"`
	Source  string `json:"source"`
	AddedAt string `json:"added_at,omitempty"`
}

// KeySource names the catalogue a key comes from, by its namespace.
func KeySource(key string) string {
	switch {
	case strings.HasPrefix(key, "hc:"):
		return KeySourceHardcover
	case strings.HasPrefix(key, "gb:"):
		return KeySourceGoogle
	default:
		return KeySourceOpenLibrary
	}
}

// keyOwner returns the book a key belongs to: its alias row, else a book currently on
// that key (a row written before book_keys existed, or by a path that skipped it).
func keyOwner(ctx context.Context, ex store.Execer, key string) (int64, bool) {
	if key == "" {
		return 0, false
	}
	var id int64
	err := ex.QueryRowContext(ctx, `SELECT book_id FROM book_keys WHERE key = ?`, key).Scan(&id)
	if err == nil {
		return id, true
	}
	if err = ex.QueryRowContext(ctx, `SELECT id FROM books WHERE ol_key = ?`, key).Scan(&id); err == nil {
		return id, true
	}
	return 0, false
}

// addKey records key as one of bookID's keys. A key already held by another book is
// left with it and reported as ErrExists; one already this book's is a no-op.
func addKey(ctx context.Context, ex store.Execer, key string, bookID int64, source string) error {
	key = strings.TrimSpace(key)
	if key == "" || bookID <= 0 {
		return nil
	}
	if owner, ok := keyOwner(ctx, ex, key); ok && owner != bookID {
		return ErrExists
	}
	if source == "" {
		source = KeySource(key)
	}
	_, err := ex.ExecContext(ctx,
		`INSERT OR IGNORE INTO book_keys (key, book_id, source) VALUES (?, ?, ?)`, key, bookID, source)
	return err
}

// AddKey records key as one of a book's keys. ErrExists means another book holds it.
func (r *Repo) AddKey(ctx context.Context, key string, bookID int64, source string) error {
	return addKey(ctx, r.db, key, bookID, source)
}

// BookIDForKey returns the book a catalogue key belongs to, current or former.
func (r *Repo) BookIDForKey(ctx context.Context, key string) (int64, bool) {
	return keyOwner(ctx, r.db, key)
}

// KeysFor returns every key a book has had, oldest first. The current key is always
// among them.
func (r *Repo) KeysFor(ctx context.Context, bookID int64) ([]BookKey, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT key, source, added_at FROM book_keys WHERE book_id = ? ORDER BY added_at, rowid`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BookKey
	seen := map[string]bool{}
	for rows.Next() {
		var k BookKey
		if err := rows.Scan(&k.Key, &k.Source, &k.AddedAt); err != nil {
			return nil, err
		}
		seen[k.Key] = true
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var cur string
	err = r.db.QueryRowContext(ctx, `SELECT ol_key FROM books WHERE id = ?`, bookID).Scan(&cur)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if cur != "" && !seen[cur] {
		out = append(out, BookKey{Key: cur, Source: KeySource(cur)})
	}
	return out, nil
}

// AllKeys maps every key, current and former, to its book — for a caller matching a
// page of catalogue results against the library in one read.
func (r *Repo) AllKeys(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	rows, err := r.db.QueryContext(ctx, `SELECT key, book_id FROM book_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var id int64
		if err := rows.Scan(&k, &id); err != nil {
			return nil, err
		}
		out[k] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// A book's current key is its own, whatever an older alias row says.
	cur, err := r.db.QueryContext(ctx, `SELECT ol_key, id FROM books WHERE ol_key != ''`)
	if err != nil {
		return nil, err
	}
	defer cur.Close()
	for cur.Next() {
		var k string
		var id int64
		if err := cur.Scan(&k, &id); err != nil {
			return nil, err
		}
		out[k] = id
	}
	return out, cur.Err()
}

// dropKey forgets one of a book's former keys. Only a Change match that corrects a
// wrong identification does this: the old key named a different book.
func dropKey(ctx context.Context, ex store.Execer, key string, bookID int64) error {
	_, err := ex.ExecContext(ctx, `DELETE FROM book_keys WHERE key = ? AND book_id = ?`, key, bookID)
	return err
}

// --- service surface ---

// AddKey records key as one of a book's keys (best effort for callers that only learn
// it in passing). ErrExists means another book holds it.
func (s *Service) AddKey(ctx context.Context, key string, bookID int64, source string) error {
	return s.repo.AddKey(ctx, key, bookID, source)
}

// BookIDForKey returns the book a catalogue key belongs to, current or former.
func (s *Service) BookIDForKey(ctx context.Context, key string) (int64, bool) {
	return s.repo.BookIDForKey(ctx, key)
}

// KeysFor returns every key a book has had.
func (s *Service) KeysFor(ctx context.Context, bookID int64) ([]BookKey, error) {
	return s.repo.KeysFor(ctx, bookID)
}

// AllKeys maps every key, current and former, to its book.
func (s *Service) AllKeys(ctx context.Context) (map[string]int64, error) {
	return s.repo.AllKeys(ctx)
}

// keyIndex maps keys to library rows for a caller holding a library list: every key the
// AllKeys read returned, plus each row's current key when that read failed.
func keyIndex(ctx context.Context, r *Repo, list []Book) map[string]Book {
	byID := make(map[int64]Book, len(list))
	out := make(map[string]Book, len(list))
	for _, b := range list {
		byID[b.ID] = b
		out[b.OLKey] = b
	}
	if all, err := r.AllKeys(ctx); err == nil {
		for k, id := range all {
			if b, ok := byID[id]; ok {
				out[k] = b
			}
		}
	}
	return out
}

// KeyIndex is keyIndex for other packages holding a library list.
func (s *Service) KeyIndex(ctx context.Context, list []Book) map[string]Book {
	return keyIndex(ctx, s.repo, list)
}
