package httpapi

import (
	"context"
	"net/http"
	"time"
)

// CalendarItem is one dated entry (episode or movie) in the Calendar.
type CalendarItem struct {
	Date      string `json:"date"` // YYYY-MM-DD
	Type      string `json:"type"` // "episode" | "movie"
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle"`
	PosterURL string `json:"poster_url,omitempty"`
	RefID     int64  `json:"ref_id"` // series id or movie id (for linking)
	HasFile   bool   `json:"has_file"`
	Monitored bool   `json:"monitored"`
}

// handleCalendar returns upcoming episodes + movie releases in a date window. Defaults to a
// window around today when start/end aren't given. Available to any signed-in user.
func (a *api) handleCalendar(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	start := q.Get("start")
	end := q.Get("end")
	if !validDate(start) || !validDate(end) {
		now := time.Now()
		start = now.AddDate(0, 0, -7).Format("2006-01-02")
		end = now.AddDate(0, 0, 42).Format("2006-01-02")
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	items := []CalendarItem{}
	if eps, err := a.deps.Series.UpcomingEpisodes(ctx, start, end); err == nil {
		for _, e := range eps {
			items = append(items, CalendarItem{
				Date: dateOnly(e.AirDate), Type: "episode", Title: e.SeriesTitle,
				Subtitle: episodeSubtitle(e.Season, e.Episode, e.EpisodeName),
				PosterURL: e.PosterURL, RefID: e.SeriesID, HasFile: e.HasFile, Monitored: e.Monitored,
			})
		}
	}
	if mv, err := a.deps.Movies.Upcoming(ctx, start, end); err == nil {
		for _, m := range mv {
			sub := "Movie"
			if m.Year > 0 {
				sub = itoa(m.Year) + " · Movie"
			}
			items = append(items, CalendarItem{
				Date: m.ReleaseDate, Type: "movie", Title: m.Title, Subtitle: sub,
				PosterURL: m.PosterURL, RefID: m.ID, HasFile: m.HasFile, Monitored: m.Monitored,
			})
		}
	}

	a.writeJSON(w, http.StatusOK, map[string]any{"items": items, "start": start, "end": end})
}

func validDate(s string) bool {
	if len(s) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func dateOnly(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func episodeSubtitle(season, ep int, name string) string {
	s := "S" + itoa(season) + " · E" + itoa(ep)
	if name != "" {
		s += " · " + name
	}
	return s
}

func itoa(n int) string {
	// small, allocation-light itoa for non-negative ints
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
