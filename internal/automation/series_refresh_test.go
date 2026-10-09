package automation

import (
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/series"
)

func TestSeriesContinuing(t *testing.T) {
	for status, want := range map[string]bool{
		"Returning Series": true, "In Production": true, "Planned": true, "": true,
		"Ended": false, "Canceled": false, "cancelled": false, " ended ": false,
	} {
		if got := seriesContinuing(status); got != want {
			t.Errorf("seriesContinuing(%q) = %v, want %v", status, got, want)
		}
	}
}

// Ended shows used to be skipped forever, so a revived show never gained its new season.
// They're re-checked once a week now; the continuing refresh is unchanged.
func TestRefreshContinuingIncludesStaleEndedShows(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	all := []series.Series{
		{ID: 1, Monitored: true, Status: "Returning Series"},
		{ID: 2, Monitored: true, Status: "Ended", LastRefreshedAt: ""},                       // never: due
		{ID: 3, Monitored: true, Status: "Ended", LastRefreshedAt: "2026-10-01 11:00:00"},    // 8 days: due
		{ID: 4, Monitored: true, Status: "Canceled", LastRefreshedAt: "2026-10-05 12:00:00"}, // 4 days: not yet
		{ID: 5, Monitored: false, Status: "Ended"},                                           // paused: never
		{ID: 6, Monitored: true, Status: ""},                                                 // unknown counts as continuing
	}
	ids := func(list []series.Series) []int64 {
		var out []int64
		for _, s := range list {
			out = append(out, s.ID)
		}
		return out
	}
	if got := ids(continuingDue(all)); len(got) != 2 || got[0] != 1 || got[1] != 6 {
		t.Errorf("continuing = %v, want [1 6]", got)
	}
	if got := ids(endedDue(all, now)); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Errorf("ended due = %v, want [2 3]", got)
	}
}
