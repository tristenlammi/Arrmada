package download

// The download-client categories Arrmada files each kind of download under. Each importer
// reads only its own category, so a torrent in the wrong one is never imported — and the
// Downloads feed labels a torrent by its category. One definition here, so automation
// (which grabs) and httpapi (which labels) can't drift apart.
const (
	// CategoryMovies is the default movie category; ARRMADA_DOWNLOAD_CATEGORY can change it.
	CategoryMovies = "arrmada"
	CategorySeries = "arrmada-tv"
	CategoryBooks  = "arrmada-books"
	CategoryMusic  = "arrmada-music"
)
