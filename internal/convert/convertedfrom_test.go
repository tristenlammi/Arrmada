package convert

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

func baselineDB(t *testing.T) (*store.Store, *slog.Logger) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, slog.New(slog.NewTextHandler(io.Discard, nil))
}

func readBaseline(t *testing.T, st *store.Store, table string, id int64) (string, int64) {
	t.Helper()
	var rel string
	var size int64
	if err := st.DB().QueryRow(`SELECT converted_from_release, converted_from_size FROM `+table+` WHERE id = ?`, id).Scan(&rel, &size); err != nil {
		t.Fatal(err)
	}
	return rel, size
}

// markConverted records what the file was — the release before its codec is restamped,
// and the original's size — on the movie and on the episode alike. DB rows and temp-dir
// paths only; nothing is encoded.
func TestMarkConvertedRecordsBaseline(t *testing.T) {
	st, log := baselineDB(t)
	db := st.DB()
	ctx := context.Background()
	dir := t.TempDir()
	movieSrc, epSrc := filepath.Join(dir, "Film.m2ts"), filepath.Join(dir, "Show.S01E01.mkv")
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO movies (id, tmdb_id, title, has_file, movie_file_path, source_release) VALUES (1, 1, 'Film', 1, ?, 'Film.2021.1080p.BluRay.REMUX.AVC-FGT')`, []any{movieSrc}},
		{`INSERT INTO series (id, tmdb_id, title) VALUES (1, 2, 'Show')`, nil},
		{`INSERT INTO episodes (id, series_id, season_number, episode_number, has_file, file_path, size_bytes, source_release) VALUES (1, 1, 1, 1, 1, ?, 5, 'Show.S01E01.1080p.WEB.x264-GRP')`, []any{epSrc}},
	} {
		if _, err := db.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{log: log,
		movies: movies.NewService(db, nil, nil, dir, "", nil, log),
		series: series.NewService(db, nil, dir, log)}

	if err := s.markConverted(ctx, &Job{Kind: "movie", MovieID: 1}, movieSrc, filepath.Join(dir, "Film.mkv"), "av1", 80<<30); err != nil {
		t.Fatal(err)
	}
	if rel, size := readBaseline(t, st, "movies", 1); rel != "Film.2021.1080p.BluRay.REMUX.AVC-FGT" || size != 80<<30 {
		t.Errorf("movie baseline = %q / %d", rel, size)
	}
	if err := s.markConverted(ctx, &Job{Kind: "episode", SeriesID: 1, Season: 1, Episode: 1}, epSrc, filepath.Join(dir, "Show.S01E01.av1.mkv"), "hevc", 3<<30); err != nil {
		t.Fatal(err)
	}
	if rel, size := readBaseline(t, st, "episodes", 1); rel != "Show.S01E01.1080p.WEB.x264-GRP" || size != 3<<30 {
		t.Errorf("episode baseline = %q / %d", rel, size)
	}
	var stamped string
	_ = db.QueryRow(`SELECT source_release FROM episodes WHERE id = 1`).Scan(&stamped)
	if stamped != "Show.S01E01.1080p.WEB.x265-GRP" {
		t.Errorf("precondition: the episode's release is restamped, got %q", stamped)
	}
}

// Files converted before baselines existed get theirs from the ledger, once — but only
// while the record still holds that conversion's output.
func TestBackfillConvertedFromLedger(t *testing.T) {
	st, log := baselineDB(t)
	db := st.DB()
	ctx := context.Background()
	for _, q := range []string{
		// Still the converted file: restamped release, same path as the ledger's output.
		`INSERT INTO movies (id, tmdb_id, title, has_file, movie_file_path, source_release) VALUES (1, 1, 'A', 1, '/m/A.mkv', 'A.2020.1080p.BluRay.AV1-GRP')`,
		// Converted, then a different release imported over it at the same path.
		`INSERT INTO movies (id, tmdb_id, title, has_file, movie_file_path, source_release) VALUES (2, 2, 'B', 1, '/m/B.mkv', 'B.2020.2160p.WEB-DL.x265-NEW')`,
		// The migration recovered the release from an appended stamp; the ledger adds the size.
		`INSERT INTO movies (id, tmdb_id, title, has_file, movie_file_path, source_release, converted_from_release) VALUES (3, 3, 'C', 1, '/m/C.mkv', 'C.2020.1080p.WEB.x265-GRP', 'C.2020.1080p.WEB.x264-GRP')`,
		`INSERT INTO series (id, tmdb_id, title) VALUES (1, 9, 'Show')`,
		`INSERT INTO episodes (id, series_id, season_number, episode_number, has_file, file_path, source_release) VALUES (1, 1, 1, 1, 1, '/tv/S01E01.mkv', 'Show.S01E01.1080p.WEB.x265-GRP')`,
		`INSERT INTO convert_history (item_key, kind, movie_id, outcome, src_release, src_size, out_path, finished_at) VALUES ('m1', 'movie', 1, 'done', 'A.2020.1080p.BluRay.x264-GRP', 8000, '/m/A.mkv', 1)`,
		// A later re-conversion of A: the first original wins.
		`INSERT INTO convert_history (item_key, kind, movie_id, outcome, src_release, src_size, out_path, finished_at) VALUES ('m1', 'movie', 1, 'done', 'A.2020.1080p.BluRay.AV1-GRP', 3000, '/m/A.mkv', 2)`,
		`INSERT INTO convert_history (item_key, kind, movie_id, outcome, src_release, src_size, out_path, finished_at) VALUES ('m2', 'movie', 2, 'done', 'B.2020.1080p.BluRay.x264-GRP', 9000, '/m/B.mkv', 1)`,
		`INSERT INTO convert_history (item_key, kind, movie_id, outcome, src_release, src_size, out_path, finished_at) VALUES ('m3', 'movie', 3, 'done', 'C.2020.1080p.WEB.x264-GRP', 7000, '/m/C.mkv', 1)`,
		`INSERT INTO convert_history (item_key, kind, series_id, season, episode, outcome, src_release, src_size, out_path, finished_at) VALUES ('e1', 'episode', 1, 1, 1, 'done', 'Show.S01E01.1080p.WEB.x264-GRP', 600, '/tv/S01E01.mkv', 1)`,
		// Not a finished conversion.
		`INSERT INTO convert_history (item_key, kind, movie_id, outcome, src_release, src_size, out_path, finished_at) VALUES ('m2', 'movie', 2, 'failed', 'B.2020.2160p.WEB-DL.x265-NEW', 1, '/m/B.mkv', 3)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	set := settings.NewService(db)
	n, err := BackfillConvertedFrom(ctx, db, set, log)
	if err != nil || n != 3 {
		t.Fatalf("backfilled %d (%v), want 3", n, err)
	}
	for _, c := range []struct {
		table string
		id    int64
		rel   string
		size  int64
	}{
		{"movies", 1, "A.2020.1080p.BluRay.x264-GRP", 8000},
		{"movies", 2, "", 0},
		{"movies", 3, "C.2020.1080p.WEB.x264-GRP", 7000},
		{"episodes", 1, "Show.S01E01.1080p.WEB.x264-GRP", 600},
	} {
		if rel, size := readBaseline(t, st, c.table, c.id); rel != c.rel || size != c.size {
			t.Errorf("%s %d: %q / %d, want %q / %d", c.table, c.id, rel, size, c.rel, c.size)
		}
	}
	// Once only.
	if _, err := db.Exec(`UPDATE movies SET converted_from_release = '', converted_from_size = 0 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if n, err := BackfillConvertedFrom(ctx, db, set, log); err != nil || n != 0 {
		t.Errorf("second run filled %d (%v)", n, err)
	}
}
