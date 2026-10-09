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
	List(ctx context.Context) ([]movies.Movie, error)
	// ByTMDBIDs is the library movies with these TMDB ids, in one query (a page's worth).
	ByTMDBIDs(ctx context.Context, tmdbIDs []int) ([]movies.Movie, error)
}

// seriesLib is the Series module, narrowed.
type seriesLib interface {
	AddWith(ctx context.Context, tmdbID int, qualityProfile string, opts series.AddOptions) (series.Series, error)
	Get(ctx context.Context, id int64) (series.Series, error)
	List(ctx context.Context) ([]series.Series, error)
	// ByTMDBIDs is the library shows with these TMDB ids, each with its Stats roll-up.
	ByTMDBIDs(ctx context.Context, tmdbIDs []int) ([]series.Series, error)
}

// bookLib is the Books module, narrowed.
type bookLib interface {
	Add(ctx context.Context, olKey, qualityProfile string, monitored bool, fallback metadata.BookResult) (books.Book, error)
	Get(ctx context.Context, id int64) (books.Book, error)
	List(ctx context.Context) ([]books.Book, error)
	// ByKeysOrIDs is the library books under these catalogue keys or with these ids.
	ByKeysOrIDs(ctx context.Context, olKeys []string, ids []int64) ([]books.Book, error)
}

// searcher is the acquisition pipeline, narrowed: the searches an approval starts and the
// movie search queue they go through.
type searcher interface {
	SearchMovie(ctx context.Context, id int64) (automation.SearchOutcome, error)
	SearchSeriesNow(ctx context.Context, seriesID int64) (automation.SearchOutcome, error)
	EnqueueMovieSearch(ctx context.Context, sub jobs.Submitter, id int64, spec jobs.Spec) (automation.MovieQueued, error)
}

// The concrete services are what main wires in.
var (
	_ movieLib  = (*movies.Service)(nil)
	_ seriesLib = (*series.Service)(nil)
	_ bookLib   = (*books.Service)(nil)
	_ searcher  = (*automation.Coordinator)(nil)
)
