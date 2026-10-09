package libroots

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/pathguard"
)

// maxEntries caps the folder count shown beside a folder. It's there to say "yes, this
// is your library" at a glance, not to inventory a 40,000-folder share.
const maxEntries = 10000

// FolderCheck is what Arrmada can tell about a folder before it's saved: whether it's
// really there, whether the app user can write to it, whether finished downloads can be
// hardlinked into it, and how much room it has.
type FolderCheck struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	IsDir    bool   `json:"is_dir"`
	Writable bool   `json:"writable"`
	// HardlinkWithDownloads is a real link made from the downloads folder into this one
	// and removed again: nil when it couldn't be tried (no downloads folder, the
	// downloads folder isn't writable, or this is the downloads folder).
	HardlinkWithDownloads *bool  `json:"hardlink_with_downloads"`
	UnderDataDir          bool   `json:"under_data_dir"`
	FreeBytes             uint64 `json:"free_bytes"`
	TotalBytes            uint64 `json:"total_bytes"`
	Entries               int    `json:"entries"`
	EntriesCapped         bool   `json:"entries_capped"`
}

// CheckFolder looks at path as a library (or, with downloads == "", as the downloads
// folder itself). It never creates the folder, and every probe file it makes is removed
// before it returns. Nothing is written in or above the data folder.
//
// The hardlink probe is the point: on Unraid, /mnt/user shares that sit on different
// disks or pools can't hardlink between each other even though they look like one
// filesystem, and the only honest answer is to try.
func CheckFolder(path, downloads, dataDir string) FolderCheck {
	c := FolderCheck{Path: path}
	if strings.TrimSpace(path) == "" {
		return c
	}
	c.UnderDataDir = UnderDataDir(path, dataDir)
	fi, err := os.Stat(path)
	if err != nil {
		return c
	}
	c.Exists = true
	c.IsDir = fi.IsDir()
	if !c.IsDir {
		return c
	}
	if u, ok := diskspace.Of(path); ok {
		c.FreeBytes, c.TotalBytes = u.FreeBytes, u.TotalBytes
	}
	c.Entries, c.EntriesCapped = countDirs(path)
	if c.UnderDataDir {
		return c
	}
	c.Writable = ProbeWritable(path) == nil
	if c.Writable && strings.TrimSpace(downloads) != "" && !samePath(path, downloads) && !UnderDataDir(downloads, dataDir) {
		c.HardlinkWithDownloads = probeHardlink(downloads, path)
	}
	return c
}

// ProbeWritable proves the folder can be written by creating and removing a file, rather
// than reading mode bits, which say nothing under a read-only bind mount or a PUID
// mismatch. It never creates the folder itself.
func ProbeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".arrmada-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// probeHardlink makes a file in from, links it into to, and removes both. nil means the
// test couldn't be run (from isn't writable); false means the link itself failed —
// across filesystems (EXDEV), refused (EPERM), or anything else — so imports will copy.
func probeHardlink(from, to string) *bool {
	f, err := os.CreateTemp(from, ".arrmada-link-probe-*")
	if err != nil {
		return nil
	}
	src := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(src) }()

	dst := filepath.Join(to, filepath.Base(src))
	ok := os.Link(src, dst) == nil
	if ok {
		// Only remove what this probe made: a failed link leaves nothing at dst, and a
		// file already there by that random name is someone else's.
		_ = os.Remove(dst)
	}
	return &ok
}

// samePath reports whether two folders are the same once resolved.
func samePath(a, b string) bool {
	ra, err := pathguard.Resolve(a)
	if err != nil {
		return false
	}
	rb, err := pathguard.Resolve(b)
	if err != nil {
		return false
	}
	return ra == rb
}

// countDirs counts the visible sub-folders of dir, reading in chunks and stopping at
// maxEntries.
func countDirs(dir string) (n int, capped bool) {
	f, err := os.Open(dir)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	for {
		entries, err := f.ReadDir(512)
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				n++
				if n >= maxEntries {
					return n, true
				}
			}
		}
		if err != nil || len(entries) == 0 {
			return n, false
		}
	}
}
