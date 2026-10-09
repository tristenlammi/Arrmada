package convert

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// This file holds the C4 quality gate: after a transcode, measure how close the output is to the
// source and re-encode at a higher quality if it falls short. We use SSIM — the bundled ffmpeg
// isn't built with libvmaf, so VMAF isn't available; SSIM is a solid always-present proxy (≈0.98+
// is visually near-transparent). If a libvmaf build is ever bundled, swap the metric here.

// ssimResult is what the final check measured: each scene it looked at and that scene's score,
// in order, and their mean.
type ssimResult struct {
	Windows []ssimWnd
	Scores  []float64
	Mean    float64
}

// worst is the index of the lowest-scoring scene (-1 when nothing was measured).
func (r ssimResult) worst() int {
	w := -1
	for i, sc := range r.Scores {
		if w < 0 || sc < r.Scores[w] {
			w = i
		}
	}
	return w
}

// passes reports whether an encode clears the bar: the average AND every single scene. The
// average alone hid a bad scene behind nine good ones.
func (r ssimResult) passes() bool {
	w := r.worst()
	return w >= 0 && r.Mean >= minSSIMMean && r.Scores[w] >= minSSIMWindow
}

// shortfall says in words why a result didn't pass, naming the scene when one fell short.
func (r ssimResult) shortfall() string {
	w := r.worst()
	switch {
	case w < 0:
		return "nothing could be measured"
	case r.Scores[w] < minSSIMWindow:
		return fmt.Sprintf("the scene at %s scored %.3f (no scene may score below %.2f)", clock(r.Windows[w].start), r.Scores[w], minSSIMWindow)
	case r.Mean < minSSIMMean:
		return fmt.Sprintf("SSIM averaged %.4f (the bar is %.2f)", r.Mean, minSSIMMean)
	}
	return "it passed"
}

// summary is the per-scene record for the activity log.
func (r ssimResult) summary() string {
	parts := make([]string, len(r.Scores))
	for i, sc := range r.Scores {
		parts[i] = fmt.Sprintf("%s %.4f", clock(r.Windows[i].start), sc)
	}
	low := ""
	if w := r.worst(); w >= 0 {
		low = fmt.Sprintf(", lowest %.4f", r.Scores[w])
	}
	return fmt.Sprintf("SSIM average %.4f%s · scenes: %s", r.Mean, low, strings.Join(parts, ", "))
}

// ssimWindowFn scores one window; a seam so the gate can be tested without ffmpeg.
var ssimWindowFn = (*Service).ssimWindow

// computeSSIM measures the structural similarity of the encoded output against the source. The
// reference is scaled to the output's resolution first, so a deliberate downscale is judged on
// how well the encode preserved the (downscaled) picture, not penalised for the resize.
//
// Rather than decode the whole film, it scores ten short scenes spread through it (see
// verifyWindows). A full-file SSIM decodes both the output and the source end-to-end on the CPU,
// which pegged every core for the length of a feature (starving VMs and everything else on the
// box). Ten slices, each judged on its own as well as on average, make a pass/fail gate at a
// small fraction of the cost.
//
// FAIL CLOSED: every scene must be measured. A scene that can't be (ffmpeg fails, prints no
// score, or prints nonsense) is an error, never skipped — skipping them once let a single
// readable window pass a whole film.
//
// Decode is on the CPU: it's the reliable path (hardware-decoded SSIM could stall the pipeline on
// some GPUs). The caller wraps this in a timeout so a slow/hung verify can never block a job.
//
// crop is the black-bar crop the encode applied (or ""): the reference gets the same crop,
// so the picture is compared with the picture rather than squashed to the output's size.
func (s *Service) computeSSIM(ctx context.Context, distorted, reference, crop string) (ssimResult, error) {
	di, err := probeFn(ctx, s.ffprobe, distorted)
	if err != nil {
		return ssimResult{}, err
	}
	if di.Width <= 0 || di.Height <= 0 {
		return ssimResult{}, fmt.Errorf("could not read output resolution")
	}
	var res ssimResult
	var sum float64
	for _, wnd := range verifyWindows(di.DurationSec) {
		sc, err := ssimWindowFn(s, ctx, distorted, reference, wnd.start, wnd.start, wnd.dur, di.Width, di.Height, di.FrameRateRat, crop)
		if err != nil {
			return ssimResult{}, fmt.Errorf("the scene at %s couldn't be measured: %w", clock(wnd.start), err)
		}
		res.Windows = append(res.Windows, wnd)
		res.Scores = append(res.Scores, sc)
		sum += sc
	}
	if len(res.Scores) == 0 {
		return ssimResult{}, fmt.Errorf("no scene to measure")
	}
	res.Mean = sum / float64(len(res.Scores))
	return res, nil
}

// ssimWnd is one sample window (seconds).
type ssimWnd struct{ start, dur float64 }

// verifyFractions are where the final check looks, as fractions of the runtime: ten scenes
// across the film, clear of the opening logos and the end credits. Each is at least 4% of the
// runtime away from the clips the test encode tuned the quality target on (ssimWindows), so
// the check judges scenes the target wasn't chosen to pass — at preflight's 20-minute minimum
// that's 48 s, more than a clip's length, so the two never overlap.
var verifyFractions = []float64{0.06, 0.11, 0.21, 0.28, 0.45, 0.52, 0.67, 0.73, 0.79, 0.90}

// verifyWindow is how long each checked scene is, in seconds.
const verifyWindow = 10.0

// verifyWindows is the scenes the final check scores. Short files are measured whole.
func verifyWindows(dur float64) []ssimWnd {
	if dur <= 0 { // unknown length: measure from the start to the end (dur 0 = whole slice)
		return []ssimWnd{{0, 0}}
	}
	if dur <= 2*verifyWindow {
		return []ssimWnd{{0, dur}}
	}
	out := make([]ssimWnd, 0, len(verifyFractions))
	for _, f := range verifyFractions {
		start := dur * f
		if start+verifyWindow > dur {
			start = dur - verifyWindow
		}
		out = append(out, ssimWnd{start, verifyWindow})
	}
	return out
}

// ssimWindows picks the test encode's clips across a runtime, skipping the very start/end
// (logos, credits) where content isn't typical. Short files are measured in a single pass. The
// final check deliberately looks elsewhere (verifyWindows).
func ssimWindows(dur float64) []ssimWnd {
	const win = 15.0
	if dur <= 0 { // unknown length: measure from the start to the end (dur 0 = whole slice)
		return []ssimWnd{{0, 0}}
	}
	if dur <= 2*win {
		return []ssimWnd{{0, dur}}
	}
	fracs := []float64{0.15, 0.38, 0.61, 0.84} // four windows within the middle ~80%
	out := make([]ssimWnd, 0, len(fracs))
	for _, f := range fracs {
		start := dur * f
		if start+win > dur {
			start = dur - win
		}
		out = append(out, ssimWnd{start, win})
	}
	return out
}

// ssimWindow scores one sample window. Both files are input-seeked to the same timestamp (fast,
// keyframe-accurate) and each slice's PTS is reset to zero so the ssim frame-sync pairs them from
// the same point — this removes any constant PTS offset the encoder introduced (an edit-list /
// encoder delay would otherwise misalign every frame by one and cap the score well below the truth,
// regardless of how high the encode quality is). The reference is scaled to the output resolution
// first so an intentional downscale isn't scored as a defect. ssim prints its "All:" summary to
// stderr; exit status is ignored — we rely on parsing that score.
//
// dStart and rStart are where each file's window begins — the same point for a full encode,
// but 0 vs the clip's position for a test clip cut from the middle of the original.
//
// Frames are paired by POSITION, not timestamp. Both streams are re-timed onto the output's
// exact frame rate and renumbered, so frame N is compared with frame N. Pairing by timestamp
// broke on any file whose video doesn't start at zero (an audio track starting a few ms
// earlier is enough): Matroska rounds timestamps to the millisecond, and after subtracting
// the different start points every third frame lined up with its neighbour — scoring 0.90
// on a perfect encode, dragging the average to ~0.96, and failing the quality check on files
// that were fine.
func (s *Service) ssimWindow(ctx context.Context, distorted, reference string, dStart, rStart, dur float64, w, h int, rate, crop string) (float64, error) {
	align := "setpts=PTS-STARTPTS"
	if validRate(rate) {
		align += ",fps=" + rate + ",setpts=N/FRAME_RATE/TB"
	}
	ref := ""
	if crop != "" {
		ref = crop + ","
	}
	lavfi := fmt.Sprintf("[0:v]%s[d];[1:v]%sscale=%d:%d:flags=bicubic,%s[r];[d][r]ssim", align, ref, w, h, align)
	args := []string{"-nostdin", "-hide_banner"}
	seek := func(path string, start float64) {
		if start > 0 {
			args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
		}
		if dur > 0 {
			args = append(args, "-t", strconv.FormatFloat(dur, 'f', 3, 64))
		}
		args = append(args, "-i", path)
	}
	seek(distorted, dStart)
	seek(reference, rStart)
	args = append(args, "-lavfi", lavfi, "-an", "-sn", "-f", "null", "-")
	out, _ := exec.CommandContext(ctx, s.ffmpeg, args...).CombinedOutput()
	return parseSSIM(string(out))
}

// validRate reports whether a probed frame rate ("24000/1001") is usable for re-timing.
func validRate(r string) bool {
	num, den, ok := strings.Cut(r, "/")
	if !ok {
		return false
	}
	n, e1 := strconv.Atoi(num)
	d, e2 := strconv.Atoi(den)
	return e1 == nil && e2 == nil && n > 0 && d > 0 && n/d < 1000
}

// parseSSIM extracts the aggregate SSIM ("All:0.987…") from ffmpeg's ssim-filter output.
func parseSSIM(out string) (float64, error) {
	i := strings.LastIndex(out, "All:")
	if i < 0 {
		return 0, fmt.Errorf("no SSIM score in ffmpeg output")
	}
	rest := strings.TrimSpace(out[i+len("All:"):])
	end := strings.IndexAny(rest, " (\r\n")
	if end < 0 {
		end = len(rest)
	}
	v, err := strconv.ParseFloat(rest[:end], 64)
	if err != nil {
		return 0, fmt.Errorf("unparseable SSIM %q: %w", rest[:end], err)
	}
	// ParseFloat takes "nan" and "inf" too; neither is a score, and a NaN would compare false
	// against every bar rather than fail it.
	if math.IsNaN(v) || v < 0 || v > 1 {
		return 0, fmt.Errorf("SSIM %q is not a score", rest[:end])
	}
	return v, nil
}

// higherQuality returns the next, tighter quality target for a retry, and false once the
// floor is reached. The floor used to be applied silently: a 4K film at CRF 18 retried at
// 16, then at 16 again — a second full encode guaranteed to reproduce the first.
func higherQuality(codec string, q int) (int, bool) {
	if q <= 0 {
		q = maxQualityCRF(codec, nil)
	}
	floor := 14
	if codec == "av1" {
		floor = 16
	}
	next := q - 2
	if next < floor {
		next = floor
	}
	return next, next < q
}

// parseFloatDefault parses a float, returning def on failure.
func parseFloatDefault(s string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return v
	}
	return def
}
