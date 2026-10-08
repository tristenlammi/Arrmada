package store

import (
	"context"
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
	// Both spellings of the directive take the safe path.
	for _, directive := range append([]string{fkOffDirective}, fkOffAliases...) {
		t.Run(directive, func(t *testing.T) {
			db := testDB(t, 1)
			fsys := memFS(map[string]string{
				"0001_init.sql":      setNullSchema,
				"0002_rebuild_p.sql": directive + "\n" + rebuildP,
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

// A directive the runner doesn't recognise must stop the boot, not fall back to
// the ordinary path, which is exactly the cascade it was meant to prevent.
func TestBadDirectiveRefusesToRun(t *testing.T) {
	cases := map[string]string{
		"misspelled": "-- arrmada:foreign-keys off\n" + rebuildP,
		"misplaced":  "-- Rebuild p to add a column.\n" + fkOffDirective + "\n" + rebuildP,
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
