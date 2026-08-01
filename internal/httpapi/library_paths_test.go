package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/config"
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
