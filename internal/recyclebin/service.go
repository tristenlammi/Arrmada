// Package recyclebin manages Arrmada's recycle bins — the folders deleted/replaced files are
// moved to instead of being hard-deleted (movie & episode deletes, and Convert originals). There
// can be several: one on each library folder, so recycling is a rename on the same drive, plus
// the old shared bin while it still holds files. It reports how much they're holding, empties
// them on demand, and enforces the user's guard rails (a maximum size in GB across all bins
// and/or a retention window in days), deleting oldest-first.
package recyclebin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// markerFiles live at the top of a bin and aren't recycled items: .plexignore keeps Plex
// from listing the bin's contents as library items.
var markerFiles = map[string]bool{".plexignore": true}

// staleTempAge is how old an unfinished copy must be before it's cleared: a copy that
// old was cut short by a crash or restart, not still being written.
const staleTempAge = 24 * time.Hour

// Service manages the recycle bins bins() lists (none = recycling is off / hard-delete).
type Service struct {
	bins     func() []library.BinDir
	settings *settings.Service
	log      *slog.Logger
	// now, free and same are the clock, the free-space reading and the same-drive test;
	// tests swap them.
	now  func() time.Time
	free func(path string) (diskspace.Usage, bool)
	same func(a, b string) (same, ok bool)
}

// New builds the manager over one bin. dir is the resolved recycle directory ("" when
// recycling is disabled).
func New(dir string, set *settings.Service, log *slog.Logger) *Service {
	return NewBins(func() []library.BinDir {
		if dir == "" {
			return nil
		}
		return []library.BinDir{{Dir: dir, Label: "Recycle bin"}}
	}, set, log)
}

// NewBins builds the manager over the bins bins() lists, asked afresh on every call so a
// library folder changed in Settings brings its bin with it. Put the legacy bin first:
// where two entries name one folder, the first wins.
func NewBins(bins func() []library.BinDir, set *settings.Service, log *slog.Logger) *Service {
	return &Service{bins: bins, settings: set, log: log, now: time.Now, free: diskspace.Of, same: diskspace.SameDevice}
}

// bin is one managed bin with its handle. key is the first 10 hex characters of the
// sha256 of its absolute path: stable across restarts, and never a path a crafted item
// ID could walk.
type bin struct {
	library.BinDir
	key string
}

func binKey(abs string) string {
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:10]
}

// current lists the bins now, absolute and de-duplicated.
func (s *Service) current() []bin {
	if s.bins == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []bin
	for _, b := range s.bins() {
		if strings.TrimSpace(b.Dir) == "" {
			continue
		}
		abs, err := filepath.Abs(b.Dir)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		b.Dir = abs
		out = append(out, bin{BinDir: b, key: binKey(abs)})
	}
	return out
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

// BinStats is one bin's share of Stats.
type BinStats struct {
	Key    string `json:"key"`
	Dir    string `json:"dir"`
	Label  string `json:"label"`
	Legacy bool   `json:"legacy"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
	// FreeBytes is the free space on the bin's drive; FreeKnown is false when it can't
	// be measured (off Linux, or a bin whose drive isn't there).
	FreeBytes uint64 `json:"free_bytes"`
	FreeKnown bool   `json:"free_known"`
	// OtherDrive: the bin isn't on the same drive as a library folder it serves, so every
	// delete into it is a full copy.
	OtherDrive bool `json:"other_drive"`
}

// Stats is a snapshot of the recycle bins plus the configured guard rails. The counts
// and sizes at the top are totals across every bin; the cap applies to that total.
type Stats struct {
	Enabled bool `json:"enabled"`
	// Dir is the first bin new deletes go to, kept for older pages; Bins lists them all.
	Dir           string     `json:"dir"`
	Bins          []BinStats `json:"bins"`
	Files         int        `json:"files"`
	Bytes         int64      `json:"bytes"`
	OldestUnix    int64      `json:"oldest_unix,omitempty"`
	MaxGB         int        `json:"max_gb"`
	RetentionDays int        `json:"retention_days"`
	// OverCapBytes is how far the bins are over the size cap (0 when under, or no cap).
	OverCapBytes int64 `json:"over_cap_bytes"`
	// ProtectedBytes is what the size cap may not touch yet: items deleted in the last
	// three days, plus the newest item.
	ProtectedBytes   int64 `json:"protected_bytes"`
	LargestItemBytes int64 `json:"largest_item_bytes"`
	// ProtectedUntil is when the last of the recent items becomes purgeable (unix, 0 = none).
	ProtectedUntil int64 `json:"protected_until,omitempty"`
}

// Mode answers "what happens to a file I delete right now?" for the delete dialogs.
// It never walks the bins, so every dialog can ask for it without paying for a scan of
// bins holding thousands of files.
type Mode struct {
	Enabled bool `json:"enabled"`
	// Dirs is every bin a deleted file could land in: one per library folder, the old
	// shared bin while it holds anything, or the single ARRMADA_RECYCLE_DIR.
	Dirs          []string `json:"dirs"`
	RetentionDays int      `json:"retention_days"`
	MaxGB         int      `json:"max_gb"`
}

// Mode reports whether deletes go to the bins (and which) or are permanent, plus the
// guard rails, without reading the bins' contents.
func (s *Service) Mode(ctx context.Context) Mode {
	bins := s.current()
	m := Mode{Enabled: len(bins) > 0, Dirs: []string{}, RetentionDays: s.retentionDays(ctx), MaxGB: s.maxGB(ctx)}
	for _, b := range bins {
		m.Dirs = append(m.Dirs, b.Dir)
	}
	return m
}

type entry struct {
	path string
	size int64
	mod  time.Time
	bin  int // index into the bins the walk was given
}

// walk lists every recycled file currently in the bins (sidecars, markers and copies
// still being written excluded), plus unfinished copies old enough to clear. Each entry's
// age comes from its .arrmeta sidecar's Deleted timestamp when one exists — the
// authoritative deletion time — falling back to file mtime only for legacy items with no
// sidecar (or where stamping the mtime failed).
func (s *Service) walk(bins []bin) (items []entry, staleTemps []string) {
	now := s.clock()
	for i, b := range bins {
		_ = filepath.WalkDir(b.Dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			name := d.Name()
			switch {
			case strings.HasSuffix(name, library.RecycleMetaExt):
				return nil
			case filepath.Dir(p) == b.Dir && markerFiles[name]:
				return nil
			case strings.HasSuffix(name, library.PartialSuffix):
				// Half a copy is never a recycled file; the source it came from is still
				// where it was. Only a long-abandoned one is cleared.
				if fi, e := d.Info(); e == nil && now.Sub(fi.ModTime()) > staleTempAge {
					staleTemps = append(staleTemps, p)
				}
				return nil
			}
			if fi, e := d.Info(); e == nil {
				mod := fi.ModTime()
				if m := library.ReadRecycleMeta(p); m.Deleted > 0 {
					mod = time.Unix(m.Deleted, 0)
				}
				items = append(items, entry{path: p, size: fi.Size(), mod: mod, bin: i})
			}
			return nil
		})
	}
	return items, staleTemps
}

// Item is one recycled file surfaced to the management UI.
type Item struct {
	// ID is "<bin key>/<path inside the bin>": a stable handle that resolve checks
	// against the bins in use, so it can't name a file anywhere else.
	ID       string `json:"id"`
	Name     string `json:"name"` // filename
	Bin      string `json:"bin"`  // the bin's key
	BinLabel string `json:"bin_label"`
	// Legacy is set for items in the old shared bin.
	Legacy      bool   `json:"legacy"`
	OrigPath    string `json:"orig_path,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
	DeletedUnix int64  `json:"deleted_unix"`
	Restorable  bool   `json:"restorable"` // false for legacy items with no recorded origin
	// ExpiresAt is when retention deletes it for good (unix; 0 = retention is off). The
	// size cap can take it sooner, but never within three days of its deletion.
	ExpiresAt int64 `json:"expires_at"`
}

// List returns the bins' contents, most-recently-deleted first.
func (s *Service) List(ctx context.Context) []Item {
	retention := s.retentionDays(ctx)
	bins := s.current()
	items, _ := s.walk(bins)
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	out := make([]Item, 0, len(items))
	for _, e := range items {
		b := bins[e.bin]
		rel, err := filepath.Rel(b.Dir, e.path)
		if err != nil {
			continue
		}
		it := Item{
			ID: b.key + "/" + filepath.ToSlash(rel), Name: filepath.Base(e.path), Bin: b.key, BinLabel: b.Label,
			Legacy: b.Legacy, SizeBytes: e.size, DeletedUnix: e.mod.Unix(),
		}
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

// resolve maps an item ID to an absolute path inside one of the bins in use. An unknown
// bin key, a path that climbs out of its bin, or a bin's own marker file is refused.
func (s *Service) resolve(id string) (string, bin, error) {
	key, rel, ok := strings.Cut(id, "/")
	if !ok || key == "" || rel == "" {
		return "", bin{}, fmt.Errorf("invalid item")
	}
	bins := s.current()
	if len(bins) == 0 {
		return "", bin{}, fmt.Errorf("recycling is disabled")
	}
	for _, b := range bins {
		if b.key != key {
			continue
		}
		full := filepath.Join(b.Dir, filepath.FromSlash(rel))
		r, err := filepath.Rel(b.Dir, full)
		if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(os.PathSeparator)) || markerFiles[r] {
			return "", bin{}, fmt.Errorf("invalid item")
		}
		return full, b, nil
	}
	return "", bin{}, fmt.Errorf("that recycle bin isn't in use any more — refresh the list")
}

// Restore moves a recycled file back to its original location and drops its sidecar.
// It refuses when the origin is unknown or already occupied.
func (s *Service) Restore(ctx context.Context, id string) error {
	full, b, err := s.resolve(id)
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
	pruneEmptyDirs(b.Dir)
	s.log.Info("recyclebin: restored", "to", m.Orig)
	return nil
}

// DeleteItem permanently removes one recycled file (and its sidecar).
func (s *Service) DeleteItem(ctx context.Context, id string) error {
	full, b, err := s.resolve(id)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(full + library.RecycleMetaExt)
	pruneEmptyDirs(b.Dir)
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
	out, err := os.CreateTemp(filepath.Dir(to), filepath.Base(to)+".*"+library.PartialSuffix)
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

// Stats reports the bins' sizes + contents and the current caps.
func (s *Service) Stats(ctx context.Context) Stats {
	bins := s.current()
	st := Stats{Enabled: len(bins) > 0, Bins: []BinStats{}, MaxGB: s.maxGB(ctx), RetentionDays: s.retentionDays(ctx)}
	if !st.Enabled {
		return st
	}
	for _, b := range bins {
		bs := BinStats{Key: b.key, Dir: b.Dir, Label: b.Label, Legacy: b.Legacy, OtherDrive: s.otherDrive(b)}
		if u, ok := s.freeOf(b.Dir); ok {
			bs.FreeBytes, bs.FreeKnown = u.FreeBytes, true
		}
		if st.Dir == "" && !b.Legacy {
			st.Dir = b.Dir
		}
		st.Bins = append(st.Bins, bs)
	}
	if st.Dir == "" {
		st.Dir = bins[0].Dir
	}
	var oldest time.Time
	items, _ := s.walk(bins)
	now := s.clock()
	protected := capProtected(items, now)
	for _, e := range items {
		st.Files++
		st.Bytes += e.size
		st.Bins[e.bin].Files++
		st.Bins[e.bin].Bytes += e.size
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

// otherDrive reports whether the bin is on a different drive from any library folder it
// serves. Unknown counts as no: the warning is only given on evidence.
func (s *Service) otherDrive(b bin) bool {
	same := s.same
	if same == nil {
		same = diskspace.SameDevice
	}
	for _, root := range b.Serves {
		if root == "" {
			continue
		}
		if sm, ok := same(b.Dir, root); ok && !sm {
			return true
		}
	}
	return false
}

// Problem is a bin the health panel should mention: on another drive from its library,
// or the old shared bin still holding files.
type Problem struct {
	Dir        string
	Label      string
	OtherDrive bool
	LegacyFull bool
}

// Problems is the health panel's cheap view of the bins: it reads the legacy bin's top
// level and asks the filesystem ids, but never walks a bin.
func (s *Service) Problems() []Problem {
	var out []Problem
	for _, b := range s.current() {
		p := Problem{Dir: b.Dir, Label: b.Label, OtherDrive: s.otherDrive(b), LegacyFull: b.Legacy && HasItems(b.Dir)}
		if p.OtherDrive || p.LegacyFull {
			out = append(out, p)
		}
	}
	return out
}

// HasItems reports whether a bin holds anything besides its marker files. It reads only
// the top level, so a bin left with nothing but empty folders still counts until the
// next clean-up removes them.
func HasItems(dir string) bool {
	kids, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, k := range kids {
		if !markerFiles[k.Name()] {
			return true
		}
	}
	return false
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) freeOf(dir string) (diskspace.Usage, bool) {
	free := s.free
	if free == nil {
		free = diskspace.Of
	}
	return free(dir)
}

// capProtected marks what the size cap may not purge: anything deleted within capHold of
// now, and the single newest item across every bin whatever its age (the thing just
// deleted is the thing most likely to be wanted back).
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

// lowOnDisk reports whether a bin's disk is nearly full (under lowDiskPct free).
// Unknown means no: the hold is only lifted on evidence.
func (s *Service) lowOnDisk(dir string) bool {
	u, ok := s.freeOf(dir)
	return ok && 100-u.UsedPct < lowDiskPct
}

// Headroom reports how many more bytes the bins can take before Enforce starts purging the
// oldest items to get back under the size cap. capped is false when there's no cap (free
// is then meaningless), and enabled is false when recycling is off and deletes are final.
// Convert asks before it retires an original: one that doesn't fit would push out other
// people's deletions and then be purged itself within the hour.
func (s *Service) Headroom(ctx context.Context) (free int64, capped, enabled bool) {
	bins := s.current()
	if len(bins) == 0 {
		return 0, false, false
	}
	maxGB := s.maxGB(ctx)
	if maxGB == 0 {
		return 0, false, true
	}
	var used int64
	items, _ := s.walk(bins)
	for _, e := range items {
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

// ErrUnknownBin is a bin key that names none of the bins in use (a page left open
// across a folder change, or a crafted request).
var ErrUnknownBin = errors.New("that recycle bin isn't in use any more — refresh the page")

// Empty removes everything in the bins — or only in the bin whose key is binKey — and
// returns the bytes freed. Each bin's marker files (.plexignore) stay, so Plex keeps
// ignoring the folder.
func (s *Service) Empty(ctx context.Context, binKey string) (int64, error) {
	var bins []bin
	for _, b := range s.current() {
		if binKey == "" || b.key == binKey {
			bins = append(bins, b)
		}
	}
	if binKey != "" && len(bins) == 0 {
		return 0, ErrUnknownBin
	}
	var freed int64
	items, _ := s.walk(bins)
	for _, e := range items {
		freed += e.size
	}
	for _, b := range bins {
		kids, err := os.ReadDir(b.Dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return freed, err
		}
		for _, k := range kids {
			if markerFiles[k.Name()] {
				continue
			}
			_ = os.RemoveAll(filepath.Join(b.Dir, k.Name()))
		}
		s.log.Info("recyclebin: emptied", "bin", b.Dir)
	}
	s.log.Info("recyclebin: freed", "freed_mb", freed>>20)
	return freed, nil
}

// Enforce applies the guard rails: first drop anything past the retention window, then, if the
// bins together are still over the size cap, delete oldest-first across all of them until
// they're under — skipping anything deleted in the last three days and the newest item, unless
// that item's disk is nearly full. A no-op when both caps are off. Unfinished copies a day old
// are cleared either way. Safe to call on a schedule.
func (s *Service) Enforce(ctx context.Context) {
	bins := s.current()
	if len(bins) == 0 {
		return
	}
	items, stale := s.walk(bins)
	for _, p := range stale {
		if err := os.Remove(p); err == nil {
			s.log.Info("recyclebin: cleared an unfinished copy left by an interrupted delete", "file", p)
		}
	}
	retentionDays, maxGB := s.retentionDays(ctx), s.maxGB(ctx)
	if (retentionDays == 0 && maxGB == 0) || len(items) == 0 {
		for _, b := range bins {
			pruneEmptyDirs(b.Dir)
		}
		return // no caps configured — keep forever
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

	// Size cap: if still over, delete oldest-first across every bin until under.
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
			// A nearly full disk outranks the hold: purge the protected items on that disk
			// too, oldest first, and say exactly what went.
			if total > limit && len(held) > 0 {
				low := map[int]bool{}
				for i, b := range bins {
					low[i] = s.lowOnDisk(b.Dir)
				}
				for _, e := range held {
					if total <= limit {
						break
					}
					if !low[e.bin] {
						continue
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

	for _, b := range bins {
		pruneEmptyDirs(b.Dir)
	}
	if removed > 0 {
		s.log.Info("recyclebin: enforced caps", "removed", removed, "freed_mb", freed>>20,
			"retention_days", retentionDays, "max_gb", maxGB)
	}
}

// pruneEmptyDirs clears out a bin's subfolders left empty after purging their files.
func pruneEmptyDirs(dir string) {
	kids, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, k := range kids {
		if !k.IsDir() {
			continue
		}
		sub := filepath.Join(dir, k.Name())
		if inner, err := os.ReadDir(sub); err == nil && len(inner) == 0 {
			_ = os.Remove(sub)
		}
	}
}
