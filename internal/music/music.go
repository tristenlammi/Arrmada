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
	CoverURL       string   `json:"cover_url,omitempty"` // an album cover standing in for a photo: see standInCover
	Genres         []string `json:"genres,omitempty"`
	Monitored      bool     `json:"monitored"`
	QualityProfile string   `json:"quality_profile"`
	AddedAt        string   `json:"added_at,omitempty"`
	Disambiguation string   `json:"disambiguation,omitempty"`
	Type           string   `json:"type,omitempty"` // Person, Group, Orchestra...
	Country        string   `json:"country,omitempty"`
	// InLibrary is false for an artist that is only credited (featured on another
	// artist's album, or removed from the library while still credited somewhere).
	InLibrary bool `json:"in_library"`

	// MonitorNew is the artist's own choice for albums MusicBrainz newly lists ("all",
	// "none"); "" follows the library default. ReleaseTypes is its own release-type choice;
	// nil follows the library default (MUS-14).
	MonitorNew   string        `json:"monitor_new"`
	ReleaseTypes *ReleaseTypes `json:"release_types,omitempty"`
	// LastRefreshedAt is when its albums were last read from MusicBrainz; StaleSince is
	// set while MusicBrainz isn't answering for it (everything kept).
	LastRefreshedAt string `json:"last_refreshed_at,omitempty"`
	StaleSince      string `json:"stale_since,omitempty"`
	// seenScopes are the browses whose release groups have been seen ("main", "other").
	seenScopes string
	// pendingAdd is the add dialog's choice, kept when the add couldn't read the albums,
	// for the first refresh that can to apply.
	pendingAdd string

	// Albums is filled by the detail read, not the list read: the albums filed under
	// this artist.
	Albums []Album `json:"albums,omitempty"`
	// AppearsOn is filled by the detail read: albums filed under another artist that
	// credit this one.
	AppearsOn []Album `json:"appears_on,omitempty"`
	// Stats summarize the artist's holdings (both reads).
	Stats *ArtistStats `json:"stats,omitempty"`
}

// ArtistStats is the at-a-glance summary of what an artist holds: its albums, and the
// tracks of each album's target edition.
type ArtistStats struct {
	Albums     int   `json:"albums"`
	Tracks     int   `json:"tracks"`
	HaveTracks int   `json:"have_tracks"`
	SizeBytes  int64 `json:"size_bytes"`
	// Album counts (MUS-10's, from the target editions). An unlisted album (no target
	// edition yet) is "unknown": never complete.
	AlbumsMonitored int `json:"albums_monitored"`
	AlbumsComplete  int `json:"albums_complete"`
	AlbumsUnlisted  int `json:"albums_unlisted"`
	AlbumsWithFiles int `json:"albums_with_files"`
	// AlbumsWanted are monitored albums that aren't complete (unlisted ones included).
	AlbumsWanted int `json:"albums_wanted"`
}

// statsFromAlbums sums an artist's albums into its stats: the same arithmetic as the
// grouped query behind the library list (Repo.artistStats), so the two always agree.
func statsFromAlbums(albums []Album) *ArtistStats {
	s := &ArtistStats{}
	for _, al := range albums {
		s.add(al.TrackCount, al.HaveTracks, al.EditionTracks, al.SizeBytes, al.Monitored && al.StaleSince == "")
	}
	return s
}

func (s *ArtistStats) add(tracks, have, editionTracks int, size int64, monitored bool) {
	s.Albums++
	s.Tracks += tracks
	s.HaveTracks += have
	s.SizeBytes += size
	complete := albumComplete(editionTracks, tracks, have)
	unlisted := editionTracks == 0
	if monitored {
		s.AlbumsMonitored++
		if !complete {
			s.AlbumsWanted++
		}
	}
	if complete {
		s.AlbumsComplete++
	}
	if unlisted {
		s.AlbumsUnlisted++
	}
	if have > 0 {
		s.AlbumsWithFiles++
	}
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
	// LastSearchAt/SearchMisses drive the sweep's backoff: how long ago it last looked
	// for this album, and how many looks in a row found nothing usable.
	LastSearchAt string `json:"last_search_at,omitempty"`
	SearchMisses int    `json:"search_misses"`

	// PrimaryType/SecondaryTypes are MusicBrainz's release-group types; AlbumType stays
	// the display label.
	PrimaryType    string `json:"primary_type,omitempty"`
	SecondaryTypes string `json:"secondary_types,omitempty"`
	// ArtistCredit is the album's credit as it reads ("Daft Punk feat. Pharrell
	// Williams"); CreditID is its row.
	ArtistCredit string `json:"artist_credit,omitempty"`
	CreditID     int64  `json:"-"`
	// TargetEditionID is the edition completeness and imports are measured against (0:
	// no listing yet); EditionPinned keeps automatic switching off it.
	TargetEditionID int64 `json:"target_edition_id,omitempty"`
	EditionPinned   bool  `json:"edition_pinned,omitempty"`
	// FoundAt is when a refresh found the album after its artist was added; FoundAs says
	// whether it was wanted and why not (see the Found* constants). StaleSince is set when
	// MusicBrainz no longer lists it: kept, files and all. Source is where it came from.
	FoundAt    string `json:"found_at,omitempty"`
	FoundAs    string `json:"found_as,omitempty"`
	StaleSince string `json:"stale_since,omitempty"`
	Source     string `json:"-"`
	// departureChecked is when the album, gone from its artist's list, was found still on
	// MusicBrainz (credited elsewhere): kept, and looked at again only in turn.
	departureChecked string

	// Tracks are the target edition's tracks with their files, filled by the detail read.
	Tracks []Track `json:"tracks,omitempty"`
	// TrackCount/HaveTracks let the UI show "7/12" without loading every track: the
	// target edition's wanted tracks, and how many of those have a file on disk.
	TrackCount int   `json:"track_count"`
	HaveTracks int   `json:"have_tracks"`
	SizeBytes  int64 `json:"size_bytes"`
	// EditionTracks is every track of the target edition, wanted or not; 0 means the
	// listing isn't known yet.
	EditionTracks int `json:"edition_tracks"`
	// HeldTier is the lowest tier among the tracks on disk ("" when none are, or one
	// can't be told), filled by the album detail read.
	HeldTier Quality `json:"held_tier,omitempty"`
	// MissingTracks counts track files the verify pass found gone from disk (flagged, never
	// searched for by themselves: the album is left out of the sweep until a person asks);
	// ChangedTracks those it found changed outside Arrmada (MUS-15).
	MissingTracks int `json:"missing_tracks,omitempty"`
	ChangedTracks int `json:"changed_tracks,omitempty"`

	// Filled by the detail read: the album's editions (summaries, the target first), the
	// target edition's discs (without their tracks), and the album's extra files (on no
	// track of the target edition: kept and listed, never deleted).
	Editions []Edition   `json:"editions,omitempty"`
	Discs    []Medium    `json:"discs,omitempty"`
	Extras   []TrackFile `json:"extras,omitempty"`
	// Notes are the upgrade's open notes on this album (detail read): tracks the old
	// table said had a file that the new model couldn't give one. While any is open the
	// sweep leaves the album alone (see Repo.WantedAlbums).
	Notes []MigrationNote `json:"notes,omitempty"`
}

// Complete reports whether every wanted track of the album's target edition has a file.
// An album with no listing is never complete: it is unknown. One whose listing is known but
// with no track wanted has nothing left to get, so it is complete: searching for it would
// grab a release nothing on it is wanted from.
func (a Album) Complete() bool { return albumComplete(a.EditionTracks, a.TrackCount, a.HaveTracks) }

func albumComplete(editionTracks, tracks, have int) bool {
	return editionTracks > 0 && have >= tracks
}

// Track is one track of an album's target edition (a release track) with its file folded
// in: the shape imports, the scan and the album page work with. ID is the release track's
// id; migrated tracks kept their old ids.
type Track struct {
	ID          int64  `json:"id"`
	AlbumID     int64  `json:"album_id"`
	MBID        string `json:"mbid,omitempty"` // the release track's MBID
	DiscNumber  int    `json:"disc_number"`
	TrackNumber int    `json:"track_number"`
	Number      string `json:"number,omitempty"` // as printed ("A1"), when it isn't just the position
	Title       string `json:"title"`
	DurationSec int    `json:"duration_sec,omitempty"`
	Monitored   bool   `json:"monitored"`
	// ArtistCredit is set only when the track's credit differs from the album's ("feat."
	// on one song).
	ArtistCredit  string `json:"artist_credit,omitempty"`
	RecordingID   int64  `json:"recording_id,omitempty"`
	RecordingMBID string `json:"recording_mbid,omitempty"`

	// The file, when there is one. HasFile is false for a file that has gone missing.
	HasFile       bool   `json:"has_file"`
	FileID        int64  `json:"file_id,omitempty"`
	FilePath      string `json:"file_path,omitempty"`
	Format        string `json:"format,omitempty"` // the measured tier, or an old scan's extension tag
	BitrateKbps   int    `json:"bitrate_kbps,omitempty"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
	SourceRelease string `json:"source_release,omitempty"`
	Codec         string `json:"codec,omitempty"`
	Lossless      bool   `json:"lossless,omitempty"`
	SampleRateHz  int    `json:"sample_rate_hz,omitempty"`
	BitDepth      int    `json:"bit_depth,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	TagState      string `json:"tag_state,omitempty"`
	Missing       bool   `json:"missing,omitempty"`
	// NotFoundSince is when the verify pass first couldn't find the file (MUS-15): not
	// missing yet, only once a later pass still can't. ChangedAt is when it found the file
	// changed outside Arrmada (measured again since).
	NotFoundSince string `json:"not_found_since,omitempty"`
	ChangedAt     string `json:"changed_at,omitempty"`
}

// Event is one entry in an artist's activity timeline.
type Event struct {
	Event     string `json:"event"`
	Detail    string `json:"detail,omitempty"`
	CreatedAt string `json:"created_at"`
}
