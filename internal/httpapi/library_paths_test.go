package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/tristenlammi/arrmada/internal/diskspace"
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

	// Saved folders must exist now, so the "share" is a temp dir.
	storage := t.TempDir()
	music, movies := filepath.Join(storage, "media", "music"), filepath.Join(storage, "media", "movies")
	for _, d := range []string{music, movies} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w := putPaths(t, a, map[string]any{"music": music, "movies": movies})
	if w.Code != http.StatusOK {
		t.Fatalf("save returned %d: %s", w.Code, w.Body.String())
	}
	// The save response itself must already reflect the new values.
	var saved map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &saved)
	if saved["music"] != music {
		t.Errorf("save response music = %q", saved["music"])
	}

	// And a fresh read must return them, not the defaults.
	got := getPaths(t, a)
	if got["music"] != music {
		t.Errorf("music did not persist: %q", got["music"])
	}
	if got["movies"] != movies {
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

// A mistyped folder is refused with a reason the UI can offer to fix; a file is refused
// outright. Nothing is saved either way.
func TestSetLibraryPathsRejectsMissing(t *testing.T) {
	a, base := folderTestAPI(t)
	before := getPaths(t, a)
	typo := filepath.Join(base, "media", "movise")

	w := putPaths(t, a, map[string]any{"movies": typo})
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusBadRequest || body["folder"] != "movies" || body["missing"] != true {
		t.Fatalf("missing folder: HTTP %d %+v", w.Code, body)
	}
	if _, err := os.Stat(typo); !os.IsNotExist(err) {
		t.Errorf("a refused save created the folder: %v", err)
	}

	file := filepath.Join(base, "media", "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := putPaths(t, a, map[string]any{"tv": file}); w.Code != http.StatusBadRequest {
		t.Errorf("a file saved as a folder: HTTP %d", w.Code)
	}
	if w := putPaths(t, a, map[string]any{"tv": "media/tv"}); w.Code != http.StatusBadRequest {
		t.Errorf("a relative path saved: HTTP %d", w.Code)
	}
	if got := getPaths(t, a); got["movies"] != before["movies"] || got["tv"] != before["tv"] {
		t.Errorf("refused saves wrote something: %+v", got)
	}

	// Blank still means "use the install default".
	if w := putPaths(t, a, map[string]any{"ebooks": ""}); w.Code != http.StatusOK {
		t.Errorf("blank refused: HTTP %d %s", w.Code, w.Body.String())
	}
}

// "Create it" makes the folder and saves it in one go.
func TestSetLibraryPathsCreate(t *testing.T) {
	a, base := folderTestAPI(t)
	p := filepath.Join(base, "media", "tv")
	w := putPaths(t, a, map[string]any{"tv": p, "create": true})
	if w.Code != http.StatusOK {
		t.Fatalf("create: HTTP %d %s", w.Code, w.Body.String())
	}
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		t.Errorf("folder not created: %v", err)
	}
	if got := getPaths(t, a); got["tv"] != p {
		t.Errorf("tv = %q, want %q", got["tv"], p)
	}
	// Create never reaches into the data folder.
	inData := filepath.Join(a.deps.Config.DataDir, "tv")
	if w := putPaths(t, a, map[string]any{"tv": inData, "create": true}); w.Code != http.StatusBadRequest {
		t.Errorf("create inside the data dir: HTTP %d", w.Code)
	}
	if _, err := os.Stat(inData); !os.IsNotExist(err) {
		t.Errorf("a folder was created inside the data dir: %v", err)
	}
}

// Saving one folder never fails because a different, untouched folder is odd — here a
// Music folder whose share isn't mounted right now.
func TestSetLibraryPathsIgnoresUntouchedOddFolder(t *testing.T) {
	a, base := folderTestAPI(t)
	a.deps.Config.MusicDir = filepath.Join(base, "unmounted", "music")
	all := getPaths(t, a)
	all["movies"] = filepath.Join(base, "media", "movies")
	body := map[string]any{}
	for k, v := range all {
		body[k] = v
	}
	if w := putPaths(t, a, body); w.Code != http.StatusOK {
		t.Errorf("save blocked by an untouched folder: HTTP %d %s", w.Code, w.Body.String())
	}
}

// The picker walks through "/" and the data folder's parent, but can't select them.
func TestBrowseMarksDataDirDisabled(t *testing.T) {
	a, base := folderTestAPI(t)
	browse := func(p string) map[string]any {
		w := httptest.NewRecorder()
		a.handleBrowse(w, httptest.NewRequest(http.MethodGet, "/?path="+url.QueryEscape(p), nil))
		var got map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		return got
	}
	if got := browse(base); got["path_disabled"] != true {
		t.Errorf("the data folder's parent is selectable: %+v", got)
	}
	if got := browse(filepath.Join(base, "media")); got["path_disabled"] != false {
		t.Errorf("an ordinary folder is not selectable: %+v", got)
	}
}

// The live check reports the facts behind the chips, and the save-time refusal in the
// same words.
func TestCheckLibraryFolder(t *testing.T) {
	a, base := folderTestAPI(t)
	check := func(q string) map[string]any {
		w := httptest.NewRecorder()
		a.handleCheckLibraryFolder(w, httptest.NewRequest(http.MethodGet, "/?"+q, nil))
		var got map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		got["_code"] = w.Code
		return got
	}
	movies := filepath.Join(base, "media", "movies")
	got := check("kind=movies&path=" + url.QueryEscape(movies))
	if got["exists"] != true || got["writable"] != true || got["hardlink_with_downloads"] != true || got["error"] != nil {
		t.Errorf("good folder: %+v", got)
	}
	got = check("kind=movies&path=" + url.QueryEscape(filepath.Join(base, "media", "nope")))
	if got["exists"] != false || got["error"] == nil {
		t.Errorf("missing folder: %+v", got)
	}
	got = check("kind=movies&path=" + url.QueryEscape(a.deps.Config.DataDir))
	if got["under_data_dir"] != true || !strings.Contains(fmt.Sprint(got["error"]), "data folder") {
		t.Errorf("data folder: %+v", got)
	}
	got = check("kind=downloads&path=" + url.QueryEscape(a.deps.Config.DownloadsDir))
	if got["hardlink_with_downloads"] != nil {
		t.Errorf("downloads linked against itself: %+v", got)
	}
	if got := check("kind=nonsense&path=/x"); got["_code"] != http.StatusBadRequest {
		t.Errorf("unknown kind: %+v", got)
	}
}

// The health panel judges the folders the user picked, one by one, and never creates a
// missing one on the way.
func TestSystemHealthChecksEachLibraryFolder(t *testing.T) {
	a, base := folderTestAPI(t)
	healthAPI(t, a)
	ctx := context.Background()
	// The user picked TV on the media share; it isn't there yet but its parent is.
	tv := filepath.Join(base, "media", "tv")
	if err := a.deps.Settings.Set(ctx, keyLibTV, tv); err != nil {
		t.Fatal(err)
	}
	// Movies picked on a share that isn't mounted at all.
	movies := filepath.Join(base, "unmounted", "movies")
	if err := a.deps.Settings.Set(ctx, keyLibMovies, movies); err != nil {
		t.Fatal(err)
	}
	// ARRMADA_LIBRARY_DIR is gone; it must not be what's judged (or created).
	a.deps.Config.LibraryDir = filepath.Join(base, "managed-volume")

	var tvWarn, moviesErr *healthWarning
	warns := healthWarnings(t, a)
	for i, w := range warns {
		switch {
		case strings.Contains(w.Message, "TV folder "+tv):
			tvWarn = &warns[i]
		case strings.Contains(w.Message, "Movies folder "+movies):
			moviesErr = &warns[i]
		case strings.Contains(w.Message, "library folder"):
			t.Errorf("the managed library dir was judged: %s", w.Message)
		}
	}
	if tvWarn == nil || tvWarn.Level != "warning" || !strings.Contains(tvWarn.Message, "doesn't exist yet") {
		t.Errorf("missing TV folder with its parent present: %+v", tvWarn)
	}
	if moviesErr == nil || moviesErr.Level != "error" || !strings.Contains(moviesErr.Message, "isn't mounted") {
		t.Errorf("missing Movies share: %+v", moviesErr)
	}
	for _, p := range []string{tv, filepath.Dir(movies), a.deps.Config.LibraryDir} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("the health check created %s", p)
		}
	}
	// Books is on by default but its folders aren't there; Music is off, so its folder
	// isn't judged at all.
	for _, w := range warns {
		if strings.Contains(w.Message, "Music folder") {
			t.Errorf("music is off but was checked: %s", w.Message)
		}
	}
}

// The disk guard panel names each library folder on the downloads drive.
func TestDiskGuardSharedWithReportsRoots(t *testing.T) {
	a, _ := folderTestAPI(t)
	if _, ok := diskspace.Device(a.deps.Config.DownloadsDir); !ok {
		t.Skip("filesystem ids can't be read on this platform")
	}
	healthAPI(t, a)
	a.deps.DiskGuard = download.NewDiskGuard(a.deps.Downloads, a.deps.Settings, a.deps.Log, a.deps.Config.DownloadsDir)

	w := httptest.NewRecorder()
	a.handleDiskGuardStatus(w, httptest.NewRequest(http.MethodGet, "/", nil))
	var got struct {
		SharedWith []struct {
			Role, Label, Path string
		} `json:"shared_with"`
		SharedWithLibrary bool `json:"shared_with_library"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding %q: %v", w.Body.String(), err)
	}
	roles := map[string]bool{}
	for _, f := range got.SharedWith {
		roles[f.Role] = true
	}
	if !roles["movies"] || !roles["tv"] || roles["downloads"] || !got.SharedWithLibrary {
		t.Errorf("all temp dirs share one filesystem; got %+v", got)
	}
}
