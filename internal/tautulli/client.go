// Package tautulli is a read-only client for a Tautulli instance, used to backfill Arrmada's
// Insights (watch monitoring) with historical play sessions so stats aren't blank on day one.
package tautulli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Row is one historical play from Tautulli's get_history, mapped to what Insights records.
type Row struct {
	UserID           int64
	User             string
	Title            string
	GrandparentTitle string
	ParentTitle      string
	MediaType        string // movie | episode | track
	Year             int
	RatingKey        string
	MediaIndex       int
	ParentIndex      int
	Started          int64 // epoch seconds
	Stopped          int64
	DurationSec      int64 // watched seconds
	PausedSec        int64
	Platform         string
	Player           string
	Product          string
	IPAddress        string
	Decision         string // transcode_decision
	Thumb            string // media poster
	UserThumb        string // user avatar (distinct from the media poster)
}

// Client talks to a Tautulli instance with an API key.
type Client struct {
	base string
	key  string
	http *http.Client
}

func New(baseURL, key string) *Client {
	return &Client{
		base: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		key:  strings.TrimSpace(key),
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// call invokes a Tautulli API command and decodes response.data into out.
func (c *Client) call(ctx context.Context, cmd string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	params.Set("apikey", c.key)
	params.Set("cmd", cmd)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v2?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tautulli: connect failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("tautulli: invalid API key")
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("tautulli: HTTP %d", resp.StatusCode)
	}
	var env struct {
		Response struct {
			Result  string          `json:"result"`
			Message any             `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("tautulli: bad response (is this a Tautulli URL?)")
	}
	if env.Response.Result != "success" {
		return fmt.Errorf("tautulli: %v", env.Response.Message)
	}
	if out != nil && len(env.Response.Data) > 0 {
		return json.Unmarshal(env.Response.Data, out)
	}
	return nil
}

// Ping verifies the URL + key by asking for one history row.
func (c *Client) Ping(ctx context.Context) error {
	var data struct {
		Data []map[string]any `json:"data"`
	}
	return c.call(ctx, "get_history", url.Values{"length": {"1"}}, &data)
}

// History pages through the full watch history, invoking fn for each batch (so a large history can
// stream into the importer rather than buffering it all).
func (c *Client) History(ctx context.Context, fn func([]Row) error) error {
	const length = 500
	for start := 0; ; start += length {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var data struct {
			Data []map[string]any `json:"data"`
		}
		if err := c.call(ctx, "get_history", url.Values{"length": {strconv.Itoa(length)}, "start": {strconv.Itoa(start)}}, &data); err != nil {
			return err
		}
		batch := make([]Row, 0, len(data.Data))
		for _, m := range data.Data {
			r := Row{
				UserID: asInt(m["user_id"]),
				User:   firstNonEmpty(asStr(m["friendly_name"]), asStr(m["user"])),
				// Prefer the plain title over full_title ("Show - Episode" / "Movie (Year)"): live
				// rows store the bare title (+ grandparent/parent for episodes), so using full_title
				// doubled show names and fragmented movie top-lists when grouping across sources.
				Title:            firstNonEmpty(asStr(m["title"]), asStr(m["full_title"])),
				GrandparentTitle: asStr(m["grandparent_title"]),
				ParentTitle:      asStr(m["parent_title"]),
				MediaType:        asStr(m["media_type"]),
				Year:             int(asInt(m["year"])),
				RatingKey:        asStr(m["rating_key"]),
				MediaIndex:       int(asInt(m["media_index"])),
				ParentIndex:      int(asInt(m["parent_media_index"])),
				Started:          asInt(m["started"]),
				Stopped:          asInt(m["stopped"]),
				DurationSec:      asInt(m["duration"]),
				PausedSec:        asInt(m["paused_counter"]),
				Platform:         asStr(m["platform"]),
				Player:           asStr(m["player"]),
				Product:          asStr(m["product"]),
				IPAddress:        asStr(m["ip_address"]),
				Decision:         asStr(m["transcode_decision"]),
				Thumb:            asStr(m["thumb"]),
				UserThumb:        asStr(m["user_thumb"]),
			}
			if r.Started == 0 {
				continue // group headers / bad rows
			}
			batch = append(batch, r)
		}
		if len(batch) > 0 {
			if err := fn(batch); err != nil {
				return err
			}
		}
		if len(data.Data) < length {
			return nil
		}
		if start > 500000 { // safety backstop
			return nil
		}
	}
}

// asInt coerces Tautulli's mixed number/string JSON to int64.
func asInt(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return i
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

func asStr(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
