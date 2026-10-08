package httpapi

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

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
	if a.deps.Config.DataDir != "" && pathguard.Under(clean, a.deps.Config.DataDir) {
		return "", errImportPathDataDir
	}
	if !pathguard.Within(clean, a.importRoots(ctx)...) {
		return "", errImportPathOutside
	}
	return clean, nil
}
