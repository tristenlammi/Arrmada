// Package music implements the Music module: MusicBrainz metadata, an artist/album/track
// library, and acquisition through the shared indexer/download/import platform.
//
// The acquisition unit is the ALBUM. Releases ship as an album, and "quality" for music is a
// FORMAT preference (FLAC over MP3 320 over MP3 V0…) scored through the shared quality
// profile's format_scores — the same shape the Books module uses, not the resolution ladder
// movies and series use.
package music

import "errors"

// ErrNotFound is returned when an id doesn't exist.
var ErrNotFound = errors.New("not found")

// ErrExists is returned when a MusicBrainz entity is already in the library.
var ErrExists = errors.New("already in library")

// Artist is one artist in the library.
type Artist struct {
	ID             int64    `json:"id"`
	MBID           string   `json:"mbid"`
	Name           string   `json:"name"`
	SortName       string   `json:"sort_name,omitempty"`
	Overview       string   `json:"overview,omitempty"`
	ImageURL       string   `json:"image_url,omitempty"`
	Genres         []string `json:"genres,omitempty"`
	Monitored      bool     `json:"monitored"`
	QualityProfile string   `json:"quality_profile"`
	AddedAt        string   `json:"added_at,omitempty"`

	// Albums is filled by the detail read, not the list read.
	Albums []Album `json:"albums,omitempty"`
	// Stats summarize the artist's holdings for the list view.
	Stats *ArtistStats `json:"stats,omitempty"`
}

// ArtistStats is the at-a-glance summary of what an artist holds.
type ArtistStats struct {
	Albums     int   `json:"albums"`
	Tracks     int   `json:"tracks"`
	HaveTracks int   `json:"have_tracks"`
	SizeBytes  int64 `json:"size_bytes"`
}

// Album is one release group (what a user calls "an album").
type Album struct {
	ID          int64  `json:"id"`
	ArtistID    int64  `json:"artist_id"`
	MBID        string `json:"mbid"`
	Title       string `json:"title"`
	Year        int    `json:"year,omitempty"`
	AlbumType   string `json:"album_type,omitempty"`
	CoverURL    string `json:"cover_url,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
	Monitored   bool   `json:"monitored"`
	AddedAt     string `json:"added_at,omitempty"`

	Tracks []Track `json:"tracks,omitempty"`
	// TrackCount/HaveTracks let the UI show "7/12" without loading every track.
	TrackCount int   `json:"track_count"`
	HaveTracks int   `json:"have_tracks"`
	SizeBytes  int64 `json:"size_bytes"`
}

// Complete reports whether every monitored track on the album has a file.
func (a Album) Complete() bool { return a.TrackCount > 0 && a.HaveTracks >= a.TrackCount }

// Track is one recording on an album.
type Track struct {
	ID            int64  `json:"id"`
	AlbumID       int64  `json:"album_id"`
	MBID          string `json:"mbid,omitempty"`
	DiscNumber    int    `json:"disc_number"`
	TrackNumber   int    `json:"track_number"`
	Title         string `json:"title"`
	DurationSec   int    `json:"duration_sec,omitempty"`
	Monitored     bool   `json:"monitored"`
	HasFile       bool   `json:"has_file"`
	FilePath      string `json:"file_path,omitempty"`
	Format        string `json:"format,omitempty"`
	BitrateKbps   int    `json:"bitrate_kbps,omitempty"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
	SourceRelease string `json:"source_release,omitempty"`
}

// Event is one entry in an artist's activity timeline.
type Event struct {
	Event     string `json:"event"`
	Detail    string `json:"detail,omitempty"`
	CreatedAt string `json:"created_at"`
}
