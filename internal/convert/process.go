package convert

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/toolrun"
)

// Seams over the disks and ffprobe, so tests can stage a full disk or a probed film without
// needing either.
var (
	freeBytesFn = freeBytes
	moveFileFn  = moveFile
	probeFn     = probe
)

// process converts one file: analyse it, decide what it needs, settle the codec, encode,
// check the result against the source, and only then swap it into the library.
func (s *Service) process(ctx context.Context, job *Job) {
	rec := s.record(job) // what the ledger keeps about this job, filled in as it runs
	src, title, origLang, ok := s.resolveSource(ctx, job)
	if !ok {
		s.dropGone(ctx, job)
		return
	}
	rec.srcPath = src
	if title != "" && job.Kind != "episode" {
		s.update(job, func(j *Job) { j.Title = title })
	}
	// Recorded but not on disk: usually mid-import or mid-upgrade. Look again later rather
	// than fail it — a missing file says nothing about whether it can be converted.
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		s.finishSkip(job, SkipSourceGone, "the library file is missing from disk — checking again later")
		return
	}
	// The result is swapped in as <name>.mkv, and a file already there is moved aside. When
	// that file is another version of this film, nothing is encoded: the swap would take it.
	if final := convertedPath(src); final != src && s.otherTrackOwns(ctx, job, final) {
		s.finishSkip(job, SkipTransient, otherTrackNote(final))
		return
	}
	mi, err := probeFn(ctx, s.ffprobe, src)
	if err != nil {
		s.finish(job, StateFailed, "could not analyze file: "+err.Error())
		return
	}
	s.update(job, func(j *Job) { j.SrcBytes = mi.SizeBytes; j.DurationSec = mi.DurationSec })
	rec.srcInfo = mi
	// Read now, before markConverted restamps it: the ledger keeps what the file came from.
	rec.srcRelease = s.sourceRelease(ctx, job, src)

	p := s.prefs(ctx)
	plan, needs := p.planFor(mi, src, origLang, nil)
	rec.setPlan(plan)
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
	// Seeding safety: a hardlinked file is still shared with the downloads folder, so replacing
	// the library copy frees nothing and duplicates the data. Tried again later. The link count
	// can't say why: a torrent seeding, or a finished usenet job whose folder hasn't been
	// cleared yet (retryDelay).
	if fileLinks(src) > 1 {
		s.finishSkip(job, SkipHardlinked, "still linked to your downloads (seeding, or not cleared yet) — will try again later")
		return
	}
	// A file of someone's at the .mkv name (no record owns it) stops the swap (swapIn), so
	// it's seen before hours go into an encode that would only be discarded.
	if final := convertedPath(src); final != src && exists(final) && !nameIsSource(final, src) {
		why := s.nameOwner(ctx, final)
		if why == "" {
			why = strayInTheWay(final)
		}
		if why != "" {
			s.finishSkip(job, SkipNameTaken, why)
			return
		}
	}
	if needs.Video && mi.DVUnconvertible() {
		s.finishSkip(job, SkipHDRUnsupported, needs.Why)
		return
	}
	scratch := s.activeScratch(ctx)
	// Resumable pieces (chunk.go): with the setting on, a re-encode keeps its finished
	// pieces across a restart, and an encode a restart interrupted carries on with the plan
	// it had settled. With it off, any pieces left from when it was on are no use.
	// Keeping Dolby Vision encodes in one go (dv.go).
	var resumed *resumeManifest
	if p.resumable && needs.Video && !plan.KeepDV {
		rec.resume = &resumeRun{cropOn: p.crop}
		resumed = s.loadResume(ctx, job, src, scratch, mi, plan, p)
	} else {
		s.dropResume(ctx, job.Key, "")
	}
	// Room on both disks and in the recycle bin is checked before anything heavy: the HDR10+
	// read, crop detection and the format test all read the film, and a file that can't fit
	// would only fail after them — then be picked again and do it all over.
	if !s.spaceCheck(ctx, job, src, mi, plan, scratch) {
		if resumed != nil && resumed.h10pJSON != "" {
			_ = os.Remove(resumed.h10pJSON)
		}
		return
	}

	h10pJSON := ""
	var enc Encoder
	if needs.Video && resumed != nil {
		// Settled before the restart: the HDR10+ read, the crop detection, the format test
		// and the test encodes aren't run again — a re-run could land on another quality
		// and orphan the pieces.
		plan.VideoCodec, plan.Quality, plan.Crop = resumed.Codec, resumed.CRF, resumed.Crop
		rec.resume.attempt, rec.resume.priorSecs = resumed.Attempt, resumed.ActiveSec
		if resumed.h10pJSON != "" {
			h10pJSON = resumed.h10pJSON
			rec.resume.h10pJSON = h10pJSON
			defer os.Remove(h10pJSON)
		}
		enc = s.pickEncoder(job, mi, plan.VideoCodec, p.useGPU)
		if !s.canPreserveHDR(mi, plan, enc, h10pJSON != "") {
			s.finishSkip(job, SkipHDRUnsupported, mi.EncodeHDR()+" couldn't be carried through a conversion on this machine — kept the original")
			return
		}
		s.event("info", fmt.Sprintf("%s: carrying on with the encode a restart interrupted — %s at CRF %d, as settled before the restart",
			job.Title, strings.ToUpper(plan.VideoCodec), plan.Quality))
	} else if needs.Video {
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
				if !s.scratchFits(job, scratch, s.jobScratchNeed(ctx, job, mi, plan, true, "cpu")) {
					_ = os.Remove(jf)
					return
				}
				if err := s.readHDR10Plus(ctx, src, jf); err != nil {
					_ = os.Remove(jf)
					if ctx.Err() != nil {
						return
					}
					s.hdr10PlusReadFailed(job, err)
					return
				}
				h10pJSON = jf
				if rec.resume != nil {
					rec.resume.h10pJSON = jf // kept with the pieces (chunk.go)
				}
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
	if !s.scratchFits(job, scratch, s.jobScratchNeed(ctx, job, mi, plan, h10pJSON != "", enc.Kind)) {
		return
	}
	s.update(job, func(j *Job) { j.Codec = plan.VideoCodec })
	rec.setPlan(plan)

	// A re-encode is rehearsed on clips first, so a film that wouldn't pass the quality
	// check or wouldn't shrink enough is found out in minutes rather than after a day. The
	// HDR10+ and Dolby Vision pipelines run without B-frames, which the projection allows for.
	if plan.VideoCodec != "" && resumed == nil {
		v, err := s.preflight(ctx, job, src, mi, enc, plan, h10pJSON != "" || plan.KeepDV)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			// Advisory: the full encode is still checked end to end.
			s.event("warn", fmt.Sprintf("%s: the test encode didn't finish (%v) — encoding without it", job.Title, err))
		case v.ran && v.skipKind != "":
			rec.plan.Quality = v.quality // the target the test encodes ended on
			s.finishSkip(job, v.skipKind, v.reason)
			return
		case v.ran:
			plan.Quality = v.quality
			s.event("info", fmt.Sprintf("%s: test encode at CRF %d scored SSIM %.4f and predicts ~%s (%d%% smaller)",
				job.Title, v.quality, v.ssim, humanBytes(v.projected), savedPct(mi.SizeBytes, v.projected)))
		}
	}

	// Keeping Dolby Vision: its metadata is read now, after the rehearsal — reading it means
	// reading the whole film, which a file the rehearsal turns away shouldn't cost.
	if plan.KeepDV {
		if !s.waitAllowed(ctx, job) {
			return
		}
		dv, ok := s.readDolbyVision(ctx, job, src, mi, scratch)
		if !ok {
			return
		}
		defer os.Remove(dv.rpu)
		rec.dv = dv
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
	rec.setPlan(plan)
	s.beginHistory(job)

	startAttempt := 0
	if resumed != nil {
		startAttempt = min(resumed.Attempt, qualityRetries) // a retry before the restart still counts
	}
	for attempt := startAttempt; ; attempt++ {
		if rec.resume != nil {
			rec.resume.attempt = attempt
		}
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
			if errors.Is(err, errDVMismatch) {
				// Fail closed: the same source encodes to the same frames every time.
				s.finishAfterEncode(job, SkipHDRUnsupported, err.Error()+" — kept the original")
				return
			}
			s.finish(job, StateFailed, "encode failed: "+err.Error())
			return
		}
		if plan.VideoCodec == "" {
			break // a copy can't lose quality
		}
		s.update(job, func(j *Job) { j.State = StateVerifying })
		if !s.waitAllowed(ctx, job) {
			return
		}
		s.event("info", fmt.Sprintf("Checking %s against the original…", job.Title))
		// The 25 minutes count only while the check runs: frozen for a Plex stream, it
		// mustn't time out and cost the encode.
		sctx, cancel := s.pausableTimeout(ctx, job, 25*time.Minute)
		res, err := s.computeSSIM(sctx, dst, src, plan.Crop.filter())
		cancel()
		rec.ssimMean, rec.ssimWindows = res.Mean, res.Scores
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// FAIL CLOSED: an unmeasurable result is not a pass. Keep the original, and count
			// it toward the failure limit (the outcome is the same every time for this file).
			s.log.Warn("convert: quality check could not measure SSIM — keeping the original", "err", err)
			s.event("warn", fmt.Sprintf("%s: quality check couldn't finish (%v)", job.Title, err))
			s.finishAfterEncode(job, SkipQualityGate, "couldn't verify the encode matched the original — kept the original")
			return
		}
		s.update(job, func(j *Job) { j.SSIM = res.Mean })
		if res.passes() {
			s.event("info", fmt.Sprintf("%s: quality check passed — %s", job.Title, res.summary()))
			break
		}
		next, ok := higherQuality(plan.VideoCodec, plan.Quality)
		if !ok || attempt >= qualityRetries {
			s.event("warn", fmt.Sprintf("%s: quality check failed — %s", job.Title, res.summary()))
			s.finishAfterEncode(job, SkipQualityGate, fmt.Sprintf("couldn't reach the quality bar after %d tries: %s — kept the original", attempt+1, res.shortfall()))
			return
		}
		plan.Quality = next
		rec.setPlan(plan)
		if !s.waitAllowed(ctx, job) {
			return
		}
		s.event("warn", fmt.Sprintf("%s: %s — encoding again at higher quality (try %d; %s)", job.Title, res.shortfall(), attempt+2, res.summary()))
		s.update(job, func(j *Job) { j.State = StateEncoding; j.Progress = 0 })
	}
	rec.encodeEnd = time.Now()
	s.finalizeOutput(ctx, job, src, dst, mi, plan)
}

// sourceRelease is the release name the library recorded for the file being converted, or
// "" when it has none (or the record now points at another file).
func (s *Service) sourceRelease(ctx context.Context, job *Job, src string) string {
	if job.Kind == "episode" {
		if s.series == nil {
			return ""
		}
		if f := s.series.CurrentEpisodeFile(ctx, job.SeriesID, job.Season, job.Episode); f.Path == src {
			return f.SourceRelease
		}
		return ""
	}
	if t, ok := s.lookupMovieTrack(ctx, job.MovieID, job.VersionID); ok && t.version.FilePath == src {
		return t.version.SourceRelease
	}
	return ""
}

// dropGone ends a job whose title or episode no longer has a file recorded at all — deleted
// since the index was built. That won't change by waiting, so nothing is recorded in
// Problems; the stale index row goes instead, so the runner stops picking it. A new import
// reindexes the item and brings it back.
func (s *Service) dropGone(ctx context.Context, job *Job) {
	s.skips.clear(ctx, job.Key)
	if s.index != nil {
		s.index.forget(ctx, job)
	}
	s.finish(job, StateSkipped, "no longer has a file in the library — removed from the list")
}

// hdr10PlusReadFailed ends a job whose full HDR10+ read failed after the 100-frame look
// found it. A full scratch disk or a read error off the array clears on its own, so those
// wait and try again; only a failure of the tool itself means the metadata can't be carried.
func (s *Service) hdr10PlusReadFailed(job *Job, err error) {
	switch {
	case errors.Is(err, syscall.ENOSPC) || transientFailure(err.Error()):
		s.finishSkip(job, SkipNoScratch, "the scratch disk filled up while reading the HDR10+ metadata — will try again later")
	case errors.Is(err, errSourceStream):
		// Usually the disk, but a damaged file fails the same way every time: counting it
		// toward the failure limit stops a daily whole-file read of a file that never reads.
		s.finishAfterEncode(job, SkipTransient, "couldn't read the whole file for its HDR10+ metadata — will try again later")
	default:
		// It has HDR10+ we couldn't read in full: converting would drop it.
		s.finishSkip(job, SkipHDRUnsupported, "HDR10+ metadata couldn't be read — kept the original rather than lose it")
	}
}

// spaceCheck makes sure there's room for this file before any heavy work, and skips it with
// a wait when there isn't. The original will be kept in the originals hold (holdRoom). The
// scratch folder holds the encode, and the finished file is then put next to the original.
// The library disk is always checked too: two mounts of one share (Unraid's /mnt/user, say)
// can't rename into each other, so the hand-in is a full copy even when both report the
// same disk — and on a genuinely shared disk this is the figure scratch already passed.
func (s *Service) spaceCheck(ctx context.Context, job *Job, src string, mi *MediaInfo, plan Plan, scratch string) bool {
	if !s.holdRoom(ctx, job, src, mi) {
		return false
	}
	// The library disk takes only the finished file, whatever the pipeline held on the way.
	finished := plan
	finished.KeepDV = false
	need := scratchNeeded(mi, finished, false)
	encKind := ""
	if s.record(job).resume != nil {
		encKind = s.encoderChoice(mi, plan.VideoCodec, s.prefs(ctx).useGPU).Kind // provisional
	}
	if !s.scratchFits(job, scratch, s.jobScratchNeed(ctx, job, mi, plan, false, encKind)) {
		return false
	}
	dir := filepath.Dir(src)
	if free := freeBytesFn(dir); free > 0 && int64(free) < need {
		s.noteStall(job, need, int64(free))
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
		s.noteStall(job, need, int64(free))
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
	// The job's own track — the default file, or the extra version it names — and never
	// another track's file: a version job whose version is gone is gone, not the default.
	t, ok := s.lookupMovieTrack(ctx, job.MovieID, job.VersionID)
	if !ok {
		return "", "", "", false
	}
	return t.version.FilePath, t.title(), movieOrigLang(t.movie), true
}

// chooseCodec settles the format for one file:
//
//   - HDR10+ → HEVC: it's the only pipeline that can carry the dynamic metadata.
//   - Dolby Vision being kept → HEVC, for the same reason (dv.go).
//   - AV1 not allowed (your devices can't all play it) → HEVC.
//   - Otherwise a side-by-side test: the same few clips encoded both ways and scored
//     against the original. AV1 only wins when it's clearly smaller at the same quality —
//     on dark or grainy footage it often isn't, and then HEVC is the better file.
func (s *Service) chooseCodec(ctx context.Context, job *Job, src string, mi *MediaInfo, plan Plan, p prefs, hasHDR10Plus bool) string {
	switch {
	case hasHDR10Plus, plan.KeepDV: // dovi_tool, like hdr10plus_tool, writes HEVC only
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
// HDR10+ only by the HEVC pipeline, and a Dolby Vision layer being kept only by the HEVC
// pipeline with dovi_tool. Hardware encoders can't write HDR metadata, which is why
// pickEncoder sends HDR to the CPU.
func (s *Service) canPreserveHDR(mi *MediaInfo, plan Plan, enc Encoder, hasHDR10Plus bool) bool {
	if plan.VideoCodec == "" {
		return true
	}
	if mi.DVUnconvertible() {
		return false
	}
	// Keeping Dolby Vision: the x265 raw-stream pipeline and dovi_tool, or nothing.
	if plan.KeepDV && (s.doviTool == "" || plan.VideoCodec != "hevc" || enc.Kind != "cpu" || !cpuWorks("hevc", s.encoders)) {
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
// HDR10+ pipeline, which holds the stream and its injected copy at once — or a pieced encode
// (chunk.go), which holds the pieces and the joined film; both pass twoCopies. Keeping Dolby
// Vision (dv.go) holds two copies the same way, so its plan always counts twice.
func scratchNeeded(mi *MediaInfo, plan Plan, twoCopies bool) int64 {
	const slack = 512 << 20
	limit := sizeCap(mi, plan)
	switch {
	case limit == 0:
		return mi.SizeBytes + slack
	case twoCopies || plan.KeepDV:
		return 2*limit + slack
	}
	return limit + slack
}

// jobScratchNeed is scratchNeeded for one job. A job running with resumable pieces on needs
// the two copies of a pieced encode when it will be pieced (encKind is the encoder it runs
// on), less the pieces already in its resume folder: they're on the disk already, and the
// free figure has counted them.
func (s *Service) jobScratchNeed(ctx context.Context, job *Job, mi *MediaInfo, plan Plan, hdr10plus bool, encKind string) int64 {
	if s.record(job).resume == nil {
		return scratchNeeded(mi, plan, hdr10plus)
	}
	pieced := s.piecedSpans(job, mi, Encoder{Kind: encKind}, plan) != nil
	return max(scratchNeeded(mi, plan, hdr10plus || pieced)-s.resumeBytes(ctx)[job.Key], 0)
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
	if plan.KeepDV {
		// Never a fallback to the standard encode: that would drop the layer you chose to keep.
		label := "CPU (x265) + Dolby Vision 8.1"
		if h10pJSON != "" {
			label = "CPU (x265) + HDR10+ + Dolby Vision 8.1"
		}
		s.update(job, func(j *Job) { j.Encoder = label })
		return s.encodeDolbyVision(ctx, job, src, dst, scratch, mi, plan, h10pJSON, s.record(job).dv)
	}
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
	if spans := s.piecedSpans(job, mi, enc, plan); spans != nil {
		// In pieces a restart can't throw away (chunk.go). Should that fail for any reason
		// but its size cap, the film is encoded in one go as before.
		err := s.encodeResumable(ctx, job, src, dst, mi, enc, plan, spans)
		if err == nil || ctx.Err() != nil || errors.Is(err, errTooBig) {
			return err
		}
		s.piecesFailed(ctx, job, err)
	}
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
	return s.runWithProgressSpan(ctx, job, args, durationSec, 0, 1)
}

// runWithProgressSpan is runWithProgress for a command that covers only base..base+span
// of the job's progress (one piece of a pieced encode, chunk.go).
func (s *Service) runWithProgressSpan(ctx context.Context, job *Job, args []string, durationSec, base, span float64) error {
	cmd := s.tool(ctx, job, toolrun.Background, s.ffmpeg, args...) // niced, paused and killed as a group
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
	s.readProgress(job, stdout, durationSec, base, span)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%v: %s", err, tail.String())
	}
	return nil
}

// readProgress consumes ffmpeg's -progress key=value stream and updates the job live,
// mapping the command's own progress onto base..base+span of the job's.
func (s *Service) readProgress(job *Job, r io.Reader, durationSec, base, span float64) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		s.tickActive(job) // encoding time for the forecast and the ledger (pauses excluded)
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "out_time_ms":
			if us, err := strconv.ParseFloat(v, 64); err == nil && durationSec > 0 {
				p := base + span*math.Min(1, (us/1e6)/durationSec)
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
	if plan.VideoCodec == "" && outSize > mi.SizeBytes+mi.SizeBytes*tidyGrowthPct/100 {
		return SkipNotSmaller, fmt.Sprintf("tidying the tracks made the file more than %d%% bigger — kept the original", tidyGrowthPct)
	}
	return "", ""
}

// tidyGrowthPct is how much bigger a tracks-only tidy may come out. Rewriting the container can
// add a little even with tracks removed (#98): small subtitle tracks free less than the new
// index and headers take. The video is copied, so this is never a loss of quality.
const tidyGrowthPct = 1

func clock(sec float64) string {
	t := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d:%02d", t/3600, t%3600/60, t%60)
}

// finalizeOutput verifies a freshly-encoded file, then safely replaces the original.
func (s *Service) finalizeOutput(ctx context.Context, job *Job, src, dst string, mi *MediaInfo, plan Plan) {
	s.update(job, func(j *Job) { j.State = StateVerifying; j.Progress = 1 })
	rec := s.record(job)
	rec.setPlan(plan)
	outInfo, err := probeFn(ctx, s.ffprobe, dst)
	if err != nil {
		s.finish(job, StateFailed, "the converted file couldn't be read — kept the original")
		return
	}
	outSize := fileSize(dst)
	rec.outInfo, rec.outSize = outInfo, outSize
	if kind, reason := verifyOutput(mi, outInfo, outSize, plan); reason != "" {
		if kind != "" {
			s.finishAfterEncode(job, kind, reason)
		} else {
			s.finish(job, StateFailed, reason+" — kept the original")
		}
		return
	}
	// A Dolby Vision layer you chose to keep is really there, one RPU per frame (dv.go).
	if plan.KeepDV {
		if !s.waitAllowed(ctx, job) {
			return
		}
		kind, reason := s.checkDolbyVision(ctx, job, dst, outInfo, rec.dv)
		if ctx.Err() != nil {
			return
		}
		if reason != "" {
			s.event("warn", fmt.Sprintf("%s: Dolby Vision check failed — %s", job.Title, reason))
			s.finishAfterEncode(job, kind, reason+" — kept the original")
			return
		}
	}
	// Every frame of the exact file about to go in decodes cleanly, or the original stays.
	if !s.checkBeforeSwap(ctx, job, src, dst, mi, outInfo, plan) {
		return
	}
	pct := 0
	if mi.SizeBytes > 0 {
		pct = int(100 - outSize*100/mi.SizeBytes)
	}
	s.event("info", fmt.Sprintf("%s — output: %s · %s (%d%% smaller)", job.Title, mediaSpec(outInfo), humanBytes(outSize), pct))

	// The swap itself — journalled, with the original kept in the hold — is swapin.go.
	s.swapIn(ctx, job, src, dst, mi, plan, outSize)
}

// markConverted repoints the library record (movie or episode) at the converted file —
// path only. It must never run the import flow: that stamped a synthetic release name and
// set off an endless download → re-encode loop.
//
// Before the repoint it records what the file was (its release, still naming the old codec,
// and srcSize, 0 when unknown) as the pre-conversion baseline, so a quality upgrade has to
// beat the original rather than the smaller converted file — or the sweep re-grabs the
// very kind of release this just shrank. A record that already has a baseline keeps it.
func (s *Service) markConverted(ctx context.Context, job *Job, src, finalPath, codec string, srcSize int64) error {
	size := fileSize(finalPath)
	token := codecToken(codec)
	if job.Kind == "episode" {
		if s.series == nil {
			return fmt.Errorf("series module not available")
		}
		if src != "" {
			if err := s.series.SetConvertedFrom(ctx, job.SeriesID, src, srcSize); err != nil {
				s.log.Warn("convert: could not record the pre-conversion baseline", "title", job.Title, "err", err)
			}
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
	if src != "" {
		if err := s.movies.SetConvertedFrom(ctx, job.MovieID, src, srcSize); err != nil {
			s.log.Warn("convert: could not record the pre-conversion baseline", "title", job.Title, "err", err)
		}
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

// codecToken is the release-name token for a conversion target, swapped into the recorded
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

// stampEpisodeCodec puts the new codec token into the episode's recorded source release,
// in place of the old one (parser.RestampCodec), so it reads as the new codec and keeps
// its release group.
func (s *Service) stampEpisodeCodec(ctx context.Context, job *Job, token string) {
	if token == "" {
		return
	}
	cur := s.series.CurrentEpisodeFile(ctx, job.SeriesID, job.Season, job.Episode)
	if cur.SourceRelease == "" {
		return
	}
	codec := parser.Parse("x " + token).Codec
	if parser.Parse(cur.SourceRelease).Codec == codec {
		return
	}
	_ = s.series.SetEpisodeSourceRelease(ctx, job.SeriesID, job.Season, job.Episode, parser.RestampCodec(cur.SourceRelease, codec))
}

// errNoClip is returned when a trial couldn't produce any clip.
var errNoClip = errors.New("no test clip could be encoded")
