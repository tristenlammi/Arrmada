package books

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// A book used to hold exactly one audiobook. People who collect audiobooks often want
// two of the same book — the standard narration and a full-cast GraphicAudio
// production — and the second one either never got grabbed (the edition was "done")
// or replaced the first. An AudioVersion is one more audiobook of the book: a label the
// user picks, the words a release must mention to count as it, and its own file. The
// book's own audiobook edition stays the standard version, so everything that only
// knows about one audiobook keeps working unchanged.

// AudioVersion is one extra audiobook version of a book.
type AudioVersion struct {
	ID        int64     `json:"id"`
	BookID    int64     `json:"book_id"`
	Label     string    `json:"label"`
	Terms     []string  `json:"terms"`
	Monitored bool      `json:"monitored"`
	File      *BookFile `json:"file,omitempty"`
	AddedAt   string    `json:"added_at,omitempty"`
}

// ErrVersionNotFound is returned for an unknown audio version id.
var ErrVersionNotFound = errors.New("audio version not found")

// ErrVersionInvalid wraps a validation failure the user can act on.
var ErrVersionInvalid = errors.New("invalid audio version")

const versionCols = `id, book_id, label, terms, monitored, path, format, size, files, added_at`

func scanVersion(row interface{ Scan(...any) error }) (AudioVersion, error) {
	var (
		v            AudioVersion
		terms        string
		mon          int
		path, format string
		size         int64
		files        int
	)
	if err := row.Scan(&v.ID, &v.BookID, &v.Label, &terms, &mon, &path, &format, &size, &files, &v.AddedAt); err != nil {
		return AudioVersion{}, err
	}
	v.Terms = splitTerms(terms)
	v.Monitored = mon != 0
	if path != "" {
		v.File = &BookFile{Path: path, Format: format, SizeBytes: size, FileCount: max1(files)}
	}
	return v, nil
}

// splitTerms turns the stored comma list (or what a user typed) into clean terms.
func splitTerms(s string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range strings.Split(s, ",") {
		t = strings.Join(strings.Fields(t), " ")
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	return out
}

// CleanTerms normalises a list of terms the way they are stored.
func CleanTerms(terms []string) []string { return splitTerms(strings.Join(terms, ",")) }

// matchText lowercases and reduces text to space-separated letters/digits, padded, so
// "Graphic-Audio" and "graphic audio" compare equal and a term only matches whole words.
func matchText(s string) string {
	var b strings.Builder
	b.WriteByte(' ')
	space := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	if !space {
		b.WriteByte(' ')
	}
	return b.String()
}

// Matches reports whether a release's text mentions any of this version's terms. A
// version without terms matches nothing: it is filled by hand.
func (v AudioVersion) Matches(text string) bool {
	if len(v.Terms) == 0 {
		return false
	}
	hay := matchText(text)
	for _, t := range v.Terms {
		if n := matchText(t); strings.TrimSpace(n) != "" && strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

// VersionFor returns the first version whose terms the text mentions, or nil.
func VersionFor(versions []AudioVersion, text string) *AudioVersion {
	for i := range versions {
		if versions[i].Matches(text) {
			return &versions[i]
		}
	}
	return nil
}

// FolderTitle is the title an extra version is filed under — "Title (Label)" — a
// sibling of the standard audiobook's folder, which Audiobookshelf shows as its own book.
func (v AudioVersion) FolderTitle(bookTitle string) string {
	return fmt.Sprintf("%s (%s)", bookTitle, v.Label)
}

// --- repo ------------------------------------------------------------------

// ListAudioVersions returns a book's extra audio versions, oldest first.
func (r *Repo) ListAudioVersions(ctx context.Context, bookID int64) ([]AudioVersion, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+versionCols+` FROM book_audio_versions WHERE book_id = ? ORDER BY id`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AudioVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// AllAudioVersions returns every extra audio version, grouped by book.
func (r *Repo) AllAudioVersions(ctx context.Context) (map[int64][]AudioVersion, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+versionCols+` FROM book_audio_versions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]AudioVersion{}
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out[v.BookID] = append(out[v.BookID], v)
	}
	return out, rows.Err()
}

// GetAudioVersion loads one version, checking it belongs to the book.
func (r *Repo) GetAudioVersion(ctx context.Context, bookID, id int64) (AudioVersion, error) {
	v, err := scanVersion(r.db.QueryRowContext(ctx,
		`SELECT `+versionCols+` FROM book_audio_versions WHERE id = ? AND book_id = ?`, id, bookID))
	if errors.Is(err, sql.ErrNoRows) {
		return AudioVersion{}, ErrVersionNotFound
	}
	return v, err
}

func (r *Repo) createAudioVersion(ctx context.Context, bookID int64, label string, terms []string, monitored bool) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO book_audio_versions (book_id, label, terms, monitored) VALUES (?, ?, ?, ?)`,
		bookID, label, strings.Join(terms, ","), boolInt(monitored))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *Repo) updateAudioVersion(ctx context.Context, bookID, id int64, label string, terms []string, monitored bool) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE book_audio_versions SET label = ?, terms = ?, monitored = ? WHERE id = ? AND book_id = ?`,
		label, strings.Join(terms, ","), boolInt(monitored), id, bookID)
	if err := affected(res, err); errors.Is(err, ErrNotFound) {
		return ErrVersionNotFound
	} else {
		return err
	}
}

// SetAudioVersionFile records the file(s) a version has on disk.
func (r *Repo) SetAudioVersionFile(ctx context.Context, id int64, path, format string, size int64, files int) error {
	if files < 1 {
		files = 1
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE book_audio_versions SET path = ?, format = ?, size = ?, files = ? WHERE id = ?`,
		path, format, size, files, id)
	return err
}

// ClearAudioVersionFile forgets a version's file (removing it is the caller's job).
func (r *Repo) ClearAudioVersionFile(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE book_audio_versions SET path = '', format = '', size = 0, files = 0 WHERE id = ?`, id)
	return err
}

// DeleteAudioVersion removes a version row.
func (r *Repo) DeleteAudioVersion(ctx context.Context, bookID, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM book_audio_versions WHERE id = ? AND book_id = ?`, id, bookID)
	if err := affected(res, err); errors.Is(err, ErrNotFound) {
		return ErrVersionNotFound
	} else {
		return err
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- service ---------------------------------------------------------------

// validateVersion checks a label/terms pair against the book's other versions.
func (s *Service) validateVersion(ctx context.Context, bookID, selfID int64, label string) (string, error) {
	label = strings.Join(strings.Fields(label), " ")
	if label == "" {
		return "", fmt.Errorf("%w: give the version a name, e.g. \"Full cast\"", ErrVersionInvalid)
	}
	if len([]rune(label)) > 60 {
		return "", fmt.Errorf("%w: keep the name under 60 characters", ErrVersionInvalid)
	}
	if strings.ContainsAny(label, `/\:*?"<>|`) {
		return "", fmt.Errorf("%w: the name becomes part of a folder name, so it can't contain / \\ : * ? \" < > |", ErrVersionInvalid)
	}
	if strings.EqualFold(label, "standard") {
		return "", fmt.Errorf("%w: \"Standard\" is the book's own audiobook — pick another name", ErrVersionInvalid)
	}
	existing, err := s.repo.ListAudioVersions(ctx, bookID)
	if err != nil {
		return "", err
	}
	for _, v := range existing {
		if v.ID != selfID && strings.EqualFold(v.Label, label) {
			return "", fmt.Errorf("%w: this book already has a %q version", ErrVersionInvalid, v.Label)
		}
	}
	return label, nil
}

// AddAudioVersion creates an extra audio version for a book.
func (s *Service) AddAudioVersion(ctx context.Context, bookID int64, label string, terms []string, monitored bool) (AudioVersion, error) {
	if _, err := s.repo.Get(ctx, bookID); err != nil {
		return AudioVersion{}, err
	}
	label, err := s.validateVersion(ctx, bookID, 0, label)
	if err != nil {
		return AudioVersion{}, err
	}
	terms = CleanTerms(terms)
	id, err := s.repo.createAudioVersion(ctx, bookID, label, terms, monitored)
	if err != nil {
		return AudioVersion{}, err
	}
	s.repo.AddEvent(ctx, bookID, "version", fmt.Sprintf("Added the %q audiobook version", label))
	return s.repo.GetAudioVersion(ctx, bookID, id)
}

// UpdateAudioVersion renames a version or changes its terms / monitoring. A rename
// doesn't move files already on disk.
func (s *Service) UpdateAudioVersion(ctx context.Context, bookID, id int64, label string, terms []string, monitored bool) (AudioVersion, error) {
	if _, err := s.repo.GetAudioVersion(ctx, bookID, id); err != nil {
		return AudioVersion{}, err
	}
	label, err := s.validateVersion(ctx, bookID, id, label)
	if err != nil {
		return AudioVersion{}, err
	}
	if err := s.repo.updateAudioVersion(ctx, bookID, id, label, CleanTerms(terms), monitored); err != nil {
		return AudioVersion{}, err
	}
	return s.repo.GetAudioVersion(ctx, bookID, id)
}

// GetAudioVersion loads one version of a book.
func (s *Service) GetAudioVersion(ctx context.Context, bookID, id int64) (AudioVersion, error) {
	return s.repo.GetAudioVersion(ctx, bookID, id)
}

// SetAudioVersionFile records a version's file(s).
func (s *Service) SetAudioVersionFile(ctx context.Context, id int64, path, format string, size int64, files int) error {
	return s.repo.SetAudioVersionFile(ctx, id, path, format, size, files)
}

// ClearAudioVersionFile forgets a version's file.
func (s *Service) ClearAudioVersionFile(ctx context.Context, id int64) error {
	return s.repo.ClearAudioVersionFile(ctx, id)
}

// DeleteAudioVersion removes a version (its files are the caller's job).
func (s *Service) DeleteAudioVersion(ctx context.Context, bookID, id int64) error {
	v, err := s.repo.GetAudioVersion(ctx, bookID, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteAudioVersion(ctx, bookID, id); err != nil {
		return err
	}
	s.repo.AddEvent(ctx, bookID, "version", fmt.Sprintf("Removed the %q audiobook version", v.Label))
	return nil
}
