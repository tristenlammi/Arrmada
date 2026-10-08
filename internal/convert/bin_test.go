package convert

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// binWith makes the recycle bin report free bytes of room under a cap.
func binWith(s *Service, free int64) {
	s.SetBinHeadroom(func(context.Context) (int64, bool, bool) { return free, true, true })
}

// An original bigger than the bin's room is never started: retiring it would make the bin
// purge it, and older deletions with it, within the hour.
func TestBinFullSkipsBeforeAnyWork(t *testing.T) {
	r := newSpaceRig(t)
	src := r.addMovie(t, 1, "Remux", 64)
	mi := film("h264", 3840, 2160, 85000, aud("truehd", "eng", 8)) // ~76 GB
	stubProbe(t, mi)
	stubDisks(t, r.scratch, 1<<50, 1<<50)
	binWith(r.Service, 12<<30)

	job := r.run(t, 1, "Remux")
	kind, retry, _ := r.skipOf(t, movieKey(1))
	if job.State != StateSkipped || kind != SkipBinFull {
		t.Fatalf("state %s, skip %q (%s)", job.State, kind, job.Note)
	}
	near(t, retry, 24*time.Hour)
	if !strings.Contains(job.Note, "12.0 GB left") || !strings.Contains(job.Note, "Settings → Recycle bin") {
		t.Fatalf("the reason should give the figures and the fix: %q", job.Note)
	}
	if b, err := os.ReadFile(src); err != nil || len(b) != 64 {
		t.Fatalf("the original was touched: %v", err)
	}
	if strings.Contains(strings.Join(logMsgs(r.Service), "\n"), "Encoding") {
		t.Fatal("an encode started for a file whose original can't be kept")
	}

	// An uncapped or switched-off bin purges nothing, so it doesn't stop a conversion.
	for _, h := range []func(context.Context) (int64, bool, bool){
		func(context.Context) (int64, bool, bool) { return 0, false, true },
		func(context.Context) (int64, bool, bool) { return 0, false, false },
	} {
		r.skips.clear(context.Background(), movieKey(1))
		r.SetBinHeadroom(h)
		job := r.run(t, 1, "Remux")
		if kind, _, _ := r.skipOf(t, movieKey(1)); kind == SkipBinFull {
			t.Fatalf("bin_full without a cap: %s", job.Note)
		}
	}
}

func logMsgs(s *Service) []string {
	var out []string
	for _, l := range s.Logs() {
		out = append(out, l.Msg)
	}
	return out
}

// finalizeRig stages a finished encode of a 1000-byte original, ready for finalizeOutput.
func finalizeRig(t *testing.T) (r *spaceRig, src, dst string, mi *MediaInfo, plan Plan) {
	t.Helper()
	r = newSpaceRig(t)
	src = r.addMovie(t, 1, "Remux", 1000)
	mi = film("h264", 1920, 1080, 35000, aud("truehd", "eng", 8))
	mi.SizeBytes = 1000
	plan = Plan{VideoCodec: "hevc"}
	stubProbe(t, &MediaInfo{VideoCodec: "hevc", DurationSec: mi.DurationSec,
		AudioTracks: len(keptAudio(mi, plan)), SubTracks: len(keptSubs(mi, plan))})
	stubDisks(t, r.scratch, 1<<50, 1<<50)
	dst = filepath.Join(r.scratch, "convert-1.mkv")
	if err := os.WriteFile(dst, []byte(strings.Repeat("y", 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	return r, src, dst, mi, plan
}

// The bin filled during the encode: the original stays, the staged copy goes, and the file
// waits — the encode is lost but nothing is destroyed.
func TestBinRecheckBeforeRetire(t *testing.T) {
	r, src, dst, mi, plan := finalizeRig(t)
	binWith(r.Service, 999)
	it, _ := parseKey(movieKey(1))
	job := r.claim(it, "Remux", true)
	r.finalizeOutput(context.Background(), job, src, dst, mi, plan)

	if kind, _, _ := r.skipOf(t, movieKey(1)); job.State != StateSkipped || kind != SkipBinFull {
		t.Fatalf("state %s, skip %q (%s)", job.State, kind, job.Note)
	}
	if b, err := os.ReadFile(src); err != nil || len(b) != 1000 {
		t.Fatalf("the original was touched: %v", err)
	}
	if _, err := os.Stat(src + ".arrpart"); !os.IsNotExist(err) {
		t.Fatal("the staged .arrpart was left behind")
	}
	if entries, _ := os.ReadDir(r.recycle); len(entries) != 0 {
		t.Fatal("the original went to the recycle bin anyway")
	}
	if n := r.failures.failureCount(context.Background(), movieKey(1)); n != 0 {
		t.Fatalf("a full bin counted %d time(s) toward the failure limit", n)
	}
}

// The bin fills while the finished file is being staged: the check under the lock, right
// before the original goes in, still catches it.
func TestBinRecheckAfterStaging(t *testing.T) {
	r, src, dst, mi, plan := finalizeRig(t)
	calls := 0
	r.SetBinHeadroom(func(context.Context) (int64, bool, bool) {
		calls++
		if calls == 1 {
			return 1000, true, true // room before staging
		}
		return 999, true, true
	})
	it, _ := parseKey(movieKey(1))
	job := r.claim(it, "Remux", true)
	r.finalizeOutput(context.Background(), job, src, dst, mi, plan)

	if kind, _, _ := r.skipOf(t, movieKey(1)); job.State != StateSkipped || kind != SkipBinFull || calls != 2 {
		t.Fatalf("state %s, skip %q after %d check(s) (%s)", job.State, kind, calls, job.Note)
	}
	if b, err := os.ReadFile(src); err != nil || len(b) != 1000 {
		t.Fatalf("the original was touched: %v", err)
	}
	if _, err := os.Stat(src + ".arrpart"); !os.IsNotExist(err) {
		t.Fatal("the staged .arrpart was left behind")
	}
}

// A bin that's too full is noticed before the finished file is copied onto the library disk.
func TestBinFullBeforeStaging(t *testing.T) {
	r, src, dst, mi, plan := finalizeRig(t)
	binWith(r.Service, 999)
	oldMove := moveFileFn
	moveFileFn = func(from, to string) error {
		t.Fatal("the encode was staged for an original the bin can't take")
		return nil
	}
	t.Cleanup(func() { moveFileFn = oldMove })
	it, _ := parseKey(movieKey(1))
	job := r.claim(it, "Remux", true)
	r.finalizeOutput(context.Background(), job, src, dst, mi, plan)
	if kind, _, _ := r.skipOf(t, movieKey(1)); kind != SkipBinFull {
		t.Fatalf("skip %q (%s), want bin_full", kind, job.Note)
	}
}

// Saving the bin's settings retries the files waiting for room straight away, and leaves
// every other kind of skip alone.
func TestBinSettingsChangedClearsBinFull(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	s.skips.record(ctx, movieKey(1), SkipBinFull, "too big")
	s.skips.record(ctx, movieKey(2), SkipNoScratch, "full")
	s.BinSettingsChanged(ctx)
	waiting := s.skips.waitingKeys(ctx)
	if waiting[movieKey(1)] || !waiting[movieKey(2)] {
		t.Fatalf("waiting after the change: %v", waiting)
	}
}

// Once a file is waiting for room in the bin, the runner passes over it without a probe or a
// log line while its original still doesn't fit — and picks it once it does.
func TestPickPassesOverStillTooBigForBin(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	set(t, s, map[string]string{keyAuto: "true"})
	indexMovie(t, s, 1, "Remux", film("h264", 3840, 2160, 85000, aud("truehd", "eng", 8)))
	if _, err := s.db.Exec(`INSERT INTO convert_skips (item_key, kind, reason, permanent, retry_after, attempts, updated_at)
		VALUES (?, ?, 'too big', 0, 0, 1, datetime('now'))`, movieKey(1), SkipBinFull); err != nil {
		t.Fatal(err) // a bin_full skip whose daily wait is over
	}
	binWith(s, 12<<30)
	if job := s.pickJob(ctx); job != nil {
		t.Fatalf("picked %+v though its original still can't fit", job)
	}
	binWith(s, 1<<50)
	if job := s.pickJob(ctx); job == nil || job.MovieID != 1 {
		t.Fatalf("with room in the bin it should be picked, got %+v", job)
	}
}

// With room in the bin the swap goes ahead as before.
func TestBinWithRoomRetiresTheOriginal(t *testing.T) {
	r, src, dst, mi, plan := finalizeRig(t)
	binWith(r.Service, 1000)
	it, _ := parseKey(movieKey(1))
	job := r.claim(it, "Remux", true)
	r.finalizeOutput(context.Background(), job, src, dst, mi, plan)

	if job.State != StateDone {
		t.Fatalf("state %s (%s), want done", job.State, job.Note)
	}
	if b, err := os.ReadFile(src); err != nil || string(b) != strings.Repeat("y", 500) {
		t.Fatalf("the converted file isn't in place: %v", err)
	}
	if entries, _ := os.ReadDir(r.recycle); len(entries) == 0 {
		t.Fatal("the original should be in the recycle bin")
	}
}
