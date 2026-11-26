package automation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/music"
)

func TestMusicTranscodeTarget(t *testing.T) {
	tests := []struct {
		name string
		src  music.Quality
		p    MusicTranscodePrefs
		want music.Quality
		ok   bool
	}{
		{name: "disabled", src: music.QualityFLAC, p: MusicTranscodePrefs{Enabled: false, Format: "mp3", Quality: "v0", LosslessOnly: true}, want: music.QualityUnknown, ok: false},
		{name: "lossless to v0", src: music.QualityFLAC, p: MusicTranscodePrefs{Enabled: true, Format: "mp3", Quality: "v0", LosslessOnly: true}, want: music.QualityMP3V0, ok: true},
		{name: "lossless to 320", src: music.QualityFLAC, p: MusicTranscodePrefs{Enabled: true, Format: "mp3", Quality: "320", LosslessOnly: true}, want: music.QualityMP3320, ok: true},
		{name: "lossy skipped by default", src: music.QualityMP3320, p: MusicTranscodePrefs{Enabled: true, Format: "mp3", Quality: "v0", LosslessOnly: true}, want: music.QualityUnknown, ok: false},
		{name: "lossy allowed when configured", src: music.QualityMP3320, p: MusicTranscodePrefs{Enabled: true, Format: "mp3", Quality: "v0", LosslessOnly: false}, want: music.QualityMP3V0, ok: true},
		{name: "same or lower tier no-op", src: music.QualityMP3192, p: MusicTranscodePrefs{Enabled: true, Format: "mp3", Quality: "320", LosslessOnly: false}, want: music.QualityUnknown, ok: false},
		{name: "unknown source blocked", src: music.QualityUnknown, p: MusicTranscodePrefs{Enabled: true, Format: "mp3", Quality: "v0", LosslessOnly: false}, want: music.QualityUnknown, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := musicTranscodeTarget(tt.src, tt.p)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("musicTranscodeTarget(%q,%+v)= (%q,%v), want (%q,%v)", tt.src, tt.p, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestMusicTranscodeArgs(t *testing.T) {
	args, err := musicTranscodeArgs("/in.flac", "/out.mp3", "v0")
	if err != nil {
		t.Fatal(err)
	}
	joined := stringsJoin(args)
	for _, want := range []string{"-i /in.flac", "-map_metadata 0", "libmp3lame", "-q:a 0", "-id3v2_version 3", "/out.mp3"} {
		if !contains(joined, want) {
			t.Fatalf("args missing %q: %v", want, args)
		}
	}
}

func TestTranscodeAudioToMP3(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	_ = ffmpeg
	dir := t.TempDir()
	src := filepath.Join(dir, "in.wav")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-ac", "2", "-ar", "44100", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make wav: %v %s", err, out)
	}
	c := &Coordinator{}
	dst, err := c.transcodeAudioToMP3(context.Background(), src, "v0")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(dst) != ".mp3" {
		t.Fatalf("dst ext = %s", dst)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatal(err)
	}
}

func stringsJoin(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}

func contains(s, sub string) bool { return len(sub) == 0 || (len(s) >= len(sub) && indexString(s, sub) >= 0) }

func indexString(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
