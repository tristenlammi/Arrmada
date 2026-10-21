package music

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Service is the Music module's application logic.
type Service struct {
	repo *Repo
	meta metadata.MusicProvider
	log  *slog.Logger
	// lookup reads a saved setting (the matcher's thresholds, the edition policy); nil
	// reads every one as its default.
	lookup func(key string) (string, bool)
	// now is the clock (tests move it); nil is the real one.
	now func() time.Time
	// names caches every artist's names and aliases (MUS-38, NameIndex).
	names nameCache
}

// NewService wires the module.
func NewService(db store.Pool, meta metadata.MusicProvider, log *slog.Logger) *Service {
	return &Service{repo: NewRepo(db), meta: meta, log: log}
}

// MetadataAvailable reports whether the metadata provider is usable (MusicBrainz always is).
func (s *Service) MetadataAvailable() bool { return s.meta.Available() }

// Lookup searches MusicBrainz for artists to add. MusicBrainz searches aliases too, so a
// romanised name finds a Japanese artist; each result says which alias matched when its
// own name doesn't show what was typed (AKA).
func (s *Service) Lookup(ctx context.Context, query string) ([]metadata.ArtistResult, error) {
	res, err := s.meta.SearchArtists(ctx, query)
	for i := range res {
		res[i].AKA = AliasMatching(res[i], query)
	}
	return res, err
}

// ListArtists returns the library.
func (s *Service) ListArtists(ctx context.Context) ([]Artist, error) { return s.repo.ListArtists(ctx) }

// HasArtists reports whether anyone has added an artist, which is what decides whether an
// existing install keeps the Music module on now that it's off by default.
func (s *Service) HasArtists(ctx context.Context) (bool, error) { return s.repo.HasArtists(ctx) }

// GetArtist returns one artist with its albums, the albums it appears on, and its stats
// (summed from the albums, so they equal the library list's).
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
	a.Stats = statsFromAlbums(albums)
	if a.AppearsOn, err = s.repo.AppearsOn(ctx, id); err != nil {
		return Artist{}, err
	}
	return a, nil
}

// AlbumRecord is one album as stored, with its track counts: no MusicBrainz call and no
// track list, for a caller that only needs to know it exists.
func (s *Service) AlbumRecord(ctx context.Context, id int64) (Album, error) {
	return s.repo.GetAlbum(ctx, id)
}

// GetAlbum returns one album in full: its target edition's tracks with their files, its
// editions, the target's discs and its extra files. The listing is fetched from
// MusicBrainz the first time it's asked for (see EnsureTracks). A fixed number of queries,
// however big the album.
func (s *Service) GetAlbum(ctx context.Context, id int64) (Album, error) {
	al, err := s.repo.GetAlbum(ctx, id)
	if err != nil {
		return Album{}, err
	}
	// Opening an album page costs no MusicBrainz call once it has a listing: its editions
	// are listed (and a migrated album re-keyed) when it's searched for, imported into, or
	// its edition picker is opened.
	if al.EditionTracks == 0 {
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
	al.HeldTier, _ = lowestHeld(tracks)
	if al.Editions, err = s.repo.Editions(ctx, id); err != nil {
		return Album{}, err
	}
	if al.TargetEditionID > 0 {
		if al.Discs, err = s.repo.Discs(ctx, al.TargetEditionID); err != nil {
			return Album{}, err
		}
	}
	files, err := s.repo.AlbumFiles(ctx, id)
	if err != nil {
		return Album{}, err
	}
	onTarget := make(map[int64]bool, len(tracks))
	for _, t := range tracks {
		if t.FileID > 0 {
			onTarget[t.FileID] = true
		}
	}
	for _, f := range files {
		if !onTarget[f.ID] {
			al.Extras = append(al.Extras, f)
		}
	}
	if al.Notes, err = s.repo.OpenNotes(ctx, id); err != nil {
		return Album{}, err
	}
	return al, nil
}

// ResolveNote marks an album's migration note as looked at, so the sweep searches the
// album again.
func (s *Service) ResolveNote(ctx context.Context, albumID, noteID int64) error {
	return s.repo.ResolveNote(ctx, albumID, noteID)
}

// LibraryArtists lists the library's artists without their stats (see Repo.LibraryArtists).
func (s *Service) LibraryArtists(ctx context.Context) ([]Artist, error) {
	return s.repo.LibraryArtists(ctx)
}

// FileOwner is the track a file path is recorded for (0: none, or an album extra).
func (s *Service) FileOwner(ctx context.Context, path string) (int64, error) {
	return s.repo.FileOwner(ctx, path)
}

// AddOptions are the add dialog's choices.
type AddOptions struct {
	// Monitored is whether the artist itself is monitored (searched at all). The scan adds
	// the artists it recognises unmonitored.
	Monitored bool
	// Monitor is which of today's albums are wanted: all (the default), future, latest
	// or none.
	Monitor string
	// MonitorNew and ReleaseTypes are the artist's own choices; "" and nil follow the
	// library default. Future and latest want new releases, none doesn't, unless given.
	MonitorNew   string
	ReleaseTypes *ReleaseTypes
}

// AddArtist adds an artist and its album list: every listed album wanted when monitored,
// none otherwise (AddArtistWith has the add dialog's other choices).
func (s *Service) AddArtist(ctx context.Context, mbid, qualityProfile string, monitored bool) (Artist, error) {
	opt := AddOptions{Monitored: monitored, Monitor: MonitorAll}
	if !monitored {
		opt.Monitor = MonitorNone
	}
	return s.AddArtistWith(ctx, mbid, qualityProfile, opt)
}

// AddArtistWith adds an artist and its album list with the add dialog's choices.
//
// Track listings are NOT fetched here. MusicBrainz allows anonymous clients one request per
// second, and a listing costs two calls per album — adding a 25-album discography would
// block for the better part of a minute before the artist even appeared. Albums arrive
// immediately (two calls total) and each album's tracks are filled in the first time it's
// opened or searched for, via EnsureTracks.
func (s *Service) AddArtistWith(ctx context.Context, mbid, qualityProfile string, opt AddOptions) (Artist, error) {
	if opt.Monitor == "" {
		opt.Monitor = MonitorAll
	}
	if !ValidMonitor(opt.Monitor) {
		return Artist{}, fmt.Errorf("unknown monitor choice %q", opt.Monitor)
	}
	if !ValidMonitorNew(opt.MonitorNew) {
		return Artist{}, fmt.Errorf("unknown new-release choice %q", opt.MonitorNew)
	}
	types := ""
	if opt.ReleaseTypes != nil {
		t, err := opt.ReleaseTypes.Normalize()
		if err != nil {
			return Artist{}, err
		}
		types = t.String()
	}
	if opt.MonitorNew == "" && opt.Monitored {
		switch opt.Monitor {
		case MonitorFuture, MonitorLatest:
			opt.MonitorNew = MonitorNewAll
		case MonitorNone:
			opt.MonitorNew = MonitorNewNone
		}
	}
	d, err := s.meta.GetArtist(ctx, mbid)
	if err != nil {
		return Artist{}, fmt.Errorf("fetch metadata: %w", err)
	}
	created, err := s.repo.CreateArtist(ctx, Artist{
		MBID: d.MBID, Name: d.Name, SortName: d.SortName, Overview: d.Overview,
		Genres: d.Genres, Monitored: opt.Monitored, QualityProfile: qualityProfile,
		Disambiguation: d.Disambiguation, Type: d.Type, Country: d.Country,
	})
	if err != nil {
		return Artist{}, err
	}
	if opt.MonitorNew != "" || types != "" {
		if err := s.repo.SetArtistOptions(ctx, created.ID, opt.MonitorNew, types); err != nil {
			return Artist{}, err
		}
		if created, err = s.repo.GetArtist(ctx, created.ID); err != nil {
			return Artist{}, err
		}
	}
	s.log.Info("music: artist added", "name", created.Name, "mbid", created.MBID)
	s.repo.AddEvent(ctx, created.ID, "added", "Added to library")
	s.forgetNames()
	s.storeAliases(ctx, created.ID, d.Aliases)

	res, aerr := s.syncAlbums(ctx, created, syncAdd, opt.Monitor, false)
	if aerr != nil || res.Unanswered {
		// The artist is in the library either way — a failed album pull is something a
		// refresh can retry, not a reason to fail the add and lose the row. The add's
		// choice is kept for that refresh, which applies it as the add would have.
		s.log.Warn("music: couldn't fetch the artist's albums", "artist", created.Name, "err", aerr)
		if err := s.repo.SetPendingAdd(ctx, created.ID, opt.Monitor); err != nil {
			return Artist{}, err
		}
	} else {
		s.repo.AddEvent(ctx, created.ID, "refreshed", fmt.Sprintf("Found %d album(s)", res.Listed))
		_ = s.repo.StampRefreshed(ctx, created.ID, s.clock())
	}
	return s.GetArtist(ctx, created.ID)
}

// EnsureTracks fills in an album's track listing when its target edition has none yet.
// Safe to call repeatedly — an album that already has a listing costs nothing. With a
// provider that lists editions (MusicBrainz) it lists every edition and keeps the target
// one by policy (EnsureEditions), which also re-keys a migrated album.
func (s *Service) EnsureTracks(ctx context.Context, al Album) error {
	if ep, ok := s.meta.(metadata.EditionProvider); ok {
		return s.ensureEditions(ctx, ep, al)
	}
	return s.ensureListing(ctx, al)
}

// ensureListing is the single-listing path: the listing of the release group's
// representative release, stored as the target edition when there is none.
func (s *Service) ensureListing(ctx context.Context, al Album) error {
	if al.EditionTracks > 0 {
		return nil
	}
	tracks, err := s.meta.AlbumTracks(ctx, al.MBID)
	if err != nil {
		return err
	}
	if len(tracks) == 0 {
		return nil // no official release carries a listing yet (a future album, say)
	}
	l := Listing{Tracks: make([]EditionTrack, 0, len(tracks))}
	for _, t := range tracks {
		if l.ReleaseMBID == "" {
			l.ReleaseMBID = t.ReleaseMBID
		}
		l.Tracks = append(l.Tracks, EditionTrack{
			Disc: t.Disc, Position: t.Number, Number: t.Printed, Title: t.Title,
			MBID: t.MBID, RecordingMBID: t.RecordingMBID, LengthMs: t.DurationSec * 1000,
		})
	}
	return s.repo.UpsertListing(ctx, al.ID, l)
}

// Refresh re-pulls an artist's metadata and album list (the Refresh button): the same
// rules as the scheduled refresh (RefreshArtist), with a history line either way.
func (s *Service) Refresh(ctx context.Context, id int64) (Artist, error) {
	if _, err := s.RefreshArtist(ctx, id, RefreshOptions{}); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Artist{}, err
		}
		s.log.Warn("music: album refresh failed", "artist_id", id, "err", err)
	}
	return s.GetArtist(ctx, id)
}

// Albums returns the albums filed under an artist.
func (s *Service) Albums(ctx context.Context, artistID int64) ([]Album, error) {
	return s.repo.AlbumsFor(ctx, artistID)
}

// WantedAlbums returns the monitored albums of monitored artists, with track counts.
func (s *Service) WantedAlbums(ctx context.Context) ([]Album, error) { return s.repo.WantedAlbums(ctx) }

// RecordSearchMiss counts a search that found nothing usable for an album (see the sweep).
func (s *Service) RecordSearchMiss(ctx context.Context, albumID int64) error {
	return s.repo.RecordSearchMiss(ctx, albumID)
}

// ResetSearchMisses clears an album's search backoff after a grab.
func (s *Service) ResetSearchMisses(ctx context.Context, albumID int64) error {
	return s.repo.ResetSearchMisses(ctx, albumID)
}

// TouchSearch stamps an album's search time without counting a miss (see the sweep).
func (s *Service) TouchSearch(ctx context.Context, albumID int64) error {
	return s.repo.TouchSearch(ctx, albumID)
}

// SearchAlbums finds library albums by album title or artist name, for pickers that
// need one album out of the whole library.
func (s *Service) SearchAlbums(ctx context.Context, q string, limit int) ([]AlbumHit, error) {
	return s.repo.SearchAlbums(ctx, q, limit)
}

// FilePaths lists every track file the library has on record, for checks that ask
// whether a missing folder used to hold anything (a share that isn't mounted).
func (s *Service) FilePaths(ctx context.Context) ([]string, error) {
	return s.repo.FilePaths(ctx)
}

// Tracks returns an album's target-edition tracks, each with its file.
func (s *Service) Tracks(ctx context.Context, albumID int64) ([]Track, error) {
	return s.repo.TracksFor(ctx, albumID)
}

// Editions lists an album's editions, the target first.
func (s *Service) Editions(ctx context.Context, albumID int64) ([]Edition, error) {
	return s.repo.Editions(ctx, albumID)
}

// SetArtistMonitored toggles an artist (cascading to its albums and tracks).
func (s *Service) SetArtistMonitored(ctx context.Context, id int64, monitored bool) error {
	return s.repo.SetArtistMonitored(ctx, id, monitored)
}

// SetAlbumMonitored toggles one album (and its tracks): a person's choice, so a single
// waiting for the automatic singles check is taken out of it and stays as they set it.
func (s *Service) SetAlbumMonitored(ctx context.Context, id int64, monitored bool) error {
	if err := s.repo.SetAlbumMonitored(ctx, id, monitored); err != nil {
		return err
	}
	return s.repo.ClearPendingSingle(ctx, id)
}

// SetQualityProfile changes an artist's quality profile.
func (s *Service) SetQualityProfile(ctx context.Context, id int64, profile string) error {
	return s.repo.SetArtistQualityProfile(ctx, id, profile)
}

// MarkTrackImported records a track's file on disk with just its tier and bitrate.
func (s *Service) MarkTrackImported(ctx context.Context, trackID int64, path, format string, bitrate int, size int64, sourceRelease string) error {
	return s.repo.SetTrackFile(ctx, trackID, path, format, bitrate, size, sourceRelease)
}

// RecordTrackFile records a track's file on disk with everything known about it.
func (s *Service) RecordTrackFile(ctx context.Context, trackID int64, rec FileRecord) error {
	return s.repo.RecordTrackFile(ctx, trackID, rec)
}

// HeldTiers is the lowest tier the album holds (ok false when it holds nothing).
func (s *Service) HeldTiers(ctx context.Context, albumID int64) (Quality, bool, error) {
	return s.repo.HeldTiers(ctx, albumID)
}

// UnmeasuredFiles lists files not yet measured with every fact (see the scan).
func (s *Service) UnmeasuredFiles(ctx context.Context, afterID int64, limit int) ([]TrackFile, error) {
	return s.repo.UnmeasuredFiles(ctx, afterID, limit)
}

// SetFileMeasured records a held file's measured tier and facts.
func (s *Service) SetFileMeasured(ctx context.Context, fileID int64, path string, q Quality, info mediainfo.AudioInfo) error {
	return s.repo.SetFileMeasured(ctx, fileID, path, q, info)
}

// ClearTrackFile flips a track back to wanted (its file row stays, as an album extra).
func (s *Service) ClearTrackFile(ctx context.Context, trackID int64) error {
	return s.repo.ClearTrackFile(ctx, trackID)
}

// Delete removes an artist from the library: rows only, never files. Albums another
// library artist is credited on stay (see Repo.DeleteArtist).
func (s *Service) Delete(ctx context.Context, id int64) error {
	defer s.forgetNames()
	return s.repo.DeleteArtist(ctx, id)
}

// AddEvent appends an artist timeline event.
func (s *Service) AddEvent(ctx context.Context, id int64, event, detail string) {
	s.repo.AddEvent(ctx, id, event, detail)
}

// Events returns an artist's activity timeline.
func (s *Service) Events(ctx context.Context, id int64, limit int) ([]Event, error) {
	return s.repo.Events(ctx, id, limit)
}
