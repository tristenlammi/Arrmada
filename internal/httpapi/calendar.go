package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/requests"
)

// CalendarItem is one dated entry (episode or movie) in the Calendar.
type CalendarItem struct {
	Date      string `json:"date"` // YYYY-MM-DD
	Type      string `json:"type"` // "episode" | "movie"
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle"` // kept for older clients; new ones build their own line from the fields below
	PosterURL string `json:"poster_url,omitempty"`
	RefID     int64  `json:"ref_id"` // series id or movie id (for linking)
	HasFile   bool   `json:"has_file"`
	Monitored bool   `json:"monitored"`
	// TMDBID and MediaType open the title's Discover page, the only title page a
	// requester can reach (staff link to ref_id instead).
	TMDBID       int    `json:"tmdb_id"`
	MediaType    string `json:"media_type"` // "movie" | "series"
	Year         int    `json:"year,omitempty"`
	Season       int    `json:"season,omitempty"`
	Episode      int    `json:"episode,omitempty"`
	EpisodeTitle string `json:"episode_title,omitempty"`
	// RequestedByMe: the viewer asked for this title or follows a request for it (and it
	// wasn't declined). Only ever about the viewer's own requests.
	RequestedByMe bool `json:"requested_by_me"`
}

// handleCalendar returns upcoming episodes + movie releases in a date window. Defaults to a
// window around today when start/end aren't given. Available to any signed-in user. Each
// item says whether the viewer requested it; ?mine=1 keeps only those.
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

	items := a.calendarItems(ctx, start, end)
	mine := q.Get("mine") == "1"
	if u, ok := userFrom(r); ok && u != nil {
		items = a.markRequestedBy(ctx, items, u.ID, mine)
	} else if mine {
		items = []CalendarItem{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items, "start": start, "end": end})
}

// calendarItems is every library episode and movie release dated within [start, end]
// (inclusive, YYYY-MM-DD). A source that fails is left out rather than failing the whole
// calendar: half a schedule beats an error page.
func (a *api) calendarItems(ctx context.Context, start, end string) []CalendarItem {
	items := []CalendarItem{}
	if a.deps.Series == nil || a.deps.Movies == nil {
		return items // only in tests that build a server without the libraries
	}
	if eps, err := a.deps.Series.UpcomingEpisodes(ctx, start, end); err == nil {
		for _, e := range eps {
			items = append(items, CalendarItem{
				Date: dateOnly(e.AirDate), Type: "episode", Title: e.SeriesTitle,
				Subtitle:  episodeSubtitle(e.Season, e.Episode, e.EpisodeName),
				PosterURL: e.PosterURL, RefID: e.SeriesID, HasFile: e.HasFile, Monitored: e.Monitored,
				TMDBID: e.TMDBID, MediaType: "series", Season: e.Season, Episode: e.Episode, EpisodeTitle: e.EpisodeName,
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
				TMDBID: m.TMDBID, MediaType: "movie", Year: m.Year,
			})
		}
	}
	return items
}

// markRequestedBy sets RequestedByMe on the items user uid asked for or follows, and with
// only set drops the rest. If the requests can't be read, 'mine' is empty rather than the
// whole library passed off as theirs.
func (a *api) markRequestedBy(ctx context.Context, items []CalendarItem, uid int64, only bool) []CalendarItem {
	var keys map[string]bool
	if a.deps.Requests != nil {
		k, err := a.deps.Requests.MediaKeysForUser(ctx, uid)
		if err != nil {
			a.deps.Log.Warn("calendar: reading the viewer's requests failed", "err", err)
		}
		keys = k
	}
	out := items[:0]
	for _, it := range items {
		it.RequestedByMe = keys[requests.MediaKey(it.MediaType, it.TMDBID)]
		if only && !it.RequestedByMe {
			continue
		}
		out = append(out, it)
	}
	return out
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
