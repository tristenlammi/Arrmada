package series

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// TestSupersedeRecyclesOld guards the fix for upgrades/renames leaving the old episode
// file orphaned on disk: importing a new file at a different path must recycle the old.
func TestSupersedeRecyclesOld(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	ctx := context.Background()
	db.ExecContext(ctx, `INSERT INTO series (id,tmdb_id,title) VALUES (1,1,'S')`)
	db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number) VALUES (1,1,1)`)

	dir := t.TempDir()
	recycle := filepath.Join(dir, ".recycle")
	oldF := filepath.Join(dir, "old.mkv")
	newF := filepath.Join(dir, "new.mkv")
	os.WriteFile(oldF, []byte("old"), 0o644)
	os.WriteFile(newF, []byte("new"), 0o644)

	svc := &Service{repo: NewRepo(db), recycle: recycle, log: slog.Default()}
	if err := svc.MarkEpisodeImported(ctx, 1, 1, 1, oldF, 3); err != nil {
		t.Fatal(err)
	}
	if err := svc.SupersedeEpisodeFile(ctx, 1, 1, 1, newF, 3, "Show.S01E01.1080p.BluRay.x264-GRP"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldF); !os.IsNotExist(err) {
		t.Error("old file should have been recycled/removed")
	}
	if p, _ := svc.repo.EpisodeFilePath(ctx, 1, 1, 1); p != newF {
		t.Errorf("db path = %q, want the new file", p)
	}
	// Same path (in-place convert) must not delete the file.
	os.WriteFile(newF, []byte("new"), 0o644)
	if err := svc.SupersedeEpisodeFile(ctx, 1, 1, 1, newF, 3, "Show.S01E01.1080p.BluRay.x264-GRP"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newF); err != nil {
		t.Error("same-path supersede must not remove the file")
	}
}
