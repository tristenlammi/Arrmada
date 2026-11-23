package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// VirtualFolder is one library as Dashboard → Libraries shows it.
type VirtualFolder struct {
	Name string `json:"Name"`
	// CollectionType is movies | tvshows | music | books | homevideos | mixed ("" for mixed
	// on some servers).
	CollectionType string `json:"CollectionType"`
	// Locations are the library's folders as the Jellyfin server sees them (its own paths).
	Locations []string `json:"Locations"`
	// ItemID is the library's item id: ParentID in an items query, RefreshItem's target.
	ItemID string `json:"ItemId"`
	// RefreshStatus is "Idle" or "Active" while the library is being scanned.
	RefreshStatus string `json:"RefreshStatus"`
}

// VirtualFolders lists the server's libraries with their folders (admin key).
//
// Emby's current API lists them at /Library/VirtualFolders/Query ({Items, TotalRecordCount});
// older Emby releases only answer the bare list Jellyfin kept, so a 404 there falls back
// to it.
func (c *Client) VirtualFolders(ctx context.Context) ([]VirtualFolder, error) {
	var out []VirtualFolder
	if c.emby {
		var page struct {
			Items []VirtualFolder `json:"Items"`
		}
		err := c.call(ctx, http.MethodGet, "/Library/VirtualFolders/Query", nil, nil, &page, true)
		if !errors.Is(err, ErrNotFound) {
			return page.Items, err
		}
	}
	err := c.call(ctx, http.MethodGet, "/Library/VirtualFolders", nil, nil, &out, true)
	return out, err
}

// Item types as Jellyfin names them.
const (
	TypeMovie   = "Movie"
	TypeSeries  = "Series"
	TypeSeason  = "Season"
	TypeEpisode = "Episode"
)

// DefaultItemFields are the extra fields an index needs: outside ids, when it arrived, its
// file or folder, and an episode's show.
var DefaultItemFields = []string{"ProviderIds", "DateCreated", "Path", "SeriesId"}

// Item is one library item, with only the fields Arrmada reads.
type Item struct {
	ID             string      `json:"Id"`
	Name           string      `json:"Name"`
	Type           string      `json:"Type"` // Movie | Series | Season | Episode | Folder …
	ProductionYear int         `json:"ProductionYear"`
	Path           string      `json:"Path"` // a movie's (first version's) file, a show's folder
	DateCreated    time.Time   `json:"DateCreated"`
	ProviderIDs    ProviderIDs `json:"ProviderIds"`
	// The show and season an episode belongs to ("" otherwise), and its numbers.
	SeriesID          string `json:"SeriesId"`
	SeriesName        string `json:"SeriesName"`
	SeasonID          string `json:"SeasonId"`
	ParentIndexNumber int    `json:"ParentIndexNumber"` // season number
	IndexNumber       int    `json:"IndexNumber"`       // episode number
	RunTimeTicks      int64  `json:"RunTimeTicks"`
	// ImageTags maps an image type ("Primary") to its cache tag, for ItemImagePath.
	ImageTags map[string]string `json:"ImageTags"`
	// MediaSources are the item's versions (only with the MediaSources field). Jellyfin
	// folds several files into one movie only when each file name starts with the folder's
	// exact name and a " - " — see the package notes on naming.
	MediaSources []MediaSource `json:"MediaSources"`
}

// MediaSource is one version (file) of an item.
type MediaSource struct {
	ID        string        `json:"Id"`
	Name      string        `json:"Name"` // the version label, e.g. "2160p"
	Path      string        `json:"Path"`
	Container string        `json:"Container"`
	Size      int64         `json:"Size"`
	Bitrate   int64         `json:"Bitrate"` // bits per second
	Streams   []MediaStream `json:"MediaStreams"`
}

// MediaStream is one stream of a file (or of a now-playing item).
type MediaStream struct {
	Index        int    `json:"Index"`
	Type         string `json:"Type"` // Video | Audio | Subtitle | …
	Codec        string `json:"Codec"`
	Profile      string `json:"Profile"`  // "Main 10", "High"
	BitDepth     int    `json:"BitDepth"` // 8, 10; 0 when not reported
	Width        int    `json:"Width"`
	Height       int    `json:"Height"`
	BitRate      int64  `json:"BitRate"`
	Language     string `json:"Language"`
	VideoRange   string `json:"VideoRange"` // SDR | HDR
	DisplayTitle string `json:"DisplayTitle"`
	IsDefault    bool   `json:"IsDefault"`
	IsExternal   bool   `json:"IsExternal"`
}

// ItemsQuery is one /Items request. Zero values leave Jellyfin's defaults in place.
type ItemsQuery struct {
	// UserID scopes the query to what that user may see. An API key lists everything
	// without one (checked against Jellyfin 12.2), so the index leaves it empty.
	UserID   string
	ParentID string
	// IDs limits the answer to these items (Insights reads a playing item's versions).
	IDs              []string
	IncludeItemTypes []string
	Recursive        bool
	Fields           []string // nil means DefaultItemFields
	SortBy           []string
	SortDescending   bool
	StartIndex       int
	Limit            int
}

func (q ItemsQuery) values() url.Values {
	v := url.Values{}
	if q.UserID != "" {
		v.Set("userId", q.UserID)
	}
	if q.ParentID != "" {
		v.Set("parentId", q.ParentID)
	}
	if len(q.IDs) > 0 {
		v.Set("ids", strings.Join(q.IDs, ","))
	}
	if len(q.IncludeItemTypes) > 0 {
		v.Set("includeItemTypes", strings.Join(q.IncludeItemTypes, ","))
	}
	if q.Recursive {
		v.Set("recursive", "true")
	}
	fields := q.Fields
	if fields == nil {
		fields = DefaultItemFields
	}
	if len(fields) > 0 {
		v.Set("fields", strings.Join(fields, ","))
	}
	if len(q.SortBy) > 0 {
		v.Set("sortBy", strings.Join(q.SortBy, ","))
		if q.SortDescending {
			v.Set("sortOrder", "Descending")
		} else {
			v.Set("sortOrder", "Ascending")
		}
	}
	if q.StartIndex > 0 {
		v.Set("startIndex", strconv.Itoa(q.StartIndex))
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	// One tag per image type is all a poster link needs; the rest only bloats a page.
	v.Set("enableImages", "true")
	v.Set("imageTypeLimit", "1")
	return v
}

// ItemsPage is one page of an items query.
type ItemsPage struct {
	Items            []Item `json:"Items"`
	TotalRecordCount int    `json:"TotalRecordCount"`
	StartIndex       int    `json:"StartIndex"`
}

// Items runs one items query (one page).
func (c *Client) Items(ctx context.Context, q ItemsQuery) (ItemsPage, error) {
	var p ItemsPage
	err := c.call(ctx, http.MethodGet, "/Items", q.values(), nil, &p, true)
	return p, err
}

// itemsPageSize is how many items AllItems asks for per request (a var for tests).
var itemsPageSize = 500

// AllItems pages through a query 500 items at a time and returns every item. q's
// StartIndex and Limit are ignored.
func (c *Client) AllItems(ctx context.Context, q ItemsQuery) ([]Item, error) {
	var out []Item
	q.Limit = itemsPageSize
	// A runaway total can't loop forever: 400 pages is 200,000 items.
	for start, page := 0, 0; page < 400; page++ {
		q.StartIndex = start
		p, err := c.Items(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Items...)
		start += len(p.Items)
		if len(p.Items) == 0 || start >= p.TotalRecordCount {
			break
		}
	}
	return out, nil
}

// RecentlyAdded returns the newest movies and episodes, newest first (episodes carry their
// show in SeriesID). userID may be empty with an API key.
func (c *Client) RecentlyAdded(ctx context.Context, limit int, userID string) ([]Item, error) {
	if limit <= 0 {
		limit = 60
	}
	p, err := c.Items(ctx, ItemsQuery{
		UserID:           userID,
		IncludeItemTypes: []string{TypeMovie, TypeEpisode},
		Recursive:        true,
		Fields:           []string{"ProviderIds", "SeriesId", "DateCreated", "Path"},
		SortBy:           []string{"DateCreated"},
		SortDescending:   true,
		Limit:            limit,
	})
	return p.Items, err
}

// Update types for LibraryMediaUpdated.
const (
	UpdateCreated  = "Created"
	UpdateModified = "Modified"
	UpdateDeleted  = "Deleted"
)

// MediaUpdate is one changed path, as the server sees it.
type MediaUpdate struct {
	Path       string `json:"Path"`
	UpdateType string `json:"UpdateType"`
}

// LibraryMediaUpdated reports changed folders or files so Jellyfin scans just those (the
// *arr apps' partial scan). Jellyfin answers at once and scans once nothing new has been
// reported for its library monitor delay (60 s by default; see the package notes). Paths
// are the server's.
func (c *Client) LibraryMediaUpdated(ctx context.Context, updates []MediaUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	body := struct {
		Updates []MediaUpdate `json:"Updates"`
	}{updates}
	for i := range body.Updates {
		if body.Updates[i].UpdateType == "" {
			body.Updates[i].UpdateType = UpdateModified
		}
	}
	return c.call(ctx, http.MethodPost, "/Library/Media/Updated", nil, body, nil, true)
}

// RefreshLibrary starts a scan of every library (Dashboard → Scan All Libraries).
func (c *Client) RefreshLibrary(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/Library/Refresh", nil, nil, nil, true)
}

// RefreshItem re-reads one item (a library, show or movie) and what's under it. full also
// replaces metadata and images rather than only filling gaps.
func (c *Client) RefreshItem(ctx context.Context, itemID string, full bool) error {
	if itemID == "" {
		return errors.New("no item to refresh")
	}
	mode := "Default"
	if full {
		mode = "FullRefresh"
	}
	q := url.Values{
		"recursive":           {"true"},
		"metadataRefreshMode": {mode},
		"imageRefreshMode":    {mode},
		"replaceAllMetadata":  {strconv.FormatBool(full)},
		"replaceAllImages":    {strconv.FormatBool(full)},
		"regenerateTrickplay": {"false"},
	}
	if c.emby {
		q.Del("regenerateTrickplay") // Jellyfin's own; Emby has no such option
	}
	return c.call(ctx, http.MethodPost, "/Items/"+url.PathEscape(itemID)+"/Refresh", q, nil, nil, true)
}

// Image types for ItemImagePath.
const (
	ImagePrimary  = "Primary"
	ImageBackdrop = "Backdrop"
	ImageThumb    = "Thumb"
	ImageLogo     = "Logo"
)

// ItemImagePath is the server path of an item's image, to append to the base URL (or hand
// to Image). tag is the item's ImageTags entry ("" is fine; it only busts caches); maxWidth
// 0 means full size.
func ItemImagePath(itemID, imageType, tag string, maxWidth int) string {
	if imageType == "" {
		imageType = ImagePrimary
	}
	p := "/Items/" + url.PathEscape(itemID) + "/Images/" + url.PathEscape(imageType)
	q := url.Values{}
	if tag != "" {
		q.Set("tag", tag)
	}
	if maxWidth > 0 {
		q.Set("maxWidth", strconv.Itoa(maxWidth))
	}
	q.Set("quality", "90")
	return p + "?" + q.Encode()
}

// Image fetches an item's image through the client so a browser never needs the server's
// address or key. The path is built here from the parts, so nothing a caller passes can
// point the keyed request at another endpoint. Caller closes the body.
func (c *Client) Image(ctx context.Context, itemID, imageType string, maxWidth int) (*http.Response, error) {
	if c.base == "" {
		return nil, ErrNotConfigured
	}
	if !(isItemID(itemID) || (c.emby && isEmbyItemID(itemID))) || !validImageType(imageType) {
		return nil, errors.New("invalid image request")
	}
	op := "GET /Items/{id}/Images/" + imageType
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase()+ItemImagePath(itemID, imageType, "", maxWidth), nil)
	if err != nil {
		return nil, &Error{Op: op, Kind: ErrNotConfigured, Detail: "the server address isn't a valid URL"}
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Op: op, Kind: ErrUnreachable, Detail: netDetail(err)}
	}
	if resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, statusError(op, resp.StatusCode, nil)
	}
	return resp, nil
}

// isItemID: Jellyfin ids are 32 hex digits (a GUID without dashes); the dashed form is
// accepted too.
func isItemID(s string) bool {
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// isEmbyItemID: Emby numbers its library items ("12345") where Jellyfin uses GUIDs.
func isEmbyItemID(s string) bool {
	if s == "" || len(s) > 19 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// IsEmbyItemID reports whether s is an Emby item number (digits only).
func IsEmbyItemID(s string) bool { return isEmbyItemID(s) }

func validImageType(t string) bool {
	switch t {
	case ImagePrimary, ImageBackdrop, ImageThumb, ImageLogo:
		return true
	}
	return false
}

// WatchURL is the link that opens an item in Jellyfin's web app:
// {public}/web/#/details?id=…&serverId=… (the hash route jellyfin-web 12.2 registers as
// "details"). publicBase is the address people reach the server at, not necessarily the one
// Arrmada uses; serverID is SystemInfo.ID.
func WatchURL(publicBase, serverID, itemID string) string {
	publicBase = strings.TrimRight(strings.TrimSpace(publicBase), "/")
	if publicBase == "" || itemID == "" {
		return ""
	}
	q := url.Values{}
	q.Set("id", itemID)
	if serverID != "" {
		q.Set("serverId", serverID)
	}
	return publicBase + "/web/#/details?" + q.Encode()
}

// WatchURLFor is WatchURL for a server of the given kind. Emby's web app opens an item at
// {public}/web/index.html#!/item?id=…&serverId=… (with the server id in lower case).
func WatchURLFor(kind, publicBase, serverID, itemID string) string {
	if NormalizeServerKind(kind) != ServerEmby {
		return WatchURL(publicBase, serverID, itemID)
	}
	publicBase = strings.TrimRight(strings.TrimSpace(publicBase), "/")
	if publicBase == "" || itemID == "" {
		return ""
	}
	q := url.Values{}
	q.Set("id", itemID)
	if serverID != "" {
		q.Set("serverId", strings.ToLower(serverID))
	}
	return publicBase + "/web/index.html#!/item?" + q.Encode()
}
