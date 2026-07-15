package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Settings keys for the in-app library folder config. Each falls back to the env-configured
// default (config.*Dir) when unset, so the app works before the user picks anything.
const (
	keyLibMovies     = "lib_movies_dir"
	keyLibTV         = "lib_tv_dir"
	keyLibEbooks     = "lib_ebooks_dir"
	keyLibAudiobooks = "lib_audiobooks_dir"
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
		"downloads":  a.libDownloads(r),
	})
}

// handleSetLibraryPaths saves whichever folders were provided (nil = leave unchanged).
func (a *api) handleSetLibraryPaths(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Movies     *string `json:"movies"`
		TV         *string `json:"tv"`
		Ebooks     *string `json:"ebooks"`
		Audiobooks *string `json:"audiobooks"`
		Downloads  *string `json:"downloads"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	set := func(key string, v *string) bool {
		if v == nil {
			return true
		}
		if err := a.deps.Settings.Set(ctx, key, strings.TrimSpace(*v)); err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not save library paths")
			return false
		}
		return true
	}
	if !set(keyLibMovies, req.Movies) || !set(keyLibTV, req.TV) || !set(keyLibEbooks, req.Ebooks) ||
		!set(keyLibAudiobooks, req.Audiobooks) || !set(keyLibDownloads, req.Downloads) {
		return
	}
	a.handleGetLibraryPaths(w, r)
}

// handleBrowse lists the sub-directories of a path, for the in-app folder picker. Admin-only,
// read-only directory listing (no file contents).
func (a *api) handleBrowse(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSpace(r.URL.Query().Get("path"))
	if p == "" {
		// Start somewhere useful — the mounted media root if it exists, else the FS root.
		p = "/"
		for _, cand := range []string{"/storage", "/media", "/data"} {
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
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, entry{Name: e.Name(), Path: filepath.ToSlash(filepath.Join(p, e.Name()))})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })

	parent := filepath.ToSlash(filepath.Dir(p))
	a.writeJSON(w, http.StatusOK, map[string]any{"path": p, "parent": parent, "dirs": dirs})
}
