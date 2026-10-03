package convert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// The rehearsal. A full re-encode of a 4K film runs for the best part of a day on a CPU,
// and until now the two things that decide whether it's kept — does it look the same, and
// is it worth the space — were only answered at the very end. A film that fell short was
// re-encoded whole at a tighter setting, and one that didn't shrink enough was thrown away
// after all that work.
//
// So a re-encode is rehearsed first: the exact encoder and settings, run on the stretches
// the final quality check measures, each scored against the original.
//
//   - Short of the quality bar → the target is tightened now, on clips, not on the film.
//   - The clips' size, scaled to the runtime, is a measured prediction of the output. A
//     file it says won't come out minSavingPct smaller is left alone before it starts.
//
// The full encode is still checked end to end afterwards; the rehearsal just means that
// check almost always passes first time.

// preflightMinDuration is the shortest file worth rehearsing, in seconds. Below it,
// encoding the file outright costs little more than the rehearsal would. (A var so the
// real-encode test can rehearse a short film.)
var preflightMinDuration = 20 * 60.0

// preflightMargin is how far over the quality bar the clips must score. The full check
// samples the same stretches, but a clip is encoded without the frames around it, so its
// score can differ from the film's by a hair.
const preflightMargin = 0.002

// hdr10plusStreamCost is how much bigger the HDR10+ pipeline's encode is than the clips:
// it has to run without B-frames (see encodeHEVCStream).
const hdr10plusStreamCost = 1.15

type preflightVerdict struct {
	ran       bool
	quality   int     // the target the full encode should use
	ssim      float64 // the clips' mean score at that target
	projected int64   // the predicted output size, bytes
	// skipKind and reason are set when the full encode shouldn't run at all.
	skipKind, reason string
}

func (s *Service) preflight(ctx context.Context, job *Job, src string, mi *MediaInfo, enc Encoder, plan Plan, hdr10plus bool) (preflightVerdict, error) {
	v := preflightVerdict{quality: plan.Quality}
	wins := ssimWindows(mi.DurationSec)
	if mi.DurationSec < preflightMinDuration || len(wins) < 2 {
		return v, nil
	}
	dir := filepath.Join(s.activeScratch(ctx), fmt.Sprintf("trial-pre-%d", job.ID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return v, err
	}
	defer os.RemoveAll(dir)
	cores := s.cpuCores(ctx)
	limit := sizeCap(mi, plan)
	for round := 0; ; round++ {
		if !s.waitAllowed(ctx, job) {
			return v, ctx.Err()
		}
		s.update(job, func(j *Job) { j.State = StateTesting; j.Progress = 0 })
		s.event("info", fmt.Sprintf("Test-encoding %d clips of %s at CRF %d…", len(wins), job.Title, v.quality))
		cp := plan
		cp.Quality = v.quality
		var bytes int64
		var secs, sum float64
		for i, w := range wins {
			out := filepath.Join(dir, fmt.Sprintf("clip-%d.mkv", i+1))
			if err := s.encodeClip(ctx, job, src, out, mi, enc, cp, trialClip{w.start, w.dur}, cores); err != nil {
				return v, err
			}
			di, err := probe(ctx, s.ffprobe, out)
			if err != nil || di.Width <= 0 {
				return v, fmt.Errorf("a test clip couldn't be read")
			}
			sc, err := s.ssimWindow(ctx, out, src, 0, w.start, w.dur, di.Width, di.Height, di.FrameRateRat, cp.Crop.filter())
			if err != nil {
				return v, err
			}
			bytes += fileSize(out)
			secs += w.dur
			sum += sc
			_ = os.Remove(out)
			p := float64(i+1) / float64(len(wins))
			s.update(job, func(j *Job) { j.Progress = p })
		}
		v.ran = true
		v.ssim = sum / float64(len(wins))
		video := float64(bytes) * mi.DurationSec / secs
		if hdr10plus {
			video *= hdr10plusStreamCost
		}
		v.projected = int64(video) + keptAudioBytes(mi, plan)
		// The Library shows this from now on instead of the estimate.
		s.recordMeasurement(ctx, src, plan.VideoCodec, measurement{Size: mi.SizeBytes, CRF: v.quality,
			VideoBytes: int64(video), SSIM: v.ssim, Source: "test encode"})
		if limit > 0 && v.projected > limit {
			v.skipKind = SkipNotSmaller
			v.reason = fmt.Sprintf("a test encode predicts only %d%% smaller (%s → ~%s) — not worth another generation of compression, kept the original",
				savedPct(mi.SizeBytes, v.projected), humanBytes(mi.SizeBytes), humanBytes(v.projected))
			return v, nil
		}
		if v.ssim >= minSSIM+preflightMargin {
			return v, nil
		}
		next, ok := higherQuality(plan.VideoCodec, v.quality)
		if !ok || round >= qualityRetries {
			v.skipKind = SkipQualityGate
			v.reason = fmt.Sprintf("test encodes couldn't reach the quality bar (SSIM %.4f at CRF %d) — kept the original", v.ssim, v.quality)
			return v, nil
		}
		s.event("info", fmt.Sprintf("%s: test clips scored SSIM %.4f at CRF %d — trying CRF %d", job.Title, v.ssim, v.quality, next))
		v.quality = next
	}
}
