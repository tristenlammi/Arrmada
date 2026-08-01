package music

import "testing"

func TestTrackNumberOf(t *testing.T) {
	cases := []struct {
		path        string
		disc, track int
	}{
		{"01 - Airbag.flac", 1, 1},
		{"01. Airbag.mp3", 1, 1},
		{"07_Fitter Happier.flac", 1, 7},
		{"2-04 Some Song.flac", 2, 4},
		{"Airbag.flac", 0, 0},           // no number at all
		{"1997 - Album Rip.flac", 0, 0}, // a year is not a track number
	}
	for _, c := range cases {
		d, n := TrackNumberOf(c.path)
		if d != c.disc || n != c.track {
			t.Errorf("TrackNumberOf(%q) = (%d,%d), want (%d,%d)", c.path, d, n, c.disc, c.track)
		}
	}
}

// Numbering first, title second, and never position-in-directory: a stray bonus track or
// intro file would shift every song after it, and an album labelled one track out is far
// worse than one left unmatched for a human to look at.
func TestMatchTracks(t *testing.T) {
	tracks := []Track{
		{ID: 1, DiscNumber: 1, TrackNumber: 1, Title: "Airbag"},
		{ID: 2, DiscNumber: 1, TrackNumber: 2, Title: "Paranoid Android"},
		{ID: 3, DiscNumber: 1, TrackNumber: 3, Title: "Subterranean Homesick Alien"},
	}
	files := []AudioFile{
		{Path: "/d/01 - Airbag.flac"},
		{Path: "/d/02 - Paranoid Android.flac"},
		{Path: "/d/Subterranean Homesick Alien.flac"}, // no number — matched by title
		{Path: "/d/bonus - Untitled Jam.flac"},        // matches nothing
	}
	matched, unmatched := MatchTracks(tracks, files)
	if len(matched) != 3 {
		t.Fatalf("want 3 matched, got %d", len(matched))
	}
	if matched[1].Path != "/d/01 - Airbag.flac" || matched[3].Path != "/d/Subterranean Homesick Alien.flac" {
		t.Errorf("wrong mapping: %+v", matched)
	}
	if len(unmatched) != 1 || unmatched[0].Path != "/d/bonus - Untitled Jam.flac" {
		t.Errorf("the unmatched file must be reported, got %+v", unmatched)
	}

	// A file whose number doesn't exist on the album is left alone, not forced onto a slot.
	m2, u2 := MatchTracks(tracks, []AudioFile{{Path: "/d/09 - Ghost Track.flac"}})
	if len(m2) != 0 || len(u2) != 1 {
		t.Errorf("an out-of-range number must not be placed: matched=%v unmatched=%v", m2, u2)
	}
}

// The gate that stops a search for one album grabbing a different one by the same artist —
// the exact hole the Books module had.
func TestReleaseIsForAlbum(t *testing.T) {
	if !ReleaseIsForAlbum("Radiohead - OK Computer (1997) [FLAC]", "Radiohead", "OK Computer") {
		t.Error("the album's own release should pass")
	}
	if ReleaseIsForAlbum("Radiohead - Kid A (2000) [FLAC]", "Radiohead", "OK Computer") {
		t.Error("a different album by the same artist must be refused")
	}
	if ReleaseIsForAlbum("Radiohead - Kid Amnesiae (2021) [FLAC]", "Radiohead", "Kid A") {
		t.Error("word boundaries matter — Kid A must not match inside Kid Amnesiae")
	}
	if ReleaseIsForAlbum("Radiohead - Discography (1993-2016) [FLAC]", "Radiohead", "OK Computer") {
		t.Error("a discography would fill one album's folder with the whole catalogue")
	}
	if ReleaseIsForAlbum("Muse - OK Computer Tribute [FLAC]", "Radiohead", "OK Computer") {
		t.Error("the artist must match too")
	}
}
