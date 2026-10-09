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

// The preview names each file with its size, counts the subtitles that go with them, and
// says where they'd go — the bin's path, or that the bin is off.
func TestMovieDeletePreview(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	s, mgr, id, video := movieDeleteServer(t, bin)
	if err := os.WriteFile(video[:len(video)-len(".mkv")]+".en.srt", []byte("subs"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := s.do("GET", fmt.Sprintf("/api/v1/movies/%d/delete-preview", id), mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Versions []struct {
			ID        int64  `json:"id"`
			Label     string `json:"label"`
			FileName  string `json:"file_name"`
			SizeBytes int64  `json:"size_bytes"`
		} `json:"versions"`
		Sidecars int   `json:"sidecars"`
		Bytes    int64 `json:"bytes"`
		Recycle  struct {
			Enabled bool `json:"enabled"`
		} `json:"recycle"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Versions) != 1 || got.Versions[0].FileName != "Heat (1995).mkv" || got.Versions[0].SizeBytes != 10 {
		t.Errorf("versions = %+v", got.Versions)
	}
	if got.Sidecars != 1 || got.Bytes != 14 {
		t.Errorf("sidecars = %d, bytes = %d; want 1 and 14", got.Sidecars, got.Bytes)
	}
	if !got.Recycle.Enabled {
		t.Error("the route server's bin is on; the preview should say so")
	}
	if rec := s.do("GET", "/api/v1/movies/999/delete-preview", mgr); rec.Code != http.StatusNotFound {
		t.Errorf("unknown movie: HTTP %d, want 404", rec.Code)
	}
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
