package series

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// ReleaseFit is how a parsed release relates to one series: whether its title names the
// show at all, and whether its year and country tag agree with it.
//
// The title alone can't tell same-named shows apart. The parser takes the year out of
// "Doctor.Who.2005" and the key ignores brackets, so the 1963 and 2005 Doctor Who, or the
// US and UK Office, share one title key. The year and the country tag are what separate
// them.
type ReleaseFit struct {
	// Title: the release's title names this show — its own title, its original title
	// (anime), its title with the release's country tag taken off, or one of its aliases.
	Title bool
	// Alias: the title matched only through an alias. The owner's aliases are deliberate
	// names for this one show, so the year and country checks don't apply to them; an
	// automatic alias gets the same checks as the show's own title.
	Alias bool
	// OK: Title, and the year and country agree (or the release doesn't name them).
	OK bool
	// Year / Country: the release names a year or country and it is this show's — positive
	// evidence that picks between same-named shows.
	Year, Country bool
	// Why says what disagreed when Title holds but OK doesn't.
	Why string
}

// reTitleYear finds a year written into a show's own title ("1923", "Star Wars 2077").
var reTitleYear = regexp.MustCompile(`\b(?:19|20)\d{2}\b`)

// FitRelease judges a parsed release against a series. It is the one identity rule behind
// every series match — the sweeps, RSS, interactive search, upgrades, the in-flight check
// and import routing — so they can't disagree about which show a release is.
//
// Three things must hold:
//   - the title key matches the show's title, its Latin-script original title (anime) or
//     an automatic alias exactly, or one of the owner's aliases as a whole-word prefix;
//   - the release's title year (parser.Release.TitleYear) is absent, or the show's year is
//     unknown, or the two are within a year of each other, or the show's title itself has a
//     year in it;
//   - the release's country tag is absent, or it is one of the show's origin countries. A
//     title that only matches once the tag is taken off ("The Office US" for "The Office")
//     needs the country to be known and to agree.
//
// The year and country checks apply to the show's own titles and its automatic aliases;
// the owner's aliases are deliberate names for one show and skip them.
func FitRelease(p parser.Release, s Series) ReleaseFit {
	key := parser.TitleKey(p.Title)
	if key == "" {
		return ReleaseFit{}
	}
	base, cc := parser.SplitCountry(p.Title)
	baseKey := ""
	if cc != "" {
		baseKey = parser.TitleKey(base)
	}
	// The show's own titles first, then its automatic aliases — both exact, both with the
	// year and country checks — and only then the owner's aliases.
	if f, ok := s.fitTitles(p, key, baseKey, cc, s.ownTitles()); ok {
		return f
	}
	if f, ok := s.fitTitles(p, key, baseKey, cc, s.autoAliasTitles()); ok {
		f.Alias = true
		return f
	}
	if s.userAliasMatches(p.Title) {
		return ReleaseFit{Title: true, Alias: true, OK: true}
	}
	return ReleaseFit{}
}

// fitTitles judges a release against titles that must match exactly (or once the
// release's country tag is taken off), with the year and country checks. ok is false when
// none of the titles is the release's.
func (s Series) fitTitles(p parser.Release, key, baseKey, cc string, titles []string) (ReleaseFit, bool) {
	matched, exact := "", false
	for _, t := range titles {
		k := parser.TitleKey(t)
		if k == "" {
			continue
		}
		if k == key {
			matched, exact = t, true
			break
		}
		if baseKey != "" && k == baseKey && matched == "" {
			matched = t
		}
	}
	if matched == "" {
		return ReleaseFit{}, false
	}
	f := ReleaseFit{Title: true}
	if cc != "" {
		fits, known := s.countryFits(cc, matched)
		switch {
		case fits:
			f.Country = known
		case !known && exact:
			// Origin not stored yet (not refreshed since it was): the title matched as
			// written, which is how it always matched, so don't start refusing it.
		default:
			if known {
				f.Why = fmt.Sprintf("tagged %s, but the show is from %s", cc, strings.Join(s.Extra.OriginCountry, ", "))
			} else {
				f.Why = fmt.Sprintf("tagged %s, and the show's country isn't known yet", cc)
			}
			return f, true
		}
	}
	if p.TitleYear > 0 && s.Year > 0 && !reTitleYear.MatchString(s.Title) {
		if d := p.TitleYear - s.Year; d < -1 || d > 1 {
			f.Why = fmt.Sprintf("the release is the %d show, this one is from %d", p.TitleYear, s.Year)
			return f, true
		}
		f.Year = true
	}
	f.OK = true
	return f, true
}

// ownTitles are the show's own names: its title, and for anime its original title, since
// anime is often released under it — but only a Latin-script one. TMDB's original_name for
// a Japanese show is kana or kanji, which no release carries; the romaji name comes from
// the automatic aliases instead.
func (s Series) ownTitles() []string {
	out := []string{s.Title}
	if s.IsAnime() && s.Extra != nil && s.Extra.OriginalTitle != "" && parser.IsLatin(s.Extra.OriginalTitle) {
		out = append(out, s.Extra.OriginalTitle)
	}
	return out
}

// autoAliasTitles are the aliases added automatically (TMDB's alternative titles, a
// renamed show's old title).
func (s Series) autoAliasTitles() []string {
	var out []string
	for _, a := range s.Aliases {
		if a.Auto() {
			out = append(out, a.Title)
		}
	}
	return out
}

// countryFits reports whether a release's country tag agrees with the show. known is
// false when the show's origin isn't stored, in which case fits is false too. A title that
// itself ends in the same tag ("This Is Us", "Shameless US") always fits: the tag is part
// of the name.
func (s Series) countryFits(cc, matched string) (fits, known bool) {
	if _, own := parser.SplitCountry(matched); own == cc {
		return true, false
	}
	if s.Extra == nil || len(s.Extra.OriginCountry) == 0 {
		return false, false
	}
	for _, c := range s.Extra.OriginCountry {
		if c == cc || (c == "UK" && cc == "GB") {
			return true, true
		}
	}
	return false, true
}

// userAliasMatches reports whether a release title matches one of the owner's aliases: a
// whole-word prefix, so an arc's alias survives a per-cour subtitle or junk the parser left
// ("Thousand-Year Blood War The Calamity"). The word boundary keeps "Bleach" off
// "Bleachers". Only the owner's aliases get this; automatic ones match exactly.
func (s Series) userAliasMatches(title string) bool {
	for _, a := range s.Aliases {
		if !a.Auto() && parser.TitleHasPrefix(title, a.Title) {
			return true
		}
	}
	return false
}

// ReleaseMatcher indexes a library snapshot once for many releases (the Downloads feed,
// an import sweep) and finds the show a parsed release belongs to, with the same rules as
// MatchRelease.
func (s *Service) ReleaseMatcher(all []Series) func(p parser.Release) (Series, bool, []Series) {
	return func(p parser.Release) (Series, bool, []Series) { return matchRelease(all, p) }
}

// MatchRelease finds the library show a parsed release belongs to, for import routing.
//
// Among the shows the release's title names, it takes the one whose year and country fit.
// When several fit, the one the release positively identifies wins (its year or its country
// tag agrees); a show matched by its own title beats one matched only through an alias.
// When that still leaves more than one — a yearless "Doctor.Who.S01E01" with both Doctor
// Whos in the library — nothing is matched and the candidates come back, so the caller can
// name them instead of guessing the newest.
func (s *Service) MatchRelease(ctx context.Context, p parser.Release) (Series, bool, []Series) {
	all, err := s.List(ctx)
	if err != nil {
		return Series{}, false, nil
	}
	return matchRelease(all, p)
}

func matchRelease(all []Series, p parser.Release) (Series, bool, []Series) {
	type cand struct {
		s   Series
		fit ReleaseFit
	}
	var own, alias []cand
	for _, sr := range all {
		f := FitRelease(p, sr)
		if !f.OK {
			continue
		}
		if f.Alias {
			alias = append(alias, cand{sr, f})
		} else {
			own = append(own, cand{sr, f})
		}
	}
	pick := func(cs []cand) (Series, bool, []Series) {
		if len(cs) == 1 {
			return cs[0].s, true, nil
		}
		var sure []cand
		for _, c := range cs {
			if c.fit.Year || c.fit.Country {
				sure = append(sure, c)
			}
		}
		if len(sure) == 1 {
			return sure[0].s, true, nil
		}
		out := make([]Series, len(cs))
		for i, c := range cs {
			out[i] = c.s
		}
		return Series{}, false, out
	}
	if len(own) > 0 {
		return pick(own)
	}
	if len(alias) > 0 {
		return pick(alias)
	}
	return Series{}, false, nil
}

// DescribeCandidates names same-titled shows for a review reason: "Doctor Who (1963),
// Doctor Who (2005)".
func DescribeCandidates(cands []Series) string {
	parts := make([]string, 0, len(cands))
	for _, c := range cands {
		if c.Year > 0 {
			parts = append(parts, fmt.Sprintf("%s (%d)", c.Title, c.Year))
		} else {
			parts = append(parts, c.Title)
		}
	}
	return strings.Join(parts, ", ")
}
