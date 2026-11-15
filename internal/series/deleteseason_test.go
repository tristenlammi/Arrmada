package series

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

// addSeason2 gives the fixture's show a second season with one episode.
func (f *deleteFixture) addSeason2(t *testing.T) {
	t.Helper()
	db := f.svc.repo.db
	if _, err := db.Exec(`INSERT INTO seasons (series_id, season_number, monitored) VALUES (?, 2, 1)`, f.id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO episodes (series_id, season_number, episode_number, monitored) VALUES (?, 2, 1, 1)`, f.id); err != nil {
		t.Fatal(err)
	}
}

func (f *deleteFixture) hasFile(t *testing.T, season, ep int) bool {
	t.Helper()
	p, err := f.svc.repo.EpisodeFilePath(context.Background(), f.id, season, ep)
	return err == nil && p != ""
}

// #111: a season's files go to the bin with their subtitles, each once (a double-episode
// file too), its episodes read as missing, its emptied folder goes, and the rest of the
// show is untouched.
func TestDeleteSeasonFiles(t *testing.T) {
	f := newDeleteFixture(t)
	f.addSeason2(t)
	ctx := context.Background()
	double := f.file(t, filepath.Join("Season 1", "The Bear - S01E01-E02.mkv"), "video")
	for _, e := range []int{1, 2} {
		if err := f.svc.MarkEpisodeImported(ctx, f.id, 1, e, double, 5); err != nil {
			t.Fatal(err)
		}
	}
	f.file(t, filepath.Join("Season 1", "The Bear - S01E01-E02.en.srt"), "subs")
	e3 := f.episode(t, 3)
	s2 := f.file(t, filepath.Join("Season 2", "The Bear - S02E01.mkv"), "video")
	if err := f.svc.MarkEpisodeImported(ctx, f.id, 2, 1, s2, 5); err != nil {
		t.Fatal(err)
	}

	plan, err := f.svc.SeasonDeletePlan(ctx, f.id, 1)
	if err != nil || !reflect.DeepEqual(plan.Paths, []string{double, e3}) || plan.Bytes != 10 || len(plan.Shared) != 0 {
		t.Fatalf("plan = %+v, %v; want the double file and E03 once each, 10 bytes", plan, err)
	}
	sum, err := f.svc.DeleteSeasonFiles(ctx, f.id, 1)
	if err != nil {
		t.Fatalf("delete: %v (%+v)", err, sum)
	}
	if got, want := binFiles(t, f.bin), []string{"The Bear - S01E01-E02.en.srt", "The Bear - S01E01-E02.mkv", "The Bear - S01E03.mkv"}; !reflect.DeepEqual(got, want) {
		t.Errorf("bin = %v, want %v", got, want)
	}
	for _, e := range []int{1, 2, 3} {
		if f.hasFile(t, 1, e) {
			t.Errorf("S01E0%d still reads as having its file", e)
		}
	}
	if exists(filepath.Join(f.show, "Season 1")) {
		t.Error("the emptied season folder is still there")
	}
	if !exists(s2) || !f.hasFile(t, 2, 1) || !f.seriesExists(t) || !exists(f.show) {
		t.Error("season 2, the show or its folder was touched")
	}
}

// A file that also holds another season's episode stays, with every episode it holds.
func TestDeleteSeasonKeepsAFileAnotherSeasonShares(t *testing.T) {
	f := newDeleteFixture(t)
	f.addSeason2(t)
	ctx := context.Background()
	across := f.file(t, "The Bear - S01E03-S02E01.mkv", "video")
	if err := f.svc.MarkEpisodeImported(ctx, f.id, 1, 3, across, 5); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.MarkEpisodeImported(ctx, f.id, 2, 1, across, 5); err != nil {
		t.Fatal(err)
	}
	e1 := f.episode(t, 1)
	plan, _ := f.svc.SeasonDeletePlan(ctx, f.id, 1)
	if !reflect.DeepEqual(plan.Paths, []string{e1}) || !reflect.DeepEqual(plan.Shared, []string{across}) {
		t.Fatalf("plan = %+v; want E01 deleted and the shared file kept", plan)
	}
	if _, err := f.svc.DeleteSeasonFiles(ctx, f.id, 1); err != nil {
		t.Fatal(err)
	}
	if !exists(across) || !f.hasFile(t, 1, 3) || !f.hasFile(t, 2, 1) {
		t.Error("the shared file or its episodes went")
	}
	if exists(e1) || f.hasFile(t, 1, 1) {
		t.Error("E01 wasn't deleted")
	}
}

// A season the show doesn't have is refused; a bin that can't be used moves nothing.
func TestDeleteSeasonRefusals(t *testing.T) {
	f := newDeleteFixture(t)
	ctx := context.Background()
	if _, err := f.svc.DeleteSeasonFiles(ctx, f.id, 9); !errors.Is(err, ErrNoSuchSeason) {
		t.Errorf("season 9: %v, want ErrNoSuchSeason", err)
	}
	v := f.episode(t, 1)
	f.file(t, filepath.Join("..", "binblock"), "a file where the bin's folder should go")
	f.svc.SetRecycleDir(filepath.Join(f.root, "binblock", "bin"))
	if _, err := f.svc.DeleteSeasonFiles(ctx, f.id, 1); !errors.Is(err, ErrFilesNotRemoved) {
		t.Fatalf("unusable bin: %v, want ErrFilesNotRemoved", err)
	}
	if !exists(v) || !f.hasFile(t, 1, 1) {
		t.Error("a file moved though the bin couldn't be used")
	}
}
