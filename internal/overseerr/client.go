// Package overseerr is a read-only client for Overseerr / Jellyseerr, used to
// migrate an existing request history into Arrmada's own Requests module. The two
// apps share the same REST API shape, so one client covers both.
package overseerr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Item is one migrated request, mapped to Arrmada's model.
type Item struct {
	MediaType string // "movie" | "series"
	TMDBID    int
	Title     string
	Year      int
	PosterURL string
	Requester string // Overseerr display name / username (for attribution)
	Status    string // "approved" | "pending" | "declined"
}

// Client talks to an Overseerr/Jellyseerr instance with an API key.
type Client struct {
	base string
	key  string
	http *http.Client
}

// New builds a client for baseURL (e.g. http://host:5055) authenticating with key.
func New(baseURL, key string) *Client {
	return &Client{
		base: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		key:  strings.TrimSpace(key),
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.key)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("overseerr: connect failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("overseerr: invalid API key")
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("overseerr: not found (check the URL — it should be the base address, e.g. http://host:5055)")
	case resp.StatusCode >= 300:
		return fmt.Errorf("overseerr: HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

// reqObj is the subset of an Overseerr request object we read.
type reqObj struct {
	Status int    `json:"status"` // 1 pending · 2 approved · 3 declined
	Type   string `json:"type"`   // "movie" | "tv"
	Media  struct {
		TmdbID    int    `json:"tmdbId"`
		MediaType string `json:"mediaType"`
		Status    int    `json:"status"` // 1 unknown · 2 pending · 3 processing · 4 partial · 5 available
	} `json:"media"`
	RequestedBy struct {
		DisplayName  string `json:"displayName"`
		PlexUsername string `json:"plexUsername"`
		Username     string `json:"username"`
	} `json:"requestedBy"`
}

// List paginates through every request and maps each to an Item (without title/
// poster — call Details to fill those). Fast: only the /request pages are fetched.
func (c *Client) List(ctx context.Context) ([]Item, error) {
	const take = 50
	var items []Item
	for skip := 0; ; skip += take {
		var page struct {
			PageInfo struct {
				Pages   int `json:"pages"`
				Page    int `json:"page"`
				Results int `json:"results"`
			} `json:"pageInfo"`
			Results []reqObj `json:"results"`
		}
		if err := c.get(ctx, fmt.Sprintf("/api/v1/request?take=%d&skip=%d&sort=added", take, skip), &page); err != nil {
			return nil, err
		}
		for _, r := range page.Results {
			if r.Media.TmdbID == 0 {
				continue
			}
			mt := "movie"
			if r.Type == "tv" || r.Media.MediaType == "tv" {
				mt = "series"
			}
			status := "pending"
			switch {
			case r.Status == 3:
				status = "declined"
			case r.Status == 2 || r.Media.Status >= 4: // approved, or already partially/fully available
				status = "approved"
			}
			items = append(items, Item{
				MediaType: mt,
				TMDBID:    r.Media.TmdbID,
				Requester: firstNonEmpty(r.RequestedBy.DisplayName, r.RequestedBy.PlexUsername, r.RequestedBy.Username),
				Status:    status,
			})
		}
		if len(page.Results) < take || (page.PageInfo.Pages > 0 && page.PageInfo.Page >= page.PageInfo.Pages) {
			break
		}
	}
	return items, nil
}

// Details fills an item's title/year/poster from Overseerr's media detail endpoint.
// Best-effort: a failure leaves the fields blank (the library add still gets full
// metadata from TMDB on approval).
func (c *Client) Details(ctx context.Context, it *Item) {
	if it.MediaType == "series" {
		var d struct {
			Name         string `json:"name"`
			FirstAirDate string `json:"firstAirDate"`
			PosterPath   string `json:"posterPath"`
		}
		if c.get(ctx, fmt.Sprintf("/api/v1/tv/%d", it.TMDBID), &d) == nil {
			it.Title, it.Year, it.PosterURL = d.Name, yearOf(d.FirstAirDate), posterURL(d.PosterPath)
		}
		return
	}
	var d struct {
		Title       string `json:"title"`
		ReleaseDate string `json:"releaseDate"`
		PosterPath  string `json:"posterPath"`
	}
	if c.get(ctx, fmt.Sprintf("/api/v1/movie/%d", it.TMDBID), &d) == nil {
		it.Title, it.Year, it.PosterURL = d.Title, yearOf(d.ReleaseDate), posterURL(d.PosterPath)
	}
}

func posterURL(path string) string {
	if path == "" {
		return ""
	}
	return "https://image.tmdb.org/t/p/w500" + path
}

func yearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(date[:4])
	return y
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
