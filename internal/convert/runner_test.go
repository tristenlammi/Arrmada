package convert

import (
	"context"
	"testing"
	"time"
)

func TestWindowAllows(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 1, h, m, 0, 0, time.Local) }
	cases := []struct {
		start, end string
		now        time.Time
		want       bool
	}{
		{"", "", at(14, 0), true},
		{"01:00", "06:00", at(3, 0), true},
		{"01:00", "06:00", at(6, 0), false},
		{"22:00", "06:00", at(23, 30), true},
		{"22:00", "06:00", at(5, 59), true},
		{"22:00", "06:00", at(12, 0), false},
		{"bad", "06:00", at(12, 0), true},
	}
	for _, c := range cases {
		if got := windowAllowsAt(c.start, c.end, c.now); got != c.want {
			t.Errorf("%s–%s at %s: %v, want %v", c.start, c.end, c.now.Format("15:04"), got, c.want)
		}
	}
}

// The restart confirm names the longest-running conversion's age and progress; finished
// jobs don't count.
func TestConvertActiveSummary(t *testing.T) {
	now := time.Now().Unix()
	s := &Service{jobs: []*Job{
		{State: StateEncoding, StartedAt: now - 600, Progress: 0.9},
		{State: StateEncoding, StartedAt: now - 3*3600, Progress: 0.41},
		{State: StateDone, StartedAt: now - 10*3600, Progress: 1},
		{State: StateVerifying, StartedAt: now - 60, Progress: 1},
	}}
	n, longest, progress := s.ActiveSummary()
	if n != 3 {
		t.Errorf("running = %d, want 3", n)
	}
	if longest < 3*3600 || longest > 3*3600+5 {
		t.Errorf("longest = %ds, want about 3h", longest)
	}
	if progress != 0.41 {
		t.Errorf("progress = %v, want the longest job's 0.41", progress)
	}
	if n, longest, progress := (&Service{}).ActiveSummary(); n != 0 || longest != 0 || progress != 0 {
		t.Errorf("idle: %d %d %v", n, longest, progress)
	}
}

// Hand-picked files ignore the hours (you asked for them); nothing runs while someone is
// watching.
func TestPauseReason(t *testing.T) {
	s := &Service{}
	closed := prefs{start: "00:00", end: "00:01"} // a window that's (almost) never open
	auto, asked := &Job{}, &Job{Requested: true}
	if !windowAllows(closed.start, closed.end) {
		if s.pauseReason(auto, closed, false) == "" {
			t.Error("an automatic job outside the hours must pause")
		}
		if s.pauseReason(asked, closed, false) != "" {
			t.Error("a requested job must ignore the hours")
		}
	}
	if s.pauseReason(asked, prefs{}, true) == "" {
		t.Error("everything pauses while someone is watching")
	}
	if s.pauseReason(auto, prefs{}, false) != "" {
		t.Error("no hours set and nobody watching: run")
	}
}

func indexMovie(t *testing.T, s *Service, id int64, title string, mi *MediaInfo) {
	t.Helper()
	mi.SizeBytes = int64(mi.BitrateKbps) * 1000 / 8 * int64(mi.DurationSec)
	err := s.index.upsert(context.Background(), indexRow{
		Path: "/movies/" + title + ".mkv", MediaType: "movie", MovieID: id, Title: title,
		SizeBytes: mi.SizeBytes, Codec: mi.VideoCodec, Info: mi,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The runner picks requests first, then the biggest saving, and leaves alone what it must:
// files that don't need anything, permanent skips, temporary skips not yet due, repeated
// failures, and files already converting.
func TestPickJobOrder(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	indexMovie(t, s, 1, "Lean", film("h264", 1920, 1080, 2000, aud("aac", "eng", 2)))
	indexMovie(t, s, 2, "Remux", film("h264", 1920, 1080, 35000, aud("truehd", "eng", 8)))
	indexMovie(t, s, 3, "BluRay", film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6)))
	indexMovie(t, s, 4, "Efficient", film("hevc", 1920, 1080, 5000, aud("eac3", "eng", 6)))

	if j := s.pickJob(ctx); j != nil {
		t.Fatalf("automatic conversion is off, but %s was picked", j.Title)
	}
	set(t, s, map[string]string{keyAuto: "true"})
	j := s.pickJob(ctx)
	if j == nil || j.MovieID != 2 {
		t.Fatalf("want the remux (biggest saving) first, got %+v", j)
	}
	if j2 := s.pickJob(ctx); j2 == nil || j2.MovieID != 3 {
		t.Fatalf("a second worker must get the next file, not the one already converting: %+v", j2)
	}
	if j3 := s.pickJob(ctx); j3 != nil {
		t.Fatalf("lean and efficient files need nothing, but got %s", j3.Title)
	}

	// Release both; a permanent skip and a blocklisted file are left alone.
	for _, k := range []string{movieKey(2), movieKey(3)} {
		s.releasePending(k, s.pending[k])
	}
	s.skips.record(ctx, movieKey(2), SkipNotSmaller, "only 12% smaller")
	for i := 0; i < maxFailures; i++ {
		s.failures.recordFailure(ctx, movieKey(3), "boom")
	}
	s.invalidateLibraryCache()
	if j := s.pickJob(ctx); j != nil {
		t.Fatalf("skipped and blocklisted files must not be picked: got %s", j.Title)
	}

	// A request goes first, runs outside the hours, and clears the old failures.
	set(t, s, map[string]string{keySweepStart: "00:00", keySweepEnd: "00:01"})
	if err := s.requests.add(ctx, movieKey(3), "BluRay"); err != nil {
		t.Fatal(err)
	}
	if j := s.pickJob(ctx); j == nil || j.MovieID != 3 || !j.Requested {
		t.Fatalf("the request should be picked even outside the hours: %+v", j)
	}
}

// A temporary skip (still seeding) is retried once its time comes, not on every pick.
func TestTemporarySkipWaits(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	s.skips.record(ctx, movieKey(9), SkipHardlinked, "still seeding")
	if !s.skips.waitingKeys(ctx)[movieKey(9)] {
		t.Fatal("a seeding file must wait before it's tried again")
	}
	if _, err := s.db.Exec(`UPDATE convert_skips SET retry_after = ? WHERE item_key = ?`, time.Now().Add(-time.Minute).Unix(), movieKey(9)); err != nil {
		t.Fatal(err)
	}
	if s.skips.waitingKeys(ctx)[movieKey(9)] {
		t.Fatal("once its retry time has passed, the file is eligible again")
	}
}

// Stopping a job the runner picked keeps it from being picked again straight away.
func TestCancelledAutoJobIsNotRepicked(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	set(t, s, map[string]string{keyAuto: "true"})
	indexMovie(t, s, 2, "Remux", film("h264", 1920, 1080, 35000, aud("truehd", "eng", 8)))
	j := s.pickJob(ctx)
	if j == nil {
		t.Fatal("nothing picked")
	}
	s.update(j, func(x *Job) { x.cancelled = true; x.State = StateEncoding })
	s.finish(j, StateFailed, "signal: killed")
	if j.State != StateCancelled {
		t.Fatalf("state = %s, want cancelled", j.State)
	}
	if again := s.pickJob(ctx); again != nil {
		t.Fatalf("a file you cancelled was picked again: %s", again.Title)
	}
}

func TestParseKeyRoundTrip(t *testing.T) {
	for _, k := range []string{"movie:12", "episode:3:0:7"} {
		it, ok := parseKey(k)
		if !ok || it.key() != k {
			t.Errorf("%s → %+v → %s", k, it, it.key())
		}
	}
	for _, bad := range []string{"", "movie:x", "episode:1:2", "song:3"} {
		if _, ok := parseKey(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}
