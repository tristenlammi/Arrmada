package store

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// 0160 turns the old per-event columns into subscription rows: every connection keeps
// exactly its previous choices, and "Imported" connections also get book imports.
func TestSubscriptionsBackfill(t *testing.T) {
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
	// Up to 0160 only: later migrations (0173's Needs-you backfill) add subscriptions of
	// their own and have their own tests.
	before, through := fstest.MapFS{}, fstest.MapFS{}
	for _, n := range names {
		if n >= "0161" {
			continue
		}
		b, err := fs.ReadFile(all, n)
		if err != nil {
			t.Fatal(err)
		}
		through[n] = &fstest.MapFile{Data: b}
		if n < "0160" {
			before[n] = &fstest.MapFile{Data: b}
		}
	}
	if err := runMigrations(ctx, db, before); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO notifications (id, name, url, on_grab, on_import, on_stream, on_buffering) VALUES (1, 'all', 'ntfy://a', 1, 1, 1, 1)`,
		`INSERT INTO notifications (id, name, url, on_grab, on_import, on_stream, on_buffering) VALUES (2, 'grabs', 'ntfy://b', 1, 0, 0, 0)`,
		`INSERT INTO notifications (id, name, url, on_grab, on_import, on_stream, on_buffering) VALUES (3, 'plex', 'ntfy://c', 0, 0, 1, 0)`,
		`INSERT INTO notifications (id, name, url, on_grab, on_import, on_stream, on_buffering) VALUES (4, 'none', 'ntfy://d', 0, 0, 0, 0)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := runMigrations(ctx, db, through); err != nil {
		t.Fatal(err)
	}

	want := map[int64]string{
		1: "book.imported,episodes.imported,movie.imported,plex.buffering,plex.stream.started,release.grabbed",
		2: "release.grabbed",
		3: "plex.stream.started",
		4: "",
	}
	for id, w := range want {
		rows, err := db.QueryContext(ctx, `SELECT event_key FROM notification_subscriptions WHERE connection_id = ? ORDER BY event_key`, id)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				t.Fatal(err)
			}
			got = append(got, k)
		}
		rows.Close()
		if strings.Join(got, ",") != w {
			t.Errorf("connection %d: %v, want %s", id, got, w)
		}
	}

	// Deleting a connection takes its subscriptions with it.
	if _, err := db.ExecContext(ctx, `DELETE FROM notifications WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_subscriptions WHERE connection_id = 1`).Scan(&n); err != nil || n != 0 {
		t.Errorf("subscriptions left after delete: %d (%v)", n, err)
	}
}
