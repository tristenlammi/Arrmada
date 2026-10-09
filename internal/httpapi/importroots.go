package httpapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/libroots"
	"github.com/tristenlammi/arrmada/internal/pathguard"
)

// Manual import used to walk whatever ?path= it was given, so "/" listed every big
// video, ebook and audio file on every disk of the host. Imports are now confined to
// the folders Arrmada was actually given: downloads and the libraries.
var (
	errImportPathDataDir = errors.New("That's Arrmada's own data folder — pick a folder inside your downloads or library folders")
	errImportPathOutside = errors.New("Pick a folder inside your downloads or library folders (Settings → Library)")
)

// importRoots lists every folder a manual import may read from. The config values were
// already overridden at boot by the folders chosen in the app (ApplySavedLibraryDirs);
// the saved settings are added too, so a folder changed since boot (music is read
// lazily) is still honoured.
func (a *api) importRoots(ctx context.Context) []string {
	c := a.deps.Config
	roots := []string{c.DownloadsDir, c.LibraryDir, c.MoviesDir, c.TVDir, c.EbooksDir, c.AudiobooksDir, c.MusicDir}
	if a.deps.Settings != nil {
		for _, k := range []string{keyLibDownloads, keyLibMovies, keyLibTV, keyLibEbooks, keyLibAudiobooks, keyLibMusic} {
			roots = append(roots, a.deps.Settings.Get(ctx, k, ""))
		}
	}
	return roots
}

// importListTimeout bounds one manual-import listing. The walks also stop at
// library.ListMaxResults files and library.ListMaxVisited entries, and as soon as the
// browser goes away (the request context).
const importListTimeout = 30 * time.Second

// importListContext is the context a listing walks under.
func importListContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), importListTimeout)
}

// writeImportList answers a manual-import listing with what the bounded walk found.
// truncated tells the UI to say "showing the first 500"; a walk that ran out of time
// returns what it had, marked truncated with a note. A browser that has already gone
// gets nothing.
func (a *api) writeImportList(w http.ResponseWriter, r *http.Request, ctx context.Context, kind, dir string, cands any, truncated bool) {
	resp := map[string]any{"path": dir, "candidates": cands, "truncated": truncated}
	if err := ctx.Err(); err != nil {
		if rerr := r.Context().Err(); rerr != nil {
			// No path in the line: it can name a book, and this is about the walk.
			a.deps.Log.Debug("manual import listing stopped: the request ended", "kind", kind, "err", rerr)
			return
		}
		resp["truncated"] = true
		resp["note"] = "This folder took too long to list, so these are the files found in the first 30 seconds — pick a narrower folder."
	}
	a.writeJSON(w, http.StatusOK, resp)
}

// checkImportPath vets a manual-import folder or file before anything touches the disk.
// An empty path means the downloads folder. It returns the cleaned absolute path to use
// from here on — the same spelling the rest of the app knows the roots by, so seeding
// and hardlink decisions that compare against the downloads folder keep working — while
// the check itself runs on the fully resolved path, so a symlink can't smuggle the walk
// out of the roots.
func (a *api) checkImportPath(ctx context.Context, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		p = a.deps.Config.DownloadsDir
	}
	clean, err := filepath.Abs(filepath.Clean(p))
	if err != nil {
		return "", errImportPathOutside
	}
	if libroots.InDataDir(clean, a.deps.Config.DataDir) {
		return "", errImportPathDataDir
	}
	if !pathguard.Within(clean, a.importRoots(ctx)...) {
		return "", errImportPathOutside
	}
	return clean, nil
}
