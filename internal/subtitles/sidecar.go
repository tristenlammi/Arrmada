// Package subtitles is the Subtitles module (Bazarr's domain): it owns all subtitle work —
// detecting/extracting/downloading/generating external SRT sidecars for the movie & episode
// files in the shared Movies and Series catalogs. Subtitle presence is derived from disk
// (sidecar files), so there's no separate "wanted" table to keep in sync.
package subtitles

import (
	"os"
	"path/filepath"
	"strings"
)

// subExts are the external subtitle extensions we recognize as already-present.
var subExts = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".vtt": true}

// langAliases maps full language names to their ISO 639-1 code, so a "<name>.english.srt" sidecar
// is recognised as English.
var langAliases = map[string]string{
	"english": "en", "spanish": "es", "french": "fr", "german": "de", "italian": "it",
	"portuguese": "pt", "dutch": "nl", "swedish": "sv", "polish": "pl", "russian": "ru",
	"turkish": "tr", "arabic": "ar", "hindi": "hi", "japanese": "ja", "korean": "ko", "chinese": "zh",
}

// knownLangs is the set of tokens we accept as a language tag (2- and 3-letter codes + full names),
// used to tell a real language segment ("eng") from a title word ("burgundy").
var knownLangs = func() map[string]bool {
	m := map[string]bool{}
	for two, three := range twoToThree {
		m[two] = true
		m[three] = true
	}
	for full := range langAliases {
		m[full] = true
	}
	return m
}()

// normLang folds a full language name to its code; codes pass through unchanged.
func normLang(tok string) string {
	if c, ok := langAliases[tok]; ok {
		return c
	}
	return tok
}

// Subtitle variants. A forced track holds only the foreign-language parts (signs, a line of
// Klingon) and is never the full subtitle; SDH is the full dialogue plus sound cues, so it
// covers the language, just as a second choice.
const (
	VariantFull   = ""
	VariantForced = "forced"
	VariantSDH    = "sdh"
)

// LangVariant is one sidecar's language and variant.
type LangVariant struct {
	Lang    string `json:"lang"`              // "" = no recognisable language tag
	Variant string `json:"variant,omitempty"` // "" (full) | forced | sdh
}

// sidecarPath returns where a subtitle should be written for a media file + language, e.g.
// "/lib/Movie (2020)/Movie (2020).en.srt" — the Plex/Jellyfin convention.
func sidecarPath(mediaPath, lang string) string {
	return sidecarPathV(mediaPath, lang, VariantFull)
}

// sidecarPathV is sidecarPath for a variant: "<base>.en.forced.srt" or "<base>.en.sdh.srt",
// the qualifiers Plex reads to flag the track.
func sidecarPathV(mediaPath, lang, variant string) string {
	dir := filepath.Dir(mediaPath)
	base := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	name := base + "." + lang
	if variant != VariantFull {
		name += "." + variant
	}
	return filepath.Join(dir, name+".srt")
}

// sidecarQualifiers are the trailing name segments that describe a track rather than name
// its language, mapped to the variant they mean ("default" says nothing about it).
var sidecarQualifiers = map[string]string{
	"forced": VariantForced, "sdh": VariantSDH, "hi": VariantSDH, "cc": VariantSDH, "default": VariantFull,
}

// isKnownLang reports whether a name segment is a language tag we recognise.
func isKnownLang(tok string) bool { return knownLangs[strings.ToLower(tok)] }

// parseSidecarTag reads the language and variant from the dotted segments after a sidecar's
// base name: ["en"] → en, ["en","forced"] → en forced, ["en","hi"] → en SDH (Bazarr writes
// hearing-impaired that way), ["hi"] → Hindi. Trailing qualifiers are peeled off first; "hi"
// only counts as one when a language sits before it, since on its own it is Hindi. The
// segment left at the end is the language when isLang accepts it, else lang is "".
//
// This is the shared sidecar-naming rule. Convert's sidecarLangs (internal/convert/preset.go)
// applies the same rule; keep the two in step until both live in one place.
func parseSidecarTag(segs []string, isLang func(string) bool) (lang, variant string) {
	i := len(segs) - 1
	for ; i >= 0; i-- {
		seg := strings.ToLower(segs[i])
		v, ok := sidecarQualifiers[seg]
		if !ok {
			break
		}
		if seg == "hi" && (i == 0 || !isLang(segs[i-1])) {
			break // a bare "hi" is the language, Hindi
		}
		// Forced wins over SDH: a forced track is never the full subtitle, whatever else it is.
		switch {
		case v == VariantForced:
			variant = VariantForced
		case v == VariantSDH && variant == VariantFull:
			variant = VariantSDH
		}
	}
	if i >= 0 && isLang(segs[i]) {
		lang = normLang(strings.ToLower(segs[i]))
	}
	return lang, variant
}

// coversFull reports whether a sidecar of this variant counts as the language's full subtitle.
func coversFull(variant string) bool { return variant != VariantForced }

// Orphan is a subtitle file in a movie's folder that pairs with no video there (its name
// isn't "<video base>[.anything]"). Plex shows it for nothing, so it isn't coverage — but
// it is probably what the owner meant to keep, so the sweep leaves its language alone.
type Orphan struct {
	Name    string `json:"name"`
	Lang    string `json:"lang,omitempty"`    // "" = no recognisable language tag
	Variant string `json:"variant,omitempty"` // "" (full) | forced | sdh
}

// sidecarScan is what one directory listing says about a video's subtitles.
type sidecarScan struct {
	Present  []string      // wanted languages with a full (or SDH) sidecar paired with the video
	Variants []LangVariant // every paired sidecar's language and variant
	Orphans  []Orphan      // movies only: subtitles in the folder that pair with no video
}

// scanSidecars reads a video's subtitle picture in a single ReadDir. A sidecar belongs to the
// video only when it is named for it ("<base>.srt", "<base>.en.srt", "<base>.en.forced.srt"),
// which is how Plex pairs them — for movies and TV alike. Present counts only full and SDH
// sidecars: a forced one is a few lines, not coverage. An untagged paired sidecar is credited
// to the first wanted language.
//
// For movies (kind "movie"), subtitles that pair with no video in the folder at all are
// reported as Orphans; in a multi-version folder one version's sidecars pair with that
// version, so they are neither the other's coverage nor orphans.
//
// Language matching tolerates 2- vs 3-letter codes and full names (en ≡ eng ≡ english).
func scanSidecars(videoPath string, wanted []string, kind string) sidecarScan {
	var sc sidecarScan
	if videoPath == "" {
		return sc
	}
	dir := filepath.Dir(videoPath)
	baseLower := strings.ToLower(strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath)))
	prefix := baseLower + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return sc
	}
	var videoBases, longer, subNames, unpaired []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		stem := strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		switch {
		case pairingVideoExts[ext]:
			videoBases = append(videoBases, stem)
			if strings.HasPrefix(stem, prefix) {
				longer = append(longer, stem) // "Movie.Proper.mkv" beside "Movie.mkv"
			}
		case subExts[ext]:
			subNames = append(subNames, e.Name())
		}
	}
	for _, name := range subNames {
		stem := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		var lv LangVariant
		switch {
		case stem == baseLower: // bare "<base>.srt" for exactly this file: untagged, full
		case strings.HasPrefix(stem, prefix) && !pairsWith(stem, longer): // "<base>.<lang>[.forced].srt"
			lv.Lang, lv.Variant = parseSidecarTag(strings.Split(stem[len(prefix):], "."), isKnownLang)
		default:
			unpaired = append(unpaired, name)
			continue
		}
		sc.Variants = append(sc.Variants, lv)
	}
	sc.Present = coveredLangs(wanted, sc.Variants)
	if kind != "movie" {
		return sc
	}
	for _, name := range unpaired {
		stem := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		if pairsWith(stem, videoBases) {
			continue // another video's (another version's) sidecar
		}
		o := Orphan{Name: name}
		o.Lang, o.Variant = parseSidecarTag(strings.Split(stem, "."), isKnownLang)
		sc.Orphans = append(sc.Orphans, o)
	}
	return sc
}

// pairsWith reports whether a (lower-case) subtitle stem is named for one of the video bases.
func pairsWith(stem string, bases []string) bool {
	for _, b := range bases {
		if stem == b || strings.HasPrefix(stem, b+".") {
			return true
		}
	}
	return false
}

// pairingVideoExts are the containers a sidecar can pair with when deciding what's an orphan.
var pairingVideoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".ts": true, ".m2ts": true,
	".mov": true, ".wmv": true, ".webm": true,
}

// coveredLangs returns the wanted languages that the given sidecars cover in full: a
// tagged full or SDH sidecar in that language, and an untagged one for the first wanted.
func coveredLangs(wanted []string, lvs []LangVariant) []string {
	tags := map[string]bool{}
	untagged := false
	for _, lv := range lvs {
		if !coversFull(lv.Variant) {
			continue
		}
		if lv.Lang != "" {
			tags[lv.Lang] = true
		} else {
			untagged = true
		}
	}
	var out []string
	for i, w := range wanted {
		matched := i == 0 && untagged
		for tok := range tags {
			if langMatches(tok, w) {
				matched = true
				break
			}
		}
		if matched {
			out = append(out, w)
		}
	}
	return out
}

// orphanCovered returns which wanted languages (lower-case) are missing as paired sidecars
// but covered by an orphan — the owner's subtitle under an old name, most likely.
func orphanCovered(wanted, present []string, orphans []Orphan) map[string]bool {
	if len(orphans) == 0 {
		return nil
	}
	lvs := make([]LangVariant, 0, len(orphans))
	for _, o := range orphans {
		lvs = append(lvs, LangVariant{Lang: o.Lang, Variant: o.Variant})
	}
	have := map[string]bool{}
	for _, p := range present {
		have[strings.ToLower(p)] = true
	}
	out := map[string]bool{}
	for _, l := range coveredLangs(wanted, lvs) {
		if !have[strings.ToLower(l)] {
			out[strings.ToLower(l)] = true
		}
	}
	return out
}

// presentVariants lists the language and variant of every sidecar named for this file
// ("<base>[.lang][.qualifier].ext").
func presentVariants(mediaPath string) []LangVariant {
	return scanSidecars(mediaPath, nil, "episode").Variants
}

// kindOf maps a job or file kind to scanSidecars' kind.
func kindOf(kind string) string {
	if kind == "episode" {
		return "episode"
	}
	return "movie"
}
