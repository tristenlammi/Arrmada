package convert

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Releases stamped the old way (" AV1" / " x265" appended) are rewritten with the codec
// swapped in place, across movies, extra versions and episodes. Unstamped rows and a name
// whose last word is simply its codec are left alone, and the guard key stops a rerun.
// DB fixtures only — no library file is touched.
func TestRepairCodecStamps(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB()
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO movies (id, tmdb_id, title, source_release) VALUES (1, 101, 'Film', 'Film.2021.1080p.BluRay.H.264-GRP AV1')`)
	mustExec(`INSERT INTO movies (id, tmdb_id, title, source_release) VALUES (2, 102, 'Other', 'Other.2020.1080p.WEB-DL.x264-GRP')`)
	mustExec(`INSERT INTO movies (id, tmdb_id, title, source_release) VALUES (3, 103, 'Bambi', 'Bambi 1942 1080p BluRay x265')`)
	mustExec(`INSERT INTO movie_versions (id, movie_id, label, source_release) VALUES (1, 1, '4K', 'Film.2021.2160p.UHD.BluRay.x265-GRP AV1')`)
	mustExec(`INSERT INTO series (id, tmdb_id, title) VALUES (1, 201, 'Show')`)
	mustExec(`INSERT INTO episodes (id, series_id, season_number, episode_number, source_release) VALUES (1, 1, 1, 1, 'Show.S01E01.1080p.WEB.h264-GRP x265')`)
	mustExec(`INSERT INTO episodes (id, series_id, season_number, episode_number, source_release) VALUES (2, 1, 1, 2, 'Show.S01E02.1080p.WEB.h264-GRP')`)

	n, err := RepairCodecStamps(ctx, db, log)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("repaired %d rows, want 3", n)
	}
	read := func(table string, id int) string {
		var rel string
		if err := db.QueryRowContext(ctx, `SELECT source_release FROM `+table+` WHERE id = ?`, id).Scan(&rel); err != nil {
			t.Fatal(err)
		}
		return rel
	}
	for _, c := range []struct {
		table string
		id    int
		want  string
	}{
		{"movies", 1, "Film.2021.1080p.BluRay.AV1-GRP"},
		{"movies", 2, "Other.2020.1080p.WEB-DL.x264-GRP"},
		{"movies", 3, "Bambi 1942 1080p BluRay x265"},
		{"movie_versions", 1, "Film.2021.2160p.UHD.BluRay.AV1-GRP"},
		{"episodes", 1, "Show.S01E01.1080p.WEB.x265-GRP"},
		{"episodes", 2, "Show.S01E02.1080p.WEB.h264-GRP"},
	} {
		if got := read(c.table, c.id); got != c.want {
			t.Errorf("%s %d = %q, want %q", c.table, c.id, got, c.want)
		}
	}
	for _, table := range []string{"movies", "movie_versions", "episodes"} {
		var stamped int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE source_release LIKE '%-GRP AV1' OR source_release LIKE '%-GRP x265'`).Scan(&stamped)
		if stamped != 0 {
			t.Errorf("%s still has %d stamped rows", table, stamped)
		}
	}

	// A second boot does nothing, even if an old-style stamp somehow reappears.
	mustExec(`UPDATE movies SET source_release = 'Film.2021.1080p.BluRay.H.264-GRP AV1' WHERE id = 1`)
	if n, err := RepairCodecStamps(ctx, db, log); err != nil || n != 0 {
		t.Errorf("second run repaired %d (err %v), want 0 — the guard key must stop it", n, err)
	}
	if got := read("movies", 1); !strings.HasSuffix(got, " AV1") {
		t.Errorf("second run rewrote a row: %q", got)
	}
	var v string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, codecStampRepairKey).Scan(&v); err == sql.ErrNoRows {
		t.Error("guard key not written")
	}
}
