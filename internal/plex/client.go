// Package plex is a thin client for a Plex Media Server's HTTP API, used by the Insights
// module to read live sessions, libraries, users, and recently-added items. Auth is via an
// X-Plex-Token; responses are requested as JSON (Plex defaults to XML).
package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client talks to one Plex server (base URL + token).
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New builds a client for the given server URL and X-Plex-Token.
func New(baseURL, token string) *Client {
	return &Client{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

// get fetches a Plex endpoint as JSON and decodes it into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	if c.base == "" || c.token == "" {
		return fmt.Errorf("plex is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("plex rejected the token (401)")
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("plex returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Identity is the server's self-identification (used to validate a connection).
type Identity struct {
	MachineIdentifier string
	Version           string
}

// Identity validates the connection and returns the server's id + version.
func (c *Client) Identity(ctx context.Context) (Identity, error) {
	var r struct {
		MediaContainer struct {
			MachineIdentifier string `json:"machineIdentifier"`
			Version           string `json:"version"`
		} `json:"MediaContainer"`
	}
	if err := c.get(ctx, "/identity", &r); err != nil {
		return Identity{}, err
	}
	return Identity{MachineIdentifier: r.MediaContainer.MachineIdentifier, Version: r.MediaContainer.Version}, nil
}

// Library is one Plex library section.
type Library struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Type  string `json:"type"` // movie | show | artist | photo
	Count int64  `json:"-"`     // filled by SectionTotal on demand
}

// Libraries lists the server's library sections.
func (c *Client) Libraries(ctx context.Context) ([]Library, error) {
	var r struct {
		MediaContainer struct {
			Directory []Library `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := c.get(ctx, "/library/sections", &r); err != nil {
		return nil, err
	}
	return r.MediaContainer.Directory, nil
}

// SectionTotal returns the item count of one library section (totalSize with a 0-size page).
func (c *Client) SectionTotal(ctx context.Context, key string) (int64, error) {
	var r struct {
		MediaContainer struct {
			TotalSize flexInt `json:"totalSize"`
			Size      flexInt `json:"size"`
		} `json:"MediaContainer"`
	}
	if err := c.get(ctx, "/library/sections/"+key+"/all?X-Plex-Container-Size=0", &r); err != nil {
		return 0, err
	}
	if r.MediaContainer.TotalSize > 0 {
		return int64(r.MediaContainer.TotalSize), nil
	}
	return int64(r.MediaContainer.Size), nil
}

// Image fetches a Plex image (poster/art) by its metadata path, authenticated with the token, so
// Arrmada can proxy it to the browser without exposing the token. Caller closes the body.
func (c *Client) Image(ctx context.Context, path string) (*http.Response, error) {
	if c.base == "" || c.token == "" {
		return nil, fmt.Errorf("plex is not configured")
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("invalid image path")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Plex-Token", c.token)
	return c.http.Do(req)
}

// flexInt tolerates Plex's habit of encoding the same numeric field as a JSON number in one
// place and a quoted string in another.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexInt(n)
		return nil
	}
	ff, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = flexInt(int64(ff))
	return nil
}
