// Package adultfilter is Arrmada's shared adult-content safety net.
//
// Arrmada is a family media manager: porn must never surface in indexer results,
// discovery rows, or search — on any source. This is a hardcoded, always-on
// filter (not a toggle) so it can't be switched off by accident. It errs toward
// blocking: a rare legitimate title that trips it can still be added by exact
// search against a metadata provider, but porn never leaks through.
//
// It lives in its own package because two very different layers need the same
// judgement: the indexer (matching scene release names) and the metadata
// provider (matching TMDB titles). One list, one rule, no drift.
package adultfilter

import (
	"regexp"
	"strings"
)

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
// appear in mainstream titles (word-boundary matched, so "Analyze" != "anal").
//
// Deliberately excludes ambiguous tokens: bare "sex" would kill "Sex Education",
// "Sex and the City" and "Sex, Lies, and Videotape"; "xxx" would kill the xXx
// films; "gonzo" is a documentary. The studio names and the provider's own adult
// flag cover those cases instead.
var adultActs = []string{
	"porn", "hardcore porn", "creampie", "gangbang", "bukkake", "cumshot", "deepthroat",
	"blowjob", "handjob", "footjob", "threesome", "anal", "milf",
}

var reAdult = regexp.MustCompile(`(?i)\b(` + strings.Join(append(append([]string{}, adultStudios...), adultActs...), "|") + `)\b`)

// sepNorm collapses release-name separators to spaces so "Reality.Kings" and
// "Reality_Kings" match the "reality kings" term.
var sepNorm = strings.NewReplacer(".", " ", "_", " ", "-", " ")

// Matches reports whether a title or release name looks like adult content.
func Matches(text string) bool {
	return reAdult.MatchString(sepNorm.Replace(text))
}
