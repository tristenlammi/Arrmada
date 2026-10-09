package movies

import (
	"os"
	"path/filepath"
	"testing"
)

// The upgrade hold ("keep existing files"): set in bulk on rows with a file, kept through a
// convert's repoint, ended by a new import, a profile change or Resume.
func TestUpgradeHoldLifecycle(t *testing.T) {
	svc, ctx := testService(t)
	dir := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	db := svc.repo.db
	main := mk("Heat.1995.1080p.BluRay.x264-GRP.mkv")
	extra := mk("Heat.1995.2160p.BluRay.x265-GRP.mkv")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO movies (id, tmdb_id, title, quality_profile, monitored, has_file, movie_file_path, source_release)
		VALUES (1, 101, 'Heat', 'custom:1', 1, 1, ?, 'Heat.1995.1080p.BluRay.x264-GRP')`, main)
	exec(`INSERT INTO movies (id, tmdb_id, title, quality_profile, monitored, has_file) VALUES (2, 102, 'Ronin', 'custom:1', 1, 0)`)
	exec(`INSERT INTO movie_versions (id, movie_id, label, quality_profile, monitored, has_file, file_path, source_release)
		VALUES (1, 1, '4K', 'custom:1', 1, 1, ?, 'Heat.1995.2160p.BluRay.x265-GRP')`, extra)
	held := func() (movie, version, missing bool) {
		t.Helper()
		m, err := svc.Get(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		vs, err := svc.VersionRows(ctx, 1)
		if err != nil || len(vs) != 2 {
			t.Fatalf("versions: %v %v", vs, err)
		}
		r, _ := svc.Get(ctx, 2)
		if vs[0].UpgradeHold != m.UpgradeHold {
			t.Errorf("default version hold %v != movie hold %v", vs[0].UpgradeHold, m.UpgradeHold)
		}
		return m.UpgradeHold, vs[1].UpgradeHold, r.UpgradeHold
	}

	m, v, err := svc.HoldUpgrades(ctx, []int64{1, 2}, []int64{1})
	if err != nil || m != 1 || v != 1 {
		t.Fatalf("hold: movies %d versions %d err %v — want 1 and 1 (Ronin has no file)", m, v, err)
	}
	if hm, hv, hr := held(); !hm || !hv || hr {
		t.Fatalf("after hold: movie %v version %v missing %v", hm, hv, hr)
	}

	// A convert repoints the file and keeps the hold.
	converted := mk("Heat.1995.1080p.BluRay.AV1-GRP.mkv")
	if _, err := svc.RepointMovieFile(ctx, 1, main, converted, 1<<30, "AV1"); err != nil {
		t.Fatal(err)
	}
	if hm, _, _ := held(); !hm {
		t.Error("a convert's repoint ended the hold")
	}

	// Setting the same profile again isn't a change; another profile is.
	if err := svc.SetQualityProfile(ctx, 1, "custom:1"); err != nil {
		t.Fatal(err)
	}
	if hm, _, _ := held(); !hm {
		t.Error("re-saving the same profile ended the hold")
	}
	if err := svc.SetQualityProfile(ctx, 1, "custom:2"); err != nil {
		t.Fatal(err)
	}
	if hm, hv, _ := held(); hm || !hv {
		t.Errorf("profile change: movie hold %v (want ended), 4K track %v (want kept: its own profile didn't change)", hm, hv)
	}
	if err := svc.UpdateVersion(ctx, 1, "4K", "custom:2", "", true); err != nil {
		t.Fatal(err)
	}
	if _, hv, _ := held(); hv {
		t.Error("changing the track's profile didn't end its hold")
	}

	// A real import of a new file ends the hold on the track it lands on.
	if _, _, err := svc.HoldUpgrades(ctx, []int64{1}, nil); err != nil {
		t.Fatal(err)
	}
	upgrade := mk("Heat.1995.1080p.BluRay.REMUX-GRP.mkv")
	if err := svc.MarkImported(ctx, 1, upgrade, "Heat.1995.1080p.BluRay.REMUX-GRP"); err != nil {
		t.Fatal(err)
	}
	if hm, _, _ := held(); hm {
		t.Error("importing a new file kept the hold")
	}

	// Resume clears every track.
	if _, _, err := svc.HoldUpgrades(ctx, []int64{1}, []int64{1}); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.ResumeUpgrades(ctx, 1); err != nil || n != 2 {
		t.Fatalf("resume: %d %v, want 2", n, err)
	}
	if hm, hv, _ := held(); hm || hv {
		t.Error("resume left a hold")
	}
	if _, err := svc.ResumeUpgrades(ctx, 99); err == nil {
		t.Error("resume on a missing movie: want an error")
	}
}
