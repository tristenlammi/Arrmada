// Package backup keeps regular copies of Arrmada's database: a nightly one on a schedule,
// a manual one on request, and the safety copies other features take before something
// they can't undo (deleting a user). Every copy is the store's checked VACUUM INTO
// snapshot, kept next to the database under <data>/backups with a per-kind retention.
//
// The copies sit on the database's own disk, so they protect against a bad update,
// corruption or an accidental delete — not against losing the disk. Nothing here ever
// reads or logs what a backup contains beyond its schema version.
package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Settings keys. The Backups card (SAFE-12) edits them; until then the defaults apply.
const (
	KeyEnabled     = "backup_enabled"      // nightly backups on/off (default on)
	KeyHour        = "backup_hour"         // local hour the nightly is due from (default 4)
	KeyKeepNightly = "backup_keep_nightly" // how many nightlies to keep (default 7)
)

const (
	defaultHour        = 4
	defaultKeepNightly = 7
)

// keep is each kind's retention. pre-migrate is the store's (it prunes its own, 5) and is
// never pruned here; nightly comes from settings.
var keep = map[store.BackupKind]int{
	store.BackupManual:             10,
	store.BackupPreRestore:         3,
	store.BackupPreDeleteUser:      3,
	store.BackupPreDeleteEmptyUser: 3,
	store.BackupUploaded:           3,
}

// Settings is the slice of the settings service backups read.
type Settings interface {
	Get(ctx context.Context, key, def string) string
	GetBool(ctx context.Context, key string, def bool) bool
}

// Backup is one database copy on disk.
type Backup struct {
	Name          string           `json:"name"`
	Kind          store.BackupKind `json:"kind"`
	SizeBytes     int64            `json:"size_bytes"`
	CreatedAt     time.Time        `json:"created_at"`
	SchemaVersion string           `json:"schema_version"` // newest migration it holds; "" if unreadable
}

// Service makes, lists, prunes and deletes database backups.
type Service struct {
	store    *store.Store
	dir      string
	settings Settings
	log      *slog.Logger
	now      func() time.Time

	mu       sync.Mutex
	versions map[string]string // schema version by file name; backups never change once written
}

// New wires the service for st; its backups live wherever st's snapshots go.
func New(st *store.Store, set Settings, log *slog.Logger) *Service {
	return &Service{store: st, dir: st.BackupsDir(), settings: set, log: log, now: time.Now, versions: map[string]string{}}
}

// Dir is the backups folder.
func (s *Service) Dir() string { return s.dir }

// ErrBadName means a name that isn't one of our backup files (a path, "..", another file).
var ErrBadName = errors.New("not a backup name")

// Create takes a backup of kind and prunes that kind to its retention. pre-migrate copies
// belong to the store's upgrade path and can't be made here.
func (s *Service) Create(ctx context.Context, kind store.BackupKind) (Backup, error) {
	if kind == store.BackupPreMigrate {
		return Backup{}, fmt.Errorf("pre-migrate backups are taken by the upgrade itself")
	}
	if _, _, ok := store.ParseBackupName(store.BackupName(kind, time.Now())); !ok {
		return Backup{}, fmt.Errorf("invalid backup kind %q", kind)
	}
	start := time.Now()
	path, err := s.store.Snapshot(ctx, kind)
	if err != nil {
		return Backup{}, err
	}
	b, err := s.describe(ctx, filepath.Base(path))
	if err != nil {
		return Backup{}, err
	}
	s.log.Info("database backup taken", "kind", kind, "name", b.Name, "size_bytes", b.SizeBytes, "duration", time.Since(start).Round(time.Millisecond))
	if removed, err := store.PruneBackups(s.dir, kind, s.keepFor(ctx, kind)); err != nil {
		s.log.Warn("pruning old backups failed", "kind", kind, "err", err)
	} else if len(removed) > 0 {
		s.log.Info("pruned old backups", "kind", kind, "removed", len(removed))
		s.forget(removed)
	}
	return b, nil
}

func (s *Service) keepFor(ctx context.Context, kind store.BackupKind) int {
	if kind == store.BackupNightly {
		return s.intSetting(ctx, KeyKeepNightly, defaultKeepNightly, 1, 365)
	}
	if n, ok := keep[kind]; ok {
		return n
	}
	return 3
}

func (s *Service) intSetting(ctx context.Context, key string, def, lo, hi int) int {
	if s.settings == nil {
		return def
	}
	n, err := strconv.Atoi(s.settings.Get(ctx, key, strconv.Itoa(def)))
	if err != nil || n < lo || n > hi {
		return def
	}
	return n
}

// List returns every backup in the folder, newest first. Files that aren't ours are skipped.
func (s *Service) List(ctx context.Context) ([]Backup, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Backup{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Backup{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if _, _, ok := store.ParseBackupName(e.Name()); !ok {
			continue
		}
		b, err := s.describe(ctx, e.Name())
		if err != nil {
			continue // vanished between the listing and the stat (pruned meanwhile)
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// describe builds a Backup for an existing file with a valid name.
func (s *Service) describe(ctx context.Context, name string) (Backup, error) {
	kind, at, ok := store.ParseBackupName(name)
	if !ok {
		return Backup{}, ErrBadName
	}
	fi, err := os.Stat(filepath.Join(s.dir, name))
	if err != nil {
		return Backup{}, err
	}
	return Backup{Name: name, Kind: kind, SizeBytes: fi.Size(), CreatedAt: at, SchemaVersion: s.schemaVersion(ctx, name)}, nil
}

// schemaVersion reads the newest applied migration from a backup, read-only, once per file.
func (s *Service) schemaVersion(ctx context.Context, name string) string {
	s.mu.Lock()
	v, ok := s.versions[name]
	s.mu.Unlock()
	if ok {
		return v
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(s.dir, name)+"?mode=ro&immutable=1")
	if err != nil {
		return ""
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var ver sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&ver); err != nil {
		return "" // not cached: a transient failure shouldn't stick
	}
	s.mu.Lock()
	s.versions[name] = ver.String
	s.mu.Unlock()
	return ver.String
}

func (s *Service) forget(names []string) {
	s.mu.Lock()
	for _, n := range names {
		delete(s.versions, n)
	}
	s.mu.Unlock()
}

// Delete removes one backup. Only exact backup names are accepted, so this can never
// reach the live database or anything outside the backups folder.
func (s *Service) Delete(name string) error {
	if filepath.Base(name) != name {
		return ErrBadName
	}
	if _, _, ok := store.ParseBackupName(name); !ok {
		return ErrBadName
	}
	if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
		return err
	}
	s.forget([]string{name})
	return nil
}

// newest returns when the newest backup of kind was taken (zero when there is none).
func (s *Service) newest(kind store.BackupKind) time.Time {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return time.Time{}
	}
	var latest time.Time
	for _, e := range entries {
		if k, at, ok := store.ParseBackupName(e.Name()); ok && k == kind && at.After(latest) {
			latest = at
		}
	}
	return latest
}

// dueNightly says whether the nightly backup should run now, given when the newest one
// was taken. It runs at once when there is none or the newest is over 36 hours old (a
// long downtime); otherwise once the newest is over 20 hours old and the local clock is
// past the configured hour. The 20-hour floor is what stops restarts from making extras.
func dueNightly(newest, now time.Time, hour int) bool {
	if newest.IsZero() {
		return true
	}
	age := now.Sub(newest)
	if age > 36*time.Hour {
		return true
	}
	return age > 20*time.Hour && now.Hour() >= hour
}

// Enabled reports whether nightly backups are switched on.
func (s *Service) Enabled(ctx context.Context) bool {
	return s.settings == nil || s.settings.GetBool(ctx, KeyEnabled, true)
}

// RunNightly is the hourly scheduler task: it takes the nightly backup when one is due.
func (s *Service) RunNightly(ctx context.Context) error {
	if !s.Enabled(ctx) {
		return nil
	}
	hour := s.intSetting(ctx, KeyHour, defaultHour, 0, 23)
	if !dueNightly(s.newest(store.BackupNightly), s.now(), hour) {
		return nil
	}
	if _, err := s.Create(ctx, store.BackupNightly); err != nil {
		return fmt.Errorf("nightly database backup: %w", err)
	}
	return nil
}

// HealthWarning is the Dashboard's line when nightly backups have stopped: they're on,
// the app has been up long enough to have made one, and the newest is over 48 hours old.
// "" when all is well.
func (s *Service) HealthWarning(ctx context.Context, uptime time.Duration) string {
	if !s.Enabled(ctx) || uptime < 24*time.Hour {
		return ""
	}
	newest := s.newest(store.BackupNightly)
	if newest.IsZero() {
		return "No nightly database backup has been made yet — check the log for why (often a full disk)."
	}
	age := s.now().Sub(newest)
	if age <= 48*time.Hour {
		return ""
	}
	return fmt.Sprintf("No database backup in %d days — check the log for why (often a full disk).", int(age.Hours()/24))
}
