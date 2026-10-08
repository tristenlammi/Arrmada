package subtitles

import "testing"

// After the sweep has queued a long backlog of AI runs, a file that just imported must be
// the very next thing the worker picks up — not days later.
func TestPopPrefersImportOverSweep(t *testing.T) {
	s := queueFixture()
	for i := int64(1); i <= 50; i++ {
		s.enqueue(&Job{Kind: "movie", MovieID: i, Title: "swept", Priority: PrioSweep})
	}
	imp := s.enqueue(&Job{Kind: "episode", SeriesID: 9, Season: 1, Episode: 1, Title: "new", Priority: PrioImport})
	man := s.enqueue(&Job{Kind: "movie", MovieID: 99, Title: "button", Priority: PrioManual})
	if got := s.pop(); got != imp {
		t.Fatalf("first pop = %+v, want the imported episode", got)
	}
	if got := s.pop(); got != man {
		t.Fatalf("second pop = %+v, want the manual job", got)
	}
	if got := s.pop(); got == nil || got.MovieID != 1 {
		t.Fatalf("third pop = %+v, want the oldest sweep job", got)
	}
}

// Within one line the order stays first-come, first-served.
func TestPopFIFOWithinPriority(t *testing.T) {
	s := queueFixture()
	for i := int64(1); i <= 3; i++ {
		s.enqueue(&Job{Kind: "movie", MovieID: i, Title: "m", Priority: PrioManual})
	}
	for want := int64(1); want <= 3; want++ {
		if got := s.pop(); got == nil || got.MovieID != want {
			t.Fatalf("pop = %+v, want movie %d", got, want)
		}
	}
}

// Ensure on a file the sweep already queued moves the same job up to the manual line —
// no duplicate, and it jumps the remaining sweep backlog.
func TestEnqueueBumpsPriority(t *testing.T) {
	s := queueFixture()
	s.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "a", Priority: PrioSweep})
	swept := s.enqueue(&Job{Kind: "movie", MovieID: 2, Title: "b", Priority: PrioSweep})
	again := s.enqueue(&Job{Kind: "movie", MovieID: 2, Title: "b", Priority: PrioManual})
	if again != swept {
		t.Fatal("Ensure on a swept file created a second job")
	}
	if swept.Priority != PrioManual {
		t.Errorf("priority = %d, want %d (manual)", swept.Priority, PrioManual)
	}
	if s.Pending() != 2 {
		t.Errorf("pending = %d, want 2", s.Pending())
	}
	if got := s.pop(); got != swept {
		t.Errorf("first pop = %+v, want the bumped job", got)
	}
	// A weaker request never demotes a job.
	imp := s.enqueue(&Job{Kind: "movie", MovieID: 3, Title: "c", Priority: PrioImport})
	s.enqueue(&Job{Kind: "movie", MovieID: 3, Title: "c", Priority: PrioSweep})
	if imp.Priority != PrioImport {
		t.Errorf("sweep demoted an import job to %d", imp.Priority)
	}
	// The stronger Redo intent still carries over alongside the bump.
	s.enqueue(&Job{Kind: "movie", MovieID: 4, Title: "d", Priority: PrioSweep})
	redo := s.enqueue(&Job{Kind: "movie", MovieID: 4, Title: "d", Priority: PrioManual, Redo: true})
	if !redo.Redo || redo.Priority != PrioManual {
		t.Errorf("redo=%v priority=%d, want redo and manual", redo.Redo, redo.Priority)
	}
}

// Clear queue empties every line, and Pending counts them all.
func TestClearQueueClearsAllPriorities(t *testing.T) {
	s := queueFixture()
	s.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "a", Priority: PrioImport})
	s.enqueue(&Job{Kind: "movie", MovieID: 2, Title: "b", Priority: PrioManual})
	s.enqueue(&Job{Kind: "movie", MovieID: 3, Title: "c", Priority: PrioSweep})
	s.enqueue(&Job{Kind: "movie", MovieID: 4, Title: "d", Priority: PrioSweep})
	if s.Pending() != 4 {
		t.Fatalf("pending = %d, want 4", s.Pending())
	}
	if n := s.ClearQueue(); n != 4 {
		t.Errorf("ClearQueue = %d, want 4", n)
	}
	if s.Pending() != 0 || s.pop() != nil {
		t.Errorf("queue not empty after clear (pending=%d)", s.Pending())
	}
	// Cleared files can be queued again.
	if j := s.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "a", Priority: PrioManual}); j.State != StateQueued {
		t.Errorf("re-queue after clear: state %q", j.State)
	}
}

// Cancelling a queued job takes it out of its own line, whichever that is.
func TestCancelQueuedJobInAnyLine(t *testing.T) {
	s := queueFixture()
	a := s.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "a", Priority: PrioSweep})
	b := s.enqueue(&Job{Kind: "movie", MovieID: 2, Title: "b", Priority: PrioImport})
	if err := s.Cancel(b.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.pop(); got != a {
		t.Errorf("pop = %+v, want the sweep job once the import was cancelled", got)
	}
}
