package libfolders

import (
	"context"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Every path column in the schema is either repointed by a move or listed as ignored with a
// reason. A new table remembering where a library file is fails here until it's decided.
func TestRewriteCoversEveryPathColumn(t *testing.T) {
	f := newFixture(t)
	db := f.st.DB()
	known := map[string]bool{}
	for _, c := range pathColumns {
		known[c.table+"."+c.column] = true
	}
	for k := range ignoredPathColumns {
		known[k] = true
	}
	pathLike := regexp.MustCompile(`^(path|folder|.*_path|.*_file)$`)
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	var missing []string
	for _, tbl := range tables {
		cols, err := db.Query(`SELECT name FROM pragma_table_info(?)`, tbl)
		if err != nil {
			t.Fatal(err)
		}
		for cols.Next() {
			var c string
			if err := cols.Scan(&c); err != nil {
				t.Fatal(err)
			}
			if c == "has_file" || !pathLike.MatchString(c) {
				continue
			}
			if !known[tbl+"."+c] {
				missing = append(missing, tbl+"."+c)
			}
		}
		cols.Close()
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("path columns neither repointed nor ignored (add them to pathColumns or ignoredPathColumns): %v", missing)
	}
	// And every listed column exists, so a rename in a migration can't leave a dead entry.
	for _, c := range pathColumns {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s.%s isn't in the schema", c.table, c.column)
		}
	}
}

// RewritePrefix repoints every table's paths under the old folder, on whole path segments,
// and treats "%" and "_" in names as themselves.
func TestRewritePrefixRepointsEveryTable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	db := f.st.DB()
	oldDir := filepath.Join(f.movies, "Heat_100% (1995)")
	newDir := filepath.Join(f.base, "media", "kids", "Heat_100% (1995)")
	// A neighbour sharing the prefix, and a name LIKE would have matched by its wildcards.
	twin := filepath.Join(f.movies, "Heat_100% (1995) Director's Cut", "Heat.mkv")
	likeTrap := filepath.Join(f.movies, "HeatX100%Z(1995)", "Heat.mkv")
	file := filepath.Join(oldDir, "Heat.mkv")
	sub := filepath.Join(oldDir, "Heat.en.srt")

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO movies (id, tmdb_id, title, has_file, movie_file_path, media_json) VALUES (1, 1, 'Heat', 1, ?, ?)`, file, `{"path":"`+jsonEsc(file)+`","codec":"hevc"}`)
	exec(`INSERT INTO movies (id, tmdb_id, title, has_file, movie_file_path) VALUES (2, 2, 'Heat DC', 1, ?)`, twin)
	exec(`INSERT INTO movies (id, tmdb_id, title, has_file, movie_file_path) VALUES (3, 3, 'Trap', 1, ?)`, likeTrap)
	exec(`INSERT INTO movie_versions (movie_id, label, has_file, file_path, media_json) VALUES (1, '4K', 1, ?, ?)`, filepath.Join(oldDir, "Heat 4K.mkv"), `{"path":"`+jsonEsc(filepath.Join(oldDir, "Heat 4K.mkv"))+`"}`)
	exec(`INSERT INTO imports (download_hash, source_path, target_path) VALUES ('h1', '/downloads/Heat', ?)`, file)
	exec(`INSERT INTO grabs (movie_id, title, replaces_path) VALUES (1, 'Heat.2160p', ?)`, file)
	exec(`INSERT INTO convert_library (path, media_type, movie_id) VALUES (?, 'movie', 1)`, file)
	// A stale cache row already at the new path is replaced, not a failed move.
	exec(`INSERT INTO convert_probe_cache (path, size_bytes, mtime_unix, info_json) VALUES (?, 1, 1, '{}'), (?, 2, 2, '{"stale":true}')`, file, filepath.Join(newDir, "Heat.mkv"))
	exec(`INSERT INTO convert_choices (path, size_bytes, codec, decided_at) VALUES (?, 1, 'hevc', 1)`, file)
	exec(`INSERT INTO convert_measured (path, codec, size_bytes, crf, video_bytes, measured_at) VALUES (?, 'av1', 1, 30, 1, 1)`, file)
	exec(`INSERT INTO convert_history (item_key, kind, outcome, src_path, out_path) VALUES ('movie:1', 'movie', 'done', ?, ?)`, file, file)
	exec(`INSERT INTO subtitle_files (media_key, kind, movie_id, video_path, path, lang, source, created_at) VALUES ('movie:1', 'movie', 1, ?, ?, 'en', 'provider', 1)`, file, sub)
	exec(`INSERT INTO subtitle_attempts (run_id, media_key, video_path, lang, rung, outcome, at) VALUES ('r', 'movie:1', ?, 'en', 'provider', 'ok', 1)`, file)
	exec(`INSERT INTO subtitle_backoff (media_key, lang, video_path, primary_reason, reasons_json, updated_at) VALUES ('movie:1', 'en', ?, 'none', '{}', 1)`, file)
	exec(`INSERT INTO subtitle_probe_cache (path, size_bytes, mtime_unix, info_json) VALUES (?, 1, 1, '{}')`, file)
	exec(`INSERT INTO issues (media_type, library_id, title, category, reported_by, created_at, updated_at, action_file) VALUES ('movie', 1, 'Heat', 'video', 1, 1, 1, ?)`, file)

	var n int64
	err := store.WithTx(ctx, db, func(tx *store.Transaction) error {
		c, err := RewritePrefix(ctx, tx, oldDir, newDir)
		n = c.Total()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	newFile := filepath.Join(newDir, "Heat.mkv")
	checks := []struct{ q, want string }{
		{`SELECT movie_file_path FROM movies WHERE id = 1`, newFile},
		{`SELECT json_extract(media_json, '$.path') FROM movies WHERE id = 1`, newFile},
		{`SELECT json_extract(media_json, '$.codec') FROM movies WHERE id = 1`, "hevc"},
		{`SELECT movie_file_path FROM movies WHERE id = 2`, twin},
		{`SELECT movie_file_path FROM movies WHERE id = 3`, likeTrap},
		{`SELECT file_path FROM movie_versions WHERE movie_id = 1`, filepath.Join(newDir, "Heat 4K.mkv")},
		{`SELECT json_extract(media_json, '$.path') FROM movie_versions WHERE movie_id = 1`, filepath.Join(newDir, "Heat 4K.mkv")},
		{`SELECT target_path FROM imports`, newFile},
		{`SELECT source_path FROM imports`, "/downloads/Heat"},
		{`SELECT replaces_path FROM grabs`, newFile},
		{`SELECT path FROM convert_library`, newFile},
		{`SELECT info_json FROM convert_probe_cache`, "{}"},
		{`SELECT path FROM convert_choices`, newFile},
		{`SELECT path FROM convert_measured`, newFile},
		{`SELECT src_path || '|' || out_path FROM convert_history`, newFile + "|" + newFile},
		{`SELECT video_path || '|' || path FROM subtitle_files`, newFile + "|" + filepath.Join(newDir, "Heat.en.srt")},
		{`SELECT video_path FROM subtitle_attempts`, newFile},
		{`SELECT video_path FROM subtitle_backoff`, newFile},
		{`SELECT path FROM subtitle_probe_cache`, newFile},
		{`SELECT action_file FROM issues`, newFile},
	}
	for _, c := range checks {
		var got string
		if err := db.QueryRow(c.q).Scan(&got); err != nil {
			t.Errorf("%s: %v", c.q, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.q, got, c.want)
		}
	}
	var cache int
	_ = db.QueryRow(`SELECT COUNT(*) FROM convert_probe_cache`).Scan(&cache)
	if cache != 1 {
		t.Errorf("probe cache rows = %d, want the moved row only", cache)
	}
	if n < 18 {
		t.Errorf("rewrite reported %d paths", n)
	}
	// "/" or "" can never be the folder rewritten.
	_ = store.WithTx(ctx, db, func(tx *store.Transaction) error {
		if _, err := RewritePrefix(ctx, tx, string(filepath.Separator), newDir); err == nil {
			t.Error("rewriting everything under / was allowed")
		}
		return nil
	})
}

func jsonEsc(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\\' || r == '"' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}
