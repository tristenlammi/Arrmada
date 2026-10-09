// Package store owns Arrmada's persistence: a SQLite database (pure-Go driver,
// so the binary stays cgo-free and cross-compiles cleanly) with an embedded
// migration runner. PostgreSQL support arrives in a later phase behind the same
// surface.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the database connection pool.
type Store struct {
	db      *sql.DB
	dataDir string
	dbPath  string

	// snapMu serialises Snapshot: two copies picking a name in the same second
	// would otherwise share a .tmp file and clobber each other.
	snapMu sync.Mutex
}

// Options tunes OpenWith. The zero value is what Open uses.
type Options struct {
	// Log receives migration and snapshot progress. Nil discards it.
	Log *slog.Logger

	// SkipMigrationSnapshot upgrades an existing database without first copying
	// it to the backups folder. It is the escape hatch for a disk too full to hold
	// the copy, nothing more (ARRMADA_SKIP_MIGRATION_SNAPSHOT).
	SkipMigrationSnapshot bool

	// AllowNewerSchema starts on a database a newer build has upgraded, which is
	// otherwise refused (ARRMADA_ALLOW_NEWER_SCHEMA). The old code then runs against
	// tables and columns it doesn't know about; it is a last resort, logged loudly.
	AllowNewerSchema bool

	// BeforeMigrate, when set, runs once with the pending migration file names
	// before any of them is applied, and only when there is at least one. An error
	// aborts Open with nothing applied.
	BeforeMigrate func(ctx context.Context, db *sql.DB, pending []string) error

	// migrations replaces the embedded migration set. Tests only.
	migrations fs.FS
}

// preMigrateKeep is how many pre-migrate snapshots are kept; older ones are
// deleted after each upgrade that succeeds.
const preMigrateKeep = 5

// Open ensures the data directory exists, opens the SQLite database with sane
// pragmas (WAL, foreign keys, busy timeout), verifies connectivity, and applies
// any pending migrations, snapshotting an existing database first.
func Open(dataDir string) (*Store, error) {
	return OpenWith(dataDir, Options{})
}

// OpenWith is Open with options.
func OpenWith(dataDir string, opt Options) (*Store, error) {
	log := opt.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %q: %w", dataDir, err)
	}

	fsys := opt.migrations
	if fsys == nil {
		fsys = embeddedMigrations()
	}
	// A restore staged from the Backups card or the CLI is put in place now, before
	// anything has the database open. It never stops the boot; see restore.go.
	applyPendingRestore(dataDir, versionsIn(fsys), log)

	st := &Store{dataDir: dataDir, dbPath: filepath.Join(dataDir, "arrmada.db")}
	db, err := openDB(st.dbPath)
	if err != nil {
		return nil, err
	}
	st.db = db

	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	// The snapshot and the migrations get no deadline: on a large database either
	// can legitimately take longer than a ping should, and failing the boot halfway
	// through an upgrade helps nobody.
	if err := st.migrate(context.Background(), opt, log); err != nil {
		_ = db.Close()
		return nil, err
	}
	return st, nil
}

// migrate applies pending migrations. On an existing database (anything already
// applied) it first takes a pre-migrate snapshot, and changes nothing if that
// copy can't be made.
func (s *Store) migrate(ctx context.Context, opt Options, log *slog.Logger) error {
	fsys := opt.migrations
	if fsys == nil {
		fsys = embeddedMigrations()
	}
	pend, last, err := pendingMigrations(ctx, s.db, fsys)
	if err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	// Before anything else changes: an older build must not snapshot, migrate or run on
	// a database a newer one has already upgraded.
	if err := s.refuseNewerSchema(ctx, fsys, opt, log); err != nil {
		return err
	}
	if len(pend) == 0 {
		return nil
	}
	// A fresh install has nothing worth copying.
	fresh := last == ""
	newest := strings.TrimSuffix(pend[len(pend)-1], ".sql")

	snapshotted := false
	if !fresh {
		var err error
		if snapshotted, err = s.snapshotBeforeMigrate(ctx, opt, log, last, newest, len(pend)); err != nil {
			return err
		}
	}

	if opt.BeforeMigrate != nil {
		if err := opt.BeforeMigrate(ctx, s.db, pend); err != nil {
			return fmt.Errorf("before migrations: %w", err)
		}
	}

	// A fresh install applies every migration; one summary line says that better
	// than ninety.
	stepLog := log
	if fresh {
		stepLog = nil
	}
	start := time.Now()
	if err := applyMigrations(ctx, s.db, fsys, pend, stepLog); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	// Prune only once the upgrade has gone through. A migration that keeps failing
	// sends the container round a restart loop, and every boot snapshots the
	// half-upgraded database again; pruning then would soon delete the one copy
	// from before the upgrade started. Until an upgrade succeeds every snapshot is
	// kept, and the free-space check stops them filling the disk.
	if snapshotted {
		s.prunePreMigrate(log)
	}
	if fresh {
		log.Info("database created", "migrations", len(pend), "version", newest,
			"duration", time.Since(start).Round(time.Millisecond).String())
	}
	return nil
}

// snapshotBeforeMigrate copies the database aside before an upgrade, so a
// migration that commits a mistake has something to roll back to. It reports
// whether a snapshot was taken.
func (s *Store) snapshotBeforeMigrate(ctx context.Context, opt Options, log *slog.Logger, from, to string, count int) (bool, error) {
	dir := BackupsDir(s.dataDir)
	if opt.SkipMigrationSnapshot {
		log.Warn("ARRMADA_SKIP_MIGRATION_SNAPSHOT is set: upgrading the database WITHOUT a snapshot, so there is nothing to roll back to if this goes wrong",
			"from", from, "to", to, "count", count)
		return false, nil
	}

	start := time.Now()
	path, err := s.Snapshot(ctx, BackupPreMigrate)
	if err != nil {
		return false, fmt.Errorf("couldn't snapshot the database before upgrading it from %s to %s: %w — nothing was changed. "+
			"Free space in %s or set ARRMADA_SKIP_MIGRATION_SNAPSHOT=1 to upgrade without one", from, to, err, dir)
	}
	var size int64
	if fi, err := os.Stat(path); err == nil {
		size = fi.Size()
	}
	log.Info("database snapshot taken before migrations",
		"path", path, "size", formatBytes(uint64(size)),
		"duration", time.Since(start).Round(time.Millisecond).String(),
		"from", from, "to", to, "count", count)
	return true, nil
}

// prunePreMigrate trims the pre-migrate snapshots to the newest preMigrateKeep.
// Pruning is housekeeping; failing it mustn't fail the upgrade that just finished.
func (s *Store) prunePreMigrate(log *slog.Logger) {
	dir := BackupsDir(s.dataDir)
	if removed, err := PruneBackups(dir, BackupPreMigrate, preMigrateKeep); err != nil {
		log.Warn("couldn't prune old pre-migrate snapshots", "dir", dir, "err", err)
	} else if len(removed) > 0 {
		log.Info("pruned old pre-migrate snapshots", "removed", removed)
	}
}

// OpenNoMigrate opens an existing database with the same settings Open uses, but
// never creates, migrates or snapshots it. It is for the command-line tools, which
// run beside a live server: they must neither upgrade the schema under it nor make
// an empty database where the real one was expected.
func OpenNoMigrate(dataDir string) (*Store, error) {
	dbPath := filepath.Join(dataDir, "arrmada.db")
	fi, err := os.Stat(dbPath)
	if err != nil {
		return nil, fmt.Errorf("no database at %s: %w", dbPath, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s isn't a database file", dbPath)
	}
	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return &Store{db: db, dataDir: dataDir, dbPath: dbPath}, nil
}

// openDB opens the pool every Store uses: WAL, foreign keys on, busy timeout, and
// transactions that BEGIN IMMEDIATE. A deferred transaction asks for the write lock only
// at its first write, and if another connection committed since it first read, SQLite
// refuses with BUSY_SNAPSHOT, which busy_timeout can't wait out. Taking the lock at BEGIN
// makes a read-then-write transaction wait its turn instead.
func openDB(dbPath string) (*sql.DB, error) {
	dsn := "file:" + dbPath +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_txlock=immediate"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite is a single-writer; keep the pool small and predictable.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Hour)
	return db, nil
}

// DataDir is the folder holding the database, its backups and the restore marker.
func (s *Store) DataDir() string { return s.dataDir }

// DB exposes the underlying pool for repositories built on top of the store.
func (s *Store) DB() *sql.DB { return s.db }

// Ping checks database connectivity (used by health checks).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close closes the connection pool.
func (s *Store) Close() error { return s.db.Close() }
