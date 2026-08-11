package automation

import (
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
)

// A paused torrent makes no progress by definition. Reading that as a stall blocklisted
// the release, deleted the torrent AND its data, and grabbed an alternate that couldn't
// download either — every time a user script paused qBittorrent because the cache drive
// filled up, and each casualty left a hit-and-run on the tracker.
func TestPausedTorrentIsNotStalled(t *testing.T) {
	c := &Coordinator{}
	g := grab{ID: 1}
	const window = time.Minute

	paused := download.Item{State: "paused", Progress: 0.4}

	// First observation never condemns anything, so drive it past the window the way the
	// two-minute sweep would: an unpaused torrent frozen at 0.4 must eventually stall.
	frozen := download.Item{State: "downloading", Progress: 0.4}
	c.stalledInQueue(g, frozen, true, window)
	c.stallProgress[g.ID] = stallSample{progress: 0.4, at: time.Now().Add(-2 * window)}
	if !c.stalledInQueue(g, frozen, true, window) {
		t.Fatal("a running torrent frozen past the window must still stall — the check has to keep working")
	}

	// Same elapsed time, same progress, but paused: not a stall, and the clock is held so
	// the pause doesn't accumulate.
	c.stallProgress[g.ID] = stallSample{progress: 0.4, at: time.Now().Add(-2 * window)}
	if c.stalledInQueue(g, paused, true, window) {
		t.Error("a paused torrent must never be condemned as stalled")
	}
	if got := c.stallProgress[g.ID]; time.Since(got.at) > time.Second {
		t.Error("the stall clock must be held while paused, not left to expire")
	}

	// Rechecking after a disk-full crash moves no progress either.
	c.stallProgress[g.ID] = stallSample{progress: 0.4, at: time.Now().Add(-2 * window)}
	if c.stalledInQueue(g, download.Item{State: "checking", Progress: 0.4}, true, window) {
		t.Error("a torrent being rechecked must not be condemned as stalled")
	}
}

// missingFiles (normalized to "error") is what a full disk produces, and it recovers when
// space is freed. Condemning it on sight made a transient storage problem permanent.
func TestErroredTorrentGetsTheStallWindow(t *testing.T) {
	c := &Coordinator{}
	g := grab{ID: 2}
	const window = time.Minute
	errored := download.Item{State: "error", Progress: 0.6}

	if c.stalledInQueue(g, errored, true, window) {
		t.Error("an errored torrent must not be condemned on its first sample")
	}
	// Still broken a window later — now it's genuinely dead and fail-over is right.
	c.stallProgress[g.ID] = stallSample{progress: 0.6, at: time.Now().Add(-2 * window)}
	if !c.stalledInQueue(g, errored, true, window) {
		t.Error("an errored torrent that never recovers must still fail over")
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
