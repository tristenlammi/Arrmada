package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const tmdbSearchSample = `{
  "results": [
    {"id": 603, "title": "The Matrix", "release_date": "1999-03-30",
     "overview": "Set in the 22nd century...", "poster_path": "/f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg",
     "vote_average": 8.2},
    {"id": 604, "title": "The Matrix Reloaded", "release_date": "2003-05-15",
     "overview": "Six months after...", "poster_path": null, "vote_average": 7.0}
  ]
}`

const tmdbMovieSample = `{
  "id": 603, "title": "The Matrix", "release_date": "1999-03-30",
  "overview": "Set in the 22nd century...", "poster_path": "/f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg",
  "vote_average": 8.2, "imdb_id": "tt0133093", "runtime": 136, "status": "Released"
}`

func TestParseTMDBSearch(t *testing.T) {
	results := parseTMDBSearch([]byte(tmdbSearchSample))
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	r := results[0]
	if r.TMDBID != 603 || r.Title != "The Matrix" || r.Year != 1999 {
		t.Errorf("unexpected result: %+v", r)
	}
	if r.PosterURL != "https://image.tmdb.org/t/p/w500/f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg" {
		t.Errorf("poster = %q", r.PosterURL)
	}
	// Null poster_path → empty URL, not a broken "base+null".
	if results[1].PosterURL != "" {
		t.Errorf("expected empty poster for null path, got %q", results[1].PosterURL)
	}
}

func TestParseTMDBMovie(t *testing.T) {
	m, err := parseTMDBMovie([]byte(tmdbMovieSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.TMDBID != 603 || m.Year != 1999 || m.IMDBID != "tt0133093" || m.Runtime != 136 || m.Status != "Released" {
		t.Errorf("unexpected details: %+v", m)
	}
}

func TestBestTrailerURL(t *testing.T) {
	// Prefer an official YouTube Trailer over a non-official one and over a teaser.
	vids := []tmdbVideo{
		{Key: "teaser1", Site: "YouTube", Type: "Teaser", Official: true},
		{Key: "unofficial", Site: "YouTube", Type: "Trailer", Official: false},
		{Key: "official", Site: "YouTube", Type: "Trailer", Official: true},
	}
	if got := bestTrailerURL(vids); got != "https://www.youtube.com/watch?v=official" {
		t.Errorf("official trailer preference: got %q", got)
	}

	// No official trailer: fall back to the first YouTube Trailer.
	if got := bestTrailerURL([]tmdbVideo{
		{Key: "t1", Site: "YouTube", Type: "Trailer", Official: false},
	}); got != "https://www.youtube.com/watch?v=t1" {
		t.Errorf("trailer fallback: got %q", got)
	}

	// No trailer at all: fall back to a YouTube Teaser.
	if got := bestTrailerURL([]tmdbVideo{
		{Key: "te", Site: "YouTube", Type: "Teaser"},
	}); got != "https://www.youtube.com/watch?v=te" {
		t.Errorf("teaser fallback: got %q", got)
	}

	// Non-YouTube and keyless videos are ignored; clips never qualify.
	if got := bestTrailerURL([]tmdbVideo{
		{Key: "v", Site: "Vimeo", Type: "Trailer", Official: true},
		{Key: "", Site: "YouTube", Type: "Trailer"},
		{Key: "clip", Site: "YouTube", Type: "Clip"},
	}); got != "" {
		t.Errorf("expected empty for no usable trailer, got %q", got)
	}

	if got := bestTrailerURL(nil); got != "" {
		t.Errorf("expected empty for no videos, got %q", got)
	}
}

const tmdbMovieDetailSample = `{
  "id": 603, "title": "The Matrix", "release_date": "1999-03-30",
  "poster_path": "/m.jpg", "vote_average": 8.2, "imdb_id": "tt0133093", "runtime": 136,
  "videos": {"results": [
    {"key": "teaser", "site": "YouTube", "type": "Teaser", "official": false},
    {"key": "trlr", "site": "YouTube", "type": "Trailer", "official": true}
  ]},
  "recommendations": {"results": [
    {"id": 604, "title": "The Matrix Reloaded", "release_date": "2003-05-15",
     "poster_path": "/r.jpg", "backdrop_path": "/rb.jpg", "vote_average": 7.0,
     "overview": "Six months after..."},
    {"id": 605, "title": "No Poster", "release_date": "2005-01-01", "poster_path": null}
  ]}
}`

// The Discover detail record must surface the trailer URL and recommendation cards from
// the append_to_response payload, mapping recommendations exactly like the browse rows
// (posterless ones dropped) and normalizing media_type.
func TestMovieDetailTrailerAndSimilar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(tmdbMovieDetailSample))
	}))
	defer srv.Close()

	tm := NewTMDB("k")
	tm.base = srv.URL
	d, err := tm.MediaDetails(context.Background(), "movie", 603)
	if err != nil {
		t.Fatalf("MediaDetails: %v", err)
	}
	if d.TrailerURL != "https://www.youtube.com/watch?v=trlr" {
		t.Errorf("trailer_url = %q", d.TrailerURL)
	}
	if len(d.Similar) != 1 {
		t.Fatalf("expected 1 similar (posterless dropped), got %d: %+v", len(d.Similar), d.Similar)
	}
	s := d.Similar[0]
	if s.MediaType != "movie" || s.TMDBID != 604 || s.Title != "The Matrix Reloaded" || s.Year != 2003 {
		t.Errorf("unexpected similar item: %+v", s)
	}
	if s.PosterURL != "https://image.tmdb.org/t/p/w500/r.jpg" || s.BackdropURL != "https://image.tmdb.org/t/p/w1280/rb.jpg" {
		t.Errorf("similar image urls: poster=%q backdrop=%q", s.PosterURL, s.BackdropURL)
	}
}

func TestNotConfigured(t *testing.T) {
	p := NewTMDB("")
	if p.Available() {
		t.Error("expected not available without key")
	}
	if _, err := p.SearchMovie(nil, "x"); err != ErrNotConfigured {
		t.Errorf("expected ErrNotConfigured, got %v", err)
	}
}
