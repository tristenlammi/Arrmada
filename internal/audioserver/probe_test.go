package audioserver

import (
	"os"
	"testing"
)

// Real ffprobe output for an M4B with two chapters (made with ffmpeg in the Arrmada image).
func TestParseProbeRealM4B(t *testing.T) {
	out, err := os.ReadFile("testdata/ffprobe_m4b.json")
	if err != nil {
		t.Fatal(err)
	}
	var af AudioFile
	if !parseProbe(out, &af) {
		t.Fatal("parse failed")
	}
	if af.Duration < 5.9 || af.Duration > 6.1 || af.Codec != "aac" || af.Bitrate <= 0 || af.Title != "Test Book" {
		t.Fatalf("parsed %+v", af)
	}
	if len(af.Chapters) != 2 || af.Chapters[1].Title != "Chapter One" || af.Chapters[1].Start != 3 || af.Chapters[1].End != 6 {
		t.Fatalf("chapters %+v", af.Chapters)
	}
	// A single file with embedded chapters uses them; several files get one chapter each.
	if ch := bookChapters([]AudioFile{af}); len(ch) != 2 {
		t.Fatalf("book chapters from one file: %+v", ch)
	}
	two := []AudioFile{{Name: "01.mp3", Ext: ".mp3", Duration: 100}, {Name: "02.mp3", Ext: ".mp3", Duration: 50, Title: "Part Two"}}
	ch := bookChapters(two)
	if len(ch) != 2 || ch[1].Start != 100 || ch[1].End != 150 || ch[1].Title != "Part Two" || ch[0].Title != "01" {
		t.Fatalf("book chapters from files: %+v", ch)
	}
}
