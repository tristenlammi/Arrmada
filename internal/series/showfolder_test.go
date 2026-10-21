package series

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/libfolders"
	"github.com/tristenlammi/arrmada/internal/libroots"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// folderFixture is a series service over one TV folder (/tv unless changed), with shows
// whose episode rows point wherever a test says. Nothing needs to exist on disk: which
// folder a show lives in is read from its recorded files (and every recorded file counts
// as there, unless a test says otherwise).
type folderFixture struct {
	t   *testing.T
	db  store.Pool
	svc *Service
	n   int
}

func newFolderFixture(t *testing.T, root string) *folderFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := NewService(st.DB(), nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetRootFunc(func() string { return root })
	svc.fileGone = func(string) bool { return false }
	return &folderFixture{t: t, db: st.DB(), svc: svc}
}

// show adds a series with one episode row per file (season 1, episodes 1..n).
func (f *folderFixture) show(title string, files ...string) int64 {
	f.t.Helper()
	f.n++
	res, err := f.db.Exec(`INSERT INTO series (tmdb_id, title, monitored) VALUES (?, ?, 1)`, 1000+f.n, title)
	if err != nil {
		f.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	for i, p := range files {
		if _, err := f.db.Exec(`INSERT INTO episodes (series_id, season_number, episode_number, monitored, has_file, file_path)
			VALUES (?, 1, ?, 1, 1, ?)`, id, i+1, filepath.FromSlash(p)); err != nil {
			f.t.Fatal(err)
		}
	}
	return id
}

// Season folders under every name people use — and none at all — give the show's folder,
// not the season folder. A show named like a number ("1992", "24") stays itself.
func TestShowFolderOfSeasonFolderNames(t *testing.T) {
	for _, c := range []struct{ name, file, want string }{
		{"S01", "/tv/Pechino Express/S12/Pechino Express - S12E01.mkv", "/tv/Pechino Express"},
		{"Stagione", "/tv/Gomorra/Stagione 1/Gomorra - S01E01.mkv", "/tv/Gomorra"},
		{"Saison", "/tv/Lupin/Saison 1/Lupin - S01E01.mkv", "/tv/Lupin"},
		{"Staffel", "/tv/Dark/Staffel 01/Dark - S01E01.mkv", "/tv/Dark"},
		{"Season.01", "/tv/Show/Season.01/Show - S01E01.mkv", "/tv/Show"},
		{"Series", "/tv/Taskmaster/Series 1/Taskmaster - S01E01.mkv", "/tv/Taskmaster"},
		{"Temporada", "/tv/Elite/Temporada 2/Elite - S02E01.mkv", "/tv/Elite"},
		{"flat", "/tv/The Bear (2022)/The Bear - S01E01.mkv", "/tv/The Bear (2022)"},
		{"release folder in season", "/tv/Show/Season 1/Show.S01E01.1080p/Show.S01E01.mkv", "/tv/Show"},
		{"odd season name", "/tv/Avatar/Book 1 Water/Avatar - S01E01.mkv", "/tv/Avatar/Book 1 Water"}, // the folder holding the files, never one above it
		{"show named 1992", "/tv/1992/S01/1992 - S01E01.mkv", "/tv/1992"},
		{"show named 24", "/tv/24/Season 1/24 - S01E01.mkv", "/tv/24"},
		{"S.W.A.T.", "/tv/S.W.A.T. (2017)/S01/S.W.A.T. - S01E01.mkv", "/tv/S.W.A.T. (2017)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFolderFixture(t, "/tv")
			id := f.show("Show", c.file)
			info := f.svc.ShowFolderOf(context.Background(), id)
			if info.Unknown || info.Path != filepath.FromSlash(c.want) {
				t.Fatalf("ShowFolderOf = %+v, want %s", info, c.want)
			}
			if got := f.svc.ExistingFolderName(context.Background(), id); got != filepath.Base(c.want) {
				t.Errorf("ExistingFolderName = %q, want %q", got, filepath.Base(c.want))
			}
		})
	}
}

// Outside every TV folder the folder names are all there is to go on: the broader season
// names are stepped over, a bare number is not.
func TestShowFolderOutsideTheTVFolder(t *testing.T) {
	for file, want := range map[string]string{
		"/old/Gomorra/Stagione 1/x.mkv": "/old/Gomorra",
		"/old/Show/S01/x.mkv":           "/old/Show",
		"/old/Show/Specials/x.mkv":      "/old/Show",
		"/old/1992/x.mkv":               "/old/1992",
		"/old/1883/Season 1/x.mkv":      "/old/1883",
	} {
		if got := ShowFolder(filepath.FromSlash(file)); got != filepath.FromSlash(want) {
			t.Errorf("ShowFolder(%s) = %s, want %s", file, got, want)
		}
		f := newFolderFixture(t, "/tv")
		if got := f.svc.ShowFolderFor(filepath.FromSlash(file)); got != filepath.FromSlash(want) {
			t.Errorf("ShowFolderFor(%s) = %s, want %s", file, got, want)
		}
	}
}

// The show's folder is where most of its files are. Files the season-folder bug put in
// <TV>/S13/Season 12 are outvoted — and even when they're the majority, never chosen,
// because a folder named like a season is never a show's folder (#56).
func TestShowFolderOfIgnoresMisplacedFiles(t *testing.T) {
	ctx := context.Background()
	f := newFolderFixture(t, "/tv")
	id := f.show("Pechino Express",
		"/tv/Pechino Express/S12/Pechino Express - S12E01.mkv",
		"/tv/Pechino Express/S12/Pechino Express - S12E02.mkv",
		"/tv/S13/Season 12/Pechino Express - S12E03.mkv",
		"/tv/S13/Season 12/Pechino Express - S12E04.mkv",
		"/tv/S13/Season 13/Pechino Express - S13E01.mkv",
	)
	info := f.svc.ShowFolderOf(ctx, id)
	if info.Unknown || info.Path != filepath.FromSlash("/tv/Pechino Express") || info.Files != 2 || info.Total != 5 {
		t.Fatalf("ShowFolderOf = %+v, want /tv/Pechino Express holding 2 of 5", info)
	}

	// Every file misplaced: there's no telling where the show lives.
	lost := f.show("Godfather of Harlem", "/tv/S01/Season 4/Godfather of Harlem - S04E01.mkv")
	info = f.svc.ShowFolderOf(ctx, lost)
	if !info.Unknown || info.Path != "" || !strings.Contains(info.Why, "named like a season folder") {
		t.Fatalf("all misplaced: ShowFolderOf = %+v, want Unknown", info)
	}
	if got := f.svc.ExistingFolderName(ctx, lost); got != "" {
		t.Errorf("ExistingFolderName = %q, want none", got)
	}
	// Once the misplaced files have been moved away by hand they no longer count against
	// the show; files in its own folder are never checked on disk.
	f.svc.fileGone = func(p string) bool { return strings.Contains(p, filepath.FromSlash("/tv/S")) }
	if info := f.svc.ShowFolderOf(ctx, lost); !info.Unknown || info.Path != "" {
		t.Errorf("misplaced files gone: ShowFolderOf = %+v", info)
	}
	if info := f.svc.ShowFolderOf(ctx, id); info.Path != filepath.FromSlash("/tv/Pechino Express") || info.Total != 2 {
		t.Errorf("Pechino Express with its misplaced files gone = %+v", info)
	}
}

// Two shows' files in one folder: it's neither's for sure, so neither imports into it.
func TestShowFolderOfSharedFolder(t *testing.T) {
	ctx := context.Background()
	f := newFolderFixture(t, "/tv")
	a := f.show("Teen Titans", "/tv/Teen Titans/Season 1/Teen Titans - S01E01.mkv")
	b := f.show("Teen Titans Go!", "/tv/Teen Titans/Season 1/Teen Titans Go! - S01E01.mkv")
	for _, id := range []int64{a, b} {
		if info := f.svc.ShowFolderOf(ctx, id); !info.Unknown || !strings.Contains(info.Why, "also holds episodes of") {
			t.Errorf("series %d: ShowFolderOf = %+v, want Unknown (shared)", id, info)
		}
	}
	// The prefix is a whole folder: "Teen Titans Go" isn't inside "Teen Titans".
	c := f.show("Teen Titans Go", "/tv/Teen Titans Go/Season 1/Teen Titans Go - S01E01.mkv")
	if info := f.svc.ShowFolderOf(ctx, c); info.Unknown || info.Path != filepath.FromSlash("/tv/Teen Titans Go") {
		t.Errorf("Teen Titans Go = %+v", info)
	}
}

// A TV folder set one level too high (/storage, with the shows in /storage/TV Shows): the
// first-level folder holds many shows, so the folder above the season folder is the show's.
func TestShowFolderOfTVFolderSetTooHigh(t *testing.T) {
	ctx := context.Background()
	f := newFolderFixture(t, "/storage")
	pe := f.show("Pechino Express", "/storage/TV Shows/Pechino Express/S12/Pechino Express - S12E01.mkv")
	f.show("Lost", "/storage/TV Shows/Lost/S01/Lost - S01E01.mkv")
	f.show("Fargo", "/storage/TV Shows/Fargo/Season 1/Fargo - S01E01.mkv")
	sr := Series{ID: pe}
	info := f.svc.ShowFolderOf(ctx, pe)
	if info.Unknown || info.Path != filepath.FromSlash("/storage/TV Shows/Pechino Express") {
		t.Fatalf("ShowFolderOf = %+v", info)
	}
	if got := f.svc.ImportDir(ctx, sr); got != filepath.FromSlash("/storage/TV Shows") {
		t.Errorf("ImportDir = %q, want /storage/TV Shows", got)
	}
	// A folder with a season name Arrmada doesn't know is taken as it is — never the folder
	// above it — and a misplaced copy beside them is still misplaced.
	dark := f.show("Dark", "/storage/TV Shows/Dark/Book One/Dark - S01E01.mkv", "/storage/TV Shows/S02/Season 2/Dark - S02E01.mkv")
	if info := f.svc.ShowFolderOf(ctx, dark); info.Path != filepath.FromSlash("/storage/TV Shows/Dark/Book One") {
		t.Errorf("Dark = %+v", info)
	}
	if _, m, _ := f.svc.MisplacedFiles(ctx, dark); len(m) != 1 || !strings.Contains(m[0].Why, "named like a season folder") {
		t.Errorf("Dark's misplaced = %+v", m)
	}
}

// A show whose only files sit loose in the TV folder has no folder yet; a new one is made
// from the scheme rather than the TV folder's own name.
func TestShowFolderOfLooseFiles(t *testing.T) {
	f := newFolderFixture(t, "/tv")
	id := f.show("Show", "/tv/Show - S01E01.mkv")
	if info := f.svc.ShowFolderOf(context.Background(), id); info.Unknown || info.Path != "" {
		t.Fatalf("ShowFolderOf = %+v, want no folder and not unknown", info)
	}
	if got := f.svc.ShowFolderFor(filepath.FromSlash("/tv/Show - S01E01.mkv")); got != "" {
		t.Errorf("ShowFolderFor(loose) = %q, want none", got)
	}
}

// Several TV folders: the show's folder is the first folder below whichever TV folder its
// files are in, and its episodes import under that TV folder.
func TestShowFolderOfSeveralTVFolders(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	base := t.TempDir()
	tv, animeDir := filepath.Join(base, "tv"), filepath.Join(base, "anime")
	for _, d := range []string{tv, animeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	roots := libroots.New(nil, config.Config{TVDir: tv})
	folders := libfolders.New(st.DB(), roots, filepath.Join(base, "data"))
	if _, err := folders.Add(ctx, libfolders.AddInput{Media: libfolders.MediaSeries, Path: animeDir}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st.DB(), nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetRootFunc(func() string { return tv })
	svc.SetFolders(folders)
	f := &folderFixture{t: t, db: st.DB(), svc: svc}
	id := f.show("Frieren", filepath.ToSlash(filepath.Join(animeDir, "Frieren", "S01", "Frieren - S01E01.mkv")))
	info := svc.ShowFolderOf(ctx, id)
	if info.Unknown || info.Path != filepath.Join(animeDir, "Frieren") {
		t.Fatalf("ShowFolderOf = %+v", info)
	}
	if got := svc.ImportDir(ctx, Series{ID: id}); got != animeDir {
		t.Errorf("ImportDir = %q, want the Anime folder", got)
	}
	// A misplaced copy in the other TV folder's "S01" doesn't move it.
	if _, err := st.DB().Exec(`INSERT INTO episodes (series_id, season_number, episode_number, monitored, has_file, file_path)
		VALUES (?, 1, 9, 1, 1, ?)`, id, filepath.Join(tv, "S01", "Season 1", "Frieren - S01E09.mkv")); err != nil {
		t.Fatal(err)
	}
	if info := svc.ShowFolderOf(ctx, id); info.Path != filepath.Join(animeDir, "Frieren") {
		t.Errorf("with a misplaced copy: ShowFolderOf = %+v", info)
	}
}

// The last check before a file is placed refuses a folder named like a season, a TV
// folder, and a folder another show's files are in.
func TestUnsafeImportFolder(t *testing.T) {
	ctx := context.Background()
	f := newFolderFixture(t, "/tv")
	a := f.show("Lost", "/tv/S01/Season 1/Lost - S01E01.mkv")
	b := f.show("Fringe", "/tv/Fringe/S01/Fringe - S01E01.mkv")
	for dir, wantRefused := range map[string]bool{
		"/tv/S13":        true,  // named like a season
		"/tv/Stagione 2": true,  // likewise
		"/tv":            true,  // the TV folder itself
		"/tv/tv":         true,  // named after it (the old flat-layout bug)
		"/tv/S01":        true,  // Lost's misplaced files are there
		"/tv/Fringe":     false, // Fringe's own folder, for Fringe
		"/tv/New Show":   false,
		"/tv/1992":       false,
	} {
		why := f.svc.UnsafeImportFolder(ctx, Series{ID: b, Title: "Fringe"}, filepath.FromSlash(dir))
		if (why != "") != wantRefused {
			t.Errorf("UnsafeImportFolder(%s) = %q, want refused=%v", dir, why, wantRefused)
		}
	}
	// Fringe's folder is another show's, for Lost.
	if why := f.svc.UnsafeImportFolder(ctx, Series{ID: a, Title: "Lost"}, filepath.FromSlash("/tv/Fringe")); why == "" {
		t.Error("Lost may import into Fringe's folder")
	}
	err := &ShowFolderError{Title: "Lost", Why: "x"}
	if !errors.Is(err, ErrNoShowFolder) || !strings.Contains(err.Error(), "Can't tell which folder Lost lives in") {
		t.Errorf("ShowFolderError = %v", err)
	}
}

// The health check's list: every show with files the bug misplaced, from one pass over the
// database — matching what each show's own MisplacedFiles says.
func TestMisplacedShows(t *testing.T) {
	ctx := context.Background()
	f := newFolderFixture(t, "/tv")
	pe := f.show("Pechino Express",
		"/tv/Pechino Express/S12/a - S12E01.mkv",
		"/tv/S13/Season 12/a - S12E02.mkv",
		"/tv/S13/Season 13/a - S13E01.mkv")
	gh := f.show("Godfather of Harlem", "/tv/S01/Season 4/b - S04E01.mkv")
	f.show("Fine", "/tv/Fine/S01/c - S01E01.mkv")
	f.show("Teen Titans", "/tv/Teen Titans/Season 1/d - S01E01.mkv")
	f.show("Teen Titans Go!", "/tv/Teen Titans/Season 1/e - S01E01.mkv")

	got, err := f.svc.MisplacedShows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SeriesID != pe || got[0].Files != 2 || got[0].NoFolder ||
		got[1].SeriesID != gh || got[1].Files != 1 || !got[1].NoFolder || got[0].Title != "Pechino Express" {
		t.Fatalf("MisplacedShows = %+v", got)
	}
	info, files, err := f.svc.MisplacedFiles(ctx, pe)
	if err != nil || info.Path != filepath.FromSlash("/tv/Pechino Express") || len(files) != 2 {
		t.Fatalf("MisplacedFiles = %+v %+v %v", info, files, err)
	}
	for _, m := range files {
		if !strings.Contains(m.Why, "named like a season folder") {
			t.Errorf("why = %q", m.Why)
		}
	}
}

// Shows kept below a category folder (or a TV folder set one level too high) are found by
// their own folder however many shows the category holds, as v0.3.1 found them — never
// the category folder, which an import would then fill with "Season N" folders.
func TestShowFolderOfNestedLayouts(t *testing.T) {
	ctx := context.Background()
	check := func(t *testing.T, f *folderFixture, id int64, want string) {
		t.Helper()
		info := f.svc.ShowFolderOf(ctx, id)
		if info.Unknown || info.Path != filepath.FromSlash(want) {
			t.Fatalf("ShowFolderOf = %+v, want %s", info, want)
		}
		if got := f.svc.ImportDirOf(ctx, Series{ID: id}, info); got != filepath.Dir(filepath.FromSlash(want)) {
			t.Errorf("ImportDir = %s, want %s", got, filepath.Dir(want))
		}
	}
	t.Run("category with one show", func(t *testing.T) {
		f := newFolderFixture(t, "/tv")
		id := f.show("Frieren", "/tv/Anime/Frieren/Season 1/Frieren - S01E01.mkv")
		f.show("Lost", "/tv/Lost/Season 1/Lost - S01E01.mkv")
		check(t, f, id, "/tv/Anime/Frieren")
		if got := f.svc.ShowFolderFor(filepath.FromSlash("/tv/Anime/Frieren/Season 1/Frieren - S01E01.mkv")); got != filepath.FromSlash("/tv/Anime/Frieren") {
			t.Errorf("ShowFolderFor = %s", got)
		}
	})
	t.Run("category with two shows", func(t *testing.T) {
		f := newFolderFixture(t, "/tv")
		a := f.show("Bluey", "/tv/Kids/Bluey/Season 1/Bluey - S01E01.mkv")
		b := f.show("Peppa Pig", "/tv/Kids/Peppa Pig/S01/Peppa Pig - S01E01.mkv")
		check(t, f, a, "/tv/Kids/Bluey")
		check(t, f, b, "/tv/Kids/Peppa Pig")
	})
	t.Run("category with three shows", func(t *testing.T) {
		f := newFolderFixture(t, "/tv")
		a := f.show("Bluey", "/tv/Kids/Bluey/Season 1/Bluey - S01E01.mkv")
		f.show("Peppa Pig", "/tv/Kids/Peppa Pig/Season 1/Peppa Pig - S01E01.mkv")
		f.show("Paw Patrol", "/tv/Kids/Paw Patrol/S01/Paw Patrol - S01E01.mkv")
		check(t, f, a, "/tv/Kids/Bluey")
	})
	t.Run("TV folder too high, one show", func(t *testing.T) {
		f := newFolderFixture(t, "/storage")
		check(t, f, f.show("Lost", "/storage/TV Shows/Lost/Season 1/Lost - S01E01.mkv"), "/storage/TV Shows/Lost")
	})
	t.Run("one season in S01", func(t *testing.T) {
		f := newFolderFixture(t, "/tv")
		id := f.show("Mini", "/tv/Mini/S01/Mini - S01E01.mkv")
		f.show("Lost", "/tv/Lost/Season 1/Lost - S01E01.mkv")
		check(t, f, id, "/tv/Mini")
	})
	t.Run("flat", func(t *testing.T) {
		f := newFolderFixture(t, "/tv")
		check(t, f, f.show("Flat", "/tv/Flat (2020)/Flat - S01E01.mkv"), "/tv/Flat (2020)")
	})
	t.Run("release folders", func(t *testing.T) {
		f := newFolderFixture(t, "/tv")
		check(t, f, f.show("Show", "/tv/Show/Show.S01.1080p.WEB-GRP/Show.S01E01.mkv"), "/tv/Show")
		check(t, f, f.show("Other", "/tv/Anime/Other/Season 1/Other.S01E01.1080p/Other.S01E01.mkv"), "/tv/Anime/Other")
	})
	t.Run("flat inside a category", func(t *testing.T) {
		f := newFolderFixture(t, "/tv")
		file := "/tv/Anime/Frieren (2023)/Frieren - S01E01.mkv"
		id := f.show("Frieren", file)
		check(t, f, id, "/tv/Anime/Frieren (2023)")
		if got := f.svc.ShowFoldersOf(ctx, id)(filepath.FromSlash(file)); got != filepath.FromSlash("/tv/Anime/Frieren (2023)") {
			t.Errorf("ShowFoldersOf = %s", got)
		}
	})
}

// A folder holding the show's files directly, deeper than the first below the TV folder,
// is the show's only when that's proven — named after the show or after what its files
// call it, or one of several such folders side by side — and otherwise the show's folder
// is unknown. It is never the category folder above it.
func TestShowFolderOfFlatInsideACategory(t *testing.T) {
	ctx := context.Background()
	t.Run("romaji folder, files named alike", func(t *testing.T) {
		f := newFolderFixture(t, "/tv")
		id := f.show("Frieren", "/tv/Anime/Sousou no Frieren/Sousou no Frieren - S01E01.mkv")
		f.show("Lost", "/tv/Anime/Lost/Season 1/Lost - S01E01.mkv")
		if info := f.svc.ShowFolderOf(ctx, id); info.Path != filepath.FromSlash("/tv/Anime/Sousou no Frieren") {
			t.Fatalf("ShowFolderOf = %+v", info)
		}
	})
	t.Run("TV folder too high, romaji folder", func(t *testing.T) {
		f := newFolderFixture(t, "/media")
		id := f.show("Frieren", "/media/TV/Sousou no Frieren/Sousou no Frieren - S01E01.mkv")
		if info := f.svc.ShowFolderOf(ctx, id); info.Path != filepath.FromSlash("/media/TV/Sousou no Frieren") {
			t.Fatalf("ShowFolderOf = %+v", info)
		}
	})
	t.Run("the folder holding the files, whatever it's called", func(t *testing.T) {
		f := newFolderFixture(t, "/tv")
		id := f.show("Frieren", "/tv/Anime/Sousou no Frieren/Frieren - S01E01.mkv")
		info := f.svc.ShowFolderOf(ctx, id)
		if info.Unknown || info.Path != filepath.FromSlash("/tv/Anime/Sousou no Frieren") {
			t.Fatalf("ShowFolderOf = %+v", info)
		}
		if _, m, _ := f.svc.MisplacedFiles(ctx, id); len(m) != 0 {
			t.Errorf("misplaced = %+v, want none", m)
		}
		if got := f.svc.ShowFolderFor(filepath.FromSlash("/tv/Anime/Sousou no Frieren/Frieren - S01E01.mkv")); got != filepath.FromSlash("/tv/Anime/Sousou no Frieren") {
			t.Errorf("ShowFolderFor = %s, never the category", got)
		}
	})
	t.Run("odd season folders side by side", func(t *testing.T) {
		f := newFolderFixture(t, "/tv")
		id := f.show("Avatar The Last Airbender",
			"/tv/Cartoons/Avatar/Book 1 Water/ATLA - S01E01.mkv",
			"/tv/Cartoons/Avatar/Book 2 Earth/ATLA - S02E01.mkv")
		if info := f.svc.ShowFolderOf(ctx, id); !info.Unknown || info.Path != "" {
			t.Fatalf("ShowFolderOf = %+v, want unknown: never the folder above them", info)
		}
	})
	t.Run("one odd season folder: the folder holding the files", func(t *testing.T) {
		f := newFolderFixture(t, "/tv")
		id := f.show("Avatar The Last Airbender", "/tv/Cartoons/Avatar/Book 1 Water/ATLA - S01E01.mkv")
		if info := f.svc.ShowFolderOf(ctx, id); info.Path != filepath.FromSlash("/tv/Cartoons/Avatar/Book 1 Water") {
			t.Fatalf("ShowFolderOf = %+v", info)
		}
	})
	t.Run("release folder and disc folder", func(t *testing.T) {
		f := newFolderFixture(t, "/tv")
		a := f.show("Some Show", "/tv/Anime/Some Show/Some.Show.S01.1080p.WEB-GRP/Some.Show.S01E01.mkv")
		b := f.show("The Wire", "/tv/The Wire (2002)/Season 01/Disc 1/The Wire - S01E01.mkv")
		if info := f.svc.ShowFolderOf(ctx, a); info.Path != filepath.FromSlash("/tv/Anime/Some Show") {
			t.Errorf("release folder: %+v", info)
		}
		if info := f.svc.ShowFolderOf(ctx, b); info.Path != filepath.FromSlash("/tv/The Wire (2002)") {
			t.Errorf("disc folder: %+v", info)
		}
	})
}

// A stray folder with one of the show's files doesn't outvote the folder holding most of
// them just because another show's files are there too: the show's folder is unknown, and
// none of its files are listed as misplaced.
func TestShowFolderOfSharedMajority(t *testing.T) {
	ctx := context.Background()
	f := newFolderFixture(t, "/tv")
	id := f.show("Teen Titans",
		"/tv/Teen Titans/Season 1/Teen Titans - S01E01.mkv",
		"/tv/Teen Titans/Season 1/Teen Titans - S01E02.mkv",
		"/tv/Teen Titans/Season 1/Teen Titans - S01E03.mkv",
		"/tv/Teen Titans (2003)/Season 2/Teen Titans - S02E01.mkv")
	f.show("Teen Titans Go!", "/tv/Teen Titans/Season 1/Teen Titans Go! - S01E01.mkv")
	if info := f.svc.ShowFolderOf(ctx, id); !info.Unknown || info.Path != "" || !strings.Contains(info.Why, "also holds episodes of") {
		t.Fatalf("ShowFolderOf = %+v, want unknown", info)
	}
	if _, m, err := f.svc.MisplacedFiles(ctx, id); err != nil || len(m) != 0 {
		t.Errorf("misplaced = %+v (%v), want none", m, err)
	}
	got, err := f.svc.MisplacedShows(ctx)
	if err != nil || len(got) != 0 {
		t.Errorf("MisplacedShows = %+v (%v)", got, err)
	}
}

// Only the TV folder a folder is in names it "after the TV folder": with TV folders /tv and
// /media/Anime, /tv/Anime is a show's folder, /tv/tv isn't.
func TestFolderNamedAfterItsOwnTVFolder(t *testing.T) {
	f := newFolderFixture(t, "/tv")
	j := &folderJudge{svc: f.svc, roots: []string{filepath.FromSlash("/tv"), filepath.FromSlash("/media/Anime")}, others: func(string) []int64 { return nil }}
	if v := j.judge(filepath.FromSlash("/tv/Anime")); v.why != "" {
		t.Errorf("/tv/Anime: %s", v.why)
	}
	if v := j.judge(filepath.FromSlash("/tv/tv")); v.kind != unsoundRoot {
		t.Errorf("/tv/tv = %+v", v)
	}
}

// The library scan doesn't take a folder named like a season, right in the TV folder, for
// a show: those hold files misplaced before #56 was fixed.
func TestScanSkipsSeasonNamedFolders(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tv := t.TempDir()
	meta := shelfMeta{shows: map[int]metadata.SeriesDetails{
		1: {SeriesResult: metadata.SeriesResult{TMDBID: 1, Title: "Bluey", Year: 2020}, Seasons: listing(2)},
		2: {SeriesResult: metadata.SeriesResult{TMDBID: 2, Title: "Stagione", Year: 2020}, Seasons: listing(2)},
	}}
	svc := NewService(st.DB(), meta, tv, slog.New(slog.NewTextHandler(io.Discard, nil)))
	touchEpisode(t, filepath.Join(tv, "Bluey (2020)", "S01", "Bluey - S01E01.mkv"))
	touchEpisode(t, filepath.Join(tv, "Stagione 2", "Season 2", "Bluey - S02E01.mkv"))
	touchEpisode(t, filepath.Join(tv, "S13", "Season 12", "Bluey - S12E01.mkv"))
	res, err := svc.ScanLibrary(context.Background(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 1 || len(res.Unmatched) != 0 {
		t.Fatalf("scan = %+v, want only Bluey", res)
	}
	if _, err := svc.repo.GetByTMDB(context.Background(), 2); err == nil {
		t.Error(`the "Stagione 2" folder was added as the show "Stagione"`)
	}
}

// A season folder template is honoured: "The Pitt Season 01" is a season folder in a
// library Arrmada names "{title} Season {season00}".
func TestShowFolderOfTemplateSeasonFolders(t *testing.T) {
	f := newFolderFixture(t, "/tv")
	f.svc.SetSeasonFolderMatcher(func(name string) bool { return strings.HasPrefix(name, "The Pitt Season ") })
	id := f.show("The Pitt", "/old/The Pitt/The Pitt Season 01/The Pitt - S01E01.mkv")
	if info := f.svc.ShowFolderOf(context.Background(), id); info.Path != filepath.FromSlash("/old/The Pitt") {
		t.Fatalf("ShowFolderOf = %+v", info)
	}
}
