package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
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
	recent   recentCache
	// unknownKindLogged remembers which unknown searcher kinds have been warned
	// about, so the RSS sweep doesn't repeat the warning every cycle.
	unknownKindLogged sync.Map
}

// recentTTL is how long an RSS feed pull is reused. The movie, series and book RSS
// sweeps each call Recent independently and fire within seconds of each other, so every
// cycle pulled the identical unfiltered feed from every indexer three times over. A
// window this short cannot delay a new release noticeably — the sweeps themselves run
// minutes apart — but it collapses the burst into one fetch per indexer.
const recentTTL = 60 * time.Second

// recentCache memoizes the last feed pull. The mutex is deliberately held across the
// fetch: a second sweep arriving mid-pull should wait and share the result rather than
// start a duplicate request, which is the whole point.
type recentCache struct {
	mu    sync.Mutex
	at    time.Time
	limit int
	res   SearchResult
}

// fresh reports whether the cached feed still stands in for a pull of this size.
// A different limit is a different feed, so it never matches.
//
// The hit is a copy of the slice header: three sweeps now share one backing array, and
// a caller appending to its own result must not reach into what the next one reads.
func (c *recentCache) fresh(limit int) (SearchResult, bool) {
	if c.at.IsZero() || c.limit != limit || time.Since(c.at) >= recentTTL {
		return SearchResult{}, false
	}
	return SearchResult{Releases: append([]Release(nil), c.res.Releases...), Errors: copyErrors(c.res.Errors)}, true
}

// copyErrors clones a per-indexer error map so callers can't mutate the cached one.
func copyErrors(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// NewService wires a Service over the database. flaresolverrURL may be empty
// (no Cloudflare solving).
func NewService(db *sql.DB, log *slog.Logger, flaresolverrURL string) *Service {
	var fs *flaresolverr.Client
	if flaresolverrURL != "" {
		fs = flaresolverr.New(flaresolverrURL)
	}
	s := &Service{repo: NewRepo(db), registry: NewRegistry(fs), log: log}
	s.registry.SetLogger(log) // per-page request tracing
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
	// Usenet (newznab): the download client fetches the NZB URL itself.
	if found.Transport() == TransportUsenet {
		return FetchResult{URL: downloadURL}, nil
	}
	// Torrent-transport URLs (torznab) are resolved server-side so downstream
	// gets an infohash-bearing payload (.torrent bytes or a magnet) instead of a
	// bare URL — stall detection, seed cleanup and import matching all depend on
	// the infohash. On any failure the URL is passed through as before, so a
	// flaky tracker can never break the grab itself.
	res, err := fetchTorrentPayload(ctx, downloadURL)
	if err != nil {
		s.log.Warn("indexer fetch: could not resolve torrent URL server-side; passing URL through",
			"indexer", indexerName, "url", redactKey(downloadURL), "err", err)
		return FetchResult{URL: downloadURL}, nil
	}
	return res, nil
}

// fetchTorrentTimeout bounds the server-side resolution of a torrent URL. Grabs
// are user-facing, so a hung tracker should fall back to URL passthrough quickly.
const fetchTorrentTimeout = 30 * time.Second

// fetchTorrentPayload HTTP-GETs a torrent-transport download URL, following
// redirects. A redirect to a magnet: URI is captured and returned as a magnet;
// a bencoded body (every .torrent starts with a 'd'-prefixed dictionary) is
// returned as file bytes. Anything else is an error, and the caller falls back
// to handing the client the raw URL.
func fetchTorrentPayload(ctx context.Context, downloadURL string) (FetchResult, error) {
	if strings.HasPrefix(downloadURL, "magnet:") {
		return FetchResult{URL: downloadURL}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTorrentTimeout)
	defer cancel()

	var magnet string
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Torznab grab endpoints commonly 302 to a magnet URI; the transport
			// can't follow that scheme, so capture it and stop.
			if req.URL.Scheme == "magnet" {
				magnet = req.URL.String()
				return http.ErrUseLastResponse
			}
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return FetchResult{}, sanitizeErr(downloadURL, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		if magnet != "" {
			return FetchResult{URL: magnet}, nil
		}
		return FetchResult{}, sanitizeErr(downloadURL, err)
	}
	defer resp.Body.Close()
	if magnet != "" {
		return FetchResult{URL: magnet}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return FetchResult{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return FetchResult{}, sanitizeErr(downloadURL, err)
	}
	if !looksLikeTorrent(body) {
		return FetchResult{}, errors.New("response body is not a bencoded torrent")
	}
	return FetchResult{File: body, Filename: torrentFilename(downloadURL)}, nil
}

// looksLikeTorrent reports whether data starts like a bencoded dictionary —
// every .torrent begins with 'd' followed by a length-prefixed key ("d8:announce…").
func looksLikeTorrent(data []byte) bool {
	return len(data) >= 2 && data[0] == 'd' && data[1] >= '0' && data[1] <= '9'
}

// torrentFilename derives a .torrent filename from the download URL's path.
func torrentFilename(downloadURL string) string {
	filename := "arrmada.torrent"
	if u, err := url.Parse(downloadURL); err == nil {
		if b := path.Base(u.Path); b != "" && b != "." && b != "/" {
			filename = b
		}
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".torrent") {
		filename += ".torrent"
	}
	return filename
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
//
// Results are shared between callers for recentTTL — see recentCache. Failures are not
// cached, so a transient indexer error doesn't suppress the next sweep's attempt.
func (s *Service) Recent(ctx context.Context, limit int) (SearchResult, error) {
	s.recent.mu.Lock()
	defer s.recent.mu.Unlock()
	if cached, ok := s.recent.fresh(limit); ok {
		return cached, nil
	}
	res, err := s.fetchRecent(ctx, limit)
	if err != nil {
		return res, err
	}
	// Cache a private copy (slice header AND errors map) so a caller mutating its
	// result can't clobber the cached run for whoever reads it next.
	s.recent.at, s.recent.limit = time.Now(), limit
	s.recent.res = SearchResult{Releases: append([]Release(nil), res.Releases...), Errors: copyErrors(res.Errors)}
	return res, nil
}

func (s *Service) fetchRecent(ctx context.Context, limit int) (SearchResult, error) {
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
			// Warn once per kind: silently skipping made a misconfigured indexer
			// invisible to RSS sync forever.
			if _, logged := s.unknownKindLogged.LoadOrStore(idx.Kind, true); !logged {
				s.log.Warn("indexer recent: no searcher for kind; skipping",
					"indexer", idx.Name, "kind", idx.Kind, "err", err)
			}
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
			// A child deadline per indexer: one hung feed must not consume the
			// whole sweep's budget while every other result waits on wg.Wait.
			ictx, cancel := context.WithTimeout(ctx, perIndexerTimeout)
			defer cancel()
			releases, err := rec.Recent(ictx, idx, limit)
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

// perIndexerTimeout caps each indexer goroutine's share of a fan-out. The fan-out
// as a whole keeps its 45s budget, but wg.Wait blocks on the slowest indexer — so
// without a per-indexer cap one hung endpoint consumed the entire budget for
// everyone. Its timeout error lands in the per-indexer Errors map as usual.
const perIndexerTimeout = 25 * time.Second

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
			// A child deadline per indexer: one hung indexer must not consume the
			// whole search's budget while every other result waits on wg.Wait.
			ictx, cancel := context.WithTimeout(ctx, perIndexerTimeout)
			defer cancel()

			searcher, err := s.registry.For(idx.Kind)
			if err == nil {
				var releases []Release
				releases, err = searcher.Search(ictx, idx, q)
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

	// The same torrent often comes back from several indexers; collapse those
	// duplicates by infohash so the ranked list shows each release once.
	result.Releases = dedupeByInfoHash(result.Releases, priority)

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

// dedupeByInfoHash collapses releases sharing a non-empty infohash, keeping the
// better copy: more seeders, ties broken by higher indexer priority (lower
// number). Releases without an infohash are never deduped — an empty hash says
// nothing about identity.
func dedupeByInfoHash(releases []Release, priority map[string]int) []Release {
	byHash := make(map[string]int, len(releases)) // infohash -> index in out
	out := make([]Release, 0, len(releases))
	for _, r := range releases {
		h := strings.ToLower(strings.TrimSpace(r.InfoHash))
		if h == "" {
			out = append(out, r)
			continue
		}
		i, ok := byHash[h]
		if !ok {
			byHash[h] = len(out)
			out = append(out, r)
			continue
		}
		kept := out[i]
		if r.Seeders > kept.Seeders ||
			(r.Seeders == kept.Seeders && priority[r.Indexer] < priority[kept.Indexer]) {
			out[i] = r
		}
	}
	return out
}
