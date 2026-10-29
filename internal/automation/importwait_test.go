package automation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The series import used to read a download folder it couldn't open as a release with no
// video in it. With qBittorrent's incomplete folder on, a torrent's content path names that
// folder until the move to the save folder is done, and Arrmada doesn't mount it: the
// season pack was blocklisted as junk, its grab failed, and the next search grabbed another
// copy of the same season — which could meet the same fate.
//
// A folder that can't be read is now retried every sweep, never blocklisted, and shows in
// Needs you with its path; once it can be read, it imports as normal.
func TestSeriesImportRetriesAFolderItCantRead(t *testing.T) {
	h := newLifecycleHarness(t)
	const name = "Show.S01.1080p.WEB-DL-GRP"
	// Where qBittorrent says the files are: a folder that doesn't exist here, as
	// /incomplete/<name> doesn't inside an Arrmada container that never mounted it.
	where := filepath.Join(t.TempDir(), "incomplete", name)
	hash := h.completedTorrent(name, seriesCategory, where)
	gid := h.seriesGrab(t, name, hash)

	for range 2 {
		h.c.ImportSeriesDownloads(h.ctx)
	}

	blocked, err := h.c.blockedSetSeries(h.ctx, h.seriesID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked[normTitle(name)] {
		t.Fatal("a release whose folder couldn't be read was blocklisted as junk")
	}
	if got := h.status(t, gid); got != grabStatusGrabbed {
		t.Fatalf("grab status = %q, want it still %q (so the season isn't searched again)", got, grabStatusGrabbed)
	}
	if len(*h.removed) != 0 {
		t.Fatalf("the torrent was removed: %+v", *h.removed)
	}
	fails := h.c.SeriesImportFailures()
	if len(fails) != 1 || fails[0].Attempts != 2 || !strings.Contains(fails[0].LastErr, where) {
		t.Fatalf("Needs you should name the unreadable folder after two sweeps, got %+v", fails)
	}

	// The move finishes: the files are where the content path says. The next sweep imports
	// them as it would any other download.
	if err := os.MkdirAll(filepath.Dir(where), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(h.download(t, name, name+".E01.mkv", name+".E02.mkv"), where); err != nil {
		t.Fatal(err)
	}
	// The pack's files are named "Show.S01.1080p.WEB-DL-GRP.E01.mkv": rename to episode
	// names the parser reads on their own.
	for _, ep := range []string{"E01", "E02"} {
		if err := os.Rename(filepath.Join(where, name+"."+ep+".mkv"), filepath.Join(where, "Show.S01"+ep+".1080p.WEB-DL-GRP.mkv")); err != nil {
			t.Fatal(err)
		}
	}
	h.c.ImportSeriesDownloads(h.ctx)
	if got := h.status(t, gid); got != grabStatusImported {
		t.Fatalf("after the folder appeared: grab status = %q, want %q", got, grabStatusImported)
	}
	if fails := h.c.SeriesImportFailures(); len(fails) != 0 {
		t.Errorf("Needs you still lists it after it imported: %+v", fails)
	}
}

// The other side of the same branch: a folder that was read and really holds no video is
// still junk — blocklisted, its grab failed — exactly as before.
func TestSeriesImportStillBlocklistsAReadableFolderWithNoVideo(t *testing.T) {
	h := newLifecycleHarness(t)
	const name = "Show.S01.1080p.WEB-DL-GRP"
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("nothing to see"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash := h.completedTorrent(name, seriesCategory, dir)
	gid := h.seriesGrab(t, name, hash)

	h.c.ImportSeriesDownloads(h.ctx)

	blocked, err := h.c.blockedSetSeries(h.ctx, h.seriesID)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked[normTitle(name)] {
		t.Error("a readable folder with no video in it wasn't blocklisted")
	}
	if got := h.status(t, gid); got != grabStatusFailed {
		t.Errorf("grab status = %q, want %q", got, grabStatusFailed)
	}
	if fails := h.c.SeriesImportFailures(); len(fails) != 0 {
		t.Errorf("a junk release isn't an import failure to retry: %+v", fails)
	}
}

// A torrent qBittorrent is still moving isn't offered to the import at all
// (download.Item.ReadyToImport): nothing is read, noted, blocklisted or failed while the
// move runs.
func TestSeriesImportWaitsWhileQbittorrentMovesATorrent(t *testing.T) {
	h := newLifecycleHarness(t)
	const name = "Show.S01.1080p.WEB-DL-GRP"
	where := filepath.Join(t.TempDir(), "incomplete", name)
	hash := h.completedTorrent(name, seriesCategory, where)
	gid := h.seriesGrab(t, name, hash)
	h.qbit.mu.Lock()
	for _, tr := range h.qbit.torrents {
		if tr["hash"] == hash {
			tr["state"] = "moving"
		}
	}
	h.qbit.mu.Unlock()
	h.c.downloads.InvalidateSnapshot()

	h.c.ImportSeriesDownloads(h.ctx)

	blocked, err := h.c.blockedSetSeries(h.ctx, h.seriesID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked[normTitle(name)] {
		t.Error("a torrent mid-move was blocklisted")
	}
	if got := h.status(t, gid); got != grabStatusGrabbed {
		t.Errorf("grab status = %q, want %q", got, grabStatusGrabbed)
	}
	if fails := h.c.SeriesImportFailures(); len(fails) != 0 {
		t.Errorf("a torrent mid-move isn't a failing import: %+v", fails)
	}
}
