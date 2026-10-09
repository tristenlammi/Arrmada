package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// The detail endpoint's acquisition block states what the sweeps will really do: a
// scanned-in film isn't monitored or upgraded, a profile with upgrades off keeps its file,
// a recorded file that's gone reads as missing, and a film not out yet isn't available.
// Synthetic rows and temp files only.
func TestMovieAcquisitionFields(t *testing.T) {
	root := t.TempDir()
	s := newRouteServer(t, func(d *Deps) {
		d.Quality = quality.NewService(d.Store.DB())
		d.Movies = movies.NewService(d.Store.DB(), nil, nil, root, "", nil, d.Log)
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	ctx := context.Background()
	still, err := s.deps.Quality.Create(ctx, quality.StoredProfile{MediaType: quality.MediaMovie, Name: "Stays", UpgradesEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	stillRef := fmt.Sprintf("custom:%d", still.ID)
	file := func(name string) string {
		p := filepath.Join(root, name, name+".mkv")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	insert := func(tmdb int, profile string, monitored, hasFile int, path, extra string) int64 {
		res, err := s.st.DB().ExecContext(ctx, `INSERT INTO movies (tmdb_id, title, year, monitored, quality_profile, has_file, movie_file_path, extra_json)
			VALUES (?, 'Film', 2020, ?, ?, ?, ?, ?)`, tmdb, monitored, profile, hasFile, path, extra)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	future := time.Now().AddDate(1, 0, 0).Format("2006-01-02")
	scanned := insert(1, "n/a", 0, 1, file("Scanned"), "")
	noUpgrades := insert(2, stillRef, 1, 1, file("Stays"), "")
	missing := insert(3, stillRef, 1, 1, filepath.Join(root, "Gone", "Gone.mkv"), "")
	notOut := insert(4, stillRef, 1, 0, "", `{"release_date":"`+future+`"}`)

	get := func(id int64) movies.Acquisition {
		t.Helper()
		rec := s.do("GET", fmt.Sprintf("/api/v1/movies/%d", id), mgr)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET movie %d: HTTP %d: %s", id, rec.Code, rec.Body)
		}
		var m movies.Movie
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || m.Acquisition == nil {
			t.Fatalf("movie %d: no acquisition block (%v): %s", id, err, rec.Body)
		}
		return *m.Acquisition
	}

	if a := get(scanned); !a.ScannedIn || a.ProfileKnown || a.Monitored || a.UpgradesAllowed || a.FileMissing {
		t.Errorf("scanned-in film: %+v", a)
	}
	if a := get(noUpgrades); a.ScannedIn || !a.ProfileKnown || !a.Monitored || a.UpgradesAllowed || a.FileMissing {
		t.Errorf("monitored on a no-upgrade profile: %+v", a)
	}
	if a := get(missing); !a.FileMissing {
		t.Errorf("recorded file gone from disk: %+v", a)
	}
	if a := get(notOut); a.Available || a.AvailableFrom != future || a.Downloading {
		t.Errorf("not yet released: %+v", a)
	}
}
