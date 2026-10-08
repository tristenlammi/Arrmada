package audiobook

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercises the real ffmpeg on generated tones — never real media. Skipped where ffmpeg
// isn't installed, so CI without it stays green.
func TestMergeAgainstRealFFmpeg(t *testing.T) {
	if !Available() {
		t.Skip("needs ffmpeg and ffprobe on PATH")
	}
	ctx := context.Background()
	dir := t.TempDir()
	gen := func(name string, args ...string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		full := append([]string{"-y", "-hide_banner", "-loglevel", "error"}, args...)
		full = append(full, p)
		if out, err := exec.Command("ffmpeg", full...).CombinedOutput(); err != nil {
			t.Fatalf("generating %s: %v %s", name, err, out)
		}
		return p
	}
	tone := func(freq string, secs string) []string {
		return []string{"-f", "lavfi", "-i", "sine=frequency=" + freq + ":duration=" + secs}
	}
	cover := gen("cover.png", "-f", "lavfi", "-i", "color=c=orange:s=64x64", "-frames:v", "1")

	aac1 := gen("aac1.m4a", append(tone("440", "3"), "-c:a", "aac", "-metadata", "title=Chapter 1", "-metadata", "album=The Tone Book", "-metadata", "artist=A. Sine")...)
	aac2 := gen("aac2.m4a", append(tone("660", "2"), "-c:a", "aac")...)
	// An MP3 with an embedded cover, and one without.
	mp31 := gen("m1.mp3", append(tone("440", "2"), "-i", cover, "-map", "0:a", "-map", "1:v", "-c:a", "libmp3lame", "-c:v", "png", "-disposition:v:0", "attached_pic", "-id3v2_version", "3")...)
	mp32 := gen("m2.mp3", append(tone("550", "2"), "-c:a", "libmp3lame")...)

	for _, c := range []struct {
		name      string
		files     []string
		wantCopy  bool
		wantSecs  float64
		wantTitle string
		wantCover bool
	}{
		{"matching AAC", []string{aac1, aac2}, true, 5, "The Tone Book", false},
		{"MP3 with cover", []string{mp31, mp32}, false, 4, "Library Title", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if p := PlanFor(ctx, c.files); p.Copy != c.wantCopy {
				t.Errorf("copy = %v, want %v (plan %+v)", p.Copy, c.wantCopy, p)
			}
			// A hidden temp name with no audio extension: the format must not be guessed
			// from the name.
			out := filepath.Join(dir, ".Book.m4b.merging")
			_ = os.Remove(out)
			res, err := Merge(ctx, c.files, out, MergeOptions{Title: "Library Title", Author: "Library Author"})
			if err != nil {
				t.Fatalf("merge: %v", err)
			}
			if math.Abs(res.SourceSeconds-c.wantSecs) > 0.5 || math.Abs(res.OutputSeconds-res.SourceSeconds) > 0.5 {
				t.Errorf("durations = %+v, want about %.0fs both", res, c.wantSecs)
			}
			if d, err := Duration(ctx, out); err != nil || math.Abs(d-res.OutputSeconds) > 0.01 {
				t.Errorf("Duration = %v, %v", d, err)
			}
			tags := readTags(ctx, out)
			if tags["title"] != c.wantTitle {
				t.Errorf("title tag = %q, want %q (tags %v)", tags["title"], c.wantTitle, tags)
			}
			if tags["artist"] == "" {
				t.Errorf("no artist tag: %v", tags)
			}
			v, _ := exec.Command("ffprobe", "-v", "quiet", "-select_streams", "v", "-show_entries", "stream=codec_name", "-of", "csv=p=0", out).Output()
			if hasCover := strings.TrimSpace(string(v)) != ""; hasCover != c.wantCover {
				t.Errorf("cover present = %v, want %v", hasCover, c.wantCover)
			}
		})
	}

	if _, err := Duration(ctx, filepath.Join(dir, "missing.mp3")); err == nil {
		t.Error("Duration of a missing file must be an error")
	}
}
