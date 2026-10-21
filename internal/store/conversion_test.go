package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresBidirectionalOfflineConversion(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	source, err := Open(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO users (id, username, password_hash) VALUES (9007199254740993, 'conversion-user', 'opaque-password-hash')`,
		`INSERT INTO user_notifications (user_id, title, ref) VALUES (9007199254740993, 'notification', 'original-ref')`,
		`INSERT INTO audio_passwords (user_id, hash, updated_at) VALUES (9007199254740993, 'opaque-audio-hash', 42)`,
		`INSERT INTO subtitle_jobs (id, media_key, kind, title, priority, state, queued_at) VALUES (900, 'movie:1', 'movie', 'Old job', 1, 'done', 1)`,
		`DELETE FROM subtitle_jobs`,
	} {
		if _, err := source.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	text := "unicode é\n tab\t slash\\ unknown {\"integer\":9007199254740993}"
	if _, err := source.db.ExecContext(ctx, `INSERT INTO settings (key,value) VALUES ('conversion-text',?)`, text); err != nil {
		t.Fatal(err)
	}
	for _, blob := range [][]byte{{0, 1, 255, '\n', '\\'}, {}} {
		key := "bytes"
		if len(blob) == 0 {
			key = "empty"
		}
		if _, err := source.db.ExecContext(ctx, `INSERT INTO metadata_cache (key,value,stored_at,expires_at) VALUES (?,?,0,1)`, key, blob); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	if _, err := source.db.ExecContext(ctx, `INSERT INTO books (ol_key,title,series_position,size_bytes,added_at,edits_json) VALUES ('conversion-book','Edited title',?,?,?,'{"title":"catalogue title"}')`, math.Nextafter(1, 2), int64(math.MaxInt64), stamp); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sourceDir, "arrmada.db")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	ctx, admin, opt, other := postgresFixture(t)
	ctx = conversionTestContext(t)
	if err := ConvertSQLiteToPostgres(ctx, file, opt, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(file)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatalf("SQLite source changed: %v", err)
	}
	pg, err := openPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	var got string
	if err := pg.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='conversion-text'`).Scan(&got); err != nil || got != text {
		t.Fatalf("converted text changed: %v", err)
	}
	var high int64
	if err := pg.QueryRowContext(ctx, `SELECT pg_sequence_last_value(pg_get_serial_sequence('subtitle_jobs','id')::regclass)`).Scan(&high); err != nil || high != 900 {
		t.Fatalf("SQLite pruned high-water lost: %d, %v", high, err)
	}
	// Keep a PostgreSQL-only rollback gap, including one in a SQLite non-
	// AUTOINCREMENT table. Bridge sequence entries must retain both gaps.
	if _, err := pg.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence('jobs','id')::regclass,700,true)`); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(t.TempDir(), "arrmada.db")
	if err := ConvertPostgresToSQLite(ctx, opt, targetFile); err != nil {
		t.Fatal(err)
	}
	target, err := OpenNoMigrate(filepath.Dir(targetFile))
	if err != nil {
		t.Fatal(err)
	}
	var blob []byte
	if err := target.db.QueryRowContext(ctx, `SELECT value FROM metadata_cache WHERE key='bytes'`).Scan(&blob); err != nil || !bytes.Equal(blob, []byte{0, 1, 255, '\n', '\\'}) {
		t.Fatalf("converted bytes changed: %v", err)
	}
	var typ string
	if err := target.db.QueryRowContext(ctx, `SELECT typeof(value) FROM metadata_cache WHERE key='empty'`).Scan(&typ); err != nil || typ != "blob" {
		t.Fatalf("empty BLOB became NULL: %q %v", typ, err)
	}
	var position float64
	var size, user int64
	var when time.Time
	if err := target.db.QueryRowContext(ctx, `SELECT series_position,size_bytes,added_at FROM books WHERE ol_key='conversion-book'`).Scan(&position, &size, &when); err != nil || position != math.Nextafter(1, 2) || size != math.MaxInt64 || !when.Equal(stamp) {
		t.Fatalf("numeric/timestamp round trip lost precision: %v, %v", when, err)
	}
	if err := target.db.QueryRowContext(ctx, `SELECT user_id FROM audio_passwords`).Scan(&user); err != nil || user != 9007199254740993 {
		t.Fatalf("identity/relationship lost: %d %v", user, err)
	}
	if _, err := target.db.ExecContext(ctx, `UPDATE books SET title='incoming catalogue title' WHERE ol_key='conversion-book'`); err != nil {
		t.Fatal(err)
	}
	if err := target.db.QueryRowContext(ctx, `SELECT title FROM books WHERE ol_key='conversion-book'`).Scan(&got); err != nil || got != "Edited title" {
		t.Fatalf("SQLite trigger was not restored: %q %v", got, err)
	}
	if err := target.db.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name='jobs'`).Scan(&high); err != nil || high != 700 {
		t.Fatalf("PostgreSQL gap bridge lost: %d %v", high, err)
	}
	_ = target.Close()
	_, _, second, _ := postgresFixture(t)
	if err := ConvertSQLiteToPostgres(ctx, targetFile, second, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	secondDB, err := openPostgresPool(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()
	if err := secondDB.QueryRowContext(ctx, `SELECT pg_sequence_last_value(pg_get_serial_sequence('jobs','id')::regclass)`).Scan(&high); err != nil || high != 700 {
		t.Fatalf("return to PostgreSQL reused a rollback/pruned id: %d %v", high, err)
	}
	if err := admin.QueryRow(ctx, `SELECT value FROM `+pgx.Identifier{other, "sentinel"}.Sanitize()).Scan(&got); err != nil || got != "unrelated application" {
		t.Fatalf("conversion changed unrelated schema: %q %v", got, err)
	}
}

func TestPostgresConversionRefusesLiveSourcesAndOccupiedDestinations(t *testing.T) {
	ctx, _, opt, _ := postgresFixture(t)
	dir := t.TempDir()
	source, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "arrmada.db")
	if err := ConvertSQLiteToPostgres(ctx, file, opt, t.TempDir()); !errors.Is(err, ErrDatabaseInUse) {
		t.Fatalf("live SQLite source admitted: %v", err)
	}
	_ = source.Close()
	pg, err := openPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if _, err := pg.ExecContext(ctx, `CREATE TABLE sentinel (value text); INSERT INTO sentinel VALUES ('preserved')`); err != nil {
		t.Fatal(err)
	}
	if err := ConvertSQLiteToPostgres(ctx, file, opt, t.TempDir()); !errors.Is(err, ErrPostgresSchemaOccupied) {
		t.Fatalf("occupied destination admitted: %v", err)
	}
	var sentinel string
	if err := pg.QueryRowContext(ctx, `SELECT value FROM sentinel`).Scan(&sentinel); err != nil || sentinel != "preserved" {
		t.Fatal("occupied PostgreSQL destination changed")
	}
	target := filepath.Join(t.TempDir(), "arrmada.db")
	if err := os.WriteFile(target, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ConvertPostgresToSQLite(ctx, opt, target); err == nil {
		t.Fatal("occupied SQLite destination admitted")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "occupied" {
		t.Fatal("occupied SQLite file changed")
	}
}

func TestConversionValuePreflight(t *testing.T) {
	for _, test := range []struct {
		kind     string
		value    any
		toSQLite bool
	}{
		{"TEXT", "nul\x00text", false}, {"TEXT", string([]byte{255}), false}, {"INTEGER", "123", false},
		{"REAL", math.NaN(), false}, {"REAL", math.Inf(1), true}, {"REAL", math.Copysign(0, -1), true},
		{"REAL", int64(9007199254740993), false}, {"TIMESTAMP", time.Date(2026, 1, 1, 0, 0, 0, 1, time.UTC), false},
	} {
		if _, err := normalizeConversionValue(conversionColumn{name: "test", kind: test.kind}, test.value, test.toSQLite); err == nil {
			t.Fatalf("unsupported %s value admitted", test.kind)
		}
	}
}

func TestSQLiteConversionLeasePreservesSharedRuntimeAccess(t *testing.T) {
	dir := t.TempDir()
	one, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	two, err := OpenNoMigrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if lease, err := lockSQLiteUse(dir, true); !errors.Is(err, ErrDatabaseInUse) {
		if lease != nil {
			lease.Close()
		}
		t.Fatalf("exclusive conversion admitted active runtime: %v", err)
	}
	_ = one.Close()
	_ = two.Close()
	lease, err := lockSQLiteUse(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if st, err := Open(dir); !errors.Is(err, ErrDatabaseInUse) {
		if st != nil {
			st.Close()
		}
		t.Fatalf("runtime started during conversion: %v", err)
	}
}

func TestPostgresConversionFailureCleanup(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, _, opt, _ := postgresFixture(t)
	ctx = conversionTestContext(t)
	file := filepath.Join(dir, "arrmada.db")
	injected := errors.New("injected conversion failure")
	for _, cancelAtCommit := range []bool{false, true} {
		runCtx, cancel := context.WithCancel(ctx)
		reached := false
		err := convertSQLiteToPostgres(runCtx, file, opt, t.TempDir(), func() error {
			reached = true
			if cancelAtCommit {
				cancel()
				return runCtx.Err()
			}
			return injected
		})
		cancel()
		if !reached || err == nil || (!cancelAtCommit && !errors.Is(err, injected)) {
			t.Fatalf("late conversion failure was not exercised: reached=%v err=%v", reached, err)
		}
		owner, err := openPostgresOwner(ctx, opt)
		if err != nil {
			t.Fatal(err)
		}
		err = emptyPostgresConversionDestination(ctx, owner)
		_ = owner.Close()
		if err != nil {
			t.Fatalf("failed destination not empty/retryable: %v", err)
		}
	}
	if err := ConvertSQLiteToPostgres(ctx, file, opt, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"failure", "cancel", "occupied"} {
		t.Run(mode, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "arrmada.db")
			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			reached := false
			err := convertPostgresToSQLite(runCtx, opt, target, func() error {
				reached = true
				switch mode {
				case "cancel":
					cancel()
					return nil
				case "occupied":
					return os.WriteFile(target, []byte("competitor"), 0600)
				default:
					return injected
				}
			})
			if !reached || err == nil {
				t.Fatalf("late publication failure was not exercised: reached=%v err=%v", reached, err)
			}
			if mode == "occupied" {
				data, _ := os.ReadFile(target)
				if string(data) != "competitor" {
					t.Fatal("competing destination overwritten")
				}
				return
			}
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatalf("failed destination appears usable: %v", err)
			}
			entries, _ := os.ReadDir(parent)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".arrmada-conversion-") {
					t.Fatal("private staging leaked")
				}
			}
			if err := ConvertPostgresToSQLite(ctx, opt, target); err != nil {
				t.Fatalf("immediate retry failed: %v", err)
			}
		})
	}
}

// Exit without Close to leave committed changes only in a genuine SQLite WAL.
func TestSQLiteConversionCrashHelper(t *testing.T) {
	dir := os.Getenv("ARRMADA_CONVERSION_CRASH_HELPER")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	s, err := OpenNoMigrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`PRAGMA wal_autocheckpoint=0; INSERT INTO settings (key,value) VALUES ('wal-only','committed before crash')`); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestPostgresConversionPreservesCrashedWAL(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSQLiteConversionCrashHelper$")
	cmd.Env = append(os.Environ(), "ARRMADA_CONVERSION_CRASH_HELPER="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v %s", err, out)
	}
	file := filepath.Join(dir, "arrmada.db")
	hashes := map[string][32]byte{}
	for _, path := range []string{file, file + "-wal"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 {
			t.Fatal("empty crash artifact")
		}
		hashes[path] = sha256.Sum256(data)
	}
	ctx, _, opt, _ := postgresFixture(t)
	if err := ConvertSQLiteToPostgres(ctx, file, opt, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for path, want := range hashes {
		data, err := os.ReadFile(path)
		if err != nil || sha256.Sum256(data) != want {
			t.Fatalf("source crash artifact changed: %s %v", path, err)
		}
	}
	pg, err := openPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	var got string
	if err := pg.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='wal-only'`).Scan(&got); err != nil || got != "committed before crash" {
		t.Fatalf("WAL-only commit lost: %q %v", got, err)
	}
}

func TestPostgresConversionRejectsUnsupportedSourceValues(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "arrmada.db")
	ctx, _, opt, _ := postgresFixture(t)
	ctx = conversionTestContext(t)
	for _, test := range []struct {
		name, sql string
		args      []any
	}{
		{"nul", `INSERT INTO settings (key,value) VALUES ('bad',?)`, []any{"secret\x00value"}},
		{"utf8", `INSERT INTO settings (key,value) VALUES ('bad',?)`, []any{string([]byte{255})}},
		{"integer", `INSERT INTO books (ol_key,title,size_bytes) VALUES ('bad','test',?)`, []any{"not-an-integer"}},
		{"timestamp", `INSERT INTO books (ol_key,title,added_at) VALUES ('bad','test',?)`, []any{time.Date(2026, 1, 1, 0, 0, 0, 1, time.UTC)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := s.db.ExecContext(ctx, test.sql, test.args...); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			err = ConvertSQLiteToPostgres(ctx, file, opt, t.TempDir())
			if err == nil {
				t.Fatal("unsupported source value admitted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("source value leaked")
			}
			after, _ := os.ReadFile(file)
			if !bytes.Equal(before, after) {
				t.Fatal("rejected source changed")
			}
			s, err = OpenNoMigrate(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key='bad'; DELETE FROM books WHERE ol_key='bad'`); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ConvertSQLiteToPostgres(ctx, file, opt, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	pg, err := openPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	for _, value := range []string{"'Infinity'::float8", "'-Infinity'::float8", "'NaN'::float8", "'-0'::float8"} {
		if _, err := pg.ExecContext(ctx, `INSERT INTO books (ol_key,title,series_position) VALUES ('bad','test',`+value+`)`); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "arrmada.db")
		if err := ConvertPostgresToSQLite(ctx, opt, target); err == nil {
			t.Fatalf("unsupported PostgreSQL %s admitted", value)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatal("rejected PostgreSQL value published a destination")
		}
		if _, err := pg.ExecContext(ctx, `DELETE FROM books WHERE ol_key='bad'`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresConversionRefusesLivePostgresSource(t *testing.T) {
	ctx, _, opt, _ := postgresFixture(t)
	s, err := openPostgresOwner(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.activate(ctx); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "arrmada.db")
	if err := ConvertPostgresToSQLite(ctx, opt, target); !errors.Is(err, ErrDatabaseInUse) {
		t.Fatalf("live PostgreSQL source admitted: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("live source rejection published a destination")
	}
}

func TestPostgresConversionRejectsSchemaDriftAndSourceAliases(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER unreviewed_conversion_trigger AFTER INSERT ON settings BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, _, opt, _ := postgresFixture(t)
	ctx = conversionTestContext(t)
	file := filepath.Join(dir, "arrmada.db")
	if err := ConvertSQLiteToPostgres(ctx, file, opt, t.TempDir()); !errors.Is(err, ErrConversionIncompatible) {
		t.Fatalf("SQLite schema drift admitted: %v", err)
	}
	s, err = OpenNoMigrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER unreviewed_conversion_trigger`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TABLE sqliteXunreviewed (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ConvertSQLiteToPostgres(ctx, file, opt, t.TempDir()); !errors.Is(err, ErrConversionIncompatible) {
		t.Fatalf("sqlite-prefixed application table escaped schema check: %v", err)
	}
	s, err = OpenNoMigrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE sqliteXunreviewed`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias.db")
	if err := os.Link(file, alias); err != nil {
		t.Fatal(err)
	}
	if err := ConvertSQLiteToPostgres(ctx, alias, opt, t.TempDir()); err == nil {
		t.Fatal("hard-link source alias admitted")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	// A symlink must use the real source's lease, not the alias directory's.
	if err := os.Symlink(file, alias); err != nil {
		t.Fatal(err)
	}
	s, err = OpenNoMigrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ConvertSQLiteToPostgres(ctx, alias, opt, t.TempDir()); !errors.Is(err, ErrDatabaseInUse) {
		t.Fatalf("symlink bypassed live source lease: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ConvertSQLiteToPostgres(ctx, alias, opt, t.TempDir()); err != nil {
		t.Fatalf("stopped symlink source failed: %v", err)
	}
	pg, err := openPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if _, err := pg.ExecContext(ctx, `ALTER TABLE settings ADD COLUMN unreviewed text`); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "arrmada.db")
	if err := ConvertPostgresToSQLite(ctx, opt, target); !errors.Is(err, ErrConversionIncompatible) {
		t.Fatalf("PostgreSQL schema drift admitted: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("schema drift published a destination")
	}
	if _, err := pg.ExecContext(ctx, `ALTER TABLE settings DROP COLUMN unreviewed; CREATE SEQUENCE unreviewed_state`); err != nil {
		t.Fatal(err)
	}
	if err := ConvertPostgresToSQLite(ctx, opt, target); !errors.Is(err, ErrConversionIncompatible) {
		t.Fatalf("untransferred sequence admitted: %v", err)
	}
	if _, err := pg.ExecContext(ctx, `DROP SEQUENCE unreviewed_state; ALTER TABLE settings ENABLE ROW LEVEL SECURITY; ALTER TABLE settings FORCE ROW LEVEL SECURITY; CREATE POLICY hide_rows ON settings USING (false)`); err != nil {
		t.Fatal(err)
	}
	if err := ConvertPostgresToSQLite(ctx, opt, target); !errors.Is(err, ErrConversionIncompatible) {
		t.Fatalf("row-hiding source policy admitted: %v", err)
	}
}

func conversionTestContext(t *testing.T) context.Context {
	t.Helper()
	// These scenarios execute several complete migrations/transfers and retry
	// them under -race. The fixture's 30s setup context is not their total budget.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}
