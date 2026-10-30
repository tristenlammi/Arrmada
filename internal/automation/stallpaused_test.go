package automation

import (
	"strings"
	"testing"
)

// A show with searching off ("Turn searching off") is skipped by the sweep, and its
// downloads already in flight must not be failed over either: a stalled one is left in
// the client to wait, a vanished one is closed with nothing grabbed in its place. With
// searching on, the same downloads fail over as usual.

const (
	pausedStalled = "Show.S01E01.1080p.WEB-DL.x264-OLD"
	pausedAlt     = "Show.S01E01.1080p.WEB-DL.x264-NEW"
)

// pausedShow is addShow (nothing on disk) with searching turned off unless searching.
func (h *stallHarness) pausedShow(t *testing.T, searching bool) int64 {
	t.Helper()
	sid := h.addShow(t, false)
	if !searching {
		if err := h.c.series.SetMonitored(h.ctx, sid, false); err != nil {
			t.Fatal(err)
		}
	}
	return sid
}

// seriesHistoryHas reports whether a "failed" line in the show's history contains substr.
func (h *stallHarness) seriesHistoryHas(t *testing.T, id int64, substr string) bool {
	t.Helper()
	evs, err := h.c.series.Events(h.ctx, id, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Event == "failed" && strings.Contains(e.Detail, substr) {
			return true
		}
	}
	return false
}

func TestStallFailoverSkipsAPausedShow(t *testing.T) {
	h := newStallHarness(t)
	sid := h.pausedShow(t, false)
	gid := h.addStalledGrab(t, "series", sid, pausedStalled, seriesCategory)
	h.ix.offer(pausedStalled, pausedAlt)

	h.observe()
	h.tick()

	if calls := h.qbit.callLog(); len(calls) != 0 {
		t.Errorf("client calls = %q, want none (no grab, and the stalled torrent kept)", calls)
	}
	if n := h.ix.searchCount(); n != 0 {
		t.Errorf("searches = %d, want none", n)
	}
	if s := h.status(t, gid); s != grabStatusGrabbed {
		t.Errorf("grab status = %q, want it still waiting (%q)", s, grabStatusGrabbed)
	}
	if n := h.blocklistCount(t); n != 0 {
		t.Errorf("blocklist rows = %d, want none", n)
	}
	if !h.seriesHistoryHas(t, sid, "searching is off for this show — not replaced") {
		t.Error("history should say the download wasn't replaced because searching is off")
	}

	// A window later it is still left alone.
	h.tick()
	if calls := h.qbit.callLog(); len(calls) != 0 {
		t.Errorf("client calls after another window = %q, want none", calls)
	}
}

func TestStallFailoverReplacesForAShowWithSearchingOn(t *testing.T) {
	h := newStallHarness(t)
	sid := h.pausedShow(t, true)
	gid := h.addStalledGrab(t, "series", sid, pausedStalled, seriesCategory)
	h.ix.offer(pausedStalled, pausedAlt)

	h.observe()
	h.tick()

	calls := h.qbit.callLog()
	want := []string{"add " + magnetFor(pausedAlt), "delete " + hashFor(pausedStalled)}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("client calls = %q\nwant          %q", calls, want)
	}
	if s := h.status(t, gid); s != grabStatusFailed {
		t.Errorf("grab status = %q, want failed", s)
	}
}

func TestVanishedGrabOfAPausedShowIsNotReplaced(t *testing.T) {
	h := newStallHarness(t)
	sid := h.pausedShow(t, false)
	gid := h.addGoneGrab(t, "series", sid, pausedStalled, grabStatusGrabbed)
	h.ix.offer(pausedAlt)

	h.goneForTheGrace()

	if s := h.status(t, gid); s != grabStatusFailed {
		t.Errorf("grab status = %q, want failed (closed, nothing to wait on)", s)
	}
	if n := h.ix.searchCount(); n != 0 {
		t.Errorf("searches = %d, want none", n)
	}
	if calls := h.qbit.callLog(); len(calls) != 0 {
		t.Errorf("client calls = %q, want none", calls)
	}
	if !h.seriesHistoryHas(t, sid, "searching is off for this show — not replaced") {
		t.Error("history should say the download wasn't replaced because searching is off")
	}
}

func TestVanishedGrabOfAShowWithSearchingOnIsReplaced(t *testing.T) {
	h := newStallHarness(t)
	sid := h.pausedShow(t, true)
	gid := h.addGoneGrab(t, "series", sid, pausedStalled, grabStatusGrabbed)
	h.ix.offer(pausedAlt)

	h.goneForTheGrace()

	if s := h.status(t, gid); s != grabStatusFailed {
		t.Errorf("grab status = %q, want failed", s)
	}
	calls := h.qbit.callLog()
	if len(calls) != 1 || calls[0] != "add "+magnetFor(pausedAlt) {
		t.Errorf("client calls = %q, want only the replacement added", calls)
	}
	if !h.seriesHistoryHas(t, sid, "searching for another") {
		t.Error("history should say a replacement was searched for")
	}
}
