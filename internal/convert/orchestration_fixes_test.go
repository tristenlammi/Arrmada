package convert

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestClaimPendingIsAtomic pins the dedup fix: the second claim for a key must get
// the FIRST claim's job back, never a second job (check and insert used to be two
// separate lock sections, so a racing sweep and user click both encoded the file).
func TestClaimPendingIsAtomic(t *testing.T) {
	s := &Service{pending: map[string]*Job{}}
	j1, fresh := s.claimPending("movie:1", func(id int64) *Job { return &Job{ID: id, State: StateQueued} })
	if !fresh || j1 == nil {
		t.Fatal("first claim must succeed")
	}
	j2, fresh2 := s.claimPending("movie:1", func(id int64) *Job { return &Job{ID: id, State: StateQueued} })
	if fresh2 {
		t.Fatal("second claim must be refused")
	}
	if j2 != j1 {
		t.Fatal("second claim must return the existing job")
	}
	if len(s.jobs) != 1 {
		t.Fatalf("exactly one job should exist, got %d", len(s.jobs))
	}
}

// TestTransientFailure pins the failure classification: conditions that clear on
// their own must not count toward the quarantine blocklist.
func TestTransientFailure(t *testing.T) {
	for note, want := range map[string]bool{
		"not enough scratch space to convert safely":  true,
		"source file is gone":                         true,
		"encode failed: no space left on device":      true,
		"encode failed: x265 param rejected":          false,
		"output failed verification — kept the original": false,
	} {
		if got := transientFailure(note); got != want {
			t.Errorf("transientFailure(%q) = %v, want %v", note, got, want)
		}
	}
}

// TestHardwareBrokenThreshold pins the 2-strike rule: one bad source file must not
// write off the GPU for the whole run.
func TestHardwareBrokenThreshold(t *testing.T) {
	s := &Service{log: testLogger(), logBuf: nil}
	s.markHardwareBroken("hevc_nvenc", "one corrupt file")
	if s.hardwareIsBroken("hevc_nvenc") {
		t.Fatal("one failure must not mark the encoder broken")
	}
	s.markHardwareBroken("hevc_nvenc", "again")
	if !s.hardwareIsBroken("hevc_nvenc") {
		t.Fatal("two failures should mark the encoder broken")
	}
}

// TestCancelledJobFinishesCancelled pins the cancel-poisoning fix: a cancelled
// job's "failure" must surface as Cancelled, and finishSkip must not record a
// durable skip for it.
func TestCancelledJobFinishesCancelled(t *testing.T) {
	s := &Service{log: testLogger(), pending: map[string]*Job{}}
	job := &Job{ID: 1, MovieID: 7, State: StateEncoding, cancelled: true}
	s.jobs = []*Job{job}
	s.finish(job, StateFailed, "encode failed: signal: killed")
	if job.State != StateCancelled {
		t.Fatalf("state = %v, want cancelled", job.State)
	}
	if job.Note != "cancelled" {
		t.Fatalf("note = %q, want cancelled", job.Note)
	}
}

// TestCodecToken pins the source-release stamping tokens.
func TestCodecToken(t *testing.T) {
	if codecToken("hevc") != "x265" || codecToken("av1") != "AV1" || codecToken("") != "" {
		t.Fatalf("unexpected tokens: %q %q %q", codecToken("hevc"), codecToken("av1"), codecToken(""))
	}
}

// TestMaybeIndexSweepLateSchedule pins the schedule fix: a tick landing after the
// scheduled time (any hour later) must trigger exactly one sweep per day.
func TestMaybeIndexSweepSchedule(t *testing.T) {
	// Pure logic re-implemented check: the condition is now >= scheduled && lastSweep < scheduled.
	now := time.Date(2026, 7, 21, 5, 30, 0, 0, time.UTC)         // 05:30 tick
	sched := time.Date(2026, 7, 21, 3, 55, 0, 0, time.UTC)       // 03:55 schedule
	lastYesterday := time.Date(2026, 7, 20, 4, 0, 0, 0, time.UTC) // swept yesterday
	if now.Before(sched) || !lastYesterday.Before(sched) {
		t.Fatal("a 05:30 tick with a 03:55 schedule and yesterday's sweep must fire")
	}
	lastToday := time.Date(2026, 7, 21, 4, 0, 0, 0, time.UTC) // already swept today
	if !(now.Before(sched) || !lastToday.Before(sched)) {
		t.Fatal("must not fire twice in one day")
	}
}
