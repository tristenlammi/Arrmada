package download

// Arrmada owns the download-client categories. Each importer only looks at its own
// category, so a torrent filed anywhere else downloads and is never imported — which is
// why there is no per-client category setting: a free-text one silently stranded every
// movie on installs that typed something other than the movie category into it.
const (
	// DefaultMovieCategory is the movie category when ARRMADA_DOWNLOAD_CATEGORY isn't set.
	// The movie one is the only configurable category (the env var predates the others).
	DefaultMovieCategory = "arrmada"
	// CategoryTV keeps TV downloads apart so the multi-file series importer takes them,
	// not the single-file movie importer.
	CategoryTV = "arrmada-tv"
	// CategoryBooks routes ebook and audiobook downloads through the book importer.
	CategoryBooks = "arrmada-books"
	// CategoryMusic routes album downloads through the music importer.
	CategoryMusic = "arrmada-music"
)

// Categories is the fixed set Arrmada files downloads under, for showing to the owner.
type Categories struct {
	Movies string `json:"movies"`
	TV     string `json:"tv"`
	Books  string `json:"books"`
	Music  string `json:"music"`
}

// FixedCategories is Arrmada's categories with the configured movie one ("" means the
// default).
func FixedCategories(movie string) Categories {
	if movie == "" {
		movie = DefaultMovieCategory
	}
	return Categories{Movies: movie, TV: CategoryTV, Books: CategoryBooks, Music: CategoryMusic}
}
