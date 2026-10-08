package music

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

func testRepo(t *testing.T) (*Repo, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewRepo(st.DB()), context.Background()
}

// HasArtists is what keeps Music on for an install already using it.
func TestHasArtists(t *testing.T) {
	r, ctx := testRepo(t)
	if has, err := r.HasArtists(ctx); err != nil || has {
		t.Fatalf("empty library: has=%v err=%v, want false", has, err)
	}
	if _, err := r.CreateArtist(ctx, Artist{MBID: "a-1", Name: "Radiohead"}); err != nil {
		t.Fatal(err)
	}
	if has, err := r.HasArtists(ctx); err != nil || !has {
		t.Fatalf("after adding an artist: has=%v err=%v, want true", has, err)
	}
}

// The backoff state: a miss counts and stamps, a reset zeroes.
func TestAlbumSearchMisses(t *testing.T) {
	r, ctx := testRepo(t)
	a, err := r.CreateArtist(ctx, Artist{MBID: "a-1", Name: "Radiohead", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.UpsertAlbum(ctx, Album{ArtistID: a.ID, MBID: "rg-1", Title: "OK Computer", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	if al, _ := r.GetAlbum(ctx, id); al.SearchMisses != 0 || al.LastSearchAt != "" {
		t.Fatalf("new album: misses=%d last=%q, want a clean slate", al.SearchMisses, al.LastSearchAt)
	}
	for i := 0; i < 2; i++ {
		if err := r.RecordSearchMiss(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if al, _ := r.GetAlbum(ctx, id); al.SearchMisses != 2 || al.LastSearchAt == "" {
		t.Fatalf("after two misses: misses=%d last=%q", al.SearchMisses, al.LastSearchAt)
	}
	if err := r.ResetSearchMisses(ctx, id); err != nil {
		t.Fatal(err)
	}
	if al, _ := r.GetAlbum(ctx, id); al.SearchMisses != 0 || al.LastSearchAt == "" {
		t.Fatalf("after a reset: misses=%d last=%q, want 0 and still stamped", al.SearchMisses, al.LastSearchAt)
	}
}

// WantedAlbums is every monitored album of a monitored artist, with its counts.
func TestWantedAlbums(t *testing.T) {
	r, ctx := testRepo(t)
	on, _ := r.CreateArtist(ctx, Artist{MBID: "a-1", Name: "Radiohead", Monitored: true})
	off, _ := r.CreateArtist(ctx, Artist{MBID: "a-2", Name: "Muse", Monitored: false})
	half, _ := r.UpsertAlbum(ctx, Album{ArtistID: on.ID, MBID: "rg-1", Title: "OK Computer", Monitored: true})
	empty, _ := r.UpsertAlbum(ctx, Album{ArtistID: on.ID, MBID: "rg-2", Title: "Kid A", Monitored: true})
	_, _ = r.UpsertAlbum(ctx, Album{ArtistID: on.ID, MBID: "rg-3", Title: "Amnesiac", Monitored: false})
	_, _ = r.UpsertAlbum(ctx, Album{ArtistID: off.ID, MBID: "rg-4", Title: "Absolution", Monitored: true})
	if err := r.UpsertTracks(ctx, half, []Track{
		{DiscNumber: 1, TrackNumber: 1, Title: "Airbag"},
		{DiscNumber: 1, TrackNumber: 2, Title: "Paranoid Android"},
		{DiscNumber: 1, TrackNumber: 3, Title: "Subterranean Homesick Alien"},
	}); err != nil {
		t.Fatal(err)
	}
	tracks, _ := r.TracksFor(ctx, half)
	if err := r.SetTrackFile(ctx, tracks[0].ID, "/music/airbag.flac", "FLAC", 0, 1000, ""); err != nil {
		t.Fatal(err)
	}

	got, err := r.WantedAlbums(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d albums, want OK Computer and Kid A only: %+v", len(got), got)
	}
	byID := map[int64]Album{}
	for _, al := range got {
		byID[al.ID] = al
	}
	if al := byID[half]; al.Title != "OK Computer" || al.TrackCount != 3 || al.HaveTracks != 1 || al.SizeBytes != 1000 {
		t.Errorf("OK Computer = %+v, want 3 tracks, 1 held, 1000 bytes", al)
	}
	if al := byID[empty]; al.Title != "Kid A" || al.TrackCount != 0 || al.HaveTracks != 0 {
		t.Errorf("Kid A = %+v, want no tracks", al)
	}
}
