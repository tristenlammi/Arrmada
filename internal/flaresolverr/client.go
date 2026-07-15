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
	"fmt"
	"io"
	"net/http"
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

// Client is a FlareSolverr HTTP client.
type Client struct {
	endpoint string
	http     *http.Client
}

// New builds a client for the given FlareSolverr base URL (e.g.
// http://arrmada-flaresolverr:8191).
func New(endpoint string) *Client {
	return &Client{
		endpoint: endpoint,
		// Solving a challenge in a headless browser can take a while.
		http: &http.Client{Timeout: 90 * time.Second},
	}
}

type solveRequest struct {
	Cmd        string `json:"cmd"`
	URL        string `json:"url"`
	MaxTimeout int    `json:"maxTimeout"`
}

type solveResponse struct {
	Status   string   `json:"status"`
	Message  string   `json:"message"`
	Solution Solution `json:"solution"`
}

// Get solves the Cloudflare challenge for targetURL and returns the solution
// (cf_clearance cookie + User-Agent).
func (c *Client) Get(ctx context.Context, targetURL string) (*Solution, error) {
	body, _ := json.Marshal(solveRequest{Cmd: "request.get", URL: targetURL, MaxTimeout: 60000})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("flaresolverr unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("flaresolverr HTTP %d", resp.StatusCode)
	}
	var sr solveResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, fmt.Errorf("flaresolverr decode: %w", err)
	}
	if sr.Status != "ok" {
		return nil, fmt.Errorf("flaresolverr: %s", sr.Message)
	}
	return &sr.Solution, nil
}
