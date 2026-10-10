package insights

import (
	"context"
	"strings"
)

// "Watched by" on a title's own page: who has played it on Plex, how often and when last,
// from the recorded play history (live sessions and imported Tautulli rows). Only Plex
// video plays are read; audiobook listening lives elsewhere and is never touched here.

// UserPlays is one person's plays of a title.
type UserPlays struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Plays      int    `json:"plays"`
	LastPlayed int64  `json:"last_played"` // unix seconds
}

// WatchStats is a title's play summary. Available is false when Plex isn't set up, so
// the page shows nothing rather than "nobody watched it".
type WatchStats struct {
	Available  bool        `json:"available"`
	Plays      int         `json:"plays"`
	LastPlayed int64       `json:"last_played"`
	Users      []UserPlays `json:"users"`
}

// WatchStats sums up a title's plays. media is "movie" or "series"; ids find the title's
// Plex rating key through the library index; title and year are Arrmada's names for it,
// used (with Plex's own, when the index knows it) for plays recorded under another key.
func (s *Service) WatchStats(ctx context.Context, media string, ids ExternalIDs, title string, year int) (WatchStats, error) {
	out := WatchStats{Users: []UserPlays{}}
	if !s.Configured(ctx) {
		return out, nil
	}
	out.Available = true
	m := titleMatch{titles: []string{title}, year: year}
	if it, ok := s.Locate(ctx, media, ids); ok {
		m.ratingKey = it.RatingKey
		m.titles = append(m.titles, it.Title) // the name Plex records plays under
		if m.year == 0 {
			m.year = it.Year
		}
	}
	users, err := s.repo.watchedBy(ctx, isShow(media), m)
	if err != nil {
		return out, err
	}
	for _, u := range users {
		out.Users = append(out.Users, u)
		out.Plays += u.Plays
		if u.LastPlayed > out.LastPlayed {
			out.LastPlayed = u.LastPlayed
		}
	}
	return out, nil
}

// titleMatch is what identifies a title's plays: its rating key, else its name(s) — with
// the year for a film, so a remake isn't counted with the original.
type titleMatch struct {
	ratingKey string
	titles    []string
	year      int
}

// watchedBy groups a title's plays by person, most recent first. A show's plays are its
// episodes' (matched on the show's name: episode rows don't keep the show's rating key).
func (r *repo) watchedBy(ctx context.Context, show bool, m titleMatch) ([]UserPlays, error) {
	var conds []string
	var args []any
	seen := map[string]bool{}
	var titles []string
	for _, t := range m.titles {
		t = strings.TrimSpace(t)
		if t != "" && !seen[strings.ToLower(t)] {
			seen[strings.ToLower(t)] = true
			titles = append(titles, t)
		}
	}
	if show {
		for _, t := range titles {
			conds = append(conds, "grandparent_title = ? COLLATE NOCASE")
			args = append(args, t)
		}
		if len(conds) == 0 {
			return nil, nil
		}
		return r.groupPlays(ctx, "media_type = 'episode' AND ("+strings.Join(conds, " OR ")+")", args)
	}
	if m.ratingKey != "" {
		conds = append(conds, "rating_key = ?")
		args = append(args, m.ratingKey)
	}
	if m.year > 0 { // without a year a title alone would sweep in every film of that name
		for _, t := range titles {
			conds = append(conds, "(title = ? COLLATE NOCASE AND year = ?)")
			args = append(args, t, m.year)
		}
	}
	if len(conds) == 0 {
		return nil, nil
	}
	return r.groupPlays(ctx, "media_type = 'movie' AND ("+strings.Join(conds, " OR ")+")", args)
}

func (r *repo) groupPlays(ctx context.Context, where string, args []any) ([]UserPlays, error) {
	// The bare user_name next to MAX(started_at) is SQLite's "value from the row with the
	// max": the name they had at their latest play, if the account isn't known by now.
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.user_id, COALESCE(NULLIF(u.username, ''), a.user_name), a.plays, a.last
		  FROM (SELECT user_id, user_name, COUNT(*) AS plays, MAX(started_at) AS last
		          FROM stream_sessions WHERE `+where+` GROUP BY user_id) a
		  LEFT JOIN plex_users u ON u.id = a.user_id
		 ORDER BY a.last DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserPlays
	for rows.Next() {
		var u UserPlays
		if err := rows.Scan(&u.ID, &u.Name, &u.Plays, &u.LastPlayed); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
