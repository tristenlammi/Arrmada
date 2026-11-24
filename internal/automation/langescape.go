package automation

import (
	"context"

	"github.com/tristenlammi/arrmada/internal/movies"
)

// The language escape (#137) is one-shot: a movie's track, or an episode, may give up
// resolution for a wanted language at most once, automatically. The quality rules decide
// whether a release is the escape (quality.IsLanguageUpgrade, IsLanguageEscape); the
// import gates say so (episodeGate, movieReplaceJudge); and the statement or transaction
// that records the placed file records the escape with it (movies/movie_versions/
// episodes.lang_escaped, migration 0356: series.SupersedeEpisodeFileEscaping,
// movies.MarkImportedToAllowing). These answer the rules' CurrentFile.EscapeUsed, so
// neither the sweep nor an import gate takes another.
//
// Why that is airtight whatever later names and probes say: every automatic placement
// over an existing file passes one of the two gates, and each reports an escape whenever
// the quality rules read the placement as one — whatever resolution the gate itself read.
// The record lands atomically with the file: a placement that fails records nothing (the
// track or episode is still on its old file, and a retry of the same release passes
// again), and one that succeeds can't be left unrecorded. Once recorded, the sweep and
// both gates refuse the escape for that track or episode; the flag stays when the file is
// later replaced or removed, follows an episode's file through a renumber, and goes only
// with the row itself. A failed read of it counts as spent.
//
// Placements the user approved (an interactive pick or upload, an approved review) skip
// the gates and record nothing: they're the user's own choice.

// movieEscapeUsed is the EscapeUsed of movie id's track v (the movie row for the default
// track, its movie_versions row otherwise). Read only when the escape would be taken.
func (c *Coordinator) movieEscapeUsed(id int64, v movies.Version) func(context.Context) bool {
	return func(ctx context.Context) bool {
		q, arg := `SELECT lang_escaped FROM movies WHERE id = ?`, id
		if !v.IsDefault && v.ID != 0 {
			q, arg = `SELECT lang_escaped FROM movie_versions WHERE id = ?`, v.ID
		}
		var n int
		if err := c.db.QueryRowContext(ctx, q, arg).Scan(&n); err != nil {
			return true // can't tell: never risk a second downgrade
		}
		return n != 0
	}
}

// episodeEscapeUsed is the EscapeUsed of the show's episode season/episode.
func (c *Coordinator) episodeEscapeUsed(seriesID int64, season, episode int) func(context.Context) bool {
	return func(ctx context.Context) bool {
		var n int
		err := c.db.QueryRowContext(ctx,
			`SELECT lang_escaped FROM episodes WHERE series_id = ? AND season_number = ? AND episode_number = ?`,
			seriesID, season, episode).Scan(&n)
		if err != nil {
			return true
		}
		return n != 0
	}
}
