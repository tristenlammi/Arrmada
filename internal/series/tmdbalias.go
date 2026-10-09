package series

import (
	"context"
	"strings"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/parser"
)

// maxTMDBAliases caps the automatic aliases a show carries. Each can cost an indexer
// query (searches use at most two of them), and past the first few TMDB's alternative
// titles are translations no release is named after.
const maxTMDBAliases = 5

// pickTMDBAliases chooses which of TMDB's alternative titles a show is likely released
// under, best first: romaji names (what SubsPlease and Erai-raws use — "Sousou no
// Frieren"), then other Latin-script Japanese titles, then US and UK English variants.
// Titles in other scripts are skipped (no release carries them), as are titles that key
// the same as the display title or each other.
func pickTMDBAliases(display string, alts []metadata.AltTitle) []string {
	own := parser.TitleKey(display)
	seen := map[string]bool{own: true, "": true}
	var romaji, japan, variant []string
	for _, a := range alts {
		t := strings.TrimSpace(a.Title)
		k := parser.TitleKey(t)
		if seen[k] || !parser.IsLatin(t) {
			continue
		}
		typ := strings.ToLower(a.Type)
		switch {
		case strings.Contains(typ, "romaji") || strings.Contains(typ, "romani"):
			romaji = append(romaji, t)
		case a.Country == "JP":
			japan = append(japan, t)
		case a.Country == "US" || a.Country == "GB":
			variant = append(variant, t)
		default:
			continue
		}
		seen[k] = true
	}
	return append(append(romaji, japan...), variant...)
}

// syncTMDBAliases seeds a show's automatic aliases from TMDB's alternative titles, on Add
// and on every Refresh. It only ever adds rows: the owner's own aliases are untouched (a
// key they already have is skipped, so a season they pinned stays pinned), an automatic
// alias they removed is never re-added, and no alias is taken that another library show
// goes by — two shows answering to one name is how releases cross over.
func (s *Service) syncTMDBAliases(ctx context.Context, id int64, d *metadata.SeriesDetails) {
	if d == nil || len(d.AltTitles) == 0 {
		return
	}
	picks := pickTMDBAliases(d.Title, d.AltTitles)
	if len(picks) == 0 {
		return
	}
	have := map[string]bool{}
	auto := 0
	for _, a := range s.repo.AllAliases(ctx, id) {
		have[a.Key()] = true
		if a.Auto() {
			auto++
		}
	}
	taken, err := s.repo.TakenTitleKeys(ctx, id)
	if err != nil {
		s.log.Warn("series: could not check other shows' titles — not adding TMDB aliases", "series", d.Title, "err", err)
		return
	}
	var added []string
	for _, t := range picks {
		if auto >= maxTMDBAliases {
			break
		}
		k := parser.TitleKey(t)
		if have[k] || taken[k] {
			continue
		}
		ok, err := s.repo.AddAutoAlias(ctx, id, t, k)
		if err != nil {
			s.log.Warn("series: could not add a TMDB alias", "series", d.Title, "alias", t, "err", err)
			continue
		}
		if ok {
			have[k] = true
			auto++
			added = append(added, t)
		}
	}
	if len(added) > 0 {
		s.log.Info("series: added alternate titles from TMDB", "series", d.Title, "aliases", strings.Join(added, " | "))
	}
}
