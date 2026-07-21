// Package plex is a thin client for a Plex Media Server's HTTP API, used by the Insights
// module to read live sessions, libraries, users, and recently-added items. Auth is via an
// X-Plex-Token; responses are requested as JSON (Plex defaults to XML).
package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
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
	Count int64  `json:"-"`    // filled by SectionTotal on demand
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

// RecentItem is a recently-added library item.
type RecentItem struct {
	RatingKey        string `json:"rating_key"`
	Type             string `json:"type"`
	Title            string `json:"title"`
	GrandparentTitle string `json:"grandparent_title"`
	Year             int    `json:"year"`
	Thumb            string `json:"thumb"` // best poster (show poster for episodes)
	AddedAt          int64  `json:"added_at"`
}

// RecentlyAdded returns the most recently added items across libraries.
func (c *Client) RecentlyAdded(ctx context.Context, limit int) ([]RecentItem, error) {
	if limit <= 0 {
		limit = 24
	}
	var r struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey        string  `json:"ratingKey"`
				Type             string  `json:"type"`
				Title            string  `json:"title"`
				GrandparentTitle string  `json:"grandparentTitle"`
				Year             flexInt `json:"year"`
				Thumb            string  `json:"thumb"`
				GrandparentThumb string  `json:"grandparentThumb"`
				AddedAt          flexInt `json:"addedAt"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := c.get(ctx, fmt.Sprintf("/library/recentlyAdded?X-Plex-Container-Start=0&X-Plex-Container-Size=%d", limit), &r); err != nil {
		return nil, err
	}
	out := make([]RecentItem, 0, len(r.MediaContainer.Metadata))
	for _, m := range r.MediaContainer.Metadata {
		thumb := m.Thumb
		if m.GrandparentThumb != "" { // show poster beats episode still
			thumb = m.GrandparentThumb
		}
		out = append(out, RecentItem{
			RatingKey: m.RatingKey, Type: m.Type, Title: m.Title, GrandparentTitle: m.GrandparentTitle,
			Year: int(m.Year), Thumb: thumb, AddedAt: int64(m.AddedAt),
		})
	}
	return out, nil
}

// Image fetches a Plex image (poster/art) by its metadata path, authenticated with the token, so
// Arrmada can proxy it to the browser without exposing the token. Caller closes the body.
func (c *Client) Image(ctx context.Context, imgPath string) (*http.Response, error) {
	if c.base == "" || c.token == "" {
		return nil, fmt.Errorf("plex is not configured")
	}
	// Defense in depth: the httpapi handler already validates this path, but the
	// token attached below makes any un-normalized path a traversal/SSRF vector,
	// so re-check here rather than trust the caller. A query or fragment could
	// inject arbitrary Plex API params, and un-cleaned "../" segments escape the
	// image namespace once Plex normalizes them.
	imgPath, ok := safeImagePath(imgPath)
	if !ok {
		return nil, fmt.Errorf("invalid image path")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+imgPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Plex-Token", c.token)
	return c.http.Do(req)
}

// safeImagePath validates and normalizes a Plex image path before it is joined
// to the base URL and sent with the admin token. It rejects anything carrying a
// query/fragment or escaping the /library/ or /photo/ image namespaces (even via
// "../" that Plex would normalize), returning the cleaned path when acceptable.
func safeImagePath(raw string) (string, bool) {
	if raw == "" || !strings.HasPrefix(raw, "/") {
		return "", false
	}
	// A '?' or '#' would smuggle query params (e.g. an alternate token) or an
	// endpoint fragment; real image paths contain neither.
	if strings.ContainsAny(raw, "?#") {
		return "", false
	}
	clean := path.Clean(raw)
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || strings.HasSuffix(clean, "/..") {
		return "", false
	}
	if !strings.HasPrefix(clean, "/library/") && !strings.HasPrefix(clean, "/photo/") {
		return "", false
	}
	return clean, true
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
