package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// TVmaze supplies EPISODE NUMBERING, and nothing else.
//
// TMDB is the better source for artwork, overviews, discovery and catalogue breadth, and
// remains how a show is identified. What it isn't good at is agreeing with how releases
// are numbered: TMDB merges two-part episodes into one entry where the rest of the world
// splits them, so "Parks and Recreation 6x03" resolved onto TMDB's episode 3 when it was
// really TMDB's episode 2 — and every episode after it in the season shifted with it.
//
// Releases are numbered against TVDB's convention. TVmaze follows the same convention and
// is free with no API key, which matters for a self-hosted app that has to work the
// moment it's installed. Sonarr uses TVDB itself, but only because it runs a central
// proxy holding that relationship for every install — infrastructure Arrmada doesn't have.
//
// Matching is by TVDB or IMDb id, both of which TMDB already hands us, so no show has to
// be re-identified.

const (
	tvmazeBase = "https://api.tvmaze.com"
	// tvmazeMinInterval paces requests. TVmaze asks for roughly 20 calls per 10 seconds;
	// a library refresh walks every series, so this keeps a sweep well inside that
	// without needing a token bucket.
	tvmazeMinInterval = 250 * time.Millisecond
	tvmazeTimeout     = 15 * time.Second
)

// TVmaze fetches episode listings numbered the way releases are.
type TVmaze struct {
	http *http.Client
	base string // overridden in tests; the public API otherwise

	mu   sync.Mutex
	last time.Time // request pacing
}

// NewTVmaze builds a client. No API key exists or is needed.
func NewTVmaze() *TVmaze {
	return &TVmaze{http: &http.Client{Timeout: tvmazeTimeout}, base: tvmazeBase}
}

// Available reports whether the source can be used. TVmaze needs no configuration, so
// it's always available — failures are handled per-request by falling back.
func (t *TVmaze) Available() bool { return t != nil }

// Episodes returns a show's seasons and episodes, matched by TVDB id (preferred) or IMDb
// id. Returns nil with no error when the show simply isn't on TVmaze — a miss is normal
// and the caller keeps its existing numbering.
func (t *TVmaze) Episodes(ctx context.Context, tvdbID int, imdbID string) ([]SeasonDetails, error) {
	showID, err := t.lookup(ctx, tvdbID, imdbID)
	if err != nil || showID == 0 {
		return nil, err
	}
	return t.episodesFor(ctx, showID)
}

// lookup resolves a TVmaze show id from the external ids TMDB already gave us.
func (t *TVmaze) lookup(ctx context.Context, tvdbID int, imdbID string) (int, error) {
	try := func(param, value string) (int, error) {
		var show struct {
			ID int `json:"id"`
		}
		err := t.get(ctx, "/lookup/shows?"+param+"="+url.QueryEscape(value), &show)
		return show.ID, err
	}
	if tvdbID > 0 {
		if id, err := try("thetvdb", fmt.Sprint(tvdbID)); err == nil && id > 0 {
			return id, nil
		} else if err != nil && !isNotFound(err) {
			return 0, err
		}
	}
	if imdbID != "" {
		if id, err := try("imdb", imdbID); err == nil && id > 0 {
			return id, nil
		} else if err != nil && !isNotFound(err) {
			return 0, err
		}
	}
	return 0, nil // not on TVmaze — not an error
}

// episodesFor pulls the full episode list, specials included, and groups it by season.
func (t *TVmaze) episodesFor(ctx context.Context, showID int) ([]SeasonDetails, error) {
	var eps []struct {
		Season  int    `json:"season"`
		Number  *int   `json:"number"` // null for specials on some entries
		Name    string `json:"name"`
		AirDate string `json:"airdate"`
		Runtime *int   `json:"runtime"`
		Summary string `json:"summary"`
		Image   *struct {
			Medium   string `json:"medium"`
			Original string `json:"original"`
		} `json:"image"`
	}
	// specials=1 keeps season 0 present, matching what TMDB returns today.
	if err := t.get(ctx, fmt.Sprintf("/shows/%d/episodes?specials=1", showID), &eps); err != nil {
		return nil, err
	}

	bySeason := map[int]*SeasonDetails{}
	for _, e := range eps {
		if e.Number == nil {
			continue // unnumbered — nothing we can place a file against
		}
		sd := bySeason[e.Season]
		if sd == nil {
			sd = &SeasonDetails{SeasonNumber: e.Season}
			bySeason[e.Season] = sd
		}
		still := ""
		if e.Image != nil {
			still = e.Image.Original
			if still == "" {
				still = e.Image.Medium
			}
		}
		runtime := 0
		if e.Runtime != nil {
			runtime = *e.Runtime
		}
		sd.Episodes = append(sd.Episodes, EpisodeDetails{
			EpisodeNumber: *e.Number,
			Title:         e.Name,
			Overview:      stripHTML(e.Summary),
			AirDate:       e.AirDate,
			Runtime:       runtime,
			StillURL:      still,
		})
	}

	out := make([]SeasonDetails, 0, len(bySeason))
	for _, sd := range bySeason {
		sort.Slice(sd.Episodes, func(i, j int) bool { return sd.Episodes[i].EpisodeNumber < sd.Episodes[j].EpisodeNumber })
		out = append(out, *sd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SeasonNumber < out[j].SeasonNumber })
	return out, nil
}

// errNotFound marks a 404 — "this show isn't on TVmaze", which is an ordinary answer
// rather than a failure.
var errNotFound = fmt.Errorf("not found")

func isNotFound(err error) bool { return err == errNotFound }

func (t *TVmaze) get(ctx context.Context, path string, out any) error {
	t.pace()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tvmaze: %s returned %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// pace spaces requests so a library-wide refresh stays within TVmaze's rate limit.
func (t *TVmaze) pace() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if wait := tvmazeMinInterval - time.Since(t.last); wait > 0 {
		time.Sleep(wait)
	}
	t.last = time.Now()
}

// stripHTML flattens TVmaze's HTML summaries into plain text — they arrive wrapped in
// <p> tags, which would otherwise show up verbatim in the UI.
func stripHTML(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(b.String()), " "))
}
