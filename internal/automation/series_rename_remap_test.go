package automation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/series"
)

// An applied renumber renames exactly the files it carried — the moves the owner reviewed
// — and leaves a file that's merely named off-scheme for the Rename button.
func TestRenameRemappedRenamesOnlyCarriedFiles(t *testing.T) {
	f := newRenameFixture(t, [2]int{1, 1}, [2]int{2, 1})
	carried := f.canonical(1, 1) // the rebuild put S01E01's old file on S02E01
	f.place(t, 2, 1, carried, "carried")
	odd := filepath.Join(f.root, f.folder, "Season 1", "odd name.mkv") // S01E01, named off-scheme
	f.place(t, 1, 1, odd, "odd")

	res, err := f.c.RenameRemapped(f.ctx, f.id, []series.EpisodeRemap{
		{Absolute: 1, OldSeason: 1, OldEpisode: 1, NewSeason: 2, NewEpisode: 1, FilePath: carried},
		{Absolute: 9, OldSeason: 1, OldEpisode: 9, FilePath: "/nowhere.mkv", Unplaced: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != 1 || len(res.Skipped) != 0 {
		t.Fatalf("result = %+v, want the one carried file renamed", res)
	}
	if p := f.pathOf(t, 2, 1); p != f.canonical(2, 1) || mustRead(t, p) != "carried" {
		t.Errorf("S02E01 = %s, want the carried file under its new name", p)
	}
	if p := f.pathOf(t, 1, 1); p != odd {
		t.Errorf("the off-scheme file moved to %s; only reviewed moves may happen", p)
	}
	if _, err := os.Stat(odd); err != nil {
		t.Errorf("the off-scheme file is gone: %v", err)
	}

	// Nothing carried: nothing renamed, even with off-scheme files about.
	if res, err := f.c.RenameRemapped(f.ctx, f.id, nil); err != nil || res.Moved != 0 {
		t.Errorf("empty remap list: %+v, %v", res, err)
	}
}
