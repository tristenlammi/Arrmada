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

// presentLanguages returns which of the wanted languages already have a subtitle sidecar next to
// the media file. Only full and SDH sidecars count: a forced one is a few lines, not coverage.
//
//   - singleFolder=true (movies live one-per-folder): ANY subtitle file in the folder counts. Its
//     language is read from a "<name>.<lang>.srt" tag when present, otherwise it's credited to the
//     first wanted language. This tolerates a sidecar left under a different/older name than the
//     (Arrmada-renamed) video.
//   - singleFolder=false (TV episodes share a season folder): the sidecar must be named for this
//     episode ("<base>[.lang].srt") so a season's subtitles aren't cross-counted.
//
// Language matching tolerates 2- vs 3-letter codes and full names (en ≡ eng ≡ english).
func presentLanguages(mediaPath string, wanted []string, singleFolder bool) []string {
	if mediaPath == "" || len(wanted) == 0 {
		return nil
	}
	tags := map[string]bool{} // recognised language tokens found on relevant full sidecars
	untagged := false         // a relevant full sidecar with no recognisable language tag
	for _, lv := range sidecarTags(mediaPath, singleFolder) {
		if !coversFull(lv.Variant) {
			continue
		}
		if lv.Lang != "" {
			tags[lv.Lang] = true
		} else {
			untagged = true
		}
	}
	var present []string
	for i, w := range wanted {
		matched := i == 0 && untagged
		for tok := range tags {
			if langMatches(tok, w) {
				matched = true
				break
			}
		}
		if matched {
			present = append(present, w)
		}
	}
	return present
}

// presentVariants lists the language and variant of every sidecar named for this file
// ("<base>[.lang][.qualifier].ext").
func presentVariants(mediaPath string) []LangVariant {
	return sidecarTags(mediaPath, false)
}

// sidecarTags reads the language/variant of the subtitle files that belong to a media file:
// those named for it, plus (singleFolder) any other subtitle in a movie's folder.
func sidecarTags(mediaPath string, singleFolder bool) []LangVariant {
	if mediaPath == "" {
		return nil
	}
	dir := filepath.Dir(mediaPath)
	baseLower := strings.ToLower(strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath)))
	prefix := baseLower + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []LangVariant
	for _, e := range entries {
		if e.IsDir() || !subExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		stem := strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		var lv LangVariant
		switch {
		case stem == baseLower: // bare "<base>.srt" for exactly this file: untagged, full
		case strings.HasPrefix(stem, prefix): // "<base>.<lang>[.forced].srt"
			lv.Lang, lv.Variant = parseSidecarTag(strings.Split(stem[len(prefix):], "."), isKnownLang)
		case singleFolder: // movie folder — any sidecar belongs to this film
			lv.Lang, lv.Variant = parseSidecarTag(strings.Split(stem, "."), isKnownLang)
		default:
			continue // TV: unrelated file in the season folder
		}
		out = append(out, lv)
	}
	return out
}
