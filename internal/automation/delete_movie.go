package automation

import (
	"context"
	"errors"
	"strings"

	"github.com/tristenlammi/arrmada/internal/download"
)

// DeleteMovieOpts is what the user ticked in the delete dialog.
type DeleteMovieOpts struct {
	DeleteFiles     bool // move the movie's files to the recycle bin (or delete them with it off)
	CancelDownloads bool // remove its in-flight downloads and their partial data
}

// PendingMovieDownload is an in-flight grab for a movie, for the delete dialog to name.
type PendingMovieDownload struct {
	Hash     string  `json:"hash"`
	Title    string  `json:"title"`
	Progress float64 `json:"progress"` // 0..1; 0 when the client doesn't list it
}

// pendingMovieGrab is a 'grabbed' grab row for one movie.
type pendingMovieGrab struct {
	id    int64
	hash  string
	title string
}

func (c *Coordinator) pendingMovieGrabs(ctx context.Context, movieID int64) ([]pendingMovieGrab, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, info_hash, title FROM grabs WHERE status = ? AND media_type = 'movie' AND movie_id = ? ORDER BY id`,
		grabStatusGrabbed, movieID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingMovieGrab
	for rows.Next() {
		var g pendingMovieGrab
		if err := rows.Scan(&g.id, &g.hash, &g.title); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// PendingMovieDownloads lists a movie's in-flight grabs with live progress from the
// download client when it answers.
func (c *Coordinator) PendingMovieDownloads(ctx context.Context, movieID int64) ([]PendingMovieDownload, error) {
	grabs, err := c.pendingMovieGrabs(ctx, movieID)
	if err != nil {
		return nil, err
	}
	out := make([]PendingMovieDownload, 0, len(grabs))
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
		out = append(out, PendingMovieDownload{Hash: g.hash, Title: g.title, Progress: progress[strings.ToLower(g.hash)]})
	}
	return out, nil
}

// DeleteMovie deletes a movie and settles its downloads, so a deleted film can't come
// back into the library by itself. Its in-flight grabs are first set aside (stall
// detection stops treating them as pending), then the movie is deleted — files to the bin
// first, aborting on a refusal — and only once that succeeded are the torrents removed
// (when asked) and the grabs closed as 'cancelled', or 'orphaned' when the torrent stays.
// If the movie delete fails, the grabs go back to 'grabbed' and no torrent was touched.
// A torrent left in the client is held for Review when it finishes (HoldMovieImport),
// never imported by name.
func (c *Coordinator) DeleteMovie(ctx context.Context, id int64, opts DeleteMovieOpts) error {
	if c.movies == nil {
		return errors.New("the movies module isn't ready")
	}
	if _, err := c.movies.Get(ctx, id); err != nil {
		return err
	}
	grabs, err := c.pendingMovieGrabs(ctx, id)
	if err != nil {
		return err
	}
	for _, g := range grabs {
		c.setGrabStatus(ctx, g.id, grabStatusOrphaned)
	}
	if err := c.movies.Delete(ctx, id, opts.DeleteFiles); err != nil {
		for _, g := range grabs {
			c.setGrabStatus(ctx, g.id, grabStatusGrabbed)
		}
		return err
	}

	if opts.CancelDownloads {
		remove := c.removeTorrent
		if remove == nil && c.downloads != nil {
			remove = c.downloads.Remove
		}
		for _, g := range grabs {
			// Data is deleted only for this grab's own torrent, named by its exact hash; a
			// grab without a usable hash stays orphaned rather than guessing.
			if remove == nil || !download.ValidHash(g.hash) {
				continue
			}
			if err := remove(ctx, g.hash, true); err != nil {
				c.log.Warn("movie delete: couldn't cancel its download — left it in the client", "release", g.title, "err", err)
				continue
			}
			c.setGrabStatus(ctx, g.id, grabStatusCancelled)
		}
	}

	if _, err := c.db.ExecContext(ctx, `DELETE FROM blocklist WHERE movie_id = ? AND media_type = 'movie'`, id); err != nil {
		c.log.Warn("movie delete: couldn't clear its blocklist", "movie_id", id, "err", err)
	}
	// movies.Delete announced movie.deleted and queued the index clean-up.
	c.log.Info("movie deleted", "movie_id", id, "files", opts.DeleteFiles, "downloads_pending", len(grabs), "cancel", opts.CancelDownloads)
	return nil
}
