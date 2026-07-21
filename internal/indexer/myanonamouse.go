package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MAMSearcher is a native MyAnonaMouse integration. MAM exposes a rich JSON
// search API (loadSearchJSONbasic.php) that returns structured author, narrator,
// series, language and file-type metadata per torrent — far more than a
// Torznab/Prowlarr feed carries — so books show their narrator reliably instead
// of relying on the name happening to contain "Narrated by".
//
// Config on the Indexer: APIKey holds the mam_id session string (from MAM →
// Preferences → Security → "Create session"). MAM rotates that session on use
// and re-issues it via Set-Cookie; we persist the fresh value through the
// session persister so a rotated session doesn't silently expire.
type MAMSearcher struct {
	http *http.Client
	// persist saves a rotated mam_id back onto the indexer record (id → new
	// session). nil is tolerated (rotation just isn't persisted).
	persist func(id int64, session string)

	rateMu  sync.Mutex
	lastReq time.Time
}

// NewMAMSearcher builds the searcher. persist may be nil.
func NewMAMSearcher(persist func(id int64, session string)) *MAMSearcher {
	return &MAMSearcher{http: &http.Client{Timeout: 45 * time.Second}, persist: persist}
}

const (
	mamBaseURL      = "https://www.myanonamouse.net"
	mamSearchPath   = "/tor/js/loadSearchJSONbasic.php"
	mamRequestDelay = 1500 * time.Millisecond // be gentle with MAM's API
)

// MAM top-level categories. 13 = Audiobooks, 14 = E-Books, 15 = Musicology,
// 16 = Radio. We search books + audiobooks by default.
var mamBookMainCats = []int{13, 14}

// throttle reserves the next request slot under the lock, then waits for it
// outside the lock, honouring ctx cancellation (see TorrentLeechSearcher.throttle).
func (m *MAMSearcher) throttle(ctx context.Context) error {
	m.rateMu.Lock()
	prev := m.lastReq
	slot := time.Now()
	if next := prev.Add(mamRequestDelay); next.After(slot) {
		slot = next
	}
	m.lastReq = slot
	m.rateMu.Unlock()

	if err := waitUntil(ctx, slot); err != nil {
		m.rateMu.Lock()
		if m.lastReq.Equal(slot) {
			m.lastReq = prev
		}
		m.rateMu.Unlock()
		return err
	}
	return nil
}

// mamSearchBody is the JSON request MAM's search endpoint expects.
type mamSearchBody struct {
	Tor        mamTor `json:"tor"`
	Thumbnails string `json:"thumbnails"`
}

type mamTor struct {
	Text        string          `json:"text"`
	SrchIn      map[string]bool `json:"srchIn"`
	SearchType  string          `json:"searchType"`
	SearchIn    string          `json:"searchIn"`
	MainCat     []int           `json:"main_cat"`
	SortType    string          `json:"sortType"`
	StartNumber string          `json:"startNumber"`
	PerPage     int             `json:"perpage"`
}

// mamResponse is MAM's search reply. On no results MAM returns {"error": "..."}.
type mamResponse struct {
	Data  []mamItem `json:"data"`
	Found int       `json:"found"`
	Error string    `json:"error"`
}

type mamItem struct {
	ID             json.Number `json:"id"`
	Title          string      `json:"title"`
	AuthorInfo     string      `json:"author_info"`   // JSON string: {"id":"Name",…}
	NarratorInfo   string      `json:"narrator_info"` // JSON string: {"id":"Name",…}
	SeriesInfo     string      `json:"series_info"`   // JSON string: {"id":["Name","#"],…}
	LangCode       string      `json:"lang_code"`
	Language       json.Number `json:"language"`
	Filetype       string      `json:"filetype"`
	Size           string      `json:"size"` // human string, e.g. "358.12 MB"
	Seeders        json.Number `json:"seeders"`
	Leechers       json.Number `json:"leechers"`
	TimesCompleted json.Number `json:"times_completed"`
	NumFiles       json.Number `json:"numfiles"`
	Added          string      `json:"added"`
	Dl             string      `json:"dl"` // download token
	Category       json.Number `json:"category"`
}

// Search runs a keyword search against MAM's JSON API and maps the structured
// metadata onto releases.
func (m *MAMSearcher) Search(ctx context.Context, idx Indexer, q SearchQuery) ([]Release, error) {
	session := strings.TrimSpace(idx.APIKey)
	if session == "" {
		return nil, fmt.Errorf("myanonamouse: mam_id session is required")
	}
	if q.MediaType != "" && q.MediaType != MediaBook {
		return nil, nil // MAM is a book/audiobook tracker; ignore non-book searches
	}

	mainCats := mamBookMainCats
	if len(q.Categories) > 0 {
		mainCats = q.Categories
	} else if len(idx.Categories) > 0 {
		mainCats = idx.Categories
	}
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	body := mamSearchBody{
		Tor: mamTor{
			Text:        strings.TrimSpace(q.Text),
			SrchIn:      map[string]bool{"title": true, "author": true, "narrator": true, "series": true},
			SearchType:  "all",
			SearchIn:    "torrents",
			MainCat:     mainCats,
			SortType:    "seedersDesc",
			StartNumber: "0",
			PerPage:     limit,
		},
		Thumbnails: "false",
	}
	payload, _ := json.Marshal(body)

	respBody, err := m.do(ctx, idx, http.MethodPost, mamBaseURL+mamSearchPath, session, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	var res mamResponse
	if err := json.Unmarshal(respBody, &res); err != nil {
		return nil, fmt.Errorf("myanonamouse: parse response: %w", err)
	}
	// MAM signals "no matches" with an error string, not an HTTP error — treat the
	// common empty-result messages as an empty list, not a failure.
	if res.Error != "" {
		if isMAMEmpty(res.Error) {
			return nil, nil
		}
		return nil, fmt.Errorf("myanonamouse: %s", res.Error)
	}

	releases := make([]Release, 0, len(res.Data))
	for _, it := range res.Data {
		releases = append(releases, m.releaseFrom(idx, it))
	}
	return releases, nil
}

func (m *MAMSearcher) releaseFrom(idx Indexer, it mamItem) Release {
	id := it.ID.String()
	title := strings.TrimSpace(html.UnescapeString(it.Title))
	format := strings.ToUpper(strings.TrimSpace(it.Filetype))
	author := joinMAMNames(it.AuthorInfo)
	narrator := joinMAMNames(it.NarratorInfo)
	series := joinMAMSeries(it.SeriesInfo)
	lang := strings.TrimSpace(it.LangCode)

	// A display title that carries author + format so the existing book pipeline
	// (which detects format from the title and matches downloads by name) keeps
	// working, while the structured fields drive the richer UI.
	display := title
	if author != "" {
		display = author + " - " + title
	}
	if format != "" {
		display += " [" + format + "]"
	}

	// Description doubles as a human blurb and a fallback the narrator regex can
	// still parse for any downstream code that only looks at text.
	var desc []string
	if narrator != "" {
		desc = append(desc, "Narrator: "+narrator)
	}
	if series != "" {
		desc = append(desc, "Series: "+series)
	}
	if lang != "" {
		desc = append(desc, "Language: "+lang)
	}

	r := Release{
		Title:       display,
		Description: strings.Join(desc, " · "),
		DownloadURL: mamBaseURL + "/tor/download.php?tid=" + id,
		InfoURL:     mamBaseURL + "/t/" + id,
		SizeBytes:   parseHumanSize(it.Size),
		Seeders:     numToInt(it.Seeders),
		Peers:       numToInt(it.Leechers),
		Indexer:     idx.Name,
		Transport:   TransportTorrent,
		Author:      author,
		Narrator:    narrator,
		Series:      series,
		Language:    lang,
		Format:      format,
	}
	if n := numToInt(it.Category); n > 0 {
		r.Categories = []int{n}
	}
	if t, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(it.Added)); err == nil {
		r.PublishedAt = t
	}
	// The MAM token lets us build a session-less .torrent link too, but we fetch
	// through the authenticated session (Fetch) so this stays robust either way.
	_ = it.Dl
	return r
}

// Fetch downloads a release's .torrent bytes through the mam_id session.
func (m *MAMSearcher) Fetch(ctx context.Context, idx Indexer, downloadURL string) (FetchResult, error) {
	session := strings.TrimSpace(idx.APIKey)
	if session == "" {
		return FetchResult{}, fmt.Errorf("myanonamouse: mam_id session is required")
	}
	data, err := m.do(ctx, idx, http.MethodGet, downloadURL, session, nil)
	if err != nil {
		return FetchResult{}, err
	}
	// A .torrent is bencoded (starts with 'd'); HTML means an auth/error page.
	if len(data) == 0 || data[0] == '<' {
		return FetchResult{}, fmt.Errorf("myanonamouse: didn't get a torrent — session may have expired; re-create the mam_id session")
	}
	return FetchResult{File: data, Filename: "myanonamouse.torrent"}, nil
}

// Test verifies the session by running a minimal search.
func (m *MAMSearcher) Test(ctx context.Context, idx Indexer) error {
	if strings.TrimSpace(idx.APIKey) == "" {
		return fmt.Errorf("myanonamouse: mam_id session is required")
	}
	_, err := m.Search(ctx, idx, SearchQuery{Text: "the", MediaType: MediaBook, Limit: 1})
	return err
}

// do issues a throttled request carrying the mam_id cookie, and persists a
// rotated mam_id if MAM re-issues one via Set-Cookie.
func (m *MAMSearcher) do(ctx context.Context, idx Indexer, method, rawurl, session string, body io.Reader) ([]byte, error) {
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(mamBaseURL)
	jar.SetCookies(u, []*http.Cookie{{Name: "mam_id", Value: session}})
	client := &http.Client{Jar: jar, Timeout: m.http.Timeout}

	req, err := http.NewRequestWithContext(ctx, method, rawurl, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Arrmada/1.0")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := m.throttle(ctx); err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("myanonamouse: request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("myanonamouse: not authorized (HTTP %d) — the mam_id session is invalid or IP-locked to a different address", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("myanonamouse: HTTP %d", resp.StatusCode)
	}
	m.persistRotatedSession(idx, jar, u, session)
	return data, nil
}

// persistRotatedSession saves a fresh mam_id if MAM issued a new one.
func (m *MAMSearcher) persistRotatedSession(idx Indexer, jar *cookiejar.Jar, u *url.URL, old string) {
	if m.persist == nil || idx.ID == 0 {
		return
	}
	for _, c := range jar.Cookies(u) {
		if c.Name == "mam_id" && c.Value != "" && c.Value != old {
			m.persist(idx.ID, c.Value)
			return
		}
	}
}

// --- helpers ---

// joinMAMNames decodes MAM's `{"id":"Name",…}` JSON-in-a-string field into a
// comma-joined list of names ("" when absent or unparseable).
func joinMAMNames(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" || raw == "{}" {
		return ""
	}
	var byID map[string]string
	if err := json.Unmarshal([]byte(raw), &byID); err != nil {
		return ""
	}
	names := make([]string, 0, len(byID))
	for _, v := range byID {
		if v = strings.TrimSpace(html.UnescapeString(v)); v != "" {
			names = append(names, v)
		}
	}
	return strings.Join(names, ", ")
}

// joinMAMSeries decodes MAM's series field `{"id":["Series","#"],…}` into
// "Series #1" form.
func joinMAMSeries(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" || raw == "{}" {
		return ""
	}
	var byID map[string][]string
	if err := json.Unmarshal([]byte(raw), &byID); err != nil {
		return ""
	}
	out := make([]string, 0, len(byID))
	for _, pair := range byID {
		if len(pair) == 0 {
			continue
		}
		name := strings.TrimSpace(html.UnescapeString(pair[0]))
		if name == "" {
			continue
		}
		if len(pair) > 1 && strings.TrimSpace(pair[1]) != "" {
			name += " #" + strings.TrimSpace(pair[1])
		}
		out = append(out, name)
	}
	return strings.Join(out, ", ")
}

// parseHumanSize converts MAM's "358.12 MB" / "1.09 GB" size strings to bytes.
func parseHumanSize(s string) int64 {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) != 2 {
		return 0
	}
	val, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{
		"B": 1, "KB": 1 << 10, "KIB": 1 << 10, "MB": 1 << 20, "MIB": 1 << 20,
		"GB": 1 << 30, "GIB": 1 << 30, "TB": 1 << 40, "TIB": 1 << 40,
	}[strings.ToUpper(fields[1])]
	if mult == 0 {
		return 0
	}
	return int64(val * mult)
}

func numToInt(n json.Number) int {
	if n == "" {
		return 0
	}
	i, err := n.Int64()
	if err != nil {
		f, ferr := n.Float64()
		if ferr != nil {
			return 0
		}
		return int(f)
	}
	return int(i)
}

// isMAMEmpty reports whether a MAM error string just means "no matches".
func isMAMEmpty(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "nothing returned") || strings.Contains(msg, "no results") || strings.Contains(msg, "no torrents")
}
