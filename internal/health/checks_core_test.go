package health

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(c Check) []Finding { return c.Run(context.Background()) }

// No enabled indexers is an error linking to Indexers; a read error says nothing.
func TestIndexersCheck(t *testing.T) {
	got := run(IndexersCheck(func(context.Context) (int, error) { return 0, nil }))
	if len(got) != 1 || got[0].Key != "indexers.none" || got[0].Level != LevelError || got[0].Fix != FixIndexers {
		t.Errorf("no indexers: %+v", got)
	}
	if got := run(IndexersCheck(func(context.Context) (int, error) { return 2, nil })); len(got) != 0 {
		t.Errorf("two indexers: %+v", got)
	}
	if got := run(IndexersCheck(func(context.Context) (int, error) { return 0, errors.New("db") })); len(got) != 0 {
		t.Errorf("read error: %+v", got)
	}
}

func TestDownloadsCheck(t *testing.T) {
	none := run(DownloadsCheck(func(context.Context) (int, error) { return 0, nil }, func(context.Context) error { return nil }))
	if len(none) != 1 || none[0].Key != "downloads.client.none" || none[0].Fix != FixDownloadClients {
		t.Errorf("no client: %+v", none)
	}
	down := run(DownloadsCheck(func(context.Context) (int, error) { return 1, nil }, func(context.Context) error { return errors.New("refused") }))
	if len(down) != 1 || down[0].Key != "downloads.reachable" || !strings.Contains(down[0].Message, "refused") {
		t.Errorf("unreachable: %+v", down)
	}
}

// The disk guard holding torrents is a warning that links to its settings.
func TestDiskGuardCheck(t *testing.T) {
	got := run(DiskGuardCheck(func(context.Context) (DiskGuardState, bool) {
		return DiskGuardState{Enabled: true, UsedPct: 96.4, PausePct: 95, ResumePct: 90, Holding: 3}, true
	}))
	if len(got) != 1 || got[0].Level != LevelWarning || got[0].Fix != FixDiskGuard ||
		!strings.Contains(got[0].Message, "96.4% full") || !strings.Contains(got[0].Message, "3 torrents") {
		t.Errorf("holding: %+v", got)
	}
	if got := run(DiskGuardCheck(func(context.Context) (DiskGuardState, bool) {
		return DiskGuardState{Enabled: true, Holding: 0}, true
	})); len(got) != 0 {
		t.Errorf("not holding: %+v", got)
	}
}

func TestDiskFreeCheck(t *testing.T) {
	at := func(gb float64) []Finding {
		return run(diskFreeCheck(func() string { return "/dl" }, func(string) (float64, bool) { return gb, true }))
	}
	if got := at(1.5); len(got) != 1 || got[0].Level != LevelError {
		t.Errorf("1.5 GB: %+v", got)
	}
	if got := at(5); len(got) != 1 || got[0].Level != LevelWarning {
		t.Errorf("5 GB: %+v", got)
	}
	if got := at(50); len(got) != 0 {
		t.Errorf("50 GB: %+v", got)
	}
}

func TestAudiobookServerCheck(t *testing.T) {
	got := run(AudiobookServerCheck(func(context.Context) (bool, bool, string) { return true, false, "port 13378 is taken" }))
	if len(got) != 1 || got[0].Level != LevelError || !strings.Contains(got[0].Message, "port 13378") {
		t.Errorf("on but down: %+v", got)
	}
	if got := run(AudiobookServerCheck(func(context.Context) (bool, bool, string) { return false, false, "" })); len(got) != 0 {
		t.Errorf("off: %+v", got)
	}
}

// The library check judges each folder it's given, never creates one, and flags a folder
// inside the data folder without probing it.
func TestLibraryFoldersCheck(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	movies := filepath.Join(base, "unmounted", "movies") // parent missing: not mounted
	tv := filepath.Join(base, "tv")                      // missing, parent writable
	music := filepath.Join(data, "music")                // inside the data folder
	for _, d := range []string{data} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st := LibraryState{
		Folders: []Folder{{Role: "movies", Label: "Movies", Path: movies}, {Role: "tv", Label: "TV", Path: tv}, {Role: "music", Label: "Music", Path: music}},
		All:     []Folder{{Role: "movies", Label: "Movies", Path: movies}, {Role: "tv", Label: "TV", Path: tv}, {Role: "music", Label: "Music", Path: music}},
		DataDir: data,
	}
	got := run(LibraryFoldersCheck(func(context.Context) LibraryState { return st }, NewProbeCache(0)))
	byKey := map[string]Finding{}
	for _, f := range got {
		byKey[f.Key] = f
	}
	if f := byKey["library.movies"]; f.Level != LevelError || !strings.Contains(f.Message, "isn't mounted") {
		t.Errorf("unmounted movies: %+v", f)
	}
	if f := byKey["library.tv"]; f.Level != LevelWarning || !strings.Contains(f.Message, "doesn't exist yet") || f.Fix != FixLibraryFolders {
		t.Errorf("missing tv: %+v", f)
	}
	if f := byKey["library.music"]; f.Level != LevelError || !strings.Contains(f.Message, "data folder") {
		t.Errorf("music in the data dir: %+v", f)
	}
	for _, p := range []string{tv, filepath.Dir(movies), music} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("the check created %s", p)
		}
	}
}

func TestBackupsCheck(t *testing.T) {
	if got := run(BackupsCheck(func(context.Context) string { return "No database backup in 3 days" })); len(got) != 1 || got[0].Fix != FixBackups {
		t.Errorf("stale backups: %+v", got)
	}
	if got := run(BackupsCheck(func(context.Context) string { return "" })); len(got) != 0 {
		t.Errorf("fresh backups: %+v", got)
	}
}
