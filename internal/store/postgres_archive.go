package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// postgresArchiveTools is deliberately private until archive validation and
// offline recovery are wired into Store. Native archive SQL is never executed
// by Arrmada: recovery must use trusted migrations and validated data instead.
type postgresArchiveTools struct {
	dump, restore string
}

func defaultPostgresArchiveTools() postgresArchiveTools {
	return postgresArchiveTools{dump: "pg_dump", restore: "pg_restore"}
}

func postgresTools(opt PostgresOptions) postgresArchiveTools {
	if opt.BinDir == "" {
		return defaultPostgresArchiveTools()
	}
	return postgresArchiveTools{dump: filepath.Join(opt.BinDir, "pg_dump"), restore: filepath.Join(opt.BinDir, "pg_restore")}
}

// postgresSnapshotLocked is also usable by the pre-migration backup callback:
// its caller already owns the schema lock, so it must not try to acquire it on
// another pooled connection. This is not yet a public backup/restore API.
func postgresSnapshotLocked(ctx context.Context, db *Database, opt PostgresOptions, tools postgresArchiveTools, dst string) error {
	return postgresSnapshotLockedChecked(ctx, db, opt, tools, dst, nil)
}

func postgresSnapshotLockedChecked(ctx context.Context, db *Database, opt PostgresOptions, tools postgresArchiveTools, dst string, guard func() error) error {
	var schema string
	var version int
	if db.dialect != Postgres {
		return errors.New("PostgreSQL snapshot requested for another backend")
	}
	if err := db.QueryRowContext(ctx, `SELECT current_schema(), current_setting('server_version_num')::integer`).Scan(&schema, &version); err != nil {
		return errors.New("could not inspect PostgreSQL backup source")
	}
	if schema != opt.Schema {
		return errors.New("PostgreSQL backup schema does not match the open database")
	}
	for _, path := range []string{tools.dump, tools.restore} {
		major, err := postgresClientMajor(ctx, path)
		if err != nil {
			return err
		}
		if major < version/10000 {
			return errors.New("PostgreSQL backup clients must not be older than the server")
		}
	}
	if err := mkPrivateDir(filepath.Dir(dst)); err != nil {
		return err
	}
	size, err := postgresSchemaBytes(ctx, db)
	if err != nil {
		return fmt.Errorf("estimate PostgreSQL backup size: %w", err)
	}
	// Custom archives include DDL and sequence metadata even on a nearly empty
	// database. Reserve a minimum as well as the usual relation-size headroom.
	need := uint64(float64(size)*spaceHeadroom) + 1024*1024
	if usage, ok := freeSpace(filepath.Dir(dst)); ok && usage.FreeBytes < need {
		return fmt.Errorf("%w: PostgreSQL backup needs about %s", ErrNoSpace, formatBytes(need))
	}
	return postgresArchiveToChecked(ctx, opt, tools, dst, guard)
}

// postgresClientEnv passes connection secrets through the child's environment,
// never its arguments. Drop inherited PG* variables (including service files,
// passwords, role and options) so they cannot silently change the target. GUCs
// accepted by pgx are not libpq connection keywords; only copy known connection
// parameters, never arbitrary options or a search_path supplied in the URL.
func postgresClientEnv(opt PostgresOptions) ([]string, error) {
	if err := opt.validate(); err != nil {
		return nil, err
	}
	u, err := url.Parse(opt.URL)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection options")
	}
	allowed := map[string]string{
		"host": "PGHOST", "hostaddr": "PGHOSTADDR", "port": "PGPORT", "user": "PGUSER",
		"password": "PGPASSWORD", "dbname": "PGDATABASE", "connect_timeout": "PGCONNECT_TIMEOUT",
		"sslmode": "PGSSLMODE", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY",
		"sslrootcert": "PGSSLROOTCERT", "sslcrl": "PGSSLCRL", "sslcrldir": "PGSSLCRLDIR",
		"sslsni": "PGSSLSNI", "ssl_min_protocol_version": "PGSSLMINPROTOCOLVERSION",
		"ssl_max_protocol_version": "PGSSLMAXPROTOCOLVERSION", "sslnegotiation": "PGSSLNEGOTIATION",
		"target_session_attrs": "PGTARGETSESSIONATTRS", "gssencmode": "PGGSSENCMODE", "krbsrvname": "PGKRBSRVNAME",
		"channel_binding": "PGCHANNELBINDING", "passfile": "PGPASSFILE",
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection options")
	}
	if q.Has("service") || q.Has("servicefile") || os.Getenv("PGSERVICE") != "" || os.Getenv("PGSERVICEFILE") != "" {
		return nil, errors.New("PostgreSQL native backup requires connection settings in the URL, not a libpq service file")
	}
	if q.Has("sslpassword") || os.Getenv("PGSSLPASSWORD") != "" {
		return nil, errors.New("PostgreSQL native backup does not support the sslpassword URL option; use a client key managed by the operating system")
	}
	cfg, err := pgx.ParseConfig(opt.URL)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection options")
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback.Host != cfg.Host || fallback.Port != cfg.Port {
			return nil, errors.New("PostgreSQL native backup currently requires a single explicit host and port")
		}
	}
	// PGDATABASE containing a URI is not expanded by pg_dump/libpq when it
	// comes from the environment. Set individual libpq variables instead.
	values := map[string]string{"PGHOST": cfg.Host, "PGPORT": strconv.Itoa(int(cfg.Port)), "PGUSER": cfg.User, "PGDATABASE": cfg.Database, "PGPASSWORD": cfg.Password}
	// Preserve TLS/auth environment defaults that pgx itself recognizes, but
	// not libpq-only environment switches that the running application ignores.
	for _, variable := range []string{"PGSSLMODE", "PGSSLKEY", "PGSSLCERT", "PGSSLSNI", "PGSSLROOTCERT", "PGSSLNEGOTIATION", "PGTARGETSESSIONATTRS", "PGCHANNELBINDING", "PGPASSFILE"} {
		if value, ok := os.LookupEnv(variable); ok {
			values[variable] = value
		}
	}
	for key, variable := range allowed {
		if q.Has(key) && key != "host" && key != "port" && key != "user" && key != "dbname" && key != "password" {
			values[variable] = q.Get(key)
		}
	}
	env := make([]string, 0, len(os.Environ())+len(values)+2)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "PG") {
			env = append(env, item)
		}
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return append(env, "PGAPPNAME=arrmada-backup", "PGCLIENTENCODING=UTF8",
		"PGOPTIONS=-c default_transaction_read_only=on -c timezone=UTC -c datestyle=ISO,YMD -c bytea_output=hex -c extra_float_digits=3"), nil
}

// nativePostgresTool returns only a classified error. Native client diagnostics
// can include passwords, server addresses or row values; they must never reach
// logs or API responses. Successful stderr is discarded for the same reason.
func nativePostgresTool(ctx context.Context, path string, env []string, out io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("PostgreSQL client failed (exit %d); check client/server compatibility, connection settings and privileges", exit.ExitCode())
		}
		return errors.New("could not start PostgreSQL client; install pg_dump and pg_restore from a supported PostgreSQL version")
	}
	return nil
}

// postgresArchiveTo creates a native, schema-only-scope custom archive. The
// caller holds the lifecycle/schema lock to exclude DDL and schema replacement.
// pg_dump itself takes a consistent MVCC snapshot. A final backup name is never
// published on failure, and an existing destination is never overwritten.
func postgresArchiveTo(ctx context.Context, opt PostgresOptions, tools postgresArchiveTools, dst string) error {
	return postgresArchiveToChecked(ctx, opt, tools, dst, nil)
}

func postgresArchiveToChecked(ctx context.Context, opt PostgresOptions, tools postgresArchiveTools, dst string, guard func() error) error {
	env, err := postgresClientEnv(opt)
	if err != nil {
		return err
	}
	dir := filepath.Dir(dst)
	if err := mkPrivateDir(dir); err != nil {
		return fmt.Errorf("create PostgreSQL backup directory: %w", err)
	}
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		return errors.New("PostgreSQL backup destination already exists or cannot be inspected")
	}
	f, err := os.CreateTemp(dir, ".postgres-backup-*")
	if err != nil {
		return fmt.Errorf("create PostgreSQL backup temporary file: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	// --schema is a pg_dump pattern, not a bare SQL identifier. Double quotes
	// make underscores literal, preventing an arrmada_test schema from also
	// matching an unrelated arrmadaXtest schema in the same database.
	args := []string{"--no-password", "--format=custom", "--compress=0", "--strict-names",
		"--schema=" + `"` + opt.Schema + `"`, "--no-owner", "--no-privileges",
		"--no-tablespaces", "--no-security-labels", "--no-comments"}
	writer := &postgresArchiveDiskWriter{w: f, dir: dir}
	if err := nativePostgresTool(ctx, tools.dump, env, writer, args...); err != nil {
		if writer.err != nil {
			return writer.err
		}
		return fmt.Errorf("create PostgreSQL backup: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("flush PostgreSQL backup: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close PostgreSQL backup: %w", err)
	}
	// Decode the entire archive, not just its TOC: a valid header can precede
	// corrupt/truncated table data. --file=- never connects or executes SQL.
	if err := nativePostgresTool(ctx, tools.restore, env, io.Discard,
		"--no-owner", "--no-privileges", "--file=-", f.Name()); err != nil {
		return fmt.Errorf("verify PostgreSQL backup archive: %w", err)
	}
	// A lost coordination session during the native dump must not publish a
	// backup that may have overlapped an upgrade. No reconnect is allowed.
	if guard != nil {
		if err := guard(); err != nil {
			return errors.New("PostgreSQL schema protection was lost during backup; no backup was published")
		}
	}
	// Hard-link publication is atomic and exclusive on the same filesystem.
	// Unlike Rename, it cannot replace a backup that appeared during the dump.
	if err := os.Link(f.Name(), dst); err != nil {
		return fmt.Errorf("publish PostgreSQL backup without replacing existing files: %w", err)
	}
	syncDir(dir)
	return nil
}

const postgresBackupReserve = 16 * 1024 * 1024

// Relation-size estimates cannot bound native COPY output: TOAST compression
// and bytea hex encoding can expand it substantially. Recheck free space while
// streaming, retaining room for the application's own filesystem state/logs.
type postgresArchiveDiskWriter struct {
	w          io.Writer
	dir        string
	untilCheck int
	err        error
}

func (w *postgresArchiveDiskWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.untilCheck <= 0 || len(p) >= w.untilCheck {
		if u, ok := freeSpace(w.dir); ok && u.FreeBytes < postgresBackupReserve+uint64(max(len(p), 1024*1024)) {
			w.err = ErrNoSpace
			return 0, w.err
		}
		w.untilCheck = 1024 * 1024
	}
	n, err := w.w.Write(p)
	w.untilCheck -= n
	if err != nil {
		w.err = err
	}
	return n, err
}

// postgresSchemaBytes estimates only this schema's stored relations, not the
// shared database's size. Used for backup free-space headroom checks.
func postgresSchemaBytes(ctx context.Context, q Execer) (int64, error) {
	var size int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(pg_catalog.pg_total_relation_size(c.oid)), 0)::bigint
		FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'm')`).Scan(&size)
	return size, err
}

// Check the native client's major before building an archive. pg_dump cannot
// dump a newer server; clients newer than our tested range are not supported.
func postgresClientMajor(ctx context.Context, path string) (int, error) {
	var version limitedVersionOutput
	if err := nativePostgresTool(ctx, path, []string{}, &version, "--version"); err != nil {
		return 0, err
	}
	words := strings.Fields(version.String())
	if len(words) < 3 || (words[0] != "pg_dump" && words[0] != "pg_restore") || words[1] != "(PostgreSQL)" {
		return 0, errors.New("could not determine PostgreSQL client version")
	}
	majorText, _, _ := strings.Cut(words[2], ".")
	major, err := strconv.Atoi(majorText)
	if err != nil || major < 16 || major > 18 {
		return 0, errors.New("PostgreSQL backup clients must be version 16, 17 or 18")
	}
	return major, nil
}

type limitedVersionOutput struct{ strings.Builder }

func (b *limitedVersionOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 256 {
		return 0, errors.New("PostgreSQL client version output is too long")
	}
	return b.Builder.Write(p)
}
