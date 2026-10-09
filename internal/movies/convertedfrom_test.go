package movies

import (
	"os"
	"path/filepath"
	"testing"
)

// Convert records what a file was before it shrank it; its own repoint keeps that, a
// second conversion doesn't overwrite the first original, and a real import clears it.
// Temp-dir files only.
func TestConvertedFromBaseline(t *testing.T) {
	svc, ctx := testService(t)
	dir := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	orig := mk("Film.2021.1080p.BluRay.REMUX.AVC-FGT.mkv")
	m, err := svc.repo.Create(ctx, Movie{TMDBID: 7, Title: "Film", Year: 2021, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkImported(ctx, m.ID, orig, "Film.2021.1080p.BluRay.REMUX.AVC-FGT"); err != nil {
		t.Fatal(err)
	}
	ver := mk("Film.2021.2160p.UHD.BluRay.x265-GRP.mkv")
	if _, err := svc.repo.db.ExecContext(ctx, `INSERT INTO movie_versions (id, movie_id, label, has_file, file_path, size_bytes, source_release)
		VALUES (1, ?, '4K', 1, ?, 9, 'Film.2021.2160p.UHD.BluRay.x265-GRP')`, m.ID, ver); err != nil {
		t.Fatal(err)
	}

	// Convert: record, then repoint and restamp.
	if err := svc.SetConvertedFrom(ctx, m.ID, orig, 80<<30); err != nil {
		t.Fatal(err)
	}
	conv := filepath.Join(dir, "Film.av1.mkv")
	if _, err := svc.RepointMovieFile(ctx, m.ID, orig, conv, 30<<30, "AV1"); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.repo.Get(ctx, m.ID)
	if got.ConvertedFromRelease != "Film.2021.1080p.BluRay.REMUX.AVC-FGT" || got.ConvertedFromSize != 80<<30 {
		t.Fatalf("baseline after convert = %q / %d", got.ConvertedFromRelease, got.ConvertedFromSize)
	}
	if got.SourceRelease == got.ConvertedFromRelease {
		t.Fatal("precondition: the repoint restamps the release to the new codec")
	}
	vs, _ := svc.VersionRows(ctx, m.ID)
	if vs[0].ConvertedFromRelease != got.ConvertedFromRelease || vs[0].ConvertedFromSize != 80<<30 {
		t.Errorf("the default version must carry the movie's baseline: %+v", vs[0])
	}
	if vs[1].ConvertedFromRelease != "" {
		t.Errorf("the 4K version wasn't converted: %+v", vs[1])
	}

	// A re-conversion keeps the first original.
	if err := svc.SetConvertedFrom(ctx, m.ID, conv, 30<<30); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.repo.Get(ctx, m.ID); got.ConvertedFromSize != 80<<30 || got.ConvertedFromRelease != "Film.2021.1080p.BluRay.REMUX.AVC-FGT" {
		t.Errorf("a re-conversion replaced the first original: %q / %d", got.ConvertedFromRelease, got.ConvertedFromSize)
	}

	// The extra version's own conversion is recorded on it.
	if err := svc.SetConvertedFrom(ctx, m.ID, ver, 9); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := svc.repo.GetVersion(ctx, 1); v.ConvertedFromRelease != "Film.2021.2160p.UHD.BluRay.x265-GRP" || v.ConvertedFromSize != 9 {
		t.Errorf("version baseline = %q / %d", v.ConvertedFromRelease, v.ConvertedFromSize)
	}

	// A real upgrade import clears it.
	if err := os.WriteFile(conv, []byte("converted"), 0o644); err != nil {
		t.Fatal(err)
	}
	up := mk("Film.2021.2160p.WEB-DL.x265-GRP.mkv")
	if err := svc.MarkImported(ctx, m.ID, up, "Film.2021.2160p.WEB-DL.x265-GRP"); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.repo.Get(ctx, m.ID); got.ConvertedFromRelease != "" || got.ConvertedFromSize != 0 {
		t.Errorf("an import must clear the baseline: %q / %d", got.ConvertedFromRelease, got.ConvertedFromSize)
	}
}

// After Convert's repoint the default file's cached media info describes the converted
// file, not the original: the upgrade sweep reads its size from there. Temp files only.
func TestRepointRefreshesCachedMedia(t *testing.T) {
	svc, ctx := testService(t)
	dir := t.TempDir()
	orig, conv := filepath.Join(dir, "Film.m2ts"), filepath.Join(dir, "Film.mkv")
	if err := os.WriteFile(orig, make([]byte, 4000), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := svc.repo.Create(ctx, Movie{TMDBID: 8, Title: "Film", Year: 2021, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkImported(ctx, m.ID, orig, "Film.2021.1080p.BluRay.REMUX.AVC-FGT"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conv, make([]byte, 1500), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RepointMovieFile(ctx, m.ID, orig, conv, 1500, "AV1"); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.repo.Get(ctx, m.ID)
	if got.File == nil || got.File.Path != conv || got.File.SizeBytes != 1500 {
		t.Errorf("cached media after repoint = %+v, want the converted file's", got.File)
	}
}
