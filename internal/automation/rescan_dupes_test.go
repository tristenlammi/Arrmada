package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
)

// When several files on disk claim one episode — three copies of every Solo Leveling
// season 2 episode, left by re-imports under different names — the rescan must keep the
// best one deterministically, not whichever the directory walk happened to yield last.
func TestBestLibraryVideoPicksHighestQuality(t *testing.T) {
	files := []library.FoundVideo{
		{Path: "/tv/Show/Season 2/Show - S02E01 - 720p WEB-DL.mkv", Size: 900},
		{Path: "/tv/Show/Season 2/Show - S02E01 - 1080p WEB-DL.mkv", Size: 800},
		{Path: "/tv/Show/Season 2/Show - S02E01 - 480p.mkv", Size: 5000},
	}
	// Resolution wins over raw size: the 5 GB 480p rip must not beat the 1080p.
	if got := bestLibraryVideo(files); got.Path != "/tv/Show/Season 2/Show - S02E01 - 1080p WEB-DL.mkv" {
		t.Errorf("want the 1080p file, got %q", got.Path)
	}

	// At equal resolution the bigger file (higher bitrate) wins.
	same := []library.FoundVideo{
		{Path: "/tv/a - 1080p.mkv", Size: 1000},
		{Path: "/tv/b - 1080p.mkv", Size: 3000},
	}
	if got := bestLibraryVideo(same); got.Path != "/tv/b - 1080p.mkv" {
		t.Errorf("want the larger 1080p file, got %q", got.Path)
	}

	// Fully tied → lowest path, so repeated rescans don't flip the recorded file back
	// and forth (which would churn the DB and any downstream rename).
	tied := []library.FoundVideo{
		{Path: "/tv/z - 1080p.mkv", Size: 1000},
		{Path: "/tv/a - 1080p.mkv", Size: 1000},
	}
	if got := bestLibraryVideo(tied); got.Path != "/tv/a - 1080p.mkv" {
		t.Errorf("a tie must resolve deterministically to the lowest path, got %q", got.Path)
	}
	// And it must be stable whichever order the walk produced them in.
	if bestLibraryVideo(tied).Path != bestLibraryVideo([]library.FoundVideo{tied[1], tied[0]}).Path {
		t.Error("the pick must not depend on walk order")
	}

	// A single file is simply itself.
	one := []library.FoundVideo{{Path: "/tv/only.mkv", Size: 1}}
	if bestLibraryVideo(one).Path != "/tv/only.mkv" {
		t.Error("a lone file should be returned unchanged")
	}
}
