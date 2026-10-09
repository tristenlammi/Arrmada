package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// The parts of Hardcover that Open Library never had: a series with every entry in
// order, an author with a photo and a biography, a similar-books list, and an exact
// lookup by ISBN. Each is an optional capability — BookSources answers ErrNotSupported
// for a key or a source that can't do it, and the callers degrade to what they had.

const hcSeriesPrefix = "hc:s:"

// ErrNotSupported is returned for a capability the current catalogue (or the
// catalogue a key belongs to) doesn't have.
var ErrNotSupported = errors.New("not supported by this catalogue")

// SeriesInfo is a series as the catalogue knows it: every entry, in reading order.
type SeriesInfo struct {
	Key   string       `json:"key"`
	Name  string       `json:"name"`
	Count int          `json:"count"`
	Books []BookResult `json:"books"` // SeriesPosition set on each
}

func hcSeriesID(key string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(key, hcSeriesPrefix))
	return n, err == nil && n > 0 && strings.HasPrefix(key, hcSeriesPrefix)
}

// SeriesBooks lists a series' entries, one per position (Hardcover holds duplicates
// and partial editions at the same position; the most-shelved wins), leaving out
// entries merged into others and compilations.
func (h *Hardcover) SeriesBooks(ctx context.Context, seriesKey string) (*SeriesInfo, error) {
	id, ok := hcSeriesID(seriesKey)
	if !ok {
		return nil, ErrNotSupported
	}
	return cached(ctx, h.cache, "series:"+seriesKey, hcTTLList, func(ctx context.Context) (*SeriesInfo, error) {
		info, err := h.seriesLive(ctx, id, true)
		if err != nil && ctx.Err() == nil && !errors.Is(err, ErrHardcoverBudget) {
			// The compilation filter sits on the join row per the docs; if this build
			// of the schema disagrees, ask without it rather than fail the page.
			info, err = h.seriesLive(ctx, id, false)
		}
		return info, err
	})
}

func (h *Hardcover) seriesLive(ctx context.Context, id int, filterCompilations bool) (*SeriesInfo, error) {
	where := `{book: {canonical_id: {_is_null: true}}}`
	if filterCompilations {
		where = `{book: {canonical_id: {_is_null: true}}, compilation: {_eq: false}}`
	}
	q := `query($id: Int!) {
  series(where: {id: {_eq: $id}}, limit: 1) {
    id name books_count
    book_series(distinct_on: position, order_by: [{position: asc}, {book: {users_count: desc}}], where: ` + where + `) {
      position book { ` + hcBookFields + ` }
    }
  }
}`
	var data struct {
		Series []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			BooksCount int    `json:"books_count"`
			BookSeries []struct {
				Position *float64 `json:"position"`
				Book     hcBook   `json:"book"`
			} `json:"book_series"`
		} `json:"series"`
	}
	if err := h.query(ctx, q, map[string]any{"id": id}, &data); err != nil {
		return nil, err
	}
	if len(data.Series) == 0 {
		return nil, fmt.Errorf("hardcover: series %d not found", id)
	}
	s := data.Series[0]
	info := &SeriesInfo{Key: hcSeriesPrefix + strconv.Itoa(s.ID), Name: s.Name, Count: s.BooksCount}
	for _, bs := range s.BookSeries {
		r := bs.Book.result()
		if r.Title == "" {
			continue
		}
		if bs.Position != nil {
			r.SeriesPosition = *bs.Position
		}
		r.SeriesName = s.Name
		info.Books = append(info.Books, r)
	}
	if info.Count < len(info.Books) {
		info.Count = len(info.Books)
	}
	return info, nil
}

// AuthorDetail returns an author with photo and biography.
func (h *Hardcover) AuthorDetail(ctx context.Context, key string) (*AuthorResult, error) {
	id, ok := hcAuthorID(key)
	if !ok || !strings.HasPrefix(key, hcAuthorPrefix) {
		return nil, ErrNotSupported
	}
	return cached(ctx, h.cache, "author:"+key, hcTTLAuthor, func(ctx context.Context) (*AuthorResult, error) {
		const q = `query($id: Int!) {
  authors(where: {id: {_eq: $id}}, limit: 1) { id name bio books_count born_year death_year image { url } }
}`
		var data struct {
			Authors []struct {
				ID         int      `json:"id"`
				Name       string   `json:"name"`
				Bio        string   `json:"bio"`
				BooksCount int      `json:"books_count"`
				BornYear   *int     `json:"born_year"`
				DeathYear  *int     `json:"death_year"`
				Image      *hcImage `json:"image"`
			} `json:"authors"`
		}
		if err := h.query(ctx, q, map[string]any{"id": id}, &data); err != nil {
			return nil, err
		}
		if len(data.Authors) == 0 {
			return nil, fmt.Errorf("hardcover: author %d not found", id)
		}
		a := data.Authors[0]
		out := &AuthorResult{Key: key, Name: a.Name, WorkCount: a.BooksCount, Bio: strings.TrimSpace(a.Bio)}
		if a.Image != nil {
			out.ImageURL = a.Image.URL
		}
		if a.BornYear != nil && *a.BornYear > 0 {
			out.BirthDate = strconv.Itoa(*a.BornYear)
			if a.DeathYear != nil && *a.DeathYear > 0 {
				out.BirthDate += "–" + strconv.Itoa(*a.DeathYear)
			}
		}
		return out, nil
	})
}

// SimilarBooks returns the catalogue's own "readers also liked" list for a book.
func (h *Hardcover) SimilarBooks(ctx context.Context, key string) ([]BookResult, error) {
	id, ok := hcBookID(key)
	if !ok {
		return nil, ErrNotSupported
	}
	return cached(ctx, h.cache, "similar:v2:"+key, hcTTLSimilar, func(ctx context.Context) ([]BookResult, error) {
		var data struct {
			Books []struct {
				IDs json.RawMessage `json:"cached_similar_book_ids"`
			} `json:"books"`
		}
		if err := h.query(ctx, `query($id: Int!) { books(where: {id: {_eq: $id}}, limit: 1) { cached_similar_book_ids } }`,
			map[string]any{"id": id}, &data); err != nil {
			return nil, err
		}
		if len(data.Books) == 0 {
			return nil, nil
		}
		ids := rawIntList(data.Books[0].IDs)
		if len(ids) > 12 {
			ids = ids[:12]
		}
		if len(ids) == 0 {
			return nil, nil
		}
		var got struct {
			Books []hcBook `json:"books"`
		}
		if err := h.query(ctx, `query($ids: [Int!]!) { books(where: {id: {_in: $ids}, canonical_id: {_is_null: true}}) { `+hcBookFields+` } }`,
			map[string]any{"ids": ids}, &got); err != nil {
			return nil, err
		}
		byID := map[int]BookResult{}
		for _, b := range got.Books {
			if r := b.result(); r.Title != "" {
				byID[b.ID] = r
			}
		}
		out := make([]BookResult, 0, len(ids))
		for _, id := range ids { // the catalogue's order is most-similar first
			if r, ok := byID[id]; ok {
				out = append(out, r)
			}
		}
		return filterBundles(out), nil
	})
}

// rawIntList reads a JSON array of ints (or numeric strings).
func rawIntList(raw json.RawMessage) []int {
	var any []json.RawMessage
	if json.Unmarshal(raw, &any) != nil {
		return nil
	}
	out := make([]int, 0, len(any))
	for _, v := range any {
		if n := rawInt(v); n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// BookByISBN resolves an ISBN to its book through the editions table.
func (h *Hardcover) BookByISBN(ctx context.Context, isbn string) (*BookResult, error) {
	isbn = NormalizeISBN(isbn)
	if isbn == "" {
		return nil, fmt.Errorf("not an ISBN")
	}
	return cached(ctx, h.cache, "isbn:"+isbn, hcTTLBook, func(ctx context.Context) (*BookResult, error) {
		const q = `query($i: String!) {
  editions(where: {_or: [{isbn_13: {_eq: $i}}, {isbn_10: {_eq: $i}}]}, limit: 1) { book { ` + hcBookFields + ` } }
}`
		var data struct {
			Editions []struct {
				Book hcBook `json:"book"`
			} `json:"editions"`
		}
		if err := h.query(ctx, q, map[string]any{"i": isbn}, &data); err != nil {
			return nil, err
		}
		if len(data.Editions) == 0 {
			return nil, nil
		}
		r := data.Editions[0].Book.result()
		if r.Title == "" {
			return nil, nil
		}
		return &r, nil
	})
}

// BookByISBN on Open Library goes through its search index.
func (o *OpenLibrary) BookByISBN(ctx context.Context, isbn string) (*BookResult, error) {
	isbn = NormalizeISBN(isbn)
	if isbn == "" {
		return nil, fmt.Errorf("not an ISBN")
	}
	q := url.Values{}
	q.Set("isbn", isbn)
	q.Set("limit", "1")
	q.Set("fields", "key,title,author_name,first_publish_year,cover_i")
	body, err := o.get(ctx, "/search.json", q)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Docs []struct {
			Key     string   `json:"key"`
			Title   string   `json:"title"`
			Authors []string `json:"author_name"`
			Year    int      `json:"first_publish_year"`
			CoverID int      `json:"cover_i"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if len(payload.Docs) == 0 || payload.Docs[0].Title == "" {
		return nil, nil
	}
	d := payload.Docs[0]
	return &BookResult{Key: workKey(d.Key), Title: d.Title, Author: firstOr(d.Authors, ""), Year: d.Year, CoverURL: coverURL(d.CoverID)}, nil
}

// NormalizeISBN strips separators and validates the checksum, returning "" for
// anything that isn't a real ISBN-10 or ISBN-13.
func NormalizeISBN(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= '0' && r <= '9') || r == 'X' {
			b.WriteRune(r)
		}
	}
	isbn := b.String()
	switch len(isbn) {
	case 13:
		sum := 0
		for i, r := range isbn {
			if r == 'X' {
				return ""
			}
			d := int(r - '0')
			if i%2 == 1 {
				d *= 3
			}
			sum += d
		}
		if sum%10 != 0 || !(strings.HasPrefix(isbn, "978") || strings.HasPrefix(isbn, "979")) {
			return ""
		}
		return isbn
	case 10:
		sum := 0
		for i, r := range isbn {
			var d int
			switch {
			case r == 'X' && i == 9:
				d = 10
			case r >= '0' && r <= '9':
				d = int(r - '0')
			default:
				return ""
			}
			sum += d * (10 - i)
		}
		if sum%11 != 0 {
			return ""
		}
		return isbn
	}
	return ""
}

// --- BookSources routing for the optional capabilities ---

func (s *BookSources) SeriesBooks(ctx context.Context, seriesKey string) (*SeriesInfo, error) {
	if strings.HasPrefix(seriesKey, hcSeriesPrefix) && s.hardcover != nil && s.hardcover.Available() {
		return s.hardcover.SeriesBooks(ctx, seriesKey)
	}
	return nil, ErrNotSupported
}

func (s *BookSources) AuthorDetail(ctx context.Context, key string) (*AuthorResult, error) {
	if strings.HasPrefix(key, hcAuthorPrefix) && s.hardcover != nil && s.hardcover.Available() {
		return s.hardcover.AuthorDetail(ctx, key)
	}
	return nil, ErrNotSupported
}

func (s *BookSources) SimilarBooks(ctx context.Context, key string) ([]BookResult, error) {
	if IsHardcoverKey(key) && s.hardcover != nil && s.hardcover.Available() {
		return s.hardcover.SimilarBooks(ctx, key)
	}
	return nil, ErrNotSupported
}

// BookByISBN asks the current catalogue first, then Open Library.
func (s *BookSources) BookByISBN(ctx context.Context, isbn string) (*BookResult, error) {
	if s.Source() == SourceHardcover {
		if r, err := s.hardcover.BookByISBN(ctx, isbn); err == nil && r != nil {
			return r, nil
		}
	}
	if ol, ok := s.openlib.(interface {
		BookByISBN(context.Context, string) (*BookResult, error)
	}); ok {
		return ol.BookByISBN(ctx, isbn)
	}
	return nil, ErrNotSupported
}

// BookByISBN on the fallback wrapper goes to the primary (Open Library).
func (f *FallbackBookProvider) BookByISBN(ctx context.Context, isbn string) (*BookResult, error) {
	if p, ok := f.primary.(interface {
		BookByISBN(context.Context, string) (*BookResult, error)
	}); ok {
		return p.BookByISBN(ctx, isbn)
	}
	return nil, ErrNotSupported
}
