package series

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// The upgrade hold on episodes: set in bulk on episodes with a file, kept through a
// convert's repoint and a renumbering rebuild, ended by a new import, a profile change or
// Resume (one season, or the whole show).
func TestEpisodeUpgradeHoldLifecycle(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	ctx := context.Background()
	dir := t.TempDir() // paths only: nothing here is read or written
	path := func(s, e int) string { return filepath.Join(dir, "Show", fmt.Sprintf("S%02dE%02d.mkv", s, e)) }
	for _, q := range []string{
		`INSERT INTO series (id, tmdb_id, title, quality_profile, monitored) VALUES (1, 7, 'Show', 'custom:1', 1)`,
		`INSERT INTO seasons (series_id, season_number, monitored) VALUES (1, 1, 1), (1, 2, 1)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	var ids []int64
	for s := 1; s <= 2; s++ {
		for e := 1; e <= 2; e++ {
			res, err := db.ExecContext(ctx, `INSERT INTO episodes (series_id, season_number, episode_number, absolute_number, monitored, has_file, file_path, size_bytes, source_release)
				VALUES (1, ?, ?, ?, 1, 1, ?, 1000, 'Show.S01E01.1080p.WEB-DL.x264-GRP')`, s, e, (s-1)*2+e, path(s, e))
			if err != nil {
				t.Fatal(err)
			}
			id, _ := res.LastInsertId()
			ids = append(ids, id)
		}
	}
	// One episode with no file: never held.
	res, _ := db.ExecContext(ctx, `INSERT INTO episodes (series_id, season_number, episode_number, absolute_number, monitored) VALUES (1, 2, 3, 5, 1)`)
	missing, _ := res.LastInsertId()

	svc := &Service{repo: NewRepo(db), log: slog.Default()}
	heldCount := func() int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM episodes WHERE upgrade_hold = 1`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	hold := func() {
		t.Helper()
		if _, err := svc.HoldUpgrades(ctx, append(append([]int64{}, ids...), missing)); err != nil {
			t.Fatal(err)
		}
	}
	// A rebuild re-creates the episode rows, so their ids are read again after one.
	reread := func() {
		t.Helper()
		ids, missing = nil, 0
		rows, err := db.QueryContext(ctx, `SELECT id, has_file FROM episodes WHERE series_id = 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var hf int
			if err := rows.Scan(&id, &hf); err != nil {
				t.Fatal(err)
			}
			if hf == 1 {
				ids = append(ids, id)
			} else {
				missing = id
			}
		}
	}

	hold()
	if n := heldCount(); n != 4 {
		t.Fatalf("held %d episodes, want the 4 with files", n)
	}
	if f := svc.CurrentEpisodeFile(ctx, 1, 1, 1); !f.Held {
		t.Error("CurrentEpisodeFile doesn't report the hold")
	}

	// A convert repoints the file: still held.
	if _, err := svc.RepointEpisodeFile(ctx, 1, path(1, 1), filepath.Join(dir, "Show", "S01E01.av1.mkv"), 500); err != nil {
		t.Fatal(err)
	}
	if !svc.CurrentEpisodeFile(ctx, 1, 1, 1).Held {
		t.Error("a convert's repoint ended the hold")
	}
	// So does a renumbering rebuild, wherever the file lands.
	seasons, err := svc.repo.SeasonsFor(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.repo.RebuildEpisodes(ctx, 1, seasons); err != nil {
		t.Fatal(err)
	}
	if n := heldCount(); n != 4 {
		t.Errorf("after a rebuild %d held, want 4", n)
	}
	reread()

	// A new file for one episode ends that episode's hold only.
	if err := svc.SupersedeEpisodeFile(ctx, 1, 1, 2, filepath.Join(dir, "Show", "S01E02.new.mkv"), 2000, "Show.S01E02.1080p.BluRay.x264-GRP"); err != nil {
		t.Fatal(err)
	}
	if svc.CurrentEpisodeFile(ctx, 1, 1, 2).Held || heldCount() != 3 {
		t.Errorf("import: S01E02 held %v, %d held in all (want false, 3)", svc.CurrentEpisodeFile(ctx, 1, 1, 2).Held, heldCount())
	}

	// Resume one season, then the rest.
	if n, err := svc.ResumeUpgrades(ctx, 1, 2); err != nil || n != 2 {
		t.Fatalf("resume season 2: %d %v, want 2", n, err)
	}
	if n := heldCount(); n != 1 {
		t.Errorf("after resuming season 2, %d held, want season 1's one", n)
	}
	if n, err := svc.ResumeUpgrades(ctx, 1, -1); err != nil || n != 1 {
		t.Fatalf("resume all: %d %v, want 1", n, err)
	}

	// A profile change ends every hold; re-saving the same profile doesn't.
	hold()
	if err := svc.SetQualityProfile(ctx, 1, "custom:1"); err != nil {
		t.Fatal(err)
	}
	if n := heldCount(); n != 4 {
		t.Errorf("same profile: %d held, want 4", n)
	}
	if err := svc.SetQualityProfile(ctx, 1, "custom:2"); err != nil {
		t.Fatal(err)
	}
	if n := heldCount(); n != 0 {
		t.Errorf("profile change: %d still held", n)
	}
	if _, err := svc.ResumeUpgrades(ctx, 99, -1); err == nil {
		t.Error("resume on a missing show: want an error")
	}
}
