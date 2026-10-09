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

// A position an app sets without playing (a progress PATCH) from a stale copy can't
// replace a newer place.
func TestReportedOlderIsIgnored(t *testing.T) {
	p := Progress{Position: 9000, Duration: 40000, UpdatedAt: 5000}
	for _, at := range []int64{4000, 5000} {
		d := Decide(&p, Report{Kind: Reported, Position: 9500, At: at})
		if d.Dirty || d.Reason != "older" || d.Progress.Position != 9000 {
			t.Fatalf("older report at %d: %+v", at, d)
		}
	}
	no := false
	finished := Progress{Position: 40000, Duration: 40000, Finished: true, UpdatedAt: 5000}
	if d := Decide(&finished, Report{Kind: Reported, Position: 3000, Finished: &no, At: 4000}); d.Dirty || !d.Progress.Finished {
		t.Fatalf("a stale copy un-finished the book: %+v", d)
	}
}

// A big jump back set without playing is held, with no session, never saved.
func TestReportedBigBackIsHeld(t *testing.T) {
	p := Progress{Position: 13629, Duration: 40000, UpdatedAt: 1000}
	d := Decide(&p, Report{Kind: Reported, Position: 5, At: 2000})
	if d.Changed || d.Reason != "held" || d.Progress.Position != 13629 {
		t.Fatalf("reported reset moved the place: %s %+v", d.Reason, d.Progress)
	}
	if d.Progress.PendingPosition == nil || *d.Progress.PendingPosition != 5 || d.Progress.PendingSession != "" || d.Progress.PendingAt != 2000 {
		t.Fatalf("hold = %+v, want 5 s held with no session", d.Progress)
	}
	// A small skip back is normal and saved.
	if d := Decide(&p, Report{Kind: Reported, Position: 13529, At: 2000}); d.Reason != "set" || d.Progress.Position != 13529 {
		t.Fatalf("a 100 s skip back: %s %v", d.Reason, d.Progress.Position)
	}
}

// A play session that carries on from a held jump an app set takes the hold over, and
// 30 s of listening from there saves it. A session elsewhere starts its own hold.
func TestReportedHoldAdoptedByContinuousLiveSession(t *testing.T) {
	p := Progress{Position: 5000, Duration: 40000, UpdatedAt: 1000}
	held := Decide(&p, Report{Kind: Reported, Position: 100, At: 2000}).Progress

	d := Decide(&held, Report{Kind: Live, Position: 110, Listened: 10, At: 3000, SessionID: "s"})
	if d.Reason != "held" || d.Progress.PendingSession != "s" || d.Progress.PendingListened != 0 || d.Progress.Position != 5000 {
		t.Fatalf("adoption: %s %+v", d.Reason, d.Progress)
	}
	d = Decide(&d.Progress, Report{Kind: Live, Position: 125, Listened: 15, At: 4000, SessionID: "s"})
	if d.Reason != "held" || d.Progress.Position != 5000 {
		t.Fatalf("15 s after adoption: %s %+v", d.Reason, d.Progress)
	}
	d = Decide(&d.Progress, Report{Kind: Live, Position: 140, Listened: 15, At: 5000, SessionID: "s"})
	if d.Reason != "rewind" || d.Progress.Position != 140 || d.Progress.PendingPosition != nil {
		t.Fatalf("30 s after adoption: %s %+v", d.Reason, d.Progress)
	}

	// Playing somewhere unrelated, far from the held spot, doesn't adopt it.
	d = Decide(&held, Report{Kind: Live, Position: 2000, Listened: 10, At: 3000, SessionID: "t"})
	if d.Reason != "held" || d.Progress.PendingSession != "t" || *d.Progress.PendingPosition != 2000 {
		t.Fatalf("an unrelated session: %s %+v", d.Reason, d.Progress)
	}
	// Carrying on from the saved place drops the hold.
	d = Decide(&held, Report{Kind: Live, Position: 5010, Listened: 10, At: 3000, SessionID: "u"})
	if d.Progress.Position != 5010 || d.Progress.PendingPosition != nil {
		t.Fatalf("playing on from the saved place: %s %+v", d.Reason, d.Progress)
	}
}

// Forward, set without playing, is saved at once; so is marking a finished book not
// finished — but a routine "not finished" on an unfinished book is just a position.
func TestReportedForwardApplies(t *testing.T) {
	p := Progress{Position: 100, Duration: 1000, UpdatedAt: 1}
	if d := Decide(&p, Report{Kind: Reported, Position: 700, At: 2}); d.Reason != "set" || d.Progress.Position != 700 || d.Progress.UpdatedAt != 2 {
		t.Fatalf("forward: %s %+v", d.Reason, d.Progress)
	}
	no := false
	finished := Progress{Position: 1000, Duration: 1000, Finished: true, FinishedAt: 1, UpdatedAt: 1}
	d := Decide(&finished, Report{Kind: Reported, Position: 1000, Finished: &no, At: 2})
	if d.Progress.Finished || d.Progress.Position != 1000 || d.Reason != "manual" {
		t.Fatalf("un-finish keeping the place: %s %+v", d.Reason, d.Progress)
	}
	// Reaching the end still marks it finished, whatever the routine flag says.
	if d := Decide(&p, Report{Kind: Reported, Position: 998, Finished: &no, At: 2}); !d.Progress.Finished {
		t.Fatalf("a report at the end with isFinished:false: %+v", d.Progress)
	}
}
