package automation

import (
	"context"

	"github.com/tristenlammi/arrmada/internal/movies"
)

// The language escape (#137) is one-shot: a movie's track, or an episode, may give up
// resolution for a wanted language at most once, ever. The quality rules decide whether
// a release is the escape (quality.IsLanguageUpgrade and friends); this records that one
// was taken (movies/movie_versions/episodes.lang_escaped, migration 0356) and answers the
// rules' CurrentFile.EscapeUsed, so neither the sweep nor an import gate takes another.
//
// Why that is airtight whatever the names and probes say: every automatic placement that
// is the escape passes one of the two import gates — wantsEpisodeFile (the lower-resolution
// branch, or an equal-resolution accept the rules read as lower) and the movie gate
// (movieReplaceJudge) — and each records it before it answers yes. Once recorded, the
// sweep (UpgradePick) and both gates refuse the escape for that track or episode. The
// flag stays when the file is replaced or removed, and goes only with the row itself.
//
// It's recorded when the gate accepts, before the file is placed: an import that then
// fails spends the escape without having taken it. That errs the safe way — a second
// downgrade is never possible — and a failed read of the flag counts as spent too.

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

// recordMovieEscape spends movie id's track v's language escape. false when it couldn't be
// written: the caller then refuses the import, so an escape is never taken unrecorded.
func (c *Coordinator) recordMovieEscape(ctx context.Context, id int64, v movies.Version) bool {
	q, arg := `UPDATE movies SET lang_escaped = 1 WHERE id = ?`, id
	if !v.IsDefault && v.ID != 0 {
		q, arg = `UPDATE movie_versions SET lang_escaped = 1 WHERE id = ?`, v.ID
	}
	return c.recordEscape(ctx, q, arg)
}

// recordEpisodeEscape spends the show's episode season/episode's language escape, like
// recordMovieEscape.
func (c *Coordinator) recordEpisodeEscape(ctx context.Context, seriesID int64, season, episode int) bool {
	return c.recordEscape(ctx,
		`UPDATE episodes SET lang_escaped = 1 WHERE series_id = ? AND season_number = ? AND episode_number = ?`,
		seriesID, season, episode)
}

func (c *Coordinator) recordEscape(ctx context.Context, q string, args ...any) bool {
	res, err := c.db.ExecContext(ctx, q, args...)
	if err != nil {
		c.log.Warn("couldn't record a language escape — refusing the import", "err", err)
		return false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.log.Warn("couldn't record a language escape: no such track or episode — refusing the import")
		return false
	}
	return true
}
