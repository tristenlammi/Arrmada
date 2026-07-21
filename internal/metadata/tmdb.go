package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	tmdbBaseURL      = "https://api.themoviedb.org/3"
	tmdbImageBase    = "https://image.tmdb.org/t/p/w500"
	tmdbBackdropBase = "https://image.tmdb.org/t/p/w1280"
	tmdbProfileBase  = "https://image.tmdb.org/t/p/w185"
	maxCast          = 15
)

// TMDB is a The Movie Database provider (v3 API key).
type TMDB struct {
	key  func() string
	http *http.Client
	base string // API base URL (tmdbBaseURL; overridable in tests)

	discMu    sync.Mutex
	discCache map[string]discoverCacheEntry // browse/search list cache (TTL-only)
}

// NewTMDB builds a TMDB provider from a static key (env/config). Empty means unconfigured.
func NewTMDB(apiKey string) *TMDB { return NewTMDBFunc(func() string { return apiKey }) }

// NewTMDBFunc builds a TMDB provider that reads its key lazily, so a key added in the
// settings menu takes effect without a restart.
func NewTMDBFunc(key func() string) *TMDB {
	return &TMDB{
		key:       key,
		http:      &http.Client{Timeout: 20 * time.Second},
		base:      tmdbBaseURL,
		discCache: map[string]discoverCacheEntry{},
	}
}

// sanitizeErr rewrites err so its text cannot leak an API key embedded in the request
// URL. Transport failures (*url.Error and friends) reproduce the full URL — key
// included — and error strings from this package are written back to API clients and
// logs, so redaction has to happen at the point the error is created.
func sanitizeErr(rawURL string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if u, e := url.Parse(rawURL); e == nil {
		q := u.Query()
		for _, param := range []string{"api_key", "apikey", "key"} {
			if key := q.Get(param); key != "" {
				msg = strings.ReplaceAll(msg, key, "REDACTED")
				if esc := url.QueryEscape(key); esc != key {
					msg = strings.ReplaceAll(msg, esc, "REDACTED")
				}
			}
		}
	}
	return errors.New(msg)
}

// Available reports whether an API key is configured.
func (t *TMDB) Available() bool { return t.key() != "" }

func (t *TMDB) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	if !t.Available() {
		return nil, ErrNotConfigured
	}
	q.Set("api_key", t.key())
	full := t.base + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, sanitizeErr(full, err)
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, sanitizeErr(full, fmt.Errorf("tmdb request: %w", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("tmdb: invalid API key")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tmdb: HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// SearchMovie searches TMDB for movies by title.
func (t *TMDB) SearchMovie(ctx context.Context, query string) ([]MovieResult, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("include_adult", "false")
	body, err := t.get(ctx, "/search/movie", q)
	if err != nil {
		return nil, err
	}
	return parseTMDBSearch(body), nil
}

// GetMovie fetches full details for a TMDB movie id, including cast, genres,
// studio, collection, and US certification (one round-trip via append_to_response).
func (t *TMDB) GetMovie(ctx context.Context, tmdbID int) (*MovieDetails, error) {
	q := url.Values{}
	q.Set("append_to_response", "credits,release_dates")
	body, err := t.get(ctx, "/movie/"+strconv.Itoa(tmdbID), q)
	if err != nil {
		return nil, err
	}
	return parseTMDBMovie(body)
}

// SearchSeries searches TMDB for TV series by title.
func (t *TMDB) SearchSeries(ctx context.Context, query string) ([]SeriesResult, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("include_adult", "false")
	body, err := t.get(ctx, "/search/tv", q)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Results []tmdbSeries `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]SeriesResult, 0, len(payload.Results))
	for _, s := range payload.Results {
		out = append(out, s.toResult())
	}
	return out, nil
}

// GetSeries fetches full details for a TV series: metadata plus every season and
// its episodes. Seasons are fetched concurrently.
func (t *TMDB) GetSeries(ctx context.Context, tmdbID int) (*SeriesDetails, error) {
	q := url.Values{}
	q.Set("append_to_response", "credits,external_ids")
	body, err := t.get(ctx, "/tv/"+strconv.Itoa(tmdbID), q)
	if err != nil {
		return nil, err
	}
	var s tmdbSeries
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("tmdb: parse series: %w", err)
	}
	d := &SeriesDetails{
		SeriesResult: s.toResult(),
		IMDBID:       s.ExternalIDs.IMDBID,
		Status:       s.Status,
		OriginalName: s.OriginalName,
		OriginalLang: s.OriginalLanguage,
		TVDBID:       s.ExternalIDs.TVDBID,
	}
	if s.BackdropPath != "" {
		d.BackdropURL = tmdbBackdropBase + s.BackdropPath
	}
	if len(s.Networks) > 0 {
		d.Network = s.Networks[0].Name
	}
	for _, g := range s.Genres {
		d.Genres = append(d.Genres, g.Name)
	}
	for _, c := range s.Credits.Cast {
		if len(d.Cast) >= maxCast {
			break
		}
		cm := CastMember{Name: c.Name, Character: c.Character}
		if c.ProfilePath != "" {
			cm.ProfileURL = tmdbProfileBase + c.ProfilePath
		}
		d.Cast = append(d.Cast, cm)
	}

	// Fetch each season's episodes concurrently.
	d.Seasons = make([]SeasonDetails, len(s.Seasons))
	var wg sync.WaitGroup
	for i, sn := range s.Seasons {
		sd := SeasonDetails{SeasonNumber: sn.SeasonNumber, Name: sn.Name, Overview: sn.Overview, AirDate: sn.AirDate}
		if sn.PosterPath != "" {
			sd.PosterURL = tmdbImageBase + sn.PosterPath
		}
		d.Seasons[i] = sd
		wg.Add(1)
		go func(i, seasonNum int) {
			defer wg.Done()
			eps, err := t.seasonEpisodes(ctx, tmdbID, seasonNum)
			if err == nil {
				d.Seasons[i].Episodes = eps
			}
		}(i, sn.SeasonNumber)
	}
	wg.Wait()
	return d, nil
}

func (t *TMDB) seasonEpisodes(ctx context.Context, tmdbID, season int) ([]EpisodeDetails, error) {
	body, err := t.get(ctx, fmt.Sprintf("/tv/%d/season/%d", tmdbID, season), url.Values{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Episodes []struct {
			EpisodeNumber int    `json:"episode_number"`
			Name          string `json:"name"`
			Overview      string `json:"overview"`
			AirDate       string `json:"air_date"`
			Runtime       int    `json:"runtime"`
			StillPath     string `json:"still_path"`
		} `json:"episodes"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]EpisodeDetails, 0, len(payload.Episodes))
	for _, e := range payload.Episodes {
		ed := EpisodeDetails{EpisodeNumber: e.EpisodeNumber, Title: e.Name, Overview: e.Overview, AirDate: e.AirDate, Runtime: e.Runtime}
		if e.StillPath != "" {
			ed.StillURL = tmdbBackdropBase + e.StillPath
		}
		out = append(out, ed)
	}
	return out, nil
}

type tmdbSeries struct {
	ID               int         `json:"id"`
	Name             string      `json:"name"`
	OriginalName     string      `json:"original_name"`
	OriginalLanguage string      `json:"original_language"`
	FirstAirDate     string      `json:"first_air_date"`
	Overview         string      `json:"overview"`
	PosterPath       string      `json:"poster_path"`
	BackdropPath     string      `json:"backdrop_path"`
	VoteAverage      float64     `json:"vote_average"`
	Status           string      `json:"status"`
	Genres           []tmdbNamed `json:"genres"`
	Networks         []tmdbNamed `json:"networks"`
	Seasons          []struct {
		SeasonNumber int    `json:"season_number"`
		Name         string `json:"name"`
		Overview     string `json:"overview"`
		AirDate      string `json:"air_date"`
		PosterPath   string `json:"poster_path"`
	} `json:"seasons"`
	ExternalIDs struct {
		IMDBID string `json:"imdb_id"`
		TVDBID int    `json:"tvdb_id"`
	} `json:"external_ids"`
	CreatedBy []tmdbCast `json:"created_by"`
	Credits   struct {
		Cast []tmdbCast `json:"cast"`
		Crew []tmdbCrew `json:"crew"`
	} `json:"credits"`
}

func (s tmdbSeries) toResult() SeriesResult {
	poster := ""
	if s.PosterPath != "" {
		poster = tmdbImageBase + s.PosterPath
	}
	return SeriesResult{
		TMDBID: s.ID, Title: s.Name, Year: yearOf(s.FirstAirDate),
		Overview: s.Overview, PosterURL: poster, VoteAverage: s.VoteAverage,
	}
}

// GetCollection fetches a TMDB collection's member movies, sorted by release
// year (earliest first) so a franchise reads in order.
func (t *TMDB) GetCollection(ctx context.Context, collectionID int) (*Collection, error) {
	body, err := t.get(ctx, "/collection/"+strconv.Itoa(collectionID), url.Values{})
	if err != nil {
		return nil, err
	}
	return parseTMDBCollection(body)
}

func parseTMDBCollection(body []byte) (*Collection, error) {
	var payload struct {
		ID    int         `json:"id"`
		Name  string      `json:"name"`
		Parts []tmdbMovie `json:"parts"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("tmdb: parse collection: %w", err)
	}
	c := &Collection{ID: payload.ID, Name: payload.Name}
	for _, m := range payload.Parts {
		c.Members = append(c.Members, m.toResult())
	}
	sort.Slice(c.Members, func(i, j int) bool {
		if c.Members[i].Year != c.Members[j].Year {
			return c.Members[i].Year < c.Members[j].Year
		}
		return c.Members[i].Title < c.Members[j].Title
	})
	return c, nil
}

// --- parsing (split out for offline tests) ---

type tmdbMovie struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	ReleaseDate  string  `json:"release_date"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	VoteAverage  float64 `json:"vote_average"`
	IMDBID       string  `json:"imdb_id"`
	Runtime      int     `json:"runtime"`
	Status       string  `json:"status"`
	OriginalLang string  `json:"original_language"`

	Genres              []tmdbNamed `json:"genres"`
	ProductionCompanies []tmdbNamed `json:"production_companies"`
	BelongsToCollection *tmdbNamed  `json:"belongs_to_collection"`

	Credits struct {
		Cast []tmdbCast `json:"cast"`
		Crew []tmdbCrew `json:"crew"`
	} `json:"credits"`
	ReleaseDates struct {
		Results []tmdbReleaseDates `json:"results"`
	} `json:"release_dates"`
	ExternalIDs struct {
		IMDBID string `json:"imdb_id"`
	} `json:"external_ids"`
}

type tmdbCrew struct {
	Name        string `json:"name"`
	Job         string `json:"job"`
	ProfilePath string `json:"profile_path"`
}

type tmdbNamed struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type tmdbCast struct {
	Name        string `json:"name"`
	Character   string `json:"character"`
	ProfilePath string `json:"profile_path"`
	Order       int    `json:"order"`
}

type tmdbReleaseDates struct {
	Country      string `json:"iso_3166_1"`
	ReleaseDates []struct {
		Certification string `json:"certification"`
	} `json:"release_dates"`
}

func (m tmdbMovie) toResult() MovieResult {
	poster := ""
	if m.PosterPath != "" {
		poster = tmdbImageBase + m.PosterPath
	}
	return MovieResult{
		TMDBID:      m.ID,
		Title:       m.Title,
		Year:        yearOf(m.ReleaseDate),
		Overview:    m.Overview,
		PosterURL:   poster,
		VoteAverage: m.VoteAverage,
	}
}

func parseTMDBSearch(body []byte) []MovieResult {
	var payload struct {
		Results []tmdbMovie `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	out := make([]MovieResult, 0, len(payload.Results))
	for _, m := range payload.Results {
		out = append(out, m.toResult())
	}
	return out
}

func parseTMDBMovie(body []byte) (*MovieDetails, error) {
	var m tmdbMovie
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("tmdb: parse movie: %w", err)
	}
	d := &MovieDetails{
		MovieResult:      m.toResult(),
		IMDBID:           m.IMDBID,
		Runtime:          m.Runtime,
		Status:           m.Status,
		ReleaseDate:      m.ReleaseDate,
		OriginalLanguage: m.OriginalLang,
	}
	if m.BackdropPath != "" {
		d.BackdropURL = tmdbBackdropBase + m.BackdropPath
	}
	for _, g := range m.Genres {
		d.Genres = append(d.Genres, g.Name)
	}
	for _, c := range m.ProductionCompanies {
		d.Studios = append(d.Studios, c.Name)
	}
	if m.BelongsToCollection != nil {
		d.CollectionID = m.BelongsToCollection.ID
		d.CollectionName = m.BelongsToCollection.Name
	}
	d.Certification = usCertification(m.ReleaseDates.Results)
	for _, c := range m.Credits.Cast {
		if len(d.Cast) >= maxCast {
			break
		}
		cm := CastMember{Name: c.Name, Character: c.Character}
		if c.ProfilePath != "" {
			cm.ProfileURL = tmdbProfileBase + c.ProfilePath
		}
		d.Cast = append(d.Cast, cm)
	}
	return d, nil
}

// usCertification returns the US content rating (e.g. "R"), if present.
func usCertification(results []tmdbReleaseDates) string {
	for _, r := range results {
		if r.Country != "US" {
			continue
		}
		for _, rd := range r.ReleaseDates {
			if rd.Certification != "" {
				return rd.Certification
			}
		}
	}
	return ""
}

func yearOf(releaseDate string) int {
	if len(releaseDate) >= 4 {
		if y, err := strconv.Atoi(releaseDate[:4]); err == nil {
			return y
		}
	}
	return 0
}
