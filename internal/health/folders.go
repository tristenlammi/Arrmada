// Package health answers "are the folders Arrmada was given actually usable?" for the
// health panel, the disk guard panel and the startup log, so all three judge the same
// folders the same way.
//
// They used to judge ARRMADA_LIBRARY_DIR — the managed volume — so the panel could say
// the library was writable while the real Movies share was read-only, and the probe
// itself created a missing mount point on the way.
package health

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/libroots"
)

// Folder is one folder the app works in: Role is the stable key ("movies", "books"),
// Label how the UI names it.
type Folder struct {
	Role  string `json:"role"`
	Label string `json:"label"`
	Path  string `json:"path"`
}

// FolderState is what a probe found. ParentExists and ParentWritable only mean something
// when the folder itself is missing: they say whether it can still be made (a folder
// imports will create) or whether the share behind it isn't mounted at all.
type FolderState struct {
	Exists         bool
	IsDir          bool
	Writable       bool
	ParentExists   bool
	ParentWritable bool
	Err            error
}

// ProbeFolder looks at path without changing anything that matters: it never creates the
// folder (or its parent), and the write probe is a temp file removed straight away.
func ProbeFolder(path string) FolderState {
	return (*ProbeCache)(nil).ProbeFolder(path)
}

// ProbeCache remembers which folders passed a write probe recently, so a health panel
// that's polled doesn't write to the array on every poll. On Unraid each probe in a
// /mnt/user share can spin up a sleeping disk and touch parity; once an hour is plenty
// to notice a share gone read-only. Only successes are remembered: a folder that failed
// is probed again next time (a failed create writes nothing), so a fix shows at once.
// Existence is always checked fresh. A nil *ProbeCache probes every time.
type ProbeCache struct {
	ttl time.Duration
	now func() time.Time
	mu  sync.Mutex
	ok  map[string]time.Time // folder → when a write last succeeded
}

// NewProbeCache remembers successful write probes for ttl.
func NewProbeCache(ttl time.Duration) *ProbeCache {
	return &ProbeCache{ttl: ttl, now: time.Now, ok: map[string]time.Time{}}
}

// ProbeFolder is the package ProbeFolder, reusing a recent successful write probe.
func (c *ProbeCache) ProbeFolder(path string) FolderState {
	var st FolderState
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		parent := filepath.Dir(filepath.Clean(path))
		if pi, perr := os.Stat(parent); perr == nil && pi.IsDir() {
			st.ParentExists = true
			st.ParentWritable = c.writable(parent) == nil
		}
		return st
	case err != nil:
		st.Err = err
		return st
	}
	st.Exists = true
	st.IsDir = fi.IsDir()
	if !st.IsDir {
		return st
	}
	if werr := c.writable(path); werr != nil {
		st.Err = werr
	} else {
		st.Writable = true
	}
	return st
}

func (c *ProbeCache) writable(dir string) error {
	if c == nil {
		return libroots.ProbeWritable(dir)
	}
	c.mu.Lock()
	at, seen := c.ok[dir]
	c.mu.Unlock()
	if seen && c.now().Sub(at) < c.ttl {
		return nil
	}
	err := libroots.ProbeWritable(dir)
	c.mu.Lock()
	if err == nil {
		c.ok[dir] = c.now()
	} else {
		delete(c.ok, dir)
	}
	c.mu.Unlock()
	return err
}

// SameFilesystem reports whether a and b live on one filesystem, and whether that could
// be told at all (ok is false off Linux, or when a path has no existing parent). A folder
// that doesn't exist yet is judged at its nearest existing parent, which is where it
// would be created.
//
// Two signals, either of which means "same": one filesystem id, or byte-identical totals
// and free space (the dashboard's test). The id is stable while a download writes between
// two readings, which makes free space differ; the free-space test catches what the disk
// guard actually measures, which is what matters when it's asking "am I watching the
// library's disk?". Saying "same" when in doubt errs towards the warning.
func SameFilesystem(a, b string) (same, ok bool) {
	pa, okA := existing(a)
	pb, okB := existing(b)
	if !okA || !okB {
		return false, false
	}
	if da, okA := diskspace.Device(pa); okA {
		if db, okB := diskspace.Device(pb); okB {
			ok = true
			if da == db {
				return true, true
			}
		}
	}
	if ua, okA := diskspace.Of(pa); okA {
		if ub, okB := diskspace.Of(pb); okB {
			return ua.TotalBytes == ub.TotalBytes && ua.FreeBytes == ub.FreeBytes, true
		}
	}
	return false, ok
}

// existing returns path, or its nearest parent that exists.
func existing(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	p := filepath.Clean(path)
	for {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", false
		}
		p = parent
	}
}

// LibraryFolders lists the folders to check, in the order the UI shows them: Movies and
// TV; Ebooks and Audiobooks when Books is on (one "Books & audiobooks" entry when they're
// the same folder); Music when it's on; then Downloads. cfg should hold the folders the
// user picked — the running config is overridden by the app's saved folders at boot, and
// handlers overlay the saved values again so a pick made since boot is what's judged.
func LibraryFolders(cfg config.Config, booksOn, musicOn bool) []Folder {
	var out []Folder
	add := func(role, label, path string) {
		if path != "" {
			out = append(out, Folder{Role: role, Label: label, Path: path})
		}
	}
	add("movies", "Movies", cfg.MoviesDir)
	add("tv", "TV", cfg.TVDir)
	if booksOn {
		if cfg.EbooksDir != "" && filepath.Clean(cfg.EbooksDir) == filepath.Clean(cfg.AudiobooksDir) {
			add("books", "Books & audiobooks", cfg.EbooksDir)
		} else {
			add("ebooks", "Ebooks", cfg.EbooksDir)
			add("audiobooks", "Audiobooks", cfg.AudiobooksDir)
		}
	}
	if musicOn {
		add("music", "Music", cfg.MusicDir)
	}
	add("downloads", "Downloads", cfg.DownloadsDir)
	return out
}
