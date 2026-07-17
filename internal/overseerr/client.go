// Package overseerr is a read-only client for Overseerr / Jellyseerr, used to
// migrate an existing request history into Arrmada's own Requests module. The two
// apps share the same REST API shape, so one client covers both.
package overseerr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Item is one migrated request, mapped to Arrmada's model.
type Item struct {
	MediaType      string // "movie" | "series"
	TMDBID         int
	Title          string
	Year           int
	PosterURL      string
	Requester      string // Overseerr display name / username (for attribution)
	RequesterPlex  int    // requester's Plex account id (0 = none) — links to a Plex sign-in
	RequesterEmail string
	Status         string // "approved" | "pending" | "declined"
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
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return &httpError{code: resp.StatusCode, body: snippet}
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
		Email        string `json:"email"`
		PlexID       int    `json:"plexId"`
	} `json:"requestedBy"`
}

// httpError carries the Overseerr HTTP status + body so a 5xx shows the real cause and can be
// detected for the skip-the-bad-request fallback.
type httpError struct {
	code int
	body string
}

func (e *httpError) Error() string {
	if e.body != "" {
		return fmt.Sprintf("overseerr: HTTP %d — %s", e.code, e.body)
	}
	return fmt.Sprintf("overseerr: HTTP %d", e.code)
}

func isServerErr(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.code >= 500
}

// page fetches one page of requests (take from skip) and maps them to Items. count is the number
// of raw results Overseerr returned (so the caller knows when it's reached the end).
func (c *Client) page(ctx context.Context, take, skip int) (items []Item, count int, err error) {
	var pg struct {
		Results []reqObj `json:"results"`
	}
	if err := c.get(ctx, fmt.Sprintf("/api/v1/request?take=%d&skip=%d&sort=added", take, skip), &pg); err != nil {
		return nil, 0, err
	}
	for _, r := range pg.Results {
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
			MediaType:      mt,
			TMDBID:         r.Media.TmdbID,
			Requester:      firstNonEmpty(r.RequestedBy.DisplayName, r.RequestedBy.PlexUsername, r.RequestedBy.Username),
			RequesterPlex:  r.RequestedBy.PlexID,
			RequesterEmail: r.RequestedBy.Email,
			Status:         status,
		})
	}
	return items, len(pg.Results), nil
}

// List paginates through every request and maps each to an Item (without title/poster — call
// Details to fill those). A 5xx on a page (Overseerr chokes on an orphaned request) triggers a
// one-at-a-time walk of that batch so a single bad request doesn't abort the whole migration.
func (c *Client) List(ctx context.Context) ([]Item, error) {
	const take = 50
	var items []Item
	for skip := 0; ; {
		batch, count, err := c.page(ctx, take, skip)
		if err != nil {
			if !isServerErr(err) {
				return items, err
			}
			// Walk this batch request-by-request, skipping the one(s) Overseerr can't serialise.
			got, atEnd, werr := c.walkBatch(ctx, skip, take, &items)
			if got == 0 && werr != nil {
				return items, werr // couldn't get anything — surface the error
			}
			if atEnd {
				break
			}
			skip += take
			continue
		}
		items = append(items, batch...)
		if count < take {
			break
		}
		skip += take
	}
	return items, nil
}

// walkBatch fetches [skip, skip+take) one request at a time, appending successes and skipping any
// single request that 5xx's. atEnd is true when Overseerr returned no more results.
func (c *Client) walkBatch(ctx context.Context, skip, take int, items *[]Item) (got int, atEnd bool, err error) {
	for i := 0; i < take; i++ {
		one, count, e := c.page(ctx, 1, skip+i)
		if e != nil {
			if isServerErr(e) {
				continue // this single request is un-serialisable on Overseerr's side — skip it
			}
			return got, false, e
		}
		if count == 0 {
			return got, true, nil
		}
		*items = append(*items, one...)
		got += len(one)
	}
	return got, false, nil
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
