package outbox

// The topics producers enqueue and what each carries. Payloads hold ids and paths, never
// titles: handlers re-read current state, which is what keeps them idempotent, and a row
// can wait a while before it runs.
const (
	// TopicMovieImported: a movie file was imported (automatic, manual or from Review).
	TopicMovieImported = "movie.imported"
	// TopicSeriesImported: episodes of a show were imported.
	TopicSeriesImported = "series.imported"
	// TopicBookImported: a book edition or audiobook version was imported.
	TopicBookImported = "book.imported"
)

// MovieImported is TopicMovieImported's payload.
type MovieImported struct {
	MovieID   int64  `json:"movie_id"`
	VersionID int64  `json:"version_id"` // 0 = the default track
	Path      string `json:"path"`
	Upgrade   bool   `json:"upgrade"` // it replaced an older file
}

// Episode is one (season, episode) an import placed.
type Episode struct {
	Season  int `json:"season"`
	Episode int `json:"episode"`
}

// SeriesImported is TopicSeriesImported's payload.
type SeriesImported struct {
	SeriesID int64     `json:"series_id"`
	Episodes []Episode `json:"episodes,omitempty"`
}

// BookImported is TopicBookImported's payload.
type BookImported struct {
	BookID  int64  `json:"book_id"`
	Edition string `json:"edition"`
}
