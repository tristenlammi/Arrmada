//go:build linux

package convert

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// TestRealConversions runs the whole pipeline with real encoders on synthetic media. It
// needs the app image's ffmpeg, x265, SVT-AV1, dovi_tool and hdr10plus_tool, so it only
// runs when ARRMADA_FFMPEG_IT is set (inside the container).
func TestRealConversions(t *testing.T) {
	if os.Getenv("ARRMADA_FFMPEG_IT") == "" {
		t.Skip("set ARRMADA_FFMPEG_IT=1 inside the app image to run real encodes")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	lib, recycle, scratch := filepath.Join(dir, "lib"), filepath.Join(dir, "recycle"), filepath.Join(dir, "scratch")
	for _, d := range []string{lib, recycle, scratch} {
		must(t, os.MkdirAll(d, 0o755))
	}
	st, err := store.Open(filepath.Join(dir, "db"))
	must(t, err)
	defer st.Close()
	db := st.DB()
	log := testLogger()
	cfg := settings.NewService(db)
	mv := movies.NewService(db, nil, nil, lib, recycle, eventbus.New(log), log)
	s := NewService(db, mv, nil, cfg, "ffmpeg", "ffprobe", scratch, recycle, log)
	var watching atomic.Bool
	s.SetWatching(watching.Load)

	ff := func(args ...string) {
		t.Helper()
		out, err := exec.Command("ffmpeg", append([]string{"-y", "-hide_banner", "-loglevel", "error"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
		}
	}
	addMovie := func(id int, title, path string) {
		_, err := db.Exec(`INSERT INTO movies (id, tmdb_id, title, monitored, has_file, movie_file_path) VALUES (?, ?, ?, 1, 1, ?)`, id, 1000+id, title, path)
		must(t, err)
		must(t, s.IndexMovie(ctx, int64(id)))
	}
	srtFile := func(name, text string) string {
		p := filepath.Join(dir, name)
		must(t, os.WriteFile(p, []byte("1\n00:00:01,000 --> 00:00:04,000\n"+text+"\n\n2\n00:00:10,000 --> 00:00:14,000\n"+text+" again\n"), 0o644))
		return p
	}
	hdrX265 := "log-level=0:hdr10=1:repeat-headers=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:" +
		"master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,50):max-cll=1000,400"
	pqTags := []string{"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc"}
	src := "testsrc2=s=640x360:r=24:d=20,noise=alls=3:allf=u"

	// 1. A bloated H.264 film: English 5.1, Japanese stereo, an English commentary; English
	//    and French subtitles.
	bloated := filepath.Join(lib, "Bloated (2020).mkv")
	ff("-f", "lavfi", "-i", src, "-f", "lavfi", "-i", "sine=f=440:d=20", "-f", "lavfi", "-i", "sine=f=550:d=20",
		"-f", "lavfi", "-i", "sine=f=660:d=20", "-i", srtFile("en.srt", "Hello"), "-i", srtFile("fr.srt", "Bonjour"),
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a", "-map", "4", "-map", "5",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "0", "-pix_fmt", "yuv420p",
		"-c:a:0", "ac3", "-ac:a:0", "6", "-c:a:1", "aac", "-c:a:2", "aac", "-c:s", "srt",
		"-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=jpn",
		"-metadata:s:a:2", "language=eng", "-metadata:s:a:2", "title=Director's Commentary",
		"-metadata:s:s:0", "language=eng", "-metadata:s:s:1", "language=fre", bloated)
	addMovie(1, "Bloated", bloated)

	// 2. A lean H.264 encode: must be left alone.
	lean := filepath.Join(lib, "Lean (2020).mkv")
	ff("-f", "lavfi", "-i", "testsrc2=s=640x360:r=24:d=20", "-c:v", "libx264", "-preset", "veryfast", "-b:v", "150k", "-pix_fmt", "yuv420p", lean)
	addMovie(2, "Lean", lean)

	// 3. A bloated HDR10 HEVC file (near-lossless, like a remux).
	hdr10 := filepath.Join(lib, "HDR10 (2020).mkv")
	ff(append([]string{"-f", "lavfi", "-i", src, "-c:v", "libx265", "-preset", "ultrafast", "-crf", "2",
		"-x265-params", hdrX265 + ":pools=none", "-pix_fmt", "yuv420p10le"}, append(pqTags, hdr10)...)...)
	addMovie(3, "HDR10", hdr10)

	// 4. Dolby Vision 8.1 over that HDR10 base: the DV layer must be dropped, HDR10 kept.
	dvSrc := filepath.Join(lib, "Dolby Vision (2020).mkv")
	es := filepath.Join(dir, "dv.hevc")
	ff(append([]string{"-f", "lavfi", "-i", src, "-c:v", "libx265", "-preset", "ultrafast", "-crf", "2",
		"-x265-params", hdrX265 + ":pools=none:bframes=0", "-pix_fmt", "yuv420p10le"}, append(pqTags, "-f", "hevc", es)...)...)
	gen := filepath.Join(dir, "gen.json")
	must(t, os.WriteFile(gen, []byte(`{"cm_version":"V40","length":480,"level6":{"max_display_mastering_luminance":1000,"min_display_mastering_luminance":1,"max_content_light_level":1000,"max_frame_average_light_level":400}}`), 0o644))
	rpu := filepath.Join(dir, "rpu.bin")
	run(t, "dovi_tool", "generate", "-j", gen, "-o", rpu)
	inj := filepath.Join(dir, "dv.inj.hevc")
	run(t, "dovi_tool", "inject-rpu", "-i", es, "--rpu-in", rpu, "-o", inj)
	tmpMP4 := filepath.Join(dir, "dv.mp4")
	ff("-r", "24", "-i", inj, "-c", "copy", "-tag:v", "hvc1", tmpMP4)
	ff("-i", tmpMP4, "-c", "copy", dvSrc)
	addMovie(4, "Dolby Vision", dvSrc)
	if mi, err := probe(ctx, "ffprobe", dvSrc); err != nil || mi.HDR != "Dolby Vision" || mi.DVBase != "HDR10" {
		t.Fatalf("synthetic DV source probed as %+v (err %v)", mi, err)
	}

	// 7. HDR10+ over HDR10: must stay HEVC and keep its dynamic metadata.
	h10pSrc := filepath.Join(lib, "HDR10 Plus (2020).mkv")
	h10es, h10json, h10inj, h10mp4 := filepath.Join(dir, "h.hevc"), filepath.Join(dir, "h.json"), filepath.Join(dir, "h.inj.hevc"), filepath.Join(dir, "h.mp4")
	ff(append([]string{"-f", "lavfi", "-i", src, "-c:v", "libx265", "-preset", "ultrafast", "-crf", "2",
		"-x265-params", hdrX265 + ":pools=none:bframes=0", "-pix_fmt", "yuv420p10le"}, append(pqTags, "-f", "hevc", h10es)...)...)
	var scenes []string
	for i := 0; i < 480; i++ {
		scenes = append(scenes, fmt.Sprintf(`{"LuminanceParameters":{"AverageRGB":1000,"LuminanceDistributions":{"DistributionIndex":[1,5,10,25,50,75,90,95,99],"DistributionValues":[10,50,100,500,1000,2000,4000,6000,9000]},"MaxScl":[9000,9000,9000]},"NumberOfWindows":1,"TargetedSystemDisplayMaximumLuminance":0,"SceneFrameIndex":%d,"SceneId":0,"SequenceFrameIndex":%d}`, i, i))
	}
	must(t, os.WriteFile(h10json, []byte(`{"JSONInfo":{"HDR10plusProfile":"A","Version":"1.0"},"SceneInfo":[`+strings.Join(scenes, ",")+`],"SceneInfoSummary":{"SceneFirstFrameIndex":[0],"SceneFrameNumbers":[480]},"ToolInfo":{"Tool":"hdr10plus_tool","Version":"1.7.2"}}`), 0o644))
	run(t, "hdr10plus_tool", "inject", "-i", h10es, "-j", h10json, "-o", h10inj)
	ff("-r", "24", "-i", h10inj, "-c", "copy", "-tag:v", "hvc1", h10mp4)
	ff("-i", h10mp4, "-c", "copy", h10pSrc)
	addMovie(7, "HDR10 Plus", h10pSrc)
	if mi := mustProbe(t, h10pSrc); mi.HDR != "HDR10+" {
		t.Fatalf("synthetic HDR10+ source probed as %s", mi.HDR)
	}

	set(t, s, map[string]string{keyAuto: "true", keyKeepAudioLangs: "en", keyDropCommentary: "true", keyKeepSubLangs: "en"})
	go s.Run(ctx)

	waitDone := func(id int64, limit time.Duration) Job {
		t.Helper()
		deadline := time.Now().Add(limit)
		for time.Now().Before(deadline) {
			for _, j := range s.Jobs() {
				if j.MovieID == id && !activeState(j.State) {
					return j
				}
			}
			time.Sleep(time.Second)
		}
		for _, l := range s.Logs() {
			t.Log(l.Msg)
		}
		st := s.Status(ctx)
		t.Logf("status: %s — remaining %d", st.Message, st.Remaining)
		if lib, err := s.Library(ctx); err == nil {
			for _, c := range lib {
				t.Logf("  %s: candidate=%v needs=%+v bitrate=%d", c.Title, c.Candidate, c.Needs, c.Info.BitrateKbps)
			}
		}
		for _, jj := range s.Jobs() {
			t.Logf("  job %d %s: %s progress=%.2f paused=%q note=%q encoder=%s", jj.ID, jj.Title, jj.State, jj.Progress, jj.Paused, jj.Note, jj.Encoder)
		}
		_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
		t.Fatalf("movie %d didn't finish within %s", id, limit)
		return Job{}
	}

	// --- 1: bloated SDR film -------------------------------------------------------------
	j := waitDone(1, 3*time.Minute)
	if j.State != StateDone {
		t.Fatalf("bloated film: %s — %s", j.State, j.Note)
	}
	out := mustProbe(t, bloated)
	if out.VideoCodec != "hevc" || !out.TenBit {
		t.Errorf("bloated film became %s (10-bit %v), want 10-bit HEVC", out.VideoCodec, out.TenBit)
	}
	if out.AudioTracks != 1 || out.Audio[0].Lang != "eng" || out.Audio[0].Codec != "ac3" || out.Audio[0].Channels != 6 {
		t.Errorf("audio should be just the English 5.1 AC3, copied: %+v", out.Audio)
	}
	if out.SubTracks != 1 || out.Subs[0].Lang != "eng" {
		t.Errorf("subtitles should be just English: %+v", out.Subs)
	}
	if out.DurationSec < 19.5 || out.DurationSec > 20.5 {
		t.Errorf("duration %.2f, want 20", out.DurationSec)
	}
	if entries, _ := os.ReadDir(recycle); len(entries) == 0 {
		t.Error("the original should be in the recycle bin")
	}
	if s.Reclaimed(ctx) <= 0 {
		t.Error("space saved should be recorded")
	}
	t.Logf("bloated: %s → %s, %s", humanBytes(j.SrcBytes), humanBytes(j.OutBytes), j.Note)

	// --- 3 and 4: HDR10 and Dolby Vision (order depends on estimated saving) -------------
	for _, id := range []int64{3, 4} {
		j := waitDone(id, 8*time.Minute)
		if j.State != StateDone {
			t.Fatalf("movie %d: %s — %s", id, j.State, j.Note)
		}
		path := hdr10
		if id == 4 {
			path = dvSrc
		}
		out := mustProbe(t, path)
		if out.VideoCodec != "hevc" || out.HDR != "HDR10" || out.HDR10 == nil || out.HDR10.MaxCLL != "1000,400" {
			t.Errorf("movie %d: want HEVC HDR10 with its mastering data and no Dolby Vision, got codec %s hdr %s meta %+v",
				id, out.VideoCodec, out.HDR, out.HDR10)
		}
		t.Logf("movie %d: %s — %s", id, j.Encoder, j.Note)
	}

	// --- 7: HDR10+ stays HEVC with its dynamic metadata ---------------------------------
	j = waitDone(7, 8*time.Minute)
	if j.State != StateDone {
		t.Fatalf("HDR10+ film: %s — %s", j.State, j.Note)
	}
	if out := mustProbe(t, h10pSrc); out.VideoCodec != "hevc" || out.HDR != "HDR10+" || out.HDR10 == nil || out.HDR10.MaxCLL != "1000,400" {
		t.Errorf("HDR10+ film: want HEVC with HDR10+ and its static base, got codec %s hdr %s meta %+v", out.VideoCodec, out.HDR, out.HDR10)
	}
	t.Logf("HDR10+: %s — %s → %s", j.Encoder, humanBytes(j.SrcBytes), humanBytes(j.OutBytes))

	// --- 2: the lean file is never touched -----------------------------------------------
	for _, j := range s.Jobs() {
		if j.MovieID == 2 {
			t.Errorf("the lean file was picked: %s %s", j.State, j.Note)
		}
	}

	// --- 5: AV1 allowed — the side-by-side test decides --------------------------------
	set(t, s, map[string]string{keyAllowAV1: "true"})
	av1Src := filepath.Join(lib, "Choice (2021).mkv")
	ff("-f", "lavfi", "-i", src, "-c:v", "libx264", "-preset", "veryfast", "-crf", "0", "-pix_fmt", "yuv420p", av1Src)
	addMovie(5, "Choice", av1Src)
	requestOK(t, s.Request(ctx, movieKey(5)))
	j = waitDone(5, 8*time.Minute)
	if j.State != StateDone {
		t.Fatalf("choice film: %s — %s", j.State, j.Note)
	}
	out = mustProbe(t, av1Src)
	if out.VideoCodec != j.Codec {
		t.Errorf("job says %s but the file is %s", j.Codec, out.VideoCodec)
	}
	for _, l := range s.Logs() {
		if strings.Contains(l.Msg, "Choice") && (strings.Contains(l.Msg, "smaller") || strings.Contains(l.Msg, "HEVC looked")) {
			t.Logf("decision: %s", l.Msg)
		}
	}

	// --- 6: pausing a running encode while someone watches -----------------------------
	// This one is rehearsed on clips first, as a feature film would be — and it's
	// letterboxed (a 2.39:1 picture in a 16:9 frame), so its black bars come off.
	set(t, s, map[string]string{keyAllowAV1: "false"})
	preflightMinDuration = 30
	defer func() { preflightMinDuration = 20 * 60 }()
	slow := filepath.Join(lib, "Long (2022).mkv")
	ff("-f", "lavfi", "-i", "testsrc2=s=1280x536:r=24:d=60,noise=alls=3:allf=u,pad=1280:720:0:92", "-c:v", "libx264", "-preset", "veryfast", "-crf", "0", "-pix_fmt", "yuv420p", slow)
	addMovie(6, "Long", slow)
	requestOK(t, s.Request(ctx, movieKey(6)))
	var pid int
	deadline := time.Now().Add(2 * time.Minute)
	for pid == 0 && time.Now().Before(deadline) {
		s.mu.Lock()
		for _, jj := range s.jobs {
			if jj.MovieID == 6 && jj.State == StateEncoding && jj.Progress > 0.05 {
				for p := range jj.procs {
					pid = p
				}
			}
		}
		s.mu.Unlock()
		time.Sleep(500 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the long encode never got going")
	}
	watching.Store(true)
	s.applyPauses(ctx)
	if st := waitState(pid, "T"); st != "T" {
		t.Errorf("encode should be stopped while someone watches, process state %q", st)
	}
	before := jobProgress(s, 6)
	time.Sleep(3 * time.Second)
	if after := jobProgress(s, 6); after != before {
		t.Errorf("progress moved while paused: %.3f → %.3f", before, after)
	}
	watching.Store(false)
	s.applyPauses(ctx)
	if st := waitState(pid, "R", "S"); st == "T" {
		t.Error("encode should resume when the stream stops")
	}
	j = waitDone(6, 10*time.Minute)
	if j.State != StateDone {
		t.Fatalf("long film after pause/resume: %s — %s", j.State, j.Note)
	}
	t.Logf("long: paused and resumed, then %s → %s", humanBytes(j.SrcBytes), humanBytes(j.OutBytes))
	rehearsed := false
	for _, l := range s.Logs() {
		if strings.Contains(l.Msg, "Long") && strings.Contains(l.Msg, "test encode at CRF") {
			rehearsed = true
			t.Logf("long: %s", l.Msg)
		}
	}
	if !rehearsed {
		t.Error("the long film should have been rehearsed on clips before its full encode")
	}
	// The bars are gone, the picture isn't: 1280×536, and it passed the quality check
	// against the original's picture area (a job only finishes Done if it did).
	if out := mustProbe(t, slow); out.Width != 1280 || out.Height != 536 {
		t.Errorf("letterboxed film: output %d×%d, want the 1280×536 picture without its bars", out.Width, out.Height)
	} else {
		t.Logf("long: black bars removed, %d×%d, SSIM %.4f", out.Width, out.Height, j.SSIM)
	}
	if m := s.prefs(ctx).measured; len(m) > 0 {
		for k := range m {
			if strings.HasSuffix(k, slow) {
				t.Errorf("the converted film's test-encode measurement should be forgotten: %s", k)
			}
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// requestOK accepts "already converting": with automatic conversion on, the runner may
// have picked the new file up first, which is just as good.
func requestOK(t *testing.T, err error) {
	t.Helper()
	if err != nil && err != ErrAlreadyQueued {
		t.Fatal(err)
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func mustProbe(t *testing.T, path string) *MediaInfo {
	t.Helper()
	mi, err := probe(context.Background(), "ffprobe", path)
	if err != nil {
		t.Fatalf("probe %s: %v", path, err)
	}
	return mi
}

// procState is a process's state letter from /proc ("T" = stopped).
func procState(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	s := string(b)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
		return string(s[i+2])
	}
	return ""
}

// waitState polls a process's state for a couple of seconds until it's one of want — a
// signal takes a moment to land.
func waitState(pid int, want ...string) string {
	st := ""
	for i := 0; i < 40; i++ {
		st = procState(pid)
		for _, w := range want {
			if st == w {
				return st
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return st
}

func jobProgress(s *Service, movieID int64) float64 {
	for _, j := range s.Jobs() {
		if j.MovieID == movieID {
			return j.Progress
		}
	}
	return -1
}
