// Package books implements the Books module: Open Library metadata, an ebook library,
// and acquisition through the shared indexer/download/import platform.
package books

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
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
}

// Book is one book in the library. It can carry an ebook edition, an audiobook
// edition, or both.
type Book struct {
	ID             int64     `json:"id"`
	OLKey          string    `json:"ol_key"`
	Title          string    `json:"title"`
	Author         string    `json:"author"`
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
}

// Repo persists books in SQLite.
type Repo struct{ db *sql.DB }

// NewRepo builds a repository over the given pool.
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

const cols = `id, ol_key, title, author, year, cover_url, description, subjects_json,
	monitored, quality_profile, added_at,
	ebook_path, ebook_format, ebook_size, ebook_files,
	audiobook_path, audiobook_format, audiobook_size, audiobook_files`

func scan(row interface{ Scan(...any) error }) (Book, error) {
	var (
		b             Book
		subjectsJSON  string
		mon           int
		ebPath, ebFmt string
		ebSize        int64
		ebFiles       int
		abPath, abFmt string
		abSize        int64
		abFiles       int
	)
	err := row.Scan(&b.ID, &b.OLKey, &b.Title, &b.Author, &b.Year, &b.CoverURL, &b.Description,
		&subjectsJSON, &mon, &b.QualityProfile, &b.AddedAt,
		&ebPath, &ebFmt, &ebSize, &ebFiles, &abPath, &abFmt, &abSize, &abFiles)
	if err != nil {
		return Book{}, err
	}
	b.Monitored = mon != 0
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
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO books (ol_key, title, author, year, cover_url, description, subjects_json, monitored, quality_profile)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.OLKey, b.Title, b.Author, b.Year, b.CoverURL, b.Description, subjectsJSON, b2i(b.Monitored), b.QualityProfile)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Book{}, ErrExists
		}
		return Book{}, err
	}
	id, _ := res.LastInsertId()
	return r.Get(ctx, id)
}

// SetMonitored toggles a book.
func (r *Repo) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	res, err := r.db.ExecContext(ctx, `UPDATE books SET monitored = ? WHERE id = ?`, b2i(monitored), id)
	return affected(res, err)
}

// SetQualityProfile changes a book's quality profile.
func (r *Repo) SetQualityProfile(ctx context.Context, id int64, profile string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE books SET quality_profile = ? WHERE id = ?`, profile, id)
	return affected(res, err)
}

// UpdateMeta refreshes a book's description/cover/subjects (from a metadata re-pull).
func (r *Repo) UpdateMeta(ctx context.Context, id int64, description, coverURL string, subjects []string) error {
	subjectsJSON := ""
	if len(subjects) > 0 {
		if raw, err := json.Marshal(subjects); err == nil {
			subjectsJSON = string(raw)
		}
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE books SET description = ?, cover_url = ?, subjects_json = ? WHERE id = ?`,
		description, coverURL, subjectsJSON, id)
	return err
}

// SetCoverURL changes a book's cover image (a cover picked from the picker, or the served
// path of a custom upload).
func (r *Repo) SetCoverURL(ctx context.Context, id int64, coverURL string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE books SET cover_url = ? WHERE id = ?`, coverURL, id)
	return affected(res, err)
}

// UpdateDetails applies a manual metadata override (title / author / year / description / cover)
// for when the providers get a book wrong.
func (r *Repo) UpdateDetails(ctx context.Context, id int64, title, author string, year int, description, coverURL string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE books SET title = ?, author = ?, year = ?, description = ?, cover_url = ? WHERE id = ?`,
		title, author, year, description, coverURL, id)
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

// Delete removes a book.
func (r *Repo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM books WHERE id = ?`, id)
	return affected(res, err)
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
