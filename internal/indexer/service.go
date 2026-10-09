package indexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/connstatus"
	"github.com/tristenlammi/arrmada/internal/flaresolverr"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/safego"
)

// Service manages configured indexers and runs aggregated searches across them.
type Service struct {
	repo     *Repo
	registry *Registry
	log      *slog.Logger
	recent   recentCache
	// status remembers how each indexer has been answering and pauses a failing one for
	// background work (see connstatus). nil records nothing and pauses nothing.
	status *connstatus.Tracker
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
	return SearchResult{Releases: append([]Release(nil), c.res.Releases...), Errors: copyErrors(c.res.Errors), Skipped: copyErrors(c.res.Skipped)}, true
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

// NewService wires a Service over the database. fs is the FlareSolverr client, whose URL
// is read on every use; nil (or one with no URL set) means no Cloudflare solving.
func NewService(db *sql.DB, log *slog.Logger, fs *flaresolverr.Client) *Service {
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
	// A TorrentLeech login keeps going after the search that started it gives up; when it
	// ends with nobody waiting, its outcome is recorded here so the row shows it. It
	// doesn't move the backoff: the searcher paces its own logins.
	s.registry.SetLoginObserver(func(idx Indexer, err error) {
		s.record(context.Background(), idx, err, 0, false)
	})
	return s
}

// SetStatus wires the integration status tracker. Without one (nil) no indexer is ever
// skipped and nothing is recorded, as before.
func (s *Service) SetStatus(t *connstatus.Tracker) { s.status = t }

// Status returns what the tracker knows about one indexer, and its last 24 hours of use.
// ok is false when the indexer hasn't been asked since it was added or last edited.
func (s *Service) Status(id int64) (st connstatus.State, counts connstatus.Counts, ok bool) {
	st, ok = s.status.Get(connstatus.KindIndexer, statusRef(id))
	return st, s.status.Counts24h(connstatus.KindIndexer, statusRef(id)), ok
}

// statusRef is an indexer's id in the status tracker.
func statusRef(id int64) string { return strconv.FormatInt(id, 10) }

// allow says whether this search may ask idx. When not — background work, and the
// indexer is backing off after repeated failures — reason is what SearchResult.Skipped
// says about it: "paused until 15:00 after 3 failures: login failed".
func (s *Service) allow(ctx context.Context, idx Indexer) (ok bool, reason string) {
	ok, st := s.status.Allow(connstatus.KindIndexer, statusRef(idx.ID), IsInteractive(ctx))
	if ok {
		return true, ""
	}
	reason = fmt.Sprintf("paused until %s after %d failures", st.BackoffUntil.Local().Format("15:04"), st.ConsecutiveFailures)
	if st.LastError != "" {
		reason += ": " + st.LastError
	}
	return false, reason
}

// record notes how one indexer's part of a search went. caller is the context the search
// was called with: when it is done (the person closed the release modal, Arrmada is
// shutting down), a failure says nothing about the indexer and is dropped. The
// per-indexer deadline is not the caller's, so an indexer that hangs past it does count.
// backoff is false for a Test: its failure shows on the row but pauses nothing.
func (s *Service) record(caller context.Context, idx Indexer, err error, dur time.Duration, backoff bool) {
	if s.status == nil || (err != nil && caller.Err() != nil) {
		return
	}
	o := connstatus.Outcome{Err: err, Dur: dur, Backoff: backoff, Name: idx.Name}
	var he *HTTPStatusError
	if errors.As(err, &he) {
		o.RetryAfter = he.RetryAfter
	}
	s.status.Record(connstatus.KindIndexer, statusRef(idx.ID), o)
}

// resetStatus forgets an indexer's failures and drops any session a native searcher
// holds for it, after its settings changed (forget=false) or it was deleted (forget=true).
func (s *Service) resetStatus(ctx context.Context, id int64, forget bool) {
	s.registry.Reset(id)
	var err error
	if forget {
		err = s.status.Forget(ctx, connstatus.KindIndexer, statusRef(id))
	} else {
		err = s.status.Reset(ctx, connstatus.KindIndexer, statusRef(id))
	}
	if err != nil {
		s.log.Warn("indexer: couldn't clear its saved status", "id", id, "err", err)
	}
}

// connectionChanged reports whether an edit touched how Arrmada reaches the indexer — its
// kind, address, login or key, or whether it's on at all. Only then are its old failures
// beside the point; scoping it to other media (the row's pills) or changing its seed
// rules leaves its health alone. A blank key or password means "keep", so only a new one
// counts.
func connectionChanged(old, upd Indexer) bool {
	return old.Kind != upd.Kind || old.URL != upd.URL || old.Username != upd.Username ||
		(upd.APIKey != "" && upd.APIKey != old.APIKey) ||
		(upd.Password != "" && upd.Password != old.Password) ||
		old.Enabled != upd.Enabled
}

// List returns all configured indexers.
func (s *Service) List(ctx context.Context) ([]Indexer, error) { return s.repo.List(ctx) }

// Get returns one indexer.
func (s *Service) Get(ctx context.Context, id int64) (Indexer, error) { return s.repo.Get(ctx, id) }

// Create stores a new indexer.
func (s *Service) Create(ctx context.Context, idx Indexer) (Indexer, error) {
	return s.repo.Create(ctx, idx)
}

// Update changes an indexer's settings. A change to how it's reached clears its recorded
// failures and any cached login, so the new settings get a fair first try.
func (s *Service) Update(ctx context.Context, idx Indexer) error {
	old, oldErr := s.repo.Get(ctx, idx.ID)
	if err := s.repo.Update(ctx, idx); err != nil {
		return err
	}
	if oldErr != nil || connectionChanged(old, idx) {
		s.resetStatus(ctx, idx.ID, false)
	}
	// What the old address said it supports says nothing about the new one.
	if oldErr == nil && (old.URL != idx.URL || old.Kind != idx.Kind) && old.CapsJSON != "" {
		if _, err := s.repo.db.ExecContext(ctx, `UPDATE indexers SET caps_json='', caps_at=NULL WHERE id=?`, idx.ID); err != nil {
			s.log.Warn("indexer: couldn't clear its old capabilities", "id", idx.ID, "err", err)
		}
	}
	return nil
}

// Delete removes an indexer, its recorded status and any cached login.
func (s *Service) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	s.resetStatus(ctx, id, true)
	return nil
}

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
	// A Test shows on the row like any search: a pass clears a backoff, and a failure is
	// recorded without moving the backoff ladder — pressing Test shouldn't pause anything.
	start := time.Now()
	caps, err := testWith(ctx, searcher, idx)
	s.record(ctx, idx, err, time.Since(start), false)
	if err == nil && caps != nil {
		s.saveCaps(ctx, idx, *caps)
	}
	return err
}

// testWith runs a searcher's Test, reading capabilities too when it can (caps is nil
// when it can't).
func testWith(ctx context.Context, searcher Searcher, idx Indexer) (*Caps, error) {
	if ct, ok := searcher.(CapsTester); ok {
		c, err := ct.TestCaps(ctx, idx)
		if err != nil {
			return nil, err
		}
		return &c, nil
	}
	return nil, searcher.Test(ctx, idx)
}

// saveCaps stores what an indexer said it supports. A failure only costs the row its
// summary line, so it's logged rather than failing the Test.
func (s *Service) saveCaps(ctx context.Context, idx Indexer, c Caps) {
	b, err := json.Marshal(c)
	if err == nil {
		err = s.repo.SetCaps(ctx, idx.ID, string(b))
	}
	if err != nil {
		s.log.Warn("indexer: couldn't save its capabilities", "indexer", idx.Name, "err", err)
	}
}

// TestSettings tests settings that may not be saved — the Add form, or an edit before
// Save — and returns what the indexer supports (a summary line; "" for kinds that don't
// say). Nothing is stored or recorded: the row, its status and its secrets stay as they
// are, and a key typed for the test is used for this one request only. idx.ID is 0 unless
// the caller deliberately keeps it (see the handler).
func (s *Service) TestSettings(ctx context.Context, idx Indexer) (string, error) {
	searcher, err := s.registry.For(idx.Kind)
	if err != nil {
		return "", err
	}
	caps, err := testWith(ctx, searcher, idx)
	if err != nil || caps == nil {
		return "", err
	}
	return caps.Summary(), nil
}

// capsRefreshAge is how old an indexer's stored capabilities may get before the daily
// refresh asks again.
const capsRefreshAge = 7 * 24 * time.Hour

// RefreshCaps reads the capabilities of the given indexers (Torznab ones; others say
// nothing), e.g. the rows a Prowlarr sync just added. Each request goes through the
// searcher's usual per-host throttle; an indexer that's backing off is left alone, and a
// failure is logged and skipped — the next Test or refresh tries again.
func (s *Service) RefreshCaps(ctx context.Context, ids []int64) error {
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		idx, err := s.repo.Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		s.refreshCaps(ctx, idx)
	}
	return nil
}

// RefreshStaleCaps is the daily indexer-caps-refresh task: every enabled Torznab indexer
// whose capabilities were never read, or were read over a week ago, is asked again.
func (s *Service) RefreshStaleCaps(ctx context.Context) error {
	list, err := s.repo.ListEnabled(ctx)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-capsRefreshAge)
	for _, idx := range list {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if idx.Kind != KindTorznab || (idx.CapsJSON != "" && idx.CapsAt.After(cutoff)) {
			continue
		}
		s.refreshCaps(ctx, idx)
	}
	return nil
}

// refreshCaps reads and stores one indexer's capabilities.
func (s *Service) refreshCaps(ctx context.Context, idx Indexer) {
	searcher, err := s.registry.For(idx.Kind)
	if err != nil {
		return
	}
	ct, ok := searcher.(CapsTester)
	if !ok {
		return
	}
	if ok, _ := s.allow(ctx, idx); !ok {
		return
	}
	ictx, cancel := context.WithTimeout(ctx, perIndexerTimeout)
	defer cancel()
	c, err := ct.TestCaps(ictx, idx)
	if err != nil {
		s.log.Info("indexer: couldn't read its capabilities", "indexer", idx.Name, "err", err)
		return
	}
	s.saveCaps(ctx, idx, c)
}

// SearchResult bundles aggregated releases with per-indexer errors so a single
// dead indexer never sinks the whole search.
type SearchResult struct {
	Releases []Release         `json:"releases"`
	Errors   map[string]string `json:"errors,omitempty"` // indexer name -> error
	// Skipped names the indexers this background search left alone because they are
	// backing off after repeated failures, with why: "paused until 15:00 after 3
	// failures: login failed". A person's own search never skips one.
	Skipped map[string]string `json:"skipped,omitempty"`
	// Asked is how many indexers the search went to, so a caller can tell "every indexer
	// failed" from "one failed and the rest found nothing". Set by Search only.
	Asked int `json:"-"`
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
	s.recent.res = SearchResult{Releases: append([]Release(nil), res.Releases...), Errors: copyErrors(res.Errors), Skipped: copyErrors(res.Skipped)}
	return res, nil
}

func (s *Service) fetchRecent(ctx context.Context, limit int) (SearchResult, error) {
	indexers, err := s.repo.ListEnabled(ctx)
	if err != nil {
		return SearchResult{}, err
	}
	caller := ctx
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		result   = SearchResult{Errors: map[string]string{}, Skipped: map[string]string{}}
		priority = map[string]int{}
		eligible int
		failed   int
		skipped  int
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
		eligible++
		if ok, why := s.allow(ctx, idx); !ok {
			result.Skipped[idx.Name] = why
			skipped++
			continue
		}
		wg.Add(1)
		go func(idx Indexer, rec Recenter) {
			defer wg.Done()
			// A child deadline per indexer: one hung feed must not consume the
			// whole sweep's budget while every other result waits on wg.Wait.
			ictx, cancel := context.WithTimeout(ctx, perIndexerTimeout)
			defer cancel()
			// A panic in one indexer's parser becomes that indexer's error; the others'
			// results still come back.
			var releases []Release
			start := time.Now()
			err := safego.Call(s.log, "indexer recent "+idx.Name, func() error {
				var e error
				releases, e = rec.Recent(ictx, idx, limit)
				return e
			})
			s.record(caller, idx, err, time.Since(start), true)
			if err != nil {
				mu.Lock()
				result.Errors[idx.Name] = err.Error()
				failed++
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
	// Every feed failing (or paused) is an outage, not a quiet hour. Returned as an error
	// so Recent doesn't cache it and the next sweep asks again.
	err = outcome(eligible, failed, skipped, result.Errors, result.Skipped)
	if len(result.Errors) == 0 {
		result.Errors = nil
	}
	if len(result.Skipped) == 0 {
		result.Skipped = nil
	}
	return result, err
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

	caller := ctx
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		result   = SearchResult{Errors: map[string]string{}, Skipped: map[string]string{}}
		priority = map[string]int{}
		eligible int
		failed   int
		skipped  int
	)

	for _, idx := range indexers {
		if !idx.Serves(q.MediaType) {
			continue // this indexer isn't scoped to the media type being searched
		}
		priority[idx.Name] = idx.Priority
		eligible++
		// A background search leaves an indexer that keeps failing alone until its pause
		// runs out, instead of retrying a dead login on every title of every sweep.
		if ok, why := s.allow(ctx, idx); !ok {
			result.Skipped[idx.Name] = why
			skipped++
			continue
		}
		result.Asked++
		wg.Add(1)
		go func(idx Indexer) {
			defer wg.Done()
			// A child deadline per indexer: one hung indexer must not consume the
			// whole search's budget while every other result waits on wg.Wait.
			ictx, cancel := context.WithTimeout(ctx, perIndexerTimeout)
			defer cancel()

			searcher, err := s.registry.For(idx.Kind)
			if err == nil {
				// A panic in one indexer's parser becomes that indexer's error; the others'
				// results still come back.
				var releases []Release
				start := time.Now()
				err = safego.Call(s.log, "indexer search "+idx.Name, func() error {
					var e error
					releases, e = searcher.Search(ictx, idx, q)
					return e
				})
				s.record(caller, idx, err, time.Since(start), true)
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
			failed++
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

	// An empty result only means "nothing found" when someone was asked and answered.
	// One indexer failing while another answers with nothing is still a real miss; only
	// a search where nobody could answer — every indexer failed or is paused — is
	// reported as an error.
	err = outcome(eligible, failed, skipped, result.Errors, result.Skipped)
	if len(result.Errors) == 0 {
		result.Errors = nil
	}
	if len(result.Skipped) == 0 {
		result.Skipped = nil
	}
	return result, err
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
