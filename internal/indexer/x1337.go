package indexer

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/tristenlammi/arrmada/internal/flaresolverr"
)

// X1337Searcher is a native 1337x integration. 1337x is a public site (no login,
// no ratio) behind Cloudflare with no API, so we scrape its HTML through
// FlareSolverr and hand qBittorrent a magnet link (nothing to seed).
type X1337Searcher struct {
	fs   *flaresolverr.Client
	http *http.Client

	rateMu  sync.Mutex
	lastReq time.Time
}

// NewX1337Searcher creates the searcher. fs may be nil (works only if 1337x
// isn't actively challenging, which is rare).
func NewX1337Searcher(fs *flaresolverr.Client) *X1337Searcher {
	return &X1337Searcher{fs: fs, http: &http.Client{Timeout: 30 * time.Second}}
}

const (
	x1337DefaultBase = "https://1337x.to"
	x1337Delay       = 1500 * time.Millisecond
)

func (s *X1337Searcher) base(idx Indexer) string {
	if idx.URL != "" {
		return strings.TrimRight(idx.URL, "/")
	}
	return x1337DefaultBase
}

func (s *X1337Searcher) throttle() {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if d := x1337Delay - time.Since(s.lastReq); d > 0 {
		time.Sleep(d)
	}
	s.lastReq = time.Now()
}

// getHTML fetches a page's HTML, via FlareSolverr when configured.
func (s *X1337Searcher) getHTML(ctx context.Context, pageURL string) (string, error) {
	s.throttle()
	if s.fs != nil {
		sol, err := s.fs.Get(ctx, pageURL)
		if err != nil {
			return "", err
		}
		return sol.Response, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", tlUserAgent)
	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b := make([]byte, 0)
	buf := make([]byte, 32*1024)
	for {
		n, e := resp.Body.Read(buf)
		b = append(b, buf[:n]...)
		if e != nil || len(b) > 8<<20 {
			break
		}
	}
	return string(b), nil
}

// Test fetches the trending page to confirm the site is reachable.
func (s *X1337Searcher) Test(ctx context.Context, idx Indexer) error {
	html, err := s.getHTML(ctx, s.base(idx)+"/trending")
	if err != nil {
		return fmt.Errorf("1337x: %w", err)
	}
	if !strings.Contains(strings.ToLower(html), "torrent") {
		return fmt.Errorf("1337x: unexpected response (Cloudflare?)")
	}
	return nil
}

// Search scrapes the 1337x search results page.
func (s *X1337Searcher) Search(ctx context.Context, idx Indexer, q SearchQuery) ([]Release, error) {
	term := strings.TrimSpace(q.Text)
	if term == "" {
		return nil, nil // 1337x has no meaningful browse-all for our purposes
	}
	base := s.base(idx)
	pageURL := base + "/search/" + url.PathEscape(term) + "/1/"

	html, err := s.getHTML(ctx, pageURL)
	if err != nil {
		return nil, fmt.Errorf("1337x: %w", err)
	}
	releases := parseX1337Results(base, html)
	for i := range releases {
		releases[i].Indexer = idx.Name
		releases[i].Transport = TransportTorrent
	}
	return releases, nil
}

// Recent scrapes 1337x's latest-Movies listing (the same table layout as search
// results) so RSS sync can catch new uploads without a title query.
func (s *X1337Searcher) Recent(ctx context.Context, idx Indexer, limit int) ([]Release, error) {
	base := s.base(idx)
	html, err := s.getHTML(ctx, base+"/cat/Movies/1/")
	if err != nil {
		return nil, fmt.Errorf("1337x: %w", err)
	}
	releases := parseX1337Results(base, html)
	for i := range releases {
		releases[i].Indexer = idx.Name
		releases[i].Transport = TransportTorrent
	}
	if limit > 0 && len(releases) > limit {
		releases = releases[:limit]
	}
	return releases, nil
}

// Fetch scrapes the release's detail page for its magnet link.
func (s *X1337Searcher) Fetch(ctx context.Context, idx Indexer, detailURL string) (FetchResult, error) {
	html, err := s.getHTML(ctx, detailURL)
	if err != nil {
		return FetchResult{}, fmt.Errorf("1337x: %w", err)
	}
	magnet := parseX1337Magnet(html)
	if magnet == "" {
		return FetchResult{}, fmt.Errorf("1337x: no magnet link found on the page")
	}
	return FetchResult{URL: magnet}, nil
}

var reX1337Size = regexp.MustCompile(`(?i)([\d.]+)\s*(TB|GB|MB|KB|B)`)

// parseX1337Results parses a 1337x search-results HTML page into releases.
func parseX1337Results(base, html string) []Release {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil
	}
	var out []Release
	doc.Find("table.table-list tbody tr").Each(func(_ int, row *goquery.Selection) {
		var title, href string
		row.Find("td.name a").Each(func(_ int, a *goquery.Selection) {
			h, _ := a.Attr("href")
			if strings.HasPrefix(h, "/torrent/") {
				title = strings.TrimSpace(a.Text())
				href = h
			}
		})
		if href == "" {
			return
		}
		out = append(out, Release{
			Title:       title,
			DownloadURL: base + href,
			Seeders:     parseInt(row.Find("td.seeds").Text()),
			Peers:       parseInt(row.Find("td.leeches").Text()),
			SizeBytes:   parseX1337Size(row.Find("td.size").Text()),
		})
	})
	return out
}

// parseX1337Magnet extracts the magnet link from a detail page.
func parseX1337Magnet(html string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return ""
	}
	magnet := ""
	doc.Find(`a[href^="magnet:"]`).EachWithBreak(func(_ int, a *goquery.Selection) bool {
		if h, ok := a.Attr("href"); ok {
			magnet = h
			return false
		}
		return true
	})
	return magnet
}

func parseX1337Size(s string) int64 {
	m := reX1337Size.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	val, _ := strconv.ParseFloat(m[1], 64)
	mult := map[string]float64{"B": 1, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40}[strings.ToUpper(m[2])]
	return int64(val * mult)
}

func parseInt(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(strings.ReplaceAll(s, ",", "")))
	return n
}
