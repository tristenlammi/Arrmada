package store

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
)

// 0145 gives every existing show "monitor new seasons" equal to its series flag, so a
// library-scanned show (added unmonitored) doesn't start monitoring new seasons on its own.
func TestMonitorNewSeasonsMigrationBackfill(t *testing.T) {
	ctx := context.Background()
	full := embeddedMigrations()
	names, err := listMigrations(full)
	if err != nil {
		t.Fatal(err)
	}
	before := fstest.MapFS{}
	for _, n := range names {
		if n >= "0145" {
			continue
		}
		b, err := fs.ReadFile(full, n)
		if err != nil {
			t.Fatal(err)
		}
		before[n] = &fstest.MapFile{Data: b}
	}
	db := testDB(t, 1)
	if err := runMigrations(ctx, db, before); err != nil {
		t.Fatalf("migrate to 0144: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO series (id, tmdb_id, title, monitored) VALUES (1, 1, 'On', 1), (2, 2, 'Scanned', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, db, full); err != nil {
		t.Fatalf("migrate the rest: %v", err)
	}
	if n := count(t, db, `SELECT monitor_new_seasons FROM series WHERE id = 1`); n != 1 {
		t.Errorf("monitored show: monitor_new_seasons = %d, want 1", n)
	}
	if n := count(t, db, `SELECT monitor_new_seasons FROM series WHERE id = 2`); n != 0 {
		t.Errorf("library-scanned show: monitor_new_seasons = %d, want 0", n)
	}
}
