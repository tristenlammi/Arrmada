package convert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// spaceRig is a Service with a real movies module over temp folders, for driving process()
// and finalizeOutput() without ffmpeg: ffprobe and the disks are stubbed through the seams.
type spaceRig struct {
	*Service
	lib, scratch, recycle string
}

func newSpaceRig(t *testing.T) *spaceRig {
	t.Helper()
	s := newTestService(t)
	dir := t.TempDir()
	r := &spaceRig{Service: s, lib: filepath.Join(dir, "lib"), scratch: filepath.Join(dir, "scratch"), recycle: filepath.Join(dir, "recycle")}
	for _, d := range []string{r.lib, r.scratch, r.recycle} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s.movies = movies.NewService(s.db, nil, nil, r.lib, r.recycle, eventbus.New(s.log), s.log)
	s.scratchDir, s.recycleDir = r.scratch, r.recycle
	// Nothing on the PATH by these names: any step that shells out fails instead of running.
	s.ffmpeg, s.ffprobe = filepath.Join(dir, "no-ffmpeg"), filepath.Join(dir, "no-ffprobe")
	return r
}

// addMovie writes a small stand-in file to the library and records it as the movie's file.
func (r *spaceRig) addMovie(t *testing.T, id int64, title string, size int) string {
	t.Helper()
	path := filepath.Join(r.lib, title+".mkv")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`INSERT INTO movies (id, tmdb_id, title, monitored, has_file, movie_file_path) VALUES (?, ?, ?, 1, 1, ?)`,
		id, 1000+id, title, path); err != nil {
		t.Fatal(err)
	}
	return path
}

// stubProbe makes every probe report a copy of mi.
func stubProbe(t *testing.T, mi *MediaInfo) {
	t.Helper()
	old := probeFn
	probeFn = func(context.Context, string, string) (*MediaInfo, error) { c := *mi; return &c, nil }
	t.Cleanup(func() { probeFn = old })
}

// stubDisks reports scratchFree for the scratch folder and libFree everywhere else.
func stubDisks(t *testing.T, scratch string, scratchFree, libFree uint64) {
	t.Helper()
	oldFree := freeBytesFn
	freeBytesFn = func(dir string) uint64 {
		if dir == scratch {
			return scratchFree
		}
		return libFree
	}
	t.Cleanup(func() { freeBytesFn = oldFree })
}

func (r *spaceRig) skipOf(t *testing.T, key string) (kind string, retryAfter int64, attempts int) {
	t.Helper()
	if err := r.db.QueryRow(`SELECT kind, retry_after, attempts FROM convert_skips WHERE item_key = ?`, key).
		Scan(&kind, &retryAfter, &attempts); err != nil {
		return "", 0, 0
	}
	return kind, retryAfter, attempts
}

func (r *spaceRig) run(t *testing.T, id int64, title string) *Job {
	t.Helper()
	it, _ := parseKey(movieKey(id))
	job := r.claim(it, title, true)
	if job == nil {
		t.Fatalf("%s is already claimed", title)
	}
	r.process(context.Background(), job)
	return job
}

func near(t *testing.T, got int64, want time.Duration) {
	t.Helper()
	if d := time.Until(time.Unix(got, 0)); d < want-time.Minute || d > want+time.Minute {
		t.Fatalf("retries in %s, want ≈ %s", d.Round(time.Second), want)
	}
}

// A full scratch folder skips the file with a growing wait — 1 h, 6 h, then daily — instead
// of failing it and picking it straight back up. A finished conversion starts the count over.
func TestNoScratchBacksOff(t *testing.T) {
	r := newSpaceRig(t)
	r.addMovie(t, 1, "Remux", 64)
	stubProbe(t, film("h264", 1920, 1080, 35000, aud("truehd", "eng", 8)))
	stubDisks(t, r.scratch, 1<<30, 1<<50)

	for i, want := range []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour, 24 * time.Hour} {
		job := r.run(t, 1, "Remux")
		if job.State != StateSkipped {
			t.Fatalf("try %d: state %s (%s), want skipped", i+1, job.State, job.Note)
		}
		if !strings.Contains(job.Note, "of scratch in "+r.scratch) || !strings.Contains(job.Note, "1.0 GB free") {
			t.Fatalf("try %d: the reason should give the figures: %q", i+1, job.Note)
		}
		kind, retry, attempts := r.skipOf(t, movieKey(1))
		if kind != SkipNoScratch || attempts != i+1 {
			t.Fatalf("try %d: skip %q attempts %d", i+1, kind, attempts)
		}
		near(t, retry, want)
	}
	if n := r.failures.failureCount(context.Background(), movieKey(1)); n != 0 {
		t.Fatalf("a full disk counted %d time(s) toward the failure limit", n)
	}

	// A success clears it, so the next full disk waits an hour again.
	it, _ := parseKey(movieKey(1))
	done := r.claim(it, "Remux", true)
	r.finish(done, StateDone, "")
	if kind, _, _ := r.skipOf(t, movieKey(1)); kind != "" {
		t.Fatalf("a finished conversion left the skip behind: %s", kind)
	}
	r.run(t, 1, "Remux")
	_, retry, attempts := r.skipOf(t, movieKey(1))
	if attempts != 1 {
		t.Fatalf("attempts = %d after a success, want 1", attempts)
	}
	near(t, retry, time.Hour)
}

// The library disk needs room for the converted file wherever scratch is: two mounts of one
// share can't rename into each other, so the hand-in may be a full copy either way.
func TestLibraryDiskAlwaysChecked(t *testing.T) {
	r := newSpaceRig(t)
	r.addMovie(t, 1, "Remux", 64)
	stubProbe(t, film("h264", 1920, 1080, 35000, aud("truehd", "eng", 8)))

	stubDisks(t, r.scratch, 1<<50, 1<<30)
	job := r.run(t, 1, "Remux")
	if kind, _, _ := r.skipOf(t, movieKey(1)); kind != SkipLibraryFull || !strings.Contains(job.Note, "library disk") {
		t.Fatalf("got %q (%s), want library_full", kind, job.Note)
	}

	r.skips.clear(context.Background(), movieKey(1))
	stubDisks(t, r.scratch, 1<<50, 1<<50)
	job = r.run(t, 1, "Remux")
	if kind, _, _ := r.skipOf(t, movieKey(1)); kind == SkipLibraryFull || kind == SkipNoScratch {
		t.Fatalf("with room on both disks: got %s (%s)", kind, job.Note)
	}
}

// A file the library records but that isn't on disk (mid-import, mid-upgrade) is a
// backing-off skip — not a probe failure counted toward the failure limit.
func TestSourceGoneBacksOff(t *testing.T) {
	r := newSpaceRig(t)
	src := r.addMovie(t, 5, "Elsewhere", 64)
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	stubProbe(t, film("h264", 1920, 1080, 35000, aud("truehd", "eng", 8)))
	job := r.run(t, 5, "Elsewhere")
	kind, retry, _ := r.skipOf(t, movieKey(5))
	if job.State != StateSkipped || kind != SkipSourceGone {
		t.Fatalf("state %s (%s), skip %q", job.State, job.Note, kind)
	}
	near(t, retry, time.Hour)
	if n := r.failures.failureCount(context.Background(), movieKey(5)); n != 0 {
		t.Fatalf("a missing file counted %d time(s) toward the failure limit", n)
	}
}

// A title with no file recorded at all (deleted since it was indexed) won't come back by
// waiting: nothing goes to Problems, and its stale index row goes so it isn't picked again.
func TestNoRecordedFileLeavesNoSkip(t *testing.T) {
	ctx := context.Background()
	r := newSpaceRig(t)
	set(t, r.Service, map[string]string{keyAuto: "true"})
	indexMovie(t, r.Service, 5, "Deleted", film("h264", 1920, 1080, 35000, aud("truehd", "eng", 8)))

	job := r.pickJob(ctx)
	if job == nil || job.MovieID != 5 {
		t.Fatalf("want the indexed movie picked, got %+v", job)
	}
	r.skips.record(ctx, movieKey(5), SkipSourceGone, "missing from disk") // from an earlier try
	r.process(ctx, job)
	if job.State != StateSkipped {
		t.Fatalf("state %s (%s), want skipped", job.State, job.Note)
	}
	if kind, _, _ := r.skipOf(t, movieKey(5)); kind != "" {
		t.Fatalf("a title with no file left a %q skip in Problems", kind)
	}
	if next := r.pickJob(ctx); next != nil {
		t.Fatalf("the stale index row should be gone, but %+v was picked", next)
	}
}

// After a file is skipped for space the runner moves on to the next one in the same window.
func TestPickMovesOnAfterNoScratch(t *testing.T) {
	ctx := context.Background()
	r := newSpaceRig(t)
	set(t, r.Service, map[string]string{keyAuto: "true"})
	indexMovie(t, r.Service, 2, "Remux", film("h264", 1920, 1080, 35000, aud("truehd", "eng", 8)))
	indexMovie(t, r.Service, 3, "BluRay", film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6)))
	r.addMovie(t, 2, "Remux", 64)
	stubProbe(t, film("h264", 1920, 1080, 35000, aud("truehd", "eng", 8)))
	stubDisks(t, r.scratch, 1<<30, 1<<50)

	job := r.pickJob(ctx)
	if job == nil || job.MovieID != 2 {
		t.Fatalf("want the remux first, got %+v", job)
	}
	r.process(ctx, job)
	if job.State != StateSkipped {
		t.Fatalf("state %s (%s), want skipped", job.State, job.Note)
	}
	if next := r.pickJob(ctx); next == nil || next.MovieID != 3 {
		t.Fatalf("after a no_scratch skip the next file should be picked, got %+v", next)
	}
}

// No HDR10+ read, crop detection or format test runs for a file that fails the space check;
// and the whole-file HDR10+ read waits until the doubled HDR10+ need is known to fit.
func TestSpaceCheckRunsBeforeHeavyWork(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses shell-script stand-ins for ffmpeg and hdr10plus_tool")
	}
	r := newSpaceRig(t)
	r.addMovie(t, 1, "HDR", 64)
	mi := film("hevc", 3840, 2160, 60000, aud("truehd", "eng", 8))
	mi.HDR = "HDR10"
	stubProbe(t, mi)
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := func(name, body string) string {
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho \""+name+" $*\" >> "+calls+"\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	r.ffmpeg = script("ffmpeg", "exit 0\n")
	r.hdr10plusTool = script("hdr10plus_tool",
		"while [ $# -gt 0 ]; do [ \"$1\" = -o ] && out=$2; shift; done\ncat > /dev/null\nprintf '{\"SceneInfo\":[0,1,2]}' > \"$out\"\n")
	log := func() string { b, _ := os.ReadFile(calls); return string(b) }

	// Not even room for the plain encode: nothing runs at all.
	plan, _ := r.prefs(context.Background()).planFor(mi, "", "", nil)
	plan.VideoCodec = "hevc"
	plain := scratchNeeded(mi, plan, false)
	stubDisks(t, r.scratch, uint64(plain-1), 1<<50)
	r.run(t, 1, "HDR")
	if kind, _, _ := r.skipOf(t, movieKey(1)); kind != SkipNoScratch {
		t.Fatalf("skip %q, want no_scratch", kind)
	}
	if l := log(); l != "" {
		t.Fatalf("heavy work ran for a file that can't fit:\n%s", l)
	}

	// Room for a plain encode but not the HDR10+ pipeline: the 100-frame look runs, the
	// whole-file read doesn't.
	r.skips.clear(context.Background(), movieKey(1))
	stubDisks(t, r.scratch, uint64(plain+1), 1<<50)
	r.run(t, 1, "HDR")
	if kind, _, _ := r.skipOf(t, movieKey(1)); kind != SkipNoScratch {
		t.Fatalf("skip %q, want no_scratch", kind)
	}
	l := log()
	if !strings.Contains(l, "--limit 100") {
		t.Fatalf("the HDR10+ look should have run:\n%s", l)
	}
	for _, line := range strings.Split(strings.TrimSpace(l), "\n") {
		if strings.HasPrefix(line, "hdr10plus_tool") && !strings.Contains(line, "--limit") {
			t.Fatalf("the whole-file HDR10+ read ran without room for the pipeline:\n%s", l)
		}
	}
	if n := strings.Count(l, "ffmpeg "); n != 1 {
		t.Fatalf("want only the HDR10+ look's ffmpeg, got %d calls:\n%s", n, l)
	}
}

// A full library disk while staging the finished file keeps the original, discards the
// staged copy and backs off — rather than re-running the whole encode on the next pick.
func TestStagingENOSPCIsLibraryFull(t *testing.T) {
	r := newSpaceRig(t)
	src := r.addMovie(t, 1, "Remux", 1000)
	mi := film("h264", 1920, 1080, 35000, aud("truehd", "eng", 8))
	mi.SizeBytes = 1000
	plan := Plan{VideoCodec: "hevc"}
	stubProbe(t, &MediaInfo{VideoCodec: "hevc", DurationSec: mi.DurationSec,
		AudioTracks: len(keptAudio(mi, plan)), SubTracks: len(keptSubs(mi, plan))})
	stubDisks(t, r.scratch, 1<<50, 1<<20)
	dst := filepath.Join(r.scratch, "convert-1.mkv")
	if err := os.WriteFile(dst, []byte(strings.Repeat("y", 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	oldMove := moveFileFn
	moveFileFn = func(from, to string) error {
		_ = os.WriteFile(to, []byte("partial"), 0o644) // a copy cut short by the full disk
		return &os.PathError{Op: "write", Path: to, Err: syscall.ENOSPC}
	}
	t.Cleanup(func() { moveFileFn = oldMove })

	it, _ := parseKey(movieKey(1))
	job := r.claim(it, "Remux", true)
	r.finalizeOutput(context.Background(), job, src, dst, mi, plan)

	kind, retry, _ := r.skipOf(t, movieKey(1))
	if job.State != StateSkipped || kind != SkipLibraryFull {
		t.Fatalf("state %s, skip %q (%s)", job.State, kind, job.Note)
	}
	near(t, retry, time.Hour)
	if !strings.Contains(job.Note, "original kept") {
		t.Fatalf("note %q", job.Note)
	}
	if b, err := os.ReadFile(src); err != nil || len(b) != 1000 {
		t.Fatalf("the original was touched: %d bytes, %v", len(b), err)
	}
	if _, err := os.Stat(src + ".arrpart"); !os.IsNotExist(err) {
		t.Fatal("the partial staged copy was left behind")
	}
	if entries, _ := os.ReadDir(r.recycle); len(entries) != 0 {
		t.Fatal("the original was moved to the recycle bin")
	}
	if n := r.failures.failureCount(context.Background(), movieKey(1)); n != 0 {
		t.Fatalf("a full disk counted %d time(s) toward the failure limit", n)
	}
}

// A transient failure that reaches finish() still backs off instead of being re-picked.
func TestTransientFailureBacksOff(t *testing.T) {
	s := newTestService(t)
	it, _ := parseKey(movieKey(4))
	job := s.claim(it, "Film", false)
	s.finish(job, StateFailed, "encode failed: av_interleaved_write_frame(): No space left on device")
	if !s.skips.waitingKeys(context.Background())[movieKey(4)] {
		t.Fatal("a transient failure must wait before the file is tried again")
	}
	if n := s.failures.failureCount(context.Background(), movieKey(4)); n != 0 {
		t.Fatalf("a transient failure counted %d time(s) toward the failure limit", n)
	}
}

// The attempt count grows while the kind repeats and starts over when it changes.
func TestSkipAttempts(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	k := movieKey(1)
	for want := 1; want <= 3; want++ {
		if got := s.skips.record(ctx, k, SkipNoScratch, "full"); got != want {
			t.Fatalf("attempts = %d, want %d", got, want)
		}
	}
	if got := s.skips.record(ctx, k, SkipLibraryFull, "full"); got != 1 {
		t.Fatalf("a new kind should start at 1, got %d", got)
	}
	list, err := s.skips.list(ctx)
	if err != nil || len(list) != 1 || list[0].Attempts != 1 || list[0].RetryAfter == 0 {
		t.Fatalf("list: %+v, %v", list, err)
	}
	if d := retryDelay(SkipHardlinked, 5); d != 12*time.Hour {
		t.Fatalf("seeding files keep their fixed wait, got %s", d)
	}
}

// A failed whole-file HDR10+ read is only "can't carry it" when the tool itself failed: a
// full scratch disk or a read error off the array waits and tries again.
func TestHDR10PlusReadFailureKinds(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	for i, c := range []struct {
		err   error
		kind  string
		count int
	}{
		{fmt.Errorf("exit status 1 (Error: No space left on device (os error 28))"), SkipNoScratch, 0},
		{&os.PathError{Op: "write", Path: "x", Err: syscall.ENOSPC}, SkipNoScratch, 0},
		{fmt.Errorf("%w: %w", errSourceStream, errors.New("exit status 1")), SkipTransient, 1},
		{errors.New("exit status 1 (Error: invalid HDR10+ payload)"), SkipHDRUnsupported, 0},
	} {
		id := int64(i + 1)
		it, _ := parseKey(movieKey(id))
		job := s.claim(it, "HDR", false)
		s.hdr10PlusReadFailed(job, c.err)
		var kind string
		var perm int
		_ = s.db.QueryRow(`SELECT kind, permanent FROM convert_skips WHERE item_key = ?`, movieKey(id)).Scan(&kind, &perm)
		if kind != c.kind || (perm == 1) != permanentSkip(c.kind) {
			t.Errorf("%v: skip %q (permanent %d), want %q", c.err, kind, perm, c.kind)
		}
		if n := s.failures.failureCount(ctx, movieKey(id)); n != c.count {
			t.Errorf("%v: %d failure(s) counted, want %d", c.err, n, c.count)
		}
	}
}
