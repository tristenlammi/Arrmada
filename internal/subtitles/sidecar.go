// Package subtitles is the Subtitles module (Bazarr's domain): it grabs external
// subtitle files (SRT etc.) and saves them alongside the movie/episode files, using the
// shared Movies and Series catalogs. Subtitle presence is derived from disk (sidecar
// files), so there's no separate "wanted" table to keep in sync.
package subtitles

import (
	"os"
	"path/filepath"
	"strings"
)

// subExts are the external subtitle extensions we recognize as already-present.
var subExts = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".vtt": true}

// sidecarPath returns where a grabbed subtitle should be written for a media file +
// language, e.g. "/lib/Movie (2020)/Movie (2020).en.srt" — the Plex/Jellyfin convention.
func sidecarPath(mediaPath, lang string) string {
	dir := filepath.Dir(mediaPath)
	base := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	return filepath.Join(dir, base+"."+lang+".srt")
}

// presentLanguages returns which of the wanted languages already have a subtitle sidecar
// next to the media file. Matching is by the "<base>.<lang>.<ext>" convention; a bare
// "<base>.<ext>" with no language tag counts toward the first wanted language.
func presentLanguages(mediaPath string, wanted []string) []string {
	if mediaPath == "" || len(wanted) == 0 {
		return nil
	}
	dir := filepath.Dir(mediaPath)
	base := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	tags := map[string]bool{}
	bare := false
	prefix := base + "."
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !subExts[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		stem := strings.TrimSuffix(name, filepath.Ext(name)) // drop the subtitle ext
		if stem == base {
			bare = true
			continue
		}
		if strings.HasPrefix(stem, prefix) {
			// The remainder can be "en", "en.forced", "english.sdh" — take the first segment.
			tag := strings.ToLower(strings.SplitN(stem[len(prefix):], ".", 2)[0])
			tags[tag] = true
		}
	}
	var present []string
	for i, w := range wanted {
		if tags[strings.ToLower(w)] || (i == 0 && bare) {
			present = append(present, w)
		}
	}
	return present
}
