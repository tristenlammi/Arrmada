package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/download"
)

// A seeding torrent is finished. Counting it as "in flight" froze a show out of the
// missing sweep, RSS sync and the upgrade sweep for the whole seeding period — 22 hours in
// the case that surfaced this — so an episode that aired inside that window was never
// picked up automatically at all.
func TestSeriesInFlightIgnoresFinishedTorrents(t *testing.T) {
	seeding := download.Item{
		Name: "Lioness.2023.S03E01.1080p.WEB.h264-ETHEL", Category: seriesCategory,
		Progress: 1, State: "seeding",
	}
	downloading := download.Item{
		Name: "Lioness.2023.S03E02.1080p.WEB.h264-ETHEL", Category: seriesCategory,
		Progress: 0.42, State: "downloading",
	}

	if got := seriesInFlight([]download.Item{seeding}, "Lioness"); got != "" {
		t.Errorf("a seeding torrent must not block the series, got %q", got)
	}
	// Completed but not yet imported is equally finished: the bytes are already on disk,
	// so grabbing something else can't stack a duplicate download.
	done := seeding
	done.State = "completed"
	if got := seriesInFlight([]download.Item{done}, "Lioness"); got != "" {
		t.Errorf("a completed torrent must not block the series, got %q", got)
	}

	// The actual purpose of the check still holds.
	if got := seriesInFlight([]download.Item{downloading}, "Lioness"); got != downloading.Name {
		t.Errorf("an in-progress grab must block and be named, got %q", got)
	}
	if got := seriesInFlight([]download.Item{seeding, downloading}, "Lioness"); got != downloading.Name {
		t.Errorf("a seeding torrent must not mask a real one, got %q", got)
	}

	// Another show's download, and a non-series category, are both irrelevant.
	other := downloading
	other.Name = "Andor.S02E01.1080p.WEB-DL-NTb"
	if got := seriesInFlight([]download.Item{other}, "Lioness"); got != "" {
		t.Errorf("another show must not block, got %q", got)
	}
	movie := downloading
	movie.Category = "arrmada"
	if got := seriesInFlight([]download.Item{movie}, "Lioness"); got != "" {
		t.Errorf("a non-series category must not block, got %q", got)
	}
}
