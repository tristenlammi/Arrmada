package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/flaresolverr"
)

// TorrentLeechSearcher is a native TorrentLeech integration — no Jackett/Prowlarr
// needed. It logs in with the user's credentials, searches the browse JSON API,
// and builds .torrent download links. When a FlareSolverr client is supplied it
// uses it to get past Cloudflare (the cf_clearance cookie + matching User-Agent),
// then makes direct requests carrying that clearance.
//
// Config on the Indexer: Username/Password (required); APIKey = optional RSS key
// for cookie-less download URLs; Categories = optional (defaults to Movies + TV).
type TorrentLeechSearcher struct {
	fs *flaresolverr.Client

	sessMu   sync.Mutex
	sessions map[int64]*tlSession

	rateMu  sync.Mutex
	lastReq time.Time
}

type tlSession struct {
	client *http.Client
	ua     string
}

// NewTorrentLeechSearcher creates the searcher. fs may be nil (no Cloudflare
// solving; works when TorrentLeech isn't actively challenging).
func NewTorrentLeechSearcher(fs *flaresolverr.Client) *TorrentLeechSearcher {
	return &TorrentLeechSearcher{fs: fs, sessions: map[int64]*tlSession{}}
}

const (
	tlBaseURL      = "https://www.torrentleech.org"
	tlUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	tlRequestDelay = 4200 * time.Millisecond // Cloudflare rate-limit guard
	tlDefaultCats  = "8,9,11,12,13,14,15,29,36,37,43,47,26,27,32,34,35,44"
)

var reReqPrefix = regexp.MustCompile(`^\[REQ(?:UEST(?:ED)?)?\]\s*`)

// throttle reserves the next request slot under the lock, then waits for it
// outside the lock, honouring ctx cancellation. Sleeping while holding rateMu
// used to block every concurrent request — including ones whose context was
// already dead — behind a plain time.Sleep.
func (t *TorrentLeechSearcher) throttle(ctx context.Context) error {
	t.rateMu.Lock()
	prev := t.lastReq
	slot := time.Now()
	if next := prev.Add(tlRequestDelay); next.After(slot) {
		slot = next
	}
	t.lastReq = slot
	t.rateMu.Unlock()

	if err := waitUntil(ctx, slot); err != nil {
		// Give the abandoned slot back if nobody queued behind it.
		t.rateMu.Lock()
		if t.lastReq.Equal(slot) {
			t.lastReq = prev
		}
		t.rateMu.Unlock()
		return err
	}
	return nil
}

// do issues a throttled request through the session, using its User-Agent, and
// returns the response plus the (size-capped) body.
func (t *TorrentLeechSearcher) do(ctx context.Context, sess *tlSession, method, rawurl string, body io.Reader) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawurl, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", sess.ua)
	req.Header.Set("Referer", tlBaseURL+"/")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if err := t.throttle(ctx); err != nil {
		return nil, nil, err
	}
	resp, err := sess.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	return resp, data, err
}

// newSession obtains Cloudflare clearance (if FlareSolverr is configured) and
// logs in, returning a ready-to-use session.
func (t *TorrentLeechSearcher) newSession(ctx context.Context, idx Indexer) (*tlSession, error) {
	if idx.Username == "" || idx.Password == "" {
		return nil, fmt.Errorf("torrentleech: username and password are required")
	}

	jar, _ := cookiejar.New(nil)
	ua := tlUserAgent

	if t.fs != nil {
		sol, err := t.fs.Get(ctx, tlBaseURL+"/")
		if err != nil {
			return nil, fmt.Errorf("torrentleech: %w", err)
		}
		if sol.UserAgent != "" {
			ua = sol.UserAgent
		}
		u, _ := url.Parse(tlBaseURL)
		cookies := make([]*http.Cookie, 0, len(sol.Cookies))
		for _, ck := range sol.Cookies {
			cookies = append(cookies, &http.Cookie{Name: ck.Name, Value: ck.Value})
		}
		jar.SetCookies(u, cookies)
	}

	sess := &tlSession{client: &http.Client{Jar: jar, Timeout: 45 * time.Second}, ua: ua}

	form := url.Values{"username": {idx.Username}, "password": {idx.Password}}
	_, body, err := t.do(ctx, sess, http.MethodPost, tlBaseURL+"/user/account/login/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("torrentleech: login request failed: %w", err)
	}
	html := string(body)

	switch {
	case strings.Contains(html, "/user/account/logout"):
		return sess, nil
	case strings.Contains(html, "One Time Password"):
		return nil, fmt.Errorf("torrentleech: account has 2FA enabled — not supported yet")
	case strings.Contains(html, "text-danger"), strings.Contains(html, "login-form"):
		return nil, fmt.Errorf("torrentleech: login failed — check username/password")
	case t.fs == nil:
		return nil, fmt.Errorf("torrentleech: login blocked (likely Cloudflare) — configure FlareSolverr")
	default:
		return nil, fmt.Errorf("torrentleech: login failed (Cloudflare challenge persisted)")
	}
}

func (t *TorrentLeechSearcher) session(ctx context.Context, idx Indexer) (*tlSession, error) {
	t.sessMu.Lock()
	s := t.sessions[idx.ID]
	t.sessMu.Unlock()
	if s != nil {
		return s, nil
	}
	s, err := t.newSession(ctx, idx)
	if err != nil {
		return nil, err
	}
	t.sessMu.Lock()
	t.sessions[idx.ID] = s
	t.sessMu.Unlock()
	return s, nil
}

func (t *TorrentLeechSearcher) dropSession(id int64) {
	t.sessMu.Lock()
	delete(t.sessions, id)
	t.sessMu.Unlock()
}

// Test verifies credentials (and Cloudflare/FlareSolverr) with a fresh login.
func (t *TorrentLeechSearcher) Test(ctx context.Context, idx Indexer) error {
	_, err := t.newSession(ctx, idx)
	return err
}

// Search runs a keyword search against the browse JSON API.
func (t *TorrentLeechSearcher) Search(ctx context.Context, idx Indexer, q SearchQuery) ([]Release, error) {
	releases, err := t.search(ctx, idx, q)
	if err != nil && strings.Contains(err.Error(), "not logged in") {
		t.dropSession(idx.ID)
		releases, err = t.search(ctx, idx, q)
	}
	return releases, err
}

func (t *TorrentLeechSearcher) search(ctx context.Context, idx Indexer, q SearchQuery) ([]Release, error) {
	sess, err := t.session(ctx, idx)
	if err != nil {
		return nil, err
	}

	cats := tlDefaultCats
	if len(q.Categories) > 0 {
		cats = joinInts(q.Categories)
	} else if len(idx.Categories) > 0 {
		cats = joinInts(idx.Categories)
	}

	var sb strings.Builder
	sb.WriteString(tlBaseURL + "/torrents/browse/list")
	if cats != "" {
		sb.WriteString("/categories/" + cats)
	}
	if term := strings.TrimSpace(q.Text); term != "" {
		sb.WriteString("/exact/1/query/" + url.PathEscape(term))
	} else {
		sb.WriteString("/newfilter/2")
	}
	sb.WriteString("/orderby/added/order/desc")

	resp, body, err := t.do(ctx, sess, http.MethodGet, sb.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("torrentleech: search failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !looksLikeJSON(resp.Header.Get("Content-Type"), body) {
		if strings.Contains(string(body), "login-form") {
			return nil, fmt.Errorf("torrentleech: not logged in")
		}
		return nil, fmt.Errorf("torrentleech: unexpected response (HTTP %d) — Cloudflare or rate limit; try again", resp.StatusCode)
	}
	return t.releasesFromJSON(idx, body)
}

func (t *TorrentLeechSearcher) releasesFromJSON(idx Indexer, body []byte) ([]Release, error) {
	var payload tlResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("torrentleech: parse response: %w", err)
	}
	releases := make([]Release, 0, len(payload.TorrentList))
	for _, it := range payload.TorrentList {
		r := Release{
			Title:       reReqPrefix.ReplaceAllString(strings.TrimSpace(it.Name), ""),
			DownloadURL: t.downloadURL(idx, it.Fid, it.Filename),
			InfoURL:     tlBaseURL + "/torrent/" + it.Fid,
			SizeBytes:   it.Size,
			Seeders:     it.Seeders,
			Peers:       it.Leechers,
			Indexer:     idx.Name,
			Transport:   TransportTorrent,
		}
		if it.CategoryID > 0 {
			r.Categories = []int{it.CategoryID}
		}
		if ts, e := time.Parse("2006-01-02 15:04:05", it.AddedTimestamp); e == nil {
			r.PublishedAt = ts
		}
		releases = append(releases, r)
	}
	return releases, nil
}

// Fetch downloads a release's .torrent bytes through the authenticated session
// (which carries the Cloudflare clearance).
func (t *TorrentLeechSearcher) Fetch(ctx context.Context, idx Indexer, downloadURL string) (FetchResult, error) {
	sess, err := t.session(ctx, idx)
	if err != nil {
		return FetchResult{}, err
	}
	resp, data, err := t.do(ctx, sess, http.MethodGet, downloadURL, nil)
	if err != nil {
		return FetchResult{}, fmt.Errorf("torrentleech: download failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := strings.TrimSpace(string(data))
		if len(snippet) > 140 {
			snippet = snippet[:140]
		}
		return FetchResult{}, fmt.Errorf("torrentleech: download HTTP %d %s", resp.StatusCode, snippet)
	}
	// A .torrent is bencoded (starts with 'd'); HTML means a challenge/login page.
	if len(data) == 0 || data[0] == '<' {
		t.dropSession(idx.ID)
		if t.fs == nil {
			return FetchResult{}, fmt.Errorf("torrentleech: download blocked (likely Cloudflare) — configure FlareSolverr")
		}
		return FetchResult{}, fmt.Errorf("torrentleech: didn't get a torrent (Cloudflare/session) — retry")
	}

	filename := "arrmada.torrent"
	if u, e := url.Parse(downloadURL); e == nil {
		if b := path.Base(u.Path); b != "" && b != "." && b != "/" {
			filename = b
		}
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".torrent") {
		filename += ".torrent"
	}
	return FetchResult{File: data, Filename: filename}, nil
}

// downloadURL builds the session-authenticated .torrent link. With FlareSolverr
// handling Cloudflare + login, this works without an RSS key (like Prowlarr).
func (t *TorrentLeechSearcher) downloadURL(_ Indexer, fid, filename string) string {
	return fmt.Sprintf("%s/download/%s/%s", tlBaseURL, fid, url.PathEscape(filename))
}

func looksLikeJSON(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "json") {
		return true
	}
	trimmed := strings.TrimSpace(string(body))
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}

type tlResponse struct {
	NumFound    int         `json:"numFound"`
	TorrentList []tlTorrent `json:"torrentList"`
}

type tlTorrent struct {
	Fid            string `json:"fid"`
	Filename       string `json:"filename"`
	Name           string `json:"name"`
	CategoryID     int    `json:"categoryID"`
	Size           int64  `json:"size"`
	Seeders        int    `json:"seeders"`
	Leechers       int    `json:"leechers"`
	Completed      int    `json:"completed"`
	AddedTimestamp string `json:"addedTimestamp"`
	ImdbID         string `json:"imdbID"`
}
