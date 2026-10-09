package movies

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/tristenlammi/arrmada/internal/mediainfo"
)

// fileCounter counts every probe and stat the service makes. The stat is answered
// from the real filesystem (temp dirs only); the probe pretends to be ffprobe.
type fileCounter struct{ probes, stats atomic.Int32 }

func (c *fileCounter) install(s *Service) {
	s.SetFileAccess(
		func(string) (mediainfo.Info, error) {
			c.probes.Add(1)
			return mediainfo.Info{Resolution: "2160p", VideoCodec: "HEVC"}, nil
		},
		func(p string) (os.FileInfo, error) {
			c.stats.Add(1)
			return os.Stat(p)
		},
	)
}

func seedMovieWithTracks(t *testing.T, svc *Service, ctx context.Context) (def, v1, v2 string) {
	t.Helper()
	dir := t.TempDir()
	def = filepath.Join(dir, "Film.2021.1080p.BluRay.x264-GRP.mkv")
	v1 = filepath.Join(dir, "Film.2021.2160p.UHD.BluRay.x265-GRP.mkv")
	v2 = filepath.Join(dir, "Film.2021.720p.WEB-DL.mkv")
	for _, p := range []string{def, v1, v2} {
		if err := os.WriteFile(p, make([]byte, 100), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	db := svc.repo.db
	if _, err := db.ExecContext(ctx, `INSERT INTO movies (id, tmdb_id, title, monitored, has_file, movie_file_path, source_release, media_json)
		VALUES (1, 101, 'Film', 1, 1, ?, 'Film.2021.1080p.BluRay.x264-GRP', ?)`, def,
		`{"path":"`+filepath.ToSlash(def)+`","filename":"Film.mkv","size_bytes":4200,"quality":"1080p BluRay","codec":"x264","resolution":"1080p","v":2}`); err != nil {
		t.Fatal(err)
	}
	for i, p := range []string{v1, v2} {
		if _, err := db.ExecContext(ctx, `INSERT INTO movie_versions (movie_id, label, monitored, has_file, file_path, size_bytes, source_release)
			VALUES (1, ?, 1, 1, ?, ?, ?)`, []string{"4K", "720p"}[i], p, 9000+i, filepath.Base(p)); err != nil {
			t.Fatal(err)
		}
	}
	return def, v1, v2
}

// The periodic sweeps' view of a movie's tracks comes from the database alone: no
// ffprobe and no stat, however often it's asked.
func TestVersionRowsNeverProbesOrStats(t *testing.T) {
	svc, ctx := testService(t)
	var fc fileCounter
	fc.install(svc)
	_, v1, _ := seedMovieWithTracks(t, svc, ctx)

	for i := 0; i < 10; i++ {
		vs, err := svc.VersionRows(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(vs) != 3 {
			t.Fatalf("tracks = %d, want 3", len(vs))
		}
		if vs[0].File == nil || vs[0].File.SizeBytes != 4200 || vs[0].File.Quality != "1080p BluRay" || vs[0].SizeBytes != 4200 {
			t.Fatalf("default track should carry the cached media info: %+v / %+v", vs[0], vs[0].File)
		}
		if vs[1].File != nil || vs[1].SizeBytes != 9000 || vs[1].FilePath != v1 || !vs[1].HasFile {
			t.Fatalf("extra track should be the bare row: %+v", vs[1])
		}
	}
	if p, s := fc.probes.Load(), fc.stats.Load(); p != 0 || s != 0 {
		t.Fatalf("VersionRows probed %d and stat'd %d times, want 0 and 0", p, s)
	}
}

// The detail page's live read probes each file once — and only there.
func TestVersionsLiveProbesEachTrackOnce(t *testing.T) {
	svc, ctx := testService(t)
	var fc fileCounter
	fc.install(svc)
	seedMovieWithTracks(t, svc, ctx)

	vs, err := svc.VersionsLive(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if p := fc.probes.Load(); p != 3 {
		t.Fatalf("probes = %d, want one per track (3)", p)
	}
	if vs[0].File == nil || vs[0].File.Resolution != "2160p" || !vs[0].File.Probed {
		t.Fatalf("live default track = %+v", vs[0].File)
	}
}

func TestSearchAndUpgradeTargets(t *testing.T) {
	svc, ctx := testService(t)
	db := svc.repo.db
	// 1 monitored, missing            → search
	// 2 unmonitored, missing          → neither
	// 3 complete + missing monitored extra → search (and upgrade: it has its main file)
	// 4 fully complete                → upgrade only
	// 5 unmonitored + monitored missing extra → search
	// 6 complete, unmonitored extra missing → upgrade only
	for _, row := range []struct {
		id, mon, hasFile int
	}{{1, 1, 0}, {2, 0, 0}, {3, 1, 1}, {4, 1, 1}, {5, 0, 0}, {6, 1, 1}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO movies (id, tmdb_id, title, monitored, has_file, movie_file_path)
			VALUES (?, ?, ?, ?, ?, CASE WHEN ? = 1 THEN '/lib/f.mkv' ELSE '' END)`,
			row.id, 100+row.id, "M", row.mon, row.hasFile, row.hasFile); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []struct{ movie, mon, hasFile int }{{3, 1, 0}, {4, 1, 1}, {5, 1, 0}, {6, 0, 0}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO movie_versions (movie_id, label, monitored, has_file) VALUES (?, 'x', ?, ?)`,
			v.movie, v.mon, v.hasFile); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(ms []Movie, err error) []int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, m := range ms {
			out = append(out, m.ID)
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	if got := ids(svc.SearchTargets(ctx)); !equalIDs(got, []int64{1, 3, 5}) {
		t.Errorf("SearchTargets = %v, want [1 3 5]", got)
	}
	if got := ids(svc.UpgradeTargets(ctx)); !equalIDs(got, []int64{3, 4, 6}) {
		t.Errorf("UpgradeTargets = %v, want [3 4 6]", got)
	}
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
