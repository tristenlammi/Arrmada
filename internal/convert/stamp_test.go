package convert

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

// A converted episode's recorded release reads as the new codec with its group intact
// ("...WEB.AV1-GRP"), never the old appended form ("...x264-GRP AV1"). A DB row and a
// temp-dir path only; no file is read or written.
func TestStampEpisodeCodecRestampsInPlace(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB()
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if _, err := db.ExecContext(ctx, `INSERT INTO series (id, tmdb_id, title) VALUES (1, 201, 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO episodes (id, series_id, season_number, episode_number, has_file, file_path, source_release)
		VALUES (1, 1, 1, 1, 1, ?, 'Show.S01E01.1080p.WEB.x264-GRP')`, filepath.Join(t.TempDir(), "Show.S01E01.mkv")); err != nil {
		t.Fatal(err)
	}
	s := &Service{series: series.NewService(db, nil, "", log), log: log}
	job := &Job{Kind: "episode", SeriesID: 1, Season: 1, Episode: 1}

	s.stampEpisodeCodec(ctx, job, codecToken("av1"))
	var got string
	if err := db.QueryRowContext(ctx, `SELECT source_release FROM episodes WHERE id = 1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "Show.S01E01.1080p.WEB.AV1-GRP" {
		t.Errorf("episode release = %q, want Show.S01E01.1080p.WEB.AV1-GRP", got)
	}
	// A rerun on the already-stamped row leaves it alone.
	s.stampEpisodeCodec(ctx, job, codecToken("av1"))
	if err := db.QueryRowContext(ctx, `SELECT source_release FROM episodes WHERE id = 1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "Show.S01E01.1080p.WEB.AV1-GRP" {
		t.Errorf("second stamp changed it to %q", got)
	}
}
