package indexer

import (
	"regexp"
	"strings"
)

// Adult-content safety filter. Arrmada is a family media manager — releases that
// look like porn must never surface in search results or be auto-grabbed, on any
// indexer. This is a hardcoded, always-on filter (not a toggle) so it can't be
// switched off by accident. It errs toward blocking: a rare legitimate title that
// trips it can still be added by exact search, but porn never leaks through.

// adultStudios are unambiguous adult-studio / site tokens that don't occur in
// mainstream movie or TV release names. Matched as whole words after separators
// (. _ -) are normalized to spaces.
var adultStudios = []string{
	"blacked", "blackedraw", "vixen", "tushy", "tushyraw", "brazzers", "legalporno",
	"realitykings", "bangbros", "naughtyamerica", "evilangel", "mofos", "teamskeet",
	"nubilefilms", "nubiles", "hardx", "darkx", "wowgirls", "twistys", "metart",
	"sexart", "21sextury", "girlsway", "adulttime", "familystrokes", "cumlouder",
	"fakehub", "fakotaxi", "propertysex", "publicagent", "pornhub", "brazzersexxtra",
	"xvideos", "onlyfans", "manyvids", "chaturbate", "digitalplayground", "wicked pictures",
	"reality kings", "bang bros", "naughty america", "evil angel", "team skeet",
	"digital playground", "adult time", "family strokes", "public agent", "property sex",
}

// adultActs are explicit sex-act terms that strongly indicate porn and don't
// appear in mainstream titles (word-boundary matched, so "Analyze" ≠ "anal").
// Deliberately excludes ambiguous tokens like "xxx" (the xXx films) and "gonzo"
// (a documentary) — the XXX indexer category and studio names cover those.
var adultActs = []string{
	"porn", "hardcore porn", "creampie", "gangbang", "bukkake", "cumshot", "deepthroat",
	"blowjob", "handjob", "footjob", "threesome", "anal", "milf",
}

var reAdult = regexp.MustCompile(`(?i)\b(` + strings.Join(append(append([]string{}, adultStudios...), adultActs...), "|") + `)\b`)

// sepNorm collapses release-name separators to spaces so "Reality.Kings" and
// "Reality_Kings" match the "reality kings" term.
var sepNorm = strings.NewReplacer(".", " ", "_", " ", "-", " ")

// isAdultRelease reports whether a release looks like adult content, by its title
// or by an XXX indexer category (Newznab/Torznab 6000–6999).
func isAdultRelease(title string, categories []int) bool {
	for _, c := range categories {
		if c >= 6000 && c < 7000 {
			return true
		}
	}
	return reAdult.MatchString(sepNorm.Replace(title))
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
