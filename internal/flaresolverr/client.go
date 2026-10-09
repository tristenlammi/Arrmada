// Package flaresolverr talks to a FlareSolverr instance to defeat Cloudflare's
// JS challenge on protected trackers (e.g. TorrentLeech). FlareSolverr solves
// the challenge in a headless browser and returns the cf_clearance cookie plus
// the exact User-Agent it used. Because Cloudflare binds cf_clearance to the
// public egress IP + UA — and Arrmada and FlareSolverr share the host's IP —
// Arrmada can then make direct requests (login/search/download) carrying that
// clearance, no per-request browser round-trip needed.
package flaresolverr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Cookie is a browser cookie returned by FlareSolverr.
type Cookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
}

// Solution is the useful part of a FlareSolverr response.
type Solution struct {
	Status    int      `json:"status"`
	UserAgent string   `json:"userAgent"`
	Cookies   []Cookie `json:"cookies"`
	Response  string   `json:"response"`
}

// ErrNotConfigured is returned by a call made with no FlareSolverr URL set.
var ErrNotConfigured = errors.New("FlareSolverr isn't set up")

// statusTTL is how long Status reuses its last answer: the Indexers page asks on every
// visit, and a ping starts no browser but is still a request to a container that may be
// busy solving a challenge.
const statusTTL = 60 * time.Second

// pingTimeout bounds a Ping: sessions.list answers at once from a live FlareSolverr.
const pingTimeout = 10 * time.Second

// Client is a FlareSolverr HTTP client. Its URL is read on every call, so a URL changed
// in Settings applies to the next TorrentLeech, 1337x or TheXEM request without a restart.
// A nil *Client is "not configured".
type Client struct {
	endpoint func() string
	http     *http.Client
	now      func() time.Time

	mu       sync.Mutex
	onResult []func(error)
	status   Status
	statusAt time.Time
	statusOf string // the endpoint the cached status was taken for
}

// Status is FlareSolverr's state for the UI. Error is the exact failure; it never
// carries a secret (the URL is a plain service address).
type Status struct {
	Configured bool      `json:"configured"`
	OK         bool      `json:"ok"`
	URL        string    `json:"url,omitempty"`
	Version    string    `json:"version,omitempty"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
}

// New builds a client for a fixed FlareSolverr base URL (e.g.
// http://arrmada-flaresolverr:8191).
func New(endpoint string) *Client {
	return NewFunc(func() string { return endpoint })
}

// NewFunc builds a client whose base URL is read from endpoint on every call — the
// saved setting, falling back to ARRMADA_FLARESOLVERR_URL.
func NewFunc(endpoint func() string) *Client {
	return &Client{
		endpoint: endpoint,
		// Solving a challenge in a headless browser can take a while.
		http: &http.Client{Timeout: 90 * time.Second},
		now:  time.Now,
	}
}

// URL is the base URL in use right now, or "" when none is set.
func (c *Client) URL() string {
	if c == nil || c.endpoint == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(c.endpoint()), "/")
}

// Configured reports whether a FlareSolverr URL is set. Safe on a nil client.
func (c *Client) Configured() bool { return c.URL() != "" }

// OnResult registers fn to hear the outcome (nil or the error) of every Get and Ping,
// for the integration status tracker. It must not block.
func (c *Client) OnResult(fn func(err error)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.onResult = append(c.onResult, fn)
	c.mu.Unlock()
}

func (c *Client) report(ctx context.Context, err error) {
	// A call the caller abandoned says nothing about FlareSolverr.
	if err != nil && ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	hooks := c.onResult
	c.mu.Unlock()
	for _, fn := range hooks {
		fn(err)
	}
}

type solveRequest struct {
	Cmd        string `json:"cmd"`
	URL        string `json:"url,omitempty"`
	MaxTimeout int    `json:"maxTimeout,omitempty"`
}

type solveResponse struct {
	Status   string   `json:"status"`
	Message  string   `json:"message"`
	Version  string   `json:"version"`
	Solution Solution `json:"solution"`
}

// post sends one command to /v1 and decodes the answer, which must say status "ok".
func (c *Client) post(ctx context.Context, base string, req solveRequest) (*solveResponse, error) {
	body, _ := json.Marshal(req)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("flaresolverr: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("FlareSolverr at %s isn't answering; is the Arrmada-flaresolverr container running? (%w)", base, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	var sr solveResponse
	decodeErr := json.Unmarshal(raw, &sr)
	if resp.StatusCode != http.StatusOK {
		// FlareSolverr answers a failed solve with a 500 and a message saying why.
		if decodeErr == nil && sr.Message != "" {
			return nil, fmt.Errorf("flaresolverr HTTP %d: %s", resp.StatusCode, sr.Message)
		}
		return nil, fmt.Errorf("flaresolverr HTTP %d", resp.StatusCode)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("flaresolverr decode: %w", decodeErr)
	}
	if sr.Status != "ok" {
		return nil, fmt.Errorf("flaresolverr: %s", sr.Message)
	}
	return &sr, nil
}

// Get solves the Cloudflare challenge for targetURL and returns the solution
// (cf_clearance cookie + User-Agent).
func (c *Client) Get(ctx context.Context, targetURL string) (*Solution, error) {
	base := c.URL()
	if base == "" {
		return nil, ErrNotConfigured
	}
	sr, err := c.post(ctx, base, solveRequest{Cmd: "request.get", URL: targetURL, MaxTimeout: 60000})
	c.report(ctx, err)
	if err != nil {
		return nil, err
	}
	return &sr.Solution, nil
}

// Ping asks FlareSolverr whether it is up (sessions.list starts no browser) and returns
// its version.
func (c *Client) Ping(ctx context.Context) (version string, err error) {
	base := c.URL()
	if base == "" {
		return "", ErrNotConfigured
	}
	// Reported against the caller's context: a ping that hits its own timeout is
	// FlareSolverr not answering, which does count.
	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	sr, err := c.post(pctx, base, solveRequest{Cmd: "sessions.list"})
	c.report(ctx, err)
	if err != nil {
		return "", err
	}
	return sr.Version, nil
}

// Status is FlareSolverr's state for the Indexers page, reusing an answer for up to 60
// seconds (a changed URL is always asked afresh). Safe on a nil client.
func (c *Client) Status(ctx context.Context) Status {
	base := c.URL()
	if base == "" {
		return Status{Configured: false, CheckedAt: time.Now()}
	}
	c.mu.Lock()
	if c.statusOf == base && !c.statusAt.IsZero() && c.now().Sub(c.statusAt) < statusTTL {
		st := c.status
		c.mu.Unlock()
		return st
	}
	c.mu.Unlock()

	st := Status{Configured: true, URL: base}
	version, err := c.Ping(ctx)
	if err != nil && ctx.Err() != nil {
		// Cut short by the caller: don't cache a failure that isn't FlareSolverr's.
		st.Error = err.Error()
		st.CheckedAt = c.now()
		return st
	}
	st.OK, st.Version = err == nil, version
	if err != nil {
		st.Error = err.Error()
	}
	st.CheckedAt = c.now()
	c.mu.Lock()
	c.status, c.statusAt, c.statusOf = st, st.CheckedAt, base
	c.mu.Unlock()
	return st
}
