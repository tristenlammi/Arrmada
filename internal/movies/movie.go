// Package movies is the Movies feature module (Radarr's domain): a monitored
// library of films with metadata, quality profiles, and — wired to the shared
// acquisition platform — automatic searching, grabbing, and import.
package movies

// Movie is a film in the library.
type Movie struct {
	ID             int64  `json:"id"`
	TMDBID         int    `json:"tmdb_id"`
	IMDBID         string `json:"imdb_id,omitempty"`
	Title          string `json:"title"`
	Year           int    `json:"year"`
	Overview       string `json:"overview,omitempty"`
	PosterURL      string `json:"poster_url,omitempty"`
	Runtime        int    `json:"runtime,omitempty"`
	Status         string `json:"status,omitempty"` // TMDB status (Released, Post Production, …)
	Monitored       bool   `json:"monitored"`
	QualityProfile  string `json:"quality_profile"`  // quality preset key
	MinAvailability string `json:"min_availability"` // announced | inCinemas | released
	HasFile         bool   `json:"has_file"`
	MovieFilePath   string `json:"movie_file_path,omitempty"`
	AddedAt         string `json:"added_at,omitempty"`
	// SourceRelease is the release name the default file was imported from — the
	// signal used to score the current file when deciding upgrades.
	SourceRelease string `json:"source_release,omitempty"`

	// Extra holds enriched metadata (genres, cast, collection, …), stored as
	// JSON. Present on both list and detail responses.
	Extra *MovieExtra `json:"extra,omitempty"`
	// File is populated on the detail endpoint only (nil in list responses).
	File *MovieFile `json:"file,omitempty"`
	// Versions is populated on the detail endpoint only: the default track plus
	// any opt-in extra version tracks.
	Versions []Version `json:"versions,omitempty"`
	// Download reflects an in-progress download for this movie (attached by the
	// HTTP layer from the live queue; nil when nothing is downloading).
	Download *DownloadStatus `json:"download,omitempty"`
}

// DownloadStatus is a lightweight view of a movie's in-flight download.
type DownloadStatus struct {
	State    string  `json:"state"`
	Progress float64 `json:"progress"` // 0..1
}

// MovieExtra is the enriched TMDB metadata beyond the core fields, persisted as
// a JSON blob so new fields don't need a migration each time.
type MovieExtra struct {
	Genres           []string     `json:"genres,omitempty"`
	Studios          []string     `json:"studios,omitempty"`
	OriginalLanguage string       `json:"original_language,omitempty"`
	Certification    string       `json:"certification,omitempty"`
	BackdropURL      string       `json:"backdrop_url,omitempty"`
	ReleaseDate      string       `json:"release_date,omitempty"`
	CollectionID     int          `json:"collection_id,omitempty"`
	CollectionName   string       `json:"collection_name,omitempty"`
	VoteAverage      float64      `json:"vote_average,omitempty"`
	Cast             []CastMember `json:"cast,omitempty"`
}

// CastMember is one billed actor.
type CastMember struct {
	Name       string `json:"name"`
	Character  string `json:"character,omitempty"`
	ProfileURL string `json:"profile_url,omitempty"`
}

// Version is one acquisition track for a movie. The default version (ID 0) is
// the movie row itself; extra versions are opt-in tracks (e.g. a 4K track next
// to 1080p, or a Director's Cut) that each search, grade, and store their own
// file independently. Single-file movies simply have one (default) version.
type Version struct {
	ID             int64      `json:"id"` // 0 = default (the movie row)
	IsDefault      bool       `json:"is_default"`
	Label          string     `json:"label"`
	QualityProfile string     `json:"quality_profile"`
	Edition        string     `json:"edition,omitempty"`
	Monitored      bool       `json:"monitored"`
	HasFile        bool       `json:"has_file"`
	FilePath       string     `json:"file_path,omitempty"`
	SizeBytes      int64      `json:"size_bytes,omitempty"`
	SourceRelease  string     `json:"source_release,omitempty"`
	File           *MovieFile `json:"file,omitempty"` // enriched on the detail endpoint
}

// MovieFile describes the on-disk file for a movie: size plus media info parsed
// from its filename. Computed at read-time by stat-ing the path.
type MovieFile struct {
	Path      string   `json:"path"`
	Filename  string   `json:"filename"`
	SizeBytes int64    `json:"size_bytes"`
	Quality   string   `json:"quality,omitempty"` // "2160p BluRay"
	Codec      string   `json:"codec,omitempty"`
	Audio      []string `json:"audio,omitempty"`
	HDR        []string `json:"hdr,omitempty"`
	Group      string   `json:"group,omitempty"`
	Resolution string   `json:"resolution,omitempty"`   // real resolution (ffprobe)
	DurationMin int     `json:"duration_min,omitempty"` // runtime from the file
	Probed     bool     `json:"probed,omitempty"`       // media info read from the file, not the name
	Subtitles  []string `json:"subtitles,omitempty"`    // sidecar subtitle filenames
	Missing    bool     `json:"missing"`                // tracked in DB but gone from disk
}
