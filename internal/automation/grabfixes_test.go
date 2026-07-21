package automation

import (
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
)

// TestBestByTitle pins the duplicate-release fix: when the same scene release is
// listed by several indexers, one copy survives — the one with the most seeders —
// so the release the quality engine scores is the release a byName lookup returns.
func TestBestByTitle(t *testing.T) {
	in := []indexer.Release{
		{Title: "Movie.2024.1080p.WEB-DL-GRP", Indexer: "slow", Seeders: 2},
		{Title: "Movie.2024.1080p.WEB-DL-GRP", Indexer: "fast", Seeders: 500},
		{Title: "Movie.2024.2160p.WEB-DL-GRP", Indexer: "slow", Seeders: 9},
	}
	out := bestByTitle(in)
	if len(out) != 2 {
		t.Fatalf("want 2 releases after dedup, got %d", len(out))
	}
	if out[0].Indexer != "fast" || out[0].Seeders != 500 {
		t.Errorf("kept the wrong copy: %+v", out[0])
	}
	if out[1].Title != "Movie.2024.2160p.WEB-DL-GRP" {
		t.Errorf("unique title dropped: %+v", out[1])
	}
}

// TestGrabbableFiltersUsenet pins the transport guard: with only a torrent client
// configured, usenet releases must never become grab candidates.
func TestGrabbableFiltersUsenet(t *testing.T) {
	in := []indexer.Release{
		{Title: "A", Transport: indexer.TransportTorrent},
		{Title: "B", Transport: indexer.TransportUsenet},
		{Title: "C"}, // unset defaults to torrent semantics elsewhere; must survive
	}
	out := grabbable(in)
	if len(out) != 2 || out[0].Title != "A" || out[1].Title != "C" {
		t.Fatalf("want [A C], got %+v", out)
	}
}

// TestStalledNeedsSustainedNoProgress pins the stall rewrite: a download that is old
// but still moving is NOT stalled; "stalled" requires no forward progress for the
// whole window (or a hard client error / disappearance from a healthy queue).
func TestStalledNeedsSustainedNoProgress(t *testing.T) {
	c := &Coordinator{}
	g := grab{ID: 1}
	window := time.Minute

	// Gone from a successfully-read queue → stalled.
	if !c.stalledInQueue(g, download.Item{}, false, window) {
		t.Error("missing from queue must count as stalled")
	}
	// Hard error states → stalled.
	if !c.stalledInQueue(g, download.Item{State: "error", Progress: 0.5}, true, window) {
		t.Error("state=error must count as stalled")
	}
	// First observation with zero speed → NOT stalled (this was the bug: one
	// instantaneous zero-speed sample condemned a working download).
	if c.stalledInQueue(g, download.Item{State: "stalledDL", Progress: 0.5, DownSpeed: 0}, true, window) {
		t.Error("first zero-progress observation must not count as stalled")
	}
	// Progress advanced since the sample → clock restarts, still not stalled.
	if c.stalledInQueue(g, download.Item{Progress: 0.6}, true, window) {
		t.Error("forward progress must reset the stall clock")
	}
	// Same progress, but window not yet elapsed → not stalled.
	if c.stalledInQueue(g, download.Item{Progress: 0.6}, true, window) {
		t.Error("window not elapsed — must not be stalled yet")
	}
	// Backdate the sample past the window: same progress now IS stalled.
	c.stallMu.Lock()
	c.stallProgress[1] = stallSample{progress: 0.6, at: time.Now().Add(-2 * window)}
	c.stallMu.Unlock()
	if !c.stalledInQueue(g, download.Item{Progress: 0.6}, true, window) {
		t.Error("no progress for a full window must count as stalled")
	}
	// Completed download (progress 1.0) is never stalled regardless of samples.
	if c.stalledInQueue(g, download.Item{Progress: 1.0}, true, window) {
		t.Error("a finished download can't be stalled")
	}
}

// TestPruneStallSamples pins the cleanup: samples for grabs that left the pending
// set are dropped.
func TestPruneStallSamples(t *testing.T) {
	c := &Coordinator{}
	c.noProgressFor(1, 0.1, time.Minute)
	c.noProgressFor(2, 0.2, time.Minute)
	c.pruneStallSamples([]grab{{ID: 2}})
	c.stallMu.Lock()
	defer c.stallMu.Unlock()
	if _, ok := c.stallProgress[1]; ok {
		t.Error("sample for resolved grab 1 should be pruned")
	}
	if _, ok := c.stallProgress[2]; !ok {
		t.Error("sample for still-pending grab 2 should survive")
	}
}

// TestPruneUnmatched pins the unmatched-counter cleanup.
func TestPruneUnmatched(t *testing.T) {
	c := &Coordinator{}
	c.noteUnmatched("aaa")
	c.noteUnmatched("bbb")
	c.pruneUnmatched(map[string]bool{"bbb": true})
	c.unmatchedMu.Lock()
	defer c.unmatchedMu.Unlock()
	if _, ok := c.unmatched["aaa"]; ok {
		t.Error("counter for a gone download should be pruned")
	}
	if c.unmatched["bbb"] != 1 {
		t.Error("counter for a live download should survive")
	}
}
