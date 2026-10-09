package indexer

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/tristenlammi/arrmada/internal/safego"
)

// TorrentLeechSearcher is a native TorrentLeech integration — no Jackett/Prowlarr
// needed. It logs in with the user's credentials, searches the browse JSON API,
// and builds .torrent download links. When a FlareSolverr client is supplied it
// uses it to get past Cloudflare (the cf_clearance cookie + matching User-Agent),
// then makes direct requests carrying that clearance.
//
// Config on the Indexer: Username/Password (required); APIKey = optional RSS key
// for cookie-less download URLs; Categories = optional (defaults to Movies + TV).
//
// Logins are the risky part: a private tracker can flag an account that logs in over and
// over. So one login per indexer runs at a time and every search that needs it waits for
// that one; a failed login pauses further logins on a ladder (see failLocked); and a
// session that stops working is dropped and replaced once, not kept until a restart.
type TorrentLeechSearcher struct {
	fs *flaresolverr.Client
	// base is TorrentLeech's address and delay the gap between requests; tests point them
	// at a stand-in.
	base  string
	delay time.Duration
	now   func() time.Time

	sessMu   sync.Mutex
	sessions map[int64]*tlSession
	logins   map[int64]*tlLogin   // the login under way, shared by every search waiting on it
	backoff  map[int64]*tlBackoff // failed logins: no new one until it runs out

	// onLogin hears how a login ended when no search was left waiting for it (they all
	// gave up first), so its outcome still reaches the indexer's status.
	onLogin func(idx Indexer, err error)

	rateMu  sync.Mutex
	lastReq time.Time
}

type tlSession struct {
	client *http.Client
	ua     string
}

// tlLogin is one login in flight. sess and err are set before done is closed.
type tlLogin struct {
	done    chan struct{}
	sess    *tlSession
	err     error
	waiters int // searches still waiting for it
}

// tlBackoff is an indexer's run of failed logins.
type tlBackoff struct {
	n        int // failed logins in a row
	refusals int // of those, TorrentLeech refusing the credentials
	until    time.Time
	held     bool // credentials refused twice: no automatic login until edited or Tested
	lastErr  string
}

// NewTorrentLeechSearcher creates the searcher. fs may be nil (no Cloudflare
// solving; works when TorrentLeech isn't actively challenging).
func NewTorrentLeechSearcher(fs *flaresolverr.Client) *TorrentLeechSearcher {
	return &TorrentLeechSearcher{
		fs: fs, base: tlDefaultBaseURL, delay: tlRequestDelay, now: time.Now,
		sessions: map[int64]*tlSession{}, logins: map[int64]*tlLogin{}, backoff: map[int64]*tlBackoff{},
	}
}

const (
	tlDefaultBaseURL = "https://www.torrentleech.org"
	tlUserAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	tlRequestDelay   = 4200 * time.Millisecond // Cloudflare rate-limit guard
	tlDefaultCats    = "8,9,11,12,13,14,15,29,36,37,43,47,26,27,32,34,35,44"

	// tlLoginTimeout bounds a login, which runs on past the search that started it: a
	// FlareSolverr solve can take a minute, longer than one search's share of the budget.
	tlLoginTimeout = 90 * time.Second
	// A failed login pauses the next one for tlLoginBackoff, doubling up to tlLoginBackoffMax.
	tlLoginBackoff    = 15 * time.Minute
	tlLoginBackoffMax = 6 * time.Hour
)

// errTLBadLogin and errTL2FA are TorrentLeech refusing the credentials themselves: trying
// them again soon can only look like password guessing.
var (
	errTLBadLogin = errors.New("torrentleech: login failed — check username/password")
	errTL2FA      = errors.New("torrentleech: account has 2FA enabled — not supported yet")
)

// errTLSession is a reply that means the session no longer works: a Cloudflare 403 or
// 503, a login page, or anything else that isn't the JSON a logged-in search gets.
var errTLSession = errors.New("torrentleech: the session stopped working (signed out, or Cloudflare stepped in)")

var reReqPrefix = regexp.MustCompile(`^\[REQ(?:UEST(?:ED)?)?\]\s*`)

// throttle reserves the next request slot under the lock, then waits for it
// outside the lock, honouring ctx cancellation. Sleeping while holding rateMu
// used to block every concurrent request — including ones whose context was
// already dead — behind a plain time.Sleep.
func (t *TorrentLeechSearcher) throttle(ctx context.Context) error {
	t.rateMu.Lock()
	prev := t.lastReq
	slot := time.Now()
	if next := prev.Add(t.delay); next.After(slot) {
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
	req.Header.Set("Referer", t.base+"/")
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

	if t.fs.Configured() {
		sol, err := t.fs.Get(ctx, t.base+"/")
		if err != nil {
			return nil, fmt.Errorf("torrentleech: %w", err)
		}
		if sol.UserAgent != "" {
			ua = sol.UserAgent
		}
		u, _ := url.Parse(t.base)
		cookies := make([]*http.Cookie, 0, len(sol.Cookies))
		for _, ck := range sol.Cookies {
			cookies = append(cookies, &http.Cookie{Name: ck.Name, Value: ck.Value})
		}
		jar.SetCookies(u, cookies)
	}

	sess := &tlSession{client: &http.Client{Jar: jar, Timeout: 45 * time.Second}, ua: ua}

	form := url.Values{"username": {idx.Username}, "password": {idx.Password}}
	_, body, err := t.do(ctx, sess, http.MethodPost, t.base+"/user/account/login/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("torrentleech: login request failed: %w", err)
	}
	html := string(body)

	switch {
	case strings.Contains(html, "/user/account/logout"):
		return sess, nil
	case strings.Contains(html, "One Time Password"):
		return nil, errTL2FA
	case strings.Contains(html, "text-danger"), strings.Contains(html, "login-form"):
		return nil, errTLBadLogin
	case !t.fs.Configured():
		return nil, errNoFlareSolverr
	default:
		return nil, fmt.Errorf("torrentleech: login failed (Cloudflare challenge persisted)")
	}
}

// session returns the indexer's logged-in session, logging in when there is none.
//
// Only one login per indexer runs at a time: a search arriving while one is under way
// waits for it rather than starting its own, so a sweep's concurrent searches cost one
// login. The login runs on its own clock (tlLoginTimeout), not the search's: a search
// that gives up only stops waiting, and the login it started still finishes and is kept
// for the next search. After a failed login, no new one is tried until the pause runs out.
func (t *TorrentLeechSearcher) session(ctx context.Context, idx Indexer) (*tlSession, error) {
	t.sessMu.Lock()
	if s := t.sessions[idx.ID]; s != nil {
		t.sessMu.Unlock()
		return s, nil
	}
	if err := t.pausedLocked(idx.ID); err != nil {
		t.sessMu.Unlock()
		return nil, err
	}
	l := t.logins[idx.ID]
	if l == nil {
		l = &tlLogin{done: make(chan struct{})}
		t.logins[idx.ID] = l
		go t.login(ctx, idx, l)
	}
	l.waiters++
	t.sessMu.Unlock()

	select {
	case <-l.done:
		return l.sess, l.err
	case <-ctx.Done():
		t.sessMu.Lock()
		l.waiters--
		t.sessMu.Unlock()
		return nil, ctx.Err()
	}
}

// login runs one login for session and settles it: a session is kept, a failure moves the
// backoff. A Reset while it ran (the indexer was edited) discards the result, because it
// was made with the old settings.
func (t *TorrentLeechSearcher) login(parent context.Context, idx Indexer, l *tlLogin) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), tlLoginTimeout)
	defer cancel()
	var sess *tlSession
	err := safego.Call(nil, "torrentleech login", func() error {
		var e error
		sess, e = t.newSession(ctx, idx)
		return e
	})

	t.sessMu.Lock()
	current := t.logins[idx.ID] == l
	if current {
		delete(t.logins, idx.ID)
		if err == nil {
			t.sessions[idx.ID] = sess
			delete(t.backoff, idx.ID)
		} else {
			t.failLocked(idx.ID, err)
		}
	}
	l.sess, l.err = sess, err
	unclaimed := l.waiters == 0
	close(l.done)
	t.sessMu.Unlock()

	if current && unclaimed && t.onLogin != nil {
		t.onLogin(idx, err)
	}
}

// failLocked counts a failed login and pauses the next one: 15 minutes, doubling up to 6
// hours. TorrentLeech refusing the username, password or 2FA goes straight to 6 hours,
// and refusing them again holds logins until the indexer is edited or Tested — the same
// wrong password tried every few hours is what gets a private-tracker account flagged.
func (t *TorrentLeechSearcher) failLocked(id int64, err error) {
	b := t.backoff[id]
	if b == nil {
		b = &tlBackoff{}
		t.backoff[id] = b
	}
	b.n++
	b.lastErr = strings.TrimPrefix(err.Error(), "torrentleech: ")
	refused := errors.Is(err, errTLBadLogin) || errors.Is(err, errTL2FA)
	if refused {
		b.refusals++
		b.held = b.refusals >= 2
	}
	d := tlLoginBackoffMax
	if !refused && b.n < 6 {
		d = tlLoginBackoff << (b.n - 1) // 15m, 30m, 1h, 2h, 4h
	}
	if d > tlLoginBackoffMax {
		d = tlLoginBackoffMax
	}
	b.until = t.now().Add(d)
}

// pausedLocked is the error a search gets while logins are paused, without contacting
// the site; nil when a login may be tried.
func (t *TorrentLeechSearcher) pausedLocked(id int64) error {
	b := t.backoff[id]
	switch {
	case b == nil:
		return nil
	case b.held:
		return fmt.Errorf("TorrentLeech login paused after: %s; edit the indexer or press Test to try again", b.lastErr)
	case t.now().Before(b.until):
		return fmt.Errorf("TorrentLeech login paused until %s after: %s", b.until.Local().Format("15:04"), b.lastErr)
	}
	return nil
}

// dropSession forgets sess if it is still the indexer's session. A search whose request
// failed on an old session mustn't throw away the new one another search just logged in.
func (t *TorrentLeechSearcher) dropSession(id int64, sess *tlSession) {
	t.sessMu.Lock()
	if t.sessions[id] == sess {
		delete(t.sessions, id)
	}
	t.sessMu.Unlock()
}

// errNoFlareSolverr is a TorrentLeech login or download that Cloudflare stopped while no
// FlareSolverr URL is set — saying where to set it. (With a URL set but the container
// down, the error is FlareSolverr's own: "FlareSolverr at … isn't answering".)
var errNoFlareSolverr = errors.New("torrentleech: Cloudflare blocked TorrentLeech and FlareSolverr isn't set up; add its URL in Settings → System → API keys")

// Reset forgets everything held for the indexer — its session, a login under way and
// any login pause — because it was edited or deleted: new credentials get a login at once.
func (t *TorrentLeechSearcher) Reset(id int64) {
	t.sessMu.Lock()
	delete(t.sessions, id)
	delete(t.logins, id)
	delete(t.backoff, id)
	t.sessMu.Unlock()
}

// Test verifies credentials (and Cloudflare/FlareSolverr) with a fresh login. It ignores
// a login pause — pressing Test is how someone says "try now" — and a pass clears the
// pause and keeps the session for the next search. Unsaved settings (id 0) leave nothing
// behind.
func (t *TorrentLeechSearcher) Test(ctx context.Context, idx Indexer) error {
	sess, err := t.newSession(ctx, idx)
	if idx.ID == 0 {
		return err
	}
	t.sessMu.Lock()
	if err == nil {
		t.sessions[idx.ID] = sess
		delete(t.backoff, idx.ID)
	} else if ctx.Err() == nil {
		t.failLocked(idx.ID, err)
	}
	t.sessMu.Unlock()
	return err
}

// Search runs a keyword search against the browse JSON API. A reply saying the session
// stopped working drops it and tries once more with a fresh login.
func (t *TorrentLeechSearcher) Search(ctx context.Context, idx Indexer, q SearchQuery) ([]Release, error) {
	sess, err := t.session(ctx, idx)
	if err != nil {
		return nil, err
	}
	releases, err := t.search(ctx, sess, idx, q)
	if errors.Is(err, errTLSession) {
		t.dropSession(idx.ID, sess)
		if sess, err = t.session(ctx, idx); err != nil {
			return nil, err
		}
		releases, err = t.search(ctx, sess, idx, q)
	}
	return releases, err
}

func (t *TorrentLeechSearcher) search(ctx context.Context, sess *tlSession, idx Indexer, q SearchQuery) ([]Release, error) {
	cats := tlDefaultCats
	if len(q.Categories) > 0 {
		cats = joinInts(q.Categories)
	} else if len(idx.Categories) > 0 {
		cats = joinInts(idx.Categories)
	}

	var sb strings.Builder
	sb.WriteString(t.base + "/torrents/browse/list")
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
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable,
		strings.Contains(string(body), "login-form"),
		resp.StatusCode == http.StatusOK && !looksLikeJSON(resp.Header.Get("Content-Type"), body):
		return nil, fmt.Errorf("%w (HTTP %d)", errTLSession, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
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
			InfoURL:     t.base + "/torrent/" + it.Fid,
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
		// Cloudflare's 403 or 503 means the clearance (or the login) behind this session
		// is spent; the next attempt logs in again instead of failing the same way.
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable {
			t.dropSession(idx.ID, sess)
		}
		snippet := strings.TrimSpace(string(data))
		if len(snippet) > 140 {
			snippet = snippet[:140]
		}
		return FetchResult{}, fmt.Errorf("torrentleech: download HTTP %d %s", resp.StatusCode, snippet)
	}
	// A .torrent is bencoded (starts with 'd'); HTML means a challenge/login page.
	if len(data) == 0 || data[0] == '<' {
		t.dropSession(idx.ID, sess)
		if !t.fs.Configured() {
			return FetchResult{}, errNoFlareSolverr
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
	return fmt.Sprintf("%s/download/%s/%s", t.base, fid, url.PathEscape(filename))
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
