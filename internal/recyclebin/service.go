// Package recyclebin manages Arrmada's recycle bin — the folder deleted/replaced files are moved
// to instead of being hard-deleted (movie & episode deletes, and Convert originals). It reports
// how much it's holding, empties it on demand, and enforces the user's guard rails (a maximum
// size in GB and/or a retention window in days), deleting oldest-first.
package recyclebin

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

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

// walk lists every regular file currently in the bin.
func (s *Service) walk() []entry {
	if s.dir == "" {
		return nil
	}
	var out []entry
	_ = filepath.WalkDir(s.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, e := d.Info(); e == nil {
			out = append(out, entry{path: p, size: fi.Size(), mod: fi.ModTime()})
		}
		return nil
	})
	return out
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

func (s *Service) maxGB(ctx context.Context) int         { return atoiClampNonNeg(s.settings.Get(ctx, keyMaxGB, "0")) }
func (s *Service) retentionDays(ctx context.Context) int { return atoiClampNonNeg(s.settings.Get(ctx, keyRetention, "0")) }

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
				if os.Remove(e.path) == nil {
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
				if os.Remove(e.path) == nil {
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
