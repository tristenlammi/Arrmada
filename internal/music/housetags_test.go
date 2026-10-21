package music

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/tristenlammi/arrmada/internal/audiotag"
	"github.com/tristenlammi/arrmada/internal/store"
)

const (
	mbDaft   = "056e4f3e-d505-4dad-8ec1-d04f521cbb56"
	mbPharr  = "149e6720-4e4a-41a4-afca-6d29083fc091"
	mbRG     = "3c1d6b4e-8d0c-4a59-9e57-6f3e4f1d2a10"
	mbRel    = "0a8e97fd-4566-4a8b-a6bc-5bb0e9d1a7f2"
	mbRec1   = "1f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
	mbRec2   = "2f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
	mbRT1    = "3f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
	mbArtist = "4f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
)

// houseAlbum builds a two-disc album with a featured artist on one track and an ISRC.
func houseAlbum(t *testing.T, r *Repo, ctx context.Context, va bool) (TagContext, []int64) {
	t.Helper()
	artistMBID, artistName := mbDaft, "Daft Punk"
	if va {
		artistMBID, artistName = VariousArtistsMBID, "Various Artists"
	}
	a, err := r.CreateArtist(ctx, Artist{MBID: artistMBID, Name: artistName, SortName: "Daft Punk", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	ph, err := r.CreateArtist(ctx, Artist{MBID: mbPharr, Name: "Pharrell Williams", SortName: "Williams, Pharrell", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	albumType := "Album"
	if va {
		albumType = "Compilation"
	}
	albumID, err := r.UpsertAlbum(ctx, Album{ArtistID: a.ID, MBID: mbRG, Title: "Random Access Memories", Year: 2013,
		ReleaseDate: "2013-05-17", AlbumType: albumType, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	var feat int64
	if err := store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		var err error
		feat, err = ensureCreditTx(ctx, tx, []CreditName{
			{ArtistID: a.ID, ArtistMBID: artistMBID, Name: artistName, JoinPhrase: " feat. "},
			{ArtistID: ph.ID, ArtistMBID: mbPharr, Name: "Pharrell Williams"},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ed, err := r.AddEdition(ctx, albumID, EditionInput{
		Edition: Edition{MBID: mbRel, Title: "Random Access Memories", Status: "Official", Country: "XE",
			ReleaseDate: "2013-05-20", Label: "Columbia", CatalogNumber: "88883716861", Barcode: "888837168612"},
		Tracks: []EditionTrack{
			{Disc: 1, DiscFormat: "CD", Position: 1, Title: "Give Life Back to Music", MBID: mbRT1, RecordingMBID: mbRec1},
			{Disc: 1, DiscFormat: "CD", Position: 2, Title: "Get Lucky", RecordingMBID: mbRec2, CreditID: feat},
			{Disc: 2, DiscFormat: "CD", DiscTitle: "Bonus", Position: 1, Title: "Horizon"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetTargetEdition(ctx, albumID, ed, false); err != nil {
		t.Fatal(err)
	}
	full, _ := r.Edition(ctx, ed)
	if err := r.AddExternalID(ctx, ExternalID{EntityType: "recording", EntityID: full.Media[0].Tracks[1].RecordingID, Source: "isrc", Value: "USQX91300108"}); err != nil {
		t.Fatal(err)
	}
	tc, err := r.tagContext(ctx, albumID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return tc, []int64{full.Media[0].Tracks[0].ID, full.Media[0].Tracks[1].ID, full.Media[1].Tracks[0].ID}
}

// MUS-33: every house field comes from the matched edition and its credits.
func TestHouseTagsFromTheEdition(t *testing.T) {
	r, ctx := testRepo(t)
	tc, ids := houseAlbum(t, r, ctx, false)

	got, ok := tc.HouseTags(ids[1], audiotag.Tags{audiotag.KeyGenre: {"Disco"}, audiotag.KeyTitle: {"old title"}, audiotag.KeyMBReleaseID: {"stale"}})
	if !ok {
		t.Fatal("no tags for the track")
	}
	want := audiotag.Tags{
		audiotag.KeyTitle:            {"Get Lucky"},
		audiotag.KeyArtist:           {"Daft Punk feat. Pharrell Williams"},
		audiotag.KeyArtists:          {"Daft Punk", "Pharrell Williams"},
		audiotag.KeyArtistSort:       {"Daft Punk feat. Williams, Pharrell"},
		audiotag.KeyAlbum:            {"Random Access Memories"},
		audiotag.KeyAlbumArtist:      {"Daft Punk"},
		audiotag.KeyAlbumArtists:     {"Daft Punk"},
		audiotag.KeyAlbumArtistSort:  {"Daft Punk"},
		audiotag.KeyTrackNumber:      {"2"},
		audiotag.KeyTrackTotal:       {"2"},
		audiotag.KeyDiscNumber:       {"1"},
		audiotag.KeyDiscTotal:        {"2"},
		audiotag.KeyDate:             {"2013-05-20"},
		audiotag.KeyOriginalDate:     {"2013-05-17"},
		audiotag.KeyReleaseType:      {"album"},
		audiotag.KeyReleaseStatus:    {"official"},
		audiotag.KeyReleaseCountry:   {"XE"},
		audiotag.KeyMedia:            {"CD"},
		audiotag.KeyLabel:            {"Columbia"},
		audiotag.KeyCatalogNumber:    {"88883716861"},
		audiotag.KeyBarcode:          {"888837168612"},
		audiotag.KeyISRC:             {"USQX91300108"},
		audiotag.KeyMBRecordingID:    {mbRec2},
		audiotag.KeyMBReleaseID:      {mbRel},
		audiotag.KeyMBReleaseGroupID: {mbRG},
		audiotag.KeyMBArtistID:       {mbDaft, mbPharr},
		audiotag.KeyMBAlbumArtistID:  {mbDaft},
		audiotag.KeyGenre:            {"Disco"}, // no source of Arrmada's own: kept as found
	}
	for k := range mergeKeys(got, want) {
		if !reflect.DeepEqual(got[k], want[k]) {
			t.Errorf("%s = %q, want %q", k, got[k], want[k])
		}
	}
	if got[audiotag.KeyCompilation] != nil {
		t.Error("an ordinary album is marked a compilation")
	}

	// Disc 2's track: its own disc number and title, and the release-track MBID only where
	// there is one.
	d2, _ := tc.HouseTags(ids[2], nil)
	if d2.Get(audiotag.KeyDiscNumber) != "2" || d2.Get(audiotag.KeyDiscSubtitle) != "Bonus" || d2.Get(audiotag.KeyTrackTotal) != "1" {
		t.Errorf("disc 2 track: %v", d2)
	}
	if t1, _ := tc.HouseTags(ids[0], nil); t1.Get(audiotag.KeyMBReleaseTrackID) != mbRT1 {
		t.Errorf("release track id: %v", t1)
	}
	// Every key is a house key and every single-value key has one value: audiotag would
	// refuse the write otherwise (before it opens the file, so a missing one says which).
	err := audiotag.New(audiotag.Permissive).Write("/nonexistent/x.flac", got, audiotag.WriteOptions{})
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("audiotag refused the house tags: %v", err)
	}
}

func mergeKeys(a, b audiotag.Tags) map[string]bool {
	m := map[string]bool{}
	for k := range a {
		m[k] = true
	}
	for k := range b {
		m[k] = true
	}
	return m
}

// A Various Artists album is a compilation: COMPILATION=1, and the names use the
// compilation template's track artist.
func TestHouseTagsCompilation(t *testing.T) {
	r, ctx := testRepo(t)
	tc, ids := houseAlbum(t, r, ctx, true)
	if !tc.Compilation() {
		t.Fatal("a Various Artists album isn't a compilation")
	}
	got, _ := tc.HouseTags(ids[1], nil)
	if got.Get(audiotag.KeyCompilation) != "1" || got.Get(audiotag.KeyAlbumArtist) != "Various Artists" {
		t.Errorf("compilation tags: %v", got)
	}
	n, _ := tc.Name(ids[1], FileFacts{Quality: QualityFLAC, Codec: "flac", BitDepth: 16, SampleRate: 44100})
	if !n.Compilation || n.TrackArtist != "Various Artists feat. Pharrell Williams" || n.AlbumArtist != "Various Artists" || n.DiscCount != 2 || n.Track != 2 {
		t.Errorf("name: %+v", n)
	}
}

// A release that isn't on MusicBrainz writes no MusicBrainz ids.
func TestHouseTagsGapFillWritesNoMBIDs(t *testing.T) {
	r, ctx := testRepo(t)
	tc, ids := houseAlbum(t, r, ctx, false)
	tc.Edition.Source = "deezer"
	got, _ := tc.HouseTags(ids[0], nil)
	for _, k := range []string{audiotag.KeyMBRecordingID, audiotag.KeyMBReleaseID, audiotag.KeyMBReleaseGroupID, audiotag.KeyMBArtistID, audiotag.KeyMBAlbumArtistID, audiotag.KeyMBReleaseTrackID} {
		if got[k] != nil {
			t.Errorf("%s written for a gap-fill release: %v", k, got[k])
		}
	}
	// And a malformed id never reaches a tag either.
	if mbid("deezer:123") != "" || mbid(mbRel) != mbRel {
		t.Error("mbid check")
	}
}

func TestNameFromTheEdition(t *testing.T) {
	r, ctx := testRepo(t)
	tc, ids := houseAlbum(t, r, ctx, false)
	n, ok := tc.Name(ids[2], FileFacts{Quality: QualityFLAC})
	if !ok || n.AlbumArtist != "Daft Punk" || n.AlbumTitle != "Random Access Memories" || n.OriginalYear != 2013 ||
		n.ReleaseYear != 2013 || n.Disc != 2 || n.DiscCount != 2 || n.DiscTitle != "Bonus" || n.Track != 1 || n.Quality != "FLAC" || n.Compilation {
		t.Errorf("name: %+v", n)
	}
	if _, ok := tc.Name(999999, FileFacts{}); ok {
		t.Error("a track off the edition got a name")
	}
	_ = context.Background
}
