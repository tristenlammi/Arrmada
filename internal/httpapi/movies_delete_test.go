package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// movieDeleteServer has one movie with one synthetic file in a temp library.
func movieDeleteServer(t *testing.T, bin string) (*routeServer, *http.Cookie, int64, string) {
	t.Helper()
	root := t.TempDir()
	s := newRouteServer(t, func(d *Deps) {
		d.Bus = eventbus.New(d.Log)
		d.Movies = movies.NewService(d.Store.DB(), nil, nil, root, bin, d.Bus, d.Log)
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	res, err := s.st.DB().Exec(`INSERT INTO movies (tmdb_id, title, year, monitored) VALUES (11, 'Heat', 1995, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	video := filepath.Join(root, "Heat (1995)", "Heat (1995).mkv")
	if err := os.MkdirAll(filepath.Dir(video), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(video, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.DB().Exec(`UPDATE movies SET has_file = 1, movie_file_path = ? WHERE id = ?`, video, id); err != nil {
		t.Fatal(err)
	}
	return s, mgr, id, video
}

// A movie delete the recycle bin refuses answers 409 with a message saying why, and the
// movie and its file are still there.
func TestDeleteMovieReturns409OnRecycleFailure(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(bin, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, mgr, id, video := movieDeleteServer(t, bin)

	rec := s.do("DELETE", fmt.Sprintf("/api/v1/movies/%d?delete_files=true", id), mgr)
	if rec.Code != http.StatusConflict {
		t.Fatalf("HTTP %d, want 409: %s", rec.Code, rec.Body)
	}
	var body struct {
		Message string   `json:"message"`
		Failed  []string `json:"failed"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Message == "" || len(body.Failed) != 1 {
		t.Errorf("409 body should explain and name the file: %s", rec.Body)
	}
	if _, err := os.Stat(video); err != nil {
		t.Error("the file was deleted although the bin refused it")
	}
	if _, err := s.deps.Movies.Get(context.Background(), id); err != nil {
		t.Errorf("the movie is gone: %v", err)
	}

	// The single-file delete answers the same way.
	if rec := s.do("DELETE", fmt.Sprintf("/api/v1/movies/%d/file", id), mgr); rec.Code != http.StatusConflict {
		t.Errorf("file delete: HTTP %d, want 409", rec.Code)
	}
	if rec := s.do("DELETE", fmt.Sprintf("/api/v1/movies/%d/versions/0/file", id), mgr); rec.Code != http.StatusConflict {
		t.Errorf("version file delete: HTTP %d, want 409", rec.Code)
	}
}
