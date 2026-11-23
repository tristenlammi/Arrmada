package videojoin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompatible(t *testing.T) {
	base := Layout{Duration: 1300, Streams: []Stream{
		{Type: "video", Codec: "h264", Profile: "High", Width: 1920, Height: 1080, PixFmt: "yuv420p", Extradata: "MD5:aa"},
		{Type: "audio", Codec: "aac", Profile: "LC", SampleRate: "48000", Channels: 2, Layout: "stereo", Language: "eng", Extradata: "MD5:bb"},
		{Type: "subtitle", Codec: "subrip", Language: "eng"},
	}}
	change := func(f func(l *Layout)) Layout {
		l := Layout{Duration: base.Duration, Streams: append([]Stream(nil), base.Streams...)}
		f(&l)
		return l
	}
	for _, c := range []struct {
		name  string
		other Layout
		want  string // "" = can be joined; else a word of the reason
	}{
		{"the same", change(func(*Layout) {}), ""},
		{"another length", change(func(l *Layout) { l.Duration = 1200 }), ""},
		{"a track short", change(func(l *Layout) { l.Streams = l.Streams[:2] }), "tracks"},
		{"another video codec", change(func(l *Layout) { l.Streams[0].Codec = "hevc" }), "hevc"},
		{"another picture size", change(func(l *Layout) { l.Streams[0].Height = 720 }), "picture"},
		{"other encoder settings", change(func(l *Layout) { l.Streams[0].Extradata = "MD5:cc" }), "settings"},
		{"other audio settings", change(func(l *Layout) { l.Streams[1].Extradata = "MD5:cc" }), "settings"},
		{"another channel count", change(func(l *Layout) { l.Streams[1].Channels = 6 }), "sound"},
		{"another audio language", change(func(l *Layout) { l.Streams[1].Language = "fre" }), "language"},
		{"another subtitle language", change(func(l *Layout) { l.Streams[2].Language = "spa" }), "language"},
		{"tracks in another order", change(func(l *Layout) { l.Streams[1], l.Streams[2] = l.Streams[2], l.Streams[1] }), "track 2"},
		{"no video", change(func(l *Layout) { l.Streams = l.Streams[1:] }), "tracks"},
	} {
		got := Compatible(base, c.other)
		if (got == "") != (c.want == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want something about %q", c.name, got, c.want)
		}
	}
	// Audio only on both sides is not an episode.
	audio := Layout{Duration: 1, Streams: base.Streams[1:2]}
	if got := Compatible(audio, audio); !strings.Contains(got, "video") {
		t.Errorf("audio only: %q", got)
	}
}

func TestParseProbe(t *testing.T) {
	l, err := parseProbe([]byte(`{"streams":[
		{"codec_type":"video","codec_name":"h264","profile":"High","width":1920,"height":1080,"pix_fmt":"yuv420p","extradata_hash":"MD5:1"},
		{"codec_type":"video","codec_name":"mjpeg","disposition":{"attached_pic":1}},
		{"codec_type":"audio","codec_name":"eac3","sample_rate":"48000","channels":6,"channel_layout":"5.1(side)","tags":{"LANGUAGE":"ENG"}}
	],"format":{"duration":"1321.504000"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Streams) != 2 || l.Streams[1].Language != "eng" || l.Streams[0].Extradata != "MD5:1" || l.Duration < 1321 || l.Duration > 1322 {
		t.Errorf("parsed %+v", l)
	}
	for _, bad := range []string{`{}`, `{"streams":[{"codec_type":"video"}],"format":{"duration":"0"}}`, `not json`} {
		if _, err := parseProbe([]byte(bad)); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

// TestJoinIT joins real files with ffmpeg: two parts made by one encoder join into a file
// as long as both with every track, a part encoded another way is refused before anything
// is written, and a failed join leaves nothing behind. Run with ARRMADA_FFMPEG_IT=1 where
// ffmpeg is installed.
func TestJoinIT(t *testing.T) {
	if os.Getenv("ARRMADA_FFMPEG_IT") == "" {
		t.Skip("set ARRMADA_FFMPEG_IT=1 where ffmpeg is installed to join real files")
	}
	ctx := context.Background()
	dir := t.TempDir()
	clip := func(name string, secs, size string, extra ...string) string {
		p := filepath.Join(dir, name)
		args := []string{"-v", "error", "-y",
			"-f", "lavfi", "-i", "testsrc=duration=" + secs + ":size=" + size + ":rate=25",
			"-f", "lavfi", "-i", "sine=frequency=440:duration=" + secs,
			"-c:v", "mpeg4", "-q:v", "5", "-c:a", "aac", "-b:a", "96k", "-metadata:s:a:0", "language=eng"}
		args = append(args, extra...)
		if out, err := exec.Command("ffmpeg", append(args, p)...).CombinedOutput(); err != nil {
			t.Fatalf("making %s: %v: %s", name, err, out)
		}
		return p
	}
	// A name with a quote and spaces, as releases have.
	p1 := clip("Show - S01E01 - It's Long Part 1.mkv", "6", "320x240")
	p2 := clip("Show - S01E02 - It's Long Part 2.mkv", "4", "320x240")
	other := clip("Show - S01E02 - other size.mkv", "4", "640x480")

	l1, err := Probe(ctx, p1)
	if err != nil {
		t.Fatal(err)
	}
	l2, err := Probe(ctx, p2)
	if err != nil {
		t.Fatal(err)
	}
	lo, err := Probe(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if why := Compatible(l1, l2); why != "" {
		t.Fatalf("two parts from one encoder: %s", why)
	}
	if why := Compatible(l1, lo); why == "" {
		t.Fatal("a part of another picture size was called joinable")
	}

	out := filepath.Join(dir, "joined", "Show - S01E01.mkv")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Join(ctx, []string{p1, p2}, []Layout{l1, l2}, out); err != nil {
		t.Fatalf("join: %v", err)
	}
	got, err := Probe(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Streams) != 2 || got.Duration < 9.5 || got.Duration > 10.5 {
		t.Errorf("joined: %d tracks, %.2f s; want 2 tracks and about 10 s", len(got.Streams), got.Duration)
	}
	if why := Compatible(l1, got); why != "" {
		t.Errorf("the joined file isn't encoded as its parts are: %s", why)
	}
	// The whole file decodes, across the seam, with no errors.
	if b, err := exec.Command("ffmpeg", "-v", "error", "-i", out, "-f", "null", "-").CombinedOutput(); err != nil || len(strings.TrimSpace(string(b))) > 0 {
		t.Errorf("decoding the joined file: %v: %s", err, b)
	}
	// The parts are as they were.
	for p, l := range map[string]Layout{p1: l1, p2: l2} {
		if again, err := Probe(ctx, p); err != nil || again.Duration != l.Duration {
			t.Errorf("%s changed: %v", filepath.Base(p), err)
		}
	}
	// Only the joined file is left beside it: no list file.
	if entries, _ := os.ReadDir(filepath.Dir(out)); len(entries) != 1 {
		t.Errorf("%d files beside the joined one", len(entries)-1)
	}

	// An existing file is never written over, and a join that fails leaves nothing.
	if err := Join(ctx, []string{p1, p2}, []Layout{l1, l2}, out); err == nil {
		t.Error("joined over an existing file")
	}
	gone := filepath.Join(dir, "joined", "missing.mkv")
	if err := Join(ctx, []string{p1, filepath.Join(dir, "nope.mkv")}, []Layout{l1, l2}, gone); err == nil {
		t.Error("joined a part that isn't there")
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Error("a failed join left its file behind")
	}
	// A length that doesn't add up is refused and removed.
	short := filepath.Join(dir, "joined", "short.mkv")
	wrong := l2
	wrong.Duration = 60
	if err := Join(ctx, []string{p1, p2}, []Layout{l1, wrong}, short); err == nil || !strings.Contains(err.Error(), "long") {
		t.Errorf("a join shorter than its parts: %v", err)
	}
	if _, err := os.Stat(short); !os.IsNotExist(err) {
		t.Error("a join of the wrong length was kept")
	}
}
