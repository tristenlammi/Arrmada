package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const osBaseURL = "https://api.opensubtitles.com/api/v1"

// OpenSubtitles is a subtitle provider backed by opensubtitles.com's REST API. Search
// needs only an API key; downloading additionally needs a (free) account, so username +
// password are optional and only required to actually grab. Credentials come from config.
type OpenSubtitles struct {
	http       *http.Client
	apiKeyFn   func() string
	usernameFn func() string
	passwordFn func() string
	ua         string

	mu    sync.Mutex
	token string // cached bearer token from /login
}

// NewOpenSubtitles builds the provider from config credentials (any may be empty).
func NewOpenSubtitles(apiKey, username, password string) *OpenSubtitles {
	return NewOpenSubtitlesFunc(
		func() string { return apiKey },
		func() string { return username },
		func() string { return password },
	)
}

// NewOpenSubtitlesFunc builds the provider reading each credential lazily, so values set
// in the settings menu take effect without a restart.
func NewOpenSubtitlesFunc(apiKey, username, password func() string) *OpenSubtitles {
	return &OpenSubtitles{
		http:       &http.Client{Timeout: 25 * time.Second},
		apiKeyFn:   apiKey,
		usernameFn: username,
		passwordFn: password,
		ua:         "Arrmada/1.0",
	}
}

func (o *OpenSubtitles) apiKey() string   { return strings.TrimSpace(o.apiKeyFn()) }
func (o *OpenSubtitles) username() string { return strings.TrimSpace(o.usernameFn()) }
func (o *OpenSubtitles) password() string { return strings.TrimSpace(o.passwordFn()) }

// Available reports whether search is possible (needs the API key).
func (o *OpenSubtitles) Available() bool { return o.apiKey() != "" }

// CanDownload reports whether we can actually fetch files (needs a full account).
func (o *OpenSubtitles) CanDownload() bool {
	return o.apiKey() != "" && o.username() != "" && o.password() != ""
}

func (o *OpenSubtitles) req(ctx context.Context, method, path string, q url.Values, body any, bearer string) (*http.Response, error) {
	u := osBaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Api-Key", o.apiKey())
	req.Header.Set("User-Agent", o.ua)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return o.http.Do(req)
}

// login fetches (and caches) a bearer token for downloads.
func (o *OpenSubtitles) login(ctx context.Context) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.token != "" {
		return o.token, nil
	}
	if !o.CanDownload() {
		return "", fmt.Errorf("%w: OpenSubtitles username/password required to download", ErrNotConfigured)
	}
	resp, err := o.req(ctx, http.MethodPost, "/login", nil, map[string]string{
		"username": o.username(), "password": o.password(),
	}, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("opensubtitles login: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Token == "" {
		return "", fmt.Errorf("opensubtitles login: no token in response")
	}
	o.token = out.Token
	return o.token, nil
}

// Search finds candidate subtitles, best (most-downloaded) first.
func (o *OpenSubtitles) Search(ctx context.Context, sr SearchRequest) ([]SubtitleResult, error) {
	if !o.Available() {
		return nil, ErrNotConfigured
	}
	q := url.Values{}
	q.Set("languages", strings.ToLower(sr.Language))
	if id := strings.TrimPrefix(sr.IMDBID, "tt"); id != "" {
		if sr.Season > 0 {
			q.Set("parent_imdb_id", id)
		} else {
			q.Set("imdb_id", id)
		}
	} else if sr.Title != "" {
		q.Set("query", sr.Title)
	}
	if sr.Season > 0 {
		q.Set("type", "episode")
		q.Set("season_number", strconv.Itoa(sr.Season))
		q.Set("episode_number", strconv.Itoa(sr.Episode))
	} else {
		q.Set("type", "movie")
	}
	resp, err := o.req(ctx, http.MethodGet, "/subtitles", q, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("opensubtitles search: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data []struct {
			Attributes struct {
				Language        string `json:"language"`
				Release         string `json:"release"`
				DownloadCount   int    `json:"download_count"`
				HearingImpaired bool   `json:"hearing_impaired"`
				Files           []struct {
					FileID int `json:"file_id"`
				} `json:"files"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	var out []SubtitleResult
	for _, d := range payload.Data {
		if len(d.Attributes.Files) == 0 {
			continue
		}
		out = append(out, SubtitleResult{
			FileID:          strconv.Itoa(d.Attributes.Files[0].FileID),
			Language:        d.Attributes.Language,
			Release:         d.Attributes.Release,
			Downloads:       d.Attributes.DownloadCount,
			HearingImpaired: d.Attributes.HearingImpaired,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Downloads > out[j].Downloads })
	return out, nil
}

// Download resolves a file id to a temporary link, then fetches the subtitle bytes.
func (o *OpenSubtitles) Download(ctx context.Context, fileID string) ([]byte, error) {
	token, err := o.login(ctx)
	if err != nil {
		return nil, err
	}
	id, _ := strconv.Atoi(fileID)
	resp, err := o.req(ctx, http.MethodPost, "/download", nil, map[string]any{"file_id": id}, token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("opensubtitles download: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Link string `json:"link"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Link == "" {
		return nil, fmt.Errorf("opensubtitles download: no link in response")
	}
	// Fetch the actual subtitle file from the temporary link.
	fr, err := http.NewRequestWithContext(ctx, http.MethodGet, out.Link, nil)
	if err != nil {
		return nil, err
	}
	fr.Header.Set("User-Agent", o.ua)
	fresp, err := o.http.Do(fr)
	if err != nil {
		return nil, err
	}
	defer fresp.Body.Close()
	if fresp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("opensubtitles fetch: HTTP %d", fresp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(fresp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	return data, nil
}
