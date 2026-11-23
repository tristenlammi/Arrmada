package convert

import (
	"context"
	"math"
	"strings"
	"testing"
)

// The quality check prepares the source the way the encode did: an interlaced source is
// deinterlaced with the encode's own filter (then cropped), and the film's own video stream
// is compared, not cover art that comes first. A progressive source is untouched.
func TestReferenceLegMatchesTheEncode(t *testing.T) {
	mi := film("mpeg2video", 720, 576, 8000)
	mi.Interlaced, mi.VideoIndex, mi.FrameRateRat = true, 1, "25/1"
	crop := &Crop{W: 720, H: 432, X: 0, Y: 72}
	plan := Plan{VideoCodec: "hevc", Crop: crop}
	lavfi := argValue(pairedArgs("out.mkv", "src.mkv", 0, 60, 10, 720, 432, "25/1", refLegOf(mi, plan), "ssim"), "-lavfi")
	if !strings.Contains(lavfi, "[1:v:1]"+deintFilter+","+crop.filter()+",scale=720:432") {
		t.Errorf("interlaced reference: %s", lavfi)
	}
	if !strings.HasPrefix(lavfi, "[0:v:0]") {
		t.Errorf("the converted file has one video stream: %s", lavfi)
	}
	// The encode's chain is the same filter, in the same order.
	if got := swFilterChain(mi, plan); got != deintFilter+","+crop.filter() {
		t.Errorf("encode chain = %q", got)
	}

	prog := film("h264", 1920, 1080, 12000)
	lavfi = argValue(pairedArgs("out.mkv", "src.mkv", 0, 0, 10, 1920, 1080, "", refLegOf(prog, Plan{VideoCodec: "hevc"}), "ssim"), "-lavfi")
	if strings.Contains(lavfi, "bwdif") || !strings.Contains(lavfi, "[1:v:0]scale=1920:1080") {
		t.Errorf("progressive reference must be untouched: %s", lavfi)
	}
}

// The HDR10+ read takes the film's own video stream, not cover art in front of it.
func TestHDR10PlusReadsTheRealVideoStream(t *testing.T) {
	args := hdr10PlusExtractArgs("/m/film.mp4", 1)
	if argValue(args, "-map") != "0:v:1" {
		t.Errorf("map = %q, want 0:v:1: %v", argValue(args, "-map"), args)
	}
	if argValue(hdr10PlusExtractArgs("/m/film.mkv", 0), "-map") != "0:v:0" {
		t.Error("a file with no cover art reads stream 0")
	}
}

// A copied audio track that came out shorter than the original's fails the check before the
// swap; one within the container's tolerance, or one whose length isn't stated, passes.
func TestCopiedAudioLengthIsChecked(t *testing.T) {
	src := film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6), aud("aac", "fre", 2))
	src.Container = "MKV"
	src.Audio[0].Sec, src.Audio[1].Sec = 7200, 7199.5
	out := &MediaInfo{VideoCodec: "hevc", DurationSec: 7200, AudioTracks: 2,
		Audio: []AudioStream{{Codec: "ac3", Sec: 7199.2}, {Codec: "aac", Sec: 7199.5}}}
	plan := Plan{VideoCodec: "hevc"}
	small := src.SizeBytes / 2
	if _, why := verifyOutput(src, out, small, plan); why != "" {
		t.Fatalf("a full-length copy failed: %s", why)
	}
	out.Audio[1].Sec = 3600
	if _, why := verifyOutput(src, out, small, plan); !strings.Contains(why, "audio track 2") || !strings.Contains(why, "1:00:00") {
		t.Errorf("a truncated copy passed: %q", why)
	}
	// Unknown lengths aren't judged.
	out.Audio[1].Sec = 0
	if _, why := verifyOutput(src, out, small, plan); why != "" {
		t.Errorf("an unstated length must not fail: %s", why)
	}
	// AVI states a guess from the bitrate, not a length: not judged.
	out.Audio[1].Sec = 3600
	src.Container = "AVI"
	if _, why := verifyOutput(src, out, small, plan); why != "" {
		t.Errorf("an AVI source's estimate must not fail a conversion: %s", why)
	}
	// Tracks are matched by position among the kept ones: a dropped commentary track
	// doesn't shift the comparison.
	src.Container = "MKV"
	comm := aud("ac3", "eng", 2)
	comm.Commentary, comm.Sec = true, 1200
	withComm := film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6), comm)
	withComm.Container = "MKV"
	withComm.Audio[0].Sec = 7200
	one := &MediaInfo{VideoCodec: "hevc", DurationSec: 7200, AudioTracks: 1, Audio: []AudioStream{{Codec: "ac3", Sec: 7200}}}
	if _, why := verifyOutput(withComm, one, small, Plan{VideoCodec: "hevc", Audio: AudioPlan{DropCommentary: true}}); why != "" {
		t.Errorf("kept tracks compared by position: %s", why)
	}
}

// "Always 10-bit" reaches the GPU when the encoder proved it can write 10-bit; one that
// can't keeps an 8-bit source 8-bit (as before), and "match the source" keeps it 8-bit.
func TestHardwareTenBit(t *testing.T) {
	sdr := film("h264", 1920, 1080, 12000)
	always := Plan{VideoCodec: "hevc", Quality: 20}
	match := Plan{VideoCodec: "hevc", Quality: 20, MatchSourceDepth: true}

	vaapi := Encoder{Codec: "hevc", Name: "hevc_vaapi", Kind: "vaapi", Hardware: true, TenBit: true}
	a := joined(videoArgs(vaapi, sdr, always, false, 4, false))
	if !strings.Contains(a, "format=p010,hwupload") || !strings.Contains(a, "-profile:v main10") {
		t.Errorf("VAAPI HEVC, always 10-bit: %s", a)
	}
	a = joined(videoArgs(vaapi, sdr, always, true, 4, false))
	if !strings.Contains(a, "-vf scale_vaapi=format=p010") || !strings.Contains(a, "-profile:v main10") {
		t.Errorf("VAAPI HEVC from GPU-decoded frames: %s", a)
	}
	a = joined(videoArgs(vaapi, sdr, match, false, 4, false))
	if !strings.Contains(a, "format=nv12,hwupload") || strings.Contains(a, "main10") {
		t.Errorf("VAAPI, match the source: %s", a)
	}
	old := vaapi
	old.TenBit = false
	a = joined(videoArgs(old, sdr, always, false, 4, false))
	if !strings.Contains(a, "format=nv12,hwupload") || strings.Contains(a, "main10") {
		t.Errorf("a GPU without 10-bit must stay 8-bit rather than fail: %s", a)
	}
	// A 10-bit source is 10-bit on any encoder, as before.
	ten := film("hevc", 3840, 2160, 60000)
	ten.TenBit = true
	if a := joined(videoArgs(old, ten, match, false, 4, false)); !strings.Contains(a, "format=p010,hwupload") || !strings.Contains(a, "main10") {
		t.Errorf("10-bit source: %s", a)
	}

	av1 := Encoder{Codec: "av1", Name: "av1_vaapi", Kind: "vaapi", Hardware: true, TenBit: true}
	a = joined(videoArgs(av1, sdr, Plan{VideoCodec: "av1", Quality: 24}, false, 4, false))
	if !strings.Contains(a, "format=p010,hwupload") || strings.Contains(a, "-profile:v") {
		t.Errorf("av1_vaapi takes 10-bit frames and no main10 profile: %s", a)
	}
	for _, kind := range []string{"nvenc", "qsv"} {
		enc := Encoder{Codec: "hevc", Name: "hevc_" + kind, Kind: kind, Hardware: true, TenBit: true}
		if a := joined(videoArgs(enc, sdr, always, false, 4, false)); !strings.Contains(a, "-pix_fmt p010le") {
			t.Errorf("%s, always 10-bit: %s", kind, a)
		}
		if a := joined(videoArgs(enc, sdr, match, false, 4, false)); strings.Contains(a, "p010") {
			t.Errorf("%s, match the source: %s", kind, a)
		}
		enc.TenBit = false
		if a := joined(videoArgs(enc, sdr, always, false, 4, false)); strings.Contains(a, "p010") {
			t.Errorf("%s without 10-bit: %s", kind, a)
		}
	}
	if outputBits(sdr, always, vaapi) != 10 || outputBits(sdr, always, old) != 8 {
		t.Error("the plan card must say what the GPU will write")
	}
}

// A skip or a failure block is about a file: a different file at the same title (an
// upgrade, a remux) is judged afresh, while the file it was about stays skipped.
func TestVerdictsFollowTheFile(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	mi := film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6))
	indexMovie(t, s, 1, "Heat", mi)
	key := movieKey(1)

	s.skips.record(ctx, key, SkipNotSmaller, "only 12% smaller")
	for i := 0; i < maxFailures; i++ {
		s.failures.recordFailure(ctx, movieKey(1), "boom")
	}
	if !s.skips.waitingKeys(ctx)[key] || !s.skips.permanentKeys(ctx)[key] || !s.failures.blockedKeys(ctx, maxFailures)[key] {
		t.Fatal("the skipped and blocked file must be held back")
	}
	if l, _ := s.skips.list(ctx); len(l) != 1 {
		t.Fatalf("Problems lists %d skips, want 1", len(l))
	}

	// The upgrader brings in a bigger file at the same path: judged afresh.
	remux := film("h264", 1920, 1080, 40000, aud("truehd", "eng", 8))
	indexMovie(t, s, 1, "Heat", remux)
	if s.skips.waitingKeys(ctx)[key] || s.skips.permanentKeys(ctx)[key] || s.failures.blockedKeys(ctx, maxFailures)[key] {
		t.Error("a replaced file must not inherit the old file's skip or failures")
	}
	if l, _ := s.skips.list(ctx); len(l) != 0 {
		t.Errorf("Problems still lists the old file's skip: %+v", l)
	}
	// Its failures count from one.
	if n := s.failures.recordFailure(ctx, key, "boom"); n != 1 {
		t.Errorf("the new file's first failure counted as %d", n)
	}

	// Back to the very file the verdict was about: it holds again.
	indexMovie(t, s, 1, "Heat", film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6)))
	if !s.skips.waitingKeys(ctx)[key] {
		t.Error("the skip must hold for the file it was about")
	}

	// A reverted skip is title-wide: the revert itself swaps the file.
	s.skips.record(ctx, movieKey(2), SkipReverted, "you reverted it")
	indexMovie(t, s, 2, "Other", film("h264", 1920, 1080, 30000))
	if !s.skips.permanentKeys(ctx)[movieKey(2)] {
		t.Error("a reverted skip must hold whatever file the title has")
	}
	// A row written before 0355 (no file) still holds.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO convert_skips (item_key, kind, reason, permanent) VALUES ('movie:3', 'quality_gate', 'x', 1)`); err != nil {
		t.Fatal(err)
	}
	if !s.skips.permanentKeys(ctx)[movieKey(3)] {
		t.Error("a skip with no file recorded must hold as before")
	}
}

// A GPU's "not smaller" or "quality" verdict holds only while the GPU is in use; the CPU's
// holds either way (a GPU would only make the file bigger).
func TestGPUVerdictDoesNotBlockTheCPU(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	indexMovie(t, s, 1, "GPU", film("h264", 1920, 1080, 12000))
	indexMovie(t, s, 2, "CPU", film("h264", 1920, 1080, 12000))
	s.skips.recordEnc(ctx, movieKey(1), SkipNotSmaller, "x", 0, "vaapi")
	s.skips.recordEnc(ctx, movieKey(2), SkipNotSmaller, "x", 0, "cpu")
	set(t, s, map[string]string{keyUseGPU: "true"})
	if p := s.skips.permanentKeys(ctx); !p[movieKey(1)] || !p[movieKey(2)] {
		t.Errorf("GPU on: both verdicts hold: %v", p)
	}
	set(t, s, map[string]string{keyUseGPU: "false"})
	if p := s.skips.permanentKeys(ctx); p[movieKey(1)] || !p[movieKey(2)] {
		t.Errorf("GPU off: only the CPU's verdict holds: %v", p)
	}
	// A skip that isn't about an encoder's output ignores the encoder.
	s.skips.recordEnc(ctx, movieKey(1), SkipHDRUnsupported, "x", 0, "vaapi")
	if !s.skips.permanentKeys(ctx)[movieKey(1)] {
		t.Error("an HDR skip has nothing to do with the encoder")
	}
}

// A rehearsal miss is only final when it's clear; one just short of the rule is tested
// again on other scenes, and given up on after borderlineTests samples in a row.
func TestRehearsalMarginOnNotSmaller(t *testing.T) {
	const size int64 = 100 << 30
	limit := size * (100 - minSavingPct) / 100
	if k, _ := sizeVerdict(size, size*70/100, limit, 0); k != "" {
		t.Errorf("30%% smaller: %q", k)
	}
	if k, why := sizeVerdict(size, size*85/100, limit, 0); k != SkipBorderline || !strings.Contains(why, "testing again") {
		t.Errorf("15%% smaller, first sample: %q %q", k, why)
	}
	if k, _ := sizeVerdict(size, size*85/100, limit, borderlineTests-2); k != SkipBorderline {
		t.Errorf("15%% smaller, second sample: %q", k)
	}
	if k, why := sizeVerdict(size, size*85/100, limit, borderlineTests-1); k != SkipNotSmaller || !strings.Contains(why, "different scenes") {
		t.Errorf("15%% smaller, last sample: %q %q", k, why)
	}
	if k, _ := sizeVerdict(size, size*95/100, limit, 0); k != SkipNotSmaller {
		t.Errorf("5%% smaller is a clear miss at once: %q", k)
	}
	if k, _ := sizeVerdict(size, size*95/100, 0, 0); k != "" {
		t.Error("no cap, no verdict")
	}
	if permanentSkip(SkipBorderline) || retryDelay(SkipBorderline, 1) != borderlineRetest {
		t.Error("borderline waits and comes back")
	}

	// Each retest samples other scenes, all clear of the final check's.
	seen := map[float64]int{}
	for set := range clipSets {
		for _, w := range ssimWindowsAt(10000, set) {
			seen[w.start]++
			for _, vf := range verifyFractions {
				if math.Abs(vf-w.start/10000) < 0.04-1e-9 {
					t.Errorf("sample %d clip at %.3f is within 4%% of the final check's %.2f", set, w.start/10000, vf)
				}
			}
		}
	}
	for start, n := range seen {
		if n > 1 {
			t.Errorf("a retest repeats the clip at %.0fs", start)
		}
	}
	if len(ssimWindowsAt(10000, 4)) != 4 {
		t.Error("the samples wrap round")
	}
	for _, dur := range []float64{preflightMinDuration, 7200} {
		for set := range clipSets {
			for _, a := range verifyWindows(dur) {
				for _, b := range ssimWindowsAt(dur, set) {
					if a.start < b.start+b.dur && b.start < a.start+a.dur {
						t.Errorf("%.0fs sample %d: checked scene %+v overlaps clip %+v", dur, set, a, b)
					}
				}
			}
		}
	}
}

// The borderline count follows the file and the kind: it's what preflight reads to pick
// the next sample.
func TestBorderlineAttempts(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	indexMovie(t, s, 1, "Close", film("h264", 1920, 1080, 12000))
	key := movieKey(1)
	if n := s.skips.attemptsOf(ctx, key, SkipBorderline); n != 0 {
		t.Fatalf("no skip yet: %d", n)
	}
	s.skips.recordEnc(ctx, key, SkipBorderline, "x", 0, "cpu")
	s.skips.recordEnc(ctx, key, SkipBorderline, "x", 0, "cpu")
	if n := s.skips.attemptsOf(ctx, key, SkipBorderline); n != 2 {
		t.Errorf("two borderline samples: %d", n)
	}
	if !s.skips.waitingKeys(ctx)[key] || s.skips.permanentKeys(ctx)[key] {
		t.Error("a borderline file waits, and isn't given up on")
	}
	indexMovie(t, s, 1, "Close", film("h264", 1920, 1080, 20000))
	if n := s.skips.attemptsOf(ctx, key, SkipBorderline); n != 0 {
		t.Errorf("a replaced file starts again: %d", n)
	}
}
