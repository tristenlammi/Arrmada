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
	entries, err := os.ReadDir(filepath.Dir(video))
	if err != nil {
		return nil
	}
	return PairedSidecarsIn(video, entries)
}

// PairedSidecarsIn is PairedSidecars over a listing of the video's folder the caller
// already read, so a page showing several files in one folder lists it once.
func PairedSidecarsIn(video string, entries []os.DirEntry) []string {
	dir := filepath.Dir(video)
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(video), filepath.Ext(video)))
	// Another video whose name extends this one's ("Movie.Proper.mkv" beside "Movie.mkv")
	// owns the sidecars named for it, even though they also start with "Movie.".
	var longer []string
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if b := strings.ToLower(strings.TrimSuffix(e.Name(), ext)); !e.IsDir() && sameBaseVideoExts[strings.ToLower(ext)] &&
			strings.HasPrefix(b, base+".") {
			longer = append(longer, b)
		}
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
		if pairsWithAny(stem, longer) {
			continue // the longer-named video's sidecar
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

// pairsWithAny reports whether a subtitle stem pairs with any of the (lower-case) video bases.
func pairsWithAny(stem string, bases []string) bool {
	for _, b := range bases {
		if stem == b || strings.HasPrefix(stem, b+".") {
			return true
		}
	}
	return false
}

// OrphanSidecars lists the subtitle files in dir that pair with no video there — left
// behind by a rename or an upgrade, or named for a different cut. Plex shows them for
// nothing, so they are neither a video's coverage nor safe to treat as one.
func OrphanSidecars(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	return OrphanSidecarsIn(dir, entries)
}

// OrphanSidecarsIn is OrphanSidecars over a listing of dir the caller already read.
func OrphanSidecarsIn(dir string, entries []os.DirEntry) []string {
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
		if !pairsWithAny(strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name))), bases) {
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
