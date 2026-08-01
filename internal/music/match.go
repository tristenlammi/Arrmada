package music

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// AudioFile is one audio file found in a download, ready to be matched to a track.
type AudioFile struct {
	Path      string
	SizeBytes int64
}

// reLeadingNum matches the track number a release puts at the front of a filename:
// "01 - Airbag", "01. Airbag", "01_Airbag", "1-04 Airbag" (disc-track).
var reLeadingNum = regexp.MustCompile(`^\s*(?:(\d{1,2})\s*[-._]\s*)?(\d{1,3})\s*[-._)\s]`)

// TrackNumberOf reads the disc and track number off a filename. Returns (0, 0) when the
// name carries no number — the caller must then fall back to matching by title, because
// guessing a position from directory order silently mislabels an album.
func TrackNumberOf(path string) (disc, track int) {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	m := reLeadingNum.FindStringSubmatch(base)
	if m == nil {
		return 0, 0
	}
	if m[1] != "" {
		disc, _ = strconv.Atoi(m[1])
	}
	track, _ = strconv.Atoi(m[2])
	if disc == 0 {
		disc = 1
	}
	// A "number" above 200 is a year or a bitrate that leaked into the name, not a track.
	if track <= 0 || track > 200 {
		return 0, 0
	}
	return disc, track
}

// titleOf strips a leading track number and the extension, leaving the song title.
func titleOf(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = reLeadingNum.ReplaceAllString(base, "")
	return strings.Trim(base, " -_.")
}

// MatchTracks maps a download's audio files onto an album's tracks.
//
// Numbering first, title second, and nothing else. Position-in-directory is deliberately NOT
// used as a fallback: a folder that happens to hold a bonus track, an intro file or a
// different edition would shift every song after it, and an album silently labelled one
// track out is far worse than one left unmatched for a human to look at.
//
// Files that match nothing are returned so the caller can report them rather than drop them
// in silence.
func MatchTracks(tracks []Track, files []AudioFile) (matched map[int64]AudioFile, unmatched []AudioFile) {
	matched = make(map[int64]AudioFile, len(tracks))
	claimed := make(map[string]bool, len(files))

	byNumber := make(map[[2]int]Track, len(tracks))
	byTitle := make(map[string][]Track, len(tracks))
	for _, t := range tracks {
		byNumber[[2]int{t.DiscNumber, t.TrackNumber}] = t
		k := NormKey(t.Title)
		if k != "" {
			byTitle[k] = append(byTitle[k], t)
		}
	}

	// Pass 1: explicit disc/track numbers.
	for _, f := range files {
		d, n := TrackNumberOf(f.Path)
		if n == 0 {
			continue
		}
		t, ok := byNumber[[2]int{d, n}]
		if !ok && d == 1 {
			// Single-disc releases often omit the disc entirely; a lone "07" on a two-disc
			// album is ambiguous, so only try this when there's exactly one candidate.
			if cands := tracksWithNumber(tracks, n); len(cands) == 1 {
				t, ok = cands[0], true
			}
		}
		if ok {
			if _, taken := matched[t.ID]; !taken {
				matched[t.ID] = f
				claimed[f.Path] = true
			}
		}
	}

	// Pass 2: titles, for files with no usable number.
	for _, f := range files {
		if claimed[f.Path] {
			continue
		}
		cands := byTitle[NormKey(titleOf(f.Path))]
		if len(cands) != 1 {
			continue // no match, or ambiguous — leave it for a human
		}
		if _, taken := matched[cands[0].ID]; taken {
			continue
		}
		matched[cands[0].ID] = f
		claimed[f.Path] = true
	}

	for _, f := range files {
		if !claimed[f.Path] {
			unmatched = append(unmatched, f)
		}
	}
	return matched, unmatched
}

func tracksWithNumber(tracks []Track, n int) []Track {
	var out []Track
	for _, t := range tracks {
		if t.TrackNumber == n {
			out = append(out, t)
		}
	}
	return out
}

// ReleaseIsForAlbum reports whether a release name names this artist and album as whole
// words — the gate that stops a search for one album grabbing a different one by the same
// artist, which is exactly the hole the Books module had.
//
// A discography release is refused outright: grabbing one against a single album would fill
// that album's folder with the artist's entire catalogue.
func ReleaseIsForAlbum(releaseName, artist, album string) bool {
	r := ParseRelease(releaseName)
	if r.Discography {
		return false
	}
	hay := " " + normWords(releaseName) + " "
	if a := normWords(artist); a != "" && !strings.Contains(hay, " "+a+" ") {
		return false
	}
	al := normWords(album)
	return al != "" && strings.Contains(hay, " "+al+" ")
}

// normWords lowercases and reduces every run of non-alphanumerics to one space, preserving
// word boundaries so "Kid A" can't match inside "Kid Amnesiae".
func normWords(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			space = true
		}
	}
	return b.String()
}

// ReleaseIsDiscographyFor reports whether a release is a whole-catalogue pack for this
// artist. Kept separate from ReleaseIsForAlbum, which refuses discographies outright:
// they're only ever wanted through the explicit "grab discography" action, where a
// multi-album importer maps each folder to its own album.
func ReleaseIsDiscographyFor(releaseName, artist string) bool {
	if !ParseRelease(releaseName).Discography {
		return false
	}
	a := normWords(artist)
	if a == "" {
		return false
	}
	return strings.Contains(" "+normWords(releaseName)+" ", " "+a+" ")
}
