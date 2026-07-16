package convert

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// This file holds the C4 quality gate: after a transcode, measure how close the output is to the
// source and re-encode at a higher quality if it falls short. We use SSIM — the bundled ffmpeg
// isn't built with libvmaf, so VMAF isn't available; SSIM is a solid always-present proxy (≈0.98+
// is visually near-transparent). If a libvmaf build is ever bundled, swap the metric here.

// computeSSIM measures the structural similarity of the encoded output against the source. The
// reference is scaled to the output's resolution first, so a deliberate downscale is judged on
// how well the encode preserved the (downscaled) picture, not penalised for the resize.
//
// Rather than decode the whole film, it samples a handful of short windows and averages them. A
// full-file SSIM decodes both the output and the source end-to-end on the CPU, which pegged every
// core for the length of a feature (starving VMs and everything else on the box). A few slices are
// statistically representative for a pass/fail gate at a tiny fraction of the cost.
//
// Decode is on the CPU: it's the reliable path (hardware-decoded SSIM could stall the pipeline on
// some GPUs). The caller wraps this in a timeout so a slow/hung verify can never block a job.
func (s *Service) computeSSIM(ctx context.Context, distorted, reference string) (float64, error) {
	di, err := probe(ctx, s.ffprobe, distorted)
	if err != nil {
		return 0, err
	}
	if di.Width <= 0 || di.Height <= 0 {
		return 0, fmt.Errorf("could not read output resolution")
	}
	var sum float64
	var n int
	for _, wnd := range ssimWindows(di.DurationSec) {
		sc, err := s.ssimWindow(ctx, distorted, reference, wnd.start, wnd.dur, di.Width, di.Height)
		if err != nil {
			continue // a single unreadable slice shouldn't fail the whole measurement
		}
		sum += sc
		n++
	}
	if n == 0 {
		return 0, fmt.Errorf("no SSIM score in ffmpeg output")
	}
	return sum / float64(n), nil
}

// ssimWnd is one sample window (seconds).
type ssimWnd struct{ start, dur float64 }

// ssimWindows picks representative sample windows across a runtime, skipping the very start/end
// (logos, credits) where content isn't typical. Short files are measured in a single pass.
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
func (s *Service) ssimWindow(ctx context.Context, distorted, reference string, start, dur float64, w, h int) (float64, error) {
	lavfi := fmt.Sprintf("[0:v]setpts=PTS-STARTPTS[d];[1:v]scale=%d:%d:flags=bicubic,setpts=PTS-STARTPTS[r];[d][r]ssim", w, h)
	args := []string{"-nostdin", "-hide_banner"}
	seek := func(path string) {
		if start > 0 {
			args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
		}
		if dur > 0 {
			args = append(args, "-t", strconv.FormatFloat(dur, 'f', 3, 64))
		}
		args = append(args, "-i", path)
	}
	seek(distorted)
	seek(reference)
	args = append(args, "-lavfi", lavfi, "-an", "-sn", "-f", "null", "-")
	out, _ := exec.CommandContext(ctx, s.ffmpeg, args...).CombinedOutput()
	return parseSSIM(string(out))
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
	return v, nil
}

// higherQuality returns a lower CRF (better quality) for a quality-gate retry, floored so we
// don't chase perfection into an enormous file.
func higherQuality(plan Plan) int {
	q := plan.Quality
	if q <= 0 {
		q = crfDefault(plan.VideoCodec)
	}
	if q -= 3; q < 16 {
		q = 16
	}
	return q
}

// parseFloatDefault parses a float, returning def on failure.
func parseFloatDefault(s string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return v
	}
	return def
}
