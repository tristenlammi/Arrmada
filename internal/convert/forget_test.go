package convert

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/movies"
)

func movieRows(t *testing.T, s *Service, movieID int64) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT path FROM convert_library WHERE media_type = 'movie' AND movie_id = ? ORDER BY path`, movieID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// ForgetMovie drops a movie's Convert rows and nobody else's.
func TestForgetMovieRemovesRows(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	for _, r := range []indexRow{
		{Path: "/lib/Heat (1995)/Heat.mkv", MediaType: "movie", MovieID: 1, Title: "Heat"},
		{Path: "/lib/Heat (1995)/Heat.old.mkv", MediaType: "movie", MovieID: 1, Title: "Heat"},
		{Path: "/lib/Ran (1985)/Ran.mkv", MediaType: "movie", MovieID: 2, Title: "Ran"},
	} {
		if err := s.index.upsert(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	gen := s.index.gen.Load()
	if err := s.ForgetMovie(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got := movieRows(t, s, 1); len(got) != 0 {
		t.Fatalf("rows left for the forgotten movie: %v", got)
	}
	if got := movieRows(t, s, 2); len(got) != 1 {
		t.Fatalf("another movie's row was touched: %v", got)
	}
	if s.index.gen.Load() == gen {
		t.Fatal("the cached list views weren't told the index changed")
	}
}

// IndexMovie follows the movie's record: a rename replaces the old path's row, and a
// movie whose file was deleted — or that was deleted itself — drops out instead of
// lingering until the nightly sweep. Running it again changes nothing.
func TestIndexMovieFollowsFileChanges(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	s.movies = movies.NewService(s.db, nil, nil, t.TempDir(), "", nil, s.log)
	if _, err := s.db.Exec(`INSERT INTO movies (id, tmdb_id, title, monitored, has_file, movie_file_path)
		VALUES (5, 500, 'Heat', 1, 1, '/lib/Heat/old.mkv')`); err != nil {
		t.Fatal(err)
	}
	if err := s.IndexMovie(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if got := movieRows(t, s, 5); len(got) != 1 || got[0] != "/lib/Heat/old.mkv" {
		t.Fatalf("after import: %v", got)
	}

	// Renamed.
	if _, err := s.db.Exec(`UPDATE movies SET movie_file_path = '/lib/Heat (1995)/Heat (1995).mkv' WHERE id = 5`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.IndexMovie(ctx, 5); err != nil {
			t.Fatal(err)
		}
	}
	if got := movieRows(t, s, 5); len(got) != 1 || got[0] != "/lib/Heat (1995)/Heat (1995).mkv" {
		t.Fatalf("after rename: %v", got)
	}

	// File deleted.
	if _, err := s.db.Exec(`UPDATE movies SET has_file = 0, movie_file_path = '' WHERE id = 5`); err != nil {
		t.Fatal(err)
	}
	if err := s.IndexMovie(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if got := movieRows(t, s, 5); len(got) != 0 {
		t.Fatalf("a movie with no file is still listed: %v", got)
	}

	// Movie deleted: not an error, and nothing listed.
	if err := s.index.upsert(ctx, indexRow{Path: "/lib/x.mkv", MediaType: "movie", MovieID: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM movies WHERE id = 5`); err != nil {
		t.Fatal(err)
	}
	if err := s.IndexMovie(ctx, 5); err != nil {
		t.Fatalf("a deleted movie should be forgotten, not an error: %v", err)
	}
	if got := movieRows(t, s, 5); len(got) != 0 {
		t.Fatalf("a deleted movie is still listed: %v", got)
	}
}
