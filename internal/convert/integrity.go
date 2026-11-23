package convert

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/toolrun"
)

// The last checks before a converted file replaces its original, run on the exact file that
// will be swapped in (the finished output in scratch, after verifyOutput):
//
//   - The full-decode integrity check, a gate. The SSIM check scores ten 10-second scenes and
//     verifyOutput reads only the container's figures, so a corrupt or truncated stretch
//     anywhere else passed both — ffmpeg exits 0 on most mid-stream damage. Every frame of the
//     video is decoded; any decode error, or a frame count that doesn't match the original's,
//     keeps the original. A check that can't run or doesn't finish also keeps it (fail
//     closed).
//   - VMAF, when this ffmpeg has libvmaf: measured on the same scenes as SSIM and recorded in
//     the ledger, but advisory for now — it doesn't decide anything until there are real
//     numbers to set a bar from. One that can't be measured is logged and the swap goes on
//     its gates.
//
// Both run inside finalizeOutput on the dst it is handed, so any way of producing the
// output — including a pieced, resumable encode joined back together (CONV-27) — gets the
// same check by handing its joined file to finalizeOutput; nothing else needs calling.

// decodeResult is what a full decode found.
type decodeResult struct {
	frames int64    // frames decoded
	errors []string // ffmpeg's error lines, the first few
}

// decodeFn decodes a file's video end to end; a seam so the gate can be tested without
// ffmpeg.
var decodeFn = (*Service).decodeVideo

// vmafWindowFn scores one scene with VMAF; a seam for tests.
var vmafWindowFn = (*Service).vmafWindow

// checkBeforeSwap runs the full-decode check (and VMAF) on dst. It returns true when the
// file may be swapped in, and false when the job has been finished here (the original kept)
// or cancelled.
func (s *Service) checkBeforeSwap(ctx context.Context, job *Job, src, dst string, mi, out *MediaInfo, plan Plan) bool {
	if !s.waitAllowed(ctx, job) {
		return false
	}
	kind, reason := s.integrityCheck(ctx, job, dst, mi, out, plan)
	if ctx.Err() != nil {
		return false
	}
	if reason != "" {
		s.event("warn", fmt.Sprintf("%s: full-decode check failed — %s", job.Title, reason))
		s.finishAfterEncode(job, kind, reason+" — kept the original")
		return false
	}
	s.measureVMAF(ctx, job, src, dst, mi, out, plan)
	return ctx.Err() == nil
}

// integrityCheck decodes dst's video end to end and returns why it fails ("" = it passed)
// and the skip that records it: a damaged file is a quality_gate skip, a check that couldn't
// run or finish a transient one that's tried again later — either way the original stays.
// On a machine with VAAPI hardware decoding for the output's format the first pass runs on
// the GPU; anything short of a clean pass there is decoded again on the CPU before judging,
// because a driver hiccup isn't corruption. The CPU pass is the verdict.
func (s *Service) integrityCheck(ctx context.Context, job *Job, dst string, mi, out *MediaInfo, plan Plan) (kind, reason string) {
	if out == nil {
		return SkipQualityGate, "the converted file couldn't be read"
	}
	want, exact := expectedFrames(mi, out, plan)
	if want <= 0 {
		return SkipQualityGate, "the original's frame count is unknown, so the converted file couldn't be checked frame for frame"
	}
	s.update(job, func(j *Job) { j.State = StateVerifying; j.Progress = 0 })
	s.event("info", fmt.Sprintf("%s: decoding the whole converted file to make sure it plays cleanly…", job.Title))
	// A hang guard, not a deadline: decoding is niced and idle-I/O, so on a busy box it can
	// take far longer than the film. Only time the job isn't paused counts.
	budget := max(time.Hour, time.Duration(3*out.DurationSec)*time.Second)
	pass := func(dev string) (string, string) {
		dctx, cancel := s.pausableTimeout(ctx, job, budget)
		defer cancel()
		res, err := decodeFn(s, dctx, job, dst, out, dev)
		switch {
		case ctx.Err() != nil:
			return SkipCancelled, "cancelled"
		case dctx.Err() != nil:
			return SkipTransient, fmt.Sprintf("the full-decode check didn't finish within %s of running time", budget.Round(time.Minute))
		case len(res.errors) > 0:
			return SkipQualityGate, "the converted file has decode errors (" + res.errors[0] + ")"
		case err != nil:
			return SkipTransient, fmt.Sprintf("the full-decode check couldn't run (%v)", err)
		}
		if r := framesVerdict(res.frames, want, exact, out.FrameRate); r != "" {
			return SkipQualityGate, r
		}
		return "", ""
	}
	if dev := s.hwDecodeDevice(ctx, out.VideoCodec); dev != "" {
		kind, reason := pass(dev)
		if reason == "" || ctx.Err() != nil {
			return kind, reason
		}
		s.log.Info("convert: hardware-decoded check didn't pass cleanly — checking again on the CPU", "title", job.Title, "why", reason)
		s.event("info", fmt.Sprintf("%s: the hardware-decoded check didn't pass cleanly (%s) — decoding again on the CPU", job.Title, reason))
		s.update(job, func(j *Job) { j.Progress = 0 })
	}
	return pass("")
}

// hwDecodeDevice is the VAAPI device to decode a codec on, or "" for the CPU. Hardware
// decoding is used where a working VAAPI encoder for the same format exists: hardware that
// encodes a format decodes it.
func (s *Service) hwDecodeDevice(ctx context.Context, codec string) string {
	c := codecClass(codec)
	if c != "hevc" && c != "av1" {
		return ""
	}
	for _, e := range s.encoders {
		if e.Codec == c && e.Kind == "vaapi" && e.Available && !s.hardwareIsBroken(e.Name) {
			return s.vaapiDev(ctx)
		}
	}
	return ""
}

// expectedFrames is how many frames the converted file's video should hold, and whether
// that number is exact (from the container's own count) or worked out from length × rate.
// It never reads the source again.
//
// A copied stream keeps the original's frames one for one. A re-encode into Matroska comes
// out at a constant rate — ffmpeg duplicates or drops to hold it — so when that rate isn't
// the original's (a variable-rate source flattened, an interlaced or telecined one whose
// nominal rate differs) the count is the original's length at the output's rate.
func expectedFrames(mi, out *MediaInfo, plan Plan) (float64, bool) {
	if mi == nil {
		return 0, false
	}
	if plan.VideoCodec != "" && out != nil && out.FrameRate > 0 &&
		(mi.FrameRate <= 0 || math.Abs(out.FrameRate-mi.FrameRate) > mi.FrameRate*0.005) {
		return videoSeconds(mi) * out.FrameRate, false
	}
	est := videoSeconds(mi) * mi.FrameRate
	// A stale count (a statistics tag left behind by an edit) would fail a good file: trust
	// it only when it agrees with the length to within 1%.
	if mi.Frames > 0 && (est <= 0 || math.Abs(float64(mi.Frames)-est) <= est*0.01) {
		return float64(mi.Frames), true
	}
	return est, false
}

// videoSeconds is the length of a file's video: the stream's own when known, else the file's.
func videoSeconds(mi *MediaInfo) float64 {
	if mi.VideoSec > 0 {
		return mi.VideoSec
	}
	return mi.DurationSec
}

// framesVerdict compares the frames decoded with the frames expected and says what's wrong
// ("" = close enough). An exact count may be off by 0.1%; a worked-out one also by two
// seconds' worth, the same allowance verifyOutput gives the running time.
func framesVerdict(got int64, want float64, exact bool, fps float64) string {
	tol := max(want*0.001, 1)
	if !exact && fps > 0 {
		tol = max(tol, 2*fps)
	}
	diff := float64(got) - want
	if math.Abs(diff) <= tol {
		return ""
	}
	approx := ""
	if !exact {
		approx = "~"
	}
	if diff < 0 {
		return fmt.Sprintf("the converted file is missing %d frames (%d decoded, the original has %s%d)",
			int64(-diff+0.5), got, approx, int64(want+0.5))
	}
	return fmt.Sprintf("the converted file has %d more frames than the original (%d decoded, the original has %s%d)",
		int64(diff+0.5), got, approx, int64(want+0.5))
}

// decodeVideo decodes a file's video end to end and throws the pictures away, counting
// frames. -xerror stops at the first decode error; -v error leaves only problems on stderr,
// so anything there is one. -fps_mode passthrough keeps ffmpeg from dropping or duplicating
// frames, so the count is what the stream holds. dev, when set, is a VAAPI device to decode
// on. Runs idle-I/O, niced and pausable like every side step.
func (s *Service) decodeVideo(ctx context.Context, job *Job, path string, out *MediaInfo, dev string) (decodeResult, error) {
	args := []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-xerror"}
	if dev != "" {
		args = append(args, "-hwaccel", "vaapi", "-hwaccel_device", dev)
	}
	args = append(args, "-i", path, "-map", fmt.Sprintf("0:v:%d", out.VideoIndex), "-an", "-sn", "-dn",
		"-fps_mode", "passthrough", "-progress", "pipe:1", "-f", "null", "-")
	cmd := s.tool(ctx, job, toolrun.Idle, s.ffmpeg, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return decodeResult{}, err
	}
	errs := &errLines{max: 5}
	cmd.Stderr = errs
	if err := cmd.Start(); err != nil {
		return decodeResult{}, err
	}
	var res decodeResult
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "frame":
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				res.frames = n
			}
		case "out_time_ms": // microseconds, despite the name
			if us, err := strconv.ParseFloat(v, 64); err == nil && out.DurationSec > 0 && job != nil {
				p := math.Min(1, us/1e6/out.DurationSec)
				s.update(job, func(j *Job) { j.Progress = p })
			}
		}
	}
	err = cmd.Wait()
	res.errors = errs.lines()
	return res, err
}

// errLines keeps the first few lines a tool writes (a damaged file can print thousands).
type errLines struct {
	mu  sync.Mutex
	max int
	buf bytes.Buffer
}

func (e *errLines) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.buf.Len() < 8<<10 {
		e.buf.Write(p)
	}
	return len(p), nil
}

func (e *errLines) lines() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, l := range strings.Split(e.buf.String(), "\n") {
		if l = strings.TrimSpace(logAddr.ReplaceAllString(l, "[$1]")); l != "" && len(out) < e.max {
			out = append(out, l)
		}
	}
	return out
}

// logAddr is the context address in an ffmpeg log prefix ("[hevc @ 0x55d1…]"): noise in a
// note someone reads.
var logAddr = regexp.MustCompile(`\[([^\]@]+?) @ 0x[0-9a-fA-F]+\]`)

// --- VMAF (advisory) -------------------------------------------------------------------------

// hasLibVMAF reports whether this ffmpeg was built with libvmaf. Checked once at startup.
// (The bundled jellyfin-ffmpeg7 wasn't, as of October 2026: it has only vmafmotion, which is
// not a VMAF score.)
func hasLibVMAF(ctx context.Context, ffmpeg string) bool {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := toolrun.Command(ctx, toolrun.Normal, ffmpeg, "-hide_banner", "-filters").Output()
	return err == nil && filtersHaveVMAF(string(out))
}

// filtersHaveVMAF looks for the libvmaf filter in `ffmpeg -filters` output, whose lines read
// " TS. ssim  VV->V  Calculate the SSIM…" — flags, then the name.
func filtersHaveVMAF(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[1] == "libvmaf" {
			return true
		}
	}
	return false
}

// measureVMAF scores the converted file with VMAF on the final check's scenes and records
// the mean in the ledger. Advisory: whatever happens here, the swap decision has been made.
func (s *Service) measureVMAF(ctx context.Context, job *Job, src, dst string, mi, out *MediaInfo, plan Plan) {
	if !s.vmaf || plan.VideoCodec == "" || out == nil || out.Width <= 0 || out.Height <= 0 {
		return
	}
	vctx, cancel := s.pausableTimeout(ctx, job, 30*time.Minute)
	defer cancel()
	var sum float64
	wins := verifyWindows(out.DurationSec)
	for _, w := range wins {
		v, err := vmafWindowFn(s, vctx, dst, src, w.start, w.start, w.dur, out.Width, out.Height, out.FrameRateRat, refLegOf(mi, plan))
		if err != nil {
			if ctx.Err() == nil {
				s.event("warn", fmt.Sprintf("%s: VMAF couldn't be measured (%v) — it's advisory; SSIM and the full-decode check passed", job.Title, err))
			}
			return
		}
		sum += v
	}
	mean := sum / float64(len(wins))
	rec := s.record(job)
	rec.vmafMean = mean
	s.history.setVMAF(context.Background(), rec.historyID, mean)
	s.event("info", fmt.Sprintf("%s: VMAF %.1f on average over %d scenes (advisory — recorded, not yet a gate)", job.Title, mean, len(wins)))
}

// vmafWindow scores one scene with VMAF, paired with the source exactly as ssimWindow pairs
// it (same seek, re-timing, crop and scaling). libvmaf takes the distorted input first.
func (s *Service) vmafWindow(ctx context.Context, distorted, reference string, dStart, rStart, dur float64, w, h int, rate string, ref refLeg) (float64, error) {
	args := pairedArgs(distorted, reference, dStart, rStart, dur, w, h, rate, ref,
		"libvmaf=n_threads="+strconv.Itoa(s.cpuCores(ctx)))
	out, _ := s.tool(ctx, nil, toolrun.Idle, s.ffmpeg, args...).CombinedOutput()
	return parseVMAF(string(out))
}

var vmafScore = regexp.MustCompile(`VMAF score[:=]\s*([0-9.]+)`)

// parseVMAF extracts the pooled score from ffmpeg's libvmaf output ("VMAF score: 95.43").
func parseVMAF(out string) (float64, error) {
	m := vmafScore.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		return 0, fmt.Errorf("no VMAF score in ffmpeg output")
	}
	v, err := strconv.ParseFloat(m[len(m)-1][1], 64)
	if err != nil || math.IsNaN(v) || v < 0 || v > 100 {
		return 0, fmt.Errorf("VMAF %q is not a score", m[len(m)-1][1])
	}
	return v, nil
}
