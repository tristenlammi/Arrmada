// Package metadata resolves media metadata (titles, years, artwork, ids) from
// external providers. It's a first-class subsystem — the lesson from Readarr and
// Lidarr is never to depend on a single fragile source — so providers sit behind
// one interface and can be added/swapped without touching modules. TMDB is the
// first movie/TV provider.
package metadata

import (
	"context"
	"errors"
	"strings"
	"unicode"
)

// ErrNotConfigured means the provider is missing required config (e.g. API key).
var ErrNotConfigured = errors.New("metadata provider not configured")

// NormalizeTitle reduces a title to lowercase alphanumerics so titles compare
// free of punctuation, spacing, and case ("Marvel's Agents of S.H.I.E.L.D." →
// "marvelsagentsofshield"). Used to match a scanned folder to a search result.
func NormalizeTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TitleYearMatch picks the search result that confidently matches the given
// folder title (and year, when known): an exact normalized title wins, confirmed
// by year when the folder supplied one; a year-only match is accepted only as a
// fallback when the folder gave a year to anchor on. It returns ok=false rather
// than guess the most popular hit — guessing is what mis-files "UNTAMED" as the
// more popular "The Untamed". titleOf/yearOf project the caller's result type.
func TitleYearMatch[T any](results []T, title string, year int, titleOf func(T) string, yearOf func(T) int) (T, bool) {
	var zero T
	want := NormalizeTitle(title)
	if want == "" {
		return zero, false
	}
	if year > 0 {
		for _, r := range results {
			if NormalizeTitle(titleOf(r)) == want && absYear(yearOf(r)-year) <= 1 {
				return r, true
			}
		}
	}
	for _, r := range results {
		if NormalizeTitle(titleOf(r)) == want {
			return r, true
		}
	}
	if year > 0 {
		for _, r := range results {
			if absYear(yearOf(r)-year) <= 1 {
				return r, true
			}
		}
	}
	return zero, false
}

func absYear(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// MovieResult is a lightweight search hit.
type MovieResult struct {
	TMDBID      int     `json:"tmdb_id"`
	Title       string  `json:"title"`
	Year        int     `json:"year"`
	Overview    string  `json:"overview"`
	PosterURL   string  `json:"poster_url"`
	VoteAverage float64 `json:"vote_average"`
}

// MovieDetails is a full movie record.
type MovieDetails struct {
	MovieResult
	IMDBID           string       `json:"imdb_id"`
	Runtime          int          `json:"runtime"`
	Status           string       `json:"status"`
	ReleaseDate      string       `json:"release_date,omitempty"`
	Genres           []string     `json:"genres,omitempty"`
	Studios          []string     `json:"studios,omitempty"`
	OriginalLanguage string       `json:"original_language,omitempty"`
	Certification    string       `json:"certification,omitempty"`
	BackdropURL      string       `json:"backdrop_url,omitempty"`
	CollectionID     int          `json:"collection_id,omitempty"`
	CollectionName   string       `json:"collection_name,omitempty"`
	Cast             []CastMember `json:"cast,omitempty"`
}

// CastMember is one billed actor.
type CastMember struct {
	Name       string `json:"name"`
	Character  string `json:"character,omitempty"`
	ProfileURL string `json:"profile_url,omitempty"`
}

// Collection is a movie franchise/collection and its member films.
type Collection struct {
	ID      int           `json:"id"`
	Name    string        `json:"name"`
	Members []MovieResult `json:"members"`
}

// MovieProvider looks up movie metadata.
type MovieProvider interface {
	// Available reports whether the provider is configured to answer queries.
	Available() bool
	SearchMovie(ctx context.Context, query string) ([]MovieResult, error)
	GetMovie(ctx context.Context, tmdbID int) (*MovieDetails, error)
	// GetCollection returns a collection's member movies (for "add whole collection").
	GetCollection(ctx context.Context, collectionID int) (*Collection, error)
}

// SeriesResult is a lightweight TV search hit.
type SeriesResult struct {
	TMDBID      int     `json:"tmdb_id"`
	Title       string  `json:"title"`
	Year        int     `json:"year"`
	Overview    string  `json:"overview"`
	PosterURL   string  `json:"poster_url"`
	VoteAverage float64 `json:"vote_average"`
}

// SeriesDetails is a full TV series record with its seasons and episodes.
type SeriesDetails struct {
	SeriesResult
	IMDBID      string          `json:"imdb_id,omitempty"`
	Status      string          `json:"status,omitempty"` // Returning Series | Ended | Canceled
	Network     string          `json:"network,omitempty"`
	BackdropURL string          `json:"backdrop_url,omitempty"`
	Genres      []string        `json:"genres,omitempty"`
	Cast        []CastMember    `json:"cast,omitempty"`
	Seasons     []SeasonDetails `json:"seasons,omitempty"`
}

// SeasonDetails is one season plus its episodes.
type SeasonDetails struct {
	SeasonNumber int              `json:"season_number"`
	Name         string           `json:"name,omitempty"`
	Overview     string           `json:"overview,omitempty"`
	PosterURL    string           `json:"poster_url,omitempty"`
	AirDate      string           `json:"air_date,omitempty"`
	Episodes     []EpisodeDetails `json:"episodes,omitempty"`
}

// EpisodeDetails is one episode.
type EpisodeDetails struct {
	EpisodeNumber int    `json:"episode_number"`
	Title         string `json:"title,omitempty"`
	Overview      string `json:"overview,omitempty"`
	AirDate       string `json:"air_date,omitempty"`
	Runtime       int    `json:"runtime,omitempty"`
	StillURL      string `json:"still_url,omitempty"`
}

// SeriesProvider looks up TV metadata.
type SeriesProvider interface {
	Available() bool
	SearchSeries(ctx context.Context, query string) ([]SeriesResult, error)
	GetSeries(ctx context.Context, tmdbID int) (*SeriesDetails, error)
}

// BookResult is a lightweight book search hit. Key is the provider's stable work id
// (Open Library work key, e.g. "OL45804W") — books use a string external id, not TMDB.
type BookResult struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	Year     int    `json:"year"`
	CoverURL string `json:"cover_url,omitempty"`
}

// BookDetails is a full book record.
type BookDetails struct {
	BookResult
	Description string   `json:"description,omitempty"`
	Subjects    []string `json:"subjects,omitempty"`
	Pages       int      `json:"pages,omitempty"`
}

// AuthorResult is a lightweight author search hit (Open Library author).
type AuthorResult struct {
	Key       string `json:"key"`  // Open Library author key, e.g. "OL23919A"
	Name      string `json:"name"`
	WorkCount int    `json:"work_count"`
	TopWork   string `json:"top_work,omitempty"`
	BirthDate string `json:"birth_date,omitempty"`
}

// BookProvider looks up book metadata (Open Library — no API key required).
type BookProvider interface {
	Available() bool
	SearchBooks(ctx context.Context, query string) ([]BookResult, error)
	GetBook(ctx context.Context, key string) (*BookDetails, error)
	// Covers returns candidate cover images for a book so the user can pick a nicer
	// one — every Open Library edition's cover plus Google Books hits.
	Covers(ctx context.Context, key, title, author string) ([]string, error)
	// SearchAuthors finds authors by name (for the Discover author search).
	SearchAuthors(ctx context.Context, query string) ([]AuthorResult, error)
	// AuthorWorks returns an author's catalogue (their works), newest/most-relevant first.
	AuthorWorks(ctx context.Context, authorKey string, limit int) ([]BookResult, error)
	// TrendingBooks returns books trending this week (for the Discover browse row).
	TrendingBooks(ctx context.Context) ([]BookResult, error)
	// BooksBySubject returns books tagged with a subject/genre.
	BooksBySubject(ctx context.Context, subject string, limit int) ([]BookResult, error)
}
