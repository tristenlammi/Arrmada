package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed postgres/*.sql
var postgresMigrationsFS embed.FS

func postgresMigrations() fs.FS {
	sub, err := fs.Sub(postgresMigrationsFS, "postgres")
	if err != nil {
		panic(err)
	}
	return sub
}

var (
	ErrDatabaseInUse          = errors.New("another Arrmada database operation holds the schema lock")
	ErrMigrationBackupNeeded  = errors.New("PostgreSQL schema upgrade requires a successful pre-migration backup")
	ErrMigrationChanged       = errors.New("an applied PostgreSQL migration differs from this build")
	ErrPostgresSchemaOccupied = errors.New("PostgreSQL schema contains objects but is not an initialized Arrmada database")
)

// lockPostgresSchema pins one connection while holding a session advisory lock.
// The namespace includes the database and schema, so another application's
// schema is not locked. Failure/cancellation must never return a locked session
// to the pool: an unsuccessful unlock discards the underlying connection.
func lockPostgresSchema(ctx context.Context, db *Database) (*sql.Conn, func(), error) {
	if db.dialect != Postgres {
		return nil, nil, errors.New("PostgreSQL schema lock requested for another backend")
	}
	conn, err := db.pool.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	var key int64
	var locked bool
	err = conn.QueryRowContext(ctx, `SELECT hashtextextended('arrmada/schema/' || current_database() || '/' || current_schema(), 0)`).Scan(&key)
	if err == nil {
		err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked)
	}
	if err != nil || !locked {
		if err != nil {
			// The server may have acquired the lock even if cancellation hid
			// its reply. Discard the session rather than guessing it is clean.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("lock PostgreSQL schema: %w", err)
		}
		return nil, nil, ErrDatabaseInUse
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var unlocked bool
			if err := conn.QueryRowContext(cleanup, `SELECT pg_advisory_unlock($1)`, key).Scan(&unlocked); err != nil || !unlocked {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
			_ = conn.Close()
		})
	}
	return conn, release, nil
}

func postgresSchema(ctx context.Context, q Execer, migrations fs.FS) (SchemaStatus, error) {
	var st SchemaStatus
	names, err := listMigrations(migrations)
	if err != nil {
		return st, err
	}
	if len(names) > 0 {
		st.Latest = strings.TrimSuffix(names[len(names)-1], ".sql")
	}
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relname = 'schema_migrations' AND c.relkind = 'r'
	)`).Scan(&exists); err != nil {
		return st, err
	}
	applied := map[string]string{}
	if exists {
		rows, err := q.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
		if err != nil {
			return st, err
		}
		for rows.Next() {
			var version, checksum string
			if err := rows.Scan(&version, &checksum); err != nil {
				rows.Close()
				return st, err
			}
			applied[version] = checksum
			if version > st.Applied {
				st.Applied = version
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return st, err
		}
	}
	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		checksum, ok := applied[version]
		if !ok {
			st.Pending = append(st.Pending, version)
			continue
		}
		raw, err := fs.ReadFile(migrations, name)
		if err != nil {
			return st, err
		}
		if checksum != migrationChecksum(raw) {
			return st, fmt.Errorf("%w: %s; restore the matching build or investigate the migration history before proceeding", ErrMigrationChanged, version)
		}
		delete(applied, version)
	}
	for version := range applied {
		st.Unknown = append(st.Unknown, version)
	}
	sort.Strings(st.Unknown)
	return st, nil
}

func migrationChecksum(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }

// migratePostgres mirrors the SQLite policy: do not start older code on a newer
// schema, snapshot before upgrading an existing installation, and apply each
// immutable migration in its own transaction. The caller supplies the actual
// backend-specific backup; nil is never an implicit permission to skip one.
func migratePostgres(ctx context.Context, db *Database, migrations fs.FS, before func(context.Context, []string) error) error {
	conn, release, err := lockPostgresSchema(ctx, db)
	if err != nil {
		return err
	}
	defer release()
	return migratePostgresLocked(ctx, conn, migrations, before)
}

// migratePostgresLocked uses a lifecycle owner's already locked connection.
// Reacquiring the lock through a different connection would deadlock/reject the
// owner's own upgrade. Never call this without exclusive schema ownership.
func migratePostgresLocked(ctx context.Context, conn *sql.Conn, migrations fs.FS, before func(context.Context, []string) error) error {
	status, err := postgresSchema(ctx, conn, migrations)
	if err != nil {
		return err
	}
	if len(status.Unknown) > 0 {
		return fmt.Errorf("%w: PostgreSQL has unknown migrations %s; use the matching build or restore a pre-upgrade PostgreSQL backup offline", ErrNewerSchema, strings.Join(status.Unknown, ", "))
	}
	if len(status.Pending) == 0 {
		return nil
	}
	if status.Applied == "" {
		// Never adopt an arbitrary schema just because it lacks our metadata.
		// Include standalone functions/types, not only tables. An empty failed
		// bookkeeping table is ours only if it has the expected structure.
		var occupied bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_depend d
			JOIN pg_catalog.pg_namespace n ON d.refclassid = 'pg_catalog.pg_namespace'::regclass AND d.refobjid = n.oid
			LEFT JOIN pg_catalog.pg_class r ON d.classid = 'pg_catalog.pg_class'::regclass AND d.objid = r.oid
			LEFT JOIN pg_catalog.pg_type t ON d.classid = 'pg_catalog.pg_type'::regclass AND d.objid = t.oid
			LEFT JOIN pg_catalog.pg_constraint c ON d.classid = 'pg_catalog.pg_constraint'::regclass AND d.objid = c.oid
			LEFT JOIN pg_catalog.pg_class owner_table ON owner_table.oid = c.conrelid
			WHERE n.nspname = current_schema() AND NOT (
				COALESCE(r.relname IN ('schema_migrations', 'schema_migrations_pkey'), false)
				OR COALESCE(t.typname IN ('schema_migrations', '_schema_migrations'), false)
				OR COALESCE(owner_table.relname = 'schema_migrations', false)
			)
		)`).Scan(&occupied); err != nil {
			return err
		}
		if occupied {
			return ErrPostgresSchemaOccupied
		}
	} else {
		if before == nil {
			return ErrMigrationBackupNeeded
		}
		if err := before(ctx, append([]string(nil), status.Pending...)); err != nil {
			return fmt.Errorf("%w: %w; no migrations were applied", ErrMigrationBackupNeeded, err)
		}
	}
	for _, version := range status.Pending {
		raw, err := fs.ReadFile(migrations, version+".sql")
		if err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := applyPostgresMigration(ctx, tx, version, raw); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply PostgreSQL migration %s: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit PostgreSQL migration %s: %w", version, err)
		}
	}
	return nil
}

func applyPostgresMigration(ctx context.Context, tx *sql.Tx, version string, raw []byte) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY,
		checksum text NOT NULL,
		applied_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(raw)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)`, version, migrationChecksum(raw))
	return err
}
