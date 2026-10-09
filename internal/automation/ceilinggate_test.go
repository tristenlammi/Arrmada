package automation

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

// ceilingGateFixture: a show whose S01E01 already holds a file (a real temp file, so the
// "gone from disk" early return doesn't fire) and whose S01E02 holds nothing, under a
// default series profile with a 1080p ceiling of 15 Mb/s. Episodes run 45 minutes.
func ceilingGateFixture(t *testing.T, currentRelease string, currentGB float64) (*Coordinator, series.Series, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	db := st.DB()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := quality.NewService(db)
	createDefault(t, q, quality.StoredProfile{
		MediaType: quality.MediaSeries, Name: "TV 1080p", UpgradesEnabled: true,
		AllowedResolutions: []string{"1080p", "720p"},
		Ideal:              &quality.IdealFile{Bitrate: map[string]quality.BitrateWindow{"1080p": {Max: 15}}},
	})
	cur := filepath.Join(t.TempDir(), "Show - S01E01.mkv")
	if err := os.WriteFile(cur, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO series (id, tmdb_id, title, monitored) VALUES (1, 1, 'Show', 1)`,
		`INSERT INTO seasons (series_id, season_number, monitored) VALUES (1, 1, 1)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO episodes (series_id, season_number, episode_number, air_date, runtime, monitored, has_file, file_path, size_bytes, source_release)
		VALUES (1, 1, 1, '2001-01-01', 45, 1, 1, ?, ?, ?)`, cur, int64(currentGB*(1<<30)), currentRelease); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO episodes (series_id, season_number, episode_number, air_date, runtime, monitored)
		VALUES (1, 1, 2, '2001-01-01', 45, 1)`); err != nil {
		t.Fatal(err)
	}
	svc := series.NewService(db, nil, t.TempDir(), log)
	s, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	return &Coordinator{db: db, log: log, series: svc, quality: q}, s, ctx
}

const gib = 1 << 30

func TestImportGateRefusesOverCeilingReplacements(t *testing.T) {
	// Equal resolution: BluRay out-scores the current WEB-DL, but ~25 Mb/s is over 15.
	c, s, ctx := ceilingGateFixture(t, "Show.S01E01.1080p.WEB-DL.x264-OLD", 1)
	big := "Show.S01E01.1080p.BluRay.x264-BIG"
	if c.wantsEpisodeFile(ctx, s, 1, 1, parser.Parse(big), big, 8*gib, 1) {
		t.Error("an over-ceiling equal-resolution release replaced the current file")
	}
	// Within the ceiling the same BluRay is a quality upgrade and is taken.
	if fine := "Show.S01E01.1080p.BluRay.x264-FINE"; !c.wantsEpisodeFile(ctx, s, 1, 1, parser.Parse(fine), fine, 3*gib, 1) {
		t.Error("an in-window quality upgrade was refused")
	}
	// Nothing there yet: a first import is never refused, ceiling or not.
	first := "Show.S01E02.1080p.BluRay.x264-BIG"
	if !c.wantsEpisodeFile(ctx, s, 1, 2, parser.Parse(first), first, 8*gib, 1) {
		t.Error("a first import was refused for its bitrate")
	}
}

func TestImportGateRefusesOverCeilingResolutionUpgrade(t *testing.T) {
	c, s, ctx := ceilingGateFixture(t, "Show.S01E01.720p.HDTV.x264-OLD", 0.5)
	big := "Show.S01E01.1080p.BluRay.x264-BIG"
	if c.wantsEpisodeFile(ctx, s, 1, 1, parser.Parse(big), big, 8*gib, 1) {
		t.Error("an over-ceiling 1080p replaced a 720p file")
	}
	if ok := "Show.S01E01.1080p.WEB-DL.x264-GRP"; !c.wantsEpisodeFile(ctx, s, 1, 1, parser.Parse(ok), ok, 2*gib, 1) {
		t.Error("an in-window resolution upgrade was refused")
	}
}

// A double-episode file's bytes are shared by both episodes: 8 GB over two 45-minute
// episodes is ~12.7 Mb/s each, inside the window, not ~25 over one.
func TestImportGateCostsAMultiEpisodeFilePerEpisode(t *testing.T) {
	c, s, ctx := ceilingGateFixture(t, "Show.S01E01.1080p.WEB-DL.x264-OLD", 1)
	dbl := "Show.S01E01E02.1080p.BluRay.x264-DBL"
	if !c.wantsEpisodeFile(ctx, s, 1, 1, parser.Parse(dbl), dbl, 8*gib, 2) {
		t.Error("a double-episode file was costed against one episode's runtime")
	}
	if c.wantsEpisodeFile(ctx, s, 1, 1, parser.Parse(dbl), dbl, 8*gib, 1) {
		t.Error("premise: the same bytes for one episode should be over the ceiling")
	}
}
