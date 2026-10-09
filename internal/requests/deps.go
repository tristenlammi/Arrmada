package requests

import (
	"context"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/series"
)

// What the Requests module needs from the libraries and the acquisition pipeline, as
// narrow interfaces. The concrete services satisfy them, so NewService keeps its
// signature; tests build a Service with fakes and can approve a movie without TMDB.

// movieLib is the Movies module, narrowed.
type movieLib interface {
	Add(ctx context.Context, tmdbID int, qualityProfile string, monitored bool) (movies.Movie, error)
	Get(ctx context.Context, id int64) (movies.Movie, error)
	// ByTMDBIDs is the library movies with these TMDB ids, in one query (a page's worth).
	ByTMDBIDs(ctx context.Context, tmdbIDs []int) ([]movies.Movie, error)
	// For approving a movie the library already has: find it, want it, say who asked.
	GetByTMDB(ctx context.Context, tmdbID int) (movies.Movie, error)
	SetMonitored(ctx context.Context, id int64, monitored bool) error
	AddEvent(ctx context.Context, id int64, event, detail string)
}

// seriesLib is the Series module, narrowed.
type seriesLib interface {
	AddWith(ctx context.Context, tmdbID int, qualityProfile string, opts series.AddOptions) (series.Series, error)
	Get(ctx context.Context, id int64) (series.Series, error)
	// ByTMDBIDs is the library shows with these TMDB ids, each with its Stats roll-up.
	ByTMDBIDs(ctx context.Context, tmdbIDs []int) ([]series.Series, error)
	GetByTMDB(ctx context.Context, tmdbID int) (series.Series, error)
	// SeasonProgress is a show's per-season counts, for season-scoped requests.
	SeasonProgress(ctx context.Context, seriesID int64) (map[int]series.SeasonProgress, error)
	// EnsureMonitored wants the seasons a request for a show already held asks for.
	EnsureMonitored(ctx context.Context, id int64, seasons []int, requestedBy string) error
}

// bookLib is the Books module, narrowed.
type bookLib interface {
	Add(ctx context.Context, olKey, qualityProfile string, monitored bool, fallback metadata.BookResult) (books.Book, error)
	Get(ctx context.Context, id int64) (books.Book, error)
	List(ctx context.Context) ([]books.Book, error)
	// ByKeysOrIDs is the library books under these catalogue keys (current or former) or
	// with these ids.
	ByKeysOrIDs(ctx context.Context, olKeys []string, ids []int64) ([]books.Book, error)
	// Catalogue keys, current and former (book_keys).
	BookIDForKey(ctx context.Context, key string) (int64, bool)
	KeysFor(ctx context.Context, bookID int64) ([]books.BookKey, error)
	KeyOwners(ctx context.Context, keys []string) (map[string]int64, error)
	KeyIndex(ctx context.Context, list []books.Book) map[string]books.Book
	AllKeys(ctx context.Context) (map[string]int64, error)
	AddKey(ctx context.Context, key string, bookID int64, source string) error
	// For approving a book the library already has: widen it, want it, say who asked.
	SetQualityProfile(ctx context.Context, id int64, profile string) error
	SetMonitored(ctx context.Context, id int64, monitored bool) error
	AddEvent(ctx context.Context, id int64, event, detail string)
}

// searcher is the acquisition pipeline, narrowed: the searches an approval starts and the
// movie search queue they go through.
type searcher interface {
	SearchMovie(ctx context.Context, id int64) (automation.SearchOutcome, error)
	SearchSeriesNow(ctx context.Context, seriesID int64) (automation.SearchOutcome, error)
	EnqueueMovieSearch(ctx context.Context, sub jobs.Submitter, id int64, spec jobs.Spec) (automation.MovieQueued, error)
	// ActiveByItem is the acquisition record's in-flight grabs of one media type, by item.
	ActiveByItem(ctx context.Context, mediaType string) (map[int64][]automation.Acquisition, error)
}

// The concrete services are what main wires in.
var (
	_ movieLib  = (*movies.Service)(nil)
	_ seriesLib = (*series.Service)(nil)
	_ bookLib   = (*books.Service)(nil)
	_ searcher  = (*automation.Coordinator)(nil)
)
