package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// PostgresOptions describes access to a dedicated schema, not permission to
// administer its database. Runtime startup can create a missing schema when the
// role has database-level CREATE; maintenance/recovery never do so. URL is secret.
type PostgresOptions struct {
	URL            string `json:"-"`
	Schema         string
	MaxConnections int
	// BinDir optionally selects a directory containing pg_dump/pg_restore.
	BinDir string
}

var postgresSchemaName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

func (o PostgresOptions) validate() error {
	if !postgresSchemaName.MatchString(o.Schema) || o.Schema == "public" ||
		o.Schema == "information_schema" || strings.HasPrefix(o.Schema, "pg_") {
		return errors.New("PostgreSQL requires a dedicated schema with a lowercase SQL identifier (not public, information_schema or pg_*)")
	}
	// Requiring an explicit URL avoids accidentally connecting to a database
	// selected by libpq environment defaults. Never echo a malformed secret.
	u, err := url.Parse(o.URL)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.Hostname() == "" || u.User == nil || u.User.Username() == "" || strings.Trim(u.Path, "/") == "" || u.Fragment != "" {
		return errors.New("PostgreSQL requires a postgres:// URL with an explicit host, user and database")
	}
	if o.MaxConnections < 0 || o.MaxConnections > 100 {
		return errors.New("PostgreSQL maximum connections must be 0 (default) or between 1 and 100")
	}
	return nil
}

// openPostgresPool establishes the connection boundary only. Schema migrations
// belong to Store's lifecycle, not to a connector or individual repository.
// In particular this never creates a schema or falls back to SQLite.
func openPostgresPool(ctx context.Context, opt PostgresOptions) (*Database, error) {
	return openPostgresPoolAfterConnect(ctx, opt, nil)
}

func openPostgresPoolAfterConnect(ctx context.Context, opt PostgresOptions, after func(context.Context, *pgx.Conn) error) (*Database, error) {
	return openPostgresPoolChecked(ctx, opt, after, false)
}

// allowMissing is private to the lifecycle owner's startup path. Connecting
// never creates anything; startup must acquire the configured schema's locks
// and pass native-client/filesystem preflight before it may create the schema.
func openPostgresPoolChecked(ctx context.Context, opt PostgresOptions, after func(context.Context, *pgx.Conn) error, allowMissing bool) (*Database, error) {
	if err := opt.validate(); err != nil {
		return nil, err
	}
	cfg, err := pgx.ParseConfig(opt.URL)
	if err != nil {
		// pgx's parse error contains the connection string, including passwords.
		return nil, errors.New("invalid PostgreSQL connection options")
	}
	// Explicitly exclude public, $user and implicit pg_temp precedence. Schemas
	// are owned by the application role and checked below. UTC keeps
	// timestamp behavior independent of the shared server's session defaults.
	cfg.RuntimeParams["search_path"] = pgx.Identifier{opt.Schema}.Sanitize() + ", pg_catalog, pg_temp"
	cfg.RuntimeParams["timezone"] = "UTC"
	cfg.RuntimeParams["datestyle"] = "ISO, YMD"
	cfg.RuntimeParams["client_encoding"] = "UTF8"
	cfg.RuntimeParams["standard_conforming_strings"] = "on"
	cfg.RuntimeParams["default_transaction_isolation"] = "read committed"
	cfg.RuntimeParams["application_name"] = "arrmada"
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	var options []stdlib.OptionOpenDB
	if after != nil {
		options = append(options, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
			if err := after(ctx, conn); err != nil {
				// stdlib does not close a connection when AfterConnect fails.
				// A rejected session may already hold a shared lifecycle lock;
				// it must not leak and indefinitely fence offline recovery.
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = conn.Close(cleanup)
				return err
			}
			return nil
		}))
	}
	pool := stdlib.OpenDB(*cfg, options...)
	maxConns := opt.MaxConnections
	if maxConns == 0 {
		maxConns = 4
	}
	pool.SetMaxOpenConns(maxConns)
	pool.SetMaxIdleConns(maxConns)
	pool.SetConnMaxLifetime(time.Hour)
	db := &Database{pool: pool, dialect: Postgres}
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("connect PostgreSQL: %w", err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&version); err != nil {
		_ = pool.Close()
		return nil, errors.New("could not determine PostgreSQL server version")
	}
	if !supportedPostgresVersion(version) {
		_ = pool.Close()
		return nil, errors.New("Arrmada requires PostgreSQL 16, 17 or 18; this server version is not supported")
	}
	var encoding string
	if err := db.QueryRowContext(ctx, `SELECT current_setting('server_encoding')`).Scan(&encoding); err != nil || encoding != "UTF8" {
		_ = pool.Close()
		return nil, errors.New("Arrmada requires a UTF8-encoded PostgreSQL database")
	}
	if err := checkPostgresSchema(ctx, db, opt.Schema); err != nil && !(allowMissing && errors.Is(err, sql.ErrNoRows)) {
		_ = pool.Close()
		return nil, errors.New("PostgreSQL schema must exist and be owned by the application role, with USAGE and CREATE privileges")
	}
	return db, nil
}

func checkPostgresSchema(ctx context.Context, q Execer, schema string) error {
	var owns, usable bool
	err := q.QueryRowContext(ctx, `SELECT
		pg_has_role(current_user, n.nspowner, 'USAGE'),
		has_schema_privilege(current_user, n.oid, 'USAGE') AND has_schema_privilege(current_user, n.oid, 'CREATE')
		FROM pg_catalog.pg_namespace AS n WHERE n.nspname = $1`, schema).Scan(&owns, &usable)
	if err != nil {
		return err
	}
	if !owns || !usable {
		return errors.New("PostgreSQL schema must be owned by the application role, with USAGE and CREATE privileges")
	}
	return nil
}

func supportedPostgresVersion(version int) bool { return version >= 160000 && version < 190000 }
