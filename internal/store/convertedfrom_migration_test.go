package store

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// 0138 recovers the release a converted file came from out of the old appended codec
// stamps — " x265" and " AV1" exactly, nothing else — and leaves the size unknown.
func TestConvertedFromBackfillStripsAppendedStamps(t *testing.T) {
	ctx := context.Background()
	db, err := openDB(filepath.Join(t.TempDir(), "arrmada.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Everything before 0138, then rows as an install upgrading from before the stamp fix
	// would have them.
	all := embeddedMigrations()
	names, err := listMigrations(all)
	if err != nil {
		t.Fatal(err)
	}
	before := fstest.MapFS{}
	for _, n := range names {
		if n >= "0138" {
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
		`INSERT INTO movies (id, tmdb_id, title, source_release) VALUES (1, 1, 'A', 'A.2020.1080p.BluRay.x264-GRP x265')`,
		`INSERT INTO movies (id, tmdb_id, title, source_release) VALUES (2, 2, 'B', 'B.2020.1080p.BluRay.H.264-GRP AV1')`,
		`INSERT INTO movies (id, tmdb_id, title, source_release) VALUES (3, 3, 'C', 'C.2020.1080p.BluRay.x265-GRP')`,
		`INSERT INTO movies (id, tmdb_id, title, source_release) VALUES (4, 4, 'D', 'D 2020 1080p WEB av1')`,
		`INSERT INTO movie_versions (id, movie_id, label, source_release) VALUES (1, 1, '4K', 'A.2020.2160p.WEB.x264-GRP AV1')`,
		`INSERT INTO series (id, tmdb_id, title) VALUES (1, 9, 'Show')`,
		`INSERT INTO episodes (id, series_id, season_number, episode_number, source_release) VALUES (1, 1, 1, 1, 'Show.S01E01.720p.HDTV.x264-GRP x265')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := runMigrations(ctx, db, all); err != nil {
		t.Fatal(err)
	}

	check := func(table string, id int64, wantRel string) {
		t.Helper()
		var rel string
		var size int64
		if err := db.QueryRowContext(ctx, `SELECT converted_from_release, converted_from_size FROM `+table+` WHERE id = ?`, id).Scan(&rel, &size); err != nil {
			t.Fatal(err)
		}
		if rel != wantRel || size != 0 {
			t.Errorf("%s %d: converted_from = %q / %d, want %q / 0", table, id, rel, size, wantRel)
		}
	}
	check("movies", 1, "A.2020.1080p.BluRay.x264-GRP")
	check("movies", 2, "B.2020.1080p.BluRay.H.264-GRP")
	check("movies", 3, "") // a name that just ends in its codec was never stamped
	check("movies", 4, "") // case matters: Convert wrote "AV1"
	check("movie_versions", 1, "A.2020.2160p.WEB.x264-GRP")
	check("episodes", 1, "Show.S01E01.720p.HDTV.x264-GRP")
}
