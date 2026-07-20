package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// TVDB is TheTVDB v4, used as the episode-numbering source when a key is configured.
//
// It matters most for anime and for any show with two-part episodes: TVDB numbers seasons
// the way releases and existing libraries do (Naruto Shippuden is 22 seasons here, 20 on
// TMDB), and — crucially — it carries an authoritative absolute number per episode. Arrmada
// otherwise derives absolute numbers by counting, which drifts the moment the season list
// is wrong. The absolute number is what anime releases ("Show - 137") are matched against.
//
// TVDB needs a key (free for personal use, with attribution). Without one this source is
// simply unavailable and numbering falls back to TVmaze, then TMDB.

const (
	tvdbBase        = "https://api4.thetvdb.com/v4"
	tvdbMinInterval = 200 * time.Millisecond
	tvdbTimeout     = 20 * time.Second
	tvdbMaxPages    = 20 // a very long anime tops out well under this at 500/page
)

// TVDB fetches episode listings, numbered the way releases are, with absolute numbers.
type TVDB struct {
	http *http.Client
	base string
	key  func() string // read lazily so a key added in settings takes effect live

	mu    sync.Mutex
	token string    // cached bearer token
	tokAt time.Time // when it was minted (re-login well before the ~1 month expiry)
	last  time.Time // request pacing
}

// NewTVDB builds the client. key resolves the API key lazily; empty means unavailable.
func NewTVDB(key func() string) *TVDB {
	return &TVDB{http: &http.Client{Timeout: tvdbTimeout}, base: tvdbBase, key: key}
}

// Available reports whether a key is configured, so the decorator can skip TVDB entirely
// when it can't be used.
func (t *TVDB) Available() bool { return t != nil && t.key() != "" }

// Episodes returns a show's seasons and episodes in TVDB's aired-order numbering, each
// carrying its absolute number. Matched by TVDB id — which TMDB already gives us. A show
// with no TVDB id, or one TVDB can't resolve, returns nil with no error so the caller
// falls back.
func (t *TVDB) Episodes(ctx context.Context, tvdbID int, _ string) ([]SeasonDetails, error) {
	if !t.Available() || tvdbID <= 0 {
		return nil, nil
	}
	eps, err := t.allEpisodes(ctx, tvdbID)
	if err != nil {
		return nil, err
	}
	return groupTVDB(eps), nil
}

// tvdbEpisode is the subset of the v4 episode record we use.
type tvdbEpisode struct {
	Name           string `json:"name"`
	Aired          string `json:"aired"`
	Runtime        int    `json:"runtime"`
	Overview       string `json:"overview"`
	Image          string `json:"image"`
	SeasonNumber   int    `json:"seasonNumber"`
	Number         int    `json:"number"`
	AbsoluteNumber int    `json:"absoluteNumber"`
}

// allEpisodes pages through /series/{id}/episodes/default (aired-order seasons).
func (t *TVDB) allEpisodes(ctx context.Context, tvdbID int) ([]tvdbEpisode, error) {
	var out []tvdbEpisode
	for page := 0; page < tvdbMaxPages; page++ {
		var body struct {
			Data struct {
				Episodes []tvdbEpisode `json:"episodes"`
			} `json:"data"`
			Links struct {
				Next string `json:"next"`
			} `json:"links"`
		}
		// The "/eng" segment asks for English episode titles; without it TVDB returns the
		// series' original language (Japanese for anime), which would replace every
		// episode title in the library with kanji.
		path := fmt.Sprintf("/series/%d/episodes/default/eng?page=%d", tvdbID, page)
		if err := t.get(ctx, path, &body); err != nil {
			if isNotFound(err) {
				return nil, nil // TVDB doesn't have this show — fall back, not an error
			}
			return nil, err
		}
		out = append(out, body.Data.Episodes...)
		if body.Links.Next == "" || len(body.Data.Episodes) == 0 {
			break
		}
	}
	return out, nil
}

// groupTVDB turns a flat episode list into seasons, carrying the absolute number and
// keeping specials (season 0), matching what TMDB and TVmaze return.
func groupTVDB(eps []tvdbEpisode) []SeasonDetails {
	bySeason := map[int]*SeasonDetails{}
	for _, e := range eps {
		sd := bySeason[e.SeasonNumber]
		if sd == nil {
			sd = &SeasonDetails{SeasonNumber: e.SeasonNumber}
			bySeason[e.SeasonNumber] = sd
		}
		sd.Episodes = append(sd.Episodes, EpisodeDetails{
			EpisodeNumber:  e.Number,
			Title:          e.Name,
			Overview:       e.Overview,
			AirDate:        e.Aired,
			Runtime:        e.Runtime,
			StillURL:       e.Image,
			AbsoluteNumber: e.AbsoluteNumber,
		})
	}
	out := make([]SeasonDetails, 0, len(bySeason))
	for _, sd := range bySeason {
		sort.Slice(sd.Episodes, func(i, j int) bool { return sd.Episodes[i].EpisodeNumber < sd.Episodes[j].EpisodeNumber })
		out = append(out, *sd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SeasonNumber < out[j].SeasonNumber })
	return out
}

// get performs an authenticated GET, logging in on demand and retrying once if the token
// was rejected.
func (t *TVDB) get(ctx context.Context, path string, out any) error {
	token, err := t.ensureToken(ctx)
	if err != nil {
		return err
	}
	status, err := t.do(ctx, path, token, out)
	if err == nil {
		return nil
	}
	if status == http.StatusUnauthorized {
		// Token expired or revoked — mint a fresh one and try once more.
		t.mu.Lock()
		t.token = ""
		t.mu.Unlock()
		if token, err = t.ensureToken(ctx); err != nil {
			return err
		}
		_, err = t.do(ctx, path, token, out)
	}
	return err
}

func (t *TVDB) do(ctx context.Context, path, token string, out any) (int, error) {
	t.pace()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.base+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := t.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("tvdb: %s returned %s", path, resp.Status)
	}
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
}

// ensureToken returns a valid bearer token, logging in when there isn't one. TVDB tokens
// last about a month; re-login after three weeks keeps well clear of the edge.
func (t *TVDB) ensureToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	if t.token != "" && time.Since(t.tokAt) < 21*24*time.Hour {
		tok := t.token
		t.mu.Unlock()
		return tok, nil
	}
	t.mu.Unlock()

	key := t.key()
	if key == "" {
		return "", ErrNotConfigured
	}
	t.pace()
	reqBody, _ := json.Marshal(map[string]string{"apikey": key})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/login", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tvdb login failed: %s (check the API key)", resp.Status)
	}
	var body struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Data.Token == "" {
		return "", fmt.Errorf("tvdb login returned no token")
	}
	t.mu.Lock()
	t.token, t.tokAt = body.Data.Token, time.Now()
	t.mu.Unlock()
	return body.Data.Token, nil
}

func (t *TVDB) pace() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if wait := tvdbMinInterval - time.Since(t.last); wait > 0 {
		time.Sleep(wait)
	}
	t.last = time.Now()
}
