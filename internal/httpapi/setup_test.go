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
	"github.com/tristenlammi/arrmada/internal/libroots"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The startup log names each folder chosen in the app over the environment's, leaves a
// blank choice alone, shouts about one inside the data folder, and changes nothing.
func TestLogLibraryDirs(t *testing.T) {
	cfg := config.Config{MoviesDir: "/media/library/movies", TVDir: "/media/library/tvshows", DownloadsDir: "/media/downloads", DataDir: "/appdata"}
	saved := map[string]string{keyLibMovies: "/storage/media/movies", keyLibTV: "  ", keyLibMusic: "/appdata/music"}
	get := func(_ context.Context, key, def string) string {
		if v, ok := saved[key]; ok {
			return v
		}
		return def
	}
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	before := cfg
	LogLibraryDirs(context.Background(), libroots.New(get, cfg), cfg, log)
	out := buf.String()
	if !strings.Contains(out, "folder=/storage/media/movies") {
		t.Errorf("the chosen movies folder isn't logged:\n%s", out)
	}
	if strings.Contains(out, "library=tv ") {
		t.Errorf("a blank tv choice must not be logged as chosen:\n%s", out)
	}
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "folder=/appdata/music") {
		t.Errorf("a folder inside the data folder must be an error line:\n%s", out)
	}
	if cfg != before {
		t.Error("LogLibraryDirs must not change the config")
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

// Folders apply live now, so no folder change ever waits on a restart.
func TestFolderRestartState(t *testing.T) {
	for name, saved := range map[string]map[string]string{
		"nothing saved":     nil,
		"downloads differs": {keyLibDownloads: "/storage/torrents"},
		"music differs":     {keyLibMusic: "/storage/music"},
		"blank saved":       {keyLibTV: "  "},
		"two differ":        {keyLibTV: "/storage/tv", keyLibEbooks: "/storage/books"},
	} {
		t.Run(name, func(t *testing.T) {
			a := pathsAPI(t)
			ctx := context.Background()
			for k, v := range saved {
				if err := a.deps.Settings.Set(ctx, k, v); err != nil {
					t.Fatal(err)
				}
			}
			st := a.folderRestartState(ctx)
			if st.Needed || len(st.Changed) != 0 || st.Changed == nil {
				t.Errorf("state = %+v, want nothing pending (and an empty list, not null)", st)
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
