package music

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

func TestCreditKeyAndDisplay(t *testing.T) {
	one := []CreditName{{ArtistMBID: "mb-a", Name: "Björk"}}
	if got := CreditKey(one); got != "mb-a" {
		t.Errorf("one artist: key %q, want its MBID", got)
	}
	two := []CreditName{
		{ArtistMBID: "mb-dp", Name: "Daft Punk", JoinPhrase: " feat. "},
		{ArtistMBID: "mb-pw", Name: "Pharrell Williams", JoinPhrase: " & "},
		{ArtistMBID: "mb-nr", Name: "Nile Rodgers"},
	}
	if got := CreditKey(two); got != "mb-dp| feat. |mb-pw| & |mb-nr" {
		t.Errorf("three artists: key %q", got)
	}
	if got := CreditDisplay(two); got != "Daft Punk feat. Pharrell Williams & Nile Rodgers" {
		t.Errorf("display %q", got)
	}
}

// An album with two editions reads back in full: the editions list (target first), the
// target's discs and tracks with their files, and the extras.
func TestTwoEditionAlbumAssembly(t *testing.T) {
	r, ctx := testRepo(t)
	a, err := r.CreateArtist(ctx, Artist{MBID: "mb-rh", Name: "Radiohead", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	pw, err := r.CreateArtist(ctx, Artist{MBID: "mb-guest", Name: "Guest", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	albumID, err := r.UpsertAlbum(ctx, Album{ArtistID: a.ID, MBID: "rg-okc", Title: "OK Computer", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	standard, err := r.AddEdition(ctx, albumID, EditionInput{
		Edition: Edition{MBID: "rel-std", Country: "GB", ReleaseDate: "1997-05-21", MediaSummary: "CD"},
		Tracks: []EditionTrack{
			{Disc: 1, Position: 1, Title: "Airbag", MBID: "rt-1", RecordingMBID: "rec-airbag", LengthMs: 284000},
			{Disc: 1, Position: 2, Title: "Paranoid Android", RecordingMBID: "rec-pa", LengthMs: 383000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The deluxe: the same recordings plus a second disc, one track with a guest credit.
	var guest int64
	if err := store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		var err error
		guest, err = ensureCreditTx(ctx, tx, []CreditName{
			{ArtistID: a.ID, ArtistMBID: "mb-rh", Name: "Radiohead", JoinPhrase: " feat. "},
			{ArtistID: pw.ID, ArtistMBID: "mb-guest", Name: "Guest"},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	deluxe, err := r.AddEdition(ctx, albumID, EditionInput{
		Edition: Edition{MBID: "rel-dlx", ReleaseDate: "2009-03-24", Disambiguation: "collector's edition"},
		Tracks: []EditionTrack{
			{Disc: 1, Position: 1, Title: "Airbag", RecordingMBID: "rec-airbag", LengthMs: 284000},
			{Disc: 1, Position: 2, Title: "Paranoid Android", RecordingMBID: "rec-pa"},
			{Disc: 2, DiscTitle: "Bonus", Position: 1, Title: "Lull", CreditID: guest, LengthMs: 120000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetTargetEdition(ctx, albumID, deluxe, false); err != nil {
		t.Fatal(err)
	}

	eds, err := r.Editions(ctx, albumID)
	if err != nil || len(eds) != 2 {
		t.Fatalf("Editions = %+v (err %v), want two", eds, err)
	}
	if eds[0].ID != deluxe || !eds[0].Target || eds[1].ID != standard || eds[1].Target {
		t.Errorf("editions order/target = %+v, want the deluxe target first", eds)
	}
	if eds[0].TrackCount != 3 || eds[0].DiscCount != 2 || eds[1].TrackCount != 2 || eds[1].DiscCount != 1 {
		t.Errorf("counts: deluxe %d/%d, standard %d/%d", eds[0].TrackCount, eds[0].DiscCount, eds[1].TrackCount, eds[1].DiscCount)
	}

	full, err := r.Edition(ctx, deluxe)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Media) != 2 || len(full.Media[0].Tracks) != 2 || len(full.Media[1].Tracks) != 1 || full.Media[1].Title != "Bonus" {
		t.Fatalf("deluxe discs = %+v", full.Media)
	}
	// Recordings are shared between the editions by MBID.
	std, _ := r.Edition(ctx, standard)
	if std.Media[0].Tracks[0].RecordingID != full.Media[0].Tracks[0].RecordingID {
		t.Error("the two editions' Airbag should share one recording")
	}
	if full.Media[1].Tracks[0].CreditID != guest {
		t.Errorf("Lull's credit = %d, want the guest credit %d", full.Media[1].Tracks[0].CreditID, guest)
	}
	creds, err := r.Credits(ctx, []int64{guest})
	if err != nil || creds[guest].Display != "Radiohead feat. Guest" || len(creds[guest].Names) != 2 {
		t.Errorf("guest credit = %+v (err %v)", creds[guest], err)
	}

	// A file on the deluxe's Lull, and an extra that's on no track.
	lull := full.Media[1].Tracks[0].ID
	if _, err := r.db.ExecContext(ctx, `INSERT INTO music_track_files (album_id, release_track_id, path, quality, bitrate_kbps, size_bytes)
	                                     VALUES (?, ?, '/m/2-01 Lull.flac', 'FLAC', 900, 1000)`, albumID, lull); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `INSERT INTO music_track_files (album_id, path, quality) VALUES (?, '/m/hidden.flac', 'FLAC')`, albumID); err != nil {
		t.Fatal(err)
	}
	full, _ = r.Edition(ctx, deluxe)
	if f := full.Media[1].Tracks[0].File; f == nil || f.Path != "/m/2-01 Lull.flac" || f.Quality != QualityFLAC {
		t.Errorf("Lull's file = %+v", f)
	}
	files, _ := r.AlbumFiles(ctx, albumID)
	if len(files) != 2 || files[0].Path != "/m/2-01 Lull.flac" || files[1].ReleaseTrackID != 0 {
		t.Errorf("album files = %+v, want Lull's and the extra", files)
	}

	// Another album's edition can't be made this one's target.
	other, _ := r.UpsertAlbum(ctx, Album{ArtistID: a.ID, MBID: "rg-kida", Title: "Kid A", Monitored: true})
	if err := r.SetTargetEdition(ctx, other, deluxe, false); err == nil {
		t.Error("another album's edition was accepted as Kid A's target")
	}
	// A release MBID already stored is not stored twice (one release group per release).
	dup, err := r.AddEdition(ctx, other, EditionInput{Edition: Edition{MBID: "rel-std"}})
	if err != nil {
		t.Fatal(err)
	}
	if eds, _ := r.Editions(ctx, other); len(eds) != 1 || eds[0].ID != dup || eds[0].MBID != "" {
		t.Errorf("a duplicate release MBID: %+v, want the edition stored without it", eds)
	}

	if err := r.AddExternalID(ctx, ExternalID{EntityType: "recording", EntityID: full.Media[0].Tracks[0].RecordingID, Source: "isrc", Value: "GBAYE9700001"}); err != nil {
		t.Fatal(err)
	}
	_ = r.AddExternalID(ctx, ExternalID{EntityType: "recording", EntityID: full.Media[0].Tracks[0].RecordingID, Source: "isrc", Value: "GBAYE9700001"})
	if ids, err := r.ExternalIDs(ctx, "recording", full.Media[0].Tracks[0].RecordingID); err != nil || len(ids) != 1 {
		t.Errorf("external ids = %+v (err %v), want the ISRC once", ids, err)
	}
}
