package metadata

import (
	"context"
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
	eps     EpisodeSource
	log     *slog.Logger
}

// NewSeriesWithEpisodes wraps a provider so episode numbering comes from eps. A nil or
// unavailable eps, a lookup failure, or a show the second source doesn't carry all fall
// back to the primary's own listing — the show still works, just with its old numbering.
func NewSeriesWithEpisodes(primary SeriesProvider, eps EpisodeSource, log *slog.Logger) SeriesProvider {
	if eps == nil || !eps.Available() {
		return primary
	}
	return &seriesWithEpisodeSource{primary: primary, eps: eps, log: log}
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
	// Nothing to match on — the second source is keyed by external ids the primary
	// supplies, so without one there's no lookup to make.
	if d.TVDBID == 0 && d.IMDBID == "" {
		return d, nil
	}
	seasons, err := s.eps.Episodes(ctx, d.TVDBID, d.IMDBID)
	if err != nil {
		// A numbering source being down must never stop a show being added or refreshed.
		s.log.Warn("episode numbering source unavailable — keeping the primary's numbering",
			"title", d.Title, "tvdb_id", d.TVDBID, "err", err)
		return d, nil
	}
	if !usableListing(seasons) {
		return d, nil // not carried there, or too thin to trust — keep what we have
	}
	d.Seasons = mergeSeasonArt(seasons, d.Seasons)
	return d, nil
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
