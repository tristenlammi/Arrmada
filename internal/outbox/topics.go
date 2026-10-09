package outbox

// The topics producers enqueue and what each carries. Payloads hold ids and paths, never
// titles: handlers re-read current state, which is what keeps them idempotent, and a row
// can wait a while before it runs.
const (
	// TopicMovieImported: a movie file was imported (automatic, manual or from Review).
	TopicMovieImported = "movie.imported"
	// TopicMovieChanged: a movie's files changed some other way — renamed, deleted, the
	// whole movie removed — so indexes that list its files must catch up.
	TopicMovieChanged = "movie.changed"
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

// What a TopicMovieChanged row is about.
const (
	ChangeRenamed     = "renamed"
	ChangeFileDeleted = "file_deleted"
	ChangeDeleted     = "deleted"  // the movie itself is gone
	ChangeDetected    = "detected" // a rescan found a different file on disk
)

// MovieChanged is TopicMovieChanged's payload.
type MovieChanged struct {
	MovieID   int64  `json:"movie_id"`
	VersionID int64  `json:"version_id"`
	Change    string `json:"change"`
	OldPath   string `json:"old_path,omitempty"`
	Path      string `json:"path,omitempty"`
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
