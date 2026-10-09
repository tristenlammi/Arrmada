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

// endedRecheckEvery is how often a monitored show stored as ended or cancelled is
// re-checked: often enough that a revived show gains its new season within a week,
// rarely enough that a big back catalogue costs the provider little.
const endedRecheckEvery = 7 * 24 * time.Hour

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

// continuingDue picks the monitored shows still airing — every one, every run.
func continuingDue(all []series.Series) []series.Series {
	var out []series.Series
	for _, s := range all {
		if s.Monitored && seriesContinuing(s.Status) {
			out = append(out, s)
		}
	}
	return out
}

// endedDue picks the monitored ended or cancelled shows not refreshed in the last week
// (or never). A stored status is only what TMDB said last time: shows get revived, and
// one skipped forever never gained the season that brought it back.
func endedDue(all []series.Series, now time.Time) []series.Series {
	var out []series.Series
	for _, s := range all {
		if !s.Monitored || seriesContinuing(s.Status) {
			continue
		}
		last, err := time.ParseInLocation("2006-01-02 15:04:05", s.LastRefreshedAt, time.UTC)
		if err != nil || now.Sub(last) >= endedRecheckEvery {
			out = append(out, s) // never refreshed ("" fails to parse) or due again
		}
	}
	return out
}

// RefreshContinuingSeries re-pulls metadata for every monitored show that is still
// airing, so newly listed episodes exist before their downloads arrive. It never
// renumbers a show or moves a file.
func (c *Coordinator) RefreshContinuingSeries(ctx context.Context) {
	if c.series == nil {
		return
	}
	all, err := c.series.List(ctx)
	if err != nil {
		return
	}
	refreshed, failed := c.refreshSeriesList(ctx, continuingDue(all))
	c.log.Info("series: scheduled metadata refresh done", "refreshed", refreshed, "failed", failed)
}

// RefreshEndedSeries re-checks monitored ended and cancelled shows once a week, so a
// revived show reads as returning again and its new season appears without anyone
// pressing Refresh. Like the continuing refresh, it never renumbers or moves a file.
func (c *Coordinator) RefreshEndedSeries(ctx context.Context) {
	if c.series == nil {
		return
	}
	all, err := c.series.List(ctx)
	if err != nil {
		return
	}
	due := endedDue(all, time.Now().UTC())
	if len(due) == 0 {
		return
	}
	refreshed, failed := c.refreshSeriesList(ctx, due)
	c.log.Info("series: weekly re-check of ended shows done", "refreshed", refreshed, "failed", failed)
}

// refreshSeriesList refreshes each show in turn, paced so the provider sees a trickle.
func (c *Coordinator) refreshSeriesList(ctx context.Context, list []series.Series) (refreshed, failed int) {
	for i, s := range list {
		if ctx.Err() != nil {
			return refreshed, failed
		}
		// Never a rebuild: this runs unattended, so a numbering change is only noted in
		// History and nothing on disk moves until the owner applies it.
		if _, _, err := c.series.Refresh(ctx, s.ID, series.RefreshOptions{}); err != nil {
			failed++
			c.log.Warn("series: scheduled refresh failed", "series", s.Title, "err", err)
		} else {
			refreshed++
		}
		if i < len(list)-1 {
			select {
			case <-ctx.Done():
				return refreshed, failed
			case <-time.After(seriesRefreshPause):
			}
		}
	}
	return refreshed, failed
}
