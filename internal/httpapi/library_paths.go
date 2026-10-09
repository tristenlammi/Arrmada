package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/libroots"
)

// Settings keys for the in-app library folder config. Each falls back to the env-configured
// default (config.*Dir) when unset, so the app works before the user picks anything.
const (
	keyLibMovies     = "lib_movies_dir"
	keyLibTV         = "lib_tv_dir"
	keyLibEbooks     = "lib_ebooks_dir"
	keyLibAudiobooks = "lib_audiobooks_dir"
	keyLibMusic      = "lib_music_dir"
	keyLibDownloads  = "lib_downloads_dir"
)

// libMovies etc. return the effective (settings-or-default) path for each library.
func (a *api) libMovies(r *http.Request) string {
	return a.deps.Settings.Get(r.Context(), keyLibMovies, a.deps.Config.MoviesDir)
}
func (a *api) libTV(r *http.Request) string {
	return a.deps.Settings.Get(r.Context(), keyLibTV, a.deps.Config.TVDir)
}
func (a *api) libEbooks(r *http.Request) string {
	return a.deps.Settings.Get(r.Context(), keyLibEbooks, a.deps.Config.EbooksDir)
}
func (a *api) libAudiobooks(r *http.Request) string {
	return a.deps.Settings.Get(r.Context(), keyLibAudiobooks, a.deps.Config.AudiobooksDir)
}
func (a *api) libMusic(r *http.Request) string {
	return a.deps.Settings.Get(r.Context(), keyLibMusic, a.deps.Config.MusicDir)
}
func (a *api) libDownloads(r *http.Request) string {
	return a.deps.Settings.Get(r.Context(), keyLibDownloads, a.deps.Config.DownloadsDir)
}

// handleGetLibraryPaths returns the configured folder for each library.
func (a *api) handleGetLibraryPaths(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, map[string]any{
		"movies":     a.libMovies(r),
		"tv":         a.libTV(r),
		"ebooks":     a.libEbooks(r),
		"audiobooks": a.libAudiobooks(r),
		"music":      a.libMusic(r),
		"downloads":  a.libDownloads(r),
	})
}

// pickedConfig is the running config with each library folder replaced by the one saved
// in the app — the folders the user picked. The running config only learns a new pick at
// the next start, so anything that judges the folders (health, the disk guard panel)
// reads this instead.
func (a *api) pickedConfig(ctx context.Context) config.Config {
	c := a.deps.Config
	if a.deps.Settings != nil {
		ApplySavedLibraryDirs(ctx, a.deps.Settings.Get, &c, nil)
	}
	return c
}

// folderLabel is how the UI names each library folder, for messages.
var folderLabel = map[string]string{
	"movies": "Movies", "tv": "TV", "ebooks": "Ebooks", "audiobooks": "Audiobooks",
	"music": "Music", "downloads": "Downloads",
}

// dataDirConflict refuses a media folder in, at or above Arrmada's data folder. The
// folder settings, the picker, the health panel and manual import all draw that line
// through libroots, so they can't disagree.
func (a *api) dataDirConflict(label, p string) error {
	if strings.TrimSpace(p) == "" || !libroots.UnderDataDir(p, a.deps.Config.DataDir) {
		return nil
	}
	return fmt.Errorf("%s folder can't be inside or contain Arrmada's data folder (%s). Media must live on its own mount, e.g. /storage or /media.",
		label, a.dataDirShown())
}

// dataDirShown is the data folder as the user knows it: absolute, forward slashes.
func (a *api) dataDirShown() string {
	d := a.deps.Config.DataDir
	if abs, err := filepath.Abs(d); err == nil {
		d = abs
	}
	return filepath.ToSlash(d)
}

// writeFolderError is a 400 naming the folder it's about, so the UI can mark that row.
func (a *api) writeFolderError(w http.ResponseWriter, folder, message string) {
	a.writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": message, "folder": folder})
}

// handleSetLibraryPaths saves whichever folders were provided (nil = leave unchanged).
// Every provided folder is checked before any is written, so one bad field never leaves
// a half-saved set behind.
func (a *api) handleSetLibraryPaths(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Movies     *string `json:"movies"`
		TV         *string `json:"tv"`
		Ebooks     *string `json:"ebooks"`
		Audiobooks *string `json:"audiobooks"`
		Music      *string `json:"music"`
		Downloads  *string `json:"downloads"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	folders := []struct {
		name  string
		key   string
		value *string
	}{
		{"movies", keyLibMovies, req.Movies},
		{"tv", keyLibTV, req.TV},
		{"ebooks", keyLibEbooks, req.Ebooks},
		{"audiobooks", keyLibAudiobooks, req.Audiobooks},
		{"music", keyLibMusic, req.Music},
		{"downloads", keyLibDownloads, req.Downloads},
	}
	for _, f := range folders {
		if f.value == nil {
			continue
		}
		*f.value = strings.TrimSpace(*f.value)
		// Blank means "use the install default", which is the environment's business.
		if err := a.dataDirConflict(folderLabel[f.name], *f.value); err != nil {
			a.writeFolderError(w, f.name, err.Error())
			return
		}
	}
	ctx := r.Context()
	for _, f := range folders {
		if f.value == nil {
			continue
		}
		if err := a.deps.Settings.Set(ctx, f.key, *f.value); err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not save library paths")
			return
		}
	}
	a.handleGetLibraryPaths(w, r)
}

// handleBrowse lists the sub-directories of a path, for the in-app folder picker. Admin-only,
// read-only directory listing (no file contents). Arrmada's data folder is never offered.
func (a *api) handleBrowse(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSpace(r.URL.Query().Get("path"))
	if p == "" {
		// Start somewhere useful — the mounted media root if it exists, else the FS root.
		// Never /data: that's the database, and opening there invites the one mistake
		// the folder rules exist to stop.
		p = "/"
		for _, cand := range []string{"/storage", "/media"} {
			if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
				p = cand
				break
			}
		}
	}
	p = filepath.Clean("/" + strings.TrimPrefix(filepath.ToSlash(p), "/"))

	entries, err := os.ReadDir(p)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "cannot open "+p+": "+err.Error())
		return
	}
	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	dirs := []entry{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.ToSlash(filepath.Join(p, e.Name()))
		if libroots.InDataDir(full, a.deps.Config.DataDir) {
			continue
		}
		dirs = append(dirs, entry{Name: e.Name(), Path: full})
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })

	parent := filepath.ToSlash(filepath.Dir(p))
	a.writeJSON(w, http.StatusOK, map[string]any{"path": p, "parent": parent, "dirs": dirs})
}
