package metadata

import "testing"

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

func TestNotConfigured(t *testing.T) {
	p := NewTMDB("")
	if p.Available() {
		t.Error("expected not available without key")
	}
	if _, err := p.SearchMovie(nil, "x"); err != ErrNotConfigured {
		t.Errorf("expected ErrNotConfigured, got %v", err)
	}
}
