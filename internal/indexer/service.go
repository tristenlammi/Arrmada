package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/flaresolverr"
	"github.com/tristenlammi/arrmada/internal/parser"
)

// Service manages configured indexers and runs aggregated searches across them.
type Service struct {
	repo     *Repo
	registry *Registry
	log      *slog.Logger
}

// NewService wires a Service over the database. flaresolverrURL may be empty
// (no Cloudflare solving).
func NewService(db *sql.DB, log *slog.Logger, flaresolverrURL string) *Service {
	var fs *flaresolverr.Client
	if flaresolverrURL != "" {
		fs = flaresolverr.New(flaresolverrURL)
	}
	s := &Service{repo: NewRepo(db), registry: NewRegistry(fs), log: log}
	// Persist a rotated MyAnonaMouse session so it doesn't silently expire.
	s.registry.SetSessionPersister(func(id int64, session string) {
		if err := s.repo.SetSession(context.Background(), id, session); err != nil {
			s.log.Warn("indexer: could not persist rotated mam_id", "id", id, "err", err)
		} else {
			s.log.Info("indexer: refreshed MyAnonaMouse session", "id", id)
		}
	})
	return s
}

// List returns all configured indexers.
func (s *Service) List(ctx context.Context) ([]Indexer, error) { return s.repo.List(ctx) }

// Get returns one indexer.
func (s *Service) Get(ctx context.Context, id int64) (Indexer, error) { return s.repo.Get(ctx, id) }

// Create stores a new indexer.
func (s *Service) Create(ctx context.Context, idx Indexer) (Indexer, error) {
	return s.repo.Create(ctx, idx)
}

// Update changes an indexer's settings.
func (s *Service) Update(ctx context.Context, idx Indexer) error { return s.repo.Update(ctx, idx) }

// Delete removes an indexer.
func (s *Service) Delete(ctx context.Context, id int64) error { return s.repo.Delete(ctx, id) }

// Fetch resolves a search result's download link via the named indexer into a
// FetchResult (file bytes or a magnet/URL) ready for the download client.
func (s *Service) Fetch(ctx context.Context, indexerName, downloadURL string) (FetchResult, error) {
	indexers, err := s.repo.List(ctx)
	if err != nil {
		return FetchResult{}, err
	}
	var found *Indexer
	for i := range indexers {
		if indexers[i].Name == indexerName {
			found = &indexers[i]
			break
		}
	}
	if found == nil {
		return FetchResult{}, fmt.Errorf("indexer %q not found", indexerName)
	}
	searcher, err := s.registry.For(found.Kind)
	if err != nil {
		return FetchResult{}, err
	}
	if f, ok := searcher.(Fetcher); ok {
		return f.Fetch(ctx, *found, downloadURL)
	}
	// No special handling (torznab/newznab): the client fetches the URL itself.
	return FetchResult{URL: downloadURL}, nil
}

// Test checks connectivity + auth for a stored indexer.
func (s *Service) Test(ctx context.Context, id int64) error {
	idx, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	searcher, err := s.registry.For(idx.Kind)
	if err != nil {
		return err
	}
	return searcher.Test(ctx, idx)
}

// SearchResult bundles aggregated releases with per-indexer errors so a single
// dead indexer never sinks the whole search.
type SearchResult struct {
	Releases []Release         `json:"releases"`
	Errors   map[string]string `json:"errors,omitempty"` // indexer name -> error
}

// Recent fetches the newest releases from every enabled indexer that supports an
// RSS-style feed (Recenter), merged and ranked like a search. Indexers without
// the capability are simply skipped.
func (s *Service) Recent(ctx context.Context, limit int) (SearchResult, error) {
	indexers, err := s.repo.ListEnabled(ctx)
	if err != nil {
		return SearchResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		result   = SearchResult{Errors: map[string]string{}}
		priority = map[string]int{}
	)
	for _, idx := range indexers {
		searcher, err := s.registry.For(idx.Kind)
		if err != nil {
			continue
		}
		rec, ok := searcher.(Recenter)
		if !ok {
			continue // this indexer kind has no feed
		}
		priority[idx.Name] = idx.Priority
		wg.Add(1)
		go func(idx Indexer, rec Recenter) {
			defer wg.Done()
			releases, err := rec.Recent(ctx, idx, limit)
			if err != nil {
				mu.Lock()
				result.Errors[idx.Name] = err.Error()
				mu.Unlock()
				s.log.Warn("indexer recent failed", "indexer", idx.Name, "err", err)
				return
			}
			if idx.MinSeeders > 0 {
				kept := releases[:0]
				for _, rel := range releases {
					if rel.Transport == TransportUsenet || rel.Seeders >= idx.MinSeeders {
						kept = append(kept, rel)
					}
				}
				releases = kept
			}
			mu.Lock()
			result.Releases = append(result.Releases, releases...)
			mu.Unlock()
		}(idx, rec)
	}
	wg.Wait()

	result.Releases = filterAdult(result.Releases)

	sort.SliceStable(result.Releases, func(i, j int) bool {
		a, b := result.Releases[i], result.Releases[j]
		if a.Seeders != b.Seeders {
			return a.Seeders > b.Seeders
		}
		return priority[a.Indexer] < priority[b.Indexer]
	})
	if len(result.Errors) == 0 {
		result.Errors = nil
	}
	return result, nil
}

// Search queries every enabled indexer concurrently and merges the results,
// ranked by seeders (desc) then indexer priority.
func (s *Service) Search(ctx context.Context, q SearchQuery) (SearchResult, error) {
	// Releases are named in ASCII, so a title carrying diacritics ("Pokémon Heroes")
	// finds nothing until it's folded ("Pokemon Heroes"). Done here so every caller —
	// movies, series, books — benefits.
	q.Text = parser.FoldAccents(q.Text)

	indexers, err := s.repo.ListEnabled(ctx)
	if err != nil {
		return SearchResult{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		result   = SearchResult{Errors: map[string]string{}}
		priority = map[string]int{}
	)

	for _, idx := range indexers {
		if !idx.Serves(q.MediaType) {
			continue // this indexer isn't scoped to the media type being searched
		}
		priority[idx.Name] = idx.Priority
		wg.Add(1)
		go func(idx Indexer) {
			defer wg.Done()

			searcher, err := s.registry.For(idx.Kind)
			if err == nil {
				var releases []Release
				releases, err = searcher.Search(ctx, idx, q)
				if err == nil {
					returned := len(releases)
					// Drop torrents below this indexer's seeder floor.
					if idx.MinSeeders > 0 {
						kept := releases[:0]
						for _, rel := range releases {
							if rel.Transport == TransportUsenet || rel.Seeders >= idx.MinSeeders {
								kept = append(kept, rel)
							}
						}
						releases = kept
					}
					// Per-indexer accounting. The aggregate count alone can't distinguish
					// "the indexer only had this much" from "the seeder floor discarded the
					// rest" — and an old season pack with few seeders is exactly the sort of
					// release that quietly vanishes here.
					s.log.Info("indexer search", "indexer", idx.Name, "query", q.Text,
						"returned", returned, "kept", len(releases),
						"dropped_low_seeders", returned-len(releases), "min_seeders", idx.MinSeeders,
						"limit", q.Limit)
					mu.Lock()
					result.Releases = append(result.Releases, releases...)
					mu.Unlock()
					return
				}
			}
			mu.Lock()
			result.Errors[idx.Name] = err.Error()
			mu.Unlock()
			s.log.Warn("indexer search failed", "indexer", idx.Name, "err", err)
		}(idx)
	}
	wg.Wait()

	// Safety: never surface or hand on adult content, on any indexer.
	result.Releases = filterAdult(result.Releases)

	sort.SliceStable(result.Releases, func(i, j int) bool {
		a, b := result.Releases[i], result.Releases[j]
		if a.Seeders != b.Seeders {
			return a.Seeders > b.Seeders
		}
		return priority[a.Indexer] < priority[b.Indexer]
	})

	if len(result.Errors) == 0 {
		result.Errors = nil
	}
	return result, nil
}
