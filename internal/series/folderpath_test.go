package series

import (
	"context"
	"path/filepath"
	"testing"
)

func TestFolderPathSeasonAndFlat(t *testing.T) {
	for _, c := range []struct{ file, want string }{
		{"/tv/The Bear (2022)/Season 01/The Bear - S01E01.mkv", "/tv/The Bear (2022)"},
		{"/tv/The Bear (2022)/Specials/The Bear - S00E01.mkv", "/tv/The Bear (2022)"},
		{"/tv/The Bear (2022)/The Bear - S01E01.mkv", "/tv/The Bear (2022)"}, // flat layout
		{"/tv/Season 4 Show/Ep.mkv", "/tv/Season 4 Show"},                    // only exact season names
	} {
		if got := ShowFolder(filepath.FromSlash(c.file)); got != filepath.FromSlash(c.want) {
			t.Errorf("ShowFolder(%q) = %q, want %q", c.file, got, c.want)
		}
	}

	f := newDeleteFixture(t)
	if got := f.svc.FolderPath(context.Background(), f.id); got != "" {
		t.Fatalf("no files yet: FolderPath = %q", got)
	}
	f.episode(t, 1)
	if got := f.svc.FolderPath(context.Background(), f.id); got != f.show {
		t.Fatalf("FolderPath = %q, want %q", got, f.show)
	}
}

// Deleting an episode file or a whole show with its files tells Plex's scanner which show
// folder changed; deleting a show but keeping its files tells it nothing.
func TestDeletesReportTheShowFolder(t *testing.T) {
	f := newDeleteFixture(t)
	var got []string
	f.svc.SetLibraryChanged(func(kind, dir string) { got = append(got, kind+"|"+dir) })
	f.episode(t, 1)
	f.episode(t, 2)

	if err := f.svc.DeleteEpisodeFile(context.Background(), f.id, 1, 1); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "show|"+f.show {
		t.Fatalf("episode delete reported %v", got)
	}
	got = nil
	if _, err := f.svc.Delete(context.Background(), f.id, true); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "show|"+f.show {
		t.Fatalf("show delete reported %v", got)
	}
}
