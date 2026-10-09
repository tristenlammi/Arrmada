package convert

import (
	"context"
	"errors"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// stubWindows makes the final check score its scenes from scores, in order; a NaN entry
// is a scene that can't be measured.
func stubWindows(t *testing.T, scores []float64) {
	t.Helper()
	stubProbe(t, &MediaInfo{VideoCodec: "hevc", Width: 1920, Height: 1080, DurationSec: 7200})
	old := ssimWindowFn
	i := 0
	ssimWindowFn = func(*Service, context.Context, string, string, float64, float64, float64, int, int, string, string) (float64, error) {
		sc := scores[i%len(scores)]
		i++
		if math.IsNaN(sc) {
			return 0, errors.New("no SSIM score in ffmpeg output")
		}
		return sc, nil
	}
	t.Cleanup(func() { ssimWindowFn = old })
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// The gate fails closed and holds every scene to the floor, not just the average.
func TestSSIMGate(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	// One scene that can't be measured is an error, not a skipped scene.
	stubWindows(t, append(repeat(0.99, 9), math.NaN()))
	if _, err := s.computeSSIM(ctx, "out.mkv", "src.mkv", ""); err == nil || !strings.Contains(err.Error(), "couldn't be measured") {
		t.Fatalf("an unmeasurable scene must fail the check: %v", err)
	}

	// A good average can't hide one bad scene.
	scores := append(repeat(0.99, 9), 0.95)
	stubWindows(t, scores)
	res, err := s.computeSSIM(ctx, "out.mkv", "src.mkv", "")
	if err != nil || len(res.Scores) != 10 {
		t.Fatalf("measured %v, err %v", res.Scores, err)
	}
	if res.Mean < minSSIMMean {
		t.Fatalf("the mean %.4f should pass on its own for this case", res.Mean)
	}
	if res.passes() {
		t.Fatal("a scene below the floor must fail the check even when the mean passes")
	}
	last := verifyWindows(7200)[9]
	if why := res.shortfall(); !strings.Contains(why, "scene at "+clock(last.start)) || !strings.Contains(why, "0.950") {
		t.Fatalf("the reason should name the failing scene: %q", why)
	}

	stubWindows(t, repeat(0.985, 10))
	if res, err := s.computeSSIM(ctx, "out.mkv", "src.mkv", ""); err != nil || !res.passes() {
		t.Fatalf("0.985 everywhere must pass: %+v, %v", res, err)
	}

	// A low average fails even with every scene above the floor.
	stubWindows(t, repeat(0.965, 10))
	if res, _ := s.computeSSIM(ctx, "out.mkv", "src.mkv", ""); res.passes() || !strings.Contains(res.shortfall(), "averaged") {
		t.Fatalf("a mean under the bar must fail: %q", res.shortfall())
	}
	if (ssimResult{}).passes() {
		t.Fatal("nothing measured is not a pass")
	}
}

// The final check never re-measures the clips the test encode tuned the target on.
func TestVerifyWindowsAvoidPreflightClips(t *testing.T) {
	for _, vf := range verifyFractions {
		for _, pw := range ssimWindows(10000) {
			if pf := pw.start / 10000; math.Abs(vf-pf) < 0.02 {
				t.Errorf("verify fraction %.2f is within 2%% of preflight's %.2f", vf, pf)
			}
		}
	}
	// At every length preflight runs, no checked scene overlaps a test clip.
	for _, dur := range []float64{preflightMinDuration, 3600, 7200, 4 * 3600} {
		v := verifyWindows(dur)
		if len(v) != 10 {
			t.Fatalf("%.0fs: %d scenes, want 10", dur, len(v))
		}
		for _, a := range v {
			if a.start < 0 || a.start+a.dur > dur {
				t.Errorf("%.0fs: scene %+v runs past the film", dur, a)
			}
			for _, b := range ssimWindows(dur) {
				if a.start < b.start+b.dur && b.start < a.start+a.dur {
					t.Errorf("%.0fs: checked scene %+v overlaps test clip %+v", dur, a, b)
				}
			}
		}
	}
	if w := verifyWindows(15); len(w) != 1 || w[0].dur != 15 {
		t.Fatalf("a short file is measured whole: %+v", w)
	}
}

func TestParseSSIMRejectsNonScores(t *testing.T) {
	for _, out := range []string{"SSIM Y:nan All:nan (nan)", "All:inf", "All:1.5 (x)", "no score here"} {
		if _, err := parseSSIM(out); err == nil {
			t.Errorf("%q parsed as a score", out)
		}
	}
	if v, err := parseSSIM("[Parsed_ssim_0] SSIM Y:0.99 All:0.987654 (19.08)"); err != nil || v != 0.987654 {
		t.Fatalf("parsed %v, %v", v, err)
	}
}

// End to end on synthetic media: an output identical to its source except for one damaged
// scene fails the check, and that scene is the one named. Needs ffmpeg on the PATH.
func TestSSIMGateCatchesOneBadScene(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	dir := t.TempDir()
	ref, bad := filepath.Join(dir, "ref.mkv"), filepath.Join(dir, "bad.mkv")
	ff := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("ffmpeg", append([]string{"-y", "-hide_banner", "-loglevel", "error"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
		}
	}
	const dur = 120.0
	ff("-f", "lavfi", "-i", "testsrc2=s=320x180:r=12:d=120", "-c:v", "ffv1", ref)
	// Heavy noise over the scene the check looks at 45% of the way in, and nowhere else.
	from := verifyWindows(dur)[4].start
	ff("-i", ref, "-vf", "noise=alls=80:allf=t:enable='between(t,"+ftoa(from-1)+","+ftoa(from+verifyWindow+1)+")'", "-c:v", "ffv1", bad)

	s := newTestService(t)
	s.ffmpeg, s.ffprobe = "ffmpeg", "ffprobe"
	ctx := context.Background()
	good, err := s.computeSSIM(ctx, ref, ref, "")
	if err != nil || !good.passes() {
		t.Fatalf("an identical file must pass: %+v, %v", good, err)
	}
	res, err := s.computeSSIM(ctx, bad, ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.passes() {
		t.Fatalf("one damaged scene must fail the check: %s", res.summary())
	}
	if w := res.worst(); w != 4 || res.Scores[w] >= minSSIMWindow {
		t.Fatalf("the damaged scene should be the worst and below the floor: %s", res.summary())
	}
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }
