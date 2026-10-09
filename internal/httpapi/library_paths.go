package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/libroots"
)

// Settings keys for the in-app library folder config (libroots owns them). Each falls
// back to the env-configured default (config.*Dir) when unset or blank, so the app works
// before the user picks anything.
const (
	keyLibMovies     = libroots.KeyMovies
	keyLibTV         = libroots.KeyTV
	keyLibEbooks     = libroots.KeyEbooks
	keyLibAudiobooks = libroots.KeyAudiobooks
	keyLibMusic      = libroots.KeyMusic
	keyLibDownloads  = libroots.KeyDownloads
)

// roots resolves the folders the same way the importer, the coordinator and the disk
// guard do (libroots), so the API never describes a folder the app isn't using.
// Deps.Config holds the environment's folders: they're the fallback, never overlaid.
func (a *api) roots() *libroots.Roots {
	var get libroots.Getter
	if a.deps.Settings != nil {
		get = a.deps.Settings.Get
	}
	return libroots.New(get, a.deps.Config)
}

// libMovies etc. return the effective (settings-or-default) path for each library.
func (a *api) libMovies(r *http.Request) string     { return a.roots().Movies(r.Context()) }
func (a *api) libTV(r *http.Request) string         { return a.roots().TV(r.Context()) }
func (a *api) libEbooks(r *http.Request) string     { return a.roots().Ebooks(r.Context()) }
func (a *api) libAudiobooks(r *http.Request) string { return a.roots().Audiobooks(r.Context()) }
func (a *api) libMusic(r *http.Request) string      { return a.roots().Music(r.Context()) }
func (a *api) libDownloads(r *http.Request) string  { return a.roots().Downloads(r.Context()) }

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

// pickedConfig is the environment's config with each folder replaced by the one in use
// now (saved in the app, or the environment's), for code that judges folders through a
// config.Config: the health panel and the disk guard panel.
func (a *api) pickedConfig(ctx context.Context) config.Config {
	return a.roots().Config(ctx)
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
// missing tells the UI it may offer to create the folder.
func (a *api) writeFolderError(w http.ResponseWriter, folder, message string, missing bool) {
	body := map[string]any{"status": "error", "message": message, "folder": folder}
	if missing {
		body["missing"] = true
	}
	a.writeJSON(w, http.StatusBadRequest, body)
}

// folderMissing and folderNotDir explain why a folder can't be saved as it stands.
func folderMissing(label, p string) string {
	return fmt.Sprintf("%s: %s doesn't exist. Check the spelling, or create it.", label, p)
}

func folderNotDir(label, p string) string {
	return fmt.Sprintf("%s: %s is a file, not a folder.", label, p)
}

// handleSetLibraryPaths saves whichever folders were provided (nil = leave unchanged).
// Every provided folder is checked before any is written, so one bad field never leaves
// a half-saved set behind.
//
// The data-folder rule applies to every folder sent. Existence is checked only for a
// folder that is changing: an install whose current folder is odd (a mount that's down
// right now) must still be able to save its other folders.
func (a *api) handleSetLibraryPaths(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Movies     *string `json:"movies"`
		TV         *string `json:"tv"`
		Ebooks     *string `json:"ebooks"`
		Audiobooks *string `json:"audiobooks"`
		Music      *string `json:"music"`
		Downloads  *string `json:"downloads"`
		// Create makes any missing folder (after the user clicked "Create it") instead
		// of refusing it.
		Create bool `json:"create"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	folders := []struct {
		name    string
		key     string
		value   *string
		current string
	}{
		{"movies", keyLibMovies, req.Movies, a.libMovies(r)},
		{"tv", keyLibTV, req.TV, a.libTV(r)},
		{"ebooks", keyLibEbooks, req.Ebooks, a.libEbooks(r)},
		{"audiobooks", keyLibAudiobooks, req.Audiobooks, a.libAudiobooks(r)},
		{"music", keyLibMusic, req.Music, a.libMusic(r)},
		{"downloads", keyLibDownloads, req.Downloads, a.libDownloads(r)},
	}
	var create []string
	for _, f := range folders {
		if f.value == nil {
			continue
		}
		*f.value = strings.TrimSpace(*f.value)
		label := folderLabel[f.name]
		// Blank means "use the install default", which is the environment's business.
		if err := a.dataDirConflict(label, *f.value); err != nil {
			a.writeFolderError(w, f.name, err.Error(), false)
			return
		}
		if *f.value == "" || *f.value == f.current {
			continue
		}
		if !filepath.IsAbs(*f.value) {
			a.writeFolderError(w, f.name, fmt.Sprintf("%s: use the full path, starting with / (for example /storage/media/%s).", label, f.name), false)
			return
		}
		fi, err := os.Stat(*f.value)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if !req.Create {
				a.writeFolderError(w, f.name, folderMissing(label, *f.value), true)
				return
			}
			create = append(create, *f.value)
		case err != nil:
			a.writeFolderError(w, f.name, fmt.Sprintf("%s: Arrmada can't open %s (%v).", label, *f.value, err), false)
			return
		case !fi.IsDir():
			a.writeFolderError(w, f.name, folderNotDir(label, *f.value), false)
			return
		}
	}
	// Folders are made only once every field has passed, as the app user, group-writable
	// so the download client (often the same PGID) can write into them too.
	for _, p := range create {
		if err := os.MkdirAll(p, 0o775); err != nil {
			a.writeError(w, http.StatusBadRequest, fmt.Sprintf("Couldn't create %s: %v", p, err))
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
	a.writeJSON(w, http.StatusOK, map[string]any{
		"path": p, "parent": parent, "dirs": dirs,
		// The folder on screen can be walked through but not chosen: "/" and anything
		// else holding the data folder, or the data folder itself.
		"path_disabled": libroots.UnderDataDir(p, a.deps.Config.DataDir),
	})
}

// handleCheckLibraryFolder — GET /api/v1/system/library/check?path=&kind=[&downloads=]
// reports what a folder looks like before it's saved, for the chips beside each row:
// there, writable, hardlinks with Downloads, free space, how many folders. kind=downloads
// checks the downloads folder itself, so there's no link to test. downloads= lets the
// form test against a Downloads folder it hasn't saved yet. error carries the reason
// the folder would be refused on save, in the same words.
func (a *api) handleCheckLibraryFolder(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := strings.TrimSpace(q.Get("path"))
	kind := q.Get("kind")
	label, ok := folderLabel[kind]
	if !ok {
		a.writeError(w, http.StatusBadRequest, "kind must be movies, tv, ebooks, audiobooks, music or downloads")
		return
	}
	if p == "" {
		a.writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	downloads := ""
	if kind != "downloads" {
		downloads = strings.TrimSpace(q.Get("downloads"))
		if downloads == "" {
			downloads = a.pickedConfig(r.Context()).DownloadsDir
		}
	}
	c := libroots.CheckFolder(p, downloads, a.deps.Config.DataDir)
	problem := ""
	switch {
	case c.UnderDataDir:
		problem = a.dataDirConflict(label, p).Error()
	case !filepath.IsAbs(p):
		problem = fmt.Sprintf("%s: use the full path, starting with /.", label)
	case !c.Exists:
		problem = folderMissing(label, p)
	case !c.IsDir:
		problem = folderNotDir(label, p)
	}
	a.writeJSON(w, http.StatusOK, struct {
		libroots.FolderCheck
		Error string `json:"error,omitempty"`
	}{c, problem})
}
