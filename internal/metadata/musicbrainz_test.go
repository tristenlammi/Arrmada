package metadata

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mbStub(t *testing.T, h http.HandlerFunc) *MusicBrainz {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	m := NewMusicBrainz()
	m.http = srv.Client()
	m.base = srv.URL
	m.art = "https://art.example"
	// The real 1.1s pacing would make the suite crawl; the pacing itself is not what these
	// tests are checking.
	m.minInterval = 0
	return m
}

// MusicBrainz answers 503 for both rate limiting and ordinary load — often enough that the
// first live run of this client hit one on its second call. It must retry, not fail.
func TestMusicBrainzRetriesServiceUnavailable(t *testing.T) {
	var calls int
	m := mbStub(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"id":"abc","name":"Radiohead","sort-name":"Radiohead"}`)
	})
	d, err := m.GetArtist(context.Background(), "abc")
	if err != nil {
		t.Fatalf("a 503 should be retried, got %v", err)
	}
	if d.Name != "Radiohead" {
		t.Errorf("name = %q", d.Name)
	}
	if calls != 3 {
		t.Errorf("expected 2 failures then a success, got %d calls", calls)
	}
}

// Every request must carry a descriptive User-Agent — MusicBrainz blocks those that don't.
func TestMusicBrainzSendsUserAgent(t *testing.T) {
	var ua string
	m := mbStub(t, func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, `{"artists":[]}`)
	})
	_, _ = m.SearchArtists(context.Background(), "x")
	if !strings.Contains(ua, "Arrmada") || !strings.Contains(ua, "http") {
		t.Errorf("User-Agent = %q, want an app name and a contact URL", ua)
	}
}

// Live albums, compilations and remix editions are secondary types of a release group that
// is usually already listed. Importing them would bury the real albums and mark hundreds of
// unobtainable things wanted.
func TestArtistAlbumsSkipsSecondaryTypes(t *testing.T) {
	m := mbStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"release-groups":[
		  {"id":"rg1","title":"OK Computer","primary-type":"Album","first-release-date":"1997-05-21"},
		  {"id":"rg2","title":"Live at the BBC","primary-type":"Album","secondary-types":["Live"],"first-release-date":"2000-01-01"},
		  {"id":"rg3","title":"Airbag EP","primary-type":"EP","first-release-date":"1998-03-01"}
		]}`)
	})
	albums, err := m.ArtistAlbums(context.Background(), "artist")
	if err != nil {
		t.Fatal(err)
	}
	if len(albums) != 2 {
		t.Fatalf("want the album and the EP but not the live record, got %d: %+v", len(albums), albums)
	}
	// Newest first.
	if albums[0].Title != "Airbag EP" || albums[0].Year != 1998 {
		t.Errorf("expected the 1998 EP first, got %+v", albums[0])
	}
	if albums[1].Year != 1997 {
		t.Errorf("year not parsed from the release date: %+v", albums[1])
	}
	if albums[1].CoverURL != "https://art.example/release-group/rg1/front-500" {
		t.Errorf("cover URL = %q", albums[1].CoverURL)
	}
}

// A release group holds many releases. Taking the deluxe reissue would mark its bonus
// tracks missing forever on an otherwise complete album, so the EARLIEST official release
// is the one to read a listing from.
func TestAlbumTracksPicksEarliestOfficialRelease(t *testing.T) {
	m := mbStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/release/"):
			if !strings.HasSuffix(r.URL.Path, "/original") {
				t.Errorf("read the listing from %q, want the earliest official release", r.URL.Path)
			}
			_, _ = io.WriteString(w, `{"media":[{"position":1,"tracks":[
			  {"id":"t1","title":"Airbag","position":1,"length":284000},
			  {"id":"t2","title":"Paranoid Android","position":2,"length":383000}]}]}`)
		default:
			_, _ = io.WriteString(w, `{"releases":[
			  {"id":"deluxe","date":"2017-06-23","status":"Official"},
			  {"id":"original","date":"1997-05-21","status":"Official"},
			  {"id":"boot","date":"1996-01-01","status":"Bootleg"}
			]}`)
		}
	})
	tracks, err := m.AlbumTracks(context.Background(), "rg1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("want 2 tracks, got %d", len(tracks))
	}
	if tracks[0].Title != "Airbag" || tracks[0].Disc != 1 || tracks[0].Number != 1 {
		t.Errorf("track 1 = %+v", tracks[0])
	}
	if tracks[1].DurationSec != 383 {
		t.Errorf("duration should be seconds, got %d", tracks[1].DurationSec)
	}
}

// An album with no official release yet (an announced-but-unreleased record) is an ordinary
// answer, not a failure.
func TestAlbumTracksWithNoOfficialRelease(t *testing.T) {
	m := mbStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"releases":[{"id":"b","date":"2020-01-01","status":"Bootleg"}]}`)
	})
	tracks, err := m.AlbumTracks(context.Background(), "rg")
	if err != nil || len(tracks) != 0 {
		t.Errorf("want (nil, nil), got %+v / %v", tracks, err)
	}
}
