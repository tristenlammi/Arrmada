package store

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
)

// 0152 applies over a database that already holds grabs — the shape every install has
// before it — and leaves those rows readable with the new fields at their zero values.
func TestGrabAcquisitionMigration(t *testing.T) {
	ctx := context.Background()
	db := testDB(t, 1)
	all := embeddedMigrations()
	names, err := listMigrations(all)
	if err != nil {
		t.Fatal(err)
	}
	// Everything before 0152, as an existing install has it.
	before := fstest.MapFS{}
	for _, n := range names {
		if n >= "0152" {
			break
		}
		body, err := fs.ReadFile(all, n)
		if err != nil {
			t.Fatal(err)
		}
		before[n] = &fstest.MapFile{Data: body}
	}
	if err := runMigrations(ctx, db, before); err != nil {
		t.Fatalf("migrate to 0151: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO grabs (movie_id, version_id, title, media_type, info_hash, status)
		VALUES (7, 2, 'Some.Film.2001.1080p', 'movie', 'ABC', 'grabbed')`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, db, all); err != nil {
		t.Fatalf("migrate the rest: %v", err)
	}
	var phase, scope, lastErr string
	var progress float64
	var progressAt, clientID, seen, done, upd, waiting int64
	if err := db.QueryRow(`SELECT phase, progress, progress_at, acq_scope, client_id, last_seen_at, completed_at,
		updated_at, last_error, still_waiting_at FROM grabs WHERE title = 'Some.Film.2001.1080p'`).Scan(
		&phase, &progress, &progressAt, &scope, &clientID, &seen, &done, &upd, &lastErr, &waiting); err != nil {
		t.Fatal(err)
	}
	if phase != "" || progress != 0 || progressAt != 0 || scope != "" || clientID != 0 || seen != 0 || done != 0 || upd != 0 || lastErr != "" || waiting != 0 {
		t.Errorf("an existing grab's new fields aren't at their zero values")
	}
	if n := count(t, db, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_grabs_item_status'`); n != 1 {
		t.Error("idx_grabs_item_status missing")
	}
}
