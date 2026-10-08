package automation

import (
	"context"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/series"
)

// A show's episode list was only ever refreshed by hand. New episodes TMDB added after
// the last refresh — a season that grew from 5 to 7 — weren't rows in the database, so
// a download of E6 resolved to an episode "the metadata doesn't have" and sat until
// someone pressed Refresh. Continuing shows are refreshed on a schedule now, and the
// import refreshes a show itself the moment a file lands on an episode it doesn't know.

// seriesRefreshPause spaces the metadata calls so a library of hundreds of shows is
// a slow trickle at TMDB, not a burst.
const seriesRefreshPause = 1500 * time.Millisecond

// seriesContinuing reports whether a show can still gain episodes. TMDB's statuses
// are "Returning Series", "In Production", "Planned", "Pilot", "Ended", "Canceled";
// unknown is treated as continuing so a show without a status isn't left stale.
func seriesContinuing(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ended", "canceled", "cancelled":
		return false
	}
	return true
}

// RefreshContinuingSeries re-pulls metadata for every monitored show that is still
// airing, so newly listed episodes exist before their downloads arrive. It never
// renumbers a show or moves a file — that takes the owner's own Refresh.
func (c *Coordinator) RefreshContinuingSeries(ctx context.Context) {
	if c.series == nil {
		return
	}
	all, err := c.series.List(ctx)
	if err != nil {
		return
	}
	refreshed, failed := 0, 0
	for i, s := range all {
		if ctx.Err() != nil {
			return
		}
		if !s.Monitored || !seriesContinuing(s.Status) {
			continue
		}
		// Never a rebuild: this runs unattended, so a numbering change is only noted in
		// History and nothing on disk moves until the owner presses Refresh.
		if _, _, err := c.series.Refresh(ctx, s.ID, series.RefreshOptions{}); err != nil {
			failed++
			c.log.Warn("series: scheduled refresh failed", "series", s.Title, "err", err)
		} else {
			refreshed++
		}
		if i < len(all)-1 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(seriesRefreshPause):
			}
		}
	}
	c.log.Info("series: scheduled metadata refresh done", "refreshed", refreshed, "failed", failed)
}
