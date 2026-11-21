package automation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A show whose only recorded files are another library show's (an early library scan
// matched that show's folder to it) is refused as before — by an import, a rescan, a rename
// and the duplicate check, none of which changes a record — until someone confirms the
// files aren't its own. Then the download is imported into a folder of the show's own, and
// the other show's files and records are exactly as they were.
func TestImportForAShowHoldingAnotherShowsFiles(t *testing.T) {
	h := newLifecycleHarness(t)
	tv := h.c.imp.TVRoot()
	theirs := libFile(t, filepath.Join(tv, "Other", "Season 1", "Other - S01E01.mkv"))
	setEpisodeFile(t, h.c, h.seriesID, 1, 1, theirs, "")
	res, err := h.c.db.Exec(`INSERT INTO series (tmdb_id, title, monitored) VALUES (99, 'Other', 1)`)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := res.LastInsertId()
	setEpisodeFile(t, h.c, other, 1, 1, theirs, "")
	before, _ := os.ReadFile(theirs)

	const name = "Show.S01E02.1080p.WEB-DL-GRP"
	dl := h.download(t, name, name+".mkv")
	hash := h.completedTorrent(name, seriesCategory, dl)
	h.seriesGrab(t, name, hash)
	h.c.ImportSeriesDownloads(h.ctx)

	// Nothing is forgotten on its own.
	if r := reviewFor(t, h.c, hash); !strings.Contains(r.Reason, "Can't tell which folder Show lives in") {
		t.Fatalf("review = %q, want the folder refusal", r.Reason)
	}
	if err := h.c.RescanSeries(h.ctx, h.seriesID); err == nil {
		t.Error("a rescan went ahead for a show whose folder can't be told")
	}
	if _, err := h.c.SeriesRenamePreview(h.ctx, h.seriesID); err == nil {
		t.Error("a rename preview went ahead for a show whose folder can't be told")
	}
	if _, err := h.c.SeriesDuplicates(h.ctx, h.seriesID); err == nil {
		t.Error("the duplicate check went ahead for a show whose folder can't be told")
	}
	if got := h.c.series.CurrentEpisodeFile(h.ctx, h.seriesID, 1, 1); got.Path != theirs {
		t.Fatalf("the record changed without being asked: %q", got.Path)
	}

	// Confirmed by hand: the show lets go of the file, and its download can come in.
	items, err := h.c.series.BorrowedFiles(h.ctx, h.seriesID)
	if err != nil || len(items) != 1 || items[0].Owner != "Other" {
		t.Fatalf("listed %+v (err %v), want Other's one file", items, err)
	}
	if n, err := h.c.series.ForgetBorrowedFiles(h.ctx, h.seriesID, []string{items[0].Path}); err != nil || n != 1 {
		t.Fatalf("forgot %d (err %v)", n, err)
	}
	s, _ := h.c.series.Get(h.ctx, h.seriesID)
	if _, _, _, failed, _, _ := h.c.importSeriesInto(h.ctx, s, dl, forceAll); failed != 0 {
		t.Fatalf("importSeriesInto failed=%d after the files were forgotten", failed)
	}
	cur := h.c.series.CurrentEpisodeFile(h.ctx, h.seriesID, 1, 2)
	if cur.Path == "" {
		t.Fatal("S01E02 wasn't imported")
	}
	if strings.HasPrefix(cur.Path, filepath.Join(tv, "Other")+string(filepath.Separator)) {
		t.Errorf("S01E02 landed in the other show's folder: %s", cur.Path)
	}
	if got := h.c.series.CurrentEpisodeFile(h.ctx, h.seriesID, 1, 1); got.Path != "" {
		t.Errorf("the show still claims %s", got.Path)
	}
	if got := h.c.series.CurrentEpisodeFile(h.ctx, other, 1, 1); got.Path != theirs {
		t.Errorf("the other show's record is now %q", got.Path)
	}
	if after, err := os.ReadFile(theirs); err != nil || string(after) != string(before) {
		t.Errorf("the other show's file changed: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(tv, "Other", "Season 1"))
	if len(entries) != 1 {
		t.Errorf("the other show's season folder holds %d files, want its one", len(entries))
	}
}
