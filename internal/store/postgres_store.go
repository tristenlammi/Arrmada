package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
)

func openPostgresStore(ctx context.Context, dataDir string, opt Options) (*Store, error) {
	if opt.SkipMigrationSnapshot || opt.AllowNewerSchema || opt.BeforeMigrate != nil || opt.migrations != nil {
		return nil, errors.New("SQLite migration overrides are not supported for PostgreSQL; upgrades require a verified backup and older builds must restore offline")
	}
	if err := opt.Postgres.validate(); err != nil {
		return nil, err
	}
	if marker, err := PendingRestore(dataDir); err != nil || marker != nil {
		return nil, errors.New("resolve the staged SQLite restore explicitly before selecting PostgreSQL; no database was changed")
	}
	owner, err := openPostgresStartupOwner(ctx, opt.Postgres)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = owner.Close()
		}
	}()
	st := &Store{pg: owner.control, owner: owner, pgOpt: opt.Postgres, dataDir: dataDir}
	log := opt.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// Establish native backup availability before a new installation can start
	// accumulating data it would later discover it cannot back up.
	var version int
	if err := owner.conn.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&version); err != nil {
		return nil, err
	}
	tools := postgresTools(opt.Postgres)
	if _, err := postgresClientEnv(opt.Postgres); err != nil {
		return nil, err
	}
	for _, path := range []string{tools.dump, tools.restore} {
		major, err := postgresClientMajor(ctx, path)
		if err != nil {
			return nil, err
		}
		if major < version/10000 {
			return nil, errors.New("PostgreSQL backup clients must not be older than the server")
		}
	}
	if err := mkPrivateDir(dataDir); err != nil {
		return nil, err
	}
	if err := checkPostgresBackupDir(BackupsDir(dataDir)); err != nil {
		return nil, err
	}
	if err := owner.ensureStartupSchema(ctx); err != nil {
		return nil, err
	}
	snapshotted := false
	if err := migratePostgresLocked(ctx, owner.conn, postgresMigrations(), func(ctx context.Context, pending []string) error {
		path := freePostgresBackupPath(BackupsDir(dataDir), BackupPreMigrate, time.Now())
		if err := postgresSnapshotLockedChecked(ctx, owner.control, opt.Postgres, tools, path, func() error { return owner.conn.PingContext(ctx) }); err != nil {
			return err
		}
		snapshotted = true
		log.Info("PostgreSQL backup taken before migrations", "path", path, "pending", pending)
		return nil
	}); err != nil {
		return nil, err
	}
	app, err := owner.activate(ctx)
	if err != nil {
		return nil, err
	}
	st.pg = app
	if snapshotted {
		if _, err := PruneBackupsFor(Postgres, BackupsDir(dataDir), BackupPreMigrate, preMigrateKeep); err != nil {
			log.Warn("could not prune PostgreSQL pre-migration backups", "err", err)
		}
	}
	ok = true
	return st, nil
}

func checkPostgresBackupDir(dir string) error {
	if err := mkPrivateDir(dir); err != nil {
		return err
	}
	if err := makePrivate(dir); err != nil {
		return fmt.Errorf("PostgreSQL backup directory must be private to the application user: %w", err)
	}
	if u, ok := freeSpace(dir); ok && u.FreeBytes < postgresBackupReserve+1024*1024 {
		return ErrNoSpace
	}
	f, err := os.CreateTemp(dir, ".postgres-publication-check-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Close(); err != nil {
		return err
	}
	link := f.Name() + ".link"
	if err := os.Link(f.Name(), link); err != nil {
		return errors.New("PostgreSQL backup directory must support atomic hard-link publication without overwriting existing backups")
	}
	return os.Remove(link)
}

// OpenPostgresNoMigrate is for maintenance reads/backups alongside a running
// server. It never creates tables, applies migrations, or touches arrmada.db.
func OpenPostgresNoMigrate(ctx context.Context, dataDir string, opt PostgresOptions) (*Store, error) {
	db, err := openPostgresPoolAfterConnect(ctx, opt, func(ctx context.Context, conn *pgx.Conn) error {
		var locked bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock_shared(hashtextextended('arrmada/schema/' || current_database() || '/' || $1::text, 0))`, opt.Schema).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return ErrDatabaseInUse
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Store{pg: db, pgOpt: opt, dataDir: dataDir}, nil
}

func freePostgresBackupPath(dir string, kind BackupKind, at time.Time) string {
	for i := 0; i < 60; i++ {
		path := filepath.Join(dir, BackupNameFor(Postgres, kind, at))
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return path
		}
		at = at.Add(time.Second)
	}
	return filepath.Join(dir, BackupNameFor(Postgres, kind, at))
}

func (s *Store) postgresSnapshot(ctx context.Context, kind BackupKind) (string, error) {
	// Shared schema lock admits online backups beside the application, but
	// fences upgrades/recovery for the entire native dump/verification. A
	// dedicated pinned session protects even a CLI backup after app shutdown.
	conn, err := s.pg.pool.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = conn.Close()
	}()
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock_shared(hashtextextended('arrmada/schema/' || current_database() || '/' || current_schema(), 0))`).Scan(&locked); err != nil {
		return "", err
	}
	if !locked {
		return "", ErrDatabaseInUse
	}
	status, err := postgresSchema(ctx, conn, postgresMigrations())
	if err != nil {
		return "", err
	}
	if status.Applied == "" {
		return "", errors.New("PostgreSQL schema is not an initialized Arrmada database")
	}
	// Query/size inspection must use the pinned session too: a one-connection
	// application pool must not deadlock while its only session is pinned.
	var size int64
	var version int
	if err := conn.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&version); err != nil {
		return "", err
	}
	tools := postgresTools(s.pgOpt)
	for _, tool := range []string{tools.dump, tools.restore} {
		major, err := postgresClientMajor(ctx, tool)
		if err != nil {
			return "", err
		}
		if major < version/10000 {
			return "", errors.New("PostgreSQL backup clients must not be older than the server")
		}
	}
	if size, err = postgresSchemaBytes(ctx, conn); err != nil {
		return "", err
	}
	dir := BackupsDir(s.dataDir)
	if err := checkPostgresBackupDir(dir); err != nil {
		return "", err
	}
	if u, ok := freeSpace(dir); ok && u.FreeBytes < uint64(float64(size)*spaceHeadroom)+1024*1024 {
		return "", ErrNoSpace
	}
	path := freePostgresBackupPath(dir, kind, time.Now())
	if err := postgresArchiveToChecked(ctx, s.pgOpt, tools, path, func() error { return conn.PingContext(ctx) }); err != nil {
		return "", fmt.Errorf("PostgreSQL snapshot: %w", err)
	}
	return path, nil
}
