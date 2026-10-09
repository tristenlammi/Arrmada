package books

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// Discover rows built on the catalogue.

type bookBrowser interface {
	BrowseBooks(ctx context.Context, kind string) ([]metadata.BookResult, error)
}

// Browse returns one of the browse rows (trending, new_releases, top_rated, popular).
func (s *Service) Browse(ctx context.Context, kind string) ([]metadata.BookResult, error) {
	if b, ok := s.meta.(bookBrowser); ok {
		return b.BrowseBooks(ctx, kind)
	}
	if kind == metadata.BrowseTrending {
		return s.meta.TrendingBooks(ctx)
	}
	return nil, metadata.ErrNotSupported
}

// RecommendedRow is one "Because you own …" strip.
type RecommendedRow struct {
	Title  string                `json:"title"`
	Seed   string                `json:"seed"` // the owned book's title
	Books  []metadata.BookResult `json:"books"`
	SeedID int64                 `json:"seed_id"`
}

const recommendedRows = 3

// Recommended builds "Because you own X" rows from the catalogue's similar-books
// lists. Seeds rotate daily through the library's Hardcover-keyed books (files first),
// so the page changes without spending more than a few requests a day; everything
// already in the library is left out of the results.
func (s *Service) Recommended(ctx context.Context) ([]RecommendedRow, error) {
	return s.recommendedAt(ctx, time.Now())
}

func (s *Service) recommendedAt(ctx context.Context, now time.Time) ([]RecommendedRow, error) {
	ex, ok := s.extras()
	if !ok {
		return nil, metadata.ErrNotSupported
	}
	list, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	owned := map[string]bool{}
	same := NewIdentityIndex(list)
	var seeds []Book
	for _, b := range list {
		owned[b.OLKey] = true
		if metadata.IsHardcoverKey(b.OLKey) {
			seeds = append(seeds, b)
		}
	}
	if len(seeds) == 0 {
		return nil, nil
	}
	// Stable order (files first, then id) and a daily offset into it.
	sort.SliceStable(seeds, func(i, j int) bool {
		if seeds[i].HasFile != seeds[j].HasFile {
			return seeds[i].HasFile
		}
		return seeds[i].ID < seeds[j].ID
	})
	start := now.UTC().YearDay() % len(seeds)
	var rows []RecommendedRow
	for i := 0; i < len(seeds) && len(rows) < recommendedRows; i++ {
		seed := seeds[(start+i)%len(seeds)]
		sim, err := ex.SimilarBooks(ctx, seed.OLKey)
		if err != nil {
			if errors.Is(err, metadata.ErrHardcoverBudget) {
				break
			}
			continue
		}
		var keep []metadata.BookResult
		for _, r := range sim {
			if owned[r.Key] {
				continue
			}
			if _, ok := same.Find(r.Title, r.Author); ok {
				continue
			}
			keep = append(keep, r)
		}
		if len(keep) < 3 {
			continue
		}
		rows = append(rows, RecommendedRow{Title: "Because you own " + seed.Title, Seed: seed.Title, SeedID: seed.ID, Books: keep})
	}
	return rows, nil
}
