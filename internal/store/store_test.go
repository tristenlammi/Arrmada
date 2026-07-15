package store

import (
	"context"
	"sort"
	"testing"
)

func TestOpenRunsMigrations(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}

	want := []string{"api_keys", "schema_migrations", "sessions", "settings", "users"}
	got := tableNames(t, st)
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("expected table %q to exist; have %v", w, got)
		}
	}

	var applied int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied < 1 {
		t.Errorf("expected at least one applied migration, got %d", applied)
	}
}

// TestMigrationsAreIdempotent proves reopening the same data dir doesn't
// re-apply or error on already-applied migrations.
func TestMigrationsAreIdempotent(t *testing.T) {
	dir := t.TempDir()

	st1, err := Open(dir)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	var first int
	_ = st1.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&first)
	_ = st1.Close()

	st2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	var second int
	_ = st2.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&second)
	if first != second {
		t.Errorf("migration count changed on reopen: %d -> %d", first, second)
	}
}

func tableNames(t *testing.T, st *Store) []string {
	t.Helper()
	rows, err := st.DB().Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
