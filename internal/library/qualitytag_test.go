package library

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// A pack states its quality once, on the folder, and names the files plainly. The
// importer parses only the filename, so {quality} rendered empty and every imported
// episode lost the " - 1080p BluRay" suffix the rest of the library carries — the naming
// became inconsistent with every other file in the show.
func TestQualityInheritedIntoTheName(t *testing.T) {
	pack := parser.Parse("Parks and Recreation (2009)  S01-S07 (1080p BDRip DDP5.1 x265) NLSUBS - DutcHY")
	if pack.Resolution != parser.Res1080p || pack.Source != parser.SourceBluray {
		t.Fatalf("premise: the pack states 1080p BluRay, got %q / %q", pack.Resolution, pack.Source)
	}

	// The file itself states neither, so it inherits both.
	file := parser.Parse("Parks and Recreation - 1x01 - Make My Pit a Park.mkv")
	if got := qualityTag(file); got != "" {
		t.Fatalf("premise: the file alone yields no quality tag, got %q", got)
	}
	merged := file
	if merged.Resolution == parser.ResUnknown {
		merged.Resolution = pack.Resolution
	}
	if merged.Source == parser.SourceUnknown {
		merged.Source = pack.Source
	}
	if got, want := qualityTag(merged), "1080p BluRay"; got != want {
		t.Errorf("qualityTag = %q, want %q", got, want)
	}
}

// A file that names its own quality is the more specific claim and must win — a pack can
// hold mixed encodes.
func TestFileQualityBeatsThePack(t *testing.T) {
	pack := parser.Parse("Show S01 (1080p BluRay x265)")
	file := parser.Parse("Show.S01E05.720p.WEB-DL.x264-GRP.mkv")
	merged := file
	if merged.Resolution == parser.ResUnknown {
		merged.Resolution = pack.Resolution
	}
	if merged.Source == parser.SourceUnknown {
		merged.Source = pack.Source
	}
	if got, want := qualityTag(merged), "720p WEB-DL"; got != want {
		t.Errorf("qualityTag = %q, want %q — the file's own quality is more specific", got, want)
	}
}
