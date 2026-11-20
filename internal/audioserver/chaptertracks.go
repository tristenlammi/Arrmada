package audioserver

import (
	"fmt"
	"math"
	"strings"
)

// A very long audiobook in one file is told to listening apps as one track per chapter.
//
// Lissen plays a book chapter by chapter, opening the file afresh for each one, and an
// Android player keeps a file's whole sample index in memory while it's open: about 24
// bytes a frame, so 190 MB for a 51-hour book at 44.1 kHz. At a chapter change two copies
// are held at once, the app runs out of memory and the person sees "source error" at
// every chapter. A track that holds only its own chapter has a small index, whatever the
// book's length. The file on disk is never changed: each track is cut from it, without
// re-encoding, the first time an app asks (clips.go). Arrmada's own player plays the file
// itself, which a browser opens once.

// splitMinSeconds is the length from which a single file is served chapter by chapter.
// Lissen asks Android for a large heap (512 MB on most phones); two open copies of a
// 32-hour file's index at 44.1 kHz take about half of that. Shorter books are left as the
// one file they are, which is also what apps that already downloaded them hold.
const splitMinSeconds = 32 * 3600

// splittable reports whether a file's container is one the cut can write back out
// (the MP4 family, where the per-frame index is the problem in the first place).
func splittable(ext string) bool {
	switch strings.ToLower(ext) {
	case ".m4b", ".m4a", ".mp4":
		return true
	}
	return false
}

// chapterTracks is an item's audio files as listening apps are told about them: the files
// themselves, or for one very long file with chapter marks, one track per chapter (split
// true). The tracks tile the whole file from 0:00 to its end, each starting where a
// chapter starts, so a moment in the book is the same moment either way and a saved place
// carries over.
func chapterTracks(files []AudioFile) (tracks []AudioFile, split bool) {
	if len(files) != 1 {
		return files, false
	}
	f := files[0]
	if f.Duration < splitMinSeconds || len(f.Chapters) < 2 || !splittable(f.Ext) {
		return files, false
	}
	// Marks that aren't in order, or run past the end, describe some other file: leave it whole.
	for i, c := range f.Chapters {
		if math.IsNaN(c.Start) || c.Start < 0 || c.Start >= f.Duration || (i > 0 && c.Start <= f.Chapters[i-1].Start) {
			return files, false
		}
	}
	used := map[string]bool{f.Ino: true}
	out := make([]AudioFile, 0, len(f.Chapters))
	for i, c := range f.Chapters {
		start, end := c.Start, f.Duration
		if i == 0 {
			start = 0 // anything before the first mark belongs to the first track
		}
		if i+1 < len(f.Chapters) {
			end = f.Chapters[i+1].Start
		}
		// The id names the span, so a file replaced by one with other chapters gives new
		// ids and an app doesn't keep playing its old download of a different stretch.
		id := fmt.Sprintf("%s#%d@%d-%d", f.Ino, i, int64(math.Round(start*1000)), int64(math.Round(end*1000)))
		ino := inoFor(id)
		for n := 2; used[ino]; n++ {
			ino = inoFor(fmt.Sprintf("%s#%d", id, n))
		}
		used[ino] = true
		title := strings.TrimSpace(c.Title)
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		out = append(out, AudioFile{
			Index: i + 1, Ino: ino, Path: f.Path, Name: fmt.Sprintf("%03d - %s.m4a", i+1, safeName(title)), Ext: ".m4a",
			// The size is a share of the file's by length: the cut isn't made until asked for.
			Size: int64(float64(f.Size) * (end - start) / f.Duration), MTimeMs: f.MTimeMs, Duration: end - start,
			Bitrate: f.Bitrate, Codec: f.Codec, Title: title, Narrator: f.Narrator,
			Clip: &clipSpan{Start: start, End: end, SrcSize: f.Size, Last: i+1 == len(f.Chapters)},
		})
	}
	return out, true
}

// clipSpan marks an AudioFile that is a stretch of the file at Path rather than the file.
type clipSpan struct {
	Start, End float64 // seconds within the file
	SrcSize    int64   // the file's size, which with its time names the cut
	Last       bool    // runs to the end of the file
}

// trackByIno finds a track by id among an item's files as apps see them: a real file, or a
// chapter track of a long one.
func trackByIno(files []AudioFile, ino string) (AudioFile, bool) {
	for _, f := range files {
		if f.Ino == ino {
			return f, true
		}
	}
	if tracks, split := chapterTracks(files); split {
		for _, t := range tracks {
			if t.Ino == ino {
				return t, true
			}
		}
	}
	return AudioFile{}, false
}

// nextTrack is the chapter track after t in the same file, if there is one.
func nextTrack(files []AudioFile, t AudioFile) (AudioFile, bool) {
	tracks, split := chapterTracks(files)
	if !split || t.Clip == nil || t.Index >= len(tracks) {
		return AudioFile{}, false
	}
	return tracks[t.Index], true // Index is 1-based: this is the one after
}
