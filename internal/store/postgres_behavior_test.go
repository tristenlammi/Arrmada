package store

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sync"
	"testing"
)

func behaviorDatabases(t *testing.T) (context.Context, map[string]Execer) {
	t.Helper()
	if os.Getenv("ARRMADA_TEST_POSTGRES_ADMIN_URL") == "" {
		t.Skip("set ARRMADA_TEST_POSTGRES_ADMIN_URL to run paired database behavior tests")
	}
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, pg, _, _ := postgresTestPool(t)
	if err := migratePostgres(ctx, pg, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	return ctx, map[string]Execer{"sqlite": st.DB(), "postgres": pg}
}

func TestPostgresBookTriggerParity(t *testing.T) {
	ctx, databases := behaviorDatabases(t)
	for name, db := range databases {
		t.Run(name, func(t *testing.T) {
			mustExec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			mustExec(`INSERT INTO books (id, ol_key, title) VALUES (1, 'work/1', 'Original title')`)
			mustExec(`UPDATE books SET title = 'Hand title', subtitle = 'Hand subtitle', author = 'Hand author',
				series_name = 'Hand series', series_position = 2.5, description = 'Hand description', year = 2024,
				language = 'en', isbn = '123', audiobook_narrator = 'Hand narrator', publisher = 'Hand publisher',
				subjects_json = '["hand"]', edited_at = 1,
				edits_json = '{"title":"Original title","subtitle":"","author":"","series_name":"","series_position":0,"description":"","year":0,"language":"","isbn":"","narrator":"","publisher":"","subjects":[]}'
				WHERE id = 1`)
			var initialEdits string
			if err := db.QueryRowContext(ctx, `SELECT edits_json FROM books WHERE id = 1`).Scan(&initialEdits); err != nil {
				t.Fatal(err)
			}
			const future int64 = 9_000_000_000_000_000
			mustExec(`UPDATE books SET meta_updated_at = ?, monitored = 0 WHERE id = 1`, future)
			var unchangedEdits string
			if err := db.QueryRowContext(ctx, `SELECT edits_json FROM books WHERE id = 1`).Scan(&unchangedEdits); err != nil || initialEdits != unchangedEdits {
				t.Fatalf("unrelated update rewrote edits: %q vs %q, %v", initialEdits, unchangedEdits, err)
			}
			mustExec(`UPDATE books SET title = ?, subtitle = ?, author = ?, series_name = ?, series_position = ?,
				description = ?, year = ?, language = ?, isbn = ?, audiobook_narrator = ?, publisher = ?, subjects_json = ? WHERE id = 1`,
				"Catalogue title", "Catalogue subtitle", "Catalogue author", "Catalogue series", 3.5,
				"Catalogue description", 2001, "fr", "456", "Catalogue narrator", "Catalogue publisher", `["catalogue"]`)
			want := []string{"Hand title", "Hand subtitle", "Hand author", "Hand series", "2.5", "Hand description", "2024", "en", "123", "Hand narrator", "Hand publisher", `["hand"]`}
			got := make([]string, len(want))
			ptrs := make([]any, len(got))
			for i := range got {
				ptrs[i] = &got[i]
			}
			if err := db.QueryRowContext(ctx, `SELECT title, subtitle, author, series_name, series_position, description, year, language, isbn, audiobook_narrator, publisher, subjects_json FROM books WHERE id = 1`).Scan(ptrs...); err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("catalogue overwrote hand edits: %v, %v", got, err)
			}
			var edits string
			var meta int64
			if err := db.QueryRowContext(ctx, `SELECT edits_json, meta_updated_at FROM books WHERE id = 1`).Scan(&edits, &meta); err != nil || meta != future {
				t.Fatalf("hidden catalogue update changed metadata time: %d, %v", meta, err)
			}
			var catalogue map[string]any
			if err := json.Unmarshal([]byte(edits), &catalogue); err != nil {
				t.Fatal(err)
			}
			wantCatalogue := map[string]any{
				"title": "Catalogue title", "subtitle": "Catalogue subtitle", "author": "Catalogue author",
				"series_name": "Catalogue series", "series_position": 3.5, "description": "Catalogue description",
				"year": float64(2001), "language": "fr", "isbn": "456", "narrator": "Catalogue narrator",
				"publisher": "Catalogue publisher", "subjects": []any{"catalogue"},
			}
			if !reflect.DeepEqual(catalogue, wantCatalogue) {
				t.Fatalf("catalogue underneath edits = %#v", catalogue)
			}
			mustExec(`UPDATE books SET title = 'Another catalogue title', cover_url = 'new cover' WHERE id = 1`)
			if err := db.QueryRowContext(ctx, `SELECT meta_updated_at FROM books WHERE id = 1`).Scan(&meta); err != nil || meta != future+1 {
				t.Fatalf("visible update did not move metadata monotonically: %d, %v", meta, err)
			}
			mustExec(`INSERT INTO book_audio_versions (id, book_id, label) VALUES (1, 1, 'Original version')`)
			mustExec(`UPDATE book_audio_versions SET narrator = 'Version narrator' WHERE id = 1`)
			mustExec(`UPDATE book_audio_versions SET narrator = 'Version narrator' WHERE id = 1`)
			if err := db.QueryRowContext(ctx, `SELECT meta_updated_at FROM books WHERE id = 1`).Scan(&meta); err != nil || meta != future+2 {
				t.Fatalf("version change/no-op metadata = %d, %v", meta, err)
			}
			if _, err := db.ExecContext(ctx, `UPDATE books SET subjects_json = 'not JSON' WHERE id = 1`); err == nil {
				t.Fatal("invalid catalogue subjects accepted")
			}
		})
	}
}

func assertViewingFlags(t *testing.T, ctx context.Context, db Execer) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT id, viewing_start,
		CASE WHEN previous_stop IS NULL OR started_at - previous_stop > 10800 THEN 1 ELSE 0 END
		FROM (SELECT id, viewing_start, started_at, LAG(stopped_at) OVER (
			PARTITION BY user_id, CASE WHEN rating_key <> '' THEN rating_key
			ELSE media_type || '|' || title || '|' || grandparent_title || '|' || parent_index || '|' || media_index END
			ORDER BY started_at, id) AS previous_stop FROM stream_sessions) viewings ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var got, want int
		if err := rows.Scan(&id, &got, &want); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("session %d viewing_start = %d, want %d", id, got, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresViewingTriggerParity(t *testing.T) {
	ctx, databases := behaviorDatabases(t)
	for name, db := range databases {
		t.Run(name, func(t *testing.T) {
			for _, query := range []string{
				`INSERT INTO stream_sessions (id, user_id, rating_key, started_at, stopped_at) VALUES (3, 'a', 'item', 30000, 30100)`,
				`INSERT INTO stream_sessions (id, user_id, rating_key, started_at, stopped_at) VALUES (1, 'a', 'item', 100, 200)`,
				`INSERT INTO stream_sessions (id, user_id, rating_key, started_at, stopped_at) VALUES (2, 'a', 'item', 10000, 20000)`,
				`UPDATE stream_sessions SET stopped_at = 10050 WHERE id = 2`,
				`DELETE FROM stream_sessions WHERE id = 2`,
				`UPDATE stream_sessions SET started_at = 500, stopped_at = 600 WHERE id = 3`,
				`INSERT INTO stream_sessions (id, user_id, rating_key, started_at, stopped_at) VALUES (4, 'b', 'item', 100, 200)`,
				`INSERT INTO stream_sessions (id, user_id, media_type, title, grandparent_title, parent_index, media_index, started_at, stopped_at) VALUES (5, 'a', 'episode', 'Pilot', 'Show', 1, 1, 1000, 1100)`,
				`INSERT INTO stream_sessions (id, user_id, media_type, title, grandparent_title, parent_index, media_index, started_at, stopped_at) VALUES (6, 'a', 'episode', 'Pilot', 'Show', 1, 1, 1200, 1300)`,
				`UPDATE stream_sessions SET media_index = 2 WHERE id = 6`,
				`UPDATE stream_sessions SET media_index = 1, user_id = 'b' WHERE id = 6`,
				`UPDATE stream_sessions SET media_index = 1, user_id = 'a', started_at = 1000, stopped_at = 1050 WHERE id = 6`,
				`DELETE FROM stream_sessions WHERE id = 5`,
			} {
				if _, err := db.ExecContext(ctx, query); err != nil {
					t.Fatalf("%s: %v", query, err)
				}
				assertViewingFlags(t, ctx, db)
			}
		})
	}
}

func TestPostgresConcurrentViewingWrites(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	var writers sync.WaitGroup
	for i := range 32 {
		writers.Go(func() {
			if _, err := db.ExecContext(ctx, `INSERT INTO stream_sessions (user_id, rating_key, started_at, stopped_at) VALUES (?, ?, ?, ?)`, "same user", "same item", i*5000, i*5000+100); err != nil {
				t.Errorf("concurrent insert: %v", err)
			}
		})
	}
	writers.Wait()
	assertViewingFlags(t, ctx, db)
	var count, starts int
	if err := db.QueryRowContext(ctx, `SELECT count(*), sum(viewing_start) FROM stream_sessions`).Scan(&count, &starts); err != nil || count != 32 || starts != 1 {
		t.Fatalf("concurrent viewing chain = %d rows, %d starts, %v", count, starts, err)
	}
}
