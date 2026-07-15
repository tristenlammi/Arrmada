package metadata

import (
	"context"
	"strings"
)

// FallbackBookProvider tries a primary provider (Open Library) first, falling back to a secondary
// (Google Books) for the acquisition-critical search + get paths when the primary errors or finds
// nothing. This is Books' answer to the metadata-fragility trap that killed Readarr's single
// Goodreads dependency: a book/author the primary can't resolve still gets found. The author and
// discover surfaces stay on the primary (the secondary doesn't implement them).
type FallbackBookProvider struct {
	primary   BookProvider
	secondary BookProvider
}

// NewBooksWithFallback wraps primary with a secondary fallback.
func NewBooksWithFallback(primary, secondary BookProvider) *FallbackBookProvider {
	return &FallbackBookProvider{primary: primary, secondary: secondary}
}

func (f *FallbackBookProvider) Available() bool {
	return f.primary.Available() || (f.secondary != nil && f.secondary.Available())
}

// SearchBooks tries the primary; if it errors or returns nothing, it tries the secondary.
func (f *FallbackBookProvider) SearchBooks(ctx context.Context, query string) ([]BookResult, error) {
	r, err := f.primary.SearchBooks(ctx, query)
	if err == nil && len(r) > 0 {
		return r, nil
	}
	if f.secondary != nil && f.secondary.Available() {
		if r2, err2 := f.secondary.SearchBooks(ctx, query); err2 == nil && len(r2) > 0 {
			return r2, nil
		}
	}
	return r, err
}

// GetBook routes by key origin: a "gb:" key came from the secondary, everything else the primary.
func (f *FallbackBookProvider) GetBook(ctx context.Context, key string) (*BookDetails, error) {
	if strings.HasPrefix(key, gbKeyPrefix) {
		if f.secondary != nil {
			return f.secondary.GetBook(ctx, key)
		}
	}
	return f.primary.GetBook(ctx, key)
}

// Author + discover surfaces pass straight through to the primary (Open Library).
func (f *FallbackBookProvider) Covers(ctx context.Context, key, title, author string) ([]string, error) {
	return f.primary.Covers(ctx, key, title, author)
}
func (f *FallbackBookProvider) SearchAuthors(ctx context.Context, query string) ([]AuthorResult, error) {
	return f.primary.SearchAuthors(ctx, query)
}
func (f *FallbackBookProvider) AuthorWorks(ctx context.Context, authorKey string, limit int) ([]BookResult, error) {
	return f.primary.AuthorWorks(ctx, authorKey, limit)
}
func (f *FallbackBookProvider) TrendingBooks(ctx context.Context) ([]BookResult, error) {
	return f.primary.TrendingBooks(ctx)
}
func (f *FallbackBookProvider) BooksBySubject(ctx context.Context, subject string, limit int) ([]BookResult, error) {
	return f.primary.BooksBySubject(ctx, subject, limit)
}
