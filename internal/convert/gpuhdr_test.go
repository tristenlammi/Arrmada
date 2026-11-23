package convert

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// hdrFilm is a 4K HEVC film of one HDR kind, with the mastering data a real HDR10 grade has.
func hdrFilm(hdr string) *MediaInfo {
	mi := film("hevc", 3840, 2160, 60000)
	mi.HDR, mi.TenBit = hdr, true
	mi.ColorPrimaries, mi.ColorTransfer, mi.ColorSpace = "bt2020", "smpte2084", "bt2020nc"
	switch hdr {
	case "HLG":
		mi.ColorTransfer = "arib-std-b67"
	case "Dolby Vision":
		mi.DVProfile, mi.DVBase = 8, "HDR10"
	}
	if hdr != "HLG" {
		mi.HDR10 = &HDR10Meta{MasterDisplay: probeMasterDisplay, MaxCLL: probeMaxCLL}
	}
	return mi
}

// gpuService has a working CPU and a VAAPI HEVC encoder that passed (or failed) the HDR test.
func gpuService(t *testing.T, hdr10, hlg bool) *Service {
	t.Helper()
	s := newTestService(t)
	s.hdr10plusTool = "/usr/local/bin/hdr10plus_tool"
	s.encoders = append(workingCPUEncoders(),
		Encoder{Codec: "hevc", Name: "hevc_vaapi", Kind: "vaapi", Label: "VAAPI (HEVC)", Hardware: true, Available: true,
			TenBit: true, HDR10: hdr10, HLG: hlg})
	return s
}

// Where each kind of HDR is encoded, with the GPU's HDR test passed or failed — and whether
// the plan that results keeps the HDR. Only plain HDR10 and HLG ever reach the GPU.
func TestGPUHDRRouting(t *testing.T) {
	type want struct{ gpu, keeps bool }
	cases := []struct {
		name       string
		mi         *MediaInfo
		plan       Plan
		h10p       bool // hdr10plus_tool found HDR10+ in a file labelled HDR10
		pass, fail want
	}{
		{"HDR10", hdrFilm("HDR10"), Plan{VideoCodec: "hevc"}, false, want{true, true}, want{false, true}},
		{"HLG", hdrFilm("HLG"), Plan{VideoCodec: "hevc"}, false, want{true, true}, want{false, true}},
		{"HDR10+ (labelled)", hdrFilm("HDR10+"), Plan{VideoCodec: "hevc"}, true, want{false, true}, want{false, true}},
		{"HDR10+ found by the tool", hdrFilm("HDR10"), Plan{VideoCodec: "hevc"}, true, want{false, true}, want{false, true}},
		{"Dolby Vision, layer dropped", hdrFilm("Dolby Vision"), Plan{VideoCodec: "hevc"}, false, want{false, true}, want{false, true}},
		{"Dolby Vision, layer kept", hdrFilm("Dolby Vision"), Plan{VideoCodec: "hevc", KeepDV: true}, false, want{false, true}, want{false, true}},
		{"SDR", film("h264", 1920, 1080, 12000), Plan{VideoCodec: "hevc"}, false, want{true, true}, want{true, true}},
	}
	for _, c := range cases {
		for _, passed := range []bool{true, false} {
			s := gpuService(t, passed, passed)
			s.doviTool = "/usr/local/bin/dovi_tool"
			w := c.fail
			if passed {
				w = c.pass
			}
			enc := s.encoderChoiceHDR(c.mi, "hevc", true, c.h10p)
			if enc.Hardware != w.gpu {
				t.Errorf("%s, HDR test passed=%v: encoded on %s, want GPU=%v", c.name, passed, enc.Name, w.gpu)
			}
			if got := s.canPreserveHDR(c.mi, c.plan, enc, c.h10p); got != w.keeps {
				t.Errorf("%s, HDR test passed=%v: keeps HDR on %s = %v, want %v", c.name, passed, enc.Name, got, w.keeps)
			}
			// GPU mode off: HDR is the CPU's as it always was.
			if e := s.encoderChoiceHDR(c.mi, "hevc", false, c.h10p); e.Hardware {
				t.Errorf("%s: GPU off but encoded on %s", c.name, e.Name)
			}
		}
	}

	// A GPU that passed only HDR10 keeps HLG on the CPU.
	s := gpuService(t, true, false)
	if e := s.encoderChoice(hdrFilm("HLG"), "hevc", true); e.Hardware {
		t.Errorf("HLG went to %s, which failed the HLG test", e.Name)
	}
	// The plan check refuses any HDR on a hardware encoder that didn't pass for it.
	failed := gpuService(t, false, false).encoders[2]
	if gpuService(t, false, false).canPreserveHDR(hdrFilm("HDR10"), Plan{VideoCodec: "hevc"}, failed, false) {
		t.Error("an encoder that failed the HDR test must not be trusted with HDR10")
	}
	// No AV1 on the GPU: an AV1 plan's HDR stays on the CPU.
	if e := s.encoderChoice(hdrFilm("HDR10"), "av1", true); e.Hardware {
		t.Errorf("AV1 HDR10 went to %s", e.Name)
	}
}

// A GPU whose real output lost the HDR stops getting that kind for the rest of the run.
func TestRevokeGPUHDR(t *testing.T) {
	s := gpuService(t, true, true)
	if e := s.encoderChoice(hdrFilm("HDR10"), "hevc", true); !e.Hardware {
		t.Fatalf("HDR10 should go to the GPU first, got %s", e.Name)
	}
	s.revokeGPUHDR("hevc_vaapi", "HDR10", "lost its mastering display")
	if e := s.encoderChoice(hdrFilm("HDR10"), "hevc", true); e.Hardware {
		t.Errorf("HDR10 still went to %s after it lost the HDR", e.Name)
	}
	if e := s.encoderChoice(hdrFilm("HLG"), "hevc", true); !e.Hardware {
		t.Errorf("HLG should still go to the GPU, got %s", e.Name)
	}
	if got := strings.Join(s.GetSettings(context.Background()).GPUHDR, ","); got != "HLG" {
		t.Errorf("Settings should now say the GPU takes HLG only, got %q", got)
	}
}

// Routing the forecast follows: an HDR10 film the GPU takes is timed as a GPU encode.
func TestEncoderClassGPUHDR(t *testing.T) {
	s := gpuService(t, true, true)
	if c := s.encoderClass("hevc", "HDR10", "HDR10", false, true); c != classGPUHEVC {
		t.Errorf("HDR10 on a GPU that keeps it: %s", c)
	}
	if c := s.encoderClass("hevc", "Dolby Vision", "HDR10", false, true); c != classCPUHEVC {
		t.Errorf("Dolby Vision stays on the CPU: %s", c)
	}
	if c := s.encoderClass("hevc", "HDR10+", "HDR10+", true, true); c != classCPUHEVCHDR10Plus {
		t.Errorf("HDR10+: %s", c)
	}
	if c := gpuService(t, false, false).encoderClass("hevc", "HDR10", "HDR10", false, true); c != classCPUHEVC {
		t.Errorf("HDR10 on a GPU that failed the test: %s", c)
	}
}

// The command line a hardware encode of an HDR10 film gets, per backend: 10-bit, the same
// BT.2020/PQ tags the CPU writes, and nothing that would strip the frames' metadata.
func TestHardwareHDRCommandLine(t *testing.T) {
	mi := hdrFilm("HDR10")
	mi.ColorPrimaries = "unknown" // the tags come from the HDR kind, not a possibly half-tagged source
	plan := Plan{VideoCodec: "hevc", Quality: 20}
	tags := "-color_primaries bt2020 -color_trc smpte2084 -colorspace bt2020nc"
	for _, c := range []struct {
		enc  Encoder
		want []string
	}{
		{Encoder{Codec: "hevc", Name: "hevc_vaapi", Kind: "vaapi", TenBit: true}, []string{"-vf format=p010,hwupload", "-c:v hevc_vaapi", "-profile:v main10", tags}},
		{Encoder{Codec: "hevc", Name: "hevc_qsv", Kind: "qsv", TenBit: true}, []string{"-c:v hevc_qsv", "-pix_fmt p010le", tags}},
		{Encoder{Codec: "hevc", Name: "hevc_nvenc", Kind: "nvenc", TenBit: true}, []string{"-c:v hevc_nvenc", "-pix_fmt p010le", tags}},
		{Encoder{Codec: "av1", Name: "av1_qsv", Kind: "qsv", TenBit: true}, []string{"-c:v av1_qsv", "-pix_fmt p010le", tags}},
		{Encoder{Codec: "av1", Name: "av1_vaapi", Kind: "vaapi", TenBit: true}, []string{"-vf format=p010,hwupload", "-c:v av1_vaapi", tags}},
	} {
		p := plan
		p.VideoCodec = c.enc.Codec
		got := joined(videoArgs(c.enc, mi, p, false, 4, false))
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in %s", c.enc.Name, w, got)
			}
		}
		if strings.Contains(got, "-color_primaries unknown") || strings.Contains(got, "x265-params") {
			t.Errorf("%s: %s", c.enc.Name, got)
		}
	}
	hlg := joined(videoArgs(Encoder{Codec: "hevc", Name: "hevc_qsv", Kind: "qsv", TenBit: true}, hdrFilm("HLG"), plan, false, 4, false))
	if !strings.Contains(hlg, "-color_trc arib-std-b67") {
		t.Errorf("HLG keeps its own curve on the GPU: %s", hlg)
	}
	// SDR keeps the source's own tags, as before.
	sdr := film("h264", 1920, 1080, 12000)
	sdr.ColorPrimaries, sdr.ColorTransfer, sdr.ColorSpace = "bt709", "bt709", "bt709"
	if got := joined(videoArgs(Encoder{Name: "hevc_qsv", Kind: "qsv"}, sdr, plan, false, 4, false)); !strings.Contains(got, "-color_primaries bt709") {
		t.Errorf("SDR tags: %s", got)
	}
}

// What counts as having kept the HDR, and what doesn't.
func TestHDRLost(t *testing.T) {
	src := hdrFilm("HDR10")
	good := func() *MediaInfo {
		o := hdrFilm("HDR10")
		// AV1 stores the mastering display in other fractions: rounding, not loss.
		o.HDR10 = &HDR10Meta{MasterDisplay: "G(13250,34501)B(7499,3000)R(34000,16000)WP(15635,16450)L(9999961,50)", MaxCLL: "1000,400"}
		return o
	}
	if why := hdrLost(src, good()); why != "" {
		t.Fatalf("a faithful encode was refused: %s", why)
	}
	cases := map[string]func(o *MediaInfo){
		"came out SDR":         func(o *MediaInfo) { o.HDR, o.ColorTransfer = "SDR", "bt709" },
		"came out HLG":         func(o *MediaInfo) { o.HDR = "HLG" },
		"no mastering display": func(o *MediaInfo) { o.HDR10.MasterDisplay = "" },
		"no metadata at all":   func(o *MediaInfo) { o.HDR10 = nil },
		"wrong peak brightness": func(o *MediaInfo) {
			o.HDR10.MasterDisplay = strings.Replace(probeMasterDisplay, "10000000", "40000000", 1)
		},
		"no light level":        func(o *MediaInfo) { o.HDR10.MaxCLL = "" },
		"different light level": func(o *MediaInfo) { o.HDR10.MaxCLL = "1000,0" },
		"untagged primaries":    func(o *MediaInfo) { o.ColorPrimaries = "" },
		"BT.709 matrix":         func(o *MediaInfo) { o.ColorSpace = "bt709" },
		"swapped green and red": func(o *MediaInfo) {
			o.HDR10.MasterDisplay = "G(34000,16000)B(7500,3000)R(13250,34500)WP(15635,16450)L(10000000,50)"
		},
	}
	for name, mutate := range cases {
		o := good()
		mutate(o)
		if why := hdrLost(src, o); why == "" {
			t.Errorf("%s: accepted", name)
		}
	}
	// HDR10 may come out HDR10+ (the pipeline found what the label missed); never the reverse.
	o := good()
	o.HDR = "HDR10+"
	if why := hdrLost(src, o); why != "" {
		t.Errorf("HDR10 → HDR10+ refused: %s", why)
	}
	if why := hdrLost(hdrFilm("HDR10+"), good()); why == "" {
		t.Error("HDR10+ → HDR10 accepted")
	}
	// HLG carries no mastering data; Dolby Vision dropped keeps its HDR10 base's.
	if why := hdrLost(hdrFilm("HLG"), hdrFilm("HLG")); why != "" {
		t.Errorf("HLG: %s", why)
	}
	if why := hdrLost(hdrFilm("Dolby Vision"), good()); why != "" {
		t.Errorf("Dolby Vision → HDR10: %s", why)
	}
	// A source whose light level is unset (0,0) isn't held to one; SDR isn't checked at all.
	zero := hdrFilm("HDR10")
	zero.HDR10.MaxCLL = "0,0"
	o = good()
	o.HDR10.MaxCLL = ""
	if why := hdrLost(zero, o); why != "" {
		t.Errorf("unset MaxCLL: %s", why)
	}
	// A PQ source that never tagged its primaries isn't held to BT.2020.
	untagged := hdrFilm("HDR10")
	untagged.ColorPrimaries = "unknown"
	o = good()
	o.ColorPrimaries = "unknown"
	if why := hdrLost(untagged, o); why != "" {
		t.Errorf("untagged source: %s", why)
	}
	if why := hdrLost(film("h264", 1920, 1080, 12000), &MediaInfo{}); why != "" {
		t.Errorf("SDR: %s", why)
	}
}

// The check before the swap refuses a re-encode that lost its HDR — any encoder's.
func TestVerifyOutputRefusesLostHDR(t *testing.T) {
	src := hdrFilm("HDR10")
	plan := Plan{VideoCodec: "hevc"}
	out := hdrFilm("HDR10")
	out.HDR10.MasterDisplay = ""
	if _, reason := verifyOutput(src, out, src.SizeBytes/2, plan); !strings.Contains(reason, "mastering-display") {
		t.Errorf("an output without its mastering display passed: %q", reason)
	}
	if _, reason := verifyOutput(src, hdrFilm("HDR10"), src.SizeBytes/2, plan); reason != "" {
		t.Errorf("a faithful HDR10 output was refused: %s", reason)
	}
	// A tracks-only tidy copies the video: nothing to check.
	if _, reason := verifyOutput(src, out, src.SizeBytes, Plan{}); reason != "" {
		t.Errorf("a copy was judged on its HDR: %s", reason)
	}
}

// A GPU conversion that lost the HDR never replaces the original: the job fails (to be tried
// again on the CPU), the original is untouched, and the GPU stops getting that kind of HDR.
func TestFinalizeRefusesGPUOutputWithoutHDR(t *testing.T) {
	r, src, dst, _, plan := finalizeRig(t)
	r.encoders = gpuService(t, true, true).encoders
	mi := hdrFilm("HDR10")
	mi.SizeBytes, mi.Audio, mi.AudioTracks = 1000, []AudioStream{aud("truehd", "eng", 8)}, 1
	out := hdrFilm("HDR10")
	out.HDR10 = nil // the mastering display and light level didn't make it
	out.DurationSec, out.AudioTracks, out.Audio = mi.DurationSec, len(keptAudio(mi, plan)), nil
	stubProbe(t, out)
	it, _ := parseKey(movieKey(1))
	job := r.claim(it, "HDR film", true)
	r.update(job, func(j *Job) { j.encKind, j.Encoder = "vaapi", "VAAPI (HEVC)" })
	r.finalizeOutput(context.Background(), job, src, dst, mi, plan)
	if job.State != StateFailed || !strings.Contains(job.Note, "on the CPU next time") {
		t.Fatalf("state %s (%s), want failed with a CPU retry", job.State, job.Note)
	}
	if sizeOf(src) != 1000 {
		t.Fatal("the original was touched")
	}
	if e := r.encoderChoice(mi, "hevc", true); e.Hardware {
		t.Errorf("HDR10 still goes to %s", e.Name)
	}
	// The same loss on the CPU is a verdict on the file: no GPU to blame.
	r2, src2, dst2, _, _ := finalizeRig(t)
	stubProbe(t, out)
	job2 := r2.claim(it, "HDR film", true)
	r2.finalizeOutput(context.Background(), job2, src2, dst2, mi, plan)
	if kind, _, _ := r2.skipOf(t, movieKey(1)); job2.State != StateSkipped || kind != SkipHDRUnsupported || sizeOf(src2) != 1000 {
		t.Fatalf("CPU: state %s, skip %q (%s)", job2.State, kind, job2.Note)
	}
}

// ffmpeg 7 puts the mastering data in the Matroska container whatever the encoder did, so a
// GPU output can look like HDR10 from outside while its video stream lacks it. The stream is
// read on its own too, and that refuses it.
func TestFinalizeReadsTheGPUVideoStream(t *testing.T) {
	r, src, dst, _, plan := finalizeRig(t)
	r.encoders = gpuService(t, true, true).encoders
	mi := hdrFilm("HDR10")
	mi.SizeBytes, mi.Audio, mi.AudioTracks = 1000, []AudioStream{aud("truehd", "eng", 8)}, 1
	container := hdrFilm("HDR10")
	container.DurationSec, container.AudioTracks = mi.DurationSec, 1
	bare := hdrFilm("HDR10")
	bare.HDR10 = nil
	old := probeFn
	probeFn = func(_ context.Context, _ string, path string) (*MediaInfo, error) {
		c := *container
		if strings.HasSuffix(path, ".hevc") {
			c = *bare
		}
		return &c, nil
	}
	t.Cleanup(func() { probeFn = old })
	var extracted []string
	oldX := extractVideoFn
	extractVideoFn = func(_ context.Context, _, from, to, format string) error {
		extracted = append(extracted, format+":"+filepath.Base(from))
		return nil
	}
	t.Cleanup(func() { extractVideoFn = oldX })

	it, _ := parseKey(movieKey(1))
	job := r.claim(it, "HDR film", true)
	r.update(job, func(j *Job) { j.encKind, j.Encoder = "vaapi", "VAAPI (HEVC)" })
	r.finalizeOutput(context.Background(), job, src, dst, mi, plan)
	if job.State != StateFailed || !strings.Contains(job.Note, "video stream lost its HDR mastering-display") {
		t.Fatalf("state %s (%s), want failed: the stream lacks the metadata", job.State, job.Note)
	}
	if len(extracted) != 1 || extracted[0] != "hevc:"+filepath.Base(dst) {
		t.Errorf("extracted %v", extracted)
	}
	if sizeOf(src) != 1000 {
		t.Fatal("the original was touched")
	}
	// A stream that can't be read at all counts as lost.
	r2, src2, dst2, _, _ := finalizeRig(t)
	r2.encoders = r.encoders
	probeFn = func(context.Context, string, string) (*MediaInfo, error) { c := *container; return &c, nil }
	extractVideoFn = func(context.Context, string, string, string, string) error { return errors.New("no stream") }
	job2 := r2.claim(it, "HDR film", true)
	r2.update(job2, func(j *Job) { j.encKind = "vaapi" })
	r2.finalizeOutput(context.Background(), job2, src2, dst2, mi, plan)
	if job2.State != StateFailed || !strings.Contains(job2.Note, "couldn't read the converted video stream") {
		t.Fatalf("unreadable stream: state %s (%s)", job2.State, job2.Note)
	}
}

// The startup clip's command and the encoders' HDR flags.
func TestHDRSampleArgs(t *testing.T) {
	x := joined(hdrSampleArgs("libx265", "HDR10", "/tmp/s.mkv"))
	for _, w := range []string{"-pix_fmt yuv420p10le", "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc", "master-display=" + probeMasterDisplay, "max-cll=" + probeMaxCLL, "pools=none", "-color_trc smpte2084"} {
		if !strings.Contains(x, w) {
			t.Errorf("x265 HDR10 clip missing %q: %s", w, x)
		}
	}
	if h := joined(hdrSampleArgs("libx265", "HLG", "/tmp/s.mkv")); !strings.Contains(h, "transfer=arib-std-b67") || strings.Contains(h, "master-display") {
		t.Errorf("HLG clip: %s", h)
	}
	if a := joined(hdrSampleArgs("libsvtav1", "HDR10", "/tmp/s.mkv")); !strings.Contains(a, "mastering-display=G(0.2650,0.6900)") {
		t.Errorf("SVT-AV1 clip: %s", a)
	}
	if got := strings.Join(hdrKinds(Encoder{HDR10: true}), ","); got != "HDR10" {
		t.Errorf("hdrKinds: %s", got)
	}
	if f, ext := rawVideoFormat("av1"); f != "obu" || ext != "obu" {
		t.Errorf("AV1 raw format: %s", f)
	}
}

// Mastering-display values are read in x265's units whatever fractions ffprobe reports them
// in: HEVC's n/50000, AV1's n/65536 and n/256, and the reduced fractions a Matroska file's own
// Colour fields come back as ("53/200", "1000/1") — read as bare numerators, those were nonsense.
func TestRatUnits(t *testing.T) {
	for _, c := range []struct {
		in    string
		scale int64
		want  string
	}{
		{"13250/50000", 50000, "13250"}, {"17367/65536", 50000, "13250"}, {"53/200", 50000, "13250"},
		{"256000/256", 10000, "10000000"}, {"1000/1", 10000, "10000000"}, {"819/16384", 10000, "500"}, {"1/200", 10000, "50"},
		{"50", 10000, "50"}, {"0/0", 50000, "0"}, {"", 50000, "0"},
	} {
		if got := ratUnits(c.in, c.scale); got != c.want {
			t.Errorf("ratUnits(%q, %d) = %s, want %s", c.in, c.scale, got, c.want)
		}
	}
}
