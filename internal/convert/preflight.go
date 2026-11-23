package convert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The rehearsal. A full re-encode of a 4K film runs for the best part of a day on a CPU,
// and until now the two things that decide whether it's kept — does it look the same, and
// is it worth the space — were only answered at the very end. A film that fell short was
// re-encoded whole at a tighter setting, and one that didn't shrink enough was thrown away
// after all that work.
//
// So a re-encode is rehearsed first: the exact encoder and settings, run on a few clips
// across the film, each scored against the original and held to the same bar as the final
// check (average and worst scene). The final check then measures OTHER scenes
// (verifyWindows): a target tuned to pass these clips is judged on scenes it wasn't tuned on.
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

// preflightMargin is how far over the quality bar (both the average and the worst clip) the
// clips must score. The full check samples different scenes, and a clip is encoded without
// the frames around it, so the film's scores can differ from the clips' by a little.
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

// A miss on the saving rule is only final when it's clear. Four 15-second clips predict a
// two-hour film to within maybe a quarter either way, so a prediction just short of the
// rule (a 15% saving against the 20% it needs) could as well be 25%. A prediction under
// clearMissPct is a permanent "not smaller"; one between that and the rule is borderline:
// the file waits borderlineRetest and is tested again on other scenes (ssimWindowsAt), and
// is given up on only when borderlineTests samples in a row have all fallen short. The size
// cap still stops a full encode the moment it can no longer save enough.
const (
	clearMissPct     = 10 // a predicted saving below this is a clear miss
	borderlineTests  = 3  // samples in a row short of the rule before it's given up on
	borderlineRetest = 7 * 24 * time.Hour
)

// sizeVerdict judges a rehearsal's projected size against the size cap (limit, 0 = none):
// "" when it would save enough, SkipBorderline when it falls just short and this file has
// had fewer than borderlineTests samples in a row (tested before this one), else
// SkipNotSmaller — at once for a clear miss, or after the last borderline sample.
func sizeVerdict(size, projected, limit int64, tested int) (kind, reason string) {
	if limit <= 0 || projected <= limit {
		return "", ""
	}
	pct := savedPct(size, projected)
	switch {
	case pct >= clearMissPct && tested+1 < borderlineTests:
		return SkipBorderline, fmt.Sprintf("a test encode predicts ~%d%% smaller (%s → ~%s), just short of the %d%% it needs — testing again on other scenes in %d days",
			pct, humanBytes(size), humanBytes(projected), minSavingPct, int(borderlineRetest.Hours()/24))
	case pct >= clearMissPct:
		return SkipNotSmaller, fmt.Sprintf("%d test encodes on different scenes all predicted under %d%% smaller (last: ~%d%%, %s → ~%s) — not worth another generation of compression, kept the original",
			borderlineTests, minSavingPct, pct, humanBytes(size), humanBytes(projected))
	}
	return SkipNotSmaller, fmt.Sprintf("a test encode predicts only %d%% smaller (%s → ~%s) — not worth another generation of compression, kept the original",
		pct, humanBytes(size), humanBytes(projected))
}

// preflight rehearses the encode. tested is how many times in a row this file's rehearsal
// has already come out borderline; each retest samples other scenes.
func (s *Service) preflight(ctx context.Context, job *Job, src string, mi *MediaInfo, enc Encoder, plan Plan, hdr10plus bool, tested int) (preflightVerdict, error) {
	v := preflightVerdict{quality: plan.Quality}
	wins := ssimWindowsAt(mi.DurationSec, tested)
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
		worst := 1.0
		for i, w := range wins {
			out := filepath.Join(dir, fmt.Sprintf("clip-%d.mkv", i+1))
			if err := s.encodeClip(ctx, job, src, out, mi, enc, cp, trialClip{w.start, w.dur}, cores); err != nil {
				return v, err
			}
			di, err := probe(ctx, s.ffprobe, out)
			if err != nil || di.Width <= 0 {
				return v, fmt.Errorf("a test clip couldn't be read")
			}
			sc, err := s.ssimWindow(ctx, out, src, 0, w.start, w.dur, di.Width, di.Height, di.FrameRateRat, refLegOf(mi, cp))
			if err != nil {
				return v, err
			}
			bytes += fileSize(out)
			secs += w.dur
			sum += sc
			worst = min(worst, sc)
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
		kind, reason := sizeVerdict(mi.SizeBytes, v.projected, limit, tested)
		if kind != SkipBorderline {
			// The Library shows this from now on instead of the estimate. A borderline
			// result isn't recorded: the file would read as not worth converting and never
			// come back for its retest.
			s.recordMeasurement(ctx, src, plan.VideoCodec, measurement{Size: mi.SizeBytes, CRF: v.quality,
				VideoBytes: int64(video), SSIM: v.ssim, Source: "test encode", Encoder: enc.Kind})
		}
		if kind != "" {
			v.skipKind, v.reason = kind, reason
			return v, nil
		}
		if v.ssim >= minSSIMMean+preflightMargin && worst >= minSSIMWindow+preflightMargin {
			return v, nil
		}
		next, ok := higherQuality(plan.VideoCodec, v.quality)
		if !ok || round >= qualityRetries {
			v.skipKind = SkipQualityGate
			v.reason = fmt.Sprintf("test encodes couldn't reach the quality bar (SSIM %.4f average, %.4f lowest at CRF %d) — kept the original",
				v.ssim, worst, v.quality)
			return v, nil
		}
		s.event("info", fmt.Sprintf("%s: test clips scored SSIM %.4f average, %.4f lowest at CRF %d — trying CRF %d", job.Title, v.ssim, worst, v.quality, next))
		v.quality = next
	}
}
