// Package recyclebin manages Arrmada's recycle bin — the folder deleted/replaced files are moved
// to instead of being hard-deleted (movie & episode deletes, and Convert originals). It reports
// how much it's holding, empties it on demand, and enforces the user's guard rails (a maximum
// size in GB and/or a retention window in days), deleting oldest-first.
package recyclebin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/settings"
)

const (
	keyMaxGB     = "recycle_max_gb"         // cap in GB; 0 = unlimited
	keyRetention = "recycle_retention_days" // auto-delete after this many days; 0 = keep forever
)

// Service manages the recycle bin at dir ("" = recycling is off / hard-delete).
type Service struct {
	dir      string
	settings *settings.Service
	log      *slog.Logger
}

// New builds the manager. dir is the resolved recycle directory ("" when recycling is disabled).
func New(dir string, set *settings.Service, log *slog.Logger) *Service {
	return &Service{dir: dir, settings: set, log: log}
}

// Stats is a snapshot of the recycle bin plus the configured guard rails.
type Stats struct {
	Enabled       bool   `json:"enabled"`
	Dir           string `json:"dir"`
	Files         int    `json:"files"`
	Bytes         int64  `json:"bytes"`
	OldestUnix    int64  `json:"oldest_unix,omitempty"`
	MaxGB         int    `json:"max_gb"`
	RetentionDays int    `json:"retention_days"`
}

type entry struct {
	path string
	size int64
	mod  time.Time
}

// walk lists every recycled file currently in the bin (sidecar metadata files excluded).
func (s *Service) walk() []entry {
	if s.dir == "" {
		return nil
	}
	var out []entry
	_ = filepath.WalkDir(s.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, library.RecycleMetaExt) {
			return nil
		}
		if fi, e := d.Info(); e == nil {
			out = append(out, entry{path: p, size: fi.Size(), mod: fi.ModTime()})
		}
		return nil
	})
	return out
}

// Item is one recycled file surfaced to the management UI.
type Item struct {
	ID          string `json:"id"`   // path relative to the bin (stable handle)
	Name        string `json:"name"` // filename
	OrigPath    string `json:"orig_path,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
	DeletedUnix int64  `json:"deleted_unix"`
	Restorable  bool   `json:"restorable"` // false for legacy items with no recorded origin
}

// List returns the bin's contents, most-recently-deleted first.
func (s *Service) List(ctx context.Context) []Item {
	items := s.walk()
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	out := make([]Item, 0, len(items))
	for _, e := range items {
		rel, err := filepath.Rel(s.dir, e.path)
		if err != nil {
			continue
		}
		it := Item{ID: filepath.ToSlash(rel), Name: filepath.Base(e.path), SizeBytes: e.size, DeletedUnix: e.mod.Unix()}
		if m := library.ReadRecycleMeta(e.path); m.Orig != "" {
			it.OrigPath = m.Orig
			it.Restorable = true
			if m.Deleted > 0 {
				it.DeletedUnix = m.Deleted
			}
		}
		out = append(out, it)
	}
	return out
}

// resolve maps an item ID to an absolute path inside the bin, rejecting traversal.
func (s *Service) resolve(id string) (string, error) {
	if s.dir == "" {
		return "", fmt.Errorf("recycling is disabled")
	}
	full := filepath.Join(s.dir, filepath.FromSlash(id))
	rel, err := filepath.Rel(s.dir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid item")
	}
	return full, nil
}

// Restore moves a recycled file back to its original location and drops its sidecar.
// It refuses when the origin is unknown or already occupied.
func (s *Service) Restore(ctx context.Context, id string) error {
	full, err := s.resolve(id)
	if err != nil {
		return err
	}
	m := library.ReadRecycleMeta(full)
	if m.Orig == "" {
		return fmt.Errorf("can't restore — the original location isn't recorded for this item")
	}
	if _, err := os.Stat(m.Orig); err == nil {
		return fmt.Errorf("a file already exists at the original location")
	}
	if err := os.MkdirAll(filepath.Dir(m.Orig), 0o755); err != nil {
		return err
	}
	if err := moveFile(full, m.Orig); err != nil {
		return err
	}
	_ = os.Remove(full + library.RecycleMetaExt)
	s.pruneEmptyDirs()
	s.log.Info("recyclebin: restored", "to", m.Orig)
	return nil
}

// DeleteItem permanently removes one recycled file (and its sidecar).
func (s *Service) DeleteItem(ctx context.Context, id string) error {
	full, err := s.resolve(id)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(full + library.RecycleMetaExt)
	s.pruneEmptyDirs()
	return nil
}

// removeItem deletes a recycled file and its sidecar.
func removeItem(path string) error {
	err := os.Remove(path)
	_ = os.Remove(path + library.RecycleMetaExt)
	return err
}

// moveFile renames from→to, falling back to copy+remove across filesystems.
func moveFile(from, to string) error {
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := to + ".arrmada-tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, to); err != nil {
		return err
	}
	return os.Remove(from)
}

// Stats reports the bin's size + contents and the current caps.
func (s *Service) Stats(ctx context.Context) Stats {
	st := Stats{Enabled: s.dir != "", Dir: s.dir, MaxGB: s.maxGB(ctx), RetentionDays: s.retentionDays(ctx)}
	if !st.Enabled {
		return st
	}
	var oldest time.Time
	for _, e := range s.walk() {
		st.Files++
		st.Bytes += e.size
		if oldest.IsZero() || e.mod.Before(oldest) {
			oldest = e.mod
		}
	}
	if !oldest.IsZero() {
		st.OldestUnix = oldest.Unix()
	}
	return st
}

// Default guard rails. The bin is on by default and absorbs every delete, quality
// upgrade and Convert original, so shipping "unlimited" quietly grows it until the
// volume fills. These are the single source of truth — the settings API renders the
// same constants, so what the UI shows is what Enforce actually applies. Set either to
// 0 in Settings to opt back into unlimited.
const (
	DefaultMaxGB         = "50"
	DefaultRetentionDays = "30"
)

func (s *Service) maxGB(ctx context.Context) int {
	return atoiClampNonNeg(s.settings.Get(ctx, keyMaxGB, DefaultMaxGB))
}
func (s *Service) retentionDays(ctx context.Context) int {
	return atoiClampNonNeg(s.settings.Get(ctx, keyRetention, DefaultRetentionDays))
}

func atoiClampNonNeg(v string) int {
	n, _ := strconv.Atoi(v)
	if n < 0 {
		n = 0
	}
	return n
}

// Empty removes everything in the bin and returns the bytes freed.
func (s *Service) Empty(ctx context.Context) (int64, error) {
	if s.dir == "" {
		return 0, nil
	}
	kids, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var freed int64
	for _, e := range s.walk() {
		freed += e.size
	}
	for _, k := range kids {
		_ = os.RemoveAll(filepath.Join(s.dir, k.Name()))
	}
	s.log.Info("recyclebin: emptied", "freed_mb", freed>>20)
	return freed, nil
}

// Enforce applies the guard rails: first drop anything past the retention window, then, if the bin
// is still over the size cap, delete oldest-first until it's under. A no-op when both caps are off.
// Safe to call on a schedule.
func (s *Service) Enforce(ctx context.Context) {
	if s.dir == "" {
		return
	}
	retentionDays, maxGB := s.retentionDays(ctx), s.maxGB(ctx)
	if retentionDays == 0 && maxGB == 0 {
		return // no caps configured — keep forever
	}
	items := s.walk()
	if len(items) == 0 {
		return
	}
	removed := 0
	var freed int64

	// Retention: delete files older than the cutoff.
	kept := items[:0]
	if retentionDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -retentionDays)
		for _, e := range items {
			if e.mod.Before(cutoff) {
				if removeItem(e.path) == nil {
					removed++
					freed += e.size
				}
				continue
			}
			kept = append(kept, e)
		}
	} else {
		kept = items
	}

	// Size cap: if still over, delete oldest-first until under.
	if maxGB > 0 {
		limit := int64(maxGB) << 30
		var total int64
		for _, e := range kept {
			total += e.size
		}
		if total > limit {
			sort.Slice(kept, func(i, j int) bool { return kept[i].mod.Before(kept[j].mod) })
			for _, e := range kept {
				if total <= limit {
					break
				}
				if removeItem(e.path) == nil {
					total -= e.size
					removed++
					freed += e.size
				}
			}
		}
	}

	s.pruneEmptyDirs()
	if removed > 0 {
		s.log.Info("recyclebin: enforced caps", "removed", removed, "freed_mb", freed>>20,
			"retention_days", retentionDays, "max_gb", maxGB)
	}
}

// pruneEmptyDirs clears out subfolders left empty after purging their files.
func (s *Service) pruneEmptyDirs() {
	kids, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, k := range kids {
		if !k.IsDir() {
			continue
		}
		sub := filepath.Join(s.dir, k.Name())
		if inner, err := os.ReadDir(sub); err == nil && len(inner) == 0 {
			_ = os.Remove(sub)
		}
	}
}
