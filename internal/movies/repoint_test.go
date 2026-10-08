package movies

import (
	"path/filepath"
	"testing"
)

// After a convert the recorded release reads as the new codec with its group intact
// ("...BluRay.AV1-GRP"), on the movie row and on an extra version alike — never the old
// appended form ("...x264-GRP AV1") that parsed as the old codec. DB rows and temp-dir
// paths only; no file is read or written.
func TestRepointMovieFileRestampsCodec(t *testing.T) {
	svc, ctx := testService(t)
	dir := t.TempDir()
	oldDef, newDef := filepath.Join(dir, "Film.x264.mkv"), filepath.Join(dir, "Film.av1.mkv")
	oldVer, newVer := filepath.Join(dir, "Film.4k.x265.mkv"), filepath.Join(dir, "Film.4k.av1.mkv")
	db := svc.repo.db
	if _, err := db.ExecContext(ctx, `INSERT INTO movies (id, tmdb_id, title, has_file, movie_file_path, source_release)
		VALUES (1, 101, 'Film', 1, ?, 'Film.2021.1080p.BluRay.x264-GRP')`, oldDef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO movie_versions (id, movie_id, label, has_file, file_path, source_release)
		VALUES (1, 1, '4K', 1, ?, 'Film.2021.2160p.UHD.BluRay.x265-GRP')`, oldVer); err != nil {
		t.Fatal(err)
	}

	if n, err := svc.RepointMovieFile(ctx, 1, oldDef, newDef, 1<<30, "AV1"); err != nil || n != 1 {
		t.Fatalf("repoint default: n=%d err=%v", n, err)
	}
	if n, err := svc.RepointMovieFile(ctx, 1, oldVer, newVer, 1<<30, "AV1"); err != nil || n != 1 {
		t.Fatalf("repoint version: n=%d err=%v", n, err)
	}
	read := func(q string) string {
		var rel string
		if err := db.QueryRowContext(ctx, q).Scan(&rel); err != nil {
			t.Fatal(err)
		}
		return rel
	}
	if got := read(`SELECT source_release FROM movies WHERE id = 1`); got != "Film.2021.1080p.BluRay.AV1-GRP" {
		t.Errorf("movie release = %q, want Film.2021.1080p.BluRay.AV1-GRP", got)
	}
	if got := read(`SELECT source_release FROM movie_versions WHERE id = 1`); got != "Film.2021.2160p.UHD.BluRay.AV1-GRP" {
		t.Errorf("version release = %q, want Film.2021.2160p.UHD.BluRay.AV1-GRP", got)
	}
}
