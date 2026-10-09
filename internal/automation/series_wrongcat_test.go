package automation

import (
	"strings"
	"testing"
	"time"
)

// OBS-09: a finished TV download that matches a library show but sits in another
// category never imports. The sweep used to only log it; now Needs you can list it.
// Downloads that are unfinished, in the TV category, or match no show aren't misfiled.
func TestWrongCategoryDownloads(t *testing.T) {
	h := newLifecycleHarness(t)
	misfiled := h.completedTorrent("Show.S01E01.1080p.WEB-DL-GRP", "other", h.download(t, "Show.S01E01.1080p.WEB-DL-GRP", "Show.S01E01.1080p.WEB-DL-GRP.mkv"))
	h.completedTorrent("Unrelated.Program.S01E01.1080p.WEB-DL-GRP", "other", t.TempDir())
	h.completedTorrent("Some.Film.2020.1080p.WEB-DL-GRP", "other", t.TempDir())
	h.qbit.mu.Lock()
	h.qbit.torrents = append(h.qbit.torrents, map[string]any{
		"hash": hashFor("Show.S01E02.1080p.WEB-DL-GRP"), "name": "Show.S01E02.1080p.WEB-DL-GRP", "state": "downloading",
		"progress": 0.5, "amount_left": 1 << 29, "size": 1 << 30, "category": "other",
	})
	h.qbit.mu.Unlock()
	h.c.downloads.InvalidateSnapshot()

	h.c.ImportSeriesDownloads(h.ctx)
	got := h.c.WrongCategoryDownloads()
	if len(got) != 1 || !strings.EqualFold(got[0].Hash, misfiled) || got[0].Category != "other" {
		t.Fatalf("wrong-category downloads = %+v, want just the Show episode", got)
	}

	// Logged once an hour, not on every 30-second sweep.
	h.c.ImportSeriesDownloads(h.ctx)
	n := &h.c.importNotes
	n.mu.Lock()
	logged := n.loggedAt[strings.ToLower(misfiled)]
	n.mu.Unlock()
	if logged.IsZero() || time.Since(logged) > time.Minute {
		t.Errorf("log stamp = %v", logged)
	}

	// Recategorised into TV, it isn't misfiled any more.
	h.qbit.mu.Lock()
	for _, tr := range h.qbit.torrents {
		if tr["hash"] == misfiled {
			tr["category"] = seriesCategory
		}
	}
	h.qbit.mu.Unlock()
	h.c.downloads.InvalidateSnapshot()
	h.c.ImportSeriesDownloads(h.ctx)
	if got := h.c.WrongCategoryDownloads(); len(got) != 0 {
		t.Errorf("after recategorising: %+v", got)
	}
}

// A series download whose files keep failing to place is counted per sweep with its
// first failure time, cleared once a sweep handles it, and forgotten when it leaves the
// client.
func TestSeriesImportFailures(t *testing.T) {
	c := &Coordinator{}
	c.noteSeriesImportFail("ABC", "Show.S01.1080p", 2)
	c.noteSeriesImportFail("abc", "Show.S01.1080p", 1)
	got := c.SeriesImportFailures()
	if len(got) != 1 || got[0].Hash != "abc" || got[0].Attempts != 2 || got[0].Name != "Show.S01.1080p" || got[0].Since.IsZero() {
		t.Fatalf("failures = %+v", got)
	}
	if got[0].LastErr != "1 file couldn't be placed in the library — the log names the folder and the error" {
		t.Errorf("last error = %q", got[0].LastErr)
	}
	c.clearSeriesImportFail("ABC")
	if got := c.SeriesImportFailures(); len(got) != 0 {
		t.Errorf("after a handled sweep: %+v", got)
	}
	c.noteSeriesImportFail("def", "Other.S01", 3)
	c.pruneSeriesImportFails(map[string]bool{"abc": true})
	if got := c.SeriesImportFailures(); len(got) != 0 {
		t.Errorf("after it left the client: %+v", got)
	}
}
