package movies

import (
	"regexp"
	"slices"
	"strings"
)

// episodeNumeral is "Episode VII" in a film's name, the way some releases number a saga.
var episodeNumeral = regexp.MustCompile(`(?i)\bepisode[\s.:_-]+(i{1,3}|iv|vi{0,3}|ix|xi{0,2})\b`)

// numeralOf is the saga number in title, lower-case; "" when it has none.
func numeralOf(title string) string {
	if m := episodeNumeral.FindStringSubmatch(title); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// withoutEpisodeNumeral is title with its "Episode VII" taken out; "" when it has none.
func withoutEpisodeNumeral(title string) string {
	if !episodeNumeral.MatchString(title) {
		return ""
	}
	return strings.TrimSpace(episodeNumeral.ReplaceAllString(title, " "))
}

// SagaBareName reports whether a download's name is m's own name but for a saga number
// ("Star Wars The Force Awakens Episode VII" for "Star Wars: The Force Awakens", #74), and
// gives the name without it, for the caller to check no other film goes by that.
//
// It only ever confirms the film a download was grabbed for, never picks one, so it isn't part
// of TitleKeys: a search or a by-name match reading "Some Film Episode II" as "Some Film" would
// take a different film. For the same reason the year has to be there and be exactly the film's,
// and a film listed with its own number has to carry the same one.
func SagaBareName(m Movie, title string, year int) (string, bool) {
	if year == 0 || year != m.Year {
		return "", false
	}
	bare := withoutEpisodeNumeral(title)
	if bare == "" {
		return "", false
	}
	key := normalizeTitle(bare)
	if len(key) < minAltKeyLen {
		return "", false
	}
	// The film may be the one listed with its number: "Star Wars: Episode VII - The Force Awakens".
	if own := numeralOf(m.Title); own != "" {
		if own != numeralOf(title) || normalizeTitle(withoutEpisodeNumeral(m.Title)) != key {
			return "", false
		}
		return bare, true
	}
	if !slices.Contains(TitleKeys(m, nil), key) {
		return "", false
	}
	return bare, true
}
