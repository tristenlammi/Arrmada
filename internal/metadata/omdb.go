package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// omdbTTL is how long a title's ratings are kept. Scores move slowly, and the free key
// allows 1,000 requests a day.
const omdbTTL = 7 * 24 * time.Hour

// omdbNotFoundTTL is how long "OMDb has no such title" is kept: OMDb may add it later.
const omdbNotFoundTTL = 24 * time.Hour

// omdbErrLogEvery spaces out the log line for a failing key (a spent quota fails every
// detail sheet opened until midnight).
const omdbErrLogEvery = time.Hour

// OMDb supplies external ratings (IMDB / Rotten Tomatoes / Metacritic) keyed by IMDB
// id — the one thing TMDB doesn't carry. A free key from omdbapi.com. Optional: with
// no key, Available() is false and the detail modal falls back to the TMDB score.
type OMDb struct {
	key  func() string
	http *http.Client
	base string     // API base URL (overridable in tests)
	disk *DiskCache // ratings kept across opens and restarts; nil = none
	log  *slog.Logger

	// The last failure and when, for Settings → System → API keys, and which key it was
	// made with: a failure with a key since replaced says nothing about the new one.
	mu        sync.Mutex
	lastErr   string
	lastErrAt time.Time
	lastErrOf string
	loggedAt  time.Time
}

// SetDiskCache keeps ratings across opens and restarts (see DiskCache).
func (o *OMDb) SetDiskCache(c *DiskCache) { o.disk = c }

// SetLogger is where a failing key is reported (at most once an hour); nil logs nothing.
func (o *OMDb) SetLogger(l *slog.Logger) { o.log = l }

// LastError is the most recent failure to fetch ratings with the current key and when it
// happened; empty once a request with it works again.
func (o *OMDb) LastError() (string, time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lastErr == "" || o.lastErrOf != o.key() {
		return "", time.Time{}
	}
	return o.lastErr, o.lastErrAt
}

// noteResult records a ratings fetch's outcome for LastError, and logs a failure at most
// once an hour.
func (o *OMDb) noteResult(key string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err == nil {
		o.lastErr, o.lastErrAt, o.lastErrOf = "", time.Time{}, ""
		return
	}
	now := time.Now()
	o.lastErr, o.lastErrAt, o.lastErrOf = err.Error(), now, key
	if o.log != nil && now.Sub(o.loggedAt) >= omdbErrLogEvery {
		o.loggedAt = now
		o.log.Warn("OMDb ratings unavailable; detail sheets show the TMDB score only", "err", err)
	}
}

// NewOMDb builds an OMDb client. apiKey may be empty (Available reports false).
func NewOMDb(apiKey string) *OMDb { return NewOMDbFunc(func() string { return apiKey }) }

// NewOMDbFunc builds an OMDb client that reads its key lazily (settings-backed).
func NewOMDbFunc(key func() string) *OMDb {
	return &OMDb{key: key, http: &http.Client{Timeout: 12 * time.Second}, base: "https://www.omdbapi.com/"}
}

// Available reports whether an OMDb API key is configured.
func (o *OMDb) Available() bool { return o.key() != "" }

// VerifyKey is the key Test: one real lookup (The Shawshank Redemption) with candidate —
// a key typed and not yet saved, sent once and kept nowhere — or the saved key when
// that's empty. OMDb's own complaint is passed through as it says it ("Invalid API key!",
// "Request limit reached!"), since that's what tells an unactivated key from a spent one.
func (o *OMDb) VerifyKey(ctx context.Context, candidate string) (string, error) {
	key := strings.TrimSpace(candidate)
	if key == "" {
		key = o.key()
	}
	if key == "" {
		return "", ErrNotConfigured
	}
	q := url.Values{}
	q.Set("apikey", key)
	q.Set("i", "tt0111161")
	full := o.base + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return "", sanitizeErr(full, err)
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return "", sanitizeErr(full, fmt.Errorf("Couldn't reach OMDb: %w", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var payload struct {
		Response string `json:"Response"`
		Error    string `json:"Error"`
		Title    string `json:"Title"`
	}
	// OMDb answers a bad key with a 401 and the usual JSON, so read the body either way.
	if json.Unmarshal(body, &payload) != nil {
		return "", fmt.Errorf("OMDb answered HTTP %d with something that isn't JSON", resp.StatusCode)
	}
	if payload.Response != "True" {
		msg := payload.Error
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return "", rejectedKey("OMDb: " + msg)
		}
		return "", fmt.Errorf("OMDb: %s", msg)
	}
	if payload.Title == "" {
		return "OK: OMDb accepted the key.", nil
	}
	return fmt.Sprintf("OK: OMDb answered with %s.", payload.Title), nil
}

// omdbEntry is a cached ratings answer; NotFound marks OMDb not knowing the title, which
// is kept for a shorter time.
type omdbEntry struct {
	Ratings  Ratings `json:"ratings"`
	NotFound bool    `json:"not_found,omitempty"`
}

// errOMDbNotFound is OMDb's "no such title" answer, which isn't a failure of the key.
var errOMDbNotFound = errors.New("omdb: title not found")

// Ratings returns IMDB / Rotten Tomatoes / Metacritic scores for an IMDB id, from the
// cache when it can (7 days; a title OMDb doesn't know, 1 day). A title OMDb doesn't
// know is empty Ratings and no error. Key and quota errors ("Invalid API key!",
// "Request limit reached!") are returned, never cached, and kept for LastError.
func (o *OMDb) Ratings(ctx context.Context, imdbID string) (Ratings, error) {
	if !o.Available() {
		return Ratings{}, ErrNotConfigured
	}
	if imdbID == "" {
		return Ratings{}, fmt.Errorf("omdb: no imdb id")
	}
	ttl := func(e omdbEntry) time.Duration {
		if e.NotFound {
			return omdbNotFoundTTL
		}
		return omdbTTL
	}
	e, err := swrTTL(ctx, o.disk, "omdb:"+imdbID, ttl, func(ctx context.Context) (omdbEntry, error) {
		key := o.key()
		rt, err := o.fetchRatings(ctx, key, imdbID)
		if errors.Is(err, errOMDbNotFound) {
			o.noteResult(key, nil) // the key worked
			return omdbEntry{NotFound: true}, nil
		}
		if err == nil || ctx.Err() == nil {
			o.noteResult(key, err) // a cancelled request says nothing about the key
		}
		return omdbEntry{Ratings: rt}, err
	})
	if err != nil {
		return Ratings{}, err
	}
	return e.Ratings, nil
}

// fetchRatings asks OMDb for one title's scores.
func (o *OMDb) fetchRatings(ctx context.Context, key, imdbID string) (Ratings, error) {
	q := url.Values{}
	q.Set("apikey", key)
	q.Set("i", imdbID)
	full := o.base + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return Ratings{}, sanitizeErr(full, err)
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return Ratings{}, sanitizeErr(full, fmt.Errorf("omdb request: %w", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var payload struct {
		Response   string `json:"Response"`
		Error      string `json:"Error"`
		IMDBRating string `json:"imdbRating"`
		Metascore  string `json:"Metascore"`
		Ratings    []struct {
			Source string `json:"Source"`
			Value  string `json:"Value"`
		} `json:"Ratings"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Ratings{}, fmt.Errorf("omdb: HTTP %d with something that isn't JSON", resp.StatusCode)
	}
	if payload.Response == "False" {
		// "Movie not found!" / "Incorrect IMDb ID." mean OMDb doesn't know the title;
		// anything else ("Invalid API key!", "Request limit reached!") is the key's.
		if msg := strings.ToLower(payload.Error); strings.Contains(msg, "not found") || strings.Contains(msg, "incorrect imdb id") {
			return Ratings{}, errOMDbNotFound
		}
		return Ratings{}, fmt.Errorf("OMDb: %s", payload.Error)
	}
	out := Ratings{}
	if payload.IMDBRating != "" && payload.IMDBRating != "N/A" {
		out.IMDB = payload.IMDBRating
	}
	if payload.Metascore != "" && payload.Metascore != "N/A" {
		out.Metacritic = payload.Metascore + "/100"
	}
	for _, r := range payload.Ratings {
		if r.Source == "Rotten Tomatoes" && r.Value != "N/A" {
			out.RottenTomatoes = r.Value
		}
	}
	return out, nil
}
