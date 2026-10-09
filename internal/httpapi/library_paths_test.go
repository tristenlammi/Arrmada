package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

func pathsAPI(t *testing.T) *api {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &api{deps: Deps{
		Settings: settings.NewService(st.DB()),
		Config: config.Config{
			LibraryDir: "/lib", MoviesDir: "/lib/movies", TVDir: "/lib/tvshows",
			EbooksDir: "/lib/ebooks", AudiobooksDir: "/lib/audiobooks",
			MusicDir: "/lib/music", DownloadsDir: "/dl",
		},
	}}
}

func getPaths(t *testing.T, a *api) map[string]string {
	t.Helper()
	w := httptest.NewRecorder()
	a.handleGetLibraryPaths(w, httptest.NewRequest(http.MethodGet, "/", nil))
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding %q: %v", w.Body.String(), err)
	}
	return got
}

// Saving a folder must actually persist and come back — including music, which was added
// last and is the one place a missing field would go unnoticed.
func TestLibraryPathsRoundTrip(t *testing.T) {
	a := pathsAPI(t)

	// Unset values fall back to the config defaults.
	if got := getPaths(t, a); got["music"] != "/lib/music" || got["movies"] != "/lib/movies" {
		t.Fatalf("defaults not returned: %+v", got)
	}

	body := `{"music":"/storage/media/music","movies":"/storage/media/movies"}`
	w := httptest.NewRecorder()
	a.handleSetLibraryPaths(w, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("save returned %d: %s", w.Code, w.Body.String())
	}
	// The save response itself must already reflect the new values.
	var saved map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &saved)
	if saved["music"] != "/storage/media/music" {
		t.Errorf("save response music = %q", saved["music"])
	}

	// And a fresh read must return them, not the defaults.
	got := getPaths(t, a)
	if got["music"] != "/storage/media/music" {
		t.Errorf("music did not persist: %q", got["music"])
	}
	if got["movies"] != "/storage/media/movies" {
		t.Errorf("movies did not persist: %q", got["movies"])
	}
	// A field omitted from the request is left alone, not blanked.
	if got["tv"] != "/lib/tvshows" {
		t.Errorf("omitted field was overwritten: tv = %q", got["tv"])
	}
}

// folderTestAPI is pathsAPI with every folder in a temp dir, beside a temp data dir —
// nothing real is touched.
func folderTestAPI(t *testing.T) (a *api, base string) {
	t.Helper()
	base = t.TempDir()
	a = pathsAPI(t)
	c := &a.deps.Config
	c.DataDir = filepath.Join(base, "data")
	c.LibraryDir = filepath.Join(base, "library")
	c.MoviesDir = filepath.Join(c.LibraryDir, "movies")
	c.TVDir = filepath.Join(c.LibraryDir, "tvshows")
	c.EbooksDir = filepath.Join(c.LibraryDir, "ebooks")
	c.AudiobooksDir = filepath.Join(c.LibraryDir, "audiobooks")
	c.MusicDir = filepath.Join(c.LibraryDir, "music")
	c.DownloadsDir = filepath.Join(base, "downloads")
	for _, d := range []string{c.DataDir, c.MoviesDir, c.TVDir, c.DownloadsDir, filepath.Join(base, "media", "movies")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	a.deps.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return a, base
}

func putPaths(t *testing.T, a *api, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	a.handleSetLibraryPaths(w, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(string(raw))))
	return w
}

// Media never goes in, at or above the data folder, from the wizard or Settings alike.
func TestSetLibraryPathsRefusesDataDir(t *testing.T) {
	a, base := folderTestAPI(t)
	data := a.deps.Config.DataDir
	before := getPaths(t, a)

	refused := map[string]string{
		"the data dir":      data,
		"a child":           filepath.Join(data, "movies"),
		"an ancestor":       base,
		"the root":          string(filepath.Separator),
		"the container dir": "/data/movies",
	}
	for name, p := range refused {
		w := putPaths(t, a, map[string]any{"movies": p})
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s (%s): HTTP %d, want 400", name, p, w.Code)
			continue
		}
		var body map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if body["folder"] != "movies" || !strings.Contains(body["message"], "Movies folder can't be inside or contain Arrmada's data folder") {
			t.Errorf("%s: unhelpful refusal %+v", name, body)
		}
	}

	// A partial body with one bad field saves nothing, not even the good one.
	w := putPaths(t, a, map[string]any{"movies": filepath.Join(base, "media", "movies"), "tv": filepath.Join(data, "tv")})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("mixed body: HTTP %d, want 400", w.Code)
	}
	if got := getPaths(t, a); got["movies"] != before["movies"] || got["tv"] != before["tv"] {
		t.Errorf("a refused save wrote something: %+v", got)
	}

	// A sibling of the data dir is fine.
	if w := putPaths(t, a, map[string]any{"movies": filepath.Join(base, "media", "movies")}); w.Code != http.StatusOK {
		t.Errorf("sibling refused: HTTP %d %s", w.Code, w.Body.String())
	}
}

// The picker never offers the data folder.
func TestBrowseSkipsDataDir(t *testing.T) {
	a, base := folderTestAPI(t)
	w := httptest.NewRecorder()
	a.handleBrowse(w, httptest.NewRequest(http.MethodGet, "/?path="+url.QueryEscape(base), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("browse: HTTP %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Dirs []struct{ Name string } `json:"dirs"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	names := map[string]bool{}
	for _, d := range got.Dirs {
		names[d.Name] = true
	}
	if names["data"] {
		t.Errorf("the data folder was listed: %+v", got.Dirs)
	}
	if !names["library"] || !names["media"] || !names["downloads"] {
		t.Errorf("ordinary folders missing: %+v", got.Dirs)
	}
}

// healthAPI wires the bits handleSystemHealth needs onto a folder test API.
func healthAPI(t *testing.T, a *api) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a.deps.Indexers = indexer.NewService(st.DB(), a.deps.Log, "")
	a.deps.Downloads = download.NewService(st.DB(), a.deps.Log)
}

func healthWarnings(t *testing.T, a *api) []healthWarning {
	t.Helper()
	w := httptest.NewRecorder()
	a.handleSystemHealth(w, httptest.NewRequest(http.MethodGet, "/", nil))
	var got struct {
		Warnings []healthWarning `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding %q: %v", w.Body.String(), err)
	}
	return got.Warnings
}

// An install whose environment already points a library into the data folder keeps
// running, but the dashboard says so in red.
func TestSystemHealthFlagsLibraryUnderDataDir(t *testing.T) {
	a, _ := folderTestAPI(t)
	healthAPI(t, a)
	a.deps.Config.TVDir = filepath.Join(a.deps.Config.DataDir, "tv")
	found := false
	for _, wrn := range healthWarnings(t, a) {
		if strings.Contains(wrn.Message, "TV folder") && strings.Contains(wrn.Message, "data folder") {
			found = true
			if wrn.Level != "error" {
				t.Errorf("level %q, want error", wrn.Level)
			}
		}
		if strings.Contains(wrn.Message, "Movies folder") && strings.Contains(wrn.Message, "data folder") {
			t.Errorf("a folder beside the data dir was flagged: %s", wrn.Message)
		}
	}
	if !found {
		t.Error("no warning for a TV folder inside the data dir")
	}
}
