package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/series"
)

// A search for "Goliath" returned "House of David S01E07 David and Goliath - Part 1" —
// the indexer matched the word in an EPISODE title. The upgrade sweep matched candidates
// on season/episode numbers alone, S01E07 lined up with Goliath's own S01E07, and the
// wrong show was grabbed and imported over the real episode.
func TestUpgradeCandidatesMustBeTheRightShow(t *testing.T) {
	goliath := series.Series{Title: "Goliath", Year: 2016}

	wrong := "House of David S01E07 David and Goliath - Part 1 1080p AMZN WEB-DL DDP5 1 H 264-FLUX"
	if seriesTitleMatches(wrong, goliath) {
		t.Errorf("%q must not pass as Goliath — the show is House of David", wrong)
	}
	// The numbers alone genuinely do line up, which is why the title gate is the only
	// thing standing between this release and the wrong library folder.
	if !episodeRelease(parser.Parse(wrong), 1, 7) {
		t.Fatal("precondition: the release really does parse as S01E07, or the test proves nothing")
	}

	// The real show still passes.
	for _, ok := range []string{
		"Goliath S01E07 1080p AMZN WEB-DL DDP5 1 H 264-NTb",
		"Goliath.2016.S01E07.1080p.WEBRip.x264-GRP",
	} {
		if !seriesTitleMatches(ok, goliath) {
			t.Errorf("%q is Goliath and must pass", ok)
		}
	}
}
