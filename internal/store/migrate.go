package store

import (
	"bufio"
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// Migrations are plain .sql files applied in filename order. Name them with a
// zero-padded numeric prefix, e.g. 0001_init.sql, 0002_add_indexers.sql. Each one
// runs inside its own transaction, so never put BEGIN or COMMIT in the file.
//
// Rebuilding a table (changing a column's type or constraints, which ALTER TABLE
// can't do) follows SQLite's 12-step recipe (https://sqlite.org/lang_altertable.html):
//
//  1. CREATE TABLE <t>_new with the new shape.
//  2. INSERT INTO <t>_new (...) SELECT ... FROM <t>.
//  3. DROP TABLE <t>.
//  4. ALTER TABLE <t>_new RENAME TO <t>.
//  5. Recreate the indexes, triggers and views that belonged to <t>.
//
// Leave legacy_alter_table OFF (the default) so the rename keeps references intact.
//
// If any other table REFERENCES <t> (users, series, books, artists and albums today),
// the file's first line must be the directive
//
//	-- arrmada:foreign-keys-off
//
// Our connections run with foreign_keys=ON, and SQLite ignores PRAGMA foreign_keys
// inside a transaction, so without the directive step 3 is an implicit DELETE of
// every row in <t>: ON DELETE CASCADE children (sessions, audiobook progress,
// episodes...) silently vanish and the migration still "succeeds". With the
// directive the runner turns foreign keys off on a dedicated connection before the
// transaction, runs PRAGMA foreign_key_check before committing (any dangling
// reference rolls the whole migration back), and turns them on again before the
// connection goes back to the pool. "-- arrmada:foreign-keys=off" is accepted as
// the same directive; any other "-- arrmada:" line, or a directive that isn't the
// first line, stops the boot rather than being ignored. migrations_lint_test.go
// enforces all of this in CI.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// fkOffDirective opts a migration into the foreign-keys-off path described above.
const fkOffDirective = "-- arrmada:foreign-keys-off"

// embeddedMigrations is the production migration source, rooted so the .sql files
// sit at the top level the same way a test's fstest.MapFS does.
func embeddedMigrations() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		// fs.Sub only fails on an invalid path, and "migrations" is a constant.
		panic(err)
	}
	return sub
}

// runMigrations applies every migration in fsys that hasn't been recorded yet,
// each in its own transaction, recording success in schema_migrations.
func runMigrations(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	pend, err := pending(ctx, db, fsys)
	if err != nil {
		return err
	}
	return applyMigrations(ctx, db, fsys, pend, nil)
}

// listMigrations returns the .sql file names in fsys, in the order they apply.
func listMigrations(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// ensureMigrationsTable creates the bookkeeping table on a brand-new database.
func ensureMigrationsTable(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	return nil
}

// pending returns the migration file names in fsys not yet recorded in
// schema_migrations, in apply order. It is split out from applying them so a
// caller can act (take a snapshot, say) before anything changes.
func pending(ctx context.Context, db *sql.DB, fsys fs.FS) ([]string, error) {
	pend, _, err := pendingMigrations(ctx, db, fsys)
	return pend, err
}

// pendingMigrations is pending plus the newest version already applied ("" on a
// fresh database, which is how callers tell an install from an upgrade).
func pendingMigrations(ctx context.Context, db *sql.DB, fsys fs.FS) (pend []string, lastApplied string, err error) {
	if err := ensureMigrationsTable(ctx, db); err != nil {
		return nil, "", err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return nil, "", err
	}
	for v := range applied {
		if v > lastApplied {
			lastApplied = v
		}
	}
	names, err := listMigrations(fsys)
	if err != nil {
		return nil, "", err
	}
	for _, name := range names {
		if !applied[strings.TrimSuffix(name, ".sql")] {
			pend = append(pend, name)
		}
	}
	return pend, lastApplied, nil
}

// applyMigrations applies the named migrations in order, stopping at the first
// failure. log may be nil.
func applyMigrations(ctx context.Context, db *sql.DB, fsys fs.FS, names []string, log *slog.Logger) error {
	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		start := time.Now()
		fkOff, err := migrationDirective(string(body))
		if err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if fkOff {
			err = applyFKOff(ctx, db, version, string(body))
		} else {
			err = applyOne(ctx, db, version, string(body))
		}
		if err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if log != nil {
			log.Info("migration applied", "version", version, "foreign_keys_off", fkOff, "duration", time.Since(start).Round(time.Millisecond).String())
		}
	}
	return nil
}

// fkOffAliases are other spellings of fkOffDirective that mean the same thing.
// The roadmap wrote it both ways, and a migration author following either must
// get the safe path.
var fkOffAliases = []string{"-- arrmada:foreign-keys=off"}

// directivePrefix marks a comment line as an instruction to this runner.
const directivePrefix = "-- arrmada:"

// migrationDirective reports whether the file opts into the foreign-keys-off path,
// which it does when its first non-blank line is the directive. A directive that is
// misspelled, or not on the first line, is an error rather than being ignored:
// ignoring it would quietly run a parent-table rebuild the cascading way.
func migrationDirective(body string) (fkOff bool, err error) {
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		isDirective := strings.HasPrefix(strings.ToLower(line), directivePrefix)
		if !first {
			if isDirective {
				return false, fmt.Errorf("directive %q must be the first line of the file", line)
			}
			continue
		}
		first = false
		if !isDirective {
			continue
		}
		if line == fkOffDirective {
			fkOff = true
			continue
		}
		known := false
		for _, a := range fkOffAliases {
			known = known || line == a
		}
		if !known {
			return false, fmt.Errorf("unknown directive %q (did you mean %q?)", line, fkOffDirective)
		}
		fkOff = true
	}
	return fkOff, sc.Err()
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func applyOne(ctx context.Context, db *sql.DB, version, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
		return err
	}
	return tx.Commit()
}

// maxFKViolations caps how many dangling references an error message lists.
const maxFKViolations = 20

// applyFKOff runs a table-rebuild migration with foreign keys switched off, so
// dropping the old parent table can't cascade into its children. The pragma has to
// be set outside a transaction, so it pins one pooled connection for the whole job.
// That connection must never go back to the pool with foreign keys still off: if
// they can't be switched back on, it is thrown away instead.
func applyFKOff(ctx context.Context, db *sql.DB, version, body string) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if rerr := restoreForeignKeys(conn); rerr != nil {
			// Raw returning ErrBadConn makes database/sql close the connection
			// rather than reuse it.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			if err == nil {
				err = rerr
			}
			return
		}
		_ = conn.Close()
	}()

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("switch foreign keys off: %w", err)
	}
	if on, err := foreignKeysOn(ctx, conn); err != nil {
		return err
	} else if on {
		return errors.New("foreign keys are still on after PRAGMA foreign_keys=OFF")
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if err := foreignKeyCheck(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
		return err
	}
	return tx.Commit()
}

// restoreForeignKeys switches foreign keys back on and confirms it took. It uses
// its own short context so a cancelled migration context can't leave the
// connection half-restored.
func restoreForeignKeys(conn *sql.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("switch foreign keys back on: %w", err)
	}
	on, err := foreignKeysOn(ctx, conn)
	if err != nil {
		return err
	}
	if !on {
		return errors.New("foreign keys are still off after PRAGMA foreign_keys=ON")
	}
	return nil
}

func foreignKeysOn(ctx context.Context, conn *sql.Conn) (bool, error) {
	var v int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&v); err != nil {
		return false, fmt.Errorf("read foreign_keys: %w", err)
	}
	return v == 1, nil
}

// foreignKeyCheck fails when the migration left any row pointing at a parent row
// that no longer exists, listing the first few so the cause is obvious from the log.
func foreignKeyCheck(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()

	var found []string
	total := 0
	for rows.Next() {
		var (
			table, parent string
			rowid         sql.NullInt64
			fkid          int
		)
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return fmt.Errorf("foreign_key_check: %w", err)
		}
		total++
		if len(found) < maxFKViolations {
			id := "-"
			if rowid.Valid {
				id = fmt.Sprint(rowid.Int64)
			}
			found = append(found, fmt.Sprintf("%s rowid %s -> %s (fk %d)", table, id, parent, fkid))
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	if total > 0 {
		return fmt.Errorf("foreign_key_check found %d dangling reference(s), nothing was applied: %s",
			total, strings.Join(found, "; "))
	}
	return nil
}
