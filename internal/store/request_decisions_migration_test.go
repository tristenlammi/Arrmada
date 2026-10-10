package store

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// 0170 adds the decision columns, and gives existing admin channels the "New request"
// alert: a connection that wants grabs, imports or auto-approvals, and every push
// connection — not a Plex-only family channel or one with nothing ticked.
func TestRequestDecisionsMigration(t *testing.T) {
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
		if n >= "0170" {
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
	for _, q := range []string{
		`INSERT INTO notifications (id, name, kind, url) VALUES (1, 'grabs', '', 'ntfy://a'), (2, 'plex', '', 'ntfy://b'),
			(3, 'phone', 'webpush', ''), (4, 'none', '', 'ntfy://d'), (5, 'auto', '', 'ntfy://e')`,
		`INSERT INTO notification_subscriptions (connection_id, event_key) VALUES (1, 'movie.imported'),
			(2, 'plex.stream.started'), (5, 'request.auto_approved')`,
		`INSERT INTO requests (media_type, tmdb_id, title, status) VALUES ('movie', 1, 'Old', 'declined')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := runMigrations(ctx, db, all); err != nil {
		t.Fatal(err)
	}

	for id, want := range map[int64]bool{1: true, 2: false, 3: true, 4: false, 5: true} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_subscriptions WHERE connection_id = ? AND event_key = 'request.created'`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if (n == 1) != want {
			t.Errorf("connection %d subscribed to request.created = %v, want %v", id, n == 1, want)
		}
	}
	var reason, byName string
	var by, at, again int
	if err := db.QueryRowContext(ctx, `SELECT decline_reason, decided_by, decided_by_name, decided_at, rerequest FROM requests`).Scan(&reason, &by, &byName, &at, &again); err != nil {
		t.Fatal(err)
	}
	if reason != "" || by != 0 || byName != "" || at != 0 || again != 0 {
		t.Errorf("an old request's decision columns = %q %d %q %d %d, want empty", reason, by, byName, at, again)
	}
}
