package series

import (
	"context"
	"fmt"
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// Alias is an alternate title a series is released under.
//
// Anime arcs are routinely published as if they were a separate show: Bleach's
// Thousand-Year Blood War ships as "BLEACH Thousand-Year Blood War S02E02" and as
// "[SubsPlease] Bleach - Sennen Kessen Hen - 15", while TMDB folds the whole arc into
// Bleach season 17 numbered continuously 1–52. Without an alias none of those titles
// match the series at all, and they are dropped before they are ever scored.
type Alias struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	// TMDBSeason pins where the alias' numbering lands.
	//
	//	0  — title-only: the release is recognised as this series and numbered as usual.
	//	>0 — the alias' numbers are read INSIDE that season. "S02E02" then means the
	//	     second cour's second episode of that season, not the series' own season 2.
	TMDBSeason int `json:"tmdb_season"`
}

// Key is the normalized form used for matching, shared with the release parser so an
// alias matches exactly the way a real title does.
func (a Alias) Key() string { return parser.TitleKey(a.Title) }

// Aliases returns a series' alternate titles.
func (s *Service) Aliases(ctx context.Context, seriesID int64) []Alias {
	return s.repo.Aliases(ctx, seriesID)
}

// AddAlias records an alternate title. season 0 means title-only.
func (s *Service) AddAlias(ctx context.Context, seriesID int64, title string, season int) (Alias, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Alias{}, fmt.Errorf("an alias needs a title")
	}
	key := parser.TitleKey(title)
	if key == "" {
		return Alias{}, fmt.Errorf("%q has no matchable words in it", title)
	}
	sr, err := s.repo.Get(ctx, seriesID)
	if err != nil {
		return Alias{}, err
	}
	// An alias identical to the real title is a no-op at best; at worst the user thinks
	// they've fixed the numbering when the base-title path is what will actually run.
	if key == parser.TitleKey(sr.Title) {
		return Alias{}, fmt.Errorf("that's already this series' own title — an alias is for the *other* names it's released under")
	}
	if season != 0 && !s.repo.SeasonExists(ctx, seriesID, season) {
		return Alias{}, fmt.Errorf("this series has no season %d to map onto", season)
	}
	a, err := s.repo.AddAlias(ctx, seriesID, title, key, season)
	if err != nil {
		return Alias{}, err
	}
	s.log.Info("series: alias added", "series", sr.Title, "alias", title, "tmdb_season", season)
	return a, nil
}

// DeleteAlias removes an alternate title.
func (s *Service) DeleteAlias(ctx context.Context, seriesID, aliasID int64) error {
	return s.repo.DeleteAlias(ctx, seriesID, aliasID)
}

// AliasFor returns the alias a release title matches, if any.
func (s *Service) AliasFor(ctx context.Context, seriesID int64, releaseTitle string) (Alias, bool) {
	key := parser.TitleKey(parser.Parse(releaseTitle).Title)
	if key == "" {
		return Alias{}, false
	}
	for _, a := range s.repo.Aliases(ctx, seriesID) {
		if a.Key() == key {
			return a, true
		}
	}
	return Alias{}, false
}

// AliasEpisodes resolves a release against a season-pinned alias, returning the TMDB
// episodes it covers. The bool reports whether an alias took responsibility for the
// numbering — false means "not ours, resolve it the normal way", which is what keeps
// this inert for every series that has no aliases.
//
// One rule covers every convention seen in the wild, because the arc lives inside a
// single TMDB season:
//
//	"S02E02"  → second cour of that season, episode 2
//	"S01E41"  → cour 1 has no episode 41, so read 41 as an index into the season
//	"- 45"    → no season at all: index 45 into the season
func (s *Service) AliasEpisodes(ctx context.Context, seriesID int64, r parser.Release) ([]EpisodeRef, bool) {
	a, ok := s.AliasFor(ctx, seriesID, r.Title)
	if !ok || a.TMDBSeason <= 0 {
		return nil, false // no alias, or a title-only one that doesn't touch numbering
	}
	nums := r.Episodes
	if len(nums) == 0 {
		nums = r.AbsoluteEpisodes
	}
	if len(nums) == 0 {
		// A whole-cour pack ("Thousand-Year Blood War S02"): every episode in that cour.
		if r.Season > 0 {
			if cour := s.courOf(ctx, seriesID, a.TMDBSeason, r.Season); len(cour) > 0 {
				return cour, true
			}
		}
		return nil, false
	}

	season := s.repo.SeasonEpisodeNumbers(ctx, seriesID, a.TMDBSeason)
	if len(season) == 0 {
		return nil, false
	}
	cour := s.courOf(ctx, seriesID, a.TMDBSeason, r.Season)

	var out []EpisodeRef
	for _, n := range nums {
		if n < 1 {
			continue
		}
		// Prefer the cour reading when the release names a season and the cour actually
		// has that episode. Xiangliu's "S01E41" names season 1 but numbers continuously,
		// and cour 1 has only 13 episodes — so it falls through to the index reading and
		// lands correctly rather than being dropped.
		if r.Season > 0 && n <= len(cour) {
			out = append(out, cour[n-1])
			continue
		}
		if n <= len(season) {
			out = append(out, EpisodeRef{Season: a.TMDBSeason, Episode: season[n-1]})
		}
	}
	return out, len(out) > 0
}

// courOf returns the nth broadcast run within a season, split on air-date gaps.
//
// Scoped to ONE season on purpose. The series-wide grouping used elsewhere would, for
// a show like Bleach, count blocks across 366 episodes of the 2004 run before it ever
// reached the arc — "cour 4" would land in 2006. Cour boundaries are never hardcoded:
// a six-month broadcast break is visible in the air dates already in the database.
func (s *Service) courOf(ctx context.Context, seriesID int64, tmdbSeason, cour int) []EpisodeRef {
	if cour < 1 {
		return nil
	}
	groups := s.seasonCours(ctx, seriesID, tmdbSeason)
	if cour > len(groups) {
		return nil
	}
	return groups[cour-1]
}

// seasonCours splits one season's episodes into broadcast runs on air-date gaps. A
// season that aired straight through comes back as a single run, which is the correct
// answer for "cour 1" on a show that has only one.
func (s *Service) seasonCours(ctx context.Context, seriesID int64, tmdbSeason int) [][]EpisodeRef {
	var eps []epAir
	for _, e := range s.repo.OrderedEpisodes(ctx, seriesID) {
		if e.season == tmdbSeason {
			eps = append(eps, e)
		}
	}
	if len(eps) == 0 {
		return nil
	}
	var groups [][]EpisodeRef
	cur := []EpisodeRef{{Season: eps[0].season, Episode: eps[0].episode}}
	for i := 1; i < len(eps); i++ {
		// Split on every real break rather than on the k-1 largest, because how many
		// cours a season has is exactly what we're trying to discover.
		if daysBetween(eps[i-1].airDate, eps[i].airDate) >= minSceneGapDays {
			groups = append(groups, cur)
			cur = nil
		}
		cur = append(cur, EpisodeRef{Season: eps[i].season, Episode: eps[i].episode})
	}
	return append(groups, cur)
}
