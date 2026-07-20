package convert

import (
	"strings"
	"testing"
)

// The safe-mode retry drops the tuning parameters and keeps everything else — the stream
// mapping, the codec, the preset and the quality target all have to survive, or the retry
// would produce a different file rather than the same one encoded more conservatively.
func TestStripTuningParamsKeepsEverythingElse(t *testing.T) {
	args := []string{
		"-y", "-i", "in.mkv",
		"-map", "0:v:0", "-map", "0:a?", "-c:a", "copy", "-map", "0:t?", "-c:t", "copy",
		"-c:v", "libx265", "-preset", "slow", "-crf", "18",
		"-x265-params", "aq-mode=3:psy-rd=2.0",
		"-map_metadata", "0", "out.mkv",
	}
	got := stripTuningParams(args)
	joined := strings.Join(got, " ")

	if strings.Contains(joined, "x265-params") || strings.Contains(joined, "aq-mode") {
		t.Errorf("tuning params should be gone: %s", joined)
	}
	for _, want := range []string{
		"-c:v libx265", "-preset slow", "-crf 18",
		"-map 0:t?", "-c:t copy", "-c:a copy", "-map_metadata 0", "out.mkv",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("safe mode dropped %q, which it must keep: %s", want, joined)
		}
	}
}

// With nothing to strip, the args are unchanged — the caller uses that to tell that the
// failure was something other than the tuning, and skips a pointless identical retry.
func TestStripTuningParamsNoopWhenAbsent(t *testing.T) {
	args := []string{"-i", "in.mkv", "-c:v", "libx265", "-crf", "18", "out.mkv"}
	if got := stripTuningParams(args); len(got) != len(args) {
		t.Errorf("expected no change, got %v", got)
	}
}

// SVT-AV1's params are stripped the same way.
func TestStripTuningParamsHandlesAV1(t *testing.T) {
	args := []string{"-c:v", "libsvtav1", "-preset", "5", "-svtav1-params", "tune=0:lp=8", "out.mkv"}
	joined := strings.Join(stripTuningParams(args), " ")
	if strings.Contains(joined, "svtav1-params") || strings.Contains(joined, "tune=0") {
		t.Errorf("svtav1 params should be gone: %s", joined)
	}
	if !strings.Contains(joined, "-preset 5") {
		t.Errorf("preset must survive: %s", joined)
	}
}

// When NUMA pool binding is denied, x265 must be told not to use pools at all — the
// default behaviour logs a warning per pool and can crash partway into the encode.
func TestNoNumaPoolsAddsPoolsNone(t *testing.T) {
	mi := &MediaInfo{VideoCodec: "h264", Height: 1080}

	blocked := strings.Join(compileOutputArgs(cpuEncoder("hevc"), mi, mkvPlan(), false, 4, true), " ")
	if !strings.Contains(blocked, "pools=none") {
		t.Errorf("expected pools=none when NUMA pools are blocked: %s", blocked)
	}

	// And it must NOT appear otherwise — unpooled costs threading efficiency, so it's
	// only worth paying when the environment actually requires it.
	ok := strings.Join(compileOutputArgs(cpuEncoder("hevc"), mi, mkvPlan(), false, 4, false), " ")
	if strings.Contains(ok, "pools=none") {
		t.Errorf("pools=none should only appear when needed: %s", ok)
	}
}
