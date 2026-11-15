package automation

import (
	"context"
	"errors"
	"strings"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/series"
)

// Deleting a show, or one season's files, settles its downloads the way a movie's delete
// does (#111): its in-flight grabs are set aside first, and once the delete succeeded,
// cancelled in the client with their partial data when asked. A season only touches grabs
// made for exactly that season; a pack of several seasons, the complete show or an
// absolute-numbered release is never cancelled for one season.

// PendingSeriesDownload is an in-flight grab for a show, for the delete dialog to name.
type PendingSeriesDownload struct {
	Hash     string  `json:"hash"`
	Title    string  `json:"title"`
	Progress float64 `json:"progress"` // 0..1; 0 when the client doesn't list it
	// Season is the one season it was grabbed for; -1 when it covers more (several
	// seasons, the complete show, absolute numbering, or unknown).
	Season int `json:"season"`
}

type pendingSeriesGrab struct {
	id     int64
	hash   string
	title  string
	season int // -1: more than one season
}

// pendingSeriesGrabs lists a show's 'grabbed' grabs; season >= 0 keeps only the ones made
// for exactly that season.
func (c *Coordinator) pendingSeriesGrabs(ctx context.Context, seriesID int64, season int) ([]pendingSeriesGrab, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, info_hash, title, acq_scope FROM grabs WHERE status = ? AND media_type = 'series' AND movie_id = ? ORDER BY id`,
		grabStatusGrabbed, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []pendingSeriesGrab
	var scopes []string
	for rows.Next() {
		var g pendingSeriesGrab
		var scope string
		if err := rows.Scan(&g.id, &g.hash, &g.title, &scope); err != nil {
			return nil, err
		}
		all = append(all, g)
		scopes = append(scopes, scope)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var show *series.Series
	out := make([]pendingSeriesGrab, 0, len(all))
	for i, g := range all {
		scope := scopes[i]
		if scope == "" && c.series != nil {
			// Older rows have no stamped scope: read it from the release name, as the
			// in-flight check does.
			if show == nil {
				if s, err := c.series.Get(ctx, seriesID); err == nil {
					show = &s
				}
			}
			if show != nil {
				scope = seriesAcqScope(g.title, *show)
			}
		}
		g.season = -1
		if sn, whole := scopeSeason(scope); !whole {
			g.season = sn
		}
		if season >= 0 && g.season != season {
			continue
		}
		out = append(out, g)
	}
	return out, nil
}

// PendingSeriesDownloads lists a show's in-flight grabs (season >= 0: only that season's
// own) with live progress from the download client when it answers.
func (c *Coordinator) PendingSeriesDownloads(ctx context.Context, seriesID int64, season int) ([]PendingSeriesDownload, error) {
	grabs, err := c.pendingSeriesGrabs(ctx, seriesID, season)
	if err != nil {
		return nil, err
	}
	out := make([]PendingSeriesDownload, 0, len(grabs))
	if len(grabs) == 0 {
		return out, nil
	}
	progress := map[string]float64{}
	if c.downloads != nil {
		if q, err := c.downloads.Queue(ctx); err == nil {
			for _, it := range q {
				progress[strings.ToLower(it.Hash)] = it.Progress
			}
		}
	}
	for _, g := range grabs {
		out = append(out, PendingSeriesDownload{Hash: g.hash, Title: g.title, Progress: progress[strings.ToLower(g.hash)], Season: g.season})
	}
	return out, nil
}

// cancelSeriesGrabs removes the grabs' torrents with their data and closes them as
// cancelled. A grab without a usable hash stays orphaned rather than guessing.
func (c *Coordinator) cancelSeriesGrabs(ctx context.Context, grabs []pendingSeriesGrab) {
	remove := c.removeTorrent
	if remove == nil && c.downloads != nil {
		remove = c.downloads.Remove
	}
	for _, g := range grabs {
		if remove == nil || !download.ValidKey(g.hash) {
			continue
		}
		if err := remove(ctx, g.hash, true); err != nil {
			c.log.Warn("series delete: couldn't cancel its download — left it in the client", "release", g.title, "err", err)
			continue
		}
		c.setGrabStatus(ctx, g.id, grabStatusCancelled)
	}
}

// DeleteSeriesOpts is what the show's delete dialog chose.
type DeleteSeriesOpts struct {
	DeleteFiles     bool
	CancelDownloads bool
}

// DeleteSeries deletes a show and settles its downloads: in-flight grabs are orphaned
// first, then the show is deleted (files to the bin first, stopping at a refusal), and
// only then are the torrents cancelled when asked. If the delete fails the grabs go back
// to 'grabbed' and no torrent was touched. A torrent left in the client finishes into
// Reviews, never imported by name.
func (c *Coordinator) DeleteSeries(ctx context.Context, id int64, opts DeleteSeriesOpts) (series.DeleteSummary, error) {
	if c.series == nil {
		return series.DeleteSummary{}, errors.New("the series module isn't ready")
	}
	if _, err := c.series.Get(ctx, id); err != nil {
		return series.DeleteSummary{}, err
	}
	grabs, err := c.pendingSeriesGrabs(ctx, id, -1)
	if err != nil {
		return series.DeleteSummary{}, err
	}
	for _, g := range grabs {
		c.setGrabStatus(ctx, g.id, grabStatusOrphaned)
	}
	sum, err := c.series.Delete(ctx, id, opts.DeleteFiles)
	if err != nil {
		for _, g := range grabs {
			c.setGrabStatus(ctx, g.id, grabStatusGrabbed)
		}
		return sum, err
	}
	if opts.CancelDownloads {
		c.cancelSeriesGrabs(ctx, grabs)
	}
	c.log.Info("series deleted", "series_id", id, "files", opts.DeleteFiles, "downloads_pending", len(grabs), "cancel", opts.CancelDownloads)
	return sum, nil
}

// DeleteSeasonOpts is what the season's delete dialog chose.
type DeleteSeasonOpts struct {
	// Unmonitor stops wanting the season, so the next search doesn't download it again.
	Unmonitor       bool
	CancelDownloads bool // only grabs made for exactly this season
}

// DeleteSeason moves one season's files to the recycle bin (series.DeleteSeasonFiles),
// optionally stops monitoring it, and cancels the season's own downloads when asked. The
// season is unmonitored first, so a search can't pick it up in between; if no file could
// be moved, its monitoring is put back.
func (c *Coordinator) DeleteSeason(ctx context.Context, id int64, season int, opts DeleteSeasonOpts) (series.DeleteSummary, error) {
	if c.series == nil {
		return series.DeleteSummary{}, errors.New("the series module isn't ready")
	}
	s, err := c.series.Get(ctx, id)
	if err != nil {
		return series.DeleteSummary{}, err
	}
	wasMonitored, found := false, false
	for _, sn := range s.Seasons {
		if sn.SeasonNumber == season {
			wasMonitored, found = sn.Monitored, true
		}
	}
	if !found {
		return series.DeleteSummary{}, series.ErrNoSuchSeason
	}
	grabs, err := c.pendingSeriesGrabs(ctx, id, season)
	if err != nil {
		return series.DeleteSummary{}, err
	}
	if opts.Unmonitor && wasMonitored {
		if err := c.series.SetSeasonMonitored(ctx, id, int64(season), false); err != nil {
			return series.DeleteSummary{}, err
		}
	}
	for _, g := range grabs {
		c.setGrabStatus(ctx, g.id, grabStatusOrphaned)
	}
	sum, err := c.series.DeleteSeasonFiles(ctx, id, season)
	if err != nil {
		// Stopped partway or never started: its downloads carry on as they were, and a
		// season nothing went from is wanted again as before.
		for _, g := range grabs {
			c.setGrabStatus(ctx, g.id, grabStatusGrabbed)
		}
		if len(sum.Moved) > 0 {
			return sum, err
		}
		if opts.Unmonitor && wasMonitored {
			if rerr := c.series.SetSeasonMonitored(ctx, id, int64(season), true); rerr != nil {
				c.log.Warn("season delete: couldn't put its monitoring back", "series_id", id, "season", season, "err", rerr)
			}
		}
		return sum, err
	}
	if opts.CancelDownloads {
		c.cancelSeriesGrabs(ctx, grabs)
	}
	c.log.Info("season files deleted", "series_id", id, "season", season, "moved", len(sum.Moved), "unmonitor", opts.Unmonitor, "cancel", opts.CancelDownloads)
	return sum, nil
}
