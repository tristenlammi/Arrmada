package videojoin

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompatible(t *testing.T) {
	base := Layout{Duration: 1300, Format: "matroska,webm", Streams: []Stream{
		{Index: 0, Type: "video", Codec: "h264", Profile: "High", Width: 1920, Height: 1080, PixFmt: "yuv420p", Extradata: "MD5:aa"},
		{Index: 1, Type: "audio", Codec: "aac", Profile: "LC", SampleRate: "48000", Channels: 2, Layout: "stereo", Language: "eng", Extradata: "MD5:bb"},
		{Index: 2, Type: "subtitle", Codec: "subrip", Language: "eng"},
		{Index: 3, Type: "attachment", Codec: "ttf"},
	}}
	change := func(f func(l *Layout)) Layout {
		l := base
		l.Streams = append([]Stream(nil), base.Streams...)
		f(&l)
		return l
	}
	flac := func(l *Layout) { l.Streams[1].Codec, l.Streams[1].Profile, l.Streams[1].Bits = "flac", "", 16 }
	for _, c := range []struct {
		name        string
		first, next Layout
		want        string // "" = can be joined; else a word of the reason
	}{
		{"the same", base, change(func(*Layout) {}), ""},
		{"another length", base, change(func(l *Layout) { l.Duration = 1200 }), ""},
		{"a track short", base, change(func(l *Layout) { l.Streams = l.Streams[:2] }), "tracks"},
		{"another video codec", base, change(func(l *Layout) { l.Streams[0].Codec = "hevc" }), "hevc"},
		{"another picture size", base, change(func(l *Layout) { l.Streams[0].Height = 720 }), "picture"},
		{"other encoder settings", base, change(func(l *Layout) { l.Streams[0].Extradata = "MD5:cc" }), "settings"},
		{"other audio settings", base, change(func(l *Layout) { l.Streams[1].Extradata = "MD5:cc" }), "settings"},
		{"another channel count", base, change(func(l *Layout) { l.Streams[1].Channels = 6 }), "sound"},
		{"another audio language", base, change(func(l *Layout) { l.Streams[1].Language = "fre" }), "language"},
		{"another subtitle language", base, change(func(l *Layout) { l.Streams[2].Language = "spa" }), "language"},
		{"tracks in another order", base, change(func(l *Layout) { l.Streams[1], l.Streams[2] = l.Streams[2], l.Streams[1] }), "track 2"},
		{"no video", base, change(func(l *Layout) { l.Streams = l.Streams[1:] }), "tracks"},
		// FLAC's setup data holds each file's own length and checksum.
		{"FLAC, each part its own setup data", change(flac), change(func(l *Layout) { flac(l); l.Streams[1].Extradata = "MD5:cc" }), ""},
		{"FLAC of another bit depth", change(flac), change(func(l *Layout) { flac(l); l.Streams[1].Bits = 24 }), "sound"},
		// An ASS subtitle's styles, a VobSub's palette.
		{"subtitles styled differently", change(func(l *Layout) { l.Streams[2].Codec, l.Streams[2].Extradata = "ass", "MD5:1" }),
			change(func(l *Layout) { l.Streams[2].Codec, l.Streams[2].Extradata = "ass", "MD5:2" }), "styled"},
		{"a part starting late", base, change(func(l *Layout) { l.Start = 3 }), "starts"},
		{"a part starting a frame late", base, change(func(l *Layout) { l.Start = 0.067 }), ""},
		{"more fonts in one part", base, change(func(l *Layout) {
			l.Streams = append(l.Streams, Stream{Index: 4, Type: "attachment", Codec: "otf"})
		}), ""},
		{"cover art in one part only", base, change(func(l *Layout) {
			l.Streams = append([]Stream{l.Streams[0], {Index: 1, Type: "video", Codec: "mjpeg", Cover: true}}, l.Streams[1:]...)
		}), "tracks"},
		{"cover art in each, different pictures", change(func(l *Layout) {
			l.Streams = append(l.Streams[:3:3], Stream{Index: 3, Type: "video", Codec: "png", Cover: true, Extradata: "MD5:1"})
		}), change(func(l *Layout) {
			l.Streams = append(l.Streams[:3:3], Stream{Index: 3, Type: "video", Codec: "png", Cover: true, Extradata: "MD5:2", Width: 9})
		}), ""},
		{"a timecode in one part only", base, change(func(l *Layout) {
			l.Streams = append(l.Streams[:3:3], Stream{Index: 3, Type: "data", Codec: "bin_data"})
		}), "tracks"},
	} {
		got := Compatible(c.first, c.next)
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
		{"index":0,"codec_type":"video","codec_name":"h264","profile":"High","width":1920,"height":1080,"pix_fmt":"yuv420p","extradata_hash":"MD5:1","disposition":{"default":1}},
		{"index":1,"codec_type":"video","codec_name":"mjpeg","disposition":{"attached_pic":1}},
		{"index":2,"codec_type":"audio","codec_name":"eac3","sample_rate":"48000","channels":6,"channel_layout":"5.1(side)","tags":{"LANGUAGE":"ENG"}},
		{"index":3,"codec_type":"subtitle","codec_name":"subrip","disposition":{"default":0,"forced":1,"hearing_impaired":1}}
	],"chapters":[{"start_time":"0.000000","end_time":"60.5","tags":{"title":"Intro"}}],
	"format":{"format_name":"matroska,webm","duration":"1321.504000","start_time":"0.500000","tags":{"TITLE":"The Long One"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Streams) != 4 || len(l.played()) != 3 || !l.Streams[1].Cover || l.Streams[2].Language != "eng" || l.Streams[0].Extradata != "MD5:1" {
		t.Errorf("parsed %+v", l)
	}
	if got := strings.Join(l.Streams[3].Disposition, "+"); got != "forced+hearing_impaired" {
		t.Errorf("subtitle flags %q", got)
	}
	if math.Abs(l.Length()-1321.004) > 0.001 || l.Title != "The Long One" || len(l.Chapters) != 1 || l.Chapters[0].Title != "Intro" {
		t.Errorf("length %.3f, title %q, chapters %+v", l.Length(), l.Title, l.Chapters)
	}
	for _, bad := range []string{`{}`, `{"streams":[{"codec_type":"video"}],"format":{"duration":"0"}}`, `not json`,
		`{"streams":[{"codec_type":"data"}],"format":{"duration":"10"}}`} {
		if _, err := parseProbe([]byte(bad)); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestFFMetadata(t *testing.T) {
	got := ffmetadata([]Layout{
		{Duration: 6, Title: "It's = Long; #1", Chapters: []Chapter{{0, 3, "Cold open"}, {3, 6, "Act 1"}}},
		{Duration: 4, Start: 0.5, Chapters: []Chapter{{0.5, 4.5, "Act 2"}}},
	})
	for _, want := range []string{"title=It's \\= Long\\; \\#1\n", "START=3000\nEND=6000\ntitle=Act 1\n", "START=6000\nEND=10000\ntitle=Act 2\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
}

// TestJoinIT joins real files with ffmpeg. Run with ARRMADA_FFMPEG_IT=1 where ffmpeg is
// installed.
func TestJoinIT(t *testing.T) {
	if os.Getenv("ARRMADA_FFMPEG_IT") == "" {
		t.Skip("set ARRMADA_FFMPEG_IT=1 where ffmpeg is installed to join real files")
	}
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %v: %v: %s", args, err, out)
		}
	}
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// clip makes a part: a picture and a sound of secs seconds, then extra inputs and
	// options.
	clip := func(name, secs, size string, extra ...string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		args := []string{"-f", "lavfi", "-i", "testsrc=duration=" + secs + ":size=" + size + ":rate=25",
			"-f", "lavfi", "-i", "sine=frequency=440:duration=" + secs}
		args = append(args, extra...)
		run(append(args, p)...)
		return p
	}
	plain := []string{"-c:v", "mpeg4", "-q:v", "5", "-c:a", "aac", "-b:a", "96k", "-metadata:s:a:0", "language=eng"}
	probe := func(p string) Layout {
		t.Helper()
		l, err := Probe(ctx, p)
		if err != nil {
			t.Fatalf("probing %s: %v", filepath.Base(p), err)
		}
		return l
	}
	clean := func(p string) { // the whole file decodes, across the seam, with no errors
		t.Helper()
		if b, err := exec.Command("ffmpeg", "-v", "error", "-i", p, "-map", "0:V", "-map", "0:a?", "-f", "null", "-").CombinedOutput(); err != nil || len(strings.TrimSpace(string(b))) > 0 {
			t.Errorf("decoding %s: %v: %s", filepath.Base(p), err, b)
		}
	}
	join := func(name string, parts ...string) (string, error) {
		t.Helper()
		ls := make([]Layout, len(parts))
		for i, p := range parts {
			ls[i] = probe(p)
			if i > 0 {
				if why := Compatible(ls[0], ls[i]); why != "" {
					t.Fatalf("%s: parts called unjoinable: %s", name, why)
				}
			}
		}
		out := filepath.Join(dir, "joined-"+name, filepath.Base(parts[0]))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatal(err)
		}
		return out, Join(ctx, parts, ls, out)
	}
	near := func(what string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 0.5 {
			t.Errorf("%s: %.2f, want about %.2f", what, got, want)
		}
	}

	t.Run("plain", func(t *testing.T) {
		// A name with a quote and spaces, as releases have.
		p1 := clip("Show - S01E01 - It's Long Part 1.mkv", "6", "320x240", plain...)
		p2 := clip("Show - S01E02 - It's Long Part 2.mkv", "4", "320x240", plain...)
		other := clip("Show - S01E02 - other size.mkv", "4", "640x480", plain...)
		l1, l2 := probe(p1), probe(p2)
		if why := Compatible(l1, probe(other)); why == "" {
			t.Fatal("a part of another picture size was called joinable")
		}
		out, err := join("plain", p1, p2)
		if err != nil {
			t.Fatalf("join: %v", err)
		}
		got := probe(out)
		if len(got.played()) != 2 {
			t.Errorf("joined: %d tracks, want 2", len(got.played()))
		}
		near("joined length", got.Length(), 10)
		if why := Compatible(l1, got); why != "" {
			t.Errorf("the joined file isn't encoded as its parts are: %s", why)
		}
		clean(out)
		for p, l := range map[string]Layout{p1: l1, p2: l2} {
			if again, err := Probe(ctx, p); err != nil || again.Duration != l.Duration {
				t.Errorf("%s changed: %v", filepath.Base(p), err)
			}
		}
		// Only the joined file is left beside it: no list or metadata file.
		if entries, _ := os.ReadDir(filepath.Dir(out)); len(entries) != 1 {
			t.Errorf("%d files beside the joined one", len(entries)-1)
		}
		// An existing file is never written over, and a join that fails leaves nothing.
		if err := Join(ctx, []string{p1, p2}, []Layout{l1, l2}, out); err == nil {
			t.Error("joined over an existing file")
		}
		gone := filepath.Join(filepath.Dir(out), "missing.mkv")
		if err := Join(ctx, []string{p1, filepath.Join(dir, "nope.mkv")}, []Layout{l1, l2}, gone); err == nil {
			t.Error("joined a part that isn't there")
		}
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Error("a failed join left its file behind")
		}
		// A length that doesn't add up is refused and removed.
		short := filepath.Join(filepath.Dir(out), "short.mkv")
		wrong := l2
		wrong.Duration = 60
		if err := Join(ctx, []string{p1, p2}, []Layout{l1, wrong}, short); err == nil || !strings.Contains(err.Error(), "long") {
			t.Errorf("a join shorter than its parts: %v", err)
		}
		if _, err := os.Stat(short); !os.IsNotExist(err) {
			t.Error("a join of the wrong length was kept")
		}
	})

	t.Run("flags, chapters and title", func(t *testing.T) {
		// Two sound tracks, the second the default; forced English subtitles; a font for
		// them; chapters.
		font := write("font.ttf", "not really a font")
		srt := write("subs.srt", "1\n00:00:00,500 --> 00:00:02,000\nHello\n\n2\n00:00:02,500 --> 00:00:03,500\nThere\n")
		part := func(name, secs, meta string) string {
			return clip(name, secs, "320x240",
				"-f", "lavfi", "-i", "sine=frequency=660:duration="+secs, "-i", srt, "-i", meta,
				"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:s", "-map_chapters", "4", "-map_metadata", "4",
				"-c:v", "mpeg4", "-q:v", "5", "-c:a", "aac", "-b:a", "96k", "-c:s", "srt",
				"-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=spa", "-metadata:s:s:0", "language=eng",
				"-disposition:a:0", "0", "-disposition:a:1", "default", "-disposition:s:0", "forced",
				"-attach", font, "-metadata:s:t", "mimetype=application/x-truetype-font")
		}
		p1 := part("Flags Part 1.mkv", "6", write("m1.txt", ";FFMETADATA1\ntitle=The Long One\n\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=3000\ntitle=Cold open\n\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=3000\nEND=6000\ntitle=Act 1\n"))
		p2 := part("Flags Part 2.mkv", "4", write("m2.txt", ";FFMETADATA1\ntitle=The Long One Part 2\n\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=4000\ntitle=Act 2\n"))
		out, err := join("flags", p1, p2)
		if err != nil {
			t.Fatalf("join: %v", err)
		}
		clean(out)
		raw, err := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", "-show_chapters", "-show_format", out).Output()
		if err != nil {
			t.Fatal(err)
		}
		var p probeOut
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		var flags []string
		for _, s := range p.Streams {
			flags = append(flags, s.CodecType+":"+tag(s.Tags, "language")+":"+map[int]string{0: "-", 1: "default"}[s.Disposition["default"]]+
				map[int]string{0: "", 1: "+forced"}[s.Disposition["forced"]])
		}
		// The video track is as the first part has it, whatever that is.
		video := map[bool]string{false: "-", true: "default"}[strings.Contains(strings.Join(probe(p1).Streams[0].Disposition, "+"), "default")]
		if got, want := strings.Join(flags, " "), "video::"+video+" audio:eng:- audio:spa:default subtitle:eng:-+forced attachment::-"; got != want {
			t.Errorf("tracks %q, want %q", got, want)
		}
		got := probe(out)
		if got.Title != "The Long One" {
			t.Errorf("title %q", got.Title)
		}
		var chs []string
		for _, c := range got.Chapters {
			chs = append(chs, c.Title)
		}
		if strings.Join(chs, ",") != "Cold open,Act 1,Act 2" {
			t.Fatalf("chapters %+v", got.Chapters)
		}
		near("Act 2 starts", got.Chapters[2].Start, 6)
		near("Act 2 ends", got.Chapters[2].End, 10)
	})

	t.Run("cover art", func(t *testing.T) {
		cover := filepath.Join(dir, "cover.png")
		run("-f", "lavfi", "-i", "color=red:size=64x64", "-frames:v", "1", cover)
		// Matroska keeps cover art as an attached picture file, MP4 as a track of its own.
		mkv := func(name, secs string) string {
			return clip(name, secs, "320x240", append([]string{"-attach", cover, "-metadata:s:t", "mimetype=image/png",
				"-metadata:s:t", "filename=cover.png"}, plain...)...)
		}
		mp4 := func(name, secs string) string {
			return clip(name, secs, "320x240", "-i", cover, "-map", "0:v", "-map", "1:a", "-map", "2:v",
				"-c:v:0", "mpeg4", "-q:v", "5", "-c:v:1", "png", "-c:a", "aac", "-b:a", "96k", "-disposition:v:1", "attached_pic")
		}
		for _, c := range []struct {
			name   string
			p1, p2 string
		}{
			{"mkv", mkv("Cover Part 1.mkv", "6"), mkv("Cover Part 2.mkv", "4")},
			{"mp4", mp4("Cover Part 1.mp4", "6"), mp4("Cover Part 2.mp4", "4")},
		} {
			if l := probe(c.p1); len(l.Streams) != 3 || !l.Streams[2].Cover {
				t.Fatalf("%s: the part has no cover art: %+v", c.name, l.Streams)
			}
			out, err := join("cover-"+c.name, c.p1, c.p2)
			if err != nil {
				t.Fatalf("%s: join: %v", c.name, err)
			}
			got := probe(out)
			if len(got.Streams) != 2 || got.Streams[0].Type != "video" || got.Streams[0].Cover {
				t.Errorf("%s: joined tracks %+v, want the picture and the sound", c.name, got.Streams)
			}
			near(c.name+" joined length", got.Length(), 10)
			clean(out)
		}
	})

	t.Run("a timecode track", func(t *testing.T) {
		part := func(name, secs string) string {
			return clip(name, secs, "320x240", append([]string{"-timecode", "01:00:00:00"}, plain...)...)
		}
		p1, p2 := part("Timecode Part 1.mov", "6"), part("Timecode Part 2.mov", "4")
		l := probe(p1)
		data := 0
		for _, s := range l.Streams {
			if s.Type == "data" {
				data++
			}
		}
		if data != 1 {
			t.Fatalf("the part has %d data tracks: %+v", data, l.Streams)
		}
		out, err := join("timecode", p1, p2)
		if err != nil {
			t.Fatalf("join: %v", err)
		}
		// The part's timecode track isn't copied (MOV writes one of its own from the
		// video's timecode tag); what plays is the parts'.
		got := probe(out)
		if why := Compatible(l, got); why != "" && !strings.Contains(why, "tracks") {
			t.Errorf("the joined file isn't as its parts: %s", why)
		}
		if gp, lp := got.played(), l.played(); len(gp) != len(lp) {
			t.Errorf("joined: %d tracks that play, the parts %d", len(gp), len(lp))
		}
		near("joined length", got.Length(), 10)
		clean(out)
	})

	t.Run("FLAC", func(t *testing.T) {
		part := func(name, secs string) string {
			return clip(name, secs, "320x240", "-c:v", "mpeg4", "-q:v", "5", "-c:a", "flac")
		}
		p1, p2 := part("Flac Part 1.mkv", "6"), part("Flac Part 2.mkv", "4")
		l1, l2 := probe(p1), probe(p2)
		if l1.Streams[1].Extradata == l2.Streams[1].Extradata {
			t.Log("the FLAC parts' setup data came out the same; the test proves less")
		}
		out, err := join("flac", p1, p2)
		if err != nil {
			t.Fatalf("join: %v", err)
		}
		near("joined length", probe(out).Length(), 10)
		clean(out)
	})

	t.Run("subtitles styled differently", func(t *testing.T) {
		ass := func(name, font string) string {
			return write(name, "[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\n"+
				"Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n"+
				"Style: Default,"+font+",20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1\n\n"+
				"[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"+
				"Dialogue: 0,0:00:00.50,0:00:02.00,Default,,0,0,0,,Hello\n")
		}
		part := func(name, secs, sub string) string {
			return clip(name, secs, "320x240", append([]string{"-i", sub, "-map", "0:v", "-map", "1:a", "-map", "2:s", "-c:s", "ass"}, plain...)...)
		}
		p1 := part("Ass Part 1.mkv", "6", ass("a.ass", "Arial"))
		p2 := part("Ass Part 2.mkv", "4", ass("b.ass", "Comic Sans MS"))
		same := part("Ass Part 2 same.mkv", "4", ass("c.ass", "Arial"))
		if why := Compatible(probe(p1), probe(p2)); !strings.Contains(why, "styled") {
			t.Errorf("subtitles of other styles: %q", why)
		}
		if why := Compatible(probe(p1), probe(same)); why != "" {
			t.Errorf("subtitles of the same styles: %q", why)
		}
	})

	t.Run("a part starting late", func(t *testing.T) {
		p1 := clip("Late Part 1.mkv", "6", "320x240", plain...)
		p2 := clip("Late Part 2.mkv", "4", "320x240", append([]string{"-output_ts_offset", "3"}, plain...)...)
		l2 := probe(p2)
		near("a late part's length", l2.Length(), 4)
		if why := Compatible(probe(p1), l2); !strings.Contains(why, "starts") {
			t.Errorf("a part starting 3 s in: %q", why)
		}
	})
}
