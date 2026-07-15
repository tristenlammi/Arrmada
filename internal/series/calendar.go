package series

import "context"

// UpcomingEpisode is one episode with an air date, for the Calendar module.
type UpcomingEpisode struct {
	AirDate     string `json:"air_date"`
	SeriesID    int64  `json:"series_id"`
	SeriesTitle string `json:"series_title"`
	PosterURL   string `json:"poster_url"`
	Season      int    `json:"season"`
	Episode     int    `json:"episode"`
	EpisodeName string `json:"episode_name"`
	HasFile     bool   `json:"has_file"`
	Monitored   bool   `json:"monitored"`
}

// UpcomingEpisodes returns library episodes airing within [from, to] (inclusive, YYYY-MM-DD),
// excluding specials. Series monitored flag rides along so the UI can dim unmonitored ones.
func (s *Service) UpcomingEpisodes(ctx context.Context, from, to string) ([]UpcomingEpisode, error) {
	rows, err := s.repo.db.QueryContext(ctx, `
		SELECT e.air_date, s.id, s.title, s.poster_url, e.season_number, e.episode_number,
		       e.title, e.has_file, s.monitored
		FROM episodes e JOIN series s ON s.id = e.series_id
		WHERE e.season_number > 0 AND e.air_date <> ''
		      AND date(e.air_date) BETWEEN date(?) AND date(?)
		ORDER BY e.air_date, s.title, e.episode_number`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UpcomingEpisode
	for rows.Next() {
		var e UpcomingEpisode
		var hasFile, mon int
		if err := rows.Scan(&e.AirDate, &e.SeriesID, &e.SeriesTitle, &e.PosterURL, &e.Season, &e.Episode,
			&e.EpisodeName, &hasFile, &mon); err != nil {
			return nil, err
		}
		e.HasFile, e.Monitored = hasFile != 0, mon != 0
		out = append(out, e)
	}
	return out, rows.Err()
}
