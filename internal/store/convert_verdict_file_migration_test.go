package store

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
)

// 0355 ties Convert's skips and failures to the file they were about. Existing rows are
// given the file the index has for them now, so they keep holding exactly as before; a
// "reverted" skip, a row whose title has no indexed file, and old measurements are handled
// as the migration says.
func TestConvertVerdictFileMigration(t *testing.T) {
	ctx := context.Background()
	db := testDB(t, 1)
	all := embeddedMigrations()
	names, err := listMigrations(all)
	if err != nil {
		t.Fatal(err)
	}
	before := fstest.MapFS{}
	for _, n := range names {
		if n >= "0355" {
			break
		}
		body, err := fs.ReadFile(all, n)
		if err != nil {
			t.Fatal(err)
		}
		before[n] = &fstest.MapFile{Data: body}
	}
	if err := runMigrations(ctx, db, before); err != nil {
		t.Fatalf("migrate to 0354: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO convert_library (path, media_type, movie_id, size_bytes) VALUES ('/m/Heat.mkv', 'movie', 12, 6000)`,
		`INSERT INTO convert_library (path, media_type, movie_id, version_id, size_bytes) VALUES ('/m/Heat 4K.mkv', 'movie', 12, 3, 9000)`,
		`INSERT INTO convert_library (path, media_type, series_id, season, episode, size_bytes) VALUES ('/tv/S02E05.mkv', 'episode', 76, 2, 5, 700)`,
		`INSERT INTO convert_skips (item_key, kind, reason, permanent) VALUES ('movie:12', 'not_smaller', 'x', 1)`,
		`INSERT INTO convert_skips (item_key, kind, reason, permanent) VALUES ('movie:12:v:3', 'quality_gate', 'x', 1)`,
		`INSERT INTO convert_skips (item_key, kind, reason, permanent) VALUES ('episode:76:2:5', 'reverted', 'x', 1)`,
		`INSERT INTO convert_skips (item_key, kind, reason, permanent) VALUES ('movie:99', 'not_smaller', 'x', 1)`,
		`INSERT INTO convert_failures (item_key, count, last_error) VALUES ('episode:76:2:5', 3, 'boom')`,
		`INSERT INTO convert_measured (path, codec, size_bytes, crf, video_bytes, measured_at) VALUES ('/m/Heat.mkv', 'hevc', 6000, 18, 3000, 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := runMigrations(ctx, db, all); err != nil {
		t.Fatalf("migrate the rest: %v", err)
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM convert_skips WHERE item_key = 'movie:12' AND file_path = '/m/Heat.mkv' AND file_size = 6000 AND encoder = ''`:         1,
		`SELECT COUNT(*) FROM convert_skips WHERE item_key = 'movie:12:v:3' AND file_path = '/m/Heat 4K.mkv' AND file_size = 9000`:                   1,
		`SELECT COUNT(*) FROM convert_skips WHERE item_key = 'episode:76:2:5' AND file_path = '' AND file_size = 0`:                                  1, // reverted stays title-wide
		`SELECT COUNT(*) FROM convert_skips WHERE item_key = 'movie:99' AND file_path = ''`:                                                          1, // no indexed file: any file
		`SELECT COUNT(*) FROM convert_failures WHERE item_key = 'episode:76:2:5' AND count = 3 AND file_path = '/tv/S02E05.mkv' AND file_size = 700`: 1,
		`SELECT COUNT(*) FROM convert_measured WHERE encoder = ''`:                                                                                   1,
	} {
		if n := count(t, db, q); n != want {
			t.Errorf("%s: got %d, want %d", q, n, want)
		}
	}
}
