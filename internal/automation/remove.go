package automation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/series"
)

// RemoveMode is what the user picked when removing a download from the client.
type RemoveMode string

const (
	// RemoveKeepFiles takes the torrent out of the client and leaves its files on disk.
	// The default: an un-imported download may be the only copy there is.
	RemoveKeepFiles RemoveMode = "keep_files"
	// RemoveDeleteFiles takes the torrent out and deletes what it downloaded.
	RemoveDeleteFiles RemoveMode = "delete_files"
	// RemoveBlock deletes it, blocklists the release and searches for another.
	RemoveBlock RemoveMode = "block"
)

// ValidRemoveMode reports whether m is one of the modes above.
func ValidRemoveMode(m RemoveMode) bool {
	return m == RemoveKeepFiles || m == RemoveDeleteFiles || m == RemoveBlock
}

// RemoveResult says what a removed download was for, so the UI can name it.
type RemoveResult struct {
	Kind  string     `json:"kind,omitempty"` // movie | series | book | music; "" when untracked
	ID    int64      `json:"id,omitempty"`
	Title string     `json:"title,omitempty"`
	Mode  RemoveMode `json:"mode"`
	// Unmonitored describes what "stop wanting" switched off ("the movie", "season 2",
	// "S02E05"), or why nothing was ("couldn't tell which episodes").
	Unmonitored string `json:"unmonitored,omitempty"`
}

// RemoveDownload is the Downloads page's remove action. It removes the torrent the way
// the user chose and closes out its grab as 'removed' — not 'failed' — so stall detection
// doesn't read the vanished torrent as stalled and quietly blocklist the release and grab
// another. Optionally it stops wanting the title, scoped to exactly what the download was
// for (one album, one season, the episodes named): never the whole show.
func (c *Coordinator) RemoveDownload(ctx context.Context, hash, name string, mode RemoveMode, unmonitor bool) (RemoveResult, error) {
	if !download.ValidHash(hash) {
		return RemoveResult{}, download.ErrInvalidHash
	}
	if !ValidRemoveMode(mode) {
		return RemoveResult{}, fmt.Errorf("unknown remove mode %q", mode)
	}
	g, status := c.grabForDownload(ctx, hash, name)
	res := c.describeGrab(ctx, g)
	res.Mode = mode
	release := name
	if g != nil && release == "" {
		release = g.Title
	}

	if mode == RemoveBlock {
		// Blocking already removes with data, marks the grab failed and finds another —
		// the only path here where something is grabbed again on the user's behalf. A
		// download tied to nothing is left alone (ErrNothingToBlock).
		t, err := c.BlockRelease(ctx, hash, release)
		if t.Kind != "" {
			res.Kind, res.ID, res.Title = t.Kind, t.ID, t.Title
		}
		return res, err
	}

	remove := c.removeTorrent
	if remove == nil {
		remove = c.downloads.Remove
	}
	deleteFiles := mode == RemoveDeleteFiles
	if err := remove(ctx, hash, deleteFiles); err != nil {
		return res, err
	}
	if g != nil && (status == grabStatusGrabbed || status == grabStatusHeld) {
		c.setGrabStatus(ctx, g.ID, grabStatusRemoved)
	}
	// A download held in Review (one grabbed for a movie since deleted, say) is settled
	// once it's out of the client; left pending, the review would offer an import of
	// files that may be gone.
	if _, err := c.db.ExecContext(ctx,
		`UPDATE import_reviews SET status = 'resolved', resolution = ?, resolved_at = CURRENT_TIMESTAMP
		  WHERE lower(hash) = lower(?) AND status = 'pending'`, ResolutionRemoved, hash); err != nil {
		c.log.Warn("downloads: couldn't settle the review for a removed download", "hash", hash, "err", err)
	} else {
		c.reviewsChanged()
	}
	what := "files kept"
	if deleteFiles {
		what = "files deleted"
	}
	c.addMediaEvent(ctx, g, "removed", fmt.Sprintf("Removed from the download client by you: %s (%s)", release, what))
	if unmonitor && g != nil {
		res.Unmonitored = c.stopWanting(ctx, g, release)
	}
	return res, nil
}

// grabForDownload finds the grab a download came from: by info hash (newest row wins),
// else by release name among grabs still in play. Returns nil when it's untracked.
func (c *Coordinator) grabForDownload(ctx context.Context, hash, name string) (*grab, string) {
	g, err := c.grabForHash(ctx, hash)
	if err == nil {
		return &g, g.Status
	}
	if !errors.Is(err, sql.ErrNoRows) || name == "" {
		return nil, ""
	}
	live, err := c.liveGrabs(ctx)
	if err != nil {
		return nil, ""
	}
	want := normRelease(name)
	for i := len(live) - 1; i >= 0; i-- { // newest first
		if normRelease(live[i].Title) == want {
			return &live[i], live[i].Status
		}
	}
	return nil, ""
}

// describeGrab names what a grab was for.
func (c *Coordinator) describeGrab(ctx context.Context, g *grab) RemoveResult {
	if g == nil {
		return RemoveResult{}
	}
	res := RemoveResult{Kind: g.MediaType, ID: g.MovieID, Title: g.Title}
	switch g.MediaType {
	case "movie":
		if c.movies != nil {
			if m, err := c.movies.Get(ctx, g.MovieID); err == nil {
				res.Title = m.Title
			}
		}
	case "series":
		if c.series != nil {
			if s, err := c.series.Get(ctx, g.MovieID); err == nil {
				res.Title = s.Title
			}
		}
	case "book":
		if c.books != nil {
			if b, err := c.books.Get(ctx, g.MovieID); err == nil {
				res.Title = b.Title
			}
		}
	case "music":
		if c.music != nil {
			if a, err := c.music.GetAlbum(ctx, g.MovieID); err == nil {
				res.Title = a.Title
			}
		}
	}
	return res
}

// addMediaEvent puts a line in the linked title's history.
func (c *Coordinator) addMediaEvent(ctx context.Context, g *grab, event, detail string) {
	if g == nil {
		return
	}
	switch g.MediaType {
	case "movie":
		if c.movies != nil {
			c.movies.AddEvent(ctx, g.MovieID, event, detail)
		}
	case "series":
		if c.series != nil {
			c.series.AddEvent(ctx, g.MovieID, event, detail)
		}
	case "book":
		if c.books != nil {
			c.books.AddEvent(ctx, g.MovieID, event, detail)
		}
	case "music":
		// Music history lives on the artist.
		if c.music != nil {
			if a, err := c.music.GetAlbum(ctx, g.MovieID); err == nil {
				c.music.AddEvent(ctx, a.ArtistID, event, detail)
			}
		}
	}
}

// stopWanting unmonitors exactly what the download was for and says what that was.
func (c *Coordinator) stopWanting(ctx context.Context, g *grab, release string) string {
	var err error
	var what string
	switch g.MediaType {
	case "movie":
		if c.movies == nil {
			return ""
		}
		what, err = "the movie", c.movies.SetMonitored(ctx, g.MovieID, false)
	case "book":
		if c.books == nil {
			return ""
		}
		what, err = "the book", c.books.SetMonitored(ctx, g.MovieID, false)
	case "music":
		if c.music == nil {
			return ""
		}
		what, err = "the album", c.music.SetAlbumMonitored(ctx, g.MovieID, false)
	case "series":
		what, err = c.unmonitorSeriesRelease(ctx, g.MovieID, release)
	default:
		return ""
	}
	if err != nil {
		c.log.Warn("downloads: stop wanting failed", "kind", g.MediaType, "id", g.MovieID, "err", err)
		return "couldn't stop wanting it: " + err.Error()
	}
	return what
}

// unmonitorSeriesRelease unmonitors the episodes a series release covers: the named
// episodes, or each season of a season pack. A release whose scope can't be read — or a
// complete-series pack — unmonitors nothing: switching off a whole show because one
// download was removed would be a far bigger change than anyone asked for.
func (c *Coordinator) unmonitorSeriesRelease(ctx context.Context, seriesID int64, release string) (string, error) {
	if c.series == nil {
		return "", nil
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return "", err
	}
	p := parser.Parse(release)
	if p.Complete {
		return "nothing — a complete-series pack would mean the whole show", nil
	}
	byNumber := map[[2]int]series.Episode{}
	byAbsolute := map[int]series.Episode{}
	for _, sn := range s.Seasons {
		for _, e := range sn.Episodes {
			byNumber[[2]int{e.SeasonNumber, e.EpisodeNumber}] = e
			if e.AbsoluteNumber > 0 {
				byAbsolute[e.AbsoluteNumber] = e
			}
		}
	}
	var eps []series.Episode
	switch {
	case len(p.Episodes) > 0:
		for _, n := range p.Episodes {
			if e, ok := byNumber[[2]int{p.Season, n}]; ok {
				eps = append(eps, e)
			}
		}
	case len(p.AbsoluteEpisodes) > 0 && s.IsAnime():
		for _, n := range p.AbsoluteEpisodes {
			if e, ok := byAbsolute[n]; ok {
				eps = append(eps, e)
			}
		}
	default:
		seasons := p.Seasons
		if len(seasons) == 0 && p.Season > 0 {
			seasons = []int{p.Season}
		}
		if len(seasons) == 0 {
			return "nothing — couldn't tell which episodes the release covered", nil
		}
		var names []string
		for _, n := range seasons {
			if err := c.series.SetSeasonMonitored(ctx, seriesID, int64(n), false); err != nil {
				return "", err
			}
			names = append(names, fmt.Sprint(n))
		}
		label := "season "
		if len(names) > 1 {
			label = "seasons "
		}
		return label + strings.Join(names, ", "), nil
	}
	if len(eps) == 0 {
		return "nothing — couldn't match the release to episodes", nil
	}
	var names []string
	for _, e := range eps {
		if err := c.series.SetEpisodeMonitored(ctx, e.ID, false); err != nil {
			return "", err
		}
		names = append(names, fmt.Sprintf("S%02dE%02d", e.SeasonNumber, e.EpisodeNumber))
	}
	return strings.Join(names, ", "), nil
}
