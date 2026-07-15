package books

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// Service is the Books module's application logic.
type Service struct {
	repo *Repo
	meta metadata.BookProvider
	log  *slog.Logger
}

// NewService wires the module.
func NewService(db *sql.DB, meta metadata.BookProvider, log *slog.Logger) *Service {
	return &Service{repo: NewRepo(db), meta: meta, log: log}
}

// MetadataAvailable reports whether the book provider is usable (Open Library always is).
func (s *Service) MetadataAvailable() bool { return s.meta.Available() }

// Lookup searches Open Library for books to add.
func (s *Service) Lookup(ctx context.Context, query string) ([]metadata.BookResult, error) {
	return s.meta.SearchBooks(ctx, query)
}

// SearchAuthors finds authors by name (Discover).
func (s *Service) SearchAuthors(ctx context.Context, query string) ([]metadata.AuthorResult, error) {
	return s.meta.SearchAuthors(ctx, query)
}

// AuthorWorks returns an author's catalogue (Discover).
func (s *Service) AuthorWorks(ctx context.Context, key string, limit int) ([]metadata.BookResult, error) {
	return s.meta.AuthorWorks(ctx, key, limit)
}

// Trending returns books trending this week (Discover).
func (s *Service) Trending(ctx context.Context) ([]metadata.BookResult, error) {
	return s.meta.TrendingBooks(ctx)
}

// BySubject returns books for a subject/genre (Discover).
func (s *Service) BySubject(ctx context.Context, subject string, limit int) ([]metadata.BookResult, error) {
	return s.meta.BooksBySubject(ctx, subject, limit)
}

// Detail fetches full metadata (description, subjects) for a work — used by the Discover
// request modal.
func (s *Service) Detail(ctx context.Context, key string) (*metadata.BookDetails, error) {
	return s.meta.GetBook(ctx, key)
}

// List returns the library.
func (s *Service) List(ctx context.Context) ([]Book, error) { return s.repo.List(ctx) }

// Get returns one book.
func (s *Service) Get(ctx context.Context, id int64) (Book, error) { return s.repo.Get(ctx, id) }

// Add pulls details for an Open Library work id and adds it. fallback supplies the
// year/author/cover/title from the search result — the work endpoint doesn't carry the
// publish year, so we backfill from what the lookup already knew.
func (s *Service) Add(ctx context.Context, olKey, qualityProfile string, monitored bool, fallback metadata.BookResult) (Book, error) {
	d, err := s.meta.GetBook(ctx, olKey)
	if err != nil {
		return Book{}, fmt.Errorf("fetch metadata: %w", err)
	}
	b := Book{
		OLKey: d.Key, Title: orStr(d.Title, fallback.Title), Author: orStr(d.Author, fallback.Author),
		Year: d.Year, CoverURL: orStr(d.CoverURL, fallback.CoverURL),
		Description: d.Description, Subjects: d.Subjects, Monitored: monitored, QualityProfile: qualityProfile,
	}
	if b.Year == 0 {
		b.Year = fallback.Year
	}
	created, err := s.repo.Create(ctx, b)
	if err != nil {
		return Book{}, err
	}
	s.log.Info("book added", "title", created.Title, "author", created.Author)
	return created, nil
}

// Covers returns candidate cover images for a book (Open Library editions + Google Books)
// for the cover picker.
func (s *Service) Covers(ctx context.Context, id int64) ([]string, error) {
	b, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.meta.Covers(ctx, b.OLKey, b.Title, b.Author)
}

// SetCover changes a book's cover image (a picked remote cover, or the served path of a
// custom upload).
func (s *Service) SetCover(ctx context.Context, id int64, coverURL string) error {
	return s.repo.SetCoverURL(ctx, id, coverURL)
}

// OverrideMetadata applies a manual metadata correction (title/author/year/description/cover).
func (s *Service) OverrideMetadata(ctx context.Context, id int64, title, author string, year int, description, coverURL string) error {
	return s.repo.UpdateDetails(ctx, id, strings.TrimSpace(title), strings.TrimSpace(author), year, description, coverURL)
}

// AddWorks bulk-adds a list of works (an author's catalogue) to the library, skipping any
// already present. Rows are created directly from the provided metadata — no per-book
// network fetch — so this stays fast for a big catalogue; descriptions/subjects fill in on
// the next refresh. Returns the books actually added and how many were skipped as dupes.
func (s *Service) AddWorks(ctx context.Context, works []metadata.BookResult, profile string, monitored bool) ([]Book, int) {
	var added []Book
	skipped := 0
	for _, wk := range works {
		if wk.Key == "" || wk.Title == "" {
			continue
		}
		created, err := s.repo.Create(ctx, Book{
			OLKey: wk.Key, Title: wk.Title, Author: wk.Author, Year: wk.Year,
			CoverURL: wk.CoverURL, Monitored: monitored, QualityProfile: profile,
		})
		if errors.Is(err, ErrExists) {
			skipped++
			continue
		}
		if err != nil {
			s.log.Warn("add author: create failed", "title", wk.Title, "err", err)
			continue
		}
		added = append(added, created)
	}
	if len(added) > 0 {
		s.log.Info("author catalogue added", "added", len(added), "skipped", skipped)
	}
	return added, skipped
}

// SetMonitored toggles a book.
func (s *Service) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	return s.repo.SetMonitored(ctx, id, monitored)
}

// SetQualityProfile changes a book's quality profile.
func (s *Service) SetQualityProfile(ctx context.Context, id int64, profile string) error {
	return s.repo.SetQualityProfile(ctx, id, profile)
}

// MarkImported records that a book edition (ebook|audiobook) landed on disk. files is
// how many files the edition has (>1 = a folder, e.g. an mp3 audiobook).
func (s *Service) MarkImported(ctx context.Context, id int64, kind, path, format string, size int64, files int) error {
	if err := s.repo.SetEdition(ctx, id, kind, path, format, size, files); err != nil {
		return err
	}
	s.log.Info("book imported", "id", id, "kind", kind, "format", format, "files", files)
	return nil
}

// ClearEdition forgets a book edition (after the user deletes its file).
func (s *Service) ClearEdition(ctx context.Context, id int64, kind string) error {
	return s.repo.ClearEdition(ctx, id, kind)
}

// Refresh re-pulls Open Library metadata (description, cover, subjects) for a book.
func (s *Service) Refresh(ctx context.Context, id int64) (Book, error) {
	b, err := s.repo.Get(ctx, id)
	if err != nil {
		return Book{}, err
	}
	if d, derr := s.meta.GetBook(ctx, b.OLKey); derr == nil {
		if d.Description != "" {
			b.Description = d.Description
		}
		if d.CoverURL != "" {
			b.CoverURL = d.CoverURL
		}
		if len(d.Subjects) > 0 {
			b.Subjects = d.Subjects
		}
		_ = s.repo.UpdateMeta(ctx, b.ID, b.Description, b.CoverURL, b.Subjects)
	}
	return s.repo.Get(ctx, id)
}

// MatchByRelease finds a book whose (normalized) title appears in a release name — used
// to route a finished download to the right book.
func (s *Service) MatchByRelease(ctx context.Context, releaseName string) (Book, bool) {
	norm := NormKey(releaseName)
	all, err := s.repo.List(ctx)
	if err != nil {
		return Book{}, false
	}
	var match Book
	found := false
	for _, b := range all {
		bt := NormKey(b.Title)
		if len(bt) >= 3 && strings.Contains(norm, bt) {
			// Prefer a match that also carries the author, to disambiguate common titles.
			if a := NormKey(b.Author); a != "" && strings.Contains(norm, a) {
				return b, true
			}
			match, found = b, true
		}
	}
	return match, found
}

// Delete removes a book.
func (s *Service) Delete(ctx context.Context, id int64) error { return s.repo.Delete(ctx, id) }

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// NormKey lowercases and keeps only alphanumerics — for tolerant title matching.
func NormKey(str string) string {
	var b []rune
	for _, r := range str {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b = append(b, r)
		case r >= 'A' && r <= 'Z':
			b = append(b, r+32)
		}
	}
	return string(b)
}
