package automation

import (
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// Packs routinely state quality ONCE, on the folder, and name the files plainly. Every
// file then parsed to unknown resolution, lost the upgrade comparison against whatever
// was already on disk, and the pack was skipped as "already has an equal-or-better file"
// — 120 of 122 Parks and Recreation episodes in one go, with the log showing an empty
// candidate_resolution on every line.
func TestQualityInheritedFromTheRelease(t *testing.T) {
	release := parser.Parse("Parks and Recreation (2009)  S01-S07 (1080p BDRip DDP5.1 x265) NLSUBS - DutcHY")
	if release.Resolution != parser.Res1080p {
		t.Fatalf("premise: release should parse 1080p, got %q", release.Resolution)
	}

	file := parser.Parse(filepath.Base("Parks and Recreation - 1x01 - Make My Pit a Park.mkv"))
	if file.Resolution != "" {
		t.Fatalf("premise: the file names no resolution, got %q", file.Resolution)
	}

	got := inheritQuality(file, release)
	if got.Resolution != parser.Res1080p {
		t.Errorf("Resolution = %q, want 1080p inherited from the release", got.Resolution)
	}
	if got.Source != release.Source {
		t.Errorf("Source = %q, want %q inherited from the release", got.Source, release.Source)
	}
	// Inheriting quality must not disturb what the file itself established.
	if got.Season != 1 || len(got.Episodes) != 1 || got.Episodes[0] != 1 {
		t.Errorf("episode identity changed: season=%d episodes=%v", got.Season, got.Episodes)
	}
}

// A pack can hold mixed quality, so a file naming its own resolution is the more specific
// claim and must win over the folder's.
func TestFileResolutionBeatsTheRelease(t *testing.T) {
	release := parser.Parse("Show S01 (1080p WEB-DL x265)")
	file := parser.Parse("Show.S01E05.720p.WEB-DL.x265.mkv")

	if got := inheritQuality(file, release); got.Resolution != parser.Res720p {
		t.Errorf("Resolution = %q, want the file's own 720p", got.Resolution)
	}
}

// Nothing to inherit must leave the file untouched rather than blanking it.
func TestInheritQualityWithBareRelease(t *testing.T) {
	file := parser.Parse("Show.S01E05.1080p.WEB-DL.mkv")
	got := inheritQuality(file, parser.Parse("some folder"))
	if got.Resolution != parser.Res1080p {
		t.Errorf("Resolution = %q, want the file's 1080p preserved", got.Resolution)
	}
}
