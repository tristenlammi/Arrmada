package series

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// Release groups mark an arc's cours in ways the release parser doesn't model, because
// they aren't season numbers: "(Cour 03)", "Part 3 E03". Left unread, such a release
// carries no numbering at all and would be taken for a pack of the whole arc — so a
// cour-3 pack gets offered for an episode in cour 4.
var (
	courRe    = regexp.MustCompile(`(?i)\b(?:cour|part)[\s._-]*0*(\d{1,2})\b`)
	looseEpRe = regexp.MustCompile(`(?i)\bE0*(\d{1,3})\b`)
)

// courAndEpisode reads a cour marker, and an episode within it when present.
func courAndEpisode(title string) (cour, episode int) {
	m := courRe.FindStringSubmatch(title)
	if m == nil {
		return 0, 0
	}
	cour, _ = strconv.Atoi(m[1])
	// Only look for an episode AFTER the cour marker, so a "Part 3" in the show's name
	// can't pair with an unrelated number earlier in the string.
	rest := title[strings.Index(title, m[0])+len(m[0]):]
	if em := looseEpRe.FindStringSubmatch(rest); em != nil {
		episode, _ = strconv.Atoi(em[1])
	}
	return cour, episode
}

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
//
// Matching is a whole-word PREFIX, not equality, because release groups append to an
// arc's name freely: a per-cour subtitle ("Thousand-Year Blood War The Calamity"), or
// junk the parser didn't strip ("Thousand-Year Blood War [BD Remux 1080p ...]"). One
// alias should cover the arc rather than needing a row per cour.
//
// When several aliases match, the longest wins, so a more specific alias can override a
// broader one on the same series.
func (s *Service) AliasFor(ctx context.Context, seriesID int64, releaseTitle string) (Alias, bool) {
	title := parser.Parse(releaseTitle).Title
	if strings.TrimSpace(title) == "" {
		return Alias{}, false
	}
	var best Alias
	bestLen := 0
	for _, a := range s.repo.Aliases(ctx, seriesID) {
		if !parser.TitleHasPrefix(title, a.Title) {
			continue
		}
		if n := len(parser.TitleWords(a.Title)); n > bestLen {
			best, bestLen = a, n
		}
	}
	return best, bestLen > 0
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
			return nil, false
		}
		// A cour marker the release parser doesn't model — "(Cour 03)", "Part 3 E03".
		// Read it before assuming the release covers the whole arc, or a single cour's
		// pack is offered for episodes it doesn't contain.
		if cour, ep := courAndEpisode(r.Title); cour > 0 {
			group := s.courOf(ctx, seriesID, a.TMDBSeason, cour)
			if len(group) == 0 {
				return nil, false
			}
			if ep > 0 {
				if ep > len(group) {
					return nil, false
				}
				return []EpisodeRef{group[ep-1]}, true
			}
			return group, true
		}
		// The number straight after the arc's name, which is how this naming works:
		// "Bleach Thousand Year Blood War 15 1080p DSNP Web-Dl ...". The release parser
		// finds nothing here — there's no SxxExx and no " - 15 " separator — so it
		// swallows the number into the title and the release looks like a pack.
		if n, ok := numberAfterAlias(r.Title, a.Title); ok {
			season := s.repo.SeasonEpisodeNumbers(ctx, seriesID, a.TMDBSeason)
			if n >= 1 && n <= len(season) {
				return []EpisodeRef{{Season: a.TMDBSeason, Episode: season[n-1]}}, true
			}
			return nil, false
		}
		// Nothing left to go on. This used to claim the WHOLE pinned season, on the
		// theory that a release named only for the arc must be a pack of it. A real one
		// disproved that: "Bleach - Thousand-Year Blood War [BD Remux 1080p AVC DTS-HD
		// MA]" holds episodes 27-40, and claiming all 52 offered it as a match for
		// episode 15 — 78GB of entirely the wrong episodes.
		//
		// Declining is the safe answer. A pack whose contents can't be read from its
		// name isn't something to grab on an assumption.
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

// seasonRefs is every episode of one season, in order.
func (s *Service) seasonRefs(ctx context.Context, seriesID int64, tmdbSeason int) []EpisodeRef {
	nums := s.repo.SeasonEpisodeNumbers(ctx, seriesID, tmdbSeason)
	out := make([]EpisodeRef, 0, len(nums))
	for _, n := range nums {
		out = append(out, EpisodeRef{Season: tmdbSeason, Episode: n})
	}
	return out
}

// numberAfterAlias reads the bare episode number that follows the arc's name.
//
// "Bleach Thousand Year Blood War 15 1080p ..." → 15. Compared word by word against the
// alias so the number has to sit immediately after it: a digit anywhere else in the
// string is a year, a resolution, a codec or a group tag.
//
// A year directly after the name ("... Blood War 2022 S01 ...") would read as episode
// 2022; the caller bounds the result by the season's episode count, which rejects it.
func numberAfterAlias(releaseTitle, aliasTitle string) (int, bool) {
	alias := parser.TitleWords(aliasTitle)
	words := parser.TitleWords(releaseTitle)
	if len(alias) == 0 || len(words) <= len(alias) {
		return 0, false
	}
	for i, w := range alias {
		if words[i] != w {
			return 0, false
		}
	}
	next := words[len(alias)]
	n, err := strconv.Atoi(next)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}
