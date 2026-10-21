package music

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// listed adds an album with a one-disc listing of the given titles and returns it with
// its tracks.
func listed(t *testing.T, r *Repo, artistID int64, mbid, title string, monitored bool, titles ...string) (int64, []Track) {
	t.Helper()
	ctx := context.Background()
	id, err := r.UpsertAlbum(ctx, Album{ArtistID: artistID, MBID: mbid, Title: title, Monitored: monitored})
	if err != nil {
		t.Fatal(err)
	}
	if len(titles) == 0 {
		return id, nil
	}
	var tracks []Track
	for i, tt := range titles {
		tracks = append(tracks, Track{DiscNumber: 1, TrackNumber: i + 1, Title: tt})
	}
	if err := r.UpsertTracks(ctx, id, tracks); err != nil {
		t.Fatal(err)
	}
	got, err := r.TracksFor(ctx, id)
	if err != nil || len(got) != len(titles) {
		t.Fatalf("tracks = %+v (err %v)", got, err)
	}
	return id, got
}

// Completeness is the target edition's wanted tracks against the ones with a file on
// disk: complete, partial, unlisted (unknown, never complete), a track not wanted, and a
// file that has gone missing.
func TestCompletenessFromTheTargetEdition(t *testing.T) {
	r, ctx := testRepo(t)
	a, _ := r.CreateArtist(ctx, Artist{MBID: "mb-a", Name: "Radiohead", Monitored: true})
	complete, ct := listed(t, r, a.ID, "rg-1", "Complete", true, "One", "Two")
	partial, pt := listed(t, r, a.ID, "rg-2", "Partial", true, "One", "Two")
	unlisted, _ := listed(t, r, a.ID, "rg-3", "Unlisted", true)
	notWanted, nt := listed(t, r, a.ID, "rg-4", "One wanted", true, "One", "Two")
	gone, gt := listed(t, r, a.ID, "rg-5", "Gone", true, "One")
	for i, tr := range ct {
		_ = r.SetTrackFile(ctx, tr.ID, "/m/c"+string(rune('0'+i))+".flac", "FLAC", 900, 10, "")
	}
	_ = r.SetTrackFile(ctx, pt[0].ID, "/m/p.flac", "FLAC", 900, 10, "")
	_ = r.SetTrackFile(ctx, nt[0].ID, "/m/n.flac", "FLAC", 900, 10, "")
	if _, err := r.db.ExecContext(ctx, `UPDATE music_release_tracks SET monitored = 0 WHERE id = ?`, nt[1].ID); err != nil {
		t.Fatal(err)
	}
	_ = r.SetTrackFile(ctx, gt[0].ID, "/m/g.flac", "FLAC", 900, 10, "")
	if _, err := r.db.ExecContext(ctx, `UPDATE music_track_files SET missing_since = CURRENT_TIMESTAMP WHERE path = '/m/g.flac'`); err != nil {
		t.Fatal(err)
	}

	type want struct {
		tracks, have, edition int
		complete              bool
	}
	for id, w := range map[int64]want{
		complete:  {2, 2, 2, true},
		partial:   {2, 1, 2, false},
		unlisted:  {0, 0, 0, false},
		notWanted: {1, 1, 2, true},
		gone:      {1, 0, 1, false},
	} {
		al, err := r.GetAlbum(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if al.TrackCount != w.tracks || al.HaveTracks != w.have || al.EditionTracks != w.edition || al.Complete() != w.complete {
			t.Errorf("%s: %d/%d of %d complete=%v, want %+v", al.Title, al.HaveTracks, al.TrackCount, al.EditionTracks, al.Complete(), w)
		}
	}
	if tracks, _ := r.TracksFor(ctx, gone); tracks[0].HasFile || !tracks[0].Missing || tracks[0].FilePath != "/m/g.flac" {
		t.Errorf("a missing file: %+v, want no file, marked missing, path kept", tracks[0])
	}

	// An unmonitored album still counts all its tracks: its page says 1/2, not 0/0.
	if err := r.SetAlbumMonitored(ctx, partial, false); err != nil {
		t.Fatal(err)
	}
	if al, _ := r.GetAlbum(ctx, partial); al.TrackCount != 2 || al.HaveTracks != 1 {
		t.Errorf("unmonitored album: %d/%d, want 1/2", al.HaveTracks, al.TrackCount)
	}
	if tracks, _ := r.TracksFor(ctx, partial); tracks[0].Monitored || tracks[1].Monitored {
		t.Error("unmonitoring an album should cascade to its tracks, as it always has")
	}
	_ = r.SetAlbumMonitored(ctx, partial, true)
	if tracks, _ := r.TracksFor(ctx, partial); !tracks[0].Monitored || !tracks[1].Monitored {
		t.Error("monitoring an album again should switch its tracks back on")
	}

	// The list and the detail read agree on the stats, and the album counts add up.
	stats, err := r.artistStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	albums, _ := r.AlbumsFor(ctx, a.ID)
	if got, want := *stats[a.ID], *statsFromAlbums(albums); got != want {
		t.Errorf("list stats %+v != detail stats %+v", got, want)
	}
	if s := stats[a.ID]; s.Albums != 5 || s.AlbumsComplete != 2 || s.AlbumsUnlisted != 1 || s.AlbumsWithFiles != 3 ||
		s.AlbumsMonitored != 5 || s.AlbumsWanted != 3 {
		t.Errorf("stats = %+v", *s)
	}

	wanted, _ := r.WantedAlbums(ctx)
	if len(wanted) != 4 {
		t.Errorf("wanted albums = %d, want every monitored album but the one with a file gone from disk (completeness is the sweep's call; a missing file is never searched by itself)", len(wanted))
	}
}

// One file per track: a replacement updates the track's own row, a path held as an album
// extra becomes the track's, a path another track has is refused, and clearing a track
// keeps its file row as an extra.
func TestRecordTrackFile(t *testing.T) {
	r, ctx := testRepo(t)
	a, _ := r.CreateArtist(ctx, Artist{MBID: "mb-a", Name: "Radiohead", Monitored: true})
	albumID, tracks := listed(t, r, a.ID, "rg-1", "OK Computer", true, "Airbag", "Paranoid Android")
	one, two := tracks[0].ID, tracks[1].ID

	if err := r.RecordTrackFile(ctx, one, FileRecord{Path: "/m/01.mp3", SizeBytes: 5, Quality: QualityMP3320, BitrateKbps: 320, SourceRelease: "rel A"}); err != nil {
		t.Fatal(err)
	}
	info := mediainfo.AudioInfo{Codec: "flac", BitsPerSample: 24, SampleRateHz: 96000, Channels: 2, BitrateKbps: 2100, DurationSec: 284.5}
	if err := r.RecordTrackFile(ctx, one, FileRecord{Path: "/m/01.flac", SizeBytes: 9, Quality: QualityFLAC24, BitrateKbps: 2100, Probe: &info}); err != nil {
		t.Fatal(err)
	}
	files, _ := r.AlbumFiles(ctx, albumID)
	if len(files) != 1 {
		t.Fatalf("files after a replacement = %+v, want the one row", files)
	}
	f := files[0]
	if f.Path != "/m/01.flac" || f.Quality != QualityFLAC24 || !f.Lossless || f.Codec != "flac" || f.BitDepth != 24 ||
		f.SampleRateHz != 96000 || f.Channels != 2 || f.DurationMs != 284500 || f.ProbedAt == "" || f.Container != "flac" ||
		f.SourceRelease != "rel A" || f.TagState != TagStateAsFound {
		t.Errorf("replaced file row = %+v", f)
	}

	// An extra at a path, then recorded for track two: the same row, now the track's.
	if _, err := r.db.ExecContext(ctx, `INSERT INTO music_track_files (album_id, path, quality) VALUES (?, '/m/02.flac', 'FLAC')`, albumID); err != nil {
		t.Fatal(err)
	}
	if err := r.SetTrackFile(ctx, two, "/m/02.flac", "FLAC", 900, 7, ""); err != nil {
		t.Fatal(err)
	}
	if files, _ := r.AlbumFiles(ctx, albumID); len(files) != 2 || files[1].ReleaseTrackID != two {
		t.Errorf("after adopting the extra: %+v", files)
	}
	// Track one can't take track two's file.
	if err := r.SetTrackFile(ctx, one, "/m/02.flac", "FLAC", 900, 7, ""); !errors.Is(err, ErrPathTaken) {
		t.Errorf("recording another track's file: err = %v, want ErrPathTaken", err)
	}
	// Clearing track two leaves its row as an extra; the track is wanted again.
	if err := r.ClearTrackFile(ctx, two); err != nil {
		t.Fatal(err)
	}
	tracks, _ = r.TracksFor(ctx, albumID)
	files, _ = r.AlbumFiles(ctx, albumID)
	if tracks[1].HasFile || len(files) != 2 || files[1].ReleaseTrackID != 0 {
		t.Errorf("after clearing: track %+v, files %+v", tracks[1], files)
	}
	if err := r.SetTrackFile(ctx, 999999, "/m/x.flac", "FLAC", 0, 0, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown track: err = %v, want ErrNotFound", err)
	}

	// A re-listing keeps files and monitoring and takes the new titles.
	if err := r.UpsertTracks(ctx, albumID, []Track{{DiscNumber: 1, TrackNumber: 1, Title: "Airbag (Remastered)"}}); err != nil {
		t.Fatal(err)
	}
	tracks, _ = r.TracksFor(ctx, albumID)
	if tracks[0].Title != "Airbag (Remastered)" || !tracks[0].HasFile || tracks[0].FilePath != "/m/01.flac" {
		t.Errorf("re-listed track = %+v", tracks[0])
	}
}

// MUS-29 step 7: deleting an artist removes it from the library, keeps the albums
// another library artist is credited on (credit intact), deletes the rest, and removes
// the artist outright once nothing credits it. Rows only.
func TestDeleteArtistKeepsCreditedAlbums(t *testing.T) {
	r, ctx := testRepo(t)
	host, _ := r.CreateArtist(ctx, Artist{MBID: "mb-host", Name: "Host", Monitored: true})
	guest, _ := r.CreateArtist(ctx, Artist{MBID: "mb-guest", Name: "Guest", Monitored: true})
	solo, _ := r.CreateArtist(ctx, Artist{MBID: "mb-solo", Name: "Solo", Monitored: true})

	// Host's own album, and a Host album that credits Guest as well.
	own, _ := listed(t, r, host.ID, "rg-own", "Own", true, "One")
	duet, _ := listed(t, r, host.ID, "rg-duet", "Duet", true, "One")
	var duetCredit int64
	if err := store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		var err error
		duetCredit, err = ensureCreditTx(ctx, tx, []CreditName{
			{ArtistID: host.ID, ArtistMBID: "mb-host", Name: "Host", JoinPhrase: " & "},
			{ArtistID: guest.ID, ArtistMBID: "mb-guest", Name: "Guest"},
		})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE albums SET artist_credit_id = ? WHERE id = ?`, duetCredit, duet)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if on, _ := r.AppearsOn(ctx, guest.ID); len(on) != 1 || on[0].ID != duet || on[0].ArtistCredit != "Host & Guest" {
		t.Errorf("Guest appears on %+v, want Duet credited to both", on)
	}
	soloAlbum, _ := listed(t, r, solo.ID, "rg-solo", "Solo Album", true, "One")
	r.AddEvent(ctx, solo.ID, "added", "Added to library")

	if err := r.DeleteArtist(ctx, host.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetAlbum(ctx, own); !errors.Is(err, ErrNotFound) {
		t.Errorf("Host's own album: err = %v, want deleted", err)
	}
	al, err := r.GetAlbum(ctx, duet)
	if err != nil || al.CreditID != duetCredit || al.ArtistCredit != "Host & Guest" || al.EditionTracks != 1 {
		t.Errorf("the duet album = %+v (err %v), want kept with its credit and listing", al, err)
	}
	h, err := r.GetArtist(ctx, host.ID)
	if err != nil || h.InLibrary || h.Monitored {
		t.Errorf("Host = %+v (err %v), want kept as a credited-only artist, unmonitored", h, err)
	}
	list, _ := r.ListArtists(ctx)
	for _, a := range list {
		if a.ID == host.ID {
			t.Error("Host is still listed in the library")
		}
	}

	// Nothing credits Solo once its album is gone: it goes outright, history and all.
	if err := r.DeleteArtist(ctx, solo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetArtist(ctx, solo.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Solo: err = %v, want removed", err)
	}
	if _, err := r.GetAlbum(ctx, soloAlbum); !errors.Is(err, ErrNotFound) {
		t.Error("Solo's album is still there")
	}
	var n int
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM music_recordings WHERE id NOT IN (SELECT recording_id FROM music_release_tracks)`).Scan(&n)
	if n != 0 {
		t.Errorf("%d recordings left with no track", n)
	}
	if err := r.DeleteArtist(ctx, solo.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting a removed artist: err = %v, want ErrNotFound", err)
	}

	// Adding Host again brings the credited-only row back into the library; adding a
	// library artist twice is still refused.
	back, err := r.CreateArtist(ctx, Artist{MBID: "mb-host", Name: "Host", Monitored: true, QualityProfile: "lossless"})
	if err != nil || back.ID != host.ID || !back.InLibrary || !back.Monitored || back.QualityProfile != "lossless" {
		t.Errorf("re-adding Host = %+v (err %v), want the same row back in the library", back, err)
	}
	if _, err := r.CreateArtist(ctx, Artist{MBID: "mb-guest", Name: "Guest"}); !errors.Is(err, ErrExists) {
		t.Errorf("adding Guest twice: err = %v, want ErrExists", err)
	}
}

// listingProvider answers one album's listing with real MusicBrainz ids.
type listingProvider struct{ flakyProvider }

func (listingProvider) AlbumTracks(context.Context, string) ([]metadata.TrackResult, error) {
	return []metadata.TrackResult{
		{MBID: "rt-1", RecordingMBID: "rec-1", ReleaseMBID: "rel-1", Title: "Airbag", Disc: 1, Number: 1, Printed: "1", DurationSec: 284},
		{MBID: "rt-2", RecordingMBID: "rec-2", ReleaseMBID: "rel-1", Title: "Side B", Disc: 2, Number: 1, Printed: "B1", DurationSec: 200},
	}, nil
}

// A listing fetched after the upgrade becomes the album's target edition, keyed to the
// release it was read from, with real recording ids and the printed numbers.
func TestEnsureTracksStoresTheRelease(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := &listingProvider{flakyProvider{albums: []metadata.AlbumResult{{MBID: "rg1", Title: "OK Computer", Type: "Live"}}}}
	s := NewService(st.DB(), p, slog.Default())
	ctx := context.Background()
	// A live album is listed only when the artist's release types allow Live (MUS-14).
	live := ReleaseTypes{Primary: []string{"Album"}, Secondary: []string{"Live"}}
	a, err := s.AddArtistWith(ctx, "abc", "", AddOptions{Monitored: true, ReleaseTypes: &live})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Albums) == 0 {
		t.Fatal("the live album wasn't listed")
	}
	if al := a.Albums[0]; al.PrimaryType != "Album" || al.SecondaryTypes != "Live" || al.ArtistCredit != "Radiohead" {
		t.Errorf("new album types/credit = %q/%q/%q", al.PrimaryType, al.SecondaryTypes, al.ArtistCredit)
	}
	al, err := s.GetAlbum(ctx, a.Albums[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(al.Editions) != 1 || al.Editions[0].MBID != "rel-1" || al.Editions[0].Legacy || !al.Editions[0].Target ||
		len(al.Discs) != 2 || len(al.Tracks) != 2 {
		t.Fatalf("album = %+v", al)
	}
	if tr := al.Tracks[1]; tr.RecordingMBID != "rec-2" || tr.MBID != "rt-2" || tr.Number != "B1" || tr.DiscNumber != 2 || tr.DurationSec != 200 {
		t.Errorf("track 2 = %+v", tr)
	}
	if tr := al.Tracks[0]; tr.Number != "" {
		t.Errorf("a printed number that is just the position should read empty, got %q", tr.Number)
	}
}
