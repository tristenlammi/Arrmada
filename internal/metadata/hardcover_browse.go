package metadata

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Browse rows for the Books Discover page. Each is one cached query (six hours), so
// the whole page costs a handful of requests a day however often it's opened.
const (
	BrowseTrending    = "trending"     // most-shelved of the last two years
	BrowseNewReleases = "new_releases" // released in the last four months, most-shelved first
	BrowseTopRated    = "top_rated"    // best average rating among widely-rated books
	BrowsePopular     = "popular"      // most-shelved of all time
)

// BrowseBooks returns one of the browse rows.
func (h *Hardcover) BrowseBooks(ctx context.Context, kind string) ([]BookResult, error) {
	switch kind {
	case BrowseTrending:
		return h.TrendingBooks(ctx)
	case BrowseNewReleases, BrowseTopRated, BrowsePopular:
	default:
		return nil, ErrNotSupported
	}
	return cached(ctx, h.cache, "browse:v2:"+kind, hcTTLList, func(ctx context.Context) ([]BookResult, error) {
		var where, order string
		vars := map[string]any{}
		switch kind {
		case BrowseNewReleases:
			where = `{canonical_id: {_is_null: true}, compilation: {_eq: false}, release_date: {_gte: $d}}`
			order = `{users_count: desc}`
			vars["d"] = time.Now().AddDate(0, -4, 0).Format("2006-01-02")
		case BrowseTopRated:
			where = `{canonical_id: {_is_null: true}, compilation: {_eq: false}, ratings_count: {_gte: 2000}}`
			order = `{rating: desc}`
		case BrowsePopular:
			where = `{canonical_id: {_is_null: true}, compilation: {_eq: false}}`
			order = `{users_count: desc}`
		}
		decl := ""
		if _, ok := vars["d"]; ok {
			decl = `($d: date!)`
		}
		q := fmt.Sprintf(`query%s { books(where: %s, order_by: %s, limit: 24) { %s } }`, decl, where, order, hcBookFields)
		var data struct {
			Books []hcBook `json:"books"`
		}
		if err := h.query(ctx, q, vars, &data); err != nil {
			return nil, err
		}
		out := make([]BookResult, 0, len(data.Books))
		for _, b := range data.Books {
			if r := b.result(); r.Title != "" {
				out = append(out, r)
			}
		}
		return filterBundles(out), nil
	})
}

// BrowseBooks on the switch: Hardcover when it's the source; Open Library only has
// trending. Anything else is ErrNotSupported and the row stays hidden.
func (s *BookSources) BrowseBooks(ctx context.Context, kind string) ([]BookResult, error) {
	if s.Source() == SourceHardcover {
		r, err := s.hardcover.BrowseBooks(ctx, kind)
		if err == nil && len(r) > 0 {
			return r, nil
		}
		if err != nil && !strings.Contains(err.Error(), "budget") && kind != BrowseTrending {
			return nil, err
		}
	}
	if kind == BrowseTrending {
		return s.openlib.TrendingBooks(ctx)
	}
	return nil, ErrNotSupported
}
