// Package movies is the Movies feature module (Radarr's domain): a monitored
// library of films with metadata, quality profiles, and — wired to the shared
// acquisition platform — automatic searching, grabbing, and import.
package movies

// Movie is a film in the library.
type Movie struct {
	ID              int64  `json:"id"`
	TMDBID          int    `json:"tmdb_id"`
	IMDBID          string `json:"imdb_id,omitempty"`
	Title           string `json:"title"`
	Year            int    `json:"year"`
	Overview        string `json:"overview,omitempty"`
	PosterURL       string `json:"poster_url,omitempty"`
	Runtime         int    `json:"runtime,omitempty"`
	Status          string `json:"status,omitempty"` // TMDB status (Released, Post Production, …)
	Monitored       bool   `json:"monitored"`
	QualityProfile  string `json:"quality_profile"`  // quality preset key
	MinAvailability string `json:"min_availability"` // announced | inCinemas | released
	HasFile         bool   `json:"has_file"`
	MovieFilePath   string `json:"movie_file_path,omitempty"`
	AddedAt         string `json:"added_at,omitempty"`
	// SourceRelease is the release name the default file was imported from — the
	// signal used to score the current file when deciding upgrades.
	SourceRelease string `json:"source_release,omitempty"`
	// UpgradeHold keeps the default file out of profile-driven upgrades ("keep existing
	// files" when its profile changed). Cleared by a new import, a profile change or Resume.
	UpgradeHold bool `json:"upgrade_hold,omitempty"`
	// ConvertedFromRelease and ConvertedFromSize are what the default file was before
	// Convert first shrank it (size in bytes, 0 = unknown). Upgrades must beat that, not
	// the smaller converted file. Empty for a file never converted; cleared on import.
	ConvertedFromRelease string `json:"converted_from_release,omitempty"`
	ConvertedFromSize    int64  `json:"converted_from_size,omitempty"`

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
	// UpgradesAllowed is computed on the detail endpoint only: whether the 6-hourly upgrade
	// sweep will look at this movie at all (monitored, has a file, profile upgrades on). The
	// page says what really happens instead of hedging "if your profile allows".
	UpgradesAllowed bool `json:"upgrades_allowed,omitempty"`
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
	// UpgradeHold keeps this track's file out of profile-driven upgrades (see Movie).
	UpgradeHold bool `json:"upgrade_hold,omitempty"`
	// The track's pre-conversion baseline (see Movie.ConvertedFromRelease).
	ConvertedFromRelease string `json:"converted_from_release,omitempty"`
	ConvertedFromSize    int64  `json:"converted_from_size,omitempty"`
}

// MovieFile describes the on-disk file for one track: size plus media info, read from the
// file (ffprobe) or parsed from its name. It's cached per track (movies.media_json,
// movie_versions.media_json) when the file is imported or changes; the detail page only
// stats the file and re-reads it in the background when its size or mtime moved.
type MovieFile struct {
	Path      string `json:"path"`
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes"`
	// MtimeUnix is the file's modification time when the entry was read (0 = an entry cached
	// before it was recorded). With the size it says whether the entry still describes the
	// file on disk, the same contract as Convert's probe cache.
	MtimeUnix   int64    `json:"mtime,omitempty"`
	Quality     string   `json:"quality,omitempty"` // "2160p BluRay"
	Codec       string   `json:"codec,omitempty"`
	Audio       []string `json:"audio,omitempty"`
	Atmos       bool     `json:"atmos,omitempty"` // Dolby Atmos in any audio track (probed, or named in the file)
	HDR         []string `json:"hdr,omitempty"`
	Group       string   `json:"group,omitempty"`
	Resolution  string   `json:"resolution,omitempty"`   // real resolution (ffprobe)
	DurationMin int      `json:"duration_min,omitempty"` // runtime from the file
	Probed      bool     `json:"probed,omitempty"`       // media info read from the file, not the name
	Subtitles   []string `json:"subtitles,omitempty"`    // subtitle filenames paired with this file
	// OrphanSubtitles are subtitles in the folder that pair with no video (an old name):
	// listed apart because Plex won't show them for this file.
	OrphanSubtitles []string `json:"orphan_subtitles,omitempty"`
	Missing         bool     `json:"missing"` // tracked in DB but gone from disk
	// MediaVersion is the probe's format version the entry was built with; older
	// entries are probed again in the background (see MediaVersion).
	MediaVersion int `json:"v,omitempty"`
}

// MediaVersion is bumped when the probe learns something new, so cached entries
// built before it get refreshed. 2: Atmos read from the audio stream's profile.
const MediaVersion = 2

// MediaStale reports whether a movie's cached media info predates the current probe.
func (m *Movie) MediaStale() bool {
	return m.HasFile && (m.File == nil || (!m.File.Missing && m.File.MediaVersion < MediaVersion))
}
