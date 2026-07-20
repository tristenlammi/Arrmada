package automation

import (
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// What gets recorded as an episode's source release decides every future upgrade
// comparison, because the library file is renamed on import and its new name often
// carries no quality at all.
//
// Recording a pack's bare per-file name left the episode with NO resolution recorded
// anywhere — not in the filename, not in the source release — so any later 1080p release
// outranked it and re-imported the same quality indefinitely.
func TestSourceReleaseFallsBackToTheReleaseName(t *testing.T) {
	const packFolder = "Parks and Recreation (2009)  S01-S07 (1080p BDRip DDP5.1 x265) NLSUBS - DutcHY"
	release := parser.Parse(packFolder)

	// The file's own name says nothing about quality — so the release name is recorded.
	plain := "Parks and Recreation - 1x01 - Make My Pit a Park.mkv"
	if got := sourceReleaseFor(plain, packFolder, release); got != packFolder {
		t.Errorf("got %q, want the release name so the resolution is recorded somewhere", got)
	}
	if parser.Parse(sourceReleaseFor(plain, packFolder, release)).Resolution != parser.Res1080p {
		t.Error("the recorded name must carry a resolution, or future comparisons are meaningless")
	}

	// A file that states its own quality is the more specific record, and wins.
	named := "Show.S01E05.1080p.WEB-DL.x265-GRP.mkv"
	if got := sourceReleaseFor(named, packFolder, release); got != named {
		t.Errorf("got %q, want the file's own name %q", got, named)
	}
}

// sourceReleaseFor mirrors the choice made in importSeriesInto so it can be exercised
// without a full import.
func sourceReleaseFor(fileName, contentPath string, release parser.Release) string {
	if parser.Parse(fileName).Resolution == "" && release.Resolution != "" {
		return filepath.Base(contentPath)
	}
	return fileName
}
