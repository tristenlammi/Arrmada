package series

import (
	"context"

	"github.com/tristenlammi/arrmada/internal/displaytitle"
	"github.com/tristenlammi/arrmada/internal/store"
)

// UpcomingEpisode is one episode with an air date, for the Calendar module.
type UpcomingEpisode struct {
	AirDate     string `json:"air_date"`
	SeriesID    int64  `json:"series_id"`
	TMDBID      int    `json:"tmdb_id"`
	SeriesTitle string `json:"series_title"`
	PosterURL   string `json:"poster_url"`
	Season      int    `json:"season"`
	Episode     int    `json:"episode"`
	EpisodeName string `json:"episode_name"`
	HasFile     bool   `json:"has_file"`
	// Monitored: the episode is searched for — it and its season are monitored and the
	// show's searching is on (what the sweep needs).
	Monitored bool `json:"monitored"`
	// SearchingOff: the episode and its season are monitored but the show's searching is
	// off, so nothing is searched until it's turned back on.
	SearchingOff bool `json:"searching_off"`
	// CanonicalTitle is the show's own (TMDB English) title when SeriesTitle is its shown
	// one: the adult filter judges both.
	CanonicalTitle string `json:"-"`
}

// UpcomingEpisodes returns library episodes airing within [from, to] (inclusive, YYYY-MM-DD),
// excluding specials. Whether each is searched for rides along, so the UI can dim the ones
// that aren't and say why (the episode isn't wanted, or the show's searching is off) — the
// same words as the series page's episode cards.
func (s *Service) UpcomingEpisodes(ctx context.Context, from, to string) ([]UpcomingEpisode, error) {
	// The show's shown title (Settings → Library → "Show titles as").
	shown := displaytitle.SQL("s")
	rows, err := s.repo.db.QueryContext(ctx, `
		SELECT e.air_date, s.id, s.tmdb_id, `+shown+`, s.poster_url, e.season_number, e.episode_number,
		       e.title, e.has_file, s.monitored, s.title,
		       CASE WHEN e.monitored = 1 AND COALESCE(sn.monitored, 1) = 1 THEN 1 ELSE 0 END
		FROM episodes e JOIN series s ON s.id = e.series_id
		LEFT JOIN seasons sn ON sn.series_id = e.series_id AND sn.season_number = e.season_number
		WHERE e.season_number > 0 AND e.air_date <> ''
		      AND `+store.DateText(s.repo.db, "e.air_date")+` BETWEEN `+store.DateText(s.repo.db, "?")+` AND `+store.DateText(s.repo.db, "?")+`
		ORDER BY e.air_date, `+shown+`, e.episode_number`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UpcomingEpisode
	for rows.Next() {
		var e UpcomingEpisode
		var hasFile, showOn, wanted int
		if err := rows.Scan(&e.AirDate, &e.SeriesID, &e.TMDBID, &e.SeriesTitle, &e.PosterURL, &e.Season, &e.Episode,
			&e.EpisodeName, &hasFile, &showOn, &e.CanonicalTitle, &wanted); err != nil {
			return nil, err
		}
		e.HasFile = hasFile != 0
		e.Monitored = wanted != 0 && showOn != 0
		e.SearchingOff = wanted != 0 && showOn == 0
		out = append(out, e)
	}
	return out, rows.Err()
}
