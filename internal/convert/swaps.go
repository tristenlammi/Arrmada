package convert

import (
	"context"
	"os"
)

// The staged-replace journal (convert_swaps, migration 0064): finalizeOutput records
// each swap before retiring the original and clears the row once the rename AND the
// library repoint both succeed. Any row still present at startup is a swap a crash
// (or a failed repoint) interrupted — the converted file is complete on disk, but
// either still named .arrpart or not yet reflected in the library record.

// recordSwap journals a staged replacement about to happen.
func (s *Service) recordSwap(job *Job, part, final, src string) {
	kind := "movie"
	if job.Kind == "episode" {
		kind = "episode"
	}
	_, err := s.db.ExecContext(context.Background(),
		`INSERT OR REPLACE INTO convert_swaps (part, final, src, kind, movie_id, series_id, season, episode)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		part, final, src, kind, job.MovieID, job.SeriesID, job.Season, job.Episode)
	if err != nil {
		s.log.Warn("convert: could not journal the file swap", "part", part, "err", err)
	}
}

// clearSwap removes a completed swap's journal row.
func (s *Service) clearSwap(part string) {
	_, _ = s.db.ExecContext(context.Background(), `DELETE FROM convert_swaps WHERE part = ?`, part)
}

// recoverSwaps replays interrupted swaps at startup: completes the .arrpart → final
// rename when the crash hit between retire and rename, and repoints the library
// record when the crash (or a DB error) hit after the rename. Without this, the
// record pointed at a recycled path, the converted file sat stranded, and the next
// sweep failed on "source file is gone" until the item was blocklisted.
func (s *Service) recoverSwaps(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT part, final, src, kind, movie_id, series_id, season, episode FROM convert_swaps`)
	if err != nil {
		return // table may predate the migration runner in tests; nothing to recover
	}
	type swap struct {
		part, final, src, kind string
		movieID, seriesID      int64
		season, episode        int
	}
	var swaps []swap
	for rows.Next() {
		var w swap
		if rows.Scan(&w.part, &w.final, &w.src, &w.kind, &w.movieID, &w.seriesID, &w.season, &w.episode) == nil {
			swaps = append(swaps, w)
		}
	}
	rows.Close()
	for _, w := range swaps {
		if _, err := os.Stat(w.part); err == nil {
			if _, err := os.Stat(w.final); err == nil {
				// Both exist — the rename happened via a retried job; the part is a
				// stale duplicate.
				_ = os.Remove(w.part)
			} else if err := os.Rename(w.part, w.final); err != nil {
				s.log.Warn("convert: could not complete an interrupted swap", "part", w.part, "err", err)
				continue // keep the row; maybe the volume comes back
			} else {
				s.log.Info("convert: completed an interrupted file swap", "final", w.final)
			}
		} else if _, err := os.Stat(w.final); err != nil {
			// Neither part nor final exists — stale row (files handled by hand).
			s.clearSwap(w.part)
			continue
		}
		// The final file exists; make sure the library record points at it.
		job := &Job{Kind: w.kind, MovieID: w.movieID, SeriesID: w.seriesID, Season: w.season, Episode: w.episode}
		if err := s.markConverted(ctx, job, w.src, w.final, ""); err != nil {
			s.log.Warn("convert: could not repoint the library record for a recovered swap",
				"final", w.final, "err", err)
			continue // keep the row for the next startup
		}
		s.log.Info("convert: reconciled an interrupted conversion", "final", w.final)
		s.clearSwap(w.part)
	}
}
