package listening

import (
	"context"
	"testing"
	"time"
)

// A big jump forward into the last minutes of a book has to prove itself, because
// landing there finishes the book and drops it off Continue Listening.

const tenHours = 36000.0

// One report jumping from 2:00:00 to the last 3 s of a 10 h book is held, not saved.
func TestBigForwardJumpIntoEndIsHeld(t *testing.T) {
	cur := &Progress{Position: 7200, Duration: tenHours, UpdatedAt: 1000}
	d := Decide(cur, Report{Kind: Live, Position: tenHours - 3, Listened: 15, At: 2000, SessionID: "s"})
	if d.Changed || d.Progress.Position != 7200 || d.Progress.Finished {
		t.Fatalf("jump to the end moved the place: %s %+v", d.Reason, d.Progress)
	}
	if d.Reason != "held-forward" || d.Progress.PendingPosition == nil || *d.Progress.PendingPosition != tenHours-3 {
		t.Fatalf("want a forward hold, got %s %+v", d.Reason, d.Progress)
	}
}

// Listening on from the jumped-to spot to the end (the position moving) confirms it.
func TestListeningToTheEndConfirmsForwardJump(t *testing.T) {
	cur := &Progress{Position: 7200, Duration: tenHours, UpdatedAt: 1000}
	d := Decide(cur, Report{Kind: Live, Position: tenHours - 3, Listened: 15, At: 2000, SessionID: "s"})
	d = Decide(&d.Progress, Report{Kind: Live, Position: tenHours, Listened: 3, At: 5000, SessionID: "s"})
	if d.Reason != "forward-proven" || !d.Progress.Finished || d.Progress.Position != tenHours || d.Progress.PendingPosition != nil {
		t.Fatalf("playing on to the end = %s %+v, want finished", d.Reason, d.Progress)
	}
	// And 30 s of listening on from a spot a few minutes before the end proves it too.
	cur = &Progress{Position: 7200, Duration: tenHours, UpdatedAt: 1000}
	d = Decide(cur, Report{Kind: Live, Position: tenHours - 300, Listened: 5, At: 2000, SessionID: "s"})
	for i, pos := range []float64{tenHours - 285, tenHours - 270} {
		d = Decide(&d.Progress, Report{Kind: Live, Position: pos, Listened: 15, At: int64(3000 + i*15000), SessionID: "s"})
	}
	if d.Reason != "forward-proven" || d.Progress.Position != tenHours-270 || d.Progress.Finished {
		t.Fatalf("30 s from the jumped-to spot = %s %+v, want it saved (not finished)", d.Reason, d.Progress)
	}
}

// A player stuck re-sending the full duration never proves itself.
func TestStuckFullDurationStaysHeld(t *testing.T) {
	cur := &Progress{Position: 7200, Duration: tenHours, UpdatedAt: 1000}
	d := Decide(cur, Report{Kind: Live, Position: tenHours, Listened: 15, At: 2000, SessionID: "s"})
	for i := 0; i < 6; i++ {
		d = Decide(&d.Progress, Report{Kind: Live, Position: tenHours, Listened: 15, At: int64(3000 + i*15000), SessionID: "s"})
		if d.Reason != "held-forward" || d.Progress.Position != 7200 || d.Progress.Finished {
			t.Fatalf("report %d of a stuck player = %s %+v, want still held", i+2, d.Reason, d.Progress)
		}
	}
}

// Normal listening, 30 s skips and chapter skips away from the end still save at once,
// and so do small skips inside the end zone.
func TestNormalForwardStillImmediate(t *testing.T) {
	for _, c := range []struct {
		from, to, listened float64
	}{
		{7200, 7215, 15},                    // listening
		{7200, 7230, 0},                     // a 30 s skip
		{7200, 9000, 5},                     // a chapter skip mid-book
		{tenHours - 400, tenHours - 370, 0}, // a 30 s skip near the end
		{tenHours - 20, tenHours, 20},       // listening to the end
	} {
		cur := &Progress{Position: c.from, Duration: tenHours, UpdatedAt: 1000}
		d := Decide(cur, Report{Kind: Live, Position: c.to, Listened: c.listened, At: 2000, SessionID: "s"})
		if d.Reason != "forward" || d.Progress.Position != c.to {
			t.Errorf("%v → %v: %s %+v, want saved at once", c.from, c.to, d.Reason, d.Progress)
		}
	}
}

// An offline upload claiming the full duration after 60 s of listening from mid-book
// isn't used; one that listened its way there is.
func TestOfflineFinishNeedsListening(t *testing.T) {
	cur := &Progress{Position: 7200, Duration: tenHours, UpdatedAt: 1000}
	d := Decide(cur, Report{Kind: Offline, Position: tenHours, From: 7200, Listened: 60, At: 2000, SessionID: "off"})
	if d.Dirty || d.Reason != "unproven" {
		t.Fatalf("offline jump to the end = %s %+v, want unproven", d.Reason, d.Progress)
	}
	// No start position from the app: judged from the saved place.
	d = Decide(cur, Report{Kind: Offline, Position: tenHours, Listened: 60, At: 2000, SessionID: "off"})
	if d.Dirty || d.Reason != "unproven" {
		t.Fatalf("offline jump to the end without a start = %s, want unproven", d.Reason)
	}
	near := &Progress{Position: tenHours - 900, Duration: tenHours, UpdatedAt: 1000}
	d = Decide(near, Report{Kind: Offline, Position: tenHours, From: tenHours - 900, Listened: 900, At: 2000, SessionID: "off"})
	if d.Reason != "offline" || !d.Progress.Finished {
		t.Fatalf("offline listening to the end = %s %+v, want applied and finished", d.Reason, d.Progress)
	}
}

// Marking a book finished is the person's own action and still applies at once.
func TestExplicitFinishStillApplies(t *testing.T) {
	yes := true
	cur := &Progress{Position: 7200, Duration: tenHours, UpdatedAt: 1000}
	d := Decide(cur, Report{Kind: Manual, Position: 7200, Finished: &yes, At: 2000})
	if !d.Progress.Finished || d.Progress.Position != tenHours {
		t.Fatalf("explicit finish = %+v", d.Progress)
	}
}

// A position an app sets near the end without playing is held with no session.
func TestReportedForwardIntoEndIsHeld(t *testing.T) {
	cur := &Progress{Position: 7200, Duration: tenHours, UpdatedAt: 1000}
	d := Decide(cur, Report{Kind: Reported, Position: tenHours - 2, At: 2000})
	if d.Reason != "held-forward" || d.Progress.Position != 7200 || d.Progress.PendingSession != "" || d.Progress.Finished {
		t.Fatalf("reported jump to the end = %s %+v, want held with no session", d.Reason, d.Progress)
	}
	// A play session carrying on from it takes the hold over and proves it at the end.
	d = Decide(&d.Progress, Report{Kind: Live, Position: tenHours - 1, Listened: 1, At: 3000, SessionID: "s"})
	if d.Progress.PendingSession != "s" {
		t.Fatalf("session didn't adopt the hold: %+v", d.Progress)
	}
	d = Decide(&d.Progress, Report{Kind: Live, Position: tenHours, Listened: 1, At: 4000, SessionID: "s"})
	if d.Reason != "forward-proven" || !d.Progress.Finished {
		t.Fatalf("playing on to the end = %s %+v", d.Reason, d.Progress)
	}
	// A forward PATCH outside the end zone still applies at once.
	d = Decide(cur, Report{Kind: Reported, Position: 20000, At: 2000})
	if d.Reason != "set" || d.Progress.Position != 20000 {
		t.Fatalf("reported forward mid-book = %s %+v", d.Reason, d.Progress)
	}
}

// "Use this spot" on a forward hold at the end finishes the book; on a jump back it
// doesn't.
func TestAcceptForwardHoldFinishes(t *testing.T) {
	s, _, uid, clock := testStore(t)
	ctx := context.Background()
	sess, _, _ := s.OpenSession(ctx, uid, "b1", "d", "Pixel", "Lissen")
	*clock = clock.Add(15 * time.Second)
	if _, err := s.Sync(ctx, uid, sess.ID, at(7200), 15, tenHours, false); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(15 * time.Second)
	d, err := s.Sync(ctx, uid, sess.ID, at(tenHours), 15, tenHours, false)
	if err != nil || d.Reason != "held-forward" {
		t.Fatalf("jump to the end = %s %v", d.Reason, err)
	}
	hist, _ := s.History(ctx, uid, "b1")
	if hist[0].Kind != KindHeld || hist[0].Reason != "held-forward" {
		t.Fatalf("timeline = %+v, want the forward hold on it", hist)
	}
	d, err = s.AcceptPending(ctx, uid, "b1")
	if err != nil || !d.Progress.Finished || d.Progress.Position != tenHours {
		t.Fatalf("accepting the forward hold = %+v %v, want finished", d.Progress, err)
	}
	// The place before the finish is on the timeline.
	hist, _ = s.History(ctx, uid, "b1")
	found := false
	for _, h := range hist {
		if h.Position == 7200 && (h.Kind == KindBefore || h.Kind == KindApplied) {
			found = true
		}
	}
	if !found {
		t.Fatalf("timeline = %+v, want 7200 to go back to", hist)
	}
}
