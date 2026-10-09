package store

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
)

// 0157 stamps ready_at only on approved requests whose ready notice is already in an inbox
// (by the exact ref the notifier writes), so nobody is told 'ready' again after the update.
func TestReadyAtBackfill(t *testing.T) {
	ctx := context.Background()
	db := testDB(t, 1)
	all := embeddedMigrations()
	names, err := listMigrations(all)
	if err != nil {
		t.Fatal(err)
	}
	before := fstest.MapFS{}
	for _, n := range names {
		if n >= "0157" {
			break
		}
		body, err := fs.ReadFile(all, n)
		if err != nil {
			t.Fatal(err)
		}
		before[n] = &fstest.MapFile{Data: body}
	}
	if err := runMigrations(ctx, db, before); err != nil {
		t.Fatalf("migrate to 0156: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO requests (id, media_type, tmdb_id, title, status) VALUES (1, 'movie', 603, 'The Matrix', 'approved')`,
		`INSERT INTO requests (id, media_type, tmdb_id, title, status) VALUES (2, 'movie', 604, 'Heat', 'approved')`,
		`INSERT INTO requests (id, media_type, tmdb_id, title, status) VALUES (3, 'series', 603, 'Same id, a show', 'approved')`,
		`INSERT INTO requests (id, media_type, ol_key, title, status) VALUES (4, 'book', 'OL1W', 'Dune', 'approved')`,
		`INSERT INTO requests (id, media_type, tmdb_id, title, status) VALUES (5, 'movie', 605, 'Only approved', 'approved')`,
		`INSERT INTO requests (id, media_type, tmdb_id, title, status) VALUES (6, 'movie', 606, 'Declined', 'declined')`,
		`INSERT INTO user_notifications (user_id, ref, created_at) VALUES (7, 'movie:603', 1700000100)`,
		`INSERT INTO user_notifications (user_id, ref, created_at) VALUES (8, 'movie:603', 1700000000)`,
		`INSERT INTO user_notifications (user_id, ref, created_at) VALUES (7, 'book:OL1W', 0)`,
		`INSERT INTO user_notifications (user_id, ref, created_at) VALUES (7, 'movie:605:approved', 1700000000)`,
		`INSERT INTO user_notifications (user_id, ref, created_at) VALUES (7, 'movie:606', 1700000000)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := runMigrations(ctx, db, all); err != nil {
		t.Fatalf("migrate the rest: %v", err)
	}
	want := map[int64]int64{1: 1700000000, 2: 0, 3: 0, 4: 1, 5: 0, 6: 0}
	for id, at := range want {
		var got int64
		if err := db.QueryRow(`SELECT ready_at FROM requests WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != at {
			t.Errorf("request %d: ready_at = %d, want %d", id, got, at)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_requests_section'`); n != 1 {
		t.Error("idx_requests_section missing")
	}
}
