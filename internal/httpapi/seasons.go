package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/series"
)

// Per-season states, as a requester sees them. Monitoring flags are never shown, and who
// asked only to staff: only what the state means for asking.
const (
	seasonInLibrary   = "in_library"  // every aired episode is on disk
	seasonRequested   = "requested"   // a pending or approved request already asks for it
	seasonPartial     = "partial"     // some aired episodes are on disk, nothing is fetching the rest
	seasonOnTheWay    = "on_the_way"  // the server is already set to grab what's missing
	seasonUnaired     = "unaired"     // nothing has aired yet
	seasonRequestable = "requestable" // aired, not here, not coming: ask for it
)

// seasonState is one row of GET /api/v1/media/series/{id}/seasons.
type seasonState struct {
	Number       int    `json:"number"`
	Name         string `json:"name,omitempty"`
	EpisodeCount int    `json:"episode_count"`
	AirDate      string `json:"air_date,omitempty"`
	Have         int    `json:"have"`  // episodes on disk
	Aired        int    `json:"aired"` // episodes out so far
	State        string `json:"state"`
	// Requestable is whether a request may ask for the season: a requestable season, or
	// a partial one whose missing episodes nobody is fetching.
	Requestable bool `json:"requestable"`
	// Request is the request standing for a requested season: its id, status and whether
	// it's the viewer's own (the requester's name for staff only).
	Request *requests.SeasonRequest `json:"request,omitempty"`
}

// seasonAsks are a show's active requests, by season, and a whole-show one if any.
type seasonAsks struct {
	bySeason map[int]requests.SeasonRequest
	whole    *requests.SeasonRequest
}

// libraryShow is what the library knows about a show, for the season states. A zero
// value is a show that isn't in the library.
type libraryShow struct {
	in        bool
	monitored bool // the series gate: off means nothing is fetched, whatever the seasons say
	progress  map[int]series.SeasonProgress
}

// buildSeasonStates folds TMDB's season list and the library's per-season counts into
// one row per regular season. Seasons only the library knows (a TVDB-numbered show's
// extra season) are listed too. A season not complete on disk that a request already
// covers reads "requested". today is YYYY-MM-DD.
func buildSeasonStates(summaries []metadata.SeasonSummary, lib libraryShow, asks seasonAsks, today string) []seasonState {
	rows := map[int]*seasonState{}
	for _, s := range summaries {
		if s.Number <= 0 {
			continue
		}
		rows[s.Number] = &seasonState{Number: s.Number, Name: s.Name, EpisodeCount: s.EpisodeCount, AirDate: s.AirDate}
	}
	for n, p := range lib.progress {
		if n <= 0 {
			continue
		}
		if rows[n] == nil {
			rows[n] = &seasonState{Number: n, Name: "Season " + strconv.Itoa(n), EpisodeCount: p.Episodes}
		}
	}
	out := make([]seasonState, 0, len(rows))
	for n, row := range rows {
		p, known := lib.progress[n]
		known = known && p.Episodes > 0
		row.Have = p.Have
		if known {
			row.Aired = p.Aired
		} else if row.AirDate != "" && row.AirDate <= today {
			// Not in the library (or not refreshed since TMDB added it): a season whose
			// first episode is out counts as aired in full.
			row.Aired = row.EpisodeCount
		}
		ask, asked := asks.bySeason[n]
		if !asked && asks.whole != nil {
			ask, asked = *asks.whole, true
		}
		switch {
		case row.Have > 0 && row.Have >= row.Aired:
			row.State = seasonInLibrary
		case asked:
			row.State = seasonRequested
			row.Request = &ask
		case lib.in && lib.monitored && known && p.Monitored && (p.Wanted > 0 || p.Upcoming > 0):
			row.State = seasonOnTheWay
		case row.Aired == 0:
			row.State = seasonUnaired
		case row.Have > 0:
			row.State = seasonPartial
		default:
			row.State = seasonRequestable
		}
		row.Requestable = row.State == seasonRequestable || row.State == seasonPartial
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

// libraryShowFor reads a show's library state by TMDB id. A show that isn't in the
// library is the zero value with no error.
func (a *api) libraryShowFor(r *http.Request, tmdbID int) (libraryShow, series.Series, error) {
	if a.deps.Series == nil {
		return libraryShow{}, series.Series{}, nil
	}
	sr, err := a.deps.Series.GetByTMDB(r.Context(), tmdbID)
	if errors.Is(err, series.ErrNotFound) {
		return libraryShow{}, series.Series{}, nil
	}
	if err != nil {
		return libraryShow{}, series.Series{}, err
	}
	prog, err := a.deps.Series.SeasonProgress(r.Context(), sr.ID)
	if err != nil {
		return libraryShow{}, sr, err
	}
	return libraryShow{in: true, monitored: sr.Monitored, progress: prog}, sr, nil
}

// handleSeriesSeasons answers which seasons of a show exist and, for each, whether it is
// on disk, partly there, on the way, not out yet, or can be asked for. The season list
// comes from the cached TMDB detail record (no extra TMDB call per sheet open); when TMDB
// can't answer, a show in the library is still described from its own seasons.
func (a *api) handleSeriesSeasons(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		a.writeError(w, http.StatusBadRequest, "invalid tmdb id")
		return
	}
	lib, _, err := a.libraryShowFor(r, id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the library")
		return
	}
	var summaries []metadata.SeasonSummary
	if a.metadataReady() {
		d, derr := a.deps.Discovery.MediaDetails(r.Context(), "series", id)
		switch {
		case derr == nil && d != nil:
			summaries = d.Seasons
		case !lib.in:
			a.writeError(w, http.StatusBadGateway, "couldn't load the show's seasons")
			return
		}
	} else if !lib.in {
		a.discoveryReady(w, r) // writes the role-appropriate "not set up" message
		return
	}
	var asks seasonAsks
	if a.deps.Requests != nil {
		var viewer int64
		staff := false
		if u, ok := userFrom(r); ok && u != nil {
			viewer, staff = u.ID, u.Role.AtLeast(auth.RoleManager)
		}
		asks.bySeason, asks.whole, err = a.deps.Requests.SeasonRequests(r.Context(), id, viewer, staff)
		if err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not read requests")
			return
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"seasons": buildSeasonStates(summaries, lib, asks, time.Now().Format("2006-01-02")),
	})
}
