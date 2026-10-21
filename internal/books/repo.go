// Package books implements the Books module: Open Library metadata, an ebook library,
// and acquisition through the shared indexer/download/import platform.
package books

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// ErrNotFound is returned when a book id doesn't exist.
var ErrNotFound = errors.New("book not found")

// ErrExists is returned when an Open Library work is already in the library.
var ErrExists = errors.New("book already in library")

// Edition kinds.
const (
	KindEbook     = "ebook"
	KindAudiobook = "audiobook"
)

// BookFile is one edition's file(s) on disk. Path points at a single file, or a folder
// when FileCount > 1 (a multi-file audiobook).
type BookFile struct {
	Path      string `json:"path"`
	Format    string `json:"format"`
	SizeBytes int64  `json:"size_bytes"`
	FileCount int    `json:"file_count"`
	// Kindle: Send to Kindle's email takes this ebook's format (KindleAccepts). Set only
	// where a page offers the send (the staff book page); never stored.
	Kindle bool `json:"kindle,omitempty"`
	// Kepub: the page may offer "Download for Kobo (KEPUB)" — an EPUB, with the conversion
	// on. Set only where a page offers it; never stored.
	Kepub bool `json:"kepub,omitempty"`
}

// Book is one book in the library. It can carry an ebook edition, an audiobook
// edition, or both.
type Book struct {
	ID             int64     `json:"id"`
	OLKey          string    `json:"ol_key"`
	Title          string    `json:"title"`
	Author         string    `json:"author"`
	AuthorID       int64     `json:"author_id,omitempty"` // the author row Author means; 0 = not linked (none, or ambiguous)
	Year           int       `json:"year"`
	CoverURL       string    `json:"cover_url,omitempty"`
	Description    string    `json:"description,omitempty"`
	Subjects       []string  `json:"subjects,omitempty"`
	Monitored      bool      `json:"monitored"`
	QualityProfile string    `json:"quality_profile"`
	Ebook          *BookFile `json:"ebook,omitempty"`
	Audiobook      *BookFile `json:"audiobook,omitempty"`
	HasFile        bool      `json:"has_file"` // has ebook OR audiobook
	// WantEbook/WantAudiobook are derived from the quality profile and filled by the
	// HTTP layer (not stored) so the detail page can show wanted-but-missing editions.
	WantEbook     bool   `json:"want_ebook"`
	WantAudiobook bool   `json:"want_audiobook"`
	AddedAt       string `json:"added_at,omitempty"`
	// SeriesName / SeriesPosition are learned from the release that matched this book —
	// book trackers state them ("Coven of Bones #1") and nothing used to keep them.
	// Position 0 means the series is known but the number isn't.
	SeriesName     string  `json:"series_name,omitempty"`
	SeriesPosition float64 `json:"series_position,omitempty"`
	SeriesKey      string  `json:"series_key,omitempty"` // catalogue's series id, when it gave one
	// KeepCatalogue: the user told the Hardcover re-match to leave this book on the
	// catalogue it's on. Not counted as "still to re-match", not retried.
	KeepCatalogue bool `json:"keep_catalogue,omitempty"`
	// AudioVersions are extra audiobooks of this book beyond the standard one (the
	// Audiobook field): a full-cast production, another narrator. Filled by the service.
	AudioVersions []AudioVersion `json:"audio_versions,omitempty"`
	// Catalogue is where the metadata came from, with a link to the book there. Computed
	// by the HTTP layer for the detail page (not stored); nil when the key is from no
	// catalogue Arrmada knows.
	Catalogue *CatalogueRef `json:"catalogue,omitempty"`
	// Aliases are the other catalogue keys the book has had (book_keys, without the
	// current one), filled by the HTTP layer for the detail page.
	Aliases []BookKey `json:"aliases,omitempty"`
	// LastSearchAt / SearchMisses are where the book stands on the search ladder: when the
	// sweep last searched it (RFC3339, UTC; "" = never) and how many searches in a row
	// found nothing. Without them a book not found in a month looked exactly like one
	// added a minute ago.
	LastSearchAt string `json:"last_search_at,omitempty"`
	SearchMisses int    `json:"search_misses"`
	// NextSearchAt is when the sweep will look again (RFC3339, UTC), computed by
	// FillNextSearch once the wanted editions are known. Empty when nothing is being
	// searched for (unmonitored, or every wanted edition is here) or the book is due now.
	NextSearchAt string `json:"next_search_at,omitempty"`
	// ReleaseDate is the day the book comes out (YYYY-MM-DD, from the catalogue; "" =
	// not known). The sweep doesn't search an upcoming book before it, searches daily for
	// two weeks after it, and its request reads 'Out <date>' (ladder.go).
	ReleaseDate string `json:"release_date,omitempty"`
	// MetaUpdatedAt is when the book's details last changed (unix ms; 0 = never since
	// it was added). SQLite triggers keep it current (migrations 0262_books_meta_updated
	// and 0263_audio_narrator), not the Go code; TouchMeta covers changes that aren't a column, like a cover file.
	// Listening apps read it as the item's updatedAt.
	MetaUpdatedAt int64 `json:"-"`
	// AudiobookNarrator is who reads the standard audiobook, typed by hand. Empty means
	// the audio files' own tags say (the audiobook server reads them).
	AudiobookNarrator string `json:"audiobook_narrator,omitempty"`
	// Subtitle, Language (a canonical internal/langs code), ISBN (bare digits) and
	// Publisher: details only a hand edit fills for now (migration 0334_book_edits).
	Subtitle  string `json:"subtitle,omitempty"`
	Language  string `json:"language,omitempty"`
	ISBN      string `json:"isbn,omitempty"`
	Publisher string `json:"publisher,omitempty"`
	// Edits are the fields edited by hand, each with the catalogue's value underneath
	// (edits.go). The fields above already hold the edits: they're what's shown.
	Edits Edits `json:"edited,omitempty"`
}

// LacksWanted reports whether the book is missing an edition its profile wants (per
// WantEbook/WantAudiobook, filled from the profile) or a monitored audio version — the
// same things the sweep searches for.
func (b Book) LacksWanted() bool {
	if (b.WantEbook && b.Ebook == nil) || (b.WantAudiobook && b.Audiobook == nil) {
		return true
	}
	for _, v := range b.AudioVersions {
		if v.Monitored && v.File == nil && len(v.Terms) > 0 {
			return true
		}
	}
	return false
}

// FillNextSearch sets NextSearchAt from the ladder. Call after the wanted-edition flags
// are filled.
func (b *Book) FillNextSearch() {
	b.NextSearchAt = ""
	if !b.Monitored || !b.LacksWanted() {
		return
	}
	if next := NextSearchAtFor(b.LastSearchAt, b.SearchMisses, b.ReleaseDate, time.Now()); !next.IsZero() {
		b.NextSearchAt = next.UTC().Format(time.RFC3339)
	}
}

// SearchState returns when the missing-books sweep last searched for this book and how
// many consecutive times it found nothing. Drives the same backoff movies and series use.
func (r *Repo) SearchState(ctx context.Context, bookID int64) (lastSearchAt string, misses int) {
	var last sql.NullString
	_ = r.db.QueryRowContext(ctx,
		`SELECT last_search_at, search_misses FROM books WHERE id = ?`, bookID).Scan(&last, &misses)
	return last.String, misses
}

// RecordSearchMiss stamps the sweep time and increments the miss counter.
func (r *Repo) RecordSearchMiss(ctx context.Context, bookID int64) {
	_, _ = r.db.ExecContext(ctx,
		`UPDATE books SET last_search_at = CURRENT_TIMESTAMP, search_misses = search_misses + 1 WHERE id = ?`, bookID)
}

// ResetSearchMisses clears the backoff after a successful grab.
func (r *Repo) ResetSearchMisses(ctx context.Context, bookID int64) {
	_, _ = r.db.ExecContext(ctx,
		`UPDATE books SET last_search_at = CURRENT_TIMESTAMP, search_misses = 0 WHERE id = ?`, bookID)
}

// SetSeries records the series a book belongs to and its position in it. Learned from the
// release that matched the book, so it only ever fills in — a later release naming no
// series can't erase what an earlier one told us.
func (r *Repo) SetSeries(ctx context.Context, bookID int64, name string, position float64) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	// A series edited by hand keeps the edit; what the release said goes underneath.
	return r.SetMatched(ctx, bookID, map[string]any{FieldSeriesName: name, FieldSeriesPosition: position})
}

// FillSeries records a series only for a book that has none, read and written in one
// transaction, so a series someone set while a scan was running is never replaced by what
// the scan read. "Has none" is about the catalogue's value: a series edited by hand
// (even one cleared on purpose) keeps its edit, and the fill goes underneath it only when
// nothing is there yet (fillMatched). It reports whether the book took it.
func (r *Repo) FillSeries(ctx context.Context, bookID int64, name string, position float64) (bool, error) {
	if strings.TrimSpace(name) == "" {
		return false, nil
	}
	return r.fillMatched(ctx, bookID, FieldSeriesName, map[string]any{FieldSeriesName: name, FieldSeriesPosition: position})
}

// FillISBN records an ISBN (already checked) only for a book whose catalogue value has
// none, like FillSeries: an ISBN edited by hand stays, with the fill underneath.
func (r *Repo) FillISBN(ctx context.Context, bookID int64, isbn string) (bool, error) {
	if isbn == "" {
		return false, nil
	}
	return r.fillMatched(ctx, bookID, FieldISBN, map[string]any{FieldISBN: isbn})
}

// fillMatched writes catalogue values through setMatched when the catalogue's value of
// field — what's underneath an edit, else the column — is empty, in one transaction.
func (r *Repo) fillMatched(ctx context.Context, bookID int64, field string, vals map[string]any) (bool, error) {
	took := false
	err := store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		b, err := getTx(ctx, tx, bookID)
		if err != nil {
			return err
		}
		f, _ := fieldByName(field)
		var cur string
		if raw, edited := b.Edits[field]; edited {
			_ = json.Unmarshal(raw, &cur)
		} else {
			_ = json.Unmarshal(f.get(&b), &cur)
		}
		if strings.TrimSpace(cur) != "" {
			return nil
		}
		took = true
		return setMatched(ctx, tx, bookID, vals)
	})
	return took, err
}

// SeriesSiblings returns every book sharing this series name, in reading order, including
// the book itself. Position 0 means "no number known" and sorts last, after the numbered
// entries, rather than pretending to be book zero.
func (r *Repo) SeriesSiblings(ctx context.Context, seriesName string) ([]Book, error) {
	if strings.TrimSpace(seriesName) == "" {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+cols+` FROM books WHERE series_name = ?
		  ORDER BY CASE WHEN series_position > 0 THEN 0 ELSE 1 END, series_position, title`, seriesName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Book
	for rows.Next() {
		b, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Repo persists books in SQLite.
type Repo struct{ db store.Pool }

// NewRepo builds a repository over the given pool.
func NewRepo(db store.Pool) *Repo { return &Repo{db: db} }

const cols = `id, ol_key, title, author, year, cover_url, description, subjects_json,
	monitored, quality_profile, added_at, series_name, series_position, series_key, keep_catalogue,
	ebook_path, ebook_format, ebook_size, ebook_files,
	audiobook_path, audiobook_format, audiobook_size, audiobook_files,
	last_search_at, search_misses, release_date, author_id,
	meta_updated_at, audiobook_narrator,
	subtitle, language, isbn, publisher, edits_json`

func scan(row interface{ Scan(...any) error }) (Book, error) {
	var (
		b             Book
		subjectsJSON  string
		mon, keep     int
		ebPath, ebFmt string
		ebSize        int64
		ebFiles       int
		abPath, abFmt string
		abSize        int64
		abFiles       int
		lastSearch    sql.NullString
		authorID      sql.NullInt64
		editsJSON     string
	)
	err := row.Scan(&b.ID, &b.OLKey, &b.Title, &b.Author, &b.Year, &b.CoverURL, &b.Description,
		&subjectsJSON, &mon, &b.QualityProfile, &b.AddedAt, &b.SeriesName, &b.SeriesPosition, &b.SeriesKey, &keep,
		&ebPath, &ebFmt, &ebSize, &ebFiles, &abPath, &abFmt, &abSize, &abFiles,
		&lastSearch, &b.SearchMisses, &b.ReleaseDate, &authorID,
		&b.MetaUpdatedAt, &b.AudiobookNarrator,
		&b.Subtitle, &b.Language, &b.ISBN, &b.Publisher, &editsJSON)
	if err != nil {
		return Book{}, err
	}
	b.Edits = decodeEdits(editsJSON)
	b.AuthorID = authorID.Int64
	if t := parseStamp(lastSearch.String); !t.IsZero() {
		b.LastSearchAt = t.Format(time.RFC3339)
	}
	b.Monitored = mon != 0
	b.KeepCatalogue = keep != 0
	if subjectsJSON != "" {
		_ = json.Unmarshal([]byte(subjectsJSON), &b.Subjects)
	}
	if ebPath != "" {
		b.Ebook = &BookFile{Path: ebPath, Format: ebFmt, SizeBytes: ebSize, FileCount: max1(ebFiles)}
	}
	if abPath != "" {
		b.Audiobook = &BookFile{Path: abPath, Format: abFmt, SizeBytes: abSize, FileCount: max1(abFiles)}
	}
	b.HasFile = b.Ebook != nil || b.Audiobook != nil
	return b, nil
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// List returns all books, newest first.
func (r *Repo) List(ctx context.Context) ([]Book, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+cols+` FROM books ORDER BY added_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Book
	for rows.Next() {
		b, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ByKeysOrIDs returns the books under any of these catalogue keys — current, or one the
// book had before (book_keys) — or with any of these ids, in one query: a page of book
// requests, which each name a key and maybe a row.
func (r *Repo) ByKeysOrIDs(ctx context.Context, olKeys []string, ids []int64) ([]Book, error) {
	var where []string
	var args []any
	if len(olKeys) > 0 {
		in := `(` + strings.TrimSuffix(strings.Repeat("?,", len(olKeys)), ",") + `)`
		where = append(where, `ol_key IN `+in, `id IN (SELECT book_id FROM book_keys WHERE key IN `+in+`)`)
		for range 2 {
			for _, k := range olKeys {
				args = append(args, k)
			}
		}
	}
	if len(ids) > 0 {
		where = append(where, `id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`)
		for _, id := range ids {
			args = append(args, id)
		}
	}
	if len(where) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+cols+` FROM books WHERE `+strings.Join(where, " OR "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Book
	for rows.Next() {
		b, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Get returns one book by id.
func (r *Repo) Get(ctx context.Context, id int64) (Book, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+cols+` FROM books WHERE id = ?`, id)
	b, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Book{}, ErrNotFound
	}
	return b, err
}

// Create inserts a book row.
func (r *Repo) Create(ctx context.Context, b Book) (Book, error) {
	subjectsJSON := ""
	if len(b.Subjects) > 0 {
		if raw, err := json.Marshal(b.Subjects); err == nil {
			subjectsJSON = string(raw)
		}
	}
	// The row and its key land together, and a key that is another book's former key
	// is that book: adding it again would make two rows of one book.
	var id int64
	err := store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		if _, taken := keyOwner(ctx, tx, b.OLKey); taken {
			return ErrExists
		}
		var err error
		id, err = store.InsertID(ctx, tx,
			`INSERT INTO books (ol_key, title, author, year, cover_url, description, subjects_json, monitored, quality_profile, release_date)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			b.OLKey, b.Title, b.Author, b.Year, b.CoverURL, b.Description, subjectsJSON, b2i(b.Monitored), b.QualityProfile, cleanReleaseDate(b.ReleaseDate))
		if err != nil {
			return err
		}
		return addKey(ctx, tx, b.OLKey, id, "")
	})
	if err != nil {
		if errors.Is(err, ErrExists) || store.IsUniqueViolation(err) {
			return Book{}, ErrExists
		}
		return Book{}, err
	}
	return r.Get(ctx, id)
}

// SetMonitored toggles a book.
func (r *Repo) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	res, err := r.db.ExecContext(ctx, `UPDATE books SET monitored = ? WHERE id = ?`, b2i(monitored), id)
	return affected(res, err)
}

// SetKeepCatalogue marks a book as left alone by the Hardcover re-match (or not).
func (r *Repo) SetKeepCatalogue(ctx context.Context, id int64, keep bool) error {
	res, err := r.db.ExecContext(ctx, `UPDATE books SET keep_catalogue = ? WHERE id = ?`, b2i(keep), id)
	return affected(res, err)
}

// SetQualityProfile changes a book's quality profile.
func (r *Repo) SetQualityProfile(ctx context.Context, id int64, profile string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE books SET quality_profile = ? WHERE id = ?`, profile, id)
	return affected(res, err)
}

// UpdateMeta refreshes a book's description/cover/subjects (from a metadata re-pull). A
// description or tags edited by hand keep the edit, with the catalogue's underneath.
func (r *Repo) UpdateMeta(ctx context.Context, id int64, description, coverURL string, subjects []string) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		if _, err := tx.ExecContext(ctx, `UPDATE books SET cover_url = ? WHERE id = ?`, coverURL, id); err != nil {
			return err
		}
		return setMatched(ctx, tx, id, map[string]any{FieldDescription: description, FieldSubjects: subjects})
	})
}

// SetCoverURL changes a book's cover image (a cover picked from the picker, or the served
// path of a custom upload).
func (r *Repo) SetCoverURL(ctx context.Context, id int64, coverURL string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE books SET cover_url = ? WHERE id = ?`, coverURL, id)
	return affected(res, err)
}

// FillCoverURL sets a cover only for a book that has none, in one statement, so a cover
// someone picked while a scan was running is never replaced. It reports whether the book
// took it.
func (r *Repo) FillCoverURL(ctx context.Context, id int64, coverURL string) (bool, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE books SET cover_url = ? WHERE id = ? AND (cover_url IS NULL OR cover_url = '')`, coverURL, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// TouchMeta marks a book's details as changed now, for a change no column records (an
// uploaded cover file replaced under the same URL). Column edits are caught by the
// triggers of migration 0262_books_meta_updated; like them, it only ever moves forward.
func (r *Repo) TouchMeta(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE books SET meta_updated_at = `+store.ScalarMax(r.db, "?", "meta_updated_at + 1")+` WHERE id = ?`, time.Now().UnixMilli(), id)
	return affected(res, err)
}

// SetEdition records a book edition's file(s) on disk (kind = "ebook" | "audiobook").
func (r *Repo) SetEdition(ctx context.Context, id int64, kind, path, format string, size int64, files int) error {
	col := "ebook"
	if kind == KindAudiobook {
		col = "audiobook"
	}
	if files < 1 {
		files = 1
	}
	q := `UPDATE books SET ` + col + `_path = ?, ` + col + `_format = ?, ` + col + `_size = ?, ` + col + `_files = ?, has_file = 1 WHERE id = ?`
	res, err := r.db.ExecContext(ctx, q, path, format, size, files, id)
	return affected(res, err)
}

// ClearEdition forgets a book edition's file (path only — file removal is the caller's job).
func (r *Repo) ClearEdition(ctx context.Context, id int64, kind string) error {
	col := "ebook"
	if kind == KindAudiobook {
		col = "audiobook"
	}
	q := `UPDATE books SET ` + col + `_path = '', ` + col + `_format = '', ` + col + `_size = 0 WHERE id = ?`
	if _, err := r.db.ExecContext(ctx, q, id); err != nil {
		return err
	}
	// Recompute has_file from whatever remains.
	_, err := r.db.ExecContext(ctx,
		`UPDATE books SET has_file = (ebook_path != '' OR audiobook_path != '') WHERE id = ?`, id)
	return err
}

// Rematch re-points a book at a different Open Library work and replaces the metadata that
// came from the old one. File/edition state, monitoring and the quality profile are the
// library's and the user's, not the provider's, so they are deliberately left alone — the
// point is to fix a wrong identification without losing the files already on disk.
//
// ol_key is UNIQUE, so re-matching onto a work already in the library returns ErrExists
// rather than silently failing: the two rows would have to be merged, which is the user's
// call, not ours.
//
// The key the book was on stays one of its keys (book_keys), so whatever still carries
// it finds the book — unless keepOld is false: a correction of a wrong identification,
// where the old key named a different book. A key that is another book's, current or
// former, is ErrExists.
//
// Details edited by hand stay edited, with the new work's values underneath — unless
// clearEdits: the person said the edits were for the book it was wrongly matched to.
func (r *Repo) Rematch(ctx context.Context, id int64, b Book, keepOld, clearEdits bool) error {
	err := store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		if owner, taken := keyOwner(ctx, tx, b.OLKey); taken && owner != id {
			return ErrExists
		}
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT ol_key FROM books WHERE id = ?`, id).Scan(&old); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if clearEdits {
			if err := clearEditsTx(ctx, tx, id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE books SET ol_key = ?, cover_url = ?, release_date = ? WHERE id = ?`,
			b.OLKey, b.CoverURL, cleanReleaseDate(b.ReleaseDate), id); err != nil {
			return err
		}
		if err := setMatched(ctx, tx, id, map[string]any{
			FieldTitle: b.Title, FieldAuthor: b.Author, FieldYear: b.Year,
			FieldDescription: b.Description, FieldSubjects: b.Subjects,
		}); err != nil {
			return err
		}
		if old != b.OLKey {
			if keepOld {
				if err := addKey(ctx, tx, old, id, ""); err != nil {
					return err
				}
			} else if err := dropKey(ctx, tx, old, id); err != nil {
				return err
			}
		}
		return addKey(ctx, tx, b.OLKey, id, "")
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrExists) &&
		store.IsUniqueViolation(err) {
		return ErrExists
	}
	return err
}

// Delete removes a book.
func (r *Repo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM books WHERE id = ?`, id)
	return affected(res, err)
}

// Event is one entry in a book's activity timeline.
type Event struct {
	Event     string `json:"event"`
	Detail    string `json:"detail,omitempty"`
	CreatedAt string `json:"created_at"`
}

// AddEvent appends a timeline event for a book (best effort — a history write must never
// fail the operation it is recording).
func (r *Repo) AddEvent(ctx context.Context, bookID int64, event, detail string) {
	_, _ = r.db.ExecContext(ctx,
		`INSERT INTO book_events (book_id, event, detail) VALUES (?, ?, ?)`, bookID, event, detail)
}

// HasEvent reports whether a book's timeline already holds this exact entry, so a
// check that reruns on every boot can say a thing once rather than every time.
func (r *Repo) HasEvent(ctx context.Context, bookID int64, event, detail string) bool {
	var one int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM book_events WHERE book_id = ? AND event = ? AND detail = ? LIMIT 1`,
		bookID, event, detail).Scan(&one)
	return err == nil
}

// Events returns a book's timeline, newest first.
func (r *Repo) Events(ctx context.Context, bookID int64, limit int) ([]Event, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT event, detail, created_at FROM book_events WHERE book_id = ? ORDER BY id DESC LIMIT ?`,
		bookID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Event, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
