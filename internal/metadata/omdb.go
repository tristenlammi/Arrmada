package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// OMDb supplies external ratings (IMDB / Rotten Tomatoes / Metacritic) keyed by IMDB
// id — the one thing TMDB doesn't carry. A free key from omdbapi.com. Optional: with
// no key, Available() is false and the detail modal falls back to the TMDB score.
type OMDb struct {
	key  func() string
	http *http.Client
	base string // API base URL (overridable in tests)
}

// NewOMDb builds an OMDb client. apiKey may be empty (Available reports false).
func NewOMDb(apiKey string) *OMDb { return NewOMDbFunc(func() string { return apiKey }) }

// NewOMDbFunc builds an OMDb client that reads its key lazily (settings-backed).
func NewOMDbFunc(key func() string) *OMDb {
	return &OMDb{key: key, http: &http.Client{Timeout: 12 * time.Second}, base: "https://www.omdbapi.com/"}
}

// Available reports whether an OMDb API key is configured.
func (o *OMDb) Available() bool { return o.key() != "" }

// Ratings returns IMDB / Rotten Tomatoes / Metacritic scores for an IMDB id.
func (o *OMDb) Ratings(ctx context.Context, imdbID string) (Ratings, error) {
	if !o.Available() {
		return Ratings{}, ErrNotConfigured
	}
	if imdbID == "" {
		return Ratings{}, fmt.Errorf("omdb: no imdb id")
	}
	q := url.Values{}
	q.Set("apikey", o.key())
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
		return Ratings{}, err
	}
	if payload.Response == "False" {
		return Ratings{}, fmt.Errorf("omdb: %s", payload.Error)
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
