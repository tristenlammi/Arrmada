package audioserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/metadata"
)

// longBook is a single file of the given length with n evenly spaced chapter marks.
func longBook(seconds float64, n int) AudioFile {
	f := AudioFile{Index: 1, Ino: "123456", Path: "/books/Long/Long.m4b", Name: "Long.m4b", Ext: ".m4b",
		Size: 5_000_000_000, MTimeMs: 42, Duration: seconds, Bitrate: 128000, Codec: "aac", Title: "Long", Narrator: "Someone"}
	for i := 0; i < n; i++ {
		f.Chapters = append(f.Chapters, Chapter{ID: i, Start: seconds * float64(i) / float64(n),
			End: seconds * float64(i+1) / float64(n), Title: fmt.Sprintf("Part %d", i+1)})
	}
	return f
}

func TestChapterTracksOnlyForVeryLongSingleFiles(t *testing.T) {
	long := longBook(40*3600, 100)
	cases := map[string][]AudioFile{
		"a normal-length book":        {longBook(20*3600, 60)},
		"no chapter marks":            {longBook(40*3600, 0)},
		"one chapter mark":            {longBook(40*3600, 1)},
		"two files":                   {long, long},
		"an mp3 (no per-frame index)": {func() AudioFile { f := long; f.Ext = ".mp3"; return f }()},
		"marks out of order": {func() AudioFile {
			f := longBook(40*3600, 5)
			f.Chapters[2].Start = f.Chapters[1].Start
			return f
		}()},
		"a mark past the end": {func() AudioFile {
			f := longBook(40*3600, 5)
			f.Chapters[4].Start = f.Duration + 1
			return f
		}()},
	}
	for name, files := range cases {
		got, split := chapterTracks(files)
		if split || len(got) != len(files) {
			t.Errorf("%s: split=%v into %d tracks, want the %d file(s) as they are", name, split, len(got), len(files))
		}
	}
	if got, split := chapterTracks([]AudioFile{long}); !split || len(got) != 100 {
		t.Fatalf("a 40-hour file with 100 chapters: split=%v into %d, want 100 tracks", split, len(got))
	}
}

func TestChapterTracksTileTheWholeFile(t *testing.T) {
	f := longBook(51*3600+17.3, 168)
	// A first mark after 0:00 and a gap between marks must not lose any audio.
	f.Chapters[0].Start = 12
	f.Chapters[10].End -= 30
	tracks, split := chapterTracks([]AudioFile{f})
	if !split {
		t.Fatal("not split")
	}
	at, total, size := 0.0, 0.0, int64(0)
	seen := map[string]bool{f.Ino: true}
	for i, tr := range tracks {
		if tr.Clip == nil || tr.Path != f.Path {
			t.Fatalf("track %d isn't a cut of the file: %+v", i, tr)
		}
		if math.Abs(tr.Clip.Start-at) > 1e-9 {
			t.Fatalf("track %d starts at %v, the one before ended at %v", i, tr.Clip.Start, at)
		}
		if i > 0 && math.Abs(tr.Clip.Start-f.Chapters[i].Start) > 1e-9 {
			t.Fatalf("track %d starts at %v, its chapter at %v", i, tr.Clip.Start, f.Chapters[i].Start)
		}
		if tr.Duration <= 0 || math.Abs(tr.Duration-(tr.Clip.End-tr.Clip.Start)) > 1e-9 {
			t.Fatalf("track %d length %v for %v–%v", i, tr.Duration, tr.Clip.Start, tr.Clip.End)
		}
		if tr.Index != i+1 || tr.Ext != ".m4a" || tr.Title != f.Chapters[i].Title || tr.Narrator != f.Narrator {
			t.Fatalf("track %d described wrongly: %+v", i, tr)
		}
		if seen[tr.Ino] {
			t.Fatalf("track %d's id %s is already used", i, tr.Ino)
		}
		seen[tr.Ino] = true
		at, total, size = tr.Clip.End, total+tr.Duration, size+tr.Size
	}
	if math.Abs(at-f.Duration) > 1e-9 || math.Abs(total-f.Duration) > 1e-6 {
		t.Fatalf("tracks end at %v and add up to %v, the file is %v long", at, total, f.Duration)
	}
	if !tracks[len(tracks)-1].Clip.Last || tracks[0].Clip.Last {
		t.Fatal("only the last track runs to the end of the file")
	}
	if d := f.Size - size; d < 0 || d > int64(len(tracks)) {
		t.Fatalf("track sizes add up to %d of the file's %d", size, f.Size)
	}

	// The same file gives the same ids; other chapter marks give other ids.
	again, _ := chapterTracks([]AudioFile{f})
	moved := f
	moved.Chapters = append([]Chapter(nil), f.Chapters...)
	moved.Chapters[5].Start += 3
	other, _ := chapterTracks([]AudioFile{moved})
	for i := range tracks {
		if again[i].Ino != tracks[i].Ino {
			t.Fatalf("track %d's id changed between two listings", i)
		}
	}
	if other[4].Ino == tracks[4].Ino || other[5].Ino == tracks[5].Ino {
		t.Fatal("a track that covers a different stretch kept its id")
	}
	if other[20].Ino != tracks[20].Ino {
		t.Fatal("a track whose stretch didn't change got a new id")
	}

	if tr, ok := trackByIno([]AudioFile{f}, tracks[7].Ino); !ok || tr.Index != 8 {
		t.Fatalf("track 8 by id: %+v %v", tr, ok)
	}
	if tr, ok := trackByIno([]AudioFile{f}, f.Ino); !ok || tr.Clip != nil {
		t.Fatal("the file itself must still be found by its own id (Arrmada's player plays it)")
	}
	if next, ok := nextTrack([]AudioFile{f}, tracks[7]); !ok || next.Index != 9 {
		t.Fatalf("after track 8 comes %+v %v", next, ok)
	}
	if _, ok := nextTrack([]AudioFile{f}, tracks[len(tracks)-1]); ok {
		t.Fatal("nothing comes after the last track")
	}
}

// longBookHarness is a harness whose library also holds a 40-hour single-file audiobook
// with 4 chapters, already probed. It returns the item key and the file's path. Cuts are
// faked: a track's bytes say which stretch it is.
func longBookHarness(t *testing.T) (h *harness, key, path string, cuts *atomic.Int32) {
	t.Helper()
	h = newHarness(t)
	ctx := context.Background()
	added, _ := h.srv.books.AddWorks(ctx, []metadata.BookResult{{Key: "hc:2", Title: "A Very Long Book", Author: "Some Author"}}, "", true)
	if len(added) != 1 {
		t.Fatal("book not added")
	}
	dir := filepath.Join(filepath.Dir(filepath.Dir(h.audioDir())), "Some Author", "A Very Long Book")
	_ = os.MkdirAll(dir, 0o755)
	path = filepath.Join(dir, "A Very Long Book.m4b")
	if err := os.WriteFile(path, bytes.Repeat([]byte("whole-file "), 4000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.srv.books.MarkImported(ctx, added[0].ID, books.KindAudiobook, path, "M4B", 44000, 1); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	chapters, _ := json.Marshal([]Chapter{{0, 0, 36000, "Prologue"}, {1, 36000, 72000, "One"}, {2, 72000, 108000, "Two"}, {3, 108000, 144000, "Epilogue"}})
	if _, err := h.srv.db.Exec(`INSERT INTO audio_file_meta (path, size, mtime, duration, bitrate, codec, title, narrator, chapters, meta_version)
		VALUES (?, ?, ?, 144000, 128000, 'aac', 'A Very Long Book', '', ?, ?)`, path, fi.Size(), fi.ModTime().UnixMilli(), string(chapters), metaVersion); err != nil {
		t.Fatal(err)
	}
	h.srv.Invalidate()
	cuts = &atomic.Int32{}
	h.srv.clips.cut = func(_ context.Context, src string, start, dur float64, title, dst string) error {
		cuts.Add(1)
		if src != path {
			return fmt.Errorf("cut from %s", src)
		}
		return os.WriteFile(dst, []byte(fmt.Sprintf("cut %.0f+%.0f %s|", start, dur, title)+strings.Repeat("x", 500)), 0o644)
	}
	h.signIn()
	return h, itemKeyFor(added[0].ID, 0), path, cuts
}

func TestVeryLongBookIsServedAsChapterTracks(t *testing.T) {
	h, key, path, cuts := longBookHarness(t)

	item := h.json("GET", "/api/items/"+key+"?expanded=1", nil)
	media := obj1(t, item["media"])
	tracks, audio, chapters := list1(t, media["tracks"]), list1(t, media["audioFiles"]), list1(t, media["chapters"])
	if len(tracks) != 4 || len(audio) != 4 || len(chapters) != 4 {
		t.Fatalf("%d tracks, %d audio files, %d chapters; want 4 of each", len(tracks), len(audio), len(chapters))
	}
	if media["duration"].(float64) != 144000 || media["numTracks"].(float64) != 4 {
		t.Fatalf("duration %v, numTracks %v", media["duration"], media["numTracks"])
	}
	fi, _ := os.Stat(path)
	if media["size"].(float64) != float64(fi.Size()) {
		t.Fatalf("the book's size is %v, the file is %d", media["size"], fi.Size())
	}
	var urls []string
	for i, v := range tracks {
		tr := obj1(t, v)
		ch := obj1(t, chapters[i])
		// Each track starts where its chapter does: a place in the book is the same place.
		if tr["startOffset"].(float64) != ch["start"].(float64) || tr["duration"].(float64) != 36000 {
			t.Fatalf("track %d: offset %v length %v, chapter starts %v", i, tr["startOffset"], tr["duration"], ch["start"])
		}
		if tr["mimeType"] != "audio/mp4" || tr["title"] != ch["title"] {
			t.Fatalf("track %d: %v %v", i, tr["mimeType"], tr["title"])
		}
		urls = append(urls, tr["contentUrl"].(string))
	}

	// In lists the book says the same number of tracks, from the database alone.
	listed := h.json("GET", "/api/libraries/"+libraryID+"/items?limit=50&minified=1", nil)
	found := false
	for _, v := range list1(t, listed["results"]) {
		if o := obj1(t, v); o["id"] == key {
			found = true
			if n := obj1(t, o["media"])["numTracks"].(float64); n != 4 {
				t.Fatalf("listed with %v tracks, want 4", n)
			}
		}
	}
	if !found {
		t.Fatal("the long book isn't in the library list")
	}

	// A track is cut when first asked for, then served like a file (Range and all).
	code, body := h.do("GET", urls[1], nil, nil)
	if code != 200 || !strings.HasPrefix(string(body), "cut 36000+36000 One|") {
		t.Fatalf("track 2: HTTP %d %.40q", code, body)
	}
	code, part := h.do("GET", urls[1], nil, map[string]string{"Range": "bytes=4-8"})
	if code != 206 || string(part) != "36000" {
		t.Fatalf("a range of track 2: HTTP %d %q", code, part)
	}
	// The one after it is cut ahead of time, and nothing is cut twice.
	deadline := time.Now().Add(5 * time.Second)
	for cuts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	_, ahead := h.do("GET", urls[2], nil, nil)
	if !strings.HasPrefix(string(ahead), "cut 72000+36000 Two|") {
		t.Fatalf("track 3: %.40q", ahead)
	}
	// The last track runs to the end of the file rather than for a counted length.
	_, last := h.do("GET", urls[3], nil, nil)
	if !strings.HasPrefix(string(last), "cut 108000+0 Epilogue|") {
		t.Fatalf("the last track: %.40q", last)
	}
	if n := cuts.Load(); n != 3 {
		t.Fatalf("%d cuts for tracks 2, 3 and 4, want 3", n)
	}

	// The same tracks by number, for apps that play from the session's own links.
	sid := h.play(key)
	h.token = ""
	code, body = h.do("GET", "/public/session/"+sid+"/track/2", nil, nil)
	if code != 200 || !strings.HasPrefix(string(body), "cut 36000+36000 One|") {
		t.Fatalf("session track 2: HTTP %d %.40q", code, body)
	}
	if code, _ := h.do("GET", "/public/session/"+sid+"/track/5", nil, nil); code != 404 {
		t.Fatalf("a track past the last: HTTP %d, want 404", code)
	}
	h.signIn()

	// The file itself is untouched and still served whole: Arrmada's player and the
	// whole-book download use it.
	real, _ := h.srv.probe.files(context.Background(), path, false)
	code, whole := h.do("GET", "/api/items/"+key+"/file/"+real[0].Ino, nil, nil)
	if code != 200 || len(whole) != int(fi.Size()) {
		t.Fatalf("the file by its own id: HTTP %d, %d bytes of %d", code, len(whole), fi.Size())
	}
	code, whole = h.do("GET", "/api/items/"+key+"/download", nil, nil)
	if code != 200 || len(whole) != int(fi.Size()) {
		t.Fatalf("the whole-book download: HTTP %d, %d bytes of %d", code, len(whole), fi.Size())
	}
	if after, _ := os.Stat(path); after.Size() != fi.Size() || !after.ModTime().Equal(fi.ModTime()) {
		t.Fatal("the audiobook file was changed")
	}
	d, err := h.srv.Detail(context.Background(), h.readerID(), key)
	if err != nil || len(d.Tracks) != 1 || len(d.Chapters) != 4 {
		t.Fatalf("Arrmada's own player: %d tracks, %d chapters, %v; want the one file and its 4 chapters", len(d.Tracks), len(d.Chapters), err)
	}
}

func TestChapterTrackThatCannotBeCut(t *testing.T) {
	h, key, _, _ := longBookHarness(t)
	h.srv.clips.cut = func(context.Context, string, float64, float64, string, string) error {
		return errors.New("no ffmpeg here")
	}
	item := h.json("GET", "/api/items/"+key+"?expanded=1", nil)
	url := obj1(t, list1(t, obj1(t, item["media"])["tracks"])[0])["contentUrl"].(string)
	if code, _ := h.do("GET", url, nil, nil); code != 503 {
		t.Fatalf("HTTP %d, want 503", code)
	}
	left, _ := os.ReadDir(h.srv.clips.dir)
	if len(left) != 0 {
		t.Fatalf("%d files left behind by failed cuts", len(left))
	}
	// A failure isn't remembered: the next ask tries again.
	h.srv.clips.cut = func(_ context.Context, _ string, _, _ float64, _, dst string) error {
		return os.WriteFile(dst, []byte("ok"), 0o644)
	}
	if code, body := h.do("GET", url, nil, nil); code != 200 || string(body) != "ok" {
		t.Fatalf("after the fix: HTTP %d %q", code, body)
	}
}

func TestClipCacheCutsOnceAndSweeps(t *testing.T) {
	dir := t.TempDir()
	c := newClipCache(dir, "", nil)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	c.lastSweep = now // no sweep of its own after a cut: the test runs them by hand
	var calls atomic.Int32
	release := make(chan struct{})
	c.cut = func(_ context.Context, _ string, _, _ float64, _, dst string) error {
		calls.Add(1)
		<-release
		return os.WriteFile(dst, bytes.Repeat([]byte("a"), 1000), 0o644)
	}
	tracks, _ := chapterTracks([]AudioFile{longBook(40*3600, 6)})

	// Many asks for the same track while it's being cut: one cut, the same file for all.
	var wg sync.WaitGroup
	paths := make([]string, 8)
	for i := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i], _ = c.get(context.Background(), tracks[0])
		}()
	}
	// Someone who gives up waiting gets their own error; the cut carries on for the rest.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx, tracks[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("a caller who left: %v", err)
	}
	close(release)
	wg.Wait()
	for _, p := range paths {
		if p == "" || p != paths[0] {
			t.Fatalf("callers got %q", paths)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d cuts, want 1", n)
	}
	// A replaced source file (new size or time) is a different cut.
	replaced := tracks[0]
	replaced.MTimeMs++
	if c.pathFor(replaced) == c.pathFor(tracks[0]) {
		t.Fatal("a replaced file would be served from the old cut")
	}

	// The sweep: by age, then by size, oldest first, sparing what was just asked for.
	for _, tr := range tracks[1:] {
		if _, err := c.get(context.Background(), tr); err != nil {
			t.Fatal(err)
		}
	}
	age := func(tr AudioFile, d time.Duration) {
		at := now.Add(-d)
		if err := os.Chtimes(c.pathFor(tr), at, at); err != nil {
			t.Fatal(err)
		}
	}
	age(tracks[0], clipMaxAge+time.Hour) // too old
	age(tracks[1], 3*time.Hour)
	age(tracks[2], 2*time.Hour)
	age(tracks[3], time.Hour)
	age(tracks[4], time.Minute) // being read
	age(tracks[5], 2*time.Minute)
	if err := os.WriteFile(filepath.Join(dir, "cut-left.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "cut-left.tmp"), old, old)
	c.maxBytes = 3500 // room for three
	if n := c.sweep(); n != 4 {
		t.Fatalf("swept %d files, want 4 (one too old, two over the size, one leftover)", n)
	}
	for i, want := range []bool{false, false, false, true, true, true} {
		if got := c.ready(tracks[i]); got != want {
			t.Errorf("track %d kept=%v, want %v", i+1, got, want)
		}
	}
	// Under the cap nothing recent goes, however small the cap.
	c.maxBytes = 1
	if n := c.sweep(); n != 1 || !c.ready(tracks[4]) || !c.ready(tracks[5]) {
		t.Fatalf("swept %d; tracks asked for in the last few minutes must stay", n)
	}
}

// TestChapterCutIT cuts real chapter tracks with ffmpeg from a real AAC file and checks
// they're whole, the right length, and together as long as the file. Run with
// ARRMADA_FFMPEG_IT=1 where ffmpeg is installed.
func TestChapterCutIT(t *testing.T) {
	if os.Getenv("ARRMADA_FFMPEG_IT") == "" {
		t.Skip("set ARRMADA_FFMPEG_IT=1 where ffmpeg is installed to cut real chapter tracks")
	}
	dir := t.TempDir()
	meta := filepath.Join(dir, "chapters.txt")
	marks := []float64{0, 7.31, 19.05, 33.5, 48.92}
	const length = 61.0
	var b strings.Builder
	b.WriteString(";FFMETADATA1\ntitle=Whole Book\n")
	for i, start := range marks {
		end := length
		if i+1 < len(marks) {
			end = marks[i+1]
		}
		fmt.Fprintf(&b, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=Chapter %d\n", int(start*1000), int(end*1000), i+1)
	}
	if err := os.WriteFile(meta, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "book.m4b")
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%v:sample_rate=44100", length),
		"-i", meta, "-map_metadata", "1", "-map_chapters", "1", "-c:a", "aac", "-b:a", "64k", "-f", "mp4", src).CombinedOutput()
	if err != nil {
		t.Fatalf("making the book: %v %s", err, out)
	}
	info, err := mediainfo.ProbeAudioWith(context.Background(), "ffprobe", src)
	if err != nil || len(info.Chapters) != len(marks) {
		t.Fatalf("the book: %v, %d chapters", err, len(info.Chapters))
	}
	fi, _ := os.Stat(src)
	f := AudioFile{Index: 1, Ino: "1", Path: src, Name: "book.m4b", Ext: ".m4b", Size: fi.Size(), MTimeMs: fi.ModTime().UnixMilli()}
	fillProbe(info, &f, "")
	// The length rule is the only thing a one-minute test file doesn't meet.
	f.Duration += splitMinSeconds
	tracks, split := chapterTracks([]AudioFile{f})
	if !split {
		t.Fatal("not split")
	}
	f.Duration -= splitMinSeconds

	c := newClipCache(filepath.Join(dir, "cache"), "", nil)
	const frame = 1024.0 / 44100
	total := 0.0
	for i, tr := range tracks {
		want := tr.Clip.End - tr.Clip.Start
		if tr.Clip.Last {
			want = f.Duration - tr.Clip.Start
		}
		p, err := c.get(context.Background(), tr)
		if err != nil {
			t.Fatalf("track %d: %v", i+1, err)
		}
		got, err := mediainfo.ProbeAudioWith(context.Background(), "ffprobe", p)
		if err != nil {
			t.Fatalf("track %d can't be read: %v", i+1, err)
		}
		if got.Codec != "aac" || len(got.Chapters) != 0 {
			t.Errorf("track %d: codec %s, %d chapter marks; want the same audio and no marks", i+1, got.Codec, len(got.Chapters))
		}
		// A copy cuts on whole frames: up to one early at the start, one late at the end.
		if d := got.DurationSec - want; d < -frame || d > 3*frame {
			t.Errorf("track %d is %.3f s long, its chapter %.3f s", i+1, got.DurationSec, want)
		}
		// The index is at the front, so an app can start playing without a second request.
		head := make([]byte, 64)
		fh, _ := os.Open(p)
		_, _ = fh.Read(head)
		fh.Close()
		if !bytes.Contains(head, []byte("moov")) {
			t.Errorf("track %d doesn't start with its index", i+1)
		}
		// It decodes without errors from start to end.
		if out, err := exec.Command("ffmpeg", "-v", "error", "-i", p, "-f", "null", "-").CombinedOutput(); err != nil || len(bytes.TrimSpace(out)) > 0 {
			t.Errorf("track %d doesn't decode cleanly: %v %s", i+1, err, out)
		}
		total += got.DurationSec
	}
	if d := total - f.Duration; d < -frame || d > float64(len(tracks))*3*frame {
		t.Errorf("the tracks add up to %.3f s, the book is %.3f s", total, f.Duration)
	}
	if after, _ := os.Stat(src); after.Size() != fi.Size() || !after.ModTime().Equal(fi.ModTime()) {
		t.Error("the source file was changed")
	}
}
