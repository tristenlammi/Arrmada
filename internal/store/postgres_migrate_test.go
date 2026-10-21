package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
)

func postgresTestPool(t *testing.T) (context.Context, *Database, *pgx.Conn, string) {
	t.Helper()
	ctx, admin, opt, other := postgresFixture(t)
	db, err := openPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return ctx, db, admin, other
}

func TestPostgresBaseline(t *testing.T) {
	ctx, db, admin, other := postgresTestPool(t)
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	status, err := postgresSchema(ctx, db, postgresMigrations())
	if err != nil || status.Applied == "" || len(status.Pending) > 0 || len(status.Unknown) > 0 {
		t.Fatalf("schema status = %+v, %v", status, err)
	}
	if err := migratePostgres(ctx, db, postgresMigrations(), func(context.Context, []string) error {
		t.Fatal("reopening a current database asked for a migration backup")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var sentinel string
	if err := admin.QueryRow(ctx, "SELECT value FROM "+pgx.Identifier{other, "sentinel"}.Sanitize()).Scan(&sentinel); err != nil || sentinel != "unrelated application" {
		t.Fatalf("unrelated schema changed: %q, %v", sentinel, err)
	}
	var profiles int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM quality_profiles`).Scan(&profiles); err != nil || profiles != 7 {
		t.Fatalf("seeded profiles = %d, %v", profiles, err)
	}
	var id int64
	if err := db.QueryRowContext(ctx, `INSERT INTO quality_profiles (media_type, name) VALUES (?, ?) RETURNING id`, "movie", "new profile").Scan(&id); err != nil || id <= 8 {
		t.Fatalf("seed sequence not advanced: %d, %v", id, err)
	}
}

func TestPostgresBaselineColumnsAndIndexesMatchSQLite(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// The SQLite template may take time to build under the race detector; the
	// fixture context bounds real server operations, not local schema creation.
	ctx = context.Background()
	rows, err := st.DB().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var wantTables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		wantTables = append(wantTables, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	pgRows, err := db.QueryContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	var gotTables []string
	for pgRows.Next() {
		var name string
		if err := pgRows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		gotTables = append(gotTables, name)
	}
	err = pgRows.Err()
	pgRows.Close()
	if err != nil || !reflect.DeepEqual(gotTables, wantTables) {
		t.Fatalf("PostgreSQL tables %v differ from SQLite %v: %v", gotTables, wantTables, err)
	}
	for _, table := range wantTables {
		if table == "schema_migrations" {
			continue // independent backend migration histories
		}
		r, err := st.DB().Query(`SELECT name, type FROM pragma_table_info(?) ORDER BY cid`, table)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		var wantTypes []string
		for r.Next() {
			var name, kind string
			if err := r.Scan(&name, &kind); err != nil {
				t.Fatal(err)
			}
			want = append(want, name)
			wantTypes = append(wantTypes, map[string]string{"INTEGER": "bigint", "TEXT": "text", "REAL": "double precision", "BLOB": "bytea", "TIMESTAMP": "timestamp without time zone"}[kind])
		}
		err = r.Err()
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		r, err = db.QueryContext(ctx, `SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ? ORDER BY ordinal_position`, table)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		var gotTypes []string
		for r.Next() {
			var name, kind string
			if err := r.Scan(&name, &kind); err != nil {
				t.Fatal(err)
			}
			got = append(got, name)
			gotTypes = append(gotTypes, kind)
		}
		err = r.Err()
		r.Close()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s columns = %v, want %v: %v", table, got, want, err)
		}
		if !reflect.DeepEqual(gotTypes, wantTypes) {
			t.Errorf("%s column types = %v, want %v", table, gotTypes, wantTypes)
		}
		var seedColumns []string
		for i, name := range want {
			if wantTypes[i] != "timestamp without time zone" {
				seedColumns = append(seedColumns, name)
			}
		}
		if source, target := baselineSeedRows(t, ctx, st.DB(), table, seedColumns), baselineSeedRows(t, ctx, db, table, seedColumns); !reflect.DeepEqual(source, target) {
			t.Errorf("%s seeded data differs:\nSQLite %v\nPostgreSQL %v", table, source, target)
		}
	}
	indexes, err := st.DB().Query(`SELECT name FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer indexes.Close()
	for indexes.Next() {
		var name string
		if err := indexes.Scan(&name); err != nil {
			t.Fatal(err)
		}
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_indexes WHERE schemaname = current_schema() AND indexname = ?)`, name).Scan(&exists); err != nil || !exists {
			t.Errorf("missing PostgreSQL index %s: %v", name, err)
		}
	}
	if err := indexes.Err(); err != nil {
		t.Fatal(err)
	}
	indexes.Close()
	var wantFKs []string
	for _, table := range wantTables {
		r, err := st.DB().Query(`SELECT "from", "table", "to", on_delete, on_update FROM pragma_foreign_key_list(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		for r.Next() {
			var from, target, to, del, update string
			if err := r.Scan(&from, &target, &to, &del, &update); err != nil {
				t.Fatal(err)
			}
			wantFKs = append(wantFKs, fmt.Sprintf("%s.%s->%s.%s/%s/%s", table, from, target, to, del, update))
		}
		err = r.Err()
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	fks, err := db.QueryContext(ctx, `SELECT src.relname, a.attname, dst.relname, b.attname, con.confdeltype::text, con.confupdtype::text
		FROM pg_catalog.pg_constraint con
		JOIN pg_catalog.pg_namespace n ON n.oid = con.connamespace
		JOIN pg_catalog.pg_class src ON src.oid = con.conrelid
		JOIN pg_catalog.pg_class dst ON dst.oid = con.confrelid
		JOIN pg_catalog.pg_attribute a ON a.attrelid = src.oid AND a.attnum = con.conkey[1]
		JOIN pg_catalog.pg_attribute b ON b.attrelid = dst.oid AND b.attnum = con.confkey[1]
		WHERE con.contype = 'f' AND n.nspname = current_schema()`)
	if err != nil {
		t.Fatal(err)
	}
	defer fks.Close()
	var gotFKs []string
	actions := map[string]string{"a": "NO ACTION", "r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}
	for fks.Next() {
		var table, from, target, to, del, update string
		if err := fks.Scan(&table, &from, &target, &to, &del, &update); err != nil {
			t.Fatal(err)
		}
		gotFKs = append(gotFKs, fmt.Sprintf("%s.%s->%s.%s/%s/%s", table, from, target, to, actions[del], actions[update]))
	}
	if err := fks.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(wantFKs)
	sort.Strings(gotFKs)
	if !reflect.DeepEqual(gotFKs, wantFKs) {
		t.Fatalf("foreign keys differ:\nPostgreSQL %v\nSQLite %v", gotFKs, wantFKs)
	}
}

func baselineSeedRows(t *testing.T, ctx context.Context, db Execer, table string, columns []string) []string {
	t.Helper()
	quoted := make([]string, len(columns))
	for i, name := range columns {
		quoted[i] = pgx.Identifier{name}.Sanitize()
	}
	rows, err := db.QueryContext(ctx, "SELECT "+strings.Join(quoted, ", ")+" FROM "+pgx.Identifier{table}.Sanitize()+" ORDER BY "+strings.Join(quoted, ", "))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		value, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(value))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPostgresMigrationUpgradeSafety(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	base := fstest.MapFS{"0001_base.sql": {Data: []byte(`CREATE TABLE sample (id bigint PRIMARY KEY);`)}}
	if err := migratePostgres(ctx, db, base, nil); err != nil {
		t.Fatal(err)
	}
	upgrade := fstest.MapFS{
		"0001_base.sql":    base["0001_base.sql"],
		"0002_upgrade.sql": {Data: []byte(`ALTER TABLE sample ADD COLUMN value text NOT NULL DEFAULT '';`)},
	}
	if err := migratePostgres(ctx, db, upgrade, nil); !errors.Is(err, ErrMigrationBackupNeeded) {
		t.Fatalf("upgraded without a backup: %v", err)
	}
	backupErr := errors.New("backup disk unavailable")
	if err := migratePostgres(ctx, db, upgrade, func(context.Context, []string) error { return backupErr }); !errors.Is(err, backupErr) {
		t.Fatalf("backup failure not preserved: %v", err)
	}
	st, err := postgresSchema(ctx, db, upgrade)
	if err != nil || st.Applied != "0001_base" || !reflect.DeepEqual(st.Pending, []string{"0002_upgrade"}) {
		t.Fatalf("failed backup changed schema: %+v, %v", st, err)
	}
	backups := 0
	if err := migratePostgres(ctx, db, upgrade, func(_ context.Context, pending []string) error {
		backups++
		if !reflect.DeepEqual(pending, []string{"0002_upgrade"}) {
			t.Errorf("backup pending = %v", pending)
		}
		return nil
	}); err != nil || backups != 1 {
		t.Fatalf("upgrade = %v, backups = %d", err, backups)
	}
	if err := migratePostgres(ctx, db, base, nil); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("older build accepted upgraded database: %v", err)
	}
	changed := fstest.MapFS{
		"0001_base.sql":    {Data: []byte(`CREATE TABLE sample (id text PRIMARY KEY);`)},
		"0002_upgrade.sql": upgrade["0002_upgrade.sql"],
	}
	if err := migratePostgres(ctx, db, changed, nil); !errors.Is(err, ErrMigrationChanged) {
		t.Fatalf("edited shipped migration accepted: %v", err)
	}
}

func TestPostgresFailedMigrationRollsBackObjectsAndMetadata(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	migrations := fstest.MapFS{"0001_bad.sql": {Data: []byte(`CREATE TABLE must_not_remain (id bigint); SELECT missing_column FROM must_not_remain;`)}}
	if err := migratePostgres(ctx, db, migrations, nil); err == nil {
		t.Fatal("invalid migration succeeded")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname = current_schema()`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed migration left tables: %d, %v", count, err)
	}
	if err := migratePostgres(ctx, db, fstest.MapFS{"0001_fixed.sql": {Data: []byte(`CREATE TABLE sample (id bigint);`)}}, nil); err != nil {
		t.Fatalf("failed migration poisoned a later attempt: %v", err)
	}
}

func TestPostgresMigrationRefusesOccupiedSchema(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	if _, err := db.ExecContext(ctx, `CREATE TABLE somebody_elses_data (value text); INSERT INTO somebody_elses_data VALUES ('preserve')`); err != nil {
		t.Fatal(err)
	}
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); !errors.Is(err, ErrPostgresSchemaOccupied) {
		t.Fatalf("adopted occupied schema: %v", err)
	}
	var value string
	if err := db.QueryRowContext(ctx, `SELECT value FROM somebody_elses_data`).Scan(&value); err != nil || value != "preserve" {
		t.Fatalf("occupied schema changed: %q, %v", value, err)
	}
}

func TestPostgresMigrationRefusesStandaloneObjects(t *testing.T) {
	for _, tt := range []struct{ name, query string }{
		{"function", `CREATE FUNCTION unrelated() RETURNS integer LANGUAGE sql AS 'SELECT 1'`},
		{"domain", `CREATE DOMAIN unrelated AS text`},
		{"collation", `CREATE COLLATION unrelated FROM pg_catalog."C"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, db, _, _ := postgresTestPool(t)
			if _, err := db.ExecContext(ctx, tt.query); err != nil {
				t.Fatal(err)
			}
			if err := migratePostgres(ctx, db, postgresMigrations(), nil); !errors.Is(err, ErrPostgresSchemaOccupied) {
				t.Fatalf("adopted schema with standalone %s: %v", tt.name, err)
			}
		})
	}
}

func TestPostgresSchemaLockAndCancellation(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	_, release, err := lockPostgresSchema(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	base := fstest.MapFS{"0001_base.sql": {Data: []byte(`CREATE TABLE sample (id bigint);`)}}
	err = migratePostgres(ctx, db, base, nil)
	release()
	if !errors.Is(err, ErrDatabaseInUse) {
		t.Fatalf("concurrent schema operation accepted: %v", err)
	}
	if err := migratePostgres(ctx, db, base, nil); err != nil {
		t.Fatal(err)
	}
	upgrade := fstest.MapFS{"0001_base.sql": base["0001_base.sql"], "0002_upgrade.sql": {Data: []byte(`ALTER TABLE sample ADD COLUMN value text;`)}}
	cancelCtx, cancel := context.WithCancel(ctx)
	err = migratePostgres(cancelCtx, db, upgrade, func(context.Context, []string) error {
		cancel()
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not preserved: %v", err)
	}
	if err := migratePostgres(ctx, db, upgrade, func(context.Context, []string) error { return nil }); err != nil {
		t.Fatalf("cancellation leaked schema lock: %v", err)
	}
}

func TestPostgresSchemaLocksDoNotBlockAnotherApplicationSchema(t *testing.T) {
	ctx, first, _, _ := postgresTestPool(t)
	_, release, err := lockPostgresSchema(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	otherCtx, second, _, _ := postgresTestPool(t)
	if err := migratePostgres(otherCtx, second, fstest.MapFS{"0001_base.sql": {Data: []byte(`CREATE TABLE sample (id bigint);`)}}, nil); err != nil {
		t.Fatalf("a different schema was blocked by the first schema's lock: %v", err)
	}
}

func TestPostgresBaselineTypesConstraintsAndDeferredCycles(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	const large int64 = 9_000_000_000_000_000
	const precise = 1.2345678901234567
	if _, err := db.ExecContext(ctx, `INSERT INTO books (id, ol_key, title, size_bytes, series_position) VALUES (?, ?, ?, ?, ?)`, large, "large", "Large", large, precise); err != nil {
		t.Fatal(err)
	}
	var size int64
	var position float64
	if err := db.QueryRowContext(ctx, `SELECT size_bytes, series_position FROM books WHERE id = ?`, large).Scan(&size, &position); err != nil || size != large || position != precise {
		t.Fatalf("numeric precision lost: %d, %.17g, %v", size, position, err)
	}
	blob := []byte{0, 0xff, 0x7f, 'x'}
	if _, err := db.ExecContext(ctx, `INSERT INTO metadata_cache (key, value, stored_at, expires_at) VALUES (?, ?, ?, ?)`, "blob", blob, 1, large); err != nil {
		t.Fatal(err)
	}
	var got []byte
	if err := db.QueryRowContext(ctx, `SELECT value FROM metadata_cache WHERE key = ?`, "blob").Scan(&got); err != nil || !bytes.Equal(got, blob) {
		t.Fatalf("blob changed: %x, %v", got, err)
	}
	for _, name := range []string{"Default", "É", "é"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO subtitle_profiles (name, items_json) VALUES (?, '[]')`, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO subtitle_profiles (name, items_json) VALUES (?, '[]')`, "default"); !IsUniqueViolation(err) {
		t.Fatalf("ASCII case-insensitive uniqueness not enforced: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO albums (artist_id, mbid, title) VALUES (999, 'missing artist', 'invalid')`); err == nil {
		t.Fatal("immediate foreign key not enforced")
	}
	native, err := db.pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Rollback()
	tx := &Transaction{tx: native, dialect: Postgres}
	for _, query := range []string{
		`SET CONSTRAINTS ALL DEFERRED`,
		`INSERT INTO artists (id, mbid, name) VALUES (1, 'artist', 'Artist')`,
		`INSERT INTO albums (id, artist_id, mbid, title, target_release_id) VALUES (1, 1, 'album', 'Album', 1)`,
		`INSERT INTO music_releases (id, album_id, title) VALUES (1, 1, 'Release')`,
	} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := native.Commit(); err != nil {
		t.Fatalf("valid deferred music cycle rejected: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO library_folders (media, path) VALUES ('invalid', '/not-a-real-folder')`); err == nil {
		t.Fatal("check constraint not enforced")
	}
}

func TestPostgresMigrationFilesStaySeparate(t *testing.T) {
	names, err := fs.Glob(postgresMigrations(), "*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("missing PostgreSQL migrations: %v", err)
	}
	if !sort.StringsAreSorted(names) {
		t.Fatal("unordered PostgreSQL migrations")
	}
}
