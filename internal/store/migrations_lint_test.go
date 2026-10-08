package store

import (
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// The migration lint guards the rules at the top of migrate.go. It runs over the
// embedded set in CI, so a rebuild that would cascade-delete a parent table's
// children fails the build instead of the owner's next update.

var (
	lintRefRe = regexp.MustCompile(`(?i)REFERENCES\s+["` + "`" + `\[]?(\w+)`)
	// DROP TABLE on a parent with foreign keys on is an implicit DELETE of every
	// row (cascade, set-null or failure, whatever the child's ON DELETE says).
	lintDropRe = regexp.MustCompile(`(?i)\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?["` + "`" + `\[]?(\w+)`)
	// Renaming a parent away re-points its children at the new name, so the
	// table that later takes the old name has no children.
	lintRenameRe = regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+["` + "`" + `\[]?(\w+)["` + "`" + `\]]?\s+RENAME\s+TO\b`)
	// Transaction control as a statement of its own. A trigger's BEGIN ... END;
	// isn't matched, because its BEGIN is followed by a statement, not ';'.
	lintTxRe      = regexp.MustCompile(`(?i)\b(?:BEGIN(?:\s+(?:DEFERRED|IMMEDIATE|EXCLUSIVE))?(?:\s+TRANSACTION)?|COMMIT(?:\s+TRANSACTION)?|END\s+TRANSACTION|ROLLBACK(?:\s+TRANSACTION)?)\s*;`)
	lintPrefixRe  = regexp.MustCompile(`^(\d+)`)
	lintCommentRe = regexp.MustCompile(`--[^\n]*`)
)

// lintMigrations returns one line per rule a migration set breaks, naming the file.
func lintMigrations(t *testing.T, fsys fs.FS) []string {
	t.Helper()
	names, err := listMigrations(fsys)
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}

	var problems []string
	prefixes := map[string]string{}
	parents := map[string]bool{}
	for _, name := range names {
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		fkOff, err := migrationDirective(string(raw))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// Comments can mention anything; only statements count.
		body := lintCommentRe.ReplaceAllString(string(raw), "")

		if m := lintPrefixRe.FindString(name); m == "" {
			problems = append(problems, fmt.Sprintf("%s: name has no numeric prefix", name))
		} else if prev, dup := prefixes[m]; dup {
			problems = append(problems, fmt.Sprintf("%s: shares prefix %s with %s", name, m, prev))
		} else {
			prefixes[m] = name
		}

		// A table becomes a parent from the file that first references it, which
		// includes this one: creating a child and rebuilding its parent in the same
		// file is just as dangerous.
		for _, m := range lintRefRe.FindAllStringSubmatch(body, -1) {
			parents[strings.ToLower(m[1])] = true
		}

		if fkOff {
			if lintTxRe.MatchString(body) {
				problems = append(problems, fmt.Sprintf("%s: a %s migration must not contain BEGIN/COMMIT (the runner owns the transaction)", name, fkOffDirective))
			}
			continue
		}
		for _, m := range lintDropRe.FindAllStringSubmatch(body, -1) {
			if tbl := strings.ToLower(m[1]); parents[tbl] {
				problems = append(problems, fmt.Sprintf("%s: DROP TABLE %q, which other tables reference, needs %q as its first line", name, tbl, fkOffDirective))
			}
		}
		for _, m := range lintRenameRe.FindAllStringSubmatch(body, -1) {
			if tbl := strings.ToLower(m[1]); parents[tbl] {
				problems = append(problems, fmt.Sprintf("%s: ALTER TABLE %q RENAME TO, which other tables reference, needs %q as its first line", name, tbl, fkOffDirective))
			}
		}
	}
	return problems
}

func TestMigrationsLint(t *testing.T) {
	for _, p := range lintMigrations(t, embeddedMigrations()) {
		t.Error(p)
	}
}

// withExtra is the embedded set plus extra synthetic files.
func withExtra(t *testing.T, extra map[string]string) fstest.MapFS {
	t.Helper()
	real := embeddedMigrations()
	names, err := listMigrations(real)
	if err != nil {
		t.Fatal(err)
	}
	m := fstest.MapFS{}
	for _, n := range names {
		b, err := fs.ReadFile(real, n)
		if err != nil {
			t.Fatal(err)
		}
		m[n] = &fstest.MapFile{Data: b}
	}
	for n, body := range extra {
		m[n] = &fstest.MapFile{Data: []byte(body)}
	}
	return m
}

func TestMigrationsLintCatchesUnsafeRebuilds(t *testing.T) {
	cases := []struct {
		name, file, body, want string
	}{
		{"drop parent", "9990_x.sql", "DROP TABLE users;", `DROP TABLE "users"`},
		{"drop parent if exists", "9990_x.sql", "DROP TABLE IF EXISTS books;", `DROP TABLE "books"`},
		{"rename parent away", "9990_x.sql", "ALTER TABLE series RENAME TO x;", `ALTER TABLE "series" RENAME TO`},
		{"quoted parent", "9990_x.sql", `DROP TABLE "albums";`, `DROP TABLE "albums"`},
		{"duplicate prefix", "0001_again.sql", "SELECT 1;", "shares prefix 0001"},
		{"BEGIN in a directive file", "9990_x.sql", fkOffDirective + "\nBEGIN;\nSELECT 1;\n", "must not contain BEGIN/COMMIT"},
		{"COMMIT in a directive file", "9990_x.sql", fkOffDirective + "\nSELECT 1;\nCOMMIT;\n", "must not contain BEGIN/COMMIT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := lintMigrations(t, withExtra(t, map[string]string{tc.file: tc.body}))
			if len(problems) != 1 || !strings.Contains(problems[0], tc.file) || !strings.Contains(problems[0], tc.want) {
				t.Fatalf("want one problem naming %s and %q, got %v", tc.file, tc.want, problems)
			}
		})
	}
}

func TestMigrationsLintAllowsSafeRebuilds(t *testing.T) {
	cases := map[string]string{
		"directive rebuild of a parent":  fkOffDirective + "\nCREATE TABLE users_new (id INTEGER PRIMARY KEY);\nDROP TABLE users;\nALTER TABLE users_new RENAME TO users;\n",
		"drop of a non-parent":           "DROP TABLE convert_failures;",
		"parent only named in comment":   "-- DROP TABLE users; would be bad\nSELECT 1;",
		"trigger body in directive file": fkOffDirective + "\nCREATE TRIGGER tr AFTER INSERT ON users BEGIN SELECT 1; END;\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if problems := lintMigrations(t, withExtra(t, map[string]string{"9990_x.sql": body})); len(problems) != 0 {
				t.Fatalf("unexpected problems: %v", problems)
			}
		})
	}
}
