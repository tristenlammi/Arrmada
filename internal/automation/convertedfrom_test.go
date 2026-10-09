package automation

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

// convertedHarness is a coordinator over a temp database with one upgrading profile.
func convertedHarness(t *testing.T) (*Coordinator, *store.Store, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := quality.NewService(st.DB())
	sp, err := q.Create(context.Background(), quality.StoredProfile{
		MediaType: quality.MediaMovie, Name: "Upgrades", UpgradesEnabled: true, UpgradeMinPercent: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{db: st.DB(), log: log, quality: q,
		movies: movies.NewService(st.DB(), nil, nil, t.TempDir(), "", nil, log)}
	return c, st, "custom:" + strconv.FormatInt(sp.ID, 10)
}

// After Convert shrinks a remux — to AV1 or to HEVC — the movie upgrade sweep's decision
// grabs neither the remux it came from nor another group's remux of the same size; a real
// step up is still taken. The conversion is replayed the way Convert writes it
// (SetConvertedFrom, then RepointMovieFile with the codec stamp); DB rows only.
func TestMovieSweepNeverUndoesAConversion(t *testing.T) {
	for _, token := range []string{"AV1", "x265"} {
		t.Run(token, func(t *testing.T) {
			c, st, ref := convertedHarness(t)
			ctx := context.Background()
			orig := "Film.2021.1080p.BluRay.REMUX.AVC.DTS-HD.MA.5.1-FGT"
			src, final := "/lib/Film (2021)/Film.m2ts", "/lib/Film (2021)/Film.mkv"
			if _, err := st.DB().Exec(`INSERT INTO movies (id, tmdb_id, title, year, runtime, monitored, quality_profile, has_file, movie_file_path, source_release)
				VALUES (1, 1, 'Film', 2021, 130, 1, ?, 1, ?, ?)`, ref, src, orig); err != nil {
				t.Fatal(err)
			}
			if err := c.movies.SetConvertedFrom(ctx, 1, src, 80<<30); err != nil {
				t.Fatal(err)
			}
			if _, err := c.movies.RepointMovieFile(ctx, 1, src, final, 30<<30, token); err != nil {
				t.Fatal(err)
			}
			// The cached media info as a refresh would leave it: the converted file's size.
			if _, err := st.DB().Exec(`UPDATE movies SET media_json = ? WHERE id = 1`,
				`{"path":"`+final+`","size_bytes":32212254720,"v":2}`); err != nil {
				t.Fatal(err)
			}
			m, err := c.movies.Get(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			vs, err := c.movies.VersionRows(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			cur := c.currentMovieFile(ctx, m, vs[0])
			if cur.OrigRelease != orig || cur.OrigSizeGB != 80 || cur.SizeGB != 30 {
				t.Fatalf("current file = %+v", cur)
			}
			if parser.Parse(cur.Release).Codec == parser.Parse(orig).Codec {
				t.Fatalf("precondition: the release reads as the new codec: %q", cur.Release)
			}

			remuxes := tagRuntime([]quality.Candidate{
				quality.NewCandidate(orig, 80, 40),
				quality.NewCandidate("Film.2021.1080p.BluRay.REMUX.AVC.DTS-HD.MA.5.1-OTHER", 80, 90),
			}, m.Runtime)
			if pick, ok := c.quality.UpgradeCandidate(ctx, ref, cur, remuxes); ok {
				t.Errorf("the sweep would grab %q over the file converted from a remux", pick.Name)
			}
			uhd := tagRuntime([]quality.Candidate{quality.NewCandidate("Film.2021.2160p.WEB-DL.x265-GRP", 20, 50)}, m.Runtime)
			if _, ok := c.quality.UpgradeCandidate(ctx, ref, cur, append(remuxes, uhd...)); !ok {
				t.Error("a higher resolution than the original is still an upgrade")
			}
		})
	}
}

// The episode sweep and the series import gate judge a converted episode against what it
// was before conversion. Temp-dir file and DB rows only.
func TestEpisodeImportGateHonoursConvertedFrom(t *testing.T) {
	c, st, ref := convertedHarness(t)
	ctx := context.Background()
	svc := series.NewService(st.DB(), nil, t.TempDir(), c.log)
	c.series = svc
	path := filepath.Join(t.TempDir(), "Show - S01E01.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := "Show.S01E01.1080p.BluRay.x264-GRP"
	for _, q := range []string{
		`INSERT INTO series (id, tmdb_id, title, monitored, quality_profile) VALUES (1, 7, 'Show', 1, '` + ref + `')`,
		`INSERT INTO seasons (series_id, season_number) VALUES (1, 1)`,
		`INSERT INTO episodes (series_id, season_number, episode_number, runtime, monitored, has_file, file_path, size_bytes, source_release)
		 VALUES (1, 1, 1, 45, 1, 1, '` + path + `', 1073741824, 'Show.S01E01.1080p.BluRay.AV1-GRP')`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	other := "Show.S01E01.1080p.BluRay.x264-OTHER"
	cand := parser.Parse(other)
	// Without a baseline a 4 GB x264 encode beats the 1 GB AV1 file on bitrate.
	if !c.wantsEpisodeFile(ctx, s, 1, 1, cand, other, 4<<30, 1) {
		t.Fatal("precondition: without a baseline the bigger encode is taken")
	}
	if _, err := st.DB().Exec(`UPDATE episodes SET converted_from_release = ?, converted_from_size = ? WHERE series_id = 1`, orig, int64(4<<30)); err != nil {
		t.Fatal(err)
	}
	if c.wantsEpisodeFile(ctx, s, 1, 1, cand, other, 4<<30, 1) {
		t.Error("the import gate replaced a converted episode with an encode no better than its original")
	}
	if c.wantsEpisodeFile(ctx, s, 1, 1, parser.Parse(orig), orig, 4<<30, 1) {
		t.Error("the import gate took back the release the episode was converted from")
	}

	// The sweep sees the same baseline.
	s, _ = svc.Get(ctx, 1)
	cur := c.currentEpisodeFile(ctx, s.Seasons[0].Episodes[0])
	if cur.OrigRelease != orig || cur.OrigSizeGB != 4 {
		t.Fatalf("episode current file = %+v", cur)
	}
	cands := tagRuntime([]quality.Candidate{quality.NewCandidate(other, 4, 50), quality.NewCandidate(orig, 4, 50)}, 45)
	if pick, ok := c.quality.UpgradeCandidate(ctx, ref, cur, cands); ok {
		t.Errorf("the episode sweep would grab %q over a converted episode", pick.Name)
	}
}
