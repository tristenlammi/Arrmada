package store

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
)

// 0164 keeps every request (as whole-show), lets a show have several requests, and still
// allows one request per movie.
func TestRequestSeasonsMigration(t *testing.T) {
	ctx := context.Background()
	full := embeddedMigrations()
	names, err := listMigrations(full)
	if err != nil {
		t.Fatal(err)
	}
	before := fstest.MapFS{}
	for _, n := range names {
		if n >= "0164" {
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
		t.Fatalf("migrate to 0163: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO requests (id, media_type, tmdb_id, title, status) VALUES
		(1, 'series', 77, 'Show', 'approved'), (2, 'movie', 5, 'Film', 'pending')`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, db, full); err != nil {
		t.Fatalf("migrate the rest: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM requests WHERE seasons = ''`); n != 2 {
		t.Errorf("legacy rows with whole-show seasons = %d, want 2", n)
	}
	if _, err := db.Exec(`INSERT INTO requests (media_type, tmdb_id, title, status, seasons) VALUES ('series', 77, 'Show', 'pending', '[4]')`); err != nil {
		t.Errorf("a second request for the show: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO requests (media_type, tmdb_id, title, status) VALUES ('movie', 5, 'Film', 'pending')`); err == nil {
		t.Error("a second request for the same movie was accepted")
	}
}
