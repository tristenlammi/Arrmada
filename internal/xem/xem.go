// Package xem fetches scene→absolute episode mappings from TheXEM (thexem.info) — the
// community database that reconciles how the scene numbers anime (split into S1/S2/…
// by arc) with the absolute episode order. It lets Arrmada map a release like
// "Dragon Ball Super S02E01" onto TMDB's continuous numbering, which air-date-gap
// inference can't do for shows that aired without a broadcast break.
package xem

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Client queries TheXEM. The zero value is unusable; use New.
type Client struct {
	http *http.Client
	base string
}

// New builds a client with a sane timeout.
func New() *Client {
	return &Client{http: &http.Client{Timeout: 20 * time.Second}, base: "https://thexem.info"}
}

// Fetch returns a scene→absolute map for a TVDB id, keyed "season-episode" (e.g. "2-1")
// → absolute episode number. An empty map (nil error) means TheXEM has no mapping for
// the show — the caller should fall back to its own heuristics.
func (c *Client) Fetch(ctx context.Context, tvdbID int) (map[string]int, error) {
	if tvdbID <= 0 {
		return map[string]int{}, nil
	}
	u := c.base + "/map/all?id=" + strconv.Itoa(tvdbID) + "&origin=tvdb"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("thexem: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}

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
