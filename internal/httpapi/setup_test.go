package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Folders chosen in the app replace the environment's for the whole app; an empty or
// unset choice leaves the environment's folder alone.
func TestApplySavedLibraryDirs(t *testing.T) {
	cfg := config.Config{MoviesDir: "/media/library/movies", TVDir: "/media/library/tvshows", DownloadsDir: "/media/downloads"}
	saved := map[string]string{keyLibMovies: "/storage/media/movies", keyLibTV: "  ", keyLibDownloads: "/storage/torrents"}
	get := func(_ context.Context, key, def string) string {
		if v, ok := saved[key]; ok {
			return v
		}
		return def
	}
	ApplySavedLibraryDirs(context.Background(), get, &cfg, nil)
	if cfg.MoviesDir != "/storage/media/movies" || cfg.DownloadsDir != "/storage/torrents" {
		t.Errorf("chosen folders not applied: movies=%q downloads=%q", cfg.MoviesDir, cfg.DownloadsDir)
	}
	if cfg.TVDir != "/media/library/tvshows" {
		t.Errorf("blank choice must keep the environment's folder, got %q", cfg.TVDir)
	}
}

// The wizard pre-fills folders from what's actually on the mount, preferring the most
// specific name and the shallowest match.
func TestSuggestFolders(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"media/movies", "media/tvshows", "media/ebooks", "media/audiobooks", "torrents", "books-old", "downloads/films"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := suggestFolders([]string{root})
	want := map[string]string{
		"movies":     "media/movies",
		"tv":         "media/tvshows",
		"ebooks":     "media/ebooks",
		"audiobooks": "media/audiobooks",
		"downloads":  "torrents",
	}
	for lib, rel := range want {
		if got[lib] != filepath.ToSlash(filepath.Join(root, rel)) {
			t.Errorf("%s: got %q, want %s", lib, got[lib], rel)
		}
	}
	if _, ok := got["music"]; ok {
		t.Errorf("no music folder exists, but one was suggested: %q", got["music"])
	}
}

// A saved folder the running app isn't using yet needs a restart; music (read live), a
// blank choice (the install default) and an unchanged folder don't.
func TestFolderRestartState(t *testing.T) {
	cases := []struct {
		name    string
		saved   map[string]string
		changed []string
	}{
		{"nothing saved", nil, nil},
		{"saved equals running", map[string]string{keyLibMovies: "/lib/movies", keyLibDownloads: "/dl"}, nil},
		{"downloads differs", map[string]string{keyLibDownloads: "/storage/torrents"}, []string{"downloads"}},
		{"music differs", map[string]string{keyLibMusic: "/storage/music"}, nil},
		{"blank saved", map[string]string{keyLibTV: "  "}, nil},
		{"two differ", map[string]string{keyLibTV: "/storage/tv", keyLibEbooks: "/storage/books"}, []string{"tv", "ebooks"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := pathsAPI(t)
			ctx := context.Background()
			for k, v := range c.saved {
				if err := a.deps.Settings.Set(ctx, k, v); err != nil {
					t.Fatal(err)
				}
			}
			st := a.folderRestartState(ctx)
			if st.Needed != (len(c.changed) > 0) {
				t.Errorf("needed = %v, want %v", st.Needed, len(c.changed) > 0)
			}
			if len(st.Changed) != len(c.changed) {
				t.Fatalf("changed = %+v, want %v", st.Changed, c.changed)
			}
			for i, lib := range c.changed {
				if st.Changed[i].Library != lib {
					t.Errorf("changed[%d] = %+v, want %s", i, st.Changed[i], lib)
				}
			}
			if c.name == "downloads differs" && (st.Changed[0].Saved != "/storage/torrents" || st.Changed[0].Running != "/dl") {
				t.Errorf("old → new not reported: %+v", st.Changed[0])
			}
		})
	}
}

// Staff see what's waiting on a restart; requesters don't.
func TestPendingRestartRoles(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := New(Deps{
		Config:   config.Config{DownloadsDir: "/dl"},
		Settings: settings.NewService(st.DB()),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	for role, want := range map[auth.Role]int{
		auth.RoleReadonly:  http.StatusForbidden,
		auth.RoleRequester: http.StatusForbidden,
		auth.RoleManager:   http.StatusOK,
		auth.RoleAdmin:     http.StatusOK,
	} {
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, lanRequest("GET", "/api/v1/system/pending-restart", "", role))
		if rec.Code != want {
			t.Errorf("%s: HTTP %d, want %d", role, rec.Code, want)
		}
		if want == http.StatusOK && !strings.Contains(rec.Body.String(), `"busy"`) {
			t.Errorf("%s: no busy summary in %s", role, rec.Body.String())
		}
	}
}
