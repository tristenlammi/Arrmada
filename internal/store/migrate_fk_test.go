package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// setNullSchema adds a SET NULL child alongside the CASCADE one, so a rebuild
// that went the unsafe way would change rows in both.
const setNullSchema = parentChildSchema + `
CREATE TABLE n (id INTEGER PRIMARY KEY, p_id INTEGER REFERENCES p(id) ON DELETE SET NULL);
INSERT INTO n (id, p_id) VALUES (20, 1), (21, 2);
`

func TestFKOffMigrationKeepsChildRows(t *testing.T) {
	// Both spellings of the directive take the safe path, and so does a file an
	// editor saved with a byte-order mark in front of it.
	heads := map[string]string{"after a BOM": utf8BOM + fkOffDirective}
	for _, d := range append([]string{fkOffDirective}, fkOffAliases...) {
		heads[d] = d
	}
	for name, head := range heads {
		t.Run(name, func(t *testing.T) {
			db := testDB(t, 1)
			fsys := memFS(map[string]string{
				"0001_init.sql":      setNullSchema,
				"0002_rebuild_p.sql": head + "\n" + rebuildP,
			})
			if err := runMigrations(context.Background(), db, fsys); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			if n := count(t, db, `SELECT COUNT(*) FROM c`); n != 3 {
				t.Errorf("cascade child rows = %d, want 3", n)
			}
			if n := count(t, db, `SELECT COUNT(*) FROM n WHERE p_id IS NOT NULL`); n != 2 {
				t.Errorf("set-null child rows still pointing at a parent = %d, want 2", n)
			}
			if fk := count(t, db, `PRAGMA foreign_keys`); fk != 1 {
				t.Errorf("foreign_keys afterwards = %d, want 1", fk)
			}
		})
	}
}

func TestFKOffMigrationFailsOnViolations(t *testing.T) {
	db := testDB(t, 1)
	ctx := context.Background()
	if err := runMigrations(ctx, db, memFS(map[string]string{"0001_init.sql": parentChildSchema})); err != nil {
		t.Fatal(err)
	}
	// A correct rebuild, but the same file also writes a child row with no parent,
	// which foreign keys being off would otherwise let through.
	fsys := memFS(map[string]string{
		"0001_init.sql":      parentChildSchema,
		"0002_rebuild_p.sql": fkOffDirective + "\n" + rebuildP + "\nINSERT INTO c (id, p_id) VALUES (99, 999);\n",
	})
	err := runMigrations(ctx, db, fsys)
	if err == nil || !strings.Contains(err.Error(), "c rowid 99 -> p") {
		t.Fatalf("err = %v, want a foreign_key_check error naming c rowid 99", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM c`); n != 3 {
		t.Errorf("child rows = %d, want the original 3", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM pragma_table_info('p') WHERE name = 'extra'`); n != 0 {
		t.Error("the rebuild was applied even though the migration failed")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM schema_migrations`); n != 1 {
		t.Errorf("schema_migrations rows = %d, want 1", n)
	}
}

// A dangling reference that was already in the database isn't the migration's
// fault and mustn't block it; one the migration adds still rolls it back, and the
// error says the old ones were there first.
func TestFKOffMigrationIgnoresExistingOrphans(t *testing.T) {
	db := testDB(t, 1)
	ctx := context.Background()
	if err := runMigrations(ctx, db, memFS(map[string]string{"0001_init.sql": parentChildSchema})); err != nil {
		t.Fatal(err)
	}
	// An orphan from long ago, written the only way one can be: with foreign keys off.
	for _, q := range []string{`PRAGMA foreign_keys=OFF`, `INSERT INTO c (id, p_id) VALUES (50, 500)`, `PRAGMA foreign_keys=ON`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	addsOne := memFS(map[string]string{
		"0001_init.sql":      parentChildSchema,
		"0002_rebuild_p.sql": fkOffDirective + "\n" + rebuildP + "\nINSERT INTO c (id, p_id) VALUES (99, 999);\n",
	})
	err := runMigrations(ctx, db, addsOne)
	if err == nil || !strings.Contains(err.Error(), "found 1 dangling") || !strings.Contains(err.Error(), "c rowid 99 -> p") {
		t.Fatalf("err = %v, want exactly the new c rowid 99 reported", err)
	}
	if strings.Contains(err.Error(), "rowid 50 ") || !strings.Contains(err.Error(), "already had 1 dangling") {
		t.Fatalf("error should blame only the new row and mention the old one: %v", err)
	}

	clean := memFS(map[string]string{
		"0001_init.sql":      parentChildSchema,
		"0002_rebuild_p.sql": fkOffDirective + "\n" + rebuildP,
	})
	if err := runMigrations(ctx, db, clean); err != nil {
		t.Fatalf("a clean rebuild was blocked by an old orphan: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM c`); n != 4 {
		t.Errorf("child rows = %d, want the 3 plus the old orphan", n)
	}
}

// If foreign keys can't be switched back on, the connection must be thrown away
// rather than handed to the next caller with them off.
func TestFKOffRestoreFailureDiscardsConn(t *testing.T) {
	db := testDB(t, 4)
	ctx := context.Background()
	orig := restoreFK
	t.Cleanup(func() { restoreFK = orig })
	stuck := errors.New("stuck")
	restoreFK = func(*sql.Conn) error { return stuck }

	fsys := memFS(map[string]string{
		"0001_init.sql":      parentChildSchema,
		"0002_rebuild_p.sql": fkOffDirective + "\n" + rebuildP,
	})
	if err := runMigrations(ctx, db, fsys); !errors.Is(err, stuck) {
		t.Fatalf("err = %v, want the restore failure", err)
	}
	// Hold every connection the pool can give at once: had the pinned one gone
	// back, it would be among them, still with foreign keys off.
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

// A directive the runner doesn't recognise must stop the boot, not fall back to
// the ordinary path, which is exactly the cascade it was meant to prevent.
func TestBadDirectiveRefusesToRun(t *testing.T) {
	cases := map[string]string{
		"misspelled":             "-- arrmada:foreign-keys off\n" + rebuildP,
		"misplaced":              "-- Rebuild p to add a column.\n" + fkOffDirective + "\n" + rebuildP,
		"misspelled after a BOM": utf8BOM + "-- arrmada:foreign-keys off\n" + rebuildP,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			db := testDB(t, 1)
			fsys := memFS(map[string]string{
				"0001_init.sql":      parentChildSchema,
				"0002_rebuild_p.sql": body,
			})
			err := runMigrations(context.Background(), db, fsys)
			if err == nil || !strings.Contains(err.Error(), "0002_rebuild_p.sql") || !strings.Contains(err.Error(), "directive") {
				t.Fatalf("err = %v, want a directive error naming the file", err)
			}
			if n := count(t, db, `SELECT COUNT(*) FROM c`); n != 3 {
				t.Fatalf("child rows = %d, want 3: the migration ran anyway", n)
			}
		})
	}
}

func TestRebuildMigrationsMustDisableFKs(t *testing.T) {
	if problems := lintMigrations(t, embeddedMigrations()); len(problems) != 0 {
		t.Fatalf("embedded migrations: %v", problems)
	}

	cases := []struct {
		name  string
		extra map[string]string
		want  string
	}{
		{
			name: "rebuild of a parent introduced by a newer migration",
			extra: map[string]string{
				"9990_q.sql":       "CREATE TABLE q (id INTEGER PRIMARY KEY);\nCREATE TABLE qc (q_id INTEGER REFERENCES q(id) ON DELETE CASCADE);\n",
				"9991_rebuild.sql": "CREATE TABLE q_new (id INTEGER PRIMARY KEY);\nDROP TABLE q;\nALTER TABLE q_new RENAME TO q;\n",
			},
			want: `9991_rebuild.sql: DROP TABLE "q"`,
		},
		{
			name:  "misspelled directive",
			extra: map[string]string{"9990_x.sql": "-- arrmada:fk-off\nSELECT 1;\n"},
			want:  "9990_x.sql: unknown directive",
		},
		{
			name:  "directive below a comment",
			extra: map[string]string{"9990_x.sql": "-- why\n" + fkOffDirective + "\nSELECT 1;\n"},
			want:  "9990_x.sql: directive",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := lintMigrations(t, withExtra(t, tc.extra))
			if len(problems) != 1 || !strings.Contains(problems[0], tc.want) {
				t.Fatalf("want one problem containing %q, got %v", tc.want, problems)
			}
		})
	}

	// A table nobody references yet can be dropped the ordinary way: there are no
	// children for the drop to reach.
	ok := map[string]string{
		"9990_q.sql":  "CREATE TABLE q (id INTEGER PRIMARY KEY);\nDROP TABLE q;\n",
		"9991_qc.sql": "CREATE TABLE q (id INTEGER PRIMARY KEY);\nCREATE TABLE qc (q_id INTEGER REFERENCES q(id));\n",
	}
	if problems := lintMigrations(t, withExtra(t, ok)); len(problems) != 0 {
		t.Fatalf("drop before the table had children was flagged: %v", problems)
	}
}
