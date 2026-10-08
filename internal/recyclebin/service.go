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

	"github.com/tristenlammi/arrmada/internal/diskspace"
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
	// now and free are the clock and the free-space reading; tests swap them.
	now  func() time.Time
	free func(path string) (diskspace.Usage, bool)
}

// New builds the manager. dir is the resolved recycle directory ("" when recycling is disabled).
func New(dir string, set *settings.Service, log *slog.Logger) *Service {
	return &Service{dir: dir, settings: set, log: log, now: time.Now, free: diskspace.Of}
}

const (
	// capHold is how long a freshly deleted item is safe from the size cap. Without it a
	// single 4K remux bigger than the cap — or the very file an upgrade just replaced —
	// was purged at the next hourly run, before anyone could notice the mistake.
	capHold = 72 * time.Hour
	// lowDiskPct is the free space below which the hold gives way: a full disk is worse
	// than losing a recently deleted file, and the log says what went.
	lowDiskPct = 5.0
)

// Stats is a snapshot of the recycle bin plus the configured guard rails.
type Stats struct {
	Enabled       bool   `json:"enabled"`
	Dir           string `json:"dir"`
	Files         int    `json:"files"`
	Bytes         int64  `json:"bytes"`
	OldestUnix    int64  `json:"oldest_unix,omitempty"`
	MaxGB         int    `json:"max_gb"`
	RetentionDays int    `json:"retention_days"`
	// OverCapBytes is how far the bin is over its size cap (0 when under, or no cap).
	OverCapBytes int64 `json:"over_cap_bytes"`
	// ProtectedBytes is what the size cap may not touch yet: items deleted in the last
	// three days, plus the newest item.
	ProtectedBytes   int64 `json:"protected_bytes"`
	LargestItemBytes int64 `json:"largest_item_bytes"`
	// ProtectedUntil is when the last of the recent items becomes purgeable (unix, 0 = none).
	ProtectedUntil int64 `json:"protected_until,omitempty"`
}

// Mode answers "what happens to a file I delete right now?" for the delete dialogs.
// It never walks the bin, so every dialog can ask for it without paying for a scan of
// a bin holding thousands of files.
type Mode struct {
	Enabled bool `json:"enabled"`
	// Dirs is every bin a deleted file could land in. Today that's the one configured
	// bin; per-library bins will list each of theirs here.
	Dirs          []string `json:"dirs"`
	RetentionDays int      `json:"retention_days"`
	MaxGB         int      `json:"max_gb"`
}

// Mode reports whether deletes go to the bin (and which one) or are permanent, plus the
// guard rails, without reading the bin's contents.
func (s *Service) Mode(ctx context.Context) Mode {
	m := Mode{Enabled: s.dir != "", Dirs: []string{}, RetentionDays: s.retentionDays(ctx), MaxGB: s.maxGB(ctx)}
	if m.Enabled {
		m.Dirs = append(m.Dirs, s.dir)
	}
	return m
}

type entry struct {
	path string
	size int64
	mod  time.Time
}

// walk lists every recycled file currently in the bin (sidecar metadata files
// excluded). Each entry's age comes from its .arrmeta sidecar's Deleted timestamp
// when one exists — the authoritative deletion time — falling back to file mtime
// only for legacy items with no sidecar (or where stamping the mtime failed).
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
			mod := fi.ModTime()
			if m := library.ReadRecycleMeta(p); m.Deleted > 0 {
				mod = time.Unix(m.Deleted, 0)
			}
			out = append(out, entry{path: p, size: fi.Size(), mod: mod})
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
	// ExpiresAt is when retention deletes it for good (unix; 0 = retention is off). The
	// size cap can take it sooner, but never within three days of its deletion.
	ExpiresAt int64 `json:"expires_at"`
}

// List returns the bin's contents, most-recently-deleted first.
func (s *Service) List(ctx context.Context) []Item {
	retention := s.retentionDays(ctx)
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
		if retention > 0 {
			it.ExpiresAt = time.Unix(it.DeletedUnix, 0).AddDate(0, 0, retention).Unix()
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

// moveFile renames from→to, falling back to copy+remove across filesystems. The
// copy goes through a uniquely-named temp in the destination dir, fsyncs, and
// renames into place; the temp is removed on any error.
func moveFile(from, to string) error {
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(to), filepath.Base(to)+".*.arrmada-tmp")
	if err != nil {
		return err
	}
	tmp := out.Name()
	fail := func(e error) error {
		out.Close()
		_ = os.Remove(tmp)
		return e
	}
	if _, err := io.Copy(out, in); err != nil {
		return fail(err)
	}
	if err := out.Sync(); err != nil { // data durable before the rename publishes it
		return fail(err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil { // CreateTemp makes 0600
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, to); err != nil {
		_ = os.Remove(tmp)
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
	items := s.walk()
	now := s.clock()
	protected := capProtected(items, now)
	for _, e := range items {
		st.Files++
		st.Bytes += e.size
		if oldest.IsZero() || e.mod.Before(oldest) {
			oldest = e.mod
		}
		if e.size > st.LargestItemBytes {
			st.LargestItemBytes = e.size
		}
		if protected[e.path] {
			st.ProtectedBytes += e.size
			if until := e.mod.Add(capHold).Unix(); until > st.ProtectedUntil && e.mod.Add(capHold).After(now) {
				st.ProtectedUntil = until
			}
		}
	}
	if !oldest.IsZero() {
		st.OldestUnix = oldest.Unix()
	}
	if limit := int64(st.MaxGB) << 30; st.MaxGB > 0 && st.Bytes > limit {
		st.OverCapBytes = st.Bytes - limit
	}
	return st
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// capProtected marks what the size cap may not purge: anything deleted within capHold of
// now, and the single newest item whatever its age (the thing just deleted is the thing
// most likely to be wanted back).
func capProtected(items []entry, now time.Time) map[string]bool {
	out := map[string]bool{}
	var newest *entry
	for i := range items {
		if now.Sub(items[i].mod) < capHold {
			out[items[i].path] = true
		}
		if newest == nil || items[i].mod.After(newest.mod) {
			newest = &items[i]
		}
	}
	if newest != nil {
		out[newest.path] = true
	}
	return out
}

// lowOnDisk reports whether the bin's disk is nearly full (under lowDiskPct free).
// Unknown means no: the hold is only lifted on evidence.
func (s *Service) lowOnDisk() bool {
	free := s.free
	if free == nil {
		free = diskspace.Of
	}
	u, ok := free(s.dir)
	return ok && 100-u.UsedPct < lowDiskPct
}

// Headroom reports how many more bytes the bin can take before Enforce starts purging the
// oldest items to get back under the size cap. capped is false when there's no cap (free
// is then meaningless), and enabled is false when recycling is off and deletes are final.
// Convert asks before it retires an original: one that doesn't fit would push out other
// people's deletions and then be purged itself within the hour.
func (s *Service) Headroom(ctx context.Context) (free int64, capped, enabled bool) {
	if s.dir == "" {
		return 0, false, false
	}
	maxGB := s.maxGB(ctx)
	if maxGB == 0 {
		return 0, false, true
	}
	var used int64
	for _, e := range s.walk() {
		used += e.size
	}
	return max(int64(maxGB)<<30-used, 0), true, true
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
// is still over the size cap, delete oldest-first until it's under — skipping anything deleted in
// the last three days and the newest item, unless the bin's disk is nearly full. A no-op when both
// caps are off. Safe to call on a schedule.
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

	now := s.clock()
	// The newest item is judged before retention runs, so the cap can't take it later in
	// this pass just because older ones were retired first.
	protected := capProtected(items, now)

	// Retention: delete files older than the cutoff.
	kept := items[:0]
	if retentionDays > 0 {
		cutoff := now.AddDate(0, 0, -retentionDays)
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
			var held []entry
			for _, e := range kept {
				if total <= limit {
					break
				}
				if protected[e.path] {
					held = append(held, e)
					continue
				}
				if removeItem(e.path) == nil {
					total -= e.size
					removed++
					freed += e.size
				}
			}
			// A nearly full disk outranks the hold: purge the protected items too, oldest
			// first, and say exactly what went.
			if total > limit && len(held) > 0 && s.lowOnDisk() {
				for _, e := range held {
					if total <= limit {
						break
					}
					if removeItem(e.path) == nil {
						total -= e.size
						removed++
						freed += e.size
						s.log.Warn("recyclebin: disk nearly full — purged a recently deleted item early",
							"file", e.path, "size_mb", e.size>>20, "deleted", e.mod.Format(time.RFC3339))
					}
				}
			}
			if total > limit {
				s.log.Warn("recyclebin: over its size cap only because of recently deleted items — they're kept for 3 days",
					"over_mb", (total-limit)>>20, "max_gb", maxGB)
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
