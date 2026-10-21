package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// All DDL/grants here target unique disposable fixtures, never deployment roles.
func missingPostgresSchema(t *testing.T, allowCreate bool) (context.Context, *pgx.Conn, PostgresOptions, string) {
	t.Helper()
	ctx, admin, opt, other := postgresFixture(t)
	if _, err := admin.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{opt.Schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	var database string
	if err := admin.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	if allowCreate {
		grant := `GRANT CREATE ON DATABASE ` + pgx.Identifier{database}.Sanitize() + ` TO ` + pgx.Identifier{opt.Schema}.Sanitize()
		if _, err := admin.Exec(ctx, grant); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := admin.Exec(ctx, `REVOKE CREATE ON DATABASE `+pgx.Identifier{database}.Sanitize()+` FROM `+pgx.Identifier{opt.Schema}.Sanitize()); err != nil {
				t.Error(err)
			}
		})
	}
	return ctx, admin, opt, other
}

func TestPostgresStartupCreatesMissingSchemaAndRestartsRestricted(t *testing.T) {
	ctx, admin, opt, other := missingPostgresSchema(t, true)
	opt = runtimePostgresOptions(t, opt)
	opt.MaxConnections = 1
	start := make(chan struct{})
	type result struct {
		st  *Store
		err error
	}
	results := make(chan result, 2)
	for range 2 {
		dir := t.TempDir()
		go func() {
			<-start
			st, err := openPostgresStore(ctx, dir, Options{Backend: Postgres, Postgres: opt})
			results <- result{st, err}
		}()
	}
	close(start)
	var winner *Store
	defer func() {
		if winner != nil {
			_ = winner.Close()
		}
	}()
	for range 2 {
		r := <-results
		if r.err == nil {
			if winner != nil {
				_ = r.st.Close()
				t.Fatal("two startups acquired one missing schema")
			}
			winner = r.st
		} else if !errors.Is(r.err, ErrDatabaseInUse) {
			t.Fatalf("concurrent bootstrap failed unexpectedly: %v", r.err)
		}
	}
	if winner == nil {
		t.Fatal("neither startup initialized the schema")
	}
	status, err := winner.Schema(ctx)
	if err != nil || status.Applied != "0004_subtitle_job_ids" || len(status.Pending) != 0 {
		t.Fatalf("bootstrap migration history: %+v %v", status, err)
	}
	var schema, owner string
	if err := winner.DB().QueryRowContext(ctx, `SELECT current_schema(), pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname=?`, opt.Schema).Scan(&schema, &owner); err != nil || schema != opt.Schema || owner != opt.Schema {
		t.Fatalf("bootstrap schema/owner: %q %q %v", schema, owner, err)
	}
	if _, err := winner.DB().ExecContext(ctx, `INSERT INTO settings (key,value) VALUES ('bootstrap-marker','preserved')`); err != nil {
		t.Fatal(err)
	}
	if err := winner.Close(); err != nil {
		t.Fatal(err)
	}
	winner = nil
	// CREATE is needed only for the first schema creation, not later starts.
	var database string
	if err := admin.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `REVOKE CREATE ON DATABASE `+pgx.Identifier{database}.Sanitize()+` FROM `+pgx.Identifier{opt.Schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	winner, err = openPostgresStore(ctx, t.TempDir(), Options{Backend: Postgres, Postgres: opt})
	if err != nil {
		t.Fatalf("restart without database CREATE: %v", err)
	}
	var value string
	if err := winner.DB().QueryRowContext(ctx, `SELECT value FROM settings WHERE key='bootstrap-marker'`).Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("restart lost state: %q %v", value, err)
	}
	if err := admin.QueryRow(ctx, `SELECT value FROM `+pgx.Identifier{other, "sentinel"}.Sanitize()).Scan(&value); err != nil || value != "unrelated application" {
		t.Fatalf("bootstrap changed unrelated schema: %q %v", value, err)
	}
	var elevated bool
	if err := admin.QueryRow(ctx, `SELECT rolcreatedb OR rolcreaterole OR rolsuper FROM pg_roles WHERE rolname=$1`, opt.Schema).Scan(&elevated); err != nil || elevated {
		t.Fatalf("bootstrap required elevated role: %v %v", elevated, err)
	}
}

func TestPostgresStartupMissingSchemaWithoutPermissionFailsClosed(t *testing.T) {
	ctx, admin, opt, _ := missingPostgresSchema(t, false)
	opt = runtimePostgresOptions(t, opt)
	if st, err := openPostgresStore(ctx, t.TempDir(), Options{Backend: Postgres, Postgres: opt}); err == nil {
		st.Close()
		t.Fatal("startup created a schema without permission")
	} else if !strings.Contains(err.Error(), "grant CREATE on this database or pre-provision") {
		t.Fatalf("missing actionable privilege error: %v", err)
	}
	assertPostgresSchemaAbsent(t, ctx, admin, opt.Schema)
	// The operator can pre-provision instead; no additional app privilege needed.
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{opt.Schema}.Sanitize()+` AUTHORIZATION `+pgx.Identifier{opt.Schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	st, err := openPostgresStore(ctx, t.TempDir(), Options{Backend: Postgres, Postgres: opt})
	if err != nil {
		t.Fatalf("pre-provisioned restricted startup: %v", err)
	}
	defer st.Close()
}

func TestPostgresMissingSchemaLifecycleLocksUseConfiguredName(t *testing.T) {
	ctx, admin, opt, _ := missingPostgresSchema(t, true)
	one, err := openPostgresStartupOwner(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	if two, err := openPostgresStartupOwner(ctx, opt); !errors.Is(err, ErrDatabaseInUse) {
		if two != nil {
			two.Close()
		}
		t.Fatalf("missing schema admitted second owner: %v", err)
	}
	if maintenance, err := OpenPostgresNoMigrate(ctx, t.TempDir(), opt); !errors.Is(err, ErrDatabaseInUse) {
		if maintenance != nil {
			maintenance.Close()
		}
		t.Fatalf("maintenance bypassed missing-schema startup lock: %v", err)
	}
	// Missing schemas must NOT all use current_schema() = pg_catalog as their
	// lock identity. An independent configured schema can initialize in parallel.
	other := opt
	other.Schema += "_independent"
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+pgx.Identifier{other.Schema}.Sanitize())
		if err != nil {
			t.Error(err)
		}
	})
	two, err := openPostgresStartupOwner(ctx, other)
	if err != nil {
		t.Fatalf("unrelated missing schema blocked: %v", err)
	}
	defer two.Close()
	if err := two.ensureStartupSchema(ctx); err != nil {
		t.Fatal(err)
	}
	assertPostgresSchemaAbsent(t, ctx, admin, opt.Schema)
	if err := one.ensureStartupSchema(ctx); err != nil {
		t.Fatal(err)
	}
	app, err := one.activate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStartupPreflightDoesNotCreateSchema(t *testing.T) {
	ctx, admin, opt, _ := missingPostgresSchema(t, true)
	opt.BinDir = t.TempDir()
	if st, err := openPostgresStore(ctx, t.TempDir(), Options{Backend: Postgres, Postgres: opt}); err == nil {
		st.Close()
		t.Fatal("startup accepted missing clients")
	}
	assertPostgresSchemaAbsent(t, ctx, admin, opt.Schema)
}

func TestPostgresStartupNeverAdoptsUnownedOrOccupiedSchema(t *testing.T) {
	for _, owned := range []bool{false, true} {
		name := "unowned"
		if owned {
			name = "occupied"
		}
		t.Run(name, func(t *testing.T) {
			ctx, admin, opt, _ := missingPostgresSchema(t, true)
			opt = runtimePostgresOptions(t, opt)
			create := `CREATE SCHEMA ` + pgx.Identifier{opt.Schema}.Sanitize()
			if owned {
				create += ` AUTHORIZATION ` + pgx.Identifier{opt.Schema}.Sanitize()
			}
			if _, err := admin.Exec(ctx, create); err != nil {
				t.Fatal(err)
			}
			sentinel := pgx.Identifier{opt.Schema, "sentinel"}.Sanitize()
			if _, err := admin.Exec(ctx, `CREATE TABLE `+sentinel+` (value text); INSERT INTO `+sentinel+` VALUES ('preserved')`); err != nil {
				t.Fatal(err)
			}
			var before string
			if err := admin.QueryRow(ctx, `SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname=$1`, opt.Schema).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if st, err := openPostgresStore(ctx, t.TempDir(), Options{Backend: Postgres, Postgres: opt}); err == nil {
				st.Close()
				t.Fatal("startup adopted an unowned or occupied schema")
			}
			var after, value string
			if err := admin.QueryRow(ctx, `SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname=$1`, opt.Schema).Scan(&after); err != nil || after != before {
				t.Fatalf("schema ownership changed: %q -> %q %v", before, after, err)
			}
			if err := admin.QueryRow(ctx, `SELECT value FROM `+sentinel).Scan(&value); err != nil || value != "preserved" {
				t.Fatalf("startup changed existing data: %q %v", value, err)
			}
		})
	}
}

func TestPostgresMaintenanceAndRecoveryNeverBootstrapSchemas(t *testing.T) {
	ctx, admin, opt, _ := postgresFixture(t)
	opt = runtimePostgresOptions(t, opt)
	source, err := openPostgresStore(ctx, t.TempDir(), Options{Backend: Postgres, Postgres: opt})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := source.Snapshot(ctx, BackupManual)
	if err != nil {
		source.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	missing := opt
	missing.Schema += "_missing"
	var database string
	if err := admin.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `GRANT CREATE ON DATABASE `+pgx.Identifier{database}.Sanitize()+` TO `+pgx.Identifier{opt.Schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), `REVOKE CREATE ON DATABASE `+pgx.Identifier{database}.Sanitize()+` FROM `+pgx.Identifier{opt.Schema}.Sanitize())
		if err != nil {
			t.Error(err)
		}
	})
	if db, err := openPostgresPool(ctx, missing); err == nil {
		db.Close()
		t.Fatal("connector adopted missing schema")
	}
	if st, err := OpenPostgresNoMigrate(ctx, t.TempDir(), missing); err == nil {
		st.Close()
		t.Fatal("maintenance adopted missing schema")
	}
	if err := RestorePostgres(ctx, missing, archive, t.TempDir()); err == nil {
		t.Fatal("restore bootstrapped a missing schema")
	}
	sqliteDir := t.TempDir()
	sqlite, err := Open(sqliteDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ConvertSQLiteToPostgres(ctx, filepath.Join(sqliteDir, "arrmada.db"), missing, t.TempDir()); err == nil {
		t.Fatal("conversion bootstrapped a missing destination")
	}
	if err := ConvertPostgresToSQLite(ctx, missing, filepath.Join(t.TempDir(), "arrmada.db")); err == nil {
		t.Fatal("conversion bootstrapped a missing source")
	}
	assertPostgresSchemaAbsent(t, ctx, admin, missing.Schema)
}

func assertPostgresSchemaAbsent(t *testing.T, ctx context.Context, admin *pgx.Conn, schema string) {
	t.Helper()
	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&exists); err != nil || exists {
		t.Fatalf("missing schema was created unexpectedly: %v %v", exists, err)
	}
}
