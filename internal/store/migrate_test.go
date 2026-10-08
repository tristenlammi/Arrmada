package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// parentChildSchema is a parent p with a CASCADE child c, the shape of users and
// its sessions/listen_progress children.
const parentChildSchema = `
CREATE TABLE p (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE c (id INTEGER PRIMARY KEY, p_id INTEGER NOT NULL REFERENCES p(id) ON DELETE CASCADE);
INSERT INTO p (id, name) VALUES (1, 'a'), (2, 'b');
INSERT INTO c (id, p_id) VALUES (10, 1), (11, 1), (12, 2);
`

// rebuildP rebuilds p with an extra column using the 12-step recipe.
const rebuildP = `
CREATE TABLE p_new (id INTEGER PRIMARY KEY, name TEXT NOT NULL, extra TEXT NOT NULL DEFAULT '');
INSERT INTO p_new (id, name) SELECT id, name FROM p;
DROP TABLE p;
ALTER TABLE p_new RENAME TO p;
`

func memFS(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, body := range files {
		m[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return m
}

// testDB opens a pool on a temp file with the same pragmas as production.
func testDB(t *testing.T, maxConns int) *sql.DB {
	t.Helper()
	db, err := openDB(filepath.Join(t.TempDir(), "arrmada.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func count(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestFKOffRebuildKeepsCascadeChildren(t *testing.T) {
	// One connection, so the pinned migration conn is the one checked afterwards.
	db := testDB(t, 1)
	ctx := context.Background()
	fsys := memFS(map[string]string{
		"0001_init.sql":      parentChildSchema,
		"0002_rebuild_p.sql": "\n" + fkOffDirective + "\n" + rebuildP,
	})
	if err := runMigrations(ctx, db, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM c`); n != 3 {
		t.Fatalf("child rows after rebuild = %d, want 3", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM p WHERE extra = ''`); n != 2 {
		t.Fatalf("parent rows after rebuild = %d, want 2", n)
	}
	if fk := count(t, db, `PRAGMA foreign_keys`); fk != 1 {
		t.Fatalf("foreign_keys after directive migration = %d, want 1", fk)
	}
	// The child's FK still points at the rebuilt table and still cascades.
	if _, err := db.Exec(`DELETE FROM p WHERE id = 1`); err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM c`); n != 1 {
		t.Fatalf("cascade after rebuild left %d child rows, want 1", n)
	}
}

func TestFKOffEveryPooledConnHasForeignKeysOn(t *testing.T) {
	db := testDB(t, 4)
	ctx := context.Background()
	fsys := memFS(map[string]string{
		"0001_init.sql":      parentChildSchema,
		"0002_rebuild_p.sql": fkOffDirective + "\n" + rebuildP,
	})
	if err := runMigrations(ctx, db, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Hold every connection the pool can give at once, so the one the migration
	// pinned is certainly among them.
	conns := make([]*sql.Conn, 4)
	for i := range conns {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		conns[i] = c
	}
	for i, c := range conns {
		var fk int
		if err := c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		if fk != 1 {
			t.Errorf("conn %d foreign_keys = %d, want 1", i, fk)
		}
		_ = c.Close()
	}
}

func TestFKOffRebuildThatOrphansRowsRollsBack(t *testing.T) {
	db := testDB(t, 1)
	ctx := context.Background()
	if err := runMigrations(ctx, db, memFS(map[string]string{"0001_init.sql": parentChildSchema})); err != nil {
		t.Fatalf("migrate 0001: %v", err)
	}
	// The copy forgets parent 2, so child 12 would dangle.
	bad := strings.Replace(rebuildP, "FROM p;", "FROM p WHERE id = 1;", 1)
	fsys := memFS(map[string]string{
		"0001_init.sql":      parentChildSchema,
		"0002_rebuild_p.sql": fkOffDirective + "\n" + bad,
	})
	err := runMigrations(ctx, db, fsys)
	if err == nil {
		t.Fatal("expected a foreign_key_check error")
	}
	if !strings.Contains(err.Error(), "foreign_key_check") || !strings.Contains(err.Error(), "c rowid 12 -> p") {
		t.Fatalf("error doesn't name the dangling row: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version = '0002_rebuild_p'`); n != 0 {
		t.Fatal("failed migration was recorded")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM p`); n != 2 {
		t.Fatalf("parent rows = %d, want the untouched 2", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM pragma_table_info('p') WHERE name = 'extra'`); n != 0 {
		t.Fatal("rebuilt table leaked out of the rolled-back migration")
	}
	if fk := count(t, db, `PRAGMA foreign_keys`); fk != 1 {
		t.Fatalf("foreign_keys after failed migration = %d, want 1", fk)
	}
}

// TestRebuildWithoutDirectiveCascades documents the trap the directive exists
// for: the same rebuild run the ordinary way silently deletes every child row.
// The lint test rejects this file before it can ship.
func TestRebuildWithoutDirectiveCascades(t *testing.T) {
	db := testDB(t, 1)
	fsys := memFS(map[string]string{
		"0001_init.sql":      parentChildSchema,
		"0002_rebuild_p.sql": rebuildP,
	})
	if err := runMigrations(context.Background(), db, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM c`); n != 0 {
		t.Fatalf("expected the cascade to empty c (that's the bug the directive prevents), have %d rows", n)
	}
	problems := lintMigrations(t, fsys)
	if len(problems) == 0 || !strings.Contains(problems[0], "0002_rebuild_p.sql") || !strings.Contains(problems[0], `"p"`) {
		t.Fatalf("lint didn't flag the undirected rebuild: %v", problems)
	}
}

func TestOpenWithBeforeMigrate(t *testing.T) {
	dir := t.TempDir()
	fsys := memFS(map[string]string{
		"0001_a.sql": `CREATE TABLE a (id INTEGER PRIMARY KEY);`,
		"0002_b.sql": `CREATE TABLE b (id INTEGER PRIMARY KEY);`,
	})
	var (
		mu    sync.Mutex
		calls [][]string
	)
	hook := func(_ context.Context, _ *sql.DB, pending []string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, append([]string(nil), pending...))
		return nil
	}

	st, err := OpenWith(dir, Options{BeforeMigrate: hook, migrations: fsys})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = st.Close()
	if want := [][]string{{"0001_a.sql", "0002_b.sql"}}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("hook calls = %v, want %v", calls, want)
	}

	// Nothing pending: the hook isn't called.
	st, err = OpenWith(dir, Options{BeforeMigrate: hook, migrations: fsys})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = st.Close()
	if len(calls) != 1 {
		t.Fatalf("hook ran with nothing pending: %v", calls)
	}

	// A failing hook aborts Open before the new migration applies.
	fsys["0003_c.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE c (id INTEGER PRIMARY KEY);`)}
	boom := errors.New("boom")
	_, err = OpenWith(dir, Options{
		BeforeMigrate: func(context.Context, *sql.DB, []string) error { return boom },
		migrations:    fsys,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("open with failing hook: err = %v, want boom", err)
	}
	st, err = OpenWith(dir, Options{migrations: memFS(map[string]string{
		"0001_a.sql": `CREATE TABLE a (id INTEGER PRIMARY KEY);`,
		"0002_b.sql": `CREATE TABLE b (id INTEGER PRIMARY KEY);`,
	})})
	if err != nil {
		t.Fatalf("reopen to inspect: %v", err)
	}
	defer st.Close()
	if n := count(t, st.DB(), `SELECT COUNT(*) FROM sqlite_master WHERE name = 'c'`); n != 0 {
		t.Fatal("0003 applied even though BeforeMigrate failed")
	}
}
