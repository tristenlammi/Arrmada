package listening

import "testing"

// Replays of the ways Audiobookshelf loses a place. Each must leave the place intact.

// A single stray report near the start (Audiobookshelf #5188: 13,629 s became 5 s) is
// held, never saved.
func TestStrayResetIsHeldNotSaved(t *testing.T) {
	cur := &Progress{Position: 13629, Duration: 40000, UpdatedAt: 1000}
	d := Decide(cur, Report{Kind: Live, Position: 5, Listened: 3, At: 2000, SessionID: "s2"})
	if d.Changed || d.Progress.Position != 13629 {
		t.Fatalf("stray reset moved the place to %v (%s)", d.Progress.Position, d.Reason)
	}
	if d.Reason != "held" || d.Progress.PendingPosition == nil {
		t.Fatalf("expected the jump to be held, got %s", d.Reason)
	}
	// The glitching app then reports the real place again: the hold is dropped.
	d2 := Decide(&d.Progress, Report{Kind: Live, Position: 13640, Listened: 10, At: 3000, SessionID: "s2"})
	if d2.Progress.Position != 13640 || d2.Progress.PendingPosition != nil {
		t.Fatalf("real place not restored cleanly: %+v", d2.Progress)
	}
}

// A deliberate restart is saved once the listener carries on for a minute from there.
func TestDeliberateRewindIsSavedAfterProof(t *testing.T) {
	p := Progress{Position: 5000, Duration: 40000, UpdatedAt: 1000}
	d := Decide(&p, Report{Kind: Live, Position: 0, Listened: 2, At: 2000, SessionID: "s"})
	pos := 0.0
	for i := 1; i <= 5 && d.Reason != "rewind"; i++ {
		pos += 15
		d = Decide(&d.Progress, Report{Kind: Live, Position: pos, Listened: 15, At: int64(2000 + i*15000), SessionID: "s"})
	}
	if d.Reason != "rewind" || d.Progress.Position > 100 || d.Progress.PendingPosition != nil {
		t.Fatalf("deliberate restart not saved after a minute of listening: %s %+v", d.Reason, d.Progress)
	}
}

// A different session can't finish another session's held jump.
func TestHeldJumpNeedsTheSameSession(t *testing.T) {
	p := Progress{Position: 5000, Duration: 40000, UpdatedAt: 1000}
	d := Decide(&p, Report{Kind: Live, Position: 100, At: 2000, SessionID: "a"})
	d = Decide(&d.Progress, Report{Kind: Live, Position: 200, Listened: 90, At: 3000, SessionID: "b"})
	if d.Reason != "held" || d.Progress.Position != 5000 {
		t.Fatalf("another session proved the jump: %s %v", d.Reason, d.Progress.Position)
	}
}

// Old offline listening uploaded later can't drag a newer place back.
func TestOlderOfflineUploadDoesNotOverwrite(t *testing.T) {
	p := Progress{Position: 9000, Duration: 40000, UpdatedAt: 5000}
	d := Decide(&p, Report{Kind: Offline, Position: 3000, Listened: 1200, At: 4000, SessionID: "off"})
	if d.Dirty || d.Progress.Position != 9000 {
		t.Fatalf("older offline session overwrote the place: %+v", d)
	}
	// Newer offline listening that moved forward is applied.
	d = Decide(&p, Report{Kind: Offline, Position: 9900, Listened: 900, At: 6000, SessionID: "off2"})
	if d.Progress.Position != 9900 {
		t.Fatalf("newer offline progress not applied: %v", d.Progress.Position)
	}
}

// Forward is always saved at once; small skips back are normal and saved too.
func TestForwardAndSmallBack(t *testing.T) {
	p := Progress{Position: 100, Duration: 1000, UpdatedAt: 1}
	if d := Decide(&p, Report{Kind: Live, Position: 700, At: 2}); d.Progress.Position != 700 {
		t.Fatal("forward skip not saved")
	}
	if d := Decide(&p, Report{Kind: Live, Position: 40, At: 2}); d.Progress.Position != 40 {
		t.Fatal("a one-minute skip back not saved")
	}
}

// Reaching the end marks finished; un-finishing, clamping and mark-finished all work.
func TestFinishedAndManual(t *testing.T) {
	p := Progress{Position: 990, Duration: 1000, UpdatedAt: 1}
	d := Decide(&p, Report{Kind: Live, Position: 998, At: 2})
	if !d.Progress.Finished {
		t.Fatal("reaching the end should mark finished")
	}
	no := false
	d = Decide(&d.Progress, Report{Kind: Manual, Position: 0, Finished: &no, At: 3})
	if d.Progress.Finished || d.Progress.Position != 0 {
		t.Fatalf("manual un-finish to the start: %+v", d.Progress)
	}
	d = Decide(&p, Report{Kind: Manual, Position: 5000, At: 3})
	if d.Progress.Position != 1000 {
		t.Fatalf("position not clamped to the duration: %v", d.Progress.Position)
	}
	yes := true
	d = Decide(&p, Report{Kind: Manual, Position: 10, Finished: &yes, At: 4})
	if !d.Progress.Finished || d.Progress.Position != 1000 {
		t.Fatalf("mark finished should move to the end: %+v", d.Progress)
	}
}

// Nothing saved yet: the first report starts the place, whatever it is.
func TestFirstReport(t *testing.T) {
	d := Decide(nil, Report{Kind: Live, Position: 42, Duration: 100, At: 1})
	if !d.Changed || d.Progress.Position != 42 {
		t.Fatalf("first report: %+v", d)
	}
}
