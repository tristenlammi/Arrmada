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
// SSIM's real cost is decoding both files; when a VAAPI device is present we decode on the GPU
// (frames auto-transfer to system memory for the CPU ssim filter), so verification doesn't peg
// the CPU after a GPU encode. Falls back to a pure-CPU pass if hardware decode can't handle the
// streams.
func (s *Service) computeSSIM(ctx context.Context, distorted, reference string) (float64, error) {
	if s.encoder.Kind == "vaapi" {
		if score, err := s.ssim(ctx, distorted, reference, true); err == nil {
			return score, nil
		} else {
			s.log.Warn("convert: hardware-decoded SSIM failed, falling back to CPU decode", "err", err)
		}
	}
	return s.ssim(ctx, distorted, reference, false)
}

// ssim runs the SSIM comparison, optionally decoding both inputs on the GPU (hwDecode).
func (s *Service) ssim(ctx context.Context, distorted, reference string, hwDecode bool) (float64, error) {
	di, err := probe(ctx, s.ffprobe, distorted)
	if err != nil {
		return 0, err
	}
	if di.Width <= 0 || di.Height <= 0 {
		return 0, fmt.Errorf("could not read output resolution")
	}
	lavfi := fmt.Sprintf("[1:v]scale=%d:%d:flags=bicubic[ref];[0:v][ref]ssim", di.Width, di.Height)
	// Per-input hardware decode (auto-downloads frames to system memory for the CPU ssim filter).
	var hw []string
	if hwDecode {
		hw = []string{"-hwaccel", "vaapi", "-hwaccel_device", vaapiDevice}
	}
	args := []string{"-nostdin", "-hide_banner"}
	args = append(args, hw...)
	args = append(args, "-i", distorted)
	args = append(args, hw...)
	args = append(args, "-i", reference)
	// ssim prints its summary to stderr; -f null discards frames. Exit status is ignored — we
	// rely on parsing the "All:" score.
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
