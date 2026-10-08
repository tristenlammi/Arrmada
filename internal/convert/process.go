package convert

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// Seams over the disks and ffprobe, so tests can stage a full disk or a probed film without
// needing either.
var (
	freeBytesFn  = freeBytes
	sameDeviceFn = sameDevice
	moveFileFn   = moveFile
	probeFn      = probe
)

// process converts one file: analyse it, decide what it needs, settle the codec, encode,
// check the result against the source, and only then swap it into the library.
func (s *Service) process(ctx context.Context, job *Job) {
	src, title, origLang, ok := s.resolveSource(ctx, job)
	if !ok {
		// Usually mid-import or mid-upgrade: look again later rather than on the next pick.
		s.finishSkip(job, SkipSourceGone, "the library file is missing — checking again later")
		return
	}
	if title != "" && job.Kind != "episode" {
		s.update(job, func(j *Job) { j.Title = title })
	}
	mi, err := probeFn(ctx, s.ffprobe, src)
	if err != nil {
		s.finish(job, StateFailed, "could not analyze file: "+err.Error())
		return
	}
	s.update(job, func(j *Job) { j.SrcBytes = mi.SizeBytes; j.DurationSec = mi.DurationSec })

	p := s.prefs(ctx)
	plan, needs := p.planFor(mi, src, origLang, nil)
	if !needs.Any() || (!job.Requested && !needs.Worth) {
		note := "already matches — nothing to do"
		if needs.Why != "" {
			note = needs.Why + " — nothing to do"
		} else if needs.Any() {
			note = "only its tracks differ, and tidying them would free little — left for now"
		}
		s.finishSkip(job, SkipAlreadyTarget, note)
		return
	}
	// Seeding safety: a hardlinked file is still shared by the download client, so replacing
	// the library copy frees nothing and duplicates the data. Tried again later.
	if fileLinks(src) > 1 {
		s.finishSkip(job, SkipHardlinked, "still seeding (hardlinked to your downloads) — will try again later")
		return
	}
	if needs.Video && mi.DVUnconvertible() {
		s.finishSkip(job, SkipHDRUnsupported, needs.Why)
		return
	}
	scratch := s.activeScratch(ctx)
	// Room on both disks is checked before anything heavy: the HDR10+ read, crop detection
	// and the format test all read the film, and a file that can't fit would only fail
	// after them — then be picked again and do it all over.
	if !s.spaceCheck(job, src, mi, plan, scratch) {
		return
	}

	h10pJSON := ""
	var enc Encoder
	if needs.Video {
		hdr := mi.EncodeHDR()
		// HDR10+ is dynamic metadata ffprobe doesn't reliably report, so any PQ grade in an
		// HEVC stream is checked by extracting it. Success means the file has it, and it is
		// carried through the HEVC pipeline (the only one that can hold it). Reading it
		// means reading the whole file, so it waits its turn like an encode.
		if (hdr == "HDR10" || hdr == "HDR10+") && codecClass(mi.VideoCodec) == "hevc" && s.hdr10plusTool != "" {
			if !s.waitAllowed(ctx, job) {
				return
			}
			jf := filepath.Join(scratch, fmt.Sprintf("h10p-%d.json", job.ID))
			found := s.hasHDR10Plus(ctx, src, jf) == nil
			if ctx.Err() != nil {
				_ = os.Remove(jf)
				return
			}
			switch {
			case found:
				// The HDR10+ pipeline holds the stream twice in scratch. Check for that room
				// before the whole-file read, not after it.
				if !s.scratchFits(job, scratch, scratchNeeded(mi, plan, true)) {
					_ = os.Remove(jf)
					return
				}
				if err := s.readHDR10Plus(ctx, src, jf); err != nil {
					_ = os.Remove(jf)
					if ctx.Err() != nil {
						return
					}
					// It has HDR10+ we couldn't read in full: converting would drop it.
					s.finishSkip(job, SkipHDRUnsupported, "HDR10+ metadata couldn't be read — kept the original rather than lose it")
					return
				}
				h10pJSON = jf
				defer os.Remove(jf)
			case hdr == "HDR10+":
				_ = os.Remove(jf)
				s.finishSkip(job, SkipHDRUnsupported, "HDR10+ metadata couldn't be read — kept the original rather than lose it")
				return
			default:
				_ = os.Remove(jf) // labelled plain HDR10: no HDR10+ is normal
			}
		}
		// Black bars are found before the format test, so the test encodes what the
		// conversion will.
		plan = s.withCrop(ctx, src, mi, plan, p)
		if ctx.Err() != nil {
			return
		}
		if c := plan.Crop; c != nil {
			s.event("info", fmt.Sprintf("%s: removing black bars — %d×%d → %d×%d", job.Title, mi.Width, mi.Height, c.W, c.H))
		}
		codec := s.chooseCodec(ctx, job, src, mi, plan, p, h10pJSON != "")
		if ctx.Err() != nil {
			return
		}
		plan.VideoCodec, plan.Quality = codec, maxQualityCRF(codec, mi)
		enc = s.pickEncoder(job, mi, plan.VideoCodec, p.useGPU)
		if !s.canPreserveHDR(mi, plan, enc, h10pJSON != "") {
			s.finishSkip(job, SkipHDRUnsupported, hdr+" couldn't be carried through a conversion on this machine — kept the original")
			return
		}
	}
	// The exact disk-space guard, now the codec is settled: room for the biggest output that
	// could be kept (a re-encode is stopped once it passes that) and the HDR10+ pipeline's
	// intermediate copy. The early check above worked from the provisional plan.
	if !s.scratchFits(job, scratch, scratchNeeded(mi, plan, h10pJSON != "")) {
		return
	}
	s.update(job, func(j *Job) { j.Codec = plan.VideoCodec })

	// A re-encode is rehearsed on clips first, so a film that wouldn't pass the quality
	// check or wouldn't shrink enough is found out in minutes rather than after a day.
	if plan.VideoCodec != "" {
		v, err := s.preflight(ctx, job, src, mi, enc, plan, h10pJSON != "")
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			// Advisory: the full encode is still checked end to end.
			s.event("warn", fmt.Sprintf("%s: the test encode didn't finish (%v) — encoding without it", job.Title, err))
		case v.ran && v.skipKind != "":
			s.finishSkip(job, v.skipKind, v.reason)
			return
		case v.ran:
			plan.Quality = v.quality
			s.event("info", fmt.Sprintf("%s: test encode at CRF %d scored SSIM %.4f and predicts ~%s (%d%% smaller)",
				job.Title, v.quality, v.ssim, humanBytes(v.projected), savedPct(mi.SizeBytes, v.projected)))
		}
	}

	dst := filepath.Join(scratch, fmt.Sprintf("convert-%d.mkv", job.ID))
	defer os.Remove(dst)

	s.event("info", fmt.Sprintf("%s — source: %s · %s", job.Title, mediaSpec(mi), humanBytes(mi.SizeBytes)))
	if plan.VideoCodec == "" {
		s.event("info", fmt.Sprintf("Tidying tracks of %s (%s) — the video is copied as is", job.Title, trackSummary(mi, plan)))
	} else {
		s.event("info", fmt.Sprintf("Encoding %s → %s on %s", job.Title, strings.ToUpper(plan.VideoCodec), enc.Label))
	}
	if !s.waitAllowed(ctx, job) {
		return
	}
	s.update(job, func(j *Job) { j.State = StateEncoding; j.Encoder = enc.Label; j.Progress = 0 })
	if plan.VideoCodec == "" {
		s.update(job, func(j *Job) { j.Encoder = "Copy" })
	}

	for attempt := 0; ; attempt++ {
		if err := s.runEncode(ctx, job, src, dst, scratch, mi, enc, plan, h10pJSON); err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, errTooBig) {
				s.mu.Lock()
				done := int(job.Progress * 100)
				s.mu.Unlock()
				s.finishAfterEncode(job, SkipNotSmaller, fmt.Sprintf("stopped %d%% of the way through — it had already reached %d%% of the original's size, so it couldn't come out %d%% smaller; kept the original",
					done, 100-minSavingPct, minSavingPct))
				return
			}
			s.finish(job, StateFailed, "encode failed: "+err.Error())
			return
		}
		if plan.VideoCodec == "" {
			break // a copy can't lose quality
		}
		s.update(job, func(j *Job) { j.State = StateVerifying })
		s.event("info", fmt.Sprintf("Checking %s against the original…", job.Title))
		sctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
		score, err := s.computeSSIM(sctx, dst, src, plan.Crop.filter())
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// FAIL CLOSED: an unmeasurable result is not a pass. Keep the original, and count
			// it toward the failure limit (the outcome is the same every time for this file).
			s.log.Warn("convert: quality check could not measure SSIM — keeping the original", "err", err)
			s.finishAfterEncode(job, SkipQualityGate, "couldn't verify the encode matched the original — kept the original")
			return
		}
		s.update(job, func(j *Job) { j.SSIM = score })
		if score >= minSSIM {
			s.event("info", fmt.Sprintf("%s: quality check passed (SSIM %.4f)", job.Title, score))
			break
		}
		next, ok := higherQuality(plan.VideoCodec, plan.Quality)
		if !ok || attempt >= qualityRetries {
			s.finishAfterEncode(job, SkipQualityGate, fmt.Sprintf("couldn't reach the quality bar (SSIM %.4f after %d tries) — kept the original", score, attempt+1))
			return
		}
		plan.Quality = next
		if !s.waitAllowed(ctx, job) {
			return
		}
		s.event("warn", fmt.Sprintf("%s: SSIM %.4f is below %.2f — encoding again at higher quality (try %d)", job.Title, score, minSSIM, attempt+2))
		s.update(job, func(j *Job) { j.State = StateEncoding; j.Progress = 0 })
	}
	s.finalizeOutput(ctx, job, src, dst, mi, plan)
}

// spaceCheck makes sure both disks have room for this file before any heavy work, and
// skips it with a backoff when they don't. The scratch folder holds the encode; when it's
// on another filesystem from the library, the finished file is then copied in next to the
// original, so the library disk needs room for it too (on the same one it's a rename).
func (s *Service) spaceCheck(job *Job, src string, mi *MediaInfo, plan Plan, scratch string) bool {
	need := scratchNeeded(mi, plan, false)
	if !s.scratchFits(job, scratch, need) {
		return false
	}
	dir := filepath.Dir(src)
	if sameDeviceFn(scratch, dir) {
		return true
	}
	if free := freeBytesFn(dir); free > 0 && int64(free) < need {
		s.finishSkip(job, SkipLibraryFull, fmt.Sprintf("needs ~%s free on the library disk for the converted file, it has %s — will try again later",
			humanBytes(need), humanBytes(int64(free))))
		return false
	}
	return true
}

// scratchFits skips the job (with a backoff) when the scratch folder has less than need
// free. An unreadable free figure (0) doesn't block: the encode itself still fails cleanly
// on a full disk.
func (s *Service) scratchFits(job *Job, scratch string, need int64) bool {
	if free := freeBytesFn(scratch); free > 0 && int64(free) < need {
		s.finishSkip(job, SkipNoScratch, fmt.Sprintf("needs ~%s of scratch in %s, it has %s free — will try again later",
			humanBytes(need), scratch, humanBytes(int64(free))))
		return false
	}
	return true
}

// resolveSource re-resolves a job's current source file, title and the title's original
// language. ok is false when the file is gone.
func (s *Service) resolveSource(ctx context.Context, job *Job) (src, title, origLang string, ok bool) {
	if job.Kind == "episode" {
		if s.series == nil {
			return "", "", "", false
		}
		path, _ := s.series.EpisodeFilePath(ctx, job.SeriesID, job.Season, job.Episode)
		if path == "" {
			return "", "", "", false
		}
		if sm, err := s.series.Get(ctx, job.SeriesID); err == nil && sm.Extra != nil {
			origLang = sm.Extra.OriginalLanguage
		}
		return path, job.Title, origLang, true
	}
	if s.movies == nil {
		return "", "", "", false
	}
	m, err := s.movies.Get(ctx, job.MovieID)
	if err != nil || !m.HasFile || m.MovieFilePath == "" {
		return "", "", "", false
	}
	return m.MovieFilePath, m.Title, movieOrigLang(m), true
}

// chooseCodec settles the format for one file:
//
//   - HDR10+ → HEVC: it's the only pipeline that can carry the dynamic metadata.
//   - AV1 not allowed (your devices can't all play it) → HEVC.
//   - Otherwise a side-by-side test: the same few clips encoded both ways and scored
//     against the original. AV1 only wins when it's clearly smaller at the same quality —
//     on dark or grainy footage it often isn't, and then HEVC is the better file.
func (s *Service) chooseCodec(ctx context.Context, job *Job, src string, mi *MediaInfo, plan Plan, p prefs, hasHDR10Plus bool) string {
	switch {
	case hasHDR10Plus:
		return "hevc"
	case !p.allowAV1:
		return "hevc"
	case p.useGPU && !s.gpuCan("av1"):
		// GPU mode is about speed; AV1 on the CPU next to HEVC on the GPU isn't that.
		return "hevc"
	}
	if c, ok := s.choices.get(ctx, src, mi.SizeBytes); ok {
		return c
	}
	if !s.waitAllowed(ctx, job) {
		return "hevc"
	}
	s.update(job, func(j *Job) { j.State = StateTesting; j.Progress = 0 })
	s.event("info", fmt.Sprintf("Testing HEVC and AV1 on a few clips of %s…", job.Title))
	res, err := s.trial(ctx, job, src, mi, plan, p, "")
	if err != nil {
		if ctx.Err() == nil {
			s.event("warn", fmt.Sprintf("%s: the HEVC/AV1 test didn't finish (%v) — using HEVC", job.Title, err))
		}
		return "hevc"
	}
	s.event("info", fmt.Sprintf("%s: %s", job.Title, res.Why))
	s.choices.put(ctx, src, mi.SizeBytes, res.Pick, res.Why)
	return res.Pick
}

// canPreserveHDR reports whether this plan converts the file without losing its HDR. A copy
// always does. Static HDR (HDR10, HLG) is carried by the CPU encoders of both formats;
// HDR10+ only by the HEVC pipeline. Hardware encoders can't write HDR metadata, which is
// why pickEncoder sends HDR to the CPU.
func (s *Service) canPreserveHDR(mi *MediaInfo, plan Plan, enc Encoder, hasHDR10Plus bool) bool {
	if plan.VideoCodec == "" {
		return true
	}
	if mi.DVUnconvertible() {
		return false
	}
	hdr := mi.EncodeHDR()
	if !isHDR(hdr) {
		return true
	}
	if enc.Kind != "cpu" || !cpuWorks(plan.VideoCodec, s.encoders) {
		return false
	}
	// An HEVC stream with a PQ grade may carry HDR10+ that only the tool can find. Without
	// the tool we can't rule it out, and converting would silently drop it.
	if codecClass(mi.VideoCodec) == "hevc" && hdr == "HDR10" && s.hdr10plusTool == "" {
		return false
	}
	switch plan.VideoCodec {
	case "av1":
		return !hasHDR10Plus && (hdr == "HDR10" || hdr == "HLG")
	case "hevc":
		if hasHDR10Plus || hdr == "HDR10+" {
			return s.hdr10plusTool != "" && hasHDR10Plus
		}
		return hdr == "HDR10" || hdr == "HLG"
	}
	return false
}

// pickEncoder chooses where a file is encoded:
//
//  1. HDR of any kind → CPU. Every HDR metadata path is software-only.
//  2. GPU mode on and a working hardware encoder for the format → GPU.
//  3. Otherwise → CPU (the best quality per byte).
//
// The CPU encoder isn't guaranteed (a broken build fails every file); when it doesn't run,
// hardware is used if there is any.
func (s *Service) pickEncoder(job *Job, mi *MediaInfo, codec string, useGPU bool) Encoder {
	enc := s.encoderChoice(mi, codec, useGPU)
	if job != nil {
		s.update(job, func(j *Job) { j.Encoder = enc.Label })
	}
	return enc
}

func (s *Service) encoderChoice(mi *MediaInfo, codec string, useGPU bool) Encoder {
	cpu := cpuEncoder(codec)
	hw, hasHW := hardwareFor(codec, s.encoders)
	hwOK := hasHW && !s.hardwareIsBroken(hw.Name)
	if !cpuWorks(codec, s.encoders) && hwOK {
		return hw
	}
	if isHDR(mi.EncodeHDR()) {
		return cpu
	}
	if useGPU && hwOK {
		return hw
	}
	return cpu
}

// gpuCan reports whether a working hardware encoder exists for a format.
func (s *Service) gpuCan(codec string) bool {
	hw, ok := hardwareFor(codec, s.encoders)
	return ok && !s.hardwareIsBroken(hw.Name)
}

// errTooBig means a re-encode was stopped at its size cap: it was on course to come out
// less than minSavingPct smaller, so finishing it would only have thrown hours away.
var errTooBig = errors.New("the encode reached its size cap")

// sizeCap is the largest a re-encode may grow before it's stopped: past it, the result
// would fail the minSavingPct rule anyway. 0 = no cap (a track tidy-up copies the video).
func sizeCap(mi *MediaInfo, plan Plan) int64 {
	if plan.VideoCodec == "" || mi.SizeBytes <= 0 {
		return 0
	}
	return mi.SizeBytes * (100 - minSavingPct) / 100
}

// keptAudioBytes estimates the audio a plan carries into the output (copied, so unchanged).
func keptAudioBytes(mi *MediaInfo, plan Plan) int64 {
	var n int64
	for _, au := range keptAudio(mi, plan) {
		n += audioBytes(au.Codec, au.Channels, mi.DurationSec)
	}
	return n
}

// scratchNeeded is the scratch space a conversion can use at its peak: the whole output for
// a track tidy-up, the size cap (plus muxing slack) for a re-encode, and twice that for the
// HDR10+ pipeline, which holds the stream and its injected copy at once.
func scratchNeeded(mi *MediaInfo, plan Plan, hdr10plus bool) int64 {
	const slack = 512 << 20
	limit := sizeCap(mi, plan)
	switch {
	case limit == 0:
		return mi.SizeBytes + slack
	case hdr10plus:
		return 2*limit + slack
	}
	return limit + slack
}

func savedPct(src, out int64) int {
	if src <= 0 {
		return 0
	}
	return max(int(100-out*100/src), 0)
}

// runEncode runs the encode and reports errTooBig when it was stopped at its size cap.
func (s *Service) runEncode(ctx context.Context, job *Job, src, dst, scratch string, mi *MediaInfo, enc Encoder, plan Plan, h10pJSON string) error {
	err := s.runEncodeOnce(ctx, job, src, dst, scratch, mi, enc, plan, h10pJSON)
	if limit := sizeCap(mi, plan); err == nil && limit > 0 && fileSize(dst) >= limit {
		return errTooBig
	}
	return err
}

// runEncodeOnce dispatches to the standard or HDR10+ pipeline, with one CPU fallback if a
// hardware encoder fails.
func (s *Service) runEncodeOnce(ctx context.Context, job *Job, src, dst, scratch string, mi *MediaInfo, enc Encoder, plan Plan, h10pJSON string) error {
	if h10pJSON != "" && plan.VideoCodec == "hevc" {
		s.update(job, func(j *Job) { j.Encoder = "CPU (x265) + HDR10+" })
		return s.encodeHDR10Plus(ctx, job, src, dst, scratch, mi, plan, h10pJSON)
	}
	// VAAPI: first try a full-GPU pipeline (hardware decode → GPU encode). If the source
	// can't be hardware-decoded, fall back to software decode + GPU encode, then CPU.
	// (Not when removing black bars: the crop runs on system-memory frames.)
	if enc.Kind == "vaapi" && plan.VideoCodec != "" && plan.Crop == nil {
		if err := s.encode(ctx, job, src, dst, mi, enc, plan, true); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return err
		} else {
			s.log.Warn("convert: hardware decode failed, retrying with software decode", "err", err)
			s.update(job, func(j *Job) { j.Progress = 0 })
		}
	}
	err := s.encode(ctx, job, src, dst, mi, enc, plan, false)
	if err != nil && ctx.Err() != nil {
		return err // cancelled — not the hardware's fault
	}
	if err != nil && enc.Hardware && cpuWorks(plan.VideoCodec, s.encoders) {
		cpu := cpuEncoder(plan.VideoCodec)
		s.markHardwareBroken(enc.Name, err.Error())
		s.update(job, func(j *Job) { j.Encoder = cpu.Label; j.Progress = 0 })
		err = s.encode(ctx, job, src, dst, mi, cpu, plan, false)
	}
	return err
}

// encode runs ffmpeg for one job, parsing live progress from the -progress pipe.
func (s *Service) encode(ctx context.Context, job *Job, src, dst string, mi *MediaInfo, enc Encoder, plan Plan, hwDecode bool) error {
	cores := s.cpuCores(ctx)
	// -loglevel warning suppresses the input stream dump, which otherwise floods the
	// retained error tail with metadata lines.
	args := []string{"-y", "-hide_banner", "-nostats", "-loglevel", "warning",
		"-progress", "pipe:1", "-threads", strconv.Itoa(cores)}
	args = append(args, globalArgs(enc, hwDecode, s.vaapiDev(ctx))...)
	args = append(args, "-i", src)
	args = append(args, compileOutputArgs(enc, mi, plan, hwDecode, cores, s.noNumaPools)...)
	if limit := sizeCap(mi, plan); limit > 0 {
		// Stops the encode cleanly once it can no longer save enough (see errTooBig).
		args = append(args, "-fs", strconv.FormatInt(limit, 10))
	}
	args = append(args, dst)

	err := s.runWithProgress(ctx, job, args, mi.DurationSec)
	if err == nil || ctx.Err() != nil {
		return err
	}
	// Safe-mode retry: the tuning parameters vary with the machine in ways that can't all be
	// verified up front. A failure there shouldn't cost the conversion when the plain form
	// would have worked — so try once without them, and say so.
	simple := stripTuningParams(args)
	if slices.Equal(simple, args) {
		return err
	}
	s.log.Warn("convert: encode failed with tuned settings — retrying with plain settings", "title", job.Title, "err", err)
	s.update(job, func(j *Job) { j.Progress = 0 })
	if err2 := s.runWithProgress(ctx, job, simple, mi.DurationSec); err2 != nil {
		return err // report the ORIGINAL failure; the retry is a bonus, not the diagnosis
	}
	s.event("warn", job.Title+": converted with plain encoder settings — the tuned ones failed on this machine")
	return nil
}

// runWithProgress runs an ffmpeg command whose stdout is a -progress stream, updating the
// job live, registering the process so it can be paused, and returning any error with a
// tail of stderr for diagnosis.
func (s *Service) runWithProgress(ctx context.Context, job *Job, args []string, durationSec float64) error {
	cmd := exec.CommandContext(ctx, s.ffmpeg, args...)
	lowPriority(cmd) // own process group: niced, paused and signalled together
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	tail := &lineTail{max: 12}
	if errPipe, e := cmd.StderrPipe(); e == nil {
		go func() {
			sc := bufio.NewScanner(errPipe)
			for sc.Scan() {
				tail.add(sc.Text())
			}
		}()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	applyNice(pid, encodeNice)
	s.trackProc(job, pid)
	defer s.untrackProc(job, pid)
	s.readProgress(job, stdout, durationSec)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%v: %s", err, tail.String())
	}
	return nil
}

// readProgress consumes ffmpeg's -progress key=value stream and updates the job live.
func (s *Service) readProgress(job *Job, r io.Reader, durationSec float64) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "out_time_ms":
			if us, err := strconv.ParseFloat(v, 64); err == nil && durationSec > 0 {
				p := math.Min(1, (us/1e6)/durationSec)
				s.update(job, func(j *Job) { j.Progress = p })
			}
		case "fps":
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				s.update(job, func(j *Job) { j.FPS = f })
			}
		case "speed":
			if sp, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "x"), 64); err == nil {
				s.update(job, func(j *Job) { j.SpeedX = sp })
			}
		}
	}
}

// verifyOutput checks a finished encode before it's allowed anywhere near the library:
// the full length of the original, every track the plan keeps, and — for a re-encode — a
// saving worth a generation of compression. Returns the skip kind and a reason on failure.
func verifyOutput(mi, out *MediaInfo, outSize int64, plan Plan) (kind, reason string) {
	if out == nil || out.VideoCodec == "" {
		return "", "the converted file has no video"
	}
	// The whole film, not "most of it". This was 90%, so an encode that lost its last ten
	// minutes would have replaced the original. Two seconds or 0.1% covers container
	// rounding and nothing more.
	tol := math.Max(2, mi.DurationSec*0.001)
	if mi.DurationSec > 0 && math.Abs(out.DurationSec-mi.DurationSec) > tol {
		return "", fmt.Sprintf("the converted file runs %s instead of %s", clock(out.DurationSec), clock(mi.DurationSec))
	}
	if want := len(keptAudio(mi, plan)); out.AudioTracks != want {
		return "", fmt.Sprintf("the converted file has %d audio track(s), expected %d", out.AudioTracks, want)
	}
	if want := len(keptSubs(mi, plan)); out.SubTracks != want {
		return "", fmt.Sprintf("the converted file has %d subtitle track(s), expected %d", out.SubTracks, want)
	}
	if outSize <= 0 {
		return "", "the converted file is empty"
	}
	if plan.VideoCodec != "" && outSize > mi.SizeBytes*(100-minSavingPct)/100 {
		saved := 0
		if mi.SizeBytes > 0 {
			saved = int(100 - outSize*100/mi.SizeBytes)
		}
		return SkipNotSmaller, fmt.Sprintf("only %d%% smaller — not worth another generation of compression, kept the original", max(saved, 0))
	}
	if plan.VideoCodec == "" && outSize > mi.SizeBytes {
		return SkipNotSmaller, "tidying the tracks made the file bigger — kept the original"
	}
	return "", ""
}

func clock(sec float64) string {
	t := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d:%02d", t/3600, t%3600/60, t%60)
}

// finalizeOutput verifies a freshly-encoded file, then safely replaces the original.
func (s *Service) finalizeOutput(ctx context.Context, job *Job, src, dst string, mi *MediaInfo, plan Plan) {
	s.update(job, func(j *Job) { j.State = StateVerifying; j.Progress = 1 })
	outInfo, err := probeFn(ctx, s.ffprobe, dst)
	if err != nil {
		s.finish(job, StateFailed, "the converted file couldn't be read — kept the original")
		return
	}
	outSize := fileSize(dst)
	if kind, reason := verifyOutput(mi, outInfo, outSize, plan); reason != "" {
		if kind != "" {
			s.finishAfterEncode(job, kind, reason)
		} else {
			s.finish(job, StateFailed, reason+" — kept the original")
		}
		return
	}
	pct := 0
	if mi.SizeBytes > 0 {
		pct = int(100 - outSize*100/mi.SizeBytes)
	}
	s.event("info", fmt.Sprintf("%s — output: %s · %s (%d%% smaller)", job.Title, mediaSpec(outInfo), humanBytes(outSize), pct))

	// Safe replace. The original stays on disk, intact, until the converted file is
	// completely written next to it: copy to a sibling .arrpart, journal the swap, retire
	// the original, then an atomic same-directory rename.
	s.update(job, func(j *Job) { j.State = StateReplacing })
	// The encode may have taken hours: make sure the library file is still the one we
	// converted. An import or upgrade landing mid-encode must not be undone.
	if cur, _, _, ok := s.resolveSource(ctx, job); !ok || cur != src {
		s.finish(job, StateSkipped, "the library file changed during the conversion — left the new file alone")
		return
	}
	if fi, err := os.Stat(src); err != nil || fi.Size() != mi.SizeBytes {
		s.finish(job, StateSkipped, "the source file changed during the conversion — left it alone")
		return
	}
	reclaimDeferred := fileLinks(src) > 1
	finalPath := strings.TrimSuffix(src, filepath.Ext(src)) + ".mkv"
	part := finalPath + ".arrpart"
	_ = os.Remove(part) // a leftover from an interrupted job
	if err := moveFileFn(dst, part); err != nil {
		_ = os.Remove(part)
		if errors.Is(err, syscall.ENOSPC) {
			// The library disk filled up. Retrying straight away would re-run the whole encode
			// only to fail the same way, so wait for space to be freed.
			s.finishSkip(job, SkipLibraryFull, fmt.Sprintf("the library disk had %s free; staging needed %s — the encode was discarded and the original kept",
				humanBytes(int64(freeBytesFn(filepath.Dir(part)))), humanBytes(outSize)))
			return
		}
		s.finish(job, StateFailed, "could not stage the converted file: "+err.Error()+" — kept the original")
		return
	}
	s.recordSwap(job, part, finalPath, src)
	if err := s.retire(src); err != nil {
		_ = os.Remove(part)
		s.clearSwap(part)
		s.finish(job, StateFailed, "could not move the original to the recycle bin: "+err.Error()+" — kept the original")
		return
	}
	if finalPath != src {
		if _, e := os.Stat(finalPath); e == nil {
			if err := s.retire(finalPath); err != nil {
				s.log.Warn("convert: could not retire the file being replaced", "path", finalPath, "err", err)
			}
		}
	}
	if err := os.Rename(part, finalPath); err != nil {
		s.log.Error("convert: converted file is staged but could not be swapped in", "part", part, "final", finalPath, "err", err)
		s.finish(job, StateFailed, "converted file is staged at "+filepath.Base(part)+
			" but could not replace the original — it will be reconciled at the next startup")
		return
	}
	if err := s.markConverted(ctx, job, src, finalPath, plan.VideoCodec); err != nil {
		s.log.Error("convert: library record update failed after the swap", "title", job.Title, "err", err)
		s.finish(job, StateFailed, "converted, but the library record could not be updated: "+err.Error()+
			" — it will be reconciled at the next startup")
		return
	}
	s.clearSwap(part)
	s.measured.forget(ctx, src)
	s.reindexConverted(ctx, job)
	s.update(job, func(j *Job) { j.OutBytes = outSize })
	if reclaimDeferred {
		s.event("info", job.Title+": space reclaim deferred — the download client still holds a hardlinked copy of the original")
	} else {
		s.addReclaimed(ctx, mi.SizeBytes-outSize)
	}
	notes := []string{}
	if t := trackSummary(mi, plan); t != "" {
		notes = append(notes, t)
	}
	for _, w := range planWarnings(mi, plan) {
		s.event("warn", job.Title+": "+w)
		notes = append(notes, w)
	}
	s.finish(job, StateDone, strings.Join(notes, " · "))
	s.log.Info("convert: done", "title", job.Title, "src_mb", mi.SizeBytes>>20, "out_mb", outSize>>20)
}

// markConverted repoints the library record (movie or episode) at the converted file —
// path only. It must never run the import flow: that stamped a synthetic release name and
// set off an endless download → re-encode loop.
func (s *Service) markConverted(ctx context.Context, job *Job, src, finalPath, codec string) error {
	size := fileSize(finalPath)
	token := codecToken(codec)
	if job.Kind == "episode" {
		if s.series == nil {
			return fmt.Errorf("series module not available")
		}
		// One file can serve several episodes ("S03E01E02"). Repoint by PATH so they all
		// follow the conversion.
		if src != "" {
			if n, err := s.series.RepointEpisodeFile(ctx, job.SeriesID, src, finalPath, size); err == nil && n > 0 {
				s.stampEpisodeCodec(ctx, job, token)
				return nil
			}
		}
		if err := s.series.MarkEpisodeImported(ctx, job.SeriesID, job.Season, job.Episode, finalPath, size); err != nil {
			return err
		}
		s.stampEpisodeCodec(ctx, job, token)
		return nil
	}
	n, err := s.movies.RepointMovieFile(ctx, job.MovieID, src, finalPath, size, token)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("no movie file record at %s to repoint", src)
	}
	return nil
}

// codecToken is the release-name token for a conversion target, appended to the recorded
// source release so upgrade scoring costs the shrunken file at the new codec's efficiency.
func codecToken(codec string) string {
	switch codec {
	case "av1":
		return "AV1"
	case "hevc":
		return "x265"
	}
	return ""
}

// stampEpisodeCodec appends the new codec token to the episode's recorded source release.
func (s *Service) stampEpisodeCodec(ctx context.Context, job *Job, token string) {
	if token == "" {
		return
	}
	cur := s.series.CurrentEpisodeFile(ctx, job.SeriesID, job.Season, job.Episode)
	if cur.SourceRelease == "" {
		return
	}
	if parser.Parse(cur.SourceRelease).Codec == parser.Parse("x "+token).Codec {
		return
	}
	_ = s.series.SetEpisodeSourceRelease(ctx, job.SeriesID, job.Season, job.Episode, cur.SourceRelease+" "+token)
}

// errNoClip is returned when a trial couldn't produce any clip.
var errNoClip = errors.New("no test clip could be encoded")
