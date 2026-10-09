// Package series is the Series (TV) feature module (Sonarr's domain): a monitored
// library of shows, each with seasons and episodes. Episodes are the unit that
// gets monitored, searched, graded, and stored. It shares the acquisition
// platform (indexers, download clients, quality) with Movies.
package series

// Series is a TV show in the library.
type Series struct {
	ID        int64  `json:"id"`
	TMDBID    int    `json:"tmdb_id"`
	TVDBID    int    `json:"tvdb_id,omitempty"`
	IMDBID    string `json:"imdb_id,omitempty"`
	Title     string `json:"title"`
	Year      int    `json:"year"`
	Overview  string `json:"overview,omitempty"`
	PosterURL string `json:"poster_url,omitempty"`
	Status    string `json:"status,omitempty"` // Returning Series | Ended | Canceled
	Network   string `json:"network,omitempty"`
	// Monitored is the series gate: off pauses the show without touching its season and
	// episode choices.
	Monitored bool `json:"monitored"`
	// MonitorNewSeasons: a season new to the show is monitored when a refresh adds it.
	MonitorNewSeasons bool   `json:"monitor_new_seasons"`
	QualityProfile    string `json:"quality_profile"`
	// SeriesType drives episode numbering. "standard" matches releases by SxxExx;
	// "anime" also matches by absolute episode number (and falls back positionally),
	// because anime releases number episodes 1..N across the whole run. Auto-set on
	// Add (TMDB Animation genre + Japanese original language) and user-overridable.
	SeriesType string `json:"series_type,omitempty"`
	AddedAt    string `json:"added_at,omitempty"`
	// NumberingSource is whose listing the stored episode numbering follows: "tvdb",
	// "tvmaze" or "tmdb", or "" when unknown (shows added before it was recorded). A
	// refresh compares it with the fresh listing's source to decide whether a numbering
	// difference is a real renumber or a stand-in listing that must not move files.
	NumberingSource string `json:"numbering_source"`
	// LastRefreshedAt is when metadata was last pulled successfully (SQLite datetime, UTC),
	// or "" when never. The weekly re-check of ended shows reads it.
	LastRefreshedAt string `json:"last_refreshed_at,omitempty"`

	Extra *SeriesExtra `json:"extra,omitempty"`
	// Aliases are the other titles this show is released under. Populated on read so
	// release matching can consult them without a lookup per candidate; empty for the
	// overwhelming majority of series, which is what keeps the feature inert.
	Aliases []Alias  `json:"aliases,omitempty"`
	Seasons []Season `json:"seasons,omitempty"` // detail endpoint only
	Stats   *Stats   `json:"stats,omitempty"`   // aggregate counts for the grid
}

// Stats are a series' roll-up numbers, for the library grid and the detail page. Specials
// are left out throughout.
type Stats struct {
	// Episodes is what the progress counts against: episodes with a file, plus aired
	// episodes that are monitored in a monitored season.
	Episodes  int   `json:"episodes"`
	HaveFiles int   `json:"have_files"` // episodes with a file on disk
	SizeBytes int64 `json:"size_bytes"`
	Seasons   int   `json:"seasons"`
	// Missing: aired, monitored (episode and season), no file — what a search is for.
	Missing int `json:"missing"`
	// UnmonitoredMissing: aired, no file, and not monitored — shown as "+N not monitored".
	UnmonitoredMissing int `json:"unmonitored_missing"`
	// NextAirDate is the soonest monitored episode still to air ("" when none).
	NextAirDate string `json:"next_air_date,omitempty"`
}

// SeriesExtra is enriched TMDB metadata stored as a JSON blob.
type SeriesExtra struct {
	Genres      []string     `json:"genres,omitempty"`
	BackdropURL string       `json:"backdrop_url,omitempty"`
	Cast        []CastMember `json:"cast,omitempty"`
	// OriginalTitle is TMDB's original_name: the title in the show's own language and
	// script. For a Japanese show that is kana or kanji (葬送のフリーレン), not the romaji
	// releases use, so it only counts as a release title when it is Latin script
	// (parser.IsLatin). The romaji name arrives as an automatic alias from TMDB's
	// alternative titles.
	OriginalTitle string `json:"original_title,omitempty"`
	// OriginalLanguage is TMDB's original_language ("ja", "en"…), so Convert can keep a
	// show's original-language audio when it trims audio tracks to your languages.
	OriginalLanguage string `json:"original_language,omitempty"`
	// OriginCountry is TMDB's origin_country ("US", "GB"…). A release that tags a country
	// ("The.Office.US") only matches a show from that country. Empty for shows not
	// refreshed since it was stored, which keeps their old matching until they are.
	OriginCountry []string `json:"origin_country,omitempty"`
}

// IsAnime reports whether the series uses anime (absolute) episode numbering.
func (s Series) IsAnime() bool { return s.SeriesType == SeriesTypeAnime }

// AbsoluteFor returns the absolute episode number for a (season, episode), or 0 when
// unknown. Requires the series' Seasons to be loaded (detail view).
func (s Series) AbsoluteFor(season, episode int) int {
	for _, sn := range s.Seasons {
		if sn.SeasonNumber != season {
			continue
		}
		for _, e := range sn.Episodes {
			if e.EpisodeNumber == episode {
				return e.AbsoluteNumber
			}
		}
	}
	return 0
}

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
	AbsoluteNumber int    `json:"absolute_number,omitempty"`
	Monitored      bool   `json:"monitored"`
	HasFile        bool   `json:"has_file"`
	FilePath       string `json:"file_path,omitempty"`
	SizeBytes      int64  `json:"size_bytes,omitempty"`
	// SourceRelease is the release name the file was imported from (e.g.
	// "Show.S01E01.1080p.BluRay.x264-GRP"). The library filename is renamed and strips
	// group/HDR/audio tags, so only this is a trustworthy baseline for upgrade scoring.
	// Empty for files imported before it was recorded — those are skipped by upgrades.
	SourceRelease string `json:"source_release,omitempty"`
	// UpgradeHold keeps the episode's file out of profile-driven upgrades ("keep existing
	// files" when its profile changed). Cleared by a new import, a profile change or Resume.
	UpgradeHold bool `json:"upgrade_hold,omitempty"`
	// ConvertedFromRelease and ConvertedFromSize are what the file was before Convert first
	// shrank it (size in bytes, 0 = unknown). Upgrades must beat that, not the converted
	// file. Empty for a file never converted; cleared when a new file is imported.
	ConvertedFromRelease string `json:"converted_from_release,omitempty"`
	ConvertedFromSize    int64  `json:"converted_from_size,omitempty"`
	// Download reflects an in-flight download for this episode (attached by the HTTP
	// layer from the live queue; nil when nothing is downloading).
	Download *EpisodeDownload `json:"download,omitempty"`
}

// EpisodeDownload is a lightweight view of an episode's in-flight download.
type EpisodeDownload struct {
	State    string  `json:"state"`
	Progress float64 `json:"progress"` // 0..1
}
