package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
)

// Shipped migration files are never renamed or removed. That is what makes the check
// below sound: a version in schema_migrations that this binary doesn't carry can only
// have been applied by a newer build.

// LatestMigration is the newest migration built into this binary (e.g.
// "0111_convert_history"): the schema it writes, and the newest one it understands.
func LatestMigration() string {
	names, err := listMigrations(embeddedMigrations())
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[len(names)-1], ".sql")
}

// ErrNewerSchema means the database was upgraded by a newer Arrmada than this one.
var ErrNewerSchema = errors.New("the database was upgraded by a newer Arrmada")

// NewerSchemaError is the refusal to start on a database with migrations this build
// doesn't have. Its message is what the owner reads in the log, so it names the way out.
type NewerSchemaError struct {
	Unknown []string // versions the database has that this build doesn't
	Latest  string   // this build's newest migration
	DataDir string
}

func (e *NewerSchemaError) Error() string {
	shown := e.Unknown
	if len(shown) > 5 {
		shown = append(append([]string(nil), shown[:5]...), fmt.Sprintf("and %d more", len(e.Unknown)-5))
	}
	return fmt.Sprintf("%v: it has migrations this build doesn't know (%s; this build stops at %s). "+
		"Running older code on it could damage it, so nothing was changed. "+
		"Update Arrmada again (./update.sh), or roll back together with the database from before that upgrade "+
		"(./update.sh --rollback --with-db; the copies are in %s). "+
		"Set ARRMADA_ALLOW_NEWER_SCHEMA=1 only to start anyway",
		ErrNewerSchema, strings.Join(shown, ", "), e.Latest, BackupsDir(e.DataDir))
}

func (e *NewerSchemaError) Unwrap() error { return ErrNewerSchema }

// refuseNewerSchema stops the boot when a newer build has already upgraded this
// database. Without it the old code would silently skip the versions it doesn't
// know and run against a schema it doesn't understand, which is how a rollback
// quietly damages data.
func (s *Store) refuseNewerSchema(ctx context.Context, fsys fs.FS, opt Options, log *slog.Logger) error {
	unknown, err := unknownMigrations(ctx, s.db, fsys)
	if err != nil {
		return fmt.Errorf("check schema version: %w", err)
	}
	if len(unknown) == 0 {
		return nil
	}
	latest := ""
	if names, err := listMigrations(fsys); err == nil && len(names) > 0 {
		latest = strings.TrimSuffix(names[len(names)-1], ".sql")
	}
	if !opt.AllowNewerSchema {
		return &NewerSchemaError{Unknown: unknown, Latest: latest, DataDir: s.dataDir}
	}
	log.Warn("ARRMADA_ALLOW_NEWER_SCHEMA is set: starting on a database a NEWER Arrmada upgraded. "+
		"This build doesn't know its newest tables and columns; update again or restore a backup from before that upgrade as soon as you can",
		"unknown_migrations", unknown, "this_build", latest)
	return nil
}

// unknownMigrations returns the versions recorded in schema_migrations that fsys
// doesn't contain, in order.
func unknownMigrations(ctx context.Context, db *sql.DB, fsys fs.FS) ([]string, error) {
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}
	names, err := listMigrations(fsys)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		delete(applied, strings.TrimSuffix(name, ".sql"))
	}
	unknown := make([]string, 0, len(applied))
	for v := range applied {
		unknown = append(unknown, v)
	}
	sort.Strings(unknown)
	return unknown, nil
}

// SchemaStatus compares a database's applied migrations with this build's.
type SchemaStatus struct {
	Applied string   // newest migration the database has ("" when it has none)
	Latest  string   // newest migration built into this binary
	Pending []string // this build's migrations the database doesn't have yet
	Unknown []string // the database's migrations this build doesn't have (a newer build applied them)
}

// Schema reports how the database's schema compares with this build's, without
// changing anything: a database with no schema_migrations table has nothing applied.
func (s *Store) Schema(ctx context.Context) (SchemaStatus, error) {
	st := SchemaStatus{Latest: LatestMigration()}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&n); err != nil {
		return st, err
	}
	applied := map[string]bool{}
	if n > 0 {
		var err error
		if applied, err = appliedVersions(ctx, s.db); err != nil {
			return st, err
		}
	}
	names, err := listMigrations(embeddedMigrations())
	if err != nil {
		return st, err
	}
	known := map[string]bool{}
	for _, name := range names {
		v := strings.TrimSuffix(name, ".sql")
		known[v] = true
		if !applied[v] {
			st.Pending = append(st.Pending, v)
		}
	}
	for v := range applied {
		if v > st.Applied {
			st.Applied = v
		}
		if !known[v] {
			st.Unknown = append(st.Unknown, v)
		}
	}
	sort.Strings(st.Unknown)
	return st, nil
}
