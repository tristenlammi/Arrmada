package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDatabaseSQLiteBoundary(t *testing.T) {
	pool, err := openDB(filepath.Join(t.TempDir(), "boundary.db"))
	if err != nil {
		t.Fatal(err)
	}
	db := &Database{pool: pool, dialect: SQLite}
	t.Cleanup(func() { _ = db.Close() })
	if db.Dialect() != SQLite {
		t.Fatal("wrong dialect")
	}
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE boundary_test (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO boundary_test (id, value) VALUES (?, ?)`, 1, "literal ? remains data"); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM boundary_test WHERE id = ?`, 1).Scan(&value); err != nil || value != "literal ? remains data" {
		t.Fatalf("query = %q, %v", value, err)
	}
	stmt, err := db.Prepare(`SELECT value FROM boundary_test WHERE id = ?`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	if err := stmt.QueryRowContext(ctx, 1).Scan(&value); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT id FROM boundary_test WHERE value = ?`, value)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("missing row: %v", rows.Err())
	}
	var id int64
	if err := rows.Scan(&id); err != nil || id != 1 {
		t.Fatalf("row = %d, %v", id, err)
	}
}

func FuzzPostgresBind(f *testing.F) {
	for _, query := range []string{`SELECT ?`, `SELECT '??', ?`, `/* nested /* ? */ */ ?`, `SELECT $tag$?$tag$, ?`, `SELECT E'\'?', ?`} {
		f.Add(query)
	}
	f.Fuzz(func(t *testing.T, query string) {
		// Malformed queries must be left for the database to reject, not panic
		// or lose the SQLite input while scanning a truncated quote/comment.
		_ = Postgres.bind(query)
		if got := SQLite.bind(query); got != query {
			t.Fatal("SQLite query changed")
		}
	})
}
