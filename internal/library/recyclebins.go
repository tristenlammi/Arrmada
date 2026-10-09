package library

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tristenlammi/arrmada/internal/libroots"
)

// RecycleDirName is the hidden bin folder at the top of each library folder. Being inside
// the library's own share keeps a delete a rename on the same drive; on Unraid one bin per
// share also keeps the file on the disk it was already on.
const RecycleDirName = ".arrmada-recycle"

// plexignoreName is the file that keeps Plex from listing a bin's contents as library
// items. "*" ignores everything under the folder it sits in.
const plexignoreName = ".plexignore"

// RootBins routes each deleted file to the bin on the library folder it came from:
// <root>/.arrmada-recycle for the longest library folder holding it. A single bin used to
// take every delete, and with libraries on separate mounts each one was a full copy, run
// inside the delete request, into a bin that could sit in the Docker image.
//
//   - Off (ARRMADA_RECYCLE_DIR=off): every delete is permanent (ErrRecycleDisabled).
//   - Explicit (ARRMADA_RECYCLE_DIR=<dir>): that one bin takes everything, as before.
//   - Otherwise a file inside no library folder goes to Legacy (the old shared bin),
//     with one warning per unmatched folder; with no Legacy either, the delete is refused.
//
// Roots is read on every delete, so a library folder changed in the app routes new
// deletes to the new folder's bin without a restart.
type RootBins struct {
	Explicit string
	Off      bool
	Roots    func() []libroots.Root
	Legacy   string
	Log      *slog.Logger

	mu     sync.Mutex
	warned map[string]bool
}

// For returns the bin path should go to, creating a library folder's bin (and its
// .plexignore) on first use.
func (b *RootBins) For(path string) (string, error) {
	if b.Off {
		return "", ErrRecycleDisabled
	}
	if b.Explicit != "" {
		return b.Explicit, nil
	}
	if root := b.rootOf(path); root != "" {
		dir := filepath.Join(root, RecycleDirName)
		if err := prepareBin(dir); err != nil {
			return "", err
		}
		return dir, nil
	}
	if b.Legacy == "" {
		return "", fmt.Errorf("%s isn't inside any library folder, so there's no recycle bin for it", path)
	}
	b.warnUnmatched(path)
	return b.Legacy, nil
}

// rootOf is the longest library folder holding path ("" when none does). The folder
// itself doesn't count as inside it.
func (b *RootBins) rootOf(path string) string {
	if b.Roots == nil {
		return ""
	}
	p, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	best := ""
	for _, r := range b.Roots() {
		if strings.TrimSpace(r.Path) == "" {
			continue
		}
		root, err := filepath.Abs(r.Path) // a relative env default ("./library/movies") too
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		if len(root) > len(best) {
			best = root
		}
	}
	return best
}

// warnUnmatched says, once per folder, that a delete went to the old shared bin because
// no library folder holds it — usually a file left behind in a folder the library has
// since moved away from. That bin can be on another drive, so it may be a slow copy.
func (b *RootBins) warnUnmatched(path string) {
	top := topDir(path)
	b.mu.Lock()
	if b.warned == nil {
		b.warned = map[string]bool{}
	}
	seen := b.warned[top]
	b.warned[top] = true
	b.mu.Unlock()
	if !seen && b.Log != nil {
		b.Log.Warn("recycle: file is outside every library folder — it goes to the old shared bin, which may be a copy across drives",
			"folder", top, "bin", b.Legacy)
	}
}

// topDir is the first two folders of path ("/storage/old-tv"), which names the share or
// library a stray file came from without logging every file.
func topDir(path string) string {
	p := filepath.ToSlash(filepath.Clean(path))
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return "/" + strings.Join(parts, "/")
}

// prepareBin makes a library folder's bin and its .plexignore. Without the .plexignore
// Plex would list every recycled film as a library item, so a bin that can't have one
// refuses the delete like any other broken bin.
//
// The library folder itself must already be there: MkdirAll on a share that isn't
// mounted would quietly build it on the container's own disk and fill that instead.
func prepareBin(dir string) error {
	if fi, err := os.Stat(filepath.Dir(dir)); err != nil {
		return fmt.Errorf("the library folder isn't there (%w)", err)
	} else if !fi.IsDir() {
		return fmt.Errorf("the library folder %s isn't a folder", filepath.Dir(dir))
	}
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	p := filepath.Join(dir, plexignoreName)
	if _, err := os.Stat(p); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.WriteFile(p, []byte("*\n"), 0o644)
}

// rootLabels names the libraries for the UI.
var rootLabels = map[string]string{
	"movies": "Movies", "tv": "TV", "ebooks": "Ebooks", "audiobooks": "Audiobooks", "music": "Music",
}

// All lists every bin the manager should look after: the old shared bin first while it
// still holds files (so they stay listed, restorable and aged until it drains), then the
// explicit bin or one bin per library folder (folders shared by two libraries share one).
// A library folder's bin is listed even before its first delete creates it.
func (b *RootBins) All() []BinDir {
	if b.Off {
		return nil
	}
	var roots []libroots.Root
	if b.Roots != nil {
		roots = b.Roots()
	}
	var serves []string
	for _, r := range roots {
		if strings.TrimSpace(r.Path) != "" {
			serves = append(serves, r.Path)
		}
	}
	var out []BinDir
	if b.Legacy != "" && filepath.Clean(b.Legacy) != filepath.Clean(b.Explicit) && binHasItems(b.Legacy) {
		out = append(out, BinDir{Dir: b.Legacy, Label: "Old shared bin", Legacy: true, Serves: serves})
	}
	if b.Explicit != "" {
		return append(out, BinDir{Dir: b.Explicit, Label: "Recycle bin", Serves: serves})
	}
	var order []string
	labels := map[string][]string{}
	for _, r := range roots {
		if strings.TrimSpace(r.Path) == "" {
			continue
		}
		root := filepath.Clean(r.Path)
		if _, ok := labels[root]; !ok {
			order = append(order, root)
		}
		name := rootLabels[r.Name]
		if name == "" {
			name = r.Name
		}
		labels[root] = append(labels[root], name)
	}
	for _, root := range order {
		out = append(out, BinDir{Dir: filepath.Join(root, RecycleDirName), Label: strings.Join(labels[root], " & "), Serves: []string{root}})
	}
	return out
}

// binHasItems reports whether a bin holds anything besides its .plexignore. Only the top
// level is read, so it's cheap enough for every delete dialog.
func binHasItems(dir string) bool {
	kids, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, k := range kids {
		if k.Name() != plexignoreName {
			return true
		}
	}
	return false
}

// SkipScanDir reports whether a library scan should skip a folder by its name. Hidden
// folders are Arrmada's own — the .arrmada-recycle bins, the audiobook merge backups — or
// a system's (.AppleDouble, .Trash): scanned as media they'd add deleted films back to the
// library, or bogus titles that vanish when the folder is cleared.
func SkipScanDir(name string) bool { return strings.HasPrefix(name, ".") }
