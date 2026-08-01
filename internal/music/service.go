package music

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// Service is the Music module's application logic.
type Service struct {
	repo *Repo
	meta metadata.MusicProvider
	log  *slog.Logger
}

// NewService wires the module.
func NewService(db *sql.DB, meta metadata.MusicProvider, log *slog.Logger) *Service {
	return &Service{repo: NewRepo(db), meta: meta, log: log}
}

// MetadataAvailable reports whether the metadata provider is usable (MusicBrainz always is).
func (s *Service) MetadataAvailable() bool { return s.meta.Available() }

// Lookup searches MusicBrainz for artists to add.
func (s *Service) Lookup(ctx context.Context, query string) ([]metadata.ArtistResult, error) {
	return s.meta.SearchArtists(ctx, query)
}

// ListArtists returns the library.
func (s *Service) ListArtists(ctx context.Context) ([]Artist, error) { return s.repo.ListArtists(ctx) }

// GetArtist returns one artist with its albums.
func (s *Service) GetArtist(ctx context.Context, id int64) (Artist, error) {
	a, err := s.repo.GetArtist(ctx, id)
	if err != nil {
		return Artist{}, err
	}
	albums, err := s.repo.AlbumsFor(ctx, id)
	if err != nil {
		return Artist{}, err
	}
	a.Albums = albums
	return a, nil
}

// GetAlbum returns one album with its track listing, fetching the listing from MusicBrainz
// the first time it's asked for (see EnsureTracks).
func (s *Service) GetAlbum(ctx context.Context, id int64) (Album, error) {
	al, err := s.repo.GetAlbum(ctx, id)
	if err != nil {
		return Album{}, err
	}
	if al.TrackCount == 0 {
		if err := s.EnsureTracks(ctx, al); err != nil {
			s.log.Warn("music: couldn't fetch the track listing", "album", al.Title, "err", err)
		}
		if refreshed, rerr := s.repo.GetAlbum(ctx, id); rerr == nil {
			al = refreshed
		}
	}
	tracks, err := s.repo.TracksFor(ctx, id)
	if err != nil {
		return Album{}, err
	}
	al.Tracks = tracks
	return al, nil
}

// AddArtist adds an artist and its album list.
//
// Track listings are NOT fetched here. MusicBrainz allows anonymous clients one request per
// second, and a listing costs two calls per album — adding a 25-album discography would
// block for the better part of a minute before the artist even appeared. Albums arrive
// immediately (two calls total) and each album's tracks are filled in the first time it's
// opened or searched for, via EnsureTracks.
func (s *Service) AddArtist(ctx context.Context, mbid, qualityProfile string, monitored bool) (Artist, error) {
	d, err := s.meta.GetArtist(ctx, mbid)
	if err != nil {
		return Artist{}, fmt.Errorf("fetch metadata: %w", err)
	}
	created, err := s.repo.CreateArtist(ctx, Artist{
		MBID: d.MBID, Name: d.Name, SortName: d.SortName, Overview: d.Overview,
		Genres: d.Genres, Monitored: monitored, QualityProfile: qualityProfile,
	})
	if err != nil {
		return Artist{}, err
	}
	s.log.Info("music: artist added", "name", created.Name, "mbid", created.MBID)
	s.repo.AddEvent(ctx, created.ID, "added", "Added to library")

	if n, aerr := s.syncAlbums(ctx, created, monitored); aerr != nil {
		// The artist is in the library either way — a failed album pull is something a
		// refresh can retry, not a reason to fail the add and lose the row.
		s.log.Warn("music: couldn't fetch the artist's albums", "artist", created.Name, "err", aerr)
	} else {
		s.repo.AddEvent(ctx, created.ID, "refreshed", fmt.Sprintf("Found %d album(s)", n))
	}
	return s.GetArtist(ctx, created.ID)
}

// syncAlbums pulls the artist's album list into the library, adding new release groups and
// correcting the metadata on ones already stored.
func (s *Service) syncAlbums(ctx context.Context, a Artist, monitored bool) (int, error) {
	albums, err := s.meta.ArtistAlbums(ctx, a.MBID)
	if err != nil {
		return 0, err
	}
	for _, al := range albums {
		if _, err := s.repo.UpsertAlbum(ctx, Album{
			ArtistID: a.ID, MBID: al.MBID, Title: al.Title, Year: al.Year,
			AlbumType: al.Type, CoverURL: al.CoverURL, ReleaseDate: al.ReleaseDate,
			Monitored: monitored,
		}); err != nil {
			s.log.Warn("music: storing an album failed", "album", al.Title, "err", err)
		}
	}
	return len(albums), nil
}

// EnsureTracks fills in an album's track listing when it has none yet. Safe to call
// repeatedly — an album that already has tracks costs nothing.
func (s *Service) EnsureTracks(ctx context.Context, al Album) error {
	if al.TrackCount > 0 {
		return nil
	}
	tracks, err := s.meta.AlbumTracks(ctx, al.MBID)
	if err != nil {
		return err
	}
	if len(tracks) == 0 {
		return nil // no official release carries a listing yet (a future album, say)
	}
	rows := make([]Track, 0, len(tracks))
	for _, t := range tracks {
		rows = append(rows, Track{
			MBID: t.MBID, DiscNumber: t.Disc, TrackNumber: t.Number,
			Title: t.Title, DurationSec: t.DurationSec,
		})
	}
	return s.repo.UpsertTracks(ctx, al.ID, rows)
}

// Refresh re-pulls an artist's metadata and album list.
func (s *Service) Refresh(ctx context.Context, id int64) (Artist, error) {
	a, err := s.repo.GetArtist(ctx, id)
	if err != nil {
		return Artist{}, err
	}
	if d, derr := s.meta.GetArtist(ctx, a.MBID); derr == nil {
		if err := s.repo.UpdateArtistMeta(ctx, id, d.Overview, a.ImageURL, d.Genres); err != nil {
			s.log.Warn("music: updating artist metadata failed", "artist", a.Name, "err", err)
		}
	} else {
		s.log.Warn("music: artist metadata refresh failed", "artist", a.Name, "err", derr)
	}
	if n, aerr := s.syncAlbums(ctx, a, a.Monitored); aerr != nil {
		s.log.Warn("music: album refresh failed", "artist", a.Name, "err", aerr)
	} else {
		s.repo.AddEvent(ctx, id, "refreshed", fmt.Sprintf("Refreshed — %d album(s) listed", n))
	}
	return s.GetArtist(ctx, id)
}

// Albums returns an artist's albums.
func (s *Service) Albums(ctx context.Context, artistID int64) ([]Album, error) {
	return s.repo.AlbumsFor(ctx, artistID)
}

// Tracks returns an album's tracks.
func (s *Service) Tracks(ctx context.Context, albumID int64) ([]Track, error) {
	return s.repo.TracksFor(ctx, albumID)
}

// SetArtistMonitored toggles an artist (cascading to its albums and tracks).
func (s *Service) SetArtistMonitored(ctx context.Context, id int64, monitored bool) error {
	return s.repo.SetArtistMonitored(ctx, id, monitored)
}

// SetAlbumMonitored toggles one album (and its tracks).
func (s *Service) SetAlbumMonitored(ctx context.Context, id int64, monitored bool) error {
	return s.repo.SetAlbumMonitored(ctx, id, monitored)
}

// SetQualityProfile changes an artist's quality profile.
func (s *Service) SetQualityProfile(ctx context.Context, id int64, profile string) error {
	return s.repo.SetArtistQualityProfile(ctx, id, profile)
}

// MarkTrackImported records a track's file on disk.
func (s *Service) MarkTrackImported(ctx context.Context, trackID int64, path, format string, bitrate int, size int64, sourceRelease string) error {
	return s.repo.SetTrackFile(ctx, trackID, path, format, bitrate, size, sourceRelease)
}

// ClearTrackFile flips a track back to wanted.
func (s *Service) ClearTrackFile(ctx context.Context, trackID int64) error {
	return s.repo.ClearTrackFile(ctx, trackID)
}

// Delete removes an artist; its albums and tracks cascade away.
func (s *Service) Delete(ctx context.Context, id int64) error { return s.repo.DeleteArtist(ctx, id) }

// AddEvent appends an artist timeline event.
func (s *Service) AddEvent(ctx context.Context, id int64, event, detail string) {
	s.repo.AddEvent(ctx, id, event, detail)
}

// Events returns an artist's activity timeline.
func (s *Service) Events(ctx context.Context, id int64, limit int) ([]Event, error) {
	return s.repo.Events(ctx, id, limit)
}
