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
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the database connection pool.
type Store struct {
	db *sql.DB
}

// Options tunes OpenWith. The zero value is what Open uses.
type Options struct {
	// BeforeMigrate, when set, runs once with the pending migration file names
	// before any of them is applied, and only when there is at least one. An error
	// aborts Open with nothing applied.
	BeforeMigrate func(ctx context.Context, db *sql.DB, pending []string) error

	// migrations replaces the embedded migration set. Tests only.
	migrations fs.FS
}

// Open ensures the data directory exists, opens the SQLite database with sane
// pragmas (WAL, foreign keys, busy timeout), verifies connectivity, and applies
// any pending migrations.
func Open(dataDir string) (*Store, error) {
	return OpenWith(dataDir, Options{})
}

// OpenWith is Open with options.
func OpenWith(dataDir string, opt Options) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %q: %w", dataDir, err)
	}

	db, err := openDB(filepath.Join(dataDir, "arrmada.db"))
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	fsys := opt.migrations
	if fsys == nil {
		fsys = embeddedMigrations()
	}
	pend, err := pending(ctx, db, fsys)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	if len(pend) > 0 && opt.BeforeMigrate != nil {
		if err := opt.BeforeMigrate(ctx, db, pend); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("before migrations: %w", err)
		}
	}
	if err := applyMigrations(ctx, db, fsys, pend, nil); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return &Store{db: db}, nil
}

// openDB opens the pool every Store uses: WAL, foreign keys on, busy timeout.
func openDB(dbPath string) (*sql.DB, error) {
	dsn := "file:" + dbPath +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)"

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

// DB exposes the underlying pool for repositories built on top of the store.
func (s *Store) DB() *sql.DB { return s.db }

// Ping checks database connectivity (used by health checks).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close closes the connection pool.
func (s *Store) Close() error { return s.db.Close() }
