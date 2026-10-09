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
	var st FolderState
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		parent := filepath.Dir(filepath.Clean(path))
		if pi, perr := os.Stat(parent); perr == nil && pi.IsDir() {
			st.ParentExists = true
			st.ParentWritable = libroots.ProbeWritable(parent) == nil
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
	if werr := libroots.ProbeWritable(path); werr != nil {
		st.Err = werr
	} else {
		st.Writable = true
	}
	return st
}

// SameFilesystem reports whether a and b live on one filesystem, and whether that could
// be told at all (ok is false off Linux, or when a path has no existing parent). It
// compares filesystem ids. The dashboard's older trick — identical totals and free space
// — gives a false "different" whenever a download writes between the two readings,
// which on a busy torrent drive is most of the time. A folder that doesn't exist yet is
// judged at its nearest existing parent, which is where it would be created.
func SameFilesystem(a, b string) (same, ok bool) {
	pa, okA := existing(a)
	pb, okB := existing(b)
	if !okA || !okB {
		return false, false
	}
	da, okA := diskspace.Device(pa)
	db, okB := diskspace.Device(pb)
	if !okA || !okB {
		return false, false
	}
	return da == db, true
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
