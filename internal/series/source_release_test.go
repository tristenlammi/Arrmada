package series

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// TestSourceReleaseRecorded is the guard for the series upgrade loop: the upgrade
// baseline must be the RELEASE name, not the renamed library filename (which strips
// group/HDR/codec tags and made every candidate look like an upgrade, forever).
func TestSourceReleaseRecorded(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	ctx := context.Background()

	db.ExecContext(ctx, `INSERT INTO series (id,tmdb_id,title,monitored) VALUES (1,7,'Show',1)`)
	db.ExecContext(ctx, `INSERT INTO seasons (series_id,season_number) VALUES (1,1)`)
	db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number,air_date,monitored) VALUES (1,1,1,'2020-01-01',1)`)

	dir := t.TempDir()
	f := filepath.Join(dir, "Show - S01E01 - Pilot - 1080p WEB-DL.mkv") // renamed library name
	os.WriteFile(f, []byte("x"), 0o644)

	svc := &Service{repo: NewRepo(db), log: slog.Default()}
	release := "Show.S01E01.1080p.WEB-DL.DDP5.1.H.264-NTb"
	if err := svc.SupersedeEpisodeFile(ctx, 1, 1, 1, f, 1, release); err != nil {
		t.Fatal(err)
	}

	seasons, err := svc.repo.SeasonsFor(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, sn := range seasons {
		for _, e := range sn.Episodes {
			if e.EpisodeNumber == 1 {
				got = e.SourceRelease
			}
		}
	}
	if got != release {
		t.Errorf("SourceRelease = %q, want the release name %q (not the library filename)", got, release)
	}

	// An empty source release must stay empty (a rename/transcode must not invent one),
	// so the upgrade path can safely skip that episode instead of guessing.
	if err := svc.SupersedeEpisodeFile(ctx, 1, 1, 1, f, 1, ""); err != nil {
		t.Fatal(err)
	}
	seasons, _ = svc.repo.SeasonsFor(ctx, 1)
	for _, sn := range seasons {
		for _, e := range sn.Episodes {
			if e.EpisodeNumber == 1 && e.SourceRelease != release {
				t.Errorf("source release was clobbered by a path-only update: %q", e.SourceRelease)
			}
		}
	}
}
