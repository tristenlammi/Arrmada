package convert

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestErrLinesKeepsTheFirstFewReadable(t *testing.T) {
	e := &errLines{max: 2}
	_, _ = e.Write([]byte("[matroska,webm @ 0x563aa65ac240] File ended prematurely\n\n[hevc @ 0x1f] Could not find ref\nthird\n"))
	got := e.lines()
	if len(got) != 2 || got[0] != "[matroska,webm] File ended prematurely" || got[1] != "[hevc] Could not find ref" {
		t.Fatalf("lines = %q", got)
	}
}

func TestExpectedFrames(t *testing.T) {
	plan := Plan{VideoCodec: "hevc"}
	mi := &MediaInfo{DurationSec: 7200, FrameRate: 24}

	// No count in the container: length × rate, not exact.
	if n, exact := expectedFrames(mi, nil, plan); n != 172800 || exact {
		t.Fatalf("estimate = %v exact=%v", n, exact)
	}
	// The video stream's own length beats the file's (an audio track can run on).
	mi.VideoSec = 7190
	if n, _ := expectedFrames(mi, nil, plan); n != 7190*24 {
		t.Fatalf("video length not used: %v", n)
	}
	// A container count that agrees with the length is exact.
	mi.Frames = 172555
	if n, exact := expectedFrames(mi, nil, plan); n != 172555 || !exact {
		t.Fatalf("tag = %v exact=%v", n, exact)
	}
	// A stale count (more than 1% off the length) is ignored.
	mi.Frames = 150000
	if n, exact := expectedFrames(mi, nil, plan); n != 7190*24 || exact {
		t.Fatalf("stale tag trusted: %v exact=%v", n, exact)
	}
	// A count with no rate to check it against is taken as it is.
	if n, exact := expectedFrames(&MediaInfo{Frames: 1234}, nil, plan); n != 1234 || !exact {
		t.Fatalf("lone tag = %v exact=%v", n, exact)
	}
	// A VFR source flattened to CFR is re-timed: judged at the output's rate.
	vfr := &MediaInfo{DurationSec: 100, FrameRate: 23.5, VFR: true, Frames: 2350}
	cfr := Plan{VideoCodec: "hevc", VFRToCFR: true}
	if n, exact := expectedFrames(vfr, &MediaInfo{FrameRate: 25}, cfr); n != 2500 || exact {
		t.Fatalf("VFR→CFR = %v exact=%v", n, exact)
	}
	// A copy keeps the stream as it is, frame for frame.
	if n, _ := expectedFrames(vfr, &MediaInfo{FrameRate: 25}, Plan{VFRToCFR: true}); n != 2350 {
		t.Fatalf("copy = %v", n)
	}
	// A re-encode at the original's rate keeps its exact count; at another rate (an
	// interlaced source whose nominal rate is the field rate, say) it follows the output.
	mi.Frames = 172555
	if n, exact := expectedFrames(mi, &MediaInfo{FrameRate: 24.0001}, plan); n != 172555 || !exact {
		t.Fatalf("same rate = %v exact=%v", n, exact)
	}
	if n, exact := expectedFrames(mi, &MediaInfo{FrameRate: 48}, plan); n != 7190*48 || exact {
		t.Fatalf("other rate = %v exact=%v", n, exact)
	}
	if n, _ := expectedFrames(&MediaInfo{}, nil, plan); n != 0 {
		t.Fatalf("nothing known = %v", n)
	}
}

func TestFramesVerdict(t *testing.T) {
	// Exact: 0.1%.
	if r := framesVerdict(172800, 172800, true, 24); r != "" {
		t.Fatal(r)
	}
	if r := framesVerdict(172700, 172800, true, 24); r != "" {
		t.Fatalf("within 0.1%%: %s", r)
	}
	if r := framesVerdict(172000, 172800, true, 24); !strings.Contains(r, "missing 800 frames") {
		t.Fatalf("missing frames: %q", r)
	}
	if r := framesVerdict(173500, 172800, true, 24); !strings.Contains(r, "700 more frames") {
		t.Fatalf("extra frames: %q", r)
	}
	// Worked out from the length: two seconds' worth too, even on a short file.
	if r := framesVerdict(31640, 31680, false, 24); r != "" {
		t.Fatalf("estimate within 2 s: %s", r)
	}
	if r := framesVerdict(31500, 31680, false, 24); !strings.Contains(r, "~31680") {
		t.Fatalf("estimate miss: %q", r)
	}
	// Tiny files still allow a frame.
	if r := framesVerdict(479, 480, true, 24); r != "" {
		t.Fatalf("one frame on a short clip: %s", r)
	}
}

func TestProbeDurationAndCountParsing(t *testing.T) {
	for in, want := range map[string]float64{
		"7199.958000":        7199.958,
		"01:59:59.958000000": 7199.958,
		"00:00:20.000000000": 20,
		"":                   0,
		"garbage":            0,
		"N/A":                0,
	} {
		if got := firstDuration(in); got < want-1e-6 || got > want+1e-6 {
			t.Errorf("firstDuration(%q) = %v, want %v", in, got, want)
		}
	}
	if got := firstDuration("", "00:01:00.5"); got != 60.5 {
		t.Errorf("fallback = %v", got)
	}
	if got := firstInt("", "N/A", "0", "172800"); got != 172800 {
		t.Errorf("firstInt = %v", got)
	}
}

func TestLibVMAFDetection(t *testing.T) {
	with := " T.. ssim              VV->V      Calculate the SSIM between two video streams.\n" +
		" ... libvmaf           VV->V      Calculate the VMAF between two video streams.\n"
	without := " TS. ssim              VV->V      Calculate the SSIM between two video streams.\n" +
		" ... vmafmotion        V->V       Calculate the VMAF Motion score.\n"
	if !filtersHaveVMAF(with) {
		t.Error("libvmaf listed but not found")
	}
	if filtersHaveVMAF(without) {
		t.Error("vmafmotion is not libvmaf")
	}
	if filtersHaveVMAF("") {
		t.Error("empty output")
	}
}

func TestParseVMAF(t *testing.T) {
	v, err := parseVMAF("[Parsed_libvmaf_6 @ 0x55] VMAF score: 94.512345\n")
	if err != nil || v < 94.51 || v > 94.52 {
		t.Fatalf("got %v, %v", v, err)
	}
	for _, bad := range []string{"", "VMAF score: nan", "VMAF score: 150"} {
		if _, err := parseVMAF(bad); err == nil {
			t.Errorf("%q parsed as a score", bad)
		}
	}
}

// The gate on the swap. Each case finishes a rig's encode; only a clean full decode with
// the original's frame count lets the converted file replace the original.
func TestFullDecodeGatesTheSwap(t *testing.T) {
	for _, tc := range []struct {
		name     string
		res      decodeResult
		err      error
		wantNote string // "" = swapped in
		wantKind string
	}{
		{"clean", decodeResult{frames: filmFrames}, nil, "", ""},
		{"decode errors", decodeResult{frames: filmFrames, errors: []string{"[hevc @ 0x1] Could not find ref with POC 12"}},
			errors.New("exit status 69"), "decode errors", SkipQualityGate},
		{"missing frames", decodeResult{frames: filmFrames - 5000}, nil, "missing 5000 frames", SkipQualityGate},
		// Not the file's fault: tried again later, but never swapped in unchecked.
		{"could not run", decodeResult{}, errors.New("exec: not found"), "couldn't run", SkipTransient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, src, dst, mi, plan := finalizeRig(t)
			stubDecode(t, tc.res, tc.err)
			it, _ := parseKey(movieKey(1))
			job := r.claim(it, "Remux", true)
			r.finalizeOutput(context.Background(), job, src, dst, mi, plan)
			if tc.wantNote == "" {
				if job.State != StateDone {
					t.Fatalf("state %s (%s), want done", job.State, job.Note)
				}
				return
			}
			kind, _, _ := r.skipOf(t, movieKey(1))
			if job.State != StateSkipped || kind != tc.wantKind || !strings.Contains(job.Note, tc.wantNote) ||
				!strings.Contains(job.Note, "kept the original") {
				t.Fatalf("state %s, skip %q, note %q", job.State, kind, job.Note)
			}
			if b, err := os.ReadFile(src); err != nil || len(b) != 1000 {
				t.Fatalf("original touched: %d bytes, %v", len(b), err)
			}
			if _, err := os.Stat(src + ".arrpart"); err == nil {
				t.Fatal("a rejected file was staged into the library")
			}
		})
	}
}

// On a box with VAAPI hardware decoding the first pass runs there; one that doesn't pass
// cleanly is decoded again on the CPU, and only the CPU's verdict counts.
func TestHardwareDecodeFallsBackToCPU(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hwOK    bool
		passes  int
		wantDev []string
	}{
		{"hardware passes", true, 1, []string{"/dev/dri/renderD128"}},
		{"hardware fails, CPU passes", false, 2, []string{"/dev/dri/renderD128", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, src, dst, mi, plan := finalizeRig(t)
			r.encoders = append(r.encoders, Encoder{Codec: "hevc", Name: "hevc_vaapi", Kind: "vaapi", Hardware: true, Available: true})
			var devs []string
			old := decodeFn
			decodeFn = func(_ *Service, _ context.Context, _ *Job, _ string, _ *MediaInfo, dev string) (decodeResult, error) {
				devs = append(devs, dev)
				if dev != "" && !tc.hwOK {
					return decodeResult{errors: []string{"Failed setup for format vaapi: hwaccel initialisation returned error."}}, nil
				}
				return decodeResult{frames: filmFrames}, nil
			}
			t.Cleanup(func() { decodeFn = old })
			it, _ := parseKey(movieKey(1))
			job := r.claim(it, "Remux", true)
			r.finalizeOutput(context.Background(), job, src, dst, mi, plan)
			if job.State != StateDone {
				t.Fatalf("state %s (%s), want done", job.State, job.Note)
			}
			if strings.Join(devs, ",") != strings.Join(tc.wantDev, ",") {
				t.Fatalf("decoded on %q, want %q", devs, tc.wantDev)
			}
		})
	}
}

// A cancel during the check keeps the original and isn't counted as a quality failure.
func TestFullDecodeCancelKeepsOriginal(t *testing.T) {
	r, src, dst, mi, plan := finalizeRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	old := decodeFn
	decodeFn = func(*Service, context.Context, *Job, string, *MediaInfo, string) (decodeResult, error) {
		cancel()
		return decodeResult{}, context.Canceled
	}
	t.Cleanup(func() { decodeFn = old })
	it, _ := parseKey(movieKey(1))
	job := r.claim(it, "Remux", true)
	r.finalizeOutput(ctx, job, src, dst, mi, plan)
	if job.State == StateDone || job.State == StateReplacing {
		t.Fatalf("state %s after a cancel", job.State)
	}
	if kind, _, _ := r.skipOf(t, movieKey(1)); kind != "" {
		t.Fatalf("a cancel recorded a %q skip", kind)
	}
	if b, err := os.ReadFile(src); err != nil || len(b) != 1000 {
		t.Fatalf("original touched: %d bytes, %v", len(b), err)
	}
}

// The ledger keeps the advisory VMAF score.
func TestVMAFRecordedInLedger(t *testing.T) {
	r, src, dst, mi, plan := finalizeRig(t)
	r.vmaf = true
	old := vmafWindowFn
	vmafWindowFn = func(*Service, context.Context, string, string, float64, float64, float64, int, int, string, refLeg) (float64, error) {
		return 95, nil
	}
	t.Cleanup(func() { vmafWindowFn = old })
	// finalizeRig's stand-in output has no picture size; VMAF needs one.
	stubProbe(t, &MediaInfo{VideoCodec: "hevc", Width: 1920, Height: 1080, DurationSec: mi.DurationSec,
		AudioTracks: len(keptAudio(mi, plan)), SubTracks: len(keptSubs(mi, plan))})
	it, _ := parseKey(movieKey(1))
	job := r.claim(it, "Remux", true)
	r.record(job).srcInfo = mi
	r.beginHistory(job)
	r.finalizeOutput(context.Background(), job, src, dst, mi, plan)
	if job.State != StateDone {
		t.Fatalf("state %s (%s)", job.State, job.Note)
	}
	rows, _, err := r.History(context.Background(), HistoryFilter{})
	if err != nil || len(rows) != 1 || rows[0].VMAFMean != 95 {
		t.Fatalf("rows %+v, err %v", rows, err)
	}
}
