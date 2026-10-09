package automation

import (
	"context"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
)

// A paused torrent makes no progress by definition. Reading that as a stall blocklisted
// the release, deleted the torrent AND its data, and grabbed an alternate that couldn't
// download either — every time a user script paused qBittorrent because the cache drive
// filled up, and each casualty left a hit-and-run on the tracker.
func TestPausedTorrentIsNotStalled(t *testing.T) {
	c := stallCoord(t, 1)
	ctx := context.Background()
	g := grab{ID: 1}
	const window = time.Minute

	paused := download.Item{State: "paused", Progress: 0.4}

	// First observation never condemns anything, so drive it past the window the way the
	// two-minute sweep would: an unpaused torrent frozen at 0.4 must eventually stall.
	frozen := download.Item{State: "downloading", Progress: 0.4}
	c.stalledInQueue(ctx, g, frozen, true, window)
	expireStall(t, c, g.ID, 0.4, 2*window)
	if !c.stalledInQueue(ctx, g, frozen, true, window) {
		t.Fatal("a running torrent frozen past the window must still stall — the check has to keep working")
	}

	// Same elapsed time, same progress, but paused: not a stall, and the clock is held so
	// the pause doesn't accumulate.
	expireStall(t, c, g.ID, 0.4, 2*window)
	if c.stalledInQueue(ctx, g, paused, true, window) {
		t.Error("a paused torrent must never be condemned as stalled")
	}
	if got := stallClockAt(t, c, g.ID); time.Since(got) > time.Second {
		t.Error("the stall clock must be held while paused, not left to expire")
	}

	// Rechecking after a disk-full crash moves no progress either.
	expireStall(t, c, g.ID, 0.4, 2*window)
	if c.stalledInQueue(ctx, g, download.Item{State: "checking", Progress: 0.4}, true, window) {
		t.Error("a torrent being rechecked must not be condemned as stalled")
	}
}

// qBittorrent's checkingDL used to normalize to "downloading", so a long recheck after a
// crash ran the stall window down and the torrent could be failed over mid-check.
func TestRecheckingTorrentHoldsTheStallClock(t *testing.T) {
	c := stallCoord(t, 7)
	ctx := context.Background()
	g := grab{ID: 7}
	const window = time.Minute
	c.holdStallClock(ctx, g.ID, 0.3)
	expireStall(t, c, g.ID, 0.3, 2*window)

	checking := download.Item{RawState: "checkingDL", State: "checking", Progress: 0.3, RemainingBytes: 1}
	if c.stalledInQueue(ctx, g, checking, true, window) {
		t.Fatal("a rechecking torrent past its window must not be stalled")
	}
	if got := stallClockAt(t, c, g.ID); time.Since(got) > time.Second {
		t.Error("the stall clock must be held while rechecking")
	}
}

// With fail-over on by default, a torrent waiting for a slot under qBittorrent's
// max-active limit would be condemned for the client's own queueing. It holds the clock;
// a magnet nobody will send metadata for does not.
func TestQueuedTorrentHoldsMetadataDoesNot(t *testing.T) {
	c := stallCoord(t, 1, 2, 3)
	ctx := context.Background()
	const window = time.Minute
	expire := func(id int64) { expireStall(t, c, id, 0, 2*window) }

	queued := grab{ID: 1}
	expire(queued.ID)
	if c.stalledInQueue(ctx, queued, download.Item{RawState: "queuedDL", State: "downloading", RemainingBytes: 1}, true, window) {
		t.Error("a torrent queued by the client past its window must not be stalled")
	}
	for _, raw := range []string{"moving", "allocating"} {
		expire(queued.ID)
		if c.stalledInQueue(ctx, queued, download.Item{RawState: raw, State: "downloading", RemainingBytes: 1}, true, window) {
			t.Errorf("a %s torrent must not be stalled", raw)
		}
	}

	// A finished download in a client error state is not a stall — its data is on disk,
	// often waiting in Review, and failing it over would delete it.
	if c.stalledInQueue(ctx, grab{ID: 3}, download.Item{RawState: "error", State: "error", Progress: 1}, true, window) {
		t.Error("a complete torrent in an error state must not be stalled")
	}

	meta := grab{ID: 2}
	expire(meta.ID)
	if !c.stalledInQueue(ctx, meta, download.Item{RawState: "metaDL", State: "downloading", RemainingBytes: 1}, true, window) {
		t.Error("a magnet stuck fetching metadata past its window must be stalled")
	}
}

// The two sides carry the container differently — the torrent as a filename extension, the
// indexer's listing as a trailing word — so their keys differed by "mp4" and no seed rule
// could ever be found for the download.
func TestNormReleaseStripsTheContainerBothWays(t *testing.T) {
	const (
		torrent = "8.Out.Of.10.Cats.Does.Countdown.S20E03.720p.HDTV.x264-Cherzo.mp4"
		listing = "8 Out Of 10 Cats Does Countdown S20E03 720p HDTV x264-Cherzo mp4"
	)
	if a, b := normRelease(torrent), normRelease(listing); a != b {
		t.Errorf("keys still differ:\n  torrent %q\n  listing %q", a, b)
	}

	// A group name that merely ends in an extension's letters must survive intact — ".ts"
	// is a video container, and GHOSTS is not a transport stream.
	if got := normRelease("Some.Show.S01E01.1080p.WEB-GHOSTS"); got != normTitle("Some.Show.S01E01.1080p.WEB-GHOSTS") {
		t.Errorf("trimmed a group name that only looks like an extension: %q", got)
	}
}
