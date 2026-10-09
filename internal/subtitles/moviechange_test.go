package subtitles

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// changeFixture is a queue over a real movies module, with a finished library pass that
// lists movies 1 and 2.
func changeFixture(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := queueFixture()
	s.movies = movies.NewService(st.DB(), nil, nil, t.TempDir(), "", nil, s.log)
	s.settings = settings.NewService(st.DB())
	s.probe = func(context.Context, string) (*mediaInfo, error) { return nil, errors.New("no ffprobe in tests") }
	for _, q := range []string{
		`INSERT INTO movies (id, tmdb_id, title, monitored, has_file, movie_file_path) VALUES (1, 101, 'Heat', 1, 1, '/lib/Heat/Heat.mkv')`,
		`INSERT INTO movies (id, tmdb_id, title, monitored, has_file, movie_file_path) VALUES (2, 102, 'Ran', 1, 1, '/lib/Ran/ran.mkv')`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s.snap.movies = []FileSubs{
		{Kind: "movie", MovieID: 1, Title: "Heat", Path: "/lib/Heat/Heat.mkv"},
		{Kind: "movie", MovieID: 2, Title: "Ran", Path: "/lib/Ran/ran.mkv"},
	}
	s.snap.at = time.Now()
	return s, st
}

func snapshotPath(s *Service, movieID int64) (string, bool) {
	list, _ := s.SnapshotMovies()
	for _, fs := range list {
		if fs.MovieID == movieID {
			return fs.Path, true
		}
	}
	return "", false
}

// A movie whose file was deleted leaves the Library page and the queue at once; the
// other movie is untouched, and the movie can be queued again later.
func TestOnMovieChangedForgetsDeletedFile(t *testing.T) {
	s, st := changeFixture(t)
	ctx := context.Background()
	job := s.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "Heat", Priority: PrioSweep})
	if _, err := st.DB().Exec(`UPDATE movies SET has_file = 0, movie_file_path = '' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := s.OnMovieChanged(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshotPath(s, 1); ok {
		t.Fatal("a movie with no file is still on the Library page")
	}
	if _, ok := snapshotPath(s, 2); !ok {
		t.Fatal("another movie was dropped")
	}
	if job.State != StateCancelled || s.Pending() != 0 {
		t.Fatalf("queued job state %q, pending %d; want it dropped", job.State, s.Pending())
	}
	if again := s.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "Heat"}); again == job {
		t.Fatal("the dropped job still blocks a new one for the movie")
	}
	// Running it again for the same state is harmless.
	if err := s.OnMovieChanged(ctx, 1); err != nil {
		t.Fatal(err)
	}
}

// A deleted movie is forgotten too, without an error the outbox would retry.
func TestOnMovieChangedForgetsDeletedMovie(t *testing.T) {
	s, st := changeFixture(t)
	if _, err := st.DB().Exec(`DELETE FROM movies WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if err := s.OnMovieChanged(context.Background(), 2); err != nil {
		t.Fatalf("a deleted movie should be done, got %v", err)
	}
	if _, ok := snapshotPath(s, 2); ok {
		t.Fatal("a deleted movie is still on the Library page")
	}
}

// A renamed movie's entry follows it to the new path without waiting for the next pass.
func TestOnMovieChangedFollowsRename(t *testing.T) {
	s, st := changeFixture(t)
	if _, err := st.DB().Exec(`UPDATE movies SET movie_file_path = '/lib/Ran (1985)/Ran (1985).mkv' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if err := s.OnMovieChanged(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if p, _ := snapshotPath(s, 2); p != "/lib/Ran (1985)/Ran (1985).mkv" {
		t.Fatalf("snapshot path = %q, want the new one", p)
	}
	if s.Pending() != 0 {
		t.Fatal("a rename queued a subtitle job; the sidecars moved with the file")
	}
}
