package books

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// What the catalogue can add on top of the library's own records: a series with every
// entry (not just the gaps between owned books), author photos and bios, similar
// books, and exact ISBN matches. All of it is optional — a catalogue without the
// capability answers metadata.ErrNotSupported and the callers fall back.

type catalogueExtras interface {
	SeriesBooks(ctx context.Context, seriesKey string) (*metadata.SeriesInfo, error)
	AuthorDetail(ctx context.Context, key string) (*metadata.AuthorResult, error)
	SimilarBooks(ctx context.Context, key string) ([]metadata.BookResult, error)
	BookByISBN(ctx context.Context, isbn string) (*metadata.BookResult, error)
}

func (s *Service) extras() (catalogueExtras, bool) {
	e, ok := s.meta.(catalogueExtras)
	return e, ok
}

// SeriesEntry is one position of a series as the catalogue lists it, with what the
// library holds at that position.
type SeriesEntry struct {
	BookID   int64   `json:"book_id,omitempty"` // 0 when not in the library
	Key      string  `json:"key,omitempty"`     // catalogue key, so a missing entry can be added
	Title    string  `json:"title"`
	Author   string  `json:"author,omitempty"`
	Year     int     `json:"year,omitempty"`
	CoverURL string  `json:"cover_url,omitempty"`
	Position float64 `json:"position,omitempty"`
	HasFile  bool    `json:"has_file"`
	Missing  bool    `json:"missing"`
}

// SeriesView is a series with the library laid over it.
type SeriesView struct {
	Key     string        `json:"key"`
	Name    string        `json:"name"`
	Total   int           `json:"total"`
	Entries []SeriesEntry `json:"entries"`
	Gaps    int           `json:"gaps"`
}

// CatalogueSeries returns the full series a book belongs to, when the book carries a
// catalogue series key the current source can expand. ok is false otherwise (the
// caller then shows the series learned from releases, as before).
func (s *Service) CatalogueSeries(ctx context.Context, bookID int64) (*SeriesView, bool) {
	view, _, ok := s.catalogueSeries(ctx, bookID)
	return view, ok
}

func (s *Service) catalogueSeries(ctx context.Context, bookID int64) (*SeriesView, []metadata.BookResult, bool) {
	b, err := s.repo.Get(ctx, bookID)
	if err != nil || b.SeriesKey == "" {
		return nil, nil, false
	}
	ex, ok := s.extras()
	if !ok {
		return nil, nil, false
	}
	info, err := ex.SeriesBooks(ctx, b.SeriesKey)
	if err != nil || info == nil || len(info.Books) == 0 {
		if err != nil && !errors.Is(err, metadata.ErrNotSupported) {
			s.log.Debug("books: catalogue series lookup failed", "series_key", b.SeriesKey, "err", err)
		}
		return nil, nil, false
	}
	// Match the library onto the series by catalogue key first, then by title+author,
	// so books still on Open Library keys count as owned.
	list, _ := s.repo.List(ctx)
	byKey := map[string]Book{}
	for _, lb := range list {
		byKey[lb.OLKey] = lb
	}
	same := NewIdentityIndex(list)
	view := &SeriesView{Key: info.Key, Name: info.Name, Total: info.Count}
	var missing []metadata.BookResult
	for _, r := range info.Books {
		e := SeriesEntry{Key: r.Key, Title: r.Title, Author: r.Author, Year: r.Year, CoverURL: r.CoverURL, Position: r.SeriesPosition}
		owned, has := byKey[r.Key]
		if !has {
			owned, has = same.Find(r.Title, r.Author)
		}
		if has {
			e.BookID, e.HasFile = owned.ID, owned.HasFile
		} else {
			e.Missing = true
			view.Gaps++
			missing = append(missing, r)
		}
		view.Entries = append(view.Entries, e)
	}
	return view, missing, true
}

// AddMissingInSeries adds every entry of a book's series that the library lacks.
func (s *Service) AddMissingInSeries(ctx context.Context, bookID int64, profile string, monitored bool) (added, skipped int, err error) {
	_, missing, ok := s.catalogueSeries(ctx, bookID)
	if !ok {
		return 0, 0, metadata.ErrNotSupported
	}
	if len(missing) == 0 {
		return 0, 0, nil
	}
	got, skip := s.AddWorks(ctx, missing, profile, monitored)
	// AddWorks builds rows from the search result alone; series membership is known
	// here, so stamp it, and the source book's series key with it.
	src, _ := s.repo.Get(ctx, bookID)
	for _, b := range got {
		for _, m := range missing {
			if m.Key == b.OLKey {
				_ = s.repo.SetSeriesRef(ctx, b.ID, m.SeriesName, m.SeriesPosition, src.SeriesKey)
				break
			}
		}
	}
	return len(got), skip, nil
}

// SimilarBooks is the catalogue's "readers also liked" for a key (Discover detail).
func (s *Service) SimilarBooks(ctx context.Context, key string) ([]metadata.BookResult, error) {
	ex, ok := s.extras()
	if !ok {
		return nil, metadata.ErrNotSupported
	}
	return ex.SimilarBooks(ctx, key)
}

// AuthorDetail returns an author with photo and biography when the catalogue has them.
func (s *Service) AuthorDetail(ctx context.Context, key string) (*metadata.AuthorResult, error) {
	ex, ok := s.extras()
	if !ok {
		return nil, metadata.ErrNotSupported
	}
	return ex.AuthorDetail(ctx, key)
}

// LookupISBN resolves an ISBN to a catalogue entry (nil when nothing has it).
func (s *Service) LookupISBN(ctx context.Context, isbn string) (*metadata.BookResult, error) {
	ex, ok := s.extras()
	if !ok {
		return nil, metadata.ErrNotSupported
	}
	return ex.BookByISBN(ctx, isbn)
}

// authorImageRetry is how long a fruitless lookup stands before an author is tried
// again, and authorImageBatch how many lookups one page load may spend.
const (
	authorImageRetry = 30 * 24 * time.Hour
	authorImageBatch = 6
)

// KnownAuthorImages returns the author photos already looked up, without triggering any
// catalogue lookups — for the audiobook server, which serves them to listening apps.
func (s *Service) KnownAuthorImages(ctx context.Context) map[string]string {
	out := map[string]string{}
	known, err := s.repo.authorImages(ctx)
	if err != nil {
		return out
	}
	for name, rec := range known {
		if rec.ImageURL != "" {
			out[name] = rec.ImageURL
		}
	}
	return out
}

// AuthorImages returns a photo URL per library author, from the book_authors table.
// Authors not yet looked up are resolved through the current catalogue a few per call
// (the Books page polls until nothing is pending), so a first visit costs a handful of
// requests and later ones none.
func (s *Service) AuthorImages(ctx context.Context) (map[string]string, int, error) {
	list, err := s.repo.List(ctx)
	if err != nil {
		return nil, 0, err
	}
	names := map[string]bool{}
	for _, b := range list {
		if n := strings.TrimSpace(b.Author); n != "" {
			names[n] = true
		}
	}
	known, err := s.repo.authorImages(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := map[string]string{}
	var pending []string
	for n := range names {
		if rec, ok := known[n]; ok {
			if rec.ImageURL != "" {
				out[n] = rec.ImageURL
			}
			if rec.ImageURL != "" || time.Since(rec.CheckedAt) < authorImageRetry {
				continue
			}
		}
		pending = append(pending, n)
	}
	if s.MetadataSource() != metadata.SourceHardcover || len(pending) == 0 {
		return out, 0, nil
	}
	looked := 0
	for _, n := range pending {
		if looked >= authorImageBatch || ctx.Err() != nil {
			break
		}
		looked++
		key, img, bio := "", "", ""
		if hits, err := s.meta.SearchAuthors(ctx, n); err == nil {
			for _, h := range hits {
				if authorKey(h.Name) == authorKey(n) {
					key, img, bio = h.Key, h.ImageURL, h.Bio
					break
				}
			}
			if key == "" && len(hits) > 0 && strings.EqualFold(hits[0].Name, n) {
				key, img, bio = hits[0].Key, hits[0].ImageURL, hits[0].Bio
			}
		} else if errors.Is(err, metadata.ErrHardcoverBudget) {
			break
		}
		_ = s.repo.setAuthorImage(ctx, n, key, img, bio)
		if img != "" {
			out[n] = img
		}
	}
	return out, len(pending) - looked, nil
}

// --- repo bits ---

type authorRecord struct {
	Key, ImageURL, Bio string
	CheckedAt          time.Time
}

func (r *Repo) authorImages(ctx context.Context) (map[string]authorRecord, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT name, key, image_url, bio, checked_at FROM book_authors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]authorRecord{}
	for rows.Next() {
		var name, checked string
		var rec authorRecord
		if err := rows.Scan(&name, &rec.Key, &rec.ImageURL, &rec.Bio, &checked); err != nil {
			return nil, err
		}
		rec.CheckedAt, _ = time.Parse("2006-01-02 15:04:05", checked)
		out[name] = rec
	}
	return out, rows.Err()
}

func (r *Repo) setAuthorImage(ctx context.Context, name, key, imageURL, bio string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO book_authors (name, key, image_url, bio, checked_at) VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(name) DO UPDATE SET key = excluded.key, image_url = excluded.image_url, bio = excluded.bio, checked_at = CURRENT_TIMESTAMP`,
		name, key, imageURL, bio)
	return err
}

// SetSeriesRef records a book's series with the catalogue's key for it.
func (r *Repo) SetSeriesRef(ctx context.Context, bookID int64, name string, position float64, key string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE books SET series_name = ?, series_position = ?, series_key = ? WHERE id = ?`, name, position, key, bookID)
	return err
}
