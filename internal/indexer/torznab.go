package indexer

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Torznab and Newznab share the same RSS-derived XML shape; the only difference
// is the attr namespace prefix (torznab: vs newznab:), which Go's xml package
// ignores when we match on the local name "attr".

type feed struct {
	XMLName xml.Name   `xml:"rss"`
	Items   []feedItem `xml:"channel>item"`
}

type feedItem struct {
	Title       string      `xml:"title"`
	Description string      `xml:"description"`
	Link        string      `xml:"link"`
	GUID      string        `xml:"guid"`
	PubDate   string        `xml:"pubDate"`
	Size      int64         `xml:"size"`
	Enclosure feedEnclosure `xml:"enclosure"`
	Attrs     []feedAttr    `xml:"attr"`
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
	var f feed
	if err := xml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}

	releases := make([]Release, 0, len(f.Items))
	for _, it := range f.Items {
		r := Release{
			Title:       it.Title,
			Description: it.Description,
			DownloadURL: firstNonEmpty(it.Enclosure.URL, it.Link),
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
	return releases, nil
}

// TorznabSearcher queries Torznab/Newznab endpoints over HTTP. It implements
// Searcher for both the KindTorznab and KindNewznab kinds.
type TorznabSearcher struct {
	http *http.Client
}

// NewTorznabSearcher returns a searcher with a sane request timeout.
func NewTorznabSearcher() *TorznabSearcher {
	return &TorznabSearcher{http: &http.Client{Timeout: 30 * time.Second}}
}

// Search runs a text search against a single indexer and returns its releases,
// stamped with the indexer's name and transport.
func (c *TorznabSearcher) Search(ctx context.Context, idx Indexer, q SearchQuery) ([]Release, error) {
	endpoint, err := buildURL(idx, "search", q)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("indexer %q: %w", idx.Name, err)
	}
	releases, err := ParseFeed(body)
	if err != nil {
		return nil, fmt.Errorf("indexer %q: %w", idx.Name, err)
	}
	for i := range releases {
		releases[i].Indexer = idx.Name
		releases[i].Transport = idx.Transport()
	}
	return releases, nil
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
	qs.Set("t", t)
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
	if q.Limit > 0 {
		qs.Set("limit", strconv.Itoa(q.Limit))
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
