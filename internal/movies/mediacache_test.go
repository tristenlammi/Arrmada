package movies

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// countDirs counts the detail page's folder listings.
func countDirs(s *Service) *atomic.Int32 {
	var n atomic.Int32
	s.readDir = func(dir string) ([]os.DirEntry, error) {
		n.Add(1)
		return os.ReadDir(dir)
	}
	return &n
}

// currentEntry is a cache entry that describes the file at p as it is on disk now.
func currentEntry(t *testing.T, p, quality string) string {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(MovieFile{Path: p, Filename: filepath.Base(p), SizeBytes: fi.Size(), MtimeUnix: fi.ModTime().Unix(),
		Quality: quality, Codec: "HEVC", HDR: []string{"HDR10"}, Probed: true, MediaVersion: MediaVersion})
	return string(b)
}

// seedCachedTracks is a movie with a default track and two extras, every one with a
// current cache entry, all three files in one folder (temp dir, synthetic files).
func seedCachedTracks(t *testing.T, svc *Service, ctx context.Context) (def, v1, v2 string, v1ID, v2ID int64) {
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
	if _, err := db.ExecContext(ctx, `INSERT INTO movies (id, tmdb_id, title, monitored, has_file, movie_file_path, media_json)
		VALUES (1, 101, 'Film', 1, 1, ?, ?)`, def, currentEntry(t, def, "1080p BluRay")); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 2)
	for i, p := range []string{v1, v2} {
		res, err := db.ExecContext(ctx, `INSERT INTO movie_versions (movie_id, label, monitored, has_file, file_path, size_bytes, media_json)
			VALUES (1, ?, 1, 1, ?, 100, ?)`, []string{"4K", "720p"}[i], p, currentEntry(t, p, []string{"2160p BluRay", "720p WEB-DL"}[i]))
		if err != nil {
			t.Fatal(err)
		}
		ids[i], _ = res.LastInsertId()
	}
	return def, v1, v2, ids[0], ids[1]
}

// Opening a movie page whose cache is current runs no ffprobe: one stat per track and one
// listing per folder. A file whose size or mtime moved is read again exactly once, in the
// background, and the next page view is back to zero probes.
func TestVersionsLiveReprobesOnlyWhenStale(t *testing.T) {
	svc, ctx := testService(t)
	var fc fileCounter
	fc.install(svc)
	dirs := countDirs(svc)
	_, v1, v2, v1ID, v2ID := seedCachedTracks(t, svc, ctx)

	vs, err := svc.VersionsLive(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	svc.bg.Wait()
	if p, s, d := fc.probes.Load(), fc.stats.Load(), dirs.Load(); p != 0 || s != 3 || d != 1 {
		t.Fatalf("current cache: probes=%d stats=%d listings=%d, want 0, 3, 1", p, s, d)
	}
	if len(vs) != 3 || vs[1].File == nil || vs[1].File.Quality != "2160p BluRay" || vs[1].File.Codec != "HEVC" || vs[1].File.Missing {
		t.Fatalf("extra track should show its cached facts: %+v", vs[1].File)
	}

	// A changed size: answered from the filename now, read once in the background.
	if err := os.WriteFile(v1, make([]byte, 250), 0o644); err != nil {
		t.Fatal(err)
	}
	vs, _ = svc.VersionsLive(ctx, 1)
	if vs[1].File.SizeBytes != 250 || vs[1].File.Probed {
		t.Fatalf("changed file should answer with its live size and name facts: %+v", vs[1].File)
	}
	svc.bg.Wait()
	if p := fc.probes.Load(); p != 1 {
		t.Fatalf("after a size change: probes = %d, want 1", p)
	}
	if v, _, _ := svc.repo.GetVersion(ctx, v1ID); v.File == nil || v.File.SizeBytes != 250 || !v.File.Probed {
		t.Fatalf("the re-read should be cached: %+v", v.File)
	}

	// A changed mtime alone: also read once.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(v2, later, later); err != nil {
		t.Fatal(err)
	}
	_, _ = svc.VersionsLive(ctx, 1)
	svc.bg.Wait()
	if p := fc.probes.Load(); p != 2 {
		t.Fatalf("after an mtime change: probes = %d, want 2", p)
	}
	if v, _, _ := svc.repo.GetVersion(ctx, v2ID); v.File == nil || v.File.MtimeUnix != later.Unix() {
		t.Fatalf("the re-read should carry the new mtime: %+v", v.File)
	}

	// Everything current again: no probe.
	_, _ = svc.VersionsLive(ctx, 1)
	svc.bg.Wait()
	if p := fc.probes.Load(); p != 2 {
		t.Fatalf("current again: probes = %d, want still 2", p)
	}
}

// A missing file shows as missing, keeps the cached facts and schedules no probe.
func TestVersionsLiveMissingFile(t *testing.T) {
	svc, ctx := testService(t)
	var fc fileCounter
	fc.install(svc)
	def, _, _, _, _ := seedCachedTracks(t, svc, ctx)
	if err := os.Remove(def); err != nil {
		t.Fatal(err)
	}
	vs, err := svc.VersionsLive(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	svc.bg.Wait()
	if f := vs[0].File; f == nil || !f.Missing || f.Quality != "1080p BluRay" {
		t.Fatalf("missing default track = %+v", f)
	}
	if p := fc.probes.Load(); p != 0 {
		t.Fatalf("a missing file was probed %d times", p)
	}
}

// An import reads the new file exactly once: routing, the size and the cache all come
// from that one read.
func TestMarkImportedProbesOnce(t *testing.T) {
	svc, ctx := testService(t)
	var fc fileCounter
	fc.install(svc) // probes answer 2160p HEVC
	svc.resolver = fakeResolver{allowed: map[string][]string{"hd": {"1080p"}, "uhd": {"2160p"}}}
	m, err := svc.repo.Create(ctx, Movie{TMDBID: 7, Title: "Film", Year: 2021, Monitored: true, QualityProfile: "hd"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.repo.CreateVersion(ctx, m.ID, Version{Label: "4K", QualityProfile: "uhd", Monitored: true}); err != nil {
		t.Fatal(err)
	}
	// The name says 1080p; the probe says 2160p, so it must route to the 4K track.
	p := filepath.Join(t.TempDir(), "Film.2021.1080p.mkv")
	if err := os.WriteFile(p, make([]byte, 300), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkImported(ctx, m.ID, p, "Film.2021.1080p.WEB-DL.x264-GRP"); err != nil {
		t.Fatal(err)
	}
	if n := fc.probes.Load(); n != 1 {
		t.Fatalf("probes = %d, want exactly 1 per import", n)
	}
	vs, _ := svc.VersionRows(ctx, m.ID)
	if vs[0].HasFile || !vs[1].HasFile || vs[1].FilePath != p {
		t.Fatalf("the probed 2160p file should land on the 4K track: %+v", vs)
	}
}

// An extra-track import fills movie_versions.media_json; clearing the track empties it.
func TestVersionMediaCachedOnImport(t *testing.T) {
	svc, ctx := testService(t)
	var fc fileCounter
	fc.install(svc)
	svc.resolver = fakeResolver{allowed: map[string][]string{"hd": {"1080p"}, "uhd": {"2160p"}}}
	m, _ := svc.repo.Create(ctx, Movie{TMDBID: 7, Title: "Film", Year: 2021, Monitored: true, QualityProfile: "hd"})
	v, _ := svc.repo.CreateVersion(ctx, m.ID, Version{Label: "4K", QualityProfile: "uhd", Monitored: true})
	p := filepath.Join(t.TempDir(), "Film.2021.2160p.UHD.BluRay.x265-GRP.mkv")
	if err := os.WriteFile(p, make([]byte, 300), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkImported(ctx, m.ID, p, ""); err != nil {
		t.Fatal(err)
	}
	got, _, _ := svc.repo.GetVersion(ctx, v.ID)
	if got.File == nil || !got.File.Probed || got.File.Codec != "HEVC" || got.File.SizeBytes != 300 || got.File.MtimeUnix == 0 {
		t.Fatalf("extra track cache after import = %+v", got.File)
	}
	if err := svc.repo.ClearVersionFile(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	var media string
	if err := svc.repo.db.QueryRowContext(ctx, `SELECT media_json FROM movie_versions WHERE id = ?`, v.ID).Scan(&media); err != nil || media != "" {
		t.Fatalf("ClearVersionFile left media_json = %q (%v)", media, err)
	}
}

// A pure rename (same size and mtime) carries the cached facts to the new path unread.
func TestRepointKeepsCacheOnPureRename(t *testing.T) {
	svc, ctx := testService(t)
	var fc fileCounter
	fc.install(svc)
	def, v1, _, v1ID, _ := seedCachedTracks(t, svc, ctx)
	newDef := filepath.Join(filepath.Dir(def), "Film (2021).mkv")
	newV1 := filepath.Join(filepath.Dir(v1), "Film (2021) 4K.mkv")
	for _, mv := range [][2]string{{def, newDef}, {v1, newV1}} {
		if err := os.Rename(mv[0], mv[1]); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.RepointMovieFile(ctx, 1, mv[0], mv[1], 100, ""); err != nil {
			t.Fatal(err)
		}
	}
	if n := fc.probes.Load(); n != 0 {
		t.Fatalf("a pure rename probed %d times", n)
	}
	m, _ := svc.repo.Get(ctx, 1)
	if m.File == nil || m.File.Path != newDef || m.File.Quality != "1080p BluRay" {
		t.Fatalf("default cache after rename = %+v", m.File)
	}
	if v, _, _ := svc.repo.GetVersion(ctx, v1ID); v.File == nil || v.File.Path != newV1 || v.File.Quality != "2160p BluRay" {
		t.Fatalf("extra cache after rename = %+v", v.File)
	}
}

// Convert's swap changes the file: the new one is read once (QUAL-09 keeps that
// synchronous so the upgrade sweep sees the converted size straight away).
func TestRepointReprobesOnSizeChange(t *testing.T) {
	svc, ctx := testService(t)
	var fc fileCounter
	fc.install(svc)
	_, v1, _, v1ID, _ := seedCachedTracks(t, svc, ctx)
	conv := filepath.Join(filepath.Dir(v1), "Film.2021.2160p.UHD.BluRay.AV1-GRP.mkv")
	if err := os.WriteFile(conv, make([]byte, 40), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RepointMovieFile(ctx, 1, v1, conv, 40, "AV1"); err != nil {
		t.Fatal(err)
	}
	if n := fc.probes.Load(); n != 1 {
		t.Fatalf("probes = %d, want 1", n)
	}
	if v, _, _ := svc.repo.GetVersion(ctx, v1ID); v.File == nil || v.File.Path != conv || v.File.SizeBytes != 40 || v.SizeBytes != 40 {
		t.Fatalf("extra cache after convert = %+v (size %d)", v.File, v.SizeBytes)
	}
}
