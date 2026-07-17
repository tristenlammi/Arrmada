// Package series is the Series (TV) feature module (Sonarr's domain): a monitored
// library of shows, each with seasons and episodes. Episodes are the unit that
// gets monitored, searched, graded, and stored. It shares the acquisition
// platform (indexers, download clients, quality) with Movies.
package series

// Series is a TV show in the library.
type Series struct {
	ID             int64  `json:"id"`
	TMDBID         int    `json:"tmdb_id"`
	IMDBID         string `json:"imdb_id,omitempty"`
	Title          string `json:"title"`
	Year           int    `json:"year"`
	Overview       string `json:"overview,omitempty"`
	PosterURL      string `json:"poster_url,omitempty"`
	Status         string `json:"status,omitempty"` // Returning Series | Ended | Canceled
	Network        string `json:"network,omitempty"`
	Monitored      bool   `json:"monitored"`
	QualityProfile string `json:"quality_profile"`
	// SeriesType drives episode numbering. "standard" matches releases by SxxExx;
	// "anime" also matches by absolute episode number (and falls back positionally),
	// because anime releases number episodes 1..N across the whole run. Auto-set on
	// Add (TMDB Animation genre + Japanese original language) and user-overridable.
	SeriesType string `json:"series_type,omitempty"`
	AddedAt    string `json:"added_at,omitempty"`

	Extra   *SeriesExtra `json:"extra,omitempty"`
	Seasons []Season     `json:"seasons,omitempty"` // detail endpoint only
	Stats   *Stats       `json:"stats,omitempty"`   // aggregate counts for the grid
}

// Stats are the roll-up numbers shown per series in the library grid.
type Stats struct {
	Episodes  int   `json:"episodes"`   // aired episodes in monitored seasons
	HaveFiles int   `json:"have_files"` // episodes with a file on disk
	SizeBytes int64 `json:"size_bytes"`
	Seasons   int   `json:"seasons"`
}

// SeriesExtra is enriched TMDB metadata stored as a JSON blob.
type SeriesExtra struct {
	Genres      []string     `json:"genres,omitempty"`
	BackdropURL string       `json:"backdrop_url,omitempty"`
	Cast        []CastMember `json:"cast,omitempty"`
	// OriginalTitle is TMDB's original_name (e.g. romaji for anime). Used as an
	// alternate search title, since anime is often released under its romaji name.
	OriginalTitle string `json:"original_title,omitempty"`
}

// IsAnime reports whether the series uses anime (absolute) episode numbering.
func (s Series) IsAnime() bool { return s.SeriesType == SeriesTypeAnime }

// Series type values stored in series.series_type.
const (
	SeriesTypeStandard = "standard"
	SeriesTypeAnime    = "anime"
)

// CastMember is one billed actor.
type CastMember struct {
	Name       string `json:"name"`
	Character  string `json:"character,omitempty"`
	ProfileURL string `json:"profile_url,omitempty"`
}

// Season is one season of a series.
type Season struct {
	ID           int64     `json:"id"`
	SeasonNumber int       `json:"season_number"`
	Name         string    `json:"name,omitempty"`
	Overview     string    `json:"overview,omitempty"`
	PosterURL    string    `json:"poster_url,omitempty"`
	Monitored    bool      `json:"monitored"`
	Episodes     []Episode `json:"episodes,omitempty"`
}

// Episode is one episode — the monitored/searched/stored unit.
type Episode struct {
	ID            int64  `json:"id"`
	SeasonNumber  int    `json:"season_number"`
	EpisodeNumber int    `json:"episode_number"`
	Title         string `json:"title,omitempty"`
	Overview      string `json:"overview,omitempty"`
	AirDate       string `json:"air_date,omitempty"`
	Runtime       int    `json:"runtime,omitempty"`
	StillURL      string `json:"still_url,omitempty"`
	// AbsoluteNumber is the episode's 1..N position across the whole series
	// (specials excluded), used to match anime releases numbered absolutely. 0 when
	// unknown/not computed.
	AbsoluteNumber int  `json:"absolute_number,omitempty"`
	Monitored      bool `json:"monitored"`
	HasFile       bool   `json:"has_file"`
	FilePath      string `json:"file_path,omitempty"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
	// Download reflects an in-flight download for this episode (attached by the HTTP
	// layer from the live queue; nil when nothing is downloading).
	Download *EpisodeDownload `json:"download,omitempty"`
}

// EpisodeDownload is a lightweight view of an episode's in-flight download.
type EpisodeDownload struct {
	State    string  `json:"state"`
	Progress float64 `json:"progress"` // 0..1
}
