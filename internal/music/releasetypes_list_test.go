package music

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// shownTitles is the artist page's album list, by MBID, with whether each is shown
// outside the artist's types.
func shownTitles(t *testing.T, f *refreshFixture, artistID int64) (map[string]bool, Artist) {
	t.Helper()
	a, err := f.svc.GetArtist(f.ctx, artistID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, al := range a.Albums {
		out[al.MBID] = al.OutsideTypes
	}
	return out, a
}

// Issue #122: unticking EP in the library's release types takes the EPs off an artist's
// album list from the next refresh on, the ones listed before the change included, and
// never adds a newly released one. An EP that is wanted or holds a file stays (marked as
// outside the types); nothing is deleted, so ticking EP again brings the rest back.
func TestUntickedEPsLeaveTheArtistList(t *testing.T) {
	f := newRefreshFixture(t)
	f.p.main["mb-rh"] = []metadata.AlbumResult{
		rg("rg-okc", "OK Computer", "Album", "1997-05-21"),
		rg("rg-ep-old", "My Iron Lung", "EP", "1994-09-26"),
		rg("rg-ep-wanted", "Airbag / How Am I Driving?", "EP", "1998-04-21"),
		rg("rg-ep-files", "COM LAG", "EP", "2004-03-24"),
	}
	a, err := f.svc.AddArtistWith(f.ctx, "mb-rh", "", AddOptions{Monitored: true, Monitor: MonitorNone})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := shownTitles(t, f, a.ID); len(got) != 4 {
		t.Fatalf("before the change the page lists %v, want all four (Album and EP are the default)", got)
	}
	// One EP is wanted by hand; another holds a file (not placed on a track: it still
	// counts) and isn't wanted.
	if err := f.svc.SetAlbumMonitored(f.ctx, f.album(t, "rg-ep-wanted").ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.db.ExecContext(f.ctx, `INSERT INTO music_track_files (album_id, path, quality) VALUES (?, '/m/comlag/01.flac', 'FLAC')`,
		f.album(t, "rg-ep-files").ID); err != nil {
		t.Fatal(err)
	}

	// Settings → Music: new releases → Kinds: Album only. A new EP comes out meanwhile.
	f.sets[KeyReleaseTypes] = `{"primary":["Album"],"secondary":[]}`
	f.p.main["mb-rh"] = append(f.p.main["mb-rh"], rg("rg-ep-new", "New EP", "EP", "2026-10-10"))
	if _, err := f.svc.RefreshArtist(f.ctx, a.ID, RefreshOptions{Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	if f.hasAlbum("rg-ep-new") {
		t.Error("the refresh added a newly released EP although EP is unticked")
	}

	got, art := shownTitles(t, f, a.ID)
	want := map[string]bool{"rg-okc": false, "rg-ep-wanted": true, "rg-ep-files": true}
	if len(got) != len(want) {
		t.Fatalf("after the change the page lists %v, want %v", got, want)
	}
	for mbid, outside := range want {
		if o, ok := got[mbid]; !ok || o != outside {
			t.Errorf("%s: shown=%v outside_types=%v; want shown, outside_types=%v", mbid, ok, o, outside)
		}
	}
	if art.HiddenAlbums != 1 {
		t.Errorf("hidden_albums = %d, want 1 (the old EP)", art.HiddenAlbums)
	}
	// Nothing was deleted, unwanted or wanted by the change.
	if !f.hasAlbum("rg-ep-old") || f.album(t, "rg-ep-old").Monitored || !f.album(t, "rg-ep-wanted").Monitored {
		t.Error("the change of types altered a stored album")
	}
	// The library list counts the same albums the page shows.
	list, err := f.svc.ListArtists(f.ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	if list[0].Stats.Albums != art.Stats.Albums || art.Stats.Albums != 3 {
		t.Errorf("albums counted: list %d, page %d; want 3 in both", list[0].Stats.Albums, art.Stats.Albums)
	}

	// Ticking EP again brings the old EP back at once, without a refresh.
	f.sets[KeyReleaseTypes] = `{"primary":["Album","EP"],"secondary":[]}`
	if got, art := shownTitles(t, f, a.ID); len(got) != 4 || art.HiddenAlbums != 0 || got["rg-ep-wanted"] {
		t.Errorf("with EP ticked again the page lists %v (hidden %d), want all four, none outside the types", got, art.HiddenAlbums)
	}
}

// An artist's own release types decide its list over the library default, both ways.
func TestArtistOwnTypesDecideItsList(t *testing.T) {
	f := newRefreshFixture(t)
	f.p.main["mb-rh"] = []metadata.AlbumResult{rg("rg-okc", "OK Computer", "Album", "1997-05-21"), rg("rg-ep", "My Iron Lung", "EP", "1994-09-26")}
	a, err := f.svc.AddArtistWith(f.ctx, "mb-rh", "", AddOptions{Monitored: true, Monitor: MonitorNone})
	if err != nil {
		t.Fatal(err)
	}
	f.sets[KeyReleaseTypes] = `{"primary":["Album"],"secondary":[]}`
	if _, err := f.svc.SetOptions(f.ctx, a.ID, "", &ReleaseTypes{Primary: []string{"Album", "EP"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := shownTitles(t, f, a.ID); len(got) != 2 {
		t.Errorf("artist ticks EP itself: page lists %v, want both", got)
	}
	f.sets[KeyReleaseTypes] = ""
	if _, err := f.svc.SetOptions(f.ctx, a.ID, "", &ReleaseTypes{Primary: []string{"Album"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := shownTitles(t, f, a.ID); len(got) != 1 {
		t.Errorf("artist unticks EP itself: page lists %v, want the album only", got)
	}
}
