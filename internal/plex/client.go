// Package plex is a thin client for a Plex Media Server's HTTP API, used by the Insights
// module to read live sessions, libraries, users, and recently-added items. Auth is via an
// X-Plex-Token; responses are requested as JSON (Plex defaults to XML).
package plex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// ErrNotConfigured means no server URL or token is set. Callers that only want to report
// real failures (the Dashboard's "Plex isn't reachable") check for it and say "not
// connected yet" instead of passing it on as an error.
var ErrNotConfigured = errors.New("plex is not configured")

// ErrUnauthorized is Plex refusing the token (HTTP 401): it was revoked or garbled, and
// retrying won't help until someone reconnects Plex.
var ErrUnauthorized = errors.New("plex rejected the token (401)")

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
		return ErrNotConfigured
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
		return ErrUnauthorized
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

// ServerName is the server's own name (what Plex apps show it as), from its root endpoint.
func (c *Client) ServerName(ctx context.Context) (string, error) {
	var r struct {
		MediaContainer struct {
			FriendlyName string `json:"friendlyName"`
		} `json:"MediaContainer"`
	}
	if err := c.get(ctx, "/", &r); err != nil {
		return "", err
	}
	return r.MediaContainer.FriendlyName, nil
}

// Library is one Plex library section.
type Library struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Type  string `json:"type"` // movie | show | artist | photo
	Count int64  `json:"-"`    // filled by SectionTotal on demand
	// Locations are the section's folders as the Plex server sees them (its own paths,
	// which usually differ from Arrmada's container paths). Partial scans name a folder
	// under one of these.
	Locations []string `json:"locations,omitempty"`
}

// Libraries lists the server's library sections.
func (c *Client) Libraries(ctx context.Context) ([]Library, error) {
	var r struct {
		MediaContainer struct {
			Directory []struct {
				Key      string `json:"key"`
				Title    string `json:"title"`
				Type     string `json:"type"`
				Location []struct {
					Path string `json:"path"`
				} `json:"Location"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := c.get(ctx, "/library/sections", &r); err != nil {
		return nil, err
	}
	out := make([]Library, 0, len(r.MediaContainer.Directory))
	for _, d := range r.MediaContainer.Directory {
		lib := Library{Key: d.Key, Title: d.Title, Type: d.Type}
		for _, l := range d.Location {
			if l.Path != "" {
				lib.Locations = append(lib.Locations, l.Path)
			}
		}
		out = append(out, lib)
	}
	return out, nil
}

// do sends a request whose answer has nothing worth reading (a scan trigger), checking
// only the status.
func (c *Client) do(ctx context.Context, method, path string) error {
	if c.base == "" || c.token == "" {
		return ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
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
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) // so the connection is reused
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("plex returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// RefreshPath asks Plex to scan one folder of a library section (a partial scan): new,
// changed and removed files under dir are picked up without walking the whole library.
// dir is the folder as the Plex server sees it. Plex answers at once and scans in the
// background. GET is what Plex Web and the other *arr apps send.
func (c *Client) RefreshPath(ctx context.Context, sectionKey, dir string) error {
	if sectionKey == "" {
		return errors.New("no library section")
	}
	return c.do(ctx, http.MethodGet, "/library/sections/"+url.PathEscape(sectionKey)+"/refresh?path="+queryEscape(dir))
}

// RefreshSection asks Plex to scan a whole library section.
func (c *Client) RefreshSection(ctx context.Context, sectionKey string) error {
	if sectionKey == "" {
		return errors.New("no library section")
	}
	return c.do(ctx, http.MethodGet, "/library/sections/"+url.PathEscape(sectionKey)+"/refresh")
}

// queryEscape escapes a query value with %20 for a space rather than '+': '+' only means a
// space in form encoding, and a server reading it literally would scan a folder that
// doesn't exist. A real '+' in a folder name is sent as %2B either way.
func queryEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
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
	RatingKey string `json:"rating_key"`
	// GrandparentRatingKey is the show's key for an episode ("" otherwise).
	GrandparentRatingKey string `json:"grandparent_rating_key,omitempty"`
	// ParentRatingKey is the show's key for a season (Plex lists a batch of new episodes
	// as their season), the season's for an episode.
	ParentRatingKey  string `json:"parent_rating_key,omitempty"`
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
				RatingKey            string  `json:"ratingKey"`
				GrandparentRatingKey string  `json:"grandparentRatingKey"`
				ParentRatingKey      string  `json:"parentRatingKey"`
				Type                 string  `json:"type"`
				Title                string  `json:"title"`
				GrandparentTitle     string  `json:"grandparentTitle"`
				Year                 flexInt `json:"year"`
				Thumb                string  `json:"thumb"`
				GrandparentThumb     string  `json:"grandparentThumb"`
				AddedAt              flexInt `json:"addedAt"`
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
			RatingKey: m.RatingKey, GrandparentRatingKey: m.GrandparentRatingKey, ParentRatingKey: m.ParentRatingKey, Type: m.Type, Title: m.Title,
			GrandparentTitle: m.GrandparentTitle, Year: int(m.Year), Thumb: thumb, AddedAt: int64(m.AddedAt),
		})
	}
	return out, nil
}

// Item is one movie or show in a library section, with the outside ids Plex matched it
// to (0 / "" when Plex has none).
type Item struct {
	RatingKey  string `json:"rating_key"`
	Type       string `json:"type"` // movie | show
	Title      string `json:"title"`
	Year       int    `json:"year"`
	AddedAt    int64  `json:"added_at"`
	SectionKey string `json:"section_key"`
	TMDB       int    `json:"tmdb,omitempty"`
	TVDB       int    `json:"tvdb,omitempty"`
	IMDB       string `json:"imdb,omitempty"`
}

// Section item types, as /library/sections/{key}/all?type= takes them.
const (
	TypeMovie = 1
	TypeShow  = 2
)

const sectionPageSize = 500

// SectionItems lists every movie (typ TypeMovie) or show (TypeShow) in a section with its
// guids, a page of 500 at a time, so a library of thousands is a handful of requests.
func (c *Client) SectionItems(ctx context.Context, sectionKey string, typ int) ([]Item, error) {
	var out []Item
	// A runaway totalSize can't loop forever: 400 pages is 200,000 items.
	for start, page := 0, 0; page < 400; page++ {
		var r struct {
			MediaContainer struct {
				Size      flexInt `json:"size"`
				TotalSize flexInt `json:"totalSize"`
				Metadata  []struct {
					RatingKey string  `json:"ratingKey"`
					Type      string  `json:"type"`
					Title     string  `json:"title"`
					Year      flexInt `json:"year"`
					AddedAt   flexInt `json:"addedAt"`
					GUID      string  `json:"guid"` // the agent's primary match
					// Guid (capital G, an array) is the new agents' list of outside ids.
					// encoding/json prefers the exact-case key, so the two don't collide.
					Guids []struct {
						ID string `json:"id"`
					} `json:"Guid"`
				} `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		path := fmt.Sprintf("/library/sections/%s/all?type=%d&includeGuids=1&X-Plex-Container-Start=%d&X-Plex-Container-Size=%d",
			url.PathEscape(sectionKey), typ, start, sectionPageSize)
		if err := c.get(ctx, path, &r); err != nil {
			return nil, err
		}
		for _, m := range r.MediaContainer.Metadata {
			guids := make([]string, 0, len(m.Guids))
			for _, g := range m.Guids {
				guids = append(guids, g.ID)
			}
			it := Item{RatingKey: m.RatingKey, Type: m.Type, Title: m.Title, Year: int(m.Year), AddedAt: int64(m.AddedAt), SectionKey: sectionKey}
			it.TMDB, it.TVDB, it.IMDB = parseGuids(m.GUID, guids)
			out = append(out, it)
		}
		n := len(r.MediaContainer.Metadata)
		start += n
		// totalSize says when it's done; without it, a short page is the last one. (A
		// server that caps the page below 500 still answers with totalSize.)
		if total := int(r.MediaContainer.TotalSize); n == 0 || (total > 0 && start >= total) || (total == 0 && n < sectionPageSize) {
			break
		}
	}
	return out, nil
}

// parseGuids reads the outside ids out of a Plex item's guids: the new agents' list
// (tmdb://603, tvdb://81189, imdb://tt0133093) first, then the legacy agents' primary
// guid (com.plexapp.agents.themoviedb://603?lang=en and the imdb and thetvdb
// equivalents). A plex:// primary carries no outside id and is ignored.
func parseGuids(primary string, guids []string) (tmdb, tvdb int, imdb string) {
	take := func(scheme, val string) {
		// Legacy guids carry ?lang=en, and episode guids /season/episode after the id.
		if i := strings.IndexAny(val, "?/"); i >= 0 {
			val = val[:i]
		}
		switch scheme {
		case "tmdb", "themoviedb":
			if n, err := strconv.Atoi(val); err == nil && n > 0 && tmdb == 0 {
				tmdb = n
			}
		case "tvdb", "thetvdb":
			if n, err := strconv.Atoi(val); err == nil && n > 0 && tvdb == 0 {
				tvdb = n
			}
		case "imdb":
			if strings.HasPrefix(val, "tt") && imdb == "" {
				imdb = val
			}
		}
	}
	split := func(g string) (string, string, bool) {
		scheme, val, ok := strings.Cut(strings.TrimSpace(g), "://")
		return strings.ToLower(scheme), val, ok
	}
	for _, g := range guids {
		if scheme, val, ok := split(g); ok {
			take(scheme, val)
		}
	}
	if scheme, val, ok := split(primary); ok && strings.HasPrefix(scheme, "com.plexapp.agents.") {
		take(strings.TrimPrefix(scheme, "com.plexapp.agents."), val)
	}
	return tmdb, tvdb, imdb
}

// Image fetches a Plex image (poster/art) by its metadata path, authenticated with the token, so
// Arrmada can proxy it to the browser without exposing the token. Caller closes the body.
func (c *Client) Image(ctx context.Context, imgPath string) (*http.Response, error) {
	if c.base == "" || c.token == "" {
		return nil, ErrNotConfigured
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

// flexStr tolerates fields whose JSON type Plex doesn't pin down: a string, a bool, a number
// or null all decode, as their text ("qsv", "true", "1"); false and null decode to "".
type flexStr string

func (f *flexStr) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case string:
		*f = flexStr(x)
	case bool:
		*f = ""
		if x {
			*f = "true"
		}
	case float64:
		*f = flexStr(strconv.FormatFloat(x, 'f', -1, 64))
	default: // null, or an object/array where a value was expected
		*f = ""
	}
	return nil
}

// on reads the field as a flag: set and not an explicit "no".
func (f flexStr) on() bool {
	switch strings.ToLower(strings.TrimSpace(string(f))) {
	case "", "0", "false", "none":
		return false
	}
	return true
}
