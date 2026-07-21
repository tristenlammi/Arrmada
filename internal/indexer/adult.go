package indexer

import (
	"github.com/tristenlammi/arrmada/internal/adultfilter"
)

// Adult-content safety filter for indexer results. The term list and matching
// live in internal/adultfilter, shared with the metadata provider so scene
// release names and TMDB discovery titles are judged by exactly one rule with no
// drift; the XXX category check below is indexer-specific.

// isAdultRelease reports whether a release looks like adult content, by its title
// or by an XXX indexer category (Newznab/Torznab 6000–6999).
func isAdultRelease(title string, categories []int) bool {
	for _, c := range categories {
		if c >= 6000 && c < 7000 {
			return true
		}
	}
	return adultfilter.Matches(title)
}

// filterAdult drops adult releases from a result set (in place).
func filterAdult(releases []Release) []Release {
	kept := releases[:0]
	for _, r := range releases {
		if isAdultRelease(r.Title, r.Categories) {
			continue
		}
		kept = append(kept, r)
	}
	return kept
}
