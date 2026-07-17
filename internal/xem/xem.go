// Package xem fetches scene→absolute episode mappings from TheXEM (thexem.info) — the
// community database that reconciles how the scene numbers anime (split into S1/S2/…
// by arc) with the absolute episode order. It lets Arrmada map a release like
// "Dragon Ball Super S02E01" onto TMDB's continuous numbering, which air-date-gap
// inference can't do for shows that aired without a broadcast break.
package xem

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// userAgent is a browser-style UA so Cloudflare's bot filter on thexem.info lets the
// request through (the default Go UA is 403'd).
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// Client queries TheXEM. The zero value is unusable; use New.
type Client struct {
	http     *http.Client
	base     string
	flareURL string // optional FlareSolverr endpoint, used when Cloudflare blocks a direct fetch
}

// New builds a client. flareURL (Arrmada's bundled FlareSolverr, e.g.
// http://arrmada-flaresolverr:8191) is used to get past a Cloudflare challenge when the
// direct request is blocked; pass "" to disable that fallback.
func New(flareURL string) *Client {
	return &Client{http: &http.Client{Timeout: 25 * time.Second}, base: "https://thexem.info", flareURL: strings.TrimRight(flareURL, "/")}
}

// Fetch returns a scene→absolute map for a TVDB id, keyed "season-episode" (e.g. "2-1")
// → absolute episode number. An empty map (nil error) means TheXEM has no mapping for
// the show — the caller should fall back to its own heuristics.
func (c *Client) Fetch(ctx context.Context, tvdbID int) (map[string]int, error) {
	if tvdbID <= 0 {
		return map[string]int{}, nil
	}
	u := c.base + "/map/all?id=" + strconv.Itoa(tvdbID) + "&origin=tvdb"

	body, status, err := c.getDirect(ctx, u)
	if err == nil && status == http.StatusOK {
		return parse(body)
	}
	// Blocked by Cloudflare (403/503/429) → route through FlareSolverr if configured.
	if c.flareURL != "" && (status == http.StatusForbidden || status == http.StatusServiceUnavailable || status == http.StatusTooManyRequests) {
		fb, ferr := c.getViaFlare(ctx, u)
		if ferr != nil {
			return nil, fmt.Errorf("thexem: HTTP %d, flaresolverr: %w", status, ferr)
		}
		return parse(fb)
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("thexem: HTTP %d", status)
}

// getDirect does a plain GET with a browser UA, returning the body and status.
func (c *Client) getDirect(ctx context.Context, u string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return body, resp.StatusCode, nil
}

// getViaFlare fetches u through FlareSolverr, which drives a real browser to solve the
// Cloudflare challenge and returns the final page — from which we extract the JSON body.
func (c *Client) getViaFlare(ctx context.Context, u string) ([]byte, error) {
	reqBody, _ := json.Marshal(map[string]any{"cmd": "request.get", "url": u, "maxTimeout": 60000})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.flareURL+"/v1", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	fc := &http.Client{Timeout: 90 * time.Second} // browser solving takes longer than a plain GET
	resp, err := fc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution struct {
			Status   int    `json:"status"`
			Response string `json:"response"`
		} `json:"solution"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, err
	}
	if out.Status != "ok" {
		return nil, fmt.Errorf("flaresolverr: %s", out.Message)
	}
	return []byte(extractJSON(out.Solution.Response)), nil
}

// extractJSON pulls the JSON payload out of a FlareSolverr page response, which wraps
// the body in HTML (Chrome renders raw JSON inside <pre>…</pre>).
func extractJSON(s string) string {
	if i := strings.Index(s, "<pre"); i >= 0 {
		if j := strings.Index(s[i:], ">"); j >= 0 {
			rest := s[i+j+1:]
			if k := strings.Index(rest, "</pre>"); k >= 0 {
				return html.UnescapeString(rest[:k])
			}
		}
	}
	// Fallback: take from the first '{' to the last '}'.
	a, b := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if a >= 0 && b > a {
		return html.UnescapeString(s[a : b+1])
	}
	return s
}

// parse turns a TheXEM /map/all JSON body into a scene "S-E" → absolute map.
func parse(body []byte) (map[string]int, error) {
	var envelope struct {
		Result string          `json:"result"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("thexem: parse: %w", err)
	}
	if envelope.Result != "success" {
		return map[string]int{}, nil // no mapping for this show
	}
	var entries []struct {
		Scene struct {
			Season   int `json:"season"`
			Episode  int `json:"episode"`
			Absolute int `json:"absolute"`
		} `json:"scene"`
	}
	if err := json.Unmarshal(envelope.Data, &entries); err != nil {
		return map[string]int{}, nil // data shape unexpected → treat as no mapping
	}
	out := make(map[string]int, len(entries))
	for _, e := range entries {
		if e.Scene.Absolute > 0 {
			out[Key(e.Scene.Season, e.Scene.Episode)] = e.Scene.Absolute
		}
	}
	return out, nil
}

// Key is the map key for a scene (season, episode).
func Key(season, episode int) string {
	return strconv.Itoa(season) + "-" + strconv.Itoa(episode)
}
