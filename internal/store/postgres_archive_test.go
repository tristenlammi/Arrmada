package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/tristenlammi/arrmada/internal/diskspace"
)

func TestPostgresClientEnvironment(t *testing.T) {
	t.Setenv("PGPASSWORD", "wrong-password")
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGSERVICEFILE", "")
	t.Setenv("PGHOST", "wrong-host")
	t.Setenv("PGOPTIONS", "-c role=wrong-role")
	opt := PostgresOptions{URL: "postgres://app:secret@localhost/shared?host=%2Fsocket&port=55473&search_path=foreign&timezone=UTC&options=-crole%3Dwrong&sslmode=disable", Schema: "arrmada_test"}
	env, err := postgresClientEnv(opt)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, item := range env {
		key, value, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "PG") {
			values[key] = value
		}
		if strings.Contains(item, "wrong-") {
			t.Fatalf("inherited or URL runtime override survived: %s", key)
		}
	}
	if values["PGPASSWORD"] != "secret" || values["PGHOST"] != "/socket" || values["PGSSLMODE"] != "disable" || values["PGDATABASE"] != "shared" || values["PGPORT"] != "55473" || values["PGUSER"] != "app" {
		t.Fatal("connection credentials, socket or TLS settings lost")
	}
	if values["PGSERVICE"] != "" || strings.Contains(values["PGOPTIONS"], "role=") {
		t.Fatal("runtime override reached libpq")
	}
	if values["PGCLIENTENCODING"] != "UTF8" {
		t.Fatal("native clients were not pinned to UTF8")
	}
	t.Setenv("PGSERVICE", "wrong-service")
	if _, err := postgresClientEnv(opt); err == nil {
		t.Fatal("service defaults were silently ignored instead of rejected")
	}
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGSSLMODE", "verify-full")
	t.Setenv("PGSSLROOTCERT", "system")
	tlsOpt := PostgresOptions{URL: "postgres://app:secret@localhost/shared", Schema: opt.Schema}
	tlsEnv, err := postgresClientEnv(tlsOpt)
	if err != nil {
		t.Fatal(err)
	}
	values = map[string]string{}
	for _, item := range tlsEnv {
		key, value, _ := strings.Cut(item, "=")
		values[key] = value
	}
	if values["PGSSLMODE"] != "verify-full" || values["PGSSLROOTCERT"] != "system" {
		t.Fatal("native clients lost the running connector's TLS environment defaults")
	}
}

func TestNativePostgresToolRedactsErrors(t *testing.T) {
	// The helper writes secrets to both streams and then fails. Neither stream
	// nor Command.String() may be included in the returned error.
	ctx := context.Background()
	err := nativePostgresTool(ctx, "/bin/sh", []string{"PGDATABASE=postgres://app:secret@private/db"}, &bytes.Buffer{}, "-c", "echo secret; echo private >&2; exit 23")
	if err == nil || !strings.Contains(err.Error(), "exit 23") || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe client error: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := nativePostgresTool(ctx, "/bin/sh", nil, nil, "-c", "exit 0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled native operation = %v", err)
	}
}

func TestPostgresArchiveStopsBeforeDiskExhaustion(t *testing.T) {
	previous := freeSpace
	t.Cleanup(func() { freeSpace = previous })
	probes := 0
	freeSpace = func(string) (diskspace.Usage, bool) {
		probes++
		if probes == 1 {
			return diskspace.Usage{FreeBytes: 32 * 1024 * 1024}, true
		}
		return diskspace.Usage{FreeBytes: postgresBackupReserve}, true
	}
	var output bytes.Buffer
	w := &postgresArchiveDiskWriter{w: &output, dir: t.TempDir()}
	block := make([]byte, 1024*1024)
	if _, err := w.Write(block); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(block); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("streaming archive exhausted its reserve: %v", err)
	}
	if output.Len() != len(block) {
		t.Fatal("archive kept writing after the free-space guard failed")
	}
}

func archiveTestTools(t *testing.T) postgresArchiveTools {
	t.Helper()
	bin := os.Getenv("ARRMADA_TEST_POSTGRES_BIN")
	if bin == "" {
		if _, err := exec.LookPath("pg_dump"); err != nil {
			if os.Getenv("ARRMADA_TEST_POSTGRES_ADMIN_URL") != "" {
				t.Fatal("PostgreSQL integration tests require native clients; set ARRMADA_TEST_POSTGRES_BIN")
			}
			t.Skip("native PostgreSQL clients not installed; set ARRMADA_TEST_POSTGRES_BIN")
		}
		return defaultPostgresArchiveTools()
	}
	return postgresArchiveTools{dump: filepath.Join(bin, "pg_dump"), restore: filepath.Join(bin, "pg_restore")}
}

func TestPostgresNativeSchemaArchive(t *testing.T) {
	tools := archiveTestTools(t)
	ctx, admin, opt, other := postgresFixture(t)
	db, err := openPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('archive-value', 'private-data-123')`); err != nil {
		t.Fatal(err)
	}
	// A pg_dump -n bare_pattern incorrectly includes this lookalike schema:
	// underscores in schema names are pattern wildcards unless quoted.
	lookalike := strings.ReplaceAll(opt.Schema, "_", "x")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{lookalike}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{lookalike}.Sanitize()+" CASCADE")
	})
	for _, schema := range []string{lookalike, other} {
		if _, err := admin.Exec(ctx, "CREATE TABLE "+pgx.Identifier{schema, "archive_unrelated"}.Sanitize()+" (value text)"); err != nil {
			t.Fatal(err)
		}
		// Grant read privileges only on disposable fixture objects so an overly
		// broad dump would succeed rather than mask a pattern bug with denial.
		if _, err := admin.Exec(ctx, "GRANT USAGE ON SCHEMA "+pgx.Identifier{schema}.Sanitize()+" TO "+pgx.Identifier{opt.Schema}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, "GRANT SELECT ON "+pgx.Identifier{schema, "archive_unrelated"}.Sanitize()+" TO "+pgx.Identifier{opt.Schema}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}
	_, unlock, err := lockPostgresSchema(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	dst := filepath.Join(t.TempDir(), "backup.pgdump")
	if err := postgresSnapshotLocked(ctx, db, opt, tools, dst); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(dst)
	if err != nil || stat.Mode().Perm() != 0600 || stat.Size() == 0 {
		t.Fatalf("archive file = %v, %v", stat, err)
	}
	env, err := postgresClientEnv(opt)
	if err != nil {
		t.Fatal(err)
	}
	var decoded bytes.Buffer
	if err := nativePostgresTool(ctx, tools.restore, env, &decoded, "--no-owner", "--no-privileges", "--file=-", dst); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decoded.String(), "private-data-123") || !strings.Contains(decoded.String(), "schema_migrations") || strings.Contains(decoded.String(), "archive_unrelated") {
		t.Fatal("archive omitted application data/metadata or included unrelated objects")
	}
	before, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if err := postgresSnapshotLocked(ctx, db, opt, tools, dst); err == nil {
		t.Fatal("existing backup was overwritten")
	}
	after, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("existing archive changed: %v", err)
	}
	// A failure after writing a partial archive must not publish a usable name.
	fail := filepath.Join(t.TempDir(), "pg-dump-failure")
	if err := os.WriteFile(fail, []byte("#!/bin/sh\necho partial-secret\necho secret >&2\nexit 9\n"), 0700); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "failed.pgdump")
	if err := postgresArchiveTo(ctx, opt, postgresArchiveTools{dump: fail, restore: tools.restore}, bad); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("failed archive = %v", err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("failed archive left files: %v, %v", files, err)
	}
	// A valid-looking dump that cannot be decoded also remains unpublished.
	dir = t.TempDir()
	if err := postgresArchiveTo(ctx, opt, postgresArchiveTools{dump: tools.dump, restore: fail}, filepath.Join(dir, "bad.pgdump")); err == nil {
		t.Fatal("archive was published without successful full decoding")
	}
	files, err = os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("undecodable archive left files: %v, %v", files, err)
	}
	truncatedDump := filepath.Join(t.TempDir(), "truncated-dump")
	if err := os.WriteFile(truncatedDump, []byte("#!/bin/sh\nhead -c 64 \"$ARRMADA_TEST_ARCHIVE_SOURCE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARRMADA_TEST_ARCHIVE_SOURCE", dst)
	dir = t.TempDir()
	if err := postgresArchiveTo(ctx, opt, postgresArchiveTools{dump: truncatedDump, restore: tools.restore}, filepath.Join(dir, "truncated.pgdump")); err == nil {
		t.Fatal("real pg_restore accepted a truncated archive")
	}
	files, err = os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("truncated archive left files: %v, %v", files, err)
	}
	previous := freeSpace
	freeSpace = func(string) (diskspace.Usage, bool) { return diskspace.Usage{}, true }
	t.Cleanup(func() { freeSpace = previous })
	if err := postgresSnapshotLocked(ctx, db, opt, tools, filepath.Join(t.TempDir(), "full.pgdump")); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("full-disk archive = %v", err)
	}
}
