package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// Movies → Wanted: what Arrmada is still looking for among the films, why it hasn't found
// it, and when it tries next. Two tabs:
//
//   - missing: the cross-media Wanted view's movie rows (buildWanted) — monitored films
//     with no file and nothing downloading, searched now or not released yet — plus,
//     apart, films whose extra version (a 4K copy, a Director's Cut) is still missing;
//   - cutoff: files on disk that don't meet their profile's target yet, and whether the
//     upgrade sweep will do anything about it (and why not).
//
// Every row is the shared wantedRow, with the movie-only fields beside it.

// movieWantedRow is one row of Movies → Wanted.
type movieWantedRow struct {
	wantedRow
	// Queued: a search of it is waiting or running in the movie search queue right now.
	Queued bool `json:"queued"`
	// Tracks are a versions row's missing extra tracks, by label.
	Tracks []string `json:"tracks,omitempty"`

	// Cutoff tab: the track judged (0 = the default; Track names any other), what about its
	// file misses the target, and whether the upgrade sweep will act — WhyNot when it won't.
	VersionID   int64              `json:"version_id,omitempty"`
	Track       string             `json:"track,omitempty"`
	Detail      string             `json:"detail,omitempty"`
	Issues      []quality.FitIssue `json:"issues,omitempty"`
	WillUpgrade bool               `json:"will_upgrade,omitempty"`
	WhyNot      string             `json:"why_not,omitempty"`
}

// Cutoff row states, beside the shared ones (waiting_download: an upgrade is in flight).
const (
	wantedUpgrading    = "upgrading"     // the upgrade sweep looks for a better release
	wantedNotUpgrading = "not_upgrading" // it won't; WhyNot says why
)

// movieMissing is the Missing tab.
type movieMissing struct {
	Searching  []movieWantedRow `json:"searching"`
	Upcoming   []movieWantedRow `json:"upcoming"`
	Versions   []movieWantedRow `json:"versions"`
	QueueKnown bool             `json:"queue_known"`
}

// handleMoviesWanted is GET /api/v1/movies/wanted?tab=missing|cutoff.
func (a *api) handleMoviesWanted(w http.ResponseWriter, r *http.Request) {
	if a.deps.Automation == nil || a.deps.Movies == nil {
		a.writeError(w, http.StatusServiceUnavailable, "movies aren't ready yet")
		return
	}
	ctx := r.Context()
	switch tab := r.URL.Query().Get("tab"); tab {
	case "", "missing":
		out, err := a.moviesWantedMissing(ctx)
		if err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not list wanted movies")
			return
		}
		a.writeJSON(w, http.StatusOK, out)
	case "cutoff":
		rows, known, err := a.moviesWantedCutoff(ctx)
		if err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not judge the library's files")
			return
		}
		a.writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "queue_known": known})
	default:
		a.writeError(w, http.StatusBadRequest, "tab must be missing or cutoff")
	}
}

// moviesWantedMissing builds the Missing tab from the shared Wanted rows, adding the
// queue's view and the films with only an extra version missing.
func (a *api) moviesWantedMissing(ctx context.Context) (movieMissing, error) {
	lists := a.buildWanted(ctx, wantedKinds{automation.AttemptMovie: true})
	out := movieMissing{
		Searching: a.movieRows(lists.Searching), Upcoming: a.movieRows(lists.Upcoming),
		Versions: []movieWantedRow{}, QueueKnown: lists.QueueKnown,
	}
	versions, err := a.missingVersionRows(ctx, lists.QueueKnown)
	if err != nil {
		return movieMissing{}, err
	}
	out.Versions = versions
	return out, nil
}

func (a *api) movieRows(rows []wantedRow) []movieWantedRow {
	out := make([]movieWantedRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, movieWantedRow{wantedRow: r, Queued: a.deps.Automation.MovieSearchBusy(r.ID)})
	}
	return out
}

// missingVersionRows lists the films the Wanted badge doesn't cover but the missing-sweep
// still searches: an extra version monitored and without a file, while the film itself
// has its file (or isn't monitored). One row per film, naming the missing tracks.
func (a *api) missingVersionRows(ctx context.Context, queueKnown bool) ([]movieWantedRow, error) {
	byMovie, all, err := a.deps.Movies.LibraryVersionRows(ctx)
	if err != nil {
		return nil, err
	}
	active, _ := a.deps.Automation.ActiveByItem(ctx, automation.AttemptMovie)
	states, _ := a.deps.Movies.SearchStates(ctx)
	names := a.profileNames(ctx)
	now := time.Now().UTC()
	rows := []movieWantedRow{}
	for _, m := range all {
		if m.Monitored && !m.HasFile {
			continue // the film itself is wanted: a Searching or Upcoming row, or downloading
		}
		var tracks []string
		for _, v := range byMovie[m.ID] {
			if !v.IsDefault && v.Monitored && !v.HasFile {
				tracks = append(tracks, trackLabel(v))
			}
		}
		if len(tracks) == 0 {
			continue
		}
		row := movieWantedRow{
			wantedRow: wantedRow{MediaType: automation.AttemptMovie, ID: m.ID, MovieID: m.ID, Title: m.Title, Year: m.Year,
				PosterURL: m.PosterURL, QualityProfile: names(m.QualityProfile), Missing: tracks, State: wantedSearching},
			Tracks: tracks, Queued: a.deps.Automation.MovieSearchBusy(m.ID),
		}
		switch {
		case !a.deps.Movies.IsAvailable(m):
			row.State = wantedNotReleased
			if m.Extra != nil {
				row.AvailableAt = m.Extra.ReleaseDate
			}
		case !queueKnown:
			row.State = wantedUnknown
		case len(active[m.ID]) > 0:
			// The sweep leaves a track alone while a grab for it is in flight.
			row.State, row.WaitingOn = wantedWaiting, active[m.ID][0].Title
		default:
			st := states[m.ID]
			slot := automation.SweepSchedule(automation.AttemptMovie, st.LastAt, st.Misses)
			row.SearchMisses = st.Misses
			if !slot.Last.IsZero() {
				row.LastSearchAt = slot.Last.UTC().Format(time.RFC3339)
			}
			next := slot.Next
			if !next.After(now) {
				next, row.Due = now, true
			}
			row.NextSearchAt = next.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	if len(rows) > 0 {
		ids := make([]int64, len(rows))
		for i := range rows {
			ids[i] = rows[i].ID
		}
		if sums, err := a.deps.Automation.LatestAttempts(ctx, automation.AttemptMovie, ids); err == nil {
			for i := range rows {
				if s, ok := sums[rows[i].ID]; ok {
					rows[i].LastSearch = &s
				}
			}
		}
	}
	return rows, nil
}

// trackLabel names an extra track in a Wanted row.
func trackLabel(v movies.Version) string {
	if v.IsDefault {
		return "Main file"
	}
	if v.Label != "" {
		return v.Label
	}
	return "Extra version"
}

// moviesWantedCutoff builds the Cutoff unmet tab: one row per track whose file misses its
// profile's target, with the last upgrade search and, for the rows the upgrade sweep will
// act on, its next run.
func (a *api) moviesWantedCutoff(ctx context.Context) ([]movieWantedRow, bool, error) {
	cuts, err := a.deps.Automation.MoviesCutoffUnmet(ctx)
	if err != nil {
		return nil, false, err
	}
	_, queueKnown, _ := a.queueSnapshot(ctx)
	active, _ := a.deps.Automation.ActiveByItem(ctx, automation.AttemptMovie)
	ids := make([]int64, 0, len(cuts))
	for _, c := range cuts {
		ids = append(ids, c.Movie.ID)
	}
	sums, err := a.deps.Automation.LatestUpgradeAttempts(ctx, automation.AttemptMovie, ids)
	if err != nil {
		return nil, false, err
	}
	nextSweep := ""
	if a.deps.Scheduler != nil {
		if st, ok := a.deps.Scheduler.Status("upgrade-movies"); ok && st.NextRun != nil {
			nextSweep = st.NextRun.UTC().Format(time.RFC3339)
		}
	}
	names := a.profileNames(ctx)
	rows := make([]movieWantedRow, 0, len(cuts))
	for _, c := range cuts {
		m, v := c.Movie, c.Track
		row := movieWantedRow{
			wantedRow: wantedRow{MediaType: automation.AttemptMovie, ID: m.ID, MovieID: m.ID, Title: m.Title, Year: m.Year,
				PosterURL: m.PosterURL, QualityProfile: c.ProfileName},
			Queued: a.deps.Automation.MovieSearchBusy(m.ID), VersionID: v.ID, Detail: cutoffDetail(c),
			Issues: c.Fit.Issues, WillUpgrade: c.WillUpgrade, WhyNot: c.WhyNot,
		}
		if row.QualityProfile == "" {
			row.QualityProfile = names(c.Profile)
		}
		if !v.IsDefault {
			row.Track = trackLabel(v)
		}
		if s, ok := sums[m.ID]; ok {
			s := s
			row.LastSearch = &s
			row.LastSearchAt = time.UnixMilli(s.Latest.StartedAt).UTC().Format(time.RFC3339)
		}
		switch {
		case len(active[m.ID]) > 0:
			row.State, row.WaitingOn = wantedWaiting, active[m.ID][0].Title
		case c.WillUpgrade:
			row.State, row.NextSearchAt = wantedUpgrading, nextSweep
		default:
			row.State = wantedNotUpgrading
		}
		rows = append(rows, row)
	}
	return rows, queueKnown, nil
}

// cutoffDetail says what about a file misses its target: the fit issues, or — when the
// file fits the target but still isn't it — that it isn't there yet.
func cutoffDetail(c automation.MovieCutoff) string {
	msgs := make([]string, 0, len(c.Fit.Issues))
	for _, is := range c.Fit.Issues {
		msgs = append(msgs, is.Msg)
	}
	if len(msgs) == 0 {
		return "Not yet the profile's target file"
	}
	return strings.Join(msgs, "; ")
}

// profileNames resolves profile refs to their names for one request, once per ref.
func (a *api) profileNames(ctx context.Context) func(string) string {
	seen := map[string]string{}
	return func(ref string) string {
		if ref == "" || ref == "n/a" || a.deps.Quality == nil {
			return ""
		}
		if n, ok := seen[ref]; ok {
			return n
		}
		n := a.profileName(ctx, ref)
		seen[ref] = n
		return n
	}
}

// bulkSearchRequest is POST /api/v1/movies/search.
type bulkSearchRequest struct {
	IDs  []int64 `json:"ids"`
	Kind string  `json:"kind"` // missing | upgrade
}

// maxBulkSearch bounds one Search all; a library's Wanted list is well under it.
const maxBulkSearch = 5000

// handleBulkMovieSearch queues searches for many movies through the movie search queue.
// Kind missing searches for what each is missing; kind upgrade looks for a better release
// under one shared upgrade budget (Settings → Downloads, the limit one sweep has), so a
// Search all over hundreds of films grabs no more upgrades than a sweep would and the rest
// wait for the next one. Nothing fires at once: the queue runs two at a time. Answers 202
// {queued, duplicates}: how many were added, and how many were already queued or running.
func (a *api) handleBulkMovieSearch(w http.ResponseWriter, r *http.Request) {
	var req bulkSearchRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Kind != "missing" && req.Kind != "upgrade" {
		a.writeError(w, http.StatusBadRequest, "kind must be missing or upgrade")
		return
	}
	ids := uniqueIDs(req.IDs)
	if len(ids) == 0 || len(ids) > maxBulkSearch {
		a.writeError(w, http.StatusBadRequest, "ids must list between 1 and 5000 movies")
		return
	}
	if a.deps.Automation == nil {
		a.writeError(w, http.StatusServiceUnavailable, "searching isn't available")
		return
	}
	var batch *automation.UpgradeBatch
	if req.Kind == "upgrade" {
		batch = a.deps.Automation.NewUpgradeBatch(r.Context())
	}
	queued, duplicates := 0, 0
	for _, id := range ids {
		spec := a.movieBulkSearchJob(id)
		if batch != nil {
			spec = a.movieBulkUpgradeJob(id, batch)
		}
		q, err := a.enqueueMovie(r, id, spec)
		if err != nil {
			if queued == 0 && duplicates == 0 {
				a.writeError(w, http.StatusServiceUnavailable, "couldn't start that just now — try again in a moment")
				return
			}
			break // shutting down part-way: say what was queued
		}
		if q.Existing {
			duplicates++
		} else {
			queued++
		}
	}
	a.writeJSON(w, http.StatusAccepted, map[string]any{"queued": queued, "duplicates": duplicates})
}

// uniqueIDs drops repeats and non-positive ids, keeping the first order.
func uniqueIDs(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
