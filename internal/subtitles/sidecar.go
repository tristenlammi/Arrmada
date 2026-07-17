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

// sidecarPath returns where a subtitle should be written for a media file + language, e.g.
// "/lib/Movie (2020)/Movie (2020).en.srt" — the Plex/Jellyfin convention.
func sidecarPath(mediaPath, lang string) string {
	dir := filepath.Dir(mediaPath)
	base := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	return filepath.Join(dir, base+"."+lang+".srt")
}

// langTokenFromSegments returns the language token sitting at the end of a dotted name (e.g. "en"
// from "en.forced", "eng" from a bare "eng"), or "" if the trailing segments aren't a language.
// Only the last two segments are considered — that's where language tags live.
func langTokenFromSegments(segs []string) string {
	for i := len(segs) - 1; i >= 0 && i >= len(segs)-2; i-- {
		if s := normLang(strings.ToLower(segs[i])); knownLangs[s] {
			return s
		}
	}
	return ""
}

// presentLanguages returns which of the wanted languages already have a subtitle sidecar next to
// the media file.
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
	dir := filepath.Dir(mediaPath)
	baseLower := strings.ToLower(strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath)))
	prefix := baseLower + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	tags := map[string]bool{} // recognised language tokens found on relevant sidecars
	untagged := false         // a relevant sidecar with no recognisable language tag
	for _, e := range entries {
		if e.IsDir() || !subExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		stem := strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		var tok string
		switch {
		case stem == baseLower: // bare "<base>.srt" for exactly this file
			// tok stays "" → untagged
		case strings.HasPrefix(stem, prefix): // "<base>.<lang>[.forced].srt"
			tok = langTokenFromSegments(strings.Split(stem[len(prefix):], "."))
		case singleFolder: // movie folder — any sidecar belongs to this film
			tok = langTokenFromSegments(strings.Split(stem, "."))
		default:
			continue // TV: unrelated file in the season folder
		}
		if tok != "" {
			tags[tok] = true
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
