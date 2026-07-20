package metadata

import (
	"context"
	"fmt"
	"log/slog"
)

// EpisodeSource supplies a show's season/episode listing from somewhere other than the
// primary provider. Implemented by TVmaze.
type EpisodeSource interface {
	Available() bool
	Episodes(ctx context.Context, tvdbID int, imdbID string) ([]SeasonDetails, error)
}

// seriesWithEpisodeSource keeps the primary provider for everything about a SHOW —
// artwork, overview, status, discovery, search — and takes only the EPISODE LISTING from
// a second source.
//
// The split exists because the two jobs have different best answers. TMDB is the better
// catalogue; it is not the source releases are numbered against. Taking numbering from a
// source that follows the release convention removes an entire class of misplacement,
// without giving up anything TMDB is good at.
type seriesWithEpisodeSource struct {
	primary SeriesProvider
	sources []EpisodeSource // tried in order; the first usable, compatible listing wins
	log     *slog.Logger
}

// NewSeriesWithEpisodes wraps a provider so episode numbering comes from the first of
// sources that can supply a usable, compatible listing. Sources are tried in priority
// order — e.g. TVDB (authoritative, needs a key) before TVmaze (free).
//
// Availability is NOT decided here. A source's Available() is checked per request, in
// GetSeries — because keys can be added in the settings menu while the app is running.
// Deciding at construction (startup) would drop TVDB for the whole process whenever it
// booted without a key, so a key added later did nothing until a restart: exactly the
// trap that left an anime library on TMDB's numbering after the key was entered.
func NewSeriesWithEpisodes(primary SeriesProvider, log *slog.Logger, sources ...EpisodeSource) SeriesProvider {
	present := make([]EpisodeSource, 0, len(sources))
	for _, src := range sources {
		if src != nil {
			present = append(present, src)
		}
	}
	if len(present) == 0 {
		return primary
	}
	return &seriesWithEpisodeSource{primary: primary, sources: present, log: log}
}

func (s *seriesWithEpisodeSource) Available() bool { return s.primary.Available() }

func (s *seriesWithEpisodeSource) SearchSeries(ctx context.Context, query string) ([]SeriesResult, error) {
	return s.primary.SearchSeries(ctx, query)
}

func (s *seriesWithEpisodeSource) GetSeries(ctx context.Context, tmdbID int) (*SeriesDetails, error) {
	d, err := s.primary.GetSeries(ctx, tmdbID)
	if err != nil || d == nil {
		return d, err
	}
	// Nothing to match on — sources are keyed by external ids the primary supplies, so
	// without one there's no lookup to make.
	if d.TVDBID == 0 && d.IMDBID == "" {
		return d, nil
	}
	for _, src := range s.sources {
		if !src.Available() {
			continue // e.g. no key configured right now — re-checked every request so a key
			// added in settings takes effect on the next add or refresh, without a restart.
		}
		seasons, err := src.Episodes(ctx, d.TVDBID, d.IMDBID)
		if err != nil {
			// A numbering source being down must never stop a show being added or
			// refreshed — just move to the next source, then to the primary.
			s.log.Warn("episode numbering source failed — trying the next", "title", d.Title, "err", err)
			continue
		}
		if !usableListing(seasons) {
			continue // not carried here, or too thin to trust
		}
		if why, ok := incompatibleNumbering(seasons, d.Seasons); !ok {
			s.log.Info("skipping a numbering source that models this show differently",
				"title", d.Title, "reason", why)
			continue
		}
		d.Seasons = mergeSeasonArt(seasons, d.Seasons)
		return d, nil
	}
	return d, nil // no source usable — keep the primary's listing
}

// usableListing rejects an empty or obviously incomplete reply. Replacing a full listing
// with a broken one would mark most of a library's episodes as nonexistent, which is far
// worse than numbering that's occasionally a slot out.
func usableListing(seasons []SeasonDetails) bool {
	for _, sn := range seasons {
		if len(sn.Episodes) > 0 {
			return true
		}
	}
	return false
}

// incompatibleNumbering rejects a listing that uses a different season MODEL, rather than
// merely different episode boundaries.
//
// TVmaze numbers some long-running shows by broadcast year — Naruto comes back as seasons
// 2002 through 2007 — which is a coherent scheme but not the one releases use, and not one
// that can be reconciled with the primary's. Importing it would leave a library with
// "Season 2002" alongside the real seasons and nothing able to match a file to either.
//
// The test is deliberately structural, not a guess at which is "right": season numbers
// that look like years, or a set that shares no season at all with what the primary
// already has. Different episode COUNTS within shared seasons are exactly what this
// feature is for and are left alone.
func incompatibleNumbering(from, primary []SeasonDetails) (string, bool) {
	for _, sn := range from {
		if sn.SeasonNumber >= 1900 {
			return fmt.Sprintf("seasons are numbered by year (found season %d)", sn.SeasonNumber), false
		}
	}
	if len(primary) == 0 {
		return "", true // nothing to compare against
	}
	have := make(map[int]bool, len(primary))
	for _, sn := range primary {
		have[sn.SeasonNumber] = true
	}
	for _, sn := range from {
		if have[sn.SeasonNumber] {
			return "", true // they agree on at least one season — same model
		}
	}
	return "no season in common with the primary listing", false
}

// mergeSeasonArt keeps the primary's season-level name, overview and poster — the second
// source owns numbering, not presentation, and TMDB's season artwork is better.
func mergeSeasonArt(from, primary []SeasonDetails) []SeasonDetails {
	art := make(map[int]SeasonDetails, len(primary))
	for _, sn := range primary {
		art[sn.SeasonNumber] = sn
	}
	out := make([]SeasonDetails, 0, len(from))
	for _, sn := range from {
		if a, ok := art[sn.SeasonNumber]; ok {
			sn.Name, sn.Overview, sn.PosterURL = a.Name, a.Overview, a.PosterURL
		}
		out = append(out, sn)
	}
	return out
}
