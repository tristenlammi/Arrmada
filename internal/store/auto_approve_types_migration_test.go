package store

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// 0171 sets every existing account's three per-type flags from the old single one, so
// nobody's requests behave differently after the upgrade.
func TestAutoApproveTypesMigration(t *testing.T) {
	ctx := context.Background()
	db, err := openDB(filepath.Join(t.TempDir(), "arrmada.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	all := embeddedMigrations()
	names, err := listMigrations(all)
	if err != nil {
		t.Fatal(err)
	}
	before := fstest.MapFS{}
	for _, n := range names {
		if n >= "0171" {
			continue
		}
		b, err := fs.ReadFile(all, n)
		if err != nil {
			t.Fatal(err)
		}
		before[n] = &fstest.MapFile{Data: b}
	}
	if err := runMigrations(ctx, db, before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users (id, username, password_hash, role, auto_approve) VALUES
		(1, 'trusted', 'x', 'requester', 1), (2, 'asks', 'x', 'requester', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, db, all); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int]int{1: 1, 2: 0} {
		var m, s, b int
		if err := db.QueryRowContext(ctx, `SELECT auto_approve_movie, auto_approve_series, auto_approve_book FROM users WHERE id = ?`, id).Scan(&m, &s, &b); err != nil {
			t.Fatal(err)
		}
		if m != want || s != want || b != want {
			t.Errorf("user %d: %d/%d/%d, want all %d", id, m, s, b, want)
		}
	}
}
