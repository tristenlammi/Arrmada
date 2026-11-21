package series

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

func (f *folderFixture) filesOf(id int64) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM episodes WHERE series_id = ? AND has_file = 1`, id).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *folderFixture) alias(id int64, title, source string) {
	f.t.Helper()
	if _, err := f.db.Exec(`INSERT INTO series_aliases (series_id, title, title_key, tmdb_season, source) VALUES (?, ?, ?, 0, ?)`,
		id, title, parser.TitleKey(title), source); err != nil {
		f.t.Fatal(err)
	}
}

func (f *folderFixture) borrowed(id int64) []BorrowedFile {
	f.t.Helper()
	items, err := f.svc.BorrowedFiles(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return items
}

func pathsOf(items []BorrowedFile) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Path
	}
	return out
}

// An early library scan matched the folder "Power" to "Blossoms of Power", which recorded
// Power's first season as its own. With Power in the library too, those files are listed
// as Power's; nothing changes until they're confirmed, and then only this show's records
// of them go: it has no files and no folder any more, so an import can make it one.
func TestBorrowedFilesFromAWronglyMatchedFolder(t *testing.T) {
	f := newFolderFixture(t, "/tv")
	ctx := context.Background()
	s1 := []string{
		"/tv/Power/Season 1/Power - S01E01 - Not Exactly How We Planned - 1080p WEB-DL.mkv",
		"/tv/Power/Season 1/Power - S01E02 - Whoever He Is - 1080p WEB-DL.mkv",
	}
	blossoms := f.show("Blossoms of Power", s1...)
	power := f.show("Power", append(s1, "/tv/Power/Season 1/Power - S01E03 - This Is Real - 1080p WEB-DL.mkv")...)

	if info := f.svc.ShowFolderOf(ctx, blossoms); !info.Unknown || !strings.Contains(info.Why, "also holds episodes of") {
		t.Fatalf("before: %+v, want the folder unclear", info)
	}
	items := f.borrowed(blossoms)
	if len(items) != 2 || items[0].Owner != "Power" || items[0].Season != 1 || items[0].Episode != 1 {
		t.Fatalf("listed %+v, want Power's two files", items)
	}
	if f.filesOf(blossoms) != 2 {
		t.Fatal("listing them changed the show's records")
	}
	// Power itself borrows nothing: its files are its own.
	if got := f.borrowed(power); len(got) != 0 {
		t.Errorf("Power's own files are listed: %+v", got)
	}

	// Only what was confirmed goes.
	if n, err := f.svc.ForgetBorrowedFiles(ctx, blossoms, pathsOf(items[:1])); err != nil || n != 1 || f.filesOf(blossoms) != 1 {
		t.Fatalf("forgot %d (err %v), %d left; want 1 and 1", n, err, f.filesOf(blossoms))
	}
	if n, err := f.svc.ForgetBorrowedFiles(ctx, blossoms, pathsOf(items)); err != nil || n != 1 {
		t.Fatalf("forgot %d (err %v), want the one left", n, err)
	}
	if info := f.svc.ShowFolderOf(ctx, blossoms); info.Unknown || info.Path != "" || info.Total != 0 {
		t.Errorf("after: %+v, want a show with no files", info)
	}
	if n := f.filesOf(power); n != 3 {
		t.Errorf("Power has %d files, want its 3 untouched", n)
	}
	if n, _ := f.svc.ForgetBorrowedFiles(ctx, blossoms, pathsOf(items)); n != 0 {
		t.Errorf("a second run forgot %d", n)
	}
	var events int
	_ = f.db.QueryRow(`SELECT COUNT(*) FROM series_events WHERE series_id = ? AND event = 'repaired'`, blossoms).Scan(&events)
	if events != 2 {
		t.Errorf("%d repaired events, want one per confirmed run", events)
	}
}

// The other show needn't have the very file recorded: its own files in that folder are
// enough, with the name.
func TestBorrowedFilesWhenTheOwnerRecordedOtherEpisodes(t *testing.T) {
	f := newFolderFixture(t, "/tv")
	wrong := f.show("Blossoms of Power", "/tv/Power/Season 1/Power - S01E01 - Pilot.mkv")
	f.show("Power", "/tv/Power/Season 2/Power - S02E01 - Consequences.mkv")
	if items := f.borrowed(wrong); len(items) != 1 {
		t.Fatalf("listed %+v, want the one file", items)
	}
}

// Everything the names can tell is the show's own is never listed.
func TestBorrowedFilesLeavesOutWhatIsTheShowsOwn(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(f *folderFixture) int64
	}{
		{"the other show isn't in the library", func(f *folderFixture) int64 {
			return f.show("Blossoms of Power", "/tv/Power/Season 1/Power - S01E01.mkv")
		}},
		{"a name no library show goes by, in a shared folder", func(f *folderFixture) int64 {
			id := f.show("Attack on Titan", "/tv/Anime/Shingeki no Kyojin - S01E01.mkv")
			f.show("Bleach", "/tv/Anime/Bleach - S01E01.mkv")
			return id
		}},
		{"the show's own alias, a file both shows record", func(f *folderFixture) int64 {
			p := "/tv/Bleach/Season 17/Bleach - S17E01.mkv"
			id := f.show("Bleach: Thousand-Year Blood War", p)
			f.alias(id, "Bleach", "user")
			f.show("Bleach", p)
			return id
		}},
		{"an automatic alias", func(f *folderFixture) int64 {
			p := "/tv/Money Heist/Season 1/Money Heist - S01E01.mkv"
			id := f.show("La Casa de Papel", p)
			f.alias(id, "Money Heist", "tmdb")
			f.show("Money Heist", p)
			return id
		}},
		{"a name without a title", func(f *folderFixture) int64 {
			id := f.show("Blossoms of Power", "/tv/Power/Season 1/S01E01.mkv")
			f.show("Power", "/tv/Power/Season 1/S01E01.mkv")
			return id
		}},
		{"its own name, in another show's folder", func(f *folderFixture) int64 {
			id := f.show("Blossoms of Power", "/tv/Power/Season 1/Blossoms of Power - S01E01.mkv")
			f.show("Power", "/tv/Power/Season 1/Power - S01E01.mkv")
			return id
		}},
		{"another show's name, but that show's files are elsewhere", func(f *folderFixture) int64 {
			id := f.show("Blossoms of Power", "/tv/Blossoms of Power (2026)/Season 1/Power - S01E01.mkv")
			f.show("Power", "/tv/Power/Season 1/Power - S01E01.mkv")
			return id
		}},
	} {
		f := newFolderFixture(t, "/tv")
		id := c.setup(f)
		if items := f.borrowed(id); len(items) != 0 {
			t.Errorf("%s: listed %+v, want none", c.name, items)
		}
	}
}

// What the names can't tell apart is why a person confirms: a sequel's files kept under
// the first show's name, with no alias, are listed just like a wrong match — and stay
// recorded until someone says otherwise.
func TestBorrowedFilesNeverForgetsWithoutBeingAsked(t *testing.T) {
	f := newFolderFixture(t, "/tv")
	ctx := context.Background()
	sequel := f.show("Bleach: Thousand-Year Blood War", "/tv/Bleach/Season 17/Bleach - S17E01.mkv")
	f.show("Bleach", "/tv/Bleach/Season 01/Bleach - S01E01.mkv")
	if items := f.borrowed(sequel); len(items) != 1 {
		t.Fatalf("listed %+v", items)
	}
	// Nothing asked for, or a path that isn't on the list: nothing forgotten.
	for _, paths := range [][]string{nil, {filepath.FromSlash("/tv/Bleach/Season 01/Bleach - S01E01.mkv")}} {
		if n, err := f.svc.ForgetBorrowedFiles(ctx, sequel, paths); err != nil || n != 0 || f.filesOf(sequel) != 1 {
			t.Fatalf("forgot %d (err %v) for %v", n, err, paths)
		}
	}
	// With the alias the person adds instead, it isn't even listed.
	f.alias(sequel, "Bleach", "user")
	if items := f.borrowed(sequel); len(items) != 0 {
		t.Errorf("still listed with the alias: %+v", items)
	}
}

// The record is only cleared while it still names the file that was judged.
func TestClearEpisodeFileAtOnlyTheJudgedFile(t *testing.T) {
	f := newFolderFixture(t, "/tv")
	ctx := context.Background()
	id := f.show("Show", "/tv/Show/Show - S01E01.mkv")
	if n, err := f.svc.repo.ClearEpisodeFileAt(ctx, id, 1, 1, filepath.FromSlash("/tv/Other/Other - S01E01.mkv")); err != nil || n != 0 || f.filesOf(id) != 1 {
		t.Fatalf("cleared %d (err %v) for another path", n, err)
	}
	if n, err := f.svc.repo.ClearEpisodeFileAt(ctx, id, 1, 1, filepath.FromSlash("/tv/Show/Show - S01E01.mkv")); err != nil || n != 1 || f.filesOf(id) != 0 {
		t.Fatalf("cleared %d (err %v) for its own path", n, err)
	}
}
