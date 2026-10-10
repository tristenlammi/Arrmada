package store

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// 0173 gives existing admin channels the Needs-you alerts that are on by default: a
// connection that wants grabs, imports or auto-approvals, and every push connection. A
// Plex-only family channel, or one with nothing ticked, gets none.
func TestAttentionStateBackfillsNeedsYouSubscriptions(t *testing.T) {
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
		if n >= "0173" {
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
		`INSERT INTO notification_subscriptions (connection_id, event_key) VALUES (1, 'release.grabbed'), (1, 'health.problem'),
			(2, 'plex.stream.started'), (5, 'request.auto_approved')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := runMigrations(ctx, db, all); err != nil {
		t.Fatal(err)
	}

	needsYou := "download.failed,health.problem,health.resolved,import.held,import.stuck"
	want := map[int64]string{
		1: "download.failed,health.problem,health.resolved,import.held,import.stuck,release.grabbed",
		2: "plex.stream.started",
		3: needsYou,
		4: "",
		5: needsYou + ",request.auto_approved",
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
	if _, err := db.ExecContext(ctx, `INSERT INTO attention_state (key, kind, first_seen, last_seen) VALUES ('health:x', 'health', 1, 1)`); err != nil {
		t.Errorf("attention_state: %v", err)
	}
}
