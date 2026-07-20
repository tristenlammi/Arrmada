package indexer

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Torznab and Newznab share the same RSS-derived XML shape; the only difference
// is the attr namespace prefix (torznab: vs newznab:), which Go's xml package
// ignores when we match on the local name "attr".

type feed struct {
	XMLName xml.Name `xml:"rss"`
	// Torznab reports pagination in <newznab:response offset="0" total="500"/>. Without
	// reading it there's no way to know a result set was truncated — an indexer returning
	// its page size looks identical to one that genuinely had that many matches.
	Response feedResponse `xml:"channel>response"`
	Items    []feedItem   `xml:"channel>item"`
}

type feedResponse struct {
	Offset int `xml:"offset,attr"`
	Total  int `xml:"total,attr"`
}

type feedItem struct {
	Title       string        `xml:"title"`
	Description string        `xml:"description"`
	Link        string        `xml:"link"`
	Comments    string        `xml:"comments"`
	GUID        string        `xml:"guid"`
	PubDate     string        `xml:"pubDate"`
	Size        int64         `xml:"size"`
	Enclosure   feedEnclosure `xml:"enclosure"`
	Attrs       []feedAttr    `xml:"attr"`
}

type feedEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type feedAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// ParseFeed converts a Torznab/Newznab XML response body into releases.
func ParseFeed(data []byte) ([]Release, error) {
	releases, _, err := parseFeedPage(data)
	return releases, err
}

// parseFeedPage also returns the total the indexer claims to have, so Search knows whether
// more pages exist.
func parseFeedPage(data []byte) ([]Release, int, error) {
	var f feed
	if err := xml.Unmarshal(data, &f); err != nil {
		return nil, 0, fmt.Errorf("parse feed: %w", err)
	}

	releases := make([]Release, 0, len(f.Items))
	for _, it := range f.Items {
		r := Release{
			Title:       it.Title,
			Description: it.Description,
			DownloadURL: firstNonEmpty(it.Enclosure.URL, it.Link),
			InfoURL:     firstURL(it.Comments, it.GUID),
			SizeBytes:   it.Size,
		}
		if t, ok := parseFeedDate(it.PubDate); ok {
			r.PublishedAt = t
		}
		for _, a := range it.Attrs {
			switch strings.ToLower(a.Name) {
			case "seeders":
				r.Seeders = atoi(a.Value)
			case "peers":
				r.Peers = atoi(a.Value)
			case "infohash":
				r.InfoHash = strings.ToLower(a.Value)
			case "size":
				if r.SizeBytes == 0 {
					r.SizeBytes = atoi64(a.Value)
				}
			case "category":
				if n := atoi(a.Value); n > 0 {
					r.Categories = append(r.Categories, n)
				}
			}
		}
		releases = append(releases, r)
	}
	return releases, f.Response.Total, nil
}

// torznabRequestDelay is the minimum gap between requests to the SAME host. The
// scraped trackers (TorrentLeech, 1337x) have always throttled; torznab did not, which
// is what earns HTTP 429s once many series are monitored — every indexer behind one
// Prowlarr shares that host, so a burst of per-series searches hits it all at once.
const torznabRequestDelay = 1000 * time.Millisecond

// TorznabSearcher queries Torznab/Newznab endpoints over HTTP. It implements
// Searcher for both the KindTorznab and KindNewznab kinds.
type TorznabSearcher struct {
	log  *slog.Logger
	http *http.Client
	mu   sync.Mutex
	// next[host] is the earliest time the next request to that host may start.
	// Keyed by host so separate trackers don't queue behind each other.
	next map[string]time.Time
}

// NewTorznabSearcher returns a searcher with a sane request timeout.
func NewTorznabSearcher() *TorznabSearcher {
	return &TorznabSearcher{
		http: &http.Client{Timeout: 30 * time.Second},
		next: map[string]time.Time{},
	}
}

// SetLogger attaches a logger so each request page can be traced. Optional — the searcher
// works without one.
func (c *TorznabSearcher) SetLogger(l *slog.Logger) { c.log = l }

// throttle reserves this request's slot for the endpoint's host and waits for it.
// The slot is claimed under the lock but slept for outside it, so concurrent callers
// to one host queue in order while other hosts proceed in parallel.
func (c *TorznabSearcher) throttle(ctx context.Context, endpoint string) error {
	host := endpoint
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		host = u.Host
	}

	c.mu.Lock()
	slot := time.Now()
	if next, ok := c.next[host]; ok && next.After(slot) {
		slot = next
	}
	c.next[host] = slot.Add(torznabRequestDelay)
	c.mu.Unlock()

	wait := time.Until(slot)
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Search runs a text search against a single indexer and returns its releases,
// stamped with the indexer's name and transport.
// maxSearchPages bounds pagination so a misbehaving indexer — one that ignores offset, or
// reports a total it never delivers — can't spin forever.
const maxSearchPages = 10

// Search fetches results, following Torznab pagination until it has what was asked for.
//
// Indexers commonly cap a response at their own page size regardless of the requested
// limit: asking TorrentLeech for 400 returns 35, and without paging the other 400+ matches
// are simply invisible. A show's season packs can be entirely absent for this reason while
// being plainly present on the tracker.
func (c *TorznabSearcher) Search(ctx context.Context, idx Indexer, q SearchQuery) ([]Release, error) {
	var all []Release
	seen := map[string]bool{}

	for page := 0; page < maxSearchPages; page++ {
		endpoint, err := buildURL(idx, "search", q)
		if err != nil {
			return nil, err
		}
		body, err := c.get(ctx, endpoint)
		if err != nil {
			return nil, fmt.Errorf("indexer %q: %w", idx.Name, err)
		}
		releases, total, err := parseFeedPage(body)
		if err != nil {
			return nil, fmt.Errorf("indexer %q: %w", idx.Name, err)
		}
		// Per-page trace. The aggregate count can't show whether paging happened, what
		// categories were asked for, or what total the indexer declared — all of which
		// decide whether a missing release was never offered or never requested.
		if c.log != nil {
			c.log.Info("torznab page", "indexer", idx.Name, "page", page,
				"offset", q.Offset, "items", len(releases), "declared_total", total,
				"url", redactKey(endpoint))
		}

		added := 0
		for _, r := range releases {
			// Dedupe across pages: some indexers repeat items when offset lands oddly.
			key := r.DownloadURL
			if key == "" {
				key = r.Title
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			r.Indexer = idx.Name
			r.Transport = idx.Transport()
			all = append(all, r)
			added++
		}

		// Stop when the page was empty, added nothing new, we have everything the indexer
		// says exists, or we've reached what the caller asked for.
		if added == 0 || len(releases) == 0 ||
			(total > 0 && len(all) >= total) ||
			(q.Limit > 0 && len(all) >= q.Limit) {
			break
		}
		q.Offset += len(releases)
	}
	if q.Limit > 0 && len(all) > q.Limit {
		all = all[:q.Limit]
	}
	return all, nil
}

// Recent fetches the newest releases (Torznab returns the RSS feed when no
// search term is given), for RSS-sync monitoring.
func (c *TorznabSearcher) Recent(ctx context.Context, idx Indexer, limit int) ([]Release, error) {
	return c.Search(ctx, idx, SearchQuery{Limit: limit})
}

// Test performs a capabilities query to verify URL + API key.
func (c *TorznabSearcher) Test(ctx context.Context, idx Indexer) error {
	endpoint, err := buildURL(idx, "caps", SearchQuery{})
	if err != nil {
		return err
	}
	_, err = c.get(ctx, endpoint)
	return err
}

func (c *TorznabSearcher) get(ctx context.Context, endpoint string) ([]byte, error) {
	if err := c.throttle(ctx, endpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func buildURL(idx Indexer, t string, q SearchQuery) (string, error) {
	u, err := url.Parse(idx.URL)
	if err != nil {
		return "", fmt.Errorf("invalid indexer url %q: %w", idx.URL, err)
	}
	qs := u.Query()
	qs.Set("t", searchType(t, q))
	if idx.APIKey != "" {
		qs.Set("apikey", idx.APIKey)
	}
	if q.Text != "" {
		qs.Set("q", q.Text)
	}
	cats := q.Categories
	if len(cats) == 0 {
		cats = idx.Categories
	}
	if len(cats) > 0 {
		qs.Set("cat", joinInts(cats))
	}
	// tvsearch takes the season/episode directly. This is what surfaces season packs: a
	// bare q= returns one capped page of general matches — 35, in the case that prompted
	// this — whereas season=7 asks the indexer for that season and gets its packs.
	if q.Season > 0 {
		qs.Set("season", strconv.Itoa(q.Season))
		if q.Episode > 0 {
			qs.Set("ep", strconv.Itoa(q.Episode))
		}
	}
	if q.Limit > 0 {
		qs.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Offset > 0 {
		qs.Set("offset", strconv.Itoa(q.Offset))
	}
	u.RawQuery = qs.Encode()
	return u.String(), nil
}

// --- small helpers ---

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// firstURL returns the first value that is an http(s) URL — used to pick a
// release's details page (comments/guid), ignoring non-URL guids.
func firstURL(vals ...string) string {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
			return v
		}
	}
	return ""
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, ",")
}

func parseFeedDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// redactKey strips the apikey from a URL so it can be logged safely.
func redactKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparseable>"
	}
	qs := u.Query()
	if qs.Get("apikey") != "" {
		qs.Set("apikey", "REDACTED")
	}
	u.RawQuery = qs.Encode()
	return u.String()
}

// searchType maps a query to the right Torznab endpoint. The generic "search" works
// everywhere but ignores season/episode, so TV queries use tvsearch and movie queries use
// movie — both of which indexers implement with better matching than a plain text search.
func searchType(base string, q SearchQuery) string {
	if base != "search" {
		return base // caps, or an explicit override
	}
	switch q.MediaType {
	case MediaSeries:
		return "tvsearch"
	case MediaMovie:
		return "movie"
	}
	return "search"
}
