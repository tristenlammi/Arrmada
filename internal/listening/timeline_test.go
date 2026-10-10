package listening

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Every place an app sends that isn't used, every hold and every removal is kept on the
// place's timeline, and can be put back.

// kinds counts the timeline rows of each kind.
func kinds(hist []HistoryEntry) map[string]int {
	out := map[string]int{}
	for _, h := range hist {
		out[h.Kind]++
	}
	return out
}

// The plane scenario: an hour into a book, the phone goes offline and listens to 4:02:11.
// Before it uploads, the book is briefly opened on a tablet, which moves the place on a
// little. The phone's upload is older than that, so it isn't used — but it's kept and
// offered, and using it puts the place at 4:02:11.
func TestRejectedUploadIsRecordedAndUsable(t *testing.T) {
	s, _, uid, clock := testStore(t)
	ctx := context.Background()
	phone, _, _ := s.OpenSession(ctx, uid, "b1", "p", "Pixel", "Lissen")
	*clock = clock.Add(30 * time.Second)
	if _, err := s.Sync(ctx, uid, phone.ID, at(3600), 30, 36000, true); err != nil {
		t.Fatal(err)
	}
	offlineFrom := *clock
	*clock = clock.Add(3 * time.Hour) // listening offline on the plane
	offlineTo := *clock
	*clock = clock.Add(30 * time.Minute)
	tablet, _, _ := s.OpenSession(ctx, uid, "b1", "t", "iPad", "Lissen")
	*clock = clock.Add(10 * time.Second)
	if _, err := s.Sync(ctx, uid, tablet.ID, at(3610), 10, 36000, true); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(30 * time.Minute)
	d, err := s.SyncOffline(ctx, uid, OfflineSession{ID: "plane", ItemKey: "b1", Device: "Pixel", Client: "Lissen",
		StartTime: 3600, Position: 14531, Duration: 36000, Listened: 10800,
		StartedAt: offlineFrom.UnixMilli(), UpdatedAt: offlineTo.UnixMilli()})
	if err != nil || d.Reason != "older" {
		t.Fatalf("upload = %s %v, want older", d.Reason, err)
	}
	if p, _, _ := s.Progress(ctx, uid, "b1"); p.Position != 3610 {
		t.Fatalf("place = %v, want the tablet's 3610", p.Position)
	}
	// Uploading the same session again doesn't add a second row.
	if _, err := s.SyncOffline(ctx, uid, OfflineSession{ID: "plane", ItemKey: "b1", Device: "Pixel", Client: "Lissen",
		StartTime: 3600, Position: 14531, Duration: 36000, Listened: 10800,
		StartedAt: offlineFrom.UnixMilli(), UpdatedAt: offlineTo.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	hist, _ := s.History(ctx, uid, "b1")
	if kinds(hist)[KindRejected] != 1 {
		t.Fatalf("timeline = %+v, want one rejected row", hist)
	}
	offers, err := s.Offers(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	o, ok := offers["b1"]
	if !ok || o.Position != 14531 || o.Device != "Pixel" || o.Reason != "older" {
		t.Fatalf("offers = %+v, want the phone's 4:02:11", offers)
	}
	if _, err := s.Restore(ctx, uid, "b1", o.ID); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := s.Progress(ctx, uid, "b1"); p.Position != 14531 || p.Finished {
		t.Fatalf("after using the offer: %+v, want 14531", p)
	}
	if offers, _ := s.Offers(ctx, uid); len(offers) != 0 {
		t.Fatalf("the used offer is still offered: %+v", offers)
	}
	hist, _ = s.History(ctx, uid, "b1")
	var restored, before bool
	for _, h := range hist {
		switch {
		case h.Kind == KindRejected && !h.Dismissed:
			t.Fatalf("the used offer isn't marked dismissed: %+v", h)
		case h.Kind == KindRestored && h.Position == 14531:
			restored = true
		case h.Kind == KindBefore && h.Position == 3610:
			before = true
		}
	}
	if !restored {
		t.Fatalf("timeline = %+v, want a restored row at 14531", hist)
	}
	_ = before // the tablet's spot may already be the newest place row (then no before row is needed)
	// The tablet's spot is on the timeline either way, so the restore can be undone.
	found := false
	for _, h := range hist {
		if h.Position == 3610 && (h.Kind == KindApplied || h.Kind == KindBefore) {
			found = true
		}
	}
	if !found {
		t.Fatalf("timeline = %+v, want the tablet's 3610 to go back to", hist)
	}
}

// A spot that isn't later than the place, or was said no to, isn't offered.
func TestOffersOnlyLaterUndismissedSpots(t *testing.T) {
	s, _, uid, clock := testStore(t)
	ctx := context.Background()
	if _, err := s.SetProgress(ctx, uid, "b1", 5000, 36000, nil, "web"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Minute)
	stale := clock.Add(-10 * time.Minute).UnixMilli()
	for i, pos := range []float64{5030, 9000} {
		if _, err := s.ReportPosition(ctx, uid, "b1", pos, 36000, false, stale+int64(i), "app"); err != nil {
			t.Fatal(err)
		}
	}
	offers, _ := s.Offers(ctx, uid)
	if offers["b1"].Position != 9000 {
		t.Fatalf("offers = %+v, want only the 9000 one", offers)
	}
	if err := s.Dismiss(ctx, uid, "b1", offers["b1"].ID); err != nil {
		t.Fatal(err)
	}
	if offers, _ := s.Offers(ctx, uid); len(offers) != 0 {
		t.Fatalf("a dismissed spot is still offered: %+v", offers)
	}
	if err := s.Dismiss(ctx, uid+1, "b1", 1); !errors.Is(err, ErrNoHistory) {
		t.Fatalf("dismissing someone else's row = %v, want ErrNoHistory", err)
	}
}

// An app's "discard progress" hides the place from apps, but it can be put back exactly.
func TestDiscardIsSoftAndUndoable(t *testing.T) {
	s, db, uid, clock := testStore(t)
	ctx := context.Background()
	if _, err := s.SetProgress(ctx, uid, "b1", 2000, 36000, nil, "web"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Minute)
	if err := s.DeleteProgress(ctx, uid, "b1", "Lissen"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.Progress(ctx, uid, "b1"); found {
		t.Fatal("a removed place is still visible")
	}
	if all, _ := s.AllProgress(ctx, uid); len(all) != 0 {
		t.Fatalf("AllProgress lists a removed place: %+v", all)
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM listen_progress WHERE user_id = ?`, uid).Scan(&rows)
	if rows != 1 {
		t.Fatalf("the removed place was hard-deleted (%d rows)", rows)
	}
	removed, err := s.Discarded(ctx, uid)
	if err != nil || len(removed) != 1 || removed[0].Position != 2000 || removed[0].By != "Lissen" || removed[0].DiscardedAt != clock.UnixMilli() {
		t.Fatalf("Discarded = %+v %v", removed, err)
	}
	// Removing it again (an app retrying) changes nothing.
	if err := s.DeleteProgress(ctx, uid, "b1", "Lissen"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Minute)
	if err := s.Undiscard(ctx, uid, "b1"); err != nil {
		t.Fatal(err)
	}
	p, found, _ := s.Progress(ctx, uid, "b1")
	if !found || p.Position != 2000 || p.DiscardedAt != 0 {
		t.Fatalf("after undiscard: %+v %v, want 2000 back", p, found)
	}
	if removed, _ := s.Discarded(ctx, uid); len(removed) != 0 {
		t.Fatalf("still listed as removed: %+v", removed)
	}
	if err := s.Undiscard(ctx, uid, "b1"); !errors.Is(err, ErrNothingToRestore) {
		t.Fatalf("undiscard of a live place = %v, want ErrNothingToRestore", err)
	}
	hist, _ := s.History(ctx, uid, "b1")
	if k := kinds(hist); k[KindDiscarded] != 1 || k[KindRestored] != 1 {
		t.Fatalf("timeline = %+v, want one discarded and one restored row", hist)
	}
}

// After a removal, a new play starts from the beginning and is a fresh place; the old
// one is still on the timeline.
func TestNewReportAfterDiscardStartsFresh(t *testing.T) {
	s, _, uid, clock := testStore(t)
	ctx := context.Background()
	if _, err := s.SetProgress(ctx, uid, "b1", 2000, 36000, nil, "web"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Minute)
	if err := s.DeleteProgress(ctx, uid, "b1", "Lissen"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Minute)
	sess, _, err := s.OpenSession(ctx, uid, "b1", "d", "Pixel", "Lissen")
	if err != nil || sess.StartPos != 0 {
		t.Fatalf("play after a removal = %+v %v, want it to start at 0", sess, err)
	}
	*clock = clock.Add(15 * time.Second)
	if _, err := s.Sync(ctx, uid, sess.ID, at(15), 15, 36000, false); err != nil {
		t.Fatal(err)
	}
	p, found, _ := s.Progress(ctx, uid, "b1")
	if !found || p.Position != 15 || p.DiscardedAt != 0 {
		t.Fatalf("fresh place = %+v %v, want 15", p, found)
	}
	if removed, _ := s.Discarded(ctx, uid); len(removed) != 0 {
		t.Fatalf("the fresh place is listed as removed: %+v", removed)
	}
	hist, _ := s.History(ctx, uid, "b1")
	var old int64
	for _, h := range hist {
		if h.Kind == KindDiscarded && h.Position == 2000 {
			old = h.ID
		}
	}
	if old == 0 {
		t.Fatalf("timeline = %+v, want the removed 2000 on it", hist)
	}
	if _, err := s.Restore(ctx, uid, "b1", old); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := s.Progress(ctx, uid, "b1"); p.Position != 2000 {
		t.Fatalf("going back to the removed place gave %v", p.Position)
	}
}

// Listening from before the removal (an offline upload, a stale PATCH) doesn't bring the
// removed place back.
func TestStaleReportAfterDiscardDoesNotResurrect(t *testing.T) {
	s, _, uid, clock := testStore(t)
	ctx := context.Background()
	if _, err := s.SetProgress(ctx, uid, "b1", 2000, 36000, nil, "web"); err != nil {
		t.Fatal(err)
	}
	listenedAt := clock.Add(10 * time.Minute)
	*clock = clock.Add(time.Hour)
	if err := s.DeleteProgress(ctx, uid, "b1", "Lissen"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Minute)
	d, err := s.SyncOffline(ctx, uid, OfflineSession{ID: "late", ItemKey: "b1", Device: "Pixel", StartTime: 2000, Position: 2500,
		Duration: 36000, Listened: 500, StartedAt: listenedAt.Add(-9 * time.Minute).UnixMilli(), UpdatedAt: listenedAt.UnixMilli()})
	if err != nil || d.Reason != "older" {
		t.Fatalf("upload from before the removal = %s %v, want older", d.Reason, err)
	}
	if d, err := s.ReportPosition(ctx, uid, "b1", 2600, 36000, false, listenedAt.UnixMilli(), "app"); err != nil || d.Reason != "older" {
		t.Fatalf("stale PATCH = %s %v, want older", d.Reason, err)
	}
	if _, found, _ := s.Progress(ctx, uid, "b1"); found {
		t.Fatal("the removed place came back")
	}
	if removed, _ := s.Discarded(ctx, uid); len(removed) != 1 || removed[0].Position != 2000 {
		t.Fatalf("Discarded = %+v, want the 2000 still restorable", removed)
	}
	hist, _ := s.History(ctx, uid, "b1")
	for _, h := range hist {
		if h.Kind == KindRejected && !h.Dismissed {
			t.Fatalf("a spot from before the removal would be offered: %+v", h)
		}
	}
	if k := kinds(hist); k[KindRejected] != 2 {
		t.Fatalf("timeline = %+v, want both stale reports on it", hist)
	}
}

// A removed place is restorable for RemovedRetention, then pruned.
func TestPruneDropsOldDiscards(t *testing.T) {
	s, db, uid, clock := testStore(t)
	ctx := context.Background()
	for _, key := range []string{"b1", "b2"} {
		if _, err := s.SetProgress(ctx, uid, key, 2000, 36000, nil, "web"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteProgress(ctx, uid, "b1", "Lissen"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(RemovedRetention - time.Hour)
	if err := s.DeleteProgress(ctx, uid, "b2", "Lissen"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(2 * time.Hour)
	if err := s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	var keys []string
	rows, _ := db.Query(`SELECT item_key FROM listen_progress WHERE user_id = ?`, uid)
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		keys = append(keys, k)
	}
	rows.Close()
	if len(keys) != 1 || keys[0] != "b2" {
		t.Fatalf("after prune: %v, want only the recently removed b2", keys)
	}
	if removed, _ := s.Discarded(ctx, uid); len(removed) != 1 || removed[0].ItemKey != "b2" {
		t.Fatalf("Discarded = %+v", removed)
	}
}

// The timeline is bounded per kind: a flood of stale reports can't push out the places
// the book actually moved through.
func TestHistoryPrunedPerKind(t *testing.T) {
	s, _, uid, clock := testStore(t)
	ctx := context.Background()
	for i := 0; i < 60; i++ {
		*clock = clock.Add(time.Minute)
		if _, err := s.SetProgress(ctx, uid, "b1", float64(1000+i*300), 36000, nil, "web"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 40; i++ {
		*clock = clock.Add(time.Minute)
		stale := clock.Add(-time.Hour).UnixMilli()
		if _, err := s.ReportPosition(ctx, uid, "b1", float64(30000+i), 36000, false, stale, "app"); err != nil {
			t.Fatal(err)
		}
	}
	hist, _ := s.History(ctx, uid, "b1")
	k := kinds(hist)
	if k[KindApplied]+k[KindBefore] != historyKeep {
		t.Fatalf("place rows = %d, want %d (%v)", k[KindApplied]+k[KindBefore], historyKeep, k)
	}
	if k[KindRejected] != historyKeepOther {
		t.Fatalf("rejected rows = %d, want %d", k[KindRejected], historyKeepOther)
	}
}

// Routine listening is written to the timeline only every few minutes, so a rewind soon
// after used to leave the spot just before it unrecorded.
func TestNonRoutineChangeKeepsThePlaceBefore(t *testing.T) {
	s, _, uid, clock := testStore(t)
	ctx := context.Background()
	sess, _, _ := s.OpenSession(ctx, uid, "b1", "d", "Pixel", "Lissen")
	for _, pos := range []float64{3000, 3060, 3120} {
		*clock = clock.Add(60 * time.Second)
		if _, err := s.Sync(ctx, uid, sess.ID, at(pos), 60, 36000, false); err != nil {
			t.Fatal(err)
		}
	}
	hist, _ := s.History(ctx, uid, "b1")
	for _, h := range hist {
		if h.Position == 3120 {
			t.Fatalf("routine listening within the gap was written: %+v", hist)
		}
	}
	*clock = clock.Add(10 * time.Second)
	if _, err := s.SetProgress(ctx, uid, "b1", 100, 36000, nil, "web"); err != nil {
		t.Fatal(err)
	}
	hist, _ = s.History(ctx, uid, "b1")
	var before *HistoryEntry
	for i := range hist {
		if hist[i].Kind == KindBefore {
			before = &hist[i]
		}
	}
	if before == nil || before.Position != 3120 || before.Device != "Pixel" {
		t.Fatalf("timeline = %+v, want a before row at 3120 from the Pixel", hist)
	}
	// A hold is recorded when it starts.
	*clock = clock.Add(10 * time.Second)
	if _, err := s.Sync(ctx, uid, sess.ID, at(20000), 10, 36000, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReportPosition(ctx, uid, "b1", 0, 36000, false, 0, "app"); err != nil {
		t.Fatal(err)
	}
	hist, _ = s.History(ctx, uid, "b1")
	if k := kinds(hist); k[KindHeld] != 1 {
		t.Fatalf("timeline = %+v, want one held row", hist)
	}
}
