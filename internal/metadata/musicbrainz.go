package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MusicBrainz is the music metadata provider. Like Open Library for books it needs no API
// key and is open data, which keeps the Music module off a single proprietary dependency.
//
// Two hard requirements the service enforces and will block you for ignoring:
//   - every request must carry a descriptive User-Agent with contact info;
//   - anonymous clients get ONE request per second.
//
// Both are handled here rather than left to callers.
type MusicBrainz struct {
	http *http.Client
	base string // musicbrainz.org web-service root; overridden in tests
	art  string // Cover Art Archive root; overridden in tests

	mu sync.Mutex
	// minInterval is the pacing gap. A field rather than a constant so tests can run
	// without sleeping a second between every request.
	minInterval time.Duration
	last        time.Time
}

const (
	mbBaseURL     = "https://musicbrainz.org/ws/2"
	caaBaseURL    = "https://coverartarchive.org"
	mbUserAgent   = "Arrmada/1.0 ( https://github.com/tristenlammi/Arrmada )"
	mbMinInterval = 1100 * time.Millisecond // the documented limit is 1/sec; leave headroom
	mbTimeout     = 20 * time.Second
)

// NewMusicBrainz builds the provider.
func NewMusicBrainz() *MusicBrainz {
	return &MusicBrainz{
		http: &http.Client{Timeout: mbTimeout},
		base: mbBaseURL, art: caaBaseURL, minInterval: mbMinInterval,
	}
}

// Available is always true — no key required.
func (m *MusicBrainz) Available() bool { return true }

// ArtistResult is one artist from a search.
type ArtistResult struct {
	MBID string `json:"mbid"`
	Name string `json:"name"`
	// Disambiguation is MusicBrainz's short qualifier ("UK punk band"), which is often the
	// only way to tell two same-named artists apart.
	Disambiguation string   `json:"disambiguation,omitempty"`
	SortName       string   `json:"sort_name,omitempty"`
	Country        string   `json:"country,omitempty"`
	Type           string   `json:"type,omitempty"` // Person | Group | …
	Genres         []string `json:"genres,omitempty"`
}

// ArtistDetails is a full artist record.
type ArtistDetails struct {
	ArtistResult
	Overview string `json:"overview,omitempty"`
}

// AlbumResult is one release group (an "album") by an artist.
type AlbumResult struct {
	MBID        string `json:"mbid"`
	Title       string `json:"title"`
	Type        string `json:"type,omitempty"` // Album | EP | Single | Live | Compilation…
	ReleaseDate string `json:"release_date,omitempty"`
	Year        int    `json:"year,omitempty"`
	CoverURL    string `json:"cover_url,omitempty"`
}

// TrackResult is one recording on an album.
type TrackResult struct {
	MBID        string `json:"mbid,omitempty"`
	Title       string `json:"title"`
	Disc        int    `json:"disc"`
	Number      int    `json:"number"`
	DurationSec int    `json:"duration_sec,omitempty"`
}

// MusicProvider is the metadata surface the Music module needs.
type MusicProvider interface {
	Available() bool
	SearchArtists(ctx context.Context, query string) ([]ArtistResult, error)
	GetArtist(ctx context.Context, mbid string) (*ArtistDetails, error)
	// ArtistAlbums returns the artist's album-type release groups, newest first.
	ArtistAlbums(ctx context.Context, mbid string) ([]AlbumResult, error)
	// AlbumTracks returns the track listing for a release group, taken from a
	// representative release within it.
	AlbumTracks(ctx context.Context, releaseGroupMBID string) ([]TrackResult, error)
}

// SearchArtists finds artists by name.
func (m *MusicBrainz) SearchArtists(ctx context.Context, query string) ([]ArtistResult, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	var body struct {
		Artists []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			SortName       string `json:"sort-name"`
			Disambiguation string `json:"disambiguation"`
			Country        string `json:"country"`
			Type           string `json:"type"`
		} `json:"artists"`
	}
	if err := m.get(ctx, "/artist", url.Values{"query": {q}, "limit": {"15"}}, &body); err != nil {
		return nil, err
	}
	out := make([]ArtistResult, 0, len(body.Artists))
	for _, a := range body.Artists {
		out = append(out, ArtistResult{
			MBID: a.ID, Name: a.Name, SortName: a.SortName,
			Disambiguation: a.Disambiguation, Country: a.Country, Type: a.Type,
		})
	}
	return out, nil
}

// GetArtist fetches one artist, including genres.
func (m *MusicBrainz) GetArtist(ctx context.Context, mbid string) (*ArtistDetails, error) {
	if strings.TrimSpace(mbid) == "" {
		return nil, fmt.Errorf("musicbrainz: empty artist id")
	}
	var body struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		SortName       string `json:"sort-name"`
		Disambiguation string `json:"disambiguation"`
		Country        string `json:"country"`
		Type           string `json:"type"`
		Genres         []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"genres"`
	}
	if err := m.get(ctx, "/artist/"+url.PathEscape(mbid), url.Values{"inc": {"genres"}}, &body); err != nil {
		return nil, err
	}
	// Genres come back unordered with a vote count; the most-voted few are the useful ones.
	sort.Slice(body.Genres, func(i, j int) bool { return body.Genres[i].Count > body.Genres[j].Count })
	genres := make([]string, 0, 5)
	for _, g := range body.Genres {
		if len(genres) == 5 {
			break
		}
		genres = append(genres, g.Name)
	}
	return &ArtistDetails{ArtistResult: ArtistResult{
		MBID: body.ID, Name: body.Name, SortName: body.SortName,
		Disambiguation: body.Disambiguation, Country: body.Country, Type: body.Type, Genres: genres,
	}}, nil
}

// ArtistAlbums returns the artist's albums and EPs, newest first.
//
// Deliberately excludes singles and the secondary types (compilation, live, remix): a
// discography import that pulled every single would bury the actual albums and set hundreds
// of unobtainable things "wanted".
func (m *MusicBrainz) ArtistAlbums(ctx context.Context, mbid string) ([]AlbumResult, error) {
	if strings.TrimSpace(mbid) == "" {
		return nil, fmt.Errorf("musicbrainz: empty artist id")
	}
	var out []AlbumResult
	for offset := 0; offset < 500; offset += 100 {
		var body struct {
			ReleaseGroups []struct {
				ID               string   `json:"id"`
				Title            string   `json:"title"`
				PrimaryType      string   `json:"primary-type"`
				SecondaryTypes   []string `json:"secondary-types"`
				FirstReleaseDate string   `json:"first-release-date"`
			} `json:"release-groups"`
			Count int `json:"release-group-count"`
		}
		q := url.Values{
			"artist": {mbid}, "type": {"album|ep"},
			"limit": {"100"}, "offset": {strconv.Itoa(offset)},
		}
		if err := m.get(ctx, "/release-group", q, &body); err != nil {
			return nil, err
		}
		for _, rg := range body.ReleaseGroups {
			if len(rg.SecondaryTypes) > 0 {
				continue // live/compilation/remix editions of an album already listed
			}
			out = append(out, AlbumResult{
				MBID: rg.ID, Title: rg.Title, Type: mbOrDefault(rg.PrimaryType, "Album"),
				ReleaseDate: rg.FirstReleaseDate, Year: mbYearOf(rg.FirstReleaseDate),
				CoverURL: m.art + "/release-group/" + rg.ID + "/front-500",
			})
		}
		if len(body.ReleaseGroups) < 100 {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Year > out[j].Year })
	return out, nil
}

// AlbumTracks returns a release group's track listing.
//
// A release group holds many releases (regional pressings, reissues, deluxe editions) with
// different track counts. We take the earliest official release with a track listing, which
// is the standard edition a release is most likely to match — picking the deluxe edition
// would mark bonus tracks missing forever on a perfectly complete album.
func (m *MusicBrainz) AlbumTracks(ctx context.Context, releaseGroupMBID string) ([]TrackResult, error) {
	if strings.TrimSpace(releaseGroupMBID) == "" {
		return nil, fmt.Errorf("musicbrainz: empty release-group id")
	}
	var list struct {
		Releases []struct {
			ID     string `json:"id"`
			Date   string `json:"date"`
			Status string `json:"status"`
		} `json:"releases"`
	}
	q := url.Values{"release-group": {releaseGroupMBID}, "limit": {"25"}}
	if err := m.get(ctx, "/release", q, &list); err != nil {
		return nil, err
	}
	pick := ""
	pickDate := ""
	for _, r := range list.Releases {
		if r.Status != "" && !strings.EqualFold(r.Status, "Official") {
			continue
		}
		if pick == "" || (r.Date != "" && (pickDate == "" || r.Date < pickDate)) {
			pick, pickDate = r.ID, r.Date
		}
	}
	if pick == "" {
		return nil, nil // nothing official to read a listing from — not an error
	}

	var rel struct {
		Media []struct {
			Position int `json:"position"`
			Tracks   []struct {
				ID       string `json:"id"`
				Title    string `json:"title"`
				Position int    `json:"position"`
				Length   int    `json:"length"` // milliseconds
			} `json:"tracks"`
		} `json:"media"`
	}
	if err := m.get(ctx, "/release/"+url.PathEscape(pick), url.Values{"inc": {"recordings"}}, &rel); err != nil {
		return nil, err
	}
	var out []TrackResult
	for _, md := range rel.Media {
		disc := md.Position
		if disc < 1 {
			disc = 1
		}
		for _, t := range md.Tracks {
			out = append(out, TrackResult{
				MBID: t.ID, Title: t.Title, Disc: disc, Number: t.Position,
				DurationSec: t.Length / 1000,
			})
		}
	}
	return out, nil
}

// mbRetries is how many times a 503 is retried before giving up. MusicBrainz answers 503
// both for "you're over the rate limit" and for ordinary server load, and it does so often
// enough that a single-shot client fails on perfectly normal use — the first live run of
// this client got one on its second call.
const mbRetries = 3

// get performs a paced, JSON web-service request, retrying a 503 with backoff.
func (m *MusicBrainz) get(ctx context.Context, path string, q url.Values, out any) error {
	if q == nil {
		q = url.Values{}
	}
	q.Set("fmt", "json")

	var lastStatus string
	for attempt := 0; attempt <= mbRetries; attempt++ {
		if attempt > 0 {
			// Back off progressively on top of the normal pacing. Scaled off the pacing
			// interval so a test that disables pacing doesn't sleep for seconds either.
			backoff := time.Duration(attempt) * m.minInterval
			if backoff > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
			}
		}
		m.pace()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+path+"?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		// MusicBrainz blocks requests without a descriptive User-Agent outright.
		req.Header.Set("User-Agent", mbUserAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := m.http.Do(req)
		if err != nil {
			return fmt.Errorf("musicbrainz: %w", err)
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return errNotFound
		}
		if resp.StatusCode == http.StatusServiceUnavailable {
			resp.Body.Close()
			lastStatus = resp.Status
			continue // rate-limited or briefly overloaded — wait and try again
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("musicbrainz: %s returned %s", path, resp.Status)
		}
		err = json.NewDecoder(resp.Body).Decode(out)
		resp.Body.Close()
		return err
	}
	return fmt.Errorf("musicbrainz: %s still returning %s after %d retries", path, lastStatus, mbRetries)
}

// pace holds anonymous clients to one request per second, which is what the service asks
// for and enforces with blocks.
func (m *MusicBrainz) pace() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if wait := m.minInterval - time.Since(m.last); wait > 0 {
		time.Sleep(wait)
	}
	m.last = time.Now()
}

func mbOrDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// mbYearOf reads the leading year from a MusicBrainz date ("1997", "1997-09", "1997-09-30").
func mbYearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return y
}
