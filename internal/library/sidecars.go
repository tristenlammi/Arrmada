package library

import (
	"os"
	"path/filepath"
	"strings"
)

// sameBaseVideoExts are the containers a sibling "same name, different extension" video
// can be in. Wider than videoExts on purpose (m2ts): this only decides whether to leave
// subtitles alone, never what gets imported.
var sameBaseVideoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".ts": true, ".m2ts": true,
	".mov": true, ".wmv": true, ".webm": true, ".mpg": true, ".mpeg": true, ".flv": true,
}

// PairedSidecars lists the subtitle files paired with video: same folder, and named either
// exactly like the video or the video's name plus a dotted suffix (".en", ".en.forced").
// That is the pairing rule Plex uses, so these are the files that belong to this video
// and should share whatever happens to it (a rename, a delete to the recycle bin).
// Matching ignores case. 'Movie 2.srt' next to 'Movie.mkv' is a neighbour, not a sidecar.
func PairedSidecars(video string) []string {
	dir := filepath.Dir(video)
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(video), filepath.Ext(video)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !subtitleExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		stem := strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		if stem != base && !strings.HasPrefix(stem, base+".") {
			continue // not this video's sidecar
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

// OrphanSidecars lists the subtitle files in dir that pair with no video there — left
// behind by a rename or an upgrade, or named for a different cut. Plex shows them for
// nothing, so they are neither a video's coverage nor safe to treat as one.
func OrphanSidecars(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var bases, subs []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		stem := strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		switch {
		case sameBaseVideoExts[ext]:
			bases = append(bases, stem)
		case subtitleExts[ext]:
			subs = append(subs, e.Name())
		}
	}
	var out []string
	for _, name := range subs {
		stem := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		paired := false
		for _, b := range bases {
			if stem == b || strings.HasPrefix(stem, b+".") {
				paired = true
				break
			}
		}
		if !paired {
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out
}

// SharesBase reports whether another video in the same folder has this video's base name —
// a container swap such as X.mp4 replaced by X.mkv. Its sidecars then belong to the
// surviving video too, so deleting this one must leave them where they are.
func SharesBase(video string) bool {
	dir := filepath.Dir(video)
	name := filepath.Base(video)
	base := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || strings.EqualFold(e.Name(), name) {
			continue
		}
		ext := filepath.Ext(e.Name())
		if !sameBaseVideoExts[strings.ToLower(ext)] {
			continue
		}
		if strings.ToLower(strings.TrimSuffix(e.Name(), ext)) == base {
			return true
		}
	}
	return false
}
